package domain

// DB-gated integration tests for aihub#334: a Postgres transaction-rollback
// error (SQLSTATE 40001 serialization_failure, 40P01 deadlock_detected) must
// leave the server as a 409 the caller can retry, never a 500.
//
// Two write paths, because they are not the same severity. UpdateProject runs
// at the default READ COMMITTED, so its 40001 is latent — it arms the moment
// anyone raises the isolation level on that path. FnClaimWorkItem already opens
// its transaction with pgx.TxOptions{IsoLevel: pgx.Serializable}, so its 40001
// is live today; both were measured on this branch before the fix and both
// returned 500 INTERNAL_ERROR.
//
// Why this can only be tested against a real Postgres: 40001 is produced by the
// server's concurrency control, at the moment a blocked `SELECT ... FOR UPDATE`
// is released by the committing writer that changed the row. Nothing short of
// two real backends racing over one real row can manufacture it, and a unit
// test that hand-builds a *pgconn.PgError would only prove that the classifier
// compiles — not that this SQLSTATE actually arrives on this code path.
//
// Follows the AIHUB_TEST_DB gating pattern of memory_latest_test.go /
// projects_members_cas_db_test.go: setupLatestTestDB SKIPs unless AIHUB_TEST_DB
// is set. That variable is deliberately NOT set on CI's "Unit tests" step, so
// this file runs in its own scoped CI step.
//
//	AIHUB_TEST_DB=postgres://postgres:testpass@localhost:5444/aihub_test?sslmode=disable \
//	go test ./internal/domain/ -run 'TestSerializationFailureSurfacesAsRetryable409' -race -v -count=1
//
// Requires migration 0032_projects_members_version.sql.

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// serializableTestPool returns a second pool onto the same test database whose
// every connection defaults to SERIALIZABLE.
//
// AfterConnect rather than a `default_transaction_isolation` runtime parameter
// in the URL: the runtime-parameter route is silently a no-op if the server
// declines the GUC in the startup packet, which would make every assertion in
// this file pass against READ COMMITTED — i.e. against a database where the
// defect being tested cannot occur. The explicit SET fails loudly instead, and
// requireSerializable below re-reads the setting rather than trusting either.
func serializableTestPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dbURL := os.Getenv("AIHUB_TEST_DB")
	if dbURL == "" {
		t.Skip("set AIHUB_TEST_DB to run this integration test")
	}
	cfg, err := pgxpool.ParseConfig(dbURL)
	require.NoError(t, err)
	cfg.AfterConnect = func(ctx context.Context, c *pgx.Conn) error {
		_, err := c.Exec(ctx, `SET default_transaction_isolation = 'serializable'`)
		return err
	}
	pool, err := pgxpool.NewWithConfig(context.Background(), cfg)
	require.NoError(t, err)
	t.Cleanup(pool.Close)
	return pool
}

// requireIsolation asserts what the pool's connections will actually use. This
// is the control for the whole file: if the pool under test were not really
// SERIALIZABLE, the FOR UPDATE below would simply block and then succeed, the
// 409 assertion would never be reached, and the test would report a lock-timing
// problem instead of "you did not test what you think you tested".
func requireIsolation(t *testing.T, pool *pgxpool.Pool, want string) {
	t.Helper()
	var got string
	require.NoError(t, pool.QueryRow(context.Background(),
		`SELECT current_setting('default_transaction_isolation')`).Scan(&got))
	require.Equal(t, want, got, "this pool is not at the isolation level this test needs")
}

// waitForRowLockWaiter blocks until some other backend on this database is
// parked waiting for a lock on a `SELECT ... FOR UPDATE`, which is how we know
// the subject has reached its row lock and is queued behind the holder.
func waitForRowLockWaiter(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	waitForLockWaiter(t, pool, "%FOR UPDATE%", "a SELECT ... FOR UPDATE")
}

// waitForLockWaiter blocks until some other backend on this database is parked
// waiting for a lock on a statement matching queryLike.
//
// Polling pg_stat_activity rather than sleeping a fixed interval: a sleep that
// is too short releases the holder before the loser has taken its snapshot, and
// the loser then simply succeeds — a green run that measured nothing. The
// pid <> pg_backend_pid() term keeps this query, whose own text contains
// queryLike as a literal, from matching itself.
//
// queryLike is a parameter rather than a fixed 'FOR UPDATE' because the three
// hops this file exercises do not all take their lock the same way: two block
// on an explicit `SELECT ... FOR UPDATE`, while the memory supersede path
// blocks on a plain `UPDATE memories SET status='archived' ...`. Waiting for
// the wrong statement text would time out and report "the writers never
// overlapped" for a test that in fact overlapped perfectly.
func waitForLockWaiter(t *testing.T, pool *pgxpool.Pool, queryLike, describe string) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for {
		var n int
		require.NoError(t, pool.QueryRow(context.Background(), `
			SELECT count(*) FROM pg_stat_activity
			WHERE datname = current_database()
			  AND pid <> pg_backend_pid()
			  AND state = 'active'
			  AND wait_event_type = 'Lock'
			  AND query ILIKE $1`, queryLike).Scan(&n))
		if n > 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("no backend ever blocked on %s within 20s, so the two writers never "+
				"overlapped and this test proved nothing", describe)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// raceUpdateAgainstHolder runs the interleaving both subtests need:
//
//	holder: BEGIN; UPDATE projects SET description=... (uncommitted, row locked)
//	loser:  UpdateProject(...) on updatePool -> blocks on SELECT ... FOR UPDATE
//	holder: COMMIT
//	loser:  unblocks
//
// and returns whatever the loser got. The holder writes with raw SQL rather
// than through UpdateProject so that a bug in the function under test cannot
// also break the fixture that is supposed to expose it.
func raceUpdateAgainstHolder(t *testing.T, seedPool, updatePool *pgxpool.Pool, project string, caller *UserRecord) *AihubError {
	t.Helper()
	ctx := context.Background()

	holder, err := seedPool.Acquire(ctx)
	require.NoError(t, err)
	defer holder.Release()
	tx, err := holder.Begin(ctx)
	require.NoError(t, err)
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback(ctx)
		}
	}()
	_, err = tx.Exec(ctx, `UPDATE projects SET description = 'held by the winner' WHERE name = $1`, project)
	require.NoError(t, err)

	got := make(chan *AihubError, 1)
	go func() {
		members := []MemberInput{{UserID: "u_loser", Role: "viewer"}}
		_, aerr := UpdateProject(ctx, updatePool, project, caller, UpdateProjectRequest{Members: &members})
		got <- aerr
	}()

	waitForRowLockWaiter(t, seedPool)
	require.NoError(t, tx.Commit(ctx))
	committed = true

	select {
	case aerr := <-got:
		return aerr
	case <-time.After(60 * time.Second):
		t.Fatal("the losing UpdateProject never returned after the holder committed")
		return nil
	}
}

// TestSerializationFailureSurfacesAsRetryable409 is aihub#334's acceptance
// criterion on both affected write paths, plus the READ COMMITTED reference arm
// that keeps them honest.
//
// All three subtests run the same interleaving — a writer holds the row in an
// uncommitted transaction, the subject blocks on SELECT ... FOR UPDATE, the
// holder commits. They differ in the subject (UpdateProject, then
// FnClaimWorkItem) and, in the third, only in the isolation level the loser
// ends up at: it is UpdateProject again, at the READ COMMITTED default, and it
// must still SUCCEED. That third arm is what makes the first two evidence — had
// the change turned every contended write into a 409, it goes red.
func TestSerializationFailureSurfacesAsRetryable409(t *testing.T) {
	pool := setupLatestTestDB(t)
	requireIsolation(t, pool, "read committed")

	t.Run("serializable loser gets a retryable 409, not a 500", func(t *testing.T) {
		serPool := serializableTestPool(t)
		requireIsolation(t, serPool, "serializable")

		u := testUser(t, pool)
		project := casProject(t, pool, u)
		caller := &UserRecord{ID: u, Role: "admin"}

		aerr := raceUpdateAgainstHolder(t, pool, serPool, project, caller)

		require.NotNil(t, aerr, "the loser of a serializable conflict must report an error, not silently drop the write")
		assert.Equal(t, 409, aerr.HTTPStatus,
			"SQLSTATE 40001 means \"retry and it will work\"; a 500 tells the caller the server is broken "+
				"and sends them to the logs instead. got %d %s: %s", aerr.HTTPStatus, aerr.Code, aerr.Message)
		assert.Equal(t, ErrConflictSerializationFailure, aerr.Code,
			"the code must name a retryable conflict, got %s: %s", aerr.Code, aerr.Message)
		assert.NotEqual(t, ErrInternalError, aerr.Code)

		details, ok := aerr.Details.(map[string]any)
		require.True(t, ok, "the conflict must carry machine-readable retry guidance; got %#v", aerr.Details)
		assert.Equal(t, true, details["retryable"],
			"a caller deciding whether to retry must not have to parse the message")
		assert.Equal(t, "40001", details["sqlstate"],
			"the originating SQLSTATE must survive, or the 409 is undiagnosable; got %#v", details["sqlstate"])
	})

	// FnClaimWorkItem opens its transaction with
	// pgx.TxOptions{IsoLevel: pgx.Serializable} and then takes the same kind of
	// row lock, so this half of the defect needs no future isolation-level
	// change to become reachable: it is reachable now, on pf_claim_work_item,
	// the busiest write path there is. Measured on this branch before the fix:
	//
	//	HTTPStatus=500 Code=INTERNAL_ERROR
	//	Message=failed to lock work_item: ERROR: could not serialize access due
	//	        to concurrent update (SQLSTATE 40001)
	//
	// A subtest rather than its own Test function on purpose: dbtestcov counts
	// DB-gated FUNCTIONS, and one more function would mean another entry in its
	// gated-test manifest for the same single guard.
	t.Run("concurrent claim gets a retryable 409, not a 500", func(t *testing.T) {
		ctx := context.Background()
		u := testUser(t, pool)
		project := testProject(t, pool, u)
		wi := seedWI(t, pool, project, u)

		holder, err := pool.Acquire(ctx)
		require.NoError(t, err)
		defer holder.Release()
		tx, err := holder.Begin(ctx)
		require.NoError(t, err)
		committed := false
		defer func() {
			if !committed {
				_ = tx.Rollback(ctx)
			}
		}()
		_, err = tx.Exec(ctx, `UPDATE work_items SET goal = 'held by the winner' WHERE id = $1`, wi.ID)
		require.NoError(t, err)

		got := make(chan *AihubError, 1)
		go func() {
			_, aerr := FnClaimWorkItem(ctx, pool, wi.ID, &ClaimRequest{
				IdempotencyKey: "aihub334-loser",
				SessionInfo: SessionInfo{
					MachineID:     "m1",
					SessionSecret: "s3cr3t-0123456789abcdef0123456789abcdef0123456789abcdef01234567",
				},
			}, u, "", "tester")
			got <- aerr
		}()

		waitForRowLockWaiter(t, pool)
		require.NoError(t, tx.Commit(ctx))
		committed = true

		var aerr *AihubError
		select {
		case aerr = <-got:
		case <-time.After(60 * time.Second):
			t.Fatal("the losing claim never returned after the holder committed")
		}

		require.NotNil(t, aerr, "the loser of a serializable claim race must report an error")
		assert.Equal(t, 409, aerr.HTTPStatus,
			"a claim that lost a serialization race must be retryable, not a broken server. got %d %s: %s",
			aerr.HTTPStatus, aerr.Code, aerr.Message)
		assert.Equal(t, ErrConflictSerializationFailure, aerr.Code)
	})

	// Instance 3, and the one a central pgx-error -> AppError conversion point
	// does NOT reach. unblockDependentWI runs inside FnCompleteAttempt's
	// SERIALIZABLE transaction and takes `SELECT ... ORDER BY id FOR UPDATE` on
	// every wi that the completing wi was blocking — but it DISCARDS that
	// query's error (`if err != nil { return nil }`), and FnCompleteAttempt in
	// turn discards unblockDependentWI's return value as "non-fatal". So a
	// 40001 there is swallowed twice: the transaction is already aborted, every
	// later statement is a no-op, and the failure only reappears at tx.Commit —
	// where pgx reports pgx.ErrTxCommitRollback, which is NOT a *pgconn.PgError
	// and carries no SQLSTATE at all. Measured on this branch with the
	// classifier wired into pgxErr and into all six PgError-shaped hops:
	//
	//	HTTPStatus=500 Code=INTERNAL_ERROR
	//	Message=failed to commit complete_attempt
	//
	// That is why this arm exists as its own assertion instead of being folded
	// into the two above: they and it fail for different reasons and are fixed
	// in different places, and a fix that only classifies *pgconn.PgError
	// leaves this one green-tested and still returning 500.
	// (No apostrophe in the subtest name: ci.yml asserts on the exact
	// `--- PASS:` line inside a single-quoted shell list.)
	t.Run("unblock sweep row-lock error still must not 500", func(t *testing.T) {
		ctx := context.Background()
		u := testUser(t, pool)
		project := testProject(t, pool, u)
		wis := seedWIs(t, pool, project, u, 2)
		blocking, blocked := wis[0], wis[1]
		createBlocksDep(t, pool, blocked.ID, blocking.ID, u)
		const secret = "aihub334-unblock-secret"
		attemptID := seedRunAttempt(t, pool, blocking.ID, u, secret)

		// Hold the BLOCKED wi's row — the row only the unblock sweep touches,
		// so FnCompleteAttempt gets all the way past its own FOR UPDATE and
		// its own writes before it collides. Holding the completing wi's row
		// instead would trip the very first hop and prove nothing new.
		holder, err := pool.Acquire(ctx)
		require.NoError(t, err)
		defer holder.Release()
		tx, err := holder.Begin(ctx)
		require.NoError(t, err)
		committed := false
		defer func() {
			if !committed {
				_ = tx.Rollback(ctx)
			}
		}()
		_, err = tx.Exec(ctx, `UPDATE work_items SET goal = 'held by the winner' WHERE id = $1`, blocked.ID)
		require.NoError(t, err)

		got := make(chan *AihubError, 1)
		go func() {
			got <- FnCompleteAttempt(ctx, pool, blocking.ID, &CompleteAttemptRequest{
				AttemptID:     attemptID,
				ClaimEpoch:    1,
				SessionSecret: secret,
				Status:        "wrapped",
			})
		}()

		waitForRowLockWaiter(t, pool)
		require.NoError(t, tx.Commit(ctx))
		committed = true

		var aerr *AihubError
		select {
		case aerr = <-got:
		case <-time.After(60 * time.Second):
			t.Fatal("the losing complete_attempt never returned after the holder committed")
		}

		require.NotNil(t, aerr, "the losing transaction was rolled back, so complete_attempt cannot report success")
		assert.Equal(t, 409, aerr.HTTPStatus,
			"the unblock sweep lost a serialization race and the whole transaction rolled back; that is "+
				"retryable, not a broken server. got %d %s: %s", aerr.HTTPStatus, aerr.Code, aerr.Message)
		assert.Equal(t, ErrConflictSerializationFailure, aerr.Code,
			"got %s: %s — a classifier that only recognises *pgconn.PgError cannot see this one, because "+
				"the swallow upstream means the error reaching the caller is pgx.ErrTxCommitRollback",
			aerr.Code, aerr.Message)
	})

	// Instance 2. Remember's supersede path opens its own transaction and, when
	// it loses a race, does so at `UPDATE memories SET status='archived' ...` —
	// a plain DML statement, not a `SELECT ... FOR UPDATE` and not a commit. It
	// is here to keep the fix from being scoped to the two hop shapes the other
	// arms happen to use. Measured on this branch before the fix:
	//
	//	HTTPStatus=500 Code=INTERNAL_ERROR
	//	Message=failed to archive head: ERROR: could not serialize access due to
	//	        concurrent update (SQLSTATE 40001)
	//
	// Remember returns a plain `error`, not an *AihubError, so this arm also
	// checks that the 409 survives that widening — a caller that cannot type
	// assert it back gets the same 500 in practice.
	t.Run("memory supersede loser gets a retryable 409, not a 500", func(t *testing.T) {
		ctx := context.Background()
		serPool := serializableTestPool(t)
		requireIsolation(t, serPool, "serializable")

		u := testUser(t, pool)
		project := testProject(t, pool, u)

		memA, _, rerr := Remember(ctx, pool, &RememberRequest{
			Project: project, Type: "fact.note", Content: "the head this test races over",
			Visibility: "project", DedupMode: "off",
			CallerUserID: u, CallerDisplay: u,
		})
		require.NoError(t, rerr)

		holder, err := pool.Acquire(ctx)
		require.NoError(t, err)
		defer holder.Release()
		tx, err := holder.Begin(ctx)
		require.NoError(t, err)
		committed := false
		defer func() {
			if !committed {
				_ = tx.Rollback(ctx)
			}
		}()
		_, err = tx.Exec(ctx, `UPDATE memories SET content = 'held by the winner' WHERE id = $1`, memA.ID)
		require.NoError(t, err)

		got := make(chan error, 1)
		go func() {
			_, _, err := Remember(ctx, serPool, &RememberRequest{
				Project: project, Type: "fact.note", Content: "the version that loses the race",
				Visibility: "project", DedupMode: "off",
				CallerUserID: u, CallerDisplay: u,
				SupersedesMemID: strp(memA.ID),
			})
			got <- err
		}()

		waitForLockWaiter(t, pool, "%UPDATE memories%", "the supersede archive UPDATE")
		require.NoError(t, tx.Commit(ctx))
		committed = true

		var loser error
		select {
		case loser = <-got:
		case <-time.After(60 * time.Second):
			t.Fatal("the losing supersede never returned after the holder committed")
		}

		require.Error(t, loser, "the loser of a serializable supersede must report an error, not silently branch the lineage")
		var aerr *AihubError
		require.ErrorAs(t, loser, &aerr,
			"Remember widens its return to plain error; if the 409 does not survive that, every caller "+
				"still sees an unclassified failure. got %T: %v", loser, loser)
		assert.Equal(t, 409, aerr.HTTPStatus,
			"got %d %s: %s", aerr.HTTPStatus, aerr.Code, aerr.Message)
		assert.Equal(t, ErrConflictSerializationFailure, aerr.Code,
			"got %s: %s", aerr.Code, aerr.Message)
	})

	// aihub#497. FnForceTakeover is the one lock path that opens its transaction
	// with pool.Begin — READ COMMITTED — so aihub#334 classified the statements on
	// it but nothing on it had ever actually been raced. aihub#451 raised the
	// isolation level as a mutant and found a second swallow underneath the first:
	// the 40001 arrives at acquireLockUpsert, whose error FnForceTakeover discarded
	// unless it was the typed lock refusal. The transaction was therefore already
	// aborted when `UPDATE work_items` ran, and the caller was handed
	//
	//	500 INTERNAL_ERROR  failed to update work_item after force_takeover:
	//	                    current transaction is aborted (SQLSTATE 25P02)
	//
	// 25P02 is class 25, so no classifier anywhere on the path could see it: the
	// caller is told about the SECOND victim, and told the server is broken.
	//
	// This arm runs on serializableTestPool for exactly the reason the
	// UpdateProject arm at the top of this file does. internal/db/db.go builds the
	// production pool with pgxpool.New(ctx, dsn) and pins no isolation level, so
	// `pool.Begin` here inherits whatever default_transaction_isolation the
	// database, the role or the DSN carries. Handing this function a
	// SERIALIZABLE-defaulted pool is therefore a CONFIGURATION of today's binary,
	// not a code change — which is what makes this 40001 reachable rather than
	// hypothetical, and it is the same argument this file already accepted for
	// UpdateProject.
	//
	// The contended row is an ORPHAN (its owner wrapped), which is the
	// displacement lockUpsertSQL exists to perform, so the aihub#393 predicate
	// permits the update and the upsert really does reach for the row lock. Owned
	// by a LIVE foreign attempt it would be refused with CONFLICT_LOCK_TAKEN
	// before any serialization failure could occur, and this arm would be
	// measuring aihub#393 instead.
	t.Run("force takeover lock upsert gets a retryable 409, not a 500", func(t *testing.T) {
		ctx := context.Background()
		serPool := serializableTestPool(t)
		requireIsolation(t, serPool, "serializable")

		uid := testUser(t, pool)
		proj := testProject(t, pool, uid)
		other := ftsSecondUser(t, pool, uid)

		wiB, bAttempt := ftcwSeedTakenOverWI(t, pool, proj, uid,
			"B: force-taken over while the contended lock row is being rewritten",
			"repo-a", "contested_by_497.go", "idem-497-b")

		// A held the row and then ended, so what the upsert faces is the orphan
		// reclaim it is entitled to perform rather than a live foreign holder.
		wiA := seedWIWithResources(t, pool, proj, uid,
			"A: ended holder of the contended path", json.RawMessage(`[]`))
		aAttempt := cwRunningAttemptWithoutLocks(t, pool, uid, wiA.ID, "idem-497-a")
		key := proj + ":repo-a:contested_by_497.go"
		commitLock := cwInsertLockInOpenTx(t, pool, key, aAttempt)
		commitLock()
		cwSetAttemptStatus(t, pool, aAttempt, "wrapped")
		require.Equal(t, aAttempt, ftsLockOwner(t, pool, key),
			"the fixture must start with the orphan row in place, or the upsert below never "+
				"reaches for a row lock and this arm measures nothing")

		// This arm ends with the takeover REFUSED, so the contended row is left
		// owned by A's wrapped attempt — i.e. an orphan lock. RunOrphanLockSweep is
		// global, while TestLockEventsDB_EveryMutationSiteEmits/orphan_sweep_release
		// counts the orphans of ONE work item and pins the sweep's Affected to that
		// number, so any row left behind here fails that assertion depending on the
		// order the two run in. Clean up rather than leaving global state around.
		t.Cleanup(func() {
			_, cerr := pool.Exec(context.Background(), `
				DELETE FROM resource_locks WHERE resource_type = 'file_scope' AND resource_key = $1`, key)
			assert.NoError(t, cerr, "failed to clean up the contended lock row")
		})

		// The holder: the same row, rewritten and left uncommitted. Raw SQL rather
		// than a second domain call, so a bug in the function under test cannot
		// also break the fixture meant to expose it.
		holder, err := pool.Acquire(ctx)
		require.NoError(t, err)
		defer holder.Release()
		tx, err := holder.Begin(ctx)
		require.NoError(t, err)
		committed := false
		defer func() {
			if !committed {
				_ = tx.Rollback(ctx)
			}
		}()
		_, err = tx.Exec(ctx, `
			UPDATE resource_locks SET claim_epoch = claim_epoch + 1
			WHERE resource_type = 'file_scope' AND resource_key = $1`, key)
		require.NoError(t, err)

		got := make(chan ftcwResult, 1)
		go func() {
			resp, aerr := ftcwTakeover(serPool, wiB.ID, other, proj, "aihub#497 serialization measurement")
			got <- ftcwResult{resp, aerr}
		}()

		waitForLockWaiter(t, pool, "%INSERT INTO resource_locks%",
			"the force takeover's lock upsert (INSERT INTO resource_locks ... ON CONFLICT)")
		require.NoError(t, tx.Commit(ctx))
		committed = true

		var out ftcwResult
		select {
		case out = <-got:
		case <-time.After(60 * time.Second):
			t.Fatal("the blocked force takeover never returned after the holder committed")
		}

		require.NotNil(t, out.aerr,
			"the takeover's transaction lost a serialization race at the lock upsert, so it cannot "+
				"report success; got resp=%#v", out.resp)
		assert.Equal(t, 409, out.aerr.HTTPStatus,
			"a force takeover that lost a class-40 race must be retryable, not a broken server. got %d %s: %s",
			out.aerr.HTTPStatus, out.aerr.Code, out.aerr.Message)
		assert.Equal(t, ErrConflictSerializationFailure, out.aerr.Code,
			"got %s: %s — an INTERNAL_ERROR naming `failed to update work_item after force_takeover` "+
				"means the upsert error is being discarded again and the caller is being told about "+
				"the statement that merely ran second", out.aerr.Code, out.aerr.Message)
		// Discriminating on purpose: dbErr keeps the driver text out of the
		// message, so the pre-fix 500 mentions no SQLSTATE at all and asserting
		// the ABSENCE of "25P02" would have passed before the fix as well. What
		// differs is WHICH statement the caller is told about — the upsert that
		// lost the race, or the UPDATE that merely ran after it.
		assert.NotContains(t, out.aerr.Message, "failed to update work_item",
			"the caller is being told about `UPDATE work_items`, which only failed with 25P02 because "+
				"the lock upsert had already aborted the transaction; the error must name the "+
				"class-40 cause instead")
		assert.Contains(t, out.aerr.Message, "40001",
			"the class-40 SQLSTATE must survive into the message the caller reads; got %q", out.aerr.Message)

		details, ok := out.aerr.Details.(map[string]any)
		require.True(t, ok, "the conflict must carry machine-readable retry guidance; got %#v", out.aerr.Details)
		assert.Equal(t, true, details["retryable"],
			"a caller deciding whether to retry must not have to parse the message")
		assert.Equal(t, "40001", details["sqlstate"],
			"the originating SQLSTATE must survive, or the 409 is undiagnosable; got %#v", details["sqlstate"])

		// FnForceTakeover supersedes the prior attempt and releases its locks
		// BEFORE it re-derives, so a failure that is not atomic leaves B with no
		// running attempt at all.
		assert.Equal(t, "running", ftsAttemptStatus(t, pool, bAttempt),
			"B's attempt %q is not running after the failed takeover; a 409 must leave the work "+
				"item exactly as it was", bAttempt)
	})

	// aihub#497, second hop. The arm above races the lock UPSERT; this one races
	// the DELETE that runs before it, so the fix is not pinned to one statement
	// shape. It is here for the reason the memory-supersede arm above is: a fix
	// that happens to work at the hop the first arm exercises, and nowhere else,
	// passes with one arm and fails in production at the other.
	//
	// The interleaving differs in what the prior attempt owns. B's prior attempt
	// holds a lock row on a key B does NOT declare, so releaseLocks' `DELETE FROM
	// resource_locks WHERE owner_attempt_id = <prior>` matches it and blocks on
	// the uncommitted holder, while the upsert loop further down never touches
	// that key. Before aihub#497 the 40001 was discarded here too, and the caller
	// was told `500 INTERNAL_ERROR failed to create new attempt after
	// force_takeover` — a third statement, two hops downstream of the one that
	// actually lost the race.
	t.Run("force takeover lock release gets a retryable 409, not a 500", func(t *testing.T) {
		ctx := context.Background()
		serPool := serializableTestPool(t)
		requireIsolation(t, serPool, "serializable")

		uid := testUser(t, pool)
		proj := testProject(t, pool, uid)
		other := ftsSecondUser(t, pool, uid)

		wiB, bAttempt := ftcwSeedTakenOverWI(t, pool, proj, uid,
			"B: force-taken over while its prior attempt's lock row is held",
			"repo-a", "declared_by_497b.go", "idem-497b-b")

		// A key the prior attempt owns and the takeover does NOT re-derive, so
		// only releaseLocks reaches it.
		relKey := proj + ":repo-a:released_by_497b.go"
		commitRel := cwInsertLockInOpenTx(t, pool, relKey, bAttempt)
		commitRel()
		require.Equal(t, bAttempt, ftsLockOwner(t, pool, relKey),
			"the fixture must start with the prior attempt owning %q, or the DELETE below "+
				"matches no row and never blocks", relKey)
		t.Cleanup(func() {
			_, cerr := pool.Exec(context.Background(), `
				DELETE FROM resource_locks WHERE resource_type = 'file_scope' AND resource_key = $1`, relKey)
			assert.NoError(t, cerr, "failed to clean up the contended lock row")
		})

		holder, err := pool.Acquire(ctx)
		require.NoError(t, err)
		defer holder.Release()
		tx, err := holder.Begin(ctx)
		require.NoError(t, err)
		committed := false
		defer func() {
			if !committed {
				_ = tx.Rollback(ctx)
			}
		}()
		_, err = tx.Exec(ctx, `
			UPDATE resource_locks SET claim_epoch = claim_epoch + 1
			WHERE resource_type = 'file_scope' AND resource_key = $1`, relKey)
		require.NoError(t, err)

		got := make(chan ftcwResult, 1)
		go func() {
			resp, aerr := ftcwTakeover(serPool, wiB.ID, other, proj, "aihub#497 release-hop measurement")
			got <- ftcwResult{resp, aerr}
		}()

		waitForLockWaiter(t, pool, "%DELETE FROM resource_locks%",
			"the force takeover's lock release (DELETE FROM resource_locks)")
		require.NoError(t, tx.Commit(ctx))
		committed = true

		var out ftcwResult
		select {
		case out = <-got:
		case <-time.After(60 * time.Second):
			t.Fatal("the blocked force takeover never returned after the holder committed")
		}

		require.NotNil(t, out.aerr,
			"the takeover's transaction lost a serialization race at the lock release, so it cannot "+
				"report success; got resp=%#v", out.resp)
		assert.Equal(t, 409, out.aerr.HTTPStatus,
			"got %d %s: %s", out.aerr.HTTPStatus, out.aerr.Code, out.aerr.Message)
		assert.Equal(t, ErrConflictSerializationFailure, out.aerr.Code,
			"got %s: %s — an INTERNAL_ERROR here means releaseLocks' error is being discarded "+
				"wholesale again and the caller is being told about a later statement",
			out.aerr.Code, out.aerr.Message)
		assert.NotContains(t, out.aerr.Message, "failed to create new attempt",
			"the caller is being told about `INSERT INTO run_attempts`, which only failed with 25P02 "+
				"because the lock release had already aborted the transaction")

		details, ok := out.aerr.Details.(map[string]any)
		require.True(t, ok, "the conflict must carry machine-readable retry guidance; got %#v", out.aerr.Details)
		assert.Equal(t, "40001", details["sqlstate"],
			"the originating SQLSTATE must survive; got %#v", details["sqlstate"])

		assert.Equal(t, "running", ftsAttemptStatus(t, pool, bAttempt),
			"B's attempt %q is not running after the failed takeover; a 409 must leave the work "+
				"item exactly as it was", bAttempt)
	})

	// aihub#545, first of three: the credential heartbeat. verifyAttemptCredential
	// ends with `UPDATE run_attempts SET last_active_at=...`, whose error was
	// discarded outright (`//nolint:errcheck`). That function runs inside four
	// SERIALIZABLE transactions and the UPDATE writes run_attempts — the very
	// table those transactions take SIReadLocks on — so a 40001 there is live,
	// not latent. Before the fix the swallow left the transaction dead and the
	// caller was told about the NEXT statement:
	//
	//	500 INTERNAL_ERROR  failed to update run_attempt status
	//	                    (current transaction is aborted, SQLSTATE 25P02)
	//
	// The race: a holder rewrites the attempt's own run_attempts row and sits on
	// it uncommitted. The completing transaction gets past its work_items FOR
	// UPDATE (untouched table) and past the credential SELECT (plain MVCC read,
	// does not block), and parks exactly at the heartbeat UPDATE.
	t.Run("complete attempt heartbeat gets a retryable 409, not a 500", func(t *testing.T) {
		ctx := context.Background()
		u := testUser(t, pool)
		project := testProject(t, pool, u)
		wi := seedWI(t, pool, project, u)
		const secret = "aihub545-heartbeat-secret"
		attemptID := seedRunAttempt(t, pool, wi.ID, u, secret)

		holder, err := pool.Acquire(ctx)
		require.NoError(t, err)
		defer holder.Release()
		tx, err := holder.Begin(ctx)
		require.NoError(t, err)
		committed := false
		defer func() {
			if !committed {
				_ = tx.Rollback(ctx)
			}
		}()
		_, err = tx.Exec(ctx, `UPDATE run_attempts SET machine_id = 'held-by-the-winner' WHERE id = $1`, attemptID)
		require.NoError(t, err)

		got := make(chan *AihubError, 1)
		go func() {
			got <- FnCompleteAttempt(ctx, pool, wi.ID, &CompleteAttemptRequest{
				AttemptID:     attemptID,
				ClaimEpoch:    1,
				SessionSecret: secret,
				Status:        "paused",
			})
		}()

		waitForLockWaiter(t, pool, "%last_active_at%",
			"the credential heartbeat (UPDATE run_attempts SET last_active_at)")
		require.NoError(t, tx.Commit(ctx))
		committed = true

		var aerr *AihubError
		select {
		case aerr = <-got:
		case <-time.After(60 * time.Second):
			t.Fatal("the losing complete_attempt never returned after the holder committed")
		}

		require.NotNil(t, aerr, "the heartbeat lost a serialization race and the whole transaction "+
			"rolled back, so complete_attempt cannot report success")
		assert.Equal(t, 409, aerr.HTTPStatus,
			"got %d %s: %s", aerr.HTTPStatus, aerr.Code, aerr.Message)
		assert.Equal(t, ErrConflictSerializationFailure, aerr.Code,
			"got %s: %s — an INTERNAL_ERROR here means the heartbeat's error is being discarded "+
				"again and the caller is being told about whichever statement ran second",
			aerr.Code, aerr.Message)
		assert.NotContains(t, aerr.Message, "failed to update run_attempt status",
			"the caller is being told about the status UPDATE, which only failed with 25P02 because "+
				"the swallowed heartbeat 40001 had already aborted the transaction")
		assert.Contains(t, aerr.Message, "40001",
			"the class-40 SQLSTATE must survive into the message the caller reads; got %q", aerr.Message)

		details, ok := aerr.Details.(map[string]any)
		require.True(t, ok, "the conflict must carry machine-readable retry guidance; got %#v", aerr.Details)
		assert.Equal(t, "40001", details["sqlstate"],
			"the originating SQLSTATE must survive; got %#v", details["sqlstate"])
	})

	// aihub#545, second of three: FnCompleteAttempt's own wi_step_state read —
	// the sibling of the two reads aihub#492 (claim path) and aihub#497
	// (takeover path) already classified, missed because the class-40 sweep
	// searched by spelling rather than by property. A plain SELECT never blocks
	// on a row lock, so the interleaving the other arms use cannot reach it;
	// what CAN is SSI's read-time dangerous-structure check, arranged
	// deterministically by ssiDoomStepStateRead below. Before the fix the 40001
	// raised at the read made stepErr non-nil, the guard read that as "no step
	// in progress", and the caller was told about the next statement:
	//
	//	500 INTERNAL_ERROR  failed to update run_attempt status  (25P02)
	t.Run("complete attempt step-state read gets a retryable 409, not a 500", func(t *testing.T) {
		ctx := context.Background()
		u := testUser(t, pool)
		project := testProject(t, pool, u)
		wi := seedWI(t, pool, project, u)
		const secret = "aihub545-stepread-secret"
		attemptID := seedRunAttempt(t, pool, wi.ID, u, secret)
		seedStepState(t, pool, wi.ID, "idle", nil)

		// Park the subject at its FIRST statement (the work_items FOR UPDATE) so
		// its snapshot is pinned before the pivot commits. The parker only LOCKS
		// the row — no write — so on release the subject proceeds on its original
		// snapshot instead of failing there.
		parker, err := pool.Acquire(ctx)
		require.NoError(t, err)
		defer parker.Release()
		parkTx, err := parker.Begin(ctx)
		require.NoError(t, err)
		parkReleased := false
		defer func() {
			if !parkReleased {
				_ = parkTx.Rollback(ctx)
			}
		}()
		var lockedID string
		require.NoError(t, parkTx.QueryRow(ctx,
			`SELECT id FROM work_items WHERE id = $1 FOR UPDATE`, wi.ID).Scan(&lockedID))

		commitPivot := ssiDoomStepStateRead(t, pool, project, wi.ID)

		got := make(chan *AihubError, 1)
		go func() {
			got <- FnCompleteAttempt(ctx, pool, wi.ID, &CompleteAttemptRequest{
				AttemptID:     attemptID,
				ClaimEpoch:    1,
				SessionSecret: secret,
				Status:        "paused",
			})
		}()

		waitForRowLockWaiter(t, pool)
		// The subject has taken its snapshot; complete the dangerous structure,
		// then let the subject run into it.
		commitPivot()
		require.NoError(t, parkTx.Commit(ctx))
		parkReleased = true

		var aerr *AihubError
		select {
		case aerr = <-got:
		case <-time.After(60 * time.Second):
			t.Fatal("the doomed complete_attempt never returned after the parker committed")
		}

		require.NotNil(t, aerr, "the step-state read lost a serialization race and the whole "+
			"transaction rolled back, so complete_attempt cannot report success")
		assert.Equal(t, 409, aerr.HTTPStatus,
			"got %d %s: %s", aerr.HTTPStatus, aerr.Code, aerr.Message)
		assert.Equal(t, ErrConflictSerializationFailure, aerr.Code,
			"got %s: %s — an INTERNAL_ERROR here means the read's error is being folded into "+
				"\"no step in progress\" again and the caller is being told about a later statement",
			aerr.Code, aerr.Message)
		assert.NotContains(t, aerr.Message, "failed to update run_attempt status",
			"the caller is being told about the status UPDATE, which only failed with 25P02 because "+
				"the swallowed step-state 40001 had already aborted the transaction")
		details, ok := aerr.Details.(map[string]any)
		require.True(t, ok, "the conflict must carry machine-readable retry guidance; got %#v", aerr.Details)
		assert.Equal(t, "40001", details["sqlstate"],
			"the originating SQLSTATE must survive; got %#v", details["sqlstate"])
	})

	// aihub#545, third of three: fnForceTerminateStep's opening current-step
	// read, whose error was discarded outright (`//nolint:errcheck`) so a
	// class-40 rollback came back as "no step to terminate" — nil. That nil is
	// what made aihub#497's guard arm at the FnForceTakeover call site
	// structurally unreachable for this hop: the arm matches
	// ErrConflictSerializationFailure on this function's RETURN, and a discarded
	// 40001 never becomes a return value.
	//
	// Called directly (same package) on a transaction pre-doomed by
	// ssiDoomStepStateRead, because no external interleaving can park a caller
	// between ITS step-state read and this one — they are adjacent statements.
	// The function-level contract is exactly what both call sites consume, and
	// asserting the CODE here is asserting the aihub#497 arm's match condition.
	t.Run("force terminate current-step read is classified, not swallowed as no step", func(t *testing.T) {
		ctx := context.Background()
		u := testUser(t, pool)
		project := testProject(t, pool, u)
		wi := seedWI(t, pool, project, u)
		const secret = "aihub545-ft-secret"
		attemptID := seedRunAttempt(t, pool, wi.ID, u, secret)
		saID := "sa_aihub545"
		seedStepState(t, pool, wi.ID, "in_progress", &saID)

		subj, err := pool.Acquire(ctx)
		require.NoError(t, err)
		defer subj.Release()
		sTx, err := subj.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
		require.NoError(t, err)
		defer sTx.Rollback(ctx) //nolint:errcheck

		// Pin the subject's snapshot on a table the pivot never writes, BEFORE
		// the pivot commits — the read under test must be the transaction's
		// first visit to the doomed tuple.
		var one int
		require.NoError(t, sTx.QueryRow(ctx, `SELECT 1 FROM work_items WHERE id = $1`, wi.ID).Scan(&one))

		commitPivot := ssiDoomStepStateRead(t, pool, project, wi.ID)
		commitPivot()

		aerr := fnForceTerminateStep(ctx, sTx, wi.ID, attemptID, &saID)

		require.NotNil(t, aerr, "the current-step read lost a class-40 race; answering nil says "+
			"\"no step to terminate\" and leaves the caller to run the rest of the operation "+
			"inside a dead transaction")
		assert.Equal(t, ErrConflictSerializationFailure, aerr.Code,
			"got %s: %s — this exact code is what FnForceTakeover's aihub#497 guard arm matches "+
				"on; anything else keeps that arm unreachable for this hop", aerr.Code, aerr.Message)
		assert.Equal(t, 409, aerr.HTTPStatus,
			"got %d %s: %s", aerr.HTTPStatus, aerr.Code, aerr.Message)
		assert.Contains(t, aerr.Message, "40001",
			"the class-40 SQLSTATE must survive into the message the caller reads; got %q", aerr.Message)
		details, ok := aerr.Details.(map[string]any)
		require.True(t, ok, "the conflict must carry machine-readable retry guidance; got %#v", aerr.Details)
		assert.Equal(t, "40001", details["sqlstate"],
			"the originating SQLSTATE must survive; got %#v", details["sqlstate"])
	})

	t.Run("read committed loser still succeeds", func(t *testing.T) {
		u := testUser(t, pool)
		project := casProject(t, pool, u)
		caller := &UserRecord{ID: u, Role: "admin"}

		aerr := raceUpdateAgainstHolder(t, pool, pool, project, caller)

		require.Nil(t, aerr,
			"at READ COMMITTED a contended update waits for the lock and then succeeds; turning "+
				"ordinary lock waiting into a 409 would break every existing caller. got %v", aerr)

		fresh, gerr := GetProject(context.Background(), pool, project, caller, "")
		require.Nil(t, gerr)
		assert.Equal(t, []string{"u_loser"}, casMembersOf(t, fresh.Members),
			"the write that waited for the lock must actually have landed")
	})
}

// seedStepState inserts a wi_step_state row directly, the way a claim's upsert
// would leave it, so the aihub#545 arms have a real tuple for their reads to
// visit. stepAttempt non-nil seeds an in_progress step named "implement".
func seedStepState(t *testing.T, pool *pgxpool.Pool, wiID, status string, stepAttempt *string) {
	t.Helper()
	var currentStep *string
	if status == "in_progress" {
		s := "implement"
		currentStep = &s
	}
	_, err := pool.Exec(context.Background(), `
		INSERT INTO wi_step_state (work_item_id, wi_type, graph_source, current_step,
		                           current_step_status, current_step_attempt)
		VALUES ($1, 'feature', 'scenario_config', $2, $3, $4)
		ON CONFLICT (work_item_id) DO UPDATE
		  SET current_step=$2, current_step_status=$3, current_step_attempt=$4`,
		wiID, currentStep, status, stepAttempt)
	require.NoError(t, err)
}

// ssiDoomStepStateRead arranges the one SSI shape that dooms a PLAIN SELECT —
// the statement shape none of this file's lock-wait interleavings can reach,
// because an MVCC read neither blocks nor takes a row lock.
//
// Postgres cancels a SERIALIZABLE reader at the read itself ("Canceled on
// conflict out to old pivot", SQLSTATE 40001) when it visits a tuple superseded
// by a concurrent COMMITTED transaction P that already carries a read-write
// conflict out to some T which committed before the reader's snapshot. This
// helper builds exactly that:
//
//	P (SERIALIZABLE): reads the project row            <- P's snapshot
//	T (SERIALIZABLE): overwrites that row, COMMITS     <- P -> T conflict out
//	<caller pins the subject's snapshot>               <- T is now "old"
//	commitPivot: P overwrites wiID's wi_step_state row, COMMITS
//	subject: first read of that wi_step_state row      -> 40001, at the read
//
// The aux row is the test's own projects row: nothing on the complete-attempt
// or force-terminate paths reads or writes `projects`, so the structure touches
// the subject ONLY through the wi_step_state tuple under test. The returned
// commitPivot must be called only after the subject's snapshot exists —
// P must commit after it, or P is not concurrent with the subject and the read
// simply sees the new tuple.
func ssiDoomStepStateRead(t *testing.T, pool *pgxpool.Pool, auxProject, wiID string) (commitPivot func()) {
	t.Helper()
	ctx := context.Background()

	pConn, err := pool.Acquire(ctx)
	require.NoError(t, err)
	pTx, err := pConn.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	require.NoError(t, err)
	pivotDone := false
	t.Cleanup(func() {
		if !pivotDone {
			_ = pTx.Rollback(ctx)
		}
		pConn.Release()
	})

	// P's read: the SIRead lock T's write will collide with.
	var desc *string
	require.NoError(t, pTx.QueryRow(ctx,
		`SELECT description FROM projects WHERE name = $1`, auxProject).Scan(&desc))

	// T: overwrite what P read and commit, giving P its conflict out to an
	// already-committed transaction. T must be SERIALIZABLE too — SSI only
	// tracks edges between serializable transactions.
	tConn, err := pool.Acquire(ctx)
	require.NoError(t, err)
	defer tConn.Release()
	tTx, err := tConn.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	require.NoError(t, err)
	tag, err := tTx.Exec(ctx,
		`UPDATE projects SET description = 'overwritten to give the pivot its out-conflict' WHERE name = $1`,
		auxProject)
	require.NoError(t, err)
	require.EqualValues(t, 1, tag.RowsAffected(),
		"the aux project row %q must exist, or P gets no conflict out and nothing is doomed", auxProject)
	require.NoError(t, tTx.Commit(ctx))

	return func() {
		tag, err := pTx.Exec(ctx,
			`UPDATE wi_step_state SET version = version + 1 WHERE work_item_id = $1`, wiID)
		require.NoError(t, err)
		require.EqualValues(t, 1, tag.RowsAffected(),
			"the wi_step_state row for %s must exist, or the subject's read visits no doomed tuple", wiID)
		require.NoError(t, pTx.Commit(ctx))
		pivotDone = true
	}
}
