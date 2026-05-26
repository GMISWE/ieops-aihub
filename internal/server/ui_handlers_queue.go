package server

// Web UI: /ui/queue — six-segment LCRS ready-queue view for a project.
//
// Two endpoints:
//
//   GET /ui/queue            -> full page (layout + section grid)
//   GET /ui/queue/partial    -> section grid only (no chrome) — htmx polls
//                               this every 5s from inside the full page.
//
// Both accept ?project=<name>. When unset we pick the alphabetically-first
// project the caller has any role on. Callers with zero project memberships
// see an in-page "no projects accessible" hint instead of a 403 — they're
// already authed, so a hard 403 would be misleading.
//
// The partial endpoint deliberately renders just the section grid (no
// <!DOCTYPE> chrome) so htmx innerHTML-swaps cleanly.

import (
	"context"
	"html/template"
	"sort"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/labstack/echo/v4"

	"github.com/GMISWE/ieops-aihub/internal/domain"
)

// queueTmpl is the cached page+partial template set, built once at register
// time. The third parameter to registerUIQueueHandlers (the shared root
// template parsed in ui_embed.go) is intentionally ignored — we use
// pageTemplate so each page owns its own {{define "content"}} block without
// colliding with the wi/memory pages.
var queueTmpl *template.Template

// getQueueFn is the function used to fetch the ready queue. Production wires
// domain.GetReadyQueue; tests override this to inject a synthetic ReadyQueue
// without touching the database.
var getQueueFn = func(ctx context.Context, pool *pgxpool.Pool, project string, max int) (*domain.ReadyQueue, *domain.AihubError) {
	return domain.GetReadyQueue(ctx, pool, project, max)
}

// queuePageData is the template payload for both the full page and the
// partial. .Q is always non-nil so the partial doesn't have to nil-guard.
type queuePageData struct {
	Title             string
	Active            string
	User              *UserContext
	Project           string
	ProjectsAvailable []string
	Q                 *domain.ReadyQueue
	Now               time.Time
	Err               string
	NoAccess          bool // user has zero project memberships
	AccessDenied      bool // user explicitly lacks viewer role on .Project
}

// registerUIQueueHandlers wires the queue full-page and partial endpoints
// onto the authenticated /ui group. The pool is captured by the closures;
// the shared root template is unused (see queueTmpl).
func registerUIQueueHandlers(g *echo.Group, pool *pgxpool.Pool, _ *template.Template) {
	if queueTmpl == nil {
		queueTmpl = pageTemplate("queue.html.tmpl", "queue_section.html.tmpl")
	}
	g.GET("/queue", handleUIQueue(pool, queueTmpl))
	g.GET("/queue/partial", handleUIQueuePartial(pool, queueTmpl))
}

// emptyQueue returns a non-nil ReadyQueue with empty slices. Used for the
// no-access / access-denied templates so the partial template doesn't have
// to guard every range.
func emptyQueue() *domain.ReadyQueue {
	return &domain.ReadyQueue{
		Items:             []domain.ReadyItem{},
		Running:           []domain.RunningItem{},
		Stalled:           []domain.StalledItem{},
		Paused:            []domain.PausedItem{},
		NeedsHumanSession: []domain.ReadyItem{},
		Unclassified:      []domain.ReadyItem{},
	}
}

// sortedProjectNames returns the user's project memberships in deterministic
// alphabetical order. Empty when the user has no project roles.
func sortedProjectNames(u *UserContext) []string {
	if u == nil || len(u.ProjectRoles) == 0 {
		return nil
	}
	out := make([]string, 0, len(u.ProjectRoles))
	for p := range u.ProjectRoles {
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}

// resolveProject picks the project to render. Order of preference:
//
//  1. ?project=<name> from the query string
//  2. alphabetically-first project from the user's ProjectRoles map
//
// Returns "" when both are empty (the no-projects hint will fire).
func resolveProject(c echo.Context, u *UserContext) (string, []string) {
	available := sortedProjectNames(u)
	q := c.QueryParam("project")
	if q != "" {
		return q, available
	}
	if len(available) > 0 {
		return available[0], available
	}
	return "", available
}

// handleUIQueue renders the full /ui/queue page. The data struct it builds
// is the same one the partial handler uses — the only difference is that
// here we render through the layout template.
func handleUIQueue(pool *pgxpool.Pool, tmpl *template.Template) echo.HandlerFunc {
	return func(c echo.Context) error {
		u := GetUser(c)
		project, available := resolveProject(c, u)

		data := queuePageData{
			Title:             "Queue",
			Active:            "queue",
			User:              u,
			Project:           project,
			ProjectsAvailable: available,
			Q:                 emptyQueue(),
			Now:               time.Now(),
		}

		// Zero project memberships and not admin: explicit hint, no DB call.
		if project == "" && (u == nil || u.Role != "admin") {
			data.NoAccess = true
			return renderTemplate(c, tmpl, "layout", data)
		}

		// Project access check — viewer or better. checkProjectAccess writes
		// a JSON error to the response on denial, which we don't want for
		// /ui/queue (it's HTML). Inline the check here so we can render an
		// in-page hint instead.
		if u != nil && u.Role != "admin" {
			if _, ok := u.ProjectRoles[project]; !ok {
				data.AccessDenied = true
				return renderTemplate(c, tmpl, "layout", data)
			}
		}

		ctx, cancel := contextWithTimeout(c)
		defer cancel()
		q, aerr := getQueueFn(ctx, pool, project, 100)
		if aerr != nil {
			data.Err = "failed to load ready queue: " + aerr.Message
		} else if q != nil {
			data.Q = q
		}
		return renderTemplate(c, tmpl, "layout", data)
	}
}

// handleUIQueuePartial renders just the section grid for the htmx
// every-5s poll. No layout / no <!DOCTYPE> chrome.
func handleUIQueuePartial(pool *pgxpool.Pool, tmpl *template.Template) echo.HandlerFunc {
	return func(c echo.Context) error {
		u := GetUser(c)
		project, available := resolveProject(c, u)

		data := queuePageData{
			Title:             "Queue",
			Active:            "queue",
			User:              u,
			Project:           project,
			ProjectsAvailable: available,
			Q:                 emptyQueue(),
			Now:               time.Now(),
		}

		if project == "" && (u == nil || u.Role != "admin") {
			data.NoAccess = true
			data.Err = "no projects accessible"
			return renderTemplate(c, tmpl, "queue_section.html.tmpl", data)
		}

		if u != nil && u.Role != "admin" {
			if _, ok := u.ProjectRoles[project]; !ok {
				data.AccessDenied = true
				data.Err = "no access to project " + project
				return renderTemplate(c, tmpl, "queue_section.html.tmpl", data)
			}
		}

		ctx, cancel := contextWithTimeout(c)
		defer cancel()
		q, aerr := getQueueFn(ctx, pool, project, 100)
		if aerr != nil {
			data.Err = "failed to load ready queue: " + aerr.Message
		} else if q != nil {
			data.Q = q
		}
		return renderTemplate(c, tmpl, "queue_section.html.tmpl", data)
	}
}
