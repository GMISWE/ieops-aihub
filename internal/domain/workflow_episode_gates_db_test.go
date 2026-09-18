package domain

// workflow_episode_gates_db_test.go — the isolated DB regressions for the
// aihub#708 re-review blocker 1 (mem_zRBlSAyQ): workflow start ordering must
// enforce UNRESOLVED repair episode role prerequisites before minting any
// successor or ship.
//
// Two holes this file holds closed:
//
//   - REVIEW-ONLY RECOVERY THEN SHIP: the episode's fresh review alone could
//     record a PASS (the failed gate's latest result then "looked complete"),
//     and an ordinary successor — above all the side-effectful ship step —
//     minted on top of an episode whose repair producer and verification never
//     ran. The fix consults episodeRoleSetComplete's state BEFORE the
//     successor mint; the seeded aftermath of the old hole must still be
//     refused.
//   - OPEN REPLACEMENT WITH OLD APPROVAL: while the episode's replacement
//     producer invocation was still open, a dependent verification/review
//     role minted on the OLD artifact's approval (the approval gate reads
//     LATEST RECORDED results, and the replacement had not recorded). The fix
//     requires the bound repair producer to have recorded a completed result
//     before any dependent role mints, and the replacement's OWN artifact to
//     be approved once it records — the old approval cannot carry over.
//
// The same file holds the PRESERVED valid episode order end to end: the chain
// repair producer -> verification -> fresh review mints and records in order,
// the episode closes on the complete set, and only then does the ordinary
// ship mint.
//
// Gated exactly like workflow_db_test.go (setupWorkflowDB): skipped unless
// AIHUB_TEST_DB is set; migrations 0043 and 0044 are idempotent replays.
//
//	AIHUB_TEST_DB=postgres://postgres:testpass@localhost:5433/aihub_wf708?sslmode=disable \
//	GOWORK=off go test ./internal/domain/ -run TestWorkflowEpisode -count=1 -v

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"

	"github.com/GMISWE/ieops-aihub/internal/skillregistry"
	wf "github.com/GMISWE/ieops-aihub/internal/workflow"
)

// wfShipFlowSpec is the gate flow with a SHIP step at the end: the final
// implementation write feeds an independent verification, an independent
// review, and a side-effectful shipping step. The review gate sits LAST
// before ship, so a fresh PASS on it makes every earlier step's latest result
// "look complete" to the ordering check — exactly the state the unresolved
// episode gate exists for.
func wfShipFlowSpec(spec, review, verify, ship string) []WorkflowStepSpec {
	return []WorkflowStepSpec{
		{ID: "spec", SkillID: spec, SkillVersion: 0, Models: wfModels()},
		{ID: "verify", SkillID: verify, SkillVersion: 0, Models: wfModels(),
			Inputs: []wf.InputRef{{Name: "change", StepID: "spec", Output: "artifact"}}},
		{ID: "review", SkillID: review, SkillVersion: 0, Models: wfModels(),
			Inputs: []wf.InputRef{{Name: "change", StepID: "spec", Output: "artifact"}}},
		{ID: "ship", SkillID: ship, SkillVersion: 0, Models: wfModels()},
	}
}

// seedEpisodeBoundResult writes, directly, what the pre-fix start ordering
// permitted an older binary to mint: an episode-bound invocation with a
// recorded result, minted without the role prerequisites this fix enforces.
// The fixed gates refuse to CREATE that state; this seeds its aftermath so
// the successor gates can be held against rows that already exist.
func seedEpisodeBoundResult(t *testing.T, pool *pgxpool.Pool, wiID string, stepsVersion int,
	stepID, attemptID string, epoch int64, episodeID string,
	status wf.ResultStatus, verdict wf.ReviewVerdict, artifactID string) string {
	t.Helper()
	stepAttemptID := NewID("sa")
	producerID := NewID("wfp")
	invocationID := NewID("winv")
	artifact, _ := json.Marshal(map[string]any{
		"id": artifactID, "version": 1, "hash": "sha256:" + wfHash(artifactID),
	})
	evidence := []any{}
	if verdict != "" {
		evidence = append(evidence, map[string]any{
			"kind": "seed", "ref": "runs/" + artifactID, "hash": "sha256:" + wfHash("ev-"+artifactID),
		})
	}
	evidenceJSON, _ := json.Marshal(evidence)
	raw, _ := json.Marshal(map[string]any{
		"status": string(status), "work_item_id": wiID, "flow_version": stepsVersion,
		"step_id": stepID, "step_attempt_id": stepAttemptID, "epoch": epoch, "producer_id": producerID,
	})
	_, err := pool.Exec(context.Background(), `
		INSERT INTO wi_workflow_invocations
		    (id, work_item_id, steps_version, step_id, step_attempt_id, run_attempt_id, claim_epoch,
		     producer_id, invocation_grant, status, recorded_at, repair_episode_id)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,
		        '{"authority":"read_only","producer_isolation":"independent"}'::jsonb,
		        'recorded', clock_timestamp(), $9)`,
		invocationID, wiID, stepsVersion, stepID, stepAttemptID, attemptID, epoch, producerID, episodeID)
	require.NoError(t, err)
	_, err = pool.Exec(context.Background(), `
		INSERT INTO wi_workflow_results
		    (id, work_item_id, invocation_id, steps_version, step_id, step_attempt_id,
		     run_attempt_id, claim_epoch, producer_id, status, review_verdict, artifact, evidence, raw)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,NULLIF($11,''),$12,$13,$14)`,
		NewID("wres"), wiID, invocationID, stepsVersion, stepID, stepAttemptID, attemptID, epoch,
		producerID, string(status), string(verdict), artifact, evidenceJSON, raw)
	require.NoError(t, err)
	return stepAttemptID
}

// episodeStatusOf reads one authorization's status.
func episodeStatusOf(t *testing.T, pool *pgxpool.Pool, id string) string {
	t.Helper()
	var status string
	require.Nil(t, pool.QueryRow(context.Background(),
		`SELECT status FROM wi_workflow_repair_episodes WHERE id=$1`, id).Scan(&status))
	return status
}

// seedEpisodeFixture pins the shared setup: the ship flow, the original run
// (final write recorded, human-approved when rhs holds, verification
// recorded, review FAILed and paused) and the resumed attempt's episode
// authorization on the failed review.
type episodeFixture struct {
	wi       *WorkItem
	attempt1 string
	attempt2 string
	auth     *WorkflowRepairAuthorization
	reviewSA string // the FAILED review's step attempt
	ownerRec *UserRecord
	owner    string
	project  string
	pool     *pgxpool.Pool
	specRHS  bool
	specArt  string // the ORIGINAL spec artifact id ("art-spec")
}

func seedEpisodeFixture(t *testing.T, specRHS bool) *episodeFixture {
	t.Helper()
	pool := setupWorkflowDB(t)
	ctx := context.Background()
	owner := wfUser(t, pool, "owner")
	project := wfProjectName(t)
	wfCleanup(t, pool, project, owner)
	t.Cleanup(func() { wfCleanup(t, pool, project, owner) })
	testSeedProject(t, pool, project, owner)
	caller := &UserRecord{ID: owner, Role: "writer"}

	spec := wfSkill(t, ctx, pool, caller, "grill-me-"+skillSuffix(t),
		wfSchemasContract([]skillregistry.Capability{skillregistry.CapAuthoring}, nil, []string{"artifact"}))
	review := wfSkill(t, ctx, pool, caller, "indep-review-"+skillSuffix(t),
		wfSchemasContract([]skillregistry.Capability{skillregistry.CapReview}, []string{"change"}, []string{"approval"}))
	verify := wfSkill(t, ctx, pool, caller, "indep-verify-"+skillSuffix(t),
		wfSchemasContract([]skillregistry.Capability{skillregistry.CapVerification}, []string{"change"}, []string{"evidence"}))
	ship := wfSkill(t, ctx, pool, caller, "ship-it-"+skillSuffix(t),
		wfContract(skillregistry.CapShipping))

	steps := wfShipFlowSpec(spec, review, verify, ship)
	if specRHS {
		steps[0].RHS = &[]bool{true}[0]
	}
	wi, aerr := wfCreate(t, ctx, pool, project, caller, owner, true, steps)
	require.Nil(t, aerr)
	attempt := wfAttempt(t, pool, wi.ID, owner, "s3cret", 1)

	specStart := wfStart(t, ctx, pool, wi.ID, attempt, "s3cret", 1, "spec")
	_, aerr = RecordWorkflowResult(ctx, pool, wi.ID, RecordWorkflowResultRequest{
		AttemptID: attempt, ClaimEpoch: 1, SessionSecret: "s3cret",
		Result: wfStepResult(t, ctx, pool, specStart, wf.StatusCompleted, "", "art-spec"),
	})
	require.Nil(t, aerr)

	f := &episodeFixture{
		wi: wi, ownerRec: caller, owner: owner, project: project,
		pool: pool, specRHS: specRHS,
	}

	if specRHS {
		// The OLD approval: a human approved the ORIGINAL artifact before the
		// review failed. This is the approval that must NOT carry over to the
		// replacement's new artifact later.
		artifact := wfLatestArtifactRef(t, pool, wi.ID, "spec")
		_, aerr = ApproveWorkflowStep(ctx, pool, wi.ID, caller, owner, "human",
			ApproveWorkflowRequest{StepsVersion: 1, StepID: "spec", Artifact: artifact, Decision: "approved"})
		require.Nil(t, aerr)
	}

	verifyStart := wfStart(t, ctx, pool, wi.ID, attempt, "s3cret", 1, "verify")
	_, aerr = RecordWorkflowResult(ctx, pool, wi.ID, RecordWorkflowResultRequest{
		AttemptID: attempt, ClaimEpoch: 1, SessionSecret: "s3cret",
		Result: wfStepResult(t, ctx, pool, verifyStart, wf.StatusCompleted, "", "art-verify"),
	})
	require.Nil(t, aerr)

	reviewStart := wfStart(t, ctx, pool, wi.ID, attempt, "s3cret", 1, "review")
	rec, aerr := RecordWorkflowResult(ctx, pool, wi.ID, RecordWorkflowResultRequest{
		AttemptID: attempt, ClaimEpoch: 1, SessionSecret: "s3cret",
		Result: wfStepResult(t, ctx, pool, reviewStart, wf.StatusCompleted, wf.ReviewFail, "art-review"),
	})
	require.Nil(t, aerr)
	require.True(t, rec.Paused)

	// The resumed attempt opens the episode on the failed review.
	f.attempt2 = wfAttempt(t, pool, wi.ID, owner, "s3cret", 2)
	auth, aerr := AuthorizeWorkflowRepair(ctx, pool, wi.ID, AuthorizeWorkflowRepairRequest{
		AttemptID: f.attempt2, ClaimEpoch: 2, SessionSecret: "s3cret",
		FailedStepAttemptID: reviewStart.StepAttemptID, Kind: "episode",
		Reason: "owner-authorized recovery from the failing review",
	})
	require.Nil(t, aerr)
	require.Equal(t, "open", auth.Status)
	f.auth = auth
	return f
}

// ─── Regression 1: review-only recovery, then ordinary ship ──────────────────

// TestWorkflowEpisodeReviewOnlyRecoveryRefusesShip holds the first named hole:
// the episode's fresh review recovered ALONE (no repair producer, no
// verification — the aftermath the pre-fix start ordering permitted), every
// earlier step's latest result therefore "looks complete", and the
// side-effectful ship step must STILL be refused while the episode is open
// and unresolved. The WI gates on the human (rhs=true) but no step does, so
// the approval gate is inert here and the unresolved-episode gate is the only
// thing that can refuse: the refusal is attributable to exactly the rule
// under test.
func TestWorkflowEpisodeReviewOnlyRecoveryRefusesShip(t *testing.T) {
	f := seedEpisodeFixture(t, false /* specRHS */)
	ctx := context.Background()

	// The hole's aftermath, seeded: the fresh review role alone recorded a
	// PASS. The fixed role-order gate refuses to mint this state now, but the
	// successor gate must still defend against rows an older binary wrote.
	seedEpisodeBoundResult(t, f.pool, f.wi.ID, 1, "review", f.attempt2, 2, f.auth.ID,
		wf.StatusCompleted, wf.ReviewPass, "art-review-reroll")

	// The ordinary ship mint is refused: the episode is open and its recovery
	// roles are not complete, so the failed review is not recovered no matter
	// how complete the latest results look.
	_, aerr := StartWorkflowStep(ctx, f.pool, f.wi.ID, StartWorkflowStepRequest{
		AttemptID: f.attempt2, ClaimEpoch: 2, SessionSecret: "s3cret", StepID: "ship"})
	require.NotNil(t, aerr)
	require.Equal(t, ErrConflictStepInProgress, aerr.Code)
	require.Contains(t, aerr.Message, f.auth.ID, "the refusal must name the open episode")
	require.Contains(t, aerr.Message, "unresolved")

	// And the episode really is still open: nothing closed it.
	require.Equal(t, "open", episodeStatusOf(t, f.pool, f.auth.ID))
}

// ─── Regression 2: open replacement with old approval ────────────────────────

// TestWorkflowEpisodeOpenReplacementOldApprovalRefusesSuccessors holds the
// second named hole, then walks the whole PRESERVED chain behind it:
//
//   - while the episode's replacement producer invocation is OPEN, neither
//     the dependent verification role nor the fresh review role may mint —
//     the OLD artifact's approval must not stand in for the replacement's;
//   - the ordinary ship is refused while the episode is open;
//   - once the replacement records its NEW artifact, the old approval is
//     STALE: the verification role still refuses until a human approves the
//     exact replacement artifact (old approval cannot carry over);
//   - with the replacement approved, the chain mints in order — verification,
//     then fresh review — and the episode closes on the complete set;
//   - only then does the ordinary ship mint.
func TestWorkflowEpisodeOpenReplacementOldApprovalRefusesSuccessors(t *testing.T) {
	f := seedEpisodeFixture(t, true /* specRHS: the spec step gates on a human */)
	ctx := context.Background()

	// The replacement producer invocation stays OPEN: minted, no result yet.
	replacement := wfStartBound(t, ctx, f.pool, f.wi.ID, f.attempt2, "s3cret", 2, "spec", f.auth.ID)

	// THE REGRESSION: the successor episode roles cannot mint while the
	// replacement is open — the old artifact's approval must not stand in.
	_, aerr := StartWorkflowStep(ctx, f.pool, f.wi.ID, StartWorkflowStepRequest{
		AttemptID: f.attempt2, ClaimEpoch: 2, SessionSecret: "s3cret",
		StepID: "verify", RepairEpisodeID: f.auth.ID})
	require.NotNil(t, aerr)
	require.Equal(t, ErrConflictStepInProgress, aerr.Code)
	require.Contains(t, aerr.Message, "repair producer")
	require.Contains(t, aerr.Message, "completed result")

	_, aerr = StartWorkflowStep(ctx, f.pool, f.wi.ID, StartWorkflowStepRequest{
		AttemptID: f.attempt2, ClaimEpoch: 2, SessionSecret: "s3cret",
		StepID: "review", RepairEpisodeID: f.auth.ID})
	require.NotNil(t, aerr)
	require.Equal(t, ErrConflictStepInProgress, aerr.Code)
	require.Contains(t, aerr.Message, "repair producer")

	// The ordinary ship is refused while the episode is open, replacement or
	// not: no successor mints behind an unresolved recovery.
	_, aerr = StartWorkflowStep(ctx, f.pool, f.wi.ID, StartWorkflowStepRequest{
		AttemptID: f.attempt2, ClaimEpoch: 2, SessionSecret: "s3cret", StepID: "ship"})
	require.NotNil(t, aerr)
	require.Equal(t, ErrConflictStepInProgress, aerr.Code)

	// The replacement records a NEW artifact. The old approval is now STALE —
	// it names art-spec, the gate demands art-spec-2 — so the verification
	// role STILL refuses: the old artifact approval cannot carry over.
	_, aerr = RecordWorkflowResult(ctx, f.pool, f.wi.ID, RecordWorkflowResultRequest{
		AttemptID: f.attempt2, ClaimEpoch: 2, SessionSecret: "s3cret",
		Result: wfStepResult(t, ctx, f.pool, replacement, wf.StatusCompleted, "", "art-spec-2"),
	})
	require.Nil(t, aerr)
	require.Equal(t, "open", episodeStatusOf(t, f.pool, f.auth.ID),
		"one recorded role must not close the episode")

	_, aerr = StartWorkflowStep(ctx, f.pool, f.wi.ID, StartWorkflowStepRequest{
		AttemptID: f.attempt2, ClaimEpoch: 2, SessionSecret: "s3cret",
		StepID: "verify", RepairEpisodeID: f.auth.ID})
	require.NotNil(t, aerr)
	require.Equal(t, ErrConflictStepInProgress, aerr.Code)
	require.Contains(t, aerr.Message, "went stale",
		"the refusal must say the old decision went stale on the replacement's new artifact")

	// A human approves the REPLACEMENT's exact artifact; the chain resumes.
	artifact2 := wfLatestArtifactRef(t, f.pool, f.wi.ID, "spec")
	_, aerr = ApproveWorkflowStep(ctx, f.pool, f.wi.ID, f.ownerRec, f.owner, "human",
		ApproveWorkflowRequest{StepsVersion: 1, StepID: "spec", Artifact: artifact2, Decision: "approved"})
	require.Nil(t, aerr)

	verif := wfStartBound(t, ctx, f.pool, f.wi.ID, f.attempt2, "s3cret", 2, "verify", f.auth.ID)

	// The fresh review still waits for the verification's COMPLETED result:
	// the chain is repair producer -> verification -> review, in that order.
	_, aerr = StartWorkflowStep(ctx, f.pool, f.wi.ID, StartWorkflowStepRequest{
		AttemptID: f.attempt2, ClaimEpoch: 2, SessionSecret: "s3cret",
		StepID: "review", RepairEpisodeID: f.auth.ID})
	require.NotNil(t, aerr)
	require.Equal(t, ErrConflictStepInProgress, aerr.Code)
	require.Contains(t, aerr.Message, "verification gate")

	_, aerr = RecordWorkflowResult(ctx, f.pool, f.wi.ID, RecordWorkflowResultRequest{
		AttemptID: f.attempt2, ClaimEpoch: 2, SessionSecret: "s3cret",
		Result: wfStepResult(t, ctx, f.pool, verif, wf.StatusCompleted, "", "art-verify-2"),
	})
	require.Nil(t, aerr)
	require.Equal(t, "open", episodeStatusOf(t, f.pool, f.auth.ID),
		"two of three roles recorded must leave the episode open")

	fresh := wfStartBound(t, ctx, f.pool, f.wi.ID, f.attempt2, "s3cret", 2, "review", f.auth.ID)
	_, aerr = RecordWorkflowResult(ctx, f.pool, f.wi.ID, RecordWorkflowResultRequest{
		AttemptID: f.attempt2, ClaimEpoch: 2, SessionSecret: "s3cret",
		Result: wfStepResult(t, ctx, f.pool, fresh, wf.StatusCompleted, wf.ReviewPass, "art-review-2"),
	})
	require.Nil(t, aerr)
	require.Equal(t, "closed", episodeStatusOf(t, f.pool, f.auth.ID),
		"the complete role set closes the episode")

	// Behind the CLOSED episode, the ordinary ship mints: the valid episode
	// order (repair -> verify -> review, then ship) is preserved.
	shipStart := wfStart(t, ctx, f.pool, f.wi.ID, f.attempt2, "s3cret", 2, "ship")
	require.NotEmpty(t, shipStart.StepAttemptID)
}
