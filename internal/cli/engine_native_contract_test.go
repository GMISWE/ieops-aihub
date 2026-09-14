package cli

import (
	"fmt"
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
		// Since aihub#664 the loop selects an agent through a ROLE_AGENT dict keyed on the role
		// `polyforge engine resolve-role` resolves — five roles, not a two-way is_review fork —
		// so the fragments pinned here are the dict's declaration and every role's own agent id.
		body := readEngineDoc(t, pluginRoot, "skills/pf-execute/engine.native.md")
		for _, frag := range []string{
			"ROLE_AGENT", "polyforge:step-executor", "polyforge:step-operator",
			"polyforge:step-explorer", "polyforge:step-reviewer", "polyforge:step-designer",
		} {
			if !strings.Contains(body, frag) {
				t.Errorf("skills/pf-execute/engine.native.md no longer contains %q — the step "+
					"body no longer selects a per-role agent, whatever the surrounding prose "+
					"claims", frag)
			}
		}
		for _, retired := range []string{"REVIEW_AGENT", "STEP_AGENT", "is_review("} {
			if strings.Contains(body, retired) {
				t.Errorf("skills/pf-execute/engine.native.md still contains %q — the retired "+
					"two-way is_review predicate (aihub#664) must not be restated in the resident "+
					"loop: it had no branch for operator/explorer/designer, so those three roles "+
					"fell through to the write-capable default agent, a silent capability "+
					"widening for any role the pre-fix predicate did not name.", retired)
			}
		}

		// aihub#657 ADDS the check below rather than replacing the ones above, and the reason is
		// worth stating because the obvious reading of the thinning is the opposite one.
		//
		// Before aihub#654 the predicate existed once, in markdown, and a substring check was
		// all there was to check. aihub#654 gave it a second home in Go (engine.IsReviewStep)
		// and aihub#657 rewrote the markdown to point at it; aihub#664 then generalised the same
		// idea from one predicate to the whole role choice (internal/engine.ResolveRole). Two
		// copies of one decision is the aihub#294 drift class, and a substring assertion is
		// blind to exactly that failure: it passes as happily on a markdown copy that has
		// quietly diverged from the Go one as on a copy that agrees. So the pointer to the
		// single implementation is asserted directly.
		if !strings.Contains(body, "polyforge engine resolve-role") {
			t.Errorf("skills/pf-execute/engine.native.md no longer names `polyforge engine " +
				"resolve-role`. That verb IS the role choice's implementation (internal/engine's " +
				"ResolveRole, tiers 1-3, tier 3 being IsReviewStep) and the markdown is a caller " +
				"of it, not a second author of the predicate. Without the pointer the markdown " +
				"copy is free-floating prose again, and a reader has no way to find out which of " +
				"the two is authoritative.")
		}
	})

	// EngineReviewPredicateMatchesTheGoImplementation used to reconcile the review-step
	// vocabulary the two engine documents spelled out in markdown against engine.IsReviewStep,
	// because before aihub#664 that markdown copy was the ONLY predicate: a substring check was
	// blind to it quietly drifting from the Go implementation it claimed to describe.
	//
	// aihub#664 removed the copy rather than the drift: the documents no longer spell out a
	// suffix rule or an exact-name tuple/table at all — every step's role, review-shaped or
	// not, comes from `polyforge engine resolve-role` (internal/engine.ResolveRole), a single
	// implementation with nothing left in markdown to restate it. Reconciling an EMPTY
	// markdown-side set against Go would not catch a regression, it would certify one: both
	// sides of every comparison would read "not a review step" regardless of what
	// engine.IsReviewStep actually says, and the subtest would stay green while asserting
	// nothing. So this is not the same test with an empty input — it is a different property.
	//
	// The reconciliation this subtest used to do is NOT dropped; it moved to where the
	// authoritative role source now lives, in internal/cli/engine_native_dispatch_model_test.go:
	// RoleAgentDictCoversExactlyTheCatalog and CapabilityAgreesWithTheCatalog compare the
	// ROLE_AGENT dict against the role catalog and its capability, and
	// TestEngineDispatchIsRootedInTheRoleCatalog/TheCatalogAndTheReviewPredicateAgreeOnWhichStepsAreReviews
	// compares the catalog's reviewer-bound step ids against engine.IsReviewStep directly — the
	// same two things this subtest used to compare, minus the now-nonexistent markdown copy in
	// the middle. What is left here is narrower and permanent: guard that the copy does not
	// reappear.
	t.Run("EngineReviewPredicateMatchesTheGoImplementation", func(t *testing.T) {
		docs := map[string]string{
			"skills/pf-execute/engine.native.md": readEngineDoc(t, pluginRoot,
				"skills/pf-execute/engine.native.md"),
			"skills/pf-execute/references/engine-native-details.md": readEngineDoc(t, pluginRoot,
				"skills/pf-execute/references/engine-native-details.md"),
		}
		for rel, body := range docs {
			for _, re := range []*regexp.Regexp{reviewSuffixRe, reviewTupleRe, reviewTableRe} {
				if m := re.FindString(body); m != "" {
					t.Errorf("%s restates a hand-written review-step predicate (%q). aihub#664 "+
						"retired every such copy in favour of `polyforge engine resolve-role` — "+
						"the one implementation, already reconciled against engine.IsReviewStep "+
						"by TestEngineDispatchIsRootedInTheRoleCatalog's "+
						"TheCatalogAndTheReviewPredicateAgreeOnWhichStepsAreReviews subtest. A "+
						"hand-written copy here can drift from it exactly as the pre-aihub#657 "+
						"copy did, silently.", rel, m)
				}
			}
		}
		// Anti-vacuity: the patterns above must still MATCH the shapes they are meant to
		// reject, or "no match" would mean "the regex stopped working" rather than "the doc is
		// clean" — TestEngineNativeDispatchSelectsAgentNotModel's own ExtractorsAreNotBlind
		// subtest does not cover these three regexes (they are private to this file), so it is
		// proven here instead.
		for _, tc := range []struct {
			name string
			re   *regexp.Regexp
			text string
		}{
			{"suffix rule", reviewSuffixRe, `sid.endswith("_review")`},
			{"python tuple", reviewTupleRe, `sid in ("review", "code_review", "release_review")`},
			{"markdown table cell", reviewTableRe, "`sid` in `review` / `code_review` / `release_review`"},
		} {
			if !tc.re.MatchString(tc.text) {
				t.Errorf("%s pattern no longer matches its own reference fixture (%q) — the "+
					"absence checks above would then reject nothing, ever, regardless of what "+
					"the engine documents contain", tc.name, tc.text)
			}
		}
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

// The review-step predicate shapes the two engine documents used to spell out, before aihub#664
// replaced every one of them with a call to `polyforge engine resolve-role`. Kept as an
// ABSENCE guard (EngineReviewPredicateMatchesTheGoImplementation above): a hand-written copy
// reappearing in either document is the aihub#294 drift class returning, silently, the same way
// it did before aihub#657 gave the predicate a Go home.
var (
	// The suffix rule: endswith("_review"), backticked or not.
	reviewSuffixRe = regexp.MustCompile(`endswith\("([a-z_]+)"\)`)

	// The resident loop's Python tuple: sid in ("review","code_review","release_review")
	reviewTupleRe = regexp.MustCompile(`sid in \(([^)]*)\)`)

	// §0f's table cell: `sid` in `review` / `code_review` / `release_review`
	reviewTableRe = regexp.MustCompile("`sid` in ((?:`[a-z_]+`(?: / )?)+)")
)

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

// ─────────────────────────────────────────────────────────────────────────────────────────────
// aihub#672: engine.IsReviewStep has hand-written re-implementations OUTSIDE this repository,
// and the guard that retired the in-tree ones is structurally unable to see them.
//
// EngineReviewPredicateMatchesTheGoImplementation (above) enforces aihub#664's discipline by
// asserting that no hand-written copy of the predicate REAPPEARS in aihub's own engine
// documents. An absence check can only hold over the tree it scans, so it is silent about every
// other repository — and silence is indistinguishable from agreement.
//
// polyforge-coding's .ci/pf_contract_lint.py carries exactly such a copy: `_is_review_step`, a
// hand-written Python re-implementation of engine.IsReviewStep, used by its Rule H to flag a
// review-shaped step that declares a write-capable role. When THIS repository WIDENS the
// predicate — a fourth exact name, or a second suffix — that copy does not move and nothing
// anywhere goes red. A step whose id matches only the new shape then passes Rule H while
// declaring `role: executor`; an explicit `role:` is tier 1 of ResolveRole's fallback and wins
// outright, so a raised-tier READ-ONLY reviewer is replaced by a default-tier WRITE-CAPABLE
// executor. That is the aihub#664 defect class, reopened in the one repository that guard
// cannot reach.
//
// NOTHING FALSE-GREENS TODAY. When this was written every live `role:` declaration in that repo
// agreed with the role catalog. This is a missed-DETECTION gap; it is filed high because the
// failure direction is silent capability widening, not because it is burning.
//
// WHY THE GUARD LIVES HERE AND NOT THERE. Two candidate homes, both measured 2026-09-14:
//
//   - A pin inside polyforge-coding's own .ci/, isomorphic to its `vocabulary_pin` for
//     _KNOWN_ROLES, cannot DETECT this. Widening IsReviewStep leaves both the Python copy and
//     any pin beside it untouched, so that repository stays green. Such a pin is anti-cheat — it
//     forces the eventual repair to be a deliberate two-place edit — not anti-drift. Worth
//     having; not a substitute for this.
//   - Reconciling against a live polyforge-coding checkout, the mechanism
//     PinnedVocabularyMatchesLiveScenarioRepo already uses, DOES NOT RUN IN AIHUB CI:
//     PF_SCENARIO_REPO appears in no workflow and no job checks that repository out.
//     findScenarioRepo's fallback does find the checkout a polyforge workspace keeps under
//     .repo/, so it runs for developers — but that is whatever the developer last pulled.
//     Measured on the workspace this was written in: the discoverable checkout predated the
//     commit that introduced `_is_review_step` and contained ZERO occurrences of it. A stale
//     checkout reconciles CLEAN against a copy that exists.
//
// So the load-bearing assertion has to be one that runs unconditionally, in aihub CI, on the
// pull request that widens the predicate. That is what this test is: a change detector, and
// deliberately so. It does not verify the out-of-tree copies are in sync — from here it cannot.
// What it does is make any edit to the predicate's body fail this test, and hand the author the
// list of copies to update. ReconcilesAgainstLiveScenarioRepo adds the verification half
// wherever a checkout happens to be reachable, and says so loudly when it is not.
//
// ⚠️ THE SCOPE OF THAT CLAIM WAS CORRECTED ONCE, so do not re-broaden it. This comment first
// read "makes widening the predicate impossible to do SILENTLY", which a review measured FALSE:
// the structured extraction below reads two SHAPES only — a quoted literal inside
// strings.HasSuffix(stepID, "…"), and the literals inside the `switch stepID` block — and two
// perfectly ordinary widenings contribute a literal to NEITHER, so they escaped with exit 0:
//
//	if strings.HasSuffix(stepID, "_review") || stepID == "arch_audit" {   // a name via ==
//	const auditSuffix = "_audit"; strings.HasSuffix(stepID, auditSuffix)  // a suffix via a const
//
// pinnedReviewPredicateBody was added to close them, and it closes the general case rather than
// those two instances: it compares the whole normalised body text, so ANY edit to the predicate
// is red. Two lesser assertions were kept beside it because they fail with an actionable message
// naming what changed, which a body-text diff does not. If you ever replace the body pin with
// something cleverer, re-measure the two forms above before claiming coverage.
//
// MEASURED BEFORE WRITING IT (copy-based backup of internal/engine/role.go, restored after):
// with `arch_audit` appended to the exact-name arm, and separately with a second `_audit`
// suffix added — each grep-proven present in the tree — `go test ./internal/cli/...
// ./internal/engine/... ./internal/roles/... ./internal/drain/...` exited 0 BOTH times. Nothing
// in this repository caught either widening. internal/engine's TestIsReviewStep is a table of
// cases that all still pass, and TheCatalogAndTheReviewPredicateAgreeOnWhichStepsAreReviews
// iterates the role catalog's step ids, so a newly accepted name that is absent from the catalog
// is never probed.

// reviewPredicateSourceRel is engine.IsReviewStep's home, relative to this package's directory
// (a Go test runs with its own package directory as the working directory).
const reviewPredicateSourceRel = "../engine/role.go"

// reviewPredicateCopies names every EXECUTABLE hand-written re-implementation of
// engine.IsReviewStep known to live outside this repository, so the failure below can tell an
// author WHAT TO GO UPDATE rather than merely that something changed. Add to this list rather
// than to a comment: it is what the failure message prints.
//
// Surveyed 2026-09-14 with `git grep -nE '_is_review|IsReviewStep|_review' -- '.ci/' 'scripts/'`
// in both repositories: aihub's own scripts/pf_contract_lint.py has none, and polyforge-coding's
// .ci/pf_contract_lint_vendored.py — vendored wholesale from GMI-marketplace — has none either.
//
// "EXECUTABLE" is doing work in that first sentence and is not hedging: polyforge-coding's
// README also states the predicate in PROSE, and that statement has already drifted once and
// been corrected (its f7412d3, "correct the Rule H is_review sentence"). Prose cannot widen a
// role's capability, so it is deliberately out of this list and out of this gate's scope — but
// it is a second thing to update by hand, so it is recorded here rather than left to be
// rediscovered.
var reviewPredicateCopies = []string{
	"polyforge-coding .ci/pf_contract_lint.py::_is_review_step " +
		"(Rule H's review-shape-vs-declared-role check), plus its self-test pin if one exists",
	"polyforge-coding README.md — PROSE restatement, not executable; update for accuracy, " +
		"it cannot cause the capability-widening defect itself",
}

// The pinned decision surface of engine.IsReviewStep: the suffixes it accepts and the exact
// names it accepts. Widening either set is a real decision with out-of-tree consequences; this
// pin makes it a VISIBLE one. Update this pin and every entry in reviewPredicateCopies in the
// same reviewable change.
var (
	pinnedReviewSuffixes   = map[string]bool{"_review": true}
	pinnedReviewExactNames = map[string]bool{
		"review": true, "code_review": true, "release_review": true,
	}
)

// pinnedReviewPredicateBody is engine.IsReviewStep's whole body, normalised by normaliseGoBody.
// It is the assertion that actually bounds the predicate; the two set pins beside it exist for
// their error messages. See the ⚠️ paragraph above for the two widenings that escaped before
// this existed.
//
// Brittle ON PURPOSE. For a function with hand-written re-implementations in another repository,
// "you cannot edit this without one deliberate line here" is the correct cost. normaliseGoBody
// strips line comments and collapses whitespace runs, so the two edits that change the text
// WITHOUT changing behaviour do not cost anything: adding a comment, and gofmt re-wrapping a
// long line. Both verified 2026-09-14 to normalise byte-identical to this value.
const pinnedReviewPredicateBody = `if strings.HasSuffix(stepID, "_review") { return true } ` +
	`switch stepID { case "review", "code_review", "release_review": return true } return false`

var (
	// The Go function's body, from its signature to the closing brace in column 0. Inner blocks
	// are indented, so `\n}` cannot terminate early on one of them.
	goReviewPredicateRe = regexp.MustCompile(`(?s)func IsReviewStep\(stepID string\) bool \{\n(.*?)\n\}`)
	goSuffixCallRe      = regexp.MustCompile(`strings\.HasSuffix\(stepID, "([^"]*)"\)`)

	// The `switch stepID { … }` block, whose string literals are the exact accepted names.
	// Scoped to the BLOCK, not to `case` LINES: the first version of this matched
	// `(?m)^[ \t]*case[ \t]+(.+):[ \t]*$`, and an end-of-line anchor stops matching the moment
	// gofmt wraps a long arm across two lines or someone appends a trailing `// comment` —
	// neither of which changes the surface, so the gate went red claiming the predicate had
	// lost every name. Measured in review, 2026-09-14; both forms are regression fixtures in
	// ExtractorsAreNotBlind now.
	goSwitchBlockRe = regexp.MustCompile(`(?s)switch stepID \{(.*?)\n[ \t]*\}`)

	stringLiteralRe = regexp.MustCompile(`"([^"]*)"`)
	goLineCommentRe = regexp.MustCompile(`//[^\n]*`)
	whitespaceRunRe = regexp.MustCompile(`\s+`)

	pySuffixCallRe = regexp.MustCompile(`endswith\("([^"]*)"\)`)
	pyMembershipRe = regexp.MustCompile(`(?s)step_id in \(([^)]*)\)`)
)

// normaliseGoBody renders a Go function body as one canonical line: line comments stripped,
// whitespace runs collapsed to a single space, trimmed. Those two normalisations are exactly
// what stops pinnedReviewPredicateBody from firing on a comment edit or a gofmt re-wrap.
func normaliseGoBody(body string) string {
	stripped := goLineCommentRe.ReplaceAllString(body, "")
	return strings.TrimSpace(whitespaceRunRe.ReplaceAllString(stripped, " "))
}

// goReviewPredicateSurface extracts the literal decision surface of engine.IsReviewStep out of
// the Go SOURCE of internal/engine/role.go.
//
// Reading the source rather than probing the compiled function is deliberate. Probing can only
// answer for the step ids it happens to ask about, so a fourth accepted name outside the probe
// corpus would be invisible — and "a name nobody thought to probe" is precisely the change this
// gate exists to catch. ThePinAgreesWithTheRunningPredicate binds the text back to behaviour so
// that reading the wrong text cannot pass as agreement.
//
// It also returns the normalised body, which is what pinnedReviewPredicateBody is compared
// against. The two sets are a decomposition of that body for messaging purposes and are NOT the
// bound: they read two shapes, and a widening expressed any other way contributes to neither.
//
// ok is false when the function could not be located at all. That must be a FAILURE and never an
// empty (and therefore trivially agreeing) surface.
func goReviewPredicateSurface(src string) (suffixes, names map[string]bool, body string, ok bool) {
	m := goReviewPredicateRe.FindStringSubmatch(src)
	if m == nil {
		return nil, nil, "", false
	}
	suffixes, names = map[string]bool{}, map[string]bool{}
	for _, s := range goSuffixCallRe.FindAllStringSubmatch(m[1], -1) {
		suffixes[s[1]] = true
	}
	for _, blk := range goSwitchBlockRe.FindAllStringSubmatch(m[1], -1) {
		for _, lit := range stringLiteralRe.FindAllStringSubmatch(blk[1], -1) {
			names[lit[1]] = true
		}
	}
	return suffixes, names, normaliseGoBody(m[1]), true
}

// pythonDefBody returns the body of a top-level `def <name>(` in Python source: the signature
// line and every line after it up to the next line that starts in column 0 and is not blank.
// ok is false when the def is absent, which the caller must not read as "the body is empty".
//
// A def at byte 0 is handled explicitly; searching only for "\ndef " would miss it and report a
// present function as absent. An INDENTED def (a class method) is still not found — the copy is
// module-level today, and the caller's not-found branch logs loudly rather than passing quietly,
// so that limitation surfaces as "not reconciled" rather than as agreement.
func pythonDefBody(src, name string) (body string, ok bool) {
	needle := "def " + name + "("
	idx := strings.Index(src, "\n"+needle)
	switch {
	case strings.HasPrefix(src, needle):
		idx = -1 // the def starts the file; the loop below wants src from byte 0
	case idx < 0:
		return "", false
	}
	var out []string
	for i, ln := range strings.Split(src[idx+1:], "\n") {
		if i > 0 && ln != "" && !strings.HasPrefix(ln, " ") && !strings.HasPrefix(ln, "\t") {
			break
		}
		out = append(out, ln)
	}
	return strings.Join(out, "\n"), true
}

// pythonReviewPredicateSurface extracts the same two sets out of a hand-written Python copy of
// the predicate. Same ok contract as goReviewPredicateSurface.
func pythonReviewPredicateSurface(src string) (suffixes, names map[string]bool, ok bool) {
	body, found := pythonDefBody(src, "_is_review_step")
	if !found {
		return nil, nil, false
	}
	suffixes, names = map[string]bool{}, map[string]bool{}
	for _, s := range pySuffixCallRe.FindAllStringSubmatch(body, -1) {
		suffixes[s[1]] = true
	}
	if m := pyMembershipRe.FindStringSubmatch(body); m != nil {
		for _, lit := range stringLiteralRe.FindAllStringSubmatch(m[1], -1) {
			names[lit[1]] = true
		}
	}
	return suffixes, names, true
}

// diffSets reports what got has that want does not, and vice versa, sorted.
func diffSets(got, want map[string]bool) (added, removed []string) {
	for k := range got {
		if !want[k] {
			added = append(added, k)
		}
	}
	for k := range want {
		if !got[k] {
			removed = append(removed, k)
		}
	}
	sort.Strings(added)
	sort.Strings(removed)
	return added, removed
}

func TestReviewPredicateSurfaceIsPinnedForOutOfTreeCopies(t *testing.T) {
	// ── Anti-vacuity, and it runs FIRST, on FIXTURES rather than on the live files. ──────────
	// Every assertion below compares an extracted set against a pin, and an extractor that finds
	// nothing agrees with nothing — which reads as "the predicate is unchanged" no matter what
	// the source actually says. Both fixtures deliberately carry a WIDER surface than the pin (a
	// fourth name AND a second suffix): an extractor hard-wired to today's three names would
	// satisfy a fixture built from today's source while staying blind to the exact change this
	// gate exists to catch.
	t.Run("ExtractorsAreNotBlind", func(t *testing.T) {
		goFixture := "func IsReviewStep(stepID string) bool {\n" +
			"\tif strings.HasSuffix(stepID, \"_review\") {\n\t\treturn true\n\t}\n" +
			"\tif strings.HasSuffix(stepID, \"_audit\") {\n\t\treturn true\n\t}\n" +
			"\tswitch stepID {\n" +
			"\tcase \"review\", \"code_review\", \"release_review\", \"arch_audit\":\n" +
			"\t\treturn true\n\t}\n\treturn false\n}\n"
		sfx, names, _, ok := goReviewPredicateSurface(goFixture)
		if !ok {
			t.Fatalf("goReviewPredicateSurface could not locate IsReviewStep in its own reference " +
				"fixture, so every 'the surface matches the pin' check below compares two empty " +
				"sets and passes for free")
		}
		if !sfx["_review"] || !sfx["_audit"] || len(sfx) != 2 {
			t.Errorf("goReviewPredicateSurface read suffixes %v from a fixture declaring two "+
				"(_review, _audit) — it cannot see a second suffix being added", keysOf(sfx))
		}
		if !names["arch_audit"] || len(names) != 4 {
			t.Errorf("goReviewPredicateSurface read names %v from a fixture declaring four — it "+
				"cannot see a fourth exact name being added", keysOf(names))
		}
		if _, _, _, ok := goReviewPredicateSurface("func SomethingElse() {}\n"); ok {
			t.Errorf("goReviewPredicateSurface claims success on source that does not define " +
				"IsReviewStep. A renamed or moved predicate would then read as an empty surface " +
				"and agree with any pin")
		}

		// FALSE-RED regression fixtures, both measured in review 2026-09-14 against the first
		// version of this gate, which matched `case` LINES with an end-of-line anchor. Each of
		// these leaves the accepted surface completely unchanged, so each MUST extract today's
		// three names — the old regex extracted zero and the gate went red claiming the
		// predicate had lost every name.
		for _, tc := range []struct{ name, caseArm string }{
			{"trailing comment on the case arm",
				"\tcase \"review\", \"code_review\", \"release_review\": // the three exact names\n"},
			{"gofmt-wrapped case arm",
				"\tcase \"review\", \"code_review\",\n\t\t\"release_review\":\n"},
		} {
			src := "func IsReviewStep(stepID string) bool {\n" +
				"\tif strings.HasSuffix(stepID, \"_review\") {\n\t\treturn true\n\t}\n" +
				"\tswitch stepID {\n" + tc.caseArm +
				"\t\treturn true\n\t}\n\treturn false\n}\n"
			gotSfx, gotNames, gotBody, gotOK := goReviewPredicateSurface(src)
			if !gotOK || len(gotNames) != 3 || !gotNames["release_review"] || len(gotSfx) != 1 {
				t.Errorf("%s: extracted suffixes %v / names %v, want the unchanged surface "+
					"[_review] / [code_review release_review review]. This shape changes no "+
					"behaviour, so reporting a different surface for it is a FALSE RED.",
					tc.name, keysOf(gotSfx), keysOf(gotNames))
			}
			// ...and the body pin must be indifferent to both, or the same innocent edit that
			// no longer trips the set pins would trip the body pin instead.
			if gotBody != pinnedReviewPredicateBody {
				t.Errorf("%s: normalised body %q != pinnedReviewPredicateBody %q. Comment "+
					"stripping and whitespace collapsing exist precisely so this shape costs "+
					"nothing.", tc.name, gotBody, pinnedReviewPredicateBody)
			}
		}

		// The two widenings that escaped the set pins entirely (review, 2026-09-14). Neither
		// contributes a literal to a HasSuffix call or to the switch block, so the sets alone
		// stay equal to the pin — which is the whole reason pinnedReviewPredicateBody exists.
		// Assert on the BODY here: these must be detectable, and the body is what detects them.
		for _, tc := range []struct{ name, src string }{
			{"exact name smuggled in as an == comparison",
				"func IsReviewStep(stepID string) bool {\n" +
					"\tif strings.HasSuffix(stepID, \"_review\") || stepID == \"arch_audit\" {\n" +
					"\t\treturn true\n\t}\n\tswitch stepID {\n" +
					"\tcase \"review\", \"code_review\", \"release_review\":\n" +
					"\t\treturn true\n\t}\n\treturn false\n}\n"},
			{"second suffix behind a named constant",
				"func IsReviewStep(stepID string) bool {\n" +
					"\tif strings.HasSuffix(stepID, \"_review\") {\n\t\treturn true\n\t}\n" +
					"\tif strings.HasSuffix(stepID, auditSuffix) {\n\t\treturn true\n\t}\n" +
					"\tswitch stepID {\n" +
					"\tcase \"review\", \"code_review\", \"release_review\":\n" +
					"\t\treturn true\n\t}\n\treturn false\n}\n"},
		} {
			_, _, gotBody, gotOK := goReviewPredicateSurface(tc.src)
			if !gotOK {
				t.Errorf("%s: could not locate the predicate at all", tc.name)
				continue
			}
			if gotBody == pinnedReviewPredicateBody {
				t.Errorf("%s: normalised body is IDENTICAL to pinnedReviewPredicateBody, so this "+
					"widening is invisible to every assertion in this test. That is the exact "+
					"hole the body pin was added to close — re-measure before weakening it.",
					tc.name)
			}
		}

		// The Python fixture is the upstream copy's shape, widened the same two ways. Proving it
		// here rather than against the live file matters: the scenario checkout may be stale or
		// absent, so an extractor only ever exercised there could be blind and never say so.
		pyFixture := "\ndef _is_review_step(step_id: str) -> bool:\n" +
			"    \"\"\"engine.IsReviewStep, verbatim: an `_review` suffix or one of three names.\"\"\"\n" +
			"    return step_id.endswith(\"_review\") or step_id.endswith(\"_audit\") or step_id in (\n" +
			"        \"review\", \"code_review\", \"release_review\", \"arch_audit\")\n" +
			"\n\ndef _next_function():\n    return 1\n"
		psfx, pnames, pok := pythonReviewPredicateSurface(pyFixture)
		if !pok {
			t.Fatalf("pythonReviewPredicateSurface could not locate _is_review_step in its own " +
				"reference fixture")
		}
		if !psfx["_review"] || !psfx["_audit"] || len(psfx) != 2 {
			t.Errorf("pythonReviewPredicateSurface read suffixes %v from a fixture declaring two",
				keysOf(psfx))
		}
		if !pnames["arch_audit"] || len(pnames) != 4 {
			t.Errorf("pythonReviewPredicateSurface read names %v from a fixture declaring four",
				keysOf(pnames))
		}
		if got, _, ok := pythonReviewPredicateSurface("def other(x):\n    return x\n"); ok {
			t.Errorf("pythonReviewPredicateSurface claims success (%v) on source with no "+
				"_is_review_step, so a deleted copy and a mis-parsed one would look the same",
				keysOf(got))
		}
		// It must also not bleed past the def it was asked for: the next function's own literals
		// would otherwise be reported as part of the predicate's surface.
		if body, _ := pythonDefBody(pyFixture, "_is_review_step"); strings.Contains(body, "_next_function") {
			t.Errorf("pythonDefBody ran past the end of _is_review_step into the following "+
				"definition; extracted body was %q", body)
		}
	})

	// ── The gate. Unconditional: no environment variable, no second checkout, no network. ────
	t.Run("SurfaceMatchesThePin", func(t *testing.T) {
		b, err := os.ReadFile(reviewPredicateSourceRel)
		if err != nil {
			t.Fatalf("%s: cannot read it (%v). This gate pins what that file declares; if it "+
				"moved, the gate must stop covering it loudly rather than go green.",
				reviewPredicateSourceRel, err)
		}
		sfx, names, body, ok := goReviewPredicateSurface(string(b))
		if !ok {
			t.Fatalf("%s no longer defines `func IsReviewStep(stepID string) bool`. Either it was "+
				"renamed or moved — in which case this pin and every copy in %v needs revisiting "+
				"— or this extractor's shape assumption broke. Both are red, neither is green.",
				reviewPredicateSourceRel, reviewPredicateCopies)
		}

		// ── The bound: the whole body. Checked FIRST, because it is the assertion that actually
		// holds, and the set comparisons below are its more legible decomposition.
		if body != pinnedReviewPredicateBody {
			t.Errorf("engine.IsReviewStep's BODY changed.\n  now:    %q\n  pinned: %q\n\n"+
				"This pin is brittle deliberately: the predicate has hand-written "+
				"re-implementations outside this repository and nothing in either tree compares "+
				"them:\n  %s\n\nIf you changed which step ids are review-shaped, update every "+
				"copy above and then this pin, in one reviewable change. If the behaviour is "+
				"identical and only the text moved, update this pin alone and say so in the "+
				"commit message — but check the surface report below first, because a widening "+
				"that adds no string literal (an `==` comparison, a suffix behind a constant) "+
				"looks exactly like a pure refactor to every other assertion here.",
				body, pinnedReviewPredicateBody, strings.Join(reviewPredicateCopies, "\n  "))
		}

		if len(sfx) == 0 || len(names) == 0 {
			// Deliberately worded as a SHAPE failure, not as "the predicate went empty". The
			// Python branch below got this distinction from the start; the Go branch did not,
			// and a review found it pointed the reader at role.go's decision surface when the
			// real fault was this extractor. Same asymmetry, now closed.
			t.Fatalf("%s: extracted suffixes %v and names %v from the LIVE predicate, and it has "+
				"BOTH today. Read that as THIS gate's shape assumption breaking rather than as "+
				"the predicate going empty: goSuffixCallRe reads `strings.HasSuffix(stepID, "+
				"\"…\")` and goSwitchBlockRe reads the literals inside `switch stepID { … }`. "+
				"Teach the extractor the new shape. (The body pin above is the assertion that "+
				"bounds behaviour; this one only keeps the messages honest.)",
				reviewPredicateSourceRel, keysOf(sfx), keysOf(names))
		}

		// Every string literal in the body must be accounted for by one of the two sets. This
		// is what turns `|| stepID == "arch_audit"` — an exact name smuggled in as a comparison
		// instead of a case arm — into a NAMED failure rather than leaving it to the body pin's
		// more generic "the body changed".
		for _, lit := range stringLiteralRe.FindAllStringSubmatch(body, -1) {
			if sfx[lit[1]] || names[lit[1]] {
				continue
			}
			t.Errorf("engine.IsReviewStep's body contains the string literal %q, which is "+
				"neither a suffix this gate read nor an exact name it read. If that literal is "+
				"a step id the predicate now accepts, it WIDENS the surface while leaving both "+
				"set pins equal to their pinned values — update %v and this gate's pins. If it "+
				"is unrelated to the decision, this accounting check needs to learn to skip it.",
				lit[1], reviewPredicateCopies)
		}

		for _, c := range []struct {
			what       string
			got, want  map[string]bool
			pinnedName string
		}{
			{"suffix", sfx, pinnedReviewSuffixes, "pinnedReviewSuffixes"},
			{"exact name", names, pinnedReviewExactNames, "pinnedReviewExactNames"},
		} {
			added, removed := diffSets(c.got, c.want)
			if len(added) == 0 && len(removed) == 0 {
				continue
			}
			t.Errorf("engine.IsReviewStep's %s surface changed: %s now reads %v, the pin %s says "+
				"%v (added %v, removed %v).\n\n"+
				"THIS IS NOT A TEST TO SILENCE BY EDITING THE PIN ALONE. The predicate has "+
				"hand-written re-implementations outside this repository, and nothing in either "+
				"tree compares them:\n  %s\n\n"+
				"Widening the surface without updating those copies is the aihub#664 defect "+
				"class. A step id matching only the NEW shape stays unknown to the copy, so a "+
				"lint that keys on it lets `role: executor` through — and an explicit `role:` is "+
				"tier 1 of ResolveRole and wins outright, replacing a read-only reviewer with a "+
				"write-capable executor. Update every copy above, then update the pin, in one "+
				"reviewable change.",
				c.what, reviewPredicateSourceRel, keysOf(c.got), c.pinnedName, keysOf(c.want),
				added, removed, strings.Join(reviewPredicateCopies, "\n  "))
		}
	})

	// ── Bind the pinned TEXT to the predicate that actually runs. ────────────────────────────
	// SurfaceMatchesThePin reads source; on its own it would also pass if the regexes had latched
	// onto some other function's literals. Checking both directions closes that: a predicate that
	// returned true for everything would satisfy the first loop and fail the second.
	t.Run("ThePinAgreesWithTheRunningPredicate", func(t *testing.T) {
		for s := range pinnedReviewSuffixes {
			if id := "some_step" + s; !engine.IsReviewStep(id) {
				t.Errorf("pinnedReviewSuffixes lists %q, but engine.IsReviewStep(%q) is false — "+
					"the pin describes text that is not this predicate", s, id)
			}
		}
		for n := range pinnedReviewExactNames {
			if !engine.IsReviewStep(n) {
				t.Errorf("pinnedReviewExactNames lists %q, but engine.IsReviewStep(%q) is false — "+
					"the pin describes text that is not this predicate", n, n)
			}
		}
		for _, id := range []string{"code_change", "commit_and_pr", "prepare_context", "build", "test"} {
			if engine.IsReviewStep(id) {
				t.Errorf("engine.IsReviewStep(%q) is true, yet %q is neither a pinned exact name "+
					"nor carries a pinned suffix %v. The pin no longer bounds the predicate's "+
					"surface, so agreement with it proves nothing.",
					id, id, keysOf(pinnedReviewSuffixes))
			}
		}
	})

	// ── The verification half: real where a checkout is reachable, honest where it is not. ────
	t.Run("ReconcilesAgainstLiveScenarioRepo", func(t *testing.T) {
		notReconciled := func(why string) {
			// Never t.Skip and never silent: the gate above is what protects this property, and
			// a quiet pass here would be read as "the copy was checked". Say which it was.
			t.Logf("NOT RECONCILED (%s). engine.IsReviewStep's surface was NOT compared against "+
				"any out-of-tree copy. This is the expected state in aihub CI, which checks no "+
				"other repository out; SurfaceMatchesThePin above is the assertion that runs "+
				"there. To reconcile, run with PF_SCENARIO_REPO=<path to a polyforge-coding "+
				"checkout>. Pinned surface: suffixes %v, names %v.",
				why, keysOf(pinnedReviewSuffixes), keysOf(pinnedReviewExactNames))
		}

		dir := findScenarioRepo()
		if dir == "" {
			notReconciled("no polyforge-coding checkout found")
			return
		}
		lintRel := filepath.Join(".ci", "pf_contract_lint.py")
		b, err := os.ReadFile(filepath.Join(dir, lintRel))
		if err != nil {
			notReconciled(fmt.Sprintf("%s has no readable %s: %v", dir, lintRel, err))
			return
		}
		sfx, names, ok := pythonReviewPredicateSurface(string(b))
		if !ok {
			// Deliberately not a failure. Deleting that copy in favour of calling
			// `polyforge engine resolve-role` is the GOAL state, and it looks identical from
			// here to a checkout that predates the copy. ExtractorsAreNotBlind is what rules out
			// the third reading, a broken extractor.
			notReconciled(fmt.Sprintf("%s/%s defines no _is_review_step — either that copy is "+
				"gone (the goal state) or this checkout predates it, and this gate cannot tell "+
				"those apart", dir, lintRel))
			return
		}
		// Distinguish "the copy accepts nothing" from "this gate can no longer read it". Without
		// this the diff below would report the copy as missing names it plainly has — a true
		// statement of the extracted sets and a false account of the file, sending the reader to
		// fix the wrong thing. The realistic trigger is the copy being refactored to hold its
		// surface in constants (`endswith(_REVIEW_SUFFIXES)`), which is exactly what adding a
		// pin on that side would tempt someone to do.
		//
		// Tested PER SIDE, not with `len(sfx) == 0 && len(names) == 0`. A review found that a
		// PARTIAL refactor — suffix moved behind a constant, membership still a literal tuple —
		// leaves names non-empty, slips past an AND, and gets reported as drift. The discriminator
		// is whether the construct is PRESENT in the body but unreadable: `endswith` appearing
		// with no literal extracted means the shape changed, whereas `endswith` being absent
		// altogether is a genuine narrowing and belongs in the drift report below.
		pyBody, _ := pythonDefBody(string(b), "_is_review_step")
		for _, c := range []struct {
			construct, marker string
			got               map[string]bool
		}{
			{"suffix test", "endswith", sfx},
			{"exact-name membership test", "step_id in", names},
		} {
			if len(c.got) > 0 || !strings.Contains(pyBody, c.marker) {
				continue
			}
			t.Errorf("%s/%s defines _is_review_step and its body mentions `%s`, but no literal "+
				"could be read out of that %s. Treat this as THIS gate's shape assumption "+
				"breaking, NOT as the copy having narrowed: it reads only the literal forms "+
				"`endswith(\"…\")` and `step_id in (…)`. Teach pythonReviewPredicateSurface the "+
				"new shape rather than reading it as drift.",
				dir, lintRel, c.marker, c.construct)
			return
		}
		if len(sfx) == 0 && len(names) == 0 {
			t.Errorf("%s/%s defines _is_review_step but neither a suffix test nor an exact-name "+
				"membership test could be read out of it, and its body mentions neither "+
				"construct. Either the copy no longer decides anything — in which case Rule H "+
				"treats every review step as ordinary work — or it was rewritten in a shape this "+
				"gate cannot read. Both are red.", dir, lintRel)
			return
		}
		t.Logf("reconciling engine.IsReviewStep against %s/%s", dir, lintRel)

		for _, c := range []struct {
			what      string
			got, want map[string]bool
		}{
			{"suffix", sfx, pinnedReviewSuffixes},
			{"exact name", names, pinnedReviewExactNames},
		} {
			added, removed := diffSets(c.got, c.want)
			if len(added) == 0 && len(removed) == 0 {
				continue
			}
			t.Errorf("%s/%s::_is_review_step has DRIFTED from engine.IsReviewStep on the %s "+
				"surface: the copy accepts %v, this repository's predicate accepts %v (copy has "+
				"extra %v, copy is missing %v). One of the two is wrong. A copy that accepts "+
				"LESS lets a review-shaped step declare a write-capable role past Rule H; a copy "+
				"that accepts MORE red-flags steps this engine treats as ordinary work.",
				dir, lintRel, c.what, keysOf(c.got), keysOf(c.want), added, removed)
		}
	})
}
