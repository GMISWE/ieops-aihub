package server

// aihub#210: methodology.* memory writes require a wi-bound attempt credential.
// These verify the pre-DB guards in handleRemember fire (400 missing wi / 403
// missing creds) BEFORE any database access — a nil pool is passed, so reaching
// the DB would panic. Mirrors router_auth_test.go's auth-before-write strategy.

import (
	"bytes"
	"context"
	"go/ast"
	"go/parser"
	"go/printer"
	"go/token"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/require"

	"github.com/GMISWE/ieops-aihub/internal/domain"
)

// methodologyWriterUser has writer access to "testproject" so checkProjectAccess
// passes and execution reaches the methodology gate.
func methodologyWriterUser() *UserContext {
	return &UserContext{
		UserID:       "u_writer",
		DisplayName:  "Writer User",
		UserType:     "human",
		Role:         "writer",
		ProjectRoles: map[string]string{"testproject": "writer"},
		APIKeyID:     "k_writer",
	}
}

func postRememberNilPool(t *testing.T, body string) *httptest.ResponseRecorder {
	t.Helper()
	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/v1/memories", strings.NewReader(body))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	setUser(c, methodologyWriterUser())
	if err := handleRemember(nil)(c); err != nil {
		e.HTTPErrorHandler(err, c)
	}
	return rec
}

func TestRemember_MethodologyRequiresWorkItem(t *testing.T) {
	rec := postRememberNilPool(t,
		`{"project":"testproject","type":"methodology.spec","content":"x"}`)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("methodology.* without work_item_id: expected 400, got %d (body: %s)",
			rec.Code, rec.Body.String())
	}
}

func TestRemember_MethodologyRequiresCredentials(t *testing.T) {
	rec := postRememberNilPool(t,
		`{"project":"testproject","type":"methodology.spec","content":"x","work_item_id":"wi_x"}`)
	if rec.Code != http.StatusForbidden {
		t.Errorf("methodology.* without attempt credentials: expected 403, got %d (body: %s)",
			rec.Code, rec.Body.String())
	}
}

// TestEnforceMethodologyAttemptGate covers the shared mutate-time gate used by
// handleReinforceMemory / handleUpdateMemory (aihub#210 bypass fix). Only the
// reject-before-verify branches are exercised (a nil pool is passed, so the
// VerifyAttemptCredentialPool path — which needs a DB — is intentionally not hit).
func TestEnforceMethodologyAttemptGate(t *testing.T) {
	wi := "wi_target"
	t.Run("methodology target without wi -> 403", func(t *testing.T) {
		e := enforceMethodologyAttemptGate(context.TODO(), nil, "methodology.spec", nil, "ra_x", "sekret", 1, "")
		if e == nil || e.Code != domain.ErrForbidden {
			t.Fatalf("want ErrForbidden, got %v", e)
		}
	})
	t.Run("methodology without credentials -> 403", func(t *testing.T) {
		e := enforceMethodologyAttemptGate(context.TODO(), nil, "methodology.plan", &wi, "", "", 0, "")
		if e == nil || e.Code != domain.ErrForbidden {
			t.Fatalf("want ErrForbidden, got %v", e)
		}
	})
	t.Run("non-methodology without credentials -> allowed", func(t *testing.T) {
		if e := enforceMethodologyAttemptGate(context.TODO(), nil, "experience.debug", nil, "", "", 0, ""); e != nil {
			t.Fatalf("want nil (verify-if-supplied skips), got %v", e)
		}
	})
	t.Run("non-methodology creds without work_item_id -> 400", func(t *testing.T) {
		e := enforceMethodologyAttemptGate(context.TODO(), nil, "fact.note", nil, "ra_x", "sekret", 1, "")
		if e == nil || e.Code != domain.ErrBadRequest {
			t.Fatalf("want ErrBadRequest, got %v", e)
		}
	})
}

// gateReachesTheVerifier runs the gate against a NIL pool and reports whether it
// got as far as VerifyAttemptCredentialPool.
//
// A panic is the observation, and it is the only one available: reaching the
// verifier is what "binds to this work item" means, and with no database there
// is nothing else the attempt could produce. Same technique as
// TestPublishedBaseStrengthRangeIsTheEnforcedOne's nil-pool arms — "a legal
// value can only demonstrate that it got through by dying on it". The recover is
// not tidiness: a panic escaping a test kills the package binary and would
// report this file's failure as every other test in internal/server failing too.
func gateReachesTheVerifier(memType string, memWorkItemID *string, reqWorkItemID string) (reached bool, err *domain.AihubError) {
	defer func() {
		if recover() != nil {
			reached = true
		}
	}()
	err = enforceMethodologyAttemptGate(context.TODO(), nil, memType, memWorkItemID,
		"ra_x", "sekret", 1, reqWorkItemID)
	return false, err
}

// TestMethodologyGateBindsToTheTargetMemorysWorkItem is aihub#543 wave 2, slice
// L6 — the live half of the pf_reinforce_memory card's sentence "`methodology.*`
// memories take the other branch, which binds to the TARGET memory's own work
// item".
//
// ─── The discriminating observation ──────────────────────────────────────────
//
// Both branches end in VerifyAttemptCredentialPool, so "it verifies something"
// separates nothing. What separates them is WHICH work item they verify against,
// and the case that shows it is a call carrying credentials with NO
// caller-supplied work_item_id:
//
//	methodology.*      the branch ignores reqWorkItemID entirely and binds to
//	                   the memory's own wi, so it reaches the verifier
//	non-methodology    the branch requires reqWorkItemID and answers 400
//	                   without reading anything
//
// With reqWorkItemID empty there is nothing else the methodology branch could
// have bound to, which is what makes the pair a measurement rather than a
// restatement. TestEnforceMethodologyAttemptGate above deliberately covers only
// the reject-before-verify branches and says so; this is the branch it excludes.
//
//	M1  make the methodology branch pass reqWorkItemID to the
//	    verifier instead of wiID                                🔴 GREEN
//	M2  delete the HasPrefix(memType, "methodology.") test      RED (both rows)
//	M3  drop the non-methodology work_item_id requirement       RED (the control
//	                                                            row reaches the
//	                                                            verifier too and
//	                                                            the pair stops
//	                                                            discriminating)
//
// 🔴 M1 IS RECORDED GREEN RATHER THAN FIXED HERE, and it is the reason the arm
// below exists. Predicted red and measured green: the mutated branch still
// reaches VerifyAttemptCredentialPool, so it still panics on the nil pool, and a
// panic cannot say which argument produced it. This arm therefore holds WHICH
// BRANCH is taken and nothing about WHAT it binds to —
// TestMethodologyGateVerifiesTheMemorysWorkItemAndNotTheCallers holds that, and
// answers M1 red. Left stated rather than deleted because the limit is the
// interesting part: a panic-observing probe is blind to arguments, which is worth
// knowing before reaching for the technique again.
func TestMethodologyGateBindsToTheTargetMemorysWorkItem(t *testing.T) {
	target := "wi_target"

	// The methodology branch: no caller-supplied work item, and it still binds.
	reached, gateErr := gateReachesTheVerifier("methodology.spec", &target, "")
	if !reached {
		t.Fatalf("a methodology.* reinforce carrying credentials and no work_item_id did not "+
			"reach the credential verifier (returned %v). The card says this branch binds to "+
			"the TARGET memory's own work item rather than to the caller-supplied one — if it "+
			"now needs the caller's, it is the same branch as the other one and the card's "+
			"account of why pf_save_artifact traffic was unaffected by aihub#325 is wrong.",
			gateErr)
	}

	// The control: the same arguments on a non-methodology memory take the other
	// branch and are refused without a read. Without this row the arm above is
	// satisfied by a gate that verifies unconditionally.
	reached, gateErr = gateReachesTheVerifier("experience.debug", &target, "")
	if reached {
		t.Fatal("a non-methodology reinforce carrying credentials and no work_item_id reached " +
			"the verifier. That path is supposed to answer 400 naming the missing field — the " +
			"400 the whole aihub#325 defect produced — so the two branches no longer differ " +
			"and the row above proves nothing about which work item is bound.")
	}
	if gateErr == nil || gateErr.Code != domain.ErrBadRequest {
		t.Fatalf("the non-methodology control returned %v, want a 400 naming work_item_id", gateErr)
	}
}

// TestMethodologyGateVerifiesTheMemorysWorkItemAndNotTheCallers is the arm the
// nil-pool pair above cannot be.
//
// 🔴 MEASURED, and this is why it exists. Changing the methodology branch to
// pass the CALLER-supplied work item to the verifier instead of the memory's own
// left TestMethodologyGateBindsToTheTargetMemorysWorkItem GREEN: the mutated
// branch still reaches VerifyAttemptCredentialPool, so it still panics on the nil
// pool, and a panic cannot say which argument produced it. The pair above holds
// WHICH BRANCH is taken; it cannot hold WHAT that branch binds to — which is the
// half of the card's sentence carrying all the weight, because binding to the
// caller's id is exactly the aihub#210 back door.
//
// So this is a code property, checked positionally rather than by local variable
// name: the credential call inside the methodology branch must not be handed the
// function's LAST parameter (the caller-supplied work item), the branch must
// dereference the memory's own, and the call outside the branch must be handed
// the caller's. Renaming a local is free; renaming a PARAMETER is a signature
// change and is meant to be read.
//
// Driving it against a database instead would need a methodology.* memory bound
// to one work item and a live attempt on another — reachable, and a heavier
// fixture for a claim that is entirely about which identifier reaches one call.
//
//	M1  methodology branch passes reqWorkItemID to the verifier   RED
//	    (the mutant the pair above answered GREEN)
//	M2  non-methodology branch passes the memory's wi             RED
//	M3  the methodology branch stops dereferencing memWorkItemID  RED
//	M4  delete one of the two verifier calls                      RED
func TestMethodologyGateVerifiesTheMemorysWorkItemAndNotTheCallers(t *testing.T) {
	const src = "routes_memory.go"
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, src, nil, 0)
	require.NoError(t, err, "parse %s", src)

	var fn *ast.FuncDecl
	ast.Inspect(file, func(n ast.Node) bool {
		d, ok := n.(*ast.FuncDecl)
		if ok && d.Name.Name == "enforceMethodologyAttemptGate" {
			fn = d
			return false
		}
		return true
	})
	require.NotNil(t, fn, "%s no longer declares enforceMethodologyAttemptGate", src)

	// Parameter names by position, so the assertions below survive a local
	// rename and refuse a silent parameter reorder.
	var params []string
	for _, field := range fn.Type.Params.List {
		for _, name := range field.Names {
			params = append(params, name.Name)
		}
	}
	require.Len(t, params, 8,
		"enforceMethodologyAttemptGate now takes %d parameter(s) (%v); this arm reads the "+
			"memory's work item and the caller's positionally, so a signature change has to "+
			"be looked at rather than absorbed", len(params), params)
	memWorkItem, callerWorkItem := params[3], params[7]

	// The methodology branch, found by its condition rather than by position.
	var methodologyBranch *ast.BlockStmt
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		ifStmt, ok := n.(*ast.IfStmt)
		if !ok {
			return true
		}
		if strings.Contains(exprText(fset, ifStmt.Cond), `"methodology."`) {
			methodologyBranch = ifStmt.Body
			return false
		}
		return true
	})
	require.NotNil(t, methodologyBranch,
		"no branch in enforceMethodologyAttemptGate tests for the methodology. prefix, so "+
			"there is no \"other branch\" for the card's sentence to be about")

	inside := verifierWorkItemArgs(fset, methodologyBranch)
	all := verifierWorkItemArgs(fset, fn.Body)
	require.Len(t, inside, 1,
		"the methodology branch makes %d credential-verification call(s) (%v); the card's "+
			"sentence is about one", len(inside), inside)
	require.Len(t, all, 2,
		"enforceMethodologyAttemptGate makes %d credential-verification call(s) (%v). Two is "+
			"the shape the card describes — one per branch — and a single shared call would "+
			"mean the two branches no longer bind different work items at all", len(all), all)

	require.NotEqual(t, callerWorkItem, inside[0],
		"the methodology branch verifies the CALLER-supplied work item (%s). The card says it "+
			"binds to the TARGET memory's own, and binding to the caller's is the aihub#210 "+
			"back door this gate closed: a caller holding any claimed work item's credentials "+
			"could then mutate any methodology.* artifact by naming their own work item.",
		callerWorkItem)
	require.Contains(t, exprText(fset, methodologyBranch), "*"+memWorkItem,
		"the methodology branch never dereferences %s, so whatever it verifies is not read "+
			"from the memory's own row", memWorkItem)

	outside := ""
	for _, arg := range all {
		if arg != inside[0] {
			outside = arg
		}
	}
	require.Equal(t, callerWorkItem, outside,
		"the non-methodology branch verifies %q rather than the caller-supplied %s. That "+
			"branch is verify-if-supplied against the work item the CALLER named — if both "+
			"branches bound the same thing the card's \"takes the other branch\" would describe "+
			"a distinction that no longer exists.", outside, callerWorkItem)
}

// verifierWorkItemArgs returns the work-item argument, as source text, of every
// VerifyAttemptCredentialPool call in a block.
func verifierWorkItemArgs(fset *token.FileSet, block ast.Node) []string {
	var out []string
	ast.Inspect(block, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != "VerifyAttemptCredentialPool" || len(call.Args) < 3 {
			return true
		}
		out = append(out, exprText(fset, call.Args[2]))
		return true
	})
	return out
}

// exprText renders a node back to source.
func exprText(fset *token.FileSet, n ast.Node) string {
	var buf bytes.Buffer
	if err := printer.Fprint(&buf, fset, n); err != nil {
		return ""
	}
	return buf.String()
}
