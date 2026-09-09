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
	"encoding/json"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"

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

// ─── aihub#396: a closed vocabulary must be published as one ────────────────
//
// `priority` was published as the string "low|normal|high|urgent" — a
// pipe-separated list inside a DESCRIPTION, four lines above a `propEnum` call
// that would have made it a real enum. `source` was published as "Source
// reference", which reads as free text and is a seven-value CHECK constraint.
// Both cost the same thing: an out-of-vocabulary value travelled all four hops
// and came back as 500 INTERNAL_ERROR with a SQLSTATE in it.
//
// 🔴 aihub#396 attributed that cost to the SDK — "the go-sdk validates an `enum`
// before the handler runs (mcp/tool.go, resolved.Validate) and cannot validate
// prose". That premise is FALSE for this codebase and the correction is recorded
// here rather than left to be re-derived (aihub#496, 2026-09-09).
//
// aihub#463 measured it on go-sdk v1.6.0 (2026-09-08): applySchema ->
// resolved.Validate is reached from toolForErr, on the GENERIC AddTool[In, Out]
// path only. polyforge registers through the untyped method
// (*mcp.Server).AddTool (addTool in server.go), and Server.callTool invokes the
// handler with no schema step — driven end to end by a test that pushed an
// illegal value through a real client session into the POST body.
//
// What that changes, and what it does not: the enum still belongs here, because
// it is how a caller LEARNS the set (an LLM reading tools/list, and any client
// that validates before sending). It is simply not what REFUSES the value. The
// refusal is server-side Go validation answering 400 with the field named —
// enums are advisory, the Go check is the hard layer. So the assertions below
// are unchanged in substance, but they defend publication, not enforcement.
//
// The published set is taken from domain — the package whose validator refuses
// the value — so a caller is offered exactly what the server accepts. The
// assertions below are on that IDENTITY, not on a hard-coded list of values: a
// test naming 'low','normal','high','urgent' here would be a third copy of the
// vocabulary and would pass while all three drifted together away from the
// migration. (domain's own side of that chain is
// TestWorkItemVocabulariesMatchTheMigrations, which parses the SQL.)

// enumOfProp reads a published property's `enum` as a sorted list.
func enumOfProp(t *testing.T, tool, param string) []string {
	t.Helper()
	props := schemaProps(t, publishedTool(t, tool))
	if _, ok := props[param]; !ok {
		t.Fatalf("%s does not publish %q at all", tool, param)
	}
	raw := rawPropOf(t, tool, param)
	rawEnum, ok := raw["enum"].([]any)
	if !ok {
		desc, _ := raw["description"].(string)
		t.Fatalf("%s publishes %q with NO enum (description: %q). A closed vocabulary stated as "+
			"prose is a set no caller can read mechanically, so an illegal value crosses every "+
			"hop and dies at the DB CHECK as a 500 (aihub#396). NB the enum is advisory — this "+
			"process does not validate it (aihub#463 measured the untyped AddTool path); the "+
			"server-side Go check is what refuses the value, and both are required (aihub#496).",
			tool, param, desc)
	}
	out := make([]string, 0, len(rawEnum))
	for _, v := range rawEnum {
		s, isStr := v.(string)
		if !isStr {
			t.Fatalf("%s.%s enum contains a non-string %#v", tool, param, v)
		}
		out = append(out, s)
	}
	sort.Strings(out)
	return out
}

// rawPropOf decodes one published property as a raw map, which schemaProps
// cannot give (it projects to type+description).
func rawPropOf(t *testing.T, tool, param string) map[string]any {
	t.Helper()
	raw, err := json.Marshal(publishedTool(t, tool).InputSchema)
	if err != nil {
		t.Fatalf("marshal InputSchema for %q: %v", tool, err)
	}
	var schema struct {
		Properties map[string]map[string]any `json:"properties"`
	}
	if err := json.Unmarshal(raw, &schema); err != nil {
		t.Fatalf("decode InputSchema for %q: %v", tool, err)
	}
	p, ok := schema.Properties[param]
	if !ok {
		t.Fatalf("%s does not publish %q", tool, param)
	}
	return p
}

func TestWorkItemVocabulariesArePublishedAsEnums(t *testing.T) {
	wantPriority := append([]string(nil), domain.WorkItemPriorityList()...)
	sort.Strings(wantPriority)
	wantSource := append([]string(nil), domain.WorkItemSourceList()...)
	sort.Strings(wantSource)

	// Anti-vacuity: an empty domain list would make every comparison below
	// trivially satisfiable by an empty enum, which is the worst possible state
	// (a published enum matching nothing rejects every call).
	if len(wantPriority) == 0 || len(wantSource) == 0 {
		t.Fatalf("domain publishes %d priorities and %d sources — one of the vocabularies is empty",
			len(wantPriority), len(wantSource))
	}

	for _, tc := range []struct {
		tool, param string
		want        []string
	}{
		{"pf_create_work_item", "priority", wantPriority},
		{"pf_create_work_item", "source", wantSource},
		{"pf_update_work_item", "priority", wantPriority},
	} {
		t.Run(tc.tool+"."+tc.param, func(t *testing.T) {
			got := enumOfProp(t, tc.tool, tc.param)
			assert.Equal(t, tc.want, got,
				"the published enum and the vocabulary domain enforces must be one set; a caller "+
					"offered a value the server refuses is the same defect as a caller not being "+
					"told about a value it accepts")
		})
	}

	// pf_update_work_item binds no `source`, so it must not offer one — the
	// reverse-drift half. Asserted here rather than assumed because publishing it
	// would be a promise UpdateWorkItemRequest cannot keep, which is exactly what
	// TestUpdateWorkItemPublishesOnlyParamsTheServerBinds above exists for.
	t.Run("pf_update_work_item_publishes_no_source", func(t *testing.T) {
		props := schemaProps(t, publishedTool(t, "pf_update_work_item"))
		if _, published := props["source"]; published {
			t.Error("pf_update_work_item publishes `source`, which domain.UpdateWorkItemRequest " +
				"does not bind and UpdateWorkItem does not validate")
		}
	})
}

// TestLabelsCapIsPublished pins the other half of aihub#396's schema work: the
// cap existed only as `cardinality(labels) <= 20` in a migration, so the 21st
// label was a 500. The number is read from domain rather than written here, for
// the same reason as the enums.
func TestLabelsCapIsPublished(t *testing.T) {
	cap := domain.MaxWorkItemLabels()
	if cap <= 0 {
		t.Fatalf("domain.MaxWorkItemLabels() is %d — the assertion below would be vacuous", cap)
	}
	want := strconv.Itoa(cap)
	for _, tool := range []string{"pf_create_work_item", "pf_update_work_item"} {
		t.Run(tool, func(t *testing.T) {
			desc := schemaProps(t, publishedTool(t, tool))["labels"].Description
			if !strings.Contains(desc, want) {
				t.Errorf("%s does not tell callers the labels cap (%d). Got %q — so the 21st "+
					"label is discovered as a 500 from the CHECK constraint.", tool, cap, desc)
			}
		})
	}
}
