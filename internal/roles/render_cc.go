package roles

import (
	"bytes"
	"fmt"
	"regexp"
)

// ccModelRe is the shape a value must have to be written into a CC agent file's
// `model:` frontmatter line.
//
// 🔴 It is NOT a model catalog and must not be read as one. Claude Code exposes
// no local model-listing command this generator could probe the way it probes
// `codex debug models`, `pi --list-models` and `opencode models`, so there is no
// oracle here for "does this model exist" -- only for "can the readers of this
// line parse it". A misspelled but well-shaped alias passes this and fails at
// dispatch; that is a real gap, stated rather than papered over.
//
// The pattern is not invented here either: it is the one BOTH existing readers
// of that line already enforce, so anything it rejects is a value that would be
// silently invisible to them rather than merely unusual --
// plugins/polyforge/hooks/pf-skill-router's AGENT_MODEL_RE (which degrades the
// whole tier table to a stub when any of the four tier-source files has no
// matching line) and internal/cli/engine_native_dispatch_model_test.go's
// agentModelRe. Writing a third spelling of it was the alternative; this is the
// first of the three that is exported, so the other two can converge on it.
var ccModelRe = regexp.MustCompile(`^[a-z][a-z0-9.-]*$`)

// ValidCCModel reports whether model can be written into a CC agent file's
// `model:` frontmatter and still be read back by this repo's parsers of that
// line. See ccModelRe for what it does and does not prove.
func ValidCCModel(model string) bool {
	return ccModelRe.MatchString(model)
}

// RenderCCAgentFiles renders every role in roleList into Claude Code's
// step-<role>.md content from the repo-committed cc_aliases.yaml table, keyed
// by output file name ("step-executor.md", ...).
//
// This is the DEFAULT-VALUE entry point: it is what internal/roles/gen (the
// go:generate-driven writer, aihub#642 plan step 6) and the cc_staleness_gate
// test (plan step 7) call, so the gate diffs the committed files against the
// same repo table that produced them and is unaffected by any machine's
// ~/.polyforge/config.toml. aihub#681 added the per-machine entry point
// alongside it (RenderCCAgentFilesWithModels); both funnel through the one
// rendering loop below, so gate, generator and the serve-startup regeneration
// cannot silently diverge from each other.
//
// Regenerating "step-executor.md" and "step-reviewer.md" from this function
// MUST byte-match their currently committed content exactly:
// internal/cli/engine_native_dispatch_model_test.go's agentFrontmatter()
// parses their frontmatter today, and must keep passing unmodified.
func RenderCCAgentFiles(roleList []Role, aliases CCAliases) (map[string]string, error) {
	models := make(map[string]string, len(roleList))
	for _, r := range roleList {
		alias, ok := aliases[r.Tier]
		if !ok || alias == "" {
			return nil, fmt.Errorf("role %q has tier %q with no cc_aliases.yaml entry", r.Name, r.Tier)
		}
		models[r.Name] = alias
	}
	return RenderCCAgentFilesWithModels(roleList, models)
}

// RenderCCAgentFilesWithModels is RenderCCAgentFiles with the model chosen per
// ROLE NAME by the caller instead of per TIER by cc_aliases.yaml. It is the
// aihub#681 entry point: a machine whose ~/.polyforge/config.toml names a
// `harness = "cc"` candidate has these five files regenerated from that table
// at MCP-server startup (internal/cli.GenerateCCAgents), while a machine that
// configures nothing keeps the committed bytes RenderCCAgentFiles produced.
//
// A role absent from resolvedModels, or mapped to "", gets NO `model:` field at
// all and its prompt says so -- the same observable omit-and-inherit degradation
// aihub#642 AC7 defines for the three install-time harnesses, and the reason CC
// now has one where the doc comment this replaces said it had none. Callers must
// NOT substitute a guessed identifier; GenerateCCAgents falls back to the
// cc_aliases default only where the tier named no cc candidate at all, and omits
// (loudly) where it named one this renderer cannot write.
//
// ⚠️ Omitting is not free: plugins/polyforge/hooks/pf-skill-router reads these
// five files' `model:` lines to build its tier table and degrades to a stub when
// ANY of the four tier-source files lacks one. That is a designed, inert
// degradation rather than a crash, but it is why omission must stay loud.
func RenderCCAgentFilesWithModels(roleList []Role, resolvedModels map[string]string) (map[string]string, error) {
	out := make(map[string]string, len(roleList))
	for _, r := range roleList {
		shape, err := CompileCapability(r.Capability.ReadOnly, "cc")
		if err != nil {
			return nil, fmt.Errorf("role %q: %w", r.Name, err)
		}

		// Frontmatter field order (name, description, model, disallowedTools)
		// matches step-executor.md/step-reviewer.md's committed order exactly.
		// disallowedTools is emitted only when non-empty: a write-capable role
		// must NOT get an empty `disallowedTools:` line -- that would parse as
		// a present-but-empty key under agentFrontmatter(), not as "absent".
		var b bytes.Buffer
		b.WriteString("---\n")
		name := mustAgentName("cc", r.Name)
		fmt.Fprintf(&b, "name: %s\n", name)
		fmt.Fprintf(&b, "description: %s\n", r.Description)
		model := resolvedModels[r.Name]
		if model != "" {
			fmt.Fprintf(&b, "model: %s\n", model)
		}
		if shape.CCDisallowedTools != "" {
			fmt.Fprintf(&b, "disallowedTools: %s\n", shape.CCDisallowedTools)
		}
		b.WriteString("---\n\n")
		prompt, err := ExpandPrompt(r.Prompt, "cc", r.Capability.ReadOnly, model != "")
		if err != nil {
			return nil, fmt.Errorf("role %q: %w", r.Name, err)
		}
		b.WriteString(prompt)

		out[name+".md"] = b.String()
	}
	return out, nil
}
