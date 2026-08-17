package mcp

import "testing"

// TestSlimRecallResult_CarriesTotal guards aihub#249: slimRecallResult builds a
// brand-new result map (`res := map[string]any{"items": slim}`) rather than
// filtering the incoming one, so any top-level key added to RecallResponse is
// silently dropped unless explicitly copied over here — exactly what happened
// when `total` was added to the REST response but not to this slimmer. Without
// this, pf_recall (the MCP surface agents actually call) would still have no
// way to tell "that's everything" from "keep paging", even after the REST fix.
func TestSlimRecallResult_CarriesTotal(t *testing.T) {
	result := map[string]any{
		"items": []any{
			map[string]any{"id": "mem_1", "type": "fact.note", "content": "hello"},
		},
		"next_cursor": "2026-08-17T00:00:00Z",
		"total":       float64(7), // json.Unmarshal into map[string]any yields float64
	}

	out := slimRecallResult(result)

	total, ok := out["total"]
	if !ok {
		t.Fatalf("slimRecallResult dropped `total`: %+v", out)
	}
	if total != float64(7) {
		t.Errorf("total = %v, want 7", total)
	}
	if nc, ok := out["next_cursor"]; !ok || nc != "2026-08-17T00:00:00Z" {
		t.Errorf("next_cursor not preserved: %+v", out)
	}
}

// TestSlimRecallResult_OmitsZeroTotalIsStillPresent verifies total=0 (a
// legitimately empty result set) is NOT treated as "absent" and dropped — only
// a genuinely missing/nil key should be omitted, matching next_cursor's own
// `!= nil` guard.
func TestSlimRecallResult_ZeroTotalStillCopied(t *testing.T) {
	result := map[string]any{
		"items": []any{},
		"total": float64(0),
	}
	out := slimRecallResult(result)
	total, ok := out["total"]
	if !ok {
		t.Fatalf("slimRecallResult dropped a zero (but present) `total`: %+v", out)
	}
	if total != float64(0) {
		t.Errorf("total = %v, want 0", total)
	}
}

// TestSlimRecallResult_NoTotalKeyOmitsIt verifies a result map with no `total`
// key at all (e.g. an older server response) doesn't synthesize one.
func TestSlimRecallResult_NoTotalKeyOmitsIt(t *testing.T) {
	result := map[string]any{
		"items": []any{},
	}
	out := slimRecallResult(result)
	if _, ok := out["total"]; ok {
		t.Errorf("slimRecallResult synthesized a `total` key that wasn't in the input: %+v", out)
	}
}
