package workflow

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/GMISWE/ieops-aihub/internal/skillregistry"
)

func boolp(value bool) *bool { return &value }

func testModel() []ModelCandidate {
	return []ModelCandidate{{Harness: "pi", Model: "provider/model", Effort: "high"}}
}

func testContract(capability skillregistry.Capability) skillregistry.SkillContract {
	return skillregistry.SkillContract{Capabilities: []skillregistry.Capability{capability}}
}

func testStep(id, skill string, version int, authority ExecutionAuthority, isolation ProducerIsolation) Step {
	return Step{
		ID: id, SkillID: skill, SkillVersion: version, Models: testModel(),
	}
}

func safeFlow() (Flow, []SkillBinding) {
	write := testStep("write", "skill_write", 2, AuthorityWrite, IsolationShared)
	review := testStep("review", "skill_review", 4, AuthorityReadOnly, IsolationRequired)
	review.Inputs = []InputRef{{Name: "change", StepID: "write", Output: "artifact"}}
	verify := testStep("verify", "skill_verify", 3, AuthorityReadOnly, IsolationRequired)
	verify.Inputs = []InputRef{{Name: "change", StepID: "write", Output: "artifact"}}
	ship := testStep("ship", "skill_ship", 7, AuthorityWrite, IsolationShared)
	ship.Inputs = []InputRef{
		{Name: "review", StepID: "review", Output: "approval"},
		{Name: "verification", StepID: "verify", Output: "evidence"},
	}
	return Flow{Version: 5, Steps: []Step{write, review, verify, ship}}, []SkillBinding{
		{SkillID: "skill_write", Version: 2, Contract: skillregistry.SkillContract{Capabilities: []skillregistry.Capability{skillregistry.CapAuthoring}, OutputSchema: json.RawMessage(`{"type":"object","properties":{"artifact":{"type":"string"}},"required":["artifact"],"additionalProperties":false}`)}},
		{SkillID: "skill_review", Version: 4, Contract: skillregistry.SkillContract{Capabilities: []skillregistry.Capability{skillregistry.CapReview}, InputSchema: json.RawMessage(`{"type":"object","properties":{"change":{"type":"string"}},"required":["change"],"additionalProperties":false}`), OutputSchema: json.RawMessage(`{"type":"object","properties":{"approval":{"type":"string"}},"required":["approval"],"additionalProperties":false}`)}},
		{SkillID: "skill_verify", Version: 3, Contract: skillregistry.SkillContract{Capabilities: []skillregistry.Capability{skillregistry.CapVerification}, InputSchema: json.RawMessage(`{"type":"object","properties":{"change":{"type":"string"}},"required":["change"],"additionalProperties":false}`), OutputSchema: json.RawMessage(`{"type":"object","properties":{"evidence":{"type":"string"}},"required":["evidence"],"additionalProperties":false}`)}},
		{SkillID: "skill_ship", Version: 7, Contract: skillregistry.SkillContract{Capabilities: []skillregistry.Capability{skillregistry.CapShipping}, InputSchema: json.RawMessage(`{"type":"object","properties":{"review":{"type":"string"},"verification":{"type":"string"}},"required":["review","verification"],"additionalProperties":false}`)}},
	}
}

func TestValidateBindsExactContractsAndReusesRegistryParamsValidator(t *testing.T) {
	flow, bindings := safeFlow()
	flow.Steps[0].Params = json.RawMessage(`{"language":"go"}`)
	bindings[0].Contract.ParamsSchema = json.RawMessage(`{
		"type":"object",
		"properties":{"language":{"type":"string","enum":["go"]}},
		"required":["language"],
		"additionalProperties":false
	}`)
	got, err := Validate(flow, bindings, safeContext(flow))
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if got.Definition().Version != 5 || len(got.BoundSteps()) != 4 {
		t.Fatalf("validated flow lost definition or bindings: %+v", got)
	}
	if !got.Unattended(false) {
		t.Fatal("all rhs=false and non-interactive contracts should be unattended")
	}

	flow.Steps[0].Params = json.RawMessage(`{"language":"rust"}`)
	if _, err := Validate(flow, bindings, safeContext(flow)); err == nil || !strings.Contains(err.Error(), "registry schema") {
		t.Fatalf("registry params validator was not enforced: %v", err)
	}
}

func TestValidateRejectsUnsafeFlowShapes(t *testing.T) {
	baseFlow, baseBindings := safeFlow()
	tests := []struct {
		name string
		edit func(*Flow, *[]SkillBinding)
		want string
	}{
		{"nonpositive flow version", func(f *Flow, _ *[]SkillBinding) { f.Version = 0 }, "version"},
		{"duplicate step id", func(f *Flow, _ *[]SkillBinding) { f.Steps[1].ID = "write" }, "duplicates id"},
		{"unpinned skill version", func(f *Flow, _ *[]SkillBinding) { f.Steps[0].SkillVersion = 0 }, "pin an exact"},
		{"missing exact binding", func(_ *Flow, b *[]SkillBinding) { *b = (*b)[1:] }, "no resolved binding"},
		{"model candidates omitted", func(f *Flow, _ *[]SkillBinding) { f.Steps[0].Models = nil }, "no model candidates"},
		{"model harness malformed", func(f *Flow, _ *[]SkillBinding) { f.Steps[0].Models[0].Harness = "Pi " }, "invalid harness"},
		{"model absent", func(f *Flow, _ *[]SkillBinding) { f.Steps[0].Models[0].Model = "" }, "invalid model"},
		{"effort absent", func(f *Flow, _ *[]SkillBinding) { f.Steps[0].Models[0].Effort = "" }, "invalid effort"},
		{"future input dependency", func(f *Flow, _ *[]SkillBinding) {
			f.Steps[0].Inputs = []InputRef{{Name: "future", StepID: "review", Output: "x"}}
		}, "earlier step"},
		{"interactive skill unattended", func(_ *Flow, b *[]SkillBinding) { (*b)[0].Contract.Runtime.Interactive = true }, "interactive-only"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			flow := cloneFlow(baseFlow)
			bindings := append([]SkillBinding(nil), baseBindings...)
			for i := range bindings {
				bindings[i].Contract = cloneContract(bindings[i].Contract)
			}
			test.edit(&flow, &bindings)
			_, err := Validate(flow, bindings, safeContext(flow))
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Validate error = %v, want substring %q", err, test.want)
			}
		})
	}
}

func TestValidateRejectsUnboundOrIncompatibleNamedInputs(t *testing.T) {
	f, b := safeFlow()
	f.Steps[1].Inputs[0].Output = "missing"
	if _, err := Validate(f, b, safeContext(f)); err == nil || !strings.Contains(err.Error(), "output") {
		t.Fatalf("missing producer output accepted: %v", err)
	}
	f, b = safeFlow()
	b[1].Contract.InputSchema = json.RawMessage(`{"type":"object","properties":{"change":{"type":"number"}}}`)
	if _, err := Validate(f, b, safeContext(f)); err == nil || !strings.Contains(err.Error(), "compatible") {
		t.Fatalf("incompatible named input accepted: %v", err)
	}
}

func TestValidateParamsPreservesExactNumbersAndOneValue(t *testing.T) {
	f, b := safeFlow()
	b[0].Contract.ParamsSchema = json.RawMessage(`{"type":"object","properties":{"n":{"type":"integer","maximum":1},"ratio":{"enum":[0.1]}},"required":["n","ratio"]}`)
	for name, raw := range map[string]string{
		"decimal enum":      `{"n":1,"ratio":0.1}`,
		"adjacent positive": `{"n":1,"ratio":0.1}`, // schema accepts the exact boundary
	} {
		f.Steps[0].Params = json.RawMessage(raw)
		if _, err := Validate(f, b, safeContext(f)); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
	}
	for name, raw := range map[string]string{
		"trailing value":     `{"n":1,"ratio":0.1} {"n":1}`,
		"fractional integer": `{"n":1.0000000000000001,"ratio":0.1}`,
		"too large exact":    `{"n":9007199254740993,"ratio":0.1}`,
	} {
		f.Steps[0].Params = json.RawMessage(raw)
		if _, err := Validate(f, b, safeContext(f)); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
	b[0].Contract.ParamsSchema = json.RawMessage(`{"type":"object","properties":{"n":{"type":"integer"}},"required":["n"]}`)
	for _, raw := range []string{`{"n":9007199254740992}`, `{"n":9007199254740993}`, `{"n":-9007199254740992}`, `{"n":-9007199254740993}`} {
		f.Steps[0].Params = json.RawMessage(raw)
		if _, err := Validate(f, b, safeContext(f)); err != nil {
			t.Errorf("exact adjacent integer %s rejected: %v", raw, err)
		}
	}
}

func TestValidateRejectsShippingBeforeLastWriteGates(t *testing.T) {
	f, b := safeFlow()
	writeB := testStep("write_b", "skill_write", 2, AuthorityWrite, IsolationShared)
	f.Steps = []Step{f.Steps[0], f.Steps[1], f.Steps[2], writeB, f.Steps[3]}
	b = append(b, SkillBinding{SkillID: "skill_write", Version: 2, Contract: b[0].Contract})
	// Reuse the exact bound skill ref; the duplicate binding is intentionally removed.
	b = b[:4]
	ctx := safeContext(f)
	ctx.Grants["write_b"] = StepGrant{Authority: AuthorityWrite, ProducerIsolation: IsolationShared}
	if _, err := Validate(f, b, ctx); err == nil || !strings.Contains(err.Error(), "shipping") {
		t.Fatalf("ship before last write gates accepted: %v", err)
	}
}
func TestValidateAllowsConsecutiveWritesWithSharedFinalGates(t *testing.T) {
	f, b := safeFlow()
	writeB := testStep("write_b", "skill_write", 2, AuthorityWrite, IsolationShared)
	f.Steps = []Step{f.Steps[0], writeB, f.Steps[1], f.Steps[2], f.Steps[3]}
	b = append(b, SkillBinding{SkillID: "skill_write", Version: 2, Contract: b[0].Contract})[:4]
	ctx := safeContext(f)
	ctx.Grants["write_b"] = StepGrant{Authority: AuthorityWrite, ProducerIsolation: IsolationShared}
	if _, err := Validate(f, b, ctx); err != nil {
		t.Fatalf("consecutive writes with final gates rejected: %v", err)
	}
}
func TestCapabilityDeclarationsDoNotGrantExecutionAuthority(t *testing.T) {
	step := testStep("draft", "skill_author", 1, AuthorityReadOnly, IsolationShared)
	flow := Flow{Version: 1, Steps: []Step{step}}
	bindings := []SkillBinding{{SkillID: "skill_author", Version: 1, Contract: testContract(skillregistry.CapAuthoring)}}
	_, err := Validate(flow, bindings, ExecutionContext{Grants: map[string]StepGrant{"draft": {AuthorityReadOnly, IsolationShared}}})
	if err != nil {
		t.Fatalf("read-only authoring declaration should not be treated as a write grant: %v", err)
	}
	if safeContext(flow).Grants["draft"].Authority != AuthorityReadOnly {
		t.Fatal("resolved capability widened explicit read-only authority")
	}
}

func TestValidatedFlowDefensivelyCopiesDefinitionsAndContracts(t *testing.T) {
	flow, bindings := safeFlow()
	validated, err := Validate(flow, bindings, safeContext(flow))
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
	flow.Steps[0].ID = "mutated_source"
	bindings[0].Contract.Capabilities[0] = skillregistry.CapShipping
	definition := validated.Definition()
	bound := validated.BoundSteps()
	definition.Steps[0].ID = "mutated_return"
	bound[0].Contract.Capabilities[0] = skillregistry.CapShipping
	if validated.Definition().Steps[0].ID != "write" || validated.BoundSteps()[0].Contract.Capabilities[0] != skillregistry.CapAuthoring {
		t.Fatal("validated flow was mutated through caller-owned or returned slices")
	}
}

func TestUnattendedRequiresRHSAndNonInteractiveRuntime(t *testing.T) {
	flow, bindings := safeFlow()
	flow.Steps[0].RHS = boolp(true)
	validated, err := Validate(flow, bindings, safeContext(flow))
	if err != nil {
		t.Fatalf("human-session flow should validate: %v", err)
	}
	if validated.Unattended(true) {
		t.Fatal("rhs=true must prevent unattended execution")
	}

	flow, bindings = safeFlow()
	flow.Steps[0].RHS = boolp(true)
	bindings[0].Contract.Runtime.Interactive = true
	validated, err = Validate(flow, bindings, safeContext(flow))
	if err != nil {
		t.Fatalf("interactive contract with rhs=true should validate: %v", err)
	}
	if validated.Unattended(true) {
		t.Fatal("interactive runtime must prevent unattended execution")
	}
}

func safeContext(flow Flow) ExecutionContext {
	grants := make(map[string]StepGrant)
	for _, step := range flow.Steps {
		grant := StepGrant{AuthorityReadOnly, IsolationShared}
		switch step.ID {
		case "write", "ship":
			grant.Authority = AuthorityWrite
		case "review", "verify":
			grant.ProducerIsolation = IsolationRequired
		}
		grants[step.ID] = grant
	}
	return ExecutionContext{Grants: grants}
}
