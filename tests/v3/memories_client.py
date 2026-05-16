"""Test client factory for v3 memories tests.

Builds a FastAPI app that includes the memories router WITHOUT modifying
app/v3_app.py (which is owned by the orchestrator and may be edited
concurrently by the admin sub-agent).
"""
from __future__ import annotations

from contextlib import asynccontextmanager

import httpx
from sqlalchemy.ext.asyncio import AsyncEngine

from app.v3_app import make_v3_app


@asynccontextmanager
async def make_memories_client(engine: AsyncEngine):
    """Like make_async_client but includes the memories router."""
    from routes.v3_memories import router as memories_router

    app = make_v3_app(engine_factory=lambda: engine)
    # Mount memories router under /v1 (idempotent if orchestrator already added it)
    # Guard: avoid double-include if the orchestrator already wired it
    existing_routes = {r.path for r in app.routes}  # type: ignore[attr-defined]
    if "/v1/memories" not in existing_routes:
        app.include_router(memories_router, prefix="/v1")

    transport = httpx.ASGITransport(app=app)
    async with httpx.AsyncClient(transport=transport, base_url="http://test") as client:
        async with app.router.lifespan_context(app):
            yield client
