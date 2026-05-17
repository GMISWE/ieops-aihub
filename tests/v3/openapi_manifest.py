"""OpenAPI endpoint shape SoT — 由 plan Task 4a 内嵌的 manifest 表派生。
Task 4b 的 aihub_openapi.yaml 必须跟此处一一对应, 由 test_openapi_valid 强校。
"""
from __future__ import annotations

from typing import NamedTuple


class EndpointShape(NamedTuple):
    method: str          # GET / POST / PATCH / DELETE
    path: str
    operation_id: str
    request_required: frozenset[str]   # request body 必填字段 (含 AC 三联 if mutating)
    response_required: frozenset[str]  # 200/201 响应必填字段 (top-level)
    error_codes: frozenset[int]        # 应出现的 4xx 状态码


AC_FIELDS = frozenset({"attempt_id", "claim_epoch", "session_secret"})


ENDPOINTS: list[EndpointShape] = [
    EndpointShape("GET", "/v1/whoami", "whoami",
                  frozenset(), frozenset({"user_id", "role", "projects"}),
                  frozenset({401})),
    EndpointShape("POST", "/v1/work_items", "create_work_item",
                  frozenset({"project", "scenario", "goal"}),
                  frozenset({"id", "project", "scenario", "goal", "status",
                             "priority", "labels", "reporter_user_id",
                             "declared_resources", "resources_version",
                             "metadata", "created_at", "updated_at"}),
                  frozenset({400, 401})),
    EndpointShape("GET", "/v1/work_items", "list_work_items",
                  frozenset(), frozenset({"items", "next_cursor"}),
                  frozenset({401})),
    EndpointShape("GET", "/v1/work_items/{work_item_id}", "get_work_item",
                  frozenset(),
                  frozenset({"work_item", "current_attempt",
                             "recent_events", "per_repo_state"}),
                  frozenset({401, 404})),
    EndpointShape("PATCH", "/v1/work_items/{work_item_id}", "update_work_item",
                  AC_FIELDS | frozenset({"patch_payload"}),
                  frozenset({"id", "project", "scenario", "goal", "status",
                             "priority", "labels", "reporter_user_id",
                             "declared_resources", "resources_version",
                             "metadata", "created_at", "updated_at"}),
                  frozenset({401, 403, 404, 409})),
    EndpointShape("POST", "/v1/work_items/{work_item_id}/claim", "claim_work_item",
                  frozenset({"idempotency_key", "session_info", "requested_locks"}),
                  frozenset({"attempt_id", "claim_epoch", "lease_until"}),
                  frozenset({401, 409})),
    EndpointShape("POST", "/v1/work_items/{work_item_id}/complete", "complete_work_item",
                  AC_FIELDS | frozenset({"final_status"}),
                  frozenset({"ok"}),
                  frozenset({401, 403, 409})),
    EndpointShape("POST", "/v1/attempts/{attempt_id}/lease", "renew_lease",
                  frozenset({"claim_epoch", "session_secret"}),
                  frozenset({"lease_until"}),
                  frozenset({401, 409})),
    EndpointShape("POST", "/v1/attempts/{attempt_id}/complete", "complete_attempt",
                  AC_FIELDS | frozenset({"status"}),
                  frozenset({"ok"}),
                  frozenset({401, 403, 409})),
    EndpointShape("POST", "/v1/attempts/{attempt_id}/pause", "pause_attempt",
                  AC_FIELDS | frozenset({"reason"}),
                  frozenset({"ok"}),
                  frozenset({401, 403, 409})),
    EndpointShape("POST", "/v1/locks", "acquire_lock",
                  AC_FIELDS | frozenset({"resource_type", "resource_key"}),
                  frozenset({"ok"}),
                  frozenset({401, 403, 409})),  # G2-re r3: 加 403 actor mismatch (§6.2)
    EndpointShape("DELETE", "/v1/locks/{resource_type}/{resource_key}", "release_lock",
                  AC_FIELDS,
                  frozenset({"ok"}),
                  frozenset({401, 403, 409})),
    EndpointShape("POST", "/v1/events", "emit_event",
                  AC_FIELDS | frozenset({"work_item_id", "event_type", "payload"}),
                  frozenset({"event_id"}),
                  frozenset({400, 401, 409, 413})),
    EndpointShape("GET", "/v1/events", "list_events",
                  frozenset(),
                  frozenset({"items", "next_cursor"}),
                  frozenset({401})),
    EndpointShape("POST", "/v1/conflicts/predict", "predict_conflicts",
                  frozenset({"project", "declared_resources"}),
                  frozenset({"severity", "predictions"}),
                  frozenset({401})),
    EndpointShape("POST", "/v1/work_items/{work_item_id}/artifacts/adopt", "adopt_artifact",
                  AC_FIELDS | frozenset({"type", "identifier", "repo"}),
                  frozenset({"ok"}),
                  frozenset({401, 409})),
    EndpointShape("POST", "/v1/work_items/{work_item_id}/artifacts/ignore", "ignore_artifact",
                  AC_FIELDS | frozenset({"type", "identifier", "repo"}),
                  frozenset({"ok"}),
                  frozenset({401, 409})),
    EndpointShape("POST", "/v1/work_items/{work_item_id}/artifacts/close", "close_artifact",
                  AC_FIELDS | frozenset({"type", "identifier", "repo"}),
                  frozenset({"ok"}),
                  frozenset({401, 409})),
    EndpointShape("POST", "/v1/memories", "create_memory",
                  frozenset({"project", "type", "content", "visibility"}),
                  frozenset({"id", "project", "author_user_id", "visibility",
                             "type", "metadata", "created_at"}),
                  frozenset({400, 401, 403})),
    EndpointShape("GET", "/v1/memories", "list_memories",
                  frozenset(),
                  frozenset({"items", "next_cursor"}),
                  frozenset({401})),
    EndpointShape("PATCH", "/v1/memories/{memory_id}", "update_memory",
                  frozenset({"patch_payload"}),
                  frozenset({"id", "project", "author_user_id", "visibility",
                             "type", "metadata", "created_at"}),
                  frozenset({401, 403, 404})),
    EndpointShape("POST", "/v1/memories/{memory_id}/redact", "redact_memory",
                  # No AC per §6.1 memory carve-out — memory is per-user, not per-attempt
                  frozenset({"reason"}),
                  frozenset({"ok"}),
                  frozenset({401, 403, 404})),
    EndpointShape("POST", "/v1/admin/users", "admin_add_user",
                  frozenset({"email", "display_name", "role", "projects"}),
                  frozenset({"user_id", "api_key"}),
                  frozenset({401, 403})),
    EndpointShape("POST", "/v1/admin/keys", "admin_create_key",
                  frozenset({"user_id", "scopes"}),
                  frozenset({"key_id", "api_key"}),
                  frozenset({401, 403, 404})),
    EndpointShape("POST", "/v1/admin/keys/revoke", "admin_revoke_key",
                  frozenset({"key_id"}),
                  frozenset({"ok", "terminated_attempts"}),
                  frozenset({401, 403, 404})),
]


assert len(ENDPOINTS) == 25, f"expected 25 endpoints, got {len(ENDPOINTS)}"
assert len({(e.method, e.path) for e in ENDPOINTS}) == 25, "duplicate (method, path)"
assert len({e.operation_id for e in ENDPOINTS}) == 25, "duplicate operation_id"
