package cli

import (
	"regexp"
	"strings"
	"testing"
)

// Contract gate for the INTERACTIVE (requires_human_session=true) engine loop — aihub#644.
//
// WHAT CHANGED
// ------------
// Until this work item the rhs=true loop dispatched nothing at all. It printed the step body
// and the human carried it out:
//
//	output: f"## Step {step_id}\n\n{expanded}"
//	wait for user input: "continue" / "skip" / "fail"
//
// Zero-layer dispatch. The owner ruled (2026-09-13, decision 2, option ①) that it becomes ONE
// layer: the main session dispatches a step agent per step and the human confirms at the STEP
// BOUNDARY. The rhs=false loop is explicitly out of scope — it already auto-dispatches.
//
// WHY A GO GATE AND NOT PROSE
// ---------------------------
// Both loops are markdown executed by an LLM, so the only thing that can hold their shape is a
// test that reads the markdown. And §1 had NO gate before this file: grepping the three engine
// contract suites on e34d066 for `rhs=true`, `interactive` or `wait for user input` returns
// nothing, so every property below was previously free to drift.
//
// WHAT IS ASSERTED, AND WHY EACH ONE IS THE PROPERTY THAT CAN ACTUALLY BREAK
//
//	A1 §1 DISPATCHES. The loop selects its agent through ROLE_AGENT[role] with role from
//	   `polyforge engine resolve-role`, exactly as the auto loop does, and neither document
//	   still tells the reader to present each step instead of dispatching. The sharp half is
//	   the negative one: an `output:` line carrying {expanded} means the STEP BODY is being
//	   printed for a person to execute, which is the zero-layer loop this ruling replaced.
//
//	A2 THE GATE IS ORDERED. "Confirm at the step boundary" is a claim about ORDER, not about
//	   vocabulary, so it is checked as one: dispatch < wait-for-human < bracket, and between
//	   the dispatch and the wait there is no pf_update_step and no bracket-plan. A loop that
//	   brackets the step, or dispatches the next one, before the human answers is zero-layer
//	   dispatch with extra steps — the answer can no longer change anything.
//
//	A3 rhs=false IS UNTOUCHED. This work item is about the true branch only. The auto block
//	   must keep dispatching and bracketing and must acquire none of the gate's landmarks.
//
//	A4 ONE DISPATCH TEMPLATE, STILL MODEL-FREE. §1 points at §0b rather than inlining a second
//	   copy (asserted by counting the template's own marker across the whole document), and its
//	   dispatch passes no model argument — an explicit per-invocation model silently overrides
//	   the agent file (aihub#555, measured), and a NEW dispatch site is a new place for that
//	   dead selector to come back.
//
//	A5 retry DOES NOT RESPEND THE STEP ATTEMPT ID. `retry` is the verb the ruling's gate needs
//	   and the one that touches no server state; `step_attempt_id` keys the step-history row
//	   (aihub#399), so a loop that re-mints on retry files two rows for one step.
//
//	A6 THE HUMAN ADJUDICATES. Option ① makes the human the judge and the agent the executor, so
//	   §1c must carry the four verbs, the override-is-recorded rule, and the ban on applying a
//	   reviewer verdict automatically — which is the auto loop, i.e. the thing ① did not pick.
//
// Every test begins by proving its own anchors resolve. A "must not contain" assertion against
// a section that was renamed away passes for free, which is the failure mode this suite exists
// to prevent rather than to join.

const (
	ixResidentDoc = "skills/pf-execute/engine.native.md"
	ixDetailDoc   = "skills/pf-execute/references/engine-native-details.md"

	ixAutoHeading        = "## Execute (rhs=false, auto mode)"
	ixResidentIntHeading = "## Execute (rhs=true, interactive mode)"
	ixDetailIntHeading   = "## 1. Execute (rhs=true, interactive mode)"
	ixAdjudicateHeading  = "## 1c."

	// The zero-layer loop's own sentence, as the resident fragment shipped it before aihub#644.
	// Banned by name: it is the single line a reader would follow straight back into the old
	// behaviour, and leaving it beside the new loop makes the document say both.
	ixZeroLayerSentence = "present each step instead of dispatching"

	// Landmarks inside §1's pseudocode.
	ixDispatchLine = "dispatch Agent(subagent_type=ROLE_AGENT[role]"
	ixRoleVerb     = "polyforge engine resolve-role"
	ixWaitLine     = "wait for user input"
	ixBracketCall  = "run bracket-plan("
	ixRetryVerb    = `"retry <feedback>"`
	ixSkipVerb     = `"skip"`

	// The sentinel that carries the two non-local exits (pause, fail) past the inner `while`.
	ixHaltSet  = "halt = True"
	ixHaltExit = "if halt:"
)

// ixNegatedDispatchRe matches any way of saying "do not dispatch". A gate that banned only the
// one sentence the old document shipped is a spell-check: rewording it defeats the ban while
// restoring the behaviour, which is the X3 mutant this expression exists to answer.
var ixNegatedDispatchRe = regexp.MustCompile(`(?i)(instead of|rather than|in place of|without|not) dispatch`)

// ixSection returns the text from heading up to the next top-level "\n## " heading. It fails
// rather than returning "" — an empty section satisfies every ban below without checking one.
func ixSection(t *testing.T, doc, rel, heading string) string {
	t.Helper()
	i := strings.Index(doc, heading)
	if i < 0 {
		t.Fatalf("%s has no %q section. Either it was restructured or this gate is anchored on "+
			"text that no longer exists; until that is resolved nothing below is checking the "+
			"loop it names.", rel, heading)
	}
	section := doc[i:]
	if end := strings.Index(section[len(heading):], "\n## "); end >= 0 {
		section = section[:len(heading)+end]
	}
	return section
}

// ixFencedLoop returns the first fenced block under heading — the loop itself. minLen guards the
// one way this whole file could go green while reading nothing: a truncated or emptied block.
func ixFencedLoop(t *testing.T, doc, rel, heading string, minLen int) string {
	t.Helper()
	block, ok := bcFirstFencedBlock(ixSection(t, doc, rel, heading))
	if !ok {
		t.Fatalf("%s: %q carries no fenced block. The loop IS that block; without it every "+
			"assertion about the loop's shape is being made against nothing.", rel, heading)
	}
	if len(block) < minLen {
		t.Fatalf("%s: %q's fenced block is %d bytes, under the %d-byte floor. A loop that short "+
			"is a truncation, and the ordering and absence checks below would both pass on it "+
			"vacuously.", rel, heading, len(block), minLen)
	}
	return block
}

// ixStripComments blanks whole-line comments, keeping the line count and therefore the reader's
// ability to locate a failure. It exists because §1's gate comment NAMES the calls it forbids
// ("no bracket-plan, no pf_update_step, and no dispatch of steps[i+1]"), so a scan that did not
// separate pseudocode from prose would find those calls in the very text that bans them.
func ixStripComments(s string) string {
	lines := strings.Split(s, "\n")
	for i, ln := range lines {
		if strings.HasPrefix(strings.TrimSpace(ln), "#") {
			lines[i] = ""
		}
	}
	return strings.Join(lines, "\n")
}

// ── A1 ───────────────────────────────────────────────────────────────────────────────────────

func TestEngineInteractiveLoopDispatchesAStepAgent(t *testing.T) {
	pluginRoot := pluginRootDir(t)
	resident := readEngineDoc(t, pluginRoot, ixResidentDoc)
	details := readEngineDoc(t, pluginRoot, ixDetailDoc)

	// Negative control, first and on its own: the bans below are free on an empty file.
	for rel, body := range map[string]string{ixResidentDoc: resident, ixDetailDoc: details} {
		if strings.TrimSpace(body) == "" {
			t.Fatalf("%s is empty — every assertion in this file would pass vacuously", rel)
		}
	}
	loop := ixFencedLoop(t, details, ixDetailDoc, ixDetailIntHeading, 800)

	if !strings.Contains(loop, ixDispatchLine) {
		t.Errorf("%s: §1's loop does not contain %q. The owner's 2026-09-13 decision 2 (option "+
			"①) is one-layer dispatch: the main session dispatches a step agent per step. A "+
			"loop with no dispatch line is still the zero-layer loop.\n%s",
			ixDetailDoc, ixDispatchLine, loop)
	}
	if !strings.Contains(loop, ixRoleVerb) {
		t.Errorf("%s: §1's loop does not resolve its role via %q. The role choice has exactly "+
			"one owner (internal/engine.ResolveRole, aihub#642/#664); a second dispatch site "+
			"that re-derives it can widen a read-only role back to a write-capable agent, "+
			"which is the aihub#664 defect.", ixDetailDoc, ixRoleVerb)
	}

	// The zero-layer sentence must be gone from BOTH tiers. The resident fragment is the copy a
	// session reads without being asked to, so a stale line there outranks the corrected one here.
	for rel, body := range map[string]string{ixResidentDoc: resident, ixDetailDoc: details} {
		if strings.Contains(body, ixZeroLayerSentence) {
			t.Errorf("%s still says %q. That is the zero-layer instruction aihub#644 replaced; "+
				"a document that carries both tells the reader to do either.", rel, ixZeroLayerSentence)
		}
	}

	// ── The resident tier, gated POSITIVELY ──────────────────────────────────────────────────
	//
	// Banning one legacy sentence is not a gate, it is a spell-check: rewording it to "you
	// present each step RATHER THAN dispatching" restores zero-layer dispatch in the tier a
	// session reads WITHOUT being asked to, and walks straight past the ban. (Measured: that
	// exact two-word edit passed the whole internal/cli package before this block existed.) So
	// the resident paragraph is required to say what it DOES, and any negation-of-dispatch
	// phrasing is refused by shape rather than by literal.
	res := ixSection(t, resident, ixResidentDoc, ixResidentIntHeading)
	for _, want := range []string{"dispatch", "wait for the human", "§1", "§1c"} {
		if !strings.Contains(res, want) {
			t.Errorf("%s: the rhs=true paragraph does not mention %q:\n%s\nThis is the ONLY copy "+
				"an interactive session is guaranteed to have read when it starts the first "+
				"step; §1 is on-demand and reaches it only because this paragraph sends it "+
				"there.", ixResidentDoc, want, res)
		}
	}
	if m := ixNegatedDispatchRe.FindString(res); m != "" {
		t.Errorf("%s: the rhs=true paragraph says %q — it negates the dispatch. aihub#644 makes "+
			"the interactive loop dispatch a step agent per step; a resident paragraph that "+
			"says otherwise is the zero-layer instruction back again under new words, and it "+
			"outranks §1 because it arrives first and unasked.", ixResidentDoc, m)
	}

	// The sharp half. Handing {expanded} — the step BODY — to the user is what zero-layer
	// dispatch IS; under one layer the body goes to the agent through §0b's prompt instead.
	for _, ln := range strings.Split(loop, "\n") {
		trimmed := strings.TrimSpace(ln)
		if !strings.HasPrefix(trimmed, "output:") {
			continue
		}
		if strings.Contains(trimmed, "{expanded}") {
			t.Errorf("%s: §1 still prints the step body to the user:\n\t%s\nThat is zero-layer "+
				"dispatch — the human executes the step. Under one-layer dispatch {expanded} "+
				"reaches the step AGENT through §0b's prompt, and what the human is shown is "+
				"what the agent returned.", ixDetailDoc, trimmed)
		}
	}
}

// ── A2 ───────────────────────────────────────────────────────────────────────────────────────

func TestEngineInteractiveHumanGateIsOrdered(t *testing.T) {
	pluginRoot := pluginRootDir(t)
	details := readEngineDoc(t, pluginRoot, ixDetailDoc)
	loop := ixFencedLoop(t, details, ixDetailDoc, ixDetailIntHeading, 800)

	// One wait, so "the wait comes after the dispatch" names a specific point. With two, the
	// ordering check could be satisfied by whichever one happened to sit in the right place.
	if n := strings.Count(loop, ixWaitLine); n != 1 {
		t.Fatalf("%s: §1's loop contains %d %q points, expected exactly 1. The step boundary is "+
			"ONE place; with %d of them the ordering assertion below would be satisfied by "+
			"whichever copy fell in the right order.", ixDetailDoc, n, ixWaitLine, n)
	}

	dispatchAt := strings.Index(loop, ixDispatchLine)
	waitAt := strings.Index(loop, ixWaitLine)
	bracketAt := strings.Index(loop, ixBracketCall)
	for name, at := range map[string]int{
		ixDispatchLine: dispatchAt, ixWaitLine: waitAt, ixBracketCall: bracketAt,
	} {
		if at < 0 {
			t.Fatalf("%s: §1's loop has no %q, so its ORDER cannot be checked at all",
				ixDetailDoc, name)
		}
	}

	if !(dispatchAt < waitAt) {
		t.Errorf("%s: §1 waits for the human at offset %d BEFORE dispatching at %d. Then the "+
			"human is being asked about work that has not happened, which is the zero-layer "+
			"loop's question, not this one's.", ixDetailDoc, waitAt, dispatchAt)
	}
	if !(waitAt < bracketAt) {
		t.Errorf("%s: §1 brackets the step at offset %d BEFORE waiting for the human at %d. The "+
			"confirmation is then decorative: the step is already filed by the time it is "+
			"asked for.", ixDetailDoc, bracketAt, waitAt)
	}

	// Nothing may commit the step, or start the next one, while the human has not answered.
	// Comments are blanked first: the gate comment names these very calls in order to ban them.
	if dispatchAt >= 0 && waitAt > dispatchAt {
		between := ixStripComments(loop[dispatchAt:waitAt])
		for _, banned := range []string{"pf_update_step(", "bracket-plan"} {
			if strings.Contains(between, banned) {
				t.Errorf("%s: §1 runs %q between the dispatch and the human's answer:\n%s\n"+
					"The step boundary is a gate only if the human's answer can still change "+
					"the outcome. Anything filed before they speak has already decided it.",
					ixDetailDoc, banned, between)
			}
		}
		// The third clause of the gate's own prose, which the two bans above do not cover:
		// running AHEAD. One dispatch opens this window; a second inside it is the next step
		// already started, and the human's answer then arrives too late to prevent it.
		if n := strings.Count(between, "dispatch Agent("); n != 1 {
			t.Errorf("%s: %d `dispatch Agent(` calls between the dispatch and the human's "+
				"answer, expected exactly 1 (the step's own):\n%s\nA second one is steps[i+1] "+
				"started while the gate is still open — the loop has run ahead of the person "+
				"it is supposed to be waiting for.", ixDetailDoc, n, between)
		}
	}
}

// ── The two non-local exits: pause (§0e) and fail (§0c) must leave BOTH loops ─────────────────
//
// One-layer dispatch nests a `while` inside the `for` so `retry` can re-run a step. That nesting
// re-homed two exits that used to be correct: a bare `break` in the pause branch or the fail
// branch now leaves only the `while` and falls into the COMPLETED bracket below it. Measured
// consequences on the first draft of this change: the paused path filed the step completed and
// started the next one — the two things §0e states in full that it must not do — and the failed
// path filed `completed` for a step it had just filed `failed`, same step_id, same sa_id.
//
// TestEngineNativeContract's Tag C pins this branch for the AUTO loop only (its `rel` is the
// resident doc), so nothing covered §1's copy. This is that gate.
func TestEngineInteractiveNonLocalExitsLeaveBothLoops(t *testing.T) {
	pluginRoot := pluginRootDir(t)
	details := readEngineDoc(t, pluginRoot, ixDetailDoc)
	loop := ixFencedLoop(t, details, ixDetailDoc, ixDetailIntHeading, 800)

	// Anti-vacuity: there must BE an inner loop, or this whole test is about nothing.
	if !strings.Contains(loop, "while True:") {
		t.Fatalf("%s: §1 has no inner `while True:`. If the retry re-entry was removed, this "+
			"gate is checking a hazard that no longer exists and should be deleted rather than "+
			"left passing.", ixDetailDoc)
	}

	// Exactly two exits need to escape the while: the paused attempt and the human's `fail`.
	if n := strings.Count(loop, ixHaltSet); n != 2 {
		t.Errorf("%s: §1 sets %q %d times, expected 2 — the paused-attempt exit (§0e) and the "+
			"human's `fail` (§0c). A branch that leaves the `while` without it drops into the "+
			"completed bracket and files a step that is already terminal, or one §0e requires "+
			"be left open.", ixDetailDoc, ixHaltSet, n)
	}
	haltAt := strings.Index(loop, ixHaltExit)
	if haltAt < 0 {
		t.Fatalf("%s: §1 never tests %q. Both non-local exits then stop at the inner `while` "+
			"and fall through to the completed bracket — silently, because `break` breaking "+
			"one loop is correct Python and reads as correct prose.", ixDetailDoc, ixHaltExit)
	}
	if bracketAt := strings.Index(loop, ixBracketCall); bracketAt >= 0 && haltAt > bracketAt {
		t.Errorf("%s: §1 tests %q at offset %d, AFTER the completed bracket at %d. The bracket "+
			"has already run by then, which is the exact failure the sentinel exists to stop.",
			ixDetailDoc, ixHaltExit, haltAt, bracketAt)
	}
}

// ── A3: the rhs=false branch this work item must NOT have touched ────────────────────────────

func TestEngineAutoLoopKeepsNoHumanGate(t *testing.T) {
	pluginRoot := pluginRootDir(t)
	resident := readEngineDoc(t, pluginRoot, ixResidentDoc)
	auto := ixFencedLoop(t, resident, ixResidentDoc, ixAutoHeading, 400)

	// Anti-vacuity: prove this really is the auto loop before asserting what it lacks.
	for _, want := range []string{ixDispatchLine, "bracket-plan"} {
		if !strings.Contains(auto, want) {
			t.Fatalf("%s: the rhs=false block does not contain %q, so it is either not the auto "+
				"loop or no longer a loop. The absence checks below would pass on any text.",
				ixResidentDoc, want)
		}
	}

	// aihub#644 is the rhs=true branch ONLY. rhs=false already auto-dispatches; acquiring any of
	// these landmarks would mean the auto path had grown a human in it.
	for _, banned := range []string{ixWaitLine, ixRetryVerb, "adjudicat"} {
		if strings.Contains(auto, banned) {
			t.Errorf("%s: the rhs=false auto loop now contains %q. aihub#644 changed the "+
				"INTERACTIVE branch; an unattended run that stops for a human never finishes, "+
				"and nothing else in the system expects it to.", ixResidentDoc, banned)
		}
	}
}

// ── A4 ───────────────────────────────────────────────────────────────────────────────────────

func TestEngineInteractiveDispatchReusesTheOneTemplate(t *testing.T) {
	pluginRoot := pluginRootDir(t)
	details := readEngineDoc(t, pluginRoot, ixDetailDoc)
	loop := ixFencedLoop(t, details, ixDetailDoc, ixDetailIntHeading, 800)

	if !strings.Contains(loop, "§0b") {
		t.Errorf("%s: §1's loop no longer points at §0b for the dispatch template. Two copies of "+
			"a prompt template drift, and the copy this loop uses is the one nothing gates "+
			"(TestEngineNativeDispatchSelectsAgentNotModel reads §0b's).", ixDetailDoc)
	}
	// One template in the whole document, proved by its own landmark rather than by reading it.
	if n := strings.Count(details, stepInstructionsMarker); n != 1 {
		t.Errorf("%s: %q appears %d times, expected 1. A second dispatch template inlined for "+
			"the interactive loop is a second thing to keep in step with the agent files; §1 "+
			"must reference §0b's, not restate it.", ixDetailDoc, stepInstructionsMarker, n)
	}
	if m := regexp.MustCompile(`dispatch Agent\([^)]*\bmodel\s*=`).FindString(loop); m != "" {
		t.Errorf("%s: §1's dispatch passes a model argument (%q). An explicit per-invocation "+
			"model silently OVERRIDES the agent file (aihub#555, measured), so the tier the "+
			"agent file carries is dead on every dispatch that fills it — and a new dispatch "+
			"site is a new place for that to come back.", ixDetailDoc, m)
	}
}

// ── A5 ───────────────────────────────────────────────────────────────────────────────────────

func TestEngineInteractiveRetryDoesNotRespendTheStepAttemptID(t *testing.T) {
	pluginRoot := pluginRootDir(t)
	details := readEngineDoc(t, pluginRoot, ixDetailDoc)
	loop := ixFencedLoop(t, details, ixDetailDoc, ixDetailIntHeading, 800)

	start := strings.Index(loop, ixRetryVerb)
	if start < 0 {
		t.Fatalf("%s: §1 offers no %s verb. Once the agent executes the step, a human who is "+
			"not satisfied has only 'accept it' or 'fail', and `fail` takes the whole attempt "+
			"down through §0c — so the step boundary stops being a place where anything can be "+
			"corrected.", ixDetailDoc, ixRetryVerb)
	}
	branch := loop[start:]
	if end := strings.Index(branch, ixSkipVerb); end > 0 {
		branch = branch[:end]
	}

	// POLARITY, not vocabulary. "makes NO server call, but mint a fresh step_attempt_id first:
	// sa_id = new_ulid()" contains every word a presence check would look for — sa_id, server
	// call, mint — while instructing the exact aihub#399 defect this assertion exists to
	// prevent. (Measured: that inversion passed the whole internal/cli package.) So the rule is
	// required as a literal, and the minting call is banned outright.
	for _, want := range []string{"sa_id is unchanged", "do NOT mint"} {
		if !strings.Contains(branch, want) {
			t.Errorf("%s: §1's retry branch does not say %q:\n%s\nretry is the one verb that "+
				"touches no server state, so the step stays open under the SAME "+
				"step_attempt_id. That id keys the step-history row (aihub#399): a loop that "+
				"mints a fresh one on retry files two rows for one step, and nothing rejects "+
				"the second. Naming the id and the word 'mint' is not enough — a branch that "+
				"tells the loop TO mint names both.", ixDetailDoc, want, branch)
		}
	}
	if strings.Contains(branch, "new_ulid") {
		t.Errorf("%s: §1's retry branch calls new_ulid:\n%s\nMinting on retry is precisely the "+
			"aihub#399 defect: the step is still open under the id it was started with, and a "+
			"second id for it files a second step-history row.", ixDetailDoc, branch)
	}

	// The feedback has to reach the dispatch, in the DISPATCH LINE. A `feedback` that is only
	// assigned and then described in a comment is a dead variable, and this is pseudocode an
	// LLM executes literally: the line it copies is the line that runs.
	for _, ln := range strings.Split(loop, "\n") {
		if !strings.Contains(ln, "dispatch Agent(") {
			continue
		}
		if !strings.Contains(ln, "feedback") {
			t.Errorf("%s: §1's dispatch line does not thread the feedback:\n\t%s\n`retry "+
				"<feedback>` collects it and nothing passes it on, so the re-dispatch is "+
				"byte-identical to the one the human just rejected — and will produce the same "+
				"answer.", ixDetailDoc, strings.TrimSpace(ln))
		}
	}

	// A retried agent's only authority is pf_get_step, and the step is still in_progress — so
	// completed_steps has no entry for it and the record reads CLEAN while the tree is dirty.
	// Nothing else in the system can tell it that a previous attempt already edited files.
	if !strings.Contains(loop, "WORKTREE") && !strings.Contains(loop, "worktree") {
		t.Errorf("%s: §1's retry dispatch never tells the agent that a previous attempt's edits "+
			"are already in the worktree. pf_get_step cannot show them (no completed_steps entry "+
			"while the step is in_progress), so an agent not told reads the gap as 'nothing has "+
			"happened yet' and starts over on top of its own earlier edits.", ixDetailDoc)
	}
}

// ── A6 ───────────────────────────────────────────────────────────────────────────────────────

func TestEngineInteractiveHumanAdjudicatesTheReviewVerdict(t *testing.T) {
	pluginRoot := pluginRootDir(t)
	details := readEngineDoc(t, pluginRoot, ixDetailDoc)
	sec := ixSection(t, details, ixDetailDoc, ixAdjudicateHeading)
	if len(sec) < 600 {
		t.Fatalf("%s: §1c is %d bytes — too short to carry the verb table and the override "+
			"rules, so the checks below would be reading a stub", ixDetailDoc, len(sec))
	}

	// The four verbs the gate needs. Three of them predate aihub#644; `retry` is what the ruling
	// requires once the human stops being the executor.
	for _, verb := range []string{"`continue`", "`retry <feedback>`", "`skip`", "`fail`"} {
		if !strings.Contains(sec, verb) {
			t.Errorf("%s: §1c does not document the %s verb. The step boundary is only as good "+
				"as the answers available at it.", ixDetailDoc, verb)
		}
	}

	// Option ① makes the human the adjudicator. Auto-applying the verdict is the auto loop —
	// the branch the owner did not choose — and it is the one change that makes the gate moot.
	//
	// Required as a FULL literal, negation included. `Contains(sec, "automatically")` is
	// satisfied by "**Apply the verdict automatically.** … FAIL goes straight to §0c without
	// asking the human" — the precise inversion of the rule, passing the check that was meant to
	// pin it. (Measured: it passed the whole internal/cli package.) Same for the override rule,
	// where "overridden" alone survives any sentence that merely uses the word.
	for _, want := range []string{
		"Never apply the verdict automatically",
		"overridden by human",
		"--artifact-summary=",
	} {
		if !strings.Contains(sec, want) {
			t.Errorf("%s: §1c does not carry the literal %q. Option ① makes the human the "+
				"adjudicator: the verdict is a recommendation and applying it unasked IS the "+
				"rhs=false loop, with the gate this work item added deciding nothing — and an "+
				"override nobody wrote down is indistinguishable in the record from a review "+
				"that passed. A paraphrase is not enough here: the inverted sentence contains "+
				"the same words.", ixDetailDoc, want)
		}
	}
}
