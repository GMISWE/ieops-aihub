"""Server-side GC scheduled job per design §17.5.

Runs every 60s in a background asyncio task (started from app lifespan).
4 sweeps:
  1. Mark lease-expired attempts (5min grace) → status='expired'
  2. Delete orphan locks (owner in terminal status)
  3. Truncate 90d+ non-pinned event payload to {"_truncated": true}
  4. Delete expired memories
"""
from __future__ import annotations

import asyncio
import logging

import sqlalchemy as sa
from sqlalchemy.ext.asyncio import AsyncEngine

log = logging.getLogger("aihub.gc")


GC_INTERVAL_SECONDS = 60


async def gc_tick(engine: AsyncEngine) -> dict:
    """Run one GC pass. Returns counts for observability."""
    results = {}
    async with engine.begin() as conn:
        r1 = await conn.execute(sa.text("""
            UPDATE run_attempts SET status='expired', ended_at=now()
            WHERE status='running'
              AND lease_until < now() - interval '5 minutes'
        """))
        results["attempts_expired"] = r1.rowcount or 0

        r2 = await conn.execute(sa.text("""
            DELETE FROM resource_locks rl USING run_attempts ra
            WHERE rl.owner_attempt_id = ra.id
              AND ra.status IN ('expired','superseded','wrapped','failed')
        """))
        results["orphan_locks_deleted"] = r2.rowcount or 0

        r3 = await conn.execute(sa.text("""
            UPDATE agent_events SET payload = '{"_truncated": true}'::jsonb
            WHERE pinned = false
              AND created_at < now() - interval '90 days'
              AND payload <> '{"_truncated": true}'::jsonb
        """))
        results["events_truncated"] = r3.rowcount or 0

        r4 = await conn.execute(sa.text("""
            DELETE FROM memories
            WHERE expires_at IS NOT NULL AND expires_at < now()
        """))
        results["memories_expired"] = r4.rowcount or 0
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
