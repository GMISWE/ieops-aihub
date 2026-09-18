package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/GMISWE/ieops-aihub/internal/config"
	"github.com/GMISWE/ieops-aihub/internal/controller"
	"github.com/GMISWE/ieops-aihub/internal/engine"
	"github.com/GMISWE/ieops-aihub/internal/roles"
	"github.com/GMISWE/ieops-aihub/internal/workflow"
	"github.com/GMISWE/ieops-aihub/pkg/client"
)

// RunEngine dispatches `polyforge engine <verb> [flags...]`. Every verb here is local-only (no
// aihub API client, no network call) and prints one JSON object to stdout on success — these are
// the CLI-layer wrapping of internal/engine's pieces (a)-(e) for a future headless orchestrator
// (aihub#654); today's LLM-driven pf-execute loop keeps calling those MCP tools directly.
// ONE exception, aihub#708: `engine workflow` is the DB-workflow adapter. It
// exposes a real three-mode protocol: drain owns unattended runs; --continue
// drives automatic steps through controller.RunStep; --prepare/--submit hand
// an exact fenced invocation to the main human session. It never claims or
// fabricates approval, and workflow-bearing work never falls back to scenarios.
func RunEngine(ctx context.Context, args []string) {
	const usage = "usage: polyforge engine <startup|resolve-role|parse-review|bracket-plan|cleanup-worktrees|workflow> [flags...]"
	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, usage)
		os.Exit(1)
	}
	verb := args[0]
	rest := args[1:]

	var (
		out any
		err error
	)
	switch verb {
	case "startup":
		out, err = runEngineStartup(ctx, rest)
	case "resolve-role":
		out, err = runEngineResolveRole(rest)
	case "parse-review":
		out, err = runEngineParseReview(rest)
	case "bracket-plan":
		out, err = runEngineBracketPlan(rest)
	case "cleanup-worktrees":
		out, err = runEngineCleanupWorktrees(ctx, rest)
	case "workflow":
		out, err = runEngineWorkflow(ctx, rest)
	default:
		fmt.Fprintf(os.Stderr, "engine: unknown verb %q\n%s\n", verb, usage)
		os.Exit(1)
	}
	if err != nil {
		// Typed controller holds are RESULTS as well as errors: keep the nonzero
		// exit, but expose stable JSON so a caller can route rather than parsing
		// prose or (worse) treating the gate as locally executable.
		var sessionHold *controller.SessionWorkerRequiredError
		if errors.As(err, &sessionHold) {
			if b, merr := json.MarshalIndent(map[string]any{
				"work_item_id": sessionHold.WorkItemID,
				"step_id":      sessionHold.StepID,
				"status":       "hold",
				"reason":       sessionHold.Reason,
				"detail":       sessionHold.Detail,
			}, "", "  "); merr == nil {
				fmt.Println(string(b))
			}
		}
		// A typed unsupported dispatch is a RESULT, not prose: print the
		// machine-readable form on stdout (with the stable reason token) so
		// callers can branch on it, and still exit 1 — unsupported is a
		// failure, and the typed JSON exists so nobody "solves" it by falling
		// back to the legacy scenario path.
		var uns *controller.UnsupportedDispatchError
		if errors.As(err, &uns) {
			if b, merr := json.MarshalIndent(map[string]any{
				"work_item_id": uns.WorkItemID,
				"step_id":      uns.StepID,
				"status":       "unsupported",
				"reason":       uns.Reason,
				"detail":       uns.Detail,
			}, "", "  "); merr == nil {
				fmt.Println(string(b))
			}
		}
		fmt.Fprintf(os.Stderr, "engine %s: %v\n", verb, err)
		os.Exit(1)
	}

	b, merr := json.MarshalIndent(out, "", "  ")
	if merr != nil {
		fmt.Fprintf(os.Stderr, "engine %s: marshal result: %v\n", verb, merr)
		os.Exit(1)
	}
	fmt.Println(string(b))
}

// flagValue extracts "--name=value" from args, returning ("", false) when absent. Mirrors
// machine_user.go's hand-rolled strings.HasPrefix convention rather than the flag package: every
// engine verb takes only "--x=y" flags and (for bracket-plan) one bare boolean flag, no
// positional sub-subcommand.
func flagValue(args []string, name string) (string, bool) {
	prefix := "--" + name + "="
	for _, a := range args {
		if strings.HasPrefix(a, prefix) {
			return strings.TrimPrefix(a, prefix), true
		}
	}
	return "", false
}

func hasFlag(args []string, name string) bool {
	needle := "--" + name
	for _, a := range args {
		if a == needle {
			return true
		}
	}
	return false
}

// execGitRunner builds a production engine.GitRunner backed by os/exec. dir is passed as the
// subprocess's working directory (cmd.Dir) exactly as given by the caller: for most verbs that is
// a scenario clone or worktree path, and for CleanupWorktrees's per-repo removal it is
// deliberately the repo's MAIN CLONE directory, not the worktree being removed (see
// internal/engine/wrap.go's doc comment for why: `git worktree remove` run from an
// already-deleted worktree directory fails at exec's own chdir before git ever runs).
func execGitRunner(ctx context.Context) engine.GitRunner {
	return func(dir string, args ...string) (string, error) {
		cmd := exec.CommandContext(ctx, "git", args...)
		cmd.Dir = dir
		var stdout, stderr bytes.Buffer
		cmd.Stdout = &stdout
		cmd.Stderr = &stderr
		if err := cmd.Run(); err != nil {
			msg := strings.TrimSpace(stderr.String())
			if msg == "" {
				msg = err.Error()
			}
			return "", fmt.Errorf("git -C %s %s: %s", dir, strings.Join(args, " "), msg)
		}
		return stdout.String(), nil
	}
}

// --- engine startup ---------------------------------------------------------------------------

// engineStartupStep is one template step plus its @include-expanded form, as returned by
// `engine startup`.
type engineStartupStep struct {
	ID       string `json:"id"`
	Content  string `json:"content"`
	Expanded string `json:"expanded"`
}

// engineStartupOutput is `engine startup`'s stdout shape.
type engineStartupOutput struct {
	ScenarioPath   string              `json:"scenario_path"`
	LegacyFallback bool                `json:"legacy_fallback"`
	SHA            string              `json:"sha"`
	TemplateSource string              `json:"template_source"`
	Steps          []engineStartupStep `json:"steps"`
}

// runEngineStartup runs internal/engine's six-step startup sequence (engine-native-details.md
// §0 pieces (a)-(e)) against a real git-backed GitRunner, then writes .pf_meta.json under
// --worktree-root — that file write is deliberately the CLI layer's job, not internal/engine's
// (internal/engine/startup.go's PinScenarioSHA doc comment).
func runEngineStartup(ctx context.Context, args []string) (*engineStartupOutput, error) {
	workspaceRoot, _ := flagValue(args, "workspace-root")
	worktreeRoot, _ := flagValue(args, "worktree-root")
	scenarioURL, _ := flagValue(args, "scenario-url")
	wiType, _ := flagValue(args, "wi-type")
	project, _ := flagValue(args, "project")

	if workspaceRoot == "" || worktreeRoot == "" || scenarioURL == "" || wiType == "" {
		return nil, fmt.Errorf("--workspace-root, --worktree-root, --scenario-url and --wi-type are all required")
	}

	git := execGitRunner(ctx)

	// gitDirExists is the W3-fixed presence probe engine.ResolveScenarioPath now requires: a
	// filesystem os.Stat of <path>/.git, matching syncScenarioClone's own probe (init.go), never
	// `git -C <path> rev-parse --git-dir` - that command succeeds (and reports an ENCLOSING
	// repo's .git) for an existing-but-not-a-repo directory nested inside any git work tree,
	// which is exactly this workspace's own shape (.repo/ sits inside a git work tree).
	gitDirExists := func(path string) bool {
		_, statErr := os.Stat(filepath.Join(path, ".git"))
		return statErr == nil
	}

	scenarioPath, legacy, err := engine.ResolveScenarioPath(git, gitDirExists, workspaceRoot, scenarioURL)
	if err != nil {
		return nil, err
	}
	sha, err := engine.PinScenarioSHA(git, scenarioPath)
	if err != nil {
		return nil, err
	}
	content, source, err := engine.ResolveTemplate(git, scenarioPath, sha, wiType, project)
	if err != nil {
		return nil, err
	}
	steps, err := engine.ScanSteps(content)
	if err != nil {
		return nil, err
	}

	fetch := func(path string) (string, error) {
		return git(scenarioPath, "show", sha+":"+path)
	}

	out := &engineStartupOutput{
		ScenarioPath:   scenarioPath,
		LegacyFallback: legacy,
		SHA:            sha,
		TemplateSource: source,
		Steps:          make([]engineStartupStep, 0, len(steps)),
	}
	for _, s := range steps {
		expanded, eerr := engine.ExpandIncludes(s.Content, fetch)
		if eerr != nil {
			return nil, fmt.Errorf("expand includes for step %q: %w", s.ID, eerr)
		}
		out.Steps = append(out.Steps, engineStartupStep{ID: s.ID, Content: s.Content, Expanded: expanded})
	}

	meta := engine.ScenarioMeta{ScenarioSHA: sha, StartedAt: time.Now().UTC().Format(time.RFC3339)}
	metaBytes, merr := json.MarshalIndent(meta, "", "  ")
	if merr != nil {
		return nil, fmt.Errorf("marshal .pf_meta.json: %w", merr)
	}
	metaPath := filepath.Join(worktreeRoot, ".pf_meta.json")
	if werr := os.WriteFile(metaPath, metaBytes, 0o644); werr != nil {
		return nil, fmt.Errorf("write %s: %w", metaPath, werr)
	}

	return out, nil
}

// --- engine resolve-role -----------------------------------------------------------------------

// engineResolveRoleOutput is `engine resolve-role`'s stdout shape. UnknownDeclaredRole is set
// (W6) only when --declared-role was given but named no role in the catalog: ResolveRole still
// falls through non-fatally to tiers 2/3 per spec, but the typo is surfaced here instead of
// vanishing the moment source != "declared" (the CLI's caller can log or event it).
type engineResolveRoleOutput struct {
	Role                string `json:"role"`
	Tier                string `json:"tier"`
	ReadOnly            bool   `json:"read_only"`
	Source              string `json:"source"`
	UnknownDeclaredRole string `json:"unknown_declared_role,omitempty"`
}

// runEngineResolveRole loads the real internal/roles catalog and runs engine.ResolveRole's
// three-tier fallback against it — the catalog is consumed verbatim, never re-implemented here.
func runEngineResolveRole(args []string) (*engineResolveRoleOutput, error) {
	stepID, _ := flagValue(args, "step-id")
	declaredRole, _ := flagValue(args, "declared-role")
	if stepID == "" {
		return nil, fmt.Errorf("--step-id is required")
	}

	catalog, err := roles.LoadRoles()
	if err != nil {
		return nil, fmt.Errorf("load roles catalog: %w", err)
	}
	role, source, unknownDeclared, err := engine.ResolveRole(catalog, stepID, declaredRole)
	if err != nil {
		return nil, err
	}
	return &engineResolveRoleOutput{
		Role:                role.Name,
		Tier:                role.Tier,
		ReadOnly:            role.Capability.ReadOnly,
		Source:              string(source),
		UnknownDeclaredRole: unknownDeclared,
	}, nil
}

// --- engine parse-review -----------------------------------------------------------------------

// engineParseReviewOutput is `engine parse-review`'s stdout shape.
type engineParseReviewOutput struct {
	Result string `json:"result"`
}

// runEngineParseReview reads --file, or stdin when --file is absent, and runs
// engine.ParseReviewResult over it verbatim (last-marker-wins, WARN on absence).
func runEngineParseReview(args []string) (*engineParseReviewOutput, error) {
	file, _ := flagValue(args, "file")

	var data []byte
	var err error
	if file != "" {
		data, err = os.ReadFile(file)
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", file, err)
		}
	} else {
		data, err = io.ReadAll(os.Stdin)
		if err != nil {
			return nil, fmt.Errorf("read stdin: %w", err)
		}
	}

	result := engine.ParseReviewResult(string(data))
	return &engineParseReviewOutput{Result: string(result)}, nil
}

// --- engine bracket-plan -----------------------------------------------------------------------

// runEngineBracketPlan builds a BracketInput from flags and returns engine.PlanStepBracket's
// []StepCall verbatim — StepCall's own json tags (bracket.go) are the wire shape, no shadow type.
func runEngineBracketPlan(args []string) ([]engine.StepCall, error) {
	stepID, _ := flagValue(args, "step-id")
	status, _ := flagValue(args, "status")
	stepAttemptID, _ := flagValue(args, "step-attempt-id")
	nextStepID, _ := flagValue(args, "next-step-id")
	nextStepAttemptID, _ := flagValue(args, "next-step-attempt-id")
	artifactSummary, _ := flagValue(args, "artifact-summary")
	errorType, _ := flagValue(args, "error-type")
	supportsNextStep := hasFlag(args, "supports-next-step")

	if stepID == "" || status == "" || stepAttemptID == "" {
		return nil, fmt.Errorf("--step-id, --status and --step-attempt-id are all required")
	}
	if status != "completed" && status != "failed" {
		return nil, fmt.Errorf(`--status must be "completed" or "failed", got %q`, status)
	}

	return engine.PlanStepBracket(engine.BracketInput{
		StepID:            stepID,
		StepAttemptID:     stepAttemptID,
		Status:            status,
		ArtifactSummary:   artifactSummary,
		ErrorType:         errorType,
		NextStepID:        nextStepID,
		NextStepAttemptID: nextStepAttemptID,
		SupportsNextStep:  supportsNextStep,
	}), nil
}

// --- engine cleanup-worktrees ------------------------------------------------------------------

// engineCleanupWorktreesOutput is `engine cleanup-worktrees`'s stdout shape. Errors is always a
// non-nil (possibly empty) map so it serializes as {} rather than null on the all-succeeded path.
type engineCleanupWorktreesOutput struct {
	Removed []string          `json:"removed"`
	Errors  map[string]string `json:"errors"`
}

// runEngineCleanupWorktrees parses --worktrees (a JSON object mapping repo name to worktree
// path - pf_complete_attempt's own `worktrees` shape) and --workspace-root (W4: the workspace
// root whose <root>/.repo/<name> is each repo's main clone, the directory CleanupWorktrees now
// runs `git worktree remove` FROM, not the worktree being removed - see
// internal/engine/wrap.go's doc comment), and runs engine.CleanupWorktrees against a real
// git-backed GitRunner and an os.RemoveAll-backed rmParent guarded by an os.Stat isdir check
// (mirroring lifecycle-details.md §0's `if os.path.isdir(parent): rm -rf <parent>`).
//
// A CleanupWorktrees failure is deliberately NOT surfaced as a process error (which would only
// report one line on stderr and lose which repos DID get removed): cleanup is best-effort, so
// this verb always exits 0 and reports whatever succeeded in "removed" plus every failure in
// "errors" - one entry per repo whose `git worktree remove` failed (engine.CleanupWorktrees's
// repoErrs now carries ALL of them, not just the first - m5's fix, a side effect of the W4
// redesign), plus a "(parent)" entry when the shared-parent removal itself failed.
func runEngineCleanupWorktrees(ctx context.Context, args []string) (*engineCleanupWorktreesOutput, error) {
	workspaceRoot, ok := flagValue(args, "workspace-root")
	if !ok {
		return nil, fmt.Errorf("--workspace-root is required")
	}
	worktreesJSON, ok := flagValue(args, "worktrees")
	if !ok {
		return nil, fmt.Errorf("--worktrees is required (a JSON object mapping repo name to worktree path)")
	}
	var worktrees map[string]string
	if err := json.Unmarshal([]byte(worktreesJSON), &worktrees); err != nil {
		return nil, fmt.Errorf("--worktrees: invalid JSON map: %w", err)
	}

	git := execGitRunner(ctx)
	rmParent := func(path string) error {
		fi, statErr := os.Stat(path)
		if statErr != nil {
			if os.IsNotExist(statErr) {
				return nil // already gone: not an error, mirrors the pseudocode's isdir guard
			}
			return statErr
		}
		if !fi.IsDir() {
			return fmt.Errorf("%s exists and is not a directory", path)
		}
		return os.RemoveAll(path)
	}

	removed, repoErrs, parentErr := engine.CleanupWorktrees(git, rmParent, workspaceRoot, worktrees)
	out := &engineCleanupWorktreesOutput{
		Removed: removed,
		Errors:  make(map[string]string, len(repoErrs)+1),
	}
	if out.Removed == nil {
		out.Removed = []string{}
	}
	for name, rerr := range repoErrs {
		out.Errors[name] = rerr.Error()
	}
	if parentErr != nil {
		out.Errors["(parent)"] = parentErr.Error()
	}
	return out, nil
}

// --- engine workflow (aihub#708 Batch 2B: low-level DB-workflow adapter) ----------------------

// workflowClient is the client surface `engine workflow` needs: the shared
// controller API (workflow read, server-minted start, structured result
// record, pinned skill fetch) plus the explicit reconcile transition. The
// concrete *client.Client satisfies it; tests substitute a fake so the verb's
// state machine is exercised without a server.
type workflowClient interface {
	controller.SessionAPI
}

// engineWorkflowOutput is `engine workflow`'s stdout shape. Status is the
// shared controller's ActionKind vocabulary (run, open_invocation,
// repair_required, revision_required, approval_required, complete), plus "legacy" for a no-flow
// work item, "executed"'s follow-up state after --execute, and "reconciled"
// after --reconcile. A human gate is ALWAYS reported as data here — the verb
// never waits on stdin and never blocks the caller.
type engineWorkflowOutput struct {
	WorkItemID   string                          `json:"work_item_id"`
	Legacy       bool                            `json:"legacy"`
	StepsVersion int                             `json:"steps_version,omitempty"`
	Status       string                          `json:"status"`
	StepID       string                          `json:"step_id,omitempty"`
	RHS          bool                            `json:"rhs,omitempty"`
	Detail       string                          `json:"detail,omitempty"`
	Preparation  *controller.SessionPreparation  `json:"preparation,omitempty"`
	Submission   *controller.SessionSubmitResult `json:"submission,omitempty"`
	Approval     *engineWorkflowApproval         `json:"approval,omitempty"`
	Result       *engineWorkflowStepResult       `json:"result,omitempty"`
	Reconciled   map[string]any                  `json:"reconciled,omitempty"`
	WorkerTail   string                          `json:"worker_output_tail,omitempty"`
}

// engineWorkflowStepResult summarizes the recorded worker result of one
// --execute run. It is a projection of the structured workflow.StepResult the
// server already accepted — the full envelope lives on the work item's
// timeline, not in this output.
type engineWorkflowStepResult struct {
	Status        string `json:"status"`
	ReviewVerdict string `json:"review_verdict,omitempty"`
	StepAttemptID string `json:"step_attempt_id"`
	ArtifactID    string `json:"artifact_id,omitempty"`
	ArtifactHash  string `json:"artifact_hash,omitempty"`
}

type engineWorkflowApproval struct {
	StepsVersion int                  `json:"steps_version"`
	StepID       string               `json:"step_id"`
	Artifact     workflow.ArtifactRef `json:"artifact"`
	Instruction  string               `json:"instruction"`
}

// workerTailLimit bounds the worker output echoed into the verb's JSON: the
// full bytes belong in --log-file (or the work item timeline), not in a
// status report.
const workerTailLimit = 4096

// runEngineWorkflow implements `engine workflow`:
//
//	polyforge engine workflow --work-item=<id> --continue           # classify/drive the next action
//	polyforge engine workflow --work-item=<id> --prepare            # open a fenced main-session invocation
//	polyforge engine workflow --work-item=<id> --submit=<file>      # save artifact + record exact result
//	polyforge engine workflow --work-item=<id> --execute            # compatibility: run ONE automatic step
//	polyforge engine workflow --work-item=<id> --reconcile=<ra_...> # fence a dead attempt's open invocations
//
// --continue uses the shared controller for automatic steps and returns
// prepare_required for interactive/effective-RHS steps. --prepare returns the
// pinned immutable skill and concrete predecessor values; --submit validates
// and records the main session's authored output. Human approval remains a
// separate authenticated action and is always rendered with its exact artifact
// tuple; this verb never manufactures it.
func runEngineWorkflow(ctx context.Context, args []string) (*engineWorkflowOutput, error) {
	wiID, _ := flagValue(args, "work-item")
	if wiID == "" {
		return nil, fmt.Errorf("--work-item is required")
	}
	reconcileAttempt, hasReconcile := flagValue(args, "reconcile")
	submitFile, hasSubmit := flagValue(args, "submit")
	logFile, _ := flagValue(args, "log-file")
	workDir, _ := flagValue(args, "work-dir")

	c, err := workflowAihubClient()
	if err != nil {
		return nil, err
	}
	return engineWorkflowDrive(ctx, c, wiID, engineWorkflowOptions{
		Execute:          hasFlag(args, "execute"),
		Prepare:          hasFlag(args, "prepare"),
		Continue:         hasFlag(args, "continue"),
		SubmitFile:       submitFile,
		HasSubmit:        hasSubmit,
		ReconcileAttempt: reconcileAttempt,
		HasReconcile:     hasReconcile,
		LogFile:          logFile,
		WorkDir:          workDir,
	})
}

// engineWorkflowOptions carries `engine workflow`'s flags.
type engineWorkflowOptions struct {
	Execute          bool
	Prepare          bool
	Continue         bool
	SubmitFile       string
	HasSubmit        bool
	ReconcileAttempt string
	HasReconcile     bool
	LogFile          string
	WorkDir          string
}

// runWorkflowStep is the one-step dispatch seam engineWorkflowDrive runs
// `--execute` through. Production is the shared controller.RunStep (the
// SAME function the drain path uses — one execution policy across modes);
// tests substitute so the verb's state machine is exercised without a
// machine model catalog or a live harness.
var runWorkflowStep = controller.RunStep

// engineWorkflowDrive is the verb's testable core: everything after client
// construction. fail-closed throughout — a workflow read error is returned,
// never interpreted as "no workflow".
func engineWorkflowDrive(ctx context.Context, c workflowClient, wiID string, opts engineWorkflowOptions) (*engineWorkflowOutput, error) {
	out := &engineWorkflowOutput{WorkItemID: wiID}
	modes := 0
	for _, set := range []bool{opts.Execute, opts.Prepare, opts.Continue, opts.HasSubmit, opts.HasReconcile} {
		if set {
			modes++
		}
	}
	if modes > 1 {
		return out, fmt.Errorf("choose exactly one of --execute, --prepare, --submit, --continue, or --reconcile")
	}

	if opts.HasReconcile {
		cred, err := workflowCredentials(wiID)
		if err != nil {
			return out, err
		}
		resp, err := c.ReconcileWorkflowInvocations(ctx, wiID, map[string]any{
			"attempt_id":           cred.AttemptID,
			"claim_epoch":          cred.ClaimEpoch,
			"session_secret":       cred.SessionSecret,
			"supersede_attempt_id": opts.ReconcileAttempt,
		})
		if err != nil {
			return out, fmt.Errorf("reconcile workflow invocations: %w", err)
		}
		out.Reconciled = resp
		out.Status = "reconciled"
		return out, nil
	}

	if opts.HasSubmit {
		cred, err := workflowCredentials(wiID)
		if err != nil {
			return out, err
		}
		data, err := os.ReadFile(opts.SubmitFile)
		if err != nil {
			return out, fmt.Errorf("read --submit file: %w", err)
		}
		var sub controller.SessionSubmission
		dec := json.NewDecoder(bytes.NewReader(data))
		dec.UseNumber()
		if err := dec.Decode(&sub); err != nil {
			return out, fmt.Errorf("decode --submit file: %w", err)
		}
		var trailing any
		if err := dec.Decode(&trailing); err != io.EOF {
			if err == nil {
				return out, fmt.Errorf("decode --submit file: trailing JSON values are not allowed")
			}
			return out, fmt.Errorf("decode --submit file trailing data: %w", err)
		}
		result, err := controller.SubmitSession(ctx, c, wiID, cred, sub)
		if err != nil {
			return out, err
		}
		out.Submission = result
		return finishWorkflowOutput(ctx, c, out, wiID)
	}

	state, err := controller.Select(ctx, c, wiID)
	if err != nil {
		return out, err
	}
	if state == nil {
		// The server confirmed this work item has no pinned generation:
		// legacy. The scenario engine path (startup / resolve-role / the
		// pf-execute loop) is what drives it — this verb says so and stops.
		out.Legacy = true
		out.Status = "legacy"
		out.Detail = "no pinned workflow generation; the legacy scenario engine path applies"
		return out, nil
	}
	out.WorkItemID = state.WorkItemID
	out.StepsVersion = state.StepsVersion

	action := state.NextAction()
	if opts.Prepare {
		cred, err := workflowCredentials(wiID)
		if err != nil {
			return out, err
		}
		prep, err := controller.PrepareSession(ctx, c, state.WorkItemID, action.StepID, cred)
		if err != nil {
			return out, err
		}
		out.Status, out.StepID, out.RHS = "prepared", prep.StepID, prep.RHS
		out.Detail = "server-fenced invocation prepared; discuss and submit the authored output object"
		out.Preparation = prep
		return out, nil
	}
	if opts.Continue {
		switch action.Kind {
		case controller.ActionApprovalRequired:
			return renderWorkflowAction(out, state, action), nil
		case controller.ActionRevisionRequired:
			route, err := workflowStepRoute(ctx, c, state, action.StepID)
			if err != nil {
				return out, err
			}
			if !route.WorkerRequired {
				renderWorkflowAction(out, state, action)
				out.Detail += "; run polyforge engine workflow --work-item=" + wiID + " --prepare"
				return out, nil
			}
			// A rejected independent gate is still not revised by its producer.
			// Dispatch a fresh server-fenced worker invocation below.
			opts.Execute = true
		case controller.ActionRun:
			route, err := workflowStepRoute(ctx, c, state, action.StepID)
			if err != nil {
				return out, err
			}
			if (action.RHS || route.Interactive) && !route.WorkerRequired {
				out.Status, out.StepID, out.RHS, out.Detail = "prepare_required", action.StepID, action.RHS,
					"this human-facing authoring step requires the main session; run polyforge engine workflow --work-item="+wiID+" --prepare"
				return out, nil
			}
			// Gate, shipping, and automatic steps deliberately fall through to
			// the SAME preflighted one-step controller used by drain. In
			// particular, effective RHS never makes an independent producer local.
			opts.Execute = true
		default:
			return renderWorkflowAction(out, state, action), nil
		}
	}
	if !opts.Execute {
		return renderWorkflowAction(out, state, action), nil
	}
	if action.Kind != controller.ActionRun && action.Kind != controller.ActionRevisionRequired {
		return renderWorkflowAction(out, state, action), nil
	}
	route, err := workflowStepRoute(ctx, c, state, action.StepID)
	if err != nil {
		return out, err
	}
	if (action.RHS || route.Interactive) && !route.WorkerRequired {
		out.Status, out.StepID, out.RHS, out.Detail = "prepare_required", action.StepID, action.RHS,
			"this human-facing authoring step requires the main session; run polyforge engine workflow --work-item="+wiID+" --prepare"
		return out, nil
	}

	cred, err := workflowCredentials(wiID)
	if err != nil {
		return out, err
	}
	workDir, err := workflowWorkDir(wiID, opts.WorkDir)
	if err != nil {
		return out, err
	}

	_, grants, err := controller.LoadValidatedFlow(ctx, c, state)
	if err != nil {
		return out, fmt.Errorf("bind pinned workflow: %w", err)
	}
	grant, ok := grants[action.StepID]
	if !ok {
		return out, fmt.Errorf("pinned workflow has no derived grant for step %s", action.StepID)
	}

	var log bytes.Buffer
	stepOut, rerr := runWorkflowStep(ctx, c, state.WorkItemID, action.StepID, workDir, controller.StepOptions{
		Credentials:          cred,
		Log:                  &log,
		ExpectedStepsVersion: state.StepsVersion,
		ExpectedGrant:        &grant,
	})
	if log.Len() > 0 {
		if opts.LogFile != "" {
			if werr := os.MkdirAll(filepath.Dir(opts.LogFile), 0o700); werr == nil {
				_ = os.WriteFile(opts.LogFile, log.Bytes(), 0o600)
			}
		}
		tail := log.Bytes()
		if len(tail) > workerTailLimit {
			tail = tail[len(tail)-workerTailLimit:]
			out.WorkerTail = "...(truncated)..." + string(tail)
		} else {
			out.WorkerTail = string(tail)
		}
	}
	if rerr != nil {
		return out, rerr
	}
	if !stepOut.Recorded {
		return out, fmt.Errorf("workflow step returned without recording a result")
	}
	result := stepOut.Result
	out.Result = &engineWorkflowStepResult{
		Status:        string(result.Status),
		ReviewVerdict: string(result.ReviewVerdict),
		StepAttemptID: result.StepAttemptID,
		ArtifactID:    result.Artifact.ID,
		ArtifactHash:  result.Artifact.Hash,
	}

	return finishWorkflowOutput(ctx, c, out, state.WorkItemID)
}

func finishWorkflowOutput(ctx context.Context, c workflowClient, out *engineWorkflowOutput, wiID string) (*engineWorkflowOutput, error) {
	next, err := controller.Select(ctx, c, wiID)
	if err != nil {
		return out, fmt.Errorf("result recorded, but re-reading the workflow failed: %w", err)
	}
	if next == nil {
		return out, fmt.Errorf("result recorded, but the pinned workflow disappeared; refusing legacy fallback")
	}
	return renderWorkflowAction(out, next, next.NextAction()), nil
}

func renderWorkflowAction(out *engineWorkflowOutput, state *controller.State, action controller.NextAction) *engineWorkflowOutput {
	out.WorkItemID, out.StepsVersion = state.WorkItemID, state.StepsVersion
	out.Status, out.StepID, out.RHS, out.Detail = string(action.Kind), action.StepID, action.RHS, action.Detail
	if action.Kind == controller.ActionApprovalRequired {
		if p := workflowProgress(state, action.StepID); p != nil {
			out.Approval = &engineWorkflowApproval{
				StepsVersion: state.StepsVersion, StepID: action.StepID, Artifact: p.Artifact,
				Instruction: fmt.Sprintf("Show artifact %s version %d (%s) to the human. Only after that human explicitly says approved or rejected may the authenticated human caller invoke pf_approve_workflow with this exact tuple; a model must not synthesize the decision.", p.Artifact.ID, p.Artifact.Version, p.Artifact.Hash),
			}
		}
	}
	return out
}

func workflowProgress(state *controller.State, stepID string) *controller.Progress {
	for i := range state.Progress {
		if state.Progress[i].StepID == stepID {
			return &state.Progress[i]
		}
	}
	return nil
}

type workflowStepExecutionRoute struct {
	Interactive    bool
	WorkerRequired bool
}

func workflowStepRoute(ctx context.Context, c workflowClient, state *controller.State, stepID string) (workflowStepExecutionRoute, error) {
	flow, grants, err := controller.LoadValidatedFlow(ctx, c, state)
	if err != nil {
		return workflowStepExecutionRoute{}, fmt.Errorf("bind pinned workflow: %w", err)
	}
	for _, step := range flow.BoundSteps() {
		if step.Step.ID == stepID {
			grant, ok := grants[stepID]
			if !ok {
				return workflowStepExecutionRoute{}, fmt.Errorf("pinned workflow has no derived grant for step %s", stepID)
			}
			_, required := controller.SessionWorkerRequirement(step.Contract, grant)
			return workflowStepExecutionRoute{
				Interactive: step.Contract.Runtime.Interactive, WorkerRequired: required,
			}, nil
		}
	}
	return workflowStepExecutionRoute{}, fmt.Errorf("pinned workflow has no bound step %q", stepID)
}

// workflowCredentials loads this work item's attempt credentials from the
// workspace state file — the same credential source every credentialed pf_*
// call uses. The server re-verifies them on every start/record/reconcile, so
// a stale file fails there, not here.
func workflowCredentials(wiID string) (controller.Credentials, error) {
	sf, err := config.ResolveStateFile(wiID)
	if err != nil {
		return controller.Credentials{}, config.StateFileMissingErr(wiID, err)
	}
	if sf.AttemptID == "" || sf.SessionSecret == "" || sf.ClaimEpoch <= 0 {
		return controller.Credentials{}, fmt.Errorf(
			"workflow credentials incomplete for %s (attempt_id/claim_epoch/session_secret); re-claim the work item", wiID)
	}
	return controller.Credentials{AttemptID: sf.AttemptID, ClaimEpoch: sf.ClaimEpoch, SessionSecret: sf.SessionSecret}, nil
}

// workflowWorkDir resolves the directory a dispatched step runs in: an
// explicit --work-dir wins; otherwise the state file's single worktree; with
// several worktrees the caller must choose; with none the step inherits this
// process's cwd (a read-only or artifact-producing step needs no worktree).
func workflowWorkDir(wiID, explicit string) (string, error) {
	if explicit != "" {
		return explicit, nil
	}
	sf, err := config.ResolveStateFile(wiID)
	if err == nil {
		if dir, ok, selectErr := selectWorkflowWorkDir(sf.Worktrees, wiID); ok || selectErr != nil {
			return dir, selectErr
		}
	}
	return os.Getwd()
}

func selectWorkflowWorkDir(worktrees map[string]string, wiID string) (string, bool, error) {
	switch len(worktrees) {
	case 1:
		for _, p := range worktrees {
			return p, true, nil
		}
	case 0:
		return "", false, nil
	default:
		return "", false, fmt.Errorf(
			"this work item has %d worktrees; pass --work-dir to choose the one the step runs in", len(worktrees))
	}
	return "", false, nil
}

// workflowAihubClient builds the aihub client `engine workflow` needs, with
// the same precedence runCLI applies to every client-requiring subcommand:
// the machine config's API key (auth.api_key / POLYFORGE_API_KEY), then the
// workspace .polyforge.yaml's key env; the URL from POLYFORGE_AIHUB_URL >
// config.toml [server] > .polyforge.yaml > the compiled-in default. RunEngine
// takes no client parameter because every other engine verb is local-only;
// duplicating the construction here keeps that signature — and main.go —
// untouched.
func workflowAihubClient() (*client.Client, error) {
	mc, err := config.LoadMachineConfig()
	if err != nil {
		return nil, fmt.Errorf("load machine config: %w", err)
	}
	apiKey := mc.ResolveAPIKey()
	wsURL := ""
	if cfg, cerr := config.Load(config.WorkspaceRoot()); cerr == nil && cfg != nil {
		wsURL = cfg.AIHub.URL
		if apiKey == "" && cfg.AIHub.APIKeyEnv != "" {
			apiKey = os.Getenv(cfg.AIHub.APIKeyEnv)
		}
	}
	if apiKey == "" {
		return nil, fmt.Errorf(
			"engine workflow: no API key. Put it in ~/.polyforge/config.toml under\n" +
				"  [auth]\n  api_key = \"pf_k1_…\"\n" +
				"or export POLYFORGE_API_KEY")
	}
	base, _ := config.EffectiveAihubURL(mc, wsURL)
	return client.New(base, apiKey), nil
}
