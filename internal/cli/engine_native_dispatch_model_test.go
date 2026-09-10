package cli

import (
	"regexp"
	"strings"
	"testing"
)

// Tag E (aihub#553) — the dispatch must carry the model EXPLICITLY, in the template the
// executor copies, not only in pseudocode it may skim.
//
// WHAT WENT WRONG
// ---------------
// aihub#338 wired the step-kind model tier (DEFAULT_TIER=sonnet, RAISED_TIER=opus, keyed on
// the step id) into engine.native.md's auto loop. aihub#544 then measured the shipped
// mapping against real transcripts: ALL THREE post-cutover review dispatches carried
// model=None. They ran on the raised tier anyway — but only because the ambient subagent
// default on that box inherits the parent session's model. The selector was therefore
// UNOBSERVABLE, not merely unmeasured: a working raise and a dead one produce identical
// transcripts (the aihub#358 defect with the sign flipped). The cause is placement: the tier
// lived in the loop's pseudocode, while the one block executors actually copy verbatim — the
// §0b dispatch template in engine-native-details.md — never mentioned a model at all.
//
// WHAT IS ASSERTED
//  1. The §0b template carries an explicit `model:` argument, stated as REQUIRED, ahead of
//     the prompt — deleting the argument goes red.
//  2. The template names BOTH tiers, and each name matches the mapping constants declared in
//     engine.native.md — swapping the tiers (in either file) goes red.
//  3. The raised tier is keyed on the review predicate (`is_review`), and the model choice
//     does not mention `level` — re-keying the choice on the review-depth parameter (the
//     aihub#358 defect) goes red.
//  4. engine.native.md's own dispatch line passes `model=` explicitly and points at §0b.
//  5. Anti-vacuity: the extraction helpers are run against fixtures reproducing each mutant,
//     so "no violation found" cannot mean "nothing was parsed".
//
// Deliberately NOT asserted: any per-invocation harness behaviour (whether a given harness
// honours the argument is beat 2, aihub#555's probe), and any particular model *enum* — the
// contract here is the agreement between the two documents, pinned to the owner's 2026-09-04
// task-kind decision recorded on aihub#338.

const (
	dispatchEngineDoc = "skills/pf-execute/engine.native.md"
	dispatchDetailDoc = "skills/pf-execute/references/engine-native-details.md"
)

var (
	// The mapping constants as the resident loop declares them, e.g.
	//   DEFAULT_TIER, RAISED_TIER = "sonnet", "opus"
	tierConstantsRe = regexp.MustCompile(`DEFAULT_TIER, RAISED_TIER = "([a-z][a-z0-9.-]*)", "([a-z][a-z0-9.-]*)"`)

	// The tier names as the §0b template states them, each tied to its constant:
	//   "opus" (RAISED_TIER) when is_review(step_id), else "sonnet" (DEFAULT_TIER)
	raisedNameRe  = regexp.MustCompile(`"([a-z][a-z0-9.-]*)"\s*\(RAISED_TIER\)`)
	defaultNameRe = regexp.MustCompile(`"([a-z][a-z0-9.-]*)"\s*\(DEFAULT_TIER\)`)

	// The binding between the raised tier and the review predicate. Whitespace-tolerant:
	// the template wraps the model argument across lines.
	raisedKeyedOnReviewRe = regexp.MustCompile(`\(RAISED_TIER\)\s+when\s+is_review\(step_id\)`)

	// The resident loop's dispatch line. The model expression is asserted verbatim: it is the
	// single line that makes the tier a dispatch argument rather than narrative.
	engineDispatchRe = regexp.MustCompile(`dispatch Agent\(model=RAISED_TIER if is_review\(step_id\) else DEFAULT_TIER`)
)

// dispatchModelRegion extracts the `model:` argument of the §0b template — the text between
// `model:` and the `prompt:` argument that follows it. ok is false when the template carries
// no model argument at all, or carries it after the prompt (where a copier who stops at the
// prompt never reads it).
func dispatchModelRegion(tmpl string) (string, bool) {
	m := strings.Index(tmpl, "model:")
	p := strings.Index(tmpl, "prompt:")
	if m < 0 || p < 0 || m > p {
		return "", false
	}
	return tmpl[m:p], true
}

func TestEngineNativeDispatchCarriesExplicitModel(t *testing.T) {
	pluginRoot := pluginRootDir(t)
	engine := readEngineDoc(t, pluginRoot, dispatchEngineDoc)
	details := readEngineDoc(t, pluginRoot, dispatchDetailDoc)

	consts := tierConstantsRe.FindStringSubmatch(engine)
	if consts == nil {
		t.Fatalf("%s no longer declares the tier constants (`DEFAULT_TIER, RAISED_TIER = ...`). "+
			"Every assertion below compares the dispatch template against them, so without the "+
			"declaration nothing here checks anything. If the mapping moved, move this regex "+
			"with it.", dispatchEngineDoc)
	}
	defaultTier, raisedTier := consts[1], consts[2]

	t.Run("ConstantsAreModelsNotReviewDepths", func(t *testing.T) {
		// The aihub#358 defect was a tier keyed on the review-DEPTH vocabulary. The constants
		// being IN that vocabulary would reintroduce it one step earlier than the template.
		for _, v := range []string{defaultTier, raisedTier} {
			if scenarioReviewLevels[v] {
				t.Errorf("tier constant %q is a review-depth value (%v) — the mapping has been "+
					"re-keyed onto the vocabulary aihub#358 removed", v, keysOf(scenarioReviewLevels))
			}
		}
		if defaultTier == raisedTier {
			t.Errorf("DEFAULT_TIER and RAISED_TIER are both %q — a mapping with one value "+
				"raises nothing", defaultTier)
		}
	})

	t.Run("EngineLoopDispatchesWithExplicitModel", func(t *testing.T) {
		if !engineDispatchRe.MatchString(engine) {
			t.Errorf("%s: the auto loop's dispatch line no longer passes "+
				"`model=RAISED_TIER if is_review(step_id) else DEFAULT_TIER` explicitly. "+
				"aihub#544 measured what an implicit model does: it inherits the session's "+
				"model, silently, on every step.", dispatchEngineDoc)
		}
	})

	tmpl, ok := subAgentPromptTemplate(details)
	if !ok {
		t.Fatalf("%s: could not locate the fenced §0b dispatch template. It is the text "+
			"executors copy verbatim; if it cannot be found, the model argument below is "+
			"checked against nothing.", dispatchDetailDoc)
	}

	t.Run("TemplateCarriesRequiredModelArgument", func(t *testing.T) {
		region, ok := dispatchModelRegion(tmpl)
		if !ok {
			t.Fatalf("the §0b template carries no `model:` argument ahead of `prompt:`. That "+
				"is the exact aihub#544 state: the tier exists only in pseudocode, every real "+
				"dispatch goes out with model=None, and a dead selector is indistinguishable "+
				"from a live one. Template:\n%s", tmpl)
		}
		if !strings.Contains(region, "REQUIRED") {
			t.Errorf("the template's model argument is not marked REQUIRED — it reads as "+
				"narrative a dispatcher may skim, which is the defect class this gate exists "+
				"to close: %q", region)
		}
		if !strings.Contains(region, "is_review(") {
			t.Errorf("the template's model argument does not key the choice on is_review — "+
				"the step-kind predicate the owner decided on (aihub#338, 2026-09-04): %q", region)
		}
		if strings.Contains(strings.ToLower(region), "level") {
			t.Errorf("the template's model argument mentions `level` — the review-DEPTH "+
				"parameter aihub#358 proved can never select a model: %q", region)
		}

		raised := raisedNameRe.FindStringSubmatch(region)
		deflt := defaultNameRe.FindStringSubmatch(region)
		if raised == nil || deflt == nil {
			t.Fatalf("the model argument does not state BOTH tiers as literal names "+
				"(`\"<name>\" (RAISED_TIER)` / `\"<name>\" (DEFAULT_TIER)`). A template that "+
				"names only one tier leaves the other dispatch implicit — model=None again: %q",
				region)
		}
		if raised[1] != raisedTier {
			t.Errorf("the template raises to %q but %s declares RAISED_TIER = %q — the two "+
				"documents dispatch different models for review steps", raised[1],
				dispatchEngineDoc, raisedTier)
		}
		if deflt[1] != defaultTier {
			t.Errorf("the template defaults to %q but %s declares DEFAULT_TIER = %q — the two "+
				"documents dispatch different models for non-review steps", deflt[1],
				dispatchEngineDoc, defaultTier)
		}
		if !raisedKeyedOnReviewRe.MatchString(region) {
			t.Errorf("the RAISED tier is not bound to the review predicate (`(RAISED_TIER) "+
				"when is_review(step_id)`). Whatever else the words say, the binding is what "+
				"stops a swap: %q", region)
		}
	})

	t.Run("ExtractorsAreNotBlind", func(t *testing.T) {
		// Every assertion above is "no defect was found", which a parser that finds nothing
		// satisfies for free. Each fixture is one of the mutants this gate exists to reject.
		noModel := "Agent(\n  subagent_type: \"general-purpose\",\n  prompt: \"\"\"\nbody\n\"\"\"\n)"
		if _, ok := dispatchModelRegion(noModel); ok {
			t.Error("dispatchModelRegion found a model argument in a template that has none")
		}
		modelAfterPrompt := "Agent(\n  prompt: \"\"\"\nbody\n\"\"\",\n  model: \"opus\"\n)"
		if _, ok := dispatchModelRegion(modelAfterPrompt); ok {
			t.Error("dispatchModelRegion accepted a model argument placed after the prompt — " +
				"a copier who stops at the prompt never reads it")
		}
		swapped := `model: <REQUIRED — "` + defaultTier + `" (RAISED_TIER) when is_review(step_id), else "` +
			raisedTier + `" (DEFAULT_TIER)>, prompt: """`
		region, ok := dispatchModelRegion(swapped)
		if !ok {
			t.Fatal("dispatchModelRegion could not parse the swapped-tier fixture")
		}
		if m := raisedNameRe.FindStringSubmatch(region); m == nil || m[1] != defaultTier {
			t.Errorf("raisedNameRe did not extract the swapped raised name; the tier-agreement "+
				"assertion could not have caught a swap (got %v)", m)
		}
		levelKeyed := `model: <REQUIRED — "opus" (RAISED_TIER) when the step's level is deep>, prompt: """`
		if region, ok := dispatchModelRegion(levelKeyed); ok {
			if strings.Contains(region, "is_review(") || !strings.Contains(strings.ToLower(region), "level") {
				t.Error("the level-keyed fixture was not recognisable as level-keyed — the " +
					"re-keying mutant would pass")
			}
		} else {
			t.Error("dispatchModelRegion could not parse the level-keyed fixture")
		}
		noDispatchModel := strings.Replace(engine,
			"dispatch Agent(model=RAISED_TIER if is_review(step_id) else DEFAULT_TIER",
			"dispatch Agent(", 1)
		if engineDispatchRe.MatchString(noDispatchModel) {
			t.Error("engineDispatchRe still matches an engine whose dispatch line lost its " +
				"model argument")
		}
	})
}
