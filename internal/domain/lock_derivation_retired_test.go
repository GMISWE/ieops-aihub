package domain

// aihub#416 AC-1 / AC-5 / AC-6: repo and service declarations derive NO lock,
// path/document/section still derive file_scope, and the lock-type VOCABULARY is
// untouched.
//
// ─── What this file replaces, and why the replacement is smaller ────────────
//
// It stands where claim_branch_lock_test.go stood. That file was the server half
// of aihub#356 — 452 lines asserting that a claim keyed its git_branch lock on
// the branch the client was really about to check out — and its whole subject is
// gone: resourceToLock derives no git_branch key for anything, so there is no
// key left to get right, no ClaimRequest.TaskBranches to carry the override, and
// no EffectiveDeclaredResource to apply it.
//
// 🔴 Deleting a test file is a coverage LOSS and is written down as one rather
// than left to be noticed. What went with it: the transform cases, the
// transport-binding case, and the two-attempts-on-one-branch collision control.
// None of them can be ported, because each asserts a property of a lock this
// change stops taking. What is kept below is the one property that survives —
// deriveClaimLocks is still the function FnClaimWorkItem hands stored
// declared_resources to — re-pointed at the new answer, plus the file_scope arm
// carried over BYTE-FOR-BYTE so the retirement cannot be confused with a
// regression in the type that still locks.
//
// ─── The distinguishing power ──────────────────────────────────────────────
//
// MUTANT (AC-1): restore either retired case in resourceToLock —
//
//	case "repo":    return "git_branch", repoName + "/" + branch
//	case "service": return "deploy_env", svc
//
// TestResourceToLock_RepoAndServiceDeriveNoLock goes red naming the type, and so
// does TestDeriveClaimLocks_RepoAndServiceContributeNoLock. Measured: both, on
// each case independently.
//
// ⚠️ AND THE CONTROLS MUST STAY GREEN UNDER THAT MUTANT, which is the half that
// makes the gate mean something. "repo derives nothing" is also satisfied by a
// resourceToLock that derives nothing for ANYTHING, so the file_scope cases
// below are not decoration: they are what separates "the retirement landed" from
// "the mapper broke".
//
// Run: go test ./internal/domain/ -run 'TestResourceToLock|TestDeriveClaimLocks' -v

import (
	"encoding/json"
	"testing"
)

// TestResourceToLock_RepoAndServiceDeriveNoLock is AC-1 at the mapper.
//
// Both halves of the pair are asserted, not just the type: a mapper returning
// ("", "some-key") would satisfy a type-only check while still handing a key to
// a caller that skips on `lockType == ""` — and the next caller to test the key
// first would insert a row with an empty type.
func TestResourceToLock_RepoAndServiceDeriveNoLock(t *testing.T) {
	cases := []struct {
		name string
		res  DeclaredResourceItem
	}{
		{"repo", DeclaredResourceItem{Type: "repo", URI: "repo:ieops-ctlchain", Intent: "write"}},
		{"repo with a declared task_branch", DeclaredResourceItem{
			Type: "repo", URI: "repo:ieops-ctlchain", TaskBranch: "polyforge/pin-bump-token", Intent: "write"}},
		{"service", DeclaredResourceItem{Type: "service", URI: "service:aihub", Intent: "write"}},
		{"external_ref", DeclaredResourceItem{Type: "external_ref", URI: "https://example.test/x"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			lockType, lockKey := resourceToLock(tc.res, "ieops")
			if lockType != "" || lockKey != "" {
				t.Errorf("resourceToLock(%s) = (%q, %q), want (\"\", \"\") — aihub#416 retired this derivation, "+
					"so a lock here is a resource_locks row nothing releases and no declaration explains",
					tc.name, lockType, lockKey)
			}
		})
	}

	// The intent rule cannot resurrect either type. derivedLock only subtracts,
	// so this cannot pass while resourceToLock is wrong — it is here because
	// derivedLock's comment used to argue at length about what intent means for
	// repo/service, and the answer is now "nothing, structurally".
	t.Run("no intent brings them back", func(t *testing.T) {
		for _, intent := range []string{"", "read", "write", "refactor", "exclusive"} {
			for _, res := range []DeclaredResourceItem{
				{Type: "repo", URI: "repo:ieops-ctlchain", Intent: intent},
				{Type: "service", URI: "service:aihub", Intent: intent},
			} {
				if lt, lk := derivedLock(res, "ieops"); lt != "" || lk != "" {
					t.Errorf("derivedLock(%s, intent=%q) = (%q, %q), want no lock", res.Type, intent, lt, lk)
				}
			}
		}
	})
}

// TestResourceToLock_PathStillDerivesFileScope is the control, and the reason
// AC-5 can be checked at all: it is the pre-change assertion, unchanged.
//
// If this goes red, the retirement has damaged the type it was supposed to leave
// alone, and no amount of the tests above passing means anything.
func TestResourceToLock_PathStillDerivesFileScope(t *testing.T) {
	for _, typ := range []string{"path", "document", "section"} {
		lockType, lockKey := resourceToLock(
			DeclaredResourceItem{Type: typ, URI: "file:internal/domain/engine.go", Repo: "ieops-core"}, "ieops")
		if lockType != "file_scope" {
			t.Errorf("resourceToLock(%s) type = %q, want file_scope", typ, lockType)
		}
		if want := "ieops:ieops-core:internal/domain/engine.go"; lockKey != want {
			t.Errorf("resourceToLock(%s) key = %q, want %q", typ, lockKey, want)
		}
	}
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

// TestDeriveClaimLocks_RepoAndServiceContributeNoLock is the CALL SITE arm, and
// it is the one that would catch a retirement applied to the mapper but bypassed
// by the claim.
//
// That is not hypothetical here: PredictConflicts rule 2 used to hardcode
// 'git_branch' in SQL and bypass resourceToLock entirely, which is why the rule
// had to be rewritten in the same change. deriveClaimLocks is the other place
// with the opportunity to do that, so it is asserted through the function
// FnClaimWorkItem really calls, on the wire JSON a claim really reads.
//
// ⚠️ Every subtest reads the derived locks off `req.RequestedLocks` AFTER the
// call rather than off a return value, and that is inherited deliberately from
// the test this replaces. deriveClaimLocks used to return the slice and leave
// FnClaimWorkItem to assign it; aihub#356 review mutant M3 deleted that one
// assignment and the claim derived no locks at all with build, vet, the whole
// suite and golangci-lint still green. The write-back into req is the function's
// contract, so asserting through req is what pins it.
func TestDeriveClaimLocks_RepoAndServiceContributeNoLock(t *testing.T) {
	const project = "ieops"
	// The measured ieops#996 payload, kept verbatim from the retired test: a
	// repo entry carrying a task_branch, alongside a path entry. Written as the
	// stored wire JSON rather than as Go structs, because
	// unmarshalDeclaredResources sits between the two and aihub#261's defect was
	// a hand-written struct in exactly this position dropping a field.
	const stored = `[
		{"type":"repo","uri":"repo:ieops-ctlchain","task_branch":"polyforge/pin-bump-token","intent":"write"},
		{"type":"service","uri":"service:aihub","intent":"write"},
		{"type":"path","uri":"file:internal/cache/x.go","repo":"ieops-ctlchain","intent":"write"}
	]`

	t.Run("three declared entries derive exactly one lock", func(t *testing.T) {
		req := &ClaimRequest{}
		probes := deriveClaimLocks(req, json.RawMessage(stored), project)
		locks := req.RequestedLocks

		if len(locks) != 1 {
			t.Fatalf("derived %d locks from repo+service+path: %+v — repo and service must contribute none", len(locks), locks)
		}
		for _, l := range locks {
			if l.ResourceType == "git_branch" || l.ResourceType == "deploy_env" {
				t.Errorf("the claim derived a %s lock (%q). aihub#416 retired that derivation: "+
					"the row would be released by no pause and reached by no orphan sweep, which is the "+
					"condition this work item exists to remove", l.ResourceType, l.ResourceKey)
			}
		}
		// Carried over BYTE-FOR-BYTE from the file this replaces (AC-5): the
		// file_scope key and its probe must be exactly what they were.
		key, probe := pickLock(t, locks, probes, "file_scope")
		if want := "ieops:ieops-ctlchain:internal/cache/x.go"; key != want {
			t.Errorf("file_scope key = %q, want %q — the retirement moved a lock it has no business touching", key, want)
		}
		if !probe.Matches(key) {
			t.Errorf("the file_scope probe no longer matches its own key %q", key)
		}
	})

	t.Run("a payload of only repo and service entries derives nothing at all", func(t *testing.T) {
		// AC-2's unit-level companion: this is the shape whose claim must end up
		// holding zero rows. The DB arm is in conflicts_db_test.go; this one says
		// the derivation is where the zero comes from, rather than an INSERT
		// failing somewhere downstream.
		req := &ClaimRequest{}
		probes := deriveClaimLocks(req, json.RawMessage(
			`[{"type":"repo","uri":"repo:aihub","intent":"write"},
			  {"type":"service","uri":"service:aihub","intent":"write"}]`), project)
		if len(req.RequestedLocks) != 0 || len(probes) != 0 {
			t.Fatalf("locks = %+v, probes = %d, want none", req.RequestedLocks, len(probes))
		}
	})

	t.Run("a client-supplied requested_locks slice is still trusted verbatim", func(t *testing.T) {
		// The raw-API escape hatch (owner ruling Q-3: it stays). It is now the
		// ONLY way a git_branch row can be created, so it is asserted rather than
		// assumed — a retirement that also closed this path would be a wider
		// change than the ruling authorised.
		//
		// It is also the control for the mutant: this subtest passes whether or
		// not the derivation is retired, because it leaves through the early
		// return before any derivation happens.
		req := &ClaimRequest{
			RequestedLocks: []ResourceLockReq{{ResourceType: "git_branch", ResourceKey: "ieops-ctlchain/polyforge/x"}},
		}
		probes := deriveClaimLocks(req, json.RawMessage(stored), project)
		locks := req.RequestedLocks
		if len(locks) != 1 || locks[0].ResourceKey != "ieops-ctlchain/polyforge/x" {
			t.Fatalf("locks = %+v, want exactly the one the client asked for", locks)
		}
		if len(probes) != 1 || !probes[0].Matches("ieops-ctlchain/polyforge/x") {
			t.Errorf("a client-supplied lock got something other than plain key equality: %+v", probes)
		}
	})
}
