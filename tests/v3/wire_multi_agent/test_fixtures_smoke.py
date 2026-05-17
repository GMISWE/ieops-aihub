"""Smoke test: verify wire_multi_agent fixtures wire up correctly.

This isn't one of the 5 P1.* scenarios — it's a sanity check that the
conftest factory builds independent adapters that all hit the same PG.
If this fails, all P1.* tests will fail too, so it's a fast canary.
"""
from __future__ import annotations

import pytest
import sqlalchemy as sa


pytestmark = pytest.mark.asyncio(loop_scope="session")


async def test_three_adapters_hit_same_pg(team_adapters, seeded_users):
    """All 3 adapters share one ASGI transport => same in-process aihub => same PG.

    Verifies that each bearer authenticates as the expected user by issuing
    a real `create_work_item` and inspecting the resulting DB row's
    `reporter_user_id`. This is a functional check via public APIs — no
    private attribute access — so adapter internals can be refactored
    without breaking this canary.
    """
    adapter_z = team_adapters["zhang"]
    adapter_l = team_adapters["li"]
    adapter_w = team_adapters["wang"]

    # DB-level confirmation that seeded_users seeded the reference team
    async with seeded_users.connect() as conn:
        rows = (await conn.execute(sa.text(
            "SELECT id, display_name FROM users WHERE role = 'writer' ORDER BY id"
        ))).mappings().all()
        assert len(rows) == 3
        assert {r["id"] for r in rows} == {"u_zhangsan", "u_lisi", "u_wangwu"}

    # Functional identity check: each bearer authenticates as the expected user
    # via a real create_work_item; the DB row's reporter_user_id confirms.
    for adapter, expected_user in [
        (adapter_z, "u_zhangsan"),
        (adapter_l, "u_lisi"),
        (adapter_w, "u_wangwu"),
    ]:
        wi = await adapter.create_work_item(
            project="marketplace", goal=f"identity probe: {expected_user}",
            scenario="coding", declared_resources=[],
        )
        async with seeded_users.connect() as conn:
            row = (await conn.execute(sa.text(
                "SELECT reporter_user_id FROM work_items WHERE id = :id"
            ), {"id": wi["work_item_id"]})).mappings().first()
            assert row["reporter_user_id"] == expected_user, (
                f"adapter authenticated as {row['reporter_user_id']}, "
                f"expected {expected_user}"
            )


async def test_concurrent_create_work_item_independent(team_adapters):
    """Three users concurrently create their own (distinct) work_items.

    Not testing a race here — just verifying that 3 in-flight requests via
    the shared ASGI transport don't trample each other's bearer / session.
    Each user creates their own wi and gets their own work_item_id.
    """
    import asyncio

    adapter_z = team_adapters["zhang"]
    adapter_l = team_adapters["li"]
    adapter_w = team_adapters["wang"]

    results = await asyncio.gather(
        adapter_z.create_work_item(
            project="marketplace", goal="zhang's task", scenario="coding",
            declared_resources=[],
        ),
        adapter_l.create_work_item(
            project="marketplace", goal="li's task", scenario="coding",
            declared_resources=[],
        ),
        adapter_w.create_work_item(
            project="marketplace", goal="wang's task", scenario="coding",
            declared_resources=[],
        ),
    )

    # 3 distinct work_item_ids
    ids = [r["work_item_id"] for r in results]
    assert len(ids) == 3
    assert len(set(ids)) == 3, f"expected 3 distinct ids, got {ids}"
    assert all(i.startswith("wi_") for i in ids)
