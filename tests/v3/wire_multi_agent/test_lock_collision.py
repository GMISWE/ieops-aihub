"""P1.4 — cross-WI git_branch lock collision.

Scenario: zhang and li each open their own work_item, but both declare a
write intent on the SAME git branch (e.g., both want to land changes on
`marketplace/polyforge/fix-login-500`). The aihub server's typed resource
locks must reject the second claim at the lock-validation step (Step 4 in
`app/run_attempts.py:148-184`) with `CONFLICT_HARD_BLOCK`, naming the
exact dimension that collided.

Critical design point per opencode review: the lock-collision rejection
only surfaces when the wis are DIFFERENT. Same-wi races hit the busy-check
at Step 3 first (`CONFLICT_BUSY_NOT_TAKEOVER_ELIGIBLE`) and never reach
Step 4. Two distinct wis sharing a lock key is the only race that
exercises the typed-lock contract.

The lock-conflict rejection can land via either of two server paths:

  * Step 4 (SELECT-based detection, `run_attempts.py:148-184`): when the
    first claim's lock is already visible at SELECT time, the second
    claim is rejected with `CONFLICT_HARD_BLOCK` AND structured details
    (`rule_id`, `resource_type`, `resource_key`, `conflicts_with`).
  * Step 8 (INSERT UNIQUE constraint race, `run_attempts.py:265-272`):
    when the second claim's SELECT happens before the first claim's
    transaction commits but its INSERT races the UNIQUE constraint, the
    second is rejected with `CONFLICT_HARD_BLOCK` but `details=None` —
    the contested type/key appear only in the message string.

Both paths are valid; this test accepts either (and records which fired
for diagnostic purposes). What it asserts unconditionally:

  - error code is `CONFLICT_HARD_BLOCK`
  - the message OR details name `git_branch` and the contested key
  - DB ends with exactly one lock row pointing at the winner
"""
from __future__ import annotations

import asyncio

import pytest
import sqlalchemy as sa

from polyforge_v3.aihub.errors import AihubError, ErrorCode


pytestmark = pytest.mark.asyncio(loop_scope="session")


# Single contested branch — both wis want to write here
CONTESTED_BRANCH_KEY = "marketplace/polyforge/fix-login-500"


async def test_two_wis_one_branch_loser_blocked(team_adapters, seeded_users):
    """zhang and li both want the same git branch. One claim succeeds,
    the other is rejected with a lock-collision error naming git_branch.
    """
    adapter_z = team_adapters["zhang"]
    adapter_l = team_adapters["li"]

    # Each user creates their OWN work_item — distinct goals, distinct wis
    wi_z = (await adapter_z.create_work_item(
        project="marketplace", goal="zhang: fix login 500",
        scenario="coding", declared_resources=[],
    ))["work_item_id"]
    wi_l = (await adapter_l.create_work_item(
        project="marketplace", goal="li: also fix login 500",
        scenario="coding", declared_resources=[],
    ))["work_item_id"]
    assert wi_z != wi_l

    locks = [
        {"resource_type": "git_branch", "resource_key": CONTESTED_BRANCH_KEY},
    ]

    results = await asyncio.gather(
        adapter_z.claim_work_item(
            work_item_id=wi_z, idempotency_key=f"lc-z-{wi_z}",
            session_info={"machine_id": "zhang-mbp"}, requested_locks=locks,
        ),
        adapter_l.claim_work_item(
            work_item_id=wi_l, idempotency_key=f"lc-l-{wi_l}",
            session_info={"machine_id": "li-mbp"}, requested_locks=locks,
        ),
        return_exceptions=True,
    )

    successes = [r for r in results if not isinstance(r, BaseException)]
    failures = [r for r in results if isinstance(r, BaseException)]
    assert len(successes) == 1, (
        f"expected 1 winner, got {len(successes)}; results={results}"
    )
    assert len(failures) == 1

    err = failures[0]
    assert isinstance(err, AihubError)
    assert err.code is ErrorCode.CONFLICT_HARD_BLOCK, (
        f"loser must raise CONFLICT_HARD_BLOCK; got {err.code} ({err.message!r})"
    )

    winner = successes[0]

    # The colliding dimension must be identifiable from EITHER details (Step 4
    # path) OR message string (Step 8 race path). For via_message, require
    # the EXACT contested key string (not just any "git_branch" substring) so
    # a server bug that returns the wrong key wouldn't slip past.
    via_details = (
        err.details is not None
        and err.details.get("resource_type") == "git_branch"
        and err.details.get("resource_key") == CONTESTED_BRANCH_KEY
    )
    # Step 8 message shape per run_attempts.py:269-272:
    #   "lock {resource_type}:{resource_key} acquired by concurrent racer"
    expected_token = f"git_branch:{CONTESTED_BRANCH_KEY}"
    via_message = expected_token in err.message
    assert via_details or via_message, (
        f"loser must identify the contested git_branch:{CONTESTED_BRANCH_KEY!r} "
        f"resource somehow; message={err.message!r}, details={err.details!r}"
    )

    # If we got Step 4 path, the conflicts_with field must name the winner
    if via_details and "conflicts_with" in (err.details or {}):
        assert err.details["conflicts_with"].get("attempt_id") == winner["attempt_id"]

    # DB-level: exactly one lock row for this branch key
    async with seeded_users.connect() as conn:
        lock_rows = (await conn.execute(sa.text("""
            SELECT owner_attempt_id, claim_epoch
            FROM resource_locks
            WHERE resource_type = 'git_branch' AND resource_key = :k
        """), {"k": CONTESTED_BRANCH_KEY})).mappings().all()
        assert len(lock_rows) == 1
        assert lock_rows[0]["owner_attempt_id"] == winner["attempt_id"]


async def test_distinct_lock_keys_both_succeed(team_adapters, seeded_users):
    """Sanity counter-test: two wis declaring DIFFERENT lock keys both win.

    Verifies that the collision check is precise — it doesn't false-positive
    on different keys, even if same resource_type. Without this counter-test,
    a broken implementation that rejects ANY git_branch claim while one is
    held would pass test 1 but break the entire system.
    """
    adapter_z = team_adapters["zhang"]
    adapter_l = team_adapters["li"]

    wi_z = (await adapter_z.create_work_item(
        project="marketplace", goal="zhang: feature A",
        scenario="coding", declared_resources=[],
    ))["work_item_id"]
    wi_l = (await adapter_l.create_work_item(
        project="marketplace", goal="li: feature B",
        scenario="coding", declared_resources=[],
    ))["work_item_id"]

    results = await asyncio.gather(
        adapter_z.claim_work_item(
            work_item_id=wi_z, idempotency_key=f"distinct-z-{wi_z}",
            session_info={"machine_id": "zhang-mbp"},
            requested_locks=[{"resource_type": "git_branch",
                              "resource_key": "marketplace/polyforge/feature-a"}],
        ),
        adapter_l.claim_work_item(
            work_item_id=wi_l, idempotency_key=f"distinct-l-{wi_l}",
            session_info={"machine_id": "li-mbp"},
            requested_locks=[{"resource_type": "git_branch",
                              "resource_key": "marketplace/polyforge/feature-b"}],
        ),
        return_exceptions=True,
    )

    failures = [r for r in results if isinstance(r, BaseException)]
    assert not failures, (
        f"both claims should succeed with distinct lock keys; "
        f"got failures: {[(type(f).__name__, str(f)) for f in failures]}"
    )

    async with seeded_users.connect() as conn:
        lock_rows = (await conn.execute(sa.text("""
            SELECT resource_key, owner_attempt_id
            FROM resource_locks
            WHERE resource_type = 'git_branch'
              AND resource_key LIKE 'marketplace/polyforge/feature-%'
            ORDER BY resource_key
        """))).mappings().all()
        keys = [r["resource_key"] for r in lock_rows]
        assert keys == [
            "marketplace/polyforge/feature-a",
            "marketplace/polyforge/feature-b",
        ]
