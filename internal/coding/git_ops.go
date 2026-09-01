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

// GitStage writes the worktree changes into the index: only `paths` when it is
// non-empty, everything (`git add -A`) otherwise.
//
// Split out of GitCommit for Ship (aihub#286), which has to stage, then look at
// what actually landed in the index, and only then decide whether a commit is
// needed. Ship's retry path depends on answering "is there anything to commit?"
// WITHOUT having first attempted a commit and failed: a fused
// commit+push+PR call that is retried after a push failure must not die at
// `git commit` with "nothing to commit" before it ever reaches the stage that
// actually failed.
func GitStage(ctx context.Context, worktreePath string, paths []string) error {
	if len(paths) > 0 {
		addArgs := append([]string{"-C", worktreePath, "add", "--"}, paths...)
		if out, err := exec.CommandContext(ctx, "git", addArgs...).CombinedOutput(); err != nil {
			return fmt.Errorf("git add: %w\n%s", err, out)
		}
		return nil
	}
	out, err := exec.CommandContext(ctx, "git", "-C", worktreePath, "add", "-A").CombinedOutput()
	if err != nil {
		return fmt.Errorf("git add -A: %w\n%s", err, out)
	}
	return nil
}

// GitHasStagedChanges reports whether the index differs from HEAD.
//
// `git diff --cached --quiet` exits 0 when the index matches HEAD, 1 when it
// does not, and something else on a real failure. The three outcomes are kept
// distinct on purpose, the same way GitIsAncestor keeps them apart: folding
// "cannot tell" into "nothing staged" would make Ship skip a commit the caller
// asked for and then report success, which is the one direction of wrongness
// that loses work silently.
func GitHasStagedChanges(ctx context.Context, worktreePath string) (bool, error) {
	out, err := exec.CommandContext(ctx, "git", "-C", worktreePath,
		"diff", "--cached", "--quiet").CombinedOutput()
	if err == nil {
		return false, nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
		return true, nil
	}
	return false, fmt.Errorf("git diff --cached --quiet: %w\n%s", err, out)
}

// gitCommitStaged commits whatever is already in the index and returns the new
// commit SHA. It does no staging of its own — callers that need it run GitStage
// first.
func gitCommitStaged(ctx context.Context, worktreePath, message string) (string, error) {
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

// GitCommit runs `git -C path commit -m message`. If paths is non-empty,
// it stages only those paths first.
//
// Behaviour is unchanged by the aihub#286 split — including the error on an
// empty commit, which pf_commit callers rely on to notice they staged nothing.
// Ship deliberately does NOT go through here; it needs the "nothing staged" case
// to be a fact it can branch on rather than an error.
func GitCommit(ctx context.Context, worktreePath, message string, paths []string) (string, error) {
	if err := GitStage(ctx, worktreePath, paths); err != nil {
		return "", err
	}
	return gitCommitStaged(ctx, worktreePath, message)
}

// BaseMovedMarker is the token GitPush puts in its error when origin moved under
// a lease-protected push, and the token pf_push and pf_ship both report back as
// the machine-readable `error` value so a caller can tell "rebase and retry"
// apart from a generic push failure.
//
// It is one exported constant rather than a literal at each end because the two
// ends are in different packages: the producer here and the matcher in
// internal/mcp. Two copies of a magic string that must agree, with nothing
// forcing them to, is a silent-degradation shape — the marker would simply stop
// being emitted and every test would stay green.
const BaseMovedMarker = "base_moved"

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
			return "", fmt.Errorf("%s: %s\nAdvice: fetch and rebase on the latest base branch, then retry", BaseMovedMarker, outStr)
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
