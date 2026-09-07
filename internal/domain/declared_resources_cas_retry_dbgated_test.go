package domain

// DB-gated invariant tests for aihub#421, flow `cas_failure` — the third of the
// four abnormal flows the aihub#412 corpus mining turned up.
//
// Provenance. docs/audits/aihub-412-corpus-facts/sequence-inventory.md finds 9
// groups (0.33% of 2756) matching "a call fails with an error code containing
// CAS_FAILED". Of the three traces it prints, exactly one is a
// declared_resources CAS — the other two fail on pf_update_step, whose
// CONFLICT_CAS_FAILED is a different predicate (step identity /
// already-in-progress) and is out of scope here:
//
//	transcript ad9fd0e8628a, 7 calls:            <- the one this file models
//	  get_work_item -> update_work_item -> update_work_item(!CONFLICT_CAS_FAILED) ->
//	  update_work_item -> claim_work_item -> complete_attempt -> get_work_item
//	transcript 5afbbac89438, 12 calls:  ... update_step(!CONFLICT_CAS_FAILED) -> get_step -> update_step ...
//	transcript a0a042eb8889, 21 calls:  ... update_step(!CONFLICT_CAS_FAILED) -> commit -> update_step ...
//
// error-taxonomy.md records the wire message, 3 calls' worth:
//
//	pf_update_work_item | 409 | CONFLICT_CAS_FAILED |
//	  declared_resources CAS failed: resources_version is <N>, not the expected <N> —
//	  reread the work item and retry with its current resources_version
//	  details={"current_resources_ver...
//
// Note the SHAPE of that trace: conflict, then one more update_work_item, then
// the flow carries on to a successful claim and wrap. The 409 is not a failure
// mode in the corpus — it is a step in a loop that closes. That is the thing
// worth testing, and it is the thing aihub#241's suite does not test.
//
// What aihub#241 already covers (internal/domain/work_items_cas_db_test.go), and
// which this file deliberately does not repeat: resources_version advances on
// the ordinary path, a stale version is a 409 and never a 400, the correct
// version still succeeds, and a rejected write does not advance the counter.
//
// What is left, and is here:
//
//  1. the 409 reports the CURRENT version, because a client retries FROM that
//     number and a conflict that only says "no" sends it into a blind loop;
//  2. the reread-and-retry loop actually closes, with the retry's payload
//     winning — the corpus trace end to end;
//  3. the losing write is discarded WHOLE, not merely as far as its version
//     counter. buildWorkItemUpdate compiles one UPDATE carrying every patched
//     column plus `AND resources_version = $n`, so a rejected CAS must also
//     discard the priority and milestone that rode along in the same call.
//     Nothing in the tree asserts that today, and a refactor that applied the
//     non-CAS columns in a second statement would keep every aihub#241 test
//     green while silently letting half of a rejected patch land.
//
// One assertion IS shared with aihub#241 on purpose: case 3 re-checks that the
// counter did not advance (work_items_cas_db_test.go:112 asserts the same
// thing). It stays because it is the baseline the two new claims in that test
// are measured against — "priority was discarded" means little if the version
// moved — and an arm that reads the row anyway costs nothing to assert it.
// Everything else here is new.
//
//	AIHUB_TEST_DB=postgres://postgres:testpass@localhost:15421/aihub_test?sslmode=disable \
//	go test ./internal/domain/ -run 'TestDeclaredResourcesCASRetry' -v -count=1

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The two payloads are DIFFERENT so "whose write landed" is answerable at all.
// declaredResourcesFixture (work_items_cas_db_test.go) is the winner's; these
// two belong to the loser and to the retry.
var (
	casLoserResources = json.RawMessage(`[{"type":"path","uri":"file:internal/loser.go","intent":"write"}]`)
	casRetryResources = json.RawMessage(`[{"type":"path","uri":"file:internal/retried.go","intent":"write"}]`)
)

// TestDeclaredResourcesCASRetry_ConflictReportsTheCurrentVersion pins the
// details payload, which is the only part of the 409 a client can act on.
//
// The message text carries the number too, but a client that has to regex its
// own retry value out of a prose sentence is one rewording away from breaking,
// so casConflictErr (work_items.go:2078) puts it in structured details as well.
// Both halves are asserted, against two DIFFERENT non-zero numbers — see the
// comment at the assertions for why that choice is what makes them
// discriminating, and how it subsumes the casVersionUnknown sentinel.
func TestDeclaredResourcesCASRetry_ConflictReportsTheCurrentVersion(t *testing.T) {
	pool := setupLatestTestDB(t)
	ctx := context.Background()
	u := testUser(t, pool)
	project := testProject(t, pool, u)
	wi := seedWIs(t, pool, project, u, 1)[0]

	// The winner's first write; two more follow below.
	_, aerr := UpdateWorkItem(ctx, pool, wi.ID, u, "admin", nil, &UpdateWorkItemRequest{
		DeclaredResources: declaredResourcesFixture,
	})
	require.Nil(t, aerr)

	// A DELIBERATELY NON-ZERO stale version. 0 would also conflict, but 0 is
	// int's zero value, so an `expected_resources_version` that the server never
	// filled in would compare equal to it and the echo assertion below would pass
	// on a missing field. Getting there needs two more writes first, which is the
	// only reason this test advances the row to 3.
	for i := 0; i < 2; i++ {
		_, aerr = UpdateWorkItem(ctx, pool, wi.ID, u, "admin", nil, &UpdateWorkItemRequest{
			DeclaredResources: declaredResourcesFixture,
		})
		require.Nil(t, aerr)
	}
	stale := 2
	_, aerr = UpdateWorkItem(ctx, pool, wi.ID, u, "admin", nil, &UpdateWorkItemRequest{
		DeclaredResources: casLoserResources,
		ResourcesVersion:  &stale,
	})
	require.NotNil(t, aerr, "a stale resources_version must conflict")
	require.Equal(t, ErrConflictCASFailed, aerr.Code)

	details, ok := aerr.Details.(map[string]any)
	require.True(t, ok,
		"the 409 must carry structured details: a caller that can only parse the prose sentence "+
			"breaks the next time the sentence is reworded, and %T is not parseable at all", aerr.Details)

	// current=3, expected=2: two DIFFERENT non-zero numbers, so neither
	// assertion can be satisfied by the other's value or by a zero value. That
	// also covers the casVersionUnknown sentinel (-1, rendered as "unknown" in
	// the message) without a separate assertion — a failed in-transaction
	// re-read would report -1 here and this Equal would fail. An explicit
	// NotEqual(-1) alongside Equal(3) could never fail on its own, and an
	// assertion that cannot fail is worse than none: it reads as a second guard
	// while adding nothing.
	assert.Equal(t, 3, details["current_resources_version"],
		"the conflict must report the version the row ACTUALLY holds — it is the value the retry "+
			"is built from, so a conflict that withholds it forces the caller into a blind loop")
	assert.Equal(t, 2, details["expected_resources_version"],
		"and it must echo what the caller believed, so a log line shows how far behind it was")
	assert.Contains(t, aerr.Message, "resources_version is 3, not the expected 2",
		"the human-readable half must agree with the details; two numbers that disagree are worse "+
			"than one number")
}

// TestDeclaredResourcesCASRetry_RereadAndRetryClosesTheLoop is the recovery arm:
// transcript ad9fd0e8628a's three consecutive update_work_item calls, in order,
// with the middle one failing.
//
// This is the arm that says the 409 is a step in a working protocol rather than
// a dead end. Note what it does NOT do: it does not retry with `stale+1`. It
// rereads. The version is advanced by Postgres itself
// (`resources_version = resources_version + 1`, work_items.go:1885), so a caller
// that guesses the next number is right only while it is the only writer — which
// is exactly the situation a CAS exists for the absence of.
func TestDeclaredResourcesCASRetry_RereadAndRetryClosesTheLoop(t *testing.T) {
	pool := setupLatestTestDB(t)
	ctx := context.Background()
	u := testUser(t, pool)
	project := testProject(t, pool, u)
	wi := seedWIs(t, pool, project, u, 1)[0]

	// Call 1 — the winner.
	_, aerr := UpdateWorkItem(ctx, pool, wi.ID, u, "admin", nil, &UpdateWorkItemRequest{
		DeclaredResources: declaredResourcesFixture,
	})
	require.Nil(t, aerr)

	// Call 2 — the loser, holding the version it read before call 1.
	stale := 0
	_, aerr = UpdateWorkItem(ctx, pool, wi.ID, u, "admin", nil, &UpdateWorkItemRequest{
		DeclaredResources: casRetryResources,
		ResourcesVersion:  &stale,
	})
	require.NotNil(t, aerr)
	require.Equal(t, ErrConflictCASFailed, aerr.Code)

	// Reread — the get_work_item the corpus trace opens with, done again.
	fresh, gerr := GetWorkItem(ctx, pool, wi.ID)
	require.Nil(t, gerr)
	require.Equal(t, 1, fresh.ResourcesVersion)

	// Call 3 — the retry, at the version just read.
	retried, aerr := UpdateWorkItem(ctx, pool, wi.ID, u, "admin", nil, &UpdateWorkItemRequest{
		DeclaredResources: casRetryResources,
		ResourcesVersion:  &fresh.ResourcesVersion,
	})
	require.Nil(t, aerr, "a retry at the reported version must succeed, or the loop never closes: %+v", aerr)
	assert.Equal(t, 2, retried.ResourcesVersion, "the successful retry advances the counter in turn")

	after, gerr := GetWorkItem(ctx, pool, wi.ID)
	require.Nil(t, gerr)
	assert.JSONEq(t, string(casRetryResources), string(after.DeclaredResources),
		"the RETRY's payload must be what is stored. Asserting only the version would pass on a "+
			"server that bumped the counter and kept the old resources — the counter is the guard, "+
			"not the thing being guarded")
}

// TestDeclaredResourcesCASRetry_LosingWriteIsDiscardedWholeNotJustItsVersion is
// the strong claim, and the one arm here with no existing analogue.
//
// The loser's call patches THREE things: declared_resources (CAS-guarded),
// priority and milestone (not). All three must vanish together. They do today
// because buildWorkItemUpdate emits a single
//
//	UPDATE work_items SET ... WHERE id = $n AND resources_version = $m
//
// so RowsAffected()==0 means nothing was written at all, and the deferred
// rollback (work_items.go:2434) covers the rest of the function.
//
// That is a property of the STATEMENT SHAPE, not of the CAS check, which is why
// it needs its own test: `isCASConflict` returning the right verdict is
// necessary and not sufficient. Split the patch across two statements, or move
// the version predicate into a separate guarding SELECT, and every aihub#241
// assertion still passes while a rejected caller's priority change quietly
// lands. A half-applied patch is worse than a rejected one, because the caller
// was told "no" and has no reason to go looking.
func TestDeclaredResourcesCASRetry_LosingWriteIsDiscardedWholeNotJustItsVersion(t *testing.T) {
	pool := setupLatestTestDB(t)
	ctx := context.Background()
	u := testUser(t, pool)
	project := testProject(t, pool, u)
	wi := seedWIs(t, pool, project, u, 1)[0]

	// Give the row a known non-default priority and milestone so "unchanged" is
	// a real observation rather than "still the zero value".
	const basePriority, baseMilestone = "low", "aihub421-base-milestone"
	seeded, aerr := UpdateWorkItem(ctx, pool, wi.ID, u, "admin", nil, &UpdateWorkItemRequest{
		Priority:  ptrTo(basePriority),
		Milestone: ptrTo(baseMilestone),
	})
	require.Nil(t, aerr)
	require.Equal(t, 0, seeded.ResourcesVersion,
		"a patch that does not touch declared_resources must not move the CAS counter, or the "+
			"stale version below would be stale for the wrong reason")

	// The winner takes the row to version 1.
	_, aerr = UpdateWorkItem(ctx, pool, wi.ID, u, "admin", nil, &UpdateWorkItemRequest{
		DeclaredResources: declaredResourcesFixture,
	})
	require.Nil(t, aerr)

	// The loser: a stale CAS plus two columns the CAS does not guard.
	stale := 0
	_, aerr = UpdateWorkItem(ctx, pool, wi.ID, u, "admin", nil, &UpdateWorkItemRequest{
		DeclaredResources: casLoserResources,
		ResourcesVersion:  &stale,
		Priority:          ptrTo("urgent"),
		Milestone:         ptrTo("aihub421-loser-milestone"),
	})
	require.NotNil(t, aerr)
	require.Equal(t, ErrConflictCASFailed, aerr.Code)

	after, gerr := GetWorkItem(ctx, pool, wi.ID)
	require.Nil(t, gerr)

	assert.Equal(t, 1, after.ResourcesVersion, "the rejected write must not advance the counter")
	assert.JSONEq(t, string(declaredResourcesFixture), string(after.DeclaredResources),
		"the WINNER's declared_resources must still be there")
	assert.Equal(t, basePriority, after.Priority,
		"priority is NOT covered by the CAS predicate, and that is exactly why it needs asserting: "+
			"it rode along in the rejected patch and must have been discarded with it. One UPDATE "+
			"with the version in its WHERE clause is what makes that true; two statements would not")
	require.NotNil(t, after.Milestone)
	assert.Equal(t, baseMilestone, *after.Milestone,
		"same for milestone — a rejected caller was told nothing landed, so nothing may have landed")
}

// ptrTo is a local pointer helper; the request struct takes *string for the
// fields above so that "not sent" and "set to empty" stay distinguishable.
func ptrTo[T any](v T) *T { return &v }
