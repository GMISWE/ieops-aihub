"""FTS5 external-content divergence tests.

Two scenarios:

1. test_trigger_guards_fts_sync — verifies the after-update trigger keeps FTS5
   in sync; dropping it causes silent divergence (stale index).

2. test_rebuild_repairs_stale_fts — verifies that calling FTS5 `rebuild`
   repairs a diverged index so the updated content becomes searchable.

3. test_vec_memories_count_mismatch_raises — verifies _step_5_integrity_check
   raises RuntimeError when vec_memories row count != memories row count,
   which is the actual runtime guard in db.py.
"""
import os
import sqlite3
import tempfile

import numpy as np
import pytest

import db


@pytest.fixture
def tmp_db(monkeypatch):
    """Isolated DB in a throwaway tmpdir (mirrors test_migration.py fixture)."""
    tmpdir = tempfile.mkdtemp()
    monkeypatch.setattr(db, "DATA_DIR", tmpdir)
    monkeypatch.setattr(db, "DB_PATH", os.path.join(tmpdir, "test.db"))
    yield


def _insert_memory_with_vec(conn, mem_id, content):
    """Insert a memory row and matching vec_memories entry."""
    conn.execute(
        "INSERT INTO memories(id, project, type, content, created_at, updated_at) "
        "VALUES (?, 'p', 'n', ?, '2026-01-01T00:00:00.000Z', '2026-01-01T00:00:00.000Z')",
        (mem_id, content),
    )
    conn.commit()
    rowid = conn.execute("SELECT rowid FROM memories WHERE id = ?", (mem_id,)).fetchone()[0]
    vec = np.random.RandomState(abs(hash(mem_id)) % (2**31)).randn(384).astype(np.float32)
    vec /= np.linalg.norm(vec)
    conn.execute(
        "INSERT INTO vec_memories(rowid, embedding) VALUES (?, ?)",
        (rowid, vec.tobytes()),
    )
    conn.commit()
    return rowid


def test_trigger_guards_fts_sync(tmp_db):
    """After-update trigger keeps FTS5 in sync; dropping it causes divergence."""
    db.init_db()

    with sqlite3.connect(db.DB_PATH) as conn:
        db._load_vec_extension(conn)
        _insert_memory_with_vec(conn, "mem-a", "hello world")

        # Verify trigger is working: FTS should find 'hello'.
        rows = conn.execute(
            "SELECT rowid FROM memories_fts WHERE memories_fts MATCH 'hello'"
        ).fetchall()
        assert len(rows) == 1, "FTS should index 'hello' via after-insert trigger"

        # Drop the after-update trigger, then silently mutate content.
        conn.execute("DROP TRIGGER memories_fts_au")
        conn.execute(
            "UPDATE memories SET content = 'goodbye world' WHERE id = 'mem-a'"
        )
        conn.commit()

    # FTS is now stale: still matches old content, not new content.
    with sqlite3.connect(db.DB_PATH) as conn:
        db._load_vec_extension(conn)
        hello_rows = conn.execute(
            "SELECT rowid FROM memories_fts WHERE memories_fts MATCH 'hello'"
        ).fetchall()
        goodbye_rows = conn.execute(
            "SELECT rowid FROM memories_fts WHERE memories_fts MATCH 'goodbye'"
        ).fetchall()

    assert len(hello_rows) >= 1, "stale 'hello' should still match in diverged FTS"
    assert len(goodbye_rows) == 0, "'goodbye' should NOT match in diverged FTS"


def test_rebuild_repairs_stale_fts(tmp_db):
    """FTS5 rebuild re-reads from the content table and fixes diverged index."""
    db.init_db()

    with sqlite3.connect(db.DB_PATH) as conn:
        db._load_vec_extension(conn)
        _insert_memory_with_vec(conn, "mem-b", "hello world")

        conn.execute("DROP TRIGGER memories_fts_au")
        conn.execute(
            "UPDATE memories SET content = 'goodbye world' WHERE id = 'mem-b'"
        )
        conn.commit()

        # Manually trigger FTS5 rebuild (same operation as _step_5_integrity_check).
        conn.execute("INSERT INTO memories_fts(memories_fts) VALUES('rebuild')")
        conn.commit()

    # After rebuild, 'goodbye' must be searchable.
    with sqlite3.connect(db.DB_PATH) as conn:
        db._load_vec_extension(conn)
        goodbye_rows = conn.execute(
            "SELECT rowid FROM memories_fts WHERE memories_fts MATCH 'goodbye'"
        ).fetchall()
        hello_rows = conn.execute(
            "SELECT rowid FROM memories_fts WHERE memories_fts MATCH 'hello'"
        ).fetchall()

    assert len(goodbye_rows) == 1, (
        f"'goodbye' should match after rebuild, got {len(goodbye_rows)} rows"
    )
    assert len(hello_rows) == 0, "'hello' should be gone after rebuild (content changed)"


def test_vec_memories_count_mismatch_raises(tmp_db):
    """_step_5_integrity_check raises RuntimeError when vec count != memories count."""
    db.init_db()

    # Manually insert a memories row WITHOUT a corresponding vec_memories entry
    # to simulate a vec/memories count divergence.
    with sqlite3.connect(db.DB_PATH) as conn:
        db._load_vec_extension(conn)
        conn.execute(
            "INSERT INTO memories(id, project, type, content, created_at, updated_at) "
            "VALUES ('mem-c', 'p', 'n', 'orphan content', "
            "'2026-01-01T00:00:00.000Z', '2026-01-01T00:00:00.000Z')"
        )
        # Deliberately do NOT insert into vec_memories.
        conn.commit()

    with sqlite3.connect(db.DB_PATH) as conn:
        db._load_vec_extension(conn)
        with pytest.raises(RuntimeError, match="vec_memories integrity failure"):
            db._step_5_integrity_check(conn)
