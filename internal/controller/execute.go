package controller

// execute.go — one server-fenced workflow invocation for the DB workflow path
// (aihub#708 Batch 2B).
//
// RunStep is deliberately NARROW: it mints exactly one invocation through the
// server's start endpoint (which is the ordering fence — ordering, approvals
// and repair bindings are enforced there under the work item lock, never
// here), materializes the pinned bundle, executes the entry skill through the
// modelruntime fallback chain, and records the worker's structured result.
// Progression decisions — which step is next, whether the flow advances,
// pauses or waits — live in run.go, because those need the history and the
// pure policy, not one invocation.
//
// What RunStep refuses to do is the security posture of the whole path:
//
//   - it never fabricates an artifact, evidence, or an approval — the worker
//     output MUST be a structured workflow.StepResult with a REAL immutable
//     artifact, and a result that attempts to carry an approval is rejected
//     by the server outright;
//   - after a candidate starts, another candidate is offered only for a
//     failure the controller can POSITIVELY classify as a channel failure (a
//     parent-side transport error, never the worker's own exit), only on a
//     read-only dispatch whose confirmed-empty stopped tree provably leaves
//     no unresolved side effects, and only after that stop and inspection
//     are recorded as explicit controller-valid reconcile evidence — every
//     other post-start failure (a nonzero or unknown exit, missing or
//     malformed output, a contradictory identity, an unconfirmed stop)
//     becomes a recoverable hold naming the side-effect reconciliation
//     required, never a reroll;
//   - a worker result whose identity contradicts the server-minted
//     invocation is refused, not rewritten;
//   - the grant it dispatches under is cross-checked against the
//     controller-derived grant, so the two copies of the capability→grant
//     mapping cannot silently drift apart.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/GMISWE/ieops-aihub/internal/config"
	"github.com/GMISWE/ieops-aihub/internal/modelruntime"
	"github.com/GMISWE/ieops-aihub/internal/skillregistry"
	"github.com/GMISWE/ieops-aihub/internal/workflow"
	"github.com/GMISWE/ieops-aihub/pkg/client"
)

// API is the existing WI workflow and immutable skill-version client surface.
// *client.Client satisfies it in full; the repair/reconcile/events halves were
// added by Batch 2B for the driver, not for RunStep alone.
type API interface {
	WorkflowReader
	ArtifactReader
	StartWorkflowStep(context.Context, string, any) (map[string]any, error)
	RecordWorkflowResult(context.Context, string, any) (map[string]any, error)
	GetSkillVersion(context.Context, string, int) (map[string]any, error)
	ReconcileWorkflowInvocations(context.Context, string, any) (map[string]any, error)
	// SaveArtifact is the controller-sink persistence seam (aihub#725): the
	// SAME attempt-authorized POST /v1/memories path pf_save_artifact uses.
	// *client.Client implements it; RunStep calls it only on the sink branch,
	// with the credentials captured at claim time — never any worker-supplied
	// credential.
	SaveArtifact(context.Context, client.SaveArtifactRequest) (client.SavedArtifact, error)
}

type Credentials struct {
	AttemptID     string `json:"attempt_id"`
	ClaimEpoch    int64  `json:"claim_epoch"`
	SessionSecret string `json:"session_secret"`
}

type Invocation struct {
	InvocationID  string                    `json:"invocation_id"`
	StepsVersion  int                       `json:"steps_version"`
	StepID        string                    `json:"step_id"`
	StepAttemptID string                    `json:"step_attempt_id"`
	ProducerID    string                    `json:"producer_id"`
	ClaimEpoch    int64                     `json:"claim_epoch"`
	Grant         workflow.StepGrant        `json:"grant"`
	SkillID       string                    `json:"skill_id"`
	SkillVersion  int                       `json:"skill_version"`
	Models        []workflow.ModelCandidate `json:"models"`
	Params        json.RawMessage           `json:"params"`
	Inputs        []workflow.InputRef       `json:"inputs"`
	EffectiveRHS  bool                      `json:"effective_rhs"`
}

// StepOptions parameterize one RunStep invocation.
type StepOptions struct {
	Credentials Credentials
	// RepairEpisodeID binds the invocation to an open repair authorization
	// (a D9 retry of a provider_error). Empty for an ordinary invocation.
	RepairEpisodeID string
	// RunDir is the drain run directory; the bundle materializes under it in
	// a private per-invocation scratch dir. Empty means "no run dir", in
	// which case the system temp directory is used — the worktree is never
	// used, because materialized bundle files are prompt scaffolding, not
	// work product, and must not pollute the repo the worker edits.
	RunDir string
	// StepTimeout bounds one invocation, independently of the whole-run context.
	// Zero selects the legacy stepTimeout (two hours).
	StepTimeout time.Duration
	// Log receives the worker's combined output and the controller's own
	// narration (fallback decisions, reconcile evidence). May be nil.
	Log *bytes.Buffer
	// Logf narrates to the run log. May be nil.
	Logf func(format string, args ...any)
	// ExpectedStepsVersion binds the start response to the generation the
	// caller selected. A revision race is refused before dispatch.
	ExpectedStepsVersion int
	// ExpectedGrant is the controller-derived grant for the step (grants.go).
	// When set, a server-minted grant that disagrees with it refuses the
	// invocation: the two copies of the capability→grant mapping must never
	// drift into silently deciding capability together.
	ExpectedGrant *workflow.StepGrant
}

// StepOutcome is what one RunStep produced.
type StepOutcome struct {
	// Invocation is the server-minted descriptor the worker echoed.
	Invocation Invocation
	// Result is valid when Recorded is true.
	Result   workflow.StepResult
	Recorded bool
}

func decodeObject(src map[string]any, dst any) error {
	b, err := json.Marshal(src)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, dst)
}

func (o *StepOptions) logf(format string, args ...any) {
	if o != nil && o.Logf != nil {
		o.Logf(format, args...)
	}
}

// deadAttemptRE extracts the dead attempt a start refusal names. The server's
// refusal is the only channel that carries the holder's attempt id (the
// workflow view exposes the open invocation's id, not its attempt), and the
// reconcile call re-verifies everything server-side, so even a mis-parse can
// only produce a refused reconcile — never a wrong supersede.
var deadAttemptRE = regexp.MustCompile(`from attempt (ra_[A-Za-z0-9]+) whose status is "([a-z]+)"`)

// startInvocation mints one invocation, reconciling a dead attempt's open
// invocation first when the server names one.
func startInvocation(ctx context.Context, api API, wiID, stepID string, opts StepOptions) (Invocation, error) {
	body := map[string]any{
		"attempt_id":     opts.Credentials.AttemptID,
		"claim_epoch":    opts.Credentials.ClaimEpoch,
		"session_secret": opts.Credentials.SessionSecret,
		"step_id":        stepID,
	}
	if opts.RepairEpisodeID != "" {
		body["repair_episode_id"] = opts.RepairEpisodeID
	}
	raw, err := api.StartWorkflowStep(ctx, wiID, body)
	if err == nil {
		return decodeInvocation(raw, stepID, opts)
	}

	// The one recoverable start refusal: an open invocation left behind by an
	// attempt a lifecycle transition already ended (a cancelled run, a pause,
	// a takeover). The server names the attempt and its status; reconciling
	// supersedes exactly those open invocations, and the retry start then
	// mints a fresh identity. Everything else is the caller's error.
	holder, status, ok := deadAttemptFrom(err)
	if !ok || status == "running" || holder == opts.Credentials.AttemptID {
		if status == "running" || holder == opts.Credentials.AttemptID || strings.Contains(strings.ToLower(err.Error()), "already has an open invocation") {
			return Invocation{}, fmt.Errorf("%w: %v", ErrInvocationOpenLive, err)
		}
		return Invocation{}, fmt.Errorf("start workflow step: %w", err)
	}
	opts.logf("workflow: step %s is blocked by an open invocation from attempt %s (status %q); reconciling", stepID, holder, status)
	if _, rerr := api.ReconcileWorkflowInvocations(ctx, wiID, map[string]any{
		"attempt_id":           opts.Credentials.AttemptID,
		"claim_epoch":          opts.Credentials.ClaimEpoch,
		"session_secret":       opts.Credentials.SessionSecret,
		"supersede_attempt_id": holder,
	}); rerr != nil {
		return Invocation{}, fmt.Errorf("reconcile dead attempt %s before starting step %s: %w", holder, stepID, rerr)
	}
	raw, err = api.StartWorkflowStep(ctx, wiID, body)
	if err != nil {
		return Invocation{}, fmt.Errorf("start workflow step after reconciling attempt %s: %w", holder, err)
	}
	return decodeInvocation(raw, stepID, opts)
}

// deadAttemptFrom pulls (attempt id, status) out of a start refusal that names
// a dead attempt's open invocation. It matches on the message because the
// refusal carries no structured details; the reconcile endpoint re-reads the
// named attempt's own row under the work item lock, so a stale or wrong id is
// refused there rather than acted on here.
func deadAttemptFrom(err error) (string, string, bool) {
	if err == nil {
		return "", "", false
	}
	m := deadAttemptRE.FindStringSubmatch(err.Error())
	if m == nil {
		return "", "", false
	}
	return m[1], m[2], true
}

func decodeInvocation(raw map[string]any, stepID string, opts StepOptions) (Invocation, error) {
	var inv Invocation
	if err := decodeObject(raw, &inv); err != nil {
		return Invocation{}, err
	}
	if inv.InvocationID == "" || inv.StepID != stepID || inv.StepsVersion <= 0 || inv.StepAttemptID == "" ||
		inv.ProducerID == "" || inv.ClaimEpoch != opts.Credentials.ClaimEpoch || inv.SkillID == "" || inv.SkillVersion <= 0 {
		return Invocation{}, fmt.Errorf("invalid workflow invocation descriptor")
	}
	if opts.ExpectedStepsVersion > 0 && inv.StepsVersion != opts.ExpectedStepsVersion {
		return Invocation{}, fmt.Errorf("workflow invocation generation %d differs from selected generation %d", inv.StepsVersion, opts.ExpectedStepsVersion)
	}
	if opts.ExpectedGrant != nil && inv.Grant != *opts.ExpectedGrant {
		return Invocation{}, fmt.Errorf(
			"server-minted grant for step %s (%s/%s) disagrees with the controller-derived grant (%s/%s); refusing rather than dispatching under an unverified grant",
			stepID, inv.Grant.Authority, inv.Grant.ProducerIsolation,
			opts.ExpectedGrant.Authority, opts.ExpectedGrant.ProducerIsolation)
	}
	return inv, nil
}

// bundleScratchDir creates a private, unique per-invocation directory under
// the run directory (or the system temp directory). A unique directory avoids
// following pre-existing symlinks or stale files if a server identity is ever
// reused or malformed.
func bundleScratchDir(runDir, stepAttemptID string) (string, error) {
	if stepAttemptID == "" {
		return "", fmt.Errorf("bundle scratch dir needs a step attempt id")
	}
	base := ""
	if runDir != "" {
		base = filepath.Join(runDir, "bundles")
		if err := os.MkdirAll(base, 0o700); err != nil {
			return "", fmt.Errorf("create bundle scratch root: %w", err)
		}
		if err := os.Chmod(base, 0o700); err != nil {
			return "", fmt.Errorf("secure bundle scratch root: %w", err)
		}
	}
	prefix := "pf-workflow-bundle-" + safeScratchName(stepAttemptID) + "-"
	return os.MkdirTemp(base, prefix)
}

func safeScratchName(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_' {
			b.WriteRune(r)
		}
	}
	if b.Len() == 0 {
		return "invocation"
	}
	return b.String()
}

// RunStep starts exactly one server-fenced invocation, materializes the
// pinned bundle, executes the entry skill, and records the worker's
// structured result.
//
// The fallback chain advances freely only for failures that happen BEFORE a
// worker starts: a preflight refusal and a spawn failure are pre-start
// unavailability — nothing ran, so the chain may advance. After a start, a
// new candidate is offered only for a failure the controller can positively
// classify as a channel failure (a parent-side transport error, never the
// worker's own exit decision) on a read-only dispatch, and only after the old
// process tree is stopped and that stop and inspection are recorded as
// explicit, controller-valid reconcile evidence proving no unresolved side
// effects remain. Every other post-start failure — a nonzero or unknown
// exit, a missing, malformed or contradictory result, an unconfirmed stop —
// stops the step with a recoverable hold that names the side-effect
// reconciliation required: the worktree may contain effects this controller
// cannot inspect away, and recovery is a decision, never a reroll. A
// semantically valid structured result is submitted even when the process
// exited nonzero, so a valid review FAIL is recorded — and pauses — instead
// of being rerolled. Once a result is recorded the invocation is closed
// server-side, so a provider_error RESULT is returned to the caller — the
// sanctioned recovery for it is the explicit retry authorization (run.go),
// never a silent re-run under a new candidate.
func RunStep(ctx context.Context, api API, wiID, stepID, workDir string, opts StepOptions) (StepOutcome, error) {
	timeout := opts.StepTimeout
	if timeout <= 0 {
		timeout = 2 * time.Hour
	}
	stepCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	invState, err := Select(stepCtx, api, wiID)
	if err != nil {
		return StepOutcome{}, fmt.Errorf("read workflow inputs: %w", err)
	}
	if invState == nil {
		return StepOutcome{}, fmt.Errorf("workflow disappeared before step %s started", stepID)
	}
	var symbolicInputs []workflow.InputRef
	for _, step := range invState.Steps.Steps {
		if step.ID == stepID {
			symbolicInputs = step.Inputs
			break
		}
	}
	resolvedInputs, err := ResolveInputs(stepCtx, api, invState, symbolicInputs)
	if err != nil {
		return StepOutcome{}, err
	}
	inv, err := startInvocation(stepCtx, api, wiID, stepID, opts)
	if err != nil {
		return StepOutcome{}, err
	}
	out := StepOutcome{Invocation: inv}

	// Replace composition descriptors with access-checked concrete values. The
	// server-minted descriptor is still cross-checked so a revised generation
	// cannot swap the input wiring between the pre-start read and the mint.
	if len(inv.Inputs) != len(symbolicInputs) {
		return out, fmt.Errorf("workflow invocation input descriptors changed during start")
	}
	for i := range symbolicInputs {
		if inv.Inputs[i].Name != symbolicInputs[i].Name || inv.Inputs[i].StepID != symbolicInputs[i].StepID || inv.Inputs[i].Output != symbolicInputs[i].Output {
			return out, fmt.Errorf("workflow invocation input descriptors changed during start")
		}
	}
	// The invocation keeps the exact symbolic wiring the server minted; only
	// the prompt receives access-checked concrete predecessor values.

	rawSkill, err := api.GetSkillVersion(stepCtx, inv.SkillID, inv.SkillVersion)
	if err != nil {
		return out, fmt.Errorf("pinned skill unavailable: %w", err)
	}
	b, err := json.Marshal(rawSkill)
	if err != nil {
		return out, err
	}
	var sv struct {
		Bundle   json.RawMessage `json:"bundle"`
		Contract json.RawMessage `json:"contract"`
		Digest   string          `json:"digest"`
	}
	if err := json.Unmarshal(b, &sv); err != nil {
		return out, err
	}
	bundle, err := skillregistry.DecodeBundle(sv.Bundle)
	if err != nil {
		return out, fmt.Errorf("invalid pinned bundle: %w", err)
	}
	contract, err := skillregistry.DecodeContract(sv.Contract)
	if err != nil {
		return out, fmt.Errorf("invalid pinned contract: %w", err)
	}

	// Materialize the whole pinned bundle — entry plus every supporting file —
	// in a private temp dir, then remove it after the worker process tree is
	// confirmed stopped. If stop cannot be confirmed, retain the scratch tree
	// along with the claim and locks rather than disrupting a possibly-live
	// worker.
	scratch, err := bundleScratchDir(opts.RunDir, inv.StepAttemptID)
	if err != nil {
		return out, err
	}
	cleanupScratch := true
	defer func() {
		if !cleanupScratch {
			opts.logf("workflow: retaining bundle scratch dir %s because the worker process tree was not confirmed stopped", scratch)
			return
		}
		if rmErr := os.RemoveAll(scratch); rmErr != nil {
			opts.logf("workflow: could not remove bundle scratch dir %s: %v", scratch, rmErr)
		}
	}()
	entry, err := MaterializeBundle(bundle, scratch)
	if err != nil {
		return out, err
	}

	mc, err := config.LoadMachineConfig()
	if err != nil {
		return out, fmt.Errorf("load model catalog: %w", err)
	}
	catalog, err := modelruntime.LoadCatalog(mc)
	if err != nil {
		return out, err
	}
	identity := modelruntime.AttemptIdentity{
		WorkItemID: wiID, FlowVersion: inv.StepsVersion, StepID: stepID,
		StepAttemptID: inv.StepAttemptID, Epoch: int(inv.ClaimEpoch),
	}
	chain, err := modelruntime.NewFallback(identity, inv.Models, inv.Grant)
	if err != nil {
		return out, err
	}

	inputsJSON, _ := json.Marshal(resolvedInputs)
	prompt := fmt.Sprintf(`Execute the pinned skill entry below for work item %s, step %s.
The skill's supporting files are materialized at %s; paths in the entry are relative to that directory.
Parameters: %s
Inputs: %s

Return ONLY a JSON workflow.StepResult with the exact invocation identity work_item_id=%q, flow_version=%d, step_id=%q, step_attempt_id=%q, epoch=%d, producer_id=%q; status (completed|incomplete|blocked|provider_error|invalid_result), review_verdict (pass|warn|fail) when the step is a review, a REAL immutable artifact {id,version,hash}, and evidence [{kind,ref,hash}] for review and verification gates. Never fabricate proof or human approval; a result that carries an approval is rejected outright.

--- pinned skill entry (%s@%d) ---
%s
--- END ---`,
		wiID, stepID, scratch, inv.Params, inputsJSON,
		wiID, inv.StepsVersion, stepID, inv.StepAttemptID, inv.ClaimEpoch, inv.ProducerID,
		inv.SkillID, inv.SkillVersion, entry)

	sources := modelruntime.LocalCatalogSources()
	startedAny := false
	for {
		if err := stepCtx.Err(); err != nil {
			if ctx.Err() != nil {
				return out, ctx.Err()
			}
			return out, &RecoverableExecutionError{Detail: fmt.Sprintf("workflow step %s timed out after %s before worker start", stepID, timeout)}
		}
		sel, e := chain.Next()
		if e != nil {
			if errors.Is(e, modelruntime.ErrExhausted) {
				writeLog(opts.Log, nil, chain)
				// Every pinned candidate was resolved before a result existed —
				// refused before a start (not in the local [[models]] catalog,
				// harness missing, capability not trusted, no read-only carrier, a
				// spawn failure), or started and lost to a positively known channel
				// failure that was stopped and reconciled with controller-valid
				// evidence on a read-only dispatch. Either way THIS MACHINE cannot
				// carry this step to a recorded result: the first is a property of
				// the machine's catalog, the second of its transport, and neither is
				// of the work item — callers report the typed error so nobody
				// "solves" it by re-running or by falling back to the legacy
				// scenario path.
				reason := "no_dispatchable_candidate"
				if startedAny {
					reason = "all_candidates_failed_infrastructure"
				}
				return out, &UnsupportedDispatchError{
					WorkItemID: wiID,
					StepID:     stepID,
					Reason:     reason,
					Detail:     fmt.Sprintf("no pinned model candidate of step %s reached a recorded result (%v); the recorded fallback-chain state is in the step log", stepID, e),
				}
			}
			return out, fmt.Errorf("model candidates exhausted for step %s: %w", stepID, e)
		}
		ready, refusal := modelruntime.Preflight(catalog, modelruntime.PreflightRequest{
			Candidate: sel.Candidate, Grant: inv.Grant, Requested: contract.Capabilities, Prompt: prompt,
		}, sources, nil)
		if refusal != nil {
			opts.logf("workflow: step %s: candidate %s/%s refused before start: %s", stepID, sel.Candidate.Harness, sel.Candidate.Model, refusal.Reason)
			_ = chain.MarkUnavailable(sel, refusal.Error())
			continue
		}
		// One mutex-protected collector per stream (outputCollector): the
		// os/exec pipe-copy goroutine keeps draining the worker's pipes after
		// Proc.Stop observes the process group empty — a group that emptied
		// says nothing about the pipes, whose in-flight bytes are still being
		// copied — and the deadline branch below reads these streams without
		// waiting for Proc.Wait. With a raw bytes.Buffer those reads raced the
		// copy goroutine's Writes (the CI -race report); with the collector every
		// Write and every snapshot serializes on its mutex.
		var stdout, stderr outputCollector
		proc, e := modelruntime.Start(ready.Command, modelruntime.StartOptions{WorkingDir: workDir, Stdout: &stdout, Stderr: &stderr})
		if e != nil {
			opts.logf("workflow: step %s: could not start %s/%s: %v", stepID, sel.Candidate.Harness, sel.Candidate.Model, e)
			_ = chain.MarkUnavailable(sel, e.Error())
			continue
		}
		_ = chain.MarkStarted(sel)
		startedAny = true
		done := make(chan error, 1)
		go func() { done <- proc.Wait() }()
		var runErr error
		select {
		case runErr = <-done:
		case <-stepCtx.Done():
			// A step deadline is distinct from cancellation of the whole run.
			// Both stop the group; only a confirmed stop permits a pause.
			stopErr := proc.Stop(10 * time.Second)
			// Snapshot under the collector's mutex: the pipe-copy goroutine can
			// still be draining here, because Stop's group-empty observation
			// precedes any Proc.Wait on this branch.
			writeLog(opts.Log, combinedWorkerOutput(stdout.bytes(), stderr.bytes()), chain)
			if stopErr != nil {
				cleanupScratch = false
				return out, &RecoverableExecutionError{Detail: fmt.Sprintf("worker stop after step deadline/cancellation was not confirmed: %v", stopErr), RetainClaim: true}
			}
			if ctx.Err() != nil {
				return out, fmt.Errorf("workflow worker stopped: %w", ctx.Err())
			}
			return out, &RecoverableExecutionError{Detail: fmt.Sprintf("workflow step %s timed out after %s; process group stopped, invocation remains open for recovery", stepID, timeout)}
		}
		// The process tree is settled on EVERY exit path — success included —
		// because a leader that exited says nothing about descendants still
		// holding the group id (modelruntime.Proc.Stop's whole contract).
		stopErr := proc.Stop(10 * time.Second)
		workerOutput := combinedWorkerOutput(stdout.bytes(), stderr.bytes())
		if stopErr != nil {
			cleanupScratch = false
			// A process tree whose stop is not confirmed can still mutate the
			// worktree. Close this candidate to fallback, retain its output, and
			// tell the driver to leave the attempt claimed so its locks remain
			// held. Pausing here would release file locks under a possibly-live
			// worker.
			reason := fmt.Sprintf("worker process group %d stop was not confirmed (%v); output retained and side effects remain unknown", proc.PID(), stopErr)
			opts.logf("workflow: step %s: %s", stepID, reason)
			_ = chain.MarkNoFallback(sel, reason)
			writeLog(opts.Log, workerOutput, chain)
			return out, &RecoverableExecutionError{
				Detail:      "worker process group did not stop; no result was processed and no fallback candidate was started: " + stopErr.Error(),
				RetainClaim: true,
			}
		}

		if stepCtx.Err() != nil {
			writeLog(opts.Log, workerOutput, chain)
			if ctx.Err() != nil {
				return out, fmt.Errorf("workflow worker stopped: %w", ctx.Err())
			}
			return out, &RecoverableExecutionError{Detail: fmt.Sprintf("workflow step %s timed out after %s; process group stopped, invocation remains open for recovery", stepID, timeout)}
		}

		// Exit status is transport metadata, not the semantic result. Some
		// harnesses exit nonzero after emitting a complete structured result;
		// accept it only if it decodes, matches the minted identity, and the
		// server's semantic validator records it. Conversely, never reroll a
		// started worker merely because it exited nonzero: malformed or absent
		// output leaves possible worktree side effects and must be repaired from
		// a recoverable hold.
		if runErr != nil {
			opts.logf("workflow: step %s: worker exited nonzero after start (%v); validating its structured result before deciding the hold", stepID, runErr)
		}
		var result workflow.StepResult
		if err := json.Unmarshal(bytes.TrimSpace(stdout.bytes()), &result); err != nil {
			if runErr == nil {
				// A clean exit that still broke the output contract: the
				// worker's own act, never something to reroll on another
				// candidate.
				reason := fmt.Sprintf("process group %d stopped after a clean worker exit; structured output was invalid (%v); stdout/stderr retained, worktree not reset, and side effects may exist", proc.PID(), err)
				_ = chain.MarkNoFallback(sel, reason)
				writeLog(opts.Log, workerOutput, chain)
				return out, &RecoverableExecutionError{Detail: "worker did not return a valid structured result; fallback is forbidden after start: " + err.Error()}
			}
			// A started failure with no decodable result is classified BEFORE
			// any fallback. Only a failure this controller can POSITIVELY
			// attribute to the channel — never to the worker's own exit — may
			// advance the chain, and only on a read-only dispatch, where the
			// confirmed-empty stopped tree provably leaves no unresolved
			// worktree side effects and the stop plus that inspection are
			// recorded as explicit controller-valid reconcile evidence. A
			// nonzero or unknown exit, or a channel failure on a dispatch that
			// may have written, stops the step with the side-effect
			// reconciliation required.
			if positivelyKnownChannelFailure(runErr) && stepCtx.Err() == nil && inv.Grant.Authority == workflow.AuthorityReadOnly {
				evidence := fmt.Sprintf(
					"process group %d stopped and observed empty after a positively known channel failure (%v); the dispatch carried the controller-granted read_only authority whose carrier preflight enforces, so the stopped tree leaves no unresolved worktree side effects; combined worker output retained in the step log",
					proc.PID(), runErr)
				opts.logf("workflow: step %s: candidate %s/%s lost its channel after start; tree stopped and side effects reconciled on the read-only dispatch, falling back", stepID, sel.Candidate.Harness, sel.Candidate.Model)
				if mErr := chain.MarkInfrastructureFailure(sel, "channel failure after start: "+runErr.Error()); mErr != nil {
					writeLog(opts.Log, workerOutput, chain)
					return out, fmt.Errorf("record infrastructure failure: %w", mErr)
				}
				if mErr := chain.Reconciled(sel, evidence); mErr != nil {
					writeLog(opts.Log, workerOutput, chain)
					return out, fmt.Errorf("record reconciliation: %w", mErr)
				}
				writeLog(opts.Log, workerOutput, chain)
				continue
			}
			if positivelyKnownChannelFailure(runErr) {
				// Positively a channel failure, but this dispatch may have
				// written to the worktree before the channel broke (or the step
				// deadline fired with it): the controller cannot prove the
				// worktree free of side effects, so it reconciles nothing and no
				// fallback may run over the unresolved state.
				reason := fmt.Sprintf("process group %d stopped after a positively known channel failure (%v); this dispatch's authority is %q, worktree side effects may exist and none are reconciled; side-effect reconciliation is required before another candidate may run", proc.PID(), runErr, inv.Grant.Authority)
				_ = chain.MarkInfrastructureFailure(sel, reason)
				writeLog(opts.Log, workerOutput, chain)
				return out, &RecoverableExecutionError{Detail: "worker channel failed after start with no structured result; fallback is forbidden and side-effect reconciliation is required: " + runErr.Error()}
			}
			// Nonzero or unknown exit: the worker ended its own process, and
			// an exit code's meaning belongs to the worker — this controller
			// cannot positively attribute the death to infrastructure or the
			// channel, so it refuses to guess, never rerolls, and stops with
			// the side-effect reconciliation required.
			reason := fmt.Sprintf("process group %d stopped after a nonzero or unknown worker failure (%v) with no decodable structured result (%v); not positively classifiable as infrastructure or channel; worktree not reset and side effects may exist; side-effect reconciliation is required before another candidate may run", proc.PID(), runErr, err)
			_ = chain.MarkInfrastructureFailure(sel, reason)
			writeLog(opts.Log, workerOutput, chain)
			return out, &RecoverableExecutionError{Detail: fmt.Sprintf("worker exited nonzero after start (%v) without a valid structured result; fallback is forbidden and side-effect reconciliation is required: %v", runErr, err)}
		}
		// The worker cannot choose its own identity; refuse contradictions
		// rather than silently rewriting them into a valid server result.
		if result.WorkItemID != wiID || result.FlowVersion != inv.StepsVersion || result.StepID != stepID ||
			result.StepAttemptID != inv.StepAttemptID || result.Epoch != int(inv.ClaimEpoch) || result.ProducerID != inv.ProducerID {
			reason := fmt.Sprintf("process group %d stopped; worker result identity differs from the invocation; output retained, worktree not reset, and side effects may exist", proc.PID())
			_ = chain.MarkNoFallback(sel, reason)
			writeLog(opts.Log, workerOutput, chain)
			return out, &RecoverableExecutionError{Detail: "worker result identity differs from invocation; fallback is forbidden after start"}
		}

		// Controller-side artifact sink (aihub#725): a credentialless worker
		// cannot call pf_save_artifact — it has no MCP tools and never sees the
		// attempt credentials — so a completed result with NO artifact triple
		// would otherwise be unrecordable. The controller, which DOES hold the
		// claim-time credentials, saves the worker's transient structured
		// output object as a methodology artifact through the same
		// attempt-authorized path pf_save_artifact uses, then fills the
		// server-returned immutable identity plus the canonical hash into the
		// result before recording. Everything the server validates afterwards
		// — existence, wi-binding, type, version, visibility, digest over the
		// STORED structured payload — still holds, because the id/version are
		// real server-returned values and the hash is computed over the exact
		// bytes sent. A result that already carries a triple is NOT rewritten:
		// the sink never replaces a worker-supplied (or fabricated) id, so the
		// server's existing refusal stays observable. Non-completed results
		// bypass the sink entirely and keep their byte-identical behavior.
		// sunkID names the artifact the sink stored, for the record-failure
		// report below: a save that succeeded is a side effect the operator
		// must see separately from the failed recording.
		var sunkID string
		// 🔴 The trigger is the ZERO triple, not a bare empty id (aihub#725
		// review_fix SF1): a result whose artifact carries any populated
		// component — an id-less fragment like {version:1,hash:...} — is a
		// worker-supplied (or malformed) reference the sink must NOT overwrite;
		// it records unchanged and the server's resolution refuses it. Only the
		// fully-zero triple means "no artifact was cited at all".
		if result.Status == workflow.StatusCompleted && result.Artifact == (workflow.ArtifactRef{}) {
			if result.Output == nil {
				// A completed result that cites no artifact and carries no
				// structured output object: nothing could be stored, so nothing
				// is recorded and no fallback may run over the possibly-dirty
				// worktree — the same discipline as any other post-start
				// contract breach.
				reason := fmt.Sprintf("process group %d stopped; the completed result cited no artifact and carried no structured output object to sink; output retained, worktree not reset, and side effects may exist", proc.PID())
				_ = chain.MarkNoFallback(sel, reason)
				writeLog(opts.Log, workerOutput, chain)
				return out, &RecoverableExecutionError{Detail: "completed worker result carries neither an artifact triple nor a structured output object; the sink cannot store it and fallback is forbidden after start"}
			}
			ref, sErr := sinkWorkerOutput(stepCtx, api, wiID, inv, opts, result, contract)
			if sErr != nil {
				// The save was refused or unconfirmed. Because a lost response
				// cannot be distinguished from a refused one, the once-guard
				// below has already burned this invocation's sink: a retry of
				// the SAME invocation will never save again, so no duplicate
				// artifact can be created by a retry loop. Nothing was recorded.
				reason := fmt.Sprintf("process group %d stopped; controller-sink artifact save was refused or unconfirmed (%v); no workflow result was recorded; output retained, worktree not reset, and side effects may exist", proc.PID(), sErr)
				_ = chain.MarkNoFallback(sel, reason)
				writeLog(opts.Log, workerOutput, chain)
				return out, &RecoverableExecutionError{Detail: "controller artifact sink: " + sErr.Error()}
			}
			result.Artifact = ref
			sunkID = ref.ID
		}
		// The transient output object never leaves this process: it exists for
		// the sink alone, and the recorded result must stay byte-identical to
		// the pre-sink wire shape (omitempty keeps a nil map off the JSON).
		result.Output = nil
		if _, err := api.RecordWorkflowResult(stepCtx, wiID, map[string]any{
			"attempt_id":     opts.Credentials.AttemptID,
			"claim_epoch":    opts.Credentials.ClaimEpoch,
			"session_secret": opts.Credentials.SessionSecret,
			"result":         result,
		}); err != nil {
			detail := "record workflow result: " + err.Error()
			if sunkID != "" {
				// aihub#725: the sink already committed an artifact server-side.
				// Sink success is NOT step completion, and the two outcomes stay
				// distinguishable: the held invocation must not re-save (the
				// once-guard has burned), and recovery re-records against the
				// known artifact id rather than silently creating another one.
				detail = fmt.Sprintf("record workflow result: %v; the controller-sink artifact WAS stored as %s (version %d, hash %s) and result recording was NOT confirmed", err, sunkID, result.Artifact.Version, result.Artifact.Hash)
			}
			reason := fmt.Sprintf("process group %d stopped; the structured result was not confirmed recorded (%v); output retained, worktree not reset, and side effects may exist", proc.PID(), err)
			_ = chain.MarkNoFallback(sel, reason)
			writeLog(opts.Log, workerOutput, chain)
			return out, &RecoverableExecutionError{Detail: detail}
		}
		if aErr := chain.Apply(sel, result); aErr != nil {
			writeLog(opts.Log, combinedWorkerOutput(stdout.bytes(), stderr.bytes()), chain)
			return out, fmt.Errorf("record candidate outcome in the fallback chain: %w", aErr)
		}
		if modelruntime.ClassifyOutcome(result) == modelruntime.OutcomeInfrastructure {
			if inv.Grant.Authority == workflow.AuthorityReadOnly {
				// A read-only dispatch provably leaves no worktree side effects,
				// and the stop was confirmed empty above, so the reconciliation
				// is complete and records as controller-valid evidence.
				evidence := fmt.Sprintf(
					"process group %d stopped and observed empty after the structured provider_error; the dispatch carried the controller-granted read_only authority whose carrier preflight enforces, so the stopped tree leaves no unresolved worktree side effects; stdout/stderr retained in the step log",
					proc.PID())
				if aErr := chain.Reconciled(sel, evidence); aErr != nil {
					writeLog(opts.Log, workerOutput, chain)
					return out, fmt.Errorf("reconcile structured provider_error: %w", aErr)
				}
			}
			// Under a write grant the provider_error'd run may have left
			// partial worktree effects this controller cannot inspect away, so
			// the reconcile requirement stays OPEN in the chain record — the
			// exact obligation the explicit retry authorization (run.go)
			// exists to drive. The old generic warning ("the worktree was not
			// reset and may carry partial effects") is an admission of
			// UNRESOLVED side effects, not evidence of an inspection: it is
			// not controller-valid and must never qualify as reconcile
			// evidence.
		}
		writeLog(opts.Log, workerOutput, chain)
		out.Result = result
		out.Recorded = true
		return out, nil
	}
}

// sinkAttempts is the controller-side fast half of aihub#725 S3's
// idempotency: a per-invocation once-guard keyed on the server-minted
// step_attempt_id. It is marked BEFORE the save call, not after it, so an
// attempt whose response was lost (a save that may or may not have committed
// server-side) can never produce a second save for the same invocation from
// THIS process.
//
// 🔴 Since aihub#725 review_fix B2 the durability no longer rests on this map:
// every sink save carries attrs.controller_sink_receipt =
// {invocation_id, step_attempt_id, attempt_id}, and the server replays that
// receipt durably — the same receipt with the same payload returns the
// already-stored row, a different payload is refused — so a sink retried
// after a controller restart (or on another process) cannot mint a duplicate
// artifact either. The map remains as the in-process fast path: it prevents
// the second HTTP save from being issued at all, keeps the error path
// explicit, and stays correct in exactly the process-local scope it claims.
var sinkAttempts sync.Map // step_attempt_id -> struct{}

// claimSinkAttempt reports whether THIS invocation's sink save may proceed,
// and burns the once-token either way: a second claim for the same
// step_attempt_id always reports false.
func claimSinkAttempt(stepAttemptID string) bool {
	_, attempted := sinkAttempts.LoadOrStore(stepAttemptID, struct{}{})
	return !attempted
}

// sinkArtifactType derives the methodology type from the pinned skill
// contract's capability set — trusted controller metadata, never a
// worker-supplied value. A review or verification-capability gate reviews
// work, so its sink artifact is methodology.review; every other automatic
// completed step's output is methodology.execute.
func sinkArtifactType(contract *skillregistry.SkillContract) string {
	for _, capability := range contract.Capabilities {
		if capability == skillregistry.CapReview || capability == skillregistry.CapVerification {
			return "methodology.review"
		}
	}
	return "methodology.execute"
}

// sinkWorkerOutput stores one artifactless completed result's transient
// structured output as a methodology artifact and returns the artifact triple
// to fill into the recorded result: the server-returned id/version plus the
// digest computed over the EXACT bytes sent as structured_payload. The
// content is the controller's own honest summary carrying the
// controller-sink provenance declaration — origin and payload author stay
// distinguishable from a worker-authored artifact.
//
// Number fidelity (aihub#725 review_fix SF2/SF4): the payload is hashed and
// stored VERBATIM — the strict decode preserved the worker's number literals
// as json.Number, json.Marshal writes them back unchanged, and the server
// stores those same bytes and recomputes the digest over a UseNumber decode
// of them. There is deliberately NO float64 canonicalization pass: the sink
// previously re-marshalled the payload through the float64 universe, which
// rewrote literals the record-time recompute treats as distinct (1.50 → 1.5,
// integers beyond 2^53 rounding away their last digit) and made the content's
// "verbatim" provenance claim false. The residual edge is declared, not
// hidden: a number written in exponent spelling (1e2, 1E+2) is normalized by
// PostgreSQL's jsonb column on store, so such a payload fails the record-time
// digest recompute LOUDLY (a recoverable hold, never a silent divergence) —
// every decimal and integer literal round-trips exactly.
func sinkWorkerOutput(ctx context.Context, api API, wiID string, inv Invocation, opts StepOptions, result workflow.StepResult, contract *skillregistry.SkillContract) (workflow.ArtifactRef, error) {
	if !claimSinkAttempt(inv.StepAttemptID) {
		return workflow.ArtifactRef{}, fmt.Errorf(
			"a sink save was already attempted for invocation %s of step %s in this process; it will not be retried here (the server-side durable receipt would return the same row, but a lost response cannot be told from a refused one)",
			inv.StepAttemptID, inv.StepID)
	}
	// The bytes ARE the canonical form: json.Marshal of the UseNumber-decoded
	// output re-serializes each json.Number as its literal, sorts keys
	// deterministically, and nothing in this path has touched a float64. The
	// digest is taken over these exact bytes, and these exact bytes are what
	// the request sends as structured_payload — so the server's record-time
	// recompute over the stored copy reproduces it.
	payloadBytes, err := json.Marshal(result.Output)
	if err != nil {
		return workflow.ArtifactRef{}, fmt.Errorf("encode worker structured output: %w", err)
	}
	hash := WorkflowArtifactHashBytes(payloadBytes)
	artifactType := sinkArtifactType(contract)
	content := fmt.Sprintf(
		"Controller-sink artifact (aihub#725): step %q of %s completed without an artifact triple; the workflow controller stored the worker's structured output object byte-for-byte (number literals as decoded from the worker envelope) in attrs.structured_payload.\n\n"+
			"Provenance — origin=controller_sink, payload_author=worker. Invocation identity: work_item_id=%s, flow_version=%d, step_id=%s, step_attempt_id=%s, epoch=%d, producer_id=%s. Durable replay receipt: invocation_id=%s, attempt_id=%s. Artifact type=%s; payload digest=%s.\n\n"+
			"The payload was authored by the worker and saved by the controller under the claim-time attempt credential; the controller filled the server-returned artifact triple into the recorded result. Server-side result validation remains authoritative, and a successful save is not by itself step completion.",
		inv.StepID, wiID,
		wiID, inv.StepsVersion, inv.StepID, inv.StepAttemptID, inv.ClaimEpoch, inv.ProducerID,
		inv.InvocationID, opts.Credentials.AttemptID,
		artifactType, hash)
	saved, err := api.SaveArtifact(ctx, client.SaveArtifactRequest{
		Type:              artifactType,
		WorkItemID:        wiID,
		Content:           content,
		StructuredPayload: json.RawMessage(payloadBytes),
		AttemptID:         opts.Credentials.AttemptID,
		ClaimEpoch:        opts.Credentials.ClaimEpoch,
		SessionSecret:     opts.Credentials.SessionSecret,
		Visibility:        "project",
		// The durable replay receipt (aihub#725 review_fix B2): the server
		// resolves it in attrs, so a same-payload retry after a lost response
		// returns the row already stored instead of minting a duplicate
		// artifact. All three components are trusted controller state.
		SinkReceipt: &client.ControllerSinkReceipt{
			InvocationID:  inv.InvocationID,
			StepAttemptID: inv.StepAttemptID,
			AttemptID:     opts.Credentials.AttemptID,
		},
	})
	if err != nil {
		return workflow.ArtifactRef{}, fmt.Errorf("save methodology artifact for step %s: %w", inv.StepID, err)
	}
	return workflow.ArtifactRef{ID: saved.ID, Version: saved.Version, Hash: hash}, nil
}

// positivelyKnownChannelFailure reports whether a post-start worker error is
// POSITIVELY attributable to the controller↔worker channel rather than to
// the worker's own exit decision — the only failure class this controller may
// classify as infrastructure/channel without guessing. proc.Wait returns
// *exec.ExitError exactly when the worker's own process ended itself: a
// nonzero exit code, or termination by a signal whose sender this controller
// cannot identify (it has not signalled yet — Stop runs after Wait). Every
// OTHER error is a parent-side transport failure: an I/O failure draining
// the worker's output pipes, or the bounded pipe drain expiring because an
// orphaned descendant held the channel open (os/exec's WaitDelay). Those are
// failures of machinery this controller owns, positively known by error TYPE
// — never by inference from an exit code, whose meaning belongs to the
// worker and stays unknown here. Unknown deaths never fall back (aihub#708
// Batch 2B blocker #1): they stop with the side-effect reconciliation
// required.
func positivelyKnownChannelFailure(runErr error) bool {
	var exitErr *exec.ExitError
	return runErr != nil && !errors.As(runErr, &exitErr)
}

// RecoverableExecutionError reports a started invocation that cannot safely
// advance to another candidate. RetainClaim is set when the worker process
// tree could not be confirmed stopped: callers must not pause the attempt in
// that case because pausing releases file locks while the worker may still be
// mutating the worktree.
type RecoverableExecutionError struct {
	Detail      string
	RetainClaim bool
}

func (e *RecoverableExecutionError) Error() string { return e.Detail }

// outputCollector is a mutex-protected collector for one worker output
// stream. os/exec hands the worker a pipe and copies it into the writer in a
// goroutine of its own, and that copy can still be running AFTER Proc.Stop
// returns: Stop's success means the process GROUP is empty, which says
// nothing about the pipes — bytes the tree wrote before dying may still be
// in flight in the pipe, and the bounded WaitDelay drain keeps the copy
// goroutine alive after the leader is reaped. RunStep reads the streams back
// on the step-deadline branch without ever waiting for Proc.Wait, so with a
// raw bytes.Buffer the read and the pipe-copy goroutine's Write touched the
// same memory with no happens-before edge between them (the CI -race
// report). The collector closes that gap at the data structure: every Write
// takes the mutex, and every read is bytes(), an atomic snapshot taken under
// the same mutex — the two goroutines can never touch the same bytes
// unsynchronized, no matter which returns first.
//
// One collector per stream, deliberately: stdout's entire contract is one
// JSON StepResult while harnesses narrate progress on stderr, and the step
// log labels the two sections separately (combinedWorkerOutput) — merging
// the streams would let stderr narration break the stdout parse. The
// collector changes no fallback, stop or lifecycle semantics: it is only the
// buffering between the pipe and the snapshot.
type outputCollector struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

// Write appends one drained chunk under the collector's mutex. It is the
// only method the os/exec pipe-copy goroutine ever calls.
func (c *outputCollector) Write(p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.buf.Write(p)
}

// bytes returns an atomic snapshot of everything drained so far, as a
// detached copy: taken under the mutex, so a concurrent Write can neither
// tear it nor run unsynchronized with it, and copied out, so later Writes can
// never alias the returned slice. RunStep takes a fresh snapshot before every
// log write and every stdout parse rather than holding a stale slice across
// code that may still be racing the pipe drain.
func (c *outputCollector) bytes() []byte {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]byte, c.buf.Len())
	copy(out, c.buf.Bytes())
	return out
}

// combinedWorkerOutput retains diagnostics in the step log without polluting
// stdout, whose entire contract is one JSON StepResult. Harnesses routinely
// write progress to stderr; parsing a combined stream would reject valid JSON.
func combinedWorkerOutput(stdout, stderr []byte) []byte {
	var b bytes.Buffer
	if len(stderr) > 0 {
		b.WriteString("[worker stderr]\n")
		b.Write(stderr)
		if stderr[len(stderr)-1] != '\n' {
			b.WriteByte('\n')
		}
	}
	b.WriteString("[worker stdout]\n")
	b.Write(stdout)
	return b.Bytes()
}

// writeLog appends the worker output and the fallback-chain record to the
// step log buffer, so the retained bytes are the whole story of the attempt.
func writeLog(log *bytes.Buffer, output []byte, chain *modelruntime.Fallback) {
	if log == nil {
		return
	}
	log.Write(output)
	for _, rec := range chain.Records() {
		fmt.Fprintf(log, "\n[modelruntime] candidate %d %s/%s@%s: %s (%s)\n",
			rec.Index, rec.Candidate.Harness, rec.Candidate.Model, rec.Candidate.Effort, rec.State, rec.Reason)
	}
}

// isPausedConflict reports whether a workflow API error is the server telling
// the controller the attempt was paused out from under it (a human pause, or
// the atomic review-FAIL pause). Typed on the error string because the
// workflow routes return plain conflict codes; the classification is used
// only to choose an ENDING, never to retry.
func isPausedConflict(err error) bool {
	if err == nil {
		return false
	}
	s := strings.ToLower(err.Error())
	return strings.Contains(s, "work item status is \"paused\"") ||
		strings.Contains(s, "attempt is paused") ||
		strings.Contains(s, "attempt_paused")
}

// ErrInvocationOpenLive is the typed error for a start refusal that names an
// open invocation of a LIVE attempt — the controller's own in-flight
// invocation, which only recording a result or the attempt ending can close.
var ErrInvocationOpenLive = errors.New("workflow: step has an open invocation of a live attempt; record its result or end the attempt before starting another")

// UnsupportedDispatchError is the typed refusal for a step this machine
// cannot dispatch at all: every pinned candidate was refused before a result
// was recorded. It is a property of the machine's [[models]] catalog and the
// harnesses installed on it — never of the work item — so callers report it
// as recoverable data (drain pauses the attempt; `engine workflow` prints the
// machine-readable form) instead of retrying it or falling back to the
// legacy scenario path, which a workflow-bearing work item does not have.
type UnsupportedDispatchError struct {
	WorkItemID string
	StepID     string
	Reason     string
	Detail     string
}

func (e *UnsupportedDispatchError) Error() string {
	return fmt.Sprintf("workflow: step %s of %s cannot be dispatched on this machine (%s): %s",
		e.StepID, e.WorkItemID, e.Reason, e.Detail)
}
