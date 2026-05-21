"""
Tests for embedder.py — bge-small-en-v1.5 with role-based prefix injection.

conftest.py mocks load_model/embed/get_model for the rest of the suite.
This module restores real implementations so prefix injection, dimension,
and L2-norm are verified against the actual fastembed TextEmbedding model.
"""
import asyncio
import importlib
import importlib.util

import numpy as np
import pytest

import embedder as _embedder_mod


def _load_real_embedder():
    """Return a fresh, unpatched copy of the embedder module."""
    spec = importlib.util.find_spec("embedder")
    real = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(real)
    return real


@pytest.fixture(autouse=True, scope="module")
def _restore_and_load():
    """Install real embedder functions on the shared module object."""
    real = _load_real_embedder()

    # Save conftest patches
    _orig_load = _embedder_mod.load_model
    _orig_embed = _embedder_mod.embed
    _orig_model = _embedder_mod._model

    # Install real implementations
    _embedder_mod.load_model = real.load_model
    _embedder_mod.embed = real.embed

    # Load the model for real
    real.load_model()
    _embedder_mod._model = real._model

    yield

    # Restore conftest patches
    _embedder_mod.load_model = _orig_load
    _embedder_mod.embed = _orig_embed
    _embedder_mod._model = _orig_model


def test_embed_query_includes_bge_prefix(monkeypatch):
    captured = {}
    real_embed_fn = _embedder_mod.get_model().embed

    def spy(texts):
        captured["texts"] = list(texts)
        return real_embed_fn(texts)

    monkeypatch.setattr(_embedder_mod.get_model(), "embed", spy)
    asyncio.run(_embedder_mod.embed("hello world", role="query"))
    assert captured["texts"][0].startswith(
        "Represent this sentence for searching relevant passages: "
    )


def test_embed_passage_has_no_prefix(monkeypatch):
    captured = {}
    real_embed_fn = _embedder_mod.get_model().embed

    def spy(texts):
        captured["texts"] = list(texts)
        return real_embed_fn(texts)

    monkeypatch.setattr(_embedder_mod.get_model(), "embed", spy)
    asyncio.run(_embedder_mod.embed("hello world", role="passage"))
    assert captured["texts"][0] == "hello world"


def test_embed_dim_384():
    vec = asyncio.run(_embedder_mod.embed("anything", role="passage"))
    assert isinstance(vec, np.ndarray)
    assert vec.shape == (384,)
    assert vec.dtype == np.float32


def test_embed_l2_normalized():
    vec = asyncio.run(_embedder_mod.embed("any content", role="passage"))
    assert abs(float(np.linalg.norm(vec)) - 1.0) < 1e-5
