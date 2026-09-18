package config

import (
	"crypto/rand"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/pelletier/go-toml/v2"
)

// MachineConfig is loaded from ~/.polyforge/config.toml.
// It holds machine-level identity and auth credentials.
// Per design §9.5.3.
type MachineConfig struct {
	// MachineID is a stable UUID v4 for this machine.
	// Auto-generated on first pf init and never changes.
	MachineID string `toml:"machine_id"`

	Auth MachineAuth `toml:"auth"`

	// Server overrides .polyforge.yaml aihub.url (optional).
	Server *MachineServer `toml:"server,omitempty"`

	// Binary controls the polyforge CLI binary update channel (optional).
	Binary *MachineBinary `toml:"binary,omitempty"`

	// Roles configures per-tier candidate model lists consumed by
	// `polyforge roles generate` for the pi, codex and opencode harnesses
	// (aihub#642 design decision #5; opencode added by aihub#653) AND, since
	// aihub#681, by Claude Code.
	//
	// The four are not read the same way, and the difference is the reason this
	// table lives in machine config at all. pi, codex and opencode carry
	// machine-local concrete model IDs -- for pi and opencode a
	// "<provider>/<model>" pair whose provider half the machine's owner named
	// themselves -- which cannot be committed to a repo. Claude Code's aliases
	// CAN be, and still are: internal/roles/definitions/cc_aliases.yaml remains
	// the committed default and the thing the staleness gate diffs against. A
	// `harness = "cc"` candidate here OVERRIDES that default for its tier, on
	// this machine only, by having internal/cli.GenerateCCAgents regenerate the
	// plugin's step-<role>.md files at MCP-server startup. A machine that names
	// no cc candidate is byte-for-byte unaffected.
	//
	// Optional; a machine that never sets it gets each harness's built-in
	// defaults, which for Claude Code means the committed cc_aliases.yaml table.
	//
	// This table decides MODELS only. It cannot redefine a role: there is no
	// user-override layer over internal/roles/definitions (see
	// UnreadRolesOverrideDir).
	Roles *MachineRoles `toml:"roles,omitempty"`

	// Models is the machine-local [[models]] catalog (aihub#708): named
	// {harness, model, effort} routes plus the uses each entry may serve.
	// It is the NEW path's model source — steps of the new WI workflow
	// persist the harness-native identity resolved deterministically from
	// these entries (internal/modelruntime), while [roles]/[roles.presets]
	// above stay the LEGACY path's tier table and are read only by the legacy
	// role/agent generators and drain. The two are not merged and neither
	// silently overrides the other (spec D9: conflicting pinning must be
	// explicit).
	//
	// Optional: a machine that never sets it has no new-path routes, and every
	// new-path dispatch preflight refuses with "candidate not in the local
	// catalog" rather than inventing a route. Strict validation lives in
	// ValidateModels; effort here is the uniform public vocabulary
	// (config.ModelEfforts), never a harness-native-only token such as
	// "xhigh"/"max"/"minimal"/"off" — those are refused so no entry can claim
	// a level the uniform adapter cannot express on all four harnesses.
	Models []MachineModel `toml:"models,omitempty"`
}

// MachineRoles is the `[roles]` table in ~/.polyforge/config.toml.
// See MachineConfig.Roles.
type MachineRoles struct {
	// Tiers maps a tier name (one of roles.ValidTiers: "lowest", "low",
	// "default", "raised") to an ordered list of candidates. `polyforge roles
	// generate` walks the list in order and uses the first candidate whose
	// model resolves in that harness's local model catalog; if none resolve,
	// the generated agent file omits the model field (so it inherits the
	// caller's model) and generation prints a loud, non-suppressible warning
	// naming the tier and harness rather than guessing an ID (aihub#642 AC7).
	//
	// This is the preset-LESS table: the one in effect when no preset is
	// selected. It remains fully supported; presets are additive.
	Tiers map[string][]RoleCandidate `toml:"tiers,omitempty"`

	// Preset names this machine's active preset, one of Presets' keys. Empty
	// means "use Tiers". It is the machine-level default that every entry
	// point honours, so that `polyforge roles generate`, the serve-startup
	// codex profile generation and `polyforge drain` cannot disagree about
	// which models this machine uses. See ResolveTiers.
	Preset string `toml:"preset,omitempty"`

	// Presets holds named snapshots of the whole tier table (aihub#642
	// draft_preset_semantics, confirmed by the owner 2026-09-14 and recorded
	// at aihub#642 attrs.OWNER_CONFIRMATION_2026_09_14_preset_semantics):
	// "preset = 「档位 → 模型表」的命名快照（如 balanced / frugal / max），切
	// preset = 换整张表".
	//
	// SWAPPING, not merging, is the whole semantic. A preset does not layer
	// over Tiers tier-by-tier; selecting one replaces the table outright. The
	// owner's reason for narrowing it this far is on the record too:
	// oh-my-opencode's preset can name models directly because it is a single
	// harness, and polyforge is multi-harness, so a preset here may only
	// decide WHICH tier table is in force.
	Presets map[string]*RolesPreset `toml:"presets,omitempty"`
}

// RolesPreset is one `[roles.presets.<name>]` table: a complete, named
// alternative to MachineRoles.Tiers.
type RolesPreset struct {
	// Tiers has exactly the shape and meaning of MachineRoles.Tiers.
	Tiers map[string][]RoleCandidate `toml:"tiers,omitempty"`
}

// ResolveTiers returns the tier→candidate table in force and names where it came
// from, so a caller can print the provenance rather than merely act on it.
//
// 🔴 This function is the ONE place that decides which table is in force, and
// that is its entire reason to exist. `polyforge roles generate`
// (internal/cli/roles_generate.go), the serve-startup codex profile generation
// (cmd/polyforge/main.go) and `polyforge drain` (internal/cli/drain.go) all
// resolve through it. Before aihub#673 the first two reached into
// mc.Roles.Tiers directly and drain did not read the table at all, so a machine
// could already resolve different models depending on which entry point ran —
// the "同一台机器两个口径" hazard aihub#673 was filed to close. Adding a preset
// layer that only one caller honoured would have reintroduced it one level up.
//
// Precedence, highest first:
//
//  1. override — `--preset=<name>` on the command line, for one invocation
//  2. mc.Roles.Preset — `[roles] preset = "<name>"`, this machine's default
//  3. mc.Roles.Tiers — `[roles.tiers]`, the preset-less table
//
// An unknown preset name is an ERROR naming the available presets, never a
// silent fall back to Tiers. Falling back would be exactly the failure this
// function prevents: the operator believes they are running `frugal`, the
// binary quietly runs something else, and nothing in the output disagrees with
// them. A name that does not resolve is a typo the operator can fix in seconds
// once told; a silent substitution can run for weeks.
//
// A preset that exists but declares no tiers is refused for the same reason:
// handing back an empty table would silently discard [roles.tiers] as well and
// leave every role inheriting its harness's default model.
//
// A nil receiver, a nil Roles and an empty table are all legitimate: they mean
// "this machine configured nothing", and the returned table is nil, which every
// caller already handles as "no candidates, use the harness default".
func (mc *MachineConfig) ResolveTiers(override string) (map[string][]RoleCandidate, string, error) {
	var roles *MachineRoles
	if mc != nil {
		roles = mc.Roles
	}

	name, source := override, "--preset"
	if name == "" && roles != nil {
		name, source = roles.Preset, "~/.polyforge/config.toml [roles] preset"
	}
	if name == "" {
		if roles == nil || len(roles.Tiers) == 0 {
			// Phrased to read correctly where it is USED. Every caller
			// interpolates this into "tier table from %s", and the earlier
			// wording ("no tier table configured ...") produced
			// "tier table from no tier table configured ...".
			return nil, "no configured table (each harness's own default model)", nil
		}
		return roles.Tiers, "~/.polyforge/config.toml [roles.tiers]", nil
	}

	if roles == nil || len(roles.Presets) == 0 {
		return nil, "", fmt.Errorf(
			"preset %q was requested via %s but %s defines no [roles.presets.<name>] tables at all",
			name, source, MachineConfigPath())
	}
	p, ok := roles.Presets[name]
	if !ok {
		return nil, "", fmt.Errorf("unknown preset %q (requested via %s); %s defines: %s",
			name, source, MachineConfigPath(), strings.Join(sortedKeys(roles.Presets), ", "))
	}
	if p == nil || len(p.Tiers) == 0 {
		return nil, "", fmt.Errorf(
			"preset %q (requested via %s) declares no tiers; add a [roles.presets.%s.tiers] table to %s. "+
				"Refused rather than resolved to an empty table, which would silently discard "+
				"[roles.tiers] too and leave every role on its harness's default model",
			name, source, name, MachineConfigPath())
	}
	return p.Tiers, fmt.Sprintf("preset %q (via %s)", name, source), nil
}

func sortedKeys(m map[string]*RolesPreset) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// RoleCandidate is one (harness, model) pair in a tier's candidate list.
type RoleCandidate struct {
	// Harness is one of ConfigurableHarnesses: "cc", "pi", "codex" or
	// "opencode" (opencode since aihub#653; the doc here said "pi or codex"
	// until aihub#676, while `polyforge roles generate opencode` had been
	// shipping and plugins/polyforge/opencode/install.sh had been calling it;
	// "cc" added by aihub#681 on the owner's decision of 2026-09-15).
	//
	// "cc" is the one key with no `polyforge roles generate --out <dir>` verb
	// behind it: Claude Code's agent files are regenerated in place, inside the
	// installed plugin, by the MCP server at startup
	// (internal/cli.GenerateCCAgents). See that function for why the plugin's
	// own directory is the target rather than ~/.claude/agents/, which is an
	// equally legal location for a CC agent.
	//
	// Run this through ValidateCandidates before acting on it. A misspelled
	// harness is not rejected by the TOML decoder and matches no candidate, so
	// without that check it looks exactly like "your model is unavailable".
	Harness string `toml:"harness"`
	// Model is the harness-native model identifier to try.
	//
	// For cc that is a Claude Code model alias ("sonnet", "opus", "haiku",
	// "fable") or a full model id -- NOT a "<provider>/<model>" pair. It is
	// written verbatim into the agent file's `model:` frontmatter, so it must
	// also be spelled in the lowercase shape that line's readers parse
	// (internal/roles.ValidCCModel); one that is not is refused at generation
	// time with the model field omitted, never rewritten into a guess.
	//
	// 🔴 For pi and opencode this MUST carry the provider prefix
	// ("<provider>/<model>", e.g. "sub2api-anthropic/claude-fable-5"). A bare
	// model id is not a shorter spelling of the same thing: aihub#676 measured
	// pi 0.85.1 rejecting a bare id outright when several authenticated
	// providers offer it, and, when only one visibly does, resolving it to a
	// DIFFERENT channel than intended. The provider name is chosen by whoever
	// configured that harness on that machine, which is exactly why this table
	// lives here and not in the repo.
	//
	// Never emitted verbatim if it fails to resolve -- see Tiers' doc comment.
	Model string `toml:"model"`
}

// ConfigurableHarnesses are the harness keys a RoleCandidate may name.
//
// ⚠️ It is NOT the same set as `polyforge roles generate <harness>`'s accepted
// positional argument, and saying so was this var's whole doc comment until
// aihub#681. "cc" is configurable but has no generate verb: its agent files are
// regenerated in place inside the installed plugin by the MCP server at startup
// (internal/cli.GenerateCCAgents), not by a CLI call pointed at an --out
// directory of the caller's choosing. Anything that needs "what can `roles
// generate` take" must not read this list; internal/cli owns that answer.
var ConfigurableHarnesses = []string{"cc", "codex", "opencode", "pi"}

// ValidateCandidates reports human-readable problems with a resolved tier
// table, in a stable order. An empty result means "nothing to say"; it is a
// WARNING source, not a hard gate, because a machine may legitimately carry a
// candidate for a harness this binary does not know yet.
//
// It exists because a harness string had NO validation anywhere in the repo
// (aihub#676 finding 6). `harness = "claude"`, or a trailing space in
// `"opencode "`, silently matched nothing forever and surfaced only as the
// generic "no resolvable <harness> model candidate" warning -- which sends the
// operator to check the MODEL, the one part of the line that was fine.
func ValidateCandidates(tiers map[string][]RoleCandidate) []string {
	known := make(map[string]bool, len(ConfigurableHarnesses))
	for _, h := range ConfigurableHarnesses {
		known[h] = true
	}

	names := make([]string, 0, len(tiers))
	for tier := range tiers {
		names = append(names, tier)
	}
	sort.Strings(names)

	var problems []string
	for _, tier := range names {
		for i, c := range tiers[tier] {
			switch {
			case c.Harness == "":
				problems = append(problems, fmt.Sprintf(
					"tier %q candidate %d declares no harness; it can never match. Known harnesses: %s",
					tier, i, strings.Join(ConfigurableHarnesses, ", ")))
			case !known[c.Harness]:
				hint := ""
				if c.Harness != strings.TrimSpace(c.Harness) {
					hint = " (note the leading/trailing whitespace)"
				} else if c.Harness == "claude" || c.Harness == "claude-code" {
					// Claude Code IS configurable here since aihub#681 -- this
					// hint used to say the opposite, and kept saying it for as
					// long as the feature did not exist. What is wrong now is
					// only the SPELLING, so that is all this says.
					hint = " (Claude Code's key is spelled \"cc\"; it is configurable, and a cc " +
						"candidate overrides internal/roles/definitions/cc_aliases.yaml for that " +
						"tier on this machine)"
				}
				problems = append(problems, fmt.Sprintf(
					"tier %q candidate %d names unknown harness %q%s; it can never match. Known harnesses: %s",
					tier, i, c.Harness, hint, strings.Join(ConfigurableHarnesses, ", ")))
			}
			if c.Model == "" {
				problems = append(problems, fmt.Sprintf(
					"tier %q candidate %d (harness %q) declares no model", tier, i, c.Harness))
				continue
			}
			if (c.Harness == "pi" || c.Harness == "opencode") && !strings.Contains(c.Model, "/") {
				problems = append(problems, fmt.Sprintf(
					"tier %q candidate %d declares the BARE %s model id %q. Write \"<provider>/%s\": "+
						"%s decides ambiguity against its whole built-in catalog, so a bare id can be "+
						"refused outright even when it looks unique locally, and generation will not "+
						"write one (aihub#676, measured on pi 0.85.1)",
					tier, i, c.Harness, c.Model, c.Model, c.Harness))
			}
		}
	}
	return problems
}

type MachineAuth struct {
	// APIKey is the raw key (use one of APIKey or APIKeyEnv, not both).
	APIKey string `toml:"api_key,omitempty"`
	// APIKeyEnv is an env-var name that holds the key.
	// Matches the .polyforge.yaml api_key_env convention.
	APIKeyEnv string `toml:"api_key_env,omitempty"`
}

type MachineServer struct {
	URL string `toml:"url"`
}

// MachineBinary controls the polyforge binary update channel.
// Set in ~/.polyforge/config.toml under [binary].
type MachineBinary struct {
	// Channel selects the bins-<channel> branch to download the binary from.
	// "dev" is the only published channel, and the default. See
	// ResolveBinaryChannel and plugins/polyforge/bin/polyforge-mcp.sh.
	Channel string `toml:"channel,omitempty"`
}

// MachineConfigDir returns ~/.polyforge.
func MachineConfigDir() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".polyforge")
}

// MachineConfigPath returns ~/.polyforge/config.toml.
func MachineConfigPath() string {
	return filepath.Join(MachineConfigDir(), "config.toml")
}

// RolesOverrideDir returns ~/.polyforge/roles -- a path polyforge does NOT
// read. See UnreadRolesOverrideDir.
func RolesOverrideDir() string {
	return filepath.Join(MachineConfigDir(), "roles")
}

// UnreadRolesOverrideDir reports whether ~/.polyforge/roles exists and holds
// anything, so a caller can say out loud that it is being ignored.
//
// 🔴 WITHDRAWN, not merely unimplemented. aihub#642's design
// (design_notes_addendum_2026_09_13.roles_source_location) ended with "用户自定义
// 走 ~/.polyforge/roles/ 覆盖" -- a user-override layer over the role
// definitions. aihub#676 measured that nothing in the repo had ever read that
// path (`git grep -c '\.polyforge/roles'` = 0, against 13 hits in 5 Go files
// for the sibling cc_aliases as a negative control), and the promise is not
// implementable as stated, for a reason that is structural rather than a
// missing afternoon of work:
//
//   - Claude Code's agent files are generated at BUILD time and committed
//     (aihub#642 two_generation_times: a CC plugin agent must live inside the
//     plugin directory to keep its namespaced id, and the plugin directory is a
//     distribution artifact). A user override could therefore never reach CC in
//     a released plugin -- it would silently apply to three harnesses of four,
//     which is a worse contract than having none.
//   - internal/roles.LoadRoles is the single loader behind BOTH the build-time
//     CC generator and internal/roles' cc_staleness_gate, which byte-diffs the
//     regenerated files against the committed ones. Teaching THAT loader to read
//     an override would turn `go test ./internal/roles/` red on any machine that
//     had one, and make `go generate` capable of committing a user's private
//     prompt into the repo.
//
// ⚠️ Only the first bullet is structural. The second argues against one
// implementation, not against the feature: a second loader, used only by the
// three install-time harnesses, would leave the gate alone -- at the cost of two
// loaders that can disagree, which is the "同一台机器两个口径" hazard aihub#673
// exists to prevent. That is a trade to put to the owner, not a fact.
//
// 🟢 THE OWNER ANSWERED THAT TRADE ON 2026-09-15 (aihub#681), FOR MODELS ONLY.
// Claude Code's tier->model mapping is now machine-configurable: a `harness =
// "cc"` candidate in [roles.tiers] makes the MCP server regenerate the plugin's
// step-<role>.md files at startup, in place. That REFUTES the first bullet as
// stated -- a machine-local value demonstrably can reach CC in a released
// plugin -- and it leaves the second bullet standing untouched, because the
// regeneration renders the SAME embedded role definitions and only substitutes
// the model.
//
// ⚠️ Read the first bullet's "must live inside the plugin directory" for
// EXACTLY what it says: a sufficient condition for keeping the namespaced id,
// not a claim that no other directory is legal. ~/.claude/agents/ is a
// perfectly legal location for a CC agent, and aihub#681's own wi body
// initially mis-derived infeasibility from this sentence by reading it as a
// necessity. internal/cli.GenerateCCAgents states the actual reason the plugin
// directory is the target.
//
// So what is still withdrawn is narrower than this comment used to describe: an
// override of the role DEFINITIONS (prompt, description, capability, step ids),
// which is the part that would need the second loader and would put a user's
// private prompt within reach of `go generate`. Reopening THAT still needs an
// owner decision, not a patch. Until then the honest behaviour is the one
// implemented here: the directory is never read, and the operator is TOLD it is
// never read rather than watching their edits have no effect.
func UnreadRolesOverrideDir() (path string, present bool) {
	path = RolesOverrideDir()
	entries, err := os.ReadDir(path)
	if err != nil {
		return path, false
	}
	return path, len(entries) > 0
}

// RolesOverrideIgnoredWarning is the exact notice every role-generating entry
// point prints when UnreadRolesOverrideDir reports a populated directory. One
// string, so the entry points cannot describe the same non-feature differently.
func RolesOverrideIgnoredWarning(path string) string {
	return fmt.Sprintf(
		"polyforge: WARNING: %s exists and is NOT read. A user-override layer over the role "+
			"DEFINITIONS (prompt, description, capability, step ids) was described in aihub#642's "+
			"design but was never implemented: the single loader it would have to hook is the same "+
			"one the cc_staleness_gate diffs against the repo, so hooking it would turn "+
			"`go test ./internal/roles/` red on any machine that had one. Role definitions come "+
			"only from internal/roles/definitions/*.yaml. The MODELS they resolve to are what %s "+
			"configures -- for all four harnesses, Claude Code included since aihub#681, via "+
			"[roles.tiers].\n",
		path, MachineConfigPath())
}

// LoadMachineConfig reads ~/.polyforge/config.toml.
// Returns an empty (but valid) config if the file does not exist.
func LoadMachineConfig() (*MachineConfig, error) {
	path := MachineConfigPath()
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return &MachineConfig{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	var mc MachineConfig
	if err := toml.Unmarshal(b, &mc); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	return &mc, nil
}

// SaveMachineConfig writes mc to ~/.polyforge/config.toml (mode 0600).
func SaveMachineConfig(mc *MachineConfig) error {
	if err := os.MkdirAll(MachineConfigDir(), 0700); err != nil {
		return fmt.Errorf("mkdir %s: %w", MachineConfigDir(), err)
	}
	b, err := toml.Marshal(mc)
	if err != nil {
		return fmt.Errorf("marshal config.toml: %w", err)
	}
	header := "# polyforge machine config — auto-generated by pf init\n" +
		"# machine_id is stable; do not edit it manually.\n\n"
	footer := "\n# The team's aihub endpoint is compiled into the polyforge binary, so this\n" +
		"# file does not need a [server] url and you should not add one just to write\n" +
		"# the address down: a copy here cannot be corrected by a release, which is how\n" +
		"# machines ended up pointing at a retired host (aihub#335). Run\n" +
		"# `polyforge doctor` to see the endpoint in use. Set this ONLY to point at a\n" +
		"# different aihub:\n" +
		"# [server]\n" +
		"# url = \"http://your-own-aihub:8080\"\n" +
		"\n# Binary update channel (optional; \"dev\" is the only published channel\n" +
		"# and the default, so you normally leave this out entirely):\n" +
		"# [binary]\n" +
		"# channel = \"dev\"\n" +
		"\n# Per-tier candidate models, for all four harnesses: pi, codex and opencode\n" +
		"# via `polyforge roles generate`, and Claude Code (harness = \"cc\") via the\n" +
		"# agent files the MCP server regenerates at startup (aihub#681). Optional;\n" +
		"# omit entirely to accept each harness's defaults -- for Claude Code that is\n" +
		"# the repo-committed alias table, which stays in force for every tier you do\n" +
		"# not give a cc candidate. Each tier lists candidates in priority order, and\n" +
		"# generation uses the first one that resolves in that harness's local model\n" +
		"# catalog.\n" +
		"#\n" +
		"# A cc candidate takes a Claude Code alias (sonnet/opus/haiku/fable) or a\n" +
		"# full model id -- NOT a \"<provider>/<model>\" pair -- and it takes effect in\n" +
		"# the NEXT Claude Code session: the files are rewritten while this one is\n" +
		"# already starting, and plugin agents are not re-read afterwards.\n" +
		"#\n" +
		"# WRITE THE FULL \"<provider>/<model>\" FOR pi AND opencode, never the bare\n" +
		"# model id. The bare form is not a shorter spelling of the same thing:\n" +
		"# aihub#676 measured pi 0.85.1 refusing `claude-fable-5` outright with\n" +
		"# \"ambiguous across providers\", and naming providers that are not even in\n" +
		"# `pi --list-models` -- pi decides ambiguity against its whole built-in\n" +
		"# catalog, so a bare id can be refused even when it looks unique here.\n" +
		"# Generation therefore never writes a bare id; it warns and omits the model.\n" +
		"# The provider name is whatever YOU called it when you set that harness up,\n" +
		"# which is why this table is here and not in the repo. Run `pi --list-models`.\n" +
		"# [roles.tiers]\n" +
		"# default = [{ harness = \"pi\", model = \"<your-provider>/claude-sonnet-4-5\" }]\n" +
		"# codex slugs carry no provider prefix; write them verbatim:\n" +
		"# raised  = [{ harness = \"codex\", model = \"gpt-6-astra\" }]\n" +
		"# Claude Code takes a bare alias or full model id:\n" +
		"# lowest  = [{ harness = \"cc\", model = \"haiku\" }]\n" +
		"#\n" +
		"# There is NO ~/.polyforge/roles/ override directory. Role definitions --\n" +
		"# which step runs as which role, at which tier, read-only or not -- come\n" +
		"# only from the polyforge binary's embedded internal/roles/definitions.\n" +
		"# This table decides only which MODEL each tier resolves to (aihub#676).\n" +
		"\n# Presets are NAMED SNAPSHOTS of that whole table. Selecting one SWAPS the\n" +
		"# entire table -- it does not merge tier-by-tier with [roles.tiers] above.\n" +
		"# `polyforge roles generate`, the serve-startup codex profile and Claude Code\n" +
		"# agent generation, and `polyforge drain` all honour the same selection, so\n" +
		"# this machine cannot end up resolving different models depending on which\n" +
		"# one you ran:\n" +
		"# [roles]\n" +
		"# preset = \"frugal\"           # this machine's active preset\n" +
		"# [roles.presets.frugal.tiers]\n" +
		"# default = [{ harness = \"pi\", model = \"<your-provider>/claude-haiku-4-5\" }]\n" +
		"# raised  = [{ harness = \"pi\", model = \"<your-provider>/claude-sonnet-4-5\" }]\n" +
		"# [roles.presets.max.tiers]\n" +
		"# default = [{ harness = \"pi\", model = \"<your-provider>/claude-opus-4-6\" }]\n" +
		"# Override for one run with `polyforge drain --preset=<name>` or\n" +
		"# `polyforge roles generate <harness> --out <dir> --preset=<name>`.\n"
	return os.WriteFile(MachineConfigPath(), append(append([]byte(header), b...), []byte(footer)...), 0600)
}

// EnsureMachineConfig loads config.toml; if it doesn't exist or has no
// machine_id, generates a fresh UUID and saves.
func EnsureMachineConfig() (*MachineConfig, error) {
	mc, err := LoadMachineConfig()
	if err != nil {
		return nil, err
	}
	if mc.MachineID == "" {
		mc.MachineID = newUUID()
		if err := SaveMachineConfig(mc); err != nil {
			return nil, err
		}
	}
	return mc, nil
}

// ResolveAPIKey returns the API key, in priority order:
//  1. POLYFORGE_API_KEY env var (highest priority — explicit override,
//     matching the `--help` contract and CLI-tool convention)
//  2. config.toml [auth] api_key
//  3. the env var named by config.toml [auth] api_key_env
func (mc *MachineConfig) ResolveAPIKey() string {
	// Global override wins (local dev key swap, CI / container env injection).
	if v := os.Getenv("POLYFORGE_API_KEY"); v != "" {
		return v
	}
	if mc.Auth.APIKey != "" {
		return mc.Auth.APIKey
	}
	if mc.Auth.APIKeyEnv != "" {
		if v := os.Getenv(mc.Auth.APIKeyEnv); v != "" {
			return v
		}
	}
	return ""
}

// ResolveBinaryChannel returns the bins-<channel> branch to fetch the polyforge
// binary from. "dev" is the only published channel and the default.
//
// aihub#305: this used to default to "stable" and pass any configured value
// through verbatim, mirroring the launcher — but bins-stable was never
// published, so both the default and a literal channel = "stable" resolved to a
// 404. The legacy value is now normalised onto dev rather than returned as-is,
// so this cannot start disagreeing with polyforge-mcp.sh's own case statement,
// which is the actual consumer of the channel today. Keep the two in step.
func (mc *MachineConfig) ResolveBinaryChannel() string {
	if mc.Binary != nil && mc.Binary.Channel != "" && mc.Binary.Channel != "stable" {
		return mc.Binary.Channel
	}
	return "dev"
}

// ResolveAihubURL returns the aihub server URL, in priority order:
//  1. POLYFORGE_AIHUB_URL env var (highest priority — explicit override,
//     matching the `--help` contract)
//  2. config.toml [server] url
//
// Returns "" if neither is set — i.e. it reports what this MACHINE was
// configured with, not what the client will end up talking to. For the latter,
// use EffectiveAihubURL; the difference is load-bearing and is explained there.
func (mc *MachineConfig) ResolveAihubURL() string {
	if v := os.Getenv("POLYFORGE_AIHUB_URL"); v != "" {
		return v
	}
	if mc.Server != nil && mc.Server.URL != "" {
		return mc.Server.URL
	}
	return ""
}

// AihubURLDefault is the team's shared aihub endpoint, compiled into the
// polyforge binary. It is the ONE place a polyforge client reads the address
// from, and the only copy of it a change has to touch.
//
// aihub#335: this string used to be transcribed by hand. docs/onboarding.md
// step 2 held a heredoc that every newcomer pasted into
// ~/.polyforge/config.toml, and thirteen other files carried one of the two
// addresses too (24 occurrences at e8fbfcb; most are historical records that
// are correct as written — see internal/cli/onboarding_address_test.go for the
// classification) — while the host it names moves. docs/deployment.md records
// the server moving from 10.146.0.16 to 10.146.0.34, and notes that the current
// host's public IP changes across stop/start. So a copy taken before a move
// points at a dead
// address, and the newcomer reads the resulting timeout as a bad API key
// (aihub#331 was that exact debugging session). A document cannot be corrected
// on the machines that already copied it; a constant in the binary can be, and
// is — the plugin launcher refreshes the binary from bins-dev daily, so a change
// here reaches every machine within a day of landing on main without anyone
// editing a config file.
//
// Both higher-priority layers still win, so running your own aihub needs no
// change to this constant (see EffectiveAihubURL):
//
//	POLYFORGE_AIHUB_URL=http://host:8080   one-shot, CI, containers
//	[server] url = "http://host:8080"      in ~/.polyforge/config.toml
//
// docs/deployment.md's "Current production" table names the same host for an
// unrelated reason — which VM an operator SSHes into to run a deploy — and is
// read by no client. Do not turn this constant into a pointer at that table, or
// that table into a pointer at this constant: they answer different questions
// and only happen to agree today.
const AihubURLDefault = "http://10.146.0.34:8080"

// EffectiveAihubURL resolves the endpoint a polyforge client actually talks to,
// and names the layer that supplied it so `polyforge doctor` can print both.
// Priority:
//
//  1. POLYFORGE_AIHUB_URL
//  2. ~/.polyforge/config.toml [server] url
//  3. workspaceURL — .polyforge.yaml aihub.url, passed in by the caller
//  4. AihubURLDefault
//
// It never returns "". Before aihub#335 an unconfigured machine got an empty
// base URL: the MCP server started anyway and every request failed on a URL
// with no host, and `polyforge init` reached client.WhoAmI on a nil client.
//
// Deliberately NOT folded into ResolveAihubURL, which still reports "" when
// this machine configured nothing. That distinction has a consumer:
// writePolyforgeYAML copies ResolveAihubURL's result into .polyforge.yaml, and
// writing the built-in default there would pin that workspace to today's
// address in a file no release can update — reintroducing the stale-copy defect
// this constant exists to remove, one copy per workspace instead of per
// document. Callers that want an endpoint to CONNECT to want this function;
// callers that PERSIST what they were told want ResolveAihubURL.
func EffectiveAihubURL(mc *MachineConfig, workspaceURL string) (url, source string) {
	if v := os.Getenv("POLYFORGE_AIHUB_URL"); v != "" {
		return v, "POLYFORGE_AIHUB_URL"
	}
	if mc != nil && mc.Server != nil && mc.Server.URL != "" {
		return mc.Server.URL, "~/.polyforge/config.toml [server] url"
	}
	if workspaceURL != "" {
		return workspaceURL, ".polyforge.yaml aihub.url"
	}
	return AihubURLDefault, "built-in default"
}

func newUUID() string {
	var b [16]byte
	rand.Read(b[:]) //nolint:errcheck
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x",
		b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}
