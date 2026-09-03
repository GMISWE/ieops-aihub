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

// IsProtectedBranch reports whether name is one of the branches this package
// refuses to push to directly.
//
// Exported so the same list answers both questions that depend on it — "may I
// push here" (GitPush) and "is this task branch configured to push here"
// (GitClearProtectedUpstream, and the doctor check that scans the workspace).
// A second copy of the list in another package is the shape BaseMovedMarker's
// comment above describes: two literals that must agree with nothing forcing
// them to, so `dev` gets added here and the scanner silently keeps missing it.
func IsProtectedBranch(name string) bool { return protectedBranches[name] }

// GitUpstream reports the remote and the remote-side branch that `branch` in
// gitDir is configured to track. Both are "" when the branch tracks nothing,
// which is not an error.
//
// It reads %(upstream:remotename) and %(upstream:remoteref) rather than
// %(upstream:short) because the short form is `<remote>/<branch>` with no
// escaping, and every task branch this repo produces is named
// `polyforge/<project>-<seq>-<desc>` — splitting that on "/" to recover the
// remote gives "origin" and "polyforge/..." for one case and "polyforge" and
// "aihub-257-..." for the other, from strings of identical shape. The two
// atoms are already parsed by git and cannot be confused.
//
// gitDir may be any working tree or repository of the branch: branch config
// lives in the shared config file, so a linked worktree and its main clone
// answer identically.
func GitUpstream(ctx context.Context, gitDir, branch string) (remote, remoteBranch string, err error) {
	want := "refs/heads/" + branch
	out, err := exec.CommandContext(ctx, "git", "-C", gitDir, "for-each-ref",
		"--format=%(refname)%09%(upstream:remotename)%09%(upstream:remoteref)",
		want).Output()
	if err != nil {
		return "", "", fmt.Errorf("git for-each-ref %s: %w", want, err)
	}
	// ⚠️ for-each-ref's pattern is NOT an exact match: a pattern with no globbing
	// character matches the ref itself OR anything under it as a directory, so
	// `refs/heads/polyforge` matches every polyforge/<task> branch in the repo.
	// Neither caller can reach that today — both pass a branch that exists, and
	// `refs/heads/X` and `refs/heads/X/Y` are a git D/F conflict — but reading
	// the first of several lines would report one branch's upstream as another's.
	//
	// %(refname) IS IN THE FORMAT FOR THIS REASON, not for display. Without it a
	// ref with no upstream prints an all-empty line, blank-line filtering drops
	// it, and a two-ref over-match where only one ref has an upstream counts as
	// one — measured on git 2.43.0 with pfx/one tracking origin/main and pfx/two
	// tracking nothing: the pattern `refs/heads/pfx` printed `origin\trefs/heads/
	// main` and `\t`, so a filter-then-count guard returned origin/main for the
	// name "pfx", which is no branch at all. Counting REFS and requiring the one
	// match to be the ref asked for is immune to what those refs are configured
	// to track.
	var refs [][3]string
	for _, l := range strings.Split(string(out), "\n") {
		if l = strings.TrimRight(l, "\r"); l == "" {
			continue
		}
		f := strings.SplitN(l, "\t", 3)
		if len(f) != 3 {
			continue
		}
		refs = append(refs, [3]string{f[0], f[1], f[2]})
	}
	if len(refs) != 1 || refs[0][0] != want {
		// Zero: no such branch — indistinguishable here from "branch with no
		// upstream", and deliberately so, because both callers want "nothing to
		// clear". Otherwise: the pattern over-matched.
		return "", "", nil
	}
	if refs[0][1] == "" || refs[0][2] == "" {
		return "", "", nil
	}
	return refs[0][1], strings.TrimPrefix(refs[0][2], "refs/heads/"), nil
}

// GitClearProtectedUpstream removes `branch`'s upstream when it points at a
// protected branch that is not `branch` itself, and reports what it cleared
// ("" when there was nothing to clear).
//
// WHY THIS EXISTS. A branch's upstream is where a bare `git push` sends it
// under push.default=upstream (and its alias `tracking`). polyforge used to
// create every task branch with `git worktree add -b <task> <path> origin/main`,
// and git's branch.autoSetupMerge default makes a start point that is a
// remote-tracking branch the new branch's upstream — so every task branch was
// configured to push to main. Nothing pushed to main only because push.default
// was unset everywhere, leaving git's built-in `simple`, which refuses when the
// upstream's name differs from the branch's. Measured in a scratch repo (git
// 2.43.0): with push.default=upstream, a bare `git push --dry-run --porcelain`
// on such a branch reports `refs/heads/polyforge/...:refs/heads/main` and exits
// 0. One config line, global or per-repo, and the accident is a fast-forward of
// main with no prompt.
//
// ⚠️ THE FAILING CASE IS THE ONE THAT SUCCEEDS. Do not test this by checking
// that a push fails; test the destination ref the dry run names.
//
// THREE THINGS ARE DELIBERATELY LEFT ALONE, and each one is a value some other
// part of the system depends on:
//
//   - `origin/<branch>` — the correct setting, and what GitPush's --set-upstream
//     leaves behind. Cleared by a repair written as "unset any upstream on a
//     task branch", and nothing would go red, because "no upstream" is also not
//     a dangerous upstream.
//   - main tracking origin/main — kept by remoteBranch != branch. Stripping it
//     breaks `git pull` in every .repo/<name> clone with no diagnostic.
//   - a LOCAL-tracking upstream, `branch.<n>.remote = "."` — what `git branch
//     --set-upstream-to=main <task>` and branch.autoSetupMerge=always off a
//     local base produce. Measured on git 2.43.0: %(upstream:remotename) is "."
//     and %(upstream:remoteref) is refs/heads/main, so without the remote != "."
//     guard this reads as a protected upstream and gets unset. It is NOT the
//     hazard — a push to "." cannot move origin/main, and git refuses to update
//     a checked-out branch in a non-bare repo — and it is exactly what gives a
//     task worktree its ahead/behind-vs-main readout in `git status`. Clearing
//     it would delete the information this change is accused of costing.
func GitClearProtectedUpstream(ctx context.Context, gitDir, branch string) (string, error) {
	remote, remoteBranch, err := GitUpstream(ctx, gitDir, branch)
	if err != nil {
		return "", err
	}
	if remote == "" || remote == "." || remoteBranch == branch || !IsProtectedBranch(remoteBranch) {
		return "", nil
	}
	out, err := exec.CommandContext(ctx, "git", "-C", gitDir,
		"branch", "--unset-upstream", branch).CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("git branch --unset-upstream %s: %w\n%s", branch, err, out)
	}
	return remote + "/" + remoteBranch, nil
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
//
// --set-upstream is passed for the OTHER direction of the same concern: it
// leaves the branch tracking origin/<branch>, which is the value that makes a
// human's bare `git push` in that worktree correct under every push.default,
// including the `upstream`/`tracking` values that used to send it to main (see
// GitClearProtectedUpstream). It is deliberately not conditional — re-setting
// an upstream that already holds the right value is a no-op, and a claim that
// resumes an older worktree is exactly the case that needs it set.
//
// ⚠️ CONSEQUENCE, recorded because it is a real behaviour change: `git pull`
// with no arguments in a task worktree now merges origin/<task branch> rather
// than origin/main, and before the first push it fails outright ("no tracking
// information") instead of quietly merging main. Merging the base branch is
// now an explicit `git merge origin/main`. That is louder, not silent, which
// is the trade this accepts. Documented for humans in docs/using-polyforge.md;
// a behaviour change that reaches no user-facing text is one people rediscover
// as a bug.
//
// ⚠️ SECOND-ORDER, accepted rather than fixed: --set-upstream writes branch.*
// into the clone's SHARED config file, so two pushes from two worktrees of the
// same clone can collide on config.lock. Git does not retry, so GitPush can
// return an error for a push that already succeeded on the remote. It is
// idempotent on retry — the branch is then present on origin and the retry
// takes the --force-with-lease path — but it is a new way for a successful
// push to be reported as a failure, and it is here so the next person seeing
// "cannot lock config file" does not go looking for a lease problem.
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

	args := []string{"-C", worktreePath, "push", "--set-upstream"}
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
