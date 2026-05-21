package cli

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"strings"

	"github.com/GMISWE/ieops-aihub/internal/config"
	"github.com/GMISWE/ieops-aihub/pkg/client"
)

// RunStalled shows stalled work items (running but idle >30 min) for a project.
// Usage: polyforge stalled [--project=<name>]
func RunStalled(ctx context.Context, c *client.Client, cfg *config.Config, args []string) {
	project := defaultProject(cfg)

	for _, a := range args {
		if strings.HasPrefix(a, "--project=") {
			project = a[len("--project="):]
		}
	}

	params := url.Values{}
	if project != "" {
		params.Set("project", project)
	}

	result, err := c.GetReadyQueue(ctx, params)
	if err != nil {
		fmt.Fprintf(os.Stderr, "pf stalled: %v\n", err)
		os.Exit(1)
	}

	stalled, _ := result["stalled"].([]any)
	if len(stalled) == 0 {
		fmt.Println("No stalled work items.")
		return
	}

	fmt.Printf("Stalled (%d):\n", len(stalled))
	for _, item := range stalled {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		id, _ := m["id"].(string)
		slug, _ := m["slug"].(string)
		goal, _ := m["goal"].(string)
		owner, _ := m["owner_display"].(string)
		display := id
		if slug != "" {
			display = slug
		}
		if len(goal) > 60 {
			goal = goal[:57] + "..."
		}
		fmt.Printf("  %-20s  [%s]  %s\n", display, owner, goal)
	}
}

// defaultProject picks the first project name from config (used when
// --project is not specified).
func defaultProject(cfg *config.Config) string {
	if cfg == nil {
		return ""
	}
	for name := range cfg.Projects {
		return name
	}
	return ""
}
