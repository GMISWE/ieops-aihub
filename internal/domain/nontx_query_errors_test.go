package domain

// aihub#607 — the census the aihub#334 guard deliberately deferred.
//
// retryable_conflict_guard_test.go's Rule 2 scopes itself to transactional
// functions and says of the rest: "an undrained error on a pool query outside
// any transaction is a real bug, but it is not THIS class, and there are
// 20-odd of them. Closing that wider set is tracked separately rather than
// smuggled in here." aihub#522 closed two functions of it (PredictConflicts,
// FnClaimWorkItem — predict_claim_query_errors_test.go); this file closes the
// remainder: BOTH aihub#500/#522 scanners now run over EVERY non-transactional
// function in this package, and every site is either compliant or carries a
// written justification below.
//
// What "compliant" buys outside a transaction: there is no transaction to
// poison, but the two lying modes are the same ones aihub#500 and aihub#522
// removed elsewhere —
//
//   - a swallowed send-time or drain error renders as an empty result inside a
//     normal response, byte-identical to the data genuinely being absent
//     (aihub#238's fake all-clear, aihub#449's silent-empty segment);
//   - an unclassified error branch answers a class-40 rollback with "the
//     server is broken" where the neighbouring branch of the same function
//     answers the retryable 409. Class 40 is rarer on autocommit reads, but
//     40P01 is reachable at any isolation level, and pgx surfaces both as a
//     plain *pgconn.PgError that dbErr/dbErrCause pass through byte-identically
//     for every other error — so classifying costs nothing.
//
// ── Census shapes, stated rather than implied ────────────────────────────────
//
// aihub#522 measured that the original census (2-LHS `rows, err := X.Query(…)`)
// was blind to `err := X.QueryRow(…).Scan(…)`, and three of the worst swallows
// hid in that blind spot. This census therefore runs BOTH scanners, and pins
// the three shapes both of them are still blind to as explicit site sets
// (TestNonTxBlindSpotShapesArePinned): a site cannot move into a blind spot —
// or a new site be born in one — without a named entry going stale or missing
// here, which is a red test either way.
//
// Both scanners here match READ shapes. The write shape — `_, err :=
// pool.Exec(…)` — joined the census in aihub#620 (nontx_exec_errors_test.go),
// with its own scanner, ledger and blind-spot pins over the same function set.
//
// No database:
//
//	GOWORK=off go test ./internal/domain/ -run 'NonTransactional|NonTxBlindSpot|DetectCycleSurfaces|CredentialSimpleSurfaces' -count=1

import (
	"context"
	"go/ast"
	"go/token"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// nontxJustification is one deliberately-unclassified site population in a
// non-transactional function: how many scanner violations it accounts for and
// why the discard/propagation cannot lie to a caller. Same philosophy as
// conflictGuardExemptions: the entry lives HERE, not in a nolint, a stale
// entry is itself a violation, and the key ("<file>:<func>") survives every
// edit around it.
type nontxJustification struct {
	Count  int
	Reason string
}

// nontxQueryJustifications covers the 2-LHS Query scanner's remaining
// violations. Every entry is one of two kinds, and says which:
//
//   - "scanner-shape": the code is behaviourally compliant but arranged in a
//     shape the scanner's "immediately after" rule cannot see; the behaviour
//     is pinned by a named test elsewhere.
//   - "best-effort": the error deliberately does not fail the caller; the
//     entry says what a failure costs and why that cost was accepted.
var nontxQueryJustifications = map[string]nontxJustification{
	"gc.go:RunNeedsHumanSessionAging": {1, "best-effort toward HTTP but not toward the operator: the send-time " +
		"error goes into GCResult.Error, which the GC loop prints and does not record as a completed run, so " +
		"the sweep retries on every tick until the query works. There is no HTTP caller to receive a 409/500 " +
		"split — classification has no audience here."},
	"gc.go:RunUnclassifiedWIAlert": {1, "same as RunNeedsHumanSessionAging: the failure is surfaced into " +
		"GCResult.Error and the tick loop retries; no HTTP caller exists for a classifier verdict."},
	"memory.go:loadForwardRelations": {1, "enrichment-only helper: both callers (Recall text path, " +
		"RecallWithVector) deliberately log-and-continue on its failure, so recall answers WITHOUT the " +
		"related[] decoration rather than failing — items themselves are never affected, and the caller can " +
		"see the difference (missing enrichment) where a swallowed item query would be invisible. The helper " +
		"itself propagates every error (send, scan, drain) — nothing is discarded inside it."},
	"memory.go:MemoryVersionChain": {1, "the sole caller is the /ui side-rail (versionChainFn in " +
		"routes_artifacts.go), which renders the version history only when the chain loads and drops the rail " +
		"otherwise — a page decoration, documented best-effort at buildVersionHistoryHTML. The helper " +
		"propagates every error; no MCP or API caller consumes it."},
	"projects.go:ListProjects": {2, "scanner-shape: the err guard sits after the admin/member if/else split " +
		"that issues the two Query calls, so the 'statement immediately after' rule cannot see it — the exact " +
		"false-positive shape aihub#522 measured. The guard exists, returns, and classifies via dbErrCause; " +
		"TestDedupAndListProjectsClassifyTheirSendTimeErrors (predict_claim_query_errors_test.go) pins it."},
	"work_items.go:attachStepState": {1, "best-effort by design (aihub#280): a step-state enrichment failure " +
		"leaves StepState nil — the same shape as 'never claimed' — because include_step_state=true must not " +
		"be able to break a list call that works without it. All three failure paths (send, scan, drain) log " +
		"to stderr, so the degradation is visible to an operator even though the caller keeps their page."},
}

// nontxQueryRowJustifications covers the QueryRow scanner's remaining
// violations, same two kinds as above.
var nontxQueryRowJustifications = map[string]nontxJustification{
	"gc.go:isAttachedPartition": {1, "scanner-shape: `return exists, err` propagates the error instead of " +
		"guarding it with an if — nothing is discarded. Consumers are the partition sweeps, which route it " +
		"into GCResult.Error / wrapped errors; no HTTP caller exists for a classifier verdict."},
	"gc.go:countDefaultRowsInRange": {1, "scanner-shape: same propagate-by-return as isAttachedPartition, " +
		"same GC-sweep consumers."},
	"memory.go:resolveRecallWorkItemRef": {1, "the ErrNoRows arm deliberately returns (ref, nil): an " +
		"unresolvable work_item_id filter passes through unresolved, for the mismatched-row reasons documented " +
		"at the function. That nil-error return is what the scanner flags. The DB-failure arm propagates, and " +
		"the single caller (Recall) classifies it via dbErrCause — a transient outage answers as an error, " +
		"never as a silent empty page."},
	"memory.go:countMemories": {1, "scanner-shape: a one-statement helper that propagates by `return n, err`. " +
		"Both callers (recallText, RecallWithVector) guard and classify the propagated error via dbErrCause."},
	"projects.go:getProjectByNameWithHash": {1, "scanner-shape: propagates by `return nil, err` so its caller " +
		"can distinguish ErrNoRows (404, existence-hiding) from real failure; the sole caller " +
		"(checkProjectAccess) classifies the failure arm via dbErrCause."},
	"wi_watches.go:IsWatchingWorkItem": {1, "best-effort documented at the function: the only caller renders " +
		"the /ui watch toggle and treats any failure as 'not watching', so a binary-before-migration deploy " +
		"degrades the control instead of the page. The error is propagated, not discarded; the swallow is the " +
		"caller's stated policy."},
}

// forEachNonTransactionalFunc visits every function in the package the
// aihub#334 guard does NOT cover — the complement of isTransactional over the
// same file set, so between the two guards no function is unscanned.
func forEachNonTransactionalFunc(t *testing.T, visit func(base string, fset *token.FileSet, fn *ast.FuncDecl, name string)) {
	t.Helper()
	for _, path := range domainSourceFiles(t) {
		fset, file := parseDomainSource(t, path)
		forEachFunc(file, func(fn *ast.FuncDecl, name string) {
			if isTransactional(t, fset, fn) {
				return
			}
			visit(path, fset, fn, name)
		})
	}
}

// checkLedger compares scanner findings against a justification ledger:
// every violation must be budgeted by its function's entry, and every entry
// must be fully consumed — a stale or half-used entry pre-authorises the next
// violation that lands on its key, so it is a failure too.
func checkLedger(t *testing.T, ledger map[string]nontxJustification, found map[string][]string) {
	t.Helper()
	var problems []string
	for key, msgs := range found {
		j, ok := ledger[key]
		if !ok {
			for _, m := range msgs {
				problems = append(problems, key+" — "+m+
					"\n    Either classify the error (dbErr/dbErrCause/pgxErr — byte-identical messages for "+
					"every non-class-40 error) or say WHY the site cannot lie to a caller, in a ledger entry "+
					"keyed "+key)
			}
			continue
		}
		if len(msgs) != j.Count {
			problems = append(problems, key+" — the ledger budgets "+strconv.Itoa(j.Count)+" violation(s) but "+
				"the scanner found "+strconv.Itoa(len(msgs))+". A site was added, fixed, or moved; "+
				"re-adjudicate the function and update the entry (reason on file: "+j.Reason+")")
		}
	}
	for key, j := range ledger {
		if _, ok := found[key]; !ok {
			problems = append(problems, key+" — is justified but the scanner no longer reports it. Delete the "+
				"entry: a stale justification pre-authorises whatever lands on this key next.\n    reason on "+
				"file: "+j.Reason)
		}
	}
	if len(problems) > 0 {
		sort.Strings(problems)
		t.Fatalf("%d non-transactional query-error problem(s) (aihub#607):\n\n%s\n",
			len(problems), strings.Join(problems, "\n\n"))
	}
}

// TestNonTransactionalQuerySitesAnswerTheirErrors runs the aihub#500/#548
// scanner over every non-transactional function in the package.
func TestNonTransactionalQuerySitesAnswerTheirErrors(t *testing.T) {
	found := map[string][]string{}
	totalSites := 0
	forEachNonTransactionalFunc(t, func(base string, fset *token.FileSet, fn *ast.FuncDecl, name string) {
		sites, violations := scanQueryErrorHandling(fn, fset)
		totalSites += sites
		for _, v := range violations {
			key := base + ":" + name
			found[key] = append(found[key], "rows "+v.Rows+": "+v.Why)
		}
	})

	// The count arm. Measured 2026-09-12 on the aihub#607 tree: 29 two-value
	// Query sites across the package's non-transactional functions (six of them
	// PredictConflicts', seven GetReadyQueue's — their own arms pin those
	// per-function). More: a query was added — guard it like the others and
	// raise this. Fewer: a site was removed, or it moved into a shape this
	// scanner cannot see (an if-init assignment, a helper wrapping the call),
	// which reads as compliant — check which before touching the number.
	//
	// 29 -> 31 same day: aihub#360 added the two lexical-section page queries
	// (recallLexical in memory_lexical.go, listWorkItemsLexical in
	// wi_lexical.go), both answering their errors through dbErrCause.
	const wantSites = 31
	if totalSites != wantSites {
		t.Errorf("scanner found %d Query sites in non-transactional functions, want %d — see the count-arm "+
			"note above this assertion before touching the number", totalSites, wantSites)
	}

	checkLedger(t, nontxQueryJustifications, found)
}

// TestNonTransactionalQueryRowSitesAnswerTheirErrors is the QueryRow twin,
// covering the shape the original census was measured blind to (aihub#522).
func TestNonTransactionalQueryRowSitesAnswerTheirErrors(t *testing.T) {
	found := map[string][]string{}
	totalSites := 0
	forEachNonTransactionalFunc(t, func(base string, fset *token.FileSet, fn *ast.FuncDecl, name string) {
		sites, violations := scanQueryRowErrorHandling(fn, fset)
		totalSites += sites
		for _, v := range violations {
			key := base + ":" + name
			found[key] = append(found[key], v.Err+": "+v.Why)
		}
	})

	// Measured 2026-09-12 on the aihub#607 tree: 21 single-assign
	// QueryRow(…).Scan(…) sites in non-transactional functions.
	//
	// 21 -> 22 same day: aihub#360's listWorkItemsLexical counts its matches
	// through a single-assign QueryRow, answered through dbErrCause (the
	// memory side reuses countMemories, an already-counted site).
	const wantSites = 22
	if totalSites != wantSites {
		t.Errorf("scanner found %d QueryRow sites in non-transactional functions, want %d — fewer may mean a "+
			"site moved into a stated blind spot (if-init, row-helper, blanked error), which "+
			"TestNonTxBlindSpotShapesArePinned holds; check which before touching the number", totalSites, wantSites)
	}

	checkLedger(t, nontxQueryRowJustifications, found)
}

// TestNonTxBlindSpotShapesArePinned enumerates the three shapes BOTH scanners
// are blind to and pins each population as an explicit site set, so a site
// cannot enter or leave a blind spot silently:
//
//   - if-init: `if err := X.QueryRow(…).Scan(…); err != nil {` — the
//     assignment lives in the if's init clause, which neither scanner walks.
//   - row-helper: `row := X.QueryRow(…)` handed to a scan helper
//     (scanProject, row.Scan in a later statement) — no `.Scan` in the
//     assignment, so the QueryRow scanner never fires.
//   - blanked Scan: `_ = X.QueryRow(…).Scan(…)` — the error position is the
//     blank identifier; inside a transaction Rule 3 of the aihub#334 guard
//     catches this, outside one nothing did.
//
// Every pinned site was adjudicated in aihub#607:
//
//   - if-init: tryAdvisoryLock and reportDefaultBacklog propagate into the GC
//     loop; repointHeadIfRedacted and TransferOwner classify via dbErrCause;
//     UnmatchedTypes discloses its failure in-band as the diagnostic string it
//     returns (never a partial answer); refreshWorkItemEmbeddingBestEffort is
//     named, documented, logged best-effort with no transaction to poison.
//   - row-helper: GetParentRef classifies via dbErr (ErrNoRows = no parent,
//     preserved); getProjectByName propagates to checkProjectAccess's
//     dbErrCause arm; CreateProject classifies via dbErrCause after its
//     23505 duplicate check; findCommitEntry classifies via pgxErr. (This
//     detector found findCommitEntry itself — the manual census had missed
//     it, which is the argument for pinning the shape rather than trusting
//     the enumeration.)
//   - blanked Scan: ReplyCommit and ResolveCommit look up a wi id for an
//     event row that the very next statement already inserts fire-and-forget
//     (//nolint:errcheck on the pool, no enclosing transaction) — the lookup
//     cannot be worth more than the row it decorates; both carry the aihub#607
//     comment at the site.
func TestNonTxBlindSpotShapesArePinned(t *testing.T) {
	ifInit := map[string]int{}
	rowHelper := map[string]int{}
	blankScan := map[string]int{}

	forEachNonTransactionalFunc(t, func(base string, fset *token.FileSet, fn *ast.FuncDecl, name string) {
		key := base + ":" + name
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			switch s := n.(type) {
			case *ast.IfStmt:
				if as, ok := s.Init.(*ast.AssignStmt); ok && len(as.Rhs) == 1 && containsQueryRowScan(as.Rhs[0]) {
					ifInit[key]++
				}
			case *ast.AssignStmt:
				if len(s.Rhs) != 1 {
					return true
				}
				if call, ok := s.Rhs[0].(*ast.CallExpr); ok && len(s.Lhs) == 1 {
					if sel, ok := call.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "QueryRow" {
						if id, ok := s.Lhs[0].(*ast.Ident); ok && id.Name != "_" {
							rowHelper[key]++
						}
					}
				}
				if containsQueryRowScan(s.Rhs[0]) {
					allBlank := true
					for _, l := range s.Lhs {
						if id, ok := l.(*ast.Ident); !ok || id.Name != "_" {
							allBlank = false
						}
					}
					if allBlank {
						blankScan[key]++
					}
				}
			}
			return true
		})
	})

	assertPinnedSet(t, "if-init QueryRow.Scan", ifInit, map[string]int{
		"gc.go:tryAdvisoryLock":                              1,
		"gc.go:reportDefaultBacklog":                         1,
		"memory.go:repointHeadIfRedacted":                    1,
		"memory_unmatched.go:UnmatchedTypes":                 1,
		"projects.go:TransferOwner":                          1,
		"wi_embedding.go:refreshWorkItemEmbeddingBestEffort": 1,
	})
	assertPinnedSet(t, "row-helper QueryRow", rowHelper, map[string]int{
		"dependencies.go:GetParentRef": 1,
		"memory.go:findCommitEntry":    1,
		"projects.go:getProjectByName": 1,
		"projects.go:CreateProject":    1,
	})
	assertPinnedSet(t, "blank-assigned QueryRow.Scan", blankScan, map[string]int{
		"memory.go:ReplyCommit":   1,
		"memory.go:ResolveCommit": 1,
	})
}

func assertPinnedSet(t *testing.T, shape string, got, want map[string]int) {
	t.Helper()
	for key, n := range got {
		if want[key] != n {
			t.Errorf("%s: %s holds %d site(s), pinned %d. A site entered or multiplied inside a scanner "+
				"blind spot — adjudicate it (classify, or justify in this file's doc) and re-pin.",
				shape, key, n, want[key])
		}
	}
	for key, n := range want {
		if got[key] != n {
			t.Errorf("%s: %s is pinned at %d site(s) but the detector sees %d. The site was fixed, moved, or "+
				"the detector went blind — check which, then re-pin.", shape, key, n, got[key])
		}
	}
}

// TestDetectCycleSurfacesQueryFailure is the behavioural arm for aihub#607's
// one genuine fail-open: detectCycle used to answer a DB failure with
// `return nil // Non-fatal; allow creation`, letting a dependency cycle into
// the graph whenever the reachability probe could not run — the gate failing
// open, exactly the shape aihub#522 fixed in PredictConflicts rule 1.
//
// No database: the pool points at a closed port and pgxpool connects lazily,
// so the first acquire fails at dial time.
func TestDetectCycleSurfacesQueryFailure(t *testing.T) {
	pool, err := pgxpool.New(context.Background(), "postgres://u:p@127.0.0.1:1/db?connect_timeout=1")
	if err != nil {
		t.Fatalf("pgxpool.New should only parse the config, got: %v", err)
	}
	defer pool.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	aerr := detectCycle(ctx, pool, "wi_child", "wi_parent", "blocks")
	if aerr == nil {
		t.Fatal("detectCycle answered nil against an unreachable pool — the cycle gate fails OPEN: a DB " +
			"failure admits the edge unchecked, which is the pre-aihub#607 swallow")
	}
	if !strings.Contains(aerr.Message, "failed to check for dependency cycles") {
		t.Errorf("error %q does not name the cycle check — the failure was surfaced by something else, "+
			"which means this site's own guard is gone", aerr.Message)
	}
}

// TestVerifyAttemptCredentialSimpleSurfacesDBFailure pins the other behaviour
// change: a DB failure while loading the stored secret hash used to fold into
// the mismatch arm and answer "invalid session_secret" — an availability fault
// delivered as a credential verdict (the aihub#522 BearerAuth second-lie
// shape, and a mis-verdict that tells an agent to re-claim rather than retry).
func TestVerifyAttemptCredentialSimpleSurfacesDBFailure(t *testing.T) {
	pool, err := pgxpool.New(context.Background(), "postgres://u:p@127.0.0.1:1/db?connect_timeout=1")
	if err != nil {
		t.Fatalf("pgxpool.New should only parse the config, got: %v", err)
	}
	defer pool.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	attemptID := "ra_test"
	wi := &WorkItem{CurrentAttemptID: &attemptID, CurrentAttemptEpoch: 3}
	aerr := verifyAttemptCredentialSimple(ctx, pool, wi, attemptID, 3, "secret")
	if aerr == nil {
		t.Fatal("verifyAttemptCredentialSimple answered nil against an unreachable pool")
	}
	if aerr.Code == ErrAttemptMismatch {
		t.Fatalf("a DB failure answered %s %q — an availability fault delivered as a credential verdict; "+
			"the load failure must surface through dbErr instead", aerr.Code, aerr.Message)
	}
	if !strings.Contains(aerr.Message, "failed to load run_attempt credential") {
		t.Errorf("error %q does not name the credential load — this site's own guard is gone", aerr.Message)
	}
}
