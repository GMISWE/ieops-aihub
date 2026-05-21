"""验证 fixtures 能创建出 reference §0 / §1 初始状态。"""
import pytest
import sqlalchemy as sa


pytestmark = pytest.mark.asyncio(loop_scope="session")


async def test_fresh_db_truncates_data_tables_but_keeps_admin(fresh_db):
    async with fresh_db.connect() as conn:
        # data 表被 TRUNCATE
        for t in ["work_items", "run_attempts", "resource_locks",
                  "agent_events", "memories"]:
            r = await conn.execute(sa.text(f"SELECT COUNT(*) FROM {t}"))
            assert r.scalar() == 0, f"{t} not empty after fresh_db"
        # admin user 留着
        r = await conn.execute(sa.text(
            "SELECT COUNT(*) FROM users WHERE role = 'admin'"
        ))
        assert r.scalar() >= 1


async def test_seed_three_users(fresh_db, seeded_users):
    """张三 / 李四 / 王五 都在。"""
    expected = {"u_zhangsan", "u_lisi", "u_wangwu"}
    async with fresh_db.connect() as conn:
        r = await conn.execute(sa.text(
            "SELECT id FROM users WHERE role = 'writer'"
        ))
        ids = {row[0] for row in r}
    assert expected <= ids


async def test_seed_reference_scenario_initial(fresh_db, seeded_reference):
    """跑 reference §1 09:00 起始状态后, wi_a3f + ra_111 + 2 locks 都在。"""
    async with fresh_db.connect() as conn:
        wi = (await conn.execute(sa.text(
            "SELECT status, reporter_user_id FROM work_items WHERE id = 'wi_a3f'"
        ))).first()
        assert wi is not None
        assert wi[0] == "running"
        assert wi[1] == "u_zhangsan"

        ra = (await conn.execute(sa.text(
            "SELECT claim_epoch, actor_user_id, status FROM run_attempts WHERE id = 'ra_111'"
        ))).first()
        assert ra == (1, "u_zhangsan", "running")

        lock_count = (await conn.execute(sa.text(
            "SELECT COUNT(*) FROM resource_locks WHERE owner_attempt_id = 'ra_111'"
        ))).scalar()
        assert lock_count == 2  # git_branch + worktree
