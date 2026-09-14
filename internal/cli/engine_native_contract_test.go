package cli

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/GMISWE/ieops-aihub/internal/engine"
)

// Contract gate for the native engine's step-progress and pause instructions.
//
// WHAT WENT WRONG
// ---------------
// aihub#265 made the server's pf_get_step the sole authority for step progress and rewrote the
// 23 polyforge-coding step templates to read it. It deliberately did not touch plugins/, so the
// plugin's own native engine kept telling a resuming sub-agent — on the FIRST line of its
// prompt — to read `.pf_steps.json` in the worktree root. Nothing writes that file any more, so
// the one instruction a resuming agent follows first points it at a stale or absent artefact.
// (aihub#353.)
//
// Separately, the newer step templates end a failed step with pf_emit_event(note) +
// pf_pause_attempt. After that the attempt is no longer `running`, and verifyAttemptCredential
// (internal/domain/run_attempts.go, step 5) hard-rejects every subsequent call on its own path —
// pf_update_step, pf_save_artifact, pf_complete_attempt, pf_commit, pf_acquire_locks, pf_wrap.
// (pf_emit_event's lighter check never reads the status, so a paused attempt may still write
// notes — a ruled contract, aihub#585, not a gap.) That is fail-safe, but the auto-mode loop had
// no branch for it: it ran on into a cascade of surprise credential errors and could retry,
// burning tokens on calls that cannot succeed. (aihub#182.)
//
// WHY THESE ASSERTIONS
//   Tag A — no worktree step file is prescribed anywhere in the injected or deferred engine text.
//   Tag B — the resuming instruction names the authority AND comes before the step body, because
//           an instruction placed after the work is not a resume instruction.
//   Tag C — the auto loop has an explicit paused-attempt exit that terminates without retrying
//           and without completing the attempt.
//   Tag E — (aihub#657) the resident loop CALLS `polyforge engine <verb>` for the mechanics
//           internal/engine now owns, and does not keep a second copy of them beside the call.
//           Added when the pseudocode was deleted: until then the loop WAS the implementation,
//           so "does it restate the implementation" was not a question that could be asked.
//   Negative control — every file is non-empty and every "must not contain" check is paired with
//           a positive anchor in the same file, so a renamed file or a typo'd path fails loudly
//           instead of satisfying the ban for free.

// engineNativeDocs are the four documents that carry the native engine's step-progress
// instructions: two resident (injected by hooks/pf-skill-router) and two reachable from them.
var engineNativeDocs = []struct {
	rel string
	// anchor is text that must genuinely be present. It pairs with the Tag A ban: a
	// "must not contain" assertion on a file that was moved, renamed or emptied would
	// otherwise pass while asserting nothing.
	anchor string
}{
	{"skills/pf-execute/engine.native.md", "## Execute (rhs=false, auto mode)"},
	{"skills/pf-execute/references/engine-native-details.md", "--- step instructions ---"},
	{"skills/_common/storage.md", "Artifact type for this step"},
	{"skills/_common/lifecycle.md", "## Bracket every step"},
}

const (
	// The worktree file aihub#265 retired. Nothing writes it; nothing may prescribe it.
	worktreeStepFile = ".pf_steps.json"

	// Tag B: the sub-agent prompt template's landmarks.
	stepInstructionsMarker = "--- step instructions ---"
	stepAuthorityTool      = "pf_get_step"
	stepAuthorityField     = "completed_steps"

	// Tag C: the paused-attempt branch's landmarks in the auto loop.
	pauseTool       = "pf_pause_attempt"
	pauseStopPhrase = "stop the loop"
	pauseNoComplete = "do NOT call pf_complete_attempt"
	completeTool    = "pf_complete_attempt"
)

func readEngineDoc(t *testing.T, pluginRoot, rel string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(pluginRoot, rel))
	if err != nil {
		t.Fatalf("%s: cannot read it (%v). This gate asserts what that file says; if it moved, "+
			"the gate stops covering it rather than going green.", rel, err)
	}
	return string(b)
}

func TestEngineNativeContract(t *testing.T) {
	pluginRoot := pluginRootDir(t)

	// ── Negative control ──────────────────────────────────────────────────────────────────
	// Runs first and independently: every later assertion is either a "must not contain" (free
	// on an empty or missing file) or a search for a landmark (free to typo). Both failure
	// modes are silent successes, which is exactly what this suite exists to prevent.
	t.Run("NegativeControl_FilesPresentAndAnchored", func(t *testing.T) {
		for _, d := range engineNativeDocs {
			body := readEngineDoc(t, pluginRoot, d.rel)
			if strings.TrimSpace(body) == "" {
				t.Errorf("%s is empty — every 'must not prescribe %s' check below would pass "+
					"vacuously on it", d.rel, worktreeStepFile)
				continue
			}
			if !strings.Contains(body, d.anchor) {
				t.Errorf("%s: anchor %q is absent. Either the document was restructured or this "+
					"gate is searching the wrong text; until that is resolved its other "+
					"assertions about this file prove nothing.", d.rel, d.anchor)
			}
		}
	})

	// ── Tag A (aihub#353) ─────────────────────────────────────────────────────────────────
	t.Run("TagA_NoWorktreeStepFilePrescribed", func(t *testing.T) {
		for _, d := range engineNativeDocs {
			body := readEngineDoc(t, pluginRoot, d.rel)
			if n := strings.Count(body, worktreeStepFile); n > 0 {
				t.Errorf("%s names %s %d time(s). aihub#265 made pf_get_step the sole authority "+
					"for step progress and nothing writes that file any more, so every mention "+
					"of it here is an instruction to read or write a stale artefact. Use "+
					"pf_get_step / completed_steps instead.", d.rel, worktreeStepFile, n)
			}
		}
	})

	// ── Tag B (aihub#353) ─────────────────────────────────────────────────────────────────
	// The acceptance criterion is an ORDERING one, so it is asserted as an index comparison:
	// "the template mentions pf_get_step somewhere" would be satisfied by a note appended
	// after the step body, which a sub-agent reads only once the work is already done.
	t.Run("TagB_ResumeInstructionPointsAtAuthorityFirst", func(t *testing.T) {
		const rel = "skills/pf-execute/references/engine-native-details.md"
		body := readEngineDoc(t, pluginRoot, rel)

		tmpl, ok := subAgentPromptTemplate(body)
		if !ok {
			t.Fatalf("%s: could not locate the fenced sub-agent prompt template in §0b. That "+
				"template is the literal text dispatched to every step sub-agent; if it cannot "+
				"be found, nothing below is being checked.", rel)
		}

		idxAuthority := strings.Index(tmpl, stepAuthorityTool)
		idxInstructions := strings.Index(tmpl, stepInstructionsMarker)

		if idxAuthority < 0 {
			t.Errorf("%s §0b: the sub-agent prompt template never names %s. A resuming sub-agent "+
				"is told nothing about where step progress lives.", rel, stepAuthorityTool)
		}
		if !strings.Contains(tmpl, stepAuthorityField) {
			t.Errorf("%s §0b: the sub-agent prompt template never names %s — the field that "+
				"carries which steps are already done.", rel, stepAuthorityField)
		}
		if idxInstructions < 0 {
			t.Fatalf("%s §0b: the template has no %q marker, so the ordering assertion below "+
				"has no reference point", rel, stepInstructionsMarker)
		}
		if idxAuthority >= 0 && idxAuthority >= idxInstructions {
			t.Errorf("%s §0b: %s is mentioned at offset %d, at or after the %q block at offset "+
				"%d. The resume instruction must come BEFORE the step body, or a sub-agent "+
				"reads it only after redoing finished work.",
				rel, stepAuthorityTool, idxAuthority, stepInstructionsMarker, idxInstructions)
		}
	})

	// ── Tag C (aihub#182) ─────────────────────────────────────────────────────────────────
	t.Run("TagC_AutoLoopHasPausedAttemptExit", func(t *testing.T) {
		const rel = "skills/pf-execute/engine.native.md"
		body := readEngineDoc(t, pluginRoot, rel)

		i := strings.Index(body, pauseTool)
		if i < 0 {
			t.Fatalf("%s: the auto-mode loop has no branch naming %s. After a step pauses the "+
				"attempt, every call that authenticates through verifyAttemptCredential is "+
				"hard-rejected (internal/domain/run_attempts.go, step 5; pf_emit_event's lighter "+
				"check still works, by design — aihub#585), so a loop without this branch ends "+
				"in a cascade of surprise errors and may retry.",
				rel, pauseTool)
		}
		// The branch is one contiguous block; the blank line after it bounds the region, so
		// "does not call pf_complete_attempt" is asserted about THIS path and not about the
		// review-FAIL path further down, which legitimately does call it.
		region := body[i:]
		if end := strings.Index(region, "\n\n"); end >= 0 {
			region = region[:end]
		}

		if !strings.Contains(region, pauseStopPhrase) {
			t.Errorf("%s: the %s branch does not say %q. It must terminate the loop rather than "+
				"retry — the rejected calls cannot succeed until a human resumes the attempt.",
				rel, pauseTool, pauseStopPhrase)
		}
		if !strings.Contains(region, pauseNoComplete) {
			t.Errorf("%s: the %s branch does not say %q. The attempt must STAY paused for the "+
				"human; completing it here would destroy the state they are meant to resume.",
				rel, pauseTool, pauseNoComplete)
		}
		if n := strings.Count(region, completeTool); n != 1 {
			t.Errorf("%s: the %s branch mentions %s %d times, expected exactly 1 (the %q "+
				"prohibition). Any further mention on this path is an instruction to complete "+
				"an attempt that must stay paused.", rel, pauseTool, completeTool, n, pauseNoComplete)
		}
	})

	// ── Tag E (aihub#657) ─────────────────────────────────────────────────────────────────
	// The resident loop delegates its mechanics to `polyforge engine <verb>` instead of
	// restating them. Both halves are asserted, because each alone is satisfiable for free: a
	// document can name the verbs AND keep the pseudocode beside them (two answers and no way
	// to tell which is current — the aihub#413 shape), or drop the pseudocode and name nothing
	// (a loop with no instructions at all).
	t.Run("TagE_MechanicsAreDelegatedNotRestated", func(t *testing.T) {
		const rel = "skills/pf-execute/engine.native.md"
		body := readEngineDoc(t, pluginRoot, rel)

		for _, verb := range engineDelegatedVerbs {
			if !strings.Contains(body, verb) {
				t.Errorf("%s does not name %q. aihub#657 deleted the pseudocode this verb "+
					"replaced, so without the call the loop is told neither how to do the work "+
					"nor where the implementation it is a caller of lives.", rel, verb)
			}
		}

		// The specific restatement that was removed: the bracket's next_* argument assembly,
		// which lived here as a second copy of _common/lifecycle.md's and is now
		// `polyforge engine bracket-plan`'s job (engine-native-details.md §0h). Assembling it
		// by hand is the lifecycle-details.md §1 trap — a two-call fallback that drops
		// step_attempt_id leaves current_step_attempt NULL and nothing reports it.
		for _, restated := range []string{"next_step=", "next_step_attempt_id="} {
			if strings.Contains(body, restated) {
				t.Errorf("%s assembles %q by hand again. That is the argument juggling "+
					"bracket-plan exists to own; a second copy here drifts from "+
					"engine.PlanStepBracket with nothing comparing the two, which is exactly "+
					"what internal/cli/engine_bc_contract_test.go was written to prevent.",
					rel, restated)
			}
		}
	})
}

// engineDelegatedVerbs are the CLI calls the resident loop must name, one per piece of
// mechanics aihub#657 removed from it. `cleanup-worktrees` is deliberately NOT here: it is a
// once-per-wi call and lives in _common/references/lifecycle-details.md §0, on the other side
// of the resident/on-demand split whose budget is the reason this thinning happened at all.
var engineDelegatedVerbs = []string{
	"polyforge engine startup",
	"polyforge engine resolve-role",
	"polyforge engine parse-review",
	"polyforge engine bracket-plan",
}

// ─────────────────────────────────────────────────────────────────────────────────────────────
// Tag D (aihub#358) — the per-step model tier compared `level:` against a value the other side
// of the contract never produces.
//
// WHAT WENT WRONG
// ---------------
// engine.native.md selected a per-step model with `step_level(content) == "opus"`. `level:` is
// not a model selector: it is the review-DEPTH argument of the scenario repo's
// common/review/SKILL.md, enumerated quick|medium|deep|challenge. Measured at
// polyforge-coding@6231732 — 9 `level:` lines, every one immediately after
// `@include: common/review/SKILL.md`, values quick×4 / deep×5, and no occurrence of any model
// name anywhere in that repo. The two sets are disjoint, so the branch was unreachable and
// every step has always dispatched default_model. The tiering never fired once, silently,
// because a selector that never matches is indistinguishable from one whose condition is
// simply never true.
//
// WHY THIS SHAPE OF GATE
// A gate on the engine side alone would not have caught this — the engine text was
// self-consistent. What was wrong was a relationship BETWEEN two repos. So the assertion is a
// subset one: every `level:` value the engine names must be one the scenario repo can produce.
// After the aihub#358 correction the engine names none, which makes the subset trivially true —
// so LevelExtractorIsNotBlind and NegativeControl_ModelNameAsLevelIsRejected below exist to
// prove the check still discriminates, since a vacuous gate and a satisfied one look identical.
//
// This gate deliberately does NOT assert any particular depth→model mapping, and after
// aihub#338 there still is none: the wired tier is keyed on the STEP ID, which costs `level:`
// nothing and leaves common/review's depth enumeration untouched. Changing what `level:` means
// would change cost on every project at once and remains the owner's decision; see §0f of
// skills/pf-execute/references/engine-native-details.md.

// scenarioReviewLevels is the vocabulary the scenario repo's `level:` directive can take.
//
// PINNED, deliberately, and reconciled rather than trusted. aihub's CI never checks out
// GMISWE/polyforge-coding — every actions/checkout in ci.yml, contract-lint.yml and
// publish-bins.yml takes this repo only — so a gate that could read only the live tree would
// not run in the one place that gates merges. The anti-rot measure is the
// PinnedVocabularyMatchesLiveScenarioRepo subtest: wherever a checkout IS reachable, this map
// is compared for SET EQUALITY against the enumeration the live repo declares and against every
// value it actually uses, and any divergence fails. Equality, not subset, in both directions: a
// pin that is too small produces false failures and one that is too large produces false
// passes.
//
// Source: common/review/SKILL.md's frontmatter, its four `## Level: <v>` headings and its
// structured_payload contract `"level": "<quick|medium|deep|challenge>"`. Re-derive with
//
//	git -C <workspace>/.repo/polyforge-coding grep -hoE '^## Level: [a-z]+' | awk '{print $3}'
var scenarioReviewLevels = map[string]bool{
	"quick": true, "medium": true, "deep": true, "challenge": true,
}

// levelValuePatterns recognise every syntactic form in which these documents have named a
// `level:` value. All four are taken from text that really shipped, which is what makes the
// blindness check below meaningful rather than a restatement of the regexes:
//
//  1. `level: opus` on an include means…                 (backticked, prose)
//  2. level: deep                                        (a bare directive line, as templates write it)
//  3. **level=opus special case**: pf-execute dispatches… (the bold form this file used)
//  4. model = "opus" if step_level(content) == "opus"     (the pseudo-code comparison)
//
// Deliberately NOT a generic `\blevel\s*=\s*(\w+)`: §0's pseudo-code contains `else level=null`,
// which such a pattern would report as an out-of-vocabulary value. A regex that cries wolf gets
// deleted, so the narrow anchors are the durable choice.
// The patterns are case-insensitive and do not anchor to end-of-line, because the defect only
// has to be REINTRODUCED in a slightly different hand to escape a tighter set: `Level: opus`,
// or `level: opus   # tier` with a trailing comment, or the comparison written the other way
// round or in single quotes. Each of those was a live blind spot in the first version of this
// gate and each is covered by a fixture in LevelExtractorIsNotBlind below.
var levelValuePatterns = []*regexp.Regexp{
	// `level: opus` — backticked, in prose.
	regexp.MustCompile("(?i)`level:[ \t]*([a-z][a-z0-9_-]*)"),
	// A bare directive line, as the step templates write it. No `$`: a trailing comment must
	// not hide the value.
	regexp.MustCompile(`(?mi)^level:[ \t]*([a-z][a-z0-9_-]*)`),
	// **level=opus special case** — the bold form this file itself used.
	regexp.MustCompile(`(?i)\*\*level=([a-z][a-z0-9_-]*)`),
	// The pseudo-code comparison, either operand order, either quote style.
	regexp.MustCompile(`(?i)step_level\([^)]*\)\s*==\s*["']([a-z][a-z0-9_-]*)["']`),
	regexp.MustCompile(`(?i)["']([a-z][a-z0-9_-]*)["']\s*==\s*step_level\(`),
}

// levelValuesIn returns every `level:` value named in body, deduplicated.
func levelValuesIn(body string) map[string]bool {
	out := map[string]bool{}
	for _, re := range levelValuePatterns {
		for _, m := range re.FindAllStringSubmatch(body, -1) {
			if m[1] != "" {
				out[m[1]] = true
			}
		}
	}
	return out
}

// engineLevelDocs are the documents that describe how a step's model is chosen: the resident
// loop and the on-demand file it points at.
var engineLevelDocs = []string{
	"skills/pf-execute/engine.native.md",
	"skills/pf-execute/references/engine-native-details.md",
}

func TestEngineNativeLevelVocabularyContract(t *testing.T) {
	pluginRoot := pluginRootDir(t)

	t.Run("LevelExtractorIsNotBlind", func(t *testing.T) {
		// Anti-vacuity, and it runs FIRST. Every assertion below is "the extractor found nothing
		// out of vocabulary", which an extractor that finds nothing at all satisfies perfectly.
		// These four fixtures are the exact forms that shipped in this tree before aihub#358.
		for _, tc := range []struct{ name, body string }{
			{"backticked prose", "the pinning is easy to get wrong.** `level: opus` on an include means\ndispatch that step with `model: opus`.\n"},
			{"bare directive line", "@include: common/review/SKILL.md\nlevel: opus\n"},
			{"bold form", "   **level=opus special case**: pf-execute dispatches that step with `model: opus` (via\n"},
			{"pseudo-code comparison", "    model = \"opus\" if step_level(content) == \"opus\" else default_model\n"},
			// The four rewordings a reintroduction could plausibly use. These are not
			// hypothetical politeness: each one escaped the first version of this gate.
			{"capitalised directive", "@include: common/review/SKILL.md\nLevel: opus\n"},
			{"directive with trailing comment", "level: opus   # architecture steps only\n"},
			{"single-quoted comparison", "    model = 'opus' if step_level(content) == 'opus' else default_model\n"},
			{"reversed comparison", "    if \"opus\" == step_level(content):\n"},
		} {
			if got := levelValuesIn(tc.body); !got["opus"] {
				t.Errorf("levelValuesIn does not recognise the %s: %q yielded %v. Every "+
					"assertion in this test is that no out-of-vocabulary value was found, so an "+
					"extractor blind to a real syntactic form makes all of them pass for free.",
					tc.name, tc.body, keysOf(got))
			}
		}
		// ...and it must not invent values, or the subset assertion becomes a false-alarm
		// generator and gets deleted by the next person it inconveniences.
		for _, body := range []string{
			"       -> read the next line; if it is \"level: <value>\" record the level, else level=null\n",
			"`level:` is review DEPTH, not a model — §0f says why there is no per-step override.\n",
			"the review depth (`quick`/`medium`/`deep`/`challenge`) is passed straight through\n",
		} {
			if got := levelValuesIn(body); len(got) != 0 {
				t.Errorf("levelValuesIn invented %v from prose that names no level value: %q",
					keysOf(got), body)
			}
		}
	})

	t.Run("EngineNamesNoLevelValueTheScenarioRepoCannotProduce", func(t *testing.T) {
		for _, rel := range engineLevelDocs {
			body := readEngineDoc(t, pluginRoot, rel)
			for v := range levelValuesIn(body) {
				if !scenarioReviewLevels[v] {
					t.Errorf("%s names `level: %s`, but the scenario repo's `level:` is "+
						"common/review's review-DEPTH argument and can only be one of %v. The two "+
						"vocabularies are disjoint, so a selector keyed on this value can never "+
						"match and the behaviour it describes never happens — which is exactly the "+
						"aihub#358 defect. Either the scenario repo must start producing %q "+
						"(a change in polyforge-coding, and it collides with the review-depth "+
						"enumeration that shares this key), or this document must stop claiming it. "+
						"Do not add %q to scenarioReviewLevels to make this pass — that pin "+
						"describes the other repo, not this wish.",
						rel, v, keysOf(scenarioReviewLevels), v, v)
				}
			}
		}
	})

	t.Run("NegativeControl_ModelNameAsLevelIsRejected", func(t *testing.T) {
		// The subset check above passes when the engine names no level value at all, which is the
		// post-fix state. Prove it still has teeth, in both directions: a model name must be
		// rejected and an in-vocabulary depth must be accepted. Without the second half this
		// would also "pass" for a check that rejects everything.
		for _, bad := range []string{"opus", "sonnet", "haiku"} {
			// Both halves of the rejection have to hold, and they fail for different reasons:
			// the extractor must SEE the value, and the pin must not CONTAIN it. Widening the pin
			// is the cheapest way to silence this whole gate, so it is asserted directly.
			if scenarioReviewLevels[bad] {
				t.Errorf("scenarioReviewLevels contains the model name %q — the pin has been "+
					"widened to accommodate the engine instead of describing the scenario repo, "+
					"which disables this gate entirely", bad)
			}
			body := "`level: " + bad + "` on an include means dispatch that step with `model: " + bad + "`."
			if !levelValuesIn(body)[bad] {
				t.Errorf("a document naming `level: %s` would not be flagged: levelValuesIn "+
					"returned %v. This is the exact text aihub#358 removed, so the gate must "+
					"reject it.", bad, keysOf(levelValuesIn(body)))
			}
		}
		for _, good := range []string{"quick", "medium", "deep", "challenge"} {
			body := "@include: common/review/SKILL.md\nlevel: " + good + "\n"
			got := levelValuesIn(body)
			if !got[good] {
				t.Errorf("levelValuesIn missed the in-vocabulary value %q, so this gate cannot "+
					"tell a legitimate depth from a model name", good)
				continue
			}
			if !scenarioReviewLevels[good] {
				t.Errorf("%q is declared by common/review/SKILL.md but is not in "+
					"scenarioReviewLevels — the gate would fail a legitimate template", good)
			}
		}
	})

	t.Run("EngineDocsSayHowTheTierIsChosen", func(t *testing.T) {
		// The subset check above is satisfied by SILENCE: a document that says nothing about
		// model selection passes it. Silence is not good enough — the aihub#358 defect was a
		// reader believing a mechanism worked, so the text has to state what actually happens.
		//
		// Until aihub#338 the true statement was "there is no per-step model tier" and these
		// markers asserted exactly that. aihub#338 WIRED one, on the owner's 2026-09-04
		// decision (task-kind -> tier), so the true statement changed and the markers moved
		// with it — as the note they replace instructed. What did NOT change is the shape: the
		// pair is still two-sided, presence of the truth AND absence of the superseded claim,
		// because a document carrying both would be self-contradicting and each half alone is
		// satisfiable for free.
		for rel, want := range map[string]string{
			"skills/pf-execute/engine.native.md":                    "keyed on the step's KIND, never on `level:`",
			"skills/pf-execute/references/engine-native-details.md": "The model tier is keyed on step KIND",
		} {
			body := readEngineDoc(t, pluginRoot, rel)
			if !strings.Contains(body, want) {
				t.Errorf("%s does not contain %q. Without it the document is merely SILENT "+
					"about how a step's model is chosen, and silence is what let a reader "+
					"assume a broken tier worked. If the wording drifted, move this marker with "+
					"it; deleting it is not the same change.", rel, want)
			}
		}
		for rel, retired := range map[string][]string{
			"skills/pf-execute/engine.native.md":                    {"No per-step model override exists"},
			"skills/pf-execute/references/engine-native-details.md": {"There is no per-step model tier"},
		} {
			body := readEngineDoc(t, pluginRoot, rel)
			for _, claim := range retired {
				if strings.Contains(body, claim) {
					t.Errorf("%s still says %q. That was true until aihub#338 wired the "+
						"task-kind tier and is false now; leaving it beside the new text gives "+
						"the reader two contradictory answers and no way to tell which is "+
						"current.", rel, claim)
				}
			}
		}
		// The mapping has to be a real predicate in the resident loop, not a promise in prose.
		// This is the string the engine dispatches on; if it goes, the tier is aspirational
		// again and the paragraph above becomes the aihub#358 defect with new wording.
		// Since aihub#555 the loop selects an AGENT (whose definition file carries the model)
		// rather than a model name, so the constants are the agent ids.
		body := readEngineDoc(t, pluginRoot, "skills/pf-execute/engine.native.md")
		for _, frag := range []string{`endswith("_review")`, "REVIEW_AGENT", "STEP_AGENT"} {
			if !strings.Contains(body, frag) {
				t.Errorf("skills/pf-execute/engine.native.md no longer contains %q — the step "+
					"body no longer selects a per-step-kind agent, whatever the surrounding "+
					"prose claims", frag)
			}
		}

		// aihub#657 ADDS the check below rather than replacing the three above, and the reason
		// is worth stating because the obvious reading of the thinning is the opposite one.
		//
		// Before aihub#654 the predicate existed once, in markdown, and a substring check was
		// all there was to check. aihub#654 gave it a second home in Go (engine.IsReviewStep)
		// and aihub#657 rewrote the markdown to point at it. Two copies of one predicate is the
		// aihub#294 drift class, and a substring assertion is blind to exactly that failure: it
		// passes as happily on a markdown copy that has quietly diverged from the Go one as on
		// a copy that agrees. So the markdown's own spelling is now PARSED and reconciled
		// against the implementation it claims to describe, in both directions.
		//
		// The three substring checks stay because they are what makes this parse possible: an
		// extractor is only evidence if its anchors are pinned, and deleting them would leave
		// the reconciliation reading an empty set — the silent-success failure this whole suite
		// exists to prevent.
		if !strings.Contains(body, "polyforge engine resolve-role") {
			t.Errorf("skills/pf-execute/engine.native.md no longer names `polyforge engine " +
				"resolve-role`. Since aihub#657 that verb IS the predicate's implementation " +
				"(internal/engine's IsReviewStep, via ResolveRole's tier 3) and the markdown is " +
				"a caller of it. Without the pointer the markdown copy is free-floating prose " +
				"again, and a reader has no way to find out which of the two is authoritative.")
		}
	})

	// EngineReviewPredicateMatchesTheGoImplementation is the reconciliation the subtest above
	// now depends on. It reads the review-step vocabulary out of BOTH engine documents and
	// compares the predicate they describe against engine.IsReviewStep over a probe corpus.
	//
	// Direction matters and both are asserted. A markdown set SMALLER than the Go one demotes a
	// review step to the default tier in any harness following the markdown by hand; a set
	// LARGER than the Go one raises a non-review step to the read-only reviewer agent, which
	// fails the step outright when it tries to edit. Neither is visible in a substring check.
	t.Run("EngineReviewPredicateMatchesTheGoImplementation", func(t *testing.T) {
		suffixes, exact := engineReviewVocabulary(
			readEngineDoc(t, pluginRoot, "skills/pf-execute/engine.native.md"),
			readEngineDoc(t, pluginRoot, "skills/pf-execute/references/engine-native-details.md"),
		)

		// Anti-vacuity, first: an extractor that found nothing would agree with any
		// implementation at all on the corpus below, since both halves would answer false.
		if len(suffixes) == 0 {
			t.Fatalf("neither engine document states the review-step SUFFIX. The reconciliation "+
				"below would then only cover the %d exact names and would silently stop covering "+
				"the suffix rule, which is the half that catches step ids the catalog has never "+
				"seen.", len(exact))
		}
		// ...and the documents must agree with EACH OTHER before either is compared to Go. Three
		// copies of this suffix ship (engine.native.md's loop, §0f's mapping table, §0f's
		// review_fix paragraph); if they disagree, at most one can match engine.IsReviewStep and
		// a reader has no way to tell which.
		if len(suffixes) > 1 {
			t.Fatalf("the engine documents state %d DIFFERENT review-step suffixes (%v). They are "+
				"copies of one predicate, so a reader following the wrong copy dispatches review "+
				"steps to the write-capable executor. Reconciling any one of them against "+
				"engine.IsReviewStep would certify the disagreement rather than catch it.",
				len(suffixes), keysOf(suffixes))
		}
		if len(exact) == 0 {
			t.Fatal("neither engine document enumerates the exact review step ids, so the " +
				"comparison below reduces to the suffix rule and asserts nothing about " +
				"`review` / `code_review` / `release_review`")
		}
		suffix := keysOf(suffixes)[0]

		docSaysReview := func(stepID string) bool {
			if strings.HasSuffix(stepID, suffix) {
				return true
			}
			return exact[stepID]
		}

		// The corpus is the documents' own names plus every step id shape that has actually
		// caused trouble: review_fix (it applies findings, it is ordinary editing work, and
		// endswith must NOT match it — §0f says so in prose, and this is that prose asserted),
		// and two words that merely CONTAIN "review".
		corpus := []string{"review_fix", "spec", "plan", "code_change", "commit_and_pr", "wrap",
			"design_review", "security_review", "reviewer", "previewing", "review"}
		for name := range exact {
			corpus = append(corpus, name)
		}
		sort.Strings(corpus)

		agreements := 0
		for _, stepID := range corpus {
			want := engine.IsReviewStep(stepID)
			if got := docSaysReview(stepID); got != want {
				t.Errorf("step id %q: the engine documents say is_review = %v, "+
					"engine.IsReviewStep says %v. The markdown is executed by hand on the B/C "+
					"path and the Go function by `polyforge engine resolve-role`, so the two "+
					"loops would dispatch this step to DIFFERENT agents — the raised read-only "+
					"reviewer or the default write-capable executor — for the same wi.",
					stepID, got, want)
				continue
			}
			agreements++
		}

		// A corpus on which the two never disagree could also be a corpus on which the
		// predicate is constant. Pin that it is not.
		if !engine.IsReviewStep("code_review") || engine.IsReviewStep("review_fix") {
			t.Errorf("engine.IsReviewStep is no longer discriminating on the corpus "+
				"(code_review=%v, review_fix=%v), so agreement with it means nothing",
				engine.IsReviewStep("code_review"), engine.IsReviewStep("review_fix"))
		}
		t.Logf("reconciled %d step ids against engine.IsReviewStep (suffix %q, exact %v)",
			agreements, suffix, keysOf(exact))
	})

	t.Run("PinnedVocabularyMatchesLiveScenarioRepo", func(t *testing.T) {
		dir := findScenarioRepo()
		if dir == "" {
			// Not a Skip: the subtest above it is the gate, and it ran. This one is the pin's
			// anti-rot check, which needs the other repo. Say so loudly enough that "it was
			// green" is never mistaken for "the pin was verified".
			t.Logf("NOT RECONCILED: no polyforge-coding checkout found, so scenarioReviewLevels "+
				"was not compared against the live repo. This is the expected state in aihub CI, "+
				"which never checks that repo out. To reconcile, run this test with "+
				"PF_SCENARIO_REPO=<path to a polyforge-coding checkout>. Pinned set: %v",
				keysOf(scenarioReviewLevels))
			return
		}
		t.Logf("reconciling scenarioReviewLevels against %s", dir)

		declared, used, err := scenarioLevelVocabulary(dir)
		if err != nil {
			t.Fatalf("reading the scenario repo at %s: %v. It was found, so a read failure means "+
				"the reconciliation did not happen — do not treat that as agreement.", dir, err)
		}
		if len(declared) == 0 {
			t.Fatalf("%s: parsed no `## Level:` headings out of common/review/SKILL.md. The "+
				"comparison below would then pass or fail for a parsing reason rather than a "+
				"vocabulary one.", dir)
		}
		for v := range declared {
			if !scenarioReviewLevels[v] {
				t.Errorf("the scenario repo declares `level: %s` but scenarioReviewLevels does "+
					"not list it. The pin has rotted: update it to %v.", v, keysOf(declared))
			}
		}
		for v := range scenarioReviewLevels {
			if !declared[v] {
				t.Errorf("scenarioReviewLevels lists %q but the scenario repo no longer declares "+
					"it. A pin larger than the truth makes this gate accept a value nothing "+
					"produces — the aihub#358 defect with the sides swapped. Update the pin to %v.",
					v, keysOf(declared))
			}
		}
		for v := range used {
			if !declared[v] {
				t.Errorf("the scenario repo USES `level: %s` in a step template but its "+
					"common/review/SKILL.md does not declare it. Whichever side is wrong, this "+
					"gate's pin cannot describe both.", v)
			}
		}
		t.Logf("scenario repo: declared %v, in use %v", keysOf(declared), keysOf(used))
	})
}

// The review-step predicate as the two engine documents spell it. Two forms, because the two
// documents legitimately write it differently: the resident loop writes Python, and §0f's
// mapping table writes a markdown table cell. Both are read, and their results are UNIONED —
// a predicate stated in one document and contradicted in the other must produce a set that
// disagrees with the Go implementation rather than one the extractor quietly picks a winner for.
var (
	// The suffix rule: endswith("_review"), backticked or not.
	reviewSuffixRe = regexp.MustCompile(`endswith\("([a-z_]+)"\)`)

	// The resident loop's Python tuple: sid in ("review","code_review","release_review")
	reviewTupleRe = regexp.MustCompile(`sid in \(([^)]*)\)`)

	// §0f's table cell: `sid` in `review` / `code_review` / `release_review`
	reviewTableRe = regexp.MustCompile("`sid` in ((?:`[a-z_]+`(?: / )?)+)")

	// The identifiers inside either of those groups, whatever quotes or separators surround them.
	reviewNameRe = regexp.MustCompile(`[a-z][a-z0-9_]*`)
)

// engineReviewVocabulary returns EVERY review-step suffix and exact step id the engine documents
// name, read out of the documents themselves rather than restated here.
//
// Both returns are sets over ALL occurrences in ALL documents, and `suffixes` being a set is the
// load-bearing part. An earlier version took the first suffix it found and ignored the rest,
// which meant the two copies in engine-native-details.md (§0f's mapping table and the review_fix
// paragraph) were never read at all: changing BOTH of them to `_reviewed` left this gate green,
// because engine.native.md is passed first and won. A predicate stated in one document and
// contradicted in another must produce a set the caller can see is inconsistent, not one an
// extractor quietly picks a winner from.
//
// A caller that finds an empty result must treat it as "the documents stopped saying it", never
// as "the documents say nothing matches" — the subtest above fails loudly on both empties for
// exactly that reason.
func engineReviewVocabulary(docs ...string) (suffixes, exact map[string]bool) {
	suffixes, exact = map[string]bool{}, map[string]bool{}
	for _, body := range docs {
		for _, m := range reviewSuffixRe.FindAllStringSubmatch(body, -1) {
			suffixes[m[1]] = true
		}
		for _, re := range []*regexp.Regexp{reviewTupleRe, reviewTableRe} {
			for _, m := range re.FindAllStringSubmatch(body, -1) {
				for _, name := range reviewNameRe.FindAllString(m[1], -1) {
					exact[name] = true
				}
			}
		}
	}
	return suffixes, exact
}

func keysOf(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// findScenarioRepo locates a polyforge-coding checkout, or returns "" when there is none.
// PF_SCENARIO_REPO wins; otherwise walk up from the repo root looking for `.repo/polyforge-coding`,
// which is the layout `polyforge init` creates (and its owner-qualified aihub#327 successor).
func findScenarioRepo() string {
	if p := os.Getenv("PF_SCENARIO_REPO"); p != "" {
		if st, err := os.Stat(p); err == nil && st.IsDir() {
			return p
		}
		return "" // pointed somewhere absent: treated as "not reachable", and the subtest says so
	}
	dir := filepath.Join("..", "..")
	for i := 0; i < 5; i++ {
		for _, name := range []string{"polyforge-coding", "GMISWE__polyforge-coding"} {
			cand := filepath.Join(dir, ".repo", name)
			if st, err := os.Stat(filepath.Join(cand, "common", "review", "SKILL.md")); err == nil && !st.IsDir() {
				return cand
			}
		}
		dir = filepath.Join(dir, "..")
	}
	return ""
}

var (
	levelHeadingRe = regexp.MustCompile(`(?m)^## Level:[ \t]*([a-z][a-z0-9_-]*)`)
	levelPayloadRe = regexp.MustCompile(`"level":[ \t]*"<([a-z|]+)>"`)
)

// scenarioLevelVocabulary reads a polyforge-coding checkout and returns (declared, used):
// the enumeration common/review/SKILL.md declares, and the values the step templates pass to it.
func scenarioLevelVocabulary(dir string) (declared, used map[string]bool, err error) {
	declared, used = map[string]bool{}, map[string]bool{}

	review, err := os.ReadFile(filepath.Join(dir, "common", "review", "SKILL.md"))
	if err != nil {
		return nil, nil, err
	}
	for _, m := range levelHeadingRe.FindAllStringSubmatch(string(review), -1) {
		declared[m[1]] = true
	}
	// Cross-check against that file's own JSON contract, so a heading rename alone cannot move
	// the enumeration without the payload template moving with it.
	if m := levelPayloadRe.FindStringSubmatch(string(review)); m != nil {
		for _, v := range strings.Split(m[1], "|") {
			declared[v] = true
		}
	}

	ents, err := os.ReadDir(dir)
	if err != nil {
		return nil, nil, err
	}
	for _, e := range ents {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") || e.Name() == "README.md" {
			continue
		}
		b, rerr := os.ReadFile(filepath.Join(dir, e.Name()))
		if rerr != nil {
			return nil, nil, rerr
		}
		for _, m := range regexp.MustCompile(`(?m)^level:[ \t]*([a-z][a-z0-9_-]*)[ \t]*$`).
			FindAllStringSubmatch(string(b), -1) {
			used[m[1]] = true
		}
	}
	return declared, used, nil
}

// subAgentPromptTemplate extracts the fenced code block inside §0b of engine-native-details.md —
// the verbatim prompt the auto loop dispatches. Scoping to that section matters: the file
// contains other fenced blocks, and an ordering claim measured across the whole document would
// be about the document's layout rather than about the prompt a sub-agent receives.
func subAgentPromptTemplate(body string) (string, bool) {
	start := strings.Index(body, "## 0b.")
	if start < 0 {
		return "", false
	}
	section := body[start:]
	if end := strings.Index(section[len("## 0b."):], "\n## "); end >= 0 {
		section = section[:len("## 0b.")+end]
	}
	open := strings.Index(section, "```")
	if open < 0 {
		return "", false
	}
	rest := section[open+3:]
	close := strings.Index(rest, "```")
	if close < 0 {
		return "", false
	}
	return rest[:close], true
}
