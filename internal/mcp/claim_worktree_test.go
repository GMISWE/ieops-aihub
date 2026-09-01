package mcp

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// End-to-end tests for the claim worktree/branch path (aihub#322).
//
// These drive real git against real repositories on disk rather than asserting
// on a name string, because the risk this change carries is not "the derivation
// is wrong" — that is covered in branchname_test.go — it is "the claim computes
// a name that is not where the work is". A unit test of the derivation cannot
// see that failure at all: both the old and the new code return a perfectly
// well-formed name, and only git knows one of them has no ref behind it, or has
// a ref with none of the work on it.
//
// NOTE ON MODE: addClaimWorktree takes no mode parameter. That is the fix for
// review finding 1, not an omission — see resolveClaimBranch. Which branch to
// attach to is decided from what exists in the clone, and the claims that most
// need the legacy branch (force_takeover, `/pf-work <slug>` without --resume, an
// omitted mode) all arrive as "fresh". With the parameter gone the mode-gated
// bug is not expressible, so these tests need not enumerate modes.
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

// wtPath is where the claim handler would put this repo's worktree. It is never
// created by the fixture: "the worktree directory is absent" is the precondition
// for every scenario here, because the handler's os.Stat reuse short-circuits
// before any of this code runs when the directory still exists.
func (r *claimRepo) wtPath() string { return filepath.Join(r.root, "pf.aihub-322", "aihub") }

// add runs the code under test with a plain background context.
func (r *claimRepo) add(t *testing.T, n claimBranchNames) error {
	t.Helper()
	if _, err := os.Stat(r.wtPath()); err == nil {
		t.Fatalf("precondition violated: %s already exists, so the claim handler would have reused it", r.wtPath())
	}
	return addClaimWorktree(context.Background(), r.src, r.wtPath(), n)
}

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

func assertFileNotInWorktree(t *testing.T, wt, name string) {
	t.Helper()
	if _, err := os.Stat(filepath.Join(wt, name)); err == nil {
		t.Errorf("%s is present in the worktree — it attached to a branch that is not this work item's", name)
	}
}

// TestAddClaimWorktree_AttachesToLegacyBranch is the compatibility test this
// change exists to satisfy.
//
// Every work item claimed before aihub#322 has a branch named
// polyforge/<ulid8>. Under the new scheme a claim computes
// polyforge/aihub-322-<goal>, which for those work items does not exist. When
// the worktree DIRECTORY also no longer exists — the handler's os.Stat
// early-return is what normally hides this — the branch lookup is reached with
// a name that has no ref behind it.
//
// MUTANT: drop the n.Legacy candidate from resolveClaimBranch's loop. Nothing
// then matches, addClaimWorktree falls through and CREATES
// polyforge/aihub-322-... from origin/main, and all three assertions below go
// red: the checked-out branch is the new name, legacy-work.txt (a commit that
// exists only on the legacy branch) is absent, and a new branch was created.
func TestAddClaimWorktree_AttachesToLegacyBranch(t *testing.T) {
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

	if err := r.add(t, names); err != nil {
		t.Fatalf("addClaimWorktree: %v", err)
	}

	if got := checkedOutBranch(t, r.wtPath()); got != legacy {
		t.Errorf("worktree is on %q, want the legacy branch %q — a pre-aihub#322 work item lost its branch", got, legacy)
	}
	assertFileInWorktree(t, r.wtPath(), "legacy-work.txt")
	if r.hasLocalBranch(t, names.Branch) {
		t.Errorf("created %q instead of attaching to the existing legacy branch", names.Branch)
	}
}

// TestAddClaimWorktree_LegacyWorkSurvivesAClaimThatIsNotAResume is review
// finding 1, reproduced and then locked.
//
// The first version of this change ran the whole branch lookup inside
// `if mode == "resume"`. That looks safe until you ask who sends "fresh":
//
//   - force_takeover (pf-work Mode D) — by definition applied to a work item
//     another agent already has a branch, and commits, for;
//   - `/pf-work <slug>` without --resume (Mode B), i.e. anyone picking a paused
//     work item back up and forgetting the flag;
//   - `mode` is an optional claim parameter, so an omitted one arrives as "".
//
// Under the old NAME-STABLE scheme all three were harmless: the name was always
// polyforge/<ulid8>, `worktree add -b` failed with "already exists", and the
// fallback attached to the prior agent's work. Under a goal-derived name the -b
// SUCCEEDS, and the takeover silently starts again from origin/main while the
// commits it was supposed to take over sit on the legacy branch. Measured
// population at the time: .repo/aihub alone had 87 local and 125 remote legacy
// polyforge/* branches, one of them 3 commits ahead of origin/main.
//
// MUTANT: re-introduce the mode parameter and wrap the resolveClaimBranch call
// in `if mode == "resume"`, passing "fresh" here. prior-agent-work.txt then
// disappears from the worktree and the branch assertion fails — which is
// exactly what the probe reported before the fix.
func TestAddClaimWorktree_LegacyWorkSurvivesAClaimThatIsNotAResume(t *testing.T) {
	r := newClaimRepo(t)
	const legacy = "polyforge/ItxdoyAT"
	r.branchWithMarker(t, legacy, "prior-agent-work.txt")

	// What a force_takeover of that work item computes today.
	names := newClaimBranchNames("aihub", "322", "readable task branch names", "ItxdoyAT")

	if err := r.add(t, names); err != nil {
		t.Fatalf("addClaimWorktree: %v", err)
	}

	if got := checkedOutBranch(t, r.wtPath()); got != legacy {
		t.Errorf("worktree is on %q, want %q — a takeover started over instead of taking over", got, legacy)
	}
	// The load-bearing assertion: the HISTORY, not the name. A branch created
	// off origin/main would carry the right-looking name in some variants of
	// this bug but never this file.
	assertFileInWorktree(t, r.wtPath(), "prior-agent-work.txt")
}

// TestAddClaimWorktree_PrefersNewFormatBranch: when both names exist the current
// one wins, so the legacy tier is a fallback and not a hijack.
func TestAddClaimWorktree_PrefersNewFormatBranch(t *testing.T) {
	r := newClaimRepo(t)
	names := newClaimBranchNames("aihub", "322", "readable task branch names", "SosL0kmU")
	r.branchWithMarker(t, names.Legacy, "legacy-work.txt")
	r.branchWithMarker(t, names.Branch, "current-work.txt")

	if err := r.add(t, names); err != nil {
		t.Fatalf("addClaimWorktree: %v", err)
	}
	if got := checkedOutBranch(t, r.wtPath()); got != names.Branch {
		t.Errorf("worktree is on %q, want the current name %q", got, names.Branch)
	}
	assertFileInWorktree(t, r.wtPath(), "current-work.txt")
}

// TestAddClaimWorktree_MatchesStemWhenGoalChanged covers the hazard the new
// scheme introduces on its own: the branch name embeds the GOAL, and the goal is
// mutable (pf_update_work_item takes one). Claim under goal A, edit the goal to
// B, delete the worktree directory, claim again — the computed name is now B's
// and A's branch would be orphaned. polyforge/<project>-<seq> identifies the
// work item independently of its goal, so a unique match under that stem is
// unambiguously the right branch.
func TestAddClaimWorktree_MatchesStemWhenGoalChanged(t *testing.T) {
	r := newClaimRepo(t)
	atClaim := newClaimBranchNames("aihub", "322", "the original goal", "SosL0kmU")
	r.branchWithMarker(t, atClaim.Branch, "original-work.txt")

	later := newClaimBranchNames("aihub", "322", "a completely rewritten goal", "SosL0kmU")
	if later.Branch == atClaim.Branch {
		t.Fatalf("the fixture is not exercising anything: both goals derive %q", atClaim.Branch)
	}

	if err := r.add(t, later); err != nil {
		t.Fatalf("addClaimWorktree: %v", err)
	}
	if got := checkedOutBranch(t, r.wtPath()); got != atClaim.Branch {
		t.Errorf("worktree is on %q, want the branch the claim actually created, %q", got, atClaim.Branch)
	}
	assertFileInWorktree(t, r.wtPath(), "original-work.txt")
}

// TestAddClaimWorktree_AttachesToABareStemBranch is review finding 3.1: the
// glob cannot match the name the scheme itself produces.
//
// Degradation row 1 is "the goal has no [a-z0-9] → polyforge/<project>-<seq>",
// and goals in this workspace are routinely Chinese, so the bare stem is a
// common name, not an exotic one — polyforge/aihub-21, -29, -47, -55, -58 and
// polyforge/ieops-210, -390, -549, -577 all exist in the live workspace today.
// Add any latin word to such a work item's goal and:
//
//	tier 1 polyforge/aihub-322-gateway-timeout-fix  → miss (new name)
//	tier 3 polyforge/<ulid8>                        → miss (never had one)
//	tier 4 polyforge/aihub-322-*                    → DOES NOT MATCH polyforge/aihub-322
//
// so the claim created a virgin branch and abandoned the commits. The mirror
// direction, desc → bare stem, always worked, which is exactly why
// MatchesStemWhenGoalChanged stayed green over this: it only exercises
// desc → desc.
//
// MUTANT: drop n.Stem from the exact-candidate loop in resolveClaimBranch. The
// branch assertion and the marker assertion both go red.
func TestAddClaimWorktree_AttachesToABareStemBranch(t *testing.T) {
	r := newClaimRepo(t)

	// What the first claim produced, under a Chinese-only goal.
	atClaim := newClaimBranchNames("aihub", "322", "修复网关超时", "SosL0kmU")
	if atClaim.Branch != "polyforge/aihub-322" {
		t.Fatalf("fixture: a Chinese-only goal derived %q, want the bare stem polyforge/aihub-322", atClaim.Branch)
	}
	r.branchWithMarker(t, atClaim.Branch, "original-work.txt")

	// The goal is edited to add latin text; the name moves off the bare stem.
	later := newClaimBranchNames("aihub", "322", "gateway timeout fix", "SosL0kmU")
	if later.Branch == atClaim.Branch {
		t.Fatalf("fixture is not exercising anything: both goals derive %q", atClaim.Branch)
	}
	// State the glob's blind spot as an assertion rather than as a claim in a
	// comment: if git ever made "<stem>-*" match the bare stem, this test would
	// pass for a reason that has nothing to do with the fix.
	if m := gitUniqueBranchMatch(r.src, "refs/heads/", later.Stem+"-*"); m != "" {
		t.Fatalf("the glob %q matched %q — this test no longer isolates the exact-candidate tier", later.Stem+"-*", m)
	}

	if err := r.add(t, later); err != nil {
		t.Fatalf("addClaimWorktree: %v", err)
	}
	if got := checkedOutBranch(t, r.wtPath()); got != atClaim.Branch {
		t.Errorf("worktree is on %q, want the bare-stem branch %q the first claim created", got, atClaim.Branch)
	}
	assertFileInWorktree(t, r.wtPath(), "original-work.txt")
}

// TestAddClaimWorktree_RemoteUniqueStemMatchAttachesLocally is review finding
// 3.2, which was a data-availability bug rather than a data-loss one: the repo
// ended up with NO worktree.
//
// Two local branches under the stem make the local glob decline, correctly. The
// remote glob then finds one — the pushed one, which is evidence about which of
// the two holds the work, not a guess — and the old code returned it flagged
// "from remote". `worktree add -b` on a branch that also exists locally fails
// with "already exists", that early return had no fallback (unlike the create
// path twenty lines below), and the handler logged one stderr line and moved on.
//
// The fix drops the flag entirely: attachWorktree asks refs/heads at the moment
// it matters. MUTANT: reinstate the flag, i.e. have this path run
// `worktree add -b <m> <wt> origin/<m>` unconditionally — addClaimWorktree then
// returns an "already exists" error and no worktree is created, so both the
// error check and the branch assertion go red.
func TestAddClaimWorktree_RemoteUniqueStemMatchAttachesLocally(t *testing.T) {
	r := newClaimRepo(t)
	const pushed = "polyforge/aihub-322-first-goal"
	r.branchWithMarker(t, pushed, "first.txt")
	r.branchWithMarker(t, "polyforge/aihub-322-second-goal", "second.txt")
	mustGit(t, r.src, "push", "-q", "origin", pushed)

	// A ulid8 with no branch anywhere, so the legacy tier cannot rescue this.
	names := newClaimBranchNames("aihub", "322", "a third goal entirely", "ZZZZZZZZ")
	if r.hasLocalBranch(t, names.Legacy) || r.hasLocalBranch(t, names.Stem) {
		t.Fatal("fixture: an exact candidate exists, so the glob tier is not what is under test")
	}

	if err := r.add(t, names); err != nil {
		t.Fatalf("addClaimWorktree: %v — a remote-unique match must attach to the local head, not fail the repo out of a worktree", err)
	}
	if got := checkedOutBranch(t, r.wtPath()); got != pushed {
		t.Errorf("worktree is on %q, want the one branch of the two that was pushed, %q", got, pushed)
	}
	assertFileInWorktree(t, r.wtPath(), "first.txt")
}

// TestAddClaimWorktree_IgnoresAmbiguousStemMatches: two branches under the same
// stem mean the goal was edited twice, and picking either would be a guess. The
// stem tier must decline; the legacy tier still applies.
func TestAddClaimWorktree_IgnoresAmbiguousStemMatches(t *testing.T) {
	r := newClaimRepo(t)
	r.branchWithMarker(t, "polyforge/aihub-322-first-goal", "first.txt")
	r.branchWithMarker(t, "polyforge/aihub-322-second-goal", "second.txt")
	r.branchWithMarker(t, "polyforge/SosL0kmU", "legacy-work.txt")

	names := newClaimBranchNames("aihub", "322", "a third goal entirely", "SosL0kmU")
	if err := r.add(t, names); err != nil {
		t.Fatalf("addClaimWorktree: %v", err)
	}
	if got := checkedOutBranch(t, r.wtPath()); got != names.Legacy {
		t.Errorf("worktree is on %q; with two stem matches the shim must fall to the unambiguous legacy branch %q", got, names.Legacy)
	}
}

// TestAddClaimWorktree_HalfStemDoesNotGlobTheWholeProject is review finding 2.
//
// The stem was built with strings.Trim(project+"-"+seq, "-"), so a seq that
// reduced to nothing collapsed the stem to the project alone: Stem
// "polyforge/aihub" globbing "polyforge/aihub-*", i.e. EVERY branch in the
// project. Reproduced before the fix: the claim landed on
// polyforge/aihub-999-someone-elses-work-item — silently resuming onto a branch
// belonging to a different work item, which is strictly worse than not finding
// one. The fix populates Stem only when both components survive.
//
// MUTANT: restore the Trim-join in newClaimBranchNames. Stem becomes
// "polyforge/aihub", the glob matches the single foreign branch below, and both
// assertions go red.
func TestAddClaimWorktree_HalfStemDoesNotGlobTheWholeProject(t *testing.T) {
	r := newClaimRepo(t)
	const foreign = "polyforge/aihub-999-someone-elses-work-item"
	r.branchWithMarker(t, foreign, "not-mine.txt")

	// A slug whose seq carries no [a-z0-9] at all.
	names := newClaimBranchNames("aihub", "###", "some goal", "SosL0kmU")
	// Errorf, not Fatalf: the point of this test is the ATTACH behaviour below,
	// and a Fatal here would stop before it — reporting the cause while never
	// demonstrating the symptom the finding is about.
	if names.Stem != "" {
		t.Errorf("Stem = %q with an unusable seq; a half stem is a set, not an identity", names.Stem)
	}

	if err := r.add(t, names); err != nil {
		t.Fatalf("addClaimWorktree: %v", err)
	}
	if got := checkedOutBranch(t, r.wtPath()); got == foreign {
		t.Errorf("attached to %q, which belongs to a different work item", foreign)
	}
	assertFileNotInWorktree(t, r.wtPath(), "not-mine.txt")
}

// TestAddClaimWorktree_HalfStemDoesNotGlobBySeqAlone is review finding 3 — the
// same root cause reached through the other component.
//
// A non-ASCII project name (branchname_test.go explicitly blesses one) reduced
// the stem to "polyforge/528", whose glob matched the hand-made
// polyforge/528-stagesconfig-wiring. That branch shape is not hypothetical: it
// is the ieops-datachain convention this change was modelled on.
func TestAddClaimWorktree_HalfStemDoesNotGlobBySeqAlone(t *testing.T) {
	r := newClaimRepo(t)
	const handMade = "polyforge/528-stagesconfig-wiring"
	r.branchWithMarker(t, handMade, "someone-elses-stages.txt")

	names := newClaimBranchNames("映坊", "528", "把配置接起来", "SosL0kmU")
	if names.Stem != "" {
		t.Errorf("Stem = %q with an unusable project; a half stem is a set, not an identity", names.Stem)
	}
	if names.Branch != "polyforge/528" {
		t.Fatalf("Branch = %q, want polyforge/528 — the surviving component is still used as an exact NAME", names.Branch)
	}

	if err := r.add(t, names); err != nil {
		t.Fatalf("addClaimWorktree: %v", err)
	}
	if got := checkedOutBranch(t, r.wtPath()); got != "polyforge/528" {
		t.Errorf("worktree is on %q, want a freshly created polyforge/528", got)
	}
	assertFileNotInWorktree(t, r.wtPath(), "someone-elses-stages.txt")
}

// TestAddClaimWorktree_AttachesToRemoteOnlyBranch: the local head is gone (a
// cleanup pass, a re-clone) but origin still has the work. Re-creating the
// branch from origin/main would orphan every pushed commit, so the branch is
// materialised from origin/<branch> instead.
func TestAddClaimWorktree_AttachesToRemoteOnlyBranch(t *testing.T) {
	r := newClaimRepo(t)
	names := newClaimBranchNames("aihub", "322", "readable task branch names", "SosL0kmU")
	r.branchWithMarker(t, names.Branch, "pushed-work.txt")
	mustGit(t, r.src, "push", "-q", "origin", names.Branch)
	mustGit(t, r.src, "branch", "-q", "-D", names.Branch)
	if r.hasLocalBranch(t, names.Branch) {
		t.Fatalf("precondition violated: %q is still a local head", names.Branch)
	}

	if err := r.add(t, names); err != nil {
		t.Fatalf("addClaimWorktree: %v", err)
	}
	if got := checkedOutBranch(t, r.wtPath()); got != names.Branch {
		t.Errorf("worktree is on %q, want %q", got, names.Branch)
	}
	assertFileInWorktree(t, r.wtPath(), "pushed-work.txt")
}

// TestAddClaimWorktree_CreatesWhenNothingExists: before this change the resume
// path unconditionally ran `worktree add <path> <branch>`, so a work item whose
// branch had been deleted got a git error and NO worktree. Creating is strictly
// better, and is the "only create a branch if nothing exists" behaviour.
func TestAddClaimWorktree_CreatesWhenNothingExists(t *testing.T) {
	r := newClaimRepo(t)
	names := newClaimBranchNames("aihub", "322", "readable task branch names", "SosL0kmU")

	if err := r.add(t, names); err != nil {
		t.Fatalf("addClaimWorktree: %v", err)
	}
	if got := checkedOutBranch(t, r.wtPath()); got != names.Branch {
		t.Errorf("worktree is on %q, want a freshly created %q", got, names.Branch)
	}
	assertFileInWorktree(t, r.wtPath(), "main.txt")
}

// TestAddClaimWorktree_CreatesReadableBranchForANewWorkItem is the headline
// behaviour: a work item with no branch anywhere gets a name a human can read
// off `git branch -r`.
func TestAddClaimWorktree_CreatesReadableBranchForANewWorkItem(t *testing.T) {
	r := newClaimRepo(t)
	names := newClaimBranchNames("aihub", "322", "readable task branch names", "SosL0kmU")

	if err := r.add(t, names); err != nil {
		t.Fatalf("addClaimWorktree: %v", err)
	}
	if got := checkedOutBranch(t, r.wtPath()); got != "polyforge/aihub-322-readable-task-branch-names" {
		t.Errorf("created %q, want polyforge/aihub-322-readable-task-branch-names", got)
	}
	if r.hasLocalBranch(t, names.Legacy) {
		t.Errorf("created the legacy name %q as well", names.Legacy)
	}
}

// TestAddClaimWorktree_AttachesWhenBranchAlreadyExists preserves the idempotent
// retry behaviour: a claim replayed after the branch was created but before the
// worktree was recorded must attach, not fail.
func TestAddClaimWorktree_AttachesWhenBranchAlreadyExists(t *testing.T) {
	r := newClaimRepo(t)
	names := newClaimBranchNames("aihub", "322", "readable task branch names", "SosL0kmU")
	r.branchWithMarker(t, names.Branch, "earlier-work.txt")

	if err := r.add(t, names); err != nil {
		t.Fatalf("addClaimWorktree: %v", err)
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
	if err := addClaimWorktree(context.Background(), r.src, r.wtPath(), claimBranchNames{}); err == nil {
		t.Fatal("addClaimWorktree accepted an empty branch name")
	}
	if _, err := os.Stat(r.wtPath()); err == nil {
		t.Error("a worktree was created despite the empty branch name")
	}
}

// TestAddClaimWorktree_FetchIsBounded locks the timeout the review asked for,
// against a remote that really does hang.
//
// `git fetch` over git:// connects, sends its request, and then blocks reading
// the ref advertisement with no timeout of its own. The listener below accepts
// and never writes, which is exactly the wedged-peer shape; before the fix this
// call never returned, inside an MCP request, for a step whose failure is
// already non-fatal (aihub#316 was the same shape).
//
// Asserting on ELAPSED TIME and not merely on "an error came back" is the point:
// a fetch that fails instantly for an unrelated reason — bad URL, no such repo —
// would satisfy an error-only assertion while proving nothing about the bound.
// The lower bound catches that; the upper bound catches the hang.
//
// MUTANT 1: drop the ctx from the fetch (exec.Command instead of
// exec.CommandContext). The call blocks and the test times out.
// MUTANT 2: use context.Background() instead of the derived fetchCtx. Same.
func TestAddClaimWorktree_FetchIsBounded(t *testing.T) {
	r := newClaimRepo(t)

	// A TCP peer that accepts and then says nothing, ever.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	accepted := make(chan net.Conn, 4)
	go func() {
		defer close(accepted)
		for {
			c, aErr := ln.Accept()
			if aErr != nil { // listener closed by the cleanup below
				return
			}
			accepted <- c // held open, never written to, never answered
		}
	}()
	t.Cleanup(func() {
		_ = ln.Close()
		for c := range accepted { // drains after the goroutine closes it
			_ = c.Close()
		}
	})

	mustGit(t, r.src, "remote", "set-url", "origin",
		fmt.Sprintf("git://127.0.0.1:%d/hangs", ln.Addr().(*net.TCPAddr).Port))

	prev := claimFetchTimeout
	claimFetchTimeout = 750 * time.Millisecond
	t.Cleanup(func() { claimFetchTimeout = prev })

	// Names that match nothing, so the create path — the only one that fetches —
	// is the one taken.
	names := newClaimBranchNames("aihub", "322", "readable task branch names", "SosL0kmU")

	// Run it off the test goroutine and race it against a timer. Calling it
	// inline would make the failure mode of this test a HANG: the package would
	// sit until go test's own 10-minute panic, and in this repo a hang gets
	// attributed to a broken harness rather than to the bug it just caught. A
	// mutant has to produce a FAIL, promptly, with a sentence saying what broke.
	const hangBudget = 20 * time.Second
	type outcome struct {
		err     error
		elapsed time.Duration
	}
	done := make(chan outcome, 1) // buffered: the goroutine must not block if we gave up
	start := time.Now()
	go func() {
		e := addClaimWorktree(context.Background(), r.src, r.wtPath(), names)
		done <- outcome{e, time.Since(start)}
	}()

	var got outcome
	select {
	case got = <-done:
	case <-time.After(hangBudget):
		t.Fatalf("addClaimWorktree had not returned after %v against a %v fetch timeout — the fetch is not bounded",
			hangBudget, claimFetchTimeout)
	}

	addErr, elapsed := got.err, got.elapsed
	if elapsed < claimFetchTimeout {
		t.Errorf("returned in %v, before the %v timeout could have fired — the fetch failed for some other reason and this test proves nothing about the bound",
			elapsed, claimFetchTimeout)
	}
	// The fetch failing is non-fatal: the worktree must still be created, off the
	// stale local origin/main, rather than the claim losing its worktree because
	// the network was down.
	if addErr != nil {
		t.Fatalf("a failed fetch must not fail the whole worktree creation: %v", addErr)
	}
	if got := checkedOutBranch(t, r.wtPath()); got != names.Branch {
		t.Errorf("worktree is on %q, want %q", got, names.Branch)
	}
}
