package modelruntime

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// fixture sources for tests: the captured local catalog renders in testdata.
// These are REAL output of the installed harness CLIs on this machine
// (2026-09-17): `codex debug models` (trimmed per aihub#642's
// captured-then-trimmed precedent), `pi --list-models`, `opencode models`,
// and --help excerpts. No model request produced any of them.
func fixtureSource(t *testing.T, name string) func() ([]byte, error) {
	t.Helper()
	return func() ([]byte, error) {
		return os.ReadFile(filepath.Join("testdata", name))
	}
}

func sourcesFor(t *testing.T) CatalogSources {
	return CatalogSources{
		CodexModels:    fixtureSource(t, "codex_debug_models.json"),
		PiModels:       fixtureSource(t, "pi_list_models.txt"),
		OpenCodeModels: fixtureSource(t, "opencode_models.txt"),
	}
}

// TestFixtureEvidenceForEffortAdapters pins the DOCUMENTED surface each
// adapter's claim rests on, straight from the captured fixtures. If a
// harness update removes or renames a flag, this fails and forces the
// adapter to be re-verified against the new reality instead of silently
// mapping onto a flag that no longer exists.
func TestFixtureEvidenceForEffortAdapters(t *testing.T) {
	for _, tc := range []struct {
		file string
		want []string
	}{
		{"pi_help_thinking.txt", []string{"--thinking", "off, minimal, low, medium, high, xhigh, max"}},
		{"claude_help_effort.txt", []string{"--effort", "low, medium, high, xhigh, max"}},
		{"opencode_help_variant.txt", []string{"--variant", "provider-specific reasoning effort"}},
	} {
		b, err := os.ReadFile(filepath.Join("testdata", tc.file))
		if err != nil {
			t.Fatalf("fixture %s unreadable: %v", tc.file, err)
		}
		for _, w := range tc.want {
			if !strings.Contains(string(b), w) {
				t.Errorf("fixture %s no longer documents %q — the adapter mapping must be re-verified", tc.file, w)
			}
		}
	}
}

func TestParseCodexModelCatalogFixture(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("testdata", "codex_debug_models.json"))
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := ParseCodexModelCatalog(data)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	// Real per-model diversity captured from the installed codex: these two
	// models list DIFFERENT supported_reasoning_levels, which is the whole
	// reason the adapter consults per-model metadata instead of assuming
	// every model supports everything.
	astra := catalog["gpt-6-astra"]
	if got := strings.Join(astra.EffortOrder, ","); got != "low,medium,high,xhigh,max,ultra" {
		t.Errorf("gpt-6-astra levels = %q", got)
	}
	if !astra.Efforts["high"] || !astra.Efforts["low"] || !astra.Efforts["medium"] {
		t.Error("gpt-6-astra must list the uniform set")
	}
	gpt55 := catalog["gpt-5.5"]
	if got := strings.Join(gpt55.EffortOrder, ","); got != "low,medium,high,xhigh" {
		t.Errorf("gpt-5.5 levels = %q", got)
	}
	if gpt55.Efforts["ultra"] || gpt55.Efforts["max"] {
		t.Error("gpt-5.5 must not list max/ultra — per-model diversity is the point of the fixture")
	}
}

func TestParsePiModelCatalogFixture(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("testdata", "pi_list_models.txt"))
	if err != nil {
		t.Fatal(err)
	}
	rows, err := ParsePiModelCatalog(data)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	// The fixture is real `pi --list-models` output: every captured row on
	// this box says thinking=yes, which the parser must carry as true…
	if r, ok := rows["sub2api-glm/glm-5.3"]; !ok || !r.Thinking {
		t.Errorf("sub2api-glm/glm-5.3 = %+v, want thinking=true", r)
	}
	// …while the header is skipped by value, not by position.
	if _, ok := rows["provider/model"]; ok {
		t.Error("header row leaked into the parsed set")
	}
}

func TestParseOpenCodeModelIDsFixture(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("testdata", "opencode_models.txt"))
	if err != nil {
		t.Fatal(err)
	}
	ids, err := ParseOpenCodeModelIDs(data)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if !ids["anthropic/claude-opus-4-6"] {
		t.Error("anthropic/claude-opus-4-6 must be in the parsed set")
	}
}

func TestEffortForUniformVocabularyIsClosed(t *testing.T) {
	for _, effort := range []string{"off", "minimal", "xhigh", "max", "ultra", "", "HIGH"} {
		_, err := EffortFor("cc", "sonnet", effort, CatalogSources{})
		if err == nil {
			t.Errorf("effort %q must be refused outside the uniform vocabulary", effort)
		} else if !strings.Contains(err.Error(), "uniform public effort vocabulary") {
			t.Errorf("effort %q refusal must name the vocabulary: %v", effort, err)
		}
	}
}

func TestEffortForCodex(t *testing.T) {
	src := sourcesFor(t)
	// A real model, a uniform level it lists: mapped onto the config override.
	s, err := EffortFor("codex", "gpt-6-astra", "high", src)
	if err != nil {
		t.Fatalf("high on gpt-6-astra: %v", err)
	}
	if !s.Supported || !s.ModelKnown || s.Verified != EffortVerificationUnavailable {
		t.Errorf("support = %+v", s)
	}
	if got := strings.Join(s.Native, " "); got != `-c model_reasoning_effort="high"` {
		t.Errorf("native = %q", got)
	}
	// The unknown path refuses: a slug the local catalog never named.
	if _, err := EffortFor("codex", "gpt-nonexistent", "high", src); err == nil ||
		!strings.Contains(err.Error(), "not in the local") {
		t.Errorf("unknown codex model must refuse, got: %v", err)
	}
	// Per-model metadata, not the assumption every model supports everything:
	// a crafted catalog (pure test of the mechanism) whose only model lists
	// no high proves the refusal path exists and fires.
	limited := func() ([]byte, error) {
		return []byte(`{"models":[{"slug":"limited-model","supported_reasoning_levels":[{"effort":"low"},{"effort":"medium"}]}]}`), nil
	}
	_, err = EffortFor("codex", "limited-model", "high", CatalogSources{CodexModels: limited})
	if err == nil || !strings.Contains(err.Error(), "does not list effort \"high\"") {
		t.Errorf("per-model unsupported effort must refuse, got: %v", err)
	}
	// A missing source refuses rather than assumes.
	if _, err := EffortFor("codex", "gpt-6-astra", "high", CatalogSources{}); err == nil {
		t.Error("missing codex catalog source must refuse")
	}
}

func TestEffortForPi(t *testing.T) {
	src := sourcesFor(t)
	s, err := EffortFor("pi", "sub2api-glm/glm-5.3", "high", src)
	if err != nil {
		t.Fatalf("high on glm-5.3: %v", err)
	}
	if !s.Supported || !s.ModelKnown || s.Verified != EffortVerificationUnavailable {
		t.Errorf("support = %+v", s)
	}
	if got := strings.Join(s.Native, " "); got != "--thinking high" {
		t.Errorf("native = %q", got)
	}
	if _, err := EffortFor("pi", "nope/nope", "low", src); err == nil ||
		!strings.Contains(err.Error(), "not in the local") {
		t.Errorf("unknown pi model must refuse, got: %v", err)
	}
	// thinking=no refuses an effort request: crafted table, pure test of the
	// mechanism (every real row on this box currently says yes).
	noThinking := func() ([]byte, error) {
		return []byte("provider model context max-out thinking images\n" +
			"p m 1M 16.4K no yes\n"), nil
	}
	if _, err := EffortFor("pi", "p/m", "low", CatalogSources{PiModels: noThinking}); err == nil ||
		!strings.Contains(err.Error(), "thinking=no") {
		t.Errorf("thinking=no must refuse effort, got: %v", err)
	}
}

func TestEffortForCC(t *testing.T) {
	// Claude Code documents --effort's choices (fixture-pinned above) but
	// exposes no local per-model catalog, so support is carried UNVERIFIED —
	// never claimed as known.
	s, err := EffortFor("cc", "sonnet", "high", CatalogSources{})
	if err != nil {
		t.Fatalf("high on cc: %v", err)
	}
	if !s.Supported || s.ModelKnown {
		t.Errorf("cc support must be carried unverified: %+v", s)
	}
	if s.Verified != EffortVerificationUnavailable {
		t.Errorf("effort verification must be reported unavailable, not fabricated: %+v", s)
	}
	if got := strings.Join(s.Native, " "); got != "--effort high" {
		t.Errorf("native = %q", got)
	}
}

func TestEffortForOpenCode(t *testing.T) {
	src := sourcesFor(t)
	s, err := EffortFor("opencode", "anthropic/claude-opus-4-6", "medium", src)
	if err != nil {
		t.Fatalf("medium on opencode: %v", err)
	}
	if !s.Supported || s.ModelKnown || s.Verified != EffortVerificationUnavailable {
		t.Errorf("opencode support must be carried unverified: %+v", s)
	}
	if got := strings.Join(s.Native, " "); got != "--variant medium" {
		t.Errorf("native = %q", got)
	}
	if _, err := EffortFor("opencode", "nope/nope", "low", src); err == nil ||
		!strings.Contains(err.Error(), "not in the local") {
		t.Errorf("unknown opencode model must refuse, got: %v", err)
	}
}

func TestEffortForUnknownHarness(t *testing.T) {
	if _, err := EffortFor("claude", "sonnet", "high", CatalogSources{}); err == nil {
		t.Error("harness key claude must be refused (cc is the key)")
	}
	if _, err := EffortFor("acme", "m", "high", CatalogSources{}); err == nil {
		t.Error("unknown harness must be refused")
	}
}

// TestMemoizeConcurrentSingleExecution is the Astra-review repair
// (aihub#708): the memoized shell sources were an unsynchronized
// once-bool + cached pair — concurrent callers raced on both. sync.Once now
// serializes them: the producer runs EXACTLY once no matter how many
// callers arrive simultaneously, every caller gets the same published
// result, and no caller can observe a half-written cache. Run with -race;
// the assertion is the call count, not just the absence of a crash.
func TestMemoizeConcurrentSingleExecution(t *testing.T) {
	var calls int32
	produce := func() ([]byte, error) {
		atomic.AddInt32(&calls, 1)
		// Hold the window open so losing racers would pile in if the
		// synchronization were missing.
		time.Sleep(25 * time.Millisecond)
		return []byte("payload"), nil
	}
	src := memoize(produce)

	const n = 32
	var wg sync.WaitGroup
	datas := make([]string, n)
	errs := make([]error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			data, err := src()
			datas[i] = string(data)
			errs[i] = err
		}(i)
	}
	wg.Wait()

	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Fatalf("producer ran %d times under %d concurrent callers, want exactly 1", got, n)
	}
	for i := 0; i < n; i++ {
		if errs[i] != nil {
			t.Errorf("caller %d: unexpected error: %v", i, errs[i])
		}
		if datas[i] != "payload" {
			t.Errorf("caller %d got %q, want the single published payload", i, datas[i])
		}
	}
}

// TestMemoizeCachesErrorSafely pins the error-caching half: a source that
// fails once (binary missing, unreadable render) is a persistent condition
// of this process — the error is cached and returned identically to every
// later call, and the producer never runs again. Caching the error safely
// is what keeps a per-candidate preflight from re-burning the source's
// timeout on the same missing binary.
func TestMemoizeCachesErrorSafely(t *testing.T) {
	wantErr := errors.New("binary not on PATH")
	var calls int32
	src := memoize(func() ([]byte, error) {
		atomic.AddInt32(&calls, 1)
		return nil, wantErr
	})
	for i := 0; i < 5; i++ {
		data, err := src()
		if err == nil || !errors.Is(err, wantErr) {
			t.Fatalf("call %d: got %v, want the cached producer error verbatim", i, err)
		}
		if data != nil {
			t.Fatalf("call %d: a failed source must not publish bytes, got %q", i, data)
		}
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Fatalf("producer ran %d times, want exactly 1 (the error is cached, not re-shelled)", got)
	}
}

// TestLocalCatalogSourcesMemoizedShellShape sanity-checks the production
// wiring: LocalCatalogSources hands memoizedShell sources, so its exact-once
// semantics apply to the real PATH lookups too.
func TestLocalCatalogSourcesMemoizedShellShape(t *testing.T) {
	sources := LocalCatalogSources()
	if sources.CodexModels == nil || sources.PiModels == nil || sources.OpenCodeModels == nil {
		t.Fatal("LocalCatalogSources must wire all three memoized sources")
	}
	// A missing binary caches its error once; both calls see the SAME
	// error value (memoizedShell wraps runLocalCatalog's deterministic
	// shape for the same missing command).
	_, err1 := sources.PiModels()
	_, err2 := sources.PiModels()
	if err1 == nil {
		t.Skip("pi is installed on this machine; the missing-binary path cannot be exercised here")
	}
	if err2 == nil || err1.Error() != err2.Error() {
		t.Errorf("second call must return the identical cached error: %v vs %v", err1, err2)
	}
}
