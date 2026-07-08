package domain

// aihub#222 integration test. Two wi's in DIFFERENT projects that declare the
// SAME relative path (as a fork repo and its parent do — their paths are
// byte-identical) must NOT hard-block each other, because file_scope lock keys
// are now namespaced by project ("<project>:<path>"). Before the fix the bare
// path was the whole key, so the fork's lock (global-routing#1) hard-blocked the
// parent (ieops#215). Gated on AIHUB_TEST_DB like the other integration tests here.
//
//	AIHUB_TEST_DB=postgres://.../aihub_test?sslmode=disable \
//	go test ./internal/domain/ -run TestCrossProjectSamePath -v -count=1

import (
	"context"
	"encoding/json"
	"testing"
)

func TestCrossProjectSamePath_NoConflict(t *testing.T) {
	pool := setupLatestTestDB(t)
	ctx := context.Background()

	uid := testUser(t, pool)
	projParent := "x222cp_parent"
	projFork := "x222cp_fork"

	for _, p := range []string{projParent, projFork} {
		mustExec(t, pool, `INSERT INTO projects(name,owner_user_id) VALUES('`+p+`','`+uid+`') ON CONFLICT (name) DO NOTHING`)
	}
	// Drop any locks left by a prior run of this test so a stale row can't
	// masquerade as a real cross-project collision.
	mustExec(t, pool, `DELETE FROM resource_locks rl USING run_attempts ra, work_items wi
		WHERE rl.owner_attempt_id = ra.id AND ra.work_item_id = wi.id
		  AND wi.project IN ('`+projParent+`','`+projFork+`')`)

	// The SAME relative path in both projects, exactly as a fork and its parent have.
	sharedPath := "file:pkg/gateway/engine.go"
	declared, err := json.Marshal([]DeclaredResourceItem{{Type: "path", URI: sharedPath, Intent: "write"}})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	wiType := "fix_bug"
	mkWI := func(proj, goal string) string {
		wi, aerr := CreateWorkItem(ctx, pool, &CreateWorkItemRequest{
			Project: proj, Goal: goal, Scenario: "coding", WIType: &wiType,
			DeclaredResources: declared, Source: "human",
			ForceCreate: true, ForceReason: "aihub#222 cross-project regression",
		}, uid, "tester")
		if aerr != nil {
			t.Fatalf("CreateWorkItem(%s): %v", proj, aerr)
		}
		return wi.ID
	}
	wiParent := mkWI(projParent, "parent repo wi")
	wiFork := mkWI(projFork, "fork repo wi (same path, different project)")

	sessSecret := "s3cr3t-0123456789abcdef0123456789abcdef0123456789abcdef01234567"
	claimReq := func(idem string) *ClaimRequest {
		return &ClaimRequest{
			IdempotencyKey: idem,
			SessionInfo:    SessionInfo{MachineID: "m1", SessionSecret: sessSecret},
			Mode:           "fresh",
		}
	}

	claimParent, aerr := FnClaimWorkItem(ctx, pool, wiParent, claimReq("idem-parent"), uid, "", "tester")
	if aerr != nil {
		t.Fatalf("claim parent wi: %v", aerr)
	}
	if len(claimParent.AcquiredLocks) == 0 {
		t.Fatalf("claim parent acquired no locks; declared_resources mapping may be broken")
	}

	// The fork wi is in a DIFFERENT project with the same path. Before aihub#222
	// this 409'd with ErrConflictLockTaken (bare-path key collided). After the fix
	// the keys are "<project>:<path>" and differ, so this must succeed.
	claimFork, aerr := FnClaimWorkItem(ctx, pool, wiFork, claimReq("idem-fork"), uid, "", "tester")
	if aerr != nil {
		if aerr.Code == ErrConflictLockTaken {
			t.Fatalf("cross-project same-path wi incorrectly 409'd (fork hard-blocked parent): %v", aerr)
		}
		t.Fatalf("claim fork wi: %v", aerr)
	}
	if len(claimFork.AcquiredLocks) == 0 {
		t.Fatalf("claim fork acquired no locks")
	}

	// The two acquired file_scope keys must be project-namespaced and distinct.
	kParent := claimParent.AcquiredLocks[0].ResourceKey
	kFork := claimFork.AcquiredLocks[0].ResourceKey
	if kParent == kFork {
		t.Errorf("cross-project lock keys should differ, both = %q", kParent)
	}
	if want := projParent + ":pkg/gateway/engine.go"; kParent != want {
		t.Errorf("parent lock key = %q, want %q", kParent, want)
	}
	if want := projFork + ":pkg/gateway/engine.go"; kFork != want {
		t.Errorf("fork lock key = %q, want %q", kFork, want)
	}
}
