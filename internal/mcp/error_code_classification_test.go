package mcp

// aihub#414: an aihub error code must be classified from the CODE FIELD, never
// from a substring of the rendered error text.
//
// THE DEFECT. pkg/client rendered every API error as
// "aihub <status> <CODE>: <message><details>" and returned it as a plain error.
// The code, the message and the details therefore shared one string — and the
// message and details carry observed values: step names, ids, file paths,
// project names. Two classifiers then ran strings.Contains over that string, so
// any observed value containing a code-shaped token flipped their answer.
//
// WHY IT MATTERED MOST AT classifyStepUpdateErr. Its mismatch arm sets
// deleteState, and the caller acts on it by removing the local credential file.
// internal/server/routes_step.go's validateStepIdentity interpolates the
// requested and stored step names into its message four times, so a step NAMED
// "ATTEMPT_MISMATCH" turned a routine step-identity refusal into
// "STALE_LOCAL_CREDENTIAL: state file deleted". The one arm with a destructive
// side effect was the one an observed value could reach.
//
// AND IT HAD ALREADY BENT A DESIGN DECISION, which is the strongest evidence
// that this was live rather than theoretical. aihub#398 wanted
// CONFLICT_STEP_ATTEMPT_MISMATCH for that endpoint — declared, 409-mapped and
// named for it in the design doc — and could not use it, because the string
// contains "ATTEMPT_MISMATCH" and this classifier would have deleted the
// caller's state file. It picked CONFLICT_CAS_FAILED instead and wrote down
// why. A client-side parsing bug had become a constraint on the server's error
// vocabulary.
//
// MUTANTS (the pre-aihub#414 build). Three, and each reddens ONLY its own half
// — measured, and one of them not as first predicted:
//
//   - classifyStepUpdateErr: `msg := err.Error()` + strings.Contains(msg, …).
//     Reddens the three hostile-value arms below and the class gate's Rule A;
//     all six controls and both other tests stay green.
//   - isAihubCode: `strings.Contains(err.Error(), code)`. Reddens exactly three
//     arms of TestIsAihubCode (message token, longer code, plain error) and the
//     class gate's Rule B; the step classifier's arms all stay green.
//   - pkg/client do(): return the fmt.Errorf string instead of &APIError{}.
//     Reddens the hop-1 wiring test AND NOTHING ELSE. That is worth stating
//     plainly, because the first draft of this comment claimed it would redden
//     every classification arm: it does not, since those arms construct an
//     APIError directly rather than going through the client. The hop-1 test is
//     the ONLY thing standing between a client regression and a classifier that
//     silently classifies nothing — which is exactly why it is a separate test
//     and not a convenience.
//
// ⚠️ Each mutant must be made to COMPILE (drop the import that goes unused when
// the typed call disappears). A mutant that fails to build proves nothing about
// the gate — the build error is an artifact of the edit, not a detection.

import (
	"encoding/json"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/GMISWE/ieops-aihub/pkg/client"
)

// apiErr builds the error pkg/client returns for an aihub error envelope.
func apiErr(status int, code, message string) error {
	return &client.APIError{StatusCode: status, Code: code, Message: message}
}

// TestClassifyStepUpdateErr_ClassifiesByCodeNotByMessageText is the wi's named
// scenario plus the sibling case aihub#398 ran into.
func TestClassifyStepUpdateErr_ClassifiesByCodeNotByMessageText(t *testing.T) {
	// The literal step name that used to be enough to destroy a credential
	// file. Written as a constant so the point is unmissable: this is a NAME,
	// chosen by a user, not a code chosen by the server.
	const hostileStepName = "ATTEMPT_MISMATCH"

	t.Run("a step NAMED like a code does not delete the state file", func(t *testing.T) {
		// Exactly what validateStepIdentity produces: a CONFLICT_CAS_FAILED whose
		// message quotes the step names. Nothing here is a stale credential.
		err := apiErr(409, "CONFLICT_CAS_FAILED", fmt.Sprintf(
			"status=%q names step %q, but this work item's current_step is %q. Nothing was committed.",
			"completed", hostileStepName, "code_change"))

		out, deleteState := classifyStepUpdateErr(err)

		if deleteState {
			t.Errorf("classifying a CONFLICT_CAS_FAILED as a stale credential DELETED THE CALLER'S STATE FILE "+
				"because the step is named %q. The code field says CONFLICT_CAS_FAILED; only the message "+
				"contains the token, and a step name is an observed value. got out=%v", hostileStepName, out)
		}
		if out.Error() != err.Error() {
			t.Errorf("an unclassified error must pass through untouched, got %q want %q", out.Error(), err.Error())
		}
	})

	t.Run("CONFLICT_STEP_ATTEMPT_MISMATCH is not a stale credential", func(t *testing.T) {
		// The code aihub#398 was forced to avoid: it CONTAINS "ATTEMPT_MISMATCH".
		// A bad step_attempt_id is not a superseded attempt, so the file stays.
		err := apiErr(409, "CONFLICT_STEP_ATTEMPT_MISMATCH", "step_attempt_id already has a history row")

		out, deleteState := classifyStepUpdateErr(err)

		if deleteState {
			t.Errorf("CONFLICT_STEP_ATTEMPT_MISMATCH deleted the state file because its NAME contains "+
				"ATTEMPT_MISMATCH. This is why aihub#398 could not use its own endpoint's declared code; "+
				"exact comparison is what lifts that constraint. got out=%v", out)
		}
	})

	t.Run("a token in the details blob does not classify either", func(t *testing.T) {
		err := &client.APIError{
			StatusCode: 500,
			Code:       "INTERNAL_ERROR",
			Message:    "boom",
			Details:    json.RawMessage(`{"step":"CONFLICT_EPOCH_MISMATCH"}`),
		}

		out, deleteState := classifyStepUpdateErr(err)

		if deleteState {
			t.Errorf("a code-shaped value in the DETAILS blob deleted the state file; details are echoed "+
				"observed values and formatDetails renders them into the same string. got out=%v", out)
		}
	})

	// ── Controls. These pass on BOTH builds; without them the arms above are
	//    satisfied by a classifier that simply never deletes anything.

	t.Run("CONTROL: ATTEMPT_PAUSED still keeps the file and points at resume", func(t *testing.T) {
		out, deleteState := classifyStepUpdateErr(apiErr(409, "ATTEMPT_PAUSED", "attempt is paused"))
		if deleteState {
			t.Fatal("a paused attempt must KEEP the state file — deleting it is what aihub#209 fixed")
		}
		for _, want := range []string{"paused", "--resume", "state file kept"} {
			if !strings.Contains(out.Error(), want) {
				t.Errorf("paused guidance lost %q: %q", want, out.Error())
			}
		}
	})

	t.Run("CONTROL: CONFLICT_EPOCH_MISMATCH still deletes", func(t *testing.T) {
		out, deleteState := classifyStepUpdateErr(apiErr(409, "CONFLICT_EPOCH_MISMATCH", "claim_epoch mismatch"))
		if !deleteState {
			t.Fatal("a superseded epoch IS a stale credential; not deleting leaves every later call failing")
		}
		if !strings.Contains(out.Error(), "STALE_LOCAL_CREDENTIAL") {
			t.Errorf("lost the STALE_LOCAL_CREDENTIAL marker: %q", out.Error())
		}
	})

	t.Run("CONTROL: ATTEMPT_MISMATCH still deletes", func(t *testing.T) {
		_, deleteState := classifyStepUpdateErr(apiErr(403, "ATTEMPT_MISMATCH", `attempt status is "superseded"`))
		if !deleteState {
			t.Fatal("a genuine ATTEMPT_MISMATCH code must still delete the state file")
		}
	})

	t.Run("CONTROL: an unrelated code passes through unchanged", func(t *testing.T) {
		in := apiErr(500, "INTERNAL_ERROR", "boom")
		out, deleteState := classifyStepUpdateErr(in)
		if deleteState {
			t.Fatal("an unrelated error must not delete the state file")
		}
		if out.Error() != in.Error() {
			t.Errorf("error was rewritten: %q want %q", out.Error(), in.Error())
		}
	})

	t.Run("CONTROL: a plain non-API error classifies as nothing", func(t *testing.T) {
		// A transport failure carries no code. Passing through is the safe
		// direction: losing a resume hint costs a sentence, guessing wrong costs
		// the credential file.
		in := errors.New("http PATCH /v1/work_items/x/step: dial tcp: connection refused")
		out, deleteState := classifyStepUpdateErr(in)
		if deleteState {
			t.Fatal("an error with no server code must never reach the deleting arm")
		}
		if out != in {
			t.Errorf("a plain error must pass through as-is, got %v", out)
		}
	})
}

// TestIsAihubCode_ComparesTheCodeFieldExactly covers the shared helper, whose
// three call sites (tools_coding.go twice, tools_lifecycle.go once) had the
// same defect as classifyStepUpdateErr.
func TestIsAihubCode_ComparesTheCodeFieldExactly(t *testing.T) {
	t.Run("the code itself matches", func(t *testing.T) {
		if !isAihubCode(apiErr(409, "CONFLICT_LOCK_TAKEN", "held by another attempt"), "CONFLICT_LOCK_TAKEN") {
			t.Error("the real code did not match, so every lock-conflict path is now misreported")
		}
	})

	t.Run("a token in the message does NOT match", func(t *testing.T) {
		// tools_coding.go publishes this decision as lock_gate="refused", and the
		// message names every blocked path — so a path is an observed value that
		// used to be able to fake a lock conflict.
		err := apiErr(500, "INTERNAL_ERROR", "could not stat docs/CONFLICT_LOCK_TAKEN.md")
		if isAihubCode(err, "CONFLICT_LOCK_TAKEN") {
			t.Error("a FILE PATH containing the token classified as a lock conflict; lock_gate would " +
				"report \"refused\" for an error that was not a refusal")
		}
	})

	t.Run("a LONGER code containing the shorter one does NOT match", func(t *testing.T) {
		// The same bug with the opposite sign, and not hypothetical: this exact
		// pair is why aihub#398 avoided its endpoint's declared code.
		err := apiErr(409, "CONFLICT_STEP_ATTEMPT_MISMATCH", "step_attempt_id already used")
		if isAihubCode(err, "ATTEMPT_MISMATCH") {
			t.Error("CONFLICT_STEP_ATTEMPT_MISMATCH matched ATTEMPT_MISMATCH by substring")
		}
	})

	t.Run("wrapping with %w still classifies", func(t *testing.T) {
		// Load-bearing: tools_coding.go's g.err is fmt.Errorf("commit refused: %w",
		// err), and it is that wrapped value the lock_gate switch classifies. If
		// this regressed, lock_gate would silently stop reporting "refused".
		wrapped := fmt.Errorf("commit refused: %w", apiErr(409, "CONFLICT_LOCK_TAKEN", "held"))
		if !isAihubCode(wrapped, "CONFLICT_LOCK_TAKEN") {
			t.Error("a %w-wrapped API error stopped classifying, so tools_coding.go's lock_gate would " +
				"report could_not_run for a genuine refusal")
		}
	})

	t.Run("nil and plain errors are not codes", func(t *testing.T) {
		if isAihubCode(nil, "CONFLICT_LOCK_TAKEN") {
			t.Error("nil must not classify")
		}
		if isAihubCode(errors.New("aihub 409 CONFLICT_LOCK_TAKEN: held"), "CONFLICT_LOCK_TAKEN") {
			t.Error("a hand-built string that merely LOOKS like the rendered envelope must not classify — " +
				"the whole point is that text is not the authority")
		}
	})
}

// TestClientReturnsTypedAPIErrorAndRendersIdentically is hop 1, and it exists
// because the classifiers above have NO substring fallback: if pkg/client ever
// went back to returning a rendered string, every one of them would silently
// stop classifying rather than misclassify. Silent is worse here, so it is
// gated.
//
// It also pins the rendered text BYTE-FOR-BYTE. Introducing the type must not
// reformat anything for the logs, tests and older consumers that still read it.
func TestClientReturnsTypedAPIErrorAndRendersIdentically(t *testing.T) {
	const (
		code    = "ATTEMPT_PAUSED"
		message = `attempt status is "paused"`
	)
	details := `{"attempt_id":"ra_x"}`

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(409)
		_, _ = w.Write([]byte(`{"code":"` + code + `","message":"attempt status is \"paused\"","details":` + details + `}`))
	}))
	defer srv.Close()

	_, err := client.New(srv.URL, "k").UpdateStep(t.Context(), "wi_x", map[string]any{"heartbeat": true})
	if err == nil {
		t.Fatal("a 409 response produced no error")
	}

	var apiError *client.APIError
	if !errors.As(err, &apiError) {
		t.Fatalf("UpdateStep returned %T, not a *client.APIError — the code never reaches the classifier, "+
			"which (having no substring fallback) then classifies nothing at all: err=%v", err, err)
	}
	if apiError.Code != code {
		t.Errorf("Code = %q, want %q", apiError.Code, code)
	}
	if apiError.StatusCode != 409 {
		t.Errorf("StatusCode = %d, want 409", apiError.StatusCode)
	}
	if apiError.Message != message {
		t.Errorf("Message = %q, want %q", apiError.Message, message)
	}

	// The legacy rendering, spelled out here rather than derived from the type,
	// so a change to Error() cannot silently agree with itself.
	want := `aihub 409 ATTEMPT_PAUSED: attempt status is "paused" details={"attempt_id":"ra_x"}`
	if got := err.Error(); got != want {
		t.Errorf("rendered error changed shape.\n got: %s\nwant: %s\nAnything still reading this text — logs, "+
			"tests, an older consumer of pkg/client — sees a different string than before, which this change "+
			"was supposed to avoid entirely", got, want)
	}

	// The no-details shape is the COMMON one, and it renders differently (no
	// " details=" suffix at all), so byte-identity has to be asserted for both
	// or half the change is unverified.
	bare := (&client.APIError{StatusCode: 404, Code: "PROJECT_NOT_FOUND", Message: "no such project"}).Error()
	if want := "aihub 404 PROJECT_NOT_FOUND: no such project"; bare != want {
		t.Errorf("an error with no details rendered as %q, want %q — formatDetails must contribute nothing "+
			"when the server sent none, exactly as the old fmt.Errorf did", bare, want)
	}

	// And the classifier consumes it end to end: hop 1 feeding hop 2.
	if _, deleteState := classifyStepUpdateErr(err); deleteState {
		t.Error("a paused attempt classified as a stale credential through the real client error")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// The class gate.

// codeShaped matches an aihub error-code literal: SCREAMING_SNAKE_CASE with at
// least one underscore. Every code in internal/domain/errors.go has that shape,
// and ordinary prose literals ("already exists") do not.
var codeShaped = regexp.MustCompile(`^[A-Z][A-Z0-9]*(_[A-Z0-9]+)+$`)

// substringMatchers are the string operations that ask "does this text contain
// that". Matching an error's rendered text with any of them is the defect;
// which one it is makes no difference.
var substringMatchers = map[string]bool{
	"Contains": true, "HasPrefix": true, "HasSuffix": true, "Index": true,
	"ContainsAny": true, "LastIndex": true,
}

// errTextMatchExemptions are the sites allowed to match an error's TEXT,
// keyed "<file>:<the call, whitespace-collapsed>" with the reason.
//
// It lives inside the guard rather than as an annotation next to the code, on
// the same principle as conflictGuardExemptions in internal/domain: the
// cheapest way to silence a gate must never be cheaper than obeying it. An
// exemption costs an edit here and a written reason; classifying properly costs
// nothing, because a typed code is already available.
//
// Note what is NOT exemptible: Rule A. A code always has a field to compare
// instead, so there is no legitimate reason to match one as a substring.
//
// ⚠️ Rule A keys on the LITERAL's shape and ignores what is being searched, so
// it would also fire on a SCREAMING_SNAKE literal matched against something
// that is not an error at all — a log line, a header, an env var. There are
// zero such sites in this package today (measured: Rule A reports nothing on
// the fixed tree), which is why no allowlist exists yet. If a genuine one
// appears, NARROW THE DETECTOR — restrict arg[0] to error-derived expressions —
// rather than adding an exemption. A false positive means the predicate is
// wrong, and an exemption would preserve the wrong predicate while hiding the
// only evidence of it.
var errTextMatchExemptions = map[string]string{
	`tools_lifecycle.go:strings.Contains(err.Error(), "already exists")`:      "git's own stderr from `git worktree add`, not an aihub envelope: there is no code field to compare, and the phrase is not a code.",
	`tools_lifecycle.go:strings.Contains(err.Error(), "already checked out")`: "same git stderr as above.",
	`tools_coding.go:strings.Contains(err.Error(), coding.BaseMovedMarker)`:   "a marker string this repo defines and puts in the error itself (internal/coding), so the text IS the contract rather than an observed value. Not a server code.",
}

// SCOPE: internal/mcp only, and that is the whole class rather than the part
// this change touched. Verified with an inverse-filter negative control,
// because an exclusion filter that silently matches nothing reports a clean
// repo. If classification of an aihub code ever appears in another package,
// widen the glob rather than trusting this note.
//
// 🔴 RE-MEASURED 2026-09-07 for aihub#409, and the re-measurement was not
// optional. The previous number was taken with a detector that could not see
// the hoisted form (`msg := err.Error()` then strings.Contains(msg, code)), so
// it was evidence about the OLD recogniser's reach, not about the repo — a
// widened detector can turn a measured "none anywhere else" into a false
// statement without anything going red, because this note is prose and the gate
// only reads internal/mcp.
//
// Measured by running findErrCodeSubstringMatches, the same detector the gate
// runs, over every non-test .go file in the repo: 102 files scanned, 4 findings,
// all four in internal/mcp and all four already covered by the three
// errTextMatchExemptions entries below (the "already exists" key matches two
// sites). Zero Rule A findings anywhere. So the conclusion is unchanged and the
// reason it is unchanged is now the current reason rather than a stale one.
func TestNoAihubErrorCodeIsClassifiedBySubstring(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatalf("glob: %v", err)
	}

	scanned := 0
	// A SET of the exemption keys actually observed, not a count of sites: one
	// key can legitimately match several call sites ("already exists" appears at
	// two places in tools_lifecycle.go), so counting sites made the invariant
	// below wrong in a way that only showed up when it fired. What must hold is
	// that every listed exemption is still real.
	exemptedSeen := map[string]bool{}

	for _, file := range files {
		if strings.HasSuffix(file, "_test.go") {
			// Tests legitimately construct and assert on rendered text — this very
			// file does. The defect is in production classification.
			continue
		}
		src, readErr := os.ReadFile(file)
		if readErr != nil {
			t.Fatalf("read %s: %v", file, readErr)
		}
		findings, scanErr := findErrCodeSubstringMatches(src, file)
		if scanErr != nil {
			t.Fatalf("%v", scanErr)
		}
		scanned++

		for _, f := range findings {
			if f.Rule == ruleCodeLiteral {
				t.Errorf(`%s:%d: matches the error code %q as a SUBSTRING.
An aihub error's rendered text is "aihub <status> <CODE>: <message><details>", so the code shares one
string with every observed value the server echoed back — step names, ids, paths. Compare the code
field instead: client.IsCode(err, %q) / isAihubCode(err, %q). (aihub#414)
  %s`, file, f.Line, f.Value, f.Value, f.Value, f.CallSrc)
				continue
			}
			key := filepath.Base(file) + ":" + f.CallSrc
			if _, exempt := errTextMatchExemptions[key]; exempt {
				exemptedSeen[key] = true
				continue
			}
			t.Errorf(`%s:%d: classifies on an error's RENDERED TEXT.
Observed values (step names, ids, paths) are interpolated into that text, so any of them can flip this
decision — which is how a step named "ATTEMPT_MISMATCH" came to delete the caller's credential file.
Use client.IsCode / isAihubCode against the typed code, or add an entry to errTextMatchExemptions in
this file with a reason if the text genuinely is the contract. (aihub#414)
  %s`, file, f.Line, f.CallSrc)
		}
	}

	// ── Anti-vacuity. A scanner that parsed nothing, or whose detector stopped
	//    matching, exits exactly like a clean package.
	if scanned < 5 {
		t.Fatalf("only %d non-test file(s) scanned in internal/mcp — the glob or the skip is wrong, and "+
			"this gate would pass on a package it never read", scanned)
	}
	for key := range errTextMatchExemptions {
		if !exemptedSeen[key] {
			t.Errorf("exemption %q matched nothing. Either the detector stopped finding that shape — in "+
				"which case it would also stop finding new violations and this gate is dead — or the site "+
				"is gone and this entry should go with it, which is the reviewable record that the "+
				"exemption is no longer needed", key)
		}
	}
}

// ─── The detector, extracted (aihub#409) ─────────────────────────────────────

// The two rule names, so a finding says which rule produced it rather than
// leaving the caller to infer it from which fields are populated.
const (
	ruleCodeLiteral = "A" // a code-shaped literal matched as a substring
	ruleErrorText   = "B" // the haystack is an error's rendered text
)

// errCodeFinding is one violation site.
type errCodeFinding struct {
	Rule    string
	Line    int
	Value   string // rule A only: the code-shaped literal
	CallSrc string
}

// findErrCodeSubstringMatches is THE detector — the single copy, called by the
// repo gate above and by the fixture self-tests below.
//
// 🔴 Extracted by aihub#409 for the reason work_item_ref_policy_test.go states
// about its own detector: a self-test that re-implements the walk measures a
// COPY, and the copy can keep passing while the detector the gate actually runs
// goes blind. Before this, the walk lived inline in the gate and the only way
// to exercise it was to put a violation into a real non-test file, so the
// hoisted-variable hole (aihub#409) could not be covered by a fixture at all —
// which is part of why it survived aihub#414.
func findErrCodeSubstringMatches(src []byte, name string) ([]errCodeFinding, error) {
	fset := token.NewFileSet()
	parsed, err := parser.ParseFile(fset, name, src, 0)
	if err != nil {
		return nil, fmt.Errorf("parse %s: %w", name, err)
	}
	errTextVars := errTextVarsByFunc(parsed)

	var out []errCodeFinding
	ast.Inspect(parsed, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || !substringMatchers[sel.Sel.Name] {
			return true
		}
		if pkg, ok := sel.X.(*ast.Ident); !ok || pkg.Name != "strings" {
			return true
		}
		if len(call.Args) < 2 {
			return true
		}
		callSrc := strings.Join(strings.Fields(string(
			src[fset.Position(call.Pos()).Offset:fset.Position(call.End()).Offset])), " ")
		line := fset.Position(call.Pos()).Line

		// Rule A — a code-shaped literal is being matched as a substring.
		// Unexemptible: if it is a code, the code field exists.
		if lit, ok := call.Args[1].(*ast.BasicLit); ok && lit.Kind == token.STRING {
			if value := strings.Trim(lit.Value, "`\""); codeShaped.MatchString(value) {
				out = append(out, errCodeFinding{Rule: ruleCodeLiteral, Line: line, Value: value, CallSrc: callSrc})
				return true
			}
		}

		// Rule B — the haystack is an error's rendered text at all. Catches the
		// helper shape, where the needle is a variable and Rule A is blind:
		// strings.Contains(err.Error(), code).
		//
		// aihub#409: "the haystack is an error's rendered text" is a fact about
		// the VALUE, not about how the expression is spelled, so requiring a
		// literal .Error() call in argument position let the hoisted form walk
		// past BOTH rules — Rule A needs a code-shaped literal needle, which the
		// helper shape does not have, and Rule B needed a syntactic call, which
		// `msg := err.Error()` moves one line up:
		//
		//	msg := err.Error()
		//	if strings.Contains(msg, code) { ... }
		//
		// Identical behaviour, zero coverage.
		if isErrorTextExpr(call.Args[0], errTextVars, call.Pos()) {
			out = append(out, errCodeFinding{Rule: ruleErrorText, Line: line, CallSrc: callSrc})
		}
		return true
	})
	return out, nil
}

// ─── aihub#409: seeing the rendered text through a variable ──────────────────

// errTextScope is one function's body range plus the identifiers assigned from
// a zero-argument .Error() call inside it.
type errTextScope struct {
	start, end token.Pos
	vars       map[string]bool
}

// errTextVarsByFunc collects, per function, every identifier that is assigned
// an error's rendered text.
//
// Assignment only — `msg := err.Error()` and `msg = err.Error()`. Not tracked:
// a value passed into another function as a parameter, or stored in a struct
// field, or built by concatenation. Those are real remaining holes, and they
// are named here rather than implied away, because the lesson of this work item
// is that a detector's stated reach is worth exactly what pins it: the reach
// asserted below is one hop through a local variable, and no more.
func errTextVarsByFunc(file *ast.File) []errTextScope {
	var out []errTextScope
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Body == nil {
			continue
		}
		scope := errTextScope{start: fn.Body.Pos(), end: fn.Body.End(), vars: map[string]bool{}}
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			as, ok := n.(*ast.AssignStmt)
			if !ok {
				return true
			}
			for i, rhs := range as.Rhs {
				if i >= len(as.Lhs) || !isErrorMethodCall(rhs) {
					continue
				}
				if id, ok := as.Lhs[i].(*ast.Ident); ok && id.Name != "_" {
					scope.vars[id.Name] = true
				}
			}
			return true
		})
		if len(scope.vars) > 0 {
			out = append(out, scope)
		}
	}
	return out
}

// isErrorMethodCall reports whether e is a zero-argument .Error() call.
func isErrorMethodCall(e ast.Expr) bool {
	call, ok := e.(*ast.CallExpr)
	if !ok || len(call.Args) != 0 {
		return false
	}
	sel, ok := call.Fun.(*ast.SelectorExpr)
	return ok && sel.Sel.Name == "Error"
}

// isErrorTextExpr reports whether the haystack expression is an error's
// rendered text: written inline as `err.Error()`, or an identifier that was
// assigned one earlier in the same function.
func isErrorTextExpr(arg ast.Expr, scopes []errTextScope, at token.Pos) bool {
	if isErrorMethodCall(arg) {
		return true
	}
	id, ok := arg.(*ast.Ident)
	if !ok {
		return false
	}
	for _, sc := range scopes {
		if at >= sc.start && at < sc.end && sc.vars[id.Name] {
			return true
		}
	}
	return false
}

// TestErrCodeDetectorSeesTheHoistedForm is aihub#409's arm, and the first
// fixture coverage this detector has ever had.
//
// Rule A keys on a code-shaped LITERAL needle; Rule B used to key on a literal
// `.Error()` call in haystack position. The helper shape has neither — the
// needle is a variable, and one `msg :=` moves the call out of the argument.
// Both rules therefore returned nothing on code that classifies an aihub error
// by its rendered text, which is the defect aihub#414 existed to remove.
//
// Every row is checked through findErrCodeSubstringMatches, the function the
// repo gate above calls. Asserting against a re-implemented walk would measure a
// copy, which is the failure mode the extraction comment on that function
// describes.
func TestErrCodeDetectorSeesTheHoistedForm(t *testing.T) {
	cases := []struct {
		name     string
		src      string
		wantRule string // "" means: must report nothing
	}{
		{
			// THE aihub#409 shape. Red before this change.
			name: "hoisted err.Error() into a variable",
			src: `package p

import "strings"

func classify(err error, code string) bool {
	msg := err.Error()
	return strings.Contains(msg, code)
}
`,
			wantRule: ruleErrorText,
		},
		{
			// Same, with = rather than := — a separate AST shape, and the one a
			// second assignment in a longer function produces.
			name: "hoisted via plain assignment",
			src: `package p

import "strings"

func classify(err error, code string) bool {
	var msg string
	msg = err.Error()
	return strings.HasPrefix(msg, code)
}
`,
			wantRule: ruleErrorText,
		},
		{
			// The shape that already worked. Kept so a future edit that fixes the
			// hoisted case by BREAKING the inline case cannot pass.
			name: "inline err.Error() still fires",
			src: `package p

import "strings"

func classify(err error, code string) bool {
	return strings.Contains(err.Error(), code)
}
`,
			wantRule: ruleErrorText,
		},
		{
			// Rule A, unchanged by this work item, asserted because the
			// extraction moved it.
			name: "code-shaped literal still fires as rule A",
			src: `package p

import "strings"

func classify(s string) bool {
	return strings.Contains(s, "CONFLICT_LOCK_TAKEN")
}
`,
			wantRule: ruleCodeLiteral,
		},
		{
			// NEGATIVE CONTROL. The tracking must be about .Error() specifically,
			// not about "a string variable was passed". Without this row, a
			// detector that treated every identifier as error text would satisfy
			// all three positive rows above and flag most of the package.
			name: "a variable that never held an error is not error text",
			src: `package p

import "strings"

func classify(h map[string]string, code string) bool {
	msg := h["x-request-id"]
	return strings.Contains(msg, code)
}
`,
			wantRule: "",
		},
		{
			// NEGATIVE CONTROL for the function scoping. `msg` is as ordinary a
			// name as exists; a file-wide set would make any function that
			// happens to use it report a violation it does not contain.
			name: "the assignment is in a different function",
			src: `package p

import "strings"

func a(err error) string {
	msg := err.Error()
	return msg
}

func b(msg, code string) bool {
	return strings.Contains(msg, code)
}
`,
			wantRule: "",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := findErrCodeSubstringMatches([]byte(tc.src), "fixture.go")
			if err != nil {
				t.Fatalf("detector failed on the fixture: %v", err)
			}
			if tc.wantRule == "" {
				if len(got) != 0 {
					t.Fatalf("detector reported %d finding(s) on a fixture that classifies nothing: %+v\n%s",
						len(got), got, tc.src)
				}
				return
			}
			if len(got) != 1 {
				t.Fatalf("detector reported %d finding(s), want exactly 1 (rule %s).\n%s",
					len(got), tc.wantRule, tc.src)
			}
			if got[0].Rule != tc.wantRule {
				t.Errorf("finding is rule %s, want rule %s — the rules are not interchangeable: "+
					"rule A is unexemptible and rule B is not", got[0].Rule, tc.wantRule)
			}
		})
	}
}

// TestErrCodeDetectorStillSeesTheExemptedSites is the "an exempt target
// survives" control, at the detector level rather than the gate level.
//
// The three errTextMatchExemptions entries are only meaningful while the
// detector still FINDS those sites — an exemption for something nothing detects
// is an entry that silently pre-approves the shape's return. The gate above has
// an arm for that, but it reads the real package, so it cannot distinguish "the
// detector found it and the exemption matched" from a detector change that
// happens to leave the count intact. This pins the shape itself.
func TestErrCodeDetectorStillSeesTheExemptedSites(t *testing.T) {
	const src = `package p

import "strings"

func f(err error, marker string) bool {
	if strings.Contains(err.Error(), "already exists") {
		return true
	}
	if strings.Contains(err.Error(), "already checked out") {
		return true
	}
	return strings.Contains(err.Error(), marker)
}
`
	got, err := findErrCodeSubstringMatches([]byte(src), "fixture.go")
	if err != nil {
		t.Fatalf("detector failed: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("detector reported %d finding(s) on the three exempted shapes, want 3: %+v\n"+
			"    If it stops seeing them, every errTextMatchExemptions entry becomes an "+
			"exemption for a shape nothing detects — which pre-approves its return.", len(got), got)
	}
	for _, f := range got {
		if f.Rule != ruleErrorText {
			t.Errorf("expected rule %s for an err.Error() haystack, got %s (%s)",
				ruleErrorText, f.Rule, f.CallSrc)
		}
	}
}
