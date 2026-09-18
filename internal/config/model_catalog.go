package config

import (
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/GMISWE/ieops-aihub/internal/skillregistry"
)

// MachineModel is one [[models]] entry in ~/.polyforge/config.toml (aihub#708
// Batch 3). It names a machine-local model route for the NEW workflow path:
// harness-native identity plus the uniform public effort, and the uses
// (skillregistry.Capability vocabulary) this entry is trusted to serve.
//
// It is data, never authority on its own: dispatch still requires the
// controller's StepGrant, and every candidate is preflighted against the
// machine's catalog before it may start (internal/modelruntime.Preflight).
//
// There are deliberately NO credentials and NO invocation flags here. Keys
// live in the harnesses' own credential stores, and the command line is
// constructed only from these typed fields by the shared adapter — a
// "[[models]] entry with extra_args" would be arbitrary flag passthrough from
// a local file, exactly the class the spec forbids (D9: "step candidates
// persist resolved harness-native identity, never secrets").
type MachineModel struct {
	// Name is the stable reference operators and WI step authoring use to
	// resolve this entry. Must match ^[a-z][a-z0-9_-]*$ (workflow's candidate
	// token shape) and be unique within the catalog.
	Name string `toml:"name"`

	// Harness is one of ConfigurableHarnesses ("cc", "codex", "opencode",
	// "pi") — the same vocabulary [roles.tiers] uses, including "cc" (not
	// "claude") for Claude Code. See RoleCandidate.Harness for the spelling
	// rules and their reasons; they are identical here.
	Harness string `toml:"harness"`

	// Model is the harness-native model identifier, with the same per-harness
	// spelling rules as RoleCandidate.Model: "<provider>/<model>" is REQUIRED
	// for pi and opencode (aihub#676's bare-id measurement), a bare alias or
	// full id for cc, and a verbatim slug for codex. For pi, do NOT append
	// pi's optional ":<thinking>" model suffix — effort belongs in the Effort
	// field, so the adapter can validate it against the uniform vocabulary
	// instead of smuggling a level through the model string.
	Model string `toml:"model"`

	// Effort is the uniform public effort for this route: one of ModelEfforts
	// ("low", "medium", "high"). Harness-native-only levels are refused here
	// (see ModelEfforts) and per-harness/per-model support is checked again at
	// dispatch preflight — this field being valid TOML does not mean every
	// model can honour it.
	Effort string `toml:"effort"`

	// Uses lists the skillregistry capability names this entry may serve, e.g.
	// ["authoring"], ["review", "verification"]. Dispatch intersects the
	// step's requested capabilities with this trusted machine policy and
	// refuses the candidate when any requested capability is missing — a
	// review step does not silently run on an entry trusted for authoring
	// only. Non-empty; every token must be in skillregistry's closed
	// capability vocabulary.
	Uses []string `toml:"uses,omitempty"`

	// Description is free-form operator documentation. It is never parsed and
	// never reaches a command line.
	Description string `toml:"description,omitempty"`
}

// ModelEfforts is the closed uniform public effort vocabulary for [[models]]
// and new-path model candidates: low, medium, high.
//
// The set is the INTERSECTION of what all four harnesses can actually express
// through their documented invocation surfaces, each pinned by a captured
// fixture in internal/modelruntime/testdata (installed --help / local catalog
// renders only — no model requests):
//
//	pi       --thinking off|minimal|low|medium|high|xhigh|max   (pi_help_thinking.txt)
//	claude   --effort low, medium, high, xhigh, max             (claude_help_effort.txt)
//	codex    -c model_reasoning_effort=<per-model levels>       (codex_debug_models.json)
//	opencode --variant <provider-specific effort>               (opencode_help_variant.txt)
//
// Harness-native-only tokens ("off", "minimal", "xhigh", "max", "ultra") are
// deliberately NOT in the uniform vocabulary: at least one harness cannot
// express each of them, so admitting one would either silently downgrade it
// somewhere else or fork the vocabulary per harness. Requesting one is a
// validation error that names the valid set — never a downgrade (spec D9:
// "unknown effort or unsupported mapping errors, no silent downgrade").
var ModelEfforts = []string{"low", "medium", "high"}

// modelNameRE is the reference shape for MachineModel.Name. It is
// intentionally identical to workflow's candidate token shape
// (^[a-z][a-z0-9_-]*$) so a name can be carried into harness/model candidate
// metadata without a second spelling.
var modelNameRE = regexp.MustCompile(`^[a-z][a-z0-9_-]*$`)

// ValidateModels strictly validates a [[models]] catalog. It returns an error
// listing EVERY problem (sorted by entry then field) or nil. Strict means
// strict: unlike ValidateCandidates — which is a warning source for the legacy
// tier table — an invalid [[models]] catalog is refused outright, because the
// new path resolves entries deterministically by exact name and a half-valid
// entry would either resolve to something the operator did not write or fail
// at dispatch with a worse error.
//
// Among the problems it refuses: two entries pinning the SAME candidate
// triple (harness, model, effort). The dispatch-time authorization map
// (modelruntime.Catalog.Match) is keyed on that exact triple, so a duplicate
// would silently resolve to whichever entry was indexed last — and with it
// that entry's uses policy. A route is a name AND a policy; duplicates are
// refused, never last-wins.
//
// What it does NOT check: per-model effort support (harness-local catalog
// knowledge; checked at dispatch preflight by internal/modelruntime) and
// binary presence. Those are machine-state questions, not config-shape
// questions.
func ValidateModels(models []MachineModel) error {
	knownHarness := make(map[string]bool, len(ConfigurableHarnesses))
	for _, h := range ConfigurableHarnesses {
		knownHarness[h] = true
	}
	knownEffort := make(map[string]bool, len(ModelEfforts))
	for _, e := range ModelEfforts {
		knownEffort[e] = true
	}

	names := make(map[string]bool, len(models))
	// triples maps a well-formed (harness, model, effort) route key to the
	// name of the first entry that pinned it. Only individually valid
	// fields participate in route identity: two broken entries that happen
	// to share their brokenness each already carry their own problem, and a
	// "duplicate" message over "model \"\"" would be noise, not signal.
	triples := make(map[string]string, len(models))
	var problems []string
	for i, m := range models {
		prefix := fmt.Sprintf("[[models]] entry %d", i)
		switch {
		case m.Name == "":
			problems = append(problems, prefix+" declares no name; it can never be referenced")
		case !modelNameRE.MatchString(m.Name):
			problems = append(problems, fmt.Sprintf(
				"%s has invalid name %q (want ^[a-z][a-z0-9_-]*$, the same shape as harness/model candidate tokens)",
				prefix, m.Name))
		case names[m.Name]:
			problems = append(problems, fmt.Sprintf(
				"%s duplicates name %q; resolution is exact and cannot be deterministic on a duplicate",
				prefix, m.Name))
		}
		names[m.Name] = true

		switch {
		case m.Harness == "":
			problems = append(problems, fmt.Sprintf(
				"%s declares no harness; it can never match. Known harnesses: %s",
				prefix, strings.Join(ConfigurableHarnesses, ", ")))
		case !knownHarness[m.Harness]:
			hint := ""
			if m.Harness == "claude" || m.Harness == "claude-code" {
				hint = ` (Claude Code's key is spelled "cc")`
			}
			problems = append(problems, fmt.Sprintf(
				"%s names unknown harness %q%s; it can never match. Known harnesses: %s",
				prefix, m.Harness, hint, strings.Join(ConfigurableHarnesses, ", ")))
		}

		switch {
		case m.Model == "":
			problems = append(problems, fmt.Sprintf("%s declares no model", prefix))
		case m.Model != strings.TrimSpace(m.Model) || strings.ContainsAny(m.Model, "\r\n\t"):
			problems = append(problems, fmt.Sprintf(
				"%s model %q carries leading/trailing whitespace or control characters; it would be passed verbatim to the harness",
				prefix, m.Model))
		case (m.Harness == "pi" || m.Harness == "opencode") && !strings.Contains(m.Model, "/"):
			problems = append(problems, fmt.Sprintf(
				"%s declares the BARE %s model id %q. Write \"<provider>/%s\": the provider half is "+
					"machine-chosen and a bare id is refused or misresolved by the harness (aihub#676, "+
					"measured on pi 0.85.1)",
				prefix, m.Harness, m.Model, m.Model))
		}

		switch {
		case m.Effort == "":
			problems = append(problems, fmt.Sprintf(
				"%s declares no effort; the uniform vocabulary is %s", prefix, strings.Join(ModelEfforts, ", ")))
		case !knownEffort[m.Effort]:
			problems = append(problems, fmt.Sprintf(
				"%s effort %q is not in the uniform public effort vocabulary %s. Harness-native-only levels "+
					"(off/minimal/xhigh/max/ultra) are deliberately excluded: at least one harness cannot "+
					"express each of them, and mapping one to something else would be a silent downgrade",
				prefix, m.Effort, strings.Join(ModelEfforts, ", ")))
		}

		// Route identity: the same harness/model/effort triple twice means
		// the dispatch-time by-candidate map would silently resolve to the
		// LAST entry — and to ITS uses policy. Refuse before that overwrite
		// can happen (modelruntime.NewCatalog re-guards its map write).
		harnessOK := knownHarness[m.Harness]
		modelOK := m.Model != "" && m.Model == strings.TrimSpace(m.Model) && !strings.ContainsAny(m.Model, "\r\n\t")
		effortOK := knownEffort[m.Effort]
		if harnessOK && modelOK && effortOK {
			key := m.Harness + "\x00" + m.Model + "\x00" + m.Effort
			if firstName, dup := triples[key]; dup {
				problems = append(problems, fmt.Sprintf(
					"%s (name %q) pins the same route as entry named %q (harness=%q model=%q effort=%q); "+
					"a duplicate route would silently resolve to the last entry and ITS uses policy — remove one",
					prefix, m.Name, firstName, m.Harness, m.Model, m.Effort))
			} else {
				triples[key] = m.Name
			}
		}

		if len(m.Uses) == 0 {
			problems = append(problems, fmt.Sprintf(
				"%s declares no uses; an entry trusted for no capability can serve no step. Valid uses: %s",
				prefix, strings.Join(capabilityNames(), ", ")))
		}
		for j, use := range m.Uses {
			if !skillregistry.IsCapability(skillregistry.Capability(use)) {
				problems = append(problems, fmt.Sprintf(
					"%s use %d (%q) is not in the closed capability vocabulary %s",
					prefix, j, use, strings.Join(capabilityNames(), ", ")))
			}
		}
	}
	if len(problems) == 0 {
		return nil
	}
	return fmt.Errorf("invalid [[models]] catalog (%d problem(s)):\n  - %s",
		len(problems), strings.Join(problems, "\n  - "))
}

// LookupModel returns the catalog entry named name. Deterministic exact match
// only: ValidateModels already refuses duplicates, so at most one entry can
// match. Callers that need candidates rather than entries use
// modelruntime.Catalog, which keeps the resolution rules (ordering, refusal
// with the available names) in one place.
func (mc *MachineConfig) LookupModel(name string) (MachineModel, bool) {
	if mc == nil {
		return MachineModel{}, false
	}
	for _, m := range mc.Models {
		if m.Name == name {
			return m, true
		}
	}
	return MachineModel{}, false
}

// capabilityNames returns skillregistry's closed capability vocabulary in its
// canonical order, for error messages. One helper so the spelling cannot
// drift between messages.
func capabilityNames() []string {
	caps := skillregistry.Capabilities()
	out := make([]string, len(caps))
	for i, c := range caps {
		out[i] = string(c)
	}
	sort.Strings(out)
	return out
}
