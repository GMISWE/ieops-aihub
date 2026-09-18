package config

import (
	"strings"
	"testing"

	"github.com/pelletier/go-toml/v2"
)

// TestModelsTOMLRoundTripLegacyUnchanged pins the compatibility property the
// umbrella wi made a hard requirement: a machine config that predates
// [[models]] (aihub#708) loads, saves and reloads through the new struct with
// UNCHANGED semantics, and the marshalled bytes carry no models key at all.
// Adding the field must be invisible to every legacy machine.
func TestModelsTOMLRoundTripLegacyUnchanged(t *testing.T) {
	legacy := `
machine_id = "01234567-89ab-cdef-0123-456789abcdef"

[auth]
api_key_env = "POLYFORGE_TEST_KEY"

[server]
url = "http://aihub.example:8080"

[binary]
channel = "dev"

[roles.tiers]
default = [{ harness = "pi", model = "my-provider/claude-sonnet-4-5" }]
`
	var mc MachineConfig
	if err := toml.Unmarshal([]byte(legacy), &mc); err != nil {
		t.Fatalf("legacy config no longer parses: %v", err)
	}
	if len(mc.Models) != 0 {
		t.Fatalf("legacy config gained %d [[models]] entries from nowhere", len(mc.Models))
	}

	b, err := toml.Marshal(&mc)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(b), "models") {
		t.Fatalf("a config with no [[models]] marshals a models key:\n%s", b)
	}

	var re MachineConfig
	if err := toml.Unmarshal(b, &re); err != nil {
		t.Fatalf("remarshal: %v", err)
	}
	if re.MachineID != mc.MachineID || re.Auth.APIKeyEnv != mc.Auth.APIKeyEnv ||
		re.Server == nil || re.Server.URL != mc.Server.URL ||
		re.Roles == nil || len(re.Roles.Tiers["default"]) != 1 ||
		re.Roles.Tiers["default"][0].Harness != "pi" {
		t.Fatalf("legacy roundtrip lost data:\n%+v", re)
	}
}

// TestModelsTOMLRoundTrip pins that a [[models]] catalog survives
// load→marshal→load with every field intact, including the ordered uses
// slice, and that array-of-tables placement is stable.
func TestModelsTOMLRoundTrip(t *testing.T) {
	withModels := `
machine_id = "id"

[[models]]
name = "impl-opus"
harness = "pi"
model = "sub2api-claude/pi-claude-opus-5"
effort = "high"
uses = ["authoring"]
description = "primary implementer"

[[models]]
name = "review-astra"
harness = "codex"
model = "gpt-6-astra"
effort = "medium"
uses = ["review", "verification"]
`
	var mc MachineConfig
	if err := toml.Unmarshal([]byte(withModels), &mc); err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(mc.Models) != 2 {
		t.Fatalf("got %d entries, want 2", len(mc.Models))
	}

	b, err := toml.Marshal(&mc)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var re MachineConfig
	if err := toml.Unmarshal(b, &re); err != nil {
		t.Fatalf("remarshal: %v", err)
	}
	want := mc.Models
	got := re.Models
	if len(got) != len(want) {
		t.Fatalf("roundtrip changed entry count: %d -> %d", len(want), len(got))
	}
	for i := range want {
		if got[i].Name != want[i].Name || got[i].Harness != want[i].Harness ||
			got[i].Model != want[i].Model || got[i].Effort != want[i].Effort ||
			got[i].Description != want[i].Description ||
			strings.Join(got[i].Uses, ",") != strings.Join(want[i].Uses, ",") {
			t.Fatalf("entry %d changed: %+v -> %+v", i, want[i], got[i])
		}
	}
}

func TestLookupModel(t *testing.T) {
	mc := &MachineConfig{Models: []MachineModel{
		{Name: "a", Harness: "pi", Model: "p/m", Effort: "low", Uses: []string{"authoring"}},
	}}
	if m, ok := mc.LookupModel("a"); !ok || m.Name != "a" {
		t.Fatalf("LookupModel(a) = %+v, %v", m, ok)
	}
	if _, ok := mc.LookupModel("nope"); ok {
		t.Fatal("LookupModel(nope) hit")
	}
	var nilMC *MachineConfig
	if _, ok := nilMC.LookupModel("a"); ok {
		t.Fatal("nil receiver must not hit")
	}
}

func TestValidateModels(t *testing.T) {
	valid := func(m MachineModel) MachineModel { return m }
	base := MachineModel{
		Name: "impl-opus", Harness: "pi", Model: "sub2api-claude/pi-claude-opus-5",
		Effort: "high", Uses: []string{"authoring"},
	}

	tests := []struct {
		name   string
		models []MachineModel
		wantOK bool
	}{
		{"valid single", []MachineModel{valid(base)}, true},
		{"valid cc alias", []MachineModel{
			{Name: "cc-reviewer", Harness: "cc", Model: "sonnet", Effort: "medium", Uses: []string{"review"}},
		}, true},
		{"empty catalog is allowed", nil, true},
		// The same model at two efforts is TWO routes, not a duplicate: the
		// effort is part of the dispatch-time authorization key.
		{"same model at two efforts is two routes", []MachineModel{
			{Name: "impl", Harness: "pi", Model: "sub2api-claude/pi-claude-opus-5", Effort: "low", Uses: []string{"authoring"}},
			{Name: "impl-high", Harness: "pi", Model: "sub2api-claude/pi-claude-opus-5", Effort: "high", Uses: []string{"authoring"}},
		}, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateModels(tc.models)
			if tc.wantOK && err != nil {
				t.Fatalf("valid catalog refused: %v", err)
			}
			if !tc.wantOK && err == nil {
				t.Fatal("invalid catalog accepted")
			}
		})
	}
}

// TestValidateModelsProblems EXECUTES the negative table. aihub#708 repair:
// this table was previously defined inside TestValidateModels and reached
// only by matching each entry's name against the positive table's case
// names — which never matched, so no negative case ever ran (the re-review's
// "model negative `problems` table not iterated"). Defining a table is not
// testing it: every entry here is iterated and must be REFUSED outright —
// no silent fallback to a partial route — with an error naming every claimed
// substring, so a validation message that loses a clause loses this test.
func TestValidateModelsProblems(t *testing.T) {
	valid := func(m MachineModel) MachineModel { return m }
	base := MachineModel{
		Name: "impl-opus", Harness: "pi", Model: "sub2api-claude/pi-claude-opus-5",
		Effort: "high", Uses: []string{"authoring"},
	}
	problems := []struct {
		name    string
		models  []MachineModel
		wantSub []string
	}{
		{"unknown harness", []MachineModel{{Name: "x", Harness: "claude", Model: "sonnet", Effort: "low", Uses: []string{"authoring"}}},
			[]string{`unknown harness "claude"`, `spelled "cc"`}},
		{"bare pi model", []MachineModel{{Name: "x", Harness: "pi", Model: "claude-sonnet-4-5", Effort: "low", Uses: []string{"authoring"}}},
			[]string{"BARE pi model id"}},
		{"bare opencode model", []MachineModel{{Name: "x", Harness: "opencode", Model: "claude-sonnet-5", Effort: "low", Uses: []string{"authoring"}}},
			[]string{"BARE opencode model id"}},
		{"unsupported uniform effort", []MachineModel{{Name: "x", Harness: "codex", Model: "gpt-6-astra", Effort: "xhigh", Uses: []string{"authoring"}}},
			[]string{`effort "xhigh" is not in the uniform public effort vocabulary`}},
		{"harness native minimal refused", []MachineModel{{Name: "x", Harness: "cc", Model: "sonnet", Effort: "minimal", Uses: []string{"authoring"}}},
			[]string{`effort "minimal"`}},
		{"missing effort", []MachineModel{{Name: "x", Harness: "cc", Model: "sonnet", Uses: []string{"authoring"}}},
			[]string{"declares no effort"}},
		{"duplicate names", []MachineModel{valid(base), valid(base)},
			[]string{`duplicates name "impl-opus"`}},
		// Astra review repair (aihub#708): a duplicate candidate triple would
		// silently last-win the dispatch-time route map and its uses policy.
		{"duplicate candidate triples", []MachineModel{
			{Name: "impl-opus", Harness: "pi", Model: "sub2api-claude/pi-claude-opus-5", Effort: "high", Uses: []string{"authoring"}},
			{Name: "shadow", Harness: "pi", Model: "sub2api-claude/pi-claude-opus-5", Effort: "high", Uses: []string{"review", "verification"}},
		}, []string{"pins the same route as entry named \"impl-opus\"", `harness="pi"`, `model="sub2api-claude/pi-claude-opus-5"`, `effort="high"`, "uses policy"}},
		{"bad name shape", []MachineModel{{Name: "Impl.Opus", Harness: "pi", Model: "p/m", Effort: "low", Uses: []string{"authoring"}}},
			[]string{"invalid name"}},
		{"no uses", []MachineModel{{Name: "x", Harness: "pi", Model: "p/m", Effort: "low"}},
			[]string{"declares no uses"}},
		{"unknown use token", []MachineModel{{Name: "x", Harness: "pi", Model: "p/m", Effort: "low", Uses: []string{"authoring", "coding"}}},
			[]string{`use 1 ("coding") is not in the closed capability vocabulary`}},
		{"whitespace model", []MachineModel{{Name: "x", Harness: "codex", Model: " gpt-6-astra", Effort: "low", Uses: []string{"authoring"}}},
			[]string{"whitespace or control characters"}},
		{"collects every problem", []MachineModel{
			{Name: "a", Harness: "claude", Model: "sonnet", Effort: "xhigh"},
			{Name: "b", Harness: "pi", Model: "bare", Effort: "low", Uses: []string{"authoring"}},
		}, []string{"unknown harness", `effort "xhigh"`, "BARE pi model id"}},
	}
	for _, tc := range problems {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateModels(tc.models)
			if err == nil {
				t.Fatalf("ValidateModels accepted an invalid catalog:\n%+v", tc.models)
			}
			for _, sub := range tc.wantSub {
				if !strings.Contains(err.Error(), sub) {
					t.Errorf("error missing %q:\n%s", sub, err)
				}
			}
		})
	}
}
