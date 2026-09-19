package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/GMISWE/ieops-aihub/internal/config"
	"github.com/GMISWE/ieops-aihub/internal/controller"
	"github.com/GMISWE/ieops-aihub/internal/skillregistry"
	"github.com/GMISWE/ieops-aihub/internal/workflow"
	"github.com/GMISWE/ieops-aihub/pkg/client"
)

// sessionE2EAPI is an in-memory authorized API. It exercises the callable
// prepare/submit/continue protocol without starting a harness or making an
// external model request.
type sessionE2EAPI struct {
	steps     []map[string]any
	skills    map[string]map[string]any
	progress  map[string]workflow.StepResult
	open      map[string]controller.Invocation
	approvals map[string]*bool
	artifacts map[string]map[string]any
	next      int
}

func newSessionE2EAPI() *sessionE2EAPI {
	t := true
	models := []any{map[string]any{"harness": "cc", "model": "fake", "effort": "high"}}
	steps := []map[string]any{
		{"id": "grill", "skill_id": "skill-grill", "skill_version": 1, "rhs": &t, "models": models},
		{"id": "spec", "skill_id": "skill-spec", "skill_version": 1, "rhs": &t, "models": models, "inputs": []any{map[string]any{"name": "interview_requirements", "step_id": "grill", "output": "distilled_requirements"}}},
		{"id": "plan", "skill_id": "skill-plan", "skill_version": 1, "models": models, "inputs": []any{map[string]any{"name": "spec", "step_id": "spec", "output": "spec"}}},
		{"id": "review", "skill_id": "skill-review", "skill_version": 1, "rhs": &t, "models": models},
		{"id": "verify", "skill_id": "skill-verify", "skill_version": 1, "rhs": &t, "models": models},
	}
	contract := func(interactive bool, input, output string) map[string]any {
		m := map[string]any{"capabilities": []any{"authoring"}, "runtime": map[string]any{"interactive": interactive}, "output_schema": json.RawMessage(output)}
		if input != "" {
			m["input_schema"] = json.RawMessage(input)
		}
		return m
	}
	gateContract := func(interactive bool, input, output string, capability skillregistry.Capability) map[string]any {
		m := contract(interactive, input, output)
		m["capabilities"] = []any{capability}
		return m
	}
	bundle := func(body string) map[string]any {
		return map[string]any{"entry": "SKILL.md", "files": []any{map[string]any{"path": "SKILL.md", "content": body}}, "license": map[string]any{"name": "Proprietary"}}
	}
	return &sessionE2EAPI{
		steps: steps,
		skills: map[string]map[string]any{
			"skill-grill":  {"bundle": bundle("Ask one question at a time; never invent the human answer."), "contract": contract(true, "", `{"type":"object","properties":{"record":{"type":"string"},"distilled_requirements":{"type":"array","items":{"type":"string"}},"open_questions":{"type":"array","items":{"type":"string"}}},"required":["record","distilled_requirements","open_questions"],"additionalProperties":false}`)},
			"skill-spec":   {"bundle": bundle("Discuss and author the spec from the exact interview input."), "contract": contract(true, `{"type":"object","properties":{"interview_requirements":{"type":"array","items":{"type":"string"}}},"required":["interview_requirements"],"additionalProperties":false}`, `{"type":"object","properties":{"spec":{"type":"string"},"requirements":{"type":"array","items":{"type":"string"}}},"required":["spec","requirements"],"additionalProperties":false}`)},
			"skill-plan":   {"bundle": bundle("Plan from inputs.spec."), "contract": contract(false, `{"type":"object","properties":{"spec":{"type":"string"}},"required":["spec"],"additionalProperties":false}`, `{"type":"object","properties":{"plan":{"type":"string"},"steps":{"type":"array","items":{"type":"object","properties":{"id":{"type":"string"}},"required":["id"],"additionalProperties":false}}},"required":["plan","steps"],"additionalProperties":false}`)},
			"skill-review": {"bundle": bundle("Independently review the authored plan."), "contract": gateContract(false, `{"type":"object","properties":{"plan":{"type":"string"}},"required":["plan"],"additionalProperties":false}`, `{"type":"object","properties":{"approval":{"type":"string"}},"required":["approval"],"additionalProperties":false}`, skillregistry.CapReview)},
			"skill-verify": {"bundle": bundle("Independently verify the authored plan."), "contract": gateContract(false, `{"type":"object","properties":{"plan":{"type":"string"}},"required":["plan"],"additionalProperties":false}`, `{"type":"object","properties":{"evidence":{"type":"string"}},"required":["evidence"],"additionalProperties":false}`, skillregistry.CapVerification)},
		},
		progress: map[string]workflow.StepResult{}, open: map[string]controller.Invocation{}, approvals: map[string]*bool{}, artifacts: map[string]map[string]any{},
	}
}

func (f *sessionE2EAPI) GetWorkItemWorkflow(context.Context, string) (map[string]any, error) {
	progress := make([]any, 0, len(f.steps))
	for _, s := range f.steps {
		id := s["id"].(string)
		p := map[string]any{"step_id": id, "rhs": s["rhs"] != nil}
		if r, ok := f.progress[id]; ok {
			p["status"], p["step_attempt_id"], p["artifact"] = r.Status, r.StepAttemptID, r.Artifact
			if r.ReviewVerdict != "" {
				p["review_verdict"] = r.ReviewVerdict
			}
			if a, ok := f.approvals[id]; ok {
				p["approved"] = a
			}
		}
		if inv, ok := f.open[id]; ok {
			p["open_invocation_id"] = inv.InvocationID
		}
		progress = append(progress, p)
	}
	return map[string]any{"work_item_id": "wi_session", "steps_version": 1, "requires_human_session": true,
		"steps": map[string]any{"version": 1, "steps": f.steps}, "progress": progress}, nil
}

func (f *sessionE2EAPI) StartWorkflowStep(_ context.Context, _ string, body any) (map[string]any, error) {
	step := body.(map[string]any)["step_id"].(string)
	if _, exists := f.open[step]; exists {
		return nil, fmt.Errorf("open invocation")
	}
	f.next++
	var spec map[string]any
	for _, s := range f.steps {
		if s["id"] == step {
			spec = s
		}
	}
	grant := workflow.StepGrant{Authority: workflow.AuthorityWrite, ProducerIsolation: workflow.IsolationShared}
	if step == "review" || step == "verify" {
		grant = workflow.StepGrant{Authority: workflow.AuthorityReadOnly, ProducerIsolation: workflow.IsolationRequired}
	}
	inv := controller.Invocation{InvocationID: fmt.Sprintf("winv_%d", f.next), StepsVersion: 1, StepID: step,
		StepAttemptID: fmt.Sprintf("sa_%d", f.next), ProducerID: fmt.Sprintf("producer_%d", f.next), ClaimEpoch: 7,
		Grant:   grant,
		SkillID: spec["skill_id"].(string), SkillVersion: 1, Models: []workflow.ModelCandidate{{Harness: "cc", Model: "fake", Effort: "high"}}, EffectiveRHS: spec["rhs"] != nil}
	b, _ := json.Marshal(spec["params"])
	inv.Params = b
	b, _ = json.Marshal(spec["inputs"])
	_ = json.Unmarshal(b, &inv.Inputs)
	f.open[step] = inv
	b, _ = json.Marshal(inv)
	var out map[string]any
	_ = json.Unmarshal(b, &out)
	return out, nil
}
func (f *sessionE2EAPI) RecordWorkflowResult(_ context.Context, _ string, body any) (map[string]any, error) {
	result := body.(map[string]any)["result"].(workflow.StepResult)
	delete(f.open, result.StepID)
	f.progress[result.StepID] = result
	delete(f.approvals, result.StepID)
	return map[string]any{"recorded": true}, nil
}
func (f *sessionE2EAPI) GetSkillVersion(_ context.Context, id string, _ int) (map[string]any, error) {
	return f.skills[id], nil
}
func (f *sessionE2EAPI) ReconcileWorkflowInvocations(context.Context, string, any) (map[string]any, error) {
	return map[string]any{}, nil
}
func (f *sessionE2EAPI) Remember(_ context.Context, body any) (map[string]any, error) {
	f.next++
	id := fmt.Sprintf("mem_%d", f.next)
	m := body.(map[string]any)
	f.artifacts[id] = map[string]any{"id": id, "work_item_id": "wi_session", "content": m["content"], "attrs": map[string]any{"structured_payload": m["structured_payload"]}}
	return map[string]any{"id": id}, nil
}
func (*sessionE2EAPI) SaveArtifact(context.Context, client.SaveArtifactRequest) (client.SavedArtifact, error) {
	// The session E2E drive persists artifacts through Remember; the
	// controller-sink is the unattended path's seam and must never fire here.
	return client.SavedArtifact{}, errors.New("unexpected controller-sink save in the session E2E drive")
}
func (f *sessionE2EAPI) GetMemory(_ context.Context, id string) (map[string]any, error) {
	return f.artifacts[id], nil
}
func (f *sessionE2EAPI) approve(step string, artifact workflow.ArtifactRef, approved bool, userType string) error {
	if userType != "human" {
		return fmt.Errorf("approval requires authenticated human")
	}
	if latest, ok := f.progress[step]; !ok || latest.Artifact != artifact {
		return fmt.Errorf("approval artifact is stale")
	}
	v := approved
	f.approvals[step] = &v
	return nil
}

func writeSessionSubmit(t *testing.T, prep *controller.SessionPreparation, output map[string]any, supersedes string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "submit.json")
	b, err := json.Marshal(controller.SessionSubmission{Invocation: prep.Invocation, Output: output, SupersedesID: supersedes})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestWorkflowSessionGrillSpecRevisionApprovalPlanAuto(t *testing.T) {
	root := t.TempDir()
	t.Setenv("POLYFORGE_WORKSPACE_ROOT", root)
	if err := config.WriteStateFile(&config.StateFile{WIID: "wi_session", AttemptID: "ra_session", ClaimEpoch: 7, SessionSecret: "secret", Claimed: true}); err != nil {
		t.Fatal(err)
	}
	api := newSessionE2EAPI()

	grill, err := controller.PrepareSession(t.Context(), api, "wi_session", "grill", controller.Credentials{AttemptID: "ra_session", ClaimEpoch: 7, SessionSecret: "secret"})
	if err != nil {
		t.Fatal(err)
	}
	if len(grill.Inputs) != 0 || grill.ExpectedOutput == nil {
		t.Fatalf("bad grill preparation: %+v", grill)
	}
	grillFile := writeSessionSubmit(t, grill, map[string]any{"record": "Q: boundary? A: one project", "distilled_requirements": []any{"one project only"}, "open_questions": []any{}}, "")
	out, err := engineWorkflowDrive(t.Context(), api, "wi_session", engineWorkflowOptions{HasSubmit: true, SubmitFile: grillFile})
	if err != nil || out.Status != "approval_required" {
		t.Fatalf("grill submit: out=%+v err=%v", out, err)
	}
	if err := api.approve("grill", out.Submission.Artifact, true, "human"); err != nil {
		t.Fatal(err)
	}

	spec, err := controller.PrepareSession(t.Context(), api, "wi_session", "spec", controller.Credentials{AttemptID: "ra_session", ClaimEpoch: 7, SessionSecret: "secret"})
	if err != nil {
		t.Fatal(err)
	}
	if got := spec.Inputs[0].Value.([]any)[0]; got != "one project only" {
		t.Fatalf("spec input=%v", got)
	}
	if spec.Invocation.Grant.ProducerIsolation != workflow.IsolationShared {
		t.Fatalf("interactive spec preparation grant = %+v, want shared producer", spec.Invocation.Grant)
	}
	specFile := writeSessionSubmit(t, spec, map[string]any{"spec": "# draft", "requirements": []any{"R1"}}, "")
	out, err = engineWorkflowDrive(t.Context(), api, "wi_session", engineWorkflowOptions{HasSubmit: true, SubmitFile: specFile})
	if err != nil || out.Approval == nil {
		t.Fatalf("spec submit: out=%+v err=%v", out, err)
	}
	first := out.Submission.Artifact
	if err := api.approve("spec", first, false, "human"); err != nil {
		t.Fatal(err)
	}
	out, err = engineWorkflowDrive(t.Context(), api, "wi_session", engineWorkflowOptions{Continue: true})
	if err != nil || out.Status != "revision_required" || out.StepID != "spec" || !strings.Contains(out.Detail, "--prepare") {
		t.Fatalf("rejection advanced or omitted the revision protocol: out=%+v err=%v", out, err)
	}

	revised, err := controller.PrepareSession(t.Context(), api, "wi_session", "spec", controller.Credentials{AttemptID: "ra_session", ClaimEpoch: 7, SessionSecret: "secret"})
	if err != nil {
		t.Fatal(err)
	}
	revisedFile := writeSessionSubmit(t, revised, map[string]any{"spec": "# revised after discussion", "requirements": []any{"R1 exact"}}, first.ID)
	out, err = engineWorkflowDrive(t.Context(), api, "wi_session", engineWorkflowOptions{HasSubmit: true, SubmitFile: revisedFile})
	if err != nil || out.Submission.Artifact == first {
		t.Fatalf("revision did not create exact new artifact: out=%+v err=%v", out, err)
	}
	if err := api.approve("spec", first, true, "human"); err == nil {
		t.Fatal("stale spec artifact approval unexpectedly accepted")
	}
	if err := api.approve("spec", out.Submission.Artifact, true, "machine"); err == nil {
		t.Fatal("machine approval unexpectedly accepted")
	}
	if err := api.approve("spec", out.Submission.Artifact, true, "human"); err != nil {
		t.Fatal(err)
	}
	state, err := controller.Select(t.Context(), api, "wi_session")
	if err != nil {
		t.Fatal(err)
	}
	planInputs, err := controller.ResolveInputs(t.Context(), api, state, state.Steps.Steps[2].Inputs)
	if err != nil || planInputs[0].Value != "# revised after discussion" {
		t.Fatalf("automatic plan did not receive the exact revised spec: inputs=%+v err=%v", planInputs, err)
	}

	oldRun := runWorkflowStep
	defer func() { runWorkflowStep = oldRun }()
	runWorkflowStep = func(ctx context.Context, a controller.API, wiID, stepID, _ string, opts controller.StepOptions) (controller.StepOutcome, error) {
		raw, err := a.StartWorkflowStep(ctx, wiID, map[string]any{"attempt_id": opts.Credentials.AttemptID, "claim_epoch": opts.Credentials.ClaimEpoch, "session_secret": opts.Credentials.SessionSecret, "step_id": stepID})
		if err != nil {
			return controller.StepOutcome{}, err
		}
		b, _ := json.Marshal(raw)
		var inv controller.Invocation
		_ = json.Unmarshal(b, &inv)
		if stepID == "review" || stepID == "verify" {
			if opts.ExpectedGrant == nil || opts.ExpectedGrant.ProducerIsolation != workflow.IsolationRequired || opts.ExpectedGrant.Authority != workflow.AuthorityReadOnly {
				t.Fatalf("RHS %s worker grant = %+v, want read-only independent", stepID, opts.ExpectedGrant)
			}
			if len(inv.Models) != 1 || inv.Models[0].Harness != "cc" || inv.Models[0].Model != "fake" || inv.Models[0].Effort != "high" {
				t.Fatalf("RHS %s lost pinned model/effort during worker handoff: %+v", stepID, inv.Models)
			}
		}
		artifact := workflow.ArtifactRef{ID: "mem_plan_auto", Version: 1, Hash: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}
		result := workflow.StepResult{Status: workflow.StatusCompleted, WorkItemID: wiID, FlowVersion: inv.StepsVersion, StepID: stepID, StepAttemptID: inv.StepAttemptID, Epoch: int(inv.ClaimEpoch), ProducerID: inv.ProducerID, Artifact: artifact}
		if stepID == "review" {
			result.ReviewVerdict = workflow.ReviewPass
		}
		_, err = a.RecordWorkflowResult(ctx, wiID, map[string]any{"result": result})
		return controller.StepOutcome{Invocation: inv, Result: result, Recorded: err == nil}, err
	}
	out, err = engineWorkflowDrive(t.Context(), api, "wi_session", engineWorkflowOptions{Continue: true})
	if err != nil || out.Result == nil || api.progress["plan"].Status != workflow.StatusCompleted {
		t.Fatalf("automatic plan did not use shared step controller: out=%+v err=%v", out, err)
	}
	if out.Status == "complete" {
		t.Fatalf("automatic plan completed flow before both independent gates: out=%+v", out)
	}

	for _, gate := range []struct {
		step    string
		skill   string
		output  map[string]any
		verdict workflow.ReviewVerdict
	}{
		{step: "review", skill: "skill-review", output: map[string]any{"approval": "pass"}, verdict: workflow.ReviewPass},
		{step: "verify", skill: "skill-verify", output: map[string]any{"evidence": "verified"}},
	} {
		beforeOpen, beforeArtifacts := len(api.open), len(api.artifacts)
		_, err = controller.PrepareSession(t.Context(), api, "wi_session", gate.step, controller.Credentials{AttemptID: "ra_session", ClaimEpoch: 7, SessionSecret: "secret"})
		var hold *controller.SessionWorkerRequiredError
		if !errors.As(err, &hold) || hold.StepID != gate.step {
			t.Fatalf("RHS %s prepare error = %v, want typed shared-worker hold", gate.step, err)
		}
		if len(api.open) != beforeOpen {
			t.Fatalf("RHS %s prepare opened an invocation before refusing: open=%v", gate.step, api.open)
		}

		// Simulate a submit file produced by an older client that opened the
		// forbidden main-session invocation. Submit must not turn its echoed
		// producer_id into local gate authority or write an artifact/result.
		api.next++
		inv := controller.Invocation{
			InvocationID: fmt.Sprintf("winv_legacy_%d", api.next), StepsVersion: 1, StepID: gate.step,
			StepAttemptID: fmt.Sprintf("sa_legacy_%d", api.next), ProducerID: fmt.Sprintf("producer_legacy_%d", api.next),
			ClaimEpoch: 7, Grant: workflow.StepGrant{Authority: workflow.AuthorityReadOnly, ProducerIsolation: workflow.IsolationRequired},
			SkillID: gate.skill, SkillVersion: 1, EffectiveRHS: true,
		}
		api.open[gate.step] = inv
		_, err = controller.SubmitSession(t.Context(), api, "wi_session", controller.Credentials{AttemptID: "ra_session", ClaimEpoch: 7, SessionSecret: "secret"}, controller.SessionSubmission{
			Invocation: inv, Output: gate.output, Status: workflow.StatusCompleted, ReviewVerdict: gate.verdict,
			Evidence: []workflow.Evidence{{Kind: "test", Ref: gate.step, Hash: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}},
		})
		if !errors.As(err, &hold) || hold.StepID != gate.step {
			t.Fatalf("RHS %s submit error = %v, want typed shared-worker hold", gate.step, err)
		}
		_, recorded := api.progress[gate.step]
		if len(api.artifacts) != beforeArtifacts || recorded {
			t.Fatalf("RHS %s submit mutated artifacts/results: artifacts=%d progress=%+v", gate.step, len(api.artifacts), api.progress[gate.step])
		}
		delete(api.open, gate.step)

		out, err = engineWorkflowDrive(t.Context(), api, "wi_session", engineWorkflowOptions{Continue: true})
		if err != nil || out.Result == nil || api.progress[gate.step].Status != workflow.StatusCompleted {
			t.Fatalf("RHS %s was not routed through shared step controller: out=%+v err=%v", gate.step, out, err)
		}
		if out.Status != "approval_required" || out.Approval == nil {
			t.Fatalf("RHS %s worker result skipped exact human approval: out=%+v", gate.step, out)
		}
		if err := api.approve(gate.step, out.Approval.Artifact, true, "human"); err != nil {
			t.Fatalf("approve RHS %s result: %v", gate.step, err)
		}
	}
	out, err = engineWorkflowDrive(t.Context(), api, "wi_session", engineWorkflowOptions{Continue: true})
	if err != nil || out.Status != "complete" {
		t.Fatalf("automatic plan/RHS gates did not complete through shared step controller: out=%+v err=%v", out, err)
	}
}
