"""v3 memories API tests — POST/GET/PATCH /v1/memories + POST /v1/memories/{id}/redact."""
from __future__ import annotations

import json

import pytest
import pytest_asyncio
import sqlalchemy as sa

from tests.v3.v3_client import (
    BEARER_LI, BEARER_WANG, BEARER_ZHANG, auth_headers,
)
from tests.v3.memories_client import make_memories_client as make_async_client

# Admin bearer for AH-3 tests that need admin access
BEARER_ADMIN_MEM = "argon2id$dummy_seed_hash_admin"


pytestmark = pytest.mark.asyncio(loop_scope="session")


# Admin bearer — we add api_key in a session-level fixture below.
BEARER_ADMIN = "argon2id$dummy_seed_hash_admin"


# ---------------------------------------------------------------------------
# Function-scoped fixture: ensure admin user has an API key each test
# ---------------------------------------------------------------------------

@pytest_asyncio.fixture(loop_scope="session", autouse=True)
async def _seed_admin_api_key(seeded_users):
    """Inject an api_key entry for the admin user so Bearer auth works in tests."""
    async with seeded_users.connect() as conn:
        await conn.execute(sa.text("""
            UPDATE users
            SET api_keys = CAST(:keys AS JSONB)
            WHERE id = 'u_admin'
        """), {
            "keys": json.dumps([{
                "id": "ak_admin_001",
                "key_hash": BEARER_ADMIN,
                "scopes": ["admin"],
                "created_at": "2026-05-16T09:00:00Z",
                "revoked_at": None,
            }])
        })
        await conn.commit()


# ---------------------------------------------------------------------------
# Helpers
# ---------------------------------------------------------------------------

def _mem_payload(**overrides):
    base = {
        "project": "marketplace",
        "type": "note",
        "content": "some useful memory content",
        "visibility": "project",
    }
    base.update(overrides)
    return base


async def _create_memory(client, bearer, **overrides):
    return await client.post(
        "/v1/memories",
        json=_mem_payload(**overrides),
        headers=auth_headers(bearer),
    )


# ---------------------------------------------------------------------------
# 1. create_memory happy path — project visibility
# ---------------------------------------------------------------------------

async def test_create_memory_project_visibility(seeded_users):
    async with make_async_client(seeded_users) as client:
        r = await _create_memory(client, BEARER_ZHANG, project="marketplace", visibility="project")

    assert r.status_code == 201
    body = r.json()
    assert body["id"].startswith("mem_")
    assert body["project"] == "marketplace"
    assert body["visibility"] == "project"
    assert body["author_user_id"] == "u_zhangsan"
    assert body["type"] == "note"
    assert body["content"] == "some useful memory content"
    assert body["redacted_at"] is None
    assert body["redaction_reason"] is None
    assert body["embedding"] is None
    assert "created_at" in body

    # Verify row exists in DB
    mem_id = body["id"]
    async with seeded_users.connect() as conn:
        row = (await conn.execute(sa.text(
            "SELECT id, project, author_user_id FROM memories WHERE id = :id"
        ), {"id": mem_id})).mappings().first()
    assert row is not None
    assert row["author_user_id"] == "u_zhangsan"


# ---------------------------------------------------------------------------
# 2. create_memory — private visibility
# ---------------------------------------------------------------------------

async def test_create_memory_private(seeded_users):
    async with make_async_client(seeded_users) as client:
        r = await _create_memory(client, BEARER_ZHANG, visibility="private")

    assert r.status_code == 201
    body = r.json()
    assert body["visibility"] == "private"
    assert body["author_user_id"] == "u_zhangsan"


# ---------------------------------------------------------------------------
# 3. create_memory — admin-only visibility by non-admin (allowed to CREATE)
# ---------------------------------------------------------------------------

async def test_create_memory_admin_visibility_by_non_admin(seeded_users):
    """Any authenticated user may mark their memory as admin-visibility.
    Only admins will be able to READ it, but writing is unrestricted."""
    async with make_async_client(seeded_users) as client:
        r = await _create_memory(client, BEARER_ZHANG, visibility="admin")

    assert r.status_code == 201
    assert r.json()["visibility"] == "admin"


# ---------------------------------------------------------------------------
# 4. list_memories — private filter (own private vs other's private)
# ---------------------------------------------------------------------------

async def test_list_memories_private_filter(seeded_users):
    """Zhang sees own private, NOT lisi's private."""
    async with make_async_client(seeded_users) as client:
        # Zhang creates private memory
        r = await _create_memory(client, BEARER_ZHANG, visibility="private",
                                 content="zhang private")
        zhang_priv_id = r.json()["id"]

        # Lisi creates private memory
        r = await _create_memory(client, BEARER_LI, project="aihub",
                                 visibility="private", content="li private")
        li_priv_id = r.json()["id"]

        # Zhang lists — should see own private, not lisi's
        r = await client.get(
            "/v1/memories",
            params={"visibility": "private"},
            headers=auth_headers(BEARER_ZHANG),
        )

    assert r.status_code == 200
    body = r.json()
    ids = [m["id"] for m in body["items"]]
    assert zhang_priv_id in ids
    assert li_priv_id not in ids


# ---------------------------------------------------------------------------
# 5. list_memories — project filter (project membership scoping)
# ---------------------------------------------------------------------------

async def test_list_memories_project_filter(seeded_users):
    """Zhang is in aihub, wang is NOT — zhang sees aihub project memories, wang doesn't."""
    async with make_async_client(seeded_users) as client:
        # Zhang creates project memory in aihub
        r = await _create_memory(
            client, BEARER_ZHANG,
            project="aihub", visibility="project", content="aihub project memory"
        )
        aihub_mem_id = r.json()["id"]

        # Zhang lists: sees it
        r = await client.get(
            "/v1/memories",
            params={"project": "aihub", "visibility": "project"},
            headers=auth_headers(BEARER_ZHANG),
        )
        assert r.status_code == 200
        ids = [m["id"] for m in r.json()["items"]]
        assert aihub_mem_id in ids

        # Wang lists aihub project memories: 403 or empty (wang not in aihub)
        # The visibility filter means wang can't see project='aihub' memories since
        # the visibility clause filters by user.projects. Wang sees 0 aihub results.
        r = await client.get(
            "/v1/memories",
            params={"project": "aihub"},
            headers=auth_headers(BEARER_WANG),
        )
    # Wang's request for aihub project memories should yield empty list
    # (visibility filter keeps them out; no 403 since project filter is optional)
    assert r.status_code == 200
    ids = [m["id"] for m in r.json()["items"]]
    assert aihub_mem_id not in ids


# ---------------------------------------------------------------------------
# 6. list_memories — admin-visibility only visible to admin
# ---------------------------------------------------------------------------

async def test_list_memories_admin_visibility(seeded_users):
    """Admin-visibility memories are only visible to admins in LIST."""
    async with make_async_client(seeded_users) as client:
        # Zhang creates admin-visibility memory
        r = await _create_memory(client, BEARER_ZHANG, visibility="admin",
                                 content="admin only content")
        admin_vis_id = r.json()["id"]

        # Zhang lists with visibility=admin — should NOT appear for non-admin
        r_zhang = await client.get(
            "/v1/memories",
            params={"visibility": "admin"},
            headers=auth_headers(BEARER_ZHANG),
        )
        # Admin lists — should appear
        r_admin = await client.get(
            "/v1/memories",
            params={"visibility": "admin"},
            headers=auth_headers(BEARER_ADMIN),
        )

    assert r_zhang.status_code == 200
    zhang_ids = [m["id"] for m in r_zhang.json()["items"]]
    assert admin_vis_id not in zhang_ids

    assert r_admin.status_code == 200
    admin_ids = [m["id"] for m in r_admin.json()["items"]]
    assert admin_vis_id in admin_ids


# ---------------------------------------------------------------------------
# 7. update_memory by author — succeeds
# ---------------------------------------------------------------------------

async def test_update_memory_by_author(seeded_users):
    async with make_async_client(seeded_users) as client:
        # Create
        r = await _create_memory(client, BEARER_ZHANG, content="original content")
        mem_id = r.json()["id"]

        # Update content and metadata
        r = await client.patch(
            f"/v1/memories/{mem_id}",
            json={"patch_payload": {"content": "updated content", "metadata": {"tag": "v2"}}},
            headers=auth_headers(BEARER_ZHANG),
        )

    assert r.status_code == 200
    body = r.json()
    assert body["content"] == "updated content"
    assert body["metadata"]["tag"] == "v2"
    assert body["id"] == mem_id


# ---------------------------------------------------------------------------
# 8. update_memory by non-author non-admin — 403
# ---------------------------------------------------------------------------

async def test_update_memory_by_non_author_non_admin(seeded_users):
    async with make_async_client(seeded_users) as client:
        # Zhang creates a memory
        r = await _create_memory(client, BEARER_ZHANG, content="zhang's memory")
        mem_id = r.json()["id"]

        # Lisi (neither author nor admin) tries to update
        r = await client.patch(
            f"/v1/memories/{mem_id}",
            json={"patch_payload": {"content": "hacked"}},
            headers=auth_headers(BEARER_LI),
        )

    assert r.status_code == 403
    assert r.json()["code"] == "FORBIDDEN"


# ---------------------------------------------------------------------------
# 9. redact_memory happy path — soft delete
# ---------------------------------------------------------------------------

async def test_redact_memory_happy_path(seeded_users):
    async with make_async_client(seeded_users) as client:
        # Create
        r = await _create_memory(client, BEARER_ZHANG, content="to be redacted")
        mem_id = r.json()["id"]

        # Redact
        r = await client.post(
            f"/v1/memories/{mem_id}/redact",
            json={"reason": "contains PII"},
            headers=auth_headers(BEARER_ZHANG),
        )
        assert r.status_code == 200
        assert r.json()["ok"] is True

        # Subsequent list returns the row (soft delete — row still exists)
        r = await client.get("/v1/memories", headers=auth_headers(BEARER_ZHANG))

    assert r.status_code == 200
    items = r.json()["items"]
    redacted_items = [m for m in items if m["id"] == mem_id]
    assert len(redacted_items) == 1
    m = redacted_items[0]
    assert m["redacted_at"] is not None
    assert m["redaction_reason"] == "contains PII"
    # AH-1: content must be nulled on redaction (hard redaction per design §15.1 #8)
    assert m["content"] is None


# ---------------------------------------------------------------------------
# 10. redact_memory — 404 for non-existent memory_id
# ---------------------------------------------------------------------------

async def test_redact_memory_not_found(seeded_users):
    async with make_async_client(seeded_users) as client:
        r = await client.post(
            "/v1/memories/mem_doesnotexist_00000000000000/redact",
            json={"reason": "test"},
            headers=auth_headers(BEARER_ZHANG),
        )

    assert r.status_code == 404
    assert r.json()["code"] == "NOT_FOUND"


# ---------------------------------------------------------------------------
# 11. AH-1: after redact, direct DB fetch confirms content is NULL
# ---------------------------------------------------------------------------

async def test_redact_nulls_content_in_db(seeded_users):
    """Hard redaction: content column must be NULL in DB after redact."""
    async with make_async_client(seeded_users) as client:
        r = await _create_memory(client, BEARER_ZHANG, content="sensitive data here")
        mem_id = r.json()["id"]

        r = await client.post(
            f"/v1/memories/{mem_id}/redact",
            json={"reason": "GDPR erasure"},
            headers=auth_headers(BEARER_ZHANG),
        )
        assert r.status_code == 200

    async with seeded_users.connect() as conn:
        row = (await conn.execute(sa.text(
            "SELECT content, redacted_at, redaction_reason FROM memories WHERE id = :id"
        ), {"id": mem_id})).mappings().first()

    assert row is not None
    assert row["content"] is None
    assert row["redacted_at"] is not None
    assert row["redaction_reason"] == "GDPR erasure"


# ---------------------------------------------------------------------------
# Helper: seed a work_item for AH-3 tests
# ---------------------------------------------------------------------------

async def _seed_work_item(engine, wi_id: str, project: str, reporter: str = "u_zhangsan"):
    async with engine.connect() as conn:
        existing = (await conn.execute(sa.text(
            "SELECT id FROM work_items WHERE id = :id"
        ), {"id": wi_id})).scalar_one_or_none()
        if existing is None:
            await conn.execute(sa.text("""
                INSERT INTO work_items (id, project, scenario, goal, status, reporter_user_id, labels)
                VALUES (:id, :project, 'coding', 'test work item', 'queued', :reporter, '[]'::jsonb)
            """), {"id": wi_id, "project": project, "reporter": reporter})
        await conn.commit()


# ---------------------------------------------------------------------------
# 12. AH-3: create_memory with valid work_item_id in caller's project — succeeds
# ---------------------------------------------------------------------------

async def test_create_memory_valid_work_item_id(seeded_users):
    """create_memory with a work_item_id in the caller's own project succeeds."""
    await _seed_work_item(seeded_users, "wi_ah3_valid", "marketplace")

    async with make_async_client(seeded_users) as client:
        r = await client.post(
            "/v1/memories",
            json={
                "project": "marketplace",
                "type": "note",
                "content": "linked to work item",
                "visibility": "project",
                "work_item_id": "wi_ah3_valid",
            },
            headers=auth_headers(BEARER_ZHANG),
        )

    assert r.status_code == 201, r.text
    body = r.json()
    assert body["work_item_id"] == "wi_ah3_valid"
    assert body["project"] == "marketplace"


# ---------------------------------------------------------------------------
# 13. AH-3: create_memory with work_item_id from different project — 403
# ---------------------------------------------------------------------------

async def test_create_memory_work_item_foreign_project_403(seeded_users):
    """Wang (marketplace-only) cannot attach memory to a work_item in aihub."""
    await _seed_work_item(seeded_users, "wi_ah3_aihub", "aihub")

    async with make_async_client(seeded_users) as client:
        r = await client.post(
            "/v1/memories",
            json={
                "project": "aihub",
                "type": "note",
                "content": "cross-project attempt",
                "visibility": "project",
                "work_item_id": "wi_ah3_aihub",
            },
            headers=auth_headers(BEARER_WANG),  # wang is marketplace-only
        )

    assert r.status_code == 403, r.text
    assert r.json()["code"] == "FORBIDDEN"


# ---------------------------------------------------------------------------
# 14. AH-3: create_memory with non-existent work_item_id — 404
# ---------------------------------------------------------------------------

async def test_create_memory_work_item_not_found_404(seeded_users):
    """Non-existent work_item_id must return 404, not 500."""
    async with make_async_client(seeded_users) as client:
        r = await client.post(
            "/v1/memories",
            json={
                "project": "marketplace",
                "type": "note",
                "content": "dangling reference",
                "visibility": "project",
                "work_item_id": "wi_does_not_exist_xyz",
            },
            headers=auth_headers(BEARER_ZHANG),
        )

    assert r.status_code == 404, r.text
    assert r.json()["code"] == "NOT_FOUND"


# ---------------------------------------------------------------------------
# 15. AH-3: admin can attach memory to work_item in any project — succeeds
# ---------------------------------------------------------------------------

async def test_create_memory_admin_cross_project_work_item(seeded_users):
    """Admin bypasses project membership check for work_item_id."""
    await _seed_work_item(seeded_users, "wi_ah3_admin_cross", "aihub")

    async with make_async_client(seeded_users) as client:
        r = await client.post(
            "/v1/memories",
            json={
                "project": "aihub",
                "type": "note",
                "content": "admin cross-project memory",
                "visibility": "admin",
                "work_item_id": "wi_ah3_admin_cross",
            },
            headers=auth_headers(BEARER_ADMIN_MEM),
        )

    assert r.status_code == 201, r.text
    body = r.json()
    assert body["work_item_id"] == "wi_ah3_admin_cross"
