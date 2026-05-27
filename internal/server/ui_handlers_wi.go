package server

// Web UI work-item pages: list + detail + events partial.
//
// Routes (mounted under /ui, behind RequireUISession):
//   GET /ui/wi                       -> list (full page)
//   GET /ui/wi/:id                   -> detail (full page)
//   GET /ui/wi/:id/events/partial    -> events timeline (partial, no layout)
//
// Detail page fetches in parallel: wi, dependencies, events, methodology
// artifacts. Artifacts link to /v1/artifacts/:mem_id/html — the visibility
// check is enforced by that endpoint, not here.

import (
	"context"
	"html/template"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/labstack/echo/v4"

	"github.com/GMISWE/ieops-aihub/internal/domain"
)

// listWorkItemsFn is the package-level seam for tests to swap in fakes
// without spinning up postgres. Defaults to domain.ListWorkItems.
var listWorkItemsFn = domain.ListWorkItems

// getWorkItemFn is the package-level seam for tests.
var getWorkItemFn = domain.GetWorkItem

// listDependenciesFn is the package-level seam for tests.
var listDependenciesFn = domain.ListDependencies

// listEventsFn is the package-level seam for tests.
var listEventsFn = domain.ListEvents

// recallFn is the package-level seam for tests. Returns methodology.*
// artifacts associated with a work item.
var recallFn = domain.Recall

// fetchWIFacetsFn is the package-level seam for tests so the list handler can
// run without a live pool. Defaults to the real distinct-facet query.
var fetchWIFacetsFn = fetchWIFacets

// validWIStatuses enumerates the values accepted in the ?status= filter.
// The empty string maps to "active" = queued + running + paused + blocked.
var validWIStatuses = map[string]bool{
	"queued":    true,
	"running":   true,
	"paused":    true,
	"blocked":   true,
	"cancelled": true,
	"wrapped":   true,
}

// validWIKinds enumerates the values accepted in the ?kind= filter.
var validWIKinds = map[string]bool{
	"feature":      true,
	"fix_bug":      true,
	"chore":        true,
	"refactor":     true,
	"critical_bug": true,
	"release":      true,
}

// activeStatuses is the default status set when no ?status= filter is set.
var activeStatuses = []string{"queued", "running", "paused", "blocked"}

// allProjectsSentinel is the ?project= value that selects the cross-project
// "view all" mode on the wi list.
const allProjectsSentinel = "__all__"

// wiListPageData is the data passed to wi_list.html.tmpl.
type wiListPageData struct {
	Title             string
	Active            string
	User              *UserContext
	Project           string
	ProjectsAvailable []string
	AllMode           bool // true when viewing across all accessible projects
	Status            string
	Kind              string
	Reporter          string
	Owner             string
	ReporterOptions   []string // distinct reporter display names for the filter dropdown
	OwnerOptions      []string // distinct owner display names for the filter dropdown
	Limit             int
	Items             []*wiListRow
	Err               string
}

// wiListRow is a view-model row for the list table. Decoupling from
// domain.WorkItem keeps template field access simple (no pointers / nil checks
// scattered through the template).
type wiListRow struct {
	ID              string
	Slug            string
	Project         string
	WIType          string
	Priority        string
	Status          string
	Goal            string
	OwnerDisplay    string // run_attempts.actor_display of current attempt; "" if no attempt
	ReporterDisplay string // who filed the wi (always populated)
}

// wiDetailPageData is the data passed to wi_detail.html.tmpl.
type wiDetailPageData struct {
	Title          string
	Active         string
	User           *UserContext
	WI             *domain.WorkItem
	WIType         string    // flattened *WI.WIType
	Content        string    // flattened *WI.Content for direct template access
	Milestone      string    // flattened *WI.Milestone
	AttemptID      string    // flattened *WI.CurrentAttemptID
	OwnerDisplay   string    // run_attempts.actor_display of current attempt, "" if none
	OwnerActive    time.Time // run_attempts.last_active_at of current attempt
	OwnerHasActive bool      // true when OwnerActive is meaningful (non-zero)
	Dependencies   *depView
	Events         []eventView
	Artifacts      []artifactLink
	Err            string
	AccessDenied   bool
}

// depView is the template-friendly projection of DependenciesResponse with
// pointer fields flattened to plain strings (templates can't readily walk
// *string).
type depView struct {
	Blocking  []depEntry
	BlockedBy []depEntry
}

// depEntry is the per-row dependency projection. `Slug` is empty when the
// caller can't see the cross-project wi.
type depEntry struct {
	Slug   string
	Kind   string
	Hidden bool
}

// wiEventsPartialData is the data passed to events_timeline.html.tmpl when
// served as a partial (no layout chrome).
type wiEventsPartialData struct {
	Events []eventView
}

// eventView is the template-friendly projection of EventRow with pointer +
// json.RawMessage fields flattened to plain strings.
type eventView struct {
	CreatedAt    time.Time
	EventType    string
	ActorDisplay string
	Payload      string
	Pinned       bool
}

// toEventViews flattens []EventRow into []eventView.
func toEventViews(rows []domain.EventRow) []eventView {
	out := make([]eventView, 0, len(rows))
	for _, r := range rows {
		ev := eventView{
			CreatedAt: r.CreatedAt,
			EventType: r.EventType,
			Pinned:    r.Pinned,
		}
		if r.ActorDisplay != nil {
			ev.ActorDisplay = *r.ActorDisplay
		}
		if len(r.Payload) > 0 {
			ev.Payload = string(r.Payload)
		}
		out = append(out, ev)
	}
	return out
}

// artifactLink is the per-row data for the artifacts section on the detail page.
type artifactLink struct {
	MemID   string
	Type    string
	Content string
}

// registerUIWIHandlers wires the /ui/wi tree onto the given group. The third
// argument (the shared root template) is ignored — each page builds its own
// self-contained *template.Template via pageTemplate so {{define "content"}}
// blocks don't collide across files.
func registerUIWIHandlers(g *echo.Group, pool *pgxpool.Pool, _ *template.Template) {
	listTmpl := pageTemplate("wi_list.html.tmpl")
	detailTmpl := pageTemplate("wi_detail.html.tmpl", "events_timeline.html.tmpl")

	g.GET("/wi", handleUIWIList(pool, listTmpl))
	g.GET("/wi/:id", handleUIWIDetail(pool, detailTmpl))
	g.GET("/wi/:id/events/partial", handleUIWIEventsPartial(pool, detailTmpl))
}

// handleUIWIList renders the work-item list page.
//
// Project selection mirrors the queue page: query ?project= wins; otherwise
// the first project (alphabetical) the caller can see. For non-admins this
// comes from their ProjectRoles map; for admins (empty map by design — see
// middleware.go ~L104-106) availableProjectsForUI falls back to all visible
// projects via domain.ListProjects.
func handleUIWIList(pool *pgxpool.Pool, tmpl *template.Template) echo.HandlerFunc {
	return func(c echo.Context) error {
		u := GetUser(c)
		ctx, cancel := contextWithTimeout(c)
		defer cancel()

		data := &wiListPageData{
			Title:  "Work items",
			Active: "wi",
			User:   u,
		}

		projects := availableProjectsForUI(ctx, pool, u)
		data.ProjectsAvailable = projects

		// project resolution:
		//   "__all__"  → view across every project the caller can see
		//   ""         → default to first available
		//   "<name>"   → single project
		projectParam := strings.TrimSpace(c.QueryParam("project"))
		allMode := projectParam == allProjectsSentinel
		project := projectParam
		if allMode {
			project = allProjectsSentinel // keep sentinel for the template's selected-option check
		} else if project == "" && len(projects) > 0 {
			project = projects[0]
		}
		data.Project = project
		data.AllMode = allMode

		// Filter params.
		statusParam := strings.TrimSpace(c.QueryParam("status"))
		kindParam := strings.TrimSpace(c.QueryParam("kind"))
		reporterParam := strings.TrimSpace(c.QueryParam("reporter"))
		ownerParam := strings.TrimSpace(c.QueryParam("owner"))
		if statusParam != "" && !validWIStatuses[statusParam] {
			statusParam = ""
		}
		if kindParam != "" && !validWIKinds[kindParam] {
			kindParam = ""
		}
		data.Status = statusParam
		data.Kind = kindParam
		data.Reporter = reporterParam
		data.Owner = ownerParam

		limit := 50
		if raw := strings.TrimSpace(c.QueryParam("limit")); raw != "" {
			if n, err := strconv.Atoi(raw); err == nil && n > 0 {
				if n > 200 {
					n = 200
				}
				limit = n
			}
		}
		data.Limit = limit

		// No project selected (and not view-all) = no listing yet — the
		// dropdown still renders so the user can pick one.
		if project == "" {
			if u.Role != "admin" && len(projects) == 0 {
				data.Err = "no projects accessible — ask an admin to add you to a project."
			} else {
				data.Err = "select a project to view work items."
			}
			return renderTemplate(c, tmpl, "layout", data)
		}

		filter := domain.ListWorkItemsFilter{Limit: limit}
		if statusParam != "" {
			filter.Status = []string{statusParam}
		} else {
			filter.Status = activeStatuses
		}
		if kindParam != "" {
			filter.WIType = &kindParam
		}
		if reporterParam != "" {
			filter.ReporterDisplay = &reporterParam
		}
		if ownerParam != "" {
			filter.OwnerDisplay = &ownerParam
		}

		// queryProject is what we hand to the domain layer: "" in view-all
		// mode (so it scopes by AccessibleProjects), else the single project.
		// facetScope is the project set used to populate the filter dropdowns.
		queryProject := project
		var facetScope []string
		if allMode {
			queryProject = ""
			if u.Role != "admin" {
				// non-admin view-all is bounded to their member projects
				filter.AccessibleProjects = projects
				facetScope = projects
			}
			// admin view-all: AccessibleProjects empty + facetScope nil = every project
		} else {
			// single project — access check (admin bypasses)
			if err := checkProjectAccessSoft(u, project); err != nil {
				data.Err = err.Error()
				return renderTemplate(c, tmpl, "layout", data)
			}
			facetScope = []string{project}
		}

		// Populate filter dropdown options for the current scope.
		facets := fetchWIFacetsFn(ctx, pool, facetScope)
		data.ReporterOptions = facets.Reporters
		data.OwnerOptions = facets.Owners

		res, aerr := listWorkItemsFn(ctx, pool, queryProject, filter)
		if aerr != nil {
			data.Err = aerr.Message
			return renderTemplate(c, tmpl, "layout", data)
		}

		// Batch-fetch current-attempt owners so the list can show "who claimed
		// it" without issuing N+1 queries.
		attemptIDs := make([]string, 0, len(res.Items))
		for _, wi := range res.Items {
			if wi.CurrentAttemptID != nil {
				attemptIDs = append(attemptIDs, *wi.CurrentAttemptID)
			}
		}
		owners := fetchAttemptOwners(ctx, pool, attemptIDs)

		rows := make([]*wiListRow, 0, len(res.Items))
		for _, wi := range res.Items {
			rows = append(rows, toListRow(wi, owners))
		}
		data.Items = rows

		return renderTemplate(c, tmpl, "layout", data)
	}
}

// handleUIWIDetail renders the work-item detail page.
//
// Fetches in parallel: dependencies, events, methodology artifacts. The wi
// itself must come first since downstream queries need wi.ID + wi.Project. On
// 404 we return a body page rather than an HTTP 404 — the layout chrome stays
// intact so the user has the nav to keep moving.
func handleUIWIDetail(pool *pgxpool.Pool, tmpl *template.Template) echo.HandlerFunc {
	return func(c echo.Context) error {
		u := GetUser(c)

		idOrSlug := strings.TrimSpace(c.Param("id"))
		if idOrSlug == "" {
			return c.Redirect(http.StatusFound, "/ui/wi")
		}

		data := &wiDetailPageData{
			Title:  "Work item",
			Active: "wi",
			User:   u,
		}

		ctx, cancel := contextWithTimeout(c)
		defer cancel()

		wi, aerr := getWorkItemFn(ctx, pool, idOrSlug)
		if aerr != nil {
			data.Err = aerr.Message
			// Use 404 on missing wi so curl callers can detect it; the layout
			// chrome still renders so the user has the top nav. renderTemplate
			// hard-codes 200 via c.HTMLBlob, so we write the body manually.
			return renderHTMLStatus(c, tmpl, "layout", data, http.StatusNotFound)
		}
		data.Title = "wi " + wi.Slug
		data.WI = wi
		if wi.WIType != nil {
			data.WIType = *wi.WIType
		}
		if wi.Content != nil {
			data.Content = *wi.Content
		}
		if wi.Milestone != nil {
			data.Milestone = *wi.Milestone
		}
		if wi.CurrentAttemptID != nil {
			data.AttemptID = *wi.CurrentAttemptID
		}

		// Project access check — must come AFTER GetWorkItem because we don't
		// know the project until we've read the row.
		if err := checkProjectAccessSoft(u, wi.Project); err != nil {
			data.Err = err.Error()
			data.AccessDenied = true
			return renderTemplate(c, tmpl, "layout", data)
		}

		// Parallel fan-out for the four side-load queries.
		var (
			deps      *domain.DependenciesResponse
			depsErr   *domain.AihubError
			events    []eventView
			eventsErr error
			arts      []artifactLink
			ownerInfo attemptOwner
			wg        sync.WaitGroup
		)

		wg.Add(4)

		go func() {
			defer wg.Done()
			// ListDependencies hides cross-project entries by checking
			// `callerProjectRoles[entry.Project] != ""`. Admins have an
			// empty ProjectRoles map by design (middleware.go ~L104-106),
			// so synthesize a viewer role on every visible project so the
			// admin sees the real slug instead of "[hidden — cross-project]".
			roles := u.ProjectRoles
			if u.Role == "admin" {
				roles = map[string]string{}
				for _, p := range availableProjectsForUI(ctx, pool, u) {
					roles[p] = "viewer"
				}
			}
			deps, depsErr = listDependenciesFn(ctx, pool, wi.ID, roles)
		}()

		go func() {
			defer wg.Done()
			limit := 20
			pinnedFirst := true
			f := &domain.ListEventsFilter{
				WorkItemID:  &wi.ID,
				Limit:       limit,
				PinnedFirst: pinnedFirst,
			}
			resp, err := listEventsFn(ctx, pool, f)
			if err != nil {
				eventsErr = err
				return
			}
			events = toEventViews(resp.Events)
		}()

		go func() {
			defer wg.Done()
			arts = fetchArtifactLinks(ctx, pool, u, wi)
		}()

		go func() {
			defer wg.Done()
			if wi.CurrentAttemptID != nil {
				ownerInfo = fetchAttemptOwner(ctx, pool, *wi.CurrentAttemptID)
			}
		}()

		wg.Wait()

		if depsErr != nil {
			data.Err = depsErr.Message
		} else {
			data.Dependencies = toDepView(deps)
		}
		if eventsErr != nil && data.Err == "" {
			data.Err = "failed to load events: " + eventsErr.Error()
		}
		data.Events = events
		data.Artifacts = arts
		data.OwnerDisplay = ownerInfo.Display
		data.OwnerActive = ownerInfo.LastActiveAt
		data.OwnerHasActive = !ownerInfo.LastActiveAt.IsZero()

		return renderTemplate(c, tmpl, "layout", data)
	}
}

// handleUIWIEventsPartial returns just the events timeline fragment (no layout
// chrome) for the HTMX poll on the detail page.
//
// Accepts ?since=<RFC3339> for incremental refreshes. ListEvents already does
// the time-cursor comparison, so we forward the raw query param as-is.
func handleUIWIEventsPartial(pool *pgxpool.Pool, tmpl *template.Template) echo.HandlerFunc {
	return func(c echo.Context) error {
		u := GetUser(c)
		idOrSlug := strings.TrimSpace(c.Param("id"))
		if idOrSlug == "" {
			return c.NoContent(http.StatusBadRequest)
		}

		ctx, cancel := contextWithTimeout(c)
		defer cancel()

		wi, aerr := getWorkItemFn(ctx, pool, idOrSlug)
		if aerr != nil {
			return c.NoContent(http.StatusNotFound)
		}
		if err := checkProjectAccessSoft(u, wi.Project); err != nil {
			return c.NoContent(http.StatusForbidden)
		}

		f := &domain.ListEventsFilter{
			WorkItemID:  &wi.ID,
			Limit:       20,
			PinnedFirst: true,
		}
		if since := strings.TrimSpace(c.QueryParam("since")); since != "" {
			f.Since = &since
		}
		resp, err := listEventsFn(ctx, pool, f)
		if err != nil {
			return c.NoContent(http.StatusInternalServerError)
		}

		return renderTemplate(c, tmpl, "events_timeline.html.tmpl",
			&wiEventsPartialData{Events: toEventViews(resp.Events)})
	}
}

// fetchArtifactLinks pulls the methodology.* memories for a wi via the recall
// path. Errors are silently swallowed — the section is best-effort; a broken
// recall query should not break the detail page.
func fetchArtifactLinks(ctx context.Context, pool *pgxpool.Pool, u *UserContext, wi *domain.WorkItem) []artifactLink {
	wiID := wi.ID
	req := &domain.RecallRequest{
		Project:      wi.Project,
		Types:        []string{"methodology.spec", "methodology.plan", "methodology.review", "methodology.execute", "methodology.retro", "methodology.wrap_summary"},
		WorkItemID:   &wiID,
		TopK:         20,
		MinStrength:  0.0,
		CallerUserID: u.UserID,
		CallerRole:   u.Role,
	}
	resp, err := recallFn(ctx, pool, req)
	if err != nil || resp == nil {
		return nil
	}
	out := make([]artifactLink, 0, len(resp.Items))
	for _, m := range resp.Items {
		// Skip private memories the caller can't read — recall already filters
		// these out, but defense in depth.
		if m.Visibility == "private" && m.AuthorUserID != u.UserID && u.Role != "admin" {
			continue
		}
		out = append(out, artifactLink{
			MemID:   m.ID,
			Type:    m.Type,
			Content: m.Content,
		})
	}
	return out
}

// toDepView flattens DependenciesResponse into the template-friendly depView.
// The Slug pointer is dereffed to a plain string, and the cross-project
// "hidden" sentinel that ListDependencies sets (ID="hidden", Slug=nil) is
// surfaced as a boolean for the template.
func toDepView(d *domain.DependenciesResponse) *depView {
	if d == nil {
		return nil
	}
	// NOTE: domain.DependenciesResponse uses inverted field semantics —
	// `Blocking` is populated from rows where this wi is the *blocked* side
	// (so it actually lists the wi's that block us), and `BlockedBy` lists
	// the wi's we are blocking. Swap them here so the template labels mean
	// what a human reader expects:
	//   depView.BlockedBy = "who blocks us" (domain.Blocking)
	//   depView.Blocking  = "who we block"  (domain.BlockedBy)
	v := &depView{
		Blocking:  make([]depEntry, 0, len(d.BlockedBy)),
		BlockedBy: make([]depEntry, 0, len(d.Blocking)),
	}
	for _, e := range d.BlockedBy {
		v.Blocking = append(v.Blocking, depEntryFrom(e))
	}
	for _, e := range d.Blocking {
		v.BlockedBy = append(v.BlockedBy, depEntryFrom(e))
	}
	return v
}

func depEntryFrom(e domain.DependencyListEntry) depEntry {
	if e.Slug == nil || e.ID == "hidden" {
		return depEntry{Kind: e.Kind, Hidden: true}
	}
	return depEntry{Slug: *e.Slug, Kind: e.Kind}
}

// toListRow is the WorkItem → wiListRow projection used by the list page.
// Owner display is derived from CurrentAttemptID heuristically — the detail
// query has the running attempt's actor available but the list does not, so
// we surface the reporter as a fallback signal of "who is associated with this
// wi" without spending a per-row query.
func toListRow(wi *domain.WorkItem, owners map[string]attemptOwner) *wiListRow {
	row := &wiListRow{
		ID:              wi.ID,
		Slug:            wi.Slug,
		Project:         wi.Project,
		Priority:        wi.Priority,
		Status:          wi.Status,
		Goal:            wi.Goal,
		ReporterDisplay: wi.ReporterDisplay,
	}
	if wi.WIType != nil {
		row.WIType = *wi.WIType
	}
	if wi.CurrentAttemptID != nil {
		if o, ok := owners[*wi.CurrentAttemptID]; ok {
			row.OwnerDisplay = o.Display
		}
	}
	return row
}

// renderHTMLStatus is a 404-aware variant of renderTemplate. The shared
// renderTemplate hard-codes status 200 via c.HTMLBlob, but the detail page
// wants 404 when the wi is missing — so we drive the response manually.
func renderHTMLStatus(c echo.Context, tmpl *template.Template, name string, data any, status int) error {
	var buf strings.Builder
	if err := tmpl.ExecuteTemplate(&buf, name, data); err != nil {
		return c.String(http.StatusInternalServerError, "template error: "+err.Error())
	}
	return c.HTMLBlob(status, []byte(buf.String()))
}

// checkProjectAccessSoft is a non-writing variant of checkProjectAccess. The
// real helper writes a JSON error to the response on denial, which would
// break the HTML render path. This variant just returns an error string and
// lets the caller decide how to render.
func checkProjectAccessSoft(u *UserContext, project string) error {
	if u == nil {
		return errSoft("not authenticated")
	}
	if u.Role == "admin" {
		return nil
	}
	if project == "" {
		return errSoft("project is required")
	}
	role, ok := u.ProjectRoles[project]
	if !ok || role == "" {
		return errSoft("no access to project " + project)
	}
	return nil
}

// errSoft is a tiny error type so we can keep the package import surface tight.
type errSoft string

func (e errSoft) Error() string { return string(e) }
