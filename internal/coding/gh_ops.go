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

// GHGetPR returns existing PR info for the current branch.
func GHGetPR(ctx context.Context, worktreePath string) (map[string]any, error) {
	cmd := exec.CommandContext(ctx, "gh", "pr", "view", "--json", "url,number,state")
	cmd.Dir = worktreePath

	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("gh pr view: %w", err)
	}

	var result map[string]any
	if err := json.Unmarshal(out, &result); err != nil {
		return nil, fmt.Errorf("parse gh pr view output: %w", err)
	}
	return result, nil
}
