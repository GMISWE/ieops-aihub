package mcp_test

// aihub#314 against the REAL stack: Postgres, the real echo router, the real
// pkg/client, the real MCP handler, and the text a model would actually receive.
//
// ─── Why the fakes next door are not enough ─────────────────────────────────
//
// request_adjusted_wiring_test.go serves `request_adjusted` from a hand-written
// REST body, so it proves the MCP hop FORWARDS the field. It cannot prove the two
// things that hop is worth nothing without:
//
//  1. that the server EMITS it — that domain.ListWorkItems really records the
//     aihub#267 limit reset and domain.Recall really records the top_k cap. With a
//     fake on the other end, every assertion in that file stays green against a
//     server that discloses nothing at all. That is the reference side of a
//     differential test lying, and it is the failure the three recall_slim
//     regressions (total, the truncation pair, unmatched_types) all shared.
//
//  2. that the two ends AGREE. A contract with N hops needs N assertions:
//     asserting only the REST body reproduces the very bug aihub#314 exists to
//     fix, because pf_recall's whitelist is where the previous three fields died
//     — REST callers saw them, the model did not.
//
// So each case below asks the SAME live server twice: once over HTTP through
// pkg/client (hop 1, what a REST caller sees) and once through the MCP transport
// (hop 2, what the model sees), and demands the same disclosure from both.
//
// ─── Every case carries its own negative control ────────────────────────────
//
// A request whose parameter was NOT adjusted must come back with no
// `request_adjusted` key at all. Two reasons, and the second is the load-bearing
// one:
//
//   - it pins the shape decision recorded in domain/request_adjusted.go (absent,
//     not an empty list, when there is nothing to say), and
//   - without it the positive assertions are satisfied by a server that emits the
//     field unconditionally — which would make the field a constant, and a
//     constant discloses nothing. The criterion is "this parameter was adjusted
//     and the caller saw THAT", never "the response has a field".
//
// DB-gated in the AIHUB_TEST_DB style of internal/domain's integration tests, so
// a plain `go test ./...` skips it. The DB must already be migrated.
//
//	AIHUB_TEST_DB='postgres://postgres:test@127.0.0.1:5433/aihub_test?sslmode=disable' \
//	  go test ./internal/mcp/ -run TestE2ERequestAdjusted -count=1 -v
//
// Wired into ci.yml as "aihub#314 request_adjusted disclosure E2E DB tests"; the
// aihub#303 coverage gate fails the build if a DB-gated test is named by no step.

import (
	"context"
	"fmt"
	"net/url"
	"testing"
)

// ─── The anti-vacuity rule these cases obey, and why it is not optional ─────
//
// 🔴 FOUND BY MUTATION, not by review, and it is the reason every case below
// seeds a row first. The first draft of the recall case asserted against an EMPTY
// project, and it PASSED with the whitelist copy in recall_slim.go deleted — a
// mutant that should have reddened it. slimRecallResultMode opens with
//
//	items, ok := result["items"].([]any)
//	if !ok { return result }
//
// and domain leaves Items nil on an empty recall, which serialises as
// `"items": null`. So the projection short-circuited and handed back the RAW REST
// body, disclosure and all: the test was measuring the server twice and the MCP
// hop not at all. A no-rows fixture put the code under test out of the path —
// the mutant was applied correctly and landed on something nothing executed.
//
// Hence both halves of every case:
//
//	seed a row               so the projection actually runs, and
//	assertProjectionRan      so it is CHECKED to have run, by naming a field the
//	                         REST body carries and the projection removes.
//
// Without the second, the first would silently stop being true the day a fixture
// changed, and this whole file would go quiet in exactly the way it exists to
// prevent.

// assertProjectionRan proves the MCP projection was in the path, by requiring a
// field the REST response carries to be absent from the tool result.
func assertProjectionRan(t *testing.T, restBody, mcpResult map[string]any, dropped, where string) {
	t.Helper()
	restItems, _ := restBody["items"].([]any)
	mcpItems, _ := mcpResult["items"].([]any)
	if len(restItems) == 0 || len(mcpItems) == 0 {
		t.Fatalf("%s: REST returned %d items and the tool returned %d; with no rows the projection "+
			"short-circuits and this case measures nothing", where, len(restItems), len(mcpItems))
	}
	restItem, _ := restItems[0].(map[string]any)
	mcpItem, _ := mcpItems[0].(map[string]any)
	if _, present := restItem[dropped]; !present {
		t.Fatalf("%s: the REST item has no %q, so its absence downstream proves nothing — "+
			"the reference side of this comparison has changed", where, dropped)
	}
	if _, present := mcpItem[dropped]; present {
		t.Errorf("%s: %q survived into the tool result, so the projection did not run and the "+
			"disclosure assertions above are about an unprojected body", where, dropped)
	}
}

// adjustmentEntry finds the entry for param in a decoded response's
// request_adjusted list and returns it. where names the hop, so a failure says
// which side of the contract broke rather than just "not found".
func adjustmentEntry(t *testing.T, body map[string]any, param, where string) map[string]any {
	t.Helper()
	raw, present := body["request_adjusted"]
	if !present {
		t.Fatalf("%s: no request_adjusted at all, so the server changed %s and said nothing. "+
			"Response: %+v", where, param, body)
	}
	entries, ok := raw.([]any)
	if !ok {
		t.Fatalf("%s: request_adjusted is %T, want a list", where, raw)
	}
	for _, e := range entries {
		entry, ok := e.(map[string]any)
		if ok && entry["param"] == param {
			return entry
		}
	}
	t.Fatalf("%s: request_adjusted names no %q entry: %#v", where, param, entries)
	return nil
}

// wantAdjustment asserts that both halves of one entry are what the server
// actually did. `requested` is the half a presence check cannot reach: without it
// a caller cannot tell whether the server received the value it thinks it did,
// which is the difference between "you asked for 500, you got 50" and the
// aihub#148 case where the parameter never arrived at all.
func wantAdjustment(t *testing.T, body map[string]any, param string, requested, applied float64, where string) {
	t.Helper()
	entry := adjustmentEntry(t, body, param, where)
	if entry["requested"] != requested {
		t.Errorf("%s: %s.requested = %v, want %v", where, param, entry["requested"], requested)
	}
	if entry["applied"] != applied {
		t.Errorf("%s: %s.applied = %v, want %v", where, param, entry["applied"], applied)
	}
}

// wantNoAdjustment is the negative control described in the header.
func wantNoAdjustment(t *testing.T, body map[string]any, where string) {
	t.Helper()
	if v, present := body["request_adjusted"]; present {
		t.Errorf("%s: request_adjusted = %#v on a request nothing was done to. The field must be "+
			"absent when there is nothing to disclose, or it stops distinguishing the adjusted "+
			"call from the honoured one", where, v)
	}
}

// TestE2ERequestAdjustedDisclosesListLimitClamp is aihub#267, end to end.
//
// GET /v1/work_items resets a limit above 200 to 50 and has never said so, which
// makes "I sent 500" indistinguishable from "this project has 50 work items".
// The clamp is NOT changed here — it is disclosed.
func TestE2ERequestAdjustedDisclosesListLimitClamp(t *testing.T) {
	s := newE2EStack(t)
	ctx := context.Background()

	// One work item, so the item projection is in the path (see the anti-vacuity
	// note above). newE2EStack has just emptied this project.
	if _, created := s.call(t, "pf_create_work_item", map[string]any{
		"project": s.project,
		"goal":    "a row for the aihub#314 limit-clamp disclosure to be measured against",
	}); created["id"] == nil {
		t.Fatalf("seed work item: %+v", created)
	}

	// ── hop 1: what a REST caller sees ──────────────────────────────────────
	rest, err := s.client.ListWorkItems(ctx, url.Values{
		"project": {s.project}, "limit": {"500"},
	})
	if err != nil {
		t.Fatalf("REST list work_items: %v", err)
	}
	wantAdjustment(t, rest, "limit", 500, 50, "REST GET /v1/work_items?limit=500")

	// ── hop 2: what the model sees, same server ─────────────────────────────
	_, mcpResult := s.call(t, "pf_list_work_items", map[string]any{
		"project": s.project, "limit": 500,
	})
	wantAdjustment(t, mcpResult, "limit", 500, 50, "pf_list_work_items(limit=500)")
	// `content` is null on every row this endpoint serves and the projection
	// deletes it (aihub#278), so its absence is the proof the projection ran.
	assertProjectionRan(t, rest, mcpResult, "content", "pf_list_work_items(limit=500)")

	// ── negative control, both hops ─────────────────────────────────────────
	restOK, err := s.client.ListWorkItems(ctx, url.Values{
		"project": {s.project}, "limit": {"10"},
	})
	if err != nil {
		t.Fatalf("REST list work_items (control): %v", err)
	}
	wantNoAdjustment(t, restOK, "REST GET /v1/work_items?limit=10")
	_, mcpOK := s.call(t, "pf_list_work_items", map[string]any{
		"project": s.project, "limit": 10,
	})
	wantNoAdjustment(t, mcpOK, "pf_list_work_items(limit=10)")
}

// TestE2ERequestAdjustedDisclosesRecallTopKCap is the recall path's remaining
// bound: domain.normalizeRecallTopK caps top_k at 200.
//
// aihub#309 removed a SECOND cap that sat in handleRecall and deliberately added
// no disclosure field, writing down the reason — a dedicated field would not have
// survived slimRecallResult's opt-in whitelist, so it would have reached REST
// callers and missed the pf_recall callers who are the affected population. The
// two hops below are that argument turned into a test.
//
// 🔴 BOTH argument spellings are driven through the MCP hop, and the reason is a
// correction rather than thoroughness. An earlier revision sent only the string
// "500" and said a numeric literal "would make the MCP half of this test assert
// nothing" — true when it was written, because tools_memory.go read `top_k` with
// strArg and dropped a non-string, and false since aihub#148 moved it to
// scalarArg (recallStringParams). A comment asserting a hop is broken, left
// standing after that hop is fixed, is how the next reader concludes the number
// form is unsupported and writes another string.
//
// So the loop below asserts the disclosure for `500` AND for `"500"`. The number
// is the shape a model actually writes ("max results: 500"); the string is the
// shape that has always travelled. Keeping both means a regression of aihub#148's
// scalarArg fix reddens here too, and reddens with a message that says WHICH
// spelling stopped arriving — which is a different fault from the disclosure
// itself disappearing, and worth being able to tell apart.
func TestE2ERequestAdjustedDisclosesRecallTopKCap(t *testing.T) {
	s := newE2EStack(t)
	ctx := context.Background()

	// 🔴 Seed a memory FIRST. An empty recall serialises `"items": null`, which
	// makes slimRecallResultMode return the REST body untouched — so an empty
	// project takes the whitelist out of the path entirely and this case would
	// pass with the aihub#314 copy deleted. Measured; see the note at the top.
	if _, err := s.pool.Exec(ctx, `DELETE FROM memories WHERE project=$1`, s.project); err != nil {
		t.Fatalf("clean project memories: %v", err)
	}
	if _, err := s.client.Remember(ctx, map[string]any{
		"project":    s.project,
		"type":       "experience.pitfall",
		"content":    "a memory that exists so the pf_recall projection is actually exercised",
		"visibility": "project",
	}); err != nil {
		t.Fatalf("seed memory: %v", err)
	}

	rest, err := s.client.Recall(ctx, url.Values{
		"project": {s.project}, "top_k": {"500"},
	})
	if err != nil {
		t.Fatalf("REST recall: %v", err)
	}
	wantAdjustment(t, rest, "top_k", 500, 200, "REST GET /v1/memories?top_k=500")

	// 500.0 rather than 500: JSON numbers arrive as float64 through the MCP
	// transport, so this is the value a real tools/call carries, not a Go int
	// that would take a different path through scalarArg.
	for _, shape := range []any{500.0, "500"} {
		where := fmt.Sprintf("pf_recall(top_k=%#v)", shape)
		_, mcpResult := s.call(t, "pf_recall", map[string]any{
			"project": s.project, "top_k": shape,
		})
		wantAdjustment(t, mcpResult, "top_k", 500, 200, where)
		// author_user_id is on every REST memory and on none of the whitelist, so
		// its absence is the proof the opt3 Phase 1 projection ran.
		assertProjectionRan(t, rest, mcpResult, "author_user_id", where)
	}

	// A page size inside the bound is honoured, so nothing is disclosed. Driven
	// with the number spelling to match the positive case above. Note what this
	// control can and cannot do: it proves the field is not emitted
	// unconditionally, and it CANNOT distinguish "top_k arrived and was honoured"
	// from "top_k never arrived" — a dropped parameter satisfies it. Detecting
	// that is the positive loop's job, which is why the loop drives both
	// spellings rather than trusting this one.
	restOK, err := s.client.Recall(ctx, url.Values{
		"project": {s.project}, "top_k": {"5"},
	})
	if err != nil {
		t.Fatalf("REST recall (control): %v", err)
	}
	wantNoAdjustment(t, restOK, "REST GET /v1/memories?top_k=5")
	_, mcpOK := s.call(t, "pf_recall", map[string]any{
		"project": s.project, "top_k": 5.0,
	})
	wantNoAdjustment(t, mcpOK, "pf_recall(top_k=5)")
}
