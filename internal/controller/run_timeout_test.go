package controller

import (
	"context"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/GMISWE/ieops-aihub/internal/workflow"
)

type timeoutRunAPI struct{ *fakeWorkflowAPI }

func (a timeoutRunAPI) ReadEvents(context.Context, url.Values) (map[string]any, error) {
	return map[string]any{"events": []any{}}, nil
}

// TestDriverStepTimeoutStopsThenPauses pins the Batch5 drain regression: a
// short step deadline must stop the process group before invoking the pause
// lifecycle seam. It may not report a completed step or fall through to a
// second candidate.
func TestDriverStepTimeoutStopsThenPauses(t *testing.T) {
	env := newRunStepTestEnv(t, "sleep 30\n")
	var pauses int
	d := &driver{
		api: timeoutRunAPI{env.api},
		opts: RunOptions{
			WorkItemID:  "wi_fake",
			Credentials: Credentials{AttemptID: "ra_fake", ClaimEpoch: 7, SessionSecret: "sec"},
			WorkDir:     t.TempDir(), RunDir: t.TempDir(), StepTimeout: 40 * time.Millisecond,
			PauseAttempt: func(context.Context, string) error { pauses++; return nil },
		},
		grants: map[string]workflow.StepGrant{
			"review-code": {Authority: workflow.AuthorityWrite, ProducerIsolation: workflow.IsolationShared},
		},
		stepsVersion: 3,
		hist:         &History{},
		logf:         func(string, ...any) {},
	}

	res := d.executeStep(context.Background(), "review-code", nil)
	if res.Status != RunPaused || pauses != 1 {
		t.Fatalf("timeout result = %+v, pauses=%d; want one confirmed pause", res, pauses)
	}
	if !strings.Contains(res.Err, "timed out") || !strings.Contains(res.Err, "process group stopped") {
		t.Fatalf("timeout hold does not record the confirmed stop: %q", res.Err)
	}
	if res.Steps != 0 || len(env.api.recorded) != 0 {
		t.Fatalf("timeout manufactured success: result=%+v recorded=%+v", res, env.api.recorded)
	}
	if starts := env.harnessStarts(t); starts != 1 {
		t.Fatalf("timeout started %d candidates; no fallback is allowed over an open invocation", starts)
	}
}

// TestDriverUnconfirmedStopRetainsClaimWithoutPause is the companion safety
// contract for a stop failure: no lifecycle seam is invoked while a worker may
// still mutate the worktree. The concrete process-group stop implementation is
// covered by internal/modelruntime's stubborn-process tests; this pins the
// controller/drain-facing outcome.
func TestDriverUnconfirmedStopRetainsClaimWithoutPause(t *testing.T) {
	var pauses int
	d := &driver{opts: RunOptions{PauseAttempt: func(context.Context, string) error { pauses++; return nil }}}
	res := d.retainClaim("worker stop after step deadline was not confirmed")
	if res.Status != RunPaused || pauses != 0 {
		t.Fatalf("unconfirmed stop result = %+v, pauses=%d", res, pauses)
	}
	if !strings.Contains(res.Err, "remains CLAIMED") || !strings.Contains(res.Err, "locks held") {
		t.Fatalf("retained-claim guidance missing: %q", res.Err)
	}
}
