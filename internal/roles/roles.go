// Package roles is the single source of truth for polyforge's role/tier/capability
// data model: which step ids run as which role, at what model tier, with how much
// write capability. Role definitions are harness-agnostic; per-harness rendering
// (Claude Code frontmatter, pi frontmatter, codex TOML, ...) lives in compile.go
// and the generators under internal/roles/gen and internal/cli.
//
// See aihub#642 for the design (methodology.spec mem_m7iwe3hM, methodology.plan
// mem_leO2mZmw): five FINAL roles (executor/operator/explorer/reviewer/designer)
// across four tiers (lowest/low/default/raised), each carrying one capability
// boolean (read_only) and a set of step ids it is dispatched for.
package roles

import (
	"embed"
	"fmt"
	"sort"

	"gopkg.in/yaml.v3"
)

//go:embed definitions/*.yaml
var definitionsFS embed.FS

// Regenerate plugins/polyforge/agents/step-*.md after changing anything under
// definitions/ or compile.go's CC shape. This is the first go:generate
// directive in this repo; the cc_staleness_gate test (aihub#642) fails the
// build if the committed files drift from what this produces.
//go:generate go run ./gen

// Capability is the one harness-agnostic capability a role carries today. The
// per-harness translation of this bool (CC disallowedTools / pi tools allowlist /
// codex sandbox_mode / a future opencode permission table) lives in compile.go.
type Capability struct {
	ReadOnly bool `yaml:"read_only"`
}

// Role is one entry of the role/tier/capability/step-id data model, parsed from
// internal/roles/definitions/<name>.yaml. Prompt and Description are the CC/pi
// agent body and one-line frontmatter description respectively; per-harness
// generators reuse them verbatim (they are harness-agnostic prose).
type Role struct {
	Name       string     `yaml:"name"`
	Tier       string     `yaml:"tier"`
	Capability Capability `yaml:"capability"`
	StepIDs    []string   `yaml:"step_ids"`
	// OccurrenceCount is the measured aihub#642 scenario-repo occurrence count for
	// this role (how many `## Step: <id>` headings across every
	// polyforge-coding wi_type template bind to one of this role's StepIDs).
	// It is documentation/audit data, not consumed by the compiler or any
	// generator — StepIDs (deduplicated) is the actual dispatch-relevant set.
	OccurrenceCount int    `yaml:"occurrence_count"`
	Description     string `yaml:"description"`
	Prompt          string `yaml:"prompt"`
}

// ValidTiers is the fixed 4-rung tier vocabulary (aihub#642 decision #1). Order
// matters: it is low-to-high, used wherever tiers must be walked or displayed in
// a stable order.
var ValidTiers = []string{"lowest", "low", "default", "raised"}

func isValidTier(t string) bool {
	for _, v := range ValidTiers {
		if v == t {
			return true
		}
	}
	return false
}

// roleFileNames lists the embedded role definitions to load, in a fixed order so
// generator output is deterministic regardless of filesystem directory order.
// cc_aliases.yaml is a sibling file with a different shape (see LoadCCAliases)
// and is deliberately excluded here.
var roleFileNames = []string{
	"executor.yaml",
	"operator.yaml",
	"explorer.yaml",
	"reviewer.yaml",
	"designer.yaml",
}

// LoadRoles parses every role definition YAML in the embedded FS and returns them
// in the fixed roleFileNames order. It fails closed: a missing file, a parse
// error, an unknown tier name, or an empty name/step_ids is an error, never a
// silently skipped role.
func LoadRoles() ([]Role, error) {
	out := make([]Role, 0, len(roleFileNames))
	for _, fname := range roleFileNames {
		data, err := definitionsFS.ReadFile("definitions/" + fname)
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", fname, err)
		}
		var r Role
		if err := yaml.Unmarshal(data, &r); err != nil {
			return nil, fmt.Errorf("parse %s: %w", fname, err)
		}
		if r.Name == "" {
			return nil, fmt.Errorf("%s: missing name", fname)
		}
		if !isValidTier(r.Tier) {
			return nil, fmt.Errorf("%s: invalid tier %q (must be one of %v)", fname, r.Tier, ValidTiers)
		}
		if len(r.StepIDs) == 0 {
			return nil, fmt.Errorf("%s: step_ids is empty", fname)
		}
		out = append(out, r)
	}
	return out, nil
}

// RoleByName returns the role with the given name from a slice previously
// returned by LoadRoles, or ok=false if not present.
func RoleByName(roleList []Role, name string) (Role, bool) {
	for _, r := range roleList {
		if r.Name == name {
			return r, true
		}
	}
	return Role{}, false
}

// RoleForStepID walks roleList (as returned by LoadRoles) and returns the role
// bound to the given step id. A step id bound to more than one role is a data
// error upstream (roles_loader_tests guards against it); the first match wins
// here rather than panicking, since this is a runtime lookup path.
func RoleForStepID(roleList []Role, stepID string) (Role, bool) {
	for _, r := range roleList {
		for _, id := range r.StepIDs {
			if id == stepID {
				return r, true
			}
		}
	}
	return Role{}, false
}

// SortedStepIDs returns a copy of the role's step ids sorted, for deterministic
// generator output and test assertions.
func (r Role) SortedStepIDs() []string {
	out := append([]string(nil), r.StepIDs...)
	sort.Strings(out)
	return out
}

// CCAliases is the portable, repo-committed, harness-agnostic table translating
// a tier name to a Claude Code model alias (sonnet/opus/haiku). It is read ONLY
// by the build-time CC agent generator (internal/roles/gen) — never by
// internal/config.LoadMachineConfig — so no contributor's personal config.toml
// can introduce staleness-gate noise (aihub#642 plan decision #2).
type CCAliases map[string]string

// LoadCCAliases parses the embedded cc_aliases.yaml sibling file and validates
// every tier in ValidTiers has an entry.
func LoadCCAliases() (CCAliases, error) {
	data, err := definitionsFS.ReadFile("definitions/cc_aliases.yaml")
	if err != nil {
		return nil, fmt.Errorf("read cc_aliases.yaml: %w", err)
	}
	var aliases CCAliases
	if err := yaml.Unmarshal(data, &aliases); err != nil {
		return nil, fmt.Errorf("parse cc_aliases.yaml: %w", err)
	}
	for _, tier := range ValidTiers {
		if _, ok := aliases[tier]; !ok {
			return nil, fmt.Errorf("cc_aliases.yaml: missing tier %q", tier)
		}
	}
	return aliases, nil
}
