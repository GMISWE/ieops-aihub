package domain

// DB-gated integration test for aihub#242 Change A: DeleteDependency must
// recompute a blocked wi's status back to 'queued' when its last active
// 'blocks' dependency is removed, and record a wi_unblocked event.
//
// Follows the AIHUB_TEST_DB gating pattern from step_pause_stall_test.go /
// memory_latest_test.go: this test SKIPS unless AIHUB_TEST_DB is set. That
// variable is NOT set locally in this sandbox (no local Postgres) and is
// deliberately NOT set on the main `go test ./...` / "Unit tests" step in CI
// either — turning it on there would also switch on every other
// AIHUB_TEST_DB-gated test in this package (memory ranking, cross-project
// resume, conflict prediction, ...) against a database whose migrations
// were never applied for that step, and whose results this change did not
// verify. Instead, .github/workflows/ci.yml runs a dedicated
// "aihub#242 dependency-requeue DB tests" step that applies migrations and
// runs only `-run 'TestDeleteDependency'` with AIHUB_TEST_DB pointed at the
// job's pgvector/pgvector:pg18 service, so this specific regression is
// exercised in CI without widening what else runs there.
//
//	AIHUB_TEST_DB=postgres://postgres:test@localhost:5440/aihub_test?sslmode=disable \
//	go test ./internal/domain/ -run TestDeleteDependency -v -count=1

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// seedWIs performs the same project-wipe cleanup step_pause_stall_test.go's
// seedWI does, but only ONCE, then creates n work items directly via
// CreateWorkItem in the given project. seedWI itself cannot be called more
// than once per test to get multiple wis: each call wipes every work item in
// the project (and, via the wi_dependencies FK's ON DELETE CASCADE on both
// columns — migration 0003 — any dependency row referencing them), so a
// second seedWI call would delete the wi(s) the first call just created. A
// test that then tried to link two "different" wis with a 'blocks' dependency
// would actually be referencing an already-deleted id and hit an FK
// violation in setup, never reaching the code under test.
//
// Each returned wi gets a distinct, mutually-dissimilar goal — never a
// repeated "seed wi for <TestName>" string — because CreateWorkItem runs a
// goal-similarity dedup check (checkDedup in work_items.go) against existing
// queued/running/paused/blocked wis in the same project: two wis in the same
// project with near-identical goals would make the second (or third) create
// fail with ErrConflictCandidates (score >= 0.65) or ErrConflictDuplicate
// (score >= 0.90) before the test ever reaches DeleteDependency. The goals
// below are unrelated topics with no shared phrasing, keeping every pairwise
// score comfortably under 0.65 (verified by hand: max ~0.43, see jaccardNGram
// in work_items.go for the scoring formula).
func seedWIs(t *testing.T, pool *pgxpool.Pool, project, userID string, n int) []*WorkItem {
	t.Helper()
	mustExec(t, pool, `DELETE FROM wi_step_completions WHERE work_item_id IN (SELECT id FROM work_items WHERE project='`+project+`')`)
	mustExec(t, pool, `DELETE FROM agent_events WHERE work_item_id IN (SELECT id FROM work_items WHERE project='`+project+`')`)
	mustExec(t, pool, `UPDATE work_items SET current_attempt_id=NULL WHERE project='`+project+`'`)
	mustExec(t, pool, `DELETE FROM run_attempts WHERE work_item_id IN (SELECT id FROM work_items WHERE project='`+project+`')`)
	mustExec(t, pool, `DELETE FROM work_items WHERE project='`+project+`'`)

	goals := []string{
		"provision a new kubernetes cluster for staging",
		"rotate expired TLS certificates on the edge proxies",
		"backfill missing avatar thumbnails for legacy users",
		"archive stale feature flags older than one year",
	}
	if n > len(goals) {
		t.Fatalf("seedWIs: n=%d exceeds available distinct-goal pool (%d)", n, len(goals))
	}

	out := make([]*WorkItem, n)
	for i := 0; i < n; i++ {
		wi, aerr := CreateWorkItem(context.Background(), pool, &CreateWorkItemRequest{
			Project: project,
			Goal:    goals[i],
			Source:  "human",
		}, userID, userID)
		require.Nil(t, aerr)
		out[i] = wi
	}
	return out
}

// createBlocksDep inserts a 'blocks' wi_dependencies row directly and derives
// status='blocked' on the blocked wi the same way CreateDependency does,
// bypassing CreateDependency's permission/cycle-detection plumbing which is
// not relevant to this test.
func createBlocksDep(t *testing.T, pool *pgxpool.Pool, blockedWIID, blockingWIID, userID string) {
	t.Helper()
	mustExec(t, pool, `
		INSERT INTO wi_dependencies (blocked_wi_id, blocking_wi_id, kind, created_by)
		VALUES ('`+blockedWIID+`', '`+blockingWIID+`', 'blocks', '`+userID+`')`)
	mustExec(t, pool, `UPDATE work_items SET status='blocked' WHERE id='`+blockedWIID+`' AND status='queued'`)
}

func wiStatusOf(t *testing.T, pool *pgxpool.Pool, id string) string {
	t.Helper()
	var s string
	require.NoError(t, pool.QueryRow(context.Background(), `SELECT status FROM work_items WHERE id=$1`, id).Scan(&s))
	return s
}

// TestDeleteDependency_LastBlockerRemoved_Requeues is the core aihub#242
// regression test: a wi with a single 'blocks' dependency must be requeued
// (and a wi_unblocked event recorded) once that dependency is removed.
func TestDeleteDependency_LastBlockerRemoved_Requeues(t *testing.T) {
	pool := setupLatestTestDB(t)
	u := testUser(t, pool)
	project := testProject(t, pool, u)

	wis := seedWIs(t, pool, project, u, 2)
	blocker, blocked := wis[0], wis[1]
	createBlocksDep(t, pool, blocked.ID, blocker.ID, u)
	require.Equal(t, "blocked", wiStatusOf(t, pool, blocked.ID), "precondition: blocked wi must be status=blocked")

	aerr := DeleteDependency(context.Background(), pool, blocked.ID, blocker.ID, "blocks")
	require.Nil(t, aerr)

	assert.Equal(t, "queued", wiStatusOf(t, pool, blocked.ID), "removing the last blocker must requeue the wi")

	var evtCount int
	require.NoError(t, pool.QueryRow(context.Background(), `
		SELECT COUNT(*) FROM agent_events WHERE work_item_id=$1 AND event_type='wi_unblocked'`,
		blocked.ID).Scan(&evtCount))
	assert.Equal(t, 1, evtCount, "expected exactly one wi_unblocked event")
}

// TestDeleteDependency_OtherBlockerRemains_StaysBlocked verifies the
// remaining-blocker case: removing one of two 'blocks' dependencies must
// leave the wi blocked because a live blocker still exists.
func TestDeleteDependency_OtherBlockerRemains_StaysBlocked(t *testing.T) {
	pool := setupLatestTestDB(t)
	u := testUser(t, pool)
	project := testProject(t, pool, u)

	wis := seedWIs(t, pool, project, u, 3)
	blockerA, blockerB, blocked := wis[0], wis[1], wis[2]
	createBlocksDep(t, pool, blocked.ID, blockerA.ID, u)
	createBlocksDep(t, pool, blocked.ID, blockerB.ID, u)
	require.Equal(t, "blocked", wiStatusOf(t, pool, blocked.ID))

	aerr := DeleteDependency(context.Background(), pool, blocked.ID, blockerA.ID, "blocks")
	require.Nil(t, aerr)

	assert.Equal(t, "blocked", wiStatusOf(t, pool, blocked.ID), "wi must stay blocked while blockerB's dependency remains")

	var evtCount int
	require.NoError(t, pool.QueryRow(context.Background(), `
		SELECT COUNT(*) FROM agent_events WHERE work_item_id=$1 AND event_type='wi_unblocked'`,
		blocked.ID).Scan(&evtCount))
	assert.Equal(t, 0, evtCount, "no wi_unblocked event should fire while a blocker remains")

	// Removing the second (last) blocker must now requeue it.
	aerr = DeleteDependency(context.Background(), pool, blocked.ID, blockerB.ID, "blocks")
	require.Nil(t, aerr)
	assert.Equal(t, "queued", wiStatusOf(t, pool, blocked.ID))
}
