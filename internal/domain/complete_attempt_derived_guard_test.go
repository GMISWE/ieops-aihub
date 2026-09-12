package domain

import (
	"strings"
	"testing"
)

// aihub#350 — hop 3/4 gates for the derived-disposition requirement on wrap.
//
// The wi's own acceptance criteria, restated as the arms below hold them:
//
//  1. the DEFAULT DIRECTION is measurable — the cheapest legal entry for a
//     finding is "folded", bare, while filed: and dropped: refuse to travel
//     without their payloads (TestFoldedIsTheCheapestLegalDerivedEntry);
//  2. a wrap that OMITS derived is refused, never defaulted to empty
//     (TestCompleteAttemptRefusesAWrapWithoutDerived), and an explicit [] is
//     legal (TestCompleteAttemptAcceptsAnExplicitlyEmptyDerived);
//  3. filed: naming a work item that does not resolve is refused — the half
//     that needs a database, held by complete_attempt_derived_db_test.go;
//  4. deleting the validation turns at least one of these red — they drive the
//     real FnCompleteAttempt, so there is no seam to delete around.
//
// Same instrument as complete_attempt_pause_reason_guard_test.go, whose header
// explains it: a nil *pgxpool.Pool tells "refused on the request's own
// contents" apart from "reached the database", because the second is a
// recovered panic. The guards under test sit before BeginTx on purpose - the
// refusal is decided without a database round-trip, which is also what lets
// this file run in the always-on Unit tests step, where AIHUB_TEST_DB is
// deliberately unset.
//
// 🔴 What NONE of these arms claim: that the list is truthful. The wi says the
// cheapest compliant call is derived: [] from an attempt that found three
// things, and the server cannot read the note's prose to catch it. These arms
// hold the friction asymmetry and the refusal surface - the two properties the
// gate really has - and nothing pretends to hold honesty.

// TestCompleteAttemptRefusesAWrapWithoutDerived is criterion 2's domain half:
// nil - the JSON absence - is refused BEFORE the pool is touched, and the
// refusal teaches the fix, because on the day this deploys every existing
// caller's wrap looks exactly like this one.
func TestCompleteAttemptRefusesAWrapWithoutDerived(t *testing.T) {
	v := runCompleteAttemptRequestChecks(t, &CompleteAttemptRequest{
		Status: "wrapped",
		// Derived deliberately absent: the zero value is nil, which is what an
		// omitted JSON field binds to.
	})
	if v.ReachedPool {
		t.Fatal("FnCompleteAttempt(status=\"wrapped\") with no derived reached the database.\n" +
			"aihub#350's measurement: 18 of 24 derived open work items had an already-wrapped " +
			"parent, because nothing at the wrap transition asked what became of the findings. " +
			"Defaulting the absent list to empty re-opens exactly that door - absence must be " +
			"refused, not normalised.")
	}
	for _, must := range []string{"derived", "folded", "filed:", "dropped:", "[]"} {
		if !strings.Contains(v.Refused, must) {
			t.Errorf("refusal = %q; want it to mention %q - the caller being refused is every "+
				"pre-aihub#350 wrap, and a refusal that does not teach the grammar and the "+
				"explicit-empty rule strands them", v.Refused, must)
		}
	}
}

// TestCompleteAttemptAcceptsAnExplicitlyEmptyDerived is the narrowness control
// on the arm above: [] is a statement ("I looked, there was nothing"), and the
// guard must accept it or the requirement collapses into "every wrap must
// invent a finding".
func TestCompleteAttemptAcceptsAnExplicitlyEmptyDerived(t *testing.T) {
	v := runCompleteAttemptRequestChecks(t, &CompleteAttemptRequest{
		Status:  "wrapped",
		Derived: []string{},
	})
	if !v.ReachedPool {
		t.Fatalf("FnCompleteAttempt refused a wrap with derived=[]: %q.\n"+
			"The explicit empty list is the legal way to declare no findings; refusing it leaves "+
			"no compliant wrap at all for an attempt that found nothing.", v.Refused)
	}
}

// TestFoldedIsTheCheapestLegalDerivedEntry is criterion 1 at the shape layer:
// the wi's whole point is which disposition the friction favours. "folded"
// travels bare; "filed:" and "dropped:" are refused without their payloads. If
// this arm ever needs a payload added to folded, or a bare filed: starts
// passing, the asymmetry the measurement asked for is gone even though "the
// field exists".
func TestFoldedIsTheCheapestLegalDerivedEntry(t *testing.T) {
	t.Run("folded_bare_is_complete", func(t *testing.T) {
		v := runCompleteAttemptRequestChecks(t, &CompleteAttemptRequest{
			Status:  "wrapped",
			Derived: []string{"folded"},
		})
		if !v.ReachedPool {
			t.Fatalf("a bare \"folded\" was refused: %q - the default disposition must be the "+
				"cheapest legal entry, and bare is as cheap as an entry gets", v.Refused)
		}
	})
	t.Run("folded_with_text_is_also_legal", func(t *testing.T) {
		v := runCompleteAttemptRequestChecks(t, &CompleteAttemptRequest{
			Status:  "wrapped",
			Derived: []string{"folded: helpers_test.go seeds a fixed clock"},
		})
		if !v.ReachedPool {
			t.Fatalf("\"folded:<text>\" was refused: %q - attaching the finding's summary must "+
				"never cost more than omitting it, or callers are trained to write less", v.Refused)
		}
	})
	t.Run("filed_demands_its_ref", func(t *testing.T) {
		for _, entry := range []string{"filed:", "filed:   "} {
			v := runCompleteAttemptRequestChecks(t, &CompleteAttemptRequest{
				Status:  "wrapped",
				Derived: []string{entry},
			})
			if v.ReachedPool {
				t.Fatalf("derived=[%q] reached the database - filed: with no ref is the claim "+
					"\"I opened a wi\" about nothing, and it must cost at least the wi's name", entry)
			}
			if !strings.Contains(v.Refused, "filed:") {
				t.Errorf("refusal = %q; want it to name filed:", v.Refused)
			}
		}
	})
	t.Run("dropped_demands_its_reason", func(t *testing.T) {
		v := runCompleteAttemptRequestChecks(t, &CompleteAttemptRequest{
			Status:  "wrapped",
			Derived: []string{"dropped:"},
		})
		if v.ReachedPool {
			t.Fatal("derived=[\"dropped:\"] reached the database - a drop with no reason is a " +
				"finding falling on the floor with a word next to it; the reason is the cost " +
				"that keeps dropping dearer than folding")
		}
		if !strings.Contains(v.Refused, "reason") {
			t.Errorf("refusal = %q; want it to say the reason is what is missing", v.Refused)
		}
	})
	t.Run("an_unknown_verb_is_refused", func(t *testing.T) {
		v := runCompleteAttemptRequestChecks(t, &CompleteAttemptRequest{
			Status:  "wrapped",
			Derived: []string{"noted the flaky test"},
		})
		if v.ReachedPool {
			t.Fatal("a free-text derived entry reached the database - free text is a fourth " +
				"disposition nobody defined, and admitting it dissolves the vocabulary the " +
				"asymmetry is built on")
		}
		for _, must := range []string{"folded", "filed:", "dropped:"} {
			if !strings.Contains(v.Refused, must) {
				t.Errorf("refusal = %q; want it to teach the three forms (missing %q)", v.Refused, must)
			}
		}
	})
}

// TestDerivedIsRefusedOnNonWrappedStatuses is the aihub#452 posture applied to
// the new field: a non-empty list on a status that stores nothing is refused
// rather than dropped, and an empty one states nothing and passes. Both halves
// matter - the first keeps a caller from believing dispositions were recorded
// on a pause, the second keeps the guard's surface no wider than the ambiguity.
func TestDerivedIsRefusedOnNonWrappedStatuses(t *testing.T) {
	for _, status := range []string{"failed", "paused"} {
		t.Run(status+"/non_empty_refused", func(t *testing.T) {
			v := runCompleteAttemptRequestChecks(t, &CompleteAttemptRequest{
				Status:  status,
				Derived: []string{"folded"},
			})
			if v.ReachedPool {
				t.Fatalf("FnCompleteAttempt(status=%q) with a non-empty derived reached the "+
					"database - nothing on this path records it, so accepting it is a promise "+
					"the store cannot keep (the pause_reason lesson, aihub#452)", status)
			}
			if !strings.Contains(v.Refused, status) {
				t.Errorf("refusal = %q; want it to name the offending status %q", v.Refused, status)
			}
		})
		t.Run(status+"/empty_passes", func(t *testing.T) {
			v := runCompleteAttemptRequestChecks(t, &CompleteAttemptRequest{
				Status:  status,
				Derived: []string{},
			})
			if !v.ReachedPool {
				t.Fatalf("FnCompleteAttempt(status=%q) refused an EMPTY derived: %q - an empty "+
					"list on a non-terminal status states nothing and there is nothing to "+
					"misplace", status, v.Refused)
			}
		})
	}
}

// TestValidateDerivedIsTheSharedShapeAuthority pins the property the MCP hop
// leans on: ValidateDerived is exported and decides shape for BOTH hops, so
// this file's verdicts and the tool-side pre-note refusals cannot drift. It
// drives the exported function directly with the same inputs the arms above
// route through FnCompleteAttempt, and requires the verdicts to agree.
func TestValidateDerivedIsTheSharedShapeAuthority(t *testing.T) {
	cases := []struct {
		entry string
		legal bool
	}{
		{"folded", true},
		{"folded: with text", true},
		{"filed:aihub#350", true},
		{"dropped:duplicate of a wrapped wi", true},
		{"filed:", false},
		{"dropped:  ", false},
		{"speculation", false},
	}
	for _, c := range cases {
		aerr := ValidateDerived([]string{c.entry})
		if c.legal && aerr != nil {
			t.Errorf("ValidateDerived([%q]) = %q, want nil", c.entry, aerr.Message)
		}
		if !c.legal && aerr == nil {
			t.Errorf("ValidateDerived([%q]) = nil, want a refusal", c.entry)
		}
		v := runCompleteAttemptRequestChecks(t, &CompleteAttemptRequest{
			Status: "wrapped", Derived: []string{c.entry},
		})
		if v.ReachedPool != c.legal {
			t.Errorf("FnCompleteAttempt and ValidateDerived disagree on %q: reached_pool=%v, "+
				"shape_legal=%v - the two hops are supposed to share one authority", c.entry,
				v.ReachedPool, c.legal)
		}
	}
}

// TestDerivedFiledRefsExtraction pins the seam the transaction's resolution
// loop runs over: which entries name a work item, and with what trimming. A
// drifted extractor would validate the wrong string against the database while
// every shape test above stayed green.
func TestDerivedFiledRefsExtraction(t *testing.T) {
	got := derivedFiledRefs([]string{
		"folded",
		"filed:aihub#351",
		"dropped:not worth a row",
		"filed: wi_AbCd1234 ",
		"folded: mentions filed: in prose",
	})
	want := []string{"aihub#351", "wi_AbCd1234"}
	if len(got) != len(want) {
		t.Fatalf("derivedFiledRefs = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("derivedFiledRefs[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

// TestDerivedStatusValidationStillFirst mirrors the ordering arm of the
// pause_reason file: an unknown status is refused for BEING unknown, even
// carrying a derived list with problems of its own, so a caller who typo'd the
// status is not told to fix a field that was never their problem.
func TestDerivedStatusValidationStillFirst(t *testing.T) {
	v := runCompleteAttemptRequestChecks(t, &CompleteAttemptRequest{
		Status:  "wraped",
		Derived: []string{"speculation"},
	})
	const wantPrefix = "status must be wrapped, failed, or paused"
	if !strings.HasPrefix(v.Refused, wantPrefix) {
		t.Errorf("refusal for an unknown status = %q (reached_pool=%v), want prefix %q",
			v.Refused, v.ReachedPool, wantPrefix)
	}
}
