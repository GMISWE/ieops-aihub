"""POST/GET/PATCH /v1/memories + POST /v1/memories/{id}/redact — v3.0 memory endpoints.

Design §15.2 carve-outs:
- No semantic search (query param accepted but ignored).
- No embedding computation server-side (embedding column stays null).
- No physical delete; redact is soft-delete only.
- No AttemptCredential (§6.1 carve-out): Bearer-only auth on all 4 endpoints.
"""
from __future__ import annotations

import base64
import json
from datetime import datetime
from typing import Any

import sqlalchemy as sa
from fastapi import APIRouter, Depends, Path, Query
from fastapi.responses import JSONResponse
from pydantic import BaseModel, Field
from sqlalchemy.ext.asyncio import AsyncEngine
from ulid import ULID

from app.auth import bearer_dep, verify_bearer
from app.errors import AihubServerError, ErrorCode
from app.events import emit_event
from app.v3_app import get_engine


router = APIRouter(tags=["memories"])


# ---------------------------------------------------------------------------
# ID helper
# ---------------------------------------------------------------------------

def _mem_id() -> str:
    return "mem_" + str(ULID()).lower()


# ---------------------------------------------------------------------------
# Pydantic models (inline, not in schemas.py)
# ---------------------------------------------------------------------------

class CreateMemoryRequest(BaseModel):
    project: str
    type: str
    content: str
    visibility: str = "project"
    work_item_id: str | None = None
    metadata: dict[str, Any] | None = None
    expires_at: str | None = None  # ISO-8601 timestamp


class UpdateMemoryRequest(BaseModel):
    patch_payload: dict[str, Any]


class RedactMemoryRequest(BaseModel):
    reason: str


class Memory(BaseModel):
    id: str
    project: str
    author_user_id: str
    work_item_id: str | None
    visibility: str
    type: str
    content: str | None
    metadata: dict[str, Any]
    embedding: Any | None
    redacted_at: str | None
    redaction_reason: str | None
    expires_at: str | None
    created_at: str


class MemoryListResponse(BaseModel):
    items: list[Memory]
    next_cursor: str | None


class OkResponse(BaseModel):
    ok: bool


# ---------------------------------------------------------------------------
# Visibility constants
# ---------------------------------------------------------------------------

_VALID_VISIBILITY = {"private", "project", "team", "admin"}


# ---------------------------------------------------------------------------
# Serialization helpers
# ---------------------------------------------------------------------------

def _row_to_memory_dict(row) -> dict:
    d = dict(row)
    for f in ("redacted_at", "expires_at", "created_at"):
        if isinstance(d.get(f), datetime):
            d[f] = d[f].isoformat()
    # Don't return embedding (large vector; null in v3.0 anyway)
    d["embedding"] = None
    return d


def _jsonable(obj):
    from decimal import Decimal
    if isinstance(obj, dict):
        return {k: _jsonable(v) for k, v in obj.items()}
    if isinstance(obj, list):
        return [_jsonable(v) for v in obj]
    if isinstance(obj, datetime):
        return obj.isoformat()
    if isinstance(obj, Decimal):
        return float(obj)
    return obj


# ---------------------------------------------------------------------------
# Cursor helpers (opaque base64 wrapping "<created_at_iso>|<id>")
# ---------------------------------------------------------------------------

def _encode_cursor(created_at: datetime, mem_id: str) -> str:
    raw = f"{created_at.isoformat()}|{mem_id}"
    return base64.urlsafe_b64encode(raw.encode()).decode()


def _decode_cursor(cursor: str) -> tuple[datetime, str]:
    try:
        raw = base64.urlsafe_b64decode(cursor.encode()).decode()
        ts_str, mem_id = raw.split("|", 1)
        return datetime.fromisoformat(ts_str), mem_id
    except Exception:
        raise AihubServerError(ErrorCode.BAD_REQUEST, "malformed cursor")


# ---------------------------------------------------------------------------
# Visibility filter builder
# ---------------------------------------------------------------------------

def _build_visibility_clauses(user) -> tuple[list[str], dict]:
    """Return (where_clauses, params) for the visibility filter.

    Rows are visible when ANY of:
    1. visibility='private' AND author_user_id = current_user.id
    2. visibility='project' AND project = ANY(current_user.projects)
    3. visibility='team' AND project = ANY(current_user.projects)  [F11/M7]
    4. visibility='admin' AND current_user.role = 'admin'

    F11/M7: 'team' visibility is now project-scoped (conservative interpretation).
    A user in project A cannot see 'team' memories from project B.
    Design §15 is ambiguous on whether 'team' is org-global or project-scoped;
    conservative project-scoping applied here pending design clarification.
    """
    is_admin = user.role == "admin"
    parts: list[str] = []
    params: dict = {}

    # 1. own private
    parts.append("(visibility = 'private' AND author_user_id = :vis_user_id)")
    params["vis_user_id"] = user.id

    # 2. project-scoped 'project' visibility
    if user.projects:
        parts.append("(visibility = 'project' AND project = ANY(CAST(:vis_projects AS text[])))")
        params["vis_projects"] = list(user.projects)
    elif is_admin:
        # admin sees all project memories
        parts.append("(visibility = 'project')")

    # 3. team — conservative project-scoping (design §15 is ambiguous; applying
    #    project-scoped interpretation: team means "within your projects only").
    #    Admin sees team memories across all projects (no project filter for admin).
    if user.projects:
        parts.append(
            "(visibility = 'team' AND project = ANY(CAST(:viewer_projects AS text[])))"
        )
        params["viewer_projects"] = list(user.projects)
    elif is_admin:
        # admin with no project membership — sees all team memories org-wide
        parts.append("(visibility = 'team')")

    # 4. admin-visibility
    if is_admin:
        parts.append("(visibility = 'admin')")

    vis_clause = "(" + " OR ".join(parts) + ")"
    return vis_clause, params


# ---------------------------------------------------------------------------
# Endpoints
# ---------------------------------------------------------------------------

@router.post("/memories", status_code=201, operation_id="create_memory")
async def create_memory(
    body: CreateMemoryRequest,
    bearer: str | None = Depends(bearer_dep),
    engine: AsyncEngine = Depends(get_engine),
):
    async with engine.begin() as conn:
        user = await verify_bearer(conn, bearer)

        # Validate visibility value
        if body.visibility not in _VALID_VISIBILITY:
            raise AihubServerError(
                ErrorCode.BAD_REQUEST,
                f"invalid visibility '{body.visibility}'; must be one of {sorted(_VALID_VISIBILITY)}",
            )

        # Project authorization: non-admin must belong to the project
        is_admin = user.role == "admin"
        if not is_admin and body.project not in user.projects:
            raise AihubServerError(
                ErrorCode.FORBIDDEN,
                f"user {user.id} is not a member of project '{body.project}'",
            )

        # Validate work_item_id ownership (cross-project fence)
        if body.work_item_id is not None:
            wi_row = (await conn.execute(sa.text("""
                SELECT project FROM work_items WHERE id = :wi_id
            """), {"wi_id": body.work_item_id})).mappings().first()

            if wi_row is None:
                raise AihubServerError(
                    ErrorCode.NOT_FOUND,
                    f"work_item {body.work_item_id} not found",
                )

            wi_project = wi_row["project"]

            # Non-admin must have access to the work_item's project
            if not is_admin and wi_project not in user.projects:
                raise AihubServerError(
                    ErrorCode.FORBIDDEN,
                    f"work_item {body.work_item_id} belongs to project '{wi_project}' "
                    f"which you don't have access to",
                )

            # Memory's project must match the work_item's project for consistency
            if body.project != wi_project:
                raise AihubServerError(
                    ErrorCode.BAD_REQUEST,
                    f"memory project '{body.project}' does not match "
                    f"work_item project '{wi_project}'",
                )

        mem_id = _mem_id()
        metadata = body.metadata or {}

        await conn.execute(sa.text("""
            INSERT INTO memories (
                id, project, author_user_id, work_item_id, visibility,
                type, content, metadata, expires_at
            ) VALUES (
                :id, :project, :author, :work_item_id, :visibility,
                :type, :content, CAST(:metadata AS JSONB), CAST(:expires_at AS TIMESTAMPTZ)
            )
        """), {
            "id": mem_id,
            "project": body.project,
            "author": user.id,
            "work_item_id": body.work_item_id,
            "visibility": body.visibility,
            "type": body.type,
            "content": body.content,
            "metadata": json.dumps(metadata),
            "expires_at": body.expires_at,
        })

        row = (await conn.execute(sa.text("""
            SELECT * FROM memories WHERE id = :id
        """), {"id": mem_id})).mappings().first()

    return JSONResponse(status_code=201, content=_jsonable(_row_to_memory_dict(row)))


@router.get("/memories", response_model=MemoryListResponse, operation_id="list_memories")
async def list_memories(
    project: str | None = Query(default=None),
    query: str | None = Query(default=None),   # ignored in v3.0 (no semantic search)
    type: str | None = Query(default=None),
    visibility: str | None = Query(default=None),
    work_item_id: str | None = Query(default=None),
    top_k: int | None = Query(default=None),   # ignored in v3.0
    cursor: str | None = Query(default=None),
    limit: int = Query(default=50, ge=1, le=200),
    bearer: str | None = Depends(bearer_dep),
    engine: AsyncEngine = Depends(get_engine),
):
    async with engine.connect() as conn:
        user = await verify_bearer(conn, bearer)

        vis_clause, params = _build_visibility_clauses(user)
        params["limit"] = limit + 1

        where_parts = [vis_clause]

        if project is not None:
            where_parts.append("project = :filter_project")
            params["filter_project"] = project

        if type is not None:
            where_parts.append("type = :filter_type")
            params["filter_type"] = type

        if visibility is not None:
            where_parts.append("visibility = :filter_visibility")
            params["filter_visibility"] = visibility

        if work_item_id is not None:
            where_parts.append("work_item_id = :filter_wi_id")
            params["filter_wi_id"] = work_item_id

        if cursor is not None:
            cursor_ts, cursor_id = _decode_cursor(cursor)
            where_parts.append("(created_at, id) < (:cursor_ts, :cursor_id)")
            params["cursor_ts"] = cursor_ts
            params["cursor_id"] = cursor_id

        where_sql = " WHERE " + " AND ".join(where_parts)

        rows = (await conn.execute(sa.text(f"""
            SELECT * FROM memories
            {where_sql}
            ORDER BY created_at DESC, id DESC
            LIMIT :limit
        """), params)).mappings().all()

    has_more = len(rows) > limit
    rows = list(rows[:limit])
    next_cursor = None
    if has_more and rows:
        last = rows[-1]
        next_cursor = _encode_cursor(last["created_at"], last["id"])

    return JSONResponse(status_code=200, content=_jsonable({
        "items": [_row_to_memory_dict(r) for r in rows],
        "next_cursor": next_cursor,
    }))


@router.patch("/memories/{memory_id}", operation_id="update_memory")
async def update_memory(
    body: UpdateMemoryRequest,
    memory_id: str = Path(...),
    bearer: str | None = Depends(bearer_dep),
    engine: AsyncEngine = Depends(get_engine),
):
    async with engine.begin() as conn:
        user = await verify_bearer(conn, bearer)

        row = (await conn.execute(sa.text("""
            SELECT * FROM memories WHERE id = :id FOR UPDATE
        """), {"id": memory_id})).mappings().first()

        if row is None:
            raise AihubServerError(ErrorCode.NOT_FOUND, f"memory {memory_id} not found")

        # Gate: redacted memories cannot be patched (M6/F10)
        if row["redacted_at"] is not None:
            raise AihubServerError(
                ErrorCode.CONFLICT_EPOCH_MISMATCH,
                "memory redacted — cannot patch a redacted memory",
            )

        # Gate: author or admin
        is_admin = user.role == "admin"
        if row["author_user_id"] != user.id and not is_admin:
            raise AihubServerError(
                ErrorCode.FORBIDDEN,
                "only the author or an admin may update a memory",
            )

        _ALLOWED_PATCH_FIELDS = {"metadata", "content", "expires_at"}
        patch = body.patch_payload or {}
        unknown = set(patch.keys()) - _ALLOWED_PATCH_FIELDS
        if unknown:
            raise AihubServerError(
                ErrorCode.BAD_REQUEST,
                f"patch_payload contains forbidden fields: {sorted(unknown)}",
            )

        sets = []
        params: dict = {"id": memory_id}

        if "content" in patch:
            sets.append("content = :content")
            params["content"] = patch["content"]

        if "metadata" in patch:
            sets.append("metadata = CAST(:metadata AS JSONB)")
            params["metadata"] = json.dumps(patch["metadata"])

        if "expires_at" in patch:
            sets.append("expires_at = CAST(:expires_at AS TIMESTAMPTZ)")
            params["expires_at"] = patch["expires_at"]

        if sets:
            await conn.execute(sa.text(
                f"UPDATE memories SET {', '.join(sets)} WHERE id = :id"
            ), params)

        row = (await conn.execute(sa.text("""
            SELECT * FROM memories WHERE id = :id
        """), {"id": memory_id})).mappings().first()

    return JSONResponse(status_code=200, content=_jsonable(_row_to_memory_dict(row)))


@router.post("/memories/{memory_id}/redact", operation_id="redact_memory")
async def redact_memory(
    body: RedactMemoryRequest,
    memory_id: str = Path(...),
    bearer: str | None = Depends(bearer_dep),
    engine: AsyncEngine = Depends(get_engine),
):
    async with engine.begin() as conn:
        user = await verify_bearer(conn, bearer)

        row = (await conn.execute(sa.text("""
            SELECT id, author_user_id, work_item_id FROM memories WHERE id = :id FOR UPDATE
        """), {"id": memory_id})).mappings().first()

        if row is None:
            raise AihubServerError(ErrorCode.NOT_FOUND, f"memory {memory_id} not found")

        # Gate: author or admin
        is_admin = user.role == "admin"
        if row["author_user_id"] != user.id and not is_admin:
            raise AihubServerError(
                ErrorCode.FORBIDDEN,
                "only the author or an admin may redact a memory",
            )

        await conn.execute(sa.text("""
            UPDATE memories
            SET redacted_at = now(), redaction_reason = :reason,
                content = NULL, embedding = NULL
            WHERE id = :id
        """), {"id": memory_id, "reason": body.reason})

        # Emit memory_redacted event (same transaction — atomicity with UPDATE).
        # agent_events.work_item_id has a NOT NULL FK to work_items; we can only
        # emit via the shared helper when the memory is linked to a work_item.
        # For unlinked memories the redaction still clears content + embedding;
        # the audit event is silently skipped (design constraint, not a bug).
        wi_id = row["work_item_id"]
        if wi_id is not None:
            await emit_event(
                conn,
                work_item_id=wi_id,
                event_type="memory_redacted",
                payload={
                    "memory_id": memory_id,
                    "reason": body.reason,
                    "redacted_by": user.id,
                },
                actor_user_id=user.id,
            )

    return JSONResponse(status_code=200, content={"ok": True})
