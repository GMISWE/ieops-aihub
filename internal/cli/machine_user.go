package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/GMISWE/ieops-aihub/internal/config"
	"github.com/GMISWE/ieops-aihub/pkg/client"
)

// ─── Claim ────────────────────────────────────────────────────────────────────

// RunClaim claims a work item via CLI (machine-user pattern).
// Usage: polyforge claim <wi_id> [--idempotency-key=<key>]
func RunClaim(ctx context.Context, c *client.Client, cfg *config.Config, args []string) {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: polyforge claim <wi_id> [--idempotency-key=<key>]")
		os.Exit(1)
	}
	wiID := args[0]
	idemKey := "cli-claim-" + wiID
	if len(wiID) > 8 {
		idemKey = "cli-claim-" + wiID[:8]
	}

	for _, a := range args[1:] {
		if strings.HasPrefix(a, "--idempotency-key=") {
			idemKey = a[len("--idempotency-key="):]
		}
	}

	body := map[string]any{
		"idempotency_key": idemKey,
	}

	result, err := c.ClaimWorkItem(ctx, wiID, body)
	if err != nil {
		fmt.Fprintf(os.Stderr, "claim: %v\n", err)
		os.Exit(1)
	}

	// Write state file.
	sf := &config.StateFile{
		WIID:          wiID,
		AttemptID:     fmt.Sprint(result["attempt_id"]),
		SessionSecret: fmt.Sprint(result["session_secret"]),
		Claimed:       true,
	}
	if epoch, ok := result["claim_epoch"].(float64); ok {
		sf.ClaimEpoch = int64(epoch)
	}

	if err := config.WriteStateFile(sf); err != nil {
		fmt.Fprintf(os.Stderr, "claim: write state file: %v\n", err)
		os.Exit(1)
	}

	b, _ := json.MarshalIndent(result, "", "  ")
	fmt.Println(string(b))
}

// ─── Get Step ─────────────────────────────────────────────────────────────────

// RunGetStep retrieves the current step for a work item.
// Usage: polyforge get-step [--wi-id=<id>]
func RunGetStep(ctx context.Context, c *client.Client, args []string) {
	wiID := resolveWIID(args, "--wi-id=")

	result, err := c.GetStep(ctx, wiID)
	if err != nil {
		fmt.Fprintf(os.Stderr, "get-step: %v\n", err)
		os.Exit(1)
	}

	b, _ := json.MarshalIndent(result, "", "  ")
	fmt.Println(string(b))
}

// ─── Update Step ──────────────────────────────────────────────────────────────

// RunUpdateStep updates the current step for a work item.
// Usage: polyforge update-step --step-id=<id> --status=<status>
//
//	[--wi-id=<wi_id>] [--step-attempt-id=<sa>] [--artifact-summary=<json>]
//	[--escalated] [--error-type=<type>] [--expected-version=<n>]
func RunUpdateStep(ctx context.Context, c *client.Client, args []string) {
	wiID := ""
	stepID := ""
	status := ""
	stepAttemptID := ""
	artifactSummary := ""
	escalated := false
	errorType := ""
	expectedVersion := ""

	for _, a := range args {
		switch {
		case strings.HasPrefix(a, "--wi-id="):
			wiID = a[len("--wi-id="):]
		case strings.HasPrefix(a, "--step-id="):
			stepID = a[len("--step-id="):]
		case strings.HasPrefix(a, "--status="):
			status = a[len("--status="):]
		case strings.HasPrefix(a, "--step-attempt-id="):
			stepAttemptID = a[len("--step-attempt-id="):]
		case strings.HasPrefix(a, "--artifact-summary="):
			artifactSummary = a[len("--artifact-summary="):]
		case a == "--escalated":
			escalated = true
		case strings.HasPrefix(a, "--error-type="):
			errorType = a[len("--error-type="):]
		case strings.HasPrefix(a, "--expected-version="):
			expectedVersion = a[len("--expected-version="):]
		}
	}

	if wiID == "" {
		wiID = resolveWIID(nil, "")
	}
	if stepID == "" || status == "" {
		fmt.Fprintln(os.Stderr, "update-step: --step-id and --status are required")
		os.Exit(1)
	}

	body := map[string]any{
		"step_id": stepID,
		"status":  status,
	}
	if stepAttemptID != "" {
		body["step_attempt_id"] = stepAttemptID
	}
	if artifactSummary != "" {
		var summary any
		if err := json.Unmarshal([]byte(artifactSummary), &summary); err != nil {
			body["artifact_summary"] = artifactSummary
		} else {
			body["artifact_summary"] = summary
		}
	}
	if escalated {
		body["escalated"] = true
	}
	if errorType != "" {
		body["error_type"] = errorType
	}
	if expectedVersion != "" {
		body["expected_version"] = expectedVersion
	}

	result, err := c.UpdateStep(ctx, wiID, body)
	if err != nil {
		fmt.Fprintf(os.Stderr, "update-step: %v\n", err)
		os.Exit(1)
	}

	b, _ := json.MarshalIndent(result, "", "  ")
	fmt.Println(string(b))
}

// ─── Commit ───────────────────────────────────────────────────────────────────

// RunCommit runs git commit inside the work item's worktree.
// Usage: polyforge commit [--wi-id=<id>] [--message=<msg>]
func RunCommit(ctx context.Context, args []string) {
	wiID := ""
	message := ""

	for _, a := range args {
		switch {
		case strings.HasPrefix(a, "--wi-id="):
			wiID = a[len("--wi-id="):]
		case strings.HasPrefix(a, "--message="):
			message = a[len("--message="):]
		}
	}

	wt := worktreePath(wiID)
	if wt == "" {
		fmt.Fprintln(os.Stderr, "commit: could not determine worktree path")
		os.Exit(1)
	}

	cmdArgs := []string{"-C", wt, "commit"}
	if message != "" {
		cmdArgs = append(cmdArgs, "-m", message)
	} else {
		fmt.Fprintln(os.Stderr, "commit: --message is required")
		os.Exit(1)
	}

	if err := runGit(ctx, cmdArgs...); err != nil {
		fmt.Fprintf(os.Stderr, "commit: %v\n", err)
		os.Exit(1)
	}
}

// ─── Push ─────────────────────────────────────────────────────────────────────

// RunPush runs git push inside the work item's worktree.
// Usage: polyforge push [--wi-id=<id>]
func RunPush(ctx context.Context, args []string) {
	wiID := ""
	for _, a := range args {
		if strings.HasPrefix(a, "--wi-id=") {
			wiID = a[len("--wi-id="):]
		}
	}

	wt := worktreePath(wiID)
	if wt == "" {
		fmt.Fprintln(os.Stderr, "push: could not determine worktree path")
		os.Exit(1)
	}

	if err := runGit(ctx, "-C", wt, "push", "--force-with-lease", "origin", "HEAD"); err != nil {
		fmt.Fprintf(os.Stderr, "push: %v\n", err)
		os.Exit(1)
	}
}

// ─── PR ───────────────────────────────────────────────────────────────────────

// RunPR creates a GitHub PR from the work item's worktree.
// Usage: polyforge pr [--wi-id=<id>] --title=<t> [--body=<b>]
func RunPR(ctx context.Context, args []string) {
	wiID := ""
	title := ""
	body := ""

	for _, a := range args {
		switch {
		case strings.HasPrefix(a, "--wi-id="):
			wiID = a[len("--wi-id="):]
		case strings.HasPrefix(a, "--title="):
			title = a[len("--title="):]
		case strings.HasPrefix(a, "--body="):
			body = a[len("--body="):]
		}
	}

	if title == "" {
		fmt.Fprintln(os.Stderr, "pr: --title is required")
		os.Exit(1)
	}

	wt := worktreePath(wiID)
	if wt == "" {
		fmt.Fprintln(os.Stderr, "pr: could not determine worktree path")
		os.Exit(1)
	}

	ghArgs := []string{"pr", "create", "--title", title}
	if body != "" {
		ghArgs = append(ghArgs, "--body", body)
	} else {
		ghArgs = append(ghArgs, "--body", "")
	}

	cmd := exec.CommandContext(ctx, "gh", ghArgs...)
	cmd.Dir = wt
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "pr: gh pr create: %v\n", err)
		os.Exit(1)
	}
}

// ─── Wrap ─────────────────────────────────────────────────────────────────────

// RunWrap pushes + creates a PR + marks the attempt as wrapped.
// Usage: polyforge wrap [--wi-id=<id>]
func RunWrap(ctx context.Context, c *client.Client, args []string) {
	wiID := ""
	for _, a := range args {
		if strings.HasPrefix(a, "--wi-id=") {
			wiID = a[len("--wi-id="):]
		}
	}

	if wiID == "" {
		wiID = os.Getenv("POLYFORGE_WORK_ITEM_ID")
	}
	if wiID == "" {
		states, _ := config.FindStateFiles()
		if len(states) == 1 {
			wiID = states[0].WIID
		} else if len(states) > 1 {
			fmt.Fprintln(os.Stderr, "wrap: multiple active work items; set --wi-id or POLYFORGE_WORK_ITEM_ID")
			os.Exit(1)
		}
	}
	if wiID == "" {
		fmt.Fprintln(os.Stderr, "wrap: no active work item")
		os.Exit(1)
	}

	sf, err := config.ReadStateFile(wiID)
	if err != nil {
		fmt.Fprintf(os.Stderr, "wrap: read state file: %v\n", err)
		os.Exit(1)
	}

	// 1. Push all worktrees.
	for _, wt := range sf.Worktrees {
		if err := runGit(ctx, "-C", wt, "push", "--force-with-lease", "origin", "HEAD"); err != nil {
			fmt.Fprintf(os.Stderr, "wrap: push %s: %v\n", wt, err)
			os.Exit(1)
		}
	}

	// 2. Complete attempt as wrapped.
	// The URL parameter is the WORK ITEM id, not the attempt id; previously this
	// called CompleteAttempt(ctx, sf.AttemptID, ...) which hit a 404. Also include
	// attempt_id in the body so the server can verify the credential.
	body := map[string]any{
		"attempt_id":     sf.AttemptID,
		"claim_epoch":    sf.ClaimEpoch,
		"session_secret": sf.SessionSecret,
		"status":         "wrapped",
	}
	result, err := c.CompleteAttempt(ctx, sf.WIID, body)
	if err != nil {
		fmt.Fprintf(os.Stderr, "wrap: complete-attempt: %v\n", err)
		os.Exit(1)
	}

	_ = config.DeleteStateFile(wiID)

	b, _ := json.MarshalIndent(result, "", "  ")
	fmt.Printf("wrapped %s\n%s\n", wiID, string(b))
}

// ─── Helpers ──────────────────────────────────────────────────────────────────

// resolveWIID extracts --<flag>=<id> from args, falling back to
// POLYFORGE_WORK_ITEM_ID and then the single state file.
func resolveWIID(args []string, flag string) string {
	for _, a := range args {
		if flag != "" && strings.HasPrefix(a, flag) {
			return a[len(flag):]
		}
	}
	if id := os.Getenv("POLYFORGE_WORK_ITEM_ID"); id != "" {
		return id
	}
	states, _ := config.FindStateFiles()
	if len(states) == 1 {
		return states[0].WIID
	}
	fmt.Fprintln(os.Stderr, "error: no work item ID (set --wi-id or POLYFORGE_WORK_ITEM_ID)")
	os.Exit(1)
	return ""
}

// worktreePath returns the first worktree path for the given wi_id from its
// state file, or empty string on failure.
func worktreePath(wiID string) string {
	if wiID == "" {
		wiID = resolveWIID(nil, "")
	}
	sf, err := config.ReadStateFile(wiID)
	if err != nil {
		return ""
	}
	for _, path := range sf.Worktrees {
		return path // return the first one
	}
	return ""
}

// runGit executes a git command with the given arguments, wiring stdout/stderr
// through to the terminal.
func runGit(ctx context.Context, args ...string) error {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}
