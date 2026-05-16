"""server 侧错误码 + JSON 信封 — 跟 client polyforge_v3.errors.ErrorCode 同步。

注: 此处 import FastAPI 是有意的架构 pin (见 plan 头部 "v3.0 Architectural pins"),
不是 scope leak — aihub backend 锁定 FastAPI 框架。
"""
from __future__ import annotations

import enum
from typing import Any
from fastapi import HTTPException
from fastapi.responses import JSONResponse


class ErrorCode(str, enum.Enum):
    UNAUTHORIZED = "UNAUTHORIZED"
    FORBIDDEN = "FORBIDDEN"
    CONFLICT_EPOCH_MISMATCH = "CONFLICT_EPOCH_MISMATCH"
    CONFLICT_LEASE_EXPIRED = "CONFLICT_LEASE_EXPIRED"
    CONFLICT_HARD_BLOCK = "CONFLICT_HARD_BLOCK"
    CONFLICT_BUSY_NOT_TAKEOVER_ELIGIBLE = "CONFLICT_BUSY_NOT_TAKEOVER_ELIGIBLE"
    BAD_REQUEST = "BAD_REQUEST"
    NOT_FOUND = "NOT_FOUND"
    PAYLOAD_TOO_LARGE = "PAYLOAD_TOO_LARGE"
    INTERNAL_ERROR = "INTERNAL_ERROR"
    SERVICE_UNAVAILABLE = "SERVICE_UNAVAILABLE"


_STATUS_MAP = {
    ErrorCode.UNAUTHORIZED: 401,
    ErrorCode.FORBIDDEN: 403,
    ErrorCode.CONFLICT_EPOCH_MISMATCH: 409,
    ErrorCode.CONFLICT_LEASE_EXPIRED: 409,
    ErrorCode.CONFLICT_HARD_BLOCK: 409,
    ErrorCode.CONFLICT_BUSY_NOT_TAKEOVER_ELIGIBLE: 409,
    ErrorCode.BAD_REQUEST: 400,
    ErrorCode.NOT_FOUND: 404,
    ErrorCode.PAYLOAD_TOO_LARGE: 413,
    ErrorCode.INTERNAL_ERROR: 500,
    ErrorCode.SERVICE_UNAVAILABLE: 503,
}


class AihubServerError(HTTPException):
    def __init__(self, code: ErrorCode, message: str, details: dict[str, Any] | None = None):
        super().__init__(
            status_code=_STATUS_MAP[code],
            detail={"code": code.value, "message": message, "details": details or {}},
        )
        self.code = code


def envelope_response(code: ErrorCode, message: str, details: dict | None = None) -> JSONResponse:
    return JSONResponse(
        status_code=_STATUS_MAP[code],
        content={"code": code.value, "message": message, "details": details or {}},
    )
