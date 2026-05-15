"""nDCG@5 golden corpus test — uses real bge-small ONNX embedder (Option A).

conftest.py patches embedder with a deterministic random mock; this module
swaps in the real implementations for module-scoped duration so RRF ranking
is semantically meaningful and the nDCG floor is testable.
"""
import importlib.util
import json
import math
import os
import tempfile
from pathlib import Path

import pytest

import db as _db_mod
import embedder as _embedder_mod

FIXTURES = Path(__file__).parent / "fixtures"
GOLDEN_CORPUS = json.loads((FIXTURES / "golden_corpus.json").read_text())
GOLDEN_QUERIES = json.loads((FIXTURES / "golden_queries.json").read_text())

NDCG_FLOOR = 0.85

ADMIN_KEY = "test-admin-key-abc123"
_GOLDEN_WRITER = "golden-writer-key-xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx"
_GOLDEN_READER = "golden-reader-key-xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx"
_GOLDEN_PROJECT = "golden-test-proj"


def _load_real_embedder():
    """Return a fresh, unpatched copy of the embedder module."""
    spec = importlib.util.find_spec("embedder")
    real = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(real)
    return real


@pytest.fixture(scope="module")
def golden_client():
    """
    Module-scoped TestClient backed by a fresh DB + real embedder.

    Steps:
    1. Save conftest mock state on _embedder_mod.
    2. Install real load_model / embed / _model.
    3. Patch db.DATA_DIR + db.DB_PATH to an isolated tmpdir so this module
       gets its own DB; the conftest session-scoped client keeps its DB.
    4. Enter TestClient context → triggers lifespan → init_db() + load_model()
       run against the patched paths with real embeddings.
    5. Register admin/writer/reader keys.
    6. Yield the client.
    7. Restore everything.
    """
    from fastapi.testclient import TestClient
    from main import app

    real = _load_real_embedder()

    # --- save conftest patches ---
    _orig_load = _embedder_mod.load_model
    _orig_embed = _embedder_mod.embed
    _orig_model = _embedder_mod._model

    # --- install real embedder ---
    _embedder_mod.load_model = real.load_model
    _embedder_mod.embed = real.embed
    real.load_model()
    _embedder_mod._model = real._model

    # --- isolated DB ---
    tmpdir = tempfile.mkdtemp()
    _orig_data = _db_mod.DATA_DIR
    _orig_path = _db_mod.DB_PATH
    _db_mod.DATA_DIR = tmpdir
    _db_mod.DB_PATH = os.path.join(tmpdir, "golden_test.db")

    with TestClient(app) as client:
        # Register writer and reader keys via admin endpoint.
        r = client.post(
            "/admin/access",
            json={"api_key": _GOLDEN_WRITER, "project": _GOLDEN_PROJECT, "role": "writer"},
            headers={"X-API-Key": ADMIN_KEY},
        )
        assert r.status_code in (200, 201, 409), f"writer reg failed: {r.text}"
        r = client.post(
            "/admin/access",
            json={"api_key": _GOLDEN_READER, "project": _GOLDEN_PROJECT, "role": "reader"},
            headers={"X-API-Key": ADMIN_KEY},
        )
        assert r.status_code in (200, 201, 409), f"reader reg failed: {r.text}"
        yield client

    # --- restore ---
    _db_mod.DATA_DIR = _orig_data
    _db_mod.DB_PATH = _orig_path
    _embedder_mod.load_model = _orig_load
    _embedder_mod.embed = _orig_embed
    _embedder_mod._model = _orig_model


def _ndcg_at_5(actual_ids: list[str], expected_id: str) -> float:
    try:
        idx = actual_ids.index(expected_id)
    except ValueError:
        return 0.0
    if idx >= 5:
        return 0.0
    return 1.0 / math.log2(idx + 2)


def test_golden_ndcg_at_5_above_floor(golden_client):
    """Seed the 30-entry golden corpus, run 10 queries, assert mean nDCG@5 >= 0.85."""
    client = golden_client

    # Seed corpus and record fixture-id → actual ULID mapping.
    id_map: dict[str, str] = {}
    for doc in GOLDEN_CORPUS:
        r = client.post(
            "/memories",
            headers={"X-API-Key": _GOLDEN_WRITER},
            json={
                "project": _GOLDEN_PROJECT,
                "type": doc["type"],
                "content": doc["content"],
            },
        )
        assert r.status_code == 201, f"seed failed for {doc['id']}: {r.text}"
        id_map[doc["id"]] = r.json()["id"]

    # Run queries and compute nDCG@5 per query.
    scores: list[float] = []
    failures: list[str] = []
    for q in GOLDEN_QUERIES:
        r = client.post(
            "/memories/search",
            headers={"X-API-Key": _GOLDEN_READER},
            json={"project": _GOLDEN_PROJECT, "query": q["query"], "top_k": 5},
        )
        assert r.status_code == 200, f"search failed for query '{q['query']}': {r.text}"
        body = r.json()
        assert body["score_scale"] == "rrf-fused", f"unexpected score_scale: {body.get('score_scale')}"

        actual_ids = [item["memory"]["id"] for item in body["results"]]
        expected_actual = id_map[q["expected_top_id"]]
        ndcg = _ndcg_at_5(actual_ids, expected_actual)
        scores.append(ndcg)
        if ndcg < 1.0:
            failures.append(
                f"  query='{q['query']}' expected={q['expected_top_id']} "
                f"got=[{', '.join(actual_ids[:3])}] nDCG={ndcg:.3f}"
            )

    mean = sum(scores) / len(scores)
    detail = "\n".join(failures) if failures else "all queries hit rank-1"
    assert mean >= NDCG_FLOOR, (
        f"nDCG@5 = {mean:.3f} below floor {NDCG_FLOOR}\n{detail}"
    )
    # Print for visibility in pytest output (-s).
    print(f"\nnDCG@5 = {mean:.4f} over {len(scores)} queries")
