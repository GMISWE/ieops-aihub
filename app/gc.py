"""Server-side GC scheduled job per design §17.5.

Runs every 60s in a background asyncio task (started from app lifespan).
5 sweeps (sweep 2 cascades from sweep 1 — execution order matters):
  1. Mark lease-expired attempts (5min grace) → status='expired'
  2. Cascade newly-expired attempts → work_items.status='blocked',
     current_attempt_id=NULL + emit work_item_blocked / attempt_expired events
     (mirrors §17.4 api_key revoke cascade so /pf3-status correctly surfaces
     "needs takeover").
  3. Delete orphan locks (owner in terminal status)
  4. Truncate 90d+ non-pinned event payload to {"_truncated": true}
  5. Delete expired memories
"""
from __future__ import annotations

import asyncio
import logging

import sqlalchemy as sa
from sqlalchemy.ext.asyncio import AsyncEngine

from app.events import emit_event

log = logging.getLogger("aihub.gc")


GC_INTERVAL_SECONDS = 60


async def gc_tick(engine: AsyncEngine) -> dict:
    """Run one GC pass. Returns counts for observability."""
    results = {}
    async with engine.begin() as conn:
        # ---- 1. Mark lease-expired attempts ----
        # RETURNING captures the just-transitioned rows so sweep 2 scopes
        # precisely to them (no cross-tick race window).
        r1 = await conn.execute(sa.text("""
            UPDATE run_attempts SET status='expired', ended_at=now()
            WHERE status='running'
              AND lease_until < now() - interval '5 minutes'
            RETURNING id, work_item_id, actor_user_id, api_key_id, claim_epoch,
                      ended_at
        """))
        expired_rows = list(r1.mappings())
        results["attempts_expired"] = len(expired_rows)

        # ---- 2. Cascade work_items pointing at just-expired attempts ----
        # Only transition work_items whose current attempt just expired AND
        # whose status is 'running' (don't trample paused/blocked/wrapped/failed
        # that someone may have set via API concurrently).
        cascaded = 0
        if expired_rows:
            aids = [r["id"] for r in expired_rows]
            cas = await conn.execute(sa.text("""
                UPDATE work_items
                SET status = 'blocked',
                    current_attempt_id = NULL,
                    updated_at = now()
                WHERE current_attempt_id = ANY(CAST(:aids AS text[]))
                  AND status = 'running'
                RETURNING id
            """), {"aids": aids})
            cascaded_wi_ids = {r["id"] for r in cas.mappings()}
            cascaded = len(cascaded_wi_ids)

            # Emit attempt_expired for every expired attempt (M5/F9); emit
            # work_item_blocked only when we actually transitioned the WI.
            # Payload per F9: {attempt_id, work_item_id, expired_at (ISO)}.
            for r in expired_rows:
                expired_at_iso = (
                    r["ended_at"].isoformat() if r["ended_at"] is not None
                    else None
                )
                await emit_event(
                    conn,
                    work_item_id=r["work_item_id"],
                    run_attempt_id=r["id"],
                    actor_user_id=r["actor_user_id"],
                    api_key_id=r["api_key_id"],
                    event_type="attempt_expired",
                    payload={
                        "attempt_id": r["id"],
                        "work_item_id": r["work_item_id"],
                        "expired_at": expired_at_iso,
                        "claim_epoch": r["claim_epoch"],
                    },
                )
                if r["work_item_id"] in cascaded_wi_ids:
                    await emit_event(
                        conn,
                        work_item_id=r["work_item_id"],
                        run_attempt_id=r["id"],
                        actor_user_id=r["actor_user_id"],
                        api_key_id=r["api_key_id"],
                        event_type="work_item_blocked",
                        payload={"reason": "lease_expired",
                                 "prior_attempt_id": r["id"],
                                 "prior_claim_epoch": r["claim_epoch"]},
                    )
        results["work_items_blocked"] = cascaded

        # ---- 3. Delete orphan locks (owners in terminal status) ----
        r3 = await conn.execute(sa.text("""
            DELETE FROM resource_locks rl USING run_attempts ra
            WHERE rl.owner_attempt_id = ra.id
              AND ra.status IN ('expired','superseded','wrapped','failed')
        """))
        results["orphan_locks_deleted"] = r3.rowcount or 0

        # ---- 4. Truncate aging event payloads ----
        r4 = await conn.execute(sa.text("""
            UPDATE agent_events SET payload = '{"_truncated": true}'::jsonb
            WHERE pinned = false
              AND created_at < now() - interval '90 days'
              AND payload <> '{"_truncated": true}'::jsonb
        """))
        results["events_truncated"] = r4.rowcount or 0

        # ---- 5. Delete expired memories ----
        r5 = await conn.execute(sa.text("""
            DELETE FROM memories
            WHERE expires_at IS NOT NULL AND expires_at < now()
        """))
        results["memories_expired"] = r5.rowcount or 0
    return results


async def gc_loop(engine: AsyncEngine, *, interval: float = GC_INTERVAL_SECONDS) -> None:
    """Run gc_tick forever, with interval sleeps. Cancel via Task.cancel()."""
    while True:
        try:
            res = await gc_tick(engine)
            if any(res.values()):
                log.info("gc tick: %s", res)
        except asyncio.CancelledError:
            raise
        except Exception:
            log.exception("gc tick failed")
        try:
            await asyncio.sleep(interval)
        except asyncio.CancelledError:
            raise
