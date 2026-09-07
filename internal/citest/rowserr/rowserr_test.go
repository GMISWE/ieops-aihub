package rowserr

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// repoRoot walks up from the test's working directory to the module root,
// identified by go.mod. Same shape as workflowsDir in internal/citest/wfroutes:
// a marker file rather than a fixed number of "..", so moving this package does
// not silently point the gate at a subtree.
func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 8; i++ {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	t.Fatal("could not find go.mod above the test's working directory; this gate cannot have run")
	return ""
}

const allowlistFile = "allowlist.txt"

// TestEveryRowsLoopChecksRowsErr is the aihub#386 gate.
//
// It is a MECHANISM gate, not the inventory of the 29 loops aihub#386 fixed. It
// names no file and no function: it asks "does a `for X.Next()` loop over query
// rows exist anywhere without a following X.Err()", which is a question a NEW
// loop added next month cannot answer differently. The inventory is in the
// aihub#386 commit; a list here would have to be edited on every legitimate
// change and would go stale in the direction of passing.
func TestEveryRowsLoopChecksRowsErr(t *testing.T) {
	root := repoRoot(t)
	loops, err := ScanDir(root)
	if err != nil {
		t.Fatalf("scanning %s: %v", root, err)
	}
	allow, err := ParseAllowlist(allowlistFile)
	if err != nil {
		t.Fatalf("reading %s: %v", allowlistFile, err)
	}

	var violations []Loop
	usedAllowEntries := map[string]bool{}
	for _, l := range Unchecked(loops) {
		if allow[l.Key()] {
			usedAllowEntries[l.Key()] = true
			continue
		}
		violations = append(violations, l)
	}

	for _, v := range violations {
		t.Errorf("%s loops over query rows and never checks %s.Err().\n"+
			"    pgx v5 defers EXECUTE-time errors to Err(), so this loop reports an execution "+
			"failure as an empty result — indistinguishable from \"nothing matched\" (aihub#382, aihub#386).\n"+
			"    Fix: after the loop, `if err := %s.Err(); err != nil { return ... }`, propagating the way "+
			"this function reports errors.\n"+
			"    If this site is genuinely best-effort, add this exact line to internal/citest/rowserr/%s "+
			"with a comment saying why:\n        %s",
			v, v.Rows, v.Rows, allowlistFile, v.Key())
	}

	// An allowlist entry that matches nothing is deleted, not left lying
	// around: it is an exemption for a loop that no longer exists, and the next
	// loop to land in that function would inherit it silently.
	for key := range allow {
		if !usedAllowEntries[key] {
			t.Errorf("allowlist entry %q in %s matches no unchecked loop — delete it.\n"+
				"    A stale exemption is worse than none: it exempts whatever lands in that "+
				"function next, without anybody deciding to.", key, allowlistFile)
		}
	}
}

// TestScannerStillSeesEveryKnownRowsLoop is why a clean report from the gate
// above means anything.
//
// The gate passes when it finds zero UNCHECKED loops — and it also passes when
// it finds no loops at all. Those are opposite facts with the same exit code,
// and narrowing the recogniser (a rename of Query, a new helper that hands rows
// around, a stricter typeIsRows) produces the second one silently. So the floor
// is asserted separately: the scanner must still SEE the population it claims
// to police.
//
// The number is a floor, deliberately well under the 46 loops present when
// aihub#386 landed, because loops are legitimately added and deleted and a gate
// pinned to an exact count is a gate somebody has to edit to ship anything. It
// is not an inventory; it is a liveness check on the recogniser.
func TestScannerStillSeesEveryKnownRowsLoop(t *testing.T) {
	const floor = 30

	root := repoRoot(t)
	loops, err := ScanDir(root)
	if err != nil {
		t.Fatalf("scanning %s: %v", root, err)
	}
	if len(loops) < floor {
		t.Fatalf("the scanner found only %d query-rows loops in the repo, expected at least %d.\n"+
			"    This gate reports violations, so seeing NOTHING and seeing nothing WRONG have the "+
			"same exit code. A drop this large means the recogniser stopped recognising rows "+
			"(rowsValuesIn / typeIsRows), not that the repo stopped querying.", len(loops), floor)
	}

	// And the checked half has to work too: if Checked were hardwired false the
	// gate would be noise, and if it were hardwired true the gate would be
	// blind. Both halves must be non-empty in a repo this size.
	checked := len(loops) - len(Unchecked(loops))
	if checked == 0 {
		t.Errorf("no loop in the repo was classified as checked; the Err()-matching half of the "+
			"scanner is not working (found %d loops, all unchecked)", len(loops))
	}
}

// ─── The scanner's own behaviour, on fixtures ────────────────────────────────
//
// Every arm below is a source shape whose verdict is known by construction, so
// the scanner is measured rather than trusted. Both directions are covered on
// purpose: an arm proving it FLAGS a bad loop, and an arm proving it ACCEPTS a
// good one. A scanner that flags everything and a scanner that flags nothing
// each pass half of this set.

func scanOne(t *testing.T, src string) []Loop {
	t.Helper()
	loops, err := ScanSource([]byte(src), "fixture.go")
	if err != nil {
		t.Fatalf("scanning fixture: %v", err)
	}
	return loops
}

func TestScannerFlagsAnUncheckedLoop(t *testing.T) {
	loops := scanOne(t, `package p
func f(pool P) error {
	rows, err := pool.Query(ctx, "SELECT 1")
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		_ = rows.Scan()
	}
	return nil
}
`)
	if len(loops) != 1 {
		t.Fatalf("found %d loops, want 1: %v", len(loops), loops)
	}
	if loops[0].Checked {
		t.Errorf("the loop has no rows.Err() after it and was reported as checked — "+
			"this scanner cannot detect the defect it exists for: %v", loops[0])
	}
	if loops[0].Rows != "rows" || loops[0].Func != "f" {
		t.Errorf("loop reported as %+v, want Rows=rows Func=f", loops[0])
	}
}

func TestScannerAcceptsACheckedLoop(t *testing.T) {
	loops := scanOne(t, `package p
func f(pool P) error {
	rows, err := pool.Query(ctx, "SELECT 1")
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		_ = rows.Scan()
	}
	if err := rows.Err(); err != nil {
		return err
	}
	return nil
}
`)
	if len(loops) != 1 {
		t.Fatalf("found %d loops, want 1: %v", len(loops), loops)
	}
	if !loops[0].Checked {
		t.Errorf("a loop WITH a following rows.Err() was reported unchecked — the gate would "+
			"fire on correct code, and a gate that cries wolf gets deleted: %v", loops[0])
	}
}

// TestScannerIgnoresANonRowsIterator is the arm that keeps the recogniser
// scoped. internal/render/svg_block.go loops on an xhtml.Tokenizer's z.Next(),
// which has nothing to do with pgx and no Err() to call. A scanner keyed on
// "any Next() method" reports it, and every future iterator, as a violation.
func TestScannerIgnoresANonRowsIterator(t *testing.T) {
	loops := scanOne(t, `package p
func f(src []byte) {
	z := xhtml.NewTokenizer(bytes.NewReader(src))
	for {
		tt := z.Next()
		if tt == xhtml.ErrorToken {
			return
		}
	}
}
func g(it Iter) {
	for it.Next() {
		_ = it.Value()
	}
}
`)
	if len(loops) != 0 {
		t.Errorf("reported %d loops over non-rows iterators: %v.\n"+
			"    Neither an html tokenizer nor a generic iterator defers a database error to "+
			"Err(); flagging them makes the gate noise.", len(loops), loops)
	}
}

// TestScannerReportsTheFirstOfTwoLoopsSharingOneErrCheck covers the blind spot
// the aihub#386 survey named in its own method note: with two loops over the
// same variable and a single trailing Err(), a position-blind matcher calls
// BOTH checked, and the first loop's execute-time failure stays silent.
func TestScannerReportsTheFirstOfTwoLoopsSharingOneErrCheck(t *testing.T) {
	loops := scanOne(t, `package p
func f(pool P) error {
	rows, _ := pool.Query(ctx, "SELECT 1")
	for rows.Next() {
		_ = rows.Scan()
	}
	rows, _ = pool.Query(ctx, "SELECT 2")
	for rows.Next() {
		_ = rows.Scan()
	}
	if err := rows.Err(); err != nil {
		return err
	}
	return nil
}
`)
	if len(loops) != 2 {
		t.Fatalf("found %d loops, want 2: %v", len(loops), loops)
	}
	sort.Slice(loops, func(i, j int) bool { return loops[i].Line < loops[j].Line })
	if loops[0].Checked {
		t.Errorf("the FIRST of two loops sharing one trailing Err() was reported checked; "+
			"its execute-time error is unreachable and the gate would miss it: %v", loops[0])
	}
	if !loops[1].Checked {
		t.Errorf("the SECOND loop is followed by rows.Err() and must be checked: %v", loops[1])
	}
}

// TestScannerSeesRowsArrivingAsAParameter covers recogniser rule 2. A helper
// that is HANDED rows has no Query call to key on, so rule 1 alone would make
// every such helper invisible — and invisible reads as compliant.
func TestScannerSeesRowsArrivingAsAParameter(t *testing.T) {
	loops := scanOne(t, `package p
func scanAll(rows pgx.Rows) error {
	for rows.Next() {
		_ = rows.Scan()
	}
	return nil
}
func scanAllPtr(rows *sql.Rows) error {
	for rows.Next() {
		_ = rows.Scan()
	}
	return rows.Err()
}
`)
	if len(loops) != 2 {
		t.Fatalf("found %d loops, want 2 (pgx.Rows and *sql.Rows parameters): %v", len(loops), loops)
	}
	byFunc := map[string]Loop{}
	for _, l := range loops {
		byFunc[l.Func] = l
	}
	if l, ok := byFunc["scanAll"]; !ok || l.Checked {
		t.Errorf("a loop over a pgx.Rows PARAMETER with no Err() must be flagged, got %+v", l)
	}
	if l, ok := byFunc["scanAllPtr"]; !ok || !l.Checked {
		t.Errorf("a loop over a *sql.Rows parameter ending in `return rows.Err()` is checked, got %+v", l)
	}
}

// TestScannerDistinguishesTwoRowsVariablesInOneFunction: GetReadyQueue reads
// seven result sets under seven names in one function, so per-variable
// bookkeeping is the normal case here, not an edge case. Checking one must not
// vouch for another.
func TestScannerDistinguishesTwoRowsVariablesInOneFunction(t *testing.T) {
	loops := scanOne(t, `package p
func f(pool P) error {
	aRows, _ := pool.Query(ctx, "SELECT 1")
	for aRows.Next() {
		_ = aRows.Scan()
	}
	if err := aRows.Err(); err != nil {
		return err
	}
	bRows, _ := pool.Query(ctx, "SELECT 2")
	for bRows.Next() {
		_ = bRows.Scan()
	}
	return nil
}
`)
	if len(loops) != 2 {
		t.Fatalf("found %d loops, want 2: %v", len(loops), loops)
	}
	byRows := map[string]Loop{}
	for _, l := range loops {
		byRows[l.Rows] = l
	}
	if !byRows["aRows"].Checked {
		t.Errorf("aRows is followed by aRows.Err() and must be checked: %+v", byRows["aRows"])
	}
	if byRows["bRows"].Checked {
		t.Errorf("bRows has no bRows.Err(); aRows' check must not vouch for it: %+v", byRows["bRows"])
	}
}

// TestAllowlistKeyOmitsTheLineNumber pins the property the Key doc argues for.
// An exemption keyed on a line number stops matching the moment anything above
// it moves, which turns a reviewed decision into a silent expiry.
func TestAllowlistKeyOmitsTheLineNumber(t *testing.T) {
	a := Loop{File: "a/b.go", Line: 10, Func: "f", Rows: "rows"}
	b := Loop{File: "a/b.go", Line: 999, Func: "f", Rows: "rows"}
	if a.Key() != b.Key() {
		t.Errorf("Key() differs for the same loop at two line numbers (%q vs %q); an allowlist "+
			"entry would expire whenever an edit above it shifted the line", a.Key(), b.Key())
	}
	if strings.Contains(a.Key(), "10") {
		t.Errorf("Key() %q contains the line number", a.Key())
	}
}

// TestAllowlistParsingIgnoresCommentsAndBlanks: the file is meant to carry a
// justification beside every entry, so comments have to survive parsing.
func TestAllowlistParsingIgnoresCommentsAndBlanks(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "allowlist.txt")
	body := "# a comment\n\n  internal/x/y.go:f:rows  \n# another\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	allow, err := ParseAllowlist(path)
	if err != nil {
		t.Fatalf("ParseAllowlist: %v", err)
	}
	if len(allow) != 1 || !allow["internal/x/y.go:f:rows"] {
		t.Errorf("parsed %v, want exactly the one trimmed entry", allow)
	}
}

// TestMissingAllowlistIsEmptyNotAnError: "no exemptions" is the steady state,
// and it must not require a file to exist.
func TestMissingAllowlistIsEmptyNotAnError(t *testing.T) {
	allow, err := ParseAllowlist(filepath.Join(t.TempDir(), "does-not-exist.txt"))
	if err != nil {
		t.Fatalf("a missing allowlist must parse as empty, got error: %v", err)
	}
	if len(allow) != 0 {
		t.Errorf("expected an empty allowlist, got %v", allow)
	}
}

// ─── aihub#409: rows held in a struct field ──────────────────────────────────
//
// The package doc's rule 2 used to claim "struct field of the receiver" while
// rowsValuesIn looked only at the signature and at local `var` declarations. So
// `for s.rows.Next()` matched nothing, and a scanner that reports violations
// reads silence as compliance — the exact failure this package's own floor arm
// exists to prevent, reached through the recogniser rather than through the
// walk.
//
// 🔴 These fixtures are the ONLY evidence for this recogniser, and that is a
// measured fact rather than an assumption: the repo contains 46 rows loops and
// ZERO of them are selector-shaped (measured 2026-09-07, both before and after
// the change — identical counts). So TestEveryRowsLoopChecksRowsErr and the
// floor arm cannot distinguish this change from a no-op, and if these two arms
// are ever deleted the recogniser goes back to being unverified while every
// other test in this package stays green.

// TestScannerSeesRowsHeldInAStructField is the positive arm: the detector MUST
// fire. Red before the change (the loop was invisible, so the fixture reported
// zero loops rather than one unchecked loop).
func TestScannerSeesRowsHeldInAStructField(t *testing.T) {
	const src = `package p

type reader struct {
	rows pgx.Rows
	name string
}

func (r *reader) drain() error {
	for r.rows.Next() {
		_ = r.name
	}
	return nil
}
`
	loops := scanOne(t, src)
	if len(loops) != 1 {
		t.Fatalf("expected exactly 1 loop over a rows STRUCT FIELD, found %d: %v\n"+
			"    A loop the scanner cannot see is reported as compliant, which is the one "+
			"outcome indistinguishable from a clean tree.", len(loops), loops)
	}
	if loops[0].Rows != "r.rows" {
		t.Errorf("the report must name the expression a reader will find in the source; "+
			"got %q, want %q", loops[0].Rows, "r.rows")
	}
	if loops[0].Checked {
		t.Errorf("no r.rows.Err() appears after the loop, so it must be reported unchecked")
	}
}

// TestScannerAcceptsACheckedStructFieldLoop is the other half. Without it the
// arm above is satisfied by a recogniser that hardwires Checked=false, which
// would flag every struct-field loop in the repo forever and get the gate
// deleted rather than obeyed.
func TestScannerAcceptsACheckedStructFieldLoop(t *testing.T) {
	const src = `package p

type reader struct {
	rows pgx.Rows
}

func (r *reader) drain() error {
	for r.rows.Next() {
	}
	return r.rows.Err()
}
`
	loops := scanOne(t, src)
	if len(loops) != 1 {
		t.Fatalf("expected exactly 1 loop, found %d: %v", len(loops), loops)
	}
	if !loops[0].Checked {
		t.Errorf("r.rows.Err() follows the loop, so it must be classified checked; " +
			"got unchecked, which makes every correct struct-field loop a false positive")
	}
}

// TestScannerCannotSeeAStructFieldDeclaredElsewhere pins the LIMIT the doc
// comment now states, rather than leaving it as prose nothing checks.
//
// The scanner does not type-check, so it recognises a field by name taken from
// struct types in the file it is reading. A rows field whose struct lives in
// another file of the same package is invisible. That is a real hole, and the
// reason it is asserted instead of quietly tolerated is that the previous
// version of this package documented a coverage it did not have; a limit
// nothing pins is free to be overstated again the next time this comment is
// edited. If someone later resolves fields across the package, this test is
// what tells them the doc paragraph above must change with it.
func TestScannerCannotSeeAStructFieldDeclaredElsewhere(t *testing.T) {
	const src = `package p

func (r *reader) drain() error {
	for r.rows.Next() {
	}
	return nil
}
`
	if loops := scanOne(t, src); len(loops) != 0 {
		t.Fatalf("the scanner reported %v for a field whose struct is not in this file. "+
			"That is more coverage than it has — if this now works, the 'in the same file' "+
			"limit in the package doc is wrong and must be rewritten.", loops)
	}
}

// TestScannerIgnoresAStructFieldThatIsNotRows is the negative control for the
// widened recogniser: an exempt target must SURVIVE it.
//
// The risk the widening introduces is a field-name collision — recognising by
// name means any `X.rows.Next()` matches once some struct in the file declares
// a rows-typed `rows`. This arm holds the other direction: a struct field whose
// type is not a Rows type is not recognised just because it has a Next() method,
// which is the html.Tokenizer shape TestScannerIgnoresANonRowsIterator covers
// for local variables.
//
// The condition is a bare `for s.tok.Next()` on purpose. Written as
// `for s.tok.Next() != html.ErrorToken` — the real html.Tokenizer idiom — the
// condition is a BinaryExpr, which zeroArgMethodCall declines whatever the
// field's type is, and the arm would pass without the type rule being consulted
// at all: a negative control satisfied by the wrong mechanism.
func TestScannerIgnoresAStructFieldThatIsNotRows(t *testing.T) {
	const src = `package p

type scanner struct {
	tok *html.Tokenizer
	buf []byte
}

func (s *scanner) run() {
	for s.tok.Next() {
		_ = s.buf
	}
}
`
	if loops := scanOne(t, src); len(loops) != 0 {
		t.Fatalf("the scanner reported %v for an html.Tokenizer held in a struct field. "+
			"A gate that flags every iterator in the repo is a gate somebody deletes.", loops)
	}
}
