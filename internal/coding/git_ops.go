// Package coding implements git/gh operations for the coding scenario.
// All git operations use `git -C <worktree_path>` — never `cd`.
package coding

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
)

// GitDiff runs `git -C path diff HEAD` and returns the diff output.
func GitDiff(ctx context.Context, worktreePath string, vsBase bool) (string, error) {
	var args []string
	if vsBase {
		args = []string{"-C", worktreePath, "diff", "origin/HEAD...HEAD"}
	} else {
		args = []string{"-C", worktreePath, "diff", "HEAD"}
	}
	out, err := exec.CommandContext(ctx, "git", args...).CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("git diff: %w\n%s", err, out)
	}
	return string(out), nil
}

// GitCommit runs `git -C path commit -m message`. If paths is non-empty,
// it stages only those paths first.
func GitCommit(ctx context.Context, worktreePath, message string, paths []string) (string, error) {
	// Stage files
	if len(paths) > 0 {
		addArgs := append([]string{"-C", worktreePath, "add", "--"}, paths...)
		if out, err := exec.CommandContext(ctx, "git", addArgs...).CombinedOutput(); err != nil {
			return "", fmt.Errorf("git add: %w\n%s", err, out)
		}
	} else {
		out, err := exec.CommandContext(ctx, "git", "-C", worktreePath, "add", "-A").CombinedOutput()
		if err != nil {
			return "", fmt.Errorf("git add -A: %w\n%s", err, out)
		}
	}

	out, err := exec.CommandContext(ctx, "git", "-C", worktreePath, "commit", "-m", message).CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("git commit: %w\n%s", err, out)
	}

	// Get the commit SHA
	shaOut, err := exec.CommandContext(ctx, "git", "-C", worktreePath, "rev-parse", "HEAD").Output()
	if err != nil {
		return "", fmt.Errorf("git rev-parse: %w", err)
	}
	return strings.TrimSpace(string(shaOut)), nil
}

// GitPush runs `git -C path push --force-with-lease origin HEAD`.
// Pre-push: verifies we're not pushing to the base branch ancestor.
func GitPush(ctx context.Context, worktreePath string, skipBaseCheck bool) (string, error) {
	if !skipBaseCheck {
		// Get current branch
		branchOut, err := exec.CommandContext(ctx, "git", "-C", worktreePath, "rev-parse", "--abbrev-ref", "HEAD").Output()
		if err != nil {
			return "", fmt.Errorf("get current branch: %w", err)
		}
		branch := strings.TrimSpace(string(branchOut))

		// Refuse to push to main/master
		if branch == "main" || branch == "master" {
			return "", fmt.Errorf("refusing to push to %s branch; use a task branch", branch)
		}
	}

	out, err := exec.CommandContext(ctx, "git", "-C", worktreePath, "push", "--force-with-lease", "origin", "HEAD").CombinedOutput()
	if err != nil {
		outStr := string(out)
		// Detect base moved
		if strings.Contains(outStr, "rejected") && strings.Contains(outStr, "stale") {
			return "", fmt.Errorf("base_moved: %s\nAdvice: rebase on the latest base branch and retry", outStr)
		}
		return "", fmt.Errorf("git push: %w\n%s", err, outStr)
	}

	// Return current SHA
	shaOut, err := exec.CommandContext(ctx, "git", "-C", worktreePath, "rev-parse", "HEAD").Output()
	if err != nil {
		return "", fmt.Errorf("git rev-parse after push: %w", err)
	}
	return strings.TrimSpace(string(shaOut)), nil
}

// GitCurrentBranch returns the current branch name.
func GitCurrentBranch(ctx context.Context, worktreePath string) (string, error) {
	out, err := exec.CommandContext(ctx, "git", "-C", worktreePath, "rev-parse", "--abbrev-ref", "HEAD").Output()
	if err != nil {
		return "", fmt.Errorf("get current branch: %w", err)
	}
	return strings.TrimSpace(string(out)), nil
}
