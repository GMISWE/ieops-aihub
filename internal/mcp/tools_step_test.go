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
