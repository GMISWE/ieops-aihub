package domain

import (
	"encoding/json"
	"math"
	"strings"
	"testing"
	"time"
)

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

// aihub#251: both sides declaring nothing is an ABSENCE of evidence, not
// evidence of similarity. The pre-fix code returned 1.0 here, which meant
// "neither wi has labels" (or "neither has declared_resources") silently
// contributed a full +0.2 (or +0.2) to the composite dedup score for every
// single candidate that also had no labels/resources -- a major driver of
// the reported false-positive collisions. Deliberately changed from the
// previous expectation of 1.0 to 0: no overlap can be demonstrated between
// two empty sets, so none should be credited.
func TestSetOverlap_BothEmpty(t *testing.T) {
	if got := setOverlap(nil, nil); got != 0 {
		t.Errorf("got %v, want 0 (absence of evidence is not evidence of similarity)", got)
	}
}

// aihub#251 defect 1 regression: setOverlap must de-duplicate BOTH sides
// before computing intersection/union, so duplicate entries in either input
// can never push the ratio above 1.0. Pre-fix, this exact shape
// (setOverlap(["repo:ieops"], five copies of "repo:ieops")) returned 5.0.
func TestSetOverlap_DuplicateLadenInputsStayBounded(t *testing.T) {
	got := setOverlap([]string{"repo:ieops"}, []string{"repo:ieops", "repo:ieops", "repo:ieops", "repo:ieops", "repo:ieops"})
	if got < 0 || got > 1 {
		t.Fatalf("setOverlap must stay within [0,1], got %v", got)
	}
	// Both sides dedup to the single-element set {"repo:ieops"} -> exact match.
	if got != 1.0 {
		t.Errorf("got %v, want 1.0 (both sides are the same set once deduplicated)", got)
	}
}

// A duplicate-laden input that is only a PARTIAL match must still land on the
// correct deduplicated Jaccard value, not merely "somewhere in [0,1]" -- a
// clamp that discarded the real ratio would pass a bounds-only check while
// still being wrong.
func TestSetOverlap_DuplicateLadenPartialMatch(t *testing.T) {
	// setA dedups to {a,b}; setB dedups to {b,c} regardless of how many times
	// "b" is repeated. intersection={b} (1), union={a,b,c} (3) -> 1/3.
	got := setOverlap([]string{"a", "b"}, []string{"b", "b", "b", "c"})
	want := 1.0 / 3.0
	if math.Abs(got-want) > 1e-9 {
		t.Errorf("got %v, want %v", got, want)
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

// ─── declaredResourceKeys / candidateScore (aihub#251 defect 2 & 3) ────────
//
// req.DeclaredResources / c.Resources hold an array of OBJECTS (the current
// {"type","uri","intent"} shape, and the legacy pre-aihub#238
// {"type":"file_scope","value":...} shape that may still live on old rows --
// see TestCreateWorkItem_RejectsUnknownTypeBeforeTouchingDB in
// declared_resources_wiring_test.go). The pre-fix code unmarshalled these
// straight into []string, which always failed silently (`_ = json.Unmarshal`)
// and left both sides nil -- and setOverlap(nil, nil) then returned 1.0,
// adding a constant +0.2 to every candidate's score regardless of whether the
// resources actually matched.

func TestDeclaredResourceKeys_ObjectShapeParsesAndCompares(t *testing.T) {
	a, aOK := declaredResourceKeys(json.RawMessage(`[{"type":"path","uri":"file:internal/a.go","intent":"write"}]`))
	if !aOK {
		t.Fatalf("expected ok=true for well-formed object-shaped resources")
	}
	b, bOK := declaredResourceKeys(json.RawMessage(`[{"type":"path","uri":"file:internal/b.go","intent":"write"}]`))
	if !bOK {
		t.Fatalf("expected ok=true for well-formed object-shaped resources")
	}
	// Two DIFFERENT files must not be treated as a match.
	if got := setOverlap(a, b); got == 1.0 {
		t.Errorf("unrelated resources scored as a perfect match: %v", got)
	}

	same, sameOK := declaredResourceKeys(json.RawMessage(`[{"type":"path","uri":"file:internal/a.go","intent":"write"}]`))
	if !sameOK {
		t.Fatalf("expected ok=true")
	}
	if got := setOverlap(a, same); got != 1.0 {
		t.Errorf("identical resource entries should score 1.0, got %v", got)
	}
}

func TestDeclaredResourceKeys_LegacyFileScopeShapeParses(t *testing.T) {
	keys, ok := declaredResourceKeys(json.RawMessage(`[{"type":"file_scope","value":"aihub:internal/a.go"}]`))
	if !ok {
		t.Fatalf("expected ok=true for legacy file_scope/value shape")
	}
	if len(keys) != 1 {
		t.Fatalf("expected 1 key, got %#v", keys)
	}
}

func TestDeclaredResourceKeys_EmptyIsOKNotAFailure(t *testing.T) {
	for _, raw := range []json.RawMessage{nil, json.RawMessage(""), json.RawMessage("[]"), json.RawMessage("null")} {
		keys, ok := declaredResourceKeys(raw)
		if !ok {
			t.Errorf("raw=%q: expected ok=true for empty/absent resources", string(raw))
		}
		if len(keys) != 0 {
			t.Errorf("raw=%q: expected no keys, got %#v", string(raw), keys)
		}
	}
}

func TestDeclaredResourceKeys_UnparseableReturnsNotOK(t *testing.T) {
	_, ok := declaredResourceKeys(json.RawMessage(`not json`))
	if ok {
		t.Fatalf("expected ok=false for unparseable resource JSON")
	}
}

func TestDeclaredResourceKeys_EntryMatchingNeitherShapeIsSkippedNotFatal(t *testing.T) {
	// An entry with neither `uri` nor `value` matches neither known shape --
	// it must be skipped, not crash and not silently count as a match.
	keys, ok := declaredResourceKeys(json.RawMessage(`[{"type":"path"}]`))
	if !ok {
		t.Fatalf("expected ok=true (the payload itself is valid JSON)")
	}
	if len(keys) != 0 {
		t.Errorf("expected the unmatched entry to be skipped, got %#v", keys)
	}
}

// TestCandidateScore_UnrelatedItemsSharingLabelAndResourceStayBelowDuplicateThreshold
// is the composite-score-level regression: two topically UNRELATED work items
// that happen to share one label and one declared resource -- where the
// shared label and resource are each represented with heavy duplicate entries
// on the candidate side, exactly the shape that produced 122-125% scores in
// production -- must not reach the 0.90 CONFLICT_DUPLICATE threshold, and the
// score must never exceed 1.0.
//
// checkDedup itself needs a live work_items table (AIHUB_TEST_DB), so this
// exercises the composite scoring logic through candidateScore, the pure
// helper the fix factors it into, instead.
func TestCandidateScore_UnrelatedItemsSharingLabelAndResourceStayBelowDuplicateThreshold(t *testing.T) {
	req := &CreateWorkItemRequest{
		Goal:              "Fix the login page CSS spacing bug",
		Labels:            []string{"bug"},
		DeclaredResources: json.RawMessage(`[{"type":"path","uri":"file:internal/a.go","intent":"write"}]`),
	}
	// Topically unrelated goal text, but the SAME label repeated 8x and the
	// SAME resource repeated 5x -- the duplicate-laden shape that inflated
	// setOverlap far past 1.0 pre-fix (labelSim alone would have been 8.0).
	candidateGoal := "Add GPU billing CSV export format"
	candidateLabels := []string{"bug", "bug", "bug", "bug", "bug", "bug", "bug", "bug"}
	candidateResources := json.RawMessage(`[
		{"type":"path","uri":"file:internal/a.go","intent":"write"},
		{"type":"path","uri":"file:internal/a.go","intent":"write"},
		{"type":"path","uri":"file:internal/a.go","intent":"write"},
		{"type":"path","uri":"file:internal/a.go","intent":"write"},
		{"type":"path","uri":"file:internal/a.go","intent":"write"}
	]`)

	score, valid := candidateScore(req, candidateGoal, candidateLabels, candidateResources)
	if !valid {
		t.Fatalf("expected a valid in-range score, candidateScore reported invalid")
	}
	if score > 1.0 {
		t.Fatalf("score exceeds 100%%: %v", score)
	}
	if score >= 0.90 {
		t.Errorf("unrelated work items falsely flagged as CONFLICT_DUPLICATE: score=%v", score)
	}
}

// TestCandidateScore_IdenticalGoalNoLabelsNoResourcesReachesDuplicateThreshold
// is the review WARN-finding regression (mem_veTEPhFm, aihub#251 follow-up):
// when NEITHER side declares labels NOR declared_resources, that dimension is
// genuinely inapplicable (no evidence either way), so its 0.2+0.2 weight must
// be dropped and renormalized onto the 0.6 goal-similarity weight rather than
// scored as a hard 0. Byte-identical goal text with nothing else declared is
// about as strong a duplicate signal as this system can ever see; pre-fix it
// scored a flat 0.6 (below even the 0.65 candidates gate) because the
// composite was structurally capped by two zeroed-out, still-included terms.
func TestCandidateScore_IdenticalGoalNoLabelsNoResourcesReachesDuplicateThreshold(t *testing.T) {
	req := &CreateWorkItemRequest{
		Goal:              "Fix the login page CSS spacing bug",
		Labels:            []string{},
		DeclaredResources: json.RawMessage(`[]`),
	}
	score, valid := candidateScore(req, req.Goal, []string{}, json.RawMessage(`[]`))
	if !valid {
		t.Fatalf("expected a valid in-range score, candidateScore reported invalid")
	}
	if score < 0.90 {
		t.Errorf("byte-identical goal with no labels/resources on either side should reach the duplicate threshold; got %v (want >= 0.90, ideally 1.0)", score)
	}
}

// TestCandidateScore_UnrelatedGoalsSharingOnlyALabelStayBelowCandidateThreshold
// guards the renormalization tradeoff the review explicitly called out:
// renormalizing weights onto applicable components increases the influence
// of whichever component IS present, so this checks that two topically
// UNRELATED work items that merely share one label -- with neither side
// declaring any resources, so the resource term drops out and its weight
// moves onto goal+label -- still stay well below the 0.65 "candidates" gate.
// This is the aihub#251 false-positive regression that started this whole
// work item; renormalization must not resurrect it.
func TestCandidateScore_UnrelatedGoalsSharingOnlyALabelStayBelowCandidateThreshold(t *testing.T) {
	req := &CreateWorkItemRequest{
		Goal:              "Fix the login page CSS spacing bug",
		Labels:            []string{"bug"},
		DeclaredResources: json.RawMessage(`[]`),
	}
	score, valid := candidateScore(req, "Add GPU billing CSV export format", []string{"bug"}, json.RawMessage(`[]`))
	if !valid {
		t.Fatalf("expected a valid in-range score, candidateScore reported invalid")
	}
	if score >= 0.65 {
		t.Errorf("unrelated work items sharing only a label falsely reached the candidates gate: score=%v (want < 0.65)", score)
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

// ─── ListWorkItems cursor pagination (aihub#147) ─────────────────────────────
//
// The domain test suite is pure-unit (no live DB / testcontainers wired into
// the worktree), so ListWorkItems cannot be exercised against a real pool here.
// Instead we assert on the WHERE clause + bound args that buildListWorkItemsWhere
// produces (cf. aihub#145's gc_test, which pins the sweep SQL). The contract is
// that when a cursor is supplied, the query gains a `wi.created_at < $n` predicate
// (matching ORDER BY wi.created_at DESC) bound to the cursor value — so passing
// back next_cursor advances to the NEXT page instead of re-returning page 1.

func ptrStr(s string) *string { return &s }

// Page 1: no cursor → the query MUST NOT contain a created_at upper-bound
// predicate, so the first page starts at the newest rows.
func TestBuildListWorkItemsWhere_NoCursor(t *testing.T) {
	_, where, args := buildListWorkItemsWhere("proj", ListWorkItemsFilter{})
	if strings.Contains(where, "wi.created_at <") {
		t.Errorf("page 1 (no cursor) must not add a created_at upper bound; got WHERE: %q", where)
	}
	// project=proj is the only bound arg.
	if len(args) != 1 || args[0] != "proj" {
		t.Errorf("expected exactly [proj] bound args, got %#v", args)
	}
}

// Page 2: cursor supplied → the query MUST add `wi.created_at < $N::timestamptz`
// bound to the cursor value, so the next page is the rows strictly older than the
// last item of page 1 (not page 1 again). This is the core regression guard.
func TestBuildListWorkItemsWhere_CursorAppliesPredicate(t *testing.T) {
	cursor := time.Date(2026, 6, 5, 8, 0, 0, 0, time.UTC).Format(time.RFC3339Nano)
	_, where, args := buildListWorkItemsWhere("proj", ListWorkItemsFilter{Cursor: ptrStr(cursor)})

	// project is $1, cursor predicate must use $2 (correct placeholder numbering).
	want := "wi.created_at < $2::timestamptz"
	if !strings.Contains(where, want) {
		t.Errorf("cursor must add %q to WHERE; got: %q", want, where)
	}
	// Strict `<` (DESC order), not `<=` or `>`, and no secondary tie-breaker —
	// matches ListEvents in memory.go.
	if strings.Contains(where, "wi.created_at <=") || strings.Contains(where, "wi.created_at >") {
		t.Errorf("cursor predicate must be a strict `<` on created_at; got: %q", where)
	}
	// Cursor value must be bound as arg $2 (index 1), unchanged.
	if len(args) != 2 || args[1] != cursor {
		t.Errorf("expected cursor %q bound as 2nd arg; got args %#v", cursor, args)
	}
}

// Placeholder numbering must stay correct when other filters precede the cursor.
// status ($2 via ANY) + cursor: cursor must land on $3, not collide with status.
func TestBuildListWorkItemsWhere_CursorPlaceholderAfterOtherFilters(t *testing.T) {
	cursor := time.Now().UTC().Format(time.RFC3339Nano)
	_, where, args := buildListWorkItemsWhere("proj", ListWorkItemsFilter{
		Status: []string{"queued"},
		Cursor: ptrStr(cursor),
	})
	// $1=project, $2=status ANY, $3=cursor.
	if !strings.Contains(where, "wi.created_at < $3::timestamptz") {
		t.Errorf("cursor placeholder numbering broken with preceding filters; got WHERE: %q", where)
	}
	if len(args) != 3 || args[2] != cursor {
		t.Errorf("expected cursor as $3 (3rd arg); got args %#v", args)
	}
}

// Empty cursor string is treated as "no cursor" (first page), so a caller that
// passes back an empty/nil next_cursor (the last page) does not wedge the query.
func TestBuildListWorkItemsWhere_EmptyCursorIgnored(t *testing.T) {
	_, where, _ := buildListWorkItemsWhere("proj", ListWorkItemsFilter{Cursor: ptrStr("")})
	if strings.Contains(where, "wi.created_at <") {
		t.Errorf("empty cursor must be ignored (no created_at upper bound); got WHERE: %q", where)
	}
}
