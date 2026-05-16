"""POST/GET /v1/work_items + GET /v1/work_items/{id} (+ PATCH stubbed)."""
from __future__ import annotations

from fastapi import APIRouter, Body, Depends, Path, Query, Request
from fastapi.responses import JSONResponse
from sqlalchemy.ext.asyncio import AsyncEngine

from app.auth import bearer_dep, verify_bearer, verify_mutation
from app.errors import AihubServerError, ErrorCode
from app.schemas import (
    CreateWorkItemRequest, WorkItem, WorkItemDetailResponse, WorkItemListResponse,
    UpdateWorkItemRequest,
)
from app.v3_app import get_engine
from app.work_items import (
    get_work_item_detail, insert_work_item, list_work_items,
)


router = APIRouter(tags=["work_items"])


@router.post("/work_items", status_code=201, operation_id="create_work_item")
async def create_work_item(
    body: CreateWorkItemRequest,
    bearer: str | None = Depends(bearer_dep),
    engine: AsyncEngine = Depends(get_engine),
):
    async with engine.begin() as conn:
        user = await verify_bearer(conn, bearer)
        wi = await insert_work_item(conn, reporter=user, body=body.model_dump(mode="json"))
    return JSONResponse(status_code=201, content=_jsonable(wi))


@router.get("/work_items", response_model=WorkItemListResponse,
            operation_id="list_work_items")
async def list_work_items_endpoint(
    project: str | None = Query(default=None),
    status: str | None = Query(default=None),
    label: str | None = Query(default=None),
    user_id: str | None = Query(default=None),
    source: str | None = Query(default=None),
    since: str | None = Query(default=None),
    cursor: str | None = Query(default=None),
    limit: int = Query(default=50, ge=1, le=200),
    bearer: str | None = Depends(bearer_dep),
    engine: AsyncEngine = Depends(get_engine),
):
    async with engine.connect() as conn:
        user = await verify_bearer(conn, bearer)
        page = await list_work_items(
            conn, user=user, project=project, status=status, label=label,
            user_id=user_id, source=source, since=since, cursor=cursor,
            limit=limit,
        )
    return JSONResponse(status_code=200, content=_jsonable(page))


@router.get("/work_items/{work_item_id}",
            response_model=WorkItemDetailResponse, operation_id="get_work_item")
async def get_work_item_endpoint(
    work_item_id: str = Path(...),
    bearer: str | None = Depends(bearer_dep),
    engine: AsyncEngine = Depends(get_engine),
):
    async with engine.connect() as conn:
        user = await verify_bearer(conn, bearer)
        detail = await get_work_item_detail(conn, wi_id=work_item_id, user=user)
    return JSONResponse(status_code=200, content=_jsonable(detail))


@router.patch("/work_items/{work_item_id}", operation_id="update_work_item")
async def update_work_item_endpoint(
    body: UpdateWorkItemRequest,
    work_item_id: str = Path(...),
    bearer: str | None = Depends(bearer_dep),
    engine: AsyncEngine = Depends(get_engine),
):
    """1A scope: validate AC + work_item exists + actor matches; persist
    patch_payload (allowed fields only) on declared_resources / labels / priority
    via row-locked update."""
    import sqlalchemy as sa
    import json as _json

    async with engine.begin() as conn:
        user, attempt = await verify_mutation(
            conn, bearer,
            attempt_id=body.attempt_id,
            claim_epoch=body.claim_epoch,
            session_secret=body.session_secret,
        )
        wi_row = (await conn.execute(sa.text("""
            SELECT * FROM work_items WHERE id = :id FOR UPDATE
        """), {"id": work_item_id})).mappings().first()
        if wi_row is None:
            raise AihubServerError(ErrorCode.NOT_FOUND, f"work_item {work_item_id}")
        if attempt.work_item_id != work_item_id:
            raise AihubServerError(ErrorCode.FORBIDDEN,
                                   "attempt does not belong to this work_item")

        allowed = {"labels", "priority", "declared_resources", "metadata"}
        patch = body.patch_payload or {}
        unknown = set(patch.keys()) - allowed
        if unknown:
            raise AihubServerError(ErrorCode.BAD_REQUEST,
                                   f"patch_payload contains forbidden fields: {sorted(unknown)}")

        sets = ["updated_at = now()"]
        params: dict = {"id": work_item_id}
        if "labels" in patch:
            sets.append("labels = CAST(:labels AS JSONB)")
            params["labels"] = _json.dumps(patch["labels"])
        if "priority" in patch:
            sets.append("priority = :priority")
            params["priority"] = patch["priority"]
        if "declared_resources" in patch:
            sets.append("declared_resources = CAST(:dr AS JSONB)")
            sets.append("resources_version = resources_version + 1")
            params["dr"] = _json.dumps(patch["declared_resources"])
        if "metadata" in patch:
            sets.append("metadata = CAST(:metadata AS JSONB)")
            params["metadata"] = _json.dumps(patch["metadata"])

        await conn.execute(sa.text(
            f"UPDATE work_items SET {', '.join(sets)} WHERE id = :id"
        ), params)
        wi_row = (await conn.execute(sa.text("""
            SELECT * FROM work_items WHERE id = :id
        """), {"id": work_item_id})).mappings().first()
    return JSONResponse(status_code=200, content=_jsonable(dict(wi_row)))


# ---------------------------------------------------------------------------
# Serialization helper
# ---------------------------------------------------------------------------

def _jsonable(obj):
    """Recursively convert datetime/Decimal/etc. for JSONResponse."""
    from datetime import datetime, date
    from decimal import Decimal

    if isinstance(obj, dict):
        return {k: _jsonable(v) for k, v in obj.items()}
    if isinstance(obj, list):
        return [_jsonable(v) for v in obj]
    if isinstance(obj, (datetime, date)):
        return obj.isoformat()
    if isinstance(obj, Decimal):
        return float(obj)
    return obj
