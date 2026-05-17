"""Fixtures for wire-tier multi-agent concurrent e2e tests.

Inherits PG container + Alembic + truncate fixtures from the parent
`tests/v3/conftest.py`. Adds:

- `wire_app` — fresh aihub FastAPI app bound to the test PG engine, plus an
  in-process httpx ASGITransport for sub-millisecond round-trip.
- `make_adapter(bearer, *, machine_id, session_secret)` factory — builds an
  independent `ConcreteAihubClient` + `AihubClientProtocolAdapter` per call.
  Sharing one ASGI transport guarantees all adapters hit the same in-process
  app + PG. Each adapter still carries its OWN bearer / session_secret /
  cred slot, so they remain distinct identities for concurrent races.
- `team_adapters` — convenience dict keyed by {"zhang", "li", "wang"} with
  pre-built adapters for the 3 reference users (`u_zhangsan` / `u_lisi` /
  `u_wangwu`) from `tests.v3.fixtures.REFERENCE_USERS`.

Scope: ONLY aihub server-layer invariants (claim / lease / event / lock /
fence). NO coding-plugin git/PR workflows. NO scenario.prepare_workspace.
"""
from __future__ import annotations

import httpx
import pytest
import pytest_asyncio

# polyforge-v3 is a marketplace plugin installed alongside aihub in dev /
# production environments, but aihub's CI doesn't install it. Skip the whole
# directory cleanly when unavailable rather than ModuleNotFoundError. Same
# pattern as test_3stream_wire_e2e.py for testcontainers.
pytest.importorskip(
    "polyforge_v3",
    reason="wire_multi_agent tests require the polyforge-v3 plugin "
           "(pip install polyforge-v3, or run from a workspace with it editable-installed)",
)

from app.v3_app import make_v3_app


# ---------------------------------------------------------------------------
# Shared constants for wire_multi_agent tests.
# Hoisted here so tuning a value doesn't require touching multiple test files.
# Tests `from .conftest import X` rather than re-defining locally.
# ---------------------------------------------------------------------------

# Server-side lease window override (sent via env). 2s lets P1.2 takeover +
# P1.5 split-brain exercise expiry in ~3s instead of waiting 60s. Sequential
# tests have headroom; lease-expiry tests sleep LEASE_EXPIRY_WAIT_S below.
SHORT_LEASE_SECONDS = "2"

# How long lease-expiry tests sleep to be safely past expiry. 2.5s gives
# 0.5s of slack vs the 2s server lease, avoiding flakes on slow CI nodes.
LEASE_EXPIRY_WAIT_S = 2.5

# read-during-write (test_read_during_write.py)
WRITER_BURST_COUNT = 50      # emit_events fired back-to-back during the test
READ_INTERVAL_S = 0.05       # reader poll interval (50ms)
READ_BUDGET_S = 0.2          # per-read budget: 200ms

# emit storm (test_emit_storm.py)
EMIT_STORM_N_AGENTS = 4      # concurrent agents sharing one AttemptCredential
EMIT_STORM_N_ROUNDS = 5      # emits per agent
EMIT_STORM_TOTAL = EMIT_STORM_N_AGENTS * EMIT_STORM_N_ROUNDS  # 20

# lease renewal storm (test_lease_renewal_storm.py)
RENEW_STORM_INTERVAL_S = 0.4
RENEW_STORM_CYCLES = 4
RENEW_STORM_PER_CALL_BUDGET_S = 0.5


@pytest.fixture(autouse=True)
def _short_lease_env(monkeypatch):
    """Force AIHUB_LEASE_SECONDS=2 for every test in this directory.

    Default lease is 60s — fine for prod, fatal for tests that wait for
    expiry. Using monkeypatch so the env reverts cleanly after each test.
    """
    monkeypatch.setenv("AIHUB_LEASE_SECONDS", SHORT_LEASE_SECONDS)

# polyforge-v3 plugin is installed; safe to import.
from polyforge_v3.aihub.client import AihubClient as ConcreteAihubClient
from polyforge_v3.aihub.adapter import AihubClientProtocolAdapter as ProtocolAdapter
from polyforge_v3.config import AihubConfig, SessionInfo


# Canonical bearer tokens (raw values; aihub stores sha256$<digest>).
# Must stay in sync with tests/v3/fixtures.py:REFERENCE_USERS.
BEARER_ZHANG = "argon2id$dummy_seed_hash_zhang"
BEARER_LI = "argon2id$dummy_seed_hash_li"
BEARER_WANG = "argon2id$dummy_seed_hash_wang"

# Canonical session_secrets per user (64 hex chars, matches
# SESSION_SECRET_PATTERN). Distinct per user so concurrent races don't
# accidentally share state at the session layer.
SECRET_ZHANG = "a" * 64
SECRET_LI = "b" * 64
SECRET_WANG = "c" * 64


@pytest_asyncio.fixture(loop_scope="session")
async def wire_app(seeded_users):
    """Fresh aihub app + in-process ASGITransport bound to the seeded PG.

    Re-uses the parent `seeded_users` fixture, which truncates between tests
    and re-inserts the 3 reference users. Tests get a clean slate per run.
    """
    app = make_v3_app(engine_factory=lambda: seeded_users)
    transport = httpx.ASGITransport(app=app)
    async with app.router.lifespan_context(app):
        yield app, transport


@pytest_asyncio.fixture(loop_scope="session")
async def make_adapter(wire_app):
    """Factory: build (adapter, concrete_client) tuples.

    Usage in tests:

        adapter_z, concrete_z = await make_adapter(
            BEARER_ZHANG, machine_id="zhang-mbp", session_secret=SECRET_ZHANG,
        )

    Each call returns a NEW concrete client + adapter. Multiple adapters
    share the same ASGI transport (same in-process app + PG) but have
    independent bearer / session / cred slots. Concurrent operations via
    `asyncio.gather` exercise the real server-side serialization.
    """
    _, transport = wire_app
    created: list[ConcreteAihubClient] = []

    async def _factory(bearer: str, *, machine_id: str, session_secret: str):
        cfg = AihubConfig(url="http://test", api_key_env="_UNUSED_", api_key=bearer)
        session = SessionInfo(
            machine_id=machine_id,
            session_id=f"sess-{machine_id}",
            session_secret=session_secret,
        )
        concrete = ConcreteAihubClient(cfg, session, transport=transport, timeout=30.0)
        created.append(concrete)
        adapter = ProtocolAdapter(concrete)
        return adapter, concrete

    yield _factory

    for c in created:
        await c.aclose()


@pytest_asyncio.fixture(loop_scope="session")
async def team_adapters(make_adapter):
    """Pre-built adapter set for the 3 reference users.

    Returns a dict {role: adapter}. The concrete client is owned by the
    factory + cleaned up via the make_adapter fixture's yield contract, so
    tests don't need to track it; if a test needs the concrete client
    (rare — only for inspecting transport state), call make_adapter directly.

    None of these adapters have `set_cred()` called yet — tests must claim
    a work_item first to obtain an `AttemptCredential` before any
    attempt-fenced write.
    """
    z, _ = await make_adapter(BEARER_ZHANG, machine_id="zhang-mbp", session_secret=SECRET_ZHANG)
    l, _ = await make_adapter(BEARER_LI,    machine_id="li-mbp",    session_secret=SECRET_LI)
    w, _ = await make_adapter(BEARER_WANG,  machine_id="wang-mbp",  session_secret=SECRET_WANG)
    return {"zhang": z, "li": l, "wang": w}
