"""POST /v1/work_items/{id}/artifacts/{adopt|ignore|close}."""
from __future__ import annotations

from fastapi import APIRouter, Depends, Path
from fastapi.responses import JSONResponse
from sqlalchemy.ext.asyncio import AsyncEngine

from app.artifacts import adopt_artifact, close_artifact, ignore_artifact
from app.auth import bearer_dep, verify_mutation
from app.schemas import ArtifactActionRequest, OkResponse
from app.v3_app import get_engine


router = APIRouter(tags=["artifacts"])


async def _action_common(
    body: ArtifactActionRequest, work_item_id: str, bearer: str | None,
    engine: AsyncEngine, handler,
):
    async with engine.begin() as conn:
        user, attempt = await verify_mutation(
            conn, bearer,
            attempt_id=body.attempt_id, claim_epoch=body.claim_epoch,
            session_secret=body.session_secret,
        )
        kwargs: dict = dict(
            conn=conn, user=user, attempt=attempt,
            wi_id=work_item_id, artifact_type=body.type,
            identifier=body.identifier, repo=body.repo,
        )
        # Thread optional CAS field through to adopt_artifact only
        if hasattr(body, "expected_resources_version") and body.expected_resources_version is not None:
            kwargs["expected_resources_version"] = body.expected_resources_version
        await handler(**kwargs)
    return JSONResponse(status_code=200, content={"ok": True})


@router.post("/work_items/{work_item_id}/artifacts/adopt",
             response_model=OkResponse, operation_id="adopt_artifact")
async def adopt_endpoint(
    body: ArtifactActionRequest,
    work_item_id: str = Path(...),
    bearer: str | None = Depends(bearer_dep),
    engine: AsyncEngine = Depends(get_engine),
):
    return await _action_common(body, work_item_id, bearer, engine, adopt_artifact)


@router.post("/work_items/{work_item_id}/artifacts/ignore",
             response_model=OkResponse, operation_id="ignore_artifact")
async def ignore_endpoint(
    body: ArtifactActionRequest,
    work_item_id: str = Path(...),
    bearer: str | None = Depends(bearer_dep),
    engine: AsyncEngine = Depends(get_engine),
):
    return await _action_common(body, work_item_id, bearer, engine, ignore_artifact)


@router.post("/work_items/{work_item_id}/artifacts/close",
             response_model=OkResponse, operation_id="close_artifact")
async def close_endpoint(
    body: ArtifactActionRequest,
    work_item_id: str = Path(...),
    bearer: str | None = Depends(bearer_dep),
    engine: AsyncEngine = Depends(get_engine),
):
    return await _action_common(body, work_item_id, bearer, engine, close_artifact)
