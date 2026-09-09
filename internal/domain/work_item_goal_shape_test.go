package domain

// aihub#474 — the goal cap must hold on BOTH write paths, and the boundary
// value must keep working.
//
// The defect: CreateWorkItem refused a goal over maxWorkItemGoalRunes and
// UpdateWorkItem did not, so the same string was a 400 naming the field through
// one MCP tool and, through the other, a 500 raised by the column CHECK that
// both paths write into (work_items_goal_check, SQLSTATE 23514, which dbErr maps
// to ErrInternalError). Nothing in the tree claimed updates were exempt — the
// owner ruled it an oversight on 2026-09-09 — and nothing pinned EITHER half:
// before this file, `grep -rn "goal exceeds" --include="*_test.go" internal/`
// returned nothing, so the create-path check that DID exist was equally free to
// disappear.
//
// ─── Why the cap is asserted as ACCEPTED and not just exceeded-as-refused ────
//
// Adding a cap to a live write path is a behaviour change with an existing
// caller on the other end. Measured on production Cloud SQL 2026-09-09
// (recorded on the work item): 2,286 work items, `max(char_length(goal)) = 500`,
// zero rows above it. So the change refuses nobody — but the maximum sits
// EXACTLY AT the cap, meaning somebody writes 500-character goals today and an
// off-by-one in the comparison would start refusing them. The acceptance arm is
// that caller's regression test, and it is the arm a `>=` typo fails.
//
// ─── Why the multibyte rows are here ─────────────────────────────────────────
//
// 500 Chinese characters are 1,500 bytes. Postgres `length()` counts CHARACTERS
// and accepts them, so a guard written against len() rather than
// utf8.RuneCountInString would refuse a legal goal — the fail-closed-too-wide
// direction maxWorkItemContentRunes' comment warns about. Those rows are the
// negative control that says the unit is right, not merely that a number is
// compared.

import (
	"context"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"strings"
	"testing"
)

// TestGoalShapeIsTheSameContractOnBothWritePaths pins the cap, the newline ban
// and the at-limit / over-limit boundary on the function both write paths call.
//
// Pinning the shared function rather than each path separately is only honest
// because the wiring is pinned too, one test below: together they say "both
// paths enforce THIS, and this is what THIS does". Either alone is the failure
// mode the other covers — a rule with no callers, or callers of a rule nobody
// checked.
func TestGoalShapeIsTheSameContractOnBothWritePaths(t *testing.T) {
	limit := maxWorkItemGoalRunes
	ascii := func(n int) string { return strings.Repeat("a", n) }
	// U+4E2D, three bytes per rune: n runes, 3n bytes.
	cjk := func(n int) string { return strings.Repeat("中", n) }

	t.Run("accepted", func(t *testing.T) {
		for _, tc := range []struct {
			name string
			goal string
		}{
			{"one rune", ascii(1)},
			{"one under the cap", ascii(limit - 1)},
			{"exactly the cap", ascii(limit)},
			{"exactly the cap in CJK", cjk(limit)},
		} {
			if err := validateWorkItemGoalShape(tc.goal); err != nil {
				t.Errorf("%s (%d runes, %d bytes) was refused with %v — the cap is %d, so this "+
					"goal is legal and the column would have stored it. Production's longest "+
					"goal is exactly %d characters (measured 2026-09-09), so refusing the "+
					"boundary breaks a caller that writes them today.",
					tc.name, len([]rune(tc.goal)), len(tc.goal), err, limit, limit)
			}
		}
	})

	t.Run("refused over the cap", func(t *testing.T) {
		for _, tc := range []struct {
			name string
			goal string
		}{
			{"one over the cap", ascii(limit + 1)},
			{"one over the cap in CJK", cjk(limit + 1)},
			{"far over the cap", ascii(limit * 3)},
		} {
			err := validateWorkItemGoalShape(tc.goal)
			if err == nil {
				t.Errorf("%s (%d runes) was ACCEPTED. work_items_goal_check caps goal at %d, "+
					"so this write reaches Postgres and comes back to the caller as a 500 "+
					"with a constraint name in it instead of a 400 naming the field — which "+
					"is the aihub#474 defect exactly, and the aihub#396 class generally.",
					tc.name, len([]rune(tc.goal)), limit)
				continue
			}
			if err.Code != ErrBadRequest {
				t.Errorf("%s was refused with code %q; the create path has always answered "+
					"%q and the two paths must not disagree about which error a caller "+
					"branches on", tc.name, err.Code, ErrBadRequest)
			}
			// The message create has always produced. Asserted verbatim because
			// the point of aihub#474 is that the two paths answer the SAME thing.
			if want := "goal exceeds 500 characters"; err.Message != want {
				t.Errorf("%s was refused with %q, want %q", tc.name, err.Message, want)
			}
		}
	})

	t.Run("refused for newlines", func(t *testing.T) {
		for _, tc := range []struct {
			name string
			goal string
		}{
			{"LF", "a\nb"},
			{"CR", "a\rb"},
			{"CRLF", "a\r\nb"},
			{"trailing LF", "a\n"},
		} {
			err := validateWorkItemGoalShape(tc.goal)
			if err == nil {
				t.Errorf("%s was accepted; `goal !~ E'[\\n\\r]'` is the other half of the same "+
					"CHECK and this is its Go mirror", tc.name)
				continue
			}
			if err.Code != ErrGoalMultiline {
				t.Errorf("%s was refused with code %q, want %q — the code is the contract, "+
					"not the message", tc.name, err.Code, ErrGoalMultiline)
			}
		}
	})

	t.Run("the cap is checked before the newline ban", func(t *testing.T) {
		// Both halves violated at once. Order matters only because it is
		// observable, and the create path has always reported the length first;
		// an update that reported the other half would be a second way the two
		// tools disagree about the same string.
		err := validateWorkItemGoalShape(ascii(limit+1) + "\n")
		if err == nil || err.Code != ErrBadRequest {
			t.Errorf("a goal that is both too long AND multiline was answered with %v; the "+
				"create path reports the length first and both paths now share the function "+
				"that decides", err)
		}
	})

	t.Run("emptiness is not this function's business", func(t *testing.T) {
		// BOTH write paths refuse "" since aihub#507 — and still not from here.
		// This function is the Go mirror of work_items_goal_check (the
		// correspondence db_check_policy_test.go asserts), and that CHECK permits
		// the empty string, so a required-ness check folded in would make the
		// mirror enforce a rule the constraint does not have while that test kept
		// passing. The refusal lives in validateWorkItemGoalPresent, which both
		// paths call separately; see work_item_goal_required_test.go.
		//
		// The other half of the old reason survives unchanged: `goal: ""` and no
		// goal at all are different requests on an update, and the `req.Goal != nil`
		// guard at the call site is what tells them apart. Both this arm and the
		// nesting arm in that file are what keep a required-ness check from
		// reaching every update that omits goal.
		if err := validateWorkItemGoalShape(""); err != nil {
			t.Errorf("the empty goal was refused here with %v; required-ness is "+
				"validateWorkItemGoalPresent's, and keeping it out of this function is what "+
				"lets this one keep claiming to be work_items_goal_check's Go mirror", err)
		}
	})
}

// TestCreateWorkItemRefusesAnOverlongGoalBeforeItTouchesTheDatabase is the
// entry-point arm for the create path: the rule above is reached by the exported
// function a caller actually enters, and it is reached BEFORE the first query.
//
// The instrument is the nil pool, the same one internal/mcp's published-contract
// tests use: a refused goal comes back as an error with the pool never touched,
// while an accepted one can only demonstrate that it got past the guard by dying
// on the nil pool. That makes the assertion bidirectional without a database.
//
// ⚠️ There is deliberately no matching test for UpdateWorkItem, and the reason is
// structural rather than an omission: UpdateWorkItem's first statement is
// `GetWorkItem(ctx, pool, …)`, so a nil pool panics before any request field is
// read and the instrument has nothing to discriminate. The update path's
// obligation is therefore carried by the wiring test below — that it calls the
// same function — plus the contract test above. Writing a DB-gated test to cover
// the same ground would move this file out of the no-database CI step for a fact
// two pure tests already establish.
func TestCreateWorkItemRefusesAnOverlongGoalBeforeItTouchesTheDatabase(t *testing.T) {
	limit := maxWorkItemGoalRunes

	panicked, err := createWorkItemReachesPool(strings.Repeat("a", limit+1))
	if panicked != nil {
		t.Errorf("a %d-rune goal reached the (nil) pool: CreateWorkItem no longer refuses an "+
			"overlong goal up front, so the column CHECK is the only rule left and the "+
			"caller gets a 500 instead of a 400", limit+1)
	} else {
		var ae *AihubError
		if !errors.As(err, &ae) || ae.Code != ErrBadRequest {
			t.Errorf("a %d-rune goal was refused with %v; it must be a 400 naming the field", limit+1, err)
		}
	}

	if panicked, err := createWorkItemReachesPool(strings.Repeat("a", limit)); panicked == nil {
		t.Errorf("a goal of exactly %d runes returned %v instead of proceeding to the pool — "+
			"the guard is refusing the boundary value, which production writes today", limit, err)
	}
}

// createWorkItemReachesPool sends one goal through the real create path with a
// nil pool and reports which of the two outcomes happened: a non-nil panic means
// the goal got past every pre-query guard, a non-nil error means it did not.
//
// The recover is not defensive tidiness — a panic escaping a test kills the
// whole package binary, which would report this file's failure as every other
// test in internal/domain failing too.
func createWorkItemReachesPool(goal string) (panicked any, err error) {
	defer func() { panicked = recover() }()
	_, aihubErr := CreateWorkItem(context.Background(), nil, &CreateWorkItemRequest{
		Goal: goal, Project: "p",
	}, "u_test", "tester", nil, "")
	if aihubErr != nil {
		return nil, aihubErr
	}
	return nil, nil
}

// TestBothWorkItemWritePathsCallTheGoalShapeValidator is the wiring half, and it
// is the arm that would have caught aihub#474 on the day it was introduced.
//
// A contract test on validateWorkItemGoalShape alone goes green on a build where
// the function is perfect and UpdateWorkItem never calls it — which, expressed as
// two inline copies instead of one function, is precisely the state this repo
// was in. So the claim being pinned is not "the rule is right", it is "both
// doors go through the rule".
//
// It reads the source rather than the behaviour because the behaviour is not
// reachable without a database on one of the two paths; go/parser is the same
// choice db_check_policy_test.go makes when it parses the migrations to compare
// them against the Go constants. A test that grepped work_items.go for the
// literal 500 would go green the day somebody typed the cap a third time, which
// is the failure being removed rather than the one being detected.
func TestBothWorkItemWritePathsCallTheGoalShapeValidator(t *testing.T) {
	const validator = "validateWorkItemGoalShape"

	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "work_items.go", nil, 0)
	if err != nil {
		t.Fatalf("cannot parse work_items.go: %v — this test's instrument is broken, not the code", err)
	}

	calls := map[string]int{}
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Recv != nil {
			continue
		}
		if fn.Name.Name != "CreateWorkItem" && fn.Name.Name != "UpdateWorkItem" {
			continue
		}
		calls[fn.Name.Name] = 0
		ast.Inspect(fn, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			if ident, ok := call.Fun.(*ast.Ident); ok && ident.Name == validator {
				calls[fn.Name.Name]++
			}
			return true
		})
	}

	for _, name := range []string{"CreateWorkItem", "UpdateWorkItem"} {
		n, found := calls[name]
		if !found {
			t.Fatalf("no top-level func %s in work_items.go — it moved, and this test can no "+
				"longer see whether it validates the goal it writes", name)
		}
		if n == 0 {
			t.Errorf("%s writes work_items.goal and never calls %s. The column CHECK caps the "+
				"goal at %d runes and bans newlines, so a path that skips the Go mirror hands "+
				"the caller a 500 from the constraint for input the other path answers with a "+
				"400 (aihub#474, the update half). Call %s with the supplied goal.",
				name, validator, maxWorkItemGoalRunes, validator)
		}
	}
}
