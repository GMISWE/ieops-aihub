"""POST /v1/conflicts/predict — 5-rule predictor (§12.4)."""
from __future__ import annotations

from fastapi import APIRouter, Depends
from fastapi.responses import JSONResponse
from sqlalchemy.ext.asyncio import AsyncEngine

from app.auth import bearer_dep, verify_bearer
from app.conflicts import predict_conflicts
from app.errors import AihubServerError, ErrorCode
from app.schemas import PredictConflictsRequest, PredictConflictsResponse
from app.v3_app import get_engine


router = APIRouter(tags=["conflicts"])


@router.post("/conflicts/predict",
             response_model=PredictConflictsResponse,
             operation_id="predict_conflicts")
async def predict_conflicts_endpoint(
    body: PredictConflictsRequest,
    bearer: str | None = Depends(bearer_dep),
    engine: AsyncEngine = Depends(get_engine),
):
    async with engine.connect() as conn:
        user = await verify_bearer(conn, bearer)
        if user.role != "admin" and body.project not in user.projects:
            raise AihubServerError(ErrorCode.FORBIDDEN,
                                   f"user not in project {body.project}")
        result = await predict_conflicts(
            conn,
            project=body.project,
            declared_resources=[r.model_dump() for r in body.declared_resources],
            work_item_id=body.work_item_id,
            attempt_id=body.attempt_id,
        )
    return JSONResponse(status_code=200, content=result)
