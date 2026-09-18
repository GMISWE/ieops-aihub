package domain

// workflow_episode_retry_lineage_db_test.go — the isolated DB regressions for
// the aihub#708 Batch 2A re-review blocker mem_FHuxXIXI: a repair EPISODE role
// invocation that records provider_error must recover through the AUTHORIZED
// RETRY LINEAGE, and the episode's effective role set — what the start-time
// role prerequisites, the episode close and the successor gates read — must
// select the latest successful authorized replacement while the failed
// history rows stay.
//
// The hole this file holds closed, in the order the reviewer named it:
//
//   - the episode's repair producer recorded provider_error; the authorized
//     retry's replacement invocation is tagged with the RETRY authorization's
//     id, but episodeRoleRows/load only ever saw invocations directly tagged
//     with the EPISODE's id. Fresh verification therefore saw the failed
//     original forever (the dependent role's prerequisite never completed),
//     a direct rebind of the step to the episode was refused (each role binds
//     exactly one), and reconcile cannot remove a RECORDED failure — the
//     episode hung open and every ordinary start with it.
//   - the fix is the immutable parent lineage (parent_episode_id on the
//     retry, derived server-side at authorization) plus the lineage-aware
//     effective role lookup; the retry CHAIN is bounded (maxRetryChainDepth),
//     so fail→authorize→fail cannot loop unbounded.
//
// The required end-to-end regression drives the whole path: producer under
// episode provider_error → authorized retry → replacement completed → fresh
// verification → fresh review → the episode closes on the EFFECTIVE role set
// → the ordinary ship starts and closes behind it. A third regression holds
// the close TRIGGER's lineage half: when the completing result is itself a
// chain replacement under a retry (the fresh review's own provider_error,
// retried), the parent episode must close in the SAME transaction — not leak
// open with an effectively-complete role set.
//
// The Astra re-review's remaining blocker (2026-09-18) is held here too:
// loadEpisodeRetryReplacements LEFT JOINs the retry's live replacement
// invocation and used to scan the nullable inv.step_attempt_id into a plain
// string BEFORE the empty-string chain-end handling ran. Two real states
// produce a NULL there — a retry AUTHORIZED whose replacement has not started
// yet, and a retry whose only replacement was RECONCILED away (superseded by
// the controller fence, not yet re-bound) — and both answered an internal
// NULL-scan DB error that rolled the caller's whole result transaction back,
// instead of leaving the episode's roles unresolved the way every other
// outstanding shape does. The fix COALESCEs the column to '' (the same
// discipline the B1 regression pinned for res.created_at), so both states
// take the existing "no entry → chain ends at the failed invocation below
// it" path:
//
//   TestWorkflowEpisodeRetryAuthorizedNotStartedStaysUnresolved
//   TestWorkflowEpisodeRetrySupersededReplacementStaysUnresolved
//
// Both hold the full contract: an UNRELATED episode result still records
// (nothing rolls back), the affected role answers typed WAITS and refusals
// (never an internal error), and the recovery path stays open — the retry's
// replacement mints, records, and the episode completes through the chain.
//
// Gated exactly like workflow_db_test.go (setupWorkflowDB): skipped unless
// AIHUB_TEST_DB is set; migrations 0043 and 0044 are idempotent replays.
//
//	AIHUB_TEST_DB=postgres://postgres:testpass@localhost:5433/aihub_wf708?sslmode=disable \
//	GOWORK=off go test ./internal/domain/ -run 'TestWorkflowEpisodeRetry|TestWorkflowEpisodeProviderError' -count=1 -v

import (
	"context"
	"fmt"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"

	"github.com/GMISWE/ieops-aihub/internal/skillregistry"
	wf "github.com/GMISWE/ieops-aihub/internal/workflow"
)

// TestWorkflowEpisodeProviderErrorRetryLineageCompletes is THE regression for
// mem_FHuxXIXI: an episode role invocation's provider_error recovers through
// the authorized retry lineage, the effective role set follows the completed
// replacement (not the failed original), the approval gate reads the
// replacement's own artifact, the episode closes on the effective set, and
// the ordinary ship starts and closes behind it. The stale original result
// stays rejected, the failed history row stays recorded, and no invocation is
// ever rebound to the episode.
func TestWorkflowEpisodeProviderErrorRetryLineageCompletes(t *testing.T) {
	// specRHS=true: the producer step gates on a human, so the replacement's
	// NEW artifact must face its own approval before dependents mint — the
	// approval gate must read the EFFECTIVE replacement, never the original.
	f := seedEpisodeFixture(t, true /* specRHS */)
	ctx := context.Background()

	// ── The blocker's entry state: the episode's repair producer role
	// invocation records provider_error mid-episode. Not a review FAIL: no
	// pause, the episode just stays open with an incomplete role.
	repair := wfStartBound(t, ctx, f.pool, f.wi.ID, f.attempt2, "s3cret", 2, "spec", f.auth.ID)
	rec, aerr := RecordWorkflowResult(ctx, f.pool, f.wi.ID, RecordWorkflowResultRequest{
		AttemptID: f.attempt2, ClaimEpoch: 2, SessionSecret: "s3cret",
		Result: wfStepResult(t, ctx, f.pool, repair, wf.StatusProviderError, "", "art-spec-pe"),
	})
	require.Nil(t, aerr)
	require.False(t, rec.Paused, "a provider_error is not a review FAIL; nothing pauses")

	// ── The hang, as the reviewer measured it: the dependent verification
	// role cannot mint while the repair role's only visible result is the
	// failed original, and the ordinary ship is refused behind the open
	// episode. (With the fix these refusals are still correct HERE — the
	// replacement has not recorded yet.)
	_, aerr = StartWorkflowStep(ctx, f.pool, f.wi.ID, StartWorkflowStepRequest{
		AttemptID: f.attempt2, ClaimEpoch: 2, SessionSecret: "s3cret",
		StepID: "verify", RepairEpisodeID: f.auth.ID})
	require.NotNil(t, aerr)
	require.Equal(t, ErrConflictStepInProgress, aerr.Code)
	require.Contains(t, aerr.Message, "repair producer")
	_, aerr = StartWorkflowStep(ctx, f.pool, f.wi.ID, StartWorkflowStepRequest{
		AttemptID: f.attempt2, ClaimEpoch: 2, SessionSecret: "s3cret", StepID: "ship"})
	require.NotNil(t, aerr)
	require.Equal(t, ErrConflictStepInProgress, aerr.Code)

	// ── The authorized retry over the episode role's failed attempt: the
	// opened authorization carries the immutable parent lineage — the episode
	// whose role this retry's replacements stand in for.
	retryAuth, aerr := AuthorizeWorkflowRepair(ctx, f.pool, f.wi.ID, AuthorizeWorkflowRepairRequest{
		AttemptID: f.attempt2, ClaimEpoch: 2, SessionSecret: "s3cret",
		FailedStepAttemptID: repair.StepAttemptID, Kind: "retry",
		Reason: "provider channel died mid-episode; retry the repair producer role",
	})
	require.Nil(t, aerr)
	require.Equal(t, "retry", retryAuth.Kind)
	require.Equal(t, f.auth.ID, retryAuth.ParentEpisodeID,
		"the retry must derive and expose the episode whose role invocation it replaces")

	// ── No direct rebind: the episode still binds exactly one invocation per
	// role step; the replacement lives under the RETRY's authorization, never
	// by re-binding the step to the episode.
	_, aerr = StartWorkflowStep(ctx, f.pool, f.wi.ID, StartWorkflowStepRequest{
		AttemptID: f.attempt2, ClaimEpoch: 2, SessionSecret: "s3cret",
		StepID: "spec", RepairEpisodeID: f.auth.ID})
	require.NotNil(t, aerr)
	require.Equal(t, ErrConflictDuplicate, aerr.Code)
	require.Contains(t, aerr.Message, "each role binds exactly one")

	// ── The replacement invocation, bound to the RETRY authorization,
	// records completed. The retry closes; the episode stays open until all
	// three of its roles are effectively complete.
	replacement := wfStartBound(t, ctx, f.pool, f.wi.ID, f.attempt2, "s3cret", 2, "spec", retryAuth.ID)
	rec, aerr = RecordWorkflowResult(ctx, f.pool, f.wi.ID, RecordWorkflowResultRequest{
		AttemptID: f.attempt2, ClaimEpoch: 2, SessionSecret: "s3cret",
		Result: wfStepResult(t, ctx, f.pool, replacement, wf.StatusCompleted, "", "art-spec-2"),
	})
	require.Nil(t, aerr)
	require.False(t, rec.Paused)
	require.Equal(t, "closed", episodeStatusOf(t, f.pool, retryAuth.ID))
	require.Equal(t, "open", episodeStatusOf(t, f.pool, f.auth.ID),
		"the retry closing must not close the episode; two roles are still outstanding")

	// ── Failed history RETAINED and stale originals REJECTED: the
	// provider_error result row is still there, and the original invocation
	// answers its distinct already-recorded refusal to any further result.
	var failedRows int
	require.Nil(t, f.pool.QueryRow(ctx,
		`SELECT count(*) FROM wi_workflow_results WHERE step_attempt_id=$1`,
		repair.StepAttemptID).Scan(&failedRows))
	require.Equal(t, 1, failedRows, "the failed history row must stay recorded, never rewritten away")
	_, aerr = RecordWorkflowResult(ctx, f.pool, f.wi.ID, RecordWorkflowResultRequest{
		AttemptID: f.attempt2, ClaimEpoch: 2, SessionSecret: "s3cret",
		Result: wfStepResult(t, ctx, f.pool, repair, wf.StatusCompleted, "", "art-spec-stale"),
	})
	require.NotNil(t, aerr)
	require.Equal(t, ErrConflictStepAttemptMismatch, aerr.Code)
	require.Contains(t, aerr.Message, "already has a recorded result")

	// ── The approval gate reads the EFFECTIVE replacement: the verification
	// role's mint is refused while the only human decision names the ORIGINAL
	// artifact (art-spec) — it went stale when the replacement recorded
	// art-spec-2. This is where the old shape minted on the old approval.
	_, aerr = StartWorkflowStep(ctx, f.pool, f.wi.ID, StartWorkflowStepRequest{
		AttemptID: f.attempt2, ClaimEpoch: 2, SessionSecret: "s3cret",
		StepID: "verify", RepairEpisodeID: f.auth.ID})
	require.NotNil(t, aerr)
	require.Equal(t, ErrConflictStepInProgress, aerr.Code)
	require.Contains(t, aerr.Message, "went stale",
		"the refusal must say the old approval went stale on the replacement's artifact: %s", aerr.Message)

	// A human approves the REPLACEMENT's exact artifact.
	artifact2 := wfLatestArtifactRef(t, f.pool, f.wi.ID, "spec")
	_, aerr = ApproveWorkflowStep(ctx, f.pool, f.wi.ID, f.ownerRec, f.owner, "human",
		ApproveWorkflowRequest{StepsVersion: 1, StepID: "spec", Artifact: artifact2, Decision: "approved"})
	require.Nil(t, aerr)

	// ── THE FIX, live: the verification role mints now — the effective repair
	// role is the COMPLETED REPLACEMENT, not the failed original the
	// direct-tag lookup used to see forever.
	verif := wfStartBound(t, ctx, f.pool, f.wi.ID, f.attempt2, "s3cret", 2, "verify", f.auth.ID)
	rec, aerr = RecordWorkflowResult(ctx, f.pool, f.wi.ID, RecordWorkflowResultRequest{
		AttemptID: f.attempt2, ClaimEpoch: 2, SessionSecret: "s3cret",
		Result: wfStepResult(t, ctx, f.pool, verif, wf.StatusCompleted, "", "art-verify-2"),
	})
	require.Nil(t, aerr)
	require.False(t, rec.Paused)
	require.Equal(t, "open", episodeStatusOf(t, f.pool, f.auth.ID), "two effective roles complete leave the episode open")

	// ── The fresh review closes the episode ON THE EFFECTIVE SET: the close
	// predicate's repair role is satisfied by the replacement through the
	// lineage, in episode order (failed review → effective repair →
	// verification → fresh review).
	fresh := wfStartBound(t, ctx, f.pool, f.wi.ID, f.attempt2, "s3cret", 2, "review", f.auth.ID)
	rec, aerr = RecordWorkflowResult(ctx, f.pool, f.wi.ID, RecordWorkflowResultRequest{
		AttemptID: f.attempt2, ClaimEpoch: 2, SessionSecret: "s3cret",
		Result: wfStepResult(t, ctx, f.pool, fresh, wf.StatusCompleted, wf.ReviewPass, "art-review-2"),
	})
	require.Nil(t, aerr)
	require.False(t, rec.Paused)
	require.Equal(t, "closed", episodeStatusOf(t, f.pool, f.auth.ID),
		"the effective role set — replacement standing in for the failed original — must close the episode")

	// ── Behind the CLOSED episode the ordinary ship starts and closes: the
	// unresolved-recovery gate reads the same effective set.
	shipStart := wfStart(t, ctx, f.pool, f.wi.ID, f.attempt2, "s3cret", 2, "ship")
	rec, aerr = RecordWorkflowResult(ctx, f.pool, f.wi.ID, RecordWorkflowResultRequest{
		AttemptID: f.attempt2, ClaimEpoch: 2, SessionSecret: "s3cret",
		Result: wfStepResult(t, ctx, f.pool, shipStart, wf.StatusCompleted, "", "art-ship"),
	})
	require.Nil(t, aerr)
	require.False(t, rec.Paused)

	// ── The read model carries the lineage: the GET view's repair summaries
	// expose parent_episode_id for the controller.
	view, aerr := GetWorkItemWorkflow(ctx, f.pool, f.wi.ID)
	require.Nil(t, aerr)
	sawRetry, sawEpisode := false, false
	for _, r := range view.Repairs {
		if r.ID == retryAuth.ID {
			sawRetry = true
			require.Equal(t, f.auth.ID, r.ParentEpisodeID)
		}
		if r.ID == f.auth.ID {
			sawEpisode = true
			require.Empty(t, r.ParentEpisodeID, "an episode authorization carries no parent")
		}
	}
	require.True(t, sawRetry)
	require.True(t, sawEpisode)
}

// TestWorkflowEpisodeRetryChainBounded holds the bound half of the lineage
// contract: a provider that keeps dying cannot be retried unbounded. Three
// retries may chain off one failed invocation (each authorized explicitly,
// each replacement recording provider_error and closing its own
// authorization); the fourth is refused at the bound, with the parent
// episode's id propagated along the whole chain, and the episode stays open
// with the unrecovered role — escalate, do not loop.
func TestWorkflowEpisodeRetryChainBounded(t *testing.T) {
	// specRHS=false: no human gate in play; the chain bound is the only rule
	// under test.
	f := seedEpisodeFixture(t, false /* specRHS */)
	ctx := context.Background()

	// The episode's repair producer role records provider_error.
	prev := wfStartBound(t, ctx, f.pool, f.wi.ID, f.attempt2, "s3cret", 2, "spec", f.auth.ID)
	_, aerr := RecordWorkflowResult(ctx, f.pool, f.wi.ID, RecordWorkflowResultRequest{
		AttemptID: f.attempt2, ClaimEpoch: 2, SessionSecret: "s3cret",
		Result: wfStepResult(t, ctx, f.pool, prev, wf.StatusProviderError, "", "art-pe-0"),
	})
	require.Nil(t, aerr)

	// Three retries chain: authorize over the latest failed attempt, bind the
	// replacement to the RETRY, record provider_error again. Every link
	// derives the same parent episode.
	for i := 1; i <= maxRetryChainDepth; i++ {
		auth, aerr := AuthorizeWorkflowRepair(ctx, f.pool, f.wi.ID, AuthorizeWorkflowRepairRequest{
			AttemptID: f.attempt2, ClaimEpoch: 2, SessionSecret: "s3cret",
			FailedStepAttemptID: prev.StepAttemptID, Kind: "retry",
			Reason: "provider channel died again mid-episode; bounded retry continues",
		})
		require.Nil(t, aerr, "retry %d of %d must be authorized", i, maxRetryChainDepth)
		require.Equal(t, f.auth.ID, auth.ParentEpisodeID,
			"retry %d must inherit the chain's parent episode", i)
		repl := wfStartBound(t, ctx, f.pool, f.wi.ID, f.attempt2, "s3cret", 2, "spec", auth.ID)
		_, aerr = RecordWorkflowResult(ctx, f.pool, f.wi.ID, RecordWorkflowResultRequest{
			AttemptID: f.attempt2, ClaimEpoch: 2, SessionSecret: "s3cret",
			Result: wfStepResult(t, ctx, f.pool, repl, wf.StatusProviderError, "", fmt.Sprintf("art-pe-%d", i)),
		})
		require.Nil(t, aerr)
		require.Equal(t, "closed", episodeStatusOf(t, f.pool, auth.ID),
			"a retry closes when its own invocation records, whatever the status")
		require.Equal(t, "open", episodeStatusOf(t, f.pool, f.auth.ID))
		prev = repl
	}

	// The fourth retry is refused at the bound: the chain already holds its
	// three retries and the provider is not recovering.
	_, aerr = AuthorizeWorkflowRepair(ctx, f.pool, f.wi.ID, AuthorizeWorkflowRepairRequest{
		AttemptID: f.attempt2, ClaimEpoch: 2, SessionSecret: "s3cret",
		FailedStepAttemptID: prev.StepAttemptID, Kind: "retry",
		Reason: "a fourth retry must be refused at the chain bound",
	})
	require.NotNil(t, aerr)
	require.Equal(t, ErrConflictDuplicate, aerr.Code)
	require.Contains(t, aerr.Message, "retry lineage")
	require.Contains(t, aerr.Message, "escalate")

	// The episode is NOT silently wedged into a pass: it stays open with the
	// unrecovered role — the chain's live end is still provider_error — so
	// the dependent role and the ordinary ship stay refused, loudly.
	_, aerr = StartWorkflowStep(ctx, f.pool, f.wi.ID, StartWorkflowStepRequest{
		AttemptID: f.attempt2, ClaimEpoch: 2, SessionSecret: "s3cret",
		StepID: "verify", RepairEpisodeID: f.auth.ID})
	require.NotNil(t, aerr)
	require.Equal(t, ErrConflictStepInProgress, aerr.Code)
	require.Contains(t, aerr.Message, "repair producer")
	_, aerr = StartWorkflowStep(ctx, f.pool, f.wi.ID, StartWorkflowStepRequest{
		AttemptID: f.attempt2, ClaimEpoch: 2, SessionSecret: "s3cret", StepID: "ship"})
	require.NotNil(t, aerr)
	require.Equal(t, ErrConflictStepInProgress, aerr.Code)

	// The failed history is fully retained: the episode-bound original and
	// every chain replacement's provider_error row are all still recorded.
	var providerErrors int
	require.Nil(t, f.pool.QueryRow(ctx, `
		SELECT count(*) FROM wi_workflow_results
		WHERE work_item_id=$1 AND step_id='spec' AND status='provider_error'`,
		f.wi.ID).Scan(&providerErrors))
	require.Equal(t, maxRetryChainDepth+1, providerErrors,
		"the original role failure plus every chain failure must remain in the history")
}

// TestWorkflowEpisodeRetryLineageClosesParentOnFinalRoleReplacement holds the
// close trigger's lineage half (mem_FHuxXIXI): the episode's LAST role to
// complete can itself be a chain replacement under a RETRY — the fresh review
// provider_errors, an authorized retry replaces it, and the replacement
// records the closing PASS. That recording's own authorization is the RETRY,
// so the parent episode's close must be evaluated through the lineage in the
// SAME transaction; without the propagation the episode leaks open forever
// with an effectively-complete role set — counting against the
// open-authorization bound and misreporting the recovery as unresolved while
// every effective-set gate already answers complete.
func TestWorkflowEpisodeRetryLineageClosesParentOnFinalRoleReplacement(t *testing.T) {
	// specRHS=false: the human gate is not under test here; the close trigger
	// and the gates behind a CLOSED episode are.
	f := seedEpisodeFixture(t, false /* specRHS */)
	ctx := context.Background()

	// The episode's first two roles record normally: the repair producer and
	// the fresh verification, both E-bound and completed, in episode order.
	repair := wfStartBound(t, ctx, f.pool, f.wi.ID, f.attempt2, "s3cret", 2, "spec", f.auth.ID)
	_, aerr := RecordWorkflowResult(ctx, f.pool, f.wi.ID, RecordWorkflowResultRequest{
		AttemptID: f.attempt2, ClaimEpoch: 2, SessionSecret: "s3cret",
		Result: wfStepResult(t, ctx, f.pool, repair, wf.StatusCompleted, "", "art-spec-e"),
	})
	require.Nil(t, aerr)
	verif := wfStartBound(t, ctx, f.pool, f.wi.ID, f.attempt2, "s3cret", 2, "verify", f.auth.ID)
	_, aerr = RecordWorkflowResult(ctx, f.pool, f.wi.ID, RecordWorkflowResultRequest{
		AttemptID: f.attempt2, ClaimEpoch: 2, SessionSecret: "s3cret",
		Result: wfStepResult(t, ctx, f.pool, verif, wf.StatusCompleted, "", "art-verify-e"),
	})
	require.Nil(t, aerr)

	// The fresh review role provider_errors: the LAST role dies, the episode
	// stays open with two completed roles and one failed. The verdict is
	// SHAPE-REQUIRED on every review result (a review row without a
	// pass/warn/fail is rejected at record time); pass is inert here — a
	// non-completed row can never satisfy a role — while fail would wrongly
	// bar the retry kind below (a FAIL recovers through an episode, never a
	// reroll of the gate).
	fresh := wfStartBound(t, ctx, f.pool, f.wi.ID, f.attempt2, "s3cret", 2, "review", f.auth.ID)
	_, aerr = RecordWorkflowResult(ctx, f.pool, f.wi.ID, RecordWorkflowResultRequest{
		AttemptID: f.attempt2, ClaimEpoch: 2, SessionSecret: "s3cret",
		Result: wfStepResult(t, ctx, f.pool, fresh, wf.StatusProviderError, wf.ReviewPass, "art-review-pe"),
	})
	require.Nil(t, aerr)
	require.Equal(t, "open", episodeStatusOf(t, f.pool, f.auth.ID))
	_, aerr = StartWorkflowStep(ctx, f.pool, f.wi.ID, StartWorkflowStepRequest{
		AttemptID: f.attempt2, ClaimEpoch: 2, SessionSecret: "s3cret", StepID: "ship"})
	require.NotNil(t, aerr, "the ship must stay refused behind the open episode")
	require.Equal(t, ErrConflictStepInProgress, aerr.Code)

	// The authorized retry over the review role's failed attempt carries the
	// immutable parent lineage, and its replacement records the closing PASS.
	retryAuth, aerr := AuthorizeWorkflowRepair(ctx, f.pool, f.wi.ID, AuthorizeWorkflowRepairRequest{
		AttemptID: f.attempt2, ClaimEpoch: 2, SessionSecret: "s3cret",
		FailedStepAttemptID: fresh.StepAttemptID, Kind: "retry",
		Reason: "provider channel died on the fresh review; retry the final role",
	})
	require.Nil(t, aerr)
	require.Equal(t, f.auth.ID, retryAuth.ParentEpisodeID)
	repl := wfStartBound(t, ctx, f.pool, f.wi.ID, f.attempt2, "s3cret", 2, "review", retryAuth.ID)
	_, aerr = RecordWorkflowResult(ctx, f.pool, f.wi.ID, RecordWorkflowResultRequest{
		AttemptID: f.attempt2, ClaimEpoch: 2, SessionSecret: "s3cret",
		Result: wfStepResult(t, ctx, f.pool, repl, wf.StatusCompleted, wf.ReviewPass, "art-review-e"),
	})
	require.Nil(t, aerr)

	// THE CLOSE PROPAGATION: the recording's own authorization (the retry) is
	// closed, and the PARENT EPISODE closed in the same transaction — its
	// effective role set completed through the lineage. Without the
	// propagation this is exactly the leaked-open episode.
	require.Equal(t, "closed", episodeStatusOf(t, f.pool, retryAuth.ID))
	require.Equal(t, "closed", episodeStatusOf(t, f.pool, f.auth.ID),
		"the parent episode must close when the lineage-completing result is a chain replacement under a retry")

	// Behind the CLOSED episode the ordinary ship starts and closes: the
	// review step's latest result is the replacement's PASS, not the failed
	// original the direct-tag shape wedged on.
	shipStart := wfStart(t, ctx, f.pool, f.wi.ID, f.attempt2, "s3cret", 2, "ship")
	_, aerr = RecordWorkflowResult(ctx, f.pool, f.wi.ID, RecordWorkflowResultRequest{
		AttemptID: f.attempt2, ClaimEpoch: 2, SessionSecret: "s3cret",
		Result: wfStepResult(t, ctx, f.pool, shipStart, wf.StatusCompleted, "", "art-ship-e"),
	})
	require.Nil(t, aerr)

	// The failed history stays: the fresh review's provider_error row is
	// retained, append-only, next to the replacement's PASS.
	var failedRows int
	require.Nil(t, f.pool.QueryRow(ctx,
		`SELECT count(*) FROM wi_workflow_results WHERE step_attempt_id=$1 AND status='provider_error'`,
		fresh.StepAttemptID).Scan(&failedRows))
	require.Equal(t, 1, failedRows, "the failed role history must stay recorded, never rewritten away")
}

// ─── The Astra re-review blocker: the nullable replacement scan ───────────

// wfRetryLineageFixture is the pre-state both NULL-scan regressions diverge
// from: a generation run to a review FAIL, an episode recovering it, both
// repair producers bound, the first recording provider_error, and a retry
// authorized off that episode-bound failure carrying the parent lineage —
// with NO live replacement invocation under that retry yet.
//
// The flow is wfTwoProducerGateFlowSpec on purpose: two repair producers feed
// the failed gate, so the episode's repair role can hold one provider_errored
// invocation (the retry's lineage root) and one unrelated invocation whose
// recording is exactly the "unrelated episode result" the old NULL scan
// rolled back. fixb stays OPEN and unrecorded in the seed.
type wfRetryLineageFixture struct {
	wi       *WorkItem
	owner    string
	attempt2 string
	epID     string
	retryID  string
	fixaEp   *StartWorkflowStepResponse
	fixbEp   *StartWorkflowStepResponse
}

func seedEpisodeRetryLineage(t *testing.T, ctx context.Context, pool *pgxpool.Pool) *wfRetryLineageFixture {
	t.Helper()
	owner := wfUser(t, pool, "owner")
	project := wfProjectName(t)
	wfCleanup(t, pool, project, owner)
	t.Cleanup(func() { wfCleanup(t, pool, project, owner) })
	testSeedProject(t, pool, project, owner)
	caller := &UserRecord{ID: owner, Role: "writer"}

	fixa := wfSkill(t, ctx, pool, caller, "fix-a-"+skillSuffix(t),
		wfSchemasContract([]skillregistry.Capability{skillregistry.CapAuthoring}, nil, []string{"artifact"}))
	fixb := wfSkill(t, ctx, pool, caller, "fix-b-"+skillSuffix(t),
		wfSchemasContract([]skillregistry.Capability{skillregistry.CapAuthoring}, nil, []string{"artifact"}))
	review := wfSkill(t, ctx, pool, caller, "indep-review-"+skillSuffix(t),
		wfSchemasContract([]skillregistry.Capability{skillregistry.CapReview}, []string{"change_a", "change_b"}, []string{"approval"}))
	verify := wfSkill(t, ctx, pool, caller, "indep-verify-"+skillSuffix(t),
		wfSchemasContract([]skillregistry.Capability{skillregistry.CapVerification}, []string{"change"}, []string{"evidence"}))

	wi, aerr := wfCreate(t, ctx, pool, project, caller, owner, true,
		wfTwoProducerGateFlowSpec(fixa, fixb, review, verify))
	require.Nil(t, aerr)
	attempt := wfAttempt(t, pool, wi.ID, owner, "s3cret", 1)

	// The generation runs to a review FAIL: both producers record, the gate
	// fails, the attempt pauses (the FAIL→pause half of spec D8).
	for _, seed := range []struct{ stepID, artifact string }{
		{"fixa", "art-fixa-1"}, {"fixb", "art-fixb-1"},
	} {
		start := wfStart(t, ctx, pool, wi.ID, attempt, "s3cret", 1, seed.stepID)
		_, aerr = RecordWorkflowResult(ctx, pool, wi.ID, RecordWorkflowResultRequest{
			AttemptID: attempt, ClaimEpoch: 1, SessionSecret: "s3cret",
			Result: wfStepResult(t, ctx, pool, start, wf.StatusCompleted, "", seed.artifact),
		})
		require.Nil(t, aerr)
	}
	failedReview := wfStart(t, ctx, pool, wi.ID, attempt, "s3cret", 1, "review")
	_, aerr = RecordWorkflowResult(ctx, pool, wi.ID, RecordWorkflowResultRequest{
		AttemptID: attempt, ClaimEpoch: 1, SessionSecret: "s3cret",
		Result: wfStepResult(t, ctx, pool, failedReview, wf.StatusCompleted, wf.ReviewFail, "art-review-1"),
	})
	require.Nil(t, aerr)

	// The resumed attempt opens the episode and binds both producers — the
	// role with no predecessors may hold two at once.
	attempt2 := wfAttempt(t, pool, wi.ID, owner, "s3cret", 2)
	ep, aerr := AuthorizeWorkflowRepair(ctx, pool, wi.ID, AuthorizeWorkflowRepairRequest{
		AttemptID: attempt2, ClaimEpoch: 2, SessionSecret: "s3cret",
		FailedStepAttemptID: failedReview.StepAttemptID, Kind: "episode",
		Reason: "owner-authorized recovery from the failing review",
	})
	require.Nil(t, aerr)
	fixaEp := wfStartBound(t, ctx, pool, wi.ID, attempt2, "s3cret", 2, "fixa", ep.ID)
	fixbEp := wfStartBound(t, ctx, pool, wi.ID, attempt2, "s3cret", 2, "fixb", ep.ID)

	// The first producer records provider_error — the infrastructure failure
	// a retry exists for — still INSIDE the episode, so the retry authorized
	// off it carries the parent lineage. The second producer stays open.
	_, aerr = RecordWorkflowResult(ctx, pool, wi.ID, RecordWorkflowResultRequest{
		AttemptID: attempt2, ClaimEpoch: 2, SessionSecret: "s3cret",
		Result: wfStepResult(t, ctx, pool, fixaEp, wf.StatusProviderError, "", "art-fixa-pe"),
	})
	require.Nil(t, aerr)
	retry, aerr := AuthorizeWorkflowRepair(ctx, pool, wi.ID, AuthorizeWorkflowRepairRequest{
		AttemptID: attempt2, ClaimEpoch: 2, SessionSecret: "s3cret",
		FailedStepAttemptID: fixaEp.StepAttemptID, Kind: "retry",
		Reason: "provider channel died mid-episode; one bounded retry",
	})
	require.Nil(t, aerr)
	require.Equal(t, ep.ID, retry.ParentEpisodeID,
		"the retry must carry the episode lineage this fixture depends on")

	return &wfRetryLineageFixture{
		wi: wi, owner: owner, attempt2: attempt2, epID: ep.ID, retryID: retry.ID,
		fixaEp: fixaEp, fixbEp: fixbEp,
	}
}

// resultCountOf reads how many results one step attempt recorded (0 or 1 —
// the rollback detector).
func resultCountOf(t *testing.T, pool *pgxpool.Pool, stepAttemptID string) int {
	t.Helper()
	var n int
	require.Nil(t, pool.QueryRow(context.Background(),
		`SELECT count(*) FROM wi_workflow_results WHERE step_attempt_id=$1`, stepAttemptID).Scan(&n))
	return n
}

// TestWorkflowEpisodeRetryAuthorizedNotStartedStaysUnresolved is the first
// NULL-scan state: a retry whose parent_episode_id names the episode has NO
// replacement invocation yet. Recording the OTHER producer's episode result
// must land (the close predicate reads the lineage and must not die on the
// LEFT JOIN's NULL), the episode stays open, every dependent answers a typed
// wait or refusal, and the recovery path completes once the replacement
// records.
func TestWorkflowEpisodeRetryAuthorizedNotStartedStaysUnresolved(t *testing.T) {
	pool := setupWorkflowDB(t)
	ctx := context.Background()
	f := seedEpisodeRetryLineage(t, ctx, pool)

	// THE regression: the retry is authorized with no replacement under it,
	// and the unrelated second producer's completed result must RECORD. The
	// old scan read inv.step_attempt_id NULL into a plain string, classified
	// the scan error as a DB fault and rolled this whole transaction back.
	rec, aerr := RecordWorkflowResult(ctx, pool, f.wi.ID, RecordWorkflowResultRequest{
		AttemptID: f.attempt2, ClaimEpoch: 2, SessionSecret: "s3cret",
		Result: wfStepResult(t, ctx, pool, f.fixbEp, wf.StatusCompleted, "", "art-fixb-2"),
	})
	require.Nil(t, aerr,
		"an unrelated episode result must record while the authorized retry has not started, not die on a NULL scan in the retry lineage lookup")
	require.False(t, rec.Paused)
	require.Equal(t, 1, resultCountOf(t, pool, f.fixbEp.StepAttemptID), "the result must actually be recorded, not rolled back")
	require.Equal(t, "open", episodeStatusOf(t, pool, f.epID),
		"the episode stays open: the provider_errored role's chain has no live end yet")

	// The unresolved state answers WAITS, not errors: an ordinary start hits
	// the unresolved-recovery gate's typed refusal...
	_, aerr = StartWorkflowStep(ctx, pool, f.wi.ID, StartWorkflowStepRequest{
		AttemptID: f.attempt2, ClaimEpoch: 2, SessionSecret: "s3cret", StepID: "verify"})
	require.NotNil(t, aerr)
	require.Equal(t, ErrConflictStepInProgress, aerr.Code,
		"an ordinary start waits on the open episode, it does not error: %s", aerr.Message)
	require.Contains(t, aerr.Message, "open and unresolved")

	// ...and the episode's own verification role waits on its repair
	// producer — the chain ends at the provider_errored original, so the
	// role is incomplete, exactly as an open E-bound invocation is.
	_, aerr = StartWorkflowStep(ctx, pool, f.wi.ID, StartWorkflowStepRequest{
		AttemptID: f.attempt2, ClaimEpoch: 2, SessionSecret: "s3cret", StepID: "verify", RepairEpisodeID: f.epID})
	require.NotNil(t, aerr)
	require.Equal(t, ErrConflictStepInProgress, aerr.Code,
		"the verification role waits on the unresolved producer chain, it does not error: %s", aerr.Message)
	require.Contains(t, aerr.Message, "repair producer")

	// The recovery path is open, not wedged: the retry's replacement mints
	// under the authorization and records...
	replacement := wfStartBound(t, ctx, pool, f.wi.ID, f.attempt2, "s3cret", 2, "fixa", f.retryID)
	_, aerr = RecordWorkflowResult(ctx, pool, f.wi.ID, RecordWorkflowResultRequest{
		AttemptID: f.attempt2, ClaimEpoch: 2, SessionSecret: "s3cret",
		Result: wfStepResult(t, ctx, pool, replacement, wf.StatusCompleted, "", "art-fixa-2"),
	})
	require.Nil(t, aerr)
	require.Equal(t, "closed", episodeStatusOf(t, pool, f.retryID), "the retry closes on its recorded replacement")
	require.Equal(t, "open", episodeStatusOf(t, pool, f.epID), "the episode still waits for its verification and fresh review roles")

	// ...and the episode completes through the chain: the replacement's
	// result stands in for the provider_errored role.
	verif := wfStartBound(t, ctx, pool, f.wi.ID, f.attempt2, "s3cret", 2, "verify", f.epID)
	_, aerr = RecordWorkflowResult(ctx, pool, f.wi.ID, RecordWorkflowResultRequest{
		AttemptID: f.attempt2, ClaimEpoch: 2, SessionSecret: "s3cret",
		Result: wfStepResult(t, ctx, pool, verif, wf.StatusCompleted, "", "art-verify-2"),
	})
	require.Nil(t, aerr)
	fresh := wfStartBound(t, ctx, pool, f.wi.ID, f.attempt2, "s3cret", 2, "review", f.epID)
	_, aerr = RecordWorkflowResult(ctx, pool, f.wi.ID, RecordWorkflowResultRequest{
		AttemptID: f.attempt2, ClaimEpoch: 2, SessionSecret: "s3cret",
		Result: wfStepResult(t, ctx, pool, fresh, wf.StatusCompleted, wf.ReviewPass, "art-review-2"),
	})
	require.Nil(t, aerr)
	require.Equal(t, "closed", episodeStatusOf(t, pool, f.epID),
		"the complete role set — with the chain replacement standing in for the provider_errored producer — must close the episode")
}

// TestWorkflowEpisodeRetrySupersededReplacementStaysUnresolved is the second
// NULL-scan state: the retry's replacement invocation was minted, then its
// attempt was taken over and the controller reconcile fenced it, leaving the
// retry with no LIVE invocation again. Re-binding the unrelated producer,
// recording its result, every dependent wait, and the recovery itself must
// all answer their typed outcomes — never the internal NULL-scan error.
func TestWorkflowEpisodeRetrySupersededReplacementStaysUnresolved(t *testing.T) {
	pool := setupWorkflowDB(t)
	ctx := context.Background()
	f := seedEpisodeRetryLineage(t, ctx, pool)

	// The replacement starts under the retry, then its attempt is taken over
	// and the leftover open invocations are fenced by the explicit reconcile
	// transition (aihub#708 B3) — the retry's live set is empty again.
	replacement := wfStartBound(t, ctx, pool, f.wi.ID, f.attempt2, "s3cret", 2, "fixa", f.retryID)
	attempt3 := wfAttempt(t, pool, f.wi.ID, f.owner, "th1rd", 3)
	rec, aerr := ReconcileWorkflowInvocations(ctx, pool, f.wi.ID, ReconcileWorkflowInvocationsRequest{
		AttemptID: attempt3, ClaimEpoch: 3, SessionSecret: "th1rd", SupersedeAttemptID: f.attempt2,
	})
	require.Nil(t, aerr)
	require.Equal(t, 2, rec.Superseded,
		"the reconcile fences the retry's open replacement AND the still-open second producer")
	require.Equal(t, 0, resultCountOf(t, pool, replacement.StepAttemptID),
		"the fenced replacement never recorded; the retry's live set is empty")

	// THE regression, start-side: binding the unrelated second producer runs
	// the episode role prerequisites, which read the retry lineage — the
	// superseded replacement made inv.step_attempt_id NULL on the LEFT JOIN,
	// and the old scan answered an internal error here.
	fixbEp3 := wfStartBound(t, ctx, pool, f.wi.ID, attempt3, "th1rd", 3, "fixb", f.epID)

	// THE regression, record-side: the unrelated producer's completed result
	// must land — the close predicate reads the same lineage and must not
	// roll the transaction back on the NULL.
	recRes, aerr := RecordWorkflowResult(ctx, pool, f.wi.ID, RecordWorkflowResultRequest{
		AttemptID: attempt3, ClaimEpoch: 3, SessionSecret: "th1rd",
		Result: wfStepResult(t, ctx, pool, fixbEp3, wf.StatusCompleted, "", "art-fixb-3"),
	})
	require.Nil(t, aerr,
		"an unrelated episode result must record while the retry's replacement is superseded, not die on a NULL scan in the retry lineage lookup")
	require.False(t, recRes.Paused)
	require.Equal(t, 1, resultCountOf(t, pool, fixbEp3.StepAttemptID), "the result must actually be recorded, not rolled back")
	require.Equal(t, "open", episodeStatusOf(t, pool, f.epID),
		"the episode stays open: the fenced role's chain ends at its provider_errored original")

	// The unresolved state answers typed WAITS: the ordinary start gate, and
	// the episode's own verification role.
	_, aerr = StartWorkflowStep(ctx, pool, f.wi.ID, StartWorkflowStepRequest{
		AttemptID: attempt3, ClaimEpoch: 3, SessionSecret: "th1rd", StepID: "verify"})
	require.NotNil(t, aerr)
	require.Equal(t, ErrConflictStepInProgress, aerr.Code,
		"an ordinary start waits on the open episode, it does not error: %s", aerr.Message)
	require.Contains(t, aerr.Message, "open and unresolved")
	_, aerr = StartWorkflowStep(ctx, pool, f.wi.ID, StartWorkflowStepRequest{
		AttemptID: attempt3, ClaimEpoch: 3, SessionSecret: "th1rd", StepID: "verify", RepairEpisodeID: f.epID})
	require.NotNil(t, aerr)
	require.Equal(t, ErrConflictStepInProgress, aerr.Code,
		"the verification role waits on the unresolved producer chain, it does not error: %s", aerr.Message)
	require.Contains(t, aerr.Message, "repair producer")

	// The recovery path is open: the live attempt mints a NEW replacement
	// under the same retry authorization (the superseded one does not count
	// against its one-binding bound) and records...
	replacement3 := wfStartBound(t, ctx, pool, f.wi.ID, attempt3, "th1rd", 3, "fixa", f.retryID)
	require.NotEqual(t, replacement.StepAttemptID, replacement3.StepAttemptID)
	_, aerr = RecordWorkflowResult(ctx, pool, f.wi.ID, RecordWorkflowResultRequest{
		AttemptID: attempt3, ClaimEpoch: 3, SessionSecret: "th1rd",
		Result: wfStepResult(t, ctx, pool, replacement3, wf.StatusCompleted, "", "art-fixa-3"),
	})
	require.Nil(t, aerr)
	require.Equal(t, "closed", episodeStatusOf(t, pool, f.retryID), "the retry closes on its new replacement's recorded result")
	require.Equal(t, "open", episodeStatusOf(t, pool, f.epID), "the episode still waits for its verification and fresh review roles")

	// ...and the episode completes through the chain.
	verif := wfStartBound(t, ctx, pool, f.wi.ID, attempt3, "th1rd", 3, "verify", f.epID)
	_, aerr = RecordWorkflowResult(ctx, pool, f.wi.ID, RecordWorkflowResultRequest{
		AttemptID: attempt3, ClaimEpoch: 3, SessionSecret: "th1rd",
		Result: wfStepResult(t, ctx, pool, verif, wf.StatusCompleted, "", "art-verify-3"),
	})
	require.Nil(t, aerr)
	fresh := wfStartBound(t, ctx, pool, f.wi.ID, attempt3, "th1rd", 3, "review", f.epID)
	_, aerr = RecordWorkflowResult(ctx, pool, f.wi.ID, RecordWorkflowResultRequest{
		AttemptID: attempt3, ClaimEpoch: 3, SessionSecret: "th1rd",
		Result: wfStepResult(t, ctx, pool, fresh, wf.StatusCompleted, wf.ReviewPass, "art-review-3"),
	})
	require.Nil(t, aerr)
	require.Equal(t, "closed", episodeStatusOf(t, pool, f.epID),
		"the complete role set — with the reconciled chain's new replacement standing in for the fenced producer — must close the episode")
}
