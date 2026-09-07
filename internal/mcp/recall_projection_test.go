package mcp_test

// aihub#418 — the pf_recall RESPONSE projection, as a mechanism.
//
// recall_slim.go held the LAST two keep-lists in this package: a per-item
// whitelist of 11 field names, and a top level that rebuilt the response as
// map[string]any{"items": …} and then conditionally copied five named keys. Under
// that shape the cheapest outcome of the server adding a field is that the model
// never sees it — and this one file recorded three separate occasions when
// exactly that happened, each closed by adding one more copy line rather than by
// changing the shape:
//
//	total                              aihub#249
//	content_truncated + content_full_len  aihub#269
//	unmatched_types                    aihub#289
//
// aihub#281 built list_wi_slim.go the other way round and said so in its header
// ("so it cannot become the fourth instance"); aihub#388 converted the claim
// response and folded this one without filing it. This is that fold, paid off, on
// the 7th-most-called tool in the registry.
//
// ─── Why the gate is quantified, not enumerated ─────────────────────────────
//
// The assertions below reflect over domain.MemoryWithStrength (which embeds
// domain.Memory) and domain.RecallResponse, so a field added tomorrow is covered
// the day it is added. Enumerating today's 32 item fields by hand would
// reproduce the very shape under repair: a list that is complete exactly until
// somebody adds something.
//
// One test asserts the SHAPE directly — a key no struct in this process has ever
// heard of must still arrive. No keep-list can pass that however complete it is,
// which is what makes this a fix rather than a fourth patch.
//
// ─── Why it drives the real tool ────────────────────────────────────────────
//
// Per aihub#388's note on the aihub#309 trap: a test on the projection function
// alone leaves a mutant one layer away green — `if false { result =
// slimRecallResultMode(...) }` at the call site in tools_memory.go is exactly
// such a mutant. Everything here therefore goes through the registered pf_recall
// tool against a fake aihub, so what is asserted is the observable tool output.
// (recall_brief_test.go's differential oracle covers the complementary question,
// that the conversion is byte-for-byte output-preserving on every field either
// shape knows about.)
//
// No database needed:
//
//	go test ./internal/mcp/ -run TestRecall -v

import (
	"encoding/json"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/GMISWE/ieops-aihub/internal/domain"
)

// recallItemKeysWithheldFromTheModel is the SPECIFICATION of what a recall item
// may withhold: a key here must not reach the model, everything else must.
//
// It lives in the test rather than being read out of the production delete-list,
// for the same reason claimKeysWithheldFromTheModel does: reading the
// implementation's own map would make this test agree with whatever the
// implementation happens to say. Stated here, the two are independent — a field
// withheld in production without a line here goes red, and a line here for a
// field that is in fact forwarded goes red too.
//
// Each entry carries a reason, because "not in the response" is otherwise
// indistinguishable from the defect this file exists to catch.
//
// `status` is deliberately absent — i.e. it must be FORWARDED. The recall
// predicate is status IN ('active','archived') when the request sets
// include_archived, so that caller receives a mixed result set and `status` is
// the answer to the question it just asked. Dropping it would be the aihub#249
// harm wearing a token-saving costume.
//
// `latest_id` is deliberately absent too, since aihub#429, and it is worth saying
// why the line that used to be here was WRONG rather than merely obsolete. It
// read "supersession bookkeeping; recall already resolves to the head version" —
// the same sentence shape as the `status` reason above, and false in the same
// case. An archived row's latest_id does not point at the row the caller is
// holding: internal/domain/memory.go inserts every row with latest_id = its own
// id (:1279-1285), archives the old head on supersede (:1205), and then repoints
// every row WHERE latest_id = oldHead — the archived head included — at the new
// id (:1333-1336). So under include_archived it is the forward edge to the
// current head, and the only one in the response.
//
// That this file's spec repeated the implementation's wording is itself the
// finding: an independent spec that inherits the implementation's reasoning is
// only independent about the FACT, not about the JUSTIFICATION. Both copies were
// wrong and neither could redden the other.
var recallItemKeysWithheldFromTheModel = map[string]string{
	"rendered_html": "a full standalone HTML document on methodology.* artifacts — the largest single " +
		"field a memory can carry, and unusable by a model.",
	"backlinks": "the reverse edge of `related`, which IS forwarded; both directions is double the " +
		"pointer volume for one graph.",

	"base_strength":     "an input to effective_strength, which is forwarded.",
	"stability_days":    "a decay parameter, not a fact about the content.",
	"activation_count":  "reinforcement bookkeeping behind effective_strength.",
	"last_activated_at": "activation bookkeeping; recency uses created_at, which is forwarded.",
	"last_activated_by": "provenance of a read, not content.",
	"is_immortal":       "a decay exemption flag, i.e. another effective_strength input.",
	"expires_at":        "lifecycle bookkeeping; an expired memory is not returned at all.",

	"project":            "the caller supplied it in the request.",
	"author_user_id":     "an opaque internal id the model cannot resolve.",
	"author_display":     "provenance rather than content.",
	"visibility":         "access control already enforced server-side.",
	"source_artifact_id": "an internal provenance pointer the model cannot resolve.",
	"updated_at":         "row-mutation bookkeeping; created_at is what a recency judgement uses.",

	"emb_model": "which embedding model produced the vector — infrastructure.",
	"emb_dims":  "the vector's dimensionality — infrastructure.",
}

// recallItemNarrowedKeys are the keys that are neither forwarded whole nor
// dropped: their VALUE is replaced by a narrower one. They are excluded from the
// forwarding census because "present but narrowed" is a third answer, and
// asserted separately by TestRecallResultNarrowsAttrsAndCommits.
var recallItemNarrowedKeys = map[string]string{
	"attrs":   "kept only as {structured_payload}.",
	"commits": "kept as body / by / replies, with ids, timestamps and thread structure stripped.",
}

// recallItemRealisticValues pins the fields whose VALUE the projection acts on,
// so the item stays on its happy path instead of being probed with a generated
// value that an internal type assertion would reject.
var recallItemRealisticValues = map[string]any{
	"id":      "mem_01JRECALLPROJECTION",
	"type":    "experience.pitfall",
	"content": "# Headline\n\nA body long enough to be worth projecting.",
	"attrs": map[string]any{
		"structured_payload": map[string]any{"level": "quick"},
		"internal_counter":   float64(3),
	},
	"commits": []any{map[string]any{
		"id": "c1", "body": "a human note", "author_display": "reviewer",
		"author_user_id": "u_x", "created_at": "2026-09-01T00:00:00Z",
		"replies": []any{map[string]any{"id": "r1", "body": "a reply"}},
	}},
	"related":   []any{map[string]any{"id": "mem_rel", "summary": "a pointer"}},
	"tags":      []any{"t1"},
	"backlinks": []any{map[string]any{"id": "mem_back", "summary": "reverse edge"}},
}

// structJSONFields returns a struct's json tag -> field type, descending into
// embedded structs so domain.Memory's fields count as MemoryWithStrength's.
//
// The recursion is the point: MemoryWithStrength adds two fields and inherits
// thirty, so a non-recursive walk would census 2 of 32 and every assertion below
// would be vacuously satisfied.
func recallStructJSONFields(t *testing.T, typ reflect.Type) map[string]reflect.Type {
	t.Helper()
	out := map[string]reflect.Type{}
	var walk func(reflect.Type)
	walk = func(ty reflect.Type) {
		for i := 0; i < ty.NumField(); i++ {
			f := ty.Field(i)
			if f.Anonymous && f.Type.Kind() == reflect.Struct {
				walk(f.Type)
				continue
			}
			tag := f.Tag.Get("json")
			if tag == "-" {
				continue
			}
			name, _, _ := strings.Cut(tag, ",")
			if name == "" {
				name = f.Name
			}
			out[name] = f.Type
		}
	}
	walk(typ)
	if len(out) == 0 {
		t.Fatalf("%s has no json fields at all — the reflection is broken, not the struct, and "+
			"every assertion below would be vacuous", typ)
	}
	return out
}

func recallItemFields(t *testing.T) map[string]reflect.Type {
	t.Helper()
	fields := recallStructJSONFields(t, reflect.TypeOf(domain.MemoryWithStrength{}))
	// A floor, because the recursion above is the one thing whose failure looks
	// exactly like a clean pass. 30 of the 32 come from the embedded Memory.
	if len(fields) < 25 {
		t.Fatalf("censused only %d recall item fields; the embedded domain.Memory is not being "+
			"walked, so this gate would check almost nothing", len(fields))
	}
	return fields
}

// recallProbeValue invents a wire value for one field type.
func recallProbeValue(t *testing.T, tag string, ft reflect.Type) any {
	t.Helper()
	if v, ok := recallItemRealisticValues[tag]; ok {
		return v
	}
	for ft.Kind() == reflect.Pointer {
		ft = ft.Elem()
	}
	switch ft.Kind() {
	case reflect.String:
		return "probe-" + tag
	case reflect.Bool:
		// false deliberately, not true: a truthiness-based copy would silently eat
		// it, and probing true would leave that bug green.
		return false
	case reflect.Int, reflect.Int32, reflect.Int64, reflect.Float32, reflect.Float64:
		return float64(7)
	case reflect.Slice:
		if ft.Elem().Kind() == reflect.String {
			return []any{"probe-" + tag}
		}
		return []any{map[string]any{"id": "probe-" + tag}}
	case reflect.Map, reflect.Struct:
		return map[string]any{"probe": tag}
	}
	t.Fatalf("no probe shape for recall item field %q of type %s — extend recallProbeValue, or "+
		"this field is silently excluded from the census", tag, ft)
	return nil
}

// fullRecallItem builds one item carrying EVERY field of the item struct.
func fullRecallItem(t *testing.T) map[string]any {
	t.Helper()
	fields := recallItemFields(t)
	for tag := range recallItemRealisticValues {
		if _, ok := fields[tag]; !ok {
			t.Errorf("recallItemRealisticValues pins %q, which is not a recall item field — a stale "+
				"entry is the rot that left \"expires_at\" in aihub#388's old keep-list", tag)
		}
	}
	item := map[string]any{}
	for tag, ft := range fields {
		item[tag] = recallProbeValue(t, tag, ft)
	}
	return item
}

// fullRecallResponse builds a response carrying every RecallResponse field and
// one item carrying every item field.
func fullRecallResponse(t *testing.T) map[string]any {
	t.Helper()
	resp := map[string]any{}
	for tag, ft := range recallStructJSONFields(t, reflect.TypeOf(domain.RecallResponse{})) {
		switch tag {
		case "items":
			continue
		case "total":
			resp[tag] = float64(1)
		default:
			resp[tag] = recallProbeValue(t, tag, ft)
		}
	}
	resp["items"] = []any{fullRecallItem(t)}
	return resp
}

// recallAgainst drives the real pf_recall against a fake aihub answering with
// `payload`, and returns the parsed tool result.
func recallAgainst(t *testing.T, payload map[string]any, args map[string]any) map[string]any {
	t.Helper()
	f := newFakeAihub(t)
	f.on("/v1/memories", func(map[string]any) (int, any) {
		return http.StatusOK, payload
	})
	call := map[string]any{"project": "aihub"}
	for k, v := range args {
		call[k] = v
	}
	_, obj := callToolText(t, f, "pf_recall", call)
	if len(obj) == 0 {
		t.Fatal("pf_recall returned an empty result — every assertion below would be vacuous")
	}
	return obj
}

func firstRecallItem(t *testing.T, result map[string]any) map[string]any {
	t.Helper()
	items, ok := result["items"].([]any)
	if !ok || len(items) == 0 {
		t.Fatalf("the result carries no items: %v", result)
	}
	m, ok := items[0].(map[string]any)
	if !ok {
		t.Fatalf("the first item is not an object: %#v", items[0])
	}
	return m
}

// TestRecallResultCarriesEveryMemoryField is THE gate, item level.
//
// It fails on the pre-fix tree for every field the 11-name whitelist did not
// mention — which is the point: it is the regression test for a projection that
// dropped fields silently, not a description of code that already worked.
func TestRecallResultCarriesEveryMemoryField(t *testing.T) {
	fields := recallItemFields(t)
	payload := fullRecallResponse(t)
	item := firstRecallItem(t, recallAgainst(t, payload, nil))

	forwarded, withheld, narrowed := 0, 0, 0
	for tag := range fields {
		if reason, ok := recallItemKeysWithheldFromTheModel[tag]; ok {
			if v, present := item[tag]; present {
				t.Errorf("%q is specified as withheld (%s) but the item carries it as %#v", tag, reason, v)
			}
			withheld++
			continue
		}
		if _, ok := recallItemNarrowedKeys[tag]; ok {
			if _, present := item[tag]; !present {
				t.Errorf("%q is specified as NARROWED, not dropped, and it is missing entirely", tag)
			}
			narrowed++
			continue
		}
		if _, present := item[tag]; !present {
			t.Errorf("the server sent recall item field %q and pf_recall's result does not carry it.\n"+
				"The projection drops it with no error, so the model reads a smaller object and "+
				"concludes the field is absent. That has happened three times in this file already "+
				"(total aihub#249, the truncation pair aihub#269, unmatched_types aihub#289), each "+
				"time to a field somebody had just added. The fix is the SHAPE: exposure by default, "+
				"deletion written down. If this field genuinely must be withheld, add it to "+
				"recallItemKeysWithheldFromTheModel with a reason.", tag)
			continue
		}
		forwarded++
	}
	if forwarded == 0 {
		t.Fatal("not one field was forwarded — this test is measuring nothing")
	}
	t.Logf("%d of %d recall item fields reached the model; %d withheld by design, %d narrowed",
		forwarded, len(fields), withheld, narrowed)
}

// TestRecallTopLevelCarriesEveryRecallResponseField is the same gate on the
// envelope, and it is where all three historical incidents actually happened.
func TestRecallTopLevelCarriesEveryRecallResponseField(t *testing.T) {
	fields := recallStructJSONFields(t, reflect.TypeOf(domain.RecallResponse{}))
	payload := fullRecallResponse(t)
	result := recallAgainst(t, payload, nil)

	for tag := range fields {
		if _, present := result[tag]; !present {
			t.Errorf("the server sent RecallResponse field %q (%#v) and pf_recall's result does not "+
				"carry it. Nothing in the envelope is withheld from the model, so this is the "+
				"aihub#249/#269/#289 failure recurring.", tag, payload[tag])
		}
	}
	if len(fields) < 5 {
		t.Fatalf("censused only %d RecallResponse fields — the reflection is broken", len(fields))
	}
}

// TestRecallResultWithholdsExactlyTheDocumentedKeys is the other direction: the
// widening must not start forwarding the bulk this projection exists to remove.
//
// rendered_html carries the marker because it is the volume hazard — a full HTML
// document — and the check is "absent from the serialised result ENTIRELY", not
// just under its own key: a copy nested inside a passed-through object costs the
// same tokens.
func TestRecallResultWithholdsExactlyTheDocumentedKeys(t *testing.T) {
	const bulkMarker = "AIHUB418-RENDERED-HTML-MUST-NOT-REACH-THE-MODEL"
	payload := fullRecallResponse(t)
	item := payload["items"].([]any)[0].(map[string]any)
	item["rendered_html"] = "<html><body>" + bulkMarker + "</body></html>"

	result := recallAgainst(t, payload, nil)
	got := firstRecallItem(t, result)

	for tag, reason := range recallItemKeysWithheldFromTheModel {
		if v, present := got[tag]; present {
			t.Errorf("%q is documented as withheld (%s) but the item carries it as %#v", tag, reason, v)
		}
	}

	blob, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("marshal result: %v", err)
	}
	if strings.Contains(string(blob), bulkMarker) {
		t.Errorf("rendered_html reached the model somewhere in the result: %s", blob)
	}
}

// TestRecallResultPassesThroughAFieldTheStructDoesNotHaveYet asserts the SHAPE,
// at both levels. This is the only assertion here that a merely-complete
// keep-list cannot pass, and it is the difference between this change and the
// three single-field patches that preceded it.
func TestRecallResultPassesThroughAFieldTheStructDoesNotHaveYet(t *testing.T) {
	const futureItem = "item_field_added_after_this_test_was_written"
	const futureTop = "response_field_added_after_this_test_was_written"

	if _, ok := recallItemFields(t)[futureItem]; ok {
		t.Fatalf("%q is now a real item field; pick another probe name", futureItem)
	}
	if _, ok := recallStructJSONFields(t, reflect.TypeOf(domain.RecallResponse{}))[futureTop]; ok {
		t.Fatalf("%q is now a real RecallResponse field; pick another probe name", futureTop)
	}

	payload := fullRecallResponse(t)
	payload[futureTop] = "envelope arrived"
	payload["items"].([]any)[0].(map[string]any)[futureItem] = "item arrived"

	result := recallAgainst(t, payload, nil)

	if got, present := result[futureTop]; !present || got != "envelope arrived" {
		t.Errorf("an envelope field this projection has never heard of did not reach the model "+
			"(got %#v, present=%v). That is the keep-list shape: exposure requires an edit, so the "+
			"cheapest outcome of adding a field is silence. Result keys: %v",
			got, present, sortedAnyKeys(result))
	}
	if got, present := firstRecallItem(t, result)[futureItem]; !present || got != "item arrived" {
		t.Errorf("an ITEM field this projection has never heard of did not reach the model "+
			"(got %#v, present=%v). The per-item whitelist is still a keep-list.", got, present)
	}
}

// TestRecallResultNarrowsAttrsAndCommits measures the RESIDUAL rather than
// describing it: these two keys survive with a narrower value, and their
// interiors are still whitelists. Stating that in a comment is what would rot.
func TestRecallResultNarrowsAttrsAndCommits(t *testing.T) {
	payload := fullRecallResponse(t)
	item := firstRecallItem(t, recallAgainst(t, payload, nil))

	attrs, ok := item["attrs"].(map[string]any)
	if !ok {
		t.Fatalf("attrs is missing or not an object: %#v", item["attrs"])
	}
	if _, ok := attrs["structured_payload"]; !ok {
		t.Error("attrs lost structured_payload, which is the half a caller wrote deliberately")
	}
	if _, leaked := attrs["internal_counter"]; leaked {
		t.Error("attrs forwarded an internal key; it must be narrowed to structured_payload alone")
	}
	if len(attrs) != 1 {
		t.Errorf("attrs carries %d keys, want exactly structured_payload: %v", len(attrs), attrs)
	}

	commits, ok := item["commits"].([]any)
	if !ok || len(commits) == 0 {
		t.Fatalf("commits is missing or empty: %#v", item["commits"])
	}
	note, ok := commits[0].(map[string]any)
	if !ok {
		t.Fatalf("the first commit is not an object: %#v", commits[0])
	}
	if note["body"] != "a human note" || note["by"] != "reviewer" {
		t.Errorf("the commit lost its insight (body/by): %v", note)
	}
	for _, gone := range []string{"id", "author_user_id", "created_at"} {
		if _, leaked := note[gone]; leaked {
			t.Errorf("commit bookkeeping %q was forwarded: %v", gone, note)
		}
	}
}

// TestRecallForwardsTheThreeHistoricalCasualties names the incidents, so a
// regression reports which one came back rather than "a field is missing".
//
// They are asserted with NO other top-level field present beyond items, i.e. the
// shape the server really sends when only one of them applies — the conditional
// copies they were fixed with were each reachable only in that shape.
func TestRecallForwardsTheThreeHistoricalCasualties(t *testing.T) {
	for _, tc := range []struct {
		wi      string
		payload map[string]any
		want    []string
		harm    string
	}{
		{
			wi: "aihub#249", want: []string{"total"},
			payload: map[string]any{"items": []any{}, "total": float64(42)},
			harm:    "a caller cannot tell \"that's everything\" from \"keep paging\"",
		},
		{
			wi: "aihub#269", want: []string{"content_truncated", "content_full_len"},
			payload: map[string]any{"items": []any{map[string]any{
				"id": "mem_t", "type": "fact.note", "content": "cut",
				"content_truncated": true, "content_full_len": float64(3587),
			}}},
			harm: "the model reasons on a snippet believing it is the whole memory, with no full " +
				"length to tell it a pf_get_memory follow-up is warranted",
		},
		{
			wi: "aihub#289", want: []string{"unmatched_types", "unmatched_types_error"},
			payload: map[string]any{"items": []any{},
				"unmatched_types": []any{"experience.typo"}, "unmatched_types_error": "count failed"},
			harm: "\"your type filter matched nothing\" becomes indistinguishable from \"this " +
				"project has no such memory\"",
		},
	} {
		t.Run(tc.wi, func(t *testing.T) {
			result := recallAgainst(t, tc.payload, nil)
			for _, key := range tc.want {
				// The truncation pair lives on the item; the others on the envelope.
				scope := result
				if strings.HasPrefix(key, "content_") {
					scope = firstRecallItem(t, result)
				}
				if _, present := scope[key]; !present {
					t.Errorf("%s regressed: %q was swallowed by the projection again. Consequence: %s",
						tc.wi, key, tc.harm)
				}
			}
		})
	}
}

// TestRecallRequestAdjustedSurvivesBothModes: request_adjusted is how the server
// says "I changed your request" (today top_k, clamped at 200). aihub#389 verified
// it survives three other projections; this is the fourth, and it must hold in
// brief mode too — a caller that asks for less detail has not asked to stop being
// told its request was rewritten.
func TestRecallRequestAdjustedSurvivesBothModes(t *testing.T) {
	adjusted := []any{map[string]any{
		"param": "top_k", "requested": float64(500), "effective": float64(200),
		"reason": "clamped to the 200 ceiling",
	}}
	for _, mode := range []struct {
		name string
		args map[string]any
	}{
		{"full", nil},
		{"brief", map[string]any{"fields": "brief"}},
	} {
		t.Run(mode.name, func(t *testing.T) {
			payload := map[string]any{
				"items":            []any{map[string]any{"id": "mem_ra", "type": "fact.note", "content": "x"}},
				"request_adjusted": adjusted,
			}
			result := recallAgainst(t, payload, mode.args)
			if _, present := result["request_adjusted"]; !present {
				t.Errorf("request_adjusted did not survive %s mode. The caller is then told its "+
					"top_k was honoured when it was clamped — and the whole point of a generic "+
					"disclosure field (aihub#314) is that the NEXT clamp costs nothing to disclose. "+
					"Result keys: %v", mode.name, sortedAnyKeys(result))
			}
		})
	}
}

// TestRecallBriefModeIsADeclaredKeepList states the boundary the work item asked
// to have made precise, as a test rather than a paragraph.
//
// Brief is deliberately NOT a delete-list: it exists to cut 74.6%, and forwarding
// by default would re-admit every bookkeeping column into the projection whose
// job is to remove them. What makes that safe is a different property — brief is
// lossy BY CONTRACT and ships its own escape hatch: `id` to re-read with, and the
// truncation pair to say there is more. So this test asserts both halves: an
// unknown item field does NOT appear (that is the declared mode behaviour, not a
// bug), and the escape hatch DOES.
func TestRecallBriefModeIsADeclaredKeepList(t *testing.T) {
	const futureItem = "item_field_added_after_this_test_was_written"
	payload := map[string]any{"items": []any{map[string]any{
		"id": "mem_brief", "type": "rule.work",
		"content":    "# Headline\n\nA second line that brief must not carry.",
		"created_at": "2026-09-01T10:00:00.123456Z",
		futureItem:   "should not reach brief",
	}}}

	item := firstRecallItem(t, recallAgainst(t, payload, map[string]any{"fields": "brief"}))

	if _, present := item[futureItem]; present {
		t.Errorf("brief mode forwarded an unknown item field. Brief is a DECLARED keep-list "+
			"(briefFields): if it forwarded by default it would stop being 25.4%% of full, which is "+
			"the only reason it exists. If this is now intended, change briefFields and say why. "+
			"Item: %v", item)
	}
	if got, ok := item["id"].(string); !ok || got != "mem_brief" {
		t.Errorf("brief dropped `id` (%#v) — it is the ONLY mechanism carrying the retrieval path, "+
			"so without it every dropped field becomes unrecoverable rather than one "+
			"pf_get_memory away", item["id"])
	}
	if _, ok := item["content_truncated"]; !ok {
		t.Error("brief shortened the body but did not flag it: the escape hatch is what makes this " +
			"a projection instead of silent loss")
	}
	if _, ok := item["content_full_len"]; !ok {
		t.Error("brief flagged truncation without a full length, so the model cannot tell whether " +
			"a follow-up read is worth a round-trip")
	}
	if body, _ := item["content"].(string); strings.Contains(body, "second line") {
		t.Errorf("brief carried more than the headline: %q", body)
	}
}

// ─── aihub#429: the include_archived result set ─────────────────────────────
//
// Both defects this section covers have the same shape, and it is the shape
// aihub#418 already caught once for `status`: a per-field decision whose stated
// reason is true of recall's DEFAULT predicate — status IN ('active') — and false
// of the one a caller opts into with include_archived, status IN
// ('active','archived'). A projection reasoned about on the default path alone
// mislays exactly the fields the other path exists to deliver.
//
// So every arm below models the mixed result set that predicate returns: an
// archived row whose latest_id points somewhere ELSE, which is the only shape in
// which either field carries information. An arm built on an active row would
// pass against the broken code, because on an active row latest_id is the item's
// own id and status is the constant the caller already knew.

// archivedRecallPayload is one archived row as the server returns it under
// include_archived: superseded, so its status is "archived" and its latest_id is
// the id of the row that replaced it.
//
// latest_id deliberately differs from id. Seeding it with the item's own id —
// which is what an ACTIVE row carries — would make "the head pointer was
// forwarded" and "the id was echoed" the same observation, and the assertion
// would stop discriminating.
func archivedRecallPayload() map[string]any {
	return map[string]any{"items": []any{map[string]any{
		"id":         "mem_superseded",
		"type":       "rule.work",
		"content":    "# The old wording\n\nA body that a newer version has replaced.",
		"created_at": "2026-09-01T10:00:00.123456Z",
		"status":     "archived",
		"latest_id":  "mem_current_head",
	}}}
}

// TestRecallForwardsLatestIDOnArchivedRows is the primary aihub#429 arm.
//
// latest_id used to be withheld as "supersession bookkeeping; recall already
// resolves to the head version, so this points at the row the caller is
// holding". For the archived rows include_archived returns, that sentence is
// false at every clause: internal/domain/memory.go inserts each row with
// latest_id = its own id (:1279-1285), archives the old head on supersede
// (:1205), then repoints every row WHERE latest_id = oldHead — which matches the
// archived head, precisely because its latest_id was its own id — at the new id
// (:1333-1336).
//
// So the field is the forward edge to the current head, and the response carries
// no other route to it. Withholding it left this caller holding a superseded body
// with no way to reach the live one short of a pf_get_memory per item, spent
// blind because nothing in the response said which items needed it.
func TestRecallForwardsLatestIDOnArchivedRows(t *testing.T) {
	item := firstRecallItem(t, recallAgainst(t, archivedRecallPayload(),
		map[string]any{"include_archived": true}))

	got, ok := item["latest_id"]
	if !ok {
		t.Fatalf("latest_id was withheld from an ARCHIVED row. On that row it is the pointer to "+
			"the current head, not — as the old reason had it — \"the row the caller is holding\": "+
			"the caller is holding mem_superseded and the head is mem_current_head. Nothing else in "+
			"the response can get it there. (aihub#429) Item: %v", item)
	}
	if got != "mem_current_head" {
		t.Errorf("latest_id = %#v, want %q", got, "mem_current_head")
	}
	// The companion field, so a regression that re-drops either is named
	// precisely rather than diagnosed from one failure.
	if item["status"] != "archived" {
		t.Errorf("status = %#v, want \"archived\": without it the caller cannot tell which rows in "+
			"a mixed result set the latest_id above even applies to", item["status"])
	}
}

// TestRecallBriefLabelsArchivedRows closes the brief-mode half.
//
// brief is a declared keep-list built from briefFields, and `status` was not in
// it, so include_archived + fields:"brief" produced a mixed result set with
// nothing on any item saying which rows were archived — the aihub#418 harm
// surviving in the mode aihub#418 did not look at.
//
// The fix is conditional rather than a new briefFields entry, and this arm is
// only half of that contract: TestRecallBriefOmitsStatusOnActiveRows below is the
// other half, and without it "forward status in brief" would be satisfied by
// forwarding it always — the variant measured at ~6.8% of a brief response to
// transmit a constant. Neither arm is sufficient alone.
func TestRecallBriefLabelsArchivedRows(t *testing.T) {
	item := firstRecallItem(t, recallAgainst(t, archivedRecallPayload(),
		map[string]any{"include_archived": true, "fields": "brief"}))

	if item["status"] != "archived" {
		t.Errorf("brief dropped `status` from an archived row (%#v). A caller that set "+
			"include_archived asked precisely which rows are archived, and brief answered with a "+
			"result set it cannot classify at all. (aihub#429) Item: %v", item["status"], item)
	}
	// Brief must not have quietly become a delete-list on the way: the escape
	// hatch and the id are what make the other dropped fields recoverable.
	if item["id"] != "mem_superseded" {
		t.Errorf("brief dropped `id` (%#v) — without it every dropped field, latest_id included, "+
			"stops being one pf_get_memory away", item["id"])
	}
	if _, present := item["latest_id"]; present {
		t.Errorf("brief forwarded latest_id (%#v). That is not the fix: brief is a declared "+
			"keep-list and its contract is that anything it drops is retrievable via `id`. If this "+
			"is now intended, change briefFields and say why", item["latest_id"])
	}
}

// TestRecallBriefOmitsStatusOnActiveRows is the NEGATIVE control for the arm
// above, and the reason the brief fix is conditional rather than a briefFields
// entry.
//
// Without this, "brief must carry status" is satisfied most cheaply by carrying
// it always — and always is what the measurement rejected. The recall predicate
// is status IN ('active') unless the request opts in, so on every recall that
// does not set include_archived the value is the constant "active" on every item.
// Forwarding it there costs ~6 tokens an item, ~120 of the canonical 1,766-token
// 20-item brief response (6.8%), taking brief from 25.4% of full to ~27.1% — in a
// file that argues over 0.6pp at briefRoundDigits. This arm is what makes that
// decision executable instead of a comment.
func TestRecallBriefOmitsStatusOnActiveRows(t *testing.T) {
	payload := map[string]any{"items": []any{map[string]any{
		"id": "mem_live", "type": "rule.work", "status": "active",
		"content": "# Still current\n\nA second line brief must not carry.",
	}}}

	item := firstRecallItem(t, recallAgainst(t, payload, map[string]any{"fields": "brief"}))

	if v, present := item["status"]; present {
		t.Errorf("brief forwarded status=%#v on an ACTIVE row. Under the default predicate that "+
			"value is a constant the caller already knows, and paying for it on every item is the "+
			"~6.8%% this fix was measured to avoid. Absence means active. (aihub#429)", v)
	}
	// Full mode is the contrast, and asserting it here keeps the two modes'
	// answers in one place: full forwards status unconditionally (aihub#418), so
	// a change that silenced BOTH modes cannot pass by looking like this one.
	full := firstRecallItem(t, recallAgainst(t, payload, nil))
	if full["status"] != "active" {
		t.Errorf("FULL mode dropped status on an active row (%#v) — aihub#418 forwards it "+
			"unconditionally there, and only brief mode is conditional", full["status"])
	}
}

// TestRecallBriefForwardsUnknownStatus pins the direction the predicate fails in.
//
// briefStatusIsInformative tests `!= "active"` rather than `== "archived"`.
// Recall cannot return a `redacted` row today — neither status set admits one —
// so this arm is not covering a reachable case, it is covering the DISPOSITION: a
// status this code has never heard of is exactly where saying nothing is worst,
// and an `== "archived"` predicate would silently drop it. Written as a test
// because a comment saying "we chose the safe direction" cannot fail when
// somebody tightens the comparison.
func TestRecallBriefForwardsUnknownStatus(t *testing.T) {
	payload := map[string]any{"items": []any{map[string]any{
		"id": "mem_odd", "type": "rule.work", "status": "quarantined",
		"content": "# A status this code has never heard of",
	}}}

	item := firstRecallItem(t, recallAgainst(t, payload, map[string]any{"fields": "brief"}))

	if item["status"] != "quarantined" {
		t.Errorf("brief dropped an UNKNOWN status (%#v). The predicate must be `!= \"active\"`, "+
			"not `== \"archived\"`: a value this projection cannot interpret is the one a caller "+
			"most needs to see. (aihub#429)", item["status"])
	}
}
