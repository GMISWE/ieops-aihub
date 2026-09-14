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

	// codex agent generation (aihub#642 plan step 10): codex DOES have a real
	// install-time hook (codex-hooks.json declares a SessionStart hook), but
	// wiring generation through that hook instead of this boot path is an
	// architectural change out of this wi's scope (tracked as a follow-up work
	// item) -- regenerating here, on every MCP server boot, was the pragmatic
	// choice that shipped with aihub#642 because every codex session already
	// boots this server fresh, giving boot-time generation the same
	// stay-in-sync effect a SessionStart hook would, without adding a second
	// invocation path. DefaultCodexAgentsDir only returns ok=true when cwd
	// genuinely looks like the codex plugin tree (see its doc comment), so
	// this is a silent no-op under CC/pi/local-dev invocations that don't.
	// Errors are a warning only, NEVER fatal: role generation must not be able
	// to break MCP server startup for anyone.
	if codexAgentsDir, ok := cli.DefaultCodexAgentsDir(); ok {
		if err := cli.GenerateRoles(mc, "codex", codexAgentsDir); err != nil {
			fmt.Fprintf(os.Stderr, "polyforge: codex agent generation failed (non-fatal, server continues): %v\n", err)
		}
	}

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
	case "roles":
		// No aihubClient needed: purely local (embedded role YAMLs + mc.Roles).
		cli.RunRolesGenerate(mc, args[1:])
	case "engine":
		// No aihubClient needed: every `engine` verb is local-only (internal/engine +
		// internal/roles + git), for a future headless orchestrator (aihub#654).
		cli.RunEngine(ctx, args[1:])
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

Role/tier agent generation (aihub#642):
  roles generate <pi|codex> --out <dir>
                              Render per-role agent files (pf-<role>.md for pi,
                              step-<role>.toml for codex) from
                              internal/roles/definitions/*.yaml and this
                              machine's ~/.polyforge/config.toml [roles.tiers]
                              candidate lists. Claude Code's step-<role>.md
                              files are generated at build time instead
                              (committed via "go generate ./internal/roles/...")
                              -- this subcommand never touches them.

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
