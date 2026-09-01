package mcp

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// End-to-end tests for the claim worktree/branch path (aihub#322).
//
// These drive real git against real repositories on disk rather than asserting
// on a name string, because the risk this change carries is not "the derivation
// is wrong" — that is covered in branchname_test.go — it is "resume computes a
// name that does not exist as a branch". A unit test of the derivation cannot
// see that failure at all: both the old and the new code return a perfectly
// well-formed name, and only git knows one of them has no ref behind it.
//
// ISOLATION: every test sets POLYFORGE_WORKSPACE_ROOT to its own t.TempDir().
// config.StateDir() is WorkspaceRoot()/.polyforge/state and WorkspaceRoot()
// otherwise walks up for .polyforge.yaml — which, from a checkout inside the
// live workspace, resolves to the real state directory holding every claimed
// work item's credentials. Nothing here writes state files today, but the env
// var makes that structurally impossible rather than merely currently true.

type claimRepo struct {
	root string // stands in for the workspace root
	bare string // the "origin" remote
	src  string // the .repo/<name> clone that worktrees are added from
}

func newClaimRepo(t *testing.T) *claimRepo {
	t.Helper()
	root := t.TempDir()
	t.Setenv("POLYFORGE_WORKSPACE_ROOT", root)

	r := &claimRepo{
		root: root,
		bare: filepath.Join(root, "origin.git"),
		src:  filepath.Join(root, ".repo", "aihub"),
	}
	mustGit(t, "", "init", "--bare", "-q", "-b", "main", r.bare)
	mustGit(t, "", "clone", "-q", r.bare, r.src)
	mustGit(t, r.src, "config", "user.email", "t@t.test")
	mustGit(t, r.src, "config", "user.name", "t")
	r.commit(t, "main.txt", "base")
	mustGit(t, r.src, "push", "-q", "-u", "origin", "main")
	return r
}

// commit writes name in the src worktree and commits it on the current branch.
func (r *claimRepo) commit(t *testing.T, name, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(r.src, name), []byte(body), 0644); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
	mustGit(t, r.src, "add", name)
	mustGit(t, r.src, "commit", "-q", "-m", "add "+name)
}

// branchWithMarker creates branch off main carrying a file unique to it, then
// returns src to main. The marker is what proves an attach landed on THAT
// branch's history rather than merely on a branch with the right name.
func (r *claimRepo) branchWithMarker(t *testing.T, branch, marker string) {
	t.Helper()
	mustGit(t, r.src, "checkout", "-q", "-b", branch, "main")
	r.commit(t, marker, marker)
	mustGit(t, r.src, "checkout", "-q", "main")
}

func (r *claimRepo) hasLocalBranch(t *testing.T, branch string) bool {
	t.Helper()
	return exec.Command("git", "-C", r.src, "show-ref", "--verify", "--quiet", "refs/heads/"+branch).Run() == nil
}

// wtPath is where the claim handler would put this repo's worktree.
func (r *claimRepo) wtPath() string { return filepath.Join(r.root, "pf.aihub-322", "aihub") }

func mustGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	if dir != "" {
		args = append([]string{"-C", dir}, args...)
	}
	out, err := exec.Command("git", args...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

// checkedOutBranch is the branch the worktree at wt has checked out.
func checkedOutBranch(t *testing.T, wt string) string {
	t.Helper()
	return mustGit(t, wt, "rev-parse", "--abbrev-ref", "HEAD")
}

func assertFileInWorktree(t *testing.T, wt, name string) {
	t.Helper()
	if _, err := os.Stat(filepath.Join(wt, name)); err != nil {
		t.Errorf("%s is missing from the worktree — it attached to the wrong history: %v", name, err)
	}
}

// TestAddClaimWorktree_ResumeAttachesToLegacyBranch is the compatibility test
// this change exists to satisfy.
//
// Every work item claimed before aihub#322 has a branch named
// polyforge/<ulid8>. Under the new scheme resume computes
// polyforge/aihub-322-<goal>, which for those work items does not exist. When
// the worktree DIRECTORY also no longer exists — the handler's os.Stat
// early-return is what normally hides this, so deleting the directory is the
// whole point of the scenario — the resume path is reached with a name that has
// no ref behind it.
//
// MUTANT: drop the n.Legacy candidate from resolveClaimBranch's loop. Nothing
// then matches, addClaimWorktree falls through and CREATES
// polyforge/aihub-322-... from origin/main, and both assertions below go red:
// the checked-out branch is the new name, and legacy-work.txt — the commit that
// only exists on the legacy branch — is absent from the worktree.
func TestAddClaimWorktree_ResumeAttachesToLegacyBranch(t *testing.T) {
	r := newClaimRepo(t)
	const legacy = "polyforge/SosL0kmU"
	r.branchWithMarker(t, legacy, "legacy-work.txt")

	names := newClaimBranchNames("aihub", "322", "readable task branch names", "SosL0kmU")
	if names.Branch == legacy {
		t.Fatalf("the fixture is not exercising anything: the new name equals the legacy name (%q)", legacy)
	}
	if r.hasLocalBranch(t, names.Branch) {
		t.Fatalf("precondition violated: %q already exists", names.Branch)
	}

	if err := addClaimWorktree(r.src, r.wtPath(), names, "resume"); err != nil {
		t.Fatalf("addClaimWorktree(resume): %v", err)
	}

	if got := checkedOutBranch(t, r.wtPath()); got != legacy {
		t.Errorf("worktree is on %q, want the legacy branch %q — a pre-aihub#322 work item lost its branch on resume", got, legacy)
	}
	assertFileInWorktree(t, r.wtPath(), "legacy-work.txt")
	if r.hasLocalBranch(t, names.Branch) {
		t.Errorf("resume created %q instead of attaching to the existing legacy branch", names.Branch)
	}
}

// TestAddClaimWorktree_ResumePrefersNewFormatBranch: when both names exist the
// current one wins, so the legacy tier is a fallback and not a hijack.
func TestAddClaimWorktree_ResumePrefersNewFormatBranch(t *testing.T) {
	r := newClaimRepo(t)
	names := newClaimBranchNames("aihub", "322", "readable task branch names", "SosL0kmU")
	r.branchWithMarker(t, names.Legacy, "legacy-work.txt")
	r.branchWithMarker(t, names.Branch, "current-work.txt")

	if err := addClaimWorktree(r.src, r.wtPath(), names, "resume"); err != nil {
		t.Fatalf("addClaimWorktree(resume): %v", err)
	}
	if got := checkedOutBranch(t, r.wtPath()); got != names.Branch {
		t.Errorf("worktree is on %q, want the current name %q", got, names.Branch)
	}
	assertFileInWorktree(t, r.wtPath(), "current-work.txt")
}

// TestAddClaimWorktree_ResumeMatchesStemWhenGoalChanged covers the hazard the
// new scheme introduces on its own: the branch name embeds the GOAL, and the
// goal is mutable (pf_update_work_item takes one). Claim under goal A, edit the
// goal to B, delete the worktree directory, resume — the computed name is now
// B's and A's branch would be orphaned. polyforge/<project>-<seq> identifies the
// work item independently of its goal, so a unique match under that stem is
// unambiguously the right branch.
func TestAddClaimWorktree_ResumeMatchesStemWhenGoalChanged(t *testing.T) {
	r := newClaimRepo(t)
	atClaim := newClaimBranchNames("aihub", "322", "the original goal", "SosL0kmU")
	r.branchWithMarker(t, atClaim.Branch, "original-work.txt")

	atResume := newClaimBranchNames("aihub", "322", "a completely rewritten goal", "SosL0kmU")
	if atResume.Branch == atClaim.Branch {
		t.Fatalf("the fixture is not exercising anything: both goals derive %q", atClaim.Branch)
	}

	if err := addClaimWorktree(r.src, r.wtPath(), atResume, "resume"); err != nil {
		t.Fatalf("addClaimWorktree(resume): %v", err)
	}
	if got := checkedOutBranch(t, r.wtPath()); got != atClaim.Branch {
		t.Errorf("worktree is on %q, want the branch the claim actually created, %q", got, atClaim.Branch)
	}
	assertFileInWorktree(t, r.wtPath(), "original-work.txt")
}

// TestAddClaimWorktree_ResumeIgnoresAmbiguousStemMatches: two branches under the
// same stem mean the goal was edited twice, and picking either would be a guess.
// The stem tier must decline; the legacy tier still applies.
func TestAddClaimWorktree_ResumeIgnoresAmbiguousStemMatches(t *testing.T) {
	r := newClaimRepo(t)
	r.branchWithMarker(t, "polyforge/aihub-322-first-goal", "first.txt")
	r.branchWithMarker(t, "polyforge/aihub-322-second-goal", "second.txt")
	r.branchWithMarker(t, "polyforge/SosL0kmU", "legacy-work.txt")

	names := newClaimBranchNames("aihub", "322", "a third goal entirely", "SosL0kmU")
	if err := addClaimWorktree(r.src, r.wtPath(), names, "resume"); err != nil {
		t.Fatalf("addClaimWorktree(resume): %v", err)
	}
	if got := checkedOutBranch(t, r.wtPath()); got != names.Legacy {
		t.Errorf("worktree is on %q; with two stem matches the shim must fall to the unambiguous legacy branch %q", got, names.Legacy)
	}
}

// TestAddClaimWorktree_ResumeAttachesToRemoteOnlyBranch: the local head is gone
// (a cleanup pass, a re-clone) but origin still has the work. Re-creating the
// branch from origin/main would orphan every pushed commit, so the branch is
// materialised from origin/<branch> instead.
func TestAddClaimWorktree_ResumeAttachesToRemoteOnlyBranch(t *testing.T) {
	r := newClaimRepo(t)
	names := newClaimBranchNames("aihub", "322", "readable task branch names", "SosL0kmU")
	r.branchWithMarker(t, names.Branch, "pushed-work.txt")
	mustGit(t, r.src, "push", "-q", "origin", names.Branch)
	mustGit(t, r.src, "branch", "-q", "-D", names.Branch)
	if r.hasLocalBranch(t, names.Branch) {
		t.Fatalf("precondition violated: %q is still a local head", names.Branch)
	}

	if err := addClaimWorktree(r.src, r.wtPath(), names, "resume"); err != nil {
		t.Fatalf("addClaimWorktree(resume): %v", err)
	}
	if got := checkedOutBranch(t, r.wtPath()); got != names.Branch {
		t.Errorf("worktree is on %q, want %q", got, names.Branch)
	}
	assertFileInWorktree(t, r.wtPath(), "pushed-work.txt")
}

// TestAddClaimWorktree_ResumeCreatesWhenNothingExists: before this change the
// resume path unconditionally ran `worktree add <path> <branch>`, so a work item
// whose branch had been deleted got a git error and NO worktree. Creating is
// strictly better than that, and is the documented "only create a branch if
// neither exists" behaviour.
func TestAddClaimWorktree_ResumeCreatesWhenNothingExists(t *testing.T) {
	r := newClaimRepo(t)
	names := newClaimBranchNames("aihub", "322", "readable task branch names", "SosL0kmU")

	if err := addClaimWorktree(r.src, r.wtPath(), names, "resume"); err != nil {
		t.Fatalf("addClaimWorktree(resume): %v", err)
	}
	if got := checkedOutBranch(t, r.wtPath()); got != names.Branch {
		t.Errorf("worktree is on %q, want a freshly created %q", got, names.Branch)
	}
	assertFileInWorktree(t, r.wtPath(), "main.txt")
}

// TestAddClaimWorktree_FreshCreatesReadableBranch is the headline behaviour: a
// new claim gets a name a human can read off `git branch -r`.
func TestAddClaimWorktree_FreshCreatesReadableBranch(t *testing.T) {
	r := newClaimRepo(t)
	names := newClaimBranchNames("aihub", "322", "readable task branch names", "SosL0kmU")

	if err := addClaimWorktree(r.src, r.wtPath(), names, "fresh"); err != nil {
		t.Fatalf("addClaimWorktree(fresh): %v", err)
	}
	if got := checkedOutBranch(t, r.wtPath()); got != "polyforge/aihub-322-readable-task-branch-names" {
		t.Errorf("fresh claim created %q, want polyforge/aihub-322-readable-task-branch-names", got)
	}
	if r.hasLocalBranch(t, names.Legacy) {
		t.Errorf("fresh claim created the legacy name %q as well", names.Legacy)
	}
}

// TestAddClaimWorktree_FreshAttachesWhenBranchExists preserves the idempotent
// retry behaviour: a claim replayed after the branch was created but before the
// worktree was recorded must attach, not fail.
func TestAddClaimWorktree_FreshAttachesWhenBranchExists(t *testing.T) {
	r := newClaimRepo(t)
	names := newClaimBranchNames("aihub", "322", "readable task branch names", "SosL0kmU")
	r.branchWithMarker(t, names.Branch, "earlier-work.txt")

	if err := addClaimWorktree(r.src, r.wtPath(), names, "fresh"); err != nil {
		t.Fatalf("addClaimWorktree(fresh, branch exists): %v", err)
	}
	if got := checkedOutBranch(t, r.wtPath()); got != names.Branch {
		t.Errorf("worktree is on %q, want %q", got, names.Branch)
	}
	assertFileInWorktree(t, r.wtPath(), "earlier-work.txt")
}

// TestAddClaimWorktree_EmptyBranchIsRefused: "" is the caller's skip signal, and
// handing it to git would produce a worktree on a detached or defaulted branch.
func TestAddClaimWorktree_EmptyBranchIsRefused(t *testing.T) {
	r := newClaimRepo(t)
	if err := addClaimWorktree(r.src, r.wtPath(), claimBranchNames{}, "fresh"); err == nil {
		t.Fatal("addClaimWorktree accepted an empty branch name")
	}
	if _, err := os.Stat(r.wtPath()); err == nil {
		t.Error("a worktree was created despite the empty branch name")
	}
}
