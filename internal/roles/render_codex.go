package roles

import (
	"bytes"
	"fmt"
	"strings"
)

// RenderCodexAgentFiles renders every role in roleList into codex's
// step-<role>.toml content, keyed by output file name ("step-executor.toml",
// ...). resolvedModels supplies the model slug to declare per role name --
// already validated against the machine's codex model catalog by the caller
// (internal/cli/roles_generate.go's CatalogProbe, aihub#642 AC6); this
// function never probes a catalog itself. A role absent from resolvedModels
// (or mapped to "") gets no `model` key at all -- the omit-and-warn fallback
// shape (AC7); the caller is responsible for having already emitted the
// non-suppressible warning before calling this with an empty model.
//
// There is no committed codex agent file to byte-match today (greenfield --
// .codex-plugin/agents/ does not exist yet and is gitignored), so this TOML
// shape is this wi's own design choice, not a pre-existing contract: `name`,
// `description`, `model` (when resolved), `sandbox_mode` (when read-only, the
// capability compiler's codex shape), and `instructions` carrying the role's
// prompt body verbatim as a TOML multi-line basic string.
func RenderCodexAgentFiles(roleList []Role, resolvedModels map[string]string) (map[string]string, error) {
	out := make(map[string]string, len(roleList))
	for _, r := range roleList {
		shape, err := CompileCapability(r.Capability.ReadOnly, "codex")
		if err != nil {
			return nil, fmt.Errorf("role %q: %w", r.Name, err)
		}

		var b bytes.Buffer
		fmt.Fprintf(&b, "name = %s\n", tomlQuoted("step-"+r.Name))
		fmt.Fprintf(&b, "description = %s\n", tomlQuoted(r.Description))
		if model := resolvedModels[r.Name]; model != "" {
			fmt.Fprintf(&b, "model = %s\n", tomlQuoted(model))
		}
		if shape.CodexSandboxMode != "" {
			fmt.Fprintf(&b, "sandbox_mode = %s\n", tomlQuoted(shape.CodexSandboxMode))
		}
		fmt.Fprintf(&b, "instructions = %s\n", tomlTripleQuoted(r.Prompt))

		out[fmt.Sprintf("step-%s.toml", r.Name)] = b.String()
	}
	return out, nil
}

// tomlQuoted renders s as a TOML basic string. Role names/descriptions are
// short ASCII prose with no embedded quotes or control characters today, so
// Go's %q is a correct TOML basic string in practice; this helper exists so
// the two escaping conventions (single-line vs triple-quoted) are named
// distinctly at each call site. Not a general-purpose TOML string encoder --
// if a future role definition's prose needs real escaping, this is the place
// to add it.
func tomlQuoted(s string) string {
	return fmt.Sprintf("%q", s)
}

// tomlTripleQuoted renders s as a TOML multi-line basic string. Every
// committed role prompt today is plain markdown prose with no embedded `"""`
// sequence, so no escaping beyond the delimiters is done; a prompt that ever
// contained one would produce invalid TOML, same caveat as tomlQuoted above.
func tomlTripleQuoted(s string) string {
	var b strings.Builder
	b.WriteString(`"""` + "\n")
	b.WriteString(s)
	if !strings.HasSuffix(s, "\n") {
		b.WriteString("\n")
	}
	b.WriteString(`"""`)
	return b.String()
}
