package mcp_test

// aihub#493 — the floors of the labelled contract gates are checked; the prose
// beside them was not.
//
// ─── The class, and why three sweeps in one day did not close it ────────────
//
// Every floor and ceiling in contract_cards_gate_test.go and
// universal_contract_gate_test.go used to restate the value it was set from:
//
//	// ... Measured 2026-09-08: 240 across 38 files.
//	floorCardAnchors = 60
//
// The CONSTANT is checked on every run — the arm compares its own count against
// it. The SENTENCE is checked by nothing. So the two halves of that pair have
// completely different lifetimes: the constant cannot rot because a test reads
// it, and the sentence cannot help rotting because nothing does.
//
// It is not a theoretical gap; it is a measured rate. aihub#446 re-derived the
// block and found two values stale against the very tree they had been written
// on. aihub#483 confirmed five more. aihub#493 re-ran every arm one day later,
// after 21 unrelated PRs, and found ELEVEN of the nineteen restated values wrong
// — seven of nine in the card block, four of seven in the universal block. The
// same day had already run two rot sweeps.
//
// 🔴 Every one of those sentences carried a date. So "carry a date" is refuted
// as the fix by the data: it is what the stale ones already did. Dating tells a
// reader when a number was true, which is exactly the information a reader of a
// FLOOR does not need — they need what it is now, and an arm one command away
// prints that.
//
// ─── What this gate demands ────────────────────────────────────────────────
//
//	NO_VALUE     a floor/ceiling comment may not restate a measured value
//	NAMES_ARM    it must name the arm whose printed line carries the value
//	NO_PRINT_ARM the pointed-at line must exist: G2's log line must carry the
//	             outbound-request count floorToolsOnWire is set from (aihub#542)
//	NO_RECIPE    the file must carry a runnable re-derivation command
//	FLOOR_CONSTS the walk must have found constants to check
//	UNGATED_FILE a labelled gate file must be in gatedFloorFiles
//
// NAMES_ARM is what keeps NO_VALUE from being lossy. Deleting a number and
// leaving nothing behind would make the block cheaper to write and useless to
// read; the pair is "no value, but always a pointer to where the value is".
// And NO_PRINT_ARM is what keeps NAMES_ARM honest: floorToolsOnWire's comment
// satisfied the pointer check while the line it pointed at printed the
// distinct-path count rather than the floored quantity, so the only way to
// read the floor's current value was to raise it to an absurd number and read
// the failure — an exemption recorded in prose that no arm here refused, until
// aihub#542 put the count into G2's log line and added the arm that keeps it
// there.
//
// ─── Scope: the arms a developer can re-run with one command ───────────────
//
// K10's floors (card_response_keys_live_e2e_db_test.go) are deliberately OUT.
// That arm needs a migrated Postgres, so its printed value costs a database
// rather than a second, and a value recorded beside it has standalone worth —
// see floorLiveKeyChecks, whose comment records a ±1 fixture drift across two
// databases that no single run shows. The line this gate draws is therefore
// "the value is pure duplication of something one command prints", not "numbers
// in comments are bad". Elsewhere in the repo — audit documents, the ci.yml
// ledger, incident records — a dated measurement IS the record and stays.
//
// UNGATED_FILE is what stops that carve-out from quietly growing: a new
// always-on labelled gate file has to be added to gatedFloorFiles, in a diff.
//
// No database needed:
//
//	GOWORK=off go test ./internal/mcp/ -run TestMeasuredFloor -count=1 -v

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// gatedFloorFiles are the always-on labelled contract-gate files whose floors
// this gate governs, each with the arm-label family it prints.
var gatedFloorFiles = map[string]string{
	"contract_cards_gate_test.go":     "K",
	"universal_contract_gate_test.go": "G",
}

// dbGatedFloorFilesOutOfScope are the labelled gate files left out, with the
// reason. Listed rather than derived from the `_db_test.go` suffix alone so that
// dropping one out of scope is a written decision: the suffix is a naming
// convention (dbtestcov reads it), and a convention is not an argument.
var dbGatedFloorFilesOutOfScope = map[string]string{
	"card_response_keys_live_e2e_db_test.go":     "K10 — needs a migrated Postgres, so its printed value is not one command away",
	"card_response_keys_live_git_e2e_db_test.go": "K10's git half — same database, and it declares no floors of its own",
}

// floorConstName matches the constants this gate governs: a floor on a
// measurement or a ceiling on debt. Both are set FROM a measurement, which is
// what makes restating it next door redundant.
var floorConstName = regexp.MustCompile(`^(floor|max|ceiling)[A-Z]`)

// measuredValueInComment matches a restated measured value: the word "measured"
// (either case), then up to 60 non-colon characters — room for a date, a work
// item, a qualifier — then a colon and a number.
//
// 🔴 Deliberately NOT "a digit anywhere in the comment". Those blocks carry
// digits that are legitimately historical and must stay: "floorCardParams
// claimed 232 where K3 printed 231", "the 50-tool era (237 params, 46 of 50 on
// the wire)". A gate that reported those would be red on a correct tree, and the
// cheapest way to make a false-positive gate green is to delete it — so a false
// positive here is not a lesser failure than a miss, it is a faster one.
// TestMeasuredFloorPatternDiscriminates pins both directions.
var measuredValueInComment = regexp.MustCompile(`(?i)measured[^:]{0,60}:\s+\d`)

// armLabelInComment matches a pointer to the arm that prints the value: K3,
// K4/K5, G1. The label families are the ones gatedFloorFiles records.
var armLabelInComment = regexp.MustCompile(`\b[KG]\d+\b`)

// rederivationRecipe matches a runnable command. `go test` is the only shape any
// of these arms is driven by; the basename walk in the card gate also offers a
// `git ls-files` recipe, so both count.
var rederivationRecipe = regexp.MustCompile(`(go test|git ls-files)`)

// floorConstFloor guards the walk itself. A parse that found no constants would
// satisfy every arm below by having nothing to quantify over — the exact shape
// of vacuous pass the constants it is inspecting exist to refuse. Deliberately
// well under the count the walk finds, like its siblings, so it fails on a
// broken walk rather than on someone adding or retiring one floor. Printed by
// this test, not restated here.
const floorConstFloor = 10

// floorComment is one governed constant plus the comment text attached to it.
type floorComment struct {
	file  string
	name  string
	doc   string // the constant's own doc + trailing comment, lines joined
	block string // its const block's doc, lines joined
}

// commentText joins a comment group into one line, so a value split across two
// `//` lines ("Measured\n// 2026-09-08: 222") is one string to match against.
// Two of the eleven stale values found by aihub#493 were split that way, so a
// per-line match would have missed them.
func commentText(g *ast.CommentGroup) string {
	if g == nil {
		return ""
	}
	var parts []string
	for _, c := range g.List {
		parts = append(parts, strings.TrimPrefix(strings.TrimPrefix(c.Text, "//"), "/*"))
	}
	return strings.Join(strings.Fields(strings.Join(parts, " ")), " ")
}

// collectFloorComments parses one file and returns its top-level governed
// constants. Only file-scope declarations: a `const max = 160` inside a test
// function is a local bound on that function, not a gate floor.
func collectFloorComments(t *testing.T, dir, base string) ([]floorComment, string) {
	t.Helper()
	path := filepath.Join(dir, base)
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, nil, parser.ParseComments)
	if err != nil {
		t.Fatalf("parse %s: %v", base, err)
	}

	var out []floorComment
	for _, d := range f.Decls {
		gd, ok := d.(*ast.GenDecl)
		if !ok || gd.Tok != token.CONST {
			continue
		}
		blockDoc := commentText(gd.Doc)
		for _, spec := range gd.Specs {
			vs, ok := spec.(*ast.ValueSpec)
			if !ok {
				continue
			}
			for _, id := range vs.Names {
				if !floorConstName.MatchString(id.Name) {
					continue
				}
				out = append(out, floorComment{
					file:  base,
					name:  id.Name,
					doc:   strings.TrimSpace(commentText(vs.Doc) + " " + commentText(vs.Comment)),
					block: blockDoc,
				})
			}
		}
	}

	// The file's own leading comment, which is where a per-file recipe belongs.
	var header string
	for _, cg := range f.Comments {
		if cg.Pos() < f.Name.End() || (len(f.Decls) > 0 && cg.Pos() < f.Decls[0].Pos()) {
			header += " " + commentText(cg)
		}
	}
	return out, header
}

func TestMeasuredFloorCommentsCarryNoValue(t *testing.T) {
	dir := "." // the package directory; `go test` runs here

	checked := 0
	for base, family := range gatedFloorFiles {
		consts, header := collectFloorComments(t, dir, base)
		if len(consts) == 0 {
			t.Errorf("MEASURED_FLOOR NO_CONSTS: %s is listed in gatedFloorFiles and declares no "+
				"floor or ceiling constant at file scope. Either its floors moved and this list is "+
				"stale, or the walk broke — and a walk that finds nothing agrees with every arm "+
				"below by having nothing to check.", base)
			continue
		}

		if !rederivationRecipe.MatchString(header) {
			t.Errorf("MEASURED_FLOOR NO_RECIPE: %s carries floor constants and its file comment "+
				"states no runnable re-derivation command. Removing the values from the comments "+
				"only helps if the reader is told how to get them; without a recipe this gate "+
				"would just be deleting information.", base)
		}

		for _, c := range consts {
			checked++
			text := c.doc + " " + c.block

			if m := measuredValueInComment.FindString(c.doc); m != "" {
				t.Errorf("MEASURED_FLOOR NO_VALUE: %s's comment on %s restates a measured value "+
					"(%q). The constant is checked on every run and the sentence is checked by "+
					"nothing, so the two rot apart — aihub#493 found 11 of 19 such sentences wrong "+
					"one day after they were re-derived, every one of them dated. Name the arm's "+
					"printed line instead of copying its number; the recipe is in this file's "+
					"header comment.", c.file, c.name, strings.TrimSpace(m))
			}

			if !armLabelInComment.MatchString(text) {
				t.Errorf("MEASURED_FLOOR NAMES_ARM: %s's comment on %s names no arm label "+
					"(expected a %s<n> token, in the constant's own comment or its const block's). "+
					"This is the half that keeps NO_VALUE from being lossy: a floor with neither "+
					"its value nor a pointer to the line that prints it cannot be checked by a "+
					"reader at all.", c.file, c.name, family)
			}
		}
	}

	for base, reason := range dbGatedFloorFilesOutOfScope {
		if _, dup := gatedFloorFiles[base]; dup {
			t.Errorf("MEASURED_FLOOR SCOPE_CONTRADICTION: %s is in gatedFloorFiles AND in "+
				"dbGatedFloorFilesOutOfScope (%s). One of the two lists is wrong, and while they "+
				"disagree the reason a file is exempt is not readable from either.", base, reason)
		}
		if _, err := os.Stat(filepath.Join(dir, base)); err != nil {
			t.Errorf("MEASURED_FLOOR EXEMPT_GONE: dbGatedFloorFilesOutOfScope names %s (%s) and "+
				"the file does not exist. A carve-out for a file nobody can find is a carve-out "+
				"nobody will notice widening.", base, reason)
		}
	}

	if checked < floorConstFloor {
		t.Errorf("MEASURED_FLOOR FLOOR_CONSTS: only %d governed constant(s) were inspected, floor "+
			"is %d. Every arm above is per-constant, so a walk that found almost nothing would "+
			"report green — the shape of pass this floor refuses.", checked, floorConstFloor)
	}
	t.Logf("measured-floor comments: %d governed constant(s) checked across %d file(s), %d file(s) "+
		"exempt", checked, len(gatedFloorFiles), len(dbGatedFloorFilesOutOfScope))
}

// TestMeasuredFloorGateCoversEveryLabelledGate is the wiring half: gatedFloorFiles
// is a hand-written list, and a list is only as good as the assertion that it is
// complete. Any *_test.go in this package that prints an arm label must be in one
// of the two lists — so adding a K12 gate file, or a second universal gate, costs
// an edit here rather than silently landing outside this gate.
func TestMeasuredFloorGateCoversEveryLabelledGate(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read package dir: %v", err)
	}

	found := 0
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), "_test.go") {
			continue
		}
		if !declaresArmLabel(t, e.Name()) {
			continue
		}
		found++
		_, gated := gatedFloorFiles[e.Name()]
		_, exempt := dbGatedFloorFilesOutOfScope[e.Name()]
		if !gated && !exempt {
			t.Errorf("MEASURED_FLOOR UNGATED_FILE: %s declares an arm label and is in neither "+
				"gatedFloorFiles nor dbGatedFloorFilesOutOfScope. Add it to one of them: a "+
				"labelled gate whose floors nothing governs is where the next dated-and-wrong "+
				"`Measured: N` will land, and it will land silently.", e.Name())
		}
	}

	// The detector has to be able to find the files that exist, or "every labelled
	// gate is covered" is a statement about the empty set.
	if want := len(gatedFloorFiles) + len(dbGatedFloorFilesOutOfScope); found < want {
		t.Errorf("MEASURED_FLOOR DETECTOR_BLIND: the arm-label detector found %d labelled file(s) "+
			"but the two lists name %d. It cannot see files it is supposed to police, so "+
			"UNGATED_FILE above would pass on a repo full of ungated gates.", found, want)
	}
	t.Logf("arm-label detector: %d labelled gate file(s) found, %d governed, %d exempt",
		found, len(gatedFloorFiles), len(dbGatedFloorFilesOutOfScope))
}

// TestMeasuredFloorPatternDiscriminates pins measuredValueInComment in BOTH
// directions on real text from this repo.
//
// 🔴 The negative half is the load-bearing one. The floor blocks this gate reads
// carry historical numbers on purpose — what a value USED to be, what a previous
// work item measured — and a pattern that flagged those would go red on a
// correct tree. Per aihub#361: a false positive is not a milder failure than a
// miss, because the cheapest way to silence a gate that fires on correct code is
// to delete the gate. So the shapes that must stay green are fixtures here, not
// prose promises.
func TestMeasuredFloorPatternDiscriminates(t *testing.T) {
	mustMatch := []struct{ name, text string }{
		{"dated, one line", "floorCardAnchors bounds ... Measured 2026-09-08: 240 across 38 files."},
		{"dated, split across two comment lines", "floorCardParams bounds the total parameter rows the cards pin. Measured 2026-09-08: 222 across the 45 tools."},
		{"undated lower-case inline", "measured: 222 across those 45"},
		{"work item instead of a date", "Measured by aihub#501: 44 tools returned an object."},
		{"zero is a value too", "Measured 2026-09-08: 0 cards are pending."},
	}
	for _, c := range mustMatch {
		if !measuredValueInComment.MatchString(c.text) {
			t.Errorf("measuredValueInComment MISSED %s: %q. Every one of these is a real shape "+
				"aihub#493 found stale in this package; a pattern that misses one lets that shape "+
				"back in.", c.name, c.text)
		}
	}

	mustNotMatch := []struct{ name, text string }{
		{"what a previous wi found stale", "aihub#446 found two stale (floorCardParams claimed 232 where K3 printed 231, floorCardAnchors claimed 207 across 30 files where K6 printed 258 across 38)."},
		{"an era being described", "they still described the 50-tool era (237 params, 46 of 50 on the wire, 46 projections, 278 bound fields)"},
		{"measured with no value at all", "Measured with GOWORK=off and AIHUB_TEST_DB unset, so the database is untouched."},
		{"measured, not predicted", "Measured, not predicted: the gate was run locally against a real Postgres."},
		{"a pointer to the printed line", "Current value: the K6 line."},
		{"a fixture-drift record", "the same drift was measured on the base walk before aihub#501 (248 fresh, 249 on the long-lived database)"},
	}
	for _, c := range mustNotMatch {
		if m := measuredValueInComment.FindString(c.text); m != "" {
			t.Errorf("measuredValueInComment FALSE POSITIVE on %s: matched %q in %q. This text is "+
				"correct as written and lives in the files this gate reads, so the gate would be "+
				"red on a clean tree — and the cheap repair for that is deleting the gate.",
				c.name, m, c.text)
		}
	}

	// And the arm-label pointer must not be satisfiable by any old capital letter
	// plus digit, or NAMES_ARM would accept noise as a pointer.
	for _, bad := range []string{"aihub#493", "pgvector/pgvector:pg16", "the 45 cards", "v1.26"} {
		if armLabelInComment.MatchString(bad) {
			t.Errorf("armLabelInComment accepted %q as an arm label — NAMES_ARM would then be "+
				"satisfied by text that points the reader nowhere.", bad)
		}
	}
	for _, good := range []string{"the K6 line", "the K4/K5 line", "G1's parameter count", "K11"} {
		if !armLabelInComment.MatchString(good) {
			t.Errorf("armLabelInComment rejected %q, which is the exact form the floor comments "+
				"use — NAMES_ARM would be red on the tree it is meant to describe.", good)
		}
	}
}

// declaresArmLabel reports whether a file FORMATS an arm label at the start of a
// test message — `t.Errorf("K6 ANCHOR_...`, `t.Logf("G2: ...` — as opposed to
// merely mentioning one in prose.
//
// 🔴 It reads the AST rather than the raw bytes, and the first version did not.
// A byte-level regex reported THIS file, because the comment two lines above
// documents the shape it looks for by quoting it. That is the same failure as
// grepping for a symbol and hitting a test that only names it in a string: a
// pattern over raw text cannot tell a declaration from a description of one, and
// the description lives in the gate's own explanation of itself. Parsing means
// only real call arguments are examined, so the gate can document its own
// mechanism without reporting itself.
func declaresArmLabel(t *testing.T, base string) bool {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, base, nil, 0) // comments dropped on purpose
	if err != nil {
		t.Fatalf("parse %s: %v", base, err)
	}

	leadingLabel := regexp.MustCompile(`^"[KG]\d+[/\d]*[ :]`)
	declares := false
	ast.Inspect(f, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok || len(call.Args) == 0 {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		switch sel.Sel.Name {
		case "Errorf", "Fatalf", "Logf":
		default:
			return true
		}
		lit, ok := call.Args[0].(*ast.BasicLit)
		if !ok || lit.Kind != token.STRING {
			return true
		}
		if leadingLabel.MatchString(lit.Value) {
			declares = true
		}
		return true
	})
	return declares
}

// ─── aihub#542: the pointed-at line must exist ───────────────────────────────

// logfFormats returns the format string of every t.Logf call in one file of
// this package, string-concatenations flattened. AST rather than raw bytes for
// the same reason as declaresArmLabel: prose that DESCRIBES a log line (this
// comment, the gate's own error text) must not count as one.
func logfFormats(t *testing.T, base string) []string {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, base, nil, 0) // comments dropped on purpose
	if err != nil {
		t.Fatalf("parse %s: %v", base, err)
	}

	var out []string
	ast.Inspect(f, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok || len(call.Args) == 0 {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != "Logf" {
			return true
		}
		if s, ok := flatStringLit(call.Args[0]); ok {
			out = append(out, s)
		}
		return true
	})
	return out
}

// flatStringLit resolves an expression to its string value when it is a string
// literal or a `+` concatenation of them, which are the only format shapes the
// Logf calls in this package use.
func flatStringLit(e ast.Expr) (string, bool) {
	switch v := e.(type) {
	case *ast.BasicLit:
		if v.Kind != token.STRING {
			return "", false
		}
		s, err := strconv.Unquote(v.Value)
		return s, err == nil
	case *ast.BinaryExpr:
		if v.Op != token.ADD {
			return "", false
		}
		l, lok := flatStringLit(v.X)
		r, rok := flatStringLit(v.Y)
		return l + r, lok && rok
	case *ast.ParenExpr:
		return flatStringLit(v.X)
	}
	return "", false
}

// TestMeasuredFloorToolsOnWirePrintArm holds the seventh print arm in place.
//
// floorToolsOnWire was the one governed constant whose value no green log line
// printed: NAMES_ARM accepted its comment because the comment named G2, but the
// line G2 printed carried the distinct-path count, not the outbound-request
// count the floor is set from — a pointer to a line that did not carry the
// value. The header of universal_contract_gate_test.go even recorded the
// workaround as the recipe: raise the floor to an absurd value and read the
// count out of the failure. aihub#542 put the count into G2's log line; this
// arm is what makes removing it again cost a red here rather than a silent
// return to failure-only observability.
func TestMeasuredFloorToolsOnWirePrintArm(t *testing.T) {
	const base = "universal_contract_gate_test.go"
	var g2 []string
	for _, format := range logfFormats(t, base) {
		if strings.HasPrefix(format, "G2") {
			g2 = append(g2, format)
		}
	}
	if len(g2) == 0 {
		t.Fatalf("MEASURED_FLOOR NO_PRINT_ARM: %s has no t.Logf whose format begins with "+
			"\"G2\" — the arm floorToolsOnWire's comment points at prints nothing on the green "+
			"path, so this test cannot pass by not finding its subject.", base)
	}
	found := false
	for _, format := range g2 {
		if strings.Contains(format, "outbound request") {
			found = true
		}
	}
	if !found {
		t.Errorf("MEASURED_FLOOR NO_PRINT_ARM: none of the %d G2 log line(s) in %s carries an "+
			"\"outbound request\" figure. floorToolsOnWire is G2's outbound-request floor and "+
			"its comment points at G2's log line for the current value (NAMES_ARM); without "+
			"the figure that pointer aims at a line that does not carry the value, which is "+
			"the exemption aihub#542 removed — the only way to read the floor's value would "+
			"again be to raise it to an absurd number and read the failure. Put len(observed) "+
			"back into G2's t.Logf.", len(g2), base)
	} else {
		t.Logf("print arm: %d G2 log line(s) in %s, the \"outbound request\" figure present",
			len(g2), base)
	}
}
