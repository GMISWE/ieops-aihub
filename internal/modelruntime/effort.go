package modelruntime

import (
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	"github.com/GMISWE/ieops-aihub/internal/config"
)

// EffortVerification reports whether the harness's applied effort could be
// READ BACK after dispatch. None of the four harnesses echo the effort it
// actually ran with in a locally parseable form, so the adapter reports
// EffortVerificationUnavailable rather than claiming the requested level was
// honoured: the dispatch record carries requested/resolved/unverified, never a
// fabricated confirmation (spec: "Record requested/resolved/unverified model
// details").
type EffortVerification string

const (
	EffortVerified                EffortVerification = "verified"
	EffortVerificationUnavailable EffortVerification = "unavailable"
)

// EffortSupport is the adapter's answer for one (harness, model, effort)
// triple. Native is the exact argv fragment(s) the effort maps to — fixed
// strings from the adapter, never operator-supplied.
type EffortSupport struct {
	// Native carries the invocation fragment(s), e.g. ["--thinking", "high"]
	// or ["-c", `model_reasoning_effort="high"`]. Empty only when Supported
	// is false.
	Native []string
	// Supported reports the effort can be expressed for this model on this
	// harness. False means preflight must refuse the candidate before start.
	Supported bool
	// ModelKnown reports whether per-model support metadata was locally
	// readable (codex's model catalog, pi's thinking column). False means
	// the harness documents the effort surface but no per-model metadata
	// exists locally, so support is carried UNVERIFIED rather than assumed
	// — the adapter never treats "cannot disprove" as "supports high".
	ModelKnown bool
	// Verified reports whether the applied effort could be read back. It is
	// EffortVerificationUnavailable for every current harness; the field
	// exists so the dispatch record can carry the honest value and a future
	// harness that does echo it has somewhere to say so.
	Verified EffortVerification
	// Refusal names the reason when Supported is false.
	Refusal string
}

// CatalogSources supplies the harness-local model catalogs the effort adapter
// consults for per-model support metadata. Every source renders a LOCAL
// catalog (installed-CLI listing or equivalent) — never a model request, and
// never a paid one. They are function fields so tests can pin the parsers
// against captured fixtures and production can shell out lazily, once, only
// when a candidate of that harness is actually preflighted.
type CatalogSources struct {
	// CodexModels renders `codex debug models` — the raw local model
	// catalog, one JSON document with per-model supported_reasoning_levels.
	CodexModels func() ([]byte, error)
	// PiModels renders `pi --list-models` — the local provider/model table
	// with its thinking column.
	PiModels func() ([]byte, error)
	// OpenCodeModels renders `opencode models` — the local provider/model
	// id list (one per line). It carries NO per-model effort metadata; it
	// is used only for model-presence, the strongest local check opencode
	// offers.
	OpenCodeModels func() ([]byte, error)
}

// LocalCatalogSources are the production sources: the harnesses' own local
// catalog renders. Each is executed lazily and at most once per process, and
// a source that fails (binary missing, unreadable) surfaces as a preflight
// refusal for candidates of that harness — never as "assume supported".
func LocalCatalogSources() CatalogSources {
	return CatalogSources{
		CodexModels:    memoizedShell("codex", "debug", "models"),
		PiModels:       memoizedShell("pi", "--list-models"),
		OpenCodeModels: memoizedShell("opencode", "models"),
	}
}

// memoizedShell runs a local harness catalog command at most once and
// caches the bytes (or the error). A nil source stays nil — Preflight treats
// a missing source as "no per-model metadata available" and says so.
func memoizedShell(name string, args ...string) func() ([]byte, error) {
	return memoize(func() ([]byte, error) {
		// Shell out through the standard PATH lookup; the command itself is
		// a local catalog render, not a model call.
		return runLocalCatalog(name, args...)
	})
}

// memoize runs produce EXACTLY ONCE and caches its outcome — bytes and
// error alike — returning the same result to every later call. Concurrency:
// sync.Once both serializes concurrent callers (the first one runs the
// command; the rest block until it has finished and then read the published
// result, so the once/data/err triple is never read or written racily) and
// publishes the writes with the necessary happens-before edge. Error
// caching is deliberate, not an accident: a source that failed once (binary
// missing, unreadable render) is a persistent condition of this process, and
// re-shelling per candidate would only burn another timeout on the same
// failure — Preflight surfaces the cached error as a refusal every time.
func memoize(produce func() ([]byte, error)) func() ([]byte, error) {
	var (
		once sync.Once
		data []byte
		err  error
	)
	return func() ([]byte, error) {
		once.Do(func() {
			data, err = produce()
		})
		return data, err
	}
}

// EffortFor maps the uniform public effort onto the harness's actual
// invocation flags for one model, consulting per-model support metadata where
// the harness exposes it locally.
//
// The mapping table, every cell pinned by a captured fixture in testdata
// (installed --help excerpts and local catalog renders only; no model
// requests were made to build it):
//
//	harness  invocation fragment                    per-model support source
//	pi       --thinking <effort>                    pi --list-models thinking column
//	cc       --effort <effort>                      none locally readable → unverified
//	codex    -c model_reasoning_effort="<effort>"   codex debug models supported_reasoning_levels
//	opencode --variant <effort>                     none (provider-specific, undocumented per model) → unverified
//
// Failure rules (fail, never silently map):
//
//   - an effort outside config.ModelEfforts is refused before any harness is
//     consulted — the uniform vocabulary is closed;
//   - pi/codex: a model absent from the harness's local catalog refuses (the
//     unknown path refuses rather than guessing a slug the catalog never named);
//   - codex: an effort absent from the model's supported_reasoning_levels
//     refuses — model-specific metadata, not the assumption that every model
//     supports high;
//   - pi: a model whose thinking column says "no" refuses an effort request;
//   - a catalog source that fails to render refuses candidates needing it.
//
// Where a harness documents the effort flag but no per-model metadata is
// locally readable (cc, opencode), the effort is mapped and support is
// reported UNVERIFIED (ModelKnown=false, Verified=unavailable): the flag's
// existence is fixture-pinned, per-model honouring is not locally provable,
// and a provider-side rejection surfaces at dispatch as the provider error it
// is, handled by the bounded fallback policy — not by inventing support
// claims here.
func EffortFor(harness, model, effort string, sources CatalogSources) (EffortSupport, error) {
	if !validUniformEffort(effort) {
		return EffortSupport{}, fmt.Errorf(
			"modelruntime: effort %q is not in the uniform public effort vocabulary %s; "+
				"harness-native-only levels (off/minimal/xhigh/max/ultra) are refused rather than downgraded",
			effort, strings.Join(config.ModelEfforts, ", "))
	}
	switch harness {
	case "pi":
		return piEffort(model, effort, sources)
	case "cc":
		// Claude Code documents --effort's choices in --help (fixture:
		// testdata/claude_help_effort.txt: "low, medium, high, xhigh, max").
		// The uniform subset is always CLI-acceptable; per-model support is
		// not locally readable anywhere, so it is carried unverified.
		return EffortSupport{
			Native:     []string{"--effort", effort},
			Supported:  true,
			ModelKnown: false,
			Verified:   EffortVerificationUnavailable,
		}, nil
	case "codex":
		return codexEffort(model, effort, sources)
	case "opencode":
		return opencodeEffort(model, effort, sources)
	default:
		return EffortSupport{}, fmt.Errorf("modelruntime: unknown harness %q", harness)
	}
}

func validUniformEffort(effort string) bool {
	for _, e := range config.ModelEfforts {
		if e == effort {
			return true
		}
	}
	return false
}

// piEffort maps effort onto pi's --thinking flag. pi --help (fixture
// testdata/pi_help_thinking.txt) documents the levels:
// "off, minimal, low, medium, high, xhigh, max" — the uniform subset passes
// CLI validation by construction. Per-model support comes from the local
// `pi --list-models` table's thinking column: a model present with
// thinking=yes supports a thinking level; absent models and thinking=no
// models refuse an effort request.
func piEffort(model, effort string, sources CatalogSources) (EffortSupport, error) {
	if sources.PiModels == nil {
		return EffortSupport{}, fmt.Errorf(
			"modelruntime: pi effort preflight needs the local `pi --list-models` catalog and none is configured; refusing rather than assuming model %q supports %q",
			model, effort)
	}
	data, err := sources.PiModels()
	if err != nil {
		return EffortSupport{}, fmt.Errorf("modelruntime: reading the local pi model catalog: %w", err)
	}
	rows, err := ParsePiModelCatalog(data)
	if err != nil {
		return EffortSupport{}, fmt.Errorf("modelruntime: parsing the local pi model catalog: %w", err)
	}
	row, ok := rows[model]
	if !ok {
		return EffortSupport{}, fmt.Errorf(
			"modelruntime: pi model %q is not in the local `pi --list-models` catalog; the unknown path refuses rather than guessing a provider/model the harness never named",
			model)
	}
	if !row.Thinking {
		return EffortSupport{}, fmt.Errorf(
			"modelruntime: pi model %q declares thinking=no in the local catalog; it cannot honour effort %q",
			model, effort)
	}
	return EffortSupport{
		Native:     []string{"--thinking", effort},
		Supported:  true,
		ModelKnown: true,
		Verified:   EffortVerificationUnavailable,
	}, nil
}

// codexEffort maps effort onto codex's model_reasoning_effort config
// override. The mechanism is a config override, not a dedicated flag
// (codex exec --help documents -c; the model_reasoning_effort key is
// verified against the installed codex binary's own config schema). The
// VALUE comes from the model's supported_reasoning_levels in the local
// `codex debug models` render — per-model metadata, so a model that does not
// list the requested level refuses instead of being assumed to support it.
func codexEffort(model, effort string, sources CatalogSources) (EffortSupport, error) {
	if sources.CodexModels == nil {
		return EffortSupport{}, fmt.Errorf(
			"modelruntime: codex effort preflight needs the local `codex debug models` catalog and none is configured; refusing rather than assuming model %q supports %q",
			model, effort)
	}
	data, err := sources.CodexModels()
	if err != nil {
		return EffortSupport{}, fmt.Errorf("modelruntime: reading the local codex model catalog: %w", err)
	}
	catalog, err := ParseCodexModelCatalog(data)
	if err != nil {
		return EffortSupport{}, fmt.Errorf("modelruntime: parsing the local codex model catalog: %w", err)
	}
	m, ok := catalog[model]
	if !ok {
		return EffortSupport{}, fmt.Errorf(
			"modelruntime: codex model %q is not in the local `codex debug models` catalog; the unknown path refuses rather than guessing a slug the catalog never named",
			model)
	}
	if !m.Efforts[effort] {
		levels := make([]string, 0, len(m.Efforts))
		for _, e := range m.EffortOrder {
			if m.Efforts[e] {
				levels = append(levels, e)
			}
		}
		return EffortSupport{}, fmt.Errorf(
			"modelruntime: codex model %q does not list effort %q in its supported_reasoning_levels (it lists: %s); "+
				"refusing rather than assuming every model supports %q",
			model, effort, strings.Join(levels, ", "), effort)
	}
	return EffortSupport{
		Native:     []string{"-c", fmt.Sprintf("model_reasoning_effort=%q", effort)},
		Supported:  true,
		ModelKnown: true,
		Verified:   EffortVerificationUnavailable,
	}, nil
}

// opencodeEffort maps effort onto opencode's --variant flag. opencode's own
// --help (fixture testdata/opencode_help_variant.txt) documents --variant as
// "model variant (provider-specific reasoning effort, e.g., high, max,
// minimal)" — provider-specific, and `opencode models` renders ids with no
// per-model variant metadata, so per-model support is NOT locally provable.
// The strongest local check available is model presence; the uniform level
// is then passed verbatim and support carried unverified.
func opencodeEffort(model, effort string, sources CatalogSources) (EffortSupport, error) {
	if sources.OpenCodeModels == nil {
		return EffortSupport{}, fmt.Errorf(
			"modelruntime: opencode effort preflight needs the local `opencode models` list and none is configured; refusing rather than assuming model %q exists",
			model)
	}
	data, err := sources.OpenCodeModels()
	if err != nil {
		return EffortSupport{}, fmt.Errorf("modelruntime: reading the local opencode model list: %w", err)
	}
	ids, err := ParseOpenCodeModelIDs(data)
	if err != nil {
		return EffortSupport{}, fmt.Errorf("modelruntime: parsing the local opencode model list: %w", err)
	}
	if !ids[model] {
		return EffortSupport{}, fmt.Errorf(
			"modelruntime: opencode model %q is not in the local `opencode models` list; the unknown path refuses rather than guessing a provider/model the harness never named",
			model)
	}
	return EffortSupport{
		Native:     []string{"--variant", effort},
		Supported:  true,
		ModelKnown: false,
		Verified:   EffortVerificationUnavailable,
	}, nil
}

// --- fixture-pinned parsers -------------------------------------------------
//
// Each parser is pinned against a real captured render in testdata. The
// captures are trimmed only in the sense aihub#642 established for
// internal/cli/testdata: real command output, optionally fewer rows/models,
// never a reformat.

// CodexModel is the per-model effort metadata the adapter consumes.
type CodexModel struct {
	// Efforts maps each supported reasoning effort (supported_reasoning_levels[].effort) to true.
	Efforts map[string]bool
	// EffortOrder preserves the catalog's declaration order for messages.
	EffortOrder []string
	// DefaultReasoningLevel is the model's default_reasoning_level, recorded
	// for provenance (the adapter never silently substitutes it).
	DefaultReasoningLevel string
}

// ParseCodexModelCatalog parses `codex debug models` output. aihub#642
// measured (codex-cli 0.154.0) that it emits ONE JSON document
// {"models":[...]} whose entries carry slug and supported_reasoning_levels —
// not one slug per line — and pinning that shape is what made codex catalog
// validation real instead of vacuous. This parser is the new path's own copy
// of that knowledge, pinned the same way against
// testdata/codex_debug_models.json.
func ParseCodexModelCatalog(data []byte) (map[string]CodexModel, error) {
	var doc struct {
		Models []struct {
			Slug                     string `json:"slug"`
			DefaultReasoningLevel    string `json:"default_reasoning_level"`
			SupportedReasoningLevels []struct {
				Effort string `json:"effort"`
			} `json:"supported_reasoning_levels"`
		} `json:"models"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("not the codex debug models JSON document: %w", err)
	}
	if len(doc.Models) == 0 {
		return nil, fmt.Errorf("model catalog lists no models")
	}
	out := make(map[string]CodexModel, len(doc.Models))
	for _, m := range doc.Models {
		if m.Slug == "" {
			return nil, fmt.Errorf("catalog entry without a slug")
		}
		if _, dup := out[m.Slug]; dup {
			// Measured legitimate case in older captures: the same slug under
			// visibility variants. First wins and is recorded; effort support
			// does not differ between variants in any captured catalog.
			continue
		}
		cm := CodexModel{Efforts: make(map[string]bool, len(m.SupportedReasoningLevels))}
		for _, l := range m.SupportedReasoningLevels {
			if l.Effort == "" {
				return nil, fmt.Errorf("model %q lists a reasoning level without an effort name", m.Slug)
			}
			if !cm.Efforts[l.Effort] {
				cm.EffortOrder = append(cm.EffortOrder, l.Effort)
			}
			cm.Efforts[l.Effort] = true
		}
		cm.DefaultReasoningLevel = m.DefaultReasoningLevel
		out[m.Slug] = cm
	}
	return out, nil
}

// PiModel is one row of pi's local model table.
type PiModel struct {
	Provider string
	Model    string
	// Thinking is the row's thinking column (yes/no). It is the only
	// per-model effort-adjacent datum pi exposes locally.
	Thinking bool
}

// ParsePiModelCatalog parses `pi --list-models` output: a whitespace-aligned
// table with header "provider model context max-out thinking images", one
// row per provider+model pair. The header is skipped by matching the literal
// field values (not line position), and rows collapse into a set keyed by
// "<provider>/<model>" — the same slug legitimately repeats under multiple
// providers, and the machine's catalog entries always carry the provider
// prefix (config validation enforces it), so the prefixed key is the exact
// identity. Pinned against testdata/pi_list_models.txt (real output).
func ParsePiModelCatalog(data []byte) (map[string]PiModel, error) {
	out := make(map[string]PiModel)
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 6 {
			continue
		}
		if fields[0] == "provider" && fields[1] == "model" {
			continue // header, matched by value not position
		}
		row := PiModel{Provider: fields[0], Model: fields[1]}
		switch strings.ToLower(fields[4]) {
		case "yes":
			row.Thinking = true
		case "no":
			row.Thinking = false
		default:
			return nil, fmt.Errorf("unrecognised thinking value %q in pi model row %q", fields[4], line)
		}
		out[row.Provider+"/"+row.Model] = row
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no model rows found")
	}
	return out, nil
}

// ParseOpenCodeModelIDs parses `opencode models` output: one
// "<provider>/<model>" id per line. Pinned against
// testdata/opencode_models.txt (real output, which includes multi-segment
// provider paths such as google-vertex/claude-sonnet-4-5@20250929 — parsed
// verbatim, never normalized).
func ParseOpenCodeModelIDs(data []byte) (map[string]bool, error) {
	out := make(map[string]bool)
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		out[line] = true
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no model ids found")
	}
	return out, nil
}
