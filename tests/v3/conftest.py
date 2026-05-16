"""共享 fixture: 启 PG 容器, 跑 alembic upgrade head, yield engine。"""
from pathlib import Path
import asyncio
import concurrent.futures
import pytest
import pytest_asyncio
import sqlalchemy as sa
from sqlalchemy.ext.asyncio import create_async_engine
from testcontainers.postgres import PostgresContainer
from alembic.config import Config
from alembic import command


# Use session-scoped event loop for all tests and fixtures in this directory.
# This prevents "Future attached to a different loop" errors when sharing an
# asyncpg engine across session fixtures and individual test coroutines.
def pytest_configure(config):
    config.addinivalue_line(
        "markers",
        "asyncio: mark test as async (pytest-asyncio)",
    )


# Session-scoped loop — pytest-asyncio 1.x exposes loop_scope on the fixture
@pytest_asyncio.fixture(scope="session", loop_scope="session")
async def pg_container():
    with PostgresContainer("pgvector/pgvector:pg16") as pg:
        yield pg


def _run_alembic_upgrade(async_url: str, ini_path: str) -> None:
    """Run alembic upgrade in a fresh thread (no event loop)."""
    cfg = Config(ini_path)
    cfg.set_main_option("sqlalchemy.url", async_url)
    command.upgrade(cfg, "head")


@pytest_asyncio.fixture(scope="session", loop_scope="session")
async def pg_engine(pg_container):
    raw_url = pg_container.get_connection_url()
    # testcontainers returns postgresql+psycopg2://... — replace driver for asyncpg
    async_url = (
        raw_url
        .replace("postgresql+psycopg2://", "postgresql+asyncpg://")
        .replace("postgresql://", "postgresql+asyncpg://")
    )
    engine = create_async_engine(async_url)
    # 先建 vector ext (alembic env.py 也会建, 但保险)
    async with engine.connect() as conn:
        await conn.execute(sa.text("CREATE EXTENSION IF NOT EXISTS vector"))
        await conn.commit()
    # 跑 alembic 迁移 — env.py 用 asyncio.run(), 必须在无 running loop 的线程里跑
    ini_path = str(Path(__file__).parent.parent.parent / "alembic.ini")
    loop = asyncio.get_running_loop()
    with concurrent.futures.ThreadPoolExecutor(max_workers=1) as pool:
        await loop.run_in_executor(pool, _run_alembic_upgrade, async_url, ini_path)
    yield engine
    await engine.dispose()
