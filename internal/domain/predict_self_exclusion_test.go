package domain

// aihub#543 probe wave 1 — the `docs/mcp-cards/pf_predict_conflicts.md` sentence
// about WHERE the caller-exclusion is bound.
//
//	"The exclusion is bound as a parameter inside the two shared containment
//	 fragments (`internal/domain/conflicts.go` (`notCallersOwnWISQL`)) rather than
//	 at the four call sites, so a fifth rule written with them inherits it."
//
// 🔴 THE INHERITANCE HALF IS THE PART NO BEHAVIOURAL ARM CAN REACH.
// TestDeLockingPredictReportsAdvisoryEntries drives rules 2, 4, 5 and 6 and
// checks that each leaves the caller out, so it holds the four rules that exist.
// The card promises something about the rule that does not exist yet: that a
// fifth one written with these fragments cannot forget. That is a property of
// where the predicate LIVES, and the only way to observe it is to read the
// declaration — a rule bolted onto the four call sites behaves identically today
// and differently on the day somebody adds the fifth, which is exactly the day
// no test would be watching.
//
// No database:
//
//	GOWORK=off go test ./internal/domain/ -run TestPredictSelfExclusion -count=1

import (
	"go/ast"
	"strings"
	"testing"
)

// predictContainmentRules is how many declaration rules ask the shared
// containment question today: 2 (repo), 4 (repo refactor), 5 (external_ref) and
// 6 (service).
const predictContainmentRules = 4

// TestPredictSelfExclusionIsBoundInsideTheSharedContainmentFragments pins the
// inheritance property.
//
// MUTANTS (run against this tree; the verdict is what happened):
//
//	M6  enforcement: replace notCallersOwnWISQL in declaresContainmentSQL with a
//	    tautology                            RED  fragments_open_with_the_exclusion
//	M7  enforcement: give rule 6 its own inline `wi.id <> $1 AND
//	    declared_resources @>` literal instead of the shared fragment
//	                                         RED  every_declaration_rule_uses_a_shared_fragment
//	                                              (3 uses, want 4) AND
//	                                              no_rule_inlines_its_own_containment.
//	                                              ⚠️ The behaviour is UNCHANGED under
//	                                              this mutant — the inline copy
//	                                              excludes the caller too — which is
//	                                              exactly why no behavioural arm can
//	                                              see it and this one can
//	M25 publication: replace this arm's name in the card with a Test symbol the
//	    tree does not declare                RED  K12 ARM_CITATION_UNRESOLVED. ⚠️
//	                                              This arm reads the SOURCE, not the
//	                                              card, so the publication side is
//	                                              held by K12's citation binding
func TestPredictSelfExclusionIsBoundInsideTheSharedContainmentFragments(t *testing.T) {
	// The floor, and it runs first: every assertion below is about a substring
	// of these three constants, and an empty one is contained by everything.
	if len(notCallersOwnWISQL) < len("wi.id <> $1") {
		t.Fatalf("notCallersOwnWISQL is %q — too short to be a predicate. A blank or truncated "+
			"constant is a prefix of nothing and a substring of everything, so the checks below "+
			"would answer about the constant rather than about the query.", notCallersOwnWISQL)
	}

	t.Run("fragments_open_with_the_exclusion", func(t *testing.T) {
		for name, frag := range map[string]string{
			"declaresContainmentSQL":       declaresContainmentSQL,
			"declaresIntentContainmentSQL": declaresIntentContainmentSQL,
		} {
			if !strings.HasPrefix(frag, notCallersOwnWISQL) {
				t.Errorf("%s does not OPEN with notCallersOwnWISQL (%q):\n    %s\nThe card promises a "+
					"fifth rule written with these fragments inherits the exclusion. It inherits "+
					"whatever the fragment carries, so an exclusion that has moved out of the fragment "+
					"is one the fifth rule is free to forget — and forgetting it means reporting the "+
					"caller its own declaration back as somebody else's conflict.",
					name, notCallersOwnWISQL, frag)
			}
		}
	})

	fn := predictConflictsDecl(t)

	t.Run("every_declaration_rule_uses_a_shared_fragment", func(t *testing.T) {
		uses := 0
		ast.Inspect(fn, func(n ast.Node) bool {
			ident, ok := n.(*ast.Ident)
			if !ok {
				return true
			}
			if ident.Name == "declaresContainmentSQL" || ident.Name == "declaresIntentContainmentSQL" {
				uses++
			}
			return true
		})
		if uses != predictContainmentRules {
			t.Errorf("PredictConflicts names the shared containment fragments %d time(s), want %d — "+
				"one per declaration rule (2 repo, 4 repo refactor, 5 external_ref, 6 service). Fewer "+
				"means a rule asks the same question its own way and does not inherit the exclusion; "+
				"more means this count is stale and the new rule was never checked against the card.",
				uses, predictContainmentRules)
		}
	})

	t.Run("no_rule_inlines_its_own_containment", func(t *testing.T) {
		var inlined []string
		ast.Inspect(fn, func(n ast.Node) bool {
			lit, ok := n.(*ast.BasicLit)
			if !ok {
				return true
			}
			for _, marker := range []string{"declared_resources @>", "wi.id <>"} {
				if strings.Contains(lit.Value, marker) {
					inlined = append(inlined, marker)
				}
			}
			return true
		})
		if len(inlined) > 0 {
			t.Errorf("PredictConflicts builds %v inside its own query strings. Both belong to the two "+
				"shared fragments: a containment test written at the call site is a rule that asks the "+
				"declaration question without inheriting the exclusion, and an exclusion written at the "+
				"call site is one the NEXT rule copies or does not. The card names the fragments as the "+
				"place this lives, and that is the whole reason a fifth rule cannot forget.", inlined)
		}
	})
}
