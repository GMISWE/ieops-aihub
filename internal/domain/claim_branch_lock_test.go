package domain

// The SERVER half of aihub#356: a claim keys its git_branch lock on the branch
// the claiming client is really about to check out, not on
// declared_resources[].task_branch.
//
// ─── Why this is in-package and needs no database ───────────────────────────
//
// The rule is a pure transform of a declared resource, so it is testable
// without a claim, a transaction or a work item.
//
// ⚠️ THE TRANSFORM AND THE CALL SITE ARE TWO ASSERTIONS, and an earlier version
// of this comment argued the second one away. It said a DB test would add
// nothing because the same loop derives EVERY lock a claim takes, so a claim
// that stopped applying the substitution would acquire nothing at all and take
// aihub#342's, aihub#261's and aihub#329's suites down with it. That is FALSE,
// and the review of aihub#356 measured it: deleting the substitution does not
// stop the loop deriving locks — the very next line still runs derivedLockProbe
// — it only reverts the key to the declared name, which is the state those
// suites were written against, so they stay green BY CONSTRUCTION. The reviewer
// deleted the line, ran the full suite including the DB suites, and everything
// passed. An untested line carrying a written argument not to test it is worse
// than an untested line.
//
// TestDeriveClaimLocks below is the fix. Every OTHER test in this file calls
// EffectiveDeclaredResource itself (through lockFor, or inline) and therefore
// tests only the transform; that one calls deriveClaimLocks, which is the
// function FnClaimWorkItem hands its stored declared_resources to, so the
// substitution has to be applied THERE for it to pass. deriveClaimLocks exists
// as a separate function for exactly this reason — see its doc comment.
//
// ─── What each case is for ─────────────────────────────────────────────────
//
// The client half — that the branch the MCP tool sends really is the one git
// checked out — is internal/mcp/claim_branch_lock_test.go. Both sides anchor on
// the literal wire name "task_branches" rather than on the Go field, so a
// rename on either side goes red here even though the two never share a symbol.
//
// Run: go test ./internal/domain/ -run 'TestClaimLockKey|TestGitBranchLock|TestClaimRequestBindsTaskBranches|TestDeriveClaimLocks' -v

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
// MUTANT: make EffectiveDeclaredResource return res unchanged. The first two
// cases go red with the ieops#996 key.
//
// ⚠️ Deleting `d = req.EffectiveDeclaredResource(d)` from deriveClaimLocks does
// NOT redden this test, because lockFor applies the transform itself. That is
// the hole TestDeriveClaimLocks closes; do not read this test as covering it.
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
		// claim that PREDICTS no branch for a repo has said nothing about it, so
		// the declaration is the only statement anyone made.
		//
		// ⚠️ Not "materialises no worktree". Those are different sets, and the
		// difference is the one case aihub#356 does not degrade cleanly: a claim
		// whose prediction succeeded and whose `worktree add` then failed DOES
		// report a branch, and the declaration is overridden. See
		// EffectiveDeclaredResource, and
		// TestClaimReportsAKeyedBranchNoWorktreeConfirms in internal/mcp.
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
// It asserts through lockConflictProbe.Matches, the repo's own Go mirror of the
// SQL predicate the claim path runs (lockConflictWhereClause).
//
// ⚠️ BUT ITS DISCRIMINATING POWER IS THE STRING COMPARISON, and an earlier
// version of this comment claimed the opposite ("what is checked is the
// collision test itself and not a restatement of string equality"). That is
// deductively false for git_branch: derivedLockProbe returns exactProbe(lockKey)
// (conflicts.go), and Matches on a probe whose Keys is exactly {lockKey} and
// whose LikePattern is empty reduces to incomingKey == heldKey — which the
// t.Fatalf three lines above has already established. The Matches assertion is
// still worth keeping (it reddens on a mutant, and it is the line that would
// notice if git_branch ever grew a non-trivial probe), but do not read it as
// covering the collision machinery. The file_scope arm of that machinery is
// where the non-trivial probes live, and aihub#261's suite owns it.
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

// pickLock returns the key and the paired probe deriveClaimLocks produced for
// the one lock of the given type, failing if the two slices have gone out of
// step or if there is not exactly one such lock.
func pickLock(t *testing.T, locks []ResourceLockReq, probes []lockConflictProbe, lockType string) (string, lockConflictProbe) {
	t.Helper()
	if len(locks) != len(probes) {
		t.Fatalf("locks (%d) and probes (%d) are out of step — lockProbes[i] no longer pairs with RequestedLocks[i] (aihub#261)",
			len(locks), len(probes))
	}
	found := -1
	for i, l := range locks {
		if l.ResourceType != lockType {
			continue
		}
		if found >= 0 {
			t.Fatalf("two %s locks derived: %q and %q", lockType, locks[found].ResourceKey, l.ResourceKey)
		}
		found = i
	}
	if found < 0 {
		t.Fatalf("no %s lock derived; got %+v", lockType, locks)
	}
	return locks[found].ResourceKey, probes[found]
}

// TestDeriveClaimLocks is the CALL SITE, and it is the only test in this file
// that reddens when the substitution is deleted from the claim path.
//
// ⚠️ READ THIS BEFORE ADDING A CASE TO ANY OTHER TEST HERE. Every one of them
// runs EffectiveDeclaredResource ITSELF — lockFor does it, and
// TestGitBranchLockStillBlocks... does it inline — so all of them assert the
// TRANSFORM and none of them asserts that the claim applies it. The review of
// aihub#356 deleted `d = req.EffectiveDeclaredResource(d)` from the claim path
// and the entire suite, DB suites included, stayed green: the key simply
// reverted to the declared name, which is the pre-aihub#356 state every one of
// those suites was written against.
//
// So this test takes the STORED WIRE PAYLOAD, hands it to the same function
// FnClaimWorkItem hands it to, and reads the key off the far end. Nothing here
// touches EffectiveDeclaredResource by name; if the claim path stops applying
// it, there is nowhere else for the substitution to come from.
//
// Every subtest reads the derived locks off `req.RequestedLocks` AFTER the call
// rather than off a return value, and that is deliberate. deriveClaimLocks used
// to return the slice and leave FnClaimWorkItem to assign it on the following
// line; aihub#356 review mutant M3 deleted that one assignment, left the call
// itself in place, and the claim derived no locks at all with build, vet, the
// whole test suite and golangci-lint still green. The write-back into req is now
// the function's contract, so asserting through req is what pins it — a future
// change that reverts to returning the slice cannot compile against these.
//
// MUTANT: delete `d = req.EffectiveDeclaredResource(d)` from deriveClaimLocks.
// The first subtest goes red naming the ieops#996 key; the three controls below
// it stay green, so a "fix" that simply disabled the feature cannot pass.
//
// MUTANT (M3, the write-back): delete `req.RequestedLocks = locks` from the end
// of deriveClaimLocks. Measured: the first THREE subtests go red on the derived
// locks being absent (and TestDerivationSkipsEmptyLockKey with them), which is
// the coverage that did not exist when M3 was found. The fourth,
// "a client-supplied requested_locks slice is still trusted verbatim", stays
// GREEN and must — that path leaves through the early return, before the
// write-back, so the mutant genuinely cannot change it. It is a control here,
// not a gap.
func TestDeriveClaimLocks(t *testing.T) {
	const project = "ieops"
	// The measured ieops#996 pair, written as the wire JSON a claim really
	// reads rather than as Go structs — unmarshalDeclaredResources sits between
	// the two, and aihub#261's defect was precisely a hand-written struct in
	// this position that dropped a field.
	const stored = `[
		{"type":"repo","uri":"repo:ieops-ctlchain","task_branch":"polyforge/pin-bump-token","intent":"write"},
		{"type":"path","uri":"file:internal/cache/x.go","repo":"ieops-ctlchain","intent":"write"}
	]`
	const checkedOut = "polyforge/ieops-996-proxy-pin-bump-pin-bump-token-checkout"
	const declaredKey = "ieops-ctlchain/polyforge/pin-bump-token"
	const realKey = "ieops-ctlchain/" + checkedOut

	t.Run("the claim keys git_branch on the branch the client reports", func(t *testing.T) {
		req := &ClaimRequest{TaskBranches: map[string]string{"ieops-ctlchain": checkedOut}}
		probes := deriveClaimLocks(req, json.RawMessage(stored), project)
		locks := req.RequestedLocks

		key, probe := pickLock(t, locks, probes, "git_branch")
		if key == declaredKey {
			t.Fatalf("the claim inserted %q — the declared task_branch, which is the ieops#996 key. "+
				"The branch it is really checking out is %q, so this lock protects a branch nobody is on",
				declaredKey, checkedOut)
		}
		if key != realKey {
			t.Fatalf("the claim inserted git_branch %q, want %q", key, realKey)
		}
		// The probe is what a LATER claim is tested against, and it is built in
		// the same call. A key that moved while its probe did not is a lock that
		// inserts under one name and collides under another — which enforces
		// nothing at all, while every assertion about the key alone stays green.
		if !probe.Matches(realKey) {
			t.Errorf("the probe paired with %q does not match it, so a second claim on that branch is let through", realKey)
		}
		if probe.Matches(declaredKey) {
			t.Errorf("the probe still matches the declared key %q — the substitution reached the key but not the probe", declaredKey)
		}
	})

	// ── controls: all three stay green under the mutant above ────────────────
	//
	// "the gate went red" is also satisfied by disabling the feature, so these
	// pin the two things a disabling fix would break.

	t.Run("a repo the client reports nothing for keeps its declared key", func(t *testing.T) {
		req := &ClaimRequest{TaskBranches: map[string]string{"some-other-repo": "polyforge/elsewhere"}}
		probes := deriveClaimLocks(req, json.RawMessage(stored), project)
		locks := req.RequestedLocks
		key, _ := pickLock(t, locks, probes, "git_branch")
		if key != declaredKey {
			t.Errorf("key = %q, want the declared %q — with nothing checked out for that repo the "+
				"declaration is the only statement anyone has made about it", key, declaredKey)
		}
	})

	t.Run("the file_scope lock alongside it is untouched", func(t *testing.T) {
		req := &ClaimRequest{TaskBranches: map[string]string{"ieops-ctlchain": checkedOut}}
		probes := deriveClaimLocks(req, json.RawMessage(stored), project)
		locks := req.RequestedLocks
		if len(locks) != 2 {
			t.Fatalf("derived %d locks from two declared entries: %+v", len(locks), locks)
		}
		key, probe := pickLock(t, locks, probes, "file_scope")
		if want := "ieops:ieops-ctlchain:internal/cache/x.go"; key != want {
			t.Errorf("file_scope key = %q, want %q — aihub#356 re-keyed a lock it has no business touching", key, want)
		}
		if !probe.Matches(key) {
			t.Errorf("the file_scope probe no longer matches its own key %q", key)
		}
	})

	t.Run("a client-supplied requested_locks slice is still trusted verbatim", func(t *testing.T) {
		// The raw-API path. An override is present for the repo the declaration
		// names, so a substitution that leaked past the "verbatim or derived,
		// never both" rule would show up as a third lock or a moved key.
		req := &ClaimRequest{
			TaskBranches:   map[string]string{"ieops-ctlchain": checkedOut},
			RequestedLocks: []ResourceLockReq{{ResourceType: "git_branch", ResourceKey: declaredKey}},
		}
		probes := deriveClaimLocks(req, json.RawMessage(stored), project)
		locks := req.RequestedLocks
		if len(locks) != 1 || locks[0].ResourceKey != declaredKey {
			t.Fatalf("locks = %+v, want exactly the one the client asked for (%q)", locks, declaredKey)
		}
		if len(probes) != 1 || !probes[0].Matches(declaredKey) || probes[0].Matches(realKey) {
			t.Errorf("a client-supplied lock got something other than plain key equality: %+v", probes)
		}
	})
}
