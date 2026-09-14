package roles

import (
	"bytes"
	"fmt"
)

// RenderPiAgentFiles renders every role in roleList into pi's pf-<role>.md
// content, keyed by output file name ("pf-executor.md", ...). resolvedModels
// supplies the model to declare per role name (already resolved from the
// machine's candidate lists by the caller, internal/cli/roles_generate.go --
// this function never reads config or a catalog itself); a role absent from
// resolvedModels (or mapped to "") gets NO `model:` field at all. That is not
// a degraded case: it is the same no-model-field shape
// plugins/polyforge/pi/agents/pf-execute.md already ships today, which
// inherits whatever model the caller supplies. aihub#642 design decision #8
// makes that shape the generated *default* on resolution failure, rather than
// the permanent hand-authored state it is today.
func RenderPiAgentFiles(roleList []Role, resolvedModels map[string]string) (map[string]string, error) {
	out := make(map[string]string, len(roleList))
	for _, r := range roleList {
		shape, err := CompileCapability(r.Capability.ReadOnly, "pi")
		if err != nil {
			return nil, fmt.Errorf("role %q: %w", r.Name, err)
		}

		// Frontmatter field order (name, description, model, tools) mirrors
		// render_cc.go's (name, description, model, disallowedTools).
		var b bytes.Buffer
		b.WriteString("---\n")
		name := mustAgentName("pi", r.Name)
		fmt.Fprintf(&b, "name: %s\n", name)
		fmt.Fprintf(&b, "description: %s\n", r.Description)
		if model := resolvedModels[r.Name]; model != "" {
			fmt.Fprintf(&b, "model: %s\n", model)
		}
		if shape.PiTools != "" {
			fmt.Fprintf(&b, "tools: %s\n", shape.PiTools)
		}
		b.WriteString("---\n\n")
		b.WriteString(r.Prompt)

		out[name+".md"] = b.String()
	}
	return out, nil
}
