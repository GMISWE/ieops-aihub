package cli

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
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
	once    sync.Once
	catalog piModelCatalog
	err     error

	mu     sync.Mutex
	warned map[string]bool
}

func (p *piCatalogProbe) load() {
	p.once.Do(func() {
		out, err := exec.Command("pi", "--list-models").Output()
		if err != nil {
			p.err = fmt.Errorf("pi --list-models: %w", err)
			return
		}
		p.catalog = parsePiModelCatalog(out)
	})
}

// HasModel resolves a configured pi model identifier (see
// piModelCatalog.Resolve) and, when the identifier is refused for a reason the
// operator can act on, explains it once per identifier. Deduplicated because
// one configured candidate is checked once per role, and five identical lines
// read as five problems.
func (p *piCatalogProbe) HasModel(model string) (bool, error) {
	p.load()
	if p.err != nil {
		return false, p.err
	}
	ok, advice := p.catalog.Resolve(model)
	if advice != "" {
		p.mu.Lock()
		if p.warned == nil {
			p.warned = make(map[string]bool)
		}
		first := !p.warned[model]
		p.warned[model] = true
		p.mu.Unlock()
		if first {
			fmt.Fprintf(os.Stderr, "polyforge: WARNING: pi model %q: %s\n", model, advice)
		}
	}
	return ok, nil
}

// piModelCatalog is the parsed view of `pi --list-models`. It keeps the
// PROVIDER column, which the pre-aihub#676 parser discarded.
//
// 🔴 Discarding it inverted the probe's verdict against pi's real behaviour.
// Measured live on 2026-09-14 (pi 0.85.1), with `pi -p --model <X> ...` -- the
// path that actually dispatches an agent, not a readiness helper:
//
//   - "sub2api-anthropic/claude-fable-5" (fully qualified)  -> ran, replied "OK"
//   - "claude-fable-5" (bare)                               -> hard error:
//     `Model "claude-fable-5" is ambiguous across providers: anthropic/...,
//     cloudflare-ai-gateway/..., github-copilot/..., opencode/...,
//     sub2api-anthropic/... Use --provider or provider/model.`
//   - "fable" (a fuzzy prefix)                              -> matched
//     amazon-bedrock, a provider `pi --list-models` does not even print
//
// The old parser keyed its set on the bare model column, so it answered FALSE
// for the only form that runs and TRUE for forms that do not. aihub#642's
// design had measured the same thing from the other side
// (design_notes_addendum_2026_09_13.model_identifier_portability_MEASURED) and
// concluded the full provider prefix is mandatory; the probe contradicted it.
//
// 🔴 SECOND MEASURED FACT, and the one that decides how bare ids are treated:
// pi's ambiguity check spans its BUILT-IN CATALOG, not the providers this table
// prints and not even the providers that are authenticated. `pi --list-models`
// shows 2 providers for claude-fable-5; pi's own error for that id names 5 --
// and `pi auth check --provider <p>` says 3 of those 5 (cloudflare-ai-gateway,
// github-copilot, opencode) have `credentials_not_configured`. The same
// mechanism is why the fuzzy prefix above reached amazon-bedrock, which is
// neither listed nor authenticated.
//
// ⇒ This table is a sound oracle for "does this provider/model pair exist" and
// NO oracle at all for "is this bare id unambiguous". Counting providers here
// cannot decide whether pi will accept a bare id, so Resolve does not try:
// every bare id is refused, and the operator is told which qualified form to
// write. That is not a guess in the safe direction, it is the only claim this
// data supports.
//
// ⚠️ INSTRUMENT WARNING, because this cost a review round and will be
// re-derived otherwise: `pi auth check --model <bare-id>` is NOT a way to test
// this. Given a bare id it does not run pi's model resolver -- it echoes
// `{"status":"invalid","provider":"grok-4.6",...}`, i.e. it parsed the MODEL as
// a PROVIDER name. Sweeping it over this box's catalog "showed" 15 of 18 bare
// ids unresolvable while `pi -p --model grok-4.6` ran that very id and replied
// OK. Use `pi -p` (it makes a billed call) or nothing.
type piModelCatalog struct {
	// Qualified is the set of "provider/model" pairs -- the form pi resolves
	// deterministically, and the only form generation ever writes.
	Qualified map[string]bool
	// ProvidersByModel maps a bare model id to every provider offering it, in
	// first-seen order, so a refusal can name the qualified alternatives.
	ProvidersByModel map[string][]string
}

// Resolve reports whether a configured identifier is usable, and returns
// non-empty advice when the operator should be told why it is not.
//
//   - "provider/model": exact membership. No advice; this is the correct form.
//   - a bare model id: NEVER resolvable, whatever the catalog shows, with
//     advice naming the qualified form(s) to write instead. See the second
//     measured fact above for why a count of providers here proves nothing.
//   - anything else: not resolvable, no advice (the generic
//     no-resolvable-candidate warning already covers it).
//
// Refusing rather than writing-and-warning is deliberate, and it is the policy
// this repo already committed to: aihub#642 AC7 says an unresolvable candidate
// must degrade to "no model field + a loud warning + inherit the caller's
// model", because that is observable, where a plausible-looking id that fails
// at dispatch is not. Writing a bare id would also contradict this change's own
// config.ValidateCandidates, which reports one as a problem -- the generator
// would warn twice and then emit it anyway.
func (c piModelCatalog) Resolve(model string) (ok bool, advice string) {
	if strings.Contains(model, "/") {
		return c.Qualified[model], ""
	}
	providers := c.ProvidersByModel[model]
	if len(providers) == 0 {
		return false, ""
	}
	qualified := make([]string, 0, len(providers))
	for _, p := range providers {
		qualified = append(qualified, p+"/"+model)
	}
	return false, fmt.Sprintf(
		"REFUSED because it is a BARE model id. pi decides ambiguity against its whole built-in "+
			"catalog, not against `pi --list-models`, so no count of listed providers can tell whether "+
			"pi would accept this (aihub#676, measured). Write the fully qualified form in "+
			"~/.polyforge/config.toml -- this machine's catalog offers: %s",
		strings.Join(qualified, ", "))
}

// parsePiModelCatalog parses `pi --list-models`' real output shape: a header
// row ("provider   model   context  max-out  thinking  images", aligned with
// runs of spaces, not tabs or commas) followed by one data row per
// provider+model pair. The header row is skipped by matching its literal field
// values ("provider", "model") rather than by line position, so a future
// reordering of pi's column layout would not silently get misparsed as a model
// entry.
//
// Both columns are kept: see piModelCatalog for the measurement that made
// keeping the provider column mandatory.
func parsePiModelCatalog(out []byte) piModelCatalog {
	c := piModelCatalog{
		Qualified:        make(map[string]bool),
		ProvidersByModel: make(map[string][]string),
	}
	for _, line := range strings.Split(string(out), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		provider, model := fields[0], fields[1]
		if provider == "provider" && model == "model" {
			continue // header row
		}
		qualified := provider + "/" + model
		if c.Qualified[qualified] {
			continue // exact duplicate row: collapse, never double-count
		}
		c.Qualified[qualified] = true
		c.ProvidersByModel[model] = append(c.ProvidersByModel[model], provider)
	}
	return c
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
	model, ok, _ = ResolveModelWithCause(candidates, harness, probe)
	return model, ok
}

// ResolveModelWithCause is ResolveModel plus the diagnosis of a failure. It is
// a separate function rather than a third return value on ResolveModel because
// `polyforge drain` also calls ResolveModel and is another work item's locked
// territory; widening the shared signature would have dragged an unrelated file
// into this change for no behavioural gain.
//
// Callers that will PRINT something on failure want this one. Callers that only
// branch on ok want ResolveModel.
func ResolveModelWithCause(candidates []config.RoleCandidate, harness string, probe CatalogProbe) (model string, ok bool, why ResolveFailure) {
	for _, c := range candidates {
		if c.Harness != harness {
			continue
		}
		why.Candidates = append(why.Candidates, c.Model)
		if probe == nil {
			return c.Model, true, ResolveFailure{}
		}
		avail, err := probe.HasModel(c.Model)
		if err != nil {
			// Recorded instead of dropped. Dropping it is what made a missing
			// or unauthenticated CLI indistinguishable from a bad config --
			// see ResolveFailure.ProbeErr.
			if why.ProbeErr == nil {
				why.ProbeErr = err
			}
			continue
		}
		if !avail {
			continue
		}
		return c.Model, true, ResolveFailure{}
	}
	return "", false, why
}

// ResolveFailure explains WHY ResolveModel found nothing, so the caller's
// warning can name the actual cause. It is meaningful only when ok is false.
//
// 🔴 This type exists because the warning was wrong in the one case an operator
// most needs it to be right. ResolveModel used to read `avail, err :=
// probe.HasModel(...); if err != nil || !avail { continue }` -- the probe error
// was discarded -- and the caller then printed "Configure
// ~/.polyforge/config.toml [roles.tiers] to fix this". So `pi` simply not being
// installed, or not being logged in, was reported as a configuration mistake,
// and the operator's next move was to edit a file that was never wrong
// (aihub#676 finding 5).
type ResolveFailure struct {
	// Candidates lists the models that named this harness, in declaration
	// order. Empty means the tier had no candidate for this harness AT ALL,
	// which is a different problem from "the candidates did not resolve".
	Candidates []string
	// ProbeErr is the first error the catalog probe returned, if any. Non-nil
	// means the harness's model catalog could not be READ -- the CLI is
	// missing, not authenticated, or failed. Nothing about the config is
	// implicated.
	ProbeErr error
}

// UnresolvedModelWarning builds the loud, non-suppressible AC7 warning for a
// tier whose model did not resolve. One builder, two call sites
// (internal/cli.generateRoles and cmd/polyforge's generateCodexProfiles), so
// the two cannot drift -- they were byte-divergent copies of the same sentence
// before aihub#676.
//
// tierSource is the provenance string config.MachineConfig.ResolveTiers
// returned. Naming it is the fix for the second half of aihub#676 finding 5:
// the message used to say "[roles.tiers]" unconditionally, which is the wrong
// file section whenever a preset is in force -- the operator would edit a table
// that is not being read.
func UnresolvedModelWarning(harness, tier, role, tierSource string, why ResolveFailure) string {
	const tail = " -- the generated file for this role will have NO model field and will inherit the " +
		"caller's default model instead."
	switch {
	case why.ProbeErr != nil:
		return fmt.Sprintf(
			"polyforge: WARNING: could not read %s's model catalog (%v) for tier %q (role %q)%s\n"+
				"  This is NOT a configuration problem: install %s and make sure it is authenticated. "+
				"The tier table in use (%s) was never consulted.\n",
			harness, why.ProbeErr, tier, role, tail, harness, tierSource)
	case len(why.Candidates) == 0:
		// The example's model shape is per-harness on purpose: codex slugs carry
		// no provider prefix, and this same builder serves the codex path from
		// cmd/polyforge (aihub#676 review). A shared "<provider>/<model>" example
		// contradicted this binary's own --help and config footer on every
		// `polyforge serve` boot.
		example := "\"<provider>/<model>\""
		if harness == "codex" {
			example = "\"<codex-slug>\""
		}
		return fmt.Sprintf(
			"polyforge: WARNING: tier %q (role %q) has no candidate naming harness %q%s\n"+
				"  Add one to the tier table in use (%s), e.g. %s = [{ harness = %q, model = %s }].\n",
			tier, role, harness, tail, tierSource, tier, harness, example)
	default:
		return fmt.Sprintf(
			"polyforge: WARNING: no resolvable %s model candidate for tier %q (role %q)%s\n"+
				"  Every candidate was checked against %s's live catalog and none is present: %s. "+
				"Fix the tier table in use (%s).\n",
			harness, tier, role, tail, harness, strings.Join(why.Candidates, ", "), tierSource)
	}
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

	// A typo'd harness string used to be invisible: nothing in the repo
	// validated it, so `harness = "claude"` or a stray trailing space simply
	// never matched any candidate and surfaced as the generic
	// no-resolvable-candidate warning below, pointing the operator at the model
	// rather than at the typo (aihub#676 finding 6).
	for _, problem := range config.ValidateCandidates(tiers) {
		fmt.Fprintf(os.Stderr, "polyforge: WARNING: %s (%s)\n", problem, tierSource)
	}

	// aihub#676 finding 2: say out loud that ~/.polyforge/roles/ is not read,
	// rather than letting an operator's edits there have no effect forever.
	if path, present := config.UnreadRolesOverrideDir(); present {
		fmt.Fprint(os.Stderr, config.RolesOverrideIgnoredWarning(path))
	}

	resolved := make(map[string]string, len(roleList))
	for _, r := range roleList {
		model, ok, why := ResolveModelWithCause(tiers[r.Tier], harness, probe)
		if !ok {
			// Loud and non-suppressible by design (AC7): no flag on this
			// subcommand, nor any caller of GenerateRoles, gates this line.
			fmt.Fprint(os.Stderr, UnresolvedModelWarning(harness, r.Tier, r.Name, tierSource, why))
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
	warnOrphanAgentFiles(os.Stderr, outDir, harness, rendered)
	return nil
}

// warnOrphanAgentFiles names files in outDir that this generator would have
// written for a role that no longer exists. It WARNS and never deletes: outDir
// is an argument, so it may be a directory the operator also keeps their own
// files in ($CODEX_HOME is literally that), and deleting by filename pattern
// there would be the generator quietly removing something it never made.
//
// aihub#676 finding 4 named this shape: generateRoles writes files but never
// reconciles, so renaming or deleting a role leaves a stale agent file that
// stays dispatchable. The equivalent on the Claude Code side is a HARD failure
// instead (those files are committed, so a gate can assert on them) -- see
// internal/roles.TestCCStalenessGateRejectsOrphans.
func warnOrphanAgentFiles(w io.Writer, outDir, harness string, rendered map[string]string) {
	// The naming rule this generator owns, per harness: same stem shape as the
	// files it just wrote. Derived from the rendered names themselves rather
	// than re-hardcoded, so the two cannot disagree.
	var prefix, suffix string
	for name := range rendered {
		i := strings.IndexByte(name, '-')
		j := strings.IndexByte(name, '.')
		if i < 0 || j < 0 || j < i {
			return // unexpected shape: say nothing rather than guess
		}
		prefix, suffix = name[:i+1], name[j:]
		break
	}
	if prefix == "" {
		return
	}

	entries, err := os.ReadDir(outDir)
	if err != nil {
		return // best-effort: the write loop above already reported real errors
	}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasPrefix(name, prefix) || !strings.HasSuffix(name, suffix) {
			continue
		}
		if _, current := rendered[name]; current {
			continue
		}
		_, _ = fmt.Fprintf(w,
			"polyforge: WARNING: %s looks like a %s agent file for a role that no longer exists. "+
				"It was NOT removed (this directory is yours, not this generator's), but nothing "+
				"can dispatch to it and it may still be loaded by %s. Delete it if you renamed or "+
				"removed a role.\n",
			filepath.Join(outDir, name), harness, harness)
	}
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

	if harness == "codex" {
		// aihub#676 finding 6: this verb still works, but what it produces is
		// not loadable by codex, and the help text used to recommend it without
		// saying so. aihub#655 established that this codex version has no
		// subagent-file auto-discovery at all, so a step-<role>.toml in ANY
		// directory is inert -- the loadable shape is the config profile the
		// serve-startup path writes into $CODEX_HOME. Said at the call site,
		// once, where an operator who typed the command will actually read it.
		fmt.Fprintf(os.Stderr,
			"polyforge: NOTE: codex does not auto-discover agent files from any directory "+
				"(aihub#655, live-verified), so the step-<role>.toml files written to %s are for "+
				"inspection only -- nothing loads them. The files codex actually reads are the "+
				"config profiles `polyforge serve` writes to $CODEX_HOME on every boot "+
				"($CODEX_HOME/step-<role>.config.toml, selected with `codex -p step-<role>`).\n",
			*outDir)
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
