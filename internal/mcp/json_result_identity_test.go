package mcp

// aihub#591 — pf_get_memory's corrected line, pinned on its live half: jsonResult
// and jsonResultCompact produce BYTE-IDENTICAL text today, because 34df071 moved
// marshalJSON from json.MarshalIndent to json.Marshal for every tool in the
// server. The card sentence recording that correction was measured invisible to
// K12 (its only backtick anchors were a commit and helper names inside a
// historical clause), and its live half was held by nothing — a later change
// that reintroduced indentation in ONE of the two would silently split the
// server's tools into two output shapes again, which is exactly the difference
// the corrected sentence says does not exist.

import (
	"testing"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestJSONResultAndCompactAreByteIdentical(t *testing.T) {
	// Nested, key-rich, and holding the characters MarshalIndent would space out —
	// if either helper ever pretty-prints again, this fixture shows it.
	fixture := map[string]any{
		"items": []any{
			map[string]any{"id": "mem_x", "score": 0.5, "tags": []any{"a", "b"}},
			map[string]any{"id": "mem_y", "content": "line1\nline2"},
		},
		"total": 2,
		"nested": map[string]any{
			"deep": map[string]any{"deeper": []any{1, 2, 3}},
		},
	}

	a, err := jsonResult(fixture)
	if err != nil {
		t.Fatalf("jsonResult: %v", err)
	}
	b, err := jsonResultCompact(fixture)
	if err != nil {
		t.Fatalf("jsonResultCompact: %v", err)
	}

	textOf := func(r *sdkmcp.CallToolResult) string {
		if len(r.Content) != 1 {
			t.Fatalf("result carries %d content blocks, want 1", len(r.Content))
		}
		tc, ok := r.Content[0].(*sdkmcp.TextContent)
		if !ok {
			t.Fatalf("content is %T, want *TextContent", r.Content[0])
		}
		return tc.Text
	}

	got, want := textOf(a), textOf(b)
	if got != want {
		t.Errorf("jsonResult and jsonResultCompact no longer agree byte for byte.\n"+
			"jsonResult:        %q\njsonResultCompact: %q\n"+
			"The pf_get_memory card states the two are identical today (the pre-34df071 "+
			"difference is history) — if a pretty-printing path is being reintroduced, it "+
			"reintroduces the two-shape split for every tool that uses the helper you "+
			"touched, and the card sentence goes false with it.", got, want)
	}
}
