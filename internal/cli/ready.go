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

// RunReady shows the ready queue for a project using the six-segment LCRS view.
// Usage: polyforge ready [--project=<name>] [--max=<n>] [--non-conflicting]
func RunReady(ctx context.Context, c *client.Client, cfg *config.Config, args []string) {
	project := defaultProject(cfg)
	maxItems := ""
	nonConflicting := false

	for _, a := range args {
		switch {
		case strings.HasPrefix(a, "--project="):
			project = a[len("--project="):]
		case strings.HasPrefix(a, "--max="):
			maxItems = a[len("--max="):]
		case a == "--non-conflicting":
			nonConflicting = true
		}
	}

	if project == "" {
		fmt.Fprintln(os.Stderr, "error: --project required (or set projects in .polyforge.yaml)")
		os.Exit(1)
	}

	params := url.Values{"project": []string{project}}
	if maxItems != "" {
		params.Set("max", maxItems)
	}
	if nonConflicting {
		params.Set("non_conflicting", "true")
	}

	result, err := c.GetReadyQueue(ctx, params)
	if err != nil {
		fmt.Fprintf(os.Stderr, "pf ready: %v\n", err)
		os.Exit(1)
	}

	// Six-segment LCRS view (§6 / §12).
	printReadySegment("items (auto)",         result["items"])
	printReadySegment("running",               result["running"])
	printReadySegment("stalled",               result["stalled"])
	printReadySegment("paused",                result["paused"])
	printReadySegment("needs you",             result["needs_human_session"])
	printReadySegment("unclassified",          result["unclassified"])
}

func printReadySegment(label string, items any) {
	arr, _ := items.([]any)
	fmt.Printf("--- %s (%d) ---\n", label, len(arr))
	for _, item := range arr {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		id, _ := m["id"].(string)
		slug, _ := m["slug"].(string)
		goal, _ := m["goal"].(string)
		display := id
		if slug != "" {
			display = slug
		}
		// Truncate long goals.
		if len(goal) > 72 {
			goal = goal[:69] + "..."
		}
		fmt.Printf("  %-20s  %s\n", display, goal)
	}
}
