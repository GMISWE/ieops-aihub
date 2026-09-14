package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/GMISWE/ieops-aihub/internal/engine"
	"github.com/GMISWE/ieops-aihub/internal/roles"
)

// RunEngine dispatches `polyforge engine <verb> [flags...]`. Every verb here is local-only (no
// aihub API client, no network call) and prints one JSON object to stdout on success — these are
// the CLI-layer wrapping of internal/engine's pieces (a)-(e) for a future headless orchestrator
// (aihub#654); today's LLM-driven pf-execute loop keeps calling those MCP tools directly.
func RunEngine(ctx context.Context, args []string) {
	const usage = "usage: polyforge engine <startup|resolve-role|parse-review|bracket-plan|cleanup-worktrees> [flags...]"
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
	default:
		fmt.Fprintf(os.Stderr, "engine: unknown verb %q\n%s\n", verb, usage)
		os.Exit(1)
	}
	if err != nil {
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
