package mcp_test

// aihub#290 — the cross-layer contract for PATCH /v1/work_items/:id/step.
//
// The defect this file exists to prevent has now happened twice on this one
// endpoint, and both times it was invisible from either side alone:
//
//   - `expected_version` was published in pf_update_step's InputSchema and
//     forwarded in the request body, and server.UpdateStepRequest never had a
//     field to bind it. echo's Bind drops unknown keys silently, so the value
//     was discarded on arrival with no error anywhere. Callers made a whole
//     extra pf_get_step round-trip to fetch that version — 92 of the 126
//     measured get_step -> update_step pairs really did send it — and paid for
//     an optimistic lock that did not exist.
//   - `artifact_summary` / `error_type` / `escalated` had the mirror-image
//     version of the same bug (aihub#206) until the struct caught up.
//
// A test on the MCP side alone cannot see this: the schema and the forwarding
// table agree perfectly, which is exactly what aihub#280's guard checks. A test
// on the server side alone cannot see it either: the struct is self-consistent.
// It is only visible by holding the two layers against each other, which is what
// this does — and it is why the assertion lives here rather than in either
// package's own tests. internal/server does not import internal/mcp, so there is
// no cycle.
//
// Run: go test ./internal/mcp/ -run TestUpdateStep -v   (no database needed)

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/GMISWE/ieops-aihub/internal/server"
)

// stepBodyFieldFor maps a pf_update_step MCP argument name to the JSON key the
// request body carries it under. Identity unless listed here.
//
// Keeping this table explicit (rather than deriving it) is deliberate: a rename
// on either side has to be acknowledged here, which is the moment to ask whether
// the other side was updated too.
var stepBodyFieldFor = map[string]string{
	"step_id": "step", // the server's field is `step`; the MCP argument is `step_id`
}

// stepArgsNotInBody are pf_update_step arguments that legitimately never appear
// in the request body, with the reason they do not. Anything NOT in this set and
// not bound by server.UpdateStepRequest is a silently-dropped parameter.
var stepArgsNotInBody = map[string]string{
	"work_item_id": "travels in the URL path (/v1/work_items/:id/step), not the body",
}

// boundJSONKeys returns the set of JSON keys a struct binds, reading the `json`
// tags the way echo's Bind does (name up to the first comma; "-" means never).
func boundJSONKeys(t *testing.T, v any) map[string]bool {
	t.Helper()
	typ := reflect.TypeOf(v)
	if typ.Kind() == reflect.Pointer {
		typ = typ.Elem()
	}
	out := map[string]bool{}
	for i := 0; i < typ.NumField(); i++ {
		f := typ.Field(i)
		tag := f.Tag.Get("json")
		if tag == "-" {
			continue
		}
		name, _, _ := strings.Cut(tag, ",")
		if name == "" {
			// No json tag: encoding/json falls back to the field name.
			name = f.Name
		}
		out[name] = true
	}
	return out
}

// publishedSchemaProps decodes a published tool's InputSchema property names.
func publishedSchemaProps(t *testing.T, name string) map[string]string {
	t.Helper()
	tool := publishedTool(t, name)
	raw, err := json.Marshal(tool.InputSchema)
	if err != nil {
		t.Fatalf("marshal InputSchema for %q: %v", name, err)
	}
	var schema struct {
		Properties map[string]struct {
			Type        string `json:"type"`
			Description string `json:"description"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(raw, &schema); err != nil {
		t.Fatalf("decode InputSchema for %q: %v", name, err)
	}
	if len(schema.Properties) == 0 {
		t.Fatalf("published InputSchema for %q has no properties", name)
	}
	out := map[string]string{}
	for k, v := range schema.Properties {
		out[k] = v.Type
	}
	return out
}

// TestUpdateStepPublishedParamsAreBoundServerSide is the guard proper: every
// argument pf_update_step advertises must be a field the server actually binds.
//
// This test FAILS on the pre-aihub#290 build, naming expected_version. That is
// the point — it is the regression test for a bug that shipped, not a
// description of code that already worked.
func TestUpdateStepPublishedParamsAreBoundServerSide(t *testing.T) {
	published := publishedSchemaProps(t, "pf_update_step")
	bound := boundJSONKeys(t, server.UpdateStepRequest{})

	for arg := range published {
		if reason, ok := stepArgsNotInBody[arg]; ok {
			t.Logf("%s: not sent in the body — %s", arg, reason)
			continue
		}
		field := arg
		if mapped, ok := stepBodyFieldFor[arg]; ok {
			field = mapped
		}
		if !bound[field] {
			t.Errorf("pf_update_step publishes %q (body key %q) but server.UpdateStepRequest has no field binding it — "+
				"echo's Bind drops it silently, so callers are promised a parameter the server never sees. "+
				"Either add the field to UpdateStepRequest and honour it, or remove the parameter from the tool schema.",
				arg, field)
		}
	}
}

// TestUpdateStepDoesNotPublishExpectedVersion pins the specific removal, so that
// re-adding the parameter without also adding the server field fails loudly here
// rather than becoming a dead argument again.
//
// Note what this does NOT say: it does not forbid optimistic locking on this
// endpoint. It forbids advertising it without implementing it. If CAS is ever
// wanted, add the field to server.UpdateStepRequest first — then the general
// test above passes and this one can be deleted in the same change.
func TestUpdateStepDoesNotPublishExpectedVersion(t *testing.T) {
	published := publishedSchemaProps(t, "pf_update_step")
	if _, ok := published["expected_version"]; ok {
		bound := boundJSONKeys(t, server.UpdateStepRequest{})
		state := "and server.UpdateStepRequest still does NOT bind it, so it is discarded on arrival"
		if bound["expected_version"] {
			state = "and server.UpdateStepRequest now binds it, so delete this test rather than the parameter"
		}
		t.Errorf("pf_update_step publishes expected_version again, %s. "+
			"The real concurrency guard on this endpoint is the `WHERE current_step_status = 'idle'` predicate, "+
			"which needs no client-supplied version — and requiring one forces a pf_get_step round-trip per step.",
			state)
	}
}

// TestUpdateStepPublishesNextStep is the other half of aihub#290: the fused
// complete-and-start argument has to be reachable. An undeclared parameter is
// one a well-behaved client will never send, so the fusion would exist in the
// server and never be used.
func TestUpdateStepPublishesNextStep(t *testing.T) {
	published := publishedSchemaProps(t, "pf_update_step")
	for _, arg := range []string{"next_step", "next_step_attempt_id"} {
		typ, ok := published[arg]
		if !ok {
			t.Errorf("pf_update_step does not publish %q, so no caller can use the fused advance", arg)
			continue
		}
		if typ != "string" {
			t.Errorf("%q is published as %q, want \"string\"", arg, typ)
		}
	}
}

// TestTerminalToolsPublishNote covers the note fusion (aihub#290 B3) at the same
// layer: pf_wrap and pf_complete_attempt must advertise `note`, or the closing
// note keeps costing a separate pf_emit_event — which additionally has to be
// ordered BEFORE the terminal call, because that call deletes the credentials
// pf_emit_event authenticates with.
func TestTerminalToolsPublishNote(t *testing.T) {
	for _, tool := range []string{"pf_wrap", "pf_complete_attempt"} {
		published := publishedSchemaProps(t, tool)
		typ, ok := published["note"]
		if !ok {
			t.Errorf("%s does not publish `note`", tool)
			continue
		}
		if typ != "string" {
			t.Errorf("%s publishes note as %q, want \"string\"", tool, typ)
		}
	}
}

// TestBatchCreateWorkItemsPublishesItemShape guards aihub#290 B5a against the
// aihub#238 failure: an array parameter whose entry shape is undocumented is a
// contract the caller has to guess at, and a wrong guess costs nothing at the
// call and everything later.
func TestBatchCreateWorkItemsPublishesItemShape(t *testing.T) {
	tool := publishedTool(t, "pf_batch_create_work_items")
	raw, err := json.Marshal(tool.InputSchema)
	if err != nil {
		t.Fatalf("marshal InputSchema: %v", err)
	}
	var schema struct {
		Properties map[string]struct {
			Type  string `json:"type"`
			Items struct {
				Type       string                     `json:"type"`
				Properties map[string]json.RawMessage `json:"properties"`
				Required   []string                   `json:"required"`
			} `json:"items"`
		} `json:"properties"`
		Required []string `json:"required"`
	}
	if err := json.Unmarshal(raw, &schema); err != nil {
		t.Fatalf("decode InputSchema: %v", err)
	}

	items, ok := schema.Properties["items"]
	if !ok {
		t.Fatal("pf_batch_create_work_items does not publish `items`")
	}
	if items.Type != "array" {
		t.Errorf("items is published as %q, want \"array\"", items.Type)
	}
	if items.Items.Type != "object" {
		t.Errorf("items entries are published as %q, want \"object\" — an array with no entry schema tells the caller nothing", items.Items.Type)
	}
	// The entry shape must actually carry the work-item fields, not just `goal`;
	// a batch that cannot set wi_type or declared_resources is not a substitute
	// for the single-item tool and callers would fall back to N calls.
	for _, field := range []string{"goal", "wi_type", "priority", "declared_resources", "content", "project"} {
		if _, ok := items.Items.Properties[field]; !ok {
			t.Errorf("batch item schema is missing %q — callers would have to fall back to pf_create_work_item", field)
		}
	}
	if len(items.Items.Required) != 1 || items.Items.Required[0] != "goal" {
		t.Errorf("batch item required = %v, want [goal] (project falls back to the top-level one)", items.Items.Required)
	}
}

// TestBatchCreateItemFieldsMatchSingleCreate stops the two create tools drifting.
// They are generated from one field table today; if someone re-inlines either
// schema, a field added to one and not the other silently becomes unreachable in
// the batch path — and the caller's only recourse is N single calls, which is the
// cost this work item removed.
func TestBatchCreateItemFieldsMatchSingleCreate(t *testing.T) {
	single := publishedSchemaProps(t, "pf_create_work_item")

	tool := publishedTool(t, "pf_batch_create_work_items")
	raw, _ := json.Marshal(tool.InputSchema)
	var schema struct {
		Properties map[string]struct {
			Items struct {
				Properties map[string]json.RawMessage `json:"properties"`
			} `json:"items"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(raw, &schema); err != nil {
		t.Fatalf("decode InputSchema: %v", err)
	}
	batchItem := schema.Properties["items"].Items.Properties

	for field := range single {
		if _, ok := batchItem[field]; !ok {
			t.Errorf("pf_create_work_item accepts %q but a batch item does not", field)
		}
	}
	for field := range batchItem {
		if _, ok := single[field]; !ok {
			t.Errorf("a batch item accepts %q but pf_create_work_item does not", field)
		}
	}
}
