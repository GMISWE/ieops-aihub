"""Tests for POST /v1/admin/users|keys|keys/revoke (admin_add_user,
admin_create_key, admin_revoke_key).

NOTE: v3_app.py is not modified per orchestrator instructions — we build the
app and attach the admin router locally so tests remain self-contained.
"""
from __future__ import annotations

import json
from contextlib import asynccontextmanager

import httpx
import pytest
import sqlalchemy as sa

from app.v3_app import make_v3_app
from routes.v3_admin import router as admin_router
from tests.v3.v3_client import (
    BEARER_LI,
    BEARER_ZHANG,
    auth_headers,
)


pytestmark = pytest.mark.asyncio(loop_scope="session")

# Admin bearer — u_admin has no api_key row in the base fixture; we add one.
# The raw key is what the HTTP client presents; BEARER_ADMIN is stored as sha256$ hash.
ADMIN_RAW_KEY = "admin_raw_key_x"
BEARER_ADMIN = "sha256$" + __import__("hashlib").sha256(ADMIN_RAW_KEY.encode()).hexdigest()


@asynccontextmanager
async def make_admin_client(engine):
    """Like make_async_client but also mounts the admin router."""
    app = make_v3_app(engine_factory=lambda: engine)
    app.include_router(admin_router, prefix="/v1")
    transport = httpx.ASGITransport(app=app)
    async with httpx.AsyncClient(transport=transport, base_url="http://test") as client:
        async with app.router.lifespan_context(app):
            yield client


async def _seed_admin_key(engine):
    """Give u_admin a real api_key so Bearer auth works."""
    key_hash = BEARER_ADMIN  # stored hash == bearer for sha256$ prefix support
    entry = {
        "id": "ak_admin_test",
        "key_hash": key_hash,
        "scopes": ["admin"],
        "created_at": "2026-05-16T09:00:00Z",
        "revoked_at": None,
    }
    async with engine.connect() as conn:
        await conn.execute(sa.text("""
            UPDATE users SET api_keys = CAST(:keys AS JSONB) WHERE id = 'u_admin'
        """), {"keys": json.dumps([entry])})
        await conn.commit()


# ---------------------------------------------------------------------------
# 1. admin_add_user — happy path
# ---------------------------------------------------------------------------

async def test_admin_add_user_happy(seeded_users):
    await _seed_admin_key(seeded_users)
    async with make_admin_client(seeded_users) as client:
        r = await client.post(
            "/v1/admin/users",
            json={
                "email": "newuser@gmi.local",
                "display_name": "New User",
                "role": "reader",
                "projects": ["marketplace"],
            },
            headers=auth_headers(ADMIN_RAW_KEY),
        )
    assert r.status_code == 201, r.text
    body = r.json()
    assert "user_id" in body
    assert body["user_id"].startswith("u_")
    assert "api_key" in body
    assert len(body["api_key"]) == 64  # secrets.token_hex(32) = 64 hex chars

    # Verify DB row
    async with seeded_users.connect() as conn:
        row = (await conn.execute(sa.text(
            "SELECT id, role, projects, api_keys FROM users WHERE id = :uid"
        ), {"uid": body["user_id"]})).mappings().first()
    assert row is not None
    assert row["role"] == "reader"
    assert "marketplace" in list(row["projects"])
    api_keys = list(row["api_keys"])
    assert len(api_keys) == 1
    assert api_keys[0]["revoked_at"] is None  # INVARIANT


# ---------------------------------------------------------------------------
# 2. admin_add_user — 403 for non-admin caller
# ---------------------------------------------------------------------------

async def test_admin_add_user_forbidden(seeded_users):
    async with make_admin_client(seeded_users) as client:
        r = await client.post(
            "/v1/admin/users",
            json={
                "email": "hacker@gmi.local",
                "display_name": "Hacker",
                "role": "reader",
                "projects": [],
            },
            headers=auth_headers(BEARER_ZHANG),  # zhang is 'writer', not 'admin'
        )
    assert r.status_code == 403
    assert r.json()["code"] == "FORBIDDEN"


# ---------------------------------------------------------------------------
# 3. admin_create_key — happy path
# ---------------------------------------------------------------------------

async def test_admin_create_key_happy(seeded_users):
    await _seed_admin_key(seeded_users)
    # Record current key count for u_lisi
    async with seeded_users.connect() as conn:
        before = (await conn.execute(sa.text(
            "SELECT api_keys FROM users WHERE id = 'u_lisi'"
        ))).scalar_one()
    count_before = len(list(before))

    async with make_admin_client(seeded_users) as client:
        r = await client.post(
            "/v1/admin/keys",
            json={"user_id": "u_lisi", "scopes": ["read:marketplace"]},
            headers=auth_headers(ADMIN_RAW_KEY),
        )
    assert r.status_code == 201, r.text
    body = r.json()
    assert "key_id" in body
    assert body["key_id"].startswith("ak_")
    assert "api_key" in body

    async with seeded_users.connect() as conn:
        after = (await conn.execute(sa.text(
            "SELECT api_keys FROM users WHERE id = 'u_lisi'"
        ))).scalar_one()
    api_keys_after = list(after)
    assert len(api_keys_after) == count_before + 1
    new_key = next(k for k in api_keys_after if k["id"] == body["key_id"])
    assert new_key["revoked_at"] is None  # INVARIANT


# ---------------------------------------------------------------------------
# 4. admin_create_key — 404 for unknown user_id
# ---------------------------------------------------------------------------

async def test_admin_create_key_user_not_found(seeded_users):
    await _seed_admin_key(seeded_users)
    async with make_admin_client(seeded_users) as client:
        r = await client.post(
            "/v1/admin/keys",
            json={"user_id": "u_does_not_exist", "scopes": []},
            headers=auth_headers(ADMIN_RAW_KEY),
        )
    assert r.status_code == 404
    assert r.json()["code"] == "NOT_FOUND"


# ---------------------------------------------------------------------------
# 5. admin_revoke_key — happy path, no live attempts
# ---------------------------------------------------------------------------

async def test_admin_revoke_key_no_attempts(seeded_users):
    await _seed_admin_key(seeded_users)
    async with make_admin_client(seeded_users) as client:
        r = await client.post(
            "/v1/admin/keys/revoke",
            json={"key_id": "ak_wang_001"},
            headers=auth_headers(ADMIN_RAW_KEY),
        )
    assert r.status_code == 200, r.text
    body = r.json()
    assert body["ok"] is True
    assert body["terminated_attempts"] == []

    # Verify revoked_at is set
    async with seeded_users.connect() as conn:
        row = (await conn.execute(sa.text(
            "SELECT api_keys FROM users WHERE id = 'u_wangwu'"
        ))).scalar_one()
    key = next(k for k in row if k["id"] == "ak_wang_001")
    assert key["revoked_at"] is not None


# ---------------------------------------------------------------------------
# 6. admin_revoke_key — cascade: terminates running attempt + cascade cleanup
# ---------------------------------------------------------------------------

async def _seed_lisi_running_attempt(engine):
    """Seed a running run_attempt for u_lisi using key ak_li_001 on wi_a3f.

    We create a fresh work_item to avoid FK conflicts with the reference scenario.
    """
    async with engine.connect() as conn:
        # Check if wi_admin_test exists to avoid duplicate inserts (session-scoped DB)
        existing = (await conn.execute(sa.text(
            "SELECT id FROM work_items WHERE id = 'wi_admin_test'"
        ))).scalar_one_or_none()
        if existing is None:
            await conn.execute(sa.text("""
                INSERT INTO work_items (id, project, scenario, goal, status,
                                        reporter_user_id, labels)
                VALUES ('wi_admin_test', 'marketplace', 'coding',
                        'admin test work item', 'running',
                        'u_lisi', '[]'::jsonb)
            """))
        # Check if ra_li_test already exists
        existing_ra = (await conn.execute(sa.text(
            "SELECT id FROM run_attempts WHERE id = 'ra_li_test'"
        ))).scalar_one_or_none()
        if existing_ra is None:
            await conn.execute(sa.text("""
                INSERT INTO run_attempts (
                    id, work_item_id, status, claim_epoch, idempotency_key,
                    lease_until, actor_user_id, api_key_id, actor_display,
                    machine_id, session_secret_hash
                )
                VALUES ('ra_li_test', 'wi_admin_test', 'running', 99, 'idem_li_test',
                        now() + interval '60 seconds',
                        'u_lisi', 'ak_li_001', '李四', 'li-mbp', 'dummy_hash')
            """))
            await conn.execute(sa.text("""
                UPDATE work_items SET current_attempt_id = 'ra_li_test'
                WHERE id = 'wi_admin_test'
            """))
            # Add a resource lock
            await conn.execute(sa.text("""
                INSERT INTO resource_locks (resource_type, resource_key, owner_attempt_id, claim_epoch)
                VALUES ('git_branch', 'marketplace/pf3-li-test', 'ra_li_test', 99)
            """))
        await conn.commit()


async def test_admin_revoke_key_cascade(seeded_users):
    await _seed_admin_key(seeded_users)
    await _seed_lisi_running_attempt(seeded_users)

    async with make_admin_client(seeded_users) as client:
        r = await client.post(
            "/v1/admin/keys/revoke",
            json={"key_id": "ak_li_001"},
            headers=auth_headers(ADMIN_RAW_KEY),
        )
    assert r.status_code == 200, r.text
    body = r.json()
    assert body["ok"] is True
    assert "ra_li_test" in body["terminated_attempts"]

    async with seeded_users.connect() as conn:
        # run_attempt should be 'failed'
        ra_row = (await conn.execute(sa.text(
            "SELECT status, ended_at FROM run_attempts WHERE id = 'ra_li_test'"
        ))).mappings().first()
        assert ra_row["status"] == "failed"
        assert ra_row["ended_at"] is not None

        # resource_locks for ra_li_test should be gone
        lock_count = (await conn.execute(sa.text(
            "SELECT count(*) FROM resource_locks WHERE owner_attempt_id = 'ra_li_test'"
        ))).scalar_one()
        assert lock_count == 0

        # auth_revoked event should exist (design §17.4 / §7.8)
        evt_row = (await conn.execute(sa.text("""
            SELECT id FROM agent_events
            WHERE run_attempt_id = 'ra_li_test' AND event_type = 'auth_revoked'
        """))).mappings().first()
        assert evt_row is not None


# ---------------------------------------------------------------------------
# 7. admin_revoke_key — 404 for unknown key_id
# ---------------------------------------------------------------------------

async def test_admin_revoke_key_not_found(seeded_users):
    await _seed_admin_key(seeded_users)
    async with make_admin_client(seeded_users) as client:
        r = await client.post(
            "/v1/admin/keys/revoke",
            json={"key_id": "ak_does_not_exist"},
            headers=auth_headers(ADMIN_RAW_KEY),
        )
    assert r.status_code == 404
    assert r.json()["code"] == "NOT_FOUND"


# ---------------------------------------------------------------------------
# 8. admin_revoke_key — race: attempt already wrapped before revoke — not terminated
# ---------------------------------------------------------------------------

async def test_admin_revoke_key_skips_wrapped_attempt(seeded_users):
    """AH-2: revoke must not overwrite a concurrently-wrapped attempt.

    Seed a running attempt, then manually advance it to 'wrapped' (simulating
    a concurrent /complete call), then call revoke.  The WHERE status='running'
    predicate should fence it out — terminated_attempts must NOT include the
    already-wrapped id.
    """
    await _seed_admin_key(seeded_users)

    # Seed a fresh work_item and attempt using ak_zhang_001
    wi_id = "wi_admin_race_test"
    ra_id = "ra_admin_race_test"
    async with seeded_users.connect() as conn:
        existing_wi = (await conn.execute(sa.text(
            "SELECT id FROM work_items WHERE id = :id"
        ), {"id": wi_id})).scalar_one_or_none()
        if existing_wi is None:
            await conn.execute(sa.text("""
                INSERT INTO work_items (id, project, scenario, goal, status,
                                        reporter_user_id, labels)
                VALUES (:wi_id, 'marketplace', 'coding', 'race test wi', 'queued',
                        'u_zhangsan', '[]'::jsonb)
            """), {"wi_id": wi_id})
        existing_ra = (await conn.execute(sa.text(
            "SELECT id FROM run_attempts WHERE id = :id"
        ), {"id": ra_id})).scalar_one_or_none()
        if existing_ra is None:
            await conn.execute(sa.text("""
                INSERT INTO run_attempts (
                    id, work_item_id, status, claim_epoch, idempotency_key,
                    lease_until, actor_user_id, api_key_id, actor_display,
                    machine_id, session_secret_hash
                )
                VALUES (:ra_id, :wi_id, 'running', 200, 'idem_race_test',
                        now() + interval '60 seconds',
                        'u_zhangsan', 'ak_zhang_001', '张三', 'zhang-mbp', 'dummy_hash')
            """), {"ra_id": ra_id, "wi_id": wi_id})
            await conn.execute(sa.text("""
                UPDATE work_items SET current_attempt_id = :ra_id WHERE id = :wi_id
            """), {"ra_id": ra_id, "wi_id": wi_id})

        # Simulate concurrent /complete: advance to 'wrapped' BEFORE revoke
        await conn.execute(sa.text("""
            UPDATE run_attempts SET status = 'wrapped', ended_at = now()
            WHERE id = :ra_id
        """), {"ra_id": ra_id})
        await conn.commit()

    async with make_admin_client(seeded_users) as client:
        r = await client.post(
            "/v1/admin/keys/revoke",
            json={"key_id": "ak_zhang_001"},
            headers=auth_headers(ADMIN_RAW_KEY),
        )
    assert r.status_code == 200, r.text
    body = r.json()
    assert body["ok"] is True
    # The already-wrapped attempt must NOT appear in terminated_attempts
    assert ra_id not in body["terminated_attempts"]

    # Verify the wrapped attempt is still 'wrapped' (not overwritten to 'failed')
    async with seeded_users.connect() as conn:
        ra_status = (await conn.execute(sa.text(
            "SELECT status FROM run_attempts WHERE id = :id"
        ), {"id": ra_id})).scalar_one()
    assert ra_status == "wrapped"


# ---------------------------------------------------------------------------
# 9. admin_revoke_key — already-revoked key is idempotent
# ---------------------------------------------------------------------------

async def test_admin_revoke_key_idempotent(seeded_users):
    """Revoking an already-revoked key should still succeed (ok=True, no crash).

    The key wang_001 was already revoked in test 5 above. Re-revoking it
    should find the key, set revoked_at again (idempotent), and return ok=True.
    """
    await _seed_admin_key(seeded_users)
    async with make_admin_client(seeded_users) as client:
        r = await client.post(
            "/v1/admin/keys/revoke",
            json={"key_id": "ak_wang_001"},
            headers=auth_headers(ADMIN_RAW_KEY),
        )
    # Since ak_wang_001's user row still has the key entry, it's findable.
    # After revocation, api_keys @> [{id: ak_wang_001}] still matches.
    assert r.status_code == 200, r.text
    body = r.json()
    assert body["ok"] is True
    assert body["terminated_attempts"] == []  # no running attempts for revoked key


# ---------------------------------------------------------------------------
# 10. F1 (H4): revoke emits auth_revoked event (not the old attempt_revoked)
# ---------------------------------------------------------------------------

async def test_admin_revoke_key_emits_auth_revoked_event(seeded_users):
    """F1/H4: key revocation must emit event_type='auth_revoked' per design §17.4/§7.8."""
    await _seed_admin_key(seeded_users)
    await _seed_lisi_running_attempt(seeded_users)

    async with make_admin_client(seeded_users) as client:
        r = await client.post(
            "/v1/admin/keys/revoke",
            json={"key_id": "ak_li_001"},
            headers=auth_headers(ADMIN_RAW_KEY),
        )
    assert r.status_code == 200, r.text
    body = r.json()
    assert body["ok"] is True

    async with seeded_users.connect() as conn:
        # Must have auth_revoked, NOT attempt_revoked
        auth_revoked = (await conn.execute(sa.text("""
            SELECT id FROM agent_events
            WHERE run_attempt_id = 'ra_li_test' AND event_type = 'auth_revoked'
        """))).mappings().first()
        assert auth_revoked is not None, "auth_revoked event not found"

        old_evt = (await conn.execute(sa.text("""
            SELECT id FROM agent_events
            WHERE run_attempt_id = 'ra_li_test' AND event_type = 'attempt_revoked'
        """))).mappings().first()
        assert old_evt is None, "stale attempt_revoked event should not exist"
