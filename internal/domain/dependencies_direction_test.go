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
}
