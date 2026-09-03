package server

// aihub#253 — the /ui artifact side rail's version list must (A) show exactly
// the lineage members the caller may see, and (B) cost one memory load no
// matter how long the lineage is.
//
// The two gates fail differently and are deliberately separate:
//
//	A  is a permissions gate. It was already satisfied before aihub#253 (that
//	   is aihub#248's W1 fix) and stays satisfied after: the point of the
//	   change is that the filter now reads project/visibility/author_user_id
//	   off the chain row instead of re-reading each version's whole record, so
//	   A is what proves the semantics survived the rewrite. It only goes red
//	   under mutation.
//
//	B  is the N+1 gate, and it is a COUNT, not a duration. On this repo, over
//	   identical work, wall clock spread 4.1x under load while operation
//	   counters stayed bit-identical (aihub#339 replaced seven wall-clock
//	   ceilings in internal/render for exactly this reason), so a timing
//	   threshold cannot separate this regression from a busy machine. The
//	   count can: pre-aihub#253 it is 1 + (N-1) and grows with the lineage,
//	   after it is 1 for every N.
//
//	C  guards the one sharp edge the fix introduces — versionRefVisibleTo
//	   hands memoryVisibleTo a PARTIAL *domain.Memory, so it asserts
//	   memoryVisibleTo reads no field beyond the ones that partial populates.

import (
	"context"
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/labstack/echo/v4"

	"github.com/GMISWE/ieops-aihub/internal/domain"
)

// ─── fixtures ───────────────────────────────────────────────────────────────

const (
	authzChainProject = "testproj"
	authzChainAuthor  = "u_author"
	// The id and date of the version the viewer must never see. Both are
	// distinctive so a leak of EITHER (aihub#248 W1 covers id, date and href
	// alike) shows up as a substring hit rather than needing a parser.
	hiddenVersionID   = "mem_hidden_ZZZQQ"
	hiddenVersionDate = "2001-09-11"
)

// projectViewer is a caller with viewer access to authzChainProject and no
// global admin bit — the only kind of caller for whom the per-version
// visibility filter can actually exclude anything.
func projectViewer(userID string) *UserContext {
	return &UserContext{
		UserID:       userID,
		DisplayName:  userID,
		Role:         "writer", // global role; the project role is what decides
		ProjectRoles: map[string]string{authzChainProject: "viewer"},
		APIKeyID:     "k_" + userID,
	}
}

// authzChainMem is the lineage head served as the primary record.
func authzChainMem(id string) *domain.Memory {
	m := &domain.Memory{
		ID:           id,
		Project:      authzChainProject,
		Type:         "methodology.spec",
		Visibility:   "project",
		AuthorUserID: authzChainAuthor,
		Content:      "# Spec\n\nbody",
		RenderedHTML: htmlPtr("<h1>Spec</h1><p>body</p>"),
		Commits:      json.RawMessage(`[]`),
		// Self-headed, so the /ui head redirect is a no-op and resolveLatestFn
		// is never consulted — this test is about the side rail, not #248's
		// redirect.
		LatestID: strptr(id),
	}
	return m
}

// versionRef builds one lineage row.
func versionRef(id, date, visibility, author string, current bool) domain.MemoryVersionRef {
	return domain.MemoryVersionRef{
		ID:           id,
		CreatedAt:    date + "T00:00:00Z",
		Status:       map[bool]string{true: "active", false: "archived"}[current],
		IsCurrent:    current,
		Project:      authzChainProject,
		Visibility:   visibility,
		AuthorUserID: author,
	}
}

// withVersionChain installs a fixed lineage behind the versionChainFn seam.
func withVersionChain(chain []domain.MemoryVersionRef) func() {
	prev := versionChainFn
	versionChainFn = func(_ context.Context, _ *pgxpool.Pool, _ string) ([]domain.MemoryVersionRef, error) {
		return chain, nil
	}
	return func() { versionChainFn = prev }
}

// withLoadMemoryByID installs an ID-DISPATCHING loadMemoryFn and counts calls.
//
// Dispatching on the id is load-bearing for gate A, not tidiness. A fake that
// ignores its id argument and returns the head for every lookup would report
// the head's visibility for every version, so the pre-aihub#253 code path
// (which authorized each version from a full record fetched through this seam)
// would authorize the hidden version as if it were the head — and gate A would
// pass on broken code. This is the same trap aihub#248's review flagged as W3
// for withResolveLatestOverride.
func withLoadMemoryByID(t *testing.T, byID map[string]*domain.Memory, calls *int) func() {
	t.Helper()
	prev := loadMemoryFn
	loadMemoryFn = func(_ context.Context, _ *pgxpool.Pool, id string) (*domain.Memory, *domain.AihubError) {
		*calls++
		m, ok := byID[id]
		if !ok {
			return nil, domain.NewErr(domain.ErrNotFound, "memory not found")
		}
		return m, nil
	}
	return func() { loadMemoryFn = prev }
}

// renderSideRail drives the real handleArtifactHTML over the /ui route and
// returns the response body.
func renderSideRail(t *testing.T, headID string, u *UserContext) string {
	t.Helper()
	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/ui/artifacts/"+headID+"/html", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetPath("/ui/artifacts/:id/html")
	c.SetParamNames("id")
	c.SetParamValues(headID)
	setUser(c, u)

	if err := handleArtifactHTML(nil)(c); err != nil {
		e.HTTPErrorHandler(err, c)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200 (body=%.400s)", rec.Code, rec.Body.String())
	}
	return rec.Body.String()
}

// countVersionRows counts side-rail version rows in a rendered page.
func countVersionRows(body string) int {
	return strings.Count(body, `class="pf-side-vrow`)
}

// ─── Gate A: a version the caller may not see stays hidden ──────────────────

// TestArtifactSideRail_VersionCallerCannotSeeIsOmittedEntirely is the negative
// half of gate A, exercised through the real handler.
//
// The lineage has four members. The second is visibility="private" and
// authored by somebody else, so a plain project viewer must not see it — and
// "not see it" means the whole row is gone: not its id, not its date, not a
// link, and not a hole in the version numbering (aihub#248 minor 4 — labels
// v1, v3, v4 would themselves disclose that a version exists between v1 and
// v3).
func TestArtifactSideRail_VersionCallerCannotSeeIsOmittedEntirely(t *testing.T) {
	const headID = "mem_head_v4"
	chain := []domain.MemoryVersionRef{
		versionRef("mem_v1", "2026-01-01", "project", authzChainAuthor, false),
		versionRef(hiddenVersionID, hiddenVersionDate, "private", "u_somebody_else", false),
		versionRef("mem_v3", "2026-03-03", "project", authzChainAuthor, false),
		versionRef(headID, "2026-04-04", "project", authzChainAuthor, true),
	}
	defer withVersionChain(chain)()

	head := authzChainMem(headID)
	byID := map[string]*domain.Memory{
		headID:   head,
		"mem_v1": {ID: "mem_v1", Project: authzChainProject, Visibility: "project", AuthorUserID: authzChainAuthor},
		hiddenVersionID: {
			ID: hiddenVersionID, Project: authzChainProject,
			Visibility: "private", AuthorUserID: "u_somebody_else",
		},
		"mem_v3": {ID: "mem_v3", Project: authzChainProject, Visibility: "project", AuthorUserID: authzChainAuthor},
	}
	var calls int
	defer withLoadMemoryByID(t, byID, &calls)()

	body := renderSideRail(t, headID, projectViewer("u_viewer"))

	// 1. Nothing about the hidden version reaches the page.
	if strings.Contains(body, hiddenVersionID) {
		t.Errorf("side rail leaked the id of a version the caller may not see (%s)", hiddenVersionID)
	}
	if strings.Contains(body, hiddenVersionDate) {
		t.Errorf("side rail leaked the date of a version the caller may not see (%s)", hiddenVersionDate)
	}

	// 2. Exactly the three permitted rows render.
	if got := countVersionRows(body); got != 3 {
		t.Errorf("version rows: got %d, want 3 (1 of 4 lineage members is denied)", got)
	}

	// 3. Labels are renumbered over the FILTERED list, so there is no gap to
	//    infer a hidden version from. Four members minus one denied is v1..v3,
	//    and a "v4" anywhere means the labels were taken from the unfiltered
	//    chain index.
	for _, want := range []string{">v1<", ">v2<", ">v3<"} {
		if !strings.Contains(body, want) {
			t.Errorf("missing version label %s; excerpt: %s", want, excerptStr(body))
		}
	}
	if strings.Contains(body, ">v4<") {
		t.Errorf("version labels were numbered from the unfiltered chain index (found v4 with only 3 visible rows)")
	}

	// 4. The visible rows' own hrefs still work (the filter must drop rows, not
	//    de-link them — a de-linked row still discloses id and date).
	if !strings.Contains(body, "/ui/artifacts/mem_v1/html?"+exactVersionParam+"=1") {
		t.Errorf("permitted version mem_v1 lost its pf_exact link; excerpt: %s", excerptStr(body))
	}
}

// TestArtifactSideRail_AdminSeesEveryVersion is the positive half of gate A:
// the filter must exclude only what it has to. An admin sees all four rows,
// hidden one included — so a "fix" that simply drops private versions for
// everybody fails here.
func TestArtifactSideRail_AdminSeesEveryVersion(t *testing.T) {
	const headID = "mem_head_v4"
	chain := []domain.MemoryVersionRef{
		versionRef("mem_v1", "2026-01-01", "project", authzChainAuthor, false),
		versionRef(hiddenVersionID, hiddenVersionDate, "private", "u_somebody_else", false),
		versionRef("mem_v3", "2026-03-03", "project", authzChainAuthor, false),
		versionRef(headID, "2026-04-04", "project", authzChainAuthor, true),
	}
	defer withVersionChain(chain)()

	byID := map[string]*domain.Memory{
		headID:   authzChainMem(headID),
		"mem_v1": {ID: "mem_v1", Project: authzChainProject, Visibility: "project", AuthorUserID: authzChainAuthor},
		hiddenVersionID: {
			ID: hiddenVersionID, Project: authzChainProject,
			Visibility: "private", AuthorUserID: "u_somebody_else",
		},
		"mem_v3": {ID: "mem_v3", Project: authzChainProject, Visibility: "project", AuthorUserID: authzChainAuthor},
	}
	var calls int
	defer withLoadMemoryByID(t, byID, &calls)()

	body := renderSideRail(t, headID, adminUser())

	if got := countVersionRows(body); got != 4 {
		t.Errorf("version rows for admin: got %d, want 4 (admin may see the whole lineage)", got)
	}
	if !strings.Contains(body, hiddenVersionID) {
		t.Errorf("admin must see the private version's row; excerpt: %s", excerptStr(body))
	}
}

// TestArtifactSideRail_PrivateVersionVisibleToItsOwnAuthor pins the third arm
// of the visibility rule: "private" is not "hidden from non-admins", it is
// "visible to its author". This is the case that would break if
// versionRefVisibleTo stopped supplying AuthorUserID (it would then compare
// the caller against "" and hide the row from its own author).
func TestArtifactSideRail_PrivateVersionVisibleToItsOwnAuthor(t *testing.T) {
	const headID = "mem_head_v3"
	chain := []domain.MemoryVersionRef{
		versionRef("mem_v1", "2026-01-01", "project", authzChainAuthor, false),
		versionRef(hiddenVersionID, hiddenVersionDate, "private", "u_viewer", false),
		versionRef(headID, "2026-03-03", "project", authzChainAuthor, true),
	}
	defer withVersionChain(chain)()

	byID := map[string]*domain.Memory{
		headID:   authzChainMem(headID),
		"mem_v1": {ID: "mem_v1", Project: authzChainProject, Visibility: "project", AuthorUserID: authzChainAuthor},
		hiddenVersionID: {
			ID: hiddenVersionID, Project: authzChainProject,
			Visibility: "private", AuthorUserID: "u_viewer",
		},
	}
	var calls int
	defer withLoadMemoryByID(t, byID, &calls)()

	body := renderSideRail(t, headID, projectViewer("u_viewer"))

	if got := countVersionRows(body); got != 3 {
		t.Errorf("version rows: got %d, want 3 (the private version is the caller's own)", got)
	}
	if !strings.Contains(body, hiddenVersionID) {
		t.Errorf("a caller's own private version must still be listed; excerpt: %s", excerptStr(body))
	}
}

// TestArtifactSideRail_VersionInAnotherProjectIsOmitted covers the other
// predicate in the pair. UpdateMemory can in principle let a version's project
// diverge from its predecessor's, and hasProjectAccess is what catches it —
// this is the case that breaks if versionRefVisibleTo stops supplying Project.
func TestArtifactSideRail_VersionInAnotherProjectIsOmitted(t *testing.T) {
	const headID = "mem_head_v3"
	foreign := versionRef(hiddenVersionID, hiddenVersionDate, "project", authzChainAuthor, false)
	foreign.Project = "someone_elses_project"
	chain := []domain.MemoryVersionRef{
		versionRef("mem_v1", "2026-01-01", "project", authzChainAuthor, false),
		foreign,
		versionRef(headID, "2026-03-03", "project", authzChainAuthor, true),
	}
	defer withVersionChain(chain)()

	byID := map[string]*domain.Memory{
		headID:   authzChainMem(headID),
		"mem_v1": {ID: "mem_v1", Project: authzChainProject, Visibility: "project", AuthorUserID: authzChainAuthor},
		hiddenVersionID: {
			ID: hiddenVersionID, Project: "someone_elses_project",
			Visibility: "project", AuthorUserID: authzChainAuthor,
		},
	}
	var calls int
	defer withLoadMemoryByID(t, byID, &calls)()

	body := renderSideRail(t, headID, projectViewer("u_viewer"))

	if strings.Contains(body, hiddenVersionID) {
		t.Errorf("side rail leaked a version living in a project the caller has no access to")
	}
	if got := countVersionRows(body); got != 2 {
		t.Errorf("version rows: got %d, want 2", got)
	}
}

// ─── Gate B: the N+1 itself, as a count ─────────────────────────────────────

// TestArtifactSideRail_VersionChainCostsOneMemoryLoad is the N+1 gate.
//
// loadMemoryFn is domain.GetMemoryByID, which issues exactly one statement per
// call — that 1:1 mapping was measured at the pgx pool with a QueryTracer over
// chain lengths 1/2/3/5/20, where total statements were N+1 and loadMemoryFn
// calls were N. So counting this seam counts SELECTs, and the count is exact
// and load-immune where a duration would be neither.
//
// The assertion is "1, for every N", not "fewer than before": a ceiling that
// scales with N is not a gate, and a wall-clock budget would be a different
// instrument measuring a different thing (see the file header).
func TestArtifactSideRail_VersionChainCostsOneMemoryLoad(t *testing.T) {
	for _, n := range []int{2, 3, 20, 26} {
		t.Run(strings.Join([]string{"chain", strconv.Itoa(n)}, "-"), func(t *testing.T) {
			headID := "mem_head_" + strconv.Itoa(n)
			chain := make([]domain.MemoryVersionRef, 0, n)
			byID := map[string]*domain.Memory{headID: authzChainMem(headID)}
			for i := 0; i < n-1; i++ {
				id := "mem_old_" + strconv.Itoa(i)
				chain = append(chain, versionRef(id, "2026-01-0"+strconv.Itoa(1+i%9), "project", authzChainAuthor, false))
				byID[id] = &domain.Memory{
					ID: id, Project: authzChainProject,
					Visibility: "project", AuthorUserID: authzChainAuthor,
				}
			}
			chain = append(chain, versionRef(headID, "2026-12-31", "project", authzChainAuthor, true))
			defer withVersionChain(chain)()

			var calls int
			defer withLoadMemoryByID(t, byID, &calls)()

			body := renderSideRail(t, headID, projectViewer("u_viewer"))

			// The page must still show the whole lineage — a count gate that
			// passes because the version list vanished is worthless.
			if got := countVersionRows(body); got != n {
				t.Fatalf("version rows: got %d, want %d — the count assertion below "+
					"only means something if the rail actually rendered", got, n)
			}
			if calls != 1 {
				t.Errorf("loadMemoryFn calls for a %d-version lineage: got %d, want 1 "+
					"(the primary record only). %d extra full-row SELECTs means the "+
					"aihub#253 N+1 is back: the side rail is re-reading each version's "+
					"26 columns to read project/visibility/author_user_id, which "+
					"domain.MemoryVersionRef already carries.", n, calls, calls-1)
			}
		})
	}
}

// TestArtifactSideRail_LoadCountIsIndependentOfChainLength states gate B as the
// invariant rather than as a magic number: whatever the per-request cost is, it
// must not be a function of how long the lineage is. A future change that made
// the head cost two loads instead of one would keep this green while an
// N-shaped regression cannot.
func TestArtifactSideRail_LoadCountIsIndependentOfChainLength(t *testing.T) {
	countFor := func(n int) int {
		headID := "mem_head_indep"
		chain := make([]domain.MemoryVersionRef, 0, n)
		byID := map[string]*domain.Memory{headID: authzChainMem(headID)}
		for i := 0; i < n-1; i++ {
			id := "mem_indep_" + strconv.Itoa(i)
			chain = append(chain, versionRef(id, "2026-01-01", "project", authzChainAuthor, false))
			byID[id] = &domain.Memory{
				ID: id, Project: authzChainProject,
				Visibility: "project", AuthorUserID: authzChainAuthor,
			}
		}
		chain = append(chain, versionRef(headID, "2026-12-31", "project", authzChainAuthor, true))
		restoreChain := withVersionChain(chain)
		defer restoreChain()

		var calls int
		restoreLoad := withLoadMemoryByID(t, byID, &calls)
		defer restoreLoad()

		if got := countVersionRows(renderSideRail(t, headID, projectViewer("u_viewer"))); got != n {
			t.Fatalf("version rows: got %d, want %d", got, n)
		}
		return calls
	}

	short, long := countFor(2), countFor(26)
	if short != long {
		t.Errorf("memory loads scale with lineage length: 2-version chain cost %d, "+
			"26-version chain cost %d. The per-request cost must not depend on N "+
			"(aihub#253).", short, long)
	}
}

// ─── Gate C: the partial-record edge versionRefVisibleTo relies on ──────────

// TestMemoryVisibleTo_ReadsOnlyTheFieldsTheChainRowSupplies is the drift guard
// for versionRefVisibleTo, which authorizes a lineage row by handing
// memoryVisibleTo a *domain.Memory populated from a MemoryVersionRef —
// Project, Visibility and AuthorUserID only, every other field left at its zero
// value. That is safe exactly as long as memoryVisibleTo reads nothing else. A
// future read of some other field would see a zero value here and could answer
// "visible" for a row that is not, with no compiler error.
//
// This asks the question SYNTACTICALLY — which fields does the function read? —
// because that is what the question actually is. The first version of this gate
// asked it behaviourally instead: fill each other field with one non-zero
// sentinel and assert the decision does not move. That gate was measured
// against a mutant which added `if mem.Status == "archived" { return true }`
// and it PASSED, because neither "" nor the sentinel equals "archived". A probe
// value cannot discover which value a field is compared against, so a
// behavioural probe over one value per field is a proxy for field-dependence,
// not a classification of it. The AST is the classification.
//
// It also rejects any use of the parameter that is not one of those field
// reads — `helper(mem)`, `m := mem`, `*mem` — since each of those moves the
// field access somewhere this test cannot see.
func TestMemoryVisibleTo_ReadsOnlyTheFieldsTheChainRowSupplies(t *testing.T) {
	// The permitted set is DERIVED from versionRefVisibleTo's own source, not
	// written down here.
	//
	// A hand-written list would be this gate's escape hatch, and a cheap one:
	// silencing a real violation would cost one map key in this test, while
	// complying would cost a field on domain.MemoryVersionRef, a column in its
	// SQL, a copy in orderVersionChain and a line in versionRefVisibleTo. Cheapest
	// wins, so the gate would lose. Measured: adding `"Status": true` to a
	// hand-written list did silence a `mem.Status == "archived"` mutant with no
	// other test objecting.
	//
	// Derived, the only way to widen the permitted set is to add the field to
	// versionRefVisibleTo's composite literal — which either supplies it for real
	// (the fix) or fails to compile because MemoryVersionRef does not carry it
	// (forcing the fix). Every cheap path to green is now a correct one.
	allowed := fieldsPopulatedByVersionRefVisibleTo(t)

	const fnName = "memoryVisibleTo"

	// Search the whole package rather than hard-coding a filename, so moving the
	// function between files in this package does not quietly disable the gate.
	goFiles, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatalf("glob: %v", err)
	}
	fset := token.NewFileSet()
	var fn *ast.FuncDecl
	var inFile string
	for _, f := range goFiles {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		parsed, perr := parser.ParseFile(fset, f, nil, 0)
		if perr != nil {
			t.Fatalf("parse %s: %v", f, perr)
		}
		for _, decl := range parsed.Decls {
			fd, ok := decl.(*ast.FuncDecl)
			if ok && fd.Recv == nil && fd.Name.Name == fnName {
				if fn != nil {
					t.Fatalf("found %s in both %s and %s", fnName, inFile, f)
				}
				fn, inFile = fd, f
			}
		}
	}
	if fn == nil {
		t.Fatalf("could not find func %s in package server. If it was renamed or "+
			"moved out of this package, retarget this test — do not delete it: "+
			"versionRefVisibleTo still hands it a partial *domain.Memory.", fnName)
	}

	// Which parameter is the *domain.Memory?
	var param string
	for _, field := range fn.Type.Params.List {
		star, ok := field.Type.(*ast.StarExpr)
		if !ok {
			continue
		}
		sel, ok := star.X.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != "Memory" {
			continue
		}
		if len(field.Names) != 1 {
			t.Fatalf("%s: the *domain.Memory parameter is not a single named one", fnName)
		}
		param = field.Names[0].Name
	}
	if param == "" {
		t.Fatalf("%s no longer takes a *domain.Memory parameter; retarget this test", fnName)
	}

	// Pass 1: every `param.Field` selector, and the idents it accounts for.
	accounted := map[*ast.Ident]bool{}
	read := map[string]token.Pos{}
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		if sel, ok := n.(*ast.SelectorExpr); ok {
			if id, ok := sel.X.(*ast.Ident); ok && id.Name == param {
				accounted[id] = true
				if _, seen := read[sel.Sel.Name]; !seen {
					read[sel.Sel.Name] = sel.Sel.Pos()
				}
			}
		}
		return true
	})

	// Pass 2: any other mention of the parameter escapes this analysis.
	var strays []string
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		if id, ok := n.(*ast.Ident); ok && id.Name == param && !accounted[id] {
			strays = append(strays, fset.Position(id.Pos()).String())
		}
		return true
	})
	if len(strays) > 0 {
		t.Errorf("%s uses its *domain.Memory parameter %q other than as a direct "+
			"field read, at %s. versionRefVisibleTo passes a PARTIAL Memory built from "+
			"a MemoryVersionRef, so any field reached indirectly (passed to a helper, "+
			"aliased, or copied wholesale) is a field this gate can no longer check "+
			"and may be zero at that call site.", fnName, param, strings.Join(strays, ", "))
	}

	if len(read) == 0 {
		t.Fatalf("%s reads no field of %q at all — either the function changed shape "+
			"or this analysis is broken; either way the gate is vacuous", fnName, param)
	}

	for field, pos := range read {
		if allowed[field] {
			continue
		}
		t.Errorf("%s reads domain.Memory.%s (at %s), which versionRefVisibleTo does "+
			"NOT populate — it builds a partial Memory from a MemoryVersionRef and fills "+
			"only %v — so at that call site %s is the zero value and the /ui artifact "+
			"side rail would decide a version's visibility from it. Either carry %s "+
			"through domain.MemoryVersionRef (its SQL projection, versionNode, "+
			"orderVersionChain) and populate it in versionRefVisibleTo, or keep %s off "+
			"this predicate.",
			fnName, field, fset.Position(pos), sortedKeys(allowed), field, field, field)
	}
}

// fieldsPopulatedByVersionRefVisibleTo reads versionRefVisibleTo's source and
// returns the set of domain.Memory fields it fills in the partial record it
// hands to memoryVisibleTo. Fails the test rather than returning a guess: a
// silent empty set here would make the caller's subset check vacuous, which is
// the failure mode this whole gate exists to avoid.
func fieldsPopulatedByVersionRefVisibleTo(t *testing.T) map[string]bool {
	t.Helper()
	const fnName = "versionRefVisibleTo"

	goFiles, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatalf("glob: %v", err)
	}
	fset := token.NewFileSet()
	var fn *ast.FuncDecl
	for _, f := range goFiles {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		parsed, perr := parser.ParseFile(fset, f, nil, 0)
		if perr != nil {
			t.Fatalf("parse %s: %v", f, perr)
		}
		for _, decl := range parsed.Decls {
			if fd, ok := decl.(*ast.FuncDecl); ok && fd.Recv == nil && fd.Name.Name == fnName {
				fn = fd
			}
		}
	}
	if fn == nil {
		t.Fatalf("could not find func %s in package server. If the /ui side rail no "+
			"longer authorizes a lineage row from a partial domain.Memory, retarget "+
			"this gate at whatever replaced it — do not delete it.", fnName)
	}

	out := map[string]bool{}
	var positional bool
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		lit, ok := n.(*ast.CompositeLit)
		if !ok {
			return true
		}
		sel, ok := lit.Type.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != "Memory" {
			return true
		}
		for _, el := range lit.Elts {
			kv, ok := el.(*ast.KeyValueExpr)
			if !ok {
				positional = true
				continue
			}
			if id, ok := kv.Key.(*ast.Ident); ok {
				out[id.Name] = true
			}
		}
		return true
	})
	if positional {
		t.Fatalf("%s builds its domain.Memory with a POSITIONAL composite literal; "+
			"this gate can only read a keyed one. Use field names.", fnName)
	}
	if len(out) == 0 {
		t.Fatalf("%s no longer builds a domain.Memory composite literal, so the set of "+
			"fields it populates cannot be derived and the subset check below would "+
			"pass vacuously. Retarget this gate.", fnName)
	}
	return out
}

// sortedKeys renders a set deterministically for failure messages.
func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
