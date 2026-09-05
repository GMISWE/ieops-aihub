package domain

import (
	"bytes"
	"go/ast"
	"go/parser"
	"go/printer"
	"go/token"
	"regexp"
	"strings"
	"testing"
)

// requires_human_session honesty gates (aihub#359).
//
// WHAT WENT WRONG
// ---------------
// Two declarations in the claim path read as if they did something and did nothing.
//
//	A. `wiTypeDef` was a struct literal initialised to a constant `true` and then overwritten
//	   ONLY from the work item's own RequiresHumanSession field. On the branch that consumed it
//	   — the one taken when that field is NULL — there was by construction nothing to overwrite
//	   it with, so the "resolved" value was the constant. The code then emitted a
//	   `wi_classification_resolved` event whose payload named `wi_type`, which made the event
//	   read as though the type had been looked up and had decided the outcome. It had not: the
//	   table that once held those defaults (scenario_phase_configs) was removed, as the comment
//	   four lines above the struct already admitted.
//
//	B. An `else if *wi.RequiresHumanSession != resolvedRHS` returned 409
//	   REQUIRES_HUMAN_SESSION_MISMATCH. Reaching that else required the field to be non-nil, and
//	   in exactly that case `resolvedRHS` had just been assigned FROM that same field — so the
//	   condition was `x != x` and the 409 was unreachable. Its message said "phase config says
//	   %v", pointing at the same removed table.
//
// The BEHAVIOUR was correct and is unchanged: an unclassified work item still defaults to
// requiring a human. Only the dishonesty was removed.
//
// WHY THESE GATES ARE STRUCTURAL AND NOT BEHAVIOURAL
// -------------------------------------------------
// Both defects are unreachable-code defects, and unreachable code cannot be observed by calling
// the function. For B especially: constructing a "mismatch" and asserting the claim SUCCEEDS
// passes identically before and after the fix, because the branch never fired in the first
// place — such a test is a demonstration, not a gate. The only assertion that separates the two
// trees is one about the shipped code. So these parse run_attempts.go and assert on its AST.
//
// WHY THE AST AND NOT A TEXT SCAN
// -------------------------------
// The first cut of this file scanned text: it took a window around the emission site and, if
// that window called something whose name matched /[Pp]ayload/, appended that function's body.
// Review broke it in one edit. This kept every gate green while restoring the defect in full:
//
//	wt := *wi.WIType
//	evtPayload, _ := json.Marshal(annotate(classificationResolvedEventPayload(v), wt))
//
// The wrapper's name did not match the regex, so the walker never followed it, and hoisting
// *wi.WIType into a local kept the banned identifier out of the window. Two holes that only
// combine — which is exactly how a text scan fails: it models a SHAPE it hopes the code has.
// The analyser below asserts on the parsed call graph instead, and its own negative controls
// are synthetic source files carrying the known-bad shapes, so a matcher that has stopped
// matching fails loudly rather than reporting a clean file.

const claimSourceFile = "run_attempts.go"

// classificationEventType is the SQL literal that marks the emission site.
const classificationEventType = "wi_classification_resolved"

// payloadBuilderName is the ONLY function whose bytes may become this event's payload.
// Declared as a literal, not by referencing the production identifier: a gate that imports the
// value it checks agrees with the code by construction and can never disagree with it.
const payloadBuilderName = "classificationResolvedEventPayload"

// ── The analyser ─────────────────────────────────────────────────────────────────────────────

// analyseEmission reports every reason the wi_classification_resolved payload in src is not
// provably free of wi_type. An empty result means clean; each element is a human-readable
// violation. Parse failures and "the shape is not modelled any more" are violations too, never
// silent passes.
//
// The invariant it enforces: the bytes stored in agent_events are exactly the bytes returned by
// payloadBuilderName, and that function does not name the wi_type. It checks this by requiring,
// inside the block that emits the event:
//
//	(1) exactly one assignment whose right-hand side is a BARE call to payloadBuilderName,
//	(2) the identifier so assigned is what the emitting call receives,
//	(3) no json.Marshal anywhere in that block — marshalling belongs inside the builder, and
//	    its presence here is the signature of the wrapping trick,
//	(4) the builder's own body never mentions wi_type or WIType.
func analyseEmission(src string) []string {
	fset := token.NewFileSet()
	// Mode 0 (not ParseComments) drops comments outright. That is load-bearing: the fix for
	// each defect deliberately leaves a comment quoting what must not come back, and a scan
	// that cannot tell the warning from the offence would force the fix to ship undocumented.
	f, err := parser.ParseFile(fset, claimSourceFile, src, 0)
	if err != nil {
		return []string{"source does not parse: " + err.Error() +
			" — a file that cannot be parsed cannot be cleared of anything"}
	}

	// Locate the emitting call: the one carrying the event-type string literal.
	var emit *ast.CallExpr
	ast.Inspect(f, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		for _, a := range call.Args {
			if lit, ok := a.(*ast.BasicLit); ok && lit.Kind == token.STRING &&
				strings.Contains(lit.Value, classificationEventType) {
				emit = call
			}
		}
		return true
	})
	if emit == nil {
		return []string{"no call carrying the string " + classificationEventType +
			" — the event is not emitted here any more, so this gate is guarding nothing"}
	}

	block := enclosingBlock(f, emit)
	if block == nil {
		return []string{"the emission call is not inside any block — the analyser cannot scope " +
			"its checks and must not report a clean file"}
	}

	var out []string

	// (1) exactly one bare assignment from the sanctioned builder
	var assigned []string
	ast.Inspect(block, func(n ast.Node) bool {
		as, ok := n.(*ast.AssignStmt)
		if !ok || len(as.Rhs) != 1 || len(as.Lhs) != 1 {
			return true
		}
		call, ok := as.Rhs[0].(*ast.CallExpr)
		if !ok {
			return true
		}
		if id, ok := call.Fun.(*ast.Ident); ok && id.Name == payloadBuilderName {
			if lhs, ok := as.Lhs[0].(*ast.Ident); ok {
				assigned = append(assigned, lhs.Name)
			}
		}
		return true
	})
	switch len(assigned) {
	case 1: // good
	case 0:
		out = append(out, "the emitting block never assigns a bare call to "+payloadBuilderName+
			"(...). The payload must come from that function and nothing else, so that the bytes "+
			"the compiled test asserts on are the bytes that reach agent_events.")
	default:
		out = append(out, "the emitting block assigns "+payloadBuilderName+
			"(...) more than once; the analyser cannot tell which result is stored")
	}

	// (3) no marshalling at the call site — it belongs inside the builder
	ast.Inspect(block, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		if sel, ok := call.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "Marshal" {
			if pkg, ok := sel.X.(*ast.Ident); ok && pkg.Name == "json" {
				out = append(out, "json.Marshal is called in the emitting block. Marshalling "+
					"belongs inside "+payloadBuilderName+"; a Marshal here is how a wrapper "+
					"(json.Marshal(annotate(build(v), wt))) slips extra keys into the stored "+
					"event while the builder's own unit test stays green.")
			}
		}
		return true
	})

	// (2) the emitting call must receive exactly that identifier
	if len(assigned) == 1 {
		want := assigned[0]
		passed := false
		for _, a := range emit.Args {
			if id, ok := a.(*ast.Ident); ok && id.Name == want {
				passed = true
			}
		}
		if !passed {
			out = append(out, "the value built by "+payloadBuilderName+" is assigned to "+want+
				", but "+want+" is not passed to the emitting call — something else is being stored")
		}
	}

	// (4) the builder must not name the wi_type
	body, ok := funcBodyAST(f, payloadBuilderName)
	if !ok {
		out = append(out, "func "+payloadBuilderName+" is not declared in "+claimSourceFile+
			". The analyser reads its body to prove the payload omits wi_type; if the builder "+
			"moved, move this gate with it rather than letting it pass on a body it cannot see.")
	} else if hit := namesWIType(body); hit != "" {
		out = append(out, "func "+payloadBuilderName+" names "+hit+". This event is emitted only "+
			"when the work item row has no classification, and on that branch the value is a "+
			"constant — the wi_type is never read, so naming it claims a derivation that did "+
			"not happen (aihub#359).")
	}

	return out
}

// enclosingBlock returns the innermost BlockStmt containing target.
func enclosingBlock(f *ast.File, target ast.Node) *ast.BlockStmt {
	var stack []ast.Node
	var found *ast.BlockStmt
	ast.Inspect(f, func(n ast.Node) bool {
		if n == nil {
			if len(stack) > 0 {
				stack = stack[:len(stack)-1]
			}
			return true
		}
		if n == target && found == nil {
			for i := len(stack) - 1; i >= 0; i-- {
				if b, ok := stack[i].(*ast.BlockStmt); ok {
					found = b
					break
				}
			}
		}
		stack = append(stack, n)
		return true
	})
	return found
}

// funcBodyAST renders the named top-level func's body back to source with go/printer.
//
// Printing beats both alternatives. Unlike brace counting it cannot be truncated by a stray
// brace inside a string literal — the failure mode declared_resources_wiring_test.go's bodyOf
// comment records as having produced a false pass. And unlike reassembling identifiers from an
// ast.Inspect walk it preserves operand ORDER, because Inspect visits a node before its
// children: a BinaryExpr walk emits the operator ahead of its own left operand, so any regex
// keyed on `x != y` would silently stop matching.
func funcBodyAST(f *ast.File, name string) (string, bool) {
	for _, d := range f.Decls {
		fn, ok := d.(*ast.FuncDecl)
		if !ok || fn.Name.Name != name || fn.Body == nil {
			continue
		}
		var buf bytes.Buffer
		if err := printer.Fprint(&buf, token.NewFileSet(), fn.Body); err != nil {
			return "", false
		}
		return buf.String(), true
	}
	return "", false
}

func namesWIType(s string) string {
	if strings.Contains(s, "wi_type") {
		return "wi_type"
	}
	if strings.Contains(s, "WIType") {
		return "WIType"
	}
	return ""
}

// ── Negative controls: synthetic sources carrying the known-bad shapes ────────────────────────

// The analyser must reject each of these. They are the two forms the defect has actually taken:
// the original inline map, and the wrapper that defeated the first version of this gate.

const fixtureInlineMap = `package domain
func FnClaimWorkItem() {
	if wi.RequiresHumanSession == nil {
		evtPayload, _ := json.Marshal(map[string]any{
			"wi_type":                *wi.WIType,
			"requires_human_session": resolvedRHS,
		})
		_, _ = tx.Exec(ctx, ` + "`INSERT INTO agent_events VALUES ('wi_classification_resolved')`" + `, evtPayload)
	}
}
func classificationResolvedEventPayload(r bool) []byte { return nil }
`

const fixtureWrapped = `package domain
func FnClaimWorkItem() {
	if wi.RequiresHumanSession == nil {
		wt := *wi.WIType
		evtPayload, _ := json.Marshal(annotate(classificationResolvedEventPayload(resolvedRHS), wt))
		_, _ = tx.Exec(ctx, ` + "`INSERT INTO agent_events VALUES ('wi_classification_resolved')`" + `, evtPayload)
	}
}
func classificationResolvedEventPayload(r bool) []byte { return nil }
`

const fixtureBuilderNamesWIType = `package domain
func FnClaimWorkItem() {
	if wi.RequiresHumanSession == nil {
		evtPayload := classificationResolvedEventPayload(resolvedRHS)
		_, _ = tx.Exec(ctx, ` + "`INSERT INTO agent_events VALUES ('wi_classification_resolved')`" + `, evtPayload)
	}
}
func classificationResolvedEventPayload(r bool) []byte {
	b, _ := json.Marshal(map[string]any{"wi_type": "x", "requires_human_session": r})
	return b
}
`

// fixtureClean is the shape the production file is supposed to have. It exists so a *broken*
// analyser — one that reports violations unconditionally — is caught too. A gate that always
// fails gets deleted just as fast as one that always passes.
const fixtureClean = `package domain
func FnClaimWorkItem() {
	if wi.RequiresHumanSession == nil {
		evtPayload := classificationResolvedEventPayload(resolvedRHS)
		_, _ = tx.Exec(ctx, ` + "`INSERT INTO agent_events VALUES ('wi_classification_resolved')`" + `, evtPayload)
	}
}
func classificationResolvedEventPayload(r bool) []byte {
	b, _ := json.Marshal(map[string]any{"requires_human_session": r, "source": "server_default"})
	return b
}
`

// TestEmissionAnalyserIsCalibrated runs the analyser over known-good and known-bad sources
// BEFORE any test trusts it against the real file. This is the whole anti-vacuity story for
// Gate A: the analyser is only evidence about run_attempts.go if it can be shown to separate
// the two classes on inputs whose answer is already known.
func TestEmissionAnalyserIsCalibrated(t *testing.T) {
	bad := map[string]string{
		"inline map naming wi_type (the original aihub#359 defect)": fixtureInlineMap,
		"wrapper that re-adds wi_type at the call site":             fixtureWrapped,
		"builder itself naming wi_type":                             fixtureBuilderNamesWIType,
	}
	for name, src := range bad {
		if got := analyseEmission(src); len(got) == 0 {
			t.Errorf("analyser reported CLEAN on a fixture that is not: %s\n"+
				"It therefore cannot detect that shape in run_attempts.go either, and every "+
				"clean result it gives below would be meaningless.", name)
		}
	}
	if got := analyseEmission(fixtureClean); len(got) != 0 {
		t.Errorf("analyser reported violations on the known-good fixture: %v\n"+
			"An analyser that cannot pass anything is not a gate, it is noise.", got)
	}
}

// ── Gate A ───────────────────────────────────────────────────────────────────────────────────

// TestClassificationEventDoesNotNameWIType is the A-half: the event must not imply that the
// wi_type participated in a decision it took no part in.
func TestClassificationEventDoesNotNameWIType(t *testing.T) {
	src := sourceOf(t, claimSourceFile)
	if len(src) < 4096 {
		t.Fatalf("%s is only %d bytes; that is not the claim implementation, and the analyser "+
			"would clear the wrong file", claimSourceFile, len(src))
	}
	for _, v := range analyseEmission(src) {
		t.Errorf("%s: %s", claimSourceFile, v)
	}
}

// TestNoFakeWITypeDefinitionStruct bans the struct literal that dressed a constant up as a
// per-type definition — the declaration-level half of defect A.
//
// This ban is keyed on the NAME, which makes it a tripwire rather than a proof: renaming the
// struct to `typeDef` would evade it. That is acceptable only because Gate A above is keyed on
// the call graph and does the real work; this one exists so the specific dead shape cannot
// reappear verbatim. Do not mistake it for a general guarantee.
func TestNoFakeWITypeDefinitionStruct(t *testing.T) {
	src := sourceOf(t, claimSourceFile)

	// Positive anchor: the fail-safe default must still exist. The ban is otherwise satisfied
	// by deleting the defaulting behaviour altogether, which the owner ruled must NOT change.
	// Tolerant of an explicit type and of gofmt's alignment padding inside a const block; the
	// VALUE is pinned in compiled form by TestDefaultRequiresHumanSessionIsTrue, which no
	// cosmetic refactor can break.
	anchor := regexp.MustCompile(`defaultRequiresHumanSession\s*(bool\s*)?=\s*true`)
	if !anchor.MatchString(src) {
		t.Errorf("%s no longer declares defaultRequiresHumanSession = true. Removing the fake "+
			"per-type struct must not remove the behaviour it was faking: an unclassified work "+
			"item still defaults to requiring a human. That default is correct and was ruled "+
			"out of scope for aihub#359.", claimSourceFile)
	}

	if strings.Contains(src, "wiTypeDef") {
		t.Errorf("%s still declares wiTypeDef. That struct was initialised to a constant and "+
			"then overwritten only from the work item's OWN requires_human_session field, so on "+
			"the NULL branch — the only branch that consumed it — nothing could overwrite it and "+
			"the 'resolved' value was the constant. Name the constant instead of building a "+
			"struct that impersonates a lookup table (aihub#359).", claimSourceFile)
	}
}

// ── Gate B ───────────────────────────────────────────────────────────────────────────────────

// mismatchComparisonRe matches the self-comparison shape: the work item's own field compared
// against anything. Keyed on the SHAPE rather than on the name `resolvedRHS`, because renaming
// the right-hand side would sidestep a name-keyed ban while leaving `x != x` exactly as
// unreachable as before.
var mismatchComparisonRe = regexp.MustCompile(`\*wi\.RequiresHumanSession\s*!=`)

// preFixMismatchFixture is the pre-aihub#359 dead branch, verbatim — the negative control.
const preFixMismatchFixture = "\t} else if *wi.RequiresHumanSession != resolvedRHS {\n" +
	"\t\t// C-R9-12: mismatch → 409 REQUIRES_HUMAN_SESSION_MISMATCH\n" +
	"\t\ttx.Rollback(ctx) //nolint:errcheck\n" +
	"\t\treturn nil, NewErrDetails(ErrRequiresHumanSessionMismatch,\n"

// TestUnreachableMismatchBranchIsGone is the B-half.
func TestUnreachableMismatchBranchIsGone(t *testing.T) {
	raw := sourceOf(t, claimSourceFile)

	// Comments are stripped for the bans below: the fix deliberately leaves a comment quoting
	// the removed branch so the next reader knows why it must not return, and a raw-text scan
	// cannot tell that warning from the offence. Parsing draws the line the compiler draws.
	code := stripComments(t, raw)

	const bannedErr = "ErrRequiresHumanSessionMismatch"

	// ── Negative controls: both predicates, over the text they exist to reject ──
	if !strings.Contains(preFixMismatchFixture, bannedErr) {
		t.Fatal("the literal ban does not fire on the pre-aihub#359 branch it exists to reject")
	}
	if !mismatchComparisonRe.MatchString(preFixMismatchFixture) {
		t.Fatalf("mismatchComparisonRe does not match the pre-aihub#359 condition it exists to "+
			"find (%q). Keyed on the wrong shape, it would report a clean file on every tree.",
			preFixMismatchFixture)
	}
	// ...and the strip must not swallow real code: the banned shape survives stripping.
	if !mismatchComparisonRe.MatchString(stripComments(t, "package p\nfunc f() {\n"+
		"\tif *wi.RequiresHumanSession != x {\n\t\t_ = 1\n\t}\n}\n")) {
		t.Fatal("comment stripping removes the banned comparison from code that really contains " +
			"it — the ban below would clear every file")
	}

	// ── Positive anchors: the claim path and its NULL branch must still be here ──
	if !strings.Contains(code, "func FnClaimWorkItem(") {
		t.Fatalf("%s no longer defines FnClaimWorkItem; this gate is reading the wrong file", claimSourceFile)
	}
	if !strings.Contains(code, "wi.RequiresHumanSession == nil") {
		t.Errorf("%s no longer branches on `wi.RequiresHumanSession == nil`. Removing the "+
			"unreachable mismatch branch must leave the reachable defaulting branch intact — "+
			"that branch is what writes the fail-safe value back to the row.", claimSourceFile)
	}

	// ── Ban 1: the unreachable 409 ──
	if strings.Contains(code, bannedErr) {
		t.Errorf("%s still references %s in code. That 409 was returned from an `else if` "+
			"reached only when wi.RequiresHumanSession is non-nil — and in that case the value "+
			"it was compared against had just been assigned from that same field, making the "+
			"condition `x != x` and the error unreachable. Its message named "+
			"scenario_phase_configs, a table already removed. Deleted by aihub#359.\n"+
			"NOTE: the error CODE itself is deliberately still declared in errors.go and still "+
			"maps to 409 — this gate bans the unreachable call site, not the vocabulary entry.",
			claimSourceFile, bannedErr)
	}

	// ── Ban 2: the self-comparison shape, independent of which error it returns ──
	if loc := mismatchComparisonRe.FindString(code); loc != "" {
		t.Errorf("%s compares the work item's own requires_human_session against something "+
			"(%q). Every value in this function that could sit on the right-hand side is derived "+
			"from that same field or is a fixed constant, so such a comparison is either "+
			"always-false or always-true — never a check. Do not reinstate one until the server "+
			"has a second, INDEPENDENT source for the expected value; the scenario repo, which "+
			"holds the real per-wi_type defaults, is readable only by the client.",
			claimSourceFile, loc)
	}
}

// stripComments returns src with comments removed, by parsing without them and re-printing.
//
// Re-printing rather than reassembling from an ast.Inspect walk: Inspect visits a node before
// its children, so a BinaryExpr would emit its operator ahead of its own left operand and
// `*wi.RequiresHumanSession !=` would come back out as `!= *wi.RequiresHumanSession`. The shape
// regex would then match nothing and the ban would clear every file. The last negative control
// in the caller exists to catch exactly that regression.
func stripComments(t *testing.T, src string) string {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "src.go", src, 0)
	if err != nil {
		t.Fatalf("parsing: %v — a file that does not parse cannot be cleared of the banned shapes", err)
	}
	var buf bytes.Buffer
	if err := printer.Fprint(&buf, fset, f); err != nil {
		t.Fatalf("re-printing: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "func ") {
		t.Fatalf("comment-stripped source lost its declarations (%d bytes from %d); the scans "+
			"over it would pass on the wrong text", len(out), len(src))
	}
	return out
}
