package domain

import (
	"encoding/json"
	"math"
	"testing"
	"time"
)

const baseClassificationConfig = `{
	"wi_types": {
		"fix_bug":       {"requires_human_session": false},
		"refactor":      {"requires_human_session": false},
		"design_review": {"requires_human_session": true}
	},
	"classification_rules": [
		{"name": "urgent-needs-human", "priority": "urgent", "set": {"wi_type": "design_review", "requires_human_session": true}},
		{"name": "starts-with-fix",    "wi_type_prefix": "fix", "set": {"wi_type": "fix_bug"}}
	]
}`

func TestApplyClassificationRules_MatchByWITypePrefix(t *testing.T) {
	req := &CreateWorkItemRequest{
		Goal:     "fix the login crash",
		Priority: "normal",
	}
	wiType, _, err := applyClassificationRules([]byte(baseClassificationConfig), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if wiType == nil || *wiType != "fix_bug" {
		t.Errorf("wiType = %v, want fix_bug", wiType)
	}
}

func TestApplyClassificationRules_MatchByPriority(t *testing.T) {
	req := &CreateWorkItemRequest{
		Goal:     "redesign onboarding",
		Priority: "urgent",
	}
	wiType, rhs, err := applyClassificationRules([]byte(baseClassificationConfig), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if wiType == nil || *wiType != "design_review" {
		t.Errorf("wiType = %v, want design_review", wiType)
	}
	if rhs == nil || !*rhs {
		t.Errorf("requires_human_session = %v, want true", rhs)
	}
}

func TestApplyClassificationRules_PresetWIType(t *testing.T) {
	// When wi_type is already set, requires_human_session is derived from wi_types config.
	preset := "refactor"
	req := &CreateWorkItemRequest{
		Goal:     "some goal",
		Priority: "normal",
		WIType:   &preset,
	}
	wiType, rhs, err := applyClassificationRules([]byte(baseClassificationConfig), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if wiType == nil || *wiType != "refactor" {
		t.Errorf("wiType = %v, want refactor", wiType)
	}
	if rhs == nil || *rhs != false {
		t.Errorf("requires_human_session = %v, want false (from wi_types)", rhs)
	}
}

func TestApplyClassificationRules_UnknownType(t *testing.T) {
	unknown := "this_type_does_not_exist"
	req := &CreateWorkItemRequest{
		Goal:     "some goal",
		Priority: "normal",
		WIType:   &unknown,
	}
	_, _, err := applyClassificationRules([]byte(baseClassificationConfig), req)
	if err == nil {
		t.Fatal("expected error for unknown wi_type")
	}
	if err.Code != ErrWITypeMismatch {
		t.Errorf("Code = %q, want WI_TYPE_MISMATCH", err.Code)
	}
}

func TestApplyClassificationRules_NoRulesMatch(t *testing.T) {
	// No rule matches: result should be unset wi_type + unset requires_human_session.
	cfg := `{"wi_types": {"a":{"requires_human_session": false}}, "classification_rules": []}`
	req := &CreateWorkItemRequest{Goal: "anything", Priority: "low"}
	wiType, rhs, err := applyClassificationRules([]byte(cfg), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if wiType != nil {
		t.Errorf("wiType = %v, want nil", wiType)
	}
	if rhs != nil {
		t.Errorf("requires_human_session = %v, want nil", rhs)
	}
}

func TestApplyClassificationRules_InvalidJSON(t *testing.T) {
	req := &CreateWorkItemRequest{Goal: "x", Priority: "normal"}
	_, _, err := applyClassificationRules([]byte("not-json{"), req)
	if err == nil || err.Code != ErrInternalError {
		t.Errorf("expected INTERNAL_ERROR for bad JSON, got %v", err)
	}
}

// ─── Jaccard / NGram ─────────────────────────────────────────────────────────

func TestJaccardNGram_SameString(t *testing.T) {
	if got := jaccardNGram("hello world", "hello world", 3); got != 1.0 {
		t.Errorf("identical strings: got %v, want 1.0", got)
	}
}

func TestJaccardNGram_EmptyStrings(t *testing.T) {
	// Both empty -> 1.0 (defined as equal sets).
	if got := jaccardNGram("", "", 3); got != 1.0 {
		t.Errorf("both empty: got %v, want 1.0", got)
	}
	// One empty -> 0 (no overlap).
	if got := jaccardNGram("abc", "", 3); got != 0 {
		t.Errorf("one empty: got %v, want 0", got)
	}
}

func TestJaccardNGram_NoOverlap(t *testing.T) {
	if got := jaccardNGram("abc", "xyz", 2); got != 0 {
		t.Errorf("disjoint: got %v, want 0", got)
	}
}

func TestJaccardNGram_PartialOverlap(t *testing.T) {
	// Overlap should be a real fraction in (0,1).
	got := jaccardNGram("apple pie", "apple cake", 3)
	if got <= 0 || got >= 1 {
		t.Errorf("partial overlap: got %v, want strictly in (0,1)", got)
	}
}

func TestJaccardNGram_CaseInsensitive(t *testing.T) {
	a := jaccardNGram("HELLO", "hello", 3)
	if a != 1.0 {
		t.Errorf("case-insensitive: got %v, want 1.0", a)
	}
}

func TestNgrams_BasicShape(t *testing.T) {
	got := ngrams("abcde", 2)
	want := []string{"ab", "bc", "cd", "de"}
	if len(got) != len(want) {
		t.Fatalf("ngrams len = %d, want %d", len(got), len(want))
	}
	for _, w := range want {
		if !got[w] {
			t.Errorf("missing ngram %q", w)
		}
	}
}

func TestNgrams_ShortString(t *testing.T) {
	// String shorter than n returns no ngrams.
	got := ngrams("ab", 5)
	if len(got) != 0 {
		t.Errorf("short string: got %d ngrams, want 0", len(got))
	}
}

// ─── setOverlap ────────────────────────────────────────────────────────────

func TestSetOverlap_BothEmpty(t *testing.T) {
	if got := setOverlap(nil, nil); got != 1.0 {
		t.Errorf("got %v, want 1.0", got)
	}
}

func TestSetOverlap_OneEmpty(t *testing.T) {
	if got := setOverlap([]string{"a"}, nil); got != 0 {
		t.Errorf("got %v, want 0", got)
	}
	if got := setOverlap(nil, []string{"a"}); got != 0 {
		t.Errorf("got %v, want 0", got)
	}
}

func TestSetOverlap_FullMatch(t *testing.T) {
	if got := setOverlap([]string{"a", "b"}, []string{"b", "a"}); got != 1.0 {
		t.Errorf("got %v, want 1.0", got)
	}
}

func TestSetOverlap_HalfMatch(t *testing.T) {
	// {a,b} ∩ {b,c} = {b}, union {a,b,c} -> 1/3
	got := setOverlap([]string{"a", "b"}, []string{"b", "c"})
	want := 1.0 / 3.0
	if math.Abs(got-want) > 1e-9 {
		t.Errorf("got %v, want %v", got, want)
	}
}

// ─── jaccardSimilarity / tokenSet ──────────────────────────────────────────

func TestJaccardSimilarity_BasicWordOverlap(t *testing.T) {
	if got := jaccardSimilarity("hello world", "hello world"); got != 1.0 {
		t.Errorf("identical: got %v, want 1.0", got)
	}
	if got := jaccardSimilarity("a b c", "d e f"); got != 0 {
		t.Errorf("disjoint: got %v, want 0", got)
	}
	// {a,b,c} vs {b,c,d}: intersection 2, union 4 -> 0.5
	got := jaccardSimilarity("a b c", "b c d")
	if math.Abs(got-0.5) > 1e-9 {
		t.Errorf("got %v, want 0.5", got)
	}
}

func TestJaccardSimilarity_BothEmpty(t *testing.T) {
	if got := jaccardSimilarity("", ""); got != 1.0 {
		t.Errorf("both empty: got %v, want 1.0", got)
	}
}

func TestTokenSet_LowercaseSplit(t *testing.T) {
	got := tokenSet("Hello WORLD hello")
	if len(got) != 2 || !got["hello"] || !got["world"] {
		t.Errorf("tokenSet = %v", got)
	}
}

// ─── MemoryStrength ────────────────────────────────────────────────────────

func TestMemoryStrength_Fresh(t *testing.T) {
	now := time.Now()
	got := MemoryStrength(3.0, 7.0, &now, now)
	// Fresh (days_since = 0) -> exp(0) = 1 -> strength ≈ base
	if math.Abs(got-3.0) > 0.01 {
		t.Errorf("fresh strength = %v, want ~3.0", got)
	}
}

func TestMemoryStrength_OldMemory(t *testing.T) {
	// 1 year old with 7-day stability -> exp(-365/7) ≈ 0 (very small).
	old := time.Now().AddDate(-1, 0, 0)
	got := MemoryStrength(3.0, 7.0, &old, old)
	if got > 0.001 {
		t.Errorf("year-old memory strength = %v, want ~0", got)
	}
}

func TestMemoryStrength_UsesLastActivated(t *testing.T) {
	// If last_activated_at is recent, strength should be high even if created_at is old.
	created := time.Now().AddDate(-1, 0, 0)
	activated := time.Now()
	got := MemoryStrength(3.0, 7.0, &activated, created)
	if math.Abs(got-3.0) > 0.01 {
		t.Errorf("activated-recent strength = %v, want ~3.0", got)
	}
}

func TestMemoryStrength_FallsBackToCreatedAt(t *testing.T) {
	// last_activated_at = nil -> uses created_at.
	created := time.Now()
	got := MemoryStrength(3.0, 7.0, nil, created)
	if math.Abs(got-3.0) > 0.01 {
		t.Errorf("nil last_activated_at: got %v, want ~3.0", got)
	}
}

func TestMemoryStrength_ZeroStability(t *testing.T) {
	now := time.Now()
	if got := MemoryStrength(3.0, 0, &now, now); got != 0 {
		t.Errorf("zero stability: got %v, want 0", got)
	}
	if got := MemoryStrength(3.0, -1, &now, now); got != 0 {
		t.Errorf("negative stability: got %v, want 0", got)
	}
}

func TestComputeStabilityDays(t *testing.T) {
	// experience.* base = 7
	// 0 activations -> 7 * 1   = 7
	// 1 activation  -> 7 * 1.5 = 10.5
	// 4 activations -> 7 * 3   = 21
	tests := []struct {
		memType string
		acts    int
		want    float64
	}{
		{"experience.success", 0, 7},
		{"experience.success", 1, 10.5},
		{"experience.success", 4, 21},
		{"fact.basic", 0, 180},
		{"rule.policy", 0, 36500},
		{"methodology.spec", 0, 36500},
		{"unknown.type", 0, 7},
	}
	for _, tt := range tests {
		t.Run(tt.memType, func(t *testing.T) {
			got := computeStabilityDays(tt.memType, tt.acts)
			if math.Abs(got-tt.want) > 0.01 {
				t.Errorf("computeStabilityDays(%q, %d) = %v, want %v", tt.memType, tt.acts, got, tt.want)
			}
		})
	}
}

func TestBaseStabilityForType(t *testing.T) {
	tests := []struct {
		t    string
		want float64
	}{
		{"experience.anything", 7},
		{"fact.anything", 180},
		{"rule.anything", 36500},
		{"methodology.anything", 36500},
		{"", 7},
		{"weird", 7},
	}
	for _, tc := range tests {
		if got := baseStabilityForType(tc.t); got != tc.want {
			t.Errorf("baseStabilityForType(%q) = %v, want %v", tc.t, got, tc.want)
		}
	}
}

func TestIsImmortalType(t *testing.T) {
	if !isImmortalType("rule.x") {
		t.Error("rule.x should be immortal")
	}
	if isImmortalType("experience.x") {
		t.Error("experience.x should not be immortal")
	}
	if isImmortalType("fact.x") {
		t.Error("fact.x should not be immortal")
	}
}

// ─── Misc smoke tests on request structs ────────────────────────────────────

func TestCreateWorkItemRequest_JSONRoundtrip(t *testing.T) {
	src := CreateWorkItemRequest{
		Project:  "myproj",
		Goal:     "do the thing",
		Priority: "high",
		Labels:   []string{"a", "b"},
	}
	b, err := json.Marshal(&src)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got CreateWorkItemRequest
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.Project != src.Project || got.Goal != src.Goal {
		t.Errorf("roundtrip mismatch: %+v vs %+v", got, src)
	}
}
