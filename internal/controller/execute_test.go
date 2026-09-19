package controller

// execute_test.go — pure-fake pins for the post-start failure rules of the
// DB workflow path (aihub#708 Batch 2B blocker #1).
//
// What these tests pin, in the terms of the repair:
//
//   - a structured workflow.StepResult is parsed and submitted even when
//     the worker process exited nonzero, so a valid review FAIL is recorded
//     (the server pauses the attempt on it) instead of being discarded and
//     rerolled on the next pinned candidate;
//   - a started worker that dies nonzero with no decodable result leaves a
//     worktree the controller cannot inspect, so RunStep stops with a
//     recoverable hold naming the side-effect reconciliation required and
//     never starts the next candidate over the possibly-dirty worktree.
//
// The fakes are total — no server, no installed harness, no network, no
// database: the API is an in-memory fake, the machine catalog is a temp HOME
// with a two-candidate [[models]] config.toml, and the "harness" is a shell
// script named `claude` that the test writes, which prints the canned worker
// output and exits with a controlled code while appending one line to a run
// counter file. The counter is the reroll guard: both tests pin TWO
// dispatchable candidates, so any post-start fallback shows up as a second
// harness start.

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/GMISWE/ieops-aihub/internal/skillregistry"
	"github.com/GMISWE/ieops-aihub/internal/workflow"
	"github.com/GMISWE/ieops-aihub/pkg/client"
)

// fakeWorkflowAPI is the whole server surface RunStep touches, in memory.
type fakeWorkflowAPI struct {
	start map[string]any
	skill map[string]any
	// recorded holds every structured result RunStep submitted.
	recorded []workflow.StepResult
	// saved holds every controller-sink artifact save RunStep issued.
	saved []client.SaveArtifactRequest
	// calls records the save/record call order (aihub#725: save MUST
	// precede record, and non-sink results must produce no save at all).
	calls []string
	// saveErr, when set, makes SaveArtifact fail (the save-failure arm).
	saveErr error
}

func (f *fakeWorkflowAPI) GetWorkItemWorkflow(context.Context, string) (map[string]any, error) {
	return map[string]any{
		"work_item_id": "wi_fake", "steps_version": 3, "requires_human_session": false,
		"steps": map[string]any{"version": 3, "steps": []any{map[string]any{
			"id": "review-code", "skill_id": "skill-fake", "skill_version": 1,
			"models": []any{map[string]any{"harness": "cc", "model": "fake-a", "effort": "high"}},
		}}},
		"progress": []any{map[string]any{"step_id": "review-code", "rhs": false}},
	}, nil
}

func (f *fakeWorkflowAPI) GetMemory(context.Context, string) (map[string]any, error) {
	return nil, errors.New("fake: input-free step must not read an artifact")
}

func (f *fakeWorkflowAPI) StartWorkflowStep(context.Context, string, any) (map[string]any, error) {
	return f.start, nil
}

func (f *fakeWorkflowAPI) RecordWorkflowResult(_ context.Context, _ string, body any) (map[string]any, error) {
	f.calls = append(f.calls, "record")
	if m, ok := body.(map[string]any); ok {
		if result, ok := m["result"].(workflow.StepResult); ok {
			f.recorded = append(f.recorded, result)
		}
	}
	return map[string]any{"recorded": true}, nil
}

// SaveArtifact is the fake controller-sink persistence seam: it records the
// request and answers the server-assigned immutable identity the real
// POST /v1/memories route returns.
func (f *fakeWorkflowAPI) SaveArtifact(_ context.Context, req client.SaveArtifactRequest) (client.SavedArtifact, error) {
	f.calls = append(f.calls, "save")
	if f.saveErr != nil {
		return client.SavedArtifact{}, f.saveErr
	}
	f.saved = append(f.saved, req)
	return client.SavedArtifact{ID: "mem_sink_1", Version: 1, IsNew: true}, nil
}

func (f *fakeWorkflowAPI) GetSkillVersion(context.Context, string, int) (map[string]any, error) {
	return f.skill, nil
}

func (f *fakeWorkflowAPI) ReconcileWorkflowInvocations(context.Context, string, any) (map[string]any, error) {
	return map[string]any{}, nil
}

// runStepTestEnv is one RunStep test: a fake server, a fake machine, and a
// fake harness whose body the test controls.
type runStepTestEnv struct {
	api *fakeWorkflowAPI
	log bytes.Buffer
	// runs is the file the fake harness appends one line to per start.
	runs string
}

// newRunStepTestEnv installs a temp HOME whose ~/.polyforge/config.toml pins
// a two-candidate cc catalog, puts a fake `claude` binary on PATH that runs
// harnessBody, and wires the canned server responses for step "review-code"
// of work item "wi_fake" under a write grant.
func newRunStepTestEnv(t *testing.T, harnessBody string) *runStepTestEnv {
	t.Helper()

	home := t.TempDir()
	cfgDir := filepath.Join(home, ".polyforge")
	if err := os.MkdirAll(cfgDir, 0o700); err != nil {
		t.Fatalf("create temp machine config dir: %v", err)
	}
	const catalog = `[[models]]
  name = "fake-a"
  harness = "cc"
  model = "fake-a"
  effort = "high"
  uses = ["authoring"]

[[models]]
  name = "fake-b"
  harness = "cc"
  model = "fake-b"
  effort = "high"
  uses = ["authoring"]
`
	if err := os.WriteFile(filepath.Join(cfgDir, "config.toml"), []byte(catalog), 0o600); err != nil {
		t.Fatalf("write temp machine config: %v", err)
	}
	t.Setenv("HOME", home)

	bin := t.TempDir()
	runs := filepath.Join(bin, "harness-runs")
	script := "#!/bin/sh\nprintf 'run\\n' >> \"$FAKE_HARNESS_LOG\"\n" + harnessBody
	harness := filepath.Join(bin, "claude")
	if err := os.WriteFile(harness, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake harness binary: %v", err)
	}
	t.Setenv("FAKE_HARNESS_LOG", runs)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))

	return &runStepTestEnv{
		api: &fakeWorkflowAPI{
			start: map[string]any{
				"invocation_id":   "inv_fake_1",
				"steps_version":   3,
				"step_id":         "review-code",
				"step_attempt_id": "sa_fake_1",
				"producer_id":     "producer-fake",
				"claim_epoch":     int64(7),
				"grant": map[string]any{
					"authority":          "write",
					"producer_isolation": "shared",
				},
				"skill_id":      "skill-fake",
				"skill_version": 1,
				"models": []map[string]any{
					{"harness": "cc", "model": "fake-a", "effort": "high"},
					{"harness": "cc", "model": "fake-b", "effort": "high"},
				},
			},
			skill: map[string]any{
				"bundle": map[string]any{
					"entry": "SKILL.md",
					"files": []map[string]any{
						{"path": "SKILL.md", "content": "Review the work and return a verdict.", "encoding": "utf-8"},
					},
					"license": map[string]any{"name": "Proprietary"},
				},
				"contract": map[string]any{
					"capabilities": []string{"authoring"},
					"runtime":      map[string]any{"interactive": false},
				},
			},
		},
		runs: runs,
	}
}

// run executes one RunStep against the env's fake machine and server.
func (e *runStepTestEnv) run(t *testing.T) (StepOutcome, error) {
	t.Helper()
	return RunStep(context.Background(), e.api, "wi_fake", "review-code", t.TempDir(), StepOptions{
		Credentials:          Credentials{AttemptID: "ra_fake", ClaimEpoch: 7, SessionSecret: "sec"},
		RunDir:               t.TempDir(),
		Log:                  &e.log,
		ExpectedStepsVersion: 3,
	})
}

// harnessStarts counts how many times the fake harness ran: the reroll guard.
func (e *runStepTestEnv) harnessStarts(t *testing.T) int {
	t.Helper()
	data, err := os.ReadFile(e.runs)
	if os.IsNotExist(err) {
		return 0
	}
	if err != nil {
		t.Fatalf("read harness run counter: %v", err)
	}
	return bytes.Count(data, []byte("\n"))
}

// TestRunStepRecordsStructuredReviewFailDespiteNonzeroExit pins the case the
// universal post-start fallback used to destroy: a worker that emits a
// complete, identity-valid review FAIL and exits nonzero. Exit status is
// transport metadata, not the verdict — RunStep must parse and submit the
// result (the server pauses the attempt on a review FAIL), and must never
// offer the second pinned candidate, which would be rerolling the review
// until it passes.
func TestRunStepRecordsStructuredReviewFailDespiteNonzeroExit(t *testing.T) {
	const failJSON = `{"status":"completed","review_verdict":"fail","work_item_id":"wi_fake","flow_version":3,"step_id":"review-code","step_attempt_id":"sa_fake_1","epoch":7,"producer_id":"producer-fake","artifact":{"id":"art-fake","version":1,"hash":"deadbeef"}}`
	env := newRunStepTestEnv(t, "printf '%s\\n' '"+failJSON+"'\nexit 1\n")

	out, err := env.run(t)
	if err != nil {
		t.Fatalf("RunStep must submit a structured result even on a nonzero exit; got error: %v", err)
	}
	if !out.Recorded {
		t.Fatal("RunStep reported no recorded result for a decodable, identity-valid review FAIL")
	}
	if out.Result.ReviewVerdict != workflow.ReviewFail {
		t.Fatalf("recorded verdict = %q, want the recorded review FAIL", out.Result.ReviewVerdict)
	}
	if len(env.api.recorded) != 1 || env.api.recorded[0].ReviewVerdict != workflow.ReviewFail {
		t.Fatalf("server record = %+v, want exactly one recorded review FAIL", env.api.recorded)
	}
	if starts := env.harnessStarts(t); starts != 1 {
		t.Fatalf("harness started %d times; a recorded review FAIL must never be rerolled onto the second candidate", starts)
	}
	if log := env.log.String(); !strings.Contains(log, "no_fallback") {
		t.Fatalf("step log lacks the no_fallback chain record for the review FAIL:\n%s", log)
	}
}

// TestRunStepSideEffectFailureStopsWithoutFallback pins the other half of the
// blocker: a started worker that dies nonzero with no decodable result leaves
// a worktree the controller cannot inspect, so RunStep must stop — a
// recoverable hold naming the side-effect reconciliation required — and must
// not fall back to the second pinned candidate over the possibly-dirty
// worktree.
func TestRunStepSideEffectFailureStopsWithoutFallback(t *testing.T) {
	env := newRunStepTestEnv(t, "echo 'the harness died for reasons of its own' >&2\nexit 1\n")

	out, err := env.run(t)
	if err == nil {
		t.Fatal("RunStep returned success for a nonzero worker exit with no structured result; want a recoverable hold")
	}
	var hold *RecoverableExecutionError
	if !errors.As(err, &hold) {
		t.Fatalf("error = %v, want a *RecoverableExecutionError", err)
	}
	if !strings.Contains(hold.Detail, "side-effect reconciliation is required") {
		t.Fatalf("hold detail %q does not name the required side-effect reconciliation", hold.Detail)
	}
	if hold.RetainClaim {
		t.Fatalf("the process group stop was confirmed; the hold must not demand claim retention: %+v", hold)
	}
	if out.Recorded {
		t.Fatal("nothing may be recorded from an undecodable worker exit")
	}
	if len(env.api.recorded) != 0 {
		t.Fatalf("server record = %+v; nothing may be recorded from an undecodable worker exit", env.api.recorded)
	}
	if starts := env.harnessStarts(t); starts != 1 {
		t.Fatalf("harness started %d times; a side-effect failure must never fall back to the second candidate", starts)
	}
	if log := env.log.String(); !strings.Contains(log, "reconcile_required") {
		t.Fatalf("step log lacks the reconcile_required chain record:\n%s", log)
	}
}

// TestRunStepDeadlineOutputSnapshotIsRaceFree pins the CI -race report on the
// step-deadline branch: RunStep reads the worker's output after Proc.Stop
// without waiting for Proc.Wait, while os/exec's pipe-copy goroutine can
// still be draining the streams — with a raw bytes.Buffer that read raced the
// copy goroutine's Write. The harness emits one line per stream and then
// outlives the deadline, so the deadline branch snapshots both streams while
// the pipes are live; under -race the mutex-protected outputCollector must
// serialize every snapshot against every pipe-copy Write (the race detector
// flags the unsynchronized pair even when the writes complete long before the
// read, because nothing on this branch ever orders them). The retained log
// also pins that the streams stay separate: the worker's stderr line belongs
// to the labeled stderr section and its stdout line to the stdout section,
// because stdout's contract is exactly one JSON StepResult and merged
// narration would break the parse.
func TestRunStepDeadlineOutputSnapshotIsRaceFree(t *testing.T) {
	env := newRunStepTestEnv(t, "printf 'deadline stdout line\\n'\nprintf 'deadline stderr line\\n' >&2\nsleep 30\n")

	out, err := RunStep(context.Background(), env.api, "wi_fake", "review-code", t.TempDir(), StepOptions{
		Credentials:          Credentials{AttemptID: "ra_fake", ClaimEpoch: 7, SessionSecret: "sec"},
		RunDir:               t.TempDir(),
		Log:                  &env.log,
		ExpectedStepsVersion: 3,
		StepTimeout:          500 * time.Millisecond,
	})

	var hold *RecoverableExecutionError
	if !errors.As(err, &hold) {
		t.Fatalf("error = %v, want a *RecoverableExecutionError for the step deadline", err)
	}
	if !strings.Contains(hold.Detail, "timed out") {
		t.Fatalf("hold detail %q does not report the step deadline", hold.Detail)
	}
	if hold.RetainClaim {
		t.Fatalf("the sleeping worker's stop is confirmed; the hold must not retain the claim: %+v", hold)
	}
	if out.Recorded || len(env.api.recorded) != 0 {
		t.Fatalf("a timed-out step records nothing; out=%+v recorded=%+v", out, env.api.recorded)
	}
	if starts := env.harnessStarts(t); starts != 1 {
		t.Fatalf("harness started %d times; a step deadline must never fall back over the open invocation", starts)
	}

	log := env.log.String()
	stderrAt := strings.Index(log, "[worker stderr]")
	stdoutAt := strings.Index(log, "[worker stdout]")
	if stderrAt < 0 || stdoutAt < 0 || stderrAt > stdoutAt {
		t.Fatalf("step log lacks the labeled, separated worker stream sections:\n%s", log)
	}
	stderrSection, stdoutSection := log[stderrAt:stdoutAt], log[stdoutAt:]
	if !strings.Contains(stderrSection, "deadline stderr line") ||
		!strings.Contains(stdoutSection, "deadline stdout line") ||
		strings.Contains(stderrSection, "deadline stdout line") ||
		strings.Contains(stdoutSection, "deadline stderr line") {
		t.Fatalf("stream separation lost in the retained step log:\n%s", log)
	}
}

// ─── aihub#725 S4: the controller-side artifact sink ────────────────────────
//
// Four arms, per the code_change dispatch:
//
//	1. completed + no triple → the sink saves exactly once, and RecordWorkflowResult
//	   receives the SERVER-RETURNED triple (mem_sink_1@1) plus the canonical hash
//	   of the worker's output — never a worker-supplied id;
//	2. completed + self-supplied triple → NO save, the triple passes through
//	   unchanged (the server's fabricated-id refusal stays observable);
//	3. non-completed → NO save, existing behavior byte-identical (and the
//	   transient output never reaches the recorded result);
//	4. the digest filled into the envelope equals sha256 over the exact
//	   encoding/json.Marshal bytes of the stored structured payload.

// sinkArmResult is one identity-valid worker result for the sink arms.
func sinkArmResult(status string, extra string) string {
	return `{"status":"` + status + `","work_item_id":"wi_fake","flow_version":3,` +
		`"step_id":"review-code","step_attempt_id":"sa_fake_1","epoch":7,` +
		`"producer_id":"producer-fake"` + extra + `}`
}

// sinkExpectedPayload is the worker output object arm 1 emits, rebuilt in Go
// with json.Number literals so the expected hash is computed over exactly the
// bytes the worker wrote.
func sinkExpectedPayload(t *testing.T) map[string]any {
	t.Helper()
	var payload map[string]any
	dec := json.NewDecoder(strings.NewReader(`{"summary":"fixed the parser","tests_run":3,"pass_ratio":0.95}`))
	dec.UseNumber()
	if err := dec.Decode(&payload); err != nil {
		t.Fatal(err)
	}
	return payload
}

func TestRunStepControllerSinkStoresArtifactlessCompletedOutput(t *testing.T) {
	env := newRunStepTestEnv(t, "printf '%s\\n' '"+sinkArmResult("completed",
		`,"output":{"summary":"fixed the parser","tests_run":3,"pass_ratio":0.95}`)+"'\n")

	out, err := env.run(t)
	if err != nil {
		t.Fatalf("RunStep: %v", err)
	}
	if !out.Recorded {
		t.Fatal("the sink branch must still record the result")
	}
	api := env.api
	if len(api.saved) != 1 {
		t.Fatalf("sink saves = %d, want exactly one", len(api.saved))
	}
	if len(api.calls) != 2 || api.calls[0] != "save" || api.calls[1] != "record" {
		t.Fatalf("call order = %v, want save before record", api.calls)
	}

	// The save body: controller-derived type (authoring contract → execute),
	// wi-bound, claim-time credentials, verbatim number literals, and the
	// honest controller-sink provenance in the content.
	req := api.saved[0]
	if req.Type != "methodology.execute" {
		t.Fatalf("artifact type = %q, want methodology.execute for an authoring-capability contract", req.Type)
	}
	if req.WorkItemID != "wi_fake" || req.AttemptID != "ra_fake" || req.ClaimEpoch != 7 || req.SessionSecret != "sec" {
		t.Fatalf("save request binding/credentials = %+v", req)
	}
	if req.Visibility != "project" {
		t.Fatalf("visibility = %q, want project", req.Visibility)
	}
	// The durable replay receipt (aihub#725 review_fix B2): all three
	// components are trusted controller state — the start descriptor's
	// invocation identity plus the claim-time attempt — never worker-supplied.
	if req.SinkReceipt == nil {
		t.Fatal("sink save carries no durable replay receipt")
	}
	wantReceipt := client.ControllerSinkReceipt{InvocationID: "inv_fake_1", StepAttemptID: "sa_fake_1", AttemptID: "ra_fake"}
	if *req.SinkReceipt != wantReceipt {
		t.Fatalf("sink receipt = %+v, want %+v", *req.SinkReceipt, wantReceipt)
	}
	if !strings.Contains(req.Content, "byte-for-byte") {
		t.Fatalf("content still claims the withdrawn verbatim spelling instead of the honest byte-for-byte provenance:\n%s", req.Content)
	}
	if !strings.Contains(req.Content, "invocation_id=inv_fake_1") || !strings.Contains(req.Content, "attempt_id=ra_fake") {
		t.Fatalf("content lacks the durable replay receipt provenance:\n%s", req.Content)
	}
	if !strings.Contains(req.Content, "origin=controller_sink") || !strings.Contains(req.Content, "payload_author=worker") {
		t.Fatalf("content lacks the controller-sink provenance declaration:\n%s", req.Content)
	}
	wantPayload, err := json.Marshal(sinkExpectedPayload(t))
	if err != nil {
		t.Fatal(err)
	}
	if string(req.StructuredPayload) != string(wantPayload) {
		t.Fatalf("structured payload\n got %s\nwant %s (number literals must survive verbatim)", req.StructuredPayload, wantPayload)
	}

	// The recorded result carries the SERVER-RETURNED triple and the canonical
	// digest — not the transient output.
	if len(api.recorded) != 1 {
		t.Fatalf("recorded results = %d, want exactly one", len(api.recorded))
	}
	recorded := api.recorded[0]
	if recorded.Artifact.ID != "mem_sink_1" || recorded.Artifact.Version != 1 {
		t.Fatalf("recorded triple = %+v, want the server-returned mem_sink_1@1", recorded.Artifact)
	}
	if want := WorkflowArtifactHash(sinkExpectedPayload(t)); recorded.Artifact.Hash != want {
		t.Fatalf("recorded hash = %q, want canonical %q", recorded.Artifact.Hash, want)
	}
	if recorded.Output != nil {
		t.Fatalf("transient output leaked into the recorded result: %#v", recorded.Output)
	}
	if out.Result.Artifact.ID != "mem_sink_1" {
		t.Fatalf("outcome artifact = %+v, want the sink-filled triple", out.Result.Artifact)
	}
}

func TestRunStepExistingArtifactTripleBypassesSink(t *testing.T) {
	env := newRunStepTestEnv(t, "printf '%s\\n' '"+sinkArmResult("completed",
		`,"artifact":{"id":"art-worker","version":1,"hash":"sha256:feed"}`)+"'\n")

	out, err := env.run(t)
	if err != nil {
		t.Fatalf("RunStep: %v", err)
	}
	if !out.Recorded {
		t.Fatal("a completed result with its own triple must record as before")
	}
	if len(env.api.saved) != 0 {
		t.Fatalf("sink saves = %d; a self-supplied triple must never be replaced", len(env.api.saved))
	}
	if len(env.api.calls) != 1 || env.api.calls[0] != "record" {
		t.Fatalf("calls = %v, want exactly one record and no save", env.api.calls)
	}
	if len(env.api.recorded) != 1 || env.api.recorded[0].Artifact.ID != "art-worker" ||
		env.api.recorded[0].Artifact.Version != 1 || env.api.recorded[0].Artifact.Hash != "sha256:feed" {
		t.Fatalf("recorded triple = %+v, want the worker-supplied one unchanged", env.api.recorded)
	}
}

func TestRunStepNonCompletedResultBypassesSink(t *testing.T) {
	// The result even carries a transient output object — an incomplete result
	// must still not save, and the output must not reach the recorded result.
	env := newRunStepTestEnv(t, "printf '%s\\n' '"+sinkArmResult("incomplete",
		`,"output":{"summary":"half done"}`)+"'\n")

	out, err := env.run(t)
	if err != nil {
		t.Fatalf("RunStep: %v", err)
	}
	if !out.Recorded {
		t.Fatal("an incomplete result must record as before")
	}
	if len(env.api.saved) != 0 {
		t.Fatalf("sink saves = %d; a non-completed result must bypass the sink", len(env.api.saved))
	}
	if len(env.api.calls) != 1 || env.api.calls[0] != "record" {
		t.Fatalf("calls = %v, want exactly one record and no save", env.api.calls)
	}
	if len(env.api.recorded) != 1 || env.api.recorded[0].Status != workflow.StatusIncomplete {
		t.Fatalf("recorded = %+v, want the incomplete result", env.api.recorded)
	}
	if env.api.recorded[0].Output != nil {
		t.Fatalf("transient output leaked into a non-completed recorded result: %#v", env.api.recorded[0].Output)
	}
}

// TestRunStepPartialArtifactTripleBypassesSink is the aihub#725 review_fix
// SF1 regression: the sink used to fire on `Artifact.ID == ""` alone, so a
// malformed id-less fragment like {version:1, hash:"sha256:x"} was silently
// OVERWRITTEN by the sink's server-returned triple — destroying the evidence
// the server's resolution would have refused. The trigger is now the ZERO
// triple: any populated component means the result cited SOMETHING, the sink
// must not touch it, and the server refuses the fragment at record time.
func TestRunStepPartialArtifactTripleBypassesSink(t *testing.T) {
	env := newRunStepTestEnv(t, "printf '%s\\n' '"+sinkArmResult("completed",
		`,"artifact":{"version":1,"hash":"sha256:feed"},`+
			`"output":{"summary":"whatever"}`)+"'\n")

	_, err := env.run(t)
	if err != nil {
		t.Fatalf("RunStep: %v", err)
	}
	if len(env.api.saved) != 0 {
		t.Fatalf("sink saves = %d; a partially-populated triple must never be replaced (SF1)", len(env.api.saved))
	}
	if len(env.api.recorded) != 1 {
		t.Fatalf("recorded results = %d, want exactly one", len(env.api.recorded))
	}
	recorded := env.api.recorded[0]
	if recorded.Artifact.ID != "" || recorded.Artifact.Version != 1 || recorded.Artifact.Hash != "sha256:feed" {
		t.Fatalf("recorded triple = %+v, want the worker's fragment recorded unchanged", recorded.Artifact)
	}
}

// TestControllerSinkDigestMatchesCanonicalJSONMarshal is the fourth arm: the
// digest the sink fills into the envelope must be exactly sha256 over the
// encoding/json.Marshal bytes of the SAME object stored as
// attrs.structured_payload — nested maps, sorted keys, and json.Number
// literals included — because that is the rule the server's
// workflowArtifactDigest recompute applies.
//
// 🔴 The second half flipped in aihub#725 review_fix SF2/SF4: it used to pin
// that the sink canonicalizes through the float64 universe (hashing 1.50 as
// 1.5); the sink now hashes and stores the worker's literals VERBATIM,
// which is what the server's UseNumber record-time recompute and the
// UseNumber read path (pkg/client GetMemory → ResolveInputs) both expect.
// The arm below states the new parity end to end: sink hash == digest over
// the bytes sent == digest over a UseNumber re-decode of those bytes (the
// server's recompute over its stored copy).
func TestControllerSinkDigestMatchesCanonicalJSONMarshal(t *testing.T) {
	payload := map[string]any{
		"summary": "did work",
		"nested":  map[string]any{"b": json.Number("1.50"), "a": "x", "deeper": map[string]any{"k": json.Number("12345678901234567890")}},
		"count":   json.Number("42"),
		"flag":    true,
		"list":    []any{json.Number("1"), json.Number("2.5")},
	}
	canonical, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(canonical)
	want := "sha256:" + hex.EncodeToString(sum[:])
	if got := WorkflowArtifactHash(payload); got != want {
		t.Fatalf("WorkflowArtifactHash = %q, want %q (sha256 over %s)", got, want, canonical)
	}
	if got := WorkflowArtifactHashBytes(canonical); got != want {
		t.Fatalf("WorkflowArtifactHashBytes = %q, want %q (the byte form must be the same one rule)", got, want)
	}
	// The server's record-time recompute (resolveCompletedResultArtifact)
	// decodes the STORED attrs with UseNumber and re-marshals: for the
	// literals a controller sink emits (and jsonb round-trips exactly — every
	// decimal and integer spelling), that reproduces the exact bytes the sink
	// hashed. State that parity here so a future canonicalization pass on
	// EITHER side goes red instead of silently desynchronizing the digest.
	var reread map[string]any
	dec := json.NewDecoder(bytes.NewReader(canonical))
	dec.UseNumber()
	if err := dec.Decode(&reread); err != nil {
		t.Fatal(err)
	}
	rereadBytes, err := json.Marshal(reread)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(rereadBytes, canonical) {
		t.Fatalf("UseNumber re-read rewrote the bytes:\n got %s\nwant %s", rereadBytes, canonical)
	}
	if got := WorkflowArtifactHash(reread); got != want {
		t.Fatalf("digest over the UseNumber re-read = %q, want %q — the sink and the server drifted", got, want)
	}
	// The red side of the red-green: the float64 universe is a DIFFERENT
	// digest, so the fixture still discriminates and cannot pass vacuously.
	var floatified map[string]any
	if err := json.Unmarshal(canonical, &floatified); err != nil {
		t.Fatal(err)
	}
	if got := WorkflowArtifactHash(floatified); got == want {
		t.Fatal("float64 canonicalization produces the same digest as the literal bytes; the fixture no longer discriminates")
	}
}

// TestSinkArtifactTypeDerivesFromContract pins the trusted typing rule: review
// or verification capability gates sink as methodology.review; everything else
// is methodology.execute. It is controller metadata from the PINNED contract —
// the worker never gets to choose its artifact type.
func TestSinkArtifactTypeDerivesFromContract(t *testing.T) {
	authoring := &skillregistry.SkillContract{Capabilities: []skillregistry.Capability{skillregistry.CapAuthoring}}
	review := &skillregistry.SkillContract{Capabilities: []skillregistry.Capability{skillregistry.CapReview}}
	verification := &skillregistry.SkillContract{Capabilities: []skillregistry.Capability{skillregistry.CapVerification, skillregistry.CapAuthoring}}
	for _, tc := range []struct {
		contract *skillregistry.SkillContract
		want     string
	}{
		{authoring, "methodology.execute"},
		{review, "methodology.review"},
		{verification, "methodology.review"},
	} {
		if got := sinkArtifactType(tc.contract); got != tc.want {
			t.Fatalf("sinkArtifactType(%v) = %q, want %q", tc.contract.Capabilities, got, tc.want)
		}
	}
}
