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
