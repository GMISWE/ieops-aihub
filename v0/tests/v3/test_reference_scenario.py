"""s10 — End-to-end reference scenario §1 drive via FastAPI TestClient.

Walks the main scenario:
  09:00 张三 POST work_items → wi queued → POST claim → ra running
  10:00 李四 POST conflicts/predict → soft_block with rule_id=same_resource_live_write
  11:00 张三 events (commit/push/pr) + complete → wrapped, locks released
  14:00 ra_111 lease forced past, 李四 renew with old epoch → 409 LEASE_EXPIRED
  14:15 王五 POST claim → takeover, epoch=2, parent_attempt_id=ra_111
"""
from __future__ import annotations

import pytest
import sqlalchemy as sa

from app.auth import _hash_session_secret
from tests.v3.v3_client import (
    BEARER_LI, BEARER_WANG, BEARER_ZHANG, auth_headers, make_async_client,
)


pytestmark = pytest.mark.asyncio(loop_scope="session")


SECRET_ZHANG = "a" * 64
SECRET_LI = "b" * 64
SECRET_WANG = "c" * 64


async def test_reference_scenario_end_to_end(seeded_users):
    """Drive §1 main scenario through every endpoint, asserting state at each step."""

    async with make_async_client(seeded_users) as client:
        # ============ 09:00 张三 创建 + claim wi_a3f ============
        r0 = await client.post(
            "/v1/work_items",
            json={
                "project": "marketplace", "scenario": "coding",
                "goal": "修 /login 500",
                "declared_resources": [
                    {"type": "repo", "uri": "repo:marketplace", "intent": "write",
                     "base_branch": "main", "task_branch": "polyforge/wi_a3f"},
                    {"type": "path", "uri": "file:marketplace/src/auth/**",
                     "intent": "write"},
                ],
                "labels": ["bug"], "priority": "high", "source": "human",
            },
            headers=auth_headers(BEARER_ZHANG),
        )
        assert r0.status_code == 201, r0.text
        wi_id = r0.json()["id"]
        assert r0.json()["status"] == "queued"

        # Pre-claim predict — should be no conflicts
        rp0 = await client.post(
            "/v1/conflicts/predict",
            json={"project": "marketplace",
                  "declared_resources": [
                      {"type": "repo", "uri": "repo:marketplace",
                       "intent": "write", "task_branch": "polyforge/wi_a3f"},
                      {"type": "path", "uri": "file:marketplace/src/auth/**",
                       "intent": "write"},
                  ]},
            headers=auth_headers(BEARER_ZHANG),
        )
        assert rp0.status_code == 200
        assert rp0.json()["severity"] == "info"

        # Claim
        rc0 = await client.post(
            f"/v1/work_items/{wi_id}/claim",
            json={
                "idempotency_key": "idem001",
                "session_info": {"machine_id": "zhang-mbp",
                                  "session_secret": SECRET_ZHANG},
                "requested_locks": [
                    {"resource_type": "git_branch",
                     "resource_key": f"marketplace/polyforge/{wi_id}"},
                    {"resource_type": "worktree",
                     "resource_key": f"zhang-mbp/marketplace/{wi_id}"},
                ],
            },
            headers=auth_headers(BEARER_ZHANG),
        )
        assert rc0.status_code == 200, rc0.text
        ra_zhang = rc0.json()["attempt_id"]
        assert rc0.json()["claim_epoch"] == 1

        # ============ 10:00 李四 预测 soft_block (Rule 1) ============
        rp_li = await client.post(
            "/v1/conflicts/predict",
            json={"project": "marketplace",
                  "declared_resources": [
                      {"type": "path", "uri": "file:marketplace/src/auth/**",
                       "intent": "refactor"},
                  ]},
            headers=auth_headers(BEARER_LI),
        )
        assert rp_li.status_code == 200
        body_p = rp_li.json()
        assert body_p["severity"] == "soft_block"
        rule_ids = {p["rule_id"] for p in body_p["predictions"]}
        assert "same_resource_live_write" in rule_ids
        actor_displays = {p["conflicts_with"]["actor_display"]
                          for p in body_p["predictions"]
                          if p["rule_id"] == "same_resource_live_write"}
        assert "张三" in actor_displays

        # ============ 11:00 张三 emit commit + push + pr events ============
        for evt in [
            ("commit", {"repo": "marketplace", "sha": "4f2a8b",
                        "files": ["src/auth/login.py"]}),
            ("push", {"repo": "marketplace", "base_sha_at_push": "main_sha"}),
            ("pr_opened", {"pr_number": 1234, "repo": "marketplace"}),
        ]:
            etype, payload = evt
            r_ev = await client.post(
                "/v1/events",
                json={"attempt_id": ra_zhang, "claim_epoch": 1,
                      "session_secret": SECRET_ZHANG,
                      "work_item_id": wi_id, "event_type": etype,
                      "payload": payload},
                headers=auth_headers(BEARER_ZHANG),
            )
            assert r_ev.status_code == 201

        # Complete attempt → work_item wraps
        r_complete = await client.post(
            f"/v1/work_items/{wi_id}/complete",
            json={"attempt_id": ra_zhang, "claim_epoch": 1,
                  "session_secret": SECRET_ZHANG, "final_status": "wrapped"},
            headers=auth_headers(BEARER_ZHANG),
        )
        assert r_complete.status_code == 200, r_complete.text

        # Verify state: wi wrapped + locks released
        async with seeded_users.connect() as conn:
            wi_status = (await conn.execute(sa.text(
                "SELECT status FROM work_items WHERE id = :id"
            ), {"id": wi_id})).scalar()
            assert wi_status == "wrapped"
            locks = (await conn.execute(sa.text(
                "SELECT COUNT(*) FROM resource_locks WHERE owner_attempt_id = :aid"
            ), {"aid": ra_zhang})).scalar()
            assert locks == 0
            ev_types = {r[0] for r in (await conn.execute(sa.text(
                "SELECT event_type FROM agent_events WHERE work_item_id = :id"
            ), {"id": wi_id}))}
            assert {"commit", "push", "pr_opened", "work_item_completed",
                    "attempt_completed"} <= ev_types

        # ============ Sub-scenario: takeover (14:00 + 14:15) on a fresh wi ============
        # Create a new work_item and claim by 李四 simulating 14:00
        r1 = await client.post(
            "/v1/work_items",
            json={"project": "marketplace", "scenario": "coding",
                  "goal": "refactor auth middleware",
                  "declared_resources": [
                      {"type": "repo", "uri": "repo:marketplace",
                       "intent": "refactor", "base_branch": "main",
                       "task_branch": "polyforge/wi_c2f"},
                  ],
                  "labels": ["refactor"], "priority": "normal",
                  "source": "human"},
            headers=auth_headers(BEARER_LI),
        )
        wi_c2f = r1.json()["id"]
        rc1 = await client.post(
            f"/v1/work_items/{wi_c2f}/claim",
            json={"idempotency_key": "idem002_li",
                  "session_info": {"machine_id": "li-mbp",
                                    "session_secret": SECRET_LI},
                  "requested_locks": [
                      {"resource_type": "git_branch",
                       "resource_key": f"marketplace/polyforge/{wi_c2f}"},
                  ]},
            headers=auth_headers(BEARER_LI),
        )
        assert rc1.status_code == 200
        ra_li = rc1.json()["attempt_id"]

        # 14:00 — wifi 断了; lease 进入过去
        async with seeded_users.connect() as conn:
            await conn.execute(sa.text("""
                UPDATE run_attempts SET lease_until = now() - interval '1 minute'
                WHERE id = :aid
            """), {"aid": ra_li})
            await conn.commit()

        # 14:00 — 李四 tries to renew with old epoch → 409 LEASE_EXPIRED
        rl = await client.post(
            f"/v1/attempts/{ra_li}/lease",
            json={"claim_epoch": 1, "session_secret": SECRET_LI},
            headers=auth_headers(BEARER_LI),
        )
        assert rl.status_code == 409
        assert rl.json()["code"] == "CONFLICT_LEASE_EXPIRED"

        # 14:15 — 王五 (wang-mbp) takeover. But wi_c2f.project=marketplace, 王五 OK.
        rc2 = await client.post(
            f"/v1/work_items/{wi_c2f}/claim",
            json={"idempotency_key": "idem003_wang",
                  "session_info": {"machine_id": "wang-mbp",
                                    "session_secret": SECRET_WANG},
                  "requested_locks": [
                      {"resource_type": "git_branch",
                       "resource_key": f"marketplace/polyforge/{wi_c2f}"},
                  ]},
            headers=auth_headers(BEARER_WANG),
        )
        assert rc2.status_code == 200, rc2.text
        ra_wang = rc2.json()["attempt_id"]
        assert rc2.json()["claim_epoch"] == 2

        # Verify takeover side effects
        async with seeded_users.connect() as conn:
            old = (await conn.execute(sa.text(
                "SELECT status FROM run_attempts WHERE id = :aid"
            ), {"aid": ra_li})).scalar()
            assert old == "superseded"
            new = (await conn.execute(sa.text("""
                SELECT parent_attempt_id, claim_epoch
                FROM run_attempts WHERE id = :aid
            """), {"aid": ra_wang})).first()
            assert new == (ra_li, 2)
            taken_over_ev = (await conn.execute(sa.text("""
                SELECT payload FROM agent_events
                WHERE work_item_id = :wi AND event_type = 'attempt_taken_over'
            """), {"wi": wi_c2f})).mappings().first()
            assert taken_over_ev is not None
            assert taken_over_ev["payload"]["from_attempt_id"] == ra_li
            assert taken_over_ev["payload"]["to_attempt_id"] == ra_wang
