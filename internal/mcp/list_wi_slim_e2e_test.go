package mcp_test

// aihub#278, driven through the registered tool rather than the projection
// function, for one reason: these two tests COMPILE against the build before
// this change, so "fails before, passes after" is a real observation and not a
// build error read as one.
//
//	go test ./internal/mcp/ -run TestListWorkItemsResponse -count=1 -v
//
// Measured against the pre-change build (jsonResult(result), no projection):
//   - TestListWorkItemsResponseDropsReconstructibleFields  FAIL, naming all 9 fields
//   - TestListWorkItemsResponseKeepsEveryConsumedField     PASS
//
// The second one is the reverse half and is SUPPOSED to pass there. Its job is
// to go red in the other direction. Measured, one mutation at a time, over both
// files' tests — each row lists only the tests that went red:
//
//	M1  slimListWorkItem returns immediately     Reconstructible, KeepsSeq…,
//	                                             KeepsUnknownTopLevelKeys,
//	                                             Tolerates…, DropsReconstructible
//	M2  + delete(m, "goal")                      Reconstructible, KeepsEveryConsumedField
//	M3  delete every key in the item             Reconstructible, KeepsNullRequires…,
//	                                             KeepsConditionallyPresent…, KeepsSeq…,
//	                                             KeepsNonCodingScenario,
//	                                             KeepsEveryConsumedField
//	M4  + requires_human_session to the          KeepsNullRequiresHumanSession ONLY
//	    null-drop allowlist
//	M5  return a rebuilt top-level map           KeepsUnknownTopLevelKeys ONLY
//
// M2 and M3 are why the reverse half exists: both keep DropsReconstructibleFields
// green — M3 satisfies it perfectly — and both are caught only by tests that
// assert something SURVIVED. M4 is the one the losslessness proof cannot see;
// its dedicated test is the whole coverage for that field.

import (
	"net/http"
	"testing"
)

// oneFullWorkItem is a GET /v1/work_items body with every field the endpoint
// serves. Nulls are spelled out because the pre-change response spells them out
// — that is the state under test.
func oneFullWorkItem() map[string]any {
	return map[string]any{
		"items": []any{map[string]any{
			"id":                     "wi_pOes3im7",
			"seq":                    278,
			"slug":                   "aihub#278",
			"project":                "aihub",
			"scenario":               "coding",
			"goal":                   "pf_list_work_items field projection",
			"source":                 "human",
			"wi_type":                "feature",
			"priority":               "normal",
			"requires_human_session": false,
			"milestone":              nil,
			"labels":                 []any{"aihub", "mcp"},
			"status":                 "queued",
			"declared_resources":     []any{map[string]any{"type": "path", "uri": "file:internal/mcp/tools_lifecycle.go"}},
			"resources_version":      0,
			"external_share_type":    nil,
			"external_share_key":     nil,
			"reporter_user_id":       "u_5dFjeaMZ",
			"reporter_display":       "xiaokang.w",
			"current_attempt_id":     nil,
			"current_attempt_epoch":  0,
			"parent_work_item_id":    nil,
			"attrs":                  map[string]any{"measured_impact": "0.49%"},
			"content":                nil,
			"created_at":             "2026-08-29T02:31:43.746959Z",
			"updated_at":             "2026-08-30T04:29:53.487734Z",
			"closed_at":              nil,
			"step_state":             map[string]any{"current_step": "execute", "graph_source": "scenario"},
		}},
		"next_cursor": "2026-08-29T02:31:43.746959Z",
	}
}

func listOneWorkItem(t *testing.T) (map[string]any, map[string]any) {
	t.Helper()
	f := newFakeAihub(t)
	f.on("/v1/work_items", func(map[string]any) (int, any) {
		return http.StatusOK, oneFullWorkItem()
	})
	result, isErr := callTool(t, f, "pf_list_work_items", map[string]any{
		"project":            "aihub",
		"include_step_state": true,
	})
	if isErr {
		t.Fatalf("pf_list_work_items failed: %v", result)
	}
	items, ok := result["items"].([]any)
	if !ok || len(items) != 1 {
		t.Fatalf("want 1 item, got %v", result["items"])
	}
	item, ok := items[0].(map[string]any)
	if !ok {
		t.Fatalf("item is %T, want an object", items[0])
	}
	return result, item
}

// TestListWorkItemsResponseDropsReconstructibleFields is the forward probe: it
// FAILS on the build before this change, where the tool answered with the REST
// body verbatim.
//
// Each field below is named with the reason its removal is lossless, because a
// list of "these must be gone" is otherwise indistinguishable from a list of
// fields somebody found inconvenient.
func TestListWorkItemsResponseDropsReconstructibleFields(t *testing.T) {
	_, item := listOneWorkItem(t)

	gone := map[string]string{
		"content":  "neither list query SELECTs wi.content, so this is null on every row this endpoint has ever returned",
		"seq":      "the integer half of slug, which is exactly \"aihub#278\"",
		"scenario": "every row that can exist holds \"coding\"; CreateWorkItem rejects the other two",

		"milestone":           "null means none, and absence says the same",
		"parent_work_item_id": "null means none",
		"closed_at":           "null means not closed, which status already says",
		"current_attempt_id":  "null means no live attempt",
		"external_share_type": "null on 1049/1049 rows sampled",
		"external_share_key":  "null on 1049/1049 rows sampled",
	}
	for field, why := range gone {
		if _, present := item[field]; present {
			t.Errorf("%s survived the projection (%s)", field, why)
		}
	}
}

// TestListWorkItemsResponseKeepsEveryConsumedField is the reverse half.
//
// It passes on the pre-change build by construction — that is the point. It
// exists to fail in the opposite direction, and the projection that would sail
// through the forward probe above ("drop every field") dies here on the first
// row.
//
// The consumer citation on each row is the justification for keeping the field,
// and is why this is a hand-maintained table rather than "assert the response
// still has 20 keys". A count cannot tell anyone WHY a field is there, so a
// count is exactly as easy to satisfy by deleting the wrong field and lowering
// the number.
//
// ⚠️ The citations are load-bearing in one direction only. The skill
// documentation this repo ships is measurably out of sync with this response —
// using-polyforge/fragments/output-format.md:29 has the model rendering
// `owner.display` off this call, a field that has never existed on it — so a
// field NAMED in the docs may well be dead. It cannot be used the other way
// round: absence from the docs is not evidence that nothing reads a field,
// because the reader is a language model and no compiler, test or error report
// stands between a removed field and a wrong answer. Hence the rule this
// projection actually obeys, which is losslessness, not usage.
func TestListWorkItemsResponseKeepsEveryConsumedField(t *testing.T) {
	result, item := listOneWorkItem(t)

	kept := map[string]string{
		"id":       "pf-release/SKILL.md builds included_wi_ids from it; pf-retro and pf-execute pass it as work_item_id",
		"slug":     "using-polyforge/fragments/output-format.md renders the wi as <project#seq>, which is the slug verbatim",
		"project":  "pf-execute/engine.native.md resolves the step graph as {wi_type}.{project}.md",
		"goal":     "rendered in the Status table of every skill's three-segment output",
		"status":   "same table; also the only field left saying whether the item is closed",
		"wi_type":  "pf-execute/engine.native.md, the other half of {wi_type}.{project}.md",
		"priority": "output-format.md's multi-wi column list",
		"requires_human_session": "pf-execute/engine.native.md selects the execution mode from it; " +
			"its null is a third state (unclassified), which is why it is not null-dropped",
		"labels":                "no response-side consumer found, and kept anyway: a request-side filter proves nothing about the response, and no evidence says the model does not read it",
		"attrs":                 "carries real payload, not bookkeeping; 42% of this project's list bytes and the single biggest thing this projection does NOT take",
		"declared_resources":    "the conflict/lock surface; resources_version below is its CAS token",
		"resources_version":     "compare-and-set guard for a declared_resources write read from this same response",
		"reporter_display":      "output-format.md renders an owner column",
		"current_attempt_epoch": "survives to distinguish never-claimed from claimed-and-released once current_attempt_id is gone",
		"reporter_user_id":      "opaque, no consumer found — kept because 'no consumer found' is not evidence on this API",
		"source":                "ditto",
		"created_at":            "recency ordering and staleness reasoning",
		"updated_at":            "ditto",
		"step_state":            "aihub#280; pf-status and pf-retro both send include_step_state on their first call, every session",
	}
	for field, consumer := range kept {
		if _, present := item[field]; !present {
			t.Errorf("%s was dropped — consumer: %s", field, consumer)
		}
	}

	// next_cursor is the top-level half. No skill reads it today (they all cap
	// with `limit`), so losing it would not surface until the day someone
	// paginates — the failure mode recall_slim.go's INVARIANT note records for
	// `total`. Asserted here rather than trusted to the projection returning the
	// same map, because that is an implementation detail this test should
	// outlive.
	if got := result["next_cursor"]; got != "2026-08-29T02:31:43.746959Z" {
		t.Errorf("next_cursor = %v, want it preserved", got)
	}
}
