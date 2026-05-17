"""Artifact reconcile helpers — adopt / ignore / close.

Design §13: client-driven (gh CLI / Jira token lives on client). Server just
records the human decision via events + per-repo state CAS for adopt.
"""
from __future__ import annotations

import json
from typing import Any

import sqlalchemy as sa
from sqlalchemy.ext.asyncio import AsyncConnection

from app.auth import AttemptRecord, UserRecord
from app.errors import AihubServerError, ErrorCode
from app.events import emit_event


# ---------------------------------------------------------------------------
# Helpers
# ---------------------------------------------------------------------------

async def _select_wi_for_update(conn, wi_id: str) -> dict:
    row = (await conn.execute(sa.text("""
        SELECT id, declared_resources, resources_version
        FROM work_items WHERE id = :id FOR UPDATE
    """), {"id": wi_id})).mappings().first()
    if row is None:
        raise AihubServerError(ErrorCode.NOT_FOUND, f"work_item {wi_id}")
    return dict(row)


def _find_repo_resource(declared: list[dict], repo: str) -> int | None:
    """Return index of declared_resources entry matching repo URI/short name."""
    target = repo if repo.startswith("repo:") else f"repo:{repo}"
    for i, r in enumerate(declared):
        if r.get("type") == "repo" and r.get("uri") == target:
            return i
    # try short-name match
    for i, r in enumerate(declared):
        if r.get("type") == "repo" and r.get("uri", "")[5:] == repo:
            return i
    return None


async def adopt_artifact(
    conn: AsyncConnection,
    *,
    user: UserRecord,
    attempt: AttemptRecord,
    wi_id: str,
    artifact_type: str,
    identifier: str,
    repo: str,
    expected_resources_version: int | None = None,
) -> None:
    """Attach artifact to declared_resources[repo] runtime fields.

    For pr type: set last_pr_number on the matching repo entry, bump version.
    Per §7.6 CAS protocol: only current_attempt may modify, resources_version
    bumped atomically.

    If expected_resources_version is provided, the UPDATE WHERE clause adds
    AND resources_version = :expected_version for optimistic concurrency control.
    If the row is not updated (stale version), raises CONFLICT_VERSION_MISMATCH.
    """
    wi = await _select_wi_for_update(conn, wi_id)
    if attempt.work_item_id != wi_id:
        raise AihubServerError(
            ErrorCode.FORBIDDEN, "attempt does not belong to this work_item",
        )
    declared = list(wi["declared_resources"] or [])
    idx = _find_repo_resource(declared, repo)
    if idx is None:
        raise AihubServerError(
            ErrorCode.NOT_FOUND,
            f"no declared repo resource matching {repo}",
        )
    entry = dict(declared[idx])
    entry["version"] = int(entry.get("version", 0)) + 1
    if artifact_type == "pr":
        try:
            entry["last_pr_number"] = int(identifier)
        except ValueError:
            raise AihubServerError(
                ErrorCode.BAD_REQUEST, "pr identifier must be integer",
            )
        entry["state"] = "pr_opened"
    elif artifact_type == "branch":
        entry["task_branch"] = identifier
    elif artifact_type == "issue":
        # attach issue ref into metadata
        meta = dict(entry.get("metadata") or {})
        meta["issue_ref"] = identifier
        entry["metadata"] = meta
    declared[idx] = entry

    # Build WHERE clause — optionally include CAS on resources_version
    params: dict[str, Any] = {
        "dr": json.dumps(declared), "id": wi_id, "aid": attempt.id,
    }
    version_clause = ""
    if expected_resources_version is not None:
        version_clause = " AND resources_version = :expected_version"
        params["expected_version"] = expected_resources_version

    result = await conn.execute(sa.text(f"""
        UPDATE work_items
        SET declared_resources = CAST(:dr AS JSONB),
            resources_version = resources_version + 1,
            updated_at = now()
        WHERE id = :id AND current_attempt_id = :aid{version_clause}
        RETURNING resources_version
    """), params)
    if result.rowcount == 0:
        raise AihubServerError(
            ErrorCode.CONFLICT_EPOCH_MISMATCH,
            "resources_version mismatch — concurrent update; refetch and retry",
        )

    await emit_event(
        conn, work_item_id=wi_id, event_type="external_artifact_reconciled",
        payload={"action": "adopt", "type": artifact_type,
                 "identifier": identifier, "repo": repo},
        actor_user_id=user.id, api_key_id=user.matched_api_key_id,
        run_attempt_id=attempt.id,
    )


async def ignore_artifact(
    conn: AsyncConnection,
    *,
    user: UserRecord,
    attempt: AttemptRecord,
    wi_id: str,
    artifact_type: str,
    identifier: str,
    repo: str,
) -> None:
    """Append entry to metadata.ignored_artifacts and emit event."""
    wi = (await conn.execute(sa.text("""
        SELECT id, metadata FROM work_items WHERE id = :id FOR UPDATE
    """), {"id": wi_id})).mappings().first()
    if wi is None:
        raise AihubServerError(ErrorCode.NOT_FOUND, f"work_item {wi_id}")
    if attempt.work_item_id != wi_id:
        raise AihubServerError(
            ErrorCode.FORBIDDEN, "attempt does not belong to this work_item",
        )
    meta = dict(wi["metadata"] or {})
    ignored = list(meta.get("ignored_artifacts") or [])
    ignored.append({"type": artifact_type, "identifier": identifier, "repo": repo})
    meta["ignored_artifacts"] = ignored

    await conn.execute(sa.text("""
        UPDATE work_items
        SET metadata = CAST(:m AS JSONB), updated_at = now()
        WHERE id = :id
    """), {"m": json.dumps(meta), "id": wi_id})

    await emit_event(
        conn, work_item_id=wi_id, event_type="external_artifact_reconciled",
        payload={"action": "ignore", "type": artifact_type,
                 "identifier": identifier, "repo": repo},
        actor_user_id=user.id, api_key_id=user.matched_api_key_id,
        run_attempt_id=attempt.id,
    )


async def close_artifact(
    conn: AsyncConnection,
    *,
    user: UserRecord,
    attempt: AttemptRecord,
    wi_id: str,
    artifact_type: str,
    identifier: str,
    repo: str,
) -> None:
    """Record that client closed an external artifact (e.g. gh pr close)."""
    wi = (await conn.execute(sa.text("""
        SELECT id FROM work_items WHERE id = :id
    """), {"id": wi_id})).first()
    if wi is None:
        raise AihubServerError(ErrorCode.NOT_FOUND, f"work_item {wi_id}")
    if attempt.work_item_id != wi_id:
        raise AihubServerError(
            ErrorCode.FORBIDDEN, "attempt does not belong to this work_item",
        )
    await emit_event(
        conn, work_item_id=wi_id, event_type="external_artifact_reconciled",
        payload={"action": "closed", "type": artifact_type,
                 "identifier": identifier, "repo": repo},
        actor_user_id=user.id, api_key_id=user.matched_api_key_id,
        run_attempt_id=attempt.id,
    )
