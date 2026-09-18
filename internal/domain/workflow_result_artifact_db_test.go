package domain

// workflow_result_artifact_db_test.go — the real-DB regressions for the
// aihub#708 final Astra blocker 2: a COMPLETED workflow result's artifact
// triple must resolve to the actual methodology artifact it names, and every
// dishonest class refuses with nothing recorded.
//
// The classes held here, one per refusal (RecordWorkflowResult's
// resolveCompletedResultArtifact):
//
//	nonexistent            the artifact id is not a memories row at all
//	wrong work item        the artifact belongs to another work item
//	not a methodology row   an experience/rule/fact memory cannot back a result
//	redacted               a withdrawn artifact cannot back a completed result
//	wrong version          a memory id is ONE immutable version: never v2+
//	not accessible         the recording attempt's user cannot read the row
//	no structured output    no attrs.structured_payload means no verifiable digest
//	wrong digest           the stored structured output hashes to something else
//	schema-invalid         the structured output violates the pinned output
//	                       schema, WHERE the pinned version is still accessible
//
// And the two preserved behaviors the blocker explicitly names:
//
//	identity/fencing     every refusal is whole-transactional — the open
//	                     invocation stays open, no result row, no events
//	                     (asserted after the refusal matrix);
//	legacy no-flow       a work item with no workflow keeps its existing
//	                     "no result is expected" refusal, before any
//	                     artifact resolution could even run.
//
// The D3 arm lives in its own test below: when the pinned skill version went
// INACCESSIBLE after the invocation was authorized, the schema is still
// validated from an internal trusted lookup of the immutable pinned contract,
// while the next start remains refused.
//
// Gated exactly like workflow_db_test.go (setupWorkflowDB): skipped unless
// AIHUB_TEST_DB is set; migrations 0043 and 0044 are idempotent replays.
//
//	AIHUB_TEST_DB=postgres://postgres:testpass@localhost:5433/aihub_wf708?sslmode=disable \
//	GOWORK=off go test ./internal/domain/ -run TestWorkflowCompletedResult -count=1 -v

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"

	"github.com/GMISWE/ieops-aihub/internal/skillregistry"
	wf "github.com/GMISWE/ieops-aihub/internal/workflow"
)

// wfResultEnvelope builds one result envelope echoing a start descriptor with
// an EXPLICIT artifact triple — the shape the negative arms need, where
// wfStepResult's real-artifact minting would be exactly wrong (the arm is
// testing a fabricated, borrowed or tampered reference).
func wfResultEnvelope(start *StartWorkflowStepResponse, status wf.ResultStatus, verdict wf.ReviewVerdict, artifact wf.ArtifactRef) json.RawMessage {
	envelope := map[string]any{
		"status":          string(status),
		"work_item_id":    startWorkItemID(start),
		"flow_version":    start.StepsVersion,
		"step_id":         start.StepID,
		"step_attempt_id": start.StepAttemptID,
		"epoch":           start.ClaimEpoch,
		"producer_id":     start.ProducerID,
		"artifact":        map[string]any{"id": artifact.ID, "version": artifact.Version, "hash": artifact.Hash},
	}
	if verdict != "" {
		envelope["review_verdict"] = string(verdict)
	}
	if verdict != "" || strings.Contains(start.StepID, "verify") {
		envelope["evidence"] = []any{map[string]any{
			"kind": "test", "ref": "runs/" + artifact.ID, "hash": "sha256:" + wfHash("ev-"+artifact.ID),
		}}
	}
	raw, _ := json.Marshal(envelope)
	return raw
}

// wfRawArtifact inserts one memory row DIRECTLY, so the negative arms can
// manufacture exactly the wrong shape (wrong type, wrong binding, private to
// another user, no structured payload, redacted). attrs must be a JSON object.
func wfRawArtifact(t *testing.T, pool *pgxpool.Pool, id, project, author string, wiID *string, visibility, memType, content string, attrs json.RawMessage) {
	t.Helper()
	_, err := pool.Exec(context.Background(), `
		INSERT INTO memories (id, project, author_user_id, work_item_id, visibility, type, content, attrs, latest_id, status)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $1, 'active')`,
		id, project, author, wiID, visibility, memType, content, attrs)
	require.NoError(t, err)
}

// wfPayloadArtifact inserts a methodology artifact bound to the work item
// carrying an ARBITRARY structured payload (wfSeedArtifact's fixed payload is
// right for the shared fixtures but wrong for the schema arms), and returns
// the exact triple a result must echo for that payload.
func wfPayloadArtifact(t *testing.T, pool *pgxpool.Pool, wiID, project, author string, payload map[string]any) wf.ArtifactRef {
	t.Helper()
	id := NewID("mem")
	attrs, _ := json.Marshal(map[string]any{"structured_payload": payload})
	wfRawArtifact(t, pool, id, project, author, &wiID, "project", "methodology.execute", "artifact row", attrs)
	return wf.ArtifactRef{ID: id, Version: 1, Hash: workflowArtifactDigest(payload)}
}

// ─── The refusal matrix ───────────────────────────────────────────────────────

// TestWorkflowCompletedResultArtifactRefusals drives every dishonest
// artifact class against one open invocation, then proves the matrix left the
// invocation exactly as it was: a real artifact records, the fabricated ones
// never did.
func TestWorkflowCompletedResultArtifactRefusals(t *testing.T) {
	pool := setupWorkflowDB(t)
	ctx := context.Background()
	owner := wfUser(t, pool, "owner")
	stranger := wfUser(t, pool, "stranger")
	project := wfProjectName(t)
	wfCleanup(t, pool, project, owner, stranger)
	defer wfCleanup(t, pool, project, owner, stranger)
	testSeedProject(t, pool, project, owner)
	caller := &UserRecord{ID: owner, Role: "writer"}

	spec := wfSkill(t, ctx, pool, caller, "grill-me-"+skillSuffix(t), wfContract(skillregistry.CapAuthoring))
	review := wfSkill(t, ctx, pool, caller, "indep-review-"+skillSuffix(t), wfContract(skillregistry.CapReview))
	verify := wfSkill(t, ctx, pool, caller, "indep-verify-"+skillSuffix(t), wfContract(skillregistry.CapVerification))

	wi, aerr := wfCreate(t, ctx, pool, project, caller, owner, true, wfFlowSpec(spec, review, verify))
	require.Nil(t, aerr)
	attempt := wfAttempt(t, pool, wi.ID, owner, "s3cret", 1)
	start := wfStart(t, ctx, pool, wi.ID, attempt, "s3cret", 1, "spec")

	// A second work item with its own real, digest-correct artifact: the
	// wrong-binding arm borrows it.
	wi2, aerr := wfCreate(t, ctx, pool, project, caller, owner, true, wfFlowSpec(spec, review, verify))
	require.Nil(t, aerr)
	foreign := wfPayloadArtifact(t, pool, wi2.ID, project, owner, map[string]any{"summary": "wi2's artifact"})

	// A real artifact for THIS work item's open invocation: the wrong-version
	// and wrong-digest arms tamper with an otherwise-valid reference.
	real := wfSeedArtifact(t, ctx, pool, start.StepAttemptID, "real-spec")

	// Non-methodology, redacted, foreign-private and payload-less rows, all
	// manufactured in place.
	nonArtifact := NewID("mem")
	wfRawArtifact(t, pool, nonArtifact, project, owner, &wi.ID, "project", "experience.pitfall", "a memory, not an artifact", json.RawMessage(`{"structured_payload":{"summary":"x"}}`))
	redacted := wfPayloadArtifact(t, pool, wi.ID, project, owner, map[string]any{"summary": "soon withdrawn"})
	mustExec(t, pool, `UPDATE memories SET status='redacted', redacted_at=clock_timestamp(), redaction_reason='test' WHERE id='`+redacted.ID+`'`)
	privateForeign := NewID("mem")
	privateAttrs, _ := json.Marshal(map[string]any{"structured_payload": map[string]any{"summary": "not yours"}})
	wfRawArtifact(t, pool, privateForeign, project, stranger, &wi.ID, "private", "methodology.execute", "stranger's artifact", privateAttrs)
	noPayload := NewID("mem")
	wfRawArtifact(t, pool, noPayload, project, owner, &wi.ID, "project", "methodology.execute", "no structured output", json.RawMessage(`{}`))

	record := func(artifact wf.ArtifactRef) *AihubError {
		_, aerr := RecordWorkflowResult(ctx, pool, wi.ID, RecordWorkflowResultRequest{
			AttemptID: attempt, ClaimEpoch: 1, SessionSecret: "s3cret",
			Result: wfResultEnvelope(start, wf.StatusCompleted, "", artifact),
		})
		return aerr
	}

	// Nonexistent: no memories row carries this id at all.
	aerr = record(wf.ArtifactRef{ID: "mem_doesnotexist", Version: 1, Hash: "sha256:" + wfHash("mem_doesnotexist")})
	require.NotNil(t, aerr)
	require.Equal(t, ErrBadRequest, aerr.Code)
	require.Contains(t, aerr.Message, "does not exist")

	// The whole-transactional fence, asserted after the first refusal: the
	// invocation is still open and nothing was recorded.
	var invStatus string
	require.Nil(t, pool.QueryRow(ctx,
		`SELECT status FROM wi_workflow_invocations WHERE id=$1`, start.InvocationID).Scan(&invStatus))
	require.Equal(t, "open", invStatus, "a refused artifact must leave the invocation open and recoverable")
	var recorded int
	require.Nil(t, pool.QueryRow(ctx,
		`SELECT count(*) FROM wi_workflow_results WHERE work_item_id=$1`, wi.ID).Scan(&recorded))
	require.Equal(t, 0, recorded, "a refused artifact must record nothing")

	// Wrong work item: real artifact, wrong binding.
	aerr = record(foreign)
	require.NotNil(t, aerr)
	require.Equal(t, ErrBadRequest, aerr.Code)
	require.Contains(t, aerr.Message, wi2.ID, "the refusal must name the work item the artifact is bound to")

	// Not a methodology artifact.
	aerr = record(wf.ArtifactRef{ID: nonArtifact, Version: 1, Hash: "sha256:" + wfHash(nonArtifact)})
	require.NotNil(t, aerr)
	require.Equal(t, ErrBadRequest, aerr.Code)
	require.Contains(t, aerr.Message, "methodology")

	// Redacted.
	aerr = record(redacted)
	require.NotNil(t, aerr)
	require.Equal(t, ErrBadRequest, aerr.Code)
	require.Contains(t, aerr.Message, "redacted")

	// Wrong version: a memory id is one immutable version.
	tamperedVersion := real
	tamperedVersion.Version = 2
	aerr = record(tamperedVersion)
	require.NotNil(t, aerr)
	require.Equal(t, ErrBadRequest, aerr.Code)
	require.Contains(t, aerr.Message, "version")

	// Not accessible: another user's private artifact, digest-correct.
	aerr = record(wf.ArtifactRef{ID: privateForeign, Version: 1, Hash: workflowArtifactDigest(map[string]any{"summary": "not yours"})})
	require.NotNil(t, aerr)
	require.Equal(t, ErrForbidden, aerr.Code)
	require.Contains(t, aerr.Message, "not readable")

	// No structured output: no digest could ever be recomputed.
	aerr = record(wf.ArtifactRef{ID: noPayload, Version: 1, Hash: "sha256:" + wfHash(noPayload)})
	require.NotNil(t, aerr)
	require.Equal(t, ErrBadRequest, aerr.Code)
	require.Contains(t, aerr.Message, "structured output")

	// Wrong digest: the stored structured output hashes to something else.
	tamperedHash := real
	tamperedHash.Hash = "sha256:" + wfHash("tampered")
	aerr = record(tamperedHash)
	require.NotNil(t, aerr)
	require.Equal(t, ErrBadRequest, aerr.Code)
	require.Contains(t, aerr.Message, "does not match")

	// The matrix leaves the invocation untouched: the real artifact still
	// records on it, first try.
	rec, aerr := RecordWorkflowResult(ctx, pool, wi.ID, RecordWorkflowResultRequest{
		AttemptID: attempt, ClaimEpoch: 1, SessionSecret: "s3cret",
		Result: wfResultEnvelope(start, wf.StatusCompleted, "", real),
	})
	require.Nil(t, aerr, "the refusal matrix must not have wedged the invocation")
	require.False(t, rec.Paused)
	require.Nil(t, pool.QueryRow(ctx,
		`SELECT status FROM wi_workflow_invocations WHERE id=$1`, start.InvocationID).Scan(&invStatus))
	require.Equal(t, "recorded", invStatus)
}

// ─── The contract arm ─────────────────────────────────────────────────────────

// strictOutputContract is a producer contract whose output schema is closed
// and REQUIRED: the structured output must be exactly {"summary": <string>}.
func strictOutputContract() json.RawMessage {
	return json.RawMessage(`{"capabilities":["` + string(skillregistry.CapAuthoring) + `"],"runtime":{"interactive":false},
		"output_schema":{"type":"object","properties":{"summary":{"type":"string"}},"required":["summary"],"additionalProperties":false}}`)
}

// TestWorkflowCompletedResultContractViolationRefuses holds the schema arm:
// with the pinned version accessible, a completed result whose artifact's
// stored structured output violates the pinned output schema is refused with
// nothing recorded — and the same artifact with a compliant payload records.
func TestWorkflowCompletedResultContractViolationRefuses(t *testing.T) {
	pool := setupWorkflowDB(t)
	ctx := context.Background()
	owner := wfUser(t, pool, "owner")
	project := wfProjectName(t)
	wfCleanup(t, pool, project, owner)
	defer wfCleanup(t, pool, project, owner)
	testSeedProject(t, pool, project, owner)
	caller := &UserRecord{ID: owner, Role: "writer"}

	strict := wfSkill(t, ctx, pool, caller, "strict-spec-"+skillSuffix(t), strictOutputContract())
	review := wfSkill(t, ctx, pool, caller, "indep-review-"+skillSuffix(t), wfContract(skillregistry.CapReview))
	verify := wfSkill(t, ctx, pool, caller, "indep-verify-"+skillSuffix(t), wfContract(skillregistry.CapVerification))

	wi, aerr := wfCreate(t, ctx, pool, project, caller, owner, true, wfFlowSpec(strict, review, verify))
	require.Nil(t, aerr)
	attempt := wfAttempt(t, pool, wi.ID, owner, "s3cret", 1)
	start := wfStart(t, ctx, pool, wi.ID, attempt, "s3cret", 1, "spec")

	// Digest-correct, correctly bound, readable — and schema-INVALID: the
	// summary is a number where the pinned contract demands a string.
	violating := wfPayloadArtifact(t, pool, wi.ID, project, owner, map[string]any{"summary": 123})
	_, aerr = RecordWorkflowResult(ctx, pool, wi.ID, RecordWorkflowResultRequest{
		AttemptID: attempt, ClaimEpoch: 1, SessionSecret: "s3cret",
		Result: wfResultEnvelope(start, wf.StatusCompleted, "", violating),
	})
	require.NotNil(t, aerr)
	require.Equal(t, ErrBadRequest, aerr.Code)
	require.Contains(t, aerr.Message, "pinned output schema")

	// Nothing recorded: the invocation is still open.
	var invStatus string
	require.Nil(t, pool.QueryRow(ctx,
		`SELECT status FROM wi_workflow_invocations WHERE id=$1`, start.InvocationID).Scan(&invStatus))
	require.Equal(t, "open", invStatus)

	// A compliant payload on the same invocation records.
	compliant := wfPayloadArtifact(t, pool, wi.ID, project, owner, map[string]any{"summary": "the honest output"})
	rec, aerr := RecordWorkflowResult(ctx, pool, wi.ID, RecordWorkflowResultRequest{
		AttemptID: attempt, ClaimEpoch: 1, SessionSecret: "s3cret",
		Result: wfResultEnvelope(start, wf.StatusCompleted, "", compliant),
	})
	require.Nil(t, aerr)
	require.False(t, rec.Paused)
}

// TestWorkflowCompletedResultContractValidatedAfterRegistryAccessLapsed holds
// the D3 half of the contract arm: the pinned skill version went inaccessible
// to the attempt actor after invocation authorization. Result validation still
// uses the immutable pinned contract internally: schema-invalid output refuses,
// while a valid authorized result records. The next start remains refused.
func TestWorkflowCompletedResultContractValidatedAfterRegistryAccessLapsed(t *testing.T) {
	pool := setupWorkflowDB(t)
	ctx := context.Background()
	owner := wfUser(t, pool, "owner")
	skillOwner := wfUser(t, pool, "skillowner")
	project := wfProjectName(t)
	wfCleanup(t, pool, project, owner, skillOwner)
	defer wfCleanup(t, pool, project, owner, skillOwner)
	testSeedProject(t, pool, project, owner)
	caller := &UserRecord{ID: owner, Role: "writer"}
	skillCaller := &UserRecord{ID: skillOwner, Role: "writer"}

	// The pinned producer is ANOTHER user's skill, public at pin time.
	strict := wfSkill(t, ctx, pool, skillCaller, "foreign-strict-"+skillSuffix(t), strictOutputContract())
	review := wfSkill(t, ctx, pool, caller, "indep-review-"+skillSuffix(t), wfContract(skillregistry.CapReview))
	verify := wfSkill(t, ctx, pool, caller, "indep-verify-"+skillSuffix(t), wfContract(skillregistry.CapVerification))
	_, aerr := SetSkillVersionVisibility(ctx, pool, skillCaller, strict, 1, "public")
	require.Nil(t, aerr)

	wi, aerr := wfCreate(t, ctx, pool, project, caller, owner, true, wfFlowSpec(strict, review, verify))
	require.Nil(t, aerr)
	attempt := wfAttempt(t, pool, wi.ID, owner, "s3cret", 1)
	start := wfStart(t, ctx, pool, wi.ID, attempt, "s3cret", 1, "spec")

	// Revoke: the version goes private to its owner. The already-authorized
	// invocation keeps its right to finish (spec D3).
	_, aerr = SetSkillVersionVisibility(ctx, pool, skillCaller, strict, 1, "private")
	require.Nil(t, aerr)

	// The immutable pinned contract is still enforced after revocation.
	violating := wfPayloadArtifact(t, pool, wi.ID, project, owner, map[string]any{"summary": 123})
	_, aerr = RecordWorkflowResult(ctx, pool, wi.ID, RecordWorkflowResultRequest{
		AttemptID: attempt, ClaimEpoch: 1, SessionSecret: "s3cret",
		Result: wfResultEnvelope(start, wf.StatusCompleted, "", violating),
	})
	require.NotNil(t, aerr)
	require.Equal(t, ErrBadRequest, aerr.Code)
	require.Contains(t, aerr.Message, "pinned output schema")

	// The refusal leaves the invocation open, so a valid result from the
	// already-authorized invocation can still finish normally.
	compliant := wfPayloadArtifact(t, pool, wi.ID, project, owner, map[string]any{"summary": "authorized after revocation"})
	rec, aerr := RecordWorkflowResult(ctx, pool, wi.ID, RecordWorkflowResultRequest{
		AttemptID: attempt, ClaimEpoch: 1, SessionSecret: "s3cret",
		Result: wfResultEnvelope(start, wf.StatusCompleted, "", compliant),
	})
	require.Nil(t, aerr)
	require.False(t, rec.Paused)

	// The lapse still bites the NEXT start: it re-checks registry access and
	// refuses, without minting another invocation.
	_, aerr = StartWorkflowStep(ctx, pool, wi.ID, StartWorkflowStepRequest{
		AttemptID: attempt, ClaimEpoch: 1, SessionSecret: "s3cret", StepID: "review"})
	require.NotNil(t, aerr)
	require.Equal(t, ErrNotFound, aerr.Code)
	require.Contains(t, aerr.Message, "skill version not found")
}

// ─── Scope and legacy preservation ────────────────────────────────────────────

// TestWorkflowNonCompletedResultNeedsNoArtifact holds the completed-only scope:
// an honest provider_error carries a shape-valid but unresolvable artifact
// triple and still records — wedging a recoverable report on artifact
// resolution would make a provider failure unrecoverable, and a non-completed
// result advances nothing.
func TestWorkflowNonCompletedResultNeedsNoArtifact(t *testing.T) {
	pool := setupWorkflowDB(t)
	ctx := context.Background()
	owner := wfUser(t, pool, "owner")
	project := wfProjectName(t)
	wfCleanup(t, pool, project, owner)
	defer wfCleanup(t, pool, project, owner)
	testSeedProject(t, pool, project, owner)
	caller := &UserRecord{ID: owner, Role: "writer"}

	spec := wfSkill(t, ctx, pool, caller, "grill-me-"+skillSuffix(t), wfContract(skillregistry.CapAuthoring))
	review := wfSkill(t, ctx, pool, caller, "indep-review-"+skillSuffix(t), wfContract(skillregistry.CapReview))
	verify := wfSkill(t, ctx, pool, caller, "indep-verify-"+skillSuffix(t), wfContract(skillregistry.CapVerification))

	wi, aerr := wfCreate(t, ctx, pool, project, caller, owner, true, wfFlowSpec(spec, review, verify))
	require.Nil(t, aerr)
	attempt := wfAttempt(t, pool, wi.ID, owner, "s3cret", 1)
	start := wfStart(t, ctx, pool, wi.ID, attempt, "s3cret", 1, "spec")

	rec, aerr := RecordWorkflowResult(ctx, pool, wi.ID, RecordWorkflowResultRequest{
		AttemptID: attempt, ClaimEpoch: 1, SessionSecret: "s3cret",
		Result: wfResultEnvelope(start, wf.StatusProviderError, "",
			wf.ArtifactRef{ID: "mem_never_minted", Version: 1, Hash: "sha256:" + wfHash("mem_never_minted")}),
	})
	require.Nil(t, aerr, "a non-completed result is not held to artifact resolution")
	require.False(t, rec.Paused)
	require.Equal(t, string(wf.StatusProviderError), rec.Status)
}

// TestWorkflowResultLegacyNoFlowRefused pins the legacy half of the blocker's
// preservation clause: a work item with NO workflow keeps its existing
// refusal — no result is expected — answered before any artifact resolution
// could run, exactly as before.
func TestWorkflowResultLegacyNoFlowRefused(t *testing.T) {
	pool := setupWorkflowDB(t)
	ctx := context.Background()
	owner := wfUser(t, pool, "owner")
	project := wfProjectName(t)
	wfCleanup(t, pool, project, owner)
	defer wfCleanup(t, pool, project, owner)
	testSeedProject(t, pool, project, owner)

	wi, aerr := CreateWorkItem(ctx, pool, &CreateWorkItemRequest{
		Project: project,
		Goal:    "legacy no-flow result refusal " + wfHash(project)[:16],
		Source:  "human",
	}, owner, owner, nil, "writer")
	require.Nil(t, aerr)
	var stepsVersion int
	require.Nil(t, pool.QueryRow(ctx, `SELECT steps_version FROM work_items WHERE id=$1`, wi.ID).Scan(&stepsVersion))
	require.Equal(t, 0, stepsVersion)
	attempt := wfAttempt(t, pool, wi.ID, owner, "s3cret", 1)

	_, aerr = RecordWorkflowResult(ctx, pool, wi.ID, RecordWorkflowResultRequest{
		AttemptID: attempt, ClaimEpoch: 1, SessionSecret: "s3cret",
		Result: wfResultEnvelope(&StartWorkflowStepResponse{
			StepsVersion: 1, StepID: "spec", StepAttemptID: "sa_legacy", ProducerID: "wfp_legacy", ClaimEpoch: 1,
		}, wf.StatusCompleted, "", wf.ArtifactRef{ID: "mem_legacy", Version: 1, Hash: "sha256:" + wfHash("mem_legacy")}),
	})
	require.NotNil(t, aerr)
	require.Equal(t, ErrConflictStepAttemptMismatch, aerr.Code)
	require.Contains(t, aerr.Message, "no workflow")
}
