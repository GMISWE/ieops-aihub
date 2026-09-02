package mcp

// aihub#314, projection half: the two MCP projections must both carry
// `request_adjusted` — the server's statement that it changed a value the caller
// sent — to the model.
//
// ─── Why these are a symmetric PAIR and not one test ────────────────────────
//
// slimRecallResult's existing lock is a pair on purpose:
// TestSlimRecallResult_StillDropsBookkeeping says the whitelist still drops, and
// TestSlimRecallResult_RewritesAttrsAndCommits says it still rewrites. A keep-only
// test would be satisfied by widening the whitelist wholesale, which would hand
// the model back every bookkeeping column this projection exists to remove — the
// benefit would vanish and nothing would go red.
//
// So `request_adjusted` gets both halves AT THE LEVEL IT LIVES ON, the top-level
// map, because that is where the change is:
//
//	CarriesRequestAdjusted             the field survives (fails if the
//	                                   conditional copy is removed)
//	StillDropsUnwhitelistedTopLevelKeys  everything unlisted still goes (fails if
//	                                   the copies are replaced by a blanket
//	                                   `for k, v := range result`)
//
// ─── The criterion is never "at least N fields" ─────────────────────────────
//
// Every assertion below names a specific parameter and demands the caller could
// see THAT parameter's adjustment. A floor on a derived quantity ("the response
// has >= 4 top-level keys") cannot catch a hole in the thing that produces the
// quantity: a projection that emitted four wrong keys, or a producer that
// recorded the wrong parameter, satisfies the floor exactly.

import (
	"reflect"
	"testing"
)

// recallWithAdjustment is a slimmable pf_recall REST body whose top_k was
// clamped, in the JSON shape a real response decodes to (numbers are float64
// through map[string]any).
func recallWithAdjustment() map[string]any {
	return map[string]any{
		"items": []any{
			map[string]any{"id": "mem_1", "type": "fact.note", "content": "hello", "similarity": 0.42},
		},
		"total": float64(220),
		"request_adjusted": []any{
			map[string]any{"param": "top_k", "requested": float64(500), "applied": float64(200)},
		},
	}
}

// wantTopKClamp asserts that out carries exactly the one adjustment
// recallWithAdjustment describes, by NAME and by both VALUES.
//
// Asserting all three fields matters: an entry that named the right parameter
// and reported `applied: 500` would tell the caller nothing was clamped while
// still being "present", and a presence check would pass.
func wantTopKClamp(t *testing.T, out map[string]any, where string) {
	t.Helper()
	raw, present := out["request_adjusted"]
	if !present {
		t.Fatalf("%s: request_adjusted was dropped — the caller asked for 500 memories, got 200, "+
			"and has no way to tell that from a corpus with 200 matches. Whole map: %+v", where, out)
	}
	entries, ok := raw.([]any)
	if !ok || len(entries) != 1 {
		t.Fatalf("%s: request_adjusted = %#v, want one entry", where, raw)
	}
	got, ok := entries[0].(map[string]any)
	if !ok {
		t.Fatalf("%s: entry is %T, want an object", where, entries[0])
	}
	want := map[string]any{"param": "top_k", "requested": float64(500), "applied": float64(200)}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("%s: request_adjusted[0] = %#v, want %#v", where, got, want)
	}
}

// TestSlimRecallResult_CarriesRequestAdjusted is the keep half, and it is the one
// that goes red if the conditional copy is deleted from slimRecallResultMode.
//
// Both modes are asserted because both are reachable from pf_recall: `fields=
// "brief"` (aihub#313) projects the ITEMS and must not touch the top level, so a
// brief caller who was clamped has to be told just the same as a full one.
func TestSlimRecallResult_CarriesRequestAdjusted(t *testing.T) {
	wantTopKClamp(t, slimRecallResult(recallWithAdjustment()), "full mode")
	wantTopKClamp(t, slimRecallResultMode(recallWithAdjustment(), true), "brief mode")
}

// TestSlimRecallResult_NoRequestAdjustedKeyDoesNotSynthesizeOne is the other side
// of the shape decision recorded in domain/request_adjusted.go: the server omits
// the key when nothing was adjusted, so this projection must not invent an empty
// one. An invented `request_adjusted: []` would be a claim ("we checked, nothing
// was changed") made by a process that cannot check.
func TestSlimRecallResult_NoRequestAdjustedKeyDoesNotSynthesizeOne(t *testing.T) {
	out := slimRecallResult(map[string]any{"items": []any{}, "total": float64(0)})
	if v, present := out["request_adjusted"]; present {
		t.Errorf("request_adjusted = %#v was synthesized from a response that had none", v)
	}
}

// TestSlimRecallResult_StillDropsUnwhitelistedTopLevelKeys is the drop half of
// the pair, at the top level where aihub#314's change lives.
//
// Without it, "make request_adjusted survive" has a trivially cheap wrong answer:
// copy every top-level key. That passes the keep test, and it would put back the
// bookkeeping this projection exists to remove the moment the REST response grows
// a heavy top-level field — silently, which is the failure mode the whole work
// item is about. The four keys below are the whole current whitelist plus one
// invented key that must NOT ride along.
func TestSlimRecallResult_StillDropsUnwhitelistedTopLevelKeys(t *testing.T) {
	result := recallWithAdjustment()
	result["next_cursor"] = "2026-09-01T00:00:00Z"
	result["unmatched_types"] = []any{"fact.nonesuch"}
	result["debug_query_plan"] = "a top-level field a future server adds and the model never reads"

	out := slimRecallResult(result)

	if _, present := out["debug_query_plan"]; present {
		t.Errorf("an unwhitelisted top-level key survived — the whitelist has been widened "+
			"wholesale, which is the edit that silently costs the whole projection: %+v", out)
	}
	for _, k := range []string{"items", "total", "next_cursor", "unmatched_types", "request_adjusted"} {
		if _, present := out[k]; !present {
			t.Errorf("%s was dropped; it is on the whitelist", k)
		}
	}
}

// TestSlimListWorkItems_KeepsRequestAdjusted is the aihub#267 half: the work-item
// list endpoint resets a `limit` over 200 to 50, and this is the projection that
// stands between that disclosure and the model.
//
// It needs no whitelist entry — slimListWorkItemsResult returns the SAME map, so
// top-level keys survive by construction — and that is exactly why it is asserted
// rather than assumed. The guarantee is a property of this function's shape, and
// the shape is one edit away from the aihub#249 rebuild that lost `total` from
// pf_recall. This test is what turns that edit red.
func TestSlimListWorkItems_KeepsRequestAdjusted(t *testing.T) {
	adjustment := []any{
		map[string]any{"param": "limit", "requested": float64(500), "applied": float64(50)},
	}
	result := map[string]any{
		"items":            []any{fullListItem()},
		"next_cursor":      "2026-08-30T04:29:53.487734Z",
		"request_adjusted": adjustment,
	}

	out := slimListWorkItemsResult(result)

	got, present := out["request_adjusted"]
	if !present {
		t.Fatalf("request_adjusted was dropped — a caller who asked for 500 work items and got "+
			"50 cannot tell that from a project with 50 work items (aihub#267): %+v", out)
	}
	if !reflect.DeepEqual(got, adjustment) {
		t.Errorf("request_adjusted = %#v, want %#v", got, adjustment)
	}
	// Anti-vacuity: without this the test would also pass against a projection
	// that does nothing at all, and "nothing at all" is not what is being locked.
	if _, present := out["items"].([]any)[0].(map[string]any)["content"]; present {
		t.Error("the item projection is not running, so this test is measuring an unprojected map")
	}
}
