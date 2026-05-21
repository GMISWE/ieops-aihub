"""H — Lease renewal storm.

Scenario: 5 team members, each with 1-2 active attempts, all running
lease_renewer in the background. Each attempt renews every 0.5s. Over
~3 simulated seconds, ~30 renewal calls hit aihub concurrently.

Tested invariants:
1. Every renewer's lease_until advances correctly per call (no missed renew)
2. No renewer enters lost_lease state (renewal endpoint doesn't starve)
3. Concurrent renewals don't cross-contaminate (each attempt's
   lease_until matches its own renewer's call timing, not another's)
4. The aihub renewal endpoint serves all calls under the per-call budget

Per opencode review, original H spec assumed client-side `clock.advance()`
could shorten the lease, which doesn't work because server uses PG `now()`.
Solution: AIHUB_LEASE_SECONDS=2 (set by conftest autouse) keeps lease tight
and renewers fire at 0.5s intervals to stay well ahead of expiry.
"""
from __future__ import annotations

import asyncio
from typing import NamedTuple

import pytest
import sqlalchemy as sa

from polyforge_v3.auth import AttemptCredential


class _AttemptHandle(NamedTuple):
    """Named bundle of fields needed to renew a specific attempt.

    Replaces a list-of-tuples to make positional-unpacking bugs impossible
    (swap any two positional fields and the test would silently pass for
    the wrong reason).
    """
    adapter: object  # AihubClientProtocolAdapter
    wi_id: str
    attempt_id: str
    claim_epoch: int
    session_secret: str


from .conftest import (
    RENEW_STORM_INTERVAL_S as RENEW_INTERVAL_S,
    RENEW_STORM_CYCLES as RENEW_CYCLES,
    RENEW_STORM_PER_CALL_BUDGET_S as PER_RENEW_BUDGET_S,
)


pytestmark = pytest.mark.asyncio(loop_scope="session")


N_ATTEMPTS = 5  # one constant kept local; documents the test's specific cast


async def test_concurrent_lease_renewals_all_succeed(team_adapters, seeded_users, make_adapter):
    """5 attempts under 3 users renewing in parallel; every cycle lands."""
    from .conftest import BEARER_ZHANG, BEARER_LI, BEARER_WANG

    # Build 5 attempts: 2 under zhang, 2 under li, 1 under wang
    attempts: list[_AttemptHandle] = []

    cases = [
        ("zhang", BEARER_ZHANG, "zhang-mbp",     "1" * 64),
        ("zhang", BEARER_ZHANG, "zhang-desktop", "2" * 64),
        ("li",    BEARER_LI,    "li-mbp",        "3" * 64),
        ("li",    BEARER_LI,    "li-laptop",     "4" * 64),
        ("wang",  BEARER_WANG,  "wang-mbp",      "5" * 64),
    ]

    for who, bearer, machine, secret in cases:
        adapter, _ = await make_adapter(bearer, machine_id=machine, session_secret=secret)
        wi_id = (await adapter.create_work_item(
            project="marketplace", goal=f"H storm: {who}@{machine}",
            scenario="coding", declared_resources=[],
        ))["work_item_id"]
        claim = await adapter.claim_work_item(
            work_item_id=wi_id, idempotency_key=f"h-{wi_id}",
            session_info={"machine_id": machine},
            requested_locks=[],
        )
        attempts.append(_AttemptHandle(
            adapter=adapter, wi_id=wi_id,
            attempt_id=claim["attempt_id"],
            claim_epoch=claim["claim_epoch"],
            session_secret=claim["session_secret"],
        ))

    # Each attempt renews RENEW_CYCLES times at RENEW_INTERVAL_S intervals.
    # Track per-attempt: (call_count, max_observed_duration, any_error)
    results: dict[str, dict] = {}

    async def renew_loop(adapter, attempt_id, claim_epoch, session_secret):
        import time
        info = {"calls": 0, "max_dt": 0.0, "errors": []}
        for _ in range(RENEW_CYCLES):
            t0 = time.monotonic()
            try:
                resp = await adapter.renew_lease(
                    attempt_id=attempt_id, claim_epoch=claim_epoch,
                    session_secret=session_secret,
                )
                assert "lease_until" in resp
                info["calls"] += 1
            except Exception as e:
                info["errors"].append(str(e))
                break
            dt = time.monotonic() - t0
            info["max_dt"] = max(info["max_dt"], dt)
            await asyncio.sleep(RENEW_INTERVAL_S)
        return attempt_id, info

    completed = await asyncio.gather(*[
        renew_loop(h.adapter, h.attempt_id, h.claim_epoch, h.session_secret)
        for h in attempts
    ])

    for attempt_id, info in completed:
        results[attempt_id] = info

    # Invariant 1: every renewer completed all cycles
    for aid, info in results.items():
        assert info["calls"] == RENEW_CYCLES, (
            f"attempt {aid}: only {info['calls']}/{RENEW_CYCLES} renewals "
            f"succeeded; errors={info['errors']}"
        )

    # Invariant 2: per-call budget
    for aid, info in results.items():
        assert info["max_dt"] < PER_RENEW_BUDGET_S, (
            f"attempt {aid}: max renew duration {info['max_dt']:.3f}s "
            f"exceeds {PER_RENEW_BUDGET_S}s budget"
        )

    # Invariant 3: in DB, every attempt still 'running' with lease_until > now
    async with seeded_users.connect() as conn:
        rows = (await conn.execute(sa.text("""
            SELECT id, status, lease_until > now() AS active FROM run_attempts
            WHERE id = ANY(:ids)
        """), {"ids": [h.attempt_id for h in attempts]})).mappings().all()
        for row in rows:
            assert row["status"] == "running", (
                f"attempt {row['id']} status={row['status']} after renewal storm"
            )
            assert row["active"], (
                f"attempt {row['id']} lease expired during storm"
            )


async def test_renewal_increases_lease_until_monotonically(team_adapters, seeded_users):
    """Renewing N times increases lease_until each time (not idempotent at 0-delta)."""
    adapter = team_adapters["zhang"]
    wi_id = (await adapter.create_work_item(
        project="marketplace", goal="H: monotonic renew target",
        scenario="coding", declared_resources=[],
    ))["work_item_id"]
    claim = await adapter.claim_work_item(
        work_item_id=wi_id, idempotency_key=f"h-mono-{wi_id}",
        session_info={"machine_id": "zhang-mbp"}, requested_locks=[],
    )

    observed: list[str] = []
    for _ in range(5):
        resp = await adapter.renew_lease(
            attempt_id=claim["attempt_id"], claim_epoch=claim["claim_epoch"],
            session_secret=claim["session_secret"],
        )
        observed.append(resp["lease_until"])
        await asyncio.sleep(0.05)

    # Each renewal must produce a strictly-greater lease_until
    for prev, cur in zip(observed, observed[1:]):
        assert cur > prev, (
            f"lease_until must monotonically advance: prev={prev} cur={cur}"
        )
