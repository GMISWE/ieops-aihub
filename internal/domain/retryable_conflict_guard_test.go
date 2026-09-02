package domain

// aihub#334: the structural guard that keeps SQLSTATE class 40 from silently
// growing back into 500s.
//
// The defect this repo kept re-finding is not one bug, it is a shape: a DB
// error is turned into ErrInternalError without anyone asking whether Postgres
// was reporting "the server is broken" or "your transaction lost a race, run it
// again". Three instances were fixed by hand (UpdateProject's row lock,
// FnCompleteAttempt's unblock sweep, Remember's supersede). Roughly a hundred
// other bare wrappings exist. Editing all of them would be treating the class as
// N instances, and would do nothing about instance N+1 written tomorrow.
//
// So the fix is one classifier (retryConflictErr) plus this guard, which is red
// whenever a NEW bare wrapping appears on a path that runs inside a Postgres
// transaction.
//
// ── What it checks, and why those two rules ─────────────────────────────────
//
// Rule 1 (bare wrapping). Inside any function that can execute statements
// within a transaction, an error branch that produces NewErr(ErrInternalError,
// …) must consult a classifier in the same branch. Concretely, this is red for:
//
//	if err := tx.Commit(ctx); err != nil {
//	    return NewErr(ErrInternalError, "failed to commit the new thing")
//	}
//
// which is exactly the shape someone adds when they write the next
// transactional endpoint, and exactly the shape all three known instances had.
//
// Rule 2 (unasked-for error). pgx's extended-protocol Query is lazy: it returns
// a Rows with a nil error, and a server-side failure only materialises while the
// result set is drained, reachable ONLY through rows.Err(). A loop that never
// calls it does not discard an error — it never obtains one, so no
// classifier, however central, can ever see it. That is what made instance 3
// (unblockDependentWI) survive the classifier being wired into every
// *pgconn.PgError-shaped hop in the package: measured, it still returned
//
//	500 INTERNAL_ERROR  "failed to commit complete_attempt"
//
// with no SQLSTATE anywhere, because by then pgx was reporting
// pgx.ErrTxCommitRollback. Rule 1 alone cannot see that hop, because that hop
// wraps nothing. Both rules are needed and neither implies the other.
//
// ── Why it is scoped to transactional functions ────────────────────────────
//
// Not because that is where the fix happened to land — that scoping ("gate what
// I touched") is how a class gate quietly stops covering the class. It is
// because class 40 is a property of transactions: 40001 is raised when a
// transaction's snapshot is invalidated, 40P01 when it is chosen to break a
// lock cycle. A statement that never runs inside one cannot produce either.
//
// ── Residual gap, stated rather than implied ───────────────────────────────
//
// A function that opens no transaction, takes no pgx.Tx and no Querier, but is
// CALLED with a tx by a transactional caller, is not covered: this guard does no
// interprocedural analysis. Nothing in internal/domain is shaped that way today
// (every tx-taking helper names the type in its signature, which is what makes
// the syntactic test sufficient), and if one appears the honest fix is to widen
// the detector here, not to add an exemption.

import (
	"go/ast"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// conflictClassifiers are the functions that map a live DB error onto the
// retryable-conflict AihubError. An error branch that calls one of these has
// asked the question; one that does not has assumed the answer.
var conflictClassifiers = map[string]bool{
	"retryConflictErr": true,
	"dbErr":            true,
	"dbErrCause":       true,
	"pgxErr":           true,
}

// txAwareParamTypes are the parameter types that let a function run statements
// inside somebody else's transaction. Querier is this package's own interface,
// satisfied by both *pgxpool.Pool and pgx.Tx, so a Querier-taking function is
// transactional whenever its caller passes the tx — which is precisely what
// Remember's supersede path does.
var txAwareParamTypes = map[string]bool{
	"pgx.Tx":  true,
	"Querier": true,
}

// conflictGuardExemptions lists the sites deliberately left unclassified.
//
// It lives inside the guard, not in an annotation next to the code, on purpose:
// the cheapest way to silence a gate must never be cheaper than obeying it. An
// exemption costs an edit to this file and a written reason; classifying the
// error costs three lines where you already are.
//
// The key is "<file>:<func>:<the NewErr call, whitespace-collapsed>", which
// survives every edit above it — a line number would let an unrelated insertion
// move a live violation into an exemption's slot.
//
// A stale entry is an error too (see the assertion at the end of Rule 1): an
// exemption whose site no longer exists, or which now classifies, must be
// deleted rather than left behind to pre-authorise a future violation that
// happens to land on the same key.
// It is EMPTY, and that is the intended steady state: every site the rule
// reports has been classified rather than excused. The one entry it briefly
// held (gc.go's "failed to begin unblock tx") was deleted once the detector
// learned that a pool.Begin failure is not a class 40 candidate — the general
// rule was the right home for it, and the stale-entry assertion below is what
// pointed that out rather than leaving it to rot in here.
var conflictGuardExemptions = map[string]string{}

// TestNoUnclassifiedTransactionalInternalErrors is Rule 1.
func TestNoUnclassifiedTransactionalInternalErrors(t *testing.T) {
	files := domainSourceFiles(t)
	seenExemptions := map[string]bool{}
	var violations []string
	transactionalFuncs := 0
	checkedSites := 0

	for _, path := range files {
		fset, file := parseDomainSource(t, path)
		base := filepath.Base(path)
		forEachFunc(file, func(fn *ast.FuncDecl, name string) {
			if !isTransactional(t, fset, fn) {
				return
			}
			transactionalFuncs++
			checkedSites += countDBErrorBranches(fn)
			for _, site := range internalErrorSites(t, fset, fn) {
				key := base + ":" + name + ":" + site.rendered
				if reason, ok := conflictGuardExemptions[key]; ok {
					seenExemptions[key] = true
					if site.classified {
						violations = append(violations, key+
							"\n    is exempted but now DOES classify. Delete the exemption; leaving it "+
							"behind silently pre-approves the next violation that lands on this key.\n    reason on file: "+reason)
					}
					continue
				}
				if site.classified {
					continue
				}
				violations = append(violations, fset.Position(site.pos).String()+
					"\n    in "+base+":"+name+" — "+site.rendered+
					"\n    This runs inside a Postgres transaction, so the error it is wrapping may be "+
					"SQLSTATE 40001/40P01: a lost concurrency race, which the caller fixes by retrying. "+
					"ErrInternalError tells them the opposite. Consult the classifier first:\n"+
					"        if aerr := retryConflictErr(err, \"<the hop that failed>\"); aerr != nil {\n"+
					"            return aerr\n        }")
			}
		})
	}

	// Anti-vacuity. Without these, deleting the detector — or renaming Querier,
	// or changing the pool API — turns this test into a permanent green that
	// checks nothing.
	//
	// The population counted here is DB ERROR BRANCHES, not unclassified ones.
	// An earlier draft of this guard counted the NewErr(ErrInternalError, …)
	// sites it was about to reject, and the floor therefore fell below itself
	// the moment the violations were fixed: the number a gate uses to prove it
	// is still looking must not be the number the gate exists to drive to zero.
	// Floors rather than exact counts, so an ordinary refactor does not have to
	// edit this file; measured on the aihub#334 branch at 43 transactional
	// functions and 156 DB error branches.
	if transactionalFuncs < 20 {
		t.Fatalf("only %d transactional functions found in internal/domain — the detector has stopped "+
			"recognising them (renamed Querier? changed pool API?), so every assertion below is vacuous",
			transactionalFuncs)
	}
	if checkedSites < 80 {
		t.Fatalf("only %d DB error branches found inside transactional functions — the branch finder "+
			"has stopped matching, so this guard is inert", checkedSites)
	}

	for key, reason := range conflictGuardExemptions {
		if !seenExemptions[key] {
			violations = append(violations, key+
				"\n    is exempted but no such site exists any more. Delete the entry — a stale exemption "+
				"pre-authorises whatever moves into its place.\n    reason on file: "+reason)
		}
	}

	if len(violations) > 0 {
		sort.Strings(violations)
		t.Fatalf("%d transactional error path(s) wrap a DB error as ErrInternalError without asking "+
			"whether Postgres said \"retry\" (aihub#334):\n\n%s\n",
			len(violations), strings.Join(violations, "\n\n"))
	}
}

// TestQueryResultsAreDrainedForErrors is Rule 2.
//
// pgx.Rows.Err() is the ONLY route by which a failure raised while streaming a
// result set can leave the code. Not calling it is not a swallow that a
// central classifier could still catch downstream: there is no error value at
// all, the loop simply sees zero rows, and the transaction's death is not
// discovered until commit, where pgx reports ErrTxCommitRollback — no SQLSTATE,
// no PgError, nothing left to classify.
func TestQueryResultsAreDrainedForErrors(t *testing.T) {
	files := domainSourceFiles(t)
	var violations []string
	checked := 0

	for _, path := range files {
		fset, file := parseDomainSource(t, path)
		base := filepath.Base(path)
		forEachFunc(file, func(fn *ast.FuncDecl, name string) {
			// Same scoping as Rule 1, and for the same reason: an undrained
			// error on a pool query outside any transaction is a real bug, but
			// it is not THIS class, and there are 20-odd of them. Closing that
			// wider set is tracked separately rather than smuggled in here.
			if !isTransactional(t, fset, fn) {
				return
			}
			for _, rowsVar := range queryRowsVars(fn) {
				checked++
				if identCalledWithMethod(fn.Body, rowsVar.name, "Err") {
					continue
				}
				violations = append(violations, fset.Position(rowsVar.pos).String()+
					"\n    in "+base+":"+name+" — the rows from "+rowsVar.call+" are iterated but "+
					rowsVar.name+".Err() is never called.\n"+
					"    pgx's Query is lazy: a server-side failure (including SQLSTATE 40001/40P01, which "+
					"kills the whole transaction) surfaces only here. Without it the loop looks empty, the "+
					"caller commits, and the failure reappears as pgx.ErrTxCommitRollback with no SQLSTATE "+
					"left to classify — measured on aihub#334 instance 3.\n"+
					"    Add after the loop:\n"+
					"        if err := "+rowsVar.name+".Err(); err != nil { ... }")
			}
		})
	}

	if checked < 3 {
		t.Fatalf("only %d iterated Query result set(s) found in internal/domain — the detector has "+
			"stopped matching, so this guard is inert", checked)
	}
	if len(violations) > 0 {
		sort.Strings(violations)
		t.Fatalf("%d result set(s) iterated without checking rows.Err() (aihub#334):\n\n%s\n",
			len(violations), strings.Join(violations, "\n\n"))
	}
}

// ── detector plumbing ───────────────────────────────────────────────────────

// domainSourceFiles lists the package's non-test .go files. It fails rather
// than returning an empty slice, so a wrong working directory cannot pass as
// "nothing to check".
func domainSourceFiles(t *testing.T) []string {
	t.Helper()
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read package dir: %v", err)
	}
	var out []string
	for _, e := range entries {
		n := e.Name()
		if e.IsDir() || !strings.HasSuffix(n, ".go") || strings.HasSuffix(n, "_test.go") {
			continue
		}
		out = append(out, n)
	}
	if len(out) < 5 {
		t.Fatalf("found %d non-test .go files in the package directory; expected the whole domain "+
			"package. This guard is not looking at the code it claims to guard.", len(out))
	}
	sort.Strings(out)
	return out
}

// forEachFunc visits every function and method with a body, naming methods
// "(Recv).Name" so guard output points at a unique declaration.
func forEachFunc(file *ast.File, visit func(fn *ast.FuncDecl, name string)) {
	for _, d := range file.Decls {
		fn, ok := d.(*ast.FuncDecl)
		if !ok || fn.Body == nil {
			continue
		}
		name := fn.Name.Name
		if fn.Recv != nil && len(fn.Recv.List) > 0 {
			name = "(" + exprString(fn.Recv.List[0].Type) + ")." + name
		}
		visit(fn, name)
	}
}

// isTransactional reports whether statements in fn can run inside a Postgres
// transaction: it either starts one, or is handed one.
func isTransactional(t *testing.T, fset *token.FileSet, fn *ast.FuncDecl) bool {
	t.Helper()
	if fn.Type.Params != nil {
		for _, p := range fn.Type.Params.List {
			if txAwareParamTypes[exprString(p.Type)] {
				return true
			}
		}
	}
	found := false
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		if sel, ok := call.Fun.(*ast.SelectorExpr); ok {
			if sel.Sel.Name == "Begin" || sel.Sel.Name == "BeginTx" {
				found = true
			}
		}
		return !found
	})
	return found
}

// countDBErrorBranches counts the error branches in fn whose error visibly came
// from a pgx call and which are not guarding a transaction's own start. This is
// the population Rule 1 judges — every one of these branches either classifies
// or is a violation — and unlike the violation count it does not move when the
// violations are fixed, which is what makes it usable as an anti-vacuity floor.
func countDBErrorBranches(fn *ast.FuncDecl) int {
	n := 0
	var stack []ast.Node
	ast.Inspect(fn.Body, func(node ast.Node) bool {
		if node == nil {
			stack = stack[:len(stack)-1]
			return true
		}
		defer func() { stack = append(stack, node) }()
		ifs, ok := node.(*ast.IfStmt)
		if !ok || !condMentionsError(ifs) {
			return true
		}
		b := errorBranch{ifs: ifs}
		if len(stack) > 0 {
			if blk, ok := stack[len(stack)-1].(*ast.BlockStmt); ok {
				for j, s := range blk.List {
					if s == ast.Stmt(ifs) && j > 0 {
						b.prev = blk.List[j-1]
					}
				}
			}
		}
		if branchTestsDBCall(b) && !branchTestsTransactionStart(b) {
			n++
		}
		return true
	})
	return n
}

type internalErrSite struct {
	pos        token.Pos
	rendered   string
	classified bool
}

// internalErrorSites finds every NewErr/NewErrDetails call in fn whose code
// argument is ErrInternalError AND which sits in an error branch, and reports
// whether that branch consults a classifier.
//
// The error-branch restriction exists because ErrInternalError is also used for
// genuine "this cannot happen" invariants (a nil map, an unreachable switch
// default) that never touch Postgres. Requiring those to consult a SQLSTATE
// classifier would be noise, and noise is how a guard gets disabled.
func internalErrorSites(t *testing.T, fset *token.FileSet, fn *ast.FuncDecl) []internalErrSite {
	t.Helper()
	var out []internalErrSite
	// Stack of enclosing nodes, so each hit can look at the branch it is in.
	var stack []ast.Node
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		if n == nil {
			stack = stack[:len(stack)-1]
			return true
		}
		defer func() { stack = append(stack, n) }()

		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		id, ok := call.Fun.(*ast.Ident)
		if !ok || (id.Name != "NewErr" && id.Name != "NewErrDetails") {
			return true
		}
		if len(call.Args) == 0 {
			return true
		}
		code, ok := call.Args[0].(*ast.Ident)
		if !ok || code.Name != "ErrInternalError" {
			return true
		}
		branch, ok := enclosingErrorBranch(stack)
		if !ok || branchTestsTransactionStart(branch) || !branchTestsDBCall(branch) {
			return true
		}
		out = append(out, internalErrSite{
			pos:        call.Lparen,
			rendered:   renderNode(t, fset, call),
			classified: callsClassifier(branch.ifs),
		})
		return true
	})
	return out
}

// errorBranch is an `if <something>err<something> { ... }` that encloses a
// NewErr site, together with the statement immediately before it in its own
// block — which is where the error being tested was usually produced.
type errorBranch struct {
	ifs  *ast.IfStmt
	prev ast.Stmt
}

// enclosingErrorBranch walks outward from a NewErr site to the nearest if
// statement whose condition or init mentions an error-looking identifier.
// ok=false means the site is not in an error branch at all.
func enclosingErrorBranch(stack []ast.Node) (errorBranch, bool) {
	for i := len(stack) - 1; i >= 0; i-- {
		ifs, ok := stack[i].(*ast.IfStmt)
		if !ok || !condMentionsError(ifs) {
			continue
		}
		b := errorBranch{ifs: ifs}
		if i > 0 {
			if blk, ok := stack[i-1].(*ast.BlockStmt); ok {
				for j, s := range blk.List {
					if s == ast.Stmt(ifs) && j > 0 {
						b.prev = blk.List[j-1]
					}
				}
			}
		}
		return b, true
	}
	return errorBranch{}, false
}

// branchTestsTransactionStart reports whether an error branch is guarding
// pool.Begin / pool.BeginTx. Those are excluded on principle, not for
// convenience: class 40 is raised when a transaction's snapshot is invalidated
// or when it is chosen to break a lock cycle, and at BEGIN there is no
// transaction yet to do either to. A failure there is connection acquisition,
// which really is a server-side problem and really is a 500.
//
// This is a rule of the detector rather than an exemption entry so that the
// next `pool.Begin` written anywhere in the package inherits it without anyone
// having to notice — an exemption list that has to grow by one per new
// transaction is a list that will be wrong.
func branchTestsTransactionStart(b errorBranch) bool {
	if beginCallIn(b.ifs.Init) || beginCallIn(b.ifs.Cond) {
		return true // `if tx, err := pool.Begin(ctx); err != nil {`
	}
	// `tx, err := pool.Begin(ctx)` on its own line, then `if err != nil {`.
	// Positional rather than name-based: `err` is reused throughout these
	// functions, so "this branch tests a variable that was assigned from Begin
	// somewhere in this function" would exempt every error branch in the file.
	if b.prev == nil {
		return false
	}
	as, ok := b.prev.(*ast.AssignStmt)
	if !ok {
		return false
	}
	return beginCallIn(as)
}

// dbCallMethods are the pgx methods that can hand back a *pgconn.PgError.
// Non-DB failures inside a transactional function — a json.Unmarshal, a
// rand.Read, a hex decode — are none of this guard's business, and demanding a
// SQLSTATE classifier on them would be the noise that gets a guard switched off.
var dbCallMethods = map[string]bool{
	"Query": true, "QueryRow": true, "Exec": true, "Scan": true,
	"SendBatch": true, "CopyFrom": true, "Commit": true,
}

// branchTestsDBCall reports whether the error this branch is testing visibly
// came out of a pgx call — either in the if's own init clause, or in the
// statement immediately before it.
//
// Syntactic and local on purpose: this guard is a test, not a type checker, and
// a local rule is one a reader can apply themselves at the moment they write
// the code. The cost is false NEGATIVES where the DB call is several statements
// above its error check; those sites are not gated. That is a stated gap, not a
// silent one, and it is the safe direction — a guard that fires on
// json.Unmarshal teaches people to reach for the exemption list.
func branchTestsDBCall(b errorBranch) bool {
	if dbCallIn(b.ifs.Init) || dbCallIn(b.ifs.Cond) {
		return true
	}
	return b.prev != nil && dbCallIn(b.prev)
}

// dbCallIn reports whether n contains a call to one of dbCallMethods.
func dbCallIn(n ast.Node) bool {
	if n == nil {
		return false
	}
	found := false
	ast.Inspect(n, func(m ast.Node) bool {
		call, ok := m.(*ast.CallExpr)
		if !ok {
			return true
		}
		if sel, ok := call.Fun.(*ast.SelectorExpr); ok && dbCallMethods[sel.Sel.Name] {
			found = true
		}
		return !found
	})
	return found
}

// beginCallIn reports whether n contains a call to .Begin / .BeginTx.
func beginCallIn(n ast.Node) bool {
	if n == nil {
		return false
	}
	found := false
	ast.Inspect(n, func(m ast.Node) bool {
		call, ok := m.(*ast.CallExpr)
		if !ok {
			return true
		}
		if sel, ok := call.Fun.(*ast.SelectorExpr); ok &&
			(sel.Sel.Name == "Begin" || sel.Sel.Name == "BeginTx") {
			found = true
		}
		return !found
	})
	return found
}

// condMentionsError reports whether an if statement is testing an error,
// looking at both its condition and its init clause: `if err != nil`,
// `if err := tx.Commit(ctx); err != nil` and `if scanErr != nil` all qualify.
func condMentionsError(ifs *ast.IfStmt) bool {
	found := false
	inspect := func(n ast.Node) {
		if n == nil {
			return
		}
		ast.Inspect(n, func(m ast.Node) bool {
			if id, ok := m.(*ast.Ident); ok && strings.Contains(strings.ToLower(id.Name), "err") {
				found = true
			}
			return !found
		})
	}
	inspect(ifs.Cond)
	if ifs.Init != nil {
		inspect(ifs.Init)
	}
	return found
}

// callsClassifier reports whether the branch consults the class-40 classifier
// anywhere within it, including inside a nested if — which is the shape the fix
// actually takes.
func callsClassifier(branch ast.Node) bool {
	found := false
	ast.Inspect(branch, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		if id, ok := call.Fun.(*ast.Ident); ok && conflictClassifiers[id.Name] {
			found = true
		}
		return !found
	})
	return found
}

type queryRowsVar struct {
	name string
	call string
	pos  token.Pos
}

// queryRowsVars finds `rows, err := <x>.Query(...)` assignments in fn whose
// result is subsequently iterated with rows.Next(). Only iterated result sets
// are reported: a Rows handed straight to a collector (pgx.CollectRows and
// friends) has its Err checked inside that collector.
func queryRowsVars(fn *ast.FuncDecl) []queryRowsVar {
	var out []queryRowsVar
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		as, ok := n.(*ast.AssignStmt)
		if !ok || len(as.Lhs) != 2 || len(as.Rhs) != 1 {
			return true
		}
		call, ok := as.Rhs[0].(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != "Query" {
			return true
		}
		id, ok := as.Lhs[0].(*ast.Ident)
		if !ok || id.Name == "_" {
			return true
		}
		if !identCalledWithMethod(fn.Body, id.Name, "Next") {
			return true
		}
		out = append(out, queryRowsVar{
			name: id.Name,
			call: exprString(sel) + "(...)",
			pos:  call.Lparen,
		})
		return true
	})
	return out
}

// identCalledWithMethod reports whether `<name>.<method>(...)` appears in body.
func identCalledWithMethod(body ast.Node, name, method string) bool {
	found := false
	ast.Inspect(body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != method {
			return true
		}
		if recv, ok := sel.X.(*ast.Ident); ok && recv.Name == name {
			found = true
		}
		return !found
	})
	return found
}

// exprString renders a type or selector expression without needing a FileSet,
// for the handful of shapes that appear in parameter lists and receivers.
func exprString(e ast.Expr) string {
	switch v := e.(type) {
	case *ast.Ident:
		return v.Name
	case *ast.StarExpr:
		return "*" + exprString(v.X)
	case *ast.SelectorExpr:
		return exprString(v.X) + "." + v.Sel.Name
	case *ast.ArrayType:
		return "[]" + exprString(v.Elt)
	case *ast.IndexExpr:
		return exprString(v.X)
	default:
		return ""
	}
}
