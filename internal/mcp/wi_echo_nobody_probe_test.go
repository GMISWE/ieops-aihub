package mcp

// aihub#591 — pf_update_work_item's no-body disambiguation sentence, pinned: "A
// work item with no body comes back as `content: null` with no `content_len`, so
// a missing `content_len` means 'this wi has no body', never 'the body was
// withheld'."
//
// TestBriefDropsTheBodyWhereTheEqualityGateWouldKeepIt exercises the WITH-body
// halves, and its M24 note records that a no-body fake reddens its control — so
// the bodyless answer itself was held by nothing. It matters because the
// sentence is a DISAMBIGUATION rule a caller executes: if a suppressed body ever
// stopped reporting content_len, or a bodyless one started reporting 0, the two
// states collapse and "missing content_len" stops meaning anything — the
// aihub#269 ambiguity, relocated.
import "testing"

func TestDropContentEchoLeavesABodylessNullAloneAndReportsNoLength(t *testing.T) {
	for _, tc := range []struct {
		name   string
		result map[string]any
	}{
		{"JSON null body", map[string]any{"id": "wi_x", "content": nil}},
		{"content key absent entirely", map[string]any{"id": "wi_x"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if dropContentEcho(tc.result) {
				t.Errorf("dropContentEcho claimed it suppressed a body that does not exist")
			}
			if _, has := tc.result["content_len"]; has {
				t.Errorf("a bodyless work item came back with content_len=%v. The card "+
					"publishes 'content: null with NO content_len' — a zero here claims a "+
					"stored empty string where the record holds NULL, and the caller-side "+
					"rule 'missing content_len means no body' collapses.", tc.result["content_len"])
			}
			if v, has := tc.result["content"]; has && v != nil {
				t.Errorf("content mutated to %v", v)
			}
		})
	}

	// Control: a real body still converts to content_len, or this arm could be
	// satisfied by a dropContentEcho that does nothing at all.
	withBody := map[string]any{"id": "wi_x", "content": "body"}
	if !dropContentEcho(withBody) || withBody["content_len"] != 4 {
		t.Fatalf("the with-body control failed: %v — a no-op suppressor satisfies every "+
			"bodyless assertion above", withBody)
	}
}
