// Command polyforge runs the polyforge v1 MCP server (stdio JSON-RPC 2.0)
// or executes a CLI subcommand when arguments are provided.
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/GMISWE/ieops-aihub/internal/cli"
	"github.com/GMISWE/ieops-aihub/internal/config"
	"github.com/GMISWE/ieops-aihub/internal/mcp"
	"github.com/GMISWE/ieops-aihub/internal/version"
	"github.com/GMISWE/ieops-aihub/pkg/client"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// CLI mode: any argument other than "serve" triggers CLI dispatch.
	if len(os.Args) > 1 && os.Args[1] != "serve" {
		runCLI(ctx, os.Args[1:])
		return
	}

	// MCP server mode (no args, or explicit "serve").
	fmt.Fprintf(os.Stderr, "polyforge MCP server %s (%s)\n", version.Version, version.GitCommit)

	// Load ~/.polyforge/config.toml (machine-level, §9.5.3).
	// EnsureMachineConfig also generates a stable machine_id on first run.
	mc, err := config.EnsureMachineConfig()
	if err != nil {
		fmt.Fprintf(os.Stderr, "config.toml: %v\n", err)
		os.Exit(1)
	}

	// Machine-config pre-flight, ONCE for the whole boot (aihub#681).
	//
	// 🔴 These two warnings describe the CONFIG, not any one harness, and they
	// used to live inside the per-harness generators. generateCodexProfiles
	// printed them, and so did the new cc path, so a machine with codex on
	// PATH got two copies of every tier-table problem and two copies of the
	// six-line ~/.polyforge/roles notice on every single serve boot (measured
	// before hoisting them here). One boot, one diagnosis.
	//
	// Hoisting also FIXES a gap rather than merely deduplicating. Before this,
	// both warnings were reachable only from `polyforge roles generate` and
	// from codex profile generation, the latter gated behind
	// exec.LookPath("codex") -- so on a machine running only Claude Code,
	// nothing ever validated the tier table at all, and a `harness = "claude"`
	// typo was unreportable exactly where it was most likely to be a typo for
	// "cc". Every serve boot reaches this line.
	//
	// A tier-table error (an unknown preset) is NOT reported here: the
	// generators below return it as a real error with their own context, and
	// duplicating it is the thing this block exists to stop.
	if tiers, tierSource, tiersErr := mc.ResolveTiers(""); tiersErr == nil {
		for _, problem := range config.ValidateCandidates(tiers) {
			fmt.Fprintf(os.Stderr, "polyforge: WARNING: %s (%s)\n", problem, tierSource)
		}
	}
	if path, present := config.UnreadRolesOverrideDir(); present {
		fmt.Fprint(os.Stderr, config.RolesOverrideIgnoredWarning(path))
	}

	// Role agent definitions: regenerate them for every harness this machine has
	// both INSTALLED and CONFIGURED (aihub#683, owner decision 2026-09-15).
	//
	// 🔴 IN A GOROUTINE, AND THAT IS A CORRECTNESS DECISION RATHER THAN A
	// PERFORMANCE ONE. This statement replaces two blocks: a $CODEX_HOME profile
	// write gated on exec.LookPath("codex"), and aihub#681's Claude Code
	// regeneration gated on $CLAUDE_PLUGIN_ROOT. Generalising those to four
	// harnesses means up to three model-catalog subprocesses, and cli's
	// catalogProbeTimeout records the measurement: opencode's catalog takes 8.2s
	// cold and 3.4s warm, pi's 1.6s/0.91s. Serially and synchronously, a fully
	// configured machine would wait 4.5-10s here before the MCP server speaks a
	// byte -- in EVERY session on the machine, since each starts its own server.
	// That is aihub#679's outage with a slower fuse, and aihub#679's own comment
	// argued a timeout was the fix "NOT backgrounding it" on the grounds that
	// backgrounding would lose an ordering codex profile generation depends on.
	// That ordering claim does not survive contact with the other three harnesses:
	// every harness reads these files at ITS OWN startup, which for the session
	// that launched this process already happened -- aihub#681 measured precisely
	// this for Claude Code and documented it as "takes effect in the NEXT
	// session". Nothing downstream of here reads them either.
	//
	// ⚠️ The cost of backgrounding, stated so it is not discovered later: a
	// process that exits before the goroutine finishes skips that boot's
	// regeneration. Both exits that can beat it are already terminal for this
	// session (no API key below, or a server error), and the next boot redoes the
	// work, so the failure mode is "one session later", which is the latency the
	// feature already has.
	//
	// Non-fatal by contract: SyncRolesOnStartup writes warnings and never exits.
	go cli.SyncRolesOnStartup(mc)

	// Load .polyforge.yaml from POLYFORGE_WORKSPACE_ROOT, or by walking up from
	// cwd to find .polyforge.yaml (non-fatal). When config.toml has api_key +
	// server.url the workspace config is optional, allowing the MCP server to
	// run from any directory (global plugin install).
	wsRoot := os.Getenv("POLYFORGE_WORKSPACE_ROOT")
	if wsRoot == "" {
		wsRoot = config.FindWorkspaceRoot()
	}
	cfg, _ := config.Load(wsRoot) // non-fatal: config.toml takes priority

	// Resolve API key: POLYFORGE_API_KEY > config.toml [auth] > .polyforge.yaml api_key_env.
	apiKey := mc.ResolveAPIKey()
	if apiKey == "" && cfg != nil {
		apiKey = os.Getenv(cfg.AIHub.APIKeyEnv)
	}
	if apiKey == "" {
		fmt.Fprintf(os.Stderr, "API key not set: configure ~/.polyforge/config.toml [auth] api_key\n")
		os.Exit(1)
	}

	// Resolve aihub URL: POLYFORGE_AIHUB_URL > config.toml [server] >
	// .polyforge.yaml > the endpoint compiled into this binary (aihub#335).
	aihubURL, _ := config.EffectiveAihubURL(mc, workspaceAihubURL(cfg))

	aihubClient := client.New(aihubURL, apiKey)
	server := mcp.New(cfg, aihubClient)

	if err := server.Serve(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "server error: %v\n", err)
		os.Exit(1)
	}
}

func runCLI(ctx context.Context, args []string) {
	if len(args) == 0 {
		printUsage()
		return
	}

	// Workspace root: prefer POLYFORGE_WORKSPACE_ROOT, then walk up from cwd
	// to find .polyforge.yaml (same logic as FindWorkspaceRoot).
	wsRoot := os.Getenv("POLYFORGE_WORKSPACE_ROOT")
	if wsRoot == "" {
		wsRoot = config.FindWorkspaceRoot()
	}

	// Load config.toml + .polyforge.yaml (non-fatal for version/help).
	//
	// The error is dropped on purpose — `version` and `help` must work on a
	// machine with no config at all — but the VALUE has to be made safe, which
	// it was not: EnsureMachineConfig returns (nil, err) when config.toml does
	// not parse, and the next line dereferences it. A single stray character in
	// that file therefore took down every subcommand with a nil-pointer panic,
	// including the one you run to find out what is wrong with your config
	// (`polyforge doctor`, whose whole job is to survive a broken workspace).
	mc, mcErr := config.EnsureMachineConfig()
	if mc == nil {
		fmt.Fprintf(os.Stderr, "polyforge: %s could not be loaded (%v); "+
			"continuing as if it were empty — fix that file, or move it aside\n",
			config.MachineConfigPath(), mcErr)
		mc = &config.MachineConfig{}
	}
	cfg, cfgErr := config.Load(wsRoot)

	// Build aihub client; credential precedence resolved by ResolveAPIKey /
	// EffectiveAihubURL (env override > config.toml > .polyforge.yaml >
	// built-in default, §9.5.3 + aihub#335).
	var aihubClient *client.Client
	apiKey := mc.ResolveAPIKey()
	if apiKey == "" && cfg != nil {
		apiKey = os.Getenv(cfg.AIHub.APIKeyEnv)
	}
	aihubURL, _ := config.EffectiveAihubURL(mc, workspaceAihubURL(cfg))
	// The key is now the ONLY thing that can leave the client nil:
	// EffectiveAihubURL never returns "". The URL used to be a second input to
	// the same outcome, which is why noApiKey below can state the cause flatly
	// instead of printing whichever config error happened to be lying around.
	if apiKey != "" {
		aihubClient = client.New(aihubURL, apiKey)
	}

	switch args[0] {
	case "init":
		if cfgErr != nil && aihubClient == nil {
			fmt.Fprintf(os.Stderr, "config: %v\n", cfgErr)
			os.Exit(1)
		}
		// RunInit does not guard on a nil client — it goes straight to
		// client.WhoAmI — so with a VALID .polyforge.yaml and no key it wrote
		// .polyforge/usage.md and then panicked, leaving the workspace
		// half-initialised. Refuse before touching anything.
		if aihubClient == nil {
			fatalf("%s", noAPIKey)
		}
		cli.RunInit(ctx, aihubClient, cfg, wsRoot, args[1:])
	case "doctor":
		// Deliberately NOT gated on the client: reporting that the key is
		// missing, and against which endpoint, is one of the things doctor is
		// for. checkConfig handles nil.
		cli.RunDoctor(ctx, aihubClient, cfg, wsRoot, args[1:])
	case "version":
		cli.RunVersion()
	case "get-step":
		if aihubClient == nil {
			fatalf("%s", noAPIKey)
		}
		cli.RunGetStep(ctx, aihubClient, args[1:])
	case "update-step":
		if aihubClient == nil {
			fatalf("%s", noAPIKey)
		}
		cli.RunUpdateStep(ctx, aihubClient, args[1:])
	case "dump-mcp-schemas":
		gitSHA := ""
		if len(args) > 1 {
			gitSHA = args[1]
		}
		if err := cli.RunDumpMCPSchemas(ctx, gitSHA, os.Stdout); err != nil {
			fmt.Fprintf(os.Stderr, "dump-mcp-schemas: %v\n", err)
			os.Exit(1)
		}
	case "commit":
		cli.RunCommit(ctx, args[1:])
	case "push":
		cli.RunPush(ctx, args[1:])
	case "pr":
		cli.RunPR(ctx, args[1:])
	case "artifact":
		if aihubClient == nil {
			fatalf("%s", noAPIKey)
		}
		cli.RunArtifact(ctx, aihubClient, args[1:])
	case "skills":
		// Explicit registry bootstrap/import. The caller's ordinary API key is
		// the authority; project-scoped keys are refused by the server and every
		// published version remains private until a separate sharing action.
		if aihubClient == nil {
			fatalf("%s", noAPIKey)
		}
		cli.RunSkills(ctx, aihubClient, args[1:])
	case "roles":
		// No aihubClient needed: purely local (embedded role YAMLs + mc.Roles).
		//
		// Two verbs, and the split is not cosmetic. `generate` renders ONE
		// harness into a directory the CALLER names, which is what an install
		// script wants. `install` knows where every harness on this machine keeps
		// its files and keeps them current, which is what a human wants -- and is
		// the same code path `polyforge serve` runs unasked at startup, so the
		// manual and automatic routes cannot diverge (aihub#683).
		if len(args) > 1 && args[1] == "install" {
			cli.RunRolesInstall(mc, args[2:])
			return
		}
		cli.RunRolesGenerate(mc, args[1:])
	case "engine":
		// No aihubClient needed: every `engine` verb is local-only (internal/engine +
		// internal/roles + git), for a future headless orchestrator (aihub#654).
		cli.RunEngine(ctx, args[1:])
	case "drain":
		// Layer 3 continuous scheduler (aihub#640). Needs a client: every scheduling
		// decision it makes is a question about server state.
		if aihubClient == nil {
			fatalf("%s", noAPIKey)
		}
		cli.RunDrain(ctx, aihubClient, wsRoot, args[1:])
	case "watch":
		// Deliberately NOT gated on the client, and it is not an oversight: watch reads a
		// local snapshot and makes no network call at all (aihub#640
		// `watch_is_light_because_of_datasource`), so requiring a credential would make the
		// observer unavailable in exactly the situations it exists for.
		cli.RunWatch(ctx, args[1:])
	case "help":
		printUsage()
	default:
		fmt.Fprintf(os.Stderr, "unknown command: %s\n", args[0])
		printUsage()
		os.Exit(1)
	}
}

// noAPIKey is what every client-requiring subcommand says when it has no
// client. Since aihub#335 that has exactly one cause — EffectiveAihubURL always
// supplies an endpoint, so the key is the only missing input — which is why this
// can name the cause instead of printing whatever config error happened to be
// in scope. Those call sites used to print `config: %v` with cfgErr, and on the
// path that actually reaches them cfgErr is nil, so the message they emitted was
// the literal "config: <nil>".
const noAPIKey = "polyforge: no API key. Put it in ~/.polyforge/config.toml under\n" +
	"  [auth]\n  api_key = \"pf_k1_…\"\n" +
	"or export POLYFORGE_API_KEY. The server address is built in; you do not need to set one.\n"

// workspaceAihubURL is the .polyforge.yaml aihub.url, or "" when the workspace
// config is absent or unparseable (both non-fatal: config.toml and the built-in
// default cover the MCP server running outside a workspace).
func workspaceAihubURL(cfg *config.Config) string {
	if cfg == nil {
		return ""
	}
	return cfg.AIHub.URL
}

func fatalf(format string, a ...any) {
	fmt.Fprintf(os.Stderr, format, a...)
	os.Exit(1)
}

func printUsage() {
	fmt.Fprintln(os.Stderr, `polyforge - polyforge v1 workspace tool

Usage:
  polyforge [serve]           Start the MCP server (default)
  polyforge <command> [args]  Run a CLI command

Workspace commands:
  init                        Set up/repair workspace (usage.md, session hook, clone repos, CLAUDE.md)
  doctor [--fix] [--force-remove=<dir>[:<status>][,...]]
                              7-item health check. --fix removes orphan worktrees, but only
                              those whose work item is provably terminal (wrapped/failed/
                              cancelled); anything else is printed with its status and KEPT.
                              --force-remove overrides that for a named directory; if its
                              work item is still active the flag must also carry that
                              status. See "polyforge doctor --help" for the detail.
  version                     Print version

Schema export:
  dump-mcp-schemas [<git-sha>]   Print all registered MCP tool schemas as contract JSON

Step management (machine-user):
  get-step [--wi-id=<id>]     Get current step
  update-step --step-id=<id> --status=<status>  Update step status

Git helpers (machine-user):
  commit [--wi-id=<id>] [--message=<msg>]  git commit in worktree
  push   [--wi-id=<id>]                    git push in worktree
  pr     [--wi-id=<id>] --title=<t>        gh pr create in worktree

Artifact viewer:
  artifact view <memory_id>   Fetch spec/plan HTML and open in browser

Skill registry (authenticated, private by default):
  skills seed                 Idempotently publish the canonical eight bundles
                              with expected-latest CAS; prints exact version pins.
  skills import --name=<name> --root=<dir> --entry=<path>
                --contract-file=<json> --license-name=<SPDX>
                [--license-url=<url>] [--license-notice-file=<path>]
                [--provenance-source=<text>] [--upstream-url=<url>]
                [--upstream-commit=<sha>] [--upstream-license=<SPDX>]
                [--provenance-notes=<text>]
                              Import a deterministic closed local bundle. An
                              unchanged re-import is a no-op; no grant or public
                              visibility is created.

Role/tier agent generation (aihub#642):
  roles install [--harness <h>] [--dry-run] [--preset=<name>]
                              Put this machine's role agent definitions where each
                              harness actually reads them, using one built-in table
                              of default directories (aihub#683):
                                pi        ${PI_AGENT_DIR:-~/.pi/agent}/agents
                                opencode  ${POLYFORGE_OPENCODE_AGENT_DIR:-
                                            ${OPENCODE_CONFIG_DIR:-
                                              ${XDG_CONFIG_HOME:-~/.config}/opencode}/agent}
                                codex     ${CODEX_HOME:-~/.codex}
                                cc        $CLAUDE_PLUGIN_ROOT/agents
                              With no --harness it regenerates ONLY for harnesses
                              whose directory ALREADY EXISTS and which this
                              machine's tier table actually names: polyforge never
                              creates a harness directory it was not asked for.
                              Naming --harness <h> is that explicit ask, and will
                              create the directory. --dry-run lists the exact paths
                              and writes nothing. Writes are atomic (temp file +
                              rename) and files already matching are left untouched.
                              This is the SAME code "polyforge serve" runs at
                              startup, so the manual and automatic routes cannot
                              diverge; run it (or /pf-update) when you have just
                              edited ~/.polyforge/config.toml and do not want to
                              wait for the next session.
  roles generate <pi|codex|opencode> --out <dir> [--preset=<name>]
                              Render per-role agent files (step-<role>.md for pi
                              and opencode, step-<role>.toml for codex) from
                              internal/roles/definitions/*.yaml and
                              this machine's ~/.polyforge/config.toml
                              [roles.tiers] candidate lists. Claude Code has no
                              form of this verb: its step-<role>.md files are
                              regenerated in place inside the installed plugin,
                              not written to an --out directory. The committed
                              defaults come from "go generate
                              ./internal/roles/..." and any machine-local
                              override is applied by "polyforge serve" at
                              startup (see below).
                              --preset generates from a named tier table
                              instead of the machine's configured selection.
                              NOTE: the codex form writes files nothing loads
                              (codex has no agent-file auto-discovery,
                              aihub#655) -- see the profile section below.
                              ~/.polyforge/roles/ is NOT read: there is no
                              user-override layer over the role definitions
                              (aihub#676).

Tier-table presets (aihub#673): a preset is a NAMED SNAPSHOT of the whole
tier->model table, and selecting one SWAPS the table rather than merging with
it. Every reader honours the same selection -- "roles generate", the
serve-startup codex profile and Claude Code agent generation, and "polyforge
drain" -- so one machine cannot resolve different models depending on which
entry point ran:
    [roles]
    preset = "frugal"              # this machine's default
    [roles.presets.frugal.tiers]
    default = [{ harness = "pi", model = "sub2api-anthropic/claude-haiku-4-5" }]
An unknown preset name is refused, never silently ignored.

Model identifiers here are HARNESS-NATIVE. Claude Code (harness = "cc") takes a
bare alias or full model id and never a provider prefix; codex takes its own
bare slug. For pi and opencode they are also MACHINE-LOCAL: write the full
"<provider>/<model>", never the bare model id.
aihub#676 measured pi 0.85.1 rejecting a bare id outright once more than one
authenticated provider offers it, and -- worse -- resolving a bare id that only
one provider visibly offers to a DIFFERENT channel than the intended one. The
provider name is whatever you called it when you configured that harness, which
is why this table lives in ~/.polyforge/config.toml and not in the repo.
"polyforge roles generate" warns about a bare id rather than silently writing
an agent file that cannot start.

Startup auto-sync (aihub#683): every "polyforge serve" boot runs exactly what
"roles install" above runs, in the background, for every harness whose directory
already exists AND which this machine's tier table names. Neither the boot nor
the command can drift from the other, because they are one code path. It is
deliberately silent when it changes nothing, and it never blocks the MCP server
from starting: its model-catalog probes are subprocesses, and "opencode models"
alone was measured at 8.2s cold on the machine this was written on.

Codex profile auto-generation (aihub#655, part of the startup sync above): when
the codex CLI is on PATH, every "polyforge serve" boot (re)writes
$CODEX_HOME/step-<role>.config.toml (default ~/.codex), one per
role, containing model/sandbox_mode/developer_instructions -- the codex
config-profile shape codex's own "-p <name>" / "--profile <name>" flag
loads. This is a different file (and a different, actually-scanned
directory) than "roles generate codex --out <dir>" above writes; see
internal/roles/render_codex.go's RenderCodexProfiles for why both exist.

Claude Code agent auto-generation (aihub#681, part of the startup sync above): a
tier may name harness = "cc", which overrides the repo-committed
internal/roles/definitions/cc_aliases.yaml alias for that tier ON THIS MACHINE.
When $CLAUDE_PLUGIN_ROOT points at an installed plugin and at least one such
candidate exists, every "polyforge serve" boot rewrites that plugin's five
agents/step-<role>.md files from this machine's table; a tier with no cc
candidate keeps the committed alias, and a machine with no cc candidate at all
is left byte-for-byte untouched. Write a bare alias or full model id
("sonnet", "claude-sonnet-4-5"), never a "<provider>/<model>" pair; one that
cannot be written into the "model:" line is refused with a warning and the
field omitted, never rewritten into a guess.
  It takes effect in the NEXT Claude Code session, not the one doing the
  writing: plugin agent definitions are read once, before this server has
  started, and are not re-read afterwards (measured, CC 2.1.258). Running
  "polyforge roles install" (or /pf-update) mid-session does not change that --
  it writes the files immediately, but the session that is running has already
  read them.
  A "/plugin marketplace update" moves the plugin to a new versioned cache
  directory and the generated files do not follow; the next boot regenerates
  them, so this self-heals one session later.

Engine (aihub#654, local-only, for a future headless orchestrator):
  engine startup --workspace-root=<dir> --worktree-root=<dir> --scenario-url=<url>
                 --wi-type=<type> [--project=<name>]
                              Resolve scenario path (owner-qualified, legacy-fallback),
                              pin its HEAD sha, resolve+scan the wi_type template into
                              steps, expand each step's @include:/level: pairs, and write
                              <worktree-root>/.pf_meta.json. Prints scenario_path,
                              legacy_fallback, sha, template_source and the step list.
  engine resolve-role --step-id=<id> [--declared-role=<name>]
                              Run the 3-tier role fallback (declared -> catalog ->
                              heuristic) against the real internal/roles catalog; never
                              bottoms out at "executor". Prints role, tier, read_only,
                              source, and unknown_declared_role (set only when
                              --declared-role was given but unknown to the catalog).
  engine parse-review [--file=<path>]
                              Parse a REVIEW_RESULT marker (last one wins if several;
                              WARN if none) from --file, or stdin if omitted. Prints
                              result.
  engine bracket-plan --step-id=<id> --status=<completed|failed> --step-attempt-id=<sa>
                       [--next-step-id=<id>] [--next-step-attempt-id=<sa>]
                       [--supports-next-step] [--artifact-summary=<text>]
                       [--error-type=<type>]
                              Plan the pf_update_step call sequence for completing/failing
                              a step (fused single call when the connected server supports
                              next_step, else the degraded two-call form). Prints a JSON
                              array of calls.
  engine cleanup-worktrees --workspace-root=<dir> --worktrees=<json-map-repo-to-path>
                              Remove each repo's worktree (git worktree remove --force,
                              run from <workspace-root>/.repo/<repo-name>, the repo's
                              main clone, so a worktree already deleted by an
                              interrupted prior cleanup is still pruned), then the
                              shared parent directory once, after the loop,
                              unconditionally (even if a per-repo removal failed).
                              Best-effort: always exits 0, reporting what succeeded in
                              "removed" and every failure in "errors" (one entry per
                              failing repo, plus "(parent)" if the shared-parent
                              removal itself failed). Prints removed, errors.

Layer 3 continuous scheduling (aihub#640):
  drain --project=<name> [--all] [--plan] [--max-parallel=<n>]
        [--max-rounds=<n>] [--max-work-items=<n>] [--channel=<h[/model],...>]
        [--preset=<name>] [--detach] [--json]
                              Repeatedly select the work items that are executable
                              right now (queued, rhs=false, no unfinished blocking
                              dependency, in scope), run them across rounds, and stop
                              with a terminal state. Exit codes: 0 COMPLETED,
                              10 IDLE, 11 BLOCKED_EXTERNAL, 12 FAILED.
                              --plan reports what WOULD run and claims nothing.
                              --preset picks the tier->model table to dispatch with
                              (see [roles.presets] below); --channel still wins
                              wherever it names a model.
                              --detach runs it in the background (setsid; there is no
                              systemd here) and prints run id + pid + log path.
  drain --stop [--run=<id>]   Stop the running drain on this machine -- the most
                              recent run, or the one named by --run. Sends SIGTERM,
                              the same signal Ctrl-C sends, so the run takes its
                              normal graceful-cancellation path and writes a proper
                              terminal snapshot. It NEVER escalates to SIGKILL:
                              that would skip the final snapshot write and leave the
                              run looking like a crash.
                              Work items whose step was in flight are left CLAIMED
                              and holding their locks (unchanged from Ctrl-C); --stop
                              names them so they can be recovered.
                              Deliberately a drain flag, not a top-level "halt":
                              /pf-stop is the WORK-ITEM lifecycle verb
                              (--pause/--wrap/--fail) and must not be shadowed.
  watch [--run=<id>] [--follow] [--list] [--json]
                              Show what a running drain is doing. Reads only that
                              run's local snapshot: zero network, so it never hangs
                              on a slow server. This is the PROCESS's view of one
                              run on this machine, not the project's -- for the
                              repository view use /pf-status.

Config files (§9.5.3):
  ~/.polyforge/config.toml   Machine-level config (machine_id, [auth] api_key)
  .polyforge.yaml            Workspace config (aihub.url, scenario, projects)

  config.toml [auth] example:
    machine_id = "<auto-generated UUID>"
    [auth]
    api_key = "your-key-here"
    # OR: api_key_env = "POLYFORGE_API_KEY"

  The api_key is the ONLY thing you have to write by hand. The team's aihub
  endpoint is compiled into this binary, so no document has to carry it and no
  copy of it can go stale on your machine; "polyforge doctor" prints the
  endpoint in use and where it came from. Add a [server] url only to point at
  a different aihub:
    [server]
    url = "http://your-own-aihub:8080"

Environment (overrides config.toml):
  POLYFORGE_WORKSPACE_ROOT   Workspace root (default: cwd)
  POLYFORGE_API_KEY          API key override (highest priority)
  POLYFORGE_AIHUB_URL        aihub URL override (else: [server] url,
                             .polyforge.yaml aihub.url, then the built-in
                             default)
  POLYFORGE_MACHINE_ID       Machine ID override (CI containers)
  POLYFORGE_WORK_ITEM_ID     Active wi ID (used by get-step/update-step/commit/push/pr)
  CI                         Set to "true" in CI environments
`[1:])
}
