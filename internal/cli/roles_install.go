package cli

import (
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"github.com/GMISWE/ieops-aihub/internal/config"
	"github.com/GMISWE/ieops-aihub/internal/roles"
)

// aihub#683 — ONE table of where each harness keeps its role agent definitions,
// one driver that writes them there, and one rule for when a boot may do it
// unasked (owner decision 2026-09-15).
//
// WHAT THIS REPLACES
// ------------------
// Before this file, "where do harness H's agent files go" had four different
// answers living in four different places, and only one of them was in Go:
//
//   - cc       internal/cli.DefaultCCAgentsDir (aihub#681, the first row)
//   - codex    a $CODEX_HOME block inlined in cmd/polyforge/main.go
//   - pi       `PI_DIR="${PI_AGENT_DIR:-$HOME/.pi/agent}"` in a shell installer
//   - opencode a three-deep shell parameter expansion in a different installer
//
// A fifth answer, DefaultCodexAgentsDir, points at a directory codex has never
// scanned (aihub#655) and has had no production caller since. It is annotated as
// superseded by this file's codexTarget; deleting it needs the test file
// aihub#682 is holding, so it is left as a follow-up rather than as a competing
// answer anybody should read.
//
// The Go side therefore could not answer the question for half the harnesses,
// which is why "install the agent definitions" was only ever a thing an install
// SCRIPT did, and why keeping them up to date afterwards was nobody's job.
//
// WHY A BINARY SUBCOMMAND AND NOT JUST A SKILL
// --------------------------------------------
// The owner's request was "add a /pf-update skill that puts the agent definition
// files into each harness's agents directory, so the behaviour is uniform".
// Placing N rendered files into M directories is a deterministic mechanical act
// with a right answer; a skill is an instruction a model reads and may drift
// from. The engine does the placement, the skill is a thin entry point to it.
//
// AND WHY THE STARTUP SYNC IS THE PART THAT MATTERS
// -------------------------------------------------
// A command someone must remember to run does not make behaviour uniform, it
// makes it LOOK uniform. SyncRolesOnStartup is what actually delivers the
// request; RunRolesInstall exists for "I just edited config.toml and want it now,
// without waiting for the next session", which is real but secondary.

// harnessTarget is one row of the per-harness default-directory table: where this
// machine keeps harness H's role agent definitions, how that location was
// decided, and whether this machine looks like it installed H at all.
type harnessTarget struct {
	// harness is the key used everywhere else in this package and in
	// ~/.polyforge/config.toml's [roles.tiers] candidates.
	harness string

	// dir is the resolved absolute directory, or "" when this machine cannot
	// name one (see unresolved).
	dir string

	// source is the provenance of dir, in the operator's own vocabulary: which
	// environment variable supplied it, or which default it fell back to. It is
	// printed rather than kept internal because "polyforge wrote somewhere I did
	// not expect" is answerable only by saying WHY that path.
	source string

	// unresolved, when non-empty, is why no directory could be named at all.
	// This is different from a directory that simply does not exist: one means
	// "cannot tell", the other means "not installed".
	unresolved string

	// exists reports whether dir is an existing directory RIGHT NOW. It is the
	// startup gate; see installTargets.
	exists bool

	// blocked, when non-empty, is why this row must not be written even though
	// the operator asked for it — today only the git-work-tree guard.
	blocked string
}

// resolveHarnessTargets builds the whole table for this machine, in a stable
// order (the order a reader of `--dry-run` should see: the two that a polyforge
// install script owns, then the two the harness itself owns).
//
// It performs NO writes, runs NO subprocess and never fails: every row either
// carries a directory or carries the reason it does not. That is what lets
// `--dry-run` be honest for free, and it is why the gate below can be a plain
// field read rather than another round of environment guessing.
func resolveHarnessTargets() []harnessTarget {
	targets := []harnessTarget{
		piTarget(),
		opencodeTarget(),
		codexTarget(),
		ccTarget(),
	}
	for i := range targets {
		t := &targets[i]
		if t.unresolved != "" {
			continue
		}
		fi, err := os.Stat(t.dir)
		t.exists = err == nil && fi.IsDir()

		// The git-work-tree guard, generalised from aihub#681's cc-only version
		// to every row. Writing a MACHINE-LOCAL model into files a repository
		// TRACKS turns that repo's own staleness gate red on this machine with no
		// hint as to why, and that hazard belongs to the target directory, not to
		// the harness that reads it: `PI_AGENT_DIR=~/src/dotfiles/pi` is a
		// perfectly ordinary thing for someone to write.
		//
		// See inGitWorkTree for the two stop conditions, the second of which
		// ($HOME) exists ONLY because of this generalisation: without it every
		// $HOME-relative row here would refuse to write on any machine whose home
		// directory is versioned.
		//
		// 🔴 DELIBERATELY NOT GUARDED BY t.exists, AND THAT BUG SHIPPED IN THIS
		// FUNCTION'S FIRST DRAFT. Written as `t.exists && inGitWorkTree(...)` the
		// guard evaluates only for a directory that is already there — so the one
		// case it most needs to catch, a target INSIDE a checkout that has not
		// been created yet, sailed straight through it: gate (1) does not apply to
		// an explicitly named harness, and generateRolesInto then mkdir -p's the
		// path. Reproduced before the fix: PI_AGENT_DIR=~/src/dotfiles/pi with
		// ~/src/dotfiles/.git present and no pi/agents yet wrote five agent files
		// into the checkout. The skip message for gate (1) below RECOMMENDS
		// `--harness <h>`, so the broken form was reachable by following this
		// tool's own advice.
		//
		// inGitWorkTree handles a non-existent dir correctly: EvalSymlinks fails
		// and leaves the lexical path, and the walk then Lstats ancestors that do
		// exist.
		if inGitWorkTree(t.dir) {
			t.blocked = fmt.Sprintf("%s is inside a git work tree, so it is a checkout rather "+
				"than an installed harness; writing this machine's model choices into files a "+
				"repository tracks would turn that repository's tests red with no hint as to why",
				t.dir)
		}
	}
	return targets
}

// piTarget is the pi row. It mirrors plugins/polyforge/pi/install.sh
// (`PI_DIR="${PI_AGENT_DIR:-$HOME/.pi/agent}"`) plus the `/agents` suffix that
// script appends, because the two must not be able to disagree about where the
// SAME files live -- the installer writes them once and this path keeps them
// current afterwards.
//
// ⚠️ $PI_AGENT_DIR IS POLYFORGE'S KNOB, NOT pi's, and the distinction matters to
// anyone reading this as documentation of pi. Measured by aihub#682 on
// 2026-09-15: zero occurrences of PI_AGENT_DIR anywhere in
// @earendil-works/pi-coding-agent -- pi resolves its agent directory from $HOME
// alone. So this variable redirects where POLYFORGE writes, and setting it
// without also moving pi's own home means writing somewhere pi will not read.
// It is honoured here only because install.sh honours it, and the two must
// agree; the consequence for tests is that isolating this row needs a scratch
// $HOME, and $PI_AGENT_DIR alone would leave the real ~/.pi/agent in play.
//
// pi discovers agents in exactly two places (install.sh's header):
// ~/.pi/agent/agents/, always loaded, and <project>/.pi/agents/, loaded only when
// agentScope is project/both, which is off by default and is a repo-controlled
// prompt besides. Only the user-scope one is ours.
func piTarget() harnessTarget {
	if dir := os.Getenv("PI_AGENT_DIR"); dir != "" {
		return harnessTarget{
			harness: "pi",
			dir:     filepath.Join(dir, "agents"),
			source:  "$PI_AGENT_DIR/agents",
		}
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return harnessTarget{harness: "pi", unresolved: piNoHomeReason(err)}
	}
	return harnessTarget{
		harness: "pi",
		dir:     filepath.Join(home, ".pi", "agent", "agents"),
		source:  "$PI_AGENT_DIR unset, so pi's default ~/.pi/agent/agents",
	}
}

func piNoHomeReason(err error) string {
	return fmt.Sprintf("$PI_AGENT_DIR is unset and $HOME could not be resolved (%v), so pi's "+
		"default ~/.pi/agent/agents cannot be named", err)
}

// opencodeTarget is the opencode row, mirroring
// plugins/polyforge/opencode/install.sh:38-39 exactly, including its THREE levels
// of override and the singular `agent` (not `agents`) directory name:
//
//	OC_CONFIG_DIR="${OPENCODE_CONFIG_DIR:-${XDG_CONFIG_HOME:-$HOME/.config}/opencode}"
//	AGENT_DIR="${POLYFORGE_OPENCODE_AGENT_DIR:-$OC_CONFIG_DIR/agent}"
//
// OPENCODE_CONFIG_DIR is opencode's OWN variable, not one polyforge invented --
// that installer's header records confirming it against the compiled binary's
// strings -- so honouring it here is honouring the harness, and skipping it would
// aim this at a directory opencode does not read.
func opencodeTarget() harnessTarget {
	if dir := os.Getenv("POLYFORGE_OPENCODE_AGENT_DIR"); dir != "" {
		return harnessTarget{
			harness: "opencode",
			dir:     dir,
			source:  "$POLYFORGE_OPENCODE_AGENT_DIR",
		}
	}
	if cfgDir := os.Getenv("OPENCODE_CONFIG_DIR"); cfgDir != "" {
		return harnessTarget{
			harness: "opencode",
			dir:     filepath.Join(cfgDir, "agent"),
			source:  "$OPENCODE_CONFIG_DIR/agent",
		}
	}
	if xdg := os.Getenv("XDG_CONFIG_HOME"); xdg != "" {
		return harnessTarget{
			harness: "opencode",
			dir:     filepath.Join(xdg, "opencode", "agent"),
			source:  "$XDG_CONFIG_HOME/opencode/agent",
		}
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return harnessTarget{
			harness: "opencode",
			unresolved: fmt.Sprintf("none of $POLYFORGE_OPENCODE_AGENT_DIR, $OPENCODE_CONFIG_DIR "+
				"or $XDG_CONFIG_HOME is set and $HOME could not be resolved (%v)", err),
		}
	}
	return harnessTarget{
		harness: "opencode",
		dir:     filepath.Join(home, ".config", "opencode", "agent"),
		source:  "no override set, so opencode's default ~/.config/opencode/agent",
	}
}

// codexTarget is the codex row, and the directory is $CODEX_HOME rather than any
// plugin- or project-relative path.
//
// ⚠️ THE FILES THIS ROW WRITES ARE CONFIG PROFILES, NOT AGENT FILES, and that is
// not a quirk of naming. aihub#655 established live against codex-cli 0.154.0
// that this codex has NO directory-scanning subagent discovery at all, so a
// step-<role>.toml agent file is inert in every directory including this one. The
// shape codex actually loads is $CODEX_HOME/step-<role>.config.toml, selected
// with `codex -p step-<role>`. `polyforge roles generate codex --out <dir>` still
// produces the inert shape for inspection and says so at its call site; this row
// produces the loadable one.
func codexTarget() harnessTarget {
	if dir := os.Getenv("CODEX_HOME"); dir != "" {
		return harnessTarget{harness: "codex", dir: dir, source: "$CODEX_HOME"}
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return harnessTarget{
			harness:    "codex",
			unresolved: fmt.Sprintf("$CODEX_HOME is unset and $HOME could not be resolved (%v)", err),
		}
	}
	return harnessTarget{
		harness: "codex",
		dir:     filepath.Join(home, ".codex"),
		source:  "$CODEX_HOME unset, so codex's default ~/.codex",
	}
}

// ccTarget is the Claude Code row. Unlike the other three it is NOT
// $HOME-relative: the files live inside the installed plugin's own tree, which is
// what keeps the "polyforge:step-<role>" namespaced dispatch id resolving to the
// plugin's file rather than to a same-named project agent. See GenerateCCAgents
// for why that guarantee is load-bearing for step-reviewer specifically.
//
// It is therefore also the one row that can be UNRESOLVABLE on a perfectly
// healthy machine: $CLAUDE_PLUGIN_ROOT is exported only into a process Claude
// Code launched, so `polyforge roles install` typed into a shell cannot name it.
// That is a true answer, not a failure -- a plugin this process was not launched
// from is not a directory to guess at.
func ccTarget() harnessTarget {
	dir, ok := DefaultCCAgentsDir()
	if !ok {
		if os.Getenv("CLAUDE_PLUGIN_ROOT") == "" {
			return harnessTarget{
				harness: "cc",
				unresolved: "$CLAUDE_PLUGIN_ROOT is not set, so this process was not launched by " +
					"Claude Code; its agent files live inside the installed plugin and there is no " +
					"machine-wide directory to write instead. A Claude Code session regenerates " +
					"them on its own MCP server's boot",
			}
		}
		return harnessTarget{
			harness: "cc",
			unresolved: fmt.Sprintf("$CLAUDE_PLUGIN_ROOT (%s) has no .claude-plugin/plugin.json, "+
				"so it is not a Claude Code plugin root", os.Getenv("CLAUDE_PLUGIN_ROOT")),
		}
	}
	return harnessTarget{harness: "cc", dir: dir, source: "$CLAUDE_PLUGIN_ROOT/agents"}
}

// harnessOrder is the canonical harness list, used to validate --harness and to
// name the choices in an error. Derived from the table so the two cannot drift.
func harnessOrder() []string {
	targets := resolveHarnessTargets()
	out := make([]string, 0, len(targets))
	for _, t := range targets {
		out = append(out, t.harness)
	}
	return out
}

// installReport is what one row's attempt produced. Every field is something the
// output must be able to state: an install that cannot say which files it wrote
// is indistinguishable from one that wrote none.
type installReport struct {
	harness string
	dir     string
	source  string

	// skipped, when non-empty, is why nothing was attempted.
	skipped string

	// written holds the absolute paths actually written, in write order. It is
	// populated on the error path too, up to the point of failure.
	written []string

	// unchanged counts files whose bytes already matched and were left alone.
	unchanged int

	err error
}

// installOptions carries the driver's knobs. A struct rather than five positional
// parameters because three of them are booleans and a call site of
// `install(mc, t, true, false, true)` cannot be read.
type installOptions struct {
	// dryRun prints what would be written and writes nothing.
	dryRun bool

	// preset overrides the machine's [roles] preset selection for this run.
	preset string

	// requireExistingDir gates on the harness's directory already existing. It is
	// TRUE for the startup sync and for a bare `roles install`, and FALSE when
	// the operator named a harness explicitly -- see installTargets.
	requireExistingDir bool

	// preflight runs the once-per-process machine-config diagnosis. False on the
	// startup path, where cmd/polyforge's main() has already run it for the whole
	// boot (aihub#681: a copy per generator gave a two-harness machine two copies
	// of every warning on every session).
	preflight bool

	// probeFor supplies each harness's model-catalog probe. Injected rather than
	// hardcoded so a test can pin resolution without shelling out to a CLI that
	// may or may not be installed on the machine running it. nil means the real
	// probes.
	probeFor func(harness string) CatalogProbe
}

func (o installOptions) probe(harness string) CatalogProbe {
	if o.probeFor == nil {
		return probeForHarness(harness)
	}
	return o.probeFor(harness)
}

// installTargets runs the table and returns one report per row.
//
// 🔴 THE GATE IS THE FEATURE, and it has two independent halves. Getting either
// one wrong in the permissive direction means polyforge conjuring ~/.pi/ on a
// machine that never installed pi.
//
//	(1) THE DIRECTORY MUST ALREADY EXIST (requireExistingDir).
//	    This is the proxy for "this machine installed this harness's polyforge
//	    integration": each directory here is created by that harness's own
//	    installer or by the harness itself, never by this code. It is deliberately
//	    a property of the MACHINE and not of the config file, because a tier table
//	    is copied between machines and an installed harness is not.
//	    It is relaxed only when the operator names a harness explicitly
//	    (`roles install --harness pi`), which is an instruction, not an inference.
//
//	(2) THE MACHINE'S TIER TABLE MUST NAME THIS HARNESS.
//	    A table that says nothing about opencode has nothing machine-local to
//	    apply to it, so regenerating would rewrite the installer's files with an
//	    identical render while printing one loud AC7 warning PER ROLE, on every
//	    boot, about a harness the operator never mentioned. aihub#681 established
//	    exactly this rule for cc (hasCCCandidate); this is the same rule for the
//	    other three.
//	    It also keeps the boot cheap: an unnamed harness's catalog probe never
//	    runs, and those probes are subprocesses costing up to seconds each
//	    (measured at catalogProbeTimeout).
//
// The two are genuinely independent: a machine can configure a pi model without
// having installed pi (gate 1 stops it), and can have pi installed while
// configuring nothing for it (gate 2 stops it).
func installTargets(mc *config.MachineConfig, only string, opts installOptions) ([]installReport, error) {
	tiers, tierSource, err := mc.ResolveTiers(opts.preset)
	if err != nil {
		// A misspelled preset is a hard error, not a per-row skip: every row
		// would fail for the same reason and saying so four times is noise.
		return nil, err
	}
	if opts.preflight {
		machineConfigPreflight(os.Stderr, tiers, tierSource)
	}

	targets := resolveHarnessTargets()
	reports := make([]installReport, 0, len(targets))
	for _, t := range targets {
		if only != "" && t.harness != only {
			continue
		}
		reports = append(reports, installOne(mc, t, tiers, opts))
	}
	return reports, nil
}

func installOne(mc *config.MachineConfig, t harnessTarget, tiers map[string][]config.RoleCandidate, opts installOptions) installReport {
	r := installReport{harness: t.harness, dir: t.dir, source: t.source}

	switch {
	case t.unresolved != "":
		r.skipped = t.unresolved
		return r
	case t.blocked != "":
		r.skipped = t.blocked
		return r
	case opts.requireExistingDir && !t.exists:
		// Gate (1). The wording names the directory because the operator's next
		// question is always "so where would it have gone", and the remedy
		// because the explicit form is the supported way to create it.
		r.skipped = fmt.Sprintf("%s does not exist, so this machine has not installed %s's "+
			"polyforge integration; nothing was created. Run `polyforge roles install --harness %s` "+
			"to create it deliberately", t.dir, t.harness, t.harness)
		return r
	case opts.requireExistingDir && t.harness == "codex" && !codexInstalled():
		// The pre-existing aihub#655 gate, preserved. It applies on the inferred
		// path only, for the same reason the directory gate does: an explicitly
		// named harness is an instruction, not an inference.
		r.skipped = "the codex CLI is not on PATH, so nothing will ever read $CODEX_HOME; " +
			"run `polyforge roles install --harness codex` to write the profiles anyway"
		return r
	case !hasHarnessCandidate(tiers, t.harness) && t.harness != "codex":
		// Gate (2).
		//
		// 🔴 codex IS EXEMPT, AND THE EXEMPTION IS THE WHOLE POINT OF THE GATE'S
		// OWN JUSTIFICATION. Gate (2) is sound because an unnamed harness's files
		// were already written by ITS INSTALLER and are left standing. codex has
		// no installer: `find plugins -name install.sh` returns pi's and
		// opencode's and nothing else, and $CODEX_HOME/step-<role>.config.toml has
		// exactly one writer in this repo — this function, on the serve boot path.
		// Applying the gate to codex therefore does not mean "leave the installer's
		// files alone", it means "there are no files, and there never will be":
		// `codex -p step-reviewer` would stop working for every codex user who has
		// not written a `harness = "codex"` candidate, and would never start on a
		// fresh machine. Before aihub#683 the codex path was gated on
		// exec.LookPath alone and wrote profiles regardless of the tier table; a
		// profile with no `model` key is still a working profile, because
		// sandbox_mode and developer_instructions are the rest of the payload.
		//
		// The cost of the exemption is the AC7 warning per role on a codex machine
		// that names no codex candidate — which is exactly what that machine
		// printed before this change too, so it is preserved behaviour rather than
		// new noise.
		r.skipped = fmt.Sprintf("this machine's tier table names no `harness = %q` candidate, so "+
			"there is nothing machine-local to apply; %s's installed defaults stand unchanged",
			t.harness, t.harness)
		return r
	case opts.preset != "" && t.harness == "cc":
		// 🔴 A SKIP, NOT AN ERROR, and the difference is a whole exit code. cc has
		// no per-invocation preset: GenerateCCAgents resolves this machine's own
		// [roles] selection and writes in place inside the installed plugin, where
		// the next session would regenerate from the machine selection anyway — so
		// honouring --preset here would write something that does not survive, and
		// ignoring it silently would be worse.
		//
		// Returning an ERROR for it (the first draft) made `roles install
		// --preset=<name>` fail outright on any Claude Code machine whose preset
		// names cc: the pi/opencode/codex rows run first and are written, then cc
		// errors, printInstallReports reports failure and the command exits 1 —
		// a partial write reported as a failed command, for a flag the /pf-update
		// skill advertises. One row cannot honour the flag; that is this row's
		// news to report, not the command's verdict.
		r.skipped = "--preset does not apply to cc: its agent files are regenerated in place " +
			"inside the installed plugin from this machine's own [roles] selection, and a " +
			"per-invocation override would be overwritten by the next session. Every other " +
			"harness honoured the preset"
		return r
	}

	if opts.dryRun {
		names, err := plannedFileNames(t.harness)
		if err != nil {
			r.err = err
			return r
		}
		for _, name := range names {
			r.written = append(r.written, filepath.Join(t.dir, name))
		}
		return r
	}

	planned, plannedErr := plannedFileNames(t.harness)
	written, err := generateForHarness(mc, t, opts)
	r.written, r.err = written, err
	if plannedErr == nil && err == nil {
		// "5 files, 1 written" and "5 files, 5 written" are different events and an
		// operator reading a quiet run needs to know which one happened.
		//
		// ⚠️ THIS INFERS "not written ⇒ already identical", WHICH IS ONLY SOUND
		// BECAUSE EVERY OTHER REASON A GENERATOR CAN RETURN EMPTY IS INTERCEPTED
		// ABOVE. On the success path each generator returns (nil, nil) only after
		// comparing every rendered file and finding them all equal; its other early
		// exits -- cc's git-work-tree refusal and its no-cc-candidate guard -- are
		// caught by t.blocked and gate (2) before generateForHarness is reached, so
		// they cannot land here and be mis-reported as "already current".
		//
		// 🔴 If a generator ever gains another empty-return path, this becomes a
		// lie in the output, and the fix is to have the generators report the count
		// rather than have this line deduce it. Review caught exactly that: before
		// the checkout guard was corrected to run for a not-yet-created directory,
		// cc's own refusal DID reach here and printed "unchanged (already current):
		// 5 file(s)" for five files that did not exist.
		r.unchanged = len(planned) - len(written)
		if r.unchanged < 0 {
			r.unchanged = 0
		}
	}
	return r
}

// generateForHarness dispatches one row to the generator that owns its file
// shape. The dispatch is by harness key and NOT by anything about the filenames:
// those come from internal/roles' renderers, which are free to rename what they
// emit (aihub#682 renames pi's from pf-<role> to step-<role>) without this file
// needing to know.
func generateForHarness(mc *config.MachineConfig, t harnessTarget, opts installOptions) ([]string, error) {
	switch t.harness {
	case "cc":
		return generateCCAgents(mc, t.dir)
	case "codex":
		return generateCodexProfiles(mc, t.dir, opts.probe("codex"), opts.preset, opts.preflight)
	case "pi", "opencode":
		return generateRolesInto(mc, t.harness, t.dir, opts.probe(t.harness), opts.preset, opts.preflight)
	default:
		return nil, fmt.Errorf("no generator for harness %q", t.harness)
	}
}

// plannedFileNames is the set of file names this harness's generator emits,
// sorted, obtained by rendering with an EMPTY resolved-model map.
//
// 🔴 IT RENDERS RATHER THAN HARDCODING, and it renders with no models rather than
// resolving them. The first half is because agent filenames are internal/roles'
// to decide and are actively being changed (aihub#682); a literal "pf-%s.md" here
// would make `--dry-run` announce paths the generator no longer writes. The
// second half is because resolving models means running a catalog subprocess
// costing up to seconds -- and the model a role resolves to does not change the
// NAME of the file it is written to, which is the only thing this function
// returns. A dry run must not have to authenticate against three CLIs to print
// four paths.
func plannedFileNames(harness string) ([]string, error) {
	roleList, err := roles.LoadRoles()
	if err != nil {
		return nil, fmt.Errorf("load roles: %w", err)
	}
	none := map[string]string{}

	var rendered map[string]string
	switch harness {
	case "cc":
		rendered, err = roles.RenderCCAgentFilesWithModels(roleList, none)
	case "codex":
		rendered, err = roles.RenderCodexProfiles(roleList, none)
	case "pi":
		rendered, err = roles.RenderPiAgentFiles(roleList, none)
	case "opencode":
		rendered, err = roles.RenderOpencodeAgentFiles(roleList, none)
	default:
		return nil, fmt.Errorf("no renderer for harness %q", harness)
	}
	if err != nil {
		return nil, fmt.Errorf("render %s agent files: %w", harness, err)
	}

	names := make([]string, 0, len(rendered))
	for name := range rendered {
		names = append(names, name)
	}
	sort.Strings(names)
	return names, nil
}

// hasHarnessCandidate reports whether any tier names this harness. It asks about
// the RAW table rather than about resolution, because the question the gate needs
// answered is "did this operator ask for this harness at all" -- a candidate whose
// model turns out to be unavailable still means yes, and must still produce the
// omit-and-warn path rather than silent inaction.
//
// This is hasCCCandidate generalised; that function remains as the cc-specific
// spelling its own call site reads better with.
func hasHarnessCandidate(tiers map[string][]config.RoleCandidate, harness string) bool {
	for _, candidates := range tiers {
		for _, c := range candidates {
			if c.Harness == harness {
				return true
			}
		}
	}
	return false
}

// generateCodexProfiles renders one codex config-profile file per role and writes
// them into codexHome as step-<role>.config.toml -- the literal filename codex's
// own `-p <name>` / `--profile <name>` flag requires.
//
// Moved here from cmd/polyforge/main.go by aihub#683. Its own doc comment there
// recorded that it duplicated internal/cli's codex probe only because
// roles_generate.go "is not one of this wi's declared_resources"; with that no
// longer true, the duplicate probe (and its separate, divergent timeout constants)
// is gone and this path uses the same bounded probe every other harness does.
//
// WHY BOOT-TIME REGENERATION AND NOT A codex SessionStart HOOK, carried over from
// that call site because it is measured evidence nothing else records. aihub#655
// built a real local codex plugin marketplace declaring a SessionStart hook,
// installed it (`codex plugin list` confirmed "installed, enabled"), then ran
// `codex exec --dangerously-bypass-hook-trust --json ...` under a scratch
// $CODEX_HOME. The hook's marker file never appeared, and the --json event stream
// -- which does carry structured lifecycle and error items -- shows no
// hook-related event anywhere in the run, including immediately after
// `thread.started`, i.e. before any network call, so the run's later 401s cannot
// explain it away. That is meaningful evidence SessionStart hooks do not fire
// under headless `codex exec`, the exact invocation mode aihub#654's headless
// orchestrator would use -- though not certain proof, since that environment never
// reached a fully authenticated turn. Boot-time regeneration was kept as the
// strictly safer choice: every codex session boots this server fresh regardless of
// interactive or headless mode.
func generateCodexProfiles(mc *config.MachineConfig, codexHome string, probe CatalogProbe, preset string, preflight bool) ([]string, error) {
	roleList, err := roles.LoadRoles()
	if err != nil {
		return nil, fmt.Errorf("load roles: %w", err)
	}

	// 🔴 Resolved through the ONE resolver, not read straight off mc.Roles.Tiers
	// (aihub#673). This is one of the production readers of the tier table; if it
	// ignored the machine's [roles] preset while `polyforge roles generate` and
	// `polyforge drain` honoured it, the same machine would resolve different
	// models depending on which entry point ran.
	tiers, tierSource, err := mc.ResolveTiers(preset)
	if err != nil {
		return nil, err
	}
	// Tied to `preflight` for the same reason generateRolesInto's twin line is:
	// this runs on every serve boot, and a boot that changes nothing must say
	// nothing.
	if preflight {
		fmt.Fprintf(os.Stderr, "polyforge: codex profiles: tier table from %s\n", tierSource)
		machineConfigPreflight(os.Stderr, tiers, tierSource)
	}

	resolved := make(map[string]string, len(roleList))
	for _, r := range roleList {
		model, ok, why := ResolveModelWithCause(tiers[r.Tier], "codex", probe)
		if !ok {
			// Loud and non-suppressible by design (aihub#642 AC7): no flag gates
			// this line, and the message is built by the same function every
			// other harness uses so the two cannot drift into blaming different
			// things for the same failure (aihub#676).
			fmt.Fprint(os.Stderr, UnresolvedModelWarning("codex", r.Tier, r.Name, tierSource, why))
			continue // resolved[r.Name] stays unset, read back as "".
		}
		resolved[r.Name] = model
	}

	rendered, err := roles.RenderCodexProfiles(roleList, resolved)
	if err != nil {
		return nil, fmt.Errorf("render codex profiles: %w", err)
	}

	changed := changedFiles(codexHome, rendered)
	if len(changed) == 0 {
		return nil, nil
	}
	if err := os.MkdirAll(codexHome, 0o755); err != nil {
		return nil, fmt.Errorf("mkdir %s: %w", codexHome, err)
	}
	// No warnOrphanAgentFiles: $CODEX_HOME is the operator's own directory, full
	// of codex's files, and naming an orphan by filename pattern there would have
	// this generator commenting on things it never wrote.
	return writeGeneratedFiles(codexHome, rendered, changed, agentFilePerm)
}

// SyncRolesOnStartup regenerates role agent definitions for every harness this
// machine has both INSTALLED and CONFIGURED, and is the thing that actually makes
// the behaviour uniform (aihub#683).
//
// 🔴 CALL IT IN A GOROUTINE. cmd/polyforge/main.go does, and the reason is
// measured rather than stylistic. This walks up to four harnesses, and three of
// them answer a model-catalog question by running a subprocess. Measured on the
// author's machine 2026-09-15:
//
//	codex debug models   176ms cold
//	pi --list-models     1.6s cold / ~0.91s warm
//	opencode models      8.2s cold / ~3.4s warm
//
// Run serially ahead of server.Serve, a fully-configured machine would therefore
// pay 4.5s warm and 10s cold BEFORE the MCP server speaks its first byte of
// protocol -- on every session on the machine, since each one starts its own
// server. aihub#679 is the record of what a pre-serve stall presents as: "MCP will
// not connect", everywhere at once, with no log line because the block precedes
// the first one. Backgrounding removes the class instead of shrinking it.
//
// Nothing here needs to complete before serving. Every harness reads these files
// at ITS OWN startup, which already happened for the session that launched this
// process -- aihub#681 measured exactly this for Claude Code ("takes effect in the
// NEXT session"). codex's profiles are read later still, when `codex -p` runs. A
// process that exits before this finishes simply skips a regeneration the next
// boot redoes.
//
// It is non-fatal by contract: every error is a line on stderr. Role generation
// must never be able to break MCP server startup for anyone.
//
// ⚠️ KNOWN, ACCEPTED LIMITATION of running it detached: if the process exits
// between writeFileAtomic's CreateTemp and its Rename, the deferred cleanup does
// not run and a `<name>.tmp-<random>` file is left in the target directory.
// Bounded in practice -- the window is microseconds per file, and the harnesses
// that scan these directories match on the real extension (`.md`, `.config.toml`)
// which a `.tmp-…` suffix does not satisfy, so a leftover is inert rather than
// loadable. It is NOT reported by warnOrphanAgentFiles, which also requires the
// real suffix. If such files are ever observed in anger, the fix is a sweep of
// `*.tmp-*` at the start of the sync, not abandoning the rename.
func SyncRolesOnStartup(mc *config.MachineConfig) {
	syncRolesOnStartup(mc, nil)
}

// syncRolesOnStartup is SyncRolesOnStartup with the catalog probes injectable.
//
// The seam exists because the alternative is a test that shells out to whichever
// of codex/pi/opencode happens to be installed on the machine running it, which
// makes "the startup path regenerated from the machine's table" pass or fail for
// reasons that have nothing to do with the startup path. probeFor == nil is
// production.
func syncRolesOnStartup(mc *config.MachineConfig, probeFor func(string) CatalogProbe) {
	reports, err := installTargets(mc, "", installOptions{
		requireExistingDir: true,
		// main() ran the machine-config diagnosis once for the whole boot before
		// calling this; repeating it per harness is what aihub#681 removed.
		preflight: false,
		probeFor:  probeFor,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "polyforge: role sync skipped (non-fatal, server continues): %v\n", err)
		return
	}
	for _, r := range reports {
		if r.err != nil {
			fmt.Fprintf(os.Stderr, "polyforge: %s role generation failed (non-fatal, server "+
				"continues): %v\n", r.harness, r.err)
		}
	}
	// Deliberately silent about success. A boot that changed nothing says
	// nothing: the generators announce their own writes, and a per-harness "up to
	// date" line would be four lines per session on every machine, forever, to
	// report that nothing happened.
}

// RunRolesInstall is the `polyforge roles install [--harness <h>] [--dry-run]
// [--preset=<name>]` subcommand, dispatched from cmd/polyforge/main.go's runCLI
// and wrapped by the /pf-update skill.
//
// 🔴 IT NAMES EVERY PATH IT WROTE (aihub#683 AC5). "Said it finished" and
// "actually finished" have to be distinguishable by reading the output, which a
// summary line cannot do: a run that wrote nothing because every directory was
// missing and a run that wrote twenty files both end in success. Each written
// file gets its own line, each skipped harness gets its reason, and a failure
// still lists what was written BEFORE it -- a partial install that reports zero is
// the same class of lie as one that reports everything.
//
// Exits non-zero if any harness failed, so a script can tell.
func RunRolesInstall(mc *config.MachineConfig, args []string) {
	fs := flag.NewFlagSet("roles install", flag.ExitOnError)
	harness := fs.String("harness", "", "install for one harness only (default: every harness this machine has installed and configured)")
	dryRun := fs.Bool("dry-run", false, "print the paths that would be written and write nothing")
	preset := fs.String("preset", "", "named tier-table snapshot to generate from (default: the machine's [roles] preset)")
	_ = fs.Parse(args)

	if *harness != "" && !slices.Contains(harnessOrder(), *harness) {
		fmt.Fprintf(os.Stderr, "roles install: unknown harness %q (must be one of %s)\n",
			*harness, strings.Join(harnessOrder(), ", "))
		os.Exit(1)
	}

	reports, err := installTargets(mc, *harness, installOptions{
		dryRun: *dryRun,
		preset: *preset,
		// An explicitly named harness is an instruction: create the directory.
		// The bare form is an inference from what is already installed, and must
		// not create anything. See installTargets' gate (1).
		requireExistingDir: *harness == "",
		preflight:          true,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "roles install: %v\n", err)
		os.Exit(1)
	}

	if printInstallReports(os.Stdout, reports, *dryRun) {
		os.Exit(1)
	}
}

// printInstallReports writes the per-path account and reports whether anything
// failed. Separated from RunRolesInstall so a test can read the bytes an operator
// would read, rather than a struct the operator never sees.
func printInstallReports(w io.Writer, reports []installReport, dryRun bool) (failed bool) {
	verb := "wrote"
	if dryRun {
		verb = "would write"
	}

	total := 0
	for _, r := range reports {
		switch {
		case r.skipped != "":
			_, _ = fmt.Fprintf(w, "%s: skipped -- %s\n", r.harness, r.skipped)
			continue
		default:
			_, _ = fmt.Fprintf(w, "%s -> %s  [%s]\n", r.harness, r.dir, r.source)
		}
		for _, path := range r.written {
			_, _ = fmt.Fprintf(w, "  %s %s\n", verb, path)
			total++
		}
		if r.unchanged > 0 {
			_, _ = fmt.Fprintf(w, "  unchanged (already current): %d file(s)\n", r.unchanged)
		}
		if len(r.written) == 0 && r.unchanged == 0 && r.err == nil {
			_, _ = fmt.Fprintf(w, "  nothing to write\n")
		}
		if r.err != nil {
			failed = true
			_, _ = fmt.Fprintf(w, "  FAILED after %d file(s): %v\n", len(r.written), r.err)
		}
	}

	if dryRun {
		// ⚠️ SAYS WHAT IT DID NOT CHECK. A dry run lists every file the generator
		// would CONSIDER, not the subset whose bytes differ -- deciding that needs
		// the resolved models, which means running each harness's catalog
		// subprocess (up to catalogProbeTimeout each, and `opencode models` alone
		// was measured at 8.2s cold). Paying seconds and three logins to print four
		// paths is the wrong trade, but silently overstating "would write" against
		// a real run that reports "0 file(s) written" would undercut exactly the
		// honesty property the per-path output exists for. So it is stated.
		_, _ = fmt.Fprintf(w, "\n%d file(s) would be written. Nothing was written: --dry-run.\n"+
			"(Paths a real run would CONSIDER. It leaves any file whose contents already match, "+
			"so it may report fewer; --dry-run does not read the current files.)\n", total)
	} else {
		_, _ = fmt.Fprintf(w, "\n%d file(s) written.\n", total)
	}
	return failed
}

// codexInstalled reports whether the codex CLI is on PATH. Kept as the one
// harness-specific precondition beyond the table's own gates, because it is the
// gate aihub#655 put there deliberately and removing it would be a regression
// this change has no reason to make: with no codex binary nothing will ever read
// $CODEX_HOME, and a stale ~/.codex left behind by an uninstalled codex should not
// keep attracting writes.
//
// It is ADDITIVE to the directory-exists gate, never a replacement: `~/.codex`
// not existing still means "skip", which is what keeps an empty $HOME empty.
func codexInstalled() bool {
	_, err := exec.LookPath("codex")
	return err == nil
}
