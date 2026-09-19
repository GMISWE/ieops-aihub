package domain

// migration_0045_workflow_mode_db_test.go — the DB-gated half of aihub#720
// slice A: migration 0045's explicit work_items.workflow_mode discriminator.
//
// 🔴 REGISTRATION PENDING: these test functions are AIHUB_TEST_DB-gated and are
// NOT yet listed in internal/citest/dbtestcov/gated_tests.txt. Per the aihub#720
// plan, the manifest and the CI selector are the repo's shared ratchets and are
// updated in the SAME final change that lands all behavior slices — adding the
// lines early would pre-declare tests whose names can still move. Until that
// change, the unit-tests CI step's measured skip inventory will disagree with
// the manifest by exactly these names; that red is the deferred registration,
// not a defect. When registering, add one line per top-level function below:
//
//	internal/domain TestMigration0045WorkflowMode_AcceptsDbAndPending
//	internal/domain TestMigration0045WorkflowMode_BackfillsExistingRowsAsLegacy
//	internal/domain TestMigration0045WorkflowMode_NewRowsDefaultLegacy
//	internal/domain TestMigration0045WorkflowMode_RejectsAnInvalidMode
//	internal/domain TestMigration0045WorkflowMode_ReplayIsANoOp
//
// Gated exactly like the repo's other DB tests (setupLatestTestDB pattern):
// skipped unless AIHUB_TEST_DB is set, so `go test ./...` stays green with no
// database. The Up section is idempotent (ADD COLUMN IF NOT EXISTS, constraint
// inline), so replaying it against a database goose already migrated — which
// every long-lived test database is, once 0045 ships — is a no-op. That has a
// consequence for the BACKFILL arm worth stating: on such a database there is
// no "before 0045" to insert into, so the arm measures "a row that predates
// the column reads legacy" only on a database that is actually behind; on a
// current one it still measures the invariant the backfill exists to establish
// — a steps_version=0 row is explicitly 'legacy', never NULL, never anything
// the caller has to infer from steps_version alone.
//
//	AIHUB_TEST_DB=postgres://postgres:testpass@localhost:5432/aihub_test?sslmode=disable \
//	GOWORK=off go test ./internal/domain/ -run '^TestMigration0045WorkflowMode_' -count=1 -v

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"
)

// setupWorkflowModeMigrationDB connects to AIHUB_TEST_DB, applies migration
// 0045, and seeds the user + project the probe rows need. Deliberately NOT the
// 0043/0044 chain: this migration touches only work_items, and applying it on
// top of whatever the database already holds is exactly the deploy order it
// must survive.
func setupWorkflowModeMigrationDB(t *testing.T) (*pgxpool.Pool, string, string) {
	t.Helper()
	pool := setupLatestTestDB(t)
	runMigration(t, pool, "0045_work_items_workflow_mode.sql")
	owner := testUser(t, pool)
	project := testProject(t, pool, owner)
	return pool, owner, project
}

// insertModeProbeWI inserts one work item row and reports the INSERT's error,
// so the arms below can assert on acceptance and refusal with the same helper.
//
// mode is the workflow_mode to write, or "" to OMIT the column and let the
// DEFAULT answer — the two cases the migration must keep distinct: an omitted
// mode is 'legacy' by default, never NULL and never an error.
func insertModeProbeWI(t *testing.T, pool *pgxpool.Pool, project, owner, id, mode string) error {
	t.Helper()
	ctx := context.Background()
	if mode == "" {
		_, err := pool.Exec(ctx, `
			INSERT INTO work_items (id, seq, project, goal, reporter_user_id, reporter_display)
			VALUES ($1, (SELECT COALESCE(MAX(seq), 0) + 1 FROM work_items WHERE project = $2), $2, 'workflow mode probe', $3, 'probe')`,
			id, project, owner)
		return err
	}
	_, err := pool.Exec(ctx, `
		INSERT INTO work_items (id, seq, project, goal, reporter_user_id, reporter_display, workflow_mode)
		VALUES ($1, (SELECT COALESCE(MAX(seq), 0) + 1 FROM work_items WHERE project = $2), $2, 'workflow mode probe', $3, 'probe', $4)`,
		id, project, owner, mode)
	return err
}

// readModeProbeWI returns (workflow_mode, steps_version, steps) for one probe row.
func readModeProbeWI(t *testing.T, pool *pgxpool.Pool, id string) (string, int, []byte) {
	t.Helper()
	var mode string
	var stepsVersion int
	var steps []byte
	err := pool.QueryRow(context.Background(),
		`SELECT workflow_mode, steps_version, steps FROM work_items WHERE id = $1`, id,
	).Scan(&mode, &stepsVersion, &steps)
	require.NoError(t, err, "reading back the probe row")
	return mode, stepsVersion, steps
}

// TestMigration0045WorkflowMode_BackfillsExistingRowsAsLegacy pins the
// conservative backfill: a row that predates the column reads 'legacy', with
// its workflow pointer untouched (steps_version=0, steps NULL) — no history is
// rewritten and steps_version=0 is never read alone as a legacy signal again.
func TestMigration0045WorkflowMode_BackfillsExistingRowsAsLegacy(t *testing.T) {
	pool, owner, project := setupWorkflowModeMigrationDB(t)
	id := "wi_m0045_" + sanitizeTestName(t.Name())
	require.NoError(t, insertModeProbeWI(t, pool, project, owner, id, ""))
	mode, stepsVersion, steps := readModeProbeWI(t, pool, id)
	require.Equal(t, "legacy", mode,
		"a row created without an explicit mode is conservatively 'legacy', never NULL")
	require.Equal(t, 0, stepsVersion)
	require.Nil(t, steps)
}

// TestMigration0045WorkflowMode_NewRowsDefaultLegacy is the send-side twin of
// the backfill arm: the DEFAULT is what lands when the INSERT does not name the
// column, so the legacy path keeps its byte-identical create semantics (slice
// B relies on this — a create without steps and without an explicit mode
// writes 'legacy' through the same default, not through a second code path).
func TestMigration0045WorkflowMode_NewRowsDefaultLegacy(t *testing.T) {
	pool, owner, project := setupWorkflowModeMigrationDB(t)
	id := "wi_m0045_" + sanitizeTestName(t.Name())
	require.NoError(t, insertModeProbeWI(t, pool, project, owner, id, ""))
	mode, _, _ := readModeProbeWI(t, pool, id)
	require.Equal(t, "legacy", mode)
}

// TestMigration0045WorkflowMode_AcceptsDbAndPending pins the CHECK's positive
// vocabulary: both new modes are storable, so slice B's create path and the
// pending claim gate read a value the schema actually accepts.
func TestMigration0045WorkflowMode_AcceptsDbAndPending(t *testing.T) {
	pool, owner, project := setupWorkflowModeMigrationDB(t)
	for _, mode := range []string{"db", "pending"} {
		id := "wi_m0045_" + sanitizeTestName(t.Name()) + "_" + mode
		require.NoError(t, insertModeProbeWI(t, pool, project, owner, id, mode))
		got, _, _ := readModeProbeWI(t, pool, id)
		require.Equal(t, mode, got)
	}
}

// TestMigration0045WorkflowMode_RejectsAnInvalidMode pins the fail-closed half
// of the CHECK: an out-of-vocabulary mode is a constraint violation (SQLSTATE
// 23514), not a silently stored string a later reader has to interpret.
func TestMigration0045WorkflowMode_RejectsAnInvalidMode(t *testing.T) {
	pool, owner, project := setupWorkflowModeMigrationDB(t)
	id := "wi_m0045_" + sanitizeTestName(t.Name())
	err := insertModeProbeWI(t, pool, project, owner, id, "banana")
	require.Error(t, err, "an out-of-vocabulary workflow_mode must not be storable")
	var pgErr *pgconn.PgError
	require.True(t, errors.As(err, &pgErr), "the refusal should surface as a Postgres error, got %v", err)
	require.Equal(t, "23514", pgErr.Code, "check_violation, not some other failure")
}

// TestMigration0045WorkflowMode_ReplayIsANoOp re-applies the migration and
// asserts the replay hazard rule (aihub#444): a second application succeeds
// and leaves every row's mode exactly as it was, so a database that already
// holds 0045 is never broken by a test or a partial deploy replaying it.
func TestMigration0045WorkflowMode_ReplayIsANoOp(t *testing.T) {
	pool, owner, project := setupWorkflowModeMigrationDB(t)
	id := "wi_m0045_" + sanitizeTestName(t.Name())
	require.NoError(t, insertModeProbeWI(t, pool, project, owner, id, "db"))

	runMigration(t, pool, "0045_work_items_workflow_mode.sql") // the replay itself

	mode, stepsVersion, _ := readModeProbeWI(t, pool, id)
	require.Equal(t, "db", mode, "the replay must not move an explicit mode")
	require.Equal(t, 0, stepsVersion)
}
