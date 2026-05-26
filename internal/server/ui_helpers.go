package server

import (
	"context"
	"sort"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/GMISWE/ieops-aihub/internal/domain"
)

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
