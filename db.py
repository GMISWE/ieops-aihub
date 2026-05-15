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
    sqlite_vec.load(conn)
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
