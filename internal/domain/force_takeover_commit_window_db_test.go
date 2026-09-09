package domain

// aihub#451 — pf_force_takeover's description ends "No flag displaces another
// work item's lock, except in a narrow commit-window race (aihub#410)". This
// file is the test that qualifier had never had.
//
// ─── Why the qualifier was in doubt ────────────────────────────────────────
//
// aihub#430 measured the commit-window interleaving on the CLAIM path and, on
// the way, ran the same interleaving with the isolation level lowered to READ
// COMMITTED — FnForceTakeover's level. It came back CONFLICT_LOCK_TAKEN rather
// than displacing, and that probe was recorded in
// claim_takeover_commit_window_db_test.go without being asserted anywhere. Read
// quickly it says "even READ COMMITTED refuses this", i.e. that the qualifier on
// pf_force_takeover describes a gap nothing can reach.
//
// It does not say that. It says the shape #430 built does not reach it. That
// shape commits only the LOCK ROW inside the window; work item A's run_attempts
// row was committed long before, so when the DO UPDATE's WHERE re-runs against
// the row version it unblocked onto, its `NOT EXISTS (SELECT 1 FROM run_attempts
// …)` subquery finds A running and foreign, and the update is skipped. The gap
// lockUpsertSQL documents is a different one: the `prior` CTE's blindness and a
// STALE run_attempts read. Reaching it needs A's ATTEMPT ROW to be invisible
// too. This file builds that.
//
// ─── The interleaving, and why it is the production shape ──────────────────
//
//	A: BEGIN; INSERT run_attempts (running, wi A); INSERT resource_locks (key K)
//	   <- both uncommitted. This is a claim in flight: FnClaimWorkItem creates
//	      the attempt row and takes its locks in ONE transaction, so between its
//	      first insert and its commit this is exactly what the database holds.
//	B: FnForceTakeover(wi B) opens pool.Begin (READ COMMITTED), probes for
//	   foreign holders and sees none (A is uncommitted), supersedes B's prior
//	   attempt, creates B's new one, reaches the upsert and BLOCKS on A's
//	   uncommitted duplicate key
//	A: COMMIT  <- inside B's window
//	B: unblocks
//
// What Postgres then does is the whole subject, and it is documented behaviour
// rather than an accident: under READ COMMITTED, ON CONFLICT DO UPDATE re-reads
// the LATEST version of the conflicting row, but every OTHER relation in the
// statement — here run_attempts, inside the aihub#393 predicate — is still read
// through the statement's original snapshot. The manual states the consequence
// directly: "it is possible for an updating command to see an inconsistent
// snapshot: it can see the effects of concurrent updating commands on the same
// rows it is trying to update, but it does not see effects of those commands on
// other rows in the database." So the predicate asks "is resource_locks'
// owner_attempt_id a live attempt of another work item?" against a run_attempts
// snapshot in which that attempt does not exist yet, answers no, and updates.
//
// ─── Measured 2026-09-08, pgvector/pgvector:pg16 with these migrations ──────
//
//	arm                                  answer
//	attempt row committed inside window  the takeover SUCCEEDS and rewrites the
//	                                     row; A stays `running` and is told
//	                                     nothing -> the qualifier is TRUE
//	only the lock row inside the window  409 CONFLICT_LOCK_TAKEN, row unchanged
//	(aihub#430's shape, on this path)     -> the gap really is narrow
//
// So the qualifier stays on pf_force_takeover, and this file is what it cites.
// The arms are each other's controls: without the second, "displaced" is also
// what a harness with the predicate accidentally disabled would report; without
// the first, "refused" is also what a harness that never entered the window
// would report; and the third is what stops the first arm's assertion that NO
// lock_released was emitted from passing in a harness where none ever is. The
// synchronisation is not a sleep — waitForLockWaiter polls pg_stat_activity
// until a backend is parked inside the upsert and FAILS if none ever is, so a
// run in which the two never overlapped is red, not green.
//
// ─── What this instrument was checked against, 2026-09-08 ──────────────────
//
// A green result is worth only as much as the instrument's ability to go red, so
// each of these was run before the file was committed. Their red sets are
// distinguishable, which is what says the two arms measure different things:
//
//	mutation                                  result
//	FnForceTakeover raised to SERIALIZABLE    BOTH arms red. The window arm gets
//	                                          no displacement at all
//	aihub#393 predicate disabled              CONTROL arm red ("the takeover
//	  (status IN ('no_such_status'))          SUCCEEDED with A's attempt visible")
//	                                          while the window arm stays green
//	sync point pointed at a statement that
//	  never runs                              RED: "no backend ever blocked ...
//	                                          proved nothing"
//	no interleaving (A commits before the     WINDOW arm red with 409
//	  takeover starts)                        CONFLICT_LOCK_TAKEN — a DIFFERENT
//	                                          answer, which is how we know the
//	                                          green run really entered the window
//
// ⚠️ The first mutant is worth more than its red. Raising this transaction to
// SERIALIZABLE is the obvious "fix", and what it actually produced was
//
//	500 INTERNAL_ERROR  failed to update work_item after force_takeover:
//	                    current transaction is aborted (SQLSTATE 25P02)
//
// Instrumented on that same mutant, the 40001 arrives at acquireLockUpsert — and
// FnForceTakeover discarded every upsert error that was not the typed lock
// refusal, on purpose, so a recovery operation was not failed over lock
// bookkeeping. The transaction was therefore already aborted when the next
// statement ran, and 25P02 is not class 40, so nothing classified it: aihub#410's
// shape on the path aihub#410 did not cover. That was measured evidence for what
// resource_events.go's comment only argued.
//
// ⚠️ The DISCARD half is fixed as of aihub#497, so the 500 above is the record of
// what the swallow did and NOT a description of today's build. Re-measured on
// that same SERIALIZABLE mutant, 2026-09-09, after the fix:
//
//	409 CONFLICT_SERIALIZATION_FAILURE  failed to acquire lock
//	  file_scope:<proj>:repo-a:contested_by_451.go during force_takeover:
//	  ERROR: could not serialize access due to concurrent update (SQLSTATE
//	  40001); the transaction was rolled back after losing a concurrency race
//	  — retry the request
//
// Both arms of this file still go red under that mutant, exactly as the table
// above records — but for a different and better reason: a classified retryable
// 409 naming the statement that actually lost the race, instead of an
// unclassifiable 500 about the statement that merely ran next. The third control
// stays green. What is STILL missing is the retry wrapper, which is why the
// isolation level here is deliberately still pool.Begin: the caller would now be
// correctly told to retry, and nothing on this path retries for them. The
// force-takeover arms of TestSerializationFailureSurfacesAsRetryable409 pin the
// 409 that replaced the 500.
//
// ⚠️ This test asserts that a defect is REACHABLE, which is an unusual thing to
// pin, so be clear about what makes it go red and what to do then. Raising
// FnForceTakeover's transaction to SERIALIZABLE, or re-reading the holder inside
// the same statement, closes this gap — and the correct response to the red is
// to DELETE the qualifier from pf_force_takeover's description, the
// resource_events.go comment and docs/mcp-cards/pf_force_takeover.md in the same
// change, not to restore the race. The red is the reminder that those sentences
// exist.
//
//	AIHUB_TEST_DB=postgres://postgres:testpass@localhost:5432/aihub_test?sslmode=disable \
//	go test ./internal/domain/ -run 'TestForceTakeoverCommitWindow' -count=1 -v

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ftcwForeignClaimInFlight opens one transaction, inserts a RUNNING run_attempts
// row for wiID and a file_scope row on `key` owned by it, and returns the
// attempt id plus a commit function. NEITHER row is committed until the caller
// says so, which is what puts both of them behind the takeover's snapshot.
//
// Raw SQL rather than a second FnClaimWorkItem, for aihub#430's reason and one
// more: a bug in the function under test must not be able to break the fixture
// meant to expose it, and a real claim commits at a moment this test does not
// control. The rows it writes are the ones FnClaimWorkItem writes — that is the
// point of the shape, not a shortcut around it.
func ftcwForeignClaimInFlight(t *testing.T, pool *pgxpool.Pool, wiID, uid, key string) (attemptID string, commit func()) {
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

	attemptID = NewID("ra")
	_, err = tx.Exec(ctx, `
		INSERT INTO run_attempts (
			id, work_item_id, status, claim_epoch, idempotency_key,
			actor_user_id, actor_display, machine_id, session_secret_hash
		) VALUES ($1, $2, 'running', 1, $3, $4, 'A (claim in flight)', 'm-451-a', 'unused')`,
		attemptID, wiID, "idem-451-"+attemptID, uid)
	require.NoError(t, err, "the fixture's own attempt insert must succeed, or the race has no contender")

	_, err = tx.Exec(ctx, `
		INSERT INTO resource_locks (resource_type, resource_key, owner_attempt_id, claim_epoch)
		VALUES ('file_scope', $1, $2, 1)`, key, attemptID)
	require.NoError(t, err, "the fixture's own lock insert must succeed, or the race has no contender")

	return attemptID, func() {
		require.NoError(t, tx.Commit(ctx))
		committed = true
	}
}

// ftcwLockReleasedFor counts lock_released events naming attemptID as the owner
// that lost the row. It is how the `prior` CTE's blindness is observed from
// outside: a displacement the CTE could see emits one (cause=owner_replaced),
// and a displacement it could not see emits none at all.
func ftcwLockReleasedFor(t *testing.T, pool *pgxpool.Pool, attemptID string) int {
	t.Helper()
	var n int
	require.NoError(t, pool.QueryRow(context.Background(), `
		SELECT count(*) FROM agent_events
		WHERE event_type = 'lock_released'
		  AND payload->>'attempt_id' = $1`, attemptID).Scan(&n))
	return n
}

// ftcwSeedTakenOverWI builds work item B: running, holding no file_scope lock,
// and only THEN declaring the contended path — the aihub#393 fixture's shape,
// and the one production reaches when pf-plan rewrites declared_resources
// mid-attempt and the agent then dies. It returns B and its running attempt.
func ftcwSeedTakenOverWI(t *testing.T, pool *pgxpool.Pool, proj, uid, goal, repo, path, idem string) (wiB *WorkItem, bAttempt string) {
	t.Helper()
	wiB = seedWIWithResources(t, pool, proj, uid, goal, json.RawMessage(`[]`))
	bAttempt = cwRunningAttemptWithoutLocks(t, pool, uid, wiB.ID, idem)
	ftsDeclare(t, pool, wiB.ID, declaredWithRepo(repo, path))
	return wiB, bAttempt
}

// ftcwTakeover runs FnForceTakeover as a maintainer of proj.
func ftcwTakeover(pool *pgxpool.Pool, wiID, uid, proj, reason string) (*ForceTakeoverResponse, *AihubError) {
	return FnForceTakeover(context.Background(), pool, wiID, uid, "taker", "admin",
		map[string]string{proj: "maintainer"},
		&ForceTakeoverRequest{Reason: reason,
			SessionInfo: SessionInfo{MachineID: "m-451-ft", SessionSecret: testSecret}})
}

// ftcwResult carries FnForceTakeover's two returns back from the goroutine that
// runs it, because the arm needs the new attempt id to say WHO the row moved to.
type ftcwResult struct {
	resp *ForceTakeoverResponse
	aerr *AihubError
}

// TestForceTakeoverCommitWindowDisplacesALiveForeignLock is aihub#451's
// measurement: pf_force_takeover's commit-window qualifier is true, and this is
// the interleaving that makes it true.
func TestForceTakeoverCommitWindowDisplacesALiveForeignLock(t *testing.T) {
	pool := setupLatestTestDB(t)

	// The measurement. A's whole claim — attempt row AND lock row — commits
	// inside the takeover's window, so the aihub#393 predicate's run_attempts
	// subquery is evaluated against a snapshot that does not contain the owner it
	// is being asked about.
	t.Run("foreign_attempt_and_its_lock_committed_inside_the_window", func(t *testing.T) {
		uid := testUser(t, pool)
		proj := testProject(t, pool, uid)
		other := ftsSecondUser(t, pool, uid)

		wiB, bAttempt := ftcwSeedTakenOverWI(t, pool, proj, uid,
			"B: force-taken over inside A's commit window", "repo-a", "contested_by_451.go", "idem-451-b")

		// A is seeded but NEVER claimed: its attempt row is born inside the
		// window, which is the only thing that separates this arm from aihub#430's.
		wiA := seedWIWithResources(t, pool, proj, uid,
			"A: claims the contended path inside B's window", json.RawMessage(`[]`))
		key := proj + ":repo-a:contested_by_451.go"
		aAttempt, commitA := ftcwForeignClaimInFlight(t, pool, wiA.ID, uid, key)

		got := make(chan ftcwResult, 1)
		go func() {
			resp, aerr := ftcwTakeover(pool, wiB.ID, other, proj, "aihub#451 commit-window measurement")
			got <- ftcwResult{resp, aerr}
		}()

		// The window is REAL only if the takeover actually parks inside the
		// upsert. This fails loudly rather than sleeping and hoping.
		waitForLockWaiter(t, pool, "%INSERT INTO resource_locks%",
			"the force takeover's lock upsert (INSERT INTO resource_locks ... ON CONFLICT)")
		commitA()

		var out ftcwResult
		select {
		case out = <-got:
		case <-time.After(60 * time.Second):
			t.Fatal("the blocked force takeover never returned after the foreign claim was committed")
		}

		// ─── The measurement ─────────────────────────────────────────────────
		owner := ftsLockOwner(t, pool, key)
		if out.aerr != nil {
			t.Fatalf("the force takeover was REFUSED (%d %s: %s) with the foreign claim committed "+
				"inside its window, and file_scope:%s is still owned by %q. No constructible "+
				"interleaving then reaches the gap lockUpsertSQL documents, so delete "+
				"\"except in a narrow commit-window race (aihub#410)\" from pf_force_takeover's "+
				"description, from docs/mcp-cards/pf_force_takeover.md and from the "+
				"resource_events.go comment, and delete this file.",
				out.aerr.HTTPStatus, out.aerr.Code, out.aerr.Message, key, owner)
		}
		t.Logf("aihub#451 measurement: the takeover succeeded; file_scope:%s moved from %q "+
			"(work item A, still running) to %q", key, aAttempt, owner)

		require.NotNil(t, out.resp, "a successful takeover must return a response")
		assert.Equal(t, out.resp.NewAttemptID, owner,
			"file_scope:%s is owned by %q, not by the takeover's new attempt %q — the takeover "+
				"reported success without taking the row, which is a THIRD outcome and neither "+
				"the displacement this arm measures nor the refusal the qualifier denies",
			key, owner, out.resp.NewAttemptID)
		assert.NotEqual(t, aAttempt, owner)

		// The harm the qualifier admits to: A is not a crashed holder whose orphan
		// row a takeover is entitled to reclaim. It is running, it committed a
		// successful claim, and it now holds a lock row that belongs to somebody
		// else without having been told.
		assert.Equal(t, "running", ftsAttemptStatus(t, pool, aAttempt),
			"A's attempt is not running, so this arm displaced an ENDED owner — which is the "+
				"legitimate orphan reclaim lockUpsertSQL exists to perform, not aihub#410's race")

		// The other half of the same gap, and the reason it is silent: `prior`
		// reads the pre-statement snapshot, so it cannot see the row it is about
		// to overwrite either. A displaced owner normally gets a lock_released
		// with cause=owner_replaced; here nobody does.
		assert.Zero(t, ftcwLockReleasedFor(t, pool, aAttempt),
			"a lock_released was emitted for A's displaced attempt %q. That is better than the "+
				"documented behaviour, not worse — but it means the `prior` CTE now sees rows "+
				"committed after its snapshot, so lockUpsertSQL's comment about what `prior` "+
				"does NOT cover has stopped being true and must be corrected", aAttempt)

		// B's own bookkeeping still has to be right: a takeover that succeeds must
		// leave the work item owned by the attempt it just created.
		assert.Equal(t, "superseded", ftsAttemptStatus(t, pool, bAttempt),
			"the successful takeover left B's prior attempt %q live", bAttempt)
		assert.Equal(t, out.resp.NewAttemptID, currentAttemptID(t, pool, wiB.ID))
	})

	// The control, and the arm that makes "narrow" a measurement rather than an
	// adjective: aihub#430's shape, run against FnForceTakeover instead of
	// FnClaimWorkItem. Only the LOCK ROW commits inside the window; A's attempt
	// row was committed before it. The DO UPDATE's WHERE re-runs against the row
	// version it unblocked onto, finds that owner live and foreign in a snapshot
	// that DOES contain it, and refuses.
	//
	// Without this arm the first one is also satisfied by a build whose aihub#393
	// predicate does nothing at all — the defect aihub#393 fixed — and the
	// qualifier would be citing a test that cannot tell the two apart.
	t.Run("control_only_the_lock_row_committed_inside_the_window_is_refused", func(t *testing.T) {
		uid := testUser(t, pool)
		proj := testProject(t, pool, uid)
		other := ftsSecondUser(t, pool, uid)

		wiB, bAttempt := ftcwSeedTakenOverWI(t, pool, proj, uid,
			"B: force-taken over against a visible foreign owner", "repo-a", "orphan_control_451.go",
			"idem-451-ctrl-b")

		// A's ATTEMPT is committed up front; only its lock row waits.
		wiA := seedWIWithResources(t, pool, proj, uid,
			"A: already-running owner of the contended path", json.RawMessage(`[]`))
		aAttempt := cwRunningAttemptWithoutLocks(t, pool, uid, wiA.ID, "idem-451-ctrl-a")

		key := proj + ":repo-a:orphan_control_451.go"
		commitA := cwInsertLockInOpenTx(t, pool, key, aAttempt)

		got := make(chan ftcwResult, 1)
		go func() {
			resp, aerr := ftcwTakeover(pool, wiB.ID, other, proj, "aihub#451 commit-window control")
			got <- ftcwResult{resp, aerr}
		}()

		waitForLockWaiter(t, pool, "%INSERT INTO resource_locks%",
			"the force takeover's lock upsert (INSERT INTO resource_locks ... ON CONFLICT)")
		commitA()

		var out ftcwResult
		select {
		case out = <-got:
		case <-time.After(60 * time.Second):
			t.Fatal("the blocked force takeover never returned after the foreign lock was committed")
		}

		owner := ftsLockOwner(t, pool, key)
		require.NotNil(t, out.aerr,
			"the takeover SUCCEEDED with A's attempt visible in its snapshot; file_scope:%s is now "+
				"owned by %q. The commit-window race is then not narrow — it is every interleaving "+
				"in this window — and the aihub#393 predicate is not doing its job", key, owner)
		t.Logf("aihub#451 control: the blocked takeover returned %d %s (%s)",
			out.aerr.HTTPStatus, out.aerr.Code, out.aerr.Message)

		assert.Equal(t, ErrConflictLockTaken, out.aerr.Code,
			"a visible live foreign owner must produce the typed lock refusal; got %d %s: %s",
			out.aerr.HTTPStatus, out.aerr.Code, out.aerr.Message)
		assert.Equal(t, aAttempt, owner,
			"file_scope:%s changed hands to %q while the caller was told %s — a refusal must leave "+
				"the row alone", key, owner, out.aerr.Code)
		// FnForceTakeover supersedes and releases before it re-derives, so a
		// refusal that is not atomic leaves B with no running attempt at all.
		assert.Equal(t, "running", ftsAttemptStatus(t, pool, bAttempt),
			"B's attempt %q is not running after the refused takeover; the refusal must roll the "+
				"whole takeover back", bAttempt)
	})

	// The second control, and it exists for one assertion: the window arm claims
	// NO lock_released was emitted for the owner it displaced. An absence is only
	// evidence if the same harness can produce the presence, and a query that
	// matched nothing — wrong event type, wrong payload key, a pipeline that
	// never writes — would satisfy it for free.
	//
	// So here the displaced row is committed BEFORE the takeover starts, which is
	// the case `prior` CAN see: an un-swept orphan whose owner has ended, the
	// legitimate reclaim lockUpsertSQL exists to perform. Same helper, same
	// takeover, one event instead of none.
	t.Run("control_a_prior_owner_the_cte_can_see_is_reported_as_replaced", func(t *testing.T) {
		uid := testUser(t, pool)
		proj := testProject(t, pool, uid)
		other := ftsSecondUser(t, pool, uid)

		wiB, _ := ftcwSeedTakenOverWI(t, pool, proj, uid,
			"B: reclaims an orphan row", "repo-a", "orphan_trail_451.go", "idem-451-trail-b")

		wiA := seedWIWithResources(t, pool, proj, uid,
			"A: crashes holding the contended path", json.RawMessage(`[]`))
		aAttempt := cwRunningAttemptWithoutLocks(t, pool, uid, wiA.ID, "idem-451-trail-a")

		key := proj + ":repo-a:orphan_trail_451.go"
		commitA := cwInsertLockInOpenTx(t, pool, key, aAttempt)
		commitA()
		cwSetAttemptStatus(t, pool, aAttempt, "wrapped")
		require.Equal(t, aAttempt, ftsLockOwner(t, pool, key),
			"the fixture must start with A owning the row")
		require.Zero(t, ftcwLockReleasedFor(t, pool, aAttempt),
			"the fixture must start with no release recorded for %q, or the count below is "+
				"reading somebody else's event", aAttempt)

		resp, aerr := ftcwTakeover(pool, wiB.ID, other, proj, "aihub#451 event-trail control")
		require.Nil(t, aerr, "a takeover must be able to reclaim a row whose owner has ended: %v", aerr)
		assert.Equal(t, resp.NewAttemptID, ftsLockOwner(t, pool, key),
			"the orphan row was not reclaimed, so this arm displaced nothing and cannot vouch "+
				"for the event count either")
		assert.Equal(t, 1, ftcwLockReleasedFor(t, pool, aAttempt),
			"a displacement `prior` CAN see emitted no lock_released for %q. ftcwLockReleasedFor "+
				"therefore matches nothing in any run, and the window arm's assertion that the "+
				"SILENT displacement emitted none is vacuous", aAttempt)
	})
}
