package domain

// aihub#543 wave 2, slice L6 — the two hop-4 sentences of the pf_activate_memory
// card, at the layer that decides them.
//
// The card says activation "increments the activation count and updates the
// stability term of the forgetting curve, which is what `effective_strength` is
// computed from at recall time", and then draws the conclusion that makes the
// tool worth calling: "activation is not bookkeeping: it changes which memories
// a later `pf_recall` returns above `min_strength`".
//
// That second sentence is the one worth a probe, and it is a claim about a
// COMPOSITION — three independent facts have to hold for it to be true:
//
//	1. the stability term rises with the activation count      ComputeStabilityDays
//	2. effective strength rises with the stability term        MemoryStrength
//	3. recall thresholds on that same expression, in SQL       the Recall WHERE
//
// Break any one and the sentence is false while the other two stay green, which
// is why they are asserted separately rather than through one end-to-end
// fixture. A single DB test that happened to return one more row would go green
// as soon as any of the three was right.
//
// No database: (1) and (2) are pure functions and (3) is read off the package's
// own SQL literals, the technique TestRecallSQL_HasNoTierOrderingOrStaleReferenceTime
// established here for exactly this reason — the DB-backed ranking assertions
// SKIP everywhere except their own CI step, so a regression in a claim this
// central must fail the ordinary `go test ./...`.

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// activationStrengthTypes are the memory-type prefixes baseStabilityForType
// answers differently, plus one it does not recognise. The unrecognised one is
// deliberate: the claim is about EVERY memory, and a default branch that stopped
// scaling with the activation count would leave the card's sentence false for
// every type nobody thought to list.
var activationStrengthTypes = []string{
	"experience.debug", "fact.note", "rule.work", "methodology.spec", "something.else",
}

// TestActivationRaisesTheStabilityTermTheStrengthFormulaReads holds facts 1 and
// 2 of the composition above.
//
//	M1  ComputeStabilityDays: drop the `activation_count × 0.5` term so it
//	    returns the base stability regardless of the count        RED
//	M2  ComputeStabilityDays: make the multiplier 0 for the default
//	    branch only                                               RED
//	    (the unrecognised type is why — a per-type list would miss it)
//	M3  MemoryStrength: divide by a constant instead of by
//	    stabilityDays                                             RED
//	M4  MemoryStrength: return baseStrength unchanged             RED
//	M5  narrow activationStrengthTypes to one entry               RED, and it is
//	                                                              the FLOOR below
//	                                                              that catches it
//	                                                              rather than any
//	                                                              assertion — a
//	                                                              one-entry walk
//	                                                              satisfies every
//	                                                              one of them
func TestActivationRaisesTheStabilityTermTheStrengthFormulaReads(t *testing.T) {
	// The floor. A walk over an empty or single-entry type list satisfies every
	// assertion below and measures almost nothing.
	require.GreaterOrEqual(t, len(activationStrengthTypes), 4,
		"this arm walks the memory-type prefixes baseStabilityForType distinguishes; with "+
			"fewer than four it has stopped covering them and every assertion below would "+
			"still pass")

	distinct := map[float64]bool{}
	for _, memType := range activationStrengthTypes {
		distinct[baseStabilityForType(memType)] = true

		// Fact 1: each activation raises the stability term, at every count.
		prev := ComputeStabilityDays(memType, 0)
		require.Greater(t, prev, 0.0,
			"%s starts at a non-positive stability, and MemoryStrength returns 0 for that — "+
				"the whole curve would be dead before the first activation", memType)
		for n := 1; n <= 5; n++ {
			cur := ComputeStabilityDays(memType, n)
			require.Greater(t, cur, prev,
				"ComputeStabilityDays(%s, %d)=%g is not above the value at %d (%g). The card "+
					"says activation UPDATES the stability term; a term that does not move makes "+
					"the tool bookkeeping, which is exactly what the card denies", memType, n, cur, n-1, prev)
			prev = cur
		}
	}
	require.Greater(t, len(distinct), 1,
		"baseStabilityForType answered the same value for every type this arm walks, so the "+
			"list has stopped distinguishing the branches it exists to cover")

	// Fact 2: effective strength is increasing in the stability term, at a fixed
	// age and base strength. Asserted over the legal base strengths rather than
	// one, because the multiplication could be hiding a sign only some of them
	// expose.
	created := time.Now().Add(-30 * 24 * time.Hour)
	for _, bs := range []float64{MinBaseStrength, DefaultBaseStrength, MaxBaseStrength} {
		prev := MemoryStrength(bs, ComputeStabilityDays("experience.debug", 0), nil, created)
		for n := 1; n <= 5; n++ {
			cur := MemoryStrength(bs, ComputeStabilityDays("experience.debug", n), nil, created)
			require.Greater(t, cur, prev,
				"at base_strength=%g and 30 days of age, activation %d did not raise the "+
					"effective strength (%g -> %g). The card's `effective_strength` is computed "+
					"from the stability term this walk just showed rising, so a flat strength "+
					"means the two are no longer connected", bs, n, prev, cur)
			prev = cur
		}
	}
}

// TestActivationCanCarryAMemoryBackOverTheRecallThreshold is the card's
// conclusion, stated as the observation that would be missing if it were false:
// a memory whose effective strength is BELOW a threshold before activation and
// AT OR ABOVE it after.
//
// Both sides are required. "Activation raises the strength" is a fact about a
// function; "it changes which memories recall returns" is a fact about a
// COMPARISON, and a raise too small to ever cross a threshold would satisfy the
// first while making the card's sentence rhetoric. The threshold used is the one
// the recall parameter's own default sits at, so the case is not invented to
// order.
//
//	M6  ComputeStabilityDays: halve the multiplier so the rise never crosses
//	    the threshold at this age                                RED
//	M7  raise the fixture's age past the point where any number of
//	    activations helps                                        RED, and that
//	                                                             is the point:
//	                                                             the arm is
//	                                                             about a
//	                                                             reachable
//	                                                             crossing, not
//	                                                             about all of
//	                                                             them
func TestActivationCanCarryAMemoryBackOverTheRecallThreshold(t *testing.T) {
	// A month-old experience memory at the default strength. experience.* has the
	// shortest base stability (7 days), which is what makes this the class where
	// activation matters most — and the class the Memory-First recall step
	// activates.
	const threshold = 0.3
	created := time.Now().Add(-21 * 24 * time.Hour)

	before := MemoryStrength(DefaultBaseStrength, ComputeStabilityDays("experience.debug", 0), nil, created)
	require.Less(t, before, threshold,
		"the fixture no longer starts BELOW the threshold (%g), so the crossing this arm is "+
			"about cannot happen and it would pass without measuring anything", threshold)

	crossedAt := -1
	for n := 1; n <= 10; n++ {
		if MemoryStrength(DefaultBaseStrength, ComputeStabilityDays("experience.debug", n), nil, created) >= threshold {
			crossedAt = n
			break
		}
	}
	require.NotEqual(t, -1, crossedAt,
		"no number of activations up to 10 carries a %d-day-old experience.* memory at the "+
			"default base strength back over %g. The card says activation \"changes which "+
			"memories a later pf_recall returns above min_strength\" — if no reachable "+
			"activation count crosses a threshold, that sentence is describing an effect too "+
			"small to have one.", 21, threshold)
	t.Logf("a 21-day-old experience.* memory at base_strength=%g crosses %g after %d "+
		"activation(s)", DefaultBaseStrength, threshold, crossedAt)
}

// TestRecallThresholdsOnTheStabilityTermActivationMoves is fact 3: the SQL that
// decides which rows come back divides the age by `stability_days` and compares
// the result against the min_strength parameter.
//
// Without it the two arms above are facts about Go functions that recall might
// not use. The predicate is built in this package as a string, so it is read the
// way TestRecallSQL_HasNoTierOrderingOrStaleReferenceTime reads its forbidden
// spellings: off the AST, whitespace-collapsed and lowercased, so a reflow or an
// indentation change cannot turn the check off.
//
// 🔴 Concatenation chains are FLATTENED, and that is not a detail. The predicate
// is written as `"… - " + memRefTimeSQL + " … / stability_days) >= $%d"`, so no
// single string literal carries the whole expression. The first version of this
// arm inspected BasicLits one at a time and found nothing — a false red that
// would have been a false GREEN the moment somebody "fixed" it by loosening the
// pattern to whatever one fragment happened to contain.
//
// It is a check that the predicate EXISTS in the form the card's argument needs,
// not that the SQL is any particular text. A rewrite that keeps dividing by
// stability_days and comparing against a bound parameter is fine; one that drops
// stability_days is the regression, because then activation moves a column
// recall does not read and the card's conclusion is false while every Go-level
// arm above stays green.
//
//	M8   delete `/ stability_days` from the min_strength predicate    RED
//	M9   compare against a literal instead of a bound parameter       RED
//	M10  stop flattening concatenation chains                         RED
//	                                                                  (measured:
//	                                                                  this is how
//	                                                                  the arm was
//	                                                                  first
//	                                                                  written)
//
// The two floors — the parsed-file count and the string-expression count — are
// the broken-reader half, and they are stated rather than mutated: pointing the
// walk at an empty directory is not something a diff to this repo can do, so the
// honest form is a floor whose failure text says which of the two answers
// "nothing found" means.
func TestRecallThresholdsOnTheStabilityTermActivationMoves(t *testing.T) {
	sources, err := filepath.Glob("*.go")
	require.NoError(t, err)

	normalise := func(s string) string {
		return strings.Join(strings.Fields(strings.ToLower(s)), " ")
	}

	found := ""
	checked := 0
	chains := 0
	for _, src := range sources {
		if strings.HasSuffix(src, "_test.go") {
			continue
		}
		fset := token.NewFileSet()
		file, perr := parser.ParseFile(fset, src, nil, 0)
		require.NoError(t, perr, "parse %s", src)
		checked++

		ast.Inspect(file, func(n ast.Node) bool {
			expr, ok := n.(ast.Expr)
			if !ok {
				return true
			}
			text, isString := flattenStringConcat(expr)
			if !isString {
				return true
			}
			chains++
			norm := normalise(text)
			// The three parts the card's argument rests on: the decay exponent, the
			// division by the column activation writes, and a comparison against a
			// bound parameter rather than a constant.
			if strings.Contains(norm, "base_strength * exp(") &&
				strings.Contains(norm, "/ stability_days") &&
				strings.Contains(norm, ">= $") {
				found = src
			}
			return true
		})
	}

	require.Greater(t, checked, 5,
		"this walk parsed only %d non-test file(s) in the package; a walk that found nothing "+
			"reports no predicate for the same reason a broken glob would, and the two must "+
			"not look alike", checked)
	require.Greater(t, chains, 100,
		"this walk saw only %d string expression(s) in the package, which is far below what "+
			"internal/domain holds — the flattener has stopped recognising them and the "+
			"assertion below would report a missing predicate rather than a broken reader", chains)
	require.NotEmpty(t, found,
		"no SQL in this package thresholds `base_strength * exp(... / stability_days)` "+
			"against a bound parameter. Either the min_strength filter moved out of SQL — in "+
			"which case point this arm at where it went — or it stopped reading "+
			"stability_days, and then pf_activate_memory moves a column recall does not "+
			"consult and the card's \"it changes which memories a later pf_recall returns\" "+
			"is false.")
	t.Logf("the min_strength predicate that reads stability_days lives in %s", found)
}

// flattenStringConcat renders a string expression, following `+` chains through
// identifiers so a query assembled from a literal, a named SQL fragment and
// another literal reads as one string.
//
// An identifier contributes its NAME rather than its value: this arm's patterns
// are about the literal SQL around it, and resolving a const would need a type
// checker for no gain. A non-string operand contributes a placeholder, which
// keeps the two literals it sits between from becoming falsely adjacent.
func flattenStringConcat(expr ast.Expr) (string, bool) {
	switch e := expr.(type) {
	case *ast.BasicLit:
		if e.Kind != token.STRING {
			return "", false
		}
		text, err := strconv.Unquote(e.Value)
		if err != nil {
			// A raw string with an escape strconv cannot unquote still carries the
			// SQL; strip the backticks rather than dropping the whole chain.
			return strings.Trim(e.Value, "`"), true
		}
		return text, true
	case *ast.BinaryExpr:
		if e.Op != token.ADD {
			return "", false
		}
		left, lok := flattenStringConcat(e.X)
		right, rok := flattenStringConcat(e.Y)
		if !lok && !rok {
			return "", false
		}
		if !lok {
			left = " \ufffd "
		}
		if !rok {
			right = " \ufffd "
		}
		return left + right, true
	case *ast.Ident:
		return " " + e.Name + " ", true
	case *ast.ParenExpr:
		return flattenStringConcat(e.X)
	}
	return "", false
}
