package roles

import (
	"fmt"
	"strings"
)

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

// The two placeholder tokens a role prompt may carry. A role's YAML prompt is
// rendered VERBATIM into all four harnesses' agent files, so any sentence in it
// that describes a harness MECHANISM is a sentence that is false for three of
// them. aihub#676 measured the consequence rather than inferring it: reviewer's
// and explorer's prompts both told the agent "Bash stays available so you can
// run builds, tests and linters", and pi's read-only shape is an ALLOWLIST
// (piReadOnlyTools) that contains no shell at all -- so under pi the reviewer
// was instructed to verify by running tests it had no way to run, leaving it
// only "I could not verify" or, worse, an unverified approval.
//
// These tokens close that class the same way CompileCapability closes it for
// the frontmatter: the YAML keeps the harness-agnostic REASONS, and the one
// switch below compiles the harness-specific MECHANISM. A role prompt must
// never again spell out a tool name, a frontmatter key or a sandbox mode.
const (
	PlaceholderCapability  = "{{CAPABILITY}}"
	PlaceholderModelSource = "{{MODEL_SOURCE}}"
)

// ExpandPrompt substitutes PlaceholderCapability and PlaceholderModelSource in
// a role's prompt with prose that is true for THIS harness, and returns an
// error for any harness dispatch.go does not have a row for.
//
// modelDeclared says whether the caller is about to emit a model field for this
// role. It is not cosmetic: on the omit-and-warn fallback path (aihub#642 AC7)
// the generated file has NO model field, and a prompt that still claimed "your
// model is set by this file" would be flatly false exactly when an operator most
// needs to know why the role is running on something unexpected.
//
// Both tokens are optional; a prompt that carries neither is returned unchanged.
// The renderers do NOT require them -- but every rendered file is checked for a
// LEFTOVER "{{" by TestNoUnexpandedPlaceholders, so a typo'd token cannot ship.
func ExpandPrompt(prompt, harness string, readOnly, modelDeclared bool) (string, error) {
	if _, ok := DispatchFor(harness); !ok {
		return "", fmt.Errorf("roles: cannot expand prompt placeholders for unknown harness %q (have %v)", harness, DispatchHarnesses())
	}
	prompt = strings.ReplaceAll(prompt, PlaceholderCapability, capabilityNote(readOnly, harness))
	prompt = strings.ReplaceAll(prompt, PlaceholderModelSource, modelSourceNote(harness, modelDeclared))
	return prompt, nil
}

// capabilityNote is the prose counterpart of CompileCapability: for each
// harness it states what that harness's read-only shape ACTUALLY does, in the
// same file as the shape itself so the two cannot drift.
//
// The write-capable text is deliberately identical for all four harnesses --
// "you inherit the full tool set" is true everywhere, because every renderer
// emits no capability field at all for a write-capable role.
func capabilityNote(readOnly bool, harness string) string {
	if !readOnly {
		return `You are write-capable. No capability field is emitted for you, so you inherit your
harness's full tool set. The rules that govern writes (Iron Rules, worktree boundaries)
arrive with your prompt and the injected payload; this file does not restate them, so they
cannot drift here.`
	}
	switch harness {
	case "cc":
		return `You cannot modify the tree. This file's ` + "`disallowedTools`" + ` frontmatter removes Edit, Write
and NotebookEdit. Bash is NOT removed, so you can run builds, tests and linters yourself --
do not use it to modify the tree, commit, push or merge.`
	case "pi":
		return `You cannot modify the tree, and you also cannot run anything. This file's ` + "`tools`" + `
frontmatter is an ALLOWLIST; read it to see exactly what you have. It gives you file reading
and search, plus the read-only polyforge work-item and memory lookups -- and no shell. So you
CANNOT run builds, tests or linters yourself. Verify from the tree, the diff, and whatever
output is already in your prompt, and state plainly which claims you could not check. An
unverifiable claim is a WARN; never approve something on the assumption that it passed.`
	case "codex":
		return `You cannot modify the tree. This role runs under ` + "`sandbox_mode = \"read-only\"`" + `, so commands
still run but every write is refused, including /tmp and the working directory. Use that to
run builds, tests and linters; anything that tries to change a file will fail, by design.`
	case "opencode":
		return `You cannot modify the tree. This file's frontmatter sets ` + "`permission: edit: deny`" + `. Bash,
read, grep and glob are left at their inherited defaults, so you can normally run builds,
tests and linters -- do not use them to modify the tree, commit, push or merge. If a command
is refused, report that rather than approving unverified.`
	}
	// Unreachable: ExpandPrompt rejects any harness without a dispatch row
	// before calling this, and dispatch.go's table covers exactly these four.
	return ""
}

// modelSourceNote names WHERE this role's model came from, per harness. The CC
// branch is the only one that may claim portability (cc_aliases.yaml ships in
// the repo); the other three carry a machine-local identifier resolved at
// generation time against that machine's own catalog.
//
// The aihub#555 "an explicit per-invocation model silently overrides this file"
// measurement is stated ONLY under cc, because that is the only harness it was
// measured on. It used to appear in all five role prompts, i.e. asserted for
// four harnesses on one harness's evidence.
func modelSourceNote(harness string, modelDeclared bool) string {
	if !modelDeclared {
		return `NO model is declared for you. This machine's tier table had no candidate that resolved
in ` + harness + `'s local model catalog, so you inherit whatever model the caller runs with and your
tier is not in force. Generation warned about this on stderr; it is a known, observable
degradation, not a silent one.`
	}
	switch harness {
	case "cc":
		return `Your model is set by this file's ` + "`model:`" + ` frontmatter, a Claude Code alias -- the one
model identifier that means the same thing on every machine, which is why this file is
generated at build time and committed to the repo. The dispatching loop must NOT pass a
` + "`model`" + ` argument: an explicit per-invocation model silently overrides this file (measured,
aihub#555), which would turn this definition into dead text.`
	case "pi":
		return `Your model is set by this file's ` + "`model:`" + ` frontmatter. It is a machine-local
` + "`provider/model`" + ` identifier, resolved when this file was generated from
~/.polyforge/config.toml against this machine's own ` + "`pi --list-models`" + ` catalog, and it is NOT
portable to another machine. The dispatching loop should not pass a model argument over it.`
	case "codex":
		return `Your model is set by the ` + "`model`" + ` key of the TOML file this text was generated into --
not by frontmatter, because codex role definitions are TOML, not markdown. The file codex
actually loads is the config profile at ` + "`$CODEX_HOME/step-<role>.config.toml`" + `, selected with
` + "`-p <name>`" + `. It is a machine-local slug resolved at generation time from
~/.polyforge/config.toml against ` + "`codex debug models`" + `.`
	case "opencode":
		return `Your model is set by this file's ` + "`model:`" + ` frontmatter. It is a machine-local
` + "`provider/model`" + ` identifier, resolved when this file was generated from
~/.polyforge/config.toml against this machine's own ` + "`opencode models`" + ` catalog, and it is NOT
portable to another machine.`
	}
	return ""
}
