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
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/GMISWE/ieops-aihub/internal/workflow"
)

// fakeWorkflowAPI is the whole server surface RunStep touches, in memory.
type fakeWorkflowAPI struct {
	start map[string]any
	skill map[string]any
	// recorded holds every structured result RunStep submitted.
	recorded []workflow.StepResult
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
	if m, ok := body.(map[string]any); ok {
		if result, ok := m["result"].(workflow.StepResult); ok {
			f.recorded = append(f.recorded, result)
		}
	}
	return map[string]any{"recorded": true}, nil
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
