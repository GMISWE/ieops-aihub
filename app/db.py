"""SQLAlchemy async engine + session factory. 1A backend 会引用。"""
import os
from sqlalchemy.ext.asyncio import create_async_engine, async_sessionmaker, AsyncSession


def _make_engine():
    url = os.environ.get("AIHUB_DATABASE_URL")
    if not url:
        raise RuntimeError("AIHUB_DATABASE_URL not set")
    if "+asyncpg" not in url and url.startswith("postgresql"):
        url = url.replace("postgresql", "postgresql+asyncpg", 1)
    return create_async_engine(url, pool_pre_ping=True)


engine = None
SessionFactory: async_sessionmaker[AsyncSession] | None = None


def init_db():
    global engine, SessionFactory
    engine = _make_engine()
    SessionFactory = async_sessionmaker(engine, expire_on_commit=False)
