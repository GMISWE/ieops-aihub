package mcp

import (
	"encoding/json"
	"strings"
	"testing"
)

// aihub#313 — pf_recall brief mode.
//
// Measured premise (live against prod aihub, 2026-09-01, real tokenizer via
// /v1/messages/count_tokens): a no-top_k pf_recall returns 20 items = 6,967 tok,
// of which content is 60.4% and `related` 15.9%. With fields="brief" the same 20
// items cost 1,728 tok = 24.8%. The point of these tests is that the 75.2% cut is
// a PROJECTION and not a LOSS: the item count is unchanged, every id survives, and
// every shortened body is flagged with the true full length so pf_get_memory can
// complete it.

// briefFixture is one item as it reaches briefRecallItem: already through
// slimRecallResult's whitelist, content already cut to 800 runes by handleRecall,
// with the real length recorded. float64 on the numbers because that is what the
// REST client's json.Unmarshal into map[string]any produces — the code has to
// handle that type and nothing else.
func briefFixture() map[string]any {
	return map[string]any{
		"id":                 "mem_abc123",
		"type":               "rule.work",
		"similarity":         0.2891933706162386,
		"effective_strength": 2.991597364792209,
		"created_at":         "2026-05-21T20:48:26.806541Z",
		"work_item_id":       "wi_XYZ",
		"tags":               []any{"a", "b"},
		"related":            []any{map[string]any{"id": "mem_other", "summary": strings.Repeat("r", 200)}},
		"content":            "# Title line\n\n" + strings.Repeat("body ", 200),
		"content_truncated":  true,
		"content_full_len":   float64(3587),
	}
}

// TestBriefRecallItem_AlwaysCarriesID locks criterion 2 of aihub#313. A brief item
// without its id is not a smaller answer, it is an unreadable one: the whole design
// rests on the caller being able to escalate to pf_get_memory(id), so losing the id
// would have swapped silent truncation for silent LOSS. Includes the degenerate
// input where id is the only field present.
func TestBriefRecallItem_AlwaysCarriesID(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   map[string]any
	}{
		{"full item", briefFixture()},
		{"id only", map[string]any{"id": "mem_only"}},
		{"id plus untruncated content", map[string]any{"id": "mem_x", "content": "short"}},
		{"id with empty content", map[string]any{"id": "mem_y", "content": ""}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := briefRecallItem(tc.in)
			if got["id"] != tc.in["id"] {
				t.Fatalf("brief item lost its id: want %v, got %v (full item: %+v)",
					tc.in["id"], got["id"], got)
			}
		})
	}
}

// TestBriefRecallItem_ProjectsBodyAndKeepsRetrievalPath is the forward probe: the
// body is reduced to a capped first line, the heavy fields are gone, and the pair
// that makes the cut recoverable is present and TRUE.
func TestBriefRecallItem_ProjectsBodyAndKeepsRetrievalPath(t *testing.T) {
	got := briefRecallItem(briefFixture())

	if got["content"] != "# Title line" {
		t.Errorf("content should be the first line only, got %q", got["content"])
	}
	for _, k := range []string{"related", "tags", "work_item_id", "attrs", "commits"} {
		if _, ok := got[k]; ok {
			t.Errorf("brief item still carries %q — it is body-weight, not a pointer", k)
		}
	}
	for _, k := range []string{"id", "type", "similarity", "effective_strength", "created_at"} {
		if _, ok := got[k]; !ok {
			t.Errorf("brief item dropped %q, which callers rank and filter on", k)
		}
	}
	// The escape hatch (aihub#269), reused rather than reinvented.
	if got["content_truncated"] != true {
		t.Errorf("shortened body not flagged content_truncated=true: %+v", got)
	}
	// THE TRAP: handleRecall had already cut this item to 800 runes and recorded
	// 3587. Taking len() of the string we hold would report 800 and tell the model
	// its 12-character snippet was nearly the whole memory.
	if got["content_full_len"] != 3587 {
		t.Errorf("content_full_len must stay the TRUE full length (3587), got %v — "+
			"a brief item that under-reports its own length is worse than no flag at all",
			got["content_full_len"])
	}
}

// TestBriefRecallItem_CapsBodylessOfNewline covers the case that makes an uncapped
// "first line" rule a no-op: 37.5% of memories in the measured corpus contain no
// newline at all, and 44.2% of first lines exceed 200 chars. For those the first
// line IS the whole body, so without briefContentMax brief mode would have returned
// the full 800 runes while reporting itself as brief.
func TestBriefRecallItem_CapsBodylessOfNewline(t *testing.T) {
	long := strings.Repeat("单", 800) // no newline anywhere, 800 runes
	got := briefRecallItem(map[string]any{"id": "mem_nl", "content": long})

	gotRunes := len([]rune(got["content"].(string)))
	if gotRunes != briefContentMax {
		t.Fatalf("newline-free content must be cut to briefContentMax (%d runes), got %d — "+
			"this is the 37.5%% of the corpus where 'first line' alone saves nothing",
			briefContentMax, gotRunes)
	}
	// Cut on a rune boundary, not a byte boundary: these are 3-byte runes, so a
	// byte-slice would have produced invalid UTF-8.
	if !json.Valid(mustJSON(t, got)) {
		t.Error("brief content is not valid JSON — the cap sliced a multi-byte rune")
	}
	if got["content_full_len"] != 800 {
		t.Errorf("content_full_len should be the length we actually held (800), got %v",
			got["content_full_len"])
	}
}

// TestBriefRecallItem_UntruncatedStaysUnflagged: an item whose whole body already
// fits pays nothing for the mechanism, so the flags must be ABSENT rather than
// present-and-false. Both are omitempty downstream; a false value would still cost
// tokens on every short memory.
func TestBriefRecallItem_UntruncatedStaysUnflagged(t *testing.T) {
	got := briefRecallItem(map[string]any{"id": "mem_s", "content": "one short line"})
	if got["content"] != "one short line" {
		t.Errorf("a body that fits must survive verbatim, got %q", got["content"])
	}
	for _, k := range []string{"content_truncated", "content_full_len"} {
		if _, ok := got[k]; ok {
			t.Errorf("untruncated item should not carry %q at all, got %v", k, got[k])
		}
	}
}

// TestBriefRecallItem_TrimsNoiseWithoutLosingMeaning locks the two noise trims that
// carry brief mode from 28.4% to 24.8% of the full response. Rounding is to 3
// decimals (the consumers are an ordering and a ">= 0.3" threshold) and timestamps
// keep their DATE and time-of-day — only sub-second exhaust goes.
func TestBriefRecallItem_TrimsNoiseWithoutLosingMeaning(t *testing.T) {
	got := briefRecallItem(briefFixture())
	if got["similarity"] != 0.289 {
		t.Errorf("similarity should round to 3dp, got %v", got["similarity"])
	}
	if got["effective_strength"] != 2.992 {
		t.Errorf("effective_strength should round to 3dp, got %v", got["effective_strength"])
	}
	if got["created_at"] != "2026-05-21T20:48:26Z" {
		t.Errorf("created_at should keep date and time-of-day at second precision, got %v",
			got["created_at"])
	}
}

// TestTrimSubsecond_LeavesUnparseableStringsAlone: brief mode must never mangle a
// field it does not understand. A timestamp shape this function cannot recognise is
// worth more intact than "cleaned".
func TestTrimSubsecond_LeavesUnparseableStringsAlone(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"2026-05-21T20:48:26.806541Z", "2026-05-21T20:48:26Z"},
		{"2026-05-21T20:48:26.5+08:00", "2026-05-21T20:48:26+08:00"},
		{"2026-05-21T20:48:26Z", "2026-05-21T20:48:26Z"}, // nothing to trim
		{"", ""},
		{"not a timestamp", "not a timestamp"},
		{"1.x", "1.x"}, // a dot with no digit group is not a fractional second
		{"trailing.", "trailing."},
	} {
		if got := trimSubsecond(tc.in); got != tc.want {
			t.Errorf("trimSubsecond(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// TestSlimRecallResultMode_BriefPreservesItemCountAndShrinks is the whole-response
// contract, and it is the NEGATIVE CONTROL for this wi (criterion 3, the shape
// aihub#287 used in TestResumePath_MeasurementFailsOnPreChangeBuild).
//
// It fails on the pre-change build two ways, deliberately: slimRecallResultMode did
// not exist (build failure), AND if only the `if brief { out = briefRecallItem(out) }`
// branch is reverted so the package still compiles, the size assertion goes red
// because brief == full. Verified by mutation, not by assumption — reverting that
// one line reproduces:
//
//	brief mode did not shrink the response: full=NNNN bytes, brief=NNNN bytes
//
// The count assertion is the other half: pf_recall's value is recall BREADTH, so a
// "saving" that dropped items would be measuring the wrong thing. Lowering top_k is
// the fix this wi exists to reject.
func TestSlimRecallResultMode_BriefPreservesItemCountAndShrinks(t *testing.T) {
	items := make([]any, 0, 20)
	for i := 0; i < 20; i++ {
		it := briefFixture()
		it["id"] = "mem_" + string(rune('a'+i))
		items = append(items, it)
	}
	in := map[string]any{"items": items, "total": float64(140)}

	full := slimRecallResultMode(deepCopy(t, in), false)
	brief := slimRecallResultMode(deepCopy(t, in), true)

	fullItems := full["items"].([]any)
	briefItems := brief["items"].([]any)
	if len(briefItems) != len(fullItems) || len(briefItems) != 20 {
		t.Fatalf("brief mode must not change the item COUNT: full=%d brief=%d (want 20 both)",
			len(fullItems), len(briefItems))
	}
	if brief["total"] != float64(140) {
		t.Errorf("brief mode dropped `total` (aihub#249): %v", brief["total"])
	}

	fullB, briefB := mustJSON(t, full), mustJSON(t, brief)
	if len(briefB) >= len(fullB) {
		t.Fatalf("brief mode did not shrink the response: full=%d bytes, brief=%d bytes",
			len(fullB), len(briefB))
	}
	// Measured live at 24.8% of full tokens; 40% of BYTES is a loose ceiling that
	// still fails loudly if the projection stops being applied, without pinning the
	// test to one corpus's exact ratio.
	if ratio := float64(len(briefB)) / float64(len(fullB)); ratio > 0.40 {
		t.Errorf("brief response is %.1f%% of full by bytes, want <=40%% "+
			"(measured 24.8%% of TOKENS on a real 20-item recall)", ratio*100)
	}

	// Every id survives, in order — the reverse probe at response scale.
	for i, it := range briefItems {
		m := it.(map[string]any)
		want := fullItems[i].(map[string]any)["id"]
		if m["id"] != want {
			t.Errorf("item %d: brief id %v != full id %v (order or identity lost)", i, m["id"], want)
		}
	}
}

// TestSlimRecallResultMode_FullModeIsUnchanged guards every existing caller. Plugin
// skills call pf_recall without `fields` — pf-revise reads the FULL content of a
// methodology.spec item it fetched with top_k=1 — so full mode has to be
// byte-identical to what slimRecallResult produced before this wi.
func TestSlimRecallResultMode_FullModeIsUnchanged(t *testing.T) {
	in := map[string]any{"items": []any{briefFixture()}, "total": float64(1)}
	viaOldEntryPoint := mustJSON(t, slimRecallResult(deepCopy(t, in)))
	viaNewEntryPoint := mustJSON(t, slimRecallResultMode(deepCopy(t, in), false))
	if string(viaOldEntryPoint) != string(viaNewEntryPoint) {
		t.Fatalf("full mode diverged between entry points:\n old=%s\n new=%s",
			viaOldEntryPoint, viaNewEntryPoint)
	}
	var got map[string]any
	if err := json.Unmarshal(viaNewEntryPoint, &got); err != nil {
		t.Fatal(err)
	}
	item := got["items"].([]any)[0].(map[string]any)
	if !strings.Contains(item["content"].(string), "body body") {
		t.Error("full mode truncated the body — that is brief mode's job, not the default's")
	}
	if _, ok := item["related"]; !ok {
		t.Error("full mode dropped `related`, which only brief mode may do")
	}
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return b
}

// deepCopy round-trips through JSON so each mode gets its own maps. briefRecallItem
// builds a new map rather than mutating, but sharing fixtures between two calls is
// the kind of coupling that makes a later in-place optimisation look correct.
func deepCopy(t *testing.T, v map[string]any) map[string]any {
	t.Helper()
	var out map[string]any
	if err := json.Unmarshal(mustJSON(t, v), &out); err != nil {
		t.Fatalf("deepCopy: %v", err)
	}
	return out
}
