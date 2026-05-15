import asyncio
import numpy as np

MODEL_NAME = "sentence-transformers/all-MiniLM-L6-v2"
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


async def embed(text: str) -> np.ndarray:
    model = get_model()

    def _run() -> np.ndarray:
        vecs = list(model.embed([text]))
        return vecs[0].astype(np.float32)

    loop = asyncio.get_running_loop()
    return await loop.run_in_executor(None, _run)
