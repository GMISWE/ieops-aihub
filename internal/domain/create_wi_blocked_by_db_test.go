package domain

// DB-gated acceptance test for aihub#357: creating a dependency edge must leave
// a MACHINE-READABLE record of it, not just a status change.
//
// 🔴 Read this before changing the assertions — the work item that produced this
// file reported something the code does not do, and the difference is the point.
//
// aihub#357 was filed as "pf_create_work_item's blocked_by sets status=blocked
// but creates no dependency edge at all". Measured against a from-zero database
// while fixing it, that is FALSE: CreateWorkItem's blocked_by loop has inserted
// wi_dependencies rows in the same transaction since the first HTTP-API commit,
// and the blocking_wi_id foreign key makes the reported state unreachable — an
// id that does not exist rolls the whole create back, so there is no way to end
// up with a blocked work item and zero edges. Two separate things produced that
// reading:
//
//  1. The READ lied. pf_list_dependencies was called with a SLUG, and
//     handleListDependencies resolved id-or-slug for its access check and then
//     handed domain.ListDependencies the caller's raw parameter, which is
//     compared against wi_dependencies.blocked_wi_id — a column that
//     FK-references work_items(id) and therefore only ever holds `wi_...`. The
//     endpoint answered 200 with an empty list, which is indistinguishable from
//     a work item that genuinely has no dependencies. That half is covered in
//     internal/server/dependencies_slug_db_test.go, at the hop where it happens.
//
//  2. No event was emitted, by EITHER writer. That half is real, and it is what
//     this file guards. `pf_read_events` on the blocked work item showed only
//     work_item_filed, so "what is blocking this" existed nowhere a machine
//     could read it — exactly the impact aihub#357 describes.
//
// So the assertions below are deliberately asymmetric. The edge-count subtest is
// a CONTROL: it passes before the fix and exists to prove the fixture reaches
// the code under test, so that a red event assertion cannot be dismissed as a
// broken seed. The event subtests are the acceptance criteria and are RED on the
// unfixed tree.
//
// Follows the AIHUB_TEST_DB gating pattern used across this package: SKIPS
// unless AIHUB_TEST_DB is set, and CI runs it from a dedicated step that applies
// migrations first and scopes itself with -run.
//
//	AIHUB_TEST_DB=postgres://postgres:testpass@localhost:5432/aihub_test?sslmode=disable \
//	go test ./internal/domain/ -run TestBlockedByIsMachineReadable -v -count=1
//
// One test FUNCTION with subtests rather than five functions, following
// aihub#334: internal/citest/dbtestcov ratchets on the number of DB-gated test
// functions, and five functions would move that ratchet five times for one
// guard. The per-arm coverage claim lives in the CI step's `--- PASS:` greps.

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// depEvent is one decoded dependency_created row off a work item's timeline.
type depEvent struct {
	BlockingWIID string `json:"blocking_wi_id"`
	Kind         string `json:"kind"`
	Via          string `json:"via"`
}

// dependencyCreatedEventsOn reads every dependency_created event recorded
// against wiID, oldest first.
//
// Scoped by work_item_id rather than by project on purpose: the events must land
// on the BLOCKED work item, because that is the timeline someone reads when they
// ask "why is this blocked". An event carrying the right payload on the wrong
// work item would satisfy a project-wide query and answer nobody's question.
func dependencyCreatedEventsOn(t *testing.T, pool *pgxpool.Pool, wiID string) []depEvent {
	t.Helper()
	rows, err := pool.Query(context.Background(), `
		SELECT payload::text FROM agent_events
		WHERE work_item_id = $1 AND event_type = 'dependency_created'
		ORDER BY created_at, id`, wiID)
	require.NoError(t, err)
	defer rows.Close()

	out := []depEvent{}
	for rows.Next() {
		var raw string
		require.NoError(t, rows.Scan(&raw))
		var ev depEvent
		require.NoError(t, json.Unmarshal([]byte(raw), &ev),
			"dependency_created payload must be a JSON object the timeline can read: %s", raw)
		out = append(out, ev)
	}
	require.NoError(t, rows.Err())
	return out
}

// blockingIDsOf projects the blocker ids out of a slice of events so an
// assertion can compare sets without depending on insertion order.
func blockingIDsOf(evs []depEvent) []string {
	out := make([]string, 0, len(evs))
	for _, e := range evs {
		out = append(out, e.BlockingWIID)
	}
	return out
}

func TestBlockedByIsMachineReadable(t *testing.T) {
	pool := setupLatestTestDB(t)
	ctx := context.Background()

	u := testUser(t, pool)
	project := testProject(t, pool, u)

	// seedWIs resets the project first, so everything below is seeded after it.
	blockers := seedWIs(t, pool, project, u, 3)
	b0, b1, b2 := blockers[0], blockers[1], blockers[2]

	// The goal must stay dissimilar from every seeded goal: CreateWorkItem runs
	// goal-similarity dedup (checkDedup) against live wis in the same project and
	// rejects a close match before reaching any of the code under test.
	blocked, aerr := CreateWorkItem(ctx, pool, &CreateWorkItemRequest{
		Project:   project,
		Goal:      "index the quarterly telemetry archive into cold storage",
		Source:    "human",
		BlockedBy: []string{b0.ID, b1.ID},
	}, u, u)
	require.Nil(t, aerr, "creating the blocked wi must succeed; got %+v", aerr)
	require.Equal(t, "blocked", blocked.Status,
		"a wi created with a non-empty blocked_by must come back status=blocked")

	// ── CONTROL. Green before the fix, on purpose. ────────────────────────────
	// aihub#357 reports that no edge is written. It is written, and this subtest
	// is what lets the red subtests below mean "the event is missing" instead of
	// "the fixture never ran".
	t.Run("control_blocked_by_writes_one_edge_per_blocker", func(t *testing.T) {
		var got []string
		rows, err := pool.Query(ctx,
			`SELECT blocking_wi_id FROM wi_dependencies WHERE blocked_wi_id=$1 AND kind='blocks'`, blocked.ID)
		require.NoError(t, err)
		defer rows.Close()
		for rows.Next() {
			var id string
			require.NoError(t, rows.Scan(&id))
			got = append(got, id)
		}
		require.NoError(t, rows.Err())
		assert.ElementsMatch(t, []string{b0.ID, b1.ID}, got,
			"blocked_by must write exactly one 'blocks' edge per entry")
	})

	// ── ACCEPTANCE. Red before the fix. ───────────────────────────────────────
	t.Run("blocked_by_emits_one_dependency_created_event_per_blocker", func(t *testing.T) {
		evs := dependencyCreatedEventsOn(t, pool, blocked.ID)
		require.Len(t, evs, 2,
			"blocked_by must record one dependency_created event per blocker on the BLOCKED wi's timeline, "+
				"so `pf_read_events` alone answers what is blocking it; got %+v", evs)
		assert.ElementsMatch(t, []string{b0.ID, b1.ID}, blockingIDsOf(evs),
			"each event must NAME its blocker — a bare count records that something blocks this wi "+
				"without recording what, which is the state aihub#357 was filed about")
		for _, e := range evs {
			assert.Equal(t, "blocks", e.Kind, "the edge kind must be on the event")
			assert.Equal(t, "create_work_item.blocked_by", e.Via,
				"the event must name the writer, so a reader can tell an edge declared at creation "+
					"from one added later by pf_create_dependency")
		}
	})

	// The other writer of the same table must leave the same record, or "what is
	// blocking this" stays unanswerable for every edge that did not come from a
	// blocked_by at creation time — the same impact, one code path over.
	t.Run("create_dependency_emits_the_same_event", func(t *testing.T) {
		before := len(dependencyCreatedEventsOn(t, pool, blocked.ID))

		aerr := CreateDependency(ctx, pool, &CreateDependencyRequest{
			BlockedWIID:  blocked.ID,
			BlockingWIID: b2.ID,
			Kind:         "blocks",
		}, u, map[string]string{project: "owner"}, "admin")
		require.Nil(t, aerr, "CreateDependency must succeed; got %+v", aerr)

		evs := dependencyCreatedEventsOn(t, pool, blocked.ID)
		require.Len(t, evs, before+1,
			"pf_create_dependency's writer must record a dependency_created event too; got %+v", evs)

		last := evs[len(evs)-1]
		assert.Equal(t, b2.ID, last.BlockingWIID, "the new event must name the blocker just added")
		assert.Equal(t, "blocks", last.Kind)
		assert.Equal(t, "create_dependency", last.Via,
			"this writer must identify itself distinctly from create_work_item.blocked_by")
	})

	// Negative control for the arm above: the insert is ON CONFLICT DO NOTHING,
	// so a repeated create is a no-op on the table and must be a no-op on the
	// timeline too. Without this, "emit an event" is satisfiable by emitting one
	// unconditionally, which would turn every retry into a fake second blocker.
	t.Run("recreating_an_existing_edge_emits_no_second_event", func(t *testing.T) {
		before := dependencyCreatedEventsOn(t, pool, blocked.ID)

		aerr := CreateDependency(ctx, pool, &CreateDependencyRequest{
			BlockedWIID:  blocked.ID,
			BlockingWIID: b2.ID,
			Kind:         "blocks",
		}, u, map[string]string{project: "owner"}, "admin")
		require.Nil(t, aerr, "re-creating an existing dependency must stay idempotent; got %+v", aerr)

		after := dependencyCreatedEventsOn(t, pool, blocked.ID)
		assert.Len(t, after, len(before),
			"a duplicate create added no row, so it must add no event either; got %+v", after)
	})
}
