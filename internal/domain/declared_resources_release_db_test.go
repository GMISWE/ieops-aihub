package domain

// DB-gated integration tests for aihub#264: narrowing declared_resources must
// release the file_scope locks the removed entries were holding.
//
// # What was wrong, exactly
//
// Lock ACQUISITION happens on the claim path (run_attempts.go); declared_resources
// MUTATION happens on the update path (work_items.go). Nothing connected them, so
// a lock outlived the declaration that justified it and was held until the work
// item reached a terminal state. A long-running work item accumulated, monotonically,
// every path it had ever declared.
//
// Reported from ieops#824: a claim 409'd on CONFLICT_LOCK_TAKEN for
// `ieops:pkg/gateway/business/center.go` held by ieops#798, whose declared_resources
// at resources_version 17 contained no form of that path — the lock was residue from
// some earlier version, and the delivered PR had never touched the file.
//
// # Why the blocked party could not work around it
//
// The second half of the item is what makes this more than a leak. The blocked
// caller cannot see the holder, so "somebody is editing this file" and "a version
// from days ago once mentioned this file" are indistinguishable from the outside.
// ieops#824's executor only established it was residue via three pieces of evidence
// external to polyforge (the holder's current declarations, the holder's merged PR
// file list, the absence of the remote branch), all of which needed read access to
// another team's repository. So the assertions below deliberately do not stop at the
// resource_locks table: each one ends at the hop the blocked party can actually
// observe, a real FnClaimWorkItem by a second work item.
//
// # Scope of the release, and why it is file_scope only
//
// See releaseUndeclaredFileScopeLocks in work_items.go. Short version: FnAcquireLocks
// re-acquires file_scope and nothing else, so a file_scope release is symmetric with
// an existing, documented way to take the lock back, while releasing a git_branch
// lock would be unrecoverable until the next claim and would put a second attempt on
// a branch the first still has checked out.
//
// Run:
//
//	AIHUB_TEST_DB=postgres://postgres:testpass@localhost:5432/aihub_test?sslmode=disable \
//	go test ./internal/domain/ -run 'TestNarrowingDeclaredResourcesReleasesItsLocks' -race -v -count=1

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// updateDeclared drives the REAL update path (UpdateWorkItem), not a raw UPDATE.
//
// That choice is the whole point of this file. The release hangs off the update
// path, so a test that writes declared_resources with SQL would exercise none of
// it and would pass just as happily against the unfixed server.
func updateDeclared(t *testing.T, pool *pgxpool.Pool, wiID, userID, declared string) *WorkItem {
	t.Helper()
	raw := json.RawMessage(declared)
	wi, aerr := UpdateWorkItem(context.Background(), pool, wiID, userID, "tester",
		map[string]string{}, &UpdateWorkItemRequest{DeclaredResources: raw})
	require.Nil(t, aerr, "update declared_resources failed: %+v", aerr)
	return wi
}

// claimBlocks reports whether a fresh claim of wiID fails with CONFLICT_LOCK_TAKEN
// — i.e. whether the blocked party is still blocked. It returns the error code so a
// failure message can distinguish "blocked" from "broke for some other reason",
// which an assertion on a bare bool cannot.
func claimBlocks(t *testing.T, pool *pgxpool.Pool, wiID, userID, idemKey string) (blocked bool, code ErrCode) {
	t.Helper()
	_, aerr := FnClaimWorkItem(context.Background(), pool, wiID, &ClaimRequest{
		IdempotencyKey: idemKey,
		SessionInfo: SessionInfo{
			MachineID:     "m_locktest",
			SessionSecret: "locktest-secret-0123456789abcdef0123456789abcdef0123456789ab",
		},
		Mode: "fresh",
	}, userID, "", "tester")
	if aerr == nil {
		return false, ""
	}
	return aerr.Code == ErrConflictLockTaken, aerr.Code
}

// TestNarrowingDeclaredResourcesReleasesItsLocks is aihub#264's acceptance criterion.
//
// One function with subtests: dbtestcov counts DB-gated FUNCTIONS, and the
// per-arm claims belong in subtest names, which ci.yml asserts on individually.
func TestNarrowingDeclaredResourcesReleasesItsLocks(t *testing.T) {
	pool := setupLatestTestDB(t)
	ctx := context.Background()

	u := testUser(t, pool)
	project := testProject(t, pool, u)

	const kept = "internal/domain/kept264.go"
	const dropped = "internal/domain/dropped264.go"
	const widened = "internal/domain/widened264.go"
	const flipped = "internal/domain/flipped264.go"
	// The ":aihub:" segment is the repo (aihub#261): declaredAll below declares
	// {"type":"repo","uri":"repo:aihub"}, so these repo-relative paths inherit it.
	keptKey := project + ":aihub:" + kept
	droppedKey := project + ":aihub:" + dropped
	widenedKey := project + ":aihub:" + widened
	flippedKey := project + ":aihub:" + flipped

	declaredAll := `[{"type":"repo","uri":"repo:aihub","intent":"write","task_branch":"aihub264"},` +
		`{"type":"path","uri":"file:` + kept + `","intent":"write"},` +
		`{"type":"path","uri":"file:` + flipped + `","intent":"write"},` +
		`{"type":"path","uri":"file:` + dropped + `","intent":"write"}]`

	holder := seedClaimableWI(t, pool, project, u,
		"release the file_scope locks whose declaration was removed", declaredAll)
	claim := claimFresh(t, pool, holder.ID, u, "aihub264-claim")

	require.ElementsMatch(t, []string{"aihub/aihub264", droppedKey, flippedKey, keptKey},
		heldLockKeys(t, pool, claim.AttemptID),
		"fixture check: the claim must really have taken all four locks, or nothing below is measuring a release")

	// MUTANT: internal/domain/work_items.go, UpdateWorkItem — delete the
	// releaseUndeclaredFileScopeLocks call. This subtest goes red; "a still
	// declared lock survives" below stays green, which is what distinguishes
	// "removed locks are released" from "locks stopped being taken at all".
	t.Run("a removed declaration releases its lock", func(t *testing.T) {
		blockedBefore, codeBefore := claimBlocks(t, pool, seedClaimableWI(t, pool, project, u,
			"blocked party before the narrowing",
			`[{"type":"path","uri":"file:`+dropped+`","intent":"write"}]`).ID, u, "aihub264-blocked-before")
		require.True(t, blockedBefore,
			"fixture check: a second work item declaring %q must be blocked BEFORE the narrowing, or the "+
				"unblocking asserted below proves nothing. got code=%q", dropped, codeBefore)

		updateDeclared(t, pool, holder.ID, u,
			`[{"type":"repo","uri":"repo:aihub","intent":"write","task_branch":"aihub264"},`+
				`{"type":"path","uri":"file:`+kept+`","intent":"write"},`+
				`{"type":"path","uri":"file:`+flipped+`","intent":"write"}]`)

		assert.Equal(t, 0, countFileScopeLocks(t, pool, droppedKey),
			"%q is no longer declared by the holder, so its lock must be gone from resource_locks; "+
				"held=%v", droppedKey, heldLockKeys(t, pool, claim.AttemptID))

		// The hop the blocked party can actually see. The table read above is the
		// mechanism; this is the consequence, and it is the one ieops#824 needed.
		other := seedClaimableWI(t, pool, project, u, "blocked party after the narrowing",
			`[{"type":"path","uri":"file:`+dropped+`","intent":"write"}]`)
		blocked, code := claimBlocks(t, pool, other.ID, u, "aihub264-blocked-after")
		assert.False(t, blocked,
			"a second work item declaring %q must now be able to claim it — before this fix the lock outlived "+
				"the declaration and 409'd forever, with the blocked party unable to tell residue from real "+
				"contention. got code=%q", dropped, code)
	})

	// The aihub#345 interaction, measured rather than reasoned about.
	//
	// These two defects covered for each other. already_held went silent for a
	// removed declaration BECAUSE the lock survived while dropping out of the
	// re-derived target set; aihub#345 fixed the reporting so the surviving lock
	// was named. Now that the lock no longer survives a removal made through the
	// API, the honest answer changes again — from "held, and now reported" to
	// "not held, so nothing to report" — and only a test that goes through
	// UpdateWorkItem can see it. aihub#345's own arm drives declared_resources
	// with a raw UPDATE, so it still observes the old shape and still passes;
	// that is correct for what it pins, and this is the arm that pins the rest.
	t.Run("a released lock is absent from already_held not merely unreported", func(t *testing.T) {
		// Note the oracle: NOT countFileScopeLocks. The arm above ends by having a
		// second work item successfully claim this very path, so a row for
		// droppedKey exists again — owned by that other attempt. The question here
		// is whether THIS attempt still holds it, and only an owner-scoped read
		// answers that. A count-based check would fail for the wrong reason and
		// read as if the release had not happened.
		require.NotContains(t, heldLockKeys(t, pool, claim.AttemptID), droppedKey,
			"fixture check: the previous arm must have released %q from this attempt", droppedKey)

		resp, aerr := FnAcquireLocks(ctx, pool, holder.ID, &AcquireLocksRequest{
			AttemptID:     claim.AttemptID,
			ClaimEpoch:    claim.ClaimEpoch,
			SessionSecret: "locktest-secret-0123456789abcdef0123456789abcdef0123456789ab",
		})
		require.Nil(t, aerr, "acquire_locks failed: %+v", aerr)

		assert.NotContains(t, reportedKeys(resp.AlreadyHeld), droppedKey,
			"already_held reads the lock TABLE (aihub#345), so once aihub#264 deletes the row the key must "+
				"stop appearing — if it is still listed, the release did not actually happen and the two "+
				"fixes are disagreeing about the same lock")
		assert.ElementsMatch(t,
			append(reportedKeys(resp.Acquired), reportedKeys(resp.AlreadyHeld)...),
			heldLockKeys(t, pool, claim.AttemptID),
			"aihub#345's partition invariant must survive aihub#264: the two arrays are still exactly what "+
				"the table says this attempt holds")
	})

	// The control that keeps the arm above honest: "release the removed ones"
	// must not be satisfiable by releasing everything. Without this, deleting
	// every file_scope lock on any update would pass.
	t.Run("a still declared lock survives", func(t *testing.T) {
		assert.Equal(t, 1, countFileScopeLocks(t, pool, keptKey),
			"%q is still declared, so the narrowing must not have touched its lock — a release that "+
				"drops still-declared locks is a protection regression, not a fix", keptKey)

		other := seedClaimableWI(t, pool, project, u, "contender for the still declared path",
			`[{"type":"path","uri":"file:`+kept+`","intent":"write"}]`)
		blocked, code := claimBlocks(t, pool, other.ID, u, "aihub264-kept-contender")
		assert.True(t, blocked,
			"the holder still declares %q, so a competing claim must still be refused; got code=%q", kept, code)
	})

	// Criterion 2, the reverse direction. Testing only narrowing would let
	// "stop taking locks altogether" pass every arm above.
	t.Run("widening still takes the new lock", func(t *testing.T) {
		updateDeclared(t, pool, holder.ID, u,
			`[{"type":"repo","uri":"repo:aihub","intent":"write","task_branch":"aihub264"},`+
				`{"type":"path","uri":"file:`+kept+`","intent":"write"},`+
				`{"type":"path","uri":"file:`+flipped+`","intent":"write"},`+
				`{"type":"path","uri":"file:`+widened+`","intent":"write"}]`)

		resp, aerr := FnAcquireLocks(ctx, pool, holder.ID, &AcquireLocksRequest{
			AttemptID:     claim.AttemptID,
			ClaimEpoch:    claim.ClaimEpoch,
			SessionSecret: "locktest-secret-0123456789abcdef0123456789abcdef0123456789ab",
		})
		require.Nil(t, aerr, "acquire_locks after widening failed: %+v", aerr)

		assert.Contains(t, reportedKeys(resp.Acquired), widenedKey,
			"a newly declared path must still be acquirable; if the release logic also ran on a widening "+
				"it would take the lock and immediately drop it")
		assert.Equal(t, 1, countFileScopeLocks(t, pool, widenedKey),
			"and the row must actually be in the table afterwards")
	})

	// The aihub#342 interaction. intent=read "takes no write lock" is the
	// contract derivedLock enforces at acquisition; a declaration flipped from
	// write to read must therefore also GIVE UP the write lock it already has,
	// or the two halves of that contract disagree about the same input.
	t.Run("flipping a declaration to read intent releases its write lock", func(t *testing.T) {
		require.Equal(t, 1, countFileScopeLocks(t, pool, flippedKey),
			"fixture check: %q must be locked before the flip", flippedKey)

		updateDeclared(t, pool, holder.ID, u,
			`[{"type":"repo","uri":"repo:aihub","intent":"write","task_branch":"aihub264"},`+
				`{"type":"path","uri":"file:`+kept+`","intent":"write"},`+
				`{"type":"path","uri":"file:`+widened+`","intent":"write"},`+
				`{"type":"path","uri":"file:`+flipped+`","intent":"read"}]`)

		assert.Equal(t, 0, countFileScopeLocks(t, pool, flippedKey),
			"the entry is still present but is now intent=read, which derivedLock maps to NO write lock; "+
				"keeping the old write lock would mean read and write intent produce the same enforcement")
	})

	// Criterion 4: the release must be inside the CAS. A rejected update must
	// leave the locks exactly as it found them, or a caller who retries after a
	// 409 has already lost protection it still believes it holds.
	t.Run("a rejected CAS releases nothing", func(t *testing.T) {
		before := heldLockKeys(t, pool, claim.AttemptID)
		require.Contains(t, before, keptKey,
			"fixture check: %q must be held going into the CAS arm", keptKey)

		stale := -1
		_, aerr := UpdateWorkItem(ctx, pool, holder.ID, u, "tester", map[string]string{},
			&UpdateWorkItemRequest{
				DeclaredResources: json.RawMessage(`[]`),
				ResourcesVersion:  &stale,
			})
		require.NotNil(t, aerr, "an update carrying resources_version=-1 must be rejected")
		assert.Equal(t, ErrConflictCASFailed, aerr.Code,
			"expected the CAS guard to reject it, got %q", aerr.Code)

		assert.ElementsMatch(t, before, heldLockKeys(t, pool, claim.AttemptID),
			"the update was rejected, so resources_version did not move and neither may the locks — "+
				"a release that outlives its own transaction is exactly the 'version unchanged but locks "+
				"changed' intermediate state the item warns about")
	})

	// An entry the VALIDATOR accepts but a typed unmarshal rejects must not
	// disable the release for the whole array.
	//
	// ValidateDeclaredResources decodes into []map[string]any and type-asserts
	// only `type` and `uri`, so `intent` (or task_branch/base_branch) holding a
	// non-string sails through it. Deriving keys with a strict
	// []DeclaredResourceItem unmarshal then failed on the whole payload, and the
	// release was skipped in silence: measured on that version, this update
	// narrowed the declaration, bumped resources_version, returned no error, and
	// left the lock in place — aihub#264's own defect, reachable from caller
	// input, straight through the fix for it.
	t.Run("a wrong typed optional field does not disable the release", func(t *testing.T) {
		const odd = "internal/domain/odd264.go"
		oddKey := project + ":" + odd

		updateDeclared(t, pool, holder.ID, u,
			`[{"type":"path","uri":"file:`+kept+`","intent":"write"},`+
				`{"type":"path","uri":"file:`+odd+`","intent":"write"}]`)
		resp, aerr := FnAcquireLocks(ctx, pool, holder.ID, &AcquireLocksRequest{
			AttemptID:     claim.AttemptID,
			ClaimEpoch:    claim.ClaimEpoch,
			SessionSecret: "locktest-secret-0123456789abcdef0123456789abcdef0123456789ab",
		})
		require.Nil(t, aerr, "acquire_locks failed: %+v", aerr)
		require.Contains(t, reportedKeys(resp.Acquired), oddKey, "fixture check: %q must be locked", oddKey)

		// `intent` is a BOOLEAN here. The entry for `odd` is gone, so its lock
		// must go with it — the malformed field on the SURVIVING entry must not
		// take the whole diff down.
		updateDeclared(t, pool, holder.ID, u,
			`[{"type":"path","uri":"file:`+kept+`","intent":true}]`)

		assert.Equal(t, 0, countFileScopeLocks(t, pool, oddKey),
			"%q was dropped from the declaration, so its lock must be released even though a "+
				"SIBLING entry carries a wrong-typed `intent`. A decoder stricter than the validator "+
				"silently skips the release for the entire array. held=%v",
			oddKey, heldLockKeys(t, pool, claim.AttemptID))
	})

	// An explicit JSON null must be "not specified", not "declare nothing".
	// Before the fold, this stored a jsonb null — destroying every declaration —
	// and the release then derived an empty key set and dropped every file_scope
	// lock the work item held. Both halves silent, both with HTTP 200.
	t.Run("an explicit null declaration changes nothing", func(t *testing.T) {
		before := heldLockKeys(t, pool, claim.AttemptID)
		require.NotEmpty(t, before, "fixture check: the attempt must hold something going into this arm")

		wi, aerr := UpdateWorkItem(ctx, pool, holder.ID, u, "tester", map[string]string{},
			&UpdateWorkItemRequest{DeclaredResources: json.RawMessage(`null`)})
		require.Nil(t, aerr, "a null declared_resources must be accepted as a no-op: %+v", aerr)

		assert.ElementsMatch(t, before, heldLockKeys(t, pool, claim.AttemptID),
			"a null payload must not release anything — a caller sending null meaning 'leave this "+
				"alone' would otherwise lose every file lock it holds, silently")
		assert.NotEqual(t, "null", strings.TrimSpace(string(wi.DeclaredResources)),
			"and it must not overwrite the stored declarations with a jsonb null either; "+
				"clearing them is spelled []")
	})

	// Blast radius: the release is keyed on the work item being updated. A
	// narrowing on one work item must not reach into another's locks, however
	// similar the key looks.
	t.Run("another work items lock is untouched", func(t *testing.T) {
		const neighbour = "internal/domain/neighbour264.go"
		neighbourKey := project + ":" + neighbour

		nWI := seedClaimableWI(t, pool, project, u, "an unrelated holder in the same project",
			`[{"type":"path","uri":"file:`+neighbour+`","intent":"write"}]`)
		nClaim := claimFresh(t, pool, nWI.ID, u, "aihub264-neighbour-claim")
		require.Equal(t, 1, countFileScopeLocks(t, pool, neighbourKey),
			"fixture check: the neighbour must hold its own lock")

		// The holder narrows to nothing at all — the widest possible removal.
		updateDeclared(t, pool, holder.ID, u, `[]`)

		assert.Equal(t, 1, countFileScopeLocks(t, pool, neighbourKey),
			"the neighbour declared %q and never changed its declarations; a release scoped to the wrong "+
				"work item would take it out. held by neighbour=%v",
			neighbour, heldLockKeys(t, pool, nClaim.AttemptID))
		assert.Equal(t, 0, countFileScopeLocks(t, pool, keptKey),
			"the holder's own remaining file_scope lock IS released by narrowing to []")
	})
}
