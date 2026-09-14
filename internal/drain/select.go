package drain

import "sort"

// Scope answers "whose work items is this drain run allowed to execute?" — the question
// aihub#640 `scope_BLOCKED_on_652` called undefinable at design time, because assignee did not
// exist and pf_list_work_items.user_id is REPORTER-only (aihub#383).
//
// aihub#652 closed it, but not the way the design sketch guessed. The sketch proposed an
// assignee column with ownership = COALESCE(assignee, reporter); the wi's own
// CORRECTION_2026_09_13_premise_falsified rejected that as a second source of truth, because
// "who claimed it" was ALREADY persisted in run_attempts.actor_user_id. What shipped instead is
// a `claimed_by` filter derived from that column. So ownership here is the union of two
// server-side filters, not a stored field:
//
//	mine = reporter == me  OR  current-attempt claimant == me
//
// That union is exactly the COALESCE the ruling asked for, evaluated at query time. Note the
// asymmetry it implies and which callers must not forget: `claimed_by` matches only a CURRENT
// attempt, so a queued work item — the only kind drain can start — never matches it. The
// claimed_by half is what keeps my own running/paused work items visible for the IDLE vs
// COMPLETED decision; the reporter half is what supplies executable candidates.
type Scope struct {
	// All disables scoping entirely. Retained per the ruling ("`--all` 保留但不默认") and
	// deliberately not the default: two people draining the same project under --all do
	// nothing but take turns losing lock races.
	All bool
	// UserID is the caller's user id, from pf_whoami. Ignored when All is true.
	UserID string
}

// Mine reports whether the scope is user-limited.
func (s Scope) Mine() bool { return !s.All && s.UserID != "" }

// priorityRank orders the four priority values from most to least urgent. An unrecognised value
// sorts after every known one rather than before: an unknown priority is not evidence of
// urgency, and treating it as urgent would let a typo jump the queue.
func priorityRank(p string) int {
	switch p {
	case "urgent":
		return 0
	case "high":
		return 1
	case "normal":
		return 2
	case "low":
		return 3
	default:
		return 4
	}
}

// OrderCandidates sorts candidates into the order drain will claim them: priority descending,
// then oldest first within a priority, then by id so the result is total and therefore
// reproducible.
//
// The priority half mirrors what the server's own ready queue does, so drain and
// `pf_get_ready_queue` agree about what is most urgent. The age half is this package's choice
// and it is FIFO on purpose: the alternative (newest first) starves the oldest work item in the
// queue exactly when the queue is long, which is when starvation matters. The id tiebreak is
// not cosmetic — without a total order, two runs over the same queue can interleave differently
// and a dispatch-order assertion becomes flaky.
//
// It sorts a copy; the input slice is not modified.
func OrderCandidates(in []Candidate) []Candidate {
	out := make([]Candidate, len(in))
	copy(out, in)
	sort.SliceStable(out, func(i, j int) bool {
		ri, rj := priorityRank(out[i].Priority), priorityRank(out[j].Priority)
		if ri != rj {
			return ri < rj
		}
		// Both are RFC3339 from the same server, so lexical order is chronological order.
		// A MISSING created_at (the ready queue omits it on items[]) sorts LAST rather than
		// first: "" < every real timestamp, so the naive comparison made an item with no date
		// leapfrog every dated peer at the same priority — the opposite of the FIFO the age
		// key exists to provide, and worst exactly when the batch is mixed.
		ai, aj := out[i].CreatedAt, out[j].CreatedAt
		if (ai == "") != (aj == "") {
			return aj == ""
		}
		if ai != aj {
			return ai < aj
		}
		return out[i].ID < out[j].ID
	})
	return out
}

// FreezeRound applies the proliferation budget (aihub#640 `convergence_divergence_detector`:
// "本轮新建的 wi 不进本轮队列") by returning the round's candidate list as a frozen, ordered
// snapshot, plus the set of ids that snapshot contains.
//
// Freezing is what makes a round a round. Without it, a work item whose execution files two
// follow-ups would see those follow-ups picked up inside the same round, and a scenario that
// reliably files follow-ups would never yield control back to the convergence check — the loop
// would be unbounded even though every individual step terminated. With it, anything created
// during round N is first considered in round N+1, where the divergence detector has already
// had a chance to look at it.
func FreezeRound(candidates []Candidate, max int) ([]Candidate, map[string]bool) {
	ordered := OrderCandidates(candidates)
	if max > 0 && len(ordered) > max {
		ordered = ordered[:max]
	}
	ids := make(map[string]bool, len(ordered))
	for _, c := range ordered {
		ids[c.ID] = true
	}
	return ordered, ids
}

// CountCreatedDuringRound returns how many of `after` are work items that were not in the
// round's frozen set and were not there before it started — the numerator of the divergence
// check.
//
// `before` is every work item id drain knew about when the round began (not just the executable
// ones): a follow-up filed as `blocked` is still proliferation even though it is not ready, and
// counting only ready work items would miss precisely the case the detector exists to catch —
// a wi that spawns a chain of blocked successors.
func CountCreatedDuringRound(before map[string]bool, after []Candidate) int {
	n := 0
	for _, c := range after {
		if !before[c.ID] {
			n++
		}
	}
	return n
}

// filterSkipped drops candidates this run has already declined to execute. See the `skipped`
// declaration in Runner.Run for why the skip is held for the whole run rather than one round.
func filterSkipped(in []Candidate, skipped map[string]bool) []Candidate {
	if len(skipped) == 0 {
		return in
	}
	out := make([]Candidate, 0, len(in))
	for _, c := range in {
		if !skipped[c.ID] {
			out = append(out, c)
		}
	}
	return out
}

// IDSet collects candidate ids into a set, for the `before` argument of
// CountCreatedDuringRound.
func IDSet(in []Candidate) map[string]bool {
	s := make(map[string]bool, len(in))
	for _, c := range in {
		s[c.ID] = true
	}
	return s
}
