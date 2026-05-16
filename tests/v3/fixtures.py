"""Reference users — 张三 / 李四 / 王五 + admin。v3-reference-scenario §0。"""
import json
import sqlalchemy as sa


# 每个 reference user 都带 1 个 api_key entry, 跟 v3-reference-scenario §1 起的
# seed scenario 里 ra_*** 引用的 api_key_id 对得上 (例如 ak_zhang_001)。
REFERENCE_USERS = [
    {
        "id": "u_zhangsan", "email": "zhang@gmi.local", "display_name": "张三",
        "role": "writer", "projects": ["marketplace", "aihub", "ieops"],
        "api_keys": [{
            "id": "ak_zhang_001",
            "key_hash": "argon2id$dummy_seed_hash_zhang",
            "scopes": ["read:marketplace", "write:marketplace",
                       "read:aihub", "write:aihub",
                       "read:ieops", "write:ieops"],
            "created_at": "2026-05-16T09:00:00Z",
            "revoked_at": None,
        }],
    },
    {
        "id": "u_lisi", "email": "li@gmi.local", "display_name": "李四",
        "role": "writer", "projects": ["marketplace", "aihub"],
        "api_keys": [{
            "id": "ak_li_001",
            "key_hash": "argon2id$dummy_seed_hash_li",
            "scopes": ["read:marketplace", "write:marketplace",
                       "read:aihub", "write:aihub"],
            "created_at": "2026-05-16T09:00:00Z",
            "revoked_at": None,
        }],
    },
    {
        "id": "u_wangwu", "email": "wang@gmi.local", "display_name": "王五",
        "role": "writer", "projects": ["marketplace"],
        "api_keys": [{
            "id": "ak_wang_001",
            "key_hash": "argon2id$dummy_seed_hash_wang",
            "scopes": ["read:marketplace", "write:marketplace"],
            "created_at": "2026-05-16T09:00:00Z",
            "revoked_at": None,
        }],
    },
]


async def insert_reference_users(conn):
    for u in REFERENCE_USERS:
        await conn.execute(sa.text("""
            INSERT INTO users (id, email, display_name, role, projects, api_keys)
            VALUES (:id, :email, :display_name, :role,
                    CAST(:projects AS JSONB), CAST(:api_keys AS JSONB))
        """), {
            **u,
            "projects": json.dumps(u["projects"]),
            "api_keys": json.dumps(u["api_keys"]),
        })
