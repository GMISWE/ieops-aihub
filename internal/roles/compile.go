package roles

import "fmt"

// Shape is one harness-native rendering of a Capability. Each field means
// something only for its own harness; a generator reads the one field it
// cares about and, when that field is empty, omits the corresponding
// frontmatter/config key entirely rather than emitting an empty value.
//
// This is the single code path aihub#642's design targets: one boolean
// (Capability.ReadOnly) compiles to four harness-native shapes through one
// switch, instead of 5 roles x N harnesses of hand-authored copies drifting
// independently (the same class of bug aihub#294 named for model names,
// applied here to capability declarations).
type Shape struct {
	// CCDisallowedTools is Claude Code's `disallowedTools:` frontmatter value,
	// e.g. "Edit, Write, NotebookEdit" -- a plain comma-separated string, not a
	// YAML list: internal/cli/engine_native_dispatch_model_test.go's
	// agentFrontmatter() parser is line-based (requires a literal `---\n`
	// prefix and a `\n---` end fence, splits remaining top-level lines on the
	// first ":"), not a real YAML parser, and this is the value shape
	// step-reviewer.md already commits to. Empty means: omit the
	// disallowedTools line entirely (write-capable role).
	CCDisallowedTools string

	// PiTools is pi's `tools:` frontmatter value, e.g. pf-explore.md's
	// existing allowlist. Empty means: omit the tools line entirely, which
	// pi's subagent extension reads as "inherit everything" -- write, edit and
	// subagent included (aihub#606) -- the correct meaning for a
	// write-capable role, not an accidental one.
	PiTools string

	// CodexSandboxMode is codex's `sandbox_mode` agent-file field.
	// "read-only" for read-only roles. Empty (omit the field) for
	// write-capable roles: this layer does not assert a specific write-mode
	// sandbox value (e.g. "workspace-write") for codex, matching the same
	// omit-and-inherit-the-caller's-default fallback aihub#642 uses for an
	// unresolvable model candidate -- one fewer place this code has an
	// opinion codex's own defaults might already hold correctly.
	CodexSandboxMode string
}

// ccReadOnlyDisallowedTools mirrors step-reviewer.md's frontmatter value
// byte-for-byte. It must never drift from the committed file's contract:
// internal/cli/engine_native_dispatch_model_test.go pins the parsed value,
// and the cc_staleness_gate (aihub#642 plan step 7) regenerates and diffs
// step-reviewer.md against this constant.
const ccReadOnlyDisallowedTools = "Edit, Write, NotebookEdit"

// piReadOnlyTools mirrors pf-explore.md's `tools:` frontmatter value
// byte-for-byte -- see that file's own comment for why every entry is spelled
// out individually (the allowlist is an exact-match Set that also filters MCP
// tool names, so a prefix or wildcard would not do the same job).
const piReadOnlyTools = "read, grep, find, ls, polyforge_pf_get_work_item, polyforge_pf_get_step, polyforge_pf_list_work_items, polyforge_pf_recall"

// codexReadOnlySandboxMode is codex's sandbox_mode value for read-only roles.
const codexReadOnlySandboxMode = "read-only"

// SupportedHarnesses lists the harnesses CompileCapability accepts today.
// "opencode" is deliberately NOT included: aihub#653 owns opencode's
// permission-rules-table capability shape, and this wi must not implement it
// (aihub#642 hard constraint, AC12). CompileCapability still recognizes the
// string so the caller gets a clear "not yet" error instead of "unknown
// harness" -- and so that adding it later needs a new case in
// CompileCapability plus a new Shape field, not a reshaped interface.
var SupportedHarnesses = []string{"cc", "pi", "codex"}

// CompileCapability renders one role's read_only capability into harness's
// native shape. harness must be "cc", "pi", or "codex" (see SupportedHarnesses).
func CompileCapability(readOnly bool, harness string) (Shape, error) {
	switch harness {
	case "cc":
		if readOnly {
			return Shape{CCDisallowedTools: ccReadOnlyDisallowedTools}, nil
		}
		return Shape{}, nil
	case "pi":
		if readOnly {
			return Shape{PiTools: piReadOnlyTools}, nil
		}
		return Shape{}, nil
	case "codex":
		if readOnly {
			return Shape{CodexSandboxMode: codexReadOnlySandboxMode}, nil
		}
		return Shape{}, nil
	case "opencode":
		return Shape{}, fmt.Errorf("roles: opencode capability compilation is aihub#653's scope, not implemented in this layer (aihub#642 AC12)")
	default:
		return Shape{}, fmt.Errorf("roles: unknown harness %q (supported: %v)", harness, SupportedHarnesses)
	}
}
