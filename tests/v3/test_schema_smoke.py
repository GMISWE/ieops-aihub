"""跑完 alembic upgrade head 后, 6 张表 + 关键索引必须存在。"""
import pytest
import sqlalchemy as sa


pytestmark = pytest.mark.asyncio(loop_scope="session")


async def test_all_six_tables_present(pg_engine):
    async with pg_engine.connect() as conn:
        rows = await conn.execute(sa.text(
            "SELECT tablename FROM pg_tables WHERE schemaname = 'public' ORDER BY tablename"
        ))
        tables = {r[0] for r in rows}
    expected = {"users", "work_items", "run_attempts", "resource_locks",
                "agent_events", "memories"}
    missing = expected - tables
    assert not missing, f"missing tables: {missing}"


async def test_circular_fk_exists(pg_engine):
    """work_items.current_attempt_id → run_attempts.id (DEFERRABLE)."""
    async with pg_engine.connect() as conn:
        rows = await conn.execute(sa.text("""
            SELECT conname, condeferrable FROM pg_constraint
            WHERE conrelid = 'work_items'::regclass AND contype = 'f'
              AND conname = 'fk_wi_current_attempt'
        """))
        row = rows.first()
    assert row is not None, "fk_wi_current_attempt missing"
    assert row[1] is True, "fk_wi_current_attempt must be DEFERRABLE"


async def test_check_constraints(pg_engine):
    """work_items.status / users.role / run_attempts.status 必须有 CHECK enum。"""
    async with pg_engine.connect() as conn:
        rows = await conn.execute(sa.text("""
            SELECT conname FROM pg_constraint
            WHERE contype = 'c' AND conrelid IN (
                'users'::regclass, 'work_items'::regclass, 'run_attempts'::regclass
            )
        """))
        names = {r[0] for r in rows}
    # at least these — 实际名字看 alembic gen 后填回
    assert any("role" in n for n in names)
    assert any("status" in n for n in names)


async def test_pgvector_extension(pg_engine):
    """memories.embedding 是 VECTOR(384), 需 vector ext。"""
    async with pg_engine.connect() as conn:
        rows = await conn.execute(sa.text(
            "SELECT extname FROM pg_extension WHERE extname = 'vector'"
        ))
    assert rows.first() is not None


async def test_critical_indexes(pg_engine):
    """v3-design §5 要求的关键索引必须存在。"""
    expected_indexes = {
        "idx_users_api_keys_gin",
        "idx_wi_project_status", "idx_wi_share", "idx_wi_declared_gin", "idx_wi_parent",
        "idx_ra_lease_expired", "idx_ra_api_key",
        "idx_lock_owner",
        "idx_ev_wi_time", "idx_ev_type_time",
        "idx_mem_project_type",
    }
    async with pg_engine.connect() as conn:
        rows = await conn.execute(sa.text(
            "SELECT indexname FROM pg_indexes WHERE schemaname = 'public'"
        ))
        actual = {r[0] for r in rows}
    missing = expected_indexes - actual
    assert not missing, f"missing indexes: {missing}"
