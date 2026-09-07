package mcp_test

// aihub#383 — hop 1 of pf_update_work_item must not promise what hop 3 drops.
//
// The failure mode is the one aihub#280 catalogues and aihub#288 met from the
// other side: a parameter published in the InputSchema, forwarded verbatim by
// the handler, then discarded at c.Bind because domain.UpdateWorkItemRequest
// has no json tag for it. The caller gets a 200 and no signal. `kind` was such
// a parameter — measured live: kind=chore on a RUNNING wi returned success with
// wi_type unchanged, where a value that had bound to wi_type would have tripped
// the status gate in domain.UpdateWorkItem.
//
// Two of the three tests here are gates. The third is a tripwire and says so.

import (
	"reflect"
	"strings"
	"testing"

	"github.com/GMISWE/ieops-aihub/internal/domain"
)

// updateWorkItemLocalParams are the published parameters the pf_update_work_item
// handler consumes itself and never forwards — its body loop skips exactly
// `work_item_id` and `brief`. Keep this in step with that loop: a name added
// here without being stripped there would silence the gate for it.
var updateWorkItemLocalParams = map[string]bool{"work_item_id": true, "brief": true}

// updateWorkItemRequestJSONNames reads the wire names domain.UpdateWorkItemRequest
// binds off its struct tags, so a rename on the server side is seen here rather
// than degrading into a silent drop two layers away.
func updateWorkItemRequestJSONNames(t *testing.T) map[string]bool {
	t.Helper()
	names := map[string]bool{}
	rt := reflect.TypeOf(domain.UpdateWorkItemRequest{})
	for i := 0; i < rt.NumField(); i++ {
		f := rt.Field(i)
		name := strings.Split(f.Tag.Get("json"), ",")[0]
		if name == "" || name == "-" {
			t.Fatalf("UpdateWorkItemRequest.%s has no json name (tag %q); every field of a c.Bind target must name its wire key", f.Name, f.Tag.Get("json"))
		}
		names[name] = true
	}
	if len(names) == 0 {
		t.Fatal("UpdateWorkItemRequest has no fields — the gate would pass vacuously")
	}
	return names
}

// TestUpdateWorkItemPublishesOnlyParamsTheServerBinds is the class gate: every
// parameter pf_update_work_item publishes AND forwards must be a name
// domain.UpdateWorkItemRequest decodes. On the pre-aihub#383 tree this fails on
// exactly one name, `kind`; it will fail again on the next parameter added to
// the schema without a binding, whatever it is called.
func TestUpdateWorkItemPublishesOnlyParamsTheServerBinds(t *testing.T) {
	props := schemaProps(t, publishedTool(t, "pf_update_work_item"))
	bound := updateWorkItemRequestJSONNames(t)

	forwarded := 0
	for name := range props {
		if updateWorkItemLocalParams[name] {
			continue
		}
		forwarded++
		if !bound[name] {
			t.Errorf("pf_update_work_item publishes %q but domain.UpdateWorkItemRequest has no json:%q field — the value is forwarded and then dropped at c.Bind with a 200 and no signal (aihub#383). Bind it on the server or stop publishing it.", name, name)
		}
	}
	if forwarded == 0 {
		t.Fatal("no forwarded parameters found — the gate passed vacuously")
	}
}

// TestUpdateWorkItemDoesNotPublishKind is the instance the class gate above was
// built from, kept as its own test so a re-introduction reads as "kind is back"
// rather than as an anonymous binding failure. `kind` on pf_list_work_items is
// a different parameter (a deprecated alias for its wi_type FILTER), is covered
// by TestListWorkItemsPublishesBothTypeSpellings, and is not touched here.
func TestUpdateWorkItemDoesNotPublishKind(t *testing.T) {
	props := schemaProps(t, publishedTool(t, "pf_update_work_item"))
	if p, ok := props["kind"]; ok {
		t.Errorf("pf_update_work_item publishes `kind` again (%q) — the server never bound it, and wiring it to wi_type would bypass reclassify_reason; see aihub#383", p.Description)
	}
	// Negative control: the parameter that does what `kind` pretended to do.
	// Without it the assertion above could pass on an emptied schema.
	if _, ok := props["wi_type"]; !ok {
		t.Errorf("pf_update_work_item no longer publishes wi_type — the `kind` assertion above would then be passing for the wrong reason")
	}
}

// TestListWorkItemsUserIDDescriptionDisclosesReporterOnly is a TRIPWIRE, not a
// proof. There is no cheap automated assertion that a description semantically
// matches a SQL predicate; this only checks that the two facts the aihub#383
// rewrite exists to state — the filter is REPORTER-only, and attempt owner /
// watchers are outside it — are still in the published text. It catches a
// revert to "Filter by user ID". It proves nothing about the predicate itself,
// which lives in domain.buildListWorkItemsWhere and is not read here.
func TestListWorkItemsUserIDDescriptionDisclosesReporterOnly(t *testing.T) {
	props := schemaProps(t, publishedTool(t, "pf_list_work_items"))
	got := props["user_id"].Description
	desc := strings.ToLower(got)
	for _, want := range []string{"reporter", "watcher"} {
		if !strings.Contains(desc, want) {
			t.Errorf("pf_list_work_items.user_id description no longer says %q — the predicate is wi.reporter_user_id = $N and callers must be told the set is reporter-only; got %q", want, got)
		}
	}
}
