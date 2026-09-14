package drain

import (
	"encoding/json"
	"time"
)

// RoundTally is what one round did, and it is the only input the convergence decision takes.
// Keeping it a plain struct (rather than letting ClassifyRound reach back into the runner) is
// what makes every branch below testable without a server.
type RoundTally struct {
	// Completed counts work items wrapped this round.
	Completed int
	// Failed counts work items whose execution failed this round.
	Failed int
	// LockBlocked counts work items skipped because another live attempt held a lock.
	LockBlocked int
	// Paused counts work items that paused themselves mid-run.
	Paused int
	// Cancelled counts work items abandoned mid-step because the run was cancelled.
	Cancelled int
	// Created counts work items that came into existence DURING this round
	// (CountCreatedDuringRound).
	Created int
	// Executable counts the work items the round actually had available to run — the size of
	// the frozen candidate set. Zero means the round found nothing to do.
	Executable int
}

// Attempted is how many work items the round actually tried to execute.
func (t RoundTally) Attempted() int {
	return t.Completed + t.Failed + t.LockBlocked + t.Paused + t.Cancelled
}

// Diverging reports whether this round created at least as many work items as it completed,
// the owner's divergence criterion verbatim (aihub#640 `convergence_divergence_detector`:
// "本轮新建 wi 数 ≥ 完成 wi 数 ⇒ 立刻停下来让人看").
//
// The `Created > 0` guard is NOT a weakening of that rule, it is what makes the rule mean what
// it says. Read literally, `Created >= Completed` is true for the round that created nothing and
// completed nothing — the single most common way for a drain run to end, and the exact opposite
// of proliferation. Firing "the queue is growing" on a round where the queue provably did not
// grow would make the loudest signal in the system also its least trustworthy one. Proliferation
// requires something to have proliferated.
//
// Everything else is the ruling unchanged, including the equality: a round that files one
// follow-up for every work item it finishes is treading water, and treading water forever is
// the failure mode the detector exists to catch. aihub's own history of "跟进批 / 清尾批" is
// cited in the ruling as the evidence this is real rather than theoretical.
func (t RoundTally) Diverging() bool {
	return t.Created > 0 && t.Created >= t.Completed
}

// BlockerRef is one dependency edge's far end: the work item that is holding mine up.
//
// It is a struct rather than the bare id string it used to be because the two things the run does
// with a blocker are DIFFERENT questions, and collapsing them produced a measured defect. Layer ②
// needs something to write a note ON, which has to be a work item this caller may open; the
// report and the log need something a person can READ, which is the slug. When a blocker lives in
// a project the caller is not a member of, aihub deliberately answers with the slug and the
// sentinel id "hidden" (internal/domain/dependencies.go, aihub#377 invariant 2) — so the one
// value is present and the other is unusable, and the old code fed the unusable one straight to
// pf_emit_event as `work_item_id: "hidden"`, producing a 404 on every single round (aihub#678 ③(a)).
type BlockerRef struct {
	// ID is the blocker's work item id, or "" when the caller may not open it.
	ID string `json:"id,omitempty"`
	// Slug is the blocker's slug. aihub sends it even for an inaccessible far end, on purpose,
	// so this is the field that is always worth printing.
	Slug string `json:"slug,omitempty"`
}

// Display is what to show a person: the slug when there is one, else the id, else a marker that
// says which of the two is missing rather than an empty string.
func (b BlockerRef) Display() string {
	switch {
	case b.Slug != "":
		return b.Slug
	case b.ID != "":
		return b.ID
	default:
		return "(unnamed blocker)"
	}
}

// Notifiable reports whether layer ② can write a note on this blocker. False for a blocker in a
// project the caller cannot open: there is no id to address, and addressing the sentinel is how
// the 404-per-round came about.
func (b BlockerRef) Notifiable() bool { return b.ID != "" }

// UnmarshalJSON accepts BOTH this struct and the bare id string BlockedWorkItem.Blockers used to
// hold, so a snapshot written by an older binary still parses.
//
// This is not defensive politeness; without it the shape change breaks the one command that
// exists to stop a runaway scheduler. `polyforge drain --stop` and `polyforge watch` read
// snapshot.json through ReadSnapshot, which returns an error on ANY unmarshal failure — and an
// unattended run and its observer meet across versions BY CONSTRUCTION (Snapshot's own doc:
// "drain may have been running for hours when the binary on disk is replaced"). A run started by
// yesterday's binary that hit an external block writes `"blockers":["wi_x"]`; a new binary would
// fail to parse it, and `--stop` exits 1 without signalling anything. SnapshotVersion is bumped
// alongside this so watch still SAYS the shape changed; the version warning is a disclosure, not
// a parser, and only this makes the file readable.
func (b *BlockerRef) UnmarshalJSON(data []byte) error {
	if len(data) > 0 && data[0] == '"' {
		var s string
		if err := json.Unmarshal(data, &s); err != nil {
			return err
		}
		// The legacy value was whatever ObserveQueue had put in the list: an id for an
		// accessible blocker, and — this is the bug being fixed — the literal "hidden" sentinel
		// for one that was not. Reading it back as an ID would restore the 404 target, so the
		// sentinel is stripped here too and survives only as something to display.
		*b = BlockerRef{ID: s, Slug: s}
		if s == "hidden" {
			b.ID = ""
		}
		return nil
	}
	// A distinct type, so this method is not called recursively on the struct form.
	type blockerRefJSON BlockerRef
	var v blockerRefJSON
	if err := json.Unmarshal(data, &v); err != nil {
		return err
	}
	*b = BlockerRef(v)
	return nil
}

// BlockedWorkItem is one in-scope work item held up by dependencies OUTSIDE the scope, together
// with the blockers responsible. It exists so the BLOCKED_EXTERNAL notification has somewhere to
// land: notification layer ② writes a note on this work item and on each blocker, and neither is
// possible from a bare count.
type BlockedWorkItem struct {
	WorkItemID string       `json:"work_item_id"`
	Slug       string       `json:"slug,omitempty"`
	Blockers   []BlockerRef `json:"blockers,omitempty"`
}

// BlockerNames renders the blockers for a message.
func (b BlockedWorkItem) BlockerNames() []string {
	out := make([]string, 0, len(b.Blockers))
	for _, ref := range b.Blockers {
		out = append(out, ref.Display())
	}
	return out
}

// QueueState is what remains in scope after a round, as observed from the server. It is
// separate from RoundTally because it answers a different question — not "what did I just do"
// but "is there anything left, and can I do it myself".
type QueueState struct {
	// Executable counts in-scope work items that are ready to claim right now.
	Executable int `json:"executable"`
	// BlockedByMine counts in-scope work items blocked by dependencies that are themselves in
	// scope. Waiting helps: finishing my own work unblocks these.
	BlockedByMine int `json:"blocked_by_mine"`
	// ExternallyBlocked lists the in-scope work items blocked by dependencies OUTSIDE my scope.
	// Waiting does not help; only another person acting does.
	//
	// This is a LIST rather than the count it used to be, and the change is what makes the
	// BLOCKED_EXTERNAL notification real. With a count, the run could report the state and had
	// nothing to write a note on, so the one terminal state the design calls "the one that
	// notifies" notified nobody. Use BlockedByOthers() when only the number is wanted; there
	// is now no way for the two to disagree.
	ExternallyBlocked []BlockedWorkItem `json:"externally_blocked,omitempty"`
	// Running counts in-scope work items with a live attempt (mine or, under --all, anyone's)
	// that this run did not start. They are neither executable nor blocked, but they are
	// certainly not "done", so they must keep COMPLETED from firing.
	Running int `json:"running"`
	// Paused counts in-scope paused work items. Drain never resumes them by default
	// (aihub#640 wi.content "权限边界"), but their existence means the project is not complete.
	Paused int `json:"paused"`
	// NeedsHumanSession lists in-scope work items that are QUEUED and that drain is structurally
	// not allowed to execute, because `requires_human_session` is not false.
	//
	// 🔴 This is the bucket that did not exist, and its absence made COMPLETED — exit 0, "every
	// in-scope work item is terminal" — the answer for a project whose queue still held work
	// (aihub#678 ③(d)). ObserveQueue classified into executable / running / paused / blocked, and
	// a queued requires_human_session=true work item is none of those: the server's
	// readyOnlyPredicate requires `requires_human_session = false`, so it never appears in
	// Executable, while its status keeps it out of the other three. It fell through every bucket,
	// Empty() said the scope was clear, and the most reassuring of the four terminal states was
	// reported on the strength of a question nobody had asked. `--plan` shared the same code and
	// the same lie.
	//
	// ⚠️ "Not false", not "true". The column is nullable and the third state is real: NULL means
	// unclassified, and `requires_human_session = false` is not satisfied by NULL either, so a
	// NULL work item is equally unexecutable here. Treating NULL as false would put this bucket's
	// own blind spot back.
	NeedsHumanSession []Candidate `json:"needs_human_session,omitempty"`
}

// BlockedByOthers is how many in-scope work items are blocked from outside the scope.
func (q QueueState) BlockedByOthers() int { return len(q.ExternallyBlocked) }

// Empty reports whether anything at all is left in scope.
func (q QueueState) Empty() bool {
	return q.Executable == 0 && q.BlockedByMine == 0 && q.BlockedByOthers() == 0 &&
		q.Running == 0 && q.Paused == 0 && len(q.NeedsHumanSession) == 0
}

// Classify decides the run's Terminal state from the cumulative run totals and the final queue
// observation.
//
// Precedence, most to least urgent, and the reasoning for the order:
//
//  1. FAILED — something broke. A person must look at it before anything else is worth saying
//     about the queue, and a failure that got reported as IDLE would be invisible in the exit
//     code, which is layer ① of the whole notification design.
//  2. BLOCKED_EXTERNAL — work is left and I cannot unblock it. Also needs a person, but it is a
//     coordination problem rather than a defect, so it sorts under FAILED.
//  3. IDLE — work is left and it waits on me. No person needed; re-running later is the fix.
//  4. COMPLETED — nothing is left.
//
// The IDLE/BLOCKED_EXTERNAL split is checked in that order (external first) because a run can
// have both kinds of leftover at once; when it does, the one that needs a human wins, since the
// whole purpose of distinguishing them is deciding whether to bother somebody.
//
// anyFailed is cumulative over the whole run, not per-round: a failure in round 1 still needs a
// human after round 5 succeeds, and letting a later clean round overwrite it would silently
// discard the report.
//
// # Why a queued requires_human_session work item sorts with BLOCKED_EXTERNAL
//
// It is not "blocked by someone else's work item", so the literal reading of the state's name
// does not cover it. The owner's own CRITERION does, and the criterion is what this switch is
// built on: `four_terminal_states` says the IDLE / BLOCKED_EXTERNAL split is "等下去有没有意义" —
// whether waiting is worth anything. Re-running drain tomorrow does not make a
// requires_human_session work item executable; only a person sitting down with it does. IDLE's
// contract is the opposite of that ("no person needed; re-running later is the fix"), so IDLE
// would be as wrong as the COMPLETED this replaces, just less loudly. Reporting it as the state
// that means "a human must act" is the honest use of the four the ruling fixes; inventing a fifth
// would contradict the ruling that the exit code distinguishes exactly four.
func Classify(anyFailed bool, q QueueState) Terminal {
	switch {
	case anyFailed:
		return TerminalFailed
	case q.BlockedByOthers() > 0 || len(q.NeedsHumanSession) > 0:
		return TerminalBlockedExternal
	case !q.Empty():
		return TerminalIdle
	default:
		return TerminalCompleted
	}
}

// Budget bounds a run in the three dimensions the design names (aihub#640
// `convergence_divergence_detector`: "加上原有预算类（跑满 N 个 / T 时间）"). A zero value in any
// field means that dimension is unbounded.
type Budget struct {
	// MaxRounds caps how many scheduling rounds run.
	MaxRounds int
	// MaxWorkItems caps how many work items are executed across the whole run.
	MaxWorkItems int
	// MaxParallel caps how many work items execute concurrently within one round.
	MaxParallel int
	// MaxDuration caps the run's WALL CLOCK.
	//
	// 🔴 This dimension was missing entirely until aihub#678 ④, while the design named it
	// verbatim ("跑满 N 个 / T 时间"). Without it a detached run's only ceiling was
	// `MaxRounds × MaxWorkItems × stepTimeout`, both of which default to unlimited — so
	// `polyforge drain --detach` had, in practice, no wall-clock bound at all. Runner.Run turns
	// it into a context deadline rather than a round-boundary test; see the comment there for
	// why, and for what that means for the terminal state.
	MaxDuration time.Duration
}

// DefaultMaxParallel is the concurrency drain uses when --max-parallel is not given.
//
// Measured, not guessed, and the two numbers behind it disagree — which is why the smaller one
// wins. aihub#640 `harness_survey_2026_09_13.concurrency_ceiling` records Claude Code's HARD
// quota as 20 concurrent subagents (60 dispatch errors, all of them that quota), alongside a
// team-experience stable value of about 9. The hard ceiling is the wrong number to default to:
// it is where dispatch starts failing, and aihub's own orchestration memory records what
// happens on the way to it — 12 parallel agents lost 4 to HTTP 429, after which the inference
// gateway returned 503 for the rest. A default should sit where throughput is, not where the
// cliff is.
//
// Note this bounds work items, not processes: a step agent is one process at a time per work
// item, so 8 work items in flight is 8 harness processes, not 8×steps.
const DefaultMaxParallel = 8

// Parallelism returns the effective per-round concurrency for a budget, never below 1.
func (b Budget) Parallelism() int {
	if b.MaxParallel <= 0 {
		return DefaultMaxParallel
	}
	return b.MaxParallel
}

// RemainingWorkItems reports how many more work items the budget allows after `done` have been
// executed, or -1 when that dimension is unbounded.
func (b Budget) RemainingWorkItems(done int) int {
	if b.MaxWorkItems <= 0 {
		return -1
	}
	if done >= b.MaxWorkItems {
		return 0
	}
	return b.MaxWorkItems - done
}
