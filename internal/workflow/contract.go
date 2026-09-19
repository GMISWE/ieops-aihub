// Package workflow defines the pure, storage-independent contracts for pinned
// skill flows and their results. It deliberately does not execute skills or
// turn a skill's capability declaration into execution authority.
package workflow

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"

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
	// Output is the worker's TRANSIENT structured output object (aihub#725):
	// a completed result of a credentialless worker may carry its business
	// output here instead of an artifact triple, and the controller-side sink
	// stores exactly this object as a methodology artifact's
	// attrs.structured_payload before recording the result. It is never part
	// of a RECORDED result — the controller clears it before calling
	// RecordWorkflowResult, and `omitempty` keeps it off the wire once
	// cleared, so the recorded-result shape stays byte-identical to what a
	// pre-sink worker produced. Numbers inside it decode as json.Number (the
	// decoder below sets UseNumber), so the envelope boundary is lossless and
	// the CONTROLLER — not the decode — decides the number semantics the sink
	// hashes and stores (see sinkWorkerOutput).
	Output map[string]any `json:"output,omitempty"`
}

// stepResultKeys is the CLOSED top-level key set of the worker result
// envelope. Anything else is refused: the envelope is untrusted worker
// stdout (or an agent-supplied MCP `result` object), and a silently ignored
// unknown key is exactly how an attempted privilege escalation or a
// doctored digest would travel (aihub#725 S1). `approval` is deliberately
// NOT here — it keeps its own, older refusal message below.
var stepResultKeys = map[string]bool{
	"status": true, "review_verdict": true, "work_item_id": true,
	"flow_version": true, "step_id": true, "step_attempt_id": true,
	"epoch": true, "producer_id": true, "artifact": true,
	"evidence": true, "output": true,
}

// stepResultClosedKeys names the envelope's CONTRACT sub-objects, whose key
// sets are closed like the top level's: `artifact` and each `evidence`
// entry are typed contract structs, so an unknown key inside one is the same
// class of silent channel as an unknown top-level key — a smuggled field the
// decoder would drop while the raw bytes still say something else. `output`
// is deliberately NOT here: it is the worker's business payload and its keys
// are arbitrary (only duplicate keys are refused there, by the token walk
// above). (aihub#725 review_fix B1: the top-level whitelist used to be the
// only strictness at depth, so `"artifact":{"id":...,"overflow":...}`
// passed.)

// UnmarshalJSON is a STRICT, single-object decode of one worker result
// envelope (aihub#725 S1). json.Unmarshal on a struct that carries its own
// UnmarshalJSON cannot be made strict from the outside — a caller-side
// json.Decoder.DisallowUnknownFields never reaches inside a custom
// unmarshaler — so the strictness lives here, where every decode path
// (controller stdout, server RecordWorkflowResult, MCP result) shares it:
//
//   - `approval` is refused, as before — a worker result cannot grant
//     approval;
//   - any unknown top-level key is refused rather than silently dropped;
//   - duplicate object keys are refused at ANY nesting depth (encoding/json
//     otherwise keeps the last value of a duplicate, letting a crafted
//     envelope present one digest to a reader and another to the machine);
//   - trailing content after the object is refused by the caller's
//     json.Unmarshal (its own contract: the whole input must be one value);
//   - numbers decode with json.Number semantics (UseNumber), so the
//     envelope boundary is lossless: the worker's number literals survive
//     the decode verbatim, and the number SEMANTICS the sink hashes and
//     stores remain a controller decision rather than something a silent
//     float64 decode decided.
//
// A refusal here happens BEFORE any side effect: the controller saves no
// artifact and records no result (the invocation stays open), and the server
// refuses the whole result transaction.
func (r *StepResult) UnmarshalJSON(data []byte) error {
	if err := rejectDuplicateKeys(json.NewDecoder(bytes.NewReader(data))); err != nil {
		return err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return err
	}
	if _, supplied := fields["approval"]; supplied {
		return errors.New("worker result cannot supply approval")
	}
	for key := range fields {
		if !stepResultKeys[key] {
			return fmt.Errorf("worker result carries unknown field %q", key)
		}
	}
	// Closed sub-objects (aihub#725 review_fix B1): `artifact` and every
	// `evidence` entry refuse unknown keys the same way the envelope does,
	// BEFORE the permissive decode below could silently discard them. A
	// refusal here is a refusal of the whole envelope — the same
	// before-any-side-effect guarantee as the checks above.
	if raw, supplied := fields["artifact"]; supplied {
		var probe ArtifactRef
		if err := decodeClosedContractValue(raw, &probe); err != nil {
			return fmt.Errorf("worker result artifact: %w", err)
		}
	}
	if raw, supplied := fields["evidence"]; supplied {
		var probe []Evidence
		if err := decodeClosedContractValue(raw, &probe); err != nil {
			return fmt.Errorf("worker result evidence: %w", err)
		}
	}
	type envelope StepResult
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	var decoded envelope
	if err := dec.Decode(&decoded); err != nil {
		return err
	}
	*r = StepResult(decoded)
	return nil
}

// decodeClosedContractValue decodes one already-extracted envelope
// sub-value into a CLOSED contract struct (or slice of them): unknown keys
// at this depth are refused instead of silently discarded, and numbers keep
// json.Number semantics for the same lossless-boundary reason as the whole
// envelope. DisallowUnknownFields applies to every struct the decoder
// reaches — the slice case covers each Evidence element. No trailing check
// is needed: raw is a single JSON value carved out of a map[string]RawMessage
// decode of the envelope, which already guarantees exactly one value.
func decodeClosedContractValue(raw json.RawMessage, into any) error {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	dec.DisallowUnknownFields()
	return dec.Decode(into)
}

// rejectDuplicateKeys walks one whole JSON value and errors on the first
// object that repeats a key, at any nesting depth. It is a validation pass
// only — the actual decode happens afterwards — so a refusal here means no
// field of the envelope was ever consumed.
func rejectDuplicateKeys(dec *json.Decoder) error {
	t, err := dec.Token()
	if err != nil {
		return err
	}
	delim, ok := t.(json.Delim)
	if !ok {
		return nil // a scalar: nothing to check
	}
	switch delim {
	case '{':
		seen := make(map[string]struct{})
		for dec.More() {
			kt, err := dec.Token()
			if err != nil {
				return err
			}
			key, ok := kt.(string)
			if !ok {
				return fmt.Errorf("object key %v is not a string", kt)
			}
			if _, dup := seen[key]; dup {
				return fmt.Errorf("duplicate object key %q", key)
			}
			seen[key] = struct{}{}
			if err := rejectDuplicateKeys(dec); err != nil {
				return err
			}
		}
		_, err = dec.Token()
		return err
	case '[':
		for dec.More() {
			if err := rejectDuplicateKeys(dec); err != nil {
				return err
			}
		}
		_, err = dec.Token()
		return err
	default:
		return nil
	}
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
