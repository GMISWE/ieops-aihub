package mcp

import (
	"reflect"
	"sort"
	"testing"
)

// Tests for the pf_list_work_items projection (aihub#278).
//
// The load-bearing one is TestSlimListWorkItems_ProjectionIsReconstructible.
// Every other test here pins a single rule and would survive a projection that
// deleted the whole item; that one would not. It is the REVERSE half the work
// item asked for: without it, widening slimListWorkItem to "delete everything"
// passes a suite made only of "field X was dropped" assertions.
//
//	go test ./internal/mcp/ -run TestSlimListWorkItems -count=1 -v
//
// The forward half — a probe that goes red against the build before this change
// — cannot live in this file, because it would have to call a function that does
// not exist there and so would fail to compile rather than fail. It is in
// list_wi_slim_e2e_test.go, driven through the registered tool, which does exist
// on both builds.

// fullListItem is one pf_list_work_items item with every field the endpoint can
// produce, spelled out by hand.
//
// Hand-written, not built from domain.WorkItem by reflection and not captured
// from the code under test: a fixture derived from the thing it is checking
// moves with it, and this fixture's job is to stay still.
func fullListItem() map[string]any {
	return map[string]any{
		"id":                     "wi_pOes3im7",
		"seq":                    float64(278),
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
		"resources_version":      float64(0),
		"external_share_type":    nil,
		"external_share_key":     nil,
		"reporter_user_id":       "u_5dFjeaMZ",
		"reporter_display":       "xiaokang.w",
		"current_attempt_id":     nil,
		"current_attempt_epoch":  float64(0),
		"parent_work_item_id":    nil,
		"attrs":                  map[string]any{"measured_impact": "0.49%"},
		"content":                nil,
		"created_at":             "2026-08-29T02:31:43.746959Z",
		"updated_at":             "2026-08-30T04:29:53.487734Z",
		"closed_at":              nil,
		// The two conditionally-present fields. A keep-list drafted from a
		// sample response would not contain either, because a sample response
		// does not have them — see the header of list_wi_slim.go.
		"similarity": float64(0.83),
		"step_state": map[string]any{"current_step": "execute", "graph_source": "scenario"},
	}
}

// restoreProjectedFields rebuilds a full item from a projected one.
//
// Deliberately spelled out with LITERAL field names rather than reading
// listWorkItemNullMeansNone, even though that map holds the same six strings.
// Reading the production constant would make this reconstructor track the
// implementation, and adding `requires_human_session` to that map — the exact
// mistake list_wi_slim.go documents — would then be restored here too and stay
// green. The duplication is the assertion.
func restoreProjectedFields(slim map[string]any) map[string]any {
	out := map[string]any{}
	for k, v := range slim {
		out[k] = v
	}
	// Rule 1: content is never selected by either list query.
	if _, ok := out["content"]; !ok {
		out["content"] = nil
	}
	// Rule 3: the only scenario a row can hold.
	if _, ok := out["scenario"]; !ok {
		out["scenario"] = "coding"
	}
	// Rule 4: nulls that mean "none".
	for _, k := range []string{
		"external_share_type", "external_share_key",
		"milestone", "parent_work_item_id", "closed_at", "current_attempt_id",
	} {
		if _, ok := out[k]; !ok {
			out[k] = nil
		}
	}
	return out
}

// TestSlimListWorkItems_ProjectionIsReconstructible is the reverse probe. It
// asserts the design rule of list_wi_slim.go directly — a field is removed only
// when the response still states the same thing without it — by rebuilding the
// full item from the projected one and demanding byte equality.
//
// Adding an UNCONDITIONAL delete of a non-reconstructible field turns this red
// with no judgement call about who reads what, which matters because on an
// LLM-facing API there is no consumer that can report the breakage.
//
// ⚠️ Its blind spot, stated because review found it the expensive way: this test
// sees what a rule DELETES, not whether the rule's guard still fires. Every
// conditional field in fullListItem() carries the value that makes its guard
// true, so removing a guard — `delete(m, "content")` unconditionally, or the
// null loop deleting by name — changes nothing about this fixture's projection
// and this test stays green. TestSlimListWorkItems_KeepsNonNullValuesOfNullDroppedFields
// is the other half; neither is redundant.
func TestSlimListWorkItems_ProjectionIsReconstructible(t *testing.T) {
	original := fullListItem()
	item := fullListItem()

	slimListWorkItem(item)

	if reflect.DeepEqual(item, original) {
		t.Fatal("projection removed nothing — this test would then pass vacuously")
	}
	restored := restoreProjectedFields(item)
	if !reflect.DeepEqual(restored, original) {
		t.Errorf("projection is not reconstructible.\n  lost:  %v\n  added: %v",
			missingKeys(original, restored), missingKeys(restored, original))
	}
}

// missingKeys names the keys of a present in b with a different (or absent) value.
func missingKeys(a, b map[string]any) []string {
	var out []string
	for k, v := range a {
		bv, ok := b[k]
		if !ok || !reflect.DeepEqual(v, bv) {
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out
}

// TestSlimListWorkItems_KeepsNonNullValuesOfNullDroppedFields is the negative
// control for the null-drop loop, and it is the only test in either file that
// has one.
//
// Every other test here runs against fullListItem(), where all six of these
// fields are null — so the guarded loop (`if v == nil`) and an unguarded one
// (`delete(m, k)` outright) produce byte-identical output, and reconstruction,
// the keep-tests and the e2e probes all stay green under the mutation that
// destroys a real milestone, a real closed_at and a live current_attempt_id.
// That gap was found in review, not by the suite: the rule the file calls "the
// largest by bytes" was the one rule with no test asserting its guard fires.
//
// So this test does the opposite of the fixture: every one of the six carries a
// real value, and none may be touched.
func TestSlimListWorkItems_KeepsNonNullValuesOfNullDroppedFields(t *testing.T) {
	populated := map[string]any{
		"milestone":           "v1.2-alpha",
		"parent_work_item_id": "wi_epicParent",
		"closed_at":           "2026-08-31T09:00:00Z",
		"current_attempt_id":  "ra_Ku2oXS0y",
		"external_share_type": "public_link",
		"external_share_key":  "shk_9f2a1c",
	}
	item := fullListItem()
	for k, v := range populated {
		item[k] = v
	}

	slimListWorkItem(item)

	for k, want := range populated {
		got, present := item[k]
		if !present {
			t.Errorf("%s was dropped although its value was %q, not null — "+
				"the null-drop loop is deleting by NAME, not by value", k, want)
			continue
		}
		if got != want {
			t.Errorf("%s = %v, want %v", k, got, want)
		}
	}

	// The same hole, one rule over. `content` is null on every row this endpoint
	// serves TODAY, but this package ships in the polyforge binary and talks to
	// aihub over HTTP, so an unconditional delete would be a claim about a SELECT
	// list in another process at another version. A server that starts serving
	// content must not have it stripped in silence.
	withBody := fullListItem()
	withBody["content"] = "## Spec\n\nthe body a newer server decided to serve"
	slimListWorkItem(withBody)
	if got := withBody["content"]; got != "## Spec\n\nthe body a newer server decided to serve" {
		t.Errorf("a non-null content was dropped or altered (got %v) — "+
			"the guard exists for version skew, not for today's SELECT list", got)
	}

	// The remaining conditional rule, scenario, has its control in
	// TestSlimListWorkItems_KeepsNonCodingScenario. content and the null six had
	// none until this test.
}

// TestSlimListWorkItems_KeepsNullRequiresHumanSession pins the field this work
// item was warned about by name. Its NULL is a third classification —
// "unclassified", 3.5% of live rows — not "no value", and a model reading its
// absence would take an unclassified work item for an unattended one.
//
// This is the case a losslessness proof cannot make: reconstructing "absent ->
// null" round-trips perfectly, so TestSlimListWorkItems_ProjectionIsReconstructible
// stays GREEN if requires_human_session is added to listWorkItemNullMeansNone.
// The two tests are not redundant; this one covers what that one is blind to.
func TestSlimListWorkItems_KeepsNullRequiresHumanSession(t *testing.T) {
	for _, v := range []any{nil, false, true} {
		item := fullListItem()
		item["requires_human_session"] = v
		slimListWorkItem(item)
		got, present := item["requires_human_session"]
		if !present {
			t.Fatalf("requires_human_session=%v was dropped; absence reads as false and "+
				"pf-execute selects its execution mode from this field", v)
		}
		if !reflect.DeepEqual(got, v) {
			t.Errorf("requires_human_session = %v, want %v", got, v)
		}
	}
	// Same argument, same blindness, for the field pf-execute resolves its step
	// graph from: `{wi_type}.{project}.md`.
	item := fullListItem()
	item["wi_type"] = nil
	slimListWorkItem(item)
	if _, present := item["wi_type"]; !present {
		t.Error("a null wi_type was dropped; untyped is a state step-graph resolution must see")
	}
}

// TestSlimListWorkItems_KeepsConditionallyPresentFields guards against this
// projection becoming instances four and five of the swallow recorded in
// recall_slim.go's INVARIANT note. Both fields below are `omitempty` on
// domain.WorkItem and are absent from any sample response taken without the
// parameter that produces them, so a keep-list drafted from such a sample drops
// them — silently, and in step_state's case on the first call pf-status and
// pf-retro each make, every session.
func TestSlimListWorkItems_KeepsConditionallyPresentFields(t *testing.T) {
	item := fullListItem()
	slimListWorkItem(item)
	for _, k := range []string{"similarity", "step_state"} {
		if _, present := item[k]; !present {
			t.Errorf("%s was dropped (aihub#273 / aihub#280)", k)
		}
	}
}

// TestSlimListWorkItems_KeepsSeq pins a NON-removal, which is unusual enough to
// say why. seq passes this file's losslessness rule outright — slug is
// `GENERATED ALWAYS AS (project || '#' || seq)`, so it is reconstructible by the
// schema — and an earlier revision did drop it.
//
// It is kept because of the one piece of hard evidence available about what the
// reader does with this response: across 315 real calls in this machine's
// transcripts, the model hand-wrote 148 `jq` projections to recover from
// oversized payloads, and `.seq` appears in 95 of them, more than any other
// field. jq renders a missing key and a null key identically, so every one of
// those filters is unaffected by the fields this projection DOES drop —
// `"\(.closed_at)"` prints `null` either way. `.seq` is the sole exception: it
// would print `278` before and `null` after.
//
// So this test guards against a future reader re-deriving the deletion from the
// rule alone. The rule permits it; the evidence does not.
func TestSlimListWorkItems_KeepsSeq(t *testing.T) {
	item := fullListItem()
	slimListWorkItem(item)
	got, present := item["seq"]
	if !present {
		t.Fatal("seq was dropped: reconstructible from slug, but named in 95 of the 148 " +
			"jq projections the model wrote against real responses, and the only dropped " +
			"field whose absence those filters would render as `null`")
	}
	if got != float64(278) {
		t.Errorf("seq = %v, want 278", got)
	}
}

// TestSlimListWorkItems_KeepsNonCodingScenario: `scenario` is dropped because
// every row that can exist holds "coding", so the drop has to be conditional on
// the value. If a migration ever makes writing/data real, those items keep their
// scenario and the rule stops applying to them instead of hiding the change.
func TestSlimListWorkItems_KeepsNonCodingScenario(t *testing.T) {
	for _, s := range []string{"writing", "data", "release"} {
		item := fullListItem()
		item["scenario"] = s
		slimListWorkItem(item)
		if got := item["scenario"]; got != s {
			t.Errorf("scenario %q was dropped or altered (got %v)", s, got)
		}
	}
}

// TestSlimListWorkItems_KeepsUnknownTopLevelKeys is the aihub#249 guard.
// slimRecallResult builds `res := map[string]any{"items": slim}` and therefore
// drops every top-level key nobody remembered to copy — which is how `total`
// vanished from pf_recall, and later `unmatched_types`. This projection returns
// the SAME map, so the guard is against someone "tidying" it into a rebuild.
//
// The made-up key matters as much as next_cursor: next_cursor could be preserved
// by an explicit conditional copy that would still swallow the NEXT field added
// to ListWorkItemsResult. A key this code has never heard of can only survive if
// the map was never rebuilt.
func TestSlimListWorkItems_KeepsUnknownTopLevelKeys(t *testing.T) {
	result := map[string]any{
		"items":                     []any{fullListItem()},
		"next_cursor":               "2026-08-30T04:29:53.487734Z",
		"total":                     float64(41),
		"a_field_added_next_summer": "must survive",
	}
	out := slimListWorkItemsResult(result)

	for k, want := range map[string]any{
		"next_cursor":               "2026-08-30T04:29:53.487734Z",
		"total":                     float64(41),
		"a_field_added_next_summer": "must survive",
	} {
		got, present := out[k]
		if !present {
			t.Errorf("top-level key %q was dropped", k)
			continue
		}
		if got != want {
			t.Errorf("top-level key %q = %v, want %v", k, got, want)
		}
	}
	if _, present := out["items"].([]any)[0].(map[string]any)["content"]; present {
		t.Error("items were not projected")
	}
}

// TestSlimListWorkItems_ToleratesShapesItCannotRead: the response is decoded
// into map[string]any, so nothing between the wire and here guarantees `items`
// is a list of objects. Every branch must pass the value through rather than
// panic or blank it.
func TestSlimListWorkItems_ToleratesShapesItCannotRead(t *testing.T) {
	if out := slimListWorkItemsResult(nil); out != nil {
		t.Errorf("nil result = %v, want nil", out)
	}
	errish := map[string]any{"error": "PROJECT_NOT_FOUND"}
	if out := slimListWorkItemsResult(errish); !reflect.DeepEqual(out, errish) {
		t.Errorf("a result with no items[] was altered: %v", out)
	}
	mixed := map[string]any{"items": []any{"not an object", fullListItem()}}
	out := slimListWorkItemsResult(mixed)
	items := out["items"].([]any)
	if items[0] != "not an object" {
		t.Errorf("unreadable item was altered: %v", items[0])
	}
	if _, present := items[1].(map[string]any)["content"]; present {
		t.Error("readable item alongside an unreadable one was not projected")
	}
}
