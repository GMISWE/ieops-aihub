package mcp

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/GMISWE/ieops-aihub/internal/domain"
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
// `unrecognized_resources` must reach the caller. Reporting at claim is the ONLY
// remedy available on the stored-data path — rejecting there would make
// historical mistyped work items unclaimable. Filtered out, the entire remedy is
// inert and the caller sees `{attempt_id, claim_epoch, ok:true,
// acquired_locks:[]}`, byte-identical to the pre-fix output that made a lockless
// wi look guarded.
//
// ─── Rewritten for aihub#388, and note WHY the old form had to go ───────────
//
// This used to regex `tools_lifecycle.go` for the claim handler's passthrough
// WHITELIST literal and check that the key was inside it. aihub#388 replaced that
// whitelist with a delete-list (claim_response_slim.go), because a keep-list had
// silently dropped four other ClaimResponse fields — so the literal this guard
// looked for no longer exists.
//
// 🔴 Credit where it is due: the old guard did NOT go quietly green when its
// target vanished. Its `t.Fatal("could not locate the claim passthrough
// whitelist — update this guard")` is the reason this rewrite happened at all,
// and it is the behaviour every source-scanning tripwire should have — a
// selector that matches nothing must FAIL, not pass.
//
// The invariant is now structural rather than textual: under a delete-list the
// key reaches the caller unless somebody names it, so the check is whether it is
// named. The anti-vacuity clause matters more than it looks: without it, EMPTYING
// claimResponseWithheldKeys would satisfy "unrecognized_resources is not
// withheld" while also leaking the session secret this projection exists to hold
// back — a green guard over the worst possible state.
//
// The behavioural half of this contract, through the real registered tool, is
// TestClaimResultCarriesEveryClaimResponseField in
// claim_response_projection_test.go; `unrecognized_resources` is one of the
// fields it censuses.
func TestClaimPassesThroughUnrecognizedResources(t *testing.T) {
	if len(claimResponseWithheldKeys) == 0 {
		t.Fatal("claimResponseWithheldKeys is empty — this guard would pass for every key, " +
			"including session_secret, which is the one key that must never be forwarded")
	}
	if _, ok := claimResponseWithheldKeys["session_secret"]; !ok {
		t.Fatal("claimResponseWithheldKeys no longer names session_secret — the projection's " +
			"whole reason for existing is gone, so a passing 'not withheld' check below proves nothing")
	}

	if reason, withheld := claimResponseWithheldKeys["unrecognized_resources"]; withheld {
		t.Errorf("the claim projection withholds \"unrecognized_resources\" (reason given: %q), so the "+
			"silent-no-lock warning never reaches the caller and the aihub#238 remedy is inert. "+
			"That key is the ONLY signal that a declared resource is holding no lock.", reason)
	}

	// And on the projection itself, so the check is not purely about a map entry:
	// a real value must survive.
	got := slimClaimResult(map[string]any{
		"attempt_id":             "ra_guard",
		"unrecognized_resources": []any{"file:internal/mcp/mistyped.go"},
	})
	if _, ok := got["unrecognized_resources"]; !ok {
		t.Errorf("slimClaimResult dropped \"unrecognized_resources\" even though it is not in the "+
			"delete-list: %v", got)
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

// ─── aihub#395: the four declared_resources contract residues ───────────────

// declaredResourcesItemsSchema renders the shared prop once so every assertion
// below reads the SAME schema the four tools publish (declaredResourcesProp is
// used by pf_create_work_item, pf_batch_create_work_items, pf_update_work_item
// and pf_predict_conflicts). One definition is the point: a per-tool copy is how
// two of these tools would end up describing different contracts.
func declaredResourcesItemsSchema(t *testing.T) map[string]any {
	t.Helper()
	schema := objectSchema(map[string]any{
		"declared_resources": declaredResourcesProp("Declared resource locks"),
	}, nil)
	return itemsSchemaFor(t, schema, "declared_resources")
}

func itemPropDescription(t *testing.T, items map[string]any, field string) (string, bool) {
	t.Helper()
	props, ok := items["properties"].(map[string]any)
	if !ok {
		t.Fatalf("items schema has no properties block: %v", items)
	}
	p, ok := props[field].(map[string]any)
	if !ok {
		return "", false
	}
	desc, _ := p["description"].(string)
	return desc, true
}

// TestDeclaredResourcesProp_DoesNotPublishBaseBranch is the aihub#395 part 2
// gate, and it FAILS on the pre-fix tree.
//
// `base_branch` was published as "Base branch (repo entries only)" and read by
// nothing: on a8ad8c0 the only non-test occurrences were the struct field and
// the decoder that fills it. No lock key, conflict rule, query or worktree base
// touched it — the worktree base is origin/main, hard-coded in addClaimWorktree
// — so setting it produced a claim off origin/main with no error and no warning.
//
// ⚠️ The struct field and the decoder are deliberately still there, so a guard
// written as "does domain bind it" would be green either way. What has to be
// asserted is that no CALLER is invited to set it, which is a property of the
// published schema and of nothing else.
func TestDeclaredResourcesProp_DoesNotPublishBaseBranch(t *testing.T) {
	items := declaredResourcesItemsSchema(t)
	props, _ := items["properties"].(map[string]any)
	if len(props) == 0 {
		t.Fatal("items schema publishes no properties at all — every assertion here would be vacuous")
	}
	if _, published := props["base_branch"]; published {
		desc, _ := itemPropDescription(t, items, "base_branch")
		t.Errorf("declared_resources entries still publish `base_branch` (%q), and nothing reads "+
			"it: no lock key, conflict rule, query or worktree base. A caller who sets it gets a "+
			"claim off origin/main with no error at any hop — aihub#395 part 2. Withdraw it from "+
			"the schema, or make something honour it.", desc)
	}
	// Anti-vacuity, and it is load-bearing: an items schema that lost its
	// properties block would satisfy the assertion above while withdrawing the
	// whole entry shape. `task_branch` is the neighbour that IS still read (it is
	// the fallback lock-key source aihub#356 left in place), so its presence
	// proves this test is looking at a populated schema.
	if _, ok := props["task_branch"]; !ok {
		t.Error("`task_branch` is not published either — this test can no longer tell " +
			"\"base_branch was withdrawn\" from \"the entry shape is empty\"")
	}
}

// TestDeclaredResourcesProp_QualifiesReadIntent is the aihub#395 part 1 gate.
//
// The description promised, unqualified, that intent:"read" "takes no write
// lock". The server drops the lock only when `lockType == "file_scope" &&
// res.Intent == "read"` (derivedLock), so a repo entry still takes git_branch
// and a service entry still takes deploy_env. The decision was to make the
// contract honest rather than widen the behaviour, so what is gated is the
// description — and the assertion is on the QUALIFICATION, not on a phrase:
// the text must name the types the exemption applies to.
func TestDeclaredResourcesProp_QualifiesReadIntent(t *testing.T) {
	items := declaredResourcesItemsSchema(t)
	desc, ok := itemPropDescription(t, items, "intent")
	if !ok {
		t.Fatal("items schema does not describe `intent`")
	}
	for _, want := range []string{"path", "document", "section"} {
		if !strings.Contains(desc, want) {
			t.Errorf("the intent description does not name %q among the types where read is "+
				"honoured — it reads as an unqualified promise, which is aihub#395 part 1: "+
				"derivedLock drops the lock for file_scope only. Got: %q", want, desc)
		}
	}
	for _, want := range []string{"repo", "service"} {
		if !strings.Contains(desc, want) {
			t.Errorf("the intent description does not say what read does on a %q entry (it still "+
				"takes its lock). Got: %q", want, desc)
		}
	}
}

// TestDeclaredResourcesProp_SaysExternalRefTakesNoLock is the aihub#395 part 3
// gate. external_ref is offered in the type enum of a field described as
// "Declared resource locks", derives no lock, and is exempt from the no-uri
// warning — the only entry that can be declared and produce no signal at all.
func TestDeclaredResourcesProp_SaysExternalRefTakesNoLock(t *testing.T) {
	items := declaredResourcesItemsSchema(t)
	desc, ok := itemPropDescription(t, items, "type")
	if !ok {
		t.Fatal("items schema does not describe `type`")
	}
	if !strings.Contains(desc, "external_ref") {
		t.Errorf("the type description does not mention external_ref at all, so nothing tells a "+
			"caller that the one lockless type is lockless. Got: %q", desc)
	}
	if !strings.Contains(strings.ToLower(desc), "no lock") {
		t.Errorf("the type description does not say external_ref takes NO lock. Got: %q", desc)
	}
	// ⚠️ The other half of this — that the claim is TRUE — cannot be asserted from
	// here: resourceToLock is unexported and adding an exported test-only shim to
	// production code to reach it would be a worse trade than the coverage is
	// worth. It is pinned where the function lives, by
	// TestValidateDeclaredResources_ExternalRefAcceptedThoughItTakesNoLock in
	// internal/domain, which fails if external_ref ever starts deriving a lock.
	// Stated rather than left implicit: if that test is ever deleted, this one
	// alone would keep asserting a sentence nobody checks.
}

// TestDeclaredResourcesProp_PublishesTheEnforcedURISchemes is the aihub#395
// part 4 gate on the PUBLISHED half.
//
// The schemes were prose here and enforced nowhere. Now the sentence is
// generated from the same table ValidateDeclaredResources applies, so this
// asserts the generation is actually wired — a hand-copied sentence that happens
// to agree today is exactly what drifted.
func TestDeclaredResourcesProp_PublishesTheEnforcedURISchemes(t *testing.T) {
	items := declaredResourcesItemsSchema(t)
	desc, ok := itemPropDescription(t, items, "uri")
	if !ok {
		t.Fatal("items schema does not describe `uri`")
	}
	generated := domain.DeclaredResourceURISchemeDoc()
	if generated == "" {
		t.Fatal("domain.DeclaredResourceURISchemeDoc() is empty — the assertion below would be vacuous")
	}
	if !strings.Contains(desc, generated) {
		t.Errorf("the published uri description does not carry the generated scheme sentence, so "+
			"it is a hand-written copy that can drift from the validator.\nwant substring: %q\ngot: %q",
			generated, desc)
	}
	// A published contract that does not say it is enforced trains the reader to
	// treat it as advice, which is how aihub#395 part 4 survived two prior wis.
	if !strings.Contains(desc, "400") {
		t.Errorf("the uri description does not say a wrong scheme is rejected. Got: %q", desc)
	}
}
