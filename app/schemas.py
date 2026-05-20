"""Pydantic models for v3 routes — round-trip-conformant to openapi/aihub_openapi.yaml.

OpenAPI yaml is the SoT; this module mirrors it. Any drift is a bug.
"""
from __future__ import annotations

from datetime import datetime
from typing import Any, Literal

from pydantic import BaseModel, ConfigDict, Field

# ---- Enums (Literal types, kept in sync with openapi yaml) ----

ResourceType = Literal["repo", "path", "document", "section", "service", "external_ref"]
ResourceIntent = Literal["read", "write", "refactor", "delete"]
WorkItemStatus = Literal["queued", "running", "blocked", "paused", "wrapped", "failed"]
Priority = Literal["low", "normal", "high", "urgent"]
RunAttemptStatus = Literal["running", "superseded", "wrapped", "failed", "expired"]
LockResourceType = Literal["git_branch", "worktree", "file_scope", "tcp_port", "deploy_env"]
PerRepoStateValue = Literal[
    "declared", "prepared", "committed", "pushed", "pr_opened",
    "blocked_merge_conflict", "blocked_dirty_worktree", "failed", "skipped",
]
Severity = Literal["info", "warn", "soft_block", "hard_block"]
ArtifactType = Literal["pr", "branch", "issue"]
ArtifactState = Literal["open", "merged", "closed", "missing"]
ExternalShareType = Literal["jira", "github"]
SourceValue = Literal[
    "human", "auto:review", "auto:execute", "auto:debug",
    "auto:retro", "sync:jira", "sync:github", "admin",
]
Role = Literal["reader", "writer", "admin"]
Visibility = Literal["private", "project", "team", "admin"]


# ---- AttemptCredential (composed into mutation requests) ----

SESSION_SECRET_PATTERN = r"^[0-9a-f]{64}$"


class AttemptCredential(BaseModel):
    """三联 AC — every mutating endpoint that touches an active attempt carries this.
    Raw session_secret (64 hex chars); server sha256-hashes for compare."""
    attempt_id: str = Field(pattern=r"^ra_[a-z0-9]+$")
    claim_epoch: int = Field(ge=1)
    session_secret: str = Field(pattern=SESSION_SECRET_PATTERN, min_length=64, max_length=64)


# ---- Error envelope ----

class ErrorEnvelope(BaseModel):
    code: str
    message: str
    details: dict[str, Any] = Field(default_factory=dict)


# ---- DeclaredResource (intent-time, no runtime fields per §5.1 r3 split) ----

class DeclaredResource(BaseModel):
    type: ResourceType
    uri: str
    intent: ResourceIntent
    base_branch: str | None = None
    task_branch: str | None = None
    worktree_path_hint: str | None = None
    metadata: dict[str, Any] = Field(default_factory=dict)


# ---- PerRepoState (runtime) ----

class PerRepoState(BaseModel):
    type: ResourceType
    uri: str
    intent: ResourceIntent | None = None
    state: PerRepoStateValue
    version: int = Field(ge=0)
    base_branch: str | None = None
    task_branch: str | None = None
    last_sha: str | None = Field(default=None, pattern=r"^[0-9a-f]{7,40}$")
    last_pr_number: int | None = None
    error: str | None = None
    metadata: dict[str, Any] = Field(default_factory=dict)


# ---- WorkItem ----

class WorkItem(BaseModel):
    id: str = Field(pattern=r"^wi_[a-z0-9]+$")
    project: str
    scenario: str
    goal: str
    status: WorkItemStatus
    priority: Priority
    labels: list[str]
    reporter_user_id: str
    current_attempt_id: str | None = None
    declared_resources: list[DeclaredResource]
    resources_version: int
    external_share_type: ExternalShareType | None = None
    external_share_key: str | None = None
    parent_work_item_id: str | None = None
    metadata: dict[str, Any] = Field(default_factory=dict)
    created_at: datetime
    updated_at: datetime
    closed_at: datetime | None = None


class RunAttempt(BaseModel):
    id: str = Field(pattern=r"^ra_[a-z0-9]+$")
    work_item_id: str
    status: RunAttemptStatus
    claim_epoch: int = Field(ge=1)
    idempotency_key: str
    lease_until: datetime | None = None
    actor_user_id: str
    api_key_id: str
    actor_display: str
    machine_id: str
    session_secret_hash: str
    parent_attempt_id: str | None = None
    prepared_workspace: dict[str, Any] | None = None
    started_at: datetime
    ended_at: datetime | None = None


class AgentEvent(BaseModel):
    id: str
    work_item_id: str
    run_attempt_id: str | None = None
    actor_user_id: str
    api_key_id: str | None = None
    event_type: str
    payload: dict[str, Any] = Field(default_factory=dict)
    pinned: bool = False
    created_at: datetime


# ---- Memory ----

class Memory(BaseModel):
    id: str
    project: str
    author_user_id: str
    work_item_id: str | None = None
    visibility: Visibility
    type: str
    content: str | None = None
    metadata: dict[str, Any] = Field(default_factory=dict)
    embedding: list[float] | None = None
    redacted_at: datetime | None = None
    redaction_reason: str | None = None
    expires_at: datetime | None = None
    created_at: datetime


# ---- ConflictPrediction ----

class ConflictWith(BaseModel):
    work_item_id: str
    attempt_id: str
    actor_user_id: str
    actor_display: str


class ConflictPrediction(BaseModel):
    rule_id: str
    severity: Severity
    resource_uri: str
    conflicts_with: ConflictWith
    message: str


# ---- ExternalArtifactState ----

class ExternalArtifactStateModel(BaseModel):
    type: ArtifactType
    identifier: str
    state: ArtifactState
    drift_detected: bool
    detail: dict[str, Any] = Field(default_factory=dict)


# ---- Request bodies ----

class CreateWorkItemRequest(BaseModel):
    project: str
    scenario: str
    goal: str
    declared_resources: list[DeclaredResource] = Field(default_factory=list)
    labels: list[str] = Field(default_factory=list)
    priority: Priority | None = None
    external_share_type: ExternalShareType | None = None
    external_share_key: str | None = None
    parent_work_item_id: str | None = None
    source: SourceValue | None = None
    metadata: dict[str, Any] = Field(default_factory=dict)


class UpdateWorkItemRequest(AttemptCredential):
    patch_payload: dict[str, Any]
    expected_resources_version: int | None = None  # CAS guard for declared_resources writes


class RequestedLock(BaseModel):
    resource_type: LockResourceType
    resource_key: str


class SessionInfo(BaseModel):
    machine_id: str
    session_secret: str = Field(pattern=SESSION_SECRET_PATTERN, min_length=64, max_length=64)


class ClaimRequest(BaseModel):
    idempotency_key: str
    session_info: SessionInfo
    requested_locks: list[RequestedLock] = Field(default_factory=list)
    force_takeover: bool = False


class ClaimResponse(BaseModel):
    attempt_id: str
    claim_epoch: int
    lease_until: datetime | None = None


class RenewLeaseRequest(BaseModel):
    claim_epoch: int = Field(ge=1)
    session_secret: str = Field(pattern=SESSION_SECRET_PATTERN, min_length=64, max_length=64)


class LeaseRenewResponse(BaseModel):
    lease_until: datetime | None = None


class CompleteAttemptRequest(AttemptCredential):
    status: Literal["wrapped", "failed"]


class PauseAttemptRequest(AttemptCredential):
    reason: str


class CompleteWorkItemRequest(AttemptCredential):
    final_status: Literal["wrapped", "failed"]


class AcquireLockRequest(AttemptCredential):
    resource_type: LockResourceType
    resource_key: str


class EmitEventRequest(AttemptCredential):
    work_item_id: str
    event_type: str
    payload: dict[str, Any] = Field(default_factory=dict)
    pinned: bool = False


class PredictConflictsRequest(BaseModel):
    project: str
    work_item_id: str | None = None
    attempt_id: str | None = None
    declared_resources: list[DeclaredResource]


class PredictConflictsResponse(BaseModel):
    severity: Severity
    predictions: list[ConflictPrediction]


class ArtifactActionRequest(AttemptCredential):
    type: ArtifactType
    identifier: str
    repo: str
    expected_resources_version: int | None = None  # Optional CAS guard (§7.6)


class OkResponse(BaseModel):
    ok: bool = True


class EmitEventResponse(BaseModel):
    event_id: str


class WhoAmIResponse(BaseModel):
    user_id: str
    role: Role
    projects: list[str]


class WorkItemDetailResponse(BaseModel):
    work_item: WorkItem
    current_attempt: RunAttempt | None = None
    recent_events: list[AgentEvent]
    per_repo_state: list[PerRepoState]

    model_config = ConfigDict(arbitrary_types_allowed=True)


class WorkItemListResponse(BaseModel):
    items: list[WorkItem]
    next_cursor: str | None = None


class EventListResponse(BaseModel):
    items: list[AgentEvent]
    next_cursor: str | None = None


class MemoryListResponse(BaseModel):
    items: list[Memory]
    next_cursor: str | None = None
