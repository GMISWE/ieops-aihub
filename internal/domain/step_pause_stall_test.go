package domain

// Integration tests for aihub#206 (C1, spec A-1): fields the client sends but
// the server used to silently drop. Follows the AIHUB_TEST_DB gating pattern
// from memory_vector_integration_test.go / memory_latest_test.go.
//
//	AIHUB_TEST_DB=postgres://postgres:test@localhost:15440/aihub_test?sslmode=disable \
//	go test ./internal/domain/ -run 'TestPauseReason|TestMigration0027' -v -count=1

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// seedRunAttempt inserts a run_attempts row directly (bypassing FnClaimWorkItem's
// full claim ceremony, which needs locks/idempotency plumbing not relevant here)
// and points work_items.current_attempt_id at it, so FnCompleteAttempt's
// verifyAttemptCredential + FOR UPDATE lookup succeed. sessionSecret is the
// plaintext; the row stores its hash, matching what FnClaimWorkItem would do.
func seedRunAttempt(t *testing.T, pool *pgxpool.Pool, wiID, userID, sessionSecret string) string {
	t.Helper()
	attemptID := NewID("ra")
	_, err := pool.Exec(context.Background(), `
		INSERT INTO run_attempts (id, work_item_id, status, claim_epoch, idempotency_key,
			actor_user_id, actor_display, machine_id, session_secret_hash)
		VALUES ($1, $2, 'running', 1, $3, $4, $4, 'm_test', $5)`,
		attemptID, wiID, "idem_"+attemptID, userID, HashSecret(sessionSecret))
	require.NoError(t, err)
	_, err = pool.Exec(context.Background(), `
		UPDATE work_items SET status='running', current_attempt_id=$1, current_attempt_epoch=1 WHERE id=$2`,
		attemptID, wiID)
	require.NoError(t, err)
	return attemptID
}

// seedWI creates a minimal work item directly via CreateWorkItem (real path,
// not a raw INSERT) so all NOT NULL / CHECK columns are populated correctly.
// Unlike memories, CreateWorkItem runs a goal-similarity dedup check against
// existing queued/running/paused/blocked work items in the same project — a
// leftover wi from a prior run of this same test (same deterministic project
// name, same goal string) would otherwise be seen as 100% similar and reject
// the new create. Clear any prior run's work items (and FK-dependent rows)
// for this project first, in child-to-parent order.
func seedWI(t *testing.T, pool *pgxpool.Pool, project, userID string) *WorkItem {
	t.Helper()
	mustExec(t, pool, `DELETE FROM wi_step_completions WHERE work_item_id IN (SELECT id FROM work_items WHERE project='`+project+`')`)
	mustExec(t, pool, `DELETE FROM agent_events WHERE work_item_id IN (SELECT id FROM work_items WHERE project='`+project+`')`)
	mustExec(t, pool, `UPDATE work_items SET current_attempt_id=NULL WHERE project='`+project+`'`)
	mustExec(t, pool, `DELETE FROM run_attempts WHERE work_item_id IN (SELECT id FROM work_items WHERE project='`+project+`')`)
	mustExec(t, pool, `DELETE FROM work_items WHERE project='`+project+`'`)
	wi, aerr := CreateWorkItem(context.Background(), pool, &CreateWorkItemRequest{
		Project: project,
		Goal:    "seed wi for " + t.Name(),
		Source:  "human",
	}, userID, userID)
	require.Nil(t, aerr)
	return wi
}

// TestPauseReasonSurfacedInReadyQueue is the AC-2 regression test: completing
// an attempt with status=paused and a pause_reason must (a) persist onto the
// run_attempts row and (b) be exposed on the PausedItem in GetReadyQueue's
// paused[] segment — the field aihub#206 found silently dropped.
func TestPauseReasonSurfacedInReadyQueue(t *testing.T) {
	pool := setupLatestTestDB(t)
	u := testUser(t, pool)
	project := testProject(t, pool, u)

	wi := seedWI(t, pool, project, u)
	attemptID := seedRunAttempt(t, pool, wi.ID, u, "secret123")

	const reason = "waiting on design review"
	aerr := FnCompleteAttempt(context.Background(), pool, wi.ID, &CompleteAttemptRequest{
		AttemptID:     attemptID,
		ClaimEpoch:    1,
		SessionSecret: "secret123",
		Status:        "paused",
		PauseReason:   strp(reason),
	})
	require.Nil(t, aerr)

	// (a) persisted on run_attempts.
	var gotReason *string
	err := pool.QueryRow(context.Background(), `SELECT pause_reason FROM run_attempts WHERE id=$1`, attemptID).Scan(&gotReason)
	require.NoError(t, err)
	require.NotNil(t, gotReason)
	assert.Equal(t, reason, *gotReason)

	// (b) surfaced via the ready-queue paused[] segment.
	rq, aerr := GetReadyQueue(context.Background(), pool, project, 50)
	require.Nil(t, aerr)
	require.Len(t, rq.Paused, 1, "expected exactly one paused item")
	require.NotNil(t, rq.Paused[0].PauseReason)
	assert.Equal(t, reason, *rq.Paused[0].PauseReason)
	assert.Equal(t, wi.ID, rq.Paused[0].ID)
}

// TestPauseReasonOmittedWhenNil is a companion check: wrapping/failing an
// attempt with no pause_reason must leave the column NULL, not write an
// empty string or panic on the nil pointer arg.
func TestPauseReasonOmittedWhenNil(t *testing.T) {
	pool := setupLatestTestDB(t)
	u := testUser(t, pool)
	project := testProject(t, pool, u)

	wi := seedWI(t, pool, project, u)
	attemptID := seedRunAttempt(t, pool, wi.ID, u, "secret456")

	aerr := FnCompleteAttempt(context.Background(), pool, wi.ID, &CompleteAttemptRequest{
		AttemptID:     attemptID,
		ClaimEpoch:    1,
		SessionSecret: "secret456",
		Status:        "wrapped",
	})
	require.Nil(t, aerr)

	var gotReason *string
	err := pool.QueryRow(context.Background(), `SELECT pause_reason FROM run_attempts WHERE id=$1`, attemptID).Scan(&gotReason)
	require.NoError(t, err)
	assert.Nil(t, gotReason)
}

// TestMigration0027_UpDown verifies the pause_reason migration applies and
// reverts cleanly (idempotent ADD/DROP COLUMN IF EXISTS), independent of the
// already-migrated test DB schema (setupLatestTestDB's DB has already run
// 0027 as part of the full migration set, so this test re-applies Up
// (no-op via IF NOT EXISTS), then exercises Down+Up explicitly).
func TestMigration0027_UpDown(t *testing.T) {
	pool := setupLatestTestDB(t)

	columnExists := func() bool {
		var exists bool
		err := pool.QueryRow(context.Background(), `
			SELECT EXISTS (
				SELECT 1 FROM information_schema.columns
				WHERE table_name='run_attempts' AND column_name='pause_reason'
			)`).Scan(&exists)
		require.NoError(t, err)
		return exists
	}

	require.True(t, columnExists(), "precondition: pause_reason should already exist from the full migration set")

	_, err := pool.Exec(context.Background(), `ALTER TABLE run_attempts DROP COLUMN IF EXISTS pause_reason`)
	require.NoError(t, err)
	assert.False(t, columnExists(), "Down: column must be gone")

	runMigration(t, pool, "0027_run_attempts_pause_reason.sql")
	assert.True(t, columnExists(), "Up: column must be restored")
}

// seedBlockedWI creates a work item with the given goal and forces it to
// status='blocked' (bypassing CreateWorkItem's dedup and the normal
// paused/failed transitions — the GC sweep only reads status + dependency
// rows, so a direct UPDATE is sufficient and keeps the seed minimal).
func seedBlockedWI(t *testing.T, pool *pgxpool.Pool, project, userID, goal string) *WorkItem {
	t.Helper()
	wi, aerr := CreateWorkItem(context.Background(), pool, &CreateWorkItemRequest{
		Project: project, Goal: goal, Source: "human",
	}, userID, userID)
	require.Nil(t, aerr)
	mustExec(t, pool, `UPDATE work_items SET status='blocked' WHERE id='`+wi.ID+`'`)
	return wi
}

// TestRunUnblockDependentWI_StalledStaysBlocked is the AC-3 GC regression test
// for aihub#206: the dependency-unblock sweep must requeue only wis that were
// actually dependency-blocked (have a 'blocks' dep row whose blockers are now
// terminal), and must leave escalated-stalled wis (blocked, no dep row) alone.
// Without the EXISTS guard the stalled wi would be auto-requeued within 60s.
func TestRunUnblockDependentWI_StalledStaysBlocked(t *testing.T) {
	pool := setupLatestTestDB(t)
	u := testUser(t, pool)
	project := testProject(t, pool, u)
	// Fresh project state so the sweep only sees this test's wis (child-to-parent
	// order to satisfy FKs; makes re-runs self-healing).
	mustExec(t, pool, `DELETE FROM wi_dependencies WHERE blocked_wi_id IN (SELECT id FROM work_items WHERE project='`+project+`') OR blocking_wi_id IN (SELECT id FROM work_items WHERE project='`+project+`')`)
	mustExec(t, pool, `DELETE FROM agent_events WHERE work_item_id IN (SELECT id FROM work_items WHERE project='`+project+`')`)
	mustExec(t, pool, `UPDATE work_items SET current_attempt_id=NULL WHERE project='`+project+`'`)
	mustExec(t, pool, `DELETE FROM run_attempts WHERE work_item_id IN (SELECT id FROM work_items WHERE project='`+project+`')`)
	mustExec(t, pool, `DELETE FROM work_items WHERE project='`+project+`'`)

	// (a) escalated-stalled wi: blocked, has a wi_stalled event, NO dep row.
	// Goals are kept mutually dissimilar so CreateWorkItem's soft-dedup
	// (>=0.65 similarity => 409 candidates) does not reject the later seeds.
	stalled := seedBlockedWI(t, pool, project, u, "escalated agent gave up on payment retries")
	mustExec(t, pool, `
		INSERT INTO agent_events (id, work_item_id, actor_user_id, event_type, payload, project)
		VALUES ('`+NewID("evt")+`', '`+stalled.ID+`', '`+u+`', 'wi_stalled',
			'{"stall_reason":"compile_error"}'::jsonb, '`+project+`')`)

	// (b) dependency-blocked wi: blocked, has a 'blocks' dep row whose only
	//     blocker is now terminal (wrapped).
	blocker := seedBlockedWI(t, pool, project, u, "migrate the billing schema to v3 tables")
	mustExec(t, pool, `UPDATE work_items SET status='wrapped' WHERE id='`+blocker.ID+`'`)
	depBlocked := seedBlockedWI(t, pool, project, u, "wire up invoice export button in the console")
	mustExec(t, pool, `
		INSERT INTO wi_dependencies (blocked_wi_id, blocking_wi_id, kind, created_by)
		VALUES ('`+depBlocked.ID+`', '`+blocker.ID+`', 'blocks', '`+u+`')`)

	res := RunUnblockDependentWI(context.Background(), pool)
	require.Empty(t, res.Error)
	require.False(t, res.Skipped, "advisory lock should be acquired in a single-test run")

	wiStatus := func(id string) string {
		var s string
		require.NoError(t, pool.QueryRow(context.Background(), `SELECT status FROM work_items WHERE id=$1`, id).Scan(&s))
		return s
	}
	assert.Equal(t, "blocked", wiStatus(stalled.ID), "stalled wi must stay blocked (no dep row)")
	assert.Equal(t, "queued", wiStatus(depBlocked.ID), "dependency-blocked wi must be requeued once blocker is terminal")
}
