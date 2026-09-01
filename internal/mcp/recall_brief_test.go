package mcp

import (
	"encoding/json"
	"math"
	"strings"
	"testing"
	"unicode/utf8"
)

// aihub#313 — pf_recall brief mode.
//
// Measured premise: see the CANONICAL MEASUREMENT block on slimRecallResultMode in
// recall_slim.go — 20 items, 6,966 tok full -> 1,766 tok brief = 25.4%, still 20
// items. Deliberately NOT restated here with its own numbers: an earlier draft of
// this file quoted 6,967/1,728/24.8% from a different call of the same query, and
// two comments disagreeing about one statistic is how a measured table rots.
//
// What these tests exist to prove is that the 74.6% cut is a PROJECTION and not a
// LOSS: the item count is unchanged, every id survives, and every shortened body is
// flagged with its true full length so pf_get_memory can complete it.

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
// "first line" rule a no-op: 37.5% of items across 659 recalls contain no newline at
// all (45% in the smaller live 20-item sample). For those the first line IS the
// whole body, so without briefContentMax brief mode would return the full 800 runes
// while reporting itself as brief.
func TestBriefRecallItem_CapsBodylessOfNewline(t *testing.T) {
	long := strings.Repeat("单", 800) // no newline anywhere, 800 runes
	got := briefRecallItem(map[string]any{"id": "mem_nl", "content": long})

	gotContent := got["content"].(string)
	gotRunes := len([]rune(gotContent))
	if gotRunes != briefContentMax {
		t.Fatalf("newline-free content must be cut to briefContentMax (%d runes), got %d — "+
			"this is the 37.5%% of the corpus where 'first line' alone saves nothing",
			briefContentMax, gotRunes)
	}
	// Cut on a rune boundary, not a byte boundary: these are 3-byte runes, so a
	// byte-slice would produce invalid UTF-8.
	//
	// utf8.ValidString, NOT json.Valid(json.Marshal(...)). The first draft used the
	// latter and review proved it tautological: encoding/json silently replaces
	// invalid UTF-8 with U+FFFD, so json.Valid(json.Marshal(x)) is true for EVERY x.
	// It read like a guard and could not fail.
	if !utf8.ValidString(gotContent) {
		t.Errorf("brief content is not valid UTF-8 — the cap sliced a multi-byte rune: %q", gotContent)
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
// are worth ~3.5 points of the 25.4% result. Rounding is to 4 decimals, not 3,
// because pf-retro gates on similarity > 0.85; timestamps keep their DATE and
// time-of-day, and only sub-second exhaust goes.
func TestBriefRecallItem_TrimsNoiseWithoutLosingMeaning(t *testing.T) {
	got := briefRecallItem(briefFixture())
	// 0.2891933706162386 -> 0.2892, not 0.289: see briefRoundDigits for why the cut
	// is at 4 decimals (pf-retro branches on similarity > 0.85 / > 0.65, so a 5e-4
	// shift from 3dp rounding can flip a reinforce into a duplicate).
	if got["similarity"] != 0.2892 {
		t.Errorf("similarity should round to 4dp, got %v", got["similarity"])
	}
	if got["effective_strength"] != 2.9916 {
		t.Errorf("effective_strength should round to 4dp, got %v", got["effective_strength"])
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
		{"2026-05-21T20:48:26.000000123-05:00", "2026-05-21T20:48:26-05:00"},
		{"2026-05-21T20:48:26.5z", "2026-05-21T20:48:26z"}, // lowercase zone
		{"2026-05-21T20:48:26Z", "2026-05-21T20:48:26Z"},   // nothing to trim
		{"", ""},
		{"not a timestamp", "not a timestamp"},
		{"1.x", "1.x"}, // a dot with no digit group is not a fractional second
		{"trailing.", "trailing."},
		// The counterexamples a review found against the first draft, which only
		// looked for "a dot followed by digits" while its doc claimed non-timestamps
		// were safe. It turned "v1.2.3" into "v1.3".
		{"v1.2.3", "v1.2.3"},
		{"0.5.1", "0.5.1"},
		{"3.14 is pi", "3.14 is pi"},
		{"12.5", "12.5"},     // digits either side, but no zone and no seconds field
		{"a12.5Z", "a12.5Z"}, // looks close, but "a12" is not a time
		{".5Z", ".5Z"},       // dot at index 0 — no seconds field to trim from
		{"2026-05-21", "2026-05-21"},
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
	// Measured live at 25.4% of full TOKENS and 31.1% of full BYTES. The 40% byte
	// ceiling is deliberately loose — it fails loudly if the projection stops being
	// applied at all, without pinning this test to one corpus's exact ratio (which
	// would make it a fixture check rather than a behaviour check). Partial
	// regressions are caught by the field-level assertions, not by this number.
	if ratio := float64(len(briefB)) / float64(len(fullB)); ratio > 0.40 {
		t.Errorf("brief response is %.1f%% of full by bytes, want <=40%% "+
			"(measured 25.4%% of TOKENS on a real 20-item recall)", ratio*100)
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

// TestBriefRecallItem_BodylessItemGainsNoContentKey: an item that arrived with no
// `content` key at all must not leave with `content: ""`. Synthesising an empty
// string would ADD bytes to the response this projection exists to shrink, and it
// would let a stray content_full_len flag an empty body as truncated. The id
// invariant still has to hold on that path — the fix is a guarded assignment
// rather than an early return precisely so the id block below it still runs.
func TestBriefRecallItem_BodylessItemGainsNoContentKey(t *testing.T) {
	got := briefRecallItem(map[string]any{"id": "mem_nobody", "type": "rule.work"})

	if _, ok := got["content"]; ok {
		t.Errorf("brief synthesised a content key for a bodyless item: %+v", got)
	}
	for _, k := range []string{"content_truncated", "content_full_len"} {
		if _, ok := got[k]; ok {
			t.Errorf("bodyless item must not be flagged %q: %v", k, got[k])
		}
	}
	if got["id"] != "mem_nobody" {
		t.Errorf("the id invariant must hold on the bodyless path too, got %v", got["id"])
	}
}

// TestBriefRecallItem_NonFiniteNumbersStayMarshalable is about what ROUNDING can
// introduce, not about implausible data. math.Round(f*1000)/1000 sends a finite but
// enormous f to +Inf, and encoding/json FAILS on +Inf — so without the guard brief
// mode could error on an item full mode serialises fine. Real values are bounded to
// roughly 0..3, so this can never fire in production; the invariant being locked is
// that brief mode is never able to fail where full mode succeeds.
func TestBriefRecallItem_NonFiniteNumbersStayMarshalable(t *testing.T) {
	for _, v := range []float64{math.MaxFloat64, 1e306, -1e306, math.Inf(1), math.Inf(-1), math.NaN()} {
		item := map[string]any{"id": "mem_big", "content": "x", "similarity": v}
		got := briefRecallItem(item)
		// Left untouched, so brief mode is exactly as (un)marshalable as full mode.
		if got["similarity"] != v && !(math.IsNaN(v) && math.IsNaN(got["similarity"].(float64))) {
			t.Errorf("similarity %v was rewritten to %v; the guard should have left it alone",
				v, got["similarity"])
		}
		if _, err := json.Marshal(got); err != nil {
			// Full mode would fail identically on this input; what must NOT happen is
			// brief failing where full succeeds.
			if _, fullErr := json.Marshal(item); fullErr == nil {
				t.Errorf("brief mode failed to marshal (%v) an item full mode handles fine: %v", err, v)
			}
		}
	}
	// And the ordinary case still rounds.
	got := briefRecallItem(map[string]any{"id": "m", "content": "x", "similarity": 0.4556157860526919})
	if got["similarity"] != 0.4556 {
		t.Errorf("finite values must still round to 4dp, got %v", got["similarity"])
	}
}

// TestBriefFields_KeepsIDAsTheRetrievalMechanism locks the mechanism, not just the
// outcome. briefFields is the ONLY path that carries `id` into a brief item, and
// criterion 2 of aihub#313 rests on it. An earlier draft had a redundant fallback
// block after the copy loop; review showed it was unreachable AND that it made
// TestBriefRecallItem_AlwaysCarriesID pass even with "id" removed from briefFields —
// the outcome test could not see the mechanism disappear. The fallback is gone, so
// that test bites again; this one names the requirement directly so a future
// refactor of briefFields cannot quietly drop it.
func TestBriefFields_KeepsIDAsTheRetrievalMechanism(t *testing.T) {
	var found bool
	for _, f := range briefFields {
		if f == "id" {
			found = true
		}
	}
	if !found {
		t.Fatalf("briefFields must contain \"id\" — it is the only thing that carries the id "+
			"into a brief item, and without it pf_get_memory is unreachable: %v", briefFields)
	}
}

// TestBriefLine_NeverReturnsAZeroInformationLine is the WARN-1 regression. Taking
// content[:IndexByte('\n')] literally returns "" for a body opening with a blank
// line and "---" for one opening with YAML frontmatter — both plausible in a
// markdown memory corpus, and both yield a brief item carrying nothing while still
// reporting itself as a projection. The model's only recovery is the full read this
// projection exists to avoid, so the degenerate case converts a saving into an
// induced round-trip (~192k tok here). Every case below must yield a line the model
// can actually judge the memory by.
func TestBriefLine_NeverReturnsAZeroInformationLine(t *testing.T) {
	for _, tc := range []struct {
		name, content, want string
	}{
		{"leading blank line", "\nsecond line here", "second line here"},
		{"leading blank lines and indent", "\n\n   # Real Title", "# Real Title"},
		{"yaml frontmatter", "---\ntitle: x\n---\n# T", "title: x"},
		{"horizontal rule first", "-----\n# After the rule", "# After the rule"},
		{"whitespace-only line first", "   \n# Real Title", "# Real Title"},
		{"crlf body", "line1\r\nline2", "line1"},
		{"plain first line", "# Title\nbody", "# Title"},
		{"single line", "just this", "just this"},
		{"trailing newline only", "hello\n", "hello"},
		// No informative line anywhere: fall back to the collapsed head. Returning ""
		// here would be LESS informative than the truth — "--- ---" is literally what
		// the memory contains — so the fallback reports it rather than hiding it. ""
		// comes back only when the body really carries no non-whitespace rune.
		{"only fences and blanks", "---\n\n---\n", "--- ---"},
		{"only whitespace", "   \n\t\n", ""},
		{"genuinely empty", "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := briefLine(tc.content); got != tc.want {
				t.Errorf("briefLine(%q) = %q, want %q", tc.content, got, tc.want)
			}
		})
	}
}

// TestBriefRecallItem_DoesNotAdvertiseWhitespaceAsMissingBody: "hello\n" holds one
// rune more than the line shown, but that rune is a newline. Flagging it truncated
// with content_full_len 6 invites a pf_get_memory round-trip that buys nothing, and
// a round-trip costs ~192k tok here — far more than the whole response.
func TestBriefRecallItem_DoesNotAdvertiseWhitespaceAsMissingBody(t *testing.T) {
	for _, content := range []string{"hello\n", "hello\n\n", "hello   ", "hello\r\n"} {
		got := briefRecallItem(map[string]any{"id": "m", "content": content})
		if _, ok := got["content_truncated"]; ok {
			t.Errorf("content %q flagged truncated (full_len=%v) when only whitespace was dropped",
				content, got["content_full_len"])
		}
	}
	// ...but a genuinely shortened body must still be flagged.
	got := briefRecallItem(map[string]any{"id": "m", "content": "head\nreal tail content"})
	if got["content_truncated"] != true {
		t.Errorf("a body with a real second line must still be flagged truncated: %+v", got)
	}
}

// TestSlimRecallResultMode_MatchesPreChangeFullMode is the missing pre-change
// reference (WARN 7). TestSlimRecallResultMode_FullModeIsUnchanged compares the new
// entry point against its own one-line delegate, so it is structurally incapable of
// noticing a change to the SHARED body — which is exactly what "byte-identical to
// what slimRecallResult produced before this wi" means. preChangeSlimRecallResult
// below is the function as it stood at 3abc042, vendored verbatim, so any drift in
// the shared path now shows up as a byte diff.
func TestSlimRecallResultMode_MatchesPreChangeFullMode(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   map[string]any
	}{
		{"rich item", map[string]any{"items": []any{briefFixture()}, "total": float64(1)}},
		{"empty items", map[string]any{"items": []any{}, "total": float64(0)}},
		{"non-map item", map[string]any{"items": []any{"not a map"}}},
		{"empty item map", map[string]any{"items": []any{map[string]any{}}}},
		{"unknown top-level field", map[string]any{"items": []any{}, "surprise": "kept?"}},
		{"no items key", map[string]any{"total": float64(3)}},
		{"cursor and diagnostics", map[string]any{
			"items": []any{briefFixture()}, "total": float64(9), "next_cursor": "c1",
			"unmatched_types": []any{"nope.type"}, "unmatched_types_error": "boom",
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			want := mustJSON(t, preChangeSlimRecallResult(deepCopy(t, tc.in)))
			got := mustJSON(t, slimRecallResultMode(deepCopy(t, tc.in), false))
			if string(want) != string(got) {
				t.Errorf("full mode drifted from the pre-change (3abc042) output:\n pre  = %s\n post = %s",
					want, got)
			}
		})
	}
}

// preChangeSlimRecallResult is slimRecallResult exactly as it stood at 3abc042, the
// commit this wi branched from. Vendored so the suite carries its own reference for
// "full mode is unchanged" instead of asserting it against code that changed in the
// same commit. Do NOT refactor this to share helpers with the production function —
// sharing is precisely what would let both drift together.
func preChangeSlimRecallResult(result map[string]any) map[string]any {
	if result == nil {
		return result
	}
	items, ok := result["items"].([]any)
	if !ok {
		return result
	}
	keep := map[string]bool{
		"id": true, "type": true, "content": true, "effective_strength": true,
		"similarity": true, "work_item_id": true, "tags": true, "related": true,
		"created_at":        true,
		"content_truncated": true, "content_full_len": true,
	}
	slim := make([]any, 0, len(items))
	for _, it := range items {
		m, ok := it.(map[string]any)
		if !ok {
			slim = append(slim, it)
			continue
		}
		out := make(map[string]any, len(keep)+1)
		for k, v := range m {
			if keep[k] {
				out[k] = v
			}
		}
		if attrs, ok := m["attrs"].(map[string]any); ok {
			if sp, ok := attrs["structured_payload"]; ok {
				out["attrs"] = map[string]any{"structured_payload": sp}
			}
		}
		if commits, ok := m["commits"].([]any); ok && len(commits) > 0 {
			notes := make([]any, 0, len(commits))
			for _, c := range commits {
				cm, ok := c.(map[string]any)
				if !ok {
					continue
				}
				note := map[string]any{}
				if b, ok := cm["body"]; ok {
					note["body"] = b
				}
				if a, ok := cm["author_display"]; ok {
					note["by"] = a
				}
				if reps, ok := cm["replies"].([]any); ok && len(reps) > 0 {
					rb := make([]any, 0, len(reps))
					for _, r := range reps {
						if rm, ok := r.(map[string]any); ok {
							if b, ok := rm["body"]; ok {
								rb = append(rb, b)
							}
						}
					}
					if len(rb) > 0 {
						note["replies"] = rb
					}
				}
				if len(note) > 0 {
					notes = append(notes, note)
				}
			}
			if len(notes) > 0 {
				out["commits"] = notes
			}
		}
		slim = append(slim, out)
	}
	res := map[string]any{"items": slim}
	if nc, ok := result["next_cursor"]; ok && nc != nil {
		res["next_cursor"] = nc
	}
	if total, ok := result["total"]; ok && total != nil {
		res["total"] = total
	}
	if um, ok := result["unmatched_types"]; ok && um != nil {
		res["unmatched_types"] = um
	}
	if ue, ok := result["unmatched_types_error"]; ok && ue != nil {
		res["unmatched_types_error"] = ue
	}
	return res
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
