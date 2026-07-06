package coding

// Tests for Wrap's merged-PR branch (aihub#207 C4 / Change 1).
//
// Wrap shells out to real `gh`/`git` with no DI seam, so these tests build a
// real local git repo (origin = a local bare repo, so push is observable) and
// put a fake `gh` script on PATH that returns canned `pr list`/`pr create`
// JSON. This exercises the actual Wrap control flow instead of re-testing
// the control flow in isolation against mocks.

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
//   - `gh pr create ...` appends "create" to the createLog file and prints {"url":"...","number":1}
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
    echo "create" >> %q
    echo '{"url":"https://example.com/pr/1","number":1}'
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

// initRepoWithRemote creates a real git repo with a local bare "origin" so
// GitPush has something real to push to, and returns the worktree path.
func initRepoWithRemote(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	bare := filepath.Join(root, "bare")
	wt := filepath.Join(root, "wt")
	run := func(dir string, args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	if err := exec.Command("git", "init", "--bare", "-q", bare).Run(); err != nil {
		t.Fatalf("git init --bare: %v", err)
	}
	if out, err := exec.Command("git", "clone", "-q", bare, wt).CombinedOutput(); err != nil {
		t.Fatalf("git clone: %v\n%s", err, out)
	}
	run(wt, "config", "user.email", "t@t.com")
	run(wt, "config", "user.name", "t")
	if err := os.WriteFile(filepath.Join(wt, "f.txt"), []byte("hi"), 0644); err != nil {
		t.Fatalf("write f.txt: %v", err)
	}
	run(wt, "add", "f.txt")
	run(wt, "commit", "-q", "-m", "init")
	run(wt, "checkout", "-b", "task-branch")
	run(wt, "push", "-u", "origin", "task-branch")
	return wt
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

// remoteBranchExists checks whether branch still exists on the bare "origin"
// remote (i.e. wt/../bare).
func remoteBranchExists(t *testing.T, wt, branch string) bool {
	t.Helper()
	bare := filepath.Join(filepath.Dir(wt), "bare")
	cmd := exec.Command("git", "ls-remote", "--heads", bare, branch)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git ls-remote: %v\n%s", err, out)
	}
	return strings.TrimSpace(string(out)) != ""
}

// TestWrap_MergedPR_NoPush verifies that when GHGetPR (backed by `gh pr list`)
// reports the existing PR as MERGED, Wrap does NOT push — regression test for
// the "wrap resurrects a deleted branch" bug (aihub#207 Change 1).
func TestWrap_MergedPR_NoPush(t *testing.T) {
	wt := initRepoWithRemote(t)

	// Simulate the branch having been deleted on the remote after merge,
	// same as GitHub does when "delete branch on merge" is on.
	delCmd := exec.Command("git", "push", "origin", "--delete", "task-branch")
	delCmd.Dir = wt
	if out, err := delCmd.CombinedOutput(); err != nil {
		t.Fatalf("delete remote branch: %v\n%s", err, out)
	}
	if remoteBranchExists(t, wt, "task-branch") {
		t.Fatal("setup: remote branch should be deleted")
	}

	createLog := filepath.Join(t.TempDir(), "create.log")
	fakeGH(t, `[{"url":"https://example.com/pr/1","number":1,"state":"MERGED"}]`, createLog)

	wsRoot := filepath.Dir(wt)
	sf := writeWrapStateFile(t, wsRoot, "wi_WrapMerged", "aihub", wt)

	result, err := Wrap(context.Background(), sf, "aihub", wsRoot, "", "")
	if err != nil {
		t.Fatalf("Wrap: %v", err)
	}
	if state, _ := result["state"].(string); state != "MERGED" {
		t.Errorf("result[state] = %v, want MERGED", result["state"])
	}

	// The core assertion: no push happened, so the remote branch is still gone.
	if remoteBranchExists(t, wt, "task-branch") {
		t.Error("Wrap pushed and resurrected the deleted remote branch for a MERGED PR")
	}
	// And no duplicate PR was created.
	if _, err := os.Stat(createLog); !os.IsNotExist(err) {
		t.Error("Wrap called `gh pr create` for a wi whose PR is already MERGED")
	}
}

// TestWrap_OpenPR_NoPush verifies the pre-existing idempotent branch: an open
// PR for the branch means Wrap returns it without pushing or creating again.
func TestWrap_OpenPR_NoPush(t *testing.T) {
	wt := initRepoWithRemote(t)
	createLog := filepath.Join(t.TempDir(), "create.log")
	fakeGH(t, `[{"url":"https://example.com/pr/2","number":2,"state":"OPEN"}]`, createLog)

	wsRoot := filepath.Dir(wt)
	sf := writeWrapStateFile(t, wsRoot, "wi_WrapOpen", "aihub", wt)

	result, err := Wrap(context.Background(), sf, "aihub", wsRoot, "", "")
	if err != nil {
		t.Fatalf("Wrap: %v", err)
	}
	if num, _ := result["number"].(float64); num != 2 {
		t.Errorf("result[number] = %v, want 2 (existing open PR)", result["number"])
	}
	if _, err := os.Stat(createLog); !os.IsNotExist(err) {
		t.Error("Wrap called `gh pr create` for a wi that already has an open PR")
	}
}

// TestWrap_NoExistingPR_PushesAndCreates verifies the normal, non-idempotent
// path still pushes and creates a PR when none exists yet.
func TestWrap_NoExistingPR_PushesAndCreates(t *testing.T) {
	wt := initRepoWithRemote(t)
	// New local commit so the push has something new to send.
	if err := os.WriteFile(filepath.Join(wt, "f2.txt"), []byte("more"), 0644); err != nil {
		t.Fatalf("write f2.txt: %v", err)
	}
	for _, args := range [][]string{{"add", "f2.txt"}, {"commit", "-q", "-m", "second"}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = wt
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}

	createLog := filepath.Join(t.TempDir(), "create.log")
	fakeGH(t, `[]`, createLog) // no existing PR

	wsRoot := filepath.Dir(wt)
	sf := writeWrapStateFile(t, wsRoot, "wi_WrapNew", "aihub", wt)

	result, err := Wrap(context.Background(), sf, "aihub", wsRoot, "my title", "my body")
	if err != nil {
		t.Fatalf("Wrap: %v", err)
	}
	if url, _ := result["url"].(string); url == "" {
		t.Errorf("result[url] empty, want the newly created PR url; got %#v", result)
	}
	if _, err := os.Stat(createLog); err != nil {
		t.Error("Wrap did not call `gh pr create` when no PR existed yet")
	}
	if !remoteBranchExists(t, wt, "task-branch") {
		t.Error("Wrap did not push; remote branch should now carry the new commit")
	}
}
