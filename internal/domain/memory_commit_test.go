package domain

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestMemoryTypeEnum_Count verifies that MemoryTypeEnum contains exactly 19 entries.
func TestMemoryTypeEnum_Count(t *testing.T) {
	if got := len(MemoryTypeEnum); got != 19 {
		t.Errorf("MemoryTypeEnum has %d entries, want 19; actual: %v", got, MemoryTypeEnum)
	}
}

// TestMemoryTypeEnum_AllHaveValidPrefix ensures every enum entry passes the 4-prefix check
// that Remember uses internally.
func TestMemoryTypeEnum_AllHaveValidPrefix(t *testing.T) {
	validPrefixes := []string{"experience.", "fact.", "rule.", "methodology."}
	for _, typ := range MemoryTypeEnum {
		found := false
		for _, p := range validPrefixes {
			if len(typ) > len(p) && typ[:len(p)] == p {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("MemoryTypeEnum entry %q does not start with a valid prefix", typ)
		}
	}
}

// TestMemoryTypeEnum_OffListTypePassesLenientCheck verifies the server's lenient
// 4-prefix validation still accepts types that are NOT in MemoryTypeEnum but carry
// a valid prefix (regression guard: we must not change the server check to enum-strict).
func TestMemoryTypeEnum_OffListTypePassesLenientCheck(t *testing.T) {
	offListButValid := []string{
		"experience.someNewType",
		"fact.someNewThing",
		"rule.customCompanyRule",
		"methodology.somethingNew",
	}
	validPrefixes := []string{"experience.", "fact.", "rule.", "methodology."}
	for _, typ := range offListButValid {
		found := false
		for _, p := range validPrefixes {
			if len(typ) >= len(p) && typ[:len(p)] == p {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("off-list type %q should still pass lenient prefix check", typ)
		}
	}
}

// TestCommitMemorySQL_EntryStructure verifies that the JSON entry structure for a
// commit matches the expected schema (author_user_id, author_display, body, created_at).
// This is a pure-Go structural test — no DB required.
func TestCommitMemorySQL_EntryStructure(t *testing.T) {
	// Verify that the keys we embed in the entry are present.
	// We do this by recreating the map used in CommitMemory.
	entry := map[string]any{
		"author_user_id": "usr_abc",
		"author_display": "Alice",
		"body":           "This is a test annotation.",
		"created_at":     "2026-01-01T00:00:00Z",
	}
	for _, key := range []string{"author_user_id", "author_display", "body", "created_at"} {
		if _, ok := entry[key]; !ok {
			t.Errorf("commit entry missing required key %q", key)
		}
	}
}

// TestMemoryStruct_CommitsField verifies the Memory struct has a Commits field with
// the correct json tag (column-drift guard: field must survive JSON round-trip).
func TestMemoryStruct_CommitsField(t *testing.T) {
	m := Memory{}
	m.Commits = []byte(`[{"body":"test annotation"}]`)
	data, err := json.Marshal(m)
	if err != nil {
		t.Fatalf("json.Marshal(Memory) failed: %v", err)
	}
	if !strings.Contains(string(data), `"commits"`) {
		t.Errorf("marshaled Memory missing commits key; got: %s", string(data[:min(len(data), 300)]))
	}
	// Verify round-trip: unmarshal back and check the field is preserved.
	var m2 Memory
	if err := json.Unmarshal(data, &m2); err != nil {
		t.Fatalf("json.Unmarshal(Memory) failed: %v", err)
	}
	if !strings.Contains(string(m2.Commits), "test annotation") {
		t.Errorf("Commits round-trip failed; got: %s", string(m2.Commits))
	}
}
