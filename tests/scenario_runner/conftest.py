"""Re-export PG fixtures from tests/v3/conftest.py so scenarios can share them.

Gated on polyforge_v3 — the scenario runner's executor + assertions chain
imports polyforge_v3.aihub.client + adapter + config. aihub's CI doesn't
install the polyforge-v3 plugin; skip cleanly in that environment.
"""
import pytest

pytest.importorskip(
    "polyforge_v3",
    reason="scenario_runner requires the polyforge-v3 plugin "
           "(pip install polyforge-v3, or run from a workspace with it editable-installed)",
)

from tests.v3.conftest import (  # noqa: F401
    pg_container, pg_engine, fresh_db, seeded_users,
)
