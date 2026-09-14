package drain

import (
	"reflect"
	"testing"
)

func cand(id, priority, created string) Candidate {
	return Candidate{ID: id, Slug: id, Priority: priority, CreatedAt: created}
}

func ids(cs []Candidate) []string {
	out := make([]string, len(cs))
	for i, c := range cs {
		out[i] = c.ID
	}
	return out
}

// TestOrderCandidates_PriorityFirstThenOldestFirst pins both sort keys with ONE fixture whose
// answer changes if either is reversed — the input is deliberately arranged so that priority
// order and age order disagree.
//
// Mutants watched go red:
//   - reverse the priority comparison (`ri > rj`): "low-old" leads instead of "urgent-new".
//   - reverse the age comparison (`>` instead of `<`): normal-new leads normal-old.
//   - drop the age comparison entirely: the two `normal` entries keep input order, which for
//     this fixture is the wrong one.
func TestOrderCandidates_PriorityFirstThenOldestFirst(t *testing.T) {
	in := []Candidate{
		cand("normal-new", "normal", "2026-09-14T00:00:00Z"),
		cand("low-old", "low", "2020-01-01T00:00:00Z"),
		cand("urgent-new", "urgent", "2026-09-14T23:59:59Z"),
		cand("normal-old", "normal", "2026-09-01T00:00:00Z"),
		cand("high-mid", "high", "2026-09-10T00:00:00Z"),
	}
	want := []string{"urgent-new", "high-mid", "normal-old", "normal-new", "low-old"}
	if got := ids(OrderCandidates(in)); !reflect.DeepEqual(got, want) {
		t.Fatalf("order = %v\nwant  %v", got, want)
	}
}

// TestOrderCandidates_UnknownPriorityDoesNotJumpTheQueue is the negative control for
// priorityRank's default branch. An unrecognised value must sort AFTER every known one: a typo
// is not evidence of urgency, and ranking the unknown first would let one mistyped field
// preempt a genuinely urgent work item.
func TestOrderCandidates_UnknownPriorityDoesNotJumpTheQueue(t *testing.T) {
	in := []Candidate{
		cand("typo", "urgnet", "2000-01-01T00:00:00Z"), // oldest, so age cannot be the reason
		cand("real-low", "low", "2026-01-01T00:00:00Z"),
		cand("real-urgent", "urgent", "2026-01-01T00:00:00Z"),
	}
	got := ids(OrderCandidates(in))
	want := []string{"real-urgent", "real-low", "typo"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("order = %v\nwant  %v (an unknown priority must rank last, behind even `low`)", got, want)
	}
}

// TestOrderCandidates_IsTotalAndDoesNotMutateItsInput covers the two properties a dispatch-order
// assertion depends on. Without the id tiebreak the order of equal-key entries is whatever the
// server returned, so a test asserting dispatch order would pass or fail by luck; and a sort that
// mutated the caller's slice would reorder the round-freeze snapshot underneath its own id set.
func TestOrderCandidates_IsTotalAndDoesNotMutateItsInput(t *testing.T) {
	in := []Candidate{
		cand("zzz", "normal", "2026-01-01T00:00:00Z"),
		cand("aaa", "normal", "2026-01-01T00:00:00Z"),
		cand("mmm", "normal", "2026-01-01T00:00:00Z"),
	}
	before := ids(in)

	first := ids(OrderCandidates(in))
	if want := []string{"aaa", "mmm", "zzz"}; !reflect.DeepEqual(first, want) {
		t.Fatalf("identical priority+age did not fall back to the id tiebreak: got %v, want %v", first, want)
	}
	if second := ids(OrderCandidates(in)); !reflect.DeepEqual(first, second) {
		t.Fatalf("order is not reproducible: %v then %v", first, second)
	}
	if after := ids(in); !reflect.DeepEqual(before, after) {
		t.Fatalf("OrderCandidates mutated its input: %v -> %v", before, after)
	}
}

// TestFreezeRound_CapsAndReportsExactlyWhatItFroze pins the round freeze. The id set MUST match
// the returned slice: the set is the proliferation baseline, so a set wider than the slice would
// count a deferred work item as "already known" and hide it from the divergence detector
// forever.
func TestFreezeRound_CapsAndReportsExactlyWhatItFroze(t *testing.T) {
	in := []Candidate{
		cand("c", "normal", "2026-01-03T00:00:00Z"),
		cand("a", "urgent", "2026-01-01T00:00:00Z"),
		cand("b", "high", "2026-01-02T00:00:00Z"),
	}

	frozen, set := FreezeRound(in, 2)
	if got := ids(frozen); !reflect.DeepEqual(got, []string{"a", "b"}) {
		t.Fatalf("capped freeze = %v, want the two most urgent [a b]", got)
	}
	if len(set) != len(frozen) {
		t.Fatalf("id set has %d entries for %d frozen candidates", len(set), len(frozen))
	}
	for _, c := range frozen {
		if !set[c.ID] {
			t.Errorf("frozen candidate %s missing from the id set", c.ID)
		}
	}
	if set["c"] {
		t.Error("the id set contains `c`, which was capped out; it would be mistaken for a " +
			"pre-existing work item and never counted as newly created")
	}

	// A non-positive cap means unbounded (Budget.RemainingWorkItems returns -1 for that),
	// which must not be read as "freeze nothing".
	for _, cap := range []int{0, -1} {
		all, _ := FreezeRound(in, cap)
		if len(all) != 3 {
			t.Errorf("cap=%d froze %d candidates, want all 3 (non-positive means unbounded)", cap, len(all))
		}
	}
}

// TestCountCreatedDuringRound_CountsOnlyGenuinelyNewIDs is the divergence detector's numerator.
// The negative control is the middle case: a work item present before the round must never be
// counted, no matter how its status changed while the round ran.
func TestCountCreatedDuringRound_CountsOnlyGenuinelyNewIDs(t *testing.T) {
	before := IDSet([]Candidate{cand("a", "normal", ""), cand("b", "normal", "")})

	cases := []struct {
		name  string
		after []Candidate
		want  int
	}{
		{"nothing changed", []Candidate{cand("a", "normal", ""), cand("b", "normal", "")}, 0},
		{"two follow-ups filed", []Candidate{cand("a", "normal", ""), cand("b", "normal", ""), cand("c", "normal", ""), cand("d", "normal", "")}, 2},
		{"one finished, none created", []Candidate{cand("a", "normal", "")}, 0},
		{"one finished while one was created", []Candidate{cand("a", "normal", ""), cand("z", "normal", "")}, 1},
		{"everything is new", []Candidate{cand("x", "normal", ""), cand("y", "normal", "")}, 2},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := CountCreatedDuringRound(before, tc.after); got != tc.want {
				t.Fatalf("CountCreatedDuringRound = %d, want %d", got, tc.want)
			}
		})
	}
}

// TestScopeMine_NeedsAUserID guards the union query in internal/cli: Mine() false means "do not
// add an ownership filter", so a Scope with All=false and an empty UserID must NOT report Mine().
// Reporting it would send `user_id=` to the server and scope the run to nobody.
func TestScopeMine_NeedsAUserID(t *testing.T) {
	if (Scope{}).Mine() {
		t.Error("an empty Scope reported Mine(); the ownership filter would be sent empty")
	}
	if (Scope{All: true, UserID: "u_x"}).Mine() {
		t.Error("--all with a user id reported Mine(); --all must drop the ownership filter")
	}
	if !(Scope{UserID: "u_x"}).Mine() {
		t.Error("a scoped run with a user id did not report Mine()")
	}
}
