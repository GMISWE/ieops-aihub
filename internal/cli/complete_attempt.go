package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/GMISWE/ieops-aihub/internal/config"
	"github.com/GMISWE/ieops-aihub/pkg/client"
)

// RunCompleteAttempt is the CI trap EXIT command.
//
// Usage:
//
//	polyforge complete-attempt [--status=wrapped|failed|paused]
//	                           [--wi-id=<wi_id>]
//	                           [--force-terminate-step]
//	                           [--reason=<text>]
//
// State-file discovery order (§12):
//  1. POLYFORGE_WORKSPACE_ROOT/.polyforge/state/*.json
//  2. cwd/.polyforge/state/*.json (upward search)
//  3. POLYFORGE_WORK_ITEM_ID env var
//
// When CI=true and POLYFORGE_EXIT_CODE != "0", status is forced to "failed".
func RunCompleteAttempt(ctx context.Context, c *client.Client, wsRoot string, args []string) {
	status := "wrapped"
	wiID := ""
	reason := ""
	forceTerminateStep := false

	for _, a := range args {
		switch {
		case strings.HasPrefix(a, "--status="):
			status = a[len("--status="):]
		case strings.HasPrefix(a, "--wi-id="):
			wiID = a[len("--wi-id="):]
		case strings.HasPrefix(a, "--reason="):
			reason = a[len("--reason="):]
		case a == "--force-terminate-step":
			forceTerminateStep = true
		}
	}
	_ = forceTerminateStep // reserved for future use

	// Auto-discover wi_id.
	if wiID == "" {
		wiID = os.Getenv("POLYFORGE_WORK_ITEM_ID")
	}
	if wiID == "" {
		states, _ := config.FindStateFiles()
		switch len(states) {
		case 0:
			fmt.Fprintln(os.Stderr, "complete-attempt: no active work item (no state files and POLYFORGE_WORK_ITEM_ID not set)")
			os.Exit(1)
		case 1:
			wiID = states[0].WIID
		default:
			fmt.Fprintln(os.Stderr, "complete-attempt: multiple active work items; set --wi-id or POLYFORGE_WORK_ITEM_ID")
			var ids []string
			for _, s := range states {
				ids = append(ids, s.WIID)
			}
			fmt.Fprintf(os.Stderr, "  found: %s\n", strings.Join(ids, ", "))
			os.Exit(1)
		}
	}

	// In CI, override status to "failed" when exit code is non-zero.
	if os.Getenv("CI") == "true" {
		exitCode := os.Getenv("POLYFORGE_EXIT_CODE")
		if exitCode != "" && exitCode != "0" {
			status = "failed"
		}
	}

	sf, err := config.ReadStateFile(wiID)
	if err != nil {
		fmt.Fprintf(os.Stderr, "complete-attempt: read state file: %v\n", err)
		os.Exit(1)
	}

	// Optionally emit a note event before completing.
	if reason != "" {
		noteBody := map[string]any{
			"work_item_id": wiID,
			"attempt_id":   sf.AttemptID,
			"event_type":   "note",
			"pinned":       false,
			"payload":      map[string]any{"text": reason},
		}
		if _, err := c.EmitEvent(ctx, noteBody); err != nil {
			// Non-fatal: log but continue.
			fmt.Fprintf(os.Stderr, "complete-attempt: emit note: %v (continuing)\n", err)
		}
	}

	body := map[string]any{
		"claim_epoch":    sf.ClaimEpoch,
		"session_secret": sf.SessionSecret,
		"status":         status,
	}

	result, err := c.CompleteAttempt(ctx, sf.AttemptID, body)
	if err != nil {
		fmt.Fprintf(os.Stderr, "complete-attempt: %v\n", err)
		os.Exit(1)
	}

	// Remove state file for terminal states.
	if status == "wrapped" || status == "failed" {
		_ = config.DeleteStateFile(wiID)
	}

	b, _ := json.MarshalIndent(result, "", "  ")
	fmt.Printf("attempt %s → %s\n%s\n", sf.AttemptID, status, string(b))
}
