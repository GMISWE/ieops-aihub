"""POST /v1/admin/users|keys|keys/revoke — admin-only provisioning endpoints.

All three endpoints require role='admin'. Design §17.4 covers key revocation
cascade. Bearer auth via existing app.auth helpers.
"""
from __future__ import annotations

import hashlib
import json
import secrets
from datetime import datetime, timezone
from typing import List

import sqlalchemy as sa
from fastapi import APIRouter, Depends
from fastapi.responses import JSONResponse
from pydantic import BaseModel
from sqlalchemy.ext.asyncio import AsyncEngine
from ulid import ULID

from app.auth import bearer_dep, verify_bearer
from app.errors import AihubServerError, ErrorCode
from app.v3_app import get_engine


router = APIRouter(tags=["admin"])


# ---------------------------------------------------------------------------
# Pydantic models (inline — do NOT touch app/schemas.py)
# ---------------------------------------------------------------------------

class AdminAddUserRequest(BaseModel):
    email: str
    display_name: str
    role: str  # 'reader' | 'writer' | 'admin'
    projects: List[str]


class AdminAddUserResponse(BaseModel):
    user_id: str
    api_key: str


class AdminCreateKeyRequest(BaseModel):
    user_id: str
    scopes: List[str]


class AdminCreateKeyResponse(BaseModel):
    key_id: str
    api_key: str


class AdminRevokeKeyRequest(BaseModel):
    key_id: str


class AdminRevokeKeyResponse(BaseModel):
    ok: bool
    terminated_attempts: List[str]


# ---------------------------------------------------------------------------
# Helpers
# ---------------------------------------------------------------------------

def _user_id() -> str:
    return "u_" + str(ULID()).lower()


def _key_id() -> str:
    return "ak_" + str(ULID()).lower()


def _generate_api_key() -> tuple[str, str]:
    """Return (raw_key, key_hash).

    key_hash = 'argon2id$' + sha256(raw_key) to match the auth.py
    _verify_api_key_hash convention (sha256 prefix inside argon2id envelope is
    NOT supported by the verifier; we use 'argon2id$' + sha256hex so that
    _verify_api_key_hash falls through to the literal-compare branch — which
    means callers must present the raw key, and the verifier checks it via
    hmac.compare_digest(raw_bearer, stored_hash). That only works if
    stored_hash == raw_bearer, which is wrong for random keys.

    Correct approach: match the sha256$ prefix that _verify_api_key_hash
    actually handles for non-dummy keys.
    """
    raw = secrets.token_hex(32)
    digest = hashlib.sha256(raw.encode("utf-8")).hexdigest()
    key_hash = "sha256$" + digest
    return raw, key_hash


def _now_iso() -> str:
    return datetime.now(timezone.utc).isoformat()


def _evt_id() -> str:
    return "evt_" + str(ULID()).lower()


def _require_admin(user) -> None:
    if user.role != "admin":
        raise AihubServerError(ErrorCode.FORBIDDEN, "admin role required")


# ---------------------------------------------------------------------------
# POST /v1/admin/users
# ---------------------------------------------------------------------------

@router.post("/admin/users", status_code=201, operation_id="admin_add_user",
             response_model=AdminAddUserResponse)
async def admin_add_user(
    body: AdminAddUserRequest,
    bearer: str | None = Depends(bearer_dep),
    engine: AsyncEngine = Depends(get_engine),
):
    async with engine.begin() as conn:
        caller = await verify_bearer(conn, bearer)
        _require_admin(caller)

        if body.role not in ("reader", "writer", "admin"):
            raise AihubServerError(ErrorCode.BAD_REQUEST,
                                   f"invalid role: {body.role!r}")

        uid = _user_id()
        raw_key, key_hash = _generate_api_key()
        key_entry = {
            "id": _key_id(),
            "key_hash": key_hash,
            "scopes": [],
            "created_at": _now_iso(),
            "revoked_at": None,
        }

        await conn.execute(sa.text("""
            INSERT INTO users (id, email, display_name, role, projects, api_keys)
            VALUES (:id, :email, :display_name, :role,
                    CAST(:projects AS JSONB), CAST(:api_keys AS JSONB))
        """), {
            "id": uid,
            "email": body.email,
            "display_name": body.display_name,
            "role": body.role,
            "projects": json.dumps(body.projects),
            "api_keys": json.dumps([key_entry]),
        })

    return JSONResponse(status_code=201, content={
        "user_id": uid,
        "api_key": raw_key,
    })


# ---------------------------------------------------------------------------
# POST /v1/admin/keys
# ---------------------------------------------------------------------------

@router.post("/admin/keys", status_code=201, operation_id="admin_create_key",
             response_model=AdminCreateKeyResponse)
async def admin_create_key(
    body: AdminCreateKeyRequest,
    bearer: str | None = Depends(bearer_dep),
    engine: AsyncEngine = Depends(get_engine),
):
    async with engine.begin() as conn:
        caller = await verify_bearer(conn, bearer)
        _require_admin(caller)

        # Verify target user exists and read current version (optimistic lock)
        row = (await conn.execute(sa.text(
            "SELECT id, api_keys, version FROM users WHERE id = :uid FOR UPDATE"
        ), {"uid": body.user_id})).mappings().first()
        if row is None:
            raise AihubServerError(ErrorCode.NOT_FOUND,
                                   f"user {body.user_id!r} not found")

        expected_version = int(row["version"])
        kid = _key_id()
        raw_key, key_hash = _generate_api_key()
        new_entry = {
            "id": kid,
            "key_hash": key_hash,
            "scopes": list(body.scopes),
            "created_at": _now_iso(),
            "revoked_at": None,
        }

        result = await conn.execute(sa.text("""
            UPDATE users
            SET api_keys = api_keys || CAST(:entry AS JSONB),
                version = version + 1
            WHERE id = :uid AND version = :expected_version
        """), {
            "uid": body.user_id,
            "entry": json.dumps([new_entry]),
            "expected_version": expected_version,
        })
        if result.rowcount == 0:
            raise AihubServerError(
                ErrorCode.CONFLICT_EPOCH_MISMATCH,
                f"version mismatch on user {body.user_id} — concurrent admin mutation; retry",
            )

    return JSONResponse(status_code=201, content={
        "key_id": kid,
        "api_key": raw_key,
    })


# ---------------------------------------------------------------------------
# POST /v1/admin/keys/revoke  — CASCADE per design §17.4
# ---------------------------------------------------------------------------

@router.post("/admin/keys/revoke", operation_id="admin_revoke_key",
             response_model=AdminRevokeKeyResponse)
async def admin_revoke_key(
    body: AdminRevokeKeyRequest,
    bearer: str | None = Depends(bearer_dep),
    engine: AsyncEngine = Depends(get_engine),
):
    async with engine.begin() as conn:
        caller = await verify_bearer(conn, bearer)
        _require_admin(caller)

        key_id = body.key_id

        # 1. Find the user that owns this key and read current version (optimistic lock)
        user_row = (await conn.execute(sa.text("""
            SELECT id, api_keys, version
            FROM users
            WHERE api_keys @> CAST(:filter AS JSONB)
            FOR UPDATE
        """), {"filter": json.dumps([{"id": key_id}])})).mappings().first()

        if user_row is None:
            raise AihubServerError(ErrorCode.NOT_FOUND,
                                   f"api key {key_id!r} not found")

        expected_version = int(user_row["version"])

        # 2. Set revoked_at on the matching key entry (find array index)
        api_keys: list = list(user_row["api_keys"])
        idx = next((i for i, k in enumerate(api_keys) if k.get("id") == key_id), None)
        if idx is None:
            raise AihubServerError(ErrorCode.NOT_FOUND,
                                   f"api key {key_id!r} not found in user record")

        now_ts = _now_iso()
        # Use jsonb_set with a literal array path (idx is a safe integer we control).
        # asyncpg cannot bind TEXT[] via sa.text params; use f-string for the path literal.
        path_literal = "{" + str(idx) + ",revoked_at}"
        result = await conn.execute(sa.text(f"""
            UPDATE users
            SET api_keys = jsonb_set(
                api_keys,
                '{path_literal}'::TEXT[],
                CAST(:revoked_at AS JSONB)
            ),
            version = version + 1
            WHERE id = :uid AND version = :expected_version
        """), {
            "uid": user_row["id"],
            "revoked_at": json.dumps(now_ts),
            "expected_version": expected_version,
        })
        if result.rowcount == 0:
            raise AihubServerError(
                ErrorCode.CONFLICT_EPOCH_MISMATCH,
                f"version mismatch on user {user_row['id']} — concurrent admin mutation; retry",
            )

        # 3. Find all running attempts using this key
        running_rows = (await conn.execute(sa.text("""
            SELECT id, work_item_id
            FROM run_attempts
            WHERE api_key_id = :kid AND status = 'running'
        """), {"kid": key_id})).mappings().all()

        terminated_ids: list[str] = []

        for attempt in running_rows:
            aid = attempt["id"]
            wid = attempt["work_item_id"]
            terminated_ids.append(aid)

            # 4a. Mark attempt as failed (§17.4 uses 'failed'; schema has no 'terminated')
            await conn.execute(sa.text("""
                UPDATE run_attempts
                SET status = 'failed', ended_at = now()
                WHERE id = :aid
            """), {"aid": aid})

            # 4b. Delete resource locks for this attempt
            await conn.execute(sa.text("""
                DELETE FROM resource_locks WHERE owner_attempt_id = :aid
            """), {"aid": aid})

            # 4c. Emit attempt_revoked event
            await conn.execute(sa.text("""
                INSERT INTO agent_events
                    (id, work_item_id, run_attempt_id, actor_user_id, api_key_id, event_type, payload)
                VALUES (:eid, :wid, :aid, :uid, :kid, 'attempt_revoked', CAST(:payload AS JSONB))
            """), {
                "eid": _evt_id(),
                "wid": wid,
                "aid": aid,
                "uid": user_row["id"],
                "kid": key_id,
                "payload": json.dumps({
                    "key_id": key_id,
                    "attempt_id": aid,
                    "revoked_by": caller.id,
                }),
            })

            # 4d. Transition work_item back to paused (§17.4)
            await conn.execute(sa.text("""
                UPDATE work_items
                SET status = 'paused', current_attempt_id = NULL
                WHERE current_attempt_id = :aid
            """), {"aid": aid})

    return JSONResponse(status_code=200, content={
        "ok": True,
        "terminated_attempts": terminated_ids,
    })
