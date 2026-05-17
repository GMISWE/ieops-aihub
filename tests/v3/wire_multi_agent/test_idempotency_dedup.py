"""I — Idempotency-key concurrent dedup on claim_work_item.

Scenario: zhao's network flakes; his CLI auto-retries claim_work_item 3
times with the SAME idempotency_key. First attempt lands but response is
lost (timeout). Attempts 2 and 3 arrive at the server concurrently. The
server must:

1. Produce exactly ONE side-effect (one run_attempt row, one
   attempt_started event)
2. Return identical responses to all 3 calls (the idempotency-replay
   cache returns the first call's result body)
3. Maintain the contract that idempotency_key uniqueness is per-wi
   (different wis can reuse the same key)

Per `app/run_attempts.py:123-133`, idempotency replay is implemented as a
SELECT on `(work_item_id, idempotency_key)` BEFORE the eligibility checks
in Step 3-4. So if the first call's row is visible to the retry, the
retry returns its row directly.

Per opencode review, original I spec targeted pf3_pause idempotency which
doesn't exist; retargeted to claim_work_item which IS idempotency-keyed.
"""
from __future__ import annotations

import asyncio

import pytest
import sqlalchemy as sa


pytestmark = pytest.mark.asyncio(loop_scope="session")


async def test_three_concurrent_claims_same_idem_key_one_effect(team_adapters, seeded_users):
    """3 concurrent claims with same idempotency_key produce 1 attempt, identical responses."""
    adapter = team_adapters["zhang"]

    wi_id = (await adapter.create_work_item(
        project="marketplace", goal="I: idempotent retry target",
        scenario="coding", declared_resources=[],
    ))["work_item_id"]

    SHARED_KEY = f"zhao-cli-retry-{wi_id}"

    # 3 concurrent calls, identical body, identical idempotency_key
    results = await asyncio.gather(*[
        adapter.claim_work_item(
            work_item_id=wi_id, idempotency_key=SHARED_KEY,
            session_info={"machine_id": "zhang-mbp"},
            requested_locks=[],
        )
        for _ in range(3)
    ], return_exceptions=True)

    # Invariant 1: all 3 responses succeeded (no rejection on retry)
    successes = [r for r in results if not isinstance(r, BaseException)]
    failures = [r for r in results if isinstance(r, BaseException)]
    assert len(successes) == 3, (
        f"all 3 idempotent retries must succeed; got {len(failures)} failures: "
        f"{[(type(f).__name__, str(f)) for f in failures]}"
    )

    # Invariant 2: all 3 returned the SAME attempt_id + claim_epoch
    attempt_ids = {r["attempt_id"] for r in successes}
    epochs = {r["claim_epoch"] for r in successes}
    assert len(attempt_ids) == 1, (
        f"all retries must return same attempt_id; got {attempt_ids}"
    )
    assert epochs == {1}, f"all retries must return claim_epoch=1; got {epochs}"

    # Invariant 3: DB has exactly ONE run_attempt for this wi
    async with seeded_users.connect() as conn:
        attempt_rows = (await conn.execute(sa.text(
            "SELECT count(*) FROM run_attempts WHERE work_item_id = :wid"
        ), {"wid": wi_id})).scalar_one()
        assert attempt_rows == 1, (
            f"only 1 attempt should be created for idempotent retries; got {attempt_rows}"
        )

        # And exactly ONE attempt_started event
        started_count = (await conn.execute(sa.text("""
            SELECT count(*) FROM agent_events
            WHERE work_item_id = :wid AND event_type = 'attempt_started'
        """), {"wid": wi_id})).scalar_one()
        assert started_count == 1, (
            f"only 1 attempt_started event for idempotent retries; got {started_count}"
        )


async def test_same_idem_key_different_wis_both_succeed(team_adapters, seeded_users):
    """Idempotency key is scoped per work_item; same key on different wis works fine."""
    adapter = team_adapters["zhang"]

    wi_a = (await adapter.create_work_item(
        project="marketplace", goal="I: idem-scope a", scenario="coding", declared_resources=[],
    ))["work_item_id"]
    wi_b = (await adapter.create_work_item(
        project="marketplace", goal="I: idem-scope b", scenario="coding", declared_resources=[],
    ))["work_item_id"]
    assert wi_a != wi_b

    SHARED_KEY = "cross-wi-reuse"  # same key string for both

    claim_a = await adapter.claim_work_item(
        work_item_id=wi_a, idempotency_key=SHARED_KEY,
        session_info={"machine_id": "zhang-mbp"}, requested_locks=[],
    )
    claim_b = await adapter.claim_work_item(
        work_item_id=wi_b, idempotency_key=SHARED_KEY,
        session_info={"machine_id": "zhang-mbp"}, requested_locks=[],
    )

    # Two distinct attempts; SHARED_KEY didn't conflict
    assert claim_a["attempt_id"] != claim_b["attempt_id"]
    assert claim_a["claim_epoch"] == 1
    assert claim_b["claim_epoch"] == 1

    async with seeded_users.connect() as conn:
        rows = (await conn.execute(sa.text("""
            SELECT work_item_id, idempotency_key FROM run_attempts
            WHERE work_item_id IN (:a, :b)
            ORDER BY work_item_id
        """), {"a": wi_a, "b": wi_b})).mappings().all()
        assert len(rows) == 2
        # Both rows share the idempotency_key string but on different wis
        assert all(r["idempotency_key"] == SHARED_KEY for r in rows)


async def test_concurrent_distinct_keys_produce_one_winner(team_adapters, seeded_users):
    """Negative: DIFFERENT idem keys on same wi => not idempotent => only 1 winner.

    Counter-test to lock down semantics: if keys differ, the second call
    is a fresh claim attempt and hits the busy-check (Step 3) rejection
    rather than the idempotency-replay path. Verifies key-based dedup is
    actually keyed, not just any-claim-replays.
    """
    from polyforge_v3.aihub.errors import AihubError, ErrorCode

    adapter_z = team_adapters["zhang"]
    adapter_l = team_adapters["li"]

    wi_id = (await adapter_z.create_work_item(
        project="marketplace", goal="I: distinct-keys counter",
        scenario="coding", declared_resources=[],
    ))["work_item_id"]

    results = await asyncio.gather(
        adapter_z.claim_work_item(
            work_item_id=wi_id, idempotency_key="zhang-distinct",
            session_info={"machine_id": "zhang-mbp"}, requested_locks=[],
        ),
        adapter_l.claim_work_item(
            work_item_id=wi_id, idempotency_key="li-distinct",
            session_info={"machine_id": "li-mbp"}, requested_locks=[],
        ),
        return_exceptions=True,
    )

    successes = [r for r in results if not isinstance(r, BaseException)]
    failures = [r for r in results if isinstance(r, BaseException)]
    assert len(successes) == 1
    assert isinstance(failures[0], AihubError)
    assert failures[0].code is ErrorCode.CONFLICT_BUSY_NOT_TAKEOVER_ELIGIBLE
