package domain

// aihub#350 — the database halves of the derived-disposition gate: what a wrap
// stores, what filed: existence really refuses, and that the default
// disposition leaves the work-item table alone.
//
// The shape halves (missing list refused, entry grammar, status scoping) run
// with a nil pool in complete_attempt_derived_guard_test.go and need no
// database; everything HERE is a claim about rows, so it is AIHUB_TEST_DB-gated
// and runs in ci.yml's "aihub#350 derived disposition DB tests" step.
//
//	AIHUB_TEST_DB=postgres://postgres:testpass@localhost:15432/aihub_test?sslmode=disable \
//	go test ./internal/domain/ -run 'TestWrapDerived|TestWrapFiled' -v -count=1

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"
)

// wrapReadyFixture seeds a work item with a running attempt and returns the
// pieces a wrap needs. The attempt row comes from seedRunAttempt (claim_epoch 1,
// hashed secret), so FnCompleteAttempt's credential check runs for real.
func wrapReadyFixture(t *testing.T, pool *pgxpool.Pool) (project string, wiID, attemptID, secret string) {
	t.Helper()
	u := testUser(t, pool)
	project = testProject(t, pool, u)
	wi := seedWI(t, pool, project, u)
	secret = "aihub350-derived-0123456789abcdef0123456789abcdef0123456789"
	attemptID = seedRunAttempt(t, pool, wi.ID, u, secret)
	return project, wi.ID, attemptID, secret
}

// countWorkItems returns how many work items the project holds. Scoped to the
// project rather than the table so a concurrently-running suite cannot move it.
func countWorkItems(t *testing.T, pool *pgxpool.Pool, project string) int {
	t.Helper()
	var n int
	require.NoError(t, pool.QueryRow(context.Background(),
		`SELECT count(*) FROM work_items WHERE project=$1`, project).Scan(&n))
	return n
}

// readDerivedColumn returns run_attempts.derived for the attempt, and whether
// it was NULL - the distinction migration 0040 exists to keep.
func readDerivedColumn(t *testing.T, pool *pgxpool.Pool, attemptID string) (entries []string, isNull bool) {
	t.Helper()
	var raw []byte
	require.NoError(t, pool.QueryRow(context.Background(),
		`SELECT derived FROM run_attempts WHERE id=$1`, attemptID).Scan(&raw))
	if raw == nil {
		return nil, true
	}
	require.NoError(t, json.Unmarshal(raw, &entries))
	return entries, false
}

// TestWrapDerivedFoldedLeavesNoNewWorkItem is acceptance criterion 1 driven
// end to end at the domain layer: an attempt that noticed something and did not
// fix it takes the cheapest legal path - one bare "folded" - and the assertion
// is that the wrap SUCCEEDS while the project's work-item population does not
// move. The disposition lands on the attempt row and in the attempt_completed
// payload, from the same value, so the row and the timeline cannot disagree.
func TestWrapDerivedFoldedLeavesNoNewWorkItem(t *testing.T) {
	pool := setupLatestTestDB(t)
	project, wiID, attemptID, secret := wrapReadyFixture(t, pool)

	before := countWorkItems(t, pool, project)

	aerr := FnCompleteAttempt(context.Background(), pool, wiID, &CompleteAttemptRequest{
		AttemptID:     attemptID,
		ClaimEpoch:    1,
		SessionSecret: secret,
		Status:        "wrapped",
		Derived:       []string{"folded: seedWI's dedup sweep also matches cancelled items"},
	}, nil, "")
	require.Nil(t, aerr, "the cheapest compliant wrap must succeed")

	if after := countWorkItems(t, pool, project); after != before {
		t.Fatalf("folding a finding changed the work-item count %d -> %d.\nThe whole design is "+
			"that the default disposition leaves NO queue residue - a fold that files is the "+
			"pre-aihub#350 behaviour wearing the new field", before, after)
	}

	var wiStatus string
	require.NoError(t, pool.QueryRow(context.Background(),
		`SELECT status FROM work_items WHERE id=$1`, wiID).Scan(&wiStatus))
	require.Equal(t, "wrapped", wiStatus)

	entries, isNull := readDerivedColumn(t, pool, attemptID)
	require.False(t, isNull, "the wrap carried a disposition and the column is NULL")
	require.Equal(t, []string{"folded: seedWI's dedup sweep also matches cancelled items"}, entries)

	// The event carries the same list. The timeline is the record that survives
	// the state file's deletion, so this is where a later reader will look.
	var payload []byte
	require.NoError(t, pool.QueryRow(context.Background(), `
		SELECT payload FROM agent_events
		WHERE work_item_id=$1 AND event_type='attempt_completed'
		ORDER BY created_at DESC LIMIT 1`, wiID).Scan(&payload))
	var evt struct {
		Status  string   `json:"status"`
		Derived []string `json:"derived"`
	}
	require.NoError(t, json.Unmarshal(payload, &evt))
	require.Equal(t, "wrapped", evt.Status)
	require.Equal(t, entries, evt.Derived,
		"the attempt_completed payload and the column disagree about the dispositions")
}

// TestWrapDerivedExplicitEmptyIsStoredAsEmptyNotNull holds migration 0040's
// distinction at the write: [] is "declared none" and NULL is "predates the
// gate / not a wrap". A write that normalises [] to NULL erases exactly the
// declaration the required field exists to extract.
func TestWrapDerivedExplicitEmptyIsStoredAsEmptyNotNull(t *testing.T) {
	pool := setupLatestTestDB(t)
	_, wiID, attemptID, secret := wrapReadyFixture(t, pool)

	aerr := FnCompleteAttempt(context.Background(), pool, wiID, &CompleteAttemptRequest{
		AttemptID:     attemptID,
		ClaimEpoch:    1,
		SessionSecret: secret,
		Status:        "wrapped",
		Derived:       []string{},
	}, nil, "")
	require.Nil(t, aerr)

	entries, isNull := readDerivedColumn(t, pool, attemptID)
	if isNull {
		t.Fatal("derived=[] was stored as NULL - \"declared none\" and \"never asked\" have " +
			"collapsed, which is the pre-aihub#350 state with extra steps")
	}
	require.Len(t, entries, 0)
}

// TestWrapDerivedIsNotWrittenOnNonWrappedCompletions is the storage half of the
// status scoping: a pause writes NULL to derived even though the request struct
// carries the field, mirroring the pause_reason normalisation one parameter
// over (aihub#452).
func TestWrapDerivedIsNotWrittenOnNonWrappedCompletions(t *testing.T) {
	pool := setupLatestTestDB(t)
	_, wiID, attemptID, secret := wrapReadyFixture(t, pool)

	aerr := FnCompleteAttempt(context.Background(), pool, wiID, &CompleteAttemptRequest{
		AttemptID:     attemptID,
		ClaimEpoch:    1,
		SessionSecret: secret,
		Status:        "paused",
		Derived:       []string{}, // empty passes the guard and must still not land
	}, nil, "")
	require.Nil(t, aerr)

	_, isNull := readDerivedColumn(t, pool, attemptID)
	require.True(t, isNull, "a pause stored a derived value; dispositions belong to the wrap alone")
}

// TestWrapFiledMustNameAnExistingWorkItem is acceptance criterion 3: filed: is
// the claim that a wi was really opened, and a ref that does not resolve
// refuses the wrap WHOLE - attempt still running, work item untouched, no
// attempt_completed event - because a partial wrap that recorded a false filing
// would be worse than the omission the gate replaces.
func TestWrapFiledMustNameAnExistingWorkItem(t *testing.T) {
	pool := setupLatestTestDB(t)
	project, wiID, attemptID, secret := wrapReadyFixture(t, pool)

	req := func(ref string) *CompleteAttemptRequest {
		return &CompleteAttemptRequest{
			AttemptID:     attemptID,
			ClaimEpoch:    1,
			SessionSecret: secret,
			Status:        "wrapped",
			Derived:       []string{"filed:" + ref},
		}
	}

	t.Run("a_nonexistent_ref_refuses_the_wrap_whole", func(t *testing.T) {
		aerr := FnCompleteAttempt(context.Background(), pool, wiID, req(project+"#99999"), nil, "")
		require.NotNil(t, aerr, "filed: named a work item that does not exist and the wrap succeeded")
		require.Equal(t, ErrNotFound, aerr.Code,
			"the refusal must be the resolver's own not-found, the one answer that does not leak "+
				"which of \"absent\" and \"hidden\" it was")

		var attemptStatus, wiStatus string
		require.NoError(t, pool.QueryRow(context.Background(),
			`SELECT status FROM run_attempts WHERE id=$1`, attemptID).Scan(&attemptStatus))
		require.NoError(t, pool.QueryRow(context.Background(),
			`SELECT status FROM work_items WHERE id=$1`, wiID).Scan(&wiStatus))
		require.Equal(t, "running", attemptStatus, "the refusal was not total: the attempt moved")
		require.Equal(t, "running", wiStatus, "the refusal was not total: the work item moved")

		var events int
		require.NoError(t, pool.QueryRow(context.Background(),
			`SELECT count(*) FROM agent_events WHERE work_item_id=$1 AND event_type='attempt_completed'`,
			wiID).Scan(&events))
		require.Zero(t, events, "a refused wrap left an attempt_completed event behind")
	})

	t.Run("a_real_slug_and_a_real_id_both_resolve", func(t *testing.T) {
		u := testUser(t, pool)
		filed, aerr := CreateWorkItem(context.Background(), pool, &CreateWorkItemRequest{
			Project: project,
			Goal:    "the wi the wrap under test really filed (aihub#350 fixture)",
			Source:  "human",
		}, u, u, nil, "")
		require.Nil(t, aerr)

		// By slug first - the form the template teaches - then, after reseeding
		// a fresh attempt, by canonical id.
		aerr = FnCompleteAttempt(context.Background(), pool, wiID, req(filed.Slug), nil, "")
		require.Nil(t, aerr, "filed:<slug> of a real same-project wi was refused")

		entries, isNull := readDerivedColumn(t, pool, attemptID)
		require.False(t, isNull)
		require.Equal(t, []string{"filed:" + filed.Slug}, entries)

		// NOT seedWI: its child-to-parent sweep deletes every work item in the
		// project, including the one this subtest just filed against.
		wi2, aerr2 := CreateWorkItem(context.Background(), pool, &CreateWorkItemRequest{
			Project: project,
			Goal:    "second wrap fixture, filing by canonical id (aihub#350)",
			Source:  "human",
		}, u, u, nil, "")
		require.Nil(t, aerr2)
		attempt2 := seedRunAttempt(t, pool, wi2.ID, u, secret)
		aerr = FnCompleteAttempt(context.Background(), pool, wi2.ID, &CompleteAttemptRequest{
			AttemptID:     attempt2,
			ClaimEpoch:    1,
			SessionSecret: secret,
			Status:        "wrapped",
			Derived:       []string{"filed:" + filed.ID},
		}, nil, "")
		require.Nil(t, aerr, "filed:<canonical id> was refused")
	})
}

// TestWrapFiledCrossProjectIsScopedByCallerRoles is the visibility half, and
// the reason FnCompleteAttempt grew the roles parameters at all: a third of
// measured derived inflow crosses projects, so filed: must be able to name a
// wi in another project - but only one the CALLER can see, through the same
// resolver blocked_by uses, or this field becomes the existence oracle
// aihub#377 closed. A hidden work item answers exactly like an absent one.
func TestWrapFiledCrossProjectIsScopedByCallerRoles(t *testing.T) {
	pool := setupLatestTestDB(t)
	project, wiID, attemptID, secret := wrapReadyFixture(t, pool)
	u := testUser(t, pool)

	// projects.name is capped at 40 chars by projects_name_check (migration
	// 0012), and testProject's own name already uses all of them; truncate
	// before suffixing rather than overflow the constraint.
	otherProject := project
	if len(otherProject) > 34 {
		otherProject = otherProject[:34]
	}
	otherProject += "_other"
	mustExec(t, pool, `INSERT INTO projects(name,owner_user_id) VALUES('`+otherProject+`','`+u+`') ON CONFLICT (name) DO NOTHING`)
	// Child-to-parent, the same order seedWI documents: events and locks hold
	// FKs into work_items/run_attempts, so a prior run's residue blocks the
	// parent deletes otherwise.
	mustExec(t, pool, `DELETE FROM agent_events WHERE work_item_id IN (SELECT id FROM work_items WHERE project='`+otherProject+`')`)
	mustExec(t, pool, `DELETE FROM resource_locks WHERE owner_attempt_id IN (SELECT id FROM run_attempts WHERE work_item_id IN (SELECT id FROM work_items WHERE project='`+otherProject+`'))`)
	mustExec(t, pool, `DELETE FROM run_attempts WHERE work_item_id IN (SELECT id FROM work_items WHERE project='`+otherProject+`')`)
	mustExec(t, pool, `DELETE FROM work_items WHERE project='`+otherProject+`'`)
	foreign, aerr := CreateWorkItem(context.Background(), pool, &CreateWorkItemRequest{
		Project: otherProject,
		Goal:    "cross-project filing target (aihub#350 fixture)",
		Source:  "human",
	}, u, u, nil, "")
	require.Nil(t, aerr)

	req := &CompleteAttemptRequest{
		AttemptID:     attemptID,
		ClaimEpoch:    1,
		SessionSecret: secret,
		Status:        "wrapped",
		Derived:       []string{"filed:" + foreign.Slug},
	}

	t.Run("invisible_means_refused_as_not_found", func(t *testing.T) {
		aerr := FnCompleteAttempt(context.Background(), pool, wiID, req, nil, "")
		require.NotNil(t, aerr,
			"a filed: ref into a project the caller holds no role in resolved anyway - this is "+
				"the enumerable oracle resolveVisibleRefOnTx exists to prevent")
		require.Equal(t, ErrNotFound, aerr.Code)
	})

	t.Run("a_role_in_the_project_makes_it_resolvable", func(t *testing.T) {
		aerr := FnCompleteAttempt(context.Background(), pool, wiID, req,
			map[string]string{otherProject: "writer"}, "")
		require.Nil(t, aerr, "the cross-project filing a third of the inflow needs was refused "+
			"even with the role that should scope it in")
	})
}
