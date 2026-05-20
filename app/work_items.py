"""Work item service helpers — insert / list / detail.

Per design §5 (schema), §6.2 (perms), §7.1 (creation paths), §7.6 (per-repo
state surfacing). Routes call these and stay thin.
"""
from __future__ import annotations

import json
from typing import Any

import sqlalchemy as sa
from sqlalchemy.ext.asyncio import AsyncConnection
from ulid import ULID

from app.auth import UserRecord
from app.errors import AihubServerError, ErrorCode


WI_ID_PREFIX = "wi_"


def _wi_id() -> str:
    return WI_ID_PREFIX + str(ULID()).lower()


def _evt_id() -> str:
    return "evt_" + str(ULID()).lower()


# ---------------------------------------------------------------------------
# Validation
# ---------------------------------------------------------------------------

_ALLOWED_TYPES = {"repo", "path", "document", "section", "service", "external_ref"}
_ALLOWED_INTENTS = {"read", "write", "refactor", "delete"}


def _validate_declared_resources(declared: list[dict]) -> None:
    for i, r in enumerate(declared):
        if not isinstance(r, dict):
            raise AihubServerError(ErrorCode.BAD_REQUEST,
                                   f"declared_resources[{i}] not an object")
        for f in ("type", "uri", "intent"):
            if f not in r:
                raise AihubServerError(ErrorCode.BAD_REQUEST,
                                       f"declared_resources[{i}] missing field '{f}'")
        if r["type"] not in _ALLOWED_TYPES:
            raise AihubServerError(ErrorCode.BAD_REQUEST,
                                   f"declared_resources[{i}].type invalid")
        if r["intent"] not in _ALLOWED_INTENTS:
            raise AihubServerError(ErrorCode.BAD_REQUEST,
                                   f"declared_resources[{i}].intent invalid")


# ---------------------------------------------------------------------------
# Insert
# ---------------------------------------------------------------------------

async def insert_work_item(
    conn: AsyncConnection,
    *,
    reporter: UserRecord,
    body: dict,
) -> dict:
    """Create a work_item + emit work_item_filed event.

    Permission: writer + reporter.project ∈ user.projects.

    Source fencing per design.md §7.1:
    - Missing/falsy source defaults to 'human'.
    - source='auto:*' (Path B) requires metadata.created_by_attempt_id pointing
      to a running attempt owned by the caller. Prevents human writers from
      injecting auto-source items that corrupt /pf3-status and parent-child
      auto-tree analytics.
    - source='sync:*' is rejected from non-admin writers (sync paths are
      external-system only; not exposed in v3.0).
    """
    project = body["project"]
    if project not in reporter.projects:
        raise AihubServerError(ErrorCode.FORBIDDEN,
                               f"user {reporter.id} not in project {project}")
    if reporter.role not in ("writer", "admin"):
        raise AihubServerError(ErrorCode.FORBIDDEN,
                               "writer or admin role required to create work_item")

    declared = body.get("declared_resources", [])
    _validate_declared_resources(declared)

    metadata = dict(body.get("metadata") or {})
    source = body.get("source") or "human"  # default to 'human' if falsy/missing

    # Fence auto:* sources — require an active attempt context (§7.1 Path B)
    if source.startswith("auto:"):
        created_by_attempt_id = metadata.get("created_by_attempt_id")
        if not created_by_attempt_id:
            raise AihubServerError(
                ErrorCode.BAD_REQUEST,
                "source='auto:*' requires metadata.created_by_attempt_id "
                "(must reference a running attempt owned by caller)",
            )
        # Verify the referenced attempt exists, is running, and belongs to caller
        ra_row = (await conn.execute(sa.text("""
            SELECT id, status, actor_user_id FROM run_attempts
            WHERE id = :aid AND status = 'running' AND actor_user_id = :uid
        """), {"aid": created_by_attempt_id, "uid": reporter.id})).mappings().first()
        if ra_row is None:
            raise AihubServerError(
                ErrorCode.FORBIDDEN,
                f"attempt {created_by_attempt_id!r} is not a running attempt "
                "owned by the calling user — auto:* source requires an active context",
            )

    # Reject sync:* from non-admin writers (external sync not exposed in v3.0)
    if source.startswith("sync:") and reporter.role != "admin":
        raise AihubServerError(
            ErrorCode.FORBIDDEN,
            "source='sync:*' is reserved for admin/external-sync paths; "
            "use 'human' or 'auto:*' (with active attempt) instead",
        )

    metadata.setdefault("source", source)

    wi_id = _wi_id()
    priority = body.get("priority") or "normal"

    await conn.execute(sa.text("""
        INSERT INTO work_items (
            id, project, scenario, goal, status, priority, labels,
            reporter_user_id, declared_resources, resources_version,
            external_share_type, external_share_key, parent_work_item_id,
            metadata
        ) VALUES (
            :id, :project, :scenario, :goal, 'queued', :priority,
            CAST(:labels AS JSONB),
            :reporter, CAST(:declared AS JSONB), 0,
            :est, :esk, :parent,
            CAST(:metadata AS JSONB)
        )
    """), {
        "id": wi_id, "project": project,
        "scenario": body["scenario"], "goal": body["goal"],
        "priority": priority,
        "labels": json.dumps(body.get("labels") or []),
        "reporter": reporter.id,
        "declared": json.dumps(declared),
        "est": body.get("external_share_type"),
        "esk": body.get("external_share_key"),
        "parent": body.get("parent_work_item_id"),
        "metadata": json.dumps(metadata),
    })

    # work_item_filed event
    await conn.execute(sa.text("""
        INSERT INTO agent_events (id, work_item_id, actor_user_id, event_type, payload)
        VALUES (:eid, :wid, :uid, 'work_item_filed', CAST(:payload AS JSONB))
    """), {
        "eid": _evt_id(), "wid": wi_id, "uid": reporter.id,
        "payload": json.dumps({
            "work_item_id": wi_id, "project": project,
            "source": metadata.get("source", "human"),
            "goal": body["goal"],
        }),
    })

    return await _select_work_item_row(conn, wi_id)


# ---------------------------------------------------------------------------
# List
# ---------------------------------------------------------------------------

async def list_work_items(
    conn: AsyncConnection,
    *,
    user: UserRecord,
    project: str | None,
    status: str | None,
    label: str | None,
    user_id: str | None,
    source: str | None,
    since: str | None,
    cursor: str | None,
    limit: int,
) -> dict:
    """Filtered + paginated list. Cursor is base64-encoded (created_at, id).

    For 1A we use a simple opaque-string cursor of the form
    '<iso8601>|<id>'. Caller passes back the next_cursor verbatim.

    Permission: visible projects = user.projects (admin sees all). If project
    filter supplied and not in projects → 403.
    """
    if limit <= 0 or limit > 200:
        raise AihubServerError(ErrorCode.BAD_REQUEST, "limit out of range")

    is_admin = user.role == "admin"
    if project is not None and not is_admin and project not in user.projects:
        raise AihubServerError(ErrorCode.FORBIDDEN,
                               f"user not in project {project}")

    where_clauses: list[str] = []
    params: dict[str, Any] = {"limit": limit + 1}

    if project is not None:
        where_clauses.append("project = :project")
        params["project"] = project
    elif not is_admin:
        # Restrict to user's projects
        where_clauses.append(
            "project = ANY(CAST(:projects AS text[]))"
        )
        params["projects"] = list(user.projects)
    if status is not None:
        statuses = [status] if isinstance(status, str) else list(status)
        where_clauses.append("status = ANY(CAST(:statuses AS text[]))")
        params["statuses"] = statuses
    if user_id is not None:
        where_clauses.append("reporter_user_id = :uid")
        params["uid"] = user_id
    if label is not None:
        where_clauses.append("labels @> CAST(:label_json AS JSONB)")
        params["label_json"] = json.dumps([label])
    if source is not None:
        where_clauses.append("metadata->>'source' = :source")
        params["source"] = source
    if since is not None:
        where_clauses.append("created_at >= CAST(:since AS TIMESTAMPTZ)")
        params["since"] = since
    if cursor is not None:
        from datetime import datetime
        try:
            cursor_ts_str, cursor_id = cursor.split("|", 1)
            cursor_ts = datetime.fromisoformat(cursor_ts_str)
        except (ValueError, TypeError):
            raise AihubServerError(ErrorCode.BAD_REQUEST, "malformed cursor")
        where_clauses.append(
            "(created_at, id) < (:cursor_ts, :cursor_id)"
        )
        params["cursor_ts"] = cursor_ts
        params["cursor_id"] = cursor_id

    where_sql = " WHERE " + " AND ".join(where_clauses) if where_clauses else ""
    rows = (await conn.execute(sa.text(f"""
        SELECT * FROM work_items
        {where_sql}
        ORDER BY created_at DESC, id DESC
        LIMIT :limit
    """), params)).mappings().all()

    has_more = len(rows) > limit
    rows = rows[:limit]
    next_cursor = None
    if has_more and rows:
        last = rows[-1]
        next_cursor = f"{last['created_at'].isoformat()}|{last['id']}"

    return {
        "items": [_row_to_wi_dict(r) for r in rows],
        "next_cursor": next_cursor,
    }


# ---------------------------------------------------------------------------
# Detail
# ---------------------------------------------------------------------------

async def get_work_item_detail(
    conn: AsyncConnection,
    *,
    wi_id: str,
    user: UserRecord,
) -> dict:
    wi_row = await _select_work_item_row_or_404(conn, wi_id)
    project = wi_row["project"]
    if user.role != "admin" and project not in user.projects:
        raise AihubServerError(ErrorCode.FORBIDDEN,
                               f"user not in project {project}")

    current_attempt = None
    if wi_row["current_attempt_id"] is not None:
        a_row = (await conn.execute(sa.text("""
            SELECT * FROM run_attempts WHERE id = :aid
        """), {"aid": wi_row["current_attempt_id"]})).mappings().first()
        if a_row is not None:
            current_attempt = _row_to_attempt_dict(a_row)

    # last 50 events for this work_item
    ev_rows = (await conn.execute(sa.text("""
        SELECT * FROM agent_events WHERE work_item_id = :wid
        ORDER BY created_at DESC, id DESC LIMIT 50
    """), {"wid": wi_id})).mappings().all()
    events = [_row_to_event_dict(r) for r in ev_rows]

    # per_repo_state: every declared resource surfaces runtime fields
    per_repo_state = _aggregate_per_repo_state(wi_row["declared_resources"])

    return {
        "work_item": wi_row,
        "current_attempt": current_attempt,
        "recent_events": events,
        "per_repo_state": per_repo_state,
    }


# ---------------------------------------------------------------------------
# Helpers
# ---------------------------------------------------------------------------

async def _select_work_item_row(conn: AsyncConnection, wi_id: str) -> dict:
    row = (await conn.execute(sa.text("""
        SELECT * FROM work_items WHERE id = :id
    """), {"id": wi_id})).mappings().first()
    if row is None:
        raise AihubServerError(ErrorCode.NOT_FOUND, f"work_item {wi_id}")
    return _row_to_wi_dict(row)


async def _select_work_item_row_or_404(conn: AsyncConnection, wi_id: str) -> dict:
    return await _select_work_item_row(conn, wi_id)


def _row_to_wi_dict(row) -> dict:
    d = dict(row)
    # JSONB columns are already loaded as python objects by asyncpg; convert
    # datetime to ISO format string for JSON serialization
    return d


def _row_to_attempt_dict(row) -> dict:
    return dict(row)


def _row_to_event_dict(row) -> dict:
    return dict(row)


_PER_REPO_RUNTIME_FIELDS = (
    "state", "version", "base_branch", "task_branch",
    "last_sha", "last_pr_number", "error", "metadata",
)


def _aggregate_per_repo_state(declared_resources: list[dict]) -> list[dict]:
    """Surface PerRepoState entries from declared_resources runtime fields.

    Per §5.1 r3: intent-time and runtime are co-stored in the same JSONB
    element; the per_repo_state response splits them apart. Each declared
    resource contributes one PerRepoState (even path-type, which is what the
    reference scenario does — wi_a3f has both repo:marketplace and
    file:marketplace/src/auth/**, both reported with state).
    """
    out = []
    for r in declared_resources or []:
        out.append({
            "type": r["type"],
            "uri": r["uri"],
            "intent": r.get("intent"),
            "state": r.get("state", "declared"),
            "version": r.get("version", 0),
            "base_branch": r.get("base_branch"),
            "task_branch": r.get("task_branch"),
            "last_sha": r.get("last_sha"),
            "last_pr_number": r.get("last_pr_number"),
            "error": r.get("error"),
            "metadata": r.get("metadata", {}),
        })
    return out
