package server

// Web UI: /ui/queue — six-segment LCRS ready-queue view for a project.
//
// The ready queue no longer has its own full page. It is embedded as a
// collapsible block at the top of the /ui/wi list page, which polls the
// partial endpoint below. Two endpoints remain:
//
//   GET /ui/queue            -> 302 redirect to /ui/wi (preserving ?project=)
//   GET /ui/queue/partial    -> section grid only (no chrome) — htmx polls
//                               this every 5s from inside the wi list page.
//
// The partial accepts ?project=<name>. When unset we pick the
// alphabetically-first project the caller has any role on. Callers with zero
// project memberships see an in-page "no projects accessible" hint.
//
// The partial endpoint deliberately renders just the section grid (no
// <!DOCTYPE> chrome) so htmx innerHTML-swaps cleanly.

import (
	"context"
	"html/template"
	"net/http"
	"net/url"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/labstack/echo/v4"

	"github.com/GMISWE/ieops-aihub/internal/domain"
)

// queueTmpl is the cached partial template set, built once at register time.
// Only the partial endpoint renders a template now (the full page is a
// redirect), so this holds just queue_section.html.tmpl. The third parameter
// to registerUIQueueHandlers (the shared root template parsed in ui_embed.go)
// is intentionally ignored — we use pageTemplate so the partial owns its own
// {{define}} block without colliding with the wi/memory pages.
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
		queueTmpl = partialTemplate("queue_section.html.tmpl")
	}
	g.GET("/queue", handleUIQueue())
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

// resolveProject picks the project to render. Order of preference:
//
//  1. ?project=<name> from the query string
//  2. alphabetically-first project the caller can see (via
//     availableProjectsForUI — admin sees all visible projects, others see
//     their ProjectRoles keys)
//
// Returns "" when both are empty (the no-projects hint will fire).
func resolveProject(ctx context.Context, pool *pgxpool.Pool, c echo.Context, u *UserContext) (string, []string) {
	available := availableProjectsForUI(ctx, pool, u)
	q := c.QueryParam("project")
	if q != "" {
		return q, available
	}
	if len(available) > 0 {
		return available[0], available
	}
	return "", available
}

// handleUIQueue redirects the old standalone queue page to the wi list page,
// where the ready queue now lives as an embedded collapsible block. The
// ?project= query param is preserved so a bookmarked /ui/queue?project=x lands
// on the right wi list.
func handleUIQueue() echo.HandlerFunc {
	return func(c echo.Context) error {
		dest := "/ui/wi"
		if p := c.QueryParam("project"); p != "" {
			dest += "?project=" + url.QueryEscape(p)
		}
		return c.Redirect(http.StatusFound, dest)
	}
}

// handleUIQueuePartial renders just the section grid for the htmx
// every-5s poll. No layout / no <!DOCTYPE> chrome.
func handleUIQueuePartial(pool *pgxpool.Pool, tmpl *template.Template) echo.HandlerFunc {
	return func(c echo.Context) error {
		u := GetUser(c)
		ctx, cancel := contextWithTimeout(c)
		defer cancel()
		project, available := resolveProject(ctx, pool, c, u)

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

		q, aerr := getQueueFn(ctx, pool, project, 100)
		if aerr != nil {
			data.Err = "failed to load ready queue: " + aerr.Message
		} else if q != nil {
			data.Q = q
		}
		return renderTemplate(c, tmpl, "queue_section.html.tmpl", data)
	}
}
