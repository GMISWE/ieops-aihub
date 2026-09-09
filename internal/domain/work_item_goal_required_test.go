package domain

// aihub#507 — an empty goal must be refused on BOTH write paths, with the same
// words, and refusing it must not disturb an update that leaves the goal alone.
//
// The defect: `pf_create_work_item {goal: ""}` was a 400 `BAD_REQUEST` reading
// "goal is required", and the identical value through `pf_update_work_item` was
// STORED — 200, work item saved, goal now blank in every list and in the ready
// queue. That is the aihub#396 class (one column, two doors, two answers) in its
// quietest form: unlike aihub#474's instance, which the column CHECK caught and
// turned into a 500, nothing here objects at all. work_items.goal is TEXT NOT
// NULL and NOT NULL admits the empty string.
//
// ─── Why the refusal could be switched on at all ─────────────────────────────
//
// Adding a refusal to a live write path breaks whoever was relying on it, so the
// question was measured before it was argued. Production Cloud SQL, 2026-09-09:
// 2,309 work items, `length(goal) = 0` → 0 rows, `length(btrim(goal)) = 0 AND
// length(goal) > 0` (whitespace-only) → 0 rows. Nobody is clearing goals, so
// "empty means clear
// the goal" was never a used affordance — it was an unstated one. The owner
// ruled on 2026-09-09 that update mirrors create.
//
// ─── The two arms that make this file non-vacuous ────────────────────────────
//
// A contract test on the validator alone passes on a build where the function is
// perfect and nobody calls it — which, expressed as one door checking and the
// other not, is exactly the state this repo was in. So the wiring is pinned too,
// and pinned NESTED: the update path's call has to sit inside the
// `req.Goal != nil` guard, because a required-ness check hoisted out of it would
// refuse every update that does not mention the goal at all. That is the
// fail-closed-too-wide direction, and it is worse than the bug being fixed.

import (
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"strings"
	"testing"
)

// TestEmptyGoalIsRefusedByTheFunctionBothWritePathsCall pins the refusal, its
// code and its message verbatim — and pins the boundary that is deliberately NOT
// crossed.
func TestEmptyGoalIsRefusedByTheFunctionBothWritePathsCall(t *testing.T) {
	t.Run("the empty string is refused", func(t *testing.T) {
		err := validateWorkItemGoalPresent("")
		if err == nil {
			t.Fatalf("the empty goal was ACCEPTED. The column is TEXT NOT NULL and NOT NULL " +
				"admits '', so nothing downstream refuses it either: the write succeeds, the " +
				"caller gets a 200, and the work item renders blank in every list. That is " +
				"the aihub#507 defect exactly.")
		}
		if err.Code != ErrBadRequest {
			t.Errorf("the empty goal was refused with code %q; pf_create_work_item has always "+
				"answered %q and the whole point of aihub#507 is that the two doors stop "+
				"disagreeing about the same value", err.Code, ErrBadRequest)
		}
		// Verbatim. The message is create's own, unchanged since long before this
		// work item, and mirroring create is what the owner ruled — a paraphrase on
		// the update path would leave a caller matching on the text still able to
		// tell which door it came through.
		if want := "goal is required"; err.Message != want {
			t.Errorf("the empty goal was refused with %q, want %q", err.Message, want)
		}
	})

	t.Run("a goal with any content is accepted", func(t *testing.T) {
		for _, tc := range []struct {
			name string
			goal string
		}{
			{"one ascii rune", "a"},
			{"one CJK rune", "中"},
			{"an ordinary goal", "fix the thing"},
			// Whitespace-only is ACCEPTED, and that is the ruling rather than an
			// oversight: create accepts it today, the owner ruled "mirror create",
			// and refusing it on update alone would close one asymmetry by opening
			// another — the very defect class. The census found zero rows of this
			// kind too, so nothing is riding on it; tightening both paths together
			// is a separate decision. If this row ever starts failing, that decision
			// was taken on one path only.
			{"a single space", " "},
			{"a tab", "\t"},
		} {
			if err := validateWorkItemGoalPresent(tc.goal); err != nil {
				t.Errorf("%s (%q) was refused with %v — this function decides EMPTINESS and "+
					"nothing else; the cap and the newline ban are validateWorkItemGoalShape's",
					tc.name, tc.goal, err)
			}
		}
	})
}

// TestCreateStillRefusesAnEmptyGoalInTheSameWords is the negative control for
// the half that was already correct.
//
// aihub#507 moved create's inline check into a shared function. A move is only a
// move if the observable answer does not change, and "the create path still
// refuses the empty string" is not enough on its own — it has to refuse it with
// the same code
// and the same text, from the same position ahead of the shape check. The nil
// pool is the instrument work_item_goal_shape_test.go established: a refused
// goal returns an error with the pool never touched, so this reaches the real
// exported entry point without a database.
func TestCreateStillRefusesAnEmptyGoalInTheSameWords(t *testing.T) {
	panicked, err := createWorkItemReachesPool("")
	if panicked != nil {
		t.Fatalf("an empty goal reached the (nil) pool: CreateWorkItem no longer refuses it up " +
			"front. The column would accept the write, so the 400 that has always guarded " +
			"this path is now the only thing that was standing between a caller and a blank " +
			"work item — and it is gone.")
	}
	var ae *AihubError
	if !errors.As(err, &ae) {
		t.Fatalf("an empty goal was refused with %v, which is not an *AihubError", err)
	}
	if ae.Code != ErrBadRequest {
		t.Errorf("an empty goal was refused with code %q, want %q — unchanged by aihub#507", ae.Code, ErrBadRequest)
	}
	if want := "goal is required"; ae.Message != want {
		t.Errorf("an empty goal was refused with %q, want %q. aihub#507 is a refactor on THIS "+
			"path — the update path was supposed to adopt create's answer, not create to "+
			"adopt a new one.", ae.Message, want)
	}
}

// TestBothWorkItemWritePathsRefuseAnEmptyGoal is the wiring half, and it carries
// the update path's entire obligation.
//
// UpdateWorkItem's first statement is GetWorkItem(ctx, pool, …), so the nil-pool
// instrument used above panics before any request field is read and can
// discriminate nothing there — the same structural reason
// work_item_goal_shape_test.go gives for having no update-side entry-point test.
// Reading the source is therefore not a shortcut around a behaviour test; it is
// the only pure instrument that can see this fact, and it is the same choice
// db_check_policy_test.go makes when it parses the migrations.
//
// The nesting assertion is the half that matters most. `goal: ""` and no `goal`
// key at all arrive as different values of the SAME *string field, and only the
// pointer tells them apart. A validateWorkItemGoalPresent call hoisted above the
// `req.Goal != nil` guard would compile, would pass a test that only counted
// calls, and would refuse every update in the system that does not touch the
// goal — which is most of them.
func TestBothWorkItemWritePathsRefuseAnEmptyGoal(t *testing.T) {
	const validator = "validateWorkItemGoalPresent"

	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "work_items.go", nil, 0)
	if err != nil {
		t.Fatalf("cannot parse work_items.go: %v — this test's instrument is broken, not the code", err)
	}

	fns := map[string]*ast.FuncDecl{}
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Recv != nil {
			continue
		}
		if fn.Name.Name == "CreateWorkItem" || fn.Name.Name == "UpdateWorkItem" {
			fns[fn.Name.Name] = fn
		}
	}

	for _, name := range []string{"CreateWorkItem", "UpdateWorkItem"} {
		fn, found := fns[name]
		if !found {
			t.Fatalf("no top-level func %s in work_items.go — it moved, and this test can no "+
				"longer see whether it refuses the empty goal it writes", name)
		}
		if n := countCalls(fn, validator); n == 0 {
			t.Errorf("%s writes work_items.goal and never calls %s. The column is TEXT NOT "+
				"NULL, which ADMITS the empty string, so a path that skips this check stores a "+
				"blank goal and answers 200 while the other path answers 400 \"goal is "+
				"required\" for the same input (aihub#507, the update half). Call %s with the "+
				"supplied goal.", name, validator, validator)
		}
	}

	// The update path's call must be under the pointer guard.
	upd, found := fns["UpdateWorkItem"]
	if !found {
		return // already reported above
	}
	guards := goalPointerGuards(upd)
	if len(guards) == 0 {
		t.Fatalf("no `if req.Goal != nil` in UpdateWorkItem — either the guard is gone (in "+
			"which case every goal-less update now runs the goal checks) or it is spelled "+
			"differently and this arm can no longer see it. Either way %s's placement is "+
			"unpinned.", validator)
	}
	var guarded int
	for _, g := range guards {
		guarded += countCalls(g, validator)
	}
	if total := countCalls(upd, validator); guarded != total {
		t.Errorf("UpdateWorkItem calls %s %d time(s), of which %d are inside an "+
			"`if req.Goal != nil` guard. A call OUTSIDE that guard sees the zero value of a "+
			"*string that was never supplied and refuses the request — so every update that "+
			"does not mention the goal (labels, priority, attrs_patch: most of them) starts "+
			"failing with \"goal is required\". That is a wider break than the bug this check "+
			"fixes.", validator, total, guarded)
	}
}

// countCalls counts calls to a package-level function by name inside a node.
func countCalls(node ast.Node, name string) int {
	var n int
	ast.Inspect(node, func(x ast.Node) bool {
		call, ok := x.(*ast.CallExpr)
		if !ok {
			return true
		}
		if ident, ok := call.Fun.(*ast.Ident); ok && ident.Name == name {
			n++
		}
		return true
	})
	return n
}

// goalPointerGuards returns the body of every `if req.Goal != nil` in a function.
//
// The condition is matched STRUCTURALLY and EXACTLY: a compound condition such
// as `req.Goal != nil || req.Content != nil` (which UpdateWorkItem also
// contains, for the embedding refresh) is not this guard and must not be
// counted as one, because the goal may be nil inside it.
func goalPointerGuards(fn *ast.FuncDecl) []*ast.BlockStmt {
	var out []*ast.BlockStmt
	ast.Inspect(fn, func(x ast.Node) bool {
		stmt, ok := x.(*ast.IfStmt)
		if !ok {
			return true
		}
		bin, ok := stmt.Cond.(*ast.BinaryExpr)
		if !ok || bin.Op != token.NEQ {
			return true
		}
		sel, ok := bin.X.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != "Goal" {
			return true
		}
		recv, ok := sel.X.(*ast.Ident)
		if !ok || recv.Name != "req" {
			return true
		}
		if ident, ok := bin.Y.(*ast.Ident); !ok || ident.Name != "nil" {
			return true
		}
		out = append(out, stmt.Body)
		return true
	})
	return out
}

// TestGoalRequirednessIsNotFoldedIntoTheCheckMirror is the structural boundary
// aihub#507 chose, asserted rather than left to a comment.
//
// validateWorkItemGoalShape is db_check_policy_test.go's declared Go mirror of
// work_items_goal_check, and that CHECK permits the empty string. Folding
// required-ness into
// it would be the tidier-looking change and would quietly make the mirror
// enforce something the constraint does not, so the correspondence that test
// asserts would stop being true while it kept passing. Two functions is what
// keeps both claims honest, and this is the arm that says so in code.
func TestGoalRequirednessIsNotFoldedIntoTheCheckMirror(t *testing.T) {
	if err := validateWorkItemGoalShape(""); err != nil {
		t.Errorf("validateWorkItemGoalShape(\"\") returned %v. That function mirrors "+
			"work_items_goal_check, which ADMITS the empty string; required-ness belongs to "+
			"validateWorkItemGoalPresent, where it is not claiming to be a constraint the "+
			"database has.", err)
	}
	// And the sibling is not a second copy of the shape rules either — a long
	// legal goal is this function's business only insofar as it is non-empty.
	if err := validateWorkItemGoalPresent(strings.Repeat("a", maxWorkItemGoalRunes+1)); err != nil {
		t.Errorf("validateWorkItemGoalPresent refused an over-cap goal with %v. The cap is the "+
			"other function's rule; enforcing it in both is how the two start disagreeing "+
			"about the message for the same input.", err)
	}
}
