"""P1.2 — lease-expired takeover.

Scenario: wang claims a wi from his mbp (epoch=1). He drops Wi-Fi mid-edit
(simulated as: lease renewer dies). Lease expires. Wang opens his desktop
and re-claims via /pf3-resume — the server detects the expired lease,
supersedes the old attempt, mints a new attempt with epoch=2 and a fresh
session_secret, and transfers the resource locks to the new attempt.

Per `app/run_attempts.py:200-209`:
  - Old attempt's status -> 'superseded' (NOT 'expired'), ended_at = now()
  - Old attempt's resource_locks rows are DELETEd
  - parent_attempt_id of new attempt -> old attempt's id

Per `run_attempts.py:309-329`:
  - 'attempt_taken_over' event emitted in addition to 'attempt_started'
  - is_takeover flag = True in attempt_started payload (when old_a was running)

This test exercises the warm-takeover path that opencode flagged as the
real coverage gap — lost-lease cross-machine recovery.
"""
from __future__ import annotations

import asyncio

import pytest
import sqlalchemy as sa


from .conftest import LEASE_EXPIRY_WAIT_S


pytestmark = pytest.mark.asyncio(loop_scope="session")


async def test_wifi_drop_then_takeover_from_desktop(team_adapters, seeded_users, make_adapter):
    """Wang on mbp claims, his lease expires, then wang on desktop takes over.

    Verifies the full lifecycle: new epoch, locks transferred, old attempt
    superseded, takeover event emitted, all with the right linkages.
    """
    from .conftest import BEARER_WANG

    # Each device generates its own random session_secret (real CLI does this
    # per machine via /pf-v3 init). Two distinct secrets so we can observe
    # the DB-level hash transition.
    SECRET_WANG_MBP = "1" * 64
    SECRET_WANG_DESKTOP = "2" * 64

    adapter_mbp, _ = await make_adapter(
        BEARER_WANG, machine_id="wang-mbp", session_secret=SECRET_WANG_MBP,
    )

    # 1. Wang on mbp claims a wi with a lock
    wi_id = (await adapter_mbp.create_work_item(
        project="marketplace", goal="wang: fix search regression",
        scenario="coding", declared_resources=[],
    ))["work_item_id"]

    locks = [
        {"resource_type": "git_branch",
         "resource_key": f"marketplace/polyforge/{wi_id}"},
    ]
    claim_mbp = await adapter_mbp.claim_work_item(
        work_item_id=wi_id, idempotency_key=f"mbp-{wi_id}",
        session_info={"machine_id": "wang-mbp"},
        requested_locks=locks,
    )
    assert claim_mbp["claim_epoch"] == 1
    attempt_mbp = claim_mbp["attempt_id"]
    secret_mbp = claim_mbp["session_secret"]

    # 2. Simulate wifi drop: just wait for lease expiry (no renewal)
    await asyncio.sleep(LEASE_EXPIRY_WAIT_S)

    # 3. Wang on desktop — build a NEW adapter with a different machine_id
    #    and different session_secret (real CLI per-machine init)
    adapter_dt, _ = await make_adapter(
        BEARER_WANG, machine_id="wang-desktop", session_secret=SECRET_WANG_DESKTOP,
    )

    # 4. Re-claim from desktop. New session_secret per claim.
    claim_dt = await adapter_dt.claim_work_item(
        work_item_id=wi_id, idempotency_key=f"dt-{wi_id}",
        session_info={"machine_id": "wang-desktop"},
        requested_locks=locks,
    )

    # --- Assert response shape ---
    assert claim_dt["claim_epoch"] == 2, (
        f"new epoch must be 2 (was {claim_dt['claim_epoch']})"
    )
    assert claim_dt["attempt_id"] != attempt_mbp
    assert claim_dt["session_secret"] != secret_mbp, (
        "client supplied a distinct secret per device; server should echo desktop's secret"
    )
    assert claim_dt["session_secret"] == SECRET_WANG_DESKTOP

    # --- Assert DB-level invariants ---
    async with seeded_users.connect() as conn:
        # Old attempt: superseded, ended_at set, parent of new attempt
        old = (await conn.execute(sa.text("""
            SELECT id, status, ended_at, claim_epoch FROM run_attempts
            WHERE id = :aid
        """), {"aid": attempt_mbp})).mappings().first()
        assert old["status"] == "superseded", (
            f"old attempt must be 'superseded' after takeover; got {old['status']!r}"
        )
        assert old["ended_at"] is not None
        assert old["claim_epoch"] == 1

        # New attempt: running, epoch=2, parent = old attempt
        new = (await conn.execute(sa.text("""
            SELECT id, status, claim_epoch, parent_attempt_id, machine_id FROM run_attempts
            WHERE id = :aid
        """), {"aid": claim_dt["attempt_id"]})).mappings().first()
        assert new["status"] == "running"
        assert new["claim_epoch"] == 2
        assert new["parent_attempt_id"] == attempt_mbp, (
            f"new attempt's parent must point at old attempt"
        )
        assert new["machine_id"] == "wang-desktop"

        # work_item now points at new attempt
        wi = (await conn.execute(sa.text(
            "SELECT current_attempt_id, status FROM work_items WHERE id = :id"
        ), {"id": wi_id})).mappings().first()
        assert wi["current_attempt_id"] == claim_dt["attempt_id"]
        assert wi["status"] == "running"

        # Resource lock owner_attempt_id transferred from old to new
        lock_rows = (await conn.execute(sa.text("""
            SELECT owner_attempt_id, claim_epoch FROM resource_locks
            WHERE resource_type = 'git_branch'
              AND resource_key = :k
        """), {"k": f"marketplace/polyforge/{wi_id}"})).mappings().all()
        assert len(lock_rows) == 1
        assert lock_rows[0]["owner_attempt_id"] == claim_dt["attempt_id"]
        assert lock_rows[0]["claim_epoch"] == 2

        # 'attempt_taken_over' event emitted alongside 'attempt_started'
        event_types = (await conn.execute(sa.text("""
            SELECT event_type, run_attempt_id FROM agent_events
            WHERE work_item_id = :wid
            ORDER BY created_at ASC, id ASC
        """), {"wid": wi_id})).mappings().all()
        types_seen = [r["event_type"] for r in event_types]
        # Must contain attempt_started from both claims AND attempt_taken_over for the second
        assert types_seen.count("attempt_started") == 2, (
            f"expected 2 attempt_started events (one per claim); types={types_seen}"
        )
        assert "attempt_taken_over" in types_seen, (
            f"must emit attempt_taken_over on takeover; types={types_seen}"
        )

        # The takeover event is attributed to the NEW attempt
        takeover_rows = [r for r in event_types if r["event_type"] == "attempt_taken_over"]
        assert len(takeover_rows) == 1
        assert takeover_rows[0]["run_attempt_id"] == claim_dt["attempt_id"]


async def test_takeover_within_active_lease_blocked(team_adapters, seeded_users, make_adapter):
    """Counter-test: if the old lease is still ALIVE, takeover is rejected.

    This is the warm-race opencode flagged: while machine A still holds a
    live lease, machine B's claim must be rejected with
    CONFLICT_BUSY_NOT_TAKEOVER_ELIGIBLE. Only after the lease expires can
    machine B succeed (test above).
    """
    from polyforge_v3.aihub.errors import AihubError, ErrorCode
    from .conftest import BEARER_WANG

    adapter_mbp, _ = await make_adapter(
        BEARER_WANG, machine_id="wang-mbp", session_secret="1" * 64,
    )
    wi_id = (await adapter_mbp.create_work_item(
        project="marketplace", goal="wang: live-lease block target",
        scenario="coding", declared_resources=[],
    ))["work_item_id"]

    await adapter_mbp.claim_work_item(
        work_item_id=wi_id, idempotency_key=f"alive-mbp-{wi_id}",
        session_info={"machine_id": "wang-mbp"},
        requested_locks=[],
    )

    # Do NOT wait for expiry. Immediately try takeover from desktop.
    adapter_dt, _ = await make_adapter(
        BEARER_WANG, machine_id="wang-desktop", session_secret="2" * 64,
    )

    with pytest.raises(AihubError) as ei:
        await adapter_dt.claim_work_item(
            work_item_id=wi_id, idempotency_key=f"alive-dt-{wi_id}",
            session_info={"machine_id": "wang-desktop"},
            requested_locks=[],
        )
    assert ei.value.code is ErrorCode.CONFLICT_BUSY_NOT_TAKEOVER_ELIGIBLE
