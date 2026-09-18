package skillregistry

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestCapabilitiesIsTheClosedVocabulary(t *testing.T) {
	want := []Capability{
		CapAuthoring, CapReview, CapShipping, CapDeterministicOperation, CapVerification,
	}
	got := Capabilities()
	if len(got) != len(want) {
		t.Fatalf("Capabilities() = %v, want exactly the five spec D6 kinds %v", got, want)
	}
	seen := map[Capability]bool{}
	for _, c := range got {
		seen[c] = true
	}
	for _, c := range want {
		if !seen[c] {
			t.Errorf("capability %q missing from the closed vocabulary", c)
		}
	}
	// The five are the spec's own list: authoring / review / verification /
	// shipping / deterministic operation.
	for _, c := range []Capability{"authoring", "review", "verification", "shipping", "deterministic_operation"} {
		if !IsCapability(c) {
			t.Errorf("IsCapability(%q) = false", c)
		}
	}
	for _, c := range []Capability{"", "execute", "admin", "Authoring", "arbitrary_code", "plugin"} {
		if IsCapability(c) {
			t.Errorf("IsCapability(%q) = true; the vocabulary is closed", c)
		}
	}
}

func TestDecodeContractAcceptsAndValidates(t *testing.T) {
	raw := `{
		"capabilities": ["authoring", "verification"],
		"input_schema": {"type": "object", "properties": {"goal": {"type": "string"}}, "required": ["goal"], "additionalProperties": false},
		"runtime": {"interactive": true}
	}`
	got, err := DecodeContract([]byte(raw))
	if err != nil {
		t.Fatalf("DecodeContract: %v", err)
	}
	if got.Runtime.Interactive != true {
		t.Errorf("runtime.interactive lost: %+v", got.Runtime)
	}
	if len(got.Capabilities) != 2 {
		t.Errorf("capabilities lost: %+v", got.Capabilities)
	}
}

func TestDecodeContractRefusesClosedSetViolations(t *testing.T) {
	for name, raw := range map[string]string{
		"unknown capability":         `{"capabilities": ["authoring","root_access"]}`,
		"no capabilities":            `{}`,
		"empty capabilities":         `{"capabilities": []}`,
		"duplicate capability":       `{"capabilities": ["authoring","authoring"]}`,
		"unknown top-level key":      `{"capabilities": ["authoring"], "executes": "bash"}`,
		"unknown runtime key":        `{"capabilities": ["authoring"], "runtime": {"interactive": true, "network": "allow"}}`,
		"unsupported schema keyword": `{"capabilities": ["authoring"], "input_schema": {"type": "object", "allOf": []}}`,
		"schema with $ref":           `{"capabilities": ["authoring"], "input_schema": {"$ref": "#/$defs/x"}}`,
		"trailing content":           `{"capabilities": ["authoring"]}{"capabilities": ["review"]}`,
	} {
		if _, err := DecodeContract([]byte(raw)); err == nil {
			t.Errorf("%s: DecodeContract accepted; want a fail-closed refusal", name)
		}
	}
}

func TestValidateContractCompilesEveryPresentSchema(t *testing.T) {
	// A contract whose params schema is inside the subset but whose output
	// schema is not must be refused AS A WHOLE — partial validation is the
	// exact failure mode D6 forbids ("pretending a partial validator
	// implements JSON Schema").
	c := &SkillContract{
		Capabilities: []Capability{CapDeterministicOperation},
		ParamsSchema: json.RawMessage(`{"type":"object"}`),
		OutputSchema: json.RawMessage(`{"type":"object","patternProperties":{}}`),
	}
	err := ValidateContract(c)
	if err == nil {
		t.Fatal("contract with an out-of-subset output schema was accepted")
	}
	if !strings.Contains(err.Error(), "output_schema") {
		t.Errorf("refusal should name which schema failed; got %v", err)
	}
}

// ─── aihub#708 Batch 1A repair: strict contract text and recursive canonicality ──

// TestDecodeContractRefusesAmbiguousText: duplicate keys at any level (which
// key wins would depend on the decoder — Go and jsonb agree only by accident)
// and malformed trailing text are both refused.
func TestDecodeContractRefusesAmbiguousText(t *testing.T) {
	for name, raw := range map[string]string{
		"duplicate top key":     `{"capabilities":["authoring"],"capabilities":["review"]}`,
		"duplicate runtime key": `{"capabilities":["authoring"],"runtime":{"interactive":false},"runtime":{"interactive":true}}`,
		"garbage tail":          `{"capabilities":["authoring"]} garbage`,
		"stray delimiter":       `{"capabilities":["authoring"]}]`,
		"nul byte tail":         "{\"capabilities\":[\"authoring\"]}\x00",
		"schema duplicate key":  `{"capabilities":["authoring"],"input_schema":{"type":"string","type":"number"}}`,
		"null input schema":     `{"capabilities":["authoring"],"input_schema":null}`,
		"null params schema":    `{"capabilities":["authoring"],"params_schema":null}`,
		"null output schema":    `{"capabilities":["authoring"],"output_schema":null}`,
		"null runtime":          `{"capabilities":["authoring"],"runtime":null}`,
	} {
		if _, err := DecodeContract([]byte(raw)); err == nil {
			t.Errorf("%s: DecodeContract accepted ambiguous/malformed contract text", name)
		}
	}
}

// TestCanonicalContractJSONCanonicalizesSchemasRecursively is the unit half of
// the digest-reproducibility repair: the same schemas spelled differently —
// reordered keys, 1.0 vs 1, 0.50 vs 0.5, big exact integers — produce
// byte-identical canonical contracts, and the canonical form is a fixed
// point. Without this, the digest computed at publish time would not
// reproduce from bytes PostgreSQL jsonb hands back (it re-serializes keys
// and numbers its own way).
func TestCanonicalContractJSONCanonicalizesSchemasRecursively(t *testing.T) {
	c1 := &SkillContract{
		Capabilities: []Capability{CapDeterministicOperation},
		InputSchema: json.RawMessage(`{
			"type":"object",
			"properties":{
				"query":{"type":"string","minLength":1},
				"weight":{"type":"number","minimum":0.50,"maximum":1e2},
				"mode":{"enum":[1.0,"fast",{"b":2,"a":1.50}]}
			},
			"required":["query","mode"],
			"additionalProperties":false
		}`),
		Runtime: RuntimeSpec{Interactive: false},
	}
	// Same contract, every re-spellable part re-spelled.
	c2 := &SkillContract{
		Capabilities: []Capability{CapDeterministicOperation},
		InputSchema: json.RawMessage(`{"additionalProperties":false,"required":["query","mode"],` +
			`"properties":{"query":{"minLength":1.0,"type":"string"},` +
			`"weight":{"maximum":100.0,"minimum":0.5,"type":"number"},` +
			`"mode":{"enum":[1,"fast",{"a":1.5,"b":2.0}]}},` +
			`"type":"object"}`),
		Runtime: RuntimeSpec{Interactive: false},
	}
	j1, err := CanonicalContractJSON(c1)
	if err != nil {
		t.Fatalf("canonicalize c1: %v", err)
	}
	j2, err := CanonicalContractJSON(c2)
	if err != nil {
		t.Fatalf("canonicalize c2: %v", err)
	}
	if string(j1) != string(j2) {
		t.Errorf("same contract, two canonical forms:\n%s\n%s", j1, j2)
	}
	// Fixed point, two ways. First: the canonical text re-canonicalizes to
	// itself. Second: the round a JSONB fetch actually performs — decode the
	// canonical text back into a contract and canonicalize THAT — lands on
	// the same bytes.
	refix, err := canonicalizeJSON(j1)
	if err != nil {
		t.Fatalf("re-canonicalize canonical bytes: %v", err)
	}
	if string(refix) != string(j1) {
		t.Errorf("canonical contract is not a fixed point:\n%s\n%s", j1, refix)
	}
	decoded, err := DecodeContract(j1)
	if err != nil {
		t.Fatalf("decode canonical contract: %v", err)
	}
	round, err := CanonicalContractJSON(decoded)
	if err != nil {
		t.Fatalf("canonicalize the decoded contract: %v", err)
	}
	if string(round) != string(j1) {
		t.Errorf("decode -> canonicalize is not the identity on canonical bytes:\n%s\n%s", j1, round)
	}
}
