"""s9 — POST /v1/work_items/{id}/artifacts/{adopt|ignore|close}."""
from __future__ import annotations

import pytest
import sqlalchemy as sa

from app.auth import _hash_session_secret
from tests.v3.v3_client import (
    BEARER_LI, BEARER_ZHANG, auth_headers, make_async_client,
)


pytestmark = pytest.mark.asyncio(loop_scope="session")


SECRET = "a" * 64


async def _seed_real_secret(engine, raw=SECRET):
    async with engine.connect() as conn:
        await conn.execute(sa.text(
            "UPDATE run_attempts SET session_secret_hash = :h WHERE id='ra_111'"
        ), {"h": _hash_session_secret(raw)})
        await conn.commit()


async def test_adopt_pr_happy(seeded_reference):
    await _seed_real_secret(seeded_reference)
    async with make_async_client(seeded_reference) as client:
        r = await client.post(
            "/v1/work_items/wi_a3f/artifacts/adopt",
            json={"attempt_id": "ra_111", "claim_epoch": 1,
                  "session_secret": SECRET,
                  "type": "pr", "identifier": "1234", "repo": "marketplace"},
            headers=auth_headers(BEARER_ZHANG),
        )
    assert r.status_code == 200
    async with seeded_reference.connect() as conn:
        wi = (await conn.execute(sa.text("""
            SELECT declared_resources, resources_version FROM work_items WHERE id='wi_a3f'
        """))).mappings().first()
        assert wi["resources_version"] == 2  # bumped from 1
        dr = wi["declared_resources"]
        repo_entry = next(r for r in dr if r["type"] == "repo")
        assert repo_entry["last_pr_number"] == 1234
        assert repo_entry["state"] == "pr_opened"

        ev = (await conn.execute(sa.text("""
            SELECT payload FROM agent_events
            WHERE event_type='external_artifact_reconciled' AND work_item_id='wi_a3f'
        """))).mappings().first()
        assert ev["payload"]["action"] == "adopt"


async def test_ignore_happy(seeded_reference):
    await _seed_real_secret(seeded_reference)
    async with make_async_client(seeded_reference) as client:
        r = await client.post(
            "/v1/work_items/wi_a3f/artifacts/ignore",
            json={"attempt_id": "ra_111", "claim_epoch": 1,
                  "session_secret": SECRET,
                  "type": "pr", "identifier": "1245", "repo": "marketplace"},
            headers=auth_headers(BEARER_ZHANG),
        )
    assert r.status_code == 200
    async with seeded_reference.connect() as conn:
        wi = (await conn.execute(sa.text("""
            SELECT metadata FROM work_items WHERE id='wi_a3f'
        """))).mappings().first()
        assert wi["metadata"]["ignored_artifacts"][0]["identifier"] == "1245"


async def test_close_happy(seeded_reference):
    await _seed_real_secret(seeded_reference)
    async with make_async_client(seeded_reference) as client:
        r = await client.post(
            "/v1/work_items/wi_a3f/artifacts/close",
            json={"attempt_id": "ra_111", "claim_epoch": 1,
                  "session_secret": SECRET,
                  "type": "pr", "identifier": "9999", "repo": "marketplace"},
            headers=auth_headers(BEARER_ZHANG),
        )
    assert r.status_code == 200
    async with seeded_reference.connect() as conn:
        ev = (await conn.execute(sa.text("""
            SELECT payload FROM agent_events
            WHERE event_type='external_artifact_reconciled' AND work_item_id='wi_a3f'
            ORDER BY created_at DESC LIMIT 1
        """))).mappings().first()
        assert ev["payload"]["action"] == "closed"


async def test_adopt_wrong_attempt(seeded_reference):
    """李四 attempts to adopt PR for wi_a3f (ra_111 belongs to 张三) → 403."""
    await _seed_real_secret(seeded_reference)
    async with make_async_client(seeded_reference) as client:
        r = await client.post(
            "/v1/work_items/wi_a3f/artifacts/adopt",
            json={"attempt_id": "ra_111", "claim_epoch": 1,
                  "session_secret": SECRET,
                  "type": "pr", "identifier": "1234", "repo": "marketplace"},
            headers=auth_headers(BEARER_LI),
        )
    # verify_mutation → attempt belongs to different user → 403
    assert r.status_code == 403


async def test_adopt_unknown_repo(seeded_reference):
    """work_item has only repo:marketplace; adopt against repo:other → 404."""
    await _seed_real_secret(seeded_reference)
    async with make_async_client(seeded_reference) as client:
        r = await client.post(
            "/v1/work_items/wi_a3f/artifacts/adopt",
            json={"attempt_id": "ra_111", "claim_epoch": 1,
                  "session_secret": SECRET,
                  "type": "pr", "identifier": "1", "repo": "other_repo"},
            headers=auth_headers(BEARER_ZHANG),
        )
    assert r.status_code == 404


async def test_adopt_pr_identifier_must_be_int(seeded_reference):
    await _seed_real_secret(seeded_reference)
    async with make_async_client(seeded_reference) as client:
        r = await client.post(
            "/v1/work_items/wi_a3f/artifacts/adopt",
            json={"attempt_id": "ra_111", "claim_epoch": 1,
                  "session_secret": SECRET,
                  "type": "pr", "identifier": "not-int", "repo": "marketplace"},
            headers=auth_headers(BEARER_ZHANG),
        )
    assert r.status_code == 400


# ---------------------------------------------------------------------------
# F3 (H5): adopt_artifact CAS — stale expected_resources_version → 409
# ---------------------------------------------------------------------------

async def test_adopt_artifact_cas_stale_version_409(seeded_reference):
    """F3/H5: adopt with stale expected_resources_version must return 409 CONFLICT_EPOCH_MISMATCH."""
    await _seed_real_secret(seeded_reference)

    # Fetch current resources_version
    async with seeded_reference.connect() as conn:
        row = (await conn.execute(sa.text(
            "SELECT resources_version FROM work_items WHERE id = 'wi_a3f'"
        ))).mappings().first()
    current_version = row["resources_version"]
    stale_version = current_version - 1  # intentionally stale

    async with make_async_client(seeded_reference) as client:
        r = await client.post(
            "/v1/work_items/wi_a3f/artifacts/adopt",
            json={"attempt_id": "ra_111", "claim_epoch": 1,
                  "session_secret": SECRET,
                  "type": "branch", "identifier": "pf3/test-cas", "repo": "marketplace",
                  "expected_resources_version": stale_version},
            headers=auth_headers(BEARER_ZHANG),
        )
    assert r.status_code == 409, r.text
    assert r.json()["code"] == "CONFLICT_EPOCH_MISMATCH"


async def test_adopt_artifact_cas_correct_version_succeeds(seeded_reference):
    """F3/H5: adopt with correct expected_resources_version succeeds."""
    await _seed_real_secret(seeded_reference)

    # Fetch current resources_version
    async with seeded_reference.connect() as conn:
        row = (await conn.execute(sa.text(
            "SELECT resources_version FROM work_items WHERE id = 'wi_a3f'"
        ))).mappings().first()
    current_version = row["resources_version"]

    async with make_async_client(seeded_reference) as client:
        r = await client.post(
            "/v1/work_items/wi_a3f/artifacts/adopt",
            json={"attempt_id": "ra_111", "claim_epoch": 1,
                  "session_secret": SECRET,
                  "type": "branch", "identifier": "pf3/test-cas-ok", "repo": "marketplace",
                  "expected_resources_version": current_version},
            headers=auth_headers(BEARER_ZHANG),
        )
    assert r.status_code == 200, r.text
