package mcp

import (
	"errors"
	"strings"
	"testing"
)

// TestClassifyStepUpdateErr locks in the aihub#209 fix: a paused attempt must
// NOT delete the local state file (so the user can resume), while a stale
// epoch/attempt mismatch still does. Mirrors the wi's integration AC without a
// live server — the delete decision is the whole risk.
func TestClassifyStepUpdateErr(t *testing.T) {
	cases := []struct {
		name       string
		errStr     string
		wantDelete bool
		wantSubs   []string // substrings required in the returned error
	}{
		{
			// AC: paused wi keeps state file + resume guidance.
			name:       "paused keeps state file",
			errStr:     "aihub 409 ATTEMPT_PAUSED: attempt is paused; resume it before continuing",
			wantDelete: false,
			wantSubs:   []string{"paused", "resume", "--resume"},
		},
		{
			name:       "epoch mismatch deletes",
			errStr:     "aihub 409 CONFLICT_EPOCH_MISMATCH: claim_epoch mismatch",
			wantDelete: true,
			wantSubs:   []string{"STALE_LOCAL_CREDENTIAL", "re-claim"},
		},
		{
			name:       "attempt mismatch deletes",
			errStr:     "aihub 403 ATTEMPT_MISMATCH: attempt status is \"superseded\"",
			wantDelete: true,
			wantSubs:   []string{"STALE_LOCAL_CREDENTIAL"},
		},
		{
			// Guard against a substring collision: ATTEMPT_PAUSED must not be
			// swept up by the ATTEMPT_MISMATCH branch.
			name:       "paused not treated as mismatch",
			errStr:     "aihub 409 ATTEMPT_PAUSED: paused",
			wantDelete: false,
		},
		{
			name:       "unrelated error passes through",
			errStr:     "aihub 500 INTERNAL_ERROR: boom",
			wantDelete: false,
			wantSubs:   []string{"INTERNAL_ERROR"}, // original error preserved
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out, del := classifyStepUpdateErr(errors.New(tc.errStr))
			if del != tc.wantDelete {
				t.Fatalf("deleteState = %v, want %v", del, tc.wantDelete)
			}
			for _, sub := range tc.wantSubs {
				if !strings.Contains(out.Error(), sub) {
					t.Errorf("error %q missing %q", out.Error(), sub)
				}
			}
		})
	}
}

// TestValidateTerminalStepArgs_MCP is the hop-1 half of aihub#399: the
// requirement itself, at the layer that publishes it.
//
// The schema has said step_attempt_id is "required for completed/failed" since
// aihub#265, and until aihub#399 neither hop enforced it — which is how the
// field became optional in practice. updateStepBody forwards the key only when
// non-empty, so an agent that simply omits the argument sends no key at all and
// the server used to answer 200 having filed no history row.
func TestValidateTerminalStepArgs_MCP(t *testing.T) {
	cases := []struct {
		status, stepAttemptID string
		wantRejected          bool
	}{
		{"completed", "sa_01JQ", false},
		{"failed", "sa_01JQ", false},
		{"completed", "", true},
		{"failed", "", true},
		{"completed", "   ", true},
		// in_progress files no history row: a bare start is how pf-execute's loop
		// opens a step graph and must stay legal.
		{"in_progress", "", false},
		{"in_progress", "sa_01JQ", false},
		// An unrecognised status is the server's business, not this check's.
		{"banana", "", false},
		{"", "", false},
	}
	for _, tc := range cases {
		err := validateTerminalStepArgs(tc.status, tc.stepAttemptID)
		if tc.wantRejected {
			if err == nil {
				t.Errorf("validateTerminalStepArgs(%q, %q) accepted a terminal transition with no usable "+
					"step_attempt_id", tc.status, tc.stepAttemptID)
				continue
			}
			if !strings.Contains(err.Error(), "step_attempt_id") {
				t.Errorf("the error must name the field; got %q", err.Error())
			}
			if !strings.Contains(err.Error(), tc.status) {
				t.Errorf("the error must name the status it applies to; got %q", err.Error())
			}
			continue
		}
		if err != nil {
			t.Errorf("validateTerminalStepArgs(%q, %q) rejected a legal call: %v", tc.status, tc.stepAttemptID, err)
		}
	}
}
