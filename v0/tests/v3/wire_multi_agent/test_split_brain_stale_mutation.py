"""P1.5 — takeover-vs-stale-mutation (split-brain rejection).

THE highest-value gap per opencode review. Without this test, the
attempt-fencing mechanism could silently accept stale writes from a
dead-but-not-yet-aware client. The whole multi-agent architecture relies
on this fence.

Scenario:
  1. Zhang claims wi (epoch=1), captures cred_A.
  2. Zhang's network drops; lease expires (no renew).
  3. Wang takes over → epoch=2.
  4. Zhang's machine, unaware of the takeover, fires a delayed
     `emit_event` using his STALE cred_A.
  5. Server MUST reject zhang's late emit. The agent_events table must
     show ONLY wang's writes; zhang's payload must NEVER land.

Per `app/auth.py:184-198` verify_mutation contract, when zhang's attempt
has status='superseded' (set by takeover at `run_attempts.py:200-205`),
the late mutation hits the "attempt not active" branch and is rejected
with `CONFLICT_LEASE_EXPIRED`.

This is the canonical split-brain prevention test. If it ever flakes or
fails, the attempt-fencing contract is broken and the whole multi-agent
design is unsafe.
"""
from __future__ import annotations

import asyncio
import json

import pytest
import sqlalchemy as sa

from polyforge_v3.aihub.errors import AihubError, ErrorCode


from .conftest import LEASE_EXPIRY_WAIT_S


pytestmark = pytest.mark.asyncio(loop_scope="session")


async def test_stale_attempt_emit_rejected_after_takeover(
    seeded_users, make_adapter
):
    """A claims epoch=1; B takes over to epoch=2; A's stale emit is rejected."""
    from .conftest import BEARER_ZHANG, BEARER_LI

    # 1. Zhang claims with cred_A
    adapter_a, _ = await make_adapter(
        BEARER_ZHANG, machine_id="zhang-mbp", session_secret="a" * 64,
    )
    wi_id = (await adapter_a.create_work_item(
        project="marketplace", goal="P1.5 split-brain target",
        scenario="coding", declared_resources=[],
    ))["work_item_id"]

    claim_a = await adapter_a.claim_work_item(
        work_item_id=wi_id, idempotency_key=f"a-{wi_id}",
        session_info={"machine_id": "zhang-mbp"},
        requested_locks=[],
    )
    attempt_a = claim_a["attempt_id"]
    epoch_a = claim_a["claim_epoch"]
    secret_a = claim_a["session_secret"]
    assert epoch_a == 1

    # Sanity: zhang can emit while his lease is alive
    await adapter_a.emit_event(
        work_item_id=wi_id, event_type="note",
        payload={"who": "zhang", "moment": "pre-expiry"},
        attempt_id=attempt_a, claim_epoch=epoch_a,
        session_secret=secret_a,
    )

    # 2. Wifi drop — wait for lease to expire
    await asyncio.sleep(LEASE_EXPIRY_WAIT_S)

    # 3. Li takes over (different bearer = different user).
    # Cross-user takeover IS allowed per app/run_attempts.py:113-118: the
    # only gates on a claimer are (a) project membership and (b) writer or
    # admin role. There's NO same-user constraint on takeover. Once Zhang's
    # lease expired (Step 3 busy-check at run_attempts.py:137 stops blocking),
    # any project-writer can claim.
    adapter_b, _ = await make_adapter(
        BEARER_LI, machine_id="li-mbp", session_secret="b" * 64,
    )
    claim_b = await adapter_b.claim_work_item(
        work_item_id=wi_id, idempotency_key=f"b-{wi_id}",
        session_info={"machine_id": "li-mbp"},
        requested_locks=[],
    )
    attempt_b = claim_b["attempt_id"]
    epoch_b = claim_b["claim_epoch"]
    assert epoch_b == 2

    # Li emits an event under the new attempt (this should persist)
    await adapter_b.emit_event(
        work_item_id=wi_id, event_type="note",
        payload={"who": "li", "moment": "post-takeover"},
        attempt_id=attempt_b, claim_epoch=epoch_b,
        session_secret=claim_b["session_secret"],
    )

    # 4. Zhang's stale machine fires its delayed emit with STALE cred_A
    with pytest.raises(AihubError) as ei:
        await adapter_a.emit_event(
            work_item_id=wi_id, event_type="note",
            payload={"who": "zhang", "moment": "STALE-must-be-rejected"},
            attempt_id=attempt_a, claim_epoch=epoch_a,
            session_secret=secret_a,
        )
    # Per app/auth.py:184: superseded attempt -> CONFLICT_LEASE_EXPIRED
    assert ei.value.code is ErrorCode.CONFLICT_LEASE_EXPIRED, (
        f"stale emit must be rejected with CONFLICT_LEASE_EXPIRED; "
        f"got {ei.value.code} ({ei.value.message!r})"
    )

    # 5. DB invariant: zhang's STALE payload must NOT have landed
    async with seeded_users.connect() as conn:
        rows = (await conn.execute(sa.text("""
            SELECT payload, run_attempt_id FROM agent_events
            WHERE work_item_id = :wid AND event_type = 'note'
            ORDER BY created_at ASC, id ASC
        """), {"wid": wi_id})).mappings().all()

        # Expected: zhang's PRE-expiry emit (epoch=1) + li's POST-takeover emit (epoch=2)
        # Stale emit must NOT appear.
        payloads = [
            json.loads(r["payload"]) if isinstance(r["payload"], str) else r["payload"]
            for r in rows
        ]
        moments = [p.get("moment") for p in payloads]
        assert "STALE-must-be-rejected" not in moments, (
            f"stale emit leaked into agent_events: moments={moments}"
        )
        assert "pre-expiry" in moments
        assert "post-takeover" in moments


async def test_stale_emit_does_not_break_active_attempt(seeded_users, make_adapter):
    """A second stale-emit attempt mustn't accidentally invalidate the live
    attempt. The fence rejection is read-only — server state unchanged.
    """
    from .conftest import BEARER_ZHANG, BEARER_LI

    adapter_a, _ = await make_adapter(
        BEARER_ZHANG, machine_id="zhang-mbp", session_secret="a" * 64,
    )
    wi_id = (await adapter_a.create_work_item(
        project="marketplace", goal="P1.5 fence-no-side-effect target",
        scenario="coding", declared_resources=[],
    ))["work_item_id"]
    claim_a = await adapter_a.claim_work_item(
        work_item_id=wi_id, idempotency_key=f"se-a-{wi_id}",
        session_info={"machine_id": "zhang-mbp"}, requested_locks=[],
    )

    await asyncio.sleep(LEASE_EXPIRY_WAIT_S)

    adapter_b, _ = await make_adapter(
        BEARER_LI, machine_id="li-mbp", session_secret="b" * 64,
    )
    claim_b = await adapter_b.claim_work_item(
        work_item_id=wi_id, idempotency_key=f"se-b-{wi_id}",
        session_info={"machine_id": "li-mbp"}, requested_locks=[],
    )

    # Fire 5 stale emits in a row — none should land, B's attempt must survive
    for i in range(5):
        with pytest.raises(AihubError):
            await adapter_a.emit_event(
                work_item_id=wi_id, event_type="note",
                payload={"stale_iter": i},
                attempt_id=claim_a["attempt_id"], claim_epoch=claim_a["claim_epoch"],
                session_secret=claim_a["session_secret"],
            )

    # B can still emit — no side effects from the rejections
    await adapter_b.emit_event(
        work_item_id=wi_id, event_type="note",
        payload={"who": "li", "after_stale_storm": True},
        attempt_id=claim_b["attempt_id"], claim_epoch=claim_b["claim_epoch"],
        session_secret=claim_b["session_secret"],
    )

    async with seeded_users.connect() as conn:
        # B's emit landed; zero stale_iter payloads
        rows = (await conn.execute(sa.text("""
            SELECT payload FROM agent_events
            WHERE work_item_id = :wid AND event_type = 'note'
        """), {"wid": wi_id})).scalars().all()
        payloads = [json.loads(p) if isinstance(p, str) else p for p in rows]
        assert any(p.get("after_stale_storm") for p in payloads)
        assert not any("stale_iter" in p for p in payloads), (
            f"no stale payloads should land; got {payloads}"
        )
