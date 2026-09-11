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
	ID                 string    `json:"id"`
	WorkItemID         string    `json:"work_item_id"`
	Status             string    `json:"status"`
	ClaimEpoch         int64     `json:"claim_epoch"`
	IdempotencyKey     string    `json:"idempotency_key"`
	LastActiveAt       time.Time `json:"last_active_at"`
	ActorUserID        string    `json:"actor_user_id"`
	APIKeyID           string    `json:"api_key_id"`
	ActorDisplay       string    `json:"actor_display"`
	MachineID          string    `json:"machine_id"`
	SessionSecretHash  string    `json:"session_secret_hash"`
	ParentAttemptID    *string   `json:"parent_attempt_id"`
	PhaseConfigVersion *int      `json:"phase_config_version"` // kept as audit field; always NULL since scenario_phase_configs was removed (aihub#38)
	// ⚠️ `prepared_workspace` is a DEAD COLUMN, kept deliberately (aihub#487,
	// from aihub#416 spec Q-6, owner-ruled 2026-09-08 in
	// aihub#416.attrs.owner_ruling_2026_09_08_q2_q6). The same plan B as
	// aihub#387 and aihub#394, each of which withdrew a dead parameter from a
	// tool schema instead of implementing it. The closest match is aihub#395
	// part 2: it withdrew `base_branch` from the published declared_resources
	// schema and KEPT its struct field, which is the shape followed here. Only
	// #395 part 2 did exactly that — #387's `non_conflicting` had no struct
	// field, and #394's `ClaimRequest.Mode` was later deleted outright by
	// aihub#424.
	//
	// Measured on 6cd8229, the complete set of non-test occurrences of the column
	// name is this one line. Both `INSERT INTO run_attempts` column lists
	// (FnClaimWorkItem and the force-takeover path) are explicit and omit it, no
	// UPDATE sets it and no SELECT reads it. The archived v0 Python writer omits
	// it from its own INSERT as well, so no generation of this codebase ever
	// wrote it — though "every row is NULL" stays an inference from the code,
	// not a read of the database.
	// It is on no wire surface in either direction: no MCP
	// tool InputSchema publishes it, no contract card mentions it, and
	// ClaimRequest does not bind it — POSTing it to /v1/work_items/:id/claim
	// answers 200 with the key dropped by encoding/json, which is the generic
	// fate of ANY unknown key rather than anything specific to this field.
	//
	// So aihub#487 withdrew nothing: there was nothing published to withdraw, and
	// the precedent's one-line schema deletion had no target. The wi's actual
	// deliverable is the gate in prepared_workspace_dead_column_test.go, which
	// asserts that write, bind and read surface stays empty.
	//
	// The field STAYS, per the precedent. It is the declared mirror of a live DDL
	// column (0004_run_attempts.sql) and dropping the column is a migration this
	// wi deliberately does not write. Two things a reviver should know: the tag
	// has no omitempty, so marshalling this struct would emit
	// `"prepared_workspace": null`; and RunAttempt as a whole currently has no
	// readers in non-test code, so nothing marshals it today.
	PreparedWorkspace *json.RawMessage `json:"prepared_workspace"`
	StartedAt         time.Time        `json:"started_at"`
	EndedAt           *time.Time       `json:"ended_at"`
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
	// ⚠️ There is deliberately no `mode` here (aihub#424). It was bound and
	// defaulted for months after aihub#394 withdrew it from pf_claim_work_item's
	// schema, which left a field no MCP caller could set and whose every read was
	// an audit value reporting the word back to whoever sent it. Re-adding it
	// fails TestClaimRequestBindsNothingUnreachable, and the capability it seemed
	// to offer does not need it: which branch a claim attaches to is decided by
	// what exists in the clone (resolveClaimBranch), and step state lives in
	// wi_step_state keyed by work item, so every re-claim already sees it.
	ForceOver   bool    `json:"force_takeover"`
	ScenarioRef *string `json:"scenario_ref,omitempty"` // git SHA of local scenario clone at claim time
	// ⚠️ There is deliberately no `task_branches` here either (aihub#416), and
	// it went for the same reason as `mode` above rather than by tidying.
	//
	// aihub#356 added it, and EffectiveDeclaredResource beside it, so the
	// git_branch lock a repo declaration derived would be keyed on the branch the
	// claiming client was really about to check out instead of on
	// declared_resources[].task_branch. That derivation is retired
	// (resourceToLock): a repo entry takes no lock under any intent. So the field
	// had exactly one reader, that reader computed exactly one thing, and the
	// thing no longer exists — binding it would leave a request field an MCP
	// caller cannot set and the server cannot act on, which is the state
	// aihub#424 removed `mode` from and TestClaimRequestBindsNothingUnreachable
	// gates against.
	//
	// The CLIENT half went with it (internal/mcp/tools_lifecycle.go): the claim
	// no longer spends a GetWorkItem round-trip predicting branch names. What
	// records where an attempt started is RepoPins below, taken from the
	// worktrees after they exist rather than predicted before they do.

	// ⚠️ And deliberately no `repo_pins` here, though run_attempts HAS that
	// column (migration 0037). The claim cannot carry them: the pins are read out
	// of the worktrees, and the MCP handler creates those AFTER the claim returns
	// — it must, because a claim can be refused (409 on an already-claimed work
	// item) and building worktrees for a refused claim would leave litter on
	// disk. Predicting them before the claim is the aihub#356 mistake this wi
	// just deleted: a value guessed at claim time and then quietly wrong.
	//
	// They are recorded by a second call instead, RecordRepoPinsRequest /
	// FnRecordRepoPins below, which the same MCP tool makes as soon as the
	// worktrees exist. Binding a field here that no caller could fill would be
	// the `mode` shape above.
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

// defaultRequiresHumanSession is what a claim falls back to when the work item row carries
// no classification at all (requires_human_session IS NULL).
//
// It is a CONSTANT, not a lookup, and having a name is the whole point of it. The table that
// once mapped wi_type -> requires_human_session (scenario_phase_configs) has been removed; the
// per-wi_type defaults now live in the scenario repo, which only the client can read — the
// create path in work_items.go says the same thing. The server therefore has nothing to
// consult here, so it fails safe: when nobody classified the work item, assume a human has to
// look at it.
//
// Do not "improve" this into a wi_type switch without first putting the defaults somewhere the
// server can actually see. Guessing a per-type default here would silently disagree with the
// scenario repo, which is the file the client already obeyed.
const defaultRequiresHumanSession = true

// classificationSourceServerDefault labels a wi_classification_resolved event as having come
// from defaultRequiresHumanSession — which, on the only branch that emits the event, is the
// one and only thing that can produce the value.
const classificationSourceServerDefault = "server_default"

// classificationResolvedEventPayload builds the MARSHALLED wi_classification_resolved payload.
//
// It deliberately does NOT name wi_type. The payload used to, and that was the defect fixed by
// aihub#359: naming the wi_type made the event read as though the type had been looked up and
// had driven the outcome. It never was. The branch that emits this event only runs when the wi
// row is NULL, and on that branch the value is a fixed constant. Naming a field that took no
// part in a decision sends the next reader hunting for a classification table that has not
// existed for months. To see that for yourself, rather than trusting a number written here:
//
//	pf_read_events(project="aihub", types=["wi_classification_resolved"],
//	               limit=200, since="2026-01-01T00:00:00Z")
//
// Every row it returns should carry requires_human_session=true, because a constant is the only
// thing that has ever produced that field. A `false` in there would mean this analysis is wrong.
//
// "source" is here so the event still says where the value came from, without implying a
// derivation that did not happen.
//
// WHY THIS RETURNS BYTES AND NOT A map. A map-returning builder can be wrapped at the call site
// — json.Marshal(annotate(build(v), *wi.WIType)) — which puts wi_type straight back into the
// stored event while a test that inspects only the builder's return value stays green. That is
// not hypothetical: it was found by review of the first cut of aihub#359, and all five gates
// passed with the defect fully reintroduced. Marshalling here makes the bytes this returns the
// same bytes that reach agent_events, so the compiled test asserts on the real payload; the
// structural gate separately pins the call site to a bare call to this function, so the
// wrapping trick cannot come back. Do not "simplify" this back into returning a map.
//
// The json.Marshal error is discarded because a map of one bool and one string cannot fail to
// marshal — there is no dynamic value in it, unlike the *string this used to dereference.
func classificationResolvedEventPayload(requiresHumanSession bool) []byte {
	b, _ := json.Marshal(map[string]any{
		"requires_human_session": requiresHumanSession,
		"source":                 classificationSourceServerDefault,
	})
	return b
}

// deriveClaimLocks writes the locks a claim carrying req will insert into
// req.RequestedLocks, and returns the probe that decides whether each one
// collides, paired index-for-index with that slice.
//
// It is the WHOLE of the claim's lock derivation: the client-supplied slice is
// trusted verbatim with a plain equality probe, and only an empty one is
// replaced by entries derived from the stored declared_resources.
//
// ⚠️ IT WRITES BACK INTO req INSTEAD OF RETURNING THE SLICE, and that is a
// correctness property rather than a style choice. It used to return the locks
// and leave the caller to run `req.RequestedLocks = locks` on the next line.
// aihub#356 review mutant M3 deleted only that assignment — keeping the call
// text itself intact — and the claim then derived NO locks at all: no
// git_branch, no file_scope. Measured on 71bade2 with the assignment removed:
//
//	$ go build ./...            -> exit 0
//	$ go vet ./...              -> exit 0
//	$ go test ./... -count=1    -> exit 0
//	$ golangci-lint run ./...   -> 0 issues
//
// The reason nothing caught it is that the only guard on the call site,
// TestRequestedLocksValidatedBeforeServerSideDerivation, scans FnClaimWorkItem's
// SOURCE for the call text, and a call whose result is discarded is still a
// call. Writing into req removes the class rather than the instance: there is no
// second line left to lose, and the probes returned here are referenced further
// down FnClaimWorkItem, so dropping the call is a compile error and not a silent
// no-op. Do not "clean this up" back into returning the slice.
//
// ⚠️ IT IS A SEPARATE FUNCTION SO THAT THE DERIVATION CAN BE TESTED, and that is
// not a stylistic preference either. Every transform below —
// EffectiveDeclaredResource, derivedLockProbe, the empty-key skip — has unit
// tests, but while this loop lived inside FnClaimWorkItem the only way to reach
// the loop ITSELF was through a *pgxpool.Pool, so a line could be deleted from
// here and every test in the DB-FREE unit step would stay green; aihub#356
// review found exactly that, on the EffectiveDeclaredResource call. Anything
// added to this function needs a case in TestDeriveClaimLocks, which needs no
// database.
//
// ⚠️ THAT LAST CLAIM IS ABOUT THE DB-FREE STEP ONLY. An earlier revision of this
// comment justified the extraction with "an AIHUB_TEST_DB-gated test runs in
// neither CI nor a default local run". That is false, and inverted — this repo
// guarantees the opposite. Measured here:
//
//	$ grep -cE '^[[:space:]]*AIHUB_TEST_DB:' .github/workflows/ci.yml
//	40
//	$ grep -cvE '^[[:space:]]*(#|$)' internal/citest/dbtestcov/gated_tests.txt
//	198
//
// (198 is the ENTRY count, and it is what the dbtestcov gate itself reports.
// `wc -l` on that file says 221 — 23 of those lines are its comment header, so
// do not quote the line count as an entry count.)
//
// ci.yml:15-27 starts a live pgvector/pgvector:pg18 service for the whole job,
// 40 `AIHUB_TEST_DB:` assignments point steps at it (the gate's own run reports
// 55 `go test` invocations under them), and the "aihub#303 DB-test coverage
// gate" at ci.yml:614 exists precisely to FAIL when a DB-gated function is named
// by no step's -run. Adding a DB test here would have cost one gated_tests.txt
// entry and one -run step — this repo's normal process, already carried out for
// all 198 of them. That is a cost, not a barrier, and it does not support "no
// test could reach it".
//
// The real reason a DB test would have bought nothing is that no DB-gated suite
// in the repo sets task_branches, so every one of them exercises only the
// fallback where EffectiveDeclaredResource returns its argument unchanged:
//
//	$ grep -rln AIHUB_TEST_DB --include='*_test.go' . | wc -l
//	69
//	$ grep -rln AIHUB_TEST_DB --include='*_test.go' . \
//	    | xargs grep -nE 'TaskBranches|task_branches'
//	(no output — zero hits across all 69 gated files)
//
// So the substitution case had to be written from scratch either way, and
// TestDeriveClaimLocks runs in the DB-free unit step — strictly MORE coverage
// than a DB-only step would have given it, not less.
//
// FnClaimWorkItem calling it is separately pinned by
// TestRequestedLocksValidatedBeforeServerSideDerivation — but that guard reads
// source text, so see the write-back ⚠️ above for what it cannot see.
func deriveClaimLocks(req *ClaimRequest, declaredResources json.RawMessage, project string) []lockConflictProbe {
	locks := req.RequestedLocks
	probes := make([]lockConflictProbe, 0, len(locks))
	for _, l := range locks {
		probes = append(probes, exactProbe(l.ResourceKey))
	}
	if len(locks) > 0 || len(declaredResources) == 0 {
		// Nothing was derived, so req.RequestedLocks already IS locks and needs
		// no write-back: either the client supplied it verbatim or it is empty
		// and stays empty.
		return probes
	}
	// aihub#261: unmarshalDeclaredResources replaces the local anonymous
	// struct this site used to declare. That struct's hand-written field list
	// was the failure mode aihub#342 called the quietest form of this class —
	// a field missing from the list never reaches the mapper and reads as a
	// zero value — and the `repo` field added here is exactly such a field.
	for _, d := range unmarshalDeclaredResources(declaredResources) {
		// aihub#416: the `d = req.EffectiveDeclaredResource(d)` substitution
		// that used to open this loop is gone with the git_branch derivation it
		// served — see ClaimRequest for why the whole task_branches path went
		// rather than being left inert.
		//
		// derivedLockProbe, not resourceToLock (aihub#342): an intent=read
		// declaration takes no write lock. This line is the reported
		// instance — claim took a file_scope lock for a read-only path
		// and then 409'd the next claimer, while pf_predict_conflicts
		// reported the identical input as `info`, so the pre-claim gate
		// had no predictive value at all.
		lockType, lockKey, probe := derivedLockProbe(d, project)
		// aihub#238: an empty key is possible from bad stored data (a
		// `service`/`path` entry with no uri). Never insert it — the row is
		// meaningless as a lock and would collide with every other empty-key
		// row of the same type. Skipping keeps the wi claimable; the entry is
		// reported via unrecognizedResources below rather than dropped silently.
		if lockType == "" || lockKey == "" {
			continue
		}
		locks = append(locks, ResourceLockReq{
			ResourceType: lockType, ResourceKey: lockKey,
		})
		probes = append(probes, probe)
	}
	// The write-back the ⚠️ above is about, and the only one: the early return
	// covers the no-derivation cases. No aliasing hazard — control only reaches
	// this loop when len(locks)==0, so every append below grows a fresh backing
	// array rather than writing into one the caller still holds.
	req.RequestedLocks = locks
	return probes
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
	// aihub#522: ErrNoRows means "no idempotent hit — take the fresh path", and
	// it is the ONLY error that means that. Every other error used to take the
	// fresh path too, inside a transaction the failed statement had already
	// aborted, so the claim died at a later statement as an unclassifiable 500
	// with the SQLSTATE gone (the aihub#334 shape). The UNIQUE
	// (work_item_id, idempotency_key) index kept this from double-claiming; it
	// could not keep the error honest.
	if idemErr != nil && !errors.Is(idemErr, pgx.ErrNoRows) {
		return nil, dbErrCause(idemErr, "failed to check claim idempotency")
	}
	if idemErr == nil {
		// Re-query the locks held by the existing attempt.
		existingLocks := []ResourceLock{}
		lockRows, lockQErr := tx.Query(ctx,
			`SELECT resource_type, resource_key, owner_attempt_id, claim_epoch
			 FROM resource_locks WHERE owner_attempt_id=$1`, existingAttemptID)
		if lockQErr != nil {
			// aihub#522: the send-time twin of the rows.Err() branch below. A
			// swallowed failure here answered the idempotent re-claim with an
			// empty AcquiredLocks slice — the exact phantom the G3 re-query
			// above exists to remove — inside a transaction the failure had
			// already doomed.
			return nil, dbErrCause(lockQErr, "failed to load locks for idempotent claim")
		}
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

		// Recompute step_recovery_hint identically to the fresh path.
		idemHint := "clean"
		var idemStepStatus string
		var idemStepStartedAt *time.Time
		stepErr := tx.QueryRow(ctx, `
			SELECT current_step_status, step_started_at FROM wi_step_state WHERE work_item_id=$1`, wi.ID,
		).Scan(&idemStepStatus, &idemStepStartedAt)
		// aihub#492 / aihub#522: the idempotent-path twin of the priorStepErr
		// read on the fresh path below. The hint stays best-effort — ErrNoRows
		// and any ordinary failure both mean "no hint" and the claim goes on —
		// but a class-40 rollback has already killed this transaction, so
		// reading past it only trades the retryable 409 for an unclassifiable
		// 500 at tx.Commit.
		if aerr := retryConflictErr(stepErr, "failed to read step state for idempotent claim"); aerr != nil {
			return nil, aerr
		}
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
		// aihub#522: this read GATES the takeover-versus-409 decision, and every
		// error used to skip the whole block — a foreign holder's claim check
		// silently not run, inside a transaction the failed statement had
		// already aborted. ErrNoRows alone keeps the skip: current_attempt_id
		// is denormalized with no FK (migration 0002), so a dangling pointer
		// must claim like "not running" rather than brick the work item.
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return nil, dbErrCause(err, "failed to load the current attempt for claim")
		}
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
	lockProbes := deriveClaimLocks(req, wi.DeclaredResources, wi.Project)

	// aihub#238: this path reads ALREADY-STORED declared_resources, so it cannot
	// reject a mistyped entry without making historical work items unclaimable
	// (~14% of entries in aihub's own recent wis are mistyped). Report instead of
	// failing, so the claimer at least learns that something they declared is
	// holding no lock. New bad data is prevented upstream, by the create/update
	// validation in work_items.go.
	unrecognizedResources := UnrecognizedDeclaredResources(wi.DeclaredResources)

	// Check lock conflicts against OTHER work items.
	//
	// aihub#393: this runs on a takeover too. It used to be guarded by
	// `&& !isTakeover`, which bought nothing except silence: the probe already
	// excludes this work item's own attempts (aihub#207, see
	// probeForeignLockHolders), so skipping it did not help a takeover reclaim
	// its own locks — it only stopped a foreign holder being reported, and the
	// upsert loop below then rewrote that holder's row and returned success.
	// A lock held by a live attempt of a DIFFERENT work item is a conflict
	// whether or not this claim is a takeover; force_takeover takes over the
	// WORK ITEM, not other people's locks.
	if aihubErr := probeForeignLockHolders(ctx, tx, wi.ID, req.RequestedLocks, lockProbes); aihubErr != nil {
		return nil, aihubErr
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
		// aihub#492: the emission stays best-effort, but a class-40 rollback is
		// not a lost event — it is a dead transaction. See bestEffortExec.
		if aerr := bestEffortExec(ctx, tx, "failed to emit attempt_superseded event", `
			INSERT INTO agent_events (id, work_item_id, actor_user_id, actor_display, event_type, payload, project)
			VALUES ($1, $2, $3, $4, 'attempt_superseded', $5, $6)`,
			supEvtID, wi.ID, callerUserID, callerDisplay, supPayload, wi.Project,
		); aerr != nil {
			return nil, aerr
		}
	}

	// Insert resource_locks for requested locks.
	//
	// aihub#343: through acquireLockUpsert, which emits one lock_acquired per row
	// AND a lock_released for any owner the upsert displaced. The displacement
	// half matters: ON CONFLICT DO UPDATE can rewrite an un-swept orphan row's
	// owner, and recording only the acquisition would leave a reader following
	// the previous owner with an unmatched lock_acquired — reading as "still
	// held" for a lock that changed hands.
	//
	// `is_resume` used to sit beside is_takeover here, computed as
	// `req.Mode == "resume"`. It went with the field (aihub#424): after aihub#394
	// no MCP caller could set `mode`, so the value was a constant false — an audit
	// line asserting "this claim was not a resume" about every claim, including
	// the resumes. is_takeover stays because it is derived from server state.
	claimOp := newLockOp(lockCauseClaim, lockActor).withExtra(map[string]any{
		"is_takeover": isTakeover,
	})
	acquiredLocks := make([]ResourceLock, 0, len(req.RequestedLocks))
	for _, l := range req.RequestedLocks {
		got, upErr := acquireLockUpsert(ctx, tx, l.ResourceType, l.ResourceKey,
			newAttemptID, newEpoch, wi.Project, wi.ID, claimOp)
		if upErr != nil {
			// aihub#393: the upsert declines to displace a live foreign holder.
			// The probe above normally catches that first; this reaches the same
			// 409 for anything the probe's key set does not cover, so the two
			// cannot disagree about the outcome.
			if aihubErr := lockTakenErrFor(upErr); aihubErr != nil {
				return nil, aihubErr
			}
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
	// aihub#492: priorStepErr is read, not returned — pgx.ErrNoRows is the
	// normal "no prior step state" answer and the guard at the read site below
	// treats any error as "no hint". A class-40 rollback is not that. Postgres
	// has already aborted this transaction, so nothing below can commit and
	// tx.Commit reports pgx.ErrTxCommitRollback — a plain sentinel, not a
	// *pgconn.PgError, which retryConflictErr at the commit site cannot
	// classify. Reading past it therefore replaces a classified retryable 409
	// with an unclassifiable 500. Same reasoning, and the same fix, as the
	// unblockDependentWI site in FnCompleteAttempt (aihub#334).
	if aerr := retryConflictErr(priorStepErr, "failed to read prior step state"); aerr != nil {
		return nil, aerr
	}

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
		// aihub#492: "non-fatal" holds only for errors that leave the
		// transaction usable. A class-40 rollback does not — see the
		// prior-step read above for why continuing past one downgrades a
		// retryable 409 into a 500 at tx.Commit.
		if aerr := retryConflictErr(err, "failed to upsert wi_step_state"); aerr != nil {
			return nil, aerr
		}
		// Non-fatal but log: agent will lack scenario_ref and fall back to default behavior.
		fmt.Fprintf(os.Stderr, "claim: wi_step_state upsert failed (scenario_ref not written): %v\n", err)
	}

	// C-R9-12: if the wi row carries no classification, fall back to the server default and
	// write it back. A row that DOES carry one is simply left alone — the client set it from
	// the scenario repo, which is the only copy of the per-wi_type defaults, so there is
	// nothing here to cross-check it against and nothing to disagree with.
	//
	// There used to be an `else if *wi.RequiresHumanSession != resolvedRHS` here that returned
	// 409 REQUIRES_HUMAN_SESSION_MISMATCH. It could never fire: reaching the else meant the
	// value was non-nil, and the value it was compared against had just been assigned FROM that
	// same field, so the condition was `x != x`. Its error text ("phase config says ...") named
	// scenario_phase_configs, which had already been removed. Removed by aihub#359 — do not
	// reinstate a mismatch check unless the server gains a second, independent source for the
	// expected value; comparing the row against itself is not a check.
	if wi.RequiresHumanSession == nil {
		resolvedRHS := defaultRequiresHumanSession
		_, err = tx.Exec(ctx, `
			UPDATE work_items SET requires_human_session=$1 WHERE id=$2`,
			resolvedRHS, wi.ID,
		)
		if err != nil {
			return nil, dbErr(err, "failed to set requires_human_session")
		}
		// Emit wi_classification_resolved event. See classificationResolvedEventPayload: the
		// payload names its source and NOT the wi_type, because the wi_type did not
		// participate in the decision. Keep this a BARE call — wrapping it in anything that
		// can add a key defeats the compiled gate, which asserts on that function's bytes.
		evtID := NewID("evt")
		evtPayload := classificationResolvedEventPayload(resolvedRHS)
		// aihub#492: see bestEffortExec. evtPayload is still the bare builder's
		// bytes and nothing here marshals anything, so the honesty gate in
		// rhs_classification_honesty_test.go is untouched.
		if aerr := bestEffortExec(ctx, tx, "failed to emit wi_classification_resolved event", `
			INSERT INTO agent_events (id, work_item_id, actor_user_id, actor_display, event_type, payload, project)
			VALUES ($1, $2, $3, $4, 'wi_classification_resolved', $5, $6)`,
			evtID, wi.ID, callerUserID, callerDisplay, evtPayload, wi.Project,
		); aerr != nil {
			return nil, aerr
		}
		wi.RequiresHumanSession = &resolvedRHS
	}

	// Emit attempt_started event
	evtID := NewID("evt")
	// No `is_resume` — see the claimOp comment above. If a later change wants the
	// distinction back on the timeline it has to be DERIVED here (the work item's
	// status before this claim is the obvious source), not taken from a word the
	// caller sends about itself.
	evtPayload, _ := json.Marshal(map[string]any{
		"machine_id":    req.SessionInfo.MachineID,
		"actor_display": callerDisplay,
		"is_takeover":   isTakeover,
		"claim_epoch":   newEpoch,
	})
	// aihub#492: this one fires on EVERY claim, so it was the widest of the
	// discard sites — see bestEffortExec.
	if aerr := bestEffortExec(ctx, tx, "failed to emit attempt_started event", `
		INSERT INTO agent_events (id, work_item_id, run_attempt_id, actor_user_id, actor_display, api_key_id, event_type, payload, project)
		VALUES ($1, $2, $3, $4, $5, $6, 'attempt_started', $7, $8)`,
		evtID, wi.ID, newAttemptID, callerUserID, callerDisplay, callerAPIKeyID, evtPayload, wi.Project,
	); aerr != nil {
		return nil, aerr
	}

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

// repoPinsJSON renders a claim's repo pins for the JSONB column, or nil when
// there are none (aihub#416).
//
// 🔴 nil, NOT `{}`, and the difference is load-bearing rather than tidy. NULL
// means "this claim recorded no starting commit for anything" — the honest state
// for a claim that built no worktree, and for every row written before migration
// 0037. An empty object would say "we looked, and there were zero repos", which
// is a different claim and a false one on both of those paths. A later reader
// asking "does this conclusion have provenance" has to be able to tell them
// apart.
//
// A marshal failure degrades to NULL rather than failing the claim. The value is
// provenance; losing it must never cost somebody a claim (the same posture the
// wi_step_state upsert below takes for scenario_ref). map[string]string cannot
// in fact fail to marshal, which is why this is a fallback and not a branch with
// a test behind it.
func repoPinsJSON(pins map[string]string) []byte {
	if len(pins) == 0 {
		return nil
	}
	b, err := json.Marshal(pins)
	if err != nil {
		return nil
	}
	return b
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

	// aihub#452: pause_reason is read on status=paused and nowhere else, so a
	// reason supplied with any other status is refused rather than recorded.
	//
	// The capability being refused here is "record a reason on a wrapped or
	// failed attempt", and it was decided against rather than narrowed by
	// accident. Three measurements, all on this tree: the column's only reader
	// is GetReadyQueue's paused[] segment, whose query filters
	// `wi.status = 'paused'`; migration 0027 scopes the column to
	// "complete_attempt(status=paused) ... so the ready-queue paused segment can
	// surface why an attempt was paused"; and the MCP tool publishes `note` as
	// the channel that records on every status. So the value written on a
	// terminal completion had no reader at all, which makes accepting it a
	// promise the store cannot keep.
	//
	// The refusal is deliberately NARROW, and both halves of that matter.
	// (a) It fires only on a NON-EMPTY reason: an absent or empty one states
	// nothing, so there is nothing to misplace and nothing to warn about —
	// widening it there would reject requests that carry no ambiguity at all.
	// (b) It is not the converse rule: pause_reason stays optional on paused,
	// because "paused, reason not given" is a legitimate call and the whole
	// point of keeping nil distinguishable from "".
	//
	// Refused BEFORE BeginTx, next to the status check, so the request is
	// rejected on its own contents without a database round-trip — which is
	// also what lets the gate for this exercise the real function with no DB.
	if req.Status != "paused" && req.PauseReason != nil && *req.PauseReason != "" {
		return NewErr(ErrBadRequest, fmt.Sprintf(
			"pause_reason is read only when status=\"paused\", but status=%q was sent with one; "+
				"drop pause_reason or use note, which is recorded on every status", req.Status))
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
	// aihub#545: stepErr is read, not returned — pgx.ErrNoRows is the normal
	// "this work item has no step state" answer and the guard below treats any
	// error as "no step in progress". A class-40 rollback is not that: this
	// transaction is SERIALIZABLE, so a 40001 here has already aborted it, and
	// reading past one replaces the classified retryable 409 with a 25P02-backed
	// 500 at the very next statement. Same read, same reasoning and same fix as
	// aihub#492's prior-step read on the claim path and aihub#497's on the
	// takeover path — this is their sibling on the complete-attempt path.
	if aerr := retryConflictErr(stepErr, "failed to read step state for complete_attempt"); aerr != nil {
		return aerr
	}
	if stepErr == nil && stepStatus == "in_progress" {
		if req.Status == "paused" || req.ForceTerminateStep {
			if aihubErr := fnForceTerminateStep(ctx, tx, wiID, req.AttemptID, stepAttempt); aihubErr != nil {
				return aihubErr
			}
		} else {
			return NewErr(ErrConflictStepInProgress, "a step is still in_progress; set force_terminate_step=true or update step first")
		}
	}

	// Set run_attempt status. pause_reason is written only on status=paused
	// (aihub#452). The guard above has already refused a non-empty reason on any
	// other status, so what this normalisation still catches is the residue: an
	// EMPTY-string pointer, which the guard deliberately lets through and which
	// would otherwise land as '' rather than NULL — turning "this attempt was not
	// paused" into "paused, reason not given" on every terminal completion that
	// bothered to set the field. That distinction is the one the column exists to
	// carry, so it is preserved here rather than left to the caller.
	pauseReason := req.PauseReason
	if req.Status != "paused" {
		pauseReason = nil
	}
	_, err = tx.Exec(ctx, `
		UPDATE run_attempts SET status=$1, ended_at=clock_timestamp(), pause_reason=$2 WHERE id=$3`,
		req.Status, pauseReason, req.AttemptID,
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
	// Same source as the column write, so the event and the row can never
	// disagree about whether a reason was recorded (aihub#452).
	if pauseReason != nil {
		evtPayloadMap["pause_reason"] = *pauseReason
	}
	evtPayload, _ := json.Marshal(evtPayloadMap)
	// aihub#492: see bestEffortExec. This is the same defect the
	// unblockDependentWI call below already fixed under aihub#334, on the
	// statement immediately before it.
	if aerr := bestEffortExec(ctx, tx, "failed to emit attempt_completed event", `
		INSERT INTO agent_events (id, work_item_id, run_attempt_id, event_type, payload, project)
		VALUES ($1, $2, $3, 'attempt_completed', $4, $5)`,
		evtID, wi.ID, req.AttemptID, evtPayload, wi.Project,
	); aerr != nil {
		return aerr
	}

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
		// aihub#492: see bestEffortExec.
		if aerr := bestEffortExec(ctx, tx, "failed to set methodology memory expiry", `
			UPDATE memories SET expires_at = clock_timestamp() + interval '90 days'
			WHERE work_item_id = $1 AND type LIKE 'methodology.%' AND expires_at IS NULL`,
			wi.ID); aerr != nil {
			return aerr
		}
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
	stepErr := tx.QueryRow(ctx, `SELECT current_step FROM wi_step_state WHERE work_item_id=$1`, wiID).Scan(&currentStep)
	// aihub#545: stepErr is read, not returned — pgx.ErrNoRows and a NULL
	// current_step both keep meaning "no step to terminate", exactly as before.
	// A class-40 rollback is not that: Postgres has already aborted the
	// enclosing transaction, so answering nil here tells the caller "nothing to
	// do, carry on", and carrying on is running the rest of a SERIALIZABLE
	// complete-attempt (or takeover) inside a dead transaction until some later
	// statement fails with 25P02 — unclassifiable, reported as a 500. Worse,
	// the swallow made aihub#497's guard arm at the FnForceTakeover call site
	// structurally unreachable for this hop: that arm matches
	// ErrConflictSerializationFailure on this function's RETURN, and a 40001
	// discarded at this Scan never becomes a return value at all. Same read,
	// same reasoning and same fix as aihub#492's prior-step read on the claim
	// path and aihub#497's step-state read on the takeover path.
	if aerr := retryConflictErr(stepErr, "failed to read current step for force_terminate"); aerr != nil {
		return aerr
	}

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
	// aihub#492: this runs on FnCompleteAttempt's SERIALIZABLE transaction —
	// see bestEffortExec.
	if aerr := bestEffortExec(ctx, tx, "failed to emit step_failed event", `
		INSERT INTO agent_events (id, work_item_id, run_attempt_id, event_type, payload, project)
		VALUES ($1, $2, $3, 'step_failed', $4,
		        (SELECT project FROM work_items WHERE id=$2))`,
		evtID, wiID, attemptID, payload); aerr != nil {
		return aerr
	}

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
	// UnrecognizedResources is the SAME report ClaimResponse carries, under the
	// same key and produced by the same UnrecognizedDeclaredResources call
	// (aihub#509, closing the half of aihub#411 T2-12 that aihub#416 left open).
	//
	// It is not a second convention: a takeover re-derives this work item's locks
	// from the same stored declared_resources a claim does, so it skips exactly
	// the same entries, and it used to skip them in silence because this struct
	// had no field for it. The old defence was that a takeover "is always
	// followed by a fresh claim, which does report" — true of the polyforge flow
	// and false of the HTTP surface, where POST /force_takeover is its own
	// endpoint and returns a running attempt that already holds (or does not
	// hold) the locks. A caller reading this response is entitled to the same
	// warning the claiming one gets.
	//
	// Empty on a healthy work item, and `omitempty`, so nothing appears in the
	// ordinary case — the same absence-means-nothing-adjusted convention
	// ClaimResponse uses.
	UnrecognizedResources []string `json:"unrecognized_resources,omitempty"`
	OK                    bool     `json:"ok"`
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

	// aihub#393: refuse before mutating anything if one of the locks this
	// takeover would re-derive is held by a live attempt of ANOTHER work item.
	//
	// This function had no conflict probe at all and discarded
	// acquireLockUpsert's error, so it displaced foreign holders through a second
	// entry point — the same defect as the claim path's `!isTakeover`, reachable
	// by a different tool (pf_force_takeover).
	//
	// It has to come BEFORE the supersede/release below. Those run first in the
	// original order, so a refusal discovered at the upsert loop would only be
	// safe because the transaction rolls back; probing here means the refusal is
	// not relying on that, and the caller gets the holder's identity.
	//
	// derivedLockProbe, not derivedLock, for the same reason as the claim path:
	// since aihub#261 the key a declaration writes and the keys that block it are
	// different sets. The locks are derived from the SAME declaredRes slice the
	// re-insert loop below uses, so the two cannot check different paths.
	declaredRes := unmarshalDeclaredResources(wi.DeclaredResources)

	// aihub#509: computed from the RAW payload, not from declaredRes, for the
	// same reason FnClaimWorkItem does it that way — unmarshalDeclaredResources
	// is tolerant by design and turns an unparseable payload into an empty
	// slice, so a report derived from the decoded slice would say "nothing wrong"
	// about exactly the payload that is most wrong.
	ftUnrecognized := UnrecognizedDeclaredResources(wi.DeclaredResources)

	ftLocks := make([]ResourceLockReq, 0, len(declaredRes))
	ftProbes := make([]lockConflictProbe, 0, len(declaredRes))
	for _, res := range declaredRes {
		lockType, lockKey, probe := derivedLockProbe(res, wi.Project)
		if lockType == "" {
			continue
		}
		ftLocks = append(ftLocks, ResourceLockReq{ResourceType: lockType, ResourceKey: lockKey})
		ftProbes = append(ftProbes, probe)
	}
	if aihubErr := probeForeignLockHolders(ctx, tx, wi.ID, ftLocks, ftProbes); aihubErr != nil {
		return nil, aihubErr
	}

	// Update step_state if in_progress (H-R7-4)
	var stepStatus string
	var stepAttempt *string
	stepErr := tx.QueryRow(ctx, `
		SELECT current_step_status, current_step_attempt FROM wi_step_state WHERE work_item_id=$1`, wi.ID,
	).Scan(&stepStatus, &stepAttempt)
	// aihub#497: stepErr is read, not returned — pgx.ErrNoRows is the normal "this
	// work item has no step state" answer and the guard below treats any error as
	// "no step to terminate". A class-40 rollback is not that. Same read, same
	// reasoning and same fix as aihub#492's prior-step read on the claim path.
	if aerr := retryConflictErr(stepErr, "failed to read step state"); aerr != nil {
		return nil, aerr
	}
	if stepErr == nil && stepStatus == "in_progress" {
		// aihub#497: this call discarded its return, and since aihub#492 wired
		// bestEffortExec into fnForceTerminateStep that return is exactly where a
		// class-40 rollback comes back. Discarding it undid that fix from the
		// caller's side. Every OTHER error it reports stays discarded — a force
		// takeover is a recovery operation and failing it over step bookkeeping
		// would leave the work item stuck with an attempt nobody holds — but a
		// rollback is not bookkeeping, it is the end of the transaction, and
		// there is nothing left to carry on with.
		//
		// A Code comparison rather than retryConflictErr because this hop returns
		// an *AihubError, not a driver error; ErrConflictSerializationFailure is
		// constructed in exactly one place (retryConflictErr), so the two agree by
		// construction.
		if aerr := fnForceTerminateStep(ctx, tx, wi.ID, priorID, stepAttempt); aerr != nil &&
			aerr.Code == ErrConflictSerializationFailure {
			return nil, aerr
		}
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
	//
	// aihub#497: with the one exception a transaction cannot tolerate. Everything
	// the paragraph above says still holds for every error that leaves the
	// transaction usable; a class-40 rollback does not leave it usable, so
	// "carry on regardless" is not one of the options. Carrying on past one
	// replaces the retryable 409 with an unclassifiable 500 raised by whichever
	// statement happens to run next. See bestEffortExec.
	ftActor := lockEventActor{UserID: callerUserID, Display: callerDisplay}
	ftOp := newLockOp(lockCauseForceTakeover, ftActor).withExtra(map[string]any{
		"prior_attempt_id": priorID,
		"reason":           req.Reason,
	})
	if _, relErr := releaseLocks(ctx, tx, lockDeleteByAttemptSQL, ftOp, priorID); relErr != nil {
		if aerr := retryConflictErr(relErr, "failed to release the prior attempt's locks"); aerr != nil {
			return nil, aerr
		}
	}

	// Emit force_takeover event
	evtID := NewID("evt")
	evtPayload, _ := json.Marshal(map[string]any{
		"prior_attempt_id": priorID,
		"prior_actor":      currentActorDisplay,
		"reason":           req.Reason,
	})
	// aihub#497: see bestEffortExec. aihub#492 converted six of these on the
	// claim and complete-attempt paths and deliberately left this one, on the
	// grounds that this transaction is READ COMMITTED so class 40 could not
	// arrive. That is true only of the 40001 half: 40P01 is raised when Postgres
	// breaks a lock cycle, which it does at ANY isolation level, and this
	// function's isolation level is not a constant either — internal/db/db.go
	// builds the pool with pgxpool.New and pins none, so the database, the role
	// or the DSN decides it. The emission stays best-effort for everything else.
	if aerr := bestEffortExec(ctx, tx, "failed to emit force_takeover event", `
		INSERT INTO agent_events (id, work_item_id, actor_user_id, actor_display, event_type, payload, project)
		VALUES ($1, $2, $3, $4, 'force_takeover', $5, $6)`,
		evtID, wi.ID, callerUserID, "", evtPayload, wi.Project,
	); aerr != nil {
		return nil, aerr
	}

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
	// aihub#238: entries the mapper cannot understand yield no lock here either.
	// Stored data, so this must not fail the takeover.
	//
	// ⚠️ This comment used to close with "the subsequent fresh claim reports them
	// via ClaimResponse.unrecognized_resources", and that sentence was the whole
	// argument for staying silent here. aihub#509 removed it rather than
	// rephrasing it: it describes the polyforge SKILL flow, not this endpoint,
	// and a response is not excused from reporting by what some other call might
	// do next. The report is now on this response too, as
	// ForceTakeoverResponse.unrecognized_resources — same producer, same key.
	//
	// aihub#343: through acquireLockUpsert, one lock_acquired per row.
	//
	// aihub#393: the error is no longer discarded WHOLESALE. A refusal to
	// displace a live foreign holder is now reported as 409 CONFLICT_LOCK_TAKEN
	// — swallowing it is what let this path steal locks, and a takeover that
	// silently proceeds without the lock it was asked to take is a second way to
	// leave the caller believing it holds something it does not.
	//
	// aihub#497: a class-40 rollback is the second error this loop cannot walk
	// past, and it is not a matter of tolerance. aihub#451 measured the shape by
	// raising this transaction to SERIALIZABLE: the 40001 landed here, the
	// discard swallowed it, and the caller was handed
	//
	//	500 INTERNAL_ERROR  failed to update work_item after force_takeover:
	//	                    current transaction is aborted (SQLSTATE 25P02)
	//
	// from the statement below — 25P02, class 25, which no classifier on the path
	// can recognise. So the discard did not cost this takeover a lock row, it
	// cost the caller the retryable 409 and told them the server was broken
	// instead. This is aihub#410's shape, on the path aihub#410 did not cover.
	//
	// The remaining discard is unchanged and still deliberate: every error that
	// leaves the transaction usable is a lock bookkeeping failure, and a force
	// takeover is a recovery operation that must not fail over one, or the work
	// item is left stuck with an attempt nobody holds.
	//
	// ftLocks/ftProbes were derived above, from this same declaredRes.
	for _, l := range ftLocks {
		_, upErr := acquireLockUpsert(ctx, tx, l.ResourceType, l.ResourceKey, newAttemptID, newEpoch,
			wi.Project, wi.ID, ftOp)
		if upErr != nil {
			if aihubErr := lockTakenErrFor(upErr); aihubErr != nil {
				return nil, aihubErr
			}
			if aerr := retryConflictErr(upErr, fmt.Sprintf(
				"failed to acquire lock %s:%s during force_takeover",
				l.ResourceType, l.ResourceKey)); aerr != nil {
				return nil, aerr
			}
		}
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
		// aihub#509. Derived above, from the same stored payload the lock
		// re-derivation read, so the two cannot describe different declarations.
		UnrecognizedResources: ftUnrecognized,
		OK:                    true,
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
	// ATTEMPT_MISMATCH (403), not UNAUTHORIZED (401) — aihub#441, residue (c) of
	// aihub#411's T2-3 row. This is the ONE line the residue was about: for the
	// same wrong session_secret this verifier answered 401 UNAUTHORIZED while
	// verifyAttemptCredentialSimple (memory.go, the pf_emit_event path) answered
	// 403 ATTEMPT_MISMATCH, so a caller classifying by code saw two problems
	// where there was one. Both now answer the same code and the same message.
	//
	// 403 rather than 401 is not a coin toss; three independent things pick it:
	//
	//   - aihub#242's principle, applied by aihub#440: 409 means wrong STATE,
	//     403 means wrong CALLER. A secret that does not hash to the stored one
	//     identifies the wrong caller. Nothing about the attempt's state is
	//     wrong — that is checked below, and answers a 409.
	//   - UNAUTHORIZED is the AUTHENTICATION layer's code everywhere else in the
	//     tree: "missing Authorization header", "must use Bearer scheme",
	//     "invalid or revoked API key", "not authenticated" (middleware.go,
	//     router.go, routes_projects.go). This line was its only other producer,
	//     so a 401 here told a caller "re-authenticate" when the correct
	//     recovery is "re-claim" — two different fixes behind one code.
	//   - internal/mcp already treats ATTEMPT_MISMATCH as exactly this:
	//     classifyStepUpdateErr maps it to "STALE_LOCAL_CREDENTIAL: state file
	//     deleted — please re-claim". A stored secret that no longer matches can
	//     never succeed again, so deleting it and re-claiming is the only
	//     recovery, and the 401 was the one credential failure that did NOT get
	//     it. This is a deliberate behaviour change on pf_update_step: an
	//     invalid secret now deletes the local state file where before the
	//     client kept a credential it could only fail with.
	//
	// It does NOT widen to the paused case. ATTEMPT_PAUSED below stays a 409 and
	// keeps the state file (aihub#209); that branch is reached only by a caller
	// whose secret is VALID, so nothing here can shadow it.
	if subtle.ConstantTimeCompare(storedHashBytes, hashBytes) != 1 {
		return NewErr(ErrAttemptMismatch, "invalid session_secret")
	}

	// 5. Attempt must be running. A paused attempt gets a distinct code so the
	// client keeps its state file and points the user at resume, instead of
	// treating it as a stale-credential mismatch and deleting it (aihub#209).
	//
	// aihub#441 added a fourth status that reaches the generic branch:
	// 'cancelled', written by CancelWorkItem. Before it existed, a cancelled work
	// item left its attempt marked 'paused', so this function answered
	// ATTEMPT_PAUSED — telling the client to resume a work item FnClaimWorkItem
	// refuses as terminal. It now falls to ATTEMPT_MISMATCH, which is the truth:
	// the credential is dead. The message names the status, so the operator reads
	// `attempt status is "cancelled"` rather than a generic refusal.
	if storedStatus != "running" {
		if storedStatus == "paused" {
			return NewErr(ErrAttemptPaused, "attempt is paused; resume it before continuing")
		}
		return NewErr(ErrAttemptMismatch, fmt.Sprintf("attempt status is %q; only running attempts can be used", storedStatus))
	}

	// 6. Update last_active_at (heartbeat)
	//
	// aihub#545: through bestEffortExec, not a discarded tx.Exec. The heartbeat
	// stays best-effort for every error that leaves the transaction usable — a
	// missed refresh costs one stall-detection tick, and failing the caller's
	// operation over it would be backwards. A class-40 rollback does not leave
	// the transaction usable: this function runs inside four SERIALIZABLE
	// transactions (FnCompleteAttempt, FnAcquireLocks, FnRecordRepoPins,
	// FnReconcileCommitLocks), and this UPDATE writes run_attempts — the very
	// table those transactions take SIReadLocks on — so a 40001 here is live,
	// not latent. Discarding it means every later statement runs against a dead
	// transaction and the caller is told about whichever one failed first with
	// 25P02, as a 500 instead of the retryable 409. See bestEffortExec.
	if aerr := bestEffortExec(ctx, tx, "failed to refresh attempt heartbeat (last_active_at)", `
		UPDATE run_attempts SET last_active_at=clock_timestamp() WHERE id=$1`, attemptID); aerr != nil {
		return aerr
	}

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
	// aihub#523: the commit stays best-effort — the only write this transaction
	// carries is the last_active_at heartbeat, and losing that costs one
	// stall-detection tick — except for the one class that is not a lost write
	// but a lost race. This is a bare pool.Begin, so its isolation level is
	// whatever the database, the role or the DSN dials in (aihub#546), and under
	// SERIALIZABLE the commit is exactly where SSI reports that the snapshot the
	// verification just answered from did not serialize. The old discarded
	// `tx.Commit(ctx) //nolint:errcheck` reported success from that doomed read
	// and threw the heartbeat away with it; the retryable 409 is the same answer
	// every other commit on this path already gives (aihub#334).
	if commitErr := tx.Commit(ctx); commitErr != nil {
		if aerr := retryConflictErr(commitErr, "failed to commit credential verification"); aerr != nil {
			return aerr
		}
	}
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

// foreignLockHolderSQL is acquireLocksCollisionSQL plus "and not this work
// item": the cross-work-item conflict probe both entry points into a claim or a
// takeover use. $4 is the work item doing the acquiring.
//
// Excluding the caller's own work item is aihub#207: a resume, and a takeover,
// legitimately re-take locks their own earlier attempt still holds, and a probe
// without this clause 409s them against themselves.
const foreignLockHolderSQL = acquireLocksCollisionSQL + `
	  AND ra.work_item_id != $4`

// probeForeignLockHolders reports ErrConflictLockTaken if any lock in `locks` is
// already held by a running-or-paused attempt of a work item other than wiID.
//
// locks[i] pairs with probes[i]: aihub#261 made the key a declaration WRITES and
// the set of keys that BLOCK it two different things, so the probe — not the
// key — is what the WHERE clause takes. A site that compared on the key alone
// would stop seeing legacy unqualified holders.
//
// aihub#393: one function, called from FnClaimWorkItem and FnForceTakeover, so
// "which paths does a takeover check" cannot be answered differently at the two
// entry points. acquireLockUpsert refuses the same displacement on its own as a
// backstop; this exists so the caller gets a 409 with a conflict_with payload
// before anything has been mutated, rather than a rolled-back transaction.
func probeForeignLockHolders(ctx context.Context, tx pgx.Tx, wiID string,
	locks []ResourceLockReq, probes []lockConflictProbe) *AihubError {

	for i, l := range locks {
		if i >= len(probes) {
			// Defensive: the two slices are built together by deriveClaimLocks /
			// derivedLockProbe. A short probe slice would silently stop checking
			// the tail, which is the one failure mode this whole function exists
			// to prevent, so it is an error rather than a skip.
			return NewErr(ErrInternalError, "lock probe list is shorter than the lock list")
		}
		probe := probes[i]
		var conflictAttemptID, conflictActorDisplay, conflictWISlug string
		err := tx.QueryRow(ctx, foreignLockHolderSQL,
			l.ResourceType, probe.Keys, probe.LikePattern, wiID,
		).Scan(&conflictAttemptID, &conflictActorDisplay, &conflictWISlug)
		if err == nil {
			return NewErrDetails(ErrConflictLockTaken,
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
		// aihub#410. ErrNoRows is the answer "nobody else holds this key", and it
		// is the only error that means anything of the sort. Every other one has
		// to be propagated, because this loop used to treat them all alike: the
		// branch above was the whole error handling, so a failed probe was read
		// as "no conflict" and the claim walked on.
		//
		// Two things went wrong at once, and the second is the one that shows.
		// The probe went blind — safety survives that, because
		// lockUpsertSQL's conditional ON CONFLICT DO UPDATE refuses the same
		// displacement on its own (see resource_events.go) and is the backstop.
		// What does not survive is the CLASSIFICATION. Both callers of this
		// function run inside a transaction, and FnClaimWorkItem's is
		// SERIALIZABLE, so 40001 is live here rather than latent. A 40001 aborts
		// the transaction, which means every later statement in it fails with
		// 25P02 — not class 40 — and the caller is told 500 INTERNAL_ERROR about
		// a statement that was only ever the second victim. The retryable 409
		// that retryConflictErr gives the neighbouring statements was lost at
		// exactly the hop that had the SQLSTATE in its hand.
		//
		// This cannot cost a claim that would otherwise have worked, which is the
		// obvious worry about turning a swallow into a return. Any statement error
		// inside a Postgres transaction aborts it, so once this probe has failed
		// the transaction is already unusable: walking on could never produce a
		// successful claim, only a later statement failing with 25P02. Measured on
		// the mutant in lock_probe_error_db_test.go — the pre-fix build reaches
		// `failed to insert run_attempt` and dies there with 25P02. So this branch
		// changes which error the caller sees, never whether there is one.
		//
		// dbErrCause, not dbErr: the driver's text names which probe failed, and
		// a caller debugging a 500 here has nothing else to go on. The non-conflict
		// outcome of this branch is a return rather than a `continue`, which is
		// why this can be one call — see pgx_err.go on which form belongs where.
		if !errors.Is(err, pgx.ErrNoRows) {
			return dbErrCause(err, fmt.Sprintf("failed to probe lock holders for %s:%s",
				l.ResourceType, l.ResourceKey))
		}
	}
	return nil
}

// lockTakenErrFor maps acquireLockUpsert's refusal to the same 409 the probe
// above produces, and returns nil for any other error so the caller can report
// it as the database failure it is.
//
// The two are deliberately the same error code: whether the foreign holder was
// noticed by the probe or by the upsert's own predicate is an implementation
// detail, and a caller that had to tell them apart would be a caller that could
// get it wrong.
func lockTakenErrFor(err error) *AihubError {
	var refusal *lockHeldByOtherWIError
	if !errors.As(err, &refusal) {
		return nil
	}
	return NewErrDetails(ErrConflictLockTaken,
		fmt.Sprintf("resource %s:%s is already locked", refusal.ResourceType, refusal.ResourceKey),
		map[string]any{
			"conflict_with": map[string]any{
				"attempt_id":     refusal.OwnerAttemptID,
				"actor_display":  refusal.ActorDisplay,
				"work_item_slug": refusal.WorkItemSlug,
			},
		},
	)
}

// ─── Repo pins (aihub#416) ──────────────────────────────────────────────────

// RecordRepoPinsRequest is the body for POST /v1/work_items/:id/repo_pins.
//
// 🔴 WHY THIS IS A SECOND CALL AND NOT PART OF THE CLAIM. The pin is
// `git rev-parse HEAD` inside a worktree, and the worktrees do not exist when
// the claim goes out — the MCP client builds them from the claim RESPONSE, and
// it has to be that way round because a claim can be refused (409 on a work item
// somebody else holds) and building worktrees for a refused claim would leave
// directories behind for a session that never started.
//
// The alternative was to PREDICT the commit before the claim, which is exactly
// what aihub#356 did for branch names and exactly what this wi deleted: a value
// computed before the fact, silently wrong whenever the prediction and the
// filesystem disagreed, with a whole reporting mechanism (keyedBranchProblems)
// built to apologise for it afterwards. Recording after the fact cannot be
// wrong; it can only be absent, and absent is a state the reader can see.
type RecordRepoPinsRequest struct {
	AttemptID     string `json:"attempt_id"`
	ClaimEpoch    int64  `json:"claim_epoch"`
	SessionSecret string `json:"session_secret"`
	// RepoPins maps repo name to the 40-char commit sha that repo's worktree was
	// on when this attempt started work. A repo whose worktree could not be built
	// is ABSENT rather than mapped to "" (owner ruling Q-4).
	RepoPins map[string]string `json:"repo_pins"`
}

// FnRecordRepoPins stores the current attempt's per-repo starting commits.
//
// Credential-checked exactly like every other per-attempt write: only the live
// attempt may record its own provenance, or the field would be a place for
// anyone with writer access to assert where somebody else's conclusions came
// from.
//
// 🔴 It OVERWRITES rather than merging, and that is the conservative direction
// here even though merging is conservative almost everywhere else in this file.
// A re-claim after a pause makes a NEW attempt row, so this only ever overwrites
// within one attempt — i.e. the same session recording the same worktrees a
// second time, where the later reading is the more accurate one. Merging would
// instead accumulate pins for repos whose worktrees have since been removed, and
// a pin naming a tree that is no longer there is worse than no pin at all,
// because it looks like evidence.
//
// 🔴 It records NOTHING and answers 200 for an empty map, rather than clearing
// the column. "I built no worktrees" and "forget what I told you" are different
// statements and only the first one is reachable from the client; a call that
// could blank the record would let a later failed claim erase the provenance of
// the work already done under this attempt.
func FnRecordRepoPins(ctx context.Context, pool *pgxpool.Pool, wiID string, req *RecordRepoPinsRequest) *AihubError {
	tx, err := pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return NewErr(ErrInternalError, "failed to begin transaction")
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	var wi WorkItem
	err = tx.QueryRow(ctx, `
		SELECT id, project, status, current_attempt_id, current_attempt_epoch
		FROM work_items WHERE id=$1 FOR UPDATE`, wiID,
	).Scan(&wi.ID, &wi.Project, &wi.Status, &wi.CurrentAttemptID, &wi.CurrentAttemptEpoch)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return NewErr(ErrNotFound, "work item not found")
		}
		return dbErr(err, "failed to lock work_item")
	}

	if aihubErr := verifyAttemptCredential(ctx, tx, wi, req.AttemptID, req.ClaimEpoch, req.SessionSecret); aihubErr != nil {
		return aihubErr
	}

	if len(req.RepoPins) > 0 {
		if _, err := tx.Exec(ctx,
			`UPDATE run_attempts SET repo_pins=$1 WHERE id=$2`,
			repoPinsJSON(req.RepoPins), req.AttemptID,
		); err != nil {
			return dbErr(err, "failed to record repo pins")
		}
	}

	if err := tx.Commit(ctx); err != nil {
		if aerr := retryConflictErr(err, "failed to commit repo pins"); aerr != nil {
			return aerr
		}
		return NewErr(ErrInternalError, "failed to commit repo pins")
	}
	return nil
}

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
	// It includes locks with no live declaration behind them: locks from a
	// client-supplied requested_locks, file_scope locks whose declared_resources
	// entry was removed before aihub#264, and — on a database carrying rows older
	// than aihub#416 — git_branch/deploy_env rows taken at claim by a build that
	// still derived them.
	//
	// ⚠️ aihub#264 changed the second of those. Removing a path from
	// declared_resources through UpdateWorkItem now DOES release its file_scope
	// lock, in the same transaction as the update — so that population is no
	// longer produced by the ordinary API path, and this field reports it only
	// for locks that predate the change or were never derived from a declaration
	// at all.
	//
	// ⚠️ This list used to say that git_branch and deploy_env "remain the main
	// reason an attempt holds a lock it cannot explain from its current
	// declarations". aihub#416 retired both derivations, so on a current build
	// nothing produces such a row and the ordinary answer here is empty. The
	// remaining producer is an explicit requested_locks.
	AlreadyHeld []ResourceLock `json:"already_held"`
	// UnrecognizedResources is the SAME report ClaimResponse carries, under the
	// same key and produced by the same UnrecognizedDeclaredResources call
	// (aihub#509, closing the half of aihub#411 T2-12 that aihub#416 left open).
	//
	// This endpoint has the strongest claim on it of the three, because its whole
	// contract is "tell me which locks I now hold" and its own decode comment
	// already says so: silently deriving zero targets answers that question with
	// an empty list and a 200, which is something the server never checked. An
	// entry the mapper cannot understand produces no target and so was invisible
	// in exactly the same way — the two lists came back complete and correct, and
	// said nothing about the declaration that contributed to neither.
	//
	// ⚠️ Read it as a report on the DECLARATIONS, not as a third lock list.
	// Acquired and AlreadyHeld stay a partition of the attempt's lock set; this
	// names declared entries that are in neither because they derive no lock at
	// all. Empty on a healthy work item, and `omitempty`.
	UnrecognizedResources []string `json:"unrecognized_resources,omitempty"`
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

	// aihub#509: from the RAW payload, like claim and force_takeover, so the
	// report describes what was STORED rather than what survived decoding.
	//
	// Placed after the decode failure above and not before it: on this endpoint
	// an unparseable payload is an ERROR, not a warning (see the comment on that
	// return), so there is no response to attach a report to. That is a genuine
	// difference from the other two paths and it is theirs, not this one's —
	// they must stay claimable, this one must not lie about lock coverage.
	alUnrecognized := UnrecognizedDeclaredResources(wi.DeclaredResources)

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
	return &AcquireLocksResponse{
		Acquired:    acquired,
		AlreadyHeld: alreadyHeld,
		// aihub#509. Derived above, from the same stored payload the target
		// derivation read.
		UnrecognizedResources: alUnrecognized,
	}, nil
}
