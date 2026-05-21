"""s2 — app/schemas.py round-trip conformance to openapi yaml."""
from __future__ import annotations

import pytest
from datetime import datetime, timezone

from app import schemas


def test_attempt_credential_validates():
    ac = schemas.AttemptCredential(
        attempt_id="ra_abc123", claim_epoch=1, session_secret="a" * 64,
    )
    assert ac.attempt_id == "ra_abc123"


def test_attempt_credential_session_secret_must_be_64_hex():
    with pytest.raises(Exception):
        schemas.AttemptCredential(
            attempt_id="ra_abc", claim_epoch=1, session_secret="not-hex-not-64",
        )


def test_attempt_credential_claim_epoch_min_1():
    with pytest.raises(Exception):
        schemas.AttemptCredential(
            attempt_id="ra_abc", claim_epoch=0, session_secret="a" * 64,
        )


def test_create_work_item_request_minimal():
    r = schemas.CreateWorkItemRequest(
        project="marketplace", scenario="coding", goal="fix login 500",
    )
    assert r.declared_resources == []
    assert r.priority is None
    assert r.metadata == {}


def test_create_work_item_request_full():
    r = schemas.CreateWorkItemRequest(
        project="marketplace", scenario="coding", goal="x",
        declared_resources=[
            {"type": "repo", "uri": "repo:marketplace", "intent": "write",
             "base_branch": "main", "task_branch": "polyforge/wi_x"},
        ],
        labels=["bug"],
        priority="high",
        source="human",
        metadata={"k": "v"},
    )
    assert r.priority == "high"
    assert r.source == "human"


def test_claim_request_shape():
    r = schemas.ClaimRequest(
        idempotency_key="idem_001",
        session_info={"machine_id": "zhang-mbp", "session_secret": "a" * 64},
        requested_locks=[{"resource_type": "git_branch", "resource_key": "marketplace/wi_x"}],
    )
    assert r.session_info.machine_id == "zhang-mbp"
    assert len(r.requested_locks) == 1


def test_claim_response_shape():
    r = schemas.ClaimResponse(
        attempt_id="ra_x", claim_epoch=1,
        lease_until=datetime(2026, 5, 16, 9, 1, tzinfo=timezone.utc),
    )
    assert r.attempt_id == "ra_x"


def test_complete_attempt_request_status_enum():
    schemas.CompleteAttemptRequest(
        attempt_id="ra_x", claim_epoch=1, session_secret="a" * 64,
        status="wrapped",
    )
    with pytest.raises(Exception):
        schemas.CompleteAttemptRequest(
            attempt_id="ra_x", claim_epoch=1, session_secret="a" * 64,
            status="bogus",
        )


def test_predict_conflicts_request_optional_ids():
    r = schemas.PredictConflictsRequest(
        project="marketplace",
        declared_resources=[
            {"type": "path", "uri": "file:marketplace/src/auth/**", "intent": "write"},
        ],
    )
    assert r.work_item_id is None
    assert r.attempt_id is None


def test_predict_conflicts_response_severity():
    r = schemas.PredictConflictsResponse(severity="soft_block", predictions=[])
    assert r.severity == "soft_block"


def test_lock_request_resource_type_enum():
    schemas.AcquireLockRequest(
        attempt_id="ra_x", claim_epoch=1, session_secret="a" * 64,
        resource_type="git_branch", resource_key="x",
    )
    with pytest.raises(Exception):
        schemas.AcquireLockRequest(
            attempt_id="ra_x", claim_epoch=1, session_secret="a" * 64,
            resource_type="bogus", resource_key="x",
        )


def test_per_repo_state_state_enum():
    schemas.PerRepoState(
        type="repo", uri="repo:m", state="prepared", version=1,
    )
    with pytest.raises(Exception):
        schemas.PerRepoState(
            type="repo", uri="repo:m", state="not_a_state", version=1,
        )


def test_artifact_action_request():
    r = schemas.ArtifactActionRequest(
        attempt_id="ra_x", claim_epoch=1, session_secret="a" * 64,
        type="pr", identifier="1234", repo="marketplace",
    )
    assert r.type == "pr"


def test_error_envelope_default_details():
    e = schemas.ErrorEnvelope(code="UNAUTHORIZED", message="x")
    assert e.details == {}


def test_work_item_list_response_default_cursor():
    r = schemas.WorkItemListResponse(items=[], next_cursor=None)
    assert r.next_cursor is None


def test_emit_event_request_default_pinned_false():
    r = schemas.EmitEventRequest(
        attempt_id="ra_x", claim_epoch=1, session_secret="a" * 64,
        work_item_id="wi_x", event_type="note", payload={"x": 1},
    )
    assert r.pinned is False
