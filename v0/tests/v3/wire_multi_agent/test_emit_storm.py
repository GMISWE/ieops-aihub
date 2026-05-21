"""P1.3 — shared-cred 4-agent emit storm.

Scenario: oncall (zhao in the design, mapped to zhang here since we reuse
reference users) claims wi_prod_outage and spawns 4 investigation agents:
agent-grafana, agent-kubectl-logs, agent-chaos, agent-codex-review. All 4
share the same AttemptCredential and emit `note` events in parallel via
`asyncio.gather`.

Per `app/run_attempts.py` + `routes/v3_misc.py:161-249`, every emit_event
is attempt-fenced: it requires (attempt_id, claim_epoch, session_secret)
matching the active attempt. With a SHARED cred, 4 concurrent emits must:

1. All succeed (no fence rejection)
2. All persist (exactly N rows in `agent_events`)
3. Be deterministically orderable by `(created_at, id)` per `v3_misc.py:245-249`
4. Preserve their payloads (no row-level overwrite under concurrency)
5. Be attributable to the same run_attempt_id

This is the core "multi-agent within one attempt" invariant — without it,
the design of "user spawns N background agents under one task" breaks
silently.
"""
from __future__ import annotations

import asyncio
import json

import pytest
import sqlalchemy as sa

from polyforge_v3.auth import AttemptCredential


from .conftest import (
    EMIT_STORM_N_AGENTS as N_AGENTS,
    EMIT_STORM_N_ROUNDS as N_ROUNDS,
    EMIT_STORM_TOTAL as TOTAL_EMITS,
)


pytestmark = pytest.mark.asyncio(loop_scope="session")


async def test_shared_cred_4_agent_emit_storm(team_adapters, seeded_users):
    """20 concurrent emit_event calls under one attempt, all persist correctly."""
    adapter = team_adapters["zhang"]

    # Set up: create + claim a work_item, propagate cred into adapter
    create_resp = await adapter.create_work_item(
        project="marketplace", goal="P1.3 emit storm target",
        scenario="coding", declared_resources=[],
    )
    wi_id = create_resp["work_item_id"]

    claim = await adapter.claim_work_item(
        work_item_id=wi_id, idempotency_key="emit-storm-claim",
        session_info={"machine_id": "zhang-mbp"},
        requested_locks=[],
    )
    attempt_id = claim["attempt_id"]
    claim_epoch = claim["claim_epoch"]

    # All 4 agents share the SAME AttemptCredential — that's the whole point.
    cred = AttemptCredential(
        attempt_id=attempt_id, claim_epoch=claim_epoch,
        session_secret=claim["session_secret"],
    )
    adapter.set_cred(cred)

    # Spawn 20 concurrent emit_event calls (4 agents × 5 rounds)
    async def emit(agent_idx: int, round_idx: int):
        await adapter.emit_event(
            work_item_id=wi_id,
            event_type="note",
            payload={
                "agent": f"agent-{agent_idx}",
                "round": round_idx,
                "seq": agent_idx * N_ROUNDS + round_idx,
            },
            attempt_id=attempt_id,
            claim_epoch=claim_epoch,
            session_secret=claim["session_secret"],
        )

    tasks = [
        emit(a, r)
        for r in range(N_ROUNDS)
        for a in range(N_AGENTS)
    ]
    results = await asyncio.gather(*tasks, return_exceptions=True)

    # Invariant 1: All emits succeeded — no fence rejection, no race failure
    failures = [r for r in results if isinstance(r, BaseException)]
    assert not failures, (
        f"expected 0 failures across {TOTAL_EMITS} concurrent emits; got "
        f"{len(failures)}: {[(type(f).__name__, str(f)) for f in failures]}"
    )

    # Invariant 2-5: DB-level checks
    async with seeded_users.connect() as conn:
        rows = (await conn.execute(sa.text("""
            SELECT id, run_attempt_id, event_type, payload, created_at
            FROM agent_events
            WHERE work_item_id = :wid AND event_type = 'note'
            ORDER BY created_at ASC, id ASC
        """), {"wid": wi_id})).mappings().all()

        # Exactly TOTAL_EMITS 'note' rows
        assert len(rows) == TOTAL_EMITS, (
            f"expected {TOTAL_EMITS} note events, got {len(rows)}"
        )

        # Every row attributed to our single run_attempt
        attempts = {r["run_attempt_id"] for r in rows}
        assert attempts == {attempt_id}, (
            f"all events must be attributed to {attempt_id}; got {attempts}"
        )

        # Payload integrity: every (agent, round) pair appears exactly once
        seen_pairs = set()
        for r in rows:
            payload = r["payload"]
            if isinstance(payload, str):
                payload = json.loads(payload)
            pair = (payload["agent"], payload["round"])
            assert pair not in seen_pairs, (
                f"duplicate payload pair {pair} — concurrent emit collision?"
            )
            seen_pairs.add(pair)
        expected_pairs = {
            (f"agent-{a}", r) for a in range(N_AGENTS) for r in range(N_ROUNDS)
        }
        assert seen_pairs == expected_pairs, (
            f"missing pairs: {expected_pairs - seen_pairs}; "
            f"extra: {seen_pairs - expected_pairs}"
        )

        # Ordering check — verify the `(created_at, id)` total order produces
        # a fully sortable result with no NULLs and no NaT. We can't assert
        # *which* order is correct (any interleaving is legal under
        # concurrency), only that the tuple is well-defined. This catches
        # missing columns / index breakage; it does NOT (and cannot) verify
        # arrival order at the wire layer, which is irrecoverable.
        for r in rows:
            assert r["created_at"] is not None, (
                "created_at must be set on every emit (NULL would break ordering)"
            )
            assert r["id"] is not None


async def test_emit_storm_rejects_stale_cred(team_adapters, seeded_users):
    """A 5th agent with the WRONG cred must be rejected mid-storm.

    Specifically: after we claim attempt epoch=1, attempting to emit_event
    with attempt_id matching but claim_epoch=99 (stale) must be rejected.
    Mixed in among the legitimate 20 emits, the bad one fails, the good
    ones succeed.
    """
    adapter = team_adapters["zhang"]

    create_resp = await adapter.create_work_item(
        project="marketplace", goal="P1.3 stale-cred reject target",
        scenario="coding", declared_resources=[],
    )
    wi_id = create_resp["work_item_id"]

    claim = await adapter.claim_work_item(
        work_item_id=wi_id, idempotency_key="emit-stale-claim",
        session_info={"machine_id": "zhang-mbp"},
        requested_locks=[],
    )
    good_attempt = claim["attempt_id"]
    good_epoch = claim["claim_epoch"]
    good_secret = claim["session_secret"]

    adapter.set_cred(AttemptCredential(
        attempt_id=good_attempt, claim_epoch=good_epoch,
        session_secret=good_secret,
    ))

    async def good_emit(seq: int):
        await adapter.emit_event(
            work_item_id=wi_id, event_type="note",
            payload={"seq": seq, "kind": "good"},
            attempt_id=good_attempt, claim_epoch=good_epoch,
            session_secret=good_secret,
        )

    async def bad_emit():
        await adapter.emit_event(
            work_item_id=wi_id, event_type="note",
            payload={"kind": "bad", "epoch": 99},
            attempt_id=good_attempt,
            claim_epoch=99,  # stale
            session_secret=good_secret,
        )

    tasks = [good_emit(i) for i in range(10)] + [bad_emit()]
    results = await asyncio.gather(*tasks, return_exceptions=True)

    successes = [r for r in results if not isinstance(r, BaseException)]
    failures = [r for r in results if isinstance(r, BaseException)]
    assert len(successes) == 10, (
        f"expected 10 good emits to succeed, got {len(successes)}; "
        f"failures={[(type(f).__name__, str(f)) for f in failures]}"
    )
    assert len(failures) == 1, (
        f"expected 1 stale-cred rejection; got {len(failures)}"
    )

    async with seeded_users.connect() as conn:
        good_rows = (await conn.execute(sa.text("""
            SELECT payload FROM agent_events
            WHERE work_item_id = :wid AND event_type = 'note'
        """), {"wid": wi_id})).scalars().all()

        # 10 rows, all marked "good", zero "bad" payloads landed
        assert len(good_rows) == 10
        for p in good_rows:
            payload = json.loads(p) if isinstance(p, str) else p
            assert payload["kind"] == "good", (
                f"stale-cred emit must NOT persist; found bad row {payload}"
            )
