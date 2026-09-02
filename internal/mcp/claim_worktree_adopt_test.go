package mcp_test

// aihub#328: the claim handler's worktree early return used to adopt any
// directory that os.Stat could see.
//
// WHY THESE ARE HANDLER TESTS AND NOT UNIT TESTS. The defect is not in
// addClaimWorktree — it is in the branch that runs INSTEAD of addClaimWorktree,
// and every test in claim_worktree_test.go asserts its own precondition that the
// worktree directory does NOT exist (see claimRepo.add). The whole of this
// defect lives in the case those tests exclude by construction.
//
// WHAT IS ASSERTED, and why it is not "the function returned an error": adoption
// is written to the state file, and the state file is what every later claim's
// early return and every downstream tool (pf_diff / pf_commit / pf_ship) reads.
// A rejection that still recorded the path would leave the damage entirely
// intact, so the assertion is on the recorded worktree map, not on a return
// value.
//
// ISOLATION: newClaimWorkspace sets POLYFORGE_WORKSPACE_ROOT to a t.TempDir(),
// which is load-bearing here rather than tidy — these tests make a real claim,
// which WRITES a state file, and config.StateDir() otherwise walks up to the
// live workspace whose state directory holds every claimed work item's
// credentials. assertStateDirIsSandboxed below turns that from a convention into
// an assertion.

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/GMISWE/ieops-aihub/internal/config"
)

// gitOutput runs git in dir and hands back its combined output and error. wsGit
// fatals on failure, which is right for fixture setup but useless for the
// assertions below, whose whole point is to observe a git command SUCCEEDING
// where a naive reading of the fix says it should fail.
func gitOutput(dir string, args ...string) (string, error) {
	full := append([]string{"-C", dir}, args...)
	out, err := exec.Command("git", full...).CombinedOutput()
	return string(out), err
}

// claimWiring is the fake-aihub plumbing all three tests share: one work item,
// one repo, a claim that succeeds on the server.
type claimWiring struct {
	w     *claimWorkspace
	f     *fakeAihub
	wiID  string
	wtDir string // where the handler would put .repo/aihub's worktree
}

func newClaimWiring(t *testing.T, wiID string) *claimWiring {
	t.Helper()
	w := newClaimWorkspace(t)
	assertStateDirIsSandboxed(t, w.root)
	f := newFakeAihub(t)
	f.on("/v1/work_items/"+wiID+"/claim", func(map[string]any) (int, any) {
		return 200, claimResponse(wiID, "aihub#328", "aihub", "validate adopted worktrees")
	})
	return &claimWiring{
		w:     w,
		f:     f,
		wiID:  wiID,
		wtDir: filepath.Join(w.root, "pf.aihub-328", "aihub"),
	}
}

func (c *claimWiring) claim(t *testing.T) map[string]any {
	t.Helper()
	result, isErr := callTool(t, c.f, "pf_claim_work_item", map[string]any{
		"work_item_id":    c.wiID,
		"idempotency_key": "idem-" + c.wiID,
	})
	if isErr {
		t.Fatalf("pf_claim_work_item failed: %v", result)
	}
	return result
}

// assertStateDirIsSandboxed proves the env var actually redirected the state
// directory before anything writes to it. Without this the isolation comment
// above is a claim; with it, a change to config.StateDir() that stopped honouring
// POLYFORGE_WORKSPACE_ROOT fails here instead of silently writing into the live
// workspace's credential directory.
func assertStateDirIsSandboxed(t *testing.T, root string) {
	t.Helper()
	dir := config.StateDir()
	if !strings.HasPrefix(dir, root+string(os.PathSeparator)) {
		t.Fatalf("config.StateDir() = %q, which is outside this test's temp root %q — refusing to run: this test writes state files", dir, root)
	}
}

// recordedWorktree is what the claim persisted for repo, which is the thing that
// outlives the call and drives every later early return.
func recordedWorktree(t *testing.T, wiID, repo string) (string, bool) {
	t.Helper()
	sf, err := config.ReadStateFile(wiID)
	if err != nil {
		t.Fatalf("read state file for %s: %v", wiID, err)
	}
	p, ok := sf.Worktrees[repo]
	return p, ok
}

// gitInitWorkspaceRoot reproduces the LIVE topology, and it is the entire point
// of the second test below: the polyforge workspace root is itself a git
// repository (gmi-ws is), and the pf.<project>-<seq>/<repo> directories sit
// inside it.
func gitInitWorkspaceRoot(t *testing.T, root string) {
	t.Helper()
	wsGit(t, "", "init", "-q", "-b", "main", root)
}

// TestClaimDoesNotAdoptADirectoryWithADanglingGitPointer is half-built shape one:
// `git worktree add` got far enough to write the .git file and then died — the
// process was SIGKILLed, or the machine rebooted — leaving a pointer to an admin
// directory that was never created.
//
// MUTANT (the pre-aihub#328 build): the handler's early return is
//
//	if _, statErr := os.Stat(wtPath); statErr == nil { worktrees[repo.Name] = wtPath; ... }
//
// with no validation. The directory is adopted, written to the state file, and
// every later claim takes the same early return, so it is adopted forever.
func TestClaimDoesNotAdoptADirectoryWithADanglingGitPointer(t *testing.T) {
	const wiID = "wi_01JADOPTDANGLING1"
	c := newClaimWiring(t, wiID)

	if err := os.MkdirAll(c.wtDir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", c.wtDir, err)
	}
	pointer := fmt.Sprintf("gitdir: %s\n", filepath.Join(c.w.src, ".git", "worktrees", "never-created"))
	if err := os.WriteFile(filepath.Join(c.wtDir, ".git"), []byte(pointer), 0o644); err != nil {
		t.Fatalf("write .git pointer: %v", err)
	}
	// Not an empty directory: a half-finished checkout has files in it, and
	// "empty" and "half-built" are indistinguishable to os.Stat but not to the
	// fix, so the fixture must be the harder of the two.
	if err := os.WriteFile(filepath.Join(c.wtDir, "main.txt"), []byte("half"), 0o644); err != nil {
		t.Fatalf("write partial checkout file: %v", err)
	}

	result := c.claim(t)

	if p, ok := recordedWorktree(t, wiID, "aihub"); ok {
		t.Errorf("the state file records %q as repo aihub's worktree, but its .git pointer names an admin directory that does not exist. Every later claim now short-circuits on it and every later pf_diff/pf_commit/pf_ship is handed a path that is not a worktree", p)
	}
	if wts, ok := result["worktrees"].(map[string]any); ok && wts["aihub"] != nil {
		t.Errorf("the claim response hands back %v as a worktree", wts["aihub"])
	}
	assertProblemReported(t, result, c.wtDir)
}

// TestClaimDoesNotAdoptAHalfCheckedOutDirectoryInsideAGitWorkspace is half-built
// shape two, and it is the one that decides which git question the fix may ask.
//
// The shape: `git worktree add` was killed before it wrote .git at all, or the
// disk filled during the checkout, leaving source files and nothing else.
//
// ⚠️ WHY THE WORKSPACE ROOT IS git init'ED HERE. aihub#328 prescribes
// `git -C <wtPath> rev-parse --git-dir`. Against a t.TempDir() that is not
// inside any repository that works — and it would be a NO-OP IN PRODUCTION,
// because git searches parent directories and the live polyforge workspace root
// is itself a git repository. The fixture therefore reproduces that, and the
// assertion below states it: with the root a repo, `rev-parse --git-dir` exits 0
// on this broken directory and answers with the WORKSPACE's .git. Measured on
// git 2.43.0. The fix compares `rev-parse --show-toplevel` against wtPath
// instead, which is what tells the two apart.
//
// MUTANT 1 (the pre-aihub#328 build): no validation at all — adopted.
// MUTANT 2 (the naive fix): validate with `rev-parse --git-dir` and accept exit
// 0. Green against a non-repo temp dir, red here, adopted in production.
func TestClaimDoesNotAdoptAHalfCheckedOutDirectoryInsideAGitWorkspace(t *testing.T) {
	const wiID = "wi_01JADOPTHALFBUILT"
	c := newClaimWiring(t, wiID)
	gitInitWorkspaceRoot(t, c.w.root)

	if err := os.MkdirAll(c.wtDir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", c.wtDir, err)
	}
	for _, f := range []string{"main.txt", "go.mod"} {
		if err := os.WriteFile(filepath.Join(c.wtDir, f), []byte("half"), 0o644); err != nil {
			t.Fatalf("write %s: %v", f, err)
		}
	}
	if _, err := os.Stat(filepath.Join(c.wtDir, ".git")); err == nil {
		t.Fatal("fixture: the directory has a .git, so this is the other half-built shape")
	}

	// State the blind spot as an assertion rather than as a claim in a comment:
	// if this ever stopped being true, this test would pass for a reason that has
	// nothing to do with what it is guarding.
	if out, err := gitOutput(c.wtDir, "rev-parse", "--git-dir"); err != nil {
		t.Fatalf("fixture: `git rev-parse --git-dir` failed inside %s (%v), so this test no longer isolates the parent-search blind spot — it would pass against the naive --git-dir check too", c.wtDir, err)
	} else if strings.TrimSpace(out) == "" {
		t.Fatal("fixture: --git-dir produced nothing")
	}

	result := c.claim(t)

	if p, ok := recordedWorktree(t, wiID, "aihub"); ok {
		t.Errorf("the state file records %q as repo aihub's worktree, but it has no .git of its own — git only accepted it because the WORKSPACE ROOT is a repository. This is the shape a `rev-parse --git-dir` check adopts in production", p)
	}
	if wts, ok := result["worktrees"].(map[string]any); ok && wts["aihub"] != nil {
		t.Errorf("the claim response hands back %v as a worktree", wts["aihub"])
	}
	assertProblemReported(t, result, c.wtDir)
}

// TestClaimReusesAHealthyExistingWorktree is the positive control aihub#328
// requires: the fix must reject broken directories, not stop reusing worktrees.
//
// It runs under the same git-repo workspace root as the test above, so the
// stricter check is exercised in the harder topology rather than only where it
// cannot misfire.
//
// The assertion that the worktree was REUSED rather than rebuilt is an untracked
// scratch file. A rebuild — which is the tempting over-correction — would have to
// remove the directory first, so the file is what a rebuild cannot leave behind,
// whereas the branch name and the path survive either way.
func TestClaimReusesAHealthyExistingWorktree(t *testing.T) {
	const wiID = "wi_01JADOPTHEALTHYWT"
	c := newClaimWiring(t, wiID)
	gitInitWorkspaceRoot(t, c.w.root)

	wsGit(t, c.w.src, "worktree", "add", "-q", "-b", "polyforge/aihub-328-earlier-claim", c.wtDir)
	const scratch = "uncommitted-work.txt"
	wsWrite(t, c.wtDir, scratch, "work nobody has committed yet")

	result := c.claim(t)

	p, ok := recordedWorktree(t, wiID, "aihub")
	if !ok {
		t.Fatalf("a healthy existing worktree at %s was not reused — the state file records no worktree for repo aihub", c.wtDir)
	}
	if p != c.wtDir {
		t.Errorf("state file records worktree %q, want %q", p, c.wtDir)
	}
	if _, err := os.Stat(filepath.Join(c.wtDir, scratch)); err != nil {
		t.Errorf("%s is gone: the worktree was rebuilt rather than reused, which destroys uncommitted work (%v)", scratch, err)
	}
	if got := wsGit(t, c.wtDir, "rev-parse", "--abbrev-ref", "HEAD"); got != "polyforge/aihub-328-earlier-claim" {
		t.Errorf("worktree is on %q, want the branch the earlier claim left it on", got)
	}
	if problems, ok := result["worktree_problems"]; ok {
		t.Errorf("a healthy worktree was reported as a problem: %v", problems)
	}
}

// TestClaimReusesAHealthyWorktreeWhenGitWritesToStderr guards the failure mode
// that the validation itself introduced, and it is the dangerous direction: a
// false REJECT tells the operator to `rm -rf` a directory that was fine.
//
// verifyClaimWorktree is the only place in tools_lifecycle.go that PARSES git's
// stdout; everywhere else the output is error text, which is why CombinedOutput
// is the house style and why reaching for it here was the natural mistake. git
// writes diagnostics to stderr and still exits 0, so with CombinedOutput the
// parsed value becomes "trace: built-in: git rev-parse --show-toplevel\n<path>",
// the comparison fails, and EVERY healthy worktree in the workspace is rejected
// at once. GIT_TRACE is one trigger; broken-ref and deprecated-config warnings
// are others.
//
// MUTANT: change `cmd.Output()` back to `.CombinedOutput()` in
// verifyClaimWorktree. This goes red; every other test in this file stays green,
// because none of them makes git chatty.
func TestClaimReusesAHealthyWorktreeWhenGitWritesToStderr(t *testing.T) {
	const wiID = "wi_01JADOPTCHATTYGIT"
	c := newClaimWiring(t, wiID)
	gitInitWorkspaceRoot(t, c.w.root)
	wsGit(t, c.w.src, "worktree", "add", "-q", "-b", "polyforge/aihub-328-chatty", c.wtDir)

	// Set AFTER the fixture is built, so the setup's own git calls stay quiet and
	// only the code under test sees a chatty git.
	t.Setenv("GIT_TRACE", "1")
	if out, err := gitOutput(c.wtDir, "rev-parse", "--show-toplevel"); err != nil {
		t.Fatalf("fixture: rev-parse failed (%v)", err)
	} else if !strings.Contains(out, "trace:") {
		t.Skipf("this git build does not emit a trace line under GIT_TRACE=1 (got %q), so the stderr-contamination shape cannot be reproduced here", strings.TrimSpace(out))
	}

	c.claim(t)

	p, ok := recordedWorktree(t, wiID, "aihub")
	if !ok {
		t.Fatalf("a healthy worktree at %s was rejected because git printed a diagnostic on stderr. The validation parses stdout, so it must not read the combined buffer — and the message it produces tells the operator to rm -rf a directory that is fine", c.wtDir)
	}
	if p != c.wtDir {
		t.Errorf("state file records worktree %q, want %q", p, c.wtDir)
	}
}

// TestClaimReusesAHealthyWorktreeWithGitDirInTheEnvironment is the second
// false-reject shape: exec.Command inherits os.Environ(), and `-C` alone does
// not beat GIT_DIR + GIT_WORK_TREE. Measured on git 2.43.0 — with both set,
// `git -C <worktree> rev-parse --show-toplevel` prints the OTHER repository's
// root and exits 0, so the comparison fails for every repo at once.
//
// Reachable when the MCP server is launched from a git hook or from a shell
// that exported them.
//
// MUTANT: drop the `cmd.Env = envWithout(...)` line. This goes red.
func TestClaimReusesAHealthyWorktreeWithGitDirInTheEnvironment(t *testing.T) {
	const wiID = "wi_01JADOPTGITDIRENV"
	c := newClaimWiring(t, wiID)
	gitInitWorkspaceRoot(t, c.w.root)
	wsGit(t, c.w.src, "worktree", "add", "-q", "-b", "polyforge/aihub-328-gitdir", c.wtDir)

	// Set after the fixture, for the same reason as above: these would redirect
	// the setup's own git calls.
	t.Setenv("GIT_DIR", filepath.Join(c.w.src, ".git"))
	t.Setenv("GIT_WORK_TREE", c.w.src)
	// State the hazard as an assertion: if `-C` ever started winning, this test
	// would pass without exercising anything.
	if out, err := gitOutput(c.wtDir, "rev-parse", "--show-toplevel"); err != nil {
		t.Fatalf("fixture: rev-parse failed (%v)", err)
	} else if strings.TrimSpace(out) == c.wtDir {
		t.Skipf("this git resolves -C over GIT_DIR/GIT_WORK_TREE, so the hazard is not reproducible here")
	}

	c.claim(t)

	if p, ok := recordedWorktree(t, wiID, "aihub"); !ok {
		t.Fatalf("a healthy worktree at %s was rejected because GIT_DIR/GIT_WORK_TREE were in the environment; the check must not let the ambient environment redirect it", c.wtDir)
	} else if p != c.wtDir {
		t.Errorf("state file records worktree %q, want %q", p, c.wtDir)
	}
}

// assertProblemReported: the claim itself succeeds, so the only way the caller
// learns its repo has no worktree is this key. Before aihub#328 the loop's one
// failure channel was a line on stderr, which an MCP server writes to a log the
// calling agent never reads — and "nobody notices" is the defect, so a silent
// skip would only relocate it.
func assertProblemReported(t *testing.T, result map[string]any, wtPath string) {
	t.Helper()
	raw, ok := result["worktree_problems"].([]any)
	if !ok || len(raw) == 0 {
		t.Fatalf("the claim reported no worktree_problems, so the caller has no way to learn that %s was rejected: %v", wtPath, result)
	}
	joined := fmt.Sprint(raw...)
	if !strings.Contains(joined, wtPath) {
		t.Errorf("worktree_problems never names the rejected path %s: %v", wtPath, raw)
	}
	// The remedy has to be in the message: the handler deliberately does not
	// delete anything, so a human has to, and telling them only that something is
	// wrong leaves them to guess between `worktree prune`, `rm -rf`, and re-cloning.
	for _, want := range []string{"worktree prune", "rm -rf"} {
		if !strings.Contains(joined, want) {
			t.Errorf("worktree_problems does not tell the caller how to clean up (%q missing): %v", want, raw)
		}
	}
	// ⚠️ AND IN THE RIGHT ORDER. `git worktree prune` only drops admin entries
	// whose working tree is already MISSING, so prune-then-rm is a no-op followed
	// by a delete that strands the registration, and the next `worktree add` on
	// that path fails with "is a missing but already registered worktree".
	// Measured on git 2.43.0: prune-then-rm → re-add exit 128; rm-then-prune →
	// exit 0. Asserting only that both commands appear (which the first draft of
	// this helper did) pins advice that produces the failure it exists to prevent.
	rmAt, pruneAt := strings.Index(joined, "rm -rf"), strings.Index(joined, "worktree prune")
	if rmAt >= 0 && pruneAt >= 0 && rmAt > pruneAt {
		t.Errorf("the cleanup advice runs `worktree prune` before `rm -rf`. That order leaves the worktree registered and the NEXT claim's `git worktree add` fails with \"missing but already registered worktree\": %v", raw)
	}
}
