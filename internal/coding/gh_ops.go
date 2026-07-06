package coding

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
)

// GHCreatePR runs `gh pr create` in the worktree directory.
// Returns the PR URL and number.
func GHCreatePR(ctx context.Context, worktreePath, title, body, head, base string) (map[string]any, error) {
	args := []string{"pr", "create", "--title", title, "--body", body}
	if head != "" {
		args = append(args, "--head", head)
	}
	if base != "" {
		args = append(args, "--base", base)
	}
	cmd := exec.CommandContext(ctx, "gh", args...)
	cmd.Dir = worktreePath

	out, err := cmd.CombinedOutput()
	if err != nil {
		// Check if PR already exists
		outStr := string(out)
		if strings.Contains(outStr, "already exists") {
			// Return existing PR info
			return map[string]any{"existing": true, "message": outStr}, nil
		}
		return nil, fmt.Errorf("gh pr create: %w\n%s", err, outStr)
	}

	var result map[string]any
	if err := json.Unmarshal(out, &result); err != nil {
		// gh may output just the URL
		return map[string]any{"url": strings.TrimSpace(string(out))}, nil
	}
	return result, nil
}

// GHGetPR returns existing PR info for the given head branch, or (nil, nil)
// if no PR exists for that branch. Unlike `gh pr view`, `gh pr list --head
// --state all` still finds a PR after it has been merged and its head branch
// deleted on the remote, so callers can tell "already merged" apart from
// "gh failed to run" instead of a swallowed error masking both as "no PR".
func GHGetPR(ctx context.Context, worktreePath, branch string) (map[string]any, error) {
	cmd := exec.CommandContext(ctx, "gh", "pr", "list", "--head", branch, "--state", "all", "--json", "url,number,state")
	cmd.Dir = worktreePath

	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("gh pr list: %w", err)
	}

	var results []map[string]any
	if err := json.Unmarshal(out, &results); err != nil {
		return nil, fmt.Errorf("parse gh pr list output: %w", err)
	}
	if len(results) == 0 {
		return nil, nil
	}
	return results[0], nil
}
