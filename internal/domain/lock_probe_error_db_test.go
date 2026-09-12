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
// nothing to block on). So the probe's unqualified `resource_locks` is made to
// resolve to a view over a set-returning function that RAISEs with ERRCODE
// 40001, in a private schema pinned ahead of public on a dedicated pool's
// search_path (the shape list_work_items_rows_err_db_test.go uses):
//
//	CREATE SCHEMA pf410_probe_poison
//	CREATE FUNCTION pf410_probe_poison.pf410_boom() RETURNS SETOF public.resource_locks ... RAISE ... 40001
//	CREATE VIEW pf410_probe_poison.resource_locks AS SELECT * FROM pf410_boom()
//	SET search_path = pf410_probe_poison, public   (pinned on every poisoned-pool connection)
//
// Only connections drawn from the returned pool see the poison. An earlier
// revision instead RENAMED the shared public.resource_locks table and put the
// raising view in its place, which poisoned every other connection sharing the
// database for the width of the window, measured at ~0.23s per run in
// aihub#593: bystander binaries that hit it got 42P01 (between the rename and
// the CREATE VIEW) or 55000 (writes against the stand-in view). The suite's
// class-40 and poison-window retries absorb that at the normal ~0.3% duty
// cycle and visibly break at amplifier duty, so aihub#601 moved the injection
// off the shared schema entirely: same statement, same SQLSTATE, no DDL on
// anything another connection can see.
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
	"os"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// installProbePoison makes the lock-conflict probe statement fail with SQLSTATE
// 40001 on every connection of the pool it RETURNS, and leaves the shared
// public.resource_locks untouched.
//
// The poison is a private schema whose `resource_locks` is a view over a
// set-returning function that RAISEs, pinned ahead of public on the returned
// pool's search_path. Isolation is the point (aihub#601): the previous shape
// renamed the shared table and re-created it as the raising view, so every
// concurrent connection saw the poison for the width of the install/restore
// window (~0.23s per run, measured in aihub#593, surfacing as 42P01/55000 in
// bystanders). Cleanup here is a single DROP SCHEMA CASCADE, and a fixture
// that dies half-installed can no longer strand the database without its
// lock table.
func installProbePoison(t *testing.T, pool *pgxpool.Pool) *pgxpool.Pool {
	t.Helper()
	ctx := context.Background()
	const schema = "pf410_probe_poison"

	for _, ddl := range []string{
		`DROP SCHEMA IF EXISTS ` + schema + ` CASCADE`,
		`CREATE SCHEMA ` + schema,
		// SETOF public.resource_locks keeps the view's row type identical to
		// the real table's, so Describe succeeds and only Execute raises,
		// which is the path the probe exercises.
		`CREATE FUNCTION ` + schema + `.pf410_boom() RETURNS SETOF public.resource_locks
		LANGUAGE plpgsql AS $$
		BEGIN
			RAISE EXCEPTION 'aihub#410 injected probe failure' USING ERRCODE = '40001';
		END $$`,
		`CREATE VIEW ` + schema + `.resource_locks AS SELECT * FROM ` + schema + `.pf410_boom()`,
	} {
		mustExec(t, pool, ddl)
	}
	t.Cleanup(func() { _, _ = pool.Exec(ctx, `DROP SCHEMA IF EXISTS `+schema+` CASCADE`) })

	// Pin search_path on every connection of a NEW pool: the probe's
	// unqualified `FROM resource_locks` resolves to the raising view, and
	// every other relation still finds public. Connections outside this pool
	// never see the schema.
	cfg, err := pgxpool.ParseConfig(os.Getenv("AIHUB_TEST_DB"))
	require.NoError(t, err, "parse AIHUB_TEST_DB")
	cfg.AfterConnect = func(ctx context.Context, conn *pgx.Conn) error {
		_, err := conn.Exec(ctx, `SET search_path = `+schema+`, public`)
		return err
	}
	poisoned, err := pgxpool.NewWithConfig(ctx, cfg)
	require.NoError(t, err, "connect with poisoned search_path")
	t.Cleanup(poisoned.Close)

	// ── Injection self-check, on the statement actually under test ──────────
	// foreignLockHolderSQL is the probe's own SQL (this file is in-package), so
	// this checks the real joins and the real plan shape, not a stand-in
	// `SELECT count(*)` that might be planned differently.
	var a, b, c string
	err = poisoned.QueryRow(ctx, foreignLockHolderSQL,
		"file_scope", []string{"aihub:aihub:probe-selfcheck"}, "", "wi_not_this_one",
	).Scan(&a, &b, &c)
	require.Error(t, err, "the injection did not fire on foreignLockHolderSQL, so this test proves nothing")
	var pgErr *pgconn.PgError
	require.ErrorAs(t, err, &pgErr, "the injected failure is not a Postgres error: %v", err)
	require.Equal(t, "40001", pgErr.Code,
		"the injection fired with SQLSTATE %s rather than 40001, so the arm below would be asserting "+
			"the wrong classification", pgErr.Code)

	// Isolation self-check, from the other side: the shared table must stay
	// readable on the PLAIN pool while the poison is installed. This is the
	// assertion that goes red if the injection ever moves back onto the shared
	// schema (aihub#601): under the old rename shape this SELECT met the
	// raising view (40001) or, mid-install, no relation at all (42P01).
	var n int
	require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM resource_locks`).Scan(&n),
		"public.resource_locks must stay readable by connections outside the poisoned pool "+
			"while the poison is installed (aihub#601)")

	return poisoned
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

	// The subject's claim runs on the poisoned pool; everything before and
	// after this line stays on the plain pool, whose connections never see the
	// injection (aihub#601).
	poisoned := installProbePoison(t, pool)

	_, aerr = claimWI(t, poisoned, uid, subject.ID, "aihub410-subject")

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
