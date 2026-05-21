"""Resource lock helpers — used by /v1/locks (POST/DELETE).

Acquire: INSERT INTO resource_locks (PK violation → CONFLICT_HARD_BLOCK).
Release: DELETE owned by current attempt only (fencing).
"""
from __future__ import annotations

import sqlalchemy as sa
from sqlalchemy.exc import IntegrityError
from sqlalchemy.ext.asyncio import AsyncConnection

from app.auth import AttemptRecord, UserRecord
from app.errors import AihubServerError, ErrorCode
from app.events import emit_event


async def acquire_lock(
    conn: AsyncConnection,
    *,
    user: UserRecord,
    attempt: AttemptRecord,
    resource_type: str,
    resource_key: str,
) -> None:
    # Pre-check: PG aborts the whole transaction on a constraint violation,
    # so we look up an existing lock first and short-circuit. The row in
    # resource_locks has PRIMARY KEY (resource_type, resource_key) so the
    # SELECT is a single row hit.
    existing = (await conn.execute(sa.text("""
        SELECT ra.id AS owner_attempt, ra.actor_user_id, ra.actor_display
        FROM resource_locks rl
        JOIN run_attempts ra ON ra.id = rl.owner_attempt_id
        WHERE rl.resource_type = :rt AND rl.resource_key = :rk
    """), {"rt": resource_type, "rk": resource_key})).mappings().first()
    if existing is not None:
        raise AihubServerError(
            ErrorCode.CONFLICT_HARD_BLOCK,
            f"lock {resource_type}:{resource_key} already held",
            details={"conflicts_with": dict(existing)},
        )
    try:
        await conn.execute(sa.text("""
            INSERT INTO resource_locks (resource_type, resource_key,
                                         owner_attempt_id, claim_epoch)
            VALUES (:rt, :rk, :owner, :epoch)
        """), {"rt": resource_type, "rk": resource_key,
               "owner": attempt.id, "epoch": attempt.claim_epoch})
    except IntegrityError:
        # Race: another claim grabbed the lock between SELECT and INSERT
        raise AihubServerError(
            ErrorCode.CONFLICT_HARD_BLOCK,
            f"lock {resource_type}:{resource_key} acquired by concurrent racer",
        )
    await emit_event(
        conn, work_item_id=attempt.work_item_id,
        event_type="lock_acquired",
        payload={"resource_type": resource_type, "resource_key": resource_key,
                 "claim_epoch": attempt.claim_epoch},
        actor_user_id=user.id, api_key_id=user.matched_api_key_id,
        run_attempt_id=attempt.id,
    )


async def release_lock(
    conn: AsyncConnection,
    *,
    user: UserRecord,
    attempt: AttemptRecord,
    resource_type: str,
    resource_key: str,
) -> None:
    # Single CTE: atomically attempt the DELETE (owner-fenced) AND snapshot the
    # current owner in one round-trip. This eliminates the TOCTOU window that
    # existed between a separate DELETE and a follow-up SELECT for disambiguation
    # — between those two statements another claim could re-acquire the lock and
    # cause us to return FORBIDDEN when the correct code is NOT_FOUND.
    result = (await conn.execute(sa.text("""
        WITH deleted AS (
            DELETE FROM resource_locks
            WHERE resource_type = :rt AND resource_key = :rk
              AND owner_attempt_id = :owner
            RETURNING owner_attempt_id
        ),
        current AS (
            SELECT owner_attempt_id
            FROM resource_locks
            WHERE resource_type = :rt AND resource_key = :rk
        )
        SELECT
            (SELECT owner_attempt_id FROM deleted)   AS deleted_owner,
            (SELECT owner_attempt_id FROM current)   AS current_owner
    """), {"rt": resource_type, "rk": resource_key, "owner": attempt.id})).mappings().first()

    if result["deleted_owner"] is not None:
        # DELETE succeeded — our attempt owned it; fall through to emit event.
        pass
    elif result["current_owner"] is None:
        # No row exists (and our DELETE found nothing) → lock was not held.
        raise AihubServerError(ErrorCode.NOT_FOUND,
                               f"lock {resource_type}:{resource_key} not held")
    else:
        # Lock exists but is owned by a different attempt.
        raise AihubServerError(ErrorCode.FORBIDDEN,
                               "lock owned by a different attempt")
    await emit_event(
        conn, work_item_id=attempt.work_item_id,
        event_type="lock_released",
        payload={"resource_type": resource_type, "resource_key": resource_key},
        actor_user_id=user.id, api_key_id=user.matched_api_key_id,
        run_attempt_id=attempt.id,
    )
