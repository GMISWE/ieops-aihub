"""Run attempt service — atomic claim, lease renew, complete/pause.

Atomic claim transaction implementation per design.md §7.2.1, exactly 9 steps:
  1. SELECT FOR UPDATE work_item
  2. SELECT FOR UPDATE old current_attempt (if any)
  3. Eligibility — running + lease alive => CONFLICT_BUSY_NOT_TAKEOVER_ELIGIBLE
  4. Lock pre-check re-validate (exclude own attempt) => CONFLICT_HARD_BLOCK
  5. Idempotency — same idempotency_key + running => return existing
  6. Supersede old (takeover) — UPDATE status='superseded' + DELETE locks
  7. INSERT new attempt (claim_epoch = old.epoch + 1, parent_attempt_id = old)
  8. INSERT requested_locks
  9. UPDATE work_item.current_attempt_id + status + emit attempt_started

Isolation: asyncpg's default is READ COMMITTED — sufficient because we lock
the (work_item, current_attempt) rows with FOR UPDATE. UNIQUE constraints
on (work_item_id, idempotency_key) + (work_item_id, claim_epoch) provide
the final ABA-safety net (23505 violations are caught and mapped).
"""
from __future__ import annotations

import hashlib
import json
from typing import Any

import sqlalchemy as sa
from sqlalchemy.exc import IntegrityError
from sqlalchemy.ext.asyncio import AsyncConnection
from ulid import ULID

from app.auth import UserRecord
from app.errors import AihubServerError, ErrorCode


def _ra_id() -> str:
    return "ra_" + str(ULID()).lower()


def _evt_id() -> str:
    return "evt_" + str(ULID()).lower()


def _hash_secret(raw: str) -> str:
    return hashlib.sha256(raw.encode("ascii")).hexdigest()


# ---------------------------------------------------------------------------
# Claim transaction (§7.2.1)
# ---------------------------------------------------------------------------

async def claim_work_item(
    conn: AsyncConnection,
    *,
    wi_id: str,
    user: UserRecord,
    idempotency_key: str,
    machine_id: str,
    session_secret_raw: str,
    requested_locks: list[dict],
) -> dict:
    """Run the atomic claim transaction; return ClaimResponse-shaped dict.

    The conn is assumed to be inside a transaction (caller wraps in
    `async with engine.begin() as conn`). Postgres default READ COMMITTED
    plus FOR UPDATE row locks per §7.2.1 pin.
    """
    # ---- Step 1: SELECT FOR UPDATE work_item ----
    wi_row = (await conn.execute(sa.text("""
        SELECT id, project, status, current_attempt_id, declared_resources
        FROM work_items WHERE id = :id FOR UPDATE
    """), {"id": wi_id})).mappings().first()
    if wi_row is None:
        raise AihubServerError(ErrorCode.NOT_FOUND, f"work_item {wi_id}")

    if wi_row["project"] not in user.projects:
        raise AihubServerError(ErrorCode.FORBIDDEN,
                               f"user not in project {wi_row['project']}")
    if user.role not in ("writer", "admin"):
        raise AihubServerError(ErrorCode.FORBIDDEN,
                               "writer or admin required to claim")

    # ---- Step 2: SELECT FOR UPDATE old current_attempt ----
    old_a = None
    if wi_row["current_attempt_id"] is not None:
        old_a = (await conn.execute(sa.text("""
            SELECT id, status, claim_epoch, lease_until, actor_user_id, actor_display
            FROM run_attempts WHERE id = :aid FOR UPDATE
        """), {"aid": wi_row["current_attempt_id"]})).mappings().first()

    # ---- Step 3a: Idempotency replay check (BEFORE eligibility, so a client
    # retrying a successful claim from the SAME attempt key gets its same
    # attempt back without hitting busy-not-eligible) ----
    existing_replay = (await conn.execute(sa.text("""
        SELECT id, claim_epoch, lease_until FROM run_attempts
        WHERE work_item_id = :wid AND idempotency_key = :key
    """), {"wid": wi_id, "key": idempotency_key})).mappings().first()
    if existing_replay is not None:
        return {
            "attempt_id": existing_replay["id"],
            "claim_epoch": existing_replay["claim_epoch"],
            "lease_until": existing_replay["lease_until"],
        }

    # ---- Step 3: Eligibility (server now()) ----
    db_now = (await conn.execute(sa.text("SELECT now()"))).scalar_one()
    if (wi_row["status"] == "running" and old_a is not None
            and old_a["status"] == "running" and old_a["lease_until"] > db_now):
        raise AihubServerError(
            ErrorCode.CONFLICT_BUSY_NOT_TAKEOVER_ELIGIBLE,
            f"work_item {wi_id} owned by {old_a['actor_display']} "
            f"until {old_a['lease_until'].isoformat()}",
            details={"owner_attempt_id": old_a["id"],
                     "owner_actor_user_id": old_a["actor_user_id"],
                     "lease_until": old_a["lease_until"].isoformat()},
        )

    # ---- Step 4: Lock conflict re-validation (exclude own old attempt) ----
    if requested_locks:
        exclude_id = old_a["id"] if old_a is not None else ""
        # Use a tuple-array join (resource_type, resource_key)
        types = [l["resource_type"] for l in requested_locks]
        keys = [l["resource_key"] for l in requested_locks]
        conflict_row = (await conn.execute(sa.text("""
            SELECT rl.resource_type, rl.resource_key,
                   ra.id AS owner_attempt, ra.actor_user_id, ra.actor_display
            FROM resource_locks rl
            JOIN run_attempts ra ON ra.id = rl.owner_attempt_id
            JOIN unnest(CAST(:types AS text[]), CAST(:keys AS text[]))
                AS req(rt, rk) ON rl.resource_type = req.rt AND rl.resource_key = req.rk
            WHERE ra.status = 'running'
              AND ra.lease_until > now()
              AND ra.id <> :exclude
            LIMIT 1
        """), {"types": types, "keys": keys, "exclude": exclude_id})).mappings().first()
        if conflict_row is not None:
            raise AihubServerError(
                ErrorCode.CONFLICT_HARD_BLOCK,
                f"lock {conflict_row['resource_type']}:{conflict_row['resource_key']} "
                f"held by {conflict_row['actor_display']}",
                details={
                    "rule_id": "lock_conflict",
                    "resource_type": conflict_row["resource_type"],
                    "resource_key": conflict_row["resource_key"],
                    "conflicts_with": {
                        "attempt_id": conflict_row["owner_attempt"],
                        "actor_user_id": conflict_row["actor_user_id"],
                        "actor_display": conflict_row["actor_display"],
                    },
                },
            )

    # ---- Step 5: (idempotency was Step 3a) ----

    # ---- Step 6: Supersede old (takeover branch) ----
    new_epoch = (old_a["claim_epoch"] + 1) if old_a is not None else 1
    parent_attempt_id = None
    if old_a is not None and old_a["status"] == "running" and old_a["lease_until"] <= db_now:
        await conn.execute(sa.text("""
            UPDATE run_attempts
            SET status = 'superseded', ended_at = now()
            WHERE id = :aid AND status = 'running'
        """), {"aid": old_a["id"]})
        await conn.execute(sa.text("""
            DELETE FROM resource_locks WHERE owner_attempt_id = :aid
        """), {"aid": old_a["id"]})
        parent_attempt_id = old_a["id"]
    elif old_a is not None and old_a["status"] != "running":
        # Old attempt already terminal (wrapped/failed/superseded/expired);
        # parent chain still records causality.
        parent_attempt_id = old_a["id"]

    # ---- Step 7: INSERT new attempt ----
    new_aid = _ra_id()
    secret_hash = _hash_secret(session_secret_raw)
    try:
        ins_row = (await conn.execute(sa.text("""
            INSERT INTO run_attempts (
                id, work_item_id, status, claim_epoch, idempotency_key,
                lease_until, actor_user_id, api_key_id, actor_display,
                machine_id, session_secret_hash, parent_attempt_id
            ) VALUES (
                :id, :wid, 'running', :epoch, :key,
                now() + interval '60 seconds',
                :uid, :kid, :display, :mid, :hash, :parent
            )
            RETURNING id, claim_epoch, lease_until
        """), {
            "id": new_aid, "wid": wi_id, "epoch": new_epoch,
            "key": idempotency_key, "uid": user.id,
            "kid": user.matched_api_key_id,
            "display": user.display_name, "mid": machine_id,
            "hash": secret_hash, "parent": parent_attempt_id,
        })).mappings().first()
    except IntegrityError as ie:
        # 23505 UNIQUE violation — could be (work_item_id, idempotency_key)
        # or (work_item_id, claim_epoch). Both mean a concurrent claim raced
        # ahead; idempotency lookup above should have caught key-dup, but the
        # race window between SELECT and INSERT can still hit here.
        msg = str(ie.orig) if ie.orig else str(ie)
        if "idempotency_key" in msg:
            raise AihubServerError(ErrorCode.CONFLICT_DUPLICATE_REQUEST,
                                   "duplicate idempotency_key (concurrent claim)")
        # claim_epoch unique violation = concurrent claim won; treat as duplicate
        raise AihubServerError(ErrorCode.CONFLICT_DUPLICATE_REQUEST,
                               "concurrent claim won the race")

    # ---- Step 8: INSERT requested_locks ----
    for l in requested_locks:
        try:
            await conn.execute(sa.text("""
                INSERT INTO resource_locks (resource_type, resource_key,
                                            owner_attempt_id, claim_epoch)
                VALUES (:rt, :rk, :owner, :epoch)
            """), {
                "rt": l["resource_type"], "rk": l["resource_key"],
                "owner": new_aid, "epoch": new_epoch,
            })
        except IntegrityError:
            # Should have been caught in step 4; if we still hit it, treat as
            # hard_block (concurrent racer slipped in between).
            raise AihubServerError(
                ErrorCode.CONFLICT_HARD_BLOCK,
                f"lock {l['resource_type']}:{l['resource_key']} "
                "acquired by concurrent racer",
            )
        # emit lock_acquired event
        await conn.execute(sa.text("""
            INSERT INTO agent_events (id, work_item_id, run_attempt_id,
                                       actor_user_id, api_key_id, event_type, payload)
            VALUES (:eid, :wid, :aid, :uid, :kid, 'lock_acquired',
                    CAST(:payload AS JSONB))
        """), {
            "eid": _evt_id(), "wid": wi_id, "aid": new_aid,
            "uid": user.id, "kid": user.matched_api_key_id,
            "payload": json.dumps({
                "resource_type": l["resource_type"],
                "resource_key": l["resource_key"],
                "claim_epoch": new_epoch,
            }),
        })

    # ---- Step 9: UPDATE work_item + emit attempt_started ----
    await conn.execute(sa.text("""
        UPDATE work_items
        SET status = 'running', current_attempt_id = :aid, updated_at = now()
        WHERE id = :wid
    """), {"aid": new_aid, "wid": wi_id})
    await conn.execute(sa.text("""
        INSERT INTO agent_events (id, work_item_id, run_attempt_id,
                                   actor_user_id, api_key_id, event_type, payload)
        VALUES (:eid, :wid, :aid, :uid, :kid, 'attempt_started',
                CAST(:payload AS JSONB))
    """), {
        "eid": _evt_id(), "wid": wi_id, "aid": new_aid,
        "uid": user.id, "kid": user.matched_api_key_id,
        "payload": json.dumps({
            "claim_epoch": new_epoch,
            "is_takeover": parent_attempt_id is not None and old_a is not None
                           and old_a["status"] == "running",
        }),
    })
    if parent_attempt_id is not None and old_a is not None and old_a["status"] == "running":
        # Takeover event
        await conn.execute(sa.text("""
            INSERT INTO agent_events (id, work_item_id, run_attempt_id,
                                       actor_user_id, api_key_id, event_type, payload)
            VALUES (:eid, :wid, :aid, :uid, :kid, 'attempt_taken_over',
                    CAST(:payload AS JSONB))
        """), {
            "eid": _evt_id(), "wid": wi_id, "aid": new_aid,
            "uid": user.id, "kid": user.matched_api_key_id,
            "payload": json.dumps({
                "from_attempt_id": old_a["id"],
                "to_attempt_id": new_aid,
                "new_claim_epoch": new_epoch,
            }),
        })

    return {
        "attempt_id": new_aid,
        "claim_epoch": new_epoch,
        "lease_until": ins_row["lease_until"],
    }
