"""s7 — /v1/work_items/{id}/complete + /v1/locks + /v1/events."""
from __future__ import annotations

import pytest
import sqlalchemy as sa

from app.auth import _hash_session_secret
from tests.v3.v3_client import (
    BEARER_LI, BEARER_WANG, BEARER_ZHANG, auth_headers, make_async_client,
)


pytestmark = pytest.mark.asyncio(loop_scope="session")


SECRET = "a" * 64


async def _seed_real_secret(engine, raw=SECRET, attempt_id="ra_111"):
    async with engine.connect() as conn:
        await conn.execute(sa.text(
            "UPDATE run_attempts SET session_secret_hash = :h WHERE id = :aid"
        ), {"h": _hash_session_secret(raw), "aid": attempt_id})
        await conn.commit()


# ---- work_item complete ----

async def test_complete_work_item_happy(seeded_reference):
    await _seed_real_secret(seeded_reference)
    async with make_async_client(seeded_reference) as client:
        r = await client.post(
            "/v1/work_items/wi_a3f/complete",
            json={"attempt_id": "ra_111", "claim_epoch": 1,
                  "session_secret": SECRET, "final_status": "wrapped"},
            headers=auth_headers(BEARER_ZHANG),
        )
    assert r.status_code == 200

    async with seeded_reference.connect() as conn:
        wi = (await conn.execute(sa.text(
            "SELECT status, closed_at, current_attempt_id FROM work_items WHERE id='wi_a3f'"
        ))).first()
        assert wi[0] == "wrapped"
        assert wi[1] is not None
        assert wi[2] is None
        ra = (await conn.execute(sa.text(
            "SELECT status FROM run_attempts WHERE id='ra_111'"
        ))).scalar()
        assert ra == "wrapped"
        ev_types = {r[0] for r in (await conn.execute(sa.text(
            "SELECT event_type FROM agent_events WHERE work_item_id='wi_a3f'"
        )))}
        assert "work_item_completed" in ev_types


async def test_complete_work_item_forbidden_random_user(seeded_reference):
    """李四 (not reporter, not current actor) tries to complete → 403.

    Verify_mutation has earlier 'attempt belongs to different user' guard, which
    fires first with 403."""
    await _seed_real_secret(seeded_reference)
    async with make_async_client(seeded_reference) as client:
        r = await client.post(
            "/v1/work_items/wi_a3f/complete",
            json={"attempt_id": "ra_111", "claim_epoch": 1,
                  "session_secret": SECRET, "final_status": "wrapped"},
            headers=auth_headers(BEARER_LI),
        )
    assert r.status_code == 403


# ---- acquire / release lock ----

async def test_acquire_lock_happy(seeded_reference):
    await _seed_real_secret(seeded_reference)
    async with make_async_client(seeded_reference) as client:
        r = await client.post(
            "/v1/locks",
            json={"attempt_id": "ra_111", "claim_epoch": 1,
                  "session_secret": SECRET,
                  "resource_type": "file_scope",
                  "resource_key": "marketplace/src/auth/login.py"},
            headers=auth_headers(BEARER_ZHANG),
        )
    assert r.status_code == 200
    async with seeded_reference.connect() as conn:
        cnt = (await conn.execute(sa.text("""
            SELECT COUNT(*) FROM resource_locks
            WHERE resource_type='file_scope' AND owner_attempt_id='ra_111'
        """))).scalar()
        assert cnt == 1


async def test_acquire_lock_conflict(seeded_reference):
    """Existing seed lock (git_branch, marketplace/polyforge/wi_a3f) → 409."""
    await _seed_real_secret(seeded_reference)
    async with make_async_client(seeded_reference) as client:
        r = await client.post(
            "/v1/locks",
            json={"attempt_id": "ra_111", "claim_epoch": 1,
                  "session_secret": SECRET,
                  "resource_type": "git_branch",
                  "resource_key": "marketplace/polyforge/wi_a3f"},
            headers=auth_headers(BEARER_ZHANG),
        )
    assert r.status_code == 409
    assert r.json()["code"] == "CONFLICT_HARD_BLOCK"


async def test_release_lock_happy(seeded_reference):
    await _seed_real_secret(seeded_reference)
    async with make_async_client(seeded_reference) as client:
        r = await client.request(
            "DELETE",
            "/v1/locks/git_branch/marketplace/polyforge/wi_a3f",
            json={"attempt_id": "ra_111", "claim_epoch": 1,
                  "session_secret": SECRET},
            headers=auth_headers(BEARER_ZHANG),
        )
    assert r.status_code == 200, r.text
    async with seeded_reference.connect() as conn:
        cnt = (await conn.execute(sa.text("""
            SELECT COUNT(*) FROM resource_locks
            WHERE resource_type='git_branch' AND resource_key='marketplace/polyforge/wi_a3f'
        """))).scalar()
        assert cnt == 0


async def test_release_lock_not_owner(seeded_reference):
    """李四 has no attempt; create ra for li, then attempt to release ra_111's lock."""
    await _seed_real_secret(seeded_reference)
    # Insert a running attempt for li to make verify_mutation pass
    async with seeded_reference.connect() as conn:
        # Create a parallel queued wi+ra for li
        await conn.execute(sa.text("""
            INSERT INTO work_items (id, project, scenario, goal, status, priority,
                                    labels, reporter_user_id, declared_resources,
                                    resources_version, metadata)
            VALUES ('wi_222li', 'marketplace', 'coding', 'x', 'running', 'normal',
                    '[]'::jsonb, 'u_lisi', '[]'::jsonb, 0, '{}'::jsonb)
        """))
        await conn.execute(sa.text("""
            INSERT INTO run_attempts (id, work_item_id, status, claim_epoch,
                                       idempotency_key, lease_until, actor_user_id,
                                       api_key_id, actor_display, machine_id,
                                       session_secret_hash)
            VALUES ('ra_222li', 'wi_222li', 'running', 1, 'idemlix',
                    now() + interval '60 seconds', 'u_lisi', 'ak_li_001',
                    '李四', 'li-mbp', :h)
        """), {"h": _hash_session_secret(SECRET)})
        await conn.execute(sa.text(
            "UPDATE work_items SET current_attempt_id='ra_222li' WHERE id='wi_222li'"
        ))
        await conn.commit()
    async with make_async_client(seeded_reference) as client:
        r = await client.request(
            "DELETE",
            "/v1/locks/git_branch/marketplace/polyforge/wi_a3f",
            json={"attempt_id": "ra_222li", "claim_epoch": 1,
                  "session_secret": SECRET},
            headers=auth_headers(BEARER_LI),
        )
    assert r.status_code == 403, r.text


# ---- emit + list events ----

async def test_emit_event_happy(seeded_reference):
    await _seed_real_secret(seeded_reference)
    async with make_async_client(seeded_reference) as client:
        r = await client.post(
            "/v1/events",
            json={"attempt_id": "ra_111", "claim_epoch": 1,
                  "session_secret": SECRET, "work_item_id": "wi_a3f",
                  "event_type": "commit",
                  "payload": {"repo": "marketplace", "sha": "4f2a8b",
                              "files": ["src/auth/login.py"]}},
            headers=auth_headers(BEARER_ZHANG),
        )
    assert r.status_code == 201
    assert r.json()["event_id"].startswith("evt_")


async def test_emit_event_payload_too_large(seeded_reference):
    await _seed_real_secret(seeded_reference)
    big = "x" * 70000
    async with make_async_client(seeded_reference) as client:
        r = await client.post(
            "/v1/events",
            json={"attempt_id": "ra_111", "claim_epoch": 1,
                  "session_secret": SECRET, "work_item_id": "wi_a3f",
                  "event_type": "note", "payload": {"big": big}},
            headers=auth_headers(BEARER_ZHANG),
        )
    assert r.status_code == 413


async def test_emit_event_wi_mismatch(seeded_reference):
    await _seed_real_secret(seeded_reference)
    async with make_async_client(seeded_reference) as client:
        r = await client.post(
            "/v1/events",
            json={"attempt_id": "ra_111", "claim_epoch": 1,
                  "session_secret": SECRET, "work_item_id": "wi_other",
                  "event_type": "note", "payload": {}},
            headers=auth_headers(BEARER_ZHANG),
        )
    assert r.status_code == 400


async def test_list_events_filter_work_item(seeded_reference):
    async with make_async_client(seeded_reference) as client:
        r = await client.get("/v1/events?work_item_id=wi_a3f",
                             headers=auth_headers(BEARER_ZHANG))
    assert r.status_code == 200
    body = r.json()
    assert all(i["work_item_id"] == "wi_a3f" for i in body["items"])
    assert len(body["items"]) >= 2


async def test_list_events_filter_types(seeded_reference):
    async with make_async_client(seeded_reference) as client:
        r = await client.get(
            "/v1/events?work_item_id=wi_a3f&types=attempt_started",
            headers=auth_headers(BEARER_ZHANG),
        )
    assert r.status_code == 200
    body = r.json()
    assert all(i["event_type"] == "attempt_started" for i in body["items"])
    assert len(body["items"]) >= 1


async def test_list_events_forbidden(seeded_reference):
    """王五 not in aihub. Create aihub wi w/ events; 王五 listing 403."""
    async with seeded_reference.connect() as conn:
        await conn.execute(sa.text("""
            INSERT INTO work_items (id, project, scenario, goal, status, priority,
                                    labels, reporter_user_id, declared_resources,
                                    resources_version, metadata)
            VALUES ('wi_aihubx', 'aihub', 'coding', 'x', 'queued', 'normal',
                    '[]'::jsonb, 'u_zhangsan', '[]'::jsonb, 0, '{}'::jsonb)
        """))
        await conn.commit()
    async with make_async_client(seeded_reference) as client:
        r = await client.get("/v1/events?work_item_id=wi_aihubx",
                             headers=auth_headers(BEARER_WANG))
    assert r.status_code == 403
