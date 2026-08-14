package coding

// Tests for Wrap's PR-idempotency decision (aihub#207 C4 / Change 1, aihub#226).
//
// Wrap shells out to real `gh`/`git` with no DI seam, so these tests build a
// real local git repo (origin = a local bare repo, so push is observable) and
// put a fake `gh` script on PATH that returns canned `pr list`/`pr create`
// JSON. This exercises the actual Wrap control flow instead of re-testing
// the control flow in isolation against mocks.
//
// The two rules these tests hold apart are easy to conflate:
//   - aihub#203/#207: re-wrapping a wi whose PR is merged must push NOTHING,
//     or it resurrects a head branch the merge deleted.
//   - aihub#226: a branch carrying commits that no PR covers must be pushed and
//     get a PR, or wrap reports ok while the commits are stranded.
//
// They only coexist because the test is "does a PR already cover HEAD", not
// "does a PR exist" — so every test below pins which commits the fake PR covers.

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/GMISWE/ieops-aihub/internal/config"
)

// fakeGH writes a fake `gh` executable to dir/gh that:
//   - `gh pr list ...` prints listJSON to stdout
//   - `gh pr create ...` appends its args to the createLog file and prints
//     {"url":"...","number":9}
//
// It prepends dir to PATH for the duration of the test.
func fakeGH(t *testing.T, listJSON string, createLog string) {
	t.Helper()
	dir := t.TempDir()
	script := fmt.Sprintf(`#!/bin/sh
case "$1 $2" in
  "pr list")
    cat <<'EOF'
%s
EOF
    ;;
  "pr create")
    echo "$@" >> %q
    echo '{"url":"https://example.com/pr/9","number":9}'
    ;;
  *)
    echo "fake gh: unhandled args: $@" >&2
    exit 1
    ;;
esac
`, listJSON, createLog)
	ghPath := filepath.Join(dir, "gh")
	if err := os.WriteFile(ghPath, []byte(script), 0755); err != nil {
		t.Fatalf("write fake gh: %v", err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// prListJSON renders one `gh pr list --json url,number,state,baseRefName,commits`
// entry covering the given commit oids.
func prListJSON(number int, state, baseRefName string, commitOIDs ...string) string {
	commits := make([]string, 0, len(commitOIDs))
	for _, oid := range commitOIDs {
		commits = append(commits, fmt.Sprintf(`{"oid":%q}`, oid))
	}
	return fmt.Sprintf(`[{"url":"https://example.com/pr/%d","number":%d,"state":%q,"baseRefName":%q,"commits":[%s]}]`,
		number, number, state, baseRefName, strings.Join(commits, ","))
}

// testRepo is a real git repo whose origin is a local bare repo, so pushes are
// observable.
type testRepo struct {
	wt      string // worktree path
	bare    string // the bare "origin"
	base    string // the repo's initial (base) branch name
	tBranch string // the task branch, checked out
}

// git runs a git command in the worktree and fails the test on error.
func (r *testRepo) git(t *testing.T, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = r.wt
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// commit writes a file and commits it, returning the new HEAD sha.
func (r *testRepo) commit(t *testing.T, name, content string) string {
	t.Helper()
	if err := os.WriteFile(filepath.Join(r.wt, name), []byte(content), 0644); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
	r.git(t, "add", name)
	r.git(t, "commit", "-q", "-m", "commit "+name)
	return r.head(t)
}

// head returns the local HEAD sha.
func (r *testRepo) head(t *testing.T) string {
	t.Helper()
	return r.git(t, "rev-parse", "HEAD")
}

// remoteSHA returns the sha of branch on the bare origin, or "" if the branch
// does not exist there.
func (r *testRepo) remoteSHA(t *testing.T, branch string) string {
	t.Helper()
	cmd := exec.Command("git", "ls-remote", "--heads", r.bare, branch)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git ls-remote: %v\n%s", err, out)
	}
	fields := strings.Fields(string(out))
	if len(fields) == 0 {
		return ""
	}
	return fields[0]
}

// newTestRepo creates a bare origin, clones it, lays down one commit on the
// base branch and checks out a task branch. Neither branch is pushed yet —
// each test pushes exactly what its scenario requires, since what origin holds
// is precisely what is under test.
func newTestRepo(t *testing.T) *testRepo {
	t.Helper()
	root := t.TempDir()
	r := &testRepo{
		wt:      filepath.Join(root, "wt"),
		bare:    filepath.Join(root, "bare"),
		tBranch: "task-branch",
	}
	if err := exec.Command("git", "init", "--bare", "-q", r.bare).Run(); err != nil {
		t.Fatalf("git init --bare: %v", err)
	}
	if out, err := exec.Command("git", "clone", "-q", r.bare, r.wt).CombinedOutput(); err != nil {
		t.Fatalf("git clone: %v\n%s", err, out)
	}
	r.git(t, "config", "user.email", "t@t.com")
	r.git(t, "config", "user.name", "t")
	r.commit(t, "f.txt", "hi")
	r.base = r.git(t, "rev-parse", "--abbrev-ref", "HEAD")
	r.git(t, "checkout", "-q", "-b", r.tBranch)
	return r
}

// writeWrapStateFile writes a minimal state file pointing worktrees[repo] at wt.
func writeWrapStateFile(t *testing.T, wsRoot, wiID, repo, wt string) *config.StateFile {
	t.Helper()
	dir := filepath.Join(wsRoot, ".polyforge", "state")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	t.Setenv("POLYFORGE_WORKSPACE_ROOT", wsRoot)
	sf := &config.StateFile{
		WIID:      wiID,
		AttemptID: "att_wrap1",
		Worktrees: map[string]string{repo: wt},
	}
	if err := config.WriteStateFile(sf); err != nil {
		t.Fatalf("WriteStateFile: %v", err)
	}
	return sf
}

// createCalled reports whether the fake gh recorded a `pr create` invocation,
// and the args it was called with.
func createCalled(t *testing.T, createLog string) (bool, string) {
	t.Helper()
	b, err := os.ReadFile(createLog)
	if os.IsNotExist(err) {
		return false, ""
	}
	if err != nil {
		t.Fatalf("read create log: %v", err)
	}
	return true, string(b)
}

// runWrap wires up the state file and calls Wrap for repo "aihub".
func runWrap(t *testing.T, r *testRepo, wiID, prTitle, prBody string) *WrapResult {
	t.Helper()
	wsRoot := filepath.Dir(r.wt)
	sf := writeWrapStateFile(t, wsRoot, wiID, "aihub", r.wt)
	res, err := Wrap(context.Background(), sf, "aihub", wsRoot, prTitle, prBody)
	if err != nil {
		t.Fatalf("Wrap: %v", err)
	}
	return res
}

// TestWrap_MergedPR_CoveringHEAD_NoPush is the aihub#203/#207 rule: re-wrapping
// a wi whose PR is merged and whose HEAD that PR already covers must push
// nothing, so a head branch the merge deleted stays deleted.
func TestWrap_MergedPR_CoveringHEAD_NoPush(t *testing.T) {
	r := newTestRepo(t)
	r.git(t, "push", "-q", "-u", "origin", r.tBranch)
	head := r.head(t)

	// GitHub deleting the head branch on merge.
	r.deleteRemoteBranchServerSide(t, r.tBranch)

	createLog := filepath.Join(t.TempDir(), "create.log")
	// The merged PR covers exactly HEAD — nothing local is undelivered.
	fakeGH(t, prListJSON(1, "MERGED", r.base, head), createLog)

	res := runWrap(t, r, "wi_WrapMergedCovered", "", "")

	if res.Action != WrapActionReusedPR {
		t.Errorf("Action = %q, want %q", res.Action, WrapActionReusedPR)
	}
	if res.Pushed {
		t.Error("Pushed = true; a merged PR that already covers HEAD must not push")
	}
	if state, _ := res.PR["state"].(string); state != "MERGED" {
		t.Errorf("PR[state] = %v, want MERGED", res.PR["state"])
	}
	// The core assertion: no push happened, so the remote branch is still gone.
	if got := r.remoteSHA(t, r.tBranch); got != "" {
		t.Errorf("Wrap resurrected the deleted remote branch (now %s) for a MERGED PR covering HEAD", got)
	}
	if called, _ := createCalled(t, createLog); called {
		t.Error("Wrap called `gh pr create` for a wi whose PR is already MERGED and covers HEAD")
	}
}

// TestWrap_MergedPR_UndeliveredCommit_PushesAndCreatesPR is the aihub#226
// regression: the branch has a commit the merged PR does not cover, so wrap must
// deliver it (push + a NEW PR) instead of reporting ok and stranding it.
func TestWrap_MergedPR_UndeliveredCommit_PushesAndCreatesPR(t *testing.T) {
	r := newTestRepo(t)
	r.git(t, "push", "-q", "-u", "origin", r.tBranch)
	mergedHead := r.head(t)

	// A commit made after the PR merged: on the branch, in no PR.
	newHead := r.commit(t, "after-merge.txt", "undelivered")
	if newHead == mergedHead {
		t.Fatal("setup: expected a new commit")
	}

	createLog := filepath.Join(t.TempDir(), "create.log")
	fakeGH(t, prListJSON(8, "MERGED", r.base, mergedHead), createLog)

	res := runWrap(t, r, "wi_WrapMergedStranded", "new title", "new body")

	if res.Action != WrapActionPushedAndCreatedPR {
		t.Errorf("Action = %q, want %q", res.Action, WrapActionPushedAndCreatedPR)
	}
	if !res.Pushed || res.PushedSHA != newHead {
		t.Errorf("Pushed=%v PushedSHA=%q, want true / %s", res.Pushed, res.PushedSHA, newHead)
	}
	// The core assertion: the undelivered commit actually reached the remote.
	if got := r.remoteSHA(t, r.tBranch); got != newHead {
		t.Errorf("remote %s = %s, want %s — the new commit was not delivered", r.tBranch, got, newHead)
	}
	called, args := createCalled(t, createLog)
	if !called {
		t.Error("Wrap did not open a new PR for commits the MERGED PR does not cover")
	}
	// The new PR must target the same base the merged PR did.
	if called && !strings.Contains(args, "--base "+r.base) {
		t.Errorf("gh pr create args %q do not carry --base %s", strings.TrimSpace(args), r.base)
	}
	// And it must not report the stale merged PR as the delivery vehicle.
	if num, _ := res.PR["number"].(float64); num == 8 {
		t.Error("Wrap reported the MERGED PR #8 as the result; want the newly created PR")
	}
}

// deleteRemoteBranchServerSide removes the branch from the bare origin directly,
// leaving the worktree's refs/remotes/origin/<branch> behind as a stale pointer.
// That is what GitHub's delete-branch-on-merge looks like from the client: a
// local `git push origin --delete` would instead prune the tracking ref and hide
// the very condition under test.
func (r *testRepo) deleteRemoteBranchServerSide(t *testing.T, branch string) {
	t.Helper()
	cmd := exec.Command("git", "-C", r.bare, "update-ref", "-d", "refs/heads/"+branch)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("delete remote branch server-side: %v\n%s", err, out)
	}
	if got := r.remoteSHA(t, branch); got != "" {
		t.Fatalf("setup: remote branch should be gone, got %s", got)
	}
	if _, err := GitRevParse(context.Background(), r.wt, "refs/remotes/origin/"+branch); err != nil {
		t.Fatalf("setup: refs/remotes/origin/%s should still be present but stale: %v", branch, err)
	}
}

// TestWrap_MergedPR_BranchDeletedOnMerge_PushesAndCreatesPR is the full
// aihub#226 shape on a repo with delete-branch-on-merge: the PR merged, GitHub
// deleted the head branch, and a later commit is sitting only in the worktree.
// The commit must still reach the remote and get a PR.
//
// This is the case a bare `--force-with-lease` cannot push: the lease is taken
// against the stale refs/remotes/origin/<branch> and git rejects with
// "stale info", so wrap failed without delivering anything.
func TestWrap_MergedPR_BranchDeletedOnMerge_PushesAndCreatesPR(t *testing.T) {
	r := newTestRepo(t)
	r.git(t, "push", "-q", "-u", "origin", r.tBranch)
	mergedHead := r.head(t)
	r.deleteRemoteBranchServerSide(t, r.tBranch)

	newHead := r.commit(t, "after-merge.txt", "undelivered")

	createLog := filepath.Join(t.TempDir(), "create.log")
	fakeGH(t, prListJSON(8, "MERGED", r.base, mergedHead), createLog)

	res := runWrap(t, r, "wi_WrapMergedBranchDeleted", "title", "body")

	if res.Action != WrapActionPushedAndCreatedPR {
		t.Errorf("Action = %q, want %q", res.Action, WrapActionPushedAndCreatedPR)
	}
	if got := r.remoteSHA(t, r.tBranch); got != newHead {
		t.Errorf("remote %s = %q, want %s — the commit never left the worktree", r.tBranch, got, newHead)
	}
	if called, _ := createCalled(t, createLog); !called {
		t.Error("Wrap did not open a PR for the commit stranded after the merge")
	}
}

// TestWrap_ClosedPR_UndeliveredCommit_PushesAndCreatesPR covers the same rule
// for a PR closed without merging: it can never carry the commits either.
func TestWrap_ClosedPR_UndeliveredCommit_PushesAndCreatesPR(t *testing.T) {
	r := newTestRepo(t)
	r.git(t, "push", "-q", "-u", "origin", r.tBranch)
	oldHead := r.head(t)
	newHead := r.commit(t, "after-close.txt", "undelivered")

	createLog := filepath.Join(t.TempDir(), "create.log")
	fakeGH(t, prListJSON(3, "CLOSED", r.base, oldHead), createLog)

	res := runWrap(t, r, "wi_WrapClosedStranded", "t", "b")

	if res.Action != WrapActionPushedAndCreatedPR {
		t.Errorf("Action = %q, want %q", res.Action, WrapActionPushedAndCreatedPR)
	}
	if got := r.remoteSHA(t, r.tBranch); got != newHead {
		t.Errorf("remote %s = %s, want %s", r.tBranch, got, newHead)
	}
	if called, _ := createCalled(t, createLog); !called {
		t.Error("Wrap did not open a PR for commits whose only PR is CLOSED")
	}
}

// TestWrap_OpenPR_UndeliveredCommit_PushesWithoutNewPR: an open PR can carry the
// new commits, so they are pushed into it and no duplicate PR is created.
func TestWrap_OpenPR_UndeliveredCommit_PushesWithoutNewPR(t *testing.T) {
	r := newTestRepo(t)
	r.git(t, "push", "-q", "-u", "origin", r.tBranch)
	oldHead := r.head(t)
	newHead := r.commit(t, "more.txt", "later work")

	createLog := filepath.Join(t.TempDir(), "create.log")
	fakeGH(t, prListJSON(2, "OPEN", r.base, oldHead), createLog)

	res := runWrap(t, r, "wi_WrapOpenBehind", "", "")

	if res.Action != WrapActionPushedToPR {
		t.Errorf("Action = %q, want %q", res.Action, WrapActionPushedToPR)
	}
	if got := r.remoteSHA(t, r.tBranch); got != newHead {
		t.Errorf("remote %s = %s, want %s — commits missing from the open PR", r.tBranch, got, newHead)
	}
	if num, _ := res.PR["number"].(float64); num != 2 {
		t.Errorf("PR[number] = %v, want the existing open PR 2", res.PR["number"])
	}
	if called, _ := createCalled(t, createLog); called {
		t.Error("Wrap created a duplicate PR while an OPEN one could carry the commits")
	}
}

// TestWrap_OpenPR_CoveringHEAD_NoPush is the ordinary idempotent replay: the
// open PR already covers HEAD, so wrap touches nothing.
func TestWrap_OpenPR_CoveringHEAD_NoPush(t *testing.T) {
	r := newTestRepo(t)
	r.git(t, "push", "-q", "-u", "origin", r.tBranch)
	head := r.head(t)

	createLog := filepath.Join(t.TempDir(), "create.log")
	fakeGH(t, prListJSON(2, "OPEN", r.base, head), createLog)

	res := runWrap(t, r, "wi_WrapOpenCovered", "", "")

	if res.Action != WrapActionReusedPR {
		t.Errorf("Action = %q, want %q", res.Action, WrapActionReusedPR)
	}
	if res.Pushed {
		t.Error("Pushed = true; an open PR already covering HEAD needs no push")
	}
	if num, _ := res.PR["number"].(float64); num != 2 {
		t.Errorf("PR[number] = %v, want 2 (existing open PR)", res.PR["number"])
	}
	if called, _ := createCalled(t, createLog); called {
		t.Error("Wrap called `gh pr create` for a wi that already has an open PR")
	}
}

// TestWrap_MergedPR_HEADAlreadyInBase_NoPush covers the second delivery proof:
// the PR's commit list does not mention HEAD, but HEAD is already contained in
// the base branch (the shape left behind by rebasing a merged branch onto base),
// so there is nothing to deliver and nothing to push.
func TestWrap_MergedPR_HEADAlreadyInBase_NoPush(t *testing.T) {
	r := newTestRepo(t)
	// Publish the base branch, and leave the task branch sitting exactly on it.
	r.git(t, "push", "-q", "origin", r.tBranch+":refs/heads/"+r.base)
	r.git(t, "fetch", "-q", "origin")
	head := r.head(t)

	createLog := filepath.Join(t.TempDir(), "create.log")
	// PR covers some unrelated (already-squashed-away) commit, not HEAD.
	fakeGH(t, prListJSON(5, "MERGED", r.base, strings.Repeat("a", 40)), createLog)

	res := runWrap(t, r, "wi_WrapRebasedOntoBase", "", "")

	if res.Action != WrapActionReusedPR {
		t.Errorf("Action = %q, want %q (HEAD %s is already in origin/%s)",
			res.Action, WrapActionReusedPR, head, r.base)
	}
	if res.Pushed {
		t.Error("Pushed = true; HEAD is already contained in the base branch")
	}
	if called, _ := createCalled(t, createLog); called {
		t.Error("Wrap opened a PR with nothing to deliver")
	}
}

// TestWrap_MergedPR_UnknownCoverage_PushesRatherThanSkipping pins the direction
// of the doubt: when the PR carries no usable commit list, coverage cannot be
// proven, and wrap must deliver (loud, visible) rather than silently skip.
func TestWrap_MergedPR_UnknownCoverage_PushesRatherThanSkipping(t *testing.T) {
	r := newTestRepo(t)
	r.git(t, "push", "-q", "-u", "origin", r.tBranch)
	newHead := r.commit(t, "work.txt", "local work")

	createLog := filepath.Join(t.TempDir(), "create.log")
	// No commits field at all, and no origin/<base> to fall back on.
	fakeGH(t, `[{"url":"https://example.com/pr/7","number":7,"state":"MERGED","baseRefName":"main"}]`, createLog)

	res := runWrap(t, r, "wi_WrapUnknownCoverage", "", "")

	if res.Action != WrapActionPushedAndCreatedPR {
		t.Errorf("Action = %q, want %q — unprovable coverage must not be read as delivered",
			res.Action, WrapActionPushedAndCreatedPR)
	}
	if got := r.remoteSHA(t, r.tBranch); got != newHead {
		t.Errorf("remote %s = %s, want %s", r.tBranch, got, newHead)
	}
}

// TestWrap_NoExistingPR_PushesAndCreates verifies the normal, non-idempotent
// path still pushes and creates a PR when none exists yet.
func TestWrap_NoExistingPR_PushesAndCreates(t *testing.T) {
	r := newTestRepo(t)
	r.git(t, "push", "-q", "-u", "origin", r.tBranch)
	newHead := r.commit(t, "f2.txt", "more")

	createLog := filepath.Join(t.TempDir(), "create.log")
	fakeGH(t, `[]`, createLog) // no existing PR

	res := runWrap(t, r, "wi_WrapNew", "my title", "my body")

	if res.Action != WrapActionPushedAndCreatedPR {
		t.Errorf("Action = %q, want %q", res.Action, WrapActionPushedAndCreatedPR)
	}
	if url, _ := res.PR["url"].(string); url == "" {
		t.Errorf("PR[url] empty, want the newly created PR url; got %#v", res.PR)
	}
	if called, _ := createCalled(t, createLog); !called {
		t.Error("Wrap did not call `gh pr create` when no PR existed yet")
	}
	if got := r.remoteSHA(t, r.tBranch); got != newHead {
		t.Errorf("remote %s = %s, want %s (the new commit)", r.tBranch, got, newHead)
	}
}

// TestWrap_GHListError_Fails guards the aihub#207 rule that a real gh failure is
// never mistaken for "no PR" — which would push and open a duplicate PR.
func TestWrap_GHListError_Fails(t *testing.T) {
	r := newTestRepo(t)
	r.git(t, "push", "-q", "-u", "origin", r.tBranch)
	head := r.head(t)

	dir := t.TempDir()
	ghPath := filepath.Join(dir, "gh")
	if err := os.WriteFile(ghPath, []byte("#!/bin/sh\necho 'boom' >&2\nexit 1\n"), 0755); err != nil {
		t.Fatalf("write fake gh: %v", err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	wsRoot := filepath.Dir(r.wt)
	sf := writeWrapStateFile(t, wsRoot, "wi_WrapGHBroken", "aihub", r.wt)
	if _, err := Wrap(context.Background(), sf, "aihub", wsRoot, "", ""); err == nil {
		t.Fatal("Wrap succeeded despite `gh pr list` failing; a gh error must not read as 'no PR'")
	}
	if got := r.remoteSHA(t, r.tBranch); got != head {
		t.Errorf("remote %s moved to %s; Wrap must not push when PR state is unknown", r.tBranch, got)
	}
}

// TestGHGetPR_PrefersOpenPR: once a branch has both a merged and a newly opened
// PR, the open one is the one a push flows into, so it is the one to report.
func TestGHGetPR_PrefersOpenPR(t *testing.T) {
	r := newTestRepo(t)
	createLog := filepath.Join(t.TempDir(), "create.log")
	fakeGH(t, `[{"url":"u1","number":8,"state":"MERGED","baseRefName":"main","commits":[]},`+
		`{"url":"u2","number":11,"state":"OPEN","baseRefName":"main","commits":[]}]`, createLog)

	pr, err := GHGetPR(context.Background(), r.wt, r.tBranch)
	if err != nil {
		t.Fatalf("GHGetPR: %v", err)
	}
	if num, _ := pr["number"].(float64); num != 11 {
		t.Errorf("GHGetPR returned PR %v, want the OPEN PR 11", pr["number"])
	}
}

// TestGitPush_RecreatesBranchDeletedOnRemote pins the push half directly: a
// bare `--force-with-lease` leases the stale refs/remotes/origin/<branch> that a
// server-side deletion leaves behind, and git rejects the push as "stale info".
// GitPush must notice the branch is gone from origin and create it instead.
func TestGitPush_RecreatesBranchDeletedOnRemote(t *testing.T) {
	r := newTestRepo(t)
	r.git(t, "push", "-q", "-u", "origin", r.tBranch)
	r.deleteRemoteBranchServerSide(t, r.tBranch)
	newHead := r.commit(t, "later.txt", "later")

	sha, err := GitPush(context.Background(), r.wt)
	if err != nil {
		t.Fatalf("GitPush: %v", err)
	}
	if sha != newHead {
		t.Errorf("GitPush returned %s, want %s", sha, newHead)
	}
	if got := r.remoteSHA(t, r.tBranch); got != newHead {
		t.Errorf("remote %s = %q, want %s", r.tBranch, got, newHead)
	}
}

// TestGitPush_LeaseStillProtectsExistingBranch: the lease must survive the
// change above — an origin that moved under us is still a rejected push, not a
// silent overwrite.
func TestGitPush_LeaseStillProtectsExistingBranch(t *testing.T) {
	r := newTestRepo(t)
	r.git(t, "push", "-q", "-u", "origin", r.tBranch)

	// Someone else advances the branch on origin; our remote-tracking ref does
	// not know about it.
	other := filepath.Join(t.TempDir(), "other")
	if out, err := exec.Command("git", "clone", "-q", "-b", r.tBranch, r.bare, other).CombinedOutput(); err != nil {
		t.Fatalf("clone: %v\n%s", err, out)
	}
	for _, args := range [][]string{
		{"config", "user.email", "o@o.com"}, {"config", "user.name", "o"},
		{"commit", "-q", "--allow-empty", "-m", "theirs"}, {"push", "-q", "origin", r.tBranch},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = other
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	theirs := r.remoteSHA(t, r.tBranch)

	r.commit(t, "ours.txt", "ours")
	if _, err := GitPush(context.Background(), r.wt); err == nil {
		t.Fatal("GitPush overwrote a branch that moved on origin; the lease must reject it")
	}
	if got := r.remoteSHA(t, r.tBranch); got != theirs {
		t.Errorf("remote %s = %s, want %s (their commit must survive)", r.tBranch, got, theirs)
	}
}

// TestGitIsAncestor_DistinguishesNoFromError: "not an ancestor" and "that rev
// does not exist here" must not collapse into one answer, since deliveredByPR
// treats the second as "cannot tell".
func TestGitIsAncestor_DistinguishesNoFromError(t *testing.T) {
	r := newTestRepo(t)
	first := r.head(t)
	second := r.commit(t, "b.txt", "b")

	ok, err := GitIsAncestor(context.Background(), r.wt, first, second)
	if err != nil || !ok {
		t.Errorf("ancestor(first, second) = %v, %v; want true, nil", ok, err)
	}
	ok, err = GitIsAncestor(context.Background(), r.wt, second, first)
	if err != nil || ok {
		t.Errorf("ancestor(second, first) = %v, %v; want false, nil", ok, err)
	}
	if _, err := GitIsAncestor(context.Background(), r.wt, strings.Repeat("b", 40), first); err == nil {
		t.Error("ancestor(missing-rev, ...) returned nil error; want a real error, not a silent false")
	}
}
