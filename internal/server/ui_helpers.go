package server

import (
	"context"
	"sort"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/GMISWE/ieops-aihub/internal/domain"
)

// attemptOwner is the slim projection of a run_attempts row used by /ui to
// display "claimed by / last active" without pulling the whole attempt struct.
type attemptOwner struct {
	Display      string
	LastActiveAt time.Time
}

// fetchAttemptOwner returns the actor_display + last_active_at for a single
// run_attempts row. Returns zero values (empty Display, zero time) on miss or
// query error so the caller can render "—" without branching on err.
func fetchAttemptOwner(ctx context.Context, pool *pgxpool.Pool, attemptID string) attemptOwner {
	var out attemptOwner
	if attemptID == "" {
		return out
	}
	_ = pool.QueryRow(ctx,
		`SELECT actor_display, last_active_at FROM run_attempts WHERE id = $1`,
		attemptID,
	).Scan(&out.Display, &out.LastActiveAt)
	return out
}

// fetchAttemptOwners is the batched form of fetchAttemptOwner for use on the
// wi list page, which would otherwise issue N+1 queries.
func fetchAttemptOwners(ctx context.Context, pool *pgxpool.Pool, attemptIDs []string) map[string]attemptOwner {
	out := map[string]attemptOwner{}
	if len(attemptIDs) == 0 {
		return out
	}
	rows, err := pool.Query(ctx,
		`SELECT id, actor_display, last_active_at FROM run_attempts WHERE id = ANY($1)`,
		attemptIDs,
	)
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		var o attemptOwner
		if rows.Scan(&id, &o.Display, &o.LastActiveAt) == nil {
			out[id] = o
		}
	}
	return out
}

// availableProjectsForUI returns the project names the user should see in
// a page's project picker.
//
// For non-admin users this is the user's ProjectRoles map keys.
//
// For admin users — who have an empty ProjectRoles by design
// (middleware.go ~L104-106) — this falls back to all visible projects via
// domain.ListProjects so the picker isn't empty. Without this fallback an
// admin lands on /ui/queue with no project selectable and zero rows.
func availableProjectsForUI(ctx context.Context, pool *pgxpool.Pool, u *UserContext) []string {
	if u == nil {
		return nil
	}
	if u.Role == "admin" {
		projs, _ := domain.ListProjects(ctx, pool, &domain.UserRecord{ID: u.UserID, Role: u.Role})
		out := make([]string, 0, len(projs))
		for _, p := range projs {
			out = append(out, p.Name)
		}
		sort.Strings(out)
		return out
	}
	out := make([]string, 0, len(u.ProjectRoles))
	for p := range u.ProjectRoles {
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}
