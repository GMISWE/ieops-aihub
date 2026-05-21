"""HIGH fix integration test — production main app uses v3 ErrorEnvelope handlers.

Verifies that:
1. AihubServerError registered on the production app returns top-level
   {code, message, details} (no "error" wrapper).
2. RequestValidationError on /v1/* routes returns 400 + BAD_REQUEST envelope.
3. Legacy routes keep their existing {"error": {...}} wrapping.
4. The AihubServerError handler is actually registered on main.app.

Tests call handlers directly (without a live DB) to isolate the exception
handler fix from DB connectivity.
"""
from __future__ import annotations

import asyncio
import json

import pytest
from fastapi import Request


def _make_scope(path: str, method: str = "GET") -> dict:
    return {
        "type": "http",
        "method": method,
        "path": path,
        "query_string": b"",
        "headers": [],
    }


# ---------------------------------------------------------------------------
# Unit-level: verify handlers are registered and return correct shapes
# ---------------------------------------------------------------------------

def test_aihubservererror_handler_registered_on_main_app():
    """AihubServerError handler must be in the main app's exception handlers."""
    from main import app
    from app.errors import AihubServerError
    handlers = dict(app.exception_handlers)
    assert AihubServerError in handlers, (
        "AihubServerError handler not registered on production app; "
        "v3 errors will fall through to legacy HTTPException handler and get wrapped"
    )


def test_aihubservererror_handler_returns_envelope_no_wrapper():
    """AihubServerError responses: {code, message, details} at top level, no 'error' wrapper."""
    from app.errors import AihubServerError, ErrorCode
    from main import v3_error_handler

    async def _run():
        exc = AihubServerError(ErrorCode.UNAUTHORIZED, "missing Bearer token")
        request = Request(_make_scope("/v1/whoami"))
        return await v3_error_handler(request, exc)

    resp = asyncio.run(_run())
    assert resp.status_code == 401
    body = json.loads(resp.body)
    assert "code" in body, f"missing 'code': {body}"
    assert "message" in body, f"missing 'message': {body}"
    assert "details" in body, f"missing 'details': {body}"
    assert "error" not in body, f"got legacy 'error' wrapper: {body}"
    assert body["code"] == "UNAUTHORIZED"


def test_legacy_httpexception_handler_still_wraps():
    """Legacy HTTPException handler must still return {\"error\": {...}} shape."""
    from fastapi import HTTPException
    from main import http_exception_handler

    async def _run():
        exc = HTTPException(status_code=403, detail="forbidden")
        request = Request(_make_scope("/admin/something"))
        return await http_exception_handler(request, exc)

    resp = asyncio.run(_run())
    assert resp.status_code == 403
    body = json.loads(resp.body)
    assert "error" in body, f"expected 'error' wrapper for legacy: {body}"


def test_validation_handler_v1_returns_400_envelope():
    """RequestValidationError on /v1/* must return 400 + BAD_REQUEST envelope."""
    from pydantic import BaseModel, ValidationError
    from fastapi.exceptions import RequestValidationError
    from main import validation_exception_handler

    class _M(BaseModel):
        x: int

    async def _run():
        try:
            _M(x="bad")
        except ValidationError as ve:
            exc = RequestValidationError(ve.errors())
        request = Request(_make_scope("/v1/work_items", "POST"))
        return await validation_exception_handler(request, exc)

    resp = asyncio.run(_run())
    assert resp.status_code == 400
    body = json.loads(resp.body)
    assert body.get("code") == "BAD_REQUEST"
    assert "error" not in body


def test_validation_handler_legacy_path_returns_422():
    """RequestValidationError on non-/v1 path returns 422 legacy envelope."""
    from pydantic import BaseModel, ValidationError
    from fastapi.exceptions import RequestValidationError
    from main import validation_exception_handler

    class _M(BaseModel):
        x: int

    async def _run():
        try:
            _M(x="bad")
        except ValidationError as ve:
            exc = RequestValidationError(ve.errors())
        request = Request(_make_scope("/memories/search", "POST"))
        return await validation_exception_handler(request, exc)

    resp = asyncio.run(_run())
    assert resp.status_code == 422
    body = json.loads(resp.body)
    assert "error" in body
