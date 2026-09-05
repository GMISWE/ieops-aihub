package domain

// aihub#222 integration test for the predict path: PredictConflicts must scope
// file_scope overlap (Rule 3) by project, resolving the project from either the
// wi (work_item_id) or the explicit req.Project fallback. A same-project overlap
// must be flagged; a same-path-different-project one must NOT. Gated on
// AIHUB_TEST_DB like the other integration tests here.
//
//	AIHUB_TEST_DB=postgres://.../aihub_test?sslmode=disable \
//	go test ./internal/domain/ -run TestPredictConflicts_FileScopeProjectScoped -v -count=1

import (
	"context"
	"encoding/json"
	"testing"
)

func TestPredictConflicts_FileScopeProjectScoped(t *testing.T) {
	pool := setupLatestTestDB(t)
	ctx := context.Background()

	uid := testUser(t, pool)
	projHolder := "x222pp_holder"
	projOther := "x222pp_other"
	for _, p := range []string{projHolder, projOther} {
		mustExec(t, pool, `INSERT INTO projects(name,owner_user_id) VALUES('`+p+`','`+uid+`') ON CONFLICT (name) DO NOTHING`)
	}
	mustExec(t, pool, `DELETE FROM resource_locks rl USING run_attempts ra, work_items wi
		WHERE rl.owner_attempt_id = ra.id AND ra.work_item_id = wi.id
		  AND wi.project IN ('`+projHolder+`','`+projOther+`')`)

	sharedPath := "file:pkg/gateway/engine.go"
	declared, err := json.Marshal([]DeclaredResourceItem{{Type: "path", URI: sharedPath, Intent: "write"}})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	// A running holder in projHolder that owns the file_scope lock projHolder:path.
	wiType := "fix_bug"
	holder, aerr := CreateWorkItem(ctx, pool, &CreateWorkItemRequest{
		Project: projHolder, Goal: "holds the file lock", Scenario: "coding", WIType: &wiType,
		DeclaredResources: declared, Source: "human", ForceCreate: true, ForceReason: "aihub#222 predict regression",
	}, uid, "tester", nil, "")
	if aerr != nil {
		t.Fatalf("CreateWorkItem holder: %v", aerr)
	}
	claim, aerr := FnClaimWorkItem(ctx, pool, holder.ID,
		&ClaimRequest{IdempotencyKey: "idem-holder", SessionInfo: SessionInfo{MachineID: "m1", SessionSecret: "s3cr3t-0123456789abcdef0123456789abcdef0123456789abcdef01234567"}, Mode: "fresh"},
		uid, "", "tester")
	if aerr != nil {
		t.Fatalf("claim holder: %v", aerr)
	}
	if len(claim.AcquiredLocks) == 0 {
		t.Fatalf("holder acquired no locks")
	}

	roles := map[string]string{projHolder: "owner", projOther: "owner"}
	hasFileScopePrediction := func(resp *PredictConflictsResponse) bool {
		for _, p := range resp.Predictions {
			if p.ResourceType == "file_scope" {
				return true
			}
		}
		return false
	}

	// Same project (via req.Project fallback — the newly-wired path): the candidate
	// key is projHolder:path, which overlaps the holder's lock -> Rule 3 soft_block.
	sameProj, aerr := PredictConflicts(ctx, pool, &PredictConflictsRequest{
		Project: projHolder, DeclaredResources: declared, DryRun: true,
	}, roles)
	if aerr != nil {
		t.Fatalf("predict same-project: %v", aerr)
	}
	if !hasFileScopePrediction(sameProj) {
		t.Errorf("same-project predict should flag a file_scope overlap, got %+v", sameProj.Predictions)
	}

	// Different project, same path: candidate key is projOther:path, which does NOT
	// overlap projHolder:path -> no file_scope prediction (the fix).
	diffProj, aerr := PredictConflicts(ctx, pool, &PredictConflictsRequest{
		Project: projOther, DeclaredResources: declared, DryRun: true,
	}, roles)
	if aerr != nil {
		t.Fatalf("predict diff-project: %v", aerr)
	}
	if hasFileScopePrediction(diffProj) {
		t.Errorf("cross-project predict must NOT flag a file_scope overlap, got %+v", diffProj.Predictions)
	}

	// Project resolved from work_item_id (a projOther wi) must behave like projOther:
	// no cross-project overlap even though req.Project is unset.
	otherWI, aerr := CreateWorkItem(ctx, pool, &CreateWorkItemRequest{
		Project: projOther, Goal: "different project, same path", Scenario: "coding", WIType: &wiType,
		DeclaredResources: declared, Source: "human", ForceCreate: true, ForceReason: "aihub#222 predict regression",
	}, uid, "tester", nil, "")
	if aerr != nil {
		t.Fatalf("CreateWorkItem otherWI: %v", aerr)
	}
	byWI, aerr := PredictConflicts(ctx, pool, &PredictConflictsRequest{
		WorkItemID: &otherWI.ID, DeclaredResources: declared, DryRun: true,
	}, roles)
	if aerr != nil {
		t.Fatalf("predict by work_item_id: %v", aerr)
	}
	if hasFileScopePrediction(byWI) {
		t.Errorf("predict by work_item_id (projOther) must NOT flag a cross-project overlap, got %+v", byWI.Predictions)
	}
}
