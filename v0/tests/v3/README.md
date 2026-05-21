# aihub v3 tests

## Fixtures

| Fixture | 作用 |
|---|---|
| `pg_engine` | session-scoped — 启 pgvector container, 跑 alembic upgrade head |
| `fresh_db` | per-test — TRUNCATE 所有 data 表, 保留 admin user |
| `seeded_users` | per-test — 在 `fresh_db` 上加 张三 / 李四 / 王五 (reference §0) |
| `seeded_reference` | per-test — 在 `seeded_users` 上加 reference §1 09:00 状态 |

## 跑测试

```
uv sync --extra test
uv run python -m pytest tests/v3/ -v
```

首次运行会拉 `pgvector/pgvector:pg16` 镜像 (~150 MB)。

## 测试组织

- `test_schema_smoke.py` — 0.1 schema 完整性
- `test_openapi_valid.py` — 0.2 OpenAPI 合规
- `test_errors.py` — 0.3 错误模型
- `test_fixtures_smoke.py` — 0.7 fixtures 自身
- 后续 Phase 1A: `test_endpoints_*.py`, `test_conflict_predictor.py`, etc.

## 辅助模块

- `fixtures.py` — `REFERENCE_USERS` 常量 + `insert_reference_users()` (reference §0)
- `seed_reference_scenario.py` — `seed_09_00_state()` (reference §1 09:00 初始状态)
