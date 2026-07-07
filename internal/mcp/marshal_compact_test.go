package mcp

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestMarshalJSONCompact guards the aihub#212 change: marshalJSON is the single
// marshal point behind every jsonResult, and must emit COMPACT JSON. A regression
// to json.MarshalIndent would reinflate every MCP tool response by ~15-25%.
func TestMarshalJSONCompact(t *testing.T) {
	v := map[string]any{
		"a":      1,
		"nested": map[string]any{"b": []int{1, 2, 3}},
		"items":  []any{map[string]any{"k": "v"}, map[string]any{"k": "w"}},
	}
	b, err := marshalJSON(v)
	if err != nil {
		t.Fatalf("marshalJSON: %v", err)
	}
	// Compact output has no newlines (indentation). The size check below is the
	// other half of the revert guard; a substring space check would false-positive
	// on legitimate double spaces inside string values.
	if strings.Contains(string(b), "\n") {
		t.Errorf("marshalJSON output is not compact: %q", b)
	}
	// Must still be valid, round-trippable JSON.
	var out map[string]any
	if err := json.Unmarshal(b, &out); err != nil {
		t.Errorf("compact output is not valid JSON: %v", err)
	}
	// And strictly smaller than the old indented form.
	ind, _ := json.MarshalIndent(v, "", "  ")
	if len(b) >= len(ind) {
		t.Errorf("compact (%d bytes) not smaller than indented (%d bytes)", len(b), len(ind))
	}
}
