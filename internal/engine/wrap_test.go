package engine

import (
	"errors"
	"fmt"
	"testing"
)

// recordingGit returns a GitRunner that appends "<dir>|<args...>" to *log for every call and
// succeeds unless dir is in failDirs.
func recordingGit(log *[]string, failDirs map[string]bool) GitRunner {
	return func(dir string, args ...string) (string, error) {
		*log = append(*log, fmt.Sprintf("git dir=%s args=%v", dir, args))
		if failDirs[dir] {
			return "", errors.New("simulated git failure")
		}
		return "", nil
	}
}

// recordingRmParent returns a func(string) error that appends "rm <path>" to *log and succeeds
// unless path is failPath.
func recordingRmParent(log *[]string, failPath string) func(string) error {
	return func(path string) error {
		*log = append(*log, "rm "+path)
		if failPath != "" && path == failPath {
			return errors.New("simulated rm failure")
		}
		return nil
	}
}

func TestCleanupWorktrees_RemovesEachRepoThenParentOnce(t *testing.T) {
	var log []string
	worktrees := map[string]string{
		"aihub":            "/ws/pf.aihub-654/aihub",
		"polyforge-coding": "/ws/pf.aihub-654/polyforge-coding",
	}
	removed, repoErrs, parentErr := CleanupWorktrees(recordingGit(&log, nil), recordingRmParent(&log, ""), "/ws", worktrees)
	if parentErr != nil {
		t.Fatalf("CleanupWorktrees: parentErr = %v", parentErr)
	}
	if len(repoErrs) != 0 {
		t.Fatalf("repoErrs = %v, want empty", repoErrs)
	}
	if len(removed) != 2 {
		t.Fatalf("removed = %v, want 2 entries", removed)
	}

	// Exactly 3 calls total: one `git worktree remove` per repo, then exactly one parent
	// removal - and the parent removal is STRICTLY LAST.
	if len(log) != 3 {
		t.Fatalf("log = %v, want 3 entries", log)
	}
	for i, entry := range log[:2] {
		if entry == "" {
			t.Errorf("log[%d] is empty", i)
		}
	}
	last := log[len(log)-1]
	if last != "rm /ws/pf.aihub-654" {
		t.Errorf("last log entry = %q, want the shared parent removed last: %q",
			last, "rm /ws/pf.aihub-654")
	}

	// Per-repo removal happens in sorted-name order: "aihub" before "polyforge-coding", and the
	// git call's dir is the MAIN CLONE (workspaceRoot/.repo/<name>), not the worktree path.
	if log[0] != "git dir=/ws/.repo/aihub args=[worktree remove --force /ws/pf.aihub-654/aihub]" {
		t.Errorf("log[0] = %q, want the aihub repo removed first (sorted order), from its main clone dir", log[0])
	}
}

func TestCleanupWorktrees_DirIsMainCloneTargetIsWorktreePath(t *testing.T) {
	// W4 regression: pin the FIXED calling convention - git(dir, args...) is invoked with dir ==
	// <workspaceRoot>/.repo/<name>, the repo's MAIN CLONE, and the worktree path appears only as
	// the trailing target argument. This is the load-bearing half of the W4 fix: the earlier
	// version passed dir == the worktree path itself, which fails at exec's own chdir (before
	// git even runs) the moment the worktree directory has already been deleted by an
	// interrupted prior cleanup - exactly the retry case lifecycle-details.md's main-clone form
	// exists to survive. This fake cannot reproduce the chdir failure itself (that needs a real
	// os/exec-backed GitRunner and an actually-deleted directory, exercised at the
	// internal/cli/engine_test.go real-git layer instead) but it does pin that CleanupWorktrees
	// requests the main-clone dir, which is the fix a real GitRunner depends on.
	var gotDir string
	var gotArgs []string
	git := func(dir string, args ...string) (string, error) {
		gotDir = dir
		gotArgs = args
		return "", nil
	}
	var rmLog []string
	_, repoErrs, parentErr := CleanupWorktrees(git, recordingRmParent(&rmLog, ""), "/ws", map[string]string{
		"aihub": "/ws/pf.aihub-654/aihub",
	})
	if parentErr != nil {
		t.Fatalf("CleanupWorktrees: parentErr = %v", parentErr)
	}
	if len(repoErrs) != 0 {
		t.Fatalf("repoErrs = %v, want empty", repoErrs)
	}
	if gotDir != "/ws/.repo/aihub" {
		t.Errorf("dir = %q, want the repo's main clone path %q, not the worktree path", gotDir, "/ws/.repo/aihub")
	}
	wantArgs := []string{"worktree", "remove", "--force", "/ws/pf.aihub-654/aihub"}
	if len(gotArgs) != len(wantArgs) {
		t.Fatalf("args = %v, want %v", gotArgs, wantArgs)
	}
	for i := range wantArgs {
		if gotArgs[i] != wantArgs[i] {
			t.Errorf("args[%d] = %q, want %q", i, gotArgs[i], wantArgs[i])
		}
	}
}

func TestCleanupWorktrees_EmptyMapIsANoOp(t *testing.T) {
	var log []string
	removed, repoErrs, parentErr := CleanupWorktrees(recordingGit(&log, nil), recordingRmParent(&log, ""), "/ws", map[string]string{})
	if parentErr != nil {
		t.Fatalf("CleanupWorktrees: parentErr = %v", parentErr)
	}
	if len(repoErrs) != 0 {
		t.Errorf("repoErrs = %v, want empty", repoErrs)
	}
	if len(removed) != 0 {
		t.Errorf("removed = %v, want empty", removed)
	}
	if len(log) != 0 {
		t.Errorf("log = %v, want no git/rmParent calls at all for an empty worktrees map", log)
	}
}

func TestCleanupWorktrees_PerRepoFailureStillRemovesParentUnconditionally(t *testing.T) {
	// W4 regression: the shared parent must be removed even when a per-repo git call failed -
	// lifecycle-details.md's unconditional-parent-removal rule has no carve-out for a per-repo
	// failure. The failing repo's error must still be visible (via repoErrs), just not treated
	// as a reason to strand every OTHER, successfully-detached repo's worktree under an
	// un-removed pf.<slug>/ parent.
	var log []string
	worktrees := map[string]string{
		"aihub":            "/ws/pf.aihub-654/aihub",
		"polyforge-coding": "/ws/pf.aihub-654/polyforge-coding",
	}
	failing := map[string]bool{"/ws/.repo/aihub": true}
	removed, repoErrs, parentErr := CleanupWorktrees(recordingGit(&log, failing), recordingRmParent(&log, ""), "/ws", worktrees)
	if parentErr != nil {
		t.Fatalf("CleanupWorktrees: parentErr = %v, want nil (parent removal itself did not fail)", parentErr)
	}
	if len(repoErrs) != 1 || repoErrs["aihub"] == nil {
		t.Fatalf("repoErrs = %v, want exactly one entry for %q", repoErrs, "aihub")
	}
	// The failing repo ("aihub") must be absent from removed; the other repo still succeeded
	// and is NOT rolled back.
	for _, name := range removed {
		if name == "aihub" {
			t.Errorf("removed = %v, must not contain the repo whose git call failed", removed)
		}
	}
	if len(removed) != 1 || removed[0] != "polyforge-coding" {
		t.Errorf("removed = %v, want exactly [\"polyforge-coding\"]", removed)
	}
	// The shared parent MUST have been removed, despite the per-repo failure above.
	sawParentRemoval := false
	for _, entry := range log {
		if entry == "rm /ws/pf.aihub-654" {
			sawParentRemoval = true
		}
	}
	if !sawParentRemoval {
		t.Errorf("log = %v, want the shared parent removed even though a per-repo removal failed", log)
	}
}

func TestCleanupWorktrees_AllFailingReposAppearInRepoErrs(t *testing.T) {
	// m5 regression: repoErrs must carry an entry for EVERY repo whose `git worktree remove`
	// failed, not just the first one encountered. repoErrs is a map precisely so a second (or
	// third) failure is not silently overwritten or dropped by an early-return/break; a caller
	// (internal/cli's runEngineCleanupWorktrees) ranges over the whole map when building its
	// JSON "errors" object, so a repoErrs that stopped at the first failure would have silently
	// hidden every failure after it.
	var log []string
	worktrees := map[string]string{
		"aihub":            "/ws/pf.aihub-654/aihub",
		"polyforge-coding": "/ws/pf.aihub-654/polyforge-coding",
		"tether":           "/ws/pf.aihub-654/tether",
	}
	failing := map[string]bool{"/ws/.repo/aihub": true, "/ws/.repo/tether": true}
	removed, repoErrs, parentErr := CleanupWorktrees(recordingGit(&log, failing), recordingRmParent(&log, ""), "/ws", worktrees)
	if parentErr != nil {
		t.Fatalf("CleanupWorktrees: parentErr = %v, want nil", parentErr)
	}
	if len(repoErrs) != 2 || repoErrs["aihub"] == nil || repoErrs["tether"] == nil {
		t.Fatalf("repoErrs = %v, want entries for both %q and %q", repoErrs, "aihub", "tether")
	}
	if len(removed) != 1 || removed[0] != "polyforge-coding" {
		t.Errorf("removed = %v, want exactly [\"polyforge-coding\"] (the only repo that did not fail)", removed)
	}
}

func TestCleanupWorktrees_ParentRemovalFailureIsReturnedNotSwallowed(t *testing.T) {
	var log []string
	worktrees := map[string]string{"aihub": "/ws/pf.aihub-654/aihub"}
	_, _, parentErr := CleanupWorktrees(recordingGit(&log, nil), recordingRmParent(&log, "/ws/pf.aihub-654"), "/ws", worktrees)
	if parentErr == nil {
		t.Fatal("CleanupWorktrees: want a parentErr when rmParent fails, got nil")
	}
}

func TestCleanupWorktrees_NilRmParentSkipsParentRemoval(t *testing.T) {
	// A caller that only wants the per-repo removals (e.g. a dry run) can pass a nil
	// rmParent; CleanupWorktrees must not panic and must simply skip that step.
	var log []string
	removed, repoErrs, parentErr := CleanupWorktrees(recordingGit(&log, nil), nil, "/ws", map[string]string{
		"aihub": "/ws/pf.aihub-654/aihub",
	})
	if parentErr != nil {
		t.Fatalf("CleanupWorktrees: parentErr = %v", parentErr)
	}
	if len(repoErrs) != 0 {
		t.Errorf("repoErrs = %v, want empty", repoErrs)
	}
	if len(removed) != 1 {
		t.Errorf("removed = %v, want 1 entry", removed)
	}
}
