package mcp

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// aihub#238 defect 3: the correct declared_resources shape was documented in
// exactly one place — pf-plan/SKILL.md Step 5 — while the MCP JSON Schema said
// only `{"type":"array","description":"..."}`. Any caller that does not route
// through pf-plan (pf-work creating a wi directly, pf-spec, a human calling MCP
// by hand) had no way to learn the shape, and the MCP schema is their only
// contract. So the schema must carry the legal values itself.

// decode pulls the `items` subschema for one array property out of a rendered
// tool input schema.
func itemsSchemaFor(t *testing.T, raw json.RawMessage, propName string) map[string]any {
	t.Helper()
	var schema struct {
		Properties map[string]struct {
			Type  string         `json:"type"`
			Items map[string]any `json:"items"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(raw, &schema); err != nil {
		t.Fatalf("input schema is not valid JSON: %v", err)
	}
	p, ok := schema.Properties[propName]
	if !ok {
		t.Fatalf("schema has no property %q", propName)
	}
	if p.Type != "array" {
		t.Fatalf("property %q has type %q, want array", propName, p.Type)
	}
	if p.Items == nil {
		t.Fatalf("property %q has NO items schema — callers cannot learn the entry shape (aihub#238)", propName)
	}
	return p.Items
}

// enumOf reads items.properties.<field>.enum as a string set.
func enumOf(t *testing.T, items map[string]any, field string) map[string]bool {
	t.Helper()
	props, ok := items["properties"].(map[string]any)
	if !ok {
		t.Fatalf("items schema has no properties block: %v", items)
	}
	f, ok := props[field].(map[string]any)
	if !ok {
		t.Fatalf("items schema does not describe field %q", field)
	}
	rawEnum, ok := f["enum"].([]any)
	if !ok {
		t.Fatalf("items.properties.%s has no enum — the legal values stay invisible", field)
	}
	got := map[string]bool{}
	for _, v := range rawEnum {
		if s, isStr := v.(string); isStr {
			got[s] = true
		}
	}
	return got
}

func TestDeclaredResourcesProp_EnumeratesDeclaredTypesNotLockTypes(t *testing.T) {
	schema := objectSchema(map[string]any{
		"declared_resources": declaredResourcesProp("Declared resource locks"),
	}, nil)
	items := itemsSchemaFor(t, schema, "declared_resources")
	got := enumOf(t, items, "type")

	for _, want := range []string{"repo", "path", "document", "section", "service", "external_ref"} {
		if !got[want] {
			t.Errorf("declared_resources.type enum is missing legal value %q", want)
		}
	}
	// The whole trap in aihub#238: file_scope is a LOCK type and must never be
	// offered as a declared type, because it is legal in the neighbouring
	// vocabulary and so a wrong guess does not look wrong.
	for _, forbidden := range []string{"file_scope", "git_branch", "worktree", "tcp_port", "deploy_env"} {
		if got[forbidden] {
			t.Errorf("declared_resources.type enum wrongly offers the LOCK type %q", forbidden)
		}
	}
}

// The field is `uri`. The report's author wrote `value`; stored data also shows
// `path`, `scope`, `access`, `resource_key`. The schema must name `uri`.
func TestDeclaredResourcesProp_DescribesURIAndIntent(t *testing.T) {
	schema := objectSchema(map[string]any{
		"declared_resources": declaredResourcesProp("Declared resource locks"),
	}, nil)
	items := itemsSchemaFor(t, schema, "declared_resources")
	props, _ := items["properties"].(map[string]any)
	if _, ok := props["uri"]; !ok {
		t.Error("items schema does not describe `uri` — the field callers most often get wrong")
	}
	// `intent` must NOT be published as a closed enum (aihub#238 review finding 6):
	// the server never validates it, only "read" and "refactor" carry any behaviour,
	// and this repo's own fixtures use "exclusive" more often than "write" (18 vs 14).
	// A closed set here would state a contract the server does not keep — the very
	// failure mode this wi is about. Describe the semantics instead.
	intentProp, ok := props["intent"].(map[string]any)
	if !ok {
		t.Fatal("items schema does not describe `intent`")
	}
	if _, hasEnum := intentProp["enum"]; hasEnum {
		t.Error("`intent` is published as a closed enum, but the server does not validate it and `exclusive` is the repo's most-used value — this misstates the contract (aihub#238)")
	}
	intentDesc, _ := intentProp["description"].(string)
	for _, want := range []string{"read", "refactor"} {
		if !strings.Contains(intentDesc, want) {
			t.Errorf("intent description should explain the behaviour of %q; got %q", want, intentDesc)
		}
	}
	if !strings.Contains(intentDesc, "Not validated") {
		t.Errorf("intent description should say it is not validated server-side; got %q", intentDesc)
	}
	req, ok := items["required"].([]any)
	if !ok || len(req) == 0 {
		t.Fatal("items schema marks nothing as required; type and uri are both mandatory")
	}
	reqSet := map[string]bool{}
	for _, r := range req {
		if s, isStr := r.(string); isStr {
			reqSet[s] = true
		}
	}
	if !reqSet["type"] || !reqSet["uri"] {
		t.Errorf("items.required = %v, want both type and uri", req)
	}
}

func TestRequestedLocksProp_EnumeratesLockTypesNotDeclaredTypes(t *testing.T) {
	schema := objectSchema(map[string]any{
		"requested_locks": requestedLocksProp("Resource locks to acquire"),
	}, nil)
	items := itemsSchemaFor(t, schema, "requested_locks")
	got := enumOf(t, items, "resource_type")

	for _, want := range []string{"git_branch", "worktree", "file_scope", "tcp_port", "deploy_env"} {
		if !got[want] {
			t.Errorf("requested_locks.resource_type enum is missing legal value %q", want)
		}
	}
	// The mirror image of the trap: declared types must not be offered here.
	for _, forbidden := range []string{"repo", "path", "document", "section", "external_ref"} {
		if got[forbidden] {
			t.Errorf("requested_locks.resource_type enum wrongly offers the DECLARED type %q", forbidden)
		}
	}
	props, _ := items["properties"].(map[string]any)
	if _, ok := props["resource_key"]; !ok {
		t.Error("items schema does not describe `resource_key` (callers guess `value`/`uri`)")
	}
}

// aihub#238 review finding 1 — the most severe defect found in review.
//
// pf_claim_work_item does not return the server's response; it rebuilds a
// `safeResult` from an explicit passthrough whitelist so the session_secret never
// reaches the model. `unrecognized_resources` must be on that list.
//
// Reporting at claim is the ONLY remedy available on the stored-data path —
// rejecting there would make historical mistyped work items unclaimable. Filtered
// out here, the entire remedy is inert and the caller sees `{attempt_id,
// claim_epoch, ok:true, acquired_locks:[]}`, byte-identical to the pre-fix output
// that made a lockless wi look guarded.
func TestClaimPassesThroughUnrecognizedResources(t *testing.T) {
	b, err := os.ReadFile("tools_lifecycle.go")
	if err != nil {
		t.Fatalf("read tools_lifecycle.go: %v", err)
	}
	src := regexp.MustCompile(`\s+`).ReplaceAllString(string(b), " ")

	// Locate the passthrough list literal inside the claim handler.
	m := regexp.MustCompile(`for _, k := range \[\]string\{([^}]*)\} \{ if v, ok := result\[k\]; ok \{`).FindStringSubmatch(src)
	if m == nil {
		t.Fatal("could not locate the claim passthrough whitelist — update this guard")
	}
	if !strings.Contains(m[1], `"unrecognized_resources"`) {
		t.Errorf("the claim passthrough whitelist omits \"unrecognized_resources\", so the silent-no-lock warning never reaches the caller (aihub#238). List is: %s", m[1])
	}
}

// The regression that matters operationally: all four call sites must actually
// use the helpers. A helper nobody wires in fixes nothing, and the go-sdk offers
// no exported way to enumerate registered tools — so scan the package source.
//
// Scoped to the whole internal/mcp package (not one file) and whitespace-
// normalised, per the lesson in mem_I98xpPgY that a tripwire scoped narrower than
// the invariant it guards is worse than none. Proven to fail by reintroducing a
// bare prop("array", ...) for one of these keys.
func TestNoResourceArrayIsRegisteredWithoutItemSchema(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatalf("glob: %v", err)
	}
	// Matches `"declared_resources": prop("array", ...)` and the requested_locks
	// equivalent, tolerating any inner whitespace/alignment.
	bare := regexp.MustCompile(`"(declared_resources|requested_locks)"\s*:\s*prop\(\s*"array"`)

	var offenders []string
	scanned := 0
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		b, readErr := os.ReadFile(f)
		if readErr != nil {
			t.Fatalf("read %s: %v", f, readErr)
		}
		scanned++
		normalised := regexp.MustCompile(`\s+`).ReplaceAllString(string(b), " ")
		for _, m := range bare.FindAllStringSubmatch(normalised, -1) {
			offenders = append(offenders, f+": "+m[1])
		}
	}
	if scanned == 0 {
		t.Fatal("scanned no source files — the guard is not actually running")
	}
	if len(offenders) > 0 {
		t.Errorf("these resource arrays are registered with a bare prop(\"array\") and so publish no entry shape (aihub#238): %v\n"+
			"use declaredResourcesProp()/requestedLocksProp() instead", offenders)
	}
}
