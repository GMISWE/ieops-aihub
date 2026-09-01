package domain

// DB-backed tests for the two emitting GC sweeps (aihub#266).
//
// The unit half in gc_test.go pins the cadence constants, the sweep table and
// the schedule arithmetic. None of that can answer the question the wi actually
// asks — how many rows does one unclassified work item cost per day — so
// everything here measures that by RUNNING the sweep 1,440 times, which is the
// number of calls cmd/aihub/main.go's 60s ticker makes in a day. The number is
// never derived from the ticker interval; the point of the bug was that the
// interval and the documented cadence disagreed.
//
// Follows the AIHUB_TEST_DB gating pattern (memory_latest_test.go), so it never
// runs in a plain `go test ./...`. Like gc_partition_db_test.go it does NOT need
// the migrated schema: it builds a self-contained work_items + agent_events in
// its own schema, so any empty Postgres will do and pgvector is not required.
//
//	AIHUB_TEST_DB='postgres://postgres@/postgres?host=/path/to/socket&port=5599' \
//	go test ./internal/domain/ -run TestAlertSweep -v -count=1
//
// Three caveats. The first two are the ones gc_partition_db_test.go carries:
//   - always pass `-run TestAlertSweep...`; the other AIHUB_TEST_DB-gated tests
//     in this package DO need the fully migrated schema, so pointing
//     AIHUB_TEST_DB at a bare database and running the whole package turns their
//     skips into `relation "users" does not exist` failures.
//   - the sweeps take advisory locks 2007/2008, which are database-wide. Point
//     AIHUB_TEST_DB at a database no live aihub instance is connected to, or the
//     sweeps return Skipped and these tests fail telling you so.
//   - do not point TWO concurrent runs of this file at one database. The schema
//     names below are deterministic (pf266_ + the test name), so two runs share
//     a schema and drop it from under each other, and the advisory locks above
//     are database-wide, so they also make each other Skip. The failures that
//     produces look like real defects in both directions. CI is safe — all the
//     DB steps are sequential in one job — but two people debugging at once are
//     not, and neither is a reviewer running the suite while the author does.
//     Use a private database (CREATE DATABASE) rather than a private schema.

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/GMISWE/ieops-aihub/internal/citest/testname"
)

// ticksPerDay is how many times cmd/aihub/main.go's 60s ticker fires in 24
// hours. Every "per day" number in this file is measured over exactly this many
// sweep drives, not extrapolated from one.
const ticksPerDay = 24 * 60

// pollsPerDay is how many of those 1,440 ticks the schedule lets an alert sweep
// through. Derived from gcAlertPollPeriod rather than written as 24, so the
// arithmetic cannot survive a change to the constant it describes.
var pollsPerDay = int(24 * time.Hour / gcAlertPollPeriod)

// workItemsFixtureDDL is the subset of work_items the two alert sweeps read:
// their predicates use requires_human_session, status, created_at and priority,
// and their payloads carry slug / wi_type / project / reporter_user_id.
//
// requires_human_session is deliberately left NULLABLE with no default, because
// NULL is the THIRD state the whole unclassified alert exists for (migration
// 0002: a NULL wi goes to the ready queue's unclassified[] rather than items[]).
// A fixture that defaulted it to false could not represent the case under test.
const workItemsFixtureDDL = `
CREATE TABLE work_items (
    id                     TEXT PRIMARY KEY,
    slug                   TEXT NOT NULL,
    project                TEXT NOT NULL,
    wi_type                TEXT,
    priority               TEXT NOT NULL DEFAULT 'medium',
    status                 TEXT NOT NULL,
    reporter_user_id       TEXT NOT NULL,
    requires_human_session BOOLEAN,
    created_at             TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp()
);
`

// setupAlertSweepTestDB gives each test its own schema holding a fresh
// work_items and a fresh partitioned agent_events. agentEventsFixtureDDL and
// attachDefaultPartition are reused from gc_partition_db_test.go so there is one
// definition of the events fixture in this package rather than two that drift.
//
// The DEFAULT partition is what makes every row land somewhere regardless of the
// created_at these tests choose, which matters because the reverse probes
// back-date rows by nearly a day. It also keeps agent_events a genuinely
// PARTITIONED relation here, which is the reason the idempotency guard is a
// repeat window and not a unique index in the first place.
func setupAlertSweepTestDB(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dbURL := os.Getenv("AIHUB_TEST_DB")
	if dbURL == "" {
		t.Skip("set AIHUB_TEST_DB to run this integration test")
	}

	schema := "pf266_" + testname.Sanitize(t.Name())
	ctx := context.Background()

	cfg, err := pgxpool.ParseConfig(dbURL)
	if err != nil {
		t.Fatalf("parse AIHUB_TEST_DB: %v", err)
	}
	cfg.AfterConnect = func(ctx context.Context, conn *pgx.Conn) error {
		// Never fall through to public: a sweep that lost its schema qualifier
		// must fail loudly here rather than quietly touching real data.
		_, err := conn.Exec(ctx, `SET search_path = `+schema)
		return err
	}

	admin, err := pgxpool.New(ctx, dbURL)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer admin.Close()
	if _, err := admin.Exec(ctx, `DROP SCHEMA IF EXISTS `+schema+` CASCADE`); err != nil {
		t.Fatalf("drop schema: %v", err)
	}
	if _, err := admin.Exec(ctx, `CREATE SCHEMA `+schema); err != nil {
		t.Fatalf("create schema: %v", err)
	}

	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatalf("connect with search_path: %v", err)
	}
	if _, err := pool.Exec(ctx, agentEventsFixtureDDL); err != nil {
		pool.Close()
		t.Fatalf("create agent_events fixture: %v", err)
	}
	if _, err := pool.Exec(ctx, workItemsFixtureDDL); err != nil {
		pool.Close()
		t.Fatalf("create work_items fixture: %v", err)
	}
	attachDefaultPartition(t, pool)

	t.Cleanup(func() {
		pool.Close()
		cleanup, err := pgxpool.New(context.Background(), dbURL)
		if err != nil {
			return
		}
		defer cleanup.Close()
		_, _ = cleanup.Exec(context.Background(), `DROP SCHEMA IF EXISTS `+schema+` CASCADE`)
	})
	return pool
}

// seedAlertWI inserts one work item. requires_human_session is a *bool so the tests
// can seed the NULL third state explicitly.
func seedAlertWI(t *testing.T, pool *pgxpool.Pool, id, slug string, rhs *bool, ageDays int, priority string) {
	t.Helper()
	_, err := pool.Exec(context.Background(), `
		INSERT INTO work_items
		  (id, slug, project, wi_type, priority, status, reporter_user_id,
		   requires_human_session, created_at)
		VALUES ($1, $2, 'aihub', 'fix_bug', $3, 'queued', 'u_test', $4,
		        clock_timestamp() - make_interval(days => $5))`,
		id, slug, priority, rhs, ageDays)
	if err != nil {
		t.Fatalf("seed wi %s: %v", slug, err)
	}
}

// seedUnclassifiedWI is the case sweep 8 exists for: queued, old enough, and
// requires_human_session IS NULL.
func seedUnclassifiedWI(t *testing.T, pool *pgxpool.Pool, id, slug string) {
	t.Helper()
	seedAlertWI(t, pool, id, slug, nil, 2, "high")
}

// seedAgedHumanSessionWI is the case sweep 7 exists for: queued,
// requires_human_session = true, past the 7-day non-urgent threshold.
func seedAgedHumanSessionWI(t *testing.T, pool *pgxpool.Pool, id, slug string) {
	t.Helper()
	rhs := true
	seedAlertWI(t, pool, id, slug, &rhs, 10, "high")
}

func countEvents(t *testing.T, pool *pgxpool.Pool, eventType string) int64 {
	t.Helper()
	var n int64
	if err := pool.QueryRow(context.Background(),
		`SELECT count(*) FROM agent_events WHERE event_type = $1`, eventType).Scan(&n); err != nil {
		t.Fatalf("count %s: %v", eventType, err)
	}
	return n
}

func countEventsFor(t *testing.T, pool *pgxpool.Pool, eventType, wiID string) int64 {
	t.Helper()
	var n int64
	if err := pool.QueryRow(context.Background(),
		`SELECT count(*) FROM agent_events WHERE event_type = $1 AND work_item_id = $2`,
		eventType, wiID).Scan(&n); err != nil {
		t.Fatalf("count %s for %s: %v", eventType, wiID, err)
	}
	return n
}

// backdateEvents moves every event of a type back by d, simulating the passage
// of time without the test waiting for it. Row movement across partitions is
// handled by Postgres; with only a DEFAULT partition attached the rows stay put.
func backdateEvents(t *testing.T, pool *pgxpool.Pool, eventType string, d time.Duration) {
	t.Helper()
	tag, err := pool.Exec(context.Background(), `
		UPDATE agent_events SET created_at = created_at - make_interval(secs => $2)
		WHERE event_type = $1`, eventType, d.Seconds())
	if err != nil {
		t.Fatalf("backdate %s: %v", eventType, err)
	}
	if tag.RowsAffected() == 0 {
		t.Fatalf("backdate %s moved no rows, so the elapsed-window probe below would be "+
			"asserting against a state it never reached", eventType)
	}
}

func mustNotSkipAlertSweep(t *testing.T, r GCResult) {
	t.Helper()
	if r.Skipped {
		t.Fatalf("%s skipped: its advisory lock is held elsewhere — point AIHUB_TEST_DB at "+
			"a database with no live aihub instance", r.SweepType)
	}
	if r.Error != "" {
		t.Fatalf("%s reported an error: %s", r.SweepType, r.Error)
	}
}

// ─── The pre-change build, reconstructed ─────────────────────────────────────

// legacyUnclassifiedCandidateSQL and legacyUnclassifiedInsertSQL are the two
// statements RunUnclassifiedWIAlert shipped with BEFORE aihub#266, copied
// verbatim from internal/domain/gc.go at ff5a5cd: a candidate query with no
// idempotency predicate, and an unconditional INSERT ... VALUES.
//
// They are the negative control. Every assertion in this file about the new
// behaviour is worthless unless the same measurement, pointed at the old
// statements, comes back with the old number — otherwise "no duplicates" could
// equally mean the counter is broken, the fixture selects nothing, or the sweep
// emits nothing at all. Which is the same class of defect as the one being
// fixed: a guard whose success condition is that nothing happened.
const legacyUnclassifiedCandidateSQL = `
		SELECT id, slug, project, reporter_user_id
		FROM work_items
		WHERE requires_human_session IS NULL
		  AND status = 'queued'
		  AND created_at < now() - interval '1 day'`

const legacyUnclassifiedInsertSQL = `
			INSERT INTO agent_events (id, work_item_id, event_type, payload, project, created_at)
			VALUES ($1, $2, 'wi_classification_missing', $3, $4, clock_timestamp())`

const legacyNeedsHumanSessionCandidateSQL = `
		SELECT id, slug, wi_type, priority, project, created_at
		FROM work_items
		WHERE requires_human_session = true
		  AND status = 'queued'
		  AND created_at < now() - CASE priority
		      WHEN 'urgent' THEN interval '1 day'
		      ELSE interval '7 days'
		    END`

const legacyNeedsHumanSessionInsertSQL = `
			INSERT INTO agent_events (id, work_item_id, event_type, payload, project, created_at)
			VALUES ($1, $2, 'wi_needs_attention', $3, $4, clock_timestamp())`

// legacyUnclassifiedProjectCol / legacyAgingProjectCol are where `project` sits
// in each legacy candidate query's select list. Named rather than derived,
// because a control that guesses at its own reconstruction is not a control.
const (
	legacyUnclassifiedProjectCol = 2 // id, slug, project, reporter_user_id
	legacyAgingProjectCol        = 4 // id, slug, wi_type, priority, project, created_at
)

// legacyEmit replays the pre-change loop shape: select candidates with no
// idempotency predicate, then INSERT one row per candidate unconditionally.
//
// Only the wi id and its project are needed to reproduce the row count. The
// payload body is not what any measurement here counts, and reproducing it would
// only invite the control to drift with a payload change that is irrelevant to
// the defect.
func legacyEmit(t *testing.T, pool *pgxpool.Pool, candidateSQL, insertSQL string, projectCol int) int64 {
	t.Helper()
	ctx := context.Background()
	rows, err := pool.Query(ctx, candidateSQL)
	if err != nil {
		t.Fatalf("legacy candidate query: %v", err)
	}
	type candidate struct{ id, project string }
	var candidates []candidate
	for rows.Next() {
		vals, err := rows.Values()
		if err != nil {
			rows.Close()
			t.Fatalf("legacy scan: %v", err)
		}
		if len(vals) <= projectCol {
			rows.Close()
			t.Fatalf("FIXTURE, NOT CODE: the legacy candidate query returns %d columns, so "+
				"column %d is not `project` any more", len(vals), projectCol)
		}
		candidates = append(candidates, candidate{
			id:      vals[0].(string),
			project: vals[projectCol].(string),
		})
	}
	rows.Close()

	emitted := int64(0)
	for _, c := range candidates {
		tag, err := pool.Exec(ctx, insertSQL, NewID("evt"), c.id, []byte(`{"legacy":true}`), c.project)
		if err != nil {
			t.Fatalf("legacy insert: %v", err)
		}
		emitted += tag.RowsAffected()
	}
	return emitted
}

// ─── 1. FORWARD: events per day, measured ────────────────────────────────────

// TestAlertSweep_UnclassifiedWIEmitsOnePerDayNotOnePerTick is criterion 1 of
// aihub#266.
//
// It drives a full simulated day of the production ticker — 1,440 calls — over a
// fixture holding exactly one unclassified work item, in four arms that cross the
// two defects. Naming both drivers and both emitters is what shows the two
// defects are independent, and which one each fix is doing:
//
//	driver            emitter          runs/day  rows/day
//	every tick (old)  unconditional        1440      1440   <- shipped behaviour
//	every tick (old)  window-guarded       1440         1   <- idempotency alone
//	hourly poll (new) unconditional          24        24   <- cadence alone
//	hourly poll (new) window-guarded         24         1   <- shipped here
//
// Row 3 is the point of running all four: the cadence fix alone cuts runs by 60x
// and still leaves 24 duplicate rows a day, because a poll period is a load knob
// and cannot be a correctness guard. Row 2 is its mirror — the idempotency fix
// alone gets the row count exactly right while still asking the database 1,440
// times. Only the two together give 24 runs and 1 row.
func TestAlertSweep_UnclassifiedWIEmitsOnePerDayNotOnePerTick(t *testing.T) {
	if os.Getenv("AIHUB_TEST_DB") == "" {
		t.Skip("set AIHUB_TEST_DB to run this integration test")
	}
	start := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	ctx := context.Background()

	for _, arm := range []struct {
		name       string
		newDriver  bool
		newEmitter bool
		wantRows   int64
		wantRuns   int
	}{
		{"old_driver_old_emitter", false, false, ticksPerDay, ticksPerDay},
		{"old_driver_new_emitter", false, true, 1, ticksPerDay},
		{"new_driver_old_emitter", true, false, int64(pollsPerDay), pollsPerDay},
		{"new_driver_new_emitter", true, true, 1, pollsPerDay},
	} {
		t.Run(arm.name, func(t *testing.T) {
			pool := setupAlertSweepTestDB(t)
			seedUnclassifiedWI(t, pool, "wi_flood", "aihub#266probe")

			sched := newGCSchedule()
			runs := 0
			for i := 0; i < ticksPerDay; i++ {
				now := start.Add(time.Duration(i) * time.Minute)
				if arm.newDriver && !sched.due(sweepUnclassifiedWIAlert, gcAlertPollPeriod, now) {
					continue
				}
				runs++
				if arm.newDriver {
					sched.record(sweepUnclassifiedWIAlert, gcAlertPollPeriod, now)
				}
				if arm.newEmitter {
					r := RunUnclassifiedWIAlert(ctx, pool)
					mustNotSkipAlertSweep(t, r)
				} else {
					legacyEmit(t, pool, legacyUnclassifiedCandidateSQL, legacyUnclassifiedInsertSQL, legacyUnclassifiedProjectCol)
				}
			}

			got := countEvents(t, pool, "wi_classification_missing")
			t.Logf("%s: %d sweep runs over one simulated day -> %d wi_classification_missing rows",
				arm.name, runs, got)
			if runs != arm.wantRuns {
				t.Errorf("the sweep ran %d times across one day of 60s ticks, want %d",
					runs, arm.wantRuns)
			}
			if got != arm.wantRows {
				t.Errorf("one unclassified wi accumulated %d events in a simulated day, want %d",
					got, arm.wantRows)
			}
		})
	}
}

// TestAlertSweep_NeedsHumanSessionEmitsOnePerDayNotOnePerTick is the same
// measurement for sweep 7, which carries the identical defect and is fixed the
// same way. Both had to be fixed: the wi measured 46 wi_classification_missing
// AND 36 wi_needs_attention rows in every single minute bucket.
func TestAlertSweep_NeedsHumanSessionEmitsOnePerDayNotOnePerTick(t *testing.T) {
	if os.Getenv("AIHUB_TEST_DB") == "" {
		t.Skip("set AIHUB_TEST_DB to run this integration test")
	}
	ctx := context.Background()

	t.Run("shipped_behaviour", func(t *testing.T) {
		pool := setupAlertSweepTestDB(t)
		seedAgedHumanSessionWI(t, pool, "wi_aged", "aihub#266aged")
		for i := 0; i < ticksPerDay; i++ {
			legacyEmit(t, pool, legacyNeedsHumanSessionCandidateSQL, legacyNeedsHumanSessionInsertSQL, legacyAgingProjectCol)
		}
		got := countEvents(t, pool, "wi_needs_attention")
		t.Logf("pre-change: %d wi_needs_attention rows/day for one aged wi", got)
		if got != ticksPerDay {
			t.Fatalf("the pre-change reconstruction produced %d rows over %d ticks, want %d; "+
				"the control is not reproducing the shipped behaviour", got, ticksPerDay, ticksPerDay)
		}
	})

	t.Run("after_the_fix", func(t *testing.T) {
		pool := setupAlertSweepTestDB(t)
		seedAgedHumanSessionWI(t, pool, "wi_aged", "aihub#266aged")
		for i := 0; i < ticksPerDay; i++ {
			mustNotSkipAlertSweep(t, RunNeedsHumanSessionAging(ctx, pool))
		}
		got := countEvents(t, pool, "wi_needs_attention")
		t.Logf("after the fix: %d wi_needs_attention rows/day for one aged wi", got)
		if got != 1 {
			t.Errorf("one aged wi accumulated %d wi_needs_attention events in a simulated "+
				"day, want 1", got)
		}
	})
}

// ─── 2. NEGATIVE CONTROL: the measurement must fail on the pre-change build ──

// TestAlertSweep_MeasurementFailsOnPreChangeBuild is criterion 3 of aihub#266,
// in the shape aihub#287's TestResumePath_MeasurementFailsOnPreChangeBuild
// established: reconstruct the build as it shipped, run the SAME measurement
// against it, and require the OLD number.
//
// Reconstruction here is the two SQL statements rather than a source tree,
// because that is where the defect lived — the pre-change sweep differs from the
// current one only in the absence of the repeat-window predicate.
//
// It also carries the fixture guard aihub#287 needed. The control could pass for
// the wrong reason: if the legacy candidate query stopped selecting anything (a
// renamed column, a fixture that no longer represents an unclassified wi), it
// would emit 0 and "the old build behaves differently" would be trivially true
// while proving nothing. So the control additionally requires the legacy
// statements and the shipped sweep to agree on the FIRST tick — both emit
// exactly one alert for the same wi — which is only possible if the legacy copy
// really is the same sweep minus the guard.
func TestAlertSweep_MeasurementFailsOnPreChangeBuild(t *testing.T) {
	pool := setupAlertSweepTestDB(t)
	ctx := context.Background()
	seedUnclassifiedWI(t, pool, "wi_control", "aihub#266control")

	// FIXTURE, NOT CODE: the legacy statements must still select this fixture's
	// wi and emit for it, exactly as the shipped sweep does on a virgin table.
	if n := legacyEmit(t, pool, legacyUnclassifiedCandidateSQL, legacyUnclassifiedInsertSQL, legacyUnclassifiedProjectCol); n != 1 {
		t.Fatalf("FIXTURE, NOT CODE: the pre-change reconstruction emitted %d alerts on a "+
			"virgin fixture, want 1. Either work_items no longer has the columns the old "+
			"query names, or the fixture no longer represents an unclassified queued wi — "+
			"so this control reconstructs nothing and the measurement below is unguarded", n)
	}
	if _, err := pool.Exec(ctx, `DELETE FROM agent_events`); err != nil {
		t.Fatalf("reset events: %v", err)
	}
	if r := RunUnclassifiedWIAlert(ctx, pool); r.Affected != 1 {
		mustNotSkipAlertSweep(t, r)
		t.Fatalf("FIXTURE, NOT CODE: the shipped sweep emitted %d alerts on the same virgin "+
			"fixture where the legacy statements emitted 1. The two are not the same sweep, "+
			"so the comparison below is not measuring the guard", r.Affected)
	}
	if _, err := pool.Exec(ctx, `DELETE FROM agent_events`); err != nil {
		t.Fatalf("reset events: %v", err)
	}

	// The measurement, on the pre-change build.
	for i := 0; i < ticksPerDay; i++ {
		legacyEmit(t, pool, legacyUnclassifiedCandidateSQL, legacyUnclassifiedInsertSQL, legacyUnclassifiedProjectCol)
	}
	old := countEvents(t, pool, "wi_classification_missing")
	if old != ticksPerDay {
		t.Fatalf("the pre-change build measures %d events/day for one unclassified wi, "+
			"expected %d. The measurement cannot tell the old build from the new one, so "+
			"every no-duplicate assertion in this file proves nothing", old, ticksPerDay)
	}
	t.Logf("negative control: pre-change build = %d events/day for ONE unclassified wi "+
		"(%d unclassified wis measured in production x %d = the observed flood)",
		old, 77, old)

	// ...and the same fixture, same measurement, on the current build.
	if _, err := pool.Exec(ctx, `DELETE FROM agent_events`); err != nil {
		t.Fatalf("reset events: %v", err)
	}
	for i := 0; i < ticksPerDay; i++ {
		mustNotSkipAlertSweep(t, RunUnclassifiedWIAlert(ctx, pool))
	}
	if now := countEvents(t, pool, "wi_classification_missing"); now != 1 {
		t.Fatalf("the current build measures %d events/day, want 1", now)
	}
}

// ─── 3. REVERSE: the alert must still arrive ─────────────────────────────────

// TestAlertSweep_StillFiresOnceForAGenuinelyUnclassifiedWI is criterion 2 of
// aihub#266, the half that a duplicates-only test cannot fail.
//
// An implementation that emits nothing at all satisfies "no duplicates"
// perfectly. Each subtest below asserts a positive: exactly one alert exists,
// for the right work item, carrying the reason a reporter needs.
func TestAlertSweep_StillFiresOnceForAGenuinelyUnclassifiedWI(t *testing.T) {
	pool := setupAlertSweepTestDB(t)
	ctx := context.Background()
	seedUnclassifiedWI(t, pool, "wi_real", "aihub#266real")

	r := RunUnclassifiedWIAlert(ctx, pool)
	mustNotSkipAlertSweep(t, r)
	if r.Affected != 1 {
		t.Fatalf("the sweep reported Affected=%d for one genuinely unclassified wi, want 1", r.Affected)
	}

	var wiID, slug, reason string
	if err := pool.QueryRow(ctx, `
		SELECT work_item_id, payload->>'wi_slug', payload->>'reason'
		FROM agent_events WHERE event_type = 'wi_classification_missing'`).
		Scan(&wiID, &slug, &reason); err != nil {
		t.Fatalf("the alert row is missing or not unique: %v", err)
	}
	if wiID != "wi_real" || slug != "aihub#266real" {
		t.Errorf("alert names work item (%q, %q), want (wi_real, aihub#266real)", wiID, slug)
	}
	if reason == "" {
		t.Error("the alert payload carries no reason, so it tells the reporter nothing")
	}
}

// TestAlertSweep_FiresAgainOnceTheRepeatWindowHasPassed is the reverse probe
// that kills the degenerate fix.
//
// "Emit once, then never again" passes every duplicate assertion and every
// single-alert assertion above, and turns a live alert into a one-shot that goes
// permanently silent the first time it fires. The guard is a WINDOW, and this is
// the only test that can tell a window from a latch.
func TestAlertSweep_FiresAgainOnceTheRepeatWindowHasPassed(t *testing.T) {
	pool := setupAlertSweepTestDB(t)
	ctx := context.Background()
	seedUnclassifiedWI(t, pool, "wi_again", "aihub#266again")

	mustNotSkipAlertSweep(t, RunUnclassifiedWIAlert(ctx, pool))
	if got := countEvents(t, pool, "wi_classification_missing"); got != 1 {
		t.Fatalf("first run emitted %d, want 1", got)
	}

	// Still inside the window: nothing new.
	mustNotSkipAlertSweep(t, RunUnclassifiedWIAlert(ctx, pool))
	if got := countEvents(t, pool, "wi_classification_missing"); got != 1 {
		t.Fatalf("a second run inside the window emitted %d rows total, want 1", got)
	}

	// Age the existing alert just past the window, then sweep again.
	backdateEvents(t, pool, "wi_classification_missing", gcAlertRepeatWindow+time.Minute)
	r := RunUnclassifiedWIAlert(ctx, pool)
	mustNotSkipAlertSweep(t, r)
	if r.Affected != 1 {
		t.Errorf("once the repeat window had passed the sweep emitted %d alerts, want 1: "+
			"the guard is a latch, not a window, so this wi would never be alerted about "+
			"again", r.Affected)
	}
	if got := countEvents(t, pool, "wi_classification_missing"); got != 2 {
		t.Errorf("after the window elapsed there are %d alerts, want 2", got)
	}
}

// TestAlertSweep_GuardIsPerWorkItemNotGlobal is the third way the fix could be
// wrong while looking right: one alert anywhere suppressing alerts everywhere.
//
// With 77 unclassified wis in production and 45 of them queued, a global guard
// would silently alert about one of them and hide the other 76.
func TestAlertSweep_GuardIsPerWorkItemNotGlobal(t *testing.T) {
	pool := setupAlertSweepTestDB(t)
	ctx := context.Background()
	seedUnclassifiedWI(t, pool, "wi_first", "aihub#266first")

	mustNotSkipAlertSweep(t, RunUnclassifiedWIAlert(ctx, pool))

	// A second wi becomes unclassified while the first one's alert is fresh.
	seedUnclassifiedWI(t, pool, "wi_second", "aihub#266second")
	r := RunUnclassifiedWIAlert(ctx, pool)
	mustNotSkipAlertSweep(t, r)

	if r.Affected != 1 {
		t.Errorf("the sweep emitted %d alerts for a newly unclassified wi while another "+
			"wi's alert was inside the window, want 1", r.Affected)
	}
	if got := countEventsFor(t, pool, "wi_classification_missing", "wi_second"); got != 1 {
		t.Errorf("the new wi has %d alerts, want 1: the guard suppresses per database "+
			"instead of per work item", got)
	}
	if got := countEventsFor(t, pool, "wi_classification_missing", "wi_first"); got != 1 {
		t.Errorf("the first wi has %d alerts, want 1", got)
	}
}

// TestAlertSweep_DoesNotAlertWorkItemsOutsideItsPredicate checks the guard did
// not arrive alongside a widened predicate. requires_human_session = false is
// CLASSIFIED (it is the answer "no human needed"), and NULL is the unclassified
// third state; a sweep that confused them would alert about every wi in the
// database.
func TestAlertSweep_DoesNotAlertWorkItemsOutsideItsPredicate(t *testing.T) {
	pool := setupAlertSweepTestDB(t)
	ctx := context.Background()

	no, yes := false, true
	seedAlertWI(t, pool, "wi_classified_false", "aihub#266false", &no, 5, "high")
	seedAlertWI(t, pool, "wi_classified_true", "aihub#266true", &yes, 5, "high")
	seedAlertWI(t, pool, "wi_too_new", "aihub#266new", nil, 0, "high")

	mustNotSkipAlertSweep(t, RunUnclassifiedWIAlert(ctx, pool))
	if got := countEvents(t, pool, "wi_classification_missing"); got != 0 {
		t.Errorf("the sweep emitted %d classification alerts for wis that are classified or "+
			"younger than a day, want 0", got)
	}

	// And the positive control on the same fixture, so a predicate that matches
	// nothing at all cannot pass this test.
	seedUnclassifiedWI(t, pool, "wi_unclassified", "aihub#266unclassified")
	if r := RunUnclassifiedWIAlert(ctx, pool); r.Affected != 1 {
		t.Errorf("on the same fixture a genuinely unclassified wi produced %d alerts, "+
			"want 1", r.Affected)
	}
}

// ─── 4. The other sweeps must still run ──────────────────────────────────────

// TestAlertSweep_RunDueStillDrivesEverySweep is the rest of criterion 2: the
// cadence change must not have taken the other six sweeps with it.
//
// It counts, per sweep, how many times RunDue actually invoked it across 90
// minute-spaced ticks. The six every-tick sweeps must be invoked on all 90; the
// two daily ones exactly once. Against this bare fixture the six report errors
// (no resource_locks, no memories) — irrelevant here, because what is being
// measured is whether RunDue called them at all, which is exactly what a blanket
// period change would break.
func TestAlertSweep_RunDueStillDrivesEverySweep(t *testing.T) {
	pool := setupAlertSweepTestDB(t)
	ctx := context.Background()
	seedUnclassifiedWI(t, pool, "wi_rundue", "aihub#266rundue")
	seedAgedHumanSessionWI(t, pool, "wi_rundue_aged", "aihub#266runduaged")

	const ticks = 90
	start := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	sched := newGCSchedule()

	invocations := map[string]int{}
	for i := 0; i < ticks; i++ {
		for _, r := range runDueAt(ctx, pool, sched, start.Add(time.Duration(i)*time.Minute)) {
			invocations[r.SweepType]++
		}
	}

	// One run at tick 0, then one per gcAlertPollPeriod. Derived, not written as
	// a literal, so the expectation cannot drift away from the constant.
	pollMinutes := int(gcAlertPollPeriod / time.Minute)
	throttled := (ticks-1)/pollMinutes + 1

	want := map[string]int{
		sweepOrphanLockCleanup:      ticks,
		sweepMemoryExpiredArchive:   ticks,
		sweepMethodologyExpiry:      ticks,
		sweepEventPayloadTruncation: ticks,
		sweepUnblockDependentWI:     ticks,
		sweepPartitionCreate:        ticks,
		sweepNeedsHumanSessionAging: throttled,
		sweepUnclassifiedWIAlert:    throttled,
	}
	if throttled >= ticks {
		t.Fatalf("FIXTURE, NOT CODE: gcAlertPollPeriod (%v) is short enough that a throttled "+
			"sweep runs on every one of %d ticks, so this test can no longer tell a throttled "+
			"sweep from an every-tick one", gcAlertPollPeriod, ticks)
	}
	for name, wantN := range want {
		if got := invocations[name]; got != wantN {
			t.Errorf("RunDue invoked %s %d times across %d ticks, want %d",
				name, got, ticks, wantN)
		}
	}
	for name := range invocations {
		if _, known := want[name]; !known {
			t.Errorf("RunDue invoked an unexpected sweep %q", name)
		}
	}

	// Both alerts still landed exactly once — the sweeps ran, they did not just
	// get called, and the repeat window held across the throttled runs.
	for _, ev := range []string{"wi_classification_missing", "wi_needs_attention"} {
		if got := countEvents(t, pool, ev); got != 1 {
			t.Errorf("%d %s rows after %d ticks (%d throttled sweep runs), want 1",
				got, ev, ticks, throttled)
		}
	}
}

// TestAlertSweep_RunAllIgnoresTheScheduleButStillCannotDuplicate pins the split
// between the two fixes at the admin endpoint.
//
// POST /v1/admin/gc calls RunAll, and an operator asking for a sweep run must
// get one rather than a silent no-op because the daily sweeps ran this morning.
// That is only safe because the idempotency guard is in the SQL and not in the
// schedule: RunAll invokes the sweep on every call, and still cannot produce a
// second row.
func TestAlertSweep_RunAllIgnoresTheScheduleButStillCannotDuplicate(t *testing.T) {
	pool := setupAlertSweepTestDB(t)
	ctx := context.Background()
	seedUnclassifiedWI(t, pool, "wi_admin", "aihub#266admin")

	table := gcSweepTable()
	for call := 1; call <= 3; call++ {
		results := RunAll(ctx, pool)
		if len(results) != len(table) {
			t.Fatalf("RunAll call %d returned %d results, want %d — the admin path is being "+
				"throttled by the schedule", call, len(results), len(table))
		}
		found := false
		for _, r := range results {
			if r.SweepType == sweepUnclassifiedWIAlert {
				found = true
			}
		}
		if !found {
			t.Fatalf("RunAll call %d did not invoke %s", call, sweepUnclassifiedWIAlert)
		}
	}

	if got := countEvents(t, pool, "wi_classification_missing"); got != 1 {
		t.Errorf("three forced RunAll calls produced %d alerts, want 1: the guard is in the "+
			"schedule rather than in the SQL, so every schedule-bypassing caller reopens "+
			"the flood", got)
	}
}

// TestAlertSweep_WindowArgIsAnIntervalPostgresAgrees closes the last gap between
// the Go constant and the database: gcAlertRepeatWindowArg is a string that Go
// never validates. If Postgres parsed it differently — or not at all — the guard
// would either error on every run or silently use the wrong window.
func TestAlertSweep_WindowArgIsAnIntervalPostgresAgrees(t *testing.T) {
	pool := setupAlertSweepTestDB(t)

	var secs float64
	if err := pool.QueryRow(context.Background(),
		`SELECT extract(epoch FROM $1::interval)`, gcAlertRepeatWindowArg).Scan(&secs); err != nil {
		t.Fatalf("Postgres rejected gcAlertRepeatWindowArg (%q): %v", gcAlertRepeatWindowArg, err)
	}
	if want := gcAlertRepeatWindow.Seconds(); secs != want {
		t.Fatalf("Postgres reads %q as %v seconds, but gcAlertRepeatWindow is %v seconds",
			gcAlertRepeatWindowArg, secs, want)
	}
	t.Logf("repeat window: %q = %v seconds, matching gcAlertRepeatWindow (%v)",
		gcAlertRepeatWindowArg, secs, gcAlertRepeatWindow)
}

// TestAlertSweep_GuardIsScopedPerEventTypeNotPerWorkItem closes a hole a
// mutation run found: dropping `e.event_type = ...` from the guard leaves every
// other probe in this file green.
//
// It survives them because the two sweeps' wi predicates are disjoint at any one
// instant — requires_human_session IS NULL versus = true — so no fixture here
// ever has one wi eligible for both alerts at once. A wi can still hold both
// event types inside one window by moving between the states: alerted while
// unclassified, then classified to true, then aged past the 7-day threshold. With
// an event-type-blind guard the stale wi_classification_missing row would
// suppress the wi_needs_attention alert, and the wi would silently stop being
// reported.
func TestAlertSweep_GuardIsScopedPerEventTypeNotPerWorkItem(t *testing.T) {
	pool := setupAlertSweepTestDB(t)
	ctx := context.Background()

	// An unclassified wi that already carries a *different* alert type, fresh
	// enough to be inside the repeat window.
	seedUnclassifiedWI(t, pool, "wi_crosstype", "aihub#266crosstype")
	if _, err := pool.Exec(ctx, `
		INSERT INTO agent_events (id, work_item_id, event_type, payload, project, created_at)
		VALUES ($1, 'wi_crosstype', 'wi_needs_attention', '{}', 'aihub', clock_timestamp())`,
		NewID("evt")); err != nil {
		t.Fatalf("seed cross-type event: %v", err)
	}

	r := RunUnclassifiedWIAlert(ctx, pool)
	mustNotSkipAlertSweep(t, r)
	if r.Affected != 1 {
		t.Errorf("a wi holding a fresh wi_needs_attention row got %d wi_classification_missing "+
			"alerts, want 1: the guard matches any event type, so one alert type suppresses "+
			"the other", r.Affected)
	}
	if got := countEventsFor(t, pool, "wi_classification_missing", "wi_crosstype"); got != 1 {
		t.Errorf("the wi has %d wi_classification_missing rows, want 1", got)
	}

	// And the reverse direction, so the assertion is not one-sided.
	if got := countEventsFor(t, pool, "wi_needs_attention", "wi_crosstype"); got != 1 {
		t.Errorf("the seeded wi_needs_attention row count is %d, want 1 — the sweep touched "+
			"an event type that is not its own", got)
	}
}

// TestAlertSweep_RunDueSharesOneProcessWideSchedule closes the second hole the
// same mutation run found: every other schedule assertion in this package injects
// its own gcSchedule into runDueAt, so replacing RunDue's process-wide
// gcTickSchedule with a fresh one per call — restoring 1,440 runs a day — left
// the whole suite green.
//
// The assertion is deliberately "not in both calls" rather than "in exactly the
// first": gcTickSchedule is process state that this test consumes, and pinning
// the first call would make the test depend on being the only one to reach
// RunDue and on running just once (`-count=2` would fail it). "At most one of two
// back-to-back calls" is true whatever ran before, and is still false the moment
// the schedule stops being shared.
func TestAlertSweep_RunDueSharesOneProcessWideSchedule(t *testing.T) {
	pool := setupAlertSweepTestDB(t)
	ctx := context.Background()
	seedUnclassifiedWI(t, pool, "wi_shared", "aihub#266shared")

	ran := func(results []GCResult, name string) bool {
		for _, r := range results {
			if r.SweepType == name {
				return true
			}
		}
		return false
	}

	first := RunDue(ctx, pool)
	second := RunDue(ctx, pool)

	for _, daily := range []string{sweepUnclassifiedWIAlert, sweepNeedsHumanSessionAging} {
		if ran(first, daily) && ran(second, daily) {
			t.Errorf("RunDue ran %s on two back-to-back calls: its schedule is not shared "+
				"across calls, so the daily period never throttles anything and the "+
				"production ticker is back to %d runs a day", daily, ticksPerDay)
		}
	}
	// Reverse: the every-tick sweeps must run on BOTH calls, or RunDue is
	// throttling the six sweeps that should never be throttled.
	for _, tick := range []string{sweepOrphanLockCleanup, sweepPartitionCreate} {
		if !ran(first, tick) || !ran(second, tick) {
			t.Errorf("RunDue did not run %s on both back-to-back calls", tick)
		}
	}
}

// TestAlertSweep_RunDueRetriesADailySweepThatFailed pins where the daily clock
// starts: on a run that COMPLETED, not on one that was attempted.
//
// The tempting implementation is a single claim() that checks and marks in one
// step. It is wrong in a way nothing else here can see: one transient database
// error at the moment a daily sweep came due would buy 24 hours of silence, and
// an instance that lost the advisory-lock race would stop trying even if the
// winner never finished. Both are invisible to every duplicate assertion, because
// both make the sweep emit LESS.
//
// The fixture induces a real failure — no work_items relation — so the sweep's
// candidate query errors rather than the test faking a GCResult.
func TestAlertSweep_RunDueRetriesADailySweepThatFailed(t *testing.T) {
	pool := setupAlertSweepTestDB(t)
	ctx := context.Background()
	seedUnclassifiedWI(t, pool, "wi_retry", "aihub#266retry")

	start := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	sched := newGCSchedule()

	countRuns := func(ticks int, from time.Time) (int, string) {
		runs, lastErr := 0, ""
		for i := 0; i < ticks; i++ {
			for _, r := range runDueAt(ctx, pool, sched, from.Add(time.Duration(i)*time.Minute)) {
				if r.SweepType == sweepUnclassifiedWIAlert {
					runs++
					lastErr = r.Error
				}
			}
		}
		return runs, lastErr
	}

	// Break the sweep, then drive five ticks. A failing daily sweep must be
	// attempted on every one of them.
	if _, err := pool.Exec(ctx, `ALTER TABLE work_items RENAME TO work_items_hidden`); err != nil {
		t.Fatalf("hide work_items: %v", err)
	}
	runs, lastErr := countRuns(5, start)
	if lastErr == "" {
		t.Fatalf("FIXTURE, NOT CODE: the sweep reported no error with work_items renamed away, "+
			"so this test is not measuring the failure path at all (runs=%d)", runs)
	}
	if runs != 5 {
		t.Errorf("a failing daily sweep was attempted %d times across 5 ticks, want 5: the "+
			"schedule starts its 24h clock on an ATTEMPT, so one transient error silences "+
			"the alert for a full day", runs)
	}
	if got := countEvents(t, pool, "wi_classification_missing"); got != 0 {
		t.Errorf("%d alerts emitted while the sweep was failing, want 0", got)
	}

	// Repair it. The next tick must succeed and emit, and the tick after that
	// must be throttled — the clock starts now, not five ticks ago.
	if _, err := pool.Exec(ctx, `ALTER TABLE work_items_hidden RENAME TO work_items`); err != nil {
		t.Fatalf("restore work_items: %v", err)
	}
	resumeAt := start.Add(10 * time.Minute)
	runs, lastErr = countRuns(30, resumeAt)
	if lastErr != "" {
		t.Fatalf("the sweep still errors after the fixture was repaired: %s", lastErr)
	}
	if runs != 1 {
		t.Errorf("after recovery the daily sweep ran %d times across 30 ticks, want 1", runs)
	}
	if got := countEvents(t, pool, "wi_classification_missing"); got != 1 {
		t.Errorf("%d alerts after recovery, want exactly 1 — the alert the failing runs owed", got)
	}
}

// TestAlertSweep_RecognisesFloodRowsWrittenByTheOldBuild is the deploy
// transition, which no other test here covers: every probe above starts from an
// empty agent_events, but on the day this ships the table already holds the
// flood — 111,221 rows for ieops#84 alone, all written by the pre-change build.
//
// The guard must recognise rows it did not write. It keys only on
// (work_item_id, event_type, created_at), so it should — but "should" is the
// word that precedes the defects in this file. A guard that had keyed on
// anything the old rows lack, a payload marker or an id prefix, would pass every
// test above and then re-flood on the first tick after deploy, which is the one
// moment nobody would be watching for it.
func TestAlertSweep_RecognisesFloodRowsWrittenByTheOldBuild(t *testing.T) {
	pool := setupAlertSweepTestDB(t)
	ctx := context.Background()
	seedUnclassifiedWI(t, pool, "wi_flooded", "aihub#266flooded")

	// One day of the pre-change build's output for this wi, written by the
	// verbatim legacy statements rather than by anything under test.
	for i := 0; i < ticksPerDay; i++ {
		legacyEmit(t, pool, legacyUnclassifiedCandidateSQL, legacyUnclassifiedInsertSQL, legacyUnclassifiedProjectCol)
	}
	before := countEvents(t, pool, "wi_classification_missing")
	if before != ticksPerDay {
		t.Fatalf("FIXTURE, NOT CODE: the pre-deploy flood is %d rows, want %d", before, ticksPerDay)
	}

	// Deploy: the new build's first day of ticks must add NOTHING, because the
	// newest legacy row is inside the repeat window.
	for i := 0; i < ticksPerDay; i++ {
		mustNotSkipAlertSweep(t, RunUnclassifiedWIAlert(ctx, pool))
	}
	if got := countEvents(t, pool, "wi_classification_missing"); got != before {
		t.Errorf("a day of post-deploy ticks added %d rows on top of an existing flood, want 0: "+
			"the guard does not recognise rows written by the previous build, so shipping this "+
			"would keep flooding", got-before)
	}

	// ...and once the window has passed, the wi is alerted about again exactly
	// once. Without this half, "adds nothing" would also be satisfied by a build
	// that never alerts about an already-flooded wi again.
	backdateEvents(t, pool, "wi_classification_missing", gcAlertRepeatWindow+time.Minute)
	r := RunUnclassifiedWIAlert(ctx, pool)
	mustNotSkipAlertSweep(t, r)
	if r.Affected != 1 {
		t.Errorf("after the window passed, an already-flooded wi got %d alerts, want 1", r.Affected)
	}
	if got := countEvents(t, pool, "wi_classification_missing"); got != before+1 {
		t.Errorf("total rows %d, want %d", got, before+1)
	}
}

// TestAlertSweep_AlertsAWorkItemWhoseWITypeIsNull covers the one nullable column
// either sweep reads.
//
// work_items.wi_type is `TEXT` with no NOT NULL and no default (migration 0002),
// and nothing couples it to requires_human_session — so "queued, needs a human,
// no wi_type" is a legal row, and an obvious one: it is a wi someone flagged
// before classifying it. Sweep 7 selects wi_type and used to scan it into a
// string.
//
// Before aihub#266 that failed the Scan and the row was dropped by
// `if err == nil`, so the wi was silently never alerted about. Making errors
// visible turned that into something worse: the error reaches GCResult.Error, so
// runDueAt refuses to record the run, and the sweep retries and re-logs on every
// tick forever while still never alerting. A test that only ever seeds
// wi_type='fix_bug' cannot see either version.
func TestAlertSweep_AlertsAWorkItemWhoseWITypeIsNull(t *testing.T) {
	pool := setupAlertSweepTestDB(t)
	ctx := context.Background()

	// Queued, requires_human_session = true, 10 days old, wi_type NULL.
	rhs := true
	if _, err := pool.Exec(ctx, `
		INSERT INTO work_items
		  (id, slug, project, wi_type, priority, status, reporter_user_id,
		   requires_human_session, created_at)
		VALUES ('wi_notype', 'aihub#266notype', 'aihub', NULL, 'high', 'queued', 'u_test',
		        $1, clock_timestamp() - make_interval(days => 10))`, rhs); err != nil {
		t.Fatalf("seed wi with NULL wi_type: %v", err)
	}

	r := RunNeedsHumanSessionAging(ctx, pool)
	if r.Error != "" {
		t.Fatalf("the sweep errored on a legal row with wi_type NULL: %s\n"+
			"This is not merely a dropped alert: runDueAt does not record a run that "+
			"errored, so the sweep retries and logs this on every 60s tick indefinitely", r.Error)
	}
	mustNotSkipAlertSweep(t, r)
	if r.Affected != 1 {
		t.Fatalf("a queued requires_human_session=true wi aged past its threshold with "+
			"wi_type NULL got %d alerts, want 1", r.Affected)
	}

	// The payload must carry the NULL honestly rather than inventing a value.
	var wiType *string
	var slug string
	if err := pool.QueryRow(ctx, `
		SELECT payload->>'wi_type', payload->>'wi_slug'
		FROM agent_events WHERE event_type = 'wi_needs_attention'`).Scan(&wiType, &slug); err != nil {
		t.Fatalf("read alert payload: %v", err)
	}
	if wiType != nil {
		t.Errorf("payload wi_type = %q for a wi whose wi_type is NULL, want JSON null", *wiType)
	}
	if slug != "aihub#266notype" {
		t.Errorf("payload wi_slug = %q, want aihub#266notype", slug)
	}

	// And a wi that DOES have a wi_type still reports it, so the pointer change
	// did not simply stop carrying the field.
	seedAgedHumanSessionWI(t, pool, "wi_typed", "aihub#266typed")
	if r := RunNeedsHumanSessionAging(ctx, pool); r.Affected != 1 || r.Error != "" {
		t.Fatalf("typed wi: Affected=%d Error=%q, want 1 and none", r.Affected, r.Error)
	}
	var typed string
	if err := pool.QueryRow(ctx, `
		SELECT payload->>'wi_type' FROM agent_events
		WHERE event_type = 'wi_needs_attention' AND work_item_id = 'wi_typed'`).Scan(&typed); err != nil {
		t.Fatalf("read typed alert payload: %v", err)
	}
	if typed != "fix_bug" {
		t.Errorf("payload wi_type = %q for a wi typed fix_bug", typed)
	}
}

// TestAlertSweep_RestartAtAnUnluckyPhaseStillAlertsWithinADay is the reason the
// poll period is an hour rather than a day.
//
// A schedule period close to the repeat window makes the schedule's PHASE decide
// the delivered cadence, and a restart randomises the phase. With the 24h period
// an earlier draft of this change used: a process restarting 22h after the last
// alert runs the sweep, the 23h window correctly suppresses it, the schedule
// records the run anyway, and the next run is 24h after THAT — the wi waits
// ~46h. That is the same 2x-period failure the window was sized to avoid,
// reached from the other side, and every duplicate-counting test in this file
// stays green through it.
//
// Two clocks have to move together here, which is the trap this probe is built
// around. The schedule reads the injected `now`; the repeat window reads
// agent_events.created_at against the DATABASE's clock. Advancing only the
// injected one leaves every alert permanently "just written" and the window
// never expires — so `advance` moves both: it back-dates the existing rows by d
// and steps the simulated clock by the same d.
func TestAlertSweep_RestartAtAnUnluckyPhaseStillAlertsWithinADay(t *testing.T) {
	pool := setupAlertSweepTestDB(t)
	ctx := context.Background()
	seedUnclassifiedWI(t, pool, "wi_phase", "aihub#266phase")

	now := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	elapsed := time.Duration(0)
	advance := func(d time.Duration) {
		backdateEvents(t, pool, "wi_classification_missing", d)
		now = now.Add(d)
		elapsed += d
	}

	// Alert #1, from a fresh process.
	sched := newGCSchedule()
	if !sched.due(sweepUnclassifiedWIAlert, gcAlertPollPeriod, now) {
		t.Fatal("a fresh schedule was not due")
	}
	mustNotSkipAlertSweep(t, RunUnclassifiedWIAlert(ctx, pool))
	sched.record(sweepUnclassifiedWIAlert, gcAlertPollPeriod, now)
	if countEvents(t, pool, "wi_classification_missing") != 1 {
		t.Fatal("first alert did not land")
	}

	// Restart at the worst possible phase: one minute before the window expires,
	// so the run it triggers emits nothing and yet is recorded.
	advance(gcAlertRepeatWindow - time.Minute)
	sched = newGCSchedule()

	r := RunUnclassifiedWIAlert(ctx, pool)
	mustNotSkipAlertSweep(t, r)
	sched.record(sweepUnclassifiedWIAlert, gcAlertPollPeriod, now)
	if r.Affected != 0 {
		t.Fatalf("FIXTURE, NOT CODE: the post-restart run one minute inside the window "+
			"emitted %d alerts, want 0. The unlucky phase this probe exists to walk was "+
			"never reached", r.Affected)
	}

	// Now poll minute by minute until alert #2 lands, moving both clocks.
	landed := false
	for i := 0; i < 2*ticksPerDay && !landed; i++ {
		advance(time.Minute)
		if !sched.due(sweepUnclassifiedWIAlert, gcAlertPollPeriod, now) {
			continue
		}
		r := RunUnclassifiedWIAlert(ctx, pool)
		mustNotSkipAlertSweep(t, r)
		if r.Error == "" && !r.Skipped {
			sched.record(sweepUnclassifiedWIAlert, gcAlertPollPeriod, now)
		}
		landed = countEvents(t, pool, "wi_classification_missing") == 2
	}

	if !landed {
		t.Fatalf("no second alert within two days of a restart at the worst phase "+
			"(elapsed %v)", elapsed)
	}
	t.Logf("restart one minute inside the window: alert-to-alert gap = %v "+
		"(window %v, poll %v, specified cadence %v)",
		elapsed, gcAlertRepeatWindow, gcAlertPollPeriod, gcSpecifiedAlertCadence)

	if elapsed > gcSpecifiedAlertCadence {
		t.Errorf("a restart at an unlucky phase stretched the alert gap to %v, past the "+
			"specified %v: the schedule's phase, not the repeat window, is deciding how "+
			"often this wi is alerted about", elapsed, gcSpecifiedAlertCadence)
	}
	if elapsed < gcAlertRepeatWindow {
		t.Errorf("alert gap %v is shorter than the repeat window %v, so the window is not "+
			"actually suppressing", elapsed, gcAlertRepeatWindow)
	}
}
