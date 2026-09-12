package domain

// aihub#620 — the Exec shape joins the aihub#607 census.
//
// nontx_query_errors_test.go runs the aihub#500/#522 scanners over every
// non-transactional function in this package, but both of those scanners match
// READ shapes (`rows, err := X.Query(…)`, `err := X.QueryRow(…).Scan(…)`).
// A write — `_, err := pool.Exec(…)` — was in no census at all: inside a
// transaction Rule 3 of the aihub#334 guard judges it, outside one nothing
// did, and the misclassification it carries is the same one aihub#548 removed
// from GetReadyQueue: the guard exists, returns, but answers with a bare
// NewErr(ErrInternalError, …), so a class-40 rollback (SQLSTATE 40001/40P01)
// reads "the server is broken" where the neighbouring QueryRow site of the
// SAME function answers the retryable 409 via dbErrCause. Class 40 on an
// autocommit UPDATE is exactly the reachable half: 40P01 fires at any
// isolation level whenever the row being updated is locked into a cycle.
//
// The census below found 16 scanner-visible Exec sites in non-transactional
// functions. Ten answered an HTTP caller with a bare NewErr and are now
// classified — CommitMemory, EditCommit, DeleteCommit, ReplyCommit,
// ResolveCommit, Redact, EmitEvent, SetMemoryVisibility (dbErr — its message
// never carried the driver text), RotateIdentifier and TransferOwner — all
// byte-identical for every non-class-40 error, per the dbErr/dbErrCause
// contract. The remaining six carry a written justification in the ledger.
// One more surfaced from a blind spot: repointHeadIfRedacted's if-init UPDATE
// (an HTTP path — Redact returns it) was a bare NewErr too, and now classifies
// via dbErrCause; see the if-init pin below.
//
// ── Census shapes ─────────────────────────────────────────────────────────────
//
// Same discipline as aihub#607: the scanner states its blind spots and this
// file pins each blind-spot population as an explicit site set
// (TestNonTxExecBlindSpotShapesArePinned), so a site cannot move into a blind
// spot — or be born in one — without a named entry going stale or missing.
// The Exec counterparts of #607's three measured blind-spot shapes are:
//
//   - if-init (counterpart of if-init QueryRow.Scan): `if _, err :=
//     X.Exec(…); err != nil {` — the assignment lives in the if's init
//     clause, which the scanner does not walk.
//   - blanked (counterpart of blanked Scan): `_, _ = X.Exec(…)` — both
//     results discarded, fire-and-forget.
//   - bare call (Exec-only; QueryRow has no counterpart because its result
//     must be consumed to do anything): `X.Exec(…)` as an expression
//     statement, which discards both results without even an assignment.
//
// There is no row-helper counterpart: Exec returns (CommandTag, error)
// directly, so there is no intermediate object whose consumption can be
// deferred past the assignment the way `row := X.QueryRow(…)` defers Scan.
//
// No database:
//
//	GOWORK=off go test ./internal/domain/ -run 'NonTransactionalExec|NonTxExecBlindSpot' -count=1

import (
	"go/ast"
	"go/token"
	"strconv"
	"testing"
)

// nontxExecJustifications covers the Exec scanner's remaining violations, same
// two kinds as nontx_query_errors_test.go's ledgers ("scanner-shape" /
// "best-effort"), same rules: every violation must be budgeted by its
// function's entry, every entry fully consumed, stale entries are failures.
var nontxExecJustifications = map[string]nontxJustification{
	"gc.go:RunMemoryExpiredSweep": {1, "best-effort toward HTTP but not toward the operator (the " +
		"RunNeedsHumanSessionAging shape, aihub#607): the send-time error goes into GCResult.Error, the run is " +
		"not recorded as completed, and the sweep retries on every tick until the UPDATE works. No HTTP caller " +
		"exists to receive a 409/500 split — classification has no audience here."},
	"gc.go:RunMethodologyExpiryArchive": {1, "same as RunMemoryExpiredSweep: the failure is surfaced into " +
		"GCResult.Error and the tick loop retries; no HTTP caller exists for a classifier verdict."},
	"gc.go:RunEventPayloadTruncation": {1, "same as RunMemoryExpiredSweep: the failure is surfaced into " +
		"GCResult.Error and the tick loop retries; no HTTP caller exists for a classifier verdict."},
	"gc.go:RunNeedsHumanSessionAging": {1, "scanner-shape: the per-wi alert INSERT deliberately accumulates " +
		"its error into errs and continues — the aihub#268 accumulate-rather-than-drop shape, documented at the " +
		"site — so one failing INSERT does not starve the remaining wis of their alerts. The joined errors reach " +
		"GCResult.Error, the run is not recorded, and the sweep retries; no HTTP caller exists."},
	"gc.go:RunUnclassifiedWIAlert": {1, "same accumulate-and-continue shape as RunNeedsHumanSessionAging, " +
		"cross-referenced at the site; errors reach GCResult.Error and the sweep retries; no HTTP caller exists."},
	"memory.go:runRenderJob": {1, "best-effort by design (aihub#130): the render worker has no caller to " +
		"answer — a failed rendered_html store is logged to stderr, counted in renderFailCount, and leaves the " +
		"column NULL, which the viewer detects and re-renders from content on read. The bare `return` the " +
		"scanner flags as publishing success is a void function's only way out; there is no result to lie in."},
}

// execErrViolation is one `tag/_, e := <recv>.Exec(…)` whose error is not
// answered by a classified return.
type execErrViolation struct {
	Line int
	Err  string
	Why  string
}

// scanExecErrorHandling is the Exec twin of scanQueryErrorHandling
// (ready_queue_query_errors_test.go): it reports every 2-LHS
// `tag, e := <recv>.Exec(…)` in fn whose error is not immediately checked
// with `e != nil`, returned, and classified. The "immediately after" rule,
// the aihub#549 nil-return rule and the aihub#548 classifier rule are all
// inherited unchanged — see the doc on scanQueryErrorHandling for why each
// exists.
//
// A site whose error position is the blank identifier (`_, _ = X.Exec(…)`)
// is NOT counted here: discarding both results is a different act from
// naming the error and then mishandling it, and that population is pinned
// per-site by TestNonTxExecBlindSpotShapesArePinned instead.
func scanExecErrorHandling(fn *ast.FuncDecl, fset *token.FileSet) (sites int, violations []execErrViolation) {
	var walk func(stmts []ast.Stmt)
	walk = func(stmts []ast.Stmt) {
		for i, s := range stmts {
			// Recurse into nested blocks first, exactly as the Query scanner
			// does, so an Exec inside an if or a loop stays in the census.
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
			if !ok || len(as.Rhs) != 1 || len(as.Lhs) != 2 {
				continue
			}
			call, ok := as.Rhs[0].(*ast.CallExpr)
			if !ok {
				continue
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || sel.Sel.Name != "Exec" {
				continue
			}
			errIdent, ok := as.Lhs[1].(*ast.Ident)
			if !ok {
				violations = append(violations, execErrViolation{fset.Position(as.Pos()).Line, exprString(as.Lhs[1]),
					"the second result is not a plain identifier, so the error cannot be traced"})
				sites++
				continue
			}
			if errIdent.Name == "_" {
				// Blanked — pinned by TestNonTxExecBlindSpotShapesArePinned.
				continue
			}
			sites++
			line := fset.Position(as.Pos()).Line

			if i+1 >= len(stmts) {
				violations = append(violations, execErrViolation{line, errIdent.Name,
					"nothing follows the call, so " + errIdent.Name + " is discarded"})
				continue
			}
			ifs, ok := stmts[i+1].(*ast.IfStmt)
			if !ok {
				violations = append(violations, execErrViolation{line, errIdent.Name,
					"the next statement is not an `if`, so " + errIdent.Name + " is not answered"})
				continue
			}
			bin, ok := ifs.Cond.(*ast.BinaryExpr)
			if !ok {
				violations = append(violations, execErrViolation{line, errIdent.Name,
					"the guard is not a comparison against " + errIdent.Name})
				continue
			}
			lhs, ok := bin.X.(*ast.Ident)
			if !ok || lhs.Name != errIdent.Name {
				violations = append(violations, execErrViolation{line, errIdent.Name,
					"the guard does not test " + errIdent.Name})
				continue
			}
			if bin.Op != token.NEQ {
				violations = append(violations, execErrViolation{line, errIdent.Name,
					"the guard is not `" + errIdent.Name + " != nil` — a failed write falls through as success"})
				continue
			}
			if !endsInReturn(ifs.Body.List) {
				violations = append(violations, execErrViolation{line, errIdent.Name,
					"the `" + errIdent.Name + " != nil` branch does not return, so the failure falls through"})
				continue
			}
			// aihub#549 rule, unchanged: the return must carry the failure out.
			if nilErrorReturn(ifs.Body.List) {
				violations = append(violations, execErrViolation{line, errIdent.Name,
					"a return inside the `" + errIdent.Name + " != nil` branch publishes success — its error " +
						"slot is nil (or the return is bare) — so the failed write leaves the guard as a " +
						"normal answer. Return the classified error explicitly: `return dbErrCause(" +
						errIdent.Name + ", …)`"})
				continue
			}
			// aihub#548 rule, unchanged: the branch must consult a class-40
			// classifier (the conflictClassifiers set).
			if !callsClassifier(ifs) {
				violations = append(violations, execErrViolation{line, errIdent.Name,
					"the `" + errIdent.Name + " != nil` branch returns without consulting a class-40 " +
						"classifier (dbErr/dbErrCause/retryConflictErr/pgxErr), so a rollback on this write " +
						"answers 500 where the same function's read sites answer the retryable 409"})
			}
		}
	}
	walk(fn.Body.List)
	return sites, violations
}

// TestNonTransactionalExecSitesAnswerTheirErrors runs the Exec scanner over
// every non-transactional function in the package — the same function set the
// aihub#607 census walks, via the same forEachNonTransactionalFunc.
func TestNonTransactionalExecSitesAnswerTheirErrors(t *testing.T) {
	found := map[string][]string{}
	totalSites := 0
	forEachNonTransactionalFunc(t, func(base string, fset *token.FileSet, fn *ast.FuncDecl, name string) {
		sites, violations := scanExecErrorHandling(fn, fset)
		totalSites += sites
		for _, v := range violations {
			key := base + ":" + name
			found[key] = append(found[key], "err "+v.Err+" (line "+strconv.Itoa(v.Line)+"): "+v.Why)
		}
	})

	// The count arm. Measured 2026-09-12 on the aihub#620 tree: 16 two-value
	// Exec sites with a named error across the package's non-transactional
	// functions (five of them gc.go sweeps, nine memory.go, two projects.go).
	// More: a write was added — guard it like the others and raise this.
	// Fewer: a site was removed, or it moved into a shape this scanner cannot
	// see (if-init, blanked, bare call — all pinned below), which reads as
	// compliant — check which before touching the number.
	const wantSites = 16
	if totalSites != wantSites {
		t.Errorf("scanner found %d Exec sites in non-transactional functions, want %d — see the count-arm "+
			"note above this assertion before touching the number", totalSites, wantSites)
	}

	checkLedger(t, nontxExecJustifications, found)
}

// TestNonTxExecBlindSpotShapesArePinned enumerates the three shapes the Exec
// scanner is blind to and pins each population as an explicit site set, so a
// site cannot enter or leave a blind spot silently — the direct counterpart of
// TestNonTxBlindSpotShapesArePinned one file over.
//
// Every pinned site was adjudicated in aihub#620:
//
//   - if-init: WatchWorkItem and UnwatchWorkItem already classified via
//     dbErrCause (WatchWorkItem after its FK-violation 404 check);
//     repointHeadIfRedacted answered its HTTP caller (Redact) with a bare
//     NewErr and was FIXED in aihub#620 to classify via dbErrCause;
//     RunPartitionCreate accumulates the audit-event INSERT's failure into
//     errs → GCResult.Error (the aihub#268 shape, reported not swallowed);
//     reportDefaultBacklog propagates by fmt.Errorf into the same sweep's
//     errs; refreshWorkItemEmbeddingBestEffort is named, documented,
//     logged best-effort with no caller to answer (UpdateWorkItem already
//     returned) and the row degrades to text-only recall, same as at birth.
//   - blanked: all seven are the fire-and-forget agent_events emissions
//     (CommitMemory, EditCommit, DeleteCommit, ReplyCommit, Activate, Redact,
//     ResolveCommit), each marked //nolint:errcheck at the site: the primary
//     write is already committed on autocommit, there is no transaction to
//     poison, and losing the event must not fail the request — the population
//     Rule 3 of the aihub#334 guard names and deliberately leaves out
//     ("Those stay out on principle, not by entry"). This pin gives that
//     principle a per-site census so the population cannot grow silently.
//   - bare call: tryAdvisoryLock's release closure discards
//     pg_advisory_unlock's result (//nolint:errcheck at the site). The call
//     errors only when the connection is broken, and a broken connection is
//     destroyed on Release rather than pooled, which ends the session and
//     releases the advisory lock server-side — the failure cannot strand the
//     lock, and the deferred-release path has no caller to report to.
func TestNonTxExecBlindSpotShapesArePinned(t *testing.T) {
	ifInit := map[string]int{}
	blanked := map[string]int{}
	bareCall := map[string]int{}

	forEachNonTransactionalFunc(t, func(base string, fset *token.FileSet, fn *ast.FuncDecl, name string) {
		key := base + ":" + name
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			switch s := n.(type) {
			case *ast.IfStmt:
				if as, ok := s.Init.(*ast.AssignStmt); ok && len(as.Rhs) == 1 && isExecCall(as.Rhs[0]) {
					ifInit[key]++
				}
			case *ast.AssignStmt:
				if len(s.Rhs) != 1 || !isExecCall(s.Rhs[0]) {
					return true
				}
				allBlank := true
				for _, l := range s.Lhs {
					if id, ok := l.(*ast.Ident); !ok || id.Name != "_" {
						allBlank = false
					}
				}
				if allBlank {
					blanked[key]++
				}
			case *ast.ExprStmt:
				if isExecCall(s.X) {
					bareCall[key]++
				}
			}
			return true
		})
	})

	assertPinnedSet(t, "if-init Exec", ifInit, map[string]int{
		"gc.go:RunPartitionCreate":                           1,
		"gc.go:reportDefaultBacklog":                         1,
		"memory.go:repointHeadIfRedacted":                    1,
		"wi_embedding.go:refreshWorkItemEmbeddingBestEffort": 1,
		"wi_watches.go:WatchWorkItem":                        1,
		"wi_watches.go:UnwatchWorkItem":                      1,
	})
	assertPinnedSet(t, "blank-assigned Exec", blanked, map[string]int{
		"memory.go:CommitMemory":  1,
		"memory.go:EditCommit":    1,
		"memory.go:DeleteCommit":  1,
		"memory.go:ReplyCommit":   1,
		"memory.go:Activate":      1,
		"memory.go:Redact":        1,
		"memory.go:ResolveCommit": 1,
	})
	assertPinnedSet(t, "bare-call Exec", bareCall, map[string]int{
		"gc.go:tryAdvisoryLock": 1,
	})
}

// isExecCall reports whether e is a `<recv>.Exec(…)` call.
func isExecCall(e ast.Expr) bool {
	call, ok := e.(*ast.CallExpr)
	if !ok {
		return false
	}
	sel, ok := call.Fun.(*ast.SelectorExpr)
	return ok && sel.Sel.Name == "Exec"
}
