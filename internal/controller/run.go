package controller

// run.go — the DB-workflow execution driver (aihub#708 Batch 2B).

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/GMISWE/ieops-aihub/internal/workflow"
)

// Run drives one CLAIMED work item's pinned workflow generation to a final
// driver outcome: it loops the shared NextAction classification (the same one
// `engine workflow` renders) to pick the next step, executes each step through
// RunStep (server-minted invocation, materialized bundle, modelruntime
// fallback, structured result), consults the pure policy (workflow.Decide)
// after every recorded result and before any wrap, and — only when the flow is
// genuinely finished — calls the EXISTING CompleteAttempt seam with status
// "wrapped", then the worktree cleanup seam.
//
// # The endings, and what each deliberately does NOT do
//
//	wrapped    every step completed and workflow.Decide said advance;
//	           CompleteAttempt wrapped it and worktrees were cleaned up.
//	failed     reserved for an explicit business-terminal disposition. Driver,
//	           machine configuration, dispatch, worker-output, and lifecycle
//	           transport failures are recoverable pauses instead: they never
//	           call CompleteAttempt("failed"). If pausing itself cannot be
//	           confirmed, the attempt remains claimed with its locks held and
//	           the result still uses the recoverable paused vocabulary.
//	paused     a RECOVERABLE hold: a review FAIL (the server paused the
//	           attempt atomically when the result was recorded), a step whose
//	           result awaits authenticated human approval (never faked — the
//	           run pauses the attempt itself and says who has to decide), an
//	           episode recovery needing a repair-producer judgment this
//	           unattended controller does not make, provider_error retries
//	           exhausted on a channel that is not recovering, or — the one that
//	           looks like success — a COMPLETE flow whose recorded history
//	           the events API cannot re-present to workflow.Decide (gate
//	           evidence is not exposed; richer repair lineages than
//	           PolicyInput names). That last pause defers the wrap to a
//	           human rather than claiming advance without the policy.
//	cancelled  the context ended. The attempt is left CLAIMED (status
//	           running, locks held) exactly like the legacy drain path leaves
//	           a cancelled work item; any open invocation stays open under it,
//	           and the next attempt must fence it through the reconcile
//	           transition. The Err text says so.
//
// # No legacy fallback, no role resolution
//
// The DB path is entered only when GET /workflow reports a pinned generation
// (Select), and a read failure anywhere in it is an error — never a silent
// return to the scenario/role path, which keeps running unchanged for legacy
// work items. Model selection on this path comes from the pinned generation's
// per-step candidates; the role/tier machinery ([roles.tiers], --preset,
// drain.ResolveRole) is the LEGACY path's and is deliberately untouched.
//
// # Interactive callers
//
// The main-session adapter lives in session.go and the CLI exposes it as
// --continue / --prepare / --submit. It shares Select/NextAction, grants,
// input resolution, RunStep for automatic steps, and the same server start / result
// fences as this driver. Session code authors no approval: it renders the exact
// artifact tuple and the existing human-only approval endpoint remains the
// sole transition. Run remains the unattended multi-step driver with injected
// lifecycle seams; session progression intentionally leaves wrap explicit.
type RunAPI interface {
	API
	EventsReader
}

func Run(ctx context.Context, api RunAPI, opts RunOptions) RunResult {
	d := &driver{api: api, opts: opts}
	return d.run(ctx)
}

// Run status vocabulary (the drain adapter maps these onto drain.Result).
const (
	RunWrapped   = "wrapped"
	RunFailed    = "failed"
	RunPaused    = "paused"
	RunCancelled = "cancelled"
)

// RunOptions is everything the driver needs beyond the API. Every mutating
// lifecycle action is an injected seam so the driver performs no I/O the
// caller did not hand it.
type RunOptions struct {
	WorkItemID string
	// AttemptID is the claim's attempt id; when non-empty it must equal the
	// credentials' attempt, else the run refuses to start (a mismatch means
	// the state file belongs to a different attempt than the one claimed).
	AttemptID string
	// Credentials are the current attempt's (from the claim's state file).
	Credentials Credentials
	// WorkDir is the claimed work item's worktree root — the worker's cwd.
	WorkDir string
	// RunDir is the drain run directory (step logs, bundle scratch dirs).
	RunDir string
	Logf   func(format string, args ...any)
	// StepLog persists one step attempt's combined output; seq is the
	// 1-based per-step attempt counter the driver tracks.
	StepLog func(stepID string, seq int, output []byte)
	// PauseAttempt pauses the current attempt (authenticated). Required for
	// the recoverable holds; a nil seam leaves the claim and locks in place and
	// returns a loud recoverable-paused outcome rather than a terminal failure.
	PauseAttempt func(ctx context.Context, reason string) error
	// CompleteAttempt terminates a successful attempt with status "wrapped".
	// Controller/infrastructure failures never use it to manufacture a
	// terminal failed business disposition; they go through PauseAttempt (or
	// retain the claim and locks when no lifecycle stop can be confirmed).
	CompleteAttempt func(ctx context.Context, status, note string) error
	// CleanupWorktrees removes the claim's worktrees after a wrap.
	CleanupWorktrees func(ctx context.Context) error
	// MaxStepRetries is retained for RunOptions compatibility. This driver
	// never mints repair authorizations: a recorded provider_error pauses for
	// an explicit server-side retry (pf_repair_workflow), which a later run
	// then drives — so no unattended bound is needed or applied.
	// StepTimeout bounds each DB invocation independently of the whole drain run.
	// Zero uses the legacy two-hour per-step default.
	StepTimeout time.Duration
}

// RunResult is the driver's final outcome.
type RunResult struct {
	Status string `json:"status"`
	// Steps counts results this run recorded with status completed.
	Steps int `json:"steps"`
	// Err is the human-readable failure/hold description, empty on a wrap.
	Err string `json:"err,omitempty"`
	// Note is what the CompleteAttempt seam was called with, when it was.
	Note string `json:"note,omitempty"`
}

// pendingRepair is an open retry authorization the next invocation of a step
// is bound to, plus the bookkeeping the history needs when it records.
type pendingRepair struct {
	id       string
	failedSA string
	reason   string
}

type driver struct {
	api  RunAPI
	opts RunOptions

	validated       *workflow.ValidatedFlow
	grants          map[string]workflow.StepGrant
	hist            *History
	rhs             bool
	stepsVersion    int
	stepsDone       int
	policyFenceOnly bool
	logf            func(format string, args ...any)
}

func (d *driver) run(ctx context.Context) RunResult {
	d.logf = d.opts.Logf
	if d.logf == nil {
		d.logf = func(string, ...any) {}
	}
	cred := d.opts.Credentials
	if cred.AttemptID == "" || cred.SessionSecret == "" || cred.ClaimEpoch <= 0 {
		return d.fail(ctx, "workflow attempt credentials are missing or incomplete")
	}
	if d.opts.AttemptID != "" && d.opts.AttemptID != cred.AttemptID {
		return d.fail(ctx, fmt.Sprintf(
			"workflow credentials name attempt %s but the claim is attempt %s; the state file does not belong to this claim",
			cred.AttemptID, d.opts.AttemptID))
	}

	state, err := Select(ctx, d.api, d.opts.WorkItemID)
	if err != nil {
		return d.fail(ctx, fmt.Sprintf("select workflow: %v", err))
	}
	if state == nil {
		return d.fail(ctx, "the pinned workflow disappeared between selection and execution; refusing legacy fallback")
	}
	// GET accepts slugs but returns the canonical identity. Pin every later
	// start/result/history call and worker envelope to that canonical id.
	d.opts.WorkItemID = state.WorkItemID
	flow, grants, err := LoadValidatedFlow(ctx, d.api, state)
	if err != nil {
		return d.fail(ctx, fmt.Sprintf("bind pinned generation: %v", err))
	}
	d.validated = flow
	d.grants = grants
	d.stepsVersion = state.StepsVersion
	d.rhs = state.RequiresHumanSession != nil && *state.RequiresHumanSession

	hist, err := LoadHistory(ctx, d.api, d.opts.WorkItemID, state.StepsVersion)
	if err != nil {
		return d.fail(ctx, fmt.Sprintf("reconstruct workflow history: %v", err))
	}
	d.hist = hist
	if _, ok := hist.PolicyInput(flow, d.rhs); !ok {
		d.policyFenceOnly = true
		d.logf("workflow: the recorded history cannot be faithfully re-presented to the pure policy (the events API does not expose " +
			"recorded gate evidence, or the repair lineage is richer than PolicyInput names); server start/result fences still " +
			"govern ordering, but this controller will pause rather than wrap without workflow.Decide")
	}

	for {
		if ctx.Err() != nil {
			return RunResult{Status: RunCancelled, Err: cancelledGuidance("(in flight)")}
		}

		state, err := Select(ctx, d.api, d.opts.WorkItemID)
		if err != nil {
			return d.fail(ctx, fmt.Sprintf("re-read workflow state: %v", err))
		}
		if state == nil {
			return d.fail(ctx, "the pinned workflow disappeared mid-run; refusing legacy fallback")
		}
		if state.StepsVersion != d.stepsVersion {
			return d.fail(ctx, fmt.Sprintf(
				"workflow generation moved from %d to %d mid-run; the attempt cannot record results for a generation it did not start under",
				d.stepsVersion, state.StepsVersion))
		}

		// The shared classification: interactive session and drain render the
		// SAME NextAction, so a hold reported by `engine workflow` is exactly
		// the hold this driver refuses to silently route around.
		action := state.NextAction()
		switch action.Kind {
		case ActionComplete:
			return d.complete(ctx)
		case ActionRun:
			// RHS approval is artifact-bound, so the step must run and record
			// its real artifact before a human can approve it. The next state
			// becomes ActionApprovalRequired; that branch pauses and never
			// fabricates the human decision.
			if res := d.executeStep(ctx, action.StepID, nil); res.Status != "" {
				return res
			}
			// executeStep recorded a completed result; loop for the next step.
		case ActionOpenInvocation:
			// Whose open invocation it is surfaces at START: a dead attempt's
			// is reconciled and replaced there (RunStep); a live one can only
			// be this run's own, which nothing here leaves behind. Drive the
			// step and let the start decide.
			if res := d.executeStep(ctx, action.StepID, nil); res.Status != "" {
				return res
			}
		case ActionRevisionRequired:
			return d.pause(ctx, action.Detail+
				". A human-session driver must prepare and submit the replacement under a new fenced invocation; unattended execution cannot author the human's revision")
		case ActionApprovalRequired:
			return d.pause(ctx, action.Detail+
				". An unattended run does not fabricate approvals; the attempt is paused for a human session "+
				"(pf_approve_workflow binds the exact artifact)")
		case ActionRepairRequired:
			if res := d.holdRepair(ctx, state, action); res.Status != "" {
				return res
			}
			// An already-authorized retry recorded successfully. Re-enter the
			// shared state loop so remaining not-yet-run verification, review,
			// shipping, and final lifecycle are driven normally. In particular,
			// do not return an empty/no-status outcome and do not manufacture a
			// gate approval.
		default:
			return d.fail(ctx, fmt.Sprintf("workflow state holds an unknown action kind %q (%s)", action.Kind, action.Detail))
		}
	}
}

// executeStep runs one step to a recorded result. bound, when non-nil, is an
// already open retry authorization the invocation binds to. A non-empty
// RunResult ends the run; an empty one means the step completed.
func (d *driver) executeStep(ctx context.Context, stepID string, bound *pendingRepair) RunResult {
	pending := bound
	if ctx.Err() != nil {
		return RunResult{Status: RunCancelled, Err: cancelledGuidance(stepID)}
	}
	repairID := ""
	if pending != nil {
		repairID = pending.id
	}
	grant := d.grants[stepID]
	var buf bytes.Buffer
	out, err := RunStep(ctx, d.api, d.opts.WorkItemID, stepID, d.opts.WorkDir, StepOptions{
		Credentials:          d.opts.Credentials,
		RepairEpisodeID:      repairID,
		RunDir:               d.opts.RunDir,
		Log:                  &buf,
		Logf:                 d.logf,
		StepTimeout:          d.opts.StepTimeout,
		ExpectedStepsVersion: d.stepsVersion,
		ExpectedGrant:        &grant,
	})
	if d.opts.StepLog != nil {
		// The per-step attempt counter is always 1: a recorded provider_error
		// pauses for an explicit server-side repair instead of rerolling, so
		// one invocation is all this driver ever drives for the step.
		d.opts.StepLog(stepID, 1, buf.Bytes())
	}
	if err != nil {
		if ctx.Err() != nil {
			return RunResult{Status: RunCancelled, Err: cancelledGuidance(stepID)}
		}
		if isPausedConflict(err) {
			return RunResult{Status: RunPaused, Steps: d.stepsDone, Err: fmt.Sprintf(
				"workflow step %s: the attempt was paused out from under the run: %v", stepID, err)}
		}
		if errors.Is(err, ErrInvocationOpenLive) {
			return d.retainClaim(fmt.Sprintf("workflow step %s: %v", stepID, err))
		}
		var executionHold *RecoverableExecutionError
		if errors.As(err, &executionHold) && executionHold.RetainClaim {
			return d.retainClaim(fmt.Sprintf("workflow step %s: %v", stepID, err))
		}
		return d.fail(ctx, fmt.Sprintf("workflow step %s: %v", stepID, err))
	}
	if !out.Recorded {
		return d.fail(ctx, fmt.Sprintf("workflow step %s returned without recording a result", stepID))
	}

	expected := workflow.ExpectedInvocation{
		WorkItemID:    d.opts.WorkItemID,
		FlowVersion:   out.Invocation.StepsVersion,
		StepID:        out.Invocation.StepID,
		StepAttemptID: out.Invocation.StepAttemptID,
		Epoch:         int(out.Invocation.ClaimEpoch),
		ProducerID:    out.Invocation.ProducerID,
	}
	d.hist.Record(expected, out.Result)
	if pending != nil {
		d.hist.NoteRepair(pending.id, "retry", pending.failedSA, pending.reason, expected, len(d.hist.Results)-1)
	}

	if out.Result.ReviewVerdict == workflow.ReviewFail {
		// The server paused the attempt atomically when the FAIL was
		// recorded; the recovery is an episode authorization, which is a
		// repair-producer judgment this unattended controller does not
		// make (see holdRepair).
		return RunResult{Status: RunPaused, Steps: d.stepsDone, Err: fmt.Sprintf(
			"review FAIL on workflow step %s (step attempt %s); the attempt is paused (recoverable, never terminal). "+
				"Recovery is a repair episode (repair producer + fresh verification + fresh independent review) driven from a resumed attempt",
			stepID, out.Invocation.StepAttemptID)}
	}

	if out.Result.Status == workflow.StatusProviderError {
		// A recorded provider_error is a real result. Opening a replacement
		// is a repair decision, so pause and require an explicit server-side
		// authorization instead of fabricating one unattended.
		return d.pause(ctx, fmt.Sprintf(
			"workflow step %s recorded provider_error; the attempt is paused with the result retained. "+
				"Authorize an explicit retry repair before re-draining; no repair was fabricated", stepID))
	}

	if out.Result.Status != workflow.StatusCompleted {
		// incomplete/blocked/invalid_result: a step outcome, not
		// infrastructure. The pure policy pauses on it; so does this run.
		return d.pause(ctx, fmt.Sprintf(
			"workflow step %s recorded %q; the flow cannot advance on it and recovery is a decision, not a reroll. "+
				"The attempt is paused with the result retained", stepID, out.Result.Status))
	}

	d.stepsDone++

	// Completed: the pure policy runs over the whole history. Mid-flow its
	// answers mean: WAIT is the ordinary "flow not finished" verdict (and,
	// after a provider_error retry, "hold until fresh gates run") — both are
	// exactly what looping does next, and the approval-required hold is
	// NextAction's to report as ActionApprovalRequired, never this branch's;
	// PAUSE is the recoverable stop. ADVANCE is the wrap-time verdict and
	// cannot appear before the last step records — complete() re-runs the
	// policy there, which is where it is acted on.
	dec, verifiable, derr := d.decide()
	switch {
	case !verifiable:
		// Fence mode (history.go): the server's start fence already
		// ordered this completion; continue on NextAction.
	case derr != nil:
		return d.fail(ctx, fmt.Sprintf("pure workflow policy refused the history after step %s: %v", stepID, derr))
	case dec == workflow.DecisionWait:
		d.logf("workflow: step %s completed; the pure policy holds the flow at wait (not finished or fresh gates pending); continuing", stepID)
	case dec == workflow.DecisionPause:
		return d.pause(ctx, fmt.Sprintf("pure workflow policy paused the flow after step %s", stepID))
	}
	return RunResult{}
}

// holdRepair maps an ActionRepairRequired hold onto an ending. A D9 retry
// is driven only when an explicit open authorization already exists. Everything
// else — above all a review FAIL — pauses without inventing repair authority.
func (d *driver) holdRepair(ctx context.Context, state *State, action NextAction) RunResult {
	p := progressFor(state, action.StepID)
	if p == nil {
		return d.fail(ctx, action.Detail)
	}
	if p.ReviewVerdict == string(workflow.ReviewFail) {
		return d.pause(ctx, fmt.Sprintf(
			"workflow step %s carries a review FAIL; recovery is a repair episode (repair producer + fresh verification + "+
				"fresh independent review), and choosing the repair producer is a judgment this unattended controller does "+
				"not make. The attempt is paused; drive the episode from a resumed session (pf_repair_workflow kind=episode, "+
				"then pf_start_workflow_step with the episode id for each role)", action.StepID))
	}
	if p.Status != string(workflow.StatusProviderError) {
		return d.pause(ctx, fmt.Sprintf(
			"workflow step %s is held (%s/%s) and no authorized recovery this run may drive applies; the attempt is paused "+
				"with the state retained", action.StepID, p.Status, p.ReviewVerdict))
	}

	// A provider_error left in the generation. Drive an OPEN retry
	// authorization exactly as recorded; otherwise pause without inventing one.
	for _, r := range state.Repairs {
		if r.Kind == "retry" && r.Status == "open" && r.FailedStepsVersion == state.StepsVersion &&
			r.FailedStepAttemptID == p.StepAttemptID {
			d.logf("workflow: step %s has an open retry authorization %s; driving it", action.StepID, r.ID)
			return d.executeStep(ctx, action.StepID, &pendingRepair{
				id: r.ID, failedSA: r.FailedStepAttemptID, reason: "retry authorization opened by an earlier attempt",
			})
		}
	}
	return d.pause(ctx, fmt.Sprintf(
		"workflow step %s is held on provider_error and has no open retry authorization; the attempt is paused and no repair was fabricated",
		action.StepID))
}

// complete is the wrap path: NextAction reports every step completed, no
// open invocation, no unresolved recovery, approvals present.
func (d *driver) complete(ctx context.Context) RunResult {
	unrepresentable := "the workflow is complete in server state, but its persisted history cannot be faithfully " +
		"re-presented to workflow.Decide (the events API does not expose recorded gate evidence, or the repair lineage is " +
		"richer than PolicyInput names); the attempt is paused rather than wrapping without a pure-policy decision. " +
		"Recovery: inspect the step results on the work item timeline, then wrap it from a human session " +
		"(pf_complete_attempt) — or leave it paused for the person who owns that call"
	if d.policyFenceOnly {
		return d.pause(ctx, unrepresentable)
	}
	in, ok := d.hist.PolicyInput(d.validated, d.rhs)
	if !ok {
		return d.pause(ctx, unrepresentable)
	}
	dec, err := workflow.Decide(d.validated, d.hist.Results, in)
	switch {
	case err != nil:
		return d.fail(ctx, fmt.Sprintf("pure workflow policy refused the wrap: %v", err))
	case dec == workflow.DecisionWait:
		return d.pause(ctx, "the flow is complete but an approval the pure policy requires is missing; the attempt is paused")
	case dec == workflow.DecisionPause:
		return d.pause(ctx, "the pure workflow policy paused the flow at completion; the attempt is paused")
	case dec != workflow.DecisionAdvance:
		return d.fail(ctx, fmt.Sprintf("pure workflow policy returned unknown wrap decision %q", dec))
	}

	note := fmt.Sprintf("drained by polyforge drain (pinned workflow generation %d, %d steps, %d recorded this run)",
		d.stepsVersion, len(d.validated.Definition().Steps), d.stepsDone)
	return d.wrap(ctx, note)
}

// decide runs the pure policy over the current history. verifiable=false
// means the history is not representable; server fences still prevent an
// invalid next start, and complete() pauses instead of wrapping without Decide.
func (d *driver) decide() (workflow.Decision, bool, error) {
	in, ok := d.hist.PolicyInput(d.validated, d.rhs)
	if !ok {
		d.policyFenceOnly = true
		return "", false, nil
	}
	dec, err := workflow.Decide(d.validated, d.hist.Results, in)
	if err != nil {
		return "", true, err
	}
	return dec, true, nil
}

// wrap completes the attempt as wrapped and cleans up. A missing seam or an
// unconfirmed completion is an infrastructure hold, not permission to turn
// successful business work into a terminal failure.
func (d *driver) wrap(ctx context.Context, note string) RunResult {
	if d.opts.CompleteAttempt == nil {
		return RunResult{Status: RunPaused, Steps: d.stepsDone,
			Err: "no CompleteAttempt seam configured; completion is unconfirmed, so the attempt is left CLAIMED holding its locks"}
	}
	if err := d.opts.CompleteAttempt(ctx, "wrapped", note); err != nil {
		return RunResult{Status: RunPaused, Steps: d.stepsDone, Err: fmt.Sprintf(
			"wrap: attempt completion was not confirmed (%v); it is left CLAIMED and still holds its locks", err)}
	}
	if d.opts.CleanupWorktrees != nil {
		if err := d.opts.CleanupWorktrees(ctx); err != nil {
			// Best-effort, exactly as the legacy path treats cleanup: a
			// leftover worktree is a disk-space problem, not a failed work
			// item.
			d.logf("workflow: wrapped, but worktree cleanup reported: %v", err)
		}
	}
	d.logf("workflow: wrapped (generation %d, %d step result(s) recorded this run)", d.stepsVersion, d.stepsDone)
	return RunResult{Status: RunWrapped, Steps: d.stepsDone, Note: note}
}

// fail is retained as the common controller-error call site, but controller,
// configuration, dispatch, and output errors are RECOVERABLE lifecycle
// outcomes. They pause instead of calling CompleteAttempt("failed"); only a
// future explicit business-terminal disposition may use RunFailed and terminal
// completion.
func (d *driver) fail(ctx context.Context, reason string) RunResult {
	return d.pause(ctx, reason)
}

// retainClaim is a lifecycle-free hold: an open live invocation or an
// unconfirmed worker stop cannot release locks or close the attempt.
func (d *driver) retainClaim(reason string) RunResult {
	return RunResult{Status: RunPaused, Steps: d.stepsDone, Err: reason +
		". The attempt remains CLAIMED (status running) with its locks held; no lifecycle mutation was attempted"}
}

// failure is still reported in the recoverable vocabulary consumed by drain,
// while explicitly stating that the claim and locks were retained.
func (d *driver) pause(ctx context.Context, reason string) RunResult {
	if d.opts.PauseAttempt == nil {
		return RunResult{Status: RunPaused, Steps: d.stepsDone, Err: fmt.Sprintf(
			"%s. The attempt could not be paused (no pause seam); it remains CLAIMED, status running, holding its locks", reason)}
	}
	if err := d.opts.PauseAttempt(ctx, reason); err != nil {
		return RunResult{Status: RunPaused, Steps: d.stepsDone, Err: fmt.Sprintf(
			"%s. The pause was not confirmed (%v); the attempt remains CLAIMED, status running, holding its locks", reason, err)}
	}
	return RunResult{Status: RunPaused, Steps: d.stepsDone, Err: reason}
}

// progressFor returns the progress entry of one step.
func progressFor(state *State, stepID string) *Progress {
	for i := range state.Progress {
		if state.Progress[i].StepID == stepID {
			return &state.Progress[i]
		}
	}
	return nil
}

func cancelledGuidance(stepID string) string {
	return fmt.Sprintf("cancelled mid-workflow at step %s; the attempt is left CLAIMED (status running) holding its locks, "+
		"exactly like a cancelled legacy step. Any open step invocation stays open under it: the next attempt must fence it "+
		"with the workflow reconcile transition (supersede_attempt_id naming this attempt) before it can start a replacement", stepID)
}
