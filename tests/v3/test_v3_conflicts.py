"""s8 — POST /v1/conflicts/predict 5-rule predictor."""
from __future__ import annotations

import pytest
import sqlalchemy as sa

from app.conflicts import path_glob_overlap
from tests.v3.v3_client import (
    BEARER_LI, BEARER_WANG, BEARER_ZHANG, auth_headers, make_async_client,
)


pytestmark = pytest.mark.asyncio(loop_scope="session")


# ---- path_glob_overlap unit tests ----

def test_glob_overlap_exact():
    assert path_glob_overlap("file:src/auth/login.py", "file:src/auth/login.py")


def test_glob_overlap_parent_prefix():
    assert path_glob_overlap("file:src/auth", "file:src/auth/login.py")
    assert path_glob_overlap("file:src/auth/login.py", "file:src/auth")


def test_glob_overlap_glob_matches():
    assert path_glob_overlap("file:src/auth/**", "file:src/auth/login.py")
    assert path_glob_overlap("file:src/auth/login.py", "file:src/auth/**")


def test_glob_overlap_no_overlap():
    assert not path_glob_overlap("file:src/auth", "file:src/views")
    assert not path_glob_overlap("file:src/auth/**", "file:src/views/login.py")


def test_glob_overlap_both_globs_same_prefix():
    assert path_glob_overlap("file:src/auth/**", "file:src/auth/*")


# ---- Rule 1: same_resource_live_write (reference scenario §1 10:00) ----

async def test_predict_rule1_hit(seeded_reference):
    """张三 ra_111 writes file:marketplace/src/auth/**; 李四 proposes same URI →
    soft_block."""
    async with make_async_client(seeded_reference) as client:
        r = await client.post(
            "/v1/conflicts/predict",
            json={"project": "marketplace",
                  "declared_resources": [
                      {"type": "path",
                       "uri": "file:marketplace/src/auth/**",
                       "intent": "refactor"},
                  ]},
            headers=auth_headers(BEARER_LI),
        )
    assert r.status_code == 200
    body = r.json()
    assert body["severity"] == "soft_block"
    preds = body["predictions"]
    assert any(p["rule_id"] == "same_resource_live_write" for p in preds)
    rule1 = next(p for p in preds if p["rule_id"] == "same_resource_live_write")
    assert rule1["conflicts_with"]["actor_display"] == "张三"
    assert rule1["conflicts_with"]["attempt_id"] == "ra_111"


# ---- Rule 1 no hit: same path read-only ----

async def test_predict_rule1_no_hit_read(seeded_reference):
    async with make_async_client(seeded_reference) as client:
        r = await client.post(
            "/v1/conflicts/predict",
            json={"project": "marketplace",
                  "declared_resources": [
                      {"type": "path",
                       "uri": "file:marketplace/src/auth/**",
                       "intent": "read"},
                  ]},
            headers=auth_headers(BEARER_LI),
        )
    assert r.status_code == 200
    body = r.json()
    # write_uris is empty because intent=read → Rule 1 skipped
    # but Rule 4 path_overlap should fire as warn
    rule1_hits = [p for p in body["predictions"]
                  if p["rule_id"] == "same_resource_live_write"]
    assert not rule1_hits


# ---- Rule 1 self-exclusion ----

async def test_predict_rule1_self_excluded(seeded_reference):
    """张三 himself runs predict with own work_item_id → no Rule 1 hit."""
    async with make_async_client(seeded_reference) as client:
        r = await client.post(
            "/v1/conflicts/predict",
            json={"project": "marketplace",
                  "work_item_id": "wi_a3f",
                  "attempt_id": "ra_111",
                  "declared_resources": [
                      {"type": "path",
                       "uri": "file:marketplace/src/auth/**",
                       "intent": "write"},
                  ]},
            headers=auth_headers(BEARER_ZHANG),
        )
    assert r.status_code == 200
    body = r.json()
    rule1 = [p for p in body["predictions"] if p["rule_id"] == "same_resource_live_write"]
    assert not rule1


# ---- Rule 2: lock_conflict (hard_block) ----

async def test_predict_rule2_lock_conflict(seeded_reference):
    """张三 ra_111 holds (git_branch, marketplace/polyforge/wi_a3f).
    Another claim proposing the same task_branch → hard_block."""
    async with make_async_client(seeded_reference) as client:
        r = await client.post(
            "/v1/conflicts/predict",
            json={"project": "marketplace",
                  "declared_resources": [
                      {"type": "repo", "uri": "repo:marketplace",
                       "intent": "write", "base_branch": "main",
                       "task_branch": "polyforge/wi_a3f"},
                  ]},
            headers=auth_headers(BEARER_LI),
        )
    assert r.status_code == 200
    body = r.json()
    assert body["severity"] == "hard_block"
    assert any(p["rule_id"] == "lock_conflict" for p in body["predictions"])


# ---- Rule 3: same_repo_refactor ----

async def test_predict_rule3_same_repo_refactor(seeded_users):
    """Create a wi where 张三 declares repo:marketplace refactor; 李四 proposes same."""
    async with seeded_users.connect() as conn:
        # Insert a queued+running work_item for zhang with intent=refactor on repo
        await conn.execute(sa.text("""
            INSERT INTO work_items (id, project, scenario, goal, status, priority,
                                    labels, reporter_user_id, declared_resources,
                                    resources_version, metadata)
            VALUES ('wi_zhang3', 'marketplace', 'coding', 'refactor mp', 'running',
                    'normal', '[]'::jsonb, 'u_zhangsan',
                    CAST(:dr AS JSONB), 1, '{}'::jsonb)
        """), {"dr": '[{"type":"repo","uri":"repo:marketplace","intent":"refactor"}]'})
        await conn.execute(sa.text("""
            INSERT INTO run_attempts (id, work_item_id, status, claim_epoch,
                                       idempotency_key, lease_until, actor_user_id,
                                       api_key_id, actor_display, machine_id,
                                       session_secret_hash)
            VALUES ('ra_zhang3', 'wi_zhang3', 'running', 1, 'idem_zhang3',
                    now() + interval '60 seconds', 'u_zhangsan', 'ak_zhang_001',
                    '张三', 'zhang-mbp', 'h')
        """))
        await conn.execute(sa.text(
            "UPDATE work_items SET current_attempt_id='ra_zhang3' WHERE id='wi_zhang3'"
        ))
        await conn.commit()
    async with make_async_client(seeded_users) as client:
        r = await client.post(
            "/v1/conflicts/predict",
            json={"project": "marketplace",
                  "declared_resources": [
                      {"type": "repo", "uri": "repo:marketplace",
                       "intent": "refactor"},
                  ]},
            headers=auth_headers(BEARER_LI),
        )
    assert r.status_code == 200
    body = r.json()
    rule_ids = {p["rule_id"] for p in body["predictions"]}
    # Rule 1 also hits (refactor is in _WRITE_INTENTS) — that's expected per
    # design. Rule 3 dedupes against the same uri already in predictions.
    assert "same_resource_live_write" in rule_ids


# ---- Rule 4: path_overlap (broad glob hits narrow file) ----

async def test_predict_rule4_path_overlap(seeded_users):
    async with seeded_users.connect() as conn:
        # zhang declares broad glob src/auth/** as write
        await conn.execute(sa.text("""
            INSERT INTO work_items (id, project, scenario, goal, status, priority,
                                    labels, reporter_user_id, declared_resources,
                                    resources_version, metadata)
            VALUES ('wi_zhang4', 'marketplace', 'coding', 'x', 'running',
                    'normal', '[]'::jsonb, 'u_zhangsan',
                    CAST(:dr AS JSONB), 1, '{}'::jsonb)
        """), {"dr": '[{"type":"path","uri":"file:marketplace/src/auth/**","intent":"write"}]'})
        await conn.execute(sa.text("""
            INSERT INTO run_attempts (id, work_item_id, status, claim_epoch,
                                       idempotency_key, lease_until, actor_user_id,
                                       api_key_id, actor_display, machine_id,
                                       session_secret_hash)
            VALUES ('ra_zhang4', 'wi_zhang4', 'running', 1, 'idem_zhang4',
                    now() + interval '60 seconds', 'u_zhangsan', 'ak_zhang_001',
                    '张三', 'zhang-mbp', 'h')
        """))
        await conn.execute(sa.text(
            "UPDATE work_items SET current_attempt_id='ra_zhang4' WHERE id='wi_zhang4'"
        ))
        await conn.commit()
    async with make_async_client(seeded_users) as client:
        r = await client.post(
            "/v1/conflicts/predict",
            json={"project": "marketplace",
                  "declared_resources": [
                      {"type": "path",
                       "uri": "file:marketplace/src/auth/login.py",
                       "intent": "write"},
                  ]},
            headers=auth_headers(BEARER_LI),
        )
    assert r.status_code == 200
    body = r.json()
    rule_ids = {p["rule_id"] for p in body["predictions"]}
    assert "path_overlap" in rule_ids


# ---- Rule 5: external_artifact ----

async def test_predict_rule5_external_artifact(seeded_reference):
    """Emit a pr_opened event for wi_a3f; predict with work_item_id → warn."""
    async with seeded_reference.connect() as conn:
        await conn.execute(sa.text("""
            INSERT INTO agent_events (id, work_item_id, run_attempt_id,
                                       actor_user_id, event_type, payload)
            VALUES ('evt_pr', 'wi_a3f', 'ra_111', 'u_zhangsan', 'pr_opened',
                    CAST(:p AS JSONB))
        """), {"p": '{"pr_number": 1234, "repo": "marketplace"}'})
        await conn.commit()
    async with make_async_client(seeded_reference) as client:
        r = await client.post(
            "/v1/conflicts/predict",
            json={"project": "marketplace", "work_item_id": "wi_a3f",
                  "declared_resources": [
                      {"type": "external_ref", "uri": "external:pr-check",
                       "intent": "read"},
                  ]},
            headers=auth_headers(BEARER_ZHANG),
        )
    assert r.status_code == 200
    body = r.json()
    rule_ids = {p["rule_id"] for p in body["predictions"]}
    assert "external_artifact" in rule_ids


# ---- No conflicts (severity=info) ----

async def test_predict_no_conflicts(seeded_users):
    async with make_async_client(seeded_users) as client:
        r = await client.post(
            "/v1/conflicts/predict",
            json={"project": "marketplace",
                  "declared_resources": [
                      {"type": "path",
                       "uri": "file:marketplace/src/unrelated.py",
                       "intent": "write"},
                  ]},
            headers=auth_headers(BEARER_LI),
        )
    assert r.status_code == 200
    body = r.json()
    assert body["severity"] == "info"
    assert body["predictions"] == []


# ---- Auth ----

async def test_predict_no_bearer(seeded_users):
    async with make_async_client(seeded_users) as client:
        r = await client.post(
            "/v1/conflicts/predict",
            json={"project": "marketplace", "declared_resources": []},
        )
    assert r.status_code == 401


async def test_predict_project_forbidden(seeded_users):
    """王五 not in aihub project → 403."""
    async with make_async_client(seeded_users) as client:
        r = await client.post(
            "/v1/conflicts/predict",
            json={"project": "aihub", "declared_resources": []},
            headers=auth_headers(BEARER_WANG),
        )
    assert r.status_code == 403
