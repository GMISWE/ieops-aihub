package domain

// DB-gated integration tests for aihub#260: UpdateProject's members
// compare-and-set.
//
// buildProjectUpdate is a pure function with ordinary, non-DB-gated coverage in
// projects_members_cas_test.go. What can only be verified against a real
// Postgres is that UpdateProject executes the statement it compiled, that the
// counter Postgres maintains really does advance, and that a WHERE predicate
// which matches nothing surfaces as a 409 CONFLICT_CAS_FAILED carrying the
// version to retry with — never a 500 and never a silent 200.
//
// Follows the AIHUB_TEST_DB gating pattern of memory_latest_test.go /
// work_items_cas_db_test.go: setupLatestTestDB SKIPs unless AIHUB_TEST_DB is
// set. That variable is deliberately NOT set on CI's "Unit tests" step, because
// setting it there would also switch on every other AIHUB_TEST_DB-gated suite
// in this package. These run in their own scoped CI step instead.
//
//	AIHUB_TEST_DB=postgres://postgres:testpass@localhost:5440/aihub_test?sslmode=disable \
//	go test ./internal/domain/ -run 'TestUpdateProjectMembersCAS' -v -count=1
//
// Requires migration 0032_projects_members_version.sql.

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// casProject seeds this test's project and then clears BOTH the member list and
// the counter.
//
// testProject's resetTestProject clears work items, attempts and memories, but
// nothing that lives in a projects column — and every version number asserted
// in this file is absolute, so without this the second run against a given
// database inherits the first run's counter and every test here fails. That is
// not hypothetical: it was measured on the second run of this suite before this
// helper existed (6 of 8 red). Same class of residue the resetTestProject doc
// comment describes.
//
// Raw SQL rather than UpdateProject: a fixture must not be built out of the
// function under test, or a bug in that function repairs the evidence of itself.
func casProject(t *testing.T, pool *pgxpool.Pool, ownerUserID string) string {
	t.Helper()
	project := testProject(t, pool, ownerUserID)
	_, err := pool.Exec(context.Background(),
		`UPDATE projects SET members='[]'::jsonb, members_version=0 WHERE name=$1`, project)
	require.NoError(t, err)
	return project
}

// casMembersOf decodes a project's members list into user ids.
func casMembersOf(t *testing.T, raw json.RawMessage) []string {
	t.Helper()
	var ms []MemberInput
	require.NoError(t, json.Unmarshal(raw, &ms))
	out := make([]string, 0, len(ms))
	for _, m := range ms {
		out = append(out, m.UserID)
	}
	return out
}

// TestUpdateProjectMembersCASVersionAdvancesAcrossWrites is the aihub#241
// failure-mode-1 regression carried over to projects: two consecutive
// UpdateProject calls that each write members and pass NO members_version (the
// ordinary path — exactly the scenario that used to leave work_items'
// equivalent counter pinned at 0 forever) must advance 0 -> 1 -> 2.
func TestUpdateProjectMembersCASVersionAdvancesAcrossWrites(t *testing.T) {
	pool := setupLatestTestDB(t)
	u := testUser(t, pool)
	project := casProject(t, pool, u)
	caller := &UserRecord{ID: u, Role: "admin"}
	ctx := context.Background()

	p0, aerr := GetProject(ctx, pool, project, caller, "")
	require.Nil(t, aerr)
	require.Equal(t, 0, p0.MembersVersion, "precondition: a freshly seeded project starts at members_version=0")

	first := []MemberInput{{UserID: "u_one", Role: "viewer"}}
	p1, aerr := UpdateProject(ctx, pool, project, caller, UpdateProjectRequest{Members: &first})
	require.Nil(t, aerr)
	assert.Equal(t, 1, p1.MembersVersion, "the first members write must advance the version 0 -> 1")

	second := []MemberInput{{UserID: "u_one", Role: "viewer"}, {UserID: "u_two", Role: "writer"}}
	p2, aerr := UpdateProject(ctx, pool, project, caller, UpdateProjectRequest{Members: &second})
	require.Nil(t, aerr)
	assert.Equal(t, 2, p2.MembersVersion, "the second members write must advance the version 1 -> 2")
}

// An update that does not touch members must leave the counter alone. This is
// the property that makes a dedicated column better than a compare-and-set on
// updated_at, which trg_projects_updated_at moves on every write.
func TestUpdateProjectMembersCASUnrelatedWriteLeavesVersionAlone(t *testing.T) {
	pool := setupLatestTestDB(t)
	u := testUser(t, pool)
	project := casProject(t, pool, u)
	caller := &UserRecord{ID: u, Role: "admin"}
	ctx := context.Background()

	members := []MemberInput{{UserID: "u_one", Role: "viewer"}}
	p1, aerr := UpdateProject(ctx, pool, project, caller, UpdateProjectRequest{Members: &members})
	require.Nil(t, aerr)
	require.Equal(t, 1, p1.MembersVersion)

	desc := "an edit that has nothing to do with membership"
	p2, aerr := UpdateProject(ctx, pool, project, caller, UpdateProjectRequest{Description: &desc})
	require.Nil(t, aerr)
	assert.Equal(t, 1, p2.MembersVersion,
		"a description-only update advanced members_version; a guard somebody is legitimately holding would now 409")
	assert.True(t, p2.UpdatedAt.After(p1.UpdatedAt) || p2.UpdatedAt.Equal(p1.UpdatedAt),
		"sanity: updated_at is the thing that DOES move, which is why it cannot be the CAS token")

	// The guard taken before that edit must still be usable.
	v := 1
	p3, aerr := UpdateProject(ctx, pool, project, caller, UpdateProjectRequest{Members: &members, MembersVersion: &v})
	require.Nil(t, aerr, "a members CAS held across an unrelated edit must still succeed")
	assert.Equal(t, 2, p3.MembersVersion)
}

// A stale members_version must be rejected as a 409 CONFLICT_CAS_FAILED that
// reports the current version, and must write nothing.
func TestUpdateProjectMembersCASStaleVersionReturns409AndReportsCurrent(t *testing.T) {
	pool := setupLatestTestDB(t)
	u := testUser(t, pool)
	project := casProject(t, pool, u)
	caller := &UserRecord{ID: u, Role: "admin"}
	ctx := context.Background()

	kept := []MemberInput{{UserID: "u_keep", Role: "viewer"}}
	p1, aerr := UpdateProject(ctx, pool, project, caller, UpdateProjectRequest{Members: &kept})
	require.Nil(t, aerr)
	require.Equal(t, 1, p1.MembersVersion)

	stale := 0
	clobber := []MemberInput{{UserID: "u_clobber", Role: "writer"}}
	_, aerr = UpdateProject(ctx, pool, project, caller,
		UpdateProjectRequest{Members: &clobber, MembersVersion: &stale})
	require.NotNil(t, aerr, "a stale members_version must be rejected")
	assert.Equal(t, 409, aerr.HTTPStatus, "a stale version is a conflict, not a 400 bad request")
	assert.Equal(t, ErrConflictCASFailed, aerr.Code)

	details, ok := aerr.Details.(map[string]any)
	require.True(t, ok, "the conflict must carry details the caller can retry from; got %#v", aerr.Details)
	assert.Equal(t, 1, details["current_members_version"],
		"the conflict must report the CURRENT version, or the caller has to re-read to retry")
	assert.Equal(t, 0, details["expected_members_version"])

	// Nothing may have been written: it is one statement, so a failed
	// precondition must leave both the list and the counter untouched.
	fresh, gerr := GetProject(ctx, pool, project, caller, "")
	require.Nil(t, gerr)
	assert.Equal(t, []string{"u_keep"}, casMembersOf(t, fresh.Members),
		"the rejected write still replaced the member list")
	assert.Equal(t, 1, fresh.MembersVersion, "a rejected CAS write must not advance the counter")
}

// The mutation guard for the test above: the predicate must actually MATCH when
// given the current version. Without this, a WHERE clause that never matches
// anything would make the 409 test pass for entirely the wrong reason.
func TestUpdateProjectMembersCASCorrectVersionSucceedsAndAdvances(t *testing.T) {
	pool := setupLatestTestDB(t)
	u := testUser(t, pool)
	project := casProject(t, pool, u)
	caller := &UserRecord{ID: u, Role: "admin"}
	ctx := context.Background()

	first := []MemberInput{{UserID: "u_one", Role: "viewer"}}
	p1, aerr := UpdateProject(ctx, pool, project, caller, UpdateProjectRequest{Members: &first})
	require.Nil(t, aerr)
	require.Equal(t, 1, p1.MembersVersion)

	current := 1
	second := []MemberInput{{UserID: "u_one", Role: "viewer"}, {UserID: "u_two", Role: "writer"}}
	p2, aerr := UpdateProject(ctx, pool, project, caller,
		UpdateProjectRequest{Members: &second, MembersVersion: &current})
	require.Nil(t, aerr, "a CAS write carrying the CURRENT members_version must succeed")
	assert.Equal(t, 2, p2.MembersVersion, "a successful CAS write must still advance the counter")
	assert.Equal(t, []string{"u_one", "u_two"}, casMembersOf(t, p2.Members))
}

// Omitting the version must keep the historical unconditional overwrite. Every
// caller that exists today sends no version, and turning the guard on by default
// would break all of them.
func TestUpdateProjectMembersCASOmittedVersionOverwritesUnconditionally(t *testing.T) {
	pool := setupLatestTestDB(t)
	u := testUser(t, pool)
	project := casProject(t, pool, u)
	caller := &UserRecord{ID: u, Role: "admin"}
	ctx := context.Background()

	first := []MemberInput{{UserID: "u_one", Role: "viewer"}}
	_, aerr := UpdateProject(ctx, pool, project, caller, UpdateProjectRequest{Members: &first})
	require.Nil(t, aerr)
	// Advance the version well past anything a caller could be holding.
	for i := 0; i < 3; i++ {
		_, aerr = UpdateProject(ctx, pool, project, caller, UpdateProjectRequest{Members: &first})
		require.Nil(t, aerr)
	}

	// The swap declares the removal it performs. aihub#333 refuses a members
	// write that drops somebody the request did not name, and `[u_one]` ->
	// `[u_other]` drops u_one even though the list is the same LENGTH. Declaring
	// it keeps this test measuring what it is named for — that omitting
	// members_version never produces a CONFLICT_CAS_FAILED — instead of failing
	// on an unrelated precondition. The two guards are independent: this request
	// carries no version, and expected_removals is not one.
	other := []MemberInput{{UserID: "u_other", Role: "writer"}}
	p, aerr := UpdateProject(ctx, pool, project, caller,
		UpdateProjectRequest{Members: &other, ExpectedRemovals: []string{"u_one"}})
	require.Nil(t, aerr, "an update with no members_version must never conflict")
	assert.Equal(t, []string{"u_other"}, casMembersOf(t, p.Members))
	assert.Equal(t, 5, p.MembersVersion)
}

// casAddMember performs one read-modify-write "add this member", optionally
// guarded, retrying on conflict. barrier, when non-nil, is signalled after the
// READ of the first attempt and waited on before that attempt's write, so two
// callers can be forced to overlap deterministically instead of hoping.
func casAddMember(t *testing.T, pool *pgxpool.Pool, project string, caller *UserRecord,
	newMember string, guarded bool, barrier *casBarrier, conflicts *int64, mu *sync.Mutex) error {
	t.Helper()
	ctx := context.Background()
	for attempt := 0; attempt < 6; attempt++ {
		p, gerr := GetProject(ctx, pool, project, caller, "")
		if gerr != nil {
			if barrier != nil && attempt == 0 {
				barrier.arrive()
			}
			return fmt.Errorf("read: %v", gerr)
		}
		var current []MemberInput
		if err := json.Unmarshal(p.Members, &current); err != nil {
			if barrier != nil && attempt == 0 {
				barrier.arrive()
			}
			return fmt.Errorf("decode members: %w", err)
		}
		next := make([]MemberInput, len(current), len(current)+1)
		copy(next, current)
		next = append(next, MemberInput{UserID: newMember, Role: "writer"})

		req := UpdateProjectRequest{Members: &next}
		if guarded {
			v := p.MembersVersion
			req.MembersVersion = &v
		}

		if barrier != nil && attempt == 0 {
			barrier.arrive()
			barrier.wait()
		}

		_, uerr := UpdateProject(ctx, pool, project, caller, req)
		if uerr == nil {
			return nil
		}
		if uerr.Code != ErrConflictCASFailed {
			return fmt.Errorf("write: %v", uerr)
		}
		mu.Lock()
		*conflicts++
		mu.Unlock()
	}
	return fmt.Errorf("gave up after 6 attempts, all of them conflicts")
}

// casBarrier releases everyone once n participants have arrived, or gives up.
// Every wait has a deadline: a test that HANGS gets read as a broken harness,
// so this must fail instead.
type casBarrier struct {
	arrived chan struct{}
	release chan struct{}
}

func newCASBarrier(n int) *casBarrier {
	return &casBarrier{arrived: make(chan struct{}, n), release: make(chan struct{})}
}
func (b *casBarrier) arrive() { b.arrived <- struct{}{} }
func (b *casBarrier) wait() {
	select {
	case <-b.release:
	case <-time.After(20 * time.Second):
	}
}

// releaseWhenAllArrived blocks until n participants have arrived (or the
// deadline passes) and then opens the gate.
func (b *casBarrier) releaseWhenAllArrived(t *testing.T, n int) {
	t.Helper()
	for i := 0; i < n; i++ {
		select {
		case <-b.arrived:
		case <-time.After(20 * time.Second):
			close(b.release)
			t.Fatalf("only %d of %d workers reached the barrier within 20s", i, n)
		}
	}
	close(b.release)
}

// TestUpdateProjectMembersCASConcurrentAddsBothSurvive is the work item's own
// acceptance criterion: two admins adding a different person at the same time,
// both people must end up in the list.
//
// The two workers are forced to overlap — each reads, waits at a barrier for
// the other to have read, and only then writes — so exactly one of them is
// guaranteed to be writing on a version that has already moved. That is what
// the assertion on `conflicts` is for: without it a run in which the two
// happened not to overlap would pass while measuring nothing at all.
//
// The reference side of the same experiment is
// TestUpdateProjectMembersRemovalUnguardedConcurrentAddsRefuseTheLoserInsteadOfLosingIt
// (projects_members_removal_db_test.go): identical interleaving, no version, and
// the loser is REFUSED. Until aihub#333 that test was
// ...ConcurrentAddsWithoutTheGuardLoseOne and a member was silently lost; the
// pairing is unchanged, only the loser's fate is. If the window ever stopped
// opening, that test goes red and tells us this one has stopped being a race.
func TestUpdateProjectMembersCASConcurrentAddsBothSurvive(t *testing.T) {
	pool := setupLatestTestDB(t)
	u := testUser(t, pool)
	project := casProject(t, pool, u)
	caller := &UserRecord{ID: u, Role: "admin"}
	ctx := context.Background()

	incumbent := []MemberInput{{UserID: "u_incumbent", Role: "viewer"}}
	seeded, aerr := UpdateProject(ctx, pool, project, caller, UpdateProjectRequest{Members: &incumbent})
	require.Nil(t, aerr)
	require.Equal(t, 1, seeded.MembersVersion)

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
			errs[idx] = casAddMember(t, pool, project, caller, member, true, barrier, &conflicts, &mu)
		}(i, who)
	}
	barrier.releaseWhenAllArrived(t, 2)

	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(60 * time.Second):
		t.Fatal("the concurrent add-member workers did not finish within 60s")
	}

	for i, err := range errs {
		require.NoError(t, err, "worker %d failed", i)
	}

	final, gerr := GetProject(ctx, pool, project, caller, "")
	require.Nil(t, gerr)
	got := casMembersOf(t, final.Members)
	assert.ElementsMatch(t, []string{"u_incumbent", "u_alice", "u_bob"}, got,
		"both concurrent additions must survive, and the incumbent with them; got %v", got)

	mu.Lock()
	seen := conflicts
	mu.Unlock()
	assert.GreaterOrEqual(t, seen, int64(1),
		"no writer ever hit CONFLICT_CAS_FAILED, so the two never actually overlapped and this test "+
			"proved nothing about the guard")

	// One seed write plus exactly two successful adds. A rejected attempt must
	// not have advanced anything, so anything above 3 means a failed CAS still
	// wrote.
	assert.Equal(t, 3, final.MembersVersion,
		"members_version = %d after 1 seed + 2 successful adds; a conflicting attempt must write nothing",
		final.MembersVersion)
}

// The unguarded path's own invariant: the counter counts WRITES, and Postgres is
// what counts them.
//
// This was TestUpdateProjectMembersCASConcurrentAddsWithoutTheGuardLoseOne, the
// characterisation of what omitting members_version still cost you — same
// interleaving, two DIFFERENT people added, one addition silently lost and
// nobody told. aihub#333 made that unreachable: the loser's list omits the
// member the winner just added, which is now an undeclared removal, so it is
// refused instead of overwriting. What replaced it is
// TestUpdateProjectMembersRemovalUnguardedConcurrentAddsRefuseTheLoserInsteadOfLosingIt
// (projects_members_removal_db_test.go), and the loss is no longer expressible
// here.
//
// The assertion below is why this test still exists rather than being deleted
// with the characterisation. It kills a counter computed in Go from
// UpdateProject's PRE-transaction read: both writers would derive the same next
// value and the counter would land on 2, leaving a third party's stale token
// looking valid. Found by mutation testing — every other assertion in this file
// survived that mutant, because the WHERE predicate keeps the GUARDED path
// correct even with a Go-computed increment, so the unguarded path is the only
// place the difference is observable.
//
// Observing it needs both writers to have READ before either WROTE, and that is
// exactly what the barrier enforces: releaseWhenAllArrived t.Fatals if both
// workers do not reach it, and each worker arrives after its read and blocks
// until released. So the overlap is structural here, not an outcome this test
// has to detect afterwards. Both workers add the SAME member so that neither
// list drops anybody and both writes are therefore allowed to land — which is
// what makes two increments observable at all.
func TestUpdateProjectMembersCASUnguardedConcurrentWritesEachAdvanceTheCounter(t *testing.T) {
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
	for i, who := range []string{"u_same", "u_same"} {
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
	for i, err := range errs {
		require.NoError(t, err, "worker %d failed. An unguarded write that removes nobody must never "+
			"error: it carries no members_version so it cannot conflict, and it drops no member so "+
			"aihub#333 has nothing to refuse", i)
	}

	final, gerr := GetProject(ctx, pool, project, caller, "")
	require.Nil(t, gerr)
	assert.Equal(t, []string{"u_incumbent", "u_same"}, casMembersOf(t, final.Members))

	mu.Lock()
	seen := conflicts
	mu.Unlock()
	assert.Equal(t, int64(0), seen, "an update carrying no members_version must never conflict")

	assert.Equal(t, 3, final.MembersVersion,
		"members_version = %d after 1 seed + 2 unguarded concurrent writes, want 3: the counter must be "+
			"incremented by Postgres from the stored value, not computed from a value some caller read",
		final.MembersVersion)
}
