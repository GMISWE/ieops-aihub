import asyncio
from typing import Literal

import numpy as np

MODEL_NAME = "BAAI/bge-small-en-v1.5"
_QUERY_PREFIX = "Represent this sentence for searching relevant passages: "
_model = None


def load_model() -> None:
    global _model
    from fastembed import TextEmbedding
    _model = TextEmbedding(MODEL_NAME)
    list(_model.embed(["warmup"]))


def get_model():
    if _model is None:
        raise RuntimeError("embedder not initialized")
    return _model


async def embed(text: str, role: Literal["query", "passage"]) -> np.ndarray:
    payload = _QUERY_PREFIX + text if role == "query" else text
    model = get_model()

    def _run() -> np.ndarray:
        vecs = list(model.embed([payload]))
        return vecs[0].astype(np.float32)

    loop = asyncio.get_running_loop()
    return await loop.run_in_executor(None, _run)
