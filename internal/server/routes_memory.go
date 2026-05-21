package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/labstack/echo/v4"

	"github.com/GMISWE/ieops-aihub/internal/domain"
)

// domainErr converts an error returned by a domain function to a *domain.AihubError,
// then calls writeError. Domain functions return error (interface) but always
// construct *domain.AihubError internally; this assertion is always safe.
// If somehow a non-AihubError surfaces, wrap it as ErrInternal.
func domainErr(c echo.Context, err error) error {
	if ae, ok := err.(*domain.AihubError); ok {
		return writeError(c, ae)
	}
	return writeError(c, domain.NewErr(domain.ErrInternalError, err.Error()))
}

// RegisterMemoryRoutes adds all Round 2b routes to the authenticated route group.
// Called once from NewRouter after the admin group is registered.
func RegisterMemoryRoutes(v1 *echo.Group, pool *pgxpool.Pool) {
	// Scenario phase config (§4.3)
	v1.GET("/scenarios/:scenario/phase_config", handleGetScenarioConfig(pool))
	v1.PUT("/scenarios/:scenario/phase_config", handleUpdateScenarioConfig(pool))

	// Memories (§4.3, §7)
	v1.POST("/memories", handleRemember(pool))
	v1.GET("/memories", handleRecall(pool))
	v1.POST("/memories/:id/activate", handleActivateMemory(pool))
	v1.PATCH("/memories/:id/redact", handleRedactMemory(pool))
	v1.PATCH("/memories/:id/reinforce", handleReinforceMemory(pool))

	// Events (§4.3) — POST is write; GET is read
	v1.POST("/events", handleEmitEvent(pool))
	v1.GET("/events", handleListEvents(pool))

	// Admin GC trigger
	gc := v1.Group("/admin/gc", RequireAdmin())
	gc.POST("", handleRunGC(pool))
}

// ─── Scenario Phase Config ────────────────────────────────────────────────────

// handleGetScenarioConfig handles GET /v1/scenarios/:scenario/phase_config.
// Requires viewer+ access.
func handleGetScenarioConfig(pool *pgxpool.Pool) echo.HandlerFunc {
	return func(c echo.Context) error {
		u := GetUser(c)
		ctx, cancel := contextWithTimeout(c)
		defer cancel()

		scenario := c.Param("scenario")
		if scenario == "" {
			return writeError(c, domain.NewErr(domain.ErrBadRequest, "scenario parameter is required"))
		}

		// Viewer+ access: check user has at least viewer role on any project
		// For scenario config we use global role check (admin or any project role)
		if u.Role != "admin" && len(u.ProjectRoles) == 0 {
			return writeError(c, domain.NewErr(domain.ErrForbidden, "viewer access required"))
		}

		cfg, aihubErr := domain.GetScenarioConfig(ctx, pool, scenario)
		if aihubErr != nil {
			return domainErr(c, aihubErr)
		}
		return c.JSON(http.StatusOK, cfg)
	}
}

// handleUpdateScenarioConfig handles PUT /v1/scenarios/:scenario/phase_config.
// Requires maintainer or admin.
func handleUpdateScenarioConfig(pool *pgxpool.Pool) echo.HandlerFunc {
	return func(c echo.Context) error {
		u := GetUser(c)
		ctx, cancel := contextWithTimeout(c)
		defer cancel()

		scenario := c.Param("scenario")
		if scenario == "" {
			return writeError(c, domain.NewErr(domain.ErrBadRequest, "scenario parameter is required"))
		}

		// Require maintainer or admin
		if u.Role != "admin" {
			hasMaintainer := false
			for _, role := range u.ProjectRoles {
				if role == "maintainer" {
					hasMaintainer = true
					break
				}
			}
			if !hasMaintainer {
				return writeError(c, domain.NewErr(domain.ErrForbidden, "maintainer or admin role required"))
			}
		}

		var req domain.UpdateScenarioConfigRequest
		if err := c.Bind(&req); err != nil {
			return writeError(c, domain.NewErr(domain.ErrBadRequest, "invalid request body"))
		}
		if len(req.Content) == 0 {
			return writeError(c, domain.NewErr(domain.ErrBadRequest, "content is required"))
		}

		cfg, aihubErr := domain.UpdateScenarioConfig(ctx, pool, scenario, &req, u.UserID)
		if aihubErr != nil {
			return domainErr(c, aihubErr)
		}
		return c.JSON(http.StatusOK, map[string]any{"version": cfg.Version})
	}
}

// ─── Memories ─────────────────────────────────────────────────────────────────

// handleRemember handles POST /v1/memories.
func handleRemember(pool *pgxpool.Pool) echo.HandlerFunc {
	return func(c echo.Context) error {
		u := GetUser(c)
		ctx, cancel := contextWithTimeout(c)
		defer cancel()

		var req domain.RememberRequest
		if err := c.Bind(&req); err != nil {
			return writeError(c, domain.NewErr(domain.ErrBadRequest, "invalid request body"))
		}
		if req.Project == "" {
			return writeError(c, domain.NewErr(domain.ErrBadRequest, "project is required"))
		}
		if req.Type == "" {
			return writeError(c, domain.NewErr(domain.ErrBadRequest, "type is required"))
		}
		if req.Content == "" {
			return writeError(c, domain.NewErr(domain.ErrBadRequest, "content is required"))
		}

		// C1: require writer access to the project
		if err := checkProjectAccess(c, u, req.Project, "writer"); err != nil {
			return err
		}

		req.CallerUserID = u.UserID
		req.CallerDisplay = u.DisplayName

		mem, isNew, aihubErr := domain.Remember(ctx, pool, &req)
		if aihubErr != nil {
			return domainErr(c, aihubErr)
		}
		// Return full memory object per design §4.3 + is_new flag
		resp := map[string]any{
			"id":               mem.ID,
			"memory_id":        mem.ID, // alias for backward compat
			"is_new":           isNew,
			"type":             mem.Type,
			"project":          mem.Project,
			"visibility":       mem.Visibility,
			"activation_count": mem.ActivationCount,
			"stability_days":   mem.StabilityDays,
			"base_strength":    mem.BaseStrength,
			"created_at":       mem.CreatedAt,
		}
		return c.JSON(http.StatusCreated, resp)
	}
}

// handleRecall handles GET /v1/memories.
func handleRecall(pool *pgxpool.Pool) echo.HandlerFunc {
	return func(c echo.Context) error {
		u := GetUser(c)
		ctx, cancel := contextWithTimeout(c)
		defer cancel()

		project := c.QueryParam("project")
		if project == "" {
			return writeError(c, domain.NewErr(domain.ErrBadRequest, "project is required"))
		}

		// C1: require viewer access
		if err := checkProjectAccess(c, u, project, "viewer"); err != nil {
			return err
		}

		req := &domain.RecallRequest{
			Project:      project,
			Query:        c.QueryParam("query"),
			Cursor:       c.QueryParam("cursor"),
			CallerUserID: u.UserID,
			CallerRole:   u.Role,
		}

		// Parse type filter (comma-separated or repeated params)
		if typeParam := c.QueryParam("type"); typeParam != "" {
			req.Types = strings.Split(typeParam, ",")
		}
		if vis := c.QueryParam("visibility"); vis != "" {
			req.Visibility = vis
		}
		if wiID := c.QueryParam("work_item_id"); wiID != "" {
			req.WorkItemID = &wiID
		}
		if topK := c.QueryParam("top_k"); topK != "" {
			if n, err := strconv.Atoi(topK); err == nil {
				req.TopK = n
			}
		}
		if minS := c.QueryParam("min_strength"); minS != "" {
			if f, err := strconv.ParseFloat(minS, 64); err == nil {
				req.MinStrength = f
			}
		}
		if rw := c.QueryParam("recency_weight"); rw != "" {
			if f, err := strconv.ParseFloat(rw, 64); err == nil {
				req.RecencyWeight = f
			}
		}
		if c.QueryParam("include_archived") == "true" {
			req.IncludeArchived = true
		}

		resp, aihubErr := domain.Recall(ctx, pool, req)
		if aihubErr != nil {
			return domainErr(c, aihubErr)
		}
		return c.JSON(http.StatusOK, resp)
	}
}

// handleActivateMemory handles POST /v1/memories/:id/activate.
func handleActivateMemory(pool *pgxpool.Pool) echo.HandlerFunc {
	return func(c echo.Context) error {
		u := GetUser(c)
		ctx, cancel := contextWithTimeout(c)
		defer cancel()

		memID := c.Param("id")
		if memID == "" {
			return writeError(c, domain.NewErr(domain.ErrBadRequest, "memory id is required"))
		}

		resp, aihubErr := domain.Activate(ctx, pool, memID, u.UserID, u.DisplayName)
		if aihubErr != nil {
			return domainErr(c, aihubErr)
		}
		return c.JSON(http.StatusOK, resp)
	}
}

// handleRedactMemory handles PATCH /v1/memories/:id/redact.
func handleRedactMemory(pool *pgxpool.Pool) echo.HandlerFunc {
	return func(c echo.Context) error {
		u := GetUser(c)
		ctx, cancel := contextWithTimeout(c)
		defer cancel()

		memID := c.Param("id")
		if memID == "" {
			return writeError(c, domain.NewErr(domain.ErrBadRequest, "memory id is required"))
		}

		if aihubErr := domain.Redact(ctx, pool, memID, u.UserID, u.Role); aihubErr != nil {
			return domainErr(c, aihubErr)
		}
		return c.JSON(http.StatusOK, map[string]bool{"ok": true})
	}
}

// ─── Events ───────────────────────────────────────────────────────────────────

// handleEmitEvent handles POST /v1/events.
func handleEmitEvent(pool *pgxpool.Pool) echo.HandlerFunc {
	return func(c echo.Context) error {
		u := GetUser(c)
		ctx, cancel := contextWithTimeout(c)
		defer cancel()

		var req domain.EmitEventRequest
		if err := c.Bind(&req); err != nil {
			return writeError(c, domain.NewErr(domain.ErrBadRequest, "invalid request body"))
		}
		if req.EventType == "" {
			return writeError(c, domain.NewErr(domain.ErrBadRequest, "event_type is required"))
		}

		evtID, aihubErr := domain.EmitEvent(ctx, pool, &req, u.UserID, u.DisplayName, u.Role)
		if aihubErr != nil {
			return domainErr(c, aihubErr)
		}
		return c.JSON(http.StatusCreated, map[string]string{"event_id": evtID})
	}
}

// handleListEvents handles GET /v1/events.
func handleListEvents(pool *pgxpool.Pool) echo.HandlerFunc {
	return func(c echo.Context) error {
		u := GetUser(c)
		ctx, cancel := contextWithTimeout(c)
		defer cancel()

		f := &domain.ListEventsFilter{}

		if wiID := c.QueryParam("work_item_id"); wiID != "" {
			f.WorkItemID = &wiID
			// C1: require viewer access to this wi's project
			wi, aihubErr := domain.GetWorkItem(ctx, pool, wiID)
			if aihubErr != nil {
				return domainErr(c, aihubErr)
			}
			if err := checkProjectAccess(c, u, wi.Project, "viewer"); err != nil {
				return err
			}
		} else if proj := c.QueryParam("project"); proj != "" {
			f.Project = &proj
			if err := checkProjectAccess(c, u, proj, "viewer"); err != nil {
				return err
			}
		} else {
			return writeError(c, domain.NewErr(domain.ErrBadRequest, "work_item_id or project is required"))
		}

		if userID := c.QueryParam("user_id"); userID != "" {
			f.UserID = &userID
		}
		if types := c.QueryParam("types"); types != "" {
			f.Types = strings.Split(types, ",")
		}
		if since := c.QueryParam("since"); since != "" {
			f.Since = &since
		}
		if cursor := c.QueryParam("cursor"); cursor != "" {
			f.Cursor = &cursor
		}
		if limit := c.QueryParam("limit"); limit != "" {
			if n, err := strconv.Atoi(limit); err == nil {
				f.Limit = n
			}
		}
		if c.QueryParam("pinned_first") == "true" {
			f.PinnedFirst = true
		}

		resp, aihubErr := domain.ListEvents(ctx, pool, f)
		if aihubErr != nil {
			return domainErr(c, aihubErr)
		}
		return c.JSON(http.StatusOK, resp)
	}
}

// ─── Admin GC Trigger ─────────────────────────────────────────────────────────

// handleReinforceMemory handles PATCH /v1/memories/:id/reinforce.
//
// Per design §7.3 / §19.6: reinforce strengthens an EXISTING memory in place —
// it does NOT create a new row. Concretely:
//   - activation_count += 1
//   - stability_days recomputed via the Ebbinghaus formula
//   - last_activated_at / last_activated_by updated
//   - attrs.reinforcements gets a new entry {added_at, from_wi, context}
//   - base_strength optionally adjusted by strength_delta (clamped to [1, 5])
//
// Returns {memory_id, activation_count, base_strength} per §5.2.
func handleReinforceMemory(pool *pgxpool.Pool) echo.HandlerFunc {
	return func(c echo.Context) error {
		u := GetUser(c)
		memID := c.Param("id")

		var req struct {
			AdditionalContext string   `json:"additional_context"`
			AttemptID         string   `json:"attempt_id"`
			ClaimEpoch        int64    `json:"claim_epoch"`
			SessionSecret     string   `json:"session_secret"`
			StrengthDelta     *float64 `json:"strength_delta,omitempty"`
			WorkItemID        string   `json:"work_item_id,omitempty"`
		}
		if err := c.Bind(&req); err != nil {
			return writeError(c, domain.NewErr(domain.ErrBadRequest, err.Error()))
		}
		if req.AdditionalContext == "" {
			return writeError(c, domain.NewErr(domain.ErrBadRequest,
				"additional_context is required"))
		}

		ctx, cancel := contextWithTimeout(c)
		defer cancel()

		// Load existing memory metadata (project for access check, attrs/strength for mutation).
		var memProject, memType, memStatus string
		var memAttrsRaw []byte
		var memBaseStrength float64
		var memActivationCount int
		if err := pool.QueryRow(ctx, `
			SELECT project, type, status, attrs, base_strength, activation_count
			FROM memories WHERE id=$1`, memID,
		).Scan(&memProject, &memType, &memStatus, &memAttrsRaw, &memBaseStrength, &memActivationCount); err != nil {
			return writeError(c, domain.NewErr(domain.ErrNotFound, "memory not found"))
		}
		if memStatus == "redacted" {
			return writeError(c, domain.NewErr(domain.ErrForbidden,
				"cannot reinforce a redacted memory"))
		}

		if err := checkProjectAccess(c, u, memProject, "writer"); err != nil {
			return err
		}

		// Verify attempt credential when caller supplied one (mutating ops should carry it).
		if req.AttemptID != "" || req.SessionSecret != "" {
			if req.WorkItemID == "" {
				return writeError(c, domain.NewErr(domain.ErrBadRequest,
					"work_item_id is required when attempt_id/session_secret are provided"))
			}
			if credErr := domain.VerifyAttemptCredentialPool(
				ctx, pool, req.WorkItemID, req.AttemptID, req.ClaimEpoch, req.SessionSecret,
			); credErr != nil {
				return writeError(c, credErr)
			}
		}

		// Append the reinforcement record to attrs.reinforcements.
		attrs := map[string]any{}
		if len(memAttrsRaw) > 0 {
			_ = json.Unmarshal(memAttrsRaw, &attrs)
		}
		reinforcements, _ := attrs["reinforcements"].([]any)
		entry := map[string]any{
			"added_at": time.Now().UTC().Format(time.RFC3339),
			"context":  req.AdditionalContext,
		}
		if req.WorkItemID != "" {
			entry["from_wi"] = req.WorkItemID
		}
		reinforcements = append(reinforcements, entry)
		attrs["reinforcements"] = reinforcements
		attrsJSON, _ := json.Marshal(attrs)

		// Compute the new base_strength and activation_count in Go,
		// then perform a single UPDATE that also recomputes stability_days.
		newActivationCount := memActivationCount + 1
		newBaseStrength := memBaseStrength
		if req.StrengthDelta != nil {
			newBaseStrength = memBaseStrength + *req.StrengthDelta
			if newBaseStrength > 5 {
				newBaseStrength = 5
			}
			if newBaseStrength < 1 {
				newBaseStrength = 1
			}
		}

		// stability_days mirrors domain.computeStabilityDays(memType, newActivationCount):
		// base_stability_for_type × (1 + activation_count × 0.5).
		// We replicate it inline since the helper is unexported.
		baseStability := 7.0
		switch {
		case strings.HasPrefix(memType, "fact."):
			baseStability = 180.0
		case strings.HasPrefix(memType, "rule."), strings.HasPrefix(memType, "methodology."):
			baseStability = 36500.0
		}
		newStability := baseStability * (1.0 + float64(newActivationCount)*0.5)

		_, execErr := pool.Exec(ctx, `
			UPDATE memories
			SET activation_count  = $1,
			    base_strength     = $2,
			    stability_days    = $3,
			    attrs             = $4,
			    last_activated_at = clock_timestamp(),
			    last_activated_by = $5,
			    status            = CASE WHEN status='archived' THEN 'active' ELSE status END,
			    updated_at        = clock_timestamp()
			WHERE id = $6`,
			newActivationCount, newBaseStrength, newStability,
			attrsJSON, u.UserID, memID,
		)
		if execErr != nil {
			return writeError(c, domain.NewErr(domain.ErrInternalError,
				fmt.Sprintf("failed to reinforce memory: %v", execErr)))
		}

		// Emit memory_reinforced event (best effort).
		payload, _ := json.Marshal(map[string]any{
			"memory_id":         memID,
			"activation_count":  newActivationCount,
			"base_strength":     newBaseStrength,
		})
		pool.Exec(ctx, `
			INSERT INTO agent_events (id, actor_user_id, actor_display, event_type, payload, project)
			VALUES ($1, $2, $3, 'memory_reinforced', $4, $5)`,
			domain.NewID("evt"), u.UserID, u.DisplayName, payload, memProject,
		) //nolint:errcheck

		return c.JSON(http.StatusOK, map[string]any{
			"memory_id":        memID,
			"activation_count": newActivationCount,
			"base_strength":    newBaseStrength,
		})
	}
}

// handleRunGC handles POST /v1/admin/gc (admin only).
// Runs all GC sweeps and returns a summary.
func handleRunGC(pool *pgxpool.Pool) echo.HandlerFunc {
	return func(c echo.Context) error {
		ctx, cancel := contextWithTimeout(c)
		defer cancel()

		results := domain.RunAll(ctx, pool)
		return c.JSON(http.StatusOK, map[string]any{"results": results})
	}
}
