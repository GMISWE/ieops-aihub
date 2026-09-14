package drain

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
	// Created counts work items that came into existence DURING this round
	// (CountCreatedDuringRound).
	Created int
	// Executable counts the work items the round actually had available to run — the size of
	// the frozen candidate set. Zero means the round found nothing to do.
	Executable int
}

// Attempted is how many work items the round actually tried to execute.
func (t RoundTally) Attempted() int {
	return t.Completed + t.Failed + t.LockBlocked + t.Paused
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

// QueueState is what remains in scope after a round, as observed from the server. It is
// separate from RoundTally because it answers a different question — not "what did I just do"
// but "is there anything left, and can I do it myself".
type QueueState struct {
	// Executable counts in-scope work items that are ready to claim right now.
	Executable int
	// BlockedByMine counts in-scope work items blocked by dependencies that are themselves in
	// scope. Waiting helps: finishing my own work unblocks these.
	BlockedByMine int
	// BlockedByOthers counts in-scope work items blocked by dependencies OUTSIDE my scope.
	// Waiting does not help; only another person acting does.
	BlockedByOthers int
	// Running counts in-scope work items with a live attempt (mine or, under --all, anyone's)
	// that this run did not start. They are neither executable nor blocked, but they are
	// certainly not "done", so they must keep COMPLETED from firing.
	Running int
	// Paused counts in-scope paused work items. Drain never resumes them by default
	// (aihub#640 wi.content "权限边界"), but their existence means the project is not complete.
	Paused int
}

// Idle reports whether anything at all is left in scope.
func (q QueueState) Empty() bool {
	return q.Executable == 0 && q.BlockedByMine == 0 && q.BlockedByOthers == 0 &&
		q.Running == 0 && q.Paused == 0
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
func Classify(anyFailed bool, q QueueState) Terminal {
	switch {
	case anyFailed:
		return TerminalFailed
	case q.BlockedByOthers > 0:
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
