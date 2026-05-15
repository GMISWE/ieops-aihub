"""ASGI startup integration test — verifies the v0.3.0 lifespan migration
runs cleanly when the app is booted against a pre-v0.3.0 DB seed.

Safety net for the previously-fragile nested-event-loop reindex.
"""
import importlib.util
import json
import os
import sqlite3
import tempfile

import pytest


def _load_real_embedder():
    spec = importlib.util.find_spec("embedder")
    real = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(real)
    return real


@pytest.fixture(scope="module", autouse=True)
def _real_embedder_and_tmpdb():
    import embedder as _embedder_mod
    import db as _db_mod

    real = _load_real_embedder()
    _orig_load = _embedder_mod.load_model
    _orig_embed = _embedder_mod.embed
    _orig_model = _embedder_mod._model
    _orig_data = _db_mod.DATA_DIR
    _orig_path = _db_mod.DB_PATH

    _embedder_mod.load_model = real.load_model
    _embedder_mod.embed = real.embed
    real.load_model()
    _embedder_mod._model = real._model

    tmpdir = tempfile.mkdtemp()
    _db_mod.DATA_DIR = tmpdir
    _db_mod.DB_PATH = os.path.join(tmpdir, "test.db")

    # Seed a v0.2.x-style DB (memories table with embedding column,
    # no vec_memories, no memories_fts, no _migration_state).
    conn = sqlite3.connect(_db_mod.DB_PATH)
    conn.executescript("""
        CREATE TABLE memories (
            id TEXT PRIMARY KEY, project TEXT NOT NULL, type TEXT NOT NULL,
            content TEXT NOT NULL, metadata TEXT NOT NULL DEFAULT '{}',
            embedding BLOB, created_at TEXT NOT NULL, updated_at TEXT NOT NULL,
            expires_at TEXT, deprecated INTEGER NOT NULL DEFAULT 0,
            deprecated_reason TEXT, superseded_by TEXT,
            author_key_id TEXT, showable INTEGER NOT NULL DEFAULT 1
        );
        CREATE TABLE access (
            key_id TEXT PRIMARY KEY, key_hash TEXT NOT NULL,
            key_hint TEXT NOT NULL, project TEXT NOT NULL, role TEXT NOT NULL
        );
    """)
    # Seed a couple of v0.2.x rows so reindex actually runs.
    conn.execute(
        "INSERT INTO memories(id,project,type,content,created_at,updated_at) VALUES (?,?,?,?,?,?)",
        ("mem-pre030-a", "p", "note", "alpha bravo", "2026-01-01T00:00:00.000Z", "2026-01-01T00:00:00.000Z"),
    )
    conn.execute(
        "INSERT INTO memories(id,project,type,content,created_at,updated_at) VALUES (?,?,?,?,?,?)",
        ("mem-pre030-b", "p", "note", "charlie delta", "2026-01-02T00:00:00.000Z", "2026-01-02T00:00:00.000Z"),
    )
    conn.commit()
    conn.close()

    yield

    _db_mod.DATA_DIR = _orig_data
    _db_mod.DB_PATH = _orig_path
    _embedder_mod.load_model = _orig_load
    _embedder_mod.embed = _orig_embed
    _embedder_mod._model = _orig_model


def test_asgi_startup_runs_migration_and_health_reports_030():
    """Boots the full FastAPI app via TestClient; lifespan triggers the
    multi-step migration on the seeded v0.2.x DB. /health must come back
    200 with version 0.3.0 and the migration must have populated
    vec_memories + memories_fts for both seeded rows."""
    from fastapi.testclient import TestClient
    # Late import — fixture has rerouted db.DATA_DIR by now.
    from main import app

    with TestClient(app) as client:
        r = client.get("/health")
        assert r.status_code == 200, r.text
        body = r.json()
        assert body["status"] == "ok"
        assert body["version"] == "0.3.0"

    # Post-startup: vec_memories has the 2 seeded rows, memories_fts has them,
    # embedding column is gone.
    import db as _db_mod
    with sqlite3.connect(_db_mod.DB_PATH) as conn:
        _db_mod._load_vec_extension(conn)
        n_vec = conn.execute("SELECT COUNT(*) FROM vec_memories").fetchone()[0]
        n_fts = conn.execute("SELECT COUNT(*) FROM memories_fts").fetchone()[0]
        cols = [r[1] for r in conn.execute("PRAGMA table_info(memories)").fetchall()]
    assert n_vec == 2
    assert n_fts == 2
    assert "embedding" not in cols
