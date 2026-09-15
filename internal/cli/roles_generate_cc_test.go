package cli

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/GMISWE/ieops-aihub/internal/config"
	"github.com/GMISWE/ieops-aihub/internal/roles"
)

// aihub#681 — Claude Code's tier→model mapping is machine-configurable, and the
// machine that configures NOTHING must not be able to tell.
//
// Every test below is paired with a mutant that was run and seen to turn it red;
// the mutants are named in each test's comment so a later reader can re-run them
// rather than trust this sentence. The one that matters most is
// TestGenerateCCAgents_NoCCCandidateWritesNothing: the natural implementation of
// this feature regenerates unconditionally, which is INVISIBLE on a machine with
// no configuration (the render is identical) until a release changes the
// committed default or someone diffs mtimes.

// ccFixtureDir stages a throwaway copy of the five committed agent files, so a
// test can assert against "what this machine shipped with" without touching the
// repo's own plugins/polyforge/agents.
func ccFixtureDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	roleList, err := roles.LoadRoles()
	if err != nil {
		t.Fatalf("LoadRoles: %v", err)
	}
	aliases, err := roles.LoadCCAliases()
	if err != nil {
		t.Fatalf("LoadCCAliases: %v", err)
	}
	rendered, err := roles.RenderCCAgentFiles(roleList, aliases)
	if err != nil {
		t.Fatalf("RenderCCAgentFiles: %v", err)
	}
	if len(rendered) != 5 {
		t.Fatalf("staged %d agent files, want 5", len(rendered))
	}
	for name, body := range rendered {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatalf("stage %s: %v", name, err)
		}
	}
	return dir
}

// snapshotDir records every file's content and modification time.
func snapshotDir(t *testing.T, dir string) map[string]struct {
	body string
	mod  time.Time
} {
	t.Helper()
	out := map[string]struct {
		body string
		mod  time.Time
	}{}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read %s: %v", dir, err)
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		fi, err := e.Info()
		if err != nil {
			t.Fatalf("stat %s: %v", e.Name(), err)
		}
		body, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			t.Fatalf("read %s: %v", e.Name(), err)
		}
		out[e.Name()] = struct {
			body string
			mod  time.Time
		}{string(body), fi.ModTime()}
	}
	return out
}

// agentModel extracts a staged agent file's `model:` frontmatter value, or ""
// when the file declares none. It parses with the SAME pattern the two
// production readers of that line use (pf-skill-router's AGENT_MODEL_RE and
// engine_native_dispatch_model_test.go's agentModelRe), so "this test can see a
// model" means "the hook and the dispatch gate can see it too".
func agentModel(t *testing.T, dir, name string) string {
	t.Helper()
	body, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	fm := regexp.MustCompile(`(?s)\A---\n(.*?)\n---`).FindStringSubmatch(string(body))
	if fm == nil {
		t.Fatalf("%s has no frontmatter block:\n%s", name, body)
	}
	m := regexp.MustCompile(`(?m)^model:[ \t]*([a-z][a-z0-9.-]*)[ \t]*$`).FindStringSubmatch(fm[1])
	if m == nil {
		return ""
	}
	return m[1]
}

func mcWithTiers(tiers map[string][]config.RoleCandidate) *config.MachineConfig {
	return &config.MachineConfig{Roles: &config.MachineRoles{Tiers: tiers}}
}

// TestGenerateCCAgents_NoCCCandidateWritesNothing is aihub#681 AC1, and it is
// the load-bearing one.
//
// It covers three machines that must all be byte-identical to today: no config
// at all, an empty table, and a fully populated table that simply never names
// cc. For each: nothing written, no mtime moved, wrote=false.
//
// MUTANT (run, red): delete the `if !hasCCCandidate(tiers) { return false, nil }`
// guard in GenerateCCAgents so it regenerates unconditionally. The content
// assertions stay GREEN (the render is identical when no tier overrides
// anything) — only the mtime and wrote=false assertions catch it, which is
// exactly why they are here and why a content-only test would have shipped the
// bug.
func TestGenerateCCAgents_NoCCCandidateWritesNothing(t *testing.T) {
	cases := []struct {
		name string
		mc   *config.MachineConfig
	}{
		{"no [roles] table at all", &config.MachineConfig{}},
		{"an empty tier table", mcWithTiers(map[string][]config.RoleCandidate{})},
		{
			"a populated table that never names cc",
			mcWithTiers(map[string][]config.RoleCandidate{
				"default": {{Harness: "pi", Model: "sub2api-anthropic/claude-sonnet-4-5"}},
				"raised":  {{Harness: "codex", Model: "gpt-6-astra"}},
			}),
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := ccFixtureDir(t)
			before := snapshotDir(t, dir)

			// Coarse filesystem timestamps would make an mtime comparison
			// vacuous: a rewrite within the same tick looks unchanged. Backdate
			// the staged files so any rewrite MUST move the mtime forward.
			old := time.Now().Add(-2 * time.Hour)
			for name := range before {
				if err := os.Chtimes(filepath.Join(dir, name), old, old); err != nil {
					t.Fatalf("chtimes %s: %v", name, err)
				}
			}
			before = snapshotDir(t, dir)

			var wrote bool
			var err error
			stderr := captureStderr(t, func() { wrote, err = GenerateCCAgents(tc.mc, dir) })
			if err != nil {
				t.Fatalf("GenerateCCAgents: %v", err)
			}
			if wrote {
				t.Errorf("wrote=true on a machine with no cc candidate; it must not regenerate at all")
			}
			after := snapshotDir(t, dir)
			if len(after) != len(before) {
				t.Errorf("file count changed: %d -> %d", len(before), len(after))
			}
			for name, was := range before {
				is, ok := after[name]
				if !ok {
					t.Errorf("%s disappeared", name)
					continue
				}
				if is.body != was.body {
					t.Errorf("%s content changed on a machine with no cc candidate", name)
				}
				if !is.mod.Equal(was.mod) {
					t.Errorf("%s was REWRITTEN (mtime %s -> %s). Identical bytes are not enough: "+
						"a machine that configured nothing must not have these files touched.",
						name, was.mod, is.mod)
				}
			}
			if strings.Contains(stderr, "regenerating") {
				t.Errorf("announced a regeneration that must not have happened:\n%s", stderr)
			}
		})
	}
}

// TestGenerateCCAgents_NoTierTableIsEntirelySilent pins the narrower promise for
// the zero-configuration machine: not merely "writes nothing" but "says
// nothing". Every Claude Code session on every machine boots this code path, so
// a line here is a line on everyone's MCP log forever.
//
// MUTANT (run, red): move the ValidateCandidates/UnreadRolesOverrideDir block
// above the `len(tiers) == 0` early return.
func TestGenerateCCAgents_NoTierTableIsEntirelySilent(t *testing.T) {
	dir := ccFixtureDir(t)
	stderr := captureStderr(t, func() {
		if _, err := GenerateCCAgents(&config.MachineConfig{}, dir); err != nil {
			t.Fatalf("GenerateCCAgents: %v", err)
		}
	})
	if stderr != "" {
		t.Errorf("a machine with no [roles] table got output on stderr:\n%s", stderr)
	}
}

// TestGenerateCCAgents_CCCandidateOverridesOnlyItsTier is aihub#681 AC2.
//
// Configuring the `raised` tier must move step-reviewer.md (the raised role)
// from the committed "opus" to "sonnet" AND leave the other four roles on their
// cc_aliases.yaml defaults — the "keep the defaults" half of the design, which a
// replace-the-whole-table implementation would silently lose.
//
// MUTANTS (both run, both red):
//  1. make the cc branch render from cc_aliases regardless of the machine table
//     (pass `aliases[r.Tier]` instead of the resolved model) -> reviewer stays
//     "opus", first assertion red;
//  2. drop the `default:` arm's cc_aliases fallback so an unmentioned tier
//     resolves to "" -> executor/operator/explorer/designer lose their model
//     lines, second assertion red.
func TestGenerateCCAgents_CCCandidateOverridesOnlyItsTier(t *testing.T) {
	dir := ccFixtureDir(t)
	if got := agentModel(t, dir, "step-reviewer.md"); got != "opus" {
		t.Fatalf("fixture precondition: step-reviewer.md model = %q, want the committed %q", got, "opus")
	}

	mc := mcWithTiers(map[string][]config.RoleCandidate{
		"raised": {{Harness: "cc", Model: "sonnet"}},
	})
	wrote, err := GenerateCCAgents(mc, dir)
	if err != nil {
		t.Fatalf("GenerateCCAgents: %v", err)
	}
	if !wrote {
		t.Fatal("wrote=false with a cc candidate configured; nothing was regenerated")
	}

	if got := agentModel(t, dir, "step-reviewer.md"); got != "sonnet" {
		t.Errorf("step-reviewer.md model = %q, want %q from [roles.tiers.raised]", got, "sonnet")
	}
	// The untouched tiers keep the repo default. Derived from the alias table
	// rather than hardcoded, so re-tiering cc_aliases.yaml does not silently
	// turn this test into a pin on a value nobody chose.
	aliases, err := roles.LoadCCAliases()
	if err != nil {
		t.Fatalf("LoadCCAliases: %v", err)
	}
	roleList, err := roles.LoadRoles()
	if err != nil {
		t.Fatalf("LoadRoles: %v", err)
	}
	for _, r := range roleList {
		if r.Tier == "raised" {
			continue
		}
		name := "step-" + r.Name + ".md"
		if got := agentModel(t, dir, name); got != aliases[r.Tier] {
			t.Errorf("%s (tier %q) model = %q, want the cc_aliases.yaml default %q: a tier the "+
				"machine did not configure must keep the committed default",
				name, r.Tier, got, aliases[r.Tier])
		}
	}
}

// TestGenerateCCAgents_FirstUsableCandidateWins pins ordered resolution and the
// harness filter together: a tier may legitimately list several harnesses'
// candidates, and only the cc ones may be considered, in declaration order.
//
// 🔴 THE CODEX CANDIDATE IS THE WHOLE TEST, and the first version of this test
// did not have it. That version listed a pi candidate
// ("sub2api-anthropic/claude-fable-5") ahead of the cc ones, and the mutant that
// DELETES `if c.Harness != "cc" { continue }` from resolveCCModel came back
// GREEN against it. Diagnosed rather than recorded as covered: a pi identifier
// carries a "/" and so fails ValidCCModel anyway, meaning the syntax check was
// silently standing in for the harness filter and the assertion could not tell
// the two apart. A codex slug is bare and lowercase -- syntactically a PERFECTLY
// VALID CC model -- so only the harness filter can reject it.
//
// MUTANT (re-run against this version, red): same deletion -> "gpt-6-astra" is
// written into step-reviewer.md's `model:` line.
func TestGenerateCCAgents_FirstUsableCandidateWins(t *testing.T) {
	dir := ccFixtureDir(t)
	mc := mcWithTiers(map[string][]config.RoleCandidate{
		"raised": {
			{Harness: "codex", Model: "gpt-6-astra"},
			{Harness: "pi", Model: "sub2api-anthropic/claude-fable-5"},
			{Harness: "cc", Model: "fable"},
			{Harness: "cc", Model: "opus"},
		},
	})
	captureStderr(t, func() {
		if _, err := GenerateCCAgents(mc, dir); err != nil {
			t.Fatalf("GenerateCCAgents: %v", err)
		}
	})
	if got := agentModel(t, dir, "step-reviewer.md"); got != "fable" {
		t.Errorf("step-reviewer.md model = %q, want %q (first cc candidate in declaration order). "+
			"%q would mean the harness filter is gone; %q would mean declaration order is not honoured.",
			got, "fable", "gpt-6-astra", "opus")
	}
	// Precondition made explicit, so the test cannot quietly decay back into the
	// version that could not see the mutant: the decoy MUST be a valid CC model.
	if !roles.ValidCCModel("gpt-6-astra") {
		t.Fatal("the codex decoy is not a syntactically valid CC model, so this test is back to " +
			"proving only that ValidCCModel works -- pick another decoy")
	}
}

// TestGenerateCCAgents_UnwritableModelOmitsAndWarns is aihub#681 AC6 — the
// aihub#642 AC7 degradation, carried over to CC.
//
// A candidate that cannot be written into a `model:` line must produce NO model
// field, a loud warning, and above all NO substituted identifier: not the
// operator's string, not a cleaned-up version of it, and not the cc_aliases
// default (which would make a rejected config look like it had worked).
//
// MUTANTS (all run, all red): (a) write c.Model unvalidated -> the bad string
// appears in the file; (b) fall back to aliases[r.Tier] on rejection -> "opus"
// reappears and the no-silent-fallback assertion fires; (c) drop the warning
// print -> the stderr assertions fire.
func TestGenerateCCAgents_UnwritableModelOmitsAndWarns(t *testing.T) {
	for _, bad := range []string{
		"anthropic/claude-opus-4-5", // a provider-prefixed pair: the pi/opencode shape, wrong here
		"Opus",                      // capitalised: invisible to both readers of the model: line
		"claude opus",               // a space: would silently truncate or break the line
		"",                          // declared with no model at all
	} {
		t.Run(bad, func(t *testing.T) {
			dir := ccFixtureDir(t)
			mc := mcWithTiers(map[string][]config.RoleCandidate{
				"raised": {{Harness: "cc", Model: bad}},
			})
			var wrote bool
			var err error
			stderr := captureStderr(t, func() { wrote, err = GenerateCCAgents(mc, dir) })
			if err != nil {
				t.Fatalf("GenerateCCAgents: %v", err)
			}
			if !wrote {
				t.Fatal("wrote=false: a cc candidate was named, so the omit-and-warn path must still run")
			}

			if got := agentModel(t, dir, "step-reviewer.md"); got != "" {
				t.Errorf("step-reviewer.md declares model %q; an unwritable candidate must omit the "+
					"field entirely and inherit the caller's model", got)
			}
			body, readErr := os.ReadFile(filepath.Join(dir, "step-reviewer.md"))
			if readErr != nil {
				t.Fatalf("read step-reviewer.md: %v", readErr)
			}
			if bad != "" && strings.Contains(string(body), bad) {
				t.Errorf("the rejected identifier %q was written into the file anyway:\n%s", bad, body)
			}
			if strings.Contains(string(body), "model: opus") {
				t.Errorf("a rejected cc candidate silently fell back to the cc_aliases default, which " +
					"would make a broken config look like it worked")
			}
			// The prompt must say the model is absent, not keep claiming this
			// file sets one (aihub#642 AC7's observability half).
			if !strings.Contains(string(body), "NO model is declared for you") {
				t.Errorf("step-reviewer.md omits the model but its prompt does not say so:\n%s", body)
			}

			if !strings.Contains(stderr, "WARNING") || !strings.Contains(stderr, `role "reviewer"`) {
				t.Errorf("no loud warning naming the role:\n%s", stderr)
			}
			if !strings.Contains(stderr, `tier "raised"`) {
				t.Errorf("the warning does not name the tier to fix:\n%s", stderr)
			}
			if !strings.Contains(stderr, "NO model field") {
				t.Errorf("the warning does not say what the consequence is:\n%s", stderr)
			}
			// It must NOT claim a catalog was consulted: there is no CC model
			// catalog to consult, and aihub#676 is the record of what a warning
			// that names the wrong cause costs.
			if strings.Contains(stderr, "live catalog") {
				t.Errorf("the warning claims a live catalog was checked, but CC has none:\n%s", stderr)
			}
		})
	}
}

// TestGenerateCCAgents_RejectionIsPerTier is the negative control for the test
// above: a bad candidate in one tier must not degrade the other four roles. A
// generator that failed closed for the whole run would pass every assertion
// there and still be wrong.
func TestGenerateCCAgents_RejectionIsPerTier(t *testing.T) {
	dir := ccFixtureDir(t)
	mc := mcWithTiers(map[string][]config.RoleCandidate{
		"raised": {{Harness: "cc", Model: "NOT-A-VALID-ALIAS"}},
		"lowest": {{Harness: "cc", Model: "haiku"}},
	})
	captureStderr(t, func() {
		if _, err := GenerateCCAgents(mc, dir); err != nil {
			t.Fatalf("GenerateCCAgents: %v", err)
		}
	})
	if got := agentModel(t, dir, "step-reviewer.md"); got != "" {
		t.Errorf("step-reviewer.md model = %q, want omitted", got)
	}
	if got := agentModel(t, dir, "step-operator.md"); got != "haiku" {
		t.Errorf("step-operator.md (lowest) model = %q, want %q: one tier's bad candidate must not "+
			"degrade another tier", got, "haiku")
	}
	aliases, err := roles.LoadCCAliases()
	if err != nil {
		t.Fatalf("LoadCCAliases: %v", err)
	}
	if got := agentModel(t, dir, "step-executor.md"); got != aliases["default"] {
		t.Errorf("step-executor.md (default, unconfigured) model = %q, want %q", got, aliases["default"])
	}
}

// TestGenerateCCAgents_HonoursPresets pins that this path resolves through
// MachineConfig.ResolveTiers like every other reader of the table. aihub#673
// exists because two entry points once disagreed about which table was in force;
// adding a third that read mc.Roles.Tiers directly would reopen it.
//
// MUTANT (run, red): replace `mc.ResolveTiers("")` with a direct read of
// `mc.Roles.Tiers` -> the preset is ignored and reviewer resolves to the
// preset-less table's "haiku" instead of the preset's "sonnet".
func TestGenerateCCAgents_HonoursPresets(t *testing.T) {
	dir := ccFixtureDir(t)
	mc := &config.MachineConfig{Roles: &config.MachineRoles{
		Preset: "frugal",
		Tiers: map[string][]config.RoleCandidate{
			"raised": {{Harness: "cc", Model: "haiku"}},
		},
		Presets: map[string]*config.RolesPreset{
			"frugal": {Tiers: map[string][]config.RoleCandidate{
				"raised": {{Harness: "cc", Model: "sonnet"}},
			}},
		},
	}}
	captureStderr(t, func() {
		if _, err := GenerateCCAgents(mc, dir); err != nil {
			t.Fatalf("GenerateCCAgents: %v", err)
		}
	})
	if got := agentModel(t, dir, "step-reviewer.md"); got != "sonnet" {
		t.Errorf("step-reviewer.md model = %q, want %q from the selected preset, not %q from "+
			"[roles.tiers] (aihub#673: one machine, one table)", got, "sonnet", "haiku")
	}
}

// TestGenerateCCAgents_UnknownPresetDoesNotWrite pins that a preset typo
// degrades to "not regenerated, loudly" and never to "regenerated from the
// wrong table" or "half the files rewritten".
func TestGenerateCCAgents_UnknownPresetDoesNotWrite(t *testing.T) {
	dir := ccFixtureDir(t)
	before := snapshotDir(t, dir)
	mc := &config.MachineConfig{Roles: &config.MachineRoles{
		Preset: "typo",
		Tiers:  map[string][]config.RoleCandidate{"raised": {{Harness: "cc", Model: "sonnet"}}},
		Presets: map[string]*config.RolesPreset{
			"frugal": {Tiers: map[string][]config.RoleCandidate{"raised": {{Harness: "cc", Model: "haiku"}}}},
		},
	}}
	wrote, err := GenerateCCAgents(mc, dir)
	if err == nil {
		t.Fatal("an unknown preset was accepted; ResolveTiers must refuse it")
	}
	if wrote {
		t.Error("wrote=true on a refused preset")
	}
	for name, was := range snapshotDir(t, dir) {
		if was.body != before[name].body {
			t.Errorf("%s was modified despite the preset being refused", name)
		}
	}
}

// TestGenerateCCAgents_WritesAtomically proves the rename-based write, by
// asserting the property that distinguishes it from os.WriteFile: the
// destination inode is REPLACED rather than truncated in place, so a reader
// holding the old file keeps seeing a complete old file instead of a truncated
// one. Several Claude Code sessions on one machine boot this concurrently.
//
// MUTANT (run, red): swap writeFileAtomic for os.WriteFile -> the inode is
// unchanged and the already-open handle observes the new bytes mid-write.
func TestGenerateCCAgents_WritesAtomically(t *testing.T) {
	dir := ccFixtureDir(t)
	path := filepath.Join(dir, "step-reviewer.md")

	before, err := os.Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer before.Close()
	beforeStat, err := before.Stat()
	if err != nil {
		t.Fatalf("stat: %v", err)
	}

	mc := mcWithTiers(map[string][]config.RoleCandidate{
		"raised": {{Harness: "cc", Model: "sonnet"}},
	})
	if _, err := GenerateCCAgents(mc, dir); err != nil {
		t.Fatalf("GenerateCCAgents: %v", err)
	}

	afterStat, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat after: %v", err)
	}
	if os.SameFile(beforeStat, afterStat) {
		t.Error("the destination file was written in place (same inode). A concurrent reader can " +
			"then observe a truncated agent file; the write must go through a temp file + rename.")
	}

	// The handle opened before the write still sees the OLD complete content —
	// the property in-place truncation destroys.
	held := make([]byte, 64)
	n, _ := before.Read(held)
	if !strings.HasPrefix(string(held[:n]), "---\nname: step-reviewer\n") {
		t.Errorf("a handle opened before the write no longer sees a complete file: %q", string(held[:n]))
	}

	// No temp file may be left behind in a directory the harness scans.
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("readdir: %v", err)
	}
	for _, e := range entries {
		if strings.Contains(e.Name(), ".tmp-") {
			t.Errorf("left a temp file behind: %s", e.Name())
		}
	}
	if len(entries) != 5 {
		t.Errorf("directory holds %d entries, want exactly the 5 agent files", len(entries))
	}
}

// TestGenerateCCAgents_RenderedFilesStillParseForEveryConsumer is aihub#681 AC5
// from the producing side: whatever this generator writes must remain readable
// by pf-skill-router, which builds its whole tier table from four of these
// files' `model:` lines and degrades to a stub if ANY of them lacks one.
//
// The stub-degradation half (a file with no model line) is asserted directly by
// plugins/polyforge/tests/pf-skill-router.test.sh; what cannot be asserted there
// is that a MACHINE-CONFIGURED value still matches the hook's regex, which is
// this test.
func TestGenerateCCAgents_RenderedFilesStillParseForEveryConsumer(t *testing.T) {
	dir := ccFixtureDir(t)
	mc := mcWithTiers(map[string][]config.RoleCandidate{
		"lowest":  {{Harness: "cc", Model: "haiku"}},
		"low":     {{Harness: "cc", Model: "claude-haiku-4-5"}},
		"default": {{Harness: "cc", Model: "sonnet"}},
		"raised":  {{Harness: "cc", Model: "claude-opus-4-5"}},
	})
	captureStderr(t, func() {
		if _, err := GenerateCCAgents(mc, dir); err != nil {
			t.Fatalf("GenerateCCAgents: %v", err)
		}
	})

	// The exact four files and tiers pf-skill-router's _TIER_SOURCE_FILES maps.
	for file, want := range map[string]string{
		"step-operator.md": "haiku",
		"step-explorer.md": "claude-haiku-4-5",
		"step-executor.md": "sonnet",
		"step-reviewer.md": "claude-opus-4-5",
	} {
		if got := agentModel(t, dir, file); got != want {
			t.Errorf("%s model = %q, want %q — pf-skill-router reads this line to build TIERS and "+
				"falls back to a stub for ALL FOUR tiers if any one of them is unreadable",
				file, got, want)
		}
	}
}

// TestValidCCModel is the unit-level negative control for the syntax rule, and
// it also documents what the rule does NOT prove: it accepts any well-shaped
// string, including one that names no real model. There is no CC model catalog
// to check against, and pretending otherwise is what this test exists to forbid.
func TestValidCCModel(t *testing.T) {
	for _, ok := range []string{"sonnet", "opus", "haiku", "fable", "claude-sonnet-4-5", "gpt-5.2"} {
		if !roles.ValidCCModel(ok) {
			t.Errorf("ValidCCModel(%q) = false, want true", ok)
		}
	}
	for _, bad := range []string{
		"", "Sonnet", "anthropic/claude-opus-4-5", "claude opus", "sonnet[1m]", "4-5-sonnet", " sonnet",
	} {
		if roles.ValidCCModel(bad) {
			t.Errorf("ValidCCModel(%q) = true, want false", bad)
		}
	}
	// Stated as an assertion so nobody upgrades this into an existence check
	// without noticing they are changing what the function promises.
	if !roles.ValidCCModel("definitely-not-a-real-model") {
		t.Error("ValidCCModel is a SYNTAX check, not a catalog lookup; a well-shaped unknown model " +
			"must pass. If this ever fails, the doc comments calling it a syntax check are wrong.")
	}
}

// TestDefaultCCAgentsDir covers the three ways this must decline, because each
// one declines for a different reason and a single "not ok" test would not tell
// them apart.
func TestDefaultCCAgentsDir(t *testing.T) {
	t.Run("declines when CLAUDE_PLUGIN_ROOT is unset", func(t *testing.T) {
		t.Setenv("CLAUDE_PLUGIN_ROOT", "")
		if got, ok := DefaultCCAgentsDir(); ok {
			t.Errorf("ok=true with no CLAUDE_PLUGIN_ROOT, resolved %q. cwd is NOT a safe fallback: "+
				"measured 2026-09-15, a CC-launched MCP server's cwd is the SESSION's directory, "+
				"not the plugin root", got)
		}
	})

	t.Run("declines when the manifest is absent", func(t *testing.T) {
		t.Setenv("CLAUDE_PLUGIN_ROOT", t.TempDir())
		if _, ok := DefaultCCAgentsDir(); ok {
			t.Error("ok=true for a directory with no .claude-plugin/plugin.json")
		}
	})

	t.Run("resolves for a real installed plugin tree", func(t *testing.T) {
		root := t.TempDir()
		if err := os.MkdirAll(filepath.Join(root, ".claude-plugin"), 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(filepath.Join(root, ".claude-plugin", "plugin.json"), []byte(`{}`), 0o644); err != nil {
			t.Fatalf("write manifest: %v", err)
		}
		t.Setenv("CLAUDE_PLUGIN_ROOT", root)
		got, ok := DefaultCCAgentsDir()
		if !ok {
			t.Fatal("ok=false for a tree that has .claude-plugin/plugin.json")
		}
		if want := filepath.Join(root, "agents"); got != want {
			t.Errorf("= %q, want %q", got, want)
		}
	})

	t.Run("declines inside a git work tree", func(t *testing.T) {
		// A contributor's checkout, not an installed plugin. Writing the MACHINE
		// table into files the repo tracks would turn internal/roles'
		// cc_staleness_gate red on their machine with no hint why.
		repo := t.TempDir()
		root := filepath.Join(repo, "plugins", "polyforge")
		if err := os.MkdirAll(filepath.Join(root, ".claude-plugin"), 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(filepath.Join(root, ".claude-plugin", "plugin.json"), []byte(`{}`), 0o644); err != nil {
			t.Fatalf("write manifest: %v", err)
		}
		// Positive control FIRST: without .git this same tree resolves, so a
		// failure below is the git check firing and not the manifest check.
		t.Setenv("CLAUDE_PLUGIN_ROOT", root)
		if _, ok := DefaultCCAgentsDir(); !ok {
			t.Fatal("control failed: the tree does not resolve even before .git exists")
		}
		if err := os.MkdirAll(filepath.Join(repo, ".git"), 0o755); err != nil {
			t.Fatalf("mkdir .git: %v", err)
		}
		stderr := captureStderr(t, func() {
			if _, ok := DefaultCCAgentsDir(); ok {
				t.Error("ok=true inside a git work tree")
			}
		})
		if !strings.Contains(stderr, "git work tree") {
			t.Errorf("declined silently; an operator whose config is being ignored must be told:\n%s", stderr)
		}
	})
}
