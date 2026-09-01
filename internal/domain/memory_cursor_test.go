package domain

// aihub#239: Recall's cursor must encode BOTH keys of its ORDER BY
// (memRefTimeSQL DESC, id DESC), and every supersede path — not just
// UpdateMemory — must carry the lineage's activation trio onto the new head.
//
// The pure cursor-codec tests below run under plain `go test ./...`. The
// DB-backed tests are gated on AIHUB_TEST_DB like memory_ranking_test.go:
//
//	AIHUB_TEST_DB=postgres://postgres:testpass@localhost:5432/aihub_test?sslmode=disable \
//	go test ./internal/domain/ -run 'TestRecallCursor|TestRemember_Supersede' -v -count=1
//
// A green `go test` is NOT evidence these ran — go test exits 0 when every
// selected test SKIPs, so CI asserts on the "--- PASS: <name>" lines instead.

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ─── pure cursor codec (no DB) ────────────────────────────────────────────────

func TestRecallCursor_RoundTrip(t *testing.T) {
	ref := time.Date(2026, 8, 19, 4, 5, 6, 123456789, time.UTC)
	got := formatRecallCursor(ref, "mem_AbC123")

	ts, id := parseRecallCursor(got)
	assert.Equal(t, "mem_AbC123", id)
	parsed, err := time.Parse(time.RFC3339Nano, ts)
	require.NoError(t, err, "timestamp half must stay RFC3339Nano-parseable — Postgres casts it with ::timestamptz")
	assert.True(t, parsed.Equal(ref), "round trip lost precision: got %v want %v", parsed, ref)
}

// A cursor issued before aihub#239 carries the timestamp alone. It must keep
// paginating under the old single-key semantics rather than erroring or being
// misread as an id, otherwise every in-flight cursor breaks on deploy.
func TestRecallCursor_LegacyTimestampOnlyHasNoIDHalf(t *testing.T) {
	legacy := time.Date(2026, 8, 19, 4, 5, 6, 0, time.UTC).Format(time.RFC3339Nano)

	ts, id := parseRecallCursor(legacy)
	assert.Equal(t, legacy, ts, "legacy cursor's timestamp must survive verbatim")
	assert.Equal(t, "", id, "legacy cursor has no id half; an empty id is what selects the single-key branch")
}

// The separator must not occur in either half, or parseRecallCursor would split
// in the wrong place. RFC3339Nano emits only digits and "-:.T+Z"; NewID emits
// base62 plus the "mem_" prefix underscore.
func TestRecallCursor_SeparatorCannotAppearInEitherHalf(t *testing.T) {
	require.Equal(t, "|", recallCursorSep, "this test's reasoning is specific to the chosen separator")

	// Offset-bearing (not just Z) timestamp, to cover the '+'/'-' forms too.
	loc := time.FixedZone("plus0530", 5*3600+30*60)
	for _, ref := range []time.Time{
		time.Date(2026, 8, 19, 4, 5, 6, 123456789, time.UTC),
		time.Date(2026, 1, 2, 3, 4, 5, 0, loc),
	} {
		stamp := ref.Format(time.RFC3339Nano)
		assert.NotContains(t, stamp, recallCursorSep, "RFC3339Nano must not contain the separator")
	}
	for i := 0; i < 200; i++ {
		assert.NotContains(t, NewID("mem"), recallCursorSep, "NewID must not contain the separator")
	}
}

// ─── DB-backed: the tie that a timestamp-only cursor dropped ──────────────────

// Six rows sharing ONE reference time, paged 2 at a time. Before aihub#239 the
// cursor was just that timestamp compared with a strict `<`, so page 2 asked for
// rows strictly older than the tie and returned nothing: 4 of the 6 rows were
// dropped from every page after the first. aihub#236's
// TestRecallRanking_CursorSkipsNoRows could not catch this because it seeds six
// rows at DISTINCT hourly offsets, so no tie is ever exercised.
func TestRecallCursor_TiedReferenceTimeSkipsNoRows(t *testing.T) {
	pool := setupLatestTestDB(t)
	uid := testUser(t, pool)
	proj := testProject(t, pool, uid)

	// One identical created_at, no activation -> identical reference times.
	// Reachable in production via bulk import or a backfill that stamps rows.
	tied := time.Now().Add(-2 * time.Hour)
	const n = 6
	// Fresh ids per run: memories.id is the primary key, so literal ids would
	// make this test fail on PK collision the second time it runs against the
	// same database (the trap TestResumeOwnLocks_* already falls into).
	ids := make([]string, n)
	for i := 0; i < n; i++ {
		ids[i] = NewID("mem")
		seedRankedMemory(t, pool, proj, uid, ids[i], "fact.note", tied, nil, 0)
	}

	seen := map[string]int{}
	cursor := ""
	pages := 0
	for pages < 10 {
		resp, aerr := Recall(context.Background(), pool, &RecallRequest{
			Project: proj, MinStrength: 0.3, TopK: 2,
			CallerUserID: uid, CallerRole: "writer", Cursor: cursor,
		})
		require.Nil(t, aerr, "Recall page %d", pages)
		pages++
		for i := range resp.Items {
			seen[resp.Items[i].ID]++
		}
		// total is counted before the cursor predicate (aihub#249), so every
		// page reports the whole matching set.
		assert.Equal(t, n, resp.Total, "page %d total", pages)
		if resp.NextCursor == nil {
			break
		}
		cursor = *resp.NextCursor
	}

	for _, id := range ids {
		assert.Equal(t, 1, seen[id], "%s seen %d times across %d pages, want exactly 1", id, seen[id], pages)
	}
	assert.Len(t, seen, n, "every tied row must appear exactly once")
}

// The compound cursor must not regress the ordinary distinct-timestamp case,
// and a caller replaying a pre-aihub#239 timestamp-only cursor must still get
// the rows after that timestamp.
func TestRecallCursor_LegacyCursorStillPaginates(t *testing.T) {
	pool := setupLatestTestDB(t)
	uid := testUser(t, pool)
	proj := testProject(t, pool, uid)

	const n = 4
	for i := 0; i < n; i++ {
		seedRankedMemory(t, pool, proj, uid,
			NewID("mem"), "fact.note",
			time.Now().Add(-time.Duration(i+1)*time.Hour), nil, 0)
	}

	first, aerr := Recall(context.Background(), pool, &RecallRequest{
		Project: proj, MinStrength: 0.3, TopK: 2,
		CallerUserID: uid, CallerRole: "writer",
	})
	require.Nil(t, aerr)
	require.NotNil(t, first.NextCursor, "4 rows at TopK=2 must yield a next cursor")

	// Downgrade the cursor to the pre-aihub#239 wire format.
	legacy, id := parseRecallCursor(*first.NextCursor)
	require.NotEqual(t, "", id, "new cursors must carry an id half")
	require.NotContains(t, legacy, recallCursorSep)

	rest, aerr := Recall(context.Background(), pool, &RecallRequest{
		Project: proj, MinStrength: 0.3, TopK: 10,
		CallerUserID: uid, CallerRole: "writer", Cursor: legacy,
	})
	require.Nil(t, aerr)

	firstIDs := map[string]bool{}
	for i := range first.Items {
		firstIDs[first.Items[i].ID] = true
	}
	require.Len(t, firstIDs, 2)
	for i := range rest.Items {
		assert.False(t, firstIDs[rest.Items[i].ID],
			"legacy cursor re-returned %s from the first page", rest.Items[i].ID)
	}
	assert.Len(t, rest.Items, n-2, "legacy cursor must still reach the remaining rows")
}

// ─── DB-backed: supersede carries the activation trio on every path ───────────

// pf_save_artifact and any POST /v1/memories with supersedes_memory_id reach
// Remember, not UpdateMemory. Before aihub#239 they minted a head with
// activation_count=0 / last_activated_at=NULL, stranding the lineage's history
// on the archived row and making aihub#236's guarantee path-dependent.
//
// experience.* is deliberate: fn_mem_immortal overwrites stability_days for
// rule.* / fact.* / methodology.*, so experience.* is the only family where the
// inherited count is observable in stability_days.
func TestRemember_SupersedeInheritsActivationState(t *testing.T) {
	pool := setupLatestTestDB(t)
	ctx := context.Background()
	uid := testUser(t, pool)
	proj := testProject(t, pool, uid)

	const memType = "experience.pitfall"
	const inheritedCount = 4
	headID := NewID("mem")
	activated := time.Now().Add(-3 * 24 * time.Hour).UTC()

	seedRankedMemory(t, pool, proj, uid, headID, memType,
		time.Now().Add(-10*24*time.Hour), &activated, inheritedCount)
	mustExec(t, pool, fmt.Sprintf(
		`UPDATE memories SET last_activated_by='%s' WHERE id='%s'`, uid, headID))

	// The Remember path, with NO trio supplied — exactly what the handler hands
	// over after zeroing the bindable fields.
	newHead, _, err := Remember(ctx, pool, &RememberRequest{
		Project:         proj,
		Type:            memType,
		Content:         "revised artifact body",
		Visibility:      "project",
		DedupMode:       "off",
		CallerUserID:    uid,
		CallerDisplay:   uid,
		SupersedesMemID: strPtr(headID),
	})
	require.NoError(t, err)

	assert.Equal(t, inheritedCount, newHead.ActivationCount,
		"supersede via Remember must carry activation_count, not reset it to 0")
	require.NotNil(t, newHead.LastActivatedAt,
		"supersede via Remember must carry last_activated_at, not leave it NULL")
	assert.WithinDuration(t, activated, newHead.LastActivatedAt.UTC(), time.Second)
	require.NotNil(t, newHead.LastActivatedBy)
	assert.Equal(t, uid, *newHead.LastActivatedBy)

	// stability_days is computed before the supersede head is resolved, so an
	// inherited count that did not trigger a recompute would leave a non-zero
	// activation_count stored against a stability derived from zero.
	assert.InDelta(t, computeStabilityDays(memType, inheritedCount), newHead.StabilityDays, 0.001,
		"stability_days must be recomputed from the inherited activation_count")

	// The reference time still resolves to the FRESH created_at: memRefTimeSQL is
	// GREATEST, so carrying a stale activation cannot decay or GC the new head
	// (this is why aihub#236 finding 1 does not reappear here).
	assert.True(t, memoryRefTime(newHead.LastActivatedAt, newHead.CreatedAt).Equal(newHead.CreatedAt),
		"fresh head's reference time must be its own created_at, not the carried activation")
}

// An explicitly supplied trio still wins — this is the UpdateMemory path, which
// passes the head's values itself. Guards against the inheritance clobbering a
// caller that already resolved the lineage.
func TestRemember_SupersedeKeepsExplicitActivationState(t *testing.T) {
	pool := setupLatestTestDB(t)
	ctx := context.Background()
	uid := testUser(t, pool)
	proj := testProject(t, pool, uid)

	const memType = "experience.pitfall"
	headID := NewID("mem")
	headActivated := time.Now().Add(-9 * 24 * time.Hour).UTC()
	seedRankedMemory(t, pool, proj, uid, headID, memType,
		time.Now().Add(-10*24*time.Hour), &headActivated, 2)

	explicitAt := time.Now().Add(-1 * 24 * time.Hour).UTC()
	newHead, _, err := Remember(ctx, pool, &RememberRequest{
		Project:         proj,
		Type:            memType,
		Content:         "explicit trio wins",
		Visibility:      "project",
		DedupMode:       "off",
		CallerUserID:    uid,
		CallerDisplay:   uid,
		SupersedesMemID: strPtr(headID),
		LastActivatedAt: &explicitAt,
		LastActivatedBy: &uid,
		ActivationCount: 7,
	})
	require.NoError(t, err)

	assert.Equal(t, 7, newHead.ActivationCount, "explicit activation_count must not be overwritten by inheritance")
	require.NotNil(t, newHead.LastActivatedAt)
	assert.WithinDuration(t, explicitAt, newHead.LastActivatedAt.UTC(), time.Second)
}

// UpdateMemory's own carry (aihub#236) and Remember's new inheritance must agree
// on the same lineage, so the guarantee is genuinely path-independent.
func TestRemember_SupersedeMatchesUpdateMemoryCarry(t *testing.T) {
	pool := setupLatestTestDB(t)
	ctx := context.Background()
	uid := testUser(t, pool)
	proj := testProject(t, pool, uid)

	const memType = "experience.pitfall"
	activated := time.Now().Add(-5 * 24 * time.Hour).UTC()

	headA, headB := NewID("mem"), NewID("mem")
	for _, id := range []string{headA, headB} {
		seedRankedMemory(t, pool, proj, uid, id, memType,
			time.Now().Add(-6*24*time.Hour), &activated, 3)
		mustExec(t, pool, fmt.Sprintf(
			`UPDATE memories SET last_activated_by='%s' WHERE id='%s'`, uid, id))
	}

	viaUpdate, uerr := UpdateMemory(ctx, pool, headA, &UpdateMemoryRequest{
		Content:       strPtr("edited via UpdateMemory"),
		CallerUserID:  uid,
		CallerDisplay: uid,
	})
	require.NoError(t, uerr)

	viaRemember, _, rerr := Remember(ctx, pool, &RememberRequest{
		Project:         proj,
		Type:            memType,
		Content:         "re-saved via Remember",
		Visibility:      "project",
		DedupMode:       "off",
		CallerUserID:    uid,
		CallerDisplay:   uid,
		SupersedesMemID: &headB,
	})
	require.NoError(t, rerr)

	assert.Equal(t, viaUpdate.ActivationCount, viaRemember.ActivationCount,
		"the two supersede paths must agree on activation_count")
	assert.InDelta(t, viaUpdate.StabilityDays, viaRemember.StabilityDays, 0.001,
		"the two supersede paths must agree on stability_days")
	require.NotNil(t, viaUpdate.LastActivatedAt)
	require.NotNil(t, viaRemember.LastActivatedAt)
	assert.WithinDuration(t, viaUpdate.LastActivatedAt.UTC(), viaRemember.LastActivatedAt.UTC(), time.Second)
}

// strPtr is a local helper so these tests do not depend on one existing
// elsewhere in the package's test files.
func strPtr(s string) *string { return &s }

// Guard the doc claim that the cursor halves are separator-free by checking the
// constant is still a single character that RFC3339Nano cannot emit.
func TestRecallCursor_SeparatorIsStable(t *testing.T) {
	assert.Len(t, recallCursorSep, 1)
	assert.False(t, strings.ContainsAny(recallCursorSep, "0123456789-:.TZ+"),
		"separator must not be a character RFC3339Nano can emit")
}
