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
// ⚠️ aihub#264 has since shrunk the first bullet, and this comment would rot
// silently without saying so: removing a declaration through UpdateWorkItem now
// releases its file_scope lock in the same transaction, so that population is no
// longer produced by the ordinary API path. It still exists — locks taken before
// aihub#264, locks from a client-supplied requested_locks, and the git_branch /
// deploy_env locks aihub#264 deliberately leaves alone — and `already_held` must
// still report every one of them, which is why nothing below was deleted.
//
// One caveat, so that list is not read as broader than it is: a file_scope lock
// taken from a client-supplied requested_locks survives a narrowing only while
// its key is NOT also in the declaration being replaced. If it is, the aihub#264
// diff releases it like any other — the release is keyed on the lock KEY and
// cannot tell which mechanism created the row.
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
// the object under test. Every subtest below pairs its claim about the RESPONSE
// with a read of the resource_locks TABLE for the same key, so "reported" is
// always checked against a fact the object under test did not supply. Where a
// subtest asserts under-reporting, the table read is what establishes the lock
// exists at all; where it asserts the partition, the table read is the total.
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
	// The ":aihub:" segment is the repo, not a typo (aihub#261): this fixture
	// declares {"type":"repo","uri":"repo:aihub"} alongside its paths, so the
	// repo-relative paths inherit that repo and the derived keys name it. A
	// fixture that declared no repo would still key on "<project>:<path>".
	keptKey := project + ":aihub:" + kept
	droppedKey := project + ":aihub:" + dropped
	addedKey := project + ":aihub:" + added

	wi := seedClaimableWI(t, pool, project, u,
		"report the locks an attempt holds rather than the ones it would re-derive",
		`[{"type":"repo","uri":"repo:aihub","intent":"write","task_branch":"aihub345"},`+
			`{"type":"path","uri":"file:`+kept+`","intent":"write"},`+
			`{"type":"path","uri":"file:`+dropped+`","intent":"write"}]`)

	// 🔴 The git_branch row is now REQUESTED, not derived (aihub#416). The repo
	// declaration above no longer produces one, and this suite needs such a row
	// as a fixture: the population it is about — "a lock the attempt holds that
	// no current declaration explains" — is exactly what `already_held` used to
	// under-report, and it still exists. requested_locks is the surviving way to
	// create it (owner ruling Q-3 kept that path deliberately open).
	//
	// The repo entry is LEFT in the declaration on purpose, as the negative
	// control for the retirement itself: if it ever started deriving a lock
	// again, the ElementsMatch below would find four keys, not three.
	claim := claimFreshWithLocks(t, pool, wi.ID, u, "aihub345-claim", []ResourceLockReq{
		{ResourceType: "git_branch", ResourceKey: "aihub/aihub345"},
		{ResourceType: "file_scope", ResourceKey: keptKey},
		{ResourceType: "file_scope", ResourceKey: droppedKey},
	})
	attemptID := claim.AttemptID
	require.ElementsMatch(t, []string{"aihub/aihub345", keptKey, droppedKey}, heldLockKeys(t, pool, attemptID),
		"fixture check: the claim must really have taken all three locks, or nothing below is measuring under-reporting")

	// aihub#492's retry, applied here by aihub#509 because that item's own
	// coverage note does not reach this file. It says the retry was put in
	// `claimWI` so that seven suites including this one are "covered without
	// being edited" — but this suite claims through `claimFreshWithLocks`
	// (lock_intent_derivation_db_test.go), which is a different helper and was
	// not wrapped, and nothing wrapped FnAcquireLocks here at all.
	// FnAcquireLocks is SERIALIZABLE and serialization_retry_test.go's header
	// names it explicitly, so every arm below could lose an SSI race to another
	// DB-gated test binary and report a failure the database asked us to retry.
	//
	// In the shared closure rather than at the new call sites, for the reason
	// aihub#492 gives: the five arms that predate aihub#509 are covered without
	// being edited. It cannot weaken any of them — the helper retries only
	// ErrConflictSerializationFailure, never ErrInternalError, and SSI gives the
	// retry a fresh serializable execution, so a refusal that should happen
	// still does.
	acquire := func(t *testing.T) *AcquireLocksResponse {
		t.Helper()
		resp, aerr := retryOnSerializationConflict(t, "acquire_locks", func() (*AcquireLocksResponse, *AihubError) {
			return FnAcquireLocks(ctx, pool, wi.ID, &AcquireLocksRequest{
				AttemptID:     attemptID,
				ClaimEpoch:    claim.ClaimEpoch,
				SessionSecret: "locktest-secret-0123456789abcdef0123456789abcdef0123456789ab",
			})
		})
		require.Nil(t, aerr, "acquire_locks failed: %+v", aerr)
		return resp
	}

	// The population that ALWAYS worked. It is a subtest, not a footnote,
	// because it is the reason the bug survived: a reporter who tests only this
	// shape sees a correct answer every time and closes the report.
	t.Run("a still-declared lock is reported", func(t *testing.T) {
		require.Contains(t, heldLockKeys(t, pool, attemptID), keptKey,
			"fixture check: the attempt must really hold %q, or 'it was reported' proves nothing", keptKey)
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
		// 🔴 aihub#264 CHANGED THE BEHAVIOUR THIS ARM RECORDS. When this test was
		// written, removing a declaration through UpdateWorkItem left the lock in
		// place — that was the defect aihub#264 then fixed, and the fixture check
		// below ("dropping a declaration must NOT release the lock") was a true
		// statement about the whole system. It no longer is: a removal made
		// through the API now releases the lock in the same transaction.
		//
		// The arm still passes, and still tests what it always tested, for one
		// reason: it drives declared_resources with a RAW UPDATE. That was
		// originally a way to keep the CAS path from adding a second suspect;
		// after aihub#264 it is also the only reason this state is still
		// reachable here, so it is now LOAD-BEARING. Do not "tidy" it into
		// UpdateWorkItem — that would release the lock, and this arm would stop
		// measuring under-reporting.
		//
		// The state is still reachable in production, which is why the arm is
		// kept rather than deleted: locks predating aihub#264, locks from a
		// client-supplied requested_locks with no declaration behind them, and
		// git_branch/deploy_env locks, which aihub#264 deliberately does not
		// release. The API-path behaviour after aihub#264 is pinned separately,
		// by TestNarrowingDeclaredResourcesReleasesItsLocks/
		// a_released_lock_is_absent_from_already_held_not_merely_unreported.
		_, err := pool.Exec(ctx,
			`UPDATE work_items SET declared_resources = $1::jsonb WHERE id = $2`,
			`[{"type":"repo","uri":"repo:aihub","intent":"write","task_branch":"aihub345"},`+
				`{"type":"path","uri":"file:`+kept+`","intent":"write"}]`, wi.ID)
		require.NoError(t, err)

		require.Contains(t, heldLockKeys(t, pool, attemptID), droppedKey,
			"fixture check: a raw UPDATE of declared_resources must not release the lock, or the assertion "+
				"below would be testing nothing. If this fails, the release added by aihub#264 has been "+
				"moved out of UpdateWorkItem and onto the column itself (a trigger, or a rewrite of this "+
				"line to use UpdateWorkItem) — re-seed the lock some other way rather than deleting the arm")

		resp := acquire(t)
		assert.Contains(t, reportedKeys(resp.AlreadyHeld), droppedKey,
			"the attempt still holds %q — the server will still 409 anybody else who claims it — but "+
				"already_held did not mention it. That is how an agent concludes it holds zero locks and "+
				"writes unprotected. reported=%v held=%v",
			droppedKey, reportedKeys(resp.AlreadyHeld), heldLockKeys(t, pool, attemptID))
	})

	// The second silent population: locks this endpoint never acquires and so
	// never used to name.
	//
	// ⚠️ ITS SOURCE CHANGED, its existence did not (aihub#416). git_branch used
	// to be taken at claim from a `repo` declaration, which made this the
	// ordinary state of every attempt in the system. It is now reachable only
	// through an explicit requested_locks, so the population is RARE rather than
	// universal — and that makes reporting it more important, not less: a caller
	// who now sees a non-file_scope key in already_held has no declaration
	// anywhere to explain it from.
	t.Run("a non-file-scope lock is reported", func(t *testing.T) {
		require.Contains(t, heldLockKeys(t, pool, attemptID), "aihub/aihub345",
			"fixture check: the git_branch lock must really be held, or this arm asserts nothing")
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

		require.Contains(t, heldLockKeys(t, pool, attemptID), readKey,
			"fixture check: the seeded lock row must be in the table and owned by this attempt, or the "+
				"assertion below is about a lock that does not exist")

		resp := acquire(t)
		assert.Contains(t, reportedKeys(resp.AlreadyHeld), readKey,
			"the target loop skips intent=read, so this lock used to fall out of the answer entirely — "+
				"which produced the delivery report sentence \"this attempt holds zero locks\" while the "+
				"server was enforcing it")
		assert.NotContains(t, reportedKeys(resp.Acquired), readKey,
			"reporting a lock must not be confused with taking one; intent=read still acquires nothing")
	})

	// aihub#509 — the THIRD silence on this endpoint, and the one that is not
	// about a lock at all.
	//
	// The four arms above are about locks the attempt HOLDS and the response did
	// not name. This one is about a declaration that holds NOTHING and the
	// response did not name either: an entry the mapper cannot understand derives
	// no target, falls out of the loop, and therefore appears in neither
	// `acquired` nor `already_held`. Both lists come back complete and correct,
	// and the caller reads "these are my locks" from an answer that never
	// mentions the declaration it is missing one for.
	//
	// That is the same defect aihub#238 closed on the claim path, arriving on a
	// different endpoint; aihub#411 T2-12 recorded it and aihub#416 shrank it
	// (repo and service now derive no lock BY DESIGN, so they are not unmappable
	// and are not reported) without closing it.
	//
	// ⚠️ The RAW UPDATE is load-bearing for the same reason as in the
	// removed-declaration arm above, and for one more: ValidateDeclaredResources
	// rejects both shapes below at the API, deliberately, so the only way this
	// state exists is as STORED data that predates the validator — which is
	// precisely the population the report is for ("roughly 14% of existing
	// entries would fail it, and those work items must stay claimable").
	// seedClaimableWI goes through CreateWorkItem and would 400.
	//
	// MUTANT: internal/domain/run_attempts.go, FnAcquireLocks — drop
	// `UnrecognizedResources` from the returned AcquireLocksResponse literal.
	// Only this subtest goes red; the four above stay green, because none of
	// them reads that field.
	t.Run("an unmappable declaration is reported", func(t *testing.T) {
		// Both shapes UnrecognizedDeclaredResources recognises, so the arm
		// cannot pass by handling one of them:
		//   1. a type outside the declared vocabulary — here `file_scope`, which
		//      is a resource_locks type and the exact confusion aihub#238 names;
		//   2. a legal type with no `uri` — here `service`, which since aihub#416
		//      derives nothing at all, so "no uri" and "no lock" are the same
		//      fact about it.
		// Plus a healthy path entry, as the control that "report everything"
		// is not satisfied by reporting everything.
		//
		// ⚠️ `{"type":"path"}` with no uri would ALSO be reported, and is
		// deliberately not the fixture used here. Measured on this tree:
		// deriveClaimLocks turns it into file_scope `"<project>:"` — a real lock
		// on a junk key — because fileScopeLockKey always emits the project
		// prefix, so the `lockKey == ""` skip never fires for it. The report's
		// wording ("acquires no lock") is therefore inaccurate for exactly that
		// shape. That is a pre-existing property of UnrecognizedDeclaredResources
		// and not aihub#509's to change — aihub#509 mirrors the claim path's
		// report, it does not redefine it — but building this arm on it would
		// pin the inaccuracy.
		const healthy = "internal/domain/healthy509.go"
		_, err := pool.Exec(ctx,
			`UPDATE work_items SET declared_resources = $1::jsonb WHERE id = $2`,
			`[{"type":"file_scope","uri":"file:internal/domain/mistyped509.go","intent":"write"},`+
				`{"type":"service","intent":"write"},`+
				`{"type":"path","uri":"file:`+healthy+`","intent":"write"}]`, wi.ID)
		require.NoError(t, err)

		resp := acquire(t)

		require.Len(t, resp.UnrecognizedResources, 2,
			"two entries derive no lock and neither can be seen in acquired/already_held; got %v "+
				"(acquired=%v already_held=%v)",
			resp.UnrecognizedResources, reportedKeys(resp.Acquired), reportedKeys(resp.AlreadyHeld))
		assert.Contains(t, resp.UnrecognizedResources[0], "file_scope",
			"the report must name the offending type — `file_scope` is a resource_locks type, not a "+
				"declared one, and telling the caller which of its entries is inert is the whole point")
		assert.Contains(t, resp.UnrecognizedResources[0], "mistyped509.go",
			"the report must quote the uri, or a caller with several entries of one type cannot tell which")
		assert.Contains(t, resp.UnrecognizedResources[1], "`uri`",
			"the no-uri shape must be reported as a MISSING FIELD, not as an unknown type; those are "+
				"different mistakes with different repairs")
		assert.Contains(t, resp.UnrecognizedResources[1], "service",
			"the report must name the entry's type, or a caller cannot tell which of its declarations "+
				"is the one missing a uri")

		// The control, and it is the half a "report everything" mutant fails:
		// the healthy entry must be a LOCK, not a warning.
		assert.NotContains(t, resp.UnrecognizedResources[0]+resp.UnrecognizedResources[1], healthy,
			"a well-formed path entry was reported as unmappable — this would make the field noise, and "+
				"a field that fires on healthy input stops being read")
		assert.Contains(t,
			append(reportedKeys(resp.Acquired), reportedKeys(resp.AlreadyHeld)...),
			project+":"+healthy,
			"fixture check: the healthy entry must really have produced a lock, or the assertion above "+
				"passes because nothing was derived at all")

		// And the shape stays a shape: unrecognized_resources is a report on
		// DECLARATIONS, so it must not disturb the acquired/already_held
		// partition the four arms above establish.
		assert.ElementsMatch(t,
			append(reportedKeys(resp.Acquired), reportedKeys(resp.AlreadyHeld)...),
			heldLockKeys(t, pool, attemptID),
			"acquired + already_held must still be exactly what the table says this attempt holds; the "+
				"new field reports declarations that took no lock, it does not add a third lock list")
	})

	// The negative control for the arm above: on a healthy payload the field is
	// absent, not empty-but-present.
	//
	// It is a separate subtest because it fails for a DIFFERENT mutant — one that
	// hard-codes a non-nil report, or wires the wrong producer in — and because
	// `omitempty` is the convention every other report on these responses uses
	// (see RequestAdjustment): a caller must be able to read "no key" as "nothing
	// to say" rather than as "old server".
	t.Run("a clean declaration reports nothing", func(t *testing.T) {
		_, err := pool.Exec(ctx,
			`UPDATE work_items SET declared_resources = $1::jsonb WHERE id = $2`,
			`[{"type":"path","uri":"file:internal/domain/clean509.go","intent":"write"},`+
				`{"type":"repo","uri":"repo:aihub","intent":"write"},`+
				`{"type":"external_ref","uri":"https://example.invalid/509"}]`, wi.ID)
		require.NoError(t, err)

		resp := acquire(t)
		assert.Empty(t, resp.UnrecognizedResources,
			"repo, external_ref and a well-formed path are all LEGAL declarations. repo derives no lock "+
				"since aihub#416 and external_ref never did, and neither is unmappable — reporting them "+
				"would make the field fire on every healthy work item in the system (aihub#509)")
	})
}
