"""s5 — POST /v1/work_items/{id}/claim atomic claim tests (§7.2.1)."""
from __future__ import annotations

import pytest
import sqlalchemy as sa

from tests.v3.v3_client import (
    BEARER_LI, BEARER_WANG, BEARER_ZHANG, auth_headers, make_async_client,
)


pytestmark = pytest.mark.asyncio(loop_scope="session")


SECRET = "a" * 64
SECRET_B = "b" * 64


def _claim_body(idempotency_key="idem_001", session_secret=SECRET,
                machine_id="zhang-mbp", locks=None):
    if locks is None:
        locks = [{"resource_type": "git_branch", "resource_key": "marketplace/polyforge/wi_x"}]
    return {
        "idempotency_key": idempotency_key,
        "session_info": {"machine_id": machine_id, "session_secret": session_secret},
        "requested_locks": locks,
    }


def _new_wi_payload(project="marketplace", goal="x"):
    return {
        "project": project, "scenario": "coding", "goal": goal,
        "declared_resources": [
            {"type": "repo", "uri": "repo:marketplace", "intent": "write",
             "base_branch": "main", "task_branch": "polyforge/wi_x"},
        ],
        "labels": [], "priority": "normal", "source": "human",
    }


# ---- happy path ----

async def test_claim_queued_happy(seeded_users):
    """Fresh queued work_item → running, new attempt epoch=1, lock created."""
    async with make_async_client(seeded_users) as client:
        r0 = await client.post("/v1/work_items", json=_new_wi_payload(),
                               headers=auth_headers(BEARER_ZHANG))
        wi_id = r0.json()["id"]
        r1 = await client.post(
            f"/v1/work_items/{wi_id}/claim",
            json=_claim_body(locks=[{"resource_type": "git_branch",
                                     "resource_key": f"marketplace/{wi_id}"}]),
            headers=auth_headers(BEARER_ZHANG),
        )
    assert r1.status_code == 200, r1.text
    body = r1.json()
    assert body["attempt_id"].startswith("ra_")
    assert body["claim_epoch"] == 1
    assert body["lease_until"] is not None

    # verify side effects in DB
    async with seeded_users.connect() as conn:
        wi = (await conn.execute(sa.text(
            "SELECT status, current_attempt_id FROM work_items WHERE id = :id"
        ), {"id": wi_id})).first()
        assert wi == ("running", body["attempt_id"])

        ra = (await conn.execute(sa.text(
            "SELECT status, claim_epoch FROM run_attempts WHERE id = :aid"
        ), {"aid": body["attempt_id"]})).first()
        assert ra == ("running", 1)

        lock_count = (await conn.execute(sa.text(
            "SELECT COUNT(*) FROM resource_locks WHERE owner_attempt_id = :aid"
        ), {"aid": body["attempt_id"]})).scalar()
        assert lock_count == 1

        events = [r[0] for r in (await conn.execute(sa.text("""
            SELECT event_type FROM agent_events
            WHERE work_item_id = :wid AND run_attempt_id = :aid
        """), {"wid": wi_id, "aid": body["attempt_id"]}))]
        assert "attempt_started" in events
        assert "lock_acquired" in events


# ---- idempotency ----

async def test_claim_duplicate_idempotency_key(seeded_users):
    """Same key + running attempt → returns same attempt, no new insert."""
    async with make_async_client(seeded_users) as client:
        r0 = await client.post("/v1/work_items", json=_new_wi_payload(),
                               headers=auth_headers(BEARER_ZHANG))
        wi_id = r0.json()["id"]
        body = _claim_body(idempotency_key="idem_dup",
                           locks=[{"resource_type": "git_branch",
                                   "resource_key": f"marketplace/{wi_id}"}])
        r1 = await client.post(f"/v1/work_items/{wi_id}/claim", json=body,
                               headers=auth_headers(BEARER_ZHANG))
        r2 = await client.post(f"/v1/work_items/{wi_id}/claim", json=body,
                               headers=auth_headers(BEARER_ZHANG))
    assert r1.status_code == 200
    assert r2.status_code == 200
    assert r1.json()["attempt_id"] == r2.json()["attempt_id"]
    assert r1.json()["claim_epoch"] == r2.json()["claim_epoch"]

    # Only one running attempt
    async with seeded_users.connect() as conn:
        cnt = (await conn.execute(sa.text(
            "SELECT COUNT(*) FROM run_attempts WHERE work_item_id = :wid"
        ), {"wid": wi_id})).scalar()
        assert cnt == 1


# ---- busy not takeover eligible ----

async def test_claim_busy_not_takeover_eligible(seeded_reference):
    """wi_a3f is running + lease alive (seed 09:00). Another claim → 409."""
    async with make_async_client(seeded_reference) as client:
        r = await client.post(
            "/v1/work_items/wi_a3f/claim",
            json=_claim_body(idempotency_key="idem_li_attempt",
                             session_secret=SECRET_B, machine_id="li-mbp",
                             locks=[]),
            headers=auth_headers(BEARER_LI),
        )
    assert r.status_code == 409
    assert r.json()["code"] == "CONFLICT_BUSY_NOT_TAKEOVER_ELIGIBLE"


# ---- takeover (lease expired) ----

async def test_claim_takeover_expired_lease(seeded_reference):
    """Force ra_111.lease_until into the past, then 王五 claims → epoch=2.
    Mirrors §1 14:15 takeover from reference scenario."""
    async with seeded_reference.connect() as conn:
        await conn.execute(sa.text("""
            UPDATE run_attempts SET lease_until = now() - interval '1 minute'
            WHERE id = 'ra_111'
        """))
        await conn.commit()
    async with make_async_client(seeded_reference) as client:
        r = await client.post(
            "/v1/work_items/wi_a3f/claim",
            json=_claim_body(idempotency_key="idem_wang_takeover",
                             session_secret=SECRET_B, machine_id="wang-mbp",
                             locks=[{"resource_type": "git_branch",
                                     "resource_key": "marketplace/polyforge/wi_a3f"}]),
            headers=auth_headers(BEARER_WANG),
        )
    assert r.status_code == 200, r.text
    body = r.json()
    assert body["claim_epoch"] == 2

    async with seeded_reference.connect() as conn:
        # old attempt superseded
        old = (await conn.execute(sa.text(
            "SELECT status, ended_at FROM run_attempts WHERE id = 'ra_111'"
        ))).first()
        assert old[0] == "superseded"
        assert old[1] is not None

        # new attempt parent chain
        new = (await conn.execute(sa.text(
            "SELECT parent_attempt_id, claim_epoch FROM run_attempts WHERE id = :aid"
        ), {"aid": body["attempt_id"]})).first()
        assert new == ("ra_111", 2)

        # old locks deleted
        old_locks = (await conn.execute(sa.text(
            "SELECT COUNT(*) FROM resource_locks WHERE owner_attempt_id = 'ra_111'"
        ))).scalar()
        assert old_locks == 0

        # attempt_taken_over event
        ev_types = {r[0] for r in (await conn.execute(sa.text(
            "SELECT event_type FROM agent_events WHERE work_item_id = 'wi_a3f'"
        )))}
        assert "attempt_taken_over" in ev_types


# ---- hard_block on lock ----

async def test_claim_hard_block_on_lock(seeded_reference):
    """wi_a3f holds lock (git_branch, marketplace/polyforge/wi_a3f) via ra_111.
    李四 creates new wi and tries to claim that same lock → 409 hard_block."""
    async with make_async_client(seeded_reference) as client:
        r0 = await client.post(
            "/v1/work_items", json=_new_wi_payload(goal="refactor auth"),
            headers=auth_headers(BEARER_LI),
        )
        wi_li = r0.json()["id"]
        r = await client.post(
            f"/v1/work_items/{wi_li}/claim",
            json=_claim_body(idempotency_key="idem_li_lockclash",
                             session_secret=SECRET_B, machine_id="li-mbp",
                             locks=[{"resource_type": "git_branch",
                                     "resource_key": "marketplace/polyforge/wi_a3f"}]),
            headers=auth_headers(BEARER_LI),
        )
    assert r.status_code == 409
    body = r.json()
    assert body["code"] == "CONFLICT_HARD_BLOCK"
    assert body["details"]["rule_id"] == "lock_conflict"
    assert body["details"]["conflicts_with"]["actor_display"] == "张三"


# ---- 404 unknown work_item ----

async def test_claim_not_found(seeded_users):
    async with make_async_client(seeded_users) as client:
        r = await client.post(
            "/v1/work_items/wi_nonexistent/claim",
            json=_claim_body(), headers=auth_headers(BEARER_ZHANG),
        )
    assert r.status_code == 404


# ---- 403 wrong project ----

async def test_claim_forbidden_project(seeded_users):
    """Create aihub wi as 张三; 王五 (not in aihub) tries to claim."""
    async with make_async_client(seeded_users) as client:
        r0 = await client.post(
            "/v1/work_items", json=_new_wi_payload(project="aihub"),
            headers=auth_headers(BEARER_ZHANG),
        )
        wi_id = r0.json()["id"]
        r = await client.post(
            f"/v1/work_items/{wi_id}/claim",
            json=_claim_body(idempotency_key="idem_wang_x",
                             session_secret=SECRET_B, machine_id="wang-mbp",
                             locks=[]),
            headers=auth_headers(BEARER_WANG),
        )
    assert r.status_code == 403


# ---- 401 no bearer ----

async def test_claim_no_bearer(seeded_users):
    async with make_async_client(seeded_users) as client:
        r = await client.post("/v1/work_items/wi_a3f/claim", json=_claim_body())
    assert r.status_code == 401


# ---- ABA-safety: re-claim with same idempotency_key after takeover ----

async def test_claim_aba_safe_after_takeover(seeded_reference):
    """Force takeover, expire Wang's lease, then Zhang re-claims with the original
    superseded idempotency_key → reaches INSERT → UNIQUE violation → 409
    CONFLICT_DUPLICATE_REQUEST.

    Per MED-1 fix (design.md §6.1): replay only applies to status='running'.
    Once ra_111 is superseded, the same key is terminal → replay returns nothing
    → INSERT → UNIQUE (work_item_id, idempotency_key) → 409.

    Note: eligibility (step 3) must pass first. We expire Wang's lease so that
    the claim is takeover-eligible, allowing us to reach the INSERT step.
    """
    async with seeded_reference.connect() as conn:
        # Expire ra_111 so Wang can take over
        await conn.execute(sa.text("""
            UPDATE run_attempts SET lease_until = now() - interval '1 minute'
            WHERE id = 'ra_111'
        """))
        await conn.commit()
    async with make_async_client(seeded_reference) as client:
        # 王五 takeover — ra_111 becomes superseded; Wang's attempt gets a fresh lease
        r1 = await client.post(
            "/v1/work_items/wi_a3f/claim",
            json=_claim_body(idempotency_key="idem_wang_aba",
                             session_secret=SECRET_B, machine_id="wang-mbp",
                             locks=[{"resource_type": "git_branch",
                                     "resource_key": "marketplace/polyforge/wi_a3f"}]),
            headers=auth_headers(BEARER_WANG),
        )
        assert r1.status_code == 200
        wang_aid = r1.json()["attempt_id"]

    # Expire Wang's new attempt too so Zhang's re-claim passes eligibility
    async with seeded_reference.connect() as conn:
        await conn.execute(sa.text("""
            UPDATE run_attempts SET lease_until = now() - interval '1 minute'
            WHERE id = :aid
        """), {"aid": wang_aid})
        await conn.commit()

    async with make_async_client(seeded_reference) as client:
        # 张三 (old owner) re-claims with original key "idem_001" — ra_111 is
        # superseded (terminal). Replay SELECT returns nothing (status≠running).
        # Eligibility: eligible (Wang's lease expired). Reaches INSERT.
        # UNIQUE (work_item_id, idempotency_key) fires → 409.
        r2 = await client.post(
            "/v1/work_items/wi_a3f/claim",
            json=_claim_body(idempotency_key="idem_001",  # original key, now terminal
                             locks=[]),
            headers=auth_headers(BEARER_ZHANG),
        )
    assert r2.status_code == 409
    assert r2.json()["code"] == "CONFLICT_DUPLICATE_REQUEST"


async def test_idempotency_replay_only_returns_running(seeded_users):
    """MED-1: idempotency replay must only match status='running' attempts.

    Flow:
      1. Zhang creates wi + claims with key K → attempt A (running)
      2. Confirm happy-path replay: same key, A still running → 200 same attempt
      3. Expire A's lease → Wang takes over → A becomes superseded
      4. Expire Wang's lease so Zhang's re-claim passes eligibility
      5. Zhang re-claims with same key K → replay returns nothing (A superseded)
         → INSERT → UNIQUE violation → 409 CONFLICT_DUPLICATE_REQUEST
    """
    KEY = "idem_med1_test"
    async with make_async_client(seeded_users) as client:
        # Create work_item
        r0 = await client.post("/v1/work_items", json=_new_wi_payload(),
                               headers=auth_headers(BEARER_ZHANG))
        assert r0.status_code == 201
        wi_id = r0.json()["id"]

        lock_key = f"marketplace/{wi_id}"
        # Claim with key K — attempt A is running
        r1 = await client.post(
            f"/v1/work_items/{wi_id}/claim",
            json=_claim_body(idempotency_key=KEY,
                             locks=[{"resource_type": "git_branch",
                                     "resource_key": lock_key}]),
            headers=auth_headers(BEARER_ZHANG),
        )
        assert r1.status_code == 200
        aid = r1.json()["attempt_id"]

        # Happy-path replay: same key, A still running → same attempt back (200)
        r_replay = await client.post(
            f"/v1/work_items/{wi_id}/claim",
            json=_claim_body(idempotency_key=KEY,
                             locks=[{"resource_type": "git_branch",
                                     "resource_key": lock_key}]),
            headers=auth_headers(BEARER_ZHANG),
        )
        assert r_replay.status_code == 200
        assert r_replay.json()["attempt_id"] == aid  # same attempt

    # Expire A's lease
    async with seeded_users.connect() as conn:
        await conn.execute(sa.text("""
            UPDATE run_attempts SET lease_until = now() - interval '2 minutes'
            WHERE id = :aid
        """), {"aid": aid})
        await conn.commit()

    async with make_async_client(seeded_users) as client:
        # Wang takeover — A becomes superseded; Wang gets a fresh lease
        r2 = await client.post(
            f"/v1/work_items/{wi_id}/claim",
            json=_claim_body(idempotency_key="idem_wang_med1",
                             session_secret=SECRET_B, machine_id="wang-mbp",
                             locks=[]),
            headers=auth_headers(BEARER_WANG),
        )
        assert r2.status_code == 200, r2.text
        wang_aid = r2.json()["attempt_id"]

    # Expire Wang's lease so Zhang's re-claim passes eligibility (step 3)
    # and reaches the INSERT (step 7) where UNIQUE violation fires.
    async with seeded_users.connect() as conn:
        await conn.execute(sa.text("""
            UPDATE run_attempts SET lease_until = now() - interval '2 minutes'
            WHERE id = :aid
        """), {"aid": wang_aid})
        await conn.commit()

    async with make_async_client(seeded_users) as client:
        # Zhang re-tries with key K — A is superseded (terminal).
        # Step 3a: replay returns nothing (status≠running).
        # Step 3: eligible (Wang's lease expired).
        # Step 7: INSERT → UNIQUE (work_item_id, idempotency_key) → 409.
        r3 = await client.post(
            f"/v1/work_items/{wi_id}/claim",
            json=_claim_body(idempotency_key=KEY, locks=[]),
            headers=auth_headers(BEARER_ZHANG),
        )
    assert r3.status_code == 409
    assert r3.json()["code"] == "CONFLICT_DUPLICATE_REQUEST"
