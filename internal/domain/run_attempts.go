package domain

import (
	"context"
	cryptoRand "crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/GMISWE/ieops-aihub/internal/auth"
)

// RunAttempt mirrors the run_attempts table.
type RunAttempt struct {
	ID                 string           `json:"id"`
	WorkItemID         string           `json:"work_item_id"`
	Status             string           `json:"status"`
	ClaimEpoch         int64            `json:"claim_epoch"`
	IdempotencyKey     string           `json:"idempotency_key"`
	LastActiveAt       time.Time        `json:"last_active_at"`
	ActorUserID        string           `json:"actor_user_id"`
	APIKeyID           string           `json:"api_key_id"`
	ActorDisplay       string           `json:"actor_display"`
	MachineID          string           `json:"machine_id"`
	SessionSecretHash  string           `json:"session_secret_hash"`
	ParentAttemptID    *string          `json:"parent_attempt_id"`
	PhaseConfigVersion *int             `json:"phase_config_version"` // kept as audit field; always NULL since scenario_phase_configs was removed (aihub#38)
	PreparedWorkspace  *json.RawMessage `json:"prepared_workspace"`
	StartedAt          time.Time        `json:"started_at"`
	EndedAt            *time.Time       `json:"ended_at"`
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
	IdempotencyKey string            `json:"idempotency_key"`
	SessionInfo    SessionInfo       `json:"session_info"`
	RequestedLocks []ResourceLockReq `json:"requested_locks"`
	Mode           string            `json:"mode"` // "fresh" | "resume"
	ForceOver      bool              `json:"force_takeover"`
	ScenarioRef    *string           `json:"scenario_ref,omitempty"` // git SHA of local scenario clone at claim time
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
	AttemptID           string         `json:"attempt_id"`
	ClaimEpoch          int64          `json:"claim_epoch"`
	AcquiredLocks       []ResourceLock `json:"acquired_locks"`
	CurrentAttemptEpoch int64          `json:"current_attempt_epoch"`
	StepRecoveryHint    string         `json:"step_recovery_hint,omitempty"`
	// UnrecognizedResources lists declared_resources entries whose type the lock
	// mapper could not understand and which are therefore holding NO lock
	// (aihub#238). Stored data cannot be rejected at claim time without making
	// historical work items unclaimable, so the claim succeeds and says so here
	// rather than staying silent. Empty on a healthy work item.
	UnrecognizedResources []string `json:"unrecognized_resources,omitempty"`
	RequiresHumanSession  *bool    `json:"requires_human_session"`
	WIType                *string  `json:"wi_type"`
	Slug                  string   `json:"slug,omitempty"`
	Project               string   `json:"project,omitempty"`
	ID                    string   `json:"id,omitempty"`
	// Goal is the work item's goal text, echoed back so the claiming client can
	// name the task branch after it — polyforge/<project>-<seq>-<kebab goal>
	// instead of the unreadable polyforge/<ulid8> (aihub#322). Without it the MCP
	// layer would need a second round-trip to read the goal it just claimed, and
	// the branch name has to be derived on the resume path too, where no such
	// fetch happens today. Not forwarded to the LLM by the MCP claim tool; it is
	// only consumed locally to build the branch name.
	Goal string `json:"goal,omitempty"`
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
		FROM work_items WHERE (id = $1 OR slug = $1) FOR UPDATE`, wiID,
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
		// aihub#334: this transaction is SERIALIZABLE (see the BeginTx above), so
		// two agents claiming the same work item at once make one of them lose
		// the FOR UPDATE race with SQLSTATE 40001 — a retryable conflict, not a
		// broken server. Unlike UpdateProject's copy of this hop, which needs
		// someone to raise an isolation level first, this one is reachable TODAY.
		if aerr := retryConflictErr(err, "failed to lock work_item"); aerr != nil {
			return nil, aerr
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
			// aihub#334: this is the same shape as unblockDependentWI's sweep —
			// a lazily-streamed result set whose error has no other exit. This
			// transaction is SERIALIZABLE, so 40001 here is reachable, and
			// without this the loop just looks empty and the caller is told 500
			// at commit with no SQLSTATE left.
			if err := lockRows.Err(); err != nil {
				if aerr := retryConflictErr(err, "failed to load locks for idempotent claim"); aerr != nil {
					return nil, aerr
				}
			}
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
			// aihub#334: SSI reports most SERIALIZABLE conflicts at COMMIT
			// rather than at the statement that caused them.
			if aerr := retryConflictErr(err, "failed to commit idempotent claim"); aerr != nil {
				return nil, aerr
			}
			return nil, NewErr(ErrInternalError, "failed to commit idempotent claim")
		}
		return &ClaimResponse{
			AttemptID:           existingAttemptID,
			ClaimEpoch:          existingEpoch,
			AcquiredLocks:       existingLocks,
			CurrentAttemptEpoch: existingEpoch,
			StepRecoveryHint:    idemHint,
			// aihub#238: an idempotent replay must repeat the warning too, or the
			// signal disappears on retry — exactly when a confused caller looks again.
			UnrecognizedResources: UnrecognizedDeclaredResources(wi.DeclaredResources),
			RequiresHumanSession:  wi.RequiresHumanSession,
			WIType:                wi.WIType,
			Slug:                  wi.Slug,
			Project:               wi.Project,
			ID:                    wi.ID,
			// aihub#322: an idempotent replay must carry the goal too — the replay is
			// what a retried claim sees, and it is the same call that has to build the
			// worktree and its branch.
			Goal: wi.Goal,
		}, nil
	}

	// C-R9-6: wi_type must be set before claim
	if wi.WIType == nil || *wi.WIType == "" {
		return nil, NewErr(ErrWITypeMismatch, "wi_type is not set; update it with pf_update_work_item(wi_type=...) before claiming")
	}

	// Determine requires_human_session for the wi_type.
	// scenario_phase_configs has been removed; the client is responsible for
	// setting requires_human_session on the wi before claiming. If it is already
	// set on the wi row we use that value; if NULL we default to true (conservative).
	wiTypeDef := struct {
		RequiresHumanSession bool
	}{RequiresHumanSession: true}
	if wi.RequiresHumanSession != nil {
		wiTypeDef.RequiresHumanSession = *wi.RequiresHumanSession
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
							"id":             *wi.CurrentAttemptID,
							"actor_display":  currentActorDisplay,
							"claim_epoch":    currentEpoch,
							"last_active_at": currentLastActive.Format(time.RFC3339),
						},
					},
				)
			}
		}
	} else if wi.Status == "blocked" {
		// aihub#242: removing a blocked wi's last active dependency now
		// auto-requeues it (DeleteDependency / requeueIfUnblocked in
		// dependencies.go), so this rejection is no longer a dead end — the
		// caller (or its blockers' owners) has a real path out via
		// pf_remove_dependency, or the reporter can cancel it (CancelWorkItem /
		// cancelGate now allows cancelling from status=blocked).
		//
		// force_takeover deliberately does NOT bypass this gate: router.go's
		// force_takeover permission check only applies when wi.CurrentAttemptID
		// is set, and a blocked wi has none, so any writer could otherwise
		// bypass the block by claiming with force_takeover=true. Do not "fix"
		// this by moving the blocked check after the force_takeover check
		// without first adding a role gate here too.
		return nil, NewErr(ErrConflictTerminalState, "work item is blocked by dependencies; resolve blockers first")
	} else if wi.Status == "paused" || wi.Status == "queued" {
		// Normal claim — no extra checks required.
	} else if wi.Status == "wrapped" || wi.Status == "failed" || wi.Status == "cancelled" {
		return nil, NewErr(ErrConflictTerminalState, fmt.Sprintf("work item is in terminal state: %s", wi.Status))
	}

	// aihub#238: validate the CLIENT-SUPPLIED locks, before the derivation block
	// below can append server-derived entries to the same slice.
	//
	// Ordering is load-bearing. Validating the merged slice instead would apply
	// input rules to server-derived entries, and derivation can legitimately
	// produce a well-typed lock with an empty key from bad stored data — e.g. a
	// stored {"type":"service"} with no uri maps to ("deploy_env", ""). That would
	// 400 the claim and make an existing work item unclaimable, which is exactly
	// the outcome this change exists to avoid.
	if aihubErr := ValidateRequestedLocks(req.RequestedLocks); aihubErr != nil {
		return nil, aihubErr
	}

	// §4.3 + §15: locks are derived from wi.declared_resources at claim time.
	// If the client did not pass RequestedLocks explicitly, derive them from the
	// work_item's declared_resources via resourceToLock mapping (§25 C-R3-8).
	// Server-derived file_scope keys are project-namespaced (aihub#222). NOTE: a
	// client that passes RequestedLocks explicitly is trusted verbatim and its
	// file_scope keys are NOT re-namespaced here; the standard polyforge flow always
	// leaves RequestedLocks empty and derives server-side, so this raw-API path is
	// a known, low-exposure limitation rather than a normal code path.
	//
	// lockProbes[i] pairs with req.RequestedLocks[i] and holds the set of
	// EXISTING keys that block that lock (aihub#261). A client-supplied lock is
	// trusted verbatim, so its probe is plain key equality — the pre-aihub#261
	// behaviour, unchanged.
	lockProbes := make([]lockConflictProbe, 0, len(req.RequestedLocks))
	for _, l := range req.RequestedLocks {
		lockProbes = append(lockProbes, exactProbe(l.ResourceKey))
	}
	if len(req.RequestedLocks) == 0 && len(wi.DeclaredResources) > 0 {
		// aihub#261: unmarshalDeclaredResources replaces the local anonymous
		// struct this site used to declare. That struct's hand-written field list
		// was the failure mode aihub#342 called the quietest form of this class —
		// a field missing from the list never reaches the mapper and reads as a
		// zero value — and the `repo` field added here is exactly such a field.
		for _, d := range unmarshalDeclaredResources(wi.DeclaredResources) {
			// derivedLockProbe, not resourceToLock (aihub#342): an intent=read
			// declaration takes no write lock. This line is the reported
			// instance — claim took a file_scope lock for a read-only path
			// and then 409'd the next claimer, while pf_predict_conflicts
			// reported the identical input as `info`, so the pre-claim gate
			// had no predictive value at all.
			lockType, lockKey, probe := derivedLockProbe(d, wi.Project)
			// aihub#238: an empty key is possible from bad stored data (a
			// `service`/`path` entry with no uri). Never insert it — the row is
			// meaningless as a lock and would collide with every other empty-key
			// row of the same type. Skipping keeps the wi claimable; the entry is
			// reported via unrecognizedResources below rather than dropped silently.
			if lockType == "" || lockKey == "" {
				continue
			}
			req.RequestedLocks = append(req.RequestedLocks, ResourceLockReq{
				ResourceType: lockType, ResourceKey: lockKey,
			})
			lockProbes = append(lockProbes, probe)
		}
	}

	// aihub#238: this path reads ALREADY-STORED declared_resources, so it cannot
	// reject a mistyped entry without making historical work items unclaimable
	// (~14% of entries in aihub's own recent wis are mistyped). Report instead of
	// failing, so the claimer at least learns that something they declared is
	// holding no lock. New bad data is prevented upstream, by the create/update
	// validation in work_items.go.
	unrecognizedResources := UnrecognizedDeclaredResources(wi.DeclaredResources)

	// Check lock conflicts (advisory — actual conflict resolution in claim)
	if len(req.RequestedLocks) > 0 && !isTakeover {
		var conflictAttemptID, conflictActorDisplay, conflictWISlug string
		for i, l := range req.RequestedLocks {
			// aihub#261: probe the set of keys that block this lock, not just the
			// key it will insert. For an unqualified file_scope declaration those
			// differ — it must still collide with every repo-qualified variant of
			// the same path, or making keys finer would buy the fix a missed
			// conflict, which is the one direction that is worse than the bug.
			probe := lockProbes[i]
			err = tx.QueryRow(ctx, `
				SELECT rl.owner_attempt_id, ra.actor_display, wi2.slug
				FROM resource_locks rl
				JOIN run_attempts ra ON ra.id = rl.owner_attempt_id
				JOIN work_items wi2 ON wi2.id = ra.work_item_id
				WHERE `+lockConflictWhereClause+`
				  AND ra.status IN ('running', 'paused')
				  AND ra.work_item_id != $4`,
				l.ResourceType, probe.Keys, probe.LikePattern, wi.ID,
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

	// aihub#343: one lock operation per claim, so the lock_acquired /
	// lock_released events this call emits share an op_id and can be regrouped
	// without inferring the grouping from timestamps.
	lockActor := lockEventActor{UserID: callerUserID, Display: callerDisplay, APIKeyID: callerAPIKeyID}

	// If takeover: supersede old attempt, delete its locks
	if isTakeover && priorAttemptID != "" {
		_, err = tx.Exec(ctx, `
			UPDATE run_attempts SET status='superseded', ended_at=clock_timestamp()
			WHERE id=$1`, priorAttemptID)
		if err != nil {
			return nil, dbErr(err, "failed to supersede prior attempt")
		}
		// aihub#343: through releaseLocks, so each row this DELETE actually
		// removes gets a lock_released event. This is the release whose absence
		// made aihub#283's "the init.go write lock was released" claim
		// unfalsifiable.
		if _, relErr := releaseLocks(ctx, tx, lockDeleteByAttemptSQL,
			newLockOp(lockCauseClaimTakeover, lockActor).withExtra(map[string]any{
				"superseded_attempt_id": priorAttemptID,
			}), priorAttemptID,
		); relErr != nil {
			return nil, dbErr(relErr, "failed to delete prior attempt locks")
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
			$10, NULL, clock_timestamp(), clock_timestamp()
		)`,
		newAttemptID, wi.ID, newEpoch, req.IdempotencyKey,
		callerUserID, callerAPIKeyID, callerDisplay, req.SessionInfo.MachineID, secretHash,
		nilIfEmpty(priorAttemptID),
	)
	if err != nil {
		return nil, dbErrCause(err, "failed to insert run_attempt")
	}

	// N5: emit attempt_superseded event now that we have the real newAttemptID
	if isTakeover && priorAttemptID != "" {
		supEvtID := NewID("evt")
		supPayload, _ := json.Marshal(map[string]any{
			"superseded_by_attempt_id": newAttemptID,
			"reason":                   "claim by same user or explicit takeover",
			"actor_user_id":            callerUserID,
		})
		_, _ = tx.Exec(ctx, `
			INSERT INTO agent_events (id, work_item_id, actor_user_id, actor_display, event_type, payload, project)
			VALUES ($1, $2, $3, $4, 'attempt_superseded', $5, $6)`,
			supEvtID, wi.ID, callerUserID, callerDisplay, supPayload, wi.Project,
		)
	}

	// Insert resource_locks for requested locks.
	//
	// aihub#343: through acquireLockUpsert, which emits one lock_acquired per row
	// AND a lock_released for any owner the upsert displaced. The displacement
	// half matters: ON CONFLICT DO UPDATE can rewrite an un-swept orphan row's
	// owner, and recording only the acquisition would leave a reader following
	// the previous owner with an unmatched lock_acquired — reading as "still
	// held" for a lock that changed hands.
	claimOp := newLockOp(lockCauseClaim, lockActor).withExtra(map[string]any{
		"is_takeover": isTakeover,
		"is_resume":   req.Mode == "resume",
	})
	acquiredLocks := make([]ResourceLock, 0, len(req.RequestedLocks))
	for _, l := range req.RequestedLocks {
		got, upErr := acquireLockUpsert(ctx, tx, l.ResourceType, l.ResourceKey,
			newAttemptID, newEpoch, wi.Project, wi.ID, claimOp)
		if upErr != nil {
			return nil, dbErrCause(upErr, fmt.Sprintf("failed to acquire lock %s:%s", l.ResourceType, l.ResourceKey))
		}
		acquiredLocks = append(acquiredLocks, ResourceLock{
			ResourceType:   got.ResourceType,
			ResourceKey:    got.ResourceKey,
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
		return nil, dbErr(err, "failed to update work_item status")
	}

	// Bug fix: read step state BEFORE the upsert resets it to idle.
	// The hint must reflect what the prior attempt left behind, not the post-reset state.
	var priorStepStatus string
	var priorStepStartedAt *time.Time
	priorStepErr := tx.QueryRow(ctx,
		`SELECT current_step_status, step_started_at FROM wi_step_state WHERE work_item_id=$1`, wi.ID,
	).Scan(&priorStepStatus, &priorStepStartedAt)

	// Upsert wi_step_state (C-R7-9: INSERT ... ON CONFLICT DO UPDATE)
	// scenario_ref is the git SHA of the local scenario clone at claim time (client-provided).
	_, err = tx.Exec(ctx, `
		INSERT INTO wi_step_state (work_item_id, wi_type, graph_source, current_step, current_step_status, scenario_ref)
		VALUES ($1, $2, 'scenario_config', NULL, 'idle', $3)
		ON CONFLICT (work_item_id) DO UPDATE
		  SET wi_type=$2, graph_source='scenario_config',
		      scenario_ref=COALESCE($3, wi_step_state.scenario_ref),
		      current_step_status='idle', current_step_attempt=NULL,
		      step_started_at=NULL, updated_at=clock_timestamp()`,
		wi.ID, wi.WIType, req.ScenarioRef,
	)
	if err != nil {
		// Non-fatal but log: agent will lack scenario_ref and fall back to default behavior.
		fmt.Fprintf(os.Stderr, "claim: wi_step_state upsert failed (scenario_ref not written): %v\n", err)
	}

	// C-R9-12: If wi.requires_human_session IS NULL, write back resolved value from config
	resolvedRHS := wiTypeDef.RequiresHumanSession
	if wi.RequiresHumanSession == nil {
		_, err = tx.Exec(ctx, `
			UPDATE work_items SET requires_human_session=$1 WHERE id=$2`,
			resolvedRHS, wi.ID,
		)
		if err != nil {
			return nil, dbErr(err, "failed to set requires_human_session")
		}
		// Emit wi_classification_resolved event
		evtID := NewID("evt")
		evtPayload, _ := json.Marshal(map[string]any{
			"wi_type":                *wi.WIType,
			"requires_human_session": resolvedRHS,
		})
		_, _ = tx.Exec(ctx, `
			INSERT INTO agent_events (id, work_item_id, actor_user_id, actor_display, event_type, payload, project)
			VALUES ($1, $2, $3, $4, 'wi_classification_resolved', $5, $6)`,
			evtID, wi.ID, callerUserID, callerDisplay, evtPayload, wi.Project,
		)
		wi.RequiresHumanSession = &resolvedRHS
	} else if *wi.RequiresHumanSession != resolvedRHS {
		// C-R9-12: mismatch → 409 REQUIRES_HUMAN_SESSION_MISMATCH
		tx.Rollback(ctx) //nolint:errcheck
		return nil, NewErrDetails(ErrRequiresHumanSessionMismatch,
			fmt.Sprintf("wi.requires_human_session=%v but phase config says %v for wi_type %q",
				*wi.RequiresHumanSession, resolvedRHS, *wi.WIType),
			map[string]any{
				"db_value":         *wi.RequiresHumanSession,
				"phase_yaml_value": resolvedRHS,
				"wi_type":          *wi.WIType,
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
	_, _ = tx.Exec(ctx, `
		INSERT INTO agent_events (id, work_item_id, run_attempt_id, actor_user_id, actor_display, api_key_id, event_type, payload, project)
		VALUES ($1, $2, $3, $4, $5, $6, 'attempt_started', $7, $8)`,
		evtID, wi.ID, newAttemptID, callerUserID, callerDisplay, callerAPIKeyID, evtPayload, wi.Project,
	)

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
		if aerr := retryConflictErr(err, "failed to commit claim transaction"); aerr != nil { // aihub#334
			return nil, aerr
		}
		return nil, NewErr(ErrInternalError, "failed to commit claim transaction")
	}

	return &ClaimResponse{
		AttemptID:             newAttemptID,
		ClaimEpoch:            newEpoch,
		AcquiredLocks:         acquiredLocks,
		CurrentAttemptEpoch:   newEpoch,
		StepRecoveryHint:      stepRecoveryHint,
		UnrecognizedResources: unrecognizedResources,
		RequiresHumanSession:  wi.RequiresHumanSession,
		WIType:                wi.WIType,
		Slug:                  wi.Slug,
		Project:               wi.Project,
		ID:                    wi.ID,
		Goal:                  wi.Goal,
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
	AttemptID          string  `json:"attempt_id"`
	ClaimEpoch         int64   `json:"claim_epoch"`
	SessionSecret      string  `json:"session_secret"`
	Status             string  `json:"status"` // "wrapped" | "failed" | "paused"
	ForceTerminateStep bool    `json:"force_terminate_step"`
	PauseReason        *string `json:"pause_reason,omitempty"`
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
		if aerr := retryConflictErr(err, "failed to lock work_item"); aerr != nil { // aihub#334
			return aerr
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

	// Set run_attempt status. pause_reason is only meaningful for status=paused
	// but is written unconditionally from req (nil for wrapped/failed), matching
	// the column's nullable, informational-only nature.
	_, err = tx.Exec(ctx, `
		UPDATE run_attempts SET status=$1, ended_at=clock_timestamp(), pause_reason=$2 WHERE id=$3`,
		req.Status, req.PauseReason, req.AttemptID,
	)
	if err != nil {
		return dbErr(err, "failed to update run_attempt status")
	}

	// N4 (revised): on paused, release only file_scope locks (acquired mid-attempt via
	// FnAcquireLocks); git_branch/deploy_env locks are retained so resume can continue
	// holding the branch/env. On terminal (wrapped/failed), release all locks.
	// NOTE: resume re-acquires released file_scope locks via the claim path. Claim's
	// INSERT uses DO UPDATE, but the advisory conflict check (status IN running,paused)
	// runs first in the same tx and hard-fails if another attempt took the file while
	// paused — so resume surfaces a conflict rather than stealing. This ordering is
	// load-bearing: do not move the DO UPDATE ahead of the conflict check.
	//
	// aihub#343: both branches go through releaseLocks, so a reader can tell the
	// two apart from the event stream alone. That distinction is the whole
	// question a later claimer asks — a paused attempt legitimately keeps its
	// git_branch/deploy_env locks, so "this attempt ended and the lock is still
	// there" is correct on pause and a leak on terminal, and the `cause` field is
	// what separates them.
	if req.Status != "paused" {
		if _, relErr := releaseLocks(ctx, tx, lockDeleteByAttemptSQL,
			newLockOp(lockCauseAttemptTerminal, lockEventActor{}).withExtra(map[string]any{
				"attempt_status": req.Status,
			}), req.AttemptID,
		); relErr != nil {
			return dbErr(relErr, "failed to release resource locks")
		}
	} else {
		if _, relErr := releaseLocks(ctx, tx, acquireLocksReleasePausedSQL,
			newLockOp(lockCauseAttemptPaused, lockEventActor{}).withExtra(map[string]any{
				"retained_types": "git_branch, deploy_env, worktree, tcp_port",
			}), req.AttemptID,
		); relErr != nil {
			return dbErr(relErr, "failed to release file_scope locks on pause")
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
		return dbErr(err, "failed to update work_item status")
	}

	// Emit attempt_completed event
	evtID := NewID("evt")
	evtPayloadMap := map[string]any{
		"status": req.Status,
	}
	if req.PauseReason != nil {
		evtPayloadMap["pause_reason"] = *req.PauseReason
	}
	evtPayload, _ := json.Marshal(evtPayloadMap)
	_, _ = tx.Exec(ctx, `
		INSERT INTO agent_events (id, work_item_id, run_attempt_id, event_type, payload, project)
		VALUES ($1, $2, $3, 'attempt_completed', $4, $5)`,
		evtID, wi.ID, req.AttemptID, evtPayload, wi.Project,
	)

	// If terminal (wrapped/failed): unblock dependent wi + set methodology expires_at
	if req.Status == "wrapped" || req.Status == "failed" {
		if aihubErr := unblockDependentWI(ctx, tx, wi.ID, wi.Project); aihubErr != nil {
			// aihub#334: the second half of instance 3. Discarding this used to
			// be safe because unblockDependentWI never returned anything but
			// nil; it now returns non-nil ONLY when the transaction has already
			// been rolled back by Postgres (see its return contract), and
			// "non-fatal" is exactly the wrong word for that — nothing below
			// can commit, so continuing only replaces a classified 409 with an
			// unclassifiable 500 at tx.Commit.
			return aihubErr
		}
		// C4: set methodology.* memory expires_at = closed_at + 90d
		_, _ = tx.Exec(ctx, `
			UPDATE memories SET expires_at = clock_timestamp() + interval '90 days'
			WHERE work_item_id = $1 AND type LIKE 'methodology.%' AND expires_at IS NULL`,
			wi.ID)
	}

	if err := tx.Commit(ctx); err != nil {
		if aerr := retryConflictErr(err, "failed to commit complete_attempt"); aerr != nil { // aihub#334
			return aerr
		}
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
		return dbErrCause(err, "failed to insert step_completion for force_terminate")
	}

	// Emit step_failed event (§4.3 force_terminate_step flow)
	evtID := NewID("evt")
	payload, _ := json.Marshal(map[string]any{
		"step_id":         *currentStep,
		"step_attempt_id": saID,
		"error_type":      "force_terminate_step",
		"escalated":       false,
	})
	_, _ = tx.Exec(ctx, `
		INSERT INTO agent_events (id, work_item_id, run_attempt_id, event_type, payload, project)
		VALUES ($1, $2, $3, 'step_failed', $4,
		        (SELECT project FROM work_items WHERE id=$2))`,
		evtID, wiID, attemptID, payload)

	// Reset wi_step_state
	_, err = tx.Exec(ctx, `
		UPDATE wi_step_state
		SET current_step_status='idle', current_step_attempt=NULL, step_started_at=NULL,
		    version=version+1, updated_at=clock_timestamp()
		WHERE work_item_id=$1`, wiID,
	)
	if err != nil {
		return dbErr(err, "failed to reset wi_step_state")
	}
	return nil
}

// unblockDependentWI handles the unblock sweep after a wi completes.
// Implements C-R7-2: FOR UPDATE ORDER BY id to prevent deadlocks.
//
// Return contract (aihub#334): a non-nil result means THE TRANSACTION IS DEAD,
// not merely that the sweep did not finish. Everything this function can fail
// at is best-effort and reported by leaving a wi blocked — except a Postgres
// class 40 rollback, which aborts the enclosing transaction, so every statement
// after it is a no-op and the caller's Commit is guaranteed to fail. The caller
// must therefore propagate a non-nil result rather than treat it as advisory.
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
		// aihub#334: this hop is why a fix placed only at the pgx-error ->
		// AihubError conversion point does not close this defect. Swallowing
		// the error here does not make it go away — it makes it UNRECOGNISABLE.
		// A 40001 raised by this FOR UPDATE aborts the transaction; the caller
		// then reaches tx.Commit, where pgx returns pgx.ErrTxCommitRollback,
		// which is not a *pgconn.PgError and carries no SQLSTATE. Every
		// SQLSTATE-based classifier downstream sees an unremarkable error and
		// emits 500, with every test still green. The classification has to
		// happen HERE, while the *pgconn.PgError is still in hand.
		if aerr := retryConflictErr(err, "failed to lock dependent work_items"); aerr != nil {
			return aerr
		}
		// Any other error stays best-effort, as before: the sweep is a
		// convenience, and leaving a dependent wi blocked is recoverable.
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
	// aihub#334, measured: this is where the 40001 actually arrives, NOT at the
	// `err` returned by tx.Query above. pgx's extended-protocol Query is lazy —
	// it returns a Rows with no error and the server's failure only materialises
	// while the result set is drained, at which point it is reachable ONLY
	// through rows.Err(). This loop never called it, so the error had no exit
	// from this function at all: the transaction was already dead, `rows` simply
	// looked empty, the sweep "found no candidates", and the caller went on to
	// tx.Commit and got pgx.ErrTxCommitRollback with no SQLSTATE attached.
	//
	// This is the shape that makes instance 3 immune to a central pgx-error ->
	// AihubError conversion point, and it is one step further from the surface
	// than "the FOR UPDATE error is discarded": there was no discarded error
	// value, because nothing ever asked for one.
	if err := rows.Err(); err != nil {
		if aerr := retryConflictErr(err, "failed to lock dependent work_items"); aerr != nil {
			return aerr
		}
		// Any other error stays best-effort, as before: the sweep is a
		// convenience, and leaving a dependent wi blocked is recoverable.
		return nil
	}

	for _, blockedID := range candidateIDs {
		// aihub#242: status recompute now lives in the shared requeueIfUnblocked
		// helper (also used by DeleteDependency). Pass wiID as the excluded
		// blocker, matching this function's pre-refactor SQL exactly.
		unblocked, err := requeueIfUnblocked(ctx, tx, blockedID, wiID)
		if err != nil {
			// aihub#334: same reasoning as the FOR UPDATE above — a class 40
			// rollback here has killed the transaction, so "skip this one and
			// carry on" would spend the rest of the loop issuing statements
			// that cannot execute and then hand the caller an unclassifiable
			// commit failure.
			if aerr := retryConflictErr(err, "failed to requeue dependent work_item"); aerr != nil {
				return aerr
			}
			// Fail closed: skip this blockedID and leave it blocked. This is a
			// deliberate behaviour change from the pre-refactor code, not a
			// continuation of it — the old inline query did
			// `.Scan(&stillBlocked)` with the error ignored, so a Scan failure
			// silently left stillBlocked at its zero value (0) and the old code
			// went ahead and requeued the wi anyway (fail-open). Here, a
			// requeueIfUnblocked error means we couldn't verify no active
			// blocker remains, so leaving the wi blocked is the safer choice.
			continue
		}
		if unblocked {
			// Emit wi_unblocked event, SAVEPOINT-isolated (see
			// emitWIUnblockedEvent in dependencies.go) so a failed insert
			// cannot roll back the requeue above.
			evtPayload, _ := json.Marshal(map[string]any{"unblocked_by_wi": wiID})
			emitWIUnblockedEvent(ctx, tx, blockedID, project, evtPayload)
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
	// Canonical work item identity, echoed so the MCP layer can key the state file
	// by the canonical id (the caller may have addressed the wi by slug) and
	// populate Slug/Project — mirroring claim_work_item. Without these, a
	// slug-addressed force_takeover writes a slug-keyed state file with an empty
	// Slug that ResolveStateFile's slug-scan can never match. (aihub#149)
	ID      string `json:"id"`
	Slug    string `json:"slug"`
	Project string `json:"project"`

	PriorAttemptID    string `json:"prior_attempt_id"`
	PriorActorDisplay string `json:"prior_actor_display"`
	// H3: new attempt credentials — written to state file by MCP layer (never returned to LLM)
	NewAttemptID  string `json:"new_attempt_id"`
	NewClaimEpoch int64  `json:"new_claim_epoch"`
	// NewSessionSecret is intentionally NOT in JSON (Decision A): the client supplied it
	// in the request body and already knows the plaintext.
	NewSessionSecret string `json:"-"`
	OK               bool   `json:"ok"`
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
		return nil, dbErr(err, "failed to load current attempt")
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
		return nil, dbErr(err2, "failed to supersede prior attempt")
	}
	// Delete locks.
	//
	// aihub#343: through releaseLocks, so the prior holder's locks leave a
	// lock_released trail. The error stays discarded, matching what this line has
	// always done — a force takeover is a recovery operation and failing it over
	// a lock delete would leave the work item stuck with an attempt nobody holds.
	ftActor := lockEventActor{UserID: callerUserID, Display: callerDisplay}
	ftOp := newLockOp(lockCauseForceTakeover, ftActor).withExtra(map[string]any{
		"prior_attempt_id": priorID,
		"reason":           req.Reason,
	})
	releaseLocks(ctx, tx, lockDeleteByAttemptSQL, ftOp, priorID) //nolint:errcheck

	// Emit force_takeover event
	evtID := NewID("evt")
	evtPayload, _ := json.Marshal(map[string]any{
		"prior_attempt_id": priorID,
		"prior_actor":      currentActorDisplay,
		"reason":           req.Reason,
	})
	_, _ = tx.Exec(ctx, `
		INSERT INTO agent_events (id, work_item_id, actor_user_id, actor_display, event_type, payload, project)
		VALUES ($1, $2, $3, $4, 'force_takeover', $5, $6)`,
		evtID, wi.ID, callerUserID, "", evtPayload, wi.Project,
	)

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
		return nil, dbErr(err2, "failed to create new attempt after force_takeover")
	}

	// Re-INSERT resource_locks for new attempt based on wi.DeclaredResources
	// (prior locks were deleted above; new attempt must hold them for conflict detection)
	//
	// aihub#342: `Intent` is a field of this struct, and that is load-bearing.
	// It used to be absent, so the value could not reach the mapper no matter
	// what the mapper did — a takeover re-created write locks for declarations
	// that had asked for none. A missing struct field is the quietest form of
	// this defect: nothing to grep for, and every `intent == "read"` check
	// downstream reads the zero value and passes.
	//
	// aihub#261: the local anonymous struct this loop used to declare is gone,
	// replaced by unmarshalDeclaredResources. The comment above describes exactly
	// why: a field absent from a hand-written list never reaches the mapper. The
	// `repo` field added by aihub#261 would have been the second instance of that
	// bug in this same function, so the list is deleted rather than extended.
	declaredRes := unmarshalDeclaredResources(wi.DeclaredResources)
	// aihub#238: entries the mapper cannot understand yield no lock here either.
	// Stored data, so this must not fail the takeover; the subsequent fresh claim
	// reports them via ClaimResponse.unrecognized_resources.
	//
	// aihub#343: through acquireLockUpsert, one lock_acquired per row. The error
	// stays discarded here as it always was, for the same reason as the delete
	// above.
	for _, res := range declaredRes {
		lockType, lockKey := derivedLock(res, wi.Project)
		if lockType == "" {
			continue
		}
		acquireLockUpsert(ctx, tx, lockType, lockKey, newAttemptID, newEpoch, //nolint:errcheck
			wi.Project, wi.ID, ftOp)
	}

	// Update work_item to running with new attempt
	_, err2 = tx.Exec(ctx, `
		UPDATE work_items SET status='running', current_attempt_id=$1, current_attempt_epoch=$2 WHERE id=$3`,
		newAttemptID, newEpoch, wi.ID)
	if err2 != nil {
		return nil, dbErr(err2, "failed to update work_item after force_takeover")
	}

	if err2 = tx.Commit(ctx); err2 != nil {
		if aerr := retryConflictErr(err2, "failed to commit force_takeover"); aerr != nil { // aihub#334
			return nil, aerr
		}
		return nil, NewErr(ErrInternalError, "failed to commit force_takeover")
	}

	return &ForceTakeoverResponse{
		ID:                wi.ID,
		Slug:              wi.Slug,
		Project:           wi.Project,
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
	// 1. Verify attempt is the current attempt for the wi. When the caller's own
	// attempt was superseded (e.g. by a force-takeover), enrich the 409 with who
	// took over and when, so the losing session can explain itself (aihub#209).
	if wi.CurrentAttemptID == nil || *wi.CurrentAttemptID != attemptID {
		return NewErrDetails(ErrConflictEpochMismatch,
			"attempt_id does not match current attempt for this work item",
			supersededByDetails(ctx, tx, attemptID, wi.CurrentAttemptID))
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
		return dbErr(err, "failed to load run_attempt")
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

	// 5. Attempt must be running. A paused attempt gets a distinct code so the
	// client keeps its state file and points the user at resume, instead of
	// treating it as a stale-credential mismatch and deleting it (aihub#209).
	if storedStatus != "running" {
		if storedStatus == "paused" {
			return NewErr(ErrAttemptPaused, "attempt is paused; resume it before continuing")
		}
		return NewErr(ErrAttemptMismatch, fmt.Sprintf("attempt status is %q; only running attempts can be used", storedStatus))
	}

	// 6. Update last_active_at (heartbeat)
	tx.Exec(ctx, `UPDATE run_attempts SET last_active_at=clock_timestamp() WHERE id=$1`, attemptID) //nolint:errcheck

	return nil
}

// supersededByDetails returns {"superseded_by": {"actor_display", "at"}} when the
// caller's attempt has been superseded (its row exists with status 'superseded'),
// otherwise nil so unrelated epoch mismatches carry no details. Best-effort: any
// query error yields nil rather than masking the original credential error.
func supersededByDetails(ctx context.Context, tx pgx.Tx, callerAttemptID string, currentAttemptID *string) any {
	var callerStatus string
	var endedAt *time.Time
	if err := tx.QueryRow(ctx,
		`SELECT status, ended_at FROM run_attempts WHERE id=$1`, callerAttemptID,
	).Scan(&callerStatus, &endedAt); err != nil || callerStatus != "superseded" {
		return nil
	}
	sb := map[string]any{}
	if currentAttemptID != nil {
		var actor string
		if err := tx.QueryRow(ctx,
			`SELECT actor_display FROM run_attempts WHERE id=$1`, *currentAttemptID,
		).Scan(&actor); err == nil {
			sb["actor_display"] = actor
		}
	}
	if endedAt != nil {
		sb["at"] = endedAt.UTC().Format(time.RFC3339)
	}
	return map[string]any{"superseded_by": sb}
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

// ─── Acquire-locks SQL constants ─────────────────────────────────────────────
//
// These are package-level consts so the domain tests can inspect them without
// a live DB (same pattern as orphanLockSweepSQL in resource_events.go).
//
// ⚠️ Only the read-only collision probe lives here. acquireLocksInsertSQL and
// acquireLocksReleasePausedSQL moved to resource_events.go with every other
// statement that MUTATES resource_locks (aihub#343) — see that file's authority
// rule, and TestLockEvents_NoLockMutatingSQLOutsideThisFile for the gate that
// keeps them there.

// acquireLocksCollisionSQL is the SELECT used to detect lock conflicts during
// FnAcquireLocks. It mirrors the claim-time conflict check (:290-298) exactly
// so collision semantics are identical.
const acquireLocksCollisionSQL = `
	SELECT rl.owner_attempt_id, ra.actor_display, wi2.slug
	FROM resource_locks rl
	JOIN run_attempts ra ON ra.id = rl.owner_attempt_id
	JOIN work_items wi2 ON wi2.id = ra.work_item_id
	WHERE ` + lockConflictWhereClause + `
	  AND ra.status IN ('running', 'paused')`

// ─── AcquireLocks request / response ────────────────────────────────────────

// AcquireLocksRequest is the body for POST /v1/work_items/:id/acquire_locks.
type AcquireLocksRequest struct {
	AttemptID     string `json:"attempt_id"`
	ClaimEpoch    int64  `json:"claim_epoch"`
	SessionSecret string `json:"session_secret"`
}

// AcquireLocksResponse is returned by FnAcquireLocks.
type AcquireLocksResponse struct {
	// Acquired lists the locks THIS call took: file_scope only, one per
	// write-intent path in the work item's declared_resources that was free.
	Acquired []ResourceLock `json:"acquired"`
	// AlreadyHeld lists every OTHER lock this attempt holds, of every type,
	// read straight from resource_locks — not just the ones this call would
	// have re-derived (aihub#345). Acquired and AlreadyHeld are disjoint, and
	// together they are exactly the attempt's lock set.
	//
	// It includes locks with no live declaration behind them: git_branch and
	// deploy_env locks taken at claim, locks from a client-supplied
	// requested_locks, and file_scope locks whose declared_resources entry was
	// removed before aihub#264.
	//
	// ⚠️ aihub#264 changed the last of those. Removing a path from
	// declared_resources through UpdateWorkItem now DOES release its file_scope
	// lock, in the same transaction as the update — so that population is no
	// longer produced by the ordinary API path, and this field reports it only
	// for locks that predate the change or were never derived from a declaration
	// at all. git_branch and deploy_env are deliberately NOT released that way,
	// so they remain the main reason an attempt holds a lock it cannot explain
	// from its current declarations.
	AlreadyHeld []ResourceLock `json:"already_held"`
}

// FnAcquireLocks acquires file_scope write-intent locks for a running attempt
// from the work item's current declared_resources. It never steals locks from
// other attempts (DO NOTHING on conflict; hard-fail on collision).
func FnAcquireLocks(ctx context.Context, pool *pgxpool.Pool, wiID string, req *AcquireLocksRequest) (*AcquireLocksResponse, *AihubError) {
	tx, err := pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return nil, NewErr(ErrInternalError, "failed to begin transaction")
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	// Load and lock the work item row.
	var wi WorkItem
	err = tx.QueryRow(ctx, `
		SELECT id, project, status, declared_resources,
		       current_attempt_id, current_attempt_epoch
		FROM work_items WHERE (id = $1 OR slug = $1) FOR UPDATE`, wiID,
	).Scan(
		&wi.ID, &wi.Project, &wi.Status, &wi.DeclaredResources,
		&wi.CurrentAttemptID, &wi.CurrentAttemptEpoch,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, NewErr(ErrNotFound, fmt.Sprintf("work item %q not found", wiID))
		}
		if aerr := retryConflictErr(err, "failed to lock work_item"); aerr != nil { // aihub#334
			return nil, aerr
		}
		return nil, NewErr(ErrInternalError, "failed to lock work_item")
	}

	// Only running work items can acquire additional locks.
	if wi.Status != "running" {
		return nil, NewErr(ErrAttemptMismatch, fmt.Sprintf("work item status is %q; only running work items can acquire locks", wi.Status))
	}

	// Verify credentials; also confirms attempt is running and matches current.
	if aihubErr := verifyAttemptCredential(ctx, tx, wi, req.AttemptID, req.ClaimEpoch, req.SessionSecret); aihubErr != nil {
		return nil, aihubErr
	}

	// Derive target locks: file_scope + write-intent only.
	//
	// aihub#261: resolveDeclaredRepos, so a path entry inherits the repo the
	// payload declares exactly as it does at claim. Without it this endpoint
	// would acquire the unqualified key for a work item whose claim took the
	// qualified one — the same path, twice, under two names.
	//
	// 🔴 Deliberately NOT routed through unmarshalDeclaredResources, unlike claim
	// and force_takeover. Those two are tolerant of an unparseable payload by
	// design (a work item must stay claimable), whereas this endpoint has always
	// returned an error for one, and it should: its whole contract is "tell me
	// which locks I now hold". Silently deriving zero targets would answer that
	// question with an empty list and a 200 — a caller that reads it as "I hold
	// nothing else" is being told something the server never checked. The shared
	// decoder exists to kill the hand-written-struct hazard, and this site never
	// had one: it already decodes into DeclaredResourceItem, so the repo field
	// reaches it regardless.
	declared, decodeOK := decodeDeclaredResources(wi.DeclaredResources)
	if !decodeOK {
		return nil, NewErr(ErrInternalError, "failed to parse declared_resources")
	}

	type targetLock struct {
		lockType string
		lockKey  string
		probe    lockConflictProbe
	}
	var targets []targetLock
	for _, d := range declared {
		// aihub#342: the read rule used to be spelled out here, and this was the
		// only one of four derivation sites that had it. It now lives in
		// derivedLock so claim, force_takeover and predict rule 1 share it.
		lType, lKey, probe := derivedLockProbe(d, wi.Project)
		if lType != "file_scope" {
			continue // this endpoint handles file_scope only
		}
		if lKey == "" {
			continue
		}
		targets = append(targets, targetLock{lType, lKey, probe})
	}

	acquired := make([]ResourceLock, 0)

	// aihub#343: one op_id for the plain acquisitions of this call. An orphan
	// RECLAIM mints its own (see reclaimOp below) because it is a distinct
	// operation on a lock that belonged to somebody else — its release and its
	// re-acquisition group together, not with the rest of this call.
	//
	// The actor is empty on purpose: this endpoint authenticates an ATTEMPT
	// credential, not a user, so there is no caller identity to stamp that would
	// not be a guess. The attempt id in the payload is the identity that matters.
	alOp := newLockOp(lockCauseAcquireLocks, lockEventActor{})

	for _, t := range targets {
		var ownerAttemptID, ownerActorDisplay, ownerWISlug string
		scanErr := tx.QueryRow(ctx, acquireLocksCollisionSQL, t.lockType, t.probe.Keys, t.probe.LikePattern).
			Scan(&ownerAttemptID, &ownerActorDisplay, &ownerWISlug)

		if scanErr == nil {
			// A live attempt holds this lock.
			if ownerAttemptID != req.AttemptID {
				// Held by a different live attempt — conflict; rollback and error.
				return nil, NewErrDetails(ErrConflictLockTaken,
					fmt.Sprintf("resource %s:%s is already locked", t.lockType, t.lockKey),
					map[string]any{
						"conflict_with": map[string]any{
							"attempt_id":     ownerAttemptID,
							"actor_display":  ownerActorDisplay,
							"work_item_slug": ownerWISlug,
						},
					},
				)
			}
			// Already held by this very attempt — no-op. Deliberately NOT
			// recorded here: already_held is read from the table below
			// (aihub#345), because building it inside this loop is precisely
			// what made it report the re-derived target set instead of what the
			// attempt holds.
			continue
		}
		if !errors.Is(scanErr, pgx.ErrNoRows) {
			return nil, NewErr(ErrInternalError, fmt.Sprintf("failed to check lock collision for %s:%s", t.lockType, t.lockKey))
		}

		// No existing lock — attempt to insert (DO NOTHING on conflict to avoid
		// stealing). aihub#343: acquireLockIfFree emits lock_acquired only when a
		// row really came back from RETURNING, so a DO NOTHING that took nothing
		// records nothing.
		took, execErr := acquireLockIfFree(ctx, tx, t.lockType, t.lockKey,
			req.AttemptID, req.ClaimEpoch, wi.Project, wi.ID, alOp)
		if execErr != nil {
			return nil, dbErrCause(execErr, fmt.Sprintf("failed to acquire lock %s:%s", t.lockType, t.lockKey))
		}
		if !took {
			// DO NOTHING hit an existing row. Re-check who owns it (live attempts only).
			var raceOwnerID, raceActorDisplay, raceWISlug string
			reScanErr := tx.QueryRow(ctx, acquireLocksCollisionSQL, t.lockType, t.probe.Keys, t.probe.LikePattern).
				Scan(&raceOwnerID, &raceActorDisplay, &raceWISlug)
			switch {
			case reScanErr == nil && raceOwnerID == req.AttemptID:
				// We already own it — no-op. Reported by the table read below
				// (aihub#345), not from here.
				continue
			case reScanErr == nil:
				// A different live attempt owns it — conflict.
				return nil, NewErrDetails(ErrConflictLockTaken,
					fmt.Sprintf("resource %s:%s is already locked", t.lockType, t.lockKey),
					map[string]any{
						"conflict_with": map[string]any{
							"attempt_id":     raceOwnerID,
							"actor_display":  raceActorDisplay,
							"work_item_slug": raceWISlug,
						},
					},
				)
			case errors.Is(reScanErr, pgx.ErrNoRows):
				// Row exists but its owner is NOT a live attempt: an orphan lock from a
				// crashed/expired attempt the orphan-sweep (gc.go) has not yet reclaimed.
				// Reclaim it: delete the dead row and insert for this attempt. Matches the
				// orphan-sweep contract (a lock owned by a non-live attempt is free).
				//
				// aihub#343: releaseLocks resolves the DELETED row's own work item
				// rather than this caller's. An orphan row can belong to a
				// DIFFERENT work item, and filing its release under the reclaiming
				// work item's timeline would hide it from the only reader who has a
				// reason to look — the person wondering where their lock went.
				reclaimOp := newLockOp(lockCauseOrphanReclaim, lockEventActor{}).withExtra(map[string]any{
					"reclaimed_by_attempt_id": req.AttemptID,
				})
				if _, delErr := releaseLocks(ctx, tx, lockDeleteByKeySQL, reclaimOp,
					t.lockType, t.lockKey); delErr != nil {
					return nil, dbErr(delErr, fmt.Sprintf("failed to reclaim orphan lock %s:%s", t.lockType, t.lockKey))
				}
				if _, insErr := acquireLockIfFree(ctx, tx, t.lockType, t.lockKey,
					req.AttemptID, req.ClaimEpoch, wi.Project, wi.ID, reclaimOp); insErr != nil {
					return nil, dbErr(insErr, fmt.Sprintf("failed to acquire reclaimed lock %s:%s", t.lockType, t.lockKey))
				}
				acquired = append(acquired, ResourceLock{
					ResourceType:   t.lockType,
					ResourceKey:    t.lockKey,
					OwnerAttemptID: req.AttemptID,
					ClaimEpoch:     req.ClaimEpoch,
				})
				continue
			default:
				return nil, NewErr(ErrInternalError, fmt.Sprintf("failed to re-check lock owner for %s:%s", t.lockType, t.lockKey))
			}
		}
		acquired = append(acquired, ResourceLock{
			ResourceType:   t.lockType,
			ResourceKey:    t.lockKey,
			OwnerAttemptID: req.AttemptID,
			ClaimEpoch:     req.ClaimEpoch,
		})
	}

	// aihub#345: read already_held from the LOCK TABLE, inside the same
	// transaction, rather than from the loop above.
	//
	// The loop only ever visited the targets re-derived from the work item's
	// CURRENT declared_resources, so already_held answered "of the locks I would
	// take right now, which do I have" — while every caller read it as "which
	// locks does this attempt hold". Anything outside that recomputed set was
	// silent though the server kept enforcing it: a declaration since REMOVED
	// (aihub#283's internal/cli/init.go, still 409ing a day after already_held
	// reported none), an intent=read declaration whose lock predates aihub#342,
	// a git_branch or deploy_env lock this endpoint never acquires, or a lock
	// from a client-supplied requested_locks with no declaration behind it.
	//
	// The cost of the old shape was not a confused reviewer. An execute agent
	// read {"acquired":[],"already_held":[]} and published "this attempt holds
	// zero locks" as a correction to a premise that had been right — a tool that
	// misleads agents, not just people. Since there is no other way to ask (the
	// only cross-checked route was pf_predict_conflicts with intent=write and no
	// work_item_id), the field has to be complete or it cannot be used at all.
	//
	// Excluding `acquired` keeps the two arrays a partition, which is how repeat
	// calls already read: what moved is in acquired, what was already there is
	// in already_held.
	alreadyHeld := make([]ResourceLock, 0)
	heldRows, heldErr := tx.Query(ctx, `
		SELECT resource_type, resource_key, owner_attempt_id, claim_epoch
		FROM resource_locks WHERE owner_attempt_id=$1
		ORDER BY resource_type, resource_key`, req.AttemptID)
	if heldErr != nil {
		if aerr := retryConflictErr(heldErr, "failed to list held locks"); aerr != nil { // aihub#334
			return nil, aerr
		}
		return nil, NewErr(ErrInternalError, "failed to list held locks")
	}
	justAcquired := make(map[string]bool, len(acquired))
	for _, l := range acquired {
		justAcquired[l.ResourceType+":"+l.ResourceKey] = true
	}
	for heldRows.Next() {
		var l ResourceLock
		if scanErr := heldRows.Scan(&l.ResourceType, &l.ResourceKey, &l.OwnerAttemptID, &l.ClaimEpoch); scanErr != nil {
			heldRows.Close()
			if aerr := retryConflictErr(scanErr, "failed to scan held locks"); aerr != nil { // aihub#334
				return nil, aerr
			}
			return nil, NewErr(ErrInternalError, "failed to scan held locks")
		}
		if justAcquired[l.ResourceType+":"+l.ResourceKey] {
			continue
		}
		alreadyHeld = append(alreadyHeld, l)
	}
	heldRows.Close()
	// aihub#334: a lazily-streamed result set's error has no other exit, and
	// this transaction is SERIALIZABLE so 40001 is reachable here. Without this
	// the loop just looks empty — which is the exact failure this whole change
	// exists to remove, arriving by a different route.
	if err := heldRows.Err(); err != nil {
		if aerr := retryConflictErr(err, "failed to list held locks"); aerr != nil {
			return nil, aerr
		}
		return nil, NewErr(ErrInternalError, "failed to list held locks")
	}

	if err := tx.Commit(ctx); err != nil {
		if aerr := retryConflictErr(err, "failed to commit acquire_locks"); aerr != nil { // aihub#334
			return nil, aerr
		}
		return nil, NewErr(ErrInternalError, "failed to commit acquire_locks")
	}
	return &AcquireLocksResponse{Acquired: acquired, AlreadyHeld: alreadyHeld}, nil
}
