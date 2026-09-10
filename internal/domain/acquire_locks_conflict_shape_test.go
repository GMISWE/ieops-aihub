package domain

// aihub#543 probe wave 1, slice C — the `docs/mcp-cards/pf_acquire_locks.md`
// sentence that is the tool's headline refusal.
//
//	"**It blocks on conflict and never steals.** A path held by another live
//	 attempt comes back `CONFLICT_LOCK_TAKEN` with the holder named; nothing is
//	 taken partially."
//	    -> TestAcquireLocksConflictNamesTheHolderAndTakesNothing
//
// 🔴 WHAT THE EXISTING ARMS COVER, AND THE TWO CLAUSES THEY DO NOT.
// run_attempts_test.go's TestAcquireLocksInsertSQL_NoSteal reads the INSERT and
// holds "never steals" — the statement is ON CONFLICT DO NOTHING, so a held row
// is never overwritten. TestAcquireLocksCollisionSQL_LiveAttemptPredicate and
// _NotRunningOnly hold "another LIVE attempt", the predicate that decides which
// holders count.
//
// Neither reaches the other two clauses, and no DB arm does either:
// file_scope_repo_key_db_test.go's TestFileScopeRepoKey_AcquireLocksDoesNotCollide
// AcrossRepos is the only arm that drives FnAcquireLocks against a contended
// key and it asserts the NEGATIVE direction — that two repos do not collide. It
// has no positive control, unlike its own sibling
// TestFileScopeRepoKey_PredictRule1NoHardBlockAcrossRepos, which drives both.
// So on this route "the holder is named" and "nothing is taken partially" were
// asserted nowhere: a refusal with an empty details map, or one that committed
// the locks it had already inserted before hitting the contended one, would
// leave every arm in the repo green.
//
// Both are properties of the FUNCTION's shape rather than of one call's answer,
// which is why this is an AST arm and not a fixture. "Nothing is taken
// partially" is not observable from a single refused call at all — the caller
// sees an error either way, and the difference is whether the transaction that
// had already inserted the free targets reached its commit. That is a question
// about where the return sits, and the AST is where the answer is.
//
// ⚠️ Stated limit: this arm reads structure, so it goes red on a
// behaviour-preserving refactor that moves the refusal into a helper. The card
// names the endpoint, so that is a card edit; the message says what was looked
// for.
//
// No database:
//
//	GOWORK=off go test ./internal/domain/ -run TestAcquireLocksConflictNamesTheHolder -count=1

import (
	"go/ast"
	"go/token"
	"strconv"
	"testing"
)

// acquireLocksConflictDetails are the fields the conflict payload has to name.
// They are the established shape — commit_locks_test.go's
// TestCommitLockConflictErr_NamesEveryHolder calls conflict_with "the shape
// claim and acquire_locks already publish" — so this arm is what makes that
// cross-reference true of this endpoint rather than only of the commit gate.
var acquireLocksConflictDetails = []string{"attempt_id", "actor_display", "work_item_slug"}

// acquireLocksConflictReturns is how many places in FnAcquireLocks can answer
// CONFLICT_LOCK_TAKEN: the pre-insert probe that finds a live foreign holder,
// and the re-check after the insert's ON CONFLICT DO NOTHING took nothing.
//
// 🔴 An EQUALITY, not a floor, and measured rather than assumed: with a floor of
// one, the mutant that moved the second branch's refusal to another error code
// left this arm green, because the first branch still satisfied every
// assertion. Both branches are reachable — the second is the race the DO NOTHING
// exists for — so a refusal that survives in one of them is not the contract the
// card describes.
const acquireLocksConflictReturns = 2

// TestAcquireLocksConflictNamesTheHolderAndTakesNothing pins the two clauses of
// the card's refusal sentence that no existing arm holds.
//
// MUTANTS (applied to this tree and run; the verdict is what happened):
//
//	M13 enforcement: replace NewErrDetails(...) with NewErr(ErrConflictLockTaken,
//	    ...) in both conflict branches          RED  the_refusal_names_the_holder
//	                                                 (no conflict_with payload)
//	M14 enforcement: drop "actor_display" from the conflict_with map
//	                                            RED  the_refusal_names_the_holder,
//	                                                 naming the missing field
//	M15 enforcement: answer the SECOND branch's conflict under ErrInternalError
//	                                            RED  the return count (1, want 2)
//	   ⚠️ GREEN on the first version of this arm, which treated the returns as a
//	   floor of one and asserted over whichever it found: the surviving branch
//	   satisfied every per-return assertion. acquireLocksConflictReturns is now an
//	   equality, and that constant exists because of this mutant.
//	M16 enforcement: commit the transaction before the target loop
//	                                            RED  nothing_is_taken_partially
//	                                                 (both refusals now return
//	                                                 after the commit) and the
//	                                                 commit count
//	M17 publication: delete the citations from the card's refusal bullet
//	                                            RED  K12 DEBT_GROWTH; this arm
//	                                                 reads the AST, so K12's
//	                                                 citation binding is its
//	                                                 publication side
func TestAcquireLocksConflictNamesTheHolderAndTakesNothing(t *testing.T) {
	_, file := parseStepGateFile(t, "run_attempts.go")
	fn := stepGateFuncDecl(t, file, "FnAcquireLocks")

	// The conflict returns, the commits, and the rollback, all read out of the
	// one function so a statement somewhere else in the file cannot stand in for
	// any of them.
	var conflictReturns []*ast.ReturnStmt
	var commits []token.Pos
	rollbackDeferred := false
	ast.Inspect(fn, func(n ast.Node) bool {
		switch node := n.(type) {
		case *ast.ReturnStmt:
			names := false
			ast.Inspect(node, func(c ast.Node) bool {
				if id, ok := c.(*ast.Ident); ok && id.Name == "ErrConflictLockTaken" {
					names = true
				}
				return true
			})
			if names {
				conflictReturns = append(conflictReturns, node)
			}
		case *ast.CallExpr:
			if sel, ok := node.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "Commit" {
				commits = append(commits, node.Pos())
			}
		case *ast.DeferStmt:
			if sel, ok := node.Call.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "Rollback" {
				rollbackDeferred = true
			}
		}
		return true
	})

	// FLOOR: with no refusal in the function there is nothing to be right about,
	// and both arms below would agree with an empty walk.
	if len(conflictReturns) == 0 {
		t.Fatalf("FnAcquireLocks contains no return naming ErrConflictLockTaken. The card's first "+
			"hop-4 bullet is that a contended path comes back CONFLICT_LOCK_TAKEN; with no such "+
			"return the endpoint either steals the lock or reports success on a lock it does not "+
			"hold. Commits found: %d, deferred rollback: %v", len(commits), rollbackDeferred)
	}
	if len(conflictReturns) != acquireLocksConflictReturns {
		t.Errorf("FnAcquireLocks answers CONFLICT_LOCK_TAKEN from %d place(s), want %d — see "+
			"acquireLocksConflictReturns. A branch that stopped refusing hands the caller a "+
			"different error, or a 200, for a lock somebody else holds, and the per-return "+
			"assertions below would still pass on whichever branch was left.",
			len(conflictReturns), acquireLocksConflictReturns)
	}
	if len(commits) != 1 {
		t.Errorf("FnAcquireLocks calls Commit %d time(s), want 1 — the ordering assertion below "+
			"compares against the single commit this function is supposed to have", len(commits))
	}

	t.Run("the_refusal_names_the_holder", func(t *testing.T) {
		for _, ret := range conflictReturns {
			keys := map[string]bool{}
			ast.Inspect(ret, func(n ast.Node) bool {
				kv, ok := n.(*ast.KeyValueExpr)
				if !ok {
					return true
				}
				lit, ok := kv.Key.(*ast.BasicLit)
				if !ok || lit.Kind != token.STRING {
					return true
				}
				if s, err := strconv.Unquote(lit.Value); err == nil {
					keys[s] = true
				}
				return true
			})
			if !keys["conflict_with"] {
				t.Errorf("the CONFLICT_LOCK_TAKEN return at %d carries no conflict_with payload.\n"+
					"\"With the holder named\" is the clause a caller acts on: without it the answer "+
					"is that somebody has the lock, and the caller has nobody to talk to and no way "+
					"to tell contention from its own residue.", ret.Pos())
				continue
			}
			for _, field := range acquireLocksConflictDetails {
				if !keys[field] {
					t.Errorf("the conflict_with payload does not name %q. The three fields are the "+
						"shape commit_locks_test.go calls \"the shape claim and acquire_locks already "+
						"publish\", so dropping one here breaks a convention two other refusals are "+
						"written against.", field)
				}
			}
		}
	})

	t.Run("nothing_is_taken_partially", func(t *testing.T) {
		if !rollbackDeferred {
			t.Errorf("FnAcquireLocks does not defer a Rollback.\nThe refusal is total because the " +
				"transaction that had already inserted the free targets never commits; with no " +
				"deferred rollback, an early return leaves that transaction to be resolved by " +
				"whatever happens to the connection next.")
		}
		if len(commits) == 0 {
			t.Skipf("no Commit found; the count arm above reports that")
		}
		for _, ret := range conflictReturns {
			if ret.Pos() > commits[0] {
				t.Errorf("the CONFLICT_LOCK_TAKEN return sits AFTER the commit.\nThe card promises " +
					"nothing is taken partially: a reconcile that derives three targets, takes two " +
					"and finds the third held must leave the attempt holding neither of the two. A " +
					"refusal raised after the commit hands the caller an error over a lock set that " +
					"has already changed, which is the one state no retry can reason about.")
			}
		}
	})
}
