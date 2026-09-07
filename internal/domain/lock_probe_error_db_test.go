package domain

// aihub#410, the end-to-end half: a probe failure must reach the CALLER as the
// retryable 409, not merely be classified correctly inside
// probeForeignLockHolders.
//
// WHY THIS EXISTS ALONGSIDE lock_probe_error_test.go. Those arms hand the
// function a stub pgx.Tx and assert what it returns; this one drives the real
// FnClaimWorkItem against a real Postgres and asserts what the caller gets.
// They are different hops and neither implies the other — the classification
// could be right and then be discarded, re-wrapped, or overtaken by the 25P02
// that the aborted transaction produces one statement later, which is precisely
// the defect's visible symptom (a 500 about the second victim). A contract with
// two hops needs an assertion on each.
//
// HOW THE FAILURE IS INJECTED, and why it is a real SQLSTATE rather than a
// hand-built error. A genuine SSI 40001 cannot be aimed at one chosen
// statement: Postgres reports it when the dangerous structure completes, which
// for a plain SELECT is not controllable from outside the transaction (the
// interleaving that serialization_failure_db_test.go uses works because its
// subject blocks on SELECT ... FOR UPDATE; the probe takes no row lock, so
// nothing to block on). So `resource_locks` is swapped for a view over a
// set-returning function that RAISEs with ERRCODE 40001:
//
//	ALTER TABLE resource_locks RENAME TO resource_locks_pf410
//	CREATE FUNCTION pf410_boom() RETURNS SETOF resource_locks_pf410 ... RAISE ... 40001
//	CREATE VIEW resource_locks AS SELECT * FROM pf410_boom()
//
// The cause is simulated; the ERROR IS NOT. It is raised by the server, carries
// SQLSTATE 40001, travels through the real pgx driver on the real probe
// statement inside the real SERIALIZABLE transaction, and aborts it exactly as
// a real serialization failure would. That is the part this test is about — a
// fabricated *pgconn.PgError would prove only that the classifier compiles,
// which is the trap serialization_failure_db_test.go names.
//
// A set-returning function in FROM rather than `WHERE pf410_boom()`: a
// predicate is only evaluated for rows that are scanned, so over an empty or
// short-circuited relation it would never fire and this test would pass
// vacuously. A SRF in FROM has to be called to produce the relation at all.
// The injection is nonetheless SELF-CHECKED below by running the actual
// foreignLockHolderSQL and requiring 40001 back, because an injection that
// silently stopped working is a green test that measures nothing.
//
// MUTANT (the pre-aihub#410 build): delete the `if !errors.Is(err,
// pgx.ErrNoRows)` branch from probeForeignLockHolders. The probe swallows the
// 40001, the transaction is already aborted, and the next statement fails with
// 25P02 — not class 40 — so the caller gets, MEASURED on that mutant against
// this fixture rather than predicted:
//
//	500 INTERNAL_ERROR  failed to insert run_attempt: ERROR: current transaction
//	                    is aborted, commands ignored until end of transaction
//	                    block (SQLSTATE 25P02)
//
// Note what that message names: a statement that was only ever the second
// victim, with the SQLSTATE that meant "retry" nowhere in it. The assertions
// below separate the two builds by CODE, not by whether an error occurred —
// both builds error here, and only one of them is right.
//
// Gated on AIHUB_TEST_DB like every DB test in this package, and registered in
// internal/citest/dbtestcov/gated_tests.txt.
//
//	AIHUB_TEST_DB=postgres://postgres:testpass@localhost:5444/aihub_test?sslmode=disable \
//	go test ./internal/domain/ -run 'TestClaimProbeFailureReachesTheCallerAsARetryable409' -count=1 -v

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// installProbePoison makes the lock-conflict probe statement fail with SQLSTATE
// 40001, and restores the schema afterwards.
//
// The cleanup is registered IMMEDIATELY after the rename, before the two
// CREATEs that can themselves fail: a t.Fatalf between the rename and the
// cleanup registration would leave the whole test database without a
// resource_locks table, and every later step in CI's DB block would fail on a
// fixture rather than on its own subject.
func installProbePoison(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	ctx := context.Background()

	mustExec(t, pool, `ALTER TABLE resource_locks RENAME TO resource_locks_pf410`)
	t.Cleanup(func() {
		// Order matters: the view depends on the function, which depends on the
		// table's composite type. IF EXISTS on both so a partial install still
		// gets the table's name back.
		_, _ = pool.Exec(ctx, `DROP VIEW IF EXISTS resource_locks`)
		_, _ = pool.Exec(ctx, `DROP FUNCTION IF EXISTS pf410_boom()`)
		if _, err := pool.Exec(ctx, `ALTER TABLE resource_locks_pf410 RENAME TO resource_locks`); err != nil {
			t.Errorf("could not restore the resource_locks table: %v — the test database is left "+
				"with the poison installed, and every later DB test in this run will fail on the fixture "+
				"instead of on its own subject", err)
			return
		}
		// Prove the restore, rather than assuming the rename returned success
		// for the right reason.
		var n int
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM resource_locks`).Scan(&n); err != nil {
			t.Errorf("resource_locks is not queryable after cleanup: %v", err)
		}
	})

	mustExec(t, pool, `
		CREATE FUNCTION pf410_boom() RETURNS SETOF resource_locks_pf410
		LANGUAGE plpgsql AS $$
		BEGIN
			RAISE EXCEPTION 'aihub#410 injected probe failure' USING ERRCODE = '40001';
		END $$`)
	mustExec(t, pool, `CREATE VIEW resource_locks AS SELECT * FROM pf410_boom()`)

	// ── Injection self-check, on the statement actually under test ──────────
	// foreignLockHolderSQL is the probe's own SQL (this file is in-package), so
	// this checks the real joins and the real plan shape, not a stand-in
	// `SELECT count(*)` that might be planned differently.
	var a, b, c string
	err := pool.QueryRow(ctx, foreignLockHolderSQL,
		"file_scope", []string{"aihub:aihub:probe-selfcheck"}, "", "wi_not_this_one",
	).Scan(&a, &b, &c)
	require.Error(t, err, "the injection did not fire on foreignLockHolderSQL, so this test proves nothing")
	var pgErr *pgconn.PgError
	require.ErrorAs(t, err, &pgErr, "the injected failure is not a Postgres error: %v", err)
	require.Equal(t, "40001", pgErr.Code,
		"the injection fired with SQLSTATE %s rather than 40001, so the arm below would be asserting "+
			"the wrong classification", pgErr.Code)
}

func TestClaimProbeFailureReachesTheCallerAsARetryable409(t *testing.T) {
	pool := setupLatestTestDB(t)
	ctx := context.Background()
	uid := testUser(t, pool)
	proj := testProject(t, pool, uid)

	declared := json.RawMessage(`[{"type":"path","uri":"file:internal/domain/run_attempts.go","repo":"aihub","intent":"write"}]`)

	// A first work item, claimed successfully BEFORE the poison goes in. Two
	// reasons, both load-bearing:
	//
	//  1. it leaves a live run_attempts row and a resource_locks row, so the
	//     probe's inner joins are not degenerate. With both sides empty the
	//     planner may satisfy the query without ever scanning the poisoned
	//     relation, and the injection would not fire.
	//  2. it is the positive control for the whole fixture: it proves a claim of
	//     a wi with these declared_resources SUCCEEDS on an unpoisoned schema,
	//     so the 409 asserted below is attributable to the injection rather than
	//     to a broken fixture.
	neighbour := seedWIWithResources(t, pool, proj, uid, "aihub#410 neighbour holding a lock", declared)
	resp, aerr := claimWI(t, pool, uid, neighbour.ID, "aihub410-neighbour")
	require.Nil(t, aerr, "the control claim failed on an unpoisoned schema, so the fixture is broken, not the subject: %v", aerr)
	require.NotEmpty(t, resp.AcquiredLocks, "the control claim took no locks, so there is no lock row for the probe to join against")

	// The subject: a DIFFERENT work item declaring a DIFFERENT path, so the
	// probe finds no genuine conflict and its only possible answer is
	// ErrNoRows — until the injection turns that statement into a 40001.
	// Declaring the same path would make ErrConflictLockTaken a legitimate
	// answer and the arm could not tell the two apart.
	subject := seedWIWithResources(t, pool, proj, uid,
		"aihub#410 subject whose probe explodes",
		json.RawMessage(`[{"type":"path","uri":"file:internal/domain/conflicts.go","repo":"aihub","intent":"write"}]`))

	// The probe loop iterates req.RequestedLocks, which the standard flow leaves
	// empty and deriveClaimLocks fills in from declared_resources. If that
	// derivation produced nothing the loop body would never run, the injection
	// would never be reached, and the claim would simply succeed — so assert the
	// precondition instead of inferring it from the outcome.
	var probeReq ClaimRequest
	probes := deriveClaimLocks(&probeReq, subject.DeclaredResources, subject.Project)
	require.NotEmpty(t, probes, "no lock probes were derived, so probeForeignLockHolders would iterate zero times")
	require.NotEmpty(t, probeReq.RequestedLocks, "no locks were derived from declared_resources")

	installProbePoison(t, pool)

	_, aerr = claimWI(t, pool, uid, subject.ID, "aihub410-subject")

	require.NotNil(t, aerr, "the lock-conflict probe failed with SQLSTATE 40001 and the claim reported success")
	assert.Equal(t, ErrConflictSerializationFailure, aerr.Code,
		"a 40001 raised BY THE PROBE must reach the caller as a retryable conflict. Pre-aihub#410 the probe "+
			"read it as \"no conflict\", the transaction was already aborted, and the caller was told "+
			"INTERNAL_ERROR about whichever later statement hit 25P02 — a 500 naming the second victim. "+
			"got %s: %s", aerr.Code, aerr.Message)
	assert.Equal(t, 409, aerr.HTTPStatus,
		"got %d %s: %s", aerr.HTTPStatus, aerr.Code, aerr.Message)
	assert.NotEqual(t, ErrInternalError, aerr.Code)

	details, ok := aerr.Details.(map[string]any)
	require.True(t, ok, "the 409 must carry machine-readable retry guidance; got %#v", aerr.Details)
	assert.Equal(t, true, details["retryable"],
		"a caller deciding whether to retry must not have to parse the message")
	assert.Equal(t, "40001", details["sqlstate"],
		"the originating SQLSTATE must survive to the caller; got %#v", details["sqlstate"])

	// WHICH hop failed has to survive too. Without this the 409 is
	// indistinguishable from the one the neighbouring statements raise, and the
	// next person to debug it starts in the wrong place.
	assert.Contains(t, aerr.Message, "probe lock holders",
		"the message does not say that the PROBE was the statement that failed; got %q", aerr.Message)

	// And the claim must not have half-landed. The subject stays queued: the
	// probe fails before the attempt row is inserted, and the transaction that
	// would have inserted it is rolled back.
	var status string
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT status FROM work_items WHERE id = $1`, subject.ID).Scan(&status))
	assert.Equal(t, "queued", status,
		"the failed claim left the work item in %q — a rolled-back transaction must leave no trace", status)
}
