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

// CommitEntry is one human annotation stored in the memories.commits column.
// aihub#70 v3: ID is required (backfilled by 0022); UpdatedAt is present only
// after an edit. The template surfaces Edit/Delete affordances when the
// current user is the entry's author or has admin role.
type CommitEntry struct {
	ID            string `json:"id"`
	AuthorUserID  string `json:"author_user_id"`
	AuthorDisplay string `json:"author_display"`
	Body          string `json:"body"`
	CreatedAt     string `json:"created_at"`
	UpdatedAt     string `json:"updated_at,omitempty"`
}

// memListPageData drives memories.html.tmpl.
type memListPageData struct {
	Title             string
	Active            string
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
	Limit       int
	// Results.
	Items       []domain.MemoryWithStrength
	HiddenCount int
	// For the link back / pagination preservation.
	FilterQuery string
	ErrMessage  string
}

// memDetailPageData drives memory_detail.html.tmpl.
type memDetailPageData struct {
	Title      string
	Active     string
	User       *UserContext
	Memory     *domain.Memory
	BackQuery  string
	RenderAsMD bool
	Commits    []CommitEntry
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
			User:              u,
			Project:           project,
			ProjectsAvailable: projects,
			Type:              c.QueryParam("type"),
			TypeOptions:       typeOptions,
			Query:             c.QueryParam("q"),
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
		data.FilterQuery = buildMemFilterQuery(project, data.Type, data.StrengthMin, data.Query, data.Limit)

		// Access gates.
		if u.Role != "admin" && len(u.ProjectRoles) == 0 {
			data.NoAccess = true
			return renderTemplate(c, tmpl, "layout", data)
		}
		if project == "" {
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

		// Spec / plan: hand off to the artifact viewer that already wraps the
		// cached rendered_html in a chroma-styled document. If rendered_html is
		// missing (legacy row), the artifact endpoint will return a clear 404 —
		// preferable to re-rendering markdown a second time here.
		if mem.Type == "methodology.spec" || mem.Type == "methodology.plan" {
			return c.Redirect(http.StatusFound, "/ui/artifacts/"+mem.ID+"/html")
		}

		// Parse commits from the JSONB column; failures yield an empty slice
		// so they never block page rendering.
		var commits []CommitEntry
		if len(mem.Commits) > 0 {
			_ = json.Unmarshal(mem.Commits, &commits)
		}

		data := memDetailPageData{
			Title:      "Memory " + mem.ID,
			Active:     "memories",
			User:       u,
			Memory:     mem,
			BackQuery:  c.QueryParam("back"),
			RenderAsMD: looksLikeMarkdown(mem.Content),
			Commits:    commits,
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
func buildMemFilterQuery(project, memType string, strengthMin float64, q string, limit int) string {
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
	if limit > 0 && limit != 50 {
		v.Set("limit", strconv.Itoa(limit))
	}
	return v.Encode()
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
var doCommitMemoryFn = func(ctx context.Context, pool *pgxpool.Pool, memID, body, callerUserID, callerDisplay string) error {
	return domain.CommitMemory(ctx, pool, memID, body, callerUserID, callerDisplay)
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

		if err := doCommitMemoryFn(ctx, pool, memID, body, u.UserID, u.DisplayName); err != nil {
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

// looksLikeMarkdown is a very rough heuristic: if the content starts with a
// heading / list / code fence marker we render through goldmark; otherwise we
// fall back to a <pre> block to avoid corrupting raw logs or JSON payloads.
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
	return false
}
