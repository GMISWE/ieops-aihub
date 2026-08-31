package domain

// Integration tests for the memories.latest_id cursor (aihub#201).
// Follows the AIHUB_TEST_DB gating pattern from memory_vector_integration_test.go:
// skipped unless AIHUB_TEST_DB is set, so it never runs in plain `go test ./...`.
//
//	AIHUB_TEST_DB=postgres://postgres:test@localhost:5440/aihub_test?sslmode=disable \
//	go test ./internal/domain/ -run TestBackfillLatestID -v -count=1

import (
	"context"
	"fmt"
	"os"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// setupLatestTestDB connects to AIHUB_TEST_DB, skipping the test if unset.
func setupLatestTestDB(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dbURL := os.Getenv("AIHUB_TEST_DB")
	if dbURL == "" {
		t.Skip("set AIHUB_TEST_DB to run this integration test")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dbURL)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

// testUser seeds (idempotently) a user + project for the given test and returns
// the user id. Each test uses a unique project name (derived from t.Name()) so
// concurrent/sequential tests sharing one DB don't collide.
func testUser(t *testing.T, pool *pgxpool.Pool) string {
	t.Helper()
	uid := "u_" + sanitizeTestName(t.Name())
	mustExec(t, pool, `INSERT INTO users(id,email,display_name) VALUES('`+uid+`','`+uid+`@test.local','`+uid+`') ON CONFLICT (id) DO NOTHING`)
	return uid
}

// testProject returns a project name unique to this test and seeds it
// (idempotently), owned by the given user id, after clearing out everything a
// previous run of the same test left in that project.
//
// The project name is DERIVED FROM t.Name(), so it is the same string on every
// run against the same database — the isolation it buys is between tests, never
// between runs. That makes the reset below load-bearing rather than tidy:
// without it, run N+1 of a test collides with run N's own residue.
//
// Measured during aihub#303 (whole suite run twice against one database, before
// this change): exactly two failures, TestResumeOwnLocks_NoSelfConflict and
// TestResumeOwnLocks_DifferentWIStillConflicts. They claim a work item whose
// declared path maps to a file_scope lock keyed by this project name, and the
// previous run's attempt was still holding it, so the first claim 409'd with
// CONFLICT_LOCK_TAKEN. Everything else survived only because it either cleans
// up itself (seedWI in step_pause_stall_test.go does its own child-to-parent
// delete for the same reason) or touches nothing but `memories`, which is all
// this reset used to clear. Re-run the suite twice if you want to re-measure;
// do not read "two" as a property of the code.
func testProject(t *testing.T, pool *pgxpool.Pool, ownerUserID string) string {
	t.Helper()
	proj := "p_" + sanitizeTestName(t.Name())
	mustExec(t, pool, `INSERT INTO projects(name,owner_user_id) VALUES('`+proj+`','`+ownerUserID+`') ON CONFLICT (name) DO NOTHING`)
	resetTestProject(t, pool, proj)
	return proj
}

// resetTestProject deletes the rows that would block re-seeding this test
// project, in an order that satisfies every foreign key pointing at work_items
// and run_attempts. Tables that cascade from work_items (wi_step_state,
// wi_dependencies) are left to the database.
//
// resource_locks must go first and explicitly: its FK to run_attempts is ON
// DELETE RESTRICT, so a leftover lock does not just linger, it blocks the
// cleanup of the attempt that owns it.
//
// The two self-referential FKs here (work_items.parent_work_item_id,
// run_attempts.parent_attempt_id) are NO ACTION, which Postgres checks at end
// of statement — so deleting a parent and its child in one statement is legal,
// and the `memories` delete is deliberately ONE statement covering both
// predicates for the same reason (memories.latest_id / supersedes_id are
// unqualified self-FKs, and splitting the delete would checkpoint mid-lineage).
//
// proj comes from sanitizeTestName, which emits only [a-z0-9_], so splicing it
// into the literals below (matching the style of the rest of this file) cannot
// break out of the quotes.
func resetTestProject(t *testing.T, pool *pgxpool.Pool, proj string) {
	t.Helper()
	wis := `(SELECT id FROM work_items WHERE project='` + proj + `')`
	attempts := `(SELECT id FROM run_attempts WHERE work_item_id IN ` + wis + `)`
	for _, stmt := range []string{
		`DELETE FROM resource_locks WHERE owner_attempt_id IN ` + attempts,
		// agent_events rows for memory/system events carry neither
		// work_item_id nor run_attempt_id (chk_evt_work_item_id permits NULL
		// for those types), so scope by project as well or they accumulate.
		`DELETE FROM agent_events WHERE project='` + proj + `' OR run_attempt_id IN ` + attempts + ` OR work_item_id IN ` + wis,
		`DELETE FROM wi_step_completions WHERE work_item_id IN ` + wis + ` OR run_attempt_id IN ` + attempts,
		`DELETE FROM memories WHERE project='` + proj + `' OR work_item_id IN ` + wis,
		`DELETE FROM run_attempts WHERE work_item_id IN ` + wis,
		`DELETE FROM work_items WHERE project='` + proj + `'`,
	} {
		mustExec(t, pool, stmt)
	}
}

// sanitizeTestName lowercases and strips characters that are unsafe to splice
// directly into a SQL identifier/literal (test names can contain '/' from
// subtests), and truncates to fit projects.name's 40-char limit alongside a
// "p_" prefix.
func sanitizeTestName(name string) string {
	out := make([]byte, 0, len(name))
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			out = append(out, byte(r))
		case r >= 'A' && r <= 'Z':
			out = append(out, byte(r-'A'+'a'))
		default:
			out = append(out, '_')
		}
	}
	if len(out) > 37 {
		out = out[:37]
	}
	return string(out)
}

// seedMemory inserts a minimal memory row directly (bypassing Remember) for
// backfill testing, with an explicit id/supersedes_id/status. Used only by
// TestBackfillLatestID to construct a pre-migration-shaped lineage.
func seedMemory(t *testing.T, pool *pgxpool.Pool, project, userID, id, supersedes, status string) {
	t.Helper()
	var supersedesArg any
	if supersedes != "" {
		supersedesArg = supersedes
	}
	_, err := pool.Exec(context.Background(), `
		INSERT INTO memories (id, project, type, content, author_user_id, author_display,
			visibility, status, supersedes_id, tags, attrs)
		VALUES ($1, $2, 'fact.note', $1, $3, $3, 'project', $4, $5, '{}', '{}')
		ON CONFLICT (id) DO NOTHING`,
		id, project, userID, status, supersedesArg,
	)
	require.NoError(t, err)
}

// runMigration applies a single goose-formatted migration file's Up section
// against pool. It extracts the SQL between "-- +goose Up" and "-- +goose Down"
// and executes it verbatim (multi-statement).
func runMigration(t *testing.T, pool *pgxpool.Pool, filename string) {
	t.Helper()
	raw, err := os.ReadFile("../db/migrations/" + filename)
	require.NoError(t, err)
	sql := string(raw)
	const upMarker = "-- +goose Up"
	const downMarker = "-- +goose Down"
	upStart := indexAfter(sql, upMarker)
	downStart := indexOf(sql, downMarker)
	require.NotEqual(t, -1, upStart, "missing +goose Up marker in %s", filename)
	require.NotEqual(t, -1, downStart, "missing +goose Down marker in %s", filename)
	upSQL := sql[upStart:downStart]
	_, err = pool.Exec(context.Background(), upSQL)
	require.NoError(t, err, "applying %s", filename)
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

func indexAfter(s, sub string) int {
	i := indexOf(s, sub)
	if i == -1 {
		return -1
	}
	return i + len(sub)
}

// latestOf returns the latest_id column value for the given memory id.
func latestOf(t *testing.T, pool *pgxpool.Pool, id string) string {
	t.Helper()
	var latest string
	err := pool.QueryRow(context.Background(), `SELECT latest_id FROM memories WHERE id=$1`, id).Scan(&latest)
	require.NoError(t, err)
	return latest
}

// statusOf returns the status column value for the given memory id.
func statusOf(t *testing.T, pool *pgxpool.Pool, id string) string {
	t.Helper()
	var status string
	err := pool.QueryRow(context.Background(), `SELECT status FROM memories WHERE id=$1`, id).Scan(&status)
	require.NoError(t, err)
	return status
}

// findByID returns the item with the given ID from a recall items slice, or
// fails the test if not found.
func findByID(items []MemoryWithStrength, id string) *Memory {
	for i := range items {
		if items[i].ID == id {
			return &items[i].Memory
		}
	}
	return nil
}

func strp(s string) *string { return &s }

// TestBackfillLatestID verifies migration 0026's backfill: a 3-version chain
// A<-B<-C (C active) should have all rows point latest_id at C (the head);
// a single-version memory S self-heads.
//
// NOTE: this test seeds rows directly (bypassing Remember, which already sets
// latest_id post-migration) to exercise the migration's backfill logic against
// pre-migration-shaped data. It runs the migration's Up SQL directly rather
// than assuming the column already exists — safe to run repeatedly since the
// migration is idempotent (ADD COLUMN IF NOT EXISTS + backfill only touches
// NULL/mismatched latest_id).
func TestBackfillLatestID(t *testing.T) {
	pool := setupLatestTestDB(t)
	u := testUser(t, pool)
	project := testProject(t, pool, u)

	idA := "mem_bfA_" + sanitizeTestName(t.Name())
	idB := "mem_bfB_" + sanitizeTestName(t.Name())
	idC := "mem_bfC_" + sanitizeTestName(t.Name())
	idS := "mem_bfS_" + sanitizeTestName(t.Name())

	seedMemory(t, pool, project, u, idA, "", "archived")
	seedMemory(t, pool, project, u, idB, idA, "archived")
	seedMemory(t, pool, project, u, idC, idB, "active")
	seedMemory(t, pool, project, u, idS, "", "active")

	runMigration(t, pool, "0026_memories_latest_id.sql")

	for _, id := range []string{idA, idB, idC} {
		assert.Equal(t, idC, latestOf(t, pool, id), "lineage member %s", id)
	}
	assert.Equal(t, idS, latestOf(t, pool, idS))
}

// TestBackfillLatestID_RedactedHead verifies the M1 fix: a component whose
// topological tip is redacted must NOT become the latest_id head. Redact()
// flips status in place (no new row), so a redacted tip is a real shape —
// pointing latest_id at it would make GetMemoryByID (which filters
// status!='redacted') 404 the whole lineage. rA(archived) -> rB(archived) ->
// rC(redacted): the head must resolve to rB, the newest NON-redacted row,
// and every row in the component must share that same head.
func TestBackfillLatestID_RedactedHead(t *testing.T) {
	pool := setupLatestTestDB(t)
	u := testUser(t, pool)
	project := testProject(t, pool, u)

	idA := "mem_rhA_" + sanitizeTestName(t.Name())
	idB := "mem_rhB_" + sanitizeTestName(t.Name())
	idC := "mem_rhC_" + sanitizeTestName(t.Name())

	seedMemory(t, pool, project, u, idA, "", "archived")
	seedMemory(t, pool, project, u, idB, idA, "archived")
	seedMemory(t, pool, project, u, idC, idB, "redacted")

	runMigration(t, pool, "0026_memories_latest_id.sql")

	for _, id := range []string{idA, idB, idC} {
		assert.Equal(t, idB, latestOf(t, pool, id), "lineage member %s", id)
	}

	got, gerr := GetLatestByID(context.Background(), pool, idA)
	require.Nil(t, gerr, "GetLatestByID(rA) must resolve, not 404")
	assert.Equal(t, idB, got.ID)
	assert.Equal(t, "archived", got.Status)
}

// TestLatestIDRoundTrip verifies that a fresh Remember (no supersede) sets
// latest_id to its own id (self-head), and that this comes back correctly
// through both Recall and GetMemoryByID (all 3 of the 6 lockstep scan sites
// exercised outside Remember itself).
func TestLatestIDRoundTrip(t *testing.T) {
	pool := setupLatestTestDB(t)
	u := testUser(t, pool)
	project := testProject(t, pool, u)

	mem, _, err := Remember(context.Background(), pool, &RememberRequest{
		Project:       project,
		Type:          "fact.note",
		Content:       "round trip content",
		Visibility:    "project",
		DedupMode:     "off",
		CallerUserID:  u,
		CallerDisplay: u,
	})
	require.NoError(t, err)
	require.NotNil(t, mem.LatestID)
	assert.Equal(t, mem.ID, *mem.LatestID)

	// GetMemoryByID round trip.
	got, gerr := GetMemoryByID(context.Background(), pool, mem.ID)
	require.Nil(t, gerr)
	require.NotNil(t, got.LatestID)
	assert.Equal(t, mem.ID, *got.LatestID)

	// Recall round trip.
	resp, rerr := Recall(context.Background(), pool, &RecallRequest{
		Project:      project,
		TopK:         10,
		MinStrength:  0,
		CallerUserID: u,
	})
	require.NoError(t, rerr)
	found := findByID(resp.Items, mem.ID)
	require.NotNil(t, found, "recalled memory not found")
	require.NotNil(t, found.LatestID)
	assert.Equal(t, mem.ID, *found.LatestID)
}

// TestSupersedeAdvancesCursor verifies the full propagation chain: A -> B (B
// supersedes A) -> C (C supersedes B, via B's OWN id, not A's). After each
// step, latest_id for every row in the lineage must point at the current
// head, and the old heads must be archived.
func TestSupersedeAdvancesCursor(t *testing.T) {
	pool := setupLatestTestDB(t)
	u := testUser(t, pool)
	project := testProject(t, pool, u)

	memA, _, err := Remember(context.Background(), pool, &RememberRequest{
		Project: project, Type: "fact.note", Content: "v1",
		Visibility: "project", DedupMode: "off",
		CallerUserID: u, CallerDisplay: u,
	})
	require.NoError(t, err)
	assert.Equal(t, memA.ID, latestOf(t, pool, memA.ID))

	memB, _, err := Remember(context.Background(), pool, &RememberRequest{
		Project: project, Type: "fact.note", Content: "v2",
		Visibility: "project", DedupMode: "off",
		CallerUserID: u, CallerDisplay: u,
		SupersedesMemID: strp(memA.ID),
	})
	require.NoError(t, err)
	assert.Equal(t, "archived", statusOf(t, pool, memA.ID))
	assert.Equal(t, memB.ID, latestOf(t, pool, memA.ID))
	assert.Equal(t, memB.ID, latestOf(t, pool, memB.ID))

	memC, _, err := Remember(context.Background(), pool, &RememberRequest{
		Project: project, Type: "fact.note", Content: "v3",
		Visibility: "project", DedupMode: "off",
		CallerUserID: u, CallerDisplay: u,
		SupersedesMemID: strp(memB.ID),
	})
	require.NoError(t, err)
	assert.Equal(t, "archived", statusOf(t, pool, memB.ID))
	for _, id := range []string{memA.ID, memB.ID, memC.ID} {
		assert.Equal(t, memC.ID, latestOf(t, pool, id), "lineage member %s", id)
	}

	// memC's own supersedes_id chain should point back to memB (linear chain).
	var supersedes *string
	err = pool.QueryRow(context.Background(), `SELECT supersedes_id FROM memories WHERE id=$1`, memC.ID).Scan(&supersedes)
	require.NoError(t, err)
	require.NotNil(t, supersedes)
	assert.Equal(t, memB.ID, *supersedes)
}

// TestGetLatestByID verifies head resolution from ANY id in a lineage
// (oldest, middle, or head itself) all return the same current head memory.
func TestGetLatestByID(t *testing.T) {
	pool := setupLatestTestDB(t)
	u := testUser(t, pool)
	project := testProject(t, pool, u)

	memA, _, err := Remember(context.Background(), pool, &RememberRequest{
		Project: project, Type: "fact.note", Content: "v1",
		Visibility: "project", DedupMode: "off",
		CallerUserID: u, CallerDisplay: u,
	})
	require.NoError(t, err)

	memB, _, err := Remember(context.Background(), pool, &RememberRequest{
		Project: project, Type: "fact.note", Content: "v2",
		Visibility: "project", DedupMode: "off",
		CallerUserID: u, CallerDisplay: u,
		SupersedesMemID: strp(memA.ID),
	})
	require.NoError(t, err)

	for _, startID := range []string{memA.ID, memB.ID} {
		got, gerr := GetLatestByID(context.Background(), pool, startID)
		require.Nil(t, gerr, "resolving from %s", startID)
		assert.Equal(t, memB.ID, got.ID, "resolving from %s", startID)
		assert.Equal(t, "v2", got.Content)
	}

	// Nonexistent id surfaces not-found.
	_, gerr := GetLatestByID(context.Background(), pool, "mem_does_not_exist_"+sanitizeTestName(t.Name()))
	require.NotNil(t, gerr)
	assert.Equal(t, ErrNotFound, gerr.Code)
}

// TestUpdateMemory verifies UpdateMemory: holding v1's id, updating content
// only creates a new version that inherits type/tags/base_strength, advances
// the cursor, and keeps v1's own id resolvable to the new head.
func TestUpdateMemory(t *testing.T) {
	pool := setupLatestTestDB(t)
	u := testUser(t, pool)
	project := testProject(t, pool, u)

	v1BaseStrength := 5.0
	v1, _, err := Remember(context.Background(), pool, &RememberRequest{
		Project: project, Type: "fact.note", Content: "v1",
		Visibility: "project", DedupMode: "off",
		CallerUserID: u, CallerDisplay: u,
		Tags:         []string{"keep"},
		BaseStrength: &v1BaseStrength,
	})
	require.NoError(t, err)
	require.Equal(t, 5.0, v1.BaseStrength)

	v2, err := UpdateMemory(context.Background(), pool, v1.ID, &UpdateMemoryRequest{
		Content:       strp("v2"),
		CallerUserID:  u,
		CallerDisplay: u,
	})
	require.NoError(t, err)
	assert.NotEqual(t, v1.ID, v2.ID)
	assert.Equal(t, "v2", v2.Content)
	assert.Equal(t, []string{"keep"}, v2.Tags)
	assert.Equal(t, "fact.note", v2.Type)
	// N1: a content-only update must inherit v1's base_strength (5), not
	// silently reset to Remember's own default of 3 for an unspecified field.
	assert.Equal(t, 5.0, v2.BaseStrength)
	assert.Equal(t, v2.ID, latestOf(t, pool, v1.ID))
	assert.Equal(t, "archived", statusOf(t, pool, v1.ID))

	// Updating visibility only (content/tags inherited from the new head).
	v3, err := UpdateMemory(context.Background(), pool, v1.ID, &UpdateMemoryRequest{
		Visibility:    strp("team"),
		CallerUserID:  u,
		CallerDisplay: u,
	})
	require.NoError(t, err)
	assert.Equal(t, "v2", v3.Content) // inherited from v2, not v1
	assert.Equal(t, "team", v3.Visibility)
	assert.Equal(t, []string{"keep"}, v3.Tags)
	assert.Equal(t, v3.ID, latestOf(t, pool, v1.ID))
	assert.Equal(t, v3.ID, latestOf(t, pool, v2.ID))

	// Unknown id surfaces not-found.
	_, err = UpdateMemory(context.Background(), pool, "mem_does_not_exist_"+sanitizeTestName(t.Name()), &UpdateMemoryRequest{
		Content: strp("nope"), CallerUserID: u, CallerDisplay: u,
	})
	require.Error(t, err)
}

// TestConcurrentUpdateSingleHead is the S2/BUG1 regression test: N concurrent
// UpdateMemory calls racing on the SAME v1 head must serialize into ONE
// linear chain, never branch into multiple active heads. This asserts the
// WHOLE-LINEAGE invariant over every row in the test's project (not just the
// subset reachable via a single, possibly-stale latest_id or a recursive
// supersedes_id walk seeded from v1) — a real branch produces extra rows
// with a distinct latest_id and/or a second active row that a narrower query
// could miss. Run with -race to also catch data races in the retry loop
// itself, and -count=3+ since it's inherently a race:
//
//	AIHUB_TEST_DB=... go test ./internal/domain/ -run TestConcurrentUpdateSingleHead -race -count=3 -v
func TestConcurrentUpdateSingleHead(t *testing.T) {
	pool := setupLatestTestDB(t)
	u := testUser(t, pool)
	project := testProject(t, pool, u)

	v1, _, err := Remember(context.Background(), pool, &RememberRequest{
		Project: project, Type: "fact.note", Content: "v1",
		Visibility: "project", DedupMode: "off",
		CallerUserID: u, CallerDisplay: u,
	})
	require.NoError(t, err)

	const n = 8
	var wg sync.WaitGroup
	errs := make([]error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			_, uerr := UpdateMemory(context.Background(), pool, v1.ID, &UpdateMemoryRequest{
				Content:       strp(fmt.Sprintf("concurrent-v%d", idx)),
				CallerUserID:  u,
				CallerDisplay: u,
			})
			errs[idx] = uerr
		}(i)
	}
	wg.Wait()

	for i, e := range errs {
		require.NoError(t, e, "goroutine %d", i)
	}

	// Every row in the test's project belongs to this lineage (testProject
	// wipes prior rows and each test uses its own project name), so scanning
	// the whole project is equivalent to scanning the whole lineage without
	// assuming any particular reachability path from v1.
	rows, qerr := pool.Query(context.Background(), `
		SELECT id, status, latest_id, supersedes_id FROM memories WHERE project = $1`, project)
	require.NoError(t, qerr)
	defer rows.Close()

	type row struct {
		id, status, latest string
		supersedes         *string
	}
	var all []row
	activeCount := 0
	latestIDs := map[string]bool{}
	for rows.Next() {
		var r row
		require.NoError(t, rows.Scan(&r.id, &r.status, &r.latest, &r.supersedes))
		all = append(all, r)
		if r.status == "active" {
			activeCount++
		}
		latestIDs[r.latest] = true
	}
	require.NoError(t, rows.Err())

	// (c) total rows == N+1 (linear chain, no branch).
	assert.Equal(t, n+1, len(all), "expected v1 plus %d concurrent updates, no branch rows", n)
	// (a) exactly one row is active.
	assert.Equal(t, 1, activeCount, "exactly one row in the lineage must be active")
	// (b) exactly one distinct latest_id, and it equals the active row's id.
	require.Len(t, latestIDs, 1, "all rows in the lineage must share one latest_id")
	var sharedLatest string
	for k := range latestIDs {
		sharedLatest = k
	}
	assert.Equal(t, "active", statusOf(t, pool, sharedLatest))
	var activeID string
	for _, r := range all {
		if r.status == "active" {
			activeID = r.id
		}
	}
	assert.Equal(t, activeID, sharedLatest, "shared latest_id must point at the active row")

	// (d) the non-root supersedes_id values must all be distinct and each
	// must reference a row that actually exists in this lineage — i.e. no two
	// rows share a parent (which would mean the archived head was superseded
	// twice, branching the chain) and no row supersedes a ghost id.
	knownIDs := map[string]bool{}
	for _, r := range all {
		knownIDs[r.id] = true
	}
	seenParents := map[string]bool{}
	for _, r := range all {
		if r.supersedes == nil {
			continue // the root (v1) has no parent
		}
		parent := *r.supersedes
		assert.False(t, seenParents[parent], "supersedes_id %s claimed by more than one row (branch)", parent)
		seenParents[parent] = true
		assert.True(t, knownIDs[parent], "supersedes_id %s does not exist in this lineage", parent)
	}
}

// TestRedactHeadRepointsCursor is the BUG2 regression test: redacting a
// lineage's active head must repoint the whole lineage's latest_id cursor at
// the newest surviving (non-redacted) row, otherwise GetLatestByID resolves
// every id in the lineage — including ones that were never redacted — to a
// row that GetMemoryByID filters out, 404ing the entire lineage.
func TestRedactHeadRepointsCursor(t *testing.T) {
	pool := setupLatestTestDB(t)
	u := testUser(t, pool)
	project := testProject(t, pool, u)

	v1, _, err := Remember(context.Background(), pool, &RememberRequest{
		Project: project, Type: "fact.note", Content: "v1",
		Visibility: "project", DedupMode: "off",
		CallerUserID: u, CallerDisplay: u,
	})
	require.NoError(t, err)

	v2, err := UpdateMemory(context.Background(), pool, v1.ID, &UpdateMemoryRequest{
		Content:       strp("v2"),
		CallerUserID:  u,
		CallerDisplay: u,
	})
	require.NoError(t, err)
	require.Equal(t, v2.ID, latestOf(t, pool, v1.ID), "precondition: v2 is the active head")

	// Redact the active head (v2). v1 is the newest surviving row.
	aerr := Redact(context.Background(), pool, v2.ID, u, "writer")
	require.Nil(t, aerr)

	assert.Equal(t, "redacted", statusOf(t, pool, v2.ID))
	assert.Equal(t, v1.ID, latestOf(t, pool, v1.ID),
		"lineage cursor must repoint to v1 (newest non-redacted) after v2 is redacted")
	assert.Equal(t, v1.ID, latestOf(t, pool, v2.ID),
		"resolving via the redacted row's own id must also see the repointed cursor")

	got, gerr := GetLatestByID(context.Background(), pool, v1.ID)
	require.Nil(t, gerr, "GetLatestByID(v1) must resolve, not 404, after v2's redaction")
	assert.Equal(t, v1.ID, got.ID)
	assert.Equal(t, "v1", got.Content)
}
