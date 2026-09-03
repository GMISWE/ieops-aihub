package coding

import (
	"context"
	"os/exec"
	"strings"
	"testing"
)

// aihub#257, producer end: GitPush must leave the task branch tracking its own
// remote branch, so that the bare `git push` a human types in that worktree has
// a correct destination under every push.default rather than a dangerous one.

func upstreamShort(t *testing.T, r *testRepo, branch string) string {
	t.Helper()
	return r.git(t, "for-each-ref", "--format=%(upstream:short)", "refs/heads/"+branch)
}

// bareDryRunTargets returns the destination refs a bare `git push` in r.wt would
// write. Push failure is an expected outcome, not a test error.
func bareDryRunTargets(t *testing.T, r *testRepo) ([]string, string) {
	t.Helper()
	cmd := exec.Command("git", "push", "--dry-run", "--porcelain")
	cmd.Dir = r.wt
	out, _ := cmd.CombinedOutput()
	raw := string(out)
	var dests []string
	for _, line := range strings.Split(raw, "\n") {
		for _, field := range strings.Fields(line) {
			if _, dst, ok := strings.Cut(field, ":"); ok && strings.HasPrefix(dst, "refs/") {
				dests = append(dests, dst)
			}
		}
	}
	return dests, raw
}

// TestGitPushSetsTheBranchOwnUpstream.
//
// MUTANT: drop --set-upstream from GitPush's arg list. The branch keeps
// whatever upstream it had — nothing for a fresh branch, origin/main for every
// branch created before this change — and this test reports upstream="".
func TestGitPushSetsTheBranchOwnUpstream(t *testing.T) {
	r := newTestRepo(t)
	r.commit(t, "work.txt", "work")

	if up := upstreamShort(t, r, r.tBranch); up != "" {
		t.Fatalf("fixture precondition: %s already tracks %q", r.tBranch, up)
	}
	if _, err := GitPush(context.Background(), r.wt); err != nil {
		t.Fatalf("GitPush: %v", err)
	}
	if up := upstreamShort(t, r, r.tBranch); up != "origin/"+r.tBranch {
		t.Fatalf("upstream is %q, want origin/%s", up, r.tBranch)
	}

	// The consequence, not just the config: a bare push must now name the task
	// branch even under the push.default value that used to send it to main.
	r.git(t, "config", "push.default", "upstream")
	r.commit(t, "more.txt", "more")
	dests, raw := bareDryRunTargets(t, r)
	want := "refs/heads/" + r.tBranch
	found := false
	for _, d := range dests {
		if d == "refs/heads/"+r.base {
			t.Errorf("a bare push would write %s\n%s", d, raw)
		}
		if d == want {
			found = true
		}
	}
	if !found {
		t.Errorf("a bare push named %v, want %s\n%s", dests, want, raw)
	}
}

// TestGitClearProtectedUpstreamOnlyTouchesProtectedTargets pins the predicate
// itself, because it is the piece that decides whether a repair is a fix or
// data loss. Each row states what must survive as loudly as what must go.
func TestGitClearProtectedUpstreamOnlyTouchesProtectedTargets(t *testing.T) {
	ctx := context.Background()

	t.Run("origin/main on a task branch is cleared", func(t *testing.T) {
		r := newTestRepo(t)
		// newTestRepo clones an EMPTY bare repo, so the base branch has tracking
		// config but no remote-tracking ref behind it, and --set-upstream-to
		// refuses a ref that does not exist. Publish the base first — which is
		// also what the real workspace looks like.
		r.git(t, "push", "-q", "origin", r.base+":refs/heads/"+r.base)
		r.git(t, "branch", "--set-upstream-to=origin/"+r.base, r.tBranch)
		got, err := GitClearProtectedUpstream(ctx, r.wt, r.tBranch)
		if err != nil {
			t.Fatalf("GitClearProtectedUpstream: %v", err)
		}
		if got != "origin/"+r.base {
			t.Errorf("reported %q cleared, want origin/%s", got, r.base)
		}
		if up := upstreamShort(t, r, r.tBranch); up != "" {
			t.Errorf("upstream is still %q", up)
		}
	})

	t.Run("the base branch keeps tracking its own remote branch", func(t *testing.T) {
		r := newTestRepo(t)
		if up := upstreamShort(t, r, r.base); up != "origin/"+r.base {
			t.Fatalf("fixture precondition: %s tracks %q", r.base, up)
		}
		got, err := GitClearProtectedUpstream(ctx, r.wt, r.base)
		if err != nil {
			t.Fatalf("GitClearProtectedUpstream: %v", err)
		}
		if got != "" {
			t.Errorf("reported %q cleared; the base branch tracking its own remote is correct", got)
		}
		if up := upstreamShort(t, r, r.base); up != "origin/"+r.base {
			t.Errorf("upstream became %q, want origin/%s", up, r.base)
		}
	})

	t.Run("a task branch tracking its own remote branch is left alone", func(t *testing.T) {
		r := newTestRepo(t)
		r.commit(t, "work.txt", "work")
		if _, err := GitPush(ctx, r.wt); err != nil {
			t.Fatalf("GitPush: %v", err)
		}
		got, err := GitClearProtectedUpstream(ctx, r.wt, r.tBranch)
		if err != nil {
			t.Fatalf("GitClearProtectedUpstream: %v", err)
		}
		if got != "" {
			t.Errorf("reported %q cleared; origin/<task branch> is the value this change wants", got)
		}
		if up := upstreamShort(t, r, r.tBranch); up != "origin/"+r.tBranch {
			t.Errorf("upstream became %q", up)
		}
	})

	t.Run("a branch with no upstream is not an error", func(t *testing.T) {
		r := newTestRepo(t)
		got, err := GitClearProtectedUpstream(ctx, r.wt, r.tBranch)
		if err != nil || got != "" {
			t.Errorf("got (%q, %v), want (\"\", nil)", got, err)
		}
	})

	t.Run("a branch that does not exist is not an error", func(t *testing.T) {
		r := newTestRepo(t)
		got, err := GitClearProtectedUpstream(ctx, r.wt, "no/such/branch")
		if err != nil || got != "" {
			t.Errorf("got (%q, %v), want (\"\", nil)", got, err)
		}
	})
}

// TestGitUpstreamSplitsRemoteFromBranchName is the reason GitUpstream does not
// read %(upstream:short): task branch names contain slashes, so splitting the
// short form on "/" recovers the wrong remote for exactly the branches this
// change is about.
func TestGitUpstreamSplitsRemoteFromBranchName(t *testing.T) {
	r := newTestRepo(t)
	const slashy = "polyforge/aihub-257-slashed"
	r.git(t, "checkout", "-q", "-b", slashy)
	r.commit(t, "s.txt", "s")
	r.git(t, "push", "-q", "-u", "origin", slashy+":refs/heads/"+slashy)

	remote, branch, err := GitUpstream(context.Background(), r.wt, slashy)
	if err != nil {
		t.Fatalf("GitUpstream: %v", err)
	}
	if remote != "origin" || branch != slashy {
		t.Errorf("got remote=%q branch=%q, want origin/%s", remote, branch, slashy)
	}
	if IsProtectedBranch(branch) {
		t.Errorf("%q was classified as a protected branch", branch)
	}
}

// TestGitUpstreamRefusesAPatternThatMatchesSeveralBranches.
//
// `git for-each-ref refs/heads/X` matches refs UNDER X as well as X itself, so
// a name that is a prefix of real branches and is no branch at all must not
// borrow one of their upstreams.
//
// ⚠️ ONLY ONE SIBLING HAS AN UPSTREAM, and that is the whole point. An earlier
// version of this test set one on both, which is the single configuration in
// which the defect is invisible: the guard it was written for filtered blank
// lines and then counted, so with both siblings tracking something the count
// was 2 and the guard fired for the wrong reason. Measured on git 2.43.0 with
// only pfx/one tracking origin/main, the pre-%(refname) format emitted
// "origin\trefs/heads/main" and "\t" — the blank-ish line was filtered, the
// count was 1, and the function returned origin/main for a name that does not
// exist. A fixture calibrated to the guard rather than to the defect passes
// against both.
//
// MUTANT: drop %(refname) from GitUpstream's format and count non-blank lines.
func TestGitUpstreamRefusesAPatternThatMatchesSeveralBranches(t *testing.T) {
	r := newTestRepo(t)
	r.git(t, "push", "-q", "origin", r.base+":refs/heads/"+r.base)
	r.git(t, "branch", "polyforge/one", r.tBranch)
	r.git(t, "branch", "polyforge/two", r.tBranch)
	r.git(t, "branch", "--set-upstream-to=origin/"+r.base, "polyforge/one")
	// polyforge/two deliberately tracks nothing.

	remote, branch, err := GitUpstream(context.Background(), r.wt, "polyforge")
	if err != nil {
		t.Fatalf("GitUpstream: %v", err)
	}
	if remote != "" || branch != "" {
		t.Errorf("got remote=%q branch=%q for a name that is a prefix of two branches "+
			"and is itself no branch at all; want empty", remote, branch)
	}
	// The consequence: nothing is unset on a branch that does not exist.
	cleared, err := GitClearProtectedUpstream(context.Background(), r.wt, "polyforge")
	if err != nil || cleared != "" {
		t.Errorf("GitClearProtectedUpstream(polyforge) = (%q, %v), want (\"\", nil)", cleared, err)
	}
	if up := upstreamShort(t, r, "polyforge/one"); up != "origin/"+r.base {
		t.Errorf("polyforge/one's upstream became %q; it must not have been touched", up)
	}
}

// TestGitClearProtectedUpstreamKeepsALocalTrackingUpstream.
//
// `git branch --set-upstream-to=main <task>` (and branch.autoSetupMerge=always
// off a local base) writes branch.<n>.remote = "." — a LOCAL-tracking upstream.
// Measured on git 2.43.0: %(upstream:remotename) is "." and
// %(upstream:remoteref) is refs/heads/main, so a predicate keyed on
// "the upstream's branch is protected" reads this as the hazard and unsets it.
//
// It is not the hazard. A push to "." cannot move origin/main. And it is the
// setting that gives a task worktree its ahead/behind-vs-main readout — the
// exact information this change is accused of costing — so clearing it would
// take away the workaround while claiming to fix the problem.
//
// MUTANT: drop `remote == "."` from GitClearProtectedUpstream's guard.
func TestGitClearProtectedUpstreamKeepsALocalTrackingUpstream(t *testing.T) {
	r := newTestRepo(t)
	r.git(t, "branch", "--set-upstream-to="+r.base, r.tBranch)

	remote, remoteBranch, err := GitUpstream(context.Background(), r.wt, r.tBranch)
	if err != nil {
		t.Fatalf("GitUpstream: %v", err)
	}
	if remote != "." || remoteBranch != r.base {
		t.Fatalf("fixture did not produce a local-tracking upstream: remote=%q branch=%q", remote, remoteBranch)
	}

	cleared, err := GitClearProtectedUpstream(context.Background(), r.wt, r.tBranch)
	if err != nil {
		t.Fatalf("GitClearProtectedUpstream: %v", err)
	}
	if cleared != "" {
		t.Errorf("reported %q cleared; a local-tracking upstream cannot push to origin/%s "+
			"and is what `git status` reads for ahead/behind vs the base branch", cleared, r.base)
	}
	if up := upstreamShort(t, r, r.tBranch); up != r.base {
		t.Errorf("upstream became %q, want %q", up, r.base)
	}
}
