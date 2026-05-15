import os
import sqlite3
from contextlib import contextmanager

DATA_DIR = os.getenv("DATA_DIR", "/data")
DB_PATH = os.path.join(DATA_DIR, "ieops-mem.db")

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
    superseded_by     TEXT
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


def init_db() -> None:
    os.makedirs(DATA_DIR, exist_ok=True)
    with sqlite3.connect(DB_PATH) as conn:
        conn.execute("PRAGMA journal_mode=WAL")
        conn.execute(_CREATE_MEMORIES)
        conn.execute(_CREATE_MEMORIES_IDX)
        conn.execute(_CREATE_ACCESS)
        conn.execute(_CREATE_ACCESS_IDX)
        conn.commit()


@contextmanager
def get_db():
    conn = sqlite3.connect(DB_PATH)
    conn.row_factory = sqlite3.Row
    conn.execute("PRAGMA journal_mode=WAL")
    try:
        yield conn
        conn.commit()
    except Exception:
        conn.rollback()
        raise
    finally:
        conn.close()
