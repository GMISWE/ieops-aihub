"""s1 — app/auth.py: find_user_by_api_key + verify_bearer + verify_mutation."""
from __future__ import annotations

import hashlib
import pytest
import sqlalchemy as sa

from app.auth import (
    extract_bearer,
    find_user_by_api_key,
    verify_bearer,
    verify_mutation,
    _hash_session_secret,
)
from app.errors import ErrorCode, AihubServerError


pytestmark = pytest.mark.asyncio(loop_scope="session")


# ---- extract_bearer ----

def test_extract_bearer_normal():
    assert extract_bearer("Bearer aihub_sk_abc") == "aihub_sk_abc"


def test_extract_bearer_case():
    assert extract_bearer("bearer xyz") == "xyz"


def test_extract_bearer_missing():
    assert extract_bearer(None) is None
    assert extract_bearer("") is None
    assert extract_bearer("Token xyz") is None


# ---- find_user_by_api_key ----

async def test_find_user_by_api_key_match(seeded_users):
    """Seed user u_zhangsan has key_hash='argon2id$dummy_seed_hash_zhang'.
    bearer == stored_hash literal matches in 1A test convention.
    """
    async with seeded_users.connect() as conn:
        u = await find_user_by_api_key(conn, "argon2id$dummy_seed_hash_zhang")
    assert u is not None
    assert u.id == "u_zhangsan"
    assert u.matched_api_key_id == "ak_zhang_001"


async def test_find_user_by_api_key_no_match(seeded_users):
    async with seeded_users.connect() as conn:
        u = await find_user_by_api_key(conn, "argon2id$nonexistent")
    assert u is None


async def test_find_user_by_api_key_empty(seeded_users):
    async with seeded_users.connect() as conn:
        assert await find_user_by_api_key(conn, "") is None


async def test_find_user_skips_revoked(seeded_users):
    """Revoke u_lisi's key; lookup should fail."""
    async with seeded_users.connect() as conn:
        await conn.execute(sa.text("""
            UPDATE users SET api_keys = jsonb_set(
                api_keys, '{0,revoked_at}', '"2026-05-16T10:00:00Z"'::jsonb
            ) WHERE id = 'u_lisi'
        """))
        await conn.commit()
        u = await find_user_by_api_key(conn, "argon2id$dummy_seed_hash_li")
    assert u is None


# ---- verify_bearer ----

async def test_verify_bearer_happy(seeded_users):
    async with seeded_users.connect() as conn:
        u = await verify_bearer(conn, "argon2id$dummy_seed_hash_zhang")
    assert u.id == "u_zhangsan"


async def test_verify_bearer_missing(seeded_users):
    async with seeded_users.connect() as conn:
        with pytest.raises(AihubServerError) as exc:
            await verify_bearer(conn, None)
    assert exc.value.code == ErrorCode.UNAUTHORIZED


async def test_verify_bearer_unknown_key(seeded_users):
    async with seeded_users.connect() as conn:
        with pytest.raises(AihubServerError) as exc:
            await verify_bearer(conn, "argon2id$wrong")
    assert exc.value.code == ErrorCode.UNAUTHORIZED


# ---- verify_mutation ----

async def _setup_real_attempt(conn, raw_secret: str):
    """Replace seed dummy hash with a real sha256 hash of a known raw_secret."""
    h = _hash_session_secret(raw_secret)
    await conn.execute(sa.text("""
        UPDATE run_attempts SET session_secret_hash = :h WHERE id = 'ra_111'
    """), {"h": h})
    await conn.commit()


# Use 64-hex secret
RAW = "a" * 64


async def test_verify_mutation_happy(seeded_reference):
    async with seeded_reference.connect() as conn:
        await _setup_real_attempt(conn, RAW)
        u, a = await verify_mutation(
            conn, "argon2id$dummy_seed_hash_zhang",
            attempt_id="ra_111", claim_epoch=1, session_secret=RAW,
        )
    assert u.id == "u_zhangsan"
    assert a.id == "ra_111"
    assert a.claim_epoch == 1


async def test_verify_mutation_wrong_secret(seeded_reference):
    async with seeded_reference.connect() as conn:
        await _setup_real_attempt(conn, RAW)
        with pytest.raises(AihubServerError) as exc:
            await verify_mutation(
                conn, "argon2id$dummy_seed_hash_zhang",
                attempt_id="ra_111", claim_epoch=1, session_secret="b" * 64,
            )
    assert exc.value.code == ErrorCode.UNAUTHORIZED


async def test_verify_mutation_epoch_mismatch(seeded_reference):
    async with seeded_reference.connect() as conn:
        await _setup_real_attempt(conn, RAW)
        with pytest.raises(AihubServerError) as exc:
            await verify_mutation(
                conn, "argon2id$dummy_seed_hash_zhang",
                attempt_id="ra_111", claim_epoch=2, session_secret=RAW,
            )
    assert exc.value.code == ErrorCode.CONFLICT_EPOCH_MISMATCH


async def test_verify_mutation_wrong_user(seeded_reference):
    async with seeded_reference.connect() as conn:
        await _setup_real_attempt(conn, RAW)
        with pytest.raises(AihubServerError) as exc:
            await verify_mutation(
                conn, "argon2id$dummy_seed_hash_li",
                attempt_id="ra_111", claim_epoch=1, session_secret=RAW,
            )
    assert exc.value.code == ErrorCode.FORBIDDEN


async def test_verify_mutation_attempt_not_active(seeded_reference):
    async with seeded_reference.connect() as conn:
        await _setup_real_attempt(conn, RAW)
        # Mark superseded
        await conn.execute(sa.text(
            "UPDATE run_attempts SET status='superseded' WHERE id='ra_111'"
        ))
        await conn.commit()
        with pytest.raises(AihubServerError) as exc:
            await verify_mutation(
                conn, "argon2id$dummy_seed_hash_zhang",
                attempt_id="ra_111", claim_epoch=1, session_secret=RAW,
            )
    assert exc.value.code == ErrorCode.CONFLICT_LEASE_EXPIRED


async def test_verify_mutation_lease_expired(seeded_reference):
    async with seeded_reference.connect() as conn:
        await _setup_real_attempt(conn, RAW)
        # Force lease into the past
        await conn.execute(sa.text(
            "UPDATE run_attempts SET lease_until = now() - interval '1 second' WHERE id='ra_111'"
        ))
        await conn.commit()
        with pytest.raises(AihubServerError) as exc:
            await verify_mutation(
                conn, "argon2id$dummy_seed_hash_zhang",
                attempt_id="ra_111", claim_epoch=1, session_secret=RAW,
            )
    assert exc.value.code == ErrorCode.CONFLICT_LEASE_EXPIRED


async def test_verify_mutation_unknown_attempt(seeded_reference):
    async with seeded_reference.connect() as conn:
        with pytest.raises(AihubServerError) as exc:
            await verify_mutation(
                conn, "argon2id$dummy_seed_hash_zhang",
                attempt_id="ra_nonexistent", claim_epoch=1, session_secret=RAW,
            )
    assert exc.value.code == ErrorCode.CONFLICT_LEASE_EXPIRED


async def test_verify_mutation_revoked_key(seeded_reference):
    async with seeded_reference.connect() as conn:
        await _setup_real_attempt(conn, RAW)
        await conn.execute(sa.text("""
            UPDATE users SET api_keys = jsonb_set(
                api_keys, '{0,revoked_at}', '"2026-05-16T10:00:00Z"'::jsonb
            ) WHERE id = 'u_zhangsan'
        """))
        await conn.commit()
        with pytest.raises(AihubServerError) as exc:
            await verify_mutation(
                conn, "argon2id$dummy_seed_hash_zhang",
                attempt_id="ra_111", claim_epoch=1, session_secret=RAW,
            )
    assert exc.value.code == ErrorCode.UNAUTHORIZED


def test_hash_session_secret_deterministic():
    h1 = _hash_session_secret(RAW)
    h2 = _hash_session_secret(RAW)
    assert h1 == h2
    assert len(h1) == 64
    assert all(c in "0123456789abcdef" for c in h1)
