"""s3 — GET /v1/whoami."""
from __future__ import annotations

import pytest
import sqlalchemy as sa

from tests.v3.v3_client import (
    BEARER_LI, BEARER_WANG, BEARER_ZHANG, auth_headers, make_async_client,
)


pytestmark = pytest.mark.asyncio(loop_scope="session")


async def test_whoami_happy(seeded_users):
    async with make_async_client(seeded_users) as client:
        r = await client.get("/v1/whoami", headers=auth_headers(BEARER_ZHANG))
    assert r.status_code == 200
    body = r.json()
    assert body["user_id"] == "u_zhangsan"
    assert body["role"] == "writer"
    assert "marketplace" in body["projects"]


async def test_whoami_no_bearer(seeded_users):
    async with make_async_client(seeded_users) as client:
        r = await client.get("/v1/whoami")
    assert r.status_code == 401
    assert r.json()["code"] == "UNAUTHORIZED"


async def test_whoami_revoked_key(seeded_users):
    async with seeded_users.connect() as conn:
        await conn.execute(sa.text("""
            UPDATE users SET api_keys = jsonb_set(
                api_keys, '{0,revoked_at}', '"2026-05-16T10:00:00Z"'::jsonb
            ) WHERE id = 'u_wangwu'
        """))
        await conn.commit()
    async with make_async_client(seeded_users) as client:
        r = await client.get("/v1/whoami", headers=auth_headers(BEARER_WANG))
    assert r.status_code == 401


async def test_whoami_different_users(seeded_users):
    async with make_async_client(seeded_users) as client:
        for bearer, expected in [
            (BEARER_LI, "u_lisi"),
            (BEARER_WANG, "u_wangwu"),
        ]:
            r = await client.get("/v1/whoami", headers=auth_headers(bearer))
            assert r.status_code == 200
            assert r.json()["user_id"] == expected
