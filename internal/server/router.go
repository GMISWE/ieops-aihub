package server

import (
	"context"
	"net/http"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/labstack/echo/v4"

	"github.com/GMISWE/ieops-aihub/internal/domain"
	"github.com/GMISWE/ieops-aihub/internal/version"
)

// NewRouter constructs the echo router with all routes.
func NewRouter(pool *pgxpool.Pool) *echo.Echo {
	e := echo.New()
	e.HideBanner = true
	e.HidePort = true

	e.Use(RequestID())
	e.Use(Recovery())

	// Unauthenticated
	e.GET("/v1/health", handleHealth(pool))
	e.GET("/v1/version", handleVersion())

	// Authenticated routes
	v1 := e.Group("/v1", BearerAuth(pool), IdempotencyMiddleware())

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

// handleListWorkItems handles GET /v1/work_items.
func handleListWorkItems(pool *pgxpool.Pool) echo.HandlerFunc {
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

		filter := domain.ListWorkItemsFilter{
			Limit: 50,
		}

		if status := c.QueryParam("status"); status != "" {
			filter.Status = []string{status}
		}
		if wiType := c.QueryParam("wi_type"); wiType != "" {
			filter.WIType = &wiType
		}
		if priority := c.QueryParam("priority"); priority != "" {
			filter.Priority = &priority
		}
		if label := c.QueryParam("label"); label != "" {
			filter.Label = &label
		}

		result, aihubErr := domain.ListWorkItems(ctx, pool, project, filter)
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

		if aihubErr := domain.CancelWorkItem(ctx, pool, c.Param("id"), u.UserID, u.Role, u.ProjectRoles); aihubErr != nil {
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

		// For force_takeover the domain layer enforces maintainer/admin or self.
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

		if aihubErr := domain.FnCompleteAttempt(ctx, pool, c.Param("id"), &req); aihubErr != nil {
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

		resp, aihubErr := domain.FnForceTakeover(ctx, pool, c.Param("id"), u.UserID, u.Role, u.ProjectRoles, &req)
		if aihubErr != nil {
			return writeError(c, aihubErr)
		}
		return c.JSON(http.StatusOK, resp)
	}
}

// handleUnblockWorkItem handles POST /v1/work_items/:id/unblock (admin only).
func handleUnblockWorkItem(pool *pgxpool.Pool) echo.HandlerFunc {
	return func(c echo.Context) error {
		ctx, cancel := contextWithTimeout(c)
		defer cancel()

		u := GetUser(c)
		wiID := c.Param("id")
		_, err := pool.Exec(ctx, `
			UPDATE work_items SET status='queued'
			WHERE id=$1 AND status='blocked'`, wiID)
		if err != nil {
			return internalError(c, "failed to unblock work item")
		}
		// H6: emit admin_unblock audit event
		if u != nil {
			pool.Exec(context.Background(), `
				INSERT INTO agent_events (id, work_item_id, actor_user_id, api_key_id, event_type, payload, project)
				VALUES ($1, $2, $3, $4, 'admin_unblock', '{"action":"unblock"}'::jsonb,
				    (SELECT project FROM work_items WHERE id=$2))`,
				domain.NewID("evt"), wiID, u.UserID, u.APIKeyID) //nolint:errcheck
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

		if aihubErr := domain.CreateDependency(ctx, pool, &req, u.UserID, u.ProjectRoles); aihubErr != nil {
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

		resp, aihubErr := domain.ListDependencies(ctx, pool, wiID, u.ProjectRoles)
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
			Email        *string           `json:"email"`
			DisplayName  string            `json:"display_name"`
			UserType     string            `json:"user_type"`
			Role         string            `json:"role"`
			ProjectRoles map[string]string `json:"project_roles"`
			AuthorAliases []string         `json:"author_aliases"`
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

		projectRolesJSON := []byte("{}")
		if req.ProjectRoles != nil {
			projectRolesJSON = must(marshalJSON(req.ProjectRoles))
		}

		userID := domain.NewID("u")
		var id string
		err := pool.QueryRow(ctx, `
			INSERT INTO users (id, email, display_name, user_type, role, project_roles, author_aliases)
			VALUES ($1, $2, $3, $4, $5, $6, $7)
			RETURNING id`,
			userID, *email, req.DisplayName, req.UserType, req.Role, projectRolesJSON, req.AuthorAliases,
		).Scan(&id)
		if err != nil {
			return internalError(c, "failed to create user")
		}

		return c.JSON(http.StatusCreated, map[string]any{
			"id":            id,
			"email":         *email,
			"display_name":  req.DisplayName,
			"user_type":     req.UserType,
			"role":          req.Role,
			"project_roles": req.ProjectRoles,
		})
	}
}

// handleListUsers handles GET /v1/admin/users.
func handleListUsers(pool *pgxpool.Pool) echo.HandlerFunc {
	return func(c echo.Context) error {
		ctx, cancel := contextWithTimeout(c)
		defer cancel()

		rows, err := pool.Query(ctx, `
			SELECT id, email, display_name, user_type, role, project_roles
			FROM users ORDER BY created_at DESC LIMIT 100`)
		if err != nil {
			return internalError(c, "failed to list users")
		}
		defer rows.Close()

		var items []map[string]any
		for rows.Next() {
			var id, email, displayName, userType, role string
			var projectRolesRaw []byte
			if err := rows.Scan(&id, &email, &displayName, &userType, &role, &projectRolesRaw); err != nil {
				continue
			}
			items = append(items, map[string]any{
				"id":            id,
				"email":         email,
				"display_name":  displayName,
				"user_type":     userType,
				"role":          role,
				"project_roles": string(projectRolesRaw),
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
			DisplayName  *string           `json:"display_name"`
			Role         *string           `json:"role"`
			ProjectRoles map[string]string `json:"project_roles"`
			AuthorAliases []string         `json:"author_aliases"`
		}
		if err := c.Bind(&req); err != nil {
			return writeError(c, domain.NewErr(domain.ErrBadRequest, "invalid request body"))
		}

		// Validate project_roles values
		if req.ProjectRoles != nil {
			for proj, role := range req.ProjectRoles {
				if role != "viewer" && role != "writer" && role != "maintainer" {
					return writeError(c, domain.NewErr(domain.ErrBadRequest,
						"invalid project_roles value for "+proj+": must be viewer, writer, or maintainer"))
				}
			}
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
		if req.ProjectRoles != nil {
			prJSON := must(marshalJSON(req.ProjectRoles))
			sets = append(sets, "project_roles=$"+itoa(idx))
			args = append(args, prJSON)
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

		_, err := pool.Exec(ctx, `
			UPDATE users SET api_keys = api_keys || $1::jsonb
			WHERE id=$2`,
			"["+string(newKeyJSON)+"]", c.Param("id"),
		)
		if err != nil {
			return internalError(c, "failed to add API key")
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
	result := ""
	for i, s := range ss {
		if i > 0 {
			result += ", "
		}
		result += s
	}
	return result
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
