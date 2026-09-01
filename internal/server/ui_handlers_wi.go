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
	"sort"
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

// getParentRefFn / listChildrenFn are the package-level seams for the
// parent/children navigation (aihub#142). Swappable in tests so the detail
// handler runs without a live pool, mirroring listDependenciesFn.
var getParentRefFn = domain.GetParentRef
var listChildrenFn = domain.ListChildren

// listEventsFn is the package-level seam for tests.
var listEventsFn = domain.ListEvents

// recallFn is the package-level seam for tests. Returns methodology.*
// artifacts associated with a work item.
var recallFn = domain.Recall

// wiVersionChainFn is the seam for MemoryVersionChain used by fetchArtifactLinks.
// Swappable in tests so the handler runs without a live pool (same pattern as recallFn).
var wiVersionChainFn = domain.MemoryVersionChain

// fetchWIFacetsFn is the package-level seam for tests so the list handler can
// run without a live pool. Defaults to the real distinct-facet query.
var fetchWIFacetsFn = fetchWIFacets

// fetchProjectWICountsFn is the seam for the per-project count query used by the
// project switcher. Swappable in tests (nil pool returns empty map).
var fetchProjectWICountsFn = fetchProjectWICounts

// fetchDoneCountFn is the package-level seam for the terminal (Done) count query
// (aihub#185). Swappable in tests (nil pool returns 0).
var fetchDoneCountFn = fetchDoneCount

// validWIStatuses enumerates the values accepted in the ?status= filter.
// The empty string maps to "active" = queued + running + paused + blocked.
// "failed" is included (aihub#185) so the Done segment can surface failed wi's,
// which were previously absent from the enum and therefore invisible in any view.
var validWIStatuses = map[string]bool{
	"queued":    true,
	"running":   true,
	"paused":    true,
	"blocked":   true,
	"cancelled": true,
	"wrapped":   true,
	"failed":    true,
}

// doneStatuses are the terminal statuses folded into the "Done" sidebar segment
// (aihub#185). failed is included here so previously-invisible failed wi's get a
// home; the row still renders its own real status badge.
var doneStatuses = []string{"wrapped", "cancelled", "failed"}

// segmentOrder is the canonical top->bottom order of the LCRS sidebar segments
// (aihub#185), replacing the old raw-status statusBlockOrder for the list view.
// "done" is rendered below a divider in the sidebar.
var segmentOrder = []string{"running", "needsyou", "unclaimed", "stalled", "paused", "done"}

// segmentLabels maps each segment key to its sidebar display label.
var segmentLabels = map[string]string{
	"running":   "Running",
	"needsyou":  "Needs you",
	"unclaimed": "Unclaimed",
	"stalled":   "Stalled",
	"paused":    "Paused",
	"done":      "Done",
}

// segmentFor returns the LCRS segment (aihub#185) a row belongs to, by precedence:
//
//	done      — terminal: status in {wrapped, cancelled, failed}
//	stalled   — running but flagged stalled by the ready-queue path
//	running   — running and alive
//	unclaimed — no current-attempt owner AND status in {queued, blocked}
//	needsyou  — owner == viewer AND status in {paused, blocked} (your work, waiting on you)
//	paused    — paused (owned by someone else; yours went to needsyou above)
//	(fallback) unclaimed — the only residual is "blocked owned by another", rare;
//	            fold into the claimable pool so it is never silently dropped.
//
// viewer is the current user's display name; stalled is the set of stalled wi IDs.
func segmentFor(r *wiListRow, viewer string, stalled map[string]bool) string {
	switch {
	case r.Status == "wrapped" || r.Status == "cancelled" || r.Status == "failed":
		return "done"
	case r.Status == "running" && stalled[r.ID]:
		return "stalled"
	case r.Status == "running":
		return "running"
	case r.OwnerDisplay == "" && (r.Status == "queued" || r.Status == "blocked"):
		return "unclaimed"
	case viewer != "" && r.OwnerDisplay == viewer && (r.Status == "paused" || r.Status == "blocked"):
		return "needsyou"
	case r.Status == "paused":
		return "paused"
	default:
		return "unclaimed"
	}
}

// segmentListRows buckets active-status rows into LCRS segments (aihub#185),
// returning per-segment counts and the rows in each. In Mine view, rows owned by
// another user are dropped from every segment EXCEPT unclaimed (the claimable pool
// is always shown) — mirroring groupListRows' owner scoping. stalled is the set of
// wi IDs flagged stalled by the ready-queue path. Terminal rows are not expected
// here (the active query excludes them); Done is counted/loaded separately.
func segmentListRows(rows []*wiListRow, viewer string, mine bool, stalled map[string]bool) (map[string]int, map[string][]*wiListRow) {
	counts := map[string]int{}
	bySeg := map[string][]*wiListRow{}
	for _, r := range rows {
		seg := segmentFor(r, viewer, stalled)
		if mine && seg != "unclaimed" && r.OwnerDisplay != viewer {
			continue
		}
		if seg == "needsyou" {
			r.NeedsYou = true // drives the .row.hot left bar
		}
		counts[seg]++
		bySeg[seg] = append(bySeg[seg], r)
	}
	return counts, bySeg
}

// stalledSet returns the set of wi IDs that are stalled (running-but-gone-quiet),
// sourced from the ready-queue path. Single-project mode queries that project;
// __all__ mode unions across accessible projects (admins: every visible project).
// Best-effort: query errors contribute nothing. Mirrors stalledCount's scoping.
func stalledSet(ctx context.Context, pool *pgxpool.Pool, project string, allMode bool, accessible []string, u *UserContext) map[string]bool {
	out := map[string]bool{}
	if pool == nil {
		return out
	}
	scope := []string{project}
	if allMode {
		scope = accessible
		if u != nil && u.Role == "admin" {
			scope = availableProjectsForUI(ctx, pool, u)
		}
	}
	for _, p := range scope {
		if p == "" {
			continue
		}
		if q, aerr := getQueueFn(ctx, pool, p, 100); aerr == nil && q != nil {
			for _, it := range q.Stalled {
				out[it.ID] = true
			}
		}
	}
	return out
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
	Theme             string
	User              *UserContext
	Project           string
	ProjectLabel      string          // human label for the project switcher button ("All projects" / "<name>")
	ProjectsAvailable []string        // sorted project names for the switcher
	ProjectCounts     map[string]int  // per-project active-wi count for the switcher
	TotalCount        int             // sum of ProjectCounts (the "All projects" count)
	AllMode           bool            // true when viewing across all accessible projects
	Status            string          // legacy single-status (kept for the hidden field / back-compat)
	Statuses          map[string]bool // multi-select status filter — set of selected status values
	StatusLabel       string          // human label for the status-filter button
	Kind              string
	Reporter          string
	Owner             string
	Mine              bool     // true when the Owner filter equals the current user (the "Mine" segment)
	ReporterOptions   []string // distinct reporter display names for the filter dropdown
	OwnerOptions      []string // distinct owner display names for the filter dropdown
	Limit             int
	Items             []*wiListRow  // flat list, kept for tests / fallback
	Groups            []wiListGroup // grouped rows for display (Needs you / status blocks / Unclaimed)
	Strip             stripCounts   // headline count strip — derived from Groups (single source of truth)
	// LCRS segment sidebar (aihub#185). SegCounts is the per-segment count for the
	// right sidebar nav; SelectedSeg is the single-selected segment; SegRows is the
	// rows the middle pane renders (only the selected segment).
	SegCounts   map[string]int
	SelectedSeg string
	SegRows     []*wiListRow
	Segments    []segNav // ordered sidebar nav (label + count + selected + divider)
	// Done-segment server pagination (aihub#298). Only meaningful when
	// SelectedSeg == "done": that segment grows without bound, so its rows are
	// fetched one cursor page at a time instead of being shipped whole and paged
	// in the browser. DoneCursor is the cursor that produced the page on screen
	// ("" = newest); DoneNextCursor is non-empty exactly when older rows exist,
	// and is the ONLY signal the template has that the archive continues past
	// what it rendered. DoneShown is len(SegRows), i.e. this page's row count —
	// deliberately distinct from SegCounts["done"], which is the archive total.
	DoneCursor     string
	DoneNextCursor string
	DoneShown      int
	Err            string
}

// segNav is one entry in the LCRS sidebar (aihub#185): a segment's display label,
// its live count, whether it is the selected segment, and whether a divider is
// drawn above it (the terminal "Done" segment sits below a divider). Built in the
// handler because Go templates can't reach the package-level segmentOrder/labels.
type segNav struct {
	Key     string
	Label   string
	Count   int
	On      bool
	Divider bool
}

// StatusOn reports whether the given status value is currently selected in the
// multi-select status filter. Used by the template to mark dropdown checkboxes.
func (d *wiListPageData) StatusOn(s string) bool { return d.Statuses[s] }

// wiListGroup is a display bucket of rows under a single heading. When Rows is
// empty the template renders the .empty empty-state component instead of
// silently dropping the section (the smart sections are always emitted; status
// blocks are emitted for every selected status even when empty — see
// groupListRows).
//
// Kind classifies the bucket so the template + client JS can treat them
// differently: "personal" (Needs you) and "pool" (Unclaimed) are smart sections
// that keep a fixed position (top / bottom respectively), while "status" blocks
// are drag-reorderable keyed by Status (the raw lowercase status value).
type wiListGroup struct {
	Label  string
	Kind   string // "personal" | "pool" | "status"
	Status string // raw status value for "status" blocks ("running", ...); "" otherwise
	Rows   []*wiListRow
}

// wiListRow is a view-model row for the list table. Decoupling from
// domain.WorkItem keeps template field access simple (no pointers / nil checks
// scattered through the template).
type wiListRow struct {
	ID              string
	Slug            string
	Project         string
	Seq             int64     // wi sequence within its project — sort tiebreak
	CreatedAt       time.Time // wi creation time — primary global sort key (DESC)
	WIType          string
	Priority        string
	Status          string
	HumanMode       string // requires_human_session folded for display: "Human" (true) / "Auto" (false) / "" (NULL/unclassified)
	NeedsHuman      bool   // requires_human_session == true (display-only "Human" mode hint)
	NeedsYou        bool   // current-attempt owner == viewer AND status in {paused,blocked} — drives the "Needs you" group + .row.hot highlight (set by groupListRows)
	Goal            string
	OwnerDisplay    string     // run_attempts.actor_display of current attempt; "" if no attempt
	ReporterDisplay string     // who filed the wi (always populated)
	Assignee        personChip // owner chip; zero value = unassigned (ghost avatar)
}

// wiDetailPageData is the data passed to wi_detail.html.tmpl.
type wiDetailPageData struct {
	Title          string
	Active         string
	Theme          string
	Origin         string // scheme://host of this request; frames post their height to it
	Nonce          string // this response's CSP nonce; the frame's bridge must run under it (aihub#243)
	User           *UserContext
	WI             *domain.WorkItem
	WIType         string     // flattened *WI.WIType
	HumanMode      string     // requires_human_session folded: "Human" (true) / "Auto" (false) / "" (NULL/unclassified)
	Content        string     // flattened *WI.Content for direct template access
	Milestone      string     // flattened *WI.Milestone
	AttemptID      string     // flattened *WI.CurrentAttemptID
	Labels         []string   // flattened *WI.Labels for the meta sidebar
	Reporter       personChip // who filed the wi — person-chip in the meta sidebar
	Assignee       personChip // current-attempt owner — zero value = unassigned (ghost)
	NeedsYou       bool       // owner == current user AND status in {paused,blocked} — drives the needs-you bar
	OwnerDisplay   string     // run_attempts.actor_display of current attempt, "" if none
	OwnerActive    time.Time  // run_attempts.last_active_at of current attempt
	OwnerHasActive bool       // true when OwnerActive is meaningful (non-zero)
	Dependencies   *depView
	Parent         *depEntry  // parent wi link (aihub#142); nil when this wi has no parent
	Children       []depEntry // child wi links ordered by seq ASC (aihub#142); empty when none
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
// caller can't see the cross-project wi. Status + Assignee are filled in by a
// view-layer batch lookup (fetchDepMeta) so each row can show the linked wi's
// status badge and owner avatar; both stay zero when the lookup misses (e.g.
// pool nil in tests or a hidden cross-project entry).
type depEntry struct {
	ID       string
	Slug     string
	Kind     string
	Hidden   bool
	Status   string     // linked wi status ("queued"/"running"/...) — drives the status badge
	Assignee personChip // linked wi current-attempt owner; zero = unassigned ghost
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
	EventType    string     // raw event_type, kept verbatim (the activity feed shows it)
	ActorDisplay string     // raw actor display name
	Actor        personChip // actor person-chip (zero value = no actor, e.g. system events)
	Payload      string
	Pinned       bool
	Family       string // semantic color family: "ok"|"good"|"warn"|"bad"|"info" (see eventFamily)
	Tag          string // short uppercase action tag for the feed icon (see eventTag)
}

// toEventViews flattens []EventRow into []eventView.
func toEventViews(rows []domain.EventRow) []eventView {
	out := make([]eventView, 0, len(rows))
	for _, r := range rows {
		ev := eventView{
			CreatedAt: r.CreatedAt,
			EventType: r.EventType,
			Pinned:    r.Pinned,
			Family:    eventFamily(r.EventType),
			Tag:       eventTag(r.EventType),
		}
		if r.ActorDisplay != nil {
			ev.ActorDisplay = *r.ActorDisplay
			ev.Actor = chipFor(*r.ActorDisplay)
		}
		if len(r.Payload) > 0 {
			ev.Payload = string(r.Payload)
		}
		out = append(out, ev)
	}
	return out
}

// eventFamily maps a raw event_type to one of the five semantic color families
// the activity feed renders (prototype design-system section 9). This is the
// SINGLE place the mapping lives:
//
//	"ok"   green + dot  in-progress     (attempt/claim started, step started, resume)
//	"good" green        positive output (attempt wrapped, memory saved, unblocked, resolved)
//	"warn" amber        waiting/blocked (paused, blocked, stall/zombie)
//	"bad"  red          failure/destructive (failed, cancelled, force takeover)
//	"info" grey         informational   (created/filed, note, reply, everything else)
//
// Matching is substring-based on the lowercased type so related variants
// (e.g. "attempt_completed_wrapped", "memory_commit_resolved") land in the
// right family without enumerating every concrete event_type.
func eventFamily(t string) string {
	s := strings.ToLower(t)
	switch {
	// failure / destructive — checked first so "*_failed" / "force_takeover"
	// never get misread as a positive/in-progress event.
	case strings.Contains(s, "fail"),
		strings.Contains(s, "cancel"),
		strings.Contains(s, "takeover"),
		strings.Contains(s, "error"):
		return "bad"
	// waiting / impediment.
	case strings.Contains(s, "pause"),
		strings.Contains(s, "block") && !strings.Contains(s, "unblock"),
		strings.Contains(s, "stall"),
		strings.Contains(s, "zombie"):
		return "warn"
	// positive output / completion.
	case strings.Contains(s, "wrap"),
		strings.Contains(s, "unblock"),
		strings.Contains(s, "resolve"),
		strings.Contains(s, "adopt"),
		strings.Contains(s, "memory") || strings.Contains(s, "artifact") || strings.Contains(s, "saved"):
		return "good"
	// in-progress.
	case strings.Contains(s, "claim"),
		strings.Contains(s, "resume"),
		strings.Contains(s, "started") || strings.Contains(s, "start"),
		strings.Contains(s, "step"),
		strings.Contains(s, "heartbeat"):
		return "ok"
	default:
		// created / note / reply / anything unmapped → informational grey.
		return "info"
	}
}

// eventTag derives the short uppercase action word shown in the feed icon
// (≤8 chars). It collapses the verbose event_type to its salient verb so the
// fixed-width icon reads cleanly; the full event_type still renders as the body
// text. Purely presentational.
func eventTag(t string) string {
	s := strings.ToLower(t)
	switch {
	case strings.Contains(s, "wrap"):
		return "wrap"
	case strings.Contains(s, "unblock"):
		return "unblock"
	case strings.Contains(s, "resolve"):
		return "resolve"
	case strings.Contains(s, "memory") || strings.Contains(s, "artifact") || strings.Contains(s, "saved"):
		return "save"
	case strings.Contains(s, "claim"):
		return "claim"
	case strings.Contains(s, "resume"):
		return "resume"
	case strings.Contains(s, "step"):
		return "step"
	case strings.Contains(s, "fail"):
		return "fail"
	case strings.Contains(s, "cancel"):
		return "cancel"
	case strings.Contains(s, "takeover"):
		return "takeover"
	case strings.Contains(s, "pause"):
		return "pause"
	case strings.Contains(s, "block"):
		return "block"
	case strings.Contains(s, "stall"), strings.Contains(s, "zombie"):
		return "stall"
	case strings.Contains(s, "note"):
		return "note"
	case strings.Contains(s, "reply"):
		return "reply"
	case strings.Contains(s, "create"):
		return "create"
	default:
		return "event"
	}
}

// artifactLink is the per-row data for the artifacts section on the detail page.
type artifactLink struct {
	MemID   string
	Type    string
	Content string
	// Versions is non-nil (len > 1) when this artifact has a supersede chain.
	// Each entry links to /ui/artifacts/<id>/html. Only populated on the /ui path.
	Versions []domain.MemoryVersionRef
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
// Project selection: an explicit ?project= wins — a concrete name narrows to a
// single project, __all__ selects the cross-project view. With no ?project= we
// default to the "All projects" view so the top-nav Work Items link always lands
// on every accessible project (not an arbitrary first one). The one exception is a
// non-admin with zero accessible projects: it falls through to the no-access guard
// below. availableProjectsForUI is the project set — non-admins get their
// ProjectRoles map; for admins (empty map by design — see middleware.go ~L104-106)
// it falls back to all visible projects via domain.ListProjects.
func handleUIWIList(pool *pgxpool.Pool, tmpl *template.Template) echo.HandlerFunc {
	return func(c echo.Context) error {
		u := GetUser(c)
		// u is populated by RequireUISession middleware, so it is never nil here in
		// practice. Guard defensively anyway so the rest of the handler can deref it
		// freely without a nil check on every access (and so staticcheck does not
		// flag SA5011 possible-nil-dereference on the bare u.Role / u.DisplayName uses).
		if u == nil {
			return c.NoContent(http.StatusUnauthorized)
		}
		ctx, cancel := contextWithTimeout(c)
		defer cancel()

		// HTMX in-place filter request: when the wi-list controls fire an
		// hx-get targeting #wi-list-body we return ONLY that fragment (the
		// "wi-list-body" block) instead of the full page, so the filter bar /
		// project switcher / count strip above are left untouched. We gate on
		// HX-Target too so a boosted full-page navigation (which also sets
		// HX-Request) still gets the whole document. Both branches feed the
		// SAME data through the SAME grouping pipeline — only the template name
		// differs (renderName below).
		renderName := "layout"
		if c.Request().Header.Get("HX-Request") == "true" && c.Request().Header.Get("HX-Target") == "wi-list-body" {
			renderName = "wi-list-body"
		}

		data := &wiListPageData{
			Title:  "Work items",
			Active: "wi",
			Theme:  themeFromCookie(c),
			User:   u,
		}

		projects := availableProjectsForUI(ctx, pool, u)
		data.ProjectsAvailable = projects

		// project resolution:
		//   "__all__"  → view across every project the caller can see
		//   ""         → default to the cross-project "All projects" view (the
		//                top-nav Work Items link lands here); a non-admin with zero
		//                accessible projects stays "" so the no-access guard fires
		//   "<name>"   → single project
		projectParam := strings.TrimSpace(c.QueryParam("project"))
		isAdmin := u.Role == "admin"
		allMode := projectParam == allProjectsSentinel ||
			(projectParam == "" && (isAdmin || len(projects) > 0))
		project := projectParam
		if allMode {
			project = allProjectsSentinel // keep sentinel for the template's selected-option check
		}
		data.Project = project
		data.AllMode = allMode
		if allMode || project == "" {
			data.ProjectLabel = "All projects"
		} else {
			data.ProjectLabel = project
		}

		// Per-project active-wi counts for the switcher dropdown. Scope to the
		// projects the user can see; nil for admins means "all projects".
		var countScope []string
		if u.Role != "admin" || u.ProjectScope != nil {
			countScope = projects
		}
		data.ProjectCounts, data.TotalCount = fetchProjectWICountsFn(ctx, pool, countScope)

		// Filter params.
		//
		// Status is multi-select: the dropdown emits repeated ?status= params
		// (one per checked box). We keep the union of valid values. The legacy
		// single ?status= shape still works (one value = a one-element set).
		statusParams := c.QueryParams()["status"]
		selStatuses := map[string]bool{}
		var statusList []string // deterministic order for the label / hidden field
		for _, s := range statusParams {
			s = strings.TrimSpace(s)
			if s != "" && validWIStatuses[s] && !selStatuses[s] {
				selStatuses[s] = true
				statusList = append(statusList, s)
			}
		}
		kindParam := strings.TrimSpace(c.QueryParam("kind"))
		if kindParam != "" && !validWIKinds[kindParam] {
			kindParam = ""
		}
		data.Statuses = selStatuses
		// data.Status keeps the first selected value so the legacy hidden field
		// and any single-value consumers stay populated.
		if len(statusList) > 0 {
			data.Status = statusList[0]
		}
		data.Kind = kindParam
		data.Reporter = strings.TrimSpace(c.QueryParam("reporter"))
		data.StatusLabel = statusFilterLabel(statusList)

		// Owner filter (aihub#185 follow-up): the list now DEFAULTS to All
		// (everyone's items). The header "me" toggle opts into a personal view by
		// setting ?owner=<my display name>; unset/empty = All. There is no separate
		// domain concept — it reuses the existing ?owner= filter. data.Mine drives
		// the in-memory owner scoping in segmentListRows + the toggle's on-state.
		ownerParam := strings.TrimSpace(c.QueryParam("owner"))
		data.Owner = ownerParam
		if ownerParam != "" && ownerParam == u.DisplayName {
			data.Mine = true
		}

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
			return renderTemplate(c, tmpl, renderName, data)
		}

		filter := domain.ListWorkItemsFilter{Limit: limit}
		// Multi-status: pass the union to the domain layer, which already does
		// `wi.status = ANY($n)` — no domain API change. Empty selection falls
		// back to the active-status set.
		if len(statusList) > 0 {
			filter.Status = statusList
		} else {
			filter.Status = activeStatuses
		}
		if kindParam != "" {
			filter.WIType = &kindParam
		}
		if data.Reporter != "" {
			filter.ReporterDisplay = &data.Reporter
		}
		// In "Mine" view we do NOT push OwnerDisplay into the DB query: the list
		// is owner-scoped for the personal sections (Needs you / Running) but
		// still has to surface the Unclaimed pool (which by definition has no
		// owner and would be filtered out). groupListRows does the owner scoping
		// in memory instead. The explicit-owner case (an "All"-view owner filter
		// the user typed) keeps the DB filter.
		if ownerParam != "" && !data.Mine {
			filter.OwnerDisplay = &ownerParam
		}

		// queryProject is what we hand to the domain layer: "" in view-all
		// mode (so it scopes by AccessibleProjects), else the single project.
		// facetScope is the project set used to populate the filter dropdowns.
		queryProject := project
		var facetScope []string
		if allMode {
			queryProject = ""
			if u.Role != "admin" || u.ProjectScope != nil {
				// non-admin (or scoped admin) view-all is bounded to their member/scoped projects
				filter.AccessibleProjects = projects
				facetScope = projects
			}
			// unscoped admin view-all: AccessibleProjects empty + facetScope nil = every project
		} else {
			// single project — access check (admin bypasses)
			if err := checkProjectAccessSoft(u, project); err != nil {
				data.Err = err.Error()
				return renderTemplate(c, tmpl, renderName, data)
			}
			facetScope = []string{project}
		}

		// Populate filter dropdown options for the current scope.
		facets := fetchWIFacetsFn(ctx, pool, facetScope)
		data.ReporterOptions = facets.Reporters
		data.OwnerOptions = facets.Owners

		viewer := u.DisplayName
		// Headline strip counts come from the grouping (single source of truth —
		// annotation #2). The strip is a STABLE overview and must NOT shift with
		// the display status filter: when a status filter is active, compute it
		// from the fixed active-status set — identical to the 5s poll
		// (handleUIQueuePartial) so the strip never flickers between first paint
		// and the poll. This runs BEFORE the display query so the display query
		// (with the user's status filter) stays the last list call. When no
		// status filter is set, the display query already uses activeStatuses, so
		// its groups are reused (no extra query).
		var stripGroups []wiListGroup
		if len(statusList) > 0 {
			sf := filter
			sf.Status = activeStatuses
			if _, ag, serr := fetchListGroups(ctx, pool, queryProject, sf, viewer, data.Mine, nil); serr == nil {
				stripGroups = ag
			}
		}

		rows, groups, aerr := fetchListGroups(ctx, pool, queryProject, filter, viewer, data.Mine, selStatuses)
		if aerr != nil {
			data.Err = aerr.Message
			return renderTemplate(c, tmpl, renderName, data)
		}
		data.Items = rows
		data.Groups = groups

		if stripGroups == nil {
			stripGroups = groups
		}
		// Stalled has no list group; pull it from the ready-queue path,
		// aggregated across projects in __all__ mode.
		data.Strip = groupCountsFromGroups(stripGroups)
		data.Strip.Stalled = stalledCount(ctx, pool, project, allMode, projects, u)

		// --- LCRS segment sidebar (aihub#185) -------------------------------
		// Single-select sidebar: ?seg= picks which segment the middle pane shows
		// (default: the actionable Unclaimed pool). The five active segments +
		// their counts come from the active rows already loaded; Done is terminal
		// — counted via aggregate (never load 100s of rows to count) and its rows
		// loaded only when it is the selected segment.
		selectedSeg := strings.TrimSpace(c.QueryParam("seg"))
		if _, ok := segmentLabels[selectedSeg]; !ok {
			selectedSeg = "unclaimed"
		}
		data.SelectedSeg = selectedSeg

		// Cursor for the Done segment's server-side pagination (aihub#298).
		// Empty = newest page. Only Done reads it; the active segments are
		// bounded and ship whole.
		doneCursor := strings.TrimSpace(c.QueryParam("done_cursor"))
		data.DoneCursor = doneCursor

		stalled := stalledSet(ctx, pool, project, allMode, projects, u)
		segCounts, segRows := segmentListRows(rows, viewer, data.Mine, stalled)

		// Done count is project-scoped (the archive is shown project-wide, not
		// owner-scoped like the active segments). Empty scope = all projects.
		doneScope := []string{project}
		if allMode {
			doneScope = nil
			if u.Role != "admin" || u.ProjectScope != nil {
				doneScope = projects
			}
		}
		segCounts["done"] = fetchDoneCountFn(ctx, pool, doneScope)
		data.SegCounts = segCounts

		// Ordered sidebar nav for the template (Done sits below a divider).
		data.Segments = make([]segNav, 0, len(segmentOrder))
		for _, k := range segmentOrder {
			data.Segments = append(data.Segments, segNav{
				Key:     k,
				Label:   segmentLabels[k],
				Count:   segCounts[k],
				On:      k == selectedSeg,
				Divider: k == "done",
			})
		}

		if selectedSeg == "done" {
			// Done is server-paginated (aihub#298). It is the one segment whose row
			// set only ever grows, so it cannot be "loaded fully and paged in the
			// browser" the way the active segments are.
			//
			// The previous code fetched a single page capped at df.Limit = 200 and
			// stopped, on the reasoning that this made the header count match the
			// rendered rows. Past 200 terminal items that reasoning inverts: the
			// header count comes from fetchDoneCount (a real COUNT(*), so it stays
			// exact) while the rows silently stop at 200, and dropdown.js then pages
			// those 200 client-side — printing "1–10 of 200" underneath a header
			// reading e.g. 417, with no control that could ever reach the rest. The
			// archive looked truncated to the point of data loss (it was not; the
			// rows were simply never requested).
			//
			// So the fetch now carries a cursor and reports whether older rows exist.
			// data.Limit is the page's own limit (default 50, ?limit= up to 200),
			// which keeps Done consistent with every other list on the page and
			// stays inside domain.ListWorkItems' 200 cap — above it that function
			// silently falls back to 50 (aihub#267).
			df := filter
			df.Status = doneStatuses
			df.Limit = data.Limit
			if doneCursor != "" {
				df.Cursor = &doneCursor
			}
			if dr, next, derr := fetchListRowsPaged(ctx, pool, queryProject, df); derr == nil {
				data.SegRows = dr
				data.DoneNextCursor = next
				data.DoneShown = len(dr)
			}
		} else {
			data.SegRows = segRows[selectedSeg]
		}

		return renderTemplate(c, tmpl, renderName, data)
	}
}

// fetchListRowsPaged is fetchListGroups' paginated sibling: it returns one
// cursor page of rows plus the cursor for the NEXT page.
//
// It exists because fetchListGroups discards res.NextCursor — which is correct
// for its callers (the count strip and the active-segment list both want a
// bounded snapshot, not a walkable archive) but leaves no way to page. Rather
// than widen that function's signature at three call sites, Done gets its own
// entry point. It also skips the grouping pass: the caller renders these rows
// directly as the selected segment, so wiListGroup would be built and dropped.
//
// The returned cursor is domain's opaque page token, empty when this is the
// last page. Ordering is whatever filter.Sort/Order specify (default
// created_at DESC), and the cursor is only valid for that same ordering —
// callers must not hand it back with a different sort.
//
// There is deliberately no `mine` parameter, which is narrower than it sounds:
// an EXPLICIT owner filter still applies to Done, because the caller's filter
// already carries filter.OwnerDisplay (set at the ?owner= handling above when
// the user typed an owner and is not in Mine view) and df inherits it. What is
// dropped is only the in-memory MINE scoping that groupListRows/segmentListRows
// apply to the active segments. The old call site passed data.Mine to
// fetchListGroups but then used its raw rows and discarded the grouping, so
// that scoping never reached Done's rows there either — this preserves the
// behaviour rather than implying a scoping that does not happen.
func fetchListRowsPaged(ctx context.Context, pool *pgxpool.Pool, queryProject string, filter domain.ListWorkItemsFilter) ([]*wiListRow, string, *domain.AihubError) {
	res, aerr := listWorkItemsFn(ctx, pool, queryProject, filter)
	if aerr != nil {
		return nil, "", aerr
	}

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
	// Same global re-sort fetchListGroups applies, so a page's rows read in the
	// same order as every other list on the page.
	sortListRows(rows)

	next := ""
	if res.NextCursor != nil {
		next = *res.NextCursor
	}
	return rows, next, nil
}

// fetchListGroups runs the wi list query, batch-loads current-attempt owners,
// projects rows, and groups them — the shared pipeline behind both the wi list
// page and the count-strip poll so the two never diverge. queryProject is "" in
// view-all mode (filter.AccessibleProjects bounds the scope); otherwise it is a
// single project name.
func fetchListGroups(ctx context.Context, pool *pgxpool.Pool, queryProject string, filter domain.ListWorkItemsFilter, viewer string, mine bool, selStatuses map[string]bool) ([]*wiListRow, []wiListGroup, *domain.AihubError) {
	res, aerr := listWorkItemsFn(ctx, pool, queryProject, filter)
	if aerr != nil {
		return nil, nil, aerr
	}

	// Batch-fetch current-attempt owners so the list can show "who claimed it"
	// without issuing N+1 queries.
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
	// One consistent global sort across ALL rows BEFORE grouping (round-3 #4).
	// In __all__ mode the domain layer concatenates per-project segments, so the
	// raw order is inconsistent (each project sorted independently). Re-sorting
	// here makes every group's rows follow the same key regardless of project.
	sortListRows(rows)
	return rows, groupListRows(rows, viewer, mine, selStatuses), nil
}

// sortListRows applies the single global ordering used across every list group:
// newest first by CreatedAt, tiebroken by project name then sequence so the
// order is deterministic and identical in every bucket (round-3 #4). Sorting the
// flat slice before grouping means each bucket inherits the same order — the
// per-project concatenation from __all__ mode no longer leaks through.
func sortListRows(rows []*wiListRow) {
	sort.SliceStable(rows, func(i, j int) bool {
		a, b := rows[i], rows[j]
		if !a.CreatedAt.Equal(b.CreatedAt) {
			return a.CreatedAt.After(b.CreatedAt) // newest first
		}
		if a.Project != b.Project {
			return a.Project < b.Project
		}
		return a.Seq < b.Seq
	})
}

// stalledCount returns the number of stalled (running-but-gone-quiet) items for
// the strip. In single-project mode it queries that project; in __all__ mode it
// sums across the accessible projects (admins: every visible project). This is
// the only strip cell not derived from the list grouping — the list has no
// "stalled" concept. Best-effort: query errors contribute 0.
func stalledCount(ctx context.Context, pool *pgxpool.Pool, project string, allMode bool, accessible []string, u *UserContext) int {
	if pool == nil {
		return 0
	}
	scope := []string{project}
	if allMode {
		scope = accessible
		if u != nil && u.Role == "admin" {
			scope = availableProjectsForUI(ctx, pool, u)
		}
	}
	total := 0
	for _, p := range scope {
		if p == "" {
			continue
		}
		if q, aerr := getQueueFn(ctx, pool, p, 100); aerr == nil && q != nil {
			total += len(q.Stalled)
		}
	}
	return total
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
			Theme:  themeFromCookie(c),
			Origin: pageOrigin(c),
			Nonce:  uiNonce(c),
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
		if wi.RequiresHumanSession != nil {
			if *wi.RequiresHumanSession {
				data.HumanMode = "Human"
			} else {
				data.HumanMode = "Auto"
			}
		}

		// Project access check — must come AFTER GetWorkItem because we don't
		// know the project until we've read the row.
		if err := checkProjectAccessSoft(u, wi.Project); err != nil {
			data.Err = err.Error()
			data.AccessDenied = true
			return renderTemplate(c, tmpl, "layout", data)
		}

		// Cross-project visibility roles, computed ONCE and shared (read-only)
		// across the dependency / parent / children fan-out goroutines.
		// ListDependencies + GetParentRef + ListChildren all hide cross-project
		// entries by checking `callerProjectRoles[entry.Project] != ""`. Admins
		// have an empty ProjectRoles map by design (middleware.go ~L104-106), so
		// synthesize a viewer role on every visible project so the admin sees the
		// real slug instead of the hidden placeholder.
		roles := u.ProjectRoles
		if u.Role == "admin" {
			roles = map[string]string{}
			for _, p := range availableProjectsForUI(ctx, pool, u) {
				roles[p] = "viewer"
			}
		}

		// Parallel fan-out for the side-load queries.
		var (
			deps      *domain.DependenciesResponse
			depsErr   *domain.AihubError
			parentRef *domain.WIRef
			childRefs []domain.WIRef
			events    []eventView
			eventsErr error
			arts      []artifactLink
			ownerInfo attemptOwner
			wg        sync.WaitGroup
		)

		wg.Add(6)

		go func() {
			defer wg.Done()
			deps, depsErr = listDependenciesFn(ctx, pool, wi.ID, roles, u.Role)
		}()

		go func() {
			defer wg.Done()
			// Parent link — nil parentRef means "no parent" (not an error).
			// Errors are best-effort: a failed lookup just drops the link.
			parentRef, _ = getParentRefFn(ctx, pool, wi.ID, roles, u.Role)
		}()

		go func() {
			defer wg.Done()
			// Children ordered by seq ASC. Best-effort: a failed lookup leaves
			// the Children card empty.
			childRefs, _ = listChildrenFn(ctx, pool, wi.ID, roles, u.Role)
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

		// Parent link (aihub#142): build the view entry straight from the domain
		// ref — parent/children are plain identity references, not dependency
		// edges, so they do not go through the blocking/blocked-by projection.
		if parentRef != nil {
			pe := wiRefToDepEntry(*parentRef)
			data.Parent = &pe
		}
		// Children ordered by seq ASC (the domain query ordered them).
		data.Children = make([]depEntry, 0, len(childRefs))
		for _, r := range childRefs {
			data.Children = append(data.Children, wiRefToDepEntry(r))
		}

		// Enrich every linked row (deps + parent + children) with the wi's status
		// + owner avatar so the sidebar reads like the prototype (status badge +
		// slug + owner). ONE batched query covering all directions; best-effort (a
		// miss just drops the adornments). Hidden cross-project entries carry no ID
		// and are skipped by the collectors below.
		metaIDs := depViewIDs(data.Dependencies)
		if data.Parent != nil && !data.Parent.Hidden && data.Parent.ID != "" {
			metaIDs = append(metaIDs, data.Parent.ID)
		}
		for _, e := range data.Children {
			if !e.Hidden && e.ID != "" {
				metaIDs = append(metaIDs, e.ID)
			}
		}
		meta := fetchDepMeta(ctx, pool, metaIDs)
		enrichDepView(data.Dependencies, meta)
		if data.Parent != nil {
			enrichDepEntry(data.Parent, meta)
		}
		for i := range data.Children {
			enrichDepEntry(&data.Children[i], meta)
		}

		if eventsErr != nil && data.Err == "" {
			data.Err = "failed to load events: " + eventsErr.Error()
		}
		data.Events = events
		data.Artifacts = arts
		data.OwnerDisplay = ownerInfo.Display
		data.OwnerActive = ownerInfo.LastActiveAt
		data.OwnerHasActive = !ownerInfo.LastActiveAt.IsZero()

		// Meta-sidebar person chips: reporter (always present) + assignee (the
		// current-attempt owner, ghost when unclaimed).
		data.Reporter = chipFor(wi.ReporterDisplay)
		data.Assignee = chipFor(ownerInfo.Display)
		data.Labels = wi.Labels

		// needs-you bar: this item is awaiting the current user — they own the
		// active attempt and it is paused/blocked (mirrors the list's Needs you
		// section gate).
		if u != nil && ownerInfo.Display != "" && ownerInfo.Display == u.DisplayName &&
			(wi.Status == "paused" || wi.Status == "blocked") {
			data.NeedsYou = true
		}

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
	seen := make(map[string]bool)
	for _, m := range resp.Items {
		// Skip private memories the caller can't read — recall already filters
		// these out, but defense in depth.
		if m.Visibility == "private" && m.AuthorUserID != u.UserID && u.Role != "admin" {
			continue
		}
		link := artifactLink{
			MemID:   m.ID,
			Type:    m.Type,
			Content: m.Content,
		}
		// aihub#124 version_history: populate version chain for artifacts that have
		// multiple versions. Best-effort — a query error leaves Versions nil, which
		// the template treats as "no history to show". Skip when pool is nil (tests).
		if pool != nil {
			if versions, verErr := wiVersionChainFn(ctx, pool, m.ID); verErr == nil && len(versions) > 1 {
				// Collapse the supersede chain to a single artifact: show only the
				// current (head) version; older versions live in its version
				// dropdown. Recall returns every version as its own row, so skip
				// any item that is a superseded (non-current) member of a chain.
				headID := m.ID
				for _, v := range versions {
					if v.IsCurrent {
						headID = v.ID
					}
				}
				if m.ID != headID || seen[headID] {
					continue
				}
				seen[headID] = true
				// Display newest-first (review feedback): reverse the
				// oldest->newest chain so the current version sits on top.
				link.Versions = reverseVersionRefs(versions)
			}
		}
		out = append(out, link)
	}
	return out
}

// reverseVersionRefs returns a new slice with the version chain reversed so the
// detail page can render newest-first while the chronological version numbers
// (computed from the total count in the template) stay correct.
func reverseVersionRefs(v []domain.MemoryVersionRef) []domain.MemoryVersionRef {
	out := make([]domain.MemoryVersionRef, len(v))
	for i := range v {
		out[len(v)-1-i] = v[i]
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
	// domain.DependenciesResponse field semantics match the template labels
	// directly: `Blocking` lists the wi's our wi blocks, `BlockedBy` lists the
	// wi's that block our wi. Copy straight across — no direction swap.
	v := &depView{
		Blocking:  make([]depEntry, 0, len(d.Blocking)),
		BlockedBy: make([]depEntry, 0, len(d.BlockedBy)),
	}
	for _, e := range d.Blocking {
		v.Blocking = append(v.Blocking, depEntryFrom(e))
	}
	for _, e := range d.BlockedBy {
		v.BlockedBy = append(v.BlockedBy, depEntryFrom(e))
	}
	return v
}

func depEntryFrom(e domain.DependencyListEntry) depEntry {
	if e.Slug == nil || e.ID == "hidden" {
		return depEntry{Kind: e.Kind, Hidden: true}
	}
	return depEntry{ID: e.ID, Slug: *e.Slug, Kind: e.Kind}
}

// wiRefToDepEntry projects a domain.WIRef (parent/children navigation) into the
// template-friendly depEntry. It reuses the SAME cross-project sentinel as
// depEntryFrom (Slug==nil || ID=="hidden" → Hidden) so hidden refs render the
// shared placeholder. Status + Assignee are left zero here; the view layer fills
// them via fetchDepMeta/enrichDepView, identical to the dependency rows.
// parent/children are plain identity references, so we build the entry straight
// from the row.
func wiRefToDepEntry(r domain.WIRef) depEntry {
	if r.Slug == nil || r.ID == "hidden" {
		return depEntry{Hidden: true}
	}
	return depEntry{ID: r.ID, Slug: *r.Slug}
}

// depMeta is the per-wi status + owner display loaded by fetchDepMeta.
type depMeta struct {
	Status     string
	OwnerActor string // current-attempt actor_display; "" when unclaimed
}

// fetchDepMeta batch-loads the status + current-attempt owner for a set of
// linked work item IDs in ONE query (work_items LEFT JOIN run_attempts on the
// current attempt). View-layer only — it reads existing columns to enrich the
// dependency rows with a status badge + owner avatar; no domain/API/DB change.
// Errors or a nil pool degrade to an empty map so the dep lists still render
// (just without the status/owner adornments).
func fetchDepMeta(ctx context.Context, pool *pgxpool.Pool, ids []string) map[string]depMeta {
	out := map[string]depMeta{}
	if pool == nil || len(ids) == 0 {
		return out
	}
	rows, err := pool.Query(ctx,
		`SELECT wi.id, wi.status, COALESCE(ra.actor_display, '')
		 FROM work_items wi
		 LEFT JOIN run_attempts ra ON ra.id = wi.current_attempt_id
		 WHERE wi.id = ANY($1)`,
		ids,
	)
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		var m depMeta
		if rows.Scan(&id, &m.Status, &m.OwnerActor) == nil {
			out[id] = m
		}
	}
	return out
}

// enrichDepEntry fills a single visible dep entry's Status + Assignee from the
// batch-loaded meta map. Hidden (cross-project) or ID-less entries are left
// untouched. Shared by enrichDepView and the parent/children enrichment.
func enrichDepEntry(e *depEntry, meta map[string]depMeta) {
	if e == nil || e.Hidden || e.ID == "" {
		return
	}
	if m, ok := meta[e.ID]; ok {
		e.Status = m.Status
		e.Assignee = chipFor(m.OwnerActor)
	}
}

// enrichDepView fills each visible dep entry's Status + Assignee from the
// batch-loaded meta map. Hidden (cross-project) entries are left untouched.
func enrichDepView(v *depView, meta map[string]depMeta) {
	if v == nil {
		return
	}
	fill := func(entries []depEntry) {
		for i := range entries {
			enrichDepEntry(&entries[i], meta)
		}
	}
	fill(v.Blocking)
	fill(v.BlockedBy)
}

// depViewIDs collects the non-hidden linked wi IDs across both directions so
// fetchDepMeta can load them in one round-trip.
func depViewIDs(v *depView) []string {
	if v == nil {
		return nil
	}
	ids := make([]string, 0, len(v.Blocking)+len(v.BlockedBy))
	add := func(entries []depEntry) {
		for _, e := range entries {
			if !e.Hidden && e.ID != "" {
				ids = append(ids, e.ID)
			}
		}
	}
	add(v.Blocking)
	add(v.BlockedBy)
	return ids
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
		Seq:             wi.Seq,
		CreatedAt:       wi.CreatedAt,
		Priority:        wi.Priority,
		Status:          wi.Status,
		Goal:            wi.Goal,
		ReporterDisplay: wi.ReporterDisplay,
	}
	if wi.WIType != nil {
		row.WIType = *wi.WIType
	}
	if wi.RequiresHumanSession != nil {
		if *wi.RequiresHumanSession {
			row.HumanMode = "Human"
			row.NeedsHuman = true
		} else {
			row.HumanMode = "Auto"
		}
	}
	if wi.CurrentAttemptID != nil {
		if o, ok := owners[*wi.CurrentAttemptID]; ok {
			row.OwnerDisplay = o.Display
		}
	}
	row.Assignee = chipFor(row.OwnerDisplay)
	return row
}

// titleASCII upper-cases the first byte of an ASCII word (e.g. "queued" ->
// "Queued"). Status names are a fixed ASCII enum, so this avoids the deprecated
// strings.Title / the heavier golang.org/x/text cases.Title.
func titleASCII(s string) string {
	if s == "" {
		return s
	}
	b := []byte(s)
	if b[0] >= 'a' && b[0] <= 'z' {
		b[0] -= 'a' - 'A'
	}
	return string(b)
}

// statusBlockOrder is the canonical default order of the six status blocks
// (top->bottom) when no drag order is applied. The client may reorder these
// live (see dropdown.js applyStatusGroupOrder); the server always emits them in
// this order so the saved drag order has a stable base to permute.
var statusBlockOrder = []string{"queued", "running", "paused", "blocked", "wrapped", "cancelled"}

// groupListRows buckets the flat row list into display groups under the
// "status-blocks primary" model (Model A). Both Mine and All views group work
// items by STATUS into blocks, with two SMART sections that are not status
// blocks: "Needs you" (pinned first) and "Unclaimed" (pinned last). Every item
// lands in EXACTLY ONE section by this precedence (no item appears twice):
//
//  1. no current-attempt owner AND status in {queued, blocked} -> "Unclaimed"
//     (the claimable pool — relevant to everyone, ALWAYS shown in both views).
//  2. else current-attempt owner == viewer AND status in {paused, blocked}
//     -> "Needs you" (your own work awaiting your attention).
//  3. else -> a status block keyed by the item's OWN status (one of
//     queued / running / paused / blocked / wrapped / cancelled).
//
// Owner scope:
//   - Mine view (mine=true): only items whose current-attempt owner is the
//     viewer feed "Needs you" and the status blocks. A running item I own goes
//     to the Running block; a queued item I own goes to the Queued block. Items
//     owned by others are dropped (except the ownerless Unclaimed pool).
//   - All view (mine=false): all items feed the status blocks; "Needs you" still
//     requires owner == viewer by definition.
//   - "Unclaimed" is NOT owner-scoped — it is always shown in both views.
//
// Which status blocks render is driven by selStatuses (the statuses chosen in
// the multi-select). The SERVER emits a block for every SELECTED status even
// when it has zero items (the template renders the .empty state inside it), so
// the filter's effect is always perceptible. When no status is selected the
// full six blocks render (empty ones included). "Needs you" / "Unclaimed" are
// always emitted (empty -> empty-state) and are never part of selStatuses.
//
// Render order top->bottom: Needs you -> status blocks (canonical order;
// reordered live by the client) -> Unclaimed.
//
// A requires_human_session item with no owner is NOT "Needs you" — it has not
// been claimed by the viewer, so it belongs in Unclaimed (the aihub#4 bug).
func groupListRows(rows []*wiListRow, viewer string, mine bool, selStatuses map[string]bool) []wiListGroup {
	const (
		gNeeds     = "Needs you"
		gUnclaimed = "Unclaimed"
	)
	var needs, unclaimed []*wiListRow
	// Status blocks keyed by raw status value. Only the SELECTED statuses get a
	// block; rows whose status is not selected are dropped from the middle (they
	// can still reach Needs you / Unclaimed via the smart-section rules above).
	statusBuckets := map[string][]*wiListRow{}
	for _, s := range statusBlockOrder {
		if len(selStatuses) == 0 || selStatuses[s] {
			statusBuckets[s] = []*wiListRow{}
		}
	}

	for _, r := range rows {
		switch {
		case r.OwnerDisplay == "" && (r.Status == "queued" || r.Status == "blocked"):
			unclaimed = append(unclaimed, r)
		case viewer != "" && r.OwnerDisplay == viewer && (r.Status == "paused" || r.Status == "blocked"):
			r.NeedsYou = true // gates the .row.hot left bar
			needs = append(needs, r)
		case mine && (viewer == "" || r.OwnerDisplay != viewer):
			// Mine view is owner-scoped: items owned by others never feed the
			// status blocks (Unclaimed already captured the ownerless pool).
			continue
		default:
			if _, ok := statusBuckets[r.Status]; ok {
				statusBuckets[r.Status] = append(statusBuckets[r.Status], r)
			}
		}
	}

	out := make([]wiListGroup, 0, 2+len(statusBuckets))
	// "Needs you" is the smart section pinned FIRST — always emitted (empty ->
	// empty-state). Not drag-reorderable, not part of selStatuses.
	out = append(out, wiListGroup{Label: gNeeds, Kind: "personal", Rows: needs})
	// Status blocks in canonical order; the client permutes them live per the
	// saved drag order. Emitted for every selected status (or all six when none
	// is selected), empty ones included.
	for _, k := range statusBlockOrder {
		rs, ok := statusBuckets[k]
		if !ok {
			continue
		}
		out = append(out, wiListGroup{Label: titleASCII(k), Kind: "status", Status: k, Rows: rs})
	}
	// "Unclaimed" is the smart pool pinned LAST — always emitted (empty ->
	// empty-state). Not owner-scoped, not drag-reorderable.
	out = append(out, wiListGroup{Label: gUnclaimed, Kind: "pool", Rows: unclaimed})
	return out
}

// stripCounts holds the four headline counts shown in the .qstrip count strip.
// Running / NeedsYou / Unclaimed are derived from the SAME grouping the list
// renders (groupCountsFromGroups), so the strip numbers always equal the
// visible list sections. Stalled has no list group — it is the GC-derived
// "running but gone quiet" segment carried over from the ready queue.
type stripCounts struct {
	Running   int
	NeedsYou  int
	Unclaimed int
	Stalled   int
}

// groupCountsFromGroups reads Running / Needs you / Unclaimed counts straight
// off the grouped output so the strip is a faithful tally of what the list
// shows. Running is the "running" STATUS block (Kind "status"); Needs you and
// Unclaimed are the smart sections. Other status blocks are ignored — the strip
// only headlines these three (plus Stalled, filled in by the caller).
func groupCountsFromGroups(groups []wiListGroup) stripCounts {
	var s stripCounts
	for _, g := range groups {
		switch {
		case g.Kind == "status" && g.Status == "running":
			s.Running = len(g.Rows)
		case g.Label == "Needs you":
			s.NeedsYou = len(g.Rows)
		case g.Label == "Unclaimed":
			s.Unclaimed = len(g.Rows)
		}
	}
	return s
}

// statusFilterLabel builds the human label for the multi-select status filter
// button: "All status" when nothing is selected, the single status name when
// exactly one is chosen, or "N selected" for two or more.
func statusFilterLabel(sel []string) string {
	switch len(sel) {
	case 0:
		return "All status"
	case 1:
		return titleASCII(sel[0])
	default:
		return strconv.Itoa(len(sel)) + " selected"
	}
}

// renderHTMLStatus is a 404-aware variant of renderTemplate. The shared
// renderTemplate hard-codes status 200 via c.HTMLBlob, but the detail page
// wants 404 when the wi is missing — so we drive the response manually.
func renderHTMLStatus(c echo.Context, tmpl *template.Template, name string, data any, status int) error {
	var buf strings.Builder
	if err := tmpl.ExecuteTemplate(&buf, name, data); err != nil {
		return c.String(http.StatusInternalServerError, "template error: "+err.Error())
	}
	setRenderHeaders(c)
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
	if u.ProjectScope != nil && *u.ProjectScope != project {
		return errSoft("no access to project " + project)
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
