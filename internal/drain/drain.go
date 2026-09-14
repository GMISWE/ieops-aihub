// Package drain implements polyforge's Layer 3 continuous scheduler: the policy half of
// `polyforge drain`, which repeatedly selects the work items that are executable right now,
// runs them, and stops with a terminal state that says why it stopped.
//
// # What this package is and is not
//
// It is SCHEDULING POLICY ONLY — which work item to run next, how many at once, what to do when
// a claim loses a lock race, and when to stop (aihub#640's owner ruling of 2026-09-13,
// `scope_in`). It is NOT a second step-execution engine. Every piece of per-step logic —
// scenario resolution, the step bracket's pf_update_step sequencing, review-marker parsing,
// `role:` resolution, wrap-time worktree cleanup — is consumed from internal/engine through the
// same `polyforge engine <verb>` surface the markdown B/C loop calls
// (plugins/polyforge/skills/pf-execute/engine.native.md). That is not a stylistic preference; it
// is aihub#640's recorded acceptance standard, `workflow_identity_constraint`:
//
//	A mode must not contain any execution logic that B/C does not have. Differences are
//	permitted in exactly three places: how it is triggered (process vs session), whether a
//	human is at the step boundary, and whether the orchestrator is code or an LLM.
//
// So if you find yourself about to add step-execution logic HERE, it belongs in internal/engine
// where both callers can reach it. The single sanctioned difference is a capability SUPERSET,
// not a logic fork: because A spawns independent OS processes it can use a different vendor per
// step (see channel.go), where B/C are pinned to whichever harness the session runs in.
//
// # Design rule, inherited from internal/engine
//
// Everything in this package is plain data in, plain data out. Selection, convergence and
// snapshot shaping are pure functions over structs; every side effect (HTTP to aihub, spawning a
// harness process, touching the filesystem, reading the clock) enters through an injected
// function type on Runner (runner.go). The real implementations live in internal/cli. This keeps
// the scheduler unit-testable with no live aihub server, no harness credentials and no git
// checkout — which matters more here than usual, because the thing being tested is a loop whose
// bugs are "it claimed and executed 40 real work items in the wrong order".
package drain

// Terminal is how a drain run ended. There are FOUR, not two, and the distinction between the
// middle pair is the whole point (aihub#640 `four_terminal_states`): IDLE and BLOCKED_EXTERNAL
// both mean "there is work left that I did not do", and they differ only in whether waiting is
// worth anything. Coming back later fixes IDLE by itself; nothing about BLOCKED_EXTERNAL
// improves without another person acting, which is why it notifies and IDLE does not.
type Terminal string

const (
	// TerminalCompleted: every in-scope work item reached a terminal status. Nothing to do,
	// and nothing left to come back for.
	TerminalCompleted Terminal = "COMPLETED"
	// TerminalIdle: in-scope work remains, but all of it waits on MY OWN unfinished work
	// items. Re-running later, after those finish, makes progress with no human involved.
	TerminalIdle Terminal = "IDLE"
	// TerminalBlockedExternal: in-scope work remains and is blocked by work items outside my
	// scope — someone else's. Waiting is pointless; a human has to act, so this state is the
	// one that notifies (notify.go layer 2).
	TerminalBlockedExternal Terminal = "BLOCKED_EXTERNAL"
	// TerminalFailed: at least one work item's execution failed outright. Needs a human.
	TerminalFailed Terminal = "FAILED"
)

// ExitCode maps a Terminal to the process exit status. This mapping is layer ① of the
// three-layer notification design (aihub#640 `notification_three_layers`: "exit code 区分四种终
// 止状态").
//
// The specific NUMBERS are implementation detail settled here, not an owner ruling. Three
// constraints picked them: 0 must mean the good outcome (COMPLETED) so `polyforge drain && ...`
// chains correctly; 1 and 2 are left alone because the CLI already uses them for usage errors
// and an unhandled failure, and a scheduler outcome must not be confusable with "you typed the
// flag wrong"; and everything at or above 126 is reserved by POSIX shells (126/127 for
// exec failures, 128+n for signals). 10-12 sits clear of all three.
func ExitCode(t Terminal) int {
	switch t {
	case TerminalCompleted:
		return 0
	case TerminalIdle:
		return 10
	case TerminalBlockedExternal:
		return 11
	case TerminalFailed:
		return 12
	default:
		// An unknown Terminal is a programming error in this package, not a user outcome.
		// Report it as the generic failure rather than silently as success: a scheduler that
		// exits 0 on a state it does not recognise is the "gate certifying its own blind
		// spot" shape aihub#657 shipped and had to fix.
		return 2
	}
}

// StopReason is WHY the loop stopped, which is a different question from what state the queue
// was left in (Terminal). Both are reported: Terminal drives the exit code because the owner
// ruling says the exit code distinguishes the four terminal states, and adding a fifth code for
// a stop reason would contradict that; StopReason rides in the snapshot and on stderr.
//
// The pairing is many-to-one, not redundant. StopDivergence in particular can co-occur with
// TerminalIdle — "there is more to do, and I am deliberately not doing it" — and that pairing
// is the reason this type exists rather than being folded into Terminal.
type StopReason string

const (
	// StopQueueDrained: a round found nothing it could execute. The ordinary ending.
	StopQueueDrained StopReason = "queue_drained"
	// StopNothingClaimable: the round had candidates and could not claim a single one, because
	// every last one was held by another live attempt or went terminal between listing and
	// claiming. Distinct from queue_drained on purpose: the work is still there.
	StopNothingClaimable StopReason = "nothing_claimable"
	// StopDivergence: the round created at least as many work items as it completed
	// (aihub#640 `convergence_divergence_detector`). The queue is growing, not shrinking, so
	// continuing would not converge. Stop and let a human look.
	StopDivergence StopReason = "divergence"
	// StopMaxRounds: the --max-rounds budget was spent.
	StopMaxRounds StopReason = "max_rounds"
	// StopMaxWorkItems: the --max-work-items budget was spent.
	StopMaxWorkItems StopReason = "max_work_items"
	// StopMaxDuration: the --max-duration budget was spent (aihub#640
	// `convergence_divergence_detector`: "加上原有预算类（跑满 N 个 / T 时间）"). Distinct from
	// StopCancelled even though both arrive as a cancelled context: one is this run's own budget
	// doing its job, the other is somebody stopping it, and an operator reading a report needs to
	// know which. See Runner.Run.
	StopMaxDuration StopReason = "max_duration"
	// StopCancelled: the context was cancelled (SIGINT/SIGTERM, or `polyforge drain --stop`).
	StopCancelled StopReason = "cancelled"
)

// Result is how executing one work item ended. Note that lock_blocked and paused are NOT
// failures: they are ordinary control flow the scheduler is required to handle (aihub#640
// `two_kinds_of_blocking`), and counting them as failures would make TerminalFailed fire on a
// perfectly healthy run where two people happened to drain the same project.
type Result string

const (
	// ResultWrapped: every step completed and the attempt was wrapped.
	ResultWrapped Result = "wrapped"
	// ResultFailed: a step or a review failed; the attempt was completed with status=failed.
	ResultFailed Result = "failed"
	// ResultLockBlocked: the claim lost a resource-lock race (409 CONFLICT_LOCK_TAKEN). The
	// work item was never claimed, so there is nothing to clean up; it is simply skipped.
	ResultLockBlocked Result = "lock_blocked"
	// ResultPaused: a step called pf_pause_attempt, or a pf_* call came back "attempt is
	// paused". The loop stops for this work item WITHOUT calling pf_complete_attempt
	// (engine-native-details.md §0e) and moves on to the next one.
	ResultPaused Result = "paused"
	// ResultClaimFailed: the claim failed for a reason that is not a lock conflict (the work
	// item was claimed by someone else first, or went terminal between listing and claiming).
	// Skipped, not failed: losing a race is not a defect.
	ResultClaimFailed Result = "claim_failed"
	// ResultCancelled: the run was cancelled (SIGINT/SIGTERM) while this work item was mid-step.
	//
	// Distinct from ResultPaused, which it used to be reported as, and the distinction matters
	// to whoever reads the run: a pause was somebody's deliberate hand-off, while this is an
	// attempt left running because the scheduler was stopped. Both leave the attempt claimed
	// and neither completes it, but only one of them means "a person decided this".
	ResultCancelled Result = "cancelled"
)

// Executed reports whether drain actually ran this work item, as opposed to skipping it before
// any step began. It is what the --max-work-items budget counts: a lock race resolved in
// milliseconds is not a work item's worth of execution, and charging the budget for it let two
// contended work items exhaust a cap having run nothing.
func (r Result) Executed() bool {
	switch r {
	case ResultWrapped, ResultFailed, ResultPaused, ResultCancelled:
		return true
	default:
		return false
	}
}

// NeedsHuman reports whether a Result requires a person before that work item can progress.
//
// A lock race resolves itself the next time drain runs, and a pause was a deliberate hand-off
// that already told somebody. The other two both need a person, for different reasons:
//
//   - ResultFailed is a genuine execution failure.
//   - ResultCancelled leaves the attempt CLAIMED, status `running`, holding its locks — its own
//     Err says so verbatim — and drain never picks a running work item back up: Executable asks
//     the server for `ready_only`, which is `queued`. So "run it again later" accomplishes
//     nothing, and IDLE, whose entire contract is "come back later and it will work", is the one
//     answer that is certainly wrong. Before aihub#678 ③(c) a `--stop` or Ctrl-C ended exit 10
//     IDLE: the loop broke with StopCancelled, the final ObserveQueue failed on the cancelled
//     context, the fallback QueueState{Executable:1} classified with anyFailed=false, and the
//     machine-readable half of the notification design said "no human needed" while the prose
//     disclosure two screens away said the opposite. Same reasoning ErrNotSupported already
//     carries: a state that does not improve by waiting must not be reported as the one terminal
//     state that deliberately notifies nobody.
//
// ⚠️ This is about a work item ABANDONED MID-STEP. A cancellation that lands between rounds
// produces no ResultCancelled outcome at all — executeRound files "cancelled before claim" as
// ResultClaimFailed for candidates it never claimed — so an idle interrupt still ends IDLE, which
// is honest: nothing was stranded.
func (r Result) NeedsHuman() bool { return r == ResultFailed || r == ResultCancelled }

// Candidate is one work item drain could execute, projected down to the fields scheduling
// actually uses. It deliberately does NOT carry the work item's content: the scheduler never
// reads it, and the step agent fetches what it needs through pf_get_step.
type Candidate struct {
	ID        string `json:"id"`
	Slug      string `json:"slug"`
	Goal      string `json:"goal"`
	Priority  string `json:"priority"`
	WIType    string `json:"wi_type"`
	CreatedAt string `json:"created_at,omitempty"`
	// RequiresHumanSession mirrors the work item's own nullable column, POINTER and all.
	//
	// The three states are real and only one of them lets drain execute the work item: the
	// server's ready predicate is `requires_human_session = false`, which NULL does not satisfy
	// any more than true does. Flattening the pointer to a bool would make "unclassified" and
	// "an agent may take it unattended" the same value, and the bucket that reads this field
	// (QueueState.NeedsHumanSession) exists precisely to stop a work item drain cannot run from
	// being counted as absent.
	RequiresHumanSession *bool `json:"requires_human_session,omitempty"`
}

// Blocker names who stood between drain and a work item it wanted. It is filled for a
// ResultLockBlocked outcome (from the 409's own payload, which names the holder) and for an
// externally blocked work item (from its dependency edges).
type Blocker struct {
	// WorkItem is the blocking work item's slug or id, when the block is a dependency edge.
	WorkItem string `json:"work_item,omitempty"`
	// Actor is the display name of whoever holds the lock, when the block is a lock race.
	Actor string `json:"actor,omitempty"`
	// Resource is the contended lock key, when the block is a lock race.
	Resource string `json:"resource,omitempty"`
	// AttemptID is the RUN ATTEMPT holding the lock, when the block is a lock race.
	//
	// 🔴 It is what makes "skip, never retry" correct instead of merely safe. The rule's stated
	// reason (runner.go's `skipped` set) is that "the holder is another live attempt, and nothing
	// this run does will end it" — which is FALSE when the holder is one of this run's OWN
	// concurrent claims. At --max-parallel>=2 worker A claims wi1 and worker B loses the race on
	// wi2; wi1 then wraps and releases the lock, but wi2 has already been skipped for the whole
	// run, so the next round filters it away, finds nothing, and stops StopQueueDrained while its
	// own final observation reports executable=1 (aihub#678 ③(b)).
	//
	// The server has always named the holder: every CONFLICT_LOCK_TAKEN in
	// internal/domain/run_attempts.go carries `conflict_with.attempt_id` — the probe path, the
	// upsert-refusal path and both acquire_locks races. internal/cli's lockBlockerFrom simply
	// did not read it.
	AttemptID string `json:"attempt_id,omitempty"`
}

// Outcome is the record of one work item drain attempted in one round.
type Outcome struct {
	Candidate Candidate `json:"candidate"`
	Result    Result    `json:"result"`
	Blocker   *Blocker  `json:"blocker,omitempty"`
	// Steps counts the steps that reached status=completed for this work item.
	Steps int `json:"steps"`
	// Err is a human-readable failure description, empty on success.
	Err string `json:"error,omitempty"`
	// LogDir is the directory holding this work item's per-step output. This is layer ③ of the
	// observability answer: step output enters no LLM context by design, so the ONLY way a
	// failure is examinable afterwards is that this path exists and has the bytes in it.
	LogDir string `json:"log_dir,omitempty"`
	// RetryNextRound marks a ResultLockBlocked outcome whose holder was one of THIS RUN's own
	// concurrent claims, so the lock will be free once that sibling finishes. Such a work item is
	// re-offered next round instead of being skipped for the rest of the run. See
	// Blocker.AttemptID for what went wrong without it.
	RetryNextRound bool `json:"retry_next_round,omitempty"`
}
