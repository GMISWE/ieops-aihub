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
				Mode: "fresh",
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
