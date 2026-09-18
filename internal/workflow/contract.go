// Package workflow defines the pure, storage-independent contracts for pinned
// skill flows and their results. It deliberately does not execute skills or
// turn a skill's capability declaration into execution authority.
package workflow

import (
	"encoding/json"
	"errors"

	"github.com/GMISWE/ieops-aihub/internal/skillregistry"
)

// Flow is an immutable, versioned sequence. A result pins FlowVersion so a
// result from an older definition cannot satisfy a newer flow accidentally.
type Flow struct {
	Version int    `json:"version"`
	Steps   []Step `json:"steps"`
}

// Step pins one published skill version. Omitted RHS defaults to false.
// It is metadata, not a grant of runtime authority.
type Step struct {
	ID           string           `json:"id"`
	SkillID      string           `json:"skill_id"`
	SkillVersion int              `json:"skill_version"`
	RHS          *bool            `json:"rhs"`
	Models       []ModelCandidate `json:"models"`
	Params       json.RawMessage  `json:"params,omitempty"`
	Inputs       []InputRef       `json:"inputs,omitempty"`
}

// ModelCandidate is one preference-ordered, harness-native model route.
// Effort is intentionally a token rather than a closed vocabulary: supported
// effort names are harness/model specific, but empty or malformed shapes are
// rejected before dispatch.
type ModelCandidate struct {
	Harness string `json:"harness"`
	Model   string `json:"model"`
	Effort  string `json:"effort"`
}

// InputRef binds a named input to an output of an earlier step. Output is a
// contract-local output name, not a URL or a dynamic skill reference.
type InputRef struct {
	Name   string `json:"name"`
	StepID string `json:"step_id"`
	Output string `json:"output"`
}

// ExecutionAuthority is an explicit runtime grant. Skill capabilities are
// declarations and never imply either value.
type ExecutionAuthority string

const (
	AuthorityReadOnly ExecutionAuthority = "read_only"
	AuthorityWrite    ExecutionAuthority = "write"
)

// ProducerIsolation is an explicit scheduling grant. Isolated means the
// runtime must use a producer identity distinct from the work it gates.
type ProducerIsolation string

const (
	IsolationShared   ProducerIsolation = "shared"
	IsolationRequired ProducerIsolation = "independent"
)

// StepGrant is supplied by the trusted controller, never the serialized Flow. A contract saying
// "shipping" declares semantics; it does not authorize writes or waive
// producer isolation.
type StepGrant struct {
	Authority         ExecutionAuthority `json:"authority"`
	ProducerIsolation ProducerIsolation  `json:"producer_isolation"`
}

// ExecutionContext contains controller-established grants for every step.
// The server must authenticate the caller and establish these values itself.
type ExecutionContext struct {
	Grants map[string]StepGrant
}

// PolicyInput is controller state, not part of a scenario or worker result.
type PolicyInput struct {
	RequiresHumanSession bool
	Expected             []ExpectedInvocation
	Approvals            []HumanApproval
	Repair               *RepairAuthorization
	RepairEpisodes       []RepairEpisode
}

// ExpectedInvocation binds each worker result to a controller-issued invocation.
// A matching envelope is not proof of signed origin; the server authenticates it.
type ExpectedInvocation struct {
	WorkItemID    string
	FlowVersion   int
	StepID        string
	StepAttemptID string
	Epoch         int
	ProducerID    string
}

// HumanApproval must be established by the controller from an authenticated
// human decision, bound to the exact immutable artifact and workflow instance.
type HumanApproval struct {
	WorkItemID  string
	FlowVersion int
	StepID      string
	Artifact    ArtifactRef
	ActorID     string
	Decision    ApprovalDecision
}

// SkillBinding is the already-authorized registry resolution for one exact
// (skill_id, version) pair. Validate binds only these exact refs; it has no
// "latest", range, or fallback path.
type SkillBinding struct {
	SkillID  string
	Version  int
	Contract skillregistry.SkillContract
}

// ValidatedFlow is the immutable result of structural and policy validation.
// Callers can inspect the original definition and each resolved contract but
// cannot substitute an unvalidated binding.
type ValidatedFlow struct {
	flow   Flow
	steps  []ValidatedStep
	grants map[string]StepGrant
}

// ValidatedStep binds a step to its exact registry contract.
type ValidatedStep struct {
	Step     Step
	Contract skillregistry.SkillContract
}

// Definition returns a defensive copy of the validated flow definition.
func (f *ValidatedFlow) Definition() Flow {
	if f == nil {
		return Flow{}
	}
	return cloneFlow(f.flow)
}

// BoundSteps returns defensive copies of the validated bound steps.
func (f *ValidatedFlow) BoundSteps() []ValidatedStep {
	if f == nil {
		return nil
	}
	out := make([]ValidatedStep, len(f.steps))
	for i := range f.steps {
		out[i] = cloneValidatedStep(f.steps[i])
	}
	return out
}

// Unattended applies WI RHS and step RHS together. A step marked rhs=true
// does not override a WI with requires_human_session=false; interactive-only
// contracts always refuse unattended execution.
func (f *ValidatedFlow) Unattended(requiresHumanSession bool) bool {
	if f == nil || len(f.steps) == 0 {
		return false
	}
	for _, s := range f.steps {
		if s.Contract.Runtime.Interactive || (requiresHumanSession && s.Step.RHS != nil && *s.Step.RHS) {
			return false
		}
	}
	return true
}

// ResultStatus describes execution only; review verdict is separate.
type ResultStatus string

const (
	StatusCompleted     ResultStatus = "completed"
	StatusIncomplete    ResultStatus = "incomplete"
	StatusBlocked       ResultStatus = "blocked"
	StatusProviderError ResultStatus = "provider_error"
	StatusInvalidResult ResultStatus = "invalid_result"
)

type ReviewVerdict string

const (
	ReviewPass ReviewVerdict = "pass"
	ReviewWarn ReviewVerdict = "warn"
	ReviewFail ReviewVerdict = "fail"
)

// StepResult is untrusted worker output. In particular it cannot grant approval.
type StepResult struct {
	Status        ResultStatus  `json:"status"`
	ReviewVerdict ReviewVerdict `json:"review_verdict,omitempty"`
	WorkItemID    string        `json:"work_item_id"`
	FlowVersion   int           `json:"flow_version"`
	StepID        string        `json:"step_id"`
	StepAttemptID string        `json:"step_attempt_id"`
	Epoch         int           `json:"epoch"`
	ProducerID    string        `json:"producer_id"`
	Artifact      ArtifactRef   `json:"artifact"`
	Evidence      []Evidence    `json:"evidence,omitempty"`
}

// UnmarshalJSON rejects legacy worker-supplied approval rather than silently
// dropping an attempted privilege escalation.
func (r *StepResult) UnmarshalJSON(data []byte) error {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return err
	}
	if _, supplied := fields["approval"]; supplied {
		return errors.New("worker result cannot supply approval")
	}
	type envelope StepResult
	var decoded envelope
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}
	*r = StepResult(decoded)
	return nil
}

// ArtifactRef identifies immutable output. Hash must be a full sha256 digest.
type ArtifactRef struct {
	ID      string `json:"id"`
	Version int    `json:"version"`
	Hash    string `json:"hash"`
}

// Evidence is a typed, immutable proof reference.
type Evidence struct {
	Kind string `json:"kind"`
	Ref  string `json:"ref"`
	Hash string `json:"hash"`
}

// ApprovalDecision is explicit; an omitted boolean can never become approval.
type ApprovalDecision string

const (
	ApprovalGranted ApprovalDecision = "approved"
	ApprovalDenied  ApprovalDecision = "rejected"
)

// RepairAuthorization is controller-established authority binding a prior failed
// attempt to a specific retry invocation, not a worker-supplied actor string.
type RepairAuthorization struct {
	FailedStepAttemptID string
	Retry               ExpectedInvocation
	Authorized          bool
	Reason              string
}

// RepairEpisode is a bounded, controller-authorized recovery from a concrete
// failed review. Every invocation in the episode is pinned so no old gate or
// implicit reroll can be substituted for the repaired producer.
type RepairEpisode struct {
	FailedReviewStepAttemptID string
	RepairedProducer          ExpectedInvocation
	Verification              ExpectedInvocation
	Review                    ExpectedInvocation
	Authorized                bool
	Reason                    string
}

// Decision is the only progression vocabulary. Invalid histories are returned
// as errors; they are never softened into WARN or WAIT.
type Decision string

const (
	DecisionAdvance Decision = "advance"
	DecisionWait    Decision = "wait"
	DecisionPause   Decision = "pause"
)
