package coding

// aihub#366 — the git half of the commit-time lock gate.
//
// Two things are under test here and they fail in opposite directions:
//
//	GitStagedPaths   must see EVERY file the pending commit touches. A path it
//	                 misses is a file that gets committed with no lock behind it,
//	                 silently — which is the defect, not a smaller version of it.
//	GitCommitGated   must actually stop the commit when the gate refuses. A gate
//	                 whose refusal still produces a commit is decoration.
//
// The differential in TestCommitGate_AbsentVersusPresent is the permanent form
// of this work item's negative control: the SAME commit, of the SAME
// out-of-scope file, run twice — once with no gate (which is exactly the
// pre-aihub#366 behaviour, still reachable through GitCommit) and once with one.
// Without the "absent" arm, "the gate blocked it" would be consistent with a
// commit that could never have succeeded anyway.

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// stagedPaths stages everything and returns what the pending commit contains.
func stagedPaths(t *testing.T, r *testRepo) []string {
	t.Helper()
	if err := GitStage(context.Background(), r.wt, nil); err != nil {
		t.Fatalf("GitStage: %v", err)
	}
	got, err := GitStagedPaths(context.Background(), r.wt)
	if err != nil {
		t.Fatalf("GitStagedPaths: %v", err)
	}
	return got
}

func contains(hay []string, want string) bool {
	for _, h := range hay {
		if h == want {
			return true
		}
	}
	return false
}

// TestGitStagedPaths_CoversEveryChangeShape walks the forms a change can take.
//
// The list is not "a few cases": every entry is a shape that a plausible
// implementation of this function gets wrong, and two of them are wrong in the
// DEFAULT git configuration rather than in some corner:
//
//	renamed    with rename detection on — the default — `--name-only` prints ONE
//	           line, the destination, and the deleted SOURCE never appears
//	non-ASCII  core.quotePath is on by default, so without -z the path comes back
//	           C-quoted and would be compared against the lock table under a name
//	           no lock can have
//
// ⚠️ THE "added file" SUBTEST IS NOT ONE OF THEM, and this comment used to say
// it was — that a brand-new file is invisible to `git diff HEAD`. Git is blind
// to UNTRACKED files; nothing here is untracked by the time it is asked, because
// stagedPaths runs `git add` first. Measured: swap GitStagedPaths to
// worktree-vs-HEAD and this subtest still passes. The range that loses
// information loses it in the opposite direction, and
// TestGitStagedPaths_WorktreeVsHEADWouldOverReport is what catches it.
//
// The remaining shapes (added, modified, deleted, symlink, submodule pointer,
// nested) are here to pin that nothing special was done for the two above at
// their expense.
func TestGitStagedPaths_CoversEveryChangeShape(t *testing.T) {
	t.Run("added file", func(t *testing.T) {
		r := newTestRepo(t)
		write(t, r, "brand-new.txt", "new")
		got := stagedPaths(t, r)
		if !contains(got, "brand-new.txt") {
			t.Fatalf("staged paths %q omit the new file, which the pending commit contains "+
				"— and a new file is the commonest shape of an undeclared change", got)
		}
	})

	t.Run("modified file", func(t *testing.T) {
		r := newTestRepo(t)
		write(t, r, "f.txt", "changed")
		if got := stagedPaths(t, r); !contains(got, "f.txt") {
			t.Fatalf("staged paths %q omit the modified file", got)
		}
	})

	t.Run("deleted file", func(t *testing.T) {
		r := newTestRepo(t)
		if err := os.Remove(filepath.Join(r.wt, "f.txt")); err != nil {
			t.Fatalf("remove: %v", err)
		}
		if got := stagedPaths(t, r); !contains(got, "f.txt") {
			t.Fatalf("staged paths %q omit the deleted file; deleting somebody else's "+
				"file is as much a change to it as editing it", got)
		}
	})

	t.Run("rename reports BOTH the source and the destination", func(t *testing.T) {
		r := newTestRepo(t)
		r.git(t, "mv", "f.txt", "renamed.txt")
		got := stagedPaths(t, r)
		if !contains(got, "renamed.txt") {
			t.Errorf("staged paths %q omit the rename destination", got)
		}
		// MUTANT: drop --no-renames from GitStagedPaths. This assertion goes red
		// and every other subtest here stays green — the discriminator between
		// "renames are handled" and "renames look handled".
		if !contains(got, "f.txt") {
			t.Errorf("staged paths %q omit the rename SOURCE. With rename detection on "+
				"(the default since git 2.9) --name-only prints only the destination, so "+
				"the commit deletes f.txt while the gate never asks for a lock on it", got)
		}
	})

	t.Run("non-ASCII path is not C-quoted", func(t *testing.T) {
		r := newTestRepo(t)
		const p = "文档/说明.md"
		if err := os.MkdirAll(filepath.Join(r.wt, "文档"), 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		write(t, r, p, "hi")
		got := stagedPaths(t, r)
		if !contains(got, p) {
			t.Fatalf("staged paths %q do not contain %q verbatim. core.quotePath is on by "+
				"default, so without -z git returns %q wrapped in quotes with the bytes "+
				"escaped, and that string matches no lock key that will ever exist", got, p, p)
		}
	})

	t.Run("path containing a space", func(t *testing.T) {
		r := newTestRepo(t)
		const p = "a file with spaces.txt"
		write(t, r, p, "hi")
		if got := stagedPaths(t, r); !contains(got, p) {
			t.Fatalf("staged paths %q do not contain %q", got, p)
		}
	})

	t.Run("symlink", func(t *testing.T) {
		r := newTestRepo(t)
		if err := os.Symlink("f.txt", filepath.Join(r.wt, "alias")); err != nil {
			t.Skipf("symlinks unavailable here: %v", err)
		}
		if got := stagedPaths(t, r); !contains(got, "alias") {
			t.Fatalf("staged paths %q omit the symlink", got)
		}
	})

	t.Run("submodule pointer", func(t *testing.T) {
		r := newTestRepo(t)
		// A gitlink written straight into the index. `git submodule add` would
		// need protocol.file.allow and a working .gitmodules, neither of which
		// this is testing: the property is that a mode-160000 entry appears as an
		// ordinary path, since a submodule bump is a change to the superproject
		// that another work item can be holding.
		r.git(t, "update-index", "--add", "--cacheinfo",
			"160000,"+r.head(t)+",vendored")
		got, err := GitStagedPaths(context.Background(), r.wt)
		if err != nil {
			t.Fatalf("GitStagedPaths: %v", err)
		}
		if !contains(got, "vendored") {
			t.Fatalf("staged paths %q omit the submodule pointer", got)
		}
	})

	t.Run("nested directory", func(t *testing.T) {
		r := newTestRepo(t)
		if err := os.MkdirAll(filepath.Join(r.wt, "a", "b"), 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		write(t, r, "a/b/deep.txt", "deep")
		if got := stagedPaths(t, r); !contains(got, "a/b/deep.txt") {
			t.Fatalf("staged paths %q omit the nested file", got)
		}
	})
}

// TestGitStagedPaths_TheDefaultDiffRangeWouldBeEmpty is the range guard, and it
// is the single most important test in this file.
//
// `git diff --name-only` with no revision compares the WORKTREE to the INDEX.
// Everything that consults this gate stages first, so at the moment the gate
// runs those two are identical and the default range returns nothing at all.
// A gate built on it does not merely under-report — it reports an empty change
// set on every commit, forever, while looking exactly like a gate that examined
// the commit and found nothing to do.
//
// So the two ranges are run side by side against one staged commit: the default
// must be EMPTY (that is the trap, demonstrated rather than described) and
// GitStagedPaths must not be.
func TestGitStagedPaths_TheDefaultDiffRangeWouldBeEmpty(t *testing.T) {
	r := newTestRepo(t)
	write(t, r, "staged-and-invisible.txt", "x")
	if err := GitStage(context.Background(), r.wt, nil); err != nil {
		t.Fatalf("GitStage: %v", err)
	}

	defaultRange, err := exec.Command("git", "-C", r.wt, "diff", "--name-only").Output()
	if err != nil {
		t.Fatalf("git diff --name-only: %v", err)
	}
	if strings.TrimSpace(string(defaultRange)) != "" {
		t.Fatalf("the default diff range returned %q after staging. This test's premise is "+
			"that it returns nothing — if git's behaviour changed, the WARNING in "+
			"GitStagedPaths' doc comment needs rewriting, not this assertion deleting",
			defaultRange)
	}

	got, err := GitStagedPaths(context.Background(), r.wt)
	if err != nil {
		t.Fatalf("GitStagedPaths: %v", err)
	}
	if !contains(got, "staged-and-invisible.txt") {
		t.Fatalf("GitStagedPaths returned %q. It is reading a range that goes empty once "+
			"the files are staged, which makes the whole lock gate a no-op", got)
	}
}

// TestGitStagedPaths_WorktreeVsHEADWouldOverReport is the negative control for
// the OTHER rejected range, and it exists because the reason first written down
// for rejecting that range was wrong.
//
// `git diff HEAD --name-only` was dismissed as "blind to untracked files". Git
// is; this call is not, because every caller stages first — so under that range
// the "added file" subtest above still passes and nothing in this file ever went
// red for it. What the range actually does is OVER-report: it compares the
// WORKTREE, so it also lists files that are dirty there and were never staged.
// Those are not in the pending commit, and each one is a lock this gate would
// demand — and take from whoever else wanted it — over a file the commit does
// not touch. Over-reporting is the quieter failure of the two, which is exactly
// why it needs the test: nothing goes unprotected, so nothing looks broken.
//
// One repository holds both shapes at once and both ranges are run against it.
func TestGitStagedPaths_WorktreeVsHEADWouldOverReport(t *testing.T) {
	r := newTestRepo(t)
	r.commit(t, "tracked.txt", "v1")

	write(t, r, "tracked.txt", "v2") // dirty in the worktree, deliberately NOT staged
	write(t, r, "staged.txt", "this one is in the commit")
	// `add --` the one file, not `add -A`: staging both is precisely the state
	// this test needs to avoid.
	r.git(t, "add", "--", "staged.txt")

	got, err := GitStagedPaths(context.Background(), r.wt)
	if err != nil {
		t.Fatalf("GitStagedPaths: %v", err)
	}
	if !contains(got, "staged.txt") {
		t.Fatalf("staged paths %q omit the file the pending commit actually contains", got)
	}
	if contains(got, "tracked.txt") {
		t.Fatalf("staged paths %q include tracked.txt, which is dirty in the worktree but not "+
			"staged. The commit will not contain it, so locking it takes a file away from "+
			"another attempt for nothing — this is the range's real defect", got)
	}

	// The rejected range, side by side, so the argument in GitStagedPaths' doc
	// comment is a measurement rather than a claim.
	out, err := exec.Command("git", "-C", r.wt, "diff", "HEAD", "--name-only").Output()
	if err != nil {
		t.Fatalf("git diff HEAD --name-only: %v", err)
	}
	rejected := strings.Fields(string(out))
	if !contains(rejected, "staged.txt") || !contains(rejected, "tracked.txt") {
		t.Fatalf("worktree-vs-HEAD returned %q. This test's premise is that it returns BOTH — "+
			"if git's behaviour changed, the rejection argued in GitStagedPaths' doc comment "+
			"needs rewriting, not this assertion deleting", rejected)
	}
}

// TestGitStagedPaths_UnbornHEADListsTheFirstCommit covers the one repository
// state where naming HEAD is an error rather than an answer.
func TestGitStagedPaths_UnbornHEADListsTheFirstCommit(t *testing.T) {
	dir := t.TempDir()
	for _, args := range [][]string{
		{"init", "-q", dir},
		{"-C", dir, "config", "user.email", "t@t.com"},
		{"-C", dir, "config", "user.name", "t"},
	} {
		if out, err := exec.Command("git", args...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "first.txt"), []byte("x"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := GitStage(context.Background(), dir, nil); err != nil {
		t.Fatalf("GitStage: %v", err)
	}

	got, err := GitStagedPaths(context.Background(), dir)
	if err != nil {
		t.Fatalf("GitStagedPaths on an unborn HEAD: %v", err)
	}
	if !contains(got, "first.txt") {
		t.Fatalf("staged paths %q on an unborn HEAD; the first commit in a repository "+
			"would go through the gate unexamined", got)
	}
}

// errRefused is the gate refusal used below. A distinct sentinel, so the
// assertions can prove the commit failed FOR THE GATE'S REASON rather than for
// any of the several other reasons a commit can fail.
var errRefused = errors.New("refused by the test gate")

// TestCommitGate_AbsentVersusPresent is this work item's negative control, in
// the form that survives.
//
// Both arms commit the identical out-of-scope file in an identical repository.
// The only difference is whether a gate is wired in. The "absent" arm is the
// behaviour every polyforge commit had before aihub#366, and it must still
// SUCCEED — if it did not, the "present" arm would prove nothing, because a
// blocked commit and a commit that was never possible look the same from here.
func TestCommitGate_AbsentVersusPresent(t *testing.T) {
	t.Run("absent: the commit goes through, unexamined", func(t *testing.T) {
		r := newTestRepo(t)
		before := r.head(t)
		write(t, r, "out-of-scope.txt", "nobody asked for a lock on me")

		sha, err := GitCommitGated(context.Background(), r.wt, "no gate", nil, nil)
		if err != nil {
			t.Fatalf("GitCommitGated with no gate: %v", err)
		}
		if sha == before {
			t.Fatal("HEAD did not move; the control arm did not actually commit")
		}
		files := r.git(t, "show", "--name-only", "--format=", "HEAD")
		if !strings.Contains(files, "out-of-scope.txt") {
			t.Fatalf("commit contains %q, not the out-of-scope file", files)
		}
	})

	t.Run("present: the same commit is refused and HEAD does not move", func(t *testing.T) {
		r := newTestRepo(t)
		before := r.head(t)
		write(t, r, "out-of-scope.txt", "nobody asked for a lock on me")

		var seen []string
		gate := func(_ context.Context, paths []string) error {
			seen = paths
			return errRefused
		}

		sha, err := GitCommitGated(context.Background(), r.wt, "gated", nil, gate)
		if !errors.Is(err, errRefused) {
			t.Fatalf("error = %v, want the gate's own refusal — a commit that fails for "+
				"another reason is not evidence the gate stopped it", err)
		}
		if sha != "" {
			t.Errorf("a refused commit returned sha %q", sha)
		}
		if got := r.head(t); got != before {
			t.Errorf("HEAD moved to %s despite the refusal; the gate is decoration", got)
		}
		if !contains(seen, "out-of-scope.txt") {
			t.Errorf("the gate was handed %q, which does not name the file being committed", seen)
		}
		// The staged index is the documented, accepted side effect. Asserted so
		// that it is a decision on record rather than something a reader has to
		// discover from a puzzled `git status`.
		staged, err := GitHasStagedChanges(context.Background(), r.wt)
		if err != nil || !staged {
			t.Errorf("staged=%v err=%v; a refused commit is documented to leave the index populated", staged, err)
		}
	})
}

// TestGitCommitGated_GateSeesExactlyWhatTheCommitContains pins the gate's input
// to the COMMIT, not to the worktree.
//
// With pf_commit(paths=[...]) the two differ: an unrelated dirty file is not
// going to be committed, and asking for a lock on it would block a commit over
// a file it does not touch. The reverse mistake is worse — a gate reading only
// `paths` would miss a directory pathspec's expansion — so both directions are
// asserted here.
func TestGitCommitGated_GateSeesExactlyWhatTheCommitContains(t *testing.T) {
	r := newTestRepo(t)
	if err := os.MkdirAll(filepath.Join(r.wt, "pkg"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	write(t, r, "pkg/one.txt", "1")
	write(t, r, "pkg/two.txt", "2")
	write(t, r, "untouched.txt", "not in this commit")

	var seen []string
	gate := func(_ context.Context, paths []string) error {
		seen = paths
		return nil
	}
	if _, err := GitCommitGated(context.Background(), r.wt, "only pkg", []string{"pkg"}, gate); err != nil {
		t.Fatalf("GitCommitGated: %v", err)
	}

	for _, want := range []string{"pkg/one.txt", "pkg/two.txt"} {
		if !contains(seen, want) {
			t.Errorf("the gate saw %q, missing %q — a directory pathspec expands to files, "+
				"and a gate that echoed the pathspec back would lock a directory name no "+
				"lock key ever uses", seen, want)
		}
	}
	if contains(seen, "untouched.txt") {
		t.Errorf("the gate saw %q, which includes a dirty file this commit does not contain; "+
			"locking it would block on a file the commit never touches", seen)
	}
}

// TestShip_GateRefusalStopsAtCommitStage is the pf_ship arm. Fusing three calls
// makes "what already happened" the thing a failure has to report, so a refusal
// must land on the commit stage with nothing pushed and no PR — not somewhere
// downstream that leaves the caller guessing.
func TestShip_GateRefusalStopsAtCommitStage(t *testing.T) {
	r := newTestRepo(t)
	createLog := filepath.Join(t.TempDir(), "create.log")
	fakeGH(t, `[]`, createLog)
	before := r.head(t)
	write(t, r, "out-of-scope.txt", "x")

	res, err := runShipGated(t, r, "wi_ShipGate", "feat: gated", nil,
		func(context.Context, []string) error { return errRefused })

	if !errors.Is(err, errRefused) {
		t.Fatalf("error = %v, want the gate's refusal", err)
	}
	if res == nil {
		t.Fatal("Ship returned a nil result alongside its error")
	}
	if res.Stage != StageCommit {
		t.Errorf("Stage = %q, want %q — the caller cannot tell how far the chain got", res.Stage, StageCommit)
	}
	if res.Committed || res.CommitSHA != "" {
		t.Errorf("Committed=%v CommitSHA=%q; a refused gate must not produce a commit", res.Committed, res.CommitSHA)
	}
	if !res.StagedUncommitted {
		t.Error("StagedUncommitted=false; the index IS populated, and this field is how the caller learns that")
	}
	if got := r.head(t); got != before {
		t.Errorf("HEAD moved to %s", got)
	}
	if got := r.remoteSHA(t, r.tBranch); got != "" {
		t.Errorf("origin/%s = %q; a refused commit must reach the remote in no form", r.tBranch, got)
	}
	if called, _ := createCalled(t, createLog); called {
		t.Error("gh pr create ran for a commit that was never made")
	}
}

// TestShip_GateNotConsultedWhenNothingIsStaged is the zero-overhead arm at the
// git layer: Ship's idempotent retry after a failed push stages nothing and
// creates no commit, so it changes no file and must not spend a gate call.
//
// Asserted with a gate that FAILS if it is called, rather than by counting
// calls: "the gate ran but harmlessly" and "the gate did not run" are different
// facts, and only the second is the contract.
func TestShip_GateNotConsultedWhenNothingIsStaged(t *testing.T) {
	r := newTestRepo(t)
	fakeGH(t, `[]`, filepath.Join(t.TempDir(), "create.log"))
	r.commit(t, "already.txt", "committed by an earlier call")

	called := false
	_, err := runShipGated(t, r, "wi_ShipNoStage", "feat: retry", nil,
		func(context.Context, []string) error {
			called = true
			return errRefused
		})
	if err != nil {
		t.Fatalf("Ship: %v", err)
	}
	if called {
		t.Error("the gate was consulted on a retry that staged nothing and committed nothing; " +
			"every such retry would pay a round-trip to be told the empty set is covered")
	}
}
