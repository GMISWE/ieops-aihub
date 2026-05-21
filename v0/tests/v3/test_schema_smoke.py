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


async def test_resource_type_check_rejects_bogus(pg_engine):
    """G2-re r3: resource_locks.resource_type CHECK enum 必须真拒绝非闭集值。
    H4 patch 后这个 negative test 是 regression guard。"""
    async with pg_engine.connect() as conn:
        # 先建一个最小的 user + work_item + attempt 让 FK 不挂
        await conn.execute(sa.text("""
            INSERT INTO users (id, email, display_name, role)
            VALUES ('u_test_neg', 'neg@gmi.local', 'neg', 'writer')
            ON CONFLICT (id) DO NOTHING
        """))
        await conn.execute(sa.text("""
            INSERT INTO work_items (id, project, scenario, goal, status, reporter_user_id)
            VALUES ('wi_neg', 'marketplace', 'coding', 'neg test', 'queued', 'u_test_neg')
            ON CONFLICT (id) DO NOTHING
        """))
        await conn.execute(sa.text("""
            INSERT INTO run_attempts (
                id, work_item_id, status, claim_epoch, idempotency_key, lease_until,
                actor_user_id, api_key_id, actor_display, machine_id, session_secret_hash
            )
            VALUES ('ra_neg', 'wi_neg', 'running', 1, 'idem_neg',
                    now() + interval '60 seconds',
                    'u_test_neg', 'ak_neg', 'neg', 'neg-host', 'hash_neg')
            ON CONFLICT (id) DO NOTHING
        """))
        # 尝试插入非法 resource_type, 期望 CHECK 拒绝
        with pytest.raises(Exception) as exc:
            await conn.execute(sa.text("""
                INSERT INTO resource_locks (resource_type, resource_key, owner_attempt_id, claim_epoch)
                VALUES ('bogus_type_not_in_enum', 'k', 'ra_neg', 1)
            """))
            await conn.commit()
        # IntegrityError / CheckViolation 都接受
        assert "check" in str(exc.value).lower() or "violates" in str(exc.value).lower() or "23514" in str(exc.value)
        # rollback 避免污染 session
        await conn.rollback()


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


async def test_api_keys_revoked_at_check_rejects_missing_field(pg_engine):
    """alembic 0002: users.api_keys CHECK constraint rejects entries that
    omit `revoked_at`. The GIN @> predicate in find_user_by_api_key relies
    on revoked_at being present (null=active, ts=revoked); silently-missing
    entries would render the user undiscoverable.
    """
    async with pg_engine.connect() as conn:
        with pytest.raises(Exception) as exc:
            await conn.execute(sa.text("""
                INSERT INTO users (id, email, display_name, role, api_keys)
                VALUES ('u_check_neg', 'neg-check@gmi.local', 'neg', 'writer',
                        '[{"id": "ak_bad", "key_hash": "x", "scopes": [],
                           "created_at": "2026-05-16T00:00:00Z"}]'::jsonb)
            """))
            await conn.commit()
        msg = str(exc.value).lower()
        assert "check" in msg or "violates" in msg or "23514" in msg
        await conn.rollback()


async def test_api_keys_revoked_at_check_accepts_null(pg_engine):
    """The same CHECK must ACCEPT entries with explicit `revoked_at: null`
    (the active-key sentinel)."""
    async with pg_engine.connect() as conn:
        await conn.execute(sa.text("""
            INSERT INTO users (id, email, display_name, role, api_keys)
            VALUES ('u_check_pos', 'pos-check@gmi.local', 'pos', 'writer',
                    '[{"id": "ak_ok", "key_hash": "x", "scopes": [],
                       "created_at": "2026-05-16T00:00:00Z",
                       "revoked_at": null}]'::jsonb)
            ON CONFLICT (id) DO NOTHING
        """))
        await conn.commit()
        # Cleanup
        await conn.execute(sa.text("DELETE FROM users WHERE id = 'u_check_pos'"))
        await conn.commit()


async def test_api_keys_revoked_at_check_accepts_empty_array(pg_engine):
    """The CHECK must ACCEPT empty api_keys arrays (newly-onboarded user
    before any key is provisioned)."""
    async with pg_engine.connect() as conn:
        await conn.execute(sa.text("""
            INSERT INTO users (id, email, display_name, role, api_keys)
            VALUES ('u_check_empty', 'empty-check@gmi.local', 'empty', 'reader',
                    '[]'::jsonb)
            ON CONFLICT (id) DO NOTHING
        """))
        await conn.commit()
        await conn.execute(sa.text("DELETE FROM users WHERE id = 'u_check_empty'"))
        await conn.commit()
