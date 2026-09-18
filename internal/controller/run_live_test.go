package controller

import (
	"context"
	"errors"
	"net/url"
	"strings"
	"testing"
)

type liveOpenAPI struct{}

func (liveOpenAPI) GetWorkItemWorkflow(context.Context, string) (map[string]any, error) {
	return map[string]any{
		"work_item_id": "wi_test", "steps_version": 1,
		"requires_human_session": false,
		"steps": map[string]any{"version": 1, "steps": []any{map[string]any{
			"id": "work", "skill_id": "skill-live", "skill_version": 1,
			"models": []any{map[string]any{"harness": "cc", "model": "fake", "effort": "high"}},
		}}},
		"progress": []any{map[string]any{
			"step_id": "work", "open_invocation_id": "inv_live", "rhs": false,
		}},
	}, nil
}

func (liveOpenAPI) ReadEvents(context.Context, url.Values) (map[string]any, error) {
	return map[string]any{"events": []any{}}, nil
}

func (liveOpenAPI) GetMemory(context.Context, string) (map[string]any, error) {
	return nil, errors.New("fixture has no artifacts")
}

func (liveOpenAPI) GetSkillVersion(context.Context, string, int) (map[string]any, error) {
	return nil, errors.New("fixture has no skill versions")
}

func (liveOpenAPI) ReconcileWorkflowInvocations(context.Context, string, any) (map[string]any, error) {
	return nil, errors.New("fixture must not reconcile a live invocation")
}

func (liveOpenAPI) StartWorkflowStep(context.Context, string, any) (map[string]any, error) {
	return nil, errors.New(`step already has an open invocation from attempt ra_live whose status is "running"`)
}

func (liveOpenAPI) RecordWorkflowResult(context.Context, string, any) (map[string]any, error) {
	return nil, errors.New("fixture must not record a live invocation")
}

func TestDriverLiveOpenInvocationRetainsClaim(t *testing.T) {
	var pauses, completes, cleanups int
	d := &driver{
		api: liveOpenAPI{},
		opts: RunOptions{
			WorkItemID: "wi_test", Credentials: Credentials{AttemptID: "ra_current", ClaimEpoch: 1, SessionSecret: "secret"},
			PauseAttempt:     func(context.Context, string) error { pauses++; return nil },
			CompleteAttempt:  func(context.Context, string, string) error { completes++; return nil },
			CleanupWorktrees: func(context.Context) error { cleanups++; return nil },
		},
	}
	res := d.executeStep(context.Background(), "work", nil)
	if res.Status != RunPaused || !strings.Contains(res.Err, "remains CLAIMED") {
		t.Fatalf("expected retained-claim hold, got %+v", res)
	}
	if pauses != 0 || completes != 0 || cleanups != 0 {
		t.Fatalf("live invocation mutated lifecycle: pause=%d complete=%d cleanup=%d", pauses, completes, cleanups)
	}
}
