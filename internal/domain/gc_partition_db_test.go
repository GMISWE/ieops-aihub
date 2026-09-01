package domain

// DB-backed tests for the agent_events partition creator (aihub#268).
//
// The pure-unit tests in gc_test.go pin partitionMonthsAhead's month arithmetic
// and the shape of the DDL string. They cannot reach the parts of this sweep that
// only Postgres can answer, and review F5 called that out: deleting the whole
// pg_inherits verification block, or main.go's error logging, left `go test ./...`
// green. Everything below needs a real server — the DEFAULT-partition drain, the
// catalog-verified attachment, error accumulation, the audit event, and the
// backlog alarm.
//
// Follows the AIHUB_TEST_DB gating pattern (memory_latest_test.go), so it never
// runs in a plain `go test ./...`. Unlike the other gated tests here it does NOT
// need the migrated schema: it builds a self-contained agent_events in its own
// schema, so any empty Postgres will do and pgvector is not required.
//
//	AIHUB_TEST_DB='postgres://postgres@/postgres?host=/path/to/socket&port=5599' \
//	go test ./internal/domain/ -run TestRunPartitionCreate -v -count=1
//
// Two caveats:
//   - Always pass `-run TestRunPartitionCreate` (or the name of one test here).
//     The OTHER AIHUB_TEST_DB-gated tests in this package DO need the fully
//     migrated schema, so pointing AIHUB_TEST_DB at a bare database and running
//     the whole package turns their skips into `relation "users" does not exist`
//     failures.
//   - RunPartitionCreate takes advisory lock 2006, which is database-wide. Point
//     AIHUB_TEST_DB at a database no live aihub instance is connected to, or the
//     sweep returns Skipped and these tests fail telling you so.

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/GMISWE/ieops-aihub/internal/citest/testname"
)

// agentEventsFixtureDDL mirrors migration 0006's agent_events closely enough for
// partition routing and the whitelist CHECK to behave identically: composite PK
// including the partition key, parent-level indexes, and the event_type
// whitelist. Foreign keys to users/work_items/run_attempts are omitted — the
// sweep only ever inserts rows with a NULL work_item_id, so they are not
// load-bearing here, and leaving them out is what lets this fixture stand alone.
const agentEventsFixtureDDL = `
CREATE TABLE agent_events (
    id             TEXT NOT NULL,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (id, created_at),
    work_item_id   TEXT,
    run_attempt_id TEXT,
    actor_user_id  TEXT,
    actor_display  TEXT,
    api_key_id     TEXT,
    project        TEXT,
    event_type     TEXT NOT NULL,
    payload        JSONB NOT NULL DEFAULT '{}',
    pinned         BOOLEAN NOT NULL DEFAULT FALSE,
    CONSTRAINT chk_evt_work_item_id CHECK (
        work_item_id IS NOT NULL
        OR event_type IN ('phase_config_updated', 'admin_redact', 'admin_unblock',
                          'system_gc', 'system_force_takeover', 'memory_gc',
                          'partition_created')
    )
) PARTITION BY RANGE (created_at);

CREATE INDEX idx_evt_wi_time ON agent_events(work_item_id, created_at DESC);
CREATE INDEX idx_evt_type_time ON agent_events(event_type, created_at DESC);
`

// setupPartitionTestDB gives each subtest its own schema containing a fresh,
// empty agent_events. search_path is pinned on every pooled connection, so the
// sweep's unqualified `agent_events` and its `'agent_events'::regclass` lookup
// both resolve here and nowhere near real data.
func setupPartitionTestDB(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dbURL := os.Getenv("AIHUB_TEST_DB")
	if dbURL == "" {
		t.Skip("set AIHUB_TEST_DB to run this integration test")
	}

	schema := "pf268_" + testname.Sanitize(t.Name())
	ctx := context.Background()

	cfg, err := pgxpool.ParseConfig(dbURL)
	if err != nil {
		t.Fatalf("parse AIHUB_TEST_DB: %v", err)
	}
	cfg.AfterConnect = func(ctx context.Context, conn *pgx.Conn) error {
		// Never fall through to public: a bug that dropped the schema qualifier
		// must fail loudly here, not silently touch a real agent_events.
		_, err := conn.Exec(ctx, `SET search_path = `+schema)
		return err
	}

	// Bootstrap the schema on a connection that predates the search_path hook.
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
		t.Fatalf("create fixture: %v", err)
	}

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

// attachDefaultPartition applies what migration 0031 does.
func attachDefaultPartition(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	if _, err := pool.Exec(context.Background(),
		`CREATE TABLE agent_events_default PARTITION OF agent_events DEFAULT`); err != nil {
		t.Fatalf("attach default partition: %v", err)
	}
}

// insertEventAt writes one event at an explicit instant, returning the error so
// callers can assert on the failure path too.
func insertEventAt(pool *pgxpool.Pool, id string, at time.Time) error {
	_, err := pool.Exec(context.Background(), `
		INSERT INTO agent_events (id, created_at, work_item_id, event_type, project)
		VALUES ($1, $2, 'wi_test', 'note', 'aihub')`, id, at)
	return err
}

func countRowsIn(t *testing.T, pool *pgxpool.Pool, table string) int64 {
	t.Helper()
	var n int64
	if err := pool.QueryRow(context.Background(),
		`SELECT count(*) FROM `+table).Scan(&n); err != nil {
		t.Fatalf("count %s: %v", table, err)
	}
	return n
}

// partitionOf reports which partition a given row actually landed in.
func partitionOf(t *testing.T, pool *pgxpool.Pool, id string) string {
	t.Helper()
	var rel string
	if err := pool.QueryRow(context.Background(),
		`SELECT tableoid::regclass::text FROM agent_events WHERE id = $1`, id).Scan(&rel); err != nil {
		t.Fatalf("locate row %s: %v", id, err)
	}
	// Strip the schema qualifier the search_path adds for non-current schemas.
	if i := strings.LastIndex(rel, "."); i >= 0 {
		rel = rel[i+1:]
	}
	return rel
}

func mustNotSkip(t *testing.T, r GCResult) {
	t.Helper()
	if r.Skipped {
		t.Fatalf("sweep skipped: advisory lock 2006 is held elsewhere — point AIHUB_TEST_DB at a database with no live aihub instance")
	}
}

func TestRunPartitionCreate_CreatesTheLookaheadWindow(t *testing.T) {
	pool := setupPartitionTestDB(t)
	ctx := context.Background()

	// No monthly partitions at all: the sweep must build the whole window.
	res := RunPartitionCreate(ctx, pool)
	mustNotSkip(t, res)
	if res.Error != "" {
		t.Fatalf("unexpected error: %s", res.Error)
	}

	want := int64(partitionLookaheadMonths + 1)
	if res.Affected != want {
		t.Errorf("Affected = %d, want %d — a CREATE TABLE command tag reports 0 rows, so this "+
			"number can only come from counting real attachments", res.Affected, want)
	}

	// Every month in the window must now be attached, by the names the pure-unit
	// tests pin.
	for _, spec := range partitionMonthsAhead(time.Now().UTC(), partitionLookaheadMonths) {
		attached, err := isAttachedPartition(ctx, pool, spec.Name)
		if err != nil {
			t.Fatalf("isAttachedPartition(%s): %v", spec.Name, err)
		}
		if !attached {
			t.Errorf("%s was not attached", spec.Name)
		}
	}

	// The audit event that was designed in 0006 and never once written.
	var events int64
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM agent_events WHERE event_type = 'partition_created'`).Scan(&events); err != nil {
		t.Fatalf("count partition_created: %v", err)
	}
	if events != want {
		t.Errorf("partition_created events = %d, want %d (one per created partition)", events, want)
	}
	// It carries a NULL work_item_id, so it must satisfy chk_evt_work_item_id.
	var nullWI int64
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM agent_events
		WHERE event_type = 'partition_created' AND work_item_id IS NULL`).Scan(&nullWI); err != nil {
		t.Fatalf("count NULL-wi events: %v", err)
	}
	if nullWI != want {
		t.Errorf("partition_created rows with NULL work_item_id = %d, want %d", nullWI, want)
	}

	// A second run must be a clean no-op: nothing created, nothing reported.
	again := RunPartitionCreate(ctx, pool)
	mustNotSkip(t, again)
	if again.Affected != 0 || again.Error != "" {
		t.Errorf("second run: Affected = %d, Error = %q; want 0 and empty", again.Affected, again.Error)
	}
}

func TestRunPartitionCreate_WritesLandInRealPartitionsAfterwards(t *testing.T) {
	pool := setupPartitionTestDB(t)
	ctx := context.Background()

	// This is the reported outage: no partition covers the row.
	future := time.Now().UTC().AddDate(0, 1, 15)
	err := insertEventAt(pool, "evt_before", future)
	if err == nil {
		t.Fatal("expected the pre-fix insert to fail with no-partition-found")
	}
	if !strings.Contains(err.Error(), "no partition of relation") {
		t.Fatalf("expected a no-partition error, got: %v", err)
	}

	mustNotSkip(t, RunPartitionCreate(ctx, pool))

	if err := insertEventAt(pool, "evt_after", future); err != nil {
		t.Fatalf("insert after sweep: %v", err)
	}
	spec := partitionMonthsAhead(future, 0)[0]
	if got := partitionOf(t, pool, "evt_after"); got != spec.Name {
		t.Errorf("row landed in %s, want %s", got, spec.Name)
	}
}

func TestRunPartitionCreate_DrainsRowsStrandedInDefault(t *testing.T) {
	pool := setupPartitionTestDB(t)
	ctx := context.Background()

	// The state after the sweep has been failing for a while: only the safety net
	// is catching writes.
	attachDefaultPartition(t, pool)

	now := time.Now().UTC()
	thisMonth := now.AddDate(0, 0, 0)
	nextMonth := time.Date(now.Year(), now.Month(), 15, 0, 0, 0, 0, time.UTC).AddDate(0, 1, 0)
	for id, at := range map[string]time.Time{
		"evt_a": time.Date(thisMonth.Year(), thisMonth.Month(), 2, 3, 4, 5, 0, time.UTC),
		"evt_b": nextMonth,
	} {
		if err := insertEventAt(pool, id, at); err != nil {
			t.Fatalf("seed %s: %v", id, err)
		}
	}
	if got := countRowsIn(t, pool, agentEventsDefaultPartition); got != 2 {
		t.Fatalf("seeded rows did not land in DEFAULT: %d there", got)
	}

	res := RunPartitionCreate(ctx, pool)
	mustNotSkip(t, res)
	if res.Error != "" {
		t.Fatalf("drain reported an error: %s", res.Error)
	}
	if res.Affected != int64(partitionLookaheadMonths+1) {
		t.Errorf("Affected = %d, want %d", res.Affected, partitionLookaheadMonths+1)
	}

	// Rows conserved, relocated, and gone from DEFAULT. Count only the seeded
	// rows: the sweep's own partition_created events live in this table too.
	var seeded int64
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM agent_events WHERE event_type = 'note'`).Scan(&seeded); err != nil {
		t.Fatalf("count seeded rows: %v", err)
	}
	if seeded != 2 {
		t.Errorf("seeded rows = %d, want 2 — the drain must move rows, never drop or duplicate them", seeded)
	}
	if left := countRowsIn(t, pool, agentEventsDefaultPartition); left != 0 {
		t.Errorf("%s still holds %d rows after the drain", agentEventsDefaultPartition, left)
	}
	for id, at := range map[string]time.Time{
		"evt_a": time.Date(thisMonth.Year(), thisMonth.Month(), 2, 0, 0, 0, 0, time.UTC),
		"evt_b": nextMonth,
	} {
		want := partitionMonthsAhead(at, 0)[0].Name
		if got := partitionOf(t, pool, id); got != want {
			t.Errorf("%s landed in %s, want %s", id, got, want)
		}
	}

	// Column values must survive the positional `SELECT * FROM moved`.
	var evType, wi, project string
	if err := pool.QueryRow(ctx,
		`SELECT event_type, work_item_id, project FROM agent_events WHERE id = 'evt_a'`).
		Scan(&evType, &wi, &project); err != nil {
		t.Fatalf("read moved row: %v", err)
	}
	if evType != "note" || wi != "wi_test" || project != "aihub" {
		t.Errorf("moved row lost column values: event_type=%q work_item_id=%q project=%q", evType, wi, project)
	}

	// The audit event should record that a drain happened.
	var drained int64
	if err := pool.QueryRow(ctx, `
		SELECT coalesce(max((payload->>'drained_rows_at_check')::bigint), 0)
		FROM agent_events WHERE event_type = 'partition_created'`).Scan(&drained); err != nil {
		t.Fatalf("read drained count: %v", err)
	}
	if drained == 0 {
		t.Error("no partition_created event recorded a non-zero drained_rows_at_check")
	}
}

func TestRunPartitionCreate_ReportsBacklogItCannotDrain(t *testing.T) {
	pool := setupPartitionTestDB(t)
	ctx := context.Background()
	attachDefaultPartition(t, pool)

	// Beyond the lookahead window, so no partition the sweep creates can claim
	// it — the review-F6 coverage-hole shape, where the drain cannot help and
	// silence would be the only symptom.
	stranded := time.Now().UTC().AddDate(0, partitionLookaheadMonths+3, 0)
	if err := insertEventAt(pool, "evt_far", stranded); err != nil {
		t.Fatalf("seed stranded row: %v", err)
	}

	res := RunPartitionCreate(ctx, pool)
	mustNotSkip(t, res)
	if !strings.Contains(res.Error, agentEventsDefaultPartition) ||
		!strings.Contains(res.Error, "not covering live writes") {
		t.Errorf("Error = %q; want it to name %s and say the range partitions are not covering live writes",
			res.Error, agentEventsDefaultPartition)
	}
	// The partitions it COULD create must still have been created.
	if res.Affected != int64(partitionLookaheadMonths+1) {
		t.Errorf("Affected = %d, want %d — a backlog warning must not abort the sweep",
			res.Affected, partitionLookaheadMonths+1)
	}

	var warnings int64
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM agent_events
		WHERE event_type = 'system_gc' AND payload->>'sweep' = 'partition_create'`).Scan(&warnings); err != nil {
		t.Fatalf("count backlog warnings: %v", err)
	}
	if warnings != 1 {
		t.Errorf("durable backlog warnings = %d, want exactly 1", warnings)
	}

	// Rate limit: a second tick within the hour must not write another row.
	second := RunPartitionCreate(ctx, pool)
	mustNotSkip(t, second)
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM agent_events
		WHERE event_type = 'system_gc' AND payload->>'sweep' = 'partition_create'`).Scan(&warnings); err != nil {
		t.Fatalf("recount backlog warnings: %v", err)
	}
	if warnings != 1 {
		t.Errorf("backlog warnings after a second tick = %d, want 1 (rate-limited to one per hour)", warnings)
	}
	if !strings.Contains(second.Error, "not covering live writes") {
		t.Errorf("second tick lost the stderr-side warning: %q", second.Error)
	}
}

func TestRunPartitionCreate_ReportsADetachedNamesakeInsteadOfCountingIt(t *testing.T) {
	pool := setupPartitionTestDB(t)
	ctx := context.Background()

	// A relation with the right name that is NOT a partition. `CREATE TABLE IF
	// NOT EXISTS ... PARTITION OF` reports success for this and attaches
	// nothing, so counting the command's success would claim a partition that
	// cannot accept a single row.
	spec := partitionMonthsAhead(time.Now().UTC(), 1)[1]
	if _, err := pool.Exec(ctx, `CREATE TABLE `+spec.Name+` (LIKE agent_events)`); err != nil {
		t.Fatalf("create decoy: %v", err)
	}

	res := RunPartitionCreate(ctx, pool)
	mustNotSkip(t, res)

	if !strings.Contains(res.Error, spec.Name) || !strings.Contains(res.Error, "detached") {
		t.Errorf("Error = %q; want it to name %s and describe it as detached", res.Error, spec.Name)
	}
	if want := int64(partitionLookaheadMonths); res.Affected != want {
		t.Errorf("Affected = %d, want %d (every month except the decoy's)", res.Affected, want)
	}
	// And it must not have been credited with an audit event.
	var events int64
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM agent_events
		WHERE event_type = 'partition_created' AND payload->>'partition' = $1`, spec.Name).Scan(&events); err != nil {
		t.Fatalf("count decoy events: %v", err)
	}
	if events != 0 {
		t.Errorf("emitted %d partition_created events for a partition that was never attached", events)
	}
}

func TestRunPartitionCreate_AccumulatesEveryError(t *testing.T) {
	pool := setupPartitionTestDB(t)
	ctx := context.Background()

	// Two independent failures in one pass. `result.Error = ...` inside the loop
	// kept only the last one, which is how a persistent failure could look like a
	// single incident.
	specs := partitionMonthsAhead(time.Now().UTC(), partitionLookaheadMonths)
	decoys := []string{specs[1].Name, specs[3].Name}
	for _, name := range decoys {
		if _, err := pool.Exec(ctx, `CREATE TABLE `+name+` (LIKE agent_events)`); err != nil {
			t.Fatalf("create decoy %s: %v", name, err)
		}
	}

	res := RunPartitionCreate(ctx, pool)
	mustNotSkip(t, res)
	for _, name := range decoys {
		if !strings.Contains(res.Error, name) {
			t.Errorf("Error lost the failure for %s: %q", name, res.Error)
		}
	}
	if got := strings.Count(res.Error, "detached"); got != len(decoys) {
		t.Errorf("Error mentions %d detached partitions, want %d: %q", got, len(decoys), res.Error)
	}
}

// TestPartitionDDLTimeoutsAreApplied proves the review-F1 guard is real: without
// lock_timeout, a queued ACCESS EXCLUSIVE request blocks every writer behind it,
// so the sweep could cause the outage it exists to prevent.
func TestPartitionDDLTimeoutsAreApplied(t *testing.T) {
	pool := setupPartitionTestDB(t)
	ctx := context.Background()
	mustNotSkip(t, RunPartitionCreate(ctx, pool))
	attachDefaultPartition(t, pool)

	// Hold a conflicting lock on agent_events from another session.
	blocker, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatalf("acquire blocker conn: %v", err)
	}
	defer blocker.Release()
	tx, err := blocker.Begin(ctx)
	if err != nil {
		t.Fatalf("begin blocker tx: %v", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	if _, err := tx.Exec(ctx, `LOCK TABLE agent_events IN ACCESS SHARE MODE`); err != nil {
		t.Fatalf("blocker lock: %v", err)
	}

	// A month outside the window the sweep just built, so the create is real work.
	spec := partitionMonthsAhead(time.Now().UTC().AddDate(0, partitionLookaheadMonths+2, 0), 0)[0]

	start := time.Now()
	err = createPartitionPlain(ctx, pool, spec)
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("expected the contended CREATE to fail on lock_timeout, but it succeeded")
	}
	if !strings.Contains(err.Error(), "lock timeout") && !strings.Contains(err.Error(), "canceling statement") {
		t.Errorf("expected a lock-timeout error, got: %v", err)
	}
	// Must give up on its own, near partitionDDLLockTimeout, not hang.
	limit, perr := time.ParseDuration(partitionDDLLockTimeout)
	if perr != nil {
		t.Fatalf("parse partitionDDLLockTimeout: %v", perr)
	}
	if elapsed > limit*3 {
		t.Errorf("waited %s for a %s lock_timeout — the timeout is not being applied", elapsed, partitionDDLLockTimeout)
	}

	// And the failure must be reported, not swallowed.
	res := RunPartitionCreate(ctx, pool)
	mustNotSkip(t, res)
	_ = fmt.Sprint(res) // res.Error content depends on which months are missing
}
