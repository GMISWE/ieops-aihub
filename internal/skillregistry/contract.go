package skillregistry

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
)

// Capability is the typed semantic execution contract of a skill version —
// what KIND of work it performs (spec D6): never inferred from the skill's
// name, always declared here. The vocabulary is CLOSED and shared with the
// workflow layer (Batch 1B, internal/workflow), which derives its required
// review/verification gates from exactly these values; an unknown capability
// is refused at publish time so no flow can be built on a name nobody gates.
type Capability string

const (
	// CapAuthoring: produce a document/artifact through discussion or drafting
	// (spec, plan, interview).
	CapAuthoring Capability = "authoring"
	// CapReview: independently inspect work produced elsewhere and return a
	// verdict.
	CapReview Capability = "review"
	// CapVerification: establish that claimed work actually holds (tests,
	// builds, gates), reporting honest skip/fail states.
	CapVerification Capability = "verification"
	// CapShipping: advance the delivery lifecycle (commit/push/PR/CI).
	CapShipping Capability = "shipping"
	// CapDeterministicOperation: perform a bounded, non-creative operation
	// whose result is checkable (git diff, cleanup) — never arbitrary code
	// execution from database text.
	CapDeterministicOperation Capability = "deterministic_operation"
)

// capabilitySet is the closed vocabulary. One map, one home.
var capabilitySet = map[Capability]bool{
	CapAuthoring:              true,
	CapReview:                 true,
	CapVerification:           true,
	CapShipping:               true,
	CapDeterministicOperation: true,
}

// Capabilities returns the closed capability vocabulary, sorted, for docs and
// tool descriptions. The result is a fresh slice.
func Capabilities() []Capability {
	out := make([]Capability, 0, len(capabilitySet))
	for c := range capabilitySet {
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// IsCapability reports whether c is in the closed vocabulary.
func IsCapability(c Capability) bool {
	return capabilitySet[c]
}

// RuntimeSpec is the execution-runtime half of the contract. Interactive=true
// declares that the skill's step REQUIRES a human session to be useful (an
// interview); Batch 1B refuses interactive-only steps in RHS=false flows on
// exactly this flag (spec D5/D6). It is a declaration, not permission: the
// runtime contract never widens what a worker may do.
type RuntimeSpec struct {
	Interactive bool `json:"interactive"`
}

// SkillContract is the runtime capability contract stored in
// skill_versions.contract. Schemas are raw JSON here but MUST be inside the
// fail-closed supported subset (CompileSchema) before a version can be
// published — ValidateContract enforces that.
type SkillContract struct {
	Capabilities []Capability    `json:"capabilities"`
	InputSchema  json.RawMessage `json:"input_schema,omitempty"`
	ParamsSchema json.RawMessage `json:"params_schema,omitempty"`
	OutputSchema json.RawMessage `json:"output_schema,omitempty"`
	Runtime      RuntimeSpec     `json:"runtime"`
}

// contractSupportedKeys lists every key the closed contract vocabulary
// accepts, for decodeStrictOne's refusal message.
var contractSupportedKeys = []string{
	"capabilities", "input_schema", "params_schema", "output_schema",
	"runtime", "runtime.interactive",
}

// DecodeContract parses raw contract JSON strictly, validating the capability
// vocabulary and compiling every present schema against the supported subset.
// Unknown keys are refused (closed contract, same rule as the bundle).
func DecodeContract(raw []byte) (*SkillContract, error) {
	if len(raw) == 0 {
		return nil, errors.New("contract is empty")
	}
	var c SkillContract
	canon, err := decodeStrictOne(raw, &c, "contract", contractSupportedKeys)
	if err != nil {
		return nil, err
	}
	// An explicitly null schema field would decode exactly like an absent one
	// (RawMessage nil → ValidateContract skips it), silently publishing an
	// unconstrained contract where the author wrote a null "schema". Refuse
	// it with the field named, by the same rule as null schema nodes inside
	// CompileSchema: null is never an unconstrained pass.
	var present map[string]json.RawMessage
	if err := json.Unmarshal(canon, &present); err != nil {
		return nil, fmt.Errorf("contract is not valid: %w", err)
	}
	for _, field := range []string{"input_schema", "params_schema", "output_schema", "runtime"} {
		if v, ok := present[field]; ok && string(v) == "null" {
			return nil, fmt.Errorf("contract field %q is null; omit the field if it carries nothing — an explicit null is never an unconstrained pass", field)
		}
	}
	if r, ok := present["runtime"]; ok {
		var runtime map[string]json.RawMessage
		if err := json.Unmarshal(r, &runtime); err == nil {
			if v, ok := runtime["interactive"]; ok && string(v) == "null" {
				return nil, errors.New("contract field \"runtime.interactive\" is null")
			}
		}
	}
	if err := ValidateContract(&c); err != nil {
		return nil, err
	}
	return &c, nil
}

// ValidateContract enforces the contract rules:
//
//   - at least one capability, all from the closed vocabulary, no duplicates —
//     "what kind of work is this" must be answerable for every version;
//   - every present schema is inside the supported JSON Schema subset
//     (CompileSchema refuses unknown keywords, so a contract can never
//     promise a construct the executor does not implement).
//
// It is exported for callers that build a contract in Go and want the same
// rules on their own struct.
func ValidateContract(c *SkillContract) error {
	if c == nil {
		return errors.New("contract is empty")
	}
	if len(c.Capabilities) == 0 {
		return errors.New("contract declares no capabilities")
	}
	seen := make(map[Capability]bool, len(c.Capabilities))
	for _, cap := range c.Capabilities {
		if !capabilitySet[cap] {
			return fmt.Errorf("contract capability %q is not in the closed vocabulary %v", cap, Capabilities())
		}
		if seen[cap] {
			return fmt.Errorf("contract declares capability %q twice", cap)
		}
		seen[cap] = true
	}
	for name, raw := range map[string]json.RawMessage{
		"input_schema":  c.InputSchema,
		"params_schema": c.ParamsSchema,
		"output_schema": c.OutputSchema,
	} {
		if len(raw) == 0 {
			continue
		}
		if _, err := CompileSchema(raw); err != nil {
			return fmt.Errorf("contract %s is not a supported schema: %w", name, err)
		}
	}
	return nil
}

// CanonicalContractJSON returns the canonical serialization of c, validated
// first. This is the form stored in skill_versions.contract and the second
// input to the content digest.
//
// Canonicality is RECURSIVE: the raw schema JSON inside c is re-canonicalized
// (object keys sorted at every level, numbers normalized to exact minimal
// decimals), not embedded verbatim. That is what makes the digest computed
// over these bytes REPRODUCE after a JSONB round trip — PostgreSQL jsonb
// re-serializes keys and numbers its own way, so what a fetch returns is not
// what was stored, but it IS the same JSON value, and canonicalizing a
// value depends only on the value (see canonical.go). Two spellings of the
// same schemas therefore yield byte-identical canonical contracts.
func CanonicalContractJSON(c *SkillContract) ([]byte, error) {
	if err := ValidateContract(c); err != nil {
		return nil, err
	}
	marshaled, err := json.Marshal(c)
	if err != nil {
		return nil, err
	}
	return canonicalizeJSON(marshaled)
}
