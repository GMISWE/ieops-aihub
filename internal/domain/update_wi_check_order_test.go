package domain

// aihub#543 probe wave 2, band 1 — two `docs/mcp-cards/pf_update_work_item.md`
// sentences about ORDER and about ABSENCE, neither of which any existing arm
// reaches.
//
//	"The checks sit BEHIND the matrix for the same reason `goal_change_reason`'s
//	 does — an edit refused on state is not first told its goal is multiline."
//	    -> TestTheEditMatrixIsCheckedBeforeTheGoalAndReasonRules
//	"Two codes were retired and are no longer produced anywhere: 409
//	 `GOAL_CHANGE_NOT_ALLOWED` and 403 `WI_RECLASSIFY_FORBIDDEN`."
//	    -> TestRetiredWorkItemErrCodesHaveNoProducerAnywhereInTheTree
//
// 🔴 WHY THE EXISTING GATES CANNOT HOLD EITHER.
//
// TestUpdateGate walks every (status, tier, actor) cell of the matrix and
// TestGoalShapeIsTheSameContractOnBothWritePaths pins what the shape rules
// decide, but both call their subject DIRECTLY. Neither can see the order the
// two are reached in inside UpdateWorkItem, and the order is observable: send a
// 600-character goal to a wrapped work item and the answer is either 409
// CONFLICT_TERMINAL_STATE (the state, which no change of goal can fix) or 400
// "goal exceeds 500 characters" (advice about a value the caller must not send
// at all). Hoisting the shape check above the gate compiles, passes every
// existing arm, and turns the first answer into the second.
//
// TestRetiredErrCodesAreNotProducedByTheUpdatePath is the closest existing arm
// to the second sentence and it is scoped to ONE PACKAGE: its glob is `*.go` in
// internal/domain. The card says "anywhere", which is a claim about the tree,
// and internal/server maps error codes to HTTP statuses and could produce one
// itself. The narrower arm stays as the one whose failure text explains the
// retirement; this one is the claim the card actually makes.
//
// No database:
//
//	GOWORK=off go test ./internal/domain/ -run 'TestTheEditMatrixIsChecked|TestRetiredWorkItemErrCodes' -count=1

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// updateWICheckOrderFile is the file that carries UpdateWorkItem.
const updateWICheckOrderFile = "work_items.go"

// gatedAfterTheMatrix names the calls and request fields whose checks the card
// says run BEHIND the matrix, with the reason each is in the list.
//
// Written as the two shape validators plus the two reason fields rather than as
// "everything after the gate": those four are what the sentence names, and a
// list that quietly grew to cover every call in the function would report a
// property nobody claimed.
var gatedAfterTheMatrix = []struct {
	name string
	why  string
}{
	{"validateWorkItemGoalShape", "the cap and the newline ban (aihub#474)"},
	{"validateWorkItemGoalPresent", "the emptiness refusal (aihub#507)"},
}

// reasonFieldsGatedAfterTheMatrix are the rider fields whose minimum-length
// checks the same sentence covers. They are selectors rather than calls, so they
// are located by field name on `req`.
var reasonFieldsGatedAfterTheMatrix = []string{"GoalChangeReason", "ReclassifyReason"}

// TestTheEditMatrixIsCheckedBeforeTheGoalAndReasonRules pins the ORDER of the
// two refusals a mixed-fault request can take.
//
// 🔴 A structural assertion, and the reason is the one work_item_goal_shape_test
// records for its own missing update-path arm: UpdateWorkItem's first statement
// is GetWorkItem, so the nil-pool instrument that makes the create path's
// ordering observable without a database has nothing to discriminate here. The
// behavioural version of this test needs a real Postgres, a seeded wrapped work
// item and a 600-character goal — for a fact the parsed call order establishes
// exactly. What it cannot establish is that the gate REFUSES, and that is
// TestUpdateGate's whole subject; the two are complementary, not redundant.
//
// MUTANTS (applied to this tree and run; the verdict is what happened):
//
//	M1  enforcement: move the goal block above the
//	    strictestSuppliedEditTier/updateGate block
//	                                            RED  names validateWorkItemGoalShape
//	                                                 and validateWorkItemGoalPresent,
//	                                                 both now ahead of the gate
//	M2  enforcement: delete the updateGate call from UpdateWorkItem entirely
//	                                            RED  the floor, which refuses to
//	                                                 answer "the gate is first" for
//	                                                 a function with no gate
//	M3a enforcement: rename the request parameter throughout UpdateWorkItem
//	    (`req` → `patch`)                       RED  the reason-field floor, naming
//	                                                 both fields
//	M3b enforcement: rename the PARAMETER but alias it back (`req := patch`)
//	                                            GREEN and correctly so: the scan is
//	                                                 keyed on the identifier the code
//	                                                 READS, and the code still reads
//	                                                 `req`. Recorded because the
//	                                                 naive expectation was red, and
//	                                                 because it is what makes M3a's
//	                                                 floor the thing that matters:
//	                                                 the arm cannot be fooled into
//	                                                 finding NOTHING silently
//	M4  publication: drop the citation clause from the card sentence
//	                                            RED  K12 DEBT_GROWTH — the sentence
//	                                                 falls back into the debt
//	                                                 column. ⚠️ This arm does not
//	                                                 read the card, so its
//	                                                 publication side is held by
//	                                                 K12's citation binding
func TestTheEditMatrixIsCheckedBeforeTheGoalAndReasonRules(t *testing.T) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, updateWICheckOrderFile, nil, 0)
	if err != nil {
		t.Fatalf("cannot parse %s: %v — this test's instrument is broken, not the code",
			updateWICheckOrderFile, err)
	}

	var fn *ast.FuncDecl
	for _, decl := range file.Decls {
		d, ok := decl.(*ast.FuncDecl)
		if ok && d.Recv == nil && d.Name.Name == "UpdateWorkItem" {
			fn = d
			break
		}
	}
	if fn == nil {
		t.Fatalf("no top-level func UpdateWorkItem in %s — it moved, and this arm can no longer "+
			"see which of its checks runs first", updateWICheckOrderFile)
	}

	gateAt := firstCallPos(fn, "updateGate")
	if gateAt == token.NoPos {
		t.Fatalf("UpdateWorkItem never calls updateGate. Every ordering assertion below would "+
			"then compare against a position that does not exist and pass vacuously — and the "+
			"absent call is the larger defect: without it %s writes contract-tier fields on a "+
			"wrapped work item (aihub#440).", updateWICheckOrderFile)
	}

	for _, c := range gatedAfterTheMatrix {
		at := firstCallPos(fn, c.name)
		if at == token.NoPos {
			t.Errorf("UpdateWorkItem never calls %s (%s), so this arm cannot tell whether it "+
				"runs before or after the matrix — and a write path that skips it is the "+
				"aihub#474/aihub#507 defect itself", c.name, c.why)
			continue
		}
		if at < gateAt {
			t.Errorf("%s (%s) is called at %s, AHEAD of updateGate at %s. A caller editing a "+
				"wrapped work item with an illegal goal is then told about the goal — advice "+
				"about a value it must not send at all — instead of about the state, which is "+
				"the half no change of value can fix. The matrix decides first; the shape "+
				"rules decide what is legal to send once it has.",
				c.name, c.why, fset.Position(at), fset.Position(gateAt))
		}
	}

	for _, field := range reasonFieldsGatedAfterTheMatrix {
		uses := reqFieldPositions(fn, field)
		if len(uses) == 0 {
			t.Errorf("UpdateWorkItem never reads req.%s. Its minimum-length check is one of the "+
				"two the card says run behind the matrix, and a field nothing reads has no "+
				"check to order", field)
			continue
		}
		for _, at := range uses {
			if at < gateAt {
				t.Errorf("req.%s is read at %s, ahead of updateGate at %s. Its reason check "+
					"would then answer an edit the matrix was going to refuse on state, so a "+
					"caller fixes a 10-character reason string and gets the same 409 back.",
					field, fset.Position(at), fset.Position(gateAt))
			}
		}
	}
}

// firstCallPos returns the position of the first call to a package-level
// function by name inside a node, or token.NoPos.
//
// ast.Inspect walks in source order for the statement list of a function body,
// which is what makes "first" meaningful here; the minimum is taken explicitly
// anyway so the answer does not depend on that.
func firstCallPos(node ast.Node, name string) token.Pos {
	out := token.NoPos
	ast.Inspect(node, func(x ast.Node) bool {
		call, ok := x.(*ast.CallExpr)
		if !ok {
			return true
		}
		ident, ok := call.Fun.(*ast.Ident)
		if !ok || ident.Name != name {
			return true
		}
		if out == token.NoPos || call.Pos() < out {
			out = call.Pos()
		}
		return true
	})
	return out
}

// reqFieldPositions returns every position at which `req.<field>` is read inside
// a node.
func reqFieldPositions(node ast.Node, field string) []token.Pos {
	var out []token.Pos
	ast.Inspect(node, func(x ast.Node) bool {
		sel, ok := x.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != field {
			return true
		}
		if recv, ok := sel.X.(*ast.Ident); ok && recv.Name == "req" {
			out = append(out, sel.Pos())
		}
		return true
	})
	return out
}

// retiredWorkItemErrCodes are the two identifiers aihub#440 retired. They stay
// DECLARED so a stale client's branch and a doc row still resolve, which is only
// safe while nothing produces them.
var retiredWorkItemErrCodes = []string{"ErrGoalChangeNotAllowed", "ErrWIReclassifyForbidden"}

// retiredCodeDeclarationSite is the one file allowed to name them: it declares
// them and carries the retirement comment, and errors.go's own status mapping
// still has to answer for a code a stale caller might quote back.
const retiredCodeDeclarationSite = "internal/domain/errors.go"

// liveErrCodeControl is a code that IS produced, used as the walk's positive
// control. Without it "no file mentions the retired codes" is answered just as
// well by a walk that read no files at all.
const liveErrCodeControl = "ErrConflictTerminalState"

// TestRetiredWorkItemErrCodesHaveNoProducerAnywhereInTheTree is the card's
// "anywhere", measured over the tree rather than over one package.
//
// MUTANTS (applied to this tree and run):
//
//	M5  enforcement: answer with ErrGoalChangeNotAllowed from
//	    internal/server/router.go's handleUpdateWorkItem
//	                                            RED  names internal/server/router.go
//	                                                 and the code — and MEASURED
//	                                                 GREEN on
//	                                                 TestRetiredErrCodesAreNotProducedByTheUpdatePath,
//	                                                 which is the whole reason this
//	                                                 arm exists rather than a rename
//	                                                 of that one
//	M6  enforcement: the same production inside internal/domain/work_items.go
//	                                            RED  here AND on the older arm; the
//	                                                 overlap is what shows this one
//	                                                 is a widening rather than a
//	                                                 replacement
//	M7  instrument: point the walk at a directory that does not exist
//	                                            RED  the walk error is a failure,
//	                                                 not an empty pass
//	M8  instrument: point the positive control at a code produced nowhere outside
//	    errors.go (stands in for a tree where none is)
//	                                            RED  the control, naming the count
//	                                                 it measured: 0 of 105 files. A
//	                                                 tree where no code is produced
//	                                                 outside its declaration is no
//	                                                 evidence about these two
//	M9  publication: drop the citation clause from the card sentence
//	                                            RED  K12 DEBT_GROWTH
func TestRetiredWorkItemErrCodesHaveNoProducerAnywhereInTheTree(t *testing.T) {
	const root = "../.."

	scanned := 0
	controlSeen := 0
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", "node_modules", "vendor":
				return fs.SkipDir
			}
			return nil
		}
		name := d.Name()
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			return nil
		}
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			rel = path
		}
		rel = filepath.ToSlash(rel)
		if rel == retiredCodeDeclarationSite {
			return nil
		}
		src, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		scanned++
		text := string(src)
		if strings.Contains(text, liveErrCodeControl) {
			controlSeen++
		}
		for _, code := range retiredWorkItemErrCodes {
			if strings.Contains(text, code) {
				t.Errorf("%s names %s, which aihub#440 retired and this card says is no longer "+
					"produced ANYWHERE. Wrong-state refusals on a work-item update are 409 "+
					"CONFLICT_WI_ALREADY_CLAIMED / CONFLICT_TERMINAL_STATE and wrong-caller "+
					"refusals are 403 FORBIDDEN — one code per rejection KIND is the whole "+
					"point of the matrix, and each retired code answered BOTH halves of its "+
					"own field's gate. If one is being revived, delete its retirement comment "+
					"in %s in the same change.", rel, code, retiredCodeDeclarationSite)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v — a walk that failed reports no mentions for the same reason a "+
			"clean tree does, and answering green while unable to look is the failure this "+
			"whole family is about", root, err)
	}

	// Two floors. The first says the walk read the tree; the second says the tree
	// it read still produces error codes at all.
	// 105 at the time of writing (2026-09-10); the floor sits below that with room
	// for ordinary deletions and far above the value a broken walk produces.
	if scanned < 80 {
		t.Fatalf("scanned only %d non-test .go file(s) under %s — this repo has over a hundred, so "+
			"the walk is not reading what it thinks it is and every absence above is an "+
			"artefact of the walk", scanned, root)
	}
	if controlSeen == 0 {
		t.Errorf("no file outside %s names %s either. A tree where NO error code is referenced "+
			"outside its declaration is one where this arm's absence assertions say nothing "+
			"about the two retired codes; the control is what makes the silence meaningful.",
			retiredCodeDeclarationSite, liveErrCodeControl)
	}
	t.Logf("scanned %d non-test .go file(s); %d of them reference %s",
		scanned, controlSeen, liveErrCodeControl)
}
