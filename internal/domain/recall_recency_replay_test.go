package domain

// aihub#469 — the replay that decided NOT to implement `recency_weight`.
//
// docs/design/polyforge-v1-design.md §7.5 argues that the blend it used to
// specify — `sim*(1-w) + normalized_recency*w + normalized_strength*0.1`, w=0.3 —
// must not be implemented, and rests that argument on a table of measured
// numbers. This file is that table, executable.
//
// 🔴 Why it exists at all. The defect aihub#469 fixed was a design document
// asserting something about the implementation that nobody could check: its
// changelog line L7 read "recency_weight default = 0.3（API 文档和实现对齐）"
// while no hop applied any such default. Replacing that with a NEW set of
// unverifiable numbers in the same section would repeat the defect in the act of
// documenting it. So the fixture below is the real result set the argument was
// derived from, and the assertions are the doc's own numbers.
//
// If someone later proposes implementing the blend, this test is the artifact to
// argue with — not the prose.
//
// ─── Provenance of the fixture ──────────────────────────────────────────────
//
// One live `pf_recall(project=aihub, query="recall ranking cosine similarity
// ordering vector path", top_k=20, min_strength=0.001)` against the production
// server, 2026-09-08. Rows are recorded IN THE ORDER THE SERVER RETURNED THEM,
// which is what makes the positive control below meaningful. `similarity` and
// `effective_strength` are the values the response carried; `created` is the
// row's created_at, used as the reference-time proxy — see the limitation note
// on replayAgeDays.
//
// No database and no network: the fixture is the measurement.

import (
	"math"
	"sort"
	"testing"
	"time"
)

// replayRow is one row of the captured result set.
type replayRow struct {
	id      string
	sim     float64 // cosine, as returned in `similarity`
	eff     float64 // as returned in `effective_strength`
	created string  // created_at, YYYY-MM-DD
}

// replayAsOf is the date the capture was taken; ages are measured from it so the
// fixture does not drift as the test ages.
const replayAsOf = "2026-09-08"

// replayCosineBand is the measured width of the cosine band this embedding model
// produces within a single result set, from aihub#311 (commit 7ad96be): "the 0.6B
// embedding model packs every cosine in a result set into a band roughly 0.04
// wide (0.68-0.72 in the reported case)". It is the denominator the design doc's
// "6.8×" is expressed against, so it is stated once here rather than inlined.
const replayCosineBand = 0.04

// replayRows is the captured result set, in server order.
var replayRows = []replayRow{
	{"mem_S6DpSVo0", 0.31254468113716616, 0.8587992858036884, "2026-08-30"},
	{"mem_xx4FDYNg", 0.3014520586402456, 2.99910954325181, "2026-08-06"},
	{"mem_WUqJQtD0", 0.30161086340080057, 2.991935170401726, "2026-06-02"},
	{"mem_Y55d4MUg", 0.2989668010433675, 2.880640585868184, "2026-09-08"},
	{"mem_i9I2g8Hv", 0.2993377149105093, 2.2150385557547096, "2026-05-26"},
	{"mem_qkmEOhEi", 0.29287179069947, 2.7906869973440944, "2026-08-26"},
	{"mem_HPUHZk8q", 0.2819445553339357, 2.9999607087943128, "2026-09-07"},
	{"mem_PHwFqxs3", 0.2796186542056387, 2.999517596868778, "2026-08-28"},
	{"mem_hnVwWRcZ", 0.2789129947320217, 2.9994525363910904, "2026-08-13"},
	{"mem_qxJTWMlN", 0.28255655065515994, 2.4925105628667232, "2026-09-07"},
	{"mem_T5leEc0s", 0.2807794678675817, 0.22092905968426657, "2026-08-21"},
	{"mem_YlnN3R8H", 0.2713059510357321, 2.9987774018499334, "2026-08-12"},
	{"mem_0lxIn9AD", 0.26732799617385816, 2.969274277655463, "2026-09-06"},
	{"mem_YSPz6nef", 0.2709708536828581, 2.893723417619466, "2026-09-08"},
	{"mem_9bFi3zDo", 0.2710299138155503, 2.8899211909689093, "2026-09-08"},
	{"mem_izQfVuPj", 0.26598277085865485, 0.14307355277056416, "2026-08-18"},
	{"mem_FglE03TA", 0.25702233654393813, 2.9998880131770704, "2026-09-07"},
	{"mem_bHTDyTbv", 0.25926501441970695, 2.9994040456588147, "2026-09-01"},
	{"mem_vTXykzOY", 0.26136775382565025, 2.9977562034868894, "2026-08-12"},
	{"mem_vElGibAo", 0.26259514689445684, 2.991935058275997, "2026-06-02"},
}

// replayAgeDays is the age used for the recency term.
//
// ⚠️ Limitation, stated because it bounds what this file proves: the recall
// response does not carry last_activated_at, so created_at is used where
// memoryRefTime would use GREATEST(last_activated_at, created_at). For a row
// that has been activated the real reference time is NEWER, so the real age is
// SMALLER. That makes every age here an upper bound, and since the design-doc
// formula's damage grows with the SPREAD of ages, this fixture is conservative
// in the direction that matters: the real scramble is no smaller than measured.
func replayAgeDays(t *testing.T, created string) float64 {
	t.Helper()
	asOf, err := time.Parse("2006-01-02", replayAsOf)
	if err != nil {
		t.Fatalf("parse replayAsOf: %v", err)
	}
	d, err := time.Parse("2006-01-02", created)
	if err != nil {
		t.Fatalf("parse created %q: %v", created, err)
	}
	return asOf.Sub(d).Hours() / 24
}

// designDocScore is docs/design/polyforge-v1-design.md §7.5's withdrawn formula,
// transcribed. It is deliberately NOT wired to anything in production.
func designDocScore(t *testing.T, r replayRow, w float64) float64 {
	t.Helper()
	normalizedRecency := math.Exp(-replayAgeDays(t, r.created) / 30.0)
	normalizedStrength := r.eff / 5.0
	return r.sim*(1-w) + normalizedRecency*w + normalizedStrength*0.1
}

// similarityInversions counts ordered pairs (i<j) where the row ranked HIGHER is
// strictly less similar than one ranked below it — i.e. how often the ordering
// contradicts the signal it is supposed to be ranking by. C(20,2) = 190 pairs.
func similarityInversions(order []replayRow) int {
	n := 0
	for i := range order {
		for j := i + 1; j < len(order); j++ {
			if order[i].sim < order[j].sim-1e-12 {
				n++
			}
		}
	}
	return n
}

// TestReplayPositiveControl is the arm that makes every other number in this file
// mean something.
//
// It asserts that ordering the captured rows by the CURRENT implementation's key
// — `round(cosine,2) DESC, eff_strength DESC`, transcribed from
// memory_vector.go's ORDER BY — reproduces the order the server actually
// returned, row for row. Without this, a scrambled "after" ordering would prove
// nothing: it could equally mean the model of the current implementation is
// wrong. This is also an independent check that the bucketing is real, since a
// plain `cosine DESC` sort does NOT reproduce the captured order.
func TestReplayPositiveControl(t *testing.T) {
	modelled := append([]replayRow(nil), replayRows...)
	sort.SliceStable(modelled, func(i, j int) bool {
		bi := math.Round(modelled[i].sim*100) / 100
		bj := math.Round(modelled[j].sim*100) / 100
		if bi != bj {
			return bi > bj
		}
		return modelled[i].eff > modelled[j].eff
	})
	for i := range modelled {
		if modelled[i].id != replayRows[i].id {
			t.Fatalf("position %d: modelled %s, server returned %s.\n"+
				"The transcription of memory_vector.go's ORDER BY no longer reproduces the "+
				"captured result set, so every comparison in this file is against a model of "+
				"an implementation that does not exist. Re-derive the fixture before trusting "+
				"the numbers in docs/design/polyforge-v1-design.md §7.5.",
				i, modelled[i].id, replayRows[i].id)
		}
	}

	// Negative control on the control: plain cosine DESC must NOT reproduce it,
	// or the bucketing this file claims to demonstrate is doing nothing and the
	// arm above would pass for the wrong reason.
	plain := append([]replayRow(nil), replayRows...)
	sort.SliceStable(plain, func(i, j int) bool { return plain[i].sim > plain[j].sim })
	same := true
	for i := range plain {
		if plain[i].id != replayRows[i].id {
			same = false
			break
		}
	}
	if same {
		t.Error("an unbucketed `cosine DESC` sort also reproduces the captured order, so this " +
			"fixture cannot distinguish bucketed from unbucketed ranking and the positive " +
			"control above is vacuous — recapture with a result set that contains a tie")
	}
}

// TestDesignDocFormulaWouldScrambleRealResults pins the table in §7.5.
//
// Each expectation is a number that document states. If this test has to change,
// that section has to change with it — which is the coupling aihub#469 existed to
// create, since the defect it fixed was a doc asserting something about ranking
// that nothing checked.
func TestDesignDocFormulaWouldScrambleRealResults(t *testing.T) {
	topByCosine := replayRows[0].id // server order, and the set's highest cosine
	for _, r := range replayRows {
		if r.sim > replayRows[0].sim {
			t.Fatalf("fixture invariant broken: %s has a higher cosine than the first "+
				"returned row %s", r.id, topByCosine)
		}
	}

	if got := similarityInversions(replayRows); got != 16 {
		t.Errorf("current implementation: similarity inversions = %d, §7.5 says 16", got)
	}

	for _, tc := range []struct {
		w             float64
		wantTopRank   int // 1-based rank the highest-cosine row falls to
		wantMoved     int // positions differing from the server order
		wantInversion int
	}{
		{w: 0.3, wantTopRank: 10, wantMoved: 19, wantInversion: 98},
		{w: 0.4, wantTopRank: 10, wantMoved: 19, wantInversion: 100},
		{w: 0.9, wantTopRank: 9, wantMoved: 20, wantInversion: 102},
	} {
		reordered := append([]replayRow(nil), replayRows...)
		sort.SliceStable(reordered, func(i, j int) bool {
			return designDocScore(t, reordered[i], tc.w) > designDocScore(t, reordered[j], tc.w)
		})

		topRank, moved := 0, 0
		for i, r := range reordered {
			if r.id == topByCosine {
				topRank = i + 1
			}
			if r.id != replayRows[i].id {
				moved++
			}
		}
		inv := similarityInversions(reordered)

		if topRank != tc.wantTopRank {
			t.Errorf("w=%.1f: the set's highest-cosine row lands at rank %d, §7.5 says %d",
				tc.w, topRank, tc.wantTopRank)
		}
		if moved != tc.wantMoved {
			t.Errorf("w=%.1f: %d/20 positions change, §7.5 says %d", tc.w, moved, tc.wantMoved)
		}
		if inv != tc.wantInversion {
			t.Errorf("w=%.1f: similarity inversions = %d/190, §7.5 says %d",
				tc.w, inv, tc.wantInversion)
		}
	}
}

// TestRecencyDominatesCosineInClosedForm pins §7.5's closed-form claim, which is
// the part that generalises beyond this one captured result set.
//
// Overturn condition, from the design doc's own formula: a cosine gap dsim is
// overturned by a recency gap drec when dsim*(1-w) < w*drec, i.e.
// dsim < (w/(1-w))*drec.
func TestRecencyDominatesCosineInClosedForm(t *testing.T) {
	const w = 0.3
	drec30 := 1 - math.Exp(-30.0/30.0) // 0 days vs 30 days
	overturned := (w / (1 - w)) * drec30

	if math.Abs(overturned-0.2709) > 5e-5 {
		t.Errorf("a 30-day age gap overturns a cosine gap of %.4f, §7.5 says 0.2709", overturned)
	}
	if ratio := overturned / replayCosineBand; math.Abs(ratio-6.8) > 0.05 {
		t.Errorf("that is %.1f× the measured cosine band, §7.5 says 6.8×", ratio)
	}

	// The largest w that keeps a full cosine band decisive against a 30-day gap:
	// (w/(1-w))*drec < band  =>  w < band/(band+drec).
	maxW := replayCosineBand / (replayCosineBand + drec30)
	if maxW >= 0.06 || maxW <= 0.05 {
		t.Errorf("cosine stays dominant only for w < %.4f; §7.5 says the bound is below 0.06 "+
			"while the design doc's default was 0.3", maxW)
	}
	if maxW >= 0.3 {
		t.Fatalf("the design doc's default w=0.3 would be SAFE by this criterion (bound %.4f) "+
			"— §7.5's central argument does not hold and must be rewritten", maxW)
	}
}
