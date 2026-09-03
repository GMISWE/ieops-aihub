package domain

// DB-gated integration tests for aihub#333: UpdateProject refuses a `members`
// write that would take access away from somebody the request did not name.
//
// The set arithmetic is pure and covered without a database in
// projects_members_removal_test.go. What can only be verified against a real
// Postgres is everything around it: that the stored list is read under the row
// lock the UPDATE is about to use, that a refusal writes NOTHING (not the
// members, not the counter), that a declared removal still lands, and that when
// both aihub#260's compare-and-set and this precondition would fire, the caller
// is told about the stale version rather than about a removal set computed
// against a list they never read.
//
// Follows the AIHUB_TEST_DB gating pattern of projects_members_cas_db_test.go:
// setupLatestTestDB SKIPs unless AIHUB_TEST_DB is set, and these run in their
// own scoped CI step rather than by setting that variable on "Unit tests".
//
//	AIHUB_TEST_DB=postgres://postgres:testpass@localhost:5440/aihub_test?sslmode=disable \
//	go test ./internal/domain/ -run 'TestUpdateProjectMembersRemoval' -v -count=1
//
// Requires migration 0032_projects_members_version.sql. aihub#333 adds no
// migration of its own: expected_removals is a request-only precondition and is
// never stored, so there is no column for it and nothing to sequence a deploy
// around.

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// removalSeed is the three-member list these tests shrink from.
func removalSeed() []MemberInput {
	return []MemberInput{
		{UserID: "u_one", Role: "viewer"},
		{UserID: "u_two", Role: "writer"},
		{UserID: "u_three", Role: "maintainer"},
	}
}

// ── half one: an UNINTENDED shrink fails, and writes nothing ────────────────
//
// This is the call aihub#260's characterization test asserted would SUCCEED. The
// caller holds the CURRENT version, so the compare-and-set passes; the refusal
// can only come from aihub#333.
func TestUpdateProjectMembersRemovalUndeclaredShrinkIsRefusedAndWritesNothing(t *testing.T) {
	pool := setupLatestTestDB(t)
	u := testUser(t, pool)
	project := casProject(t, pool, u)
	caller := &UserRecord{ID: u, Role: "admin"}
	ctx := context.Background()

	full := removalSeed()
	p1, aerr := UpdateProject(ctx, pool, project, caller, UpdateProjectRequest{Members: &full})
	require.Nil(t, aerr)
	require.Len(t, casMembersOf(t, p1.Members), 3)

	current := p1.MembersVersion
	truncated := []MemberInput{{UserID: "u_one", Role: "viewer"}}
	_, aerr = UpdateProject(ctx, pool, project, caller,
		UpdateProjectRequest{Members: &truncated, MembersVersion: &current})
	require.NotNil(t, aerr, "a list short by two was accepted while its members_version matched. "+
		"A version counter cannot tell \"I mean to remove two\" from \"I lost two\", which is why the "+
		"removal set has to be checked separately")
	assert.Equal(t, ErrProjectMembersUndeclaredRemoval, aerr.Code)
	assert.Equal(t, 412, aerr.HTTPStatus,
		"412 and not 409: a client that loops on CONFLICT_CAS_FAILED would re-send the same short list "+
			"forever, so this must not be reported as a retryable conflict")

	details, ok := aerr.Details.(map[string]any)
	require.True(t, ok, "the refusal carries no details, so a caller cannot see who it was about to remove")
	assert.Equal(t, []string{"u_three", "u_two"}, details["undeclared_removals"])
	assert.Equal(t, false, details["retryable"],
		"re-sending this request unchanged fails again forever; a retry policy must be able to learn that "+
			"without pattern-matching the message")
	assert.Contains(t, aerr.Message, "u_two", "the names have to be in the message too — details are what a "+
		"program reads, the message is what the operator sees")

	// Nothing written. A guard that fires after the UPDATE is not a guard.
	fresh, gerr := GetProject(ctx, pool, project, caller, "")
	require.Nil(t, gerr)
	assert.Equal(t, []string{"u_one", "u_two", "u_three"}, casMembersOf(t, fresh.Members))
	assert.Equal(t, current, fresh.MembersVersion,
		"the refused write advanced members_version, so it reached the UPDATE")
}

// ── half two: an INTENDED shrink succeeds ───────────────────────────────────
//
// Without this the test above is satisfied by a server that refuses every
// members write. That is not a fix, it is an outage: `members` is live
// access-control data and removing somebody is routine.
func TestUpdateProjectMembersRemovalDeclaredShrinkSucceedsAndAdvancesVersion(t *testing.T) {
	pool := setupLatestTestDB(t)
	u := testUser(t, pool)
	project := casProject(t, pool, u)
	caller := &UserRecord{ID: u, Role: "admin"}
	ctx := context.Background()

	full := removalSeed()
	p1, aerr := UpdateProject(ctx, pool, project, caller, UpdateProjectRequest{Members: &full})
	require.Nil(t, aerr)

	current := p1.MembersVersion
	kept := []MemberInput{{UserID: "u_one", Role: "viewer"}}
	p2, aerr := UpdateProject(ctx, pool, project, caller, UpdateProjectRequest{
		Members:          &kept,
		MembersVersion:   &current,
		ExpectedRemovals: []string{"u_two", "u_three"},
	})
	require.Nil(t, aerr, "a shrink declaring exactly the two user_ids it removes was refused (%v)", aerr)
	assert.Equal(t, []string{"u_one"}, casMembersOf(t, p2.Members))
	assert.Equal(t, current+1, p2.MembersVersion,
		"a successful declared shrink must still advance the counter, or the next writer's token is stale")
}

// ── the work item's own acceptance criterion ────────────────────────────────
//
// aihub#333 asks for a test in which the caller adds one person WITHOUT knowing
// the current member set, and the others do not disappear. That caller sends the
// only member they know about, which is both an addition and a two-member
// removal — and it is refused, so the others survive. The refusal is the
// mechanism: there is no merge here, and adding one silently is exactly what
// cannot be distinguished from truncating.
func TestUpdateProjectMembersRemovalCallerWhoDoesNotKnowTheListCannotWipeIt(t *testing.T) {
	pool := setupLatestTestDB(t)
	u := testUser(t, pool)
	project := casProject(t, pool, u)
	caller := &UserRecord{ID: u, Role: "admin"}
	ctx := context.Background()

	full := removalSeed()
	_, aerr := UpdateProject(ctx, pool, project, caller, UpdateProjectRequest{Members: &full})
	require.Nil(t, aerr)

	// No members_version either: this caller has not read the project at all.
	newcomer := []MemberInput{{UserID: "u_newcomer", Role: "writer"}}
	_, aerr = UpdateProject(ctx, pool, project, caller, UpdateProjectRequest{Members: &newcomer})
	require.NotNil(t, aerr, "a caller who had never read the member list replaced all three of them with "+
		"one newcomer and was told nothing")
	assert.Equal(t, ErrProjectMembersUndeclaredRemoval, aerr.Code)

	fresh, gerr := GetProject(ctx, pool, project, caller, "")
	require.Nil(t, gerr)
	assert.Equal(t, []string{"u_one", "u_two", "u_three"}, casMembersOf(t, fresh.Members),
		"the incumbents did not survive a write from somebody who did not know they existed")

	// And the way through is to read first and send everybody — which needs no
	// declaration, because it removes nobody. This is the half that keeps the
	// refusal from being a dead end.
	together := append(removalSeed(), MemberInput{UserID: "u_newcomer", Role: "writer"})
	p, aerr := UpdateProject(ctx, pool, project, caller, UpdateProjectRequest{Members: &together})
	require.Nil(t, aerr, "reading the list and sending it back with the addition was refused (%v); the "+
		"additive path must stay open with no new parameter", aerr)
	assert.Equal(t, []string{"u_one", "u_two", "u_three", "u_newcomer"}, casMembersOf(t, p.Members))
}

// ── ordering: the compare-and-set answers first ─────────────────────────────
//
// When the caller's members_version is stale AND their list would remove
// somebody, the stale version is what they are told about. Their whole view of
// the list is out of date, so a removal set computed against a list they never
// read would name people for reasons they cannot act on, and the right next step
// is the reread that CONFLICT_CAS_FAILED asks for.
//
// This also pins that the compare-and-set is still the WHERE predicate and not a
// Go comparison: nothing in the aihub#333 path returns a CAS error.
func TestUpdateProjectMembersRemovalStaleVersionConflictTakesPrecedence(t *testing.T) {
	pool := setupLatestTestDB(t)
	u := testUser(t, pool)
	project := casProject(t, pool, u)
	caller := &UserRecord{ID: u, Role: "admin"}
	ctx := context.Background()

	full := removalSeed()
	p1, aerr := UpdateProject(ctx, pool, project, caller, UpdateProjectRequest{Members: &full})
	require.Nil(t, aerr)

	stale := p1.MembersVersion - 1
	truncated := []MemberInput{{UserID: "u_one", Role: "viewer"}}
	_, aerr = UpdateProject(ctx, pool, project, caller,
		UpdateProjectRequest{Members: &truncated, MembersVersion: &stale})
	require.NotNil(t, aerr)
	assert.Equal(t, ErrConflictCASFailed, aerr.Code,
		"a request that is BOTH racing and truncating was answered with the truncation. The caller's view "+
			"is stale, so the removal set is computed against a list they never saw; telling them to reread "+
			"(409) is actionable, telling them which of a list they do not have would be removed is not")

	fresh, gerr := GetProject(ctx, pool, project, caller, "")
	require.Nil(t, gerr)
	assert.Equal(t, []string{"u_one", "u_two", "u_three"}, casMembersOf(t, fresh.Members))
}

// ── malformed stored data must stay REPAIRABLE through the API ──────────────
//
// Nothing constrains projects.members to be an array of well-formed member
// objects, and this codebase already has a documented policy for the case:
// internal/server/middleware.go's warnMalformedMembersOnce applies the elements
// that do decode, warns once, and tells the operator to "fix the row in
// projects.members". A members write is the only way to follow that advice
// through the API, so decoding the stored list must not be able to refuse one.
//
// Found in review of aihub#333's first cut, which unmarshalled the locked list
// strictly and answered 500 INTERNAL_ERROR — turning the one supported repair
// path into a dead end reachable only by raw SQL. Measured on the pre-fix
// commit: `[{"user_id":"u_a","role":"viewer"},"oops"]` gave
// `INTERNAL_ERROR: decode stored members: json: cannot unmarshal string into Go
// value of type domain.projectMember`, where base 6555a15 had repaired the row.
func TestUpdateProjectMembersRemovalMalformedStoredListStaysRepairable(t *testing.T) {
	pool := setupLatestTestDB(t)
	u := testUser(t, pool)
	project := casProject(t, pool, u)
	caller := &UserRecord{ID: u, Role: "admin"}
	ctx := context.Background()

	// Raw SQL: this row cannot be produced through the API, which is the point.
	_, err := pool.Exec(ctx,
		`UPDATE projects SET members='[{"user_id":"u_a","role":"viewer"},"oops",{"user_id":"u_b","role":"writer"}]'::jsonb
		 WHERE name=$1`, project)
	require.NoError(t, err)

	// Sending everybody who is really there removes nobody, so it needs no
	// declaration and must simply succeed.
	repaired := []MemberInput{{UserID: "u_a", Role: "viewer"}, {UserID: "u_b", Role: "writer"}}
	p, aerr := UpdateProject(ctx, pool, project, caller, UpdateProjectRequest{Members: &repaired})
	require.Nil(t, aerr, "a members write against a row with one malformed element was refused (%v). "+
		"warnMalformedMembersOnce tells operators to fix such a row in projects.members, and a members "+
		"write is the only way to do that through the API", aerr)
	assert.Equal(t, []string{"u_a", "u_b"}, casMembersOf(t, p.Members))

	// And the elements that DO decode are still protected: dropping u_b from a
	// malformed row is still an undeclared removal, so tolerating the junk must
	// not tolerate the truncation next to it.
	_, err = pool.Exec(ctx,
		`UPDATE projects SET members='[{"user_id":"u_a","role":"viewer"},"oops",{"user_id":"u_b","role":"writer"}]'::jsonb
		 WHERE name=$1`, project)
	require.NoError(t, err)
	short := []MemberInput{{UserID: "u_a", Role: "viewer"}}
	_, aerr = UpdateProject(ctx, pool, project, caller, UpdateProjectRequest{Members: &short})
	require.NotNil(t, aerr, "one malformed element disabled the removal check for the well-formed ones")
	assert.Equal(t, ErrProjectMembersUndeclaredRemoval, aerr.Code)
	details, ok := aerr.Details.(map[string]any)
	require.True(t, ok)
	assert.Equal(t, []string{"u_b"}, details["undeclared_removals"])
}

// ── a member entry that names nobody must not become a sticky refusal ───────
//
// Two halves of one defect found in review.
//
// Input: UpdateProject validated `role` and never `user_id`, so
// `[{"user_id":"","role":"viewer"}]` was accepted and stored. It grants access to
// nobody, and afterwards every members write was refused with a message whose
// list of names read literally "did not declare: ." — the escape hatch
// (expected_removals: [""]) is not guessable from that, so the project's member
// list was stuck.
//
// Stored: rejecting the input cannot help a row that already has one, or one
// written by raw SQL or a future migration. So a stored entry with a blank
// user_id is also not treated as a removable member: it names nobody, and the
// check exists to protect ACCESS, which a blank user_id does not grant.
func TestUpdateProjectMembersRemovalBlankUserIDIsRejectedAndNeverSticks(t *testing.T) {
	pool := setupLatestTestDB(t)
	u := testUser(t, pool)
	project := casProject(t, pool, u)
	caller := &UserRecord{ID: u, Role: "admin"}
	ctx := context.Background()

	for _, blank := range []string{"", "   "} {
		bad := []MemberInput{{UserID: "u_a", Role: "viewer"}, {UserID: blank, Role: "viewer"}}
		_, aerr := UpdateProject(ctx, pool, project, caller, UpdateProjectRequest{Members: &bad})
		require.NotNil(t, aerr, "a member entry with user_id %q was accepted; it grants access to nobody "+
			"and makes every later members write fail with a refusal that names nobody", blank)
		assert.Equal(t, ErrBadRequest, aerr.Code)
		assert.Contains(t, aerr.Message, "user_id")
	}

	// A row that already carries one (only reachable by raw SQL now) must not
	// block a legitimate write.
	_, err := pool.Exec(ctx,
		`UPDATE projects SET members='[{"user_id":"u_a","role":"viewer"},{"user_id":"","role":"viewer"}]'::jsonb
		 WHERE name=$1`, project)
	require.NoError(t, err)
	fixed := []MemberInput{{UserID: "u_a", Role: "viewer"}}
	p, aerr := UpdateProject(ctx, pool, project, caller, UpdateProjectRequest{Members: &fixed})
	require.Nil(t, aerr, "a stored member entry with a blank user_id blocked a write that removes nobody "+
		"real (%v); the project's member list would be unfixable through the API", aerr)
	assert.Equal(t, []string{"u_a"}, casMembersOf(t, p.Members))
}

// ── the reference side of TestUpdateProjectMembersCASConcurrentAddsBothSurvive ──
//
// Same forced interleaving as the guarded acceptance test, no members_version.
// Until aihub#333 this was ...ConcurrentAddsWithoutTheGuardLoseOne: one addition
// was silently discarded and nobody was told, which was the price of leaving
// aihub#260's guard opt-in. It is no longer payable — the loser's list omits the
// member the winner just added, and that is an undeclared removal — so the
// unguarded read-modify-write now REFUSES the loser instead of losing it.
//
// Worth stating plainly because it narrows what members_version is uniquely for:
// after aihub#333 an unguarded concurrent ADD cannot lose anybody. What still
// needs the version is a concurrent change that removes nobody — somebody else
// re-grading a member's role, or re-adding one you just removed — where there is
// no removal for this check to see.
//
// It is also the alarm on the guarded test: if the window stopped opening, both
// writers would succeed and the assertion below goes red, saying that
// "both survive" has stopped measuring a race.
func TestUpdateProjectMembersRemovalUnguardedConcurrentAddsRefuseTheLoserInsteadOfLosingIt(t *testing.T) {
	pool := setupLatestTestDB(t)
	u := testUser(t, pool)
	project := casProject(t, pool, u)
	caller := &UserRecord{ID: u, Role: "admin"}
	ctx := context.Background()

	incumbent := []MemberInput{{UserID: "u_incumbent", Role: "viewer"}}
	_, aerr := UpdateProject(ctx, pool, project, caller, UpdateProjectRequest{Members: &incumbent})
	require.Nil(t, aerr)

	var (
		conflicts int64
		mu        sync.Mutex
		wg        sync.WaitGroup
	)
	barrier := newCASBarrier(2)
	errs := make([]error, 2)
	for i, who := range []string{"u_alice", "u_bob"} {
		wg.Add(1)
		go func(idx int, member string) {
			defer wg.Done()
			errs[idx] = casAddMember(t, pool, project, caller, member, false, barrier, &conflicts, &mu)
		}(i, who)
	}
	barrier.releaseWhenAllArrived(t, 2)

	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(60 * time.Second):
		t.Fatal("the unguarded add-member workers did not finish within 60s")
	}

	var refused int
	for _, err := range errs {
		if err == nil {
			continue
		}
		refused++
		assert.Contains(t, err.Error(), string(ErrProjectMembersUndeclaredRemoval),
			"the loser failed for some reason other than an undeclared removal: %v", err)
	}
	assert.Equal(t, 1, refused,
		"exactly one of two forced-overlapping unguarded writers must be refused. 0 means the interleaving "+
			"stopped producing a race (and TestUpdateProjectMembersCASConcurrentAddsBothSurvive has stopped "+
			"measuring one); 2 means the winner's own write was refused too")

	mu.Lock()
	seen := conflicts
	mu.Unlock()
	assert.Equal(t, int64(0), seen, "an update carrying no members_version must never CAS-conflict; the "+
		"refusal above has to come from the removal check, not from a version nobody sent")

	// The decisive assertion: whoever won, NOBODY was dropped. Before aihub#333
	// the list here was also length 2 — but by having silently overwritten the
	// other writer's addition, and the surviving pair could be either
	// {incumbent, alice} or {incumbent, bob} with no record of the loss.
	final, gerr := GetProject(ctx, pool, project, caller, "")
	require.Nil(t, gerr)
	got := casMembersOf(t, final.Members)
	assert.Contains(t, got, "u_incumbent", "the incumbent was dropped by a write that only meant to add")
	assert.Len(t, got, 2, "members = %v; one add must land and the other must be refused, so the incumbent "+
		"plus exactly one newcomer", got)
	assert.Equal(t, 2, final.MembersVersion,
		"members_version = %d after 1 seed + 1 successful add + 1 refused write, want 2: a refused write "+
			"must not have advanced anything", final.MembersVersion)
}
