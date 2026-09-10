package domain

// aihub#543 probe wave 1, slice C — the two `docs/mcp-cards/pf_complete_attempt.md`
// sentences whose subject is the SHAPE of a decision rather than the answer to
// one call.
//
//	"**A step still `in_progress` fails the completion on `wrapped` and
//	 `failed`** unless `force_terminate_step` is set — but **`paused` does not
//	 need the flag.** The H-R9-11 block … force-terminates a live step when
//	 `status="paused"` OR the flag is set, and refuses with
//	 `ErrConflictStepInProgress` only otherwise, so the flag is load-bearing on
//	 the two terminal statuses alone."
//	    -> TestTheStepInProgressRefusalIsGatedOnPausedOrTheFlagAlone
//	"…the column's only reader is `internal/domain/work_items.go`
//	 (`GetReadyQueue`), whose paused segment filters `wi.status = 'paused'`…"
//	    -> TestPauseReasonHasExactlyOneReader
//
// 🔴 WHY NEITHER IS A DATABASE ARM. Both sentences quantify over a POPULATION
// rather than describing one call's outcome, and a DB arm sees one call. "The
// flag is load-bearing on the two terminal statuses ALONE" is a statement about
// all three statuses and about every other read of the field; a second read
// added elsewhere — a status the flag also unlocked, a route that consumed it
// early — would leave a per-status behavioural arm green on every status it
// drove. "The column's only reader" is the same shape one layer down: a second
// SELECT would satisfy every arm that observes the first.
//
// The AST rather than a regexp, because the questions are structural: is this
// literal one operand of that disjunction, does the else branch return that
// error, is this string a SELECT or a struct tag. A text scan answers those by
// proximity, and the guards in declared_resources_wiring_test.go say in as many
// words what that costs — it measures TEXT and cannot see a call whose result
// is dropped.
//
// ⚠️ Shared limit, stated rather than left to be discovered: both arms go red
// on a behaviour-preserving rename or refactor. That is the intended direction
// — the card names the block and the function, so a rename is a card edit — and
// the messages say what was looked for so the reader is not left diagnosing the
// harness.
//
// No database:
//
//	GOWORK=off go test ./internal/domain/ -run 'TestTheStepInProgressRefusal|TestPauseReasonHasExactlyOneReader' -count=1

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// stepGateStatusVocabulary is what FnCompleteAttempt's vocabulary must turn out
// to be. It is written here so the count arithmetic in the arm below — three
// statuses, one of them force-terminating unconditionally, therefore TWO where
// the flag decides — is checked rather than assumed.
var stepGateStatusVocabulary = []string{"failed", "paused", "wrapped"}

// parseStepGateFile parses one non-test file of this package.
func parseStepGateFile(t *testing.T, name string) (*token.FileSet, *ast.File) {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, name, nil, parser.ParseComments)
	if err != nil {
		t.Fatalf("parse %s: %v", name, err)
	}
	return fset, f
}

// stepGateFuncDecl returns the named top-level function's declaration.
func stepGateFuncDecl(t *testing.T, file *ast.File, name string) *ast.FuncDecl {
	t.Helper()
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if ok && fn.Recv == nil && fn.Name.Name == name {
			return fn
		}
	}
	t.Fatalf("could not find func %s — was it renamed? The card names it, so a rename is a "+
		"card edit and this guard is where that is noticed", name)
	return nil
}

// stepGateFieldRead reports whether n is the expression `<ident>.<field>`.
func stepGateFieldRead(n ast.Node, field string) bool {
	sel, ok := n.(*ast.SelectorExpr)
	return ok && sel.Sel.Name == field
}

// stepGateRequestField is the narrow form: `req.<field>` and nothing else.
//
// 🔴 Required for the status vocabulary, and measured rather than assumed.
// FnCompleteAttempt compares FOUR status literals — the fourth is `cancelled`,
// and it belongs to `wi.Status`, the work item's own state, not to the request.
// A receiver-blind walk reads the vocabulary as four wide, and the card's "the
// two terminal statuses" then becomes arithmetic over the wrong set.
func stepGateRequestField(n ast.Node, field string) bool {
	sel, ok := n.(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != field {
		return false
	}
	recv, ok := sel.X.(*ast.Ident)
	return ok && recv.Name == "req"
}

// stepGateStatusLiteral returns the string a `<ident>.Status <op> "lit"` comparison
// names, and whether e is one.
func stepGateStatusLiteral(n ast.Node) (string, token.Token, bool) {
	bin, ok := n.(*ast.BinaryExpr)
	if !ok {
		return "", token.ILLEGAL, false
	}
	if bin.Op != token.EQL && bin.Op != token.NEQ {
		return "", bin.Op, false
	}
	if !stepGateRequestField(bin.X, "Status") {
		return "", bin.Op, false
	}
	lit, ok := bin.Y.(*ast.BasicLit)
	if !ok || lit.Kind != token.STRING {
		return "", bin.Op, false
	}
	s, err := strconv.Unquote(lit.Value)
	if err != nil {
		return "", bin.Op, false
	}
	return s, bin.Op, true
}

// TestTheStepInProgressRefusalIsGatedOnPausedOrTheFlagAlone reads the H-R9-11
// block out of FnCompleteAttempt and checks the three facts the card's sentence
// is built from: what unlocks the force-terminate, what happens otherwise, and
// that no other read of the flag exists to unlock anything else.
//
// The last one is the arm with no substitute. `force_terminate_step` is
// published on this tool and inert on pf_pause_attempt, and the universal
// contract gate's exception table states WHY in prose — "FnCompleteAttempt's
// only read of the field is `if req.Status == "paused" || req.ForceTerminateStep`".
// That is an assertion written as a comment: nothing refuses a second read, and
// a second read is exactly what would make the exception false and the card's
// "the flag is load-bearing on the two terminal statuses alone" false with it.
//
// MUTANTS (applied to this tree and run; the verdict is what happened):
//
//	M37 enforcement: drop `|| req.ForceTerminateStep` from the condition
//	                                            RED  the_flag_unlocks_the_force
//	                                                 _terminate
//	M38 enforcement: change the condition to `req.Status == "wrapped" ||
//	    req.ForceTerminateStep`                 RED  paused_needs_no_flag, naming
//	                                                 wrapped as the status found
//	M39 enforcement: replace ErrConflictStepInProgress in the else branch with
//	    ErrBadRequest                           RED  otherwise_it_refuses_with
//	                                                 _its_own_code
//	M40 enforcement: add a second read — `if req.ForceTerminateStep { … }` above
//	    the block                               RED  the_flag_is_read_in_one
//	                                                 _place_only (2 reads, want 1)
//	M41 publication: delete the citation clause from the card's hop 4 bullet
//	                                            RED  K12 DEBT_GROWTH; this arm
//	                                                 reads the AST, so K12's
//	                                                 citation binding is its
//	                                                 publication side
//	G3  control:     rename the local `stepStatus` variable
//	                                          GREEN  the arm is anchored on the
//	                                                 literal "in_progress" and on
//	                                                 the field names the card
//	                                                 publishes, not on locals
func TestTheStepInProgressRefusalIsGatedOnPausedOrTheFlagAlone(t *testing.T) {
	_, file := parseStepGateFile(t, "run_attempts.go")
	fn := stepGateFuncDecl(t, file, "FnCompleteAttempt")

	// The status vocabulary, read out of the function rather than trusted: the
	// card's "the two terminal statuses" is 3 minus the 1 the disjunction names,
	// and that subtraction is only meaningful if the 3 is measured.
	seen := map[string]bool{}
	ast.Inspect(fn, func(n ast.Node) bool {
		if lit, _, ok := stepGateStatusLiteral(n); ok {
			seen[lit] = true
		}
		return true
	})

	// The H-R9-11 gate: the innermost `if` that reads the flag.
	var gate *ast.IfStmt
	gates := 0
	flagReads := 0
	ast.Inspect(fn, func(n ast.Node) bool {
		if stepGateFieldRead(n, "ForceTerminateStep") {
			flagReads++
		}
		ifStmt, ok := n.(*ast.IfStmt)
		if !ok {
			return true
		}
		found := false
		ast.Inspect(ifStmt.Cond, func(c ast.Node) bool {
			if stepGateFieldRead(c, "ForceTerminateStep") {
				found = true
			}
			return true
		})
		if found {
			gates++
			gate = ifStmt
		}
		return true
	})

	// FLOOR: without the gate there is nothing below to be right or wrong about,
	// and every arm would agree with an empty walk.
	if gate == nil {
		t.Fatalf("FnCompleteAttempt has no `if` reading ForceTerminateStep. The card describes the "+
			"H-R9-11 block as the one place the flag decides anything; with no such branch the "+
			"published parameter selects nothing, which is aihub#394's signature. Statuses this "+
			"walk saw compared against req.Status: %v", stepGateSortedKeys(seen))
	}
	if gates != 1 {
		t.Errorf("FnCompleteAttempt has %d branches reading ForceTerminateStep, want 1 — this arm "+
			"describes the last one it found, so the others are unchecked", gates)
	}

	t.Run("the_status_vocabulary_is_three_wide", func(t *testing.T) {
		if got := stepGateSortedKeys(seen); !stepGateEqualStrings(got, stepGateStatusVocabulary) {
			t.Errorf("FnCompleteAttempt compares req.Status against %v, want %v.\nThe card's claim "+
				"is that the flag is load-bearing on the TWO TERMINAL statuses alone, which is this "+
				"vocabulary minus the one the gate below force-terminates for. A fourth status "+
				"changes that arithmetic and the sentence with it.", got, stepGateStatusVocabulary)
		}
	})

	// The condition has to be a disjunction, and both of its operands are the
	// claim: one status, and the flag.
	cond, isOr := gate.Cond.(*ast.BinaryExpr)
	if !isOr || cond.Op != token.LOR {
		t.Fatalf("the force-terminate gate's condition is %T rather than an `||` — the card says "+
			"the block force-terminates when the status is paused OR the flag is set, and a "+
			"conjunction or a single test is a different rule", gate.Cond)
	}

	t.Run("the_flag_unlocks_the_force_terminate", func(t *testing.T) {
		if !stepGateFieldRead(cond.X, "ForceTerminateStep") && !stepGateFieldRead(cond.Y, "ForceTerminateStep") {
			t.Errorf("neither operand of the force-terminate gate reads ForceTerminateStep.\nThe " +
				"flag is published on this tool and inert on pf_pause_attempt precisely because " +
				"this is where it decides something; a gate that no longer reads it makes the " +
				"published parameter a switch that selects nothing on every status.")
		}
	})

	t.Run("paused_needs_no_flag", func(t *testing.T) {
		var statuses []string
		for _, operand := range []ast.Expr{cond.X, cond.Y} {
			if lit, op, ok := stepGateStatusLiteral(operand); ok && op == token.EQL {
				statuses = append(statuses, lit)
			}
		}
		if len(statuses) != 1 {
			t.Fatalf("the force-terminate gate names %d status literal(s) %v, want exactly 1.\n"+
				"The card says paused — and only paused — force-terminates without the flag, so "+
				"the count is the claim as much as the value", len(statuses), statuses)
		}
		if statuses[0] != "paused" {
			t.Errorf("the force-terminate gate unlocks on status=%q, not \"paused\".\nEvery "+
				"executor in the workspace ends a run through this call: if pausing needed the "+
				"flag, a pause with a live step would answer 409 and the agent would have no exit "+
				"that keeps its credentials", statuses[0])
		}
		// The other side of the same claim: the statuses NOT named here are the
		// ones where the flag is the only way through, and the card says there
		// are two of them.
		if remaining := len(seen) - 1; remaining != 2 {
			t.Errorf("%d status(es) are left needing the flag, and the card says two (wrapped and "+
				"failed). Statuses seen: %v", remaining, stepGateSortedKeys(seen))
		}
	})

	t.Run("the_gate_only_applies_to_a_live_step", func(t *testing.T) {
		// The card's sentence opens with "A step still `in_progress`", so the whole
		// rule is conditional on there BEING one. A gate that had floated out of
		// that check would force-terminate on every paused completion, including the
		// ones with no live step to terminate — and none of the arms above can see
		// the difference, since they read the inner condition alone.
		var host *ast.IfStmt
		ast.Inspect(fn, func(n ast.Node) bool {
			ifStmt, ok := n.(*ast.IfStmt)
			if !ok {
				return true
			}
			literal := false
			ast.Inspect(ifStmt.Cond, func(c ast.Node) bool {
				if lit, ok := c.(*ast.BasicLit); ok && lit.Kind == token.STRING {
					if v, err := strconv.Unquote(lit.Value); err == nil && v == "in_progress" {
						literal = true
					}
				}
				return true
			})
			if literal {
				host = ifStmt
			}
			return true
		})
		if host == nil {
			t.Fatalf("FnCompleteAttempt has no branch testing for a step status of \"in_progress\". " +
				"The card's rule is about a step that is still running; with nothing testing for one, " +
				"either the force-terminate or the refusal now happens unconditionally")
		}
		inside := false
		ast.Inspect(host.Body, func(n ast.Node) bool {
			if n == ast.Node(gate) {
				inside = true
			}
			return true
		})
		if !inside {
			t.Errorf("the force-terminate gate is not inside the \"in_progress\" branch. Outside it, " +
				"a paused completion force-terminates a step that is not running and a terminal one " +
				"refuses a work item with no live step at all")
		}
	})

	t.Run("otherwise_it_refuses_with_its_own_code", func(t *testing.T) {
		if gate.Else == nil {
			t.Fatalf("the force-terminate gate has no else branch — the card says a live step " +
				"refuses with ErrConflictStepInProgress when neither the status nor the flag " +
				"unlocks it, and a missing else silently completes the attempt over a running step")
		}
		found := false
		ast.Inspect(gate.Else, func(n ast.Node) bool {
			if id, ok := n.(*ast.Ident); ok && id.Name == "ErrConflictStepInProgress" {
				found = true
			}
			return true
		})
		if !found {
			t.Errorf("the force-terminate gate's else branch does not name ErrConflictStepInProgress.\n" +
				"internal/mcp classifies attempt errors by code, and a step-in-progress refusal " +
				"wearing another code either deletes the caller's state file or reads as a " +
				"retryable failure. The distinct code is what tells the caller to update the step " +
				"or set the flag.")
		}
	})

	t.Run("the_flag_is_read_in_one_place_only", func(t *testing.T) {
		if flagReads != 1 {
			t.Errorf("FnCompleteAttempt reads ForceTerminateStep %d time(s), want exactly 1.\nThe "+
				"universal contract gate's local-consumption table asserts this in prose — it "+
				"records that the field's ONLY read is the disjunction above, which is what makes "+
				"the parameter provably inert on pf_pause_attempt rather than inert by convention. "+
				"A second read makes that note false, and nothing else in the repo would notice.",
				flagReads)
		}
		// And nowhere else in the package: a read moved into a helper satisfies
		// the count above by leaving the function.
		elsewhere := stepGateFlagReadsOutside(t, "FnCompleteAttempt")
		if len(elsewhere) != 0 {
			t.Errorf("ForceTerminateStep is also read outside FnCompleteAttempt: %v.\nThe field is "+
				"one bool on a request struct two routes bind; a read anywhere else is a second "+
				"capability the caller was never told about", elsewhere)
		}
	})
}

// stepGateFlagReadsOutside returns "<file>:<func>" for every read of
// ForceTerminateStep in this package's non-test files outside the named
// function.
func stepGateFlagReadsOutside(t *testing.T, skip string) []string {
	t.Helper()
	var out []string
	for _, name := range stepGateSourceFiles(t) {
		_, file := parseStepGateFile(t, name)
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Name.Name == skip {
				continue
			}
			ast.Inspect(fn, func(n ast.Node) bool {
				if stepGateFieldRead(n, "ForceTerminateStep") {
					out = append(out, name+":"+fn.Name.Name)
				}
				return true
			})
		}
	}
	sort.Strings(out)
	return out
}

// stepGateSourceFiles lists this package's non-test .go files.
func stepGateSourceFiles(t *testing.T) []string {
	t.Helper()
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read the package directory: %v", err)
	}
	var out []string
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		out = append(out, name)
	}
	if len(out) == 0 {
		t.Fatal("no non-test .go files in this package — the census walk is broken")
	}
	sort.Strings(out)
	return out
}

// stepGatePauseReasonSite is one string literal in the tree that mentions the
// pause_reason column, with where it sits and what kind of statement it is.
type stepGatePauseReasonSite struct {
	where string // "<path>:<func>"
	kind  string // "read" | "write" | "other"
	text  string
}

// TestPauseReasonHasExactlyOneReader is the census behind the card's argument
// for refusing a reason on a terminal status.
//
// The refusal's justification is not "a reason on a wrap is untidy", it is
// "nothing would ever read it": the column has ONE reader, and that reader
// filters on the paused status. A second reader — a projection, a detail
// endpoint, an export — would make the value written on a terminal completion
// visible after all, and the card's disposition-2 argument (fix the code so the
// prose becomes true, aihub#452) would have to be revisited rather than merely
// re-read.
//
// Reads are told from writes by the statement the literal contains, so the
// UPDATE in FnCompleteAttempt is not counted as a reader. The Go-side spellings
// — the struct tags, the published property description, the refusal message —
// are classified `other` and named in the failure, because a census that
// silently dropped them could not say whether it had found the SQL at all.
//
// MUTANTS:
//
//	M42 enforcement: add a second `SELECT ra.pause_reason …` literal inside
//	    GetReadyQueue                           RED  exactly_one_reader (2, and it
//	                                                 names both sites)
//	M43 enforcement: drop `AND wi.status = 'paused'` from GetReadyQueue's paused
//	    query                                   RED  the_reader_filters_on_paused
//	M44 enforcement: make that SELECT read `NULL::text` instead of the column
//	                                            RED  the FLOOR (0 readers) — a
//	                                                 census that found none reports
//	                                                 that rather than agreeing with
//	                                                 "only one"
//	M45 publication: delete the citation clause from the card's reader sentence
//	                                            RED  K12 DEBT_GROWTH
func TestPauseReasonHasExactlyOneReader(t *testing.T) {
	const column = "pause_reason"
	var sites []stepGatePauseReasonSite

	root := filepath.Join("..", "..")
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", "node_modules", "vendor", "testdata":
				return fs.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(d.Name(), ".go") || strings.HasSuffix(d.Name(), "_test.go") {
			return nil
		}
		fset := token.NewFileSet()
		file, perr := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
		if perr != nil {
			return nil // a file this package cannot parse is not a reader of the column
		}
		rel, _ := filepath.Rel(root, path)
		rel = filepath.ToSlash(rel)
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok {
				continue
			}
			ast.Inspect(fn, func(n ast.Node) bool {
				lit, ok := n.(*ast.BasicLit)
				if !ok || lit.Kind != token.STRING || !strings.Contains(lit.Value, column) {
					return true
				}
				text, uerr := strconv.Unquote(lit.Value)
				if uerr != nil {
					text = lit.Value
				}
				sites = append(sites, stepGatePauseReasonSite{
					where: rel + ":" + fn.Name.Name,
					kind:  stepGateClassifySQL(text),
					text:  text,
				})
				return true
			})
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", root, err)
	}

	var readers, writers, others []stepGatePauseReasonSite
	for _, s := range sites {
		switch s.kind {
		case "read":
			readers = append(readers, s)
		case "write":
			writers = append(writers, s)
		default:
			others = append(others, s)
		}
	}

	// FLOOR, in both directions. No sites at all means the walk is broken; no
	// WRITE means the walk is not seeing SQL, and "exactly one reader" would
	// then be a statement about a census that had found nothing to classify.
	if len(sites) == 0 {
		t.Fatalf("no string literal in the tree mentions %q — this census is broken, and a "+
			"broken one agrees with every claim about how few readers there are", column)
	}
	if len(writers) == 0 {
		t.Fatalf("the census found %d literal(s) mentioning %q and not one write. FnCompleteAttempt "+
			"UPDATEs the column, so a walk that cannot see that statement cannot see a SELECT "+
			"either. Sites: %v", len(sites), column, stepGateDescribeSites(sites))
	}

	t.Run("exactly_one_reader", func(t *testing.T) {
		if len(readers) != 1 {
			t.Errorf("%d SELECT(s) read %q: %v.\nThe card argues the capability of recording a "+
				"reason on a wrapped or failed attempt was decided against BECAUSE the value would "+
				"have no reader — one reader, whose query filters on the paused status. A second "+
				"reader makes a terminal reason visible after all, which turns aihub#452's "+
				"disposition from \"the prose was right\" into an open question. Non-SQL "+
				"spellings, for contrast: %v",
				len(readers), column, stepGateDescribeSites(readers), stepGateDescribeSites(others))
			return
		}
		const want = "internal/domain/work_items.go:GetReadyQueue"
		if readers[0].where != want {
			t.Errorf("the only reader of %q is %s, and the card names %s", column, readers[0].where, want)
		}
	})

	t.Run("the_reader_filters_on_paused", func(t *testing.T) {
		if len(readers) != 1 {
			t.Skipf("the reader count is %d; the arm above reports that", len(readers))
		}
		if !strings.Contains(readers[0].text, "wi.status = 'paused'") {
			t.Errorf("the query at %s reads %q without filtering on the paused status:\n%s\n"+
				"That filter is the second half of the card's argument: it is what makes a reason "+
				"recorded on a wrapped attempt unreachable rather than merely unusual.",
				readers[0].where, column, readers[0].text)
		}
	})
}

// stepGateClassifySQL says whether a literal is a read of the column, a write, or one
// of the Go-side spellings (a struct tag, a message, a published description).
func stepGateClassifySQL(text string) string {
	upper := strings.ToUpper(text)
	switch {
	case strings.Contains(text, `json:"`):
		return "other"
	case strings.Contains(upper, "SELECT"):
		return "read"
	case strings.Contains(upper, "UPDATE "), strings.Contains(upper, "INSERT "):
		return "write"
	default:
		return "other"
	}
}

func stepGateDescribeSites(sites []stepGatePauseReasonSite) []string {
	out := make([]string, 0, len(sites))
	for _, s := range sites {
		out = append(out, s.where+"["+s.kind+"]")
	}
	sort.Strings(out)
	return out
}

func stepGateSortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func stepGateEqualStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// TestTheTerminalPathNilsThePauseReasonBeforeTheWrite is the residue clause of
// the card's pause_reason paragraph: an EMPTY reason on a terminal status is
// normalised to NULL rather than written as `”`.
//
// 🔴 It is the one clause of that paragraph nothing could hold. The aihub#452
// guard refuses a NON-EMPTY reason on a non-paused status and deliberately lets
// an empty one through, and internal/mcp never forwards an empty one at all —
// so the value this normalisation catches arrives only from a raw API caller
// that set the field to its zero value, and every arm in
// complete_attempt_pause_reason_guard_test.go runs with a nil pool by design,
// which is what lets them exercise the refusal without a database and also what
// stops them seeing anything that happens at the write.
//
// The distinction is the whole reason the column is nullable: NULL means "this
// attempt was not paused" and `”` means "paused, reason not given", and
// GetReadyQueue's paused segment surfaces the second. Dropping the two lines
// below turns every terminal completion that set the field into the second.
//
// MUTANTS:
//
//	M46 enforcement: delete the `if req.Status != "paused" { pauseReason = nil }`
//	    block                                   RED  the_reason_is_nilled
//	M47 enforcement: move that block BELOW the UPDATE that writes the column
//	                                            RED  before_the_write
//	M48 publication: delete the citation clauses from the card's pause_reason
//	    bullet                                  RED  K12 DEBT_GROWTH
func TestTheTerminalPathNilsThePauseReasonBeforeTheWrite(t *testing.T) {
	_, file := parseStepGateFile(t, "run_attempts.go")
	fn := stepGateFuncDecl(t, file, "FnCompleteAttempt")

	// The write: the Exec whose SQL names the column.
	var writePos token.Pos
	ast.Inspect(fn, func(n ast.Node) bool {
		lit, ok := n.(*ast.BasicLit)
		if !ok || lit.Kind != token.STRING || !strings.Contains(lit.Value, "pause_reason=") {
			return true
		}
		writePos = lit.Pos()
		return true
	})
	// FLOOR: no write means there is no ordering to get right, and the arm below
	// would agree with a function that had stopped writing the column at all.
	if writePos == token.NoPos {
		t.Fatalf("FnCompleteAttempt contains no statement writing pause_reason. The card says the " +
			"column is written on paused and on nothing else; a function that writes it nowhere " +
			"makes both halves of that vacuous, and the ready queue's paused segment would have " +
			"nothing to surface")
	}

	// The normalisation: an assignment of nil guarded by a non-paused status.
	var nilPos token.Pos
	ast.Inspect(fn, func(n ast.Node) bool {
		ifStmt, ok := n.(*ast.IfStmt)
		if !ok {
			return true
		}
		lit, op, ok := stepGateStatusLiteral(ifStmt.Cond)
		if !ok || op != token.NEQ || lit != "paused" {
			return true
		}
		ast.Inspect(ifStmt.Body, func(b ast.Node) bool {
			assign, ok := b.(*ast.AssignStmt)
			if !ok || len(assign.Rhs) != 1 {
				return true
			}
			if id, ok := assign.Rhs[0].(*ast.Ident); ok && id.Name == "nil" {
				nilPos = assign.Pos()
			}
			return true
		})
		return true
	})

	t.Run("the_reason_is_nilled", func(t *testing.T) {
		if nilPos == token.NoPos {
			t.Errorf("FnCompleteAttempt never assigns nil to the pause reason under a `!= \"paused\"` " +
				"guard.\nThe aihub#452 refusal covers a non-empty reason on a terminal status; an " +
				"EMPTY one is let through on purpose, so without this normalisation it lands as " +
				"`''` and the column stops distinguishing \"was not paused\" from \"paused, reason " +
				"not given\" — which is the only distinction it exists to carry.")
		}
	})

	t.Run("before_the_write", func(t *testing.T) {
		if nilPos == token.NoPos {
			t.Skipf("no normalisation found; the arm above reports that")
		}
		if nilPos > writePos {
			t.Errorf("the pause-reason normalisation sits AFTER the statement that writes the " +
				"column, so the value reaching the row is the caller's rather than the normalised " +
				"one. The card places it at the write for that reason.")
		}
	})
}
