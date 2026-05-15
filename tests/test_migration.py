import os
import sqlite3
import tempfile

import pytest

import db


@pytest.fixture
def tmp_db(monkeypatch):
    tmpdir = tempfile.mkdtemp()
    monkeypatch.setattr(db, "DATA_DIR", tmpdir)
    monkeypatch.setattr(db, "DB_PATH", os.path.join(tmpdir, "test.db"))
    yield


def test_migration_state_table_created(tmp_db):
    db.init_db()
    with sqlite3.connect(db.DB_PATH) as conn:
        rows = conn.execute(
            "SELECT name FROM sqlite_master WHERE type='table' AND name='_migration_state'"
        ).fetchall()
    assert len(rows) == 1


def test_migration_state_target_v030_at_step_6_after_fresh_init(tmp_db):
    db.init_db()
    with sqlite3.connect(db.DB_PATH) as conn:
        row = conn.execute(
            "SELECT step, completed_at FROM _migration_state WHERE target_version = ?",
            ("0.3.0",),
        ).fetchone()
    assert row is not None
    assert row[0] == 6  # all steps complete
    assert row[1] is not None


def test_step_2_reindexes_existing_rows_into_vec_memories(tmp_db):
    # Seed pre-v0.3.0 schema: just memories table with embedding BLOB.
    import sqlite3
    os.makedirs(os.path.dirname(db.DB_PATH), exist_ok=True)
    conn = sqlite3.connect(db.DB_PATH)
    conn.executescript("""
        CREATE TABLE memories (
            id TEXT PRIMARY KEY, project TEXT NOT NULL, type TEXT NOT NULL,
            content TEXT NOT NULL, metadata TEXT NOT NULL DEFAULT '{}',
            embedding BLOB, created_at TEXT NOT NULL, updated_at TEXT NOT NULL,
            expires_at TEXT, deprecated INTEGER NOT NULL DEFAULT 0,
            deprecated_reason TEXT, superseded_by TEXT,
            author_key_id TEXT, showable INTEGER NOT NULL DEFAULT 1
        );
    """)
    conn.execute(
        "INSERT INTO memories(id,project,type,content,created_at,updated_at) VALUES (?,?,?,?,?,?)",
        ("mem-test-1", "p", "note", "alpha bravo charlie", "2026-01-01T00:00:00.000Z", "2026-01-01T00:00:00.000Z"),
    )
    conn.commit()
    conn.close()

    db.init_db()

    with sqlite3.connect(db.DB_PATH) as conn:
        db._load_vec_extension(conn)
        n = conn.execute("SELECT COUNT(*) FROM vec_memories").fetchone()[0]
    assert n == 1


def test_step_4_populates_memories_fts_from_existing(tmp_db):
    import sqlite3
    os.makedirs(os.path.dirname(db.DB_PATH), exist_ok=True)
    conn = sqlite3.connect(db.DB_PATH)
    conn.executescript("""
        CREATE TABLE memories (
            id TEXT PRIMARY KEY, project TEXT NOT NULL, type TEXT NOT NULL,
            content TEXT NOT NULL, metadata TEXT NOT NULL DEFAULT '{}',
            embedding BLOB, created_at TEXT NOT NULL, updated_at TEXT NOT NULL,
            expires_at TEXT, deprecated INTEGER NOT NULL DEFAULT 0,
            deprecated_reason TEXT, superseded_by TEXT,
            author_key_id TEXT, showable INTEGER NOT NULL DEFAULT 1
        );
    """)
    conn.execute(
        "INSERT INTO memories(id,project,type,content,created_at,updated_at) VALUES (?,?,?,?,?,?)",
        ("mem-test-1", "p", "note", "alpha bravo charlie", "2026-01-01T00:00:00.000Z", "2026-01-01T00:00:00.000Z"),
    )
    conn.commit()
    conn.close()

    db.init_db()

    with sqlite3.connect(db.DB_PATH) as conn:
        db._load_vec_extension(conn)
        rows = conn.execute(
            "SELECT rowid FROM memories_fts WHERE memories_fts MATCH 'bravo'"
        ).fetchall()
    assert len(rows) == 1
