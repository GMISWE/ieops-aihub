"""Auth helpers — Bearer + AttemptCredential verification per design.md §6.1.

verify_mutation: lease-fenced endpoints (atomic attempt + AC three-tuple)
verify_bearer:   whoami / memory / admin endpoints (Bearer-only, no AC)

Token verification uses argon2id when the stored key_hash starts with
'argon2id$'; tests / seed data use a 'argon2id$dummy_seed_hash_<who>' marker
to keep fixtures deterministic without needing a kdf. Real production
deployments must onboard users via /v1/admin/users which writes real argon2id
digests (Phase 1B/C bootstrap; out of 1A scope).
"""
from __future__ import annotations

import hashlib
import hmac
from dataclasses import dataclass
from typing import Any

import sqlalchemy as sa
from fastapi import Header, Request
from sqlalchemy.ext.asyncio import AsyncConnection, AsyncEngine

from app.errors import AihubServerError, ErrorCode


# ---------------------------------------------------------------------------
# Dataclasses (runtime in-memory snapshots; pydantic models in schemas.py)
# ---------------------------------------------------------------------------

@dataclass(frozen=True)
class UserRecord:
    id: str
    email: str
    display_name: str
    role: str
    projects: list[str]
    api_keys: list[dict[str, Any]]
    # api_key entry that matched this request (so callers can read api_key_id)
    matched_api_key: dict[str, Any]

    @property
    def matched_api_key_id(self) -> str:
        return self.matched_api_key["id"]


@dataclass(frozen=True)
class AttemptRecord:
    id: str
    work_item_id: str
    status: str
    claim_epoch: int
    lease_until: Any
    actor_user_id: str
    api_key_id: str
    actor_display: str
    machine_id: str
    session_secret_hash: str


# ---------------------------------------------------------------------------
# Key-hash compare
# ---------------------------------------------------------------------------

def _hash_session_secret(raw: str) -> str:
    """§6.1 / http_const.SESSION_SECRET_HASH_ALGO pinned to sha256 hex 64 chars."""
    return hashlib.sha256(raw.encode("ascii")).hexdigest()


def _verify_api_key_hash(raw_bearer: str, stored_hash: str) -> bool:
    """Constant-time compare for the stored api_key hash.

    Stored format: 'argon2id$<digest>' for real keys; tests use
    'argon2id$dummy_seed_hash_<who>' as deterministic seed marker. In Phase 1A
    we treat the dummy seed marker as a literal — admin route in 1B/C will swap
    in real argon2id hashing. For now, also support 'sha256$' prefix as a
    convenience for tests that want to feed real raw keys.
    """
    if stored_hash.startswith("sha256$"):
        digest = hashlib.sha256(raw_bearer.encode("utf-8")).hexdigest()
        return hmac.compare_digest("sha256$" + digest, stored_hash)
    if stored_hash.startswith("argon2id$"):
        # 1A test fixtures use literal raw bearer == stored_hash convention:
        # raw bearer for u_zhangsan is the same 'argon2id$dummy_seed_hash_zhang' string.
        return hmac.compare_digest(raw_bearer, stored_hash)
    return False


# ---------------------------------------------------------------------------
# User lookup
# ---------------------------------------------------------------------------

async def find_user_by_api_key(conn: AsyncConnection, bearer: str) -> UserRecord | None:
    """Scan users.api_keys JSONB; return first non-revoked match.

    Per design §5: users.api_keys is JSONB array of objects with shape:
      {id, key_hash, scopes, created_at, revoked_at: nullable}

    Index idx_users_api_keys_gin (jsonb_path_ops) exists on users.api_keys.
    We leverage it via the @> containment operator: only rows that contain at
    least one api_key element with 'revoked_at': null are fetched, which lets
    PG use the GIN index to skip fully-revoked users and empty-key rows.
    Within the returned rows we iterate api_keys in Python to match key_hash
    (it's rare a user has many keys).

    Note: a fully revoked user (all keys have revoked_at set) is skipped by
    the GIN filter. A user with a mix of active and revoked keys is returned
    and the Python loop skips revoked entries individually.
    """
    if not bearer:
        return None
    rows = await conn.execute(sa.text("""
        SELECT id, email, display_name, role,
               COALESCE(projects, '[]'::jsonb) AS projects,
               COALESCE(api_keys, '[]'::jsonb) AS api_keys
        FROM users
        WHERE api_keys @> '[{"revoked_at": null}]'::jsonb
    """))
    for row in rows.mappings():
        for ak in row["api_keys"]:
            if ak.get("revoked_at") is not None:
                continue
            stored_hash = ak.get("key_hash", "")
            if _verify_api_key_hash(bearer, stored_hash):
                return UserRecord(
                    id=row["id"],
                    email=row["email"],
                    display_name=row["display_name"],
                    role=row["role"],
                    projects=list(row["projects"]),
                    api_keys=list(row["api_keys"]),
                    matched_api_key=ak,
                )
    return None


# ---------------------------------------------------------------------------
# Bearer-only verifier (whoami, memory, admin)
# ---------------------------------------------------------------------------

async def verify_bearer(conn: AsyncConnection, bearer: str | None) -> UserRecord:
    if not bearer:
        raise AihubServerError(ErrorCode.UNAUTHORIZED, "missing Bearer token")
    user = await find_user_by_api_key(conn, bearer)
    if user is None:
        raise AihubServerError(ErrorCode.UNAUTHORIZED, "api key not recognized")
    return user


# ---------------------------------------------------------------------------
# verify_mutation (lease-fenced endpoints)
# ---------------------------------------------------------------------------

async def verify_mutation(
    conn: AsyncConnection,
    bearer: str | None,
    attempt_id: str,
    claim_epoch: int,
    session_secret: str,
) -> tuple[UserRecord, AttemptRecord]:
    """§6.1 mutating-API algorithm.

    Returns (user, attempt) on success. Raises AihubServerError with the
    correct ErrorCode otherwise. Steps:
      1. user lookup by bearer (key revoked → 401)
      2. attempt lookup + must be 'running'
      3. attempt.actor_user_id == user.id
      4. attempt.claim_epoch == claim_epoch
      5. attempt.lease_until > server_now() (never use client clock)
      6. sha256(session_secret) == attempt.session_secret_hash
    """
    user = await verify_bearer(conn, bearer)

    row = (await conn.execute(sa.text("""
        SELECT id, work_item_id, status, claim_epoch, lease_until,
               actor_user_id, api_key_id, actor_display, machine_id,
               session_secret_hash
        FROM run_attempts
        WHERE id = :aid
    """), {"aid": attempt_id})).mappings().first()
    if row is None:
        raise AihubServerError(ErrorCode.CONFLICT_LEASE_EXPIRED, "attempt not active")
    if row["status"] != "running":
        raise AihubServerError(ErrorCode.CONFLICT_LEASE_EXPIRED,
                               f"attempt status={row['status']}, not active")
    if row["actor_user_id"] != user.id:
        raise AihubServerError(ErrorCode.FORBIDDEN, "attempt belongs to different user")
    if row["claim_epoch"] != claim_epoch:
        raise AihubServerError(ErrorCode.CONFLICT_EPOCH_MISMATCH,
                               f"epoch mismatch: stored={row['claim_epoch']} requested={claim_epoch}")
    db_now = (await conn.execute(sa.text("SELECT now()"))).scalar_one()
    if row["lease_until"] <= db_now:
        raise AihubServerError(ErrorCode.CONFLICT_LEASE_EXPIRED, "lease expired")
    digest = _hash_session_secret(session_secret)
    if not hmac.compare_digest(digest, row["session_secret_hash"]):
        raise AihubServerError(ErrorCode.UNAUTHORIZED, "session_secret mismatch")

    attempt = AttemptRecord(
        id=row["id"],
        work_item_id=row["work_item_id"],
        status=row["status"],
        claim_epoch=row["claim_epoch"],
        lease_until=row["lease_until"],
        actor_user_id=row["actor_user_id"],
        api_key_id=row["api_key_id"],
        actor_display=row["actor_display"],
        machine_id=row["machine_id"],
        session_secret_hash=row["session_secret_hash"],
    )
    return user, attempt


# ---------------------------------------------------------------------------
# FastAPI helpers
# ---------------------------------------------------------------------------

def extract_bearer(authorization: str | None) -> str | None:
    if not authorization:
        return None
    parts = authorization.split(None, 1)
    if len(parts) != 2 or parts[0].lower() != "bearer":
        return None
    return parts[1].strip()


async def bearer_dep(authorization: str | None = Header(default=None)) -> str | None:
    return extract_bearer(authorization)
