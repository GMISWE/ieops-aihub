package domain

// DB-gated integration tests for aihub#345: pf_acquire_locks must report the
// locks its attempt actually holds, not the subset it happened to recompute.
//
// # What was wrong, exactly
//
// FnAcquireLocks builds a TARGET set by re-deriving locks from the work item's
// CURRENT declared_resources, and `already_held` was filled only from inside
// that loop. So `already_held` never meant "the locks this attempt holds"; it
// meant "of the locks I would take right now, these I already have". Every lock
// outside that recomputed set was invisible while the server went on enforcing
// it.
//
// That distinction is the substance of the item, because the tool is NOT
// uniformly broken and a reporter who only ever sees the working case will
// conclude it is fine. Held locks fall into two populations:
//
//	REPORTED   the declaration that produced the lock is still present, still a
//	           path/document/section, still not intent=read, still maps to a
//	           non-empty key.
//	SILENT     everything else the attempt holds:
//	             - the declaration was REMOVED from declared_resources
//	               (aihub#283 / internal/cli/init.go, example one)
//	             - the declaration is intent=read, so the target loop skips it
//	               while claim had derived the lock anyway
//	               (aihub#297 / .gitignore, example two — that half is aihub#342)
//	             - the lock is git_branch or deploy_env, which this endpoint
//	               filters out of its targets and therefore never mentioned
//	             - the lock came from a client-supplied requested_locks at claim
//	               time and has no declared_resources entry behind it at all
//
// Both recorded incidents are the first bullet or the second, and both ended
// with someone writing down "this attempt holds zero locks". The second one put
// that sentence in a delivery report as a *Correction* to a premise that had
// been right.
//
// # Why this is worse than a reporting nit
//
// The server keeps enforcing the invisible locks: claiming another work item
// over aihub#283's un-reported init.go lock still returned 409
// CONFLICT_LOCK_TAKEN a day later. So the failure mode is an agent concluding
// it holds no locks and writing where it must not — and an agent, unlike a
// human reviewer, has nothing else to consult.
//
// # The fix, and why this shape
//
// `already_held` is now read from resource_locks by owner_attempt_id after
// reconciliation, minus whatever this very call acquired (so the two arrays stay
// disjoint, which is how repeat calls already behaved). That is the only reading
// under which the field cannot mislead, and it is the reading every caller
// already had.
//
// ⚠️ Do NOT verify any of this with pf_acquire_locks' own return value; it is
// the object under test. Every assertion below reads the resource_locks TABLE.
//
// Run:
//
//	AIHUB_TEST_DB=postgres://postgres:testpass@localhost:5432/aihub_test?sslmode=disable \
//	go test ./internal/domain/ -run 'TestAcquireLocksReportsEveryHeldLock' -race -v -count=1

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// heldLockKeys reads the keys an attempt really holds, straight from the table.
func heldLockKeys(t *testing.T, pool *pgxpool.Pool, attemptID string) []string {
	t.Helper()
	rows, err := pool.Query(context.Background(),
		`SELECT resource_key FROM resource_locks WHERE owner_attempt_id=$1 ORDER BY resource_key`, attemptID)
	require.NoError(t, err)
	defer rows.Close()
	keys := []string{}
	for rows.Next() {
		var k string
		require.NoError(t, rows.Scan(&k))
		keys = append(keys, k)
	}
	require.NoError(t, rows.Err())
	return keys
}

// reportedKeys extracts the keys from one side of an AcquireLocksResponse.
func reportedKeys(locks []ResourceLock) []string {
	keys := make([]string, 0, len(locks))
	for _, l := range locks {
		keys = append(keys, l.ResourceKey)
	}
	return keys
}

// TestAcquireLocksReportsEveryHeldLock is aihub#345's acceptance criterion.
//
// One function with subtests: dbtestcov counts DB-gated FUNCTIONS and the
// per-population claim belongs in the subtest names, which ci.yml asserts on.
func TestAcquireLocksReportsEveryHeldLock(t *testing.T) {
	pool := setupLatestTestDB(t)
	ctx := context.Background()

	u := testUser(t, pool)
	project := testProject(t, pool, u)

	const kept = "internal/domain/kept.go"
	const dropped = "internal/domain/dropped.go"
	const added = "internal/domain/added.go"
	keptKey := project + ":" + kept
	droppedKey := project + ":" + dropped
	addedKey := project + ":" + added

	wi := seedClaimableWI(t, pool, project, u,
		"report the locks an attempt holds rather than the ones it would re-derive",
		`[{"type":"repo","uri":"repo:aihub","intent":"write","task_branch":"aihub345"},`+
			`{"type":"path","uri":"file:`+kept+`","intent":"write"},`+
			`{"type":"path","uri":"file:`+dropped+`","intent":"write"}]`)

	claim := claimFresh(t, pool, wi.ID, u, "aihub345-claim")
	attemptID := claim.AttemptID
	require.ElementsMatch(t, []string{"aihub/aihub345", keptKey, droppedKey}, heldLockKeys(t, pool, attemptID),
		"fixture check: the claim must really have taken all three locks, or nothing below is measuring under-reporting")

	acquire := func(t *testing.T) *AcquireLocksResponse {
		t.Helper()
		resp, aerr := FnAcquireLocks(ctx, pool, wi.ID, &AcquireLocksRequest{
			AttemptID:     attemptID,
			ClaimEpoch:    claim.ClaimEpoch,
			SessionSecret: "locktest-secret-0123456789abcdef0123456789abcdef0123456789ab",
		})
		require.Nil(t, aerr, "acquire_locks failed: %+v", aerr)
		return resp
	}

	// The population that ALWAYS worked. It is a subtest, not a footnote,
	// because it is the reason the bug survived: a reporter who tests only this
	// shape sees a correct answer every time and closes the report.
	t.Run("a still-declared lock is reported", func(t *testing.T) {
		resp := acquire(t)
		assert.Contains(t, reportedKeys(resp.AlreadyHeld), keptKey,
			"this is the case that already worked; if it broke, the fix replaced one gap with another")
	})

	// MUTANT: internal/domain/run_attempts.go, FnAcquireLocks — put the
	// `alreadyHeld` appends back inside the target loop and delete the
	// held-lock query. This subtest goes red and the one above stays green,
	// which is exactly the discriminator the item asks for.
	t.Run("a lock whose declaration was removed is still reported", func(t *testing.T) {
		// Example one, reproduced: aihub#283 dropped internal/cli/init.go from
		// its declared_resources, called acquire_locks, got already_held: [],
		// and recorded "the init.go write lock released". A day later the
		// server was still 409ing on it.
		//
		// Raw UPDATE rather than UpdateWorkItem: this arm is about what the
		// LOCK TABLE contains versus what the tool says, and going through the
		// CAS path would add a second suspect.
		_, err := pool.Exec(ctx,
			`UPDATE work_items SET declared_resources = $1::jsonb WHERE id = $2`,
			`[{"type":"repo","uri":"repo:aihub","intent":"write","task_branch":"aihub345"},`+
				`{"type":"path","uri":"file:`+kept+`","intent":"write"}]`, wi.ID)
		require.NoError(t, err)

		require.Contains(t, heldLockKeys(t, pool, attemptID), droppedKey,
			"fixture check: dropping a declaration must NOT release the lock — if it did, the premise of this "+
				"whole item is gone and the assertion below would be testing nothing")

		resp := acquire(t)
		assert.Contains(t, reportedKeys(resp.AlreadyHeld), droppedKey,
			"the attempt still holds %q — the server will still 409 anybody else who claims it — but "+
				"already_held did not mention it. That is how an agent concludes it holds zero locks and "+
				"writes unprotected. reported=%v held=%v",
			droppedKey, reportedKeys(resp.AlreadyHeld), heldLockKeys(t, pool, attemptID))
	})

	// The second silent population: locks this endpoint never acquires and so
	// never used to name. git_branch is taken at claim from a `repo`
	// declaration and released only at wrap, so it is held for the entire life
	// of every ordinary attempt.
	t.Run("a non-file-scope lock is reported", func(t *testing.T) {
		resp := acquire(t)
		assert.Contains(t, reportedKeys(resp.AlreadyHeld), "aihub/aihub345",
			"this endpoint only ACQUIRES file_scope, but the question `already_held` answers is what the "+
				"attempt holds; a git_branch lock it is silently holding is exactly what misleads a caller "+
				"who reads the empty-looking answer as `no locks`")
	})

	// The partition must stay a partition: a lock taken by THIS call belongs in
	// `acquired`, not in `already_held`. Without this, "report everything held"
	// could be satisfied by reporting each new lock in both arrays, and a
	// caller diffing the two across repeat calls would see nothing move.
	t.Run("a newly taken lock is acquired not already held", func(t *testing.T) {
		_, err := pool.Exec(ctx,
			`UPDATE work_items SET declared_resources = $1::jsonb WHERE id = $2`,
			`[{"type":"repo","uri":"repo:aihub","intent":"write","task_branch":"aihub345"},`+
				`{"type":"path","uri":"file:`+kept+`","intent":"write"},`+
				`{"type":"path","uri":"file:`+added+`","intent":"write"}]`, wi.ID)
		require.NoError(t, err)

		resp := acquire(t)
		assert.Equal(t, []string{addedKey}, reportedKeys(resp.Acquired),
			"only the newly declared path was free to take on this call")
		assert.NotContains(t, reportedKeys(resp.AlreadyHeld), addedKey,
			"a lock taken by this very call must not also appear under already_held, or the two arrays stop "+
				"telling the caller what changed")
		assert.ElementsMatch(t,
			append(reportedKeys(resp.Acquired), reportedKeys(resp.AlreadyHeld)...),
			heldLockKeys(t, pool, attemptID),
			"acquired + already_held must together be exactly what the table says this attempt holds")
	})

	// The read-intent population (aihub#297 / .gitignore, example two). After
	// aihub#342 claim no longer derives a lock for an intent=read declaration,
	// so this reproduces the shape by seeding the lock row directly — the state
	// a work item claimed before that fix is still in today.
	t.Run("a lock behind a read declaration is reported", func(t *testing.T) {
		const readPath = ".gitignore"
		readKey := project + ":" + readPath
		_, err := pool.Exec(ctx,
			`INSERT INTO resource_locks (resource_type, resource_key, owner_attempt_id, claim_epoch)
			 VALUES ('file_scope', $1, $2, $3)`, readKey, attemptID, claim.ClaimEpoch)
		require.NoError(t, err)
		_, err = pool.Exec(ctx,
			`UPDATE work_items SET declared_resources = $1::jsonb WHERE id = $2`,
			`[{"type":"path","uri":"file:`+readPath+`","intent":"read"}]`, wi.ID)
		require.NoError(t, err)

		resp := acquire(t)
		assert.Contains(t, reportedKeys(resp.AlreadyHeld), readKey,
			"the target loop skips intent=read, so this lock used to fall out of the answer entirely — "+
				"which produced the delivery report sentence \"this attempt holds zero locks\" while the "+
				"server was enforcing it")
		assert.NotContains(t, reportedKeys(resp.Acquired), readKey,
			"reporting a lock must not be confused with taking one; intent=read still acquires nothing")
	})
}
