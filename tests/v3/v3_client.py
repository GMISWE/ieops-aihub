"""V3-only FastAPI TestClient harness — sync client wrapping AsyncClient.

Uses ASGI Transport so requests go through FastAPI without a real server.
"""
from __future__ import annotations

import asyncio
from contextlib import asynccontextmanager

import httpx
import pytest_asyncio
from sqlalchemy.ext.asyncio import AsyncEngine

from app.v3_app import make_v3_app


@asynccontextmanager
async def make_async_client(engine: AsyncEngine):
    """Yield an httpx AsyncClient bound to a v3 FastAPI app using `engine`.

    The lifespan handler will overwrite app.state.engine; we pass a factory
    that returns the existing engine (so we don't double-create).
    """
    app = make_v3_app(engine_factory=lambda: engine)
    transport = httpx.ASGITransport(app=app)
    async with httpx.AsyncClient(transport=transport, base_url="http://test") as client:
        # Ensure lifespan startup runs (ASGITransport handles it lazily; force via /v1/whoami)
        # Simpler: use lifespan context manager directly via FastAPI startup hook
        async with _lifespan_ctx(app):
            yield client


@asynccontextmanager
async def _lifespan_ctx(app):
    # Run startup
    async with app.router.lifespan_context(app):
        yield


def auth_headers(bearer: str) -> dict[str, str]:
    return {"Authorization": f"Bearer {bearer}"}


# Pre-known bearer tokens for reference seeded users (1A test convention:
# bearer == stored key_hash literal)
BEARER_ZHANG = "argon2id$dummy_seed_hash_zhang"
BEARER_LI = "argon2id$dummy_seed_hash_li"
BEARER_WANG = "argon2id$dummy_seed_hash_wang"
