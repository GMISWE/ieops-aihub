package server

// aihub#543 wave 2, slice L6 — the two hop-4 sentences of the
// pf_reinforce_memory card that are about the CLAMP rather than about the
// refusal beside it.
//
//	"Since aihub#433 that clamp reads internal/domain/memory.go
//	 (MinBaseStrength) and (MaxBaseStrength) rather than two literals that
//	 happened to agree with them."
//
//	"The consequence is that the clamp and the codec can no longer disagree. A
//	 stored value is SMALLINT and therefore whole, an accepted delta is whole,
//	 and the clamp's two bounds are integers — so every sum this handler can
//	 produce is already whole and the int2 truncation is unreachable from the
//	 API."
//
// Both are structural claims about this handler, and both were unheld. The
// range arms in internal/domain hold what the constants ARE and what the
// published descriptions SAY about them; nothing held that this clamp is the
// thing reading them, and nothing held the closure argument the second sentence
// makes — which is the argument that makes deleting the RETURNING clause look
// safe. The card says the RETURNING clause stays anyway, and the reason it
// gives is that a response must not be able to disagree with the row rather
// than that a disagreement is reachable; that reasoning only survives if the
// closure is checked rather than assumed.
//
// No database: one arm reads this package's own AST, the other exercises the
// clamp's arithmetic over the values it can actually be handed.

import (
	"go/ast"
	"go/parser"
	"go/token"
	"math"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/GMISWE/ieops-aihub/internal/domain"
)

// TestReinforceClampBoundsAreNamedConstantsNotLiterals reads handleReinforceMemory
// and requires its two saturation comparisons to name the domain constants.
//
// 🔴 Both directions, and the second is the one that matters. Requiring the
// selectors to be PRESENT is satisfied by a handler that mentions them in a
// comment-free way and still compares against 5.0; requiring the comparisons to
// carry NO numeric literal is satisfied by a handler that compares against some
// other constant. Together they say: the clamp's bounds are these two names, and
// nothing else. That is the whole of what aihub#433 bought — before it, one
// column had three disjoint answers about its range and each was locally
// self-consistent.
//
//	M1  replace `domain.MaxBaseStrength` with `5.0` in the clamp     RED
//	    (LITERAL_BOUND)
//	M2  replace `domain.MinBaseStrength` with a local const          RED
//	    (MISSING_BOUND — the selector is gone)
//	M3  delete the upper clamp entirely                              RED
//	    (only one comparison found)
//	M4  rename the handler                                           RED
//	    (HANDLER_NOT_FOUND, rather than a vacuous pass over no
//	    function at all)
func TestReinforceClampBoundsAreNamedConstantsNotLiterals(t *testing.T) {
	const src = "routes_memory.go"
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, src, nil, 0)
	require.NoError(t, err, "parse %s", src)

	var body *ast.BlockStmt
	ast.Inspect(file, func(n ast.Node) bool {
		fn, ok := n.(*ast.FuncDecl)
		if ok && fn.Name.Name == "handleReinforceMemory" {
			body = fn.Body
			return false
		}
		return true
	})
	require.NotNil(t, body,
		"HANDLER_NOT_FOUND: %s no longer declares handleReinforceMemory, so this arm walked "+
			"nothing. A rename is legitimate — point this arm at the new name in the same "+
			"diff — but a walk over no function is the same green as a correct clamp.", src)

	// Every ordered comparison in the handler that touches a strength bound.
	//
	// 🔴 Scoped to comparisons that NAME a bound, rather than to every ordered
	// comparison. The first version of this arm checked all of them and reported
	// `len(memAttrsRaw) > 0` as a literal bound — a false red on a line that has
	// nothing to do with strength. Narrowing it costs one thing and buys it back
	// below: a clamp that replaced BOTH selectors with literals names no bound and
	// so is invisible to the per-comparison rule, which is exactly what the
	// MISSING_BOUND census after the loop is for.
	seen := map[string]bool{}
	bounded := 0
	ast.Inspect(body, func(n ast.Node) bool {
		bin, ok := n.(*ast.BinaryExpr)
		if !ok || (bin.Op != token.GTR && bin.Op != token.LSS &&
			bin.Op != token.GEQ && bin.Op != token.LEQ) {
			return true
		}
		names := boundSelectorsIn(bin)
		if len(names) == 0 {
			return true
		}
		bounded++
		for _, name := range names {
			seen[name] = true
		}
		// Any numeric literal ANYWHERE in a comparison against a bound, not just
		// as a bare operand: `> domain.MaxBaseStrength + 0.5` is a bound plus an
		// opinion, and it reads as compliant to a check on operands alone.
		if lit := numericLiteralIn(bin); lit != "" {
			t.Errorf("LITERAL_BOUND: handleReinforceMemory compares a strength against an "+
				"expression carrying the literal %s (%v). The card says this clamp reads "+
				"domain.MinBaseStrength/MaxBaseStrength rather than two literals that happened "+
				"to agree with them — a literal here is a fourth independent answer about one "+
				"column's range, which is the whole of aihub#411 T1-3.", lit, names)
		}
		return true
	})

	require.GreaterOrEqual(t, bounded, 2,
		"handleReinforceMemory carries only %d ordered comparison(s) naming a strength bound; "+
			"the saturation clamp is two, so either a bound was lost or this arm has stopped "+
			"seeing it", bounded)
	for _, want := range []string{"domain.MinBaseStrength", "domain.MaxBaseStrength"} {
		require.True(t, seen[want],
			"MISSING_BOUND: no ordered comparison in handleReinforceMemory names %s. Either "+
				"the clamp lost a bound or it is reading something else, and the card's "+
				"account of where the range comes from is then wrong.", want)
	}
}

// boundSelectorsIn returns every `domain.*BaseStrength` selector inside an
// expression, by the name a reader would write.
func boundSelectorsIn(expr ast.Expr) []string {
	var out []string
	ast.Inspect(expr, func(n ast.Node) bool {
		sel, ok := n.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		pkg, ok := sel.X.(*ast.Ident)
		if !ok || !strings.HasSuffix(sel.Sel.Name, "BaseStrength") {
			return true
		}
		out = append(out, pkg.Name+"."+sel.Sel.Name)
		return true
	})
	return out
}

// numericLiteralIn returns the first INT or FLOAT literal in an expression, or
// "" if there is none.
func numericLiteralIn(expr ast.Expr) string {
	found := ""
	ast.Inspect(expr, func(n ast.Node) bool {
		lit, ok := n.(*ast.BasicLit)
		if !ok || found != "" {
			return found == ""
		}
		if lit.Kind == token.INT || lit.Kind == token.FLOAT {
			found = lit.Value
			return false
		}
		return true
	})
	return found
}

// TestEverySumTheReinforceClampCanProduceIsAWholeNumber holds the closure the
// card's "the clamp and the codec can no longer disagree" rests on.
//
// The argument has three premises and the sentence states all three. Two are
// facts about declarations and are asserted here directly; the third — that a
// stored value is whole because the column is SMALLINT — is the migration's, and
// internal/domain/memory_base_strength_range_test.go
// (TestBaseStrengthBoundsAreTheColumnsOwn) is what reads it. This arm therefore
// asserts the CONCLUSION over the domain the premises describe, which is the
// only form that catches a premise silently ceasing to hold: widen
// MaxBaseStrength to 5.5 and the constants arm still passes (it compares against
// the DDL, which would have had to move too) while every sum through this clamp
// stops being whole.
//
// Exhaustive rather than sampled: the stored values are the integers in the
// range and the deltas are the integers that can move a value across it, which
// is a few dozen pairs. A sampled version would be a weaker claim for no saving.
//
//	M5  make MaxBaseStrength 5.5                                    RED
//	M6  make the clamp's upper bound `MaxBaseStrength + 0.5`        RED
//	M7  drop the integrality guard from the delta set below         RED
//	    (0.5 then reaches the arithmetic and the sum is fractional —
//	    the control that shows this arm is about the guard and not
//	    about integers being closed under addition)
func TestEverySumTheReinforceClampCanProduceIsAWholeNumber(t *testing.T) {
	// Premise: the clamp's two bounds are integers.
	for _, tc := range []struct {
		name string
		v    float64
	}{
		{"MinBaseStrength", domain.MinBaseStrength},
		{"MaxBaseStrength", domain.MaxBaseStrength},
		{"DefaultBaseStrength", domain.DefaultBaseStrength},
	} {
		require.Equal(t, math.Trunc(tc.v), tc.v,
			"domain.%s is %g, which is not a whole number. The card's closure argument names "+
				"the bounds being integers as one of its three premises, and without it a sum "+
				"pinned to a bound is fractional, reaches the SMALLINT column, and is "+
				"truncated toward zero with no error — the aihub#475 defect, back through the "+
				"one door aihub#459 was supposed to have closed.", tc.name, tc.v)
	}

	// Premise: an accepted delta is whole. The guard is domain's, so this walk
	// asks it rather than assuming a set of legal deltas.
	var deltas []float64
	span := domain.MaxBaseStrength - domain.MinBaseStrength
	for d := -(span + 2); d <= span+2; d += 0.5 {
		if domain.ValidateIntegralStrength("strength_delta", d) == nil {
			deltas = append(deltas, d)
		}
	}
	require.NotEmpty(t, deltas, "the guard accepted no delta at all, so the walk below is empty")
	require.Less(t, len(deltas), int(2*(span+2))+2,
		"the guard accepted every half-step this walk offered, so it is no longer refusing "+
			"fractional deltas and the second premise of the card's argument has gone")

	// The conclusion, over every (stored value, accepted delta) pair the handler
	// can be handed, through the same clamp the arm above just pinned.
	pairs := 0
	for stored := domain.MinBaseStrength; stored <= domain.MaxBaseStrength; stored++ {
		for _, d := range deltas {
			sum := stored + d
			if sum > domain.MaxBaseStrength {
				sum = domain.MaxBaseStrength
			}
			if sum < domain.MinBaseStrength {
				sum = domain.MinBaseStrength
			}
			pairs++
			require.Equal(t, math.Trunc(sum), sum,
				"stored=%g + delta=%g clamps to %g, which is not a whole number. pgx encodes "+
					"this float64 through its int2 codec on the way into the SMALLINT column, "+
					"truncating toward zero and returning no error, so the row would hold a value "+
					"nobody named — and the card says that path is unreachable from the API.",
				stored, d, sum)
		}
	}
	require.Greater(t, pairs, 20,
		"this walk covered only %d (stored, delta) pair(s); the range holds %g values and the "+
			"guard accepted %d deltas, so a count this low means one of the two sets collapsed",
		pairs, domain.MaxBaseStrength-domain.MinBaseStrength+1, len(deltas))
	t.Logf("every one of %d (stored, accepted delta) sums through the clamp is a whole number",
		pairs)
}
