"""Ownership-only lease mode tests (wi_01ks2861rzc2h9a8p22zq090e5).

T1: Normal ownership claim → lease_until=null, last_active_at set
T2: force_takeover by non-admin → 403 FORBIDDEN
T3: force_takeover by admin → 200, prior attempt superseded, force_takeover event
T4: Ownership-mode lock blocks concurrent claim (CONFLICT_HARD_BLOCK)
T5: GC skips ownership rows (lease_until IS NULL)
T6: Legacy expired attempt (non-NULL lease_until) can be superseded as before
T7: renew_lease on ownership attempt → no-op returning null, updates last_active_at
"""
from __future__ import annotations

import hashlib
import json

import pytest
import sqlalchemy as sa

from app.auth import _hash_session_secret
from app.gc import gc_tick
from tests.v3.v3_client import (
    BEARER_LI, BEARER_ZHANG, auth_headers, make_async_client,
)


pytestmark = pytest.mark.asyncio(loop_scope="session")


SECRET = "a" * 64
SECRET_B = "b" * 64

# Admin bearer — derive sha256$ hash from a known raw key
_ADMIN_RAW = "admin_ownership_test_key"
BEARER_ADMIN = "sha256$" + hashlib.sha256(_ADMIN_RAW.encode()).hexdigest()


def _claim_body(
    idempotency_key="idem_own",
    session_secret=SECRET,
    machine_id="test-host",
    locks=None,
    force_takeover=False,
):
    return {
        "idempotency_key": idempotency_key,
        "session_info": {"machine_id": machine_id, "session_secret": session_secret},
        "requested_locks": locks or [],
        "force_takeover": force_takeover,
    }


def _new_wi(project="marketplace", goal="ownership-test"):
    return {
        "project": project, "scenario": "coding", "goal": goal,
        "declared_resources": [], "labels": [], "priority": "normal", "source": "human",
    }


async def _seed_admin_key(engine):
    """Give u_admin a usable api_key and marketplace project membership."""
    entry = {
        "id": "ak_admin_own_test",
        "key_hash": BEARER_ADMIN,
        "scopes": ["admin"],
        "created_at": "2026-05-16T09:00:00Z",
        "revoked_at": None,
    }
    async with engine.connect() as conn:
        await conn.execute(sa.text("""
            UPDATE users
            SET api_keys = CAST(:keys AS JSONB),
                projects = '["marketplace", "aihub"]'::jsonb
            WHERE id = 'u_admin'
        """), {"keys": json.dumps([entry])})
        await conn.commit()


# ---------------------------------------------------------------------------
# T1 — normal claim returns lease_until=null, last_active_at is set
# ---------------------------------------------------------------------------

async def test_ownership_claim_returns_null_lease(seeded_users):
    async with make_async_client(seeded_users) as client:
        r0 = await client.post("/v1/work_items", json=_new_wi(),
                               headers=auth_headers(BEARER_ZHANG))
        assert r0.status_code == 200
        wi_id = r0.json()["id"]
        r1 = await client.post(f"/v1/work_items/{wi_id}/claim",
                               json=_claim_body(), headers=auth_headers(BEARER_ZHANG))
    assert r1.status_code == 200, r1.text
    body = r1.json()
    assert body["lease_until"] is None
    assert body["attempt_id"].startswith("ra_")
    async with seeded_users.connect() as conn:
        ra = (await conn.execute(sa.text("""
            SELECT lease_until, last_active_at FROM run_attempts WHERE id = :aid
        """), {"aid": body["attempt_id"]})).mappings().first()
    assert ra["lease_until"] is None
    assert ra["last_active_at"] is not None


# ---------------------------------------------------------------------------
# T2 — force_takeover by non-admin writer → 403 FORBIDDEN
# ---------------------------------------------------------------------------

async def test_force_takeover_non_admin_forbidden(seeded_users):
    async with make_async_client(seeded_users) as client:
        r0 = await client.post("/v1/work_items", json=_new_wi(goal="ft-non-admin"),
                               headers=auth_headers(BEARER_ZHANG))
        wi_id = r0.json()["id"]
        await client.post(f"/v1/work_items/{wi_id}/claim",
                          json=_claim_body(idempotency_key="owner"),
                          headers=auth_headers(BEARER_ZHANG))
        # Li is a writer, not admin — force_takeover must be rejected
        r2 = await client.post(
            f"/v1/work_items/{wi_id}/claim",
            json=_claim_body(idempotency_key="takeover", session_secret=SECRET_B,
                             force_takeover=True),
            headers=auth_headers(BEARER_LI),
        )
    assert r2.status_code == 403, r2.text
    assert r2.json()["code"] == "FORBIDDEN"


# ---------------------------------------------------------------------------
# T3 — force_takeover by admin → 200, prior attempt superseded, audit event emitted
# ---------------------------------------------------------------------------

async def test_force_takeover_admin_succeeds(seeded_users):
    await _seed_admin_key(seeded_users)
    async with make_async_client(seeded_users) as client:
        r0 = await client.post("/v1/work_items", json=_new_wi(goal="ft-admin"),
                               headers=auth_headers(BEARER_ZHANG))
        wi_id = r0.json()["id"]
        r1 = await client.post(f"/v1/work_items/{wi_id}/claim",
                               json=_claim_body(idempotency_key="owner"),
                               headers=auth_headers(BEARER_ZHANG))
        prior_aid = r1.json()["attempt_id"]
        r2 = await client.post(
            f"/v1/work_items/{wi_id}/claim",
            json=_claim_body(idempotency_key="admin_takeover", session_secret=SECRET_B,
                             force_takeover=True),
            headers=auth_headers(BEARER_ADMIN),
        )
    assert r2.status_code == 200, r2.text
    assert r2.json()["lease_until"] is None
    new_aid = r2.json()["attempt_id"]
    async with seeded_users.connect() as conn:
        prior_status = (await conn.execute(sa.text(
            "SELECT status FROM run_attempts WHERE id = :aid"
        ), {"aid": prior_aid})).scalar()
        assert prior_status == "superseded"
        event_types = [r[0] for r in (await conn.execute(sa.text("""
            SELECT event_type FROM agent_events
            WHERE work_item_id = :wid AND run_attempt_id = :aid
        """), {"wid": wi_id, "aid": new_aid}))]
    assert "force_takeover" in event_types
    assert "attempt_taken_over" in event_types


# ---------------------------------------------------------------------------
# T4 — ownership-mode lock is visible to concurrent claim conflict detection
# ---------------------------------------------------------------------------

async def test_ownership_lock_visible_to_conflict_detection(seeded_users):
    lock_key = "marketplace/ownership_lock_test_branch"
    async with make_async_client(seeded_users) as client:
        r_a = await client.post("/v1/work_items", json=_new_wi(goal="lock-holder"),
                                headers=auth_headers(BEARER_ZHANG))
        r_b = await client.post("/v1/work_items", json=_new_wi(goal="lock-waiter"),
                                headers=auth_headers(BEARER_LI))
        wi_a, wi_b = r_a.json()["id"], r_b.json()["id"]
        # Zhang claims wi_A with a lock (ownership mode — lease_until=NULL)
        await client.post(
            f"/v1/work_items/{wi_a}/claim",
            json=_claim_body(idempotency_key="lock_holder",
                             locks=[{"resource_type": "git_branch", "resource_key": lock_key}]),
            headers=auth_headers(BEARER_ZHANG),
        )
        # Li tries to claim wi_B requesting the same lock → hard_block
        r2 = await client.post(
            f"/v1/work_items/{wi_b}/claim",
            json=_claim_body(idempotency_key="lock_waiter", session_secret=SECRET_B,
                             locks=[{"resource_type": "git_branch", "resource_key": lock_key}]),
            headers=auth_headers(BEARER_LI),
        )
    assert r2.status_code == 409, r2.text
    assert r2.json()["code"] == "CONFLICT_HARD_BLOCK"


# ---------------------------------------------------------------------------
# T5 — GC does not expire ownership rows (lease_until IS NULL)
# ---------------------------------------------------------------------------

async def test_gc_skips_null_lease_until(seeded_users):
    async with make_async_client(seeded_users) as client:
        r0 = await client.post("/v1/work_items", json=_new_wi(goal="gc-skip-test"),
                               headers=auth_headers(BEARER_ZHANG))
        wi_id = r0.json()["id"]
        r1 = await client.post(f"/v1/work_items/{wi_id}/claim",
                               json=_claim_body(idempotency_key="gc_skip"),
                               headers=auth_headers(BEARER_ZHANG))
        aid = r1.json()["attempt_id"]
    await gc_tick(seeded_users)
    async with seeded_users.connect() as conn:
        status = (await conn.execute(sa.text(
            "SELECT status FROM run_attempts WHERE id = :aid"
        ), {"aid": aid})).scalar()
    assert status == "running", (
        f"GC must skip NULL lease_until rows; got status={status!r}"
    )


# ---------------------------------------------------------------------------
# T6 — legacy expired attempt (non-NULL, past) is still superseded on new claim
# ---------------------------------------------------------------------------

async def test_legacy_expired_attempt_takeover(seeded_users):
    async with seeded_users.connect() as conn:
        await conn.execute(sa.text("""
            INSERT INTO work_items (id, project, scenario, goal, status, priority,
                                    labels, reporter_user_id, declared_resources,
                                    resources_version, metadata, current_attempt_id)
            VALUES ('wi_legacy_own', 'marketplace', 'coding', 'legacy takeover test',
                    'running', 'normal', '[]'::jsonb, 'u_zhangsan',
                    '[]'::jsonb, 0, '{}'::jsonb, 'ra_legacy_own')
        """))
        await conn.execute(sa.text("""
            INSERT INTO run_attempts (
                id, work_item_id, status, claim_epoch, idempotency_key,
                lease_until, actor_user_id, api_key_id, actor_display,
                machine_id, session_secret_hash
            ) VALUES (
                'ra_legacy_own', 'wi_legacy_own', 'running', 1, 'idem_legacy_own',
                now() - interval '10 minutes',
                'u_zhangsan', 'ak_zhang_001', '张三', 'old-host',
                :hash
            )
        """), {"hash": _hash_session_secret("x" * 64)})
        await conn.commit()
    async with make_async_client(seeded_users) as client:
        r = await client.post(
            "/v1/work_items/wi_legacy_own/claim",
            json=_claim_body(idempotency_key="new_ownership_claim"),
            headers=auth_headers(BEARER_ZHANG),
        )
    assert r.status_code == 200, r.text
    assert r.json()["lease_until"] is None
    async with seeded_users.connect() as conn:
        old_status = (await conn.execute(sa.text(
            "SELECT status FROM run_attempts WHERE id='ra_legacy_own'"
        ))).scalar()
    assert old_status == "superseded"


# ---------------------------------------------------------------------------
# T7 — renew_lease on ownership attempt → no-op, returns null, updates last_active_at
# ---------------------------------------------------------------------------

async def test_renew_lease_noop_updates_last_active_at(seeded_users):
    async with make_async_client(seeded_users) as client:
        r0 = await client.post("/v1/work_items", json=_new_wi(goal="renew-noop"),
                               headers=auth_headers(BEARER_ZHANG))
        wi_id = r0.json()["id"]
        r1 = await client.post(f"/v1/work_items/{wi_id}/claim",
                               json=_claim_body(idempotency_key="renew_noop"),
                               headers=auth_headers(BEARER_ZHANG))
        aid = r1.json()["attempt_id"]
        r2 = await client.post(
            f"/v1/attempts/{aid}/lease",
            json={"claim_epoch": 1, "session_secret": SECRET},
            headers=auth_headers(BEARER_ZHANG),
        )
    assert r2.status_code == 200, r2.text
    assert r2.json()["lease_until"] is None
    async with seeded_users.connect() as conn:
        ra = (await conn.execute(sa.text("""
            SELECT lease_until, last_active_at FROM run_attempts WHERE id = :aid
        """), {"aid": aid})).mappings().first()
    assert ra["lease_until"] is None
    assert ra["last_active_at"] is not None
