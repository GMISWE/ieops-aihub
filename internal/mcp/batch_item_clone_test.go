package mcp

// aihub#543 probe wave 2 — the `docs/mcp-cards/pf_batch_create_work_items.md`
// sentence about whose data the batch loop edits.
//
//	"Each item is cloned before defaulting, so filling in `project` or
//	 `force_reason` does not edit the caller's own array."
//	    -> TestBatchItemDefaultingRunsOnACopy
//
// WHY THIS IS AN INTERNAL TEST AND NOT A WIRE PROBE. The claim is about a map
// the caller owns, and there is no caller on the far side of an MCP session who
// still holds it: the SDK marshals arguments to JSON and the handler decodes a
// fresh map, so a handler that mutated its input in place would be
// indistinguishable, over the wire, from one that did not. The property is real
// and load-bearing for anything that calls the handler in-process — and the only
// place it can be observed is here.
//
// Two arms, because either alone is satisfied by the defect:
//
//   - the helper really copies (a wire-visible behaviour would not need saying,
//     and a `cloneArgs` that returned its argument would pass any test that only
//     checked the call site exists), and
//   - the call site really runs BEFORE the defaulting (a perfect copier called
//     after the first `item[...] = …` protects nothing).
//
//	GOWORK=off go test ./internal/mcp/ -run TestBatchItemDefaultingRunsOnACopy -count=1 -v

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"testing"
)

// batchHandlerSourceFile is the file the batch loop lives in.
const batchHandlerSourceFile = "tools_lifecycle.go"

// cloneOrderFinding is one reason a source does not provably clone before it
// defaults. An empty result means clean.
type cloneOrderFinding string

// analyseCloneOrder reports every reason src cannot be shown to clone a batch
// item before writing to it.
//
// It walks the parsed tree rather than scanning text, for the reason
// analyseEmission in internal/domain gives for the same choice: a text scan
// models a shape it HOPES the code has, and both of the ways this one could be
// evaded — hoisting the write into a helper, reordering two adjacent lines —
// leave the same bytes in the file in a different order.
//
// The rule it enforces, inside the range loop that clones:
//
//	(1) some statement assigns `X = cloneArgs(X)` — a clone that is stored back
//	    over the caller's handle, not into a second variable the loop then
//	    ignores, and
//	(2) every index-assignment to X in that loop comes after it.
//
// A source with no cloneArgs call at all is a violation, never a silent pass.
func analyseCloneOrder(src string) []cloneOrderFinding {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, batchHandlerSourceFile, src, 0)
	if err != nil {
		return []cloneOrderFinding{cloneOrderFinding(
			"source does not parse: " + err.Error() +
				" — a file that cannot be parsed cannot be cleared of anything")}
	}

	var loops []*ast.RangeStmt
	ast.Inspect(f, func(n ast.Node) bool {
		r, ok := n.(*ast.RangeStmt)
		if !ok {
			return true
		}
		found := false
		ast.Inspect(r.Body, func(inner ast.Node) bool {
			if call, isCall := inner.(*ast.CallExpr); isCall {
				if id, isIdent := call.Fun.(*ast.Ident); isIdent && id.Name == "cloneArgs" {
					found = true
				}
			}
			return true
		})
		if found {
			loops = append(loops, r)
		}
		return true
	})
	if len(loops) == 0 {
		return []cloneOrderFinding{cloneOrderFinding(
			"no range loop in " + batchHandlerSourceFile + " calls cloneArgs — the batch loop " +
				"either stopped cloning or stopped being a loop, and either way the caller's " +
				"entries are whatever the defaulting leaves them")}
	}

	var out []cloneOrderFinding
	for _, loop := range loops {
		// The clone: `X = cloneArgs(X)`, with the same identifier on both sides.
		clonedAt := map[string]token.Pos{}
		for _, stmt := range loop.Body.List {
			assign, ok := stmt.(*ast.AssignStmt)
			if !ok || len(assign.Lhs) != 1 || len(assign.Rhs) != 1 {
				continue
			}
			lhs, isIdent := assign.Lhs[0].(*ast.Ident)
			call, isCall := assign.Rhs[0].(*ast.CallExpr)
			if !isIdent || !isCall {
				continue
			}
			fn, isFnIdent := call.Fun.(*ast.Ident)
			if !isFnIdent || fn.Name != "cloneArgs" || len(call.Args) != 1 {
				continue
			}
			arg, isArgIdent := call.Args[0].(*ast.Ident)
			if !isArgIdent || arg.Name != lhs.Name {
				out = append(out, cloneOrderFinding(
					"the clone is stored somewhere other than over the handle it copied, so the "+
						"original is still in scope and still the one a later write could reach"))
				continue
			}
			clonedAt[lhs.Name] = assign.Pos()
		}
		if len(clonedAt) == 0 {
			out = append(out, cloneOrderFinding(
				"a loop calls cloneArgs but never assigns the copy back over the variable it "+
					"copied, so the defaulting below still writes through the caller's own map"))
			continue
		}

		// Every index-assignment to a cloned identifier must come after its clone.
		ast.Inspect(loop.Body, func(n ast.Node) bool {
			assign, ok := n.(*ast.AssignStmt)
			if !ok {
				return true
			}
			for _, lhs := range assign.Lhs {
				idx, isIndex := lhs.(*ast.IndexExpr)
				if !isIndex {
					continue
				}
				base, isIdent := idx.X.(*ast.Ident)
				if !isIdent {
					continue
				}
				pos, cloned := clonedAt[base.Name]
				if !cloned {
					out = append(out, cloneOrderFinding(
						"the loop writes into `"+base.Name+"[…]`, which it never cloned"))
					continue
				}
				if assign.Pos() < pos {
					out = append(out, cloneOrderFinding(
						"the loop writes into `"+base.Name+"[…]` BEFORE it clones it, so that write "+
							"lands in the caller's own entry and the clone protects only what comes after"))
				}
			}
			return true
		})
	}
	return out
}

// The calibration fixtures. Each is the smallest source that carries one known
// answer, so the analyser is shown to separate the two classes before anything
// trusts it against the real file.
const (
	cloneFixtureClean = `package p
func h(raw []any, project string) {
	for i, entry := range raw {
		item, _ := entry.(map[string]any)
		item = cloneArgs(item)
		if item["project"] == nil {
			item["project"] = project
		}
		_ = i
	}
}
`
	cloneFixtureWritesFirst = `package p
func h(raw []any, project string) {
	for i, entry := range raw {
		item, _ := entry.(map[string]any)
		item["project"] = project
		item = cloneArgs(item)
		_ = i
	}
}
`
	cloneFixtureCloneDiscarded = `package p
func h(raw []any, project string) {
	for i, entry := range raw {
		item, _ := entry.(map[string]any)
		copied := cloneArgs(item)
		item["project"] = project
		_ = copied
		_ = i
	}
}
`
	cloneFixtureNoClone = `package p
func h(raw []any, project string) {
	for i, entry := range raw {
		item, _ := entry.(map[string]any)
		item["project"] = project
		_ = i
	}
}
`
)

// TestBatchItemDefaultingRunsOnACopy is the arm.
//
// MUTANTS. Every one was applied to this tree and the verdict is what RAN.
//
//	── enforcement side (the handler, the card untouched) ──
//	M58 delete the `item = cloneArgs(item)` line from the batch loop
//	                                          RED  the_call_site (no loop clones)
//	M59 move it below the `item["project"] = project` default
//	                                          RED  the_call_site (writes before cloning)
//	M60 make cloneArgs return its argument     RED  the_helper_really_copies
//	M61 assign the copy to a second variable the loop ignores
//	                                          RED  the_call_site (clone discarded)
//	── publication side (the card, the handler untouched) ──
//	M62 strip the citing sentence's citation, path and symbol both
//	                                          RED  K12 ledger drift//
//
// ⚠️ The publication-side mutant strips the citation's FILE PATH as well as its
// test symbol, and that is not tidiness. Measured 2026-09-10: de-backticking the
// symbol alone left every one of this wave's seventeen sentences still counted as
// Cited, because cardclaims.CitesAnArm is satisfied by EITHER anchor — so the
// publication side of a citation is only as strong as whichever anchor a later
// edit leaves behind. Reported as an incidental finding of aihub#576.
func TestBatchItemDefaultingRunsOnACopy(t *testing.T) {
	// ── the analyser's own calibration, before anything trusts it.
	t.Run("the_analyser_is_calibrated", func(t *testing.T) {
		for name, src := range map[string]string{
			"writes into the entry before cloning it": cloneFixtureWritesFirst,
			"clones into a variable it then ignores":  cloneFixtureCloneDiscarded,
			"does not clone at all":                   cloneFixtureNoClone,
		} {
			if got := analyseCloneOrder(src); len(got) == 0 {
				t.Errorf("the analyser reported CLEAN on a fixture that is not: %s.\nIt therefore "+
					"cannot detect that shape in %s either, and every clean result below would be "+
					"meaningless.", name, batchHandlerSourceFile)
			}
		}
		if got := analyseCloneOrder(cloneFixtureClean); len(got) != 0 {
			t.Errorf("the analyser reported violations on the known-good fixture: %v.\nAn analyser "+
				"that cannot pass anything is not a gate, it is noise.", got)
		}
	})

	t.Run("the_helper_really_copies", func(t *testing.T) {
		caller := map[string]any{"goal": "an item the caller still holds"}
		copied := cloneArgs(caller)

		copied["project"] = "aihub"
		copied["force_reason"] = "force_create=true via MCP (admin bypass dedup check)"
		copied["goal"] = "rewritten in the copy"

		if _, leaked := caller["project"]; leaked {
			t.Errorf("defaulting the copy put `project` into the caller's own entry: %#v", caller)
		}
		if _, leaked := caller["force_reason"]; leaked {
			t.Errorf("defaulting the copy put `force_reason` into the caller's own entry: %#v", caller)
		}
		if got := caller["goal"]; got != "an item the caller still holds" {
			t.Errorf("the caller's goal is now %#v — cloneArgs handed back the same map, so every "+
				"per-item default the batch applies is a write into the array it was given", got)
		}
		// The floor: a "copy" that dropped what it was copying would satisfy every
		// assertion above.
		if copied["goal"] != "rewritten in the copy" || len(copied) != len(caller)+2 {
			t.Errorf("the copy holds %#v against a caller entry of %#v — it must carry the entry's "+
				"own field plus exactly the two defaults written above, or the assertions on the "+
				"caller are about an empty map rather than about a copy", copied, caller)
		}
	})

	t.Run("the_call_site", func(t *testing.T) {
		raw, err := os.ReadFile(batchHandlerSourceFile)
		if err != nil {
			t.Fatalf("read %s: %v", batchHandlerSourceFile, err)
		}
		if len(raw) < 4096 {
			t.Fatalf("%s is only %d bytes; that is not the lifecycle tool registration, and the "+
				"analyser would clear the wrong file", batchHandlerSourceFile, len(raw))
		}
		for _, v := range analyseCloneOrder(string(raw)) {
			t.Errorf("%s: %s\nThe card tells callers that filling in `project` or `force_reason` "+
				"does not edit the array they passed. A copier that is perfect and called too "+
				"late protects nothing, which is why the ORDER is what this asserts.",
				batchHandlerSourceFile, v)
		}
	})
}
