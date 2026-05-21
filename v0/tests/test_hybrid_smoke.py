"""Hybrid-search smoke test — uses real bge-small ONNX embedder (Option A).

Asserts:
1. A single seeded document with content "polyforge ieops-mem live smoke test"
   ranks top-1 when queried with "smoke test".
2. The 3-channel hybrid score (vec + BM25 + recency) is >= the 2-channel score
   (vec + BM25 only, recency_boost=0).
"""
import importlib.util
import os
import tempfile

import pytest

import db as _db_mod
import embedder as _embedder_mod

ADMIN_KEY = "test-admin-key-abc123"
_SMOKE_WRITER = "smoke-writer-key-xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx"
_SMOKE_READER = "smoke-reader-key-xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx"
_SMOKE_PROJECT = "smoke-test-proj"


def _load_real_embedder():
    """Return a fresh, unpatched copy of the embedder module."""
    spec = importlib.util.find_spec("embedder")
    real = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(real)
    return real


@pytest.fixture(scope="module")
def smoke_client():
    """Module-scoped TestClient backed by a fresh DB + real embedder."""
    from fastapi.testclient import TestClient
    from main import app

    real = _load_real_embedder()

    _orig_load = _embedder_mod.load_model
    _orig_embed = _embedder_mod.embed
    _orig_model = _embedder_mod._model

    _embedder_mod.load_model = real.load_model
    _embedder_mod.embed = real.embed
    real.load_model()
    _embedder_mod._model = real._model

    tmpdir = tempfile.mkdtemp()
    _orig_data = _db_mod.DATA_DIR
    _orig_path = _db_mod.DB_PATH
    _db_mod.DATA_DIR = tmpdir
    _db_mod.DB_PATH = os.path.join(tmpdir, "smoke_test.db")

    with TestClient(app) as client:
        r = client.post(
            "/admin/access",
            json={"api_key": _SMOKE_WRITER, "project": _SMOKE_PROJECT, "role": "writer"},
            headers={"X-API-Key": ADMIN_KEY},
        )
        assert r.status_code in (200, 201, 409), f"writer reg failed: {r.text}"
        r = client.post(
            "/admin/access",
            json={"api_key": _SMOKE_READER, "project": _SMOKE_PROJECT, "role": "reader"},
            headers={"X-API-Key": ADMIN_KEY},
        )
        assert r.status_code in (200, 201, 409), f"reader reg failed: {r.text}"
        yield client

    _db_mod.DATA_DIR = _orig_data
    _db_mod.DB_PATH = _orig_path
    _embedder_mod.load_model = _orig_load
    _embedder_mod.embed = _orig_embed
    _embedder_mod._model = _orig_model


def test_smoke_test_phrase_ranks_top1_and_beats_vector_only(smoke_client):
    client = smoke_client
    content = "polyforge ieops-mem live smoke test"

    r = client.post(
        "/memories",
        headers={"X-API-Key": _SMOKE_WRITER},
        json={"project": _SMOKE_PROJECT, "type": "smoke", "content": content},
    )
    assert r.status_code == 201, r.text
    mem_id = r.json()["id"]

    # Full hybrid (vec + BM25 + recency) — must rank the seeded doc top-1.
    full = client.post(
        "/memories/search",
        headers={"X-API-Key": _SMOKE_READER},
        json={"project": _SMOKE_PROJECT, "query": "smoke test", "top_k": 1},
    )
    assert full.status_code == 200, full.text
    full_body = full.json()
    assert len(full_body["results"]) == 1
    assert full_body["results"][0]["memory"]["id"] == mem_id, (
        f"expected {mem_id}, got {full_body['results'][0]['memory']['id']}"
    )

    # 2-channel only (recency_boost=0 disables the recency channel).
    # Full hybrid (3 channels) score should be >= 2-channel score.
    vec_bm25 = client.post(
        "/memories/search",
        headers={"X-API-Key": _SMOKE_READER},
        json={"project": _SMOKE_PROJECT, "query": "smoke test", "top_k": 1, "recency_boost": 0},
    )
    assert vec_bm25.status_code == 200, vec_bm25.text
    vec_bm25_body = vec_bm25.json()
    assert len(vec_bm25_body["results"]) == 1

    full_score = full_body["results"][0]["score"]
    two_ch_score = vec_bm25_body["results"][0]["score"]
    assert full_score >= two_ch_score, (
        f"full-hybrid score {full_score:.6f} < 2-channel score {two_ch_score:.6f}"
    )
