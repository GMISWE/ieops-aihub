package domain

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Dependency mirrors a wi_dependencies row.
type Dependency struct {
	BlockedWIID  string     `json:"blocked_wi_id"`
	BlockingWIID string     `json:"blocking_wi_id"`
	Kind         string     `json:"kind"`
	CreatedAt    time.Time  `json:"created_at"`
	CreatedBy    *string    `json:"created_by"`
	Note         *string    `json:"note"`
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
func CreateDependency(ctx context.Context, pool *pgxpool.Pool, req *CreateDependencyRequest, callerUserID string, callerProjectRoles map[string]string) *AihubError {
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
		role := callerProjectRoles[blockingProject]
		if role == "" {
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

// DeleteDependency removes a wi_dependency.
func DeleteDependency(ctx context.Context, pool *pgxpool.Pool, blockedWIID, blockingWIID, kind string) *AihubError {
	result, err := pool.Exec(ctx, `
		DELETE FROM wi_dependencies WHERE blocked_wi_id=$1 AND blocking_wi_id=$2 AND kind=$3`,
		blockedWIID, blockingWIID, kind,
	)
	if err != nil {
		return NewErr(ErrInternalError, fmt.Sprintf("failed to delete dependency: %v", err))
	}
	if result.RowsAffected() == 0 {
		return NewErr(ErrNotFound, "dependency not found")
	}
	return nil
}

// ListDependencies returns blocking and blocked_by dependencies for a work item.
// Respects cross-project visibility rules.
func ListDependencies(ctx context.Context, pool *pgxpool.Pool, wiID string, callerProjectRoles map[string]string) (*DependenciesResponse, *AihubError) {
	resp := &DependenciesResponse{
		Blocking:  []DependencyListEntry{},
		BlockedBy: []DependencyListEntry{},
	}

	// blocking: wi that are being blocked BY our wi (blocked_wi_id=wiID)
	blockingRows, err := pool.Query(ctx, `
		SELECT d.blocking_wi_id, wi.slug, wi.project, d.kind, d.note
		FROM wi_dependencies d
		JOIN work_items wi ON wi.id = d.blocking_wi_id
		WHERE d.blocked_wi_id = $1`, wiID,
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
		if callerProjectRoles[entry.Project] != "" {
			entry.Slug = &slug
		} else {
			entry.ID = "hidden"
		}
		resp.Blocking = append(resp.Blocking, entry)
	}
	blockingRows.Close()

	// blocked_by: wi that block our wi (blocking_wi_id=wiID)
	blockedByRows, err := pool.Query(ctx, `
		SELECT d.blocked_wi_id, wi.slug, wi.project, d.kind, d.note
		FROM wi_dependencies d
		JOIN work_items wi ON wi.id = d.blocked_wi_id
		WHERE d.blocking_wi_id = $1`, wiID,
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
		if callerProjectRoles[entry.Project] != "" {
			entry.Slug = &slug
		} else {
			entry.ID = "hidden"
		}
		resp.BlockedBy = append(resp.BlockedBy, entry)
	}
	blockedByRows.Close()

	return resp, nil
}
