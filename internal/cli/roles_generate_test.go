package cli

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/GMISWE/ieops-aihub/internal/config"
)

// fakeProbe is a CatalogProbe test double: models present in the set
// resolve, everything else does not. Used in place of a real live probe
// (codexCatalogProbe / piCatalogProbe) so tests never depend on the codex or
// pi CLI being installed. A bare untyped nil CatalogProbe (not a nil
// *fakeProbe) is used separately, in TestResolveModel, to exercise
// ResolveModel's own "no live check" mode -- production no longer takes that
// path for either harness, but ResolveModel still supports it generically.
type fakeProbe struct {
	available map[string]bool
}

func (p *fakeProbe) HasModel(model string) (bool, error) {
	return p.available[model], nil
}

// catalogProbe is a CatalogProbe backed by a REAL parsed `pi --list-models`
// catalog, so a test can exercise pi's actual resolution policy (provider/model
// only) instead of a hand-set boolean. fakeProbe cannot do that: it answers
// whatever the test seeded, which is right for testing the plumbing and wrong
// for testing the policy (aihub#676 review B1 -- the first version of
// TestGenerateRoles_WarnsBareModelIDAndUnknownHarness used fakeProbe and so
// asserted a bare id was refused while the fake happily accepted it).
type catalogProbe struct{ c piModelCatalog }

func (p catalogProbe) HasModel(model string) (bool, error) {
	ok, _ := p.c.Resolve(model)
	return ok, nil
}

func realPiCatalogProbe(t *testing.T) catalogProbe {
	t.Helper()
	data, err := os.ReadFile("testdata/pi_list_models_sample.txt")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	return catalogProbe{c: parsePiModelCatalog(data)}
}

// erroringProbe always returns an error, proving a probe error is treated as
// "not available" (never guessed) rather than propagated as a generation
// failure.
type erroringProbe struct{}

func (erroringProbe) HasModel(model string) (bool, error) {
	return false, os.ErrClosed // any non-nil error
}

func TestResolveModel(t *testing.T) {
	candidates := []config.RoleCandidate{
		{Harness: "pi", Model: "pi-model-a"},
		{Harness: "codex", Model: "codex-model-a"},
		{Harness: "codex", Model: "codex-model-b"},
	}

	t.Run("nil probe: first matching-harness candidate wins unconditionally (no live check)", func(t *testing.T) {
		// A nil probe is no longer what production probeForHarness returns for
		// either pi or codex (both validate against a live catalog now) -- this
		// exercises ResolveModel's own nil-probe support directly, independent
		// of which harness happens to be passed in.
		model, ok := ResolveModel(candidates, "pi", nil)
		if !ok || model != "pi-model-a" {
			t.Fatalf("ResolveModel(pi, nil probe) = (%q, %v), want (pi-model-a, true)", model, ok)
		}
	})

	t.Run("codex walks candidates in order, skipping ones the probe rejects", func(t *testing.T) {
		probe := &fakeProbe{available: map[string]bool{"codex-model-b": true}}
		model, ok := ResolveModel(candidates, "codex", probe)
		if !ok || model != "codex-model-b" {
			t.Fatalf("ResolveModel(codex, probe accepting only -b) = (%q, %v), want (codex-model-b, true)", model, ok)
		}
	})

	t.Run("codex with no candidate accepted by the probe -> unresolved, never guessed", func(t *testing.T) {
		probe := &fakeProbe{available: map[string]bool{}}
		model, ok := ResolveModel(candidates, "codex", probe)
		if ok || model != "" {
			t.Fatalf("ResolveModel(codex, empty-accepting probe) = (%q, %v), want (\"\", false)", model, ok)
		}
	})

	t.Run("a probe error is treated as not-available, not propagated", func(t *testing.T) {
		model, ok := ResolveModel(candidates, "codex", erroringProbe{})
		if ok || model != "" {
			t.Fatalf("ResolveModel(codex, erroringProbe) = (%q, %v), want (\"\", false)", model, ok)
		}
	})

	t.Run("no candidate for the harness at all -> unresolved", func(t *testing.T) {
		model, ok := ResolveModel(candidates, "opencode", nil)
		if ok || model != "" {
			t.Fatalf("ResolveModel(opencode, ...) = (%q, %v), want (\"\", false)", model, ok)
		}
	})
}

// TestGenerateRoles_PiWritesAllFiles exercises the full pipeline end to end
// for pi: LoadRoles -> ResolveModel (fake probe standing in for pi's live
// `pi --list-models` catalog check) -> RenderPiAgentFiles -> write to disk.
// Asserts all 5 pf-<role>.md files land, one resolved model makes it into
// frontmatter verbatim, and the read-only/write-capable split still holds
// post-generation (explorer/reviewer carry `tools:`, the rest do not).
//
// Deliberately uses an explicit fakeProbe rather than probeForHarness("pi"):
// pi generation now validates against a real, live `pi --list-models` call
// (aihub#642 code_review escalation), and this test must stay green on any
// machine/CI runner regardless of whether the pi CLI is installed there.
func TestGenerateRoles_PiWritesAllFiles(t *testing.T) {
	dir := t.TempDir()
	mc := &config.MachineConfig{
		Roles: &config.MachineRoles{
			Tiers: map[string][]config.RoleCandidate{
				// Qualified, because that is the only form generation writes
				// since aihub#676; a bare id here would be refused by the real
				// probe and this test would be asserting an impossible state.
				"default": {{Harness: "pi", Model: "anthropic/claude-sonnet-4-5"}},
			},
		},
	}
	probe := realPiCatalogProbe(t)

	if err := generateRoles(mc, "pi", dir, probe, ""); err != nil {
		t.Fatalf("generateRoles(pi) error: %v", err)
	}

	for _, name := range []string{"pf-executor.md", "pf-operator.md", "pf-explorer.md", "pf-reviewer.md", "pf-designer.md"} {
		path := filepath.Join(dir, name)
		data, err := os.ReadFile(path)
		if err != nil {
			t.Errorf("%s: not written: %v", name, err)
			continue
		}
		content := string(data)
		if name == "pf-executor.md" {
			if !strings.Contains(content, "model: anthropic/claude-sonnet-4-5") {
				t.Errorf("pf-executor.md: expected resolved model in frontmatter, got:\n%s", content)
			}
		}
	}

	explorer, _ := os.ReadFile(filepath.Join(dir, "pf-explorer.md"))
	if !strings.Contains(string(explorer), "tools:") {
		t.Errorf("pf-explorer.md (read_only role) should carry a tools: allowlist")
	}
	operator, _ := os.ReadFile(filepath.Join(dir, "pf-operator.md"))
	if strings.Contains(string(operator), "tools:") {
		t.Errorf("pf-operator.md (write-capable role) should NOT carry a tools: line")
	}
}

// TestGenerateRoles_CodexValidatesAgainstProbe proves AC6's differentiation
// end to end: codex generation only accepts a candidate the (fake) catalog
// probe reports as present, and rejects one it does not -- unlike pi, which
// TestGenerateRoles_PiWritesAllFiles above shows accepts its only candidate
// unconditionally.
func TestGenerateRoles_CodexValidatesAgainstProbe(t *testing.T) {
	dir := t.TempDir()
	mc := &config.MachineConfig{
		Roles: &config.MachineRoles{
			Tiers: map[string][]config.RoleCandidate{
				"default": {{Harness: "codex", Model: "not-in-catalog"}},
			},
		},
	}
	probe := &fakeProbe{available: map[string]bool{"gpt-5-codex": true}}

	if err := generateRoles(mc, "codex", dir, probe, ""); err != nil {
		t.Fatalf("generateRoles(codex) error: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(dir, "step-executor.toml"))
	if err != nil {
		t.Fatalf("step-executor.toml not written: %v", err)
	}
	if strings.Contains(string(data), "model =") {
		t.Errorf("step-executor.toml: candidate not in the probe's catalog must NOT be emitted, got:\n%s", data)
	}
}

// TestGenerateRoles_PiValidatesAgainstProbe mirrors
// TestGenerateRoles_CodexValidatesAgainstProbe above, but for pi: proves pi
// generation now validates every candidate against a live catalog probe too
// (aihub#642 code_review escalation) rather than writing whatever model ID
// was configured unconditionally. A candidate the probe does not report as
// present must NOT be written into the rendered pf-<role>.md's frontmatter.
func TestGenerateRoles_PiValidatesAgainstProbe(t *testing.T) {
	dir := t.TempDir()
	mc := &config.MachineConfig{
		Roles: &config.MachineRoles{
			Tiers: map[string][]config.RoleCandidate{
				"default": {{Harness: "pi", Model: "not-in-catalog"}},
			},
		},
	}
	probe := &fakeProbe{available: map[string]bool{"claude-sonnet-4-5": true}}

	if err := generateRoles(mc, "pi", dir, probe, ""); err != nil {
		t.Fatalf("generateRoles(pi) error: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(dir, "pf-executor.md"))
	if err != nil {
		t.Fatalf("pf-executor.md not written: %v", err)
	}
	if strings.Contains(string(data), "model:") {
		t.Errorf("pf-executor.md: candidate not in the probe's catalog must NOT be emitted, got:\n%s", data)
	}
}

// TestGenerateRoles_OpencodeWritesAllFiles mirrors
// TestGenerateRoles_PiWritesAllFiles above, but for opencode: exercises
// LoadRoles -> ResolveModel (fake probe standing in for opencode's live
// `opencode models` catalog check) -> RenderOpencodeAgentFiles -> write to
// disk. Asserts all 5 step-<role>.md files land (opencode has no `name:`
// frontmatter field -- the filename IS the agent's name, per
// https://opencode.ai/docs/agents/), one resolved model makes it into
// frontmatter verbatim, and the read-only/write-capable split holds
// post-generation: explorer/reviewer carry `permission:\n  edit: deny`, the
// other three carry no permission block at all (aihub#653 AC9).
func TestGenerateRoles_OpencodeWritesAllFiles(t *testing.T) {
	dir := t.TempDir()
	mc := &config.MachineConfig{
		Roles: &config.MachineRoles{
			Tiers: map[string][]config.RoleCandidate{
				"default": {{Harness: "opencode", Model: "anthropic/claude-sonnet-4-5"}},
			},
		},
	}
	probe := &fakeProbe{available: map[string]bool{"anthropic/claude-sonnet-4-5": true}}

	if err := generateRoles(mc, "opencode", dir, probe, ""); err != nil {
		t.Fatalf("generateRoles(opencode) error: %v", err)
	}

	for _, name := range []string{"step-executor.md", "step-operator.md", "step-explorer.md", "step-reviewer.md", "step-designer.md"} {
		path := filepath.Join(dir, name)
		data, err := os.ReadFile(path)
		if err != nil {
			t.Errorf("%s: not written: %v", name, err)
			continue
		}
		content := string(data)
		if name == "step-executor.md" {
			if !strings.Contains(content, "model: anthropic/claude-sonnet-4-5") {
				t.Errorf("step-executor.md: expected resolved model in frontmatter, got:\n%s", content)
			}
		}
	}

	explorer, _ := os.ReadFile(filepath.Join(dir, "step-explorer.md"))
	if !strings.Contains(string(explorer), "permission:\n  edit: deny") {
		t.Errorf("step-explorer.md (read_only role) should carry `permission:\\n  edit: deny`, got:\n%s", explorer)
	}
	operator, _ := os.ReadFile(filepath.Join(dir, "step-operator.md"))
	if strings.Contains(string(operator), "permission:") {
		t.Errorf("step-operator.md (write-capable role) should NOT carry a permission: block, got:\n%s", operator)
	}
}

// TestGenerateRoles_OpencodeValidatesAgainstProbe mirrors
// TestGenerateRoles_CodexValidatesAgainstProbe above, but for opencode: proves
// opencode generation validates every candidate against a live `opencode
// models` catalog probe, never writing a model ID the probe does not report
// as present.
func TestGenerateRoles_OpencodeValidatesAgainstProbe(t *testing.T) {
	dir := t.TempDir()
	mc := &config.MachineConfig{
		Roles: &config.MachineRoles{
			Tiers: map[string][]config.RoleCandidate{
				"default": {{Harness: "opencode", Model: "not-in-catalog"}},
			},
		},
	}
	probe := &fakeProbe{available: map[string]bool{"anthropic/claude-sonnet-4-5": true}}

	if err := generateRoles(mc, "opencode", dir, probe, ""); err != nil {
		t.Fatalf("generateRoles(opencode) error: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(dir, "step-executor.md"))
	if err != nil {
		t.Fatalf("step-executor.md not written: %v", err)
	}
	if strings.Contains(string(data), "model:") {
		t.Errorf("step-executor.md: candidate not in the probe's catalog must NOT be emitted, got:\n%s", data)
	}
}

// captureStderr swaps os.Stderr for a pipe for the duration of fn and returns
// everything written to it. fmt.Fprintf(os.Stderr, ...) reads the os.Stderr
// variable at call time, so reassigning it here is enough to intercept every
// warning generateRoles/ResolveModel print -- no subprocess, no real fd 2
// redirection needed.
func captureStderr(t *testing.T, fn func()) string {
	t.Helper()
	orig := os.Stderr
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	os.Stderr = w
	fn()
	_ = w.Close()
	os.Stderr = orig

	var buf bytes.Buffer
	if _, err := io.Copy(&buf, r); err != nil {
		t.Fatalf("read captured stderr: %v", err)
	}
	_ = r.Close()
	return buf.String()
}

// TestGenerateRoles_UnresolvableCandidateFallback pins aihub#642 AC7/plan step
// 12 end to end, on BOTH generation paths (pi and codex, both now
// probe-validated per aihub#642's code_review escalation): when a tier has no
// resolvable candidate for the harness being generated, (1) the written
// agent file must have NO model field, and (2) generateRoles' stderr warning
// must be loud and must name BOTH the affected tier and the affected harness
// -- a warning that only named one of the two would leave an operator with
// five files short a model and no way to tell which tier/harness combination
// to go fix in ~/.polyforge/config.toml [roles.tiers].
//
// mc is a bare &config.MachineConfig{} -- Roles is nil, so every tier's
// candidate list is empty for every harness, and ResolveModel (see
// TestResolveModel's "no candidate for the harness at all" case) returns
// ("", false) before ever consulting a probe. That is deliberate: it proves
// the fallback path fires from configuration alone, with no probe rejection
// needed to force it -- which is also why both harnesses' fakeProbe below
// rejects everything (moot: 0 candidates means it is never even consulted).
func TestGenerateRoles_UnresolvableCandidateFallback(t *testing.T) {
	// tier, role, and generated-file-name for two DIFFERENT tiers, so the
	// assertion cannot pass by coincidentally matching every warning against
	// the same tier name.
	cases := []struct{ tier, role string }{
		{"default", "executor"},
		{"lowest", "operator"},
	}

	for _, harness := range []string{"pi", "codex", "opencode"} {
		t.Run(harness, func(t *testing.T) {
			dir := t.TempDir()
			mc := &config.MachineConfig{} // Roles == nil: no candidates anywhere.
			probe := CatalogProbe(&fakeProbe{available: map[string]bool{}})

			stderr := captureStderr(t, func() {
				if err := generateRoles(mc, harness, dir, probe, ""); err != nil {
					t.Fatalf("generateRoles(%s) error: %v", harness, err)
				}
			})

			for _, c := range cases {
				// mc has NO candidates at all, so the honest diagnosis is
				// "this tier names no candidate for this harness", not "the
				// candidates did not resolve" (aihub#676 finding 5: the one
				// message used to be printed for every cause).
				want := `tier "` + c.tier + `" (role "` + c.role + `") has no candidate naming harness "` + harness + `"`
				if !strings.Contains(stderr, want) {
					t.Errorf("%s: stderr does not contain a warning naming BOTH tier %q and harness %q "+
						"(role %q); want substring %q, got:\n%s", harness, c.tier, harness, c.role, want, stderr)
				}
			}
			// Non-suppressible: no flag, env var, or caller-visible knob gates this
			// warning -- generateRoles takes no such parameter at all, so the only
			// way this assertion could fail is if the warning were removed outright.
			if strings.TrimSpace(stderr) == "" {
				t.Fatalf("%s: no warning at all was printed for an unresolvable candidate list", harness)
			}

			var fileName string
			var modelMarker string
			switch harness {
			case "pi":
				fileName, modelMarker = "pf-executor.md", "model:"
			case "codex":
				fileName, modelMarker = "step-executor.toml", "model ="
			case "opencode":
				fileName, modelMarker = "step-executor.md", "model:"
			}
			data, err := os.ReadFile(filepath.Join(dir, fileName))
			if err != nil {
				t.Fatalf("%s: %s not written: %v", harness, fileName, err)
			}
			if strings.Contains(string(data), modelMarker) {
				t.Errorf("%s: %s must have NO model field when nothing resolved, got:\n%s", harness, fileName, data)
			}
		})
	}
}

// TestGenerateRoles_NonFatalOnWriteFailure pins the hard "generation must
// never break the host" requirement for the serve-startup wiring
// (cmd/polyforge/main.go calls GenerateRoles and only logs a warning on
// error, never os.Exit). GenerateRoles/generateRoles must return a plain
// error -- never panic, never call os.Exit -- even when the output directory
// cannot be created, so main's non-fatal wrapping actually has an error value
// to catch.
func TestGenerateRoles_NonFatalOnWriteFailure(t *testing.T) {
	dir := t.TempDir()
	// Create a FILE where the generator wants to MkdirAll a directory, so
	// os.MkdirAll fails with ENOTDIR/EEXIST rather than any panic path.
	blocker := filepath.Join(dir, "blocked")
	if err := os.WriteFile(blocker, []byte("x"), 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}
	outDir := filepath.Join(blocker, "agents") // blocked's a file, not a dir

	err := GenerateRoles(&config.MachineConfig{}, "codex", outDir)
	if err == nil {
		t.Fatalf("GenerateRoles with an unwritable outDir: want a non-nil error, got nil")
	}
	// The point of this test: we got here at all (no panic, no os.Exit) AND
	// hold a plain error value, which is exactly what main()'s non-fatal
	// wrapper (`if err := cli.GenerateRoles(...); err != nil { warn, continue }`)
	// depends on.
	t.Logf("GenerateRoles correctly returned an error instead of crashing: %v", err)
}

// TestDefaultCodexAgentsDir_SkipsWhenNotAPluginTree proves the cwd-based
// resolution (chosen instead of os.Executable(), see DefaultCodexAgentsDir's
// doc comment) is self-verifying: it only fires when cwd/.codex-plugin
// actually exists, and quietly returns ok=false everywhere else (a bare
// `go test`/`go run` invocation from an arbitrary directory, in particular).
func TestDefaultCodexAgentsDir_SkipsWhenNotAPluginTree(t *testing.T) {
	dir := t.TempDir() // no .codex-plugin sibling here
	restore := chdir(t, dir)
	defer restore()

	if _, ok := DefaultCodexAgentsDir(); ok {
		t.Fatalf("DefaultCodexAgentsDir() ok=true in a directory with no .codex-plugin -- should have skipped")
	}
}

func TestDefaultCodexAgentsDir_ResolvesWhenPluginTreePresent(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".codex-plugin"), 0o755); err != nil {
		t.Fatalf("setup: %v", err)
	}
	restore := chdir(t, dir)
	defer restore()

	got, ok := DefaultCodexAgentsDir()
	if !ok {
		t.Fatalf("DefaultCodexAgentsDir() ok=false with a real .codex-plugin sibling present")
	}
	want := filepath.Join(dir, ".codex-plugin", "agents")
	if got != want {
		t.Fatalf("DefaultCodexAgentsDir() = %q, want %q", got, want)
	}
}

// chdir changes to dir and returns a func that restores the original
// working directory. Test-only helper; os.Chdir is process-global, so
// callers must not run this test in parallel with others in this package.
func chdir(t *testing.T, dir string) func() {
	t.Helper()
	orig, err := os.Getwd()
	if err != nil {
		t.Fatalf("os.Getwd: %v", err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("os.Chdir(%s): %v", dir, err)
	}
	return func() {
		if err := os.Chdir(orig); err != nil {
			t.Fatalf("restore os.Chdir(%s): %v", orig, err)
		}
	}
}

// TestParseCodexModelCatalog_RealFormat pins the actual `codex debug models`
// output shape (aihub#642 code_review AC6 finding): the real command emits
// ONE line of JSON, {"models":[{"slug":"...", ...}]}, not one bare model slug
// per line -- which is what the parser wrongly assumed before this fix. The
// fixture was captured live from `codex debug models` (codex-cli 0.154.0)
// and trimmed to 3 of its real models, with long prose fields truncated but
// every key name, nesting level, and slug left intact.
func TestParseCodexModelCatalog_RealFormat(t *testing.T) {
	data, err := os.ReadFile("testdata/codex_debug_models_sample.json")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	models, err := parseCodexModelCatalog(data)
	if err != nil {
		t.Fatalf("parseCodexModelCatalog: %v", err)
	}
	for _, want := range []string{"gpt-6-astra", "gpt-5.6-sol", "codex-auto-review"} {
		if !models[want] {
			t.Errorf("parseCodexModelCatalog: missing slug %q from real fixture, got %v", want, models)
		}
	}
	if len(models) != 3 {
		t.Errorf("parseCodexModelCatalog: got %d models, want 3 (fixture has exactly 3 entries)", len(models))
	}
}

// TestParseCodexModelCatalog_RejectsOneSlugPerLine pins the actual bug this
// wi fixes: the OLD parser split raw output on "\n" and treated every
// non-empty line as a bare model slug, so it never even looked at the real
// single-line JSON shape -- it just silently produced an empty (or garbage)
// catalog. This asserts the new parser recognizes that shape is not valid
// JSON and errors instead of silently misparsing it.
func TestParseCodexModelCatalog_RejectsOneSlugPerLine(t *testing.T) {
	_, err := parseCodexModelCatalog([]byte("gpt-6-astra\ngpt-5.6-sol\n"))
	if err == nil {
		t.Fatalf("parseCodexModelCatalog(one-slug-per-line input): want a JSON parse error, got nil")
	}
}

// TestParsePiModelCatalog_RealFormat pins the actual `pi --list-models`
// output shape: a header row ("provider   model   context ...", aligned with
// runs of spaces) followed by one data row per provider+model pair. The
// fixture is the real, unmodified output of `pi --list-models` (21 lines).
func TestParsePiModelCatalog_RealFormat(t *testing.T) {
	data, err := os.ReadFile("testdata/pi_list_models_sample.txt")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	c := parsePiModelCatalog(data)
	for _, want := range []string{
		"anthropic/claude-sonnet-4-5",
		"anthropic/claude-opus-5",
		"sub2api-openai/gpt-6-astra",
		"sub2api-grok/grok-4.6",
	} {
		if !c.Qualified[want] {
			t.Errorf("parsePiModelCatalog: missing qualified model %q from real fixture, got %v", want, c.Qualified)
		}
	}
	if c.Qualified["provider/model"] {
		t.Errorf("parsePiModelCatalog: header row must not be parsed as a model, got %v", c.Qualified)
	}
	if _, ok := c.ProvidersByModel["model"]; ok {
		t.Errorf("parsePiModelCatalog: header row must not be parsed as a model, got %v", c.ProvidersByModel)
	}
	// The provider column is the whole point of the type: a bare model id is
	// NOT a key of Qualified, because pi does not resolve one deterministically
	// (aihub#676 measurement, see piModelCatalog's doc comment).
	if c.Qualified["claude-sonnet-4-5"] {
		t.Error("parsePiModelCatalog: a BARE model id must not be a Qualified key -- keying on the " +
			"model column alone is the aihub#676 defect this type exists to prevent")
	}
}

// TestPiModelCatalogResolve pins the three-way verdict aihub#676's live pi
// measurement requires, against the real captured fixture.
func TestPiModelCatalogResolve(t *testing.T) {
	data, err := os.ReadFile("testdata/pi_list_models_sample.txt")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	c := parsePiModelCatalog(data)

	cases := []struct {
		name       string
		model      string
		wantOK     bool
		wantAdvice bool
		// adviceHas, when non-empty, must appear in the advice: it is what
		// makes the diagnostic actionable rather than merely present.
		adviceHas string
	}{
		{
			// The form the design measured as the only reliable one. It is
			// also the form that used to resolve FALSE.
			name: "qualified pair resolves with nothing to say", model: "sub2api-anthropic/claude-fable-5",
			wantOK: true, wantAdvice: false,
		},
		{
			// Measured live: `pi -p --model claude-fable-5` exits with
			// `Model "claude-fable-5" is ambiguous across providers`. It used
			// to resolve TRUE.
			name: "ambiguous bare id is refused and names the providers", model: "claude-fable-5",
			wantOK: false, wantAdvice: true, adviceHas: "sub2api-anthropic/claude-fable-5",
		},
		{
			// Unique in this table -- and still refused. The table cannot decide
			// ambiguity (pi weighs its whole built-in catalog, including
			// providers that are neither listed nor authenticated), so a count
			// of one here proves nothing about what pi would do. Refusing routes
			// into aihub#642 AC7's observable degradation instead of writing an
			// id that may not dispatch.
			name: "unique bare id is ALSO refused, and told what to write", model: "claude-opus-5",
			wantOK: false, wantAdvice: true, adviceHas: "anthropic/claude-opus-5",
		},
		{
			// The refusal for an ambiguous id must offer EVERY qualified
			// alternative, not just the first: picking for the operator is how
			// the aihub#642 design says a silent misroute starts.
			name: "ambiguous bare id refusal lists every qualified alternative", model: "claude-fable-5",
			wantOK: false, wantAdvice: true, adviceHas: "anthropic/claude-fable-5, sub2api-anthropic/claude-fable-5",
		},
		{
			name: "qualified pair for a provider that does not offer it", model: "sub2api-grok/claude-opus-5",
			wantOK: false, wantAdvice: false,
		},
		{
			name: "absent model", model: "no-such-model", wantOK: false, wantAdvice: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ok, advice := c.Resolve(tc.model)
			if ok != tc.wantOK {
				t.Errorf("Resolve(%q) ok = %v, want %v", tc.model, ok, tc.wantOK)
			}
			if (advice != "") != tc.wantAdvice {
				t.Errorf("Resolve(%q) advice = %q, want non-empty=%v", tc.model, advice, tc.wantAdvice)
			}
			if tc.adviceHas != "" && !strings.Contains(advice, tc.adviceHas) {
				t.Errorf("Resolve(%q) advice must contain %q so the operator knows what to write instead; got %q",
					tc.model, tc.adviceHas, advice)
			}
		})
	}
}

// TestParseOpencodeModelCatalog_RealFormat pins the actual `opencode models`
// output shape (aihub#653 measurement, opencode 1.18.30): one bare
// "provider/model-id" slug per line, no header row, no other stdout noise --
// the simplest of the three harness catalog formats (contrast codex's
// single-line JSON blob and pi's aligned header+data-row table). The fixture
// is a trimmed real capture (`opencode models` run in a clean, isolated
// sandbox on this box; the box is a GCP VM so google-vertex models appear via
// ambient metadata-server credentials even with HOME pointed at an empty
// temp dir) -- 9 of the real ~55 lines, chosen to cover the format's actual
// variety: a bare "provider/slug" line, one with an "@date"/"@default"
// version suffix, and two with a nested provider path segment
// (google-vertex/meta/..., google-vertex/xai/...).
func TestParseOpencodeModelCatalog_RealFormat(t *testing.T) {
	data, err := os.ReadFile("testdata/opencode_models_sample.txt")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	models := parseOpencodeModelCatalog(data)
	for _, want := range []string{
		"opencode/big-pickle",
		"google-vertex/claude-opus-5@default",
		"google-vertex/claude-sonnet-4-5@20250929",
		"google-vertex/meta/llama-4-maverick-17b-128e-instruct-maas",
		"google-vertex/xai/grok-4.6",
	} {
		if !models[want] {
			t.Errorf("parseOpencodeModelCatalog: missing slug %q from real fixture, got %v", want, models)
		}
	}
	if len(models) != 9 {
		t.Errorf("parseOpencodeModelCatalog: got %d models, want 9 (fixture has exactly 9 lines)", len(models))
	}
}

// TestParseOpencodeModelCatalog_SkipsBlankLines proves blank lines (a
// trailing newline in particular, which `exec.Command(...).Output()` always
// has) do not get parsed as an empty-string "model".
func TestParseOpencodeModelCatalog_SkipsBlankLines(t *testing.T) {
	models := parseOpencodeModelCatalog([]byte("opencode/big-pickle\n\n  \nanthropic/claude-opus-5\n"))
	if len(models) != 2 {
		t.Errorf("parseOpencodeModelCatalog: got %d models, want 2 (blank/whitespace-only lines must be skipped), got %v", len(models), models)
	}
	if models[""] {
		t.Errorf("parseOpencodeModelCatalog: empty string must never be a key, got %v", models)
	}
}

// TestUnresolvedModelWarning_NamesTheActualCause pins aihub#676 finding 5. The
// generator used to print ONE sentence for three different causes, and that
// sentence ended "Configure ~/.polyforge/config.toml [roles.tiers] to fix
// this" -- so `pi` merely not being installed, or not being logged in, sent the
// operator to edit a config file that was never wrong. Worse, the probe error
// that would have said so was discarded inside ResolveModel before the caller
// could see it.
//
// Three arms, each asserting the message DISCRIMINATES: it must say the thing
// that is true of its own cause and must NOT say the thing that is true of
// another. The negative halves are the load-bearing ones -- a message that
// merely mentions everything would pass a positives-only test.
func TestUnresolvedModelWarning_NamesTheActualCause(t *testing.T) {
	const tierSource = `preset "frugal" (via ~/.polyforge/config.toml [roles] preset)`

	t.Run("probe error is not reported as a config problem", func(t *testing.T) {
		_, ok, why := ResolveModelWithCause(
			[]config.RoleCandidate{{Harness: "pi", Model: "sub2api-anthropic/x"}}, "pi", erroringProbe{})
		if ok {
			t.Fatal("ResolveModelWithCause resolved against an always-erroring probe")
		}
		if why.ProbeErr == nil {
			t.Fatal("ResolveFailure.ProbeErr is nil: the probe error was swallowed again, which is " +
				"exactly the aihub#676 defect -- the caller then has nothing to report but the config")
		}
		msg := UnresolvedModelWarning("pi", "raised", "reviewer", tierSource, why)
		if !strings.Contains(msg, "could not read pi's model catalog") {
			t.Errorf("message does not say the catalog could not be read; got:\n%s", msg)
		}
		if !strings.Contains(msg, "NOT a configuration problem") {
			t.Errorf("message does not disclaim the config; got:\n%s", msg)
		}
		if !strings.Contains(msg, "install pi") {
			t.Errorf("message does not name the actionable fix (install/authenticate the CLI); got:\n%s", msg)
		}
		// The discriminator: this cause must NOT tell the operator to go fix
		// the tier table.
		if strings.Contains(msg, "Fix the tier table") {
			t.Errorf("message still blames the tier table for a probe failure; got:\n%s", msg)
		}
	})

	t.Run("no candidate for this harness names the harness, not the models", func(t *testing.T) {
		_, ok, why := ResolveModelWithCause(
			[]config.RoleCandidate{{Harness: "codex", Model: "gpt-6-astra"}}, "pi", &fakeProbe{})
		if ok {
			t.Fatal("ResolveModelWithCause resolved a codex-only candidate list for harness pi")
		}
		if len(why.Candidates) != 0 {
			t.Fatalf("ResolveFailure.Candidates = %v, want empty: no candidate named harness pi", why.Candidates)
		}
		msg := UnresolvedModelWarning("pi", "raised", "reviewer", tierSource, why)
		if !strings.Contains(msg, `has no candidate naming harness "pi"`) {
			t.Errorf("message does not say the tier names no pi candidate; got:\n%s", msg)
		}
		if !strings.Contains(msg, `raised = [{ harness = "pi"`) {
			t.Errorf("message does not show the line to add; got:\n%s", msg)
		}
		if strings.Contains(msg, "could not read") {
			t.Errorf("message blames the catalog for a config gap; got:\n%s", msg)
		}
	})

	t.Run("candidates that all missed name themselves", func(t *testing.T) {
		_, ok, why := ResolveModelWithCause([]config.RoleCandidate{
			{Harness: "pi", Model: "sub2api-anthropic/gone"},
			{Harness: "pi", Model: "sub2api-anthropic/also-gone"},
		}, "pi", &fakeProbe{available: map[string]bool{}})
		if ok {
			t.Fatal("ResolveModelWithCause resolved against a probe that reports nothing available")
		}
		if why.ProbeErr != nil {
			t.Fatalf("ResolveFailure.ProbeErr = %v, want nil: a probe answering false is not an error", why.ProbeErr)
		}
		msg := UnresolvedModelWarning("pi", "raised", "reviewer", tierSource, why)
		for _, want := range []string{"sub2api-anthropic/gone", "sub2api-anthropic/also-gone", "Fix the tier table"} {
			if !strings.Contains(msg, want) {
				t.Errorf("message does not contain %q; got:\n%s", want, msg)
			}
		}
		if strings.Contains(msg, "NOT a configuration problem") {
			t.Errorf("message disclaims the config for a cause that IS the config; got:\n%s", msg)
		}
	})

	t.Run("the example model shape is the one the named harness actually uses", func(t *testing.T) {
		// aihub#676 review W2: this builder serves the codex path too
		// (cmd/polyforge's generateCodexProfiles), and a shared
		// "<provider>/<model>" example contradicted this same binary's --help
		// and config footer -- on every `polyforge serve` boot, five times.
		codex := UnresolvedModelWarning("codex", "raised", "reviewer", tierSource, ResolveFailure{})
		if strings.Contains(codex, "<provider>/") {
			t.Errorf("the codex example teaches a provider prefix, which codex slugs do not carry:\n%s", codex)
		}
		if !strings.Contains(codex, "<codex-slug>") {
			t.Errorf("the codex example does not show a codex slug:\n%s", codex)
		}
		for _, harness := range []string{"pi", "opencode"} {
			msg := UnresolvedModelWarning(harness, "raised", "reviewer", tierSource, ResolveFailure{})
			if !strings.Contains(msg, "<provider>/<model>") {
				t.Errorf("%s's example omits the provider prefix, which %s REQUIRES:\n%s", harness, harness, msg)
			}
		}
	})

	t.Run("every cause names the tier table actually in force", func(t *testing.T) {
		// The second half of finding 5: the old message said "[roles.tiers]"
		// unconditionally, which is the wrong section whenever a preset is
		// selected -- the operator edits a table nothing reads.
		for name, why := range map[string]ResolveFailure{
			"probe error":  {ProbeErr: os.ErrClosed},
			"no candidate": {},
			"all missed":   {Candidates: []string{"sub2api-anthropic/gone"}},
		} {
			msg := UnresolvedModelWarning("pi", "raised", "reviewer", tierSource, why)
			if !strings.Contains(msg, tierSource) {
				t.Errorf("%s: message does not name the tier table in force (%q); got:\n%s", name, tierSource, msg)
			}
			if strings.Contains(msg, "[roles.tiers]") {
				t.Errorf("%s: message hardcodes [roles.tiers] while a preset is in force; got:\n%s", name, msg)
			}
		}
	})
}

// TestParsePiModelCatalog_SameModelUnderMultipleProviders keeps this test's
// original INTENT and corrects its assertion.
//
// The intent, verbatim from the version aihub#676 replaced, was to prove the
// parser "collapses a model slug that legitimately repeats under multiple
// providers, rather than erroring or double-counting" -- a statement about the
// parser being robust to a duplicate model column. That intent is still worth
// pinning and is still pinned below: parsing the fixture must not error, must
// not lose either row, and must not count either row twice.
//
// The ASSERTION it used to make, `models["claude-fable-5"] == true`, is the
// thing that was wrong. Keying on the bare model column is not "collapsing a
// duplicate"; it is discarding the provider, which is the half of the
// identifier pi actually resolves on. Measured live on 2026-09-14 (pi 0.85.1),
// the bare form this test blessed is the form pi REFUSES:
//
//	$ pi -p --model claude-fable-5 ...
//	Error: Model "claude-fable-5" is ambiguous across providers: anthropic/...,
//	cloudflare-ai-gateway/..., github-copilot/..., opencode/...,
//	sub2api-anthropic/... Use --provider or provider/model.
//
// while `--model sub2api-anthropic/claude-fable-5` -- which the old parser
// reported as UNAVAILABLE -- ran and answered. So this was not a pin protecting
// something real that aihub#676 traded away: it was a correct-sounding
// observation about a map, asserted as if it were a statement about pi.
func TestParsePiModelCatalog_SameModelUnderMultipleProviders(t *testing.T) {
	data, err := os.ReadFile("testdata/pi_list_models_sample.txt")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	c := parsePiModelCatalog(data)

	// Neither row is lost: both providers that offer claude-fable-5 survive as
	// distinct, individually resolvable entries.
	for _, want := range []string{"anthropic/claude-fable-5", "sub2api-anthropic/claude-fable-5"} {
		if !c.Qualified[want] {
			t.Errorf("parsePiModelCatalog: %q missing; a model offered by 2 providers must yield 2 entries, got %v",
				want, c.Qualified)
		}
	}

	// Neither row is double-counted: the provider list for the shared model id
	// has exactly the two providers the fixture shows, once each.
	got := c.ProvidersByModel["claude-fable-5"]
	want := []string{"anthropic", "sub2api-anthropic"}
	if len(got) != len(want) {
		t.Fatalf("ProvidersByModel[claude-fable-5] = %v, want exactly %v (no duplicates, nothing dropped)", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("ProvidersByModel[claude-fable-5][%d] = %q, want %q (first-seen order)", i, got[i], want[i])
		}
	}

	// And the correction itself: the bare id the old assertion demanded be
	// TRUE must now be unresolvable, because pi refuses it.
	if ok, _ := c.Resolve("claude-fable-5"); ok {
		t.Error("Resolve(\"claude-fable-5\") = true, but pi rejects that bare id as ambiguous across " +
			"providers (measured, pi 0.85.1). Reporting it as available is the aihub#676 defect.")
	}
}

// TestGenerateRoles_WarnsAboutOrphanAgentFiles pins the generator half of
// aihub#676 finding 4. generateRoles writes files but never reconciles, so a
// renamed or deleted role leaves a stale agent file behind that the harness
// will still happily load and dispatch to.
//
// It WARNS rather than deletes, and that is deliberate: --out is an argument,
// so it may be a directory the operator keeps their own files in ($CODEX_HOME
// is exactly that). The two negative assertions below are what make the
// warning safe to trust -- an untouched real file, and an unrelated file that
// merely lives in the same directory.
func TestGenerateRoles_WarnsAboutOrphanAgentFiles(t *testing.T) {
	dir := t.TempDir()
	mc := &config.MachineConfig{}
	probe := CatalogProbe(&fakeProbe{available: map[string]bool{}})

	orphan := filepath.Join(dir, "pf-zombie.md")
	if err := os.WriteFile(orphan, []byte("---\nname: pf-zombie\n---\n"), 0o644); err != nil {
		t.Fatalf("seed orphan: %v", err)
	}
	// Same directory, not this generator's naming: must be left alone AND
	// unmentioned.
	bystander := filepath.Join(dir, "notes.md")
	if err := os.WriteFile(bystander, []byte("mine\n"), 0o644); err != nil {
		t.Fatalf("seed bystander: %v", err)
	}

	stderr := captureStderr(t, func() {
		if err := generateRoles(mc, "pi", dir, probe, ""); err != nil {
			t.Fatalf("generateRoles(pi) error: %v", err)
		}
	})

	if !strings.Contains(stderr, "pf-zombie.md") {
		t.Errorf("stderr does not name the orphan pf-zombie.md; a deleted role's agent file stays "+
			"dispatchable and nothing says so. got:\n%s", stderr)
	}
	if !strings.Contains(stderr, "no longer exists") {
		t.Errorf("stderr does not say WHY pf-zombie.md is a problem; got:\n%s", stderr)
	}
	// Warned, never removed.
	if _, err := os.Stat(orphan); err != nil {
		t.Errorf("the orphan was DELETED (%v). --out may be a directory the operator also uses; this "+
			"generator must not remove files it did not write", err)
	}
	// Files it DID just write are not orphans.
	if strings.Contains(stderr, "pf-executor.md looks like") {
		t.Errorf("a file generated by this very run was reported as an orphan; got:\n%s", stderr)
	}
	// A file that does not match the naming this generator owns is not its business.
	if strings.Contains(stderr, "notes.md") {
		t.Errorf("an unrelated file in --out was reported; got:\n%s", stderr)
	}
	if _, err := os.Stat(bystander); err != nil {
		t.Errorf("an unrelated file in --out was removed (%v)", err)
	}
}

// TestGenerateRoles_WarnsBareModelIDAndUnknownHarness pins that the two
// config-side checks aihub#676 added actually reach an operator running
// `polyforge roles generate`, rather than only existing as a library function
// nothing calls (which is what `~/.polyforge/roles/` had been for a year).
func TestGenerateRoles_WarnsBareModelIDAndUnknownHarness(t *testing.T) {
	dir := t.TempDir()
	mc := &config.MachineConfig{
		Roles: &config.MachineRoles{
			Tiers: map[string][]config.RoleCandidate{
				// Bare id: pi would reject or misroute this at dispatch time.
				"default": {{Harness: "pi", Model: "claude-sonnet-4-5"}},
				// Typo: matches nothing, forever, silently.
				"raised": {{Harness: "claude", Model: "opus"}},
			},
		},
	}
	// The REAL catalog, not a fake: "claude-sonnet-4-5" is genuinely present in
	// the fixture (under anthropic), so this asserts the bare form is refused on
	// policy, not merely absent from a seeded map.
	probe := CatalogProbe(realPiCatalogProbe(t))

	stderr := captureStderr(t, func() {
		if err := generateRoles(mc, "pi", dir, probe, ""); err != nil {
			t.Fatalf("generateRoles(pi) error: %v", err)
		}
	})

	for _, want := range []string{"BARE pi model id", `unknown harness "claude"`} {
		if !strings.Contains(stderr, want) {
			t.Errorf("stderr does not contain %q; got:\n%s", want, stderr)
		}
	}
	// And the warning is not the whole of it: the bare id must not be WRITTEN.
	// A generator that warns twice and then emits the id anyway would leave the
	// operator with a file that reads as configured and does not dispatch --
	// exactly what aihub#642 AC7 exists to prevent (aihub#676 review B1).
	executor, readErr := os.ReadFile(filepath.Join(dir, "pf-executor.md"))
	if readErr != nil {
		t.Fatalf("pf-executor.md not written: %v", readErr)
	}
	if strings.Contains(string(executor), "model:") {
		t.Errorf("pf-executor.md declares a model despite the candidate being a refused bare id:\n%s", executor)
	}
	// And the provenance is attached, so an operator with several tables knows
	// which one to edit.
	if !strings.Contains(stderr, "[roles.tiers]") {
		t.Errorf("stderr does not name the tier table the problem is in; got:\n%s", stderr)
	}
}
