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

	// --- observation -----------------------------------------------------------------------

	// Publish is called whenever the snapshot changes.
	Publish func(*Snapshot)
	// Logf reports progress on stderr.
	Logf func(format string, args ...any)

	mu       sync.Mutex
	snapshot *Snapshot
	chanIdx  int
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

	var (
		outcomes  []Outcome
		anyFailed bool
		stop      = StopQueueDrained
		round     int
	)

	// skipped holds work items this RUN has already declined to execute — ones whose claim lost
	// a lock race, and ones whose claim failed for any other reason.
	//
	// This set is what makes "skip, never retry" (aihub#640 `two_kinds_of_blocking`) actually
	// mean skip. A lock-blocked work item is never claimed, so its status stays `queued` and
	// the server keeps returning it as executable; without this set the next round freezes it
	// again, claims again, is refused again, and the run never terminates — a busy-wait wearing
	// a round loop's clothing, and precisely the deadlock temptation the ruling warns about
	// ("等待是死锁的温床"). Holding the skip for the whole run is also the honest scope: the
	// holder is another live attempt, and nothing this run does will end it.
	skipped := map[string]bool{}

	for {
		if err := ctx.Err(); err != nil {
			stop = StopCancelled
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
			return RunReport{}, fmt.Errorf("list in-scope work items: %w", err)
		}
		beforeIDs := IDSet(before)

		candidates, err := r.Executable(ctx)
		if err != nil {
			return RunReport{}, fmt.Errorf("list executable work items: %w", err)
		}

		budgetLeft := r.Budget.RemainingWorkItems(len(outcomes))
		if budgetLeft == 0 {
			stop = StopMaxWorkItems
			break
		}
		frozen, _ := FreezeRound(filterSkipped(candidates, skipped), budgetLeft)
		if len(frozen) == 0 {
			stop = StopQueueDrained
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
				anyFailed = true
			case ResultLockBlocked:
				tally.LockBlocked++
				skipped[o.Candidate.ID] = true
			case ResultPaused:
				tally.Paused++
			case ResultClaimFailed:
				skipped[o.Candidate.ID] = true
			}
			r.update(func(s *Snapshot) { s.Totals.Add(o.Result); s.Recent = append(s.Recent, o) })
		}

		// Re-list to count what came into existence while the round was running.
		after, err := r.AllInScope(ctx)
		if err != nil {
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
			stop = StopQueueDrained
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
		r.notifyExternalBlocks(ctx)
	}

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

	claim, blocker, err := r.Claim(ctx, c.ID, r.NewULID())
	switch {
	case errors.Is(err, ErrLockTaken):
		// SKIP, never retry (`two_kinds_of_blocking`). Waiting on a lock inside a scheduler
		// that holds locks of its own is how a deadlock is built.
		r.Logf("drain: %s skipped: a resource lock is held by another attempt", c.Slug)
		res.Result = ResultLockBlocked
		res.Blocker = blocker
		if blocker != nil && blocker.WorkItem != "" && r.Notify != nil {
			// Layer ②'s second half: tell the holder somebody is waiting.
			_ = r.Notify(ctx, Notification{
				WorkItemID: blocker.WorkItem,
				BlockerID:  c.ID,
				Note: fmt.Sprintf("polyforge drain skipped %s: it needs a resource this work item holds (%s).",
					c.Slug, blocker.Resource),
			})
		}
		return res
	case err != nil:
		r.Logf("drain: %s claim failed: %v", c.Slug, err)
		res.Result = ResultClaimFailed
		res.Err = err.Error()
		return res
	}

	steps, err := r.Startup(ctx, *claim)
	if err != nil {
		res.Result = ResultFailed
		res.Err = fmt.Sprintf("engine startup: %v", err)
		_ = r.CompleteAttempt(ctx, c.ID, "failed", "failed reason: engine startup: "+err.Error())
		return res
	}
	if len(steps) == 0 {
		res.Result = ResultFailed
		res.Err = "engine startup returned no steps"
		_ = r.CompleteAttempt(ctx, c.ID, "failed", "failed reason: scenario template produced no steps")
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
		_ = r.CompleteAttempt(ctx, c.ID, "failed", "failed reason: "+res.Err)
		return res
	}

	for i, step := range steps {
		if ctx.Err() != nil {
			res.Result = ResultPaused
			res.Err = "cancelled mid-run; attempt left running for resumption"
			return res
		}

		role, readOnly, rerr := r.ResolveRole(step.ID)
		if rerr != nil {
			res.Result = ResultFailed
			res.Err = fmt.Sprintf("resolve role for step %s: %v", step.ID, rerr)
			_ = r.CompleteAttempt(ctx, c.ID, "failed", "failed reason: "+res.Err)
			return res
		}

		ch := r.currentChannel()
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
			_ = r.CompleteAttempt(ctx, c.ID, "failed", "failed reason: "+res.Err)
			return res
		}

		if DetectPause(out.Output) {
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
					_ = r.UpdateStep(ctx, c.ID, call)
				}
				_ = r.CompleteAttempt(ctx, c.ID, "failed",
					"failed reason: review_fail at step "+step.ID)
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
			// The fused form is correct against every server that publishes next_step.
			// engine.PlanStepBracket owns the fused-vs-degraded choice; drain only reports
			// what the connected server supports, and a server that does not is older than
			// this subcommand.
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
				_ = r.CompleteAttempt(ctx, c.ID, "failed", "failed reason: "+res.Err)
				return res
			}
		}
		saID = nextSA
		res.Steps = i + 1
	}

	r.clearActive(c.ID)
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

// dispatchWithFallback runs one step, falling to the next channel candidate on an authentication
// failure — the run-time half of `three_ops_problems` ② ("运行中 401 则靠候选列表自动落到下一条通
// 道"). It never falls through for an ordinary step failure: only a credential problem is the
// channel's fault rather than the work's.
func (r *Runner) dispatchWithFallback(ctx context.Context, req DispatchRequest) (DispatchResult, error) {
	for {
		ch := r.currentChannel()
		req.Channel = ch
		out, err := r.Dispatch(ctx, req)
		verdict := ClassifyStepDispatch(out, err)
		switch verdict {
		case StepOK:
			return out, nil
		case StepAuthFailure:
			if !r.demoteChannel(ch) {
				return out, fmt.Errorf("%w: every channel candidate is unauthenticated (last: %s)",
					ErrAuth, ch)
			}
			r.Logf("drain: channel %s failed authentication; falling back to %s",
				ch, r.currentChannel())
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
	if err != nil || errors.Is(out.ExitErr, ErrAuth) {
		if IsAuthFailure(out.Output) || errors.Is(err, ErrAuth) {
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

func (r *Runner) currentChannel() Channel {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.chanIdx >= len(r.Channels) {
		return r.Channels[len(r.Channels)-1]
	}
	return r.Channels[r.chanIdx]
}

// demoteChannel advances to the next candidate, returning false when there is none left.
func (r *Runner) demoteChannel(failed Channel) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	// Only advance if nobody already advanced past the channel that failed: several steps run
	// concurrently and may all hit the same expired credential, and each of them calling this
	// would otherwise burn one healthy candidate per concurrent step.
	if r.chanIdx < len(r.Channels) && r.Channels[r.chanIdx] != failed {
		return true
	}
	if r.chanIdx+1 >= len(r.Channels) {
		return false
	}
	r.chanIdx++
	r.snapshot.Channel = r.Channels[r.chanIdx]
	return true
}

func (r *Runner) update(fn func(*Snapshot)) {
	r.mu.Lock()
	fn(r.snapshot)
	cp := *r.snapshot
	r.mu.Unlock()
	r.Publish(&cp)
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

// notifyExternalBlocks writes layer ② notes for a BLOCKED_EXTERNAL ending. Best-effort: the run
// is over and its exit code already carries the state, so a failure to annotate must not change
// what the run reports.
func (r *Runner) notifyExternalBlocks(ctx context.Context) {
	if r.Notify == nil {
		return
	}
	if err := r.Notify(ctx, Notification{
		Note: fmt.Sprintf("polyforge drain stopped with BLOCKED_EXTERNAL on project %s: "+
			"remaining in-scope work is blocked by work items outside this scope.", r.Project),
	}); err != nil {
		r.Logf("drain: warning: could not record the BLOCKED_EXTERNAL note: %v", err)
	}
}
