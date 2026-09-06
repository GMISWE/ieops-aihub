package coding

// Tests for Ship — the fused commit -> push -> PR chain (aihub#286).
//
// These reuse wrap_test.go's harness: a real git repo whose origin is a local
// bare repo (so pushes are observable) plus a fake `gh` on PATH. Nothing here is
// mocked at the Ship boundary, because the property under test is precisely what
// happens to the WORKTREE and the REMOTE when a stage fails halfway.
//
// The failure that matters is deliberately a real one — GitPush's
// protectedBranches guard on a branch actually named "main" — and every negative
// test asserts WHY it failed, not just that it did. A suite that stubs the
// failure, or that never checks the failure reason, passes for the wrong reason
// (team memory mem_quFPJ1VN).

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// runShip wires up a state file and calls Ship for repo "aihub", returning both
// the result and the error — the point of Ship is that BOTH are meaningful.
//
// The nil gate is the pre-aihub#366 behaviour and is what every test in this
// file wants: none of them is about the commit-time lock check. The gated arm
// lives in commit_gate_test.go.
func runShip(t *testing.T, r *testRepo, wiID, message string, paths []string) (*ShipResult, error) {
	t.Helper()
	return runShipGated(t, r, wiID, message, paths, nil)
}

// runShipGated is runShip with a CommitGate.
func runShipGated(t *testing.T, r *testRepo, wiID, message string, paths []string,
	gate CommitGate) (*ShipResult, error) {
	t.Helper()
	wsRoot := filepath.Dir(r.wt)
	sf := writeWrapStateFile(t, wsRoot, wiID, "aihub", r.wt)
	return Ship(context.Background(), sf, "aihub", wsRoot, message, paths,
		"a title", "a body", "", gate)
}

// write puts an uncommitted file in the worktree.
func write(t *testing.T, r *testRepo, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(r.wt, name), []byte(content), 0644); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
}

// onProtectedBranch moves the worktree onto a branch named "main" and proves it
// got there. GitPush refuses main/master/dev/tot, so this is a push that CANNOT
// succeed — and asserting the branch name here is what stops the test from
// silently exercising a different path if newTestRepo's default ever changes.
func onProtectedBranch(t *testing.T, r *testRepo) {
	t.Helper()
	r.git(t, "checkout", "-q", "-B", "main")
	branch, err := GitCurrentBranch(context.Background(), r.wt)
	if err != nil {
		t.Fatalf("GitCurrentBranch: %v", err)
	}
	if branch != "main" {
		t.Fatalf("worktree is on %q, want main — this test needs a branch GitPush refuses", branch)
	}
	if !protectedBranches[branch] {
		t.Fatalf("branch %q is not in protectedBranches; the push under test would succeed", branch)
	}
}

// TestShip_CommitsPushesAndCreatesPR is the positive path: one call does all
// three stages.
func TestShip_CommitsPushesAndCreatesPR(t *testing.T) {
	r := newTestRepo(t)
	createLog := filepath.Join(t.TempDir(), "create.log")
	fakeGH(t, `[]`, createLog)

	write(t, r, "new.txt", "shipped")

	res, err := runShip(t, r, "wi_ShipOK", "feat: ship it", nil)
	if err != nil {
		t.Fatalf("Ship: %v", err)
	}
	if res.Stage != StageDone {
		t.Errorf("Stage = %q, want %q", res.Stage, StageDone)
	}
	if !res.Committed || res.CommitSHA == "" {
		t.Fatalf("Committed=%v CommitSHA=%q; Ship must have created a commit", res.Committed, res.CommitSHA)
	}
	if got := r.head(t); got != res.CommitSHA {
		t.Errorf("CommitSHA = %s but worktree HEAD is %s", res.CommitSHA, got)
	}
	if res.Wrap == nil || !res.Wrap.Pushed {
		t.Fatalf("Wrap = %+v; the commit must have been pushed", res.Wrap)
	}
	if res.Wrap.Action != WrapActionPushedAndCreatedPR {
		t.Errorf("pr action = %q, want %q", res.Wrap.Action, WrapActionPushedAndCreatedPR)
	}
	if got := r.remoteSHA(t, r.tBranch); got != res.CommitSHA {
		t.Errorf("origin/%s = %q, want the shipped commit %s", r.tBranch, got, res.CommitSHA)
	}
	if called, _ := createCalled(t, createLog); !called {
		t.Error("gh pr create was never invoked, so no PR was opened")
	}
}

// TestShip_PushFails_ReportsTheCommitItAlreadyMade is THE test this change
// exists for.
//
// Fusing three calls into one takes away the caller's ability to observe the
// stages separately: with pf_commit + pf_push it had already SEEN the commit sha
// come back before it ever attempted a push. With one call, a bare "push failed"
// would leave a commit sitting in the worktree that the caller has no way to
// know about. So the failure result must name the stage AND carry the sha.
func TestShip_PushFails_ReportsTheCommitItAlreadyMade(t *testing.T) {
	r := newTestRepo(t)
	createLog := filepath.Join(t.TempDir(), "create.log")
	fakeGH(t, `[]`, createLog)
	onProtectedBranch(t, r)

	before := r.head(t)
	write(t, r, "stranded.txt", "this commit must be findable")

	res, err := runShip(t, r, "wi_ShipPushFail", "feat: will not push", nil)
	if err == nil {
		t.Fatal("Ship succeeded while pushing to a protected branch")
	}
	if res == nil {
		t.Fatal("Ship returned a nil result alongside its error; the caller cannot see the commit it just made")
	}

	// Which stage failed.
	if res.Stage != StagePush {
		t.Errorf("Stage = %q, want %q", res.Stage, StagePush)
	}
	// Why it failed — pin the reason, not just the redness.
	if !strings.Contains(err.Error(), "refusing to push to main") {
		t.Errorf("error = %q, want the protected-branch refusal; the test may be exercising a different failure", err)
	}

	// The side effect the caller can no longer observe for itself.
	if !res.Committed {
		t.Fatal("Committed = false, but the commit stage ran before the push")
	}
	head := r.head(t)
	if head == before {
		t.Fatal("worktree HEAD did not move; there is no commit for the result to report")
	}
	if res.CommitSHA != head {
		t.Errorf("CommitSHA = %q, want the real new HEAD %s", res.CommitSHA, head)
	}
	if res.HeadSHA != head {
		t.Errorf("HeadSHA = %q, want %s", res.HeadSHA, head)
	}

	// And nothing beyond the commit happened.
	if res.Wrap != nil && res.Wrap.Pushed {
		t.Error("Wrap reports a push that cannot have happened")
	}
	if got := r.remoteSHA(t, "main"); got != "" {
		t.Errorf("origin/main = %q; nothing must have been pushed", got)
	}
	if called, args := createCalled(t, createLog); called {
		t.Errorf("gh pr create ran after the push failed: %s", args)
	}
}

// TestShip_RetryAfterPushFailure_DoesNotDuplicateCommit is the idempotency
// guarantee. A caller that fixes the push problem and retries with the same
// arguments must not end up with two commits — and must still be told where the
// first one is.
func TestShip_RetryAfterPushFailure_DoesNotDuplicateCommit(t *testing.T) {
	r := newTestRepo(t)
	createLog := filepath.Join(t.TempDir(), "create.log")
	fakeGH(t, `[]`, createLog)
	onProtectedBranch(t, r)

	write(t, r, "once.txt", "only one commit")

	first, err := runShip(t, r, "wi_ShipRetry", "feat: once", nil)
	if err == nil {
		t.Fatal("first Ship succeeded while pushing to a protected branch")
	}
	if !first.Committed {
		t.Fatal("first Ship did not commit")
	}
	countAfterFirst := r.git(t, "rev-list", "--count", "HEAD")

	second, err := runShip(t, r, "wi_ShipRetry", "feat: once", nil)
	if err == nil {
		t.Fatal("second Ship succeeded while pushing to a protected branch")
	}
	if second.Committed {
		t.Errorf("second Ship reported Committed=true (sha %s); the retry duplicated a commit", second.CommitSHA)
	}
	if second.CommitSHA != "" {
		t.Errorf("second Ship reported CommitSHA=%q; it created no commit", second.CommitSHA)
	}
	if got := r.git(t, "rev-list", "--count", "HEAD"); got != countAfterFirst {
		t.Errorf("commit count went %s -> %s across a retry; Ship must be idempotent", countAfterFirst, got)
	}
	// The retry still has to say where the work is, or the caller loses it.
	if second.HeadSHA != first.CommitSHA {
		t.Errorf("HeadSHA = %q, want the first call's commit %s", second.HeadSHA, first.CommitSHA)
	}
	if second.Stage != StagePush {
		t.Errorf("Stage = %q, want %q", second.Stage, StagePush)
	}
}

// TestShip_NothingToCommit_StillPushes pins the branch the retry above rides on:
// "nothing staged" is a fact Ship acts on, not an error it stops at. GitCommit
// (and therefore pf_commit) still errors here — see TestGitCommit_*.
func TestShip_NothingToCommit_StillPushes(t *testing.T) {
	r := newTestRepo(t)
	createLog := filepath.Join(t.TempDir(), "create.log")
	fakeGH(t, `[]`, createLog)

	head := r.head(t)

	res, err := runShip(t, r, "wi_ShipNoop", "feat: nothing staged", nil)
	if err != nil {
		t.Fatalf("Ship: %v", err)
	}
	if res.Committed {
		t.Errorf("Committed = true with an empty index (sha %s)", res.CommitSHA)
	}
	if res.Stage != StageDone {
		t.Errorf("Stage = %q, want %q — an empty commit must not stop the chain", res.Stage, StageDone)
	}
	if res.Wrap == nil || !res.Wrap.Pushed {
		t.Fatalf("Wrap = %+v; Ship must still have pushed", res.Wrap)
	}
	if got := r.remoteSHA(t, r.tBranch); got != head {
		t.Errorf("origin/%s = %q, want %s", r.tBranch, got, head)
	}
}

// TestShip_OpenPRExists_PushesOntoItWithoutCreatingASecond is why Ship reuses
// Wrap's chain rather than calling GHCreatePR directly: shipping a second time
// on a branch that already has an open PR is the ordinary case, and a bare
// `gh pr create` fails there.
func TestShip_OpenPRExists_PushesOntoItWithoutCreatingASecond(t *testing.T) {
	r := newTestRepo(t)
	r.git(t, "push", "-q", "-u", "origin", r.tBranch)
	createLog := filepath.Join(t.TempDir(), "create.log")
	// An open PR that covers only the base commit, not what we are about to add.
	fakeGH(t, prListJSON(7, "OPEN", r.base, r.head(t)), createLog)

	write(t, r, "second.txt", "review fix")

	res, err := runShip(t, r, "wi_ShipOpenPR", "fix: review feedback", nil)
	if err != nil {
		t.Fatalf("Ship: %v", err)
	}
	if !res.Committed {
		t.Fatal("Ship did not commit the review fix")
	}
	if res.Wrap == nil || res.Wrap.Action != WrapActionPushedToPR {
		t.Fatalf("pr action = %+v, want %q", res.Wrap, WrapActionPushedToPR)
	}
	if called, args := createCalled(t, createLog); called {
		t.Errorf("gh pr create ran even though an open PR already covers the branch: %s", args)
	}
	if got := r.remoteSHA(t, r.tBranch); got != res.CommitSHA {
		t.Errorf("origin/%s = %q, want the new commit %s", r.tBranch, got, res.CommitSHA)
	}
}

// TestWrap_PushFails_ReturnsPartialResultNamingTheStage pins the contract Ship
// depends on: pushAndOpenPR reports how far it got even when it fails. Wrap's
// own caller (pf_wrap) ignores the result on error, so nothing else would catch
// a regression here.
func TestWrap_PushFails_ReturnsPartialResultNamingTheStage(t *testing.T) {
	r := newTestRepo(t)
	createLog := filepath.Join(t.TempDir(), "create.log")
	fakeGH(t, `[]`, createLog)
	onProtectedBranch(t, r)

	wsRoot := filepath.Dir(r.wt)
	sf := writeWrapStateFile(t, wsRoot, "wi_WrapPartial", "aihub", r.wt)
	res, err := Wrap(context.Background(), sf, "aihub", wsRoot, "t", "b")
	if err == nil {
		t.Fatal("Wrap succeeded while pushing to a protected branch")
	}
	if res == nil {
		t.Fatal("Wrap returned a nil result on error; Ship cannot report which stage failed")
	}
	if res.Stage != StagePush {
		t.Errorf("Stage = %q, want %q", res.Stage, StagePush)
	}
	if res.Pushed {
		t.Error("Pushed = true after a refused push")
	}
}

// TestGitPush_StaleLeaseActuallyEmitsTheBaseMovedMarker closes the producer end
// of the base_moved contract.
//
// pf_push and pf_ship both key their machine-readable `error` on this marker,
// but detection is a substring match over git's human-readable rejection text
// ("rejected" + "stale"). The existing lease test proves the push is refused; it
// never checks that the refusal is TAGGED. Without this, git rewording that
// message would silently stop the marker being emitted, both tools would quietly
// degrade to a generic error, and every test would stay green.
//
// The rejection here is a real one: a second clone advances origin behind this
// worktree's back, exactly as a concurrent push would.
func TestGitPush_StaleLeaseActuallyEmitsTheBaseMovedMarker(t *testing.T) {
	r := newTestRepo(t)
	r.git(t, "push", "-q", "-u", "origin", r.tBranch)

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

	r.commit(t, "ours.txt", "ours")
	_, err := GitPush(context.Background(), r.wt)
	if err == nil {
		t.Fatal("GitPush accepted a push onto a branch that moved on origin")
	}
	if !strings.Contains(err.Error(), BaseMovedMarker) {
		t.Fatalf("a real stale-lease rejection produced %q, which does not carry %q — "+
			"pf_push and pf_ship would both report it as a generic push failure", err, BaseMovedMarker)
	}
}

// TestShip_NeverReturnsANilResult enforces the invariant the whole failure
// contract rests on, across the EARLY failures too — not just the push failure
// covered above. Ship's doc comment promises a non-nil result in every case;
// without this the promise is only a comment, and the first early return that
// forgets it hands the caller back exactly the blindness this tool was supposed
// to remove.
func TestShip_NeverReturnsANilResult(t *testing.T) {
	cases := []struct {
		name  string
		setup func(t *testing.T, r *testRepo)
		repo  string
		paths []string
	}{
		{
			name: "worktree cannot be resolved",
			repo: "not-a-declared-repo",
		},
		{
			name:  "git add fails on a path that does not exist",
			repo:  "aihub",
			paths: []string{"no-such-file.txt"},
		},
		{
			name: "push refused on a protected branch",
			repo: "aihub",
			setup: func(t *testing.T, r *testRepo) {
				onProtectedBranch(t, r)
				write(t, r, "x.txt", "x")
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := newTestRepo(t)
			fakeGH(t, `[]`, filepath.Join(t.TempDir(), "create.log"))
			if tc.setup != nil {
				tc.setup(t, r)
			}
			wsRoot := filepath.Dir(r.wt)
			sf := writeWrapStateFile(t, wsRoot, "wi_ShipNilGuard", "aihub", r.wt)

			res, err := Ship(context.Background(), sf, tc.repo, wsRoot,
				"feat: x", tc.paths, "t", "b", "", nil)
			if err == nil {
				t.Fatal("expected this case to fail; it did not, so the invariant is untested here")
			}
			if res == nil {
				t.Fatal("Ship returned a nil result alongside its error")
			}
			if res.Stage == "" {
				t.Error("Stage is empty; the caller cannot tell where the chain stopped")
			}
		})
	}
}

// TestGitCommit_StillErrorsOnEmptyCommit is the characterization test for the
// GitStage extraction. pf_commit callers rely on an empty commit being an error;
// only Ship wants it to be a branch.
func TestGitCommit_StillErrorsOnEmptyCommit(t *testing.T) {
	r := newTestRepo(t)
	if _, err := GitCommit(context.Background(), r.wt, "nothing here", nil); err == nil {
		t.Fatal("GitCommit succeeded with an empty index; pf_commit would silently report a commit that is not there")
	}
}

// TestGitCommit_StagesEverythingByDefault and ..._StagesOnlyNamedPaths pin both
// arms of the staging logic moved into GitStage.
func TestGitCommit_StagesEverythingByDefault(t *testing.T) {
	r := newTestRepo(t)
	write(t, r, "a.txt", "a")
	write(t, r, "b.txt", "b")

	sha, err := GitCommit(context.Background(), r.wt, "both", nil)
	if err != nil {
		t.Fatalf("GitCommit: %v", err)
	}
	if sha != r.head(t) {
		t.Errorf("returned sha %s != HEAD %s", sha, r.head(t))
	}
	files := r.git(t, "show", "--name-only", "--format=", "HEAD")
	for _, want := range []string{"a.txt", "b.txt"} {
		if !strings.Contains(files, want) {
			t.Errorf("commit does not contain %s; files were %q", want, files)
		}
	}
}

func TestGitCommit_StagesOnlyNamedPaths(t *testing.T) {
	r := newTestRepo(t)
	write(t, r, "wanted.txt", "in")
	write(t, r, "unwanted.txt", "out")

	if _, err := GitCommit(context.Background(), r.wt, "one only", []string{"wanted.txt"}); err != nil {
		t.Fatalf("GitCommit: %v", err)
	}
	files := r.git(t, "show", "--name-only", "--format=", "HEAD")
	if !strings.Contains(files, "wanted.txt") {
		t.Errorf("commit is missing wanted.txt; files were %q", files)
	}
	if strings.Contains(files, "unwanted.txt") {
		t.Errorf("commit swept in unwanted.txt; files were %q", files)
	}
}

// TestGitHasStagedChanges covers both answers, since Ship branches on it.
func TestGitHasStagedChanges(t *testing.T) {
	r := newTestRepo(t)
	ctx := context.Background()

	if staged, err := GitHasStagedChanges(ctx, r.wt); err != nil || staged {
		t.Fatalf("clean worktree: got (%v, %v), want (false, nil)", staged, err)
	}
	write(t, r, "c.txt", "c")
	if err := GitStage(ctx, r.wt, nil); err != nil {
		t.Fatalf("GitStage: %v", err)
	}
	if staged, err := GitHasStagedChanges(ctx, r.wt); err != nil || !staged {
		t.Fatalf("after staging: got (%v, %v), want (true, nil)", staged, err)
	}
}
