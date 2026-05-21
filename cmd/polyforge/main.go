// Command polyforge runs the polyforge v1 MCP server (stdio JSON-RPC 2.0).
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/GMISWE/ieops-aihub/internal/config"
	"github.com/GMISWE/ieops-aihub/internal/mcp"
	"github.com/GMISWE/ieops-aihub/internal/version"
	"github.com/GMISWE/ieops-aihub/pkg/client"
)

func main() {
	fmt.Fprintf(os.Stderr, "polyforge MCP server %s (%s)\n", version.Version, version.GitCommit)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// Load .polyforge.yaml from cwd
	cfg, err := config.Load(".")
	if err != nil {
		fmt.Fprintf(os.Stderr, "config load: %v\n", err)
		os.Exit(1)
	}

	// Resolve API key from env
	apiKey := os.Getenv(cfg.AIHub.APIKeyEnv)
	if apiKey == "" {
		// Also check POLYFORGE_API_KEY directly (CI / ephemeral runtime, §9.5.7)
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
