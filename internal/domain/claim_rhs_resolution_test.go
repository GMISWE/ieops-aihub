package domain

// aihub#543 probe wave 2 — the `docs/mcp-cards/pf_create_work_item.md` sentences
// that describe what a CLAIM does to a work item created with no
// requires_human_session.
//
//	"`NULL` is also not permanent — `domain.FnClaimWorkItem` resolves it to
//	 `true` from a server default on the FIRST claim, writes it back and emits
//	 `wi_classification_resolved`, so a work item that has ever been claimed
//	 cannot still be unclassified."
//	    -> TestClaimResolvesAnUnclassifiedWorkItemAndSaysSo
//	"It is a constant (`domain.defaultRequiresHumanSession`), the server has no
//	 second source to check a classification against, and
//	 `domain.ErrRequiresHumanSessionMismatch` documents at length why the 409
//	 that was supposed to police it never fired."
//	    -> TestClaimResolvesAnUnclassifiedWorkItemAndSaysSo
//	    (with TestDefaultRequiresHumanSessionIsTrue and
//	     TestUnreachableMismatchBranchIsGone, which already hold the other halves)
//
// WHY THE THREE-PART SENTENCE HAD NO ARM. Its pieces were covered unevenly.
// TestDefaultRequiresHumanSessionIsTrue pins the constant's VALUE.
// TestClassificationEventDoesNotNameWIType proves the event is emitted somewhere
// in run_attempts.go and that its payload does not name the wi_type.
// TestUnreachableMismatchBranchIsGone proves the dead 409 is gone. Nothing said
// the resolution is GATED on the column being NULL, and nothing said the value
// is written BACK — which are the two halves that make "a work item that has
// ever been claimed cannot still be unclassified" true. An emission with no
// UPDATE beside it produces a timeline that says the classification was resolved
// and a row that is still NULL; a resolution not gated on NULL overwrites a
// classification the client set from the scenario repo, which is the one copy of
// the per-wi_type defaults and the reason there is nothing here to cross-check
// against.
//
// WHY IT IS A SOURCE ARM AND NOT A DB ONE. The write happens inside
// FnClaimWorkItem's transaction, so observing the row needs AIHUB_TEST_DB — and
// under the aihub#543 spec's selection rule a new AIHUB_TEST_DB-gated top-level
// function costs a gated_tests.txt line and a ci.yml step, which is the single
// largest per-probe cost multiplier in this repo. This arm buys the two missing
// halves for neither, at the price of being about the SHAPE of the claim path
// rather than about a row: it would not notice a `tx.Exec` that ran and silently
// affected nothing. Stated rather than left implicit, because that is the gap a
// later DB arm would close.
//
//	GOWORK=off go test ./internal/domain/ -run TestClaimResolvesAnUnclassifiedWorkItemAndSaysSo -count=1 -v

import (
	"go/ast"
	"go/parser"
	"go/printer"
	"go/token"
	"strings"
	"testing"
)

// classificationUpdateFragment is the SQL the resolution must run.
//
// Matched on the two identifiers that make it the right statement — the table
// and the column — rather than on the whole statement text, because the
// formatting of an inline SQL literal is not contract and gofmt does not own it.
var classificationUpdateFragment = []string{"UPDATE work_items", "requires_human_session"}

// analyseClassificationResolution reports every reason src does not show the
// claim path resolving an unclassified work item.
//
// It requires, of the block that emits wi_classification_resolved:
//
//	(1) that block is the body of an `if` whose condition tests the work item's
//	    own requires_human_session against nil — so a row that already carries a
//	    classification is left alone,
//	(2) the same block runs an UPDATE against work_items' requires_human_session
//	    column, and
//	(3) the same block writes the resolved value back onto the in-memory work
//	    item, so the response a caller reads agrees with the row.
//
// A source with no emission at all is a violation, never a silent pass — the
// same rule analyseEmission states for itself two files over, and for the same
// reason: this walk cannot be evidence about a claim path it did not find.
func analyseClassificationResolution(src string) []string {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, claimSourceFile, src, 0)
	if err != nil {
		return []string{"source does not parse: " + err.Error() +
			" — a file that cannot be parsed cannot be cleared of anything"}
	}

	// The emission site: the call carrying the event type in its SQL-QUOTED form.
	//
	// 🔴 The quotes are load-bearing and were measured to be. analyseEmission next
	// door locates the same call on a bare `strings.Contains(lit.Value,
	// classificationEventType)`, and the emitting statement passes the event name
	// TWICE — once inside the INSERT and once inside bestEffortExec's error
	// message ("failed to emit wi_classification_resolved event"). So a mutant that
	// renames only the event the row is filed under is still located by the error
	// text, and a locator keyed on the bare substring reports the emission as
	// present on a tree that no longer emits it. Requiring the SQL form makes the
	// mutant red here; the same blind spot in analyseEmission is reported as an
	// incidental finding of aihub#576 rather than edited from this change.
	quoted := "'" + classificationEventType + "'"
	var emit ast.Node
	ast.Inspect(f, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		for _, a := range call.Args {
			if lit, isLit := a.(*ast.BasicLit); isLit && lit.Kind == token.STRING &&
				strings.Contains(lit.Value, quoted) {
				emit = call
			}
		}
		return true
	})
	if emit == nil {
		return []string{"no call filing a row under " + quoted +
			" — the claim path does not record the resolution any more, so \"a work item that " +
			"has ever been claimed cannot still be unclassified\" is unreadable from the timeline " +
			"even if the row is right"}
	}

	block := enclosingBlock(f, emit)
	if block == nil {
		return []string{"the " + classificationEventType + " emission is not inside a block, " +
			"which is not a shape this analyser models — and an unmodelled shape is a finding, " +
			"not a pass"}
	}

	var out []string

	// (1) the NULL guard. Found by walking every `if` in the file and keeping the
	// innermost one whose body IS this block, so a guard that moved outwards —
	// wrapping the emission in something looser — is not credited.
	var guard *ast.IfStmt
	ast.Inspect(f, func(n ast.Node) bool {
		ifStmt, ok := n.(*ast.IfStmt)
		if ok && ifStmt.Body == block {
			guard = ifStmt
		}
		return true
	})
	if guard == nil {
		out = append(out, "the block that emits "+classificationEventType+" is not the body of an "+
			"`if` at all, so the resolution is not gated on the work item being unclassified. A "+
			"claim would then overwrite a classification the client set from the scenario repo — "+
			"the only copy of the per-wi_type defaults, and the reason this path has nothing to "+
			"cross-check a value against")
	} else if cond := printNode(fset, guard.Cond); !strings.Contains(cond, "RequiresHumanSession") ||
		!strings.Contains(cond, "nil") {
		out = append(out, "the resolution is gated on `"+cond+"`, which does not test the work "+
			"item's own RequiresHumanSession against nil. Only the NULL branch may resolve: a "+
			"row that carries a value was classified by somebody, and a claim rewriting it is "+
			"the write aihub#411 T2-9 spent a section failing to attribute")
	}

	// (2) the write-back to the column, and (3) the write-back to the struct.
	body := printNode(fset, block)
	for _, want := range classificationUpdateFragment {
		if !strings.Contains(body, want) {
			out = append(out, "the resolving block does not contain "+want+" — it emits an event "+
				"saying the classification was resolved without storing the resolution, which "+
				"leaves the row NULL and every later reader in the ready queue's unclassified[] "+
				"segment while the timeline says otherwise")
		}
	}
	if !strings.Contains(body, "wi.RequiresHumanSession =") {
		out = append(out, "the resolving block never assigns the resolved value back onto the work "+
			"item, so the response this claim returns still reports the pre-claim NULL. A caller "+
			"reading its own claim's reply would conclude the work item is unclassified, which is "+
			"the state the card says a claimed work item cannot be in")
	}
	return out
}

// printNode prints an AST node back to source.
//
// go/printer rather than reassembling identifiers from a walk, for the reason
// funcBodyAST records in rhs_classification_honesty_test.go: Inspect visits a
// node before its children, so a reconstructed binary expression emits its
// operator ahead of its own left operand and any substring built on the result
// is about a string the file does not contain.
//
// Named printNode rather than renderNode because projects_members_cas_test.go
// already declares a renderNode in this package with a different signature; two
// spellings of one idea is a smaller cost than a shared helper whose callers
// disagree about whether it takes a *testing.T.
func printNode(fset *token.FileSet, n ast.Node) string {
	var b strings.Builder
	if err := printer.Fprint(&b, fset, n); err != nil {
		return ""
	}
	return b.String()
}

// The calibration fixtures. Each is the smallest claim-path shape carrying one
// known answer.
const (
	classificationFixtureClean = `package p
func claim(wi *W, tx T) error {
	if wi.RequiresHumanSession == nil {
		resolvedRHS := defaultRequiresHumanSession
		_, err := tx.Exec(ctx, ` + "`UPDATE work_items SET requires_human_session=$1 WHERE id=$2`" + `, resolvedRHS, wi.ID)
		if err != nil {
			return err
		}
		evtPayload := classificationResolvedEventPayload(resolvedRHS)
		_, _ = tx.Exec(ctx, ` + "`INSERT INTO agent_events (event_type) VALUES ('wi_classification_resolved')`" + `, evtPayload)
		wi.RequiresHumanSession = &resolvedRHS
	}
	return nil
}
`
	// The event fires, nothing is stored: a timeline that says resolved over a
	// row that is still NULL.
	classificationFixtureNoUpdate = `package p
func claim(wi *W, tx T) error {
	if wi.RequiresHumanSession == nil {
		resolvedRHS := defaultRequiresHumanSession
		evtPayload := classificationResolvedEventPayload(resolvedRHS)
		_, _ = tx.Exec(ctx, ` + "`INSERT INTO agent_events (event_type) VALUES ('wi_classification_resolved')`" + `, evtPayload)
		wi.RequiresHumanSession = &resolvedRHS
	}
	return nil
}
`
	// Ungated: every claim rewrites the classification, including one the client
	// set deliberately.
	classificationFixtureUngated = `package p
func claim(wi *W, tx T) error {
	resolvedRHS := defaultRequiresHumanSession
	_, err := tx.Exec(ctx, ` + "`UPDATE work_items SET requires_human_session=$1 WHERE id=$2`" + `, resolvedRHS, wi.ID)
	if err != nil {
		return err
	}
	evtPayload := classificationResolvedEventPayload(resolvedRHS)
	_, _ = tx.Exec(ctx, ` + "`INSERT INTO agent_events (event_type) VALUES ('wi_classification_resolved')`" + `, evtPayload)
	wi.RequiresHumanSession = &resolvedRHS
	return nil
}
`
	// Gated on the wrong thing: a condition that compiles, reads like a guard,
	// and does not test the column.
	classificationFixtureWrongGuard = `package p
func claim(wi *W, tx T) error {
	if wi.WIType == nil {
		resolvedRHS := defaultRequiresHumanSession
		_, err := tx.Exec(ctx, ` + "`UPDATE work_items SET requires_human_session=$1 WHERE id=$2`" + `, resolvedRHS, wi.ID)
		if err != nil {
			return err
		}
		evtPayload := classificationResolvedEventPayload(resolvedRHS)
		_, _ = tx.Exec(ctx, ` + "`INSERT INTO agent_events (event_type) VALUES ('wi_classification_resolved')`" + `, evtPayload)
		wi.RequiresHumanSession = &resolvedRHS
	}
	return nil
}
`
	// The row is written, the response is not: a caller reading its own claim's
	// reply still sees NULL.
	classificationFixtureNoStructWriteBack = `package p
func claim(wi *W, tx T) error {
	if wi.RequiresHumanSession == nil {
		resolvedRHS := defaultRequiresHumanSession
		_, err := tx.Exec(ctx, ` + "`UPDATE work_items SET requires_human_session=$1 WHERE id=$2`" + `, resolvedRHS, wi.ID)
		if err != nil {
			return err
		}
		evtPayload := classificationResolvedEventPayload(resolvedRHS)
		_, _ = tx.Exec(ctx, ` + "`INSERT INTO agent_events (event_type) VALUES ('wi_classification_resolved')`" + `, evtPayload)
	}
	return nil
}
`
	// No emission at all.
	classificationFixtureNoEmission = `package p
func claim(wi *W, tx T) error {
	if wi.RequiresHumanSession == nil {
		resolvedRHS := defaultRequiresHumanSession
		_, err := tx.Exec(ctx, ` + "`UPDATE work_items SET requires_human_session=$1 WHERE id=$2`" + `, resolvedRHS, wi.ID)
		if err != nil {
			return err
		}
		wi.RequiresHumanSession = &resolvedRHS
	}
	return nil
}
`
)

// TestClaimResolvesAnUnclassifiedWorkItemAndSaysSo is the arm.
//
// MUTANTS. Every one was applied to this tree and the verdict is what RAN.
//
//	── enforcement side (the claim path, the card untouched) ──
//	M63 delete the UPDATE from the resolving block
//	                                          RED  the_claim_path (no UPDATE work_items)
//	M64 change the guard to `wi.RequiresHumanSession != nil`
//	                                        GREEN  ⚠️ the analyser tests the condition for
//	                                               `RequiresHumanSession` and `nil`, and an
//	                                               INVERTED guard contains both. Named
//	                                               rather than left as silence: the
//	                                               inversion is caught by
//	                                               TestUnreachableMismatchBranchIsGone's
//	                                               ban on `*wi.RequiresHumanSession !=`
//	                                               only for the dereferenced shape, so
//	                                               this direction is the gap a DB arm
//	                                               would close
//	M65 replace the guard with `if true`, so every claim resolves
//	                                          RED  the_claim_path (gated on `true`,
//	                                               which does not test the column)
//	M66 delete `wi.RequiresHumanSession = &resolvedRHS`
//	                                          RED  the_claim_path (no struct write-back)
//	M67 rename the event the INSERT files the row under, leaving bestEffortExec's
//	    error message naming the old one
//	                                          RED  the_claim_path (no call files a row
//	                                               under the event type). ⚠️ It was
//	                                               GREEN on the first cut of this
//	                                               analyser, which located the call on
//	                                               the bare substring and found it in
//	                                               the error message — see the comment
//	                                               on the locator, and the incidental
//	                                               finding it raises about
//	                                               analyseEmission
//	── publication side (the card, the claim path untouched) ──
//	M68 strip the citing sentence's citation, path and symbol both
//	                                          RED  K12 ledger drift//
//
// ⚠️ The publication-side mutant strips the citation's FILE PATH as well as its
// test symbol, and that is not tidiness. Measured 2026-09-10: de-backticking the
// symbol alone left every one of this wave's seventeen sentences still counted as
// Cited, because cardclaims.CitesAnArm is satisfied by EITHER anchor — so the
// publication side of a citation is only as strong as whichever anchor a later
// edit leaves behind. Reported as an incidental finding of aihub#576.
func TestClaimResolvesAnUnclassifiedWorkItemAndSaysSo(t *testing.T) {
	// ── calibration, before anything trusts the analyser.
	t.Run("the_analyser_is_calibrated", func(t *testing.T) {
		for name, src := range map[string]string{
			"emits the event without storing the resolution": classificationFixtureNoUpdate,
			"resolves on every claim rather than on NULL":    classificationFixtureUngated,
			"gates on a different nullable field":            classificationFixtureWrongGuard,
			"stores the row but not the response":            classificationFixtureNoStructWriteBack,
			"does not record the resolution at all":          classificationFixtureNoEmission,
		} {
			if got := analyseClassificationResolution(src); len(got) == 0 {
				t.Errorf("the analyser reported CLEAN on a fixture that is not: %s.\nIt therefore "+
					"cannot detect that shape in %s either, and every clean result below would be "+
					"meaningless.", name, claimSourceFile)
			}
		}
		if got := analyseClassificationResolution(classificationFixtureClean); len(got) != 0 {
			t.Errorf("the analyser reported violations on the known-good fixture: %v.\nAn analyser "+
				"that cannot pass anything is not a gate, it is noise.", got)
		}
	})

	t.Run("the_claim_path", func(t *testing.T) {
		src := sourceOf(t, claimSourceFile)
		if len(src) < 4096 {
			t.Fatalf("%s is only %d bytes; that is not the claim implementation, and the analyser "+
				"would clear the wrong file", claimSourceFile, len(src))
		}
		for _, v := range analyseClassificationResolution(src) {
			t.Errorf("%s: %s", claimSourceFile, v)
		}
	})

	// ── the constant the resolution resolves TO, which is the second card
	// sentence's first clause. Its VALUE is pinned by
	// TestDefaultRequiresHumanSessionIsTrue; what is asserted here is that it is a
	// constant with no second source behind it, because "the server has no second
	// source to check a classification against" is the reason the 409 that was
	// supposed to police this never fired.
	t.Run("the_default_is_a_constant_not_a_lookup", func(t *testing.T) {
		src := sourceOf(t, claimSourceFile)
		if !strings.Contains(src, "resolvedRHS := defaultRequiresHumanSession") {
			t.Errorf("%s no longer resolves an unclassified work item straight from "+
				"defaultRequiresHumanSession. If a real per-wi_type source appeared, the card's "+
				"Open section changes with it — the whole reason that section calls this a design "+
				"question rather than a defect is that there is one constant and nothing to "+
				"disagree with it.", claimSourceFile)
		}
	})
}
