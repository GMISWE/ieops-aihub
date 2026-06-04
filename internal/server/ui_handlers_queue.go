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
	"strings"
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

// queuePageData is the template payload for the count-strip partial. .Strip
// carries the four headline counts. Running / Needs you / Unclaimed are derived
// from the SAME grouping the wi list uses (single source of truth — annotation
// #2); Stalled comes from the ready-queue path.
type queuePageData struct {
	Title             string
	Active            string
	Theme             string
	User              *UserContext
	Project           string
	ProjectsAvailable []string
	Strip             stripCounts
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

// handleUIQueuePartial renders just the count strip for the htmx every-5s poll.
// No layout / no <!DOCTYPE> chrome.
//
// The strip's Running / Needs you / Unclaimed counts are derived from the SAME
// grouping the wi list renders (groupCountsFromGroups) so the poll can never
// drift away from the visible list. It supports ?project=__all__ by aggregating
// across the caller's accessible projects, mirroring the list page.
func handleUIQueuePartial(pool *pgxpool.Pool, tmpl *template.Template) echo.HandlerFunc {
	return func(c echo.Context) error {
		u := GetUser(c)
		ctx, cancel := contextWithTimeout(c)
		defer cancel()

		available := availableProjectsForUI(ctx, pool, u)
		projectParam := strings.TrimSpace(c.QueryParam("project"))
		allMode := projectParam == allProjectsSentinel
		project := projectParam
		if !allMode && project == "" && len(available) > 0 {
			project = available[0]
		}

		data := queuePageData{
			Title:             "Queue",
			Active:            "queue",
			Theme:             themeFromCookie(c),
			User:              u,
			Project:           project,
			ProjectsAvailable: available,
			Now:               time.Now(),
		}

		if !allMode && project == "" && (u == nil || u.Role != "admin") {
			data.NoAccess = true
			data.Err = "no projects accessible"
			return renderTemplate(c, tmpl, "queue_section.html.tmpl", data)
		}

		if !allMode && u != nil && u.Role != "admin" {
			if _, ok := u.ProjectRoles[project]; !ok {
				data.AccessDenied = true
				data.Err = "no access to project " + project
				return renderTemplate(c, tmpl, "queue_section.html.tmpl", data)
			}
		}

		// Mirror the list page's personal-dashboard defaults: Mine view scopes
		// the personal sections to the viewer but still surfaces the Unclaimed
		// pool, so we do NOT push an owner filter into the DB (groupListRows does
		// the owner scoping in memory). The "all=1" opt-out switches to All view.
		viewer := ""
		if u != nil {
			viewer = u.DisplayName
		}
		mine := viewer != "" && c.QueryParam("all") != "1"

		filter := domain.ListWorkItemsFilter{Limit: 200, Status: activeStatuses}
		queryProject := project
		if allMode {
			queryProject = ""
			if u != nil && u.Role != "admin" {
				filter.AccessibleProjects = available
			}
		}

		// The strip always tallies the active-status set (Running / Needs you /
		// Unclaimed); it never pre-seeds per-status buckets, so no selStatuses.
		_, groups, aerr := fetchListGroups(ctx, pool, queryProject, filter, viewer, mine, nil)
		if aerr != nil {
			data.Err = "failed to load ready queue: " + aerr.Message
			return renderTemplate(c, tmpl, "queue_section.html.tmpl", data)
		}
		data.Strip = groupCountsFromGroups(groups)
		data.Strip.Stalled = stalledCount(ctx, pool, project, allMode, available, u)

		return renderTemplate(c, tmpl, "queue_section.html.tmpl", data)
	}
}
