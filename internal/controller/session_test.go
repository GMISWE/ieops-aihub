package controller

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/GMISWE/ieops-aihub/internal/skillregistry"
	"github.com/GMISWE/ieops-aihub/internal/workflow"
)

// revocableSessionAPI models the distinction between caller-visible registry
// reads and the trusted server result validator. Revocation blocks a new
// start, but does not invalidate an already authorized invocation.
type revocableSessionAPI struct {
	revoked                bool
	entry                  string
	starts, reads, records int
	open                   string
	stored                 map[string]any
}

const sessionSchema = `{"type":"object","properties":{"spec":{"type":"string"}},"required":["spec"],"additionalProperties":false}`

func (f *revocableSessionAPI) GetWorkItemWorkflow(context.Context, string) (map[string]any, error) {
	return map[string]any{
		"work_item_id": "wi_session", "steps_version": 1, "requires_human_session": true,
		"steps": map[string]any{"version": 1, "steps": []any{map[string]any{
			"id": "spec", "skill_id": "skill-spec", "skill_version": 1, "rhs": true,
			"models": []any{map[string]any{"harness": "cc", "model": "fake", "effort": "high"}},
		}}},
		"progress": []any{map[string]any{"step_id": "spec", "rhs": true, "open_invocation_id": f.open}},
	}, nil
}
func (f *revocableSessionAPI) GetMemory(context.Context, string) (map[string]any, error) {
	return nil, errors.New("unexpected input read")
}
func (f *revocableSessionAPI) GetSkillVersion(context.Context, string, int) (map[string]any, error) {
	f.reads++
	if f.revoked {
		return nil, errors.New("skill sharing revoked")
	}
	return map[string]any{
		"bundle":   map[string]any{"entry": "SKILL.md", "files": []any{map[string]any{"path": "SKILL.md", "content": f.entry}}, "license": map[string]any{"name": "Proprietary"}},
		"contract": map[string]any{"capabilities": []any{"deterministic_operation"}, "runtime": map[string]any{"interactive": true}, "output_schema": json.RawMessage(sessionSchema)},
	}, nil
}
func (f *revocableSessionAPI) StartWorkflowStep(_ context.Context, _ string, _ any) (map[string]any, error) {
	if f.revoked {
		return nil, errors.New("start denied: skill sharing revoked")
	}
	f.starts++
	f.open = "inv_session"
	return map[string]any{
		"invocation_id": f.open, "steps_version": 1, "step_id": "spec", "step_attempt_id": "sa_session",
		"producer_id": "producer_session", "claim_epoch": 7, "skill_id": "skill-spec", "skill_version": 1,
		"grant": map[string]any{"authority": "read_only", "producer_isolation": "shared"}, "effective_rhs": true,
	}, nil
}
func (*revocableSessionAPI) ReconcileWorkflowInvocations(context.Context, string, any) (map[string]any, error) {
	return nil, errors.New("unexpected reconcile")
}
func (f *revocableSessionAPI) Remember(_ context.Context, body any) (map[string]any, error) {
	f.stored = body.(map[string]any)["structured_payload"].(map[string]any)
	return map[string]any{"id": "mem_session"}, nil
}
func (f *revocableSessionAPI) RecordWorkflowResult(_ context.Context, _ string, body any) (map[string]any, error) {
	result := body.(map[string]any)["result"].(workflow.StepResult)
	if result.StepAttemptID != "sa_session" || result.ProducerID != "producer_session" || f.open != "inv_session" {
		return nil, errors.New("stale invocation")
	}
	if result.Artifact.ID != "mem_session" || result.Artifact.Version != 1 || result.Artifact.Hash != WorkflowArtifactHash(f.stored) {
		return nil, errors.New("invalid artifact identity")
	}
	schema, err := skillregistry.CompileSchema(json.RawMessage(sessionSchema))
	if err != nil {
		return nil, err
	}
	if err := schema.CheckValue(f.stored); err != nil {
		return nil, fmt.Errorf("trusted pinned schema: %w", err)
	}
	f.records++
	f.open = ""
	return map[string]any{"recorded": true}, nil
}

func TestPrepareSessionChecksBundleBeforeStart(t *testing.T) {
	cred := Credentials{AttemptID: "ra_session", ClaimEpoch: 7, SessionSecret: "secret"}
	for _, tc := range []struct {
		name, entry string
		revoked     bool
	}{
		{"revoked", "Pinned instructions", true},
		{"empty entry", "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			api := &revocableSessionAPI{entry: tc.entry, revoked: tc.revoked}
			if _, err := PrepareSession(t.Context(), api, "wi_session", "spec", cred); err == nil {
				t.Fatal("expected preparation failure")
			}
			if api.starts != 0 || api.open != "" {
				t.Fatalf("failed preparation left open invocation: starts=%d open=%q", api.starts, api.open)
			}
		})
	}
}

func TestSessionSubmissionSurvivesSharingRevocation(t *testing.T) {
	api := &revocableSessionAPI{entry: "Pinned instructions"}
	cred := Credentials{AttemptID: "ra_session", ClaimEpoch: 7, SessionSecret: "secret"}
	prep, err := PrepareSession(t.Context(), api, "wi_session", "spec", cred)
	if err != nil {
		t.Fatal(err)
	}
	if prep.SkillEntry != api.entry || !strings.Contains(string(prep.ExpectedOutput), `"spec"`) || api.starts != 1 {
		t.Fatalf("preparation lost authorized bundle/schema: %+v", prep)
	}
	api.revoked = true
	reads := api.reads
	out, err := SubmitSession(t.Context(), api, "wi_session", cred, SessionSubmission{
		Invocation: prep.Invocation, Output: map[string]any{"spec": "# authored"},
	})
	if err != nil {
		t.Fatalf("already-authorized invocation must finish after revocation: %v", err)
	}
	if api.reads != reads || api.records != 1 || api.open != "" || out.Result.Artifact != out.Artifact {
		t.Fatalf("submit re-read registry or lost recorded result: reads=%d records=%d open=%q out=%+v", api.reads, api.records, api.open, out)
	}
	if _, err := PrepareSession(t.Context(), api, "wi_session", "spec", cred); err == nil || !strings.Contains(err.Error(), "revoked") {
		t.Fatalf("new preparation after revocation should fail: %v", err)
	}
	if api.starts != 1 {
		t.Fatalf("revoked skill opened another invocation: %d", api.starts)
	}
}

func TestSessionWorkerRequirement(t *testing.T) {
	sharedWrite := workflow.StepGrant{Authority: workflow.AuthorityWrite, ProducerIsolation: workflow.IsolationShared}
	independentRead := workflow.StepGrant{Authority: workflow.AuthorityReadOnly, ProducerIsolation: workflow.IsolationRequired}

	for _, tc := range []struct {
		name     string
		contract skillregistry.SkillContract
		grant    workflow.StepGrant
		want     bool
	}{
		{name: "interactive authoring stays in session", contract: skillregistry.SkillContract{Capabilities: []skillregistry.Capability{skillregistry.CapAuthoring}, Runtime: skillregistry.RuntimeSpec{Interactive: true}}, grant: sharedWrite},
		{name: "review uses worker", contract: skillregistry.SkillContract{Capabilities: []skillregistry.Capability{skillregistry.CapReview}}, grant: independentRead, want: true},
		{name: "verification uses worker", contract: skillregistry.SkillContract{Capabilities: []skillregistry.Capability{skillregistry.CapVerification}}, grant: independentRead, want: true},
		{name: "shipping uses worker", contract: skillregistry.SkillContract{Capabilities: []skillregistry.Capability{skillregistry.CapShipping}}, grant: sharedWrite, want: true},
		{name: "independent grant uses worker", contract: skillregistry.SkillContract{Capabilities: []skillregistry.Capability{skillregistry.CapDeterministicOperation}}, grant: independentRead, want: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reason, got := SessionWorkerRequirement(tc.contract, tc.grant)
			if got != tc.want {
				t.Fatalf("SessionWorkerRequirement() required=%v reason=%q, want required=%v", got, reason, tc.want)
			}
			if got && reason == "" {
				t.Fatal("worker-required classification lacks a stable reason")
			}
		})
	}
}
