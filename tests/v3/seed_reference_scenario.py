"""Reference scenario §1 09:00 张三 wi_a3f / ra_111 / 2 locks 状态。"""
import json
import sqlalchemy as sa


async def seed_09_00_state(conn):
    """v3-reference-scenario §1 09:00 — 张三 claim wi_a3f 后的状态。"""
    # 09:00 state declared_resources — intent-time + runtime 字段混存同一 JSONB 元素 (§5.1)
    declared = [
        {
            # intent-time
            "type": "repo", "uri": "repo:marketplace", "intent": "write",
            "base_branch": "main", "task_branch": "polyforge/wi_a3f",
            # runtime (per §7.6, 由 server 维护 — seed 模拟刚 prepare 完的状态)
            "state": "prepared", "version": 1,
        },
        {
            # path 类型 — intent-time + runtime; version=1 跟 repo item 对齐 (CAS 起始值)
            "type": "path", "uri": "file:marketplace/src/auth/**", "intent": "write",
            "state": "prepared", "version": 1,
        },
    ]

    # Insert work_item with current_attempt_id=NULL first (FK is DEFERRABLE INITIALLY DEFERRED)
    await conn.execute(sa.text("""
        INSERT INTO work_items (id, project, scenario, goal, status, priority,
                                labels, reporter_user_id, declared_resources,
                                resources_version, metadata)
        VALUES ('wi_a3f', 'marketplace', 'coding',
                '修 marketplace /login 500 错误',
                'running', 'high',
                CAST(:labels AS JSONB), 'u_zhangsan',
                CAST(:declared AS JSONB), 1,
                CAST(:metadata AS JSONB))
    """), {
        "labels": json.dumps(["bug"]),
        "declared": json.dumps(declared),
        "metadata": json.dumps({"source": "human"}),
    })

    await conn.execute(sa.text("""
        INSERT INTO run_attempts (
            id, work_item_id, status, claim_epoch, idempotency_key,
            lease_until,
            actor_user_id, api_key_id, actor_display, machine_id,
            session_secret_hash
        )
        VALUES ('ra_111', 'wi_a3f', 'running', 1, 'idem_001',
                now() + interval '60 seconds',
                'u_zhangsan', 'ak_zhang_001', '张三', 'zhang-mbp',
                'sha256_dummy_hash_for_seed')
    """))

    await conn.execute(sa.text("""
        UPDATE work_items SET current_attempt_id = 'ra_111' WHERE id = 'wi_a3f'
    """))

    locks = [
        ("git_branch", "marketplace/polyforge/wi_a3f"),
        ("worktree", "zhang-mbp/marketplace/wi_a3f"),
    ]
    for resource_type, resource_key in locks:
        await conn.execute(sa.text("""
            INSERT INTO resource_locks (resource_type, resource_key, owner_attempt_id, claim_epoch)
            VALUES (:rt, :rk, 'ra_111', 1)
        """), {"rt": resource_type, "rk": resource_key})

    # events
    await conn.execute(sa.text("""
        INSERT INTO agent_events (id, work_item_id, run_attempt_id, actor_user_id,
                                  event_type, payload)
        VALUES
          ('evt_001', 'wi_a3f', null,      'u_zhangsan', 'work_item_filed',
           CAST(:p1 AS JSONB)),
          ('evt_002', 'wi_a3f', 'ra_111',  'u_zhangsan', 'attempt_started',
           CAST(:p2 AS JSONB))
    """), {
        "p1": json.dumps({"work_item_id": "wi_a3f", "project": "marketplace",
                           "source": "human", "goal": "修 /login 500"}),
        "p2": json.dumps({"claim_epoch": 1, "is_takeover": False}),
    })
