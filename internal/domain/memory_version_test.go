package domain

import (
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
