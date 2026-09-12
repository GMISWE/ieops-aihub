package domain

// DB-gated integration tests for aihub#370: the commit-time lock gate's
// DB-touching half (FnReconcileCommitLocks, aihub#366).
//
// # Why this file exists
//
// aihub#366 shipped the gate with resident tests for its PURE-LOGIC parts only
// (commit_locks_test.go: key-form coverage; commit_gate_wire_test.go: the MCP
// wiring; commit_gate_test.go: the git change surface). The half that touches
// the database — acquire, conflict, orphan-reclaim — was measured live on
// 2026-09-06 against a real migrated database, in a THROWAWAY test file that
// was deleted before commit because extending that attempt's locks to
// .github/workflows/ci.yml hit a 409 held by aihub#365. The measurements
// survive only as timeline notes on aihub#366 ("REQUIREMENT 2 measured live"
// and "MUTATION PROBES"). This file turns those notes back into a resident
// gate.
//
// # The authority for what is asserted
//
// The assertions below align with aihub#366's measured record, NOT with a
// fresh reading of the code — the record is what the owner-accepted behaviour
// was measured to be. The mapping, arm by arm (letters are the record's):
//
//	(a)  auto-acquire does not interrupt: nil error, Covered=[declared],
//	     AcquiredPaths=[undeclared], and the resource_locks TABLE — not the
//	     response — shows the new key.        → subtest "an undeclared path…"
//	(a2) the acquisition carries cause=commit_gate on the timeline.  → same
//	(a3) the acquired path is NOT written into declared_resources.   → same
//	(c)  a repeat call over the same paths returns Probed=0,
//	     AcquiredPaths=[], Covered=both, with lock-row and lock_acquired
//	     event counts unchanged — and the never-declared path reads as
//	     COVERED, which is the direct proof the gate reads the lock table
//	     and not declared_resources.          → subtest "a repeat call…"
//	(b)  a path held by another live attempt returns 409
//	     CONFLICT_LOCK_TAKEN naming attempt_id / actor_display /
//	     work_item_slug, and the free path in the SAME call is not
//	     acquired.                            → subtest "a path held by…"
//
// The orphan-reclaim arm has NO letter: aihub#366's measured record does not
// cover it (its five arms are a/a2/a3/c/b), even though the wi that filed this
// work names it as one of the three paths. For that arm the authority is the
// code's own documented contract (commit_locks.go, the phase-2 reclaim branch:
// "Reclaimed on the same terms as FnAcquireLocks: the release is filed under
// the DEAD row's own work item, not this one"), asserted against the table and
// the audit trail rather than against the response alone.
//
// # Requirement 3, preserved by mutation
//
// aihub#366's most important mutant is M5: derive the held set from
// declared_resources instead of resource_locks → only arm (c) goes red. That
// discriminator is what this suite must keep alive, and the repeat-call
// subtest is where it lands: under M5 the never-declared path falls out of the
// derived held set, the gate re-probes it, and Probed stops being 0. Row and
// event counts CANNOT catch that mutant — a redundant probe writes nothing —
// which is exactly why the response carries Probed at all.
//
// MUTANT (conflict arm): internal/domain/commit_locks.go, phase 1 — collapse
// `case scanErr == nil && ownerAttemptID == req.AttemptID:` to
// `case scanErr == nil:`, so a foreign live holder reads as covered. Only
// "a path held by another live attempt refuses the whole call" goes red.
//
// MUTANT (repeat-call arm, = aihub#366's M5): replace heldFileScopeKeysSQL
// with a query deriving keys from declared_resources' path entries. Only
// "a repeat call probes nothing and covers the never-declared path" goes red,
// on the Probed=0 assertion.
//
// ⚠️ Do NOT verify lock state with the response of the function under test
// where the record says TABLE: every arm pairs its response claim with a read
// of resource_locks / agent_events, the oracle the gate did not supply.
//
// Run:
//
//	AIHUB_TEST_DB=postgres://postgres:testpass@localhost:5432/aihub_test?sslmode=disable \
//	go test ./internal/domain/ -run 'TestCommitLockGateDB_AcquireConflictOrphanReclaim' -race -v -count=1

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// lockOwnerAndCount reads who owns a file_scope key straight from the table.
// n distinguishes "nobody" (0) from "somebody" independently of owner.
func lockOwnerAndCount(t *testing.T, pool *pgxpool.Pool, key string) (owner string, n int) {
	t.Helper()
	require.NoError(t, pool.QueryRow(context.Background(),
		`SELECT count(*), COALESCE(min(owner_attempt_id), '')
		 FROM resource_locks WHERE resource_type='file_scope' AND resource_key=$1`, key).Scan(&n, &owner))
	return owner, n
}

// countLockEventsForAttempt counts lock events scoped by the ATTEMPT that owns
// the lock row, not by key alone: the test project name is derived from
// t.Name() and therefore identical across runs against one database, so a
// key-scoped count would see the previous run's events. Attempt IDs are minted
// fresh every run.
func countLockEventsForAttempt(t *testing.T, pool *pgxpool.Pool, eventType, attemptID, resourceKey string) int {
	t.Helper()
	var n int
	require.NoError(t, pool.QueryRow(context.Background(),
		`SELECT count(*) FROM agent_events
		 WHERE event_type=$1 AND run_attempt_id=$2 AND payload->>'resource_key'=$3`,
		eventType, attemptID, resourceKey).Scan(&n))
	return n
}

// TestCommitLockGateDB_AcquireConflictOrphanReclaim is aihub#370's acceptance
// criterion: the three DB-touching paths of FnReconcileCommitLocks, resident.
//
// One function with subtests: dbtestcov counts DB-gated FUNCTIONS, and the
// per-path claims belong in the subtest names, which ci.yml asserts on.
func TestCommitLockGateDB_AcquireConflictOrphanReclaim(t *testing.T) {
	pool := setupLatestTestDB(t)
	ctx := context.Background()

	u := testUser(t, pool)
	project := testProject(t, pool, u)

	// Distinct basenames on purpose: "declared370.go" would be a SUBSTRING of
	// "undeclared370.go", and the declared_resources non-write assertion in the
	// first arm is a NotContains over the serialized declaration.
	const declared = "internal/domain/gate370_declared.go"
	const surprise = "internal/domain/gate370_surprise.go"
	const contested = "internal/domain/gate370_contested.go"
	const bystander = "internal/domain/gate370_bystander.go"
	const orphaned = "internal/domain/gate370_orphaned.go"

	// The ":aihub:" segment is the repo (aihub#261): each fixture declares
	// {"type":"repo","uri":"repo:aihub"} alongside its paths, so the derived
	// keys are repo-qualified — the same form the gate itself derives, because
	// FnReconcileCommitLocks is called with Repo and keys its acquisitions
	// "<project>:<repo>:<path>". The orphan arm NEEDS that agreement: the
	// reclaim branch is reachable only when the dead row's key EQUALS the key
	// the gate would insert.
	declaredKey := project + ":aihub:" + declared
	surpriseKey := project + ":aihub:" + surprise
	contestedKey := project + ":aihub:" + contested
	bystanderKeyQualified := project + ":aihub:" + bystander
	bystanderKeyLegacy := project + ":" + bystander
	orphanKey := project + ":aihub:" + orphaned

	wiCaller := seedClaimableWI(t, pool, project, u,
		"commit gate caller: declares one path, will change more",
		`[{"type":"repo","uri":"repo:aihub","intent":"write"},`+
			`{"type":"path","uri":"file:`+declared+`","intent":"write"}]`)
	claim := claimFresh(t, pool, wiCaller.ID, u, "aihub370-caller")
	require.ElementsMatch(t, []string{declaredKey}, heldLockKeys(t, pool, claim.AttemptID),
		"fixture check: the claim must hold exactly the declared path's repo-qualified lock, or the "+
			"covered/missing split below is measured against the wrong held set")

	// Through retryOnSerializationConflict for aihub#492's reason: the gate's
	// transaction is SERIALIZABLE, so any arm could lose an SSI race to another
	// DB-gated test binary sharing this database and report a failure the
	// database asked us to retry. The helper passes every OTHER error through,
	// so the conflict arm's 409 still arrives as a 409.
	gate := func(t *testing.T, paths ...string) (*ReconcileCommitLocksResponse, *AihubError) {
		t.Helper()
		return retryOnSerializationConflict(t, "commit-lock reconcile",
			func() (*ReconcileCommitLocksResponse, *AihubError) {
				return FnReconcileCommitLocks(ctx, pool, wiCaller.ID, &ReconcileCommitLocksRequest{
					AttemptID:     claim.AttemptID,
					ClaimEpoch:    claim.ClaimEpoch,
					SessionSecret: "locktest-secret-0123456789abcdef0123456789abcdef0123456789ab",
					Repo:          "aihub",
					Paths:         paths,
				})
			})
	}

	// aihub#366 arms (a), (a2), (a3).
	t.Run("an undeclared path is locked without interrupting the commit", func(t *testing.T) {
		resp, aerr := gate(t, declared, surprise)
		require.Nil(t, aerr,
			"(a) auto-acquire must not interrupt: aihub#366 measured nil error when the difference was "+
				"free to take; got %+v", aerr)
		assert.ElementsMatch(t, []string{declared}, resp.Covered,
			"the declared path was covered by the claim-time lock and only it")
		assert.Equal(t, []string{surprise}, resp.AcquiredPaths,
			"the undeclared path is the difference, and the gate takes it rather than warning or blocking")

		// (a) second half — the record is explicit that the oracle is the
		// resource_locks TABLE, not the gate's own response.
		owner, n := lockOwnerAndCount(t, pool, surpriseKey)
		require.Equal(t, 1, n, "the acquisition must be a real row under the repo-qualified key")
		assert.Equal(t, claim.AttemptID, owner, "and owned by the committing attempt")

		// (a2) the audit trail: one lock_acquired event with cause=commit_gate —
		// not acquire_locks, because "the change set outran the declaration" is
		// the count worth auditing separately (commit_locks.go).
		var withCause int
		require.NoError(t, pool.QueryRow(ctx,
			`SELECT count(*) FROM agent_events
			 WHERE event_type='lock_acquired' AND run_attempt_id=$1
			   AND payload->>'resource_key'=$2 AND payload->>'cause'='commit_gate'`,
			claim.AttemptID, surpriseKey).Scan(&withCause))
		assert.Equal(t, 1, withCause,
			"aihub#366 arm (a2): the acquisition carries cause=commit_gate on the timeline")

		// (a3) NOT written into declared_resources — deliberately, because
		// aihub#264 releases the locks of removed declarations and pf-plan
		// rewrites the declaration wholesale, so declaring the path here would
		// make the protection WEAKER (commit_locks.go's header carries the
		// argument). A "tidier" gate that appends to the declaration fails here.
		var decl string
		require.NoError(t, pool.QueryRow(ctx,
			`SELECT declared_resources::text FROM work_items WHERE id=$1`, wiCaller.ID).Scan(&decl))
		assert.NotContains(t, decl, "gate370_surprise",
			"aihub#366 arm (a3): the commit-gate acquisition must stay OUT of declared_resources")
		assert.Contains(t, decl, "gate370_declared",
			"control: the original declaration is still there, so the assertion above is about the "+
				"acquired path and not about a wiped declaration")
	})

	// aihub#366 arm (c) — and requirement 3's discriminator (mutant M5): the
	// never-declared path reads as covered because the gate reads the LOCK
	// TABLE this attempt holds, not declared_resources.
	t.Run("a repeat call probes nothing and covers the never-declared path", func(t *testing.T) {
		_, surpriseRows := lockOwnerAndCount(t, pool, surpriseKey)
		require.Equal(t, 1, surpriseRows,
			"fixture check: the first arm must have left the acquired lock in place, or 'covered' below "+
				"is trivially about a free key")
		acquiredEventsBefore := countLockEventsForAttempt(t, pool, EventLockAcquired, claim.AttemptID, surpriseKey) +
			countLockEventsForAttempt(t, pool, EventLockAcquired, claim.AttemptID, declaredKey)
		heldBefore := heldLockKeys(t, pool, claim.AttemptID)

		resp, aerr := gate(t, declared, surprise)
		require.Nil(t, aerr, "a fully covered repeat call must pass: %+v", aerr)

		assert.Equal(t, 0, resp.Probed,
			"aihub#366 arm (c): Probed=0 is the zero-overhead promise, and the ONLY instrument that "+
				"catches a gate deriving its held set from declared_resources (mutant M5) — a redundant "+
				"probe writes no row and no event, so the counts below cannot see it")
		assert.Empty(t, resp.AcquiredPaths, "a repeat call takes nothing")
		assert.ElementsMatch(t, []string{declared, surprise}, resp.Covered,
			"aihub#366 arm (c): the surprise path is in NO declaration yet reads as covered — the direct "+
				"proof the gate compares against resource_locks and not declared_resources")

		assert.ElementsMatch(t, heldBefore, heldLockKeys(t, pool, claim.AttemptID),
			"lock rows unchanged by a covered call")
		acquiredEventsAfter := countLockEventsForAttempt(t, pool, EventLockAcquired, claim.AttemptID, surpriseKey) +
			countLockEventsForAttempt(t, pool, EventLockAcquired, claim.AttemptID, declaredKey)
		assert.Equal(t, acquiredEventsBefore, acquiredEventsAfter,
			"lock_acquired event count unchanged by a covered call")
	})

	// aihub#366 arm (b).
	t.Run("a path held by another live attempt refuses the whole call", func(t *testing.T) {
		wiHolder := seedClaimableWI(t, pool, project, u,
			"commit gate holder: keeps a live lock on the contested path",
			`[{"type":"repo","uri":"repo:aihub","intent":"write"},`+
				`{"type":"path","uri":"file:`+contested+`","intent":"write"}]`)
		holderClaim := claimFresh(t, pool, wiHolder.ID, u, "aihub370-holder")
		require.Contains(t, heldLockKeys(t, pool, holderClaim.AttemptID), contestedKey,
			"fixture check: the holder's claim must really have taken the contested lock, or the refusal "+
				"below is about a free key")

		resp, aerr := gate(t, contested, bystander)
		require.NotNil(t, aerr,
			"aihub#366 arm (b): a path held by another live attempt must refuse the call, not warn")
		assert.Nil(t, resp, "a refusal returns no response")
		assert.Equal(t, ErrConflictLockTaken, aerr.Code)
		assert.Equal(t, 409, aerr.HTTPStatus, "CONFLICT_LOCK_TAKEN is the 409 the record names")

		details, ok := aerr.Details.(map[string]any)
		require.True(t, ok, "details is %T, want map[string]any", aerr.Details)
		conflicts, ok := details["conflicts"].([]commitLockConflict)
		require.True(t, ok, "details.conflicts is %T, want []commitLockConflict", details["conflicts"])
		require.Len(t, conflicts, 1)

		// The holder must be NAMED, and named correctly: the record lists all
		// three fields. actor_display is compared against the run_attempts row
		// rather than a literal, so the oracle is the table and not this test's
		// knowledge of what claimFresh happens to pass.
		var storedDisplay string
		require.NoError(t, pool.QueryRow(ctx,
			`SELECT actor_display FROM run_attempts WHERE id=$1`, holderClaim.AttemptID).Scan(&storedDisplay))
		require.NotEmpty(t, storedDisplay,
			"fixture check: the holder attempt must carry an actor_display, or the assertion below "+
				"passes on two empty strings")
		c := conflicts[0]
		assert.Equal(t, contested, c.Path)
		assert.Equal(t, holderClaim.AttemptID, c.AttemptID, "arm (b): the refusal names the holding attempt")
		assert.Equal(t, storedDisplay, c.ActorDisplay, "arm (b): the refusal names the holding actor")
		assert.Equal(t, wiHolder.Slug, c.WorkItemSlug, "arm (b): the refusal names the holding work item")

		// All-or-nothing: the FREE path in the same call was not acquired.
		// Verified against the table under both key forms the gate could have
		// written, exactly as the record puts it ("verified against the table").
		_, nQualified := lockOwnerAndCount(t, pool, bystanderKeyQualified)
		_, nLegacy := lockOwnerAndCount(t, pool, bystanderKeyLegacy)
		assert.Zero(t, nQualified+nLegacy,
			"aihub#366 arm (b): the free path in the SAME call must not be acquired when any path "+
				"conflicts — a commit is not half-protected")

		// And the contested row did not change hands.
		owner, n := lockOwnerAndCount(t, pool, contestedKey)
		assert.Equal(t, 1, n)
		assert.Equal(t, holderClaim.AttemptID, owner, "the refusal never steals")
	})

	// The third path, NOT in aihub#366's measured record (see the file header):
	// authority here is commit_locks.go's phase-2 reclaim contract, checked
	// against the table and the audit trail.
	t.Run("an orphan lock from an ended attempt is reclaimed", func(t *testing.T) {
		wiCrashed := seedClaimableWI(t, pool, project, u,
			"commit gate crashed holder: its attempt ends without releasing",
			`[{"type":"repo","uri":"repo:aihub","intent":"write"},`+
				`{"type":"path","uri":"file:`+orphaned+`","intent":"write"}]`)
		crashedClaim := claimFresh(t, pool, wiCrashed.ID, u, "aihub370-crashed")
		require.Contains(t, heldLockKeys(t, pool, crashedClaim.AttemptID), orphanKey,
			"fixture check: the claim must really have taken the lock that is about to be orphaned")

		// The raw UPDATE is load-bearing: every API path that ends an attempt
		// releases its file_scope locks in the same transaction, so the orphan
		// state — a lock row whose owning attempt is terminal — is reachable
		// only as the residue of a CRASH the gc sweep has not reached yet.
		// Driving it through FnCompleteAttempt would release the row and leave
		// this arm asserting about a key nobody holds.
		_, err := pool.Exec(ctx,
			`UPDATE run_attempts SET status='failed' WHERE id=$1`, crashedClaim.AttemptID)
		require.NoError(t, err)
		owner, n := lockOwnerAndCount(t, pool, orphanKey)
		require.Equal(t, 1, n, "fixture check: the crash simulation must leave the lock row behind")
		require.Equal(t, crashedClaim.AttemptID, owner,
			"fixture check: still owned by the dead attempt, or nothing below reclaims anything")

		resp, aerr := gate(t, orphaned)
		require.Nil(t, aerr,
			"an orphan is not a conflict: its owner is not live, so the gate reclaims rather than "+
				"refusing; got %+v", aerr)
		assert.Equal(t, []string{orphaned}, resp.AcquiredPaths,
			"the reclaimed path is reported as acquired — the caller's commit proceeds protected")

		// The table: exactly one row for the key, and it changed hands.
		owner, n = lockOwnerAndCount(t, pool, orphanKey)
		assert.Equal(t, 1, n, "reclaim replaces the row, it does not duplicate it")
		assert.Equal(t, claim.AttemptID, owner, "the committing attempt now holds the reclaimed key")

		// The audit trail, as commit_locks.go documents it: the release is
		// filed under the DEAD row's own work item — not the caller's — so the
		// person wondering where their lock went can find it, and it carries
		// who took it and through which door.
		var released int
		require.NoError(t, pool.QueryRow(ctx,
			`SELECT count(*) FROM agent_events
			 WHERE event_type='lock_released' AND work_item_id=$1 AND run_attempt_id=$2
			   AND payload->>'resource_key'=$3 AND payload->>'cause'='orphan_reclaim'
			   AND payload->>'reclaimed_by_attempt_id'=$4 AND payload->>'reclaimed_by_cause'='commit_gate'`,
			wiCrashed.ID, crashedClaim.AttemptID, orphanKey, claim.AttemptID).Scan(&released))
		assert.Equal(t, 1, released,
			"the reclaim's release event must sit on the dead attempt's own work item timeline, with "+
				"cause=orphan_reclaim and the reclaiming attempt + commit_gate door named in the payload")
	})
}
