"""V3 FastAPI app factory — used by main.py (mount on legacy app) AND tests.

Keeps the v3 surface independent from legacy ieops-mem startup (embedder /
backup scheduler) so v3 tests don't have to load fastembed / sqlite-vec.
"""
from __future__ import annotations

import asyncio
import logging
from contextlib import asynccontextmanager
from typing import Awaitable, Callable

from fastapi import FastAPI, HTTPException, Request
from fastapi.exceptions import RequestValidationError
from fastapi.responses import JSONResponse
from sqlalchemy.ext.asyncio import AsyncEngine

from app.errors import ErrorCode

log = logging.getLogger("aihub.v3")


def make_v3_app(
    engine_factory: Callable[[], AsyncEngine],
    gc_loop_factory: Callable[[AsyncEngine], Awaitable[None]] | None = None,
    *,
    enable_gc: bool = False,
) -> FastAPI:
    """Build a FastAPI app with all /v1/* v3 routers mounted.

    engine_factory: zero-arg callable returning an AsyncEngine (called once at
        app startup). Lets tests inject a testcontainers-backed engine and prod
        inject the real one. The engine is stashed on app.state.engine.
    gc_loop_factory: optional async coroutine factory invoked at startup; the
        returned coro is launched as a Task and cancelled at shutdown. None
        skips GC (default; tests don't need a background loop).
    """
    # Import routers lazily to avoid circular imports during module load
    from routes.v3_whoami import router as whoami_router
    from routes.v3_work_items import router as wi_router
    from routes.v3_claim import router as claim_router
    from routes.v3_attempts import router as attempts_router
    from routes.v3_misc import router as misc_router
    from routes.v3_conflicts import router as conflicts_router
    from routes.v3_artifacts import router as artifacts_router

    @asynccontextmanager
    async def lifespan(app: FastAPI):
        app.state.engine = engine_factory()
        gc_task: asyncio.Task | None = None
        if enable_gc and gc_loop_factory is not None:
            gc_task = asyncio.create_task(gc_loop_factory(app.state.engine))
        try:
            yield
        finally:
            if gc_task is not None:
                gc_task.cancel()
                try:
                    await gc_task
                except (asyncio.CancelledError, Exception):
                    pass

    app = FastAPI(title="ieops-aihub v3", version="0.4.0", lifespan=lifespan)
    for r in (whoami_router, wi_router, claim_router, attempts_router,
              misc_router, conflicts_router, artifacts_router):
        app.include_router(r, prefix="/v1")

    @app.exception_handler(HTTPException)
    async def _http_ex_handler(request: Request, exc: HTTPException):
        detail = exc.detail
        if isinstance(detail, dict) and "code" in detail:
            return JSONResponse(status_code=exc.status_code, content=detail)
        return JSONResponse(
            status_code=exc.status_code,
            content={"code": "UNKNOWN_REMOTE_ERROR", "message": str(detail),
                     "details": {}},
        )

    @app.exception_handler(RequestValidationError)
    async def _validation_handler(request: Request, exc: RequestValidationError):
        return JSONResponse(
            status_code=400,
            content={"code": ErrorCode.BAD_REQUEST.value,
                     "message": "request validation failed",
                     "details": {"errors": exc.errors()}},
        )

    @app.exception_handler(Exception)
    async def _generic_handler(request: Request, exc: Exception):
        log.exception("unhandled v3 error: %s", exc)
        return JSONResponse(
            status_code=500,
            content={"code": ErrorCode.INTERNAL_ERROR.value,
                     "message": "internal server error", "details": {}},
        )

    return app


# ---------------------------------------------------------------------------
# Dependency helpers
# ---------------------------------------------------------------------------

async def get_engine(request: Request) -> AsyncEngine:
    """FastAPI dep — returns the engine stashed on app.state by lifespan."""
    engine = getattr(request.app.state, "engine", None)
    if engine is None:
        raise RuntimeError("v3 app engine not initialized (lifespan didn't run)")
    return engine
