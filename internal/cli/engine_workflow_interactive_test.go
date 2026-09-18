package cli

import (
	"context"
	"errors"
	"strings"
	"testing"
)

type interactiveHoldWorkflowClient struct {
	startCalls int
}

func (c *interactiveHoldWorkflowClient) GetWorkItemWorkflow(context.Context, string) (map[string]any, error) {
	return map[string]any{
		"work_item_id":           "wi_interactive",
		"steps_version":          1,
		"requires_human_session": true,
		"steps": map[string]any{"version": 1, "steps": []any{
			map[string]any{
				"id":            "spec",
				"skill_id":      "skill_spec",
				"skill_version": 1,
				"rhs":           true,
				"models": []any{
					map[string]any{"harness": "pi", "model": "provider/model", "effort": "high"},
				},
			},
		}},
		"progress": []any{
			map[string]any{"step_id": "spec", "status": "completed", "step_attempt_id": "sa_spec", "rhs": true, "approved": false},
		},
	}, nil
}

func (c *interactiveHoldWorkflowClient) StartWorkflowStep(context.Context, string, any) (map[string]any, error) {
	c.startCalls++
	return nil, errors.New("must not start while approval is required")
}
func (*interactiveHoldWorkflowClient) RecordWorkflowResult(context.Context, string, any) (map[string]any, error) {
	return nil, errors.New("must not record while approval is required")
}
func (*interactiveHoldWorkflowClient) Remember(context.Context, any) (map[string]any, error) {
	return nil, errors.New("must not save an artifact while approval is required")
}
func (*interactiveHoldWorkflowClient) GetMemory(context.Context, string) (map[string]any, error) {
	return nil, errors.New("must not fetch an artifact while approval is required")
}
func (c *interactiveHoldWorkflowClient) GetSkillVersion(context.Context, string, int) (map[string]any, error) {
	// Rejection is a legitimate revision hold, so the route must still bind
	// the pinned skill. Keep this response exact enough to prove access was not
	// bypassed: this interactive spec-session contract is deliberately
	// non-writing, so it remains in the explicit main-session prepare path
	// without manufacturing an implementation write that needs downstream
	// review and verification gates.
	return map[string]any{
		"contract": map[string]any{
			"capabilities": []any{"deterministic_operation"},
			"runtime":      map[string]any{"interactive": true},
		},
	}, nil
}
func (*interactiveHoldWorkflowClient) ReconcileWorkflowInvocations(context.Context, string, any) (map[string]any, error) {
	return nil, errors.New("must not reconcile while approval is required")
}

// TestEngineWorkflowInteractiveRejectionIsAHoldNotSuccess pins the deliberately
// incomplete interactive adapter contract. Even with --execute, an artifact-
// bound human rejection is returned as revision_required; no worker starts
// and the output cannot be mistaken for whole-flow completion.
func TestEngineWorkflowInteractiveRejectionIsAHoldNotSuccess(t *testing.T) {
	api := &interactiveHoldWorkflowClient{}
	out, err := engineWorkflowDrive(t.Context(), api, "wi_interactive", engineWorkflowOptions{Execute: true})
	if err != nil {
		t.Fatalf("engineWorkflowDrive: %v", err)
	}
	if out.Status != "prepare_required" && out.Status != "revision_required" {
		t.Fatalf("interactive rejection output = %+v; want an explicit non-success hold", out)
	}
	detail := strings.ToLower(out.Detail)
	if out.StepID != "spec" || (!strings.Contains(detail, "prepare") && !strings.Contains(detail, "revise") && !strings.Contains(detail, "rejected")) {
		t.Fatalf("revision hold lacks actionable rejection disclosure: %+v", out)
	}
	if out.Result != nil || api.startCalls != 0 {
		t.Fatalf("revision hold manufactured execution: result=%+v starts=%d", out.Result, api.startCalls)
	}
}
