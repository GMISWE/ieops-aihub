"""POST /v1/work_items/{id}/claim — atomic claim (§7.2.1)."""
from __future__ import annotations

from fastapi import APIRouter, Depends, Path
from fastapi.responses import JSONResponse
from sqlalchemy.ext.asyncio import AsyncEngine

from app.auth import bearer_dep, verify_bearer
from app.run_attempts import claim_work_item
from app.schemas import ClaimRequest, ClaimResponse
from app.v3_app import get_engine


router = APIRouter(tags=["claim"])


@router.post("/work_items/{work_item_id}/claim",
             response_model=ClaimResponse, operation_id="claim_work_item")
async def claim_work_item_endpoint(
    body: ClaimRequest,
    work_item_id: str = Path(...),
    bearer: str | None = Depends(bearer_dep),
    engine: AsyncEngine = Depends(get_engine),
):
    async with engine.begin() as conn:
        user = await verify_bearer(conn, bearer)
        result = await claim_work_item(
            conn,
            wi_id=work_item_id,
            user=user,
            idempotency_key=body.idempotency_key,
            machine_id=body.session_info.machine_id,
            session_secret_raw=body.session_info.session_secret,
            requested_locks=[l.model_dump() for l in body.requested_locks],
        )
    # ClaimResponse: attempt_id + claim_epoch + lease_until
    return JSONResponse(status_code=200, content={
        "attempt_id": result["attempt_id"],
        "claim_epoch": result["claim_epoch"],
        "lease_until": result["lease_until"].isoformat(),
    })
