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
