// Package coding implements git/gh operations for the coding scenario.
// All git operations use `git -C <worktree_path>` — never `cd`.
package coding

import (
	"context"
	"errors"
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

// protectedBranches lists branches GitPush refuses to push to directly.
var protectedBranches = map[string]bool{
	"main":   true,
	"master": true,
	"dev":    true,
	"tot":    true,
}

// GitRemoteBranchExists reports whether branch currently exists on origin.
//
// This asks the remote rather than reading refs/remotes/origin/<branch>, because
// the two disagree in exactly the case that matters: when a merge deletes the
// head branch server-side, the local remote-tracking ref survives as a stale
// pointer until something prunes it, and nothing in this codebase prunes.
func GitRemoteBranchExists(ctx context.Context, worktreePath, branch string) (bool, error) {
	out, err := exec.CommandContext(ctx, "git", "-C", worktreePath,
		"ls-remote", "--heads", "origin", "refs/heads/"+branch).CombinedOutput()
	if err != nil {
		return false, fmt.Errorf("git ls-remote --heads origin %s: %w\n%s", branch, err, out)
	}
	return strings.TrimSpace(string(out)) != "", nil
}

// GitPush pushes HEAD to origin/<current branch>.
// Pre-push: always verifies we're not pushing to a protected branch.
//
// Whether the push is a lease-protected overwrite or a plain ref creation
// depends on what origin actually holds:
//
//   - branch present on origin -> `--force-with-lease`, so a rewritten task
//     branch (the rebase in commit_and_pr) can be replaced but a concurrent
//     push by someone else is not clobbered.
//   - branch absent from origin -> a plain push. There is nothing to overwrite,
//     so no force is needed, and more importantly `--force-with-lease` would
//     REJECT this push: the lease is taken against the stale remote-tracking
//     ref left behind by a server-side branch deletion, and git reports
//     "stale info". That rejection is what stopped a wi whose PR merged with
//     delete-branch-on-merge from ever delivering a later commit (aihub#226).
//
// The refspec is explicit (HEAD:refs/heads/<branch>) rather than a bare HEAD so
// the destination does not depend on the repo's push.default setting.
func GitPush(ctx context.Context, worktreePath string) (string, error) {
	// Get current branch
	branchOut, err := exec.CommandContext(ctx, "git", "-C", worktreePath, "rev-parse", "--abbrev-ref", "HEAD").Output()
	if err != nil {
		return "", fmt.Errorf("get current branch: %w", err)
	}
	branch := strings.TrimSpace(string(branchOut))

	// Refuse to push to a protected branch
	if protectedBranches[branch] {
		return "", fmt.Errorf("refusing to push to %s branch; use a task branch", branch)
	}

	remoteExists, err := GitRemoteBranchExists(ctx, worktreePath, branch)
	if err != nil {
		return "", err
	}

	args := []string{"-C", worktreePath, "push"}
	if remoteExists {
		args = append(args, "--force-with-lease")
	}
	args = append(args, "origin", "HEAD:refs/heads/"+branch)

	out, err := exec.CommandContext(ctx, "git", args...).CombinedOutput()
	if err != nil {
		outStr := string(out)
		// Detect base moved. Reachable only on the remoteExists path now: the
		// lease is held against refs/remotes/origin/<branch>, so a rejection
		// here means origin genuinely moved under us.
		if strings.Contains(outStr, "rejected") && strings.Contains(outStr, "stale") {
			return "", fmt.Errorf("base_moved: %s\nAdvice: fetch and rebase on the latest base branch, then retry", outStr)
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

// GitRevParse resolves rev to a full object id in the worktree.
func GitRevParse(ctx context.Context, worktreePath, rev string) (string, error) {
	out, err := exec.CommandContext(ctx, "git", "-C", worktreePath, "rev-parse", "--verify", rev+"^{commit}").Output()
	if err != nil {
		return "", fmt.Errorf("git rev-parse %s: %w", rev, err)
	}
	return strings.TrimSpace(string(out)), nil
}

// GitIsAncestor reports whether ancestor is reachable from descendant
// (`git merge-base --is-ancestor`).
//
// The three outcomes of that command are kept distinct on purpose: exit 0 means
// yes, exit 1 means no, and any other exit (typically a rev that does not exist
// locally) is a real error. Collapsing "unknown" into "no" is what callers want
// here — see deliveredByPR — but that decision belongs to the caller, not to
// this helper, so the error is returned rather than swallowed.
func GitIsAncestor(ctx context.Context, worktreePath, ancestor, descendant string) (bool, error) {
	cmd := exec.CommandContext(ctx, "git", "-C", worktreePath,
		"merge-base", "--is-ancestor", ancestor, descendant)
	out, err := cmd.CombinedOutput()
	if err == nil {
		return true, nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
		return false, nil
	}
	return false, fmt.Errorf("git merge-base --is-ancestor %s %s: %w\n%s", ancestor, descendant, err, out)
}
