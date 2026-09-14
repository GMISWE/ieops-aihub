package roles

import (
	"fmt"
	"sort"
)

// aihub#670 — the per-harness subagent dispatch surface, in ONE place.
//
// WHAT WENT WRONG
// ---------------
// plugins/polyforge/skills/pf-execute/engine.native.md stated the dispatch as
// `dispatch Agent(subagent_type=ROLE_AGENT[role], prompt=§0b)` with ROLE_AGENT mapping every
// role to "polyforge:step-<role>". `Agent`, `subagent_type` AND the "polyforge:" namespace are
// all three Claude Code-private, and that file ships byte-identical to three other harnesses:
//
//   - pi:       plugins/polyforge/pi/install.sh's place_skills() is
//     `cp -r "$PLUGIN_ROOT"/skills/* "$dst/"`, printing "(no modification needed)".
//   - codex:    plugins/polyforge/.codex-plugin/plugin.json declares "skills": "./skills/" —
//     the directory is read IN PLACE, so there is no copy step to transform.
//   - opencode: plugins/polyforge/opencode/install.sh never copies skills at all.
//
// Three of the four harnesses therefore have NO installer stage that could rewrite the file,
// which is why the fix is a table the markdown CARRIES rather than a transform an installer
// applies. The reader of that line is the model, not the `polyforge` process — injecting
// harness identity into the binary was refuted twice (aihub#649) and would not help here.
//
// WHY THIS TABLE IS IN internal/roles AND NOT ONLY IN THE MARKDOWN
// ----------------------------------------------------------------
// Each harness's agent IDENTITY is already decided in this package — render_cc.go writes
// "step-<role>", render_pi.go writes "pf-<role>", render_codex.go and render_opencode.go write
// "step-<role>". A dispatch table hand-written in markdown would be a SECOND copy of that
// decision, free to drift: change render_pi.go's prefix and a markdown-only gate stays green
// while every pi dispatch starts naming an agent that does not exist. The renderers below read
// their name from this table, and internal/cli/engine_native_dispatch_model_test.go pins the
// markdown against it — so a renamed agent file forces the documented dispatch to move with it.

// HarnessDispatch is one harness's native way to dispatch a polyforge step agent.
//
// AgentNameFormat is the agent's own name (and generated file stem) — what this package
// renders. AgentIDFormat is what the DISPATCH CALL must name, which is not always the same
// string: Claude Code addresses a plugin-shipped agent by the namespaced "polyforge:<name>"
// (measured aihub#555 — the bare name resolves to a same-named user/project agent when one
// exists, the namespaced id always resolves to the plugin's file), while pi, codex and opencode
// address the bare name.
//
// Call is the dispatch expression a model running under this harness must write, with <id>
// standing for AgentIDFormat applied to the resolved role and <§0b> for the dispatch template.
// It is prose for a model to copy, not something this package executes — and those two
// placeholder spellings are the ones the markdown uses verbatim, so that
// internal/cli/engine_native_dispatch_model_test.go can pin the documented row by exact
// containment rather than by a fuzzy match that would drift back open.
type HarnessDispatch struct {
	// Harness is the harness key, matching `polyforge roles generate <harness>`'s positional
	// argument where one exists ("cc" has no generate verb: its agent files are committed under
	// plugins/polyforge/agents/ and byte-diffed by cc_staleness_gate_test.go).
	Harness string

	// AgentNameFormat is a single %s format applied to the role name, e.g. "step-%s".
	AgentNameFormat string

	// AgentIDFormat is a single %s format applied to the role name, e.g. "polyforge:step-%s".
	AgentIDFormat string

	// Call is the harness-native dispatch expression, or "" when this harness cannot dispatch
	// one of these agents in-session at all. An empty Call is a real answer, not a gap in this
	// table: see Note.
	Call string

	// Note is the evidence for the row, and for an empty Call the reason it is empty. Never
	// blank — a row nobody can check is a row nobody can correct.
	Note string
}

// harnessDispatches is the table. Ordered by how well established each row is, which is also
// roughly the order they were measured in.
var harnessDispatches = []HarnessDispatch{
	{
		Harness:         "cc",
		AgentNameFormat: "step-%s",
		AgentIDFormat:   "polyforge:step-%s",
		Call:            `Agent(subagent_type=<id>, prompt=<§0b>)`,
		Note: "Claude Code. The namespaced id is required: a bare `step-reviewer` resolves to a " +
			"same-named user/project agent when one exists, while `polyforge:step-reviewer` " +
			"always resolves to the plugin's file (measured, aihub#555, Claude Code 2.1.258).",
	},
	{
		Harness:         "pi",
		AgentNameFormat: "pf-%s",
		AgentIDFormat:   "pf-%s",
		Call:            `subagent(agent=<id>, task=<§0b>)`,
		Note: "pi's own subagent extension, which plugins/polyforge/pi/install.sh installs from " +
			"pi's examples/extensions/subagent. Its tool is named `subagent` and its two " +
			"required parameters are `agent` and `task` (index.ts's SubagentParams); `agents.ts` " +
			"resolves `agent` against each file's frontmatter `name:`, which render_pi.go " +
			"writes as pf-<role>. NOTE all three of Claude Code's spellings differ here: the " +
			"tool name, both argument names, and the agent id.",
	},
	{
		Harness:         "opencode",
		AgentNameFormat: "step-%s",
		AgentIDFormat:   "step-%s",
		Call:            `task(subagent_type=<id>, prompt=<§0b>, description=<3-5 words>)`,
		Note: "opencode's `task` tool (packages/opencode/src/tool/task.ts). Its argument is " +
			"spelled `subagent_type` exactly as Claude Code's is, which makes this the row most " +
			"likely to be got half-right: the TOOL is `task` not `Agent`, `description` is " +
			"REQUIRED, and the id is the bare `step-<role>` — `Agent.get(\"polyforge:step-x\")` " +
			"throws `Unknown agent type`. render_opencode.go deliberately emits no `name:`, so " +
			"the generated file's stem is the id.",
	},
	{
		Harness:         "codex",
		AgentNameFormat: "step-%s",
		AgentIDFormat:   "step-%s",
		Call:            "",
		Note: "codex has a subagent tool (`spawn_agent`, feature `multi_agent`, on by default) " +
			"but it resolves `agent_type` against an `[agents]` config section that nothing in " +
			"this repo writes, so no polyforge role is addressable through it TODAY. What " +
			"`polyforge roles generate codex` does produce is $CODEX_HOME/step-<role>.config.toml, " +
			"a CONFIG PROFILE consumed by the `-p`/`--profile` PROCESS flag, not by any " +
			"in-session dispatch — codex has no agent-file auto-discovery in this version at all " +
			"(aihub#655, live-verified codex-cli 0.154.0). Until an `[agents]` entry exists, a " +
			"codex session runs the step itself rather than dispatching it.",
	},
}

// Dispatches returns the per-harness dispatch table, sorted by harness name so callers that
// iterate it (the gate in internal/cli, any future generator) get a stable order without each
// re-sorting. The slice is a copy: the rows are value types, so a caller cannot mutate the
// table through it.
func Dispatches() []HarnessDispatch {
	out := make([]HarnessDispatch, len(harnessDispatches))
	copy(out, harnessDispatches)
	sort.Slice(out, func(i, j int) bool { return out[i].Harness < out[j].Harness })
	return out
}

// DispatchFor returns one harness's row. ok is false for a harness this table does not cover —
// which a caller must treat as "this harness's dispatch is undefined", never as a zero row,
// because a zero row's AgentIDFormat formats every role to the same empty-ish string.
func DispatchFor(harness string) (HarnessDispatch, bool) {
	for _, d := range harnessDispatches {
		if d.Harness == harness {
			return d, true
		}
	}
	return HarnessDispatch{}, false
}

// DispatchHarnesses lists the harnesses this table covers, sorted.
//
// Deliberately NOT the same list as SupportedHarnesses in compile.go, and the difference is
// real rather than an oversight: SupportedHarnesses is whose CAPABILITY (read_only) this
// package can compile, and opencode is excluded there because its read-only expression is a
// different shape handled directly in render_opencode.go. Whose AGENTS this package renders is
// all four. Merging the two lists would either claim a capability compilation that does not
// exist or drop a harness whose agent files this package writes today.
func DispatchHarnesses() []string {
	out := make([]string, 0, len(harnessDispatches))
	for _, d := range harnessDispatches {
		out = append(out, d.Harness)
	}
	sort.Strings(out)
	return out
}

// AgentNameFor is the agent's own name for a role under a harness — also the stem of the file
// each renderer writes. Every renderer in this package calls it, so this is the single copy.
func AgentNameFor(harness, role string) (string, error) {
	d, ok := DispatchFor(harness)
	if !ok {
		return "", fmt.Errorf("roles: no dispatch row for harness %q (have %v)", harness, DispatchHarnesses())
	}
	return fmt.Sprintf(d.AgentNameFormat, role), nil
}

// AgentIDFor is what a dispatch call under this harness must NAME. For cc that is the
// plugin-namespaced id, which is not the agent's own name; for the other three it is.
func AgentIDFor(harness, role string) (string, error) {
	d, ok := DispatchFor(harness)
	if !ok {
		return "", fmt.Errorf("roles: no dispatch row for harness %q (have %v)", harness, DispatchHarnesses())
	}
	return fmt.Sprintf(d.AgentIDFormat, role), nil
}

// mustAgentName is AgentNameFor for the renderers in this package, which pass a harness literal
// this file declares two screens up. A miss there is a programming error in this package, not a
// caller's bad input, and returning an error the renderer would only wrap loses that
// distinction — every renderer already returns an error, and threading a second impossible one
// through makes the possible ones harder to read.
func mustAgentName(harness, role string) string {
	name, err := AgentNameFor(harness, role)
	if err != nil {
		panic(err)
	}
	return name
}
