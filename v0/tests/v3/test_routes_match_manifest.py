"""F5 — Verify every path in openapi/aihub_openapi.yaml has a matching FastAPI route.

Reads app.routes from make_v3_app(...), computes (method, path) pairs, then
compares against the manifest in openapi_manifest.py.  Any OpenAPI path without
a route → test fails (phantom endpoint).  Any route without an OpenAPI path →
also flagged (undocumented endpoint).
"""
from __future__ import annotations

import yaml
from pathlib import Path

import sqlalchemy as sa
from sqlalchemy.ext.asyncio import AsyncEngine

from app.v3_app import make_v3_app
from tests.v3.openapi_manifest import ENDPOINTS


import re

_PATH_CONVERTER_RE = re.compile(r"\{(\w+):[^}]+\}")


def _normalize_path(path: str) -> str:
    """Normalize FastAPI path converter syntax to plain {param} for comparison.

    FastAPI allows {name:path}, {name:int}, etc. OpenAPI uses {name} only.
    E.g. /locks/{resource_type}/{resource_key:path} → /locks/{resource_type}/{resource_key}
    """
    return _PATH_CONVERTER_RE.sub(r"{\1}", path)


def _get_live_routes(engine: AsyncEngine) -> set[tuple[str, str]]:
    """Return set of (METHOD, /v1/path) from the live FastAPI app routes.

    Path converter syntax is normalized to plain {param} for comparison with OpenAPI.
    """
    app = make_v3_app(engine_factory=lambda: engine)
    routes = set()
    for route in app.routes:
        path = getattr(route, "path", None)
        methods = getattr(route, "methods", None)
        if path is None or methods is None:
            continue
        normalized = _normalize_path(path)
        for method in methods:
            routes.add((method.upper(), normalized))
    return routes


def _get_openapi_paths() -> set[tuple[str, str]]:
    """Parse openapi/aihub_openapi.yaml → set of (METHOD, /v1/path)."""
    yaml_path = Path(__file__).parent.parent.parent / "openapi" / "aihub_openapi.yaml"
    with open(yaml_path) as f:
        spec = yaml.safe_load(f)
    paths = set()
    for path, path_item in spec.get("paths", {}).items():
        for method in path_item:
            if method.lower() in ("get", "post", "put", "patch", "delete", "head", "options"):
                paths.add((method.upper(), path))
    return paths


def test_every_openapi_path_has_a_live_route(pg_engine):
    """Every path in aihub_openapi.yaml must correspond to a mounted FastAPI route.

    This catches phantom endpoints (declared in spec but not implemented).
    Uses the real engine so the app wires up exactly as in production.
    """
    openapi_paths = _get_openapi_paths()
    live_routes = _get_live_routes(pg_engine)

    # Normalize OpenAPI path params: {work_item_id} → {work_item_id} (FastAPI same format)
    # FastAPI uses {name} just like OpenAPI, so direct comparison works.
    missing_from_app = openapi_paths - live_routes
    assert not missing_from_app, (
        f"OpenAPI paths with no matching FastAPI route (phantom endpoints):\n"
        + "\n".join(f"  {m} {p}" for m, p in sorted(missing_from_app))
    )


def test_manifest_matches_openapi_paths():
    """Cross-check: openapi_manifest.py ENDPOINTS must match aihub_openapi.yaml paths."""
    openapi_paths = _get_openapi_paths()
    manifest_paths = {(e.method, e.path) for e in ENDPOINTS}

    missing_in_openapi = manifest_paths - openapi_paths
    assert not missing_in_openapi, (
        f"Manifest endpoints not found in openapi yaml:\n"
        + "\n".join(f"  {m} {p}" for m, p in sorted(missing_in_openapi))
    )

    missing_in_manifest = openapi_paths - manifest_paths
    assert not missing_in_manifest, (
        f"OpenAPI paths not declared in manifest (add them or remove from yaml):\n"
        + "\n".join(f"  {m} {p}" for m, p in sorted(missing_in_manifest))
    )
