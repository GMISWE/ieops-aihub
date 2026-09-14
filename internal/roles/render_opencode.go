package roles

import (
	"bytes"
	"fmt"
	"strings"
)

// RenderOpencodeAgentFiles renders every role in roleList into opencode's
// step-<role>.md content, keyed by output file name ("step-executor.md", ...).
// opencode DOES accept an overriding `name:` frontmatter field (config/config.ts
// ~423 spreads `...md.data`; agent/agent.ts ~226 -- confirmed against opencode's
// own source, aihub#653 code_review), so the filename is not the only possible
// identity source in general. This renderer deliberately never emits `name:`,
// so for OUR generated files the filename is what determines the agent's name
// in practice -- matching render_cc.go's and render_codex.go's "step-<role>"
// naming keeps the same logical agent name across all three "step-" harnesses.
//
// Unlike RenderPiAgentFiles/RenderCCAgentFiles/RenderCodexAgentFiles, this
// function deliberately does NOT call CompileCapability: compile.go's
// "opencode" case is a permanent stub (aihub#642 AC12) that this wi
// (aihub#653) must not touch -- SupportedHarnesses and Shape stay
// {"cc","pi","codex"}-only, pinned by the passing negative-control test
// TestCompileCapabilityRejectsOpencodeAndUnknownHarness in compile_test.go.
// opencode's read-only expression is a structurally different shape from the
// other three anyway (a permission RULE MAPPING, not a denylist/allowlist/
// sandbox-tier), so it gets its own private, self-contained compiler
// (opencodePermissionBlock below) instead of a new Shape field.
//
// resolvedModels supplies the model slug to declare per role name, already
// resolved from the machine's opencode model catalog (`opencode models`) by
// the caller (internal/cli/roles_generate.go's CatalogProbe); this function
// never probes a catalog itself. A role absent from resolvedModels (or mapped
// to "") gets no `model:` field at all -- same omit-and-inherit convention as
// the other three renderers.
func RenderOpencodeAgentFiles(roleList []Role, resolvedModels map[string]string) (map[string]string, error) {
	out := make(map[string]string, len(roleList))
	for _, r := range roleList {
		// Frontmatter field order (description, mode, model, permission)
		// mirrors the docs' own example ordering
		// (https://opencode.ai/docs/agents/): description and mode first
		// since every generated file carries them unconditionally, then the
		// two conditional fields in the order a reader hits them ("what
		// model does this run as" before "what can it not do").
		var b bytes.Buffer
		b.WriteString("---\n")
		fmt.Fprintf(&b, "description: %s\n", yamlScalar(r.Description))
		b.WriteString("mode: subagent\n")
		if model := resolvedModels[r.Name]; model != "" {
			fmt.Fprintf(&b, "model: %s\n", model)
		}
		if perm := opencodePermissionBlock(r.Capability.ReadOnly); perm != "" {
			b.WriteString(perm)
		}
		b.WriteString("---\n\n")
		b.WriteString(r.Prompt)

		out[fmt.Sprintf("step-%s.md", r.Name)] = b.String()
	}
	return out, nil
}

// opencodePermissionBlock compiles a role's ReadOnly capability into
// opencode's frontmatter permission mapping (a plain YAML map of permission
// name -> "allow"|"ask"|"deny", per https://opencode.ai/docs/agents/ -- NOT
// the ordered rule array `opencode agent list` prints at runtime, which is a
// resolved/merged display view of the fully-loaded config, not the authoring
// schema a role's .md frontmatter is written in).
//
// read_only=true gets exactly one key, `edit: deny` -- parity with CC's
// disallowedTools: "Edit, Write, NotebookEdit" shape (render_cc.go): block
// mutation, deliberately leave bash/read/grep/glob/list/webfetch etc. unset
// so they inherit the caller's default (typically allow), because a reviewer
// role still needs to run tests and read broadly. opencode's single `edit`
// permission key already covers write/edit/apply_patch (one key, not CC's
// three separate tool names) per the docs' key vocabulary, so one line is the
// complete parity shape, not an abbreviation of a larger one.
//
// read_only=false gets no permission block at all -- the same "absent means
// unrestricted, inherit everything" convention the other three renderers use
// for a write-capable role (an empty CCDisallowedTools/PiTools/
// CodexSandboxMode all mean "no field emitted", never "field emitted empty").
//
// Empirically verified, not just doc-derived (aihub#653): a hand-written
// probe agent file with only `permission:\n  edit: deny` in its frontmatter,
// loaded in a clean sandbox (opencode 1.18.30, `--pure`, isolated
// HOME/XDG_CONFIG_HOME/OPENCODE_CONFIG_DIR to strip this box's own plugin
// customization), resolved via `opencode agent list` to exactly one added
// rule -- {"permission":"edit","action":"deny","pattern":"*"} -- appended
// after the shared baseline rules, with bash/read/grep/glob/list/webfetch all
// left at their inherited default. No other keys are needed to get a
// genuinely read-only role (aihub#653 AC9); see
// TestRenderOpencodeAgentFiles_ReadOnlyIsGenuinelyReadOnly.
//
// KNOWN, INTENTIONAL divergence from cc/pi/codex (flagged in aihub#653
// code_review, not a defect): none of the permission keys this function ever
// emits is "task". opencode's tool/task.ts computes
// hasTaskPermission = agent.permission.some(r => r.permission == "task"); an
// agent with no "task" rule at all -- true for every role this package
// renders today -- gets task:*:deny injected into any child session it
// spawns. So every opencode step agent is one-level-terminal: it cannot
// itself dispatch a further subagent, unlike its cc/pi/codex counterparts.
// The wi's own background pre-cleared this (today's step graphs never ask a
// step-executor/step-reviewer to spawn a further Task), so this function
// takes no action on it -- but a future role that DID need to nest would
// need an explicit `task: allow` (or narrower) line added here, which this
// function does not currently know how to produce.
func opencodePermissionBlock(readOnly bool) string {
	if !readOnly {
		return ""
	}
	return "permission:\n  edit: deny\n"
}

// yamlScalar renders s as a YAML frontmatter value, quoting only when a plain
// (unquoted) scalar would parse differently or fail outright -- a leading
// character YAML treats as an indicator (quote, `#`, `&`, `*`, `!`, `|`, `>`,
// `%`, `@`, backtick, or a plain `-`/`?`/`:` that must be followed by a
// space to be safe), or a `: ` / trailing `:` / ` #` sequence anywhere in the
// string, per the YAML 1.1/1.2 plain-scalar grammar every generator in this
// package targets. Every committed role description today is safe plain
// prose and round-trips unquoted (byte-identical to before this helper
// existed), so this only changes behavior for a description nobody has
// written yet -- not a new risk today, just closing the door on one
// (aihub#653 code_review minor #8: config/config.ts throws InvalidError on a
// frontmatter parse failure, so an unquoted `: ` would hard-fail agent
// loading instead of degrading).
func yamlScalar(s string) string {
	needsQuote := s == ""
	if len(s) > 0 {
		switch s[0] {
		case '"', '\'', '#', '&', '*', '!', '|', '>', '%', '@', '`', '-', '?', ':', '[', ']', '{', '}', ',':
			needsQuote = true
		}
	}
	if strings.Contains(s, ": ") || strings.HasSuffix(s, ":") || strings.Contains(s, " #") ||
		strings.HasSuffix(s, " ") || strings.HasPrefix(s, " ") || strings.ContainsAny(s, "\n\t") {
		needsQuote = true
	}
	if !needsQuote {
		return s
	}
	return fmt.Sprintf("%q", s)
}
