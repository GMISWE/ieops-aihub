package domain

import "testing"

// aihub#222: file_scope lock keys must be namespaced by project so that
// byte-identical relative paths in different projects (a fork repo and its
// parent) do not share a lock key and hard-block each other. git_branch and
// deploy_env keys must stay unaffected by project.

func TestResourceToLock_FileScopeNamespacedByProject(t *testing.T) {
	lt, lk := resourceToLock(DeclaredResourceItem{Type: "path", URI: "file:internal/domain/engine.go"}, "aihub")
	if lt != "file_scope" {
		t.Fatalf("lockType = %q, want file_scope", lt)
	}
	if want := "aihub:internal/domain/engine.go"; lk != want {
		t.Errorf("file_scope key = %q, want %q", lk, want)
	}
}

// The core regression: the same relative path in two different projects must
// produce DIFFERENT lock keys, so a fork repo's wi can no longer hard-block the
// parent repo's wi over an identical path (the global-routing#1 / ieops#215
// incident).
func TestResourceToLock_SamePathDifferentProjectsDontCollide(t *testing.T) {
	res := DeclaredResourceItem{Type: "path", URI: "file:pkg/gateway/engine.go"}
	_, keyParent := resourceToLock(res, "ieops")
	_, keyFork := resourceToLock(res, "global-routing")
	if keyParent == keyFork {
		t.Fatalf("cross-project keys collided: both %q — fork would still hard-block parent", keyParent)
	}
}

// Same path within the SAME project must still produce the SAME key so two wi's
// touching one file still conflict (no over-loosening).
func TestResourceToLock_SamePathSameProjectStillCollides(t *testing.T) {
	res := DeclaredResourceItem{Type: "path", URI: "file:internal/domain/x.go"}
	_, k1 := resourceToLock(res, "aihub")
	_, k2 := resourceToLock(res, "aihub")
	if k1 != k2 {
		t.Fatalf("same project/path produced different keys %q vs %q", k1, k2)
	}
}

// aihub#416 retired what this test used to assert.
//
// It was TestResourceToLock_BranchAndEnvKeysUnaffectedByProject, and it pinned
// the KEY FORMATS of the two derivations that no longer happen: that a repo
// entry keyed "<repo>/<branch>" ignoring the project, and that a service entry
// keyed the bare service name so cross-project deploys to one environment still
// collided. Both statements are now false, and the second one's REASON — that
// two projects deploying to one environment must conflict — is precisely what
// the de-locking ruling withdrew: exclusion is replaced by the observer's
// generation check, and deploy exclusion moves to the runbook.
//
// It is replaced rather than deleted, because the property it protected still
// exists in the neighbourhood: `project` must not leak into a key it does not
// belong in. That question now has only one type to ask it of, and
// TestFileScopeLockKey_Shape below already pins the answer — so what is kept
// here is the RETIREMENT itself, phrased so this file cannot silently regain a
// project-namespacing bug on a resurrected type.
//
// The behavioural arm lives in lock_derivation_retired_test.go; this one is the
// note that stops a reader of THIS file concluding the old formats still hold.
func TestResourceToLock_NoProjectSensitiveKeyOutsideFileScope(t *testing.T) {
	// Every declared type, both projects, one assertion: the only type that may
	// produce a key at all is file_scope, and it is the only one whose key may
	// differ between projects.
	for _, typ := range []string{"repo", "service", "external_ref"} {
		res := DeclaredResourceItem{Type: typ, URI: typ + ":x", TaskBranch: "polyforge/x"}
		for _, project := range []string{"ieops", "global-routing"} {
			if lt, lk := resourceToLock(res, project); lt != "" || lk != "" {
				t.Errorf("resourceToLock(%s, project=%q) = (%q, %q), want no lock — aihub#416 retired "+
					"the git_branch and deploy_env derivations, so no key format survives to be "+
					"project-sensitive or not", typ, project, lt, lk)
			}
		}
	}
}

// fileScopeLockKey is the single source of the file_scope key shape. Both forms
// are pinned here: the unqualified one is what every row written before
// aihub#261 contains, and the whole no-migration argument rests on the new code
// re-deriving it byte-for-byte from a declaration with no repo.
func TestFileScopeLockKey_Shape(t *testing.T) {
	if got := fileScopeLockKey("aihub", "", "file:a/b.go"); got != "aihub:a/b.go" {
		t.Errorf("fileScopeLockKey(no repo) = %q, want %q", got, "aihub:a/b.go")
	}
	if got := fileScopeLockKey("aihub", "ieops-core", "file:a/b.go"); got != "aihub:ieops-core:a/b.go" {
		t.Errorf("fileScopeLockKey(repo) = %q, want %q", got, "aihub:ieops-core:a/b.go")
	}
}
