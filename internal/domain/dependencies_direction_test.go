package domain

// DB-gated regression test for aihub#230: ListDependencies populated its
// `blocking` and `blocked_by` response lists from swapped SQL. Both the WHERE
// predicate and the SELECT projection column were inverted relative to the
// field being filled, so `blocking` returned "who blocks me" and `blocked_by`
// returned "who I block".
//
// No direction-explicit test existed before this one — that absence is why the
// inversion survived. Counting entries is not enough: with a single edge both
// the correct and the inverted implementation return exactly one entry, just on
// the wrong side of the response. This test therefore asserts the identity of
// the wi in each list AND that the opposite list is empty, which is the only
// shape that fails against the pre-fix SQL.
//
// It also asserts each entry's Slug, not just its ID, so that the projected
// column and the JOIN column cannot silently diverge: projecting blocked_wi_id
// while joining work_items on blocking_wi_id yields the correct ID paired with
// the wrong slug/project, and an ID-only assertion accepts it.
//
// Follows the AIHUB_TEST_DB gating pattern used across this package (see
// dependencies_requeue_test.go for the full rationale): SKIPS unless
// AIHUB_TEST_DB is set, and is run in CI by a dedicated step that applies
// migrations first and scopes itself with -run so it does not widen every other
// AIHUB_TEST_DB-gated test in the package.
//
//	AIHUB_TEST_DB=postgres://postgres:test@localhost:5440/aihub_test?sslmode=disable \
//	go test ./internal/domain/ -run TestListDependencies_Direction -v -count=1

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestListDependencies_Direction builds one unambiguous edge — A blocks B — and
// asserts that reading each end reports the direction that matches the field
// name:
//
//	read(A) -> blocking=[B], blocked_by=[]   (A blocks B)
//	read(B) -> blocked_by=[A], blocking=[]   (B is blocked by A)
//
// This matches the write-side semantics of CreateWorkItem(blocked_by=[...]) ==
// "these block me", which was already correct; only the read labels disagreed.
func TestListDependencies_Direction(t *testing.T) {
	pool := setupLatestTestDB(t)
	ctx := context.Background()

	userID := testUser(t, pool)
	project := testProject(t, pool, userID)

	wis := seedWIs(t, pool, project, userID, 2)
	blocker, blocked := wis[0], wis[1]

	// The stored edge: blocked_wi_id=blocked, blocking_wi_id=blocker.
	createBlocksDep(t, pool, blocked.ID, blocker.ID, userID)

	// Sanity-check the fixture itself, so a direction failure below cannot be
	// blamed on the seed data or on status derivation.
	require.Equal(t, "blocked", wiStatusOf(t, pool, blocked.ID),
		"fixture: the blocked wi should have derived status='blocked'")

	// callerRole=admin so cross-project masking never collapses id/slug here;
	// this test is about direction only. (The masking behavior is aihub#227.)
	roles := map[string]string{project: "owner"}

	// ── Read the BLOCKER end: it blocks one wi, nothing blocks it. ──
	fromBlocker, aerr := ListDependencies(ctx, pool, blocker.ID, roles, "admin")
	require.Nil(t, aerr)
	require.NotNil(t, fromBlocker)

	require.Len(t, fromBlocker.Blocking, 1,
		"blocker.blocking must contain exactly the wi it blocks; got %+v", fromBlocker.Blocking)
	assert.Equal(t, blocked.ID, fromBlocker.Blocking[0].ID,
		"blocker.blocking must list the BLOCKED wi (%s), not itself or the blocker", blocked.ID)
	// Assert the slug too, not just the ID: the JOIN column must be the SAME
	// column that is projected. A query that projects blocked_wi_id but joins
	// work_items on blocking_wi_id returns the right ID with the WRONG slug and
	// project, which every ID-only assertion happily accepts.
	if assert.NotNil(t, fromBlocker.Blocking[0].Slug, "slug must be populated for a same-project entry") {
		assert.Equal(t, blocked.Slug, *fromBlocker.Blocking[0].Slug,
			"blocker.blocking slug must describe the BLOCKED wi — a slug/ID mismatch means the JOIN column diverged from the projected column")
	}
	assert.Empty(t, fromBlocker.BlockedBy,
		"nothing blocks the blocker, so blocker.blocked_by must be empty; got %+v", fromBlocker.BlockedBy)

	// ── Read the BLOCKED end: it is blocked by one wi, it blocks nothing. ──
	fromBlocked, aerr := ListDependencies(ctx, pool, blocked.ID, roles, "admin")
	require.Nil(t, aerr)
	require.NotNil(t, fromBlocked)

	require.Len(t, fromBlocked.BlockedBy, 1,
		"blocked.blocked_by must contain exactly the wi blocking it; got %+v", fromBlocked.BlockedBy)
	assert.Equal(t, blocker.ID, fromBlocked.BlockedBy[0].ID,
		"blocked.blocked_by must list the BLOCKER wi (%s)", blocker.ID)
	if assert.NotNil(t, fromBlocked.BlockedBy[0].Slug, "slug must be populated for a same-project entry") {
		assert.Equal(t, blocker.Slug, *fromBlocked.BlockedBy[0].Slug,
			"blocked.blocked_by slug must describe the BLOCKER wi — a slug/ID mismatch means the JOIN column diverged from the projected column")
	}
	assert.Empty(t, fromBlocked.Blocking,
		"the blocked wi blocks nothing, so blocked.blocking must be empty; got %+v", fromBlocked.Blocking)

	// ── aihub#543 probe wave 2: the FOLDING half of pf_list_dependencies' card ──
	//
	// Subtests of this function rather than new top-level ones, deliberately: it
	// is already in internal/citest/dbtestcov/gated_tests.txt and already named
	// by ci.yml's "aihub#230 dependency-direction DB tests" step, whose grep is
	// on this function's own PASS line — so these run in CI with no manifest
	// line and no new workflow step (aihub#543 spec §3.3 rule 2).
	//
	// They are added AFTER every assertion above, and the far-project edge they
	// create is a second blocker on `blocked`, so nothing above can see it.
	//
	// Mutants, all applied to the tree and run (2026-09-10):
	//
	//	M107 the fold also strips the slug, as it did before aihub#377   RED
	//	M108 the fold drops the row instead of folding it                RED
	//	M110 the callerRole == "admin" short-circuit is removed          RED (the
	//	                                                                 admin
	//	                                                                 subtest,
	//	                                                                 and the
	//	                                                                 aihub#230
	//	                                                                 arms above
	//	                                                                 too, since
	//	                                                                 their
	//	                                                                 fixture
	//	                                                                 holds
	//	                                                                 "owner",
	//	                                                                 which the
	//	                                                                 ladder does
	//	                                                                 not rank)
	//	M111 green control: reword a comment in ListDependencies         GREEN
	t.Run("a_folded_row_withholds_only_the_id", func(t *testing.T) {
		far, farWI := seedFarProjectBlocker(t, pool, userID, blocked.ID, "fold")

		// The caller: writer on their own project, NOTHING on the far one.
		outsider := map[string]string{project: "writer"}
		got, aerr := ListDependencies(ctx, pool, blocked.ID, outsider, "writer")
		require.Nil(t, aerr)
		entry := entryFor(t, got.BlockedBy, farWI.ID, far)

		assert.False(t, entry.Accessible,
			"a caller with no role on %s must be told the far end is inaccessible, in the explicit "+
				"boolean rather than through a sentinel they have to know about", far)
		assert.Equal(t, "hidden", entry.ID,
			"the canonical id is the ONE field withheld; got %+v", entry)
		if assert.NotNil(t, entry.Slug, "the slug is returned unconditionally (aihub#377 invariant 2): "+
			"somebody in the caller's own project wrote this reference into their work item, so "+
			"hiding it makes the owner unable to read their own dependency graph") {
			assert.Equal(t, farWI.Slug, *entry.Slug)
		}
		assert.Equal(t, far, entry.Project, "the far end's project is disclosed too")
		assert.Equal(t, "blocks", entry.Kind, "the edge kind is not a permissioned field")
		if assert.NotNil(t, entry.Note, "the note is returned unconditionally as well") {
			assert.Equal(t, farNote, *entry.Note)
		}

		// The positive control: viewer+ on the far project and the SAME row comes
		// back whole. Without it, "withholds only the id" is satisfied by a
		// function that withholds the id from everybody.
		insider := map[string]string{project: "writer", far: "viewer"}
		got2, aerr := ListDependencies(ctx, pool, blocked.ID, insider, "writer")
		require.Nil(t, aerr)
		entry2 := entryFor(t, got2.BlockedBy, farWI.ID, far)
		assert.True(t, entry2.Accessible)
		assert.Equal(t, farWI.ID, entry2.ID,
			"a viewer on the far project must be handed the canonical id; if not, the fold above is "+
				"unconditional and this arm measures nothing")

		// And the count is honest in both cases — the row is folded, never dropped.
		assert.Equal(t, len(got.BlockedBy), len(got2.BlockedBy),
			"the folded read returned %d entries and the permitted read %d; the row is what makes the "+
				"count honest, so a fold that drops it changes what the caller believes about their "+
				"own work item", len(got.BlockedBy), len(got2.BlockedBy))
	})

	t.Run("an_admin_short_circuits_the_ladder", func(t *testing.T) {
		far, farWI := seedFarProjectBlocker(t, pool, userID, blocked.ID, "admin")

		// callerRole=admin with an EMPTY project-roles map: admins have no member
		// rows by design, which is why the global role is consulted BEFORE the
		// per-project ladder (aihub#227). This is the exemption a later "admins
		// see everything" change would quietly remove.
		got, aerr := ListDependencies(ctx, pool, blocked.ID, map[string]string{}, "admin")
		require.Nil(t, aerr)
		entry := entryFor(t, got.BlockedBy, farWI.ID, far)
		assert.True(t, entry.Accessible,
			"an admin holds no member row anywhere, so gating on the ladder alone folds every "+
				"cross-project row for them")
		assert.Equal(t, farWI.ID, entry.ID)

		// The discriminating control: the same empty map with a non-admin global
		// role folds the row, so the arm above is about the SHORT-CIRCUIT rather
		// than about an empty map being permissive.
		got2, aerr := ListDependencies(ctx, pool, blocked.ID, map[string]string{}, "writer")
		require.Nil(t, aerr)
		entry2 := entryFor(t, got2.BlockedBy, farWI.ID, far)
		assert.False(t, entry2.Accessible,
			"a non-admin with the same empty roles map was ALSO given the far end, so the admin "+
				"assertion above passes for a reason that has nothing to do with being an admin")
		assert.Equal(t, "hidden", entry2.ID)
	})
}

// farNote is the note the cross-project edge carries, so the folding subtest can
// assert a note survives the fold rather than only that the field exists.
const farNote = "the far-project blocker this edge was declared against"

// seedFarProjectBlocker creates a second project with one work item and makes it
// block the caller's `blocked` wi, returning the project name and the wi.
//
// A second project rather than a second role on the same one: the fold is keyed
// on the FAR END's project, so a same-project fixture cannot reach it at all.
func seedFarProjectBlocker(t *testing.T, pool *pgxpool.Pool, userID, blockedWIID, suffix string) (string, *WorkItem) {
	t.Helper()
	// A SHORT deterministic name: projects.name is CHECKed against
	// ^[a-z][a-z0-9_-]{0,39}$, and a name derived from t.Name() inside a subtest
	// runs past 40 characters and is refused by the database rather than by
	// anything a reader would look at.
	far := "p_dep543_far_" + suffix
	mustExec(t, pool, `INSERT INTO projects(name,owner_user_id) VALUES('`+far+`','`+userID+`') ON CONFLICT (name) DO NOTHING`)
	// resetTestProject rather than a bare DELETE on work_items: CreateWorkItem
	// leaves an agent_events row, whose FK is checked before the cascade from
	// wi_dependencies — so a hand-rolled delete works on the first run of a
	// fresh database and fails on the second, which is a fixture failure that
	// reads exactly like a red assertion. Measured on 2026-09-10 while running
	// this arm's own mutants.
	resetTestProject(t, pool, far)

	wi, aerr := CreateWorkItem(context.Background(), pool, &CreateWorkItemRequest{
		Project: far,
		Goal:    "retune the shard rebalancer for the analytics replica",
		Source:  "human",
	}, userID, userID, nil, "")
	require.Nil(t, aerr, "seeding the far-project blocker must succeed; got %+v", aerr)

	note := farNote
	mustExec(t, pool, `
		INSERT INTO wi_dependencies (blocked_wi_id, blocking_wi_id, kind, created_by, note)
		VALUES ('`+blockedWIID+`', '`+wi.ID+`', 'blocks', '`+userID+`', '`+note+`')
		ON CONFLICT (blocked_wi_id, blocking_wi_id, kind) DO NOTHING`)
	return far, wi
}

// entryFor picks the entry naming a work item out of a response list, by its
// project and slug rather than by its id — the id is exactly the field the fold
// replaces, so matching on it would find nothing in the folded case.
func entryFor(t *testing.T, entries []DependencyListEntry, wantID, wantProject string) DependencyListEntry {
	t.Helper()
	var hits []DependencyListEntry
	for _, e := range entries {
		if e.Project == wantProject {
			hits = append(hits, e)
		}
	}
	require.Len(t, hits, 1,
		"want exactly one entry from project %s (the far end %s) in %+v; zero means the fixture's "+
			"edge is missing and every assertion below would be about an empty struct",
		wantProject, wantID, entries)
	return hits[0]
}
