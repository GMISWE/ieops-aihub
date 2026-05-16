"""s5b — work_items.parent_work_item_id tree linkage tests.

Design §5: work_items.parent_work_item_id TEXT REFERENCES work_items(id),
index idx_wi_parent WHERE parent_work_item_id IS NOT NULL.

§15.1 / §1 reference scenario §1 09:30 use parent_work_item_id as a
TOP-LEVEL column (not inside metadata). app/work_items.py:148 confirms:
    "parent": body.get("parent_work_item_id"),
wired into the INSERT statement directly.
"""
from __future__ import annotations

import pytest
import sqlalchemy as sa

from tests.v3.v3_client import (
    BEARER_ZHANG, auth_headers, make_async_client,
)


pytestmark = pytest.mark.asyncio(loop_scope="session")


def _wi_payload(goal: str, project: str = "marketplace", **extra) -> dict:
    base = {
        "project": project,
        "scenario": "coding",
        "goal": goal,
        "declared_resources": [
            {"type": "repo", "uri": "repo:marketplace", "intent": "write"},
        ],
        "source": "human",
    }
    base.update(extra)
    return base


# ---------------------------------------------------------------------------
# Test 1: create parent + child, child stores parent_work_item_id
# ---------------------------------------------------------------------------

async def test_create_parent_child_linkage(seeded_users):
    """POST parent → POST child with parent_work_item_id → child stored correctly.

    Verifies that parent_work_item_id is passed as a top-level field (not
    inside metadata), stored in the DB column, and returned in the response.
    """
    async with make_async_client(seeded_users) as client:
        # Create parent work item
        r_parent = await client.post(
            "/v1/work_items",
            json=_wi_payload("parent task: refactor auth"),
            headers=auth_headers(BEARER_ZHANG),
        )
        assert r_parent.status_code == 201, r_parent.text
        parent_id = r_parent.json()["id"]
        assert parent_id.startswith("wi_")

        # Create child work item with top-level parent_work_item_id
        r_child = await client.post(
            "/v1/work_items",
            json=_wi_payload("child task: update login handler",
                             parent_work_item_id=parent_id),
            headers=auth_headers(BEARER_ZHANG),
        )
        assert r_child.status_code == 201, r_child.text
        child_body = r_child.json()
        child_id = child_body["id"]
        assert child_id.startswith("wi_")

    # Verify top-level column (not just metadata)
    assert child_body.get("parent_work_item_id") == parent_id, (
        f"expected parent_work_item_id={parent_id!r} in response body, "
        f"got {child_body.get('parent_work_item_id')!r}"
    )
    # Also confirm it is NOT stored only in metadata (metadata may also reference it
    # but the canonical field must be the top-level column)
    # The DB-side check follows:
    async with seeded_users.connect() as conn:
        row = (await conn.execute(sa.text(
            "SELECT parent_work_item_id FROM work_items WHERE id = :id"
        ), {"id": child_id})).mappings().first()
    assert row is not None
    assert row["parent_work_item_id"] == parent_id, (
        "parent_work_item_id column in DB does not match — "
        "possibly stored in metadata only (regression)"
    )


# ---------------------------------------------------------------------------
# Test 2: GET child detail includes parent_work_item_id in response
# ---------------------------------------------------------------------------

async def test_get_child_returns_parent_linkage(seeded_users):
    """GET /v1/work_items/{child_id} detail must surface parent_work_item_id."""
    async with make_async_client(seeded_users) as client:
        r_parent = await client.post(
            "/v1/work_items",
            json=_wi_payload("parent: integrate oauth"),
            headers=auth_headers(BEARER_ZHANG),
        )
        assert r_parent.status_code == 201
        parent_id = r_parent.json()["id"]

        r_child = await client.post(
            "/v1/work_items",
            json=_wi_payload("child: implement token refresh",
                             parent_work_item_id=parent_id),
            headers=auth_headers(BEARER_ZHANG),
        )
        assert r_child.status_code == 201
        child_id = r_child.json()["id"]

        # GET detail
        r_detail = await client.get(
            f"/v1/work_items/{child_id}",
            headers=auth_headers(BEARER_ZHANG),
        )
    assert r_detail.status_code == 200, r_detail.text
    detail = r_detail.json()
    wi = detail["work_item"]
    assert wi["parent_work_item_id"] == parent_id, (
        f"GET detail missing parent_work_item_id; got {wi.get('parent_work_item_id')!r}"
    )


# ---------------------------------------------------------------------------
# Test 3: partial index idx_wi_parent — SQL-level filter returns only children
# ---------------------------------------------------------------------------

async def test_parent_index_filter_returns_exact_children(seeded_users):
    """Insert 1 parent + 3 children + 5 unrelated wi's.
    SELECT WHERE parent_work_item_id = parent.id must return exactly 3 rows.

    Tests that the idx_wi_parent partial index (WHERE parent_work_item_id IS NOT NULL)
    is usable and the FK constraint correctly groups children.

    API route does not expose a parent_id filter param, so we test at the
    SQL level — this is the correct layer for index coverage validation.
    """
    async with make_async_client(seeded_users) as client:
        # Create 1 parent
        r_parent = await client.post(
            "/v1/work_items",
            json=_wi_payload("tree parent: large refactor"),
            headers=auth_headers(BEARER_ZHANG),
        )
        assert r_parent.status_code == 201
        parent_id = r_parent.json()["id"]

        # Create 3 children
        child_ids = []
        for i in range(3):
            r = await client.post(
                "/v1/work_items",
                json=_wi_payload(f"child subtask {i}", parent_work_item_id=parent_id),
                headers=auth_headers(BEARER_ZHANG),
            )
            assert r.status_code == 201, r.text
            child_ids.append(r.json()["id"])

        # Create 5 unrelated work items (no parent)
        for i in range(5):
            r = await client.post(
                "/v1/work_items",
                json=_wi_payload(f"unrelated work item {i}"),
                headers=auth_headers(BEARER_ZHANG),
            )
            assert r.status_code == 201, r.text

    # SQL-level assertion against the DB (no route needed)
    async with seeded_users.connect() as conn:
        rows = (await conn.execute(sa.text("""
            SELECT id FROM work_items
            WHERE parent_work_item_id = :parent_id
        """), {"parent_id": parent_id})).mappings().all()

    found_ids = {r["id"] for r in rows}
    assert found_ids == set(child_ids), (
        f"Expected exactly the 3 children {set(child_ids)}, "
        f"got {found_ids} — index filter or FK linkage broken"
    )


# ---------------------------------------------------------------------------
# Test 4: complete parent while a child is still running — emits warn event
# ---------------------------------------------------------------------------

async def test_parent_complete_with_running_child_emits_warn_event(seeded_reference):
    """POST /v1/work_items/{parent_id}/complete while a child is still queued.

    Per design §11 / reference §1 11:00: parent completion with open children
    should NOT be blocked — it completes the parent AND emits a
    'work_item_completed_with_open_children' warn event with child_ids.

    We create a fresh parent + child tree via the API, then directly seed a
    running attempt for the parent (avoids claim scheduler non-determinism)
    so that verify_mutation accepts our /complete call.
    """
    import hashlib as _hashlib
    import sqlalchemy as sa
    from ulid import ULID
    from tests.v3.v3_client import make_async_client, BEARER_ZHANG, auth_headers

    # 1. Create a fresh parent work item
    async with make_async_client(seeded_reference) as client:
        r_parent = await client.post(
            "/v1/work_items",
            json=_wi_payload("parent: needs warn-on-open-child"),
            headers=auth_headers(BEARER_ZHANG),
        )
        assert r_parent.status_code == 201, r_parent.text
        parent_id = r_parent.json()["id"]

        # 2. Create a child work item (stays 'queued' — non-terminal)
        r_child = await client.post(
            "/v1/work_items",
            json=_wi_payload("child: still queued",
                             parent_work_item_id=parent_id),
            headers=auth_headers(BEARER_ZHANG),
        )
        assert r_child.status_code == 201, r_child.text
        child_id = r_child.json()["id"]

    # 3. Directly seed a running attempt for the parent so /complete works.
    # session_secret must be exactly 64 hex chars (§6.1 constraint).
    # The stored hash is sha256(raw_secret).hexdigest() — no prefix (see auth.py).
    session_secret = "a" * 64  # 64-char hex-like string satisfying the pattern
    secret_hash = _hashlib.sha256(session_secret.encode("ascii")).hexdigest()
    attempt_id = "ra_" + str(ULID()).lower()
    claim_epoch = 1

    async with seeded_reference.begin() as conn:
        await conn.execute(sa.text("""
            INSERT INTO run_attempts (
                id, work_item_id, status, claim_epoch, idempotency_key,
                lease_until, actor_user_id, api_key_id, actor_display,
                machine_id, session_secret_hash
            )
            VALUES (:aid, :wid, 'running', :epoch, :idem,
                    now() + interval '60 seconds',
                    'u_zhangsan', 'ak_zhang_001', '张三', 'test-machine',
                    :secret_hash)
        """), {
            "aid": attempt_id,
            "wid": parent_id,
            "epoch": claim_epoch,
            "idem": f"idem_warn_{parent_id}",
            "secret_hash": secret_hash,
        })
        await conn.execute(sa.text("""
            UPDATE work_items
            SET status = 'running', current_attempt_id = :aid
            WHERE id = :wid
        """), {"aid": attempt_id, "wid": parent_id})

    # 4. Complete the parent while the child is still 'queued' (non-terminal)
    async with make_async_client(seeded_reference) as client:
        r_complete = await client.post(
            f"/v1/work_items/{parent_id}/complete",
            json={
                "attempt_id": attempt_id,
                "claim_epoch": claim_epoch,
                "session_secret": session_secret,
                "final_status": "wrapped",
            },
            headers=auth_headers(BEARER_ZHANG),
        )
    # Parent complete must succeed (warn-not-block semantics)
    assert r_complete.status_code == 200, (
        f"Expected 200 (warn-not-block), got {r_complete.status_code}: {r_complete.text}"
    )

    # 5. Verify the warn event was emitted with the child id in payload
    async with seeded_reference.connect() as conn:
        evt_row = (await conn.execute(sa.text("""
            SELECT payload FROM agent_events
            WHERE work_item_id = :wid
              AND event_type = 'work_item_completed_with_open_children'
            ORDER BY created_at DESC
            LIMIT 1
        """), {"wid": parent_id})).mappings().first()

    assert evt_row is not None, (
        "Expected 'work_item_completed_with_open_children' event to be emitted "
        "when parent is completed while child is still open — event not found"
    )
    payload = dict(evt_row["payload"])
    assert child_id in payload.get("child_ids", []), (
        f"Expected child_id={child_id!r} in warn event payload.child_ids; "
        f"got payload={payload}"
    )
    assert payload.get("child_count", 0) >= 1, (
        f"Expected child_count >= 1 in warn event payload; got {payload}"
    )
