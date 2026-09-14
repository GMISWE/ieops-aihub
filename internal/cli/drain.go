package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/GMISWE/ieops-aihub/internal/drain"
	"github.com/GMISWE/ieops-aihub/internal/engine"
	"github.com/GMISWE/ieops-aihub/internal/roles"
	"github.com/GMISWE/ieops-aihub/pkg/client"
)

// DrainUsage is `polyforge drain`'s help text.
const DrainUsage = `usage: polyforge drain --project=<name> [options]

Layer 3 continuous scheduler: repeatedly select the work items that are executable
right now, run them, and stop with a terminal state that says why.

Options:
  --project=<name>        Project to drain (required).
  --all                   Drain every work item in the project, not just mine.
                          Not the default: two people draining the same project
                          under --all do little but take turns losing lock races.
  --plan                  Plan only. List and order what WOULD run, report the
                          terminal classification, claim nothing, execute nothing.
  --max-parallel=<n>      Work items in flight at once (default 8).
  --max-rounds=<n>        Stop after n scheduling rounds (default unlimited).
  --max-work-items=<n>    Stop after executing n work items (default unlimited).
  --channel=<h[/model],...>
                          Preference-ordered harness candidates, e.g.
                          "claude,codex/gpt-5.6,pi/anthropic/claude-opus-4-5".
                          Default: claude,codex,opencode,pi. The first candidate
                          that answers a live preflight probe is used.
  --json                  Emit the run report as JSON on stdout.

Exit codes (notification layer 1):
  0   COMPLETED         every in-scope work item is terminal
  10  IDLE              work remains, waiting on my own work items; come back later
  11  BLOCKED_EXTERNAL  work remains, blocked by someone else; a human must act
  12  FAILED            at least one work item's execution failed
  1   usage error       2  internal error`

// RunDrain implements `polyforge drain`.
func RunDrain(ctx context.Context, c *client.Client, wsRoot string, args []string) {
	opts, err := parseDrainArgs(args)
	if err != nil {
		fmt.Fprintf(os.Stderr, "drain: %v\n\n%s\n", err, DrainUsage)
		os.Exit(1)
	}
	if opts.Help {
		fmt.Println(DrainUsage)
		return
	}
	if opts.Project == "" {
		fmt.Fprintf(os.Stderr, "drain: --project is required\n\n%s\n", DrainUsage)
		os.Exit(1)
	}

	me, err := resolveSelfUserID(ctx, c)
	if err != nil && !opts.All {
		fmt.Fprintf(os.Stderr, "drain: could not resolve your user id (%v).\n"+
			"Scoping to \"my work items\" needs it; pass --all to drain the whole project instead.\n", err)
		os.Exit(2)
	}
	scope := drain.Scope{All: opts.All, UserID: me}

	home, err := polyforgeHome()
	if err != nil {
		fmt.Fprintf(os.Stderr, "drain: %v\n", err)
		os.Exit(2)
	}
	runID := drain.NewRunID(time.Now(), os.Getpid())
	runDir := drain.RunDir(home, runID)

	q := &drainQueries{c: c, project: opts.Project, scope: scope}

	if opts.Plan {
		if err := runDrainPlan(ctx, q, opts); err != nil {
			fmt.Fprintf(os.Stderr, "drain: %v\n", err)
			os.Exit(2)
		}
		return
	}

	// Said once, up front, so the operator is not surprised by what follows. The run still
	// proceeds: preflight, the snapshot and `polyforge watch` are all real and worth
	// exercising, and the claim itself reports the gap per work item. What must NOT happen is
	// the run ending IDLE, which would read as "come back later" for a capability that does
	// not exist — see drain.ErrNotSupported, which is why it ends FAILED instead.
	fmt.Fprintf(os.Stderr, "drain: WARNING: %v\n", errLifecycleSeamMissing)

	// Ops problem 2, startup half: prove a channel works before claiming anything. Claiming
	// first and discovering the credential problem afterwards would leave a trail of claimed
	// work items nobody is executing, each holding locks.
	channels, rejected, err := preflightChannels(ctx, opts.Channels, runDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "drain: %v\n", err)
		for _, v := range rejected {
			fmt.Fprintf(os.Stderr, "  %-10s rejected: %s\n", v.Channel, v.Reason)
		}
		os.Exit(2)
	}

	r := &drain.Runner{
		Project:  opts.Project,
		Scope:    scope,
		Budget:   opts.Budget,
		RunDir:   runDir,
		Channels: channels,
		Now:      time.Now,
		NewULID:  newULID,

		Executable:      q.Executable,
		AllInScope:      q.AllInScope,
		ObserveQueue:    q.ObserveQueue,
		Claim:           claimForDrain,
		UpdateStep:      q.UpdateStep,
		CompleteAttempt: q.CompleteAttempt,
		Notify:          q.Notify,

		AttemptPaused: q.AttemptPaused,

		Startup:     drainStartup(ctx, wsRoot, opts.Project),
		ResolveRole: drainResolveRole,
		Dispatch:    dispatchStepAgent,
		Cleanup:     drainCleanup(ctx, wsRoot),

		Logf: func(format string, a ...any) { fmt.Fprintf(os.Stderr, format+"\n", a...) },
		Publish: func(s *drain.Snapshot) {
			s.RunID, s.PID, s.PreflightRejected = runID, os.Getpid(), rejected
			if err := drain.WriteSnapshot(runDir, s); err != nil {
				fmt.Fprintf(os.Stderr, "drain: warning: snapshot write failed: %v\n", err)
			}
		},
	}
	if err := drain.WriteLatest(home, runID); err != nil {
		fmt.Fprintf(os.Stderr, "drain: warning: could not record this as the latest run: %v\n", err)
	}
	fmt.Fprintf(os.Stderr, "drain: run %s. Watch it with `polyforge watch`.\n", runID)

	report, err := r.Run(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "drain: %v\n", err)
		os.Exit(2)
	}

	if opts.JSON {
		b, _ := json.MarshalIndent(map[string]any{
			"run_id": runID, "terminal": report.Terminal, "stop_reason": report.StopReason,
			"totals": report.Totals, "queue": report.Queue, "outcomes": report.Outcomes,
		}, "", "  ")
		fmt.Println(string(b))
	} else {
		printDrainReport(report, runID, runDir)
	}
	os.Exit(drain.ExitCode(report.Terminal))
}

func printDrainReport(report drain.RunReport, runID, runDir string) {
	fmt.Printf("drain %s: %s (stopped: %s)\n", runID, report.Terminal, report.StopReason)
	fmt.Printf("  wrapped=%d failed=%d lock-blocked=%d paused=%d claim-failed=%d created=%d rounds=%d\n",
		report.Totals.Wrapped, report.Totals.Failed, report.Totals.LockBlocked,
		report.Totals.Paused, report.Totals.ClaimFailed, report.Totals.Created, report.Totals.Rounds)
	fmt.Printf("  queue: executable=%d blocked-by-mine=%d blocked-by-others=%d running=%d paused=%d\n",
		report.Queue.Executable, report.Queue.BlockedByMine, report.Queue.BlockedByOthers(),
		report.Queue.Running, report.Queue.Paused)
	if runDir != "" {
		fmt.Printf("  step output: %s\n", runDir)
	}
}

// runDrainPlan is `--plan`: everything the scheduler decides, with nothing claimed and nothing
// executed. It exercises the whole policy half — scope resolution, the server's ready predicate,
// ordering, the round freeze, the queue observation and the terminal classification — which is
// exactly this work item's declared scope ("本 wi 只负责调度策略（选 wi / 判停 / 并发）").
func runDrainPlan(ctx context.Context, q *drainQueries, opts drainOptions) error {
	candidates, err := q.Executable(ctx)
	if err != nil {
		return err
	}
	frozen, _ := drain.FreezeRound(candidates, opts.Budget.RemainingWorkItems(0))
	queue, err := q.ObserveQueue(ctx)
	if err != nil {
		return err
	}
	terminal := drain.Classify(false, queue)

	if opts.JSON {
		b, _ := json.MarshalIndent(map[string]any{
			"project": opts.Project, "scope_all": opts.All,
			"round_1_order": frozen, "queue": queue,
			"terminal_if_nothing_ran": terminal,
			"parallelism":             opts.Budget.Parallelism(),
		}, "", "  ")
		fmt.Println(string(b))
		return nil
	}

	fmt.Printf("drain --plan: project %s, scope %s, parallelism %d\n",
		opts.Project, scopeLabel(opts.All), opts.Budget.Parallelism())
	if len(frozen) == 0 {
		fmt.Println("  round 1: nothing executable")
	} else {
		fmt.Printf("  round 1 would claim %d work item(s), in this order:\n", len(frozen))
		for i, c := range frozen {
			fmt.Printf("    %2d. %-14s %-7s %s\n", i+1, c.Slug, c.Priority, truncate(c.Goal, 88))
		}
	}
	fmt.Printf("  queue: executable=%d blocked-by-mine=%d blocked-by-others=%d running=%d paused=%d\n",
		queue.Executable, queue.BlockedByMine, queue.BlockedByOthers(), queue.Running, queue.Paused)
	fmt.Printf("  if nothing ran, this run would end: %s (exit %d)\n", terminal, drain.ExitCode(terminal))
	return nil
}

func scopeLabel(all bool) string {
	if all {
		return "--all"
	}
	return "mine"
}

// ─── options ──────────────────────────────────────────────────────────────────

type drainOptions struct {
	Project  string
	All      bool
	Plan     bool
	JSON     bool
	Help     bool
	Budget   drain.Budget
	Channels []drain.Channel
}

func parseDrainArgs(args []string) (drainOptions, error) {
	o := drainOptions{Channels: defaultChannels()}
	for _, a := range args {
		switch {
		case strings.HasPrefix(a, "--project="):
			o.Project = strings.TrimPrefix(a, "--project=")
		case a == "--all":
			o.All = true
		case a == "--plan", a == "--dry-run":
			o.Plan = true
		case a == "--json":
			o.JSON = true
		case strings.HasPrefix(a, "--max-parallel="):
			n, err := positiveInt(a, "--max-parallel")
			if err != nil {
				return o, err
			}
			o.Budget.MaxParallel = n
		case strings.HasPrefix(a, "--max-rounds="):
			n, err := positiveInt(a, "--max-rounds")
			if err != nil {
				return o, err
			}
			o.Budget.MaxRounds = n
		case strings.HasPrefix(a, "--max-work-items="):
			n, err := positiveInt(a, "--max-work-items")
			if err != nil {
				return o, err
			}
			o.Budget.MaxWorkItems = n
		case strings.HasPrefix(a, "--channel="):
			chs, err := parseChannels(strings.TrimPrefix(a, "--channel="))
			if err != nil {
				return o, err
			}
			o.Channels = chs
		case a == "--help", a == "-h":
			// Reported, not executed. os.Exit(0) here would end the PROCESS from inside a
			// pure-looking parser that four tests call directly, so a future table-driven
			// case containing "--help" would silently terminate the test binary with status
			// 0 — a green run that stopped executing partway through.
			o.Help = true
		default:
			// Refuse rather than ignore. An unknown flag on a scheduler that claims and
			// executes real work items is far more likely to be a typo in a budget cap —
			// `--max-workitems=3` silently meaning "unlimited" — than something safe to
			// drop, and the whole point of the budgets is that they hold.
			return o, fmt.Errorf("unknown flag %q", a)
		}
	}
	return o, nil
}

func positiveInt(arg, name string) (int, error) {
	v := arg[strings.IndexByte(arg, '=')+1:]
	n, err := strconv.Atoi(v)
	if err != nil {
		return 0, fmt.Errorf("%s: %q is not a number", name, v)
	}
	if n <= 0 {
		return 0, fmt.Errorf("%s must be positive, got %d", name, n)
	}
	return n, nil
}

func defaultChannels() []drain.Channel {
	out := make([]drain.Channel, 0, len(drain.KnownHarnesses))
	for _, h := range drain.KnownHarnesses {
		out = append(out, drain.Channel{Harness: h})
	}
	return out
}

// parseChannels reads "claude,codex/gpt-5.6,pi/anthropic/claude-opus-4-5".
//
// Everything after the FIRST slash is the model, not everything between slashes: pi and opencode
// both take `<provider>/<model>` identifiers, so a model name legitimately contains a slash, and
// splitting on every slash would mangle exactly the two harnesses whose identifiers aihub#640's
// addendum measured as the fragile ones.
func parseChannels(s string) ([]drain.Channel, error) {
	var out []drain.Channel
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		name, model := part, ""
		if i := strings.IndexByte(part, '/'); i >= 0 {
			name, model = part[:i], part[i+1:]
		}
		h := drain.Harness(name)
		if !knownHarness(h) {
			return nil, fmt.Errorf("--channel: unknown harness %q in %q", name, part)
		}
		out = append(out, drain.Channel{Harness: h, Model: model})
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("--channel: no candidates given")
	}
	return out, nil
}

func knownHarness(h drain.Harness) bool {
	for _, k := range drain.KnownHarnesses {
		if k == h {
			return true
		}
	}
	return false
}

// ─── preflight ────────────────────────────────────────────────────────────────

// preflightChannels probes each candidate with a real (tiny) invocation and returns the healthy
// ones in preference order, plus every rejection and its reason.
//
// A real probe rather than an auth-check subcommand: measured 2026-09-14, `pi auth check` reports
// `{"status":"ready"}` on this machine while the next real call returns HTTP 401 (see
// drain.ClassifyPreflight). A candidate whose binary is not installed is skipped silently — not
// having codex is a normal machine configuration, not a fault — but a candidate that IS installed
// and fails is reported, because that is a credential problem somebody can fix.
func preflightChannels(ctx context.Context, candidates []drain.Channel, runDir string) ([]drain.Channel, []drain.PreflightVerdict, error) {
	var healthy []drain.Channel
	var rejected []drain.PreflightVerdict

	for _, ch := range candidates {
		inv, err := drain.BuildInvocation(ch, drain.PreflightPrompt)
		if err != nil {
			rejected = append(rejected, drain.PreflightVerdict{Channel: ch, OK: false, Reason: err.Error()})
			continue
		}
		if _, err := exec.LookPath(inv.Path); err != nil {
			// Not a fault — not having codex is an ordinary machine configuration — but it
			// must still be RECORDED. Skipping silently meant that on a machine with none of
			// the four installed, `rejected` came back empty and the operator was told only
			// "every candidate is missing, unauthenticated or refusing" with no indication of
			// which binaries had been sought.
			rejected = append(rejected, drain.PreflightVerdict{Channel: ch, OK: false,
				Reason: fmt.Sprintf("%q is not installed on this machine", inv.Path)})
			continue
		}
		probeCtx, cancel := context.WithTimeout(ctx, preflightTimeout)
		// One log file PER HARNESS. They all shared `<runDir>/preflight` until a real run
		// showed why that is useless: four probes wrote it in turn and only the last one
		// survived, so the log of the channel that actually failed was overwritten by the
		// log of the channel that failed after it.
		out, runErr := runHarness(probeCtx, inv, "",
			filepath.Join(runDir, "preflight", string(ch.Harness)+".log"))
		cancel()
		v := drain.ClassifyPreflight(ch, runErr, out)
		if v.OK {
			healthy = append(healthy, ch)
		} else {
			rejected = append(rejected, v)
		}
	}

	if len(healthy) == 0 {
		return nil, rejected, fmt.Errorf(
			"no usable harness channel: every candidate is missing, unauthenticated or refusing. " +
				"Unattended execution cannot re-authenticate, so this stops before claiming anything")
	}
	return healthy, rejected, nil
}

const (
	preflightTimeout = 3 * time.Minute
	stepTimeout      = 2 * time.Hour
)

// runHarness executes one invocation and returns its combined output, also appending that output
// to logPath when one is given.
//
// Combined, not separated: the four harnesses disagree about which stream carries the answer and
// which carries the error (codex puts its 401 on stderr, opencode puts it on stderr with ANSI
// colour, claude puts a refusal on stdout), and a reader at 3am wants one file in the order it
// happened, not two files to interleave by hand.
//
// Stdin is /dev/null for every harness. The survey records `pi -p` blocking forever with an open
// stdin and producing NOTHING on either stream — no error to match on, nothing to time out except
// a wall clock — and closing stdin turns any harness's "waiting for input" into a fast EOF.
func runHarness(ctx context.Context, inv drain.Invocation, workDir, logPath string) (string, error) {
	cmd := exec.CommandContext(ctx, inv.Path, inv.Args...)
	if workDir != "" {
		cmd.Dir = workDir
	}
	if inv.CloseStdin {
		devnull, err := os.Open(os.DevNull)
		if err != nil {
			return "", fmt.Errorf("open %s: %w", os.DevNull, err)
		}
		defer func() { _ = devnull.Close() }()
		cmd.Stdin = devnull
	}
	out, err := cmd.CombinedOutput()
	if logPath != "" {
		if mkErr := os.MkdirAll(filepath.Dir(logPath), 0o755); mkErr == nil {
			_ = os.WriteFile(logPath, out, 0o644)
		}
	}
	return string(out), err
}

// dispatchStepAgent runs one step agent as an independent OS process.
//
// This is the "zero nesting" property from aihub#640 `why_A_nesting_is_zero`: nesting depth means
// SUBAGENT dispatch depth inside a harness, and this process is not a harness agent at all, so
// the child is that harness's own level 1. It is what lets A sidestep codex's max_depth and
// opencode's subagent_depth=1, which gate in-harness dispatch and have nothing to say about a
// process someone else spawned.
func dispatchStepAgent(ctx context.Context, req drain.DispatchRequest) (drain.DispatchResult, error) {
	inv, err := drain.BuildInvocation(req.Channel, req.Prompt)
	if err != nil {
		return drain.DispatchResult{}, err
	}
	stepCtx, cancel := context.WithTimeout(ctx, stepTimeout)
	defer cancel()
	out, runErr := runHarness(stepCtx, inv, req.WorkDir, req.LogPath)
	return drain.DispatchResult{Output: out, ExitErr: runErr}, runErr
}

// ─── engine seams ─────────────────────────────────────────────────────────────

// drainStartup wraps internal/engine's startup sequence. It calls the SAME functions
// `polyforge engine startup` calls (internal/cli/engine.go), in-process: one copy of the logic,
// reached two ways, which is what aihub#640's workflow_identity_constraint requires.
func drainStartup(_ context.Context, wsRoot, project string) func(context.Context, drain.ClaimInfo) ([]drain.StepSpec, error) {
	return func(ctx context.Context, c drain.ClaimInfo) ([]drain.StepSpec, error) {
		git := execGitRunner(ctx)
		gitDirExists := func(path string) bool {
			_, err := os.Stat(filepath.Join(path, ".git"))
			return err == nil
		}
		scenarioPath, _, err := engine.ResolveScenarioPath(git, gitDirExists, wsRoot, c.ScenarioURL)
		if err != nil {
			return nil, err
		}
		sha, err := engine.PinScenarioSHA(git, scenarioPath)
		if err != nil {
			return nil, err
		}
		content, _, err := engine.ResolveTemplate(git, scenarioPath, sha, c.WIType, project)
		if err != nil {
			return nil, err
		}
		steps, err := engine.ScanSteps(content)
		if err != nil {
			return nil, err
		}
		fetch := func(path string) (string, error) { return git(scenarioPath, "show", sha+":"+path) }
		out := make([]drain.StepSpec, 0, len(steps))
		for _, s := range steps {
			expanded, eerr := engine.ExpandIncludes(s.Content, fetch)
			if eerr != nil {
				return nil, fmt.Errorf("expand includes for step %q: %w", s.ID, eerr)
			}
			out = append(out, drain.StepSpec{ID: s.ID, Expanded: expanded})
		}
		return out, nil
	}
}

// drainResolveRole runs the three-tier role fallback against the real catalog — engine.ResolveRole
// verbatim, so a step id absent from the catalog that is review-shaped by name still resolves to
// the read-only reviewer and never silently to the write-capable executor.
func drainResolveRole(stepID string) (string, bool, error) {
	catalog, err := roles.LoadRoles()
	if err != nil {
		return "", false, fmt.Errorf("load roles catalog: %w", err)
	}
	role, _, _, err := engine.ResolveRole(catalog, stepID, "")
	if err != nil {
		return "", false, err
	}
	return role.Name, role.Capability.ReadOnly, nil
}

func drainCleanup(_ context.Context, wsRoot string) func(context.Context, drain.ClaimInfo) error {
	return func(ctx context.Context, c drain.ClaimInfo) error {
		if len(c.Worktrees) == 0 {
			return nil
		}
		git := execGitRunner(ctx)
		rmParent := func(path string) error {
			fi, err := os.Stat(path)
			if err != nil {
				if os.IsNotExist(err) {
					return nil
				}
				return err
			}
			if !fi.IsDir() {
				return fmt.Errorf("%s exists and is not a directory", path)
			}
			return os.RemoveAll(path)
		}
		_, repoErrs, parentErr := engine.CleanupWorktrees(git, rmParent, wsRoot, c.Worktrees)
		if len(repoErrs) > 0 {
			names := make([]string, 0, len(repoErrs))
			for n := range repoErrs {
				names = append(names, n)
			}
			sort.Strings(names)
			return fmt.Errorf("worktree cleanup failed for: %s", strings.Join(names, ", "))
		}
		return parentErr
	}
}

// claimForDrain is the one seam `polyforge drain` cannot fill today, and it refuses loudly
// rather than half-filling it.
//
// # What is missing, precisely
//
// aihub#640's `retro_and_crystallize` ruling splits the work item lifecycle in two: the
// DETERMINISTIC half (claim / pf_update_step / wrap) is called directly by pf, and only the LLM
// half (step execution, retro, crystallize) is dispatched to an agent. Every deterministic piece
// of the STEP loop got a Go home in aihub#654 — internal/engine holds startup, the step bracket,
// review parsing, role resolution and wrap-time worktree cleanup, and internal/cli and the
// markdown B/C loop both call it. The work-item-level lifecycle did not:
//
//	pf_claim_work_item's implementation — session_secret minting, branch naming
//	(newClaimBranchNames/resolveClaimBranch), worktree creation WITH the adoption safety checks
//	that aihub#328/#257/#264 each had to add, .git/info/exclude seeding, state-file and repo-pin
//	writes — lives entirely in internal/mcp/tools_lifecycle.go, in SIX unexported functions
//	(addClaimWorktree, verifyClaimWorktree, newClaimBranchNames, resolveClaimBranch,
//	writeWorktreeExcludes, repairReusedWorktreeUpstream). It is reachable only over MCP stdio.
//
// pkg/client.ClaimWorkItem performs the server-side half, but on its own it produces a claimed
// work item with NO worktree, NO state file and NO session_secret — so the step agent it then
// dispatches has nowhere to work and cannot make an authenticated pf_* call.
//
// # Why this returns an error instead of a reimplementation
//
// Because reimplementing it here is precisely what the workflow_identity_constraint forbids: it
// would put ~350 lines of execution logic in A that B/C does not share, and make a second source
// of truth for worktree adoption — the exact thing whose FIRST source of truth needed three
// separate bug fixes to get right. The constraint's own words are that finding yourself writing
// step-execution logic means the logic belongs in the shared layer.
//
// The fix is the aihub#654 move applied to the work-item lifecycle: extract the claim/wrap
// lifecycle out of internal/mcp into a package both the MCP tool and this scheduler call. That is
// a separate change to files this work item does not own, so it is reported rather than guessed.
// Until it lands, `--plan` exercises the entire scheduling half, which is what this work item is
// scoped to.
var errLifecycleSeamMissing = errors.New(
	"cannot execute yet: the work-item lifecycle (claim + worktree provisioning + state file + " +
		"session_secret) has no Go-callable implementation outside internal/mcp, where it lives " +
		"in six unexported functions reachable only over MCP stdio. " +
		"`polyforge drain --plan` exercises the whole scheduling half and is fully supported. " +
		"Fix: extract pf_claim_work_item's lifecycle into a shared package, the way aihub#654 " +
		"extracted the step engine")

func claimForDrain(_ context.Context, wiID, _ string) (*drain.ClaimInfo, *drain.Blocker, error) {
	// Wrapped in drain.ErrNotSupported so the run ends FAILED (exit 12, "a human has to act")
	// rather than IDLE (exit 10, "re-running later makes progress with no human involved").
	// Re-running never helps here.
	return nil, nil, fmt.Errorf("%w: %w (work item %s)", drain.ErrNotSupported, errLifecycleSeamMissing, wiID)
}

// ─── aihub queries ────────────────────────────────────────────────────────────

// drainQueries holds the aihub-facing half of the runner's injected functions.
type drainQueries struct {
	c       *client.Client
	project string
	scope   drain.Scope
}

// Executable lists in-scope work items that are ready to claim RIGHT NOW.
//
// It asks the server with ready_only=true rather than filtering locally, deliberately. That flag
// is the server's own readyOnlyPredicate — the single SQL constant that also backs
// pf_get_ready_queue's items[] — so "queued, requires_human_session=false, no unfinished blocking
// dependency" is evaluated in exactly one place. Re-deriving dependency readiness in the client
// would be a second implementation of the predicate that decides what runs, and the two would
// drift silently, with drain claiming work items the server considers blocked.
func (q *drainQueries) Executable(ctx context.Context) ([]drain.Candidate, error) {
	p := url.Values{}
	p.Set("project", q.project)
	p.Set("ready_only", "true")
	p.Set("limit", "200")
	if q.scope.Mine() {
		// user_id is REPORTER-only (aihub#383, restated verbatim in the tool schema). For
		// EXECUTABLE work items that is the correct and complete half of ownership: a ready
		// work item is by definition queued, a queued work item has no current attempt, and
		// claimed_by matches only a current attempt — so claimed_by can never contribute a
		// candidate here. It contributes to AllInScope/ObserveQueue instead.
		p.Set("user_id", q.scope.UserID)
	}
	return q.listCandidates(ctx, p)
}

// AllInScope lists every non-terminal in-scope work item, ready or not — the proliferation
// baseline. Blocked work items must be included: a follow-up filed as blocked is still
// proliferation, and a baseline that counted only ready work items would be blind to a work item
// that spawns a chain of blocked successors.
func (q *drainQueries) AllInScope(ctx context.Context) ([]drain.Candidate, error) {
	return q.listByStatus(ctx, "queued,running,blocked,paused")
}

// listByStatus lists in-scope work items in the given statuses, unioning the two halves of
// ownership (see drain.Scope): reporter==me, plus current-attempt-claimant==me.
func (q *drainQueries) listByStatus(ctx context.Context, status string) ([]drain.Candidate, error) {
	base := url.Values{}
	base.Set("project", q.project)
	base.Set("status", status)
	base.Set("limit", "200")

	if !q.scope.Mine() {
		return q.listCandidates(ctx, base)
	}

	byReporter := cloneValues(base)
	byReporter.Set("user_id", q.scope.UserID)
	a, err := q.listCandidates(ctx, byReporter)
	if err != nil {
		return nil, err
	}
	byClaimant := cloneValues(base)
	byClaimant.Set("claimed_by", q.scope.UserID)
	b, err := q.listCandidates(ctx, byClaimant)
	if err != nil {
		return nil, err
	}
	return unionCandidates(a, b), nil
}

func (q *drainQueries) listCandidates(ctx context.Context, p url.Values) ([]drain.Candidate, error) {
	res, err := q.c.ListWorkItems(ctx, p)
	if err != nil {
		return nil, err
	}
	raw, _ := res["items"].([]any)
	out := make([]drain.Candidate, 0, len(raw))
	for _, it := range raw {
		m, ok := it.(map[string]any)
		if !ok {
			continue
		}
		out = append(out, drain.Candidate{
			ID:        str(m["id"]),
			Slug:      str(m["slug"]),
			Goal:      str(m["goal"]),
			Priority:  str(m["priority"]),
			WIType:    str(m["wi_type"]),
			CreatedAt: str(m["created_at"]),
		})
	}
	return out, nil
}

// ObserveQueue classifies what is left in scope, resolving for each blocked work item whether its
// blockers are inside the scope (waiting helps) or outside it (waiting does not) — the
// distinction the IDLE / BLOCKED_EXTERNAL split is made of.
//
// The dependency lookups are the cost the ruling deliberately assigns here rather than to watch:
// `watch` is specified zero-network, so anything only the server knows must be resolved by drain,
// which is already talking to the server anyway.
func (q *drainQueries) ObserveQueue(ctx context.Context) (drain.QueueState, error) {
	var st drain.QueueState

	ready, err := q.Executable(ctx)
	if err != nil {
		return st, err
	}
	st.Executable = len(ready)

	running, err := q.listByStatus(ctx, "running")
	if err != nil {
		return st, err
	}
	st.Running = len(running)

	paused, err := q.listByStatus(ctx, "paused")
	if err != nil {
		return st, err
	}
	st.Paused = len(paused)

	blocked, err := q.listByStatus(ctx, "blocked")
	if err != nil {
		return st, err
	}
	if len(blocked) == 0 {
		return st, nil
	}

	mine, err := q.AllInScope(ctx)
	if err != nil {
		return st, err
	}
	inScope := drain.IDSet(mine)

	for _, b := range blocked {
		var outsiders []string
		deps, derr := q.c.ListDependencies(ctx, b.ID)
		if derr != nil {
			// An unresolvable dependency list must not be read as "blocked by me". Guessing
			// the reassuring answer here converts a lookup failure into a silent IDLE, and
			// IDLE is the state that tells nobody. Assume external: the cost of being wrong
			// is one unnecessary notification, versus a person never hearing about a block.
			outsiders = append(outsiders, "(dependency lookup failed: "+derr.Error()+")")
		} else {
			for _, d := range dependencyIDs(deps) {
				if !inScope[d] {
					outsiders = append(outsiders, d)
				}
			}
		}
		if len(outsiders) > 0 {
			st.ExternallyBlocked = append(st.ExternallyBlocked, drain.BlockedWorkItem{
				WorkItemID: b.ID, Slug: b.Slug, Blockers: outsiders,
			})
		} else {
			st.BlockedByMine++
		}
	}
	return st, nil
}

// dependencyIDs pulls the blocking work item ids out of a pf_list_dependencies response,
// tolerating both the "blocked_by" and "blocking" spellings the endpoint has used.
func dependencyIDs(res map[string]any) []string {
	var out []string
	for _, key := range []string{"blocked_by", "blocking", "dependencies"} {
		arr, ok := res[key].([]any)
		if !ok {
			continue
		}
		for _, it := range arr {
			switch v := it.(type) {
			case string:
				out = append(out, v)
			case map[string]any:
				for _, f := range []string{"blocking_wi_id", "work_item_id", "id", "blocking_id"} {
					if s := str(v[f]); s != "" {
						out = append(out, s)
						break
					}
				}
			}
		}
	}
	return out
}

func (q *drainQueries) UpdateStep(ctx context.Context, wiID string, call engine.StepCall) error {
	body := map[string]any{"step_id": call.StepID, "status": call.Status}
	if call.StepAttemptID != "" {
		body["step_attempt_id"] = call.StepAttemptID
	}
	if call.NextStep != "" {
		body["next_step"] = call.NextStep
	}
	if call.NextStepAttemptID != "" {
		body["next_step_attempt_id"] = call.NextStepAttemptID
	}
	if call.ArtifactSummary != "" {
		body["artifact_summary"] = call.ArtifactSummary
	}
	if call.ErrorType != "" {
		body["error_type"] = call.ErrorType
	}
	_, err := q.c.UpdateStep(ctx, wiID, body)
	return classifyHubError(err)
}

func (q *drainQueries) CompleteAttempt(ctx context.Context, wiID, status, note string) error {
	_, err := q.c.CompleteAttempt(ctx, wiID, map[string]any{"status": status, "note": note})
	return classifyHubError(err)
}

// AttemptPaused asks the server whether a work item's current attempt is paused. It is the
// authority behind drain.Runner.confirmPaused: DetectPause scans the step agent's prose for
// phrases that also appear in fourteen of this repository's own Go files, so a work item whose
// step greps them must not be abandoned on that evidence alone.
func (q *drainQueries) AttemptPaused(ctx context.Context, wiID string) (bool, error) {
	wi, err := q.c.GetWorkItem(ctx, wiID)
	if err != nil {
		return false, err
	}
	return str(wi["status"]) == "paused", nil
}

func (q *drainQueries) Notify(ctx context.Context, n drain.Notification) error {
	target := n.WorkItemID
	if target == "" {
		return nil // a project-level note has no timeline to land on today
	}
	_, err := q.c.EmitEvent(ctx, map[string]any{
		"work_item_id": target,
		"event_type":   "note",
		"payload":      map[string]any{"text": n.Note, "source": "polyforge drain"},
	})
	return err
}

// classifyHubError maps aihub's error strings onto the sentinels the runner branches on, so the
// scheduler's control flow does not depend on string matching spread through the loop.
func classifyHubError(err error) error {
	if err == nil {
		return nil
	}
	s := strings.ToLower(err.Error())
	switch {
	case strings.Contains(s, "conflict_lock_taken"):
		return fmt.Errorf("%w: %v", drain.ErrLockTaken, err)
	case strings.Contains(s, "attempt_paused"), strings.Contains(s, "attempt is paused"):
		return fmt.Errorf("%w: %v", drain.ErrAttemptPaused, err)
	default:
		return err
	}
}

// ─── small helpers ────────────────────────────────────────────────────────────

func cloneValues(v url.Values) url.Values {
	out := url.Values{}
	for k, vals := range v {
		out[k] = append([]string(nil), vals...)
	}
	return out
}

// unionCandidates merges two result sets, keeping the first occurrence of each id.
func unionCandidates(a, b []drain.Candidate) []drain.Candidate {
	seen := make(map[string]bool, len(a)+len(b))
	out := make([]drain.Candidate, 0, len(a)+len(b))
	for _, set := range [][]drain.Candidate{a, b} {
		for _, c := range set {
			if c.ID == "" || seen[c.ID] {
				continue
			}
			seen[c.ID] = true
			out = append(out, c)
		}
	}
	return out
}

func str(v any) string {
	s, _ := v.(string)
	return s
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

func resolveSelfUserID(ctx context.Context, c *client.Client) (string, error) {
	if c == nil {
		return "", fmt.Errorf("no aihub client")
	}
	me, err := c.WhoAmI(ctx)
	if err != nil {
		return "", err
	}
	for _, k := range []string{"user_id", "id"} {
		if s := str(me[k]); s != "" {
			return s, nil
		}
	}
	if u, ok := me["user"].(map[string]any); ok {
		for _, k := range []string{"user_id", "id"} {
			if s := str(u[k]); s != "" {
				return s, nil
			}
		}
	}
	return "", fmt.Errorf("whoami response carried no user id")
}

func polyforgeHome() (string, error) {
	if v := os.Getenv("POLYFORGE_HOME"); v != "" {
		return v, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home directory: %w", err)
	}
	return filepath.Join(home, ".polyforge"), nil
}

// ulidSeq makes newULID's output unique even when two callers land in the same nanosecond.
var ulidSeq atomic.Uint64

// newULID mints a step-attempt id. The loop needs ids that are unique and sort by creation time;
// it does not need canonical ULID encoding, and the server treats them as opaque strings.
//
// Called concurrently, once per step, from every worker in a round, so it carries a counter
// rather than trusting the clock alone to separate two calls. A timestamp is not a uniqueness
// guarantee: two goroutines can read the same nanosecond, and a duplicated step-attempt id is
// the kind of defect that shows up as a work item whose step state is quietly wrong rather than
// as a crash.
func newULID() string {
	return fmt.Sprintf("sa_%d_%d_%d", time.Now().UTC().UnixNano(), os.Getpid(), ulidSeq.Add(1))
}
