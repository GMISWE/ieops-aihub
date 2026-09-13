package roles

import (
	"bytes"
	"fmt"
)

// RenderCCAgentFiles renders every role in roleList into Claude Code's
// step-<role>.md content, keyed by output file name ("step-executor.md", ...).
// This is the ONE code path both internal/roles/gen (the go:generate-driven
// writer, aihub#642 plan step 6) and the cc_staleness_gate test (plan step 7)
// use -- the gate diffs this function's output against the committed files
// rather than re-implementing the rendering, so gate and generator cannot
// silently diverge from each other.
//
// Regenerating "step-executor.md" and "step-reviewer.md" from this function
// MUST byte-match their currently committed content exactly:
// internal/cli/engine_native_dispatch_model_test.go's agentFrontmatter()
// parses their frontmatter today, and must keep passing unmodified.
func RenderCCAgentFiles(roleList []Role, aliases CCAliases) (map[string]string, error) {
	out := make(map[string]string, len(roleList))
	for _, r := range roleList {
		alias, ok := aliases[r.Tier]
		if !ok || alias == "" {
			return nil, fmt.Errorf("role %q has tier %q with no cc_aliases.yaml entry", r.Name, r.Tier)
		}
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
		fmt.Fprintf(&b, "name: step-%s\n", r.Name)
		fmt.Fprintf(&b, "description: %s\n", r.Description)
		fmt.Fprintf(&b, "model: %s\n", alias)
		if shape.CCDisallowedTools != "" {
			fmt.Fprintf(&b, "disallowedTools: %s\n", shape.CCDisallowedTools)
		}
		b.WriteString("---\n\n")
		b.WriteString(r.Prompt)

		out[fmt.Sprintf("step-%s.md", r.Name)] = b.String()
	}
	return out, nil
}
