package mcp_test

// aihub#314, transport half: `request_adjusted` must reach the MODEL, not merely
// survive a projection function called directly.
//
// ─── Why this exists next to the pure tests in request_adjusted_test.go ─────
//
// aihub#309 measured the failure this guards against. Its mutation was applied to
// a pure function one layer away from the defect, and four pure tests stayed
// GREEN while the defect stood — a clamp in the HTTP handler that the domain-level
// tests could not see. So the assertion that decides whether this work item
// delivered anything has to be made where the model reads: the decoded text of a
// tools/call result, off the real MCP transport, from the tool as registered.
//
// The REST body is a fake here, which is deliberate and is exactly half the
// contract: these cases prove the MCP hop forwards whatever the server said.
// That the server SAYS it — that domain.ListWorkItems really records the aihub#267
// clamp and handleRecall really serves it — is the other half, and no fake can
// prove it. request_adjusted_e2e_db_test.go proves that against a real Postgres,
// a real router and this same transport. Two hops, two assertions.
//
//	go test ./internal/mcp/ -run TestRequestAdjusted -count=1 -v     (no database)

import (
	"net/http"
	"testing"
)

// adjustedRecallBody is a GET /v1/memories response for a caller who sent
// top_k=500 and was capped at 200.
func adjustedRecallBody() map[string]any {
	return map[string]any{
		"items": []any{
			map[string]any{
				"id": "mem_9Kd2", "type": "experience.pitfall",
				"content": "a memory body", "effective_strength": 1.4, "similarity": 0.31,
			},
		},
		"total": float64(220),
		"request_adjusted": []any{
			map[string]any{"param": "top_k", "requested": float64(500), "applied": float64(200)},
		},
	}
}

// oneAdjustment pulls the single expected entry out of a decoded tool result and
// checks its three fields. Presence alone is not the criterion: an entry naming
// the wrong parameter, or reporting applied == requested, is a disclosure that
// discloses nothing and would satisfy a presence check.
func oneAdjustment(t *testing.T, result map[string]any, param string, requested, applied float64) {
	t.Helper()
	raw, present := result["request_adjusted"]
	if !present {
		t.Fatalf("request_adjusted never reached the model. The server said it changed %s from "+
			"%v to %v and the MCP layer dropped the statement — the exact shape aihub#314 exists "+
			"to end. Result: %+v", param, requested, applied, result)
	}
	entries, ok := raw.([]any)
	if !ok || len(entries) != 1 {
		t.Fatalf("request_adjusted = %#v, want exactly one entry", raw)
	}
	entry, ok := entries[0].(map[string]any)
	if !ok {
		t.Fatalf("request_adjusted[0] is %T, want an object", entries[0])
	}
	if entry["param"] != param {
		t.Errorf("request_adjusted[0].param = %v, want %q", entry["param"], param)
	}
	if entry["requested"] != requested {
		t.Errorf("request_adjusted[0].requested = %v, want %v — the caller cannot check the "+
			"server's arithmetic without the value it says it received", entry["requested"], requested)
	}
	if entry["applied"] != applied {
		t.Errorf("request_adjusted[0].applied = %v, want %v", entry["applied"], applied)
	}
}

// TestRequestAdjustedReachesPfRecall drives pf_recall through the registered tool
// and the real in-memory MCP transport.
//
// top_k is sent as a JSON number, the shape a model actually writes. An earlier
// revision sent the string "500" and explained that a number was dropped in-process
// by strArg — that was true when written and is false since aihub#148 moved
// pf_recall's forwarding to scalarArg. The correction is recorded rather than
// quietly applied, because the previous note would otherwise keep teaching that the
// number form is unsupported.
//
// The fake answers this call from a canned body, so the spelling does not change
// what is asserted HERE; the case that genuinely depends on `top_k` surviving the
// hop is the e2e one, which drives both spellings.
func TestRequestAdjustedReachesPfRecall(t *testing.T) {
	f := newFakeAihub(t)
	f.on("/v1/memories", func(map[string]any) (int, any) {
		return http.StatusOK, adjustedRecallBody()
	})

	result, isErr := callTool(t, f, "pf_recall", map[string]any{
		"project": "aihub", "query": "silent adjust", "top_k": 500,
	})
	if isErr {
		t.Fatalf("pf_recall failed: %v", result)
	}
	oneAdjustment(t, result, "top_k", 500, 200)

	// Anti-vacuity: pf_recall's projection must still be RUNNING. Without this the
	// case would also pass against a tool that forwarded the REST body verbatim,
	// and then it would be measuring nothing about the whitelist it is here to
	// pin. `total` is on the whitelist and survives; the item's bookkeeping does
	// not (opt3 Phase 1).
	if result["total"] != float64(220) {
		t.Errorf("total = %v, want 220 — the whitelist's other top-level copies are gone too",
			result["total"])
	}
}

// TestRequestAdjustedReachesPfRecallBrief repeats it for fields="brief"
// (aihub#313), which is a per-ITEM projection: a caller who chose the small
// output is no less entitled to be told their page size was cut, and brief mode
// takes a different code path to the same top-level map.
func TestRequestAdjustedReachesPfRecallBrief(t *testing.T) {
	f := newFakeAihub(t)
	f.on("/v1/memories", func(map[string]any) (int, any) {
		return http.StatusOK, adjustedRecallBody()
	})

	result, isErr := callTool(t, f, "pf_recall", map[string]any{
		"project": "aihub", "query": "silent adjust", "top_k": "500", "fields": "brief",
	})
	if isErr {
		t.Fatalf("pf_recall failed: %v", result)
	}
	oneAdjustment(t, result, "top_k", 500, 200)
}

// TestListWorkItemsResponseCarriesRequestAdjusted is the aihub#267 case at the
// transport level: the work-item list endpoint bounds a limit over 200 (to 200,
// the ceiling — it used to reset to the default of 50), and the model must be
// able to see that it did.
//
// Named in the note in list_wi_slim.go, which argues that this field needs no
// whitelist entry there because that projection returns the SAME top-level map.
// This test is what makes the argument checkable — it goes red if the projection
// is ever rebuilt into the aihub#249 shape that lost `total` from pf_recall.
func TestListWorkItemsResponseCarriesRequestAdjusted(t *testing.T) {
	f := newFakeAihub(t)
	f.on("/v1/work_items", func(map[string]any) (int, any) {
		body := oneFullWorkItem()
		body["request_adjusted"] = []any{
			map[string]any{"param": "limit", "requested": float64(500), "applied": float64(200)},
		}
		return http.StatusOK, body
	})

	result, isErr := callTool(t, f, "pf_list_work_items", map[string]any{
		"project": "aihub", "limit": 500,
	})
	if isErr {
		t.Fatalf("pf_list_work_items failed: %v", result)
	}
	// 200, not 50: the fixture must be a response the real server can actually
	// produce, or this test pins the transport against a shape that no longer
	// exists anywhere upstream of it.
	oneAdjustment(t, result, "limit", 500, 200)

	// Anti-vacuity, same reason as above: the item projection must be running, or
	// this is a test about an unprojected map.
	item := result["items"].([]any)[0].(map[string]any)
	if _, present := item["milestone"]; present {
		t.Error("the null milestone survived — the projection is not running at all")
	}
}
