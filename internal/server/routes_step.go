package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"reflect"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/labstack/echo/v4"

	"github.com/GMISWE/ieops-aihub/internal/domain"
)

// CompletedStep is one row of a work item's step history — one attempt that
// reached a terminal outcome, with the summary that attempt recorded.
//
// This is the record that used to have no read path (aihub#265). PATCH
// /v1/work_items/:id/step has written wi_step_completions since 0005, and every
// scenario step graph nevertheless opened by telling the agent to read prior
// context out of a hand-written `.pf_steps.json` in the worktree root, because
// the server offered nothing to read instead.
//
// Be precise about what "nothing writes it" means, because the loose version of
// this sentence was wrong. NO CODE PATH reads or writes it: in this repository
// its only non-prose references are a .gitignore entry, the
// writeWorktreeExcludes pattern list, and one comment. The file is produced
// entirely by natural-language instructions to an agent — in polyforge-coding's
// step templates (removed by the companion change) and in this repo's own
// plugins/polyforge/skills/pf-execute/ skill, which still tells the agent to
// read and write it. That plugin contradiction is real and is NOT fixed here;
// touching plugins/ forces a version bump, so it is reported rather than made.
//
// The load-bearing figure is the zero, not a ratio. For the record, 6 of the 258
// repo worktrees on this build host held one on 2026-09-03 — but that
// denominator counts worktrees whose work item never ran a step, and it moves as
// work items come and go. What does not move is that no code path creates it.
//
// Measured evidence that the two records really are written independently, which
// is the defect itself: for ieops#961, three sampled steps have DIFFERENT prose
// on the two sides (code_review 244 chars locally against ~640 on the server,
// each containing detail the other lacks). If the plugin's
// `read_json(".pf_steps.json")` were the source of the stored summary they would
// be byte-identical.
//
// ArtifactSummary and ErrorType are pointers WITHOUT omitempty on purpose: the
// column is nullable, and "this step recorded no summary" has to stay
// distinguishable from "this response does not carry summaries".
type CompletedStep struct {
	StepID          string    `json:"step_id"`
	Status          string    `json:"status"` // "completed" | "failed"
	ArtifactSummary *string   `json:"artifact_summary"`
	ErrorType       *string   `json:"error_type"`
	Escalated       bool      `json:"escalated"`
	RunAttemptID    *string   `json:"run_attempt_id"`
	CompletedAt     time.Time `json:"completed_at"`
}

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

	// CompletedSteps is the prior-step context a resuming agent needs in order
	// to skip work that is already done, oldest first, retries included.
	//
	// 🔴 NO omitempty, and handleGetStep never leaves it nil. Three states have
	// to stay distinguishable and omitempty collapses two of them:
	//
	//	[]      -> this work item has completed no step
	//	[...]   -> these steps are done; do not redo them
	//	absent  -> the server predates aihub#265 and cannot answer the question
	//
	// With omitempty the first and third both serialise to nothing, so a client
	// talking to an old server would read "nothing is done yet" and start over
	// from step 1 — which is the exact failure aihub#265 is about, relocated
	// from a stale file to a confident-looking server response.
	CompletedSteps []CompletedStep `json:"completed_steps"`
	// CompletedStepsTruncated discloses that completedStepsLimit was hit and the
	// OLDEST rows were dropped. A ceiling without disclosure would be a fresh
	// instance of the defect this field exists to prevent — the same argument
	// GET /v1/events makes for having no ceiling at all.
	CompletedStepsTruncated bool `json:"completed_steps_truncated"`
}

// completedStepsLimit caps the step history one response carries.
//
// It is not expected to fire: the longest step graph in polyforge-coding is 10
// steps (feature.tether.md and critical_bug.ieops.md, counted by `^## Step:`
// headings on 2026-09-03), so a work item would need 20 attempts per step to
// reach it. It is here because wi_step_completions is append-only and a client
// looping step transitions can grow it without bound, and because a cap that
// fires silently is worse than no cap — hence CompletedStepsTruncated.
const completedStepsLimit = 200

// completedStepsFetch is what the QUERY asks for: one more row than the response
// carries, so the caller can tell "exactly at the cap" from "over it".
//
// Fetching exactly completedStepsLimit is a silent defect, not an off-by-one you
// would notice: truncateCompletedSteps could then never report truncation, and a
// history that is exactly full would be served as if it were complete. Named
// here, and pinned by TestTruncateCompletedSteps, because the arithmetic is
// invisible from either side alone.
const completedStepsFetch = completedStepsLimit + 1

// truncateCompletedSteps trims a history read to the response cap and reports
// whether anything was dropped.
//
// It is a separate, pure function on purpose. The assembly of this response is
// otherwise reachable only with a database, and the DB-gated test that would
// cover it cannot land while .github/workflows/ci.yml is held by another work
// item — so the arithmetic and the empty-vs-nil handling are pulled out to where
// a table test can reach them. What that does NOT cover is that handleGetStep
// passes it the real query result; see routes_step_authority_test.go.
//
// `rows` arrives oldest-first with at most limit+1 entries, and the surplus is
// at the FRONT: loadCompletedSteps asks the database for the NEWEST limit+1 and
// flips them, so a truncated response keeps the recent history rather than the
// first 200 attempts of a runaway loop.
//
// It never returns nil. A nil slice marshals to `null`, which is a third
// spelling of "empty" on a field whose whole point is that `[]` and absent mean
// different things (see StepState.CompletedSteps).
func truncateCompletedSteps(rows []CompletedStep, limit int) ([]CompletedStep, bool) {
	if len(rows) <= limit {
		if rows == nil {
			return []CompletedStep{}, false
		}
		return rows, false
	}
	return rows[len(rows)-limit:], true
}

// scanTargets returns pointers to this row's fields in DECLARATION order, which
// is the order completedStepsQuery selects them in.
//
// It is built by reflection rather than written out, so the positional Scan
// cannot drift from the struct. A hand-written argument list is one
// transposition away from filing every step's summary under error_type and
// every error under artifact_summary — both are *string, so neither the
// compiler, nor vet, nor the driver, nor any test that only checks "a row came
// back" would notice. This removes that failure mode instead of trying to detect
// it; what remains is keeping the SQL column list in the same order, which
// TestCompletedStepsQueryMatchesStructOrder pins without a database.
//
// Cost is one reflect walk per row over at most completedStepsLimit+1 rows.
func (cs *CompletedStep) scanTargets() []any {
	v := reflect.ValueOf(cs).Elem()
	out := make([]any, v.NumField())
	for i := range out {
		out[i] = v.Field(i).Addr().Interface()
	}
	return out
}

// completedStepsQuery reads the step history for one work item.
//
// 🔴 The outer SELECT list must stay in CompletedStep's field-declaration order:
// scanTargets is positional and derives from the struct, so the SQL is the half
// that can drift. TestCompletedStepsQueryMatchesStructOrder asserts the two
// agree, and it is DB-free, so it runs on every PR.
//
// The subquery takes the NEWEST rows and the outer query flips them back to
// oldest-first: when the cap fires, an agent needs the recent history, not the
// first 200 attempts of a runaway loop. Ordering is (completed_at, id) rather
// than completed_at alone so that two rows written inside one clock_timestamp()
// tick still come back in a stable order.
const completedStepsQuery = `
	SELECT step_id, status, artifact_summary, error_type,
	       COALESCE(escalated, false), run_attempt_id, completed_at
	FROM (
		SELECT step_id, status, artifact_summary, error_type, escalated,
		       run_attempt_id, completed_at, id
		FROM wi_step_completions
		WHERE work_item_id = $1
		ORDER BY completed_at DESC, id DESC
		LIMIT $2
	) recent
	ORDER BY completed_at ASC, id ASC`

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
	v1.POST("/work_items/:id/commit_locks", handleReconcileCommitLocks(pool))
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
		switch {
		case scanErr == nil:
			// nothing to do
		case errors.Is(scanErr, pgx.ErrNoRows):
			// No row is a real answer: this work item has not started a step.
			s.CurrentStepStatus = "idle"
			s.Version = 0
		default:
			// A genuine query failure is NOT "idle, version 0". That reply is
			// indistinguishable from "nothing has started", which is precisely
			// the false-negative aihub#265 exists to remove — the code below
			// refuses to make it for the history read, and making it here would
			// leave the same lie reachable through the other query on the same
			// pool.
			return writeError(c, domain.NewErr(domain.ErrInternalError,
				"step state read failed"))
		}

		// The step history is read even when wi_step_state has no row. Those two
		// tables are written independently (wi_step_completions is append-only
		// and survives a wi_step_state reset), so keying the history read on the
		// current-state read succeeding would reintroduce a silent empty answer
		// on exactly the resume path this exists for.
		//
		// A history read failure is NOT swallowed. Returning 200 with
		// `completed_steps: []` after a failed query would tell a resuming agent
		// "nothing is done" — the same lie the stale local file told, which is
		// what makes it worth an error here rather than a best-effort skip.
		//
		// The error TEXT is deliberately not the driver's. This endpoint is open
		// to any project viewer, and pgx errors carry relation names, column
		// names and SQLSTATEs; the detail belongs in the server log, not in a
		// reply. Callers get a stable message and a 500 they cannot mistake for
		// an empty history.
		steps, histErr := loadCompletedSteps(c.Request().Context(), pool, wiID)
		if histErr != nil {
			c.Logger().Errorf("get_step: history read failed for %s: %v", wiID, histErr)
			return writeError(c, domain.NewErr(domain.ErrInternalError,
				"step history read failed"))
		}
		s.CompletedSteps, s.CompletedStepsTruncated =
			truncateCompletedSteps(steps, completedStepsLimit)
		return c.JSON(http.StatusOK, s)
	}
}

// loadCompletedSteps returns one work item's step history, oldest first, with
// one row more than completedStepsLimit when there is one so the caller can
// tell "exactly at the cap" from "over the cap".
//
// wiID must already be the canonical work_items.id. A slug reaches no rows
// here, and would come back as an empty history rather than an error — the
// resolution is handleGetStep's job (aihub#127) and this function is the reason
// it stays there.
func loadCompletedSteps(ctx context.Context, pool *pgxpool.Pool, wiID string) ([]CompletedStep, error) {
	rows, err := pool.Query(ctx, completedStepsQuery, wiID, completedStepsFetch)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]CompletedStep, 0, 16)
	for rows.Next() {
		var cs CompletedStep
		if scanErr := rows.Scan(cs.scanTargets()...); scanErr != nil {
			return nil, scanErr
		}
		out = append(out, cs)
	}
	if rows.Err() != nil {
		return nil, rows.Err()
	}
	return out, nil
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
					INSERT INTO wi_step_completions (id, work_item_id, run_attempt_id, step_attempt_id, step_id, status, artifact_summary, error_type, escalated)
					VALUES ($1, $2, $3, $4, $5, 'failed', $6, $7, $8)`,
					domain.NewID("sc"), wiID, req.AttemptID, *req.StepAttemptID, derefStr(currentStep), req.ArtifactSummary, req.ErrorType, req.Escalated); bpErr != nil {
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

// handleReconcileCommitLocks backs the commit-time lock gate (aihub#366).
//
// The access level is "writer", matching handleAcquireLocks: this call can take
// locks, so it is not a read even on the request that ends up taking none.
func handleReconcileCommitLocks(pool *pgxpool.Pool) echo.HandlerFunc {
	return func(c echo.Context) error {
		u := GetUser(c)
		wiID := c.Param("id")
		var req domain.ReconcileCommitLocksRequest
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

		resp, aihubErr := domain.FnReconcileCommitLocks(c.Request().Context(), pool, wi.ID, &req)
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
