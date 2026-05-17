"""POST /v1/attempts/{id}/lease|complete|pause per design §7.3 / §7.7."""
from __future__ import annotations

import hashlib
import hmac

import sqlalchemy as sa
from fastapi import APIRouter, Depends, Path
from fastapi.responses import JSONResponse
from sqlalchemy.ext.asyncio import AsyncEngine

from app.auth import bearer_dep, verify_bearer, verify_mutation, _hash_session_secret
from app.errors import AihubServerError, ErrorCode
from app.events import emit_event
from app.schemas import (
    CompleteAttemptRequest, LeaseRenewResponse, OkResponse,
    PauseAttemptRequest, RenewLeaseRequest,
)
from app.v3_app import get_engine


router = APIRouter(tags=["attempts"])


# ---------------------------------------------------------------------------
# POST /v1/attempts/{id}/lease — renew (no AttemptCredential; only epoch+secret)
# ---------------------------------------------------------------------------

@router.post("/attempts/{attempt_id}/lease",
             response_model=LeaseRenewResponse, operation_id="renew_lease")
async def renew_lease_endpoint(
    body: RenewLeaseRequest,
    attempt_id: str = Path(...),
    bearer: str | None = Depends(bearer_dep),
    engine: AsyncEngine = Depends(get_engine),
):
    """Server SQL per §7.3. 0-rows return → disambiguate per design:
       - current.claim_epoch > requested  → CONFLICT_LEASE_TAKEN_OVER
       - hash mismatch                    → UNAUTHORIZED
       - else (lease expired / status≠running) → CONFLICT_LEASE_EXPIRED
    """
    # Bearer is required for audit, even though renew uses the secret as the
    # primary fence (per §7.3 description). Verifying bearer also catches
    # revoked-key cases up-front.
    secret_hash = _hash_session_secret(body.session_secret)
    async with engine.begin() as conn:
        user = await verify_bearer(conn, bearer)
        row = (await conn.execute(sa.text("""
            UPDATE run_attempts ra
            SET lease_until = now() + interval '60 seconds'
            WHERE ra.id = :aid
              AND ra.claim_epoch = :epoch
              AND ra.session_secret_hash = :hash
              AND ra.lease_until > now()
              AND ra.status = 'running'
              AND ra.actor_user_id = :uid
              -- Defense-in-depth ABA check (§7.5 third leg): verify the work_item
              -- still points at THIS attempt, closing the window between status='running'
              -- and a concurrent takeover that may have reassigned current_attempt_id.
              AND ra.work_item_id = (SELECT id FROM work_items WHERE current_attempt_id = ra.id)
            RETURNING ra.lease_until
        """), {"aid": attempt_id, "epoch": body.claim_epoch,
               "hash": secret_hash, "uid": user.id})).mappings().first()
        if row is not None:
            return JSONResponse(status_code=200, content={
                "lease_until": row["lease_until"].isoformat(),
            })

        # 0-rows: disambiguate
        cur = (await conn.execute(sa.text("""
            SELECT claim_epoch, session_secret_hash, status, actor_user_id, lease_until
            FROM run_attempts WHERE id = :aid
        """), {"aid": attempt_id})).mappings().first()
        if cur is None:
            raise AihubServerError(ErrorCode.NOT_FOUND, f"attempt {attempt_id}")
        if cur["actor_user_id"] != user.id:
            raise AihubServerError(ErrorCode.FORBIDDEN, "attempt belongs to different user")
        if cur["claim_epoch"] > body.claim_epoch:
            raise AihubServerError(
                ErrorCode.CONFLICT_LEASE_TAKEN_OVER,
                f"attempt taken over; current claim_epoch={cur['claim_epoch']}",
                details={"current_claim_epoch": cur["claim_epoch"]},
            )
        if not hmac.compare_digest(cur["session_secret_hash"], secret_hash):
            raise AihubServerError(ErrorCode.UNAUTHORIZED, "session_secret mismatch")
        # else: lease expired or status≠running
        raise AihubServerError(
            ErrorCode.CONFLICT_LEASE_EXPIRED,
            f"lease not renewable; status={cur['status']}",
        )


# ---------------------------------------------------------------------------
# POST /v1/attempts/{id}/complete — wrap or fail the attempt
# ---------------------------------------------------------------------------

@router.post("/attempts/{attempt_id}/complete",
             response_model=OkResponse, operation_id="complete_attempt")
async def complete_attempt_endpoint(
    body: CompleteAttemptRequest,
    attempt_id: str = Path(...),
    bearer: str | None = Depends(bearer_dep),
    engine: AsyncEngine = Depends(get_engine),
):
    """Path-id and body.attempt_id must agree; terminate attempt cleanly:
    UPDATE status, DELETE locks, emit attempt_completed."""
    if body.attempt_id != attempt_id:
        raise AihubServerError(ErrorCode.BAD_REQUEST,
                               "body.attempt_id does not match path attempt_id")
    async with engine.begin() as conn:
        user, attempt = await verify_mutation(
            conn, bearer,
            attempt_id=body.attempt_id,
            claim_epoch=body.claim_epoch,
            session_secret=body.session_secret,
        )
        import logging as _logging
        _log = _logging.getLogger("aihub.attempts")

        new_status = body.status  # 'wrapped' | 'failed'
        await conn.execute(sa.text("""
            UPDATE run_attempts SET status = :s, ended_at = now()
            WHERE id = :aid AND status = 'running'
        """), {"s": new_status, "aid": attempt_id})
        await conn.execute(sa.text("""
            DELETE FROM resource_locks WHERE owner_attempt_id = :aid
        """), {"aid": attempt_id})
        await emit_event(
            conn, work_item_id=attempt.work_item_id, event_type="attempt_completed",
            payload={"status": new_status}, actor_user_id=user.id,
            api_key_id=user.matched_api_key_id, run_attempt_id=attempt_id,
        )
        # Per §7.7 state machine: running → wrap/fail transitions BOTH attempt
        # AND work_item together in the same transaction. Without this, the
        # work_item stays status='running' pointing to a terminal attempt.
        #
        # M4 (F8): guard against 0-rowcount race — if a concurrent takeover
        # reassigned current_attempt_id between verify_mutation and this UPDATE,
        # WHERE current_attempt_id = :aid returns 0 rows.  Skip work_item_completed
        # in that case; log a warning (the takeover path owns the WI now).
        wi_result = await conn.execute(sa.text("""
            UPDATE work_items
            SET status = :s, closed_at = now(), updated_at = now()
            WHERE id = :wid AND current_attempt_id = :aid
        """), {"s": new_status, "wid": attempt.work_item_id, "aid": attempt_id})
        if wi_result.rowcount == 0:
            _log.warning(
                "complete_attempt: work_item %s no longer points at attempt %s "
                "(concurrent takeover race); skipping work_item_completed event",
                attempt.work_item_id, attempt_id,
            )
        else:
            await emit_event(
                conn, work_item_id=attempt.work_item_id,
                event_type="work_item_completed",
                payload={"work_item_id": attempt.work_item_id, "final_status": new_status},
                actor_user_id=user.id, api_key_id=user.matched_api_key_id,
                run_attempt_id=attempt_id,
            )
    return JSONResponse(status_code=200, content={"ok": True})


# ---------------------------------------------------------------------------
# POST /v1/attempts/{id}/pause — user-initiated pause; attempt → wrapped (§7.7)
# ---------------------------------------------------------------------------

@router.post("/attempts/{attempt_id}/pause",
             response_model=OkResponse, operation_id="pause_attempt")
async def pause_attempt_endpoint(
    body: PauseAttemptRequest,
    attempt_id: str = Path(...),
    bearer: str | None = Depends(bearer_dep),
    engine: AsyncEngine = Depends(get_engine),
):
    """Per §7.7: pause => attempt terminal (wrapped), work_item.status=paused,
    work_item.current_attempt_id=NULL. Locks released. Emit attempt_paused."""
    if body.attempt_id != attempt_id:
        raise AihubServerError(ErrorCode.BAD_REQUEST,
                               "body.attempt_id does not match path attempt_id")
    async with engine.begin() as conn:
        user, attempt = await verify_mutation(
            conn, bearer,
            attempt_id=body.attempt_id,
            claim_epoch=body.claim_epoch,
            session_secret=body.session_secret,
        )
        await conn.execute(sa.text("""
            UPDATE run_attempts SET status = 'wrapped', ended_at = now()
            WHERE id = :aid AND status = 'running'
        """), {"aid": attempt_id})
        await conn.execute(sa.text("""
            DELETE FROM resource_locks WHERE owner_attempt_id = :aid
        """), {"aid": attempt_id})
        await conn.execute(sa.text("""
            UPDATE work_items
            SET status = 'paused', current_attempt_id = NULL, updated_at = now()
            WHERE id = :wid
        """), {"wid": attempt.work_item_id})
        await emit_event(
            conn, work_item_id=attempt.work_item_id, event_type="attempt_paused",
            payload={"reason": body.reason}, actor_user_id=user.id,
            api_key_id=user.matched_api_key_id, run_attempt_id=attempt_id,
        )
    return JSONResponse(status_code=200, content={"ok": True})
