package domain

// aihub#679 — the H7 visibility fold must not name the project it is refusing to
// show.
//
// 🔴 WHY THIS ARM EXISTS AT ALL, given aihub#665 already has a whole DB-gated
// file about this fold. That file
// (internal/server/predict_conflicts_visibility_db_test.go) pins that the wi id,
// the slug, the actor and the attempt id are withheld. It did NOT pin the
// project name — and the fold was publishing it, in the same string, on the same
// code path, the entire time. An absence census that enumerates the secrets it
// remembers cannot catch the one nobody listed, which is how this survived a
// change whose stated subject was this exact redaction.
//
// So this arm does not test the ANSWER, it tests the SHAPE: the fold's
// description must come from a constant, because a constant is the only form
// that cannot be re-widened by concatenating something the caller cannot see.
// The DB arm in package server measures the wire; this one makes the wire's
// property structural, and it runs with no database, on every `go test ./...`.
//
// The two are deliberately not the same assertion. A wire assertion goes green
// the moment the fixture stops producing a cross-project conflict; a shape
// assertion goes red the moment somebody writes `+ wiProject` back in, whatever
// the fixture is doing.

import (
	"strings"
	"testing"
)

// conflictsSourceFile is the file PredictConflicts and the fold live in.
const conflictsSourceFile = "conflicts.go"

// TestFoldedConflictDescriptionNamesNothingTheCallerCannotSee is the aihub#679
// gate.
func TestFoldedConflictDescriptionNamesNothingTheCallerCannotSee(t *testing.T) {
	// ── The constant itself ──────────────────────────────────────────────────

	t.Run("the_constant_carries_no_substitution_site", func(t *testing.T) {
		// A description assembled at runtime is the defect class. Neither a
		// fmt verb nor a concatenation marker may survive into the shipped
		// string, because either one means the label has a hole in it and the
		// only values in scope at the fold are the holder's.
		for _, forbidden := range []string{"%s", "%v", "%q", "%d", "+"} {
			if strings.Contains(FoldedConflictDescription, forbidden) {
				t.Errorf("FoldedConflictDescription = %q contains %q — a folded description "+
					"with a substitution site is one edit away from being filled with the "+
					"project name again, which is the aihub#679 defect.",
					FoldedConflictDescription, forbidden)
			}
		}
	})

	t.Run("the_constant_does_not_say_in_project_X", func(t *testing.T) {
		// 🔴 The one phrasing that must not come back. "in project " is what the
		// old label led with, and any replacement that keeps that lead-in is
		// almost certainly followed by a name.
		if strings.Contains(FoldedConflictDescription, "in project ") {
			t.Errorf("FoldedConflictDescription = %q still leads with \"in project \". "+
				"The label c5a4f1c shipped was \"[conflict in project \" + wiProject + "+
				"\", no visibility]\"; a rewrite that keeps the lead-in and drops the "+
				"variable today invites the variable back tomorrow.", FoldedConflictDescription)
		}
	})

	t.Run("the_constant_still_tells_the_caller_it_is_blocked", func(t *testing.T) {
		// 🔴 THE NEGATIVE CONTROL, and the reason it is not optional: every arm
		// above is satisfied by the empty string. Redacting a prediction into
		// nothing would pass this whole file while removing the signal pf-work
		// branches on — the caller must still learn a conflict exists.
		if !strings.Contains(FoldedConflictDescription, "conflict") {
			t.Errorf("FoldedConflictDescription = %q no longer says a conflict exists. "+
				"The fold withholds the COUNTERPARTY, not the refusal: a caller that is "+
				"hard-blocked and told nothing cannot act on it.", FoldedConflictDescription)
		}
		// The every_rule_is_folded arm in package server counts occurrences of
		// this tail to prove all three rules folded rather than one folding and
		// two returning empty. Changing the tail silently turns that count into a
		// measurement of nothing.
		const countedTail = ", no visibility]"
		if !strings.HasSuffix(FoldedConflictDescription, countedTail) {
			t.Errorf("FoldedConflictDescription = %q no longer ends with %q. "+
				"TestPredictConflictsVisibilityAcrossProjects/every_rule_is_folded… counts "+
				"that exact substring to tell three folded predictions from one folded and "+
				"two empty; change both or neither.", FoldedConflictDescription, countedTail)
		}
	})

	// ── The fold's assignment ────────────────────────────────────────────────

	t.Run("the_fold_assigns_the_constant_and_never_the_project", func(t *testing.T) {
		// Comments are stripped first: this file's own subject matter appears in
		// PredictConflicts' doc comment and in the measurement tables aihub#665
		// left behind, which are HISTORY and are deliberately still worded as
		// they were taken. Matching them would make this arm green for the wrong
		// reason — the tombstone-reads-as-a-hit failure.
		src := stripComments(t, sourceOf(t, conflictsSourceFile))
		body := bodyOf(t, src, "PredictConflicts")

		// Negative control on the matcher itself, before it is trusted either
		// way: the shapes below must be findable in source that really has them,
		// or "not found" means "my regexp is broken", not "the code is clean".
		ctrl := stripComments(t, "package p\nfunc f() {\n"+
			"\tp.Description = \"[conflict in project \" + wiProject + \", no visibility]\"\n"+
			"\tq.Description = FoldedConflictDescription\n}\n")
		ctrlBody := bodyOf(t, ctrl, "f")
		if !strings.Contains(ctrlBody, "wiProject") ||
			!strings.Contains(ctrlBody, ".Description = \"[conflict in ") ||
			!strings.Contains(ctrlBody, "Description = FoldedConflictDescription") {
			t.Fatal("the matchers do not find the shapes they exist to find in a control that " +
				"carries both — every assertion below is meaningless until this passes")
		}

		const wantAssign = "p.Description = FoldedConflictDescription"
		if !strings.Contains(body, wantAssign) {
			t.Errorf("PredictConflicts no longer assigns the folded description from the "+
				"constant (%s). The fold is the LAST thing standing between an "+
				"unauthorized caller and the holder's project name, and building that "+
				"string from anything in scope at that point means building it from the "+
				"holder's own row.", wantAssign)
		}

		// 🔴 THE ARM THAT ACTUALLY CATCHES THE DEFECT. `wiProject` is the local
		// holding the invisible project's name. It is legitimately READ — it is
		// what canSeeProject is asked about, and what the `wiProject != ""` guard
		// tests — so the assertion cannot be "the identifier is absent". It is
		// that the identifier never reaches a Description.
		//
		// ⚠️ THE LEADING DOT IS LOAD-BEARING and was added after this arm fired on
		// itself. bodyOf cuts at the next line starting "func ", and the
		// FoldedConflictDescription const sits between PredictConflicts and
		// canSeeProject — so the const's own declaration is inside the slice being
		// searched, and a bare `Description = "` matched
		// `FoldedConflictDescription = "[conflict in another project…"`. The fix
		// is the fix and not a widened allowance: every assignment this arm exists
		// to catch is to a FIELD (`p.Description`), so requiring the selector
		// excludes the declaration without excluding a single real defect.
		for _, banned := range []string{
			".Description = \"",              // any literal-built description in the fold
			".Description = \"[conflict in ", // the exact old shape
			"wiProject +",                    // concatenating the project onto anything
			"+ wiProject",
		} {
			if strings.Contains(body, banned) {
				t.Errorf("PredictConflicts contains %q. The H7 fold redacts actor_display, "+
					"work_item_id, work_item_slug and attempt_id and then has exactly one "+
					"remaining field it could leak through; aihub#679 removed the project "+
					"name from it, and this is the shape that puts something back.", banned)
			}
		}
	})
}
