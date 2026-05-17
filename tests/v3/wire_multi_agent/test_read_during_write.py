"""F — read during write does not block.

Scenario: zhang is doing a heavy write workload (rapid emit_event calls
under one attempt). Qian (PM, mapped to li here) polls `pf3_status` /
`pf3_read_events` / direct read endpoints throughout. Reads must NOT be
blocked by writes — qian's dashboard stays responsive at <200ms per poll.

Tested invariants:
1. Reader concurrent with writer never sees a blocked / >200ms request
2. Reader sees a monotonically-growing event count (no regressions)
3. Reader's final view matches the writer's final state (no lost rows)

Per opencode review F was descoped from the original "8s pf3_push during
status poll" because pf3_push is a coding-plugin git operation (not
aihub). Recast to "rapid emit_event burst during pf3_read_events poll"
which exercises the same isolation invariant at the aihub layer cleanly.
"""
from __future__ import annotations

import asyncio
import time

import pytest
import sqlalchemy as sa

from polyforge_v3.auth import AttemptCredential


from .conftest import WRITER_BURST_COUNT, READ_INTERVAL_S, READ_BUDGET_S


pytestmark = pytest.mark.asyncio(loop_scope="session")


async def test_status_read_not_blocked_by_emit_burst(
    wire_app, team_adapters, seeded_users, make_adapter,
):
    """Writer fires 50 emits; reader polls every 50ms; no read > 200ms.

    Caveat documented in module docstring: reader and writer go through the
    same in-process ASGI app, so this exercises tx isolation, not network
    contention. To partially mitigate, the reader uses its OWN ASGITransport
    instance — so the two clients have independent httpx connection pools
    (different cookie jars, different async semaphores). Still ASGI-bound
    though; for true network-level concurrency, run aihub via uvicorn in a
    follow-up driver.
    """
    from .conftest import BEARER_LI
    writer_adapter = team_adapters["zhang"]

    # Reader uses a DIFFERENT transport pointing at the same ASGI app, so
    # the writer's httpx queue doesn't head-of-line-block the reader.
    import httpx as _httpx
    app, _ = wire_app
    reader_transport = _httpx.ASGITransport(app=app)
    from polyforge_v3.aihub.client import AihubClient
    from polyforge_v3.aihub.adapter import AihubClientProtocolAdapter
    from polyforge_v3.config import AihubConfig, SessionInfo
    reader_cfg = AihubConfig(url="http://test", api_key_env="_UNUSED_", api_key=BEARER_LI)
    reader_session = SessionInfo(
        machine_id="li-reader", session_id="sess-li-reader",
        session_secret="9" * 64,
    )
    reader_client = AihubClient(reader_cfg, reader_session,
                                transport=reader_transport, timeout=30.0)
    reader_adapter = AihubClientProtocolAdapter(reader_client)

    try:
        # Setup: writer claims wi, sets cred
        wi_id = (await writer_adapter.create_work_item(
            project="marketplace", goal="F: read-during-write target",
            scenario="coding", declared_resources=[],
        ))["work_item_id"]
        claim = await writer_adapter.claim_work_item(
            work_item_id=wi_id, idempotency_key=f"f-{wi_id}",
            session_info={"machine_id": "zhang-mbp"},
            requested_locks=[],
        )
        attempt_id = claim["attempt_id"]
        claim_epoch = claim["claim_epoch"]
        session_secret = claim["session_secret"]
        writer_adapter.set_cred(AttemptCredential(
            attempt_id=attempt_id, claim_epoch=claim_epoch,
            session_secret=session_secret,
        ))

        # Background writer task: 50 sequential emits
        async def writer():
            for i in range(WRITER_BURST_COUNT):
                await writer_adapter.emit_event(
                    work_item_id=wi_id, event_type="note",
                    payload={"seq": i, "kind": "write_load"},
                    attempt_id=attempt_id, claim_epoch=claim_epoch,
                    session_secret=session_secret,
                )

        # Background reader: asyncio.Event for stop, not a closure flag.
        # The Event handshake is task-safe even if reader were moved to a
        # different loop in the future.
        stop_event = asyncio.Event()
        read_durations: list[float] = []
        seen_counts: list[int] = []

        async def reader():
            while not stop_event.is_set():
                t0 = time.monotonic()
                resp = await reader_adapter.get_work_item(work_item_id=wi_id)
                dt = time.monotonic() - t0
                read_durations.append(dt)
                # Response schema (WorkItemDetailResponse, schemas.py:295):
                # has `recent_events`. If this key disappears or is renamed,
                # we want a loud test failure — assert positively rather
                # than silently fall back to 0.
                assert "recent_events" in resp, (
                    f"GET /work_items/<id> response missing 'recent_events' — "
                    f"schema change? Got keys: {sorted(resp.keys())}"
                )
                seen_counts.append(len(resp["recent_events"]))
                await asyncio.sleep(READ_INTERVAL_S)

        writer_task = asyncio.create_task(writer())
        reader_task = asyncio.create_task(reader())
        await writer_task
        stop_event.set()
        await reader_task

        # Invariant 1: every read completed within the budget
        over_budget = [d for d in read_durations if d > READ_BUDGET_S]
        assert not over_budget, (
            f"{len(over_budget)} reads exceeded {READ_BUDGET_S}s budget; "
            f"max={max(read_durations):.3f}s, samples over budget: "
            f"{[f'{d:.3f}' for d in over_budget[:5]]}"
        )

        # Invariant 2: read count is non-decreasing across polls
        for prev, cur in zip(seen_counts, seen_counts[1:]):
            assert cur >= prev, (
                f"event count regressed: prev={prev} cur={cur} in {seen_counts}"
            )

        # Invariant 3: final DB state has all 50 emits
        async with seeded_users.connect() as conn:
            final_count = (await conn.execute(sa.text("""
                SELECT count(*) FROM agent_events
                WHERE work_item_id = :wid AND event_type = 'note'
            """), {"wid": wi_id})).scalar_one()
            assert final_count == WRITER_BURST_COUNT, (
                f"expected {WRITER_BURST_COUNT} note events, got {final_count}"
            )
    finally:
        await reader_client.aclose()


async def test_two_concurrent_readers_same_wi(team_adapters, seeded_users):
    """Two readers polling the same wi simultaneously — neither blocks the other.

    Sanity counter-test: even when both are read-only, aihub's connection
    pool / tx isolation must let them run concurrently without serialization.
    """
    reader_a = team_adapters["zhang"]
    reader_b = team_adapters["li"]

    wi_id = (await reader_a.create_work_item(
        project="marketplace", goal="F: concurrent-readers target",
        scenario="coding", declared_resources=[],
    ))["work_item_id"]

    async def poll(adapter, n: int):
        durations = []
        for _ in range(n):
            t0 = time.monotonic()
            await adapter.get_work_item(work_item_id=wi_id)
            durations.append(time.monotonic() - t0)
        return durations

    a_durations, b_durations = await asyncio.gather(
        poll(reader_a, 10), poll(reader_b, 10),
    )

    for d in a_durations + b_durations:
        assert d < READ_BUDGET_S, f"concurrent read exceeded budget: {d:.3f}s"
