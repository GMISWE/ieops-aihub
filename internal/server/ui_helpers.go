package server

import (
	"context"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/labstack/echo/v4"

	"github.com/GMISWE/ieops-aihub/internal/domain"
)

// themeCookieName is the cookie that carries the user's chosen UI theme.
// It is set client-side by static/theme.js (non-HttpOnly, a UI preference)
// and read here so the server can render <html data-theme="..."> with the
// correct theme on first paint — no flash of the wrong theme.
const themeCookieName = "theme"

// themeFromCookie returns the validated UI theme mode for the request: one of
// "auto", "light", or "dark". Any missing, malformed, or unrecognized cookie
// value falls back to the "auto" default (follow the OS color scheme). Used by
// every /ui page handler to thread .Theme into the layout template, which
// renders <html data-theme="..."> so CSS resolves the colors on first paint.
func themeFromCookie(c echo.Context) string {
	if ck, err := c.Cookie(themeCookieName); err == nil && ck != nil {
		switch ck.Value {
		case "light", "dark", "auto":
			return ck.Value
		}
	}
	return "auto"
}

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
	if pool == nil || attemptID == "" {
		return out
	}
	_ = pool.QueryRow(ctx,
		`SELECT actor_display, last_active_at FROM run_attempts WHERE id = $1`,
		attemptID,
	).Scan(&out.Display, &out.LastActiveAt)
	return out
}

// wiFacets holds the distinct reporter / owner display names available within
// a set of projects, used to populate the wi-list filter dropdowns.
type wiFacets struct {
	Reporters []string
	Owners    []string
}

// fetchWIFacets returns the sorted distinct reporter_display and current-attempt
// owner (run_attempts.actor_display) values across the given projects. An empty
// projects slice means "all projects" (admin view-all). Errors degrade to empty
// lists so the filter dropdowns simply show no options rather than 500ing.
func fetchWIFacets(ctx context.Context, pool *pgxpool.Pool, projects []string) wiFacets {
	var f wiFacets
	if pool == nil {
		return f
	}

	repWhere, ownWhere := "", ""
	args := []any{}
	if len(projects) > 0 {
		repWhere = "WHERE project = ANY($1) AND reporter_display <> ''"
		ownWhere = "WHERE wi.project = ANY($1) AND ra.actor_display <> ''"
		args = append(args, projects)
	} else {
		repWhere = "WHERE reporter_display <> ''"
		ownWhere = "WHERE ra.actor_display <> ''"
	}

	if rows, err := pool.Query(ctx,
		`SELECT DISTINCT reporter_display FROM work_items `+repWhere+` ORDER BY 1`, args...); err == nil {
		defer rows.Close()
		for rows.Next() {
			var s string
			if rows.Scan(&s) == nil {
				f.Reporters = append(f.Reporters, s)
			}
		}
	}

	if rows, err := pool.Query(ctx,
		`SELECT DISTINCT ra.actor_display
		 FROM run_attempts ra JOIN work_items wi ON wi.current_attempt_id = ra.id `+ownWhere+` ORDER BY 1`, args...); err == nil {
		defer rows.Close()
		for rows.Next() {
			var s string
			if rows.Scan(&s) == nil {
				f.Owners = append(f.Owners, s)
			}
		}
	}

	return f
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
		projs, _ := domain.ListProjects(ctx, pool, &domain.UserRecord{ID: u.UserID, Role: u.Role, ProjectScope: u.ProjectScope})
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

// uiScopeBlocks reports whether the caller's api-key project_scope forbids
// this project. A scoped key (any role, including admin) may touch only its
// one project on the /ui plane.
func uiScopeBlocks(u *UserContext, project string) bool {
	return u != nil && u.ProjectScope != nil && *u.ProjectScope != project
}

// personChip is the view-model for the prototype .who/.av person chip. Empty
// Display means "unassigned" and the template renders the dashed ghost avatar.
type personChip struct {
	Display    string // full display name shown next to the avatar
	Initials   string // 1-2 uppercase letters inside the avatar
	ColorClass string // one of the .av-c0..7 palette classes (presentational only)
}

// chipFor builds a personChip from a display name. The color class is a stable
// hash of the name into the fixed .av-c* palette — purely presentational, no
// backend meaning. An empty name yields a zero chip (ghost avatar in the
// template).
func chipFor(display string) personChip {
	display = strings.TrimSpace(display)
	if display == "" {
		return personChip{}
	}
	return personChip{
		Display:    display,
		Initials:   initialsFor(display),
		ColorClass: avatarColorClass(display),
	}
}

// initialsFor derives up to two uppercase initials from a display name. It
// prefers the first letters of the first two whitespace-separated words; for a
// single token it takes the first two letters. Non-letter leading characters
// (e.g. "@") are skipped so "@monte" -> "MO".
func initialsFor(name string) string {
	fields := strings.Fields(name)
	letters := make([]rune, 0, 2)
	for _, f := range fields {
		for _, r := range f {
			if unicode.IsLetter(r) || unicode.IsDigit(r) {
				letters = append(letters, unicode.ToUpper(r))
				break
			}
		}
		if len(letters) == 2 {
			break
		}
	}
	if len(letters) == 0 {
		// no word boundary produced a letter — fall back to first two runes.
		for _, r := range name {
			if unicode.IsLetter(r) || unicode.IsDigit(r) {
				letters = append(letters, unicode.ToUpper(r))
			}
			if len(letters) == 2 {
				break
			}
		}
	} else if len(letters) == 1 {
		// single word: add its second letter for a fuller chip.
		first := fields[0]
		seen := false
		for _, r := range first {
			if !unicode.IsLetter(r) && !unicode.IsDigit(r) {
				continue
			}
			if !seen {
				seen = true
				continue
			}
			letters = append(letters, unicode.ToUpper(r))
			break
		}
	}
	if len(letters) == 0 {
		return "?"
	}
	return string(letters)
}

// avatarColorClass maps a display name to one of the eight .av-c* palette
// classes via a small deterministic hash (FNV-style). Same name always gets
// the same color within and across renders.
func avatarColorClass(name string) string {
	var h uint32 = 2166136261
	for i := 0; i < len(name); i++ {
		h ^= uint32(name[i])
		h *= 16777619
	}
	return avatarPalette[h%uint32(len(avatarPalette))]
}

// avatarPalette is the fixed set of person-color classes defined in ui.css.
var avatarPalette = []string{
	"av-c0", "av-c1", "av-c2", "av-c3", "av-c4", "av-c5", "av-c6", "av-c7",
}

// fetchProjectWICounts returns the per-project count of active (queued +
// running + paused + blocked) work items across the given projects, plus the
// total. Used to annotate the project-switcher dropdown. View-layer only — a
// presentation count, not a domain/business change. An empty projects slice
// means "all projects" (admin view-all). Errors degrade to an empty map so the
// dropdown simply shows no counts rather than 500ing.
func fetchProjectWICounts(ctx context.Context, pool *pgxpool.Pool, projects []string) (map[string]int, int) {
	counts := map[string]int{}
	total := 0
	if pool == nil {
		return counts, total
	}
	active := []string{"queued", "running", "paused", "blocked"}
	query := `SELECT project, COUNT(*) FROM work_items WHERE status = ANY($1) GROUP BY project`
	args := []any{active}
	if len(projects) > 0 {
		query = `SELECT project, COUNT(*) FROM work_items
		         WHERE status = ANY($1) AND project = ANY($2) GROUP BY project`
		args = append(args, projects)
	}
	rows, err := pool.Query(ctx, query, args...)
	if err != nil {
		return counts, total
	}
	defer rows.Close()
	for rows.Next() {
		var p string
		var n int
		if rows.Scan(&p, &n) == nil {
			counts[p] = n
			total += n
		}
	}
	return counts, total
}

// fetchDoneCount returns the count of terminal (wrapped + cancelled + failed) work
// items for the Done sidebar segment (aihub#185). Mirrors fetchProjectWICounts'
// scoping: an empty projects slice means "all projects" (admin view-all). View-layer
// only — a presentation count; errors degrade to 0 so the sidebar simply shows no
// Done count rather than 500ing.
func fetchDoneCount(ctx context.Context, pool *pgxpool.Pool, projects []string) int {
	if pool == nil {
		return 0
	}
	query := `SELECT COUNT(*) FROM work_items WHERE status = ANY($1)`
	args := []any{[]string{"wrapped", "cancelled", "failed"}}
	if len(projects) > 0 {
		query += ` AND project = ANY($2)`
		args = append(args, projects)
	}
	var n int
	if err := pool.QueryRow(ctx, query, args...).Scan(&n); err != nil {
		return 0
	}
	return n
}
