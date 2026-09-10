package mcp_test

// aihub#543 probe wave 2 — the two `docs/mcp-cards/pf_recall.md` sentences that
// state what a caller can and cannot do about visibility, now that aihub#484 has
// withdrawn the parameter that looked like it offered a choice:
//
//	"There is no caller-facing visibility filter, and there never was one. The
//	 `visibility` clauses in both recall paths are authorization scoping the
//	 server derives from the caller's own role and id, so they narrow a page the
//	 same way whether or not the caller says anything."
//	"What a caller can still do is read a row's `visibility` through
//	 `pf_get_memory` …; what they cannot do is select on it — and the recall
//	 projection withholds the field per row …" (the sentence this wave
//	 CORRECTED — see the note below)
//	    -> TestRecallOffersNoVisibilitySelectorAndWithholdsTheFieldItself
//
// ─── Why the withdrawal needs its own arm at all ───────────────────────────
//
// The parameter shipped from this tool's first commit and every gate in this
// package was green on it for the whole time: it was published, it was forwarded
// (recallStringParams), and it was bound (handleRecall -> RecallRequest). What
// nothing asked was whether the ranking read it, which is the hop
// TestRecallEveryPublishedParamIsReadByTheRankingCode now holds — and that arm
// quantifies over what is PUBLISHED, so the moment the parameter was withdrawn
// it left that arm's field of view entirely. Re-publishing it tomorrow is
// therefore caught by that gate only if a reader is also added, and a
// re-published parameter with a plausible-looking hop-3 binding is exactly the
// artefact aihub#424 had to clean up months after aihub#394's withdrawal of
// `mode`.
//
// So this arm asserts the withdrawal itself, from the model's side of the wire:
// the name is absent from the published schema, absent from the query string the
// process emits when a caller sends it anyway, and the field's own fate in the
// response is stated correctly rather than optimistically.
//
// ─── 🔴 The second sentence was measured FALSE ─────────────────────────────
//
// Until this wave the card said:
//
//	"What a caller can still do is read each row's `visibility` in the response;
//	 what they cannot do is select on it."
//
// Measured 2026-09-10 by driving the real tool: the first half is wrong. The
// recall projection is a delete-list and `visibility` is one of the keys it
// deliberately withholds, with its reason written next to it in
// internal/mcp/recall_slim.go (recallItemWithheldKeys) — "an access-control fact
// already enforced server-side". So an MCP caller's recall page does not carry
// the field at all, and the card was consoling them with a capability they do
// not have. The withheld-key behaviour is right and already probed
// (TestRecallResultWithholdsExactlyTheDocumentedKeys); what was wrong was the
// sentence, and the honest version names the tool that DOES return it. That half
// is driven here too, because "reachable through pf_get_memory" is the whole of
// what a caller is left with, and nothing else in the tree asserts that this
// projection-free tool really carries it.
//
// No database:
//
//	GOWORK=off go test ./internal/mcp/ -run TestRecallOffersNoVisibilitySelector -count=1 -v

import (
	"encoding/json"
	"net/http"
	"os"
	"regexp"
	"strings"
	"testing"
)

// recallCardForVisibility is the published side of both sentences.
const recallCardForVisibility = "../../docs/mcp-cards/pf_recall.md"

// visibilityFieldFromCard reads the response field the card says a caller can
// still read and the tool that hands it over, out of the card's own sentences.
//
// 🔴 Read rather than written, for the reason §3.4 gives about never hard-coding
// the value: a test that spells `visibility` itself stays green on the day the
// card starts promising something else, which is the day the promise and the
// response can disagree.
func visibilityFieldFromCard(t *testing.T) (field, escape string) {
	t.Helper()
	raw, err := os.ReadFile(recallCardForVisibility)
	if err != nil {
		t.Fatalf("read %s: %v — the card is the published half of this claim, so an unreadable "+
			"card is a failure rather than a pass", recallCardForVisibility, err)
	}
	flat := strings.Join(strings.Fields(string(raw)), " ")
	m := regexp.MustCompile("the one MCP read that does is `(pf_[a-z_]+)`").FindStringSubmatch(flat)
	if m == nil {
		t.Fatalf("%s no longer names the tool that DOES hand a caller this field. That clause is "+
			"the half of the correction that makes the withholding survivable — without it the "+
			"card says only what a caller cannot have, and this arm has no escape to drive.",
			recallCardForVisibility)
	}
	escapeTool := m[1]
	m = regexp.MustCompile("read a recall row's `([a-z_]+)` either").FindStringSubmatch(flat)
	if m == nil {
		t.Fatalf("%s no longer says which row field a caller can still read, and through which "+
			"tool. That clause is the half of the sentence that makes the withdrawal survivable "+
			"— without it the card promises only what a caller CANNOT do, and this arm has no "+
			"field to trace.", recallCardForVisibility)
	}
	return m[1], escapeTool
}

// TestRecallOffersNoVisibilitySelectorAndWithholdsTheFieldItself holds the
// corrected pair: no selector, no field on a recall row, and the one tool that
// does hand it over.
//
// Mutants, all applied to the tree and run (2026-09-10):
//
//	M1  recallSchema re-publishes a `visibility` property              RED (no selector)
//	M2  recallStringParams forwards `visibility` again                 RED (wire)
//	M3  the key leaves recallItemWithheldKeys, so recall carries it    RED (withheld)
//	M4  pf_get_memory is projected through slimRecallResultMode        RED (escape)
//	M5  the card's clause names a different row field                  RED (publication)
//	M6  green control: reword the sentence around the same field
//	    name and the same three claims                                 GREEN
func TestRecallOffersNoVisibilitySelectorAndWithholdsTheFieldItself(t *testing.T) {
	field, escapeTool := visibilityFieldFromCard(t)

	// ── half 1: the tool publishes no selector for it ────────────────────────
	schema, err := json.Marshal(publishedTool(t, "pf_recall").InputSchema)
	if err != nil {
		t.Fatalf("re-marshal pf_recall's input schema: %v", err)
	}
	var decoded struct {
		Properties map[string]json.RawMessage `json:"properties"`
	}
	if err := json.Unmarshal(schema, &decoded); err != nil {
		t.Fatalf("pf_recall's input schema is not valid JSON: %v", err)
	}
	if len(decoded.Properties) == 0 {
		t.Fatal("pf_recall publishes no parameters at all, so the absence below is not evidence " +
			"about this one — it is an instrument failure")
	}
	if _, republished := decoded.Properties[field]; republished {
		t.Errorf("pf_recall publishes a %q parameter again. aihub#484 withdrew it on 2026-09-09 "+
			"after measuring that hops 1-3 were intact and hop 4 was empty: a caller sending "+
			"%s=project got the whole page, byte-identical to sending nothing. The card now "+
			"states in two places that no caller-facing filter exists, and the arm that would "+
			"catch an unread parameter (TestRecallEveryPublishedParamIsReadByTheRankingCode) "+
			"only sees names this schema publishes — so publishing it is also what puts it back "+
			"in scope. If this is a real filter now, say so on the card and give it a reader.",
			field, field)
	}

	// ── half 2: sending it anyway puts nothing on the wire ───────────────────
	q := newQueryRecorder(t)
	callToolAgainstRecorder(t, q, "pf_recall", map[string]any{
		"project": "aihub", "query": "token cost", field: "project",
	})
	sent := q.last(t)
	if sent.Has(field) {
		t.Errorf("pf_recall put %s=%q on the wire for an argument it does not publish. An "+
			"unpublished name reaching the query string is how a filter looks implemented from "+
			"every source scan while nothing reads it — the hop-2-intact, hop-4-empty shape this "+
			"tool has now produced twice (aihub#148, aihub#469).", field, sent.Get(field))
	}
	if sent.Get("project") != "aihub" {
		t.Fatalf("the recorded query carries no project (%v), so the absence above was measured "+
			"against a request that never happened", sent)
	}

	// ── half 3: a recall row does NOT carry it, and pf_get_memory does ──────
	row := map[string]any{
		"id": "mem_vis", "type": "fact.note", "content": "body", field: "private",
	}
	f := newFakeAihub(t)
	f.on("/v1/memories", func(map[string]any) (int, any) {
		return http.StatusOK, map[string]any{"items": []any{row}, "total": float64(1)}
	})
	_, result := callToolText(t, f, "pf_recall", map[string]any{"project": "aihub"})
	items, ok := result["items"].([]any)
	if !ok || len(items) == 0 {
		t.Fatalf("pf_recall returned no items (%v), so the projection half asserted nothing", result)
	}
	item, ok := items[0].(map[string]any)
	if !ok {
		t.Fatalf("the first item is not an object: %#v", items[0])
	}
	if got, present := item[field]; present {
		t.Errorf("a recall row reached the model carrying %s=%#v. The projection is a "+
			"delete-list that withholds this key on purpose, with its reason recorded beside it "+
			"in recall_slim.go, and the card now says so — a row that carries it means the entry "+
			"went and the card is describing a projection that no longer exists.",
			field, got)
	}
	if item["id"] != "mem_vis" {
		t.Fatalf("the projected row lost its id (%#v), so the absence above is evidence about a "+
			"projection that dropped everything rather than about this one key", item["id"])
	}

	// The escape the corrected sentence points at. pf_get_memory applies no
	// projection at all, which is why it is the answer — and why an arm is
	// needed: nothing else fails if a projection is added to it.
	g := newFakeAihub(t)
	g.on("/v1/memories/mem_vis", func(map[string]any) (int, any) { return http.StatusOK, row })
	_, full := callToolText(t, g, escapeTool, map[string]any{"memory_id": "mem_vis"})
	if got, present := full[field]; !present || got != "private" {
		t.Errorf("%[1]s answered without %[2]s (%#[3]v, present=%[4]v). It is the tool the card "+
			"now sends a caller to for this field, because it projects nothing; if it starts "+
			"projecting, the corrected sentence is false in its remaining half and a caller has "+
			"no way to read a row's visibility through MCP at all.", escapeTool, field, got, present)
	}
}
