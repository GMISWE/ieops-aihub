package mcp

import (
	"strings"
	"testing"

	"github.com/GMISWE/ieops-aihub/pkg/client"
)

// TestClassifyStepUpdateErr locks in the aihub#209 fix: a paused attempt must
// NOT delete the local state file (so the user can resume), while a stale
// epoch/attempt mismatch still does. Mirrors the wi's integration AC without a
// live server — the delete decision is the whole risk.
//
// ⚠️ THE INPUTS ARE TYPED ERRORS, NOT STRINGS, SINCE aihub#414 — and that is
// not a cosmetic port. This table used to feed errors.New("aihub 409 CODE: …")
// because the classifier read the rendered text, and by doing so it PINNED that
// mechanism: the case names below ("paused not treated as mismatch") show the
// author was already worrying about substring collisions between codes, while
// the collision that mattered was between a code and an observed VALUE — a step
// named "ATTEMPT_MISMATCH" deleted the caller's credential file. A table of
// hand-written strings could not express that case, so it never appeared.
//
// The aihub#209 contract asserted here is unchanged. The cases that could only
// exist once classification moved to the code field — a hostile step name, a
// token in the details blob, a longer code containing a shorter one — live in
// error_code_classification_test.go rather than being bolted on here.
func TestClassifyStepUpdateErr(t *testing.T) {
	cases := []struct {
		name       string
		status     int
		code       string
		message    string
		wantDelete bool
		wantSubs   []string // substrings required in the returned error
	}{
		{
			// AC: paused wi keeps state file + resume guidance.
			name:       "paused keeps state file",
			status:     409,
			code:       "ATTEMPT_PAUSED",
			message:    "attempt is paused; resume it before continuing",
			wantDelete: false,
			wantSubs:   []string{"paused", "resume", "--resume"},
		},
		{
			name:       "epoch mismatch deletes",
			status:     409,
			code:       "CONFLICT_EPOCH_MISMATCH",
			message:    "claim_epoch mismatch",
			wantDelete: true,
			wantSubs:   []string{"STALE_LOCAL_CREDENTIAL", "re-claim"},
		},
		{
			name:       "attempt mismatch deletes",
			status:     403,
			code:       "ATTEMPT_MISMATCH",
			message:    `attempt status is "superseded"`,
			wantDelete: true,
			wantSubs:   []string{"STALE_LOCAL_CREDENTIAL"},
		},
		{
			// Kept from the original table. It no longer guards a substring
			// collision — exact codes cannot collide — but it still asserts the
			// arm ordering is harmless, and it costs one line.
			name:       "paused not treated as mismatch",
			status:     409,
			code:       "ATTEMPT_PAUSED",
			message:    "paused",
			wantDelete: false,
		},
		{
			name:       "unrelated error passes through",
			status:     500,
			code:       "INTERNAL_ERROR",
			message:    "boom",
			wantDelete: false,
			wantSubs:   []string{"INTERNAL_ERROR"}, // original error preserved
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			in := &client.APIError{StatusCode: tc.status, Code: tc.code, Message: tc.message}
			out, del := classifyStepUpdateErr(in)
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
