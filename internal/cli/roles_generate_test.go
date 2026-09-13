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
				"default": {{Harness: "pi", Model: "claude-sonnet-4-5"}},
			},
		},
	}
	probe := &fakeProbe{available: map[string]bool{"claude-sonnet-4-5": true}}

	if err := generateRoles(mc, "pi", dir, probe); err != nil {
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
			if !strings.Contains(content, "model: claude-sonnet-4-5") {
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

	if err := generateRoles(mc, "codex", dir, probe); err != nil {
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

	if err := generateRoles(mc, "pi", dir, probe); err != nil {
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

	for _, harness := range []string{"pi", "codex"} {
		t.Run(harness, func(t *testing.T) {
			dir := t.TempDir()
			mc := &config.MachineConfig{} // Roles == nil: no candidates anywhere.
			probe := CatalogProbe(&fakeProbe{available: map[string]bool{}})

			stderr := captureStderr(t, func() {
				if err := generateRoles(mc, harness, dir, probe); err != nil {
					t.Fatalf("generateRoles(%s) error: %v", harness, err)
				}
			})

			for _, c := range cases {
				want := `no resolvable ` + harness + ` model candidate for tier "` + c.tier + `" (role "` + c.role + `")`
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
	models := parsePiModelCatalog(data)
	for _, want := range []string{"claude-sonnet-4-5", "claude-opus-5", "gpt-6-astra"} {
		if !models[want] {
			t.Errorf("parsePiModelCatalog: missing model %q from real fixture, got %v", want, models)
		}
	}
	if models["provider"] || models["model"] {
		t.Errorf("parsePiModelCatalog: header row must not be parsed as a model, got %v", models)
	}
}

// TestParsePiModelCatalog_SameModelUnderMultipleProviders proves the
// set-based return collapses a model slug that legitimately repeats under
// multiple providers, rather than erroring or double-counting.
// "claude-fable-5" appears under both "anthropic" and "sub2api-anthropic" in
// the real fixture.
func TestParsePiModelCatalog_SameModelUnderMultipleProviders(t *testing.T) {
	data, err := os.ReadFile("testdata/pi_list_models_sample.txt")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	models := parsePiModelCatalog(data)
	if !models["claude-fable-5"] {
		t.Errorf("parsePiModelCatalog: claude-fable-5 (appears under 2 providers in the fixture) missing, got %v", models)
	}
}
