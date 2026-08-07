package server

import (
	"context"
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
	// Memories (§4.3, §7)
	v1.POST("/memories", handleRemember(pool))
	v1.GET("/memories", handleRecall(pool))
	v1.POST("/memories/:id/activate", handleActivateMemory(pool))
	v1.PATCH("/memories/:id/redact", handleRedactMemory(pool))
	v1.POST("/memories/:id/commit/:commit_id/resolve", handleResolveCommit(pool))
	v1.POST("/memories/:id/commit/:commit_id/reply", handleV1ReplyCommit(pool))
	v1.PATCH("/memories/:id/reinforce", handleReinforceMemory(pool))
	v1.PATCH("/memories/:id/update", handleUpdateMemory(pool))

	// Events (§4.3) — POST is write; GET is read
	v1.POST("/events", handleEmitEvent(pool))
	v1.GET("/events", handleListEvents(pool))

	// Admin GC trigger
	gc := v1.Group("/admin/gc", RequireAdmin())
	gc.POST("", handleRunGC(pool))
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
		// aihub#236: activation state is server-derived and must never come from
		// the request. `json:"-"` on those fields is NOT sufficient — echo's
		// DefaultBinder routes application/xml and text/xml bodies to
		// encoding/xml, which ignores json tags and falls back to the Go field
		// name, so <ActivationCount>9999</ActivationCount> binds straight through.
		// Zero the trio here, before any use, exactly as CallerUserID/CallerDisplay
		// are overwritten below. Only UpdateMemory may populate these, and it
		// constructs the struct directly rather than binding it.
		req.LastActivatedAt = nil
		req.LastActivatedBy = nil
		req.ActivationCount = 0
		// If project is not provided but work_item_id is, back-fill project from the work item.
		if req.Project == "" && req.WorkItemID != nil && *req.WorkItemID != "" {
			wi, wiErr := domain.GetWorkItem(ctx, pool, *req.WorkItemID)
			if wiErr != nil {
				return writeError(c, domain.NewErr(domain.ErrBadRequest, "project is required (work_item_id lookup failed)"))
			}
			req.Project = wi.Project
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

		// C5 (aihub#210): methodology.* artifacts (spec/plan/review/...) are
		// wi-scoped and may only be written by the session holding the target wi's
		// current attempt. Project-writer alone is not enough — otherwise any writer
		// (including a drifted subagent or a fleet machine user) could overwrite or
		// supersede another wi's spec/plan. Require the attempt credential AND bind
		// it to the memory's work_item_id: VerifyAttemptCredentialPool confirms the
		// attempt is the CURRENT attempt for that wi. Non-methodology memories are
		// unaffected and stay project-writer gated.
		if strings.HasPrefix(req.Type, "methodology.") {
			wiID := ""
			if req.WorkItemID != nil {
				wiID = *req.WorkItemID
			}
			if wiID == "" {
				return writeError(c, domain.NewErr(domain.ErrBadRequest,
					"work_item_id is required for methodology.* artifacts"))
			}
			if req.AttemptID == "" || req.SessionSecret == "" {
				return writeError(c, domain.NewErr(domain.ErrForbidden,
					"methodology.* artifacts require attempt credentials; write them via pf_save_artifact from the claiming session"))
			}
			if credErr := domain.VerifyAttemptCredentialPool(
				ctx, pool, wiID, req.AttemptID, req.ClaimEpoch, req.SessionSecret,
			); credErr != nil {
				return writeError(c, credErr)
			}
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

// enforceMethodologyAttemptGate is the aihub#210 wi-binding gate for MUTATING an
// existing methodology.* memory (supersede-via-update, or in-place reinforce).
// Such artifacts may only be mutated by the session holding the TARGET memory's
// own work-item attempt — project-writer is not enough, otherwise pf_update_memory
// / pf_reinforce_memory become a back door around handleRemember's create-time
// gate. memType/memWorkItemID come from the LOADED target memory (never from the
// caller). Returns a non-nil AihubError to write on rejection; nil to proceed.
// Non-methodology memories keep the pre-existing verify-if-supplied contract.
func enforceMethodologyAttemptGate(
	ctx context.Context, pool *pgxpool.Pool,
	memType string, memWorkItemID *string,
	attemptID, sessionSecret string, claimEpoch int64, reqWorkItemID string,
) *domain.AihubError {
	if strings.HasPrefix(memType, "methodology.") {
		wiID := ""
		if memWorkItemID != nil {
			wiID = *memWorkItemID
		}
		if wiID == "" {
			return domain.NewErr(domain.ErrForbidden,
				"methodology.* artifact is not bound to a work item and cannot be mutated")
		}
		if attemptID == "" || sessionSecret == "" {
			return domain.NewErr(domain.ErrForbidden,
				"mutating a methodology.* artifact requires its work item's attempt credentials")
		}
		// Bind to the target memory's wi, not the caller-supplied one.
		return domain.VerifyAttemptCredentialPool(ctx, pool, wiID, attemptID, claimEpoch, sessionSecret)
	}
	// Non-methodology: verify only if the caller supplied a credential.
	if attemptID != "" || sessionSecret != "" {
		if reqWorkItemID == "" {
			return domain.NewErr(domain.ErrBadRequest,
				"work_item_id is required when attempt_id/session_secret are provided")
		}
		return domain.VerifyAttemptCredentialPool(ctx, pool, reqWorkItemID, attemptID, claimEpoch, sessionSecret)
	}
	return nil
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
		if algo := c.QueryParam("recall_algo"); algo != "" {
			req.RecallAlgo = algo
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

		// Enforce project-writer access before reinforcing — activate is a mutating
		// reinforcement (bumps activation_count/stability and revives archived rows),
		// so it requires the same writer gate as handleResolveCommit/handleV1ReplyCommit.
		// Without this any authed user could strengthen/revive any memory (IDOR, aihub#146).
		// A memory's project is immutable, so a pre-check has no TOCTOU concern.
		project, _, loadErr := commitMemoryProjectFn(ctx, pool, memID)
		if loadErr != nil {
			return writeError(c, domain.NewErr(domain.ErrNotFound, "memory not found"))
		}
		if err := checkProjectAccess(c, u, project, "writer"); err != nil {
			return err
		}

		resp, aihubErr := doActivateFn(ctx, pool, memID, u.UserID, u.DisplayName)
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

// handleResolveCommit handles POST /v1/memories/:id/commit/:commit_id/resolve.
// Marks the commit entry as resolved, writes an AI reply, and emits
// memory_commit_resolved. Requires writer access on the memory's project.
func handleResolveCommit(pool *pgxpool.Pool) echo.HandlerFunc {
	return func(c echo.Context) error {
		u := GetUser(c)
		ctx, cancel := contextWithTimeout(c)
		defer cancel()

		memID := c.Param("id")
		commitID := c.Param("commit_id")
		if memID == "" || commitID == "" {
			return writeError(c, domain.NewErr(domain.ErrBadRequest, "memory id and commit_id are required"))
		}

		var req struct {
			Reply string `json:"reply"`
		}
		if err := c.Bind(&req); err != nil {
			return writeError(c, domain.NewErr(domain.ErrBadRequest, "invalid request body"))
		}
		if req.Reply == "" {
			return writeError(c, domain.NewErr(domain.ErrBadRequest, "reply is required"))
		}

		// Enforce project-writer access before mutating — mirrors handleUIEditCommit
		// and handleUIArtifactCommit. A memory's project is immutable, so a pre-check
		// is safe with no TOCTOU concern.
		project, _, loadErr := commitMemoryProjectFn(ctx, pool, memID)
		if loadErr != nil {
			return writeError(c, domain.NewErr(domain.ErrNotFound, "memory not found"))
		}
		if err := checkProjectAccess(c, u, project, "writer"); err != nil {
			return err
		}

		if err := domain.ResolveCommit(ctx, pool, memID, commitID, req.Reply, u.UserID, u.DisplayName); err != nil {
			return domainErr(c, err)
		}
		return c.JSON(http.StatusOK, map[string]bool{"ok": true})
	}
}

// handleV1ReplyCommit handles POST /v1/memories/:id/commit/:commit_id/reply.
// Appends a threaded reply to a commit entry. Requires writer access on the
// memory's project. JSON body: {"body": "..."}. Returns {ok: true}.
func handleV1ReplyCommit(pool *pgxpool.Pool) echo.HandlerFunc {
	return func(c echo.Context) error {
		u := GetUser(c)
		ctx, cancel := contextWithTimeout(c)
		defer cancel()

		memID := c.Param("id")
		commitID := c.Param("commit_id")
		if memID == "" || commitID == "" {
			return writeError(c, domain.NewErr(domain.ErrBadRequest, "memory id and commit_id are required"))
		}

		var req struct {
			Body string `json:"body"`
		}
		if err := c.Bind(&req); err != nil {
			return writeError(c, domain.NewErr(domain.ErrBadRequest, "invalid request body"))
		}
		if req.Body == "" {
			return writeError(c, domain.NewErr(domain.ErrBadRequest, "body is required"))
		}

		project, _, loadErr := commitMemoryProjectFn(ctx, pool, memID)
		if loadErr != nil {
			return writeError(c, domain.NewErr(domain.ErrNotFound, "memory not found"))
		}
		if err := checkProjectAccess(c, u, project, "writer"); err != nil {
			return err
		}

		if err := doReplyCommitFn(ctx, pool, memID, commitID, u.UserID, u.DisplayName, req.Body); err != nil {
			return domainErr(c, err)
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

		// C1: enforce project-level writer access.
		// Derive the project from the work_item when work_item_id is provided.
		if req.WorkItemID != "" {
			wi, aihubErr := domain.GetWorkItem(ctx, pool, req.WorkItemID)
			if aihubErr != nil {
				return domainErr(c, aihubErr)
			}
			if err := checkProjectAccess(c, u, wi.Project, "writer"); err != nil {
				return err
			}
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
		var memWorkItemID *string
		var memAttrsRaw []byte
		var memBaseStrength float64
		var memActivationCount int
		if err := pool.QueryRow(ctx, `
			SELECT project, type, status, work_item_id, attrs, base_strength, activation_count
			FROM memories WHERE id=$1`, memID,
		).Scan(&memProject, &memType, &memStatus, &memWorkItemID, &memAttrsRaw, &memBaseStrength, &memActivationCount); err != nil {
			return writeError(c, domain.NewErr(domain.ErrNotFound, "memory not found"))
		}
		if memStatus == "redacted" {
			return writeError(c, domain.NewErr(domain.ErrForbidden,
				"cannot reinforce a redacted memory"))
		}

		if err := checkProjectAccess(c, u, memProject, "writer"); err != nil {
			return err
		}

		// C5 (aihub#210): methodology.* memories require the target wi's attempt
		// credential (mandatory + wi-bound); non-methodology keeps verify-if-supplied.
		if gateErr := enforceMethodologyAttemptGate(ctx, pool, memType, memWorkItemID,
			req.AttemptID, req.SessionSecret, req.ClaimEpoch, req.WorkItemID); gateErr != nil {
			return writeError(c, gateErr)
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
			"memory_id":        memID,
			"activation_count": newActivationCount,
			"base_strength":    newBaseStrength,
		})
		_, _ = pool.Exec(ctx, `
			INSERT INTO agent_events (id, actor_user_id, actor_display, event_type, payload, project)
			VALUES ($1, $2, $3, 'memory_reinforced', $4, $5)`,
			domain.NewID("evt"), u.UserID, u.DisplayName, payload, memProject,
		)

		return c.JSON(http.StatusOK, map[string]any{
			"memory_id":        memID,
			"activation_count": newActivationCount,
			"base_strength":    newBaseStrength,
		})
	}
}

// handleUpdateMemory handles PATCH /v1/memories/:id/update (aihub#201).
// Creates a new version superseding the current lineage head resolved from
// :id, inheriting any field the caller didn't override, and advances the
// latest_id cursor across the whole lineage. Mirrors handleReinforceMemory's
// auth/credential shape.
func handleUpdateMemory(pool *pgxpool.Pool) echo.HandlerFunc {
	return func(c echo.Context) error {
		u := GetUser(c)
		memID := c.Param("id")

		var req struct {
			Content       *string  `json:"content,omitempty"`
			Visibility    *string  `json:"visibility,omitempty"`
			Tags          []string `json:"tags,omitempty"`
			BaseStrength  *float64 `json:"base_strength,omitempty"`
			AttemptID     string   `json:"attempt_id"`
			ClaimEpoch    int64    `json:"claim_epoch"`
			SessionSecret string   `json:"session_secret"`
			WorkItemID    string   `json:"work_item_id,omitempty"`
		}
		if err := c.Bind(&req); err != nil {
			return writeError(c, domain.NewErr(domain.ErrBadRequest, err.Error()))
		}

		ctx, cancel := contextWithTimeout(c)
		defer cancel()

		// Load current lineage head for the access check (project) and to know
		// which memory record actually gets superseded.
		head, aerr := domain.GetLatestByID(ctx, pool, memID)
		if aerr != nil {
			return writeError(c, aerr)
		}
		if head.Status == "redacted" {
			return writeError(c, domain.NewErr(domain.ErrForbidden,
				"cannot update a redacted memory"))
		}

		if err := checkProjectAccess(c, u, head.Project, "writer"); err != nil {
			return err
		}

		// C5 (aihub#210): superseding a methodology.* memory via update requires the
		// TARGET memory's own wi attempt credential (mandatory + bound to head.WorkItemID,
		// not the caller-supplied work_item_id) — closes the pf_update_memory back door
		// around handleRemember's create-time gate. Non-methodology keeps verify-if-supplied.
		if gateErr := enforceMethodologyAttemptGate(ctx, pool, head.Type, head.WorkItemID,
			req.AttemptID, req.SessionSecret, req.ClaimEpoch, req.WorkItemID); gateErr != nil {
			return writeError(c, gateErr)
		}

		newHead, err := domain.UpdateMemory(ctx, pool, memID, &domain.UpdateMemoryRequest{
			Content:       req.Content,
			Visibility:    req.Visibility,
			Tags:          req.Tags,
			BaseStrength:  req.BaseStrength,
			CallerUserID:  u.UserID,
			CallerDisplay: u.DisplayName,
		})
		if err != nil {
			return domainErr(c, err)
		}

		// Emit memory_updated event (best effort — memory_updated is not in
		// chk_evt_work_item_id's whitelist, so this only lands when the memory
		// carries a work_item_id; a constraint violation here must not fail
		// the request, matching handleReinforceMemory's memory_reinforced emission).
		payload, _ := json.Marshal(map[string]any{
			"memory_id":     memID,
			"new_memory_id": newHead.ID,
		})
		if head.WorkItemID != nil && *head.WorkItemID != "" {
			_, _ = pool.Exec(ctx, `
				INSERT INTO agent_events (id, work_item_id, actor_user_id, actor_display, event_type, payload, project)
				VALUES ($1, $2, $3, $4, 'memory_updated', $5, $6)`,
				domain.NewID("evt"), *head.WorkItemID, u.UserID, u.DisplayName, payload, head.Project,
			)
		} else {
			_, _ = pool.Exec(ctx, `
				INSERT INTO agent_events (id, actor_user_id, actor_display, event_type, payload, project)
				VALUES ($1, $2, $3, 'memory_updated', $4, $5)`,
				domain.NewID("evt"), u.UserID, u.DisplayName, payload, head.Project,
			)
		}

		return c.JSON(http.StatusOK, newHead)
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
