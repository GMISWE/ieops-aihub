package mcp_test

// aihub#543 probe wave 2 — the three hop-2/hop-4 claims of the pf_get_work_item
// card that no arm read.
//
// The card's own summary of this tool is that it is the ONE call that hands a
// caller a work item's body, and that its `brief` is a different operation from
// the identically-named flag on pf_update_work_item. Both halves were carded and
// neither was held: `pf_get_work_item.brief` appears in exactly one arm in the
// tree before this file (universal_contract_gate_test.go's
// localConsumptionParams, which holds that it never reaches the wire) and
// nothing at all drove the flag.
//
//	TestGetWorkItemBriefDeletesContentAndLeavesNoLengthBehind
//	  The behaviour AND the two descriptions that tell a caller the two flags
//	  differ. The reason this needs both sides is written on the
//	  pf_update_work_item description it reads: a caller told the two are
//	  equivalent applies "no content_len means no body" to this tool's reply and
//	  reads a work item with a 4 KB body as empty. That is a wrong answer no
//	  error can surface, so the only place it can be caught is here.
//
//	TestGetWorkItemRefusesAnEmptyIdWithoutReachingTheServer
//	  The card's hop-2 sentence says the empty id is rejected LOCALLY. A 400 from
//	  the server would satisfy every status assertion and none of that claim, so
//	  the arm counts requests rather than reading the refusal.
//
//	GOWORK=off go test ./internal/mcp/ -run TestGetWorkItem -count=1 -v

import (
	"encoding/json"
	"sort"
	"strings"
	"testing"
)

// count is how many HTTP requests this recorder has answered.
//
// The rejection arm below is about a call that must produce NONE, and "the
// response was an error" cannot distinguish a local refusal from a refusal the
// server sent back. Defined here rather than in recall_wire_query_test.go so the
// accessor sits next to its only reader.
func (q *queryRecorder) count() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return len(q.queries)
}

// getWorkItemBody is the fake server's reply: a work item with a body, and with
// the four fields the card says come back on this tool and are projected away or
// nulled on pf_list_work_items.
//
// Hand-written rather than built from domain.WorkItem, for the reason
// list_wi_slim_test.go's fullListItem states for itself — a fixture derived from
// the thing it checks moves with the defect instead of catching it.
func getWorkItemBody() map[string]any {
	return map[string]any{
		"id":                 "wi_gwiProbe",
		"slug":               "aihub#579",
		"project":            "aihub",
		"goal":               "a work item with a body",
		"status":             "queued",
		"content":            strings.Repeat("body ", 900), // ~4.5 kB, the card's example size
		"attrs":              map[string]any{"probe": "aihub#579"},
		"declared_resources": []any{},
		"resources_version":  float64(3),
	}
}

// TestGetWorkItemBriefDeletesContentAndLeavesNoLengthBehind is the behaviour and
// the publication of the two-flags distinction.
//
// 🔴 The absence of `content_len` is asserted, not just the absence of
// `content`. Deleting the body and reporting its size would be the OTHER tool's
// operation, and it is the answer a reader would reach for if the two flags were
// being unified — so an arm that only checked `content` was gone would go green
// on exactly the change the descriptions exist to forbid.
//
// The two descriptions are read off a real session rather than off the source:
// the contract JSON cli.RunDumpMCPSchemas produces carries no per-property
// descriptions, so it cannot see the strings this arm is about.
//
// ─── Recorded mutants ──────────────────────────────────────────────────────
//
// Every one was applied to this tree and the verdict is what RAN.
//
//	── enforcement side (the handler, the descriptions untouched) ──
//	M1 delete the `if boolArg(args, "brief") { delete(result, "content") }`
//	   branch in tools_lifecycle.go        RED  brief=true returned the body
//	M2 replace the delete with
//	   `result["content_len"] = len(...)`; body still removed
//	                                       RED  the length came back, which is
//	                                            pf_update_work_item's operation
//	                                            and the one the descriptions
//	                                            promise this tool does not do
//	M3 make the delete unconditional (drop the boolArg guard)
//	                                       RED  the control arm lost the body it
//	                                            never asked to drop
//
//	── publication side (the strings, the handler untouched) ──
//	M4 cut "NOT the same as pf_get_work_item's brief, which deletes content
//	   outright and reports no length." from pf_update_work_item's `brief`
//	                                       RED  the distinction goes unpublished
//	M5 reword pf_get_work_item's `brief` to "Replace the content field with its
//	   length"                             RED  the two descriptions now promise
//	                                            the same operation
//	G1 control: reword pf_get_work_item's `brief` as "Leave the content field out
//	   of the response (default false)"   GREEN  the promise survives a
//	                                            rewording, which is what keeps
//	                                            this from being a quote check
func TestGetWorkItemBriefDeletesContentAndLeavesNoLengthBehind(t *testing.T) {
	t.Run("brief=true removes the body and reports no length", func(t *testing.T) {
		q := newQueryRecorder(t)
		q.respondWith(getWorkItemBody())
		res := callToolAgainstRecorderResult(t, q, "pf_get_work_item", map[string]any{
			"work_item_id": "aihub#579", "brief": true,
		})
		if res.IsError {
			t.Fatalf("pf_get_work_item(brief=true) failed: %s", toolResultText(t, res))
		}
		got := decodeToolObject(t, toolResultText(t, res))

		if _, present := got["content"]; present {
			t.Errorf("brief=true returned a `content` key. The whole point of the flag is that a "+
				"~4 kB body does not ride back on a call the caller made for its metadata, and a "+
				"flag that is accepted and does nothing is indistinguishable from one that is not "+
				"published at all. Keys: %v", objectKeysSorted(got))
		}
		if _, present := got["content_len"]; present {
			t.Errorf("brief=true reported a `content_len`. That is pf_update_work_item's "+
				"operation: it REPLACES the body with its length, and its own published "+
				"description says this tool does not. A caller who learns the two are the same "+
				"applies \"no content_len means no body\" to this reply and reads a work item "+
				"with a 4 kB body as empty — a wrong answer with no error attached. Keys: %v",
				objectKeysSorted(got))
		}
		// The rest of the record must survive. Without this the arm above is
		// satisfied by a projection that returned nothing at all.
		for _, key := range []string{"id", "slug", "goal", "attrs", "declared_resources", "resources_version"} {
			if _, present := got[key]; !present {
				t.Errorf("brief=true also dropped %q. The flag names ONE field; anything else it "+
					"removes is a projection nobody published. Keys: %v", key, objectKeysSorted(got))
			}
		}
	})

	// CONTROL. Without this, an unconditional delete satisfies the arm above.
	t.Run("brief absent keeps the body", func(t *testing.T) {
		q := newQueryRecorder(t)
		q.respondWith(getWorkItemBody())
		res := callToolAgainstRecorderResult(t, q, "pf_get_work_item", map[string]any{
			"work_item_id": "aihub#579",
		})
		if res.IsError {
			t.Fatalf("pf_get_work_item failed: %s", toolResultText(t, res))
		}
		got := decodeToolObject(t, toolResultText(t, res))
		body, ok := got["content"].(string)
		if !ok {
			t.Fatalf("with no `brief` the body must come back; got content=%#v. This is the "+
				"call the coding scenario's spec step makes, and the reason it is told to make "+
				"THIS call rather than pf_list_work_items, whose two queries select no "+
				"wi.content at all.", got["content"])
		}
		if len(body) < 1000 {
			t.Errorf("the fixture body is %d B — too small for the arm above to be about a body "+
				"worth omitting", len(body))
		}
	})

	// The publication half: a caller has to be able to read that the two flags
	// are different operations, and they can only read the schema.
	t.Run("the two brief descriptions publish different operations", func(t *testing.T) {
		get := publishedParamDescriptionOf(t, "pf_get_work_item", "brief")
		update := publishedParamDescriptionOf(t, "pf_update_work_item", "brief")

		if get == update {
			t.Fatalf("pf_get_work_item.brief and pf_update_work_item.brief publish the SAME "+
				"string:\n    %q\nThey are different operations — one deletes the body, the "+
				"other replaces it with a length — and one string cannot describe both.", get)
		}

		// This tool's flag must promise a removal. Any of these carries it;
		// requiring a particular phrasing would be a quote check wearing a
		// costume.
		removal := []string{"omit", "leave the content field out", "without the content", "drop"}
		if !containsAnyFold(get, removal) {
			t.Errorf("pf_get_work_item.brief does not say it REMOVES the content field:\n    %q\n"+
				"None of %v appears in it, so a caller has no way to tell this from the other "+
				"tool's replace-with-a-length.", get, removal)
		}
		// And it must not promise a length, which is the other tool's operation.
		if strings.Contains(strings.ToLower(get), "content_len") {
			t.Errorf("pf_get_work_item.brief mentions content_len:\n    %q\nThis flag reports no "+
				"length; publishing one is the confusion the two descriptions exist to prevent.", get)
		}

		// The other tool's description is where the distinction is stated
		// OUTRIGHT, and it is the half a caller of this tool is most likely to
		// be reading when they get it wrong.
		for _, must := range []string{"pf_get_work_item", "content_len"} {
			if !strings.Contains(update, must) {
				t.Errorf("pf_update_work_item.brief never says %q:\n    %q\nIts own comment "+
					"records why both halves are exact: both were wrong once, and a caller told "+
					"the two flags are equivalent concludes a work item with a body is empty.",
					must, update)
			}
		}
		if !containsAnyFold(update, []string{"not the same", "differs from", "unlike"}) {
			t.Errorf("pf_update_work_item.brief no longer distinguishes itself from this tool's "+
				"brief:\n    %q\nIt names the tool and the field, which is not the same as "+
				"saying the two operations differ.", update)
		}
	})
}

// TestGetWorkItemRefusesAnEmptyIdWithoutReachingTheServer holds the card's hop-2
// sentence, which says the rejection is LOCAL.
//
// ⚠️ The assertion is a request COUNT, not a status. GET /v1/work_items/ with no
// segment would answer 404 or 405, so "the call came back an error" is true of
// both the local guard and its absence — and the difference is a round trip on
// every mistyped id, plus an error written by the router rather than for the
// caller.
//
// ─── Recorded mutants ──────────────────────────────────────────────────────
//
//	── enforcement side ──
//	M6 delete the `if id == ""` guard in pf_get_work_item's handler
//	                                       RED  one request reached the server
//	M7 change the guard to `if id == "x"`  RED  same, and the message stopped
//	                                            naming the parameter
//
//	── publication side (the declaration, the guard untouched) ──
//	M8 drop "work_item_id" from the tool's required list in objectSchema
//	                                       RED  the parameter the guard enforces
//	                                            is no longer declared required,
//	                                            so a caller reads it as optional
func TestGetWorkItemRefusesAnEmptyIdWithoutReachingTheServer(t *testing.T) {
	q := newQueryRecorder(t)
	q.respondWith(getWorkItemBody())

	res := callToolAgainstRecorderResult(t, q, "pf_get_work_item", map[string]any{
		"work_item_id": "",
	})
	if !res.IsError {
		t.Fatalf("an empty work_item_id was accepted: %s", toolResultText(t, res))
	}
	if n := q.count(); n != 0 {
		t.Errorf("the refusal cost %d HTTP request(s). The card says this tool rejects an empty "+
			"id LOCALLY; a rejection that goes to the server first is a round trip on every "+
			"mistyped id and an error message written by the router instead of for the caller.", n)
	}
	if msg := toolResultText(t, res); !strings.Contains(msg, "work_item_id") {
		t.Errorf("the refusal does not name the parameter, so the caller cannot act on it; got %q", msg)
	}

	// The declaration half: the guard enforces what the schema declares
	// required. Read off a real session, because that is what a caller sees.
	tool := publishedTool(t, "pf_get_work_item")
	raw, err := json.Marshal(tool.InputSchema)
	if err != nil {
		t.Fatalf("marshal pf_get_work_item InputSchema: %v", err)
	}
	var schema struct {
		Required   []string       `json:"required"`
		Properties map[string]any `json:"properties"`
	}
	if err := json.Unmarshal(raw, &schema); err != nil {
		t.Fatalf("pf_get_work_item InputSchema is not valid JSON: %v", err)
	}
	if len(schema.Properties) != 2 {
		t.Errorf("pf_get_work_item publishes %d parameter(s), and the card says two "+
			"(`work_item_id`, `brief`). A third one would be a promise nothing above reads: %v",
			len(schema.Properties), objectKeysSorted(schema.Properties))
	}
	found := false
	for _, r := range schema.Required {
		if r == "work_item_id" {
			found = true
		}
	}
	if !found {
		t.Errorf("pf_get_work_item does not declare `work_item_id` required (required=%v), but "+
			"its handler refuses the call without one. A parameter enforced and not declared is "+
			"a refusal the caller could not have predicted.", schema.Required)
	}
}

// ─── helpers shared by this file and the read-events probes beside it ────────

// decodeToolObject decodes a tool result's text block as a JSON object.
//
// A Fatal rather than a skip when it will not decode: every caller below is
// asserting about the object's KEYS, and "the payload was unreadable" would
// otherwise pass the absence checks for the worst possible reason.
func decodeToolObject(t *testing.T, text string) map[string]any {
	t.Helper()
	var out map[string]any
	if err := json.Unmarshal([]byte(text), &out); err != nil {
		t.Fatalf("the tool result is not a JSON object (%v):\n%s", err, text)
	}
	return out
}

// objectKeysSorted renders a decoded object's keys, sorted, for failure text.
func objectKeysSorted[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// publishedParamDescriptionOf reads one parameter's description off a REAL
// session, failing if the tool or the parameter is not published.
//
// Wrapping publishedTool + publishedParamDescription because every caller here
// wants the same failure on a missing subject: an arm about a published string
// must not pass by not finding the string.
func publishedParamDescriptionOf(t *testing.T, tool, param string) string {
	t.Helper()
	published := publishedTool(t, tool)
	raw, err := json.Marshal(published.InputSchema)
	if err != nil {
		t.Fatalf("marshal %s InputSchema: %v", tool, err)
	}
	desc, ok := publishedParamDescription(t, raw, param)
	if !ok {
		t.Fatalf("%s publishes no `%s` parameter, so the claim this arm is about describes a "+
			"string no caller can read. An arm cannot pass by not finding its subject.", tool, param)
	}
	if len(desc) < 20 {
		t.Fatalf("%s.%s's description is %d character(s) (%q) — too short to carry any claim, "+
			"and every check below would be asserting about a stub", tool, param, len(desc), desc)
	}
	return desc
}

// containsAnyFold reports whether s contains any of the phrases, case-folded.
//
// Several arms here require a PROMISE to still be present without pinning its
// wording: an assertion against one exact sentence goes red on a rewrite that
// keeps every promise, and the author's cheapest repair is then deleting the
// assertion.
func containsAnyFold(s string, phrases []string) bool {
	lower := strings.ToLower(s)
	for _, p := range phrases {
		if strings.Contains(lower, strings.ToLower(p)) {
			return true
		}
	}
	return false
}
