package workflow

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"

	"github.com/GMISWE/ieops-aihub/internal/skillregistry"
)

var (
	identifierRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]*$`)
	tokenRE      = regexp.MustCompile(`^[a-z][a-z0-9_-]*$`)
	sha256RE     = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
)

type skillKey struct {
	id      string
	version int
}

// Validate binds a flow to exact, already-resolved registry contracts and
// checks all authoring-time invariants. It performs no I/O and does not grant
// access to a registry version; the caller must resolve accessible bindings
// before calling it.
func Validate(flow Flow, bindings []SkillBinding, context ExecutionContext) (*ValidatedFlow, error) {
	if flow.Version <= 0 {
		return nil, errors.New("flow version must be positive")
	}
	if len(flow.Steps) == 0 {
		return nil, errors.New("flow has no steps")
	}

	bound := make(map[skillKey]skillregistry.SkillContract, len(bindings))
	for i, b := range bindings {
		if !validIdentifier(b.SkillID) || b.Version <= 0 {
			return nil, fmt.Errorf("binding %d does not name an exact positive skill_id/version", i)
		}
		key := skillKey{b.SkillID, b.Version}
		if _, exists := bound[key]; exists {
			return nil, fmt.Errorf("binding %d duplicates exact skill ref %s@%d", i, b.SkillID, b.Version)
		}
		if err := skillregistry.ValidateContract(&b.Contract); err != nil {
			return nil, fmt.Errorf("binding %s@%d has an invalid contract: %w", b.SkillID, b.Version, err)
		}
		bound[key] = cloneContract(b.Contract)
	}

	validated := &ValidatedFlow{flow: cloneFlow(flow), steps: make([]ValidatedStep, 0, len(flow.Steps)), grants: make(map[string]StepGrant)}
	stepIndex := make(map[string]int, len(flow.Steps))
	for i, step := range flow.Steps {
		if err := validateStepShape(i, step); err != nil {
			return nil, err
		}
		if prior, exists := stepIndex[step.ID]; exists {
			return nil, fmt.Errorf("step %d duplicates id %q first used by step %d", i, step.ID, prior)
		}
		stepIndex[step.ID] = i

		contract, ok := bound[skillKey{step.SkillID, step.SkillVersion}]
		if !ok {
			return nil, fmt.Errorf("step %q has no resolved binding for exact skill ref %s@%d", step.ID, step.SkillID, step.SkillVersion)
		}
		grant, granted := context.Grants[step.ID]
		if !granted {
			return nil, fmt.Errorf("step %q missing trusted grant", step.ID)
		}
		if err := validateGrant(i, step.ID, grant); err != nil {
			return nil, err
		}
		validated.grants[step.ID] = grant
		if contract.Runtime.Interactive && (step.RHS == nil || !*step.RHS) {
			return nil, fmt.Errorf("step %q resolves to an interactive-only skill but rhs=false would permit unattended execution", step.ID)
		}
		if err := validateParams(step.ID, step.Params, contract.ParamsSchema); err != nil {
			return nil, err
		}
		if err := validateInputs(i, step, stepIndex, validated.steps, contract); err != nil {
			return nil, err
		}
		validated.steps = append(validated.steps, ValidatedStep{
			Step:     cloneStep(step),
			Contract: cloneContract(contract),
		})
	}
	if len(context.Grants) != len(flow.Steps) {
		return nil, errors.New("execution context must grant every step exactly once")
	}
	if err := validateSafetyGates(validated.steps, validated.grants); err != nil {
		return nil, err
	}
	return validated, nil
}

func validateStepShape(index int, step Step) error {
	prefix := fmt.Sprintf("step %d", index)
	if !validIdentifier(step.ID) {
		return fmt.Errorf("%s has invalid id %q", prefix, step.ID)
	}
	if !validIdentifier(step.SkillID) || step.SkillVersion <= 0 {
		return fmt.Errorf("%s %q must pin an exact positive skill_id/version", prefix, step.ID)
	}
	if len(step.Models) == 0 {
		return fmt.Errorf("%s %q has no model candidates", prefix, step.ID)
	}
	seen := make(map[string]bool, len(step.Models))
	for j, candidate := range step.Models {
		if !tokenRE.MatchString(candidate.Harness) {
			return fmt.Errorf("%s %q model candidate %d has invalid harness %q", prefix, step.ID, j, candidate.Harness)
		}
		if candidate.Model == "" || candidate.Model != strings.TrimSpace(candidate.Model) || strings.ContainsAny(candidate.Model, "\r\n\t") {
			return fmt.Errorf("%s %q model candidate %d has an invalid model", prefix, step.ID, j)
		}
		if !tokenRE.MatchString(candidate.Effort) {
			return fmt.Errorf("%s %q model candidate %d has invalid effort %q", prefix, step.ID, j, candidate.Effort)
		}
		key := candidate.Harness + "\x00" + candidate.Model + "\x00" + candidate.Effort
		if seen[key] {
			return fmt.Errorf("%s %q duplicates model candidate %d", prefix, step.ID, j)
		}
		seen[key] = true
	}
	return nil
}

func validateGrant(index int, id string, grant StepGrant) error {
	prefix := fmt.Sprintf("step %d %q", index, id)
	switch grant.Authority {
	case AuthorityReadOnly, AuthorityWrite:
	default:
		return fmt.Errorf("%s %q has no valid execution authority grant (want %q or %q)", prefix, id, AuthorityReadOnly, AuthorityWrite)
	}
	switch grant.ProducerIsolation {
	case IsolationShared, IsolationRequired:
	default:
		return fmt.Errorf("%s %q has no valid producer-isolation grant (want %q or %q)", prefix, id, IsolationShared, IsolationRequired)
	}
	return nil
}

func validateInputs(index int, step Step, prior map[string]int, priorSteps []ValidatedStep, consumer skillregistry.SkillContract) error {
	seen := make(map[string]bool, len(step.Inputs))
	for j, input := range step.Inputs {
		if !validIdentifier(input.Name) || !validIdentifier(input.StepID) || !validIdentifier(input.Output) {
			return fmt.Errorf("step %q input %d must have valid name, step_id, and output", step.ID, j)
		}
		if seen[input.Name] {
			return fmt.Errorf("step %q input %d duplicates input name %q", step.ID, j, input.Name)
		}
		seen[input.Name] = true
		producerIndex, exists := prior[input.StepID]
		if !exists || producerIndex >= index {
			return fmt.Errorf("step %q input %q must reference an earlier step; %q is not earlier", step.ID, input.Name, input.StepID)
		}
		producerSchema, err := namedPropertySchema(priorSteps[producerIndex].Contract.OutputSchema, input.Output)
		if err != nil {
			return fmt.Errorf("step %q input %q producer %q output %q: %w", step.ID, input.Name, input.StepID, input.Output, err)
		}
		consumerSchema, err := namedPropertySchema(consumer.InputSchema, input.Name)
		if err != nil {
			return fmt.Errorf("step %q input %q: %w", step.ID, input.Name, err)
		}
		producerCanonical, err := canonicalSchema(producerSchema)
		if err != nil {
			return fmt.Errorf("step %q input %q producer schema: %w", step.ID, input.Name, err)
		}
		consumerCanonical, err := canonicalSchema(consumerSchema)
		if err != nil {
			return fmt.Errorf("step %q input %q consumer schema: %w", step.ID, input.Name, err)
		}
		if !bytes.Equal(producerCanonical, consumerCanonical) {
			return fmt.Errorf("step %q input %q is not provably compatible with producer %q output %q", step.ID, input.Name, input.StepID, input.Output)
		}
	}
	return nil
}

func namedPropertySchema(raw json.RawMessage, name string) (json.RawMessage, error) {
	if len(bytes.TrimSpace(raw)) == 0 {
		return nil, errors.New("schema is missing")
	}
	var root struct {
		Properties map[string]json.RawMessage `json:"properties"`
	}
	if err := json.Unmarshal(raw, &root); err != nil {
		return nil, fmt.Errorf("schema cannot be inspected: %w", err)
	}
	property, ok := root.Properties[name]
	if !ok {
		return nil, fmt.Errorf("schema properties do not define %q", name)
	}
	return property, nil
}

func canonicalSchema(raw json.RawMessage) ([]byte, error) {
	value, err := decodeOneValue(raw)
	if err != nil {
		return nil, err
	}
	return json.Marshal(value)
}

func validateParams(stepID string, raw, schema json.RawMessage) error {
	trimmed := bytes.TrimSpace(raw)
	if len(schema) == 0 {
		if len(trimmed) == 0 {
			return nil
		}
		if _, err := decodeOneValue(trimmed); err != nil {
			return fmt.Errorf("step %q params are not one JSON value: %w", stepID, err)
		}
		return nil
	}
	compiled, err := skillregistry.CompileSchema(schema)
	if err != nil {
		return fmt.Errorf("step %q resolved params schema is invalid: %w", stepID, err)
	}
	var value any
	if len(trimmed) == 0 {
		value = nil
	} else if value, err = decodeOneValue(trimmed); err != nil {
		return fmt.Errorf("step %q params are not one JSON value: %w", stepID, err)
	}
	if err := compiled.CheckValue(value); err != nil {
		return fmt.Errorf("step %q params violate the resolved registry schema: %w", stepID, err)
	}
	return nil
}

func decodeOneValue(raw []byte) (any, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, err
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return nil, errors.New("multiple JSON values")
		}
		return nil, fmt.Errorf("trailing data: %w", err)
	}
	return value, nil
}

func validateSafetyGates(steps []ValidatedStep, grants map[string]StepGrant) error {
	for i, step := range steps {
		shipping := hasCapability(step.Contract, skillregistry.CapShipping)
		if shipping && grants[step.Step.ID].Authority != AuthorityWrite {
			return fmt.Errorf("shipping step %q lacks explicit write authority", step.Step.ID)
		}
		if grants[step.Step.ID].Authority == AuthorityWrite && !shipping {
			// Intermediate implementation writes may be followed by another
			// implementation write. Gates are required for the final write in
			// that run, not between every pair of implementation tasks.
			nextWrite := -1
			for j := i + 1; j < len(steps); j++ {
				if grants[steps[j].Step.ID].Authority == AuthorityWrite && !hasCapability(steps[j].Contract, skillregistry.CapShipping) {
					nextWrite = j
					break
				}
			}
			if nextWrite >= 0 {
				continue
			}
			boundary := len(steps)
			for j := i + 1; j < len(steps); j++ {
				if hasCapability(steps[j].Contract, skillregistry.CapShipping) {
					boundary = j
					break
				}
			}
			review, verification := gateIndexes(steps, grants, i+1, boundary)
			if review < 0 || verification < 0 || review == verification {
				if boundary < len(steps) {
					return fmt.Errorf("shipping step %q requires distinct independent review and verification gates after the last preceding write and before shipping", steps[boundary].Step.ID)
				}
				return fmt.Errorf("write step %q requires distinct downstream independent review and verification gates after the final implementation write", step.Step.ID)
			}
		}
		if shipping {
			lastWrite := -1
			for j := i - 1; j >= 0; j-- {
				if grants[steps[j].Step.ID].Authority == AuthorityWrite && !hasCapability(steps[j].Contract, skillregistry.CapShipping) {
					lastWrite = j
					break
				}
			}
			if lastWrite < 0 {
				return fmt.Errorf("shipping step %q has no preceding write", step.Step.ID)
			}
			review, verification := gateIndexes(steps, grants, lastWrite+1, i)
			if review < 0 || verification < 0 || review == verification {
				return fmt.Errorf("shipping step %q requires distinct independent review and verification gates after the last preceding write and before shipping", step.Step.ID)
			}
		}
	}
	return nil
}

func gateIndexes(steps []ValidatedStep, grants map[string]StepGrant, start, end int) (review, verification int) {
	review, verification = -1, -1
	for i := start; i < end; i++ {
		step := steps[i]
		if grants[step.Step.ID].Authority != AuthorityReadOnly || grants[step.Step.ID].ProducerIsolation != IsolationRequired {
			continue
		}
		if hasCapability(step.Contract, skillregistry.CapReview) {
			review = i
		}
		if hasCapability(step.Contract, skillregistry.CapVerification) {
			verification = i
		}
	}
	return review, verification
}

func hasCapability(contract skillregistry.SkillContract, wanted skillregistry.Capability) bool {
	for _, capability := range contract.Capabilities {
		if capability == wanted {
			return true
		}
	}
	return false
}

func validIdentifier(value string) bool {
	return identifierRE.MatchString(value)
}

func cloneFlow(flow Flow) Flow {
	out := Flow{Version: flow.Version, Steps: make([]Step, len(flow.Steps))}
	for i := range flow.Steps {
		out.Steps[i] = cloneStep(flow.Steps[i])
	}
	return out
}

func cloneStep(step Step) Step {
	out := step
	if step.RHS != nil {
		value := *step.RHS
		out.RHS = &value
	}
	out.Models = append([]ModelCandidate(nil), step.Models...)
	out.Params = append(json.RawMessage(nil), step.Params...)
	out.Inputs = append([]InputRef(nil), step.Inputs...)
	return out
}

func cloneContract(contract skillregistry.SkillContract) skillregistry.SkillContract {
	out := contract
	out.Capabilities = append([]skillregistry.Capability(nil), contract.Capabilities...)
	out.InputSchema = append(json.RawMessage(nil), contract.InputSchema...)
	out.ParamsSchema = append(json.RawMessage(nil), contract.ParamsSchema...)
	out.OutputSchema = append(json.RawMessage(nil), contract.OutputSchema...)
	return out
}

func cloneValidatedStep(step ValidatedStep) ValidatedStep {
	return ValidatedStep{Step: cloneStep(step.Step), Contract: cloneContract(step.Contract)}
}
