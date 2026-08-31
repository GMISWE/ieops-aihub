package domain

// Behavioural coverage for GetReadyQueue's items[] segment (aihub#280 B3).
//
// Why this file exists, stated precisely, because the gap it closes was hidden
// by a comment claiming otherwise:
//
// aihub#280 made `?ready_only=true` and this segment share one SQL constant,
// readyOnlyPredicate, and added a test that inspected buildReadyQueueItemsQuery
// to "prove" the sharing. It proved nothing about GetReadyQueue. Nothing forces
// GetReadyQueue to call that helper, so inlining a divergent query at the call
// site — dropping requires_human_session and the blocker NOT EXISTS — left every
// aihub#280 test green, including the one named for the property. Verified by
// mutation, not assumed.
//
// Before this file there was no DB test of GetReadyQueue's items[] contents at
// all: `grep -rn GetReadyQueue --include=*_test.go` found two hits, neither
// asserting the returned set. So the segment that decides what work an agent is
// handed had its behaviour asserted nowhere.
//
// These tests call GetReadyQueue itself. A divergent inline query at the call
// site fails them regardless of what any helper returns.
//
// DB-gated; wired into the "aihub#280 list work_items param contract DB tests"
// step in .github/workflows/ci.yml, which greps for each PASS by name and
// rejects a SKIP — `go test` prints "ok" and exits 0 when everything skips.

import (
	"context"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

// readyQueueTestPool opens the AIHUB_TEST_DB pool, skipping when unset — the
// same gating idiom as the rest of the DB-backed domain suites.
func readyQueueTestPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("AIHUB_TEST_DB")
	if dsn == "" {
		t.Skip("AIHUB_TEST_DB not set; skipping DB-backed test")
	}
	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

// seedReadyQueueFixture builds a project whose every work item is queued, so the
// ONLY thing that can exclude one from items[] is a condition under test. If the
// fixture varied status as well, a predicate that dropped requires_human_session
// could still look correct.
//
// Idempotent: it clears the project first. The two non-idempotent DB tests in
// this package (TestResumeOwnLocks_*) fail on a re-run for exactly the lack of
// this, and pass fine on a virgin database.
func seedReadyQueueFixture(t *testing.T, pool *pgxpool.Pool) (project string, ids map[string]string) {
	t.Helper()
	ctx := context.Background()
	// sanitizeTestName (memory_latest_test.go) lowercases, strips subtest
	// slashes, and truncates to fit projects.name's CHECK
	// (^[a-z][a-z0-9_-]{0,39}$) — a raw t.Name() violates it.
	project = "p_" + sanitizeTestName(t.Name())
	uid := "u_readyq_test"

	for _, q := range []string{
		`DELETE FROM wi_dependencies WHERE blocked_wi_id IN (SELECT id FROM work_items WHERE project=$1)
		    OR blocking_wi_id IN (SELECT id FROM work_items WHERE project=$1)`,
		`DELETE FROM wi_step_state WHERE work_item_id IN (SELECT id FROM work_items WHERE project=$1)`,
		`UPDATE work_items SET current_attempt_id=NULL WHERE project=$1`,
		`DELETE FROM run_attempts WHERE work_item_id IN (SELECT id FROM work_items WHERE project=$1)`,
		`DELETE FROM agent_events WHERE work_item_id IN (SELECT id FROM work_items WHERE project=$1)`,
		`DELETE FROM memories WHERE work_item_id IN (SELECT id FROM work_items WHERE project=$1)`,
		`DELETE FROM work_items WHERE project=$1`,
	} {
		if _, err := pool.Exec(ctx, q, project); err != nil {
			t.Fatalf("cleanup %q: %v", q, err)
		}
	}
	if _, err := pool.Exec(ctx,
		`INSERT INTO users(id,email,display_name,role) VALUES($1,$1||'@test.local',$1,'writer')
		 ON CONFLICT (id) DO NOTHING`, uid); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	if _, err := pool.Exec(ctx,
		`INSERT INTO projects(name,owner_user_id) VALUES($1,$2) ON CONFLICT (name) DO NOTHING`,
		project, uid); err != nil {
		t.Fatalf("seed project: %v", err)
	}

	ids = map[string]string{}
	// All four are queued. Only the condition named by the key may exclude one.
	rows := []struct {
		key           string
		requiresHuman any
	}{
		{"plain", false},      // must appear
		{"alsoplain", false},  // must appear — proves the segment is not empty
		{"needshuman", true},  // excluded by requires_human_session
		{"blocked", false},    // excluded by a live blocker, wired below
		{"blocker", false},    // the blocker itself: queued and unblocked
		{"unclassified", nil}, // requires_human_session IS NULL -> its own segment
	}
	for i, r := range rows {
		id := NewID("wi")
		ids[r.key] = id
		if _, err := pool.Exec(ctx, `
			INSERT INTO work_items (id, seq, project, scenario, goal, source, wi_type, priority,
				requires_human_session, labels, status, declared_resources,
				reporter_user_id, reporter_display, attrs)
			VALUES ($1,$2,$3,'coding',$4,'human','chore','normal',$5,'{}','queued','[]',$6,$6,'{}')`,
			id, 9100+i, project, "ready queue fixture "+r.key, r.requiresHuman, uid); err != nil {
			t.Fatalf("seed wi %s: %v", r.key, err)
		}
	}
	// A live 'blocks' dependency: blocker is queued, so it is NOT terminal.
	if _, err := pool.Exec(ctx,
		`INSERT INTO wi_dependencies (blocked_wi_id, blocking_wi_id, kind, created_by)
		 VALUES ($1,$2,'blocks',$3)`, ids["blocked"], ids["blocker"], uid); err != nil {
		t.Fatalf("seed dependency: %v", err)
	}
	return project, ids
}

// The guard. Calls GetReadyQueue and asserts the contents of items[].
func TestGetReadyQueue_ItemsExcludesHumanSessionAndBlocked(t *testing.T) {
	pool := readyQueueTestPool(t)
	project, ids := seedReadyQueueFixture(t, pool)

	rq, aerr := GetReadyQueue(context.Background(), pool, project, 100)
	if aerr != nil {
		t.Fatalf("GetReadyQueue: %v", aerr)
	}
	got := map[string]bool{}
	for _, it := range rq.Items {
		got[it.ID] = true
	}

	// Positive control first: without it, a predicate that returns nothing at
	// all would satisfy every exclusion assertion below.
	for _, key := range []string{"plain", "alsoplain", "blocker"} {
		if !got[ids[key]] {
			t.Errorf("items[] is missing %q, which is queued, needs no human, and has no blocker — "+
				"the segment must not be empty for the exclusions below to mean anything", key)
		}
	}
	// The two exclusions. Each is a separate condition of readyOnlyPredicate, so
	// dropping either one is caught individually rather than only in aggregate.
	if got[ids["needshuman"]] {
		t.Errorf("items[] contains the requires_human_session wi — the " +
			"requires_human_session = false condition is not being applied")
	}
	if got[ids["blocked"]] {
		t.Errorf("items[] contains a wi with a live 'blocks' dependency — the " +
			"blocker NOT EXISTS condition is not being applied")
	}
	// requires_human_session IS NULL is neither true nor false in SQL, so it
	// belongs to unclassified[], not items[].
	if got[ids["unclassified"]] {
		t.Errorf("items[] contains the requires_human_session IS NULL wi; that belongs to unclassified[]")
	}
	if len(rq.Items) != 3 {
		t.Errorf("items[] should hold exactly the 3 ready wis; got %d", len(rq.Items))
	}
}

// The property aihub#280 actually claims: `?ready_only=true` asks the ready
// queue's question. Asserted on what both code paths RETURN, not on what a
// helper says they would return — that is the assertion that mutation showed to
// be vacuous.
func TestGetReadyQueue_ItemsAgreesWithReadyOnlyFilter(t *testing.T) {
	pool := readyQueueTestPool(t)
	project, _ := seedReadyQueueFixture(t, pool)
	ctx := context.Background()

	rq, aerr := GetReadyQueue(ctx, pool, project, 100)
	if aerr != nil {
		t.Fatalf("GetReadyQueue: %v", aerr)
	}
	queue := map[string]bool{}
	for _, it := range rq.Items {
		queue[it.ID] = true
	}

	res, aerr := ListWorkItems(ctx, pool, project, ListWorkItemsFilter{ReadyOnly: true, Limit: 100})
	if aerr != nil {
		t.Fatalf("ListWorkItems: %v", aerr)
	}
	filter := map[string]bool{}
	for _, wi := range res.Items {
		filter[wi.ID] = true
	}

	if len(queue) == 0 {
		t.Fatal("the ready queue returned nothing; two empty sets would agree trivially")
	}
	for id := range queue {
		if !filter[id] {
			t.Errorf("%s is in the ready queue's items[] but not in ready_only=true", id)
		}
	}
	for id := range filter {
		if !queue[id] {
			t.Errorf("%s is in ready_only=true but not in the ready queue's items[]", id)
		}
	}
}
