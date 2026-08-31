package domain

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
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
	result := GCResult{SweepType: "orphan_lock_cleanup"}
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
	result := GCResult{SweepType: "memory_expired_archive"}
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
	result := GCResult{SweepType: "methodology_expiry_archive"}
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
	result := GCResult{SweepType: "event_payload_truncation"}
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
	result := GCResult{SweepType: "unblock_dependent_wi"}
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
	if _, err := pool.Exec(ctx, `
		INSERT INTO agent_events (id, event_type, payload, created_at)
		SELECT $1, 'system_gc', $2, clock_timestamp()
		WHERE NOT EXISTS (
		  SELECT 1 FROM agent_events
		  WHERE event_type = 'system_gc'
		    AND payload->>'sweep' = 'partition_create'
		    AND payload ? 'default_partition_rows'
		    AND created_at > clock_timestamp() - interval '1 hour'
		)`, NewID("evt"), payload); err != nil {
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
	result := GCResult{SweepType: "partition_create"}
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

// ─── Sweep 7: Needs Human Session Aging (daily) ──────────────────────────────

// RunNeedsHumanSessionAging emits wi_needs_attention for queued requires_human_session=true
// work_items that have been waiting too long (§15 sweep 7).
func RunNeedsHumanSessionAging(ctx context.Context, pool *pgxpool.Pool) GCResult {
	result := GCResult{SweepType: "needs_human_session_aging"}
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

	rows, err := pool.Query(ctx, `
		SELECT id, slug, wi_type, priority, project, created_at
		FROM work_items
		WHERE requires_human_session = true
		  AND status = 'queued'
		  AND created_at < now() - CASE priority
		      WHEN 'urgent' THEN interval '1 day'
		      ELSE interval '7 days'
		    END`)
	if err != nil {
		result.Error = fmt.Sprintf("needs_human_session query: %v", err)
		return result
	}
	defer rows.Close()

	type wiRow struct {
		ID, Slug, WIType, Priority, Project string
		CreatedAt                           time.Time
	}
	var wis []wiRow
	for rows.Next() {
		var w wiRow
		if err := rows.Scan(&w.ID, &w.Slug, &w.WIType, &w.Priority, &w.Project, &w.CreatedAt); err == nil {
			wis = append(wis, w)
		}
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
		_, err := pool.Exec(ctx, `
			INSERT INTO agent_events (id, work_item_id, event_type, payload, project, created_at)
			VALUES ($1, $2, 'wi_needs_attention', $3, $4, clock_timestamp())`,
			NewID("evt"), w.ID, payload, w.Project)
		if err == nil {
			affected++
		}
	}
	result.Affected = affected
	return result
}

// ─── Sweep 8: Unclassified WI Alert (daily) ──────────────────────────────────

// RunUnclassifiedWIAlert emits wi_classification_missing for old unclassified work_items.
func RunUnclassifiedWIAlert(ctx context.Context, pool *pgxpool.Pool) GCResult {
	result := GCResult{SweepType: "unclassified_wi_alert"}
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

	rows, err := pool.Query(ctx, `
		SELECT id, slug, project, reporter_user_id
		FROM work_items
		WHERE requires_human_session IS NULL
		  AND status = 'queued'
		  AND created_at < now() - interval '1 day'`)
	if err != nil {
		result.Error = fmt.Sprintf("unclassified wi query: %v", err)
		return result
	}
	defer rows.Close()

	type wiRow struct{ ID, Slug, Project, ReporterID string }
	var wis []wiRow
	for rows.Next() {
		var w wiRow
		if err := rows.Scan(&w.ID, &w.Slug, &w.Project, &w.ReporterID); err == nil {
			wis = append(wis, w)
		}
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
		_, err := pool.Exec(ctx, `
			INSERT INTO agent_events (id, work_item_id, event_type, payload, project, created_at)
			VALUES ($1, $2, 'wi_classification_missing', $3, $4, clock_timestamp())`,
			NewID("evt"), w.ID, payload, w.Project)
		if err == nil {
			affected++
		}
	}
	result.Affected = affected
	return result
}

// ─── RunAll ───────────────────────────────────────────────────────────────────

// RunAll executes all 8 GC sweeps (the 60s-tick set) in sequence.
func RunAll(ctx context.Context, pool *pgxpool.Pool) []GCResult {
	sweeps := []func(context.Context, *pgxpool.Pool) GCResult{
		RunOrphanLockSweep,
		RunMemoryExpiredSweep,
		RunMethodologyExpiryArchive,
		RunEventPayloadTruncation,
		RunUnblockDependentWI,
		RunPartitionCreate,
		RunNeedsHumanSessionAging,
		RunUnclassifiedWIAlert,
	}

	results := make([]GCResult, 0, len(sweeps))
	for _, sweep := range sweeps {
		results = append(results, sweep(ctx, pool))
	}
	return results
}
