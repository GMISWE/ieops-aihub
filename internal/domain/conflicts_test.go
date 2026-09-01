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

// git_branch and deploy_env keys must NOT be namespaced by project: git_branch
// is already repo-qualified (repo/branch) and deploy_env (service) is
// intentionally global so cross-project deploys to one environment still conflict.
func TestResourceToLock_BranchAndEnvKeysUnaffectedByProject(t *testing.T) {
	_, branchA := resourceToLock(DeclaredResourceItem{Type: "repo", URI: "repo:ieops-v2", TaskBranch: "polyforge/x"}, "ieops")
	_, branchB := resourceToLock(DeclaredResourceItem{Type: "repo", URI: "repo:ieops-v2", TaskBranch: "polyforge/x"}, "global-routing")
	if branchA != branchB {
		t.Errorf("git_branch key changed with project: %q vs %q (must be repo-qualified only)", branchA, branchB)
	}
	if want := "ieops-v2/polyforge/x"; branchA != want {
		t.Errorf("git_branch key = %q, want %q", branchA, want)
	}

	_, envA := resourceToLock(DeclaredResourceItem{Type: "service", URI: "service:tot"}, "ieops")
	_, envB := resourceToLock(DeclaredResourceItem{Type: "service", URI: "service:tot"}, "aihub")
	if envA != envB {
		t.Errorf("deploy_env key changed with project: %q vs %q (cross-project deploy must still conflict)", envA, envB)
	}
	if want := "tot"; envA != want {
		t.Errorf("deploy_env key = %q, want %q", envA, want)
	}
}

// fileScopeLockKey is the single source of the "<project>:<path>" shape.
func TestFileScopeLockKey_Shape(t *testing.T) {
	if got := fileScopeLockKey("aihub", "file:a/b.go"); got != "aihub:a/b.go" {
		t.Errorf("fileScopeLockKey = %q, want %q", got, "aihub:a/b.go")
	}
}
