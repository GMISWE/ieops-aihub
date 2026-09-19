package client

// workflows.go — client methods for the WI-owned workflow (aihub#708 Batch
// 2A). They mirror the client's established shape (map-typed bodies and
// responses, path-escaped work-item refs) so the MCP layer and any CLI worker
// can call the same endpoints with the same envelopes the HTTP API defines.
//
// Credentials travel in the body exactly the way pf_update_step's do — the
// MCP tool layer injects them from the state file; a raw HTTP caller supplies
// them explicitly. The server derives everything else (grants, identities,
// ordering) itself.

import (
	"context"
	"net/http"
)

// GetWorkItemWorkflow calls GET /v1/work_items/:id/workflow — the current
// pinned flow, the append-only generation history, per-step progress with
// approval state, and the open repair authorizations.
func (c *Client) GetWorkItemWorkflow(ctx context.Context, wiID string) (map[string]any, error) {
	var out map[string]any
	return out, c.do(ctx, http.MethodGet, "/v1/work_items/"+seg(wiID)+"/workflow", nil, &out)
}

// UpdateWorkItemWorkflow calls PUT /v1/work_items/:id/workflow — revise (or
// first pin) the workflow. body must carry expected_steps_version (the CAS
// token: 0 for a work item with no workflow), an explicit requires_human_session,
// and steps. SkillVersion 0 in a step means "pin the caller's latest accessible
// version"; the server resolves and validates atomically.
func (c *Client) UpdateWorkItemWorkflow(ctx context.Context, wiID string, body any) (map[string]any, error) {
	var out map[string]any
	return out, c.do(ctx, http.MethodPut, "/v1/work_items/"+seg(wiID)+"/workflow", body, &out)
}

// StartWorkflowStep calls POST /v1/work_items/:id/workflow/start. body carries
// the attempt credentials, step_id, and optionally repair_episode_id. The
// response is the server-minted invocation descriptor (step_attempt_id,
// producer_id, grant, pinned skill ref, models, params, inputs, effective_rhs)
// the worker result must echo exactly.
func (c *Client) StartWorkflowStep(ctx context.Context, wiID string, body any) (map[string]any, error) {
	var out map[string]any
	return out, c.do(ctx, http.MethodPost, "/v1/work_items/"+seg(wiID)+"/workflow/start", body, &out)
}

// RecordWorkflowResult calls POST /v1/work_items/:id/workflow/result. body
// carries the attempt credentials and the full structured StepResult envelope
// (work_item_id, flow_version, step_id, step_attempt_id, epoch, producer_id,
// status, review_verdict, artifact, evidence). A worker result may not supply
// an approval — the server rejects the envelope outright.
func (c *Client) RecordWorkflowResult(ctx context.Context, wiID string, body any) (map[string]any, error) {
	var out map[string]any
	return out, c.do(ctx, http.MethodPost, "/v1/work_items/"+seg(wiID)+"/workflow/result", body, &out)
}

// ApproveWorkflowStep calls POST /v1/work_items/:id/workflow/approve. Only an
// authenticated HUMAN caller may approve; body names the exact steps_version,
// step_id and artifact {id, version, hash} of the step's latest recorded
// result, plus decision approved|rejected.
func (c *Client) ApproveWorkflowStep(ctx context.Context, wiID string, body any) (map[string]any, error) {
	var out map[string]any
	return out, c.do(ctx, http.MethodPost, "/v1/work_items/"+seg(wiID)+"/workflow/approve", body, &out)
}

// AuthorizeWorkflowRepair calls POST /v1/work_items/:id/workflow/repair. body
// carries the attempt credentials, the failed_step_attempt_id, kind
// (retry|episode) and a reason; the opened authorization is what later starts
// bind their invocations to.
func (c *Client) AuthorizeWorkflowRepair(ctx context.Context, wiID string, body any) (map[string]any, error) {
	var out map[string]any
	return out, c.do(ctx, http.MethodPost, "/v1/work_items/"+seg(wiID)+"/workflow/repair", body, &out)
}

// ReconcileWorkflowInvocations calls POST /v1/work_items/:id/workflow/reconcile
// — the explicit controller-reconcile transition (aihub#708 B3). body carries
// the CURRENT attempt's credentials plus supersede_attempt_id naming the
// paused/taken-over attempt whose OPEN invocations are being abandoned; the
// server verifies the named attempt's own row (it must belong to this work
// item and not be running) before superseding anything.
func (c *Client) ReconcileWorkflowInvocations(ctx context.Context, wiID string, body any) (map[string]any, error) {
	var out map[string]any
	return out, c.do(ctx, http.MethodPost, "/v1/work_items/"+seg(wiID)+"/workflow/reconcile", body, &out)
}

// ─── aihub#720: typed create-with-workflow surface ─────────────────────────
//
// The map-typed CreateWorkItem above stays the raw transport. The types below
// are the typed mirror of the composition vocabulary the server accepts on
// POST /v1/work_items (steps + workflow_mode, aihub#720) and PUT
// /v1/work_items/:id/workflow — the SAME per-step shape both endpoints share
// (domain.WorkflowStepSpec). They exist so a Go composer gets compile-time
// field names instead of a map[string]any; they add NO client-side policy. The
// server remains the only place a proposal is resolved, validated and pinned.

// WorkflowModelCandidate is the client-side mirror of the server's ordered
// model candidate (internal/workflow.ModelCandidate): harness, model and
// effort. The server validates each candidate against the step's pinned
// contract — a candidate the contract does not allow is a COMPOSE_FAILED
// refusal, never a client-side rewrite.
type WorkflowModelCandidate struct {
	Harness string `json:"harness"`
	Model   string `json:"model"`
	Effort  string `json:"effort"`
}

// WorkflowInputRef is the client-side mirror of an input reference: wire an
// EARLIER step's output (by step id and output name) into a later step under
// a param name. Forward references are refused server-side.
type WorkflowInputRef struct {
	Name   string `json:"name"`
	StepID string `json:"step_id"`
	Output string `json:"output"`
}

// WorkflowStep is one step of a composition proposal. SkillVersion 0 means
// "pin the caller's latest accessible version" — the server freezes the exact
// number at pin time. RHS is a pointer so an omitted human gate (nil) stays
// distinct from an explicit false; Params is any JSON-encodable value. The
// server derives grants from the pinned contract and ignores nothing silently.
type WorkflowStep struct {
	ID           string                   `json:"id"`
	SkillID      string                   `json:"skill_id"`
	SkillVersion int                      `json:"skill_version"`
	RHS          *bool                    `json:"rhs,omitempty"`
	Models       []WorkflowModelCandidate `json:"models"`
	Params       any                      `json:"params,omitempty"`
	Inputs       []WorkflowInputRef       `json:"inputs,omitempty"`
}

// CreateWorkItemWithWorkflowRequest is the typed body for the aihub#720
// create-time composition path of POST /v1/work_items. The mode rules are the
// server's (resolveCreateWorkflowMode) and are restated here only as a map:
//
//	steps present            → workflow_mode must be "" or "db"; generation 1
//	                          is pinned in the same transaction, mode lands 'db'
//	workflow_mode "pending"  → steps MUST be absent; the row is filed for an
//	                          orchestrator and refuses claims (COMPOSE_PENDING)
//	                          until a first generation is pinned
//	workflow_mode "legacy"   → steps MUST be absent; explicit scenario-graph
//	                          opt-in, the compatibility path
//	neither                   → legacy create, byte-identical to pre-aihub#720
//
// Any other combination answers 400 COMPOSE_FAILED with a machine-readable
// details.reason. RequiresHumanSession is a pointer for the same three-state
// reason as everywhere else: nil omits the field (the legacy NULL/unclassified
// create), set pins the classification explicitly — and the server REQUIRES it
// explicitly whenever steps are present.
type CreateWorkItemWithWorkflowRequest struct {
	Project              string   `json:"project"`
	Goal                 string   `json:"goal"`
	WIType               string   `json:"wi_type,omitempty"`
	Scenario             string   `json:"scenario,omitempty"`
	Priority             string   `json:"priority,omitempty"`
	Milestone            string   `json:"milestone,omitempty"`
	Labels               []string `json:"labels,omitempty"`
	Content              string   `json:"content,omitempty"`
	RequiresHumanSession *bool    `json:"requires_human_session,omitempty"`
	WorkflowMode         string   `json:"workflow_mode,omitempty"`
	// Steps carries the composition as a POINTER so three states stay distinct
	// on the wire: nil (field omitted — pending/legacy create, no composition),
	// &[]WorkflowStep{} (explicit EMPTY composition — the server answers
	// COMPOSE_FAILED, db_mode_requires_steps, because an empty pin is a
	// contradiction, not a request for the default), and a non-empty pointer
	// (compose and pin generation 1). A plain slice with omitempty would
	// silently rewrite the second into the first — the exact silent-degradation
	// shape the composition boundary exists to refuse (Astra review B1).
	Steps *[]WorkflowStep `json:"steps,omitempty"`
}

// CreateWorkItemWithWorkflow calls POST /v1/work_items with the typed
// composition request above. Composition is server-owned: on success the
// response carries the created work item with its explicit workflow_mode and
// (when steps were supplied) steps_version=1; on refusal it is an *APIError
// with Code COMPOSE_FAILED (or COMPOSE_PENDING from the claim gate), and the
// machine-readable reason in Details — never a silently-legacy work item.
func (c *Client) CreateWorkItemWithWorkflow(ctx context.Context, req *CreateWorkItemWithWorkflowRequest) (map[string]any, error) {
	var out map[string]any
	return out, c.do(ctx, http.MethodPost, "/v1/work_items", req, &out)
}
