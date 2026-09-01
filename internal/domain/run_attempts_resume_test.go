package domain

// Integration test for aihub#207 Change 3: resuming a wi that declares
// resource locks must not 409 against its OWN prior (paused) attempt's
// still-held locks. Follows the AIHUB_TEST_DB gating pattern from
// memory_vector_integration_test.go / memory_latest_test.go: skipped unless
// AIHUB_TEST_DB is set, so it never runs in plain `go test ./...`.
//
//	AIHUB_TEST_DB=postgres://postgres:test@localhost:5440/aihub_test?sslmode=disable \
//	go test ./internal/domain/ -run TestResumeOwnLocks -v -count=1

import (
	"context"
	"encoding/json"
	"testing"
)

// TestResumeOwnLocks_NoSelfConflict claims a wi that declares a path resource
// (-> a file_scope lock), pauses it, then claims it again (simulating resume).
// Before the Change 3 fix, the lock-conflict advisory SELECT in
// FnClaimWorkItem found the wi's own paused attempt still holding the lock
// and returned ErrConflictLockTaken. After the fix (`AND ra.work_item_id !=
// $3`), the wi's own attempts are excluded from the conflict scan.
func TestResumeOwnLocks_NoSelfConflict(t *testing.T) {
	pool := setupLatestTestDB(t)
	ctx := context.Background()

	uid := testUser(t, pool)
	proj := testProject(t, pool, uid)

	// Scope the locked path to this test's project name so that OTHER tests
	// sharing the database cannot contend for the same file_scope lock.
	//
	// It buys nothing against this test's own previous runs, and an earlier
	// version of this comment claimed the opposite ("a stale lock left behind by
	// a prior failed/killed run of this same test can never collide"). That was
	// backwards: proj is derived from t.Name(), so a prior run produced the
	// SAME key — the determinism is precisely what let residue collide, and
	// this was one of only two non-idempotent tests in the suite (aihub#303).
	// What actually makes a re-run safe is testProject's reset, which clears
	// this project's work_items / run_attempts / resource_locks first.
	lockPath := "file:internal/domain/" + proj + "_run_attempts.go"
	declared, err := json.Marshal([]DeclaredResourceItem{
		{Type: "path", URI: lockPath, Intent: "write"},
	})
	if err != nil {
		t.Fatalf("marshal declared_resources: %v", err)
	}

	wiType := "fix_bug"
	created, aerr := CreateWorkItem(ctx, pool, &CreateWorkItemRequest{
		Project:           proj,
		Goal:              "resume lock self-conflict regression",
		Scenario:          "coding",
		WIType:            &wiType,
		DeclaredResources: declared,
		Source:            "human",
		ForceCreate:       true,
		ForceReason:       "regression test, force past dedup",
	}, uid, "tester")
	if aerr != nil {
		t.Fatalf("CreateWorkItem: %v", aerr)
	}

	claimReq := func(idemKey string) *ClaimRequest {
		return &ClaimRequest{
			IdempotencyKey: idemKey,
			SessionInfo: SessionInfo{
				MachineID:     "m1",
				SessionSecret: "s3cr3t-0123456789abcdef0123456789abcdef0123456789abcdef01234567",
			},
			Mode: "fresh",
		}
	}

	// First claim: acquires the file_scope lock derived from declared_resources.
	claim1, aerr := FnClaimWorkItem(ctx, pool, created.ID, claimReq("idem-1"), uid, "", "tester")
	if aerr != nil {
		t.Fatalf("first claim: %v", aerr)
	}
	if len(claim1.AcquiredLocks) == 0 {
		t.Fatalf("first claim acquired no locks; declared_resources mapping may be broken")
	}

	// Pause: FnCompleteAttempt(status=paused) only releases file_scope locks
	// acquired MID-ATTEMPT via pf_acquire_locks (acquireLocksReleasePausedSQL).
	// Locks derived from declared_resources at CLAIM time are untouched by
	// pause, so this attempt keeps holding the file_scope lock while paused
	// (status IN ('running','paused') keeps it "conflicting" to everyone else).
	if aerr := FnCompleteAttempt(ctx, pool, created.ID, &CompleteAttemptRequest{
		AttemptID:     claim1.AttemptID,
		ClaimEpoch:    claim1.ClaimEpoch,
		SessionSecret: "s3cr3t-0123456789abcdef0123456789abcdef0123456789abcdef01234567",
		Status:        "paused",
	}); aerr != nil {
		t.Fatalf("pause: %v", aerr)
	}

	// Resume: claim the SAME wi again. Before the fix this 409s with
	// ErrConflictLockTaken because the paused attempt from claim1 still holds
	// the file_scope lock and the conflict SELECT didn't exclude "this wi".
	claim2, aerr := FnClaimWorkItem(ctx, pool, created.ID, claimReq("idem-2"), uid, "", "tester")
	if aerr != nil {
		if aerr.Code == ErrConflictLockTaken {
			t.Fatalf("resume incorrectly 409'd against its own prior attempt's locks: %v", aerr)
		}
		t.Fatalf("resume claim: %v", aerr)
	}
	if claim2.AttemptID == claim1.AttemptID {
		t.Errorf("resume should create a new attempt, got same attempt id %s", claim2.AttemptID)
	}
}

// TestResumeOwnLocks_DifferentWIStillConflicts is the regression guard: a
// genuinely different wi holding the same file_scope lock must still 409.
// Without this, a broken fix (e.g. removing the predicate entirely instead of
// scoping it to "not this wi") would silently let cross-wi collisions through.
func TestResumeOwnLocks_DifferentWIStillConflicts(t *testing.T) {
	pool := setupLatestTestDB(t)
	ctx := context.Background()

	uid := testUser(t, pool)
	proj := testProject(t, pool, uid)

	// Scope the locked path to this test's project name (see the NoSelfConflict
	// test above: this isolates the lock from OTHER tests, not from this test's
	// own previous runs — testProject's reset is what handles those).
	lockPath := "file:internal/domain/" + proj + "_shared_conflict_target.go"
	declared, err := json.Marshal([]DeclaredResourceItem{
		{Type: "path", URI: lockPath, Intent: "write"},
	})
	if err != nil {
		t.Fatalf("marshal declared_resources: %v", err)
	}

	wiType := "fix_bug"
	wiA, aerr := CreateWorkItem(ctx, pool, &CreateWorkItemRequest{
		Project: proj, Goal: "wi A holds the lock", Scenario: "coding",
		WIType: &wiType, DeclaredResources: declared, Source: "human", ForceCreate: true, ForceReason: "regression test, force past dedup",
	}, uid, "tester")
	if aerr != nil {
		t.Fatalf("CreateWorkItem A: %v", aerr)
	}
	wiB, aerr := CreateWorkItem(ctx, pool, &CreateWorkItemRequest{
		Project: proj, Goal: "wi B wants the same lock", Scenario: "coding",
		WIType: &wiType, DeclaredResources: declared, Source: "human", ForceCreate: true, ForceReason: "regression test, force past dedup",
	}, uid, "tester")
	if aerr != nil {
		t.Fatalf("CreateWorkItem B: %v", aerr)
	}

	sessSecret := "s3cr3t-0123456789abcdef0123456789abcdef0123456789abcdef01234567"
	claimReq := func(idemKey string) *ClaimRequest {
		return &ClaimRequest{
			IdempotencyKey: idemKey,
			SessionInfo:    SessionInfo{MachineID: "m1", SessionSecret: sessSecret},
			Mode:           "fresh",
		}
	}

	if _, aerr := FnClaimWorkItem(ctx, pool, wiA.ID, claimReq("idem-a"), uid, "", "tester"); aerr != nil {
		t.Fatalf("claim wiA: %v", aerr)
	}

	_, aerr = FnClaimWorkItem(ctx, pool, wiB.ID, claimReq("idem-b"), uid, "", "tester")
	if aerr == nil {
		t.Fatalf("claim wiB: expected ErrConflictLockTaken (wiA holds the same file_scope lock), got success")
	}
	if aerr.Code != ErrConflictLockTaken {
		t.Fatalf("claim wiB: got error %v, want ErrConflictLockTaken", aerr)
	}
}
