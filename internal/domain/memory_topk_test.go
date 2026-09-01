package domain

// Page-size resolution for recall (aihub#309). These are pure — no database, no
// network — so they run on ci.yml's "Unit tests" step and cannot fall out of a
// -run filter.
//
//	go test ./internal/domain/ -run TestNormalizeRecallTopK -v -count=1
//
// WHY A RELATIONAL ASSERTION AND NOT A TABLE OF POINTS
// ----------------------------------------------------
// The defect was an inversion: handleRecall capped a caller's top_k at 10 while
// Recall's default page size was 20, so `top_k=30` returned 10 items and `top_k`
// unset returned 20. Every single-point expectation one could write about that is
// satisfiable by moving the cap: pinning "top_k=30 returns 30" goes green the
// moment the cap becomes 1000, while a cap of 1000 with a default of 2000 would
// be the same defect at a different value. The property that is violated exactly
// when a cap sits below the default is the inequality in noInversionViolations,
// swept across the crossover band — so that is what these tests assert.

import (
	"testing"
)

// pageSizeResolver is the shape of a page-size resolution: what the caller
// requested (non-positive meaning "the caller named no page size") mapped to the
// page size actually used.
type pageSizeResolver func(requested int) int

// topKSweep is the requested-page-size domain the invariant is checked over. It
// has to span the crossover band — every value between a candidate cap and the
// default page size — because that band is where an inverted cap is visible and
// nowhere else. 1..260 covers the whole band for any cap up to the 200 ceiling
// and reaches past it.
func topKSweep() []int {
	sweep := make([]int, 0, 260)
	for r := 1; r <= 260; r++ {
		sweep = append(sweep, r)
	}
	return sweep
}

// noInversionViolations returns every requested page size r in the sweep for which
// f breaks the no-inversion invariant
//
//	f(r) >= min(r, f(unset))     for every r > 0
//
// in words: a caller who names a page size never receives fewer items than they
// would have received by naming none, unless they asked for fewer than that.
//
// The min() is load-bearing in both directions. Without it the invariant would
// forbid `top_k=1` returning 1 item, which is correct behaviour — the caller asked
// for it. With it, the only way to violate the invariant is to hand back less than
// BOTH what was asked for and what silence would have produced, which is a
// reduction nobody requested. An upper bound that sits at or above f(unset) can
// never do that, and one that sits below it always does, for every r in
// (bound, f(unset)].
func noInversionViolations(f pageSizeResolver, sweep []int) []int {
	unset := f(0)
	var bad []int
	for _, r := range sweep {
		if f(r) < min(r, unset) {
			bad = append(bad, r)
		}
	}
	return bad
}

// nonMonotonicAt returns every adjacent pair in the sweep where a larger request
// yields a strictly smaller page. Kept next to the invariant above ON PURPOSE, as
// the counter-example to reaching for it: see
// TestNormalizeRecallTopK_MeasurementFailsOnPreChangeBuild, which measures this
// check GREEN on the build that carried the defect.
func nonMonotonicAt(f pageSizeResolver, sweep []int) []int {
	var bad []int
	for i := 1; i < len(sweep); i++ {
		if f(sweep[i]) < f(sweep[i-1]) {
			bad = append(bad, sweep[i])
		}
	}
	return bad
}

// preChangeResolve reconstructs handleRecall composed with Recall exactly as they
// shipped before aihub#309, so the assertions below can be run against the defect
// itself rather than against a description of it.
//
//	internal/server/routes_memory.go (deleted here):
//		if req.TopK > recallTopKMax /* 10 */ { req.TopK = recallTopKMax }
//	internal/domain/memory.go (now normalizeRecallTopK):
//		if req.TopK <= 0  { req.TopK = 20 }
//		if req.TopK > 200 { req.TopK = 200 }
//
// The order is the composition order: the handler capped first, three lines above
// its only call to Recall, and Recall's own bounds ran on the already-capped
// value. That order is the whole defect — it is why the "default" of 20 was
// reachable only by NOT asking.
func preChangeResolve(requested int) int {
	topK := requested
	if topK > 10 {
		topK = 10
	}
	if topK <= 0 {
		topK = 20
	}
	if topK > 200 {
		topK = 200
	}
	return topK
}

// TestNormalizeRecallTopK_LargerRequestIsNeverASmallerPage is the decisive
// assertion of aihub#309.
func TestNormalizeRecallTopK_LargerRequestIsNeverASmallerPage(t *testing.T) {
	// The instance named in the report, spelled out on its own so the mapping from
	// the bug to the invariant is legible: f(30) >= f(unset). Production measured
	// 10 >= 20, which is false.
	if got, unset := normalizeRecallTopK(30), normalizeRecallTopK(0); got < unset {
		t.Errorf("f(30) = %d but f(unset) = %d: asking for a bigger page returns a "+
			"smaller one, which is the aihub#309 inversion", got, unset)
	}

	// ...and the general form, which is what actually holds this closed: the
	// assertion above alone would go green again under any cap >= 30.
	if bad := noInversionViolations(normalizeRecallTopK, topKSweep()); len(bad) > 0 {
		t.Errorf("requested page sizes %v return fewer items than requesting nothing "+
			"(f(unset) = %d): a page-size bound below the default inverts the endpoint",
			bad, normalizeRecallTopK(0))
	}
}

// The reverse direction of aihub#309's criteria, and the aihub#249 contract it
// must not break: bad input falls back to the DEFAULT, never to a smaller page.
//
// Expectations are literals on purpose. Writing recallTopKDefault here would make
// the fixture derive from the constant under test, and the test would then follow
// that constant wherever it went instead of pinning it.
func TestNormalizeRecallTopK_BadInputFallsBackToTheDefault(t *testing.T) {
	for _, requested := range []int{0, -1, -5, -1000} {
		if got := normalizeRecallTopK(requested); got != 20 {
			t.Errorf("normalizeRecallTopK(%d) = %d, want the default 20 "+
				"(aihub#249: bad input falls back to the default, not to a smaller page)",
				requested, got)
		}
	}
	// A positive request below the default is honoured, not raised to it: that is
	// the caller's own choice and the reason the invariant above is stated with a
	// min() rather than as "never less than the default".
	for requested, want := range map[int]int{1: 1, 5: 5, 10: 10, 19: 19, 20: 20} {
		if got := normalizeRecallTopK(requested); got != want {
			t.Errorf("normalizeRecallTopK(%d) = %d, want %d honoured verbatim", requested, got, want)
		}
	}
}

// The ceiling must be REACHABLE. aihub#309 criterion 4: an unreachable ceiling is
// a false one — before this change no input at all produced 200 (top_k=300
// measured 10 items against production), while a comment in routes_memory.go
// asserted the endpoint "clamps to 200".
func TestNormalizeRecallTopK_CeilingIsReachable(t *testing.T) {
	for _, requested := range []int{200, 201, 300, 100000} {
		if got := normalizeRecallTopK(requested); got != 200 {
			t.Errorf("normalizeRecallTopK(%d) = %d, want the ceiling 200", requested, got)
		}
	}

	// Reachability as a measurement over the sweep, not as a claim about one
	// value — this is the half that goes red if a new cap is ever reintroduced
	// upstream of the ceiling, whatever value it takes.
	reached := false
	for _, r := range topKSweep() {
		if normalizeRecallTopK(r) == 200 {
			reached = true
			break
		}
	}
	if !reached {
		t.Error("no requested page size in 1..260 resolves to the 200 ceiling: the ceiling " +
			"is unreachable, so it is not the endpoint's real upper bound and the next " +
			"reader will believe a number that is not true")
	}
}

// ─── NEGATIVE CONTROL: the measurement must fail on the pre-change build ──────

// TestNormalizeRecallTopK_MeasurementFailsOnPreChangeBuild runs the SAME checks
// used above against preChangeResolve — handleRecall's deleted cap composed with
// Recall's bounds, as shipped — and requires the invariant to come back RED.
//
// Without this, the tests above could be passing because the sweep is empty, the
// inequality is the wrong way round, or the resolver they call is not the one in
// the request path. A probe that has never been shown to go red is not evidence.
// Shape copied from TestResumePath_MeasurementFailsOnPreChangeBuild (aihub#287).
func TestNormalizeRecallTopK_MeasurementFailsOnPreChangeBuild(t *testing.T) {
	// 1. The invariant must fire, and it must fire ON THE REPORTED VALUE — "some
	//    violation somewhere" would also be satisfied by a broken sweep.
	bad := noInversionViolations(preChangeResolve, topKSweep())
	if len(bad) == 0 {
		t.Fatal("the pre-change build passes the no-inversion invariant, so the invariant " +
			"cannot distinguish it from the fix and TestNormalizeRecallTopK_" +
			"LargerRequestIsNeverASmallerPage proves nothing")
	}
	foundReported := false
	for _, r := range bad {
		if r == 30 {
			foundReported = true
		}
	}
	if !foundReported {
		t.Errorf("the pre-change build violates the invariant at %v, but NOT at the "+
			"requested page size the report measured (30). The check is firing on "+
			"something other than the defect", bad)
	}

	// 2. Every value in the crossover band (cap 10 < r <= default 20) must be a
	//    violation. This is the band the fix widens, and naming it here is what
	//    makes the probe insensitive to which single value the report happened to
	//    use: if a future cap of 1000 were introduced against a default of 2000,
	//    the same band assertion would find it.
	inBand := map[int]bool{}
	for _, r := range bad {
		inBand[r] = true
	}
	for r := 11; r <= 20; r++ {
		if !inBand[r] {
			t.Errorf("pre-change build: requested %d is not reported as an inversion, "+
				"but the deleted cap (10) is below the default (20), so every value in "+
				"11..20 is one", r)
		}
	}

	// 3. The pre-change build must still PASS the reverse-direction half, so "the
	//    old build is red" is red for the right reason: aihub#249's contract was
	//    intact and this change must not be credited with fixing it.
	for _, requested := range []int{0, -1, -5} {
		if got := preChangeResolve(requested); got != 20 {
			t.Errorf("pre-change build: resolve(%d) = %d, want 20. The fixture is not the "+
				"pre-change build — aihub#249's bad-input fallback was working before "+
				"this change and the reconstruction has to reproduce that too",
				requested, got)
		}
	}

	// 4. The ceiling was unreachable on the pre-change build — criterion 4's premise
	//    measured rather than asserted.
	for _, r := range topKSweep() {
		if preChangeResolve(r) == 200 {
			t.Errorf("pre-change build: requested %d resolves to 200, so the ceiling was "+
				"reachable after all and the dead-code finding in the report is wrong", r)
		}
	}

	// 5. Monotonicity is GREEN on the defect. This is why the invariant above is
	//    stated the way it is: "a bigger request never yields a smaller page than
	//    the request before it" is a property the inverted cap satisfies (1..10
	//    rise, then everything above 10 is flat at 10), because f(unset) sits
	//    outside the positive domain it compares over. Reaching for monotonicity
	//    here would have produced a test that passes on the bug.
	if bad := nonMonotonicAt(preChangeResolve, topKSweep()); len(bad) > 0 {
		t.Errorf("monotonicity was expected to hold on the pre-change build (it is the "+
			"weaker check that could not see the defect) but it fails at %v — the "+
			"reconstruction does not match the code that shipped", bad)
	}
	if bad := nonMonotonicAt(normalizeRecallTopK, topKSweep()); len(bad) > 0 {
		t.Errorf("the fix must satisfy monotonicity too, it fails at %v", bad)
	}
}
