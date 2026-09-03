package domain

import (
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
			t.Errorf("%s.Visibility = %q, want %q — the /ui side rail decides this "+
				"row's visibility from this field, and %q is the value that means "+
				"\"visible to everyone\"", want.ID, got.Visibility, want.Visibility, "")
		}
		if got.AuthorUserID != want.AuthorUserID {
			t.Errorf("%s.AuthorUserID = %q, want %q — this is what makes a private "+
				"version visible to its own author and nobody else",
				want.ID, got.AuthorUserID, want.AuthorUserID)
		}
	}
}

// TestMemoryVersionChainQuery_ProjectionOrderIsPinned pins the first hop: the
// final SELECT list of memoryVersionChainQuery is consumed by a POSITIONAL
// rows.Scan in MemoryVersionChain.
//
// Adding or removing a column is caught loudly at runtime — pgx refuses a
// destination count that does not match the row description. Transposing two
// columns of the same type is not caught by anything, and two of these columns
// are `visibility` and `author_user_id`: swap them and every lineage row's
// visibility becomes a user id, which is not one of the values the visibility
// switch handles and therefore falls through to "visible". That turns a
// permissions filter into a pass-through with no error, no failing scan and no
// other failing test.
//
// So this asserts the ORDER, normalized past the `m.` qualifiers and the
// COALESCE/cast wrappers that are cosmetic here. It is deliberately a
// projection-order assertion and not a whole-query string match: reformatting
// the CTE must not redden it, transposing the projection must.
func TestMemoryVersionChainQuery_ProjectionOrderIsPinned(t *testing.T) {
	// Must stay in step with the rows.Scan destination order in
	// MemoryVersionChain. Changing one without the other is the bug this catches.
	want := []string{"id", "supersedes_id", "status", "created_at", "project", "visibility", "author_user_id"}

	// The projection under test is the LAST SELECT in the statement (the CTEs
	// above it each select a single bare id).
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

	// Split on commas at paren-depth 0, so COALESCE(m.supersedes_id, '') stays one
	// item instead of becoming two.
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

	got := make([]string, 0, len(items))
	for _, it := range items {
		it = strings.TrimSpace(it)
		if it == "" {
			continue
		}
		// An explicit alias wins — it is what the column is called.
		if i := strings.LastIndex(strings.ToUpper(it), " AS "); i >= 0 {
			got = append(got, strings.TrimSpace(it[i+4:]))
			continue
		}
		// Otherwise take the last bare identifier: strips the `m.` qualifier and
		// any ::text cast, and for a bare COALESCE(x, '') names the column x.
		it = strings.NewReplacer("(", " ", ")", " ", ",", " ").Replace(it)
		fields := strings.Fields(it)
		last := ""
		for _, f := range fields {
			f = strings.TrimSuffix(f, "::text")
			if f == "" || strings.HasPrefix(f, "'") {
				continue // a literal, e.g. COALESCE's '' default
			}
			if i := strings.LastIndex(f, "."); i >= 0 {
				f = f[i+1:]
			}
			last = f
		}
		got = append(got, last)
	}

	if len(got) != len(want) {
		t.Fatalf("projection has %d columns %v, want %d %v", len(got), got, len(want), want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("memoryVersionChainQuery projection position %d is %q, want %q.\n"+
				"got:  %v\nwant: %v\n"+
				"This list is scanned positionally in MemoryVersionChain. If you meant "+
				"to change the projection, change the rows.Scan destination order to "+
				"match and update this test — do not update only one of the three.",
				i, got[i], want[i], got, want)
		}
	}
}
