import os
import tempfile
from unittest.mock import MagicMock

import numpy as np

# Must be set before any app module is imported
_tmp = tempfile.mkdtemp()
os.environ["DATA_DIR"] = _tmp
os.environ["HASH_SECRET"] = "test-hash-secret-at-least-32-bytes!!"
os.environ["ADMIN_API_KEY"] = "test-admin-key-abc123"

# Patch embedder: replace load_model and embed before main.py is imported
import embedder as _embedder  # noqa: E402

def _mock_load_model() -> None:
    pass

async def _mock_embed(text: str, role: str = "passage") -> np.ndarray:
    seed = abs(hash(text)) % (2**31)
    rng = np.random.RandomState(seed)
    vec = rng.randn(384).astype(np.float32)
    vec /= np.linalg.norm(vec)
    return vec

def _mock_model_embed(texts):
    """Sync generator that yields deterministic unit vectors — used by
    _embed_passage_sync (the migration path) which calls model.embed() directly."""
    import numpy as np
    for text in texts:
        seed = abs(hash(text)) % (2**31)
        rng = np.random.RandomState(seed)
        vec = rng.randn(384).astype(np.float32)
        vec /= np.linalg.norm(vec)
        yield vec


class _MockModel:
    """Minimal fastembed-compatible stub for the conftest mock embedder."""
    embed = staticmethod(_mock_model_embed)


_embedder.load_model = _mock_load_model
_embedder.embed = _mock_embed
# Make get_model() return a mock model so /health reports "loaded"
# and _embed_passage_sync (migration path) can call model.embed() directly.
_embedder._model = _MockModel()

# Patch backup scheduler to no-op
import backup as _backup  # noqa: E402

_mock_scheduler = MagicMock()
_mock_scheduler.shutdown = MagicMock()
_backup.start_scheduler = MagicMock(return_value=_mock_scheduler)

import pytest  # noqa: E402
from fastapi.testclient import TestClient  # noqa: E402
from main import app  # noqa: E402

ADMIN_KEY = "test-admin-key-abc123"
WRITER_KEY = "test-writer-key-xyz456"
READER_KEY = "test-reader-key-xyz789"
PROJECT = "test-proj"


@pytest.fixture(scope="session")
def client():
    with TestClient(app) as c:
        # register writer and reader keys
        c.post(
            "/admin/access",
            json={"api_key": WRITER_KEY, "project": PROJECT, "role": "writer"},
            headers={"X-API-Key": ADMIN_KEY},
        )
        c.post(
            "/admin/access",
            json={"api_key": READER_KEY, "project": PROJECT, "role": "reader"},
            headers={"X-API-Key": ADMIN_KEY},
        )
        yield c


@pytest.fixture
def admin_key():
    return ADMIN_KEY

@pytest.fixture
def writer_key():
    return WRITER_KEY

@pytest.fixture
def reader_key():
    return READER_KEY

@pytest.fixture
def project():
    return PROJECT
