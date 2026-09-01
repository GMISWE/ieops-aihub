package mcp_test

import (
	"net/http"
	"strings"
	"testing"
)

// aihub#313 — the WIRING hops for pf_recall's `fields` parameter.
//
// recall_brief_test.go covers the projection itself. It is not enough, and the
// mutation probe proved it: neutralising `if brief { out = briefRecallItem(out) }`
// left EVERY briefRecallItem unit test green, because they call the function
// directly and never travel the path the model travels. Only a whole-request test
// went red. So:
//
//	hop 1  MCP tool args -> handler          fields="brief" reaches strArg
//	hop 2  handler -> projection             slimRecallResultMode(result, true)
//	hop 3  projection -> tool output TEXT    assertions below are on the text
//	hop 4  (deliberately absent)             see TestRecallFieldsNeedsNoServerHop
//
// This is the aihub#282 hazard, stated precisely. #282 is about a parameter
// (`similarity_threshold`) that is published in pf_recall's InputSchema, is not in
// the handler's forwarding loop, and is not parsed by handleRecall either — while
// being fully implemented in domain. Live-confirmed on the pre-change build while
// writing this wi: passing 0.99 and passing nothing returned the same 20 items in
// the same order, min similarity 0.154.
//
// `fields` is immune to that failure by construction rather than by care, and the
// fake below is what proves it: it answers /v1/memories with ONE fixed payload and
// ignores the query string entirely. So a difference between the two calls cannot
// have come from the server. It can only have come from this process.
//
// Run: go test ./internal/mcp/ -run TestRecallFields -v   (no database needed)

const briefProbeBody = "# Headline that survives\n\nSecond line of the body that must NOT survive brief mode."

func serveRecall(f *fakeAihub, items []any) {
	f.on("/v1/memories", func(map[string]any) (int, any) {
		return http.StatusOK, map[string]any{"items": items, "total": len(items)}
	})
}

func recallProbeItems() []any {
	out := make([]any, 0, 3)
	for _, id := range []string{"mem_p1", "mem_p2", "mem_p3"} {
		out = append(out, map[string]any{
			"id": id, "type": "rule.work", "content": briefProbeBody,
			"similarity": 0.4556157860526919, "effective_strength": 2.998209067126785,
			"created_at": "2026-08-10T10:34:19.484466Z", "work_item_id": "wi_probe",
			"tags":    []any{"t"},
			"related": []any{map[string]any{"id": "mem_rel", "summary": strings.Repeat("x", 120)}},
		})
	}
	return out
}

// TestRecallFieldsBriefArrivesAndProjects is the POSITIVE probe, in the direction
// aihub#282 could not go: the same request, the same server payload, one argument
// different, and a different answer. If `fields` were dropped anywhere between the
// tool call and the projection, these two texts would be byte-identical — which is
// exactly the evidence #282 produced for similarity_threshold.
func TestRecallFieldsBriefArrivesAndProjects(t *testing.T) {
	base := map[string]any{"project": "aihub", "query": "token cost"}

	f1 := newFakeAihub(t)
	serveRecall(f1, recallProbeItems())
	fullText, fullObj := callToolText(t, f1, "pf_recall", base)

	f2 := newFakeAihub(t)
	serveRecall(f2, recallProbeItems())
	briefArgs := map[string]any{"project": "aihub", "query": "token cost", "fields": "brief"}
	briefText, briefObj := callToolText(t, f2, "pf_recall", briefArgs)

	if fullText == briefText {
		t.Fatalf("fields=\"brief\" changed nothing — the parameter never reached the projection. "+
			"This is aihub#282's exact signature (a schema-declared param that no hop consumes).\n text: %s", fullText)
	}

	// Hop 3 is asserted on the TEXT, not the decoded map: bytes in the transcript
	// are the quantity this wi exists to move.
	if !strings.Contains(fullText, "Second line of the body") {
		t.Error("full mode lost the body — then the brief assertion below proves nothing")
	}
	if strings.Contains(briefText, "Second line of the body") {
		t.Error("brief mode still carries the body past the first line")
	}
	if strings.Contains(briefText, "\"related\"") {
		t.Error("brief mode still carries `related` (15.9% of a real response)")
	}
	if !strings.Contains(briefText, "# Headline that survives") {
		t.Error("brief mode dropped the first line too — that is the one part it must keep")
	}
	if len(briefText) >= len(fullText) {
		t.Errorf("brief text (%d bytes) is not smaller than full (%d)", len(briefText), len(fullText))
	}

	// Breadth is the value of recall; the projection must not become a filter.
	fullItems := fullObj["items"].([]any)
	briefItems := briefObj["items"].([]any)
	if len(fullItems) != 3 || len(briefItems) != 3 {
		t.Fatalf("item count changed: full=%d brief=%d, want 3 and 3", len(fullItems), len(briefItems))
	}
	// Reverse probe at the wire: ids survive, and every shortened item says so and
	// says how long it really is, so pf_get_memory can complete it.
	for i, it := range briefItems {
		m := it.(map[string]any)
		if m["id"] != fullItems[i].(map[string]any)["id"] {
			t.Errorf("item %d: id lost or reordered (%v)", i, m["id"])
		}
		if m["content_truncated"] != true {
			t.Errorf("item %d: shortened body not flagged content_truncated", i)
		}
		if n, ok := m["content_full_len"].(float64); !ok || int(n) != len([]rune(briefProbeBody)) {
			t.Errorf("item %d: content_full_len = %v, want %d (the TRUE body length)",
				i, m["content_full_len"], len([]rune(briefProbeBody)))
		}
	}
}

// TestRecallFieldsUnknownValueIsFullNotAnError: `fields` is an opt-in, so anything
// that is not exactly "brief" must leave the response as it was. A typo silently
// shrinking a response would be a worse failure than the one this wi is fixing.
func TestRecallFieldsUnknownValueIsFullNotAnError(t *testing.T) {
	f := newFakeAihub(t)
	serveRecall(f, recallProbeItems())
	baseline, _ := callToolText(t, f, "pf_recall", map[string]any{"project": "aihub"})

	for _, v := range []string{"", "full", "Brief", "BRIEF", "brief ", "summary", "true"} {
		f2 := newFakeAihub(t)
		serveRecall(f2, recallProbeItems())
		got, _ := callToolText(t, f2, "pf_recall", map[string]any{"project": "aihub", "fields": v})
		if got != baseline {
			t.Errorf("fields=%q changed the response; only the exact value \"brief\" may project", v)
		}
	}
}

// TestRecallFieldsNeedsNoServerHop documents hop 4's deliberate absence, and is the
// reason this wi did not have to touch routes_memory.go (aihub#309's declared file)
// or domain. The fake ignores the query string and serves one fixed payload, so the
// only path by which `fields` can change the answer is inside this process — and
// the test above shows it does change the answer. One hop, consumed where it is
// read, with no wire contract to be silently dropped on.
//
// The recorder keys on r.URL.Path, so this asserts the request SHAPE is unchanged:
// brief mode must not turn one recall into two, or into a different endpoint.
func TestRecallFieldsNeedsNoServerHop(t *testing.T) {
	f := newFakeAihub(t)
	serveRecall(f, recallProbeItems())
	callToolText(t, f, "pf_recall", map[string]any{"project": "aihub", "fields": "brief"})

	got := f.paths()
	if len(got) != 1 || got[0] != "/v1/memories" {
		t.Fatalf("brief mode changed the upstream request shape: %v, want exactly [/v1/memories]", got)
	}
}
