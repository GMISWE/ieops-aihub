"""Smoke + execution tests for scenario_runner.

For each *.scenario.md in tests/scenarios/, this test file:
  1. Verifies the file parses
  2. (For the subset that work in Mode 1) executes it end-to-end + evaluates assertions

Scenarios that require Mode-2 (real subagents) are parsed-only here; their
execution is handled by `/execute-scenario --subagents` from a Claude Code
session, not pytest.
"""
from __future__ import annotations

from pathlib import Path

import httpx
import pytest

from app.v3_app import make_v3_app
from tests.scenario_runner.parser import parse_scenario
from tests.scenario_runner.executor import execute_scenario
from tests.scenario_runner.assertions import evaluate


SCENARIOS_DIR = Path(__file__).parent.parent / "scenarios"


def _all_scenarios() -> list[Path]:
    return sorted(SCENARIOS_DIR.glob("*.scenario.md"))


@pytest.mark.parametrize("scenario_path", _all_scenarios(), ids=lambda p: p.stem)
def test_scenario_parses_cleanly(scenario_path: Path):
    """Every .scenario.md in the dir parses without raising."""
    scenario = parse_scenario(scenario_path)
    assert scenario.name
    assert scenario.description
    assert len(scenario.cast) >= 1
    assert len(scenario.timeline) >= 1
    assert len(scenario.assertions) >= 1


# Mode-1-compatible scenarios. After C5 fix (per-role var namespace via
# contextvars), parallel-cast scenarios like standup-claim-race work too.
# Excluded: oncall-emit-storm — uses `share_attempt_from` which the runner
# doesn't implement yet (LOW finding in review).
_MODE1_SCENARIOS = ["cross-machine-takeover", "standup-claim-race"]


@pytest.mark.parametrize("scenario_name", _MODE1_SCENARIOS)
@pytest.mark.asyncio(loop_scope="session")
async def test_scenario_runs_end_to_end(scenario_name, seeded_users, tmp_path):
    """Pick one scenario, execute through aihub, verify all assertions pass.

    Uses the seeded_users fixture (parent conftest) to get a clean PG with
    reference users. Builds an in-process ASGI transport so the executor
    can hit make_v3_app over httpx without opening a real port.
    """
    scenario_path = SCENARIOS_DIR / f"{scenario_name}.scenario.md"
    scenario = parse_scenario(scenario_path)

    app = make_v3_app(engine_factory=lambda: seeded_users)
    transport = httpx.ASGITransport(app=app)

    async with app.router.lifespan_context(app):
        ctx = await execute_scenario(scenario, workspace=tmp_path, transport=transport)
        await evaluate(scenario, ctx, seeded_users)
