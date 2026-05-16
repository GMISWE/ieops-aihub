"""GET /v1/whoami — Bearer-only identity probe."""
from __future__ import annotations

from fastapi import APIRouter, Depends, Header
from sqlalchemy.ext.asyncio import AsyncEngine

from app.auth import bearer_dep, verify_bearer
from app.errors import AihubServerError, ErrorCode
from app.schemas import WhoAmIResponse
from app.v3_app import get_engine


router = APIRouter(tags=["whoami"])


@router.get("/whoami", response_model=WhoAmIResponse, operation_id="whoami")
async def whoami(
    bearer: str | None = Depends(bearer_dep),
    engine: AsyncEngine = Depends(get_engine),
) -> WhoAmIResponse:
    async with engine.connect() as conn:
        user = await verify_bearer(conn, bearer)
    return WhoAmIResponse(
        user_id=user.id, role=user.role, projects=list(user.projects),
    )
