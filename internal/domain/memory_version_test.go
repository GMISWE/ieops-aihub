package domain

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"strings"
	"testing"
)

// TestOrderVersionChain_* tests the pure chain-ordering helper without a DB.

// helper: build a versionNode map from a flat list of (id, supersedesID, status).
func buildNodes(rows []struct{ id, sup, status string }) map[string]versionNode {
	m := make(map[string]versionNode, len(rows))
	for _, r := range rows {
		m[r.id] = versionNode{SupersedesID: r.sup, Status: r.status, CreatedAt: "2024-01-01T00:00:00Z"}
	}
	return m
}

// Single version (no chain) → 1-element slice, IsCurrent=true.
func TestOrderVersionChain_SingleVersion(t *testing.T) {
	nodes := buildNodes([]struct{ id, sup, status string }{
		{"mem_a", "", "active"},
	})
	chain := orderVersionChain(nodes, "mem_a", 100)
	if len(chain) != 1 {
		t.Fatalf("want 1 entry, got %d", len(chain))
	}
	if chain[0].ID != "mem_a" {
		t.Errorf("want id=mem_a, got %s", chain[0].ID)
	}
	if !chain[0].IsCurrent {
		t.Errorf("single version must be IsCurrent")
	}
}

// Linear chain v1→v2→v3 (v3 is head, active).
// v3.supersedes=v2, v2.supersedes=v1. Returns [v1, v2, v3].
func TestOrderVersionChain_ThreeVersions_OldestFirst(t *testing.T) {
	nodes := buildNodes([]struct{ id, sup, status string }{
		{"mem_v1", "", "archived"},
		{"mem_v2", "mem_v1", "archived"},
		{"mem_v3", "mem_v2", "active"},
	})
	// Start from the middle version — chain must include all three.
	chain := orderVersionChain(nodes, "mem_v2", 100)
	if len(chain) != 3 {
		t.Fatalf("want 3 entries, got %d: %+v", len(chain), chain)
	}
	want := []string{"mem_v1", "mem_v2", "mem_v3"}
	for i, w := range want {
		if chain[i].ID != w {
			t.Errorf("chain[%d].ID = %q, want %q", i, chain[i].ID, w)
		}
	}
	// Only v3 is IsCurrent.
	for i, r := range chain {
		wantCurrent := r.ID == "mem_v3"
		if r.IsCurrent != wantCurrent {
			t.Errorf("chain[%d] IsCurrent=%v, want %v (id=%s)", i, r.IsCurrent, wantCurrent, r.ID)
		}
	}
}

// Start from the head — same chain is returned.
func TestOrderVersionChain_StartFromHead(t *testing.T) {
	nodes := buildNodes([]struct{ id, sup, status string }{
		{"mem_v1", "", "archived"},
		{"mem_v2", "mem_v1", "active"},
	})
	chain := orderVersionChain(nodes, "mem_v2", 100)
	if len(chain) != 2 {
		t.Fatalf("want 2 entries, got %d", len(chain))
	}
	if chain[0].ID != "mem_v1" || chain[1].ID != "mem_v2" {
		t.Errorf("wrong order: %v", chain)
	}
}

// Start from the oldest — same chain.
func TestOrderVersionChain_StartFromOldest(t *testing.T) {
	nodes := buildNodes([]struct{ id, sup, status string }{
		{"mem_v1", "", "archived"},
		{"mem_v2", "mem_v1", "active"},
	})
	chain := orderVersionChain(nodes, "mem_v1", 100)
	if len(chain) != 2 {
		t.Fatalf("want 2 entries, got %d", len(chain))
	}
	if chain[0].ID != "mem_v1" || chain[1].ID != "mem_v2" {
		t.Errorf("wrong order: %v", chain)
	}
}

// No active node: IsCurrent goes to the last (newest) entry.
func TestOrderVersionChain_NoActiveHead_LastIsCurrent(t *testing.T) {
	nodes := buildNodes([]struct{ id, sup, status string }{
		{"mem_v1", "", "archived"},
		{"mem_v2", "mem_v1", "archived"},
	})
	chain := orderVersionChain(nodes, "mem_v1", 100)
	if len(chain) != 2 {
		t.Fatalf("want 2 entries, got %d", len(chain))
	}
	if chain[0].IsCurrent {
		t.Errorf("v1 must NOT be IsCurrent when v2 is also archived")
	}
	if !chain[1].IsCurrent {
		t.Errorf("last entry must be IsCurrent when none is active")
	}
}

// MemoryVersionRef.Status is preserved faithfully.
func TestOrderVersionChain_StatusPreserved(t *testing.T) {
	nodes := buildNodes([]struct{ id, sup, status string }{
		{"mem_v1", "", "archived"},
		{"mem_v2", "mem_v1", "active"},
	})
	chain := orderVersionChain(nodes, "mem_v1", 100)
	if chain[0].Status != "archived" {
		t.Errorf("v1 status=%q, want archived", chain[0].Status)
	}
	if chain[1].Status != "active" {
		t.Errorf("v2 status=%q, want active", chain[1].Status)
	}
}

// Empty nodes map → nil slice (unknown memID).
func TestOrderVersionChain_EmptyNodes(t *testing.T) {
	chain := orderVersionChain(nil, "mem_x", 100)
	if chain != nil {
		t.Errorf("want nil for empty nodes, got %v", chain)
	}
}

// maxChainLen cap: a chain of 5 with cap=2 returns only what it can walk (no panic).
func TestOrderVersionChain_MaxLenCap(t *testing.T) {
	nodes := buildNodes([]struct{ id, sup, status string }{
		{"mem_v1", "", "archived"},
		{"mem_v2", "mem_v1", "archived"},
		{"mem_v3", "mem_v2", "archived"},
		{"mem_v4", "mem_v3", "archived"},
		{"mem_v5", "mem_v4", "active"},
	})
	// With maxChainLen=3, we won't see all 5 — the important thing is no panic.
	chain := orderVersionChain(nodes, "mem_v3", 3)
	if len(chain) == 0 {
		t.Errorf("chain should not be empty")
	}
	// Must not contain duplicates.
	seen := map[string]bool{}
	for _, r := range chain {
		if seen[r.ID] {
			t.Errorf("duplicate id %s in chain (cycle not handled)", r.ID)
		}
		seen[r.ID] = true
	}
}

// ─── aihub#253: the authorization scalars on a chain row ────────────────────

// TestOrderVersionChain_CarriesAuthorizationScalars pins the second hop of the
// aihub#253 contract.
//
// The /ui artifact side rail decides whether a caller may see each member of a
// supersede lineage from the MemoryVersionRef alone — hasProjectAccess on
// .Project, and the visibility rule on .Visibility/.AuthorUserID (see
// versionRefVisibleTo in internal/server/routes_artifacts.go). That replaced a
// per-version GetMemoryByID, so these three fields are now load-bearing for a
// permissions decision rather than decoration.
//
// If orderVersionChain stopped copying them out of versionNode, every ref would
// carry "" and the failure would NOT be loud: an admin caller still sees the
// whole lineage (hasProjectAccess short-circuits on the admin bit and ""
// visibility falls through the switch to "visible"), so the side rail looks
// perfectly healthy while every non-admin silently loses their entire version
// history. The server-side gates cannot catch it either — they stub
// versionChainFn and so never run this function. Hence this test.
func TestOrderVersionChain_CarriesAuthorizationScalars(t *testing.T) {
	nodes := map[string]versionNode{
		"mem_v1": {
			SupersedesID: "", Status: "archived", CreatedAt: "2026-01-01T00:00:00Z",
			Project: "proj_one", Visibility: "private", AuthorUserID: "u_first",
		},
		"mem_v2": {
			SupersedesID: "mem_v1", Status: "active", CreatedAt: "2026-02-02T00:00:00Z",
			Project: "proj_two", Visibility: "project", AuthorUserID: "u_second",
		},
	}
	chain := orderVersionChain(nodes, "mem_v1", 100)
	if len(chain) != 2 {
		t.Fatalf("want 2 entries, got %d", len(chain))
	}

	// Deliberately distinct per row AND per field: a single shared fixture value
	// would let a copy that reads the wrong node, or the wrong field of the right
	// node, still pass.
	for _, want := range []MemoryVersionRef{
		{ID: "mem_v1", Project: "proj_one", Visibility: "private", AuthorUserID: "u_first"},
		{ID: "mem_v2", Project: "proj_two", Visibility: "project", AuthorUserID: "u_second"},
	} {
		var got *MemoryVersionRef
		for i := range chain {
			if chain[i].ID == want.ID {
				got = &chain[i]
			}
		}
		if got == nil {
			t.Fatalf("%s missing from the chain", want.ID)
		}
		if got.Project != want.Project {
			t.Errorf("%s.Project = %q, want %q — the /ui side rail runs "+
				"hasProjectAccess on this field", want.ID, got.Project, want.Project)
		}
		if got.Visibility != want.Visibility {
			t.Errorf("%s.Visibility = %q, want %q — the /ui side rail decides this row's "+
				"visibility from this field, and the empty string is not a denial: it "+
				"falls through memoryVisibleTo's switch to \"visible\"",
				want.ID, got.Visibility, want.Visibility)
		}
		if got.AuthorUserID != want.AuthorUserID {
			t.Errorf("%s.AuthorUserID = %q, want %q — this is what makes a private "+
				"version visible to its own author and nobody else",
				want.ID, got.AuthorUserID, want.AuthorUserID)
		}
	}
}

// TestMemoryVersionChain_ProjectionAndScanOrderAgree pins the first hop of the
// aihub#253 contract: the correspondence between memoryVersionChainQuery's final
// SELECT list and the rows.Scan destination list in MemoryVersionChain.
//
// That correspondence is positional and unchecked by the compiler. Adding or
// removing a column on one side is caught loudly at runtime — pgx refuses a
// destination count that does not match the row description — but TRANSPOSING
// two same-typed entries on EITHER side is caught by nothing. All seven
// destinations are `string`, and two of the columns are `visibility` and
// `author_user_id`: swap them and every lineage row's visibility becomes a user
// id, which is not a case in memoryVisibleTo's switch and so falls through to
// "visible". The /ui side rail's per-version permission filter silently becomes
// a pass-through, with no error, no failing scan, and no other failing test.
//
// So both sides are read out of the source and compared as sequences. An
// earlier version of this test read only the SQL side and claimed to have
// "pinned the order"; it would not have noticed a transposition in the Scan.
//
// This does couple the test to the Scan variables' names. That is deliberate:
// keeping them in obvious correspondence with their columns is what makes the
// positional mapping reviewable at all, and a rename that breaks the
// correspondence should make somebody look at this mapping rather than pass
// silently.
func TestMemoryVersionChain_ProjectionAndScanOrderAgree(t *testing.T) {
	projection := chainQueryProjectionColumns(t)
	dests := chainScanDestinations(t)

	if len(projection) != len(dests) {
		t.Fatalf("memoryVersionChainQuery selects %d columns %v but MemoryVersionChain "+
			"scans into %d destinations %v. pgx would fail this at runtime; fix both "+
			"sides together.", len(projection), projection, len(dests), dests)
	}
	if len(projection) == 0 {
		t.Fatal("no columns found — the parse below is broken, so this gate is vacuous")
	}

	for i := range projection {
		col, dest := projection[i], dests[i]
		if normalizeIdent(col) != normalizeIdent(dest) {
			t.Fatalf("position %d of the chain query's projection is column %q but it is "+
				"scanned into %q.\n  projection: %v\n  scan dests: %v\n"+
				"These two lists are matched POSITIONALLY and nothing else checks them. "+
				"If this is a deliberate rename, rename both sides to correspond; if it "+
				"is a transposition, note that swapping visibility with author_user_id "+
				"turns the /ui side rail's permission filter into a pass-through.",
				i, col, dest, projection, dests)
		}
	}
}

// normalizeIdent maps a SQL column and its Go scan variable onto a common form,
// so author_user_id and authorUserID compare equal.
func normalizeIdent(s string) string {
	return strings.ToLower(strings.ReplaceAll(s, "_", ""))
}

// chainQueryProjectionColumns returns the column names of the FINAL SELECT in
// memoryVersionChainQuery, in order. (The CTEs above it each select a bare id.)
//
// Normalization is deliberately narrow: it unwraps the `m.` qualifier, a
// trailing ::text cast, an explicit alias, and a COALESCE(col, literal), which
// is everything the current query uses. Anything else it cannot name reliably
// fails the test rather than being guessed at.
func chainQueryProjectionColumns(t *testing.T) []string {
	t.Helper()
	idx := strings.LastIndex(memoryVersionChainQuery, "SELECT")
	if idx < 0 {
		t.Fatal("no SELECT in memoryVersionChainQuery")
	}
	rest := memoryVersionChainQuery[idx+len("SELECT"):]
	end := strings.Index(rest, "FROM")
	if end < 0 {
		t.Fatal("no FROM after the final SELECT in memoryVersionChainQuery")
	}
	projection := rest[:end]
	if strings.Contains(projection, "--") {
		t.Fatal("the chain query's projection contains a -- comment, which this parse " +
			"does not model; either drop the comment or teach this test about it")
	}

	// Split on commas at paren depth 0, so COALESCE(m.supersedes_id, '') stays one item.
	var items []string
	depth, start := 0, 0
	for i, r := range projection {
		switch r {
		case '(':
			depth++
		case ')':
			depth--
		case ',':
			if depth == 0 {
				items = append(items, projection[start:i])
				start = i + 1
			}
		}
	}
	items = append(items, projection[start:])

	out := make([]string, 0, len(items))
	for _, it := range items {
		it = strings.TrimSpace(it)
		if it == "" {
			continue
		}
		// An explicit alias is the column's name. Match " as " case-insensitively
		// on token boundaries rather than by index arithmetic over a case-folded
		// copy, whose offsets need not line up with the original.
		fields := strings.Fields(it)
		aliased := false
		for i := 0; i+1 < len(fields); i++ {
			if strings.EqualFold(fields[i], "AS") {
				out = append(out, fields[i+1])
				aliased = true
				break
			}
		}
		if aliased {
			continue
		}
		// Otherwise: strip the m. qualifier and a ::text cast off a bare column
		// reference, and reject anything more elaborate than that.
		bare := strings.TrimSuffix(it, "::text")
		if i := strings.LastIndex(bare, "."); i >= 0 {
			bare = bare[i+1:]
		}
		if bare == "" || strings.ContainsAny(bare, "()' \t\n") {
			t.Fatalf("cannot name the column selected by %q. This test matches the "+
				"projection against MemoryVersionChain's positional rows.Scan, so every "+
				"entry must be nameable; give it an explicit AS alias.", it)
		}
		out = append(out, bare)
	}
	return out
}

// chainScanDestinations returns the identifier names passed to rows.Scan inside
// MemoryVersionChain, in argument order — i.e. `&project` yields "project".
func chainScanDestinations(t *testing.T) []string {
	t.Helper()
	const fnName = "MemoryVersionChain"

	goFiles, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatalf("glob: %v", err)
	}
	fset := token.NewFileSet()
	var fn *ast.FuncDecl
	var found string
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
				if fn != nil {
					t.Fatalf("found %s in both %s and %s", fnName, found, f)
				}
				fn, found = fd, f
			}
		}
	}
	if fn == nil {
		t.Fatalf("could not find func %s in package domain; retarget this gate rather "+
			"than deleting it — the projection/Scan correspondence it checks is what "+
			"keeps a transposed visibility column from disabling the /ui side rail's "+
			"permission filter.", fnName)
	}

	var out []string
	var calls int
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != "Scan" {
			return true
		}
		calls++
		out = nil
		for _, arg := range call.Args {
			unary, ok := arg.(*ast.UnaryExpr)
			if !ok || unary.Op != token.AND {
				t.Fatalf("%s: rows.Scan argument %d is not a plain &ident, which this "+
					"gate cannot map to a column; keep the destinations as simple "+
					"address-of locals.", fnName, len(out))
			}
			id, ok := unary.X.(*ast.Ident)
			if !ok {
				t.Fatalf("%s: rows.Scan argument %d takes the address of something that "+
					"is not a plain identifier", fnName, len(out))
			}
			out = append(out, id.Name)
		}
		return true
	})
	if calls != 1 {
		t.Fatalf("%s contains %d Scan calls; this gate assumes exactly one and would "+
			"otherwise be pinning the wrong one", fnName, calls)
	}
	return out
}
