package mcp

// aihub#499 (the tail of aihub#445 / aihub#411 decision table §6.2 T2-6):
// pf_save_artifact's `type` must NOT be published as a closed 6-value enum, the
// description that replaced it must state the rule the tool really applies, and
// that rule must actually refuse something.
//
// Three halves, and none implies the others. Withdrawing the enum alone would
// leave the tool publishing less than before. Describing a prefix rule nobody
// checks would repeat aihub#445's finding one layer down — the published claim
// "methodology.* kinds" was already there and already untrue, because
// handleRemember only BRANCHES on the prefix and domain.Remember accepts all
// four of MemoryTypePrefixes. And enforcing the SIX names would have reversed
// §6.2 T2-6's "keep the leniency" ruling against measured live data: 3 of the
// 1,185 methodology.* rows in production on 2026-09-09 are off the six.
//
//	go test ./internal/mcp/ -run TestSaveArtifactType -v

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/GMISWE/ieops-aihub/internal/domain"
)

// saveArtifactTypeProp returns pf_save_artifact's `type` property as a decoded
// map, so a missing `enum` key can be told apart from an empty one.
func saveArtifactTypeProp(t *testing.T) map[string]any {
	t.Helper()
	var schema struct {
		Properties map[string]map[string]any `json:"properties"`
	}
	if err := json.Unmarshal(saveArtifactSchema(), &schema); err != nil {
		t.Fatalf("pf_save_artifact InputSchema is not valid JSON: %v", err)
	}
	p, ok := schema.Properties["type"]
	if !ok {
		t.Fatal("pf_save_artifact publishes no `type` property at all")
	}
	return p
}

// TestSaveArtifactTypeIsNotPublishedAsAClosedEnum is the withdrawal itself.
//
// internal/cli/dump_schemas_test.go had already named this parameter as the one
// aihub#445 left behind — "the same published-but-unenforced shape this work
// item withdrew, and pinning a test to it would entrench it" — and declined to
// assert on it for that reason. This is the assertion it was waiting for.
func TestSaveArtifactTypeIsNotPublishedAsAClosedEnum(t *testing.T) {
	if raw, ok := saveArtifactTypeProp(t)["enum"]; ok {
		t.Errorf("pf_save_artifact.type publishes enum %v. The accepted set is a PREFIX rule "+
			"(domain.MethodologyTypePrefix), so it is infinite and no list of names can state "+
			"it — measured: 3 live methodology.playbook rows sit outside these six. If the list "+
			"is worth publishing, publish it as a suggestion in the description, which is what "+
			"methodologyTypeParamDesc does.", raw)
	}
	if got, _ := saveArtifactTypeProp(t)["type"].(string); got != "string" {
		t.Errorf("pf_save_artifact.type is published as %q, want \"string\"", got)
	}
}

// TestSaveArtifactTypeDescriptionStatesWhatIsEnforced covers the second half:
// hop 1 is the only thing an LLM caller sees.
func TestSaveArtifactTypeDescriptionStatesWhatIsEnforced(t *testing.T) {
	desc, _ := saveArtifactTypeProp(t)["description"].(string)
	if desc == "" {
		t.Fatal("pf_save_artifact.type has no description; with the enum gone it publishes nothing at all")
	}

	// Anti-vacuity: an empty vocabulary would make every Contains below trivially
	// true.
	if len(domain.MethodologyTypeEnum) == 0 || domain.MethodologyTypePrefix == "" {
		t.Fatalf("domain publishes %d suggested artifact types and prefix %q — one vocabulary is empty",
			len(domain.MethodologyTypeEnum), domain.MethodologyTypePrefix)
	}

	if !strings.Contains(desc, domain.MethodologyTypePrefix) {
		t.Errorf("the description does not name the enforced prefix %q; got %q",
			domain.MethodologyTypePrefix, desc)
	}
	// The credential gate (aihub#210) is half of what this tool is FOR, and a
	// caller who does not know the artifact is wi-bound cannot read a 403.
	if !strings.Contains(desc, "credentials") {
		t.Errorf("the description does not mention the attempt credentials this tool sends; got %q", desc)
	}
	// The '|' ban (aihub#289): a piped type passes the prefix check, so the rule
	// cannot be deduced from the prefix alone.
	if !strings.Contains(desc, "'|'") {
		t.Errorf("the description does not publish the '|' ban, which the prefix rule does not "+
			"imply — \"methodology.spec|plan\" starts with a legal prefix; got %q", desc)
	}
	// The suggestion list, marked as open. "not a closed set" is asserted
	// verbatim because it is the entire difference between this description and
	// the enum it replaced.
	if !strings.Contains(desc, "not a closed set") {
		t.Errorf("the description lists suggested types without saying the list is open, which is "+
			"the enum again in prose; got %q", desc)
	}
	for _, v := range domain.MethodologyTypeEnum {
		if !strings.Contains(desc, v) {
			t.Errorf("the suggested kind %q was dropped from the description; withdrawing the enum "+
				"must not cost the caller the vocabulary", v)
		}
	}
	// 🔴 The consequence of keeping the set open. An off-list type stores but is
	// not pre-rendered and is absent from the wi's artifact-links section, and
	// nothing else on this tool warns a caller of that. Asserted here so the
	// disclosure cannot be dropped as wordiness.
	if !strings.Contains(desc, "NOT pre-rendered") {
		t.Errorf("the description does not disclose that an off-list type is stored but not "+
			"pre-rendered (domain.defaultRenderTypes names the six literally); got %q", desc)
	}
}

// TestSaveArtifactTypeDescriptionIsDerivedNotRetyped guards the reason the lists
// are read from domain rather than written out here: a retyped copy would stay
// green while it drifted.
func TestSaveArtifactTypeDescriptionIsDerivedNotRetyped(t *testing.T) {
	desc, _ := saveArtifactTypeProp(t)["description"].(string)
	if want := methodologyTypeParamDesc(); desc != want {
		t.Fatalf("the published description is not the one the builder produces:\n got %q\nwant %q", desc, want)
	}
	if !strings.Contains(desc, strings.Join(domain.MethodologyTypeEnum, ", ")) {
		t.Errorf("the suggested list is not rendered from domain.MethodologyTypeEnum in order, so it "+
			"can drift from MemoryTypeEnum without this test noticing; got %q", desc)
	}
}

// TestSaveArtifactTypeIsEnforced is the third half, and the one that makes the
// other two more than a rewording. It is also the leniency assertion: the six
// pass, and so does an off-list methodology.* name.
//
// 🔴 methodology.playbook is not a hypothetical. It is the live value this work
// item measured — 3 rows on ieops wi_TYllxcv1, operator handover documents with
// no slot among the six — and it is the reason the names were withdrawn instead
// of enforced. A change that makes this case fail has reversed §6.2 T2-6's
// ruling, not tightened a validator.
func TestSaveArtifactTypeIsEnforced(t *testing.T) {
	base := func(ty string) map[string]any {
		return map[string]any{"type": ty, "work_item_id": "wi_probe499", "content": "x"}
	}

	for _, ty := range domain.MethodologyTypeEnum {
		if err := validatePfSaveArtifactArgs(base(ty)); err != nil {
			t.Errorf("the suggested kind %q was refused: %v", ty, err)
		}
	}
	for _, ty := range []string{"methodology.playbook", "methodology.release", "methodology.audit"} {
		if err := validatePfSaveArtifactArgs(base(ty)); err != nil {
			t.Errorf("off-list %q was refused, which reverses §6.2 T2-6's leniency ruling and "+
				"would have rejected the 3 methodology.playbook rows measured live: %v", ty, err)
		}
	}

	// Refusals. "spec" is the aihub#211 case the withdrawn enum existed to catch
	// statically; fact.note / experience.debug are the wider gap aihub#499 found,
	// which the server never refused at all because Remember accepts every
	// member of MemoryTypePrefixes.
	for _, ty := range []string{"spec", "retro", "fact.note", "experience.debug", "rule.work", "playbook"} {
		err := validatePfSaveArtifactArgs(base(ty))
		if err == nil {
			t.Errorf("type %q was accepted; pf_save_artifact must take only %q types",
				ty, domain.MethodologyTypePrefix)
			continue
		}
		// Every refusal names the offending value, the rule and the suggestions —
		// the aihub#420 error shape. A bare "invalid type" leaves the caller
		// guessing which of the two doors to use.
		for _, want := range []string{ty, domain.MethodologyTypePrefix, domain.MethodologyTypeEnum[0]} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("refusing %q produced a message missing %q: %v", ty, want, err)
			}
		}
	}

	// 🔴 And the hint is case-correct, which is why offPrefixHint branches.
	// "store it with pf_remember instead" would be actively wrong for a bare
	// name: pf_remember refuses "spec" too, so a caller who followed it would
	// earn a second 400. Each arm is pinned to the case it is right for.
	for ty, want := range map[string]string{
		// aihub#211's corpus shape: a real kind with the prefix filed off.
		"spec":  "did you mean " + domain.MethodologyTypePrefix + "spec",
		"retro": "did you mean " + domain.MethodologyTypePrefix + "retro",
		// A legal memory type, wrong door.
		"fact.note":        "pf_remember type",
		"experience.debug": "pf_remember type",
		"rule.work":        "pf_remember type",
		// Neither: not a kind, and no legal prefix.
		"playbook": domain.MemoryTypePrefixGloss(),
	} {
		err := validatePfSaveArtifactArgs(base(ty))
		if err == nil {
			t.Errorf("type %q was accepted", ty)
			continue
		}
		if !strings.Contains(err.Error(), want) {
			t.Errorf("refusing %q should hint %q, got: %v", ty, want, err)
		}
	}
	// The negative half of that: a bare kind must NOT be told to use pf_remember,
	// which would refuse it as well.
	if err := validatePfSaveArtifactArgs(base("spec")); err == nil {
		t.Error("spec was accepted")
	} else if strings.Contains(err.Error(), "pf_remember type") {
		t.Errorf("refusing bare \"spec\" pointed the caller at pf_remember, which refuses it too: %v", err)
	}

	// The '|' arm, and the ORDER it sits in: a piped type has a legal prefix, so
	// the prefix arm cannot catch it.
	err := validatePfSaveArtifactArgs(base("methodology.spec|methodology.plan"))
	if err == nil {
		t.Error("a piped type was accepted; it has a legal prefix, so only the '|' arm can refuse it")
	} else if !strings.Contains(err.Error(), "'|'") {
		t.Errorf("a piped type was refused for the wrong reason: %v", err)
	}

	// Required-field arms, moved off the handler into the validator by aihub#499.
	if err := validatePfSaveArtifactArgs(map[string]any{"work_item_id": "wi_probe499"}); err == nil {
		t.Error("a missing type was accepted")
	}
	if err := validatePfSaveArtifactArgs(map[string]any{"type": domain.MethodologyTypeEnum[0]}); err == nil {
		t.Error("a missing work_item_id was accepted")
	}
}
