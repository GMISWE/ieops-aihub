package domain

// aihub#543 probe wave 2, lane L11 — the `docs/mcp-cards/pf_pause_attempt.md`
// hop-4 sentences about WHICH CALLS A PAUSE REFUSES, and the Policy sentence
// about why the aihub#441 unification cannot shadow that refusal.
//
//	"**From that moment the server hard-rejects every call that goes through
//	 `verifyAttemptCredential`** with its own distinct code, `ErrAttemptPaused` …"
//	"**A paused attempt retains the right to write timeline events, and that is
//	 the contract** (owner ruling ②, 2026-09-10, `aihub#585` …)"
//	"`ATTEMPT_PAUSED` … is unchanged, still 409, and still reached only by a
//	 caller whose secret is VALID, so the unification cannot shadow it."
//
// 🔴 THE CARD SAID "EVERY CREDENTIAL-CHECKED pf_* CALL" AND THAT WAS FALSE.
// Measured on this tree: internal/domain holds more than one credential
// verifier, and only one of them looks at the attempt's status at all.
// verifyAttemptCredentialSimple — the pf_emit_event path — checks the current
// attempt id, the epoch and the secret hash, and stops. A pause moves neither
// work_items.current_attempt_id nor current_attempt_epoch nor the stored secret
// hash (FnCompleteAttempt writes run_attempts.status, ended_at, pause_reason and
// work_items.status), so every one of those three checks still passes and
// pf_emit_event is NOT refused after a pause.
//
// That asymmetry is the residue of aihub#441 T2-3 itself: the row unified the
// two verifiers' answer for a wrong SECRET and left their status handling
// unequal, which errors.go's own comment on ErrAttemptMismatch describes for the
// secret half and says nothing about for this one. Lane L11 corrected the card's
// sentence rather than the code, because widening the Simple verifier is a
// behaviour change on pf_emit_event and that lane was a documentation lane; the
// behaviour question it filed as aihub#585 was then RULED, option ②, by the
// owner on 2026-09-10: KEEP the behaviour — a paused attempt retains
// event-writing rights by design (pausing hands a wi to a human, and the note
// that says why often lands after the pause; the 2026-09-10 close-out paused
// aihub#543 and then wrote its checkpoint note through exactly this path). Both
// cards now state that grant affirmatively, this census is its structural pin,
// and paused_attempt_emit_event_dbgated_test.go
// (TestPausedAttemptStillWritesTimelineEvents) drives it end-to-end against a
// real database.
//
// ─── Why this is a source census and not a behavioural arm ────────────────
//
// The claim is "exactly one of the credential verifiers", which is a statement
// about a POPULATION. A behavioural arm can only drive the verifiers somebody
// thought to name, so a THIRD verifier added later — the shape this whole family
// exists to catch — would leave every driven case green while making the
// sentence false again. The walk enumerates the verifiers out of the AST, so a
// new one is in the population the day it is written.
//
// The ordering half is structural for the same reason: "reached only by a caller
// whose secret is VALID" is a property of where the branch SITS relative to the
// constant-time comparison, and a test that drove a wrong secret would observe
// the answer without establishing that the ordering is what produced it.
//
// No database:
//
//	GOWORK=off go test ./internal/domain/ -run TestOnlyOneCredentialVerifier -count=1

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// credentialVerifierPrefix is how a credential verifier is recognised. A prefix
// rather than a list of names, because the population is the point: three
// functions match today and the arm must see a fourth without being edited.
const credentialVerifierPrefix = "erifyAttemptCredential"

// pausedRefusalVerifier is the ONE verifier that examines the attempt's status
// and can therefore answer ATTEMPT_PAUSED.
const pausedRefusalVerifier = "verifyAttemptCredential"

// credentialVerifierFloor is how many verifiers the walk must find. An AST walk
// pointed at the wrong root finds none, and "exactly one of them refuses a
// paused attempt" is then a statement about an empty set — which is the same
// green as a healthy tree.
const credentialVerifierFloor = 3

// verifierDecl is one credential verifier as the AST sees it.
type verifierDecl struct {
	name          string
	file          string
	fn            *ast.FuncDecl
	pausedReturns []token.Pos // positions of NewErr(ErrAttemptPaused, …) inside it
}

// TestOnlyOneCredentialVerifierRefusesAPausedAttempt is the corrected sentence,
// in the three parts a caller acts on.
//
// MUTANTS (applied to this tree and run; the verdict is what happened):
//
//	M36 enforcement: add the paused branch to verifyAttemptCredentialSimple —
//	    i.e. unify the two verifiers
//	                                        RED  exactly_one_verifier_refuses_a
//	                                             _paused_attempt, naming the
//	                                             second one. 🔴 That is the
//	                                             INTENDED red: since the owner
//	                                             ruled the asymmetry IS the
//	                                             contract (option ②, 2026-09-10,
//	                                             aihub#585), unifying is
//	                                             overturning a ruling — the
//	                                             affirmative grant in both cards
//	                                             becomes false and must be
//	                                             deleted in the same diff, citing
//	                                             a new ruling. The failure
//	                                             message says so. Re-run RED
//	                                             2026-09-10 under aihub#585.
//	M37 enforcement: delete the `storedStatus == "paused"` branch from
//	    verifyAttemptCredential             RED  exactly_one_verifier_refuses_a
//	                                             _paused_attempt (zero found) —
//	                                             and the floor keeps that from
//	                                             reading as a broken walk
//	M38 enforcement: move the paused branch ABOVE the ConstantTimeCompare guard
//	                                        RED  the_paused_branch_is_below_the
//	                                             _secret_check
//	M39 enforcement: map ErrAttemptPaused to 403 in codeToHTTPStatus
//	                                        RED  attempt_paused_is_still_a_409
//	M40 publication: delete the citation clause from the card's hop 4 bullet
//	                                        RED  K12 DEBT_GROWTH
//	G1  control:     rename verifyAttemptCredentialSimple's local `secretHash`
//	                                      GREEN  the walk keys on the function
//	                                             name and on ErrAttemptPaused, not
//	                                             on either body's internals
func TestOnlyOneCredentialVerifierRefusesAPausedAttempt(t *testing.T) {
	verifiers := credentialVerifiers(t)

	if len(verifiers) < credentialVerifierFloor {
		t.Fatalf("the walk found %d credential verifier(s) in internal/domain (%v), floor is %d.\n"+
			"Every assertion below is about which of them refuses a paused attempt, and a walk "+
			"that found too few would answer \"exactly one\" about a population it had only half "+
			"read. Current value: %d.",
			len(verifiers), verifierNames(verifiers), credentialVerifierFloor, len(verifiers))
	}

	t.Run("exactly_one_verifier_refuses_a_paused_attempt", func(t *testing.T) {
		var refusing []string
		for _, v := range verifiers {
			if len(v.pausedReturns) > 0 {
				refusing = append(refusing, v.name)
			}
		}
		sort.Strings(refusing)
		if len(refusing) != 1 || refusing[0] != pausedRefusalVerifier {
			t.Errorf("the credential verifiers that answer ErrAttemptPaused are %v, and the cards "+
				"say exactly one does: %s.\n\nIf a verifier was ADDED to that list, the behaviour "+
				"just became uniform — and that is OVERTURNING AN OWNER RULING, not a cleanup: "+
				"option ② (2026-09-10, aihub#585) keeps a paused attempt's event-writing rights "+
				"by design. Cite a new ruling, delete the affirmative grant bullets from "+
				"docs/mcp-cards/pf_pause_attempt.md AND docs/mcp-cards/pf_emit_event.md in this "+
				"same diff, retire TestPausedAttemptStillWritesTimelineEvents (which is also red "+
				"right now), and update this arm's expectation.\n\nIf the list is EMPTY, a paused "+
				"attempt is no longer refused anywhere: a step loop that pauses can then go on "+
				"advancing step state, which is the corruption aihub#209's distinct code exists "+
				"to make impossible.\n\nWalked: %v",
				refusing, pausedRefusalVerifier, verifierNames(verifiers))
		}
	})

	t.Run("the_paused_branch_is_below_the_secret_check", func(t *testing.T) {
		var target *verifierDecl
		for i := range verifiers {
			if verifiers[i].name == pausedRefusalVerifier {
				target = &verifiers[i]
			}
		}
		if target == nil || len(target.pausedReturns) == 0 {
			t.Fatalf("%s does not answer ErrAttemptPaused; the ordering assertion has no subject",
				pausedRefusalVerifier)
		}
		guard := constantTimeCompareGuard(t, target.fn)
		for _, pos := range target.pausedReturns {
			if pos <= guard.End() {
				t.Errorf("the ErrAttemptPaused return sits at or above the constant-time secret "+
					"comparison in %s.\nThe card's Policy bullet rests on the opposite: the paused "+
					"branch is reached only by a caller whose secret is VALID, which is why "+
					"aihub#441 unifying the invalid-credential class to 403 ATTEMPT_MISMATCH "+
					"cannot shadow it. Above that guard, a WRONG secret on a paused attempt would "+
					"answer \"resume it\" — telling a caller to keep and re-use a credential that "+
					"can never work.", pausedRefusalVerifier)
			}
		}
	})

	t.Run("attempt_paused_is_still_a_409", func(t *testing.T) {
		// Compiled, not parsed: this half is about the map's value, and the card
		// states the number.
		if got := codeToHTTPStatus(ErrAttemptPaused); got != 409 {
			t.Errorf("ErrAttemptPaused maps to %d, and the card says 409.\nThe status is half the "+
				"classification a client keys on — internal/mcp renders \"aihub <status> <CODE>\" "+
				"— and 403 in particular is the invalid-credential class this code must stay "+
				"outside of.", got)
		}
		if got := codeToHTTPStatus(ErrAttemptMismatch); got != 403 {
			t.Errorf("CONTROL: ErrAttemptMismatch maps to %d, want 403. Without this, the arm "+
				"above is satisfied by a table that answers 409 for everything, and \"distinct "+
				"from a stale credential\" is the claim.", got)
		}
	})
}

// credentialVerifiers parses every non-test .go file in this package and returns
// the credential verifiers it declares.
func credentialVerifiers(t *testing.T) []verifierDecl {
	t.Helper()
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatalf("glob: %v", err)
	}
	fset := token.NewFileSet()
	var out []verifierDecl
	scanned := 0
	for _, path := range files {
		if strings.HasSuffix(path, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", path, err)
		}
		scanned++
		for _, d := range f.Decls {
			fn, ok := d.(*ast.FuncDecl)
			if !ok || fn.Recv != nil || fn.Body == nil {
				continue
			}
			if !strings.Contains(fn.Name.Name, credentialVerifierPrefix) {
				continue
			}
			out = append(out, verifierDecl{
				name: fn.Name.Name, file: path, fn: fn,
				pausedReturns: newErrPositions(fn, "ErrAttemptPaused"),
			})
		}
	}
	if scanned < 10 {
		t.Fatalf("parsed only %d non-test file(s) in internal/domain; the glob is broken and the "+
			"census below would be about almost nothing", scanned)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].name < out[j].name })
	return out
}

// newErrPositions returns the position of every NewErr / NewErrDetails call
// inside fn whose first argument names code.
func newErrPositions(fn *ast.FuncDecl, code string) []token.Pos {
	var out []token.Pos
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok || len(call.Args) == 0 {
			return true
		}
		name, ok := call.Fun.(*ast.Ident)
		if !ok || (name.Name != "NewErr" && name.Name != "NewErrDetails") {
			return true
		}
		if arg, ok := call.Args[0].(*ast.Ident); ok && arg.Name == code {
			out = append(out, call.Pos())
		}
		return true
	})
	return out
}

// constantTimeCompareGuard returns the `if subtle.ConstantTimeCompare(…) != 1`
// statement, which is the secret check the ordering claim is relative to.
func constantTimeCompareGuard(t *testing.T, fn *ast.FuncDecl) *ast.IfStmt {
	t.Helper()
	var found *ast.IfStmt
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		stmt, ok := n.(*ast.IfStmt)
		if !ok {
			return true
		}
		ast.Inspect(stmt.Cond, func(c ast.Node) bool {
			call, ok := c.(*ast.CallExpr)
			if !ok {
				return true
			}
			if sel, ok := call.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "ConstantTimeCompare" {
				found = stmt
			}
			return true
		})
		return true
	})
	if found == nil {
		t.Fatalf("%s contains no subtle.ConstantTimeCompare guard.\nThe secret comparison is what "+
			"the paused branch is ordered against, and a verifier that no longer compares the "+
			"secret in constant time has a bigger problem than this arm's subject.", fn.Name.Name)
	}
	return found
}

func verifierNames(vs []verifierDecl) []string {
	out := make([]string, 0, len(vs))
	for _, v := range vs {
		marker := ""
		if len(v.pausedReturns) > 0 {
			marker = " (refuses paused)"
		}
		out = append(out, v.name+" ["+v.file+"]"+marker)
	}
	return out
}
