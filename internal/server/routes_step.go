package server

import (
	"net/http"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/labstack/echo/v4"

	"github.com/GMISWE/ieops-aihub/internal/domain"
)

// StepState is returned by GET /v1/work_items/:id/step.
type StepState struct {
	WorkItemID         string     `json:"work_item_id"`
	WIType             *string    `json:"wi_type,omitempty"`
	CurrentStep        *string    `json:"current_step,omitempty"`
	CurrentStepStatus  string     `json:"current_step_status"`
	CurrentStepAttempt *string    `json:"current_step_attempt,omitempty"`
	StepStartedAt      *time.Time `json:"step_started_at,omitempty"`
	Version            int64      `json:"version"`
}

// UpdateStepRequest is the body for PATCH /v1/work_items/:id/step.
type UpdateStepRequest struct {
	AttemptID     string         `json:"attempt_id"`
	ClaimEpoch    int64          `json:"claim_epoch"`
	SessionSecret string         `json:"session_secret"`
	Status        string         `json:"status"` // "in_progress" | "completed" | "failed"
	Step          *string        `json:"step,omitempty"`
	StepAttemptID *string        `json:"step_attempt_id,omitempty"`
	Outcome       map[string]any `json:"outcome,omitempty"`
	Heartbeat     bool           `json:"heartbeat,omitempty"`
}

// RegisterStepRoutes adds step / release / attempt lifecycle routes.
func RegisterStepRoutes(v1 *echo.Group, pool *pgxpool.Pool) {
	v1.GET("/work_items/:id/step", handleGetStep(pool))
	v1.PATCH("/work_items/:id/step", handleUpdateStep(pool))
	v1.PATCH("/work_items/:id/renew", handleRenewLease(pool))
	v1.POST("/work_items/:id/pause", handlePauseAttempt(pool))
	// Phase 2 stubs
	v1.POST("/releases/alpha", handleCutAlpha())
	v1.POST("/releases/promote", handlePromote())
}

func handleGetStep(pool *pgxpool.Pool) echo.HandlerFunc {
	return func(c echo.Context) error {
		u := GetUser(c)
		wiID := c.Param("id")

		wi, err := domain.GetWorkItem(c.Request().Context(), pool, wiID)
		if err != nil {
			return writeError(c, domain.NewErr(domain.ErrNotFound, "work item not found"))
		}
		if err := checkProjectAccess(c, u, wi.Project, "viewer"); err != nil {
			return err
		}

		var s StepState
		s.WorkItemID = wiID
		scanErr := pool.QueryRow(c.Request().Context(), `
			SELECT wi_type, current_step, current_step_status,
			       current_step_attempt, step_started_at, version
			FROM wi_step_state WHERE work_item_id = $1`, wiID,
		).Scan(&s.WIType, &s.CurrentStep, &s.CurrentStepStatus,
			&s.CurrentStepAttempt, &s.StepStartedAt, &s.Version)
		if scanErr != nil {
			s.CurrentStepStatus = "idle"
			s.Version = 0
		}
		return c.JSON(http.StatusOK, s)
	}
}

func handleUpdateStep(pool *pgxpool.Pool) echo.HandlerFunc {
	return func(c echo.Context) error {
		u := GetUser(c)
		wiID := c.Param("id")
		var req UpdateStepRequest
		if err := c.Bind(&req); err != nil {
			return writeError(c, domain.NewErr(domain.ErrBadRequest, err.Error()))
		}

		wi, err := domain.GetWorkItem(c.Request().Context(), pool, wiID)
		if err != nil {
			return writeError(c, domain.NewErr(domain.ErrNotFound, "work item not found"))
		}
		if err := checkProjectAccess(c, u, wi.Project, "writer"); err != nil {
			return err
		}

		// N3: verify AttemptCredential — session_secret must match the active attempt
		if req.AttemptID != "" && req.SessionSecret != "" {
			if credErr := domain.VerifyAttemptCredentialPool(
				c.Request().Context(), pool, wiID,
				req.AttemptID, req.ClaimEpoch, req.SessionSecret,
			); credErr != nil {
				return writeError(c, credErr)
			}
		}

		if req.Heartbeat {
			// Heartbeat: best-effort timestamp bump, transient DB errors must not
			// fail the heartbeat (caller will retry anyway).
			pool.Exec(c.Request().Context(), `
				UPDATE wi_step_state SET step_started_at = clock_timestamp(), updated_at = clock_timestamp()
				WHERE work_item_id = $1`, wiID) //nolint:errcheck
			return c.JSON(http.StatusOK, map[string]string{"status": "heartbeat_ok"})
		}

		// All step transitions run in a single transaction for atomicity
		tx, txErr := pool.Begin(c.Request().Context())
		if txErr != nil {
			return writeError(c, domain.NewErr(domain.ErrInternalError, "begin tx"))
		}
		defer tx.Rollback(c.Request().Context()) //nolint:errcheck

		eventType := ""
		// Read current step name for step_completions
		var currentStep *string
		tx.QueryRow(c.Request().Context(), `SELECT current_step FROM wi_step_state WHERE work_item_id=$1`, wiID).Scan(&currentStep) //nolint:errcheck

		switch req.Status {
		case "in_progress":
			// H-Medium: guard idle→in_progress only; reject if already in_progress
			tag, execErr := tx.Exec(c.Request().Context(), `
				INSERT INTO wi_step_state (work_item_id, current_step, current_step_status,
				    current_step_attempt, step_started_at, version)
				VALUES ($1, $2, 'in_progress', $3, clock_timestamp(), 1)
				ON CONFLICT (work_item_id) DO UPDATE
				SET current_step_status = 'in_progress',
				    current_step = EXCLUDED.current_step,
				    current_step_attempt = $3,
				    step_started_at = clock_timestamp(),
				    version = wi_step_state.version + 1,
				    updated_at = clock_timestamp()
				WHERE wi_step_state.current_step_status = 'idle'`,
				wiID, req.Step, req.StepAttemptID)
			if execErr != nil {
				return writeError(c, domain.NewErr(domain.ErrInternalError, execErr.Error()))
			}
			if tag.RowsAffected() == 0 {
				return writeError(c, domain.NewErr(domain.ErrConflictCASFailed, "step already in_progress; cannot start again until completed or failed"))
			}
			eventType = "step_started"
		case "completed":
			if _, execErr := tx.Exec(c.Request().Context(), `
				UPDATE wi_step_state
				SET current_step = $2, current_step_status = 'idle',
				    current_step_attempt = NULL, step_started_at = NULL,
				    version = version + 1, updated_at = clock_timestamp()
				WHERE work_item_id = $1`, wiID, req.Step); execErr != nil {
				return writeError(c, domain.NewErr(domain.ErrInternalError, execErr.Error()))
			}
			if req.StepAttemptID != nil {
				// Completion-row insert is best-effort; primary state change above
				// has already succeeded.
				tx.Exec(c.Request().Context(), `
					INSERT INTO wi_step_completions (id, work_item_id, run_attempt_id, step_attempt_id, step_id, status)
					VALUES ($1, $2, $3, $4, $5, 'completed')`,
					domain.NewID("sc"), wiID, req.AttemptID, *req.StepAttemptID, derefStr(currentStep)) //nolint:errcheck
			}
			eventType = "step_completed"
		case "failed":
			if _, execErr := tx.Exec(c.Request().Context(), `
				UPDATE wi_step_state
				SET current_step_status = 'idle', current_step_attempt = NULL,
				    step_started_at = NULL, version = version + 1, updated_at = clock_timestamp()
				WHERE work_item_id = $1`, wiID); execErr != nil {
				return writeError(c, domain.NewErr(domain.ErrInternalError, execErr.Error()))
			}
			if req.StepAttemptID != nil {
				// Best-effort row; failure marker has already been written above.
				tx.Exec(c.Request().Context(), `
					INSERT INTO wi_step_completions (id, work_item_id, run_attempt_id, step_attempt_id, step_id, status)
					VALUES ($1, $2, $3, $4, $5, 'failed')`,
					domain.NewID("sc"), wiID, req.AttemptID, *req.StepAttemptID, derefStr(currentStep)) //nolint:errcheck
			}
			eventType = "step_failed"
		default:
			return writeError(c, domain.NewErr(domain.ErrBadRequest, "status must be in_progress|completed|failed"))
		}

		// Emit step event inside transaction
		if eventType != "" && u != nil {
			tx.Exec(c.Request().Context(), `
				INSERT INTO agent_events
				    (id, work_item_id, run_attempt_id, actor_user_id, api_key_id, event_type, payload, project)
				VALUES ($1, $2, $3, $4, $5, $6, $7::jsonb,
				    (SELECT project FROM work_items WHERE id=$2))`,
				domain.NewID("evt"), wiID, req.AttemptID, u.UserID, u.APIKeyID, eventType,
				`{"step":"`+derefStr(req.Step)+`"}`) //nolint:errcheck
		}

		if err := tx.Commit(c.Request().Context()); err != nil {
			return writeError(c, domain.NewErr(domain.ErrInternalError, "commit step update"))
		}
		return c.JSON(http.StatusOK, map[string]string{"status": req.Status})
	}
}

func handleRenewLease(pool *pgxpool.Pool) echo.HandlerFunc {
	return func(c echo.Context) error {
		wiID := c.Param("id")
		var req struct {
			AttemptID     string `json:"attempt_id"`
			ClaimEpoch    int64  `json:"claim_epoch"`
			SessionSecret string `json:"session_secret"`
		}
		if err := c.Bind(&req); err != nil {
			return writeError(c, domain.NewErr(domain.ErrBadRequest, err.Error()))
		}
		// Verify AttemptCredential before touching last_active_at
		if credErr := domain.VerifyAttemptCredentialPool(
			c.Request().Context(), pool, wiID, req.AttemptID, req.ClaimEpoch, req.SessionSecret,
		); credErr != nil {
			return writeError(c, credErr)
		}
		// ownership-only: bump last_active_at (best-effort lease renewal).
		pool.Exec(c.Request().Context(), `
			UPDATE run_attempts SET last_active_at = clock_timestamp()
			WHERE id = $1 AND work_item_id = $2 AND claim_epoch = $3`,
			req.AttemptID, wiID, req.ClaimEpoch) //nolint:errcheck
		return c.JSON(http.StatusOK, map[string]string{"status": "renewed"})
	}
}

func handlePauseAttempt(pool *pgxpool.Pool) echo.HandlerFunc {
	return func(c echo.Context) error {
		u := GetUser(c)
		wiID := c.Param("id")
		var req domain.CompleteAttemptRequest
		if err := c.Bind(&req); err != nil {
			return writeError(c, domain.NewErr(domain.ErrBadRequest, err.Error()))
		}
		req.Status = "paused"

		wi, err := domain.GetWorkItem(c.Request().Context(), pool, wiID)
		if err != nil {
			return writeError(c, domain.NewErr(domain.ErrNotFound, "work item not found"))
		}
		if err := checkProjectAccess(c, u, wi.Project, "writer"); err != nil {
			return err
		}

		// Delegate to FnCompleteAttempt(paused) — correctly keeps locks, emits events
		if aihubErr := domain.FnCompleteAttempt(c.Request().Context(), pool, wiID, &req); aihubErr != nil {
			return writeError(c, aihubErr)
		}
		return c.JSON(http.StatusOK, map[string]string{"status": "paused"})
	}
}

func handleCutAlpha() echo.HandlerFunc {
	return func(c echo.Context) error {
		return writeError(c, domain.NewErr(domain.ErrNotImplemented, "pf_cut_alpha: Phase 2"))
	}
}

func handlePromote() echo.HandlerFunc {
	return func(c echo.Context) error {
		return writeError(c, domain.NewErr(domain.ErrNotImplemented, "pf_promote: Phase 2"))
	}
}

func derefStr(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
