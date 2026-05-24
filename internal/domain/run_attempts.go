package domain

import (
	"context"
	cryptoRand "crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/GMISWE/ieops-aihub/internal/auth"
)


// RunAttempt mirrors the run_attempts table.
type RunAttempt struct {
	ID                  string     `json:"id"`
	WorkItemID          string     `json:"work_item_id"`
	Status              string     `json:"status"`
	ClaimEpoch          int64      `json:"claim_epoch"`
	IdempotencyKey      string     `json:"idempotency_key"`
	LastActiveAt        time.Time  `json:"last_active_at"`
	ActorUserID         string     `json:"actor_user_id"`
	APIKeyID            string     `json:"api_key_id"`
	ActorDisplay        string     `json:"actor_display"`
	MachineID           string     `json:"machine_id"`
	SessionSecretHash   string     `json:"session_secret_hash"`
	ParentAttemptID     *string    `json:"parent_attempt_id"`
	PhaseConfigVersion  *int       `json:"phase_config_version"`
	PreparedWorkspace   *json.RawMessage `json:"prepared_workspace"`
	StartedAt           time.Time  `json:"started_at"`
	EndedAt             *time.Time `json:"ended_at"`
}

// ResourceLock mirrors a resource_locks row.
type ResourceLock struct {
	ResourceType   string `json:"resource_type"`
	ResourceKey    string `json:"resource_key"`
	OwnerAttemptID string `json:"owner_attempt_id"`
	ClaimEpoch     int64  `json:"claim_epoch"`
}

// ClaimRequest is the parsed body for POST /v1/work_items/:id/claim.
type ClaimRequest struct {
	IdempotencyKey string        `json:"idempotency_key"`
	SessionInfo    SessionInfo   `json:"session_info"`
	RequestedLocks []ResourceLockReq `json:"requested_locks"`
	Mode           string        `json:"mode"` // "fresh" | "resume"
	ForceOver      bool          `json:"force_takeover"`
}

// SessionInfo carries machine_id and session_secret.
type SessionInfo struct {
	MachineID     string `json:"machine_id"`
	SessionSecret string `json:"session_secret"` // hex-encoded 64-byte random
}

// ResourceLockReq is one lock acquisition request.
type ResourceLockReq struct {
	ResourceType string `json:"resource_type"`
	ResourceKey  string `json:"resource_key"`
}

// ClaimResponse is returned by POST /v1/work_items/:id/claim.
type ClaimResponse struct {
	AttemptID             string         `json:"attempt_id"`
	ClaimEpoch            int64          `json:"claim_epoch"`
	AcquiredLocks         []ResourceLock `json:"acquired_locks"`
	CurrentAttemptEpoch   int64          `json:"current_attempt_epoch"`
	StepRecoveryHint      string         `json:"step_recovery_hint,omitempty"`
	RequiresHumanSession  *bool          `json:"requires_human_session"`
	WIType                *string        `json:"wi_type"`
	Slug                  string         `json:"slug,omitempty"`
	Project               string         `json:"project,omitempty"`
}

// FnClaimWorkItem implements the atomic claim transaction per §7 / §8.4 of the design doc.
// Implements C-R9-6, C-R9-10, C-R9-12 fixes.
func FnClaimWorkItem(ctx context.Context, pool *pgxpool.Pool, wiID string, req *ClaimRequest, callerUserID, callerAPIKeyID, callerDisplay string) (*ClaimResponse, *AihubError) {
	if req.IdempotencyKey == "" {
		return nil, NewErr(ErrBadRequest, "idempotency_key is required")
	}
	if req.SessionInfo.MachineID == "" {
		return nil, NewErr(ErrBadRequest, "session_info.machine_id is required")
	}
	if req.SessionInfo.SessionSecret == "" {
		return nil, NewErr(ErrBadRequest, "session_info.session_secret is required")
	}
	if req.Mode == "" {
		req.Mode = "fresh"
	}

	// Hash the session_secret for storage
	secretHash := HashSecret(req.SessionInfo.SessionSecret)

	tx, err := pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return nil, NewErr(ErrInternalError, "failed to begin transaction")
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	// Lock the work_item row FOR UPDATE to prevent concurrent claims
	var wi WorkItem
	err = tx.QueryRow(ctx, `
		SELECT id, seq, slug, project, scenario, goal, source, wi_type, priority,
		       requires_human_session, milestone, labels, status,
		       declared_resources, resources_version, external_share_type, external_share_key,
		       reporter_user_id, reporter_display, current_attempt_id, current_attempt_epoch,
		       parent_work_item_id, attrs, created_at, updated_at, closed_at
		FROM work_items WHERE id = $1 FOR UPDATE`, wiID,
	).Scan(
		&wi.ID, &wi.Seq, &wi.Slug, &wi.Project, &wi.Scenario, &wi.Goal, &wi.Source,
		&wi.WIType, &wi.Priority, &wi.RequiresHumanSession, &wi.Milestone, &wi.Labels,
		&wi.Status, &wi.DeclaredResources, &wi.ResourcesVersion,
		&wi.ExternalShareType, &wi.ExternalShareKey,
		&wi.ReporterUserID, &wi.ReporterDisplay,
		&wi.CurrentAttemptID, &wi.CurrentAttemptEpoch,
		&wi.ParentWorkItemID, &wi.Attrs, &wi.CreatedAt, &wi.UpdatedAt, &wi.ClosedAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, NewErr(ErrNotFound, fmt.Sprintf("work item %q not found", wiID))
		}
		return nil, NewErr(ErrInternalError, fmt.Sprintf("failed to lock work_item: %v", err))
	}

	// Check idempotency: if this key was already used for this wi, return cached response.
	// G3 fix (design §7): on idem hit, re-query the live resource_locks for that attempt
	// and recompute step_recovery_hint so callers (state-file writers) get the same shape
	// as a fresh claim, not a phantom empty AcquiredLocks slice.
	var existingAttemptID string
	var existingEpoch int64
	idemErr := tx.QueryRow(ctx,
		`SELECT id, claim_epoch FROM run_attempts WHERE work_item_id=$1 AND idempotency_key=$2`,
		wi.ID, req.IdempotencyKey,
	).Scan(&existingAttemptID, &existingEpoch)
	if idemErr == nil {
		// Re-query the locks held by the existing attempt.
		existingLocks := []ResourceLock{}
		lockRows, lockQErr := tx.Query(ctx,
			`SELECT resource_type, resource_key, owner_attempt_id, claim_epoch
			 FROM resource_locks WHERE owner_attempt_id=$1`, existingAttemptID)
		if lockQErr == nil {
			for lockRows.Next() {
				var l ResourceLock
				if scanErr := lockRows.Scan(&l.ResourceType, &l.ResourceKey, &l.OwnerAttemptID, &l.ClaimEpoch); scanErr == nil {
					existingLocks = append(existingLocks, l)
				}
			}
			lockRows.Close()
		}

		// Recompute step_recovery_hint identically to the fresh path.
		idemHint := "clean"
		var idemStepStatus string
		var idemStepStartedAt *time.Time
		stepErr := tx.QueryRow(ctx, `
			SELECT current_step_status, step_started_at FROM wi_step_state WHERE work_item_id=$1`, wi.ID,
		).Scan(&idemStepStatus, &idemStepStartedAt)
		if stepErr == nil && idemStepStatus == "in_progress" {
			if idemStepStartedAt != nil && time.Since(*idemStepStartedAt) < 15*time.Second {
				idemHint = "active_in_progress_conflict"
			} else {
				idemHint = "crashed_in_progress"
			}
		}

		if err := tx.Commit(ctx); err != nil {
			return nil, NewErr(ErrInternalError, "failed to commit idempotent claim")
		}
		return &ClaimResponse{
			AttemptID:            existingAttemptID,
			ClaimEpoch:           existingEpoch,
			AcquiredLocks:        existingLocks,
			CurrentAttemptEpoch:  existingEpoch,
			StepRecoveryHint:     idemHint,
			RequiresHumanSession: wi.RequiresHumanSession,
			WIType:               wi.WIType,
			Slug:                 wi.Slug,
			Project:              wi.Project,
		}, nil
	}

	// C-R9-6: wi_type must be set before claim
	if wi.WIType == nil || *wi.WIType == "" {
		return nil, NewErr(ErrWITypeMismatch, "wi_type is not set; update it with pf_update_work_item(wi_type=...) before claiming")
	}

	// Load scenario_phase_configs for this scenario
	var configRaw []byte
	err = tx.QueryRow(ctx,
		`SELECT content FROM scenario_phase_configs WHERE scenario = $1`, wi.Scenario,
	).Scan(&configRaw)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			// C-R9-3: no config found → 503
			return nil, NewErr(ErrServiceUnavailable, fmt.Sprintf("no phase config for scenario %q — server not fully initialized", wi.Scenario))
		}
		return nil, NewErr(ErrInternalError, "failed to load scenario config")
	}

	var config struct {
		Version  int
		WITypes  map[string]struct {
			RequiresHumanSession bool `json:"requires_human_session"`
		} `json:"wi_types"`
	}
	var configFull struct {
		WITypes map[string]struct {
			RequiresHumanSession bool `json:"requires_human_session"`
		} `json:"wi_types"`
	}
	if jsonErr := json.Unmarshal(configRaw, &configFull); jsonErr != nil {
		return nil, NewErr(ErrInternalError, "failed to parse scenario config")
	}
	config.WITypes = configFull.WITypes

	// Also load config version
	var configVersion int
	tx.QueryRow(ctx, `SELECT version FROM scenario_phase_configs WHERE scenario = $1`, wi.Scenario).Scan(&configVersion) //nolint:errcheck
	config.Version = configVersion

	// C-R9-10: validate wi_type still exists in config
	wiTypeDef, typeExists := config.WITypes[*wi.WIType]
	if !typeExists {
		available := make([]string, 0, len(config.WITypes))
		for k := range config.WITypes {
			available = append(available, k)
		}
		return nil, NewErrDetails(ErrWITypeMismatch,
			fmt.Sprintf("wi_type %q no longer exists in scenario config", *wi.WIType),
			map[string]any{"wi_type": *wi.WIType, "available_wi_types": available},
		)
	}

	isTakeover := false
	priorAttemptID := ""

	// C-R9-12: check if same user_id re-claim on a running wi (implicit force_takeover)
	if wi.Status == "running" && wi.CurrentAttemptID != nil {
		// Load current attempt actor
		var currentActorUserID string
		var currentEpoch int64
		var currentActorDisplay string
		var currentLastActive time.Time
		err = tx.QueryRow(ctx,
			`SELECT actor_user_id, claim_epoch, actor_display, last_active_at FROM run_attempts WHERE id=$1`,
			*wi.CurrentAttemptID,
		).Scan(&currentActorUserID, &currentEpoch, &currentActorDisplay, &currentLastActive)
		if err == nil {
			if currentActorUserID == callerUserID {
				// Same user → implicit force_takeover
				isTakeover = true
				priorAttemptID = *wi.CurrentAttemptID
			} else if req.ForceOver {
				// Explicit force_takeover request — caller must be maintainer/admin (handled upstream)
				isTakeover = true
				priorAttemptID = *wi.CurrentAttemptID
			} else {
				// Different user, no force_takeover → 409
				return nil, NewErrDetails(ErrConflictWIAlreadyClaimed,
					fmt.Sprintf("work item is already claimed by %s", currentActorDisplay),
					map[string]any{
						"current_attempt": map[string]any{
							"id":            *wi.CurrentAttemptID,
							"actor_display": currentActorDisplay,
							"claim_epoch":   currentEpoch,
							"last_active_at": currentLastActive.Format(time.RFC3339),
						},
					},
				)
			}
		}
	} else if wi.Status == "blocked" {
		return nil, NewErr(ErrConflictTerminalState, "work item is blocked by dependencies; resolve blockers first")
	} else if wi.Status == "paused" || wi.Status == "queued" {
		// Normal claim — no extra checks required.
	} else if wi.Status == "wrapped" || wi.Status == "failed" || wi.Status == "cancelled" {
		return nil, NewErr(ErrConflictTerminalState, fmt.Sprintf("work item is in terminal state: %s", wi.Status))
	}

	// §4.3 + §15: locks are derived from wi.declared_resources at claim time.
	// If the client did not pass RequestedLocks explicitly, derive them from the
	// work_item's declared_resources via resourceToLock mapping (§25 C-R3-8).
	if len(req.RequestedLocks) == 0 && len(wi.DeclaredResources) > 0 {
		var declared []struct {
			Type       string `json:"type"`
			URI        string `json:"uri"`
			Intent     string `json:"intent,omitempty"`
			TaskBranch string `json:"task_branch,omitempty"`
		}
		if jsonErr := json.Unmarshal(wi.DeclaredResources, &declared); jsonErr == nil {
			for _, d := range declared {
				lockType, lockKey := resourceToLock(DeclaredResourceItem{
					Type: d.Type, URI: d.URI, Intent: d.Intent, TaskBranch: d.TaskBranch,
				})
				if lockType == "" {
					continue
				}
				req.RequestedLocks = append(req.RequestedLocks, ResourceLockReq{
					ResourceType: lockType, ResourceKey: lockKey,
				})
			}
		}
	}

	// Check lock conflicts (advisory — actual conflict resolution in claim)
	if len(req.RequestedLocks) > 0 && !isTakeover {
		resourceKeys := make([]string, len(req.RequestedLocks))
		resourceTypes := make([]string, len(req.RequestedLocks))
		for i, l := range req.RequestedLocks {
			resourceKeys[i] = l.ResourceKey
			resourceTypes[i] = l.ResourceType
		}
		var conflictAttemptID, conflictActorDisplay, conflictWISlug string
		for i, l := range req.RequestedLocks {
			err = tx.QueryRow(ctx, `
				SELECT rl.owner_attempt_id, ra.actor_display, wi2.slug
				FROM resource_locks rl
				JOIN run_attempts ra ON ra.id = rl.owner_attempt_id
				JOIN work_items wi2 ON wi2.id = ra.work_item_id
				WHERE rl.resource_type = $1 AND rl.resource_key = $2
				  AND ra.status IN ('running', 'paused')`,
				resourceTypes[i], resourceKeys[i],
			).Scan(&conflictAttemptID, &conflictActorDisplay, &conflictWISlug)
			if err == nil {
				return nil, NewErrDetails(ErrConflictLockTaken,
					fmt.Sprintf("resource %s:%s is already locked", l.ResourceType, l.ResourceKey),
					map[string]any{
						"conflict_with": map[string]any{
							"attempt_id":     conflictAttemptID,
							"actor_display":  conflictActorDisplay,
							"work_item_slug": conflictWISlug,
						},
					},
				)
			}
		}
	}

	// If takeover: supersede old attempt, delete its locks
	if isTakeover && priorAttemptID != "" {
		_, err = tx.Exec(ctx, `
			UPDATE run_attempts SET status='superseded', ended_at=clock_timestamp()
			WHERE id=$1`, priorAttemptID)
		if err != nil {
			return nil, NewErr(ErrInternalError, "failed to supersede prior attempt")
		}
		_, err = tx.Exec(ctx, `DELETE FROM resource_locks WHERE owner_attempt_id=$1`, priorAttemptID)
		if err != nil {
			return nil, NewErr(ErrInternalError, "failed to delete prior attempt locks")
		}

		// N5: supersede event emitted after new attempt INSERT (see below)
	}

	// Calculate new claim_epoch = current_attempt_epoch + 1
	newEpoch := wi.CurrentAttemptEpoch + 1

	// Insert new run_attempt
	newAttemptID := NewID("ra")
	_, err = tx.Exec(ctx, `
		INSERT INTO run_attempts (
			id, work_item_id, status, claim_epoch, idempotency_key,
			actor_user_id, api_key_id, actor_display, machine_id, session_secret_hash,
			parent_attempt_id, phase_config_version, started_at, last_active_at
		) VALUES (
			$1, $2, 'running', $3, $4,
			$5, $6, $7, $8, $9,
			$10, $11, clock_timestamp(), clock_timestamp()
		)`,
		newAttemptID, wi.ID, newEpoch, req.IdempotencyKey,
		callerUserID, callerAPIKeyID, callerDisplay, req.SessionInfo.MachineID, secretHash,
		nilIfEmpty(priorAttemptID), config.Version,
	)
	if err != nil {
		return nil, NewErr(ErrInternalError, fmt.Sprintf("failed to insert run_attempt: %v", err))
	}

	// N5: emit attempt_superseded event now that we have the real newAttemptID
	if isTakeover && priorAttemptID != "" {
		supEvtID := NewID("evt")
		supPayload, _ := json.Marshal(map[string]any{
			"superseded_by_attempt_id": newAttemptID,
			"reason":                  "claim by same user or explicit takeover",
			"actor_user_id":           callerUserID,
		})
		tx.Exec(ctx, `
			INSERT INTO agent_events (id, work_item_id, actor_user_id, actor_display, event_type, payload, project)
			VALUES ($1, $2, $3, $4, 'attempt_superseded', $5, $6)`,
			supEvtID, wi.ID, callerUserID, callerDisplay, supPayload, wi.Project,
		) //nolint:errcheck
	}

	// Insert resource_locks for requested locks
	acquiredLocks := make([]ResourceLock, 0, len(req.RequestedLocks))
	for _, l := range req.RequestedLocks {
		_, err = tx.Exec(ctx, `
			INSERT INTO resource_locks (resource_type, resource_key, owner_attempt_id, claim_epoch)
			VALUES ($1, $2, $3, $4)
			ON CONFLICT (resource_type, resource_key) DO UPDATE
			  SET owner_attempt_id=$3, claim_epoch=$4, acquired_at=clock_timestamp()`,
			l.ResourceType, l.ResourceKey, newAttemptID, newEpoch,
		)
		if err != nil {
			return nil, NewErr(ErrInternalError, fmt.Sprintf("failed to acquire lock %s:%s: %v", l.ResourceType, l.ResourceKey, err))
		}
		acquiredLocks = append(acquiredLocks, ResourceLock{
			ResourceType:   l.ResourceType,
			ResourceKey:    l.ResourceKey,
			OwnerAttemptID: newAttemptID,
			ClaimEpoch:     newEpoch,
		})
	}

	// Update work_items: status=running, current_attempt_id, current_attempt_epoch
	_, err = tx.Exec(ctx, `
		UPDATE work_items
		SET status='running', current_attempt_id=$1, current_attempt_epoch=$2
		WHERE id=$3`,
		newAttemptID, newEpoch, wi.ID,
	)
	if err != nil {
		return nil, NewErr(ErrInternalError, "failed to update work_item status")
	}

	// Bug fix: read step state BEFORE the upsert resets it to idle.
	// The hint must reflect what the prior attempt left behind, not the post-reset state.
	var priorStepStatus string
	var priorStepStartedAt *time.Time
	priorStepErr := tx.QueryRow(ctx,
		`SELECT current_step_status, step_started_at FROM wi_step_state WHERE work_item_id=$1`, wi.ID,
	).Scan(&priorStepStatus, &priorStepStartedAt)

	// Upsert wi_step_state (C-R7-9: INSERT ... ON CONFLICT DO UPDATE)
	_, err = tx.Exec(ctx, `
		INSERT INTO wi_step_state (work_item_id, wi_type, graph_source, current_step, current_step_status)
		VALUES ($1, $2, 'scenario_config', NULL, 'idle')
		ON CONFLICT (work_item_id) DO UPDATE
		  SET wi_type=$2, graph_source='scenario_config',
		      current_step_status='idle', current_step_attempt=NULL,
		      step_started_at=NULL, updated_at=clock_timestamp()`,
		wi.ID, wi.WIType,
	)
	if err != nil {
		// step_state upsert failure is non-fatal for free-execution mode
		_ = err
	}

	// C-R9-12: If wi.requires_human_session IS NULL, write back resolved value from config
	resolvedRHS := wiTypeDef.RequiresHumanSession
	if wi.RequiresHumanSession == nil {
		_, err = tx.Exec(ctx, `
			UPDATE work_items SET requires_human_session=$1 WHERE id=$2`,
			resolvedRHS, wi.ID,
		)
		if err != nil {
			return nil, NewErr(ErrInternalError, "failed to set requires_human_session")
		}
		// Emit wi_classification_resolved event
		evtID := NewID("evt")
		evtPayload, _ := json.Marshal(map[string]any{
			"wi_type":                *wi.WIType,
			"requires_human_session": resolvedRHS,
		})
		tx.Exec(ctx, `
			INSERT INTO agent_events (id, work_item_id, actor_user_id, actor_display, event_type, payload, project)
			VALUES ($1, $2, $3, $4, 'wi_classification_resolved', $5, $6)`,
			evtID, wi.ID, callerUserID, callerDisplay, evtPayload, wi.Project,
		) //nolint:errcheck
		wi.RequiresHumanSession = &resolvedRHS
	} else if *wi.RequiresHumanSession != resolvedRHS {
		// C-R9-12: mismatch → 409 REQUIRES_HUMAN_SESSION_MISMATCH
		tx.Rollback(ctx) //nolint:errcheck
		return nil, NewErrDetails(ErrRequiresHumanSessionMismatch,
			fmt.Sprintf("wi.requires_human_session=%v but phase config says %v for wi_type %q",
				*wi.RequiresHumanSession, resolvedRHS, *wi.WIType),
			map[string]any{
				"db_value":        *wi.RequiresHumanSession,
				"phase_yaml_value": resolvedRHS,
				"wi_type":         *wi.WIType,
			},
		)
	}

	// Emit attempt_started event
	evtID := NewID("evt")
	evtPayload, _ := json.Marshal(map[string]any{
		"machine_id":    req.SessionInfo.MachineID,
		"actor_display": callerDisplay,
		"is_takeover":   isTakeover,
		"is_resume":     req.Mode == "resume",
		"claim_epoch":   newEpoch,
	})
	tx.Exec(ctx, `
		INSERT INTO agent_events (id, work_item_id, run_attempt_id, actor_user_id, actor_display, api_key_id, event_type, payload, project)
		VALUES ($1, $2, $3, $4, $5, $6, 'attempt_started', $7, $8)`,
		evtID, wi.ID, newAttemptID, callerUserID, callerDisplay, callerAPIKeyID, evtPayload, wi.Project,
	) //nolint:errcheck

	// Determine step_recovery_hint from the state we read BEFORE the reset upsert.
	// (Reading post-upsert would always return idle — that was the original bug.)
	stepRecoveryHint := "clean"
	if priorStepErr == nil && priorStepStatus == "in_progress" {
		if isTakeover {
			// For takeover: step was freshly started by the prior attempt (< 15s) → conflict
			// vs. genuinely crashed (≥ 15s) → recommend re-running
			if priorStepStartedAt != nil && time.Since(*priorStepStartedAt) < 15*time.Second {
				stepRecoveryHint = "active_in_progress_conflict"
			} else {
				stepRecoveryHint = "crashed_in_progress"
			}
		} else {
			stepRecoveryHint = "crashed_in_progress"
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, NewErr(ErrInternalError, "failed to commit claim transaction")
	}

	return &ClaimResponse{
		AttemptID:            newAttemptID,
		ClaimEpoch:           newEpoch,
		AcquiredLocks:        acquiredLocks,
		CurrentAttemptEpoch:  newEpoch,
		StepRecoveryHint:     stepRecoveryHint,
		RequiresHumanSession: wi.RequiresHumanSession,
		WIType:               wi.WIType,
		Slug:                 wi.Slug,
		Project:              wi.Project,
	}, nil
}

// nilIfEmpty returns nil if s is empty, else a pointer to s.
func nilIfEmpty(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// HashSecret returns sha256 hex of a session secret (exported for use by server layer).
func HashSecret(secret string) string {
	return hashSecretInternal(secret)
}

// CompleteAttemptRequest is the parsed body for POST /v1/work_items/:id/complete.
type CompleteAttemptRequest struct {
	AttemptID          string `json:"attempt_id"`
	ClaimEpoch         int64  `json:"claim_epoch"`
	SessionSecret      string `json:"session_secret"`
	Status             string `json:"status"` // "wrapped" | "failed" | "paused"
	ForceTerminateStep bool   `json:"force_terminate_step"`
}

// FnCompleteAttempt implements the complete_attempt transaction.
// Implements H-R9-11: if wi.status='paused', auto-force_terminate the step first.
func FnCompleteAttempt(ctx context.Context, pool *pgxpool.Pool, wiID string, req *CompleteAttemptRequest) *AihubError {
	if req.Status != "wrapped" && req.Status != "failed" && req.Status != "paused" {
		return NewErr(ErrBadRequest, "status must be wrapped, failed, or paused")
	}

	tx, err := pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return NewErr(ErrInternalError, "failed to begin transaction")
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	// Load and lock the work item
	var wi WorkItem
	err = tx.QueryRow(ctx, `
		SELECT id, project, status, current_attempt_id, current_attempt_epoch
		FROM work_items WHERE id=$1 FOR UPDATE`, wiID,
	).Scan(&wi.ID, &wi.Project, &wi.Status, &wi.CurrentAttemptID, &wi.CurrentAttemptEpoch)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return NewErr(ErrNotFound, "work item not found")
		}
		return NewErr(ErrInternalError, "failed to lock work_item")
	}

	// H4: Reject double-wrap on terminal states
	if wi.Status == "wrapped" || wi.Status == "failed" || wi.Status == "cancelled" {
		return NewErr(ErrConflictTerminalState, fmt.Sprintf("work item is already %s", wi.Status))
	}

	// Verify attempt credential
	if aihubErr := verifyAttemptCredential(ctx, tx, wi, req.AttemptID, req.ClaimEpoch, req.SessionSecret); aihubErr != nil {
		return aihubErr
	}

	// H-R9-11: if there is a step in_progress and status=paused, force_terminate it first
	var stepStatus string
	var stepAttempt *string
	stepErr := tx.QueryRow(ctx, `
		SELECT current_step_status, current_step_attempt FROM wi_step_state WHERE work_item_id=$1`, wiID,
	).Scan(&stepStatus, &stepAttempt)
	if stepErr == nil && stepStatus == "in_progress" {
		if req.Status == "paused" || req.ForceTerminateStep {
			if aihubErr := fnForceTerminateStep(ctx, tx, wiID, req.AttemptID, stepAttempt); aihubErr != nil {
				return aihubErr
			}
		} else {
			return NewErr(ErrConflictStepInProgress, "a step is still in_progress; set force_terminate_step=true or update step first")
		}
	}

	// Set run_attempt status
	_, err = tx.Exec(ctx, `
		UPDATE run_attempts SET status=$1, ended_at=clock_timestamp() WHERE id=$2`,
		req.Status, req.AttemptID,
	)
	if err != nil {
		return NewErr(ErrInternalError, "failed to update run_attempt status")
	}

	// N4: on paused, keep resource_locks so resume can reclaim them (C5-3 design invariant)
	// on wrapped/failed: release locks
	if req.Status != "paused" {
		_, err = tx.Exec(ctx, `DELETE FROM resource_locks WHERE owner_attempt_id=$1`, req.AttemptID)
		if err != nil {
			return NewErr(ErrInternalError, "failed to release resource locks")
		}
	}

	// Update work_item status
	wiStatus := req.Status
	switch wiStatus {
	case "wrapped", "failed":
		// terminal — work_item moves to same status
	case "paused":
		// paused — keep wiStatus as-is
	}

	_, err = tx.Exec(ctx, `UPDATE work_items SET status=$1 WHERE id=$2`, wiStatus, wi.ID)
	if err != nil {
		return NewErr(ErrInternalError, "failed to update work_item status")
	}

	// Emit attempt_completed event
	evtID := NewID("evt")
	evtPayload, _ := json.Marshal(map[string]any{
		"status": req.Status,
	})
	tx.Exec(ctx, `
		INSERT INTO agent_events (id, work_item_id, run_attempt_id, event_type, payload, project)
		VALUES ($1, $2, $3, 'attempt_completed', $4, $5)`,
		evtID, wi.ID, req.AttemptID, evtPayload, wi.Project,
	) //nolint:errcheck

	// If terminal (wrapped/failed): unblock dependent wi + set methodology expires_at
	if req.Status == "wrapped" || req.Status == "failed" {
		if aihubErr := unblockDependentWI(ctx, tx, wi.ID, wi.Project); aihubErr != nil {
			_ = aihubErr // non-fatal
		}
		// C4: set methodology.* memory expires_at = closed_at + 90d
		tx.Exec(ctx, `
			UPDATE memories SET expires_at = clock_timestamp() + interval '90 days'
			WHERE work_item_id = $1 AND type LIKE 'methodology.%' AND expires_at IS NULL`,
			wi.ID) //nolint:errcheck
	}

	if err := tx.Commit(ctx); err != nil {
		return NewErr(ErrInternalError, "failed to commit complete_attempt")
	}
	return nil
}

// fnForceTerminateStep inserts a wi_step_completions row with status=failed,
// error_type=force_terminate, emits a step_failed agent_event, and resets wi_step_state.
// Per §4.3 force_terminate_step flow.
func fnForceTerminateStep(ctx context.Context, tx pgx.Tx, wiID, attemptID string, stepAttemptID *string) *AihubError {
	// Get current step
	var currentStep *string
	tx.QueryRow(ctx, `SELECT current_step FROM wi_step_state WHERE work_item_id=$1`, wiID).Scan(&currentStep) //nolint:errcheck

	if currentStep == nil {
		return nil // No step to terminate
	}

	saID := "unknown"
	if stepAttemptID != nil {
		saID = *stepAttemptID
	}

	scID := NewID("sc")
	_, err := tx.Exec(ctx, `
		INSERT INTO wi_step_completions (id, work_item_id, step_id, step_attempt_id, run_attempt_id,
		                                  status, error_type, escalated, completed_at)
		VALUES ($1, $2, $3, $4, $5, 'failed', 'force_terminate', false, clock_timestamp())
		ON CONFLICT (step_attempt_id) DO NOTHING`,
		scID, wiID, *currentStep, saID, attemptID,
	)
	if err != nil {
		return NewErr(ErrInternalError, fmt.Sprintf("failed to insert step_completion for force_terminate: %v", err))
	}

	// Emit step_failed event (§4.3 force_terminate_step flow)
	evtID := NewID("evt")
	payload, _ := json.Marshal(map[string]any{
		"step_id":         *currentStep,
		"step_attempt_id": saID,
		"error_type":      "force_terminate_step",
		"escalated":       false,
	})
	tx.Exec(ctx, `
		INSERT INTO agent_events (id, work_item_id, run_attempt_id, event_type, payload, project)
		VALUES ($1, $2, $3, 'step_failed', $4,
		        (SELECT project FROM work_items WHERE id=$2))`,
		evtID, wiID, attemptID, payload) //nolint:errcheck

	// Reset wi_step_state
	_, err = tx.Exec(ctx, `
		UPDATE wi_step_state
		SET current_step_status='idle', current_step_attempt=NULL, step_started_at=NULL,
		    version=version+1, updated_at=clock_timestamp()
		WHERE work_item_id=$1`, wiID,
	)
	if err != nil {
		return NewErr(ErrInternalError, "failed to reset wi_step_state")
	}
	return nil
}

// unblockDependentWI handles the unblock sweep after a wi completes.
// Implements C-R7-2: FOR UPDATE ORDER BY id to prevent deadlocks.
func unblockDependentWI(ctx context.Context, tx pgx.Tx, wiID, project string) *AihubError {
	// Get candidate blocked wi IDs (that were blocked by wiID), locked FOR UPDATE ORDER BY id
	rows, err := tx.Query(ctx, `
		SELECT id FROM work_items
		WHERE id IN (
		  SELECT dep.blocked_wi_id FROM wi_dependencies dep
		  WHERE dep.blocking_wi_id = $1 AND dep.kind = 'blocks'
		) AND status = 'blocked'
		ORDER BY id
		FOR UPDATE`, wiID,
	)
	if err != nil {
		return nil
	}
	var candidateIDs []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err == nil {
			candidateIDs = append(candidateIDs, id)
		}
	}
	rows.Close()

	for _, blockedID := range candidateIDs {
		// Check if all other blockers are terminal
		var stillBlocked int
		tx.QueryRow(ctx, `
			SELECT COUNT(*) FROM wi_dependencies dep
			JOIN work_items blocker ON dep.blocking_wi_id = blocker.id
			WHERE dep.blocked_wi_id = $1
			  AND dep.kind = 'blocks'
			  AND dep.blocking_wi_id != $2
			  AND blocker.status NOT IN ('wrapped','cancelled','failed')`,
			blockedID, wiID,
		).Scan(&stillBlocked) //nolint:errcheck

		if stillBlocked == 0 {
			// All blockers done — move to queued
			tx.Exec(ctx, `UPDATE work_items SET status='queued' WHERE id=$1`, blockedID) //nolint:errcheck

			// Emit wi_unblocked event
			evtID := NewID("evt")
			evtPayload, _ := json.Marshal(map[string]any{"unblocked_by_wi": wiID})
			tx.Exec(ctx, `
				INSERT INTO agent_events (id, work_item_id, event_type, payload, project)
				VALUES ($1, $2, 'wi_unblocked', $3, $4)`,
				evtID, blockedID, evtPayload, project,
			) //nolint:errcheck
		}
	}
	return nil
}

// ForceTakeoverRequest is the parsed body for POST /v1/work_items/:id/force_takeover.
// Carol-2 WALL-6: force_takeover includes implicit claim semantics; client supplies
// session_info.session_secret so MCP server can persist it locally (Decision A:
// secret never returns over HTTP).
type ForceTakeoverRequest struct {
	Reason      string      `json:"reason"`
	SessionInfo SessionInfo `json:"session_info"`
}

// ForceTakeoverResponse is returned by POST /v1/work_items/:id/force_takeover.
type ForceTakeoverResponse struct {
	PriorAttemptID    string `json:"prior_attempt_id"`
	PriorActorDisplay string `json:"prior_actor_display"`
	// H3: new attempt credentials — written to state file by MCP layer (never returned to LLM)
	NewAttemptID    string `json:"new_attempt_id"`
	NewClaimEpoch   int64  `json:"new_claim_epoch"`
	// NewSessionSecret is intentionally NOT in JSON (Decision A): the client supplied it
	// in the request body and already knows the plaintext.
	NewSessionSecret string `json:"-"`
	OK              bool   `json:"ok"`
}

// FnForceTakeover implements the force_takeover operation (H-R7-4).
// Permission check: same user → writer; other user → maintainer/admin.
func FnForceTakeover(ctx context.Context, pool *pgxpool.Pool, wiID, callerUserID, callerDisplay, callerRole string, callerProjectRoles map[string]string, req *ForceTakeoverRequest) (*ForceTakeoverResponse, *AihubError) {
	if req.Reason == "" {
		return nil, NewErr(ErrBadRequest, "reason is required for force_takeover")
	}

	wi, aihubErr := GetWorkItem(ctx, pool, wiID)
	if aihubErr != nil {
		return nil, aihubErr
	}

	if wi.Status != "running" {
		return nil, NewErr(ErrBadRequest, fmt.Sprintf("work item is not running (status=%s); cannot force_takeover", wi.Status))
	}
	if wi.CurrentAttemptID == nil {
		return nil, NewErr(ErrInternalError, "work item is running but has no current_attempt_id")
	}

	// Load current attempt
	var currentActorUserID, currentActorDisplay string
	err := pool.QueryRow(ctx, `
		SELECT actor_user_id, actor_display FROM run_attempts WHERE id=$1`,
		*wi.CurrentAttemptID,
	).Scan(&currentActorUserID, &currentActorDisplay)
	if err != nil {
		return nil, NewErr(ErrInternalError, "failed to load current attempt")
	}

	// Permission check per §9.4 and v1.21 ownership-only model.
	// Only the same user (self-takeover) or a maintainer/admin may force_takeover.
	// There is NO time-based auto-takeover: idle time does not grant takeover rights.
	isSelf := currentActorUserID == callerUserID
	projectRole := callerProjectRoles[wi.Project]
	isMaintainerOrAdmin := projectRole == "maintainer" || callerRole == "admin"

	if !isSelf && !isMaintainerOrAdmin {
		return nil, NewErr(ErrForbidden, "insufficient permissions: only the owner or a maintainer/admin can force_takeover")
	}

	tx, err2 := pool.Begin(ctx)
	if err2 != nil {
		return nil, NewErr(ErrInternalError, "failed to begin transaction")
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	priorID := *wi.CurrentAttemptID

	// Update step_state if in_progress (H-R7-4)
	var stepStatus string
	var stepAttempt *string
	stepErr := tx.QueryRow(ctx, `
		SELECT current_step_status, current_step_attempt FROM wi_step_state WHERE work_item_id=$1`, wi.ID,
	).Scan(&stepStatus, &stepAttempt)
	if stepErr == nil && stepStatus == "in_progress" {
		fnForceTerminateStep(ctx, tx, wi.ID, priorID, stepAttempt) //nolint:errcheck
	}

	// Supersede old attempt
	_, err2 = tx.Exec(ctx, `
		UPDATE run_attempts SET status='superseded', ended_at=clock_timestamp() WHERE id=$1`, priorID)
	if err2 != nil {
		return nil, NewErr(ErrInternalError, "failed to supersede prior attempt")
	}
	// Delete locks
	tx.Exec(ctx, `DELETE FROM resource_locks WHERE owner_attempt_id=$1`, priorID) //nolint:errcheck

	// Emit force_takeover event
	evtID := NewID("evt")
	evtPayload, _ := json.Marshal(map[string]any{
		"prior_attempt_id": priorID,
		"prior_actor":      currentActorDisplay,
		"reason":           req.Reason,
	})
	tx.Exec(ctx, `
		INSERT INTO agent_events (id, work_item_id, actor_user_id, actor_display, event_type, payload, project)
		VALUES ($1, $2, $3, $4, 'force_takeover', $5, $6)`,
		evtID, wi.ID, callerUserID, "", evtPayload, wi.Project,
	) //nolint:errcheck

	// H3 + Decision A: use the session_secret supplied by the client.
	// The client generated it before calling and wrote it to its local state file;
	// returning a server-generated secret over JSON is impossible without breaking
	// Decision A. Fall back to a server-generated secret only when the client omitted one
	// (legacy callers / CLI which can't persist secrets).
	newEpoch := wi.CurrentAttemptEpoch + 1
	newAttemptID := NewID("ra")
	newSecret := req.SessionInfo.SessionSecret
	if newSecret == "" {
		var genErr error
		newSecret, genErr = generateSessionSecret()
		if genErr != nil {
			return nil, NewErr(ErrInternalError, "failed to generate session_secret")
		}
	}
	newSecretHash := auth.HashSecret(newSecret)
	machineID := req.SessionInfo.MachineID
	if machineID == "" {
		machineID = "force-takeover"
	}
	_, err2 = tx.Exec(ctx, `
		INSERT INTO run_attempts (
			id, work_item_id, status, claim_epoch, idempotency_key,
			actor_user_id, api_key_id, actor_display, machine_id, session_secret_hash,
			parent_attempt_id, started_at, last_active_at
		) VALUES (
			$1, $2, 'running', $3, $4,
			$5, '', $6, $7, $8,
			$9, clock_timestamp(), clock_timestamp()
		)`,
		newAttemptID, wi.ID, newEpoch, "force-takeover-"+newAttemptID,
		callerUserID, callerDisplay, machineID, newSecretHash,
		priorID, // parent_attempt_id = superseded attempt
	)
	if err2 != nil {
		return nil, NewErr(ErrInternalError, "failed to create new attempt after force_takeover")
	}

	// Re-INSERT resource_locks for new attempt based on wi.DeclaredResources
	// (prior locks were deleted above; new attempt must hold them for conflict detection)
	var declaredRes []struct {
		Type       string `json:"type"`
		URI        string `json:"uri"`
		TaskBranch string `json:"task_branch,omitempty"`
	}
	if len(wi.DeclaredResources) > 0 {
		json.Unmarshal(wi.DeclaredResources, &declaredRes) //nolint:errcheck
	}
	for _, res := range declaredRes {
		lockType, lockKey := resourceToLock(DeclaredResourceItem{Type: res.Type, URI: res.URI, TaskBranch: res.TaskBranch})
		if lockType == "" {
			continue
		}
		tx.Exec(ctx, `
			INSERT INTO resource_locks (resource_type, resource_key, owner_attempt_id, claim_epoch)
			VALUES ($1, $2, $3, $4)
			ON CONFLICT (resource_type, resource_key) DO UPDATE
			  SET owner_attempt_id=$3, claim_epoch=$4, acquired_at=clock_timestamp()`,
			lockType, lockKey, newAttemptID, newEpoch) //nolint:errcheck
	}

	// Update work_item to running with new attempt
	_, err2 = tx.Exec(ctx, `
		UPDATE work_items SET status='running', current_attempt_id=$1, current_attempt_epoch=$2 WHERE id=$3`,
		newAttemptID, newEpoch, wi.ID)
	if err2 != nil {
		return nil, NewErr(ErrInternalError, "failed to update work_item after force_takeover")
	}

	if err2 = tx.Commit(ctx); err2 != nil {
		return nil, NewErr(ErrInternalError, "failed to commit force_takeover")
	}

	return &ForceTakeoverResponse{
		PriorAttemptID:    priorID,
		PriorActorDisplay: currentActorDisplay,
		NewAttemptID:      newAttemptID,
		NewClaimEpoch:     newEpoch,
		NewSessionSecret:  newSecret,
		OK:                true,
	}, nil
}


// generateSessionSecret returns (plaintext, nil) for a new 32-byte session secret.
func generateSessionSecret() (string, error) {
	b := make([]byte, 32)
	if _, err := cryptoRand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// verifyAttemptCredential validates attempt_id, claim_epoch, and session_secret
// against the DB. Matches §21 of the design doc.
func verifyAttemptCredential(ctx context.Context, tx pgx.Tx, wi WorkItem, attemptID string, claimEpoch int64, sessionSecret string) *AihubError {
	// 1. Verify attempt is the current attempt for the wi
	if wi.CurrentAttemptID == nil || *wi.CurrentAttemptID != attemptID {
		return NewErr(ErrConflictEpochMismatch, "attempt_id does not match current attempt for this work item")
	}

	// 2. Load the attempt
	var storedEpoch int64
	var storedSecretHash, storedStatus string
	err := tx.QueryRow(ctx, `
		SELECT claim_epoch, session_secret_hash, status FROM run_attempts WHERE id=$1`, attemptID,
	).Scan(&storedEpoch, &storedSecretHash, &storedStatus)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return NewErr(ErrNotFound, "run_attempt not found")
		}
		return NewErr(ErrInternalError, "failed to load run_attempt")
	}

	// 3. Verify claim_epoch
	if storedEpoch != claimEpoch {
		return NewErr(ErrConflictEpochMismatch, "claim_epoch mismatch")
	}

	// 4. Verify session_secret (constant-time)
	hash := hashSecretInternal(sessionSecret)
	storedHashBytes, err2 := hex.DecodeString(storedSecretHash)
	if err2 != nil {
		return NewErr(ErrStaleCredential, "invalid stored credential format")
	}
	hashBytes, err3 := hex.DecodeString(hash)
	if err3 != nil {
		return NewErr(ErrInternalError, "failed to decode computed hash")
	}
	if subtle.ConstantTimeCompare(storedHashBytes, hashBytes) != 1 {
		return NewErr(ErrUnauthorized, "invalid session_secret")
	}

	// 5. Attempt must be running
	if storedStatus != "running" {
		return NewErr(ErrAttemptMismatch, fmt.Sprintf("attempt status is %q; only running attempts can be used", storedStatus))
	}

	// 6. Update last_active_at (heartbeat)
	tx.Exec(ctx, `UPDATE run_attempts SET last_active_at=clock_timestamp() WHERE id=$1`, attemptID) //nolint:errcheck

	return nil
}

// VerifyAttemptCredentialPool is the exported pool-based variant used by HTTP handlers
// that don't yet have an open transaction (e.g. step routes).
func VerifyAttemptCredentialPool(ctx context.Context, pool *pgxpool.Pool, wiID, attemptID string, claimEpoch int64, sessionSecret string) *AihubError {
	wi, aihubErr := GetWorkItem(ctx, pool, wiID)
	if aihubErr != nil {
		return aihubErr
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		return NewErr(ErrInternalError, "failed to begin verification tx")
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	if aihubErr = verifyAttemptCredential(ctx, tx, *wi, attemptID, claimEpoch, sessionSecret); aihubErr != nil {
		return aihubErr
	}
	tx.Commit(ctx) //nolint:errcheck
	return nil
}
