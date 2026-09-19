package domain

// create_work_item_workflow_mode_test.go — the non-DB half of aihub#720
// slice B: the create-time composition-mode boundary. Everything here is
// reachable before pool.Begin (the nil-pool technique of
// TestCreateWorkItem_RejectsUnknownTypeBeforeTouchingDB) or pure, so no
// database is needed.
//
// The DB-gated half (mode persistence on the real INSERT, the pending claim
// refusal) lives in create_work_item_workflow_mode_db_test.go and is NOT run
// in this slice; the pin path itself is already held by workflow_db_test.go's
// arms (TestWorkflowCreatePinsAtomically and siblings), which slice B does not
// duplicate — resolveCreateWorkflowMode and composeFailedFromPin are the ONLY
// new rules, and they are the ones below.
//
//	GOWORK=off go test ./internal/domain/ -run 'TestResolveCreateWorkflowMode|TestComposeFailed|TestCreateWorkItem.*Mode' -count=1 -v

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// TestResolveCreateWorkflowMode is the combination table, one arm per cell.
//
// The two halves of the fail-closed requirement this exists to pin:
//
//   - no cell resolves to legacy silently — legacy is returned ONLY when the
//     caller asked for it (explicitly or by omitting both mode and steps),
//     which is what keeps pre-aihub#720 callers byte-identical while making
//     every OTHER combination an error;
//   - every refusal is COMPOSE_FAILED with a NON-EMPTY machine-readable
//     reason, so a composer branches on details.reason, not on message prose.
func TestResolveCreateWorkflowMode(t *testing.T) {
	cases := []struct {
		name       string
		requested  string
		hasSteps   bool
		wantMode   string // "" when a refusal is expected
		wantReason string
	}{
		// ── steps present: db is the only destination ─────────────────────
		{"steps plus omitted mode pins db", "", true, "db", ""},
		{"steps plus explicit db pins db", "db", true, "db", ""},
		{"steps plus legacy is refused", "legacy", true, "", "steps_with_legacy_mode"},
		{"steps plus pending is refused", "pending", true, "", "steps_with_pending_mode"},
		// ── no steps: the legacy default survives untouched ────────────────
		{"no steps and no mode keeps legacy", "", false, "legacy", ""},
		{"no steps plus explicit legacy", "legacy", false, "legacy", ""},
		{"no steps plus pending parks for orchestration", "pending", false, "pending", ""},
		{"db without steps is refused", "db", false, "", "db_mode_requires_steps"},
		// ── the vocabulary itself ─────────────────────────────────────────
		{"an out-of-vocabulary mode is refused", "banana", false, "", "invalid_workflow_mode"},
		{"an out-of-vocabulary mode is refused even with steps", "banana", true, "", "invalid_workflow_mode"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, aerr := resolveCreateWorkflowMode(tc.requested, tc.hasSteps)
			if tc.wantMode != "" {
				if aerr != nil {
					t.Fatalf("resolveCreateWorkflowMode(%q, hasSteps=%v) refused a legal combination: %s",
						tc.requested, tc.hasSteps, aerr.Error())
				}
				if got != tc.wantMode {
					t.Errorf("mode = %q, want %q", got, tc.wantMode)
				}
				return
			}
			if aerr == nil {
				t.Fatalf("resolveCreateWorkflowMode(%q, hasSteps=%v) returned %q, want a refusal",
					tc.requested, tc.hasSteps, got)
			}
			if aerr.Code != ErrComposeFailed {
				t.Errorf("code = %s, want COMPOSE_FAILED (got message %q)", aerr.Code, aerr.Message)
			}
			details, ok := aerr.Details.(map[string]any)
			if !ok {
				t.Fatalf("COMPOSE_FAILED must carry a details object; got %T (%v)", aerr.Details, aerr.Details)
			}
			reason, _ := details["reason"].(string)
			if reason == "" {
				t.Errorf("details.reason is empty: COMPOSE_FAILED must carry a non-empty machine-readable reason")
			}
			if tc.wantReason != "" && reason != tc.wantReason {
				t.Errorf("details.reason = %q, want %q", reason, tc.wantReason)
			}
		})
	}
}

// TestCreateWorkItem_RejectsModeContradictionsBeforeTouchingDB drives the REAL
// CreateWorkItem against a nil pool with each contradictory combination and
// asserts the refusal arrives before the pool is ever touched — the same
// technique as TestCreateWorkItem_RejectsUnknownTypeBeforeTouchingDB, and the
// load-bearing half: a mode contradiction that only surfaced after Begin would
// spend the embedding round-trip on a doomed request and (worse) leave the
// classification of WHERE it failed ambiguous.
func TestCreateWorkItem_RejectsModeContradictionsBeforeTouchingDB(t *testing.T) {
	steps := []WorkflowStepSpec{{ID: "s1", SkillID: "skill_probe", SkillVersion: 1}}
	for _, tc := range []struct {
		name string
		req  *CreateWorkItemRequest
	}{
		{"steps with legacy mode", &CreateWorkItemRequest{
			Project: "aihub", Goal: "probe", Steps: steps, WorkflowMode: "legacy",
			DeclaredResources: json.RawMessage("[]"), Attrs: json.RawMessage("{}"),
		}},
		{"steps with pending mode", &CreateWorkItemRequest{
			Project: "aihub", Goal: "probe", Steps: steps, WorkflowMode: "pending",
			DeclaredResources: json.RawMessage("[]"), Attrs: json.RawMessage("{}"),
		}},
		{"db mode without steps", &CreateWorkItemRequest{
			Project: "aihub", Goal: "probe", WorkflowMode: "db",
			DeclaredResources: json.RawMessage("[]"), Attrs: json.RawMessage("{}"),
		}},
		{"an out-of-vocabulary mode", &CreateWorkItemRequest{
			Project: "aihub", Goal: "probe", WorkflowMode: "hybrid",
			DeclaredResources: json.RawMessage("[]"), Attrs: json.RawMessage("{}"),
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			wi, err := CreateWorkItem(context.Background(), nil, tc.req, "u_probe", "probe", nil, "")
			if err == nil {
				t.Fatalf("CreateWorkItem accepted a contradictory workflow_mode; wi=%+v", wi)
			}
			if err.Code != ErrComposeFailed {
				t.Errorf("code = %s, want COMPOSE_FAILED (message %q)", err.Code, err.Message)
			}
			if err.HTTPStatus != 400 {
				t.Errorf("HTTPStatus = %d, want 400", err.HTTPStatus)
			}
			if d, ok := err.Details.(map[string]any); !ok || d["reason"] == "" || d["reason"] == nil {
				t.Errorf("COMPOSE_FAILED must carry a non-empty details.reason; got %v", err.Details)
			}
		})
	}
}

// TestCreateWorkItemWithSteps_MissingClassificationIsComposeFailed is the
// same nil-pool arm for the classification guard, which slice B moved ABOVE
// the transaction: a steps-bearing create without an explicit
// requires_human_session must answer COMPOSE_FAILED (not the plain 400 it was
// before aihub#720) before the pool is touched.
func TestCreateWorkItemWithSteps_MissingClassificationIsComposeFailed(t *testing.T) {
	req := &CreateWorkItemRequest{
		Project: "aihub", Goal: "probe",
		Steps:                []WorkflowStepSpec{{ID: "s1", SkillID: "skill_probe", SkillVersion: 1}},
		RequiresHumanSession: nil, // the omission under test
		DeclaredResources:    json.RawMessage("[]"),
		Attrs:                json.RawMessage("{}"),
	}
	_, err := CreateWorkItem(context.Background(), nil, req, "u_probe", "probe", nil, "")
	if err == nil {
		t.Fatal("CreateWorkItem accepted a steps-bearing create with no explicit requires_human_session")
	}
	if err.Code != ErrComposeFailed {
		t.Errorf("code = %s, want COMPOSE_FAILED (message %q)", err.Code, err.Message)
	}
	if !strings.Contains(err.Message, "requires_human_session") {
		t.Errorf("the refusal should name the missing field; got %q", err.Message)
	}
}

// TestComposeFailedFromPin pins the re-typing boundary: the pin path's own
// typed refusals become COMPOSE_FAILED carrying the original message as the
// reason, and everything else — a database failure, a wiring defect — passes
// through untouched, because re-typing those would promise a caller its flow
// was invalid when the server was.
func TestComposeFailedFromPin(t *testing.T) {
	t.Run("a bad-request composition refusal is re-typed", func(t *testing.T) {
		in := NewErr(ErrBadRequest, "step \"review\" resolves to an interactive-only skill")
		out := composeFailedFromPin(in)
		if out.Code != ErrComposeFailed {
			t.Errorf("code = %s, want COMPOSE_FAILED", out.Code)
		}
		if out.Message != in.Message {
			t.Errorf("the original message must survive verbatim; got %q want %q", out.Message, in.Message)
		}
		d, ok := out.Details.(map[string]any)
		if !ok || d["reason"] != in.Message {
			t.Errorf("details.reason must carry the original message; got %v", out.Details)
		}
	})
	t.Run("the no-oracle not-found refusal is re-typed", func(t *testing.T) {
		in := NewErr(ErrNotFound, "skill version not found")
		out := composeFailedFromPin(in)
		if out.Code != ErrComposeFailed {
			t.Errorf("an inaccessible skill must surface as COMPOSE_FAILED on the create boundary; got %s", out.Code)
		}
	})
	t.Run("an internal error passes through", func(t *testing.T) {
		in := NewErr(ErrInternalError, "insert workflow generation: database is down")
		out := composeFailedFromPin(in)
		if out.Code != ErrInternalError {
			t.Errorf("a server failure must NOT be re-typed as a composition verdict; got %s", out.Code)
		}
	})
	t.Run("nil is nil", func(t *testing.T) {
		if got := composeFailedFromPin(nil); got != nil {
			t.Errorf("composeFailedFromPin(nil) = %v, want nil", got)
		}
	})
}

// TestComposePendingMapsToConflict pins the claim-side code's HTTP mapping:
// COMPOSE_PENDING is a 409 state conflict, distinct from COMPOSE_FAILED's 400,
// because the claim itself was well-formed — the work item is simply not ready.
func TestComposePendingMapsToConflict(t *testing.T) {
	e := NewErr(ErrConflictComposePending, "probe")
	if e.HTTPStatus != 409 {
		t.Errorf("COMPOSE_PENDING maps to %d, want 409", e.HTTPStatus)
	}
	if string(ErrConflictComposePending) != "COMPOSE_PENDING" {
		t.Errorf("wire code = %s, want COMPOSE_PENDING", ErrConflictComposePending)
	}
	if string(ErrComposeFailed) != "COMPOSE_FAILED" {
		t.Errorf("wire code = %s, want COMPOSE_FAILED", ErrComposeFailed)
	}
}
