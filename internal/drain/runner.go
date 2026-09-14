package drain

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/GMISWE/ieops-aihub/internal/engine"
)

// ClaimInfo is what a successful claim gave back: the identifiers the rest of the run threads
// through, and the worktrees wrap-time cleanup will remove.
type ClaimInfo struct {
	WorkItemID   string
	AttemptID    string
	WIType       string
	ScenarioURL  string
	WorktreeRoot string
	// Worktrees maps repo name to worktree path — pf_complete_attempt's own `worktrees` shape,
	// and what engine.CleanupWorktrees consumes.
	Worktrees map[string]string
}

// StepSpec is one step as `engine startup` reports it.
type StepSpec struct {
	ID       string
	Expanded string
}

// DispatchRequest is one step agent invocation.
type DispatchRequest struct {
	Claim    ClaimInfo
	Step     StepSpec
	Index    int
	Total    int
	Role     string
	ReadOnly bool
	Channel  Channel
	Prompt   string
	LogPath  string
	WorkDir  string
	// NoAgentSelector asks the dispatcher to build the command WITHOUT the harness's agent
	// selector while keeping the capability flags. It is set only on the retry after a harness
	// refused the selector (drain.IsAgentNotFound) — see dispatchWithFallback.
	NoAgentSelector bool
}

// Binding projects a DispatchRequest onto what BuildStepInvocation needs. It exists so the
// dispatcher seam in internal/cli cannot pick a different subset of these fields than the loop
// meant to send: Role and ReadOnly used to be on this struct with NOBODY reading them.
func (r DispatchRequest) Binding() Binding {
	return Binding{Role: r.Role, ReadOnly: r.ReadOnly, NoAgentSelector: r.NoAgentSelector}
}

// DispatchResult is what came back from the harness process.
type DispatchResult struct {
	// Output is the agent's combined stdout+stderr, also written to LogPath by the dispatcher.
	Output string
	// ExitErr is non-nil when the process exited non-zero or could not be started.
	ExitErr error
}

// Notification is the aihub-side half of the notification design (aihub#640
// `notification_three_layers` layer ②). Two work items are named because the ruling asks for
// both halves: a note on MY blocked work item, and — the part it calls "更漂亮的一手" — a note on
// the BLOCKER's work item, so the person holding it learns somebody is waiting. In an unattended
// run that is the only channel that reaches another human at all.
type Notification struct {
	WorkItemID string
	BlockerID  string
	Note       string
}

// ErrLockTaken is what Runner.Claim must return (or wrap) when a claim loses a resource-lock
// race, so the loop can treat it as ordinary control flow rather than an error.
//
// aihub#640 `two_kinds_of_blocking` is emphatic about this branch: dependency blocking keeps a
// work item OUT of the ready queue entirely, so drain never sees it, while lock blocking happens
// to a work item that is queued and looks perfectly runnable — "predict 是快照，claim 才是真相，
// 一定会撞上". The prescribed policy is SKIP, never retry: "等待是死锁的温床".
var ErrLockTaken = errors.New("drain: resource lock held by another attempt")

// ErrAttemptPaused is what any hub call must return (or wrap) when the attempt has been paused
// out from under the loop.
var ErrAttemptPaused = errors.New("drain: attempt is paused")

// ErrAuth is what a dispatch must return (or wrap) when the harness could not authenticate, so
// the run can fall to the next channel candidate instead of blaming the work item.
var ErrAuth = errors.New("drain: harness authentication failed")

// ErrNotSupported is what Claim returns when the scheduler STRUCTURALLY cannot do this, as
// opposed to having lost a race. It exists so such a run is not reported as IDLE.
//
// IDLE means "come back later and it will work"; a missing capability never gets better by
// waiting, and reporting it as the one terminal state that deliberately notifies nobody is the
// most misleading answer available. A run that hits this ends FAILED, which is what "a human has
// to act" is spelled as in the exit code.
var ErrNotSupported = errors.New("drain: this scheduler cannot execute work items yet")

// Runner is the scheduler. Every side effect is an injected function (package doc, "Design
// rule"), so the whole loop below runs in tests against fakes.
type Runner struct {
	Project string
	Scope   Scope
	Budget  Budget
	RunDir  string

	// Channels is the preference-ordered candidate list. Index 0 is used until it fails
	// authentication, at which point the run falls to the next healthy one.
	Channels []Channel

	Now     func() time.Time
	NewULID func() string

	// --- server side -----------------------------------------------------------------------

	// Executable returns the in-scope work items that are ready to claim right now. It must
	// use the server's own ready predicate rather than re-deriving dependency readiness.
	Executable func(ctx context.Context) ([]Candidate, error)
	// AllInScope returns every non-terminal in-scope work item, ready or not. It feeds the
	// proliferation baseline, so it must include blocked work items.
	AllInScope func(ctx context.Context) ([]Candidate, error)
	// ObserveQueue classifies what remains in scope into the QueueState buckets, including
	// resolving whether each blocked work item's blockers are inside or outside the scope.
	ObserveQueue func(ctx context.Context) (QueueState, error)
	// Claim claims a work item. It must return ErrLockTaken on 409 CONFLICT_LOCK_TAKEN, with
	// the holder in the returned Blocker when the server named one.
	Claim func(ctx context.Context, wiID, idempotencyKey string) (*ClaimInfo, *Blocker, error)
	// UpdateStep makes one pf_update_step call, exactly as planned by engine.PlanStepBracket.
	UpdateStep func(ctx context.Context, wiID string, call engine.StepCall) error
	// CompleteAttempt terminates the attempt: status "wrapped" or "failed".
	CompleteAttempt func(ctx context.Context, wiID, status, note string) error
	// Notify writes a note onto a work item timeline (layer ②).
	Notify func(ctx context.Context, n Notification) error

	// --- execution side --------------------------------------------------------------------

	// Startup runs `engine startup` for a claimed work item and returns its steps.
	Startup func(ctx context.Context, c ClaimInfo) ([]StepSpec, error)
	// ResolveRole runs the three-tier role fallback for a step id. It exists as an injected
	// function only so tests need not embed the role catalog; the production implementation
	// calls engine.ResolveRole against the real catalog and adds nothing.
	ResolveRole func(stepID string) (role string, readOnly bool, err error)
	// Dispatch runs one step agent and returns its output.
	Dispatch func(ctx context.Context, req DispatchRequest) (DispatchResult, error)
	// Cleanup removes the work item's worktrees after wrap (engine.CleanupWorktrees).
	Cleanup func(ctx context.Context, c ClaimInfo) error
	// AttemptPaused asks the server whether this work item's attempt is actually paused. It is
	// the AUTHORITY for that question; DetectPause is only a cheap pre-filter over the step
	// agent's prose. Optional: when nil, DetectPause's verdict is taken as final.
	AttemptPaused func(ctx context.Context, wiID string) (bool, error)

	// --- observation -----------------------------------------------------------------------

	// Publish is called whenever the snapshot changes.
	Publish func(*Snapshot)
	// Logf reports progress on stderr.
	Logf func(format string, args ...any)

	mu       sync.Mutex
	snapshot *Snapshot
	chanIdx  int
	// liveAttempts holds the attempt ids THIS RUN currently has in flight, so a lock refusal can
	// tell "somebody else holds it" from "one of my own workers holds it". Registered on a
	// successful claim and removed when executeWorkItem returns, whatever the outcome — see
	// heldByThisRun.
	liveAttempts map[string]bool
	// liveCandidates holds the ids AND slugs of the work items currently being executed,
	// registered BEFORE the claim goes out. It is the half of heldByThisRun that is not subject
	// to a race with the server's own lock acquisition; see that function.
	liveCandidates map[string]bool
}

// registerCandidate records a work item this run is about to claim, by BOTH id and slug, because
// the 409's `conflict_with.work_item_slug` is a slug while the candidate list is keyed on ids and
// either may be what a caller compares. Returns a function that forgets it.
func (r *Runner) registerCandidate(c Candidate) func() {
	keys := make([]string, 0, 2)
	for _, k := range []string{c.ID, c.Slug} {
		if k != "" {
			keys = append(keys, k)
		}
	}
	if len(keys) == 0 {
		return func() {}
	}
	r.mu.Lock()
	if r.liveCandidates == nil {
		r.liveCandidates = map[string]bool{}
	}
	for _, k := range keys {
		r.liveCandidates[k] = true
	}
	r.mu.Unlock()
	return func() {
		r.mu.Lock()
		for _, k := range keys {
			delete(r.liveCandidates, k)
		}
		r.mu.Unlock()
	}
}

// registerAttempt records an attempt this run just claimed. Returns a function that forgets it.
//
// Deregistration on EVERY exit, including paused and cancelled, is load-bearing rather than
// tidy. A paused or cancelled attempt is left claimed and still holds its locks, so it looks
// exactly like an own attempt — but nothing in this run will ever release it, which is the
// condition under which "skip for the whole run" is the correct policy. Keeping it registered
// would make the lock-blocked work item retry-eligible every round forever: a busy-wait wearing a
// round loop's clothing, which is precisely what aihub#640 `two_kinds_of_blocking` warns about
// ("等待是死锁的温床"). With this deregistration the set is empty at every round boundary, so the
// only retry-eligible refusal is one raised by a SIBLING WORKER IN THE SAME ROUND — and that
// sibling, having claimed, produces a non-lock-blocked outcome, so every retried round also made
// progress.
func (r *Runner) registerAttempt(id string) func() {
	if id == "" {
		return func() {}
	}
	r.mu.Lock()
	if r.liveAttempts == nil {
		r.liveAttempts = map[string]bool{}
	}
	r.liveAttempts[id] = true
	r.mu.Unlock()
	return func() {
		r.mu.Lock()
		delete(r.liveAttempts, id)
		r.mu.Unlock()
	}
}

// heldByThisRun reports whether a lock refusal names something this run is currently executing —
// and so something that will release the lock without anybody's help.
//
// It consults TWO registries, and the second one is not redundant: it closes a race the first
// cannot.
//
//	liveAttempts    the holder's attempt id, known only AFTER that worker's claim returned
//	liveCandidates  the holder's work item, registered BEFORE this worker's claim goes out
//
// The race: the server takes worker A's lock and refuses worker B inside the same request window,
// so B can be back with a 409 naming A's attempt before A's claim has returned and registered it.
// Keyed on the attempt alone, that refusal reads as foreign and the work item is written off for
// the whole run — non-deterministically, which is the worst way for this to be wrong. The work
// item id and slug are known from the candidate list, so registering those at the top of
// executeWorkItem removes the window entirely; `conflict_with.work_item_slug` is the matching
// field, and it travels in the same payload as the attempt id.
//
// A nil Blocker, or one the server could not name at all, answers false: the holder lookup is
// best-effort server-side (internal/domain/resource_events.go says a refusal reported without a
// name is still a refusal), and "I could not tell" must fall to the conservative policy, which is
// the whole-run skip.
func (r *Runner) heldByThisRun(b *Blocker) bool {
	if b == nil {
		return false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if b.AttemptID != "" && r.liveAttempts[b.AttemptID] {
		return true
	}
	return b.WorkItem != "" && r.liveCandidates[b.WorkItem]
}

// RunReport is what a completed drain run has to say for itself.
type RunReport struct {
	Terminal   Terminal
	StopReason StopReason
	Totals     Totals
	Queue      QueueState
	Outcomes   []Outcome
}

// Run executes rounds until one of the stop conditions fires, then classifies the result.
//
// The round structure is not decoration. Each iteration freezes its candidate set BEFORE
// executing anything (FreezeRound), so work items created by this round's own execution are
// deferred to the next one and the divergence detector gets a chance to see them. That is the
// proliferation budget from `convergence_divergence_detector`, and it is what bounds a loop
// whose individual steps are all perfectly terminating.
func (r *Runner) Run(ctx context.Context) (RunReport, error) {
	r.init()

	// The wall-clock budget (aihub#640 `convergence_divergence_detector` asks for "跑满 N 个 /
	// T 时间"; only the first half existed — aihub#678 ④). It is a context DEADLINE rather than a
	// check at the top of each round, and the difference is the whole value of the flag: a round
	// can hold a work item whose step is allowed two hours, so a round-boundary test would leave
	// the real bound at `--max-duration + stepTimeout × rounds` and a detached run would still
	// have no honest ceiling. A deadline cuts the in-flight step exactly the way `--stop` does.
	//
	// The consequence is deliberate and is NOT hidden: a run cut off mid-step leaves that work
	// item claimed and holding locks, so its outcome is ResultCancelled, which NeedsHuman, so the
	// run ends FAILED rather than IDLE. That is the same disposition Ctrl-C has, reported the
	// same way.
	deadlineHit := func() bool { return false }
	if r.Budget.MaxDuration > 0 {
		// ⚠️ `parent` is captured BEFORE ctx is reassigned, and that is not style. A closure over
		// `ctx` captures the VARIABLE, so reading it after the reassignment below would ask the
		// budget context whether the budget context had expired — always yes, making every
		// caller-side timeout report as max_duration. Whose deadline fired is exactly what this
		// predicate exists to answer.
		parent := ctx
		budgetCtx, cancel := context.WithDeadline(ctx, r.Now().Add(r.Budget.MaxDuration))
		defer cancel()
		deadlineHit = func() bool {
			return errors.Is(budgetCtx.Err(), context.DeadlineExceeded) &&
				!errors.Is(parent.Err(), context.DeadlineExceeded)
		}
		ctx = budgetCtx
	}

	var (
		outcomes  []Outcome
		anyFailed bool
		stop      = StopQueueDrained
		round     int
		executed  int
	)
	// cancelStop names the ending for a cancelled context: this run's own budget, or somebody
	// else's cancellation. Called at each of the three places the loop can break on one.
	cancelStop := func() StopReason {
		if deadlineHit() {
			return StopMaxDuration
		}
		return StopCancelled
	}

	// skipped holds work items this RUN has already declined to execute — ones whose claim lost
	// a lock race, and ones whose claim failed for any other reason.
	//
	// This set is what makes "skip, never retry" (aihub#640 `two_kinds_of_blocking`) actually
	// mean skip. A lock-blocked work item is never claimed, so its status stays `queued` and
	// the server keeps returning it as executable; without this set the next round freezes it
	// again, claims again, is refused again, and the run never terminates — a busy-wait wearing
	// a round loop's clothing, and precisely the deadlock temptation the ruling warns about
	// ("等待是死锁的温床").
	//
	// ⚠️ Holding the skip FOR THE WHOLE RUN rests on a premise that used to be stated as fact and
	// is only usually true: "the holder is another live attempt, and nothing this run does will
	// end it". At --max-parallel>=2 the holder can be one of this run's OWN concurrent claims,
	// which ends within the round — see Outcome.RetryNextRound and Blocker.AttemptID. Such a work
	// item is re-offered next round instead of being written off. Nothing waits on a lock either
	// way; the deadlock argument is untouched.
	skipped := map[string]bool{}

	for {
		if err := ctx.Err(); err != nil {
			stop = cancelStop()
			break
		}
		if r.Budget.MaxRounds > 0 && round >= r.Budget.MaxRounds {
			stop = StopMaxRounds
			break
		}

		// The proliferation baseline is every in-scope work item, not just the runnable
		// ones: a follow-up filed as `blocked` is still proliferation, and counting only
		// ready work items would blind the detector to the very shape it exists to catch —
		// a work item that spawns a chain of blocked successors.
		before, err := r.AllInScope(ctx)
		if err != nil {
			if r.bailOut(ctx, "list in-scope work items", err) {
				stop = cancelStop()
				break
			}
			return RunReport{}, fmt.Errorf("list in-scope work items: %w", err)
		}
		beforeIDs := IDSet(before)

		candidates, err := r.Executable(ctx)
		if err != nil {
			if r.bailOut(ctx, "list executable work items", err) {
				stop = cancelStop()
				break
			}
			return RunReport{}, fmt.Errorf("list executable work items: %w", err)
		}

		// Count EXECUTED work items, not every outcome. Budget.MaxWorkItems is documented as
		// "how many work items are executed", and a lock-blocked or claim-failed work item
		// was never executed — it was skipped in milliseconds. Charging the budget for those
		// let two contended work items exhaust `--max-work-items=2` having run nothing at
		// all, and then report stop_reason=max_work_items as though the cap had done its job.
		budgetLeft := r.Budget.RemainingWorkItems(executed)
		if budgetLeft == 0 {
			stop = StopMaxWorkItems
			break
		}
		frozen, _ := FreezeRound(filterSkipped(candidates, skipped), budgetLeft)
		if len(frozen) == 0 {
			// "Drained" and "untouchable" are different endings and must not share a word.
			// A run that skipped every candidate on a lock race leaves the work exactly where
			// it was; calling that queue_drained hides the one fact an operator needs, which
			// is that the work is still there and somebody else is holding it.
			stop = StopQueueDrained
			if executed == 0 && len(skipped) > 0 {
				stop = StopNothingClaimable
			}
			break
		}

		round++
		r.update(func(s *Snapshot) { s.Round = round; s.Totals.Rounds = round })
		r.Logf("drain: round %d: %d executable work item(s), parallelism %d",
			round, len(frozen), r.Budget.Parallelism())

		roundOutcomes := r.executeRound(ctx, frozen)
		outcomes = append(outcomes, roundOutcomes...)

		tally := RoundTally{Executable: len(frozen)}
		for _, o := range roundOutcomes {
			switch o.Result {
			case ResultWrapped:
				tally.Completed++
			case ResultFailed:
				tally.Failed++
			case ResultLockBlocked:
				tally.LockBlocked++
				if !o.RetryNextRound {
					skipped[o.Candidate.ID] = true
				}
			case ResultPaused:
				tally.Paused++
			case ResultCancelled:
				tally.Cancelled++
			case ResultClaimFailed:
				skipped[o.Candidate.ID] = true
			}
			if o.Result.Executed() {
				executed++
			}
			if o.Result.NeedsHuman() {
				anyFailed = true
			}
			r.update(func(s *Snapshot) { s.Totals.Add(o.Result); s.Recent = append(s.Recent, o) })
		}

		// Re-list to count what came into existence while the round was running.
		after, err := r.AllInScope(ctx)
		if err != nil {
			if r.bailOut(ctx, "re-list in-scope work items", err) {
				stop = cancelStop()
				break
			}
			return RunReport{}, fmt.Errorf("re-list in-scope work items: %w", err)
		}
		tally.Created = CountCreatedDuringRound(beforeIDs, after)
		r.update(func(s *Snapshot) { s.Totals.Created += tally.Created })

		if q, qerr := r.ObserveQueue(ctx); qerr == nil {
			r.update(func(s *Snapshot) { s.Queue = q })
		}

		if tally.Diverging() {
			// Loud on purpose. The exit code cannot carry this — the owner ruling fixes the
			// exit code to the four terminal states — so stderr and the snapshot are the
			// only channels left, and a quiet line here would mean the single stop
			// condition that most needs a person reads exactly like the routine one.
			r.Logf("drain: STOPPING, DIVERGENCE DETECTED: round %d created %d work item(s) and completed %d. "+
				"The queue is growing, not shrinking; a human should look before draining again.",
				round, tally.Created, tally.Completed)
			stop = StopDivergence
			break
		}

		if tally.Attempted() == 0 {
			// Nothing in the frozen set could even be attempted. Another round would freeze
			// the same set and do the same nothing.
			//
			// This is NOT "queue_drained": the queue was not drained, it was untouchable —
			// every candidate was held by another attempt or vanished between listing and
			// claiming. Reporting the routine ending for it hides the one fact an operator
			// needs, which is that the work is still there and somebody else has it.
			stop = StopNothingClaimable
			break
		}
	}

	queue, qerr := r.ObserveQueue(ctx)
	if qerr != nil {
		r.Logf("drain: warning: final queue observation failed (%v); "+
			"classifying from the run's own outcomes only", qerr)
		// A failed observation must not be reported as an empty queue, which would say
		// COMPLETED — the most reassuring of the four states — on the strength of a
		// question that was never answered. Assume work remains.
		queue = QueueState{Executable: 1}
	}

	terminal := Classify(anyFailed, queue)
	r.finish(terminal, stop, queue)

	if terminal == TerminalBlockedExternal {
		r.notifyExternalBlocks(ctx, queue.ExternallyBlocked)
	}
	// Reported on EVERY ending, not only the one it can cause. A run that also failed something
	// ends FAILED, and these work items would then be named nowhere at all — the report prints a
	// count, and a count of work items nobody can execute is not something anyone can act on.
	r.reportHumanSessionNeeded(queue.NeedsHumanSession)

	var totals Totals
	r.mu.Lock()
	totals = r.snapshot.Totals
	r.mu.Unlock()

	return RunReport{
		Terminal:   terminal,
		StopReason: stop,
		Totals:     totals,
		Queue:      queue,
		Outcomes:   outcomes,
	}, nil
}

// executeRound runs the frozen candidate set with bounded concurrency, IN ORDER.
//
// The ordering is the reason this is a worker pool over an index channel rather than the more
// obvious "spawn a goroutine per candidate and let them queue on a semaphore". Both bound
// concurrency correctly, but the obvious version bounds only concurrency: which goroutine
// reaches the semaphore first is the Go scheduler's business, so with --max-parallel=1 the
// claims come out in an arbitrary order, and OrderCandidates' careful priority ordering decides
// nothing at all. That is not a test artifact — it means an urgent work item can sit behind a
// low-priority one on a serial run — and it is invisible without an explicit order assertion,
// which is how it survived until TestRun_DispatchOrderRespectsPriorityAndDependencyChains ran.
//
// Feeding indices down one channel makes the pool take candidates in slice order. Work items
// still FINISH in whatever order they finish; what is guaranteed is the order they are STARTED,
// which is the half scheduling policy controls.
func (r *Runner) executeRound(ctx context.Context, frozen []Candidate) []Outcome {
	// Deliberately NOT clamped to len(frozen). Spawning Parallelism() workers for a shorter
	// round costs a few goroutines that immediately observe a closed channel and exit, and it
	// keeps this function free of a bound that nothing reports: aihub#314's convention is that
	// a clamp discloses itself, and a clamp with no observable effect is one nobody can
	// usefully be told about (internal/citest/clampdisclosure).
	n := r.Budget.Parallelism()
	out := make([]Outcome, len(frozen))
	idx := make(chan int)
	var wg sync.WaitGroup

	for w := 0; w < n; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range idx {
				c := frozen[i]
				if ctx.Err() != nil {
					out[i] = Outcome{Candidate: c, Result: ResultClaimFailed, Err: "cancelled before claim"}
					continue
				}
				out[i] = r.executeWorkItem(ctx, c)
			}
		}()
	}
	for i := range frozen {
		idx <- i
	}
	close(idx)
	wg.Wait()

	res := make([]Outcome, 0, len(out))
	for _, o := range out {
		if o.Candidate.ID != "" {
			res = append(res, o)
		}
	}
	return res
}

// executeWorkItem claims one work item and runs its steps to a terminal attempt state.
//
// The step loop below is deliberately the SAME shape as engine.native.md's `## Execute (rhs=false,
// auto mode)` block, call for call: dispatch the step agent, break on a pause without completing
// the attempt (§0e), parse the review marker on review steps and take the §0c path on FAIL, then
// plan the bracket and make every pf_update_step call it printed, in order.
//
// It calls internal/engine's functions in-process rather than shelling out to `polyforge engine
// <verb>`. That is not a divergence from the workflow_identity_constraint but the strictest
// available way to satisfy it: the constraint is about EXECUTION LOGIC, and there is exactly one
// copy of that logic — the functions in internal/engine — which both A and B/C reach. Going
// in-process also removes a hazard B/C has to live with and A does not: every value B/C passes
// crosses a shell, and aihub#657 shipped, then had to fix, a gate that missed an unquoted
// --artifact-summary truncating at the first space with exit 0. A summary passed as a Go string
// cannot be truncated by a shell that is not there.
func (r *Runner) executeWorkItem(ctx context.Context, c Candidate) Outcome {
	res := Outcome{Candidate: c, LogDir: WILogDir(r.RunDir, c.Slug)}

	// BEFORE the claim, not after: a sibling worker's lock refusal can come back naming this
	// work item while this claim is still in flight. See heldByThisRun.
	defer r.registerCandidate(c)()

	claim, blocker, err := r.Claim(ctx, c.ID, r.NewULID())
	switch {
	case errors.Is(err, ErrLockTaken):
		// SKIP, never retry (`two_kinds_of_blocking`). Waiting on a lock inside a scheduler
		// that holds locks of its own is how a deadlock is built.
		//
		// ⚠️ "Skip" means skip for the rest of the RUN, and that is right only while the rule's
		// own stated reason holds: "the holder is another live attempt, and nothing this run
		// does will end it". When the holder is one of this run's own in-flight claims the
		// reason is simply false — the sibling will release the lock within the round — so the
		// work item is re-offered next round rather than written off. It is still never
		// retried in place; nothing waits on a lock here.
		res.Result = ResultLockBlocked
		res.Blocker = blocker
		res.RetryNextRound = r.heldByThisRun(blocker)
		if res.RetryNextRound {
			r.Logf("drain: %s deferred to the next round: the resource lock is held by this run's "+
				"own attempt %s, which will release it", c.Slug, blocker.AttemptID)
		} else {
			r.Logf("drain: %s skipped: a resource lock is held by another attempt", c.Slug)
		}
		if !res.RetryNextRound && blocker != nil && blocker.WorkItem != "" && r.Notify != nil {
			// Layer ②'s second half: tell the holder somebody is waiting.
			_ = r.Notify(ctx, Notification{
				WorkItemID: blocker.WorkItem,
				BlockerID:  c.ID,
				Note: fmt.Sprintf("polyforge drain skipped %s: it needs a resource this work item holds (%s).",
					c.Slug, blocker.Resource),
			})
		}
		return res
	case errors.Is(err, ErrNotSupported):
		// NOT ResultClaimFailed: that is the "somebody beat me to it" outcome, which is
		// transient and correctly ends the run IDLE. This is a capability that does not
		// exist, so waiting accomplishes nothing and a person has to act.
		r.Logf("drain: %s cannot be executed: %v", c.Slug, err)
		res.Result = ResultFailed
		res.Err = err.Error()
		return res
	case err != nil:
		r.Logf("drain: %s claim failed: %v", c.Slug, err)
		res.Result = ResultClaimFailed
		res.Err = err.Error()
		return res
	}

	// Every early return below used to leave this work item in Snapshot.Active for the rest of
	// the run, so `polyforge watch` showed it "in flight" with an age that only grew. A run that
	// failed forty work items displayed forty phantom ones on the single screen this feature
	// exists to make trustworthy. A defer covers all eight exits at once, which is the point:
	// the next person to add a return does not have to remember.
	defer r.clearActive(c.ID)
	// Same shape, for the same reason: every exit below must forget this attempt, so a sibling
	// worker's lock refusal can only be called "mine, and about to be released" while it really
	// is. See registerAttempt.
	defer r.registerAttempt(claim.AttemptID)()

	steps, err := r.Startup(ctx, *claim)
	if err != nil {
		res.Result = ResultFailed
		res.Err = fmt.Sprintf("engine startup: %v", err)
		r.failAttempt(ctx, c.ID, "engine startup: "+err.Error())
		return res
	}
	if len(steps) == 0 {
		res.Result = ResultFailed
		res.Err = "engine startup returned no steps"
		r.failAttempt(ctx, c.ID, "scenario template produced no steps")
		return res
	}

	saID := r.NewULID()
	// Open the first step, exactly as the B/C loop does before entering its for-loop.
	if err := r.UpdateStep(ctx, c.ID, engine.StepCall{
		Tool:          "pf_update_step",
		StepID:        steps[0].ID,
		Status:        "in_progress",
		StepAttemptID: saID,
	}); err != nil {
		if errors.Is(err, ErrAttemptPaused) {
			res.Result = ResultPaused
			return res
		}
		res.Result = ResultFailed
		res.Err = fmt.Sprintf("open first step: %v", err)
		r.failAttempt(ctx, c.ID, res.Err)
		return res
	}

	for i, step := range steps {
		if ctx.Err() != nil {
			// NOT ResultPaused. Both leave the attempt claimed and neither completes it, but
			// a pause was somebody's deliberate hand-off and this is an attempt abandoned
			// because the scheduler was stopped. Reporting "paused=1" for it tells the
			// operator a person decided something when nobody did.
			res.Result = ResultCancelled
			res.Err = "cancelled mid-run; the attempt is left claimed so it can be resumed"
			return res
		}

		role, readOnly, rerr := r.ResolveRole(step.ID)
		if rerr != nil {
			res.Result = ResultFailed
			res.Err = fmt.Sprintf("resolve role for step %s: %v", step.ID, rerr)
			r.failAttempt(ctx, c.ID, res.Err)
			return res
		}

		ch, _ := r.currentChannel()
		r.setActive(ActiveWI{
			Candidate: c, StepID: step.ID, StepIndex: i + 1, StepCount: len(steps),
			Role: role, Channel: ch, StepStarted: r.Now().UTC().Format(time.RFC3339),
			LogDir: res.LogDir,
		})

		out, derr := r.dispatchWithFallback(ctx, DispatchRequest{
			Claim: *claim, Step: step, Index: i + 1, Total: len(steps),
			Role: role, ReadOnly: readOnly,
			Prompt:  StepAgentPrompt(claim.WorkItemID, step.ID, step.Expanded),
			LogPath: StepLogPath(r.RunDir, c.Slug, i+1, step.ID),
			WorkDir: claim.WorktreeRoot,
		})
		if derr != nil {
			res.Result = ResultFailed
			res.Err = fmt.Sprintf("step %s: %v", step.ID, derr)
			r.failAttempt(ctx, c.ID, res.Err)
			return res
		}

		if DetectPause(out.Output) && r.confirmPaused(ctx, c, step.ID) {
			// engine-native-details.md §0e: stop the loop, no retry, and do NOT call
			// pf_complete_attempt. The pause already put the work item in the state its
			// author wanted; completing the attempt here would overwrite that.
			r.Logf("drain: %s step %s paused the attempt; moving on", c.Slug, step.ID)
			res.Result = ResultPaused
			res.Steps = i
			return res
		}

		if engine.IsReviewStep(step.ID) {
			switch engine.ParseReviewResult(out.Output) {
			case engine.ReviewFail:
				// §0c, both calls, in that order.
				for _, call := range engine.PlanStepBracket(engine.BracketInput{
					StepID: step.ID, StepAttemptID: saID, Status: "failed", ErrorType: "review_fail",
				}) {
					if uerr := r.UpdateStep(ctx, c.ID, call); uerr != nil {
						// §0c is explicit that either call alone leaves a broken state, so a
						// failure here is not cosmetic: the step would show in_progress
						// forever while the report says only "review_fail".
						r.Logf("drain: %s: could not file the failed step %s: %v",
							c.Slug, step.ID, uerr)
					}
				}
				r.failAttempt(ctx, c.ID, "review_fail at step "+step.ID)
				r.Logf("drain: %s review FAIL at step %s", c.Slug, step.ID)
				res.Result = ResultFailed
				res.Steps = i
				res.Err = "review_fail at step " + step.ID
				return res
			case engine.ReviewWarn:
				r.Logf("drain: %s review WARN at step %s (continuing)", c.Slug, step.ID)
			}
		}

		var nextID, nextSA string
		if i+1 < len(steps) {
			nextID = steps[i+1].ID
			nextSA = r.NewULID()
		}
		for _, call := range engine.PlanStepBracket(engine.BracketInput{
			StepID:            step.ID,
			StepAttemptID:     saID,
			Status:            "completed",
			ArtifactSummary:   SummaryLine(out.Output),
			NextStepID:        nextID,
			NextStepAttemptID: nextSA,
			// Hardcoded true, and drain does NOT negotiate this: `polyforge drain` shipped
			// after next_step did, so any server new enough to be drained publishes it.
			// engine.PlanStepBracket still owns the fused-vs-degraded choice; this is the
			// caller stating a fact about its own minimum server, not probing for one.
			SupportsNextStep: true,
		}) {
			if err := r.UpdateStep(ctx, c.ID, call); err != nil {
				if errors.Is(err, ErrAttemptPaused) {
					res.Result = ResultPaused
					res.Steps = i
					return res
				}
				res.Result = ResultFailed
				res.Err = fmt.Sprintf("update step %s: %v", step.ID, err)
				r.failAttempt(ctx, c.ID, res.Err)
				return res
			}
		}
		saID = nextSA
		res.Steps = i + 1
	}

	if err := r.CompleteAttempt(ctx, c.ID, "wrapped", "drained by polyforge drain"); err != nil {
		res.Result = ResultFailed
		res.Err = fmt.Sprintf("wrap: %v", err)
		return res
	}
	if r.Cleanup != nil {
		if err := r.Cleanup(ctx, *claim); err != nil {
			// Cleanup is best-effort, exactly as engine cleanup-worktrees is: losing a
			// worktree directory is a disk-space problem, and reporting the work item as
			// failed because of one would be a much worse lie than a leftover directory.
			r.Logf("drain: %s wrapped, but worktree cleanup reported: %v", c.Slug, err)
		}
	}
	r.Logf("drain: %s wrapped (%d steps)", c.Slug, res.Steps)
	res.Result = ResultWrapped
	return res
}

// failAttempt terminates the attempt as failed and REPORTS a bookkeeping failure rather than
// discarding it.
//
// Every one of these calls used to be `_ = r.CompleteAttempt(...)`. The error being dropped is
// not a detail: if it fails, the work item is left `running` with nobody executing it, holding
// its locks, and the run's report says only why the STEP failed. A person reading that report
// would have no way to learn that the bookkeeping itself did not land, and the next drain would
// quietly skip the work item as already claimed.
func (r *Runner) failAttempt(ctx context.Context, wiID, reason string) {
	if err := r.CompleteAttempt(ctx, wiID, "failed", "failed reason: "+reason); err != nil {
		r.Logf("drain: %s: the attempt could not be marked failed (%v); it is left running and "+
			"still holds its locks. Original failure: %s", wiID, err, reason)
	}
}

// confirmPaused checks with the server before believing DetectPause.
//
// DetectPause is an unanchored substring scan over the step agent's whole combined output, and
// on its own that is dangerous rather than merely imprecise. Drain's first customer is aihub's
// own work items, and this very repository contains "ATTEMPT_PAUSED" in fourteen Go files: a
// perfectly healthy `code_change` step that greps or runs tests over them trips the detector. On
// a false positive the loop returns without calling pf_complete_attempt — correct for a REAL
// pause — so the attempt is left `running` with its locks held by a process that has exited, and
// recovering it needs pf_force_takeover.
//
// So the scan stays as the cheap pre-filter (it costs nothing and is right almost always) and
// the server settles it. When the check is unavailable or fails, the answer is YES: not
// completing a live attempt is recoverable, while completing an attempt somebody deliberately
// paused destroys the hand-off they asked for.
func (r *Runner) confirmPaused(ctx context.Context, c Candidate, stepID string) bool {
	if r.AttemptPaused == nil {
		return true
	}
	paused, err := r.AttemptPaused(ctx, c.ID)
	if err != nil {
		r.Logf("drain: %s: step %s looked like a pause and the server could not confirm it (%v); "+
			"treating it as paused, which leaves the attempt claimed", c.Slug, stepID, err)
		return true
	}
	if !paused {
		r.Logf("drain: %s: step %s mentioned a pause but the attempt is not paused; continuing",
			c.Slug, stepID)
	}
	return paused
}

// dispatchWithFallback runs one step, falling to the next channel candidate on an authentication
// failure — the run-time half of `three_ops_problems` ② ("运行中 401 则靠候选列表自动落到下一条通
// 道"). It never falls through for an ordinary step failure: only a credential problem is the
// channel's fault rather than the work's.
//
// Two more channel-level verdicts join it, both from aihub#678 ①, and both share that property —
// they are facts about the CHANNEL, never about the work item:
//
//   - StepChannelUnsuitable: this harness cannot express the step's read-only capability at all
//     (opencode today). It is demoted for the rest of the run rather than for this step alone,
//     because a read-only role appears in nearly every work item's step graph: a channel that
//     cannot run one is not a channel this run can use, and per-step re-selection would
//     re-discover the same fact on every review step while making the auth demotion's index
//     bookkeeping ambiguous.
//   - IsAgentNotFound: the harness REFUSED the agent selector. That is not the channel's fault
//     and not the work's — it is a stale plugin install — so it is retried once on the same
//     channel with the selector suppressed and the capability flags intact, loudly.
func (r *Runner) dispatchWithFallback(ctx context.Context, req DispatchRequest) (DispatchResult, error) {
	for {
		ch, idx := r.currentChannel()
		req.Channel = ch
		out, err := r.Dispatch(ctx, req)
		verdict := ClassifyStepDispatch(out, err)
		switch verdict {
		case StepOK:
			if AgentFellBackToDefault(out.Output) {
				if req.ReadOnly {
					// 🔴 The capability was NOT applied. On the harness measured to do this the
					// agent file is the only carrier of read_only (render_opencode.go's
					// `permission: edit: deny`), so a silent fall back to the default agent means
					// this read-only step just ran WRITE-CAPABLE — the exact defect aihub#678 ①
					// is about, and the reason this cannot be a log line. Failing the step is the
					// safe direction: the work is redoable, an unreviewed edit made by a reviewer
					// is not.
					return out, fmt.Errorf("step agent for read-only role %q was not applied: %s "+
						"could not find its agent and silently ran the DEFAULT (write-capable) "+
						"agent instead. Generate this harness's agent files with "+
						"`polyforge roles generate %s`; see %s",
						req.Role, ch.Harness, ch.Harness, req.LogPath)
				}
				r.Logf("drain: %s ignored the agent selector for role %q and ran its DEFAULT agent; "+
					"the step ran with the wrong model tier and prompt. Generate this harness's "+
					"agent files (`polyforge roles generate`) to fix it.", ch.Harness, req.Role)
			}
			return out, nil
		case StepAuthFailure:
			if !r.demoteChannel(idx) {
				return out, fmt.Errorf("%w: every channel candidate is unauthenticated (last: %s)",
					ErrAuth, ch)
			}
			next, _ := r.currentChannel()
			r.Logf("drain: channel %s failed authentication; falling back to %s", ch, next)
			continue
		case StepChannelUnsuitable:
			if !r.demoteChannel(idx) {
				return out, fmt.Errorf("%w: no channel candidate can run a read-only role "+
					"(last: %s). Refusing to run it write-capable: %v", ErrNoReadOnlyCapability, ch, err)
			}
			next, _ := r.currentChannel()
			r.Logf("drain: channel %s cannot express the read-only role %q, so it would run the step "+
				"WRITE-CAPABLE; falling back to %s", ch, req.Role, next)
			continue
		case StepAgentSelectorRefused:
			if req.NoAgentSelector {
				// Already retried; the refusal is coming from somewhere else.
				return out, fmt.Errorf("step agent failed: %v; see %s", err, req.LogPath)
			}
			r.Logf("drain: %s does not have an agent for role %q (stale plugin install?); retrying "+
				"WITHOUT the agent selector. The capability flags still apply, but the role's model "+
				"tier and prompt do NOT.", ch.Harness, req.Role)
			req.NoAgentSelector = true
			continue
		case StepSilentRefusal:
			// The measured `claude -p` default-permission shape: exit 0, no work done. Never
			// let this be reported as a completed step.
			return out, fmt.Errorf("step agent exited 0 but produced no usable output "+
				"(silent refusal or empty reply); see %s", req.LogPath)
		default:
			return out, fmt.Errorf("step agent failed: %v; see %s", err, req.LogPath)
		}
	}
}

// StepVerdict classifies a step dispatch.
type StepVerdict int

const (
	// StepOK: the agent ran and said something.
	StepOK StepVerdict = iota
	// StepFailed: the process exited non-zero for a reason that is not authentication.
	StepFailed
	// StepAuthFailure: the process failed because it could not authenticate.
	StepAuthFailure
	// StepSilentRefusal: the process exited ZERO and produced nothing usable.
	StepSilentRefusal
	// StepChannelUnsuitable: the command could not even be BUILT, because this harness cannot
	// express the step's read-only capability (drain.ErrNoReadOnlyCapability). Nothing ran, so
	// this is not a step failure; it is the channel being wrong for this work.
	StepChannelUnsuitable
	// StepAgentSelectorRefused: the harness rejected the agent selector by name — a stale
	// plugin install, not a failure of the step or of the credential. Retried once with the
	// selector suppressed; see dispatchWithFallback.
	StepAgentSelectorRefused
)

// ClassifyStepDispatch decides what a dispatch result means.
//
// The StepSilentRefusal branch is the one that earns this function's existence, and it is
// measured rather than defensive. aihub#640's design states the non-interactive hazard as "每个
// step 都会静默卡死等一个永远不来的人" — a hang. Measured on this machine 2026-09-14, Claude Code
// does not hang: with default permissions it declines the tool call, prints an explanation, and
// EXITS 0 with empty stderr, and with `--permission-mode dontAsk` it denies Bash the same way.
// A scheduler that read exit status alone would mark those steps completed and move on, and the
// work item would be wrapped having done nothing — the worst available outcome, because it is
// indistinguishable from success in every record the run leaves behind.
//
// Exit status is therefore necessary but not sufficient: a step must also have SAID something.
// That is a weak signal and is meant to be — it catches the empty and near-empty cases without
// pretending to judge whether the work was any good, which is the reviewer's job and is already
// modelled (review steps, REVIEW_RESULT markers). Its real defence is that a refusal produces
// far less output than a step, and zero output is never a successful step: §0b requires every
// agent to return a one-line summary, so silence violates the contract regardless of why.
func ClassifyStepDispatch(out DispatchResult, err error) StepVerdict {
	// Checked FIRST, and before the error branch below, because nothing ran: the command could
	// not be built, so there is no output to classify and no work item to blame. Reading this as
	// StepFailed would fail the work item for a property of the machine's harness list.
	if errors.Is(err, ErrNoReadOnlyCapability) || errors.Is(out.ExitErr, ErrNoReadOnlyCapability) {
		return StepChannelUnsuitable
	}
	if err != nil || out.ExitErr != nil {
		// Before the credential test: a refused agent selector is neither a credential problem
		// nor the work item's fault, and both of the other answers are wrong for it. Auth wins
		// where both match, because an unauthenticated harness cannot have resolved an agent.
		if !IsAuthFailure(out.Output) && IsAgentNotFound(out.Output) {
			return StepAgentSelectorRefused
		}
		// Both error slots are consulted on BOTH sides of this branch. They used to disagree:
		// the outer test looked at ExitErr and the inner one did not, so a result carrying
		// ExitErr=ErrAuth with a nil err classified as StepFailed — the opposite of what the
		// outer test had just detected — and the run would blame the work item for an expired
		// credential instead of falling to the next channel.
		if IsAuthFailure(out.Output) || errors.Is(err, ErrAuth) || errors.Is(out.ExitErr, ErrAuth) {
			return StepAuthFailure
		}
		return StepFailed
	}
	if strings.TrimSpace(out.Output) == "" {
		return StepSilentRefusal
	}
	return StepOK
}

// pauseMarkers are the phrases that mean the attempt was paused out from under the loop. The
// first is what a step agent reports after calling pf_pause_attempt; the second is the server's
// own refusal text, which engine-native-details.md §0e names verbatim as the other way this is
// discovered ("or a pf_* call is rejected 'attempt is paused'").
var pauseMarkers = []string{
	"pf_pause_attempt",
	"attempt is paused",
	"attempt_paused",
}

// DetectPause reports whether a step agent's output shows the attempt was paused.
func DetectPause(output string) bool {
	l := strings.ToLower(output)
	for _, m := range pauseMarkers {
		if strings.Contains(l, m) {
			return true
		}
	}
	return false
}

// SummaryLine extracts the one-line summary a step agent was told to return (§0b: "RETURN your
// one-line summary of this step in your output"), which the loop passes straight to
// pf_update_step(artifact_summary=...).
//
// The LAST non-blank line wins. Agents narrate first and conclude last, so the tail is the
// conclusion; taking the head would file the opening sentence of the reasoning as the summary.
func SummaryLine(output string) string {
	line := strings.TrimSpace(lastMeaningfulLine(output))
	const max = 500
	if len(line) > max {
		// Trim on a rune boundary: artifact summaries are routinely Chinese in this project,
		// and a byte-sliced multi-byte rune would file invalid UTF-8 to the server.
		r := []rune(line)
		if len(r) > max {
			r = r[:max]
		}
		line = string(r)
	}
	if line == "" {
		return "(step produced no summary line)"
	}
	return line
}

// StepAgentPrompt builds the step agent's prompt.
//
// The body is engine-native-details.md §0b's template VERBATIM — it is the same text the B/C
// loop hands its subagent, and that is required rather than tidy: the constraint says A must not
// contain execution logic B/C lacks, and instructions to the step agent ARE execution logic. The
// §0b paragraph about counting only "completed" entries in completed_steps, in particular, is
// what stops a resumed work item from redoing finished steps, and an A-mode paraphrase that
// dropped it would be a silent behavioural fork.
//
// What A does NOT reproduce is §0b's `Agent(subagent_type=...)` wrapper, because that is the
// DISPATCH MECHANISM rather than the instructions: B/C selects an agent inside its harness,
// where A selects a role and spawns a process. That is one of the three differences the
// constraint explicitly permits ("编排器是代码还是 LLM").
func StepAgentPrompt(wiID, stepID, expanded string) string {
	return fmt.Sprintf(`You are executing step %s of wi %s.

Call pf_get_step(work_item_id=%s) FIRST - it is the only authority for prior-step context.
In completed_steps, count only entries whose status is "completed" as done - a "failed" entry did
NOT finish (pausing an attempt files its in-progress step that way too), so redo that step_id
unless a later entry completes it. Read each done entry's artifact_summary. Never take step
progress from a file in the worktree; nothing writes one.

--- step instructions ---
%s
--- END ---

When done, RETURN your one-line summary of this step in your output; the loop passes it straight
to pf_update_step(artifact_summary=...). Do not write it to a file.
If there are learnings worth keeping, call pf_remember to store them in aihub.
`, stepID, wiID, wiID, expanded)
}

// --- snapshot plumbing -------------------------------------------------------------------------

func (r *Runner) init() {
	if r.Now == nil {
		r.Now = time.Now
	}
	if r.Logf == nil {
		r.Logf = func(string, ...any) {}
	}
	if r.Publish == nil {
		r.Publish = func(*Snapshot) {}
	}
	if len(r.Channels) == 0 {
		r.Channels = []Channel{{Harness: HarnessClaude}}
	}
	if r.snapshot == nil {
		r.snapshot = &Snapshot{
			Project:     r.Project,
			ScopeAll:    r.Scope.All,
			ScopeUserID: r.Scope.UserID,
			Channel:     r.Channels[0],
			StartedAt:   r.Now().UTC().Format(time.RFC3339),
			Active:      []ActiveWI{},
			Recent:      []Outcome{},
		}
	}
}

// currentChannel returns the channel in use and its index, so a caller that later needs to
// demote it names the same slot rather than a value that may appear more than once.
func (r *Runner) currentChannel() (Channel, int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	// A bounds guard, not dead code: demoteChannel never walks past the last index, but this
	// keeps a future edit to the candidate list from turning a scheduling bug into a panic in
	// an unattended process.
	if r.chanIdx >= len(r.Channels) {
		return r.Channels[len(r.Channels)-1], len(r.Channels) - 1
	}
	return r.Channels[r.chanIdx], r.chanIdx
}

// demoteChannel advances past the candidate at index `failedIdx`, returning false when there is
// none left.
//
// It takes an INDEX rather than a Channel value. Comparing by value was wrong whenever the
// candidate list contains the same (harness, model) pair twice — which `--channel=claude,codex,claude`
// produces and parseChannels accepts: a worker failing on the FIRST claude, after chanIdx had
// already reached the second, compared equal, advanced again, and could report "every channel
// candidate is unauthenticated" without codex ever having been tried.
//
// The guard itself is the point: several steps run concurrently and may all hit the same expired
// credential, so without it each concurrent step burns one healthy candidate.
func (r *Runner) demoteChannel(failedIdx int) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.chanIdx != failedIdx {
		// Somebody already moved past it; this worker just retries on the current channel.
		return true
	}
	if r.chanIdx+1 >= len(r.Channels) {
		return false
	}
	r.chanIdx++
	r.snapshot.Channel = r.Channels[r.chanIdx]
	return true
}

// update mutates the snapshot under the lock and hands Publish a copy that shares NOTHING with
// the live one.
//
// The deep copy is the whole point, and a shallow `cp := *r.snapshot` was a real data race
// rather than a theoretical one. A struct copy duplicates the slice HEADERS and leaves both
// pointing at the same backing arrays, while setActive writes `s.Active[i] = a` in place and
// clearActive compacts through `s.Active[:0]` in place. The production Publish marshals its
// argument to JSON, so `-race` catches it as a read of an element another worker is writing,
// and the consequence without the detector is a torn ActiveWI in snapshot.json or a fault
// inside reflect on a half-updated string header.
//
// Copying under the lock and publishing outside it keeps Publish (which does file I/O) off the
// critical path, which matters because every worker in a round calls this on every step.
func (r *Runner) update(fn func(*Snapshot)) {
	r.mu.Lock()
	fn(r.snapshot)
	cp := *r.snapshot
	cp.Active = append([]ActiveWI(nil), r.snapshot.Active...)
	cp.Recent = append([]Outcome(nil), r.snapshot.Recent...)
	cp.PreflightRejected = append([]PreflightVerdict(nil), r.snapshot.PreflightRejected...)
	cp.Queue.ExternallyBlocked = copyBlocked(r.snapshot.Queue.ExternallyBlocked)
	cp.Queue.NeedsHumanSession = append([]Candidate(nil), r.snapshot.Queue.NeedsHumanSession...)
	r.mu.Unlock()
	r.Publish(&cp)
}

// copyBlocked deep-copies the externally-blocked list INCLUDING each entry's Blockers slice.
//
// A one-level `append([]BlockedWorkItem(nil), ...)` was enough while Blockers was built once and
// never touched again; it stopped being enough when the element type gained a slice of its own,
// because the copy's entries would still share that backing array with the live snapshot. The
// invariant this whole function promises is "shares NOTHING with the live one", and a copy that
// is one level short is exactly the shape `-race` finds months later, inside encoding/json.
func copyBlocked(in []BlockedWorkItem) []BlockedWorkItem {
	if in == nil {
		return nil
	}
	out := make([]BlockedWorkItem, len(in))
	for i, b := range in {
		b.Blockers = append([]BlockerRef(nil), b.Blockers...)
		out[i] = b
	}
	return out
}

func (r *Runner) setActive(a ActiveWI) {
	r.update(func(s *Snapshot) {
		for i := range s.Active {
			if s.Active[i].Candidate.ID == a.Candidate.ID {
				s.Active[i] = a
				return
			}
		}
		s.Active = append(s.Active, a)
	})
}

func (r *Runner) clearActive(wiID string) {
	r.update(func(s *Snapshot) {
		out := s.Active[:0]
		for _, a := range s.Active {
			if a.Candidate.ID != wiID {
				out = append(out, a)
			}
		}
		s.Active = out
	})
}

func (r *Runner) finish(t Terminal, stop StopReason, q QueueState) {
	r.update(func(s *Snapshot) {
		s.Finished = true
		s.Terminal = t
		s.StopReason = stop
		s.Queue = q
		s.ExitCode = ExitCode(t)
		s.Active = []ActiveWI{}
	})
}

// notifyExternalBlocks writes layer ② notes for a BLOCKED_EXTERNAL ending: one on each work item
// of mine that is stuck, and one on each blocker holding it up.
//
// It takes the LIST rather than reading a count, and that is the fix for a defect that the test
// suite actively certified as working. The note used to be built with no WorkItemID at all, and
// the production Notify seam returns early when the target is empty (a note needs a timeline to
// land on), so the run emitted nothing — while the fake in the tests appended unconditionally and
// the assertion "a note was recorded" passed. The single terminal state the design calls "the one
// that notifies" notified nobody, and the suite said otherwise.
//
// Best-effort: the run is over and its exit code already carries the state, so a failure to
// annotate must not change what the run reports. It is logged rather than swallowed.
func (r *Runner) notifyExternalBlocks(ctx context.Context, blocked []BlockedWorkItem) {
	if r.Notify == nil || len(blocked) == 0 {
		return
	}
	for _, b := range blocked {
		name := b.Slug
		if name == "" {
			name = b.WorkItemID
		}
		if err := r.Notify(ctx, Notification{
			WorkItemID: b.WorkItemID,
			Note: fmt.Sprintf("polyforge drain stopped on project %s: this work item is blocked by "+
				"work outside the draining scope (%s), so waiting will not clear it.",
				r.Project, strings.Join(b.BlockerNames(), ", ")),
		}); err != nil {
			r.Logf("drain: warning: could not annotate blocked work item %s: %v", name, err)
		}
		// The half the ruling calls "更漂亮的一手": tell the blocker somebody is waiting. In an
		// unattended run this is the only channel that reaches another person at all.
		for _, blocker := range b.Blockers {
			if !blocker.Notifiable() {
				// A blocker in a project this caller cannot open. aihub sends its slug and
				// withholds its id on purpose, so there is nothing to address: the note used to
				// go out with work_item_id="hidden" and answer 404 on every round. Reported once
				// instead, because "somebody outside your visibility is holding this" is real
				// information even when it cannot be delivered to them.
				r.Logf("drain: %s is blocked by %s, which is outside your project visibility, so "+
					"no note can be left on it", name, blocker.Display())
				continue
			}
			if err := r.Notify(ctx, Notification{
				WorkItemID: blocker.ID,
				BlockerID:  b.WorkItemID,
				Note: fmt.Sprintf("polyforge drain is blocked on %s, which is waiting for this work item.",
					name),
			}); err != nil {
				r.Logf("drain: warning: could not annotate blocker %s: %v", blocker.Display(), err)
			}
		}
	}
}

// reportHumanSessionNeeded names the requires_human_session work items that made this run end
// BLOCKED_EXTERNAL.
//
// 🔴 It deliberately does NOT write a note, and that is a decision rather than an omission.
// Notification layer ② is for reaching ANOTHER PERSON about work they hold — the ruling calls the
// note on the blocker "更漂亮的一手 … the only channel that reaches another human in an unattended
// run". A queued requires_human_session work item has no blocker and no other person: it is the
// drain operator's own queue, and they are already reading this run's exit code and report.
//
// The cost of getting that wrong is measured rather than guessed. aihub#636's note de-duplication
// keys on the emitting ATTEMPT (`run_attempt_id`, internal/domain/memory.go), and drain's Notify
// deliberately sends no attempt credentials — it must not, because neither notification target is
// a work item this run holds an attempt on. So drain's notes are never de-duplicated: an hourly
// drain against a project with eight such work items would add 192 identical timeline entries a
// day to the one record a human actually reads. A stable population needs a report, not a stream
// of identical notes.
//
// So this lands in layers ① and ③ instead: the exit code carries the state, and the slugs go to
// stderr AND into the snapshot (QueueState.NeedsHumanSession is a list, so `polyforge watch`
// renders them with no network).
func (r *Runner) reportHumanSessionNeeded(wis []Candidate) {
	if len(wis) == 0 {
		return
	}
	names := make([]string, 0, len(wis))
	for _, c := range wis {
		name := c.Slug
		if name == "" {
			name = c.ID
		}
		names = append(names, name)
	}
	r.Logf("drain: %d work item(s) are queued but marked requires_human_session, so an unattended "+
		"run may not execute them: %s. Each needs a person in a session, or a deliberate "+
		"reclassification to requires_human_session=false.", len(names), strings.Join(names, ", "))
}

// bailOut decides whether a failed server call is the run being cancelled rather than a genuine
// error.
//
// It exists because the alternative was measured to be wrong in a way that reaches the operator:
// a Ctrl-C landing mid-round made the next list call fail with context.Canceled, Run returned an
// error, and Classify and finish never ran. The CLI then exited 2 ("internal error") for an
// ordinary interrupt, StopCancelled was never reported for the one case it exists for, and the
// snapshot was left with Finished=false and a dead pid, which `polyforge watch` renders as
// "DIED (process gone, run never finished)". Cancellation is an ENDING, not a failure, and has to
// go through the same terminal-state machinery as every other ending.
func (r *Runner) bailOut(ctx context.Context, what string, err error) bool {
	if ctx.Err() == nil && !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	r.Logf("drain: stopping, the run was cancelled during %s", what)
	return true
}
