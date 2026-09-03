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
	"regexp"
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

// ─── aihub#265: pf_get_step's description IS its response contract ──────────
//
// The defect this closes is not a dropped parameter but a dropped ANSWER.
// pf_get_step advertised "step graph, current status, progress, previous steps"
// and server.StepState carried the current wi_step_state row and nothing else —
// no history, no summaries, no graph. Measured on production 2026-09-03,
// pf_get_step("tether#167") returned exactly five keys:
// current_step, current_step_status, version, wi_type, work_item_id.
//
// Nothing went red, because both layers were internally consistent: the schema
// declares only work_item_id (which is bound), and the struct is a valid struct.
// It is only visible by holding the description against the response type, which
// is what these two tests do. The consequence was two record points for one
// fact: every scenario step graph told the agent to read prior context from a
// hand-written `.pf_steps.json`, because this tool had nothing to read.

// getStepAdvertised pairs a claim pf_get_step's description makes about its
// RESPONSE with the JSON key of server.StepState that has to deliver it.
//
// Kept explicit for the reason stepBodyFieldFor is: a claim removed from the
// description, or a field removed from the struct, has to be acknowledged here —
// which is the moment to ask whether the other side moved too.
var getStepAdvertised = []struct{ Claim, JSONKey string }{
	{"completed_steps", "completed_steps"},
	{"artifact_summary", "completed_steps"}, // carried inside each entry
	{"current_step_status", "current_step_status"},
	{"current_step", "current_step"},
	{"work_item_id", "work_item_id"},
	{"version", "version"},
}

// getStepRequiredPhrases are sentences the description must keep that name no
// field at all, so getStepAdvertised cannot cover them.
//
// This exists because of a measured hole: the field-name assertions all stayed
// GREEN when the prohibition sentence was deleted, and that sentence is the only
// thing telling a caller who is NOT running a polyforge step template to
// distrust a progress file it finds in the worktree. The templates carry the
// same warning, but a caller reaching pf_get_step directly never reads them.
// A 989 -> 756 char trim of this description could have taken it out unnoticed.
var getStepRequiredPhrases = []struct{ Phrase, Why string }{
	{"Never take step progress from a file in the worktree",
		"the prohibition; the only warning a non-template caller ever sees"},
	{"absent only on a server older than",
		"absent vs [] is a different answer, and reading absent as empty is the original defect"},
}

// getStepAdvertisedMayBeAbsent records advertised keys that carry omitempty, and
// why that is acceptable. boundJSONKeys deliberately strips the option, so
// without this table the guard cannot tell "always on the wire" from "may
// vanish" — and a key that vanishes on exactly the resume path is the same class
// of defect as the one this file exists to close.
var getStepAdvertisedMayBeAbsent = map[string]string{
	"current_step": "absent when the work item has never started a step; completed_steps " +
		"answers the resume question on its own, and [] there is unambiguous",
}

// getStepDescriptionNonFields are snake_case tokens in pf_get_step's
// description that are deliberately NOT response fields, each with why. The
// general guard below rejects anything else, so a new promise cannot be added
// without either a field to back it or an entry here.
var getStepDescriptionNonFields = map[string]string{
	"step_id":        "a key of each completed_steps entry, not of StepState itself",
	"pf_recall":      "a sibling tool this description points callers at",
	"pf_read_events": "a sibling tool this description points callers at",
}

// Deliberately NOT listed above: work_item_id and completed_steps. Both ARE
// bound keys, so listing them here would let the loop wave them through if the
// field were later deleted — an allowlist entry for a real field is an escape
// hatch cheaper than the fix.

// snakeCaseToken matches the identifier shape this codebase uses for JSON keys
// and tool names, so the guard sees what a reader would read as a field name.
var snakeCaseToken = regexp.MustCompile(`\b[a-z][a-z0-9]*(?:_[a-z0-9]+)+\b`)

// jsonTagOption reports whether a struct field's json tag carries an option
// (e.g. "omitempty").
func jsonTagOption(t *testing.T, v any, jsonKey, option string) bool {
	t.Helper()
	typ := reflect.TypeOf(v)
	if typ.Kind() == reflect.Pointer {
		typ = typ.Elem()
	}
	for i := 0; i < typ.NumField(); i++ {
		tag := typ.Field(i).Tag.Get("json")
		name, opts, _ := strings.Cut(tag, ",")
		if name != jsonKey {
			continue
		}
		for _, o := range strings.Split(opts, ",") {
			if o == option {
				return true
			}
		}
		return false
	}
	t.Fatalf("%T binds no json key %q", v, jsonKey)
	return false
}

// TestGetStepAdvertisesAResponseItActuallyReturns is the guard proper, in both
// directions.
//
// How it was proven red, stated precisely because the loose version of this
// sentence was wrong. Against the pre-aihub#265 build with this test file added,
// the package does not COMPILE (server.CompletedStep and StepState.CompletedSteps
// do not exist) — and a compile error is the weaker outcome, because it reads as
// a broken build rather than as the defect. The discriminating red is the
// description-only mutant: restore the old description, KEEP the struct field,
// and this fails on 5 assertions plus the anti-vacuity t.Fatal. Renaming the
// json tag to step_history is red in both directions. Both were measured.
func TestGetStepAdvertisesAResponseItActuallyReturns(t *testing.T) {
	desc := publishedTool(t, "pf_get_step").Description
	bound := boundJSONKeys(t, server.StepState{})

	// Direction 1: every advertised claim is present in the description AND
	// backed by a bound field.
	for _, promise := range getStepAdvertised {
		if !strings.Contains(desc, promise.Claim) {
			t.Errorf("pf_get_step's description no longer mentions %q. It was advertised as part of the "+
				"response, so callers were told to rely on it; if it is genuinely gone, delete the entry "+
				"from getStepAdvertised in the same change so the removal is reviewable.", promise.Claim)
		}
		if !bound[promise.JSONKey] {
			t.Errorf("pf_get_step's description advertises %q but server.StepState binds no %q key — "+
				"the response cannot carry it, and a caller reading the description will conclude the "+
				"work it names is already accounted for. This is the aihub#265 defect: the tool promised "+
				"\"previous steps\" and returned only the current wi_step_state row.",
				promise.Claim, promise.JSONKey)
		}
	}

	// Direction 2, the general half: every field-shaped token the description
	// uses must be a real key somewhere in the response, or declared a non-field
	// with a reason. This is what makes the guard close the class rather than
	// this instance — a future description cannot invent a response field.
	stepKeys := boundJSONKeys(t, server.CompletedStep{})
	tokens := snakeCaseToken.FindAllString(desc, -1)
	if len(tokens) == 0 {
		t.Fatal("no snake_case tokens found in pf_get_step's description — the general guard below would " +
			"pass vacuously, so this is a failure of the guard, not a clean description")
	}
	sawCompletedSteps := false
	for _, tok := range tokens {
		if tok == "completed_steps" {
			sawCompletedSteps = true
		}
		if bound[tok] || stepKeys[tok] {
			continue
		}
		if _, ok := getStepDescriptionNonFields[tok]; ok {
			continue
		}
		t.Errorf("pf_get_step's description names %q, which is neither a JSON key of server.StepState "+
			"nor of server.CompletedStep. Either add the field, or add %q to "+
			"getStepDescriptionNonFields with the reason it is not one.", tok, tok)
	}
	// A stale allowlist entry is dead weight that silently widens the guard: it
	// keeps waving through a token the description no longer uses, so the next
	// person to reintroduce that token gets a free pass. Require every entry to
	// be earning its place.
	for tok, why := range getStepDescriptionNonFields {
		if !strings.Contains(desc, tok) {
			t.Errorf("getStepDescriptionNonFields lists %q (%s) but the description does not use it. "+
				"Delete the entry — an allowlist entry for an absent token pre-approves its return.", tok, why)
		}
	}

	// The phrases that carry no field name and would otherwise be unguarded.
	for _, req := range getStepRequiredPhrases {
		if !strings.Contains(desc, req.Phrase) {
			t.Errorf("pf_get_step's description no longer contains %q — %s. Every field-name "+
				"assertion in this test stays green without it, which is why it is listed separately.",
				req.Phrase, req.Why)
		}
	}

	// N1: advertised keys that may be omitted have to say so out loud.
	for _, promise := range getStepAdvertised {
		if !jsonTagOption(t, server.StepState{}, promise.JSONKey, "omitempty") {
			continue
		}
		if _, ok := getStepAdvertisedMayBeAbsent[promise.JSONKey]; !ok {
			t.Errorf("the description advertises %q and server.StepState omits it when empty, so a "+
				"caller told to rely on it can find it missing. Either drop omitempty or record why "+
				"absence is acceptable in getStepAdvertisedMayBeAbsent.", promise.JSONKey)
		}
	}

	if !sawCompletedSteps {
		t.Error("pf_get_step's description does not name completed_steps, so nothing tells a resuming " +
			"agent that the server holds its prior-step history — which is the whole of aihub#265")
	}

	// The description must not claim to return the step graph. It never did: the
	// graph is a scenario template in polyforge-coding, and advertising it here
	// is what let an agent believe one call would list the remaining steps.
	if strings.Contains(desc, "step graph, ") || strings.Contains(desc, "returns the step graph") {
		t.Error("pf_get_step's description claims to return the step graph again. aihub does not store " +
			"one — the graph is a scenario template, pinned per work item by scenario_ref.")
	}
}

// TestGetStepCompletedStepsDistinguishesEmptyFromAbsent guards the wire
// encoding, which is where this fix can be silently undone.
//
// `[]` and absent must not be the same bytes. `[]` means "this work item has
// completed no step"; absent means "this server predates aihub#265 and cannot
// answer". omitempty collapses them, and the collapsed value reads as "nothing
// is done" — so a client talking to an old server would start again from step 1
// while believing it had consulted the authority. That is the original defect
// with the stale local file swapped for a confident server response.
func TestGetStepCompletedStepsDistinguishesEmptyFromAbsent(t *testing.T) {
	if jsonTagOption(t, server.StepState{}, "completed_steps", "omitempty") {
		t.Error("server.StepState.CompletedSteps carries omitempty: an empty history and a server that " +
			"cannot report one now serialise identically, so a resuming agent cannot tell them apart")
	}

	empty, err := json.Marshal(server.StepState{CompletedSteps: []server.CompletedStep{}})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(empty), `"completed_steps":[]`) {
		t.Errorf("an empty history does not serialise as \"completed_steps\":[] — got %s", empty)
	}

	// A nil slice marshals to null, and the zero value of StepState HAS a nil
	// slice — so this asserts the hazard exists rather than pretending it does
	// not. The guarantee that callers never see it lives one layer down:
	// handleGetStep assigns from truncateCompletedSteps, which never returns nil
	// (TestTruncateCompletedSteps, internal/server).
	//
	// The earlier version of this assertion checked only that the key was
	// present, which PASSES on `"completed_steps":null` — the third spelling the
	// comment above it warned against. Checking for the key is not checking the
	// value.
	nilled, err := json.Marshal(server.StepState{})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(nilled), `"completed_steps":null`) {
		t.Errorf("StepState's zero value no longer marshals completed_steps as null — got %s.\n"+
			"That is not necessarily wrong, but it changes which spellings exist on the wire, so "+
			"update this assertion and truncateCompletedSteps' contract together.", nilled)
	}

	// Each entry has to carry what a resuming agent reads. step_id says which
	// step is done; artifact_summary is the content that used to live in
	// `.pf_steps.json` and is the reason a pointer at the server is a
	// replacement for that file rather than a downgrade.
	entry := boundJSONKeys(t, server.CompletedStep{})
	for _, key := range []string{"step_id", "status", "artifact_summary", "completed_at"} {
		if !entry[key] {
			t.Errorf("server.CompletedStep binds no %q key; a resuming agent cannot use the history without it", key)
		}
	}
	if jsonTagOption(t, server.CompletedStep{}, "artifact_summary", "omitempty") {
		t.Error("CompletedStep.ArtifactSummary carries omitempty: a step that recorded no summary and a " +
			"response that does not carry summaries become the same thing")
	}
}
