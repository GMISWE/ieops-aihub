package domain

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Advisory lock IDs for GC sweeps (pg_try_advisory_lock).
// Each sweep has its own lock ID to allow independent concurrent sweeps.
const (
	gcLockOrphanLocks        = int64(2001)
	gcLockMemoryExpired      = int64(2002)
	gcLockMethodologyExpiry  = int64(2003)
	gcLockEventPayloadTrunc  = int64(2004)
	gcLockUnblockDependent   = int64(2005)
	gcLockPartitionCreate    = int64(2006)
	gcLockNeedsHumanAging    = int64(2007)
	gcLockUnclassifiedAlert  = int64(2008)
	gcLockZombieSweep        = int64(2009) // 5-minute tick
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

// RunOrphanLockSweep removes resource_locks whose owner_attempt_id points to a non-running attempt.
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

	tag, err := pool.Exec(ctx, `
		DELETE FROM resource_locks rl
		WHERE NOT EXISTS (
			SELECT 1 FROM run_attempts ra
			WHERE ra.id = rl.owner_attempt_id AND ra.status = 'running'
		)`)
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

	tag, err := pool.Exec(ctx, `
		UPDATE memories
		SET status = 'archived', updated_at = clock_timestamp()
		WHERE status = 'active'
		  AND is_immortal = FALSE
		  AND (
		    base_strength * exp(
		      -extract(epoch FROM (clock_timestamp() - COALESCE(last_activated_at, created_at))) / 86400.0
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

	tag, err := tx.Exec(ctx, `
		UPDATE work_items wi
		SET status = 'queued', updated_at = clock_timestamp()
		WHERE wi.status = 'blocked'
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

// ─── Sweep 6: Partition Creator (daily) ──────────────────────────────────────

// RunPartitionCreate ensures agent_events monthly partitions exist 60 days ahead.
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

	// Create partitions for the next 60 days (month boundaries)
	now := time.Now().UTC()
	created := int64(0)
	for i := 0; i <= 2; i++ { // current month + 2 ahead = covers 60 days
		month := now.AddDate(0, i, 0)
		year := month.Year()
		mo := int(month.Month())
		start := fmt.Sprintf("%d-%02d-01", year, mo)
		endMonth := month.AddDate(0, 1, 0)
		end := fmt.Sprintf("%d-%02d-01", endMonth.Year(), int(endMonth.Month()))
		name := fmt.Sprintf("agent_events_%d_%02d", year, mo)

		tag, err := pool.Exec(ctx, fmt.Sprintf(`
			CREATE TABLE IF NOT EXISTS %s
			PARTITION OF agent_events
			FOR VALUES FROM ('%s') TO ('%s')`, name, start, end))
		if err != nil {
			// Non-fatal: log and continue
			result.Error = fmt.Sprintf("create partition %s: %v", name, err)
			continue
		}
		created += tag.RowsAffected()
	}
	result.Affected = created
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
			"wi_id":     w.ID,
			"wi_slug":   w.Slug,
			"wi_type":   w.WIType,
			"priority":  w.Priority,
			"waiting_since": w.CreatedAt,
			"reason":    "requires_human_session=true, no claim after aging threshold",
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

// ─── Zombie Sweeper (5-min tick) ─────────────────────────────────────────────

// RunZombieSweep finds run_attempts with last_active_at > 24h and performs a system
// force_takeover: releases locks, sets attempt.status='lost', wi.status='paused',
// emits attempt_zombied + wi_possibly_abandoned events (C-R9-5).
func RunZombieSweep(ctx context.Context, pool *pgxpool.Pool) GCResult {
	result := GCResult{SweepType: "zombie_sweep"}
	acquired, release, err := tryAdvisoryLock(ctx, pool, gcLockZombieSweep)
	if err != nil {
		result.Error = err.Error()
		return result
	}
	if !acquired {
		result.Skipped = true
		return result
	}
	defer release()

	// Find zombie attempts: running for > 24h without activity
	rows, err := pool.Query(ctx, `
		SELECT ra.id, ra.work_item_id, ra.actor_display, wi.project, wi.slug
		FROM run_attempts ra
		JOIN work_items wi ON wi.id = ra.work_item_id
		WHERE ra.status = 'running'
		  AND ra.last_active_at < now() - interval '24 hours'`)
	if err != nil {
		result.Error = fmt.Sprintf("zombie query: %v", err)
		return result
	}
	defer rows.Close()

	type zombieRow struct{ AttemptID, WIID, ActorDisplay, Project, WISlug string }
	var zombies []zombieRow
	for rows.Next() {
		var z zombieRow
		if err := rows.Scan(&z.AttemptID, &z.WIID, &z.ActorDisplay, &z.Project, &z.WISlug); err == nil {
			zombies = append(zombies, z)
		}
	}
	rows.Close()

	affected := int64(0)
	for _, z := range zombies {
		if err := runZombieForce(ctx, pool, z.AttemptID, z.WIID, z.ActorDisplay, z.Project, z.WISlug); err == nil {
			affected++
		}
	}
	result.Affected = affected
	return result
}

// runZombieForce performs a single zombie force_takeover atomically.
func runZombieForce(ctx context.Context, pool *pgxpool.Pool, attemptID, wiID, priorDisplay, project, wiSlug string) error {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	// Mark attempt as lost
	_, err = tx.Exec(ctx, `
		UPDATE run_attempts SET status = 'lost', ended_at = clock_timestamp()
		WHERE id = $1 AND status = 'running'`, attemptID)
	if err != nil {
		return err
	}

	// Release resource locks
	tx.Exec(ctx, `DELETE FROM resource_locks WHERE owner_attempt_id = $1`, attemptID) //nolint:errcheck

	// Set wi.status to paused (resumable)
	tx.Exec(ctx, `
		UPDATE work_items SET status = 'paused', current_attempt_id = NULL, updated_at = clock_timestamp()
		WHERE id = $1 AND status = 'running'`, wiID) //nolint:errcheck

	// Emit attempt_zombied event
	zombiedPayload, _ := json.Marshal(map[string]any{
		"attempt_id":    attemptID,
		"prior_display": priorDisplay,
		"reason":        "last_active_at exceeded 24h threshold",
	})
	tx.Exec(ctx, `
		INSERT INTO agent_events (id, work_item_id, event_type, payload, project, created_at)
		VALUES ($1, $2, 'attempt_zombied', $3, $4, clock_timestamp())`,
		NewID("evt"), wiID, zombiedPayload, project) //nolint:errcheck

	// Emit wi_possibly_abandoned event
	abandonedPayload, _ := json.Marshal(map[string]any{
		"wi_id":         wiID,
		"wi_slug":       wiSlug,
		"attempt_id":    attemptID,
		"prior_display": priorDisplay,
	})
	tx.Exec(ctx, `
		INSERT INTO agent_events (id, work_item_id, event_type, payload, project, created_at)
		VALUES ($1, $2, 'wi_possibly_abandoned', $3, $4, clock_timestamp())`,
		NewID("evt"), wiID, abandonedPayload, project) //nolint:errcheck

	return tx.Commit(ctx)
}

// ─── RunAll ───────────────────────────────────────────────────────────────────

// RunAll executes all 8 GC sweeps (the 60s-tick set) in sequence.
// The zombie sweeper (5-min tick) is also included here for completeness.
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
		RunZombieSweep,
	}

	results := make([]GCResult, 0, len(sweeps))
	for _, sweep := range sweeps {
		results = append(results, sweep(ctx, pool))
	}
	return results
}
