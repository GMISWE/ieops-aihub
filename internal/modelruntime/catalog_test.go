package modelruntime

import (
	"strings"
	"testing"

	"github.com/GMISWE/ieops-aihub/internal/config"
	"github.com/GMISWE/ieops-aihub/internal/workflow"
)

func testCatalog(t *testing.T) *Catalog {
	t.Helper()
	c, err := NewCatalog([]config.MachineModel{
		{Name: "impl-pi", Harness: "pi", Model: "sub2api-glm/glm-5.3", Effort: "high", Uses: []string{"authoring"}},
		{Name: "review-codex", Harness: "codex", Model: "gpt-6-astra", Effort: "medium", Uses: []string{"review", "verification"}},
		{Name: "impl-cc", Harness: "cc", Model: "sonnet", Effort: "high", Uses: []string{"authoring"}},
	})
	if err != nil {
		t.Fatalf("NewCatalog: %v", err)
	}
	return c
}

func TestCatalogRefusesInvalidEntries(t *testing.T) {
	if _, err := NewCatalog([]config.MachineModel{
		{Name: "x", Harness: "claude", Model: "sonnet", Effort: "low", Uses: []string{"authoring"}},
	}); err == nil {
		t.Fatal("NewCatalog must refuse what config.ValidateModels refuses")
	}
}

// TestCatalogRefusesDuplicateTriples is the Astra-review repair (aihub#708):
// two entries pinning the same harness/model/effort triple used to make the
// by-candidate map silently resolve to the LAST entry — and with it that
// entry's uses policy. A duplicate route is now refused before any map
// write: NewCatalog answers the validator's error naming both entries, and
// Match can never authorize a candidate against a policy the operator
// shadowed.
func TestCatalogRefusesDuplicateTriples(t *testing.T) {
	_, err := NewCatalog([]config.MachineModel{
		{Name: "alpha", Harness: "cc", Model: "sonnet", Effort: "high", Uses: []string{"authoring"}},
		{Name: "beta", Harness: "cc", Model: "sonnet", Effort: "high", Uses: []string{"review"}},
	})
	if err == nil {
		t.Fatal("two entries pinning the same candidate triple must refuse, not last-win the map")
	}
	for _, want := range []string{"alpha", "beta", `harness="cc"`, `model="sonnet"`, `effort="high"`} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error must name %q (both entries and the shared triple), got: %v", want, err)
		}
	}

	// The same model at a DIFFERENT effort is a different route — still
	// valid, still resolvable.
	c, err := NewCatalog([]config.MachineModel{
		{Name: "alpha", Harness: "cc", Model: "sonnet", Effort: "high", Uses: []string{"authoring"}},
		{Name: "alpha-low", Harness: "cc", Model: "sonnet", Effort: "low", Uses: []string{"review"}},
	})
	if err != nil {
		t.Fatalf("same model at a different effort is a different route: %v", err)
	}
	if e, ok := c.Match(workflow.ModelCandidate{Harness: "cc", Model: "sonnet", Effort: "low"}); !ok || e.Name != "alpha-low" {
		t.Errorf("low-effort route must resolve to alpha-low, got %+v ok=%v", e, ok)
	}
	if e, ok := c.Match(workflow.ModelCandidate{Harness: "cc", Model: "sonnet", Effort: "high"}); !ok || e.Name != "alpha" {
		t.Errorf("high-effort route must resolve to alpha, got %+v ok=%v", e, ok)
	}
}

// TestCandidatesDeterministicExactResolution pins the resolution rules the
// spec fixed: order preserved (the order IS the fallback order), unknown
// names refuse naming what IS available, repeats refuse.
func TestCandidatesDeterministicExactResolution(t *testing.T) {
	c := testCatalog(t)
	got, err := c.Candidates([]string{"impl-cc", "review-codex", "impl-pi"})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	want := []workflow.ModelCandidate{
		{Harness: "cc", Model: "sonnet", Effort: "high"},
		{Harness: "codex", Model: "gpt-6-astra", Effort: "medium"},
		{Harness: "pi", Model: "sub2api-glm/glm-5.3", Effort: "high"},
	}
	if len(got) != len(want) {
		t.Fatalf("got %d candidates, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("candidate %d = %+v, want %+v", i, got[i], want[i])
		}
	}

	if _, err := c.Candidates([]string{"impl-pi", "nope"}); err == nil ||
		!strings.Contains(err.Error(), `"nope" is not in the local [[models]] catalog`) ||
		!strings.Contains(err.Error(), "impl-pi") {
		t.Errorf("unknown name must refuse listing available names, got: %v", err)
	}
	if _, err := c.Candidates([]string{"impl-pi", "impl-pi"}); err == nil ||
		!strings.Contains(err.Error(), "appears twice") {
		t.Errorf("repeat name must refuse, got: %v", err)
	}
	if _, err := c.Candidates(nil); err == nil {
		t.Error("empty name list must refuse")
	}
}

// TestMatchExact pins the dispatch-time authorization key: the exact triple.
// Same harness+model at a different effort is a DIFFERENT route and must not
// match — resolving it by alias would be an effort downgrade in disguise.
func TestMatchExact(t *testing.T) {
	c := testCatalog(t)
	if e, ok := c.Match(workflow.ModelCandidate{Harness: "codex", Model: "gpt-6-astra", Effort: "medium"}); !ok || e.Name != "review-codex" {
		t.Errorf("exact triple must match, got %+v ok=%v", e, ok)
	}
	if _, ok := c.Match(workflow.ModelCandidate{Harness: "codex", Model: "gpt-6-astra", Effort: "high"}); ok {
		t.Error("same model at a different effort must NOT match the entry")
	}
	if _, ok := c.Match(workflow.ModelCandidate{Harness: "codex", Model: "gpt-5.6-sol", Effort: "medium"}); ok {
		t.Error("unknown model must not match")
	}
	if _, ok := c.Entry("review-codex"); !ok {
		t.Error("Entry by name must work")
	}
}

// TestCandidatesRoundTripThroughWorkflowStepShape proves the resolved
// candidates satisfy the workflow layer's own step validation shape, so
// nothing resolved here can be rejected there for shape reasons.
func TestCandidatesRoundTripThroughWorkflowStepShape(t *testing.T) {
	c := testCatalog(t)
	got, err := c.Candidates([]string{"impl-pi", "review-codex"})
	if err != nil {
		t.Fatal(err)
	}
	// workflow.Validate's step-shape rules on candidates: token harness,
	// non-empty model, token effort, no duplicates.
	seen := map[string]bool{}
	for _, cand := range got {
		if !tokenShape(cand.Harness) || !tokenShape(cand.Effort) || cand.Model == "" {
			t.Errorf("candidate %+v violates the step token shape", cand)
		}
		key := cand.Harness + "\x00" + cand.Model + "\x00" + cand.Effort
		if seen[key] {
			t.Errorf("duplicate candidate %+v", cand)
		}
		seen[key] = true
	}
}

func tokenShape(s string) bool {
	if s == "" {
		return false
	}
	for i, r := range s {
		switch {
		case r >= 'a' && r <= 'z':
		case i > 0 && ((r >= '0' && r <= '9') || r == '_' || r == '-'):
		default:
			return false
		}
	}
	return true
}
