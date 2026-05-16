"""POST /v1/work_items/{id}/complete + /v1/locks + /v1/events."""
from __future__ import annotations

import json
from datetime import datetime

import sqlalchemy as sa
from fastapi import APIRouter, Body, Depends, Path, Query
from fastapi.responses import JSONResponse
from sqlalchemy.ext.asyncio import AsyncEngine

from app.auth import (
    bearer_dep, verify_bearer, verify_mutation,
)
from app.errors import AihubServerError, ErrorCode
from app.events import emit_event, _payload_too_large
from app.locks import acquire_lock, release_lock
from app.schemas import (
    AcquireLockRequest, AttemptCredential, CompleteWorkItemRequest,
    EmitEventRequest, EmitEventResponse, EventListResponse, OkResponse,
)
from app.v3_app import get_engine


router = APIRouter(tags=["misc"])


# ---------------------------------------------------------------------------
# Work item complete
# ---------------------------------------------------------------------------

@router.post("/work_items/{work_item_id}/complete",
             response_model=OkResponse, operation_id="complete_work_item")
async def complete_work_item_endpoint(
    body: CompleteWorkItemRequest,
    work_item_id: str = Path(...),
    bearer: str | None = Depends(bearer_dep),
    engine: AsyncEngine = Depends(get_engine),
):
    """Per §6.2: reporter OR current actor may complete. Transitions:
       - work_item.status -> wrapped|failed, closed_at=now()
       - current_attempt -> wrapped (if still running)
       - DELETE owner_attempt_id locks
       - emit work_item_completed + attempt_completed
    """
    async with engine.begin() as conn:
        user, attempt = await verify_mutation(
            conn, bearer,
            attempt_id=body.attempt_id, claim_epoch=body.claim_epoch,
            session_secret=body.session_secret,
        )
        wi_row = (await conn.execute(sa.text("""
            SELECT id, reporter_user_id, current_attempt_id, status
            FROM work_items WHERE id = :id FOR UPDATE
        """), {"id": work_item_id})).mappings().first()
        if wi_row is None:
            raise AihubServerError(ErrorCode.NOT_FOUND, f"work_item {work_item_id}")
        is_reporter = wi_row["reporter_user_id"] == user.id
        is_current_actor = (
            wi_row["current_attempt_id"] == attempt.id
            and attempt.actor_user_id == user.id
        )
        if not (is_reporter or is_current_actor):
            raise AihubServerError(
                ErrorCode.FORBIDDEN, "only reporter or current actor may complete",
            )

        new_status = body.final_status  # 'wrapped' | 'failed'
        # 1. complete attempt (if running)
        await conn.execute(sa.text("""
            UPDATE run_attempts SET status='wrapped', ended_at=now()
            WHERE id = :aid AND status = 'running'
        """), {"aid": attempt.id})
        await conn.execute(sa.text("""
            DELETE FROM resource_locks WHERE owner_attempt_id = :aid
        """), {"aid": attempt.id})
        # 2. complete work_item
        await conn.execute(sa.text("""
            UPDATE work_items
            SET status = :s, closed_at = now(), current_attempt_id = NULL,
                updated_at = now()
            WHERE id = :id
        """), {"s": new_status, "id": work_item_id})
        # 3. events
        await emit_event(
            conn, work_item_id=work_item_id, event_type="attempt_completed",
            payload={"status": "wrapped"}, actor_user_id=user.id,
            api_key_id=user.matched_api_key_id, run_attempt_id=attempt.id,
        )
        await emit_event(
            conn, work_item_id=work_item_id, event_type="work_item_completed",
            payload={"work_item_id": work_item_id, "final_status": new_status},
            actor_user_id=user.id, api_key_id=user.matched_api_key_id,
        )
    return JSONResponse(status_code=200, content={"ok": True})


# ---------------------------------------------------------------------------
# Locks
# ---------------------------------------------------------------------------

@router.post("/locks", response_model=OkResponse, operation_id="acquire_lock")
async def acquire_lock_endpoint(
    body: AcquireLockRequest,
    bearer: str | None = Depends(bearer_dep),
    engine: AsyncEngine = Depends(get_engine),
):
    async with engine.begin() as conn:
        user, attempt = await verify_mutation(
            conn, bearer,
            attempt_id=body.attempt_id, claim_epoch=body.claim_epoch,
            session_secret=body.session_secret,
        )
        await acquire_lock(
            conn, user=user, attempt=attempt,
            resource_type=body.resource_type, resource_key=body.resource_key,
        )
    return JSONResponse(status_code=200, content={"ok": True})


@router.delete("/locks/{resource_type}/{resource_key:path}",
               response_model=OkResponse, operation_id="release_lock")
async def release_lock_endpoint(
    body: AttemptCredential,
    resource_type: str = Path(...),
    resource_key: str = Path(...),
    bearer: str | None = Depends(bearer_dep),
    engine: AsyncEngine = Depends(get_engine),
):
    async with engine.begin() as conn:
        user, attempt = await verify_mutation(
            conn, bearer,
            attempt_id=body.attempt_id, claim_epoch=body.claim_epoch,
            session_secret=body.session_secret,
        )
        await release_lock(
            conn, user=user, attempt=attempt,
            resource_type=resource_type, resource_key=resource_key,
        )
    return JSONResponse(status_code=200, content={"ok": True})


# ---------------------------------------------------------------------------
# Events
# ---------------------------------------------------------------------------

@router.post("/events", status_code=201, response_model=EmitEventResponse,
             operation_id="emit_event")
async def emit_event_endpoint(
    body: EmitEventRequest,
    bearer: str | None = Depends(bearer_dep),
    engine: AsyncEngine = Depends(get_engine),
):
    """Mutating: verify_mutation (attempt-scoped). Payload size cap 64 KiB."""
    if _payload_too_large(body.payload):
        raise AihubServerError(ErrorCode.PAYLOAD_TOO_LARGE,
                               "event payload exceeds 64 KiB")
    async with engine.begin() as conn:
        user, attempt = await verify_mutation(
            conn, bearer,
            attempt_id=body.attempt_id, claim_epoch=body.claim_epoch,
            session_secret=body.session_secret,
        )
        # work_item_id sanity — must match attempt.work_item_id
        if body.work_item_id != attempt.work_item_id:
            raise AihubServerError(
                ErrorCode.BAD_REQUEST,
                "work_item_id does not match attempt.work_item_id",
            )
        eid = await emit_event(
            conn, work_item_id=body.work_item_id, event_type=body.event_type,
            payload=body.payload, actor_user_id=user.id,
            api_key_id=user.matched_api_key_id, run_attempt_id=body.attempt_id,
            pinned=body.pinned,
        )
    return JSONResponse(status_code=201, content={"event_id": eid})


@router.get("/events", response_model=EventListResponse,
            operation_id="list_events")
async def list_events_endpoint(
    work_item_id: str | None = Query(default=None),
    types: str | None = Query(default=None),
    since: str | None = Query(default=None),
    cursor: str | None = Query(default=None),
    limit: int = Query(default=50, ge=1, le=200),
    bearer: str | None = Depends(bearer_dep),
    engine: AsyncEngine = Depends(get_engine),
):
    async with engine.connect() as conn:
        user = await verify_bearer(conn, bearer)
        where: list[str] = []
        params: dict = {"limit": limit + 1}
        if work_item_id is not None:
            where.append("ev.work_item_id = :wid")
            params["wid"] = work_item_id
            # Permission: user must be in the project of that work_item
            proj_row = (await conn.execute(sa.text(
                "SELECT project FROM work_items WHERE id = :id"
            ), {"id": work_item_id})).first()
            if proj_row is None:
                raise AihubServerError(ErrorCode.NOT_FOUND, f"work_item {work_item_id}")
            if user.role != "admin" and proj_row[0] not in user.projects:
                raise AihubServerError(ErrorCode.FORBIDDEN, "project not in user scope")
        else:
            if user.role != "admin":
                # restrict to events from work_items in user's projects
                where.append("""ev.work_item_id IN (
                    SELECT id FROM work_items WHERE project = ANY(CAST(:projects AS text[]))
                )""")
                params["projects"] = list(user.projects)
        if types is not None:
            type_list = [t.strip() for t in types.split(",") if t.strip()]
            if type_list:
                where.append("ev.event_type = ANY(:types)")
                params["types"] = type_list
        if since is not None:
            where.append("ev.created_at >= CAST(:since AS TIMESTAMPTZ)")
            params["since"] = since
        if cursor is not None:
            try:
                cursor_ts_str, cursor_id = cursor.split("|", 1)
                cursor_ts = datetime.fromisoformat(cursor_ts_str)
            except (ValueError, TypeError):
                raise AihubServerError(ErrorCode.BAD_REQUEST, "malformed cursor")
            where.append("(ev.created_at, ev.id) < (:cursor_ts, :cursor_id)")
            params["cursor_ts"] = cursor_ts
            params["cursor_id"] = cursor_id

        where_sql = " WHERE " + " AND ".join(where) if where else ""
        rows = (await conn.execute(sa.text(f"""
            SELECT ev.* FROM agent_events ev
            {where_sql}
            ORDER BY ev.created_at DESC, ev.id DESC
            LIMIT :limit
        """), params)).mappings().all()

        has_more = len(rows) > limit
        rows = rows[:limit]
        next_cursor = None
        if has_more and rows:
            last = rows[-1]
            next_cursor = f"{last['created_at'].isoformat()}|{last['id']}"
        items = [{**dict(r), "created_at": r["created_at"].isoformat()} for r in rows]
    return JSONResponse(status_code=200, content={
        "items": items, "next_cursor": next_cursor,
    })
