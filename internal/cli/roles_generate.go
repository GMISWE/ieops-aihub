package cli

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/GMISWE/ieops-aihub/internal/config"
	"github.com/GMISWE/ieops-aihub/internal/roles"
)

// CatalogProbe reports whether a model is known to be available in a
// harness's local model catalog. nil means "no live check" -- ResolveModel
// still supports it generically (tests use it directly, and it is the
// zero-value probeForHarness would return for any future harness that has no
// catalog to check), but as of aihub#642's review-fix round, BOTH harnesses
// this package actually generates for in production validate every candidate
// against a real, live probe: codex via `codex debug models`, pi via `pi
// --list-models`. pi's probe is an escalation from an earlier "no live
// check" design -- pi used to write any configured model ID unvalidated,
// which meant a stale or typo'd model ID only surfaced as an opaque 401 at
// runtime instead of a build-time warning here. A probe error is always
// treated as "not available": generation never guesses a model ID, it only
// ever walks to the next candidate or falls through to the omit-and-warn
// fallback (AC7).
type CatalogProbe interface {
	HasModel(model string) (bool, error)
}

// codexCatalogProbe shells out to `codex debug models` (best-effort, at most
// once per process — the result cannot change mid-generation). If the codex
// CLI is missing, not authenticated, or the command fails for any reason,
// every model is conservatively treated as unavailable: never guess.
type codexCatalogProbe struct {
	once   sync.Once
	models map[string]bool
	err    error
}

func (p *codexCatalogProbe) load() {
	p.once.Do(func() {
		out, err := exec.Command("codex", "debug", "models").Output()
		if err != nil {
			p.err = fmt.Errorf("codex debug models: %w", err)
			return
		}
		models, err := parseCodexModelCatalog(out)
		if err != nil {
			p.err = err
			return
		}
		p.models = models
	})
}

func (p *codexCatalogProbe) HasModel(model string) (bool, error) {
	p.load()
	if p.err != nil {
		return false, p.err
	}
	return p.models[model], nil
}

// codexModelCatalog is the subset of `codex debug models`' real output shape
// this package cares about. The real command prints ONE line of JSON shaped
// {"models":[{"slug":"...", ...many other fields (context_window,
// model_messages, ...) this package never reads...}]} -- NOT one bare model
// slug per line, which is what this parser wrongly assumed before aihub#642's
// code_review caught it (the old code split the raw output on "\n" and
// treated every non-empty line as a slug, so it silently matched nothing
// against a single-line JSON blob and every codex candidate fell through to
// the omit-and-warn fallback). See testdata/codex_debug_models_sample.json
// for a trimmed-but-structurally-faithful real sample (captured live from
// `codex debug models` and trimmed to 3 of its real models, with long prose
// fields truncated but every key name, nesting level, and slug left intact).
type codexModelCatalog struct {
	Models []struct {
		Slug string `json:"slug"`
	} `json:"models"`
}

// parseCodexModelCatalog parses `codex debug models`' real single-line JSON
// output into a set of known model slugs. Extracted from codexCatalogProbe.load
// so it can be pinned directly against a real captured fixture in a test,
// independent of whether the codex CLI itself is installed.
func parseCodexModelCatalog(data []byte) (map[string]bool, error) {
	var catalog codexModelCatalog
	if err := json.Unmarshal(data, &catalog); err != nil {
		return nil, fmt.Errorf("parse codex debug models output as JSON: %w", err)
	}
	models := make(map[string]bool, len(catalog.Models))
	for _, m := range catalog.Models {
		if m.Slug != "" {
			models[m.Slug] = true
		}
	}
	return models, nil
}

// piCatalogProbe shells out to `pi --list-models` (best-effort, at most once
// per process — the result cannot change mid-generation). If the pi CLI is
// missing, not authenticated, or the command fails for any reason, every
// model is conservatively treated as unavailable: never guess. This closes
// an asymmetry code_review flagged: pi used to be the one harness that wrote
// a configured model ID straight into the rendered agent file with no
// validation at all, unlike codex.
type piCatalogProbe struct {
	once   sync.Once
	models map[string]bool
	err    error
}

func (p *piCatalogProbe) load() {
	p.once.Do(func() {
		out, err := exec.Command("pi", "--list-models").Output()
		if err != nil {
			p.err = fmt.Errorf("pi --list-models: %w", err)
			return
		}
		p.models = parsePiModelCatalog(out)
	})
}

func (p *piCatalogProbe) HasModel(model string) (bool, error) {
	p.load()
	if p.err != nil {
		return false, p.err
	}
	return p.models[model], nil
}

// parsePiModelCatalog parses `pi --list-models`' real output shape: a header
// row ("provider   model   context  max-out  thinking  images", aligned with
// runs of spaces, not tabs or commas) followed by one data row per
// provider+model pair. The SAME model slug legitimately repeats under
// multiple providers (e.g. "claude-fable-5" under both "anthropic" and
// "sub2api-anthropic" in testdata/pi_list_models_sample.txt, a real captured
// sample), so this returns a set, and duplicates collapse harmlessly. The
// header row is skipped by matching its literal field values ("provider",
// "model") rather than by line position, so a future reordering of pi's
// column layout would not silently get misparsed as a model entry.
func parsePiModelCatalog(out []byte) map[string]bool {
	models := make(map[string]bool)
	for _, line := range strings.Split(string(out), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		if fields[0] == "provider" && fields[1] == "model" {
			continue // header row
		}
		models[fields[1]] = true
	}
	return models
}

// opencodeCatalogProbe shells out to `opencode models` (best-effort, at most
// once per process — the result cannot change mid-generation). Unlike
// codex's single-line JSON blob or pi's aligned header+data-row table,
// opencode's real output is the simplest of the three: one bare
// "provider/model-id" slug per line, no header row, no other stdout noise
// (aihub#653 measurement, opencode 1.18.30: `opencode models` produced 122
// clean lines with nothing but a slug on each). If the opencode CLI is
// missing, not authenticated, or the command fails for any reason, every
// model is conservatively treated as unavailable: never guess.
type opencodeCatalogProbe struct {
	once   sync.Once
	models map[string]bool
	err    error
}

func (p *opencodeCatalogProbe) load() {
	p.once.Do(func() {
		out, err := exec.Command("opencode", "models").Output()
		if err != nil {
			p.err = fmt.Errorf("opencode models: %w", err)
			return
		}
		p.models = parseOpencodeModelCatalog(out)
	})
}

func (p *opencodeCatalogProbe) HasModel(model string) (bool, error) {
	p.load()
	if p.err != nil {
		return false, p.err
	}
	return p.models[model], nil
}

// parseOpencodeModelCatalog parses `opencode models`' real output shape: one
// bare "provider/model-id" slug per line, no header row and no other stdout
// noise (aihub#653 measurement). Blank lines are skipped; every non-blank
// line is trusted as a complete slug verbatim -- unlike pi's table there is
// no column structure to split on, and unlike codex's blob there is no JSON
// to unmarshal.
func parseOpencodeModelCatalog(out []byte) map[string]bool {
	models := make(map[string]bool)
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		models[line] = true
	}
	return models
}

// probeForHarness returns the real CatalogProbe generation uses in
// production for the given harness: a live codex probe for "codex", a live
// pi probe for "pi", a live opencode probe for "opencode", and nil for
// anything else.
func probeForHarness(harness string) CatalogProbe {
	switch harness {
	case "codex":
		return &codexCatalogProbe{}
	case "pi":
		return &piCatalogProbe{}
	case "opencode":
		return &opencodeCatalogProbe{}
	default:
		return nil
	}
}

// ResolveModel walks candidates (a tier's full candidate list, NOT yet
// filtered to one harness) in declaration order and returns the first whose
// Harness matches and whose Model resolves. With probe == nil (no harness
// probeForHarness returns in production takes this path; it exists so tests,
// and any future no-catalog harness, can exercise the unconditional-resolve
// case directly), the first matching-harness candidate resolves
// unconditionally. With a non-nil probe -- both pi and codex in production
// today -- a candidate resolves only if probe.HasModel reports true with no
// error; a probe error or a false is "does not resolve", never "assume yes".
// ok=false with model=="" means: the caller must omit the model field and
// emit the non-suppressible tier+harness warning (AC7).
func ResolveModel(candidates []config.RoleCandidate, harness string, probe CatalogProbe) (model string, ok bool) {
	for _, c := range candidates {
		if c.Harness != harness {
			continue
		}
		if probe == nil {
			return c.Model, true
		}
		avail, err := probe.HasModel(c.Model)
		if err != nil || !avail {
			continue
		}
		return c.Model, true
	}
	return "", false
}

// GenerateRoles renders and writes one harness's ("pi", "codex", or
// "opencode") agent files into outDir, resolving each role's tier against mc.Roles.Tiers
// (aihub#642 design decision #5). It returns an error rather than exiting the
// process on any failure -- this is deliberately reusable from a context that
// must never crash the host (cmd/polyforge/main.go's serve startup path,
// which calls this after config.EnsureMachineConfig() and only logs a
// warning on error; see RunRolesGenerate below for the CLI-exits-on-error
// wrapper used by `polyforge roles generate`).
//
// A tier with no resolvable candidate for this harness is not an error: the
// generated file for that role simply has no model field, and a loud,
// non-suppressible warning naming the tier and harness is printed to stderr
// (AC7) -- this warning cannot be silenced by any flag or caller, unlike the
// returned error which callers may choose to just log.
func GenerateRoles(mc *config.MachineConfig, harness, outDir string) error {
	return GenerateRolesWithPreset(mc, harness, outDir, "")
}

// GenerateRolesWithPreset is GenerateRoles with an explicit preset override
// (`polyforge roles generate <harness> --out <dir> --preset=<name>`), resolved
// through config.MachineConfig.ResolveTiers so this path and every other reader
// of the tier table agree about which table is in force (aihub#673).
//
// An empty override means "use this machine's configured selection", which is
// what GenerateRoles passes and what the serve-startup path wants.
func GenerateRolesWithPreset(mc *config.MachineConfig, harness, outDir, preset string) error {
	return generateRoles(mc, harness, outDir, probeForHarness(harness), preset)
}

func generateRoles(mc *config.MachineConfig, harness, outDir string, probe CatalogProbe, preset string) error {
	if harness != "pi" && harness != "codex" && harness != "opencode" {
		return fmt.Errorf("unsupported harness %q for roles generate (must be \"pi\", \"codex\", or \"opencode\")", harness)
	}

	roleList, err := roles.LoadRoles()
	if err != nil {
		return fmt.Errorf("load roles: %w", err)
	}

	// 🔴 Resolved, never read straight off mc.Roles.Tiers. Before aihub#673 this
	// line was `tiers = mc.Roles.Tiers`, which meant a preset selected anywhere
	// else on this machine would not reach the files this function writes. See
	// config.MachineConfig.ResolveTiers for why one resolver is the whole point.
	tiers, tierSource, err := mc.ResolveTiers(preset)
	if err != nil {
		return err
	}
	// Provenance on stderr, not stdout: stdout carries the subcommand's own
	// success line, and an operator comparing two machines needs to see WHICH
	// table produced these files, not merely that some table did.
	fmt.Fprintf(os.Stderr, "polyforge: roles generate %s: tier table from %s\n", harness, tierSource)

	resolved := make(map[string]string, len(roleList))
	for _, r := range roleList {
		model, ok := ResolveModel(tiers[r.Tier], harness, probe)
		if !ok {
			// Loud and non-suppressible by design (AC7): no flag on this
			// subcommand, nor any caller of GenerateRoles, gates this line.
			fmt.Fprintf(os.Stderr,
				"polyforge: WARNING: no resolvable %s model candidate for tier %q (role %q) -- "+
					"generated agent file will have NO model field and will inherit the caller's "+
					"default model instead. Configure ~/.polyforge/config.toml [roles.tiers] to fix this.\n",
				harness, r.Tier, r.Name)
			continue // resolved[r.Name] stays unset, read back as "".
		}
		resolved[r.Name] = model
	}

	var rendered map[string]string
	switch harness {
	case "pi":
		rendered, err = roles.RenderPiAgentFiles(roleList, resolved)
	case "codex":
		rendered, err = roles.RenderCodexAgentFiles(roleList, resolved)
	case "opencode":
		rendered, err = roles.RenderOpencodeAgentFiles(roleList, resolved)
	}
	if err != nil {
		return fmt.Errorf("render %s agent files: %w", harness, err)
	}

	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return fmt.Errorf("mkdir %s: %w", outDir, err)
	}
	names := make([]string, 0, len(rendered))
	for name := range rendered {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		path := filepath.Join(outDir, name)
		if err := os.WriteFile(path, []byte(rendered[name]), 0o644); err != nil {
			return fmt.Errorf("write %s: %w", path, err)
		}
	}
	return nil
}

// RunRolesGenerate is the `polyforge roles generate <pi|codex|opencode> --out
// <dir>` CLI subcommand (aihub#642 plan steps 10/11; opencode added by
// aihub#653), dispatched from cmd/polyforge/main.go's runCLI. Unlike
// GenerateRoles, this DOES exit the process on error -- matching every other
// Run* subcommand in this package -- because a human or install script
// invoking this directly wants a non-zero exit code on failure, not a
// silently-degraded install.
//
// opencode generation is deliberately install-script-invoked ONLY: unlike
// codex (see DefaultCodexAgentsDir below), there is no auto-regeneration hook
// for opencode in cmd/polyforge/main.go's serve startup path -- that call
// site is aihub#654's locked territory, and opencode's install script
// (plugins/polyforge/opencode/install.sh) computes its own --out target
// directly in shell rather than through a Go-side Default*Dir helper, so no
// such helper is added here. Wiring an auto-regen hook into serve startup, if
// ever wanted, is left to a follow-up work item.
func RunRolesGenerate(mc *config.MachineConfig, args []string) {
	usage := "usage: polyforge roles generate <pi|codex|opencode> --out <dir> [--preset=<name>]"
	if len(args) < 1 || args[0] != "generate" {
		fmt.Fprintln(os.Stderr, usage)
		os.Exit(1)
	}
	args = args[1:]
	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, usage)
		os.Exit(1)
	}
	harness := args[0]
	if harness != "pi" && harness != "codex" && harness != "opencode" {
		fmt.Fprintf(os.Stderr, "roles generate: unsupported harness %q (must be \"pi\", \"codex\", or \"opencode\")\n%s\n", harness, usage)
		os.Exit(1)
	}

	fs := flag.NewFlagSet("roles generate "+harness, flag.ExitOnError)
	outDir := fs.String("out", "", "output directory for the generated agent files")
	// Overrides ~/.polyforge/config.toml [roles] preset for this invocation
	// only; an unknown name is refused by ResolveTiers rather than silently
	// falling back to [roles.tiers] (aihub#673).
	preset := fs.String("preset", "", "named tier-table snapshot to generate from (default: the machine's [roles] preset)")
	_ = fs.Parse(args[1:])
	if *outDir == "" {
		fmt.Fprintln(os.Stderr, "roles generate: --out is required\n"+usage)
		os.Exit(1)
	}

	if err := GenerateRolesWithPreset(mc, harness, *outDir, *preset); err != nil {
		fmt.Fprintf(os.Stderr, "roles generate: %v\n", err)
		os.Exit(1)
	}
	_, _ = fmt.Fprintf(os.Stdout, "roles generate: wrote %s agent files to %s\n", harness, *outDir)
}

// DefaultCodexAgentsDir returns plugins/polyforge/.codex-plugin/agents,
// resolved from the CURRENT WORKING DIRECTORY -- deliberately NOT from this
// binary's own executable location (aihub#642 design decision #3 / C7): the
// running polyforge binary is downloaded independently of, and drifts from,
// the checked-out plugin commit (bin/polyforge-mcp.sh's daily auto-update
// check), so a path derived from os.Executable() would silently point at the
// wrong plugin tree the moment those two disagree -- exactly the failure mode
// the spec calls out for the embed-source decision, which applies equally
// here. cwd is trustworthy instead: both .claude-plugin/plugin.json's
// mcpServers.polyforge.cwd ("${CLAUDE_PLUGIN_ROOT}") and
// .codex-plugin/mcp.json's (".", resolved against the same base as its own
// sibling "./bin/polyforge-mcp.sh" command path) set the MCP server's cwd to
// the plugin root, and bin/polyforge-mcp.sh execs the polyforge binary
// without ever changing directory -- so a `serve` process launched by either
// harness's own MCP config already has cwd == the plugin root.
//
// ok=false (generation must be skipped, never guessed at) when
// cwd/.codex-plugin does not exist -- e.g. a bare `go run ./cmd/polyforge
// serve` from the repo root during local development, or a pi-launched serve
// process (pi/mcp.json sets no cwd at all, so pi's serve process's cwd is
// whatever pi itself happens to use). This makes the check self-verifying
// rather than harness-conditioned: it fires exactly when the invocation
// context actually looks like the codex plugin tree, and quietly no-ops
// everywhere else.
//
// Nothing in plugins/polyforge/.codex-plugin/plugin.json declares this
// directory: an earlier revision added an "agents": "./.codex-plugin/agents/"
// manifest key here, but that key is not part of any documented codex plugin
// manifest schema (checked against the public agent-plugins.org 1.0.0 schema
// and codex's own plugin docs, aihub#642 code_review) -- codex discovers
// subagents on its own, from standalone TOML files under ~/.codex/agents/ or
// <project>/.codex/agents/ (fields name/description/developer_instructions),
// a mechanism with no manifest declaration at all, not a directory a plugin
// points at. The unverifiable key was removed rather than kept on the chance
// codex silently honours it; wiring generation into a place codex's own
// discovery mechanism actually reads is out of this wi's scope and is left
// for a follow-up work item.
func DefaultCodexAgentsDir() (string, bool) {
	cwd, err := os.Getwd()
	if err != nil {
		return "", false
	}
	codexDir := filepath.Join(cwd, ".codex-plugin")
	fi, err := os.Stat(codexDir)
	if err != nil || !fi.IsDir() {
		return "", false
	}
	return filepath.Join(codexDir, "agents"), true
}
