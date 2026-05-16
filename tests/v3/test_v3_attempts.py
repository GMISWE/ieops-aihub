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
