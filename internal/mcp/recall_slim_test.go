package mcp

import (
	"encoding/json"
	"reflect"
	"testing"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

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

// TestSlimRecallResult_CarriesTruncationFlags guards aihub#269. PR #245
// (aihub#244) truncates every recall item's content to 800 runes server-side and
// announces it with content_truncated / content_full_len, declaring
// GET /v1/memories/:id the escape hatch for the full text. Those two fields were
// never added to slimRecallResult's `keep` whitelist, so over MCP the agent got
// an 800-rune snippet with nothing marking it as a snippet — it would reason on
// 40% of a memory believing it had all of it, and had no full-length pointer to
// know a fetch was even warranted. Same failure class as
// TestSlimRecallResult_CarriesTotal above: the whitelist is opt-in, so any field
// added downstream is dropped by default.
func TestSlimRecallResult_CarriesTruncationFlags(t *testing.T) {
	result := map[string]any{
		"items": []any{
			map[string]any{
				"id":                "mem_trunc",
				"type":              "fact.architecture",
				"content":           "800 runes of prefix...",
				"content_truncated": true,
				"content_full_len":  float64(2194),
			},
		},
	}

	out := slimRecallResult(result)

	items, ok := out["items"].([]any)
	if !ok || len(items) != 1 {
		t.Fatalf("items not preserved: %+v", out)
	}
	item, ok := items[0].(map[string]any)
	if !ok {
		t.Fatalf("item is not a map: %+v", items[0])
	}
	if got, ok := item["content_truncated"]; !ok || got != true {
		t.Errorf("content_truncated = %v (present=%v), want true — the agent cannot "+
			"tell a snippet from a whole memory without it", got, ok)
	}
	if got, ok := item["content_full_len"]; !ok || got != float64(2194) {
		t.Errorf("content_full_len = %v (present=%v), want 2194 — without it there is "+
			"no pointer to GET /v1/memories/:id", got, ok)
	}
}

// TestSlimRecallResult_StillDropsBookkeeping locks in the other half of the
// aihub#269 contract: the fix is "add the two truncation fields", NOT "widen the
// projection". These columns are dropped on purpose (opt3 Phase 1) and are the
// bulk of the token saving; this test fails loudly if a future whitelist edit
// lets any of them back in. rendered_html matters most by volume — it is a full
// HTML document on methodology.* memories.
//
// Note what is NOT in this list: `attrs` and `commits` are not dropped, they are
// REWRITTEN (attrs down to structured_payload, commits down to body/by/replies)
// by the transform blocks in slimRecallResult. Asserting they vanish would encode
// a false invariant, so they get their own test below. Every field here is seeded
// with a realistic value, not a type-mismatched sentinel — a sentinel that fails
// an internal type assertion would make this test pass for the wrong reason.
func TestSlimRecallResult_StillDropsBookkeeping(t *testing.T) {
	dropped := map[string]any{
		"author_user_id":     "u_5dFjeaMZ",
		"author_display":     "xiaokang.w",
		"base_strength":      float64(0.9),
		"stability_days":     float64(10.5),
		"status":             "active",
		"visibility":         "project",
		"project":            "ieops",
		"updated_at":         "2026-08-27T05:02:27Z",
		"latest_id":          "mem_newer",
		"is_immortal":        false,
		"activation_count":   float64(3),
		"last_activated_at":  "2026-08-27T05:02:27Z",
		"expires_at":         "2027-01-01T00:00:00Z",
		"source_artifact_id": "mem_src",
		"emb_model":          "bge-m3",
		"emb_dims":           float64(1024),
		"rendered_html":      "<html><body>a very large rendered artifact</body></html>",
		"backlinks":          []any{map[string]any{"id": "mem_b", "type": "fact.note", "summary": "x"}},
	}
	item := map[string]any{
		"id": "mem_1", "type": "fact.note", "content": "hello",
		"content_truncated": true, "content_full_len": float64(900),
	}
	for k, v := range dropped {
		item[k] = v
	}

	out := slimRecallResult(map[string]any{"items": []any{item}})

	got := out["items"].([]any)[0].(map[string]any)
	for k := range dropped {
		if v, ok := got[k]; ok {
			t.Errorf("bookkeeping field %q leaked through the whitelist (=%v); the "+
				"aihub#269 fix must not widen the projection", k, v)
		}
	}
	// and the fields that must survive are still there
	for _, k := range []string{"id", "type", "content", "content_truncated", "content_full_len"} {
		if _, ok := got[k]; !ok {
			t.Errorf("kept field %q went missing", k)
		}
	}
}

// TestSlimRecallResult_RewritesAttrsAndCommits pins the two fields that are
// neither kept whole nor dropped, so nobody "simplifies" them into the keep map
// (which would drag the full attrs blob and whole comment threads back onto the
// wire) or into the drop list (which would lose real human insight).
func TestSlimRecallResult_RewritesAttrsAndCommits(t *testing.T) {
	item := map[string]any{
		"id": "mem_1", "type": "fact.note", "content": "hello",
		"attrs": map[string]any{
			"structured_payload": map[string]any{"result": "WARN"},
			"similar_to":         "mem_other", // internal bookkeeping, must not survive
		},
		"commits": []any{
			map[string]any{
				"id": "cm_1", "author_user_id": "u_1", "created_at": "2026-08-27T00:00:00Z",
				"body": "this assumption is wrong", "author_display": "dahe.p",
				"replies": []any{map[string]any{"id": "cm_2", "body": "agreed, fixed"}},
			},
		},
	}

	out := slimRecallResult(map[string]any{"items": []any{item}})
	got := out["items"].([]any)[0].(map[string]any)

	attrs, ok := got["attrs"].(map[string]any)
	if !ok {
		t.Fatalf("attrs missing or wrong shape: %#v", got["attrs"])
	}
	if _, ok := attrs["structured_payload"]; !ok {
		t.Errorf("attrs.structured_payload dropped: %#v", attrs)
	}
	if _, ok := attrs["similar_to"]; ok {
		t.Errorf("attrs passed through whole; only structured_payload should survive: %#v", attrs)
	}

	commits, ok := got["commits"].([]any)
	if !ok || len(commits) != 1 {
		t.Fatalf("commits missing or wrong shape: %#v", got["commits"])
	}
	note := commits[0].(map[string]any)
	if note["body"] != "this assumption is wrong" || note["by"] != "dahe.p" {
		t.Errorf("commit insight not preserved: %#v", note)
	}
	for _, k := range []string{"id", "author_user_id", "created_at", "replies_ids"} {
		if _, ok := note[k]; ok {
			t.Errorf("commit bookkeeping %q survived: %#v", k, note)
		}
	}
	reps, ok := note["replies"].([]any)
	if !ok || len(reps) != 1 || reps[0] != "agreed, fixed" {
		t.Errorf("reply bodies not flattened as expected: %#v", note["replies"])
	}
}

// TestSlimRecallResult_CarriesUnmatchedTypes guards aihub#289, and is the THIRD
// instance of the same trap the two tests above were written for: slimRecallResult
// builds a brand-new map, so a field added to RecallResponse reaches the REST
// response and dies here. `total` (aihub#249) and the truncation pair (aihub#269)
// were the first two.
//
// This one matters more than either, because unmatched_types exists ONLY to be read
// by the model, and this slimmer is the model's surface. Dropped here, the field
// would be a fix that never reaches the party it was written for: pf_recall would
// keep returning a bare empty set for a wrong type name, i.e. the exact silence
// aihub#289 exists to end, while the REST endpoint looked fixed.
func TestSlimRecallResult_CarriesUnmatchedTypes(t *testing.T) {
	result := map[string]any{
		"items":           []any{},
		"total":           float64(0),
		"unmatched_types": []any{"rule.work|fact.test"},
	}

	out := slimRecallResult(result)

	um, ok := out["unmatched_types"]
	if !ok {
		t.Fatalf("slimRecallResult dropped `unmatched_types` — the model never learns its type value was wrong: %+v", out)
	}
	got, ok := um.([]any)
	if !ok || len(got) != 1 || got[0] != "rule.work|fact.test" {
		t.Errorf("unmatched_types mangled: %#v", um)
	}
}

// ...and it must stay absent when the server said nothing, so the healthy call
// shapes pay no tokens for it. A slimmer that materialised an empty list here would
// spend budget on every recall to report "no problem".
func TestSlimRecallResult_OmitsAbsentUnmatchedTypes(t *testing.T) {
	for name, result := range map[string]map[string]any{
		"key absent": {"items": []any{}, "total": float64(3)},
		"key nil":    {"items": []any{}, "total": float64(3), "unmatched_types": nil},
	} {
		t.Run(name, func(t *testing.T) {
			out := slimRecallResult(result)
			if _, ok := out["unmatched_types"]; ok {
				t.Errorf("unmatched_types materialised when the server reported none: %+v", out)
			}
		})
	}
}

// TestRecallResult_SurvivesIntoCallToolResult closes hop 4 of the aihub#289 contract.
//
// The chain from a wrong type name to a model that knows about it is four hops:
// domain.UnmatchedTypes computes it, handleRecall assigns it, RecallResponse serialises
// it, and the MCP surface forwards it. Hops 1-3 are gated elsewhere (memory_unmatched_test
// and routes_memory_types_test). Hop 4 was NOT: slimRecallResult was tested, but nothing
// asserted the slimmed map actually reaches a CallToolResult intact — and that is the
// hop where the model finally reads it. Both the slimmer and jsonResultCompact are
// exercised here in the order pf_recall calls them, so a marshalling change that dropped
// or renamed the field cannot pass.
func TestRecallResult_SurvivesIntoCallToolResult(t *testing.T) {
	for _, tc := range []struct {
		name  string
		in    map[string]any
		want  map[string]any
		unset []string
	}{
		{
			name: "unmatched types reach the model",
			in: map[string]any{
				"items": []any{}, "total": float64(0),
				"unmatched_types": []any{"rule.work|fact.test"},
			},
			want:  map[string]any{"unmatched_types": []any{"rule.work|fact.test"}},
			unset: []string{"unmatched_types_error"},
		},
		{
			name: "a broken diagnostic reaches the model as an error, not as silence",
			in: map[string]any{
				"items": []any{}, "total": float64(0),
				"unmatched_types_error": "type diagnostic unavailable: boom",
			},
			want:  map[string]any{"unmatched_types_error": "type diagnostic unavailable: boom"},
			unset: []string{"unmatched_types"},
		},
		{
			name:  "a healthy recall carries neither",
			in:    map[string]any{"items": []any{}, "total": float64(2)},
			want:  map[string]any{"total": float64(2)},
			unset: []string{"unmatched_types", "unmatched_types_error"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			res, err := jsonResultCompact(slimRecallResult(tc.in))
			if err != nil {
				t.Fatalf("jsonResultCompact: %v", err)
			}
			if len(res.Content) != 1 {
				t.Fatalf("expected exactly one content block, got %d", len(res.Content))
			}
			text, ok := res.Content[0].(*sdkmcp.TextContent)
			if !ok {
				t.Fatalf("content is not TextContent: %T", res.Content[0])
			}

			var got map[string]any
			if err := json.Unmarshal([]byte(text.Text), &got); err != nil {
				t.Fatalf("tool result text is not JSON: %v\n%s", err, text.Text)
			}
			for k, want := range tc.want {
				g, present := got[k]
				if !present {
					t.Errorf("%q never reached the model: %s", k, text.Text)
					continue
				}
				if !reflect.DeepEqual(g, want) {
					t.Errorf("%q = %#v, want %#v", k, g, want)
				}
			}
			for _, k := range tc.unset {
				if _, present := got[k]; present {
					t.Errorf("%q materialised when the server reported none: %s", k, text.Text)
				}
			}
		})
	}
}
