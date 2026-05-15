import logging
import os
import sqlite3
from contextlib import contextmanager
from datetime import datetime, timezone
from typing import Callable

import sqlite_vec

logger = logging.getLogger(__name__)

DATA_DIR = os.getenv("DATA_DIR", "/data")
DB_PATH = os.path.join(DATA_DIR, "ieops-mem.db")

TARGET_VERSION = "0.3.0"

_CREATE_MEMORIES = """
CREATE TABLE IF NOT EXISTS memories (
    id                TEXT PRIMARY KEY,
    project           TEXT NOT NULL,
    type              TEXT NOT NULL,
    content           TEXT NOT NULL,
    metadata          TEXT NOT NULL DEFAULT '{}',
    embedding         BLOB,
    created_at        TEXT NOT NULL,
    updated_at        TEXT NOT NULL,
    expires_at        TEXT,
    deprecated        INTEGER NOT NULL DEFAULT 0,
    deprecated_reason TEXT,
    superseded_by     TEXT,
    author_key_id     TEXT,
    showable          INTEGER NOT NULL DEFAULT 1
)
"""

_CREATE_MEMORIES_IDX = """
CREATE INDEX IF NOT EXISTS idx_memories_filter
ON memories(project, type, deprecated, expires_at)
"""

_CREATE_ACCESS = """
CREATE TABLE IF NOT EXISTS access (
    key_id   TEXT PRIMARY KEY,
    key_hash TEXT NOT NULL,
    key_hint TEXT NOT NULL,
    project  TEXT NOT NULL,
    role     TEXT NOT NULL
)
"""

_CREATE_ACCESS_IDX = """
CREATE UNIQUE INDEX IF NOT EXISTS idx_access_key_project
ON access(key_hash, project)
"""

_CREATE_MIGRATION_STATE = """
CREATE TABLE IF NOT EXISTS _migration_state (
    target_version TEXT PRIMARY KEY,
    step           INTEGER NOT NULL,
    started_at     TEXT NOT NULL,
    completed_at   TEXT
)
"""


def _utcnow_iso() -> str:
    return datetime.now(timezone.utc).strftime("%Y-%m-%dT%H:%M:%S.%f")[:-3] + "Z"


def _load_vec_extension(conn: sqlite3.Connection) -> None:
    """Enable extension loading for one call, load sqlite-vec, then lock off."""
    conn.enable_load_extension(True)
    try:
        sqlite_vec.load(conn)
    finally:
        conn.enable_load_extension(False)


def _read_step(conn: sqlite3.Connection) -> int:
    row = conn.execute(
        "SELECT step FROM _migration_state WHERE target_version = ?",
        (TARGET_VERSION,),
    ).fetchone()
    return int(row[0]) if row else 0


def _start_migration(conn: sqlite3.Connection) -> None:
    conn.execute(
        """INSERT INTO _migration_state(target_version, step, started_at, completed_at)
           VALUES (?, 0, ?, NULL)
           ON CONFLICT(target_version) DO NOTHING""",
        (TARGET_VERSION, _utcnow_iso()),
    )


def _advance(conn: sqlite3.Connection, step: int) -> None:
    completed_at = _utcnow_iso() if step == 6 else None
    conn.execute(
        "UPDATE _migration_state SET step = ?, completed_at = ? WHERE target_version = ?",
        (step, completed_at, TARGET_VERSION),
    )


# Step implementations registered below by Tasks 4-7 will append here.
_MIGRATION_STEPS: list[tuple[int, Callable[[sqlite3.Connection], None]]] = []


def _run_migration(conn: sqlite3.Connection) -> None:
    _start_migration(conn)
    current = _read_step(conn)
    for step_num, fn in _MIGRATION_STEPS:
        if current >= step_num:
            continue
        logger.info("migration[v%s] running step %d", TARGET_VERSION, step_num)
        fn(conn)
        _advance(conn, step_num)
        conn.commit()


def _step_1_create_vec_memories(conn: sqlite3.Connection) -> None:
    conn.execute(
        "CREATE VIRTUAL TABLE IF NOT EXISTS vec_memories USING vec0(embedding float[384])"
    )


def _embed_passage_sync(content: str):
    """Sync embed path for migration — bypasses asyncio wrapper to stay
    safe inside FastAPI lifespan (no nested event loops)."""
    import embedder
    import numpy as np
    model = embedder.get_model()
    vecs = list(model.embed([content]))
    return vecs[0].astype(np.float32)


def _step_2_reindex_into_vec(conn: sqlite3.Connection) -> None:
    # Find memories.rowid not yet in vec_memories. INSERT OR IGNORE is the
    # idempotency guarantee — resumed runs after partial completion just
    # fill the missing rowids.
    rows = conn.execute(
        """SELECT m.rowid, m.content FROM memories m
           LEFT JOIN vec_memories v ON v.rowid = m.rowid
           WHERE v.rowid IS NULL"""
    ).fetchall()
    if not rows:
        return
    for rowid, content in rows:
        vec = _embed_passage_sync(content)
        conn.execute(
            "INSERT OR IGNORE INTO vec_memories(rowid, embedding) VALUES (?, ?)",
            (rowid, vec.tobytes()),
        )


_MIGRATION_STEPS.append((1, _step_1_create_vec_memories))
_MIGRATION_STEPS.append((2, _step_2_reindex_into_vec))


_CREATE_FTS_TRIGGERS = """
CREATE TRIGGER IF NOT EXISTS memories_fts_ai AFTER INSERT ON memories BEGIN
    INSERT INTO memories_fts(rowid, content) VALUES (new.rowid, new.content);
END;
CREATE TRIGGER IF NOT EXISTS memories_fts_au AFTER UPDATE OF content ON memories BEGIN
    INSERT INTO memories_fts(memories_fts, rowid, content) VALUES('delete', old.rowid, old.content);
    INSERT INTO memories_fts(rowid, content) VALUES (new.rowid, new.content);
END;
CREATE TRIGGER IF NOT EXISTS memories_fts_ad AFTER DELETE ON memories BEGIN
    INSERT INTO memories_fts(memories_fts, rowid, content) VALUES('delete', old.rowid, old.content);
END;
"""


def _step_3_create_memories_fts(conn: sqlite3.Connection) -> None:
    conn.execute(
        """CREATE VIRTUAL TABLE IF NOT EXISTS memories_fts USING fts5(
               content,
               content='memories', content_rowid='rowid',
               tokenize='porter unicode61'
           )"""
    )
    conn.executescript(_CREATE_FTS_TRIGGERS)


def _step_4_rebuild_memories_fts(conn: sqlite3.Connection) -> None:
    conn.execute("INSERT INTO memories_fts(memories_fts) VALUES('rebuild')")


_MIGRATION_STEPS.append((3, _step_3_create_memories_fts))
_MIGRATION_STEPS.append((4, _step_4_rebuild_memories_fts))


def _step_5_integrity_check(conn: sqlite3.Connection) -> None:
    # FTS5 built-in: raises if external-content shadow has diverged.
    try:
        conn.execute("INSERT INTO memories_fts(memories_fts) VALUES('integrity-check')")
    except sqlite3.DatabaseError as e:
        logger.warning("memories_fts diverged (%s); rebuilding", e)
        conn.execute("INSERT INTO memories_fts(memories_fts) VALUES('rebuild')")
        conn.execute("INSERT INTO memories_fts(memories_fts) VALUES('integrity-check')")

    # Set-equality check via two EXCEPTs — catches counts-equal-but-rowids-differ.
    missing = conn.execute(
        "SELECT rowid FROM memories EXCEPT SELECT rowid FROM vec_memories LIMIT 1"
    ).fetchone()
    orphan = conn.execute(
        "SELECT rowid FROM vec_memories EXCEPT SELECT rowid FROM memories LIMIT 1"
    ).fetchone()
    if missing or orphan:
        raise RuntimeError(
            f"vec_memories integrity failure: missing={missing} orphan={orphan}"
        )


def _step_6_drop_embedding_column(conn: sqlite3.Connection) -> None:
    cols = [r[1] for r in conn.execute("PRAGMA table_info(memories)").fetchall()]
    if "embedding" in cols:
        conn.execute("ALTER TABLE memories DROP COLUMN embedding")


_MIGRATION_STEPS.append((5, _step_5_integrity_check))
_MIGRATION_STEPS.append((6, _step_6_drop_embedding_column))


def init_db() -> None:
    os.makedirs(DATA_DIR, exist_ok=True)
    with sqlite3.connect(DB_PATH) as conn:
        conn.row_factory = sqlite3.Row
        _load_vec_extension(conn)
        conn.execute("PRAGMA journal_mode=WAL")
        conn.execute(_CREATE_MEMORIES)
        conn.execute(_CREATE_MEMORIES_IDX)
        conn.execute(_CREATE_ACCESS)
        conn.execute(_CREATE_ACCESS_IDX)
        conn.execute(_CREATE_MIGRATION_STATE)
        conn.commit()
        _run_migration(conn)


@contextmanager
def get_db():
    conn = sqlite3.connect(DB_PATH)
    conn.row_factory = sqlite3.Row
    _load_vec_extension(conn)
    conn.execute("PRAGMA journal_mode=WAL")
    try:
        yield conn
        conn.commit()
    except Exception:
        conn.rollback()
        raise
    finally:
        conn.close()
