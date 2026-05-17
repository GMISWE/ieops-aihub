"""s10 — GC cron tick correctness (gc_tick run directly, not the loop)."""
from __future__ import annotations

import pytest
import sqlalchemy as sa

from app.gc import gc_tick


pytestmark = pytest.mark.asyncio(loop_scope="session")


async def test_gc_marks_expired_attempts(seeded_reference):
    # Force ra_111 lease > 5min in the past
    async with seeded_reference.connect() as conn:
        await conn.execute(sa.text("""
            UPDATE run_attempts SET lease_until = now() - interval '10 minutes'
            WHERE id='ra_111'
        """))
        await conn.commit()
    res = await gc_tick(seeded_reference)
    assert res["attempts_expired"] >= 1
    async with seeded_reference.connect() as conn:
        status = (await conn.execute(sa.text(
            "SELECT status FROM run_attempts WHERE id='ra_111'"
        ))).scalar()
        assert status == "expired"


async def test_gc_deletes_orphan_locks(seeded_reference):
    # Mark ra_111 as wrapped manually (without going through complete) so locks
    # become orphans; GC should clean them.
    async with seeded_reference.connect() as conn:
        await conn.execute(sa.text(
            "UPDATE run_attempts SET status='wrapped', ended_at=now() WHERE id='ra_111'"
        ))
        await conn.commit()
    res = await gc_tick(seeded_reference)
    assert res["orphan_locks_deleted"] >= 1
    async with seeded_reference.connect() as conn:
        cnt = (await conn.execute(sa.text(
            "SELECT COUNT(*) FROM resource_locks WHERE owner_attempt_id='ra_111'"
        ))).scalar()
        assert cnt == 0


async def test_gc_truncates_old_events(seeded_users):
    async with seeded_users.connect() as conn:
        await conn.execute(sa.text("""
            INSERT INTO work_items (id, project, scenario, goal, status, priority,
                                    labels, reporter_user_id, declared_resources,
                                    resources_version, metadata)
            VALUES ('wi_oldev', 'marketplace', 'coding', 'x', 'queued', 'normal',
                    '[]'::jsonb, 'u_zhangsan', '[]'::jsonb, 0, '{}'::jsonb)
        """))
        await conn.execute(sa.text("""
            INSERT INTO agent_events (id, work_item_id, actor_user_id, event_type,
                                       payload, pinned, created_at)
            VALUES ('evt_old', 'wi_oldev', 'u_zhangsan', 'note',
                    '{"large": "payload"}'::jsonb, false,
                    now() - interval '100 days')
        """))
        await conn.commit()
    res = await gc_tick(seeded_users)
    assert res["events_truncated"] >= 1
    async with seeded_users.connect() as conn:
        row = (await conn.execute(sa.text(
            "SELECT payload FROM agent_events WHERE id='evt_old'"
        ))).mappings().first()
        assert row["payload"] == {"_truncated": True}


async def test_gc_deletes_expired_memories(seeded_users):
    async with seeded_users.connect() as conn:
        await conn.execute(sa.text("""
            INSERT INTO memories (id, project, author_user_id, visibility, type,
                                   content, expires_at)
            VALUES ('mem_old', 'marketplace', 'u_zhangsan', 'project', 'note',
                    'x', now() - interval '1 day')
        """))
        await conn.execute(sa.text("""
            INSERT INTO memories (id, project, author_user_id, visibility, type,
                                   content, expires_at)
            VALUES ('mem_fresh', 'marketplace', 'u_zhangsan', 'project', 'note',
                    'y', now() + interval '1 day')
        """))
        await conn.commit()
    res = await gc_tick(seeded_users)
    assert res["memories_expired"] >= 1
    async with seeded_users.connect() as conn:
        ids = {r[0] for r in (await conn.execute(sa.text(
            "SELECT id FROM memories"
        )))}
        assert "mem_old" not in ids
        assert "mem_fresh" in ids


async def test_gc_idempotent(seeded_reference):
    """Run gc_tick twice → second call no-op."""
    res1 = await gc_tick(seeded_reference)
    res2 = await gc_tick(seeded_reference)
    assert res2["orphan_locks_deleted"] == 0


async def test_gc_cascade_expires_work_item_to_blocked(seeded_reference):
    """When a running attempt's lease lapses, GC must cascade:
       attempt → expired, AND
       work_item → blocked + current_attempt_id=NULL,
       and emit attempt_expired + work_item_blocked events.

    Mirrors §17.4 api_key revoke cascade for the §17.5 lease-expiry path.
    Pre-fix this was silently incomplete (work_item stayed status='running'
    pointing at an expired attempt; /pf3-status couldn't surface the gap).
    """
    # Force ra_111 lease > 5 min into the past so sweep 1 picks it up.
    async with seeded_reference.connect() as conn:
        await conn.execute(sa.text(
            "UPDATE run_attempts SET lease_until = now() - interval '10 minutes' "
            "WHERE id = 'ra_111'"
        ))
        await conn.commit()

    res = await gc_tick(seeded_reference)
    assert res["attempts_expired"] >= 1, res
    assert res["work_items_blocked"] >= 1, res

    async with seeded_reference.connect() as conn:
        # work_item now blocked, current_attempt_id NULL.
        row = (await conn.execute(sa.text(
            "SELECT status, current_attempt_id FROM work_items WHERE id = 'wi_a3f'"
        ))).mappings().first()
        assert row["status"] == "blocked", row
        assert row["current_attempt_id"] is None, row

        # Both audit events emitted, referencing the prior attempt.
        evts = list((await conn.execute(sa.text(
            "SELECT event_type, payload FROM agent_events "
            "WHERE work_item_id = 'wi_a3f' AND run_attempt_id = 'ra_111' "
            "AND event_type IN ('attempt_expired','work_item_blocked') "
            "ORDER BY created_at"
        ))).mappings())
        types = {e["event_type"] for e in evts}
        assert {"attempt_expired", "work_item_blocked"}.issubset(types), evts
        # work_item_blocked payload pins the reason + prior context.
        blocked = next(e for e in evts if e["event_type"] == "work_item_blocked")
        assert blocked["payload"]["reason"] == "lease_expired", blocked
        assert blocked["payload"]["prior_attempt_id"] == "ra_111", blocked


async def test_gc_attempt_expired_event_has_rich_payload(seeded_reference):
    """F9/M5: GC must emit attempt_expired with {attempt_id, work_item_id, expired_at}."""
    async with seeded_reference.connect() as conn:
        await conn.execute(sa.text(
            "UPDATE run_attempts SET lease_until = now() - interval '10 minutes' "
            "WHERE id = 'ra_111'"
        ))
        await conn.commit()

    res = await gc_tick(seeded_reference)
    assert res["attempts_expired"] >= 1

    async with seeded_reference.connect() as conn:
        evt = (await conn.execute(sa.text("""
            SELECT payload FROM agent_events
            WHERE run_attempt_id = 'ra_111' AND event_type = 'attempt_expired'
            ORDER BY created_at DESC LIMIT 1
        """))).mappings().first()

    assert evt is not None, "attempt_expired event not found"
    payload = evt["payload"]
    assert payload["attempt_id"] == "ra_111"
    assert payload["work_item_id"] == "wi_a3f"
    assert payload["expired_at"] is not None  # ISO timestamp set


async def test_gc_cascade_skips_non_running_work_items(seeded_reference):
    """If a work_item was paused/blocked/wrapped concurrently by the API while
    its attempt's lease was lapsing, GC must NOT clobber the API-set status.
    Sweep 2's `AND status='running'` filter guards this; this test pins it."""
    async with seeded_reference.connect() as conn:
        # Simulate: someone paused the WI via API at the same time lease expired.
        await conn.execute(sa.text(
            "UPDATE work_items SET status='paused' WHERE id = 'wi_a3f'"
        ))
        await conn.execute(sa.text(
            "UPDATE run_attempts SET lease_until = now() - interval '10 minutes' "
            "WHERE id = 'ra_111'"
        ))
        await conn.commit()

    res = await gc_tick(seeded_reference)
    assert res["attempts_expired"] >= 1
    # Attempt still expired, but WI must remain paused (not flipped to blocked).
    async with seeded_reference.connect() as conn:
        row = (await conn.execute(sa.text(
            "SELECT status FROM work_items WHERE id = 'wi_a3f'"
        ))).mappings().first()
        assert row["status"] == "paused", row
