package workflow

import (
	"bytes"
	"encoding/json"
	"testing"
)

func TestRHSMatrixAndOmittedDefault(t *testing.T) {
	for _, tc := range []struct {
		wi         bool
		step       *bool
		unattended bool
	}{
		{false, nil, true}, {false, boolp(false), true}, {false, boolp(true), true},
		{true, nil, true}, {true, boolp(false), true}, {true, boolp(true), false},
	} {
		f, b := safeFlow()
		f.Steps[0].RHS = tc.step
		v, e := Validate(f, b, safeContext(f))
		if e != nil {
			t.Fatal(e)
		}
		if v.Unattended(tc.wi) != tc.unattended {
			t.Fatalf("WI=%t step=%v unattended=%t", tc.wi, tc.step, v.Unattended(tc.wi))
		}
	}
	f, b := safeFlow()
	b[0].Contract.Runtime.Interactive = true
	if _, e := Validate(f, b, safeContext(f)); e == nil {
		t.Fatal("omitted step rhs allowed interactive-only skill")
	}
	f.Steps[0].RHS = boolp(true)
	v, e := Validate(f, b, safeContext(f))
	if e != nil {
		t.Fatal(e)
	}
	if _, e := Decide(v, nil, PolicyInput{}); e == nil {
		t.Fatal("WI rhs=false allowed interactive-only skill")
	}
}

func TestWorkerApprovalCannotGrant(t *testing.T) {
	raw := `{"status":"completed","approval":{"decision":"approved","actor_id":"human"}}`
	var result StepResult
	if e := json.Unmarshal([]byte(raw), &result); e == nil {
		t.Fatal("worker-supplied approval accepted")
	}
}

func TestTrustedGrantRequiredAndWriteRequiresGates(t *testing.T) {
	f, b := safeFlow()
	grants := safeContext(f)
	delete(grants.Grants, "review")
	if _, e := Validate(f, b, grants); e == nil {
		t.Fatal("missing controller grant accepted")
	}
	grants = safeContext(f)
	grants.Grants["review"] = StepGrant{AuthorityReadOnly, IsolationShared}
	if _, e := Validate(f, b, grants); e == nil {
		t.Fatal("missing independent reviewer accepted")
	}
}

// ─── aihub#725 S1: strict, number-preserving worker envelope decode ─────────
//
// The result envelope is untrusted worker stdout (or an agent-supplied MCP
// `result` object). StepResult.UnmarshalJSON is the one decode every path
// shares — controller stdout, server RecordWorkflowResult, MCP result — so
// its strictness is the whole surface: a caller-side
// json.Decoder.DisallowUnknownFields never reaches inside a custom
// unmarshaler, which is why the checks live here and not in execute.go.

// stepResultFixture is one fully-populated, valid envelope every strictness
// arm below starts from: if the STRICT decoder rejected something the LOOSE
// one accepted, the arms would silently stop testing what they claim.
const stepResultFixture = `{"status":"completed","review_verdict":"warn",
	"work_item_id":"wi_1","flow_version":2,"step_id":"spec","step_attempt_id":"sa_1",
	"epoch":3,"producer_id":"p_1",
	"artifact":{"id":"mem_1","version":1,"hash":"sha256:aa"},
	"evidence":[{"kind":"test","ref":"runs/x","hash":"sha256:bb"}]}`

func TestStepResultStrictDecodeRejectsUnknownFields(t *testing.T) {
	for name, raw := range map[string]string{
		"unknown top-level key": stepResultFixture[:len(stepResultFixture)-1] + `,"worker_override":1}`,
		"non-object output":     stepResultFixture[:len(stepResultFixture)-1] + `,"output":"a string"}`,
		"array output":          stepResultFixture[:len(stepResultFixture)-1] + `,"output":[1,2]}`,
	} {
		var result StepResult
		if e := json.Unmarshal([]byte(raw), &result); e == nil {
			t.Fatalf("%s: accepted by the strict decoder", name)
		}
	}
}

// TestStepResultStrictDecodeRejectsNestedUnknownKeys is the aihub#725
// review_fix B1 regression: the top-level whitelist alone used to be the
// only strictness at depth, so an unknown key inside `artifact` or inside an
// `evidence` entry was silently discarded by the permissive struct decode
// while the raw bytes still carried it — a smuggled field exactly like the
// unknown top-level keys the whitelist refuses. `output` is the contrast
// arm: it is the worker's business payload, so its keys stay arbitrary.
func TestStepResultStrictDecodeRejectsNestedUnknownKeys(t *testing.T) {
	for name, raw := range map[string]string{
		"unknown key in artifact": `{"status":"completed","work_item_id":"wi_1","flow_version":2,
			"step_id":"spec","step_attempt_id":"sa_1","epoch":3,"producer_id":"p_1",
			"artifact":{"id":"mem_1","version":1,"hash":"sha256:aa","overflow":true}}`,
		"non-object artifact": `{"status":"completed","work_item_id":"wi_1","flow_version":2,
			"step_id":"spec","step_attempt_id":"sa_1","epoch":3,"producer_id":"p_1",
			"artifact":"mem_1"}`,
		"unknown key in evidence entry": `{"status":"completed","work_item_id":"wi_1","flow_version":2,
			"step_id":"spec","step_attempt_id":"sa_1","epoch":3,"producer_id":"p_1",
			"evidence":[{"kind":"test","ref":"runs/x","hash":"sha256:bb","extra":1}]}`,
		"evidence not an array": `{"status":"completed","work_item_id":"wi_1","flow_version":2,
			"step_id":"spec","step_attempt_id":"sa_1","epoch":3,"producer_id":"p_1",
			"evidence":{"kind":"test"}}`,
	} {
		var result StepResult
		if e := json.Unmarshal([]byte(raw), &result); e == nil {
			t.Fatalf("%s: accepted by the strict decoder (unknown nested contract key silently discarded)", name)
		}
	}

	// The contrast arms: output keys are business keys and stay arbitrary,
	// and the closed sub-objects keep accepting every LEGAL spelling — null
	// included — so the new strictness did not narrow the contract itself.
	for name, raw := range map[string]string{
		"arbitrary output keys": `{"status":"completed","work_item_id":"wi_1","flow_version":2,
			"step_id":"spec","step_attempt_id":"sa_1","epoch":3,"producer_id":"p_1",
			"output":{"anything_goes":{"nested":true},"count":2}}`,
		"null artifact": `{"status":"completed","work_item_id":"wi_1","flow_version":2,
			"step_id":"spec","step_attempt_id":"sa_1","epoch":3,"producer_id":"p_1","artifact":null}`,
		"null evidence": `{"status":"completed","work_item_id":"wi_1","flow_version":2,
			"step_id":"spec","step_attempt_id":"sa_1","epoch":3,"producer_id":"p_1","evidence":null}`,
		"fixture unchanged": stepResultFixture,
	} {
		var result StepResult
		if e := json.Unmarshal([]byte(raw), &result); e != nil {
			t.Fatalf("%s: rejected by the strict decoder: %v", name, e)
		}
	}
}

func TestStepResultStrictDecodeRejectsDuplicateKeys(t *testing.T) {
	for name, raw := range map[string]string{
		"duplicate top-level key": stepResultFixture[:len(stepResultFixture)-1] + `,"status":"incomplete"}`,
		"duplicate nested key in artifact": `{"status":"completed",
			"work_item_id":"wi_1","flow_version":2,"step_id":"spec","step_attempt_id":"sa_1",
			"epoch":3,"producer_id":"p_1",
			"artifact":{"id":"mem_1","version":1,"version":2,"hash":"sha256:aa"}}`,
		"duplicate nested key in output": stepResultFixture[:len(stepResultFixture)-1] +
			`,"output":{"summary":"x","summary":"y"}}`,
	} {
		var result StepResult
		if e := json.Unmarshal([]byte(raw), &result); e == nil {
			t.Fatalf("%s: accepted by the strict decoder (encoding/json would keep the LAST value)", name)
		}
	}
}

func TestStepResultStrictDecodeRejectsTrailingContent(t *testing.T) {
	// Trailing rejection is json.Unmarshal's own contract — the whole input
	// must be one JSON value — and the controller parses stdout with exactly
	// that entry point, so a trailing second object must never decode.
	raw := stepResultFixture + ` {"status":"completed"}`
	var result StepResult
	if e := json.Unmarshal([]byte(raw), &result); e == nil {
		t.Fatal("trailing content after the envelope accepted")
	}
}

func TestStepResultDecodesOutputWithNumberLiteralsPreserved(t *testing.T) {
	raw := `{"status":"completed","work_item_id":"wi_1","flow_version":2,
		"step_id":"spec","step_attempt_id":"sa_1","epoch":3,"producer_id":"p_1",
		"output":{"summary":"done","count":42,"ratio":1.50,"big":12345678901234567890}}`
	var result StepResult
	if e := json.Unmarshal([]byte(raw), &result); e != nil {
		t.Fatalf("valid envelope with transient output rejected: %v", e)
	}
	count, ok := result.Output["count"].(json.Number)
	if !ok || count.String() != "42" {
		t.Fatalf("integer 42 lost its literal: %#v", result.Output["count"])
	}
	if n, ok := result.Output["ratio"].(json.Number); !ok || n.String() != "1.50" {
		t.Fatalf("trailing-zero decimal 1.50 lost its literal: %#v", result.Output["ratio"])
	}
	if n, ok := result.Output["big"].(json.Number); !ok || n.String() != "12345678901234567890" {
		t.Fatalf("large integer lost its literal (UseNumber missing): %#v", result.Output["big"])
	}
	// The preserved literals survive the decode byte-for-byte: the envelope
	// boundary is lossless (UseNumber), and the sink hashes and stores those
	// same literal bytes verbatim (sinkWorkerOutput, aihub#725 review_fix
	// SF2/SF4) — the read paths recompute the digest over the same literals.
	remarshalled, err := json.Marshal(result.Output)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"big":12345678901234567890,"count":42,"ratio":1.50,"summary":"done"}`
	if string(remarshalled) != want {
		t.Fatalf("number literals changed across marshal:\n got %s\nwant %s", remarshalled, want)
	}
	// Null output decodes to a nil map — an artifactless completed result
	// without an object-valued output is the sink's refusal, not the
	// decoder's: the decoder only enforces that output is an object WHEN
	// present.
	var nullOut StepResult
	if e := json.Unmarshal([]byte(`{"status":"blocked","output":null}`), &nullOut); e != nil {
		t.Fatalf("null transient output rejected: %v", e)
	}
	if nullOut.Output != nil {
		t.Fatalf("null output should decode to a nil map, got %#v", nullOut.Output)
	}
}

func TestStepResultMarshalOmitsClearedOutput(t *testing.T) {
	// The recorded result must stay byte-identical to the pre-sink wire
	// shape: once the controller clears the transient output, `omitempty`
	// keeps it off the JSON, so RecordWorkflowResult never sees it.
	var result StepResult
	if e := json.Unmarshal([]byte(stepResultFixture), &result); e != nil {
		t.Fatal(e)
	}
	result.Output = map[string]any{"transient": json.Number("1")}
	withOutput, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	result.Output = nil
	without, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(without, []byte("output")) {
		t.Fatalf("cleared transient output still on the recorded-result wire: %s", without)
	}
	if !bytes.Contains(withOutput, []byte("output")) {
		t.Fatal("fixture lost the output key before the first marshal; the test is not testing what it claims")
	}
}
