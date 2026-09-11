package domain

// aihub#522 — PredictConflicts and FnClaimWorkItem must answer every query
// error, because for both of them a silent empty result is a live lie.
//
// The aihub#500 census (PR #433's out-of-scope list) named the swallowed-Query
// shape at 13 sites outside GetReadyQueue. The ones in this package's two
// hottest functions are gated here with the SAME scanner aihub#500 built and
// aihub#548 extended (scanQueryErrorHandling, ready_queue_query_errors_test.go),
// plus a QueryRow twin, because the census's shape — a two-value `rows, err :=
// X.Query(…)` — is blind to `err := X.QueryRow(…).Scan(…)`, and three of the
// worst instances in PredictConflicts were exactly that shape:
//
//   - rule 1, the HARD gate: any probe error read as "nobody holds this lock",
//     so the one rule whose job is to block failed open (the same defect
//     aihub#410 fixed in probeForeignLockHolders, this probe's claim-path twin);
//   - the id/project resolution: a failed lookup silently mis-namespaced every
//     file_scope key the rules below compare;
//   - the H7 visibility fold: a failed project lookup SKIPPED the redaction and
//     handed the caller an unredacted cross-project prediction.
//
// Why PredictConflicts surfaces rather than documents best-effort: the response
// {"predictions":[],"severity":"info"} is byte-identical to a genuine all-clear
// (aihub#238's fake all-clear, re-derived at every one of these sites), pf-work's
// pre-claim gate branches on it, and aihub#511's wrap note records that the
// swallow survived the injection fix precisely because no caller can tell a
// failed query from an empty answer. FnClaimWorkItem's sites run inside a
// SERIALIZABLE transaction, where a swallowed statement error additionally
// downgrades a classified retryable 409 into an unclassifiable 500 at tx.Commit
// (aihub#334 instance 3's mechanism).
//
// No database:
//
//	GOWORK=off go test ./internal/domain/ -run 'QueryError|QueryRowError|SurfacesQueryFailure' -count=1

import (
	"context"
	"go/ast"
	"go/token"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// TestPredictConflictsAnswersEveryQueryError runs aihub#500/#548's scanner over
// PredictConflicts: every `rows, err := pool.Query(…)` must be answered by the
// statement right after it, returning through a class-40 classifier.
func TestPredictConflictsAnswersEveryQueryError(t *testing.T) {
	fset, file := parseDomainSource(t, "conflicts.go")
	fn := funcDeclNamed(t, fset, file, "PredictConflicts")

	sites, violations := scanQueryErrorHandling(fn, fset)
	for _, v := range violations {
		t.Errorf("conflicts.go:%d PredictConflicts: rows %q — %s\n"+
			"    A swallowed query error renders that rule as zero predictions inside a normal\n"+
			"    response — byte-identical to \"no conflicts\", which is the aihub#238 fake all-clear\n"+
			"    and the lying mode aihub#522 removed. Answer it like GetReadyQueue's segments do:\n"+
			"        if err != nil {\n"+
			"            return nil, dbErrCause(err, \"failed to query <rule>\")\n"+
			"        }",
			v.Line, v.Rows, v.Why)
	}

	// The count arm: rules 2-6 plus will_unlock. A detector that stopped
	// recognising pool.Query would report zero violations on any tree.
	const wantSites = 6
	if sites != wantSites {
		t.Errorf("scanner found %d Query sites in PredictConflicts, want %d.\n"+
			"    More: a rule was added — guard its error the same way and raise this number here.\n"+
			"    Fewer: a rule was removed, or a site changed to a shape this scanner cannot see\n"+
			"    (an if-init assignment, a helper wrapping the call) — which reads as compliant, so\n"+
			"    check which before touching this number.", sites, wantSites)
	}
}

// TestClaimAnswersItsQueryError is the same scanner over FnClaimWorkItem, whose
// one two-value Query site is the idempotent re-claim's lock re-query — the
// query that exists to remove G3's phantom empty AcquiredLocks, and that used to
// reproduce the phantom itself whenever the send failed.
func TestClaimAnswersItsQueryError(t *testing.T) {
	fset, file := parseDomainSource(t, "run_attempts.go")
	fn := funcDeclNamed(t, fset, file, "FnClaimWorkItem")

	sites, violations := scanQueryErrorHandling(fn, fset)
	for _, v := range violations {
		t.Errorf("run_attempts.go:%d FnClaimWorkItem: rows %q — %s", v.Line, v.Rows, v.Why)
	}
	const wantSites = 1
	if sites != wantSites {
		t.Errorf("scanner found %d Query sites in FnClaimWorkItem, want %d — see the count-arm note "+
			"on TestPredictConflictsAnswersEveryQueryError before touching this number.", sites, wantSites)
	}
}

// queryRowSite is one `err := X.QueryRow(…).Scan(…)` assignment.
type queryRowSite struct {
	Line int
	Err  string
	Why  string
}

// scanQueryRowErrorHandling is the QueryRow twin of scanQueryErrorHandling.
//
// The shape it accepts: a single-identifier assignment from `.QueryRow(…).Scan(…)`
// whose NEXT statement is an if that either
//
//   - tests `<err> != nil` somewhere in its condition (the `&& !errors.Is(err,
//     pgx.ErrNoRows)` refinements keep the ErrNoRows arm meaningful), or
//   - feeds <err> to a class-40 classifier in its init clause (the
//     `if aerr := retryConflictErr(err, …); aerr != nil` best-effort shape,
//     aihub#492's priorStepErr precedent),
//
// AND whose body ends in a return, AND which consults a classifier. Anything
// else — above all the census swallow, `if err == nil { … }` with no else —
// is a violation.
//
// Stated blind spot, deliberate for the same reason scanQueryErrorHandling's
// "immediately" rule exists: an assignment buried in an if's INIT clause
// (`if e := X.QueryRow(…).Scan(…); e == nil {`) is not counted at all. That is
// what the per-function site-count arms are for — a site moving into the blind
// spot changes the count and goes red, instead of reading as compliant.
func scanQueryRowErrorHandling(fn *ast.FuncDecl, fset *token.FileSet) (sites int, violations []queryRowSite) {
	var walk func(stmts []ast.Stmt)
	walk = func(stmts []ast.Stmt) {
		for i, s := range stmts {
			switch n := s.(type) {
			case *ast.IfStmt:
				walk(n.Body.List)
				for e := n.Else; e != nil; {
					switch el := e.(type) {
					case *ast.BlockStmt:
						walk(el.List)
						e = nil
					case *ast.IfStmt:
						walk(el.Body.List)
						e = el.Else
					default:
						e = nil
					}
				}
			case *ast.ForStmt:
				walk(n.Body.List)
			case *ast.RangeStmt:
				walk(n.Body.List)
			case *ast.BlockStmt:
				walk(n.List)
			}

			as, ok := s.(*ast.AssignStmt)
			if !ok || len(as.Rhs) != 1 || len(as.Lhs) != 1 {
				continue
			}
			if !containsQueryRowScan(as.Rhs[0]) {
				continue
			}
			errIdent, ok := as.Lhs[0].(*ast.Ident)
			if !ok || errIdent.Name == "_" {
				continue
			}
			sites++
			line := fset.Position(as.Pos()).Line

			if i+1 >= len(stmts) {
				violations = append(violations, queryRowSite{line, errIdent.Name,
					"nothing follows the Scan, so " + errIdent.Name + " is discarded"})
				continue
			}
			ifs, ok := stmts[i+1].(*ast.IfStmt)
			if !ok {
				violations = append(violations, queryRowSite{line, errIdent.Name,
					"the next statement is not an `if`, so " + errIdent.Name + " is not answered"})
				continue
			}
			if !condTestsErrNotNil(ifs.Cond, errIdent.Name) && !initFeedsClassifier(ifs.Init, errIdent.Name) {
				violations = append(violations, queryRowSite{line, errIdent.Name,
					"the guard neither tests `" + errIdent.Name + " != nil` nor feeds " + errIdent.Name +
						" to a class-40 classifier in its init — `if " + errIdent.Name + " == nil { … }` " +
						"reads every failure as the empty answer, which is the census swallow itself"})
				continue
			}
			if !endsInReturn(ifs.Body.List) {
				violations = append(violations, queryRowSite{line, errIdent.Name,
					"the guard's branch does not return, so the failure falls through"})
				continue
			}
			if !callsClassifier(ifs) {
				violations = append(violations, queryRowSite{line, errIdent.Name,
					"the guard returns without consulting a class-40 classifier " +
						"(dbErr/dbErrCause/retryConflictErr/pgxErr), so a serialization rollback answers " +
						"500 where the neighbouring sites answer the retryable 409"})
			}
		}
	}
	walk(fn.Body.List)
	return sites, violations
}

// containsQueryRowScan reports whether e is a `.Scan(…)` call whose receiver
// chain contains a `.QueryRow(…)` call.
func containsQueryRowScan(e ast.Expr) bool {
	call, ok := e.(*ast.CallExpr)
	if !ok {
		return false
	}
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != "Scan" {
		return false
	}
	found := false
	ast.Inspect(sel.X, func(n ast.Node) bool {
		if inner, ok := n.(*ast.CallExpr); ok {
			if isel, ok := inner.Fun.(*ast.SelectorExpr); ok && isel.Sel.Name == "QueryRow" {
				found = true
			}
		}
		return !found
	})
	return found
}

// condTestsErrNotNil reports whether cond contains `<name> != nil` anywhere —
// so `err != nil && !errors.Is(err, pgx.ErrNoRows)` qualifies and `err == nil`
// does not.
func condTestsErrNotNil(cond ast.Expr, name string) bool {
	found := false
	ast.Inspect(cond, func(n ast.Node) bool {
		bin, ok := n.(*ast.BinaryExpr)
		if !ok || bin.Op != token.NEQ {
			return !found
		}
		x, xok := bin.X.(*ast.Ident)
		y, yok := bin.Y.(*ast.Ident)
		if xok && yok && ((x.Name == name && y.Name == "nil") || (x.Name == "nil" && y.Name == name)) {
			found = true
		}
		return !found
	})
	return found
}

// initFeedsClassifier reports whether the if's init clause calls one of the
// class-40 classifiers with <name> as its first argument.
func initFeedsClassifier(init ast.Stmt, name string) bool {
	if init == nil {
		return false
	}
	found := false
	ast.Inspect(init, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		id, ok := call.Fun.(*ast.Ident)
		if !ok || !conflictClassifiers[id.Name] || len(call.Args) == 0 {
			return true
		}
		if arg, ok := call.Args[0].(*ast.Ident); ok && arg.Name == name {
			found = true
		}
		return !found
	})
	return found
}

// TestPredictConflictsAnswersEveryQueryRowError covers the three QueryRow sites
// the census's two-value shape could not see: rule 1's lock probe, the
// id/project resolution, and the H7 visibility fold.
func TestPredictConflictsAnswersEveryQueryRowError(t *testing.T) {
	fset, file := parseDomainSource(t, "conflicts.go")
	fn := funcDeclNamed(t, fset, file, "PredictConflicts")

	sites, violations := scanQueryRowErrorHandling(fn, fset)
	for _, v := range violations {
		t.Errorf("conflicts.go:%d PredictConflicts: %s — %s", v.Line, v.Err, v.Why)
	}
	const wantSites = 3
	if sites != wantSites {
		t.Errorf("scanner found %d QueryRow sites in PredictConflicts, want %d (rule 1, the id/project "+
			"resolution, the H7 fold). Fewer may mean a site moved into the scanner's stated blind spot "+
			"(an if-init assignment), which reads as compliant — check before touching this number.",
			sites, wantSites)
	}
}

// TestClaimAnswersEveryQueryRowError covers FnClaimWorkItem's five QueryRow
// sites: the FOR UPDATE row load, the idempotency probe, the idempotent-path
// step-state read, the current-attempt load that gates takeover-versus-409, and
// the fresh-path prior-step read.
func TestClaimAnswersEveryQueryRowError(t *testing.T) {
	fset, file := parseDomainSource(t, "run_attempts.go")
	fn := funcDeclNamed(t, fset, file, "FnClaimWorkItem")

	sites, violations := scanQueryRowErrorHandling(fn, fset)
	for _, v := range violations {
		t.Errorf("run_attempts.go:%d FnClaimWorkItem: %s — %s", v.Line, v.Err, v.Why)
	}
	const wantSites = 5
	if sites != wantSites {
		t.Errorf("scanner found %d QueryRow sites in FnClaimWorkItem, want %d — see "+
			"TestPredictConflictsAnswersEveryQueryRowError's count-arm note.", sites, wantSites)
	}
}

// TestQueryRowScannerRejectsTheShapeItWasWrittenFor is the calibration arm: the
// scanner must be RED on the pre-aihub#522 shapes, or it is measuring nothing.
func TestQueryRowScannerRejectsTheShapeItWasWrittenFor(t *testing.T) {
	cases := []struct {
		name string
		src  string
		want string
	}{{
		name: "the census swallow: if err == nil with no else",
		src: `func f() {
			err := pool.QueryRow(ctx, "SELECT 1").Scan(&x)
			if err == nil {
				use(x)
			}
		}`,
		want: "census swallow",
	}, {
		name: "error never consulted at all",
		src: `func f() {
			err := pool.QueryRow(ctx, "SELECT 1").Scan(&x)
			use(x)
			_ = err
		}`,
		want: "not an `if`",
	}, {
		name: "guarded but falls through instead of returning",
		src: `func f() {
			err := pool.QueryRow(ctx, "SELECT 1").Scan(&x)
			if err != nil {
				log.Print(err)
			}
		}`,
		want: "does not return",
	}, {
		name: "guarded and returns, but bare NewErr instead of the classifier",
		src: `func f() {
			err := pool.QueryRow(ctx, "SELECT 1").Scan(&x)
			if err != nil {
				return nil, NewErr(ErrInternalError, "failed")
			}
		}`,
		want: "class-40 classifier",
	}}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fset, fn := parseFuncFixture(t, tc.src)
			sites, violations := scanQueryRowErrorHandling(fn, fset)
			if sites != 1 {
				t.Fatalf("fixture should hold exactly 1 QueryRow site, scanner saw %d", sites)
			}
			if len(violations) != 1 {
				t.Fatalf("scanner accepted a shape aihub#522 exists to reject: got %d violations, want 1",
					len(violations))
			}
			if !strings.Contains(violations[0].Why, tc.want) {
				t.Errorf("violation reason = %q, want it to mention %q", violations[0].Why, tc.want)
			}
		})
	}
}

// TestQueryRowScannerAcceptsTheFixedShapes is the other half: both compliant
// shapes — the ErrNoRows-preserving guard and the aihub#492 best-effort
// classifier init — must be green, and the stated blind spot must read as zero
// sites rather than as a pass.
func TestQueryRowScannerAcceptsTheFixedShapes(t *testing.T) {
	for _, tc := range []struct {
		name string
		src  string
	}{{
		name: "ErrNoRows-preserving guard",
		src: `func f() {
			err := pool.QueryRow(ctx, "SELECT 1").Scan(&x)
			if err != nil && !errors.Is(err, pgx.ErrNoRows) {
				return nil, dbErrCause(err, "failed")
			}
			if err == nil {
				use(x)
			}
		}`,
	}, {
		name: "aihub#492 best-effort classifier init",
		src: `func f() {
			stepErr := pool.QueryRow(ctx, "SELECT 1").Scan(&x)
			if aerr := retryConflictErr(stepErr, "failed"); aerr != nil {
				return nil, aerr
			}
			if stepErr == nil {
				use(x)
			}
		}`,
	}} {
		t.Run(tc.name, func(t *testing.T) {
			fset, fn := parseFuncFixture(t, tc.src)
			sites, violations := scanQueryRowErrorHandling(fn, fset)
			if sites != 1 {
				t.Fatalf("fixture should hold exactly 1 QueryRow site, scanner saw %d", sites)
			}
			if len(violations) != 0 {
				t.Errorf("scanner reported the compliant shape as a violation: %+v", violations)
			}
		})
	}

	t.Run("the stated blind spot counts zero sites, not a pass", func(t *testing.T) {
		fset, fn := parseFuncFixture(t, `func f() {
			if e := pool.QueryRow(ctx, "SELECT 1").Scan(&x); e == nil {
				use(x)
			}
		}`)
		sites, violations := scanQueryRowErrorHandling(fn, fset)
		if sites != 0 || len(violations) != 0 {
			t.Errorf("the if-init shape should be invisible (sites=0) — it is held by the per-function "+
				"count arms instead; got sites=%d violations=%+v", sites, violations)
		}
	})
}

// TestPredictConflictsHasNoLogAndContinuePath pins the rows.Err() half of the
// aihub#522 fix. aihub#382/#386 put `fmt.Fprintf(os.Stderr, …)` on every rule's
// drain, which made execute-time failures visible to an OPERATOR while the
// CALLER still received the fake all-clear — predictions built from a partial
// scan inside a normal response. All six logs were upgraded to returns, and
// this arm holds the function to zero log-and-continue paths: the scanners
// above check the send-time guard, but none of them can see a rows.Err()
// branch quietly downgraded back to a log.
func TestPredictConflictsHasNoLogAndContinuePath(t *testing.T) {
	fset, file := parseDomainSource(t, "conflicts.go")
	fn := funcDeclNamed(t, fset, file, "PredictConflicts")

	ast.Inspect(fn.Body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		if sel, ok := call.Fun.(*ast.SelectorExpr); ok &&
			(sel.Sel.Name == "Fprintf" || sel.Sel.Name == "Printf" || sel.Sel.Name == "Print" || sel.Sel.Name == "Println") {
			t.Errorf("PredictConflicts logs at %v. A log-and-continue error path answers the caller with "+
				"a truncated prediction set inside a normal response — the fake all-clear aihub#522 "+
				"removed. Return dbErrCause(err, …) instead; if some condition genuinely must not fail "+
				"the call, bring a ruling and record it here.", fset.Position(call.Pos()))
		}
		return true
	})
}

// TestDedupAndListProjectsClassifyTheirSendTimeErrors pins the two census
// entries that turned out NOT to be swallows: checkDedup and ListProjects each
// check their Query error — the census's "immediately after" shape just cannot
// see a guard that sits after an if/else assignment split, which is exactly why
// those four entries were flagged as possible false positives.
//
// What IS pinned, because aihub#522 changed it:
//
//   - checkDedup's discard stays deliberate best-effort, but now carries the
//     class-40 carve-out: a 40001/40P01 has already killed the transaction
//     CreateWorkItem is holding, so "allow creation" was never a real fallback
//     there — the caller just got an unclassifiable 500 at a later statement
//     (aihub#492 / bestEffortExec's reasoning, and the same carve-out its own
//     rows.Err() arm has carried since aihub#334).
//   - ListProjects' send-time arm answers through dbErrCause, as its own
//     rows.Err() arm already did — byte-identical for every non-class-40 error.
//
// The arm reads the FIRST top-level if after the last .Query call in each
// function (the guard both functions place after their if/else assignment) and
// requires it to test the error and consult a classifier.
func TestDedupAndListProjectsClassifyTheirSendTimeErrors(t *testing.T) {
	for _, target := range []struct{ file, fn string }{
		{"work_items.go", "checkDedup"},
		{"projects.go", "ListProjects"},
	} {
		t.Run(target.fn, func(t *testing.T) {
			fset, file := parseDomainSource(t, target.file)
			fn := funcDeclNamed(t, fset, file, target.fn)

			var lastQuery token.Pos
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				if call, ok := n.(*ast.CallExpr); ok {
					if sel, ok := call.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "Query" && call.Pos() > lastQuery {
						lastQuery = call.Pos()
					}
				}
				return true
			})
			if lastQuery == token.NoPos {
				t.Fatalf("%s holds no .Query call any more — this arm checks nothing; re-point it", target.fn)
			}

			var guard *ast.IfStmt
			for _, s := range fn.Body.List {
				ifs, ok := s.(*ast.IfStmt)
				if ok && ifs.Pos() > lastQuery {
					guard = ifs
					break
				}
			}
			if guard == nil {
				t.Fatalf("%s has no top-level if after its queries — the send-time guard is gone, so a "+
					"failed query reads as zero candidates", target.fn)
			}
			if !condTestsErrNotNil(guard.Cond, "err") {
				t.Fatalf("%s's first if after the queries does not test `err != nil` (found at %v) — the "+
					"send-time guard moved or was rewritten; re-point this arm at it",
					target.fn, fset.Position(guard.Pos()))
			}
			if !callsClassifier(guard) {
				t.Errorf("%s's send-time error branch (%v) no longer consults a class-40 classifier "+
					"(dbErr/dbErrCause/retryConflictErr). For checkDedup that re-opens aihub#492's hole — a "+
					"serialization rollback is discarded as \"best effort\" although it has already killed "+
					"the caller's transaction; for ListProjects it makes the send-time arm answer 500 where "+
					"its own rows.Err() arm answers the retryable 409.",
					target.fn, fset.Position(guard.Pos()))
			}
		})
	}
}

// TestPredictConflictsSurfacesQueryFailure is the behavioural arm: against a
// pool whose connections cannot be established, PredictConflicts must FAIL,
// not answer {"predictions":[],"severity":"info"}. Before aihub#522 all three
// requests below returned that byte-identical fake all-clear.
//
// No database: the pool points at a closed port and pgxpool connects lazily, so
// the first acquire fails at dial time — which is exactly the send-time error
// class the census named.
func TestPredictConflictsSurfacesQueryFailure(t *testing.T) {
	pool, err := pgxpool.New(context.Background(), "postgres://u:p@127.0.0.1:1/db?connect_timeout=1")
	if err != nil {
		t.Fatalf("pgxpool.New should only parse the config, got: %v", err)
	}
	defer pool.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	wiRef := "wi_nonexistent"
	cases := []struct {
		name    string
		req     *PredictConflictsRequest
		wantMsg string // pins WHICH site answered, so restoring one swallow goes red
	}{{
		name: "rule 2's Query (the census shape)",
		req: &PredictConflictsRequest{
			DeclaredResources: []byte(`[{"type":"repo","uri":"repo:x"}]`),
		},
		wantMsg: "rule 2",
	}, {
		name: "rule 1's QueryRow (the hard gate, census-blind shape)",
		req: &PredictConflictsRequest{
			Project:           "p",
			DeclaredResources: []byte(`[{"type":"path","uri":"file:a.go","intent":"write"}]`),
		},
		wantMsg: "rule 1",
	}, {
		name: "the id/project resolution QueryRow",
		req: &PredictConflictsRequest{
			WorkItemID: &wiRef,
		},
		wantMsg: "resolve the work item",
	}}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			result, aerr := PredictConflicts(ctx, pool, tc.req, nil)
			if aerr == nil {
				t.Fatalf("PredictConflicts answered %+v with a nil error against an unreachable pool — "+
					"the aihub#238 fake all-clear aihub#522 removed", result)
			}
			if result != nil {
				t.Errorf("a failed predict must not also hand back a result, got %+v", result)
			}
			if !strings.Contains(aerr.Message, tc.wantMsg) {
				t.Errorf("error %q does not name the site (%q) — the failure was surfaced by a LATER "+
					"query, which means this site's own guard is gone", aerr.Message, tc.wantMsg)
			}
		})
	}
}
