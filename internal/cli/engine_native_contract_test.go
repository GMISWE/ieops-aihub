package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
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
// (internal/domain/run_attempts.go, step 5) hard-rejects every subsequent credential-checked
// pf_* call. That is fail-safe, but the auto-mode loop had no branch for it: it ran on into a
// cascade of surprise credential errors and could retry, burning tokens on calls that cannot
// succeed. (aihub#182.)
//
// WHY THESE ASSERTIONS
//   Tag A — no worktree step file is prescribed anywhere in the injected or deferred engine text.
//   Tag B — the resuming instruction names the authority AND comes before the step body, because
//           an instruction placed after the work is not a resume instruction.
//   Tag C — the auto loop has an explicit paused-attempt exit that terminates without retrying
//           and without completing the attempt.
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
				"attempt every credential-checked pf_* call is hard-rejected "+
				"(internal/domain/run_attempts.go, verifyAttemptCredential step 5), so a loop "+
				"without this branch ends in a cascade of surprise errors and may retry.",
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
