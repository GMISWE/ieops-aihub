package domain

// The SERVER half of aihub#356: a claim keys its git_branch lock on the branch
// the claiming client is really about to check out, not on
// declared_resources[].task_branch.
//
// ─── Why this is in-package and needs no database ───────────────────────────
//
// The rule is a pure transform of a declared resource, so it is testable
// without a claim, a transaction or a work item. What a DB test would add on
// top is only that FnClaimWorkItem still calls the transform — and that is
// already load-bearing for every existing claim/lock DB suite: the same loop
// derives EVERY lock a claim takes, so a claim that stopped running it would
// acquire nothing at all and take aihub#342's, aihub#261's and aihub#329's
// suites down with it.
//
// ─── What each case is for ─────────────────────────────────────────────────
//
// The client half — that the branch the MCP tool sends really is the one git
// checked out — is internal/mcp/claim_branch_lock_test.go. Both sides anchor on
// the literal wire name "task_branches" rather than on the Go field, so a
// rename on either side goes red here even though the two never share a symbol.
//
// Run: go test ./internal/domain/ -run 'TestClaim.*TaskBranch|TestGitBranchLock' -v

import (
	"encoding/json"
	"testing"
)

// repoRes is a {"type":"repo"} declaration, the only shape that takes a
// git_branch lock.
func repoRes(name, taskBranch string) DeclaredResourceItem {
	return DeclaredResourceItem{Type: "repo", URI: "repo:" + name, TaskBranch: taskBranch}
}

// lockFor is the key a claim carrying these overrides would insert for res.
// It runs the same two calls, in the same order, as FnClaimWorkItem's
// derivation loop.
func lockFor(t *testing.T, overrides map[string]string, res DeclaredResourceItem, project string) (string, string) {
	t.Helper()
	req := &ClaimRequest{TaskBranches: overrides}
	return derivedLock(req.EffectiveDeclaredResource(res), project)
}

// TestClaimLockKeyFollowsTheActualTaskBranch is the server-side statement of the
// fix: the git_branch key is the branch the claim reports, and the declared
// task_branch is only the fallback.
//
// MUTANT: delete the `d = req.EffectiveDeclaredResource(d)` line from
// FnClaimWorkItem's derivation loop, or make EffectiveDeclaredResource return
// res unchanged. The first two cases go red with the ieops#996 key.
func TestClaimLockKeyFollowsTheActualTaskBranch(t *testing.T) {
	const project = "ieops"

	t.Run("the reported branch beats the declared task_branch", func(t *testing.T) {
		// The measured ieops#996 pair, verbatim: the work item declared
		// polyforge/pin-bump-token and the worktree came out somewhere else.
		lockType, key := lockFor(t,
			map[string]string{"ieops-ctlchain": "polyforge/ieops-996-proxy-pin-bump-pin-bump-token-checkout"},
			repoRes("ieops-ctlchain", "polyforge/pin-bump-token"), project)
		if lockType != "git_branch" {
			t.Fatalf("lock type = %q, want git_branch", lockType)
		}
		if want := "ieops-ctlchain/polyforge/ieops-996-proxy-pin-bump-pin-bump-token-checkout"; key != want {
			t.Errorf("key = %q, want %q — the lock is keyed on a branch nobody checked out", key, want)
		}
	})

	t.Run("the reported branch beats the default of main", func(t *testing.T) {
		// A repo entry with NO task_branch defaults to "<repo>/main" for lock
		// derivation, which is worse than a wrong name: every work item in the
		// repo derives the identical key, so the claims that most need to be
		// told apart are exactly the ones that cannot be.
		_, key := lockFor(t,
			map[string]string{"aihub": "polyforge/aihub-356-lock-key"},
			repoRes("aihub", ""), project)
		if want := "aihub/polyforge/aihub-356-lock-key"; key != want {
			t.Errorf("key = %q, want %q", key, want)
		}
	})

	t.Run("no report leaves the declaration in force", func(t *testing.T) {
		// The honest degradation, and the reason this is not a precondition: a
		// claim that materialises no worktree for a repo has put nothing on any
		// branch there, so the declaration is the only statement anyone made.
		for name, overrides := range map[string]map[string]string{
			"nil map":            nil,
			"empty map":          {},
			"another repo":       {"some-other-repo": "polyforge/elsewhere"},
			"empty string value": {"aihub": ""},
		} {
			_, key := lockFor(t, overrides, repoRes("aihub", "polyforge/declared"), project)
			if want := "aihub/polyforge/declared"; key != want {
				t.Errorf("%s: key = %q, want %q", name, key, want)
			}
		}
	})

	t.Run("with neither a report nor a declaration the key is unchanged", func(t *testing.T) {
		_, key := lockFor(t, nil, repoRes("aihub", ""), project)
		if want := "aihub/main"; key != want {
			t.Errorf("key = %q, want %q — aihub#356 must not move the pre-existing default", key, want)
		}
	})
}

// TestClaimLockKeyOverrideTouchesNothingElse is the containment control. The
// scope of this change is one field of one declared type; every other lock a
// claim takes has to come out byte-identical, or "fix the git_branch key" has
// quietly become "re-key everything".
func TestClaimLockKeyOverrideTouchesNothingElse(t *testing.T) {
	const project = "ieops"
	// An override is present for the very repo these entries name, so anything
	// that leaked would leak here.
	overrides := map[string]string{"ieops-core": "polyforge/ieops-42-real-branch"}

	t.Run("a path entry keeps its file_scope key", func(t *testing.T) {
		res := DeclaredResourceItem{Type: "path", URI: "file:internal/cache/x.go", Repo: "ieops-core"}
		lockType, key := lockFor(t, overrides, res, project)
		if lockType != "file_scope" || key != "ieops:ieops-core:internal/cache/x.go" {
			t.Errorf("got (%q, %q), want (file_scope, ieops:ieops-core:internal/cache/x.go)", lockType, key)
		}
	})

	t.Run("a service entry keeps its deploy_env key", func(t *testing.T) {
		res := DeclaredResourceItem{Type: "service", URI: "service:gateway"}
		lockType, key := lockFor(t, overrides, res, project)
		if lockType != "deploy_env" || key != "gateway" {
			t.Errorf("got (%q, %q), want (deploy_env, gateway)", lockType, key)
		}
	})

	t.Run("a read-intent path entry still takes no lock at all", func(t *testing.T) {
		// aihub#342's rule sits DOWNSTREAM of the substitution: reporting a
		// branch must not resurrect a lock the read rule had refused.
		res := DeclaredResourceItem{Type: "path", URI: "file:internal/cache/x.go", Repo: "ieops-core", Intent: "read"}
		lockType, key := lockFor(t, overrides, res, project)
		if lockType != "" || key != "" {
			t.Errorf("got (%q, %q), want no lock — aihub#342's read rule was bypassed", lockType, key)
		}
	})

	t.Run("a read-intent repo entry keeps taking a git_branch lock, now on the real branch", func(t *testing.T) {
		// ⚠️ NOT a bug, and NOT what a first reading of aihub#342 predicts.
		// derivedLock's read rule is written as a condition on lockType and is
		// deliberately scoped to file_scope: a repo entry takes its git_branch
		// lock whatever the intent, because a branch is not a per-file exclusion
		// a reader can harmlessly share, and because PredictConflicts rule 2 has
		// no intent check either. Whether intent=read should mean anything for a
		// repo entry is recorded there as undecided.
		//
		// aihub#356 must not decide it in passing. Skipping the substitution for
		// read intent would leave exactly ONE shape of declaration still keyed on
		// a branch nobody checked out — the quietest possible half-fix — so the
		// override applies here for the same reason the lock does.
		res := repoRes("ieops-core", "polyforge/declared")
		res.Intent = "read"
		lockType, key := lockFor(t, overrides, res, project)
		if lockType != "git_branch" || key != "ieops-core/polyforge/ieops-42-real-branch" {
			t.Errorf("got (%q, %q), want (git_branch, ieops-core/polyforge/ieops-42-real-branch)", lockType, key)
		}
	})

	t.Run("the caller's own declaration is not mutated", func(t *testing.T) {
		// EffectiveDeclaredResource returns a copy. If it wrote through, the
		// substituted branch would reach unrecognizedResources and the
		// wi_resources_updated audit as though the work item had declared it.
		res := repoRes("ieops-core", "polyforge/declared")
		req := &ClaimRequest{TaskBranches: overrides}
		_ = req.EffectiveDeclaredResource(res)
		if res.TaskBranch != "polyforge/declared" {
			t.Errorf("the input declaration was rewritten to %q", res.TaskBranch)
		}
	})
}

// TestGitBranchLockStillBlocksTwoAttemptsOnOneBranch is the POSITIVE control:
// re-keying the lock must not stop it locking.
//
// It asserts through lockConflictProbe.Matches, which is the repo's own Go
// mirror of the SQL predicate the claim path runs (lockConflictWhereClause), so
// what is checked is the collision test itself and not a restatement of string
// equality.
func TestGitBranchLockStillBlocksTwoAttemptsOnOneBranch(t *testing.T) {
	const branch = "polyforge/ieops-996-proxy-pin-bump"

	// Two work items whose claims both end up on `branch` of the same repo:
	// what the aihub#322 comment describes when one repo is listed under two
	// projects, and what a hand-made shared branch does. They declare DIFFERENT
	// task_branches, so before aihub#356 they derived two different keys and
	// could not collide — the very hazard this work item was filed for.
	held := &ClaimRequest{TaskBranches: map[string]string{"ieops-ctlchain": branch}}
	incoming := &ClaimRequest{TaskBranches: map[string]string{"ieops-ctlchain": branch}}

	_, heldKey, _ := derivedLockProbe(held.EffectiveDeclaredResource(
		repoRes("ieops-ctlchain", "polyforge/pin-bump-token")), "ieops")
	_, incomingKey, incomingProbe := derivedLockProbe(incoming.EffectiveDeclaredResource(
		repoRes("ieops-ctlchain", "polyforge/something-else-entirely")), "ieops-two")

	if heldKey != incomingKey {
		t.Fatalf("two attempts on %s of one repo derived different keys (%q vs %q), so neither would ever see the other",
			branch, heldKey, incomingKey)
	}
	if !incomingProbe.Matches(heldKey) {
		t.Errorf("the incoming claim's conflict probe does not match the lock already held on %q — "+
			"the second agent would be let onto a branch the first is working on", heldKey)
	}

	t.Run("and still lets a different branch through", func(t *testing.T) {
		// The other half: a probe that matches everything would pass the test
		// above while blocking every claim in the repo.
		other := &ClaimRequest{TaskBranches: map[string]string{"ieops-ctlchain": "polyforge/ieops-994-unrelated"}}
		_, _, otherProbe := derivedLockProbe(other.EffectiveDeclaredResource(
			repoRes("ieops-ctlchain", "polyforge/pin-bump-token")), "ieops")
		if otherProbe.Matches(heldKey) {
			t.Errorf("a claim on an unrelated branch collides with %q — the lock now blocks work it should not", heldKey)
		}
	})
}

// TestClaimRequestBindsTaskBranchesFromTheWire is the transport hop.
//
// The MCP client writes the key "task_branches" into a map[string]any and the
// HTTP handler binds the body into this struct; nothing in between would notice
// a tag that had drifted. aihub#290's expected_version is the precedent — a
// parameter that travelled the entire way and was discarded in silence at the
// far end — and the shape of the failure is a claim that keeps deriving the old
// key while every test of the transform above stays green.
func TestClaimRequestBindsTaskBranchesFromTheWire(t *testing.T) {
	// Written as the literal body the client sends, not as a marshal of the
	// struct: marshalling and unmarshalling the same struct agrees with itself
	// whatever the tag says.
	const body = `{
		"idempotency_key": "idem-1",
		"session_info": {"machine_id": "m1", "session_secret": "s1"},
		"task_branches": {"aihub": "polyforge/aihub-356-real-branch"}
	}`

	var req ClaimRequest
	if err := json.Unmarshal([]byte(body), &req); err != nil {
		t.Fatalf("unmarshal claim body: %v", err)
	}
	if got := req.TaskBranches["aihub"]; got != "polyforge/aihub-356-real-branch" {
		t.Fatalf("task_branches did not reach ClaimRequest (got %q) — the JSON tag and the wire name disagree", got)
	}

	// And it reaches the derivation, which is the only reason it is carried.
	_, key := derivedLock(req.EffectiveDeclaredResource(repoRes("aihub", "polyforge/declared")), "aihub")
	if want := "aihub/polyforge/aihub-356-real-branch"; key != want {
		t.Errorf("key = %q, want %q", key, want)
	}

	t.Run("a body with no task_branches leaves the map nil", func(t *testing.T) {
		var old ClaimRequest
		if err := json.Unmarshal([]byte(`{"idempotency_key":"idem-2"}`), &old); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if old.TaskBranches != nil {
			t.Errorf("TaskBranches = %#v, want nil — an older client must not be read as reporting branches", old.TaskBranches)
		}
	})
}
