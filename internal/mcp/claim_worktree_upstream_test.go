package mcp

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// aihub#257: a claim must not leave a task branch configured to push to main.
//
// THE CRITERION IS THE DESTINATION REF, NOT WHETHER THE PUSH FAILS. With the
// defect present the push SUCCEEDS — that is the whole problem. A test written
// as "assert the push is rejected" passes for the wrong reason today (git's
// built-in push.default=simple refuses on the name mismatch) and would keep
// passing after somebody sets push.default=upstream, which is the exact
// configuration the defect needs to become a data-loss event. So these tests
// set push.default=upstream themselves and read the ref `git push --dry-run
// --porcelain` says it would write.
//
// Every test here carries its own POSITIVE CONTROL: a second worktree built the
// pre-fix way (`worktree add -b <b> <path> origin/main`, no --no-track) on a
// throwaway branch, asserted to target refs/heads/main. Without it, a dry run
// that fails for an unrelated reason — an unreachable remote, a git that does
// not know --porcelain — produces no "refs/heads/main" either, and the subject
// assertion passes while measuring nothing.

// pushDryRunDestinations returns every destination ref a bare `git push` in wt
// would write, and the raw output for error messages. A failed push is not an
// error here: "refused" is one of the outcomes under test.
func pushDryRunDestinations(t *testing.T, wt string) ([]string, string) {
	t.Helper()
	out, _ := exec.Command("git", "-C", wt, "push", "--dry-run", "--porcelain").CombinedOutput()
	raw := string(out)
	var dests []string
	for _, line := range strings.Split(raw, "\n") {
		for _, field := range strings.Fields(line) {
			if _, dst, ok := strings.Cut(field, ":"); ok && strings.HasPrefix(dst, "refs/") {
				dests = append(dests, dst)
			}
		}
	}
	return dests, raw
}

// preFixControlWorktree reproduces the pre-aihub#257 creation command on a
// throwaway branch and returns its worktree path. It exists to prove the
// instrument above can still see the hazard.
func preFixControlWorktree(t *testing.T, r *claimRepo) string {
	t.Helper()
	wt := filepath.Join(r.root, "control-worktree")
	// ⚠️ Deliberately NOT under polyforge/aihub-257-*: resolveClaimBranch's last
	// tier globs the stem, so a control branch inside that namespace is the
	// unique match and the claim under test attaches to the control arm instead
	// of creating anything. That is what this name being ugly buys.
	mustGit(t, r.src, "worktree", "add", "-q", "-b", "control/aihub-257-pre-fix-arm", wt, "origin/main")
	if err := os.WriteFile(filepath.Join(wt, "control.txt"), []byte("x"), 0644); err != nil {
		t.Fatalf("write control file: %v", err)
	}
	mustGit(t, wt, "add", "control.txt")
	mustGit(t, wt, "commit", "-q", "-m", "control commit")

	dests, raw := pushDryRunDestinations(t, wt)
	if !containsString(dests, "refs/heads/main") {
		t.Fatalf("POSITIVE CONTROL FAILED: a branch created the pre-fix way did not report "+
			"refs/heads/main as its push destination, so this test cannot detect the defect "+
			"it exists to detect. destinations=%v\n%s", dests, raw)
	}
	return wt
}

func containsString(in []string, want string) bool {
	for _, s := range in {
		if s == want {
			return true
		}
	}
	return false
}

// upstreamOf reads the branch's configured upstream in short form.
func upstreamOf(t *testing.T, gitDir, branch string) string {
	t.Helper()
	return mustGit(t, gitDir, "for-each-ref", "--format=%(upstream:short)", "refs/heads/"+branch)
}

// TestClaimCreatedBranchNeverPushesToMain is the gate for aihub#257's create
// path.
//
// MUTANT: restore the pre-fix creation line in addClaimWorktree —
//
//	err := runGit(srcPath, "worktree", "add", "-b", n.Branch, wtPath, "origin/main")
//	if err == nil { return nil }
//
// The branch is then created tracking origin/main and this test reports
// `destinations=[refs/heads/main]`.
//
// ⚠️ IT IS A DISJUNCTION, AND SAYING SO IS THE POINT. The mutant above removes
// --no-track AND the clearBaseUpstream call; removing either ALONE leaves this
// green, because each alone is sufficient. That is not a hole to be plugged
// with a second test — measured on git 2.43.0, --no-track wins even under
// branch.autoSetupMerge=always, so on any git that accepts the flag the
// create-exit clearBaseUpstream call has no reachable behaviour at all. It is
// defence in depth against a git too old for the flag, and there is no
// single-mutation gate for a call that cannot change an outcome. Labelling this
// a conjunction gate would have been the lie.
func TestClaimCreatedBranchNeverPushesToMain(t *testing.T) {
	r := newClaimRepo(t)
	// push.default is repo-level and the config file is shared by every worktree
	// of this clone, so both arms run under the same setting.
	mustGit(t, r.src, "config", "push.default", "upstream")

	control := preFixControlWorktree(t, r)
	t.Logf("positive control worktree %s does target refs/heads/main, as expected pre-fix", control)

	const branch = "polyforge/aihub-257-task-branch-upstream"
	if err := r.add(t, claimBranchNames{Branch: branch, Stem: "polyforge/aihub-257"}); err != nil {
		t.Fatalf("addClaimWorktree: %v", err)
	}
	wt := r.wtPath()
	if got := checkedOutBranch(t, wt); got != branch {
		t.Fatalf("worktree is on %q, want %q", got, branch)
	}
	if err := os.WriteFile(filepath.Join(wt, "task.txt"), []byte("work"), 0644); err != nil {
		t.Fatalf("write task file: %v", err)
	}
	mustGit(t, wt, "add", "task.txt")
	mustGit(t, wt, "commit", "-q", "-m", "task commit")

	dests, raw := pushDryRunDestinations(t, wt)
	if containsString(dests, "refs/heads/main") {
		t.Errorf("a bare `git push` in a freshly claimed worktree would write refs/heads/main. "+
			"destinations=%v\n%s", dests, raw)
	}
	if up := upstreamOf(t, r.src, branch); up == "origin/main" {
		t.Errorf("branch %s tracks %s; a claim must not leave it aimed at the base branch", branch, up)
	}
}

// TestClaimRepairsInheritedBaseUpstream covers the half a creation-path fix
// cannot reach: every task branch claimed before aihub#257 already carries
// upstream=origin/main, and re-claiming it must not hand back the hazard.
//
// MUTANT: drop the clearBaseUpstream call from addClaimWorktree's attach exit.
// The pre-existing upstream survives and the dry run targets refs/heads/main.
func TestClaimRepairsInheritedBaseUpstream(t *testing.T) {
	r := newClaimRepo(t)
	mustGit(t, r.src, "config", "push.default", "upstream")
	preFixControlWorktree(t, r)

	const branch = "polyforge/aihub-257-legacy"
	r.branchWithMarker(t, branch, "legacy-marker.txt")
	// What every pre-fix claim left behind.
	mustGit(t, r.src, "branch", "--set-upstream-to=origin/main", branch)
	if up := upstreamOf(t, r.src, branch); up != "origin/main" {
		t.Fatalf("fixture did not reproduce the legacy state: upstream is %q", up)
	}

	if err := r.add(t, claimBranchNames{Branch: branch, Stem: "polyforge/aihub-257"}); err != nil {
		t.Fatalf("addClaimWorktree: %v", err)
	}
	wt := r.wtPath()
	assertFileInWorktree(t, wt, "legacy-marker.txt")

	if up := upstreamOf(t, r.src, branch); up == "origin/main" {
		t.Errorf("claiming an existing task branch left it tracking %s", up)
	}
	dests, raw := pushDryRunDestinations(t, wt)
	if containsString(dests, "refs/heads/main") {
		t.Errorf("after re-claiming a legacy branch a bare `git push` would still write "+
			"refs/heads/main. destinations=%v\n%s", dests, raw)
	}
}

// TestClaimKeepsAnUpstreamThatIsNotTheBaseBranch is the over-reach guard.
//
// GitPush sets upstream=origin/<task branch> on the first push, which is the
// value that makes a bare push correct. A repair written as "unset any upstream
// on a task branch" would delete it on the next claim, and nothing else in the
// suite would notice — the hazard tests would stay green, because no upstream
// is also not a dangerous upstream.
func TestClaimKeepsAnUpstreamThatIsNotTheBaseBranch(t *testing.T) {
	r := newClaimRepo(t)

	const branch = "polyforge/aihub-257-already-pushed"
	r.branchWithMarker(t, branch, "pushed-marker.txt")
	mustGit(t, r.src, "push", "-q", "origin", branch+":refs/heads/"+branch)
	mustGit(t, r.src, "branch", "--set-upstream-to=origin/"+branch, branch)

	if err := r.add(t, claimBranchNames{Branch: branch, Stem: "polyforge/aihub-257"}); err != nil {
		t.Fatalf("addClaimWorktree: %v", err)
	}
	if up := upstreamOf(t, r.src, branch); up != "origin/"+branch {
		t.Errorf("upstream is %q, want origin/%s — the repair must only drop upstreams that "+
			"point at a protected branch", up, branch)
	}
}

// TestClaimLeavesTheCloneOwnMainAlone: the clone's own main tracks origin/main
// and that is correct. A repair keyed on "upstream is origin/main" without the
// branch != remoteBranch guard would strip it the first time anything claimed
// main, and `git pull` in .repo/<name> would stop working with no diagnostic.
func TestClaimLeavesTheCloneOwnMainAlone(t *testing.T) {
	r := newClaimRepo(t)
	if up := upstreamOf(t, r.src, "main"); up != "origin/main" {
		t.Fatalf("fixture precondition: main tracks %q, want origin/main", up)
	}
	if err := clearBaseUpstream(context.Background(), r.src, "main"); err != nil {
		t.Fatalf("clearBaseUpstream on main: %v", err)
	}
	if up := upstreamOf(t, r.src, "main"); up != "origin/main" {
		t.Errorf("main's upstream became %q; it must keep tracking origin/main", up)
	}
}
