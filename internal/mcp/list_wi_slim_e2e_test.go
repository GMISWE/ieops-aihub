package mcp_test

// aihub#278, driven through the registered tool rather than the projection
// function, for one reason: these tests COMPILE against the build before this
// change, so "fails before, passes after" is a real observation and not a build
// error read as one.
//
//	go test ./internal/mcp/ -run TestListWorkItemsResponse -count=1 -v
//
// Measured against the pre-change build (jsonResult(result), no projection):
//   - TestListWorkItemsResponseDropsReconstructibleFields  FAIL, naming all 9 fields
//   - TestListWorkItemsResponseKeepsPopulatedOptionalFields FAIL, on its
//     anti-vacuity tail only: the seven fields it protects all survive there (of
//     course — nothing is projected), but `seq` survives too, and that check is
//     what stops the test from passing against a projection that does nothing.
//   - TestListWorkItemsResponseKeepsEveryConsumedField     PASS
//
// The second one is the reverse half and is SUPPOSED to pass there. Its job is
// to go red in the other direction. Measured, one mutation at a time, over both
// files' tests — each row lists ONLY the tests that went red. Re-measure by
// re-applying them; do not extend this table by reasoning about it.
//
//	M1  slimListWorkItem returns immediately   Reconstructible · KeepsSeq… ·
//	                                           KeepsUnknownTopLevelKeys · Tolerates… ·
//	                                           KeepsPopulatedOptionalFields ·
//	                                           DropsReconstructibleFields
//	M2  + delete(m, "goal")                    Reconstructible · KeepsEveryConsumedField
//	M3  delete every key in the item           Reconstructible · KeepsNonNullValues… ·
//	                                           KeepsNullRequires… · KeepsConditionallyPresent… ·
//	                                           KeepsSeq… · KeepsNonCodingScenario ·
//	                                           KeepsPopulatedOptionalFields ·
//	                                           KeepsEveryConsumedField
//	M4  + requires_human_session to the        KeepsNullRequiresHumanSession ONLY
//	    null-drop set
//	M5  return a rebuilt top-level map         KeepsUnknownTopLevelKeys ONLY
//	M6  content deleted unconditionally        KeepsNonNullValues… ·
//	                                           KeepsPopulatedOptionalFields
//	M7  null loop deletes by NAME              KeepsNonNullValues… ·
//	    (drop the `v == nil` guard)            KeepsPopulatedOptionalFields
//	M8  drop seq's float64 range guard         NOTHING — see below
//
// Three things this table is here to say:
//
// M2 and M3 are why the reverse half exists. Both leave DropsReconstructibleFields
// green — M3 satisfies it perfectly, by deleting everything — and both are caught
// only by tests that assert something SURVIVED.
//
// M6 and M7 are why the two "…Populated…"/"…NonNullValues…" tests exist. They
// relax a rule's GUARD rather than adding a delete, so the projection of a
// null-valued fixture is unchanged and every other test stays green. Both were
// found in review, not by this suite; the suite had eight tests and no negative
// control on the rule the file calls its largest by bytes.
//
// M8 fails nothing, and that is reported rather than fixed. `int64(seq)` outside
// int64's range is implementation-defined; on amd64 it yields the indefinite
// value, the slug comparison then fails, and `seq` survives — the same outcome
// the guard produces, so no test can distinguish the two. The guard is kept
// precisely because a property that holds by platform accident cannot be pinned
// by a test; "safe because checked" is the only version of it a reader can rely
// on. Do not read the blank cell as "add a test here".

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

// TestListWorkItemsResponseKeepsPopulatedOptionalFields is the negative control
// at the transport level: the same item, but with a value in every field the
// projection is allowed to drop only when it is null.
//
// oneFullWorkItem() has all six as null — deliberately, because that is the
// pre-change response shape the forward probe is measured against — which means
// a projection that deleted those six by NAME rather than by VALUE would pass
// every other test in both files. Review found that, the suite did not.
func TestListWorkItemsResponseKeepsPopulatedOptionalFields(t *testing.T) {
	populated := map[string]any{
		"milestone":           "v1.2-alpha",
		"parent_work_item_id": "wi_epicParent",
		"closed_at":           "2026-08-31T09:00:00Z",
		"current_attempt_id":  "ra_Ku2oXS0y",
		"external_share_type": "public_link",
		"external_share_key":  "shk_9f2a1c",
		"content":             "a body a future server decided to serve on the list endpoint",
	}
	f := newFakeAihub(t)
	f.on("/v1/work_items", func(map[string]any) (int, any) {
		body := oneFullWorkItem()
		item := body["items"].([]any)[0].(map[string]any)
		for k, v := range populated {
			item[k] = v
		}
		return http.StatusOK, body
	})
	result, isErr := callTool(t, f, "pf_list_work_items", map[string]any{"project": "aihub"})
	if isErr {
		t.Fatalf("pf_list_work_items failed: %v", result)
	}
	item := result["items"].([]any)[0].(map[string]any)
	for k, want := range populated {
		got, present := item[k]
		if !present {
			t.Errorf("%s was dropped although it held %q — the rule is deleting by name, not by value", k, want)
			continue
		}
		if got != want {
			t.Errorf("%s = %v, want %v", k, got, want)
		}
	}
	// seq must still go: this item's slug does reconstruct it, and the point of
	// the control is that the OTHER rules stopped firing, not that all of them did.
	if _, present := item["seq"]; present {
		t.Error("seq survived an agreeing slug — the projection is not running at all")
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
// ⚠️ Read the citations as "why it is plausible this is read", NOT as proof.
// The skill documentation this repo ships is measurably out of sync with this
// response: using-polyforge/fragments/output-format.md:27 has the model render a
// multi-wi list with an `owner_display` column, and no item this endpoint serves
// has ever had that field (domain.WorkItem has reporter_display; owner_display
// is the ready queue's ReadyItem/RunningItem). So a field NAMED in the docs may
// be dead — and a field the docs do not name may still be read, because the
// reader is a language model and no compiler, test or error report stands
// between a removed field and a wrong answer. Neither direction is evidence,
// which is exactly why the projection obeys losslessness and not usage, and why
// the rows below justify KEEPING things and can never justify dropping one.
//
// That cuts against the reporter_display row in particular, so it says so at the
// row rather than borrowing authority from a line this same comment has just
// called wrong.
func TestListWorkItemsResponseKeepsEveryConsumedField(t *testing.T) {
	result, item := listOneWorkItem(t)

	kept := map[string]string{
		"id": "pf-release/SKILL.md builds included_wi_ids from it; pf-retro and pf-execute pass it as work_item_id",
		"slug": "output-format.md renders the wi as <project#seq>, which is the slug verbatim; " +
			"it is also the only remaining source of seq, which skills need to build the " +
			"pf.<project>-<seq>/<repo>/ worktree path (iron-rules.md, repo-routing.md, pf-init, pf-crystallize)",
		"project":  "pf-execute/engine.native.md resolves the step graph as {wi_type}.{project}.md",
		"goal":     "rendered in the Status table of every skill's three-segment output",
		"status":   "same table; also the only field left saying whether the item is closed",
		"wi_type":  "pf-execute/engine.native.md, the other half of {wi_type}.{project}.md",
		"priority": "output-format.md's multi-wi column list",
		"requires_human_session": "pf-execute/engine.native.md selects the execution mode from it; " +
			"its null is a third state (unclassified), which is why it is not null-dropped",
		"labels":             "no response-side consumer found, and kept anyway: a request-side filter proves nothing about the response, and no evidence says the model does not read it",
		"attrs":              "carries real payload, not bookkeeping; 42% of this project's list bytes and the single biggest thing this projection does NOT take",
		"declared_resources": "the conflict/lock surface; resources_version below is its CAS token",
		"resources_version":  "compare-and-set guard for a declared_resources write read from this same response",
		"reporter_display": "no exact consumer. output-format.md asks for an owner column but names " +
			"`owner_display`, which is not a field of this response — this is the nearest real one, " +
			"and it is kept under the same rule as reporter_user_id, not on the strength of that line",
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
