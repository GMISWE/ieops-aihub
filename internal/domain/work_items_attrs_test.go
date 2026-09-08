package domain

// Unit tests for the aihub#288 attrs merge path: the SQL that buildWorkItemUpdate
// compiles, and the validateAttrsPatch guard. These run unconditionally — no
// AIHUB_TEST_DB — because both are pure functions.
//
// The behavioural proof (three keys survive a two-key update, real concurrency,
// deletion, terminal work items) lives in work_items_attrs_db_test.go and is
// DB-gated. What is covered HERE is what that suite cannot see when it SKIPs:
// the shape of the statement, in particular the parenthesisation, which is a
// silent-wrong-answer trap rather than an error.

import (
	"encoding/json"
	"strings"
	"testing"
)

// buildFromJSON compiles the UPDATE for a PATCH body, decoding it the way
// echo's c.Bind does rather than via a struct literal — same reasoning as the
// DB suite, and it keeps the fixtures readable as the wire format callers
// actually send.
func buildFromJSON(t *testing.T, body string) workItemUpdate {
	t.Helper()
	var req UpdateWorkItemRequest
	if err := json.Unmarshal([]byte(body), &req); err != nil {
		t.Fatalf("test fixture is not valid JSON: %v", err)
	}
	return buildWorkItemUpdate(&req, "wi_test")
}

// attrsAssignment returns the single `attrs = ...` assignment out of the SET
// body, failing if there is none or more than one. Postgres rejects a second
// assignment to the same column, so "exactly one" is itself an invariant worth
// asserting rather than grepping the whole query for a substring.
func attrsAssignment(t *testing.T, u workItemUpdate) string {
	t.Helper()
	setBody, _ := splitSetWhere(t, u.Query)
	var found []string
	for _, clause := range strings.Split(setBody, ", ") {
		if strings.HasPrefix(strings.TrimSpace(clause), "attrs =") {
			found = append(found, strings.TrimSpace(clause))
		}
	}
	if len(found) != 1 {
		t.Fatalf("expected exactly one `attrs = ...` assignment, got %d in SET body: %s", len(found), setBody)
	}
	return found[0]
}

// TestBuildWorkItemUpdate_PlainAttrsIsStillWholeColumnReplace locks the
// behaviour that must NOT change. `attrs` alone has to compile to exactly the
// assignment it compiled to before aihub#288 — a bare placeholder, no merge
// operator anywhere near it.
func TestBuildWorkItemUpdate_PlainAttrsIsStillWholeColumnReplace(t *testing.T) {
	u := buildFromJSON(t, `{"attrs":{"a":1}}`)

	if got := attrsAssignment(t, u); got != "attrs = $1" {
		t.Errorf("attrs alone must still be a whole-column REPLACE `attrs = $1`, got %q\nfull query: %s", got, u.Query)
	}
	if strings.Contains(u.Query, "||") {
		t.Errorf("attrs alone must not emit a merge operator: %s", u.Query)
	}
}

// TestBuildWorkItemUpdate_AttrsPatchMergesInsteadOfReplacing is the statement-shape
// half of the reproduction: attrs_patch must read the stored column and merge
// into it, never assign over it.
func TestBuildWorkItemUpdate_AttrsPatchMergesInsteadOfReplacing(t *testing.T) {
	u := buildFromJSON(t, `{"attrs_patch":{"b":2}}`)

	got := attrsAssignment(t, u)
	if got != "attrs = ("+attrsAsObject+" || $1::jsonb)" {
		t.Errorf("attrs_patch must merge into the stored value, got %q\nfull query: %s", got, u.Query)
	}
	if len(u.Args) != 2 { // patch + wiID
		t.Fatalf("expected 2 args (patch, id), got %d: %v", len(u.Args), u.Args)
	}
	patch, ok := u.Args[0].(json.RawMessage)
	if !ok {
		t.Fatalf("arg $1 must be the raw patch JSON, got %T", u.Args[0])
	}
	if string(patch) != `{"b":2}` {
		t.Errorf("arg $1 must be the patch as sent, got %s", patch)
	}
}

// TestBuildWorkItemUpdate_AttrsUnsetDeletesKeys covers the deletion path on its
// own, including the ::text[] cast — without it the parameter type is ambiguous
// and `jsonb - unknown` resolves to the single-key operator, so an array
// argument would fail at execution time rather than delete anything.
func TestBuildWorkItemUpdate_AttrsUnsetDeletesKeys(t *testing.T) {
	u := buildFromJSON(t, `{"attrs_unset":["gone"]}`)

	if got := attrsAssignment(t, u); got != "attrs = ("+attrsAsObject+" - $1::text[])" {
		t.Errorf("attrs_unset must delete keys from the stored value, got %q\nfull query: %s", got, u.Query)
	}
	keys, ok := u.Args[0].([]string)
	if !ok {
		t.Fatalf("arg $1 must be a []string of key names, got %T", u.Args[0])
	}
	if len(keys) != 1 || keys[0] != "gone" {
		t.Errorf("arg $1 must carry the key names as sent, got %v", keys)
	}
}

// TestBuildWorkItemUpdate_MergeThenUnsetIsParenthesised guards a trap that
// produces no error at all, just the wrong answer.
//
// Postgres assigns operator precedence by SPELLING, not by operand type. Binary
// `-` sits at the addition/subtraction level, which binds TIGHTER than `||`
// ("any other operator"). So `attrs || $1::jsonb - $2::text[]` parses as
// `attrs || ($1::jsonb - $2::text[])`: the named keys get stripped out of the
// INCOMING patch, nothing is removed from the stored value, and the statement
// executes happily. Only the parentheses make merge-then-delete mean
// merge-then-delete.
func TestBuildWorkItemUpdate_MergeThenUnsetIsParenthesised(t *testing.T) {
	u := buildFromJSON(t, `{"attrs_patch":{"b":2},"attrs_unset":["gone"]}`)

	got := attrsAssignment(t, u)
	want := "attrs = ((" + attrsAsObject + " || $1::jsonb) - $2::text[])"
	if got != want {
		t.Errorf("merge must be parenthesised before the delete.\n got: %q\nwant: %q\n(unparenthesised, `-` binds tighter than `||` and the delete would be applied to the patch instead of the stored value)", got, want)
	}
	if len(u.Args) != 3 { // patch + unset + wiID
		t.Fatalf("expected 3 args (patch, unset, id), got %d: %v", len(u.Args), u.Args)
	}
	if _, ok := u.Args[0].(json.RawMessage); !ok {
		t.Errorf("$1 must be the patch, got %T", u.Args[0])
	}
	if _, ok := u.Args[1].([]string); !ok {
		t.Errorf("$2 must be the unset key list, got %T", u.Args[1])
	}
}

// TestBuildWorkItemUpdate_NoAttrsFieldsLeavesAttrsAlone is the mutation guard
// for every assertion above: if buildWorkItemUpdate emitted an attrs clause
// unconditionally, the tests that check its shape would still pass while every
// unrelated update silently rewrote the column.
func TestBuildWorkItemUpdate_NoAttrsFieldsLeavesAttrsAlone(t *testing.T) {
	u := buildFromJSON(t, `{"priority":"high"}`)

	setBody, _ := splitSetWhere(t, u.Query)
	if strings.Contains(setBody, "attrs") {
		t.Errorf("an update that mentions no attrs field must not touch the attrs column: %s", setBody)
	}
}

// TestValidateAttrsPatch covers the guard that keeps contradictory and
// malformed payloads away from Postgres. It is a pure function precisely so
// this coverage executes: inline in UpdateWorkItem, deleting the check would
// leave `go test ./...` green because the only behavioural check of it is
// DB-gated.
func TestValidateAttrsPatch(t *testing.T) {
	cases := []struct {
		name    string
		body    string
		wantErr bool
	}{
		{"nothing at all", `{"priority":"high"}`, false},
		{"attrs alone is the untouched REPLACE path", `{"attrs":{"a":1}}`, false},
		{"patch alone", `{"attrs_patch":{"a":1}}`, false},
		{"empty patch object is a legal no-op", `{"attrs_patch":{}}`, false},
		{"unset alone", `{"attrs_unset":["a"]}`, false},
		{"patch and unset together", `{"attrs_patch":{"a":1},"attrs_unset":["b"]}`, false},

		{"attrs with patch is contradictory", `{"attrs":{"a":1},"attrs_patch":{"b":2}}`, true},
		{"attrs with unset is contradictory", `{"attrs":{"a":1},"attrs_unset":["b"]}`, true},
		// Normalised to "absent" rather than rejected — see
		// TestNormalizeAttrsPatch_NullFilledOptionalsAreAbsent.
		{"explicit JSON null patch", `{"attrs_patch":null}`, false},
		{"attrs with a null patch is not a conflict", `{"attrs":{"a":1},"attrs_patch":null}`, false},
		{"attrs with an empty unset is not a conflict", `{"attrs":{"a":1},"attrs_unset":[]}`, false},

		{"array patch", `{"attrs_patch":[1,2]}`, true},
		{"scalar patch", `{"attrs_patch":"nope"}`, true},
		{"number patch", `{"attrs_patch":7}`, true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var req UpdateWorkItemRequest
			if err := json.Unmarshal([]byte(tc.body), &req); err != nil {
				t.Fatalf("test fixture is not valid JSON: %v", err)
			}
			normalizeAttrsPatch(&req)
			aerr := validateAttrsPatch(&req)
			if tc.wantErr && aerr == nil {
				t.Fatalf("expected %s to be rejected, it was accepted", tc.body)
			}
			if !tc.wantErr && aerr != nil {
				t.Fatalf("expected %s to be accepted, got %v", tc.body, aerr)
			}
			if tc.wantErr && aerr.HTTPStatus != 400 {
				t.Errorf("a malformed caller payload must be a 400, got %d", aerr.HTTPStatus)
			}
		})
	}
}

// TestValidateAttrsPatch_BadPatchIsReportedAsABadPatch pins the check ORDER.
// Both rules can fire on the same request; if the combination rule went first, a
// caller who sent a malformed patch alongside attrs would be told they had a
// conflict, sending them to remove the wrong field.
func TestValidateAttrsPatch_BadPatchIsReportedAsABadPatch(t *testing.T) {
	var req UpdateWorkItemRequest
	if err := json.Unmarshal([]byte(`{"attrs":{"a":1},"attrs_patch":[1,2]}`), &req); err != nil {
		t.Fatalf("test fixture is not valid JSON: %v", err)
	}
	normalizeAttrsPatch(&req)
	aerr := validateAttrsPatch(&req)
	if aerr == nil {
		t.Fatal("expected a rejection")
	}
	if !strings.Contains(aerr.Message, "attrs_patch must be a JSON object") {
		t.Errorf("a malformed patch must be reported as a malformed patch, not as a conflict; got: %s", aerr.Message)
	}
}

// TestNormalizeAttrsPatch_NullFilledOptionalsAreAbsent covers the reason
// normalisation exists at all: json.RawMessage keeps the four bytes `null`
// instead of staying nil, so without this a client that null-fills its optional
// parameters would be unable to send a plain `attrs` update at all.
func TestNormalizeAttrsPatch_NullFilledOptionalsAreAbsent(t *testing.T) {
	var req UpdateWorkItemRequest
	if err := json.Unmarshal([]byte(`{"attrs":{"a":1},"attrs_patch":null,"attrs_unset":[]}`), &req); err != nil {
		t.Fatalf("test fixture is not valid JSON: %v", err)
	}
	if req.AttrsPatch == nil {
		t.Fatal("precondition failed: json.RawMessage was expected to keep the literal null — if it now decodes to nil, normalizeAttrsPatch's reason for existing has changed")
	}

	normalizeAttrsPatch(&req)
	if req.AttrsPatch != nil {
		t.Errorf("a literal null patch must normalise to absent, got %s", req.AttrsPatch)
	}
	if req.AttrsUnset != nil {
		t.Errorf("an empty unset list must normalise to absent, got %v", req.AttrsUnset)
	}

	// ...and the resulting statement must be the plain REPLACE, untouched.
	u := buildWorkItemUpdate(&req, "wi_test")
	if got := attrsAssignment(t, u); got != "attrs = $1" {
		t.Errorf("after normalisation this is an ordinary attrs REPLACE, got %q", got)
	}
}

// TestBuildWorkItemUpdate_AttrsMergeComposesWithDeclaredResourcesCAS pins
// placeholder density in the worst case: the merge block shares argIdx with the
// `add` closure, and `resources_version = resources_version + 1` deliberately
// consumes no placeholder. An off-by-one here binds the patch to the wrong
// parameter — which Postgres would report as a type error at execution time, in
// a code path only the DB-gated suite reaches.
func TestBuildWorkItemUpdate_AttrsMergeComposesWithDeclaredResourcesCAS(t *testing.T) {
	u := buildFromJSON(t, `{
		"declared_resources": `+legalResources+`,
		"resources_version": 3,
		"attrs_patch": {"b":2},
		"attrs_unset": ["gone"]
	}`)

	setBody, whereBody := splitSetWhere(t, u.Query)
	if !strings.Contains(setBody, "declared_resources = $1") {
		t.Errorf("declared_resources must take $1: %s", setBody)
	}
	if !strings.Contains(setBody, "resources_version = resources_version + 1") {
		t.Errorf("the version increment must stay computed by Postgres and consume no placeholder: %s", setBody)
	}
	want := "attrs = ((" + attrsAsObject + " || $2::jsonb) - $3::text[])"
	if got := attrsAssignment(t, u); got != want {
		t.Errorf("attrs clause must continue the placeholder run.\n got: %q\nwant: %q", got, want)
	}
	if whereBody != "id = $4 AND resources_version = $5" {
		t.Errorf("WHERE must continue the same dense run, got %q", whereBody)
	}
	if len(u.Args) != 5 {
		t.Fatalf("expected 5 args (resources, patch, unset, id, version), got %d: %v", len(u.Args), u.Args)
	}
}

// TestAttrsPatchShapeErr_NamesWhatArrived is the aihub#420 gate.
//
// The defect it pins is not that a bad attrs_patch is rejected — it always was —
// but that the rejection named only the EXPECTATION. Five materially different
// caller mistakes produced one byte-identical sentence, so the message could not
// tell a caller which of the five they had made. The costly one is the
// JSON-encoded string: a client that serialises a large object argument as a
// string sends something its author correctly calls an object, is told it "must
// be a JSON object", and concludes the real constraint must be size. aihub#420
// was filed on exactly that inference, against a cap that does not exist.
//
// RED on the tree before this change: every want below is absent from the old
// message, which is the constant "attrs_patch must be a JSON object".
func TestAttrsPatchShapeErr_NamesWhatArrived(t *testing.T) {
	cases := []struct {
		name     string
		patch    string
		wantKind string
		wantIn   []string
	}{
		{
			name:     "array",
			patch:    `[1,2]`,
			wantKind: "a JSON array",
			wantIn:   []string{"got a JSON array of 5 bytes"},
		},
		{
			name:     "number",
			patch:    `7`,
			wantKind: "a JSON number",
			wantIn:   []string{"got a JSON number of 1 bytes"},
		},
		{
			name:     "boolean",
			patch:    `true`,
			wantKind: "a JSON boolean",
			wantIn:   []string{"got a JSON boolean of 4 bytes"},
		},
		{
			name:     "plain string that is not an encoded object",
			patch:    `"nope"`,
			wantKind: "a JSON string",
			wantIn:   []string{"got a JSON string of 6 bytes"},
		},
		{
			// The case that produced aihub#420. The caller's data is fine; their
			// serialisation is not, and only this line can tell them so.
			name:     "string that decodes to an object",
			patch:    `"{\"a\":1}"`,
			wantKind: "a JSON string",
			wantIn: []string{
				"got a JSON string of 11 bytes",
				"that string decodes to a JSON object",
				"not a JSON-encoded string of it",
			},
		},
		{
			// Starts like an object but does not parse. Reported as malformed
			// rather than as "a JSON object", which would be self-contradictory
			// in a message explaining that an object was required.
			name:     "malformed",
			patch:    `{"a":`,
			wantKind: "malformed JSON",
			wantIn:   []string{"got malformed JSON of 5 bytes"},
		},
	}

	seen := map[string]string{}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var req UpdateWorkItemRequest
			// json.Unmarshal cannot carry the malformed fixture through a full
			// request body, so the raw field is set directly — c.Bind would have
			// rejected the envelope first, but AttrsPatch is json.RawMessage and
			// the guard is what is under test here.
			req.AttrsPatch = json.RawMessage(tc.patch)
			aerr := validateAttrsPatch(&req)
			if aerr == nil {
				t.Fatalf("%s must be rejected", tc.patch)
			}
			if aerr.HTTPStatus != 400 {
				t.Errorf("a malformed caller payload must be a 400, got %d", aerr.HTTPStatus)
			}
			for _, want := range tc.wantIn {
				if !strings.Contains(aerr.Message, want) {
					t.Errorf("message must name what arrived.\n  want substring: %q\n  got message:    %q", want, aerr.Message)
				}
			}
			// The clause that stops the phantom cap being rediscovered. Measured
			// 2026-09-07: 199,983 bytes accepted over HTTP, 8,016 bytes accepted
			// through the MCP tool path. See jsonObjectParamErr's comment
			// (attrsPatchShapeErr until aihub#465 generalised it to all four
			// jsonb object parameters).
			if !strings.Contains(aerr.Message, "attrs_patch has no length cap") {
				t.Errorf("every shape rejection must rule size out explicitly; got: %q", aerr.Message)
			}

			details, ok := aerr.Details.(map[string]any)
			if !ok {
				t.Fatalf("details must be machine-readable so a caller need not parse prose; got %T", aerr.Details)
			}
			if details["field"] != "attrs_patch" {
				t.Errorf("details.field = %v, want attrs_patch", details["field"])
			}
			if details["got"] != tc.wantKind {
				t.Errorf("details.got = %v, want %v", details["got"], tc.wantKind)
			}
			if details["bytes"] != len(tc.patch) {
				t.Errorf("details.bytes = %v, want %d", details["bytes"], len(tc.patch))
			}

			// The defect in one assertion: the five kinds used to be
			// indistinguishable. If any two messages collide again, the
			// information this change adds has been lost even if every
			// substring above still matches.
			if prev, dup := seen[aerr.Message]; dup {
				t.Errorf("message is not distinguishable from the %s case: %q", prev, aerr.Message)
			}
			seen[aerr.Message] = tc.name
		})
	}
}

// TestValidateAttrsPatch_NoSizeCap pins the measurement that falsified
// aihub#420's premise: attrs_patch is not length-limited. Measured 2026-09-07
// against the live server at 3,983 / 7,483 / 15,983 / 64,983 / 199,983 bytes,
// every one HTTP 200, plus 8,016 bytes through the full MCP tool path.
//
// It is a guard against the fix for that work item being "helpfully" completed
// later by someone adding the cap the shipped message promises does not exist.
// The message and the behaviour would then disagree, and only a test like this
// one would say so.
//
// ⚠️ WHAT THIS ARM COVERS IS THE DOMAIN VALIDATOR AND NOTHING ELSE, and aihub#454
// narrowed the wording to say so. It used to open with "there is no length limit
// on attrs_patch anywhere in the request path" while calling exactly one
// function, and its failure message repeated the claim. A cap added anywhere but
// here would have left it green while making jsonObjectParamErr's shipped
// "attrs_patch has no length cap" — which IS a statement about the whole path —
// quietly false. A gate whose self-description is wider than its reach reports
// the reach it describes, not the one it has.
//
// Widening the claim to match the code is not available from inside this
// package: a request-path arm needs the transport. It lives in
// internal/server/attrs_patch_no_size_cap_test.go, which pushes a 200KB
// attrs_patch through the real router and checks the answer is the ordinary auth
// refusal rather than a size refusal.
//
// Between the two, the covered layers are the domain validator and the transport
// ahead of authentication. Two of the three places aihub#420's review named stay
// UNCOVERED, and they are written down here rather than implied away: a length
// check added in the MCP tool layer, and a CHECK constraint added on the column.
// Either would falsify the shipped message with every test in this tree green.
func TestValidateAttrsPatch_NoSizeCap(t *testing.T) {
	for _, n := range []int{4 << 10, 8 << 10, 200 << 10} {
		body, err := json.Marshal(map[string]string{"k": strings.Repeat("x", n)})
		if err != nil {
			t.Fatalf("building the fixture failed: %v", err)
		}
		var req UpdateWorkItemRequest
		req.AttrsPatch = body
		if aerr := validateAttrsPatch(&req); aerr != nil {
			t.Errorf("validateAttrsPatch rejected a %d-byte attrs_patch object. THIS LAYER imposes no "+
				"size cap — and if that changed deliberately, jsonObjectParamErr's shipped "+
				"%q is now false and has to change in the same commit. Got: %v",
				len(body), "attrs_patch has no length cap", aerr)
		}
	}
}
