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

	// Load .polyforge.yaml from POLYFORGE_WORKSPACE_ROOT or cwd.
	wsRoot := os.Getenv("POLYFORGE_WORKSPACE_ROOT")
	if wsRoot == "" {
		wsRoot = "."
	}
	cfg, err := config.Load(wsRoot)
	if err != nil {
		fmt.Fprintf(os.Stderr, "config load: %v\n", err)
		os.Exit(1)
	}

	// Resolve API key from env.
	apiKey := os.Getenv(cfg.AIHub.APIKeyEnv)
	if apiKey == "" {
		apiKey = os.Getenv("POLYFORGE_API_KEY")
	}
	if apiKey == "" {
		fmt.Fprintf(os.Stderr, "API key not set: env %s (or POLYFORGE_API_KEY)\n", cfg.AIHub.APIKeyEnv)
		os.Exit(1)
	}

	aihubURL := cfg.AIHub.URL
	if envURL := os.Getenv("POLYFORGE_AIHUB_URL"); envURL != "" {
		aihubURL = envURL
	}

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

	// Workspace root: prefer POLYFORGE_WORKSPACE_ROOT, fall back to cwd.
	wsRoot := os.Getenv("POLYFORGE_WORKSPACE_ROOT")
	if wsRoot == "" {
		var err error
		wsRoot, err = os.Getwd()
		if err != nil {
			wsRoot = "."
		}
	}

	// Load config (non-fatal for version/help).
	cfg, cfgErr := config.Load(wsRoot)

	// Build aihub client.
	var aihubClient *client.Client
	if cfg != nil {
		apiKey := os.Getenv(cfg.AIHub.APIKeyEnv)
		if apiKey == "" {
			apiKey = os.Getenv("POLYFORGE_API_KEY")
		}
		aihubURL := cfg.AIHub.URL
		if envURL := os.Getenv("POLYFORGE_AIHUB_URL"); envURL != "" {
			aihubURL = envURL
		}
		aihubClient = client.New(aihubURL, apiKey)
	} else if args[0] != "version" && args[0] != "help" {
		// For most commands we need a client; build one from env vars alone.
		apiKey := os.Getenv("POLYFORGE_API_KEY")
		aihubURL := os.Getenv("POLYFORGE_AIHUB_URL")
		if apiKey != "" && aihubURL != "" {
			aihubClient = client.New(aihubURL, apiKey)
		}
	}

	switch args[0] {
	case "init":
		if cfgErr != nil && aihubClient == nil {
			fmt.Fprintf(os.Stderr, "config: %v\n", cfgErr)
			os.Exit(1)
		}
		cli.RunInit(ctx, aihubClient, cfg, wsRoot, args[1:])
	case "doctor":
		cli.RunDoctor(ctx, aihubClient, cfg, wsRoot, args[1:])
	case "ready":
		if aihubClient == nil {
			fatalf("config: %v\n", cfgErr)
		}
		cli.RunReady(ctx, aihubClient, cfg, args[1:])
	case "stalled":
		if aihubClient == nil {
			fatalf("config: %v\n", cfgErr)
		}
		cli.RunStalled(ctx, aihubClient, cfg, args[1:])
	case "version":
		cli.RunVersion()
	case "complete-attempt":
		if aihubClient == nil {
			fatalf("config: %v\n", cfgErr)
		}
		cli.RunCompleteAttempt(ctx, aihubClient, wsRoot, args[1:])
	case "claim":
		if aihubClient == nil {
			fatalf("config: %v\n", cfgErr)
		}
		cli.RunClaim(ctx, aihubClient, cfg, args[1:])
	case "get-step":
		if aihubClient == nil {
			fatalf("config: %v\n", cfgErr)
		}
		cli.RunGetStep(ctx, aihubClient, args[1:])
	case "update-step":
		if aihubClient == nil {
			fatalf("config: %v\n", cfgErr)
		}
		cli.RunUpdateStep(ctx, aihubClient, args[1:])
	case "commit":
		cli.RunCommit(ctx, args[1:])
	case "push":
		cli.RunPush(ctx, args[1:])
	case "pr":
		cli.RunPR(ctx, args[1:])
	case "wrap":
		if aihubClient == nil {
			fatalf("config: %v\n", cfgErr)
		}
		cli.RunWrap(ctx, aihubClient, args[1:])
	case "help":
		printUsage()
	default:
		fmt.Fprintf(os.Stderr, "unknown command: %s\n", args[0])
		printUsage()
		os.Exit(1)
	}
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
  init [--apply]              Fetch scenario config → .polyforge/phase.yaml
  doctor [--fix]              5-item health check
  ready [--project=<p>]       Ready queue (6-segment LCRS view)
  stalled [--project=<p>]     Stalled work items
  version                     Print version

Work-item lifecycle:
  claim <id>                  Claim a work item
  complete-attempt            Mark attempt wrapped/failed (CI trap EXIT)
  wrap [--wi-id=<id>]         Push + PR + complete-attempt(wrapped)

Step management (machine-user):
  get-step [--wi-id=<id>]     Get current step
  update-step --step-id=<id> --status=<status>  Update step status

Git helpers (machine-user):
  commit [--wi-id=<id>] [--message=<msg>]  git commit in worktree
  push   [--wi-id=<id>]                    git push in worktree
  pr     [--wi-id=<id>] --title=<t>        gh pr create in worktree

Environment:
  POLYFORGE_WORKSPACE_ROOT   Workspace root (default: cwd)
  POLYFORGE_API_KEY          API key override
  POLYFORGE_AIHUB_URL        aihub URL override
  POLYFORGE_WORK_ITEM_ID     Active wi ID (for CI / complete-attempt)
  POLYFORGE_EXIT_CODE        Exit code to pass to complete-attempt
  CI                         Set to "true" in CI environments
`[1:])
}
