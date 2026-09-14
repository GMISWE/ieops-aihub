package engine

import (
	"errors"
	"fmt"
	"strings"
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

// TestCleanupWorktrees_RefusesAParentThatIsNotAPolyforgeWorktreeDir is the
// aihub#679 guard: rmParent is an unconditional recursive delete and the path it
// is handed comes from data nobody validated, so the parent's basename is
// checked before it is used.
//
// 🔴 THE RECORDING rmParent IS THE INSTRUMENT, not a stand-in. In production
// rmParent is os.RemoveAll behind an is-a-directory guard (internal/cli/engine.go
// and internal/cli/drain.go both spell the same closure), so "was rmParent
// called" IS "was the tree deleted". Asserting on the log rather than on a
// temporary directory is what makes the destructive case safe to test at all:
// the arm below hands it $HOME-shaped paths, and a test that actually deleted
// them to prove the point would be the defect.
func TestCleanupWorktrees_RefusesAParentThatIsNotAPolyforgeWorktreeDir(t *testing.T) {
	// The corruptions this exists for. Each is one plausible way the worktree map
	// stops describing a pf.<slug>/ directory: a truncated or hand-edited state
	// file, and a hand-typed `engine cleanup-worktrees --worktrees` (which takes
	// the map straight off argv with no validation whatsoever).
	for _, tc := range []struct {
		name string
		path string
		dir  string
	}{
		{"home directory", "/root/aihub", "/root"},
		{"workspace root itself", "/ws/aihub", "/ws"},
		{"filesystem root", "/aihub", "/"},
		{"a sibling repo checkout", "/ws/.repo/aihub", "/ws/.repo"},
		{"a pf-ish name that is not the prefix", "/ws/mypf.aihub-1/aihub", "/ws/mypf.aihub-1"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var log []string
			removed, repoErrs, parentErr := CleanupWorktrees(
				recordingGit(&log, nil), recordingRmParent(&log, ""), "/ws",
				map[string]string{"aihub": tc.path})

			// 🔴 THE ASSERTION THAT MATTERS. Everything else here is reporting;
			// this is the deletion not happening.
			for _, entry := range log {
				if entry == "rm "+tc.dir {
					t.Fatalf("CleanupWorktrees called rmParent(%q) — in production that is "+
						"os.RemoveAll and %q is gone. The worktree path was %q, which is not "+
						"under a pf.<slug>/ directory.", tc.dir, tc.dir, tc.path)
				}
			}

			// Refusing has to be LOUD. The per-repo `git worktree remove` above it
			// has already run, so a silent skip leaves a half-finished cleanup with
			// no reason given, and cleanup is best-effort enough that nobody would
			// look.
			if parentErr == nil {
				t.Fatal("CleanupWorktrees returned no parentErr. It declined to remove the " +
					"shared parent and said nothing, which is indistinguishable from having " +
					"removed it successfully.")
			}
			if !strings.Contains(parentErr.Error(), tc.dir) {
				t.Errorf("parentErr = %v, and it does not name the directory %q it refused. "+
					"The operator has to be able to tell which path was wrong.", parentErr, tc.dir)
			}

			// The per-repo work still happened and is still reported: the guard is
			// on the shared parent only, and a refusal there must not be read as
			// "nothing was cleaned up".
			if len(removed) != 1 || removed[0] != "aihub" {
				t.Errorf("removed = %v, want [aihub] — the per-repo `git worktree remove` runs "+
					"before the parent guard and its result is still the caller's answer",
					removed)
			}
			if len(repoErrs) != 0 {
				t.Errorf("repoErrs = %v, want empty", repoErrs)
			}
		})
	}
}

// TestCleanupWorktrees_StillRemovesLegitimatePfParents is the guard's negative
// control, and it is the half that decides whether the guard is a fix or an
// outage.
//
// 🔴 "NOTHING WAS DELETED" IS ALSO WHAT A GUARD THAT REFUSES EVERYTHING
// PRODUCES. Every arm in the test above is satisfied by `return parentErr` as
// the first line of the function. These rows are the real shapes — the workspace
// layout, the seq-and-ulid form pf.<seq>.<ulid8>, and the absolute path outside
// the workspace root that internal/cli/drain_test.go already pins as legitimate
// input — and they must all still be removed.
func TestCleanupWorktrees_StillRemovesLegitimatePfParents(t *testing.T) {
	for _, tc := range []struct {
		name string
		path string
		dir  string
	}{
		{"the ordinary workspace shape", "/ws/pf.aihub-679/aihub", "/ws/pf.aihub-679"},
		{"the seq.ulid shape doctor.go also accepts", "/ws/pf.7.aBcD1234/aihub", "/ws/pf.7.aBcD1234"},
		// 🔴 NOT under --workspace-root, and deliberately so. This is why the
		// guard is on the basename and not on a workspaceRoot prefix: a prefix
		// rule would refuse this, and internal/cli/drain_test.go pins
		// /elsewhere/pf.aihub-667/aihub as a path the drain path really passes in.
		{"an absolute path outside the workspace root", "/elsewhere/pf.aihub-667/aihub", "/elsewhere/pf.aihub-667"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var log []string
			removed, repoErrs, parentErr := CleanupWorktrees(
				recordingGit(&log, nil), recordingRmParent(&log, ""), "/ws",
				map[string]string{"aihub": tc.path})
			if parentErr != nil {
				t.Fatalf("CleanupWorktrees refused a legitimate polyforge worktree parent: %v. "+
					"The aihub#679 guard may only reject directories that are not named "+
					"pf.<something>; refusing this one strands the parent directory of every "+
					"wrapped work item.", parentErr)
			}
			found := false
			for _, entry := range log {
				if entry == "rm "+tc.dir {
					found = true
				}
			}
			if !found {
				t.Errorf("rmParent(%q) was never called; log = %v. The parent of a real "+
					"pf.<slug>/ worktree must still be removed once, after the per-repo "+
					"removals.", tc.dir, log)
			}
			if len(removed) != 1 || len(repoErrs) != 0 {
				t.Errorf("removed = %v, repoErrs = %v, want [aihub] and empty", removed, repoErrs)
			}
		})
	}
}
