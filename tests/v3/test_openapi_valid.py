"""aihub_openapi.yaml 必须 (a) 通过 openapi-spec-validator (b) 跟 openapi_manifest.ENDPOINTS 一一对应。"""
from pathlib import Path
import yaml
import pytest
from openapi_spec_validator import validate_spec

from tests.v3.openapi_manifest import ENDPOINTS, AC_FIELDS


SPEC_PATH = Path(__file__).parent.parent.parent / "openapi" / "aihub_openapi.yaml"


@pytest.fixture(scope="module")
def spec():
    return yaml.safe_load(SPEC_PATH.read_text())


def test_openapi_spec_valid(spec):
    validate_spec(spec)


def test_version_prefix_v1(spec):
    for path in spec["paths"]:
        assert path.startswith("/v1/"), f"endpoint {path!r} missing /v1/ prefix"


def test_no_top_level_components_duplicate(spec):
    """⭐ G1 修复关键: components 必须只在顶层出现一次, 不可嵌在 paths 内。"""
    paths_block = spec.get("paths", {})
    assert "components" not in paths_block, (
        "components must NOT be nested under paths — move to top level"
    )
    assert "components" in spec, "components must exist at top level"


def test_all_24_endpoints_present(spec):
    actual_pairs = set()
    for path, methods in spec["paths"].items():
        for method in methods:
            if method.lower() in ("get", "post", "put", "patch", "delete"):
                actual_pairs.add((method.upper(), path))
    expected_pairs = {(e.method, e.path) for e in ENDPOINTS}
    missing = expected_pairs - actual_pairs
    extra = actual_pairs - expected_pairs
    assert not missing, f"missing endpoints: {missing}"
    assert not extra, f"unexpected endpoints not in manifest: {extra}"


@pytest.mark.parametrize("endpoint", ENDPOINTS, ids=lambda e: f"{e.method} {e.path}")
def test_endpoint_operation_id(spec, endpoint):
    op = spec["paths"][endpoint.path][endpoint.method.lower()]
    assert op.get("operationId") == endpoint.operation_id


def _collect_required(schema: dict, spec: dict) -> frozenset[str]:
    """递归收集 required 集合 — 支持 $ref + allOf 组合 (N4 修复)。"""
    if "$ref" in schema:
        name = schema["$ref"].split("/")[-1]
        schema = spec["components"]["schemas"][name]
    if "allOf" in schema:
        out: set[str] = set()
        for sub in schema["allOf"]:
            out |= _collect_required(sub, spec)
        return frozenset(out)
    return frozenset(schema.get("required", []))


@pytest.mark.parametrize("endpoint", ENDPOINTS, ids=lambda e: f"{e.method} {e.path}")
def test_endpoint_request_required_matches_manifest(spec, endpoint):
    """⭐ G1 修复关键: 每个 endpoint 的 request body required 必须等于 manifest。
    N4 修复: 走 allOf 组合 — AC 三联从 $ref AttemptCredential 继承, 各 endpoint 独有字段 inline。
    """
    op = spec["paths"][endpoint.path][endpoint.method.lower()]
    has_rb = "requestBody" in op
    if not endpoint.request_required:
        # GET / DELETE-without-body — 应无 requestBody (N5 收口)
        if endpoint.method in ("GET",):
            assert not has_rb, f"{endpoint.method} {endpoint.path} should not have requestBody"
        return
    assert has_rb, f"{endpoint.method} {endpoint.path} missing requestBody"
    schema = op["requestBody"]["content"]["application/json"]["schema"]
    actual_required = _collect_required(schema, spec)
    assert actual_required == endpoint.request_required, (
        f"{endpoint.method} {endpoint.path}: "
        f"request required={sorted(actual_required)} "
        f"!= manifest={sorted(endpoint.request_required)}"
    )


@pytest.mark.parametrize("endpoint", ENDPOINTS, ids=lambda e: f"{e.method} {e.path}")
def test_endpoint_error_codes_present(spec, endpoint):
    op = spec["paths"][endpoint.path][endpoint.method.lower()]
    responses = op.get("responses", {})
    actual = {int(c) for c in responses if c.isdigit() and int(c) >= 400}
    missing = endpoint.error_codes - actual
    assert not missing, (
        f"{endpoint.method} {endpoint.path}: missing 4xx codes {missing}"
    )


def test_components_attempt_credential_shape(spec):
    """AttemptCredential schema 必须有 attempt_id + claim_epoch + session_secret 三联。"""
    ac = spec["components"]["schemas"]["AttemptCredential"]
    required = set(ac["required"])
    assert required >= AC_FIELDS, f"AttemptCredential missing fields: {AC_FIELDS - required}"


def test_components_error_envelope_shape(spec):
    ee = spec["components"]["schemas"]["ErrorEnvelope"]
    assert "code" in ee["properties"]
    assert "message" in ee["properties"]


def test_pagination_response_consistent(spec):
    """Design §20 M12: 所有 GET list endpoint 必须返 {items, next_cursor}。"""
    list_endpoints = ["/v1/work_items", "/v1/events", "/v1/memories"]
    for ep in list_endpoints:
        schema_ref = spec["paths"][ep]["get"]["responses"]["200"]["content"]["application/json"]["schema"]
        if "$ref" in schema_ref:
            name = schema_ref["$ref"].split("/")[-1]
            schema_ref = spec["components"]["schemas"][name]
        props = schema_ref.get("properties", {})
        assert "items" in props, f"{ep} response missing items"
        assert "next_cursor" in props, f"{ep} response missing next_cursor"


def test_source_enum_matches_polyforge_v3_constants(spec):
    """⭐ M3 修复: OpenAPI POST /v1/work_items source enum 必须等于 SOURCE_VALUES。"""
    from polyforge_v3.events.constants import SOURCE_VALUES
    op = spec["paths"]["/v1/work_items"]["post"]
    schema = op["requestBody"]["content"]["application/json"]["schema"]
    if "$ref" in schema:
        name = schema["$ref"].split("/")[-1]
        schema = spec["components"]["schemas"][name]
    source_enum = tuple(schema["properties"]["source"]["enum"])
    assert source_enum == SOURCE_VALUES


def test_status_enum_matches_design_check(spec):
    """work_items.status enum 必须等于 design §5 CHECK + polyforge_v3 constants。"""
    from polyforge_v3.events.constants import WORK_ITEM_STATUSES
    wi = spec["components"]["schemas"]["WorkItem"]
    status_enum = tuple(wi["properties"]["status"]["enum"])
    assert status_enum == WORK_ITEM_STATUSES


def test_resource_type_enum_matches_constants(spec):
    """RESOURCE_TYPES 在 POST /v1/locks request body 必须一致。"""
    from polyforge_v3.events.constants import RESOURCE_TYPES
    op = spec["paths"]["/v1/locks"]["post"]
    rb_schema = op["requestBody"]["content"]["application/json"]["schema"]
    if "$ref" in rb_schema:
        name = rb_schema["$ref"].split("/")[-1]
        rb_schema = spec["components"]["schemas"][name]
    locks_enum = tuple(rb_schema["properties"]["resource_type"]["enum"])
    assert locks_enum == RESOURCE_TYPES
