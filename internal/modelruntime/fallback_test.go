package modelruntime

import (
	"errors"
	"strings"
	"testing"

	"github.com/GMISWE/ieops-aihub/internal/workflow"
)

var identity = AttemptIdentity{
	WorkItemID:    "wi_test",
	FlowVersion:   1,
	StepID:        "impl",
	StepAttemptID: "sa-1",
	Epoch:         3,
}

func chain(t *testing.T, candidates ...workflow.ModelCandidate) *Fallback {
	t.Helper()
	if len(candidates) == 0 {
		candidates = []workflow.ModelCandidate{
			{Harness: "cc", Model: "sonnet", Effort: "high"},
			{Harness: "codex", Model: "gpt-6-astra", Effort: "medium"},
			{Harness: "pi", Model: "sub2api-glm/glm-5.3", Effort: "low"},
		}
	}
	f, err := NewFallback(identity, candidates, workflow.StepGrant{
		Authority:         workflow.AuthorityWrite,
		ProducerIsolation: workflow.IsolationShared,
	})
	if err != nil {
		t.Fatalf("NewFallback: %v", err)
	}
	return f
}

// TestFallbackOrderedCandidatesBeforeStart pins the happy ordering: candidates
// are offered strictly in list order, each at most once, and a PRE-START
// unavailability advances without ceremony because nothing ran.
func TestFallbackOrderedCandidatesBeforeStart(t *testing.T) {
	f := chain(t)
	order := []string{}
	for {
		sel, err := f.Next()
		if errors.Is(err, ErrExhausted) {
			break
		}
		if err != nil {
			t.Fatalf("Next: %v", err)
		}
		order = append(order, sel.Candidate.Harness)
		if err := f.MarkUnavailable(sel, "preflight refused"); err != nil {
			t.Fatalf("MarkUnavailable: %v", err)
		}
	}
	if strings.Join(order, ",") != "cc,codex,pi" {
		t.Errorf("order = %v, want cc,codex,pi", order)
	}
	if _, err := f.Next(); !errors.Is(err, ErrExhausted) {
		t.Errorf("after exhaustion Next must refuse with ErrExhausted, got %v", err)
	}
	recs := f.Records()
	if len(recs) != 3 || recs[0].State != StateUnavailable || recs[2].Candidate.Harness != "pi" {
		t.Errorf("records = %+v", recs)
	}
	for _, r := range recs {
		if r.Identity != identity {
			t.Errorf("records must carry the attempt identity: %+v", r)
		}
	}
}

// TestFallbackStartedInfrastructureFailureRequiresReconcile pins the
// classification rule for a STARTED candidate: an infrastructure failure
// parks the chain behind an explicit stop-and-reconcile, and only recorded
// evidence advances it. The new invocation happens under the same grant.
func TestFallbackStartedInfrastructureFailureRequiresReconcile(t *testing.T) {
	f := chain(t)
	sel, err := f.Next()
	if err != nil {
		t.Fatal(err)
	}
	if err := f.MarkStarted(sel); err != nil {
		t.Fatal(err)
	}
	// While started with no outcome, the chain refuses to offer another.
	if _, err := f.Next(); err == nil || !strings.Contains(err.Error(), "still started") {
		t.Errorf("pending outcome must block, got %v", err)
	}
	if err := f.MarkInfrastructureFailure(sel, "provider 502 after start"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.Next(); !errors.Is(err, ErrReconcileRequired) {
		t.Errorf("must refuse with ErrReconcileRequired before reconcile, got %v", err)
	}
	// Reconcile requires evidence; an empty record is not a reconciliation.
	if err := f.Reconciled(sel, "   "); err == nil {
		t.Error("empty evidence must refuse")
	}
	if err := f.Reconciled(sel, "stopped pgid 4242; worktree inspected: no changes retained"); err != nil {
		t.Fatalf("Reconciled: %v", err)
	}
	next, err := f.Next()
	if err != nil {
		t.Fatalf("Next after reconcile: %v", err)
	}
	if next.Index != 1 || next.Candidate.Harness != "codex" {
		t.Errorf("next = %+v, want candidate 1 (codex)", next)
	}
	// The failed candidate is never offered again.
	if next.Index == 0 {
		t.Error("failed candidate re-offered")
	}
	recs := f.Records()
	if recs[0].State != StateReconciled || !strings.Contains(recs[0].Reason, "reconciled: stopped pgid 4242") {
		t.Errorf("records = %+v", recs)
	}
}

// TestFallbackNoFallbackOnReviewOrTestFAIL pins the no-reroll rule: a review
// FAIL (or test FAIL) closes the chain permanently. Not reconcile, not
// exhaustion, not anything offers another candidate; the only way forward is
// the controller's authorized repair, which opens a NEW chain.
func TestFallbackNoFallbackOnReviewOrTestFAIL(t *testing.T) {
	f := chain(t)
	sel, _ := f.Next()
	if err := f.MarkStarted(sel); err != nil {
		t.Fatal(err)
	}
	if err := f.MarkNoFallback(sel, `review/test FAIL (verdict "fail")`); err != nil {
		t.Fatal(err)
	}
	if _, err := f.Next(); !errors.Is(err, ErrNoFallback) {
		t.Errorf("Next after FAIL must refuse with ErrNoFallback, got %v", err)
	}
	// Reconcile cannot smuggle the chain open again.
	if err := f.Reconciled(sel, "anything"); err == nil {
		t.Error("Reconciled must not apply to a no-fallback outcome")
	}
	// And a second mark of the same selection refuses.
	if err := f.MarkNoFallback(sel, "again"); err == nil {
		t.Error("double-resolving a selection must refuse")
	}
	recs := f.Records()
	if len(recs) != 1 || recs[0].State != StateNoFallback || !strings.Contains(recs[0].Reason, "review/test FAIL") {
		t.Errorf("records = %+v", recs)
	}
}

// TestFallbackSuccessClosesChain pins that a succeeded candidate finishes
// the chain and every later Next refuses.
func TestFallbackSuccessClosesChain(t *testing.T) {
	f := chain(t)
	sel, _ := f.Next()
	if err := f.MarkStarted(sel); err != nil {
		t.Fatal(err)
	}
	if err := f.MarkSucceeded(sel); err != nil {
		t.Fatal(err)
	}
	if _, err := f.Next(); !errors.Is(err, ErrDone) {
		t.Errorf("Next after success must refuse with ErrDone, got %v", err)
	}
}

// TestFallbackNextIsIdempotentBetweenMarks lets a controller that crashed
// mid-decision re-derive the same selection.
func TestFallbackNextIsIdempotentBetweenMarks(t *testing.T) {
	f := chain(t)
	a, _ := f.Next()
	b, _ := f.Next()
	if a != b {
		t.Errorf("Next must be idempotent between marks: %+v != %+v", a, b)
	}
}

// TestFallbackOncePerCandidate pins the bound: each candidate resolves at
// most once per attempt identity, and a resolved candidate is never offered
// again however the caller pokes.
func TestFallbackOncePerCandidate(t *testing.T) {
	f := chain(t)
	sel, _ := f.Next()
	if err := f.MarkUnavailable(sel, "refused"); err != nil {
		t.Fatal(err)
	}
	if err := f.MarkUnavailable(sel, "again"); err == nil {
		t.Error("second resolution of the same candidate must refuse")
	}
	if err := f.MarkStarted(sel); err == nil {
		t.Error("started-after-unavailable must refuse")
	}
	again, err := f.Next()
	if err != nil || again.Index == sel.Index {
		t.Errorf("resolved candidate re-offered: %+v %v", again, err)
	}
}

// TestFallbackConstructionRefusals pins the machine's own shape gates.
func TestFallbackConstructionRefusals(t *testing.T) {
	grant := workflow.StepGrant{Authority: workflow.AuthorityWrite, ProducerIsolation: workflow.IsolationShared}
	if _, err := NewFallback(AttemptIdentity{}, nil, grant); err == nil {
		t.Error("empty identity must refuse")
	}
	if _, err := NewFallback(identity, nil, grant); err == nil {
		t.Error("empty candidate list must refuse")
	}
	dup := []workflow.ModelCandidate{
		{Harness: "cc", Model: "sonnet", Effort: "high"},
		{Harness: "cc", Model: "sonnet", Effort: "high"},
	}
	if _, err := NewFallback(identity, dup, grant); err == nil ||
		!strings.Contains(err.Error(), "duplicates") {
		t.Errorf("duplicate candidates must refuse, got %v", err)
	}
	incomplete := []workflow.ModelCandidate{{Harness: "cc", Model: "", Effort: "high"}}
	if _, err := NewFallback(identity, incomplete, grant); err == nil {
		t.Error("incomplete candidate must refuse")
	}
	if _, err := NewFallback(identity, dup[:1], workflow.StepGrant{}); err == nil {
		t.Error("invalid grant must refuse")
	}
}

// TestClassifyOutcome pins the result→transition policy in one table:
// provider_error is the only reconcile-and-advance failure; a FAIL verdict
// never falls back; non-infrastructure failures close the chain too.
func TestClassifyOutcome(t *testing.T) {
	tests := []struct {
		name   string
		result workflow.StepResult
		want   Outcome
	}{
		{"completed", workflow.StepResult{Status: workflow.StatusCompleted}, OutcomeSucceeded},
		{"completed warn verdict", workflow.StepResult{Status: workflow.StatusCompleted, ReviewVerdict: workflow.ReviewWarn}, OutcomeSucceeded},
		{"provider error", workflow.StepResult{Status: workflow.StatusProviderError}, OutcomeInfrastructure},
		{"review FAIL", workflow.StepResult{Status: workflow.StatusCompleted, ReviewVerdict: workflow.ReviewFail}, OutcomeNoFallback},
		{"provider error with fail verdict", workflow.StepResult{Status: workflow.StatusProviderError, ReviewVerdict: workflow.ReviewFail}, OutcomeNoFallback},
		{"incomplete", workflow.StepResult{Status: workflow.StatusIncomplete}, OutcomeNoFallback},
		{"blocked", workflow.StepResult{Status: workflow.StatusBlocked}, OutcomeNoFallback},
		{"invalid result", workflow.StepResult{Status: workflow.StatusInvalidResult}, OutcomeNoFallback},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := ClassifyOutcome(tc.result); got != tc.want {
				t.Errorf("ClassifyOutcome(%+v) = %d, want %d", tc.result, got, tc.want)
			}
		})
	}
}

// TestApplyIsTheOneCallForm runs Apply through the two decisive transitions.
func TestApplyIsTheOneCallForm(t *testing.T) {
	f := chain(t)
	sel, _ := f.Next()
	if err := f.MarkStarted(sel); err != nil {
		t.Fatal(err)
	}
	if err := f.Apply(sel, workflow.StepResult{Status: workflow.StatusProviderError}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.Next(); !errors.Is(err, ErrReconcileRequired) {
		t.Errorf("apply(provider_error) must park behind reconcile, got %v", err)
	}
	if err := f.Reconciled(sel, "stopped and inspected: clean"); err != nil {
		t.Fatal(err)
	}
	next, _ := f.Next()
	if err := f.MarkStarted(next); err != nil {
		t.Fatal(err)
	}
	if err := f.Apply(next, workflow.StepResult{Status: workflow.StatusCompleted, ReviewVerdict: workflow.ReviewFail}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.Next(); !errors.Is(err, ErrNoFallback) {
		t.Errorf("apply(FAIL) must close the chain, got %v", err)
	}
}
