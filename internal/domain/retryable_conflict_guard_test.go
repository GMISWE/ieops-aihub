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

// class40DiscardJustifications lists the discard sites Rule 3 accepts, each
// with the reason the discard cannot swallow a class-40 rollback. Same
// mechanics and same philosophy as conflictGuardExemptions: the entry lives
// HERE, not in a `//nolint` next to the code — a bare nolint silences the
// linter, it does not answer the question this rule asks — and a stale entry
// is itself a violation, so deleting a justified site (or fixing it) forces
// this map to shrink with it. The key is "<file>:<func>:<the discarded call,
// whitespace-collapsed>".
var class40DiscardJustifications = map[string]string{
	"dependencies.go:emitWIUnblockedEvent:tx.Exec(ctx, `SAVEPOINT bp_wi_unblocked`)": "SAVEPOINT takes no snapshot, reads and writes nothing, and waits on no lock, so it cannot " +
		"raise class 40 on a live transaction; it fails only when the transaction is already aborted " +
		"or doomed — a state this function did not cause and cannot repair, in which the guarded " +
		"INSERT fails identically and the caller's commit reports the original death.",

	"dependencies.go:emitWIUnblockedEvent:tx.Exec(ctx, `ROLLBACK TO SAVEPOINT bp_wi_unblocked`)": "this IS the containment: rolling back to the savepoint un-poisons the caller's transaction " +
		"for every ordinary INSERT failure, and for a class-40 the doom is transaction-wide and " +
		"survives subtransaction rollback, so the caller's commit still answers 40001 as a live " +
		"PgError — classified there by the retryConflictErr every commit carries (Rule 1). Its own " +
		"failure means the savepoint was never established, i.e. the transaction was dead before " +
		"this function ran.",

	"dependencies.go:emitWIUnblockedEvent:tx.Exec(ctx, `RELEASE SAVEPOINT bp_wi_unblocked`)": "runs only after the INSERT succeeded; RELEASE performs no reads or writes, and its failure " +
		"modes (aborted transaction, unknown savepoint) all mean a death that predates this " +
		"function and is already on its way to the caller's commit classifier.",

	"memory.go:Remember:pool.Exec(ctx, ` INSERT INTO agent_events (id, work_item_id, actor_user_id, actor_display, event_type, payload, project) VALUES ($1, $2, $3, $4, 'memory_created', $5, $6)`, NewID(\"evt\"), req.WorkItemID, req.CallerUserID, req.CallerDisplay, payload, req.Project, )": "runs on the POOL, not on this function's supersede tx, and only after that tx has " +
		"committed — a separate autocommit connection cannot poison the transaction, and a class-40 " +
		"here costs exactly the memory_created event row this fire-and-forget already agreed to " +
		"lose. (In scope at all only because Remember's supersede path makes the function " +
		"transactional.)",

	"resource_events.go:lockRefusalFor:tx.QueryRow(ctx, lockHolderLookupSQL, lockType, lockKey, workItemID). Scan(&refusal.OwnerAttemptID, &refusal.ActorDisplay, &refusal.WorkItemSlug)": "best-effort holder naming on a refusal that is already decided: the caller turns the " +
		"refusal into 409 CONFLICT_LOCK_TAKEN and abandons the transaction either way, so a " +
		"class-40 here can cost only the holder's name — a degradation the type declares ('a " +
		"refusal reported without a name is still a refusal'). Classifying it into the retryable " +
		"409 would be worse: the holder is real, so a retry lands on the same refusal.",
}

// TestNoUnjustifiedClass40CapableDiscards is Rule 3 (aihub#523).
//
// Rules 1 and 2 gate the two shapes the aihub#334 instances had: a DB error
// wrapped as ErrInternalError without classification, and a result set whose
// error is never obtained. Both are blind to a third shape, and the blindness
// is structural: a call whose error is DISCARDED — `tx.Exec(…)` as a bare
// statement behind //nolint:errcheck, `_, _ = pool.Exec(…)`, `_ =
// tx.QueryRow(…).Scan(…)`, a naked `tx.Commit(ctx)` — wraps nothing and
// drains nothing, so neither rule ever sees it. aihub#492 fixed the class and
// still left five instances of this shape in one function; aihub#545 fixed
// three more and its commit names the gap exactly: "the static guard cannot
// see the discarded-error shape". Each of those was found by hand. This rule
// is the detector that was missing.
//
// Why a discarded error is not a discarded failure (aihub#492 / bestEffortExec):
// inside a live transaction, the FAILED STATEMENT is what kills the
// transaction, not the error value. Drop the error and the death is still
// there — every later statement answers 25P02, tx.Commit answers
// pgx.ErrTxCommitRollback, and no classifier downstream can recover the
// SQLSTATE. So "best effort" is a statement the caller is not entitled to
// make alone: the database gets a vote, and class 40 is how it votes no.
//
// Scope: transactional functions, same test and same reasoning as Rules 1-2 —
// class 40 is a property of transactions. A fire-and-forget pool.Exec in a
// function that opens no transaction (the memory.go event emissions in
// CommitMemory, EditCommit, ReplyCommit, Activate, Redact, ResolveCommit;
// gc.go's advisory unlock) is autocommit on its own connection: a class-40
// there costs exactly the best-effort row the site already agreed to lose,
// and there is no enclosing transaction to poison. Those stay out on
// principle, not by entry.
//
// Rollback is excluded on principle (it is absent from dbCallMethods): a
// ROLLBACK takes no snapshot, waits on no lock, and so cannot raise class 40;
// by the time it runs, the class-40 that killed the transaction has already
// surfaced — or will surface — at a statement or at commit, which are the
// places Rules 1-3 stand. That single exclusion covers the 20-odd
// `defer tx.Rollback(ctx) //nolint:errcheck` idioms in this package.
//
// Stated blind spots, deliberate:
//
//   - consulted-then-dropped: `if upErr != nil { <classifiers>; }` falling
//     through discards what the classifiers declined, but the error IS routed
//     through them, which is this rule's compliant lane. The one deliberate
//     instance — FnForceTakeover's lock re-upsert loop — was adjudicated with
//     aihub#523: its class-40 and foreign-holder exits are consulted in-branch
//     and pinned behaviourally ("force takeover lock upsert gets a retryable
//     409" in serialization_failure_db_test.go), and every error it still
//     drops has already killed the transaction, so the takeover fails at the
//     next statement regardless — the discard costs a message, not the class.
//     A branch that consults NO classifier and falls through is invisible to
//     this rule too; that is Rule 1's territory whenever it wraps, and a
//     stated gap when it does not.
//   - laundering through a named variable (`err := …; _ = err`) is invisible
//     here; the aihub#522 scanners hold that shape where it bit.
//   - a helper that swallows internally (the way bestEffortExec would if it
//     dropped its return) needs interprocedural analysis this guard does not
//     do, same as Rule 1's stated residual gap.
func TestNoUnjustifiedClass40CapableDiscards(t *testing.T) {
	files := domainSourceFiles(t)
	seenJustifications := map[string]bool{}
	var violations []string
	transactionalFuncs := 0

	for _, path := range files {
		fset, file := parseDomainSource(t, path)
		base := filepath.Base(path)
		forEachFunc(file, func(fn *ast.FuncDecl, name string) {
			if !isTransactional(t, fset, fn) {
				return
			}
			transactionalFuncs++
			for _, site := range class40DiscardSites(t, fset, fn) {
				key := base + ":" + name + ":" + site.rendered
				if _, ok := class40DiscardJustifications[key]; ok {
					seenJustifications[key] = true
					continue
				}
				violations = append(violations, fset.Position(site.pos).String()+
					"\n    in "+base+":"+name+" — the error of "+site.rendered+" is discarded."+
					"\n    This runs where a transaction may be live, and a discarded error is not a "+
					"discarded failure: a class-40 rollback kills the transaction at the FAILED STATEMENT, "+
					"so every later statement answers 25P02 and the commit answers pgx.ErrTxCommitRollback "+
					"— an unclassifiable 500 in place of the retryable 409 (aihub#492/#497/#545, measured). "+
					"Either route it through the classification helpers —\n"+
					"        bestEffortExec(ctx, tx, \"<what>\", sql, args...)   // fire-and-forget statements\n"+
					"        if aerr := retryConflictErr(err, \"<what>\"); aerr != nil { return aerr }\n"+
					"    — or, if the discard genuinely cannot swallow class 40, say WHY in a "+
					"class40DiscardJustifications entry keyed\n        "+key)
			}
		})
	}

	// Anti-vacuity: same floor as Rule 1 — if the transactional-function
	// detector stops matching, this rule is scanning nothing. Detector liveness
	// for the discard shapes themselves is held by the calibration test below,
	// which is red the moment class40DiscardSites stops flagging the naked
	// shapes, so this rule does not need to floor a population it exists to
	// drive to zero.
	if transactionalFuncs < 20 {
		t.Fatalf("only %d transactional functions found in internal/domain — the detector has stopped "+
			"recognising them, so this rule is vacuous", transactionalFuncs)
	}

	for key, reason := range class40DiscardJustifications {
		if !seenJustifications[key] {
			violations = append(violations, key+
				"\n    is justified but no such discard exists any more. Delete the entry — a stale "+
				"justification pre-authorises whatever discard lands on this key next.\n    reason on file: "+reason)
		}
	}

	if len(violations) > 0 {
		sort.Strings(violations)
		t.Fatalf("%d class-40-capable call(s) discard their error without justification (aihub#523):\n\n%s\n",
			len(violations), strings.Join(violations, "\n\n"))
	}
}

// TestDiscardDetectorIsRedOnTheNakedShape is Rule 3's calibration: the
// detector must flag every discard shape the rule exists to catch, and must
// NOT flag the compliant and principled-exclusion shapes — otherwise the rule
// above is either inert or noise, and both failure modes are silent without
// this arm.
func TestDiscardDetectorIsRedOnTheNakedShape(t *testing.T) {
	flagged := []struct {
		name, src, method string
	}{
		{"bare statement behind nolint (the aihub#545 shape)",
			`func f(ctx context.Context, tx pgx.Tx) { tx.Exec(ctx, "UPDATE t SET x=1") }`, "Exec"},
		{"blank-identifier pair (the memory.go shape)",
			`func f(ctx context.Context, tx pgx.Tx) { _, _ = tx.Exec(ctx, "INSERT INTO t VALUES (1)") }`, "Exec"},
		{"blanked QueryRow Scan (the lockRefusalFor shape)",
			`func f(ctx context.Context, tx pgx.Tx) { _ = tx.QueryRow(ctx, "SELECT 1").Scan(&x) }`, "Scan"},
		{"naked commit (the VerifyAttemptCredentialPool shape)",
			`func f(ctx context.Context, tx pgx.Tx) { tx.Commit(ctx) }`, "Commit"},
		{"value kept, error blanked",
			`func f(ctx context.Context, tx pgx.Tx) { rows, _ := tx.Query(ctx, "SELECT 1"); use(rows) }`, "Query"},
	}
	for _, tc := range flagged {
		t.Run("flags: "+tc.name, func(t *testing.T) {
			fset, fn := parseFuncFixture(t, tc.src)
			sites := class40DiscardSites(t, fset, fn)
			if len(sites) != 1 {
				t.Fatalf("detector found %d discard site(s), want exactly 1 — Rule 3 no longer sees the "+
					"shape it was written for", len(sites))
			}
			if sites[0].method != tc.method {
				t.Errorf("flagged method = %q, want %q", sites[0].method, tc.method)
			}
		})
	}

	clean := []struct{ name, src string }{
		{"deferred rollback (principled exclusion: ROLLBACK cannot raise class 40)",
			`func f(ctx context.Context, tx pgx.Tx) { defer tx.Rollback(ctx) }`},
		{"blanked rollback (same exclusion, assignment form)",
			`func f(ctx context.Context, tx pgx.Tx) { _ = tx.Rollback(ctx) }`},
		{"captured error (not a discard, Rules 1-2 territory)",
			`func f(ctx context.Context, tx pgx.Tx) {
				_, err := tx.Exec(ctx, "UPDATE t SET x=1")
				if err != nil {
					return
				}
			}`},
		{"routed through bestEffortExec (the compliant lane)",
			`func f(ctx context.Context, tx pgx.Tx) {
				if aerr := bestEffortExec(ctx, tx, "emit event", "INSERT INTO t VALUES (1)"); aerr != nil {
					return
				}
			}`},
		{"consulted-then-dropped (stated blind spot, held behaviourally — see the rule's doc)",
			`func f(ctx context.Context, tx pgx.Tx) {
				_, upErr := tx.Exec(ctx, "UPDATE t SET x=1")
				if upErr != nil {
					if aerr := retryConflictErr(upErr, "upsert"); aerr != nil {
						return
					}
				}
			}`},
	}
	for _, tc := range clean {
		t.Run("passes: "+tc.name, func(t *testing.T) {
			fset, fn := parseFuncFixture(t, tc.src)
			if sites := class40DiscardSites(t, fset, fn); len(sites) != 0 {
				t.Errorf("detector flagged a shape Rule 3 must accept: %+v — this is how a guard turns "+
					"into noise and gets switched off", sites)
			}
		})
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

// class40DiscardSite is one call whose error is discarded — Rule 3's unit.
type class40DiscardSite struct {
	pos      token.Pos
	method   string
	rendered string
}

// class40DiscardSites finds the calls in fn whose error is discarded and whose
// method can hand back a *pgconn.PgError (dbCallMethods — the same population
// Rule 1 judges, and Rollback is absent from it on principle, see Rule 3's
// doc). Two shapes:
//
//   - a bare expression statement: `tx.Exec(ctx, …)` — with or without a
//     //nolint:errcheck, which this detector never reads: a nolint is an
//     instruction to the linter, not a justification to this rule;
//   - an assignment whose ERROR position is the blank identifier: for every
//     method in the set the error is the last return value, so `_, _ = …`,
//     `_ = ….Scan(…)` and `rows, _ := ….Query(…)` all qualify, while
//     `_, err := …` does not (that error's handling is Rules 1-2 territory).
//
// A deferred call is neither shape (ast.DeferStmt wraps the CallExpr
// directly), which is correct: the only DB calls this package defers are
// rollbacks, and Rollback is excluded before the shape question arises.
func class40DiscardSites(t *testing.T, fset *token.FileSet, fn *ast.FuncDecl) []class40DiscardSite {
	t.Helper()
	var out []class40DiscardSite
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		var call *ast.CallExpr
		switch s := n.(type) {
		case *ast.ExprStmt:
			c, ok := s.X.(*ast.CallExpr)
			if !ok {
				return true
			}
			call = c
		case *ast.AssignStmt:
			if len(s.Rhs) != 1 {
				return true
			}
			c, ok := s.Rhs[0].(*ast.CallExpr)
			if !ok {
				return true
			}
			last, ok := s.Lhs[len(s.Lhs)-1].(*ast.Ident)
			if !ok || last.Name != "_" {
				return true
			}
			call = c
		default:
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || !dbCallMethods[sel.Sel.Name] {
			return true
		}
		out = append(out, class40DiscardSite{
			pos:      call.Lparen,
			method:   sel.Sel.Name,
			rendered: renderNode(t, fset, call),
		})
		return true
	})
	return out
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
