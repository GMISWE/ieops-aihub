package domain

// DB-backed ranking tests for aihub#236. Gated on AIHUB_TEST_DB like
// memory_latest_test.go, so plain `go test ./...` skips them:
//
//	AIHUB_TEST_DB=postgres://postgres:test@localhost:5440/aihub_test?sslmode=disable \
//	go test ./internal/domain/ -run TestRecallRanking -v -count=1

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// seedRankedMemory inserts one memory with explicit created_at / activation
// state so ranking order can be constructed deterministically. Note the
// BEFORE INSERT trigger trg_mem_immortal overrides stability_days by type
// prefix, so pass memType deliberately (fact.* -> 180d, methodology.* -> 36500).
func seedRankedMemory(t *testing.T, pool *pgxpool.Pool, project, userID, id, memType string,
	createdAt time.Time, lastActivatedAt *time.Time, activationCount int) {
	t.Helper()
	_, err := pool.Exec(context.Background(), `
		INSERT INTO memories (id, project, type, content, author_user_id, author_display,
			visibility, status, tags, attrs, base_strength,
			activation_count, last_activated_at, created_at, latest_id)
		VALUES ($1,$2,$3,$1,$4,$4,'project','active','{}','{}',3,$5,$6,$7,$1)`,
		id, project, memType, userID, activationCount, lastActivatedAt, createdAt)
	if err != nil {
		t.Fatalf("seedRankedMemory(%s): %v", id, err)
	}
}

func recallAll(t *testing.T, pool *pgxpool.Pool, project, userID string, topK int) []MemoryWithStrength {
	t.Helper()
	resp, aerr := Recall(context.Background(), pool, &RecallRequest{
		Project:      project,
		MinStrength:  0.3,
		TopK:         topK,
		CallerUserID: userID,
		CallerRole:   "writer",
	})
	if aerr != nil {
		t.Fatalf("Recall: %v", aerr)
	}
	return resp.Items
}

func rankOf(items []MemoryWithStrength, id string) int {
	for i := range items {
		if items[i].ID == id {
			return i
		}
	}
	return -1
}

// THE REPORTED BUG. An older memory activated once must NOT outrank a newer
// never-activated one. Under `ORDER BY last_activated_at DESC NULLS LAST`
// every activated row formed a tier above every never-activated row, so the
// fresh memory sorted last regardless of age.
func TestRecallRanking_FreshNeverActivatedBeatsOlderActivated(t *testing.T) {
	pool := setupLatestTestDB(t)
	uid := testUser(t, pool)
	proj := testProject(t, pool, uid)

	oldActivation := time.Now().Add(-13 * 24 * time.Hour)
	seedRankedMemory(t, pool, proj, uid, "mem_rank_activated", "fact.note",
		time.Now().Add(-14*24*time.Hour), &oldActivation, 1)
	seedRankedMemory(t, pool, proj, uid, "mem_rank_fresh", "fact.note",
		time.Now().Add(-1*time.Hour), nil, 0)

	items := recallAll(t, pool, proj, uid, 10)
	fresh, activated := rankOf(items, "mem_rank_fresh"), rankOf(items, "mem_rank_activated")
	if fresh < 0 || activated < 0 {
		t.Fatalf("both memories must be recalled: fresh=%d activated=%d", fresh, activated)
	}
	if fresh > activated {
		t.Errorf("fresh never-activated memory ranked #%d, below activated-13d-ago at #%d "+
			"— NULLS LAST tier still present", fresh, activated)
	}
}

// Pins the load-bearing, dialect-specific property: PostgreSQL GREATEST
// IGNORES NULL arguments. On Oracle/MySQL this returns NULL and the whole
// ranking silently collapses.
func TestRecallRanking_GreatestIgnoresNullArgument(t *testing.T) {
	pool := setupLatestTestDB(t)
	var equal bool
	err := pool.QueryRow(context.Background(),
		`SELECT GREATEST(NULL::timestamptz, now()) = now()`).Scan(&equal)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if !equal {
		t.Fatal("GREATEST(NULL, now()) != now() — this PostgreSQL ignores-NULL " +
			"behaviour is required by memRefTimeSQL")
	}
}

// Paging must not skip rows. The old cursor was a single timestamp compared
// against a two-branch predicate, so crossing from the activated tier into the
// NULL tier permanently skipped any never-activated row created after the last
// activated row's activation time.
func TestRecallRanking_CursorSkipsNoRows(t *testing.T) {
	pool := setupLatestTestDB(t)
	uid := testUser(t, pool)
	proj := testProject(t, pool, uid)

	// Interleave activated and never-activated rows so any tier boundary is
	// crossed mid-pagination.
	for i := 0; i < 6; i++ {
		created := time.Now().Add(-time.Duration(i) * time.Hour)
		var act *time.Time
		count := 0
		if i%2 == 0 {
			a := time.Now().Add(-time.Duration(i)*time.Hour - 30*time.Minute)
			act, count = &a, 1
		}
		seedRankedMemory(t, pool, proj, uid,
			fmt.Sprintf("mem_cur_%d", i), "fact.note", created, act, count)
	}

	seen := map[string]int{}
	cursor := ""
	for page := 0; page < 10; page++ {
		resp, aerr := Recall(context.Background(), pool, &RecallRequest{
			Project: proj, MinStrength: 0.3, TopK: 2,
			CallerUserID: uid, CallerRole: "writer", Cursor: cursor,
		})
		if aerr != nil {
			t.Fatalf("Recall page %d: %v", page, aerr)
		}
		for i := range resp.Items {
			seen[resp.Items[i].ID]++
		}
		if resp.NextCursor == nil {
			break
		}
		cursor = *resp.NextCursor
	}
	for i := 0; i < 6; i++ {
		id := fmt.Sprintf("mem_cur_%d", i)
		if seen[id] != 1 {
			t.Errorf("%s seen %d times across pages, want exactly 1", id, seen[id])
		}
	}
}

// An edit must not discard the lineage's activation history. Before aihub#236
// UpdateMemory rebuilt RememberRequest from the head without the activation
// trio, so every new version started at activation_count=0 with a NULL
// last_activated_at — which, under the old ranking, demoted it.
func TestUpdateMemory_CarriesActivationState(t *testing.T) {
	pool := setupLatestTestDB(t)
	ctx := context.Background()
	uid := testUser(t, pool)
	proj := testProject(t, pool, uid)

	act := time.Now().Add(-3 * 24 * time.Hour)
	seedRankedMemory(t, pool, proj, uid, "mem_carry_head", "fact.note",
		time.Now().Add(-4*24*time.Hour), &act, 2)
	mustExec(t, pool, `UPDATE memories SET last_activated_by='`+uid+
		`' WHERE id='mem_carry_head'`)

	newHead, err := UpdateMemory(ctx, pool, "mem_carry_head", &UpdateMemoryRequest{
		Content:       strp("edited content"),
		CallerUserID:  uid,
		CallerDisplay: uid,
	})
	if err != nil {
		t.Fatalf("UpdateMemory: %v", err)
	}

	if newHead.ActivationCount != 2 {
		t.Errorf("activation_count: got %d, want 2 (carried from head)", newHead.ActivationCount)
	}
	if newHead.LastActivatedAt == nil {
		t.Fatal("last_activated_at: got nil, want the head's timestamp")
	}
	if d := newHead.LastActivatedAt.Sub(act); d > time.Second || d < -time.Second {
		t.Errorf("last_activated_at: got %v, want ~%v", *newHead.LastActivatedAt, act)
	}
	if newHead.LastActivatedBy == nil || *newHead.LastActivatedBy != uid {
		t.Errorf("last_activated_by: got %v, want %q", newHead.LastActivatedBy, uid)
	}
	if newHead.Content != "edited content" {
		t.Errorf("content not applied: %q", newHead.Content)
	}
}

// The regression Task 2's min_strength change exists to prevent: a fact.*
// memory (stability 180d) whose carried last_activated_at is ~200 days old is
// brand new as a row, and must NOT be decay-filtered out of recall.
func TestUpdateMemory_FreshEditWithStaleActivationSurvivesMinStrength(t *testing.T) {
	pool := setupLatestTestDB(t)
	ctx := context.Background()
	uid := testUser(t, pool)
	proj := testProject(t, pool, uid)

	// 500 days, NOT 200. The break-even for a fact.* memory (trigger-forced
	// stability_days=180, base_strength=3) against the default min_strength of
	// 0.3 is 180*ln(3/0.3) = 414.5 days. At 200 days the OLD COALESCE code
	// still computes 3*exp(-200/180) = 0.988 >= 0.3, so the assertion passed
	// against the unfixed implementation and proved nothing. At 500 days the
	// stale reference time yields 3*exp(-500/180) = 0.186 < 0.3 and the row is
	// filtered out, so this test genuinely fails without the fix.
	stale := time.Now().Add(-500 * 24 * time.Hour)
	seedRankedMemory(t, pool, proj, uid, "mem_stale_head", "fact.note",
		time.Now().Add(-501*24*time.Hour), &stale, 1)

	newHead, err := UpdateMemory(ctx, pool, "mem_stale_head", &UpdateMemoryRequest{
		Content:       strp("re-edited long-dormant note"),
		CallerUserID:  uid,
		CallerDisplay: uid,
	})
	if err != nil {
		t.Fatalf("UpdateMemory: %v", err)
	}

	items := recallAll(t, pool, proj, uid, 10)
	if rankOf(items, newHead.ID) < 0 {
		t.Errorf("freshly edited head %s was filtered out of recall at min_strength=0.3 "+
			"— reference time is decaying against the carried stale activation "+
			"instead of the new created_at", newHead.ID)
	}
}

// Guards the blocking regression found in code review round 1: the GC sweep
// ARCHIVES rows, and it computed decay from COALESCE(last_activated_at,
// created_at). Once UpdateMemory began carrying a stale last_activated_at onto
// each new version, a memory edited seconds ago decayed against that stale
// value and was archived on the next 60s tick — silently undoing the edit while
// Recall still reported full strength.
//
// experience.* is used deliberately: the trg_mem_immortal trigger does NOT
// force stability_days for that prefix, so computeStabilityDays applies
// (7 * (1 + 1*0.5) = 10.5 days) and the archive threshold of 0.1 is reached
// after only 10.5*ln(30) ~= 36 days. With fact.* the trigger pins 180 days and
// the threshold is ~612 days away, which would make the test slow to reason
// about rather than wrong.
func TestMemoryExpiredSweep_DoesNotArchiveFreshEditWithStaleActivation(t *testing.T) {
	pool := setupLatestTestDB(t)
	ctx := context.Background()
	uid := testUser(t, pool)
	proj := testProject(t, pool, uid)

	// A brand-new row (created_at = now) carrying a 60-day-old activation.
	// Reference time must be created_at, so strength is ~3.0 and it survives.
	// Under COALESCE it would be 3*exp(-60/10.5) = 0.010 < 0.1 -> archived.
	stale := time.Now().Add(-60 * 24 * time.Hour)
	seedRankedMemory(t, pool, proj, uid, "mem_gc_freshedit", "experience.pitfall",
		time.Now(), &stale, 1)
	mustExec(t, pool, `UPDATE memories SET stability_days = 10.5
		WHERE id = 'mem_gc_freshedit'`)

	if res := RunMemoryExpiredSweep(ctx, pool); res.Error != "" {
		t.Fatalf("sweep: %s", res.Error)
	}

	var status string
	if err := pool.QueryRow(ctx,
		`SELECT status FROM memories WHERE id='mem_gc_freshedit'`).Scan(&status); err != nil {
		t.Fatalf("read back: %v", err)
	}
	if status != "active" {
		t.Errorf("freshly created row carrying a 60-day-old activation was %q, want \"active\" "+
			"— the GC sweep is decaying against the stale activation instead of created_at, "+
			"so editing a memory silently deletes it", status)
	}
}

// The converse, so the test above cannot pass merely because the sweep is inert:
// a genuinely dormant row (old created_at AND old activation) must still be
// archived. Without this, replacing the sweep's WHERE clause with `false` would
// satisfy the previous test.
func TestMemoryExpiredSweep_StillArchivesGenuinelyDecayedMemory(t *testing.T) {
	pool := setupLatestTestDB(t)
	ctx := context.Background()
	uid := testUser(t, pool)
	proj := testProject(t, pool, uid)

	old := time.Now().Add(-60 * 24 * time.Hour)
	seedRankedMemory(t, pool, proj, uid, "mem_gc_dormant", "experience.pitfall",
		time.Now().Add(-60*24*time.Hour), &old, 1)
	mustExec(t, pool, `UPDATE memories SET stability_days = 10.5
		WHERE id = 'mem_gc_dormant'`)

	if res := RunMemoryExpiredSweep(ctx, pool); res.Error != "" {
		t.Fatalf("sweep: %s", res.Error)
	}

	var status string
	if err := pool.QueryRow(ctx,
		`SELECT status FROM memories WHERE id='mem_gc_dormant'`).Scan(&status); err != nil {
		t.Fatalf("read back: %v", err)
	}
	if status != "archived" {
		t.Errorf("genuinely dormant memory (created 60d ago, activated 60d ago) was %q, "+
			"want \"archived\" — the sweep must still reclaim real decay", status)
	}
}
