package server

// The same-value gate for aihub#552 (owner ruling 2026-09-09, option ③).
//
// TWO literals bound a recall page size at 200: the /ui memories page's limit
// ceiling — queryIntLenientUI(c, "limit", 50, 200) in ui_handlers_memory.go —
// and recallTopKCeiling in internal/domain/memory.go. They are equal on
// purpose, but no compile-time relationship holds them together: the domain
// constant is deliberately unexported, and the recorded decision beside it says
// no test may derive an EXPECTATION from it (a fixture that reads the constant
// under test moves with the defect instead of catching it). This gate is the
// shape that decision leaves open, the same shape the clampdisclosure ledger
// and queryparam_gate_test.go already use: read the SOURCE of both sites,
// extract the two literals, and assert they equal each other. Neither value is
// an oracle here — the relation is — so a mutant that moves either side alone
// goes red, and a mutant that moves both together stays green, which is exactly
// the contract the ruling asked for.
//
// If this went red because you moved one side deliberately: move the other side
// too. If the fork itself is being resolved (aihub#552's rejected option ① —
// wiring /ui to the real source), delete this gate together with the fork it
// pins; do not baseline it, and do not export the constant to appease it.

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"strconv"
	"testing"
)

// uiRecallLimitCeilingLiteral extracts the ceiling argument of the
// queryIntLenientUI(c, "limit", <default>, <ceiling>) call in
// ui_handlers_memory.go — the file scope matters: ui_handlers_wi.go carries an
// identical call whose 200 is a wi-list page size with no recall coupling, and
// it must stay free to move. Exactly one matching call must exist and its
// ceiling must be an integer literal; anything else fails loudly rather than
// letting the gate go vacuously green.
func uiRecallLimitCeilingLiteral(t *testing.T) int {
	t.Helper()
	const file = "ui_handlers_memory.go"
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, file, nil, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", file, err)
	}
	var ceilings []int
	ast.Inspect(f, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		id, ok := call.Fun.(*ast.Ident)
		if !ok || id.Name != "queryIntLenientUI" || len(call.Args) != 4 {
			return true
		}
		name, ok := call.Args[1].(*ast.BasicLit)
		if !ok || name.Kind != token.STRING || name.Value != `"limit"` {
			return true
		}
		lit, ok := call.Args[3].(*ast.BasicLit)
		if !ok || lit.Kind != token.INT {
			t.Fatalf("%s: the queryIntLenientUI limit ceiling is no longer an integer literal — "+
				"if /ui was just wired to a real source (aihub#552's rejected option ①), delete "+
				"this gate along with the fork it pins instead of adapting the extraction", file)
		}
		v, convErr := strconv.Atoi(lit.Value)
		if convErr != nil {
			t.Fatalf("%s: ceiling literal %q: %v", file, lit.Value, convErr)
		}
		ceilings = append(ceilings, v)
		return true
	})
	if len(ceilings) != 1 {
		t.Fatalf("%s: found %d queryIntLenientUI(..., %q, ...) calls, want exactly 1 — "+
			"the extraction no longer matches the site; fix the gate before trusting it",
			file, len(ceilings), "limit")
	}
	return ceilings[0]
}

// domainRecallTopKCeilingLiteral extracts recallTopKCeiling's literal value
// from internal/domain/memory.go, by source text rather than by importing it —
// see the file comment for why the constant may not be the oracle.
func domainRecallTopKCeilingLiteral(t *testing.T) int {
	t.Helper()
	file := filepath.Join("..", "domain", "memory.go")
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, file, nil, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", file, err)
	}
	var vals []int
	ast.Inspect(f, func(n ast.Node) bool {
		spec, ok := n.(*ast.ValueSpec)
		if !ok {
			return true
		}
		for i, name := range spec.Names {
			if name.Name != "recallTopKCeiling" {
				continue
			}
			if i >= len(spec.Values) {
				t.Fatalf("%s: recallTopKCeiling has no explicit value in its spec — "+
					"the gate cannot read it; restore the literal or retire the gate deliberately", file)
			}
			lit, ok := spec.Values[i].(*ast.BasicLit)
			if !ok || lit.Kind != token.INT {
				t.Fatalf("%s: recallTopKCeiling is no longer an integer literal — "+
					"the gate cannot read it; restore the literal or retire the gate deliberately", file)
			}
			v, convErr := strconv.Atoi(lit.Value)
			if convErr != nil {
				t.Fatalf("%s: recallTopKCeiling literal %q: %v", file, lit.Value, convErr)
			}
			vals = append(vals, v)
		}
		return true
	})
	if len(vals) != 1 {
		t.Fatalf("%s: found %d declarations of recallTopKCeiling, want exactly 1 — "+
			"the extraction no longer matches the site; fix the gate before trusting it",
			file, len(vals))
	}
	return vals[0]
}

// TestUIRecallLimitCeilingEqualsRecallTopKCeiling is aihub#552's same-value
// assertion: the /ui memories page's limit ceiling and recallTopKCeiling are
// deliberately equal, and this is the one place that holds them together.
func TestUIRecallLimitCeilingEqualsRecallTopKCeiling(t *testing.T) {
	ui := uiRecallLimitCeilingLiteral(t)
	dom := domainRecallTopKCeilingLiteral(t)
	if ui != dom {
		t.Fatalf("the /ui memories page caps limit at %d (queryIntLenientUI ceiling in "+
			"ui_handlers_memory.go) but recallTopKCeiling (internal/domain/memory.go) is %d — "+
			"the two are deliberately equal (aihub#552, owner ruling 2026-09-09, option ③); "+
			"move them together, or resolve the fork for real and delete this gate", ui, dom)
	}
}
