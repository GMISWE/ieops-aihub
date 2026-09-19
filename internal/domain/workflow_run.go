package domain

// workflow_run.go — the WI workflow service half (aihub#708 Batch 2A): the
// revision transaction, the controller-minted step invocations, the fenced
// result recording, the human approval and the repair authorization.
//
// Where the guarantees live, in the order a request meets them:
//
//	GET /workflow         project-read access (the caller already sees the WI);
//	                      discloses references and progress, never skill content.
//	PUT /workflow         edit-tier "contract" gate (reporter/maintainer/admin,
//	                      open-status only — no revision while a worker is in
//	                      progress), expected_steps_version CAS, atomic
//	                      resolve→validate→pin in ONE transaction, append-only
//	                      generations.
//	POST /workflow/start  the CURRENT attempt's credentials (id + epoch +
//	                      secret), schema/access re-validated against the
//	                      registry at start time, server-minted step attempt and
//	                      producer identities, ordering + repair-role rules +
//	                      the human-approval gate (effective WI rhs AND step
//	                      rhs: every gated step's LATEST artifact must carry a
//	                      human approval before a successor invocation mints).
//	POST /workflow/result the CURRENT attempt's credentials + the result must
//	                      echo an OPEN invocation's exact identity (generation,
//	                      step, step attempt, producer, epoch), and a
//	                      COMPLETED result's artifact triple is resolved to the
//	                      actual methodology artifact it names — a memories
//	                      row of type methodology.*, bound to THIS work item,
//	                      at its immutable per-id version, whose stored
//	                      structured output hashes to the claimed digest and
//	                      is readable by the recording attempt's actor;
//	                      immutable pinned output schema (even when registry
//	                      sharing is revoked after invocation authorization: the
//	                      server validates internally without exposing content);
//	                      nonexistent / wrong-work-item / wrong-digest /
//	                      schema-invalid artifacts all refuse, nothing
//	                      recorded). Results persist
//	                      transactionally with the invocation close and the
//	                      timeline events; a review FAIL PAUSES the attempt
//	                      (never terminal). Workflow results are step history
//	                      in wi_workflow_results ONLY — nothing is copied into
//	                      the legacy wi_step_completions, whose readers filter
//	                      by work_item_id alone and would read a superseded
//	                      generation's rows as current step history.
//	POST /workflow/approve authenticated HUMAN only; binds the exact artifact
//	                      triple of the step's latest recorded result; the actor
//	                      is the authenticated principal, never a body field.
//	                      Serialized on the work item row, with the DB identity
//	                      unique on the artifact EXCLUDING the decision, so no
//	                      two transactions can record contradictory decisions
//	                      for one artifact.
//	POST /workflow/repair the CURRENT attempt's credentials; opens a bounded
//	                      retry or repair-episode authorization that later
//	                      starts bind to, mirrored against the pure
//	                      RepairEpisode policy rules at bind time. A retry
//	                      opened over an episode role invocation that recorded
//	                      provider_error carries the IMMUTABLE PARENT LINEAGE
//	                      (parent_episode_id): its replacement invocations
//	                      stand in for that role in the episode's EFFECTIVE
//	                      role set — the latest successful authorized
//	                      replacement — while the failed history rows stay,
//	                      no invocation is rebound, and the retry chain is
//	                      bounded (maxRetryChainDepth), never unbounded.
//	POST /workflow/reconcile the CURRENT attempt's credentials; the EXPLICIT
//	                      controller-reconcile transition (aihub#708 B3): it
//	                      supersedes a dead attempt's OPEN invocations — ordinary
//	                      and episode-bound alike — never silently and never on
//	                      trust: the named attempt's own row is read under the
//	                      work item lock and must already be paused, superseded
//	                      or ended. Superseded invocations are refused results
//	                      (stale), stop counting toward an episode's role set
//	                      and its binding bounds, and free the step for a
//	                      replacement invocation by the live attempt.
//
// Lifecycle fencing (aihub#708 B2): every attempt-credential route — start,
// result, repair, reconcile — takes the work item row FOR UPDATE as its FIRST
// statement and holds it through the mutation, the same order FnClaimWorkItem,
// FnCompleteAttempt, FnAcquireLocks and FnRecordRepoPins already use. The
// credential check and the write are therefore one atomic window: a pause or
// takeover that commits between them is impossible, because whichever
// transaction takes the row second waits, re-reads the moved row, and answers
// the refusal. A request that loses that race leaves NO invocation, result or
// authorization behind — its transaction never reaches a write.
//
// The DECISION itself (advance/wait/pause over the whole result history) is
// assembled from these tables by the controller (Batch 2B) through the pure
// policy — internal/workflow.Decide. This file is the store and the fence: it
// records, it refuses what must be refused, and it pauses on FAIL.

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/GMISWE/ieops-aihub/internal/skillregistry"
	wf "github.com/GMISWE/ieops-aihub/internal/workflow"
)

// ─── Shared row helpers ──────────────────────────────────────────────────────

// workflowPointer is the workflow half of a work-item read: the pinned steps
// JSON and the current generation number. It travels beside the *WorkItem the
// shared struct already defines, so every downstream bind uses wi.ID off a
// *WorkItem — the canonical-by-construction shape the slug census recognizes —
// rather than a second struct's field.
type workflowPointer struct {
	Steps        []byte
	StepsVersion int
}

// getWorkflowWIOnTx loads the work item (as a *WorkItem, resolved id-or-slug)
// plus its workflow pointer, optionally FOR UPDATE (the revision, approval and
// every attempt-credential path locks the row — aihub#708 B2's lifecycle
// fence; only the read view does not).
func getWorkflowWIOnTx(ctx context.Context, tx pgx.Tx, idOrSlug string, forUpdate bool) (*WorkItem, *workflowPointer, *AihubError) {
	q := `SELECT id, project, status, reporter_user_id, requires_human_session, steps, steps_version,
	             current_attempt_id, current_attempt_epoch
	      FROM work_items WHERE id = $1 OR slug = $1`
	if forUpdate {
		q += ` FOR UPDATE`
	}
	var wi WorkItem
	var steps []byte
	var stepsVersion int
	err := tx.QueryRow(ctx, q, idOrSlug).Scan(
		&wi.ID, &wi.Project, &wi.Status, &wi.ReporterUserID, &wi.RequiresHumanSession, &steps, &stepsVersion,
		&wi.CurrentAttemptID, &wi.CurrentAttemptEpoch)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil, NewErr(ErrNotFound, "work item not found")
	}
	if err != nil {
		if forUpdate {
			if aerr := retryConflictErr(err, "lock work item for workflow"); aerr != nil {
				return nil, nil, aerr
			}
		}
		return nil, nil, dbErrCause(err, "load work item for workflow")
	}
	return &wi, &workflowPointer{Steps: steps, StepsVersion: stepsVersion}, nil
}

// getWorkflowWI is the pool twin of getWorkflowWIOnTx for the read-only view.
func getWorkflowWI(ctx context.Context, pool *pgxpool.Pool, idOrSlug string) (*WorkItem, *workflowPointer, *AihubError) {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return nil, nil, NewErr(ErrInternalError, "begin workflow read tx")
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	return getWorkflowWIOnTx(ctx, tx, idOrSlug, false)
}

// attemptActor loads the acting user of an attempt for event attribution.
func attemptActor(ctx context.Context, tx pgx.Tx, attemptID string) (string, string, *AihubError) {
	var userID, display string
	err := tx.QueryRow(ctx,
		`SELECT actor_user_id, actor_display FROM run_attempts WHERE id=$1`, attemptID).Scan(&userID, &display)
	if err != nil {
		return "", "", dbErrCause(err, "read attempt actor")
	}
	return userID, display, nil
}

// insertWorkflowEvent writes one server-emitted timeline event on a
// transaction. Its failure is returned, never swallowed — the same rule
// aihub#399 imposed on the legacy step events (an event that silently fails
// leaves the timeline under-reporting what the history rows claim).
func insertWorkflowEvent(ctx context.Context, tx pgx.Tx, wiID, project, actorUserID, actorDisplay, eventType string, payload map[string]any) *AihubError {
	payloadJSON, err := json.Marshal(payload)
	if err != nil {
		return NewErr(ErrInternalError, "workflow event does not serialize")
	}
	var display *string
	if actorDisplay != "" {
		display = &actorDisplay
	}
	_, err = tx.Exec(ctx, `
		INSERT INTO agent_events (id, work_item_id, actor_user_id, actor_display, event_type, payload, project)
		VALUES ($1, $2, $3, $4, $5, $6, $7)`,
		NewID("evt"), wiID, actorUserID, display, eventType, payloadJSON, project)
	if err != nil {
		return dbErrCause(err, "emit "+eventType)
	}
	return nil
}

// truncateRunes bounds s to max runes without splitting a rune — a byte slice
// would store invalid UTF-8 in a TEXT column and fail the INSERT. It had one
// caller, the legacy wi_step_completions copy in RecordWorkflowResult, which
// the isolation fix removed (aihub#708 review blocker: legacy readers must not
// see workflow rows); if a workflow path ever needs to bound a stored string
// again, reintroduce it there rather than truncating by bytes.

// ─── Read: the workflow view ─────────────────────────────────────────────────

// WorkflowGenerationSummary is one generation in the append-only history.
type WorkflowGenerationSummary struct {
	StepsVersion         int       `json:"steps_version"`
	RequiresHumanSession bool      `json:"requires_human_session"`
	CreatedBy            string    `json:"created_by"`
	CreatedAt            time.Time `json:"created_at"`
	Digest               string    `json:"digest"`
}

// WorkflowStepProgress is the per-step state of the CURRENT generation, as a
// controller or a minimal flow UI reads it.
type WorkflowStepProgress struct {
	StepID string `json:"step_id"`
	// Status/ReviewVerdict/StepAttemptID/Artifact describe the LATEST recorded
	// result for the step in the current generation; Status is empty when the
	// step has no result yet.
	Status        string          `json:"status,omitempty"`
	ReviewVerdict string          `json:"review_verdict,omitempty"`
	StepAttemptID string          `json:"step_attempt_id,omitempty"`
	Artifact      json.RawMessage `json:"artifact,omitempty"`
	// OpenInvocationID is non-empty while a controller-minted invocation for
	// this step has no recorded result yet.
	OpenInvocationID string `json:"open_invocation_id,omitempty"`
	// Approved is tri-state: nil = no decision applies to the latest result's
	// artifact; true = an authenticated human approved exactly it; false = a
	// human rejected exactly it.
	Approved *bool `json:"approved,omitempty"`
	// RHS is the step's effective human gate: WI requires_human_session AND
	// step rhs (step omitted defaults false — spec D5).
	RHS bool `json:"rhs"`
}

// WorkflowRepairSummary is one repair authorization.
type WorkflowRepairSummary struct {
	ID                  string    `json:"id"`
	Kind                string    `json:"kind"`
	FailedStepsVersion  int       `json:"failed_steps_version"`
	FailedStepID        string    `json:"failed_step_id"`
	FailedStepAttemptID string    `json:"failed_step_attempt_id"`
	Status              string    `json:"status"`
	CreatedAt           time.Time `json:"created_at"`
	// ParentEpisodeID is the immutable parent lineage: non-empty on retries
	// that descend from an episode role invocation (an episode role that
	// recorded provider_error); empty on episodes and on retries of ordinary
	// invocations.
	ParentEpisodeID string `json:"parent_episode_id,omitempty"`
}

// WorkItemWorkflow is the GET /v1/work_items/:id/workflow response.
type WorkItemWorkflow struct {
	WorkItemID   string `json:"work_item_id"`
	StepsVersion int    `json:"steps_version"`
	// Steps is the CURRENT generation's pinned flow, or null for a legacy
	// no-workflow work item. References and params only — never skill content.
	Steps                json.RawMessage             `json:"steps"`
	RequiresHumanSession *bool                       `json:"requires_human_session"`
	Generations          []WorkflowGenerationSummary `json:"generations"`
	Progress             []WorkflowStepProgress      `json:"progress"`
	Repairs              []WorkflowRepairSummary     `json:"repairs"`
}

// GetWorkItemWorkflow reads a work item's workflow state. Authorization is the
// caller's project-read access to the work item (the route applies it); this
// function is a pure read of frozen state — it never touches the registry, so
// a workflow view stays readable even after a share is revoked (an
// already-pinned flow keeps its references; only NEW starts and revisions
// re-check access, per spec D3).
func GetWorkItemWorkflow(ctx context.Context, pool *pgxpool.Pool, idOrSlug string) (*WorkItemWorkflow, *AihubError) {
	wi, wp, aerr := getWorkflowWI(ctx, pool, idOrSlug)
	if aerr != nil {
		return nil, aerr
	}
	out := &WorkItemWorkflow{
		WorkItemID:           wi.ID,
		StepsVersion:         wp.StepsVersion,
		RequiresHumanSession: wi.RequiresHumanSession,
		Generations:          []WorkflowGenerationSummary{},
		Progress:             []WorkflowStepProgress{},
		Repairs:              []WorkflowRepairSummary{},
	}
	if wp.StepsVersion == 0 {
		return out, nil
	}
	if len(wp.Steps) > 0 {
		out.Steps = json.RawMessage(wp.Steps)
	}

	genRows, err := pool.Query(ctx, `
		SELECT steps_version, requires_human_session, created_by, created_at, validation_digest
		FROM wi_workflow_generations WHERE work_item_id = $1 ORDER BY steps_version`, wi.ID)
	if err != nil {
		return nil, dbErrCause(err, "read workflow generations")
	}
	defer genRows.Close()
	for genRows.Next() {
		var g WorkflowGenerationSummary
		if err := genRows.Scan(&g.StepsVersion, &g.RequiresHumanSession, &g.CreatedBy, &g.CreatedAt, &g.Digest); err != nil {
			return nil, dbErrCause(err, "read workflow generations")
		}
		out.Generations = append(out.Generations, g)
	}
	if err := genRows.Err(); err != nil {
		return nil, dbErrCause(err, "read workflow generations")
	}

	var flow wf.Flow
	if len(out.Steps) > 0 {
		if err := json.Unmarshal(out.Steps, &flow); err != nil {
			return nil, NewErr(ErrInternalError, "stored workflow flow does not parse")
		}
	}
	wiRHS := wi.RequiresHumanSession != nil && *wi.RequiresHumanSession
	for _, step := range flow.Steps {
		p := WorkflowStepProgress{StepID: step.ID, RHS: wiRHS && step.RHS != nil && *step.RHS}

		var status, verdict, attempt string
		var artifact []byte
		err := pool.QueryRow(ctx, `
			SELECT status, COALESCE(review_verdict,''), step_attempt_id, artifact FROM (
				SELECT status, review_verdict, step_attempt_id, artifact
				FROM wi_workflow_results
				WHERE work_item_id = $1 AND steps_version = $2 AND step_id = $3
				ORDER BY created_at DESC, id DESC LIMIT 1
			) latest`, wi.ID, out.StepsVersion, step.ID,
		).Scan(&status, &verdict, &attempt, &artifact)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return nil, dbErrCause(err, "read workflow progress")
		}
		// ErrNoRows is an ordinary answer here (the step has no recorded
		// result yet); only a real failure is classified. The fields below
		// stay zero-valued on the empty answer.
		if err == nil {
			p.Status, p.ReviewVerdict, p.StepAttemptID = status, verdict, attempt
			p.Artifact = json.RawMessage(artifact)
		}

		if err := pool.QueryRow(ctx, `
			SELECT id FROM wi_workflow_invocations
			WHERE work_item_id = $1 AND steps_version = $2 AND step_id = $3 AND status = 'open'
			ORDER BY created_at DESC LIMIT 1`, wi.ID, out.StepsVersion, step.ID,
		).Scan(&p.OpenInvocationID); err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return nil, dbErrCause(err, "read workflow progress")
		}

		if p.StepAttemptID != "" {
			var decision string
			err := pool.QueryRow(ctx, `
				SELECT decision FROM wi_workflow_approvals
				WHERE work_item_id = $1 AND steps_version = $2 AND step_id = $3
				  AND artifact_id = $4::jsonb->>'id'
				  AND artifact_version = ($4::jsonb->>'version')::int
				  AND artifact_hash = $4::jsonb->>'hash'
				ORDER BY created_at DESC LIMIT 1`,
				wi.ID, out.StepsVersion, step.ID, p.Artifact).Scan(&decision)
			if err != nil && !errors.Is(err, pgx.ErrNoRows) {
				return nil, dbErrCause(err, "read workflow progress")
			}
			// ErrNoRows is an ordinary answer here (no decision for this
			// exact artifact); only a real failure is classified, and
			// p.Approved stays nil on the empty answer.
			if err == nil {
				approved := decision == "approved"
				p.Approved = &approved
			}
		}
		out.Progress = append(out.Progress, p)
	}

	repRows, err := pool.Query(ctx, `
		SELECT id, kind, failed_steps_version, failed_step_id, failed_step_attempt_id, status, created_at,
		       COALESCE(parent_episode_id,'')
		FROM wi_workflow_repair_episodes WHERE work_item_id = $1 ORDER BY created_at`, wi.ID)
	if err != nil {
		return nil, dbErrCause(err, "read workflow repairs")
	}
	defer repRows.Close()
	for repRows.Next() {
		var r WorkflowRepairSummary
		if err := repRows.Scan(&r.ID, &r.Kind, &r.FailedStepsVersion, &r.FailedStepID, &r.FailedStepAttemptID, &r.Status, &r.CreatedAt, &r.ParentEpisodeID); err != nil {
			return nil, dbErrCause(err, "read workflow repairs")
		}
		out.Repairs = append(out.Repairs, r)
	}
	if err := repRows.Err(); err != nil {
		return nil, dbErrCause(err, "read workflow repairs")
	}
	return out, nil
}

// ─── Revision (PUT) ──────────────────────────────────────────────────────────

// UpdateWorkItemWorkflowRequest is the PUT /v1/work_items/:id/workflow body.
type UpdateWorkItemWorkflowRequest struct {
	// ExpectedStepsVersion is the CAS token: the steps_version the caller last
	// saw. 0 = "this work item has no workflow yet".
	ExpectedStepsVersion int `json:"expected_steps_version"`
	// RequiresHumanSession must be EXPLICIT (spec D5): a workflow revision
	// classifies the work item. nil is refused, not defaulted.
	RequiresHumanSession *bool              `json:"requires_human_session"`
	Steps                []WorkflowStepSpec `json:"steps"`
}

// UpdateWorkItemWorkflow revises (or first pins) a work item's workflow.
// The whole operation is ONE transaction: gate, CAS, resolve, validate,
// insert generation, move the pointer, emit event. Any refusal leaves the
// work item exactly as it was — an invalid composition can never leave a
// half-pinned workflow behind (spec D4).
func UpdateWorkItemWorkflow(ctx context.Context, pool *pgxpool.Pool, idOrSlug string,
	caller *UserRecord, callerUserID, callerRole string, callerProjectRoles map[string]string,
	req UpdateWorkItemWorkflowRequest) (*WorkItemWorkflow, *AihubError) {

	if req.RequiresHumanSession == nil {
		return nil, NewErr(ErrBadRequest,
			"requires_human_session is required on a workflow revision: a workflow-bearing work item needs an explicit human-session classification")
	}
	if len(req.Steps) == 0 {
		return nil, NewErr(ErrBadRequest, "steps must name at least one step")
	}

	tx, err := pool.Begin(ctx)
	if err != nil {
		return nil, NewErr(ErrInternalError, "begin workflow revision tx")
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	// Lock the work item row: the CAS compares under the lock, and the lock is
	// what serializes two concurrent revisions, the same way publication
	// serializes on the skills row.
	w, wp, aerr := getWorkflowWIOnTx(ctx, tx, idOrSlug, true)
	if aerr != nil {
		return nil, aerr
	}

	// Contract-tier gate, deliberately the SAME matrix the goal/wi_type edits
	// use: live status (a worker in progress) refuses with
	// CONFLICT_WI_ALREADY_CLAIMED, terminal with CONFLICT_TERMINAL_STATE, and
	// the caller must be the reporter, a project maintainer or an admin.
	if aerr := updateGate(w.Status, wiTierContract, "steps", w.ReporterUserID == callerUserID,
		callerRole, callerProjectRoles[w.Project]); aerr != nil {
		return nil, aerr
	}

	// A legacy scenario graph and a workflow are two authorities over the same
	// step history; once a legacy step has run, the work item keeps its
	// scenario graph forever ("no second local authority", at composition
	// level).
	var legacyVersion int64
	if err := tx.QueryRow(ctx,
		`SELECT version FROM wi_step_state WHERE work_item_id = $1`, w.ID).Scan(&legacyVersion); err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return nil, dbErrCause(err, "read legacy step state")
	}
	if legacyVersion > 0 {
		return nil, NewErr(ErrConflictTerminalState,
			"this work item has already run scenario-graph steps; its steps cannot be replaced by a workflow")
	}

	// CAS: the caller's expected_steps_version must equal the locked row's.
	if req.ExpectedStepsVersion != wp.StepsVersion {
		return nil, NewErrDetails(ErrConflictCASFailed,
			fmt.Sprintf("workflow was revised concurrently: expected_steps_version %d but the current steps_version is %d",
				req.ExpectedStepsVersion, wp.StepsVersion),
			map[string]any{"expected_steps_version": req.ExpectedStepsVersion, "current_steps_version": wp.StepsVersion})
	}

	// Resolve + validate the new generation INSIDE the transaction.
	next := wp.StepsVersion + 1
	pin, aerr := pinWorkflowGeneration(ctx, tx, next, workflowRevisionInput{
		Caller:               caller,
		RequiresHumanSession: *req.RequiresHumanSession,
		Steps:                req.Steps,
	})
	if aerr != nil {
		return nil, aerr
	}

	stepsJSON, grantsJSON, metaJSON, aerr := marshalWorkflowGeneration(pin)
	if aerr != nil {
		return nil, aerr
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO wi_workflow_generations
		    (work_item_id, steps_version, steps, grants, step_meta, validation_digest, requires_human_session, created_by)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
		w.ID, next, stepsJSON, grantsJSON, metaJSON, pin.Digest, *req.RequiresHumanSession, callerUserID); err != nil {
		return nil, dbErrCause(err, "insert workflow generation")
	}

	// Move the pointer and record the explicit classification together: a
	// workflow-bearing work item can never hold a NULL requires_human_session.
	if _, err := tx.Exec(ctx, `
		UPDATE work_items
		SET steps = $1, steps_version = $2, requires_human_session = $3, workflow_mode = 'db', updated_at = clock_timestamp()
		WHERE id = $4`,
		stepsJSON, next, *req.RequiresHumanSession, w.ID); err != nil {
		return nil, dbErrCause(err, "move workflow pointer")
	}

	if aerr := insertWorkflowEvent(ctx, tx, w.ID, w.Project, callerUserID, "", "workflow_revision", map[string]any{
		"steps_version":          next,
		"expected_steps_version": req.ExpectedStepsVersion,
		"requires_human_session": *req.RequiresHumanSession,
		"steps":                  len(req.Steps),
		"validation_digest":      pin.Digest,
	}); aerr != nil {
		return nil, aerr
	}

	if err := tx.Commit(ctx); err != nil {
		if aerr := retryConflictErr(err, "commit workflow revision"); aerr != nil {
			return nil, aerr
		}
		return nil, NewErr(ErrInternalError, "commit workflow revision")
	}
	return GetWorkItemWorkflow(ctx, pool, w.ID)
}

// marshalWorkflowGeneration serializes a pin outcome into the three JSONB
// payloads a generation row stores.
func marshalWorkflowGeneration(pin *workflowPinOutcome) (steps, grants, meta []byte, aerr *AihubError) {
	steps, err := json.Marshal(pin.Validated.Definition())
	if err != nil {
		return nil, nil, nil, NewErr(ErrInternalError, "workflow does not serialize")
	}
	grants, err = json.Marshal(pin.Grants)
	if err != nil {
		return nil, nil, nil, NewErr(ErrInternalError, "workflow grants do not serialize")
	}
	meta, err = json.Marshal(pin.StepMeta)
	if err != nil {
		return nil, nil, nil, NewErr(ErrInternalError, "workflow metadata do not serialize")
	}
	return steps, grants, meta, nil
}

// pinFirstWorkflowGenerationInTx is the create-time twin of the revision
// above: called from CreateWorkItem's transaction right after the work_items
// INSERT, it pins generation 1 so a new-flow work item is born fully validated
// or not at all (spec D4: "Atomic create/pin latest accessible refs using
// same transaction").
func pinFirstWorkflowGenerationInTx(ctx context.Context, tx pgx.Tx, wiID, project, callerUserID string,
	in workflowRevisionInput) *AihubError {

	pin, aerr := pinWorkflowGeneration(ctx, tx, 1, in)
	if aerr != nil {
		return aerr
	}
	stepsJSON, grantsJSON, metaJSON, aerr := marshalWorkflowGeneration(pin)
	if aerr != nil {
		return aerr
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO wi_workflow_generations
		    (work_item_id, steps_version, steps, grants, step_meta, validation_digest, requires_human_session, created_by)
		VALUES ($1, 1, $2, $3, $4, $5, $6, $7)`,
		wiID, stepsJSON, grantsJSON, metaJSON, pin.Digest, in.RequiresHumanSession, callerUserID); err != nil {
		return dbErrCause(err, "insert workflow generation")
	}
	if _, err := tx.Exec(ctx, `
		UPDATE work_items SET steps = $1, steps_version = 1, workflow_mode = 'db' WHERE id = $2`, stepsJSON, wiID); err != nil {
		return dbErrCause(err, "pin workflow pointer")
	}
	return insertWorkflowEvent(ctx, tx, wiID, project, callerUserID, "", "workflow_revision", map[string]any{
		"steps_version":          1,
		"requires_human_session": in.RequiresHumanSession,
		"steps":                  len(in.Steps),
		"validation_digest":      pin.Digest,
	})
}

// ─── Start ───────────────────────────────────────────────────────────────────

// StartWorkflowStepRequest is the POST /workflow/start body.
type StartWorkflowStepRequest struct {
	AttemptID     string `json:"attempt_id"`
	ClaimEpoch    int64  `json:"claim_epoch"`
	SessionSecret string `json:"session_secret"`
	StepID        string `json:"step_id"`
	// RepairEpisodeID binds this invocation to an open repair authorization
	// (retry or episode). Empty for an ordinary first invocation.
	RepairEpisodeID string `json:"repair_episode_id,omitempty"`
}

// StartWorkflowStepResponse is the invocation descriptor the controller
// executes and the worker result must echo.
type StartWorkflowStepResponse struct {
	InvocationID  string              `json:"invocation_id"`
	StepsVersion  int                 `json:"steps_version"`
	StepID        string              `json:"step_id"`
	StepAttemptID string              `json:"step_attempt_id"`
	ProducerID    string              `json:"producer_id"`
	ClaimEpoch    int64               `json:"claim_epoch"`
	Grant         wf.StepGrant        `json:"grant"`
	SkillID       string              `json:"skill_id"`
	SkillVersion  int                 `json:"skill_version"`
	Models        []wf.ModelCandidate `json:"models"`
	Params        json.RawMessage     `json:"params,omitempty"`
	Inputs        []wf.InputRef       `json:"inputs,omitempty"`
	// EffectiveRHS is WI requires_human_session AND step rhs: the step cannot
	// advance past a recorded result until an authenticated human approval for
	// that exact artifact exists (what the pure policy holds on the controller
	// side, and what ApproveWorkflowStep binds).
	EffectiveRHS bool `json:"effective_rhs"`
}

// StartWorkflowStep mints one step invocation for the CURRENT attempt.
//
// Everything the worker needs is re-derived server-side here, from the pinned
// generation AND the live registry: access to every pinned version is
// RE-CHECKED at start time from the ATTEMPT ACTOR's registry view (spec D3 —
// revoked access blocks the next start, even though it cannot erase an
// already-authorized invocation), and the composition is re-validated so a
// generation is started against exactly the contract it was pinned with.
// step_attempt_id and producer_id are minted by the server (spec D7): a worker
// cannot pre-guess the identity a result must echo.
func StartWorkflowStep(ctx context.Context, pool *pgxpool.Pool, idOrSlug string, req StartWorkflowStepRequest) (*StartWorkflowStepResponse, *AihubError) {
	if req.StepID == "" {
		return nil, NewErr(ErrBadRequest, "step_id is required")
	}

	tx, err := pool.Begin(ctx)
	if err != nil {
		return nil, NewErr(ErrInternalError, "begin workflow start tx")
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	w, wp, aerr := getWorkflowWIOnTx(ctx, tx, idOrSlug, true)
	if aerr != nil {
		return nil, aerr
	}
	// Lifecycle fence (aihub#708 B2): the row lock is taken as this
	// transaction's FIRST statement and held through the INSERT below, the
	// same order the claim/complete/pause transitions use. verifyAttemptCredential
	// re-reads current_attempt_id off the LOCKED row, so a pause or takeover that
	// lands after this point waits for the commit and the next start sees it —
	// an invocation cannot be minted from an attempt a transition already
	// killed between the check and the write.
	if aerr := verifyAttemptCredential(ctx, tx, *w, req.AttemptID, req.ClaimEpoch, req.SessionSecret); aerr != nil {
		return nil, aerr
	}

	if wp.StepsVersion == 0 {
		return nil, NewErr(ErrNotFound, "this work item has no workflow; nothing to start")
	}
	if w.Status != "running" {
		return nil, NewErr(ErrConflictTerminalState,
			fmt.Sprintf("work item status is %q; a workflow step can only start on a running attempt", w.Status))
	}
	wiRHS := w.RequiresHumanSession != nil && *w.RequiresHumanSession

	// Re-resolve and re-validate the generation against the live registry.
	flow, grants, stepMeta, aerr := revalidateWorkflowGeneration(ctx, tx, wp.StepsVersion, req.AttemptID, wiRHS)
	if aerr != nil {
		return nil, aerr
	}

	var target *wf.Step
	targetIndex := -1
	for i := range flow.Steps {
		if flow.Steps[i].ID == req.StepID {
			target = &flow.Steps[i]
			targetIndex = i
			break
		}
	}
	if target == nil {
		return nil, NewErr(ErrNotFound, fmt.Sprintf("step %q is not in the current workflow", req.StepID))
	}

	// One open invocation per (generation, step) at a time: the identity a
	// result must echo must be unambiguous. An open invocation left behind by a
	// DEAD attempt is not silently reusable and not silently superseded either
	// (aihub#708 B3): the live attempt must fence it explicitly through the
	// reconcile transition first — no implicit trust in a start that reuses a
	// dead attempt's identity.
	var openID, openAttemptID, openAttemptStatus string
	err = tx.QueryRow(ctx, `
		SELECT inv.id, inv.run_attempt_id, ra.status FROM wi_workflow_invocations inv
		JOIN run_attempts ra ON ra.id = inv.run_attempt_id
		WHERE inv.work_item_id = $1 AND inv.steps_version = $2 AND inv.step_id = $3 AND inv.status = 'open'`,
		w.ID, wp.StepsVersion, target.ID).Scan(&openID, &openAttemptID, &openAttemptStatus)
	switch {
	case err == nil:
		if openAttemptStatus != "running" {
			return nil, NewErr(ErrConflictStepInProgress, fmt.Sprintf(
				"step %q has an open invocation from attempt %s whose status is %q; reconcile (POST /workflow/reconcile) supersedes a dead attempt's open invocations before a replacement can start",
				target.ID, openAttemptID, openAttemptStatus))
		}
		return nil, NewErr(ErrConflictStepInProgress,
			fmt.Sprintf("step %q already has an open invocation; record its result before starting another", target.ID))
	case !errors.Is(err, pgx.ErrNoRows):
		return nil, dbErrCause(err, "read open invocation")
	}

	if aerr := checkWorkflowStartOrdering(ctx, tx, w.ID, wp.StepsVersion, flow, grants, stepMeta,
		target.ID, targetIndex, req.RepairEpisodeID, wiRHS); aerr != nil {
		return nil, aerr
	}

	// Mint identities. Server-generated, never caller-chosen (spec D7).
	stepAttemptID := NewID("sa")
	producerID := NewID("wfp")
	invocationID := NewID("winv")
	grantJSON, gmErr := json.Marshal(grants[target.ID])
	if gmErr != nil {
		return nil, NewErr(ErrInternalError, "grant does not serialize")
	}

	var episodeID *string
	if req.RepairEpisodeID != "" {
		episodeID = &req.RepairEpisodeID
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO wi_workflow_invocations
		    (id, work_item_id, steps_version, step_id, step_attempt_id, run_attempt_id, claim_epoch,
		     producer_id, invocation_grant, status, repair_episode_id)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, 'open', $10)`,
		invocationID, w.ID, wp.StepsVersion, target.ID, stepAttemptID, req.AttemptID, req.ClaimEpoch,
		producerID, grantJSON, episodeID); err != nil {
		return nil, dbErrCause(err, "insert workflow invocation")
	}

	actorUserID, actorDisplay, aerr := attemptActor(ctx, tx, req.AttemptID)
	if aerr != nil {
		return nil, aerr
	}
	if aerr := insertWorkflowEvent(ctx, tx, w.ID, w.Project, actorUserID, actorDisplay, "workflow_invocation_started", map[string]any{
		"steps_version":     wp.StepsVersion,
		"step_id":           target.ID,
		"step_attempt_id":   stepAttemptID,
		"producer_id":       producerID,
		"run_attempt_id":    req.AttemptID,
		"claim_epoch":       req.ClaimEpoch,
		"repair_episode_id": req.RepairEpisodeID,
	}); aerr != nil {
		return nil, aerr
	}

	if err := tx.Commit(ctx); err != nil {
		if aerr := retryConflictErr(err, "commit workflow start"); aerr != nil {
			return nil, aerr
		}
		return nil, NewErr(ErrInternalError, "commit workflow start")
	}

	return &StartWorkflowStepResponse{
		InvocationID:  invocationID,
		StepsVersion:  wp.StepsVersion,
		StepID:        target.ID,
		StepAttemptID: stepAttemptID,
		ProducerID:    producerID,
		ClaimEpoch:    req.ClaimEpoch,
		Grant:         grants[target.ID],
		SkillID:       target.SkillID,
		SkillVersion:  target.SkillVersion,
		Models:        target.Models,
		Params:        target.Params,
		Inputs:        target.Inputs,
		EffectiveRHS:  wiRHS && target.RHS != nil && *target.RHS,
	}, nil
}

// revalidateWorkflowGeneration re-resolves every pinned version of the current
// generation through the registry's accessibility predicate — from the
// ATTEMPT ACTOR's user record, since that is whose registry view decides
// access — and re-runs the pure validation. Returns the stored flow, the
// grants and the step metadata. A revoked share or a flipped visibility
// answers HERE, before any invocation is minted (spec D3).
func revalidateWorkflowGeneration(ctx context.Context, tx pgx.Tx, stepsVersion int, attemptID string, wiRHS bool) (wf.Flow, map[string]wf.StepGrant, map[string]wiStepMeta, *AihubError) {
	var stepsRaw []byte
	if err := tx.QueryRow(ctx,
		`SELECT steps FROM work_items WHERE id = (SELECT work_item_id FROM run_attempts WHERE id = $1)`,
		attemptID).Scan(&stepsRaw); err != nil {
		return wf.Flow{}, nil, nil, dbErrCause(err, "read workflow steps")
	}
	var flow wf.Flow
	if err := json.Unmarshal(stepsRaw, &flow); err != nil {
		return wf.Flow{}, nil, nil, NewErr(ErrInternalError, "stored workflow flow does not parse")
	}

	actor, aerr := userRecordForAttempt(ctx, tx, attemptID)
	if aerr != nil {
		return wf.Flow{}, nil, nil, aerr
	}

	// Re-pin in memory: resolution through the SAME predicate inside the SAME
	// transaction, grants through the SAME policy, validation through the
	// SAME pure call.
	specs := make([]WorkflowStepSpec, 0, len(flow.Steps))
	for _, s := range flow.Steps {
		specs = append(specs, WorkflowStepSpec{
			ID: s.ID, SkillID: s.SkillID, SkillVersion: s.SkillVersion,
			RHS: s.RHS, Models: s.Models, Params: s.Params, Inputs: s.Inputs,
		})
	}
	pin, aerr := pinWorkflowGeneration(ctx, tx, stepsVersion, workflowRevisionInput{
		Caller:               actor,
		RequiresHumanSession: wiRHS,
		Steps:                specs,
	})
	if aerr != nil {
		return wf.Flow{}, nil, nil, aerr
	}
	return flow, pin.Grants, pin.StepMeta, nil
}

// userRecordForAttempt loads the UserRecord of the user who owns the given
// run attempt — the registry view whose access a start re-check applies. The
// scope half mirrors how BearerAuth derives it: a project scope is a property
// of the API KEY the attempt was claimed with, not of the user, so it is read
// from the key the attempt recorded (nil for a session/key without one, which
// is the unscoped view).
func userRecordForAttempt(ctx context.Context, tx pgx.Tx, attemptID string) (*UserRecord, *AihubError) {
	var userID, role string
	var apiKeyID *string
	err := tx.QueryRow(ctx, `
		SELECT ra.actor_user_id, u.role, ra.api_key_id FROM run_attempts ra
		JOIN users u ON u.id = ra.actor_user_id
		WHERE ra.id = $1`, attemptID).Scan(&userID, &role, &apiKeyID)
	if err != nil {
		return nil, dbErrCause(err, "read attempt actor")
	}
	var scope *string
	if apiKeyID != nil && *apiKeyID != "" {
		// users.api_keys is a JSONB array; the recorded key id names the
		// element. A missing element (key revoked and re-created, or a row
		// written before the id was recorded) reads as unscoped rather than
		// failing the start — the user-level view is unchanged by which key
		// they used, only the scope confinement differs.
		if err := tx.QueryRow(ctx, `
			SELECT k->>'project_scope' FROM users u,
			     jsonb_array_elements(u.api_keys) AS k
			WHERE u.id = $1 AND k->>'id' = $2`, userID, *apiKeyID).Scan(&scope); err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return nil, dbErrCause(err, "read attempt key scope")
		}
	}
	return &UserRecord{ID: userID, Role: role, ProjectScope: scope}, nil
}

// workflowLatestResult is the latest recorded result for one step in one
// generation.
type workflowLatestResult struct {
	Status        string
	ReviewVerdict string
	StepAttemptID string
}

func latestWorkflowResults(ctx context.Context, tx pgx.Tx, wiID string, stepsVersion int) (map[string]workflowLatestResult, *AihubError) {
	rows, err := tx.Query(ctx, `
		SELECT DISTINCT ON (step_id) step_id, status, COALESCE(review_verdict,''), step_attempt_id
		FROM wi_workflow_results
		WHERE work_item_id = $1 AND steps_version = $2
		ORDER BY step_id, created_at DESC, id DESC`, wiID, stepsVersion)
	if err != nil {
		return nil, dbErrCause(err, "read workflow results")
	}
	defer rows.Close()
	out := make(map[string]workflowLatestResult)
	for rows.Next() {
		var r workflowLatestResult
		var stepID string
		if err := rows.Scan(&stepID, &r.Status, &r.ReviewVerdict, &r.StepAttemptID); err != nil {
			return nil, dbErrCause(err, "read workflow results")
		}
		out[stepID] = r
	}
	if err := rows.Err(); err != nil {
		return nil, dbErrCause(err, "read workflow results")
	}
	return out, nil
}

func workflowStepIndex(flow wf.Flow, stepID string) int {
	for i := range flow.Steps {
		if flow.Steps[i].ID == stepID {
			return i
		}
	}
	return len(flow.Steps)
}

// workflowStepFeeds reports whether producerStep is a named input source of
// consumerStep.
func workflowStepFeeds(flow wf.Flow, producerStep, consumerStep string) bool {
	for i := range flow.Steps {
		if flow.Steps[i].ID != consumerStep {
			continue
		}
		for _, in := range flow.Steps[i].Inputs {
			if in.StepID == producerStep {
				return true
			}
		}
	}
	return false
}

// stepHasCapability reads the capability set out of the generation metadata.
func stepHasCapability(meta map[string]wiStepMeta, stepID string, want skillregistry.Capability) bool {
	m, ok := meta[stepID]
	if !ok {
		return false
	}
	for _, c := range m.Capabilities {
		if c == want {
			return true
		}
	}
	return false
}

// checkWorkflowStartOrdering enforces the ordering and repair-role rules at
// start time, mirroring what the pure policy holds over the result history
// later (internal/workflow.Decide / validateRepairEpisodes): ordinary
// invocations follow definition order; only an authorized repair may reopen an
// earlier step; an episode's three roles have fixed semantic shapes; and every
// step whose effective human gate (WI rhs AND step rhs) holds must have its
// LATEST recorded result's artifact approved by a human before any later
// invocation mints (aihub#708 B4 — a side-effectful step never starts past an
// approval that has not been given, was rejected, or went stale on an older
// artifact).
//
// Two further gates hold an UNRESOLVED recovery in (aihub#708 re-review
// blocker 1, the episode prerequisites):
//
//   - an ordinary mint is refused while ANY open repair authorization of the
//     current generation has an incomplete bounded set. The hole this
//     closes: an episode's fresh-review role could record a PASS on its own,
//     after which every earlier step's latest result "looked complete" and a
//     side-effectful ship minted on top of an episode whose repair producer
//     and verification never ran. episodeRoleSetComplete — the same predicate
//     that closes the episode at record time — is consulted BEFORE the mint,
//     so a successor can only start behind a CLOSED recovery.
//   - an episode role mint is refused before its predecessors in the chain
//     repair producer -> verification -> fresh review have recorded COMPLETED
//     results (checkEpisodeRolePrerequisites). This is what keeps the
//     replacement's OWN artifact in front of the approval gate: while the
//     replacement producer invocation is still open, "latest recorded
//     result" still names the pre-episode artifact, and a dependent
//     verification/review would otherwise mint on the OLD artifact's approval.
func checkWorkflowStartOrdering(ctx context.Context, tx pgx.Tx, wiID string, stepsVersion int,
	flow wf.Flow, grants map[string]wf.StepGrant, meta map[string]wiStepMeta,
	stepID string, stepIndex int, repairID string, wiRHS bool) *AihubError {

	latest, aerr := latestWorkflowResults(ctx, tx, wiID, stepsVersion)
	if aerr != nil {
		return aerr
	}

	if repairID != "" {
		var kind, failedStepID, failedStepAttempt, status string
		var failedVersion int
		err := tx.QueryRow(ctx, `
			SELECT kind, failed_steps_version, failed_step_id, failed_step_attempt_id, status
			FROM wi_workflow_repair_episodes WHERE id = $1 AND work_item_id = $2`,
			repairID, wiID).Scan(&kind, &failedVersion, &failedStepID, &failedStepAttempt, &status)
		if errors.Is(err, pgx.ErrNoRows) {
			return NewErr(ErrNotFound, "repair authorization not found")
		}
		if err != nil {
			return dbErrCause(err, "read repair authorization")
		}
		if status != "open" {
			return NewErr(ErrConflictTerminalState, "repair authorization is closed")
		}
		if failedVersion != stepsVersion {
			return NewErr(ErrConflictStepAttemptMismatch,
				fmt.Sprintf("repair authorization belongs to workflow generation %d, current is %d; a revised flow needs a new authorization",
					failedVersion, stepsVersion))
		}
		failed, ok := latest[failedStepID]
		if !ok || failed.StepAttemptID != failedStepAttempt {
			return NewErr(ErrConflictStepAttemptMismatch,
				"the failed step attempt this repair authorizes is no longer that step's latest result; open a new authorization")
		}

		switch kind {
		case "retry":
			if stepID != failedStepID {
				return NewErr(ErrBadRequest, fmt.Sprintf(
					"a retry authorization covers step %q only; step %q is not it", failedStepID, stepID))
			}
			var bound int
			if err := tx.QueryRow(ctx, `
				SELECT count(*) FROM wi_workflow_invocations
				WHERE repair_episode_id = $1 AND status <> 'superseded'`, repairID).Scan(&bound); err != nil {
				return dbErrCause(err, "read retry bindings")
			}
			if bound > 0 {
				return NewErr(ErrConflictDuplicate, "this retry authorization already has its one invocation")
			}
		case "episode":
			// The failed result must be a FAIL-verdict review gate.
			if failed.ReviewVerdict != string(wf.ReviewFail) {
				return NewErr(ErrBadRequest, "an episode authorization must name a failed review verdict")
			}
			if !stepHasCapability(meta, failedStepID, skillregistry.CapReview) {
				return NewErr(ErrBadRequest, "an episode authorization must name a review gate's failed result")
			}
			// One invocation per step per episode: the bounded set is exactly one
			// repair producer, one verification and one review (the pure policy
			// binds one invocation per role), so a second binding of the same
			// step can only duplicate a role.
			var boundStep int
			if err := tx.QueryRow(ctx, `
				SELECT count(*) FROM wi_workflow_invocations
				WHERE repair_episode_id = $1 AND step_id = $2 AND status <> 'superseded'`, repairID, stepID).Scan(&boundStep); err != nil {
				return dbErrCause(err, "read episode bindings")
			}
			if boundStep > 0 {
				return NewErr(ErrConflictDuplicate, fmt.Sprintf(
					"this episode already has an invocation for step %q; each role binds exactly one", stepID))
			}
			grant := grants[stepID]
			failedIdx := workflowStepIndex(flow, failedStepID)
			switch {
			case stepID == failedStepID:
				// The fresh review role: a NEW invocation of the same gate.
			case grant.Authority == wf.AuthorityWrite && grant.ProducerIsolation == wf.IsolationShared &&
				stepIndex < failedIdx && workflowStepFeeds(flow, stepID, failedStepID):
				// The repair producer: a write step earlier than the gate,
				// feeding the gate's inputs.
			case grant.Authority == wf.AuthorityReadOnly && grant.ProducerIsolation == wf.IsolationRequired &&
				stepHasCapability(meta, stepID, skillregistry.CapVerification):
				// The verification role.
			case grant.Authority == wf.AuthorityReadOnly && grant.ProducerIsolation == wf.IsolationRequired &&
				stepHasCapability(meta, stepID, skillregistry.CapReview) && stepID != failedStepID:
				// A distinct fresh review gate.
			default:
				return NewErr(ErrBadRequest, fmt.Sprintf(
					"step %q is not a repair producer, verification gate or review gate for the failed review %q", stepID, failedStepID))
			}

			// The episode's role ORDER at mint: the chain is repair producer ->
			// verification -> fresh review, and no dependent role may mint
			// before its predecessors recorded completed results (aihub#708
			// re-review blocker 1). Above all, not while the replacement
			// producer invocation is still open: the approval gate below reads
			// LATEST RECORDED results, and while the replacement has not
			// recorded, "latest" still names the pre-episode artifact — a
			// successor episode role would mint on an approval the replacement
			// never earned.
			if aerr := checkEpisodeRolePrerequisites(ctx, tx, repairID, flow, grants, meta, failedStepID, stepID); aerr != nil {
				return aerr
			}
		default:
			return NewErr(ErrBadRequest, "repair kind must be retry or episode")
		}
		// The human gate holds on a repair start too (aihub#708 B4): a recovery
		// is side-effectful like any other invocation, and an RHS-gated step's
		// latest artifact must still carry a human approval. The step the
		// episode RECOVERS is excluded — its FAIL result is the reason the
		// episode exists, and demanding an approval for a failed verdict would
		// deadlock every recovery on its own evidence.
		return checkWorkflowStartApprovals(ctx, tx, wiID, stepsVersion, flow, wiRHS, stepID, failedStepID)
	}

	// Ordinary start: the step has no results yet in this generation, every
	// earlier step's latest result is completed and not a FAIL verdict, and no
	// step is waiting on a failure (an outstanding failure needs an explicit
	// repair authorization, never a quiet restart — spec D8).
	if _, hasResults := latest[stepID]; hasResults {
		// An authenticated human rejection is itself the bounded authority to
		// revise THIS effective-RHS step. It never permits a successor (the
		// approval gate below still refuses rejected artifacts); it permits only
		// a fresh invocation of the same step, whose new artifact needs a new
		// exact approval. Without this transition a rejection is a permanent
		// dead-end rather than a review/revision loop.
		revisionAllowed := wiRHS && targetStepRejectsLatest(ctx, tx, wiID, stepsVersion, flow.Steps[stepIndex])
		if !revisionAllowed {
			return NewErr(ErrConflictDuplicate, fmt.Sprintf(
				"step %q already has a recorded result in this generation; reopening it requires an explicit repair authorization or an authenticated rejection of its latest RHS artifact", stepID))
		}
	}

	// The unresolved-recovery gate (aihub#708 re-review blocker 1): while any
	// open repair authorization of the CURRENT generation has an incomplete
	// bounded set, no ordinary invocation mints at all. Authorizations of an
	// older generation do not count — a revised flow needs a new
	// authorization, and a stale one must not wedge the new generation
	// forever. Practically every step an episode could unblock is already
	// fenced by the checks below; this gate exists for the case they cannot
	// see, where the recovery's own recorded results make the old artifacts
	// LOOK complete (the failed gate's fresh PASS, above all) while the
	// episode's other roles never ran. Recovery then proceeds only through
	// authorization-bound invocations until the bounded set completes and the
	// episode closes — the same predicate closeRepairEpisodeIfComplete applies
	// at record time, consulted here BEFORE the mint.
	type openAuthzRow struct {
		id, kind, failedStepID, failedStepAttempt string
	}
	openRows, err := tx.Query(ctx, `
		SELECT id, kind, failed_step_id, failed_step_attempt_id
		FROM wi_workflow_repair_episodes
		WHERE work_item_id = $1 AND failed_steps_version = $2 AND status = 'open'
		ORDER BY created_at, id`, wiID, stepsVersion)
	if err != nil {
		return dbErrCause(err, "read open repair authorizations")
	}
	var openAuthz []openAuthzRow
	for openRows.Next() {
		var a openAuthzRow
		if err := openRows.Scan(&a.id, &a.kind, &a.failedStepID, &a.failedStepAttempt); err != nil {
			openRows.Close()
			return dbErrCause(err, "read open repair authorizations")
		}
		openAuthz = append(openAuthz, a)
	}
	if err := openRows.Err(); err != nil {
		openRows.Close()
		return dbErrCause(err, "read open repair authorizations")
	}
	openRows.Close()
	for _, authz := range openAuthz {
		switch authz.kind {
		case "retry":
			// Bounded set complete iff its single invocation has recorded —
			// the same shape closeRepairEpisodeIfComplete closes on.
			var stillOpen, recorded int
			if err := tx.QueryRow(ctx, `
				SELECT
					(SELECT count(*) FROM wi_workflow_invocations WHERE repair_episode_id=$1 AND status='open'),
					(SELECT count(*) FROM wi_workflow_invocations WHERE repair_episode_id=$1 AND status='recorded')`,
				authz.id).Scan(&stillOpen, &recorded); err != nil {
				return dbErrCause(err, "read retry bindings")
			}
			if stillOpen == 0 && recorded > 0 {
				continue
			}
			return NewErr(ErrConflictStepInProgress, fmt.Sprintf(
				"step %q cannot start while retry authorization %s is open and unresolved (its retry invocation has not recorded a result); only the authorization's own bound invocation may run until it closes",
				stepID, authz.id))
		case "episode":
			complete, aerr := episodeRoleSetComplete(ctx, tx, authz.id, wiID, stepsVersion, authz.failedStepID, authz.failedStepAttempt)
			if aerr != nil {
				return aerr
			}
			if complete {
				continue
			}
			return NewErr(ErrConflictStepInProgress, fmt.Sprintf(
				"step %q cannot start while repair episode %s is open and unresolved: its recovery roles (repair producer, fresh verification, fresh review) are not all complete, so the failed review %q is not yet recovered; only episode-bound invocations may mint until the episode closes",
				stepID, authz.id, authz.failedStepID))
		default:
			return NewErr(ErrInternalError, fmt.Sprintf(
				"repair authorization %s carries unknown kind %q; refusing every start rather than trust it", authz.id, authz.kind))
		}
	}

	for i := 0; i < stepIndex; i++ {
		r, ok := latest[flow.Steps[i].ID]
		if !ok {
			return NewErr(ErrConflictStepInProgress, fmt.Sprintf(
				"step %q cannot start before step %q has a completed result", stepID, flow.Steps[i].ID))
		}
		if r.Status != string(wf.StatusCompleted) || r.ReviewVerdict == string(wf.ReviewFail) {
			return NewErr(ErrConflictStepInProgress, fmt.Sprintf(
				"step %q cannot start while step %q's latest result is %q (verdict %q); a failure needs an explicit repair authorization",
				stepID, flow.Steps[i].ID, r.Status, r.ReviewVerdict))
		}
	}
	// The human gate (aihub#708 B4): every earlier step that gates on the
	// human must have its latest recorded result's artifact approved.
	return checkWorkflowStartApprovals(ctx, tx, wiID, stepsVersion, flow, wiRHS, stepID, "")
}

// targetStepRejectsLatest reports the one ordinary re-entry authorization:
// the target is RHS-gated and an authenticated human rejected its exact latest
// artifact. Query failures fail closed as false; the subsequent invocation
// insert is never authorized by an unavailable decision.
func targetStepRejectsLatest(ctx context.Context, tx pgx.Tx, wiID string, stepsVersion int, step wf.Step) bool {
	if step.RHS == nil || !*step.RHS {
		return false
	}
	var decision string
	err := tx.QueryRow(ctx, `
		SELECT appr.decision
		FROM (
			SELECT artifact FROM wi_workflow_results
			WHERE work_item_id=$1 AND steps_version=$2 AND step_id=$3
			ORDER BY created_at DESC, id DESC LIMIT 1
		) res
		JOIN wi_workflow_approvals appr
		  ON appr.work_item_id=$1 AND appr.steps_version=$2 AND appr.step_id=$3
		 AND appr.artifact_id = res.artifact->>'id'
		 AND appr.artifact_version = (res.artifact->>'version')::int
		 AND appr.artifact_hash = res.artifact->>'hash'`,
		wiID, stepsVersion, step.ID).Scan(&decision)
	return err == nil && decision == string(wf.ApprovalDenied)
}

// checkWorkflowStartApprovals is the start-side human gate (aihub#708 B4).
// For every step with an effective human gate (WI requires_human_session AND
// step rhs) that has a recorded result — excluding the step being started and
// the excludeStepID a repair recovers — the LATEST result's exact artifact
// triple must carry a HUMAN approval. Three refusals, one per state:
//
//	no decision for the exact artifact  the approval never happened (or a
//	                                  machine credential tried and was
//	                                  refused — approvals are human-only at
//	                                  the recording route, so this gate can
//	                                  read them without re-checking actorship)
//	rejected                        a human rejected this exact artifact
//	stale mismatch                   the step recorded a NEWER artifact after
//	                                  the one a decision exists for; the old
//	                                  decision satisfies nothing
//
// A step with no recorded result is skipped, not refused: reachability is the
// ordering check's job, and a not-yet-run later step cannot block an earlier
// start. Runs on the caller's locked transaction, so the approval state it
// reads is the same snapshot the invocation INSERT lands in — there is no
// window where an approval could land between this check and the mint.
func checkWorkflowStartApprovals(ctx context.Context, tx pgx.Tx, wiID string, stepsVersion int,
	flow wf.Flow, wiRHS bool, targetStepID, excludeStepID string) *AihubError {
	if !wiRHS {
		return nil
	}
	for i := range flow.Steps {
		s := &flow.Steps[i]
		if s.ID == targetStepID || (excludeStepID != "" && s.ID == excludeStepID) ||
			s.RHS == nil || !*s.RHS {
			continue
		}
		var artifact []byte
		var decision *string
		err := tx.QueryRow(ctx, `
			SELECT res.artifact, appr.decision
			FROM (
				SELECT artifact FROM wi_workflow_results
				WHERE work_item_id=$1 AND steps_version=$2 AND step_id=$3
				ORDER BY created_at DESC, id DESC LIMIT 1
			) res
			LEFT JOIN wi_workflow_approvals appr
			  ON appr.work_item_id=$1 AND appr.steps_version=$2 AND appr.step_id=$3
			 AND appr.artifact_id = res.artifact->>'id'
			 AND appr.artifact_version = (res.artifact->>'version')::int
			 AND appr.artifact_hash = res.artifact->>'hash'`,
			wiID, stepsVersion, s.ID,
		).Scan(&artifact, &decision)
		if errors.Is(err, pgx.ErrNoRows) {
			// No recorded result for this gated step yet: the ordering check is
			// what decides whether that is reachable, not this gate.
			continue
		}
		if err != nil {
			return dbErrCause(err, "read workflow approval state")
		}
		if decision == nil {
			// No decision binds the LATEST artifact. Distinguish a decision
			// that went stale on an older artifact from one that never came:
			// the recovery action differs (re-approve the new artifact vs.
			// approve at all), and the message is the only place the server can
			// say which world the caller is in.
			var anyDecision int
			if err := tx.QueryRow(ctx, `
				SELECT count(*) FROM wi_workflow_approvals
				WHERE work_item_id=$1 AND steps_version=$2 AND step_id=$3`,
				wiID, stepsVersion, s.ID).Scan(&anyDecision); err != nil {
				return dbErrCause(err, "read workflow approval history")
			}
			if anyDecision > 0 {
				return NewErr(ErrConflictStepInProgress, fmt.Sprintf(
					"step %q cannot start: a human decision exists for step %q but not for its LATEST recorded artifact; the exact latest artifact must be approved (the old decision went stale when the step re-recorded)",
					targetStepID, s.ID))
			}
			return NewErr(ErrConflictStepInProgress, fmt.Sprintf(
				"step %q cannot start before a human approves step %q's latest recorded result (the step gates on a human session)",
				targetStepID, s.ID))
		}
		if *decision != string(wf.ApprovalGranted) {
			return NewErr(ErrConflictStepInProgress, fmt.Sprintf(
				"step %q cannot start: a human REJECTED step %q's latest recorded result; the step must re-record before anything builds on it",
				targetStepID, s.ID))
		}
	}
	return nil
}

// ─── Episode role prerequisites at start (aihub#708 re-review blocker 1) ────

// checkEpisodeRolePrerequisites enforces the repair episode's role ORDER at
// mint time: the chain is repair producer -> verification -> fresh review, and
// no dependent role may mint before the predecessors recorded COMPLETED
// results. The hole this closes: the start ordering classified roles by SHAPE
// only, so an episode's verification or fresh review could mint while the
// bound replacement producer invocation was still OPEN — and since the human
// approval gate reads LATEST RECORDED results, "latest" still named the
// pre-episode artifact, so the dependent minted on the OLD artifact's approval
// and the replacement's own artifact never faced a human. With the producer's
// completion required first, the approval gate (checkWorkflowStartApprovals,
// aihub#708 B4) then sees exactly the replacement's artifact once it records,
// and the old approval cannot carry over.
//
// The target's role is classified with the SAME shapes the bind-time switch
// in checkWorkflowStartOrdering just accepted it under, and the episode's
// recorded progress is classified by the SAME loader episodeRoleSetComplete
// judges completeness with (loadEpisodeRoleRows) — the start gate and the
// close predicate cannot disagree on what a role is. What each role waits
// for mirrors the pure policy's event-order chain (failed result, then
// repair, then verification, then review):
//
//	repair producer   nothing (the head of the chain)
//	verification      every bound repair producer recorded COMPLETED
//	fresh review      every bound repair producer AND verification recorded
//	                  COMPLETED — the last link before the flow resumes
//
// A step that can serve BOTH the verification and the review role (a contract
// carrying both capabilities) is gated at the verification level, exactly as
// the bind-time switch classifies it; the close predicate remains the final
// judge over the whole set.
func checkEpisodeRolePrerequisites(ctx context.Context, tx pgx.Tx, episodeID string,
	flow wf.Flow, grants map[string]wf.StepGrant, meta map[string]wiStepMeta,
	failedStepID, targetStepID string) *AihubError {

	roles, aerr := loadEpisodeRoleRows(ctx, tx, episodeID, flow, grants, meta, failedStepID)
	if aerr != nil {
		return aerr
	}

	grant := grants[targetStepID]
	idx := workflowStepIndex(flow, targetStepID)
	failedIdx := workflowStepIndex(flow, failedStepID)
	// Same precedence the bind-time switch in checkWorkflowStartOrdering
	// classified this target under: the failed gate itself is the fresh
	// review (first arm), then the producer shape, then verification, and only
	// then a distinct review gate — so a contract carrying both verification
	// and review capabilities is gated the same way it was bound.
	isRepairProducer := targetStepID != failedStepID &&
		grant.Authority == wf.AuthorityWrite && grant.ProducerIsolation == wf.IsolationShared &&
		idx < failedIdx && workflowStepFeeds(flow, targetStepID, failedStepID)
	if isRepairProducer {
		return nil // the head of the chain: nothing precedes it
	}
	repairDone := episodeRoleRowsCompleted(roles.repair)
	isVerification := targetStepID != failedStepID &&
		grant.Authority == wf.AuthorityReadOnly && grant.ProducerIsolation == wf.IsolationRequired &&
		stepHasCapability(meta, targetStepID, skillregistry.CapVerification)
	if isVerification {
		if !repairDone {
			return NewErr(ErrConflictStepInProgress, fmt.Sprintf(
				"step %q (the episode's verification gate) cannot start before the episode's bound repair producer records a completed result (episode %s): the replacement is still outstanding, and the old artifact's approval cannot stand in for the replacement's",
				targetStepID, episodeID))
		}
		return nil
	}
	// The fresh review: of the failed gate itself, or a distinct fresh review
	// gate. The last link — it waits for the whole chain behind it.
	if !repairDone || !episodeRoleRowsCompleted(roles.verify) {
		return NewErr(ErrConflictStepInProgress, fmt.Sprintf(
			"step %q (the episode's fresh review) cannot start before the episode's bound repair producer and verification gate record completed results (episode %s)",
			targetStepID, episodeID))
	}
	return nil
}

// episodeRoles is an episode's LIVE (non-superseded) bound invocations,
// classified into the three roles over frozen generation state.
type episodeRoles struct {
	repair []episodeResultRow
	verify []episodeResultRow
	review []episodeResultRow
}

// episodeRoleRowsCompleted reports whether a role's bounded set is non-empty
// and every result it recorded is COMPLETED. An invocation with no recorded
// result carries an empty status, so an outstanding (open) invocation makes
// its role incomplete by construction — the start gate refuses that role's
// dependents and the close predicate leaves the episode open, the same answer
// from the same rows.
func episodeRoleRowsCompleted(rows []episodeResultRow) bool {
	if len(rows) == 0 {
		return false
	}
	for _, r := range rows {
		if r.status != string(wf.StatusCompleted) {
			return false
		}
	}
	return true
}

// loadEpisodeRoleRows classifies an episode's LIVE invocations (status <>
// 'superseded' — an abandoned invocation is not part of the bounded set, its
// replacement takes the role) into the three roles, over the GIVEN frozen
// generation state (flow, grants, step metadata). Shared by the close
// predicate (episodeRoleSetComplete) and the start-time prerequisites
// (checkEpisodeRolePrerequisites), so the two can never disagree on what a
// role is. An invocation with no recorded result carries an empty status and
// a zero time: it is an unfinished role, incomplete by construction.
//
// Keyed by repair_episode_id alone — episode ids are globally unique (the
// table's primary key), so the work item is implied by the episode and no
// second identity has to agree.
//
// EFFECTIVE ROLE RESULTS (aihub#708 Batch 2A re-review blocker, mem_FHuxXIXI):
// a role invocation that recorded provider_error is NOT the role's last word.
// The authorized retry lineage stands in for it: loadEpisodeRetryReplacements
// loads every retry whose parent_episode_id names this episode together with
// its live replacement invocation, and each role row's result fields are
// replaced by the chain's live end — the latest authorized replacement —
// while the failed history rows stay in wi_workflow_results untouched. The
// old shape (direct repair_episode_id matches only) left the episode open
// forever: the E-bound row recorded provider_error, the retry's replacement
// recorded under the RETRY's id, and no reader of E ever saw the completed
// replacement — fresh verification was refused on the failed original, a
// direct rebind was refused, and reconcile cannot remove a RECORDED failure.
// The chain walk is the repair: no rebind (the E-bound invocation keeps its
// binding), no history rewrite (results are append-only), and the retry chain
// itself is bounded at authorization time (maxRetryChainDepth), so following
// it cannot loop. A replacement that itself provider_errors simply leaves
// the role still incomplete until the next authorized retry completes or the
// bound is hit.
func loadEpisodeRoleRows(ctx context.Context, tx pgx.Tx, episodeID string,
	flow wf.Flow, grants map[string]wf.StepGrant, meta map[string]wiStepMeta,
	failedStepID string) (episodeRoles, *AihubError) {

	rows, err := tx.Query(ctx, `
		SELECT inv.step_id, inv.step_attempt_id,
		       COALESCE(res.status,''), res.created_at, COALESCE(res.id,'')
		FROM wi_workflow_invocations inv
		LEFT JOIN wi_workflow_results res ON res.invocation_id = inv.id
		WHERE inv.repair_episode_id = $1 AND inv.status <> 'superseded'
		ORDER BY res.created_at, res.id`, episodeID)
	if err != nil {
		return episodeRoles{}, dbErrCause(err, "read episode invocations")
	}
	defer rows.Close()
	var out episodeRoles
	failedIdx := workflowStepIndex(flow, failedStepID)
	for rows.Next() {
		var r episodeResultRow
		var resStatus string
		var createdAt *time.Time
		var resID string
		if err := rows.Scan(&r.stepID, &r.stepAttemptID, &resStatus, &createdAt, &resID); err != nil {
			return episodeRoles{}, dbErrCause(err, "read episode invocations")
		}
		// The nullable created_at is scanned into a POINTER: scanning it into
		// time.Time answers a scan error that rolls the caller's transaction
		// back instead of reporting the row as unfinished (aihub#708 B1
		// regression, held by
		// TestWorkflowEpisodeOutstandingResultRecordsAndStaysOpen).
		r.status = resStatus
		if createdAt != nil {
			r.createdAt = *createdAt
			r.id = resID
		}
		// Role classification over the FROZEN grants and metadata, mirroring
		// both checkWorkflowStartOrdering's bind-time shapes and the pure
		// validator's role predicates. A step with review AND verification
		// capabilities can serve either role; a single attempt can never serve
		// both (the order check below requires verification strictly before
		// review), exactly as the pure policy's position chain does.
		grant := grants[r.stepID]
		idx := workflowStepIndex(flow, r.stepID)
		if grant.Authority == wf.AuthorityWrite && grant.ProducerIsolation == wf.IsolationShared &&
			!stepHasCapability(meta, r.stepID, skillregistry.CapShipping) &&
			r.stepID != failedStepID && idx < failedIdx && workflowStepFeeds(flow, r.stepID, failedStepID) {
			out.repair = append(out.repair, r)
		}
		if grant.Authority == wf.AuthorityReadOnly && grant.ProducerIsolation == wf.IsolationRequired &&
			stepHasCapability(meta, r.stepID, skillregistry.CapVerification) {
			out.verify = append(out.verify, r)
		}
		if grant.Authority == wf.AuthorityReadOnly && grant.ProducerIsolation == wf.IsolationRequired &&
			stepHasCapability(meta, r.stepID, skillregistry.CapReview) {
			out.review = append(out.review, r)
		}
	}
	if err := rows.Err(); err != nil {
		return episodeRoles{}, dbErrCause(err, "read episode invocations")
	}

	// The lineage overlay: role rows whose own result is not the chain's live
	// end take the replacement's result fields. Applied after classification
	// so the shapes stay frozen-generation facts (the replacement invocation
	// is for the SAME step — a retry covers its failed step only — so the
	// classification cannot change under the overlay).
	replacements, aerr := loadEpisodeRetryReplacements(ctx, tx, episodeID)
	if aerr != nil {
		return episodeRoles{}, aerr
	}
	if len(replacements) > 0 {
		overlay := func(rows []episodeResultRow) []episodeResultRow {
			for i := range rows {
				cur := rows[i].stepAttemptID
				for {
					node, ok := replacements[cur]
					if !ok || node.stepAttemptID == "" {
						break // chain end: this invocation's result speaks for the role
					}
					rows[i].stepAttemptID = node.stepAttemptID
					rows[i].status = node.status
					if node.hasResult {
						rows[i].createdAt = node.createdAt
						rows[i].id = node.id
					} else {
						// An open replacement: the role is outstanding again,
						// incomplete by construction, zero time — the same shape
						// an open E-bound invocation has.
						rows[i].createdAt = time.Time{}
						rows[i].id = ""
					}
					cur = node.stepAttemptID
				}
			}
			return rows
		}
		out.repair = overlay(out.repair)
		out.verify = overlay(out.verify)
		out.review = overlay(out.review)
	}
	return out, nil
}

// episodeReplacementNode is one LIVE (non-superseded) replacement invocation
// of a retry descended from an episode, with its recorded result if any.
type episodeReplacementNode struct {
	stepAttemptID string
	status        string
	createdAt     time.Time
	id            string
	hasResult     bool
}

// loadEpisodeRetryReplacements maps failedStepAttemptID → the live
// replacement invocation of the retry authorized for exactly that failed
// attempt, for every retry whose parent_episode_id names this episode. A
// retry binds at most one live invocation ever (its bind check counts
// non-superseded invocations, and reconcile-superseded ones do not count), so
// the map has one entry per retry chain hop; a retry whose replacement was
// superseded and not yet re-bound contributes no entry, leaving the chain to
// end at the failed invocation below it. Ordered by (created_at, id) so a
// corrupt duplicate would leave the LATEST replacement in the map, never the
// stale one.
//
// The LEFT JOIN's nullable inv.step_attempt_id is COALESCEd to the empty
// string — the same discipline the B1 regression pinned for res.created_at
// in the query one function up: a retry authorized but not yet started, or
// whose only replacement was reconciled away, must answer the EMPTY chain-end sentinel
// the loop below reads, never a NULL scan error that classifies as a DB
// fault and rolls the caller's whole result transaction back (aihub#708
// Astra re-review blocker, held by
// TestWorkflowEpisodeRetryAuthorizedNotStartedStaysUnresolved and
// TestWorkflowEpisodeRetrySupersededReplacementStaysUnresolved).
func loadEpisodeRetryReplacements(ctx context.Context, tx pgx.Tx, episodeID string) (map[string]episodeReplacementNode, *AihubError) {
	rows, err := tx.Query(ctx, `
		SELECT r.failed_step_attempt_id, COALESCE(inv.step_attempt_id, ''),
		       COALESCE(res.status,''), res.created_at, COALESCE(res.id,'')
		FROM wi_workflow_repair_episodes r
		LEFT JOIN wi_workflow_invocations inv
		       ON inv.repair_episode_id = r.id AND inv.status <> 'superseded'
		LEFT JOIN wi_workflow_results res ON res.invocation_id = inv.id
		WHERE r.parent_episode_id = $1
		ORDER BY res.created_at, res.id`, episodeID)
	if err != nil {
		return nil, dbErrCause(err, "read episode retry lineage")
	}
	defer rows.Close()
	out := make(map[string]episodeReplacementNode)
	for rows.Next() {
		var failedAttempt, invAttempt, resStatus, resID string
		var createdAt *time.Time
		if err := rows.Scan(&failedAttempt, &invAttempt, &resStatus, &createdAt, &resID); err != nil {
			return nil, dbErrCause(err, "read episode retry lineage")
		}
		if invAttempt == "" {
			continue // no live replacement under this retry (superseded, unbound)
		}
		node := episodeReplacementNode{stepAttemptID: invAttempt, status: resStatus}
		if createdAt != nil {
			node.createdAt = *createdAt
			node.id = resID
			node.hasResult = true
		}
		out[failedAttempt] = node
	}
	if err := rows.Err(); err != nil {
		return nil, dbErrCause(err, "read episode retry lineage")
	}
	return out, nil
}

// ─── Result ──────────────────────────────────────────────────────────────────

// RecordWorkflowResultRequest is the POST /workflow/result body: attempt
// credentials plus the worker's structured StepResult envelope.
type RecordWorkflowResultRequest struct {
	AttemptID     string          `json:"attempt_id"`
	ClaimEpoch    int64           `json:"claim_epoch"`
	SessionSecret string          `json:"session_secret"`
	Result        json.RawMessage `json:"result"`
}

// WorkflowResultRecorded is the response: what was recorded and what it did.
type WorkflowResultRecorded struct {
	WorkItemID    string `json:"work_item_id"`
	StepsVersion  int    `json:"steps_version"`
	StepID        string `json:"step_id"`
	StepAttemptID string `json:"step_attempt_id"`
	Status        string `json:"status"`
	ReviewVerdict string `json:"review_verdict,omitempty"`
	// Paused is true when this result was a review FAIL: the attempt and the
	// work item were paused in the same transaction (spec D8: FAIL records
	// evidence and pauses; it is never a terminal failure).
	Paused bool `json:"paused"`
}

// RecordWorkflowResult records one worker result for an open invocation.
//
// Identity fence, in order: current attempt credentials; the result must name
// the CURRENT generation; the step attempt must be an OPEN invocation of this
// attempt with this producer and this epoch. A stale result (superseded epoch,
// superseded generation, unknown or already-recorded step attempt, forged
// producer) answers 409 with the reason in details and completes nothing —
// the invocation stays open, the recoverable state is intact.
//
// Persisting is ONE transaction: result row, invocation close, step-history
// row, timeline events, and — on a review FAIL — the pause. Either all of it
// landed or none of it did.
func RecordWorkflowResult(ctx context.Context, pool *pgxpool.Pool, idOrSlug string, req RecordWorkflowResultRequest) (*WorkflowResultRecorded, *AihubError) {
	var result wf.StepResult
	if len(req.Result) == 0 {
		return nil, NewErr(ErrBadRequest, "result is required")
	}
	// wf.StepResult.UnmarshalJSON also refuses a worker-supplied "approval"
	// key: the escalation this endpoint exists to make impossible.
	if err := json.Unmarshal(req.Result, &result); err != nil {
		return nil, NewErr(ErrBadRequest, fmt.Sprintf("result is not a valid step result: %v", err))
	}

	tx, err := pool.Begin(ctx)
	if err != nil {
		return nil, NewErr(ErrInternalError, "begin workflow result tx")
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	w, wp, aerr := getWorkflowWIOnTx(ctx, tx, idOrSlug, true)
	if aerr != nil {
		return nil, aerr
	}
	// Lifecycle fence (aihub#708 B2): lock the row, THEN verify — the same
	// order every other attempt-mutating route uses — and hold the lock through
	// the result INSERT, the invocation close, the review-FAIL pause and the
	// repair close, all in this one transaction. A pause or takeover that
	// commits after the credential check would otherwise let a dead attempt's
	// result land (and, on a review FAIL, write a pause the transition already
	// superseded); with the lock, the losing request re-reads the moved row and
	// leaves nothing behind.
	if aerr := verifyAttemptCredential(ctx, tx, *w, req.AttemptID, req.ClaimEpoch, req.SessionSecret); aerr != nil {
		return nil, aerr
	}
	if wp.StepsVersion == 0 {
		return nil, NewErr(ErrConflictStepAttemptMismatch, "this work item has no workflow; no result is expected")
	}
	if result.WorkItemID != w.ID {
		return nil, NewErr(ErrConflictStepAttemptMismatch, fmt.Sprintf(
			"result names work item %q but it was submitted for %q", result.WorkItemID, w.ID))
	}
	if result.FlowVersion != wp.StepsVersion {
		return nil, NewErrDetails(ErrConflictStepAttemptMismatch,
			fmt.Sprintf("result is for workflow generation %d but the current generation is %d; it was superseded by a revision",
				result.FlowVersion, wp.StepsVersion),
			map[string]any{"result_flow_version": result.FlowVersion, "current_steps_version": wp.StepsVersion})
	}

	inv, aerr := loadOpenInvocation(ctx, tx, w.ID, wp.StepsVersion, result.StepID, result.StepAttemptID)
	if aerr != nil {
		return nil, aerr
	}
	if inv.runAttemptID != req.AttemptID {
		return nil, NewErr(ErrConflictEpochMismatch, fmt.Sprintf(
			"step attempt %s belongs to attempt %s, not the current attempt %s; nothing was recorded",
			result.StepAttemptID, inv.runAttemptID, req.AttemptID))
	}
	if inv.claimEpoch != req.ClaimEpoch || int64(result.Epoch) != req.ClaimEpoch {
		return nil, NewErr(ErrConflictEpochMismatch, fmt.Sprintf(
			"result epoch %d does not match the invocation's epoch %d; nothing was recorded", result.Epoch, inv.claimEpoch))
	}
	if result.ProducerID != inv.producerID {
		return nil, NewErr(ErrAttemptMismatch, fmt.Sprintf(
			"result producer %q does not match the invocation's server-assigned producer %q; the producer identity cannot be forged",
			result.ProducerID, inv.producerID))
	}

	// Shape validation against the generation's frozen metadata.
	stepMeta, aerr := workflowStepMetaFor(ctx, tx, w.ID, wp.StepsVersion, result.StepID)
	if aerr != nil {
		return nil, aerr
	}
	if err := validateWorkflowResultShape(result, stepMeta); err != nil {
		return nil, NewErr(ErrBadRequest, fmt.Sprintf("step %q returned an invalid result: %v", result.StepID, err))
	}

	// Authoritative artifact resolution (aihub#708 final Astra blocker 2):
	// a COMPLETED result's artifact triple is not taken on the worker's word.
	// It must name an actual methodology artifact — a memories row of type
	// methodology.* bound to THIS work item — whose stored structured output
	// hashes to the claimed digest, is readable by the recording attempt's
	// actor, and satisfies the pinned output schema where the pinned skill
	// version is still accessible. Non-completed results keep the shape check
	// alone: an honest blocked/incomplete/provider_error advances nothing, so
	// nothing is built on its artifact, and wedging a recoverable report on an
	// artifact-resolution refusal would make a provider failure unrecoverable.
	// This runs BEFORE any write, inside the same fenced transaction — a
	// refused artifact leaves no result row, no closed invocation, no events,
	// and the open invocation stays recoverable, exactly like every identity
	// refusal above it.
	if result.Status == wf.StatusCompleted {
		if aerr := resolveCompletedResultArtifact(ctx, tx, w, wp, req.AttemptID, result); aerr != nil {
			return nil, aerr
		}
	}

	artifactJSON, mErr := json.Marshal(result.Artifact)
	if mErr != nil {
		return nil, NewErr(ErrInternalError, "artifact does not serialize")
	}
	evidenceJSON, eErr := json.Marshal(result.Evidence)
	if eErr != nil {
		return nil, NewErr(ErrInternalError, "evidence does not serialize")
	}

	if _, err := tx.Exec(ctx, `
		INSERT INTO wi_workflow_results
		    (id, work_item_id, invocation_id, steps_version, step_id, step_attempt_id,
		     run_attempt_id, claim_epoch, producer_id, status, review_verdict, artifact, evidence, raw)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, NULLIF($11,''), $12, $13, $14)`,
		NewID("wres"), w.ID, inv.id, wp.StepsVersion, result.StepID, result.StepAttemptID,
		req.AttemptID, req.ClaimEpoch, result.ProducerID, string(result.Status), string(result.ReviewVerdict),
		artifactJSON, evidenceJSON, []byte(req.Result)); err != nil {
		return nil, dbErrCause(err, "insert workflow result")
	}

	if _, err := tx.Exec(ctx, `
		UPDATE wi_workflow_invocations SET status = 'recorded', recorded_at = clock_timestamp()
		WHERE id = $1`, inv.id); err != nil {
		return nil, dbErrCause(err, "close workflow invocation")
	}

	// Workflow results are step history in the WORKFLOW's own table only:
	// every wi_workflow_results row carries its full identity (work item,
	// generation, step, step attempt, run attempt, epoch, producer), and the
	// workflow readers all select by exactly that identity. The legacy
	// wi_step_completions table is the scenario graph's: its readers filter by
	// work_item_id alone, so a workflow row copied there would let a SUPERSEDED
	// generation's result masquerade as current step history to every legacy
	// reader — pf_get_step's completed_steps above all (aihub#708 review
	// blocker: "legacy readers can interpret superseded result"). The copy is
	// therefore not made at all: isolation rather than a discriminator,
	// because the legacy readers cannot be taught to filter, and a workflow
	// work item has no scenario step graph for such rows to belong to anyway.
	//
	// Timeline parity is kept: the same step_completed/step_failed events the
	// legacy path emits, payload enriched with the workflow identity.
	eventType := "step_failed"
	if result.Status == wf.StatusCompleted {
		eventType = "step_completed"
	}
	actorUserID, actorDisplay, aerr := attemptActor(ctx, tx, req.AttemptID)
	if aerr != nil {
		return nil, aerr
	}
	if aerr := insertWorkflowEvent(ctx, tx, w.ID, w.Project, actorUserID, actorDisplay, eventType, map[string]any{
		"step":            result.StepID,
		"steps_version":   wp.StepsVersion,
		"step_attempt_id": result.StepAttemptID,
		"status":          string(result.Status),
		"review_verdict":  string(result.ReviewVerdict),
		"artifact":        result.Artifact,
	}); aerr != nil {
		return nil, aerr
	}

	paused := false
	if result.ReviewVerdict == wf.ReviewFail {
		paused = true
		if aerr := pauseAttemptForReviewFail(ctx, tx, w, req.AttemptID, result); aerr != nil {
			return nil, aerr
		}
	}

	// Close a repair authorization when its bounded set is COMPLETE: a retry
	// when its one invocation has a recorded result, an episode only when all
	// three roles (repair producer, fresh verification, fresh review) have
	// recorded completed results and the episode semantics hold — the pure
	// policy validates complete episodes, never first-recorded ones.
	if inv.repairEpisodeID != nil && *inv.repairEpisodeID != "" {
		if aerr := closeRepairEpisodeIfComplete(ctx, tx, *inv.repairEpisodeID); aerr != nil {
			return nil, aerr
		}
		// The lineage's second close trigger (mem_FHuxXIXI): a retry whose
		// replacement just recorded may have completed its PARENT EPISODE's
		// effective role set. The last role to complete can itself be a chain
		// replacement — a fresh review that provider_errored, retried, whose
		// replacement records the closing PASS — and that recording's own
		// authorization is the RETRY, so without this propagation the episode
		// would never be evaluated for close in the transaction that completed
		// it: it leaks open (counting against the open-authorization bound and
		// the read model) while every gate that consults the effective set
		// already answers complete. closeRepairEpisodeIfComplete is a no-op on
		// a closed or incomplete episode, so this is safe on every path.
		if aerr := closeParentEpisodeAfterRetry(ctx, tx, *inv.repairEpisodeID); aerr != nil {
			return nil, aerr
		}
	}

	if err := tx.Commit(ctx); err != nil {
		if aerr := retryConflictErr(err, "commit workflow result"); aerr != nil {
			return nil, aerr
		}
		return nil, NewErr(ErrInternalError, "commit workflow result")
	}

	return &WorkflowResultRecorded{
		WorkItemID:    w.ID,
		StepsVersion:  wp.StepsVersion,
		StepID:        result.StepID,
		StepAttemptID: result.StepAttemptID,
		Status:        string(result.Status),
		ReviewVerdict: string(result.ReviewVerdict),
		Paused:        paused,
	}, nil
}

// openWorkflowInvocation is the row shape RecordWorkflowResult fences against.
type openWorkflowInvocation struct {
	id              string
	runAttemptID    string
	claimEpoch      int64
	producerID      string
	stepID          string
	status          string
	repairEpisodeID *string
}

func loadOpenInvocation(ctx context.Context, tx pgx.Tx, wiID string, stepsVersion int, stepID, stepAttemptID string) (*openWorkflowInvocation, *AihubError) {
	var inv openWorkflowInvocation
	err := tx.QueryRow(ctx, `
		SELECT id, run_attempt_id, claim_epoch, producer_id, step_id, status, repair_episode_id
		FROM wi_workflow_invocations
		WHERE work_item_id = $1 AND steps_version = $2 AND step_attempt_id = $3`,
		wiID, stepsVersion, stepAttemptID).Scan(
		&inv.id, &inv.runAttemptID, &inv.claimEpoch, &inv.producerID, &inv.stepID, &inv.status, &inv.repairEpisodeID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, NewErr(ErrConflictStepAttemptMismatch, fmt.Sprintf(
			"step attempt %q is not an invocation of this work item's current workflow generation; nothing was recorded", stepAttemptID))
	}
	if err != nil {
		return nil, dbErrCause(err, "read workflow invocation")
	}
	if inv.stepID != stepID {
		return nil, NewErr(ErrConflictStepAttemptMismatch, fmt.Sprintf(
			"step attempt %q belongs to step %q, not %q", stepAttemptID, inv.stepID, stepID))
	}
	if inv.status == "superseded" {
		return nil, NewErr(ErrConflictStepAttemptMismatch, fmt.Sprintf(
			"step attempt %q was superseded without a result when its dead attempt was reconciled; record against the replacement invocation the live attempt started", stepAttemptID))
	}
	if inv.status != "open" {
		return nil, NewErr(ErrConflictStepAttemptMismatch, fmt.Sprintf(
			"step attempt %q already has a recorded result; a retry needs an explicit repair authorization", stepAttemptID))
	}
	return &inv, nil
}

// workflowStepMetaFor loads one step's frozen semantic metadata from the
// generation row.
func workflowStepMetaFor(ctx context.Context, tx pgx.Tx, wiID string, stepsVersion int, stepID string) (wiStepMeta, *AihubError) {
	var metaRaw []byte
	if err := tx.QueryRow(ctx,
		`SELECT step_meta FROM wi_workflow_generations WHERE work_item_id = $1 AND steps_version = $2`,
		wiID, stepsVersion).Scan(&metaRaw); err != nil {
		return wiStepMeta{}, dbErrCause(err, "read workflow generation metadata")
	}
	var meta map[string]wiStepMeta
	if err := json.Unmarshal(metaRaw, &meta); err != nil {
		return wiStepMeta{}, NewErr(ErrInternalError, "stored workflow metadata does not parse")
	}
	m, ok := meta[stepID]
	if !ok {
		return wiStepMeta{}, NewErr(ErrInternalError, fmt.Sprintf("workflow metadata is missing step %q", stepID))
	}
	return m, nil
}

// validateWorkflowResultShape is the server-side mirror of the pure package's
// per-result rules (internal/workflow policy.validateResult — unexported, and
// that is the reported API gap this function works around). Same vocabulary,
// same artifact/evidence shapes, same review-gate requirements — enforced
// against the generation's FROZEN metadata so a running execution stays
// validatable even if registry access was revoked after the invocation was
// authorized (spec D3: a running execution finishes).
func validateWorkflowResultShape(result wf.StepResult, meta wiStepMeta) error {
	switch result.Status {
	case wf.StatusCompleted, wf.StatusIncomplete, wf.StatusBlocked, wf.StatusProviderError, wf.StatusInvalidResult:
	default:
		return fmt.Errorf("unknown status %q", result.Status)
	}
	if result.Artifact.ID == "" || result.Artifact.Version <= 0 || !isSHA256Digest(result.Artifact.Hash) {
		return errors.New("artifact must carry id, positive version and a full sha256 digest")
	}
	for i, e := range result.Evidence {
		if e.Kind == "" || strings.TrimSpace(e.Ref) == "" || !isSHA256Digest(e.Hash) {
			return fmt.Errorf("evidence %d must carry kind, ref and a full sha256 digest", i)
		}
	}
	isReview := false
	hasVerification := false
	for _, c := range meta.Capabilities {
		if c == skillregistry.CapReview {
			isReview = true
		}
		if c == skillregistry.CapVerification {
			hasVerification = true
		}
	}
	if isReview {
		switch result.ReviewVerdict {
		case wf.ReviewPass, wf.ReviewWarn, wf.ReviewFail:
		case "":
			// Mirror of policy.validateResult: a provider infrastructure error
			// is distinct from a review FAIL and may carry no verdict; only a
			// COMPLETED review must always carry one, and a malformed verdict
			// is refused whatever the status.
			if result.Status != wf.StatusProviderError {
				return errors.New("review result is missing a pass/warn/fail verdict")
			}
		default:
			return errors.New("review result is missing a pass/warn/fail verdict")
		}
	} else if result.ReviewVerdict != "" {
		return errors.New("non-review result carries a review verdict")
	}
	if (isReview || hasVerification) && result.Status == wf.StatusCompleted && len(result.Evidence) == 0 {
		return errors.New("a completed review or verification gate requires evidence")
	}
	return nil
}

// isSHA256Digest accepts exactly "sha256:" followed by 64 LOWERCASE hex
// characters. The suffix is DECODED, not merely length-checked: a 64-byte
// suffix of non-hex characters passed the old prefix+length test and stored a
// digest no consumer could ever recompute against an artifact (aihub#708
// review blocker: "digest suffix not hex"). Lowercase is part of the shape
// — the pure package's sha256RE and the DB CHECK constraints both say
// [0-9a-f], and hex.DecodeString alone would accept uppercase — so the check
// is decode + vocabulary, matching every other layer.
func isSHA256Digest(s string) bool {
	const prefix = "sha256:"
	if !strings.HasPrefix(s, prefix) || len(s) != len(prefix)+64 {
		return false
	}
	suffix := s[len(prefix):]
	if suffix != strings.ToLower(suffix) {
		return false
	}
	_, err := hex.DecodeString(suffix)
	return err == nil
}

// ─── Completed-result artifact resolution (aihub#708 final Astra blocker 2) ───

// workflowArtifactDigest is the server-side twin of the artifact digest the
// controller's session path computes before recording (internal/controller's
// WorkflowArtifactHash). The two must stay byte-compatible: the rule is
// sha256 over encoding/json's deterministic serialization of the artifact's
// STRUCTURED OUTPUT — the attrs.structured_payload object a methodology
// artifact stores — so anyone who can read the artifact can recompute the
// digest a result claims, and a fabricated hash never survives its own
// resolution. internal/controller is not importable from this package (it
// sits on pkg/client, which this domain sits under), so this copy lives
// beside its only server-side caller — the same mirror discipline
// validateWorkflowResultShape applies to the pure package's unexported
// policy.validateResult, and for the same reported-API-gap reason.
func workflowArtifactDigest(payload map[string]any) string {
	b, _ := json.Marshal(payload)
	sum := sha256.Sum256(b)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// resolveCompletedResultArtifact resolves a COMPLETED result's artifact
// triple to the actual methodology artifact it names, refusing every class
// the Astra review named. In order:
//
//	nonexistent          the id is not a memories row at all
//	wrong work item      the row exists but is bound to another work item
//	                      (or to none)
//	not an artifact      the row is not a methodology.* memory
//	redacted             the artifact was withdrawn (redaction is soft-delete;
//	                      a superseded/archived row stays citable — its bytes
//	                      are immutable and its lineage moved to a NEW id, so
//	                      the triple still names exactly what was produced)
//	wrong version        the claimed version is not the row's immutable
//	                      per-id version (always 1: a memory id IS one
//	                      version; revisions are new ids — the artifact model
//	                      internal/controller's session path documents)
//	not accessible       the recording attempt's actor cannot read the row
//	                      under the memory visibility rules
//	no structured output the row carries no attrs.structured_payload, so no
//	                      digest could bind the claim to the content
//	wrong digest         the stored structured output does not hash to the
//	                      claimed artifact hash
//	schema-invalid       the structured output violates the immutable pinned
//	                      output schema, even when registry sharing was revoked
//	                      after invocation authorization
//
// The refusal is whole-transactional: this runs before the result INSERT, so
// a refusal completes nothing (the invocation stays open, the recoverable
// state intact) — the same guarantee every identity fence in
// RecordWorkflowResult already carries. The existence read is UNSCOPED so
// "does not exist" and "not yours to read" can be distinct answers (the
// digest is what binds the claim to content, and the caller here is the
// authenticated attempt of the very work item the artifact must belong to,
// so there is no existence oracle to leak); the ACCESS check then goes
// through memoryVisibilityScopeSQL — THE single SQL copy of the memory
// visibility rule (aihub#379), never an inline restatement.
//
// The schema arm is WHERE AVAILABLE by design: a generation freezes
// references and derived metadata, never bundles or schemas (workflow.go
// rule 3), so the pinned output schema can only come from the live registry
// — and spec D3 says a running execution FINISHES even if registry access
// lapsed after the invocation was authorized (a revoked share or a
// visibility flip blocks the NEXT start, not the in-flight result). An
// inaccessible pinned version therefore skips ONLY this contract re-check;
// the existence/binding/version/access/digest fences above all still hold,
// because they read the memories row, not the registry.
func resolveCompletedResultArtifact(ctx context.Context, tx pgx.Tx, w *WorkItem, wp *workflowPointer, attemptID string, result wf.StepResult) *AihubError {
	// The actor whose memory-visibility and registry views decide access —
	// the user who owns the recording attempt, the same UserRecord the start
	// path's registry recheck resolves (userRecordForAttempt).
	actor, aerr := userRecordForAttempt(ctx, tx, attemptID)
	if aerr != nil {
		return aerr
	}

	// Existence, binding, type and status — one UNSCOPED read (see the
	// function comment for why the scope lives in its own probe below).
	var boundWI *string
	var memType, status string
	var attrsRaw []byte
	err := tx.QueryRow(ctx, `
		SELECT work_item_id, type, status, attrs FROM memories WHERE id = $1`,
		result.Artifact.ID).Scan(&boundWI, &memType, &status, &attrsRaw)
	if errors.Is(err, pgx.ErrNoRows) {
		return NewErr(ErrBadRequest, fmt.Sprintf(
			"step %q returned an invalid result: artifact %q does not exist as a stored artifact; a completed result must cite the methodology artifact the step actually produced",
			result.StepID, result.Artifact.ID))
	}
	if err != nil {
		return dbErrCause(err, "read workflow result artifact")
	}
	if boundWI == nil || *boundWI != w.ID {
		owner := "no work item"
		if boundWI != nil {
			owner = *boundWI
		}
		return NewErr(ErrBadRequest, fmt.Sprintf(
			"step %q returned an invalid result: artifact %q is bound to %s, not this work item (%s)",
			result.StepID, result.Artifact.ID, owner, w.ID))
	}
	if !strings.HasPrefix(memType, "methodology.") {
		return NewErr(ErrBadRequest, fmt.Sprintf(
			"step %q returned an invalid result: artifact %q is a %q memory; a workflow artifact must be a methodology.* artifact",
			result.StepID, result.Artifact.ID, memType))
	}
	if status == "redacted" {
		return NewErr(ErrBadRequest, fmt.Sprintf(
			"step %q returned an invalid result: artifact %q was redacted and can no longer back a completed result",
			result.StepID, result.Artifact.ID))
	}
	if result.Artifact.Version != 1 {
		return NewErr(ErrBadRequest, fmt.Sprintf(
			"step %q returned an invalid result: artifact %q is claimed at version %d; a workflow artifact id is one immutable memory version (a revision is a NEW id), so the version is always 1",
			result.StepID, result.Artifact.ID, result.Artifact.Version))
	}

	// Access, through THE single-copy visibility predicate — the same rule
	// every other caller-scoped memories reader applies, recomputed from the
	// current rows (no cached grant: a visibility flip blocks this very
	// result). Admins pass by construction (the predicate is empty for them).
	visClause, visArgs, _ := memoryVisibilityScopeSQL(actor.Role, actor.ID, 2)
	probeArgs := append([]any{result.Artifact.ID}, visArgs...)
	var visible int
	err = tx.QueryRow(ctx, fmt.Sprintf(`
		SELECT 1 FROM memories WHERE id = $1%s`, visClause), probeArgs...).Scan(&visible)
	if errors.Is(err, pgx.ErrNoRows) {
		return NewErr(ErrForbidden, fmt.Sprintf(
			"step %q returned an invalid result: artifact %q is not readable by the recording attempt's user; a completed result cannot cite another user's private artifact",
			result.StepID, result.Artifact.ID))
	}
	if err != nil {
		return dbErrCause(err, "check workflow result artifact visibility")
	}

	// The digest binds the claim to the content: the claimed hash must be the
	// digest of the artifact's stored STRUCTURED OUTPUT. No structured output
	// means no digest could ever be recomputed — the claim is unverifiable and
	// therefore refused, not waived.
	var attrs map[string]any
	if len(attrsRaw) > 0 {
		dec := json.NewDecoder(bytes.NewReader(attrsRaw))
		dec.UseNumber()
		if err := dec.Decode(&attrs); err != nil {
			return NewErr(ErrInternalError, fmt.Sprintf("stored artifact %s attrs do not parse", result.Artifact.ID))
		}
		var trailing any
		if err := dec.Decode(&trailing); err != io.EOF {
			return NewErr(ErrInternalError, fmt.Sprintf("stored artifact %s attrs contain trailing data", result.Artifact.ID))
		}
	}
	payload, hasPayload := attrs["structured_payload"].(map[string]any)
	if !hasPayload || payload == nil {
		return NewErr(ErrBadRequest, fmt.Sprintf(
			"step %q returned an invalid result: artifact %q carries no structured output object (attrs.structured_payload); the artifact digest is computed over it, so the claim cannot be verified",
			result.StepID, result.Artifact.ID))
	}
	if got := workflowArtifactDigest(payload); got != result.Artifact.Hash {
		return NewErr(ErrBadRequest, fmt.Sprintf(
			"step %q returned an invalid result: artifact hash %s does not match artifact %q's stored structured output (recomputed %s)",
			result.StepID, result.Artifact.Hash, result.Artifact.ID, got))
	}

	// The contract arm validates the pinned step's output schema. The
	// generation deliberately freezes no schema bytes (workflow.go rule 3),
	// so the exact immutable contract is loaded by the trusted server-side
	// lookup below rather than through the actor's current registry view.
	var flow wf.Flow
	if len(wp.Steps) == 0 {
		return NewErr(ErrInternalError, "work item has a workflow pointer but no stored flow")
	}
	if err := json.Unmarshal(wp.Steps, &flow); err != nil {
		return NewErr(ErrInternalError, "stored workflow flow does not parse")
	}
	var pinned *wf.Step
	for i := range flow.Steps {
		if flow.Steps[i].ID == result.StepID {
			pinned = &flow.Steps[i]
			break
		}
	}
	if pinned == nil {
		return NewErr(ErrInternalError, fmt.Sprintf("stored workflow is missing step %q", result.StepID))
	}
	// The contract arm uses an internal trusted lookup of the exact immutable
	// pinned version. This bypasses caller visibility only for server-side
	// validation of an already-authorized invocation; it exposes no contract
	// content and does not weaken pinning or the next-start access check.
	ref, aerr := resolvePinnedSkillRefTrusted(ctx, tx, pinned.SkillID, pinned.SkillVersion)
	if aerr != nil {
		return aerr
	}
	if len(ref.contract.OutputSchema) == 0 {
		return nil // no declared output contract: nothing to check against
	}
	schema, cerr := skillregistry.CompileSchema(ref.contract.OutputSchema)
	if cerr != nil {
		// Publication compiled this schema (ValidateContract); a stored
		// contract that no longer compiles is a data defect — fail closed
		// rather than validate against something we cannot interpret.
		return NewErr(ErrInternalError, fmt.Sprintf("stored skill contract's output schema is invalid: %v", cerr))
	}
	if verr := schema.CheckValue(payload); verr != nil {
		return NewErr(ErrBadRequest, fmt.Sprintf(
			"step %q returned an invalid result: artifact %q does not satisfy the pinned output schema: %v",
			result.StepID, result.Artifact.ID, verr))
	}
	return nil
}

// pauseAttemptForReviewFail is the FAIL→pause half of spec D8, mirroring the
// paused branch of FnCompleteAttempt (run_attempt status, file_scope-only lock
// release with cause-bearing events, work_items.status) inside the caller's
// result transaction — the pause lands WITH the result or not at all. The work
// item is never marked failed: repeat recovery is authorized repair, never a
// terminal write.
func pauseAttemptForReviewFail(ctx context.Context, tx pgx.Tx, w *WorkItem, attemptID string, result wf.StepResult) *AihubError {
	reason := fmt.Sprintf("review FAIL on workflow step %q (generation %d, step attempt %s)",
		result.StepID, result.FlowVersion, result.StepAttemptID)
	if _, err := tx.Exec(ctx, `
		UPDATE run_attempts SET status='paused', ended_at=clock_timestamp(), pause_reason=$1
		WHERE id=$2`, reason, attemptID); err != nil {
		return dbErrCause(err, "pause run attempt for review fail")
	}
	// File_scope-only release, the pause branch's retention rule: resume may
	// keep holding the branch/env locks. Through releaseLocks so every removed
	// row leaves its lock_released event with cause=attempt_paused.
	if _, relErr := releaseLocks(ctx, tx, acquireLocksReleasePausedSQL,
		newLockOp(lockCauseAttemptPaused, lockEventActor{}).withExtra(map[string]any{
			"retained_types": "git_branch, deploy_env, worktree, tcp_port",
			"reason":         reason,
		}), attemptID); relErr != nil {
		return dbErr(relErr, "release file_scope locks for review-fail pause")
	}
	if _, err := tx.Exec(ctx, `UPDATE work_items SET status='paused' WHERE id=$1`, w.ID); err != nil {
		return dbErrCause(err, "pause work item for review fail")
	}
	return insertWorkflowEvent(ctx, tx, w.ID, w.Project, w.ReporterUserID, "", "attempt_completed", map[string]any{
		"status":          "paused",
		"pause_reason":    reason,
		"step_attempt_id": result.StepAttemptID,
		"review_verdict":  string(result.ReviewVerdict),
	})
}

// closeRepairEpisodeIfComplete closes an authorization only when its bounded
// invocation set is COMPLETE — and, for an episode, when the recorded set
// satisfies the pure policy's episode semantics over the generation's frozen
// grants and metadata (the same predicate family internal/workflow's
// validateRepairEpisodes holds at Decide time; it is unexported, so this is
// the server-side mirror, the same reported-API gap validateWorkflowResultShape
// works around).
//
// A RETRY closes when its single bounded invocation has a recorded result. An
// EPISODE closes only when all three roles — repair producer, fresh
// verification, fresh review — have recorded COMPLETED results, in episode
// order (failed result, then repair, then verification, then review), with
// the repair producer actually feeding the failed gate. Anything less leaves
// the authorization OPEN: the old shape closed after the FIRST bound
// invocation recorded, un-binding the remaining roles and letting a
// half-repaired episode pass as recovered (aihub#708 review blocker:
// "repair episode closes before required invocations").
func closeRepairEpisodeIfComplete(ctx context.Context, tx pgx.Tx, episodeID string) *AihubError {
	var wiID, kind, failedStepID, failedStepAttempt, status string
	var failedVersion int
	err := tx.QueryRow(ctx, `
		SELECT work_item_id, kind, failed_steps_version, failed_step_id, failed_step_attempt_id, status
		FROM wi_workflow_repair_episodes WHERE id = $1`, episodeID,
	).Scan(&wiID, &kind, &failedVersion, &failedStepID, &failedStepAttempt, &status)
	if err != nil {
		return dbErrCause(err, "read repair authorization for close")
	}
	if status != "open" {
		return nil
	}

	complete := false
	switch kind {
	case "retry":
		// One bounded invocation: complete exactly when it has recorded.
		var open, recorded int
		if err := tx.QueryRow(ctx, `
			SELECT
				(SELECT count(*) FROM wi_workflow_invocations WHERE repair_episode_id=$1 AND status='open'),
				(SELECT count(*) FROM wi_workflow_invocations WHERE repair_episode_id=$1 AND status='recorded')`,
			episodeID).Scan(&open, &recorded); err != nil {
			return dbErrCause(err, "count repair invocations")
		}
		complete = open == 0 && recorded > 0
	case "episode":
		ok, aerr := episodeRoleSetComplete(ctx, tx, episodeID, wiID, failedVersion, failedStepID, failedStepAttempt)
		if aerr != nil {
			return aerr
		}
		complete = ok
	default:
		return NewErr(ErrInternalError, fmt.Sprintf("repair authorization %s carries unknown kind %q", episodeID, kind))
	}

	if !complete {
		return nil
	}
	if _, err := tx.Exec(ctx, `
		UPDATE wi_workflow_repair_episodes SET status='closed', closed_at=clock_timestamp()
		WHERE id=$1 AND status='open'`, episodeID); err != nil {
		return dbErrCause(err, "close repair authorization")
	}
	return nil
}

// closeParentEpisodeAfterRetry is the lineage half of the close trigger
// (mem_FHuxXIXI): when the authorization a result just recorded under is a
// RETRY that carries the immutable parent lineage, the parent episode's
// effective role set may have just completed — the recording invocation
// stands in for one of the episode's roles through the chain, and a
// chain-replacement result is never bound to the episode itself. It reads
// the retry's parent_episode_id and evaluates the episode for close with
// the SAME predicate every other close trigger uses; an episode with no
// parent (an ordinary retry) or a parent already closed is a no-op. The
// parent is always an EPISODE id (resolveRetryLineage only ever names an
// episode binding as the chain root), but the predicate handles any kind
// safely rather than trusting that invariant.
func closeParentEpisodeAfterRetry(ctx context.Context, tx pgx.Tx, retryID string) *AihubError {
	var parent *string
	err := tx.QueryRow(ctx, `
		SELECT parent_episode_id FROM wi_workflow_repair_episodes WHERE id = $1`, retryID,
	).Scan(&parent)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil // the caller just closed it; nothing to propagate
	}
	if err != nil {
		return dbErrCause(err, "read retry lineage for episode close")
	}
	if parent == nil || *parent == "" {
		return nil
	}
	return closeRepairEpisodeIfComplete(ctx, tx, *parent)
}

// episodeResultRow is one recorded result of an episode-bound invocation, in
// recording order ((created_at, id), the same tie-break the history readers
// use).
type episodeResultRow struct {
	stepID        string
	stepAttemptID string
	status        string
	createdAt     time.Time
	id            string
}

// episodeRoleSetComplete reports whether an episode's recorded results cover
// the pure policy's three roles — repair producer, fresh verification, fresh
// review — each COMPLETED, in episode order, with the repair producer feeding
// the failed gate. The classification uses ONLY the generation's frozen state
// (flow, grants, step metadata): the close check runs inside the result
// transaction, where re-resolving the live registry is forbidden (spec D3 — a
// running execution finishes even if registry access was revoked after the
// invocation was authorized).
//
// Anything that does not satisfy the mirror leaves the episode open: the pure
// validator remains the final judge at Decide time, and an open episode is
// the loud answer, never a silent half-close.
func episodeRoleSetComplete(ctx context.Context, tx pgx.Tx, episodeID, wiID string, stepsVersion int,
	failedStepID, failedStepAttemptID string) (bool, *AihubError) {

	// The frozen generation: flow, grants and per-step semantic metadata.
	var stepsRaw, grantsRaw, metaRaw []byte
	if err := tx.QueryRow(ctx, `
		SELECT steps, grants, step_meta FROM wi_workflow_generations
		WHERE work_item_id = $1 AND steps_version = $2`, wiID, stepsVersion,
	).Scan(&stepsRaw, &grantsRaw, &metaRaw); err != nil {
		return false, dbErrCause(err, "read workflow generation for episode close")
	}
	var flow wf.Flow
	if err := json.Unmarshal(stepsRaw, &flow); err != nil {
		return false, NewErr(ErrInternalError, "stored workflow flow does not parse")
	}
	var grants map[string]wf.StepGrant
	if err := json.Unmarshal(grantsRaw, &grants); err != nil {
		return false, NewErr(ErrInternalError, "stored workflow grants do not parse")
	}
	var meta map[string]wiStepMeta
	if err := json.Unmarshal(metaRaw, &meta); err != nil {
		return false, NewErr(ErrInternalError, "stored workflow metadata does not parse")
	}

	// The failed review this episode recovers must exist, be a FAIL verdict
	// and sit on a review gate (the authorization already enforced both; the
	// results are immutable, so this is the same fact re-read, not a re-check
	// that could drift).
	var failedVerdict string
	var failedAt time.Time
	var failedRowID string
	err := tx.QueryRow(ctx, `
		SELECT COALESCE(review_verdict,''), created_at, id FROM wi_workflow_results
		WHERE work_item_id = $1 AND steps_version = $2 AND step_attempt_id = $3`,
		wiID, stepsVersion, failedStepAttemptID).Scan(&failedVerdict, &failedAt, &failedRowID)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, dbErrCause(err, "read failed review for episode close")
	}
	if failedVerdict != string(wf.ReviewFail) || !stepHasCapability(meta, failedStepID, skillregistry.CapReview) {
		return false, nil
	}

	// The episode's live invocations and their results, classified into the
	// three roles by the SAME loader the start-time prerequisites use
	// (loadEpisodeRoleRows): a bound invocation with no result yet carries an
	// empty status and means the set is not complete — and a SUPERSEDED
	// invocation (its attempt was fenced by the reconcile transition, aihub#708
	// B3) is not part of the bounded set at all: it is abandoned, its
	// replacement takes the role, and a dead row with no result must not keep
	// the episode open forever. Only live invocations are scanned.
	roles, aerr := loadEpisodeRoleRows(ctx, tx, episodeID, flow, grants, meta, failedStepID)
	if aerr != nil {
		return false, aerr
	}
	repair, verify, review := roles.repair, roles.verify, roles.review
	if len(repair) == 0 || len(verify) == 0 || len(review) == 0 {
		return false, nil
	}
	// Every role result must be completed — the pure policy: "repair episode
	// requires completed repair, verification, and review".
	for _, rs := range [][]episodeResultRow{repair, verify, review} {
		for _, r := range rs {
			if r.status != string(wf.StatusCompleted) {
				return false, nil
			}
		}
	}
	// Episode order, the pure policy's event-order chain: failed result,
	// then repair, then verification, then review. rows are ordered by
	// (created_at, id), so the first slice element of each role is its earliest
	// recorded result.
	before := func(a, b episodeResultRow) bool {
		return a.createdAt.Before(b.createdAt) || (a.createdAt.Equal(b.createdAt) && a.id < b.id)
	}
	failedRow := episodeResultRow{createdAt: failedAt, id: failedRowID}
	if !before(failedRow, repair[0]) || !before(repair[0], verify[0]) || !before(verify[0], review[0]) {
		return false, nil
	}
	return true, nil
}

// ─── Approval ────────────────────────────────────────────────────────────────

// ApproveWorkflowRequest is the POST /workflow/approve body. There is NO actor
// field: the actor is the authenticated principal the route carries.
type ApproveWorkflowRequest struct {
	StepsVersion int            `json:"steps_version"`
	StepID       string         `json:"step_id"`
	Artifact     wf.ArtifactRef `json:"artifact"`
	Decision     string         `json:"decision"`
}

// WorkflowApproval is the recorded human decision.
type WorkflowApproval struct {
	ID           string         `json:"id"`
	WorkItemID   string         `json:"work_item_id"`
	StepsVersion int            `json:"steps_version"`
	StepID       string         `json:"step_id"`
	Artifact     wf.ArtifactRef `json:"artifact"`
	Decision     string         `json:"decision"`
	ActorUserID  string         `json:"actor_user_id"`
	ActorDisplay string         `json:"actor_display"`
	CreatedAt    time.Time      `json:"created_at"`
}

// approveScanColumns is the shared column list for scanning a
// wi_workflow_approvals row into a WorkflowApproval.
const approveScanColumns = `
	id, work_item_id, steps_version, step_id, artifact_id, artifact_version, artifact_hash,
	decision, actor_user_id, actor_display, created_at`

func scanWorkflowApproval(row pgx.Row) (*WorkflowApproval, error) {
	var a WorkflowApproval
	err := row.Scan(&a.ID, &a.WorkItemID, &a.StepsVersion, &a.StepID, &a.Artifact.ID, &a.Artifact.Version,
		&a.Artifact.Hash, &a.Decision, &a.ActorUserID, &a.ActorDisplay, &a.CreatedAt)
	if err != nil {
		return nil, err
	}
	return &a, nil
}

// ApproveWorkflowStep records a HUMAN decision on the exact artifact of a
// step's latest recorded result (spec D7: "Human approval is a distinct
// authenticated action bound to artifact ID/version/digest and flow/step
// identity; resolving annotation is not approval").
//
// Three fences, all before any write:
//   - actorType must be "human" — a machine user's credential may hold writer
//     on the project and still cannot approve. Model prose cannot approve
//     itself, and no request field can name a different actor: the actor is
//     the authenticated principal, full stop.
//   - the artifact triple must EQUAL the latest recorded result's artifact for
//     that (generation, step). An approval names exactly what was produced; a
//     superseded generation's approvals stay readable in history but never
//     carry across a revision.
//   - the step must actually gate on the human (effective RHS: WI
//     requires_human_session AND step rhs). An approval on a non-gating step
//     is refused rather than stored as a no-op — a stored no-op reads as
//     satisfied to every later reader.
func ApproveWorkflowStep(ctx context.Context, pool *pgxpool.Pool, idOrSlug string,
	actor *UserRecord, actorDisplay, actorType string, req ApproveWorkflowRequest) (*WorkflowApproval, *AihubError) {

	if req.Decision != string(wf.ApprovalGranted) && req.Decision != string(wf.ApprovalDenied) {
		return nil, NewErr(ErrBadRequest, "decision must be approved or rejected")
	}
	if actorType != "human" {
		return nil, NewErr(ErrForbidden,
			"only an authenticated human user may approve or reject a workflow step; a machine credential cannot")
	}
	if actor == nil || actor.ID == "" {
		return nil, NewErr(ErrUnauthorized, "not authenticated")
	}

	tx, err := pool.Begin(ctx)
	if err != nil {
		return nil, NewErr(ErrInternalError, "begin workflow approval tx")
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	// Lock the work item row: this SERIALIZES concurrent approvals of one work
	// item, so the read-existing/insert pair below cannot interleave. Combined
	// with the artifact-identity unique key (which EXCLUDES the decision), no
	// two transactions can record contradictory decisions for one artifact:
	// the second waits on the lock, re-reads the first's committed row, and
	// answers the idempotent or the conflicting path deterministically
	// (aihub#708 review blocker: "approval contradictory race").
	w, wp, aerr := getWorkflowWIOnTx(ctx, tx, idOrSlug, true)
	if aerr != nil {
		return nil, aerr
	}
	if wp.StepsVersion == 0 {
		return nil, NewErr(ErrNotFound, "this work item has no workflow")
	}
	if req.StepsVersion != wp.StepsVersion {
		return nil, NewErrDetails(ErrConflictStepAttemptMismatch,
			fmt.Sprintf("approval names workflow generation %d but the current generation is %d; approvals do not carry across revisions",
				req.StepsVersion, wp.StepsVersion),
			map[string]any{"approval_steps_version": req.StepsVersion, "current_steps_version": wp.StepsVersion})
	}

	var flow wf.Flow
	if err := json.Unmarshal(wp.Steps, &flow); err != nil {
		return nil, NewErr(ErrInternalError, "stored workflow flow does not parse")
	}
	var step *wf.Step
	for i := range flow.Steps {
		if flow.Steps[i].ID == req.StepID {
			step = &flow.Steps[i]
			break
		}
	}
	if step == nil {
		return nil, NewErr(ErrNotFound, fmt.Sprintf("step %q is not in the current workflow", req.StepID))
	}
	wiRHS := w.RequiresHumanSession != nil && *w.RequiresHumanSession
	if !wiRHS || step.RHS == nil || !*step.RHS {
		return nil, NewErr(ErrBadRequest, fmt.Sprintf(
			"step %q does not gate on a human approval (effective RHS is false); there is nothing to approve", req.StepID))
	}

	// The artifact must be the step's LATEST recorded result's, exactly.
	var latestArtifact []byte
	err = tx.QueryRow(ctx, `
		SELECT artifact FROM (
			SELECT artifact FROM wi_workflow_results
			WHERE work_item_id = $1 AND steps_version = $2 AND step_id = $3
			ORDER BY created_at DESC, id DESC LIMIT 1
		) latest`, w.ID, wp.StepsVersion, req.StepID).Scan(&latestArtifact)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, NewErr(ErrConflictStepAttemptMismatch, fmt.Sprintf(
			"step %q has no recorded result to approve; approval binds the produced artifact, not an expectation", req.StepID))
	}
	if err != nil {
		return nil, dbErrCause(err, "read latest workflow result")
	}
	var latest wf.ArtifactRef
	if err := json.Unmarshal(latestArtifact, &latest); err != nil {
		return nil, NewErr(ErrInternalError, "stored artifact does not parse")
	}
	if latest != req.Artifact {
		return nil, NewErrDetails(ErrConflictStepAttemptMismatch,
			fmt.Sprintf("approval artifact (%s v%d %s) does not match the step's latest recorded result (%s v%d %s)",
				req.Artifact.ID, req.Artifact.Version, req.Artifact.Hash, latest.ID, latest.Version, latest.Hash),
			map[string]any{"submitted_artifact": req.Artifact, "latest_artifact": latest})
	}

	// Same-triple decisions: identical is idempotent, different conflicts.
	var existingID, existingDecision string
	err = tx.QueryRow(ctx, `
		SELECT id, decision FROM wi_workflow_approvals
		WHERE work_item_id=$1 AND steps_version=$2 AND step_id=$3
		  AND artifact_id=$4 AND artifact_version=$5 AND artifact_hash=$6`,
		w.ID, wp.StepsVersion, req.StepID, req.Artifact.ID, req.Artifact.Version, req.Artifact.Hash,
	).Scan(&existingID, &existingDecision)
	if err == nil && existingDecision != req.Decision {
		return nil, NewErr(ErrConflictDuplicate,
			fmt.Sprintf("this exact artifact already carries a %q decision; a conflicting decision requires a new artifact", existingDecision))
	}
	if err == nil {
		// Idempotent re-decision: return the recorded row, unmodified.
		a, qErr := scanWorkflowApproval(tx.QueryRow(ctx,
			`SELECT`+approveScanColumns+` FROM wi_workflow_approvals WHERE id=$1`, existingID))
		if qErr != nil {
			return nil, dbErrCause(qErr, "read existing approval")
		}
		return a, nil
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return nil, dbErrCause(err, "read existing approval")
	}

	id := NewID("wapp")
	if _, err := tx.Exec(ctx, `
		INSERT INTO wi_workflow_approvals
		    (id, work_item_id, steps_version, step_id, artifact_id, artifact_version, artifact_hash, decision, actor_user_id, actor_display)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)`,
		id, w.ID, wp.StepsVersion, req.StepID, req.Artifact.ID, req.Artifact.Version, req.Artifact.Hash,
		req.Decision, actor.ID, actorDisplay); err != nil {
		if isUniqueViolation(err) {
			// The artifact-identity key fired: this exact artifact already
			// carries a decision. The FOR UPDATE lock above already orders
			// same-work-item approvals, so this arm is the fence for anything
			// the row lock cannot reach — and it makes the DB, not the
			// check-then-insert window, the authority on contradictions.
			return nil, NewErr(ErrConflictDuplicate,
				"this exact artifact already carries a decision; a conflicting decision requires a new artifact")
		}
		return nil, dbErrCause(err, "insert workflow approval")
	}

	if aerr := insertWorkflowEvent(ctx, tx, w.ID, w.Project, actor.ID, actorDisplay, "workflow_approval", map[string]any{
		"steps_version": wp.StepsVersion,
		"step_id":       req.StepID,
		"artifact":      req.Artifact,
		"decision":      req.Decision,
	}); aerr != nil {
		return nil, aerr
	}

	if err := tx.Commit(ctx); err != nil {
		if aerr := retryConflictErr(err, "commit workflow approval"); aerr != nil {
			return nil, aerr
		}
		return nil, NewErr(ErrInternalError, "commit workflow approval")
	}

	a, qErr := scanWorkflowApproval(pool.QueryRow(ctx,
		`SELECT`+approveScanColumns+` FROM wi_workflow_approvals WHERE id=$1`, id))
	if qErr != nil {
		return nil, dbErrCause(qErr, "read recorded approval")
	}
	return a, nil
}

// ─── Repair authorization ────────────────────────────────────────────────────

// AuthorizeWorkflowRepairRequest is the POST /workflow/repair body.
type AuthorizeWorkflowRepairRequest struct {
	AttemptID           string `json:"attempt_id"`
	ClaimEpoch          int64  `json:"claim_epoch"`
	SessionSecret       string `json:"session_secret"`
	FailedStepAttemptID string `json:"failed_step_attempt_id"`
	// Kind: "retry" (the bounded single retry of one failed invocation, spec
	// D9's infrastructure-failure fallback) or "episode" (the review-FAIL
	// recovery: repair producer + fresh verification + fresh independent
	// review, spec D8).
	Kind   string `json:"kind"`
	Reason string `json:"reason"`
}

// WorkflowRepairAuthorization is the opened authorization.
type WorkflowRepairAuthorization struct {
	ID                  string    `json:"id"`
	Kind                string    `json:"kind"`
	WorkItemID          string    `json:"work_item_id"`
	FailedStepsVersion  int       `json:"failed_steps_version"`
	FailedStepID        string    `json:"failed_step_id"`
	FailedStepAttemptID string    `json:"failed_step_attempt_id"`
	Status              string    `json:"status"`
	CreatedAt           time.Time `json:"created_at"`
	// ParentEpisodeID is the immutable parent lineage: non-empty on a retry
	// whose failed attempt belongs to an episode role invocation — the
	// episode whose effective role set this retry's replacements stand in
	// for. Empty on episodes and on retries of ordinary invocations. Derived
	// server-side from the failed attempt's own invocation row; never a
	// request field.
	ParentEpisodeID string `json:"parent_episode_id,omitempty"`
}

// maxOpenRepairAuthorizations bounds how many OPEN authorizations a work item
// may accumulate, mirroring the pure policy's repair-episode bound (Decide
// refuses more than 8 episodes over a history). A refused authorization here
// keeps the bound BEFORE the invocations exist, not after.
const maxOpenRepairAuthorizations = 8

// maxRetryChainDepth bounds how many retries may chain off ONE failed
// invocation lineage (aihub#708 Batch 2A re-review blocker, mem_FHuxXIXI): a
// retry's replacement that itself provider_errors needs a NEW authorization,
// and without a chain bound that loop is unbounded — each retry closes on its
// recorded result, so the open-authorization bound above never fires. The
// walk in resolveRetryLineage counts the retries already above the failed
// attempt; a new one is refused at this depth. Three keeps D9's
// infrastructure-failure fallback genuinely bounded (original plus up to
// three replacements) while tolerating a flaky provider, the same philosophy
// the pure policy's episode bound carries at its own scale.
const maxRetryChainDepth = 3

// AuthorizeWorkflowRepair opens an explicit recovery authorization for one
// failed result (spec D8: "Authorized recovery explicitly opens repair" —
// there is no automatic reroll, and this is the only route that can open one).
//
// Authorization is EXPLICIT and FENCED: the caller must hold the CURRENT
// attempt's credentials, so after a review-FAIL pause this is reachable only
// from the resumed (new) attempt — the paused one's credentials are dead by
// construction. The authorization binds the exact failed step attempt and a
// reason (10..2000 chars); later /workflow/start calls bind their invocations
// to it, and the pure policy validates the completed set.
func AuthorizeWorkflowRepair(ctx context.Context, pool *pgxpool.Pool, idOrSlug string,
	req AuthorizeWorkflowRepairRequest) (*WorkflowRepairAuthorization, *AihubError) {

	if req.Kind != "retry" && req.Kind != "episode" {
		return nil, NewErr(ErrBadRequest, "kind must be retry or episode")
	}
	trimmedReason := strings.TrimSpace(req.Reason)
	if len(trimmedReason) < 10 || len(trimmedReason) > 2000 {
		return nil, NewErr(ErrBadRequest, "reason must be between 10 and 2000 characters and say why recovery is authorized")
	}
	if req.FailedStepAttemptID == "" {
		return nil, NewErr(ErrBadRequest, "failed_step_attempt_id is required")
	}

	tx, err := pool.Begin(ctx)
	if err != nil {
		return nil, NewErr(ErrInternalError, "begin workflow repair tx")
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	w, wp, aerr := getWorkflowWIOnTx(ctx, tx, idOrSlug, true)
	if aerr != nil {
		return nil, aerr
	}
	// Lifecycle fence (aihub#708 B2): the row lock comes before the credential
	// check and is held through the authorization INSERT, matching the order
	// the claim/pause/takeover transitions serialize on. Without it, a takeover
	// that commits between the check and the INSERT leaves an authorization
	// opened by — and recoverable only by — an attempt that no longer exists;
	// with it, the losing request re-reads the moved row and leaves no
	// authorization behind.
	if aerr := verifyAttemptCredential(ctx, tx, *w, req.AttemptID, req.ClaimEpoch, req.SessionSecret); aerr != nil {
		return nil, aerr
	}
	if wp.StepsVersion == 0 {
		return nil, NewErr(ErrNotFound, "this work item has no workflow")
	}

	// The failed result must exist in the CURRENT generation and belong to
	// this attempt's lineage (the fencing above already guarantees the
	// attempt is current, so reading its own results is a formality the
	// identity check below still wants).
	var failedStepID, failedStatus, failedVerdict string
	var failedVersion int
	err = tx.QueryRow(ctx, `
		SELECT steps_version, step_id, status, COALESCE(review_verdict,'')
		FROM wi_workflow_results
		WHERE work_item_id = $1 AND step_attempt_id = $2`,
		w.ID, req.FailedStepAttemptID).Scan(&failedVersion, &failedStepID, &failedStatus, &failedVerdict)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, NewErr(ErrNotFound, "failed step attempt is not a recorded result of this work item")
	}
	if err != nil {
		return nil, dbErrCause(err, "read failed result")
	}
	if failedVersion != wp.StepsVersion {
		return nil, NewErr(ErrConflictStepAttemptMismatch, fmt.Sprintf(
			"failed result belongs to workflow generation %d, current is %d; authorize a current-generation failure",
			failedVersion, wp.StepsVersion))
	}

	stepMeta, aerr := workflowStepMetaFor(ctx, tx, w.ID, wp.StepsVersion, failedStepID)
	if aerr != nil {
		return nil, aerr
	}
	// The immutable parent lineage, resolved for the retry kind below: the
	// episode whose role invocation the failed attempt belongs to, when it
	// belongs to one at all.
	var parentEpisodeID string
	switch req.Kind {
	case "episode":
		// An episode recovers a failed REVIEW verdict (the pure policy binds
		// episodes to a FAIL-verdict review gate).
		if failedVerdict != string(wf.ReviewFail) {
			return nil, NewErr(ErrBadRequest, fmt.Sprintf(
				"an episode authorization must name a review FAIL; result %s carries verdict %q",
				req.FailedStepAttemptID, failedVerdict))
		}
		if !stepMetaIsReview(stepMeta) {
			return nil, NewErr(ErrBadRequest, "an episode authorization must name a review gate's failed result")
		}
	case "retry":
		// A retry is the D9 infrastructure-failure fallback and NOTHING else:
		// the failed invocation ended provider_error and carries no FAIL
		// verdict. A review FAIL recovers through an EPISODE (repair producer +
		// fresh verification + fresh independent review) — never a reroll of
		// the gate — and every other non-completed outcome is a defect the flow
		// must surface, not something a retry launders (aihub#708 review
		// blocker: "retry can rerun review FAIL").
		if failedStatus != string(wf.StatusProviderError) || failedVerdict == string(wf.ReviewFail) {
			return nil, NewErr(ErrBadRequest, fmt.Sprintf(
				"retry authorizes a provider_error only; result %s is %q (verdict %q); a review FAIL recovers through an episode authorization, never a retry",
				req.FailedStepAttemptID, failedStatus, failedVerdict))
		}
		// The parent lineage and the chain bound (mem_FHuxXIXI): when the
		// failed attempt's invocation is EPISODE-BOUND (an episode role that
		// recorded provider_error), this retry's replacements must stand in
		// for that role — recorded as parent_episode_id, read by the effective
		// role lookup — and no matter what the failed attempt descends from,
		// the retry CHAIN above it is bounded, never an unbounded loop of
		// fail-authorize-fail.
		lineage, aerr := resolveRetryLineage(ctx, tx, w.ID, req.FailedStepAttemptID)
		if aerr != nil {
			return nil, aerr
		}
		if lineage.depth >= maxRetryChainDepth {
			return nil, NewErr(ErrConflictDuplicate, fmt.Sprintf(
				"this failure's retry lineage already holds %d retries (bound %d); the provider channel is not recovering; escalate instead of authorizing another retry",
				lineage.depth, maxRetryChainDepth))
		}
		parentEpisodeID = lineage.parentEpisodeID
	}

	// Bound the outstanding authorizations before adding another.
	var openCount int
	if err := tx.QueryRow(ctx, `
		SELECT count(*) FROM wi_workflow_repair_episodes
		WHERE work_item_id = $1 AND status = 'open'`, w.ID).Scan(&openCount); err != nil {
		return nil, dbErrCause(err, "count open repair authorizations")
	}
	if openCount >= maxOpenRepairAuthorizations {
		return nil, NewErr(ErrConflictDuplicate, fmt.Sprintf(
			"%d repair authorizations are already open; close them before opening another", openCount))
	}

	id := NewID("wrep")
	actorUserID, _, aerr := attemptActor(ctx, tx, req.AttemptID)
	if aerr != nil {
		return nil, aerr
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO wi_workflow_repair_episodes
		    (id, work_item_id, kind, failed_steps_version, failed_step_id, failed_step_attempt_id,
		     reason, run_attempt_id, claim_epoch, authorized_by, status, parent_episode_id)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, 'open', NULLIF($11,''))`,
		id, w.ID, req.Kind, wp.StepsVersion, failedStepID, req.FailedStepAttemptID,
		trimmedReason, req.AttemptID, req.ClaimEpoch, actorUserID, parentEpisodeID); err != nil {
		if isUniqueViolation(err) {
			return nil, NewErr(ErrConflictDuplicate,
				"this failed step attempt already has an authorization of this kind")
		}
		return nil, dbErrCause(err, "insert repair authorization")
	}

	repairPayload := map[string]any{
		"repair_id":              id,
		"kind":                   req.Kind,
		"failed_step_id":         failedStepID,
		"failed_step_attempt_id": req.FailedStepAttemptID,
		"steps_version":          wp.StepsVersion,
		"reason":                 trimmedReason,
	}
	if parentEpisodeID != "" {
		// The lineage on the timeline: the episode whose role this retry's
		// replacements stand in for.
		repairPayload["parent_episode_id"] = parentEpisodeID
	}
	if aerr := insertWorkflowEvent(ctx, tx, w.ID, w.Project, actorUserID, "", "workflow_repair_authorized", repairPayload); aerr != nil {
		return nil, aerr
	}

	if err := tx.Commit(ctx); err != nil {
		if aerr := retryConflictErr(err, "commit workflow repair"); aerr != nil {
			return nil, aerr
		}
		return nil, NewErr(ErrInternalError, "commit workflow repair")
	}

	var out WorkflowRepairAuthorization
	if qErr := pool.QueryRow(ctx, `
		SELECT id, kind, work_item_id, failed_steps_version, failed_step_id, failed_step_attempt_id, status, created_at,
		       COALESCE(parent_episode_id,'')
		FROM wi_workflow_repair_episodes WHERE id = $1`, id,
	).Scan(&out.ID, &out.Kind, &out.WorkItemID, &out.FailedStepsVersion, &out.FailedStepID,
		&out.FailedStepAttemptID, &out.Status, &out.CreatedAt, &out.ParentEpisodeID); qErr != nil {
		return nil, dbErrCause(qErr, "read opened repair authorization")
	}
	return &out, nil
}

// retryLineage is what resolveRetryLineage found above a failed step attempt.
type retryLineage struct {
	// parentEpisodeID names the episode whose role invocation the chain
	// descends from, or "" when the failed attempt is an ordinary (unbound)
	// invocation or a replacement in an ordinary retry chain.
	parentEpisodeID string
	// depth is how many retries already sit in the chain above the failed
	// attempt: 0 for the first retry of an original invocation, +1 for every
	// retry of a failed replacement.
	depth int
}

// resolveRetryLineage walks the immutable parent lineage BACKWARDS from the
// failed step attempt a retry is being authorized for, entirely from rows the
// server itself wrote (mem_FHuxXIXI): the failed attempt's invocation row
// names the authorization it was bound to; an episode binding is the chain's
// root (the parent episode), a retry binding is one chain hop whose
// failed_step_attempt_id names the attempt below it. The walk terminates at
// an invocation with no binding (an ordinary root) or an episode binding —
// both inside maxRetryChainDepth+1 hops, which the loop guard enforces
// loudly rather than trusting the rows to be acyclic (each hop names a
// strictly older attempt, but a corrupt row must never loop a transaction).
// Nothing in the request influences the walk: lineage is derived, never
// claimed.
func resolveRetryLineage(ctx context.Context, tx pgx.Tx, wiID, failedStepAttemptID string) (retryLineage, *AihubError) {
	var out retryLineage
	cur := failedStepAttemptID
	for hops := 0; ; hops++ {
		if hops > maxRetryChainDepth+1 {
			return out, NewErr(ErrInternalError,
				"retry lineage walk exceeded its bound; the authorization chain is corrupt and must be inspected, not extended")
		}
		var bindID *string
		err := tx.QueryRow(ctx, `
			SELECT repair_episode_id FROM wi_workflow_invocations
			WHERE work_item_id = $1 AND step_attempt_id = $2`, wiID, cur).Scan(&bindID)
		if err != nil {
			// Every result carries a live invocation row (the FK is ON DELETE
			// RESTRICT), and the caller already verified the failed result
			// exists — a missing row here is a hard error, never an empty
			// lineage.
			return out, dbErrCause(err, "read retry lineage invocation")
		}
		if bindID == nil || *bindID == "" {
			return out, nil // ordinary root: no episode above this chain
		}
		var kind, prevFailed string
		var parent *string
		err = tx.QueryRow(ctx, `
			SELECT kind, failed_step_attempt_id, parent_episode_id
			FROM wi_workflow_repair_episodes WHERE id = $1 AND work_item_id = $2`,
			*bindID, wiID).Scan(&kind, &prevFailed, &parent)
		if errors.Is(err, pgx.ErrNoRows) {
			// ErrNoRows is not a class-40 candidate, but the classifier is
			// consulted anyway — the aihub#334 rule: never assume the answer
			// on a transactional path.
			if aerr := retryConflictErr(err, "read retry lineage authorization"); aerr != nil {
				return out, aerr
			}
			return out, NewErr(ErrInternalError, fmt.Sprintf(
				"invocation of %q is bound to authorization %q which does not exist; refusing to derive lineage from it",
				cur, *bindID))
		}
		if err != nil {
			return out, dbErrCause(err, "read retry lineage authorization")
		}
		switch kind {
		case "episode":
			// The chain's root: the failed lineage descends from this
			// episode's role invocation. (A stored parent_episode_id on a
			// retry below is a shortcut to this same id; the walk remains the
			// authority, so retries written before the column existed still
			// resolve.)
			out.parentEpisodeID = *bindID
			return out, nil
		case "retry":
			out.depth++
			if parent != nil && *parent != "" {
				out.parentEpisodeID = *parent
			}
			cur = prevFailed
		default:
			return out, NewErr(ErrInternalError, fmt.Sprintf(
				"authorization %s carries unknown kind %q; refusing to derive lineage from it", *bindID, kind))
		}
	}
}

func stepMetaIsReview(meta wiStepMeta) bool {
	for _, c := range meta.Capabilities {
		if c == skillregistry.CapReview {
			return true
		}
	}
	return false
}

// ─── Controller reconcile: fencing a dead attempt's open invocations ────────

// ReconcileWorkflowInvocationsRequest is the POST /workflow/reconcile body.
// The credentials are the CURRENT attempt's — the reconcile is a controller
// action under the same fencing every attempt-credential route uses — and the
// transition names the attempt whose leftovers it is fencing.
type ReconcileWorkflowInvocationsRequest struct {
	AttemptID     string `json:"attempt_id"`
	ClaimEpoch    int64  `json:"claim_epoch"`
	SessionSecret string `json:"session_secret"`
	// SupersedeAttemptID names the attempt whose OPEN invocations are being
	// abandoned: the one a pause or takeover ended before its invocations
	// recorded. Its dead-ness is READ from its own row under the work item
	// lock — the request's claim is never trusted (aihub#708 B3: no implicit
	// trust).
	SupersedeAttemptID string `json:"supersede_attempt_id"`
}

// WorkflowInvocationReconcile is the transition's outcome.
type WorkflowInvocationReconcile struct {
	WorkItemID          string `json:"work_item_id"`
	SupersededAttemptID string `json:"superseded_attempt_id"`
	// Superseded is how many open invocations the transition abandoned,
	// ordinary and episode-bound alike. 0 is an ordinary, idempotent answer
	// (nothing was left open, or a prior reconcile already fenced them).
	Superseded    int      `json:"superseded"`
	InvocationIDs []string `json:"invocation_ids"`
}

// ReconcileWorkflowInvocations is the EXPLICIT controller-reconcile transition
// (aihub#708 B3): it supersedes a dead attempt's OPEN workflow invocations so
// the live attempt can start replacements.
//
// Why it must exist at all: a pause or takeover ends an attempt's credential
// but used to leave its open invocations standing, and no production code wrote
// the 'superseded' status the schema promised. A step with an open invocation
// can never be started again (one open invocation per generation+step), so a
// dead attempt's leftover blocked the flow FOREVER — and an episode-bound
// leftover additionally occupied its role's one binding. The reconcile is the
// single writer of 'superseded', and it is fenced on three sides:
//
//   - the CURRENT attempt's credentials verify, so only the attempt that
//     inherits the work can fence the predecessor's leftovers — a dead
//     attempt cannot reconcile anything, not even its own funeral;
//   - the named attempt's OWN row is read under the work item lock and must
//     belong to this work item and NOT be running. Naming a running attempt —
//     above all the current one — is refused; nothing in the request is
//     trusted over the database;
//   - the work item row is locked first and held through the UPDATE, the same
//     lifecycle-fence order as start/result/repair (aihub#708 B2), so a
//     takeover racing the reconcile cannot leave one attempt's leftovers
//     half-fenced.
//
// After the transition: the superseded invocations refuse results with a
// distinct stale-refusal (loadOpenInvocation), stop counting toward an
// episode's role set (episodeRoleSetComplete) and its binding bounds
// (checkWorkflowStartOrdering), and the step is free for the live attempt to
// mint a replacement — which it still has to do explicitly, with its own
// credentials, under the same ordering and approval gates as any start.
func ReconcileWorkflowInvocations(ctx context.Context, pool *pgxpool.Pool, idOrSlug string,
	req ReconcileWorkflowInvocationsRequest) (*WorkflowInvocationReconcile, *AihubError) {

	if req.SupersedeAttemptID == "" {
		return nil, NewErr(ErrBadRequest, "supersede_attempt_id is required")
	}

	tx, err := pool.Begin(ctx)
	if err != nil {
		return nil, NewErr(ErrInternalError, "begin workflow reconcile tx")
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	// Lifecycle fence (aihub#708 B2): lock first, verify second, mutate under
	// the lock — the order every attempt-mutating route already uses.
	w, wp, aerr := getWorkflowWIOnTx(ctx, tx, idOrSlug, true)
	if aerr != nil {
		return nil, aerr
	}
	if aerr := verifyAttemptCredential(ctx, tx, *w, req.AttemptID, req.ClaimEpoch, req.SessionSecret); aerr != nil {
		return nil, aerr
	}
	if wp.StepsVersion == 0 {
		return nil, NewErr(ErrNotFound, "this work item has no workflow; nothing to reconcile")
	}

	// No implicit trust: the named attempt is read from its own row, under the
	// work item lock. The request says WHO to fence; the database says whether
	// that is legal.
	var namedWI, namedStatus string
	err = tx.QueryRow(ctx, `
		SELECT work_item_id, status FROM run_attempts WHERE id = $1`,
		req.SupersedeAttemptID).Scan(&namedWI, &namedStatus)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, NewErr(ErrNotFound, fmt.Sprintf(
			"supersede_attempt_id %q is not a run attempt", req.SupersedeAttemptID))
	}
	if err != nil {
		return nil, dbErrCause(err, "read the attempt being reconciled")
	}
	if namedWI != w.ID {
		return nil, NewErr(ErrConflictStepAttemptMismatch, fmt.Sprintf(
			"attempt %s belongs to work item %s, not %s; a reconcile fences only this work item's attempts",
			req.SupersedeAttemptID, namedWI, w.ID))
	}
	if w.CurrentAttemptID != nil && *w.CurrentAttemptID == req.SupersedeAttemptID {
		return nil, NewErr(ErrConflictStepInProgress, fmt.Sprintf(
			"attempt %s is the CURRENT attempt; the current attempt cannot reconcile itself away; name the attempt a lifecycle transition ended",
			req.SupersedeAttemptID))
	}
	if namedStatus == "running" {
		return nil, NewErr(ErrConflictStepInProgress, fmt.Sprintf(
			"attempt %s is still running; only a paused, superseded or ended attempt's invocations can be abandoned",
			req.SupersedeAttemptID))
	}

	// The transition: every OPEN invocation of the named attempt — ordinary
	// and episode-bound alike — becomes 'superseded', with the timestamp the
	// schema's recorded_at twin carries for the abandoned side.
	rows, err := tx.Query(ctx, `
		UPDATE wi_workflow_invocations
		SET status = 'superseded', superseded_at = clock_timestamp()
		WHERE work_item_id = $1 AND run_attempt_id = $2 AND status = 'open'
		RETURNING id, steps_version, step_id, step_attempt_id, repair_episode_id`,
		w.ID, req.SupersedeAttemptID)
	if err != nil {
		return nil, dbErrCause(err, "supersede the dead attempt's open invocations")
	}
	type supersededInvocation struct {
		id, stepID, stepAttemptID string
		stepsVersion              int
		repairEpisodeID           *string
	}
	superseded := []supersededInvocation{}
	for rows.Next() {
		var s supersededInvocation
		if err := rows.Scan(&s.id, &s.stepsVersion, &s.stepID, &s.stepAttemptID, &s.repairEpisodeID); err != nil {
			rows.Close()
			return nil, dbErrCause(err, "supersede the dead attempt's open invocations")
		}
		superseded = append(superseded, s)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, dbErrCause(err, "supersede the dead attempt's open invocations")
	}
	rows.Close()

	actorUserID, actorDisplay, aerr := attemptActor(ctx, tx, req.AttemptID)
	if aerr != nil {
		return nil, aerr
	}
	ids := make([]string, 0, len(superseded))
	for _, s := range superseded {
		ids = append(ids, s.id)
		payload := map[string]any{
			"steps_version":         s.stepsVersion,
			"step_id":               s.stepID,
			"step_attempt_id":       s.stepAttemptID,
			"invocation_id":         s.id,
			"superseded_attempt_id": req.SupersedeAttemptID,
			"run_attempt_id":        req.AttemptID,
		}
		if s.repairEpisodeID != nil && *s.repairEpisodeID != "" {
			payload["repair_episode_id"] = *s.repairEpisodeID
		}
		if aerr := insertWorkflowEvent(ctx, tx, w.ID, w.Project, actorUserID, actorDisplay,
			"workflow_invocation_superseded", payload); aerr != nil {
			return nil, aerr
		}
	}

	if err := tx.Commit(ctx); err != nil {
		if aerr := retryConflictErr(err, "commit workflow reconcile"); aerr != nil {
			return nil, aerr
		}
		return nil, NewErr(ErrInternalError, "commit workflow reconcile")
	}

	return &WorkflowInvocationReconcile{
		WorkItemID:          w.ID,
		SupersededAttemptID: req.SupersedeAttemptID,
		Superseded:          len(superseded),
		InvocationIDs:       ids,
	}, nil
}
