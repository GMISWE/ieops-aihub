package domain

// aihub#621 — every credential verifier compares the secret in constant time.
//
// verifyAttemptCredential (run_attempts.go) guarded its hash comparison with
// subtle.ConstantTimeCompare from the start; verifyAttemptCredentialSimple
// (memory.go, the pf_emit_event path) compared the same two sha256-hex strings
// with a plain `!=`, which short-circuits at the first differing byte and so
// leaks how much of the hash prefix a guessed secret matched. aihub#607 fixed
// that function's DB-failure arm and deliberately left the comparison-timing
// half to this work item.
//
// ─── Why this is a source census and not a timing measurement ───────────────
//
// A behavioural test cannot hold this property: equal/unequal answers are
// identical on both sides of the fix (attempt_status_vocabulary_dbgated_test.go
// already drives accept and refuse for BOTH verifiers against a real database),
// and a wall-clock timing assertion over a sub-microsecond string compare is
// noise — it would flake on any loaded runner without ever distinguishing the
// two implementations. What CAN be held is the same thing
// paused_refusal_scope_test.go's constantTimeCompareGuard already keys on: the
// comparison's shape in the AST. The census walks every credential verifier
// (same prefix, same floor as TestOnlyOneCredentialVerifierRefusesAPausedAttempt,
// so a fourth verifier is in the population the day it is written) and requires
// each one to either carry a subtle.ConstantTimeCompare guard itself or
// delegate its whole check to another verifier in the same population
// (VerifyAttemptCredentialPool, which opens a transaction and calls
// verifyAttemptCredential).
//
// MUTANTS (applied to this tree and run; the verdict is what happened):
//
//	M44 enforcement: revert verifyAttemptCredentialSimple's guard to the plain
//	    `storedHash != secretHash`          RED  naming the verifier that
//	                                             compares without the guard
//	M45 enforcement: delete the ConstantTimeCompare guard from
//	    verifyAttemptCredential (compare the decoded bytes with bytes.Equal)
//	                                        RED  same arm — the census covers
//	                                             the twin, not just the fixed
//	                                             function
//	G2  control:     rename verifyAttemptCredentialSimple's local `secretHash`
//	                                      GREEN  the walk keys on the call's
//	                                             selector name, never on either
//	                                             body's identifiers
//
// No database:
//
//	GOWORK=off go test ./internal/domain/ -run TestEveryCredentialVerifier -count=1

import (
	"go/ast"
	"strings"
	"testing"
)

// TestEveryCredentialVerifierComparesTheSecretInConstantTime is the census.
func TestEveryCredentialVerifierComparesTheSecretInConstantTime(t *testing.T) {
	verifiers := credentialVerifiers(t)

	if len(verifiers) < credentialVerifierFloor {
		t.Fatalf("the walk found %d credential verifier(s) in internal/domain (%v), floor is %d.\n"+
			"\"Every verifier compares in constant time\" over a half-read population is the same "+
			"green as a healthy tree.",
			len(verifiers), verifierNames(verifiers), credentialVerifierFloor)
	}

	direct := 0
	for _, v := range verifiers {
		guarded := containsConstantTimeCompare(v.fn)
		if guarded {
			direct++
			continue
		}
		if !callsAnotherCredentialVerifier(v.fn) {
			t.Errorf("%s [%s] neither compares the secret through subtle.ConstantTimeCompare nor "+
				"delegates to a verifier that does.\nA plain string or byte comparison returns at the "+
				"first differing byte, so response timing tells a caller how long a prefix of the "+
				"stored hash their guess matched — the leak aihub#621 closed in "+
				"verifyAttemptCredentialSimple. Guard the comparison like the other verifiers, or "+
				"route the check through one of them.", v.name, v.file)
		}
	}

	// Control: at least two verifiers must hold the guard DIRECTLY
	// (verifyAttemptCredential and verifyAttemptCredentialSimple today). A tree
	// where everything "delegates" and nothing compares would satisfy the loop
	// above while no constant-time comparison exists anywhere in the population.
	if direct < 2 {
		t.Errorf("only %d credential verifier(s) carry a subtle.ConstantTimeCompare guard directly, "+
			"want at least 2 (the transactional verifier and its pool-level twin). If a comparing "+
			"verifier was refactored into a helper, teach this census the new shape rather than "+
			"deleting the arm.\nWalked: %v", direct, verifierNames(verifiers))
	}
}

// containsConstantTimeCompare reports whether fn's body calls
// subtle.ConstantTimeCompare (matched by selector name, the same key
// constantTimeCompareGuard uses).
func containsConstantTimeCompare(fn *ast.FuncDecl) bool {
	found := false
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		if sel, ok := call.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "ConstantTimeCompare" {
			found = true
		}
		return true
	})
	return found
}

// callsAnotherCredentialVerifier reports whether fn's body calls a function
// whose name matches the census prefix — the delegation shape
// VerifyAttemptCredentialPool uses.
func callsAnotherCredentialVerifier(fn *ast.FuncDecl) bool {
	found := false
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		name := ""
		switch f := call.Fun.(type) {
		case *ast.Ident:
			name = f.Name
		case *ast.SelectorExpr:
			name = f.Sel.Name
		}
		if name != fn.Name.Name && strings.Contains(name, credentialVerifierPrefix) {
			found = true
		}
		return true
	})
	return found
}
