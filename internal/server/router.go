package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/labstack/echo/v4"

	"github.com/GMISWE/ieops-aihub/internal/auth"
	"github.com/GMISWE/ieops-aihub/internal/domain"
	"github.com/GMISWE/ieops-aihub/internal/version"
)

// NewRouter constructs the echo router with all routes.
//
// uiCookieSecret seeds the HMAC for /ui/* session cookies (see ui_session.go).
// Pass at least 32 bytes for production; main.go reads POLYFORGE_UI_COOKIE_SECRET
// or generates an ephemeral secret with a warning when unset.
func NewRouter(pool *pgxpool.Pool, uiCookieSecret []byte) *echo.Echo {
	e := echo.New()
	e.HideBanner = true
	e.HidePort = true

	e.Use(RequestID())
	e.Use(Recovery())

	// Unauthenticated
	e.GET("/v1/health", handleHealth(pool))
	e.GET("/v1/version", handleVersion())
	// Bootstrap: creates the first admin user when users table is empty.
	// Protected by ADMIN_BOOTSTRAP_KEY env var; disabled when key is unset.
	e.POST("/v1/bootstrap", handleBootstrap(pool))

	// aihub#96: unauthenticated public artifact share — memory_id is the unguessable link.
	e.GET("/share/:id", handleSharedArtifact(pool))

	// Authenticated routes
	v1 := e.Group("/v1", BearerAuth(pool), IdempotencyMiddleware())

	// Identity (pf_whoami)
	v1.GET("/users/me", handleWhoami())

	// Work items
	v1.POST("/work_items", handleCreateWorkItem(pool))
	v1.GET("/work_items/ready", handleGetReadyQueue(pool)) // must come before :id
	v1.GET("/work_items", handleListWorkItems(pool))
	v1.GET("/work_items/:id", handleGetWorkItem(pool))
	v1.PATCH("/work_items/:id", handleUpdateWorkItem(pool))
	v1.POST("/work_items/:id/cancel", handleCancelWorkItem(pool))
	v1.POST("/work_items/:id/claim", handleClaimWorkItem(pool))
	v1.POST("/work_items/:id/complete", handleCompleteAttempt(pool))
	v1.POST("/work_items/:id/force_takeover", handleForceTakeover(pool))
	v1.POST("/work_items/:id/unblock", handleUnblockWorkItem(pool), RequireAdmin())

	// Dependencies (path matches client: /v1/work_items/:id/dependencies)
	v1.POST("/work_items/:id/dependencies", handleCreateDependency(pool))
	v1.GET("/work_items/:id/dependencies", handleListDependencies(pool))
	v1.DELETE("/work_items/:blocked_id/dependencies/:blocking_id/:kind", handleDeleteDependency(pool))

	// Conflicts
	v1.POST("/conflicts/predict", handlePredictConflicts(pool))

	// Admin users
	admin := v1.Group("/admin", RequireAdmin())
	admin.POST("/users", handleCreateUser(pool))
	admin.GET("/users", handleListUsers(pool))
	admin.PATCH("/users/:id", handleUpdateUser(pool))
	admin.POST("/users/:id/keys", handleCreateAPIKey(pool))
	admin.DELETE("/users/:id/keys/:key_id", handleRevokeAPIKey(pool))

	// Round 2b: memories, events, scenario configs, GC
	RegisterMemoryRoutes(v1, pool)

	// Round 2 fix: step state, release stubs, attempt lifecycle
	RegisterStepRoutes(v1, pool)

	// Projects CRUD + identifier rotation + owner transfer
	RegisterProjectRoutes(v1, pool)

	// aihub#27 / IEBE-1694: spec/plan artifact HTML viewer
	RegisterArtifactRoutes(v1, pool)

	// aihub#59 / IEBE-1696: read-only Web UI
	RegisterUIRoutes(e, pool, uiCookieSecret)

	return e
}

// handleHealth returns server health status.
func handleHealth(pool *pgxpool.Pool) echo.HandlerFunc {
	return func(c echo.Context) error {
		ctx := c.Request().Context()
		dbOK := pool.Ping(ctx) == nil
		return c.JSON(http.StatusOK, map[string]any{
			"status":  "ok",
			"version": version.Version,
			"db_ok":   dbOK,
		})
	}
}

// handleWhoami returns the caller's identity and project roles (pf_whoami).
// Path: GET /v1/users/me — required by §5.2 (Core Tools).
func handleWhoami() echo.HandlerFunc {
	return func(c echo.Context) error {
		u := GetUser(c)
		if u == nil {
			return writeError(c, domain.NewErr(domain.ErrUnauthorized, "not authenticated"))
		}
		// Build a non-empty projects list (callers expect [], not null)
		projects := make([]string, 0, len(u.ProjectRoles))
		for p := range u.ProjectRoles {
			projects = append(projects, p)
		}
		return c.JSON(http.StatusOK, map[string]any{
			"user_id":        u.UserID,
			"email":          u.Email,
			"display_name":   u.DisplayName,
			"user_type":      u.UserType,
			"role":           u.Role,
			"project_roles":  u.ProjectRoles,
			"projects":       projects,
			"api_key_id":     u.APIKeyID,
			"server_version": version.Version,
		})
	}
}

// handleVersion returns the server version and min_client_version.
func handleVersion() echo.HandlerFunc {
	return func(c echo.Context) error {
		return c.JSON(http.StatusOK, map[string]any{
			"version":            version.Version,
			"git_commit":         version.GitCommit,
			"build_time":         version.BuildTime,
			"min_client_version": "1.0.0",
		})
	}
}

// handleCreateWorkItem handles POST /v1/work_items.
func handleCreateWorkItem(pool *pgxpool.Pool) echo.HandlerFunc {
	return func(c echo.Context) error {
		u := GetUser(c)
		ctx, cancel := contextWithTimeout(c)
		defer cancel()

		var req domain.CreateWorkItemRequest
		if err := c.Bind(&req); err != nil {
			return writeError(c, domain.NewErr(domain.ErrBadRequest, "invalid request body"))
		}

		// C1: Require writer access to the target project
		if err := checkProjectAccess(c, u, req.Project, "writer"); err != nil {
			return err
		}

		wi, aihubErr := domain.CreateWorkItem(ctx, pool, &req, u.UserID, u.DisplayName)
		if aihubErr != nil {
			return writeError(c, aihubErr)
		}
		return c.JSON(http.StatusCreated, wi)
	}
}

// parseListWIBool reads a boolean query param for GET /v1/work_items. It returns
// (value, true) for an absent param or a recognised spelling, and (false, false)
// for anything else so the caller can reject it.
//
// strconv.ParseBool's set is used rather than a hand-rolled `== "true"` so the
// spellings a caller is likely to try (True, TRUE, T, 1, 0, f) are all either
// honoured or refused — never silently read as false (aihub#280).
func parseListWIBool(c echo.Context, name string) (value, ok bool) {
	raw := strings.TrimSpace(c.QueryParam(name))
	if raw == "" {
		return false, true
	}
	b, err := strconv.ParseBool(raw)
	if err != nil {
		return false, false
	}
	return b, true
}

// trimmedParam reads a query param with surrounding whitespace removed.
//
// Every scalar filter on GET /v1/work_items goes through this, because a
// half-applied rule is worse than none: `?milestone=%20v2` matching nothing
// while `?wi_type=%20fix_bug` works is a difference no caller can see or
// predict (aihub#280).
func trimmedParam(c echo.Context, name string) string {
	return strings.TrimSpace(c.QueryParam(name))
}

// splitCSVParam splits a comma-separated query param, trimming each entry and
// dropping empties.
//
// Trimming is load-bearing, not tidiness: `ids=wi_a, wi_b` is the form a human
// or an agent writes, and an untrimmed " wi_b" matches no row while looking like
// a working filter — the same silent-miss this wi exists to close. /ui already
// trims its equivalents (ui_handlers_wi.go); this endpoint did not (aihub#280).
func splitCSVParam(raw string) []string {
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// handleListWorkItems handles GET /v1/work_items.
func handleListWorkItems(pool *pgxpool.Pool) echo.HandlerFunc {
	return func(c echo.Context) error {
		u := GetUser(c)
		ctx, cancel := contextWithTimeout(c)
		defer cancel()

		project := c.QueryParam("project")
		ids := splitCSVParam(c.QueryParam("ids"))

		filter := domain.ListWorkItemsFilter{
			Limit: 50,
		}

		// aihub#280: an ids= lookup no longer requires project=.
		//
		// pf-status, pf-retro and pf-execute all open with
		// pf_list_work_items(ids=[<current_wi_id>]) and pass no project — a wi id
		// already names exactly one wi, so there is nothing for the caller to
		// supply. That request used to 400, meaning those three skills' first
		// call had almost certainly never once succeeded. Requiring the caller to
		// restate a project the id already determines is the wrong end to fix.
		//
		// Access control does not weaken: with no project to check a role
		// against, the query is bounded to the projects this caller can see via
		// AccessibleProjects (the same mechanism the /ui view-all option uses),
		// so an id outside that set simply returns no rows.
		if project == "" {
			// len(ids)==0 rather than the raw param being empty: `ids=,` and
			// `ids=%20` are non-empty strings that carry no id, and treating
			// those as "an ids lookup" would drop the project requirement and
			// then list every accessible project in full.
			if len(ids) == 0 {
				return writeError(c, domain.NewErr(domain.ErrBadRequest,
					"project query parameter is required (or pass ids= to look up work items by id)"))
			}
			if u == nil {
				return writeError(c, domain.NewErr(domain.ErrUnauthorized, "not authenticated"))
			}
			switch {
			case u.ProjectScope != nil:
				// ProjectScope is a *confinement* on the API key, never a grant.
				// BearerAuth already intersects memberships with it
				// (middleware.go), so a non-admin whose key is scoped to a
				// project they are not a member of arrives here with an EMPTY
				// ProjectRoles. Scoping to *u.ProjectScope unconditionally would
				// hand that caller read access that `?project=X` still answers
				// 403 for — and would mean removing someone from a project no
				// longer revokes their reads. Admins legitimately have an empty
				// ProjectRoles (the membership query is skipped for them), so
				// they are the one exemption.
				// Compared through roleLevel, not tested for non-emptiness:
				// checkProjectAccess gates on roleLevel[role] >= roleLevel[min],
				// and roleLevel maps an unrecognised string to 0. Testing for a
				// non-empty role would therefore admit a legacy value that
				// ?project= rejects. SetProjectMembers validates to the three
				// known roles today, but migration 0013_backfill_projects.sql
				// copied arbitrary users.project_roles values in, mapping only
				// maintainer→writer — so such rows may exist. Going through the
				// same lookup removes the class without needing to know.
				if u.Role != "admin" && roleLevel[u.ProjectRoles[*u.ProjectScope]] < roleLevel["viewer"] {
					return writeError(c, domain.NewErr(domain.ErrForbidden,
						fmt.Sprintf("no access to project %q", *u.ProjectScope)))
				}
				filter.AccessibleProjects = []string{*u.ProjectScope}
			case u.Role == "admin":
				// Empty AccessibleProjects + empty project = no project clause,
				// i.e. every project. Matches the admin "view all" contract
				// documented on ListWorkItems.
			default:
				projects := make([]string, 0, len(u.ProjectRoles))
				for p, role := range u.ProjectRoles {
					// Through roleLevel for the same reason as the scoped branch
					// above: an unrecognised legacy role is level 0, and must not
					// grant here what ?project= denies.
					if roleLevel[role] >= roleLevel["viewer"] {
						projects = append(projects, p)
					}
				}
				if len(projects) == 0 {
					return writeError(c, domain.NewErr(domain.ErrForbidden,
						"no accessible projects; pass project= explicitly"))
				}
				// Sorted so the bound arg is deterministic (map order is not).
				sort.Strings(projects)
				filter.AccessibleProjects = projects
			}
		} else {
			// C1: Require at least viewer access to the project
			if err := checkProjectAccess(c, u, project, "viewer"); err != nil {
				return err
			}
		}

		if status := c.QueryParam("status"); status != "" {
			filter.Status = splitCSVParam(status) // supports "running,paused,queued"
		}
		// `kind` is a deprecated spelling of `wi_type`. It is kept rather than
		// removed because the MCP schema published it first and /ui still reads
		// `kind` (and only `kind`) for what it folds onto filter.WIType
		// (ui_handlers_wi.go) — so "kind means wi_type" is already this codebase's
		// convention. An explicit wi_type wins; there is deliberately no third
		// spelling and no separate `kind` column (aihub#280).
		wiType := trimmedParam(c, "wi_type")
		if wiType == "" {
			wiType = trimmedParam(c, "kind")
		}
		if wiType != "" {
			filter.WIType = &wiType
		}
		// EVERY scalar filter goes through trimmedParam, not just some.
		//
		// An earlier revision trimmed wi_type/kind and left the other six raw,
		// while carrying a comment arguing that trimming is load-bearing. It is:
		// `?milestone=%20v2` returned HTTP 200 with items: [], indistinguishable
		// from "no work items on that milestone" — the same silent miss this wi
		// exists to close, reintroduced in the fix for it. Assigning through a
		// table rather than eight hand-written ifs is what stops the next added
		// param from being the one that gets forgotten (aihub#280).
		for _, p := range []struct {
			name string
			dest **string
		}{
			{"priority", &filter.Priority},
			{"milestone", &filter.Milestone},
			{"scenario", &filter.Scenario},
			{"label", &filter.Label},
			{"user_id", &filter.UserID},
			{"source", &filter.Source},
		} {
			if v := trimmedParam(c, p.name); v != "" {
				value := v
				*p.dest = &value
			}
		}
		if len(ids) > 0 {
			filter.IDs = ids
		}
		// since: parsed here rather than passed through, because filter.Since is
		// a time.Time. An unparseable value is rejected loudly instead of being
		// dropped (aihub#280).
		//
		// 🔴 `since` filters wi.created_at, NOT closed_at. It answers "created at
		// or after T", so `status=wrapped&since=T` is NOT "wrapped since T" — a
		// work item created three months ago and wrapped yesterday is excluded.
		// Do not describe this param as expressing "wrapped since the last
		// release"; that set needs closed_at (stamped by trg_wi_closed_at, and
		// already reachable via sort=closed_at) and no param exposes it yet.
		// The distinction matters in the dangerous direction: a created_at filter
		// is silently UNDER-inclusive, and a caller cannot tell a short list from
		// a complete one.
		if since := trimmedParam(c, "since"); since != "" {
			ts, parseErr := time.Parse(time.RFC3339, since)
			if parseErr != nil {
				return writeError(c, domain.NewErr(domain.ErrBadRequest,
					fmt.Sprintf("since must be an RFC3339 timestamp, got %q", since)))
			}
			filter.Since = &ts
		}
		// Booleans are rejected rather than coerced, for the same reason `since`
		// is: `ready_only=True` or `ready_only=yes` silently meaning false is the
		// defect class this whole wi is about, and it would be indistinguishable
		// from not sending the param at all (aihub#280).
		if b, ok := parseListWIBool(c, "ready_only"); !ok {
			return writeError(c, domain.NewErr(domain.ErrBadRequest,
				fmt.Sprintf("ready_only must be true or false, got %q", c.QueryParam("ready_only"))))
		} else {
			filter.ReadyOnly = b
		}
		if b, ok := parseListWIBool(c, "include_step_state"); !ok {
			return writeError(c, domain.NewErr(domain.ErrBadRequest,
				fmt.Sprintf("include_step_state must be true or false, got %q", c.QueryParam("include_step_state"))))
		} else {
			filter.IncludeStepState = b
		}
		if limit := c.QueryParam("limit"); limit != "" {
			var n int
			fmt.Sscanf(limit, "%d", &n) //nolint:errcheck // parse error -> n stays 0 -> default kicks in
			if n > 0 {
				filter.Limit = n
			}
		}
		if cursor := c.QueryParam("cursor"); cursor != "" {
			filter.Cursor = &cursor
		}
		// aihub#273: semantic search. Similarity ordering has no stable
		// pagination key and overrides sort — reject the combinations loudly
		// instead of silently ignoring a parameter (the aihub#267/#271 family).
		if q := c.QueryParam("query"); q != "" {
			if c.QueryParam("sort") != "" || c.QueryParam("order") != "" || c.QueryParam("cursor") != "" {
				return writeError(c, domain.NewErr(domain.ErrBadRequest,
					"query (semantic search) does not combine with sort, order, or cursor"))
			}
			filter.Query = &q
		}
		// sort/order (aihub#224). Both default to the historical behaviour
		// (created_at desc); an unrecognised value is rejected rather than
		// silently ignored. sort=closed_at returns only closed items, since a
		// NULL close time has no position in that ordering.
		sortBy, order, sortErr := domain.NormalizeListWorkItemsSort(
			c.QueryParam("sort"), c.QueryParam("order"))
		if sortErr != nil {
			return writeError(c, sortErr)
		}
		filter.Sort = sortBy
		filter.Order = order

		// Via the package-level seam (ui_handlers_wi.go) so the query-param →
		// filter wiring is testable without a live pool.
		result, aihubErr := listWorkItemsFn(ctx, pool, project, filter)
		if aihubErr != nil {
			return writeError(c, aihubErr)
		}
		return c.JSON(http.StatusOK, result)
	}
}

// handleGetWorkItem handles GET /v1/work_items/:id.
func handleGetWorkItem(pool *pgxpool.Pool) echo.HandlerFunc {
	return func(c echo.Context) error {
		u := GetUser(c)
		ctx, cancel := contextWithTimeout(c)
		defer cancel()

		wi, aihubErr := domain.GetWorkItem(ctx, pool, c.Param("id"))
		if aihubErr != nil {
			return writeError(c, aihubErr)
		}

		// C1: Require viewer access to the work item's project
		if err := checkProjectAccess(c, u, wi.Project, "viewer"); err != nil {
			return err
		}

		return c.JSON(http.StatusOK, wi)
	}
}

// handleUpdateWorkItem handles PATCH /v1/work_items/:id.
func handleUpdateWorkItem(pool *pgxpool.Pool) echo.HandlerFunc {
	return func(c echo.Context) error {
		u := GetUser(c)
		ctx, cancel := contextWithTimeout(c)
		defer cancel()

		var req domain.UpdateWorkItemRequest
		if err := c.Bind(&req); err != nil {
			return writeError(c, domain.NewErr(domain.ErrBadRequest, "invalid request body"))
		}

		// C1: Load wi to get project, then check writer access
		existing, aihubErr := domain.GetWorkItem(ctx, pool, c.Param("id"))
		if aihubErr != nil {
			return writeError(c, aihubErr)
		}
		if err := checkProjectAccess(c, u, existing.Project, "writer"); err != nil {
			return err
		}

		wi, aihubErr := domain.UpdateWorkItem(ctx, pool, c.Param("id"), u.UserID, u.Role, u.ProjectRoles, &req)
		if aihubErr != nil {
			return writeError(c, aihubErr)
		}
		return c.JSON(http.StatusOK, wi)
	}
}

// handleCancelWorkItem handles POST /v1/work_items/:id/cancel.
func handleCancelWorkItem(pool *pgxpool.Pool) echo.HandlerFunc {
	return func(c echo.Context) error {
		u := GetUser(c)
		ctx, cancel := contextWithTimeout(c)
		defer cancel()

		// C1: Load wi to get project; reporter needs writer, others need maintainer.
		wi, aihubErr := domain.GetWorkItem(ctx, pool, c.Param("id"))
		if aihubErr != nil {
			return writeError(c, aihubErr)
		}
		minRole := "writer"
		if wi.ReporterUserID != u.UserID {
			minRole = "maintainer"
		}
		if err := checkProjectAccess(c, u, wi.Project, minRole); err != nil {
			return err
		}

		// Pass the resolved canonical id (c.Param("id") may be a slug). (aihub#127)
		if aihubErr := domain.CancelWorkItem(ctx, pool, wi.ID, u.UserID, u.Role, u.ProjectRoles); aihubErr != nil {
			return writeError(c, aihubErr)
		}
		return c.JSON(http.StatusOK, map[string]bool{"ok": true})
	}
}

// handleClaimWorkItem handles POST /v1/work_items/:id/claim.
func handleClaimWorkItem(pool *pgxpool.Pool) echo.HandlerFunc {
	return func(c echo.Context) error {
		u := GetUser(c)
		ctx, cancel := contextWithTimeout(c)
		defer cancel()

		var req domain.ClaimRequest
		if err := c.Bind(&req); err != nil {
			return writeError(c, domain.NewErr(domain.ErrBadRequest, "invalid request body"))
		}

		// C1: Load wi to get project; require writer access; also enforce force_takeover permissions.
		wi, aihubErr := domain.GetWorkItem(ctx, pool, c.Param("id"))
		if aihubErr != nil {
			return writeError(c, aihubErr)
		}
		if err := checkProjectAccess(c, u, wi.Project, "writer"); err != nil {
			return err
		}

		// Cross-user force_takeover via the claim path requires maintainer/admin (§4.3, §9.4).
		// Self-takeover (same user_id) is implicit and only needs writer.
		if req.ForceOver && wi.CurrentAttemptID != nil {
			var currentActorUserID string
			pool.QueryRow(ctx, `SELECT actor_user_id FROM run_attempts WHERE id=$1`, *wi.CurrentAttemptID).Scan(&currentActorUserID) //nolint:errcheck
			if currentActorUserID != "" && currentActorUserID != u.UserID {
				projRole := u.ProjectRoles[wi.Project]
				if u.Role != "admin" && projRole != "maintainer" {
					return writeError(c, domain.NewErr(domain.ErrForbidden,
						"force_takeover of another user's attempt requires maintainer or admin role"))
				}
			}
		}

		resp, aihubErr := domain.FnClaimWorkItem(ctx, pool, c.Param("id"), &req, u.UserID, u.APIKeyID, u.DisplayName)
		if aihubErr != nil {
			return writeError(c, aihubErr)
		}
		return c.JSON(http.StatusOK, resp)
	}
}

// handleCompleteAttempt handles POST /v1/work_items/:id/complete.
func handleCompleteAttempt(pool *pgxpool.Pool) echo.HandlerFunc {
	return func(c echo.Context) error {
		u := GetUser(c)
		ctx, cancel := contextWithTimeout(c)
		defer cancel()

		var req domain.CompleteAttemptRequest
		if err := c.Bind(&req); err != nil {
			return writeError(c, domain.NewErr(domain.ErrBadRequest, "invalid request body"))
		}

		// C1: Load wi to get project; require writer access.
		// AttemptCredential (session_secret) provides additional per-attempt gating inside domain.
		wi, aihubErr := domain.GetWorkItem(ctx, pool, c.Param("id"))
		if aihubErr != nil {
			return writeError(c, aihubErr)
		}
		if err := checkProjectAccess(c, u, wi.Project, "writer"); err != nil {
			return err
		}

		// Pass the resolved canonical id (c.Param("id") may be a slug). (aihub#127)
		if aihubErr := domain.FnCompleteAttempt(ctx, pool, wi.ID, &req); aihubErr != nil {
			return writeError(c, aihubErr)
		}
		return c.JSON(http.StatusOK, map[string]bool{"ok": true})
	}
}

// handleForceTakeover handles POST /v1/work_items/:id/force_takeover.
func handleForceTakeover(pool *pgxpool.Pool) echo.HandlerFunc {
	return func(c echo.Context) error {
		u := GetUser(c)
		ctx, cancel := contextWithTimeout(c)
		defer cancel()

		var req domain.ForceTakeoverRequest
		if err := c.Bind(&req); err != nil {
			return writeError(c, domain.NewErr(domain.ErrBadRequest, "invalid request body"))
		}

		// C5: Load wi; same-user self-takeover needs Writer; cross-user needs Maintainer.
		wi, aihubErr := domain.GetWorkItem(ctx, pool, c.Param("id"))
		if aihubErr != nil {
			return writeError(c, aihubErr)
		}
		minRole := "maintainer"
		if wi.CurrentAttemptID != nil {
			// Check if the current attempt belongs to this user
			var actorUserID string
			pool.QueryRow(ctx, `SELECT actor_user_id FROM run_attempts WHERE id=$1`, *wi.CurrentAttemptID).Scan(&actorUserID) //nolint:errcheck
			if actorUserID == u.UserID {
				minRole = "writer" // same user, different machine → self-takeover
			}
		}
		if err := checkProjectAccess(c, u, wi.Project, minRole); err != nil {
			return err
		}

		resp, aihubErr := domain.FnForceTakeover(ctx, pool, c.Param("id"), u.UserID, u.DisplayName, u.Role, u.ProjectRoles, &req)
		if aihubErr != nil {
			return writeError(c, aihubErr)
		}
		return c.JSON(http.StatusOK, resp)
	}
}

// handleUnblockWorkItem handles POST /v1/work_items/:id/unblock (admin only).
// §4.3: body {reason} — required; emit admin_unblock event with the reason.
func handleUnblockWorkItem(pool *pgxpool.Pool) echo.HandlerFunc {
	return func(c echo.Context) error {
		ctx, cancel := contextWithTimeout(c)
		defer cancel()

		u := GetUser(c)
		wiID := c.Param("id")

		var req struct {
			Reason string `json:"reason"`
		}
		_ = c.Bind(&req) // reason is optional in v1 but recorded if present

		// Only unblock work_items that are actually in 'blocked' state; terminal/running → 409.
		var status string
		err := pool.QueryRow(ctx, `SELECT status FROM work_items WHERE id=$1`, wiID).Scan(&status)
		if err != nil {
			return writeError(c, domain.NewErr(domain.ErrNotFound, "work item not found"))
		}
		if status != "blocked" {
			return writeError(c, domain.NewErr(domain.ErrConflictTerminalState,
				fmt.Sprintf("work item is not blocked (status=%s); cannot unblock", status)))
		}

		if _, err := pool.Exec(ctx, `
			UPDATE work_items SET status='queued', updated_at=clock_timestamp()
			WHERE id=$1 AND status='blocked'`, wiID); err != nil {
			return internalError(c, "failed to unblock work item")
		}

		// H6: emit admin_unblock audit event with reason payload (best-effort).
		if u != nil {
			payload := map[string]any{"action": "unblock", "reason": req.Reason}
			payloadJSON, _ := jsonMarshal(payload)
			_, _ = pool.Exec(context.Background(), `
				INSERT INTO agent_events (id, work_item_id, actor_user_id, api_key_id, event_type, payload, project)
				VALUES ($1, $2, $3, $4, 'admin_unblock', $5::jsonb,
				    (SELECT project FROM work_items WHERE id=$2))`,
				domain.NewID("evt"), wiID, u.UserID, u.APIKeyID, string(payloadJSON))
		}
		return c.JSON(http.StatusOK, map[string]bool{"ok": true})
	}
}

// handleGetReadyQueue handles GET /v1/work_items/ready.
func handleGetReadyQueue(pool *pgxpool.Pool) echo.HandlerFunc {
	return func(c echo.Context) error {
		u := GetUser(c)
		ctx, cancel := contextWithTimeout(c)
		defer cancel()

		project := c.QueryParam("project")
		if project == "" {
			return writeError(c, domain.NewErr(domain.ErrBadRequest, "project query parameter is required"))
		}

		// C1: Require at least viewer access to the project
		if err := checkProjectAccess(c, u, project, "viewer"); err != nil {
			return err
		}

		max := 10
		if m := c.QueryParam("max"); m != "" {
			var n int
			fmt.Sscanf(m, "%d", &n) //nolint:errcheck // parse error -> n stays 0 -> default kicks in
			if n > 0 {
				max = n
			}
		}
		result, aihubErr := domain.GetReadyQueue(ctx, pool, project, max)
		if aihubErr != nil {
			return writeError(c, aihubErr)
		}
		return c.JSON(http.StatusOK, result)
	}
}

// handleCreateDependency handles POST /v1/dependencies.
func handleCreateDependency(pool *pgxpool.Pool) echo.HandlerFunc {
	return func(c echo.Context) error {
		u := GetUser(c)
		ctx, cancel := contextWithTimeout(c)
		defer cancel()

		var req domain.CreateDependencyRequest
		if err := c.Bind(&req); err != nil {
			return writeError(c, domain.NewErr(domain.ErrBadRequest, "invalid request body"))
		}
		// path /:id overrides body field when present
		if pathID := c.Param("id"); pathID != "" {
			req.BlockedWIID = pathID
		}

		// C1: Load blocked wi → require writer on its project (caller "owns" it).
		blockedWI, aihubErr := domain.GetWorkItem(ctx, pool, req.BlockedWIID)
		if aihubErr != nil {
			return writeError(c, aihubErr)
		}
		if err := checkProjectAccess(c, u, blockedWI.Project, "writer"); err != nil {
			return err
		}

		// For cross-project dependencies, also require viewer on the blocking wi's project.
		blockingWI, aihubErr := domain.GetWorkItem(ctx, pool, req.BlockingWIID)
		if aihubErr != nil {
			return writeError(c, aihubErr)
		}
		if blockingWI.Project != blockedWI.Project {
			if err := checkProjectAccess(c, u, blockingWI.Project, "viewer"); err != nil {
				return writeError(c, domain.NewErr(domain.ErrForbidden,
					"no visibility to blocking work item's project"))
			}
		}

		if aihubErr := domain.CreateDependency(ctx, pool, &req, u.UserID, u.ProjectRoles, u.Role); aihubErr != nil {
			return writeError(c, aihubErr)
		}
		return c.JSON(http.StatusCreated, map[string]bool{"ok": true})
	}
}

// handleListDependencies handles GET /v1/dependencies.
func handleListDependencies(pool *pgxpool.Pool) echo.HandlerFunc {
	return func(c echo.Context) error {
		u := GetUser(c)
		ctx, cancel := contextWithTimeout(c)
		defer cancel()

		wiID := c.Param("id")
		if wiID == "" {
			wiID = c.QueryParam("work_item_id")
		}
		if wiID == "" {
			return writeError(c, domain.NewErr(domain.ErrBadRequest, "work_item_id required"))
		}

		wi, aihubErr := domain.GetWorkItem(ctx, pool, wiID)
		if aihubErr != nil {
			return writeError(c, aihubErr)
		}
		if err := checkProjectAccess(c, u, wi.Project, "viewer"); err != nil {
			return err
		}

		resp, aihubErr := domain.ListDependencies(ctx, pool, wiID, u.ProjectRoles, u.Role)
		if aihubErr != nil {
			return writeError(c, aihubErr)
		}
		return c.JSON(http.StatusOK, resp)
	}
}

// handleDeleteDependency handles DELETE /v1/dependencies/:blocked_id/:blocking_id/:kind.
func handleDeleteDependency(pool *pgxpool.Pool) echo.HandlerFunc {
	return func(c echo.Context) error {
		u := GetUser(c)
		ctx, cancel := contextWithTimeout(c)
		defer cancel()

		// C1: Require writer access — load blocked wi to get project
		blockedWI, aihubErr := domain.GetWorkItem(ctx, pool, c.Param("blocked_id"))
		if aihubErr != nil {
			return writeError(c, aihubErr)
		}
		if err := checkProjectAccess(c, u, blockedWI.Project, "writer"); err != nil {
			return err
		}

		if aihubErr := domain.DeleteDependency(ctx, pool,
			c.Param("blocked_id"), c.Param("blocking_id"), c.Param("kind")); aihubErr != nil {
			return writeError(c, aihubErr)
		}
		return c.JSON(http.StatusOK, map[string]bool{"ok": true})
	}
}

// handlePredictConflicts handles POST /v1/conflicts/predict.
func handlePredictConflicts(pool *pgxpool.Pool) echo.HandlerFunc {
	return func(c echo.Context) error {
		u := GetUser(c)
		ctx, cancel := contextWithTimeout(c)
		defer cancel()

		var req domain.PredictConflictsRequest
		if err := c.Bind(&req); err != nil {
			return writeError(c, domain.NewErr(domain.ErrBadRequest, "invalid request body"))
		}

		resp, aihubErr := domain.PredictConflicts(ctx, pool, &req, u.ProjectRoles)
		if aihubErr != nil {
			return writeError(c, aihubErr)
		}
		return c.JSON(http.StatusOK, resp)
	}
}

// handleCreateUser handles POST /v1/admin/users.
func handleCreateUser(pool *pgxpool.Pool) echo.HandlerFunc {
	return func(c echo.Context) error {
		ctx, cancel := contextWithTimeout(c)
		defer cancel()

		var req struct {
			Email         *string  `json:"email"`
			DisplayName   string   `json:"display_name"`
			UserType      string   `json:"user_type"`
			Role          string   `json:"role"`
			AuthorAliases []string `json:"author_aliases"`
		}
		if err := c.Bind(&req); err != nil {
			return writeError(c, domain.NewErr(domain.ErrBadRequest, "invalid request body"))
		}

		if req.DisplayName == "" {
			return writeError(c, domain.NewErr(domain.ErrBadRequest, "display_name is required"))
		}

		if req.UserType == "" {
			req.UserType = "human"
		}
		if req.Role == "" {
			req.Role = "writer"
		}

		// Machine users get auto-generated email
		email := req.Email
		if req.UserType == "machine" {
			slug := slugify(req.DisplayName)
			autoEmail := "machine-" + slug + "@polyforge.internal"
			email = &autoEmail
		}
		if email == nil {
			return writeError(c, domain.NewErr(domain.ErrBadRequest, "email is required for human users"))
		}

		// author_aliases is NOT NULL in the schema — default to empty slice when not provided.
		if req.AuthorAliases == nil {
			req.AuthorAliases = []string{}
		}

		userID := domain.NewID("u")
		var id string
		err := pool.QueryRow(ctx, `
			INSERT INTO users (id, email, display_name, user_type, role, author_aliases)
			VALUES ($1, $2, $3, $4, $5, $6)
			RETURNING id`,
			userID, *email, req.DisplayName, req.UserType, req.Role, req.AuthorAliases,
		).Scan(&id)
		if err != nil {
			return internalError(c, "failed to create user")
		}

		return c.JSON(http.StatusCreated, map[string]any{
			"id":           id,
			"email":        *email,
			"display_name": req.DisplayName,
			"user_type":    req.UserType,
			"role":         req.Role,
		})
	}
}

// handleListUsers handles GET /v1/admin/users.
func handleListUsers(pool *pgxpool.Pool) echo.HandlerFunc {
	return func(c echo.Context) error {
		ctx, cancel := contextWithTimeout(c)
		defer cancel()

		rows, err := pool.Query(ctx, `
			SELECT id, email, display_name, user_type, role
			FROM users ORDER BY created_at DESC LIMIT 100`)
		if err != nil {
			return internalError(c, "failed to list users")
		}
		defer rows.Close()

		var items []map[string]any
		for rows.Next() {
			var id, email, displayName, userType, role string
			if err := rows.Scan(&id, &email, &displayName, &userType, &role); err != nil {
				continue
			}
			items = append(items, map[string]any{
				"id":           id,
				"email":        email,
				"display_name": displayName,
				"user_type":    userType,
				"role":         role,
			})
		}
		if items == nil {
			items = []map[string]any{}
		}
		return c.JSON(http.StatusOK, map[string]any{"items": items})
	}
}

// handleUpdateUser handles PATCH /v1/admin/users/:id.
func handleUpdateUser(pool *pgxpool.Pool) echo.HandlerFunc {
	return func(c echo.Context) error {
		ctx, cancel := contextWithTimeout(c)
		defer cancel()

		var req struct {
			DisplayName   *string  `json:"display_name"`
			Role          *string  `json:"role"`
			AuthorAliases []string `json:"author_aliases"`
		}
		if err := c.Bind(&req); err != nil {
			return writeError(c, domain.NewErr(domain.ErrBadRequest, "invalid request body"))
		}

		sets := []string{}
		args := []any{}
		idx := 1

		if req.DisplayName != nil {
			sets = append(sets, "display_name=$"+itoa(idx))
			args = append(args, *req.DisplayName)
			idx++
		}
		if req.Role != nil {
			if *req.Role != "writer" && *req.Role != "admin" {
				return writeError(c, domain.NewErr(domain.ErrBadRequest, "role must be writer or admin"))
			}
			sets = append(sets, "role=$"+itoa(idx))
			args = append(args, *req.Role)
			idx++
		}
		if req.AuthorAliases != nil {
			sets = append(sets, "author_aliases=$"+itoa(idx))
			args = append(args, req.AuthorAliases)
			idx++
		}

		if len(sets) == 0 {
			return writeError(c, domain.NewErr(domain.ErrBadRequest, "no fields to update"))
		}

		args = append(args, c.Param("id"))
		query := "UPDATE users SET " + joinComma(sets) + " WHERE id=$" + itoa(idx)
		_, err := pool.Exec(ctx, query, args...)
		if err != nil {
			return internalError(c, "failed to update user")
		}

		return c.JSON(http.StatusOK, map[string]bool{"ok": true})
	}
}

// handleCreateAPIKey handles POST /v1/admin/users/:id/keys.
func handleCreateAPIKey(pool *pgxpool.Pool) echo.HandlerFunc {
	return func(c echo.Context) error {
		ctx, cancel := contextWithTimeout(c)
		defer cancel()

		var req struct {
			Name         string  `json:"name"`
			ProjectScope *string `json:"project_scope"`
		}
		if err := c.Bind(&req); err != nil {
			return writeError(c, domain.NewErr(domain.ErrBadRequest, "invalid request body"))
		}
		if req.Name == "" {
			return writeError(c, domain.NewErr(domain.ErrBadRequest, "name is required"))
		}

		// Generate raw key: "pf_k1_" + 32 base62 chars
		rawKey := "pf_k1_" + domain.NewBase62(32)
		keyHash := domain.HashSecret(rawKey) // reusing sha256 hash
		keyID := "k" + domain.NewBase62(8)

		newKey := map[string]any{
			"id":       keyID,
			"key_hash": keyHash,
			"name":     req.Name,
		}
		if req.ProjectScope != nil {
			newKey["project_scope"] = *req.ProjectScope
		}

		newKeyJSON := must(marshalJSON(newKey))

		tag, err := pool.Exec(ctx, `
			UPDATE users SET api_keys = api_keys || $1::jsonb
			WHERE id=$2`,
			"["+string(newKeyJSON)+"]", c.Param("id"),
		)
		if err != nil {
			return internalError(c, "failed to add API key")
		}
		if tag.RowsAffected() == 0 {
			return writeError(c, domain.NewErr(domain.ErrNotFound, "user not found"))
		}

		return c.JSON(http.StatusCreated, map[string]any{
			"key_id":  keyID,
			"raw_key": rawKey,
		})
	}
}

// handleRevokeAPIKey handles DELETE /v1/admin/users/:id/keys/:key_id.
func handleRevokeAPIKey(pool *pgxpool.Pool) echo.HandlerFunc {
	return func(c echo.Context) error {
		ctx, cancel := contextWithTimeout(c)
		defer cancel()

		userID := c.Param("id")
		keyID := c.Param("key_id")

		// Soft delete: set revoked_at in the JSONB array element
		_, err := pool.Exec(ctx, `
			UPDATE users
			SET api_keys = (
			  SELECT jsonb_agg(
			    CASE WHEN k->>'id' = $2
			         THEN k || jsonb_build_object('revoked_at', now()::text)
			         ELSE k
			    END
			  )
			  FROM jsonb_array_elements(api_keys) AS k
			)
			WHERE id=$1`,
			userID, keyID,
		)
		if err != nil {
			return internalError(c, "failed to revoke API key")
		}

		return c.JSON(http.StatusOK, map[string]bool{"ok": true})
	}
}

// Helpers

func slugify(s string) string {
	out := make([]byte, 0, len(s))
	for _, c := range strings.ToLower(s) {
		if (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') {
			out = append(out, byte(c))
		} else if c == ' ' || c == '_' || c == '-' {
			out = append(out, '-')
		}
	}
	return string(out)
}

func itoa(n int) string {
	return intToString(n)
}

func intToString(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	pos := len(buf)
	for n > 0 {
		pos--
		buf[pos] = byte(n%10) + '0'
		n /= 10
	}
	if neg {
		pos--
		buf[pos] = '-'
	}
	return string(buf[pos:])
}

func joinComma(ss []string) string {
	return strings.Join(ss, ", ")
}

func marshalJSON(v any) ([]byte, error) {
	return jsonMarshal(v)
}

func must(b []byte, err error) []byte {
	if err != nil {
		return []byte("{}")
	}
	return b
}

// handleBootstrap creates the first admin user when the users table is empty.
// Requires ADMIN_BOOTSTRAP_KEY env var to match the X-Bootstrap-Key header.
// Disabled (405) when the env var is not set.
func handleBootstrap(pool *pgxpool.Pool) echo.HandlerFunc {
	return func(c echo.Context) error {
		bootstrapKey := os.Getenv("ADMIN_BOOTSTRAP_KEY")
		if bootstrapKey == "" {
			return writeError(c, domain.NewErr(domain.ErrNotImplemented, "bootstrap is disabled (ADMIN_BOOTSTRAP_KEY not set)"))
		}
		if c.Request().Header.Get("X-Bootstrap-Key") != bootstrapKey {
			return writeError(c, domain.NewErr(domain.ErrForbidden, "invalid bootstrap key"))
		}

		ctx, cancel := contextWithTimeout(c)
		defer cancel()

		// Guard: only allowed when no users exist yet
		var count int
		if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM users`).Scan(&count); err != nil {
			return internalError(c, "failed to check users table")
		}
		if count > 0 {
			return writeError(c, domain.NewErr(domain.ErrForbidden, "bootstrap already done — users table is non-empty"))
		}

		var req struct {
			Email       string `json:"email"`
			DisplayName string `json:"display_name"`
			APIKeyName  string `json:"api_key_name"`
		}
		if err := c.Bind(&req); err != nil {
			return writeError(c, domain.NewErr(domain.ErrBadRequest, err.Error()))
		}
		if req.Email == "" || req.DisplayName == "" {
			return writeError(c, domain.NewErr(domain.ErrBadRequest, "email and display_name required"))
		}

		// Generate API key
		rawKey := domain.NewBase62(32) // 32-char base62 key
		keyID := "k_" + domain.NewBase62(8)
		keyHash := auth.HashKey(rawKey)
		apiKeysJSON, _ := json.Marshal([]map[string]any{{
			"id":         keyID,
			"key_hash":   keyHash,
			"name":       req.APIKeyName,
			"created_at": "now",
		}})

		userID := domain.NewID("u")
		if _, err := pool.Exec(ctx, `
			INSERT INTO users (id, email, display_name, user_type, role, api_keys, author_aliases)
			VALUES ($1, $2, $3, 'human', 'admin', $4, '{}')`,
			userID, req.Email, req.DisplayName, apiKeysJSON,
		); err != nil {
			return internalError(c, "failed to create bootstrap admin user")
		}

		return c.JSON(http.StatusCreated, map[string]any{
			"user_id":      userID,
			"email":        req.Email,
			"display_name": req.DisplayName,
			"role":         "admin",
			"api_key":      rawKey,
			"api_key_id":   keyID,
			"note":         "save api_key — it will not be shown again",
		})
	}
}
