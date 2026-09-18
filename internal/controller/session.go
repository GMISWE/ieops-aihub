package controller

// session.go is the owner-required interactive adapter for a pinned workflow.
// It deliberately does not run a model. PrepareSession opens one server-fenced
// invocation and returns the exact immutable skill, concrete predecessor
// values, and output schema to the main harness. The main harness may discuss
// those instructions with the human and author ONLY the output object.
// SubmitSession validates that object, stores it through the existing
// attempt-authorized methodology artifact API, and records the exact accepted
// id/version/hash as the invocation result.

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"regexp"
	"strings"

	"github.com/GMISWE/ieops-aihub/internal/skillregistry"
	"github.com/GMISWE/ieops-aihub/internal/workflow"
)

// SessionAPI is the existing authenticated API surface used by the session
// adapter. Remember is the same POST /v1/memories path pf_save_artifact uses;
// no alternate artifact store or weaker authorization path is introduced.
type SessionAPI interface {
	API
	Remember(context.Context, any) (map[string]any, error)
}

// SessionPreparation is the complete, callable hand-off to a main harness.
// Invocation is the fence that SubmitSession must echo. SkillEntry and
// SupportingFiles are immutable copies fetched from the pinned exact version;
// no materialized skill directory is exposed for the main session to mutate.
type SessionPreparation struct {
	WorkItemID      string                    `json:"work_item_id"`
	StepsVersion    int                       `json:"steps_version"`
	StepID          string                    `json:"step_id"`
	RHS             bool                      `json:"rhs"`
	Invocation      Invocation                `json:"invocation"`
	SkillEntry      string                    `json:"skill_entry"`
	SupportingFiles []skillregistry.SkillFile `json:"supporting_files,omitempty"`
	Params          any                       `json:"params"`
	Inputs          []ResolvedInput           `json:"inputs"`
	ExpectedOutput  json.RawMessage           `json:"expected_output_schema"`
	Instructions    []string                  `json:"instructions"`
}

// SessionSubmission is the file shape accepted by `engine workflow --submit`.
// Invocation must be copied unchanged from --prepare; Output is the only
// authored part. A human/model cannot smuggle approval in this shape.
type SessionSubmission struct {
	Invocation    Invocation             `json:"invocation"`
	Output        map[string]any         `json:"output"`
	Status        workflow.ResultStatus  `json:"status,omitempty"`
	ReviewVerdict workflow.ReviewVerdict `json:"review_verdict,omitempty"`
	Evidence      []workflow.Evidence    `json:"evidence,omitempty"`
	SupersedesID  string                 `json:"supersedes_artifact_id,omitempty"`
}

// SessionSubmitResult names the exact artifact and server-recorded result.
type SessionSubmitResult struct {
	Artifact workflow.ArtifactRef `json:"artifact"`
	Result   workflow.StepResult  `json:"result"`
}

// SessionWorkerRequiredError is the typed, actionable hold returned when the
// main interactive producer is not allowed to perform a step. The caller may
// dispatch the step only through the shared RunStep path, which preflights the
// pinned model/effort candidate and enforces the server-minted grant. If an
// invocation is already open (for example, a submit file prepared by an older
// client), it must be reconciled or ended before that worker can be started.
type SessionWorkerRequiredError struct {
	WorkItemID string
	StepID     string
	Reason     string
	Detail     string
}

func (e *SessionWorkerRequiredError) Error() string {
	return fmt.Sprintf("workflow: step %s of %s requires the shared worker (%s): %s", e.StepID, e.WorkItemID, e.Reason, e.Detail)
}

// SessionWorkerRequirement classifies contracts that a main interactive
// producer must never execute locally. Capability is semantic and grant is
// authority; both are checked so neither a drifted grant nor a misleading RHS
// bit can collapse an independent gate into its producer's session.
func SessionWorkerRequirement(contract skillregistry.SkillContract, grant workflow.StepGrant) (string, bool) {
	for _, capability := range contract.Capabilities {
		switch capability {
		case skillregistry.CapReview:
			return "review_requires_worker", true
		case skillregistry.CapVerification:
			return "verification_requires_worker", true
		case skillregistry.CapShipping:
			return "shipping_requires_worker", true
		}
	}
	if grant.ProducerIsolation == workflow.IsolationRequired {
		return "independent_producer_required", true
	}
	return "", false
}

func sessionWorkerHold(wiID, stepID, reason string, invocationOpen bool) error {
	detail := "run polyforge engine workflow --work-item=" + wiID + " --continue to dispatch the pinned model and effort through controller preflight"
	if invocationOpen {
		detail = "this prepared invocation cannot be submitted by the main producer; end its live attempt (or reconcile the owning attempt after it is no longer live), then " + detail
	}
	return &SessionWorkerRequiredError{WorkItemID: wiID, StepID: stepID, Reason: reason, Detail: detail}
}

// PrepareSession starts exactly one manual/session invocation. It is valid for
// a human-facing interactive/effective-RHS authoring contract only. Independent
// gates and shipping always use the shared preflighted RunStep worker path,
// regardless of RHS.
func PrepareSession(ctx context.Context, api SessionAPI, wiID, stepID string, cred Credentials) (*SessionPreparation, error) {
	state, err := Select(ctx, api, wiID)
	if err != nil {
		return nil, err
	}
	if state == nil {
		return nil, fmt.Errorf("work item %s has no pinned workflow", wiID)
	}
	action := state.NextAction()
	if action.Kind == ActionRevisionRequired {
		if stepID == "" {
			stepID = action.StepID
		}
	} else if action.Kind != ActionRun {
		return nil, fmt.Errorf("workflow is not ready for session preparation: %s: %s", action.Kind, action.Detail)
	} else if stepID == "" {
		stepID = action.StepID
	}
	if stepID != action.StepID {
		return nil, fmt.Errorf("requested step %q is not the workflow's next step %q", stepID, action.StepID)
	}

	flow, grants, err := LoadValidatedFlow(ctx, api, state)
	if err != nil {
		return nil, fmt.Errorf("bind pinned workflow: %w", err)
	}
	var bound *workflow.ValidatedStep
	for _, s := range flow.BoundSteps() {
		if s.Step.ID == stepID {
			copy := s
			bound = &copy
			break
		}
	}
	if bound == nil {
		return nil, fmt.Errorf("pinned workflow has no bound step %q", stepID)
	}
	grant, ok := grants[stepID]
	if !ok {
		return nil, fmt.Errorf("pinned workflow has no derived grant for step %s", stepID)
	}
	if reason, required := SessionWorkerRequirement(bound.Contract, grant); required {
		return nil, sessionWorkerHold(state.WorkItemID, stepID, reason, false)
	}
	if !bound.Contract.Runtime.Interactive && !action.RHS && action.Kind != ActionRevisionRequired {
		return nil, fmt.Errorf("step %q is automatic; use --continue so it runs through the shared controller", stepID)
	}
	inputs, err := ResolveInputs(ctx, api, state, bound.Step.Inputs)
	if err != nil {
		return nil, err
	}
	// Resolve the exact bundle through the caller's registry access before
	// opening the invocation. A revoked share (or an invalid entry) must not
	// strand a live invocation that the caller cannot prepare or submit.
	rawSkill, err := api.GetSkillVersion(ctx, bound.Step.SkillID, bound.Step.SkillVersion)
	if err != nil {
		return nil, fmt.Errorf("fetch pinned skill: %w", err)
	}
	bundle, contract, err := decodeSessionSkill(rawSkill)
	if err != nil {
		return nil, err
	}
	if !reflect.DeepEqual(*contract, bound.Contract) {
		return nil, fmt.Errorf("fetched pinned contract differs from validated workflow binding")
	}
	entry := ""
	support := make([]skillregistry.SkillFile, 0, len(bundle.Files)-1)
	for _, f := range bundle.Files {
		if f.Path == bundle.Entry {
			if f.Encoding == skillregistry.EncodingFileBase64 {
				return nil, fmt.Errorf("pinned skill entry %q is binary", f.Path)
			}
			entry = f.Content
		} else {
			support = append(support, f)
		}
	}
	if strings.TrimSpace(entry) == "" {
		return nil, fmt.Errorf("pinned skill entry is empty")
	}
	inv, err := startInvocation(ctx, api, state.WorkItemID, stepID, StepOptions{
		Credentials: cred, ExpectedStepsVersion: state.StepsVersion, ExpectedGrant: &grant,
	})
	if err != nil {
		return nil, err
	}
	if inv.SkillID != bound.Step.SkillID || inv.SkillVersion != bound.Step.SkillVersion {
		return nil, fmt.Errorf("workflow invocation skill differs from authorized pinned skill")
	}
	if len(inv.Inputs) != len(bound.Step.Inputs) {
		return nil, fmt.Errorf("workflow invocation input descriptors changed during session preparation")
	}
	for i := range bound.Step.Inputs {
		if inv.Inputs[i] != bound.Step.Inputs[i] {
			return nil, fmt.Errorf("workflow invocation input descriptors changed during session preparation")
		}
	}
	var params any = map[string]any{}
	if len(inv.Params) > 0 {
		if err := json.Unmarshal(inv.Params, &params); err != nil {
			return nil, fmt.Errorf("decode invocation params: %w", err)
		}
	}
	return &SessionPreparation{
		WorkItemID: state.WorkItemID, StepsVersion: state.StepsVersion, StepID: stepID,
		RHS: inv.EffectiveRHS, Invocation: inv, SkillEntry: entry, SupportingFiles: support,
		Params: params, Inputs: inputs, ExpectedOutput: append(json.RawMessage(nil), contract.OutputSchema...),
		Instructions: []string{
			"Discuss the pinned instructions with the human; ask rather than synthesize any human decision.",
			"Author only the output object required by expected_output_schema; do not edit or rewrite the pinned skill bundle.",
			"Copy invocation unchanged into the submit file, then run engine workflow --submit=<file>.",
			"A completed RHS result still requires a separate authenticated human approval of the exact returned artifact before --continue can advance.",
		},
	}, nil
}

// SubmitSession validates and records one prepared session result. The artifact
// write occurs before result recording, and stale/open-invocation checks happen
// before that write so an obviously stale submit does not create an orphan.
func SubmitSession(ctx context.Context, api SessionAPI, wiID string, cred Credentials, sub SessionSubmission) (*SessionSubmitResult, error) {
	inv := sub.Invocation
	if inv.InvocationID == "" || inv.StepAttemptID == "" || inv.ProducerID == "" || inv.StepID == "" || inv.StepsVersion <= 0 {
		return nil, fmt.Errorf("submit file lacks the exact invocation returned by --prepare")
	}
	state, err := Select(ctx, api, wiID)
	if err != nil {
		return nil, err
	}
	if state == nil || state.WorkItemID != wiID && !strings.Contains(wiID, "#") || state.StepsVersion != inv.StepsVersion {
		return nil, fmt.Errorf("prepared invocation does not belong to the current workflow generation")
	}
	if inv.ClaimEpoch != cred.ClaimEpoch {
		return nil, fmt.Errorf("prepared invocation epoch %d does not match current claim epoch %d", inv.ClaimEpoch, cred.ClaimEpoch)
	}
	p := progressFor(state, inv.StepID)
	if p == nil || p.OpenInvocationID != inv.InvocationID {
		return nil, fmt.Errorf("prepared invocation %s is not the open invocation for step %s", inv.InvocationID, inv.StepID)
	}
	var pinned *workflow.Step
	for i := range state.Steps.Steps {
		if state.Steps.Steps[i].ID == inv.StepID {
			pinned = &state.Steps.Steps[i]
			break
		}
	}
	if pinned == nil || pinned.SkillID != inv.SkillID || pinned.SkillVersion != inv.SkillVersion {
		return nil, fmt.Errorf("prepared invocation's skill does not match the pinned workflow step")
	}
	// Registry sharing can be revoked after the invocation starts. Do not
	// re-read the skill through the caller's visibility here: the server's
	// RecordWorkflowResult path validates the stored artifact and its output
	// against the trusted pinned contract, even after that visibility lapses.
	// Independent/read-only grants cannot be submitted by the main producer.
	if inv.Grant.ProducerIsolation != workflow.IsolationShared {
		return nil, sessionWorkerHold(state.WorkItemID, inv.StepID, "independent_producer_required", true)
	}
	status := sub.Status
	if status == "" {
		status = workflow.StatusCompleted
	}
	switch status {
	case workflow.StatusCompleted, workflow.StatusIncomplete, workflow.StatusBlocked, workflow.StatusProviderError, workflow.StatusInvalidResult:
	default:
		return nil, fmt.Errorf("submitted status %q is not a workflow result status", status)
	}
	if sub.ReviewVerdict != "" {
		return nil, fmt.Errorf("main-session submission cannot carry review_verdict")
	}
	for i, evidence := range sub.Evidence {
		if evidence.Kind == "" || strings.TrimSpace(evidence.Ref) == "" || !workflowDigestRE.MatchString(evidence.Hash) {
			return nil, fmt.Errorf("evidence %d must carry kind, ref, and a lowercase sha256 digest", i)
		}
	}
	content, artifactType, structured := sessionArtifactBody(inv.StepID, sub.Output)
	body := map[string]any{
		"type": artifactType, "work_item_id": state.WorkItemID, "content": content,
		"structured_payload": structured, "visibility": "project", "dedup_mode": "off",
		"attempt_id": cred.AttemptID, "claim_epoch": cred.ClaimEpoch, "session_secret": cred.SessionSecret,
	}
	if sub.SupersedesID != "" {
		body["supersedes_memory_id"] = sub.SupersedesID
	}
	stored, err := api.Remember(ctx, body)
	if err != nil {
		return nil, fmt.Errorf("save session artifact through the authorized artifact API: %w", err)
	}
	artifactID, _ := stored["id"].(string)
	if artifactID == "" {
		artifactID, _ = stored["memory_id"].(string)
	}
	if artifactID == "" {
		return nil, fmt.Errorf("artifact API returned no immutable id")
	}
	// A memory id identifies one immutable artifact row. Revisions receive a
	// new id (and may supersede the old id), so the per-id artifact version is
	// always 1; identity across revisions is the new id plus this content hash.
	artifact := workflow.ArtifactRef{ID: artifactID, Version: 1, Hash: WorkflowArtifactHash(sub.Output)}
	result := workflow.StepResult{
		Status: status, ReviewVerdict: sub.ReviewVerdict, WorkItemID: state.WorkItemID,
		FlowVersion: inv.StepsVersion, StepID: inv.StepID, StepAttemptID: inv.StepAttemptID,
		Epoch: int(inv.ClaimEpoch), ProducerID: inv.ProducerID, Artifact: artifact, Evidence: sub.Evidence,
	}
	if _, err := api.RecordWorkflowResult(ctx, state.WorkItemID, map[string]any{
		"attempt_id": cred.AttemptID, "claim_epoch": cred.ClaimEpoch, "session_secret": cred.SessionSecret,
		"result": result,
	}); err != nil {
		return nil, fmt.Errorf("artifact %s saved but workflow result was not recorded: %w", artifact.ID, err)
	}
	return &SessionSubmitResult{Artifact: artifact, Result: result}, nil
}

func decodeSessionSkill(raw map[string]any) (*skillregistry.SkillBundle, *skillregistry.SkillContract, error) {
	b, err := json.Marshal(raw)
	if err != nil {
		return nil, nil, err
	}
	var wire struct {
		Bundle   json.RawMessage `json:"bundle"`
		Contract json.RawMessage `json:"contract"`
	}
	if err := json.Unmarshal(b, &wire); err != nil {
		return nil, nil, err
	}
	bundle, err := skillregistry.DecodeBundle(wire.Bundle)
	if err != nil {
		return nil, nil, fmt.Errorf("decode pinned bundle: %w", err)
	}
	contract, err := skillregistry.DecodeContract(wire.Contract)
	if err != nil {
		return nil, nil, fmt.Errorf("decode pinned contract: %w", err)
	}
	return bundle, contract, nil
}

var (
	artifactTypeToken = regexp.MustCompile(`[^a-z0-9_]+`)
	workflowDigestRE  = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
)

func sessionArtifactBody(stepID string, output map[string]any) (string, string, map[string]any) {
	if s, ok := output["spec"].(string); ok {
		return s, "methodology.spec", output
	}
	if s, ok := output["plan"].(string); ok {
		return s, "methodology.plan", output
	}
	if s, ok := output["record"].(string); ok {
		return s, "methodology.execute", output
	}
	b, _ := json.MarshalIndent(output, "", "  ")
	token := strings.Trim(artifactTypeToken.ReplaceAllString(strings.ToLower(stepID), "_"), "_")
	if token == "" {
		token = "execute"
	}
	return string(b), "methodology." + token, output
}
