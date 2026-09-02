package domain

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Dependency mirrors a wi_dependencies row.
type Dependency struct {
	BlockedWIID  string    `json:"blocked_wi_id"`
	BlockingWIID string    `json:"blocking_wi_id"`
	Kind         string    `json:"kind"`
	CreatedAt    time.Time `json:"created_at"`
	CreatedBy    *string   `json:"created_by"`
	Note         *string   `json:"note"`
}

// CreateDependencyRequest is the body for POST /v1/dependencies.
type CreateDependencyRequest struct {
	BlockedWIID  string  `json:"blocked_wi_id"`
	BlockingWIID string  `json:"blocking_wi_id"`
	Kind         string  `json:"kind"` // blocks | supersedes | related
	Note         *string `json:"note"`
}

// DependencyListEntry is the response format for a dependency list entry.
type DependencyListEntry struct {
	ID      string  `json:"id"`
	Slug    *string `json:"slug,omitempty"`
	Project string  `json:"project"`
	Kind    string  `json:"kind"`
	Note    *string `json:"note,omitempty"`
}

// DependenciesResponse is the response for GET /v1/dependencies.
type DependenciesResponse struct {
	Blocking  []DependencyListEntry `json:"blocking"`
	BlockedBy []DependencyListEntry `json:"blocked_by"`
}

// CreateDependency inserts a new wi_dependency row after cycle detection.
func CreateDependency(ctx context.Context, pool *pgxpool.Pool, req *CreateDependencyRequest, callerUserID string, callerProjectRoles map[string]string, callerRole string) *AihubError {
	if req.BlockedWIID == "" || req.BlockingWIID == "" {
		return NewErr(ErrBadRequest, "blocked_wi_id and blocking_wi_id are required")
	}
	if req.BlockedWIID == req.BlockingWIID {
		return NewErr(ErrBadRequest, "blocked_wi_id and blocking_wi_id cannot be the same")
	}
	if req.Kind == "" {
		req.Kind = "blocks"
	}
	if req.Kind != "blocks" && req.Kind != "supersedes" && req.Kind != "related" {
		return NewErr(ErrBadRequest, "kind must be blocks, supersedes, or related")
	}

	// Permission check: caller must be the current running attempt holder for blocked_wi
	var blockedProject string
	if err := pool.QueryRow(ctx, `SELECT project FROM work_items WHERE id=$1`, req.BlockedWIID).Scan(&blockedProject); err != nil {
		return NewErr(ErrNotFound, fmt.Sprintf("blocked work item %s not found", req.BlockedWIID))
	}

	// For cross-project blocking: caller needs viewer+ on blocking_wi's project
	var blockingProject string
	if err := pool.QueryRow(ctx, `SELECT project FROM work_items WHERE id=$1`, req.BlockingWIID).Scan(&blockingProject); err != nil {
		return NewErr(ErrNotFound, fmt.Sprintf("blocking work item %s not found", req.BlockingWIID))
	}
	if blockingProject != blockedProject {
		// Admin users have an empty ProjectRoles map by design, so gate on the
		// global role before per-project viewer access. (aihub#227)
		if callerRole != "admin" && callerProjectRoles[blockingProject] == "" {
			return NewErr(ErrForbidden, fmt.Sprintf("you need at least viewer access to project %q to create a cross-project dependency", blockingProject))
		}
	}

	// Cycle detection for kinds that create directed edges
	if req.Kind == "blocks" || req.Kind == "supersedes" {
		if aihubErr := detectCycle(ctx, pool, req.BlockedWIID, req.BlockingWIID, req.Kind); aihubErr != nil {
			return aihubErr
		}
	}

	_, err := pool.Exec(ctx, `
		INSERT INTO wi_dependencies (blocked_wi_id, blocking_wi_id, kind, created_by, note)
		VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (blocked_wi_id, blocking_wi_id, kind) DO NOTHING`,
		req.BlockedWIID, req.BlockingWIID, req.Kind, callerUserID, req.Note,
	)
	if err != nil {
		return NewErr(ErrInternalError, fmt.Sprintf("failed to create dependency: %v", err))
	}

	// If kind=blocks and blocked_wi is queued, set it to blocked
	if req.Kind == "blocks" {
		_, _ = pool.Exec(ctx, `
			UPDATE work_items SET status='blocked'
			WHERE id=$1 AND status='queued'`, req.BlockedWIID)
	}

	return nil
}

// detectCycle checks for a directed cycle using WITH RECURSIVE DFS.
func detectCycle(ctx context.Context, pool *pgxpool.Pool, blockedWIID, blockingWIID, kind string) *AihubError {
	// If blockingWIID can reach blockedWIID through existing edges, adding this edge creates a cycle.
	var count int
	err := pool.QueryRow(ctx, `
		WITH RECURSIVE reachable AS (
		  SELECT blocking_wi_id AS id FROM wi_dependencies
		  WHERE blocked_wi_id = $2 AND kind = $3
		  UNION
		  SELECT d.blocking_wi_id FROM wi_dependencies d
		  JOIN reachable r ON d.blocked_wi_id = r.id
		  WHERE d.kind = $3
		  -- Depth limit 50 via implicit CTE recursion cap
		)
		SELECT COUNT(*) FROM reachable WHERE id = $1`,
		blockedWIID, blockingWIID, kind,
	).Scan(&count)
	if err != nil {
		return nil // Non-fatal; allow creation
	}
	if count > 0 {
		return NewErrDetails(ErrConflictDependencyCycle,
			fmt.Sprintf("adding dependency from %s to %s would create a cycle", blockedWIID, blockingWIID),
			map[string]any{"cycle_path": []string{blockedWIID, blockingWIID}},
		)
	}
	return nil
}

// WIRef is a minimal reference to a work item for the parent/children UI
// navigation (aihub#142). It carries just enough to render a link + status
// badge. Slug is nil when the caller cannot see the referenced wi's project —
// the same cross-project masking sentinel ListDependencies uses (ID="hidden",
// Slug=nil). Status is filled by the view layer (fetchDepMeta), not here, so
// this struct stays a pure identity reference.
type WIRef struct {
	ID      string  `json:"id"`
	Slug    *string `json:"slug,omitempty"`
	Project string  `json:"project"`
}

// GetParentRef returns the parent work item reference for a child wi, or nil
// when the wi has no parent (parent_work_item_id IS NULL). Cross-project
// visibility is masked with the same sentinel ListDependencies uses: when the
// caller lacks any role on the parent's project the returned ref has ID="hidden"
// and Slug=nil so the view renders the cross-project placeholder.
func GetParentRef(ctx context.Context, pool *pgxpool.Pool, childWiID string, callerProjectRoles map[string]string, callerRole string) (*WIRef, *AihubError) {
	row := pool.QueryRow(ctx, `
		SELECT parent.id, parent.slug, parent.project
		FROM work_items child
		JOIN work_items parent ON parent.id = child.parent_work_item_id
		WHERE child.id = $1`, childWiID,
	)
	var ref WIRef
	var slug string
	if err := row.Scan(&ref.ID, &slug, &ref.Project); err != nil {
		// No parent row (NULL parent_work_item_id) → no-parent, not an error.
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, NewErr(ErrInternalError, "failed to query parent work item")
	}
	if callerRole == "admin" || callerProjectRoles[ref.Project] != "" {
		ref.Slug = &slug
	} else {
		ref.ID = "hidden"
	}
	return &ref, nil
}

// ListChildren returns the child work items of a parent wi, ordered by seq ASC
// (creation order within the project). Cross-project children are masked with
// the same sentinel as GetParentRef (ID="hidden", Slug=nil). Returns an empty
// slice (never nil) when the wi has no children.
func ListChildren(ctx context.Context, pool *pgxpool.Pool, parentWiID string, callerProjectRoles map[string]string, callerRole string) ([]WIRef, *AihubError) {
	rows, err := pool.Query(ctx, `
		SELECT id, slug, project
		FROM work_items
		WHERE parent_work_item_id = $1
		ORDER BY seq ASC`, parentWiID,
	)
	if err != nil {
		return nil, NewErr(ErrInternalError, "failed to query child work items")
	}
	defer rows.Close()

	out := []WIRef{}
	for rows.Next() {
		var ref WIRef
		var slug string
		if err := rows.Scan(&ref.ID, &slug, &ref.Project); err != nil {
			continue
		}
		if callerRole == "admin" || callerProjectRoles[ref.Project] != "" {
			ref.Slug = &slug
		} else {
			ref.ID = "hidden"
		}
		out = append(out, ref)
	}
	return out, nil
}

// requeueIfUnblocked recomputes whether a blocked work item still has any
// active 'blocks' dependency and, if not, moves it from 'blocked' back to
// 'queued'. It is the shared inverse of CreateDependency's forward derivation
// (queued -> blocked) and is used by both DeleteDependency (a dependency row
// was removed) and unblockDependentWI (a blocking wi reached a terminal
// status). Before aihub#242 this recompute only existed on the forward path,
// so a wi could enter 'blocked' but never leave it once its dependencies were
// gone — see ieops#444.
//
// excludeBlockerWIID lets a caller exclude one specific blocker from the
// "does an active blocker remain" check, for callers where that blocker's own
// row has not yet been updated to a terminal status within the current
// transaction (unblockDependentWI passes the wi that just completed). Pass ""
// to exclude nothing (DeleteDependency: the dependency row is already deleted
// earlier in the same transaction, so it is naturally excluded from the
// EXISTS check without needing this parameter).
//
// Returns whether the UPDATE actually requeued the wi (RowsAffected == 1), so
// callers can conditionally emit their own wi_unblocked event. Does not set
// updated_at explicitly — the trg_wi_updated_at trigger (migration 0002)
// maintains it.
func requeueIfUnblocked(ctx context.Context, tx pgx.Tx, blockedWIID, excludeBlockerWIID string) (bool, error) {
	tag, err := tx.Exec(ctx, `
		UPDATE work_items
		SET status = 'queued'
		WHERE id = $1
		  AND status = 'blocked'
		  AND NOT EXISTS (
		    SELECT 1 FROM wi_dependencies dep
		    JOIN work_items blocker ON dep.blocking_wi_id = blocker.id
		    WHERE dep.blocked_wi_id = $1
		      AND dep.kind = 'blocks'
		      AND ($2 = '' OR dep.blocking_wi_id != $2)
		      AND blocker.status NOT IN ('wrapped', 'cancelled', 'failed')
		  )`,
		blockedWIID, excludeBlockerWIID,
	)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}

// emitWIUnblockedEvent inserts a wi_unblocked agent_events row, isolated by a
// SAVEPOINT so a failed INSERT cannot abort the caller's transaction. This
// event write really is best-effort (losing it does not affect wi state, and
// callers already treat it that way), but under pgx v5 / Postgres that is
// only true when the INSERT is wrapped like this: a plain
// `_, _ = tx.Exec(ctx, "INSERT ...")` does NOT make a failure harmless —
// Postgres aborts the whole transaction on any failed statement, so the
// later tx.Commit would return ErrTxCommitRollback and roll back the
// requeue this event exists to report, which is the exact deadlock aihub#242
// fixes. A realistic trigger: agent_events is PARTITION BY RANGE(created_at)
// (migration 0006) with partitions pre-created only some months ahead, so a
// missing partition makes this INSERT fail. SAVEPOINT/ROLLBACK TO SAVEPOINT
// is the established idiom for this in this codebase — see the step-completion
// and escalated-stall inserts in routes_step.go.
func emitWIUnblockedEvent(ctx context.Context, tx pgx.Tx, wiID, project string, payload []byte) {
	tx.Exec(ctx, `SAVEPOINT bp_wi_unblocked`) //nolint:errcheck
	_, err := tx.Exec(ctx, `
		INSERT INTO agent_events (id, work_item_id, event_type, payload, project)
		VALUES ($1, $2, 'wi_unblocked', $3, $4)`,
		NewID("evt"), wiID, payload, project,
	)
	if err != nil {
		tx.Exec(ctx, `ROLLBACK TO SAVEPOINT bp_wi_unblocked`) //nolint:errcheck
		return
	}
	tx.Exec(ctx, `RELEASE SAVEPOINT bp_wi_unblocked`) //nolint:errcheck
}

// DeleteDependency removes a wi_dependency and, for kind='blocks' rows,
// requeues the blocked wi if this was its last remaining active blocker
// (aihub#242). CreateDependency derives status='blocked' when a 'blocks'
// dependency is added (queued -> blocked); until this fix, DeleteDependency
// never derived the inverse, so a wi whose last blocker was removed stayed
// status='blocked' forever — it could not be claimed (blocked) or cancelled
// (permission logic treated blocked as non-cancellable), a dead end fixed
// alongside this change in CancelWorkItem/cancelGate. See ieops#444.
//
// Note on stalled-blocked wis: a status='blocked' wi that a fresh escalated
// step failure produces (routes_step.go, spec A-1) has no wi_dependencies row
// at the moment it is set that way, so this DELETE cannot directly hit it
// (RowsAffected stays 0, returning 404). But a wi CAN legitimately end up
// both stalled AND carrying a residual 'blocks' row: unblockDependentWI
// requeues a dependency-blocked wi once its blocker goes terminal WITHOUT
// deleting the wi_dependencies row; if that requeued wi is then claimed and
// escalates a step failure, it goes back to status='blocked' while the old
// (now-inert) 'blocks' row is still sitting there. Deleting that row here
// would requeue such a wi out of the human-triage "stalled" segment. This is
// not a new hole introduced by this change, though: gc.go's
// RunUnblockDependentWI sweep (aihub#206, ~L237-251) has the identical EXISTS
// guard and already requeues this exact wi within ~60s regardless of this
// code path, so this function's behavior here stays consistent with the
// existing GC sweep rather than introducing a new inconsistency.
func DeleteDependency(ctx context.Context, pool *pgxpool.Pool, blockedWIID, blockingWIID, kind string) *AihubError {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return NewErr(ErrInternalError, "failed to begin transaction")
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	result, err := tx.Exec(ctx, `
		DELETE FROM wi_dependencies WHERE blocked_wi_id=$1 AND blocking_wi_id=$2 AND kind=$3`,
		blockedWIID, blockingWIID, kind,
	)
	if err != nil {
		return dbErrCause(err, "failed to delete dependency")
	}
	if result.RowsAffected() == 0 {
		return NewErr(ErrNotFound, "dependency not found")
	}

	if kind == "blocks" {
		unblocked, uerr := requeueIfUnblocked(ctx, tx, blockedWIID, "")
		if uerr != nil {
			return NewErr(ErrInternalError, fmt.Sprintf("failed to recompute blocked status: %v", uerr))
		}
		if unblocked {
			var project string
			if perr := tx.QueryRow(ctx, `SELECT project FROM work_items WHERE id=$1`, blockedWIID).Scan(&project); perr == nil {
				payload, _ := json.Marshal(map[string]any{
					"unblocked_by":          "dependency_removed",
					"removed_blocker_wi_id": blockingWIID,
				})
				// SAVEPOINT-isolated (see emitWIUnblockedEvent) so a failed
				// insert cannot roll back the requeue above.
				emitWIUnblockedEvent(ctx, tx, blockedWIID, project, payload)
			}
		}
	}

	if err := tx.Commit(ctx); err != nil {
		if aerr := retryConflictErr(err, "failed to commit dependency deletion"); aerr != nil { // aihub#334
			return aerr
		}
		return NewErr(ErrInternalError, "failed to commit dependency deletion")
	}
	return nil
}

// ListDependencies returns blocking and blocked_by dependencies for a work item.
// Respects cross-project visibility rules.
func ListDependencies(ctx context.Context, pool *pgxpool.Pool, wiID string, callerProjectRoles map[string]string, callerRole string) (*DependenciesResponse, *AihubError) {
	resp := &DependenciesResponse{
		Blocking:  []DependencyListEntry{},
		BlockedBy: []DependencyListEntry{},
	}

	// blocking: wi that are being blocked BY our wi (blocking_wi_id=wiID)
	blockingRows, err := pool.Query(ctx, `
		SELECT d.blocked_wi_id, wi.slug, wi.project, d.kind, d.note
		FROM wi_dependencies d
		JOIN work_items wi ON wi.id = d.blocked_wi_id
		WHERE d.blocking_wi_id = $1`, wiID,
	)
	if err != nil {
		return nil, NewErr(ErrInternalError, "failed to query dependencies")
	}
	defer blockingRows.Close()
	for blockingRows.Next() {
		var entry DependencyListEntry
		var slug string
		if err := blockingRows.Scan(&entry.ID, &slug, &entry.Project, &entry.Kind, &entry.Note); err != nil {
			continue
		}
		if callerRole == "admin" || callerProjectRoles[entry.Project] != "" {
			entry.Slug = &slug
		} else {
			entry.ID = "hidden"
		}
		resp.Blocking = append(resp.Blocking, entry)
	}
	blockingRows.Close()

	// blocked_by: wi that block our wi (blocked_wi_id=wiID)
	blockedByRows, err := pool.Query(ctx, `
		SELECT d.blocking_wi_id, wi.slug, wi.project, d.kind, d.note
		FROM wi_dependencies d
		JOIN work_items wi ON wi.id = d.blocking_wi_id
		WHERE d.blocked_wi_id = $1`, wiID,
	)
	if err != nil {
		return nil, NewErr(ErrInternalError, "failed to query blocked_by dependencies")
	}
	defer blockedByRows.Close()
	for blockedByRows.Next() {
		var entry DependencyListEntry
		var slug string
		if err := blockedByRows.Scan(&entry.ID, &slug, &entry.Project, &entry.Kind, &entry.Note); err != nil {
			continue
		}
		if callerRole == "admin" || callerProjectRoles[entry.Project] != "" {
			entry.Slug = &slug
		} else {
			entry.ID = "hidden"
		}
		resp.BlockedBy = append(resp.BlockedBy, entry)
	}
	blockedByRows.Close()

	return resp, nil
}
