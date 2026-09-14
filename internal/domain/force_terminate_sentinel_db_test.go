package domain

// aihub#675 — the force-terminate history row for a step that was opened without a
// step_attempt_id must be filed under an id unique to that row.
//
// WHAT WENT WRONG
// ---------------
// fnForceTerminateStep filed such a row under the bare literal "unknown". idx_wsc_attempt
// (migration 0005) is a GLOBAL UNIQUE index on wi_step_completions.step_attempt_id, and the
// INSERT is ON CONFLICT (step_attempt_id) DO NOTHING. So the FIRST such row in the whole
// database landed and every later one — any work item, any project, any attempt — was discarded
// in silence, on a request that still emitted step_failed, still reset wi_step_state and still
// answered 200. Nothing reports it; the step simply is not in pf_get_step's completed_steps,
// which aihub#265 made the record a resuming agent is told to trust.
//
// Reachability was not theoretical: both pf-execute loops (engine.native.md's auto loop and
// engine-native-details.md §1) opened steps[0] without the field, so EVERY B/C attempt's first
// step was exposed. aihub#675 fixed the documents too, but step_attempt_id remains optional on
// in_progress (internal/mcp/tools_step.go), so this path stays reachable from any other client
// and the server-side fix is the one that cannot be undone by a document edit.
//
// MEASURED against the pre-change build, which is what this suite reproduces: two work items,
// each paused during a step whose current_step_attempt was NULL, produced 1 history row and 2
// step_failed events. The control arm with distinct ids produced 2 of 2.
//
// MUTANTS (applied to this tree and run 2026-09-14; the verdict is what happened):
//
//	M1 revert: `saID := "unknown"` in place of unknownStepAttemptID(scID)
//	                                       RED  two_paused_steps_without_an_attempt_id_each_file
//	                                            _their_own_row (2 rows expected, 1 found) — the
//	                                            exact pre-change measurement
//	M2 over-fix: drop the ON CONFLICT clause entirely
//	                                       RED  a_repeat_force_terminate_of_the_same_attempt_id
//	                                            _is_still_idempotent — the guarantee the fix
//	                                            must not trade away
//	M3 prefix: return completionID with no prefix at all
//	                                       RED  the_synthesised_id_says_it_was_synthesised
//
//	AIHUB_TEST_DB=postgres://postgres:testpass@localhost:15432/aihub_test?sslmode=disable \
//	go test ./internal/domain/ -run '^TestForceTerminateSynthesisesAUniqueStepAttemptID$' -v -count=1

import (
	"context"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ftsArm is one work item with a running attempt whose credentials a pause can be driven with.
type ftsArm struct {
	wiID      string
	attemptID string
	secret    string
	epoch     int
}

// ftsSeedAttempt inserts a further run attempt on an existing work item and points the work item
// at it. seedRunAttempt cannot be reused for the second one: it hardcodes claim_epoch=1 and
// run_attempts carries a UNIQUE(work_item_id, claim_epoch).
func ftsSeedAttempt(t *testing.T, pool *pgxpool.Pool, a ftsArm, epoch int) ftsArm {
	t.Helper()
	ctx := context.Background()
	id := NewID("ra")
	_, err := pool.Exec(ctx, `
		INSERT INTO run_attempts (id, work_item_id, status, claim_epoch, idempotency_key,
			actor_user_id, actor_display, machine_id, session_secret_hash)
		SELECT $1, $2, 'running', $3, $4, actor_user_id, actor_display, machine_id, $5
		  FROM run_attempts WHERE id = $6`,
		id, a.wiID, epoch, "idem_"+id, HashSecret(a.secret), a.attemptID)
	require.NoError(t, err)
	_, err = pool.Exec(ctx, `
		UPDATE work_items SET status='running', current_attempt_id=$1, current_attempt_epoch=$2 WHERE id=$3`,
		id, epoch, a.wiID)
	require.NoError(t, err)
	return ftsArm{wiID: a.wiID, attemptID: id, secret: a.secret, epoch: epoch}
}

// ftsSeedArm creates a work item in project with its own running attempt. goal must differ per
// arm: CreateWorkItem runs a goal-similarity dedup sweep over the project's open work items, so
// two arms sharing a goal would make the second create fail rather than the assertion speak.
func ftsSeedArm(t *testing.T, pool *pgxpool.Pool, project, user, goal string) ftsArm {
	t.Helper()
	wi, aerr := CreateWorkItem(context.Background(), pool, &CreateWorkItemRequest{
		Project: project,
		Goal:    goal,
		Source:  "human",
	}, user, user, nil, "")
	require.Nil(t, aerr, "seeding %q", goal)
	const secret = "aihub675-force-terminate-0123456789abcdef0123456789"
	return ftsArm{wiID: wi.ID, attemptID: seedRunAttempt(t, pool, wi.ID, user, secret), secret: secret, epoch: 1}
}

// ftsPause drives a real pause through FnCompleteAttempt, which is the production entry point
// that force-terminates an in_progress step (H-R9-11). Going through it rather than calling
// fnForceTerminateStep directly is what makes this an observation rather than a unit test of a
// helper: the silent drop was only ever visible from here.
func ftsPause(t *testing.T, pool *pgxpool.Pool, a ftsArm) {
	t.Helper()
	aerr := FnCompleteAttempt(context.Background(), pool, a.wiID, &CompleteAttemptRequest{
		AttemptID:     a.attemptID,
		ClaimEpoch:    int64(a.epoch),
		SessionSecret: a.secret,
		Status:        "paused",
	}, nil, "")
	require.Nil(t, aerr, "the pause must succeed — it reported success in the defective build too, "+
		"which is precisely why the dropped row was invisible")
}

// ftsHistory returns the step_attempt_ids of every wi_step_completions row for these work items.
func ftsHistory(t *testing.T, pool *pgxpool.Pool, wiIDs ...string) []string {
	t.Helper()
	rows, err := pool.Query(context.Background(),
		`SELECT step_attempt_id FROM wi_step_completions WHERE work_item_id = ANY($1) ORDER BY completed_at, id`,
		wiIDs)
	require.NoError(t, err)
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s string
		require.NoError(t, rows.Scan(&s))
		out = append(out, s)
	}
	require.NoError(t, rows.Err())
	return out
}

// ftsStepFailedEvents counts the step_failed events these work items carry. It is the other half
// of the pair: the defect was not "nothing happened", it was the event landing while the row did
// not, so an assertion about rows alone cannot tell a fixed build from a build that also stopped
// emitting.
func ftsStepFailedEvents(t *testing.T, pool *pgxpool.Pool, wiIDs ...string) int {
	t.Helper()
	var n int
	require.NoError(t, pool.QueryRow(context.Background(),
		`SELECT count(*) FROM agent_events WHERE event_type='step_failed' AND work_item_id = ANY($1)`,
		wiIDs).Scan(&n))
	return n
}

func TestForceTerminateSynthesisesAUniqueStepAttemptID(t *testing.T) {
	pool := setupLatestTestDB(t)
	user := testUser(t, pool)
	project := testProject(t, pool, user)

	t.Run("two_paused_steps_without_an_attempt_id_each_file_their_own_row", func(t *testing.T) {
		a := ftsSeedArm(t, pool, project, user, "alpha zebra lantern first arm of the aihub675 sentinel probe")
		b := ftsSeedArm(t, pool, project, user, "omicron bicycle marmalade second arm about entirely other matters")

		// The shape under test: an in_progress step whose current_step_attempt is NULL, which is
		// exactly what startStep stores when the client omits step_attempt_id.
		seedStepState(t, pool, a.wiID, "in_progress", nil)
		seedStepState(t, pool, b.wiID, "in_progress", nil)

		ftsPause(t, pool, a)
		ftsPause(t, pool, b)

		got := ftsHistory(t, pool, a.wiID, b.wiID)
		require.Len(t, got, 2,
			"both force-terminated steps must be filed. Before aihub#675 both rows were offered "+
				"under the same literal \"unknown\" and the GLOBAL unique index idx_wsc_attempt "+
				"turned the second into a no-op via ON CONFLICT DO NOTHING — DB-wide, so the two "+
				"work items need not be related in any way. Rows found: %v", got)
		assert.NotEqual(t, got[0], got[1],
			"the two rows must carry DIFFERENT synthesised ids; equal ids mean the next such "+
				"force-terminate anywhere in this database is dropped again")

		assert.Equal(t, 2, ftsStepFailedEvents(t, pool, a.wiID, b.wiID),
			"both pauses must still emit step_failed. The defect was the row and the event "+
				"DISAGREEING (1 row, 2 events), so a build that lost the event too would satisfy "+
				"the row assertion above while being just as wrong in the other direction")
	})

	t.Run("the_synthesised_id_says_it_was_synthesised", func(t *testing.T) {
		c := ftsSeedArm(t, pool, project, user, "tungsten harpsichord thursday third arm of this probe")
		seedStepState(t, pool, c.wiID, "in_progress", nil)
		ftsPause(t, pool, c)

		got := ftsHistory(t, pool, c.wiID)
		require.Len(t, got, 1)
		assert.True(t, strings.HasPrefix(got[0], unknownStepAttemptIDPrefix),
			"a reader of the history must be able to tell a synthesised id from one a caller "+
				"sent; %q carries no marker, so it reads as an attempt id that can be joined on "+
				"and cannot be", got[0])
	})

	t.Run("a_step_that_carried_an_attempt_id_is_still_filed_under_that_id", func(t *testing.T) {
		// The control that keeps the fix from being "synthesise one always": a real id must
		// survive to the history row untouched, or the force-terminate record stops naming the
		// step attempt the client knows about.
		d := ftsSeedArm(t, pool, project, user, "quartz meridian sailcloth fourth arm carrying a real id")
		real := NewID("sa")
		seedStepState(t, pool, d.wiID, "in_progress", &real)
		ftsPause(t, pool, d)

		assert.Equal(t, []string{real}, ftsHistory(t, pool, d.wiID),
			"a step opened WITH an attempt id must be filed under exactly that id")
	})

	t.Run("a_repeat_force_terminate_of_the_same_attempt_id_is_still_idempotent", func(t *testing.T) {
		// The guarantee ON CONFLICT DO NOTHING exists for, asserted so the fix cannot be
		// "delete the conflict clause". Two attempts on one work item, both force-terminated
		// while the SAME step attempt id is open: the second must add no row.
		e := ftsSeedArm(t, pool, project, user, "vermilion clockwork orchard fifth arm repeating one id")
		same := NewID("sa")

		seedStepState(t, pool, e.wiID, "in_progress", &same)
		ftsPause(t, pool, e)
		require.Equal(t, []string{same}, ftsHistory(t, pool, e.wiID), "first force-terminate")

		// A second running attempt on the same work item, re-opening the same step attempt id —
		// the shape a resend or a replayed pause produces.
		second := ftsSeedAttempt(t, pool, e, 2)
		seedStepState(t, pool, e.wiID, "in_progress", &same)
		ftsPause(t, pool, second)

		assert.Equal(t, []string{same}, ftsHistory(t, pool, e.wiID),
			"the same step attempt must have exactly ONE history row however many times it is "+
				"force-terminated; a second row here means the aihub#675 fix bought uniqueness by "+
				"giving up the idempotency the ON CONFLICT clause provides")
	})
}
