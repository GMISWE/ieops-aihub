package server

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"
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
	ScenarioRef        *string    `json:"scenario_ref,omitempty"`
}

// UpdateStepRequest is the body for PATCH /v1/work_items/:id/step.
//
// Note on what is deliberately NOT here: there is no `expected_version`. The MCP
// layer used to publish and forward one, but this struct never had the field, so
// echo's Bind dropped it and no CAS was ever performed — 92 of the 126 measured
// get_step -> update_step pairs paid a whole round-trip for a version number that
// was discarded on arrival (aihub#290). The real concurrency guard is the
// `WHERE current_step_status = 'idle'` predicate on the in_progress transition,
// which needs no client-supplied version. The parameter has been removed from the
// MCP schema rather than implemented; if optimistic locking is ever wanted here,
// it has to be added to THIS struct first or it will be dropped again.
type UpdateStepRequest struct {
	AttemptID       string         `json:"attempt_id"`
	ClaimEpoch      int64          `json:"claim_epoch"`
	SessionSecret   string         `json:"session_secret"`
	Status          string         `json:"status"` // "in_progress" | "completed" | "failed"
	Step            *string        `json:"step,omitempty"`
	StepAttemptID   *string        `json:"step_attempt_id,omitempty"`
	Outcome         map[string]any `json:"outcome,omitempty"`
	Heartbeat       bool           `json:"heartbeat,omitempty"`
	ArtifactSummary *string        `json:"artifact_summary,omitempty"`
	ErrorType       *string        `json:"error_type,omitempty"`
	Escalated       bool           `json:"escalated,omitempty"`

	// NextStep fuses "this step is done" and "the next one has started" into one
	// request (aihub#290). A step-graph walk brackets every step with a completed
	// call followed immediately by an in_progress call for the successor, and the
	// second one reads nothing out of the first one's response — 350 measured
	// adjacent pairs, 0.358% of billed input, spent entirely on the round-trip.
	//
	// Only meaningful with Status=="completed"; sending it with any other status
	// is rejected rather than ignored, because a silently-dropped parameter is the
	// exact defect this work item exists to remove.
	NextStep *string `json:"next_step,omitempty"`
	// NextStepAttemptID is the client-generated attempt id for the step being
	// STARTED. It is separate from StepAttemptID, which identifies the attempt
	// being COMPLETED — one request now carries both, and conflating them would
	// file the completion history row under the successor's id.
	NextStepAttemptID *string `json:"next_step_attempt_id,omitempty"`
}

// RegisterStepRoutes adds step / release / attempt lifecycle routes.
func RegisterStepRoutes(v1 *echo.Group, pool *pgxpool.Pool) {
	v1.GET("/work_items/:id/step", handleGetStep(pool))
	v1.PATCH("/work_items/:id/step", handleUpdateStep(pool))
	v1.PATCH("/work_items/:id/renew", handleRenewLease(pool))
	v1.POST("/work_items/:id/pause", handlePauseAttempt(pool))
	v1.POST("/work_items/:id/acquire_locks", handleAcquireLocks(pool))
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
		// Resolve slug -> canonical work_items.id so wi_step_state lookups key
		// correctly (a slug returns no rows -> always idle). (aihub#127)
		wiID = wi.ID

		var s StepState
		s.WorkItemID = wiID
		scanErr := pool.QueryRow(c.Request().Context(), `
			SELECT wi_type, current_step, current_step_status,
			       current_step_attempt, step_started_at, version, scenario_ref
			FROM wi_step_state WHERE work_item_id = $1`, wiID,
		).Scan(&s.WIType, &s.CurrentStep, &s.CurrentStepStatus,
			&s.CurrentStepAttempt, &s.StepStartedAt, &s.Version, &s.ScenarioRef)
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
		// Resolve slug -> canonical work_items.id so the credential check and every
		// wi_step_state read/write key on work_items.id, not the raw slug (which
		// violates the wi_step_state.work_item_id FK on INSERT). (aihub#127)
		wiID = wi.ID

		// N3: verify AttemptCredential — session_secret must match the active attempt
		if req.AttemptID != "" && req.SessionSecret != "" {
			if credErr := domain.VerifyAttemptCredentialPool(
				c.Request().Context(), pool, wiID,
				req.AttemptID, req.ClaimEpoch, req.SessionSecret,
			); credErr != nil {
				return writeError(c, credErr)
			}
		}

		// The fused-advance arguments are validated together, before anything
		// acts on them, and every combination that cannot be honoured is REJECTED
		// rather than ignored. This endpoint's own history — the dropped
		// expected_version (aihub#290) — is the argument for never accepting a
		// parameter we are not going to act on.
		if aerr := validateNextStepArgs(derefStr(req.NextStep), derefStr(req.NextStepAttemptID),
			req.Status, req.Heartbeat); aerr != nil {
			return writeError(c, aerr)
		}

		if req.Heartbeat {
			// Heartbeat: best-effort timestamp bump, transient DB errors must not
			// fail the heartbeat (caller will retry anyway).
			_, _ = pool.Exec(c.Request().Context(), `
				UPDATE wi_step_state SET step_started_at = clock_timestamp(), updated_at = clock_timestamp()
				WHERE work_item_id = $1`, wiID)
			return c.JSON(http.StatusOK, map[string]string{"status": "heartbeat_ok"})
		}

		// All step transitions run in a single transaction for atomicity
		tx, txErr := pool.Begin(c.Request().Context())
		if txErr != nil {
			return writeError(c, domain.NewErr(domain.ErrInternalError, "begin tx"))
		}
		defer tx.Rollback(c.Request().Context()) //nolint:errcheck

		// Events accumulate rather than being a single value: a fused
		// completed+next_step request emits BOTH step_completed and step_started,
		// so that one call leaves exactly the timeline two calls would have.
		var events []stepEvent
		// Read current step name for step_completions
		var currentStep *string
		tx.QueryRow(c.Request().Context(), `SELECT current_step FROM wi_step_state WHERE work_item_id=$1`, wiID).Scan(&currentStep) //nolint:errcheck

		switch req.Status {
		case "in_progress":
			// H-Medium: guard idle→in_progress only; reject if already in_progress
			started, execErr := startStep(c.Request().Context(), tx, wiID, req.Step, req.StepAttemptID)
			if execErr != nil {
				return writeError(c, domain.NewErr(domain.ErrInternalError, execErr.Error()))
			}
			if !started {
				return writeError(c, domain.NewErr(domain.ErrConflictCASFailed, "step already in_progress; cannot start again until completed or failed"))
			}
			events = append(events, stepEvent{eventType: "step_started", step: derefStr(req.Step)})
		case "completed":
			// Mandatory-record gate (aihub#221): spec/plan steps cannot complete
			// without the corresponding methodology.* artifact already recorded.
			// Existence-only check — never reads content — so it stays cheap and
			// does not route through scanMemory.
			if req.Step != nil && (*req.Step == "spec" || *req.Step == "plan") {
				requiredType := "methodology." + *req.Step
				var exists bool
				if qErr := tx.QueryRow(c.Request().Context(), `
					SELECT EXISTS(SELECT 1 FROM memories WHERE work_item_id=$1 AND type=$2 AND status='active')`,
					wiID, requiredType).Scan(&exists); qErr != nil {
					return writeError(c, domain.NewErr(domain.ErrInternalError, qErr.Error()))
				}
				if !exists {
					return writeError(c, domain.NewErr(domain.ErrBadRequest,
						*req.Step+" step cannot complete without a "+requiredType+" artifact; record it first"))
				}
			}
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
				// has already succeeded. SAVEPOINT isolates this insert so that a
				// unique-constraint violation (e.g. duplicate step_attempt_id) does
				// not abort the surrounding transaction.
				tx.Exec(c.Request().Context(), `SAVEPOINT bp`) //nolint:errcheck
				if _, bpErr := tx.Exec(c.Request().Context(), `
					INSERT INTO wi_step_completions (id, work_item_id, run_attempt_id, step_attempt_id, step_id, status, artifact_summary)
					VALUES ($1, $2, $3, $4, $5, 'completed', $6)`,
					domain.NewID("sc"), wiID, req.AttemptID, *req.StepAttemptID, derefStr(currentStep), req.ArtifactSummary); bpErr != nil {
					tx.Exec(c.Request().Context(), `ROLLBACK TO SAVEPOINT bp`) //nolint:errcheck
				} else {
					tx.Exec(c.Request().Context(), `RELEASE SAVEPOINT bp`) //nolint:errcheck
				}
			}
			events = append(events, stepEvent{
				eventType:       "step_completed",
				step:            derefStr(req.Step),
				artifactSummary: req.ArtifactSummary,
			})

			// Fused advance (aihub#290): start the successor in the SAME
			// transaction.
			//
			// In the normal case the idle guard inside startStep cannot fail: the
			// UPDATE above matched the row, set current_step_status='idle' and
			// holds its lock for the rest of this transaction, so startStep reads
			// our own uncommitted 'idle' and no concurrent writer can get between
			// the two. If no wi_step_state row exists at all, the UPDATE matches
			// nothing and takes no lock, but then startStep's INSERT half fires and
			// still reports success.
			//
			// So `!started` means the row exists and is not idle — reachable only
			// by losing a race in the no-row case, where a concurrent writer
			// inserted an in_progress row between the two statements. Rolling the
			// whole request back there is the one place fused and split behave
			// differently (split would have committed the completion and failed
			// only the second call); reporting a completion whose successor
			// silently never started would be worse, and a retry is safe.
			if req.NextStep != nil && *req.NextStep != "" {
				started, execErr := startStep(c.Request().Context(), tx, wiID, req.NextStep, req.NextStepAttemptID)
				if execErr != nil {
					return writeError(c, domain.NewErr(domain.ErrInternalError, execErr.Error()))
				}
				if !started {
					return writeError(c, domain.NewErr(domain.ErrConflictCASFailed,
						"completed, but next_step could not be started (another actor holds the step) — nothing was committed; retry"))
				}
				events = append(events, stepEvent{eventType: "step_started", step: *req.NextStep})
			}
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
				// SAVEPOINT isolates this insert so that a unique-constraint violation
				// does not abort the surrounding transaction.
				tx.Exec(c.Request().Context(), `SAVEPOINT bp`) //nolint:errcheck
				if _, bpErr := tx.Exec(c.Request().Context(), `
					INSERT INTO wi_step_completions (id, work_item_id, run_attempt_id, step_attempt_id, step_id, status, artifact_summary, error_type)
					VALUES ($1, $2, $3, $4, $5, 'failed', $6, $7)`,
					domain.NewID("sc"), wiID, req.AttemptID, *req.StepAttemptID, derefStr(currentStep), req.ArtifactSummary, req.ErrorType); bpErr != nil {
					tx.Exec(c.Request().Context(), `ROLLBACK TO SAVEPOINT bp`) //nolint:errcheck
				} else {
					tx.Exec(c.Request().Context(), `RELEASE SAVEPOINT bp`) //nolint:errcheck
				}
			}
			events = append(events, stepEvent{eventType: "step_failed", step: derefStr(req.Step)})

			// Escalated stall (spec A-1): an escalated failure means the agent gave up
			// and a human must triage — it is NOT the same kind of "blocked" as a
			// dependency block. Dependency-blocked wis have a wi_dependencies row and
			// RunUnblockDependentWI() auto-requeues them once their blockers reach a
			// terminal status (see gc.go). Stalled-blocked wis have NO dependency row,
			// so they are excluded from that dependency-unblock GC sweep and stay
			// blocked for human triage via the ready-queue "stalled" segment. This
			// distinction is deliberate design (spec A-1), not a bug.
			//
			// The status='blocked' UPDATE and the wi_stalled event must be atomic:
			// the stalled segment requires BOTH (it JOINs wi.status='blocked' to a
			// wi_stalled event), so a wi that is blocked with no event would vanish
			// from both the queued and stalled segments. Gate the whole block on
			// u != nil and wrap both writes in ONE savepoint — they commit or roll
			// back together.
			if req.Escalated && u != nil {
				tx.Exec(c.Request().Context(), `SAVEPOINT bp`) //nolint:errcheck
				_, upErr := tx.Exec(c.Request().Context(), `
					UPDATE work_items SET status='blocked' WHERE id=$1`, wiID)
				var evErr error
				if upErr == nil {
					stallPayload, _ := json.Marshal(map[string]any{
						"step":         derefStr(req.Step),
						"error_type":   req.ErrorType,
						"stall_reason": derefStr(req.ErrorType),
					})
					_, evErr = tx.Exec(c.Request().Context(), `
						INSERT INTO agent_events
						    (id, work_item_id, run_attempt_id, actor_user_id, api_key_id, event_type, payload, project)
						VALUES ($1, $2, $3, $4, $5, 'wi_stalled', $6::jsonb,
						    (SELECT project FROM work_items WHERE id=$2))`,
						domain.NewID("evt"), wiID, req.AttemptID, u.UserID, u.APIKeyID, stallPayload)
				}
				if upErr != nil || evErr != nil {
					tx.Exec(c.Request().Context(), `ROLLBACK TO SAVEPOINT bp`) //nolint:errcheck
				} else {
					tx.Exec(c.Request().Context(), `RELEASE SAVEPOINT bp`) //nolint:errcheck
				}
			}
		default:
			return writeError(c, domain.NewErr(domain.ErrBadRequest, "status must be in_progress|completed|failed"))
		}

		// Emit step events inside transaction — best-effort; SAVEPOINT ensures a
		// failed insert (e.g. FK violation on run_attempt_id) does not abort the
		// main transaction. JSON payload uses json.Marshal of a map to avoid
		// manual-escaping bugs (fmt.Sprintf(%q) is fine for a single string but
		// does not compose safely once a second field is added). Each event gets
		// its OWN savepoint, so one bad insert cannot take the other down with it.
		if u != nil {
			for _, ev := range events {
				evtPayloadMap := map[string]any{"step": ev.step}
				if ev.eventType == "step_completed" && ev.artifactSummary != nil {
					evtPayloadMap["artifact_summary"] = *ev.artifactSummary
				}
				evtPayload, _ := json.Marshal(evtPayloadMap)

				tx.Exec(c.Request().Context(), `SAVEPOINT bp`) //nolint:errcheck
				if _, bpErr := tx.Exec(c.Request().Context(), `
					INSERT INTO agent_events
					    (id, work_item_id, run_attempt_id, actor_user_id, api_key_id, event_type, payload, project)
					VALUES ($1, $2, $3, $4, $5, $6, $7::jsonb,
					    (SELECT project FROM work_items WHERE id=$2))`,
					domain.NewID("evt"), wiID, req.AttemptID, u.UserID, u.APIKeyID, ev.eventType,
					evtPayload); bpErr != nil {
					tx.Exec(c.Request().Context(), `ROLLBACK TO SAVEPOINT bp`) //nolint:errcheck
				} else {
					tx.Exec(c.Request().Context(), `RELEASE SAVEPOINT bp`) //nolint:errcheck
				}
			}
		}

		if err := tx.Commit(c.Request().Context()); err != nil {
			return writeError(c, domain.NewErr(domain.ErrInternalError, "commit step update"))
		}
		resp := map[string]any{"status": req.Status}
		// Name the successor in the response. A fused call is the only place the
		// caller cannot infer the resulting current_step from its own request, and
		// the whole point of the fusion is that it will not make a second call to
		// go and look.
		if req.NextStep != nil && *req.NextStep != "" {
			resp["next_step"] = *req.NextStep
			resp["next_step_status"] = "in_progress"
		}
		return c.JSON(http.StatusOK, resp)
	}
}

// validateNextStepArgs rejects every combination of the fused-advance arguments
// that this endpoint cannot honour. It returns nil when there is nothing to
// object to, including when neither argument was sent.
//
// There are three ways to get this wrong, and all three end the same way — the
// caller is told the request succeeded while the successor never starts, which
// is precisely the silent-drop defect aihub#290 exists to remove:
//
//   - next_step with a non-completed status: there is no completion for the
//     successor to follow.
//   - next_step on a HEARTBEAT. This is the one that is easy to miss, because a
//     heartbeat may legitimately carry status="completed" — it is selected by
//     the `heartbeat` flag, not by the status — and it returns early after
//     touching only step_started_at. A status check alone therefore lets a
//     heartbeat+next_step request through to be answered "heartbeat_ok" with
//     next_step discarded.
//   - next_step_attempt_id WITHOUT next_step: it identifies a step that is not
//     being started, so nothing would ever read it.
//
// Shared with the MCP layer's equivalent check (internal/mcp/tools_step.go) in
// intent but not in code — the two layers bind different types, and the MCP one
// exists to fail before the call costs a round-trip, not to be the authority.
// This function is the authority.
func validateNextStepArgs(nextStep, nextStepAttemptID, status string, heartbeat bool) *domain.AihubError {
	if nextStep != "" {
		if heartbeat {
			return domain.NewErr(domain.ErrBadRequest,
				"next_step cannot be combined with heartbeat=true: a heartbeat only refreshes step_started_at and completes no step, so the successor would never be started")
		}
		if status != "completed" {
			return domain.NewErr(domain.ErrBadRequest,
				`next_step is only valid with status="completed" (it starts the successor of the step being completed)`)
		}
		return nil
	}
	if nextStepAttemptID != "" {
		return domain.NewErr(domain.ErrBadRequest,
			"next_step_attempt_id was sent without next_step; it names the attempt of the step being STARTED, so with no next_step there is nothing for it to identify (use step_attempt_id for the step being completed)")
	}
	return nil
}

// stepEvent is one agent_events row a step transition owes the timeline.
type stepEvent struct {
	eventType       string
	step            string
	artifactSummary *string
}

// startStep performs the idle -> in_progress transition, reporting whether it
// took. Shared by the plain in_progress request and the fused completed+next_step
// one (aihub#290) so the two cannot drift on the guard that IS the concurrency
// control for this table: `WHERE current_step_status = 'idle'` is what stops two
// agents running the same step, and it is the reason no client-supplied version
// number is needed.
//
// A false return means the guard rejected the transition (the step was already
// in_progress), not that an error occurred.
func startStep(ctx context.Context, tx pgx.Tx, wiID string, step, stepAttemptID *string) (bool, error) {
	tag, err := tx.Exec(ctx, `
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
		wiID, step, stepAttemptID)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() > 0, nil
}

func handleRenewLease(pool *pgxpool.Pool) echo.HandlerFunc {
	return func(c echo.Context) error {
		return c.JSON(http.StatusGone, map[string]string{
			"error": "pf_renew_lease removed: claim is permanent ownership",
		})
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

		// Delegate to FnCompleteAttempt(paused) — correctly keeps locks, emits events.
		// Pass the resolved canonical id (wiID may be a slug). (aihub#127)
		if aihubErr := domain.FnCompleteAttempt(c.Request().Context(), pool, wi.ID, &req); aihubErr != nil {
			return writeError(c, aihubErr)
		}
		return c.JSON(http.StatusOK, map[string]string{"status": "paused"})
	}
}

func handleAcquireLocks(pool *pgxpool.Pool) echo.HandlerFunc {
	return func(c echo.Context) error {
		u := GetUser(c)
		wiID := c.Param("id")
		var req domain.AcquireLocksRequest
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

		resp, aihubErr := domain.FnAcquireLocks(c.Request().Context(), pool, wi.ID, &req)
		if aihubErr != nil {
			return writeError(c, aihubErr)
		}
		return c.JSON(http.StatusOK, resp)
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
