package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
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
	// RepoPins is the CURRENT attempt's per-repo starting commit
	// ({"<repo>": "<40-char sha>"}, aihub#416). It lives on run_attempts, not on
	// wi_step_state, and is surfaced here because this is the call a resuming
	// agent already makes — the alternative was a second round-trip for a fact
	// that answers the same question the rest of this response answers ("what is
	// the state of the work I am picking up").
	//
	// ⚠️ It is provenance, not a constraint: see domain.ClaimRequest.RepoPins.
	// Nothing checks a worktree against it, and it does not expire.
	//
	// Absent when the work item has no attempt, when the attempt recorded none
	// (no worktree was built), and on a row written before migration 0037. Those
	// three are deliberately NOT distinguished here — all of them mean "no
	// recorded starting commit", and inventing three spellings of that would
	// invite a caller to branch on which one it got.
	RepoPins map[string]string `json:"repo_pins,omitempty"`

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

// maxArtifactSummaryChars is the cap wi_step_completions puts on
// artifact_summary: `CHECK (length(artifact_summary) <= 4096)` in
// internal/db/migrations/0005_step_state.sql. Postgres length() counts
// characters, not bytes, so the check in handleUpdateStep counts runes.
//
// It is named here, in the handler, because of aihub#390. The handler used to
// let the INSERT discover the cap and then swallowed the CHECK violation inside
// a savepoint — so a 4,243-character summary bumped version, emitted a
// step_completed event carrying the full text (agent_events has no such cap),
// answered 200, and left completed_steps one step short with
// completed_steps_truncated=false. TestArtifactSummaryCapMatchesTheMigration
// pins this constant to the migration's number, so the two cannot drift apart
// without a DB-free test going red.
const maxArtifactSummaryChars = 4096

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
//
// `outcome` (map[string]any) was the mirror image and is gone (aihub#424): bound
// here, read by nothing in this package, and published by no tool — so it could
// only ever be filled in by a caller that had no way to send it, and would have
// been discarded if one had. Do not re-add it without a reader; aihub#419's G4
// arm reports any field of this struct that no tool can reach.
type UpdateStepRequest struct {
	AttemptID       string  `json:"attempt_id"`
	ClaimEpoch      int64   `json:"claim_epoch"`
	SessionSecret   string  `json:"session_secret"`
	Status          string  `json:"status"` // "in_progress" | "completed" | "failed"
	Step            *string `json:"step,omitempty"`
	StepAttemptID   *string `json:"step_attempt_id,omitempty"`
	Heartbeat       bool    `json:"heartbeat,omitempty"`
	ArtifactSummary *string `json:"artifact_summary,omitempty"`
	ErrorType       *string `json:"error_type,omitempty"`
	Escalated       bool    `json:"escalated,omitempty"`

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
	v1.POST("/work_items/:id/repo_pins", handleRecordRepoPins(pool))
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
			return writeError(c, hideNotFound(err))
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

		// aihub#416: the current attempt's repo pins, read separately because they
		// live on run_attempts while everything above lives on wi_step_state.
		//
		// Best-effort by design, and it is the one read in this handler that is:
		// the three reads above refuse to answer "idle / nothing done" on a query
		// failure because that answer is indistinguishable from a true one and
		// would make a resuming agent redo finished work. A missing pin cannot
		// mislead in that direction — it is already the honest value for a claim
		// that built no worktree — so failing the whole request over it would
		// trade a real capability for a fact that is advisory by construction.
		var pinsRaw []byte
		if err := pool.QueryRow(c.Request().Context(), `
			SELECT ra.repo_pins
			FROM work_items wi
			JOIN run_attempts ra ON ra.id = wi.current_attempt_id
			WHERE wi.id = $1`, wiID,
		).Scan(&pinsRaw); err == nil && len(pinsRaw) > 0 {
			var pins map[string]string
			if json.Unmarshal(pinsRaw, &pins) == nil {
				s.RepoPins = pins
			}
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
			return writeError(c, hideNotFound(err))
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
			// aihub#442. The bump used to be `_, _ = pool.Exec(...)` — the only
			// discarded error in this handler — under the rationale "transient DB
			// errors must not fail the heartbeat (caller will retry anyway)". That
			// rationale is circular: a caller retries what it is TOLD failed, and
			// this was the one branch that told it nothing. The question the work
			// item posed is "check the error, or state in the code that nothing
			// depends on the write". Something does, so it is checked:
			//
			//   - step_recovery_hint, recomputed on every claim of an
			//     already-claimed wi on BOTH paths (domain.FnClaimWorkItem and its
			//     idempotent twin, internal/domain/run_attempts.go). It reports
			//     `active_in_progress_conflict` when current_step_status is
			//     'in_progress' AND step_started_at is younger than 15s, and
			//     `crashed_in_progress` otherwise. A bump that fails in silence
			//     therefore makes a LIVE agent read as crashed to whoever takes
			//     the wi over.
			//   - GET /v1/work_items/:id/step publishes step_started_at verbatim
			//     (StepStartedAt above, and pf_get_step's card), where a stale
			//     value is simply a wrong answer.
			//
			// Two limits on that, written down so the next reader does not overrate
			// the write. _common/lifecycle.md teaches one heartbeat per ~5 min, so
			// the 15s window is open for 15 of every 300 seconds and the hint reads
			// `crashed_in_progress` most of the time no matter what this write
			// does; and the 60s watchdog the design doc describes for the same
			// column (`step_agent_unresponsive`, design §3.3 and §21.7) was never
			// built — the event name occurs in no Go file. So this is a WEAK input
			// to a real consumer, which is still not the same thing as an input to
			// nothing.
			//
			// RETURNING answers all three questions in one round trip: did the
			// statement fail, was there a row to bump at all, and which step got
			// refreshed.
			var bumpedStep *string
			bumpErr := pool.QueryRow(c.Request().Context(), `
				UPDATE wi_step_state SET step_started_at = clock_timestamp(), updated_at = clock_timestamp()
				WHERE work_item_id = $1
				RETURNING current_step`, wiID).Scan(&bumpedStep)
			if bumpErr != nil && !errors.Is(bumpErr, pgx.ErrNoRows) {
				return writeError(c, domain.NewErr(domain.ErrInternalError, fmt.Sprintf(
					"heartbeat could not refresh step_started_at: %v; nothing was recorded", bumpErr)))
			}
			// ErrNoRows is NOT that error: it means the wi has no wi_step_state row,
			// so the statement ran fine and bumped nothing. It stays a 200 for two
			// reasons — the caller cannot act on it (the row is created by claim,
			// whose upsert failure is itself non-fatal by design, see the
			// "Non-fatal but log" arm of FnClaimWorkItem), and a 4xx/5xx on this
			// endpoint means "your request was not honoured because of something
			// you can fix". But answering a bare "heartbeat_ok" for it is the same
			// silence this work item exists to remove, so the outcome is stated in
			// the body instead, and stated on EVERY heartbeat rather than only on
			// the false one: a key that appears only when something is wrong turns
			// its own absence into a claim that nothing is, which is a claim an
			// older server would also be making.
			resp := map[string]any{
				"status":                    "heartbeat_ok",
				"step_started_at_refreshed": bumpErr == nil,
			}
			// The other half of aihub#442: the branch returns HERE, so a step_id or
			// status sent in the same request goes nowhere.
			//
			// It is disclosed rather than rejected, and that is a decision with
			// evidence on both sides of it:
			//
			//   - rejecting it would break the taught producer. lifecycle.md says
			//     "add heartbeat=true to an in_progress call every ~5 min", and the
			//     measured corpus agrees: of 1,998 pf_update_step calls across
			//     2,222 transcripts, 50 carry heartbeat=true, and 49 of those carry
			//     status="in_progress" plus a step_id (the 50th carries neither).
			//   - heartbeat + status="completed" answering 200 heartbeat_ok is
			//     PINNED as intended, by three tests in this package that each send
			//     that exact combination and assert it
			//     (routes_step_outcome_records_db_test.go,
			//     routes_step_identity_db_test.go and
			//     routes_step_history_row_db_test.go), and by aihub#398's owner
			//     decision, which chose to DOCUMENT the drop rather than change it.
			//     mcp/tools_step_contract_test.go pins the SCHEMA SENTENCE that
			//     describes the drop, which is a different guarantee — it would
			//     survive a change of behaviour.
			//
			// So the remaining honest option is the one aihub#314 built for exactly
			// this shape — request_adjusted, "what the server DID to a value it
			// HELD": the caller sent it, the server applied nothing.
			//
			// ⚠️ This can only fire for a DIRECT HTTP caller. pf_update_step's own
			// heartbeat branch sends a credentials-only body, so from an MCP caller
			// step_id/status never arrive and there is nothing here to disclose —
			// which is also why no matching disclosure was added at that hop: the
			// only shape it could usefully report there, heartbeat plus a TERMINAL
			// status, occurs ZERO times in the corpus above, and the schema sentence
			// aihub#398 landed ("Returns early and DISCARDS step_id/status") already
			// carries it to the caller that does hold the values.
			var dropped []domain.RequestAdjustment
			if req.Step != nil && *req.Step != "" {
				// Applied is the step actually refreshed, not nil: the bump keys on
				// work_item_id alone, so naming a step that is not the open one
				// refreshes the open one regardless — which is worth telling the
				// caller, and costs nothing now that RETURNING has the value.
				dropped = append(dropped, domain.RequestAdjustment{
					Param: "step_id", Requested: *req.Step, Applied: bumpedStep,
				})
			}
			if req.Status != "" {
				dropped = append(dropped, domain.RequestAdjustment{
					Param: "status", Requested: req.Status, Applied: nil,
				})
			}
			if len(dropped) > 0 {
				resp["request_adjusted"] = dropped
			}
			return c.JSON(http.StatusOK, resp)
		}

		// aihub#390: artifact_summary is persisted by the completed and failed
		// branches into wi_step_completions, whose CHECK caps it at
		// maxArtifactSummaryChars. Reject an oversize value HERE, before any
		// write, naming the cap and the actual length — the alternative, letting
		// the INSERT trip the CHECK inside the transaction, is how the same
		// request used to answer 200 with the step missing from the history.
		// Placed AFTER the heartbeat return above on purpose: a heartbeat may
		// carry status="completed" (see validateNextStepArgs) and must stay
		// untouched by this. in_progress ignores the field, so an oversize value
		// there stays as ignored as it was. The check keys on the status and the
		// field alone, NOT on step_attempt_id: a completed/failed request without
		// one files no row today (aihub#403 owns that shape), and the cap is a
		// property of the field on this transition either way, so it is applied
		// uniformly rather than encoding "no step_attempt_id means no row" in a
		// second place.
		if (req.Status == "completed" || req.Status == "failed") && req.ArtifactSummary != nil {
			if n := utf8.RuneCountInString(*req.ArtifactSummary); n > maxArtifactSummaryChars {
				return writeError(c, domain.NewErr(domain.ErrPayloadTooLarge, fmt.Sprintf(
					"artifact_summary is %d characters; the step history stores at most %d; shorten it and resend, nothing was recorded",
					n, maxArtifactSummaryChars)))
			}
		}

		// aihub#399: step_attempt_id is REQUIRED on a terminal transition, and is
		// now enforced rather than merely published. Sits here, beside the cap
		// check and after the heartbeat return, for the same reason that one does:
		// a heartbeat is selected by its flag and may legitimately carry
		// status="completed" while completing no step.
		if aerr := validateTerminalStepArgs(req.Status, req.StepAttemptID, req.Heartbeat); aerr != nil {
			return writeError(c, aerr)
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

		// aihub#398, step 1 of two: a terminal transition must name the step the
		// server currently has open. Before aihub#398 the row filed below was
		// keyed on derefStr(currentStep), so a completed/failed request naming
		// any other step recorded the WRONG step as finished and then overwrote
		// current_step with the caller's value. The same change re-keyed the row
		// on req.Step (see the completed branch below), so the history row now
		// names the caller's step by construction; what this predicate owns is
		// refusing a request whose named step contradicts the open one, before
		// anything is written.
		//
		// It must sit here, and not one line either side:
		//
		//   - AFTER the currentStep read, because that read is the stored value
		//     the predicate compares the request against — there is nothing to
		//     check before it exists. It is the comparison operand ONLY:
		//     insertStepCompletion files the row under req.Step, not under this
		//     read (see the completed branch below).
		//   - BEFORE the switch, so completed and failed cannot drift, and so it
		//     precedes the completed branch's mandatory-record gate — a request
		//     about the wrong step should be told that, not told which artifact
		//     the wrong step is missing.
		//   - AFTER the heartbeat return above, for the same reason
		//     validateTerminalStepArgs and aihub#390's cap check are: a heartbeat
		//     is selected by its own flag and may legitimately carry
		//     status="completed" while completing no step.
		//
		// What it deliberately does NOT check is current_step_status. Requiring
		// 'in_progress' is a SEPARATE predicate and a separate work item: measured
		// over 21 days of transcripts, ~17% of completed calls (118/698, an upper
		// bound — cross-session resumes are counted) have no prior in_progress for
		// that (wi, step) in the same transcript, so requiring it today would
		// refuse real pf-execute flows. The consequence is stated rather than
		// hidden: an idle step whose name still matches can be completed twice
		// with two different step_attempt_ids, and both rows are filed. That is
		// what the state predicate closes, and
		// TestHandleUpdateStep_DoubleCompleteIsCaughtByWhicheverGuardApplies pins
		// it so the boundary is a measured fact rather than a comment.
		if aerr := validateStepIdentity(req.Status, req.Heartbeat, req.Step, currentStep); aerr != nil {
			return writeError(c, aerr)
		}

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
			// Unconditional since aihub#399. The `if req.StepAttemptID != nil`
			// this replaces was the whole defect: it turned "no step_attempt_id"
			// into "file no history row", silently, on a request that still
			// bumped version, still emitted step_completed and still answered
			// 200. validateTerminalStepArgs above has already refused a nil or
			// blank id, so the guard is not moved here — it is gone, because a
			// leftover `!= nil` would be dead code able to hide the same defect
			// again if that check were ever relaxed.
			// stepID comes from req.Step, NOT from the currentStep read above
			// (aihub#398). validateStepIdentity has already refused every request
			// where the two could differ AND a step was open, so in the normal
			// case this is the same value — but where NO step was open the old
			// source was derefStr(nil), i.e. the row was filed under the empty
			// string. Reading the caller's own value makes "the history row names
			// the step the caller said it finished" true by construction rather
			// than true because a predicate happened to run first, which is the
			// same reason aihub#399 deleted its `!= nil` guard instead of moving
			// it: a guarantee that depends on a check placed elsewhere breaks
			// silently when that check is relaxed.
			if aerr := insertStepCompletion(c.Request().Context(), tx, wiID, req.AttemptID, derefStr(req.StepAttemptID),
				derefStr(req.Step), "completed", req.ArtifactSummary, nil, false); aerr != nil {
				return writeError(c, aerr)
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
						"completed, but next_step could not be started (another actor holds the step); nothing was committed, retry"))
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
			// Unconditional since aihub#399 — see the completed branch above.
			// req.Step, not currentStep — see the completed branch above (aihub#398).
			if aerr := insertStepCompletion(c.Request().Context(), tx, wiID, req.AttemptID, derefStr(req.StepAttemptID),
				derefStr(req.Step), "failed", req.ArtifactSummary, req.ErrorType, req.Escalated); aerr != nil {
				return writeError(c, aerr)
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

		// Emit the step events inside the transaction, and NOT best-effort
		// (aihub#399). This loop used to wrap each INSERT in its own
		// `SAVEPOINT bp` and discard the error on ROLLBACK, on the same theory
		// aihub#390 removed from the history row: that the state change was the
		// primary write and the record of it a courtesy. It is the mirror image
		// of that defect. aihub#390 made completed_steps strict and recorded in
		// its own notes that the events side could still fail silently — so the
		// invariant "completed_steps equals the attempt's step_completed /
		// step_failed events" stayed breakable from the other direction: history
		// row written, timeline entry swallowed, 200 answered, and pf_read_events
		// then under-reporting what pf_get_step claims.
		//
		// So a 200 now means BOTH records landed, and a transition that cannot
		// write its event is refused with nothing committed. The accepted cost is
		// the symmetric one: an agent_events problem that used to pass unnoticed
		// now fails step transitions. That is the same trade as the history row,
		// and it is bounded — 0031 added the DEFAULT partition precisely so that
		// routing can no longer be the thing that fails.
		//
		// u cannot be nil here: checkProjectAccess above answers 401 for an
		// unauthenticated request, so this branch is unreachable and has NO gate
		// — it is stated as an assertion, not claimed to be tested. It is a
		// refusal rather than a skip because "emit no timeline at all" is exactly
		// the outcome this change exists to remove.
		if u == nil {
			return writeError(c, domain.NewErr(domain.ErrInternalError,
				"no authenticated actor to attribute the step timeline to; nothing was committed"))
		}
		for _, ev := range events {
			if aerr := insertStepEvent(c.Request().Context(), tx, wiID, req.AttemptID, u, ev); aerr != nil {
				return writeError(c, aerr)
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

// validateTerminalStepArgs enforces what the pf_update_step schema has claimed
// since aihub#265 and the server never checked: a completed or failed
// transition must carry a step_attempt_id.
//
// It is the (a) half of aihub#399 (filed twice — aihub#403 is the same finding
// from aihub#390's session). The history INSERT used to be reached only
// `if req.StepAttemptID != nil`, so omitting the field did not fail the
// request, it removed the record: wi_step_state advanced, version bumped, the
// step_completed / step_failed event was emitted with the full summary, the
// call answered 200 — and pf_get_step's completed_steps, which the tool tells a
// resuming agent to treat as the full done-set, was one step short with
// completed_steps_truncated=false. A resumer then redoes a finished step. The
// MCP layer made this reachable without any malformed input:
// internal/mcp/tools_step.go's updateStepBody forwards the key only when
// non-empty, so an agent that simply omits the argument sends no key at all.
//
// Blank is rejected as well as absent, and it is the WORSE of the two: "" is
// non-nil, so it used to reach the INSERT and file a row keyed on the empty
// string. wi_step_completions.step_attempt_id is NOT NULL with a GLOBAL unique
// index (idx_wsc_attempt), so the first blank id was accepted and the SECOND
// one — from an unrelated work item, in an unrelated attempt — came back 409
// "already has a step-history row" naming an id its caller never chose.
// Whitespace-only is treated as blank for the same reason; it is not an id
// either.
//
// Heartbeats are exempt, and that exemption is the subtle part rather than a
// convenience: `heartbeat` is selected by its own flag, not by the status, so a
// heartbeat may legitimately carry status="completed", and it returns early
// having completed no step and written no history row. Requiring an id there
// would reject a valid liveness ping. handleUpdateStep therefore calls this
// AFTER the heartbeat return — the same position, for the same reason, as
// aihub#390's artifact_summary cap check.
//
// in_progress is exempt because it files no history row at all; a bare start is
// how pf-execute's loop opens a step graph.
//
// Pure and DB-free so TestValidateTerminalStepArgs can hold every combination
// without a database.
func validateTerminalStepArgs(status string, stepAttemptID *string, heartbeat bool) *domain.AihubError {
	if heartbeat || (status != "completed" && status != "failed") {
		return nil
	}
	const why = ": the step-history row that pf_get_step's completed_steps is read from is keyed on it, so the " +
		"transition could only be recorded in the timeline and never in the history, which is the silent " +
		"disagreement that made a resuming agent redo a finished step. Nothing was committed; resend with the " +
		"step_attempt_id used to start the step"
	if stepAttemptID == nil {
		return domain.NewErr(domain.ErrBadRequest,
			`status="`+status+`" requires step_attempt_id and none was sent`+why)
	}
	if strings.TrimSpace(*stepAttemptID) == "" {
		return domain.NewErr(domain.ErrBadRequest,
			`status="`+status+`" requires step_attempt_id and a blank one is not an id`+why+
				" (a blank id is not merely useless: it is filed under the empty string, and the global unique "+
				"index then answers 409 on the next such request about an id nobody chose)")
	}
	return nil
}

// validateStepIdentity refuses a completed/failed transition that names a step
// other than the one wi_step_state currently has open (aihub#398, step 1 of
// two).
//
// The defect it closes is a history corruption, not a race. handleUpdateStep
// USED TO file the wi_step_completions row under the STORED current_step rather
// than under req.Step — and the tense is load-bearing, because this change fixes
// that too (see the call site), so a comment left in the present tense would
// describe code that no longer exists. A completion naming a different step
// recorded the stored step as finished, emitted a step_completed event naming
// the CALLER's step, and then set current_step to the caller's value: three
// records disagreeing, and a 200. pf_get_step's completed_steps — which
// aihub#265 tells a resuming agent to treat as the full done-set — then reported
// a step that never finished while omitting the one that did, so a resumer
// skipped real work and redid finished work in the same walk.
//
// With the row keyed on req.Step, the misfiling is gone whether or not this
// predicate runs. What is left for it to prevent is the rest of that list:
// recording a step as finished while a DIFFERENT one is what actually ran, and
// overwriting current_step so the running step's own outcome is never recorded.
//
// This is reachable without any malformed input. The interactive loop's own
// documented "skip" path (the polyforge plugin's
// pf-execute/references/engine-native-details.md) tells the agent to skip a step
// by calling NO pf_update_step at all, leaving the skipped step as current_step,
// and asserts that "the next step you actually complete reports itself and
// advances from there". That is exactly the shape above, and the assertion is
// false: the next completion is filed under the skipped step. After this change
// it is a 409 instead, which is why the message spells out the recovery.
//
// Why the comparison is sound without locking the row. The value compared here
// and the value the row is filed under are the SAME read, in the same
// transaction, so a concurrent transition between the read and the UPDATE cannot
// separate them: whatever this accepted is what gets written. A stale read can
// still let the UPDATE overwrite a newer state — but that is a state question,
// and the state predicate is the other work item. Adding `FOR UPDATE` here would
// buy that protection and change the in_progress path from "refuse immediately"
// to "block, then refuse", which is a concurrency change this work item is not
// making.
//
// Exact byte comparison, deliberately: no trimming, no case folding. startStep
// stores the step name verbatim and insertStepCompletion files it verbatim, so
// " implement" really is a different stored step from "implement", and
// normalising here would make the predicate accept a request whose row then gets
// filed under the other name — the very disagreement it exists to prevent.
//
// Heartbeats and in_progress are exempt for the reasons given at the call site.
//
// 🔴 On the error code, and on why the reason recorded here is no longer the
// reason. CONFLICT_STEP_ATTEMPT_MISMATCH is declared, 409-mapped and named for
// this endpoint in docs/design/polyforge-v1-design.md, and when aihub#398 wrote
// this comment using it WOULD have been a bug: the MCP layer's
// classifyStepUpdateErr matched the server's code by SUBSTRING of the rendered
// "aihub <status> <CODE>: <message>", and its mismatch arm tested for
// "ATTEMPT_MISMATCH" — a substring of that longer code — so naming the wrong
// step would have deleted the caller's local state file. CONFLICT_CAS_FAILED
// was chosen because it contains none of those triggers.
//
// aihub#414 fixed the parsing bug: that classifier now compares the code field
// exactly, and pkg/client returns a typed *client.APIError instead of a
// pre-rendered string. So the constraint this paragraph describes is GONE, and
// nothing here is load-bearing against it any more. Do not cite it as a reason
// to avoid a code.
//
// CONFLICT_CAS_FAILED nevertheless stays, on its own merits rather than by
// inheritance: it is what the sibling idle predicate on this endpoint already
// returns, so one endpoint answers one code for "your precondition about the
// open step did not hold", and it is what the landed clients and tests of that
// endpoint expect. Changing it is a contract change, and it needs its own work
// item and its own gate — not a drive-by rename justified by a constraint that
// no longer exists.
//
// Pure and DB-free so TestValidateStepIdentity can hold every combination
// without a database; the caller supplies the stored value.
func validateStepIdentity(status string, heartbeat bool, reqStep, storedStep *string) *domain.AihubError {
	if heartbeat || (status != "completed" && status != "failed") {
		return nil
	}
	want := derefStr(reqStep)

	// An absent step_id and an explicit "" are the same fact to this predicate:
	// neither identifies a step. They are distinguished only in the WORDING, and
	// only because the recovery differs — "start \"\" first" is not advice. The
	// comparison itself does not special-case them, so a stored "" still matches
	// a request that sent nothing (see TestValidateStepIdentity); refusing an
	// empty step_id outright is a different predicate and not smuggled in here.
	named := `status="` + status + `" names step ` + fmt.Sprintf("%q", want)
	howToRecover := "start " + fmt.Sprintf("%q", want) + ` with status="in_progress" first; note that a start ` +
		"is refused while another step is in_progress, so the open step has to reach a terminal status either way"
	if want == "" {
		named = `status="` + status + `" identifies no step (step_id absent or empty)`
		howToRecover = "resend with step_id naming the step that finished"
	}

	// 🔴 No stored step means there is NOTHING to contradict, so this passes.
	//
	// That is the opposite of the first implementation, and the reason is the
	// scope line between this work item and the state predicate. A terminal
	// transition on a work item with no wi_step_state row is a work item with no
	// prior in_progress — verbatim the population the state predicate was
	// deferred over (~17% of measured completed calls), so refusing it here
	// would ship half of step 2 while claiming to ship step 1. Measured, not
	// argued: refusing it turned three landed tests red
	// (TestHandleUpdateStep_ArtifactSummary, _EscalatedStall,
	// _MandatoryRecordGate), each of which completes a step it never started —
	// evidence that the shape is live in this codebase and not hypothetical.
	//
	// Nothing is given up by passing, because the row is no longer filed from
	// this value: handleUpdateStep files it under req.Step (see the call site),
	// so "no step open" now records the step the caller named instead of the
	// empty string it used to record. The corruption is closed by where the
	// row's name COMES FROM; this predicate's remaining job is narrower and
	// exact — stop a caller ending a step that is not the open one.
	if storedStep == nil {
		return nil
	}
	if want == *storedStep {
		return nil
	}
	// What this says has to be what the request would do to the code AS IT IS,
	// not what the old code did. "the row would be keyed on the server's value"
	// was true of the handler this change replaced and is no longer a
	// consequence of anything — the row is keyed on req.Step now — so claiming
	// it here would be a refusal explaining itself with a stale reason, which is
	// exactly how a correct verdict comes to rest on a rotten premise. The harm
	// that remains is the state half.
	return domain.NewErr(domain.ErrConflictCASFailed, named+
		", but this work item's current_step is "+fmt.Sprintf("%q", *storedStep)+
		". Ending a step other than the open one would record "+fmt.Sprintf("%q", want)+
		" as finished while "+fmt.Sprintf("%q", *storedStep)+" is what actually ran, and would overwrite "+
		"current_step, so "+fmt.Sprintf("%q", *storedStep)+"'s own outcome would never be recorded and "+
		"pf_get_step's completed_steps would disagree with what ran, which is what makes a resuming agent skip "+
		"real work. Nothing was committed. Either finish "+fmt.Sprintf("%q", *storedStep)+
		" (the step that is actually open), or "+howToRecover)
}

// stepEvent is one agent_events row a step transition owes the timeline.
type stepEvent struct {
	eventType       string
	step            string
	artifactSummary *string
}

// insertStepEvent appends one step_started / step_completed / step_failed row to
// agent_events inside the caller's transaction. Its errors are the request's
// errors — see the call site in handleUpdateStep for why this stopped being
// best-effort (aihub#399).
//
// runAttemptID is sent as SQL NULL when empty, and that is a fix rather than a
// tidy-up. agent_events.run_attempt_id is a nullable FK to run_attempts
// (migration 0006 keeps it nullable for events that belong to no attempt),
// while ” names no attempt — so the handler, which passes a non-pointer
// string, made EVERY step transition PATCHed without an attempt_id violate
// agent_events_run_attempt_id_fkey. Under the old savepoint that violation was
// swallowed and the event vanished; with errors now reaching the caller it
// would instead turn a shape that answered 200 into a 400. Neither is right:
// "this event belongs to no attempt" is a fact the column can hold, so it is
// recorded. Measured on PostgreSQL 18.6 with this repo's migrations:
// run_attempt_id=” -> 23503, run_attempt_id=NULL -> accepted.
//
// The error mapping:
//
//   - 23503 is the run_attempt_id foreign key with a NON-empty attempt_id, i.e.
//     the caller named an attempt that does not exist. The credential check in
//     handleUpdateStep is skipped when session_secret is empty, so nothing
//     before this notices. 400, naming the field — the same answer
//     insertStepCompletion gives the same FK.
//   - 22P05 is the measured one: `step` is copied into the JSON verbatim and
//     the jsonb cast rejects a NUL byte ("unsupported Unicode escape sequence
//     ... cannot be converted to text"). That is the caller's own string, so
//     400. Note the completed branch escapes this by accident — its own
//     `UPDATE ... current_step = $2` fails first — which is why the failed
//     branch is where the gate exercises it. 22021 is listed alongside it as
//     the same class raised by a text column rather than by the cast; it is
//     NOT measured here and no input is known to reach it through this
//     statement, since every text parameter comes from a resolved row or the
//     authenticated user.
//   - anything else is 500. work_item_id was resolved from a real row and
//     actor_user_id comes from the authenticated user, so nothing else here is
//     caller-driven.
func insertStepEvent(ctx context.Context, tx pgx.Tx, wiID, runAttemptID string, u *UserContext, ev stepEvent) *domain.AihubError {
	// json.Marshal of a map rather than manual escaping: fmt.Sprintf(%q) is fine
	// for a single string but does not compose safely once a second field is
	// added.
	payloadMap := map[string]any{"step": ev.step}
	if ev.eventType == "step_completed" && ev.artifactSummary != nil {
		payloadMap["artifact_summary"] = *ev.artifactSummary
	}
	payload, mErr := json.Marshal(payloadMap)
	if mErr != nil {
		return domain.NewErr(domain.ErrInternalError, "marshal "+ev.eventType+" payload")
	}

	var runAttempt any
	if runAttemptID != "" {
		runAttempt = runAttemptID
	}
	_, err := tx.Exec(ctx, `
		INSERT INTO agent_events
		    (id, work_item_id, run_attempt_id, actor_user_id, api_key_id, event_type, payload, project)
		VALUES ($1, $2, $3, $4, $5, $6, $7::jsonb,
		    (SELECT project FROM work_items WHERE id=$2))`,
		domain.NewID("evt"), wiID, runAttempt, u.UserID, u.APIKeyID, ev.eventType, payload)
	if err == nil {
		return nil
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		switch pgErr.Code {
		case "23503":
			return domain.NewErr(domain.ErrBadRequest, fmt.Sprintf(
				"attempt_id %q does not name an existing run attempt, so the %s event cannot be filed against it; "+
					"the step timeline is part of what a 200 promises; nothing was committed",
				runAttemptID, ev.eventType))
		case "22P05", "22021":
			return domain.NewErr(domain.ErrBadRequest, fmt.Sprintf(
				"the %s event could not be recorded: step %q carries a character the event payload cannot store "+
					"(SQLSTATE %s); nothing was committed",
				ev.eventType, ev.step, pgErr.Code))
		}
	}
	return domain.NewErr(domain.ErrInternalError, "record "+ev.eventType+" event")
}

// insertStepCompletion appends the wi_step_completions row for a step that just
// reached a terminal outcome, inside the caller's transaction. Its errors are
// the request's errors: the row commits with the state change or neither does.
//
// It replaces two copies of a "best-effort" INSERT that ran inside a SAVEPOINT
// whose ROLLBACK swallowed every error, on the theory that the wi_step_state
// UPDATE was the primary write and the history row a courtesy. aihub#265
// inverted that theory without touching this code: pf_get_step's
// completed_steps is now THE record a resuming agent is told to trust, so a
// swallowed INSERT produced a request that bumped version, emitted a
// step_completed event carrying the summary, answered 200 — and left the step
// out of the history with completed_steps_truncated=false. Measured on
// aihub#383 (aihub#390): a 4,243-character artifact_summary tripped the
// migration's CHECK (length <= 4096); 7 step_completed events, 6 history rows.
//
//   - 23505 (idx_wsc_attempt, the GLOBAL unique index on step_attempt_id) means
//     this step attempt already has its row, i.e. the same completion was
//     submitted twice. That is answered 409 with nothing committed, so a retry
//     after a lost response is loud instead of a silent second no-op that
//     advanced the state machine again.
//   - 23503 is the run_attempt_id foreign key — work_item_id was resolved from
//     a real row, so it is the only FK that can fail — and it fails when the
//     request carried no attempt_id: the credential check above is skipped for
//     an empty attempt_id, so nothing before this INSERT notices. That is the
//     caller's request, answered 400 and naming the field.
//   - anything else is 500 with a fixed message. The artifact_summary CHECK is
//     pre-validated in handleUpdateStep, so reaching it here means the constant
//     and the migration disagree — which TestArtifactSummaryCapMatchesTheMigration
//     exists to catch first.
func insertStepCompletion(ctx context.Context, tx pgx.Tx, wiID, runAttemptID, stepAttemptID, stepID, status string,
	artifactSummary, errorType *string, escalated bool,
) *domain.AihubError {
	_, err := tx.Exec(ctx, `
		INSERT INTO wi_step_completions (id, work_item_id, run_attempt_id, step_attempt_id, step_id, status, artifact_summary, error_type, escalated)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)`,
		domain.NewID("sc"), wiID, runAttemptID, stepAttemptID, stepID, status, artifactSummary, errorType, escalated)
	if err == nil {
		return nil
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		switch pgErr.Code {
		case "23505":
			return domain.NewErr(domain.ErrConflictDuplicate,
				"step_attempt_id "+stepAttemptID+" already has a step-history row; this step attempt was recorded before; nothing was committed, do not resend")
		case "23503":
			return domain.NewErr(domain.ErrBadRequest,
				"attempt_id "+fmt.Sprintf("%q", runAttemptID)+" does not name an existing run attempt; the step history records which attempt "+
					"finished the step, so a completed/failed transition with a step_attempt_id needs the real attempt_id; nothing was committed")
		}
	}
	return domain.NewErr(domain.ErrInternalError, "record step history")
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
			return writeError(c, hideNotFound(err))
		}
		if err := checkProjectAccess(c, u, wi.Project, "writer"); err != nil {
			return err
		}

		// Delegate to FnCompleteAttempt(paused) — correctly keeps locks, emits events.
		// Pass the resolved canonical id (wiID may be a slug). (aihub#127)
		// nil/"" visibility: derived filed: resolution (aihub#350) runs only on
		// status=wrapped, which this route forces away one line up, so no filed:
		// ref is ever resolved here and nothing needs the caller's roles.
		if aihubErr := domain.FnCompleteAttempt(c.Request().Context(), pool, wi.ID, &req, nil, ""); aihubErr != nil {
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
			return writeError(c, hideNotFound(err))
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
			return writeError(c, hideNotFound(err))
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

// handleRecordRepoPins backs POST /v1/work_items/:id/repo_pins (aihub#416).
//
// "writer", matching handleAcquireLocks and handleReconcileCommitLocks: it
// writes to the attempt row, so it is not a read. The attempt credential inside
// FnRecordRepoPins is the real gate — project writer access says you may act on
// this work item, the credential says you are the attempt whose provenance this
// is.
func handleRecordRepoPins(pool *pgxpool.Pool) echo.HandlerFunc {
	return func(c echo.Context) error {
		u := GetUser(c)
		var req domain.RecordRepoPinsRequest
		if err := c.Bind(&req); err != nil {
			return writeError(c, domain.NewErr(domain.ErrBadRequest, err.Error()))
		}

		wi, err := domain.GetWorkItem(c.Request().Context(), pool, c.Param("id"))
		if err != nil {
			return writeError(c, hideNotFound(err))
		}
		if err := checkProjectAccess(c, u, wi.Project, "writer"); err != nil {
			return err
		}

		// The canonical id, never c.Param("id"): that may be a slug, and
		// FnRecordRepoPins locks work_items by id (aihub#127).
		if aihubErr := domain.FnRecordRepoPins(c.Request().Context(), pool, wi.ID, &req); aihubErr != nil {
			return writeError(c, aihubErr)
		}
		return c.JSON(http.StatusOK, map[string]bool{"ok": true})
	}
}
