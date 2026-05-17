"""P1.1 — concurrent claim, one wins.

Scenario: PM (qian) queued a work_item last night. At standup, both zhang
and wang see it via /pf3-list-work-items and both fire /pf3-claim within
milliseconds. The aihub server must atomically pick exactly one winner.

This is the ROOT contract of the multi-agent architecture. Without this
test, claim atomicity is unverified at the wire layer.

Concurrency model in this test: `asyncio.gather` + `httpx.ASGITransport`
provides in-process concurrency at the *API* boundary. Both claim requests
are issued before either response returns, but the ASGI event loop
serializes them through the single aihub app instance — so they don't
produce a *DB-level* race at the (work_item_id, claim_epoch) UNIQUE
constraint. Instead, the second claim observes the first's already-running
attempt + live lease and is rejected at `app/run_attempts.py:137-146` with
`CONFLICT_BUSY_NOT_TAKEOVER_ELIGIBLE`.

This is the realistic outcome for two near-simultaneous CLI invocations
hitting the server. The pure DB-level epoch-collision race
(`CONFLICT_DUPLICATE_REQUEST` from `app/run_attempts.py:241-252`) needs
multiple separate Python processes hitting PG over the network — a
follow-up coverage gap, not addressed here.

In either case the *server-side guarantee* is the same: exactly one
winner, no double-claim, no orphan rows. That guarantee is what this
test verifies.
"""
from __future__ import annotations

import asyncio

import pytest
import sqlalchemy as sa

from polyforge_v3.aihub.errors import AihubError, ErrorCode


pytestmark = pytest.mark.asyncio(loop_scope="session")


async def test_two_serialized_claims_one_wins_via_busy_check(team_adapters, seeded_users):
    """Two adapters concurrently claim the same queued wi. Exactly one wins.

    Verifies (in this order):
      1. asyncio.gather returns exactly 1 success + 1 AihubError
      2. The error's ErrorCode is CONFLICT_BUSY_NOT_TAKEOVER_ELIGIBLE
         (the realistic ASGI-serialized outcome — see module docstring)
      3. The error details name the winner (owner_attempt_id, lease_until)
      4. DB row count for run_attempts on this wi == 1 (no orphan rows)
      5. agent_events has exactly one 'attempt_started' for this wi
      6. work_item.status == 'running' and current_attempt_id == winner's id
    """
    adapter_z = team_adapters["zhang"]
    adapter_w = team_adapters["wang"]

    # 1. Set up a queued work_item (no auto-claim)
    create_resp = await adapter_z.create_work_item(
        project="marketplace",
        goal="P1.1 cold-race target",
        scenario="coding",
        declared_resources=[],
    )
    wi_id = create_resp["work_item_id"]
    assert wi_id.startswith("wi_")

    # Confirm pre-race state
    async with seeded_users.connect() as conn:
        row = (await conn.execute(sa.text(
            "SELECT status, current_attempt_id FROM work_items WHERE id = :id"
        ), {"id": wi_id})).mappings().first()
        assert row["status"] == "queued"
        assert row["current_attempt_id"] is None

    # 2. CONCURRENT claim from both adapters. Distinct idempotency_keys so
    # idempotency-replay short-circuit doesn't kick in — we want the actual
    # claim_epoch UNIQUE race.
    results = await asyncio.gather(
        adapter_z.claim_work_item(
            work_item_id=wi_id,
            idempotency_key="p1-1-zhang-key",
            session_info={"machine_id": "zhang-mbp"},
            requested_locks=[],
        ),
        adapter_w.claim_work_item(
            work_item_id=wi_id,
            idempotency_key="p1-1-wang-key",
            session_info={"machine_id": "wang-mbp"},
            requested_locks=[],
        ),
        return_exceptions=True,
    )

    # 3. Exactly one success, one AihubError
    successes = [r for r in results if not isinstance(r, BaseException)]
    failures = [r for r in results if isinstance(r, BaseException)]
    assert len(successes) == 1, (
        f"expected exactly 1 winner, got {len(successes)}; results={results}"
    )
    assert len(failures) == 1
    err = failures[0]
    assert isinstance(err, AihubError), (
        f"loser must raise AihubError, got {type(err).__name__}: {err}"
    )
    assert err.code is ErrorCode.CONFLICT_BUSY_NOT_TAKEOVER_ELIGIBLE, (
        f"loser must raise CONFLICT_BUSY_NOT_TAKEOVER_ELIGIBLE, "
        f"got {err.code} ({err.message!r})"
    )

    # Loser's error details must name the winner
    winner = successes[0]
    assert err.details is not None
    assert err.details["owner_attempt_id"] == winner["attempt_id"], (
        f"loser's owner_attempt_id ({err.details['owner_attempt_id']}) must match "
        f"winner's attempt_id ({winner['attempt_id']})"
    )
    assert "lease_until" in err.details

    # 4. Winner's response shape
    assert winner["attempt_id"].startswith("ra_")
    assert winner["claim_epoch"] == 1

    # 5. DB invariants
    async with seeded_users.connect() as conn:
        # Exactly 1 run_attempt for this wi
        attempts = (await conn.execute(sa.text(
            "SELECT id, claim_epoch, status FROM run_attempts WHERE work_item_id = :wid"
        ), {"wid": wi_id})).mappings().all()
        assert len(attempts) == 1, f"expected 1 attempt, got {len(attempts)}: {list(attempts)}"
        assert attempts[0]["id"] == winner["attempt_id"]
        assert attempts[0]["claim_epoch"] == 1
        assert attempts[0]["status"] == "running"

        # work_item state matches winner
        wi_row = (await conn.execute(sa.text(
            "SELECT status, current_attempt_id FROM work_items WHERE id = :id"
        ), {"id": wi_id})).mappings().first()
        assert wi_row["status"] == "running"
        assert wi_row["current_attempt_id"] == winner["attempt_id"]

        # Exactly one attempt_started event (no duplicate)
        started_events = (await conn.execute(sa.text("""
            SELECT id, run_attempt_id, payload FROM agent_events
            WHERE work_item_id = :wid AND event_type = 'attempt_started'
        """), {"wid": wi_id})).mappings().all()
        assert len(started_events) == 1
        assert started_events[0]["run_attempt_id"] == winner["attempt_id"]


async def test_serialized_claim_no_orphan_locks(team_adapters, seeded_users):
    """The loser must not leave orphan resource_locks behind.

    When two concurrent claims declare the SAME resource lock, only the
    winner's locks should land in resource_locks. The loser is rejected
    before Step 8 (lock INSERT) — either at the busy-check (Step 3) or
    via the duplicate-epoch path — so no orphan rows appear.

    This protects against split-brain locks where a loser's lock rows
    linger after its attempt INSERT failed.
    """
    adapter_z = team_adapters["zhang"]
    adapter_w = team_adapters["wang"]

    create_resp = await adapter_z.create_work_item(
        project="marketplace",
        goal="P1.1 orphan-lock target",
        scenario="coding",
        declared_resources=[],
    )
    wi_id = create_resp["work_item_id"]

    locks = [
        {"resource_type": "git_branch", "resource_key": f"marketplace/polyforge/{wi_id}"},
    ]

    results = await asyncio.gather(
        adapter_z.claim_work_item(
            work_item_id=wi_id, idempotency_key="orphan-z-key",
            session_info={"machine_id": "zhang-mbp"}, requested_locks=locks,
        ),
        adapter_w.claim_work_item(
            work_item_id=wi_id, idempotency_key="orphan-w-key",
            session_info={"machine_id": "wang-mbp"}, requested_locks=locks,
        ),
        return_exceptions=True,
    )

    successes = [r for r in results if not isinstance(r, BaseException)]
    assert len(successes) == 1
    winner = successes[0]

    async with seeded_users.connect() as conn:
        lock_rows = (await conn.execute(sa.text("""
            SELECT resource_type, resource_key, owner_attempt_id, claim_epoch
            FROM resource_locks
            WHERE owner_attempt_id IN (
                SELECT id FROM run_attempts WHERE work_item_id = :wid
            )
        """), {"wid": wi_id})).mappings().all()

        # Exactly 1 lock row, owned by winner
        assert len(lock_rows) == 1, (
            f"expected 1 lock, got {len(lock_rows)} (orphan from loser?): {list(lock_rows)}"
        )
        assert lock_rows[0]["owner_attempt_id"] == winner["attempt_id"]
        assert lock_rows[0]["resource_type"] == "git_branch"
        assert lock_rows[0]["claim_epoch"] == 1
