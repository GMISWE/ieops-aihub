"""Reference users — 张三 / 李四 / 王五 + admin。v3-reference-scenario §0。"""
import json
import sqlalchemy as sa


REFERENCE_USERS = [
    {
        "id": "u_zhangsan", "email": "zhang@gmi.local", "display_name": "张三",
        "role": "writer", "projects": ["marketplace", "aihub", "ieops"],
    },
    {
        "id": "u_lisi", "email": "li@gmi.local", "display_name": "李四",
        "role": "writer", "projects": ["marketplace", "aihub"],
    },
    {
        "id": "u_wangwu", "email": "wang@gmi.local", "display_name": "王五",
        "role": "writer", "projects": ["marketplace"],
    },
]


async def insert_reference_users(conn):
    for u in REFERENCE_USERS:
        await conn.execute(sa.text("""
            INSERT INTO users (id, email, display_name, role, projects)
            VALUES (:id, :email, :display_name, :role, CAST(:projects AS JSONB))
        """), {**u, "projects": json.dumps(u["projects"])})
