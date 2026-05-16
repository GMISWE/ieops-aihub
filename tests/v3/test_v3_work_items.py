"""s4 — POST/GET /v1/work_items + GET /v1/work_items/{id}."""
from __future__ import annotations

import json

import pytest
import sqlalchemy as sa

from tests.v3.v3_client import (
    BEARER_LI, BEARER_WANG, BEARER_ZHANG, auth_headers, make_async_client,
)


pytestmark = pytest.mark.asyncio(loop_scope="session")


def _wi_payload(project="marketplace", goal="fix login 500"):
    return {
        "project": project,
        "scenario": "coding",
        "goal": goal,
        "declared_resources": [
            {"type": "repo", "uri": "repo:marketplace", "intent": "write",
             "base_branch": "main", "task_branch": "polyforge/wi_new"},
            {"type": "path", "uri": "file:marketplace/src/auth/**", "intent": "write"},
        ],
        "labels": ["bug"],
        "priority": "high",
        "source": "human",
    }


# ---- POST /v1/work_items ----

async def test_create_work_item_happy(seeded_users):
    async with make_async_client(seeded_users) as client:
        r = await client.post(
            "/v1/work_items", json=_wi_payload(),
            headers=auth_headers(BEARER_ZHANG),
        )
    assert r.status_code == 201
    body = r.json()
    assert body["id"].startswith("wi_")
    assert body["status"] == "queued"
    assert body["reporter_user_id"] == "u_zhangsan"
    assert body["project"] == "marketplace"
    assert body["priority"] == "high"
    assert body["labels"] == ["bug"]
    assert body["metadata"].get("source") == "human"
    assert body["resources_version"] == 0
    assert len(body["declared_resources"]) == 2


async def test_create_work_item_emits_event(seeded_users):
    async with make_async_client(seeded_users) as client:
        r = await client.post(
            "/v1/work_items", json=_wi_payload(),
            headers=auth_headers(BEARER_ZHANG),
        )
        wi_id = r.json()["id"]
    async with seeded_users.connect() as conn:
        row = (await conn.execute(sa.text("""
            SELECT event_type, payload FROM agent_events WHERE work_item_id = :id
        """), {"id": wi_id})).mappings().first()
    assert row is not None
    assert row["event_type"] == "work_item_filed"
    assert row["payload"]["project"] == "marketplace"


async def test_create_work_item_no_bearer(seeded_users):
    async with make_async_client(seeded_users) as client:
        r = await client.post("/v1/work_items", json=_wi_payload())
    assert r.status_code == 401


async def test_create_work_item_project_not_in_scope(seeded_users):
    """王五 is only in marketplace; aihub project → 403."""
    async with make_async_client(seeded_users) as client:
        r = await client.post(
            "/v1/work_items", json=_wi_payload(project="aihub"),
            headers=auth_headers(BEARER_WANG),
        )
    assert r.status_code == 403
    assert r.json()["code"] == "FORBIDDEN"


async def test_create_work_item_invalid_resource(seeded_users):
    payload = _wi_payload()
    payload["declared_resources"][0]["intent"] = "bogus"
    async with make_async_client(seeded_users) as client:
        r = await client.post(
            "/v1/work_items", json=payload, headers=auth_headers(BEARER_ZHANG),
        )
    # pydantic-level enum check returns 400 (our handler) or 422 (default).
    # We force 400 via our handler.
    assert r.status_code == 400


async def test_create_work_item_auto_source(seeded_reference):
    """Path B — auto source allowed when caller has a running attempt (§7.1 Path B).

    MED-3: source='auto:*' now requires metadata.created_by_attempt_id pointing
    to a running attempt owned by the caller. ra_111 belongs to 张三 and is
    running in the seeded_reference state.
    """
    payload = _wi_payload(goal="OAuth refresh 401")
    payload["source"] = "auto:debug"
    payload["metadata"] = {"created_by_attempt_id": "ra_111",
                           "parent_work_item_id": "wi_a3f"}
    async with make_async_client(seeded_reference) as client:
        r = await client.post(
            "/v1/work_items", json=payload, headers=auth_headers(BEARER_ZHANG),
        )
    assert r.status_code == 201
    body = r.json()
    assert body["metadata"]["source"] == "auto:debug"


# ---- GET /v1/work_items ----

async def test_list_work_items_filter_project(seeded_reference):
    async with make_async_client(seeded_reference) as client:
        r = await client.get(
            "/v1/work_items?project=marketplace",
            headers=auth_headers(BEARER_ZHANG),
        )
    assert r.status_code == 200
    body = r.json()
    assert "items" in body and "next_cursor" in body
    ids = {i["id"] for i in body["items"]}
    assert "wi_a3f" in ids


async def test_list_work_items_filter_label(seeded_reference):
    async with make_async_client(seeded_reference) as client:
        r = await client.get(
            "/v1/work_items?label=bug", headers=auth_headers(BEARER_ZHANG),
        )
    assert r.status_code == 200
    body = r.json()
    assert any(i["id"] == "wi_a3f" for i in body["items"])


async def test_list_work_items_filter_status(seeded_reference):
    async with make_async_client(seeded_reference) as client:
        r = await client.get(
            "/v1/work_items?status=running",
            headers=auth_headers(BEARER_ZHANG),
        )
    assert r.status_code == 200
    assert all(i["status"] == "running" for i in r.json()["items"])


async def test_list_work_items_pagination(seeded_users):
    """Create 3 work_items, fetch with limit=2 → cursor; fetch second page."""
    async with make_async_client(seeded_users) as client:
        for i in range(3):
            await client.post(
                "/v1/work_items",
                json=_wi_payload(goal=f"task {i}"),
                headers=auth_headers(BEARER_ZHANG),
            )
        r1 = await client.get("/v1/work_items?limit=2",
                              headers=auth_headers(BEARER_ZHANG))
        assert r1.status_code == 200
        body1 = r1.json()
        assert len(body1["items"]) == 2
        assert body1["next_cursor"] is not None

        r2 = await client.get(
            "/v1/work_items", params={"limit": 2, "cursor": body1["next_cursor"]},
            headers=auth_headers(BEARER_ZHANG),
        )
        body2 = r2.json()
        assert len(body2["items"]) == 1
        assert body2["next_cursor"] is None


async def test_list_work_items_no_project_restricts_to_user_projects(seeded_users):
    """王五 only in marketplace; listing should not show aihub items."""
    async with make_async_client(seeded_users) as client:
        # 张三 creates aihub item
        await client.post(
            "/v1/work_items",
            json=_wi_payload(project="aihub", goal="aihub work"),
            headers=auth_headers(BEARER_ZHANG),
        )
        await client.post(
            "/v1/work_items",
            json=_wi_payload(project="marketplace", goal="mp work"),
            headers=auth_headers(BEARER_ZHANG),
        )
        r = await client.get("/v1/work_items", headers=auth_headers(BEARER_WANG))
    assert r.status_code == 200
    body = r.json()
    projects = {i["project"] for i in body["items"]}
    assert projects <= {"marketplace"}


# ---- GET /v1/work_items/{id} ----

async def test_get_work_item_detail(seeded_reference):
    async with make_async_client(seeded_reference) as client:
        r = await client.get(
            "/v1/work_items/wi_a3f", headers=auth_headers(BEARER_ZHANG),
        )
    assert r.status_code == 200
    body = r.json()
    assert body["work_item"]["id"] == "wi_a3f"
    assert body["current_attempt"] is not None
    assert body["current_attempt"]["id"] == "ra_111"
    assert len(body["recent_events"]) >= 2
    assert len(body["per_repo_state"]) == 2
    states = {p["uri"]: p["state"] for p in body["per_repo_state"]}
    assert states["repo:marketplace"] == "prepared"


async def test_get_work_item_detail_404(seeded_reference):
    async with make_async_client(seeded_reference) as client:
        r = await client.get(
            "/v1/work_items/wi_nonexistent",
            headers=auth_headers(BEARER_ZHANG),
        )
    assert r.status_code == 404


async def test_get_work_item_detail_forbidden(seeded_reference):
    """王五 not in aihub; create an aihub wi, then 403 on get."""
    async with make_async_client(seeded_reference) as client:
        r0 = await client.post(
            "/v1/work_items",
            json=_wi_payload(project="aihub", goal="aihub work"),
            headers=auth_headers(BEARER_ZHANG),
        )
        wi_id = r0.json()["id"]
        r = await client.get(
            f"/v1/work_items/{wi_id}", headers=auth_headers(BEARER_WANG),
        )
    assert r.status_code == 403


# ---- MED-3: source='auto:*' fencing (§7.1 Path B) ----

async def test_auto_source_without_attempt_id_returns_400(seeded_users):
    """MED-3: source='auto:*' with no metadata.created_by_attempt_id → 400 BAD_REQUEST."""
    payload = _wi_payload(goal="auto without attempt")
    payload["source"] = "auto:debug"
    # No created_by_attempt_id in metadata
    async with make_async_client(seeded_users) as client:
        r = await client.post(
            "/v1/work_items", json=payload, headers=auth_headers(BEARER_ZHANG),
        )
    assert r.status_code == 400
    body = r.json()
    assert body["code"] == "BAD_REQUEST"


async def test_auto_source_with_nonexistent_attempt_returns_403(seeded_users):
    """MED-3: source='auto:*' with a non-existent attempt_id → 403 FORBIDDEN."""
    payload = _wi_payload(goal="auto with bad attempt")
    payload["source"] = "auto:execute"
    payload["metadata"] = {"created_by_attempt_id": "ra_nonexistent"}
    async with make_async_client(seeded_users) as client:
        r = await client.post(
            "/v1/work_items", json=payload, headers=auth_headers(BEARER_ZHANG),
        )
    assert r.status_code == 403
    assert r.json()["code"] == "FORBIDDEN"


async def test_auto_source_with_running_attempt_succeeds(seeded_reference):
    """MED-3 happy path: source='auto:*' with a valid running attempt owned by
    the caller → 201. Confirms Path B is usable from within an active attempt.

    ra_111 is running and owned by 张三 in seeded_reference.
    """
    payload = _wi_payload(goal="auto happy path")
    payload["source"] = "auto:execute"
    payload["metadata"] = {"created_by_attempt_id": "ra_111",
                           "parent_work_item_id": "wi_a3f"}
    async with make_async_client(seeded_reference) as client:
        r = await client.post(
            "/v1/work_items", json=payload, headers=auth_headers(BEARER_ZHANG),
        )
    assert r.status_code == 201
    body = r.json()
    assert body["metadata"]["source"] == "auto:execute"


async def test_sync_source_rejected_for_writer(seeded_users):
    """MED-3: source='sync:*' from a non-admin writer → 403 FORBIDDEN."""
    payload = _wi_payload(goal="sync source test")
    payload["source"] = "sync:jira"
    async with make_async_client(seeded_users) as client:
        r = await client.post(
            "/v1/work_items", json=payload, headers=auth_headers(BEARER_ZHANG),
        )
    assert r.status_code == 403
    assert r.json()["code"] == "FORBIDDEN"
