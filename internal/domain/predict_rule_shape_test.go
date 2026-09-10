package domain

// aihub#543 probe wave 1 — the `docs/mcp-cards/pf_predict_conflicts.md`
// sentences about HOW THE SIX RULES ARE ORDERED AND WHAT EACH ONE CAN ANSWER.
//
//	"…whereas rule 1 answers \"would taking this lock collide\", it `return`s on
//	 the first hit and suppresses every rule after it…"
//	"A payload of only those two types can no longer return `hard_block`: they
//	 derive no lock, so the lock-table rule cannot fire for them."
//	"A repo overlap reports `soft_block` (rule 2 or 4) and a service overlap
//	 `info` (rule 6)…"
//	"- **Rule 6 is new** (`service`, `info`, not gated on `dry_run`)."
//
// 🔴 WHY THIS IS A SOURCE-SHAPE ARM AND NOT A DATABASE ONE. The DB arms next
// door (TestDeLockingPredictReportsAdvisoryEntries, and
// TestFileScopeRepoKey_PredictRule1NoHardBlockAcrossRepos) drive one payload at a
// time and observe the answer, so each of them sees the rules that FIRE for that
// payload. None of them can see a property of the whole ladder: that rule 1 is
// the only rule that stops the ones after it, that it is the only rule gated on
// dry_run, and that it is the only place `hard_block` is produced at all. Those
// are the three facts the card's severity-ceiling paragraph rests on, and a
// seventh rule appending a hard_block prediction would leave every existing DB
// arm green while making that paragraph false for every payload that reached it.
//
// The ladder is read out of the AST rather than matched with a regexp because
// the questions are structural — "is this literal inside that guard", "does this
// loop contain a return" — and a text scan answers them by proximity.
//
// No database:
//
//	GOWORK=off go test ./internal/domain/ -run TestOnlyTheLockTableRule -count=1

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"
)

// predictRuleSite is one `ConflictPrediction{…}` literal in PredictConflicts,
// with the three structural facts the card's paragraphs are about.
type predictRuleSite struct {
	rule       int
	severity   string // the identifier named in the Severity field
	pos        token.Pos
	inDryRun   bool // inside the `if !req.DryRun` guard
	loopReturn bool // the rule's own `for … range resources` loop contains a return
}

// predictRuleSeverities is what each rule may answer, and it is the card's
// severity table rather than a copy of the code.
//
// Rule 3 is deliberately absent: its severity is a local variable, because the
// rule answers soft_block or info depending on `intent`. It is checked against
// the one thing the card DOES promise for it — that it is not hard_block — in
// the walk below.
var predictRuleSeverities = map[int]string{
	1: "SeverityHardBlock",
	2: "SeveritySoftBlock",
	4: "SeveritySoftBlock",
	5: "SeverityInfo",
	6: "SeverityInfo",
}

// predictRuleCount is how many rules PredictConflicts runs. It is a floor AND an
// equality: a walk that found four rules would report "only rule 1 hard-blocks"
// about a ladder it had only half read, and a seventh rule nobody added to this
// number is a rule this arm never looked at.
const predictRuleCount = 6

// TestOnlyTheLockTableRuleHardBlocksAndItStopsTheRulesAfterIt is the ladder
// property the severity ceiling rests on.
//
// MUTANTS (run against this tree; the verdict is what happened):
//
//	M1  enforcement: delete `return result, nil` from rule 1's hit branch
//	                                         RED  suppression
//	M2  enforcement: wrap rule 6's loop in an `if !req.DryRun` guard of its own
//	                                         RED  dry_run_gate, naming rule 6 —
//	                                              and suppression, severity and the
//	                                              ordering arm all stay green, which
//	                                              is what says the arms are separable
//	M3  enforcement: give rule 6 SeverityHardBlock
//	                                         RED  hard_block_is_rule_1_only AND
//	                                              severity, both naming rule 6
//	M4  enforcement: drop rule 4's `Rule:` field
//	                                         RED  the floor (5 literals, want 6,
//	                                              rules [1 2 3 5 6])
//	M5  GREEN CONTROL: reword rule 2's Description string
//	                                         GREEN — this arm pins the ladder's
//	                                              shape, not its prose, and a
//	                                              control that reddened here would
//	                                              mean it was reading the wrong thing
//	M22 publication: drop this arm's citation from the card sentence
//	                                         RED  K12 DEBT_GROWTH (cited 15 -> 14,
//	                                              unclassified 0 -> 1). ⚠️ This arm
//	                                              reads the SOURCE, not the card, so
//	                                              the publication side is held by
//	                                              K12's citation binding
func TestOnlyTheLockTableRuleHardBlocksAndItStopsTheRulesAfterIt(t *testing.T) {
	fn := predictConflictsDecl(t)
	dryRunGuards := dryRunGuardBodies(t, fn)
	sites := predictRuleSites(t, fn, dryRunGuards)

	// ── The floor. Every assertion below is "only rule 1 does X", and a walk
	// that found one rule satisfies all of them for free.
	if len(sites) != predictRuleCount {
		t.Fatalf("the walk found %d ConflictPrediction literal(s) in PredictConflicts, want %d "+
			"(rules %v). Every assertion in this test has the form \"only rule 1 does X\", which a "+
			"walk that found rule 1 alone satisfies without reading anything — so this is a hard "+
			"stop rather than an error. If a rule was genuinely added or retired, move "+
			"predictRuleCount in the same change and say which rule.",
			len(sites), predictRuleCount, ruleNumbers(sites))
	}

	t.Run("the rules run in numeric order", func(t *testing.T) {
		for i, s := range sites {
			if s.rule != i+1 {
				t.Errorf("the %d%s ConflictPrediction in source order is rule %d, want rule %d "+
					"(order found: %v). The card numbers the rules the way a reader meets them, and "+
					"\"rule 1 suppresses every rule after it\" is a statement about THIS order — a "+
					"ladder renumbered without renumbering the card describes a different tool.",
					i+1, ordinalSuffix(i+1), s.rule, i+1, ruleNumbers(sites))
			}
		}
	})

	t.Run("hard_block_is_rule_1_only", func(t *testing.T) {
		for _, s := range sites {
			if s.rule == 1 {
				continue
			}
			if s.severity == "SeverityHardBlock" {
				t.Errorf("rule %d answers SeverityHardBlock. The card publishes a CEILING — a payload "+
					"of only repo and service entries can no longer return hard_block, because neither "+
					"derives a lock and rule 1 is the only rule that reads the lock table. A second "+
					"hard_block rule makes that ceiling false for every payload it fires on, and "+
					"pf-work's pre-claim gate branches on the value.", s.rule)
			}
		}
	})

	t.Run("severity", func(t *testing.T) {
		for _, s := range sites {
			want, pinned := predictRuleSeverities[s.rule]
			if !pinned {
				// Rule 3 alone: its severity is a local, because intent decides it.
				if s.severity == "SeverityHardBlock" || s.severity == "SeveritySoftBlock" || s.severity == "SeverityInfo" {
					t.Errorf("rule %d answers the constant %s, but this arm has it recorded as the one "+
						"rule whose severity depends on `intent`. Either the rule stopped reading intent "+
						"or predictRuleSeverities is out of date; both need the card changed too.",
						s.rule, s.severity)
				}
				continue
			}
			if s.severity != want {
				t.Errorf("rule %d answers %s, and the card publishes %s for it. The repo/service "+
					"paragraph names the severity per rule (\"a repo overlap reports soft_block (rule 2 "+
					"or 4) and a service overlap info (rule 6)\"), so a rule that changes severity "+
					"without that paragraph changing is a published promise the tool stopped keeping.",
					s.rule, s.severity, want)
			}
		}
	})

	t.Run("suppression", func(t *testing.T) {
		for _, s := range sites {
			switch {
			case s.rule == 1 && !s.loopReturn:
				t.Errorf("rule 1's loop contains no return. The card says it \"returns on the first hit " +
					"and suppresses every rule after it\", and that suppression is why the two halves of " +
					"the self-report are never both visible — without it a caller sees rule 1 AND rule 3 " +
					"for one path, which is the two-rules-one-input contradiction aihub#342 removed.")
			case s.rule != 1 && s.loopReturn:
				t.Errorf("rule %d's loop returns early as well. Only rule 1 may: every other rule "+
					"reports an advisory overlap, and a rule that stops the ladder hides every rule "+
					"after it from a caller the card promises will see them.", s.rule)
			}
		}
	})

	t.Run("dry_run_gate", func(t *testing.T) {
		for _, s := range sites {
			switch {
			case s.rule == 1 && !s.inDryRun:
				t.Error("rule 1 is not inside the `if !req.DryRun` guard. dry_run means \"do not consult " +
					"the lock table\", and rule 1 is the only rule that consults it — ungated, a dry " +
					"run starts reporting hard_block for a call whose whole point is that it takes " +
					"nothing.")
			case s.rule != 1 && s.inDryRun:
				t.Errorf("rule %d is inside the `if !req.DryRun` guard. The card says rule 6 in "+
					"particular is \"not gated on dry_run\", and the reason generalises: a rule that "+
					"reads declarations has nothing to be advisory about, so gating it makes a dry run "+
					"answer with less than it knows.", s.rule)
			}
		}
	})
}

// predictConflictsDecl parses conflicts.go and returns PredictConflicts.
func predictConflictsDecl(t *testing.T) *ast.FuncDecl {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "conflicts.go", nil, parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("parse conflicts.go: %v — this arm is structural, so it has to be re-pointed when "+
			"the function moves rather than deleted", err)
	}
	for _, decl := range f.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if ok && fn.Recv == nil && fn.Name.Name == "PredictConflicts" {
			return fn
		}
	}
	t.Fatal("conflicts.go declares no PredictConflicts — the walk found nothing to read, and every " +
		"assertion about \"only rule 1\" would be vacuously true")
	return nil
}

// dryRunGuardBodies returns the body of EVERY `if !…DryRun` guard in the
// function. All of them, rather than the first: the per-rule assertion is "rule 1
// is gated and nothing else is", and reading one guard would answer that question
// about a second guard by not looking at it — a rule gated inside guard two would
// read as ungated, which is the wrong answer in the quiet direction.
func dryRunGuardBodies(t *testing.T, fn *ast.FuncDecl) []*ast.BlockStmt {
	t.Helper()
	var found []*ast.BlockStmt
	ast.Inspect(fn, func(n ast.Node) bool {
		ifStmt, ok := n.(*ast.IfStmt)
		if !ok {
			return true
		}
		unary, ok := ifStmt.Cond.(*ast.UnaryExpr)
		if !ok || unary.Op != token.NOT {
			return true
		}
		sel, ok := unary.X.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != "DryRun" {
			return true
		}
		found = append(found, ifStmt.Body)
		return true
	})
	if len(found) == 0 {
		t.Fatal("PredictConflicts has no `if !req.DryRun` guard at all. dry_run is a published " +
			"parameter whose whole promise is that the lock table is not consulted; with no guard " +
			"the promise is not kept anywhere, and the per-rule assertions below would report the " +
			"opposite of the truth")
	}
	return found
}

// predictRuleSites collects every ConflictPrediction literal in source order,
// with whether it sits inside the dry-run guard and whether its own rule loop
// returns early.
func predictRuleSites(t *testing.T, fn *ast.FuncDecl, dryRunGuards []*ast.BlockStmt) []predictRuleSite {
	t.Helper()
	var loops []*ast.RangeStmt
	ast.Inspect(fn, func(n ast.Node) bool {
		if rs, ok := n.(*ast.RangeStmt); ok {
			loops = append(loops, rs)
		}
		return true
	})

	var sites []predictRuleSite
	ast.Inspect(fn, func(n ast.Node) bool {
		lit, ok := n.(*ast.CompositeLit)
		if !ok {
			return true
		}
		ident, ok := lit.Type.(*ast.Ident)
		if !ok || ident.Name != "ConflictPrediction" {
			return true
		}
		site := predictRuleSite{pos: lit.Pos(), rule: -1}
		for _, elt := range lit.Elts {
			kv, ok := elt.(*ast.KeyValueExpr)
			if !ok {
				continue
			}
			key, ok := kv.Key.(*ast.Ident)
			if !ok {
				continue
			}
			switch key.Name {
			case "Rule":
				site.rule = intLiteral(t, kv.Value)
			case "Severity":
				site.severity = identName(kv.Value)
			}
		}
		if site.rule < 0 {
			t.Errorf("a ConflictPrediction literal in PredictConflicts sets no Rule field. The rule "+
				"number is what a caller keys on and what this arm groups by, so a prediction without "+
				"one is invisible to both (position %v)", site.pos)
			return true
		}
		for _, guard := range dryRunGuards {
			if guard.Pos() < site.pos && site.pos < guard.End() {
				site.inDryRun = true
				break
			}
		}
		site.loopReturn = innermostLoopReturns(loops, site.pos)
		sites = append(sites, site)
		return true
	})
	return sites
}

// innermostLoopReturns reports whether the smallest `for … range` loop
// containing pos has a return in it.
func innermostLoopReturns(loops []*ast.RangeStmt, pos token.Pos) bool {
	var best *ast.RangeStmt
	for _, l := range loops {
		if l.Pos() >= pos || pos >= l.End() {
			continue
		}
		if best == nil || l.Pos() > best.Pos() {
			best = l
		}
	}
	if best == nil {
		return false
	}
	returns := false
	ast.Inspect(best.Body, func(n ast.Node) bool {
		if _, ok := n.(*ast.ReturnStmt); ok {
			returns = true
		}
		return !returns
	})
	return returns
}

func intLiteral(t *testing.T, e ast.Expr) int {
	t.Helper()
	lit, ok := e.(*ast.BasicLit)
	if !ok || lit.Kind != token.INT {
		return -1
	}
	n := 0
	for _, r := range lit.Value {
		if r < '0' || r > '9' {
			return -1
		}
		n = n*10 + int(r-'0')
	}
	return n
}

func identName(e ast.Expr) string {
	switch v := e.(type) {
	case *ast.Ident:
		return v.Name
	case *ast.SelectorExpr:
		return v.Sel.Name
	}
	return ""
}

func ruleNumbers(sites []predictRuleSite) []int {
	out := make([]int, 0, len(sites))
	for _, s := range sites {
		out = append(out, s.rule)
	}
	return out
}

func ordinalSuffix(n int) string {
	switch {
	case n%100 >= 11 && n%100 <= 13:
		return "th"
	case n%10 == 1:
		return "st"
	case n%10 == 2:
		return "nd"
	case n%10 == 3:
		return "rd"
	}
	return "th"
}
