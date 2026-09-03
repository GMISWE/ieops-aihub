package cli

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/GMISWE/ieops-aihub/internal/config"
)

// aihub#257, detection end. checkBranchUpstreams is what makes worktrees that
// ALREADY exist visible: the creation-path fix reaches new claims only, and at
// the time this was written 199 of 227 live task worktrees in the real
// workspace were already configured to push to main.

type upstreamWS struct {
	root string
	bare string
	repo string // .repo/demo
}

func (w *upstreamWS) git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	full := append([]string{"-C", dir}, args...)
	out, err := exec.Command("git", full...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

func newUpstreamWS(t *testing.T) *upstreamWS {
	t.Helper()
	root := t.TempDir()
	w := &upstreamWS{
		root: root,
		bare: filepath.Join(root, "origin.git"),
		repo: filepath.Join(root, ".repo", "demo"),
	}
	w.git(t, root, "init", "--bare", "-q", "-b", "main", w.bare)
	w.git(t, root, "clone", "-q", w.bare, w.repo)
	w.git(t, w.repo, "config", "user.email", "t@t.test")
	w.git(t, w.repo, "config", "user.name", "t")
	if err := os.WriteFile(filepath.Join(w.repo, "f.txt"), []byte("base"), 0644); err != nil {
		t.Fatalf("write: %v", err)
	}
	w.git(t, w.repo, "add", "f.txt")
	w.git(t, w.repo, "commit", "-q", "-m", "base")
	w.git(t, w.repo, "push", "-q", "-u", "origin", "main")
	return w
}

// addTaskWorktree materialises a task worktree the pre-fix way (tracking
// origin/main) or the fixed way (--no-track).
func (w *upstreamWS) addTaskWorktree(t *testing.T, dir, branch string, tracking bool) string {
	t.Helper()
	wt := filepath.Join(w.root, dir, "demo")
	args := []string{"worktree", "add", "-q"}
	if !tracking {
		args = append(args, "--no-track")
	}
	args = append(args, "-b", branch, wt, "origin/main")
	w.git(t, w.repo, args...)
	return wt
}

func (w *upstreamWS) cfg() *config.Config {
	return &config.Config{Projects: map[string]config.Project{
		"demo": {Repos: []config.Repo{{Name: "demo"}}},
	}}
}

// TestBranchUpstreamCheckFindsTaskBranchesAimedAtMain is the positive probe.
func TestBranchUpstreamCheckFindsTaskBranchesAimedAtMain(t *testing.T) {
	w := newUpstreamWS(t)
	w.addTaskWorktree(t, "pf.demo-1", "polyforge/demo-1-tracking", true)
	w.addTaskWorktree(t, "pf.demo-2", "polyforge/demo-2-safe", false)

	res := checkBranchUpstreams(context.Background(), w.cfg(), w.root)
	if res.Status != "warning" {
		t.Fatalf("status %q, want warning: %s", res.Status, res.Message)
	}
	if !strings.Contains(res.Message, "1 of 2") {
		t.Errorf("message does not report 1 offender out of 2 scanned: %s", res.Message)
	}
	if !strings.Contains(res.Message, "pf.demo-1") {
		t.Errorf("message does not name the offending worktree: %s", res.Message)
	}
	if strings.Contains(res.Message, "pf.demo-2") {
		t.Errorf("message names the worktree that is already safe: %s", res.Message)
	}
	if res.FixCmd == "" {
		t.Error("no remedy printed")
	}
}

// TestBranchUpstreamCheckIsCleanWhenNothingTracksMain is the other half. Without
// it, an implementation that reports nothing ever passes the probe above's
// negative assertions and every other test here.
func TestBranchUpstreamCheckIsCleanWhenNothingTracksMain(t *testing.T) {
	w := newUpstreamWS(t)
	w.addTaskWorktree(t, "pf.demo-1", "polyforge/demo-1-safe", false)
	w.addTaskWorktree(t, "pf.demo-2", "polyforge/demo-2-also-safe", false)

	res := checkBranchUpstreams(context.Background(), w.cfg(), w.root)
	if res.Status != "ok" {
		t.Fatalf("status %q, want ok: %s", res.Status, res.Message)
	}
	if !strings.Contains(res.Message, "2 task worktrees") {
		t.Errorf("message does not say how many were scanned: %s", res.Message)
	}
}

// TestBranchUpstreamCheckIgnoresTheCloneOwnMainWorktree.
//
// MUTANT: drop the `resolved != main` guard from linkedWorktreeBranches. Every
// clone's own checkout is then scanned, and a workspace with no task worktrees
// at all reports "1 task worktrees, none tracking a protected branch" — 45 in
// the real one, so every count this check prints is inflated by the number of
// repos and the denominator stops meaning "task worktrees".
//
// ⚠️ THE ASSERTION IS THE COUNT, not the offender list, and the difference was
// measured rather than assumed: with the guard removed the clone's main is
// scanned but still not FLAGGED, because remoteBranch == branch excludes it.
// An earlier version of this test asserted only that nothing was flagged and
// passed against the mutant — it was gating the other guard, twice.
func TestBranchUpstreamCheckIgnoresTheCloneOwnMainWorktree(t *testing.T) {
	w := newUpstreamWS(t)

	if up := w.git(t, w.repo, "for-each-ref", "--format=%(upstream:short)", "refs/heads/main"); up != "origin/main" {
		t.Fatalf("fixture precondition: the clone's main tracks %q", up)
	}
	res := checkBranchUpstreams(context.Background(), w.cfg(), w.root)
	if res.Status != "ok" {
		t.Fatalf("a workspace with no task worktrees at all was reported %q: %s", res.Status, res.Message)
	}
	if res.Message != "no task worktrees to check" {
		t.Errorf("message is %q; a workspace whose only worktree is the clone's own has "+
			"nothing to scan", res.Message)
	}
}

// TestBranchUpstreamCheckIgnoresDetachedWorktrees: a detached HEAD has no
// branch, so no upstream, so nothing that can aim a push anywhere. Counting it
// inflates the denominator this check's whole message is built on.
//
// ⚠️ It also puts a repo in cfg that was never cloned, but does NOT claim to
// gate that: deleting the os.Stat guard in checkBranchUpstreams changes
// nothing, because linkedWorktreeBranches fails on a non-existent path and the
// caller already treats that as "could not read". The repo is here so the
// detached assertion runs in the presence of a missing clone, not as coverage
// of it.
//
// MUTANT: stop requiring a `branch refs/heads/` line, i.e. record the worktree
// on its path alone. The count becomes "1 of 2".
func TestBranchUpstreamCheckIgnoresDetachedWorktrees(t *testing.T) {
	w := newUpstreamWS(t)
	w.addTaskWorktree(t, "pf.demo-1", "polyforge/demo-1-tracking", true)
	detached := filepath.Join(w.root, "pf.demo-2", "demo")
	w.git(t, w.repo, "worktree", "add", "-q", "--detach", detached, "origin/main")

	cfg := w.cfg()
	proj := cfg.Projects["demo"]
	proj.Repos = append(proj.Repos, config.Repo{Name: "never-cloned"})
	cfg.Projects["demo"] = proj

	res := checkBranchUpstreams(context.Background(), cfg, w.root)
	if res.Status != "warning" || !strings.Contains(res.Message, "1 of 1") {
		t.Errorf("want one offender out of one scanned; got %s: %s", res.Status, res.Message)
	}
}

// TestBranchUpstreamCheckIgnoresPrunableWorktrees: a worktree whose directory
// was deleted but whose registration survives still prints its
// `branch refs/heads/<b>` line. It cannot receive a bare `git push` — the
// check's entire criterion — and the remedy printed for it would tell a human
// to run a command in a directory that is gone.
//
// MUTANT: drop the `prunable` case from linkedWorktreeBranches. The count
// becomes "2 of 2" and names a directory that does not exist.
func TestBranchUpstreamCheckIgnoresPrunableWorktrees(t *testing.T) {
	w := newUpstreamWS(t)
	w.addTaskWorktree(t, "pf.demo-1", "polyforge/demo-1-tracking", true)
	gone := w.addTaskWorktree(t, "pf.demo-gone", "polyforge/demo-gone-tracking", true)
	if err := os.RemoveAll(filepath.Dir(gone)); err != nil {
		t.Fatalf("remove worktree dir: %v", err)
	}
	// Deliberately NOT pruned: the registration surviving a deleted directory is
	// the state under test.
	if out := w.git(t, w.repo, "worktree", "list", "--porcelain"); !strings.Contains(out, "prunable") {
		t.Fatalf("fixture did not produce a prunable worktree:\n%s", out)
	}

	res := checkBranchUpstreams(context.Background(), w.cfg(), w.root)
	if !strings.Contains(res.Message, "1 of 1") {
		t.Errorf("a prunable worktree was counted: %s", res.Message)
	}
	if strings.Contains(res.Message, "pf.demo-gone") {
		t.Errorf("a worktree whose directory is gone was reported as fixable: %s", res.Message)
	}
}

// TestBranchUpstreamCheckSaysSoWhenItCouldNotLook.
//
// "Nothing found" and "nothing looked" must not print the same line. A repo
// directory that exists but is not a git repository makes `git worktree list`
// fail; folded into an empty result it produces `[ok] no task worktrees to
// check`, which is indistinguishable from a clean workspace — the failure mode
// of the instrument wearing the answer's clothes.
//
// MUTANT: have linkedWorktreeBranches return (nil, nil) on error.
func TestBranchUpstreamCheckSaysSoWhenItCouldNotLook(t *testing.T) {
	w := newUpstreamWS(t)
	notARepo := filepath.Join(w.root, ".repo", "broken")
	if err := os.MkdirAll(notARepo, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	cfg := w.cfg()
	proj := cfg.Projects["demo"]
	proj.Repos = append(proj.Repos, config.Repo{Name: "broken"})
	cfg.Projects["demo"] = proj

	res := checkBranchUpstreams(context.Background(), cfg, w.root)
	if res.Status != "warning" {
		t.Errorf("status is %q; a repo it could not read must not report ok: %s", res.Status, res.Message)
	}
	if !strings.Contains(res.Message, "could not be read") || !strings.Contains(res.Message, "broken") {
		t.Errorf("message does not name the repo it failed on: %s", res.Message)
	}
}

// TestBranchUpstreamCheckCountsASharedCloneOnce: a repo listed under two
// projects is one clone on disk. Counting it twice would double every number
// the check reports.
func TestBranchUpstreamCheckCountsASharedCloneOnce(t *testing.T) {
	w := newUpstreamWS(t)
	w.addTaskWorktree(t, "pf.demo-1", "polyforge/demo-1-tracking", true)

	cfg := w.cfg()
	cfg.Projects["other"] = config.Project{Repos: []config.Repo{{Name: "demo"}}}

	res := checkBranchUpstreams(context.Background(), cfg, w.root)
	if !strings.Contains(res.Message, "1 of 1") {
		t.Errorf("a clone shared by two projects was counted twice: %s", res.Message)
	}
}
