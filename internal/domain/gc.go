package domain

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Advisory lock IDs for GC sweeps (pg_try_advisory_lock).
// Each sweep has its own lock ID to allow independent concurrent sweeps.
const (
	gcLockOrphanLocks       = int64(2001)
	gcLockMemoryExpired     = int64(2002)
	gcLockMethodologyExpiry = int64(2003)
	gcLockEventPayloadTrunc = int64(2004)
	gcLockUnblockDependent  = int64(2005)
	gcLockPartitionCreate   = int64(2006)
	gcLockNeedsHumanAging   = int64(2007)
	gcLockUnclassifiedAlert = int64(2008)
)

// Sweep type names. Each sweep reports its own name in GCResult.SweepType and
// gcSweepTable keys the run schedule on the same constant, so the name an
// operator reads in a `gc: <name> affected=N` line and the name the schedule
// throttles cannot drift apart.
const (
	sweepOrphanLockCleanup      = "orphan_lock_cleanup"
	sweepMemoryExpiredArchive   = "memory_expired_archive"
	sweepMethodologyExpiry      = "methodology_expiry_archive"
	sweepEventPayloadTruncation = "event_payload_truncation"
	sweepUnblockDependentWI     = "unblock_dependent_wi"
	sweepPartitionCreate        = "partition_create"
	sweepNeedsHumanSessionAging = "needs_human_session_aging"
	sweepUnclassifiedWIAlert    = "unclassified_wi_alert"
)

// GCResult summarizes what a single GC sweep did.
type GCResult struct {
	SweepType string `json:"sweep_type"`
	Affected  int64  `json:"affected"`
	Skipped   bool   `json:"skipped"` // true if advisory lock not acquired
	Error     string `json:"error,omitempty"`
}

// tryAdvisoryLock acquires a session-level advisory lock. Returns false if not acquired.
func tryAdvisoryLock(ctx context.Context, pool *pgxpool.Pool, lockID int64) (bool, func(), error) {
	conn, err := pool.Acquire(ctx)
	if err != nil {
		return false, nil, err
	}

	var acquired bool
	if err := conn.QueryRow(ctx, "SELECT pg_try_advisory_lock($1)", lockID).Scan(&acquired); err != nil {
		conn.Release()
		return false, nil, err
	}
	if !acquired {
		conn.Release()
		return false, nil, nil
	}
	release := func() {
		conn.Exec(ctx, "SELECT pg_advisory_unlock($1)", lockID) //nolint:errcheck
		conn.Release()
	}
	return true, release, nil
}

// ─── Sweep 1: Orphan Lock Cleanup ────────────────────────────────────────────

// orphanLockSweepSQL deletes resource_locks whose owner attempt is no longer
// holding them per the lock-retention contract. A lock is retained while its
// owner attempt is 'running' OR 'paused': FnCompleteAttempt keeps the locks on
// paused so resume can reclaim them (N4 / C5-3 design invariant), and the claim
// conflict-check (run_attempts.go) treats the retention set as IN ('running',
// 'paused'). The sweep predicate must match that set, otherwise the GC tick
// deletes a paused attempt's locks within 60s — breaking the resume invariant
// and allowing a concurrent claim to steal the resource (aihub#145).
const orphanLockSweepSQL = `
	DELETE FROM resource_locks rl
	WHERE NOT EXISTS (
		SELECT 1 FROM run_attempts ra
		WHERE ra.id = rl.owner_attempt_id AND ra.status IN ('running', 'paused')
	)`

// RunOrphanLockSweep removes resource_locks whose owner_attempt_id points to an
// attempt that is neither running nor paused (i.e. genuinely orphaned).
func RunOrphanLockSweep(ctx context.Context, pool *pgxpool.Pool) GCResult {
	result := GCResult{SweepType: sweepOrphanLockCleanup}
	acquired, release, err := tryAdvisoryLock(ctx, pool, gcLockOrphanLocks)
	if err != nil {
		result.Error = err.Error()
		return result
	}
	if !acquired {
		result.Skipped = true
		return result
	}
	defer release()

	tag, err := pool.Exec(ctx, orphanLockSweepSQL)
	if err != nil {
		result.Error = fmt.Sprintf("orphan lock sweep: %v", err)
		return result
	}
	result.Affected = tag.RowsAffected()
	return result
}

// ─── Sweep 2: Expired Memory Archival ────────────────────────────────────────

// RunMemoryExpiredSweep archives memories where effective_strength < 0.1 (raw) per §7.4.
// Uses the Ebbinghaus formula inline in SQL.
func RunMemoryExpiredSweep(ctx context.Context, pool *pgxpool.Pool) GCResult {
	result := GCResult{SweepType: sweepMemoryExpiredArchive}
	acquired, release, err := tryAdvisoryLock(ctx, pool, gcLockMemoryExpired)
	if err != nil {
		result.Error = err.Error()
		return result
	}
	if !acquired {
		result.Skipped = true
		return result
	}
	defer release()

	// Reference time MUST be memRefTimeSQL, not COALESCE. This sweep ARCHIVES
	// rows, so getting it wrong loses data: UpdateMemory carries a lineage's
	// last_activated_at onto each new version (aihub#236), so a freshly edited
	// memory holds an old activation timestamp. Under COALESCE that stale value
	// wins over the new created_at and the brand-new head is decayed as if it
	// were months old — an edit made minutes ago would be archived on the next
	// 60s tick while Recall still reports it at full strength.
	tag, err := pool.Exec(ctx, `
		UPDATE memories
		SET status = 'archived', updated_at = clock_timestamp()
		WHERE status = 'active'
		  AND is_immortal = FALSE
		  AND (
		    base_strength * exp(
		      -extract(epoch FROM (clock_timestamp() - `+memRefTimeSQL+`)) / 86400.0
		      / NULLIF(stability_days, 0)
		    )
		  ) < 0.1`)
	if err != nil {
		result.Error = fmt.Sprintf("memory expired sweep: %v", err)
		return result
	}
	result.Affected = tag.RowsAffected()
	return result
}

// ─── Sweep 3: Methodology Memory expires_at ──────────────────────────────────

// RunMethodologyExpiryArchive archives methodology.* memories whose expires_at has passed.
func RunMethodologyExpiryArchive(ctx context.Context, pool *pgxpool.Pool) GCResult {
	result := GCResult{SweepType: sweepMethodologyExpiry}
	acquired, release, err := tryAdvisoryLock(ctx, pool, gcLockMethodologyExpiry)
	if err != nil {
		result.Error = err.Error()
		return result
	}
	if !acquired {
		result.Skipped = true
		return result
	}
	defer release()

	tag, err := pool.Exec(ctx, `
		UPDATE memories
		SET status = 'archived', updated_at = clock_timestamp()
		WHERE status = 'active'
		  AND type LIKE 'methodology.%'
		  AND is_immortal = FALSE
		  AND expires_at IS NOT NULL
		  AND expires_at < clock_timestamp()`)
	if err != nil {
		result.Error = fmt.Sprintf("methodology expiry sweep: %v", err)
		return result
	}
	result.Affected = tag.RowsAffected()
	return result
}

// ─── Sweep 4: Event Payload Truncation ───────────────────────────────────────

// RunEventPayloadTruncation truncates agent_events payloads that exceed 64KB.
func RunEventPayloadTruncation(ctx context.Context, pool *pgxpool.Pool) GCResult {
	result := GCResult{SweepType: sweepEventPayloadTruncation}
	acquired, release, err := tryAdvisoryLock(ctx, pool, gcLockEventPayloadTrunc)
	if err != nil {
		result.Error = err.Error()
		return result
	}
	if !acquired {
		result.Skipped = true
		return result
	}
	defer release()

	tag, err := pool.Exec(ctx, `
		UPDATE agent_events
		SET payload = jsonb_build_object(
		    '_truncated', true,
		    '_original_size', octet_length(payload::text),
		    'note', 'payload truncated by GC: exceeded 64KB limit'
		)
		WHERE octet_length(payload::text) > 65536`)
	if err != nil {
		result.Error = fmt.Sprintf("event payload truncation: %v", err)
		return result
	}
	result.Affected = tag.RowsAffected()
	return result
}

// ─── Sweep 5: Unblock Dependent WIs (GC fallback) ────────────────────────────

// RunUnblockDependentWI unblocks work_items whose blocking wi are all terminal.
// This is the GC fallback (60s tick); the primary path is inside fn_complete_attempt.
func RunUnblockDependentWI(ctx context.Context, pool *pgxpool.Pool) GCResult {
	result := GCResult{SweepType: sweepUnblockDependentWI}
	acquired, release, err := tryAdvisoryLock(ctx, pool, gcLockUnblockDependent)
	if err != nil {
		result.Error = err.Error()
		return result
	}
	if !acquired {
		result.Skipped = true
		return result
	}
	defer release()

	tx, err := pool.Begin(ctx)
	if err != nil {
		result.Error = fmt.Sprintf("begin tx: %v", err)
		return result
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	// aihub#206: only requeue wis that were actually *dependency*-blocked, i.e.
	// have at least one 'blocks' dependency row (CreateWorkItem inserts one per
	// blocked_by entry). Escalated-stalled wis are also status='blocked' but
	// carry NO dependency row, so without the EXISTS guard they would match the
	// NOT EXISTS clause and get auto-requeued within 60s — silently emptying the
	// stalled segment and defeating human triage. The EXISTS guard leaves them
	// blocked; the NOT EXISTS clause still requeues dependency-blocked wis once
	// all their blockers reach a terminal status.
	tag, err := tx.Exec(ctx, `
		UPDATE work_items wi
		SET status = 'queued', updated_at = clock_timestamp()
		WHERE wi.status = 'blocked'
		  AND EXISTS (
		    SELECT 1 FROM wi_dependencies dep
		    WHERE dep.blocked_wi_id = wi.id
		      AND dep.kind = 'blocks'
		  )
		  AND NOT EXISTS (
		    SELECT 1 FROM wi_dependencies dep
		    JOIN work_items blocker ON dep.blocking_wi_id = blocker.id
		    WHERE dep.blocked_wi_id = wi.id
		      AND dep.kind = 'blocks'
		      AND blocker.status NOT IN ('wrapped', 'cancelled', 'failed')
		  )`)
	if err != nil {
		result.Error = fmt.Sprintf("unblock query: %v", err)
		return result
	}
	affected := tag.RowsAffected()

	if err := tx.Commit(ctx); err != nil {
		result.Error = fmt.Sprintf("commit: %v", err)
		return result
	}
	result.Affected = affected
	return result
}

// ─── Sweep 6: Partition Creator (60s tick) ───────────────────────────────────

// partitionLookaheadMonths is how many months BEYOND the current one the sweep
// keeps agent_events partitions for. Almost every aihub action writes
// agent_events, so running out of partitions is a total write-path outage, not a
// lost audit row — the lookahead is the runway available to notice and fix a
// creator that has stopped working.
//
// It was 2 (a "60 days ahead" comment over a `current month + 2` loop). That is
// too thin to be a safety margin: the failure it protects against is a *silent*
// one, and two months of runway on a sweep nobody was watching is what produced
// aihub#268. Six months costs six empty tables.
const partitionLookaheadMonths = 6

// agentEventsDefaultPartition is the DEFAULT partition added by migration 0031.
// Rows whose created_at falls outside every range partition land there instead
// of aborting the transaction; the sweep drains it into real partitions as it
// creates them.
const agentEventsDefaultPartition = "agent_events_default"

// partitionSpec is one monthly agent_events partition: its table name and its
// half-open [Start, End) range bound.
//
// Start/End are full timestamptz literals carrying an explicit +00 offset, NOT
// bare dates. A bare 'YYYY-MM-01' in a range bound is cast to timestamptz using
// the *session* TimeZone, so the same DDL run by a server in +08 would place the
// boundary at 2026-10-31 16:00 UTC and either overlap or leave a gap against the
// UTC-aligned bounds migration 0006 created — the CREATE would fail, or worse
// succeed with a hole. The same literals feed the DEFAULT-partition range
// predicates, which have the identical hazard.
type partitionSpec struct {
	Name  string
	Start string
	End   string
}

// partitionBoundLayout renders a bound as an unambiguous UTC timestamptz
// literal, e.g. "2026-11-01 00:00:00+00".
const partitionBoundLayout = "2006-01-02 15:04:05-07"

// partitionMonthsAhead returns the specs for the month containing now plus the
// next `ahead` months, oldest first.
//
// Every date is derived from the FIRST of the month, never from now's own
// day-of-month. The previous implementation used now.AddDate(0, i, 0) directly,
// and AddDate normalises overflow rather than clamping, so at month end it
// silently skipped a month: on 2026-08-31, i=1 gave 2026-09-31 → 2026-10-01, so
// the loop asked for August, October, October and never September. Anchoring to
// day 1 makes the sequence exactly consecutive for any now.
func partitionMonthsAhead(now time.Time, ahead int) []partitionSpec {
	first := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
	specs := make([]partitionSpec, 0, ahead+1)
	for i := 0; i <= ahead; i++ {
		start := first.AddDate(0, i, 0)
		end := start.AddDate(0, 1, 0)
		specs = append(specs, partitionSpec{
			Name:  fmt.Sprintf("agent_events_%d_%02d", start.Year(), int(start.Month())),
			Start: start.Format(partitionBoundLayout),
			End:   end.Format(partitionBoundLayout),
		})
	}
	return specs
}

// createPartitionSQL is the DDL that attaches one monthly partition. The
// interpolated values come from partitionMonthsAhead (a table name built from a
// clock reading and two formatted dates), never from request input.
func createPartitionSQL(spec partitionSpec) string {
	return fmt.Sprintf(`
		CREATE TABLE IF NOT EXISTS %s
		PARTITION OF agent_events
		FOR VALUES FROM ('%s') TO ('%s')`, spec.Name, spec.Start, spec.End)
}

// partitionDDLLockTimeout bounds how long a partition DDL statement will WAIT
// for its ACCESS EXCLUSIVE lock, and partitionDDLStatementTimeout bounds the
// work once it has it.
//
// aihub#268 review F1: without lock_timeout this sweep could cause the outage it
// exists to prevent. A *queued* ACCESS EXCLUSIVE request blocks every ordinary
// INSERT behind it, even when the current holder is only a plain SELECT — and a
// realistic holder exists, because the other aihub instance's
// RunEventPayloadTruncation full-scans agent_events on its own 60s tick under a
// DIFFERENT advisory lock id (2004 vs this sweep's 2006), so the two do not
// serialise against each other. Bounded waiting turns that from a write-path
// stall into a logged error and a retry 60 seconds later.
const (
	partitionDDLLockTimeout      = "3s"
	partitionDDLStatementTimeout = "60s"
)

// setPartitionDDLTimeouts applies the timeouts above to one transaction.
// SET LOCAL is reverted on COMMIT/ROLLBACK, so nothing leaks to the next user of
// this pooled connection. pgx's extended protocol rejects multiple statements in
// one Exec, hence two calls.
func setPartitionDDLTimeouts(ctx context.Context, tx pgx.Tx) error {
	if _, err := tx.Exec(ctx, `SET LOCAL lock_timeout = '`+partitionDDLLockTimeout+`'`); err != nil {
		return fmt.Errorf("set lock_timeout: %w", err)
	}
	if _, err := tx.Exec(ctx, `SET LOCAL statement_timeout = '`+partitionDDLStatementTimeout+`'`); err != nil {
		return fmt.Errorf("set statement_timeout: %w", err)
	}
	return nil
}

// createPartitionPlain attaches spec's partition when the DEFAULT partition holds
// no rows belonging to it. It runs in a transaction purely so the timeouts above
// apply — this is NOT a cheap statement: Postgres validates the whole DEFAULT
// partition against the new bound before attaching, so it scans the default even
// when zero rows fall in range (measured: ~205ms against a 3.3M-row default,
// holding ACCESS EXCLUSIVE throughout).
func createPartitionPlain(ctx context.Context, pool *pgxpool.Pool, spec partitionSpec) error {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // no-op once Commit succeeded

	if err := setPartitionDDLTimeouts(ctx, tx); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, createPartitionSQL(spec)); err != nil {
		return fmt.Errorf("create %s: %w", spec.Name, err)
	}
	return tx.Commit(ctx)
}

// isAttachedPartition reports whether `name` is currently attached to
// agent_events. Used both to skip months that already exist and — after a
// CREATE — to prove the CREATE actually attached something, since
// `CREATE TABLE IF NOT EXISTS` succeeds silently when a same-named relation
// exists while being detached.
func isAttachedPartition(ctx context.Context, pool *pgxpool.Pool, name string) (bool, error) {
	var exists bool
	// The parent is matched via 'agent_events'::regclass rather than by relname:
	// regclass resolves through search_path exactly as the CREATE does, so a
	// same-named table in another schema cannot be mistaken for the parent. A
	// false "already attached" here would make the sweep skip a month it needed
	// to create, which is the outage this whole sweep exists to prevent.
	err := pool.QueryRow(ctx, `
		SELECT EXISTS (
		  SELECT 1
		  FROM pg_inherits i
		  JOIN pg_class child ON child.oid = i.inhrelid
		  WHERE i.inhparent = 'agent_events'::regclass
		    AND child.relname = $1
		)`, name).Scan(&exists)
	return exists, err
}

// countDefaultRowsInRange counts rows already sitting in the DEFAULT partition
// that belong to spec's range. Any such row makes a plain
// `CREATE TABLE ... PARTITION OF` fail (Postgres validates the default
// partition against the new bound), so it selects the drain path below.
// Returns 0 when there is no DEFAULT partition.
func countDefaultRowsInRange(ctx context.Context, pool *pgxpool.Pool, spec partitionSpec) (int64, error) {
	attached, err := isAttachedPartition(ctx, pool, agentEventsDefaultPartition)
	if err != nil || !attached {
		return 0, err
	}
	var n int64
	err = pool.QueryRow(ctx, fmt.Sprintf(`
		SELECT count(*) FROM %s
		WHERE created_at >= $1::timestamptz AND created_at < $2::timestamptz`,
		agentEventsDefaultPartition), spec.Start, spec.End).Scan(&n)
	return n, err
}

// createPartitionDrainingDefault creates spec's partition when the DEFAULT
// partition holds rows belonging to it — the recovery path after the sweep has
// been failing long enough for writes to fall through to DEFAULT.
//
// Postgres will not attach a range partition while the default partition holds
// rows that would belong to it, so the default has to come off first. All of it
// runs in one transaction (DDL is transactional in Postgres) and takes ACCESS
// EXCLUSIVE on agent_events, i.e. it blocks writes for its duration.
//
// That duration is O(size of the DEFAULT partition), NOT O(rows moved): the
// closing `ATTACH PARTITION ... DEFAULT` re-validates every remaining row in the
// default against the new partition set. Measured against a 3.3M-row / 668MB
// default: ~445ms to attach after moving a single row. So this path is at its
// most expensive exactly when it is most needed — a default that has been
// catching writes for months — which is why it runs under
// partitionDDLStatementTimeout and reports rather than retrying forever.
func createPartitionDrainingDefault(ctx context.Context, pool *pgxpool.Pool, spec partitionSpec) error {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // no-op once Commit succeeded

	if err := setPartitionDDLTimeouts(ctx, tx); err != nil {
		return err
	}
	// Take the lock explicitly and first, so a contended run fails on this
	// statement (inside lock_timeout) rather than part-way through the
	// detach/create/move sequence.
	if _, err := tx.Exec(ctx, `LOCK TABLE agent_events IN ACCESS EXCLUSIVE MODE`); err != nil {
		return fmt.Errorf("lock agent_events: %w", err)
	}
	if _, err := tx.Exec(ctx, fmt.Sprintf(
		`ALTER TABLE agent_events DETACH PARTITION %s`, agentEventsDefaultPartition)); err != nil {
		return fmt.Errorf("detach %s: %w", agentEventsDefaultPartition, err)
	}
	if _, err := tx.Exec(ctx, createPartitionSQL(spec)); err != nil {
		return fmt.Errorf("create %s: %w", spec.Name, err)
	}
	// Move, not copy: the rows must not exist in both once DEFAULT is reattached.
	if _, err := tx.Exec(ctx, fmt.Sprintf(`
		WITH moved AS (
		  DELETE FROM %s
		  WHERE created_at >= $1::timestamptz AND created_at < $2::timestamptz
		  RETURNING *
		)
		INSERT INTO agent_events SELECT * FROM moved`,
		agentEventsDefaultPartition), spec.Start, spec.End); err != nil {
		return fmt.Errorf("drain %s into %s: %w", agentEventsDefaultPartition, spec.Name, err)
	}
	if _, err := tx.Exec(ctx, fmt.Sprintf(
		`ALTER TABLE agent_events ATTACH PARTITION %s DEFAULT`, agentEventsDefaultPartition)); err != nil {
		return fmt.Errorf("reattach %s: %w", agentEventsDefaultPartition, err)
	}
	return tx.Commit(ctx)
}

// defaultBacklogWarnSQL emits the DEFAULT-partition backlog warning at most once
// an hour. Named rather than inline so that
// TestGCSQL_PartitionKeyBoundsReadFromNowNotClockTimestamp can hold it to the
// same rule as the alert guards.
const defaultBacklogWarnSQL = `
	INSERT INTO agent_events (id, event_type, payload, created_at)
	SELECT $1, 'system_gc', $2, clock_timestamp()
	WHERE NOT EXISTS (
	  SELECT 1 FROM agent_events
	  WHERE event_type = 'system_gc'
	    AND payload->>'sweep' = 'partition_create'
	    AND payload ? 'default_partition_rows'
	    AND created_at > now() - interval '1 hour'
	)`

// reportDefaultBacklog is the signal the DEFAULT partition would otherwise
// remove. Rows in agent_events_default mean the range partitions are NOT
// covering live writes — the safety net is load-bearing right now — and without
// this the only symptom is the absence of a symptom, which is precisely the
// silence that produced aihub#268 (review F4).
//
// It also covers the one case the drain cannot reach (review F6): rows stranded
// in a *coverage hole* rather than a missing month, e.g. bounds created under a
// non-UTC session leaving 8 hours uncovered between two partitions that both
// exist. The drain only fires for months with no partition at all, so those rows
// would sit in DEFAULT forever, un-drained and unreported.
//
// The stderr line (via GCResult.Error, now actually printed) fires every tick;
// the durable agent_events row is rate-limited to one per hour so a persistent
// backlog cannot flood the very table it is warning about.
func reportDefaultBacklog(ctx context.Context, pool *pgxpool.Pool) (int64, error) {
	attached, err := isAttachedPartition(ctx, pool, agentEventsDefaultPartition)
	if err != nil || !attached {
		return 0, err
	}

	var n int64
	if err := pool.QueryRow(ctx, fmt.Sprintf(
		`SELECT count(*) FROM %s`, agentEventsDefaultPartition)).Scan(&n); err != nil {
		return 0, fmt.Errorf("count %s: %w", agentEventsDefaultPartition, err)
	}
	if n == 0 {
		return 0, nil
	}

	payload, _ := json.Marshal(map[string]any{
		"sweep":                  "partition_create",
		"default_partition":      agentEventsDefaultPartition,
		"default_partition_rows": n,
		"note": "rows are landing in the DEFAULT partition: agent_events range " +
			"partitions are not covering live writes",
	})
	// WHERE NOT EXISTS is the rate limit: no extra state, and the lookup rides
	// idx_evt_type_time.
	//
	// The window reads from now(), not clock_timestamp(), for the reason set out
	// at length in alertNotRepeatedSQL: agent_events is RANGE partitioned on
	// created_at, now() is STABLE and prunes, clock_timestamp() is VOLATILE and
	// does not. This predicate runs on every 60s tick for as long as the DEFAULT
	// partition is non-empty — that is, precisely when the table is at its worst —
	// so it is where the difference is paid most often. The INSERTED created_at
	// stays clock_timestamp(): that is a value being written, not a bound being
	// scanned.
	if _, err := pool.Exec(ctx, defaultBacklogWarnSQL, NewID("evt"), payload); err != nil {
		return n, fmt.Errorf("emit default-backlog warning: %w", err)
	}
	return n, nil
}

// RunPartitionCreate ensures agent_events monthly partitions exist
// partitionLookaheadMonths months ahead, and reports what it did.
//
// aihub#268: this sweep had been running every 60s since the first commit but
// had never once had to create a partition (migration 0006 pre-created through
// 2026-10 and the old 2-month horizon never reached past it), and it could not
// have told anyone if it had failed:
//   - CREATE TABLE's command tag reports RowsAffected() == 0, so Affected was
//     always 0 even on success;
//   - the caller in cmd/aihub/main.go skips any sweep with Affected == 0, so
//     Error was never printed either;
//   - no partition_created event was ever emitted, despite the
//     chk_evt_work_item_id whitelist having allowed that event_type all along
//     (0 rows in production, which read as "the mechanism does not exist").
//
// So: count real attachments rather than trusting the command tag, accumulate
// every error instead of keeping only the last, and leave an audit event per
// created partition.
func RunPartitionCreate(ctx context.Context, pool *pgxpool.Pool) GCResult {
	result := GCResult{SweepType: sweepPartitionCreate}
	acquired, release, err := tryAdvisoryLock(ctx, pool, gcLockPartitionCreate)
	if err != nil {
		result.Error = err.Error()
		return result
	}
	if !acquired {
		result.Skipped = true
		return result
	}
	defer release()

	var errs []string
	created := int64(0)
	for _, spec := range partitionMonthsAhead(time.Now().UTC(), partitionLookaheadMonths) {
		attached, err := isAttachedPartition(ctx, pool, spec.Name)
		if err != nil {
			errs = append(errs, fmt.Sprintf("check partition %s: %v", spec.Name, err))
			continue
		}
		if attached {
			continue
		}

		stranded, err := countDefaultRowsInRange(ctx, pool, spec)
		if err != nil {
			errs = append(errs, fmt.Sprintf("check %s for %s: %v", agentEventsDefaultPartition, spec.Name, err))
			continue
		}

		if stranded > 0 {
			if err := createPartitionDrainingDefault(ctx, pool, spec); err != nil {
				errs = append(errs, err.Error())
				continue
			}
		} else if err := createPartitionPlain(ctx, pool, spec); err != nil {
			errs = append(errs, fmt.Sprintf("create partition %s: %v", spec.Name, err))
			continue
		}

		// Prove the CREATE attached something. `IF NOT EXISTS` returns success
		// for a same-named relation that is detached from agent_events, which
		// would otherwise be counted as a created partition that cannot receive
		// a single row.
		attached, err = isAttachedPartition(ctx, pool, spec.Name)
		if err != nil {
			errs = append(errs, fmt.Sprintf("verify partition %s: %v", spec.Name, err))
			continue
		}
		if !attached {
			errs = append(errs, fmt.Sprintf(
				"partition %s was not attached to agent_events: a relation of that name already exists but is detached",
				spec.Name))
			continue
		}
		created++

		// Audit trail — the event_type the whitelist has always allowed and
		// nothing ever wrote. Its failure is reported, not swallowed: this
		// event is how an operator confirms the sweep is alive.
		payload, _ := json.Marshal(map[string]any{
			"partition":   spec.Name,
			"range_start": spec.Start,
			"range_end":   spec.End,
			// Counted before the move (review F7); concurrent writes can add
			// more, which the next tick drains.
			"drained_rows_at_check": stranded,
			"lookahead_months":      partitionLookaheadMonths,
		})
		if _, err := pool.Exec(ctx, `
			INSERT INTO agent_events (id, event_type, payload, created_at)
			VALUES ($1, 'partition_created', $2, clock_timestamp())`,
			NewID("evt"), payload); err != nil {
			errs = append(errs, fmt.Sprintf("emit partition_created for %s: %v", spec.Name, err))
		}
	}

	if backlog, err := reportDefaultBacklog(ctx, pool); err != nil {
		errs = append(errs, err.Error())
	} else if backlog > 0 {
		errs = append(errs, fmt.Sprintf(
			"%s holds %d rows: agent_events range partitions are not covering live writes",
			agentEventsDefaultPartition, backlog))
	}

	result.Affected = created
	if len(errs) > 0 {
		result.Error = strings.Join(errs, "; ")
	}
	return result
}

// ─── Alert sweeps 7 & 8: cadence and idempotency ─────────────────────────────

// Sweeps 7 and 8 are the only two of the eight that EMIT a row rather than
// mutate one, and that is exactly why they were the two defective ones.
//
// Every other sweep's write falsifies its own WHERE predicate — a deleted
// resource_lock cannot be deleted twice, an archived memory is no longer
// 'active', a truncated payload is no longer over 64KB, an unblocked wi is no
// longer 'blocked', an attached partition is skipped by isAttachedPartition — so
// re-running it is a no-op by construction and its cadence costs only query
// time. An INSERT has no such property: re-running it inserts again. For these
// two, idempotency had to be written down, and it never was — both looped over
// their candidate wis calling NewID("evt") and INSERTing unconditionally.
//
// Driven from the 60s ticker in cmd/aihub/main.go, that is 1,440 rows per
// matching wi per day, each carrying an identical
// (work_item_id, event_type, payload) triple. Measured in production before this
// change (aihub#266): ieops#84 alone held 111,221 of these rows, aihub#118 over
// 50,000, and agent_events grew ~105,000 rows/day for the ieops project alone —
// enough that "the most recent N events" stopped working as a diagnostic window,
// which is how the flood was found in the first place.
//
// Both defects are fixed, in different places on purpose:
//
//   - the DUPLICATE (this section) is fixed in SQL, by a repeat window inside
//     the sweeps' own statements;
//   - the CADENCE (gcSweepTable below) is fixed in the run schedule, by giving
//     these two the daily period their headers always claimed.
//
// The SQL guard, not the schedule, is what makes the no-duplicates property
// true, because there are ways to reach these sweeps that no in-process schedule
// can see: a restart resets the schedule, so a crash-looping instance would
// re-emit on every start; and POST /v1/admin/gc calls RunAll, which deliberately
// ignores the schedule. The schedule cuts runs per day from 1,440 to 24. It
// cannot cut rows.
//
// What it does NOT do is make the write atomic. INSERT ... SELECT ... WHERE NOT
// EXISTS is a snapshot read: under READ COMMITTED two concurrent transactions
// both see no row and both write. Measured on two pgx connections against this
// exact statement and DDL shape — 1 row inserted by each, 2 rows for one wi
// inside one window. What serialises concurrent sweeps is tryAdvisoryLock:
// pg_try_advisory_lock is session-level and held on a dedicated pooled
// connection for the whole sweep, so the loser returns Skipped without running a
// statement at all.
//
// That lock earns its keep in a SINGLE-process deployment, which is the part
// worth not losing: cmd/aihub/main.go runs the GC ticker on its own goroutine
// while the HTTP server answers POST /v1/admin/gc on another, and those two call
// RunDue and RunAll concurrently against one database. The advisory lock is the
// only thing between an admin-forced run and a tick that overlaps it. Narrowing
// it — per project, say, for throughput — would remove that, and no test here
// would notice, because every test in this package runs one sweep at a time.
//
// An earlier version of this comment justified the lock differently: it asserted
// that two aihub instances ran tickers against one database. That was false, and
// how it got here is worth recording, because the same bait is still lying in
// the work item. It was inherited from aihub#266's own 2026-08-27 narrative,
// which audited「两个实例」10.146.0.16 and 10.146.0.34 — but those were two
// deployments with DIVERGED databases (the divergence was the thing being
// audited), not two tickers writing one table. Measured on 2026-09-01 against
// production: pg_stat_activity on the aihub database showed exactly one
// client_addr, the other aihub containers on that host had been Exited for four
// days, and the flood arrived as ONE burst per minute — 60 rows in 59.0 minutes
// for a single wi, 1.003 bursts/min, with sweep 7's rows strictly before sweep
// 8's inside each 151-182ms burst. One emitter. The narrative said "two"; the
// rows carry no emitter identity at all, so only the timing could answer it.
//
// The guard is a repeat WINDOW rather than a unique constraint because
// agent_events is RANGE partitioned on created_at: a unique index on
// (work_item_id, event_type) would have to include the partition key, and
// created_at is precisely the column that differs between two duplicates.
// reportDefaultBacklog above already uses this shape, for the same reason.

// The two sweeps have TWO cadences, and keeping them apart is the whole design:
//
//	gcAlertRepeatWindow  how often a wi may be ALERTED ABOUT. Enforced in SQL.
//	gcAlertPollPeriod    how often the sweep ASKS the database. Enforced by the
//	                     in-process schedule.
//
// The window is the user-visible cadence; the poll period is only a load knob.
// Making one constant do both jobs is the trap, and an earlier draft of this
// change fell into it by giving the schedule a 24h period and the window 23h.
//
// The failure is not the obvious one. Window >= period deadlocks — the row
// written at T+ε is still inside the window at T+24h, so the sweep emits every
// 48h instead of 24h — and 23h < 24h avoids that. But window ≈ period makes the
// SCHEDULE'S PHASE decide the delivered cadence, and a restart randomises that
// phase. Measured on the 24h/23h pair: an instance restarting 22h after the last
// alert runs the sweep, the 23h window correctly suppresses it, the schedule
// records the run anyway, and the next run is 24h later — an alert gap of ~46h,
// which is the very 2x-period outcome the slack existed to prevent, reached
// through a different door.
//
// Polling far more often than the window closes it: the window is then always
// the binding constraint, and the schedule's phase can only add up to one poll
// period of delay. Alert-to-alert lands in [23h, 24h] — under a day, as
// specified — for any phase, any restart, any number of instances.
const (
	// gcAlertPollPeriod is how often RunDue lets an alert sweep ask. One hour
	// turns 1,440 runs a day into 24, which is the whole of the load problem;
	// going further would buy nothing and start to matter for phase.
	gcAlertPollPeriod = time.Hour

	// gcAlertRepeatWindow is how recently an alert of the same type for the same
	// wi must have landed to suppress a new one. One poll period short of a day,
	// so that window + worst-case poll delay is exactly a day rather than a day
	// and a bit.
	gcAlertRepeatWindow = 24*time.Hour - gcAlertPollPeriod
)

// gcSpecifiedAlertCadence is what docs/design/polyforge-v1-design.md §15 items 7
// and 8 call these sweeps ("daily tick"), and what their section headers have
// claimed since they were written. Nothing schedules on it — it is the number
// gcAlertRepeatWindow is derived to stay under, and the tests compare against.
//
// aihub#266 asked which side of the comment/code mismatch was the intent and
// which the drift, and told the fixer not to assume it was the comment. The
// answer is in gcSweepTable's doc comment: §15 declares the whole sweep list a
// "60s tick" job and then marks exactly three of its eight items as daily
// exceptions. The code kept the header and lost the exceptions.
const gcSpecifiedAlertCadence = 24 * time.Hour

// gcAlertRepeatWindowArg is gcAlertRepeatWindow as the interval argument bound
// into both alert statements.
//
// A whole count of seconds rather than gcAlertRepeatWindow.String(): Go renders
// a Duration as e.g. "23h0m0s", and Postgres' interval parser happening to
// accept that spelling is a coincidence between two independent grammars, not a
// contract. "82800 seconds" is unambiguous in both.
var gcAlertRepeatWindowArg = fmt.Sprintf("%d seconds", int64(gcAlertRepeatWindow/time.Second))

// alertNotRepeatedSQL renders "this wi has had no alert of this type inside the
// repeat window", for one event type.
//
// eventType is interpolated and is always a literal defined in this file, never
// request input. wiExpr is either a correlated column reference ("wi.id", in a
// candidate query) or a bind placeholder ("$2", in an INSERT).
//
// It is a shared fragment because each sweep applies the SAME guard at two
// moments: once when choosing candidates, so that the steady state costs one
// query and zero INSERT round trips, and once inside the INSERT, so that a row
// which landed between the two — from an admin-forced RunAll, or from this
// process before a restart — is still respected. Two hand-maintained copies of
// one predicate is how they would quietly diverge, and divergence in the
// direction where the candidate query is looser than the INSERT produces an
// alert that never arrives, with Affected honestly reporting 0 and nothing else
// to see.
//
// The INSERT's copy is NOT a concurrency guard, whatever its shape suggests:
// INSERT ... SELECT ... WHERE NOT EXISTS is a snapshot read, so under READ
// COMMITTED two simultaneous transactions both see nothing and both write
// (measured: 2 rows for one wi inside one window). Two aihub instances are kept
// apart by tryAdvisoryLock, not by this. See the section comment above.
//
// The window is measured from now(), NOT from clock_timestamp(), and that is the
// one thing in this function that is not free to change. Everything else in this
// file timestamps with clock_timestamp(), so "make it consistent" is a tempting
// edit; it costs two orders of magnitude.
//
// agent_events is RANGE partitioned on created_at, and pruning a partition needs
// a bound the planner can evaluate. now() is STABLE, so it can; clock_timestamp()
// is VOLATILE, so it cannot, and the guard then has to visit every partition for
// every candidate. Measured against a fixture rebuilt to the shape production is
// in — 199,221 events over seven monthly partitions, 111,221 of them on one work
// item, 78 unclassified wis:
//
//	clock_timestamp()  181-197 ms, no pruning, 78 correlated subplan executions
//	now()              1.1-1.3 ms, "Subplans Removed: 4", hash right anti join
//
// A 165x difference on a predicate whose semantics are identical either way: the
// window is 23 hours, and transaction start versus statement start differ by
// microseconds. The candidate queries already age their wis with now() for the
// same reason.
func alertNotRepeatedSQL(wiExpr, eventType, intervalArg string) string {
	return `NOT EXISTS (
			SELECT 1 FROM agent_events e
			WHERE e.work_item_id = ` + wiExpr + `
			  AND e.event_type = '` + eventType + `'
			  AND e.created_at > now() - ` + intervalArg + `::interval
		)`
}

// ─── Sweep 7: Needs Human Session Aging (daily) ──────────────────────────────

// needsHumanSessionCandidateSQL selects queued requires_human_session=true wis
// past their aging threshold that have NOT already been alerted inside the
// repeat window. Carrying the window predicate here as well as in the INSERT is
// what makes the steady state — every matching wi already alerted today — cost
// exactly this one query and no INSERT round trips at all.
var needsHumanSessionCandidateSQL = `
	SELECT wi.id, wi.slug, wi.wi_type, wi.priority, wi.project, wi.created_at
	FROM work_items wi
	WHERE wi.requires_human_session = true
	  AND wi.status = 'queued'
	  AND wi.created_at < now() - CASE wi.priority
	      WHEN 'urgent' THEN interval '1 day'
	      ELSE interval '7 days'
	    END
	  AND ` + alertNotRepeatedSQL("wi.id", "wi_needs_attention", "$1")

// needsHumanSessionInsertSQL emits one wi_needs_attention unless an alert for
// this wi landed inside the window since the candidate query ran — an
// admin-forced RunAll, or this process before a restart. INSERT ... SELECT ...
// WHERE NOT EXISTS rather than VALUES so the re-check rides the write itself
// instead of being a second round trip that can be skipped. It does NOT make the
// write atomic against a concurrent inserter; see alertNotRepeatedSQL.
var needsHumanSessionInsertSQL = `
	INSERT INTO agent_events (id, work_item_id, event_type, payload, project, created_at)
	SELECT $1, $2, 'wi_needs_attention', $3, $4, clock_timestamp()
	WHERE ` + alertNotRepeatedSQL("$2", "wi_needs_attention", "$5")

// RunNeedsHumanSessionAging emits wi_needs_attention for queued
// requires_human_session=true work_items that have been waiting too long
// (§15 sweep 7) — at most one per wi per gcAlertRepeatWindow.
func RunNeedsHumanSessionAging(ctx context.Context, pool *pgxpool.Pool) GCResult {
	result := GCResult{SweepType: sweepNeedsHumanSessionAging}
	acquired, release, err := tryAdvisoryLock(ctx, pool, gcLockNeedsHumanAging)
	if err != nil {
		result.Error = err.Error()
		return result
	}
	if !acquired {
		result.Skipped = true
		return result
	}
	defer release()

	rows, err := pool.Query(ctx, needsHumanSessionCandidateSQL, gcAlertRepeatWindowArg)
	if err != nil {
		result.Error = fmt.Sprintf("needs_human_session query: %v", err)
		return result
	}
	defer rows.Close()

	// WIType is a POINTER because work_items.wi_type is nullable — migration 0002
	// declares `wi_type TEXT` with no NOT NULL and no default — and nothing ties it
	// to requires_human_session: a wi can be queued, flagged as needing a human,
	// and still have no wi_type, which is exactly the population worth nagging
	// about. Scanning that into a string fails with "cannot scan NULL into
	// *string", and after this change the failure is no longer swallowed by
	// `if err == nil`: it reaches GCResult.Error, so runDueAt never records the
	// run and the sweep retries and re-logs on every tick forever, while the wi
	// that caused it is never alerted about. A nil marshals to JSON null, which is
	// what the column actually holds.
	type wiRow struct {
		ID, Slug, Priority, Project string
		WIType                      *string
		CreatedAt                   time.Time
	}
	var wis []wiRow
	var errs []string
	for rows.Next() {
		var w wiRow
		if err := rows.Scan(&w.ID, &w.Slug, &w.WIType, &w.Priority, &w.Project, &w.CreatedAt); err != nil {
			errs = append(errs, fmt.Sprintf("scan candidate: %v", err))
			continue
		}
		wis = append(wis, w)
	}
	rows.Close()

	affected := int64(0)
	for _, w := range wis {
		payload, _ := json.Marshal(map[string]any{
			"wi_id":         w.ID,
			"wi_slug":       w.Slug,
			"wi_type":       w.WIType,
			"priority":      w.Priority,
			"waiting_since": w.CreatedAt,
			"reason":        "requires_human_session=true, no claim after aging threshold",
		})
		// Count rows actually written, not "Exec returned no error". The guard
		// makes a 0-row INSERT a legitimate outcome, and the old
		// `if err == nil { affected++ }` would have reported one as an emitted
		// alert. Errors accumulate rather than being dropped (the aihub#268
		// shape): a permanently failing INSERT would otherwise be
		// indistinguishable from having nothing to alert about, which is the same
		// "success looks exactly like silence" defect this change is about.
		tag, err := pool.Exec(ctx, needsHumanSessionInsertSQL,
			NewID("evt"), w.ID, payload, w.Project, gcAlertRepeatWindowArg)
		if err != nil {
			errs = append(errs, fmt.Sprintf("emit wi_needs_attention for %s: %v", w.Slug, err))
			continue
		}
		affected += tag.RowsAffected()
	}

	result.Affected = affected
	if len(errs) > 0 {
		result.Error = strings.Join(errs, "; ")
	}
	return result
}

// ─── Sweep 8: Unclassified WI Alert (daily) ──────────────────────────────────

// unclassifiedWIAlertCandidateSQL selects day-old queued wis with
// requires_human_session IS NULL that have not already been alerted inside the
// repeat window. See needsHumanSessionCandidateSQL for why the window predicate
// appears here as well as in the INSERT.
var unclassifiedWIAlertCandidateSQL = `
	SELECT wi.id, wi.slug, wi.project, wi.reporter_user_id
	FROM work_items wi
	WHERE wi.requires_human_session IS NULL
	  AND wi.status = 'queued'
	  AND wi.created_at < now() - interval '1 day'
	  AND ` + alertNotRepeatedSQL("wi.id", "wi_classification_missing", "$1")

// unclassifiedWIAlertInsertSQL emits one wi_classification_missing, guarded the
// same way as needsHumanSessionInsertSQL.
var unclassifiedWIAlertInsertSQL = `
	INSERT INTO agent_events (id, work_item_id, event_type, payload, project, created_at)
	SELECT $1, $2, 'wi_classification_missing', $3, $4, clock_timestamp()
	WHERE ` + alertNotRepeatedSQL("$2", "wi_classification_missing", "$5")

// RunUnclassifiedWIAlert emits wi_classification_missing for old unclassified
// work_items — at most one per wi per gcAlertRepeatWindow.
//
// requires_human_session IS NULL is the unclassified THIRD state, not false:
// migration 0002 routes a NULL wi to the ready queue's unclassified[] rather
// than items[], so nothing can be dispatched to it. That is what this alert is
// for, and why it must keep firing (once) rather than being silenced.
func RunUnclassifiedWIAlert(ctx context.Context, pool *pgxpool.Pool) GCResult {
	result := GCResult{SweepType: sweepUnclassifiedWIAlert}
	acquired, release, err := tryAdvisoryLock(ctx, pool, gcLockUnclassifiedAlert)
	if err != nil {
		result.Error = err.Error()
		return result
	}
	if !acquired {
		result.Skipped = true
		return result
	}
	defer release()

	rows, err := pool.Query(ctx, unclassifiedWIAlertCandidateSQL, gcAlertRepeatWindowArg)
	if err != nil {
		result.Error = fmt.Sprintf("unclassified wi query: %v", err)
		return result
	}
	defer rows.Close()

	type wiRow struct{ ID, Slug, Project, ReporterID string }
	var wis []wiRow
	var errs []string
	for rows.Next() {
		var w wiRow
		if err := rows.Scan(&w.ID, &w.Slug, &w.Project, &w.ReporterID); err != nil {
			errs = append(errs, fmt.Sprintf("scan candidate: %v", err))
			continue
		}
		wis = append(wis, w)
	}
	rows.Close()

	affected := int64(0)
	for _, w := range wis {
		payload, _ := json.Marshal(map[string]any{
			"wi_id":       w.ID,
			"wi_slug":     w.Slug,
			"reporter_id": w.ReporterID,
			"reason":      "requires_human_session is NULL — please set wi_type to classify",
		})
		// See RunNeedsHumanSessionAging for why this counts RowsAffected and
		// accumulates errors instead of counting successful Execs.
		tag, err := pool.Exec(ctx, unclassifiedWIAlertInsertSQL,
			NewID("evt"), w.ID, payload, w.Project, gcAlertRepeatWindowArg)
		if err != nil {
			errs = append(errs, fmt.Sprintf("emit wi_classification_missing for %s: %v", w.Slug, err))
			continue
		}
		affected += tag.RowsAffected()
	}

	result.Affected = affected
	if len(errs) > 0 {
		result.Error = strings.Join(errs, "; ")
	}
	return result
}

// ─── Sweep cadence: RunDue and RunAll ────────────────────────────────────────

// gcEveryTick marks a sweep that runs on every RunDue call, i.e. at whatever
// cadence its caller's ticker fires (60s, in cmd/aihub/main.go).
//
// Zero rather than a literal 60s, deliberately. Comparing "elapsed since last
// run" against 60s would let ordinary scheduling jitter push one tick's
// now.Sub(last) a millisecond under the threshold and drop that tick, silently
// halving the cadence of six sweeps that are documented to run on every one of
// them. "Every tick" is not a duration, so it is not stored as one — and the
// ticker's period stays the business of the file that owns the ticker.
const gcEveryTick = time.Duration(0)

// gcSweep is one row of the sweep table: the sweep function, the name it reports
// in GCResult.SweepType, and how often RunDue may run it.
type gcSweep struct {
	Name   string
	Period time.Duration
	Fn     func(context.Context, *pgxpool.Pool) GCResult
}

// gcSweepTable is the eight sweeps and their cadences.
//
// Six run on every tick and aihub#266 does NOT change their period. A blanket
// period change to the shared RunAll — the obvious way to make two sweeps daily
// — would have moved all six with them, so each one is accounted for here.
//
// aihub#266 asked which side of the comment/code mismatch was the intent and
// which the drift, and warned against assuming it was the comment. The design
// doc answers it directly, and not by saying "daily" somewhere: §15 introduces
// this exact list as
//
//	GC job（60s tick，pg_try_advisory_lock 单实例保证只有一个实例跑）：
//
// and then marks THREE of its eight items, and only three, as exceptions —
// item 6 partition creator「daily tick 独立」(independent daily tick), item 7
// needs_human_session aging「daily tick」, item 8 unclassified wi 告警
// 「daily tick」. Item 5 goes the other way and pins itself to the header
// ("GC 60s tick 仅作兜底").
//
// So the spec is not ambiguous and it is not silent: it states a default cadence
// for the list and names the departures from it. The code applied the header to
// all eight and dropped the three exceptions. The comments in this file are the
// surviving trace of them.
//
// (§7.4 separately calls the memory chapter's GC a「daily job」. It is not a
// second opinion about item 2: §15 is the operational sweep list, it places
// memory archival in the 60s set, and aihub#236's much later comment in
// RunMemoryExpiredSweep reasons about what gets archived "on the next 60s tick".
// Following §7.4 instead would slow a sweep two subsequent changes have relied
// on being fast.)
//
// Of the three real exceptions, one is deliberately NOT restored: aihub#268
// rebuilt partition_create around the 60s cadence after the spec was written.
// The other two are restored here, and the reason to treat them differently from
// the five 60s sweeps survives even without the spec: they are the only two that
// EMIT. Every other sweep's write falsifies its own WHERE predicate — an
// archived memory is no longer 'active', a truncated payload no longer exceeds
// 64KB, an attached partition is skipped by isAttachedPartition — so running it
// 1,440x a day produces exactly the same rows as running it once, and costs only
// query time. An INSERT has no such property. Same mismatch, different cost.
//
// Per sweep:
//
//	orphan_lock_cleanup        every tick — §15 item 1, unmarked. A lock still
//	                           held by a dead attempt blocks the next claim of
//	                           that resource until this runs, and the aihub#145
//	                           retention contract is stated in terms of the 60s
//	                           tick. Idempotent: a deleted row cannot re-match.
//	memory_expired_archive     every tick — §15 item 2, inside the "60s tick"
//	                           header with no exception marker, and aihub#236's
//	                           comment reasons explicitly about what would be
//	                           archived "on the next 60s tick". Idempotent
//	                           (UPDATE off 'active'), so the cadence costs only
//	                           query time.
//	methodology_expiry_archive every tick — §15 item 3, likewise unmarked. Same
//	                           idempotent UPDATE shape as the sweep above.
//	event_payload_truncation   every tick — §15 item 4, unmarked. It bounds the
//	                           size of a row that is ALREADY written, so delay
//	                           leaves oversized payloads being served to readers.
//	                           Idempotent: a truncated payload no longer exceeds
//	                           64KB.
//	unblock_dependent_wi       every tick — §15 calls it exactly the 60s fallback
//	                           ("GC 60s tick 仅作兜底") behind
//	                           fn_complete_attempt's primary path, so here the
//	                           spec and the code already agree. Idempotent: an
//	                           unblocked wi is no longer 'blocked'.
//	partition_create           every tick — §15 item 6 says "daily tick 独立", but
//	                           aihub#268 later and deliberately built this sweep
//	                           around the 60s cadence: its lock_timeout exists so
//	                           that a contended run degrades to "a logged error
//	                           and a retry 60 seconds later". Retrying a
//	                           write-path outage tomorrow is a different
//	                           guarantee. Idempotent twice over —
//	                           isAttachedPartition skips existing months, and
//	                           reportDefaultBacklog's durable warning is already
//	                           rate-limited to one per hour by the same
//	                           WHERE NOT EXISTS shape this change adopts.
//
// The two alert sweeps carry gcAlertPollPeriod, NOT the daily cadence §15 asks
// for. That is deliberate and is explained where those constants are defined:
// the daily cadence is enforced by the SQL repeat window, and this period only
// decides how often the sweep asks. A period equal to the cadence would put the
// schedule's restart-randomised phase in charge of it instead.
func gcSweepTable() []gcSweep {
	return []gcSweep{
		{sweepOrphanLockCleanup, gcEveryTick, RunOrphanLockSweep},
		{sweepMemoryExpiredArchive, gcEveryTick, RunMemoryExpiredSweep},
		{sweepMethodologyExpiry, gcEveryTick, RunMethodologyExpiryArchive},
		{sweepEventPayloadTruncation, gcEveryTick, RunEventPayloadTruncation},
		{sweepUnblockDependentWI, gcEveryTick, RunUnblockDependentWI},
		{sweepPartitionCreate, gcEveryTick, RunPartitionCreate},
		{sweepNeedsHumanSessionAging, gcAlertPollPeriod, RunNeedsHumanSessionAging},
		{sweepUnclassifiedWIAlert, gcAlertPollPeriod, RunUnclassifiedWIAlert},
	}
}

// gcSchedule is RunDue's memory of when each sweep last ran in THIS process.
//
// In-process on purpose. Its ways of being bypassed are all benign, because it
// is not what prevents duplicate alerts — alertNotRepeatedSQL is:
//
//   - a restart forgets everything, so every sweep is due on the first tick
//     after start. Deliberate: a daily sweep that waited a full day after each
//     deploy would be dark most of the time on a frequently deployed service.
//     It is safe only because the SQL guard, not this map, stops re-emission.
//   - RunAll does not consult this map at all, so POST /v1/admin/gc runs the
//     sweep whenever an operator asks, however recently it last ran. That is the
//     point of that endpoint, and it is safe for the same reason.
//   - a multi-replica deployment would give each replica its own map, so a sweep
//     would be attempted once per replica per period. Listed because it costs
//     nothing if it ever happens, NOT because it is the case: measured
//     2026-09-01, production is one container against Cloud SQL, one
//     client_addr in pg_stat_activity, one sweep burst per minute.
//
// Every one of those would be a defect if this map were the guard. It is not; it
// decides only how often the database is asked.
type gcSchedule struct {
	mu      sync.Mutex
	lastRun map[string]time.Time
}

func newGCSchedule() *gcSchedule {
	return &gcSchedule{lastRun: make(map[string]time.Time)}
}

// due reports whether `name` is due at `now`, recording nothing. A gcEveryTick
// period is always due, and so is a sweep this process has never run.
//
// due+record is deliberately NOT atomic, unlike the single claim() it replaced,
// and the mutex below should not be read as making it so — it protects the map,
// not the check-then-act across the two calls. Two callers can therefore both
// find the same sweep due and both run it. Nothing does that today (one GC
// goroutine per process; RunAll does not touch the schedule at all), and if
// something ever did, the consequence is bounded twice over: tryAdvisoryLock
// makes the loser return Skipped, and the SQL repeat window makes a duplicate
// alert impossible even if it did not. Atomicity here would buy nothing and
// would cost the property that matters more — that the clock starts on a
// COMPLETED run.
func (s *gcSchedule) due(name string, period time.Duration, now time.Time) bool {
	if period <= 0 {
		return true
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	last, ran := s.lastRun[name]
	return !ran || now.Sub(last) >= period
}

// record marks `name` as having COMPLETED a run at `now`. Separate from due()
// rather than folded into one claim() call, because what starts the clock is a
// sweep that did its job — see runDueAt.
//
// A no-op for gcEveryTick sweeps: they are never throttled, so there is nothing
// for a recorded timestamp to be compared against, and writing one would put the
// "period 0 means unscheduled" rule in two places.
func (s *gcSchedule) record(name string, period time.Duration, now time.Time) {
	if period <= 0 {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lastRun[name] = now
}

// gcTickSchedule is the schedule RunDue throttles against: one per process,
// matching the single GC goroutine in cmd/aihub/main.go.
var gcTickSchedule = newGCSchedule()

// RunDue executes the sweeps whose period has elapsed since this process last
// ran them. It is the entry point for the background ticker.
//
// A sweep that is not due is omitted from the results rather than reported as a
// skip: main.go already mutes any sweep with Affected == 0, so "not due" and
// "nothing to do" print identically anyway, and inventing a third GCResult state
// for it would only give the admin endpoint's JSON a field that is always false.
func RunDue(ctx context.Context, pool *pgxpool.Pool) []GCResult {
	return runDueAt(ctx, pool, gcTickSchedule, time.Now())
}

// runDueAt is RunDue with the schedule and the clock injected, so that a test
// can drive a day of ticks without waiting a day.
func runDueAt(ctx context.Context, pool *pgxpool.Pool, sched *gcSchedule, now time.Time) []GCResult {
	table := gcSweepTable()
	results := make([]GCResult, 0, len(table))
	for _, s := range table {
		if !sched.due(s.Name, s.Period, now) {
			continue
		}
		r := s.Fn(ctx, pool)
		results = append(results, r)

		// The period starts from a run that actually DID something, not from one
		// that was attempted. A sweep that errored did not do its job, and one
		// that reported Skipped had its advisory lock held by the other
		// instance — neither should buy 24 hours of silence. Recording
		// unconditionally would turn a single transient database error into a
		// full day with no alert, and would make the instance that lost the lock
		// race stop trying even if the winner never finished.
		//
		// A partial failure is the case that makes this safe rather than merely
		// nicer: if the sweep emitted for three wis and then errored on a
		// fourth, it retries on the next tick and the repeat window suppresses
		// the three it already did, so the retry emits exactly the one it
		// missed. The guard being in the SQL is what lets the schedule be
		// pessimistic here for free.
		//
		// The price is that a PERSISTENTLY failing daily sweep retries every
		// tick and therefore logs every tick. That is already what all six
		// every-tick sweeps do on failure, stderr lines are the signal an
		// operator wants, and it is not the agent_events growth this change is
		// about.
		if r.Error == "" && !r.Skipped {
			sched.record(s.Name, s.Period, now)
		}
	}
	return results
}

// RunAll executes all 8 GC sweeps in sequence, ignoring the per-sweep periods.
//
// This is the admin path (POST /v1/admin/gc): an operator who asks for a sweep
// run must get one, not a silent no-op because the daily sweeps happened to run
// four hours ago. It remains safe to call at any frequency because the alert
// sweeps' idempotency lives in their SQL rather than in the schedule — a forced
// run cannot re-emit an alert that is already inside its repeat window.
//
// The background ticker uses RunDue instead.
func RunAll(ctx context.Context, pool *pgxpool.Pool) []GCResult {
	table := gcSweepTable()
	results := make([]GCResult, 0, len(table))
	for _, s := range table {
		results = append(results, s.Fn(ctx, pool))
	}
	return results
}
