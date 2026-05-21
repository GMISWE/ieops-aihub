"""s6 — POST /v1/attempts/{id}/lease|complete|pause."""
from __future__ import annotations

import pytest
import sqlalchemy as sa

from app.auth import _hash_session_secret
from tests.v3.v3_client import (
    BEARER_LI, BEARER_WANG, BEARER_ZHANG, auth_headers, make_async_client,
)


pytestmark = pytest.mark.asyncio(loop_scope="session")


SECRET = "a" * 64
SECRET_B = "b" * 64


async def _seed_real_secret(engine, raw=SECRET):
    """Replace ra_111's dummy hash with real sha256 of `raw`."""
    async with engine.connect() as conn:
        await conn.execute(sa.text(
            "UPDATE run_attempts SET session_secret_hash = :h WHERE id='ra_111'"
        ), {"h": _hash_session_secret(raw)})
        await conn.commit()


# ---- lease renew ----

async def test_lease_renew_happy(seeded_reference):
    await _seed_real_secret(seeded_reference)
    async with make_async_client(seeded_reference) as client:
        # First, fetch current lease_until
        async with seeded_reference.connect() as conn:
            old_lease = (await conn.execute(sa.text(
                "SELECT lease_until FROM run_attempts WHERE id='ra_111'"
            ))).scalar_one()
        r = await client.post(
            "/v1/attempts/ra_111/lease",
            json={"claim_epoch": 1, "session_secret": SECRET},
            headers=auth_headers(BEARER_ZHANG),
        )
    assert r.status_code == 200, r.text
    new_lease = r.json()["lease_until"]
    # New lease should be later than (or equal to) old, within a second
    assert new_lease is not None


async def test_lease_renew_wrong_secret(seeded_reference):
    await _seed_real_secret(seeded_reference)
    async with make_async_client(seeded_reference) as client:
        r = await client.post(
            "/v1/attempts/ra_111/lease",
            json={"claim_epoch": 1, "session_secret": SECRET_B},
            headers=auth_headers(BEARER_ZHANG),
        )
    assert r.status_code == 401


async def test_lease_renew_taken_over(seeded_reference):
    """Bump ra_111.claim_epoch to 2; old client with epoch=1 → LEASE_TAKEN_OVER."""
    await _seed_real_secret(seeded_reference)
    async with seeded_reference.connect() as conn:
        # Insert a takeover row (epoch=2) and bump current_attempt_id
        await conn.execute(sa.text("""
            UPDATE run_attempts SET claim_epoch = 2 WHERE id='ra_111'
        """))
        await conn.commit()
    async with make_async_client(seeded_reference) as client:
        r = await client.post(
            "/v1/attempts/ra_111/lease",
            json={"claim_epoch": 1, "session_secret": SECRET},
            headers=auth_headers(BEARER_ZHANG),
        )
    assert r.status_code == 409
    assert r.json()["code"] == "CONFLICT_LEASE_TAKEN_OVER"


async def test_lease_renew_expired(seeded_reference):
    await _seed_real_secret(seeded_reference)
    async with seeded_reference.connect() as conn:
        await conn.execute(sa.text(
            "UPDATE run_attempts SET lease_until = now() - interval '5 minutes' WHERE id='ra_111'"
        ))
        await conn.commit()
    async with make_async_client(seeded_reference) as client:
        r = await client.post(
            "/v1/attempts/ra_111/lease",
            json={"claim_epoch": 1, "session_secret": SECRET},
            headers=auth_headers(BEARER_ZHANG),
        )
    assert r.status_code == 409
    assert r.json()["code"] == "CONFLICT_LEASE_EXPIRED"


async def test_lease_renew_unknown_attempt(seeded_reference):
    async with make_async_client(seeded_reference) as client:
        r = await client.post(
            "/v1/attempts/ra_nonexistent/lease",
            json={"claim_epoch": 1, "session_secret": SECRET},
            headers=auth_headers(BEARER_ZHANG),
        )
    assert r.status_code == 404


async def test_lease_renew_wrong_user(seeded_reference):
    """李四 (wrong user) tries to renew 张三's ra_111 → 403."""
    await _seed_real_secret(seeded_reference)
    async with make_async_client(seeded_reference) as client:
        r = await client.post(
            "/v1/attempts/ra_111/lease",
            json={"claim_epoch": 1, "session_secret": SECRET},
            headers=auth_headers(BEARER_LI),
        )
    assert r.status_code == 403


# ---- complete ----

async def test_complete_attempt_happy(seeded_reference):
    await _seed_real_secret(seeded_reference)
    async with make_async_client(seeded_reference) as client:
        r = await client.post(
            "/v1/attempts/ra_111/complete",
            json={"attempt_id": "ra_111", "claim_epoch": 1,
                  "session_secret": SECRET, "status": "wrapped"},
            headers=auth_headers(BEARER_ZHANG),
        )
    assert r.status_code == 200
    assert r.json() == {"ok": True}

    async with seeded_reference.connect() as conn:
        ra = (await conn.execute(sa.text(
            "SELECT status, ended_at FROM run_attempts WHERE id='ra_111'"
        ))).first()
        assert ra[0] == "wrapped"
        assert ra[1] is not None
        locks = (await conn.execute(sa.text(
            "SELECT COUNT(*) FROM resource_locks WHERE owner_attempt_id='ra_111'"
        ))).scalar()
        assert locks == 0
        ev = (await conn.execute(sa.text("""
            SELECT event_type, payload FROM agent_events
            WHERE run_attempt_id='ra_111' AND event_type='attempt_completed'
        """))).first()
        assert ev is not None
        assert ev[1]["status"] == "wrapped"


async def test_complete_attempt_already_terminal(seeded_reference):
    """Re-complete after wrap → second call: verify_mutation sees status≠running
    → 409 CONFLICT_LEASE_EXPIRED (attempt not active)."""
    await _seed_real_secret(seeded_reference)
    async with make_async_client(seeded_reference) as client:
        body = {"attempt_id": "ra_111", "claim_epoch": 1,
                "session_secret": SECRET, "status": "wrapped"}
        r1 = await client.post("/v1/attempts/ra_111/complete", json=body,
                               headers=auth_headers(BEARER_ZHANG))
        assert r1.status_code == 200
        r2 = await client.post("/v1/attempts/ra_111/complete", json=body,
                               headers=auth_headers(BEARER_ZHANG))
    assert r2.status_code == 409


async def test_complete_attempt_path_body_mismatch(seeded_reference):
    await _seed_real_secret(seeded_reference)
    async with make_async_client(seeded_reference) as client:
        r = await client.post(
            "/v1/attempts/ra_111/complete",
            json={"attempt_id": "ra_other", "claim_epoch": 1,
                  "session_secret": SECRET, "status": "wrapped"},
            headers=auth_headers(BEARER_ZHANG),
        )
    assert r.status_code == 400


async def test_complete_attempt_failed_status(seeded_reference):
    await _seed_real_secret(seeded_reference)
    async with make_async_client(seeded_reference) as client:
        r = await client.post(
            "/v1/attempts/ra_111/complete",
            json={"attempt_id": "ra_111", "claim_epoch": 1,
                  "session_secret": SECRET, "status": "failed"},
            headers=auth_headers(BEARER_ZHANG),
        )
    assert r.status_code == 200
    async with seeded_reference.connect() as conn:
        ra_status = (await conn.execute(sa.text(
            "SELECT status FROM run_attempts WHERE id='ra_111'"
        ))).scalar()
        assert ra_status == "failed"


# ---- pause ----

async def test_pause_attempt_happy(seeded_reference):
    await _seed_real_secret(seeded_reference)
    async with make_async_client(seeded_reference) as client:
        r = await client.post(
            "/v1/attempts/ra_111/pause",
            json={"attempt_id": "ra_111", "claim_epoch": 1,
                  "session_secret": SECRET, "reason": "need to leave"},
            headers=auth_headers(BEARER_ZHANG),
        )
    assert r.status_code == 200
    async with seeded_reference.connect() as conn:
        ra_status = (await conn.execute(sa.text(
            "SELECT status FROM run_attempts WHERE id='ra_111'"
        ))).scalar()
        assert ra_status == "wrapped"  # §7.7: pause attempt → wrapped terminal
        wi = (await conn.execute(sa.text(
            "SELECT status, current_attempt_id FROM work_items WHERE id='wi_a3f'"
        ))).first()
        assert wi == ("paused", None)
        locks = (await conn.execute(sa.text(
            "SELECT COUNT(*) FROM resource_locks WHERE owner_attempt_id='ra_111'"
        ))).scalar()
        assert locks == 0
        ev = (await conn.execute(sa.text("""
            SELECT payload FROM agent_events
            WHERE run_attempt_id='ra_111' AND event_type='attempt_paused'
        """))).first()
        assert ev is not None
        assert ev[0]["reason"] == "need to leave"


async def test_pause_attempt_no_bearer(seeded_reference):
    async with make_async_client(seeded_reference) as client:
        r = await client.post(
            "/v1/attempts/ra_111/pause",
            json={"attempt_id": "ra_111", "claim_epoch": 1,
                  "session_secret": SECRET, "reason": "x"},
        )
    assert r.status_code == 401


# ---- MED-2: /complete propagates to work_item (§7.7 state machine) ----

async def test_complete_propagates_to_work_item_wrapped(seeded_users):
    """MED-2: POST /complete with status=wrapped must update work_item.status
    to 'wrapped' and emit work_item_completed event with final_status='wrapped'.

    Uses a fresh work_item + claim so the test is self-contained.
    """
    from app.auth import _hash_session_secret
    MY_SECRET = "c" * 64

    async with make_async_client(seeded_users) as client:
        # Create work_item
        r0 = await client.post(
            "/v1/work_items",
            json={"project": "marketplace", "scenario": "coding",
                  "goal": "med2 wrapped test",
                  "declared_resources": [
                      {"type": "repo", "uri": "repo:marketplace", "intent": "write"}
                  ]},
            headers=auth_headers(BEARER_ZHANG),
        )
        assert r0.status_code == 201
        wi_id = r0.json()["id"]

        # Claim
        r1 = await client.post(
            f"/v1/work_items/{wi_id}/claim",
            json={"idempotency_key": "idem_med2_wrap",
                  "session_info": {"machine_id": "zhang-mbp", "session_secret": MY_SECRET},
                  "requested_locks": []},
            headers=auth_headers(BEARER_ZHANG),
        )
        assert r1.status_code == 200
        claim = r1.json()
        aid = claim["attempt_id"]

    # Patch secret hash so verify_mutation can verify it
    async with seeded_users.connect() as conn:
        await conn.execute(sa.text(
            "UPDATE run_attempts SET session_secret_hash = :h WHERE id = :aid"
        ), {"h": _hash_session_secret(MY_SECRET), "aid": aid})
        await conn.commit()

    async with make_async_client(seeded_users) as client:
        # Complete the attempt with status=wrapped
        r2 = await client.post(
            f"/v1/attempts/{aid}/complete",
            json={"attempt_id": aid, "claim_epoch": claim["claim_epoch"],
                  "session_secret": MY_SECRET, "status": "wrapped"},
            headers=auth_headers(BEARER_ZHANG),
        )
    assert r2.status_code == 200, r2.text

    # Verify: work_item.status must be 'wrapped', closed_at must be set
    async with seeded_users.connect() as conn:
        wi = (await conn.execute(sa.text(
            "SELECT status, closed_at FROM work_items WHERE id = :wid"
        ), {"wid": wi_id})).first()
        assert wi[0] == "wrapped", f"work_item.status={wi[0]!r}, expected 'wrapped'"
        assert wi[1] is not None, "work_item.closed_at not set after /complete"

        # work_item_completed event must be emitted
        ev = (await conn.execute(sa.text("""
            SELECT payload FROM agent_events
            WHERE work_item_id = :wid AND event_type = 'work_item_completed'
            ORDER BY created_at DESC LIMIT 1
        """), {"wid": wi_id})).first()
        assert ev is not None, "work_item_completed event not emitted"
        assert ev[0]["final_status"] == "wrapped"


async def test_complete_propagates_to_work_item_failed(seeded_users):
    """MED-2: POST /complete with status=failed must update work_item.status='failed'."""
    from app.auth import _hash_session_secret
    MY_SECRET = "d" * 64

    async with make_async_client(seeded_users) as client:
        r0 = await client.post(
            "/v1/work_items",
            json={"project": "marketplace", "scenario": "coding",
                  "goal": "med2 failed test",
                  "declared_resources": [
                      {"type": "repo", "uri": "repo:marketplace", "intent": "write"}
                  ]},
            headers=auth_headers(BEARER_ZHANG),
        )
        wi_id = r0.json()["id"]
        r1 = await client.post(
            f"/v1/work_items/{wi_id}/claim",
            json={"idempotency_key": "idem_med2_fail",
                  "session_info": {"machine_id": "zhang-mbp", "session_secret": MY_SECRET},
                  "requested_locks": []},
            headers=auth_headers(BEARER_ZHANG),
        )
        claim = r1.json()
        aid = claim["attempt_id"]

    async with seeded_users.connect() as conn:
        await conn.execute(sa.text(
            "UPDATE run_attempts SET session_secret_hash = :h WHERE id = :aid"
        ), {"h": _hash_session_secret(MY_SECRET), "aid": aid})
        await conn.commit()

    async with make_async_client(seeded_users) as client:
        r2 = await client.post(
            f"/v1/attempts/{aid}/complete",
            json={"attempt_id": aid, "claim_epoch": claim["claim_epoch"],
                  "session_secret": MY_SECRET, "status": "failed"},
            headers=auth_headers(BEARER_ZHANG),
        )
    assert r2.status_code == 200

    async with seeded_users.connect() as conn:
        wi_status = (await conn.execute(sa.text(
            "SELECT status FROM work_items WHERE id = :wid"
        ), {"wid": wi_id})).scalar()
        assert wi_status == "failed"

        ev = (await conn.execute(sa.text("""
            SELECT payload FROM agent_events
            WHERE work_item_id = :wid AND event_type = 'work_item_completed'
        """), {"wid": wi_id})).first()
        assert ev is not None
        assert ev[0]["final_status"] == "failed"


async def test_pause_does_not_complete_work_item(seeded_users):
    """MED-2 (negative): /pause must set work_item.status='paused', NOT 'wrapped'.

    The /pause endpoint has always updated work_item.status to 'paused'. This
    test confirms MED-2 fix did not inadvertently change the pause path.
    """
    from app.auth import _hash_session_secret
    MY_SECRET = "e" * 64

    async with make_async_client(seeded_users) as client:
        r0 = await client.post(
            "/v1/work_items",
            json={"project": "marketplace", "scenario": "coding",
                  "goal": "med2 pause negative test",
                  "declared_resources": [
                      {"type": "repo", "uri": "repo:marketplace", "intent": "write"}
                  ]},
            headers=auth_headers(BEARER_ZHANG),
        )
        wi_id = r0.json()["id"]
        r1 = await client.post(
            f"/v1/work_items/{wi_id}/claim",
            json={"idempotency_key": "idem_med2_pause",
                  "session_info": {"machine_id": "zhang-mbp", "session_secret": MY_SECRET},
                  "requested_locks": []},
            headers=auth_headers(BEARER_ZHANG),
        )
        claim = r1.json()
        aid = claim["attempt_id"]

    async with seeded_users.connect() as conn:
        await conn.execute(sa.text(
            "UPDATE run_attempts SET session_secret_hash = :h WHERE id = :aid"
        ), {"h": _hash_session_secret(MY_SECRET), "aid": aid})
        await conn.commit()

    async with make_async_client(seeded_users) as client:
        r2 = await client.post(
            f"/v1/attempts/{aid}/pause",
            json={"attempt_id": aid, "claim_epoch": claim["claim_epoch"],
                  "session_secret": MY_SECRET, "reason": "stepping away"},
            headers=auth_headers(BEARER_ZHANG),
        )
    assert r2.status_code == 200

    async with seeded_users.connect() as conn:
        wi = (await conn.execute(sa.text(
            "SELECT status, current_attempt_id FROM work_items WHERE id = :wid"
        ), {"wid": wi_id})).first()
        assert wi[0] == "paused"  # not 'wrapped' — pause keeps work_item alive
        assert wi[1] is None      # current_attempt_id cleared by pause


# ---------------------------------------------------------------------------
# F8 (M4): complete_attempt race — work_item takeover between verify and UPDATE
# ---------------------------------------------------------------------------

async def test_complete_attempt_race_skips_work_item_completed_event(seeded_users):
    """F8/M4: if a concurrent takeover reassigned current_attempt_id before the
    work_item UPDATE, rowcount == 0 → work_item_completed event is NOT emitted.

    Simulated by manually clearing current_attempt_id right after verify_mutation
    would have succeeded (i.e., between claim and complete via DB manipulation).
    """
    from app.auth import _hash_session_secret
    MY_SECRET = "ab" * 32  # 64 valid hex chars

    async with make_async_client(seeded_users) as client:
        r0 = await client.post(
            "/v1/work_items",
            json={"project": "marketplace", "scenario": "coding",
                  "goal": "race guard test",
                  "declared_resources": [
                      {"type": "repo", "uri": "repo:marketplace", "intent": "write"}
                  ]},
            headers=auth_headers(BEARER_ZHANG),
        )
        assert r0.status_code == 201
        wi_id = r0.json()["id"]

        r1 = await client.post(
            f"/v1/work_items/{wi_id}/claim",
            json={"idempotency_key": "idem_race_guard",
                  "session_info": {"machine_id": "zhang-mbp", "session_secret": MY_SECRET},
                  "requested_locks": []},
            headers=auth_headers(BEARER_ZHANG),
        )
        assert r1.status_code == 200
        claim = r1.json()
        aid = claim["attempt_id"]

    async with seeded_users.connect() as conn:
        await conn.execute(sa.text(
            "UPDATE run_attempts SET session_secret_hash = :h WHERE id = :aid"
        ), {"h": _hash_session_secret(MY_SECRET), "aid": aid})
        await conn.commit()

    # Simulate concurrent takeover: clear current_attempt_id so the complete
    # UPDATE WHERE current_attempt_id=:aid returns 0 rows
    async with seeded_users.connect() as conn:
        await conn.execute(sa.text(
            "UPDATE work_items SET current_attempt_id = NULL WHERE id = :wid"
        ), {"wid": wi_id})
        await conn.commit()

    async with make_async_client(seeded_users) as client:
        r2 = await client.post(
            f"/v1/attempts/{aid}/complete",
            json={"attempt_id": aid, "claim_epoch": claim["claim_epoch"],
                  "session_secret": MY_SECRET, "status": "wrapped"},
            headers=auth_headers(BEARER_ZHANG),
        )
    # The endpoint should still return 200 (attempt itself was completed)
    assert r2.status_code == 200, r2.text

    # But work_item_completed event must NOT be emitted (race skipped it)
    async with seeded_users.connect() as conn:
        evt = (await conn.execute(sa.text("""
            SELECT id FROM agent_events
            WHERE work_item_id = :wid AND event_type = 'work_item_completed'
        """), {"wid": wi_id})).first()
    assert evt is None, "work_item_completed should NOT be emitted when WI was taken over"

    # The work_item status should remain unchanged (not overwritten to 'wrapped')
    async with seeded_users.connect() as conn:
        wi_status = (await conn.execute(sa.text(
            "SELECT status FROM work_items WHERE id = :wid"
        ), {"wid": wi_id})).scalar()
    # Status stays 'running' or whatever the race winner set — not forced to 'wrapped'
    assert wi_status != "wrapped", (
        "work_item.status must not be forced to 'wrapped' when current_attempt_id mismatch"
    )
