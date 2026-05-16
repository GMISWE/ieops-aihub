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
