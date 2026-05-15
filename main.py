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

logging.basicConfig(level=os.getenv("LOG_LEVEL", "INFO").upper())


try:
    __version__ = _pkg_version("ieops-mem")
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
    yield
    scheduler.shutdown(wait=False)


app = FastAPI(title="ieops-mem", version=__version__, lifespan=lifespan)
app.include_router(memories_router)
app.include_router(admin_router)


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


@app.exception_handler(RequestValidationError)
async def validation_exception_handler(request: Request, exc: RequestValidationError):
    return JSONResponse(
        status_code=422,
        content={"error": {"code": "VALIDATION_ERROR", "message": str(exc)}},
    )
