package domain

// create_work_item_workflow_mode_db_test.go — the DB-gated half of aihub#720
// slice B: what the create path PERSISTS and what the claim gate refuses.
// The pure combination table and the COMPOSE_FAILED re-typing boundary live in
// create_work_item_workflow_mode_test.go (no database); the pin semantics
// themselves stay where aihub#708 put them (workflow_db_test.go), which this
// file does not duplicate — the arms here cover only what slice B adds: the
// persisted mode and the pending claim refusal.
//
// 🔴 REGISTRATION PENDING: these test functions are AIHUB_TEST_DB-gated and are
// NOT yet listed in internal/citest/dbtestcov/gated_tests.txt — same deferred
// registration as migration_0045_workflow_mode_db_test.go, for the same reason
// (the manifest and the CI selector move in the same final change that lands
// all slices). When registering, add:
//
//	internal/domain TestCreateWorkItemWorkflowMode_LegacyDefaultWithoutSteps
//	internal/domain TestCreateWorkItemWorkflowMode_PendingIsStoredAndUnclaimable
//	internal/domain TestCreateWorkItemWorkflowMode_PersistsModeDbWithSteps
//
//	AIHUB_TEST_DB=postgres://postgres:testpass@localhost:5432/aihub_test?sslmode=disable \
//	GOWORK=off go test ./internal/domain/ -run '^TestCreateWorkItemWorkflowMode_' -count=1 -v

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"

	"github.com/GMISWE/ieops-aihub/internal/skillregistry"
)

// modeOf reads one work item's persisted workflow_mode.
func modeOf(t *testing.T, pool *pgxpool.Pool, id string) string {
	t.Helper()
	var mode string
	require.NoError(t, pool.QueryRow(context.Background(),
		`SELECT workflow_mode FROM work_items WHERE id = $1`, id).Scan(&mode))
	return mode
}

// setupWorkflowModeDB is setupWorkflowDB (0043 + 0044) plus 0045: the mode
// column only exists after the slice-A migration, and the create/claim paths
// below write and read it.
func setupWorkflowModeDB(t *testing.T) *pgxpool.Pool {
	t.Helper()
	pool := setupWorkflowDB(t)
	runMigration(t, pool, "0045_work_items_workflow_mode.sql")
	return pool
}

// TestCreateWorkItemWorkflowMode_PersistsModeDbWithSteps pins the slice-B
// contract for the steps branch: a create that pins generation 1 lands with
// workflow_mode='db' IN THE SAME transaction — so the row and its generation
// are inseparable, and 'db' is never a mode a row reached by default.
func TestCreateWorkItemWorkflowMode_PersistsModeDbWithSteps(t *testing.T) {
	pool := setupWorkflowModeDB(t)
	ctx := context.Background()
	owner := wfUser(t, pool, "owner")
	project := wfProjectName(t)
	wfCleanup(t, pool, project, owner)
	defer wfCleanup(t, pool, project, owner)
	testSeedProject(t, pool, project, owner)
	caller := &UserRecord{ID: owner, Role: "writer"}

	spec := wfSkill(t, ctx, pool, caller, "mode-spec-"+skillSuffix(t), wfContract(skillregistry.CapAuthoring))
	review := wfSkill(t, ctx, pool, caller, "mode-review-"+skillSuffix(t), wfContract(skillregistry.CapReview))
	verify := wfSkill(t, ctx, pool, caller, "mode-verify-"+skillSuffix(t), wfContract(skillregistry.CapVerification))

	// Omitted mode + steps: db.
	wi, aerr := wfCreate(t, ctx, pool, project, caller, owner, true, wfFlowSpec(spec, review, verify))
	require.Nil(t, aerr)
	require.Equal(t, "db", modeOf(t, pool, wi.ID),
		"a create that pinned generation 1 must land workflow_mode='db' in the same transaction")

	// Explicit workflow_mode="db" + steps: same row, accepted.
	rhs := true
	wi2, aerr := CreateWorkItem(ctx, pool, &CreateWorkItemRequest{
		Project: project, Goal: "mode explicit db " + wfHash(project)[:16], Source: "human",
		RequiresHumanSession: &rhs, WorkflowMode: "db",
		Steps: wfFlowSpec(spec, review, verify), RegistryCaller: caller,
	}, owner, owner, nil, "writer")
	require.Nil(t, aerr)
	require.Equal(t, "db", modeOf(t, pool, wi2.ID))
}

// TestCreateWorkItemWorkflowMode_LegacyDefaultWithoutSteps pins the
// no-steps half: a create without steps and without an explicit mode is
// byte-identical to pre-aihub#720 semantics — the row is queued, claimable, and
// explicitly 'legacy', never NULL and never a mode the caller has to infer.
func TestCreateWorkItemWorkflowMode_LegacyDefaultWithoutSteps(t *testing.T) {
	pool := setupWorkflowModeDB(t)
	ctx := context.Background()
	owner := wfUser(t, pool, "owner")
	project := wfProjectName(t)
	wfCleanup(t, pool, project, owner)
	defer wfCleanup(t, pool, project, owner)
	testSeedProject(t, pool, project, owner)

	wi, aerr := CreateWorkItem(ctx, pool, &CreateWorkItemRequest{
		Project: project, Goal: "mode legacy default " + wfHash(project)[:16], Source: "human",
	}, owner, owner, nil, "writer")
	require.Nil(t, aerr)
	require.Equal(t, "legacy", modeOf(t, pool, wi.ID))
	require.Equal(t, "queued", wi.Status, "a legacy-mode create is an ordinary claimable work item")
}

// TestCreateWorkItemWorkflowMode_PendingIsStoredAndUnclaimable pins the
// orchestration mode: a pending work item exists, stays queued with no
// workflow, and REFUSES a claim with 409 COMPOSE_PENDING — the refusal that
// keeps the legacy scenario dispatch from silently adopting a work item that
// was filed for DB composition.
func TestCreateWorkItemWorkflowMode_PendingIsStoredAndUnclaimable(t *testing.T) {
	pool := setupWorkflowModeDB(t)
	ctx := context.Background()
	owner := wfUser(t, pool, "owner")
	project := wfProjectName(t)
	wfCleanup(t, pool, project, owner)
	defer wfCleanup(t, pool, project, owner)
	testSeedProject(t, pool, project, owner)

	wi, aerr := CreateWorkItem(ctx, pool, &CreateWorkItemRequest{
		Project: project, Goal: "mode pending " + wfHash(project)[:16], Source: "human",
		WorkflowMode: "pending",
	}, owner, owner, nil, "writer")
	require.Nil(t, aerr)
	require.Equal(t, "pending", modeOf(t, pool, wi.ID))
	require.Equal(t, "queued", wi.Status)

	// The claim refusal. Idempotency key is UNIQUE per (work item, key), so a
	// re-run of this test against a shared database must not collide: derive it
	// from the work item's own id.
	_, aerr = FnClaimWorkItem(ctx, pool, wi.ID, &ClaimRequest{
		IdempotencyKey: "mode-pending-" + wfHash(wi.ID)[:20],
		SessionInfo:    SessionInfo{MachineID: "mode-probe", SessionSecret: "m0de-s3cret"},
	}, owner, "", owner)
	require.NotNil(t, aerr, "a workflow_mode=pending work item must not be claimable")
	require.Equal(t, ErrConflictComposePending, aerr.Code)
	require.Equal(t, 409, aerr.HTTPStatus)

	// The refusal is total: no attempt exists, the row is untouched, and the
	// work item is still queued and still pending — the recovery is a workflow
	// revision (slice G's pilot), not a re-claim.
	var attempts int
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT count(*) FROM run_attempts WHERE work_item_id = $1`, wi.ID).Scan(&attempts))
	require.Equal(t, 0, attempts, "the refused claim must leave no attempt behind")
	require.Equal(t, "pending", modeOf(t, pool, wi.ID))
}

// wfTypeFeature is a takeable address for the *string WIType field; a literal
// pointer helper keeps the create request one expression.
var wfTypeFeature = "feature"

// TestCreateWorkItemWorkflowMode_PendingFlipsToDbOnFirstPin closes the composer
// deadlock the first draft of slice B left: claim refuses workflow_mode=pending
// unconditionally, but UpdateWorkItemWorkflow's pointer UPDATE originally did
// not write workflow_mode — so the documented recovery ("pin a first workflow
// generation and claim again") would have left the work item pending forever,
// with a pinned flow nothing could ever claim. The pin path must flip the mode
// to db in the same transaction that moves the pointer.
//
// RED against the pre-fix UPDATE (steps/steps_version only): the pin succeeds,
// the mode stays pending, and the claim below still answers COMPOSE_PENDING.
func TestCreateWorkItemWorkflowMode_PendingFlipsToDbOnFirstPin(t *testing.T) {
	pool := setupWorkflowModeDB(t)
	ctx := context.Background()
	owner := wfUser(t, pool, "owner")
	project := wfProjectName(t)
	wfCleanup(t, pool, project, owner)
	defer wfCleanup(t, pool, project, owner)
	testSeedProject(t, pool, project, owner)
	caller := &UserRecord{ID: owner, Role: "writer"}

	spec := wfSkill(t, ctx, pool, caller, "flip-spec-"+skillSuffix(t), wfContract(skillregistry.CapAuthoring))
	review := wfSkill(t, ctx, pool, caller, "flip-review-"+skillSuffix(t), wfContract(skillregistry.CapReview))
	verify := wfSkill(t, ctx, pool, caller, "flip-verify-"+skillSuffix(t), wfContract(skillregistry.CapVerification))

	wi, aerr := CreateWorkItem(ctx, pool, &CreateWorkItemRequest{
		Project: project, Goal: "flip test " + wfHash(project)[:16], Source: "human",
		WIType: &wfTypeFeature, WorkflowMode: "pending",
	}, owner, owner, nil, "writer")
	require.Nil(t, aerr)
	require.Equal(t, "pending", modeOf(t, pool, wi.ID))

	// The documented recovery: pin the first generation.
	rhs := false
	_, aerr = UpdateWorkItemWorkflow(ctx, pool, wi.ID, caller, owner, "writer", nil,
		UpdateWorkItemWorkflowRequest{ExpectedStepsVersion: 0, RequiresHumanSession: &rhs,
			Steps: wfFlowSpec(spec, review, verify)})
	require.Nil(t, aerr)
	require.Equal(t, "db", modeOf(t, pool, wi.ID),
		"a successful first pin must flip workflow_mode from pending to db in the same transaction")

	// And the claim gate that refused pending must no longer refuse.
	_, aerr = FnClaimWorkItem(ctx, pool, wi.ID, &ClaimRequest{
		IdempotencyKey: "mode-flip-" + wfHash(wi.ID)[:20],
		SessionInfo:    SessionInfo{MachineID: "mode-flip-probe", SessionSecret: "fl1p-s3cret"},
	}, owner, "", owner)
	require.Nil(t, aerr, "after the first pin a pending-origin work item must be claimable")
}
