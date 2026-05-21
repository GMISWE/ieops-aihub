import asyncio
import logging
import os
from contextlib import asynccontextmanager
from importlib.metadata import PackageNotFoundError, version as _pkg_version

from fastapi import FastAPI, HTTPException, Request
from fastapi.exceptions import RequestValidationError
from fastapi.responses import JSONResponse

import backup
import db
import embedder
from auth import bootstrap, validate_hash_secret
from routes.admin import router as admin_router
from routes.memories import router as memories_router

# v3 routers + errors (mount under /v1 — separate, do not regress legacy routes)
from app.db import init_db as _v3_init_db
from app.gc import gc_loop as _v3_gc_loop
from app.errors import AihubServerError, ErrorCode
from routes.v3_whoami import router as v3_whoami_router
from routes.v3_work_items import router as v3_wi_router
from routes.v3_claim import router as v3_claim_router
from routes.v3_attempts import router as v3_attempts_router
from routes.v3_misc import router as v3_misc_router
from routes.v3_conflicts import router as v3_conflicts_router
from routes.v3_artifacts import router as v3_artifacts_router
from routes.v3_admin import router as v3_admin_router
from routes.v3_memories import router as v3_memories_router

logging.basicConfig(level=os.getenv("LOG_LEVEL", "INFO").upper())


try:
    __version__ = _pkg_version("polyforge-aihub")
except PackageNotFoundError:
    # Not pip-installed (e.g. running from a source checkout without install).
    __version__ = "0.0.0+unknown"


@asynccontextmanager
async def lifespan(app: FastAPI):
    validate_hash_secret()
    # Load the embedder BEFORE db.init_db — the v0.3.0 migration step 2
    # reindex calls embedder.get_model() to re-embed historical rows; if
    # the model isn't loaded yet, init_db raises RuntimeError on first
    # boot against a pre-v0.3.0 DB. v0.3.0 shipped with this swapped and
    # crash-looped in prod on first deploy.
    embedder.load_model()
    db.init_db()
    bootstrap(os.getenv("ADMIN_API_KEY"))
    scheduler = backup.start_scheduler()

    # v3: init async engine + start GC loop (only if AIHUB_DATABASE_URL set —
    # legacy ieops-mem deployments without Postgres skip v3 startup gracefully).
    v3_gc_task: asyncio.Task | None = None
    if os.environ.get("AIHUB_DATABASE_URL"):
        try:
            _v3_init_db()
            from app.db import engine as _engine
            app.state.engine = _engine
            v3_gc_task = asyncio.create_task(_v3_gc_loop(_engine))
        except Exception:
            logging.getLogger(__name__).exception(
                "v3 init failed; legacy routes still served"
            )

    try:
        yield
    finally:
        if v3_gc_task is not None:
            v3_gc_task.cancel()
            try:
                await v3_gc_task
            except (asyncio.CancelledError, Exception):
                pass
        scheduler.shutdown(wait=False)


app = FastAPI(title="polyforge-aihub", version=__version__, lifespan=lifespan)
app.include_router(memories_router)
app.include_router(admin_router)

# v3 routers — all mounted under /v1
for _r in (v3_whoami_router, v3_wi_router, v3_claim_router, v3_attempts_router,
           v3_misc_router, v3_conflicts_router, v3_artifacts_router,
           v3_admin_router, v3_memories_router):
    app.include_router(_r, prefix="/v1")


@app.get("/health")
async def health():
    try:
        with db.get_db() as conn:
            conn.execute("SELECT 1").fetchone()
        db_status = "connected"
    except Exception:
        db_status = "error"

    try:
        embedder.get_model()
        model_status = "loaded"
    except Exception:
        model_status = "not loaded"

    return {"status": "ok", "db": db_status, "model": model_status, "version": __version__}


# ---------------------------------------------------------------------------
# Exception handlers
# ---------------------------------------------------------------------------

# v3 routes raise AihubServerError (subclass of HTTPException). Its detail is
# already a dict with {code, message, details} — the ErrorEnvelope shape per
# openapi. Register a specific handler for AihubServerError FIRST so FastAPI
# picks it over the generic HTTPException handler below (most-specific wins).
@app.exception_handler(AihubServerError)
async def v3_error_handler(request: Request, exc: AihubServerError):
    """Return v3 ErrorEnvelope directly (no "error" wrapper)."""
    return JSONResponse(status_code=exc.status_code, content=exc.detail)


# v3 validation errors (RequestValidationError on /v1/* routes) — return 400
# with ErrorEnvelope shape, not 422 with legacy wrapper. Routes under /v1 use
# Pydantic models that trigger this handler; legacy routes use form-based
# validation that never raises RequestValidationError (they do 400 via their
# own checks). If a /v1 path triggers validation error, we detect the prefix.
@app.exception_handler(RequestValidationError)
async def validation_exception_handler(request: Request, exc: RequestValidationError):
    if request.url.path.startswith("/v1"):
        return JSONResponse(
            status_code=400,
            content={"code": ErrorCode.BAD_REQUEST.value,
                     "message": "request validation failed",
                     "details": {"errors": exc.errors()}},
        )
    return JSONResponse(
        status_code=422,
        content={"error": {"code": "VALIDATION_ERROR", "message": str(exc)}},
    )


# Legacy HTTPException handler — wraps in {"error": {...}} for /admin/* /memories/*
@app.exception_handler(HTTPException)
async def http_exception_handler(request: Request, exc: HTTPException):
    detail = exc.detail
    if isinstance(detail, dict):
        body = {"error": detail}
    else:
        body = {"error": {"code": "ERROR", "message": str(detail)}}
    return JSONResponse(status_code=exc.status_code, content=body)


@app.exception_handler(Exception)
async def generic_exception_handler(request: Request, exc: Exception):
    logging.getLogger(__name__).exception(exc)
    return JSONResponse(
        status_code=500,
        content={"error": {"code": "INTERNAL_ERROR", "message": "internal server error"}},
    )
