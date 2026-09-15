package cli

// aihub#683 — one table of per-harness directories, one driver, and one rule for
// when a boot may write unasked.
//
// Every test below is paired with a mutant that was APPLIED, COMPILED and seen to
// turn it red; each mutant is named in its test's comment so a later reader can
// re-run it rather than trust this sentence. The load-bearing one is
// TestSyncRolesOnStartup_EmptyHomeIsNotPolluted: the natural implementation of
// "regenerate for every harness" creates ~/.pi/ and ~/.codex/ on machines that
// never installed either, and nothing about a freshly-conjured directory looks
// wrong until someone asks where it came from.

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/GMISWE/ieops-aihub/internal/config"
)

// isolateHarnessEnv points $HOME at a fresh empty directory and CLEARS every
// environment variable the table consults.
//
// 🔴 CLEARING ALL SEVEN IS THE POINT, not tidiness. Each one can redirect a row
// out of $HOME, so a machine that happens to export XDG_CONFIG_HOME would make
// "nothing was created under HOME" true while files were written somewhere else
// entirely — the assertion would stay green for the wrong reason. The list is
// also the closest thing this package has to an executable copy of the table.
func isolateHarnessEnv(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	for _, key := range []string{
		"PI_AGENT_DIR",
		"CODEX_HOME",
		"POLYFORGE_OPENCODE_AGENT_DIR",
		"OPENCODE_CONFIG_DIR",
		"XDG_CONFIG_HOME",
		"CLAUDE_PLUGIN_ROOT",
	} {
		t.Setenv(key, "")
	}
	// os.UserHomeDir reads $HOME on Unix; if that ever stops being true every
	// assertion below would be measuring the developer's real home directory.
	got, err := os.UserHomeDir()
	if err != nil || got != home {
		t.Fatalf("os.UserHomeDir() = %q, %v; want the temp dir %q. The isolation this whole "+
			"file depends on is not in force.", got, err, home)
	}
	return home
}

// everyHarnessConfigured is a tier table naming ALL FOUR harnesses, so that a
// skip can only ever be attributed to the machine's INSTALL state and never to
// "the config said nothing about it" (installTargets' gate 2).
func everyHarnessConfigured() *config.MachineConfig {
	return mcWithTiers(map[string][]config.RoleCandidate{
		"lowest":  {{Harness: "pi", Model: "prov/low"}, {Harness: "codex", Model: "codex-low"}},
		"low":     {{Harness: "opencode", Model: "prov/oc-low"}, {Harness: "cc", Model: "haiku"}},
		"default": {{Harness: "pi", Model: "prov/mid"}, {Harness: "codex", Model: "codex-mid"}},
		"raised": {{Harness: "pi", Model: "prov/high"}, {Harness: "opencode", Model: "prov/oc-high"},
			{Harness: "codex", Model: "codex-high"}, {Harness: "cc", Model: "opus"}},
	})
}

// alwaysAvailable is a probe that resolves every model, so a test asserting on
// WHICH model was written is not silently measuring a probe failure instead.
type alwaysAvailable struct{}

func (alwaysAvailable) HasModel(string) (bool, error) { return true, nil }

func stubProbes(string) CatalogProbe { return alwaysAvailable{} }

// countEntries reports how many entries a directory holds, treating "does not
// exist" as zero rather than as an error.
func countEntries(t *testing.T, dir string) int {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return 0
		}
		t.Fatalf("read %s: %v", dir, err)
	}
	return len(entries)
}

// ---------------------------------------------------------------------------
// AC3 — the one that matters most.
// ---------------------------------------------------------------------------

// TestSyncRolesOnStartup_EmptyHomeIsNotPolluted is aihub#683 AC3.
//
// A machine that has installed NONE of the four harnesses must come out of a
// serve boot byte-for-byte where it went in — not "with harmless empty
// directories", not "with default agent files nobody asked for". ZERO
// directories created is the assertion, because a weaker one ("no files written")
// stays green against a generator that mkdir -p's four trees and then finds
// nothing to put in them.
//
// The machine here CONFIGURES all four harnesses (everyHarnessConfigured), which
// is what makes the test sharp: the only thing standing between this HOME and
// four new directory trees is the install-state gate.
//
// MUTANT A (applied, compiled, red): delete the `opts.requireExistingDir &&
// !t.exists` case from installOne. Result: 3 directories created under HOME
// (.pi/agent/agents, .config/opencode/agent, .codex on a machine with codex on
// PATH), assertion fails naming them.
//
// MUTANT B (applied, compiled, red): change the gate to `t.exists ||
// opts.requireExistingDir` — i.e. keep a gate but get its polarity wrong. Same
// failure. This is the arm that proves the test reads the gate's SENSE and not
// merely its presence.
func TestSyncRolesOnStartup_EmptyHomeIsNotPolluted(t *testing.T) {
	home := isolateHarnessEnv(t)

	captureStderr(t, func() {
		syncRolesOnStartup(everyHarnessConfigured(), stubProbes)
	})

	entries, err := os.ReadDir(home)
	if err != nil {
		t.Fatalf("read HOME: %v", err)
	}
	if len(entries) != 0 {
		names := make([]string, 0, len(entries))
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Fatalf("the startup sync created %d entr(ies) in an empty HOME: %v.\n"+
			"A machine that never installed pi/codex/opencode must not acquire their "+
			"directories because polyforge booted once. The gate is "+
			"installTargets' requireExistingDir.", len(entries), names)
	}
}

// TestSyncRolesOnStartup_EmptyHomeControl is the negative control for the test
// above, and without it that green is uninterpretable.
//
// 🔴 "NOTHING WAS CREATED" IS ALSO WHAT A GENERATOR THAT CANNOT WRITE AT ALL
// PRODUCES. If LoadRoles failed, or the tier table resolved to nothing, or
// installTargets returned no reports, AC3 would pass while the feature was
// entirely dead. This arm creates ONE harness's directory and requires that the
// same call then fills it.
func TestSyncRolesOnStartup_EmptyHomeControl(t *testing.T) {
	home := isolateHarnessEnv(t)
	piDir := filepath.Join(home, ".pi", "agent", "agents")
	if err := os.MkdirAll(piDir, 0o755); err != nil {
		t.Fatalf("setup: %v", err)
	}

	captureStderr(t, func() {
		syncRolesOnStartup(everyHarnessConfigured(), stubProbes)
	})

	if n := countEntries(t, piDir); n == 0 {
		t.Fatal("the control wrote nothing into a directory that DOES exist, so the " +
			"empty-HOME test above is not evidence of a gate — it is evidence of a generator " +
			"that never runs")
	}
	// And still nothing for the harnesses whose directories were absent.
	if n := countEntries(t, filepath.Join(home, ".codex")); n != 0 {
		t.Errorf("~/.codex holds %d entr(ies); only pi's directory existed", n)
	}
	if n := countEntries(t, filepath.Join(home, ".config", "opencode", "agent")); n != 0 {
		t.Errorf("opencode's directory holds %d entr(ies); only pi's directory existed", n)
	}
}

// ---------------------------------------------------------------------------
// AC2 — the startup sync reads THIS MACHINE's table.
// ---------------------------------------------------------------------------

// TestSyncRolesOnStartup_UsesTheMachineTable is aihub#683 AC2, expressed without
// restarting anything: change a [roles.tiers] pi candidate, run the startup path,
// and the generated pi agent file's `model:` line follows.
//
// ⚠️ THE FILENAME IS NOT HARDCODED. It comes from plannedFileNames("pi"), which
// renders through internal/roles — the same source the generator writes from.
// aihub#682 is renaming pi's agent files from pf-<role> to step-<role> in
// parallel with this change, and a literal "pf-executor.md" here would be a test
// that goes red on a rename that is not a defect.
//
// MUTANT (applied, compiled, red): in generateRolesInto — NOT in installTargets —
// replace `mc.ResolveTiers(preset)` with `(&config.MachineConfig{}).ResolveTiers("")`,
// i.e. render from the DEFAULT table rather than this machine's. Result: the file
// is written with no model at all and the assertion fails on the `model:` line.
//
// 🔴 A GREEN MUTANT WAS RUN FIRST AND IS RECORDED HERE BECAUSE IT IS INSTRUCTIVE.
// The obvious mutation is the same substitution in installTargets, and it SURVIVED
// once installTargets' own gate was also disabled. The reason is a real fact about
// this code: installTargets resolves the tier table only to answer its two GATES,
// while the models that reach the file come from each generator's own
// ResolveTiers call. Recording the installTargets mutation as "AC2 covered" would
// have been proving a layer rather than the behaviour — the mutation point had to
// move to where the value actually comes from.
func TestSyncRolesOnStartup_UsesTheMachineTable(t *testing.T) {
	home := isolateHarnessEnv(t)
	piDir := filepath.Join(home, ".pi", "agent", "agents")
	if err := os.MkdirAll(piDir, 0o755); err != nil {
		t.Fatalf("setup: %v", err)
	}

	names, err := plannedFileNames("pi")
	if err != nil {
		t.Fatalf("plannedFileNames: %v", err)
	}
	if len(names) == 0 {
		t.Fatal("plannedFileNames(pi) is empty; the assertions below would vacuously pass")
	}
	probe := filepath.Join(piDir, names[0])

	const first = "some-provider/model-one"
	const second = "some-provider/model-two"

	for _, model := range []string{first, second} {
		mc := mcWithTiers(map[string][]config.RoleCandidate{
			"lowest":  {{Harness: "pi", Model: model}},
			"low":     {{Harness: "pi", Model: model}},
			"default": {{Harness: "pi", Model: model}},
			"raised":  {{Harness: "pi", Model: model}},
		})
		captureStderr(t, func() { syncRolesOnStartup(mc, stubProbes) })

		body, err := os.ReadFile(probe)
		if err != nil {
			t.Fatalf("the startup sync did not write %s for a machine that names a pi "+
				"candidate: %v", probe, err)
		}
		if !strings.Contains(string(body), model) {
			t.Fatalf("%s does not mention %q after a startup sync from a table that names it. "+
				"The sync is reading some table other than this machine's.\n--- file ---\n%s",
				probe, model, body)
		}
	}

	// The second pass is the half that proves it RE-reads rather than writes once:
	// the first model must be gone, not merely the second present.
	body, err := os.ReadFile(probe)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if strings.Contains(string(body), first) {
		t.Errorf("%s still mentions the ORIGINAL model %q after the table changed to %q. "+
			"A sync that only ever writes the first value it saw is not a sync.",
			probe, first, second)
	}
}

// TestSyncRolesOnStartup_UnconfiguredHarnessIsLeftAlone pins installTargets'
// gate 2, which is aihub#681's hasCCCandidate rule generalised.
//
// A harness whose directory exists but which this machine's table never names has
// nothing machine-local to apply. Regenerating anyway would rewrite the
// installer's files with an identical render while printing one loud AC7 warning
// PER ROLE about a harness the operator never mentioned — five lines per boot,
// per harness, forever.
//
// MUTANT (applied, compiled, red): delete the `!hasHarnessCandidate(...)` case
// from installOne. Result: opencode's directory is filled and stderr carries five
// "no candidate naming harness" warnings; both assertions fail.
func TestSyncRolesOnStartup_UnconfiguredHarnessIsLeftAlone(t *testing.T) {
	home := isolateHarnessEnv(t)
	ocDir := filepath.Join(home, ".config", "opencode", "agent")
	if err := os.MkdirAll(ocDir, 0o755); err != nil {
		t.Fatalf("setup: %v", err)
	}

	// Names pi and nothing else. opencode's directory exists; its table row does not.
	mc := mcWithTiers(map[string][]config.RoleCandidate{
		"default": {{Harness: "pi", Model: "prov/mid"}},
	})
	stderr := captureStderr(t, func() { syncRolesOnStartup(mc, stubProbes) })

	if n := countEntries(t, ocDir); n != 0 {
		t.Errorf("opencode's directory holds %d entr(ies) after a sync from a table that "+
			"never names opencode", n)
	}
	if strings.Contains(stderr, "opencode") {
		t.Errorf("the startup sync talked about opencode on a machine that configured no "+
			"opencode candidate:\n%s", stderr)
	}
}

// ---------------------------------------------------------------------------
// AC1 — --dry-run tells the truth about THIS machine.
// ---------------------------------------------------------------------------

// TestRolesInstallDryRun_ListsOnlyWhatIsInstalled is aihub#683 AC1.
//
// ⚠️ THE wi's LITERAL WORDING IS STALE and this test deliberately does not
// implement it. It says --dry-run must not list `~/.claude/agents/`; after
// aihub#681 landed, `~/.claude/agents/` is not a target polyforge has ANY row
// for — Claude Code's files live inside the installed plugin ($CLAUDE_PLUGIN_ROOT
// /agents), for the resolution-determinism reason recorded on GenerateCCAgents.
// An assertion that a string polyforge never emits is absent cannot fail, so the
// property actually pinned here is the one the wi meant: the dry run lists
// exactly the harnesses whose directory exists, and invents none.
//
// MUTANT (applied, compiled, red): delete the `opts.requireExistingDir &&
// !t.exists` case from installOne. Result: the output gains five
// `would write <home>/.config/opencode/agent/step-*.md` lines and the
// "no harness whose directory is absent may appear" assertion fails.
func TestRolesInstallDryRun_ListsOnlyWhatIsInstalled(t *testing.T) {
	home := isolateHarnessEnv(t)
	piDir := filepath.Join(home, ".pi", "agent", "agents")
	ocDir := filepath.Join(home, ".config", "opencode", "agent")
	if err := os.MkdirAll(piDir, 0o755); err != nil {
		t.Fatalf("setup: %v", err)
	}

	reports, err := installTargets(everyHarnessConfigured(), "", installOptions{
		dryRun:             true,
		requireExistingDir: true,
		probeFor:           stubProbes,
	})
	if err != nil {
		t.Fatalf("installTargets: %v", err)
	}
	var sb strings.Builder
	if failed := printInstallReports(&sb, reports, true); failed {
		t.Fatalf("a dry run reported a failure:\n%s", sb.String())
	}
	out := sb.String()

	if !strings.Contains(out, piDir) {
		t.Errorf("the dry run does not name pi's directory %q, which exists on this "+
			"machine:\n%s", piDir, out)
	}
	if strings.Contains(out, "would write "+ocDir) {
		t.Errorf("the dry run offers to write into %q, which does not exist:\n%s", ocDir, out)
	}
	if !strings.Contains(out, "opencode: skipped") {
		t.Errorf("the dry run does not say WHY opencode was left out; a silent omission is "+
			"indistinguishable from a harness the table forgot:\n%s", out)
	}
	if !strings.Contains(out, "cc: skipped") {
		t.Errorf("the dry run does not account for cc at all:\n%s", out)
	}

	// 🔴 A dry run that writes is not a dry run. HOME must still hold only the one
	// directory the test created.
	if got, want := countEntries(t, home), 1; got != want {
		t.Errorf("HOME holds %d entr(ies) after --dry-run, want %d (only the .pi tree the "+
			"test made)", got, want)
	}
	if n := countEntries(t, piDir); n != 0 {
		t.Errorf("--dry-run wrote %d file(s) into %s", n, piDir)
	}
}

// TestRolesInstallExplicitHarnessCreatesItsDirectory pins the deliberate
// asymmetry: gate 1 is an INFERENCE from what is installed, so naming a harness
// on the command line overrides it. Without this, `roles install --harness pi` on
// a machine mid-setup would refuse with the message "run roles install --harness
// pi", which is the command the operator just ran.
func TestRolesInstallExplicitHarnessCreatesItsDirectory(t *testing.T) {
	home := isolateHarnessEnv(t)
	piDir := filepath.Join(home, ".pi", "agent", "agents")

	reports, err := installTargets(everyHarnessConfigured(), "pi", installOptions{
		requireExistingDir: false, // what RunRolesInstall passes for --harness <h>
		probeFor:           stubProbes,
	})
	if err != nil {
		t.Fatalf("installTargets: %v", err)
	}
	if len(reports) != 1 || reports[0].harness != "pi" {
		t.Fatalf("--harness pi produced %d report(s), want exactly pi's: %+v", len(reports), reports)
	}
	if reports[0].err != nil {
		t.Fatalf("explicit pi install failed: %v", reports[0].err)
	}
	if n := countEntries(t, piDir); n == 0 {
		t.Fatalf("`--harness pi` did not create and fill %s", piDir)
	}
	// And it did NOT touch the harnesses it was not asked about.
	if n := countEntries(t, filepath.Join(home, ".codex")); n != 0 {
		t.Errorf("`--harness pi` also wrote into ~/.codex")
	}
}

// ---------------------------------------------------------------------------
// The table itself.
// ---------------------------------------------------------------------------

// TestResolveHarnessTargets_HonoursEveryOverride pins each row's environment
// contract against the two install scripts it has to agree with
// (plugins/polyforge/{pi,opencode}/install.sh). Those scripts and this table
// write the SAME files to the SAME directories; if they disagree, the installer
// puts them in one place and every later boot refreshes a different one, and
// nothing anywhere reports a problem.
func TestResolveHarnessTargets_HonoursEveryOverride(t *testing.T) {
	byHarness := func() map[string]harnessTarget {
		out := map[string]harnessTarget{}
		for _, tgt := range resolveHarnessTargets() {
			out[tgt.harness] = tgt
		}
		return out
	}

	t.Run("defaults", func(t *testing.T) {
		home := isolateHarnessEnv(t)
		got := byHarness()
		for _, tc := range []struct{ harness, want string }{
			// pi/install.sh:41 + :177 -> ${PI_AGENT_DIR:-$HOME/.pi/agent}/agents
			{"pi", filepath.Join(home, ".pi", "agent", "agents")},
			// opencode/install.sh:38-39, note the SINGULAR "agent"
			{"opencode", filepath.Join(home, ".config", "opencode", "agent")},
			// codex's own default $CODEX_HOME
			{"codex", filepath.Join(home, ".codex")},
		} {
			if got[tc.harness].dir != tc.want {
				t.Errorf("%s dir = %q, want %q", tc.harness, got[tc.harness].dir, tc.want)
			}
		}
		if got["cc"].unresolved == "" {
			t.Errorf("cc resolved to %q with no $CLAUDE_PLUGIN_ROOT; it must decline instead "+
				"of guessing a directory", got["cc"].dir)
		}
	})

	t.Run("pi_honours_PI_AGENT_DIR", func(t *testing.T) {
		isolateHarnessEnv(t)
		t.Setenv("PI_AGENT_DIR", "/custom/pi")
		if got := byHarness()["pi"].dir; got != filepath.Join("/custom/pi", "agents") {
			t.Errorf("pi dir = %q, want /custom/pi/agents", got)
		}
	})

	t.Run("codex_honours_CODEX_HOME", func(t *testing.T) {
		isolateHarnessEnv(t)
		t.Setenv("CODEX_HOME", "/custom/codex")
		if got := byHarness()["codex"].dir; got != "/custom/codex" {
			t.Errorf("codex dir = %q, want /custom/codex", got)
		}
	})

	// opencode's three levels, in precedence order. The middle one is opencode's
	// OWN variable and the outer one is the XDG convention; skipping either would
	// aim this at a directory opencode does not read.
	t.Run("opencode_precedence", func(t *testing.T) {
		isolateHarnessEnv(t)
		t.Setenv("XDG_CONFIG_HOME", "/xdg")
		if got := byHarness()["opencode"].dir; got != filepath.Join("/xdg", "opencode", "agent") {
			t.Errorf("with XDG only: %q, want /xdg/opencode/agent", got)
		}
		t.Setenv("OPENCODE_CONFIG_DIR", "/occfg")
		if got := byHarness()["opencode"].dir; got != filepath.Join("/occfg", "agent") {
			t.Errorf("OPENCODE_CONFIG_DIR must outrank XDG_CONFIG_HOME: %q, want /occfg/agent", got)
		}
		t.Setenv("POLYFORGE_OPENCODE_AGENT_DIR", "/direct")
		if got := byHarness()["opencode"].dir; got != "/direct" {
			t.Errorf("POLYFORGE_OPENCODE_AGENT_DIR must outrank both: %q, want /direct", got)
		}
	})

	t.Run("cc_uses_CLAUDE_PLUGIN_ROOT", func(t *testing.T) {
		isolateHarnessEnv(t)
		root := t.TempDir()
		if err := os.MkdirAll(filepath.Join(root, ".claude-plugin"), 0o755); err != nil {
			t.Fatalf("setup: %v", err)
		}
		if err := os.WriteFile(filepath.Join(root, ".claude-plugin", "plugin.json"), []byte("{}"), 0o644); err != nil {
			t.Fatalf("setup: %v", err)
		}
		t.Setenv("CLAUDE_PLUGIN_ROOT", root)
		got := byHarness()["cc"]
		if got.unresolved != "" {
			t.Fatalf("cc declined a real plugin root: %s", got.unresolved)
		}
		if want := filepath.Join(root, "agents"); got.dir != want {
			t.Errorf("cc dir = %q, want %q", got.dir, want)
		}
	})

	// Every row must explain itself. A row that resolves a directory with no
	// provenance leaves "polyforge wrote somewhere I did not expect"
	// unanswerable, which is the question this table exists to answer.
	t.Run("every_row_states_its_provenance", func(t *testing.T) {
		isolateHarnessEnv(t)
		for _, tgt := range resolveHarnessTargets() {
			if tgt.unresolved != "" {
				continue
			}
			if tgt.source == "" {
				t.Errorf("%s resolved %q with an empty source", tgt.harness, tgt.dir)
			}
			if !filepath.IsAbs(tgt.dir) {
				t.Errorf("%s dir %q is not absolute", tgt.harness, tgt.dir)
			}
		}
	})
}

// TestInGitWorkTree_StopsAtHome pins the second stop condition, added when the
// checkout guard stopped being Claude Code's alone.
//
// 🔴 WITHOUT IT, EXTENDING THE GUARD TO THE OTHER THREE HARNESSES SILENTLY
// DISABLES THEM FOR ANYONE WHO VERSIONS THEIR DOTFILES. Every non-cc row is
// $HOME-relative, so a $HOME/.git would be found from all three and polyforge
// would refuse to write anything, forever, while advising the operator to run
// `go generate` on a repo they have never cloned. That is the same class of bug
// the FIRST stop condition was added to fix.
//
// MUTANT (applied, compiled, red): delete the `dir == home` case from
// inGitWorkTree. Result: the first arm below returns true and fails.
func TestInGitWorkTree_StopsAtHome(t *testing.T) {
	home := isolateHarnessEnv(t)
	if err := os.MkdirAll(filepath.Join(home, ".git"), 0o755); err != nil {
		t.Fatalf("setup: %v", err)
	}
	piDir := filepath.Join(home, ".pi", "agent", "agents")
	if err := os.MkdirAll(piDir, 0o755); err != nil {
		t.Fatalf("setup: %v", err)
	}

	if inGitWorkTree(piDir) {
		t.Errorf("%s was called a checkout because $HOME is a git work tree. Versioning your "+
			"dotfiles must not disable role generation.", piDir)
	}

	// 🔴 THE CONTROL, and it is what stops the fix from being "return false".
	// A repository cloned BELOW $HOME is still a checkout and must still be
	// refused: the guard has to find the NEARER .git before the home stop.
	repo := filepath.Join(home, "src", "aihub")
	nested := filepath.Join(repo, "plugins", "polyforge", "agents")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatalf("setup: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(repo, ".git"), 0o755); err != nil {
		t.Fatalf("setup: %v", err)
	}
	if !inGitWorkTree(nested) {
		t.Errorf("%s is inside a git checkout under $HOME and was NOT refused. Writing "+
			"machine-local models into repo-tracked files is what this guard exists to stop.",
			nested)
	}
}

// TestResolveHarnessTargets_RefusesACheckoutThatDoesNotExistYet is a REGRESSION
// test for a bug this file's first draft shipped and its own checkout test
// missed.
//
// 🔴 THE GUARD WAS WRITTEN `t.exists && inGitWorkTree(t.dir)`, so it evaluated
// only for a directory that was already there — and the case it most needs to
// catch is a target inside a checkout that has NOT been created yet. Gate (1)
// does not apply to an explicitly named harness, so nothing else stood in the
// way and generateRolesInto mkdir -p'd straight into the repository.
//
// Reproduced against the pre-fix binary, not merely reasoned about:
// PI_AGENT_DIR=<home>/src/dotfiles/pi with <home>/src/dotfiles/.git present and
// no pi/agents yet wrote five pf-*.md files inside the checkout. The
// pre-existing TestResolveHarnessTargets_RefusesACheckout could not see it: it
// pre-creates the leaf directory in setup, which is exactly the condition the
// broken guard required.
//
// MUTANT (applied, compiled, red): restore `t.exists &&` in front of
// inGitWorkTree in resolveHarnessTargets.
func TestResolveHarnessTargets_RefusesACheckoutThatDoesNotExistYet(t *testing.T) {
	home := isolateHarnessEnv(t)
	repo := filepath.Join(home, "src", "dotfiles")
	if err := os.MkdirAll(filepath.Join(repo, ".git"), 0o755); err != nil {
		t.Fatalf("setup: %v", err)
	}
	// Deliberately NOT creating <repo>/pi/agents: its absence is the bug.
	t.Setenv("PI_AGENT_DIR", filepath.Join(repo, "pi"))

	reports, err := installTargets(everyHarnessConfigured(), "pi", installOptions{
		requireExistingDir: false, // what `--harness pi` passes — the broken path
		probeFor:           stubProbes,
	})
	if err != nil {
		t.Fatalf("installTargets: %v", err)
	}
	if len(reports) != 1 {
		t.Fatalf("want exactly pi's report, got %d", len(reports))
	}
	if reports[0].skipped == "" {
		t.Errorf("`--harness pi` into a not-yet-created directory inside a git checkout was "+
			"NOT refused: %+v", reports[0])
	}
	if n := len(reports[0].written); n != 0 {
		t.Errorf("wrote %d file(s) into a git checkout", n)
	}
	// The strongest form of the assertion: nothing was created anywhere under the
	// repository, not even the directory.
	var created []string
	_ = filepath.Walk(repo, func(p string, fi os.FileInfo, err error) error {
		if err == nil && p != repo && !strings.Contains(p, ".git") {
			created = append(created, p)
		}
		return nil
	})
	if len(created) != 0 {
		t.Errorf("the guard let %d path(s) be created inside a checkout: %v", len(created), created)
	}
}

// TestResolveHarnessTargets_RefusesACheckout is the guard's effect on the table:
// a row whose directory is a checkout is BLOCKED rather than merely skipped, and
// says so.
//
// ⚠️ This arm pre-creates the leaf directory, which means it exercises only the
// already-exists half. The not-yet-created half is the test above; the two are
// deliberately separate because a single test covering "a checkout" that happened
// to create the directory is what let the bug through.
func TestResolveHarnessTargets_RefusesACheckout(t *testing.T) {
	home := isolateHarnessEnv(t)
	piRoot := filepath.Join(home, "src", "dotfiles")
	if err := os.MkdirAll(filepath.Join(piRoot, ".git"), 0o755); err != nil {
		t.Fatalf("setup: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(piRoot, "agents"), 0o755); err != nil {
		t.Fatalf("setup: %v", err)
	}
	t.Setenv("PI_AGENT_DIR", piRoot)

	var pi harnessTarget
	for _, tgt := range resolveHarnessTargets() {
		if tgt.harness == "pi" {
			pi = tgt
		}
	}
	if pi.blocked == "" {
		t.Fatalf("PI_AGENT_DIR pointed into a git checkout and the pi row was not blocked: %+v", pi)
	}

	// And the block survives all the way to the report, including for an
	// explicitly-named harness — "I asked for it" does not make writing into a
	// repository safe.
	reports, err := installTargets(everyHarnessConfigured(), "pi", installOptions{
		requireExistingDir: false,
		probeFor:           stubProbes,
	})
	if err != nil {
		t.Fatalf("installTargets: %v", err)
	}
	if len(reports) != 1 || reports[0].skipped == "" {
		t.Fatalf("an explicit `--harness pi` into a checkout was not skipped: %+v", reports)
	}
	if n := countEntries(t, filepath.Join(piRoot, "agents")); n != 0 {
		t.Errorf("wrote %d file(s) into a git checkout", n)
	}
}

// ---------------------------------------------------------------------------
// AC4 — atomicity.
// ---------------------------------------------------------------------------

// TestWriteGeneratedFiles_IsAtomic is aihub#683 AC4's success half: a replacement
// swaps in a new inode, so a reader that already has the file open keeps seeing a
// COMPLETE old file rather than a truncated new one. These directories are
// scanned by a live harness, so that window is a real reader loading a
// half-written agent definition, not a thought experiment.
//
// MUTANT (applied, compiled, red): in writeGeneratedFiles, replace
// `writeFileAtomic(path, ...)` with `os.WriteFile(path, ...)`. Result: os.SameFile
// reports the same inode and the held handle reads the NEW bytes; both assertions
// fail.
func TestWriteGeneratedFiles_IsAtomic(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "a.md")
	const old = "OLD-COMPLETE-CONTENT\n"
	if err := os.WriteFile(path, []byte(old), 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}

	before, err := os.Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer func() { _ = before.Close() }()
	beforeStat, err := before.Stat()
	if err != nil {
		t.Fatalf("stat: %v", err)
	}

	rendered := map[string]string{"a.md": "NEW-CONTENT\n"}
	written, err := writeGeneratedFiles(dir, rendered, []string{"a.md"}, agentFilePerm)
	if err != nil {
		t.Fatalf("writeGeneratedFiles: %v", err)
	}
	if len(written) != 1 || written[0] != path {
		t.Fatalf("written = %v, want [%s]", written, path)
	}

	afterStat, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat after: %v", err)
	}
	if os.SameFile(beforeStat, afterStat) {
		t.Error("the destination was written in place (same inode). A concurrent reader can " +
			"then observe a truncated agent file; the write must go through a temp file + rename.")
	}

	held := make([]byte, 64)
	n, _ := before.Read(held)
	if string(held[:n]) != old {
		t.Errorf("a handle opened before the write sees %q, want the complete old content %q",
			string(held[:n]), old)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("readdir: %v", err)
	}
	for _, e := range entries {
		if strings.Contains(e.Name(), ".tmp-") {
			t.Errorf("left a temp file behind in a directory a harness scans: %s", e.Name())
		}
	}
}

// TestWriteGeneratedFiles_FailureLeavesNoPartialFile is AC4's failure half, using
// a REAL induced failure rather than a mocked one.
//
// 🔴 THE INJECTION POINT IS A DIRECTORY AT THE DESTINATION PATH. os.Rename of a
// file onto an existing directory fails with EISDIR — and it fails at the LAST
// step, AFTER writeFileAtomic has created and fully written its temp file. That
// is precisely the window in which a non-atomic implementation has already
// truncated or clobbered the destination. A failure injected any earlier would
// prove nothing about rename semantics, because nothing would have been written
// yet.
//
// Three things must hold afterwards: the error surfaces, the files written BEFORE
// the failure are complete (a partial install must not be reported as none — see
// writeGeneratedFiles), and no temp file survives in a directory a harness scans.
//
// MUTANT (applied, compiled, red): delete the `defer os.Remove(tmp)` from
// writeFileAtomic. Result: a `a.md.tmp-*` file survives the failed rename and the
// residue assertion fails.
func TestWriteGeneratedFiles_FailureLeavesNoPartialFile(t *testing.T) {
	dir := t.TempDir()

	// "b.md" is a directory, so its rename cannot succeed. "a.md" is written
	// first (writeGeneratedFiles honours the order it is given) and must survive
	// complete; "c.md" comes after the failure and must not appear at all.
	if err := os.MkdirAll(filepath.Join(dir, "b.md"), 0o755); err != nil {
		t.Fatalf("setup: %v", err)
	}
	rendered := map[string]string{
		"a.md": "A-COMPLETE\n",
		"b.md": "B-NEVER\n",
		"c.md": "C-NEVER\n",
	}

	written, err := writeGeneratedFiles(dir, rendered, []string{"a.md", "b.md", "c.md"}, agentFilePerm)
	if err == nil {
		t.Fatal("renaming onto a directory succeeded; the injection point is not injecting")
	}
	if !strings.Contains(err.Error(), filepath.Join(dir, "b.md")) {
		t.Errorf("the error %q does not name the file that failed", err)
	}

	// The honesty property: what it says it wrote is what it wrote.
	if len(written) != 1 || written[0] != filepath.Join(dir, "a.md") {
		t.Errorf("written = %v, want exactly [a.md] — the files before the failure, and no "+
			"more", written)
	}
	if body, readErr := os.ReadFile(filepath.Join(dir, "a.md")); readErr != nil || string(body) != "A-COMPLETE\n" {
		t.Errorf("the file written before the failure is %q, %v; want the complete content",
			string(body), readErr)
	}
	if _, statErr := os.Stat(filepath.Join(dir, "c.md")); statErr == nil {
		t.Error("a file after the failure point was written anyway")
	}

	// The destination that failed is untouched: still a directory, not a partial
	// file and not a clobbered one.
	fi, err := os.Stat(filepath.Join(dir, "b.md"))
	if err != nil || !fi.IsDir() {
		t.Errorf("the failed destination is %v (err %v); the write reached through the "+
			"rename and changed it", fi, err)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("readdir: %v", err)
	}
	for _, e := range entries {
		if strings.Contains(e.Name(), ".tmp-") {
			t.Errorf("a failed write left %s behind. A harness scanning this directory would "+
				"load it as an agent definition.", e.Name())
		}
	}
}

// TestWriteGeneratedFiles_PartialFileWouldBeVisible is the instrument check for
// the two tests above: it proves the assertions CAN see a torn file, so their
// greens are not vacuous.
//
// Without it, "no partial file was observed" is equally satisfied by a test that
// cannot observe one.
func TestWriteGeneratedFiles_PartialFileWouldBeVisible(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "a.md")
	const old = "OLD-COMPLETE-CONTENT\n"
	if err := os.WriteFile(path, []byte(old), 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}
	before, err := os.Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer func() { _ = before.Close() }()
	beforeStat, _ := before.Stat()

	// The non-atomic write the production path must NOT be.
	if err := os.WriteFile(path, []byte("TORN"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	afterStat, _ := os.Stat(path)
	if !os.SameFile(beforeStat, afterStat) {
		t.Error("os.WriteFile replaced the inode; the inode assertion in " +
			"TestWriteGeneratedFiles_IsAtomic cannot distinguish the two writers and is vacuous")
	}
	held := make([]byte, 64)
	n, _ := before.Read(held)
	if string(held[:n]) == old {
		t.Error("a handle held across an in-place truncation still saw the old content; the " +
			"held-handle assertion in TestWriteGeneratedFiles_IsAtomic is vacuous")
	}
}

// ---------------------------------------------------------------------------
// AC5 — the output names what it actually wrote.
// ---------------------------------------------------------------------------

// TestPrintInstallReports_NamesEveryPath is aihub#683 AC5.
//
// "Said it finished" and "actually finished" must be distinguishable by READING
// THE OUTPUT. A summary line cannot do that: a run that wrote nothing because
// every directory was missing and a run that wrote twenty files both end in
// success, and "done" is true of both.
//
// MUTANT (applied, compiled, red): replace printInstallReports' per-path loop with
// a single `fmt.Fprintf(w, "  %d file(s)\n", len(r.written))`. Result: every
// path assertion fails.
func TestPrintInstallReports_NamesEveryPath(t *testing.T) {
	reports := []installReport{
		{
			harness: "pi", dir: "/h/.pi/agent/agents", source: "$PI_AGENT_DIR unset",
			written:   []string{"/h/.pi/agent/agents/one.md", "/h/.pi/agent/agents/two.md"},
			unchanged: 3,
		},
		{harness: "opencode", skipped: "/h/.config/opencode/agent does not exist"},
	}

	var sb strings.Builder
	if failed := printInstallReports(&sb, reports, false); failed {
		t.Fatal("printInstallReports reported failure for reports carrying no error")
	}
	out := sb.String()

	for _, want := range []string{
		"/h/.pi/agent/agents/one.md",
		"/h/.pi/agent/agents/two.md",
		"$PI_AGENT_DIR unset",
		"opencode: skipped",
		"/h/.config/opencode/agent does not exist",
		"unchanged (already current): 3",
		"2 file(s) written",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("the output does not contain %q:\n%s", want, out)
		}
	}

	// Each written path on its OWN line: a reader has to be able to diff two
	// machines' output, and a comma-joined blob does not diff.
	for _, path := range reports[0].written {
		var found bool
		for _, line := range strings.Split(out, "\n") {
			if strings.TrimSpace(line) == "wrote "+path {
				found = true
			}
		}
		if !found {
			t.Errorf("%q does not appear on a line of its own:\n%s", path, out)
		}
	}
}

// TestPrintInstallReports_FailureIsNotSilent pins the other half of AC5's
// honesty: a run that wrote three files and then failed must say BOTH, and must
// exit non-zero.
//
// MUTANT (applied, compiled, red): drop the `if r.err != nil` branch from
// printInstallReports. Result: `failed` comes back false and the error text is
// absent; both assertions fail. This is the mutant that turns a partial install
// into a silent success.
func TestPrintInstallReports_FailureIsNotSilent(t *testing.T) {
	reports := []installReport{{
		harness: "pi", dir: "/h/agents", source: "test",
		written: []string{"/h/agents/one.md"},
		err:     errors.New("write /h/agents/two.md: no space left on device"),
	}}

	var sb strings.Builder
	failed := printInstallReports(&sb, reports, false)
	out := sb.String()

	if !failed {
		t.Error("a report carrying an error did not make printInstallReports report failure, " +
			"so `polyforge roles install` would exit 0 on a partial install")
	}
	if !strings.Contains(out, "no space left on device") {
		t.Errorf("the failure is not in the output:\n%s", out)
	}
	if !strings.Contains(out, "/h/agents/one.md") {
		t.Errorf("the file written BEFORE the failure is not named; a partial install that "+
			"reports zero is the same class of lie as one that reports everything:\n%s", out)
	}
	if !strings.Contains(out, "FAILED after 1 file(s)") {
		t.Errorf("the output does not say how far it got:\n%s", out)
	}
}

// TestRolesInstall_DryRunSaysItWroteNothing guards the one sentence a reader
// most needs to trust. A dry run's per-path lines look exactly like a real run's;
// the verb and the closing line are the only things that distinguish them.
func TestRolesInstall_DryRunSaysItWroteNothing(t *testing.T) {
	reports := []installReport{{
		harness: "pi", dir: "/h/agents", source: "test",
		written: []string{"/h/agents/one.md"},
	}}

	var dry, real strings.Builder
	printInstallReports(&dry, reports, true)
	printInstallReports(&real, reports, false)

	if !strings.Contains(dry.String(), "would write /h/agents/one.md") {
		t.Errorf("dry run does not use the conditional verb:\n%s", dry.String())
	}
	if !strings.Contains(dry.String(), "Nothing was written: --dry-run.") {
		t.Errorf("dry run does not state that it wrote nothing:\n%s", dry.String())
	}
	if strings.Contains(real.String(), "would write") {
		t.Errorf("a real run used the conditional verb:\n%s", real.String())
	}
	if dry.String() == real.String() {
		t.Error("a dry run and a real run produce byte-identical output, so the output cannot " +
			"be used to tell whether anything happened")
	}
}

// ---------------------------------------------------------------------------
// Filenames are internal/roles', not this package's.
// ---------------------------------------------------------------------------

// TestPlannedFileNamesComeFromTheRenderers pins that --dry-run's paths are
// derived from the same renderers the generator writes through, rather than from
// a literal in this package.
//
// ⚠️ aihub#682 is renaming pi's agent files from pf-<role> to step-<role> AT THE
// SAME TIME as this change. This test must therefore assert the RELATIONSHIP
// (dry-run's names == the generator's names) and never the names themselves —
// which is also the property that actually matters, since a --dry-run that
// announces paths the generator does not write is worse than no --dry-run.
func TestPlannedFileNamesComeFromTheRenderers(t *testing.T) {
	home := isolateHarnessEnv(t)

	for _, harness := range []string{"pi", "opencode", "codex"} {
		t.Run(harness, func(t *testing.T) {
			dir := filepath.Join(home, harness)
			if err := os.MkdirAll(dir, 0o755); err != nil {
				t.Fatalf("setup: %v", err)
			}

			planned, err := plannedFileNames(harness)
			if err != nil {
				t.Fatalf("plannedFileNames: %v", err)
			}
			if len(planned) == 0 {
				t.Fatal("no planned names; the comparison below would be vacuous")
			}

			var written []string
			captureStderr(t, func() {
				written, err = generateForHarness(
					everyHarnessConfigured(),
					harnessTarget{harness: harness, dir: dir},
					installOptions{probeFor: stubProbes},
				)
			})
			if err != nil {
				t.Fatalf("generateForHarness(%s): %v", harness, err)
			}

			got := make([]string, 0, len(written))
			for _, p := range written {
				got = append(got, filepath.Base(p))
			}
			if strings.Join(got, ",") != strings.Join(planned, ",") {
				t.Errorf("--dry-run would announce %v but the generator wrote %v", planned, got)
			}
		})
	}
}

// TestGenerateRolesInto_SkipsIdenticalRenders pins the property that keeps the
// startup sync silent and mtime-stable in the steady state. Without it every
// session on the machine renames every agent file, forever, to change nothing.
//
// MUTANT (applied, compiled, red): in generateRolesInto, pass
// `sortedNames(rendered)` instead of `changedFiles(outDir, rendered)`. Result: the
// second call reports the full set as written and the assertion fails.
func TestGenerateRolesInto_SkipsIdenticalRenders(t *testing.T) {
	dir := t.TempDir()
	mc := everyHarnessConfigured()

	var first, second []string
	var err error
	captureStderr(t, func() {
		first, err = generateRolesInto(mc, "pi", dir, alwaysAvailable{}, "", false)
	})
	if err != nil {
		t.Fatalf("first: %v", err)
	}
	if len(first) == 0 {
		t.Fatal("the first pass wrote nothing; the second-pass assertion would be vacuous")
	}

	captureStderr(t, func() {
		second, err = generateRolesInto(mc, "pi", dir, alwaysAvailable{}, "", false)
	})
	if err != nil {
		t.Fatalf("second: %v", err)
	}
	if len(second) != 0 {
		t.Errorf("a second identical generation rewrote %d file(s): %v", len(second), second)
	}
}

// TestSyncRolesOnStartup_CodexIsExemptFromTheTierTableGate is a REGRESSION test
// for the second defect review found: generalising aihub#681's cc-shaped
// "the tier table must name this harness" rule to all four harnesses silently
// REMOVED a shipped feature for codex.
//
// 🔴 codex HAS NO INSTALLER. `find plugins -name install.sh` returns pi's and
// opencode's; $CODEX_HOME/step-<role>.config.toml has exactly one writer in this
// repo, the serve boot path. So for codex the gate's own justification ("the
// installer's files stand unchanged") is false: there are no files, and under the
// gate there never would be. Before aihub#683, a codex user with an empty tier
// table still got working profiles — no `model` key, but sandbox_mode and
// developer_instructions, which is the rest of the payload — and `codex -p
// step-reviewer` worked. The gate would have broken that for everyone who never
// wrote a `harness = "codex"` candidate.
//
// MUTANT (applied, compiled, red): drop `&& t.harness != "codex"` from the gate
// in installOne. Result: ~/.codex stays empty and this test fails.
func TestSyncRolesOnStartup_CodexIsExemptFromTheTierTableGate(t *testing.T) {
	if !codexInstalled() {
		t.Skip("codex is not on PATH; the pre-existing aihub#655 LookPath gate applies first")
	}
	home := isolateHarnessEnv(t)
	codexHome := filepath.Join(home, ".codex")
	if err := os.MkdirAll(codexHome, 0o755); err != nil {
		t.Fatalf("setup: %v", err)
	}

	// A tier table that names pi and NOT codex — the machine the gate would have
	// silently switched off.
	mc := mcWithTiers(map[string][]config.RoleCandidate{
		"lowest":  {{Harness: "pi", Model: "prov/a"}},
		"low":     {{Harness: "pi", Model: "prov/a"}},
		"default": {{Harness: "pi", Model: "prov/a"}},
		"raised":  {{Harness: "pi", Model: "prov/a"}},
	})
	captureStderr(t, func() { syncRolesOnStartup(mc, stubProbes) })

	names, err := plannedFileNames("codex")
	if err != nil {
		t.Fatalf("plannedFileNames: %v", err)
	}
	for _, name := range names {
		if _, statErr := os.Stat(filepath.Join(codexHome, name)); statErr != nil {
			t.Fatalf("$CODEX_HOME/%s was not written on a machine with codex installed and a "+
				"tier table that never names codex. Nothing else in this repo writes that file, "+
				"so `codex -p %s` is now broken where it used to work.",
				name, strings.TrimSuffix(name, ".config.toml"))
		}
	}

	// The negative control that keeps the exemption honest: it is codex-specific,
	// not a hole in the gate. opencode's directory exists and its row is unnamed,
	// so it must still be left alone.
	ocDir := filepath.Join(home, ".config", "opencode", "agent")
	if err := os.MkdirAll(ocDir, 0o755); err != nil {
		t.Fatalf("setup: %v", err)
	}
	captureStderr(t, func() { syncRolesOnStartup(mc, stubProbes) })
	if n := countEntries(t, ocDir); n != 0 {
		t.Errorf("opencode's directory holds %d entr(ies); the codex exemption must not have "+
			"widened into a general hole in gate (2)", n)
	}
}

// TestRolesInstall_PresetSkipsCCRatherThanFailing is a REGRESSION test for the
// third defect review found.
//
// `--preset` cannot apply to cc: its files are regenerated in place inside the
// installed plugin from the machine's own [roles] selection, so a per-invocation
// override would be overwritten by the next session. The first draft expressed
// that as a hard ERROR from generateForHarness, which meant `roles install
// --preset=<name>` inside a Claude Code session whose preset names cc wrote
// pi/opencode/codex, then failed the WHOLE command and exited 1 — a partial write
// reported as a failed run, for a flag plugins/polyforge/skills/pf-update/SKILL.md
// advertises.
//
// MUTANT (applied, compiled, red): move the preset/cc case back into
// generateForHarness as `return nil, fmt.Errorf(...)`. Result: cc's report carries
// an error, printInstallReports reports failure, and both assertions fail.
func TestRolesInstall_PresetSkipsCCRatherThanFailing(t *testing.T) {
	home := isolateHarnessEnv(t)

	// A plugin root that looks installed, so the cc row resolves and is reached.
	root := filepath.Join(home, "plugin")
	if err := os.MkdirAll(filepath.Join(root, ".claude-plugin"), 0o755); err != nil {
		t.Fatalf("setup: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, ".claude-plugin", "plugin.json"), []byte("{}"), 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(root, "agents"), 0o755); err != nil {
		t.Fatalf("setup: %v", err)
	}
	t.Setenv("CLAUDE_PLUGIN_ROOT", root)
	piDir := filepath.Join(home, ".pi", "agent", "agents")
	if err := os.MkdirAll(piDir, 0o755); err != nil {
		t.Fatalf("setup: %v", err)
	}

	mc := &config.MachineConfig{Roles: &config.MachineRoles{
		Presets: map[string]*config.RolesPreset{"frugal": {Tiers: map[string][]config.RoleCandidate{
			"lowest":  {{Harness: "pi", Model: "prov/a"}, {Harness: "cc", Model: "haiku"}},
			"low":     {{Harness: "pi", Model: "prov/a"}, {Harness: "cc", Model: "haiku"}},
			"default": {{Harness: "pi", Model: "prov/a"}, {Harness: "cc", Model: "sonnet"}},
			"raised":  {{Harness: "pi", Model: "prov/a"}, {Harness: "cc", Model: "sonnet"}},
		}}},
	}}

	var reports []installReport
	var err error
	captureStderr(t, func() {
		reports, err = installTargets(mc, "", installOptions{
			preset:             "frugal",
			requireExistingDir: true,
			probeFor:           stubProbes,
		})
	})
	if err != nil {
		t.Fatalf("installTargets: %v", err)
	}

	var sb strings.Builder
	failed := printInstallReports(&sb, reports, false)
	if failed {
		t.Errorf("`roles install --preset=frugal` reported failure, so it would exit 1, because "+
			"one row cannot honour the flag:\n%s", sb.String())
	}

	var cc installReport
	for _, r := range reports {
		if r.harness == "cc" {
			cc = r
		}
	}
	if cc.err != nil {
		t.Errorf("the cc row carries an error rather than a skip: %v", cc.err)
	}
	if cc.skipped == "" {
		t.Error("the cc row neither failed nor explained itself; a silently ignored --preset is " +
			"the outcome the refusal exists to prevent")
	}
	// And the rows that CAN honour the preset still did.
	if n := countEntries(t, piDir); n == 0 {
		t.Error("pi was not written, so this test is not exercising the mixed-row case")
	}
}
