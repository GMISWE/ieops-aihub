package server

// aihub#475 — the reinforce response must report the value the COLUMN holds.
//
// WHAT WENT WRONG
// ---------------
// handleReinforceMemory computed the new base_strength in Go, wrote it with a
// bare Exec, and then rendered both the 200 body and the memory_reinforced
// event payload from that Go value. memories.base_strength is SMALLINT, so pgx
// encodes the float64 through its int2 codec, which truncates toward zero and
// returns no error (measured in internal/domain,
// TestBaseStrengthIsTruncatedByThePgxInt2Codec). On a row stored at 3,
// `strength_delta: 0.5` stored 3 and answered 3.5; the next identical call read
// 3 again and answered 3.5 again. Any |strength_delta| below 1 was a permanent
// no-op that reported progress every single time, and nothing a caller could
// observe distinguished it from a working call.
//
// The fix is that the UPDATE carries `RETURNING base_strength, activation_count`
// and the response is built from what comes back.
//
// WHY THIS GATE IS STRUCTURAL
// ---------------------------
// The behavioural proof needs a database — a response can only be compared with
// a stored row if there IS a stored row — so it lives in
// routes_memory_reinforce_returning_db_test.go and SKIPs everywhere except the
// scoped CI step that has a Postgres. That is precisely the condition under
// which a fix quietly regresses: a later refactor that reverts to Exec plus the
// computed value would go green in every default `go test ./...` run. This file
// asserts on the shipped code instead, so the invariant is checked wherever the
// package is built.
//
// WHY IT PARSES RATHER THAN GREPS
// -------------------------------
// The property is not "the file contains the word RETURNING" — that is
// satisfied by a comment, by a different statement, or by a RETURNING whose
// result is scanned and then ignored. The property is a data-flow one: the
// identifier the response reports under "base_strength" must be an identifier
// SCANNED BACK from a statement whose own SQL says `RETURNING base_strength`.
// The negative controls at the bottom include the near-miss that a text scan
// cannot see — a RETURNING that is present and correct while the response
// reports the value read by the handler's OPENING SELECT, which is the pre-fix
// number under a new name.

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// reinforceHonestyReport is what one analysed function yields.
type reinforceHonestyReport struct {
	// scannedFromReturning names every identifier that receives a column of a
	// statement whose SQL contains RETURNING base_strength.
	scannedFromReturning map[string]bool
	// reportSites counts the `"base_strength": <expr>` entries found in map
	// literals — the 200 body and the event payload are two separate sites and
	// both have to be honest.
	reportSites int
	problems    []string
}

// analyseReinforceHonesty parses src, finds fnName, and reports whether every
// place that publishes a base_strength publishes one read back from the
// database.
func analyseReinforceHonesty(t *testing.T, filename, src, fnName string) reinforceHonestyReport {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, filename, src, parser.ParseComments)
	require.NoError(t, err, "parsing %s", filename)

	var fn *ast.FuncDecl
	ast.Inspect(f, func(n ast.Node) bool {
		if fd, ok := n.(*ast.FuncDecl); ok && fd.Name.Name == fnName {
			fn = fd
			return false
		}
		return true
	})
	require.NotNil(t, fn, "%s declares no func %s — if it was renamed, point this gate "+
		"at the new name rather than deleting it", filename, fnName)

	rep := reinforceHonestyReport{scannedFromReturning: map[string]bool{}}

	// Pass 1: bind every `x := <call>` in the function, so a receiver written as
	// `row := pool.QueryRow(...)` / `row.Scan(&v)` is followed like the fluent
	// form. Without this the gate would red on a legal refactor, which is its own
	// kind of wrong — a gate people learn to work around stops being a gate.
	bound := map[string]*ast.CallExpr{}
	ast.Inspect(fn, func(n ast.Node) bool {
		as, ok := n.(*ast.AssignStmt)
		if !ok {
			return true
		}
		for i, lhs := range as.Lhs {
			id, ok := lhs.(*ast.Ident)
			if !ok || i >= len(as.Rhs) {
				continue
			}
			if call, ok := as.Rhs[i].(*ast.CallExpr); ok {
				bound[id.Name] = call
			}
		}
		return true
	})

	// sqlOf collects every string literal argument of the call that produced the
	// receiver of a .Scan — i.e. the SQL of the statement being scanned.
	sqlOf := func(recv ast.Expr) string {
		var call *ast.CallExpr
		switch r := recv.(type) {
		case *ast.CallExpr:
			call = r
		case *ast.Ident:
			call = bound[r.Name]
		}
		if call == nil {
			return ""
		}
		var b strings.Builder
		for _, a := range call.Args {
			if lit, ok := a.(*ast.BasicLit); ok && lit.Kind == token.STRING {
				if s, err := strconv.Unquote(lit.Value); err == nil {
					b.WriteString(s)
				} else {
					b.WriteString(lit.Value)
				}
			}
		}
		return b.String()
	}

	// Pass 2: every .Scan whose statement RETURNs base_strength contributes its
	// destinations. `RETURNING` and `base_strength` are required together and in
	// that order so an ordinary SELECT of the column cannot qualify.
	ast.Inspect(fn, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != "Scan" {
			return true
		}
		sql := sqlOf(sel.X)
		up := strings.ToUpper(sql)
		i := strings.Index(up, "RETURNING")
		if i < 0 || !strings.Contains(up[i:], "BASE_STRENGTH") {
			return true
		}
		for _, arg := range call.Args {
			un, ok := arg.(*ast.UnaryExpr)
			if !ok || un.Op != token.AND {
				continue
			}
			if id, ok := un.X.(*ast.Ident); ok {
				rep.scannedFromReturning[id.Name] = true
			}
		}
		return true
	})

	// Pass 3: every `"base_strength": <expr>` published from a map literal must
	// name one of those identifiers, bare.
	ast.Inspect(fn, func(n ast.Node) bool {
		cl, ok := n.(*ast.CompositeLit)
		if !ok {
			return true
		}
		for _, elt := range cl.Elts {
			kv, ok := elt.(*ast.KeyValueExpr)
			if !ok {
				continue
			}
			key, ok := kv.Key.(*ast.BasicLit)
			if !ok || key.Kind != token.STRING {
				continue
			}
			name, err := strconv.Unquote(key.Value)
			if err != nil || name != "base_strength" {
				continue
			}
			rep.reportSites++
			id, ok := ast.Unparen(kv.Value).(*ast.Ident)
			if !ok {
				rep.problems = append(rep.problems,
					"a base_strength is published from an expression rather than from a bare "+
						"identifier, so this gate cannot tell where the value came from. Scan the "+
						"column into a variable and publish that variable")
				continue
			}
			if !rep.scannedFromReturning[id.Name] {
				rep.problems = append(rep.problems,
					"base_strength is published from "+id.Name+", which is not scanned from a "+
						"statement whose SQL says RETURNING base_strength")
			}
		}
		return true
	})

	return rep
}

// TestReinforceResponseReportsTheStoredBaseStrength is the gate on the shipped
// handler.
func TestReinforceResponseReportsTheStoredBaseStrength(t *testing.T) {
	const file = "routes_memory.go"
	src, err := os.ReadFile(file)
	require.NoError(t, err)

	rep := analyseReinforceHonesty(t, file, string(src), "handleReinforceMemory")

	// Two sites, not one: the 200 body AND the memory_reinforced event payload.
	// The event is what a later reader reconstructs the memory's history from, so
	// an honest response over a lying event would leave the false number in the
	// durable record — and it was the shape the bug actually shipped in.
	require.GreaterOrEqual(t, rep.reportSites, 2,
		"handleReinforceMemory publishes base_strength at %d site(s); the 200 body and the "+
			"memory_reinforced payload are both required to carry it. If a site was "+
			"deliberately removed, lower this floor in the same change and say why — an "+
			"unexplained drop is indistinguishable from the assertion quietly measuring "+
			"nothing", rep.reportSites)

	require.Empty(t, rep.problems,
		"handleReinforceMemory reports a base_strength it did not read back from the row.\n"+
			"%s\n"+
			"memories.base_strength is SMALLINT and pgx truncates a fractional value toward "+
			"zero with no error, so the Go arithmetic and the stored value routinely differ — "+
			"3 + 0.5 stores 3. Keep `RETURNING base_strength` on the UPDATE and publish what "+
			"it hands back. (Changing WHAT may be stored — reject, round, or widen the column "+
			"— is aihub#459, and is a separate decision from this one.)",
		strings.Join(rep.problems, "\n"))

	require.NotEmpty(t, rep.scannedFromReturning,
		"no RETURNING base_strength was found in handleReinforceMemory at all")
}

// TestReinforceHonestyAnalyserRejectsTheKnownBadShapes is the analyser's own
// control. A matcher that has stopped matching reports a clean file, so each arm
// below carries a defect the gate above exists to catch and requires it to be
// seen. The last arm is a positive control: it must come back clean, or the gate
// is merely rejecting everything.
func TestReinforceHonestyAnalyserRejectsTheKnownBadShapes(t *testing.T) {
	const header = "package p\nfunc h() {\n"
	const footer = "\n}\n"

	// 1. The pre-fix shape, verbatim in miniature: Exec, then report the Go value.
	preFix := header + `
	var memBaseStrength float64
	pool.QueryRow(ctx, "SELECT base_strength FROM memories WHERE id=$1", memID).Scan(&memBaseStrength)
	newBaseStrength := memBaseStrength + 0.5
	pool.Exec(ctx, "UPDATE memories SET base_strength = $1 WHERE id = $2", newBaseStrength, memID)
	_ = map[string]any{"base_strength": newBaseStrength}
	_ = map[string]any{"base_strength": newBaseStrength}
	` + footer
	rep := analyseReinforceHonesty(t, "prefix.go", preFix, "h")
	require.Len(t, rep.problems, 2,
		"the pre-fix shape must be flagged at BOTH publication sites; got %v", rep.problems)

	// 2. The near-miss a text scan cannot see: a correct RETURNING is present and
	// its result is even scanned — but the response reports the value the OPENING
	// SELECT read, which is the old number wearing a different name.
	returningIgnored := header + `
	var memBaseStrength float64
	pool.QueryRow(ctx, "SELECT base_strength FROM memories WHERE id=$1", memID).Scan(&memBaseStrength)
	var stored float64
	pool.QueryRow(ctx, "UPDATE memories SET base_strength=$1 WHERE id=$2 RETURNING base_strength", v, memID).Scan(&stored)
	_ = map[string]any{"base_strength": memBaseStrength}
	` + footer
	rep = analyseReinforceHonesty(t, "ignored.go", returningIgnored, "h")
	require.Len(t, rep.problems, 1,
		"a RETURNING whose result is scanned and then not published is still the bug; got %v",
		rep.problems)
	require.Contains(t, rep.problems[0], "memBaseStrength")

	// 3. SELECTing the column is not RETURNING it. Without the ordering check a
	// plain `SELECT ... base_strength` would credit its destination and arm 2
	// would pass for the wrong reason.
	selectOnly := header + `
	var memBaseStrength float64
	pool.QueryRow(ctx, "SELECT project, base_strength FROM memories WHERE id=$1", memID).Scan(&memProject, &memBaseStrength)
	_ = map[string]any{"base_strength": memBaseStrength}
	` + footer
	rep = analyseReinforceHonesty(t, "selectonly.go", selectOnly, "h")
	require.Len(t, rep.problems, 1, "a SELECT of the column must not count as a read-back")
	require.Empty(t, rep.scannedFromReturning)

	// 4. Positive control, in the split-receiver form the gate has to follow as
	// well as the fluent one.
	fixed := header + `
	var stored float64
	row := pool.QueryRow(ctx, "UPDATE memories SET base_strength=$1 WHERE id=$2 RETURNING base_strength, activation_count", v, memID)
	row.Scan(&stored, &storedCount)
	_ = map[string]any{"base_strength": stored}
	_ = map[string]any{"base_strength": (stored)}
	` + footer
	rep = analyseReinforceHonesty(t, "fixed.go", fixed, "h")
	require.Empty(t, rep.problems,
		"the fixed shape must come back clean, including through a named receiver and "+
			"parentheses; got %v", rep.problems)
	require.Equal(t, 2, rep.reportSites)
}
