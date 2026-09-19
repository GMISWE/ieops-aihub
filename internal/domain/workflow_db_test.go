package domain

// workflow_db_test.go — the live-PostgreSQL half of the Batch 2A workflow
// tests: atomic pinning (create and revision), the CAS + no-revision-while-
// in-progress rules, explicit RHS classification, start fencing with the
// registry access recheck, stale-result refusals, review-FAIL pause (never
// terminal), the human-only approval bound to the exact artifact, repair
// authorizations, and the privacy rules (private versions never pin, workflow
// views never leak skill content).
//
// Gated exactly like the registry tests (setupSkillRegistryDB pattern):
// skipped unless AIHUB_TEST_DB is set. Migrations 0043 and 0044 are applied by
// the setup helper — both are idempotent, so replaying them against a database
// goose already migrated is a no-op.
//
//	AIHUB_TEST_DB=postgres://postgres:testpass@localhost:5433/aihub_skillcheck?sslmode=disable \
//	GOWORK=off go test ./internal/domain/ -run TestWorkflow -count=1 -v

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"

	"github.com/GMISWE/ieops-aihub/internal/skillregistry"
	wf "github.com/GMISWE/ieops-aihub/internal/workflow"
)

func setupWorkflowDB(t *testing.T) *pgxpool.Pool {
	t.Helper()
	pool := setupLatestTestDB(t)
	runMigration(t, pool, "0043_skill_registry.sql")
	runMigration(t, pool, "0044_wi_workflows.sql")
	return pool
}

// wfUser seeds one user with a t.Name()-derived id plus a suffix.
func wfUser(t *testing.T, pool *pgxpool.Pool, suffix string) string {
	t.Helper()
	uid := "u_" + testname_Sanitize(t.Name()) + "_" + suffix
	mustExec(t, pool, `INSERT INTO users(id,email,display_name,user_type) VALUES('`+uid+`','`+uid+`@test.local','`+uid+`','human')
		ON CONFLICT (id) DO NOTHING`)
	return uid
}

// testname_Sanitize is a tiny local alias so the helper above reads the same
// as the citest helper without importing it twice under different names.
var testname_Sanitize = sanitizeTestName

func sanitizeTestName(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			b.WriteRune(r)
		default:
			b.WriteRune('_')
		}
	}
	return b.String()
}

// wfProjectName derives a legal, unique project name from the test name:
// projects.name is CHECK-constrained to lowercase letters/digits/dash/
// underscore within 40 chars, so a raw t.Name() (uppercase, dots, and often
// longer than 40) cannot be used directly.
func wfProjectName(t *testing.T) string {
	return "wf_" + wfHash(t.Name())[:24]
}

// wfCleanup removes the project's whole work-item footprint so a re-run
// against the same database starts clean (the same reset discipline seedWI
// applies, extended to the workflow tables).
func wfCleanup(t *testing.T, pool *pgxpool.Pool, project string, users ...string) {
	t.Helper()
	userList := ""
	for i, u := range users {
		if i > 0 {
			userList += ","
		}
		userList += "'" + u + "'"
	}
	if userList != "" {
		mustExec(t, pool, `DELETE FROM skills WHERE owner_user_id IN (`+userList+`)`)
	}
	for _, stmt := range []string{
		// Memories FIRST: the workflow fixtures mint real methodology artifacts
		// bound to the work items (aihub#708 final Astra blocker 2's resolution
		// reads exactly these rows), and memories.work_item_id carries a plain
		// REFERENCES work_items(id) — no ON DELETE — so deleting the work items
		// while such a row points at one fails the FK and breaks the cleanup.
		`DELETE FROM memories WHERE work_item_id IN (SELECT id FROM work_items WHERE project=$1)`,
		`DELETE FROM wi_workflow_results WHERE work_item_id IN (SELECT id FROM work_items WHERE project=$1)`,
		`DELETE FROM wi_workflow_invocations WHERE work_item_id IN (SELECT id FROM work_items WHERE project=$1)`,
		`DELETE FROM wi_workflow_approvals WHERE work_item_id IN (SELECT id FROM work_items WHERE project=$1)`,
		`DELETE FROM wi_workflow_repair_episodes WHERE work_item_id IN (SELECT id FROM work_items WHERE project=$1)`,
		`DELETE FROM wi_workflow_generations WHERE work_item_id IN (SELECT id FROM work_items WHERE project=$1)`,
		`DELETE FROM wi_step_completions WHERE work_item_id IN (SELECT id FROM work_items WHERE project=$1)`,
		`DELETE FROM agent_events WHERE work_item_id IN (SELECT id FROM work_items WHERE project=$1)`,
		`UPDATE work_items SET current_attempt_id=NULL WHERE project=$1`,
		`DELETE FROM resource_locks WHERE owner_attempt_id IN (SELECT id FROM run_attempts WHERE work_item_id IN (SELECT id FROM work_items WHERE project=$1))`,
		`DELETE FROM run_attempts WHERE work_item_id IN (SELECT id FROM work_items WHERE project=$1)`,
		`DELETE FROM work_items WHERE project=$1`,
	} {
		_, err := pool.Exec(context.Background(), stmt, project)
		require.NoError(t, err)
	}
}

// wfContract returns a raw contract for one capability set.
func wfContract(caps ...skillregistry.Capability) json.RawMessage {
	list := make([]string, len(caps))
	for i, c := range caps {
		list[i] = string(c)
	}
	capsJSON, _ := json.Marshal(list)
	return json.RawMessage(`{"capabilities":` + string(capsJSON) + `,"runtime":{"interactive":false}}`)
}

// wfSchemasContract is wfContract plus named string input/output properties,
// so flows can wire named inputs between steps (the gate-flow shape an
// episode's repair producer needs: the gate consumes the producer's output).
func wfSchemasContract(caps []skillregistry.Capability, inputs, outputs []string) json.RawMessage {
	list := make([]string, len(caps))
	for i, c := range caps {
		list[i] = string(c)
	}
	props := func(names []string) string {
		p := make([]string, len(names))
		for i, n := range names {
			p[i] = fmt.Sprintf(`%q:{"type":"string"}`, n)
		}
		return strings.Join(p, ",")
	}
	out := `{"capabilities":["` + strings.Join(list, `","`) + `"],"runtime":{"interactive":false}`
	if len(inputs) > 0 {
		out += `,"input_schema":{"type":"object","properties":{` + props(inputs) + `}}`
	}
	if len(outputs) > 0 {
		out += `,"output_schema":{"type":"object","properties":{` + props(outputs) + `}}`
	}
	return json.RawMessage(out + `}`)
}

// wfBundle returns a canonical closed bundle with the given content.
func wfBundle(content string) json.RawMessage {
	return json.RawMessage(fmt.Sprintf(`{"entry":"SKILL.md","files":[{"path":"SKILL.md","content":%q}],"license":{"name":"MIT"}}`, content))
}

// wfSkill publishes a skill with the given contracts as successive versions
// and returns its id.
func wfSkill(t *testing.T, ctx context.Context, pool *pgxpool.Pool, caller *UserRecord, name string, contracts ...json.RawMessage) string {
	t.Helper()
	detail, aerr := CreateSkill(ctx, pool, caller, CreateSkillRequest{Name: name})
	require.Nil(t, aerr)
	for i, contract := range contracts {
		_, aerr := PublishSkillVersion(ctx, pool, caller, PublishSkillVersionRequest{
			SkillID:        detail.ID,
			ExpectedLatest: i,
			Bundle:         wfBundle(fmt.Sprintf("%s v%d", name, i+1)),
			Contract:       contract,
		})
		require.Nil(t, aerr)
	}
	return detail.ID
}

// wfModels is one valid model candidate.
func wfModels() []wf.ModelCandidate {
	return []wf.ModelCandidate{{Harness: "pi", Model: "gpt-test", Effort: "high"}}
}

// wfFlowSpec returns a minimal VALID composition: an authoring write followed
// by an independent review and an independent verification (the final write's
// required gates). skill_version 0 means "latest accessible".
func wfFlowSpec(spec, review, verify string) []WorkflowStepSpec {
	return []WorkflowStepSpec{
		{ID: "spec", SkillID: spec, SkillVersion: 0, Models: wfModels()},
		{ID: "review", SkillID: review, SkillVersion: 0, Models: wfModels()},
		{ID: "verify", SkillID: verify, SkillVersion: 0, Models: wfModels()},
	}
}

// wfGateFlowSpec is wfFlowSpec with the input wiring an episode's repair
// producer needs: the review and verification gates consume the spec step's
// output, so a repair of spec provably affects the gate that failed. The
// skills must be published with matching schemas (wfSchemasContract).
func wfGateFlowSpec(spec, review, verify string) []WorkflowStepSpec {
	return []WorkflowStepSpec{
		{ID: "spec", SkillID: spec, SkillVersion: 0, Models: wfModels()},
		{ID: "review", SkillID: review, SkillVersion: 0, Models: wfModels(),
			Inputs: []wf.InputRef{{Name: "change", StepID: "spec", Output: "artifact"}}},
		{ID: "verify", SkillID: verify, SkillVersion: 0, Models: wfModels(),
			Inputs: []wf.InputRef{{Name: "change", StepID: "spec", Output: "artifact"}}},
	}
}

// wfTwoProducerGateFlowSpec is wfGateFlowSpec with TWO producers feeding the
// review gate, so an episode's repair role can bind two invocations at once —
// the shape the episode role-order gate (aihub#708 re-review blocker 1) still
// allows to mint together, since neither producer has a predecessor. The
// review skill must declare change_a/change_b inputs (wfSchemasContract).
func wfTwoProducerGateFlowSpec(fixa, fixb, review, verify string) []WorkflowStepSpec {
	return []WorkflowStepSpec{
		{ID: "fixa", SkillID: fixa, SkillVersion: 0, Models: wfModels()},
		{ID: "fixb", SkillID: fixb, SkillVersion: 0, Models: wfModels()},
		{ID: "review", SkillID: review, SkillVersion: 0, Models: wfModels(),
			Inputs: []wf.InputRef{
				{Name: "change_a", StepID: "fixa", Output: "artifact"},
				{Name: "change_b", StepID: "fixb", Output: "artifact"},
			}},
		{ID: "verify", SkillID: verify, SkillVersion: 0, Models: wfModels(),
			Inputs: []wf.InputRef{{Name: "change", StepID: "fixa", Output: "artifact"}}},
	}
}

// wfHash returns a 64-hex sha256 of the seed (the digest body, no prefix).
func wfHash(seed string) string {
	sum := sha256.Sum256([]byte(seed))
	return hex.EncodeToString(sum[:])
}

// wfAttempt seeds a running attempt at the given epoch, superseding any prior
// attempt, and returns its id. The real resume path is FnClaimWorkItem; this
// is the same row shape for tests that only need fencing.
func wfAttempt(t *testing.T, pool *pgxpool.Pool, wiID, userID, secret string, epoch int64) string {
	t.Helper()
	attemptID := NewID("ra")
	_, err := pool.Exec(context.Background(), `
		INSERT INTO run_attempts (id, work_item_id, status, claim_epoch, idempotency_key,
			actor_user_id, actor_display, machine_id, session_secret_hash)
		VALUES ($1, $2, 'running', $3, $4, $5, $5, 'm_test', $6)`,
		attemptID, wiID, epoch, "idem_"+attemptID, userID, HashSecret(secret))
	require.NoError(t, err)
	_, err = pool.Exec(context.Background(), `
		UPDATE run_attempts SET status='superseded', ended_at=clock_timestamp()
		WHERE work_item_id=$1 AND id<>$2 AND status IN ('running','paused')`, wiID, attemptID)
	require.NoError(t, err)
	_, err = pool.Exec(context.Background(), `
		UPDATE work_items SET status='running', current_attempt_id=$1, current_attempt_epoch=$2 WHERE id=$3`,
		attemptID, epoch, wiID)
	require.NoError(t, err)
	return attemptID
}

// wfCreateNonce keeps every wfCreate goal unique: CreateWorkItem runs
// goal-similarity dedup against live work items, and two creates in one test
// with the same shape of goal would otherwise answer CONFLICT_CANDIDATES.
var wfCreateNonce int

// wfCreate pins a workflow at create time and returns the work item.
func wfCreate(t *testing.T, ctx context.Context, pool *pgxpool.Pool, project string,
	caller *UserRecord, userID string, rhs bool, steps []WorkflowStepSpec) (*WorkItem, *AihubError) {
	t.Helper()
	wfCreateNonce++
	return CreateWorkItem(ctx, pool, &CreateWorkItemRequest{
		Project:              project,
		Goal:                 fmt.Sprintf("workflow test %d %s", len(steps), wfHash(fmt.Sprintf("%s-%d", project, wfCreateNonce))[:20]),
		Source:               "human",
		RequiresHumanSession: &rhs,
		Steps:                steps,
		RegistryCaller:       caller,
	}, userID, userID, nil, "writer")
}

// wfStepResult builds one valid result envelope echoing a start descriptor,
// against the artifact model the server enforces (aihub#708 final Astra
// blocker 2): a COMPLETED result's artifact triple is minted from a REAL
// methodology memory bound to the work item — its structured output digested
// with the same sha256 the server recomputes at record time — while every
// non-completed status keeps the historical fabricated triple, because the
// server resolves artifacts on completed results only (an honest
// blocked/incomplete/provider_error advances nothing, and wedging a
// recoverable report on artifact resolution would make a provider failure
// unrecoverable).
func wfStepResult(t *testing.T, ctx context.Context, pool *pgxpool.Pool, start *StartWorkflowStepResponse,
	status wf.ResultStatus, verdict wf.ReviewVerdict, artifactTag string) json.RawMessage {
	t.Helper()
	artifact := wf.ArtifactRef{ID: artifactTag, Version: 1, Hash: "sha256:" + wfHash(artifactTag)}
	if status == wf.StatusCompleted {
		artifact = wfSeedArtifact(t, ctx, pool, start.StepAttemptID, artifactTag)
	}
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
			"kind": "test", "ref": "runs/" + artifactTag, "hash": "sha256:" + wfHash("ev-"+artifactTag),
		}}
	}
	raw, _ := json.Marshal(envelope)
	return raw
}

// wfSeedArtifact inserts a REAL methodology artifact — the row shape a
// completed workflow result's artifact triple must resolve to (aihub#708
// final Astra blocker 2): a memories row of type methodology.*, bound to the
// invocation's work item, authored by the recording attempt's actor,
// carrying the structured output the digest is computed over. Returns the
// exact triple a completed result must echo.
func wfSeedArtifact(t *testing.T, ctx context.Context, pool *pgxpool.Pool, stepAttemptID, tag string) wf.ArtifactRef {
	t.Helper()
	var wiID, project, author string
	require.Nil(t, pool.QueryRow(ctx, `
		SELECT w.id, w.project, ra.actor_user_id
		FROM work_items w
		JOIN wi_workflow_invocations i ON i.work_item_id = w.id AND i.step_attempt_id = $1
		JOIN run_attempts ra ON ra.id = i.run_attempt_id`,
		stepAttemptID).Scan(&wiID, &project, &author))
	id := NewID("mem")
	payload := map[string]any{"summary": "artifact " + tag}
	attrs, _ := json.Marshal(map[string]any{"structured_payload": payload})
	_, err := pool.Exec(ctx, `
		INSERT INTO memories (id, project, author_user_id, work_item_id, visibility, type, content, attrs, latest_id, status)
		VALUES ($1, $2, $3, $4, 'project', 'methodology.execute', $5, $6, $1, 'active')`,
		id, project, author, wiID, "artifact "+tag, attrs)
	require.NoError(t, err)
	return wf.ArtifactRef{ID: id, Version: 1, Hash: workflowArtifactDigest(payload)}
}

// wfLatestArtifactRef reads the exact artifact triple the step's latest
// recorded result carries — what a human approval must echo, now that the
// recorded triple names a real memory row.
func wfLatestArtifactRef(t *testing.T, pool *pgxpool.Pool, wiID, stepID string) wf.ArtifactRef {
	t.Helper()
	var artifact []byte
	require.Nil(t, pool.QueryRow(context.Background(), `
		SELECT artifact FROM wi_workflow_results
		WHERE work_item_id = $1 AND step_id = $2
		ORDER BY created_at DESC, id DESC LIMIT 1`, wiID, stepID).Scan(&artifact))
	var ref wf.ArtifactRef
	require.Nil(t, json.Unmarshal(artifact, &ref))
	return ref
}

// startWorkItemID returns the work item id of a started descriptor via the
// invocation row (kept as a helper so the envelope builder stays literal).
func startWorkItemID(start *StartWorkflowStepResponse) string {
	return startWorkIDs[start.StepAttemptID]
}

var startWorkIDs = map[string]string{}

// wfStart is StartWorkflowStep with the id bookkeeping the result builder
// needs (the response carries no work_item_id of its own).
func wfStart(t *testing.T, ctx context.Context, pool *pgxpool.Pool, wiID, attemptID, secret string, epoch int64, stepID string) *StartWorkflowStepResponse {
	t.Helper()
	start, aerr := StartWorkflowStep(ctx, pool, wiID, StartWorkflowStepRequest{
		AttemptID: attemptID, ClaimEpoch: epoch, SessionSecret: secret, StepID: stepID,
	})
	require.Nil(t, aerr)
	startWorkIDs[start.StepAttemptID] = wiID
	return start
}

// ─── Create-time atomic pin ──────────────────────────────────────────────────

// TestWorkflowCreatePinsAtomically holds the spec D4 create half: a valid flow
// is born pinned (generation 1, exact resolved versions, server-derived
// grants), an invalid composition leaves NO work item behind, and steps
// without an explicit requires_human_session are refused.
func TestWorkflowCreatePinsAtomically(t *testing.T) {
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

	// Valid create: pinned generation 1 with resolved versions and grants.
	wi, aerr := wfCreate(t, ctx, pool, project, caller, owner, true, wfFlowSpec(spec, review, verify))
	require.Nil(t, aerr)
	view, aerr := GetWorkItemWorkflow(ctx, pool, wi.ID)
	require.Nil(t, aerr)
	require.Equal(t, 1, view.StepsVersion)
	require.Len(t, view.Generations, 1)
	require.True(t, view.Generations[0].RequiresHumanSession)
	require.Len(t, view.Progress, 3)
	var flow wf.Flow
	require.Nil(t, json.Unmarshal(view.Steps, &flow))
	require.Equal(t, 1, flow.Steps[0].SkillVersion, "skill_version 0 must pin the caller's latest accessible version")
	// Grants frozen on the generation row, derived from capabilities.
	var grantsRaw []byte
	require.Nil(t, pool.QueryRow(ctx,
		`SELECT grants FROM wi_workflow_generations WHERE work_item_id=$1 AND steps_version=1`,
		wi.ID).Scan(&grantsRaw))
	var grants map[string]wf.StepGrant
	require.Nil(t, json.Unmarshal(grantsRaw, &grants))
	require.Equal(t, wf.StepGrant{Authority: wf.AuthorityWrite, ProducerIsolation: wf.IsolationShared}, grants["spec"])
	require.Equal(t, wf.StepGrant{Authority: wf.AuthorityReadOnly, ProducerIsolation: wf.IsolationRequired}, grants["review"])

	// Invalid composition (duplicate step ids): the WHOLE create is refused —
	// no work item, no generation.
	bad := wfFlowSpec(spec, review, verify)
	bad[1].ID = "spec"
	_, aerr = wfCreate(t, ctx, pool, project, caller, owner, true, bad)
	require.NotNil(t, aerr)
	// aihub#720 slice B: the create path's composition refusals are typed
	// COMPOSE_FAILED (with the reason in details), not the pin path's own
	// BAD_REQUEST — the code is what tells a composer its flow was invalid.
	require.Equal(t, ErrComposeFailed, aerr.Code)
	var n int
	require.Nil(t, pool.QueryRow(ctx,
		`SELECT count(*) FROM work_items WHERE project=$1 AND goal LIKE 'workflow test 3%'`, project).Scan(&n))
	require.Equal(t, 1, n, "the invalid create must leave no partial work item")

	// Missing a required gate is an invalid composition too.
	_, aerr = wfCreate(t, ctx, pool, project, caller, owner, true, []WorkflowStepSpec{
		{ID: "spec", SkillID: spec, SkillVersion: 0, Models: wfModels()},
	})
	require.NotNil(t, aerr)
	require.Equal(t, ErrComposeFailed, aerr.Code)
	require.Contains(t, aerr.Message, "review and verification")

	// RHS must be explicit when steps are supplied.
	rhs := true
	_, aerr = CreateWorkItem(ctx, pool, &CreateWorkItemRequest{
		Project: project, Goal: "workflow rhs test " + wfHash(project)[:16], Source: "human",
		Steps: wfFlowSpec(spec, review, verify), RegistryCaller: caller,
	}, owner, owner, nil, "writer")
	require.NotNil(t, aerr)
	// aihub#720 slice B: the classification guard moved above the transaction
	// and onto the COMPOSE_FAILED code.
	require.Equal(t, ErrComposeFailed, aerr.Code)
	require.Contains(t, aerr.Message, "requires_human_session")
	_ = rhs

	// Interactive-only step in an rhs=false flow is an invalid composition
	// (spec D5: interview-only steps cannot run unattended).
	interactive := wfSkill(t, ctx, pool, caller, "interview-"+skillSuffix(t), json.RawMessage(
		`{"capabilities":["authoring"],"runtime":{"interactive":true}}`))
	rhsFalse := false
	_, aerr = wfCreate(t, ctx, pool, project, caller, owner, false, []WorkflowStepSpec{
		{ID: "chat", SkillID: interactive, SkillVersion: 0, RHS: &rhsFalse, Models: wfModels()},
		{ID: "review", SkillID: review, SkillVersion: 0, Models: wfModels()},
		{ID: "verify", SkillID: verify, SkillVersion: 0, Models: wfModels()},
	})
	require.NotNil(t, aerr)
	require.Equal(t, ErrComposeFailed, aerr.Code)
	require.Contains(t, aerr.Message, "interactive-only")
}

// skillSuffix makes one test's skill names unique so re-runs against a shared
// database do not collide on the (owner, name) unique key — the same reasoning
// as wfProjectName, for the registry's identity instead of the project's.
func skillSuffix(t *testing.T) string { return wfHash(t.Name())[:10] }

// testSeedProject seeds one project owned by userID (the same row shape
// testProject produces, kept local so this file names its own fixture).
func testSeedProject(t *testing.T, pool *pgxpool.Pool, name, userID string) {
	t.Helper()
	mustExec(t, pool, `INSERT INTO projects(name, owner_user_id) VALUES('`+name+`','`+userID+`')
		ON CONFLICT (name) DO NOTHING`)
}

// ─── Revision: CAS, in-progress refusal, append-only history ────────────────

// TestWorkflowRevisionCASAndInProgress holds the PUT half of spec D4: the
// expected_steps_version CAS, the refusal while a worker is in progress, and
// the append-only generation history (an old generation is never overwritten).
func TestWorkflowRevisionCASAndInProgress(t *testing.T) {
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

	wi, aerr := CreateWorkItem(ctx, pool, &CreateWorkItemRequest{
		Project: project, Goal: "revision test " + wfHash(project)[:12] + "-1", Source: "human",
	}, owner, owner, nil, "writer")
	require.Nil(t, aerr)
	// The legacy zero shape: no steps, no generations, before the first pin.
	zeroView, aerr := GetWorkItemWorkflow(ctx, pool, wi.ID)
	require.Nil(t, aerr)
	require.Equal(t, 0, zeroView.StepsVersion)
	require.Nil(t, zeroView.Steps)
	require.Empty(t, zeroView.Generations)

	rhs := true
	// Stale CAS: the work item has no workflow yet, so expecting 1 is refused.
	_, aerr = UpdateWorkItemWorkflow(ctx, pool, wi.ID, caller, owner, "writer", nil,
		UpdateWorkItemWorkflowRequest{ExpectedStepsVersion: 1, RequiresHumanSession: &rhs, Steps: wfFlowSpec(spec, review, verify)})
	require.NotNil(t, aerr)
	require.Equal(t, ErrConflictCASFailed, aerr.Code)

	// Correct CAS pins generation 1.
	_, aerr = UpdateWorkItemWorkflow(ctx, pool, wi.ID, caller, owner, "writer", nil,
		UpdateWorkItemWorkflowRequest{ExpectedStepsVersion: 0, RequiresHumanSession: &rhs, Steps: wfFlowSpec(spec, review, verify)})
	require.Nil(t, aerr)

	// Revision to generation 2 succeeds and history stays append-only.
	rhs2 := false
	view, aerr := UpdateWorkItemWorkflow(ctx, pool, wi.ID, caller, owner, "writer", nil,
		UpdateWorkItemWorkflowRequest{ExpectedStepsVersion: 1, RequiresHumanSession: &rhs2, Steps: wfFlowSpec(spec, review, verify)})
	require.Nil(t, aerr)
	require.Equal(t, 2, view.StepsVersion)
	require.Len(t, view.Generations, 2)
	var gen1Digest string
	require.Nil(t, pool.QueryRow(ctx,
		`SELECT validation_digest FROM wi_workflow_generations WHERE work_item_id=$1 AND steps_version=1`,
		wi.ID).Scan(&gen1Digest))
	require.NotEmpty(t, gen1Digest, "generation 1 must stay readable after the revision")
	var wiRHS *bool
	require.Nil(t, pool.QueryRow(ctx,
		`SELECT requires_human_session FROM work_items WHERE id=$1`, wi.ID).Scan(&wiRHS))
	require.NotNil(t, wiRHS)
	require.False(t, *wiRHS, "the explicit revision classification must land on the work item")

	// While a worker is in progress, revision is refused (spec D4: reject
	// changes while a worker is in progress).
	wfAttempt(t, pool, wi.ID, owner, "s3cret", 1)
	_, aerr = UpdateWorkItemWorkflow(ctx, pool, wi.ID, caller, owner, "writer", nil,
		UpdateWorkItemWorkflowRequest{ExpectedStepsVersion: 2, RequiresHumanSession: &rhs, Steps: wfFlowSpec(spec, review, verify)})
	require.NotNil(t, aerr)
	require.Equal(t, ErrConflictWIAlreadyClaimed, aerr.Code)
}

// ─── Start: fencing, ordering, access recheck ────────────────────────────────

// TestWorkflowStartFencingAndAccessRecheck holds the start half: attempt
// credential fencing, one-open-invocation, definition order, and the registry
// access recheck (a pinned version the attempt actor cannot read blocks the
// next start — spec D3).
func TestWorkflowStartFencingAndAccessRecheck(t *testing.T) {
	pool := setupWorkflowDB(t)
	ctx := context.Background()
	owner := wfUser(t, pool, "owner")
	project := wfProjectName(t)
	wfCleanup(t, pool, project, owner)
	defer wfCleanup(t, pool, project, owner)
	testSeedProject(t, pool, project, owner)
	ownerRec := &UserRecord{ID: owner, Role: "writer"}

	spec := wfSkill(t, ctx, pool, ownerRec, "grill-me-"+skillSuffix(t), wfContract(skillregistry.CapAuthoring))
	review := wfSkill(t, ctx, pool, ownerRec, "indep-review-"+skillSuffix(t), wfContract(skillregistry.CapReview))
	verify := wfSkill(t, ctx, pool, ownerRec, "indep-verify-"+skillSuffix(t), wfContract(skillregistry.CapVerification))

	wi, aerr := wfCreate(t, ctx, pool, project, ownerRec, owner, true, wfFlowSpec(spec, review, verify))
	require.Nil(t, aerr)
	attempt := wfAttempt(t, pool, wi.ID, owner, "s3cret", 1)

	// Fencing: wrong secret, wrong epoch.
	_, aerr = StartWorkflowStep(ctx, pool, wi.ID, StartWorkflowStepRequest{
		AttemptID: attempt, ClaimEpoch: 1, SessionSecret: "wrong", StepID: "spec"})
	require.NotNil(t, aerr)
	require.Equal(t, ErrAttemptMismatch, aerr.Code)
	_, aerr = StartWorkflowStep(ctx, pool, wi.ID, StartWorkflowStepRequest{
		AttemptID: attempt, ClaimEpoch: 7, SessionSecret: "s3cret", StepID: "spec"})
	require.NotNil(t, aerr)
	require.Equal(t, ErrConflictEpochMismatch, aerr.Code)

	// Ordering: a later step cannot start first.
	_, aerr = StartWorkflowStep(ctx, pool, wi.ID, StartWorkflowStepRequest{
		AttemptID: attempt, ClaimEpoch: 1, SessionSecret: "s3cret", StepID: "review"})
	require.NotNil(t, aerr)
	require.Equal(t, ErrConflictStepInProgress, aerr.Code)

	start := wfStart(t, ctx, pool, wi.ID, attempt, "s3cret", 1, "spec")
	require.Equal(t, 1, start.StepsVersion)
	require.NotEmpty(t, start.StepAttemptID)
	require.NotEmpty(t, start.ProducerID)
	require.Equal(t, wf.StepGrant{Authority: wf.AuthorityWrite, ProducerIsolation: wf.IsolationShared}, start.Grant)
	require.True(t, start.EffectiveRHS == false || start.EffectiveRHS == true) // shape pinned below
	require.False(t, start.EffectiveRHS, "spec step has no rhs, so the effective gate is false")

	// One open invocation per step at a time.
	_, aerr = StartWorkflowStep(ctx, pool, wi.ID, StartWorkflowStepRequest{
		AttemptID: attempt, ClaimEpoch: 1, SessionSecret: "s3cret", StepID: "spec"})
	require.NotNil(t, aerr)
	require.Equal(t, ErrConflictStepInProgress, aerr.Code)

	// Access recheck: a flow pinned by the OWNER cannot be started by an
	// attempt whose actor cannot read the pinned versions. Re-do the fixture
	// with a stranger's attempt on a flow whose skills are private.
	stranger := wfUser(t, pool, "stranger")
	wi2, aerr := wfCreate(t, ctx, pool, project, ownerRec, owner, true, wfFlowSpec(spec, review, verify))
	require.Nil(t, aerr)
	strangerAttempt := wfAttempt(t, pool, wi2.ID, stranger, "str4nger", 1)
	_, aerr = StartWorkflowStep(ctx, pool, wi2.ID, StartWorkflowStepRequest{
		AttemptID: strangerAttempt, ClaimEpoch: 1, SessionSecret: "str4nger", StepID: "spec"})
	require.NotNil(t, aerr)
	require.Equal(t, ErrNotFound, aerr.Code,
		"a private version the attempt actor cannot read must block the start, answering like a missing one")
}

// ─── Result: stale refusals, valid recording, review-FAIL pause ─────────────

// TestWorkflowResultStaleRefusals holds the lifecycle Requirement: a stale
// result (superseded epoch/generation/attempt, unknown or recorded step
// attempt, forged producer) does not complete a step; the record preserves the
// reason and the recoverable state.
func TestWorkflowResultStaleRefusals(t *testing.T) {
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

	// Unknown step attempt.
	_, aerr = RecordWorkflowResult(ctx, pool, wi.ID, RecordWorkflowResultRequest{
		AttemptID: attempt, ClaimEpoch: 1, SessionSecret: "s3cret",
		Result: mutateJSON(wfStepResult(t, ctx, pool, start, wf.StatusCompleted, "", "art-x"), "step_attempt_id", "sa_forged"),
	})
	require.NotNil(t, aerr)
	require.Equal(t, ErrConflictStepAttemptMismatch, aerr.Code)

	// Forged producer identity.
	_, aerr = RecordWorkflowResult(ctx, pool, wi.ID, RecordWorkflowResultRequest{
		AttemptID: attempt, ClaimEpoch: 1, SessionSecret: "s3cret",
		Result: mutateJSON(wfStepResult(t, ctx, pool, start, wf.StatusCompleted, "", "art-x"), "producer_id", "wfp_forged"),
	})
	require.NotNil(t, aerr)
	require.Equal(t, ErrAttemptMismatch, aerr.Code)

	// Superseded generation.
	_, aerr = RecordWorkflowResult(ctx, pool, wi.ID, RecordWorkflowResultRequest{
		AttemptID: attempt, ClaimEpoch: 1, SessionSecret: "s3cret",
		Result: mutateJSON(wfStepResult(t, ctx, pool, start, wf.StatusCompleted, "", "art-x"), "flow_version", 99),
	})
	require.NotNil(t, aerr)
	require.Equal(t, ErrConflictStepAttemptMismatch, aerr.Code)

	// Another attempt's step attempt (identity belongs to the minting attempt).
	_, aerr = RecordWorkflowResult(ctx, pool, wi.ID, RecordWorkflowResultRequest{
		AttemptID: attempt, ClaimEpoch: 1, SessionSecret: "s3cret",
		Result: mutateJSON(wfStepResult(t, ctx, pool, start, wf.StatusCompleted, "", "art-x"), "epoch", 9),
	})
	require.NotNil(t, aerr)
	require.Equal(t, ErrConflictEpochMismatch, aerr.Code)

	// A worker result that tries to carry an approval is refused outright
	// (wf.StepResult.UnmarshalJSON's escalation guard).
	_, aerr = RecordWorkflowResult(ctx, pool, wi.ID, RecordWorkflowResultRequest{
		AttemptID: attempt, ClaimEpoch: 1, SessionSecret: "s3cret",
		Result: rawExtend(wfStepResult(t, ctx, pool, start, wf.StatusCompleted, "", "art-x"), `"approval":{"decision":"approved"}`),
	})
	require.NotNil(t, aerr)
	require.Equal(t, ErrBadRequest, aerr.Code)

	// Digests are DECODED, not length-checked: a 64-character suffix that is
	// not lowercase hex is refused on the artifact (aihub#708 review blocker:
	// "digest suffix not hex").
	_, aerr = RecordWorkflowResult(ctx, pool, wi.ID, RecordWorkflowResultRequest{
		AttemptID: attempt, ClaimEpoch: 1, SessionSecret: "s3cret",
		Result: mutateJSON(wfStepResult(t, ctx, pool, start, wf.StatusCompleted, "", "art-hex"), "artifact",
			map[string]any{"id": "art-hex", "version": 1, "hash": "sha256:" + strings.Repeat("z", 64)}),
	})
	require.NotNil(t, aerr)
	require.Equal(t, ErrBadRequest, aerr.Code)
	require.Contains(t, aerr.Message, "digest")

	// The same check holds for every evidence hash.
	_, aerr = RecordWorkflowResult(ctx, pool, wi.ID, RecordWorkflowResultRequest{
		AttemptID: attempt, ClaimEpoch: 1, SessionSecret: "s3cret",
		Result: mutateJSON(wfStepResult(t, ctx, pool, start, wf.StatusCompleted, "", "art-hex"), "evidence",
			[]any{map[string]any{"kind": "test", "ref": "runs/x", "hash": "sha256:" + strings.Repeat("g", 64)}}),
	})
	require.NotNil(t, aerr)
	require.Equal(t, ErrBadRequest, aerr.Code)
	require.Contains(t, aerr.Message, "digest")

	// Valid result: recorded in the WORKFLOW's own tables only, with events,
	// the invocation closed — and NOTHING in the legacy wi_step_completions,
	// whose readers filter by work_item_id alone and would read a superseded
	// generation's rows as current scenario history (isolation fix for the
	// aihub#708 review blocker "legacy history lacks flow identity").
	rec, aerr := RecordWorkflowResult(ctx, pool, wi.ID, RecordWorkflowResultRequest{
		AttemptID: attempt, ClaimEpoch: 1, SessionSecret: "s3cret",
		Result: wfStepResult(t, ctx, pool, start, wf.StatusCompleted, "", "art-1"),
	})
	require.Nil(t, aerr)
	require.False(t, rec.Paused)
	var legacyRows int
	require.Nil(t, pool.QueryRow(ctx,
		`SELECT count(*) FROM wi_step_completions WHERE work_item_id=$1`, wi.ID).Scan(&legacyRows))
	require.Equal(t, 0, legacyRows, "workflow results must stay out of the legacy step history")
	var invocationStatus string
	require.Nil(t, pool.QueryRow(ctx,
		`SELECT status FROM wi_workflow_invocations WHERE id=$1`, start.InvocationID).Scan(&invocationStatus))
	require.Equal(t, "recorded", invocationStatus)
	var eventTypeCount int
	require.Nil(t, pool.QueryRow(ctx,
		`SELECT count(*) FROM agent_events WHERE work_item_id=$1 AND event_type='step_completed' AND payload->>'step_attempt_id'=$2`,
		wi.ID, start.StepAttemptID).Scan(&eventTypeCount))
	require.Equal(t, 1, eventTypeCount)

	// The same step attempt cannot be recorded twice.
	_, aerr = RecordWorkflowResult(ctx, pool, wi.ID, RecordWorkflowResultRequest{
		AttemptID: attempt, ClaimEpoch: 1, SessionSecret: "s3cret",
		Result: wfStepResult(t, ctx, pool, start, wf.StatusCompleted, "", "art-1b"),
	})
	require.NotNil(t, aerr)
	require.Equal(t, ErrConflictStepAttemptMismatch, aerr.Code)

	// Review results must carry a verdict (400, nothing recorded).
	startReview := wfStart(t, ctx, pool, wi.ID, attempt, "s3cret", 1, "review")
	_, aerr = RecordWorkflowResult(ctx, pool, wi.ID, RecordWorkflowResultRequest{
		AttemptID: attempt, ClaimEpoch: 1, SessionSecret: "s3cret",
		Result: wfStepResult(t, ctx, pool, startReview, wf.StatusCompleted, "", "art-r"),
	})
	require.NotNil(t, aerr)
	require.Equal(t, ErrBadRequest, aerr.Code)
	require.Contains(t, aerr.Message, "verdict")
}

// mutateJSON returns a copy of a raw JSON envelope with one top-level field
// replaced by a string value.
func mutateJSON(raw json.RawMessage, field string, value any) json.RawMessage {
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		panic(err)
	}
	m[field] = value
	out, _ := json.Marshal(m)
	return out
}

// rawExtend splices one more key into a raw JSON object.
func rawExtend(raw json.RawMessage, extra string) json.RawMessage {
	s := strings.TrimRight(string(raw), "}")
	return json.RawMessage(s + "," + extra + "}")
}

// TestWorkflowReviewFailPausesNotTerminal holds spec D8: a reviewer FAIL
// records evidence and PAUSES the work item — the report and changes remain,
// recovery is an authorized repair, and the work item is never terminally
// failed by a review.
func TestWorkflowReviewFailPausesNotTerminal(t *testing.T) {
	pool := setupWorkflowDB(t)
	ctx := context.Background()
	owner := wfUser(t, pool, "owner")
	project := wfProjectName(t)
	wfCleanup(t, pool, project, owner)
	defer wfCleanup(t, pool, project, owner)
	testSeedProject(t, pool, project, owner)
	caller := &UserRecord{ID: owner, Role: "writer"}

	spec := wfSkill(t, ctx, pool, caller, "grill-me-"+skillSuffix(t),
		wfSchemasContract([]skillregistry.Capability{skillregistry.CapAuthoring}, nil, []string{"artifact"}))
	review := wfSkill(t, ctx, pool, caller, "indep-review-"+skillSuffix(t),
		wfSchemasContract([]skillregistry.Capability{skillregistry.CapReview}, []string{"change"}, []string{"approval"}))
	verify := wfSkill(t, ctx, pool, caller, "indep-verify-"+skillSuffix(t),
		wfSchemasContract([]skillregistry.Capability{skillregistry.CapVerification}, []string{"change"}, []string{"evidence"}))

	// The gate flow: the review gate consumes the spec step's output, so spec
	// is the repair producer an episode can bind (wfGateFlowSpec).
	wi, aerr := wfCreate(t, ctx, pool, project, caller, owner, true, wfGateFlowSpec(spec, review, verify))
	require.Nil(t, aerr)
	attempt := wfAttempt(t, pool, wi.ID, owner, "s3cret", 1)

	specStart := wfStart(t, ctx, pool, wi.ID, attempt, "s3cret", 1, "spec")
	_, aerr = RecordWorkflowResult(ctx, pool, wi.ID, RecordWorkflowResultRequest{
		AttemptID: attempt, ClaimEpoch: 1, SessionSecret: "s3cret",
		Result: wfStepResult(t, ctx, pool, specStart, wf.StatusCompleted, "", "art-spec"),
	})
	require.Nil(t, aerr)

	reviewStart := wfStart(t, ctx, pool, wi.ID, attempt, "s3cret", 1, "review")
	rec, aerr := RecordWorkflowResult(ctx, pool, wi.ID, RecordWorkflowResultRequest{
		AttemptID: attempt, ClaimEpoch: 1, SessionSecret: "s3cret",
		Result: wfStepResult(t, ctx, pool, reviewStart, wf.StatusCompleted, wf.ReviewFail, "art-review"),
	})
	require.Nil(t, aerr)
	require.True(t, rec.Paused)

	var wiStatus, attemptStatus string
	require.Nil(t, pool.QueryRow(ctx, `SELECT status FROM work_items WHERE id=$1`, wi.ID).Scan(&wiStatus))
	require.Equal(t, "paused", wiStatus)
	require.Nil(t, pool.QueryRow(ctx, `SELECT status FROM run_attempts WHERE id=$1`, attempt).Scan(&attemptStatus))
	require.Equal(t, "paused", attemptStatus)
	require.NotEqual(t, "failed", wiStatus, "a review FAIL must never terminally fail the work item")

	// The paused attempt's credentials are dead: further results are refused.
	_, aerr = RecordWorkflowResult(ctx, pool, wi.ID, RecordWorkflowResultRequest{
		AttemptID: attempt, ClaimEpoch: 1, SessionSecret: "s3cret",
		Result: wfStepResult(t, ctx, pool, reviewStart, wf.StatusCompleted, wf.ReviewPass, "art-review2"),
	})
	require.NotNil(t, aerr)

	// Recovery: the resumed attempt opens an episode. The episode closes ONLY
	// when all three roles — repair producer, fresh verification, fresh
	// review — have recorded completed results in episode order; the close
	// used to fire after the FIRST bound invocation recorded, un-binding the
	// remaining roles (aihub#708 review blocker).
	attempt2 := wfAttempt(t, pool, wi.ID, owner, "s3cret", 2)
	auth, aerr := AuthorizeWorkflowRepair(ctx, pool, wi.ID, AuthorizeWorkflowRepairRequest{
		AttemptID: attempt2, ClaimEpoch: 2, SessionSecret: "s3cret",
		FailedStepAttemptID: reviewStart.StepAttemptID, Kind: "episode",
		Reason: "owner-authorized recovery from the failing review",
	})
	require.Nil(t, aerr)
	require.Equal(t, "open", auth.Status)

	episodeStatus := func() string {
		var status string
		require.Nil(t, pool.QueryRow(ctx,
			`SELECT status FROM wi_workflow_repair_episodes WHERE id=$1`, auth.ID).Scan(&status))
		return status
	}

	// The repair producer role: the spec step feeds the failed gate's inputs.
	repair := wfStartBound(t, ctx, pool, wi.ID, attempt2, "s3cret", 2, "spec", auth.ID)
	rec, aerr = RecordWorkflowResult(ctx, pool, wi.ID, RecordWorkflowResultRequest{
		AttemptID: attempt2, ClaimEpoch: 2, SessionSecret: "s3cret",
		Result: wfStepResult(t, ctx, pool, repair, wf.StatusCompleted, "", "art-spec-2"),
	})
	require.Nil(t, aerr)
	require.False(t, rec.Paused)
	require.Equal(t, "open", episodeStatus(), "one recorded role must not close the episode")

	// Each role binds exactly one invocation: a second binding of the same
	// step under the same episode is refused.
	_, aerr = StartWorkflowStep(ctx, pool, wi.ID, StartWorkflowStepRequest{
		AttemptID: attempt2, ClaimEpoch: 2, SessionSecret: "s3cret", StepID: "spec", RepairEpisodeID: auth.ID,
	})
	require.NotNil(t, aerr)
	require.Equal(t, ErrConflictDuplicate, aerr.Code)

	// The verification role.
	verif := wfStartBound(t, ctx, pool, wi.ID, attempt2, "s3cret", 2, "verify", auth.ID)
	rec, aerr = RecordWorkflowResult(ctx, pool, wi.ID, RecordWorkflowResultRequest{
		AttemptID: attempt2, ClaimEpoch: 2, SessionSecret: "s3cret",
		Result: wfStepResult(t, ctx, pool, verif, wf.StatusCompleted, "", "art-verify"),
	})
	require.Nil(t, aerr)
	require.False(t, rec.Paused)
	require.Equal(t, "open", episodeStatus(), "two recorded roles must not close the episode")

	// The fresh review role: only the complete role set closes the episode.
	fresh := wfStartBound(t, ctx, pool, wi.ID, attempt2, "s3cret", 2, "review", auth.ID)
	rec, aerr = RecordWorkflowResult(ctx, pool, wi.ID, RecordWorkflowResultRequest{
		AttemptID: attempt2, ClaimEpoch: 2, SessionSecret: "s3cret",
		Result: wfStepResult(t, ctx, pool, fresh, wf.StatusCompleted, wf.ReviewPass, "art-review2"),
	})
	require.Nil(t, aerr)
	require.False(t, rec.Paused)
	require.Equal(t, "closed", episodeStatus(), "the complete role set closes the episode")
}

// wfStartBound is wfStart with a repair binding.
func wfStartBound(t *testing.T, ctx context.Context, pool *pgxpool.Pool, wiID, attemptID, secret string, epoch int64, stepID, repairID string) *StartWorkflowStepResponse {
	t.Helper()
	start, aerr := StartWorkflowStep(ctx, pool, wiID, StartWorkflowStepRequest{
		AttemptID: attemptID, ClaimEpoch: epoch, SessionSecret: secret, StepID: stepID, RepairEpisodeID: repairID,
	})
	require.Nil(t, aerr)
	startWorkIDs[start.StepAttemptID] = wiID
	return start
}

// ─── Approval: human-only, exact artifact binding ────────────────────────────

// TestWorkflowApprovalHumanOnlyExactArtifact holds spec D7: approval is a
// distinct authenticated action by a HUMAN, bound to the exact artifact
// id/version/hash and flow/step identity, with no worker-forged actor.
func TestWorkflowApprovalHumanOnlyExactArtifact(t *testing.T) {
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

	// The gating step: WI rhs=true AND step rhs=true.
	steps := wfFlowSpec(spec, review, verify)
	steps[0].RHS = &[]bool{true}[0]
	wi, aerr := wfCreate(t, ctx, pool, project, caller, owner, true, steps)
	require.Nil(t, aerr)
	attempt := wfAttempt(t, pool, wi.ID, owner, "s3cret", 1)
	start := wfStart(t, ctx, pool, wi.ID, attempt, "s3cret", 1, "spec")
	_, aerr = RecordWorkflowResult(ctx, pool, wi.ID, RecordWorkflowResultRequest{
		AttemptID: attempt, ClaimEpoch: 1, SessionSecret: "s3cret",
		Result: wfStepResult(t, ctx, pool, start, wf.StatusCompleted, "", "art-spec"),
	})
	require.Nil(t, aerr)

	// The exact triple the latest recorded result carries (now a real memory
	// row): what the human approval must echo.
	artifact := wfLatestArtifactRef(t, pool, wi.ID, "spec")

	// A machine user cannot approve, whatever its role.
	_, aerr = ApproveWorkflowStep(ctx, pool, wi.ID, caller, owner, "machine",
		ApproveWorkflowRequest{StepsVersion: 1, StepID: "spec", Artifact: artifact, Decision: "approved"})
	require.NotNil(t, aerr)
	require.Equal(t, ErrForbidden, aerr.Code)

	// A non-gating step is refused rather than stored as a no-op.
	_, aerr = ApproveWorkflowStep(ctx, pool, wi.ID, caller, owner, "human",
		ApproveWorkflowRequest{StepsVersion: 1, StepID: "review", Artifact: artifact, Decision: "approved"})
	require.NotNil(t, aerr)
	require.Equal(t, ErrBadRequest, aerr.Code)

	// The artifact triple must match the latest result exactly.
	wrong := artifact
	wrong.Hash = "sha256:" + wfHash("not-the-artifact")
	_, aerr = ApproveWorkflowStep(ctx, pool, wi.ID, caller, owner, "human",
		ApproveWorkflowRequest{StepsVersion: 1, StepID: "spec", Artifact: wrong, Decision: "approved"})
	require.NotNil(t, aerr)
	require.Equal(t, ErrConflictStepAttemptMismatch, aerr.Code)

	// Approvals do not carry across generations.
	_, aerr = ApproveWorkflowStep(ctx, pool, wi.ID, caller, owner, "human",
		ApproveWorkflowRequest{StepsVersion: 2, StepID: "spec", Artifact: artifact, Decision: "approved"})
	require.NotNil(t, aerr)
	require.Equal(t, ErrConflictStepAttemptMismatch, aerr.Code)

	// Valid: recorded with the authenticated principal as the actor.
	approval, aerr := ApproveWorkflowStep(ctx, pool, wi.ID, caller, owner, "human",
		ApproveWorkflowRequest{StepsVersion: 1, StepID: "spec", Artifact: artifact, Decision: "approved"})
	require.Nil(t, aerr)
	require.Equal(t, owner, approval.ActorUserID)

	// Idempotent: the same decision on the same triple returns the row.
	again, aerr := ApproveWorkflowStep(ctx, pool, wi.ID, caller, owner, "human",
		ApproveWorkflowRequest{StepsVersion: 1, StepID: "spec", Artifact: artifact, Decision: "approved"})
	require.Nil(t, aerr)
	require.Equal(t, approval.ID, again.ID)

	// A conflicting decision on the same triple is refused.
	_, aerr = ApproveWorkflowStep(ctx, pool, wi.ID, caller, owner, "human",
		ApproveWorkflowRequest{StepsVersion: 1, StepID: "spec", Artifact: artifact, Decision: "rejected"})
	require.NotNil(t, aerr)
	require.Equal(t, ErrConflictDuplicate, aerr.Code)

	// The DB fence behind that 409: the artifact identity is unique EXCLUDING
	// the decision, so not even a direct write can record a contradictory
	// decision for one artifact (aihub#708 review blocker: the old key
	// included `decision`, letting an approved and a rejected row coexist).
	_, execErr := pool.Exec(ctx, `
		INSERT INTO wi_workflow_approvals
		    (id, work_item_id, steps_version, step_id, artifact_id, artifact_version, artifact_hash, decision, actor_user_id, actor_display)
		VALUES ($1, $2, 1, 'spec', $3, 1, $4, 'rejected', $5, $5)`,
		NewID("wapp"), wi.ID, artifact.ID, artifact.Hash, owner)
	var pgErr *pgconn.PgError
	require.ErrorAs(t, execErr, &pgErr)
	require.Equal(t, "23505", pgErr.Code)

	// The view exposes the approval for exactly that artifact.
	view, aerr := GetWorkItemWorkflow(ctx, pool, wi.ID)
	require.Nil(t, aerr)
	var specProgress *WorkflowStepProgress
	for i := range view.Progress {
		if view.Progress[i].StepID == "spec" {
			specProgress = &view.Progress[i]
		}
	}
	require.NotNil(t, specProgress)
	require.NotNil(t, specProgress.Approved)
	require.True(t, *specProgress.Approved)
	require.True(t, specProgress.RHS)
}

// ─── Privacy: private versions never pin, views never leak content ─────────

// TestWorkflowPrivacyHoldsLatestAccessiblePin holds the registry Requirement's
// workflow half: pinning resolves the caller's latest ACCESSIBLE version (a
// private newer version is invisible), an inaccessible ref answers like a
// missing one, and the workflow view carries references only — no bundle or
// contract bytes anywhere in it.
func TestWorkflowPrivacyHoldsLatestAccessiblePin(t *testing.T) {
	pool := setupWorkflowDB(t)
	ctx := context.Background()
	owner := wfUser(t, pool, "owner")
	member := wfUser(t, pool, "member")
	project := wfProjectName(t)
	wfCleanup(t, pool, project, owner)
	defer wfCleanup(t, pool, project, owner)
	testSeedProject(t, pool, project, owner)
	mustExec(t, pool, `UPDATE projects SET members='[{"user_id":"`+member+`","role":"writer"}]'::jsonb WHERE name='`+project+`'`)

	ownerRec := &UserRecord{ID: owner, Role: "writer"}
	memberRec := &UserRecord{ID: member, Role: "writer"}

	// v1 shared with the project; v2 stays private.
	review := wfSkill(t, ctx, pool, ownerRec, "indep-review-"+skillSuffix(t), wfContract(skillregistry.CapReview))
	verify := wfSkill(t, ctx, pool, ownerRec, "indep-verify-"+skillSuffix(t), wfContract(skillregistry.CapVerification))
	specSkill := wfSkill(t, ctx, pool, ownerRec, "grill-me-"+skillSuffix(t), wfContract(skillregistry.CapAuthoring))
	for _, shared := range []string{specSkill, review, verify} {
		require.Nil(t, ShareSkillVersionWithProject(ctx, pool, ownerRec, SkillVersionShareRequest{
			SkillID: shared, Version: 1, Project: project}))
	}
	_, aerr := PublishSkillVersion(ctx, pool, ownerRec, PublishSkillVersionRequest{
		SkillID: specSkill, ExpectedLatest: 1, Bundle: wfBundle("grill-me v2 private"), Contract: wfContract(skillregistry.CapAuthoring),
	})
	require.Nil(t, aerr)

	// The member pins: skill_version 0 must resolve to v1 (latest ACCESSIBLE),
	// never the private v2.
	wi, aerr := wfCreate(t, ctx, pool, project, memberRec, member, true, wfFlowSpec(specSkill, review, verify))
	require.Nil(t, aerr)
	view, aerr := GetWorkItemWorkflow(ctx, pool, wi.ID)
	require.Nil(t, aerr)
	var flow wf.Flow
	require.Nil(t, json.Unmarshal(view.Steps, &flow))
	require.Equal(t, 1, flow.Steps[0].SkillVersion, "latest-accessible pinning must stop at the shared v1")

	// The workflow view carries references and progress only: no skill content.
	raw, err := json.Marshal(view)
	require.Nil(t, err)
	require.NotContains(t, string(raw), "grill-me v2 private")
	require.NotContains(t, string(raw), "\"bundle\"")
	require.NotContains(t, string(raw), "\"contract\"")

	// A member CAN pin what is shared; a stranger gets the no-oracle refusal
	// for the same refs (skill exists but no accessible version).
	stranger := wfUser(t, pool, "stranger")
	strangerRec := &UserRecord{ID: stranger, Role: "writer"}
	wi2, aerr := CreateWorkItem(ctx, pool, &CreateWorkItemRequest{
		Project: project, Goal: "stranger pin test " + wfHash(project)[:12] + "-1", Source: "human",
	}, stranger, stranger, nil, "writer")
	require.Nil(t, aerr)
	_, aerr = UpdateWorkItemWorkflow(ctx, pool, wi2.ID, strangerRec, stranger, "writer", nil,
		UpdateWorkItemWorkflowRequest{ExpectedStepsVersion: 0,
			RequiresHumanSession: &[]bool{true}[0], Steps: wfFlowSpec(specSkill, review, verify)})
	require.NotNil(t, aerr)
	require.Equal(t, ErrNotFound, aerr.Code)
}

// ─── Retry authorization (the bounded D9 fallback) ───────────────────────────

// TestWorkflowRetryAuthorizationBounded holds the retry half of the repair
// contract: an incomplete invocation can be retried exactly ONCE through an
// explicit authorization, the authorization is bound to the exact failed step
// attempt, and a second invocation under it is refused.
func TestWorkflowRetryAuthorizationBounded(t *testing.T) {
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

	// Provider error is recorded (not paused — it is not a review FAIL).
	rec, aerr := RecordWorkflowResult(ctx, pool, wi.ID, RecordWorkflowResultRequest{
		AttemptID: attempt, ClaimEpoch: 1, SessionSecret: "s3cret",
		Result: wfStepResult(t, ctx, pool, start, wf.StatusProviderError, "", "art-spec"),
	})
	require.Nil(t, aerr)
	require.False(t, rec.Paused)

	// An ordinary re-start is refused: the step already has a result.
	_, aerr = StartWorkflowStep(ctx, pool, wi.ID, StartWorkflowStepRequest{
		AttemptID: attempt, ClaimEpoch: 1, SessionSecret: "s3cret", StepID: "spec"})
	require.NotNil(t, aerr)
	require.Equal(t, ErrConflictDuplicate, aerr.Code)

	// A retry authorization for the failed (provider_error) invocation is
	// the D9 fallback shape: bounded to exactly one new invocation.
	auth, aerr := AuthorizeWorkflowRepair(ctx, pool, wi.ID, AuthorizeWorkflowRepairRequest{
		AttemptID: attempt, ClaimEpoch: 1, SessionSecret: "s3cret",
		FailedStepAttemptID: start.StepAttemptID, Kind: "retry",
		Reason: "provider channel died mid-run",
	})
	require.Nil(t, aerr)
	require.Equal(t, "retry", auth.Kind)
	require.Empty(t, auth.ParentEpisodeID,
		"a retry of an ORDINARY invocation carries no parent episode; the lineage column stays empty outside episode-descended chains")

	// Bind the retry invocation and record it: the authorization closes.
	retry := wfStartBound(t, ctx, pool, wi.ID, attempt, "s3cret", 1, "spec", auth.ID)
	rec, aerr = RecordWorkflowResult(ctx, pool, wi.ID, RecordWorkflowResultRequest{
		AttemptID: attempt, ClaimEpoch: 1, SessionSecret: "s3cret",
		Result: wfStepResult(t, ctx, pool, retry, wf.StatusCompleted, "", "art-spec-2"),
	})
	require.Nil(t, aerr)
	require.False(t, rec.Paused)
	var episodeStatus string
	require.Nil(t, pool.QueryRow(ctx,
		`SELECT status FROM wi_workflow_repair_episodes WHERE id=$1`, auth.ID).Scan(&episodeStatus))
	require.Equal(t, "closed", episodeStatus)

	// The episode kind refuses a non-review failure.
	_, aerr = AuthorizeWorkflowRepair(ctx, pool, wi.ID, AuthorizeWorkflowRepairRequest{
		AttemptID: attempt, ClaimEpoch: 1, SessionSecret: "s3cret",
		FailedStepAttemptID: start.StepAttemptID, Kind: "episode",
		Reason: "not a review failure, so this must be refused",
	})
	require.NotNil(t, aerr)
	require.Equal(t, ErrBadRequest, aerr.Code)

	// The retry kind is the provider_error fallback ONLY: a review FAIL never
	// rerolls through a retry — it must take the episode path (aihub#708
	// review blocker: "retry can rerun review FAIL").
	reviewStart := wfStart(t, ctx, pool, wi.ID, attempt, "s3cret", 1, "review")
	rec, aerr = RecordWorkflowResult(ctx, pool, wi.ID, RecordWorkflowResultRequest{
		AttemptID: attempt, ClaimEpoch: 1, SessionSecret: "s3cret",
		Result: wfStepResult(t, ctx, pool, reviewStart, wf.StatusCompleted, wf.ReviewFail, "art-review"),
	})
	require.Nil(t, aerr)
	require.True(t, rec.Paused)
	attempt2 := wfAttempt(t, pool, wi.ID, owner, "s3cret", 2)
	_, aerr = AuthorizeWorkflowRepair(ctx, pool, wi.ID, AuthorizeWorkflowRepairRequest{
		AttemptID: attempt2, ClaimEpoch: 2, SessionSecret: "s3cret",
		FailedStepAttemptID: reviewStart.StepAttemptID, Kind: "retry",
		Reason: "a FAIL verdict must not reroll through a retry",
	})
	require.NotNil(t, aerr)
	require.Equal(t, ErrBadRequest, aerr.Code)
	require.Contains(t, aerr.Message, "episode")

	episodeAuth, aerr := AuthorizeWorkflowRepair(ctx, pool, wi.ID, AuthorizeWorkflowRepairRequest{
		AttemptID: attempt2, ClaimEpoch: 2, SessionSecret: "s3cret",
		FailedStepAttemptID: reviewStart.StepAttemptID, Kind: "episode",
		Reason: "the FAIL recovers through the episode path instead",
	})
	require.Nil(t, aerr)
	require.Equal(t, "episode", episodeAuth.Kind)

	// A second work item drives the non-provider_error refusal: `incomplete`
	// is a worker defect the flow must surface, not something a retry launders.
	wi2, aerr := wfCreate(t, ctx, pool, project, caller, owner, true, wfFlowSpec(spec, review, verify))
	require.Nil(t, aerr)
	attemptB := wfAttempt(t, pool, wi2.ID, owner, "s3cret", 1)
	startB := wfStart(t, ctx, pool, wi2.ID, attemptB, "s3cret", 1, "spec")
	_, aerr = RecordWorkflowResult(ctx, pool, wi2.ID, RecordWorkflowResultRequest{
		AttemptID: attemptB, ClaimEpoch: 1, SessionSecret: "s3cret",
		Result: wfStepResult(t, ctx, pool, startB, wf.StatusIncomplete, "", "art-spec-b"),
	})
	require.Nil(t, aerr)
	_, aerr = AuthorizeWorkflowRepair(ctx, pool, wi2.ID, AuthorizeWorkflowRepairRequest{
		AttemptID: attemptB, ClaimEpoch: 1, SessionSecret: "s3cret",
		FailedStepAttemptID: startB.StepAttemptID, Kind: "retry",
		Reason: "incomplete is not an infrastructure failure",
	})
	require.NotNil(t, aerr)
	require.Equal(t, ErrBadRequest, aerr.Code)
}
