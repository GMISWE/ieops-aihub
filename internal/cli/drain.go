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
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/GMISWE/ieops-aihub/internal/config"
	"github.com/GMISWE/ieops-aihub/internal/drain"
	"github.com/GMISWE/ieops-aihub/internal/engine"
	"github.com/GMISWE/ieops-aihub/internal/lifecycle"
	"github.com/GMISWE/ieops-aihub/internal/roles"
	"github.com/GMISWE/ieops-aihub/pkg/client"
)

// DrainUsage is `polyforge drain`'s help text.
const DrainUsage = `usage: polyforge drain --project=<name> [options]

Layer 3 continuous scheduler: repeatedly select the work items that are executable
right now, run them, and stop with a terminal state that says why.

Options:
  --project=<name>        Project to drain (required, except with --stop).
  --all                   Drain every work item in the project, not just mine.
                          Not the default: two people draining the same project
                          under --all do little but take turns losing lock races.
  --plan                  Plan only. List and order what WOULD run, report the
                          terminal classification, claim nothing, execute nothing.
  --detach                Start the run in the background and return immediately,
                          printing the run id, the pid and the log path. The
                          process is setsid'd, so it survives this terminal (this
                          box is a compose container: there is no systemd to hand
                          it to). Follow it with "polyforge watch".
  --stop                  Stop the running drain on this machine (the most recent
                          run, or --run=<id>) and return. Sends SIGTERM, which is
                          exactly what Ctrl-C sends, so the run takes its normal
                          graceful-cancellation path and ends with a proper
                          terminal state in its snapshot.
                          WARNING: work items whose step was in flight are left
                          CLAIMED and still holding their locks. That is the same
                          disposition Ctrl-C has always had. --stop names them so
                          you can recover them; it does not recover them for you.
                          It is a drain flag and not a top-level "polyforge halt"
                          on purpose: /pf-stop is the WORK-ITEM lifecycle verb
                          (--pause/--wrap/--fail), and one verb meaning two things
                          is how somebody ends a work item when they meant to end
                          a scheduler.
  --run=<id>              With --stop: which run to stop (default: the most
                          recent). Ignored otherwise.
  --preset=<name>         Resolve this run's per-step models from the named tier
                          table in this machine's config.toml
                          ([roles.presets.<name>.tiers]) instead of its
                          configured selection. A preset is a named snapshot of
                          the whole tier->model table; selecting one swaps the
                          table outright. An unknown name is refused, never
                          silently ignored.
                          --channel=<h/model> still wins where it names a model.
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

	home, err := polyforgeHome()
	if err != nil {
		fmt.Fprintf(os.Stderr, "drain: %v\n", err)
		os.Exit(2)
	}

	// --stop is resolved BEFORE the --project check and before anything touches
	// the network. It names a run that already exists, so it needs no project,
	// no user id and no aihub client -- and requiring any of them would make the
	// one command that ends a runaway scheduler unavailable in exactly the
	// situations that produce one.
	if opts.Stop {
		runDrainStop(home, opts)
		return
	}

	if opts.Project == "" {
		fmt.Fprintf(os.Stderr, "drain: --project is required\n\n%s\n", DrainUsage)
		os.Exit(1)
	}

	// --detach re-execs this binary without --detach and returns. It is checked
	// after the flag validation above so that `--detach --project=` still fails
	// in the foreground with a readable message, rather than spawning a child
	// that fails the same way into a log file.
	if opts.Detach {
		runDrainDetach(home, args, opts)
		return
	}

	me, err := resolveSelfUserID(ctx, c)
	if err != nil && !opts.All {
		fmt.Fprintf(os.Stderr, "drain: could not resolve your user id (%v).\n"+
			"Scoping to \"my work items\" needs it; pass --all to drain the whole project instead.\n", err)
		os.Exit(2)
	}
	scope := drain.Scope{All: opts.All, UserID: me}

	runID := detachedRunID()
	if runID == "" {
		runID = drain.NewRunID(time.Now(), os.Getpid())
	}
	runDir := drain.RunDir(home, runID)

	q := &drainQueries{c: c, project: opts.Project, scope: scope}

	if opts.Plan {
		if err := runDrainPlan(ctx, q, opts); err != nil {
			fmt.Fprintf(os.Stderr, "drain: %v\n", err)
			os.Exit(2)
		}
		return
	}

	// The scenario URL is read ONCE per run, not once per claim. It is a property of the
	// project, the claim response does not carry it (domain.ClaimResponse has no such field),
	// and `engine startup` cannot resolve a step graph without it — so a run that cannot read
	// it would fail every work item identically, one round-trip at a time. Failing here says
	// so once. `--plan` returned above and never needs it.
	scenarioURL, err := projectScenarioURL(ctx, c, opts.Project)
	if err != nil {
		fmt.Fprintf(os.Stderr, "drain: %v\n", err)
		os.Exit(2)
	}

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

	// The tier table is resolved AFTER preflight so the catalog probes only run
	// for harnesses that actually answered, and BEFORE the first claim so a
	// misspelled --preset costs nothing. An unresolvable preset is fatal here
	// rather than a warning: the operator asked for a specific set of models,
	// and running the wrong ones unattended is the failure this flag exists to
	// prevent.
	mc, mcErr := config.LoadMachineConfig()
	if mc == nil {
		// Same tolerance runCLI already applies to an unparseable config.toml:
		// continue as if it were empty rather than taking the whole command
		// down. A --preset that then cannot resolve is refused a line later,
		// which is the loud outcome; a run with no preset is unaffected.
		fmt.Fprintf(os.Stderr, "drain: %s could not be loaded (%v); continuing as if it were empty\n",
			config.MachineConfigPath(), mcErr)
		mc = &config.MachineConfig{}
	}
	tierModels, presetLabel, err := resolvePresetModels(mc, opts.Preset, channels, probeForHarness)
	if err != nil {
		fmt.Fprintf(os.Stderr, "drain: %v\n", err)
		os.Exit(1)
	}
	fmt.Fprintf(os.Stderr, "drain: models from %s\n", presetLabel)

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
		Claim:           (&drainClaimer{c: c, wsRoot: wsRoot, scenarioURL: scenarioURL}).Claim,
		UpdateStep:      q.UpdateStep,
		CompleteAttempt: q.CompleteAttempt,
		Notify:          q.Notify,

		AttemptPaused: q.AttemptPaused,

		Startup:     drainStartup(ctx, wsRoot, opts.Project),
		ResolveRole: drainResolveRole,
		Dispatch:    dispatchWithPresetModel(tierModels),
		Cleanup:     drainCleanup(ctx, wsRoot),

		Logf: func(format string, a ...any) { fmt.Fprintf(os.Stderr, format+"\n", a...) },
		Publish: func(s *drain.Snapshot) {
			s.RunID, s.PID, s.PreflightRejected = runID, os.Getpid(), rejected
			s.Preset = presetLabel
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
	Project string
	All     bool
	Plan    bool
	JSON    bool
	Help    bool
	// Detach spawns the run as an independent background process and returns.
	Detach bool
	// Stop signals a running drain instead of starting one.
	Stop bool
	// RunID qualifies Stop. It is deliberately read ONLY with --stop: a flag
	// that silently did nothing on a normal run would be worse than one that
	// does not exist.
	RunID string
	// Preset names a tier table in ~/.polyforge/config.toml to resolve this
	// run's per-step models from. "" means the machine's configured selection.
	Preset   string
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
		case a == "--detach":
			o.Detach = true
		case a == "--stop":
			o.Stop = true
		case strings.HasPrefix(a, "--run="):
			o.RunID = strings.TrimPrefix(a, "--run=")
		case strings.HasPrefix(a, "--preset="):
			o.Preset = strings.TrimPrefix(a, "--preset=")
			if o.Preset == "" {
				// An empty value means "the machine's configured selection",
				// which is what passing no flag at all already means. Accepting
				// it would make `--preset=` (a shell variable that expanded to
				// nothing) look like it selected something.
				return o, fmt.Errorf("--preset: needs a preset name, e.g. --preset=frugal")
			}
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

	// Mode conflicts are REFUSED, not resolved by precedence. Each of these
	// pairs has two defensible readings, and a scheduler that claims and
	// executes real work items should never pick one silently.
	if o.Stop && o.Detach {
		return o, fmt.Errorf("--stop and --detach are opposites: one ends a run, the other starts one")
	}
	if o.Stop && o.Plan {
		return o, fmt.Errorf("--stop and --plan cannot be combined: --plan describes a run that would start, " +
			"--stop ends one that is already going")
	}
	if o.Detach && o.Plan {
		// --plan's whole output is a report meant to be read. Detaching it
		// would write that report to a log file nobody asked for and print a
		// pid for a process that exits before you can watch it.
		return o, fmt.Errorf("--detach and --plan cannot be combined: --plan prints a report and claims nothing, " +
			"so there is nothing to run in the background")
	}
	if o.RunID != "" && !o.Stop {
		// Refused rather than ignored, for the same reason the parser refuses
		// unknown flags: on this command a silently-inert flag reads as a
		// budget or target that was honoured when it was not.
		return o, fmt.Errorf("--run=%s only applies to --stop; to watch a particular run use "+
			"`polyforge watch --run=%s`", o.RunID, o.RunID)
	}
	if o.Stop && o.Preset != "" {
		return o, fmt.Errorf("--preset does not apply to --stop: it selects the models a run uses, " +
			"and --stop ends a run that already chose them")
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

// ─── --detach ─────────────────────────────────────────────────────────────────

// detachRunIDEnv hands a parent-minted run id down to the detached child.
//
// The child could mint its own — RunDrain does exactly that when this is unset —
// but then the PARENT cannot name the run it just started without polling
// `latest` and hoping. drain.NewRunID's timestamp has one-second resolution, so
// a parent that recomputed the id independently would disagree with the child
// across a second boundary: rare, silent, and it would point `--stop` and
// `polyforge watch` at a directory that does not exist.
const detachRunIDEnv = "POLYFORGE_DRAIN_RUN_ID"

// detachedRunID returns the run id a --detach parent minted for this process,
// and REMOVES it from the environment.
//
// The removal is not tidiness. Step agents are spawned with this process's
// environment, so a `polyforge drain` run from inside a step (a scheduler
// draining a project whose work item drains another) would inherit the variable
// and adopt its parent's run id — two processes writing one snapshot.json,
// each overwriting the other's view of a different run. Unsetting it here means
// only the process the parent actually spawned can use it.
func detachedRunID() string {
	id := os.Getenv(detachRunIDEnv)
	if id == "" {
		return ""
	}
	_ = os.Unsetenv(detachRunIDEnv)
	// The value is interpolated straight into a filesystem path (RunDir,
	// WriteLatest), so its SHAPE is checked rather than trusted. No privilege
	// boundary is crossed — only the invoking user can set this — but
	// POLYFORGE_DRAIN_RUN_ID=../../x would otherwise write outside the drain
	// directory, and a run id has one fixed shape from NewRunID, so requiring
	// it costs nothing. A malformed value is DISCARDED rather than refused:
	// RunDrain then mints its own and the run proceeds, which beats failing a
	// scheduler over an environment variable.
	if !validRunID(id) {
		fmt.Fprintf(os.Stderr, "drain: ignoring malformed %s=%q; minting a fresh run id\n",
			detachRunIDEnv, id)
		return ""
	}
	return id
}

// runIDShape is drain.NewRunID's output: "20060102T150405Z-<pid>".
var runIDShape = regexp.MustCompile(`^[0-9]{8}T[0-9]{6}Z-[0-9]+$`)

func validRunID(id string) bool { return runIDShape.MatchString(id) }

// detachLogFile is where a detached run's own stdout and stderr land, inside its
// run directory next to the per-step logs.
const detachLogFile = "drain.log"

// runDrainDetach starts this same command as an independent background process
// and returns immediately, printing the run id, the pid and the log path.
//
// That output contract is fixed by aihub#640 `entrypoint_not_a_session`, which
// says a convenience entry point's job "只能是 spawn 后台进程后立即返回 pid+日志
// 路径". The same ruling records that this box is a compose container with NO
// systemd, so residency comes from setsid rather than from a unit file: the
// child gets its own session and process group, and therefore survives both the
// parent exiting and the terminal going away.
//
// The child is NOT waited on. Its exit code is the run's terminal state and is
// recorded in the snapshot, which `polyforge watch` reads; blocking here to
// collect it would be the one thing --detach exists not to do.
func runDrainDetach(home string, args []string, opts drainOptions) {
	self, err := os.Executable()
	if err != nil {
		fmt.Fprintf(os.Stderr, "drain: --detach: cannot find this binary to re-exec: %v\n", err)
		os.Exit(2)
	}

	// The run id is minted HERE, from this process's pid, and handed down. See
	// detachRunIDEnv for why the child is not left to mint its own.
	runID := drain.NewRunID(time.Now(), os.Getpid())
	runDir := drain.RunDir(home, runID)
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		fmt.Fprintf(os.Stderr, "drain: --detach: create run dir %s: %v\n", runDir, err)
		os.Exit(2)
	}
	logPath := filepath.Join(runDir, detachLogFile)
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		fmt.Fprintf(os.Stderr, "drain: --detach: open %s: %v\n", logPath, err)
		os.Exit(2)
	}
	defer func() { _ = logFile.Close() }()

	devnull, err := os.Open(os.DevNull)
	if err != nil {
		fmt.Fprintf(os.Stderr, "drain: --detach: open %s: %v\n", os.DevNull, err)
		os.Exit(2)
	}
	defer func() { _ = devnull.Close() }()

	pid, err := startDetached(self, detachChildArgs(args), append(os.Environ(), detachRunIDEnv+"="+runID),
		devnull, logFile)
	if err != nil {
		fmt.Fprintf(os.Stderr, "drain: --detach: could not start the background run: %v\n", err)
		os.Exit(2)
	}

	// 🔴 REGISTER THE RUN HERE, in the parent, before returning.
	//
	// The child writes its own snapshot and `latest` pointer too, but not for a
	// while: it must first resolve the caller's user id, read the project's
	// scenario URL, and PREFLIGHT EVERY CHANNEL CANDIDATE — which spawns a real
	// harness process per candidate. That is seconds to tens of seconds.
	//
	// Leaving the window unregistered made `polyforge drain --stop` act on the
	// PREVIOUS run for its whole duration, which is the runaway-scheduler
	// failure --stop exists to prevent, in two flavours: if the previous run had
	// finished, --stop printed "already finished. Nothing to stop." and exited
	// 0 while the new run went on claiming and executing work items unattended;
	// if it had not, --stop SIGTERMed the wrong run and stranded ITS in-flight
	// work items. The pid-reuse guard cannot catch either, because it asks "is
	// this pid a drain" and not "is it THIS run". It also made `polyforge watch
	// --run=<the id just printed>` fail with "cannot read run".
	//
	// Both writes are idempotent against the child's later ones: the id is the
	// same, and the child's Publish sets PID to its own pid, which is the pid
	// written here. Non-fatal on failure — the run is already started, and
	// refusing to report it would be strictly worse than reporting it with a
	// missing pointer.
	if werr := drain.WriteSnapshot(runDir, &drain.Snapshot{
		RunID:     runID,
		Project:   opts.Project,
		PID:       pid,
		StartedAt: time.Now().UTC().Format(time.RFC3339),
		ScopeAll:  opts.All,
		Preset:    opts.Preset,
	}); werr != nil {
		fmt.Fprintf(os.Stderr, "drain: --detach: warning: could not record the run's initial state (%v); "+
			"`drain --stop` with no --run may act on an older run until this one publishes\n", werr)
	}
	if werr := drain.WriteLatest(home, runID); werr != nil {
		fmt.Fprintf(os.Stderr, "drain: --detach: warning: could not record this as the latest run (%v); "+
			"use `drain --stop --run=%s` rather than the bare form\n", werr, runID)
	}

	if opts.JSON {
		b, _ := json.MarshalIndent(map[string]any{
			"run_id": runID, "pid": pid, "log": logPath, "run_dir": runDir,
		}, "", "  ")
		fmt.Println(string(b))
		return
	}
	fmt.Printf("run_id: %s\n", runID)
	fmt.Printf("pid:    %d\n", pid)
	fmt.Printf("log:    %s\n", logPath)
	fmt.Printf("\nwatch it:  polyforge watch --run=%s --follow\n", runID)
	fmt.Printf("stop it:   polyforge drain --stop --run=%s\n", runID)
}

// startDetached spawns path in its own session and returns the child's pid,
// having released it rather than reaped it.
//
// 🔴 The pid is READ BEFORE Release, and that ordering is the whole reason this
// is a separate function. os.Process.Release sets Pid to -1 on Unix, so reading
// cmd.Process.Pid afterwards yields -1 — which is exactly what the first version
// of --detach printed, breaking the one output contract aihub#640 fixes for it
// ("立即返回 pid+日志路径"). It was invisible to every unit test and turned up on
// the first live run, so the ordering is now stated here once and asserted by
// TestStartDetached_ReturnsAUsablePid.
//
// Setsid, not merely "started in the background": without a new session the child
// stays in this process group and takes the terminal's SIGHUP with it. Build
// targets are linux and darwin only (publish-bins.yml, ci.yml) and both have the
// field.
func startDetached(path string, args, env []string, stdin, out *os.File) (int, error) {
	cmd := exec.Command(path, args...) //nolint:gosec // path is os.Executable()
	cmd.Stdin = stdin
	cmd.Stdout = out
	cmd.Stderr = out
	cmd.Env = env
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}

	if err := cmd.Start(); err != nil {
		return 0, err
	}
	pid := cmd.Process.Pid // BEFORE Release; see above.
	// Release rather than Wait: this process is about to exit and the run is
	// reparented to init (pid 1 in this container — there is no systemd).
	_ = cmd.Process.Release()
	return pid, nil
}

// detachChildArgs rebuilds the child's argument vector: the `drain` subcommand
// plus this invocation's own flags, minus --detach.
//
// Dropping --detach is what stops the child from detaching again, forever. It is
// removed by value rather than by reconstructing the flags from drainOptions,
// so a flag this function has never heard of still reaches the child.
func detachChildArgs(args []string) []string {
	out := make([]string, 0, len(args)+1)
	out = append(out, "drain")
	for _, a := range args {
		if a == "--detach" {
			continue
		}
		out = append(out, a)
	}
	return out
}

// ─── --stop ───────────────────────────────────────────────────────────────────

// stopGraceDeadline bounds how long --stop waits for the run to exit on its own.
// A drain that is mid-step has to unwind a killed child process and write a final
// snapshot; that is fast, but it is not instant.
const stopGraceDeadline = 30 * time.Second

// stopPollInterval is how often --stop rechecks whether the process is gone.
const stopPollInterval = 250 * time.Millisecond

// runDrainStop stops a running drain by sending it SIGTERM, and reports what that
// left behind.
//
// # Why SIGTERM and nothing else
//
// The graceful path already exists and this hooks into it rather than inventing a
// second one. cmd/polyforge/main.go wraps every command in
// `signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)`, so SIGTERM cancels
// the run context — the same cancellation Ctrl-C causes. It is NOT the same
// delivery: Ctrl-C signals the whole foreground process group, so see
// signalTarget for what this signals and what it cannot reach.
// internal/drain/runner.go
// then unwinds through the cancellation path it already has: the round loop
// breaks with StopCancelled, in-flight step agents die with their
// exec.CommandContext, bailOut() converts a mid-call context.Canceled into an
// ending rather than an error, and finish() writes a snapshot with Finished=true
// and a real terminal state. `polyforge watch` renders that correctly today.
//
// # What it leaves behind, and why this says so out loud
//
// A work item whose step was in flight is left CLAIMED — status `running`, locks
// still held. That is runner.go's deliberate, pre-existing disposition, in its
// own words "the attempt is left claimed so it can be resumed", and it is what
// Ctrl-C has always done. Giving --stop a DIFFERENT disposition would be worse
// than this one: two shutdown paths that disagree about whether an interrupted
// attempt is `running` or `paused` is a far harder thing to reason about at 3am
// than one path that is merely imperfect.
//
// So this does not quietly inherit that behaviour: it reads the snapshot BEFORE
// signalling and names the work items it is about to strand, with the recovery
// step. An unattended scheduler's stop command that left a pile of `running` work
// items without saying so would be worse than not having the flag at all.
//
// ⚠️ That list is Snapshot.Active, which is a LOWER BOUND, not the complete set.
// runner.go's executeWorkItem claims a work item and then runs engine startup and
// opens the first step before it calls setActive, so a work item can be claimed
// and holding locks while absent from Active for that whole stretch — and with
// --max-parallel=8, eight of them can be. Publishing an entry at claim time
// instead would close the gap, but that is runner.go, outside this work item's
// declared files. The printed text therefore says "had a step in flight", which
// is exactly what is known, and adds a line about the ones that may not appear.
//
// # Why it never escalates to SIGKILL
//
// SIGKILL cannot be caught, so the run would never reach finish(). The snapshot
// would keep Finished=false with a pid that no longer exists — precisely the
// shape `polyforge watch` reports as "DIED (process gone, run never finished)".
// Escalating would manufacture that state deliberately. If the process does not
// exit within the deadline this says so and stops, leaving the decision with the
// person who can make it.
func runDrainStop(home string, opts drainOptions) {
	runID := opts.RunID
	if runID == "" {
		runID = drain.ReadLatest(home)
		if runID == "" {
			fmt.Fprintln(os.Stderr, "drain --stop: no drain run recorded on this machine.")
			os.Exit(1)
		}
	}
	dir := drain.RunDir(home, runID)
	s, err := drain.ReadSnapshot(dir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "drain --stop: cannot read run %s (%v).\n"+
			"`polyforge watch --list` shows the runs on this machine.\n", runID, err)
		os.Exit(1)
	}

	// Guards, in order. Every one is a refusal rather than a warning: this
	// command sends a signal, and "probably the right process" is not a standard
	// a signal should be sent on.
	if s.Finished {
		fmt.Printf("drain --stop: run %s already finished: %s (stopped: %s, exit %d). Nothing to stop.\n",
			runID, s.Terminal, s.StopReason, s.ExitCode)
		return
	}
	if s.PID <= 0 {
		fmt.Fprintf(os.Stderr, "drain --stop: run %s records no pid, so there is nothing to signal.\n", runID)
		os.Exit(1)
	}
	if !processAlive(s.PID) {
		fmt.Fprintf(os.Stderr, "drain --stop: run %s is not running: pid %d is gone but the run never "+
			"finished, so it DIED rather than ended.\n"+
			"Any work item it had claimed is still claimed; `polyforge watch --run=%s` lists them.\n",
			runID, s.PID, runID)
		os.Exit(1)
	}
	if ok, checked := pidLooksLikeDrain(s.PID); checked && !ok {
		// Pid reuse is the one way this command can do real damage: the pid in a
		// snapshot from days ago may belong to something else entirely by now,
		// and SIGTERM to an unrelated process is not recoverable by apologising.
		fmt.Fprintf(os.Stderr, "drain --stop: REFUSING to signal pid %d: it is alive but is not a polyforge "+
			"drain run, so run %s's pid has been reused by something else.\n"+
			"Nothing was signalled.\n", s.PID, runID)
		os.Exit(1)
	} else if !checked {
		fmt.Fprintf(os.Stderr, "drain --stop: note: cannot verify pid %d is still polyforge on this platform; "+
			"proceeding on the snapshot's word.\n", s.PID)
	}

	// Read the casualty list BEFORE signalling: once the run unwinds, finish()
	// clears Snapshot.Active, so afterwards there is nothing left to report.
	inFlight := append([]drain.ActiveWI(nil), s.Active...)

	target, whole := signalTarget(s.PID)
	if err := syscall.Kill(target, syscall.SIGTERM); err != nil {
		fmt.Fprintf(os.Stderr, "drain --stop: could not signal pid %d: %v\n", s.PID, err)
		os.Exit(2)
	}
	if whole {
		fmt.Printf("drain --stop: sent SIGTERM to run %s (process group %d); waiting up to %s for it "+
			"to finish unwinding.\n", runID, s.PID, stopGraceDeadline)
	} else {
		// Disclosed rather than glossed: on this path the step agents' own
		// children are not in the signal's target set. See signalTarget.
		fmt.Printf("drain --stop: sent SIGTERM to run %s (pid %d only, not its process group); "+
			"waiting up to %s for it to finish unwinding.\n", runID, s.PID, stopGraceDeadline)
	}

	stopped := waitForRunToStop(s.PID)

	if len(inFlight) > 0 {
		fmt.Printf("\nWARNING: %d work item(s) had a step in flight. Cancelling does NOT complete their attempts:\n",
			len(inFlight))
		for _, a := range inFlight {
			fmt.Printf("    %-14s step %s (%d/%d)\n", a.Candidate.Slug, a.StepID, a.StepIndex, a.StepCount)
		}
		fmt.Printf("  Each is left CLAIMED, status \"running\", still holding its locks. That is the same\n" +
			"  thing Ctrl-C has always done. To release one, resume it, or call pf_complete_attempt on\n" +
			"  it; if its credentials are gone you will need pf_force_takeover.\n")
	}
	if len(inFlight) > 0 || !stopped {
		// A lower bound, and said so. A work item that was claimed but had not
		// yet opened its first step does not appear above, so the list can
		// under-report. Better to name the uncertainty than to let the count be
		// read as exhaustive.
		fmt.Printf("  This list can UNDER-REPORT: a work item claimed moments before the stop, whose\n" +
			"  first step had not opened yet, is held but not listed. Check `/pf-status` for the\n" +
			"  project's own view of what is still running.\n")
	}

	if !stopped {
		fmt.Fprintf(os.Stderr, "\ndrain --stop: pid %d has not exited after %s. It was NOT killed: SIGKILL "+
			"would skip the run's final snapshot write and leave it looking like a crash.\n"+
			"Check \"polyforge watch --run=%s\"; it may still be unwinding a long step.\n",
			s.PID, stopGraceDeadline, runID)
		os.Exit(1)
	}
	if final, ferr := drain.ReadSnapshot(dir); ferr == nil && final.Finished {
		fmt.Printf("\ndrain --stop: run %s ended: %s (stopped: %s, exit %d).\n",
			runID, final.Terminal, final.StopReason, final.ExitCode)
	} else {
		fmt.Printf("\ndrain --stop: run %s stopped.\n", runID)
	}
}

// signalTarget decides what to signal for a run whose leader is pid: the whole
// process group (a negative pid, whole=true) when pid is its own group leader,
// or just pid otherwise.
//
// # Why this is not simply pid
//
// SIGTERM to a single pid is NOT what Ctrl-C does, and the difference reaches the
// machine. Ctrl-C is delivered by the tty to the entire foreground process GROUP,
// so a step agent and everything it spawned all get it. runHarness uses
// exec.CommandContext with no Cancel, so Go's cancellation sends SIGKILL to the
// harness process ONLY — uncatchable, giving it no chance to reap its own
// children, which are then orphaned onto init.
//
// That is not a theoretical cost on this box. A subagent once left load
// generators running after its session ended and they burned 7.5 of 12 cores for
// eleven days, with nothing but the load average to show for it.
//
// A --detach'ed run is always its own group leader (startDetached sets Setsid, so
// pgid == pid) and its step agents inherit that group, so the group signal
// reaches the whole tree. A FOREGROUND drain is normally not a leader — the shell
// owns the pipeline's group — and signalling -pid there would either fail or hit
// an unrelated group, so that case falls back to the single pid and runDrainStop
// says so.
//
// The remaining gap is a foreground run stopped with --stop rather than Ctrl-C:
// its step agents' grandchildren can still be orphaned. Closing that needs
// Setpgid plus a Cancel on runHarness itself, which changes how every run
// (foreground, detached and preflight) signals its children; it is recorded as a
// follow-up rather than smuggled in here.
func signalTarget(pid int) (target int, whole bool) {
	pgid, err := syscall.Getpgid(pid)
	if err == nil && pgid == pid {
		return -pid, true
	}
	return pid, false
}

// waitForRunToStop polls until the run's process is gone or the deadline passes,
// reporting whether it stopped.
//
// It watches the PROCESS, not the snapshot's Finished flag, because those answer
// different questions: finish() writes the flag and then Run returns, so a
// snapshot can say Finished while the process is still tearing down, and a
// process that died without writing one never sets it at all. "Has it exited" is
// the question --stop actually needs answered.
func waitForRunToStop(pid int) bool {
	deadline := time.Now().Add(stopGraceDeadline)
	for {
		if !processAlive(pid) {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(stopPollInterval)
	}
}

// pidLooksLikeDrain reports whether pid's command line belongs to a polyforge
// drain run, and whether the check could be performed at all.
//
// checked=false is a real and distinct answer, not a failure: it means this
// platform has no /proc to ask (darwin is a build target), so the caller must
// proceed on weaker evidence AND SAY SO. Collapsing "not a drain" and "could not
// tell" into one boolean would either refuse to stop anything on darwin or
// silently drop the guard on Linux, and both are worse than reporting which
// happened.
func pidLooksLikeDrain(pid int) (ok, checked bool) {
	// The binary this process is running as. A --detach'ed run is literally a
	// re-exec of it (startDetached takes os.Executable()), so its name is the
	// most reliable thing to compare against — more so than the string
	// "polyforge", which a renamed or locally-built binary does not carry.
	self, _ := os.Executable()

	if b, err := os.ReadFile(fmt.Sprintf("/proc/%d/cmdline", pid)); err == nil {
		// /proc/<pid>/cmdline is NUL-separated; argv[0] and the subcommand run
		// together into one token unless the separators are replaced.
		return cmdlineIsDrainRun(strings.ReplaceAll(string(b), "\x00", " "), self), true
	}
	// No /proc: darwin, which is half of what gets published
	// (publish-bins.yml ships darwin/amd64 and darwin/arm64). `ps` answers the
	// same question there, and the guard this function exists to provide would
	// otherwise be silently absent on those binaries while the comments read as
	// though it applied everywhere.
	out, err := exec.Command("ps", "-o", "command=", "-p", strconv.Itoa(pid)).Output()
	if err != nil {
		return false, false
	}
	line := strings.TrimSpace(string(out))
	if line == "" {
		return false, false
	}
	return cmdlineIsDrainRun(line, self), true
}

// cmdlineIsDrainRun decides whether a process command line is a polyforge drain
// run. Split from the /proc read so the decision can be asserted on directly.
//
// # Why both clauses
//
// The hazard is pid REUSE: a snapshot written hours ago names a pid that the
// kernel has since handed to something else, and SIGTERM to a stranger is not
// undone by apologising. "Is it polyforge" alone is not enough, because the
// commonest polyforge process on any of these machines is the long-lived MCP
// server every editor session starts — killing the team's `polyforge serve`
// because a drain pid got recycled would be a worse outcome than the one this
// guard exists to prevent. So the command line must name the binary AND be a
// drain invocation.
//
// # Why the binary is matched on argv[0] against self, not on the whole string
//
// An earlier version asked only `strings.Contains(cmdline, "polyforge")`, which
// is wrong in BOTH directions and a live run caught the worse one:
//
//   - Too strict. The published binaries are named polyforge-linux-amd64 and so
//     on, but a locally built or renamed one is not, and the first live test of
//     --stop refused to stop a perfectly real run because the binary under test
//     was /tmp/pf673. A stop command that will not stop things is worse than no
//     stop command. selfBinary fixes that: --detach re-execs os.Executable(), so
//     the run and the process stopping it are the same file by construction.
//   - Too loose. Searching the WHOLE command line let any process mentioning
//     "polyforge" anywhere — a log path, a working directory — satisfy the
//     binary half. Only argv[0] identifies the program.
//
// The `drain` token is still required, and is still the clause that matters most
// for pid reuse: the commonest polyforge process on these machines is the
// long-lived `polyforge serve` MCP server, and killing the team's MCP server
// because a drain pid got recycled would be worse than the failure being
// prevented.
//
// Deliberately a token test rather than a flag parse: one element of the vector
// is literally "drain" (detachChildArgs puts it there), and a parser here would
// be a second, drifting copy of the dispatch in cmd/polyforge/main.go.
func cmdlineIsDrainRun(cmdline, selfBinary string) bool {
	fields := strings.Fields(cmdline)
	if len(fields) == 0 {
		return false
	}
	argv0 := fields[0]
	named := strings.Contains(filepath.Base(argv0), "polyforge")
	if !named && selfBinary != "" {
		named = filepath.Base(argv0) == filepath.Base(selfBinary)
	}
	if !named {
		return false
	}
	for _, f := range fields[1:] {
		if f == "drain" {
			return true
		}
	}
	return false
}

// ─── --preset ─────────────────────────────────────────────────────────────────

// resolvePresetModels builds the tier→model table this run dispatches with, one
// entry per (harness, tier), and returns a label naming where the table came
// from.
//
// # Why drain resolves this at all
//
// Before aihub#673, `polyforge roles generate` and the serve-startup codex
// profile generation read [roles.tiers] and drain did not read it at all. That is
// already two 口径 on one machine: the same tier resolved to one model in a
// generated agent file and to whatever the harness defaulted to under drain.
// Adding --preset to drain alone would have deepened that rather than fixed it,
// which is why config.MachineConfig.ResolveTiers is the single resolver all three
// now share.
//
// # Why once per run rather than once per step
//
// The candidates are validated against each harness's live model catalog
// (probeForHarness), and a catalog probe shells out. Doing that per step would
// cost a process per dispatch; doing it never would let a model ID that is not in
// the local catalog through, and the resulting "model not found" would be
// classified by ClassifyStepDispatch as an ordinary step failure — blaming the
// work item for a configuration error. Once per run is the only placement that is
// both cheap and honest.
//
// Only harnesses that survived preflight are probed: the others cannot be
// dispatched to, so resolving models for them would emit warnings about
// configuration that could not have mattered.
// probeFor is injected so this can be exercised without a live harness: the
// production value is probeForHarness, which shells out to each CLI's model
// catalog. It returns nil for a harness with no catalog (claude), and that nil
// is load-bearing — see the loop below.
func resolvePresetModels(mc *config.MachineConfig, preset string, channels []drain.Channel,
	probeFor func(string) CatalogProbe) (map[drain.Harness]map[string]string, string, error) {

	tiers, source, err := mc.ResolveTiers(preset)
	if err != nil {
		return nil, "", err
	}
	if len(tiers) == 0 {
		// No table configured. Every channel keeps Model=="" and each harness
		// picks its own default, which is drain's behaviour to date.
		return nil, source, nil
	}

	harnesses := make([]drain.Harness, 0, len(channels))
	seen := map[drain.Harness]bool{}
	for _, ch := range channels {
		if !seen[ch.Harness] {
			seen[ch.Harness] = true
			harnesses = append(harnesses, ch.Harness)
		}
	}

	tierNames := make([]string, 0, len(tiers))
	for t := range tiers {
		tierNames = append(tierNames, t)
	}
	sort.Strings(tierNames)

	out := make(map[drain.Harness]map[string]string, len(harnesses))
	for _, h := range harnesses {
		// probeForHarness returns nil for claude, and that is the right answer
		// rather than a gap: a RoleCandidate's harness is pi/codex/opencode by
		// contract (config.RoleCandidate), so no tier can name claude, and
		// ResolveModel below finds nothing for it. Leaving Claude Code's model
		// empty is also what it wants — aihub#555 measured that passing --model
		// silently OVERRIDES the agent file's own frontmatter.
		probe := probeFor(string(h))
		if probe == nil {
			continue
		}
		for _, tier := range tierNames {
			model, ok := ResolveModel(tiers[tier], string(h), probe)
			if ok {
				if out[h] == nil {
					out[h] = map[string]string{}
				}
				out[h][tier] = model
				continue
			}
			if !tierNamesHarness(tiers[tier], string(h)) {
				// The tier simply does not mention this harness. That is an
				// ordinary configuration — a pi-only preset need say nothing
				// about codex — not something to warn about.
				continue
			}
			// Declared but unresolvable: the operator wrote candidates for this
			// harness and none of them are in its catalog. Loud, matching the
			// AC7 warning `roles generate` prints for the same condition.
			fmt.Fprintf(os.Stderr, "drain: WARNING: tier %q names %s candidates but none resolve in %s's "+
				"local model catalog; steps at that tier will use %s's default model instead of a "+
				"configured one.\n", tier, h, h, h)
		}
	}
	return out, source, nil
}

func tierNamesHarness(candidates []config.RoleCandidate, harness string) bool {
	for _, c := range candidates {
		if c.Harness == harness {
			return true
		}
	}
	return false
}

// dispatchWithPresetModel wraps dispatchStepAgent so each step runs on the model
// its ROLE's tier selects, per the resolved preset.
//
// This is where drain honours the preset per STEP rather than per run, and it
// lives here — in the dispatch seam — for a structural reason. The obvious place
// would be internal/drain's Runner, but Runner.ResolveRole returns only
// (role, readOnly): the tier never reaches the loop. DispatchRequest, on the
// other hand, already carries .Role and .Channel, so the whole mapping resolves
// on this side of the seam with no change to the scheduler.
//
// An explicitly requested model is never overwritten: --channel=pi/some-model
// arrives with Channel.Model already set, and explicit beats configured.
//
// With no preset models resolved this returns dispatchStepAgent unchanged, so a
// machine that configures nothing pays nothing and behaves exactly as before.
func dispatchWithPresetModel(tierModels map[drain.Harness]map[string]string) func(context.Context, drain.DispatchRequest) (drain.DispatchResult, error) {
	if len(tierModels) == 0 {
		return dispatchStepAgent
	}
	tierOf, err := roleTiers()
	if err != nil || len(tierOf) == 0 {
		// The role catalog is embedded, so this is close to impossible; if it
		// ever happens, dispatching on each harness's default model is a far
		// better failure than refusing to run.
		fmt.Fprintf(os.Stderr, "drain: warning: could not index role tiers (%v); "+
			"steps will use each harness's default model\n", err)
		return dispatchStepAgent
	}
	return wrapDispatchWithPresetModel(tierModels, tierOf, dispatchStepAgent)
}

// wrapDispatchWithPresetModel is dispatchWithPresetModel's pure half: the model
// substitution, with the catalog lookup and the process spawn both injected.
//
// Split out so the mapping can be asserted on directly. The rule under test is
// "which model does a step of this role, on this harness, run with" — a question
// about a struct field, not about a child process — and a test that had to spawn
// a harness to ask it would be testing the harness.
func wrapDispatchWithPresetModel(
	tierModels map[drain.Harness]map[string]string,
	tierOf map[string]string,
	next func(context.Context, drain.DispatchRequest) (drain.DispatchResult, error),
) func(context.Context, drain.DispatchRequest) (drain.DispatchResult, error) {
	return func(ctx context.Context, req drain.DispatchRequest) (drain.DispatchResult, error) {
		// Only an EMPTY model is filled in. A non-empty one came from
		// --channel=<harness>/<model>, and explicit beats configured.
		if req.Channel.Model == "" {
			if tier, ok := tierOf[req.Role]; ok {
				if model := tierModels[req.Channel.Harness][tier]; model != "" {
					req.Channel.Model = model
				}
			}
		}
		return next(ctx, req)
	}
}

// roleTiers indexes the embedded role catalog as role name → tier.
func roleTiers() (map[string]string, error) {
	catalog, err := roles.LoadRoles()
	if err != nil {
		return nil, err
	}
	out := make(map[string]string, len(catalog))
	for _, r := range catalog {
		out[r.Name] = r.Tier
	}
	return out, nil
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

// ─── the work-item lifecycle seam ─────────────────────────────────────────────
//
// This USED TO BE A REFUSAL. Until aihub#667 the whole of pf_claim_work_item's local half —
// session_secret minting and its idempotency-key replay, task-branch naming, worktree creation
// with the aihub#328 and aihub#257 adoption checks, .git/info/exclude seeding, the state file,
// repo pins — lived in six unexported functions in internal/mcp/tools_lifecycle.go and was
// reachable only over MCP stdio. pkg/client.ClaimWorkItem does the server-side half, but on its
// own it produces a claimed work item with NO worktree, NO state file and NO session_secret, so
// the step agent dispatched next has nowhere to work and cannot make an authenticated pf_* call.
//
// aihub#640 refused to reimplement it here and ended the run FAILED instead, because
// reimplementing is exactly what the workflow_identity_constraint forbids: ~350 lines of
// execution logic in A that B/C does not share, and a second source of truth for worktree
// adoption — the thing whose FIRST source of truth needed two separate incident fixes to get
// right. aihub#667 did the aihub#654 move instead. What is left here is a translation between
// two struct shapes, and that is the whole point: if this file ever grows a git command or a
// path rule, it has become the second implementation again.

// drainClaimer holds the per-run values a claim needs and the loop does not carry.
type drainClaimer struct {
	c      *client.Client
	wsRoot string
	// scenarioURL is the project's step-graph repo, read once per run. It is not on the claim
	// response, so it cannot come out of internal/lifecycle; see RunDrain.
	scenarioURL string
}

// Claim is drain.Runner.Claim. It calls the same internal/lifecycle.Claim the MCP tool calls,
// and adds nothing except the mapping onto drain.ClaimInfo.
func (d *drainClaimer) Claim(ctx context.Context, wiID, idempotencyKey string) (*drain.ClaimInfo, *drain.Blocker, error) {
	// startupCfg is nil deliberately: this process has no startup config snapshot, and
	// lifecycle.Claim reads .polyforge.yaml out of the workspace root first in either case
	// (resolveWorkspaceConfig). The MCP server passes its snapshot only as a fallback.
	res, err := lifecycle.Claim(ctx, d.c, nil, lifecycle.ClaimRequest{
		WorkItemID:     wiID,
		IdempotencyKey: idempotencyKey,
	})
	if err != nil {
		// classifyHubError is what turns a 409 CONFLICT_LOCK_TAKEN into drain.ErrLockTaken,
		// which the loop treats as ordinary control flow (skip, never retry).
		return nil, lockBlockerFrom(err), classifyHubError(err)
	}

	info := &drain.ClaimInfo{
		WorkItemID: res.State.WIID,
		AttemptID:  res.State.AttemptID,
		// From the RESPONSE, not from the state file: wi_type is server state that selects
		// the step-graph template, and the state file deliberately holds only credentials.
		WIType:       str(res.Response["wi_type"]),
		ScenarioURL:  d.scenarioURL,
		WorktreeRoot: claimWorktreeRoot(d.wsRoot, res.State),
		Worktrees:    res.State.Worktrees,
	}
	// Worktree problems are NOT an error — the claim succeeded and says so. They are exactly
	// the aihub#328 rejections, and they reach the operator here because in A there is no
	// model reading an ok:true response to notice them.
	//
	// Printed BEFORE the attempt_id check below, not after: that check returns, and a return
	// that swallowed the aihub#328 warnings would put the rejected directory back in the
	// "noticed by nobody" state the check was filed about.
	for _, p := range res.WorktreeProblems {
		fmt.Fprintf(os.Stderr, "drain: %s: %s\n", wiID, p)
	}
	if info.AttemptID == "" {
		// A claim that did not come back with an attempt id cannot authenticate anything
		// afterwards. Reported rather than executed: the alternative is a work item running
		// under credentials that do not exist, failing one step at a time.
		//
		// ⚠️ THIS IS ON THE FAR SIDE OF THE SERVER COMMIT, which is why the message says so.
		// The run classifies a plain error here as ResultClaimFailed — "somebody beat me to
		// it", which ends the run IDLE, i.e. come back later — and that is false: the work
		// item IS claimed, holds its locks, and no later round can take it. Same hazard the
		// aihub#323 message in internal/lifecycle guards on the MCP side.
		return nil, nil, fmt.Errorf(
			"claim of %s SUCCEEDED ON THE SERVER but returned no attempt_id, so nothing here can "+
				"authenticate as the attempt: it is claimed, holds this work item's locks, and no "+
				"later round will pick it up. A human has to force_takeover or complete it", wiID)
	}
	return info, nil, nil
}

// claimWorktreeRoot is the pf.<project>-<seq> directory the claim materialised: the parent of
// every per-repo worktree, which is what engine.CleanupWorktrees removes at wrap and what the
// step agent runs in.
//
// Derived from the worktrees the claim actually recorded rather than recomputed from the slug,
// so a repo that was skipped (a rejected directory) cannot make this name a guess. Falls back
// to the slug-derived name only when there are no worktrees at all, which is the case where
// nothing was created and the value is used for nothing.
func claimWorktreeRoot(wsRoot string, sf *config.StateFile) string {
	for _, path := range sf.Worktrees {
		return filepath.Dir(path)
	}
	// The seq comes from the slug's "#" suffix and ONLY from there. A slug with no "#" has no
	// seq, and treating the whole slug as one produced pf.aihub-aihub — a path this claim never
	// created, handed to a cleanup that removes a directory tree. Found by the test below, not
	// by reading.
	i := strings.LastIndex(sf.Slug, "#")
	if i < 0 {
		return ""
	}
	seq := sf.Slug[i+1:]
	if sf.Project == "" || seq == "" {
		return ""
	}
	return filepath.Join(wsRoot, fmt.Sprintf("pf.%s-%s", sf.Project, seq))
}

// lockBlockerFrom pulls the holder out of a 409 CONFLICT_LOCK_TAKEN so layer ② can tell them
// somebody is waiting (aihub#640 `notification_three_layers`).
//
// Best-effort by construction, and nil is a legitimate answer: the server's holder lookup is
// itself best-effort (internal/domain/resource_events.go says a refusal reported without a name
// is still a refusal), so the fields may be empty on the wire. A nil Blocker costs the note,
// never the skip.
func lockBlockerFrom(err error) *drain.Blocker {
	var apiErr *client.APIError
	if !errors.As(err, &apiErr) || apiErr.Code != "CONFLICT_LOCK_TAKEN" {
		return nil
	}
	var details struct {
		ConflictWith struct {
			ActorDisplay string `json:"actor_display"`
			WorkItemSlug string `json:"work_item_slug"`
		} `json:"conflict_with"`
	}
	if len(apiErr.Details) > 0 {
		_ = json.Unmarshal(apiErr.Details, &details)
	}
	b := &drain.Blocker{
		Actor:    details.ConflictWith.ActorDisplay,
		WorkItem: details.ConflictWith.WorkItemSlug,
		// The message is "resource <type>:<key> is already locked"; the key is the half an
		// operator can act on. Read off the message because the details object carries the
		// holder and not the resource.
		Resource: lockResourceFromMessage(apiErr.Message),
	}
	if b.Actor == "" && b.WorkItem == "" && b.Resource == "" {
		return nil
	}
	return b
}

const (
	lockMsgPrefix = "resource "
	lockMsgSuffix = " is already locked"
)

func lockResourceFromMessage(msg string) string {
	if !strings.HasPrefix(msg, lockMsgPrefix) || !strings.HasSuffix(msg, lockMsgSuffix) {
		return ""
	}
	return msg[len(lockMsgPrefix) : len(msg)-len(lockMsgSuffix)]
}

// projectScenarioURL reads the project's step-graph repo URL from aihub.
func projectScenarioURL(ctx context.Context, c *client.Client, project string) (string, error) {
	proj, err := c.GetProject(ctx, project)
	if err != nil {
		return "", fmt.Errorf("read project %q to find its scenario repo: %w", project, err)
	}
	url := str(proj["scenario"])
	if url == "" {
		return "", fmt.Errorf("project %q has no scenario repo configured, so no work item in it has a "+
			"step graph to execute; set one with `polyforge project update --scenario=<git url>`", project)
	}
	return url, nil
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

// attemptCredentials reads the three fields every credential-checked pf_* call carries, out of
// the state file THIS RUN's claim wrote.
//
// 🔴 This is the other half of aihub#667, and it did not work before it. Without a state file
// there was nothing to read, so drain sent step updates with no attempt identity at all and the
// server answered
//
//	400 BAD_REQUEST: attempt_id "" does not name an existing run attempt
//
// on the FIRST terminal step transition — measured on a real round, not predicted. The MCP tools
// have always done exactly this (internal/mcp/tools_step.go, tools_events.go): the state file is
// the one local record of who the attempt is, and a scheduler that claims without reading it back
// is claiming as nobody.
//
// Missing or unreadable is NOT fatal here: an unauthenticated call is refused by the server with a
// message that says so, which is a better failure than one invented locally. What must not happen
// is sending an EMPTY attempt_id, which is the shape the server rejects with the message above —
// so a blank id is omitted rather than sent.
func attemptCredentials(wiID string) map[string]any {
	sf, err := config.ResolveStateFile(wiID)
	if err != nil || sf == nil || sf.AttemptID == "" {
		return nil
	}
	return map[string]any{
		"attempt_id":     sf.AttemptID,
		"claim_epoch":    sf.ClaimEpoch,
		"session_secret": sf.SessionSecret,
	}
}

func (q *drainQueries) UpdateStep(ctx context.Context, wiID string, call engine.StepCall) error {
	body := map[string]any{"step_id": call.StepID, "status": call.Status}
	for k, v := range attemptCredentials(wiID) {
		body[k] = v
	}
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
	body := map[string]any{"status": status, "note": note}
	for k, v := range attemptCredentials(wiID) {
		body[k] = v
	}
	if status == "wrapped" {
		// 🔴 `derived` is REQUIRED on a wrap since aihub#350 (migration 0040) and the server
		// refuses without it — "an omitted list is refused because …". Sending nothing here made
		// every wrap fail, so a drain run could execute a work item perfectly and still end
		// FAILED on the last call; found running a real round for aihub#667.
		//
		// An EMPTY list is the honest value, not a placeholder: `derived` is the disposition of
		// findings this attempt noticed and did not fix, and a headless scheduler has no channel
		// on which a step agent can report one. Sending [] says "nothing was carried forward",
		// which is true of A today. When the step agent grows a structured return (aihub#640
		// `retro_and_crystallize` wants one for retro_worthy anyway), this is where its
		// dispositions belong.
		body["derived"] = []any{}
	}
	if _, err := q.c.CompleteAttempt(ctx, wiID, body); err != nil {
		return classifyHubError(err)
	}
	// Terminal statuses delete the local credential, exactly as pf_complete_attempt does
	// (internal/mcp/tools_lifecycle.go). Leaving it behind would hand the next round a state
	// file for an attempt that no longer exists, which is the shape every "invalid
	// session_secret" report starts from. Paused keeps it: a paused attempt is resumable.
	if status == "wrapped" || status == "failed" {
		if sf, err := config.ResolveStateFile(wiID); err == nil && sf != nil {
			_ = config.DeleteStateFile(sf.WIID)
			if sf.WIID != wiID {
				_ = config.DeleteStateFile(wiID)
			}
		}
	}
	return nil
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
	body := map[string]any{
		"work_item_id": target,
		"event_type":   "note",
		"payload":      map[string]any{"text": n.Note, "source": "polyforge drain"},
	}
	// 🔴 NO attempt credentials here, deliberately, and an earlier draft of this change got it
	// wrong. Neither Notify target is a work item this run holds an attempt on: one is the
	// BLOCKER's (internal/drain/runner.go, the "tell the holder somebody is waiting" half) and
	// the other is a blocked work item in scope that was never claimed. Attaching a credential
	// keyed on the target would not merely be useless — config.ResolveStateFile falls back to
	// matching on SLUG across every file in the state directory, and the runner passes a slug,
	// so on a live workspace (328 state files here) a stale or foreign record can resolve. The
	// server verifies the credential only when attempt_id is non-empty, so the effect of
	// attaching one is to turn a note that WOULD have landed unauthenticated into a 403 — on
	// the one channel that reaches another human in an unattended run.
	_, err := q.c.EmitEvent(ctx, body)
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
