package domain

// aihub#260, the half that runs everywhere: the SQL buildProjectUpdate compiles.
//
// These are deliberately NOT DB-gated. The two ways this exact pattern has
// failed before are both properties of the compiled statement, not of any
// observable outcome against a database:
//
//   - "the counter never advanced" (aihub#241's first attempt) is the SET clause
//     saying `members_version = <something the caller sent> + 1` instead of
//     `members_version = members_version + 1`.
//
//   - "there was no compare-and-set at all" (aihub#241's second attempt) is a
//     WHERE clause with no members_version predicate while the parameter is
//     accepted and looks wired.
//
// The first of those is, today, not detectable from behaviour at all:
// UpdateProject holds SELECT ... FOR UPDATE across the write, so two racing
// writers are serialised and a version computed in Go would still come out
// 0 -> 1 -> 2 in any sequential test. What makes the in-database form
// load-bearing is that UpdateProject's own pre-transaction read
// (checkProjectAccess) happens BEFORE the lock — so a Go-computed next value
// derived from it is stale by construction. A statement-shape assertion is the
// only thing that fails the moment someone writes it that way, which is why
// these live here rather than in the DB-gated file next door.

import (
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"go/printer"
	"go/token"
	"strconv"
	"strings"
	"testing"
)

// casTestMembers is a legal members list.
var casTestMembers = []MemberInput{
	{UserID: "u_alice", Role: "writer"},
	{UserID: "u_bob", Role: "viewer"},
}

func mustBuildProjectUpdate(t *testing.T, req *UpdateProjectRequest) projectUpdate {
	t.Helper()
	upd, aerr := buildProjectUpdate(req, "p_probe")
	if aerr != nil {
		t.Fatalf("buildProjectUpdate returned an error: %v", aerr)
	}
	return upd
}

// setClauseOf returns the text between "SET " and " WHERE ", i.e. only what the
// statement writes. Assertions about what must NOT be stored have to be scoped
// to this, or the WHERE predicate's own `members_version=$n` would satisfy them
// and turn a negative assertion into a false pass.
func setClauseOf(t *testing.T, query string) string {
	t.Helper()
	start := strings.Index(query, " SET ")
	end := strings.Index(query, " WHERE ")
	if start < 0 || end < 0 || end < start {
		t.Fatalf("could not split SET/WHERE out of %q", query)
	}
	return query[start+len(" SET ") : end]
}

// whereClauseOf returns the text between " WHERE " and " RETURNING ".
func whereClauseOf(t *testing.T, query string) string {
	t.Helper()
	start := strings.Index(query, " WHERE ")
	end := strings.Index(query, " RETURNING ")
	if start < 0 || end < 0 || end < start {
		t.Fatalf("could not split WHERE/RETURNING out of %q", query)
	}
	return query[start+len(" WHERE ") : end]
}

// ─── failure mode 1: the counter must advance in the database ────────────────

// A members write must increment members_version with Postgres reading the
// stored value. This is the assertion that fails if anyone recomputes it in Go.
func TestBuildProjectUpdate_MembersWriteIncrementsVersionInSQL(t *testing.T) {
	upd := mustBuildProjectUpdate(t, &UpdateProjectRequest{Members: &casTestMembers})

	set := setClauseOf(t, upd.Query)
	if !strings.Contains(set, "members_version = members_version + 1") {
		t.Errorf("the SET clause does not increment members_version in the database.\n"+
			"got SET: %s\n"+
			"A version computed in Go would be derived from UpdateProject's pre-transaction read, "+
			"which happens before the row lock, so two racing writers would compute the same next "+
			"value and the counter would stop being a usable CAS token (aihub#241 failure mode 1).", set)
	}
	// The increment must carry no placeholder: a `members_version=$n` in the SET
	// clause is precisely the Go-computed form this exists to forbid.
	if strings.Contains(set, "members_version=$") || strings.Contains(set, "members_version = $") {
		t.Errorf("the SET clause binds members_version to a parameter — it is being computed outside "+
			"the database.\ngot SET: %s", set)
	}
}

// The increment is keyed on `members` being written, NOT on "any update". This
// is the property that makes a dedicated counter better than a CAS on
// updated_at: an unrelated edit must not invalidate a members guard somebody is
// holding.
func TestBuildProjectUpdate_UnrelatedWriteDoesNotTouchMembersVersion(t *testing.T) {
	desc := "a new description, nothing to do with membership"
	upd := mustBuildProjectUpdate(t, &UpdateProjectRequest{Description: &desc})

	set := setClauseOf(t, upd.Query)
	if strings.Contains(set, "members_version") {
		t.Errorf("a description-only update advances members_version.\ngot SET: %s\n"+
			"Then any edit would 409 a members compare-and-set that is still perfectly valid, "+
			"and this counter would be no better than updated_at.", set)
	}
}

// ─── failure mode 2: the version must become a WHERE predicate ──────────────

func TestBuildProjectUpdate_SuppliedVersionAddsAWherePredicate(t *testing.T) {
	v := 7
	upd := mustBuildProjectUpdate(t, &UpdateProjectRequest{Members: &casTestMembers, MembersVersion: &v})

	if !upd.CAS {
		t.Error("CAS is false although members_version was supplied — UpdateProject keys its 409 off this flag, " +
			"so a conflict would surface as a 500 instead")
	}
	where := whereClauseOf(t, upd.Query)
	if !strings.Contains(where, "members_version=$") {
		t.Errorf("the WHERE clause carries no members_version predicate.\ngot WHERE: %s\n"+
			"Accepting the parameter without making it a precondition is aihub#241 failure mode 2: "+
			"a stale writer still wins, silently.", where)
	}
	if got := upd.Args[len(upd.Args)-1]; got != 7 {
		t.Errorf("the supplied version is not the last bound argument: got %#v, want 7", got)
	}
}

// The version is a precondition and nothing else — it must never be STORED.
// aihub#241's second attempt did exactly that: passing the version changed what
// got written and guarded nothing.
func TestBuildProjectUpdate_SuppliedVersionIsNeverStored(t *testing.T) {
	v := 7
	upd := mustBuildProjectUpdate(t, &UpdateProjectRequest{Members: &casTestMembers, MembersVersion: &v})

	set := setClauseOf(t, upd.Query)
	if strings.Contains(set, "members_version=$") || strings.Contains(set, "members_version = $") {
		t.Errorf("the caller's members_version is being written into the row.\ngot SET: %s", set)
	}
	if !strings.Contains(set, "members_version = members_version + 1") {
		t.Errorf("a CAS write must still advance the counter.\ngot SET: %s", set)
	}
}

// Omitting the version must keep the historical unconditional overwrite, which
// every caller that exists today depends on.
func TestBuildProjectUpdate_OmittedVersionAddsNoPredicate(t *testing.T) {
	upd := mustBuildProjectUpdate(t, &UpdateProjectRequest{Members: &casTestMembers})

	if upd.CAS {
		t.Error("CAS is true although no members_version was supplied")
	}
	where := whereClauseOf(t, upd.Query)
	if strings.Contains(where, "members_version") {
		t.Errorf("an update with no members_version still carries a precondition.\ngot WHERE: %s\n"+
			"That would break every existing caller, none of which sends one.", where)
	}
	if where != "name=$2" {
		t.Errorf("WHERE = %q, want exactly %q", where, "name=$2")
	}
}

// ─── statement shape ────────────────────────────────────────────────────────

// The new predicate takes the placeholder after `name`, so an off-by-one here
// would bind the version where the name belongs. Assert the whole statement for
// a request that exercises every field.
func TestBuildProjectUpdate_PlaceholdersAndArgsLineUp(t *testing.T) {
	desc := "d"
	visible := false
	scenario := "git@github.com:GMISWE/polyforge-coding.git"
	v := 3
	upd := mustBuildProjectUpdate(t, &UpdateProjectRequest{
		Description:    &desc,
		Visible:        &visible,
		Scenario:       &scenario,
		Repos:          json.RawMessage(`[{"name":"r","url":"u"}]`),
		Members:        &casTestMembers,
		MembersVersion: &v,
	})

	wantSet := "description=$1, visible=$2, scenario=$3, repos=$4, members=$5, members_version = members_version + 1"
	if got := setClauseOf(t, upd.Query); got != wantSet {
		t.Errorf("SET  = %q\nwant = %q", got, wantSet)
	}
	wantWhere := "name=$6 AND members_version=$7"
	if got := whereClauseOf(t, upd.Query); got != wantWhere {
		t.Errorf("WHERE = %q\nwant  = %q", got, wantWhere)
	}
	if len(upd.Args) != 7 {
		t.Fatalf("len(Args) = %d, want 7 (5 written columns + name + version); Args=%#v", len(upd.Args), upd.Args)
	}
	if upd.Args[5] != "p_probe" {
		t.Errorf("Args[5] = %#v, want the project name", upd.Args[5])
	}
	if upd.Args[6] != 3 {
		t.Errorf("Args[6] = %#v, want the supplied version 3", upd.Args[6])
	}
}

// The statement must return the updated row including the new counter, so a
// caller that just wrote members holds the next token without a second read.
func TestBuildProjectUpdate_ReturnsTheNewVersion(t *testing.T) {
	upd := mustBuildProjectUpdate(t, &UpdateProjectRequest{Members: &casTestMembers})
	if !strings.Contains(upd.Query, "members_version") ||
		!strings.Contains(upd.Query[strings.Index(upd.Query, " RETURNING "):], "members_version") {
		t.Errorf("the RETURNING list does not include members_version — the caller would have to re-read "+
			"to find the token for its next write.\ngot: %s", upd.Query)
	}
}

func TestBuildProjectUpdate_NoFieldsProducesNoStatement(t *testing.T) {
	upd := mustBuildProjectUpdate(t, &UpdateProjectRequest{})
	if !upd.Empty {
		t.Errorf("Empty = false for a request with no fields; Query=%q", upd.Query)
	}
	if upd.Query != "" {
		t.Errorf("Query = %q, want empty", upd.Query)
	}
}

// projectUpdateWritesSomething gates the "guard with nothing to guard" 400 and
// is a second spelling of buildProjectUpdate's Empty. Drift between them is
// silent in both directions, so pin them together over every field.
func TestProjectUpdateWritesSomethingAgreesWithBuild(t *testing.T) {
	desc := "d"
	visible := true
	scenario := ""
	cases := map[string]*UpdateProjectRequest{
		"nothing":      {},
		"description":  {Description: &desc},
		"visible":      {Visible: &visible},
		"scenario":     {Scenario: &scenario},
		"repos":        {Repos: json.RawMessage(`[{"name":"r","url":"u"}]`)},
		"repos null":   {Repos: json.RawMessage(`null`)},
		"repos empty":  {Repos: json.RawMessage(``)},
		"members":      {Members: &casTestMembers},
		"members none": {Members: &[]MemberInput{}},
	}
	for label, req := range cases {
		upd := mustBuildProjectUpdate(t, req)
		if got, want := projectUpdateWritesSomething(req), !upd.Empty; got != want {
			t.Errorf("%s: projectUpdateWritesSomething=%v but buildProjectUpdate.Empty=%v — "+
				"they disagree, so either a real write is rejected as \"changes nothing\" or a "+
				"members_version is accepted with no statement to guard", label, got, upd.Empty)
		}
	}
}

// An empty members list is still a members WRITE (it removes everyone), so it
// must advance the counter like any other.
func TestBuildProjectUpdate_EmptyMembersListStillCounts(t *testing.T) {
	empty := []MemberInput{}
	upd := mustBuildProjectUpdate(t, &UpdateProjectRequest{Members: &empty})
	if upd.Empty {
		t.Fatal("clearing the member list was treated as no update at all")
	}
	if !strings.Contains(setClauseOf(t, upd.Query), "members_version = members_version + 1") {
		t.Error("clearing the member list does not advance members_version, so a stale writer holding " +
			"the old version could still overwrite the (now empty) list unnoticed")
	}
}

// ─── the 400 for a guard with nothing to guard ──────────────────────────────

// Runs against a nil pool: the check is before any database access, so this is
// real behaviour rather than a source scan.
func TestUpdateProject_MembersVersionWithNothingToWriteIs400(t *testing.T) {
	v := 4
	p, err := UpdateProject(t.Context(), nil, "p_probe",
		&UserRecord{ID: "u_probe", Role: "admin"},
		UpdateProjectRequest{MembersVersion: &v})
	if err == nil {
		t.Fatalf("a members_version with nothing to write was accepted; project=%+v", p)
	}
	if err.HTTPStatus != 400 {
		t.Errorf("HTTPStatus = %d, want 400", err.HTTPStatus)
	}
	if !strings.Contains(err.Message, "changes nothing") {
		t.Errorf("the error should say the request writes nothing; got %q", err.Message)
	}
}

// The mirror image, and the guard against the check above being over-tight: a
// members_version alongside a real members write must NOT be rejected before
// the database is reached. Reaching the nil pool (a panic) is the outcome under
// test, exactly as TestCreateWorkItem_ValidTypeIsNotRejectedByValidation does it.
func TestUpdateProject_MembersVersionWithAWriteIsNotRejectedEarly(t *testing.T) {
	defer func() { _ = recover() }()
	v := 4
	if _, err := UpdateProject(t.Context(), nil, "p_probe",
		&UserRecord{ID: "u_probe", Role: "admin"},
		UpdateProjectRequest{Members: &casTestMembers, MembersVersion: &v}); err != nil && err.HTTPStatus == 400 {
		t.Errorf("a legal members + members_version request was rejected: %s", err.Message)
	}
}

// ─── the 409 payload ────────────────────────────────────────────────────────

// The conflict must name the current version so the caller can retry, and must
// be a 409 rather than a 400 — the caller's payload was well formed.
func TestMembersCASConflictErr_ReportsBothVersions(t *testing.T) {
	err := membersCASConflictErr(0, 3)
	if err.HTTPStatus != 409 {
		t.Errorf("HTTPStatus = %d, want 409", err.HTTPStatus)
	}
	if err.Code != ErrConflictCASFailed {
		t.Errorf("Code = %s, want %s", err.Code, ErrConflictCASFailed)
	}
	details, ok := err.Details.(map[string]any)
	if !ok {
		t.Fatalf("Details = %#v, want a map the caller can read the current version out of", err.Details)
	}
	if details["current_members_version"] != 3 {
		t.Errorf("details.current_members_version = %#v, want 3 — without it the caller cannot retry "+
			"without a second read", details["current_members_version"])
	}
	if details["expected_members_version"] != 0 {
		t.Errorf("details.expected_members_version = %#v, want 0", details["expected_members_version"])
	}
	// The numbers must not be transposed in the prose either: "is 3, not the
	// expected 0" and "is 0, not the expected 3" are both plausible sentences
	// and only one of them tells the caller what to send next.
	if !strings.Contains(err.Message, "members_version is 3, not the expected 0") {
		t.Errorf("message does not state current-then-expected: %q", err.Message)
	}
}

// ─── the compiled statement must reach the database unmodified ──────────────

// parseDomainSource parses a file of this package for the structural guard
// below. Structure rather than text: a substring scan can only ever forbid the
// spellings somebody thought of.
func parseDomainSource(t *testing.T, path string) (*token.FileSet, *ast.File) {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	return fset, f
}

// funcDeclNamed returns the top-level (non-method) func declaration named name,
// and fails the test if there is none — so moving or renaming the function under
// guard turns the guard red instead of silently inert.
func funcDeclNamed(t *testing.T, fset *token.FileSet, f *ast.File, name string) *ast.FuncDecl {
	t.Helper()
	for _, d := range f.Decls {
		fn, ok := d.(*ast.FuncDecl)
		if ok && fn.Recv == nil && fn.Name.Name == name && fn.Body != nil {
			return fn
		}
	}
	t.Fatalf("no top-level func %s with a body in %s — it was renamed, moved to another file, or turned "+
		"into a method. This guard now checks nothing; point it at the new declaration.",
		name, fset.Position(f.Pos()).Filename)
	return nil
}

// rootIdentOf peels selectors, indexes, slices, derefs and parens off an
// expression and returns the identifier at its base: `upd`, `upd.Query`,
// `upd.Args[0]`, `(*upd).Query` and `upd.Args[1:]` all root at `upd`. An
// expression not rooted at a plain identifier (a call result, a literal)
// returns nil.
func rootIdentOf(e ast.Expr) *ast.Ident {
	for {
		switch v := e.(type) {
		case *ast.Ident:
			return v
		case *ast.ParenExpr:
			e = v.X
		case *ast.SelectorExpr:
			e = v.X
		case *ast.IndexExpr:
			e = v.X
		case *ast.IndexListExpr:
			e = v.X
		case *ast.SliceExpr:
			e = v.X
		case *ast.StarExpr:
			e = v.X
		case *ast.TypeAssertExpr:
			e = v.X
		default:
			return nil
		}
	}
}

// renderNode prints a node back to source with its whitespace collapsed, so an
// expression assertion survives gofmt alignment and line wrapping.
func renderNode(t *testing.T, fset *token.FileSet, n ast.Node) string {
	t.Helper()
	var b strings.Builder
	if err := printer.Fprint(&b, fset, n); err != nil {
		t.Fatalf("render %T: %v", n, err)
	}
	return strings.Join(strings.Fields(b.String()), " ")
}

// UpdateProject must execute exactly the statement buildProjectUpdate compiled.
//
// A source-structure guard, and deliberately so: this is the one aihub#241
// invariant with no behavioural test that can fail. Rewriting
// `members_version = members_version + 1` into a literal computed in Go from the
// row read under SELECT ... FOR UPDATE produces identical results in every test
// above — the row lock serialises the two writers, so the Go-computed value is
// current. It is still the form aihub#241 records as a failure, because its
// correctness rests entirely on that lock rather than on the arithmetic, and the
// lock is not what anybody reading the SET clause would check.
//
// ─── what this asserts ──────────────────────────────────────────────────────
//
// Over UpdateProject's AST, with the variable name read off the
// buildProjectUpdate call site rather than hardcoded:
//
//  1. buildProjectUpdate is called exactly once, and its result is bound to a
//     plain identifier (call it `upd`). Otherwise: fatal, this guard is lost.
//  2. `tx.QueryRow(ctx, upd.Query, upd.Args...)` appears exactly once. Fatal,
//     because a guard that no longer sees the execution site guards nothing.
//  3. Nothing other than that one declaration assigns to anything rooted at
//     `upd` — no `upd = …`, `upd.Query = …`, `upd.Query += …`, `upd.Args[0] = …`,
//     `upd.X++`, `for upd.X = range …` — and `upd` is never taken by address
//     (`&upd`, `&upd.Query`), which is the other way a callee could rewrite it.
//  4. In buildProjectUpdate, the string literal
//     `members_version = members_version + 1` appears exactly ONCE — a count,
//     not a boolean, so appending a second increment (which would advance the
//     counter by 2 and invalidate every token a caller is holding) is red too.
//
// An earlier version of this guard tested `strings.Contains(body, "upd.Query =")`.
// That was one spelling of an unbounded class, and it was measured green against
// a whole-struct reassignment (`upd = projectUpdate{Query: patched, …}`) that
// expressed exactly the defect the guard exists to catch. Adding more substrings
// would not have closed it. The AST form is mutation-verified against three
// mutants: whole-struct reassignment, `upd.Query += …`, and `upd.Query = upd.Query`.
//
// ─── what it does NOT cover ─────────────────────────────────────────────────
//
// It is a syntactic check over one function in one file, with no type
// information, so it cannot see:
//
//   - a pointer-receiver method call, `upd.patch()`, which takes the address
//     implicitly and appears in the AST as neither an assignment nor a `&`;
//   - mutation through an alias of a reference-typed field —
//     `a := upd.Args; a[0] = …` writes the same backing array;
//   - anything wrong INSIDE buildProjectUpdate (that is what every other test
//     in this file is for) or in any other function UpdateProject calls;
//   - a rewrite of `req` before buildProjectUpdate is called.
//
// The reason it is still worth having is that the cheapest way to introduce
// aihub#241 failure mode 1 is to patch the compiled string in place, and every
// in-place patch has to name `upd` on a left-hand side or take its address.
func TestUpdateProjectExecutesTheCompiledStatementUnmodified(t *testing.T) {
	fset, file := parseDomainSource(t, "projects.go")
	fn := funcDeclNamed(t, fset, file, "UpdateProject")

	// (1) Locate the compiled statement and learn what it is called here.
	var buildCall *ast.CallExpr
	buildCalls := 0
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		if id, ok := call.Fun.(*ast.Ident); ok && id.Name == "buildProjectUpdate" {
			buildCalls++
			buildCall = call
		}
		return true
	})
	if buildCalls != 1 {
		t.Fatalf("UpdateProject calls buildProjectUpdate %d times, want exactly 1 — this guard cannot tell "+
			"which result reaches the database, so it is asserting nothing. Update it.", buildCalls)
	}
	var declStmt *ast.AssignStmt
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		as, ok := n.(*ast.AssignStmt)
		if !ok {
			return true
		}
		for _, rhs := range as.Rhs {
			if rhs == ast.Expr(buildCall) {
				declStmt = as
			}
		}
		return true
	})
	if declStmt == nil || len(declStmt.Lhs) == 0 {
		t.Fatal("buildProjectUpdate's result is not bound to a variable in UpdateProject — this guard tracks " +
			"that variable, so it can no longer see whether the compiled statement is patched. Update it.")
	}
	updIdent, ok := declStmt.Lhs[0].(*ast.Ident)
	if !ok || updIdent.Name == "_" {
		t.Fatalf("buildProjectUpdate's result is bound to %q, not a plain named variable, so this guard "+
			"cannot follow it. Update it.", renderNode(t, fset, declStmt.Lhs[0]))
	}
	upd := updIdent.Name

	// (2) The compiled statement is what gets executed, verbatim and once.
	// Fatal: without this, the negative assertion below could be satisfied by
	// the execution site having moved somewhere this guard does not look.
	wantExec := fmt.Sprintf("tx.QueryRow(ctx, %s.Query, %s.Args...)", upd, upd)
	execs := 0
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		if call, ok := n.(*ast.CallExpr); ok && renderNode(t, fset, call) == wantExec {
			execs++
		}
		return true
	})
	if execs != 1 {
		t.Fatalf("UpdateProject contains %d occurrences of %s, want exactly 1 — it no longer executes "+
			"buildProjectUpdate's compiled query directly. Update this guard, and make sure whatever "+
			"replaced it still runs the statement verbatim.", execs, wantExec)
	}

	// (3) Nothing patches it on the way there.
	const why = "Every assertion about the SET clause is made against buildProjectUpdate's output, so a " +
		"rewrite here is invisible to all of them — including the one that requires members_version to be " +
		"incremented by Postgres rather than computed in Go (aihub#241 failure mode 1)."
	flag := func(what string, n ast.Node) {
		t.Errorf("UpdateProject modifies the statement buildProjectUpdate compiled: %s at %s\n  %s\n%s",
			what, fset.Position(n.Pos()), renderNode(t, fset, n), why)
	}
	checkLHS := func(what string, n ast.Node, targets ...ast.Expr) {
		for _, e := range targets {
			if e == nil {
				continue
			}
			if id := rootIdentOf(e); id != nil && id.Name == upd {
				flag(what, n)
				return
			}
		}
	}
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		switch s := n.(type) {
		case *ast.AssignStmt:
			if s == declStmt {
				// The one legitimate write: the declaration itself. Its
				// right-hand side is still walked, so `&upd` hidden in there
				// would still be caught.
				return true
			}
			checkLHS(fmt.Sprintf("assignment (%s)", s.Tok), s, s.Lhs...)
		case *ast.IncDecStmt:
			checkLHS(fmt.Sprintf("%s statement", s.Tok), s, s.X)
		case *ast.RangeStmt:
			checkLHS("range assignment", s, s.Key, s.Value)
		case *ast.UnaryExpr:
			if s.Op == token.AND {
				checkLHS("address taken (a callee can write through it)", s, s.X)
			}
		}
		return true
	})

	// (4) And the increment it compiles is there exactly once.
	const incr = "members_version = members_version + 1"
	build := funcDeclNamed(t, fset, file, "buildProjectUpdate")
	incrs := 0
	ast.Inspect(build.Body, func(n ast.Node) bool {
		lit, ok := n.(*ast.BasicLit)
		if !ok || lit.Kind != token.STRING {
			return true
		}
		s, err := strconv.Unquote(lit.Value)
		if err != nil {
			s = lit.Value
		}
		incrs += strings.Count(s, incr)
		return true
	})
	if incrs != 1 {
		t.Errorf("buildProjectUpdate's string literals contain %q %d times, want exactly 1.\n"+
			"0 means the in-database increment is gone (aihub#241 failure mode 1); more than 1 means a "+
			"members write advances the counter by more than one step, which invalidates the token every "+
			"caller is holding and turns a correct compare-and-set into a 409.", incr, incrs)
	}
}
