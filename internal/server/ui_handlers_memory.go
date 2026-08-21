package server

import (
	"context"
	"encoding/json"
	"html/template"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/labstack/echo/v4"

	"github.com/GMISWE/ieops-aihub/internal/domain"
)

// recallMemoryFn is the shape of domain.Recall — typed locally so tests can
// inject a fake. (Named `recallMemoryFn` to avoid collision with the WI peer
// handler's `recallFn` variable in ui_handlers_wi.go.)
type recallMemoryFn func(ctx context.Context, pool *pgxpool.Pool, req *domain.RecallRequest) (*domain.RecallResponse, error)

// recallMemoriesFn is the production-wired Recall — swappable in tests via the
// same pattern queue handlers use (getQueueFn).
var recallMemoriesFn recallMemoryFn = domain.Recall

// loadMemoryFn is the production-wired GetMemoryByID — swappable in tests.
var loadMemoryFn memLoaderFn = domain.GetMemoryByID

// resolveLatestFn is the production-wired GetLatestByID — swappable in tests.
// Used ONLY on /ui responses (handleArtifactHTML, handleUIMemoryDetail) to
// resolve a possibly-superseded memory id to the current head of its
// supersede lineage (aihub#248). Kept as a seam separate from loadMemoryFn so
// tests can inject a head that differs from the originally-requested record
// and assert on each seam's call count independently.
var resolveLatestFn memLoaderFn = domain.GetLatestByID

// CommitAnchor identifies the section of a spec/plan artifact that a CommitEntry
// is anchored to. Both fields come from the UI at annotation time; they are
// stored verbatim and never re-derived server-side.
//
// aihub#125: Quote/Prefix/Suffix carry an exact text selection with surrounding
// context (omitempty so legacy entries without these fields unmarshal cleanly).
type CommitAnchor struct {
	HeadingID   string `json:"heading_id"`
	HeadingText string `json:"heading_text"`
	Quote       string `json:"quote,omitempty"`  // exact selected text
	Prefix      string `json:"prefix,omitempty"` // context before the selection
	Suffix      string `json:"suffix,omitempty"` // context after the selection
}

// CommitReply is a threaded reply to a CommitEntry, stored inside the entry's
// replies array. Fields mirror CommitEntry's author/body shape (aihub#125).
type CommitReply struct {
	ID            string `json:"id"`
	AuthorUserID  string `json:"author_user_id"`
	AuthorDisplay string `json:"author_display"`
	Body          string `json:"body"`
	CreatedAt     string `json:"created_at"` // RFC3339
}

// Commit status constants. An absent (empty) status is treated as open for
// backward compatibility with entries written before aihub#124.
const (
	CommitStatusOpen     = "open"
	CommitStatusResolved = "resolved"
)

// CommitEntry is one human annotation stored in the memories.commits column.
// aihub#70 v3: ID is required (backfilled by 0022); UpdatedAt is present only
// after an edit. The template surfaces Edit/Delete affordances when the
// current user is the entry's author or has admin role.
//
// aihub#124: Anchor, Status, Reply, ResolvedAt, ResolvedBy are all optional
// (omitempty) so existing entries without these fields unmarshal cleanly.
type CommitEntry struct {
	ID            string        `json:"id"`
	AuthorUserID  string        `json:"author_user_id"`
	AuthorDisplay string        `json:"author_display"`
	Body          string        `json:"body"`
	CreatedAt     string        `json:"created_at"`
	UpdatedAt     string        `json:"updated_at,omitempty"`
	Anchor        *CommitAnchor `json:"anchor,omitempty"`
	Status        string        `json:"status,omitempty"`
	Reply         string        `json:"reply,omitempty"`
	ResolvedAt    string        `json:"resolved_at,omitempty"`
	ResolvedBy    string        `json:"resolved_by,omitempty"`
	Replies       []CommitReply `json:"replies,omitempty"`
}

// IsOpen reports whether the entry is in the open state. Entries written
// before aihub#124 (Status=="") are treated as open.
func (c CommitEntry) IsOpen() bool {
	return c.Status == "" || c.Status == CommitStatusOpen
}

// IsResolved reports whether the entry has been resolved.
func (c CommitEntry) IsResolved() bool {
	return c.Status == CommitStatusResolved
}

// memListPageData drives memories.html.tmpl.
type memListPageData struct {
	Title             string
	Active            string
	Theme             string
	User              *UserContext
	Project           string
	ProjectsAvailable []string
	NoAccess          bool
	AccessDenied      bool
	// Filter state (echoed back into the form).
	Type        string
	TypeOptions []string
	StrengthMin float64
	Query       string
	WorkItemID  string
	Limit       int
	// Results.
	Items       []domain.MemoryWithStrength
	HiddenCount int
	// For the link back / pagination preservation.
	FilterQuery string
	ErrMessage  string
}

// MemRelatedRef is the view-layer representation of a related memory, sourced
// from mem.Attrs["related_ids"] (a JSON string array written by pf_remember).
//
// Type and Summary are empty for now — the attrs source only provides IDs.
// TODO(aihub#112 Stream A): replace attrs.related_ids source with join-table-
// enriched Related[] including type and summary.
type MemRelatedRef struct {
	ID      string
	Type    string
	Summary string
}

// memDetailPageData drives memory_detail.html.tmpl.
type memDetailPageData struct {
	Title      string
	Active     string
	Theme      string
	Origin     string // scheme://host of this request; frames post their height to it
	Nonce      string // this response's CSP nonce; the frame's bridge must run under it (aihub#243)
	User       *UserContext
	Memory     *domain.Memory
	BackQuery  string
	RenderAsMD bool
	// ViewingSource reports that ?source=1 asked for the unrendered content. Kept separate
	// from RenderAsMD because they answer different questions: RenderAsMD is "would this
	// content render as markdown at all", ViewingSource is "did the reader ask not to".
	// The template needs both to label the toggle correctly for content that is not
	// markdown in the first place, where there is nothing to toggle to.
	ViewingSource bool
	// AgentHTML is this memory's rendered_html when it is AGENT-AUTHORED rather than
	// server-rendered from the markdown. Empty otherwise, and the view toggle is then not
	// shown at all: for an auto-rendered row the two views are the same content by two
	// routes, so offering a switch would imply a second authored artifact that does not
	// exist. The condition mirrors the D7 gate in routes_artifacts.go — fact.architecture is
	// not in renderTypes, so a non-NULL rendered_html on that type came from the author.
	//
	// aihub#240: this used to be AgentHTMLHref, a link off to /ui/artifacts/<id>/html. The
	// twin's two halves now live on one page behind a [HTML | Markdown] switch, because
	// sending the reader to a different URL to see the other half of the same artifact is
	// not a comparison — it is navigation, and it loses the page's Comments and Details.
	AgentHTML string
	// ShowAgentHTML is which half is on screen. It defaults to the agent's HTML whenever
	// there is one: that half is the artifact the agent actually authored for a reader,
	// while the markdown is its source twin.
	ShowAgentHTML bool
	Commits       []CommitEntry
	Related       []MemRelatedRef
}

// Package-level template cache. Initialised by registerUIMemoryHandlers.
var (
	memListTmpl   *template.Template
	memDetailTmpl *template.Template
)

// registerUIMemoryHandlers wires /ui/memories, /ui/memories/:id, and
// POST /ui/memories/:id/commit (the only write operation in the UI).
// The 3rd template arg is the shared root (unused — we build per-page
// templates here to avoid {{define "content"}} collisions across pages).
func registerUIMemoryHandlers(g *echo.Group, pool *pgxpool.Pool, _ *template.Template) {
	memListTmpl = pageTemplate("memories.html.tmpl")
	memDetailTmpl = pageTemplate("memory_detail.html.tmpl")

	g.GET("/memories", handleUIMemories(pool, memListTmpl))
	g.GET("/memories/:id", handleUIMemoryDetail(pool, memDetailTmpl))
	g.POST("/memories/:id/commit", handleUICommitMemory(pool))
	g.POST("/memories/:id/commit/:commit_id/edit", handleUIEditCommit(pool))
	g.POST("/memories/:id/commit/:commit_id/delete", handleUIDeleteCommit(pool))
	g.POST("/memories/:id/commit/:commit_id/reply", handleUIReplyCommit(pool))
	g.POST("/memories/:id/commit/:commit_id/resolve", handleUIResolveCommit(pool))
}

// handleUIMemories renders the memory index. The package-level recallMemoriesFn
// is overridable in tests so we never hit a live DB.
func handleUIMemories(pool *pgxpool.Pool, tmpl *template.Template) echo.HandlerFunc {
	return func(c echo.Context) error {
		u := GetUser(c)
		if u == nil {
			return redirectToLogin(c)
		}
		ctx, cancel := contextWithTimeout(c)
		defer cancel()

		projects := availableProjectsForUI(ctx, pool, u)
		project := c.QueryParam("project")
		if project == "" && len(projects) > 0 {
			project = projects[0]
		}

		typeOptions := append(
			[]string{"experience.*", "fact.*", "rule.*", "methodology.*"},
			domain.MemoryTypeEnum...,
		)
		data := memListPageData{
			Title:             "Memories",
			Active:            "memories",
			Theme:             themeFromCookie(c),
			User:              u,
			Project:           project,
			ProjectsAvailable: projects,
			Type:              c.QueryParam("type"),
			TypeOptions:       typeOptions,
			Query:             c.QueryParam("q"),
			WorkItemID:        c.QueryParam("wi"),
		}

		// Strength filter — default 0.3, clamp to non-negative.
		if raw := c.QueryParam("strength_min"); raw != "" {
			if f, err := strconv.ParseFloat(raw, 64); err == nil && f >= 0 {
				data.StrengthMin = f
			} else {
				data.StrengthMin = 0.3
			}
		} else {
			data.StrengthMin = 0.3
		}

		// Limit — default 50, max 200.
		data.Limit = 50
		if raw := c.QueryParam("limit"); raw != "" {
			if n, err := strconv.Atoi(raw); err == nil && n > 0 {
				if n > 200 {
					n = 200
				}
				data.Limit = n
			}
		}

		// Build filter-query string for "self link" pagination / detail back-link.
		data.FilterQuery = buildMemFilterQuery(project, data.Type, data.StrengthMin, data.Query, data.WorkItemID, data.Limit)

		// Access gates.
		if u.Role != "admin" && len(u.ProjectRoles) == 0 {
			data.NoAccess = true
			return renderTemplate(c, tmpl, "layout", data)
		}
		if project == "" {
			data.AccessDenied = true
			return renderTemplate(c, tmpl, "layout", data)
		}
		if uiScopeBlocks(u, project) {
			data.AccessDenied = true
			return renderTemplate(c, tmpl, "layout", data)
		}
		if u.Role != "admin" {
			if _, ok := u.ProjectRoles[project]; !ok {
				data.AccessDenied = true
				return renderTemplate(c, tmpl, "layout", data)
			}
		}

		// Build RecallRequest. domain.Recall natively supports the "prefix.*"
		// wildcard form via strings.HasSuffix(t, ".*") at memory.go:442, so we
		// pass the raw type query through unchanged.
		req := &domain.RecallRequest{
			Project:      project,
			MinStrength:  data.StrengthMin,
			Query:        data.Query,
			TopK:         data.Limit,
			CallerUserID: u.UserID,
			CallerRole:   u.Role,
		}
		if data.Type != "" {
			req.Types = []string{data.Type}
		}
		if data.WorkItemID != "" {
			req.WorkItemID = &data.WorkItemID
		}

		resp, err := recallMemoriesFn(ctx, pool, req)
		if err != nil {
			data.ErrMessage = "failed to load memories: " + err.Error()
			return renderTemplate(c, tmpl, "layout", data)
		}

		// Per-row visibility re-check — defense-in-depth over Recall's inline
		// WHERE clauses, and the single source of truth shared with the artifact
		// HTML route (routes_artifacts.go::checkMemoryVisibility).
		hidden := 0
		filtered := make([]domain.MemoryWithStrength, 0, len(resp.Items))
		for i := range resp.Items {
			if !memoryVisibleTo(u, &resp.Items[i].Memory) {
				hidden++
				continue
			}
			filtered = append(filtered, resp.Items[i])
		}
		data.Items = filtered
		data.HiddenCount = hidden

		return renderTemplate(c, tmpl, "layout", data)
	}
}

// memLoaderFn lets tests inject a fake memory loader.
type memLoaderFn func(ctx context.Context, pool *pgxpool.Pool, id string) (*domain.Memory, *domain.AihubError)

// appendQueryString appends c's raw query string (verbatim, as Echo already
// received it — NOT re-encoded from c.QueryParams()) to path, if present.
// Used by the /ui-only lineage-head redirects in handleArtifactHTML and
// handleUIMemoryDetail so a preserved param like ?back=... or ?view=md is
// never subtly mangled by a decode/re-encode round trip (aihub#248).
func appendQueryString(c echo.Context, path string) string {
	if qs := c.QueryString(); qs != "" {
		return path + "?" + qs
	}
	return path
}

// exactVersionParam is the /ui-only marker that opts a link out of the
// lineage-head redirect (aihub#248 spec amendment to non-goal 6, following
// deep review mem_eCIctvsx). It exists SOLELY so the two shipped
// deliberate-past-version surfaces — the artifact viewer's side-rail
// "Version history" links (routes_artifacts.go, buildSideRail's srVersions)
// and the wi-detail page's per-version "View" link
// (templates/wi_detail.html.tmpl) — can still reach a specific superseded
// revision once every non-head row's LatestID resolves to the head. It is
// NOT a general-purpose "disable the redirect" switch: no other link site may
// emit it, and honoring it never bypasses the authorization already applied
// to the requested record (checkProjectAccess/checkMemoryVisibility on mem
// still run first, unconditionally, in both handlers) — it only skips head
// resolution.
const exactVersionParam = "pf_exact"

// isExactVersionRequest reports whether c carries the exact-version marker.
func isExactVersionRequest(c echo.Context) bool {
	return c.QueryParam(exactVersionParam) == "1"
}

// handleUIMemoryDetail renders a single memory's detail page. Spec/plan
// artifacts redirect to /ui/artifacts/<id>/html, the cookie-authed mirror of
// /v1/artifacts/<id>/html (same handler — handleArtifactHTML — mounted under
// uiGroup so the session cookie satisfies auth without requiring users to
// paste their bearer key).
func handleUIMemoryDetail(pool *pgxpool.Pool, tmpl *template.Template) echo.HandlerFunc {
	return func(c echo.Context) error {
		u := GetUser(c)
		if u == nil {
			return redirectToLogin(c)
		}

		memID := c.Param("id")
		if memID == "" {
			return writeError(c, domain.NewErr(domain.ErrBadRequest, "memory id is required"))
		}

		ctx, cancel := contextWithTimeout(c)
		defer cancel()

		mem, aihubErr := loadMemoryFn(ctx, pool, memID)
		if aihubErr != nil {
			return writeError(c, aihubErr)
		}

		// Project + visibility gates.
		if err := checkProjectAccess(c, u, mem.Project, "viewer"); err != nil {
			return err
		}
		if err := checkMemoryVisibility(c, u, mem); err != nil {
			return err
		}

		// aihub#248: this id may have been superseded (mem.LatestID != nil and
		// != mem.ID) by a later pf_update_memory/pf_save_artifact call. On /ui
		// (this handler is only ever mounted there) we want the reader to land
		// on the lineage head instead of silently viewing stale content — but
		// ONLY once the head is re-authorized for THIS caller; reusing the
		// authorization already established above for mem would be a privilege
		// escalation, since UpdateMemory lets a new version's Visibility diverge
		// from its predecessor's. hasProjectAccess/memoryVisibleTo are the pure
		// (non-response-writing) mirrors of checkProjectAccess/checkMemoryVisibility
		// — calling the side-effecting originals against head would commit a
		// 403/401 to c even on the "fall back to mem" path, leaking that a newer,
		// inaccessible version exists. Any failure to resolve or authorize the
		// head — including a genuinely nonexistent lineage — silently falls back
		// to rendering the originally-requested mem exactly as today.
		//
		// This MUST happen before the spec/plan-type redirect immediately below:
		// resolving the head first means a stale spec/plan id whose head is also
		// spec/plan reaches /ui/artifacts/<head>/html in one hop, and the type
		// check below is driven by the head (once resolved) rather than by the
		// stale id's own type, so a spec superseded by a non-spec (or vice versa)
		// never redirects on the wrong type.
		//
		// aihub#248 review (W2): head.ID != mem.ID only rules out a self-redirect;
		// it says nothing about the TARGET's own cursor. Also require
		// head.LatestID == nil || *head.LatestID == head.ID, so a head whose own
		// latest_id points elsewhere (multi-hop, or a 2-cycle) does not redirect
		// again — the normal write-path invariants keep every real head
		// self-headed, but this handler stays defensive rather than trusting that.
		//
		// aihub#248 review (blocking, spec amendment): isExactVersionRequest skips
		// resolution entirely when the caller followed a deliberate past-version
		// link (side rail / wi-detail "View") that carries pf_exact=1 — see
		// exactVersionParam's doc comment. It does NOT bypass the
		// checkProjectAccess/checkMemoryVisibility gates on mem above; it only
		// skips head resolution.
		target := mem
		resolvedHead := false
		if !isExactVersionRequest(c) && mem.LatestID != nil && *mem.LatestID != mem.ID {
			if head, aerr := resolveLatestFn(ctx, pool, mem.ID); aerr == nil && head != nil &&
				head.ID != mem.ID &&
				(head.LatestID == nil || *head.LatestID == head.ID) &&
				hasProjectAccess(u, head.Project, "viewer") && memoryVisibleTo(u, head) {
				target = head
				resolvedHead = true
			}
		}

		// Spec / plan: hand off to the artifact viewer that already wraps the
		// cached rendered_html in a chroma-styled document. If rendered_html is
		// missing (legacy row), the artifact endpoint will return a clear 404 —
		// preferable to re-rendering markdown a second time here.
		//
		// aihub#248 review (minor): forwarding the query string here even when
		// resolvedHead is false is intentional, not an oversight left over from
		// the head-redirect case — it predates this feature (any ?back=... or
		// ?view=md on the /ui/memories request must survive the hop) and is now
		// load-bearing for a second reason: pf_exact must also survive this hop,
		// or a deliberate exact-version link that happens to land on this handler
		// (e.g. a superseded spec/plan id requested with ?pf_exact=1) would lose
		// its exactness the moment it hands off to the artifact viewer. Pinned by
		// TestUIMemoryDetail_SpecRedirect_ForwardsQueryString.
		if target.Type == "methodology.spec" || target.Type == "methodology.plan" {
			return c.Redirect(http.StatusFound, appendQueryString(c, "/ui/artifacts/"+url.PathEscape(target.ID)+"/html"))
		}
		// Non-spec/plan head resolved: redirect so the address bar self-heals to
		// the canonical id rather than rendering head content under the stale URL.
		if resolvedHead {
			return c.Redirect(http.StatusFound, appendQueryString(c, "/ui/memories/"+url.PathEscape(target.ID)))
		}

		// Parse commits from the JSONB column; failures yield an empty slice
		// so they never block page rendering.
		var commits []CommitEntry
		if len(mem.Commits) > 0 {
			_ = json.Unmarshal(mem.Commits, &commits)
		}

		// aihub#240: the twin pair on one page. agentHTML is non-empty only for an
		// agent-authored rendered_html; ?view=md asks for the markdown half, and ?source=1
		// (the raw-content view) implies the markdown half because that is the half it is
		// the source of. Everything else lands on the HTML half, including a bare URL —
		// that is the "agent HTML is the default view" rule.
		agentHTML := agentAuthoredHTML(mem)
		viewingSource := c.QueryParam("source") != ""
		showAgentHTML := agentHTML != "" && c.QueryParam("view") != "md" && !viewingSource

		data := memDetailPageData{
			Title:     "Memory " + mem.ID,
			Active:    "memories",
			Theme:     themeFromCookie(c),
			Origin:    pageOrigin(c),
			Nonce:     uiNonce(c),
			User:      u,
			Memory:    mem,
			BackQuery: c.QueryParam("back"),
			// aihub#240: ?source=1 shows the stored content unrendered. The twin-pair
			// architecture makes "what the agent wrote" and "what the reader sees" two
			// different artifacts, and the rendered view alone cannot show which is which
			// — a d2 fence in the source arrives as a figure, so the source is not
			// recoverable by eye from the rendered page.
			RenderAsMD:    !showAgentHTML && looksLikeMarkdown(mem.Content) && !viewingSource,
			ViewingSource: viewingSource,
			AgentHTML:     agentHTML,
			ShowAgentHTML: showAgentHTML,
			Commits:       commits,
			Related:       parseMemRelatedRefs(mem.Attrs),
		}
		return renderTemplate(c, tmpl, "layout", data)
	}
}

// memoryVisibleTo mirrors checkMemoryVisibility without touching c — used in
// the list path where each excluded row should silently drop instead of
// short-circuiting the response.
func memoryVisibleTo(u *UserContext, mem *domain.Memory) bool {
	if u == nil {
		return false
	}
	if u.Role == "admin" {
		return true
	}
	switch mem.Visibility {
	case "private":
		return mem.AuthorUserID == u.UserID
	case "admin":
		return false
	}
	return true
}

// buildMemFilterQuery rebuilds the current filter as a URL query so the detail
// page can link back to the list with state preserved.
func buildMemFilterQuery(project, memType string, strengthMin float64, q, workItemID string, limit int) string {
	v := url.Values{}
	if project != "" {
		v.Set("project", project)
	}
	if memType != "" {
		v.Set("type", memType)
	}
	v.Set("strength_min", strconv.FormatFloat(strengthMin, 'f', -1, 64))
	if q != "" {
		v.Set("q", q)
	}
	if workItemID != "" {
		v.Set("wi", workItemID)
	}
	if limit > 0 && limit != 50 {
		v.Set("limit", strconv.Itoa(limit))
	}
	return v.Encode()
}

// parseMemRelatedRefs parses mem.Attrs["related_ids"] (a JSON string array
// written by pf_remember) into []MemRelatedRef for the memory detail template.
// JSON parsing is handled by parseRelatedIDs (ui_embed.go); kept separate from
// parseRelatedRefs in routes_artifacts.go so the server package doesn't import
// render for a pure view-data concern.
//
// TODO(aihub#112 Stream A): replace attrs.related_ids source with join-table-
// enriched Related[] including type and summary.
func parseMemRelatedRefs(attrs json.RawMessage) []MemRelatedRef {
	ids := parseRelatedIDs(attrs)
	if len(ids) == 0 {
		return nil
	}
	refs := make([]MemRelatedRef, 0, len(ids))
	for _, id := range ids {
		refs = append(refs, MemRelatedRef{ID: id})
	}
	return refs
}

// commitMemoryProjectFn fetches (project, status) for a memory without filtering
// out redacted rows — allowing the caller to do a project access-check before
// CommitMemory's own redacted guard fires. Swappable in tests.
var commitMemoryProjectFn = func(ctx context.Context, pool *pgxpool.Pool, memID string) (project, status string, err error) {
	err = pool.QueryRow(ctx, `SELECT project, status FROM memories WHERE id=$1`, memID).
		Scan(&project, &status)
	return
}

// doCommitMemoryFn wraps domain.CommitMemory; swappable in tests.
// The anchor param is passed through for artifact-scoped commits (aihub#124/125).
// The plain memory commit path passes zero CommitAnchorArgs (no anchor).
var doCommitMemoryFn = func(ctx context.Context, pool *pgxpool.Pool, memID, body, callerUserID, callerDisplay string, anchor domain.CommitAnchorArgs) error {
	return domain.CommitMemory(ctx, pool, memID, body, callerUserID, callerDisplay, anchor)
}

// doEditCommitFn / doDeleteCommitFn — same pattern as doCommitMemoryFn,
// swappable for testing. The domain functions handle author-or-admin checks
// internally; the handlers only enforce the project-writer gate.
var doEditCommitFn = func(ctx context.Context, pool *pgxpool.Pool, memID, commitID, body, callerUserID, callerDisplay, callerRole string) error {
	return domain.EditCommit(ctx, pool, memID, commitID, body, callerUserID, callerDisplay, callerRole)
}
var doDeleteCommitFn = func(ctx context.Context, pool *pgxpool.Pool, memID, commitID, callerUserID, callerDisplay, callerRole string) error {
	return domain.DeleteCommit(ctx, pool, memID, commitID, callerUserID, callerDisplay, callerRole)
}

// doReplyCommitFn wraps domain.ReplyCommit; swappable in tests (aihub#125).
var doReplyCommitFn = func(ctx context.Context, pool *pgxpool.Pool, memID, commitID, authorUserID, authorDisplay, body string) error {
	return domain.ReplyCommit(ctx, pool, memID, commitID, authorUserID, authorDisplay, body)
}

// doResolveCommitFn wraps domain.ResolveCommit; swappable in tests (aihub#125).
var doResolveCommitFn = func(ctx context.Context, pool *pgxpool.Pool, memID, commitID, reply, callerUserID, callerDisplay string) error {
	return domain.ResolveCommit(ctx, pool, memID, commitID, reply, callerUserID, callerDisplay)
}

// doActivateFn wraps domain.Activate; swappable in tests so handleActivateMemory's
// project-access gate can be unit-tested without a DB (aihub#146).
var doActivateFn = func(ctx context.Context, pool *pgxpool.Pool, memID, callerUserID, callerDisplay string) (*domain.ActivateResponse, error) {
	return domain.Activate(ctx, pool, memID, callerUserID, callerDisplay)
}

// handleUICommitMemory handles POST /ui/memories/:id/commit.
//
// Appends a human annotation to the memory's commits JSONB column.
// Access: must be a logged-in writer on the memory's project.
// Auth note: no CSRF token — relies on same-site=Lax session cookie (htmx
// first write operation; full CSRF review deferred to a follow-up wi).
func handleUICommitMemory(pool *pgxpool.Pool) echo.HandlerFunc {
	return func(c echo.Context) error {
		u := GetUser(c)
		if u == nil {
			return redirectToLogin(c)
		}

		memID := c.Param("id")
		if memID == "" {
			return writeError(c, domain.NewErr(domain.ErrBadRequest, "memory id is required"))
		}

		body := c.FormValue("body")
		if body == "" {
			return writeError(c, domain.NewErr(domain.ErrBadRequest, "body is required"))
		}

		ctx, cancel := contextWithTimeout(c)
		defer cancel()

		// Load (project, status) without filtering redacted so we can do the
		// access check before CommitMemory's own redacted guard fires.
		project, _, loadErr := commitMemoryProjectFn(ctx, pool, memID)
		if loadErr != nil {
			return writeError(c, domain.NewErr(domain.ErrNotFound, "memory not found"))
		}

		// C1: require writer access before mutating.
		if err := checkProjectAccess(c, u, project, "writer"); err != nil {
			return err
		}

		if err := doCommitMemoryFn(ctx, pool, memID, body, u.UserID, u.DisplayName, domain.CommitAnchorArgs{}); err != nil {
			return domainErr(c, err)
		}

		return c.Redirect(http.StatusSeeOther, "/ui/memories/"+memID)
	}
}

// handleUIEditCommit handles POST /ui/memories/:id/commit/:commit_id/edit.
//
// Replaces the body of a single commit. Access: project writer (handler) +
// commit author OR global admin (domain). Empty body is rejected with 400.
func handleUIEditCommit(pool *pgxpool.Pool) echo.HandlerFunc {
	return func(c echo.Context) error {
		u := GetUser(c)
		if u == nil {
			return redirectToLogin(c)
		}
		memID := c.Param("id")
		commitID := c.Param("commit_id")
		if memID == "" || commitID == "" {
			return writeError(c, domain.NewErr(domain.ErrBadRequest, "memory id and commit id are required"))
		}
		body := c.FormValue("body")
		if body == "" {
			return writeError(c, domain.NewErr(domain.ErrBadRequest, "body is required"))
		}

		ctx, cancel := contextWithTimeout(c)
		defer cancel()

		project, _, loadErr := commitMemoryProjectFn(ctx, pool, memID)
		if loadErr != nil {
			return writeError(c, domain.NewErr(domain.ErrNotFound, "memory not found"))
		}
		if err := checkProjectAccess(c, u, project, "writer"); err != nil {
			return err
		}
		if err := doEditCommitFn(ctx, pool, memID, commitID, body, u.UserID, u.DisplayName, u.Role); err != nil {
			return domainErr(c, err)
		}
		return c.Redirect(http.StatusSeeOther, "/ui/memories/"+memID)
	}
}

// handleUIDeleteCommit handles POST /ui/memories/:id/commit/:commit_id/delete.
//
// Hard-deletes a single commit. Access: project writer (handler) + commit
// author OR global admin (domain).
func handleUIDeleteCommit(pool *pgxpool.Pool) echo.HandlerFunc {
	return func(c echo.Context) error {
		u := GetUser(c)
		if u == nil {
			return redirectToLogin(c)
		}
		memID := c.Param("id")
		commitID := c.Param("commit_id")
		if memID == "" || commitID == "" {
			return writeError(c, domain.NewErr(domain.ErrBadRequest, "memory id and commit id are required"))
		}

		ctx, cancel := contextWithTimeout(c)
		defer cancel()

		project, _, loadErr := commitMemoryProjectFn(ctx, pool, memID)
		if loadErr != nil {
			return writeError(c, domain.NewErr(domain.ErrNotFound, "memory not found"))
		}
		if err := checkProjectAccess(c, u, project, "writer"); err != nil {
			return err
		}
		if err := doDeleteCommitFn(ctx, pool, memID, commitID, u.UserID, u.DisplayName, u.Role); err != nil {
			return domainErr(c, err)
		}
		return c.Redirect(http.StatusSeeOther, "/ui/memories/"+memID)
	}
}

// handleUIReplyCommit handles POST /ui/memories/:id/commit/:commit_id/reply.
//
// Appends a threaded reply to a commit. Access: project writer.
// Form field: body (required, non-empty). 303 redirect back to the memory detail page.
func handleUIReplyCommit(pool *pgxpool.Pool) echo.HandlerFunc {
	return func(c echo.Context) error {
		u := GetUser(c)
		if u == nil {
			return redirectToLogin(c)
		}
		memID := c.Param("id")
		commitID := c.Param("commit_id")
		if memID == "" || commitID == "" {
			return writeError(c, domain.NewErr(domain.ErrBadRequest, "memory id and commit id are required"))
		}
		body := c.FormValue("body")
		if body == "" {
			return writeError(c, domain.NewErr(domain.ErrBadRequest, "body is required"))
		}

		ctx, cancel := contextWithTimeout(c)
		defer cancel()

		project, _, loadErr := commitMemoryProjectFn(ctx, pool, memID)
		if loadErr != nil {
			return writeError(c, domain.NewErr(domain.ErrNotFound, "memory not found"))
		}
		if err := checkProjectAccess(c, u, project, "writer"); err != nil {
			return err
		}
		if err := doReplyCommitFn(ctx, pool, memID, commitID, u.UserID, u.DisplayName, body); err != nil {
			return domainErr(c, err)
		}
		return c.Redirect(http.StatusSeeOther, "/ui/memories/"+memID)
	}
}

// handleUIResolveCommit handles POST /ui/memories/:id/commit/:commit_id/resolve.
//
// Marks a commit as resolved with an optional reply. Access: project writer.
// Form field: reply (optional free text). 303 redirect back to the memory detail page.
func handleUIResolveCommit(pool *pgxpool.Pool) echo.HandlerFunc {
	return func(c echo.Context) error {
		u := GetUser(c)
		if u == nil {
			return redirectToLogin(c)
		}
		memID := c.Param("id")
		commitID := c.Param("commit_id")
		if memID == "" || commitID == "" {
			return writeError(c, domain.NewErr(domain.ErrBadRequest, "memory id and commit id are required"))
		}

		ctx, cancel := contextWithTimeout(c)
		defer cancel()

		project, _, loadErr := commitMemoryProjectFn(ctx, pool, memID)
		if loadErr != nil {
			return writeError(c, domain.NewErr(domain.ErrNotFound, "memory not found"))
		}
		if err := checkProjectAccess(c, u, project, "writer"); err != nil {
			return err
		}
		reply := c.FormValue("reply")
		if err := doResolveCommitFn(ctx, pool, memID, commitID, reply, u.UserID, u.DisplayName); err != nil {
			return domainErr(c, err)
		}
		return c.Redirect(http.StatusSeeOther, "/ui/memories/"+memID)
	}
}

// looksLikeMarkdown is a very rough heuristic: if the content starts with a
// heading / list / code fence marker we render through goldmark; otherwise we
// fall back to a <pre> block to avoid corrupting raw logs or JSON payloads.
//
// The prefix test alone is not enough for diagrams (aihub#231): an architecture
// note that opens with a sentence of prose and only then draws a ```d2 block
// would take the raw <pre> branch, so the diagram never reaches the md ->
// RenderDiagramsForUI path and the reader still sees d2 source. A d2 fence
// anywhere in the body is therefore treated as markdown on its own. The check
// stays deliberately narrow -- a d2 fence specifically, anchored to a line
// start -- so raw logs and JSON payloads keep their <pre> treatment unless they
// really do contain a fenced d2 diagram.
func looksLikeMarkdown(s string) bool {
	t := strings.TrimLeft(s, " \t\r\n")
	if t == "" {
		return false
	}
	for _, p := range []string{"# ", "## ", "### ", "- ", "* ", "> ", "```", "1. ", "|"} {
		if strings.HasPrefix(t, p) {
			return true
		}
	}
	return containsD2Fence(t)
}

// containsD2Fence reports whether any line opens a ```d2 fenced block. Matching
// at a line start avoids firing on prose that merely mentions "```d2" mid-
// sentence.
func containsD2Fence(s string) bool {
	for _, line := range strings.Split(s, "\n") {
		trimmed := strings.TrimLeft(line, " \t")
		if !strings.HasPrefix(trimmed, "```") {
			continue
		}
		info := strings.TrimSpace(strings.TrimPrefix(trimmed, "```"))
		if strings.EqualFold(info, "d2") {
			return true
		}
	}
	return false
}

// agentAuthoredHTML returns mem's rendered_html when the AGENT wrote it, or "" when it did not.
//
// aihub#240: the twin-pair architecture gives one memory two authored halves — the markdown
// (Content) and the finished page (rendered_html). Both now render on this page, one at a time.
// The predecessor of this function returned a link to the artifact viewer instead; the viewer
// still exists and still serves the same bytes at /ui/artifacts/<id>/html, but it is no longer
// the only way to see the html half, and it is no longer where the reader is sent by default.
//
// The gate matches routes_artifacts.go's sandboxBody. fact.architecture is absent from
// renderTypes, so resolveRenderedHTML never auto-fills it — a non-NULL value on this type came
// from the author. For an auto-rendered type the two halves are the same content by two routes,
// and a switch implying a second authored artifact would be a lie.
func agentAuthoredHTML(mem *domain.Memory) string {
	if mem == nil || mem.RenderedHTML == nil || mem.Type != "fact.architecture" {
		return ""
	}
	return *mem.RenderedHTML
}
