package domain

// aihub#430 — is the claim path's force_takeover really exposed to aihub#410's
// commit-window race, or does the qualifier on pf_claim_work_item.force_takeover
// deny a guarantee the code actually holds?
//
// ─── The question, and why only a database can answer it ───────────────────
//
// pf_claim_work_item's `force_takeover` description ends "No flag displaces
// another work item's lock, except in a narrow commit-window race (aihub#410)".
// That exception is documented at lockUpsertSQL (resource_events.go): under READ
// COMMITTED, `INSERT ... ON CONFLICT DO UPDATE` overwrites a row committed after
// the statement's snapshot, which the `prior` CTE cannot see and whose owner the
// WHERE was evaluated against a stale view of. The three callers of that
// statement do NOT share an isolation level:
//
//	FnClaimWorkItem   BeginTx(IsoLevel: pgx.Serializable)
//	FnAcquireLocks    BeginTx(IsoLevel: pgx.Serializable)
//	FnForceTakeover   pool.Begin(ctx)   <- READ COMMITTED
//
// So the qualifier was inherited by the claim tool from a note written about the
// takeover tool. Whether it is true THERE is a property of Postgres's
// concurrency control, not of this repo's source, and it is decided at the
// moment a blocked writer is released by the committing one. A unit test that
// hand-builds the interleaving proves nothing about it; two real backends racing
// over one real row are the only instrument.
//
// ─── The interleaving ──────────────────────────────────────────────────────
//
//	B: FnClaimWorkItem(force_takeover) opens SERIALIZABLE, takes its snapshot,
//	   passes probeForeignLockHolders (which cannot see A's uncommitted row),
//	   reaches the upsert and BLOCKS on A's uncommitted duplicate key
//	A: COMMIT  <- inside B's window, which is the whole point
//	B: unblocks, and does one of three things
//
// The three outcomes are not "pass/fail", they are the measurement:
//
//	displacement          B's upsert rewrites A's row and B is told nothing
//	                      -> the qualifier is TRUE and stays
//	retryable 409         B gets SQLSTATE 40001 (or the typed lock refusal)
//	                      -> the qualifier is FALSE on this path and goes
//	500                   the race is not a displacement but the mapping is
//	                      broken -> a separate defect, reported not fixed
//
// The synchronisation point is not a sleep: waitForLockWaiter (borrowed from
// serialization_failure_db_test.go) polls pg_stat_activity until a backend is
// parked on a lock inside the upsert statement, and FAILS if none ever is. A
// sleep too short releases the holder before B has taken its snapshot, and B
// then simply succeeds — a green run that measured nothing.
//
// ─── What this instrument was checked against, 2026-09-07 ──────────────────
//
// A green result here is worth only as much as the instrument's ability to go
// red, so each of these was run on a throwaway copy of the tree before the
// qualifier was deleted:
//
//	tree                                          arm 1 answers
//	as shipped (SERIALIZABLE, aihub#393 predicate) 409 CONFLICT_SERIALIZATION_FAILURE, row unchanged
//	no interleaving (A commits before B starts)    409 CONFLICT_LOCK_TAKEN  <- a DIFFERENT code, which is
//	                                               how we know the window was really entered
//	sync point pointed at a statement that never
//	runs                                           RED: "no backend ever blocked ... proved nothing"
//	READ COMMITTED **and** the predicate removed   RED: "the takeover SUCCEEDED ... That is the
//	                                               aihub#410 displacement" — the instrument SEES a
//	                                               displacement when there is one
//
// ⚠️ One probe is recorded here because it is interesting and is NOT asserted
// anywhere: with only the isolation level lowered to READ COMMITTED — the
// aihub#393 predicate left in place — this same interleaving answers 409
// CONFLICT_LOCK_TAKEN rather than displacing, because the DO UPDATE's WHERE
// re-runs against the row version it unblocked onto. That is an observation
// about ONE interleaving, not a verdict on pf_force_takeover: the gap
// resource_events.go documents is the `prior` CTE's blindness and a stale
// run_attempts read, which this shape does not reach. The qualifier stays on
// that tool, and settling it needs its own work item.
//
// That work item was aihub#451, and it settled it the other way: commit the
// foreign ATTEMPT row inside the window as well as its lock row — a claim in
// flight, which writes both in one transaction — and FnForceTakeover really does
// displace a live foreign holder, silently. See
// force_takeover_commit_window_db_test.go. Read the probe above as "this shape
// does not reach the gap", never as "nothing does".
//
//	AIHUB_TEST_DB=postgres://postgres:testpass@localhost:5432/aihub_test?sslmode=disable \
//	go test ./internal/domain/ -run 'TestClaimTakeoverCommitWindow' -count=1 -v

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// cwRunningAttemptWithoutLocks claims wiID for uid and returns the attempt id,
// having asserted the claim took no file_scope lock.
//
// The assertion is the fixture's own control: every arm below needs the LOCK ROW
// to arrive at a moment this test chooses, so a claim that quietly took the key
// on its way in would leave the race testing nothing.
func cwRunningAttemptWithoutLocks(t *testing.T, pool *pgxpool.Pool, uid, wiID, idem string) string {
	t.Helper()
	resp, aerr := ftsClaim(t, pool, uid, wiID, idem, false)
	require.Nil(t, aerr, "fixture claim of %s must succeed", wiID)
	require.Empty(t, fileScopeKeys(resp.AcquiredLocks),
		"the fixture needs this attempt holding no file_scope lock; it took %v",
		fileScopeKeys(resp.AcquiredLocks))
	return resp.AttemptID
}

// cwInsertLockInOpenTx opens a transaction, inserts one file_scope row owned by
// ownerAttempt, and returns a commit function. The row is NOT committed until
// the caller says so, which is what makes it invisible to a snapshot taken in
// between.
//
// Raw SQL rather than a second FnClaimWorkItem: a bug in the function under test
// must not be able to break the fixture that is supposed to expose it, and a
// real claim would commit the row at a moment this test does not control.
func cwInsertLockInOpenTx(t *testing.T, pool *pgxpool.Pool, key, ownerAttempt string) (commit func()) {
	t.Helper()
	ctx := context.Background()
	conn, err := pool.Acquire(ctx)
	require.NoError(t, err)
	tx, err := conn.Begin(ctx)
	require.NoError(t, err)

	committed := false
	t.Cleanup(func() {
		if !committed {
			_ = tx.Rollback(ctx)
		}
		conn.Release()
	})

	_, err = tx.Exec(ctx, `
		INSERT INTO resource_locks (resource_type, resource_key, owner_attempt_id, claim_epoch)
		VALUES ('file_scope', $1, $2, 1)`, key, ownerAttempt)
	require.NoError(t, err, "the fixture's own insert must succeed, or the race has no contender")

	return func() {
		require.NoError(t, tx.Commit(ctx))
		committed = true
	}
}

// cwSetAttemptStatus forces one attempt's status. Used to build the ORPHAN
// holder the control arm needs — a row whose owner has ended, which lockUpsertSQL
// is supposed to displace.
func cwSetAttemptStatus(t *testing.T, pool *pgxpool.Pool, attemptID, status string) {
	t.Helper()
	_, err := pool.Exec(context.Background(),
		`UPDATE run_attempts SET status=$1, ended_at=clock_timestamp() WHERE id=$2`, status, attemptID)
	require.NoError(t, err)
}

// TestClaimTakeoverCommitWindowCannotDisplaceAForeignLock is aihub#430's
// measurement.
//
// Two arms, and the second one is what makes the first mean anything: if this
// harness could not observe a lock changing hands at all, "the owner did not
// change" would be a property of the test rather than of the code.
func TestClaimTakeoverCommitWindowCannotDisplaceAForeignLock(t *testing.T) {
	pool := setupLatestTestDB(t)

	t.Run("foreign lock committed inside the takeover window", func(t *testing.T) {
		uid := testUser(t, pool)
		proj := testProject(t, pool, uid)
		other := ftsSecondUser(t, pool, uid)

		// B: running, declaring the contended path, holding nothing. Declared
		// AFTER the claim for exactly that reason (ftsDeclare's own note).
		declared := declaredWithRepo("repo-a", "contested_by_430.go")
		wiB := seedWIWithResources(t, pool, proj, uid, "B: takes over inside the window", json.RawMessage(`[]`))
		bAttempt := cwRunningAttemptWithoutLocks(t, pool, uid, wiB.ID, "idem-430-b")
		ftsDeclare(t, pool, wiB.ID, declared)

		// A: a LIVE attempt of a different work item, which will own the row.
		wiA := seedWIWithResources(t, pool, proj, uid, "A: holds the contended path", json.RawMessage(`[]`))
		aAttempt := cwRunningAttemptWithoutLocks(t, pool, uid, wiA.ID, "idem-430-a")

		key := proj + ":repo-a:contested_by_430.go"
		commitA := cwInsertLockInOpenTx(t, pool, key, aAttempt)

		got := make(chan *AihubError, 1)
		go func() {
			_, aerr := ftsClaim(t, pool, other, wiB.ID, "idem-430-window", true)
			got <- aerr
		}()

		// The window is REAL only if B actually parks inside the upsert. This
		// fails loudly rather than sleeping and hoping.
		waitForLockWaiter(t, pool, "%INSERT INTO resource_locks%",
			"the claim's lock upsert (INSERT INTO resource_locks ... ON CONFLICT)")
		commitA()

		var aerr *AihubError
		select {
		case aerr = <-got:
		case <-time.After(60 * time.Second):
			t.Fatal("the blocked takeover never returned after the foreign lock was committed")
		}

		// ─── The measurement ─────────────────────────────────────────────────
		owner := ftsLockOwner(t, pool, key)
		if aerr == nil {
			t.Fatalf("the takeover SUCCEEDED with the foreign row committed inside its window; "+
				"file_scope:%s is now owned by %q (A's live attempt was %q). That is the aihub#410 "+
				"displacement, on the claim path — keep the qualifier on "+
				"pf_claim_work_item.force_takeover and cite this test.", key, owner, aAttempt)
		}
		t.Logf("aihub#430 measurement: the blocked takeover returned %d %s (%s)",
			aerr.HTTPStatus, aerr.Code, aerr.Message)

		assert.Equal(t, aAttempt, owner,
			"file_scope:%s changed hands to %q while the caller was told %s — a refusal must leave "+
				"the row alone, and a displacement must not be reported as an error",
			key, owner, aerr.Code)
		assert.Equal(t, 409, aerr.HTTPStatus,
			"the loser of this race must be told to retry, not that the server is broken; got %d %s: %s",
			aerr.HTTPStatus, aerr.Code, aerr.Message)
		assert.NotEqual(t, ErrInternalError, aerr.Code,
			"SQLSTATE 40001 reaching the caller as a 500 would be a mapping defect (aihub#334's "+
				"shape) on a path aihub#334 did not cover: %s", aerr.Message)
		assert.Contains(t, []string{string(ErrConflictSerializationFailure), string(ErrConflictLockTaken)},
			string(aerr.Code),
			"the only two answers that mean 'nothing was displaced' are the serialization retry and "+
				"the typed lock refusal; %s is neither", aerr.Code)

		// The whole transaction rolled back, so the takeover must not have left
		// B's prior attempt superseded on its way out — otherwise a caller that
		// retries as instructed finds the work item ownerless.
		assert.Equal(t, "running", ftsAttemptStatus(t, pool, bAttempt),
			"the refused takeover superseded B's prior attempt anyway; the retry the 409 asks for "+
				"would then be racing a work item nobody owns")
	})

	// The control. Without it, "the owner did not change" above is satisfied just
	// as well by a harness that cannot see the column move at all — a wrong key,
	// a claim that never reached the upsert, a fixture that locked nothing.
	//
	// It is also the positive statement of what lockUpsertSQL's WHERE is FOR: an
	// un-swept orphan row, owner already ended, is the displacement a takeover
	// exists to perform, and refusing it would make a takeover unable to recover
	// from a crashed holder.
	t.Run("orphan lock committed before the claim is displaced", func(t *testing.T) {
		uid := testUser(t, pool)
		proj := testProject(t, pool, uid)
		other := ftsSecondUser(t, pool, uid)

		declared := declaredWithRepo("repo-a", "orphaned_by_430.go")
		wiB := seedWIWithResources(t, pool, proj, uid, "B: takes over an orphan row", json.RawMessage(`[]`))
		cwRunningAttemptWithoutLocks(t, pool, uid, wiB.ID, "idem-430-orphan-b")
		ftsDeclare(t, pool, wiB.ID, declared)

		wiA := seedWIWithResources(t, pool, proj, uid, "A: crashes holding the path", json.RawMessage(`[]`))
		aAttempt := cwRunningAttemptWithoutLocks(t, pool, uid, wiA.ID, "idem-430-orphan-a")

		key := proj + ":repo-a:orphaned_by_430.go"
		commitA := cwInsertLockInOpenTx(t, pool, key, aAttempt)
		commitA()
		// A's attempt ends without its lock being swept — the gc sweep is up to a
		// minute away, and this is the row that state leaves behind.
		cwSetAttemptStatus(t, pool, aAttempt, "wrapped")
		require.Equal(t, aAttempt, ftsLockOwner(t, pool, key),
			"the fixture must start with A owning the row, or the arm proves nothing")

		resp, aerr := ftsClaim(t, pool, other, wiB.ID, "idem-430-orphan", true)
		require.Nil(t, aerr, "a takeover must be able to reclaim a row whose owner has ended: %v", aerr)

		owner := ftsLockOwner(t, pool, key)
		assert.Equal(t, resp.AttemptID, owner,
			"file_scope:%s is still owned by the ended attempt %q — this harness cannot observe a "+
				"lock changing hands, so the other arm's 'unchanged' proves nothing", key, aAttempt)
		assert.NotEqual(t, aAttempt, owner)
	})
}
