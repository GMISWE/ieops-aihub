package domain

// Unit tests for the aihub#465 shape guard over the four caller-supplied jsonb
// object parameters — `attrs`, `attrs_patch`, `payload`, `structured_payload`.
//
// Before it, only `attrs_patch` checked its shape. The other three bound to a
// bare json.RawMessage and the bytes went straight into the column, so a
// JSON-encoded STRING of an object was stored verbatim under an HTTP 200 and the
// column came back holding a string where every reader expects an object.
// Measured live four times (aihub#456), and misdiagnosed once before that as an
// escaping bug in the caller (aihub#284's own attrs still record the wrong
// cause).
//
// These run unconditionally — no AIHUB_TEST_DB — and that is the point rather
// than a convenience. Three of the five guarded call sites are exercised
// through their real domain function with a NIL pool, which works because the
// guard sits above the first query: the request is rejected before anything
// would be read. That placement is itself part of the fix (a check below the
// first query answers 500 with the driver's text in it, per aihub#433), so a
// future refactor that moves the guard down turns these into a nil-pointer
// panic rather than a silent pass. The remaining two sites are pure functions
// extracted for the reason mem_I98xpPgY gives: a check written inline in a
// pool-taking function can only be covered by the DB-gated suite, which SKIPs
// everywhere but its own CI step, so deleting it would leave `go test ./...`
// entirely green.
//
// ⚠️ What these tests deliberately do NOT assert with strings.Contains: the
// malformed-string message embeds encoding/json's parse error, which is derived
// from CALLER bytes. A Contains check for any phrase inside the message could
// therefore be satisfied by a fixture that merely quotes that phrase. Every
// assertion here is anchored — HasPrefix from byte 0, HasSuffix to the last
// byte, or an exact match on a details key — so no fixture can forge a pass.

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// encodedObjectString is the shape aihub#456 measured: an object the caller
// meant, arriving as a JSON string of itself. Valid JSON inside the quotes.
func encodedObjectString(t *testing.T) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(`{"investigation_2026_09_08":{"finding":"stringified"},"n":3}`)
	if err != nil {
		t.Fatalf("building the fixture failed: %v", err)
	}
	return raw
}

// braceShortString is the shape 18 of the 19 real payloads have: hand-escaped
// JSON that is one closing brace short. NOT valid JSON inside the quotes, which
// is exactly why aihub#420's "that string decodes to a JSON object" clause had
// never reached a caller.
func braceShortString(t *testing.T) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(`{"investigation_2026_09_08":{"finding":"stringified"`)
	if err != nil {
		t.Fatalf("building the fixture failed: %v", err)
	}
	return raw
}

// asAihubErr narrows the `error` the domain entry points return. A non-AihubError
// here means the request reached something other than the guard.
func asAihubErr(t *testing.T, err error) *AihubError {
	t.Helper()
	if err == nil {
		return nil
	}
	aerr, ok := err.(*AihubError)
	if !ok {
		t.Fatalf("the guard must answer an AihubError the handler can map to a 400; got %T: %v", err, err)
	}
	return aerr
}

// rejectViaEntryPoint runs a pool-taking domain entry point with a nil pool and
// turns the nil-pointer panic a MISSING guard produces into a legible failure.
//
// Without it a deleted guard aborts the whole test binary, so the one arm that
// broke masks every other result in the package — "all red" and "the runner
// died" then look identical, which is the failure mode that makes a mutation
// sweep unreadable. A panic here means exactly one thing and the message says
// it: the request reached the database instead of being rejected on its shape.
func rejectViaEntryPoint(t *testing.T, call func() error) (aerr *AihubError) {
	t.Helper()
	defer func() {
		if r := recover(); r != nil {
			t.Errorf("the guard must reject before the first query; this call reached the database instead (nil-pool panic: %v)", r)
			aerr = nil
		}
	}()
	return asAihubErr(t, call())
}

// guardedSite is one (published parameter, domain entry point) pair. `field` is
// the name the rejection must use — the name the CALLER typed, not the Go field.
type guardedSite struct {
	name  string
	field string
	// reject invokes the real entry point and returns the shape rejection.
	reject func(t *testing.T, raw json.RawMessage) *AihubError
	// poolFree is true when the entry point can also be called with a VALID
	// object and no pool, i.e. it is a pure validator. The three that are not
	// go on to do real work, so only their rejection path is reachable here.
	poolFree bool
}

func guardedSites() []guardedSite {
	return []guardedSite{
		{
			name:     "pf_update_work_item attrs",
			field:    "attrs",
			poolFree: true,
			reject: func(t *testing.T, raw json.RawMessage) *AihubError {
				t.Helper()
				return validateAttrsPatch(&UpdateWorkItemRequest{Attrs: raw})
			},
		},
		{
			name:     "pf_update_work_item attrs_patch",
			field:    "attrs_patch",
			poolFree: true,
			reject: func(t *testing.T, raw json.RawMessage) *AihubError {
				t.Helper()
				return validateAttrsPatch(&UpdateWorkItemRequest{AttrsPatch: raw})
			},
		},
		{
			name:  "pf_create_work_item attrs",
			field: "attrs",
			reject: func(t *testing.T, raw json.RawMessage) *AihubError {
				t.Helper()
				// Goal and project are the only fields required above the guard;
				// priority/source/labels are defaulted by CreateWorkItem itself.
				return rejectViaEntryPoint(t, func() error {
					_, aerr := CreateWorkItem(context.Background(), nil,
						&CreateWorkItemRequest{Goal: "probe", Project: "aihub", Attrs: raw},
						"u_test", "tester", map[string]string{"aihub": "writer"}, "user")
					if aerr == nil {
						return nil
					}
					return aerr
				})
			},
		},
		{
			name:     "pf_remember attrs",
			field:    "attrs",
			poolFree: true,
			reject: func(t *testing.T, raw json.RawMessage) *AihubError {
				t.Helper()
				return validateRememberJSONParams(&RememberRequest{Attrs: raw})
			},
		},
		{
			name:     "pf_save_artifact structured_payload",
			field:    "structured_payload",
			poolFree: true,
			reject: func(t *testing.T, raw json.RawMessage) *AihubError {
				t.Helper()
				return validateRememberJSONParams(&RememberRequest{StructuredPayload: raw})
			},
		},
		{
			name:  "pf_update_memory attrs",
			field: "attrs",
			reject: func(t *testing.T, raw json.RawMessage) *AihubError {
				t.Helper()
				return rejectViaEntryPoint(t, func() error {
					_, err := UpdateMemory(context.Background(), nil, "mem_probe",
						&UpdateMemoryRequest{Attrs: raw})
					return err
				})
			},
		},
		{
			name:  "pf_emit_event payload",
			field: "payload",
			reject: func(t *testing.T, raw json.RawMessage) *AihubError {
				t.Helper()
				// No work_item_id/attempt_id, so credential verification (the
				// first thing that would touch the pool) is skipped.
				return rejectViaEntryPoint(t, func() error {
					_, err := EmitEvent(context.Background(), nil,
						&EmitEventRequest{EventType: "note", Payload: raw},
						"u_test", "tester", "user")
					return err
				})
			},
		},
	}
}

// TestStringifiedObjectParamIsRejected is the work item in one table: two
// probes per parameter — a JSON string whose contents ARE a valid object, and
// one that is a closing brace short — across every guarded call site. On the
// pre-fix build every one of these returned nil and the value was stored.
func TestStringifiedObjectParamIsRejected(t *testing.T) {
	fixtures := []struct {
		name           string
		raw            func(*testing.T) json.RawMessage
		wantDecodesTo  string
		wantDecodeErr  bool
		wantClauseHead string
	}{
		{
			name:           "string that decodes to an object",
			raw:            encodedObjectString,
			wantDecodesTo:  "a JSON object",
			wantClauseHead: ", and that string decodes to a JSON object — send the object itself, not a JSON-encoded string of it",
		},
		{
			name:          "string one closing brace short",
			raw:           braceShortString,
			wantDecodesTo: "malformed JSON",
			wantDecodeErr: true,
			// The aihub#465 half: the message the 18-of-19 population actually
			// receives. Truncated after the opening paren because the parse
			// error itself is asserted through details, not through prose.
			wantClauseHead: ", and that string opens like a JSON object but does not parse (",
		},
	}

	for _, site := range guardedSites() {
		for _, fx := range fixtures {
			t.Run(site.name+"/"+fx.name, func(t *testing.T) {
				raw := fx.raw(t)
				aerr := site.reject(t, raw)
				if aerr == nil {
					t.Fatalf("a stringified %s must be rejected, not stored", site.field)
				}
				if aerr.HTTPStatus != 400 {
					t.Errorf("a caller shape mistake must be a 400, got %d", aerr.HTTPStatus)
				}

				// Anchored at byte 0: field name, observed type, byte length,
				// then the kind-specific clause. Nothing caller-controlled
				// precedes any of it.
				head := fmt.Sprintf("%s must be a JSON object; got a JSON string of %d bytes%s",
					site.field, len(raw), fx.wantClauseHead)
				if !strings.HasPrefix(aerr.Message, head) {
					t.Errorf("message must open by naming the field, the type and the size.\n  want prefix: %q\n  got:         %q", head, aerr.Message)
				}

				// Anchored at the last byte. The size sentence is per-field
				// because it is false for payload as attrs_patch words it.
				wantTail := ". Size is never the reason for this rejection: " + jsonObjectParamSizeNote[site.field]
				if !strings.HasSuffix(aerr.Message, wantTail) {
					t.Errorf("message must close by ruling size out in this field's own terms.\n  want suffix: %q\n  got:         %q", wantTail, aerr.Message)
				}

				details, ok := aerr.Details.(map[string]any)
				if !ok {
					t.Fatalf("details must be machine-readable so an automated caller need not parse prose; got %T", aerr.Details)
				}
				if details["field"] != site.field {
					t.Errorf("details.field = %v, want %q", details["field"], site.field)
				}
				if details["got"] != "a JSON string" {
					t.Errorf("details.got = %v, want %q", details["got"], "a JSON string")
				}
				if details["bytes"] != len(raw) {
					t.Errorf("details.bytes = %v, want %d", details["bytes"], len(raw))
				}
				// The acceptance criterion "says which it was": valid-JSON-inside
				// and malformed-inside must be distinguishable WITHOUT reading
				// the sentence.
				if details["string_decodes_to"] != fx.wantDecodesTo {
					t.Errorf("details.string_decodes_to = %v, want %q", details["string_decodes_to"], fx.wantDecodesTo)
				}
				decodeErr, hasDecodeErr := details["string_decode_error"].(string)
				if fx.wantDecodeErr && (!hasDecodeErr || decodeErr == "") {
					t.Errorf("a malformed inner string must report WHERE it broke; details = %v", details)
				}
				if !fx.wantDecodeErr && hasDecodeErr {
					t.Errorf("a well-formed inner string must not carry a parse error; got %q", decodeErr)
				}
			})
		}
	}
}

// TestStringifiedObjectParamMessagesStayDistinguishable is the defect in one
// assertion. Four fields times two inner kinds is eight materially different
// caller mistakes; if any two produce the same sentence, the information this
// change adds has been lost even though every anchored assertion above still
// passes.
func TestStringifiedObjectParamMessagesStayDistinguishable(t *testing.T) {
	seen := map[string]string{}
	for field := range jsonObjectParamSizeNote {
		for _, fx := range []struct {
			kind string
			raw  func(*testing.T) json.RawMessage
		}{
			{"decodes to an object", encodedObjectString},
			{"one brace short", braceShortString},
		} {
			aerr := jsonObjectParamErr(field, fx.raw(t))
			label := field + "/" + fx.kind
			if prev, dup := seen[aerr.Message]; dup {
				t.Errorf("%s is indistinguishable from %s: %q", label, prev, aerr.Message)
			}
			seen[aerr.Message] = label
		}
	}
	if len(seen) != 2*len(jsonObjectParamSizeNote) {
		t.Errorf("expected one distinct message per (field, inner kind); got %d for %d fields", len(seen), len(jsonObjectParamSizeNote))
	}
}

// TestRealObjectsAreUntouched is the other half of the acceptance criterion:
// the object path must not move by a byte. aihub#456 measured a 6,634-byte
// object travelling the full MCP path unchanged and answering 200, so that exact
// size is a fixture here rather than a round number.
//
// The three sites whose entry point goes on to do real work are absent from the
// acceptance direction on purpose: calling them with a valid object would run
// the whole create/update, which needs a database. What holds for them is that
// the guard is the ONLY statement added to each, and it is this predicate.
func TestRealObjectsAreUntouched(t *testing.T) {
	small, err := json.Marshal(map[string]any{"a": 1, "nested": map[string]string{"b": "c"}})
	if err != nil {
		t.Fatalf("building the fixture failed: %v", err)
	}
	measured, err := json.Marshal(map[string]string{"k": strings.Repeat("x", 6626)})
	if err != nil {
		t.Fatalf("building the fixture failed: %v", err)
	}
	if len(measured) != 6634 {
		t.Fatalf("the aihub#456 fixture must stay the size that was measured: got %d bytes, want 6634", len(measured))
	}

	objects := []json.RawMessage{
		json.RawMessage(`{}`),
		small,
		measured,
	}
	for _, field := range []string{"attrs", "attrs_patch", "structured_payload", "payload"} {
		for _, obj := range objects {
			if aerr := validateJSONObjectParam(field, obj); aerr != nil {
				t.Errorf("a %d-byte %s OBJECT must still be accepted; got: %v", len(obj), field, aerr.Message)
			}
		}
		// Absent and literal-null keep the meaning each field already gave
		// them: this guard changes the shape face, not the null semantics.
		if aerr := validateJSONObjectParam(field, nil); aerr != nil {
			t.Errorf("an absent %s must stay absent, not become a 400; got: %v", field, aerr.Message)
		}
		if aerr := validateJSONObjectParam(field, json.RawMessage(`null`)); aerr != nil {
			t.Errorf("a literal null %s must keep its existing meaning; got: %v", field, aerr.Message)
		}
	}

	for _, site := range guardedSites() {
		if !site.poolFree {
			continue
		}
		for _, obj := range objects {
			if aerr := site.reject(t, obj); aerr != nil {
				t.Errorf("%s: a %d-byte object must reach the column unchanged; got: %v", site.name, len(obj), aerr.Message)
			}
		}
	}
}

// TestJSONObjectParamNoLengthCapPreserved guards the three fields whose
// rejection promises no length cap against someone later adding the cap the
// message says does not exist. attrs_patch's own version of this lives in
// work_items_attrs_test.go (aihub#420); this extends it to the two fields
// aihub#465 brought under the same message.
//
// `payload` is excluded BECAUSE it is capped, at 64KB in EmitEvent, which is
// also why its size sentence is worded differently.
func TestJSONObjectParamNoLengthCapPreserved(t *testing.T) {
	for _, field := range []string{"attrs", "structured_payload"} {
		for _, n := range []int{4 << 10, 8 << 10, 200 << 10} {
			body, err := json.Marshal(map[string]string{"k": strings.Repeat("x", n)})
			if err != nil {
				t.Fatalf("building the fixture failed: %v", err)
			}
			if aerr := validateJSONObjectParam(field, body); aerr != nil {
				t.Errorf("a %d-byte %s object must be accepted; %s has no size cap, got: %v", len(body), field, field, aerr.Message)
			}
		}
	}
}

// TestBadAttrsIsReportedAsBadAttrsNotAsAConflict pins the ordering aihub#465
// chose inside validateAttrsPatch, which is a decision and not an accident.
//
// A request carrying a stringified `attrs` AND an `attrs_patch` has two faults
// at once. The shape is reported, not the combination, by symmetry with
// TestValidateAttrsPatch_BadPatchIsReportedAsABadPatch: a caller told they have
// a conflict goes and removes the wrong field, and the shape fault is the one
// that is actually actionable from the message. Reordering the two checks flips
// which error the caller sees, silently, so it needs an assertion.
func TestBadAttrsIsReportedAsBadAttrsNotAsAConflict(t *testing.T) {
	stringified, err := json.Marshal(`{"a":1}`)
	if err != nil {
		t.Fatalf("building the fixture failed: %v", err)
	}
	req := &UpdateWorkItemRequest{
		Attrs:      stringified,
		AttrsPatch: json.RawMessage(`{"b":2}`),
	}
	aerr := validateAttrsPatch(req)
	if aerr == nil {
		t.Fatal("a stringified attrs must be rejected even when attrs_patch is well-formed")
	}
	details, ok := aerr.Details.(map[string]any)
	if !ok || details["field"] != "attrs" {
		t.Errorf("the rejection must name the field whose SHAPE is wrong; got message %q details %v", aerr.Message, aerr.Details)
	}

	// The mirror case is already covered for attrs_patch; asserted here too so
	// the pair reads as one rule rather than two coincidences.
	req = &UpdateWorkItemRequest{
		Attrs:      json.RawMessage(`{"a":1}`),
		AttrsPatch: json.RawMessage(`[1,2]`),
	}
	aerr = validateAttrsPatch(req)
	if aerr == nil {
		t.Fatal("a non-object attrs_patch must be rejected even when attrs is well-formed")
	}
	details, ok = aerr.Details.(map[string]any)
	if !ok || details["field"] != "attrs_patch" {
		t.Errorf("the rejection must name the field whose SHAPE is wrong; got message %q details %v", aerr.Message, aerr.Details)
	}
}

// TestSizeNoteMatchesTheCapThatActuallyExists is the one assertion the rest of
// this file cannot make: every other test compares the message against
// jsonObjectParamSizeNote, so changing the table changes both sides and a note
// that is simply UNTRUE would go unnoticed. This one checks each note against
// the behaviour it describes.
//
// It exists because the first draft of this fix copied attrs_patch's
// unconditional "has no length cap" onto all four fields, which is false for
// `payload`: EmitEvent caps it at 64 KB. Publishing a constraint the server does
// not keep and denying one it does are the same defect class — both make the
// caller believe something false.
func TestSizeNoteMatchesTheCapThatActuallyExists(t *testing.T) {
	// payload IS capped, and the cap is checked before the shape guard, so this
	// call rejects without needing a pool.
	oversized, err := json.Marshal(map[string]string{"k": strings.Repeat("x", 70<<10)})
	if err != nil {
		t.Fatalf("building the fixture failed: %v", err)
	}
	if len(oversized) <= 65536 {
		t.Fatalf("the fixture must exceed EmitEvent's 64KB cap; got %d bytes", len(oversized))
	}
	emitErr := rejectViaEntryPoint(t, func() error {
		_, err := EmitEvent(context.Background(), nil,
			&EmitEventRequest{EventType: "note", Payload: oversized},
			"u_test", "tester", "user")
		return err
	})
	if emitErr == nil {
		t.Error("payload's size note says the 64KB cap exists and was checked first; nothing rejected a 70KB payload")
	}
	if strings.Contains(jsonObjectParamSizeNote["payload"], "no length cap") {
		t.Errorf("payload IS capped, so its note must not claim otherwise; got %q", jsonObjectParamSizeNote["payload"])
	}

	// The other three are not capped, and their notes say so — checked against
	// the same 200 KB acceptance TestJSONObjectParamNoLengthCapPreserved uses.
	for _, field := range []string{"attrs", "attrs_patch", "structured_payload"} {
		if !strings.Contains(jsonObjectParamSizeNote[field], "no length cap") {
			t.Errorf("%s has no cap anywhere in the request path; its note must say so, got %q", field, jsonObjectParamSizeNote[field])
		}
		big, mErr := json.Marshal(map[string]string{"k": strings.Repeat("x", 200<<10)})
		if mErr != nil {
			t.Fatalf("building the fixture failed: %v", mErr)
		}
		if aerr := validateJSONObjectParam(field, big); aerr != nil {
			t.Errorf("%s's note promises no cap, but a %d-byte object was rejected: %v", field, len(big), aerr.Message)
		}
	}
}

// TestRememberAttrsProvenanceSplit pins both directions of the split
// mem_X8JDSC96 prescribes, because each direction fails in a way the other
// cannot see.
//
// Reject direction: caller-supplied bytes stop at the point of the mistake.
// Accept direction: the same bytes read back out of the column do NOT, or
// pf_update_memory would answer 400 for every memory whose attrs is already a
// string — rows written before this guard existed, whose only repair is an
// edit. That second arm looks like a hole in the validation and is the reason
// the validation can ship at all, so a later "tightening" that removes it must
// go red here.
func TestRememberAttrsProvenanceSplit(t *testing.T) {
	stringified := encodedObjectString(t)

	if aerr := validateRememberJSONParams(&RememberRequest{Attrs: stringified}); aerr == nil {
		t.Error("caller-supplied attrs must be hard-rejected at the point of the mistake")
	}

	fromRow := &RememberRequest{Attrs: stringified, attrsFromStoredRow: true}
	if aerr := validateRememberJSONParams(fromRow); aerr != nil {
		t.Errorf("attrs read back out of the column must never fail the caller's edit; got: %v", aerr.Message)
	}

	// The exemption is scoped to attrs. A structured_payload on the same request
	// came from the wire whatever the attrs provenance is, so it stays checked —
	// otherwise the flag would be a general escape hatch rather than a
	// provenance statement.
	both := &RememberRequest{Attrs: stringified, StructuredPayload: stringified, attrsFromStoredRow: true}
	aerr := validateRememberJSONParams(both)
	if aerr == nil {
		t.Fatal("attrsFromStoredRow must exempt attrs ONLY; structured_payload is always caller-supplied")
	}
	details, ok := aerr.Details.(map[string]any)
	if !ok || details["field"] != "structured_payload" {
		t.Errorf("the rejection must name structured_payload, not attrs; got details %v", aerr.Details)
	}
}

// TestResolveUpdateMemoryAttrsReportsProvenance covers the half of the split
// TestRememberAttrsProvenanceSplit cannot see: that half proves the guard
// HONOURS the flag, this one proves UpdateMemory SETS it. Both are needed —
// honouring a flag nobody sets protects nothing, and neither failure shows up in
// the other's assertions.
func TestResolveUpdateMemoryAttrsReportsProvenance(t *testing.T) {
	caller := json.RawMessage(`{"from":"caller"}`)
	stored := json.RawMessage(`{"from":"row"}`)

	got, fromRow := resolveUpdateMemoryAttrs(caller, stored)
	if string(got) != string(caller) || fromRow {
		t.Errorf("caller-supplied attrs must win and must NOT be marked stored; got %s fromRow=%v", got, fromRow)
	}

	got, fromRow = resolveUpdateMemoryAttrs(nil, stored)
	if string(got) != string(stored) || !fromRow {
		t.Errorf("inherited attrs must be marked stored, or a pre-existing string attrs makes the row uneditable; got %s fromRow=%v", got, fromRow)
	}

	// Empty-but-non-nil is the same "not supplied" as nil: echo binds an omitted
	// field to a zero-length RawMessage, not always to nil.
	got, fromRow = resolveUpdateMemoryAttrs(json.RawMessage{}, stored)
	if string(got) != string(stored) || !fromRow {
		t.Errorf("a zero-length attrs is 'not supplied', not an empty override; got %s fromRow=%v", got, fromRow)
	}
}

// TestJSONObjectParamSizeNoteCoversEveryGuardedField closes the loop between
// the guard and the sentence it emits: every field name passed to the guard must
// have a size note, or the rejection silently drops its closing sentence and the
// caller is back to suspecting a length limit — the exact wrong conclusion
// aihub#420 exists to prevent.
//
// It reads the package's own source rather than a hand-kept list, because a
// hand-kept list is the thing that gets forgotten. ⚠️ Its blind spot is
// deliberate and narrow: it only sees a field name written as a STRING LITERAL
// at the call. A call passing a variable is invisible to it, so the closure is
// a ratchet over the normal case, not a proof.
func TestJSONObjectParamSizeNoteCoversEveryGuardedField(t *testing.T) {
	sources, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatalf("cannot list the package source: %v", err)
	}
	callRe := regexp.MustCompile(`(?:validateJSONObjectParam|jsonObjectParamErr)\("([a-z_]+)"`)

	found := map[string]bool{}
	scanned := 0
	for _, src := range sources {
		if strings.HasSuffix(src, "_test.go") {
			continue
		}
		body, rErr := os.ReadFile(src)
		if rErr != nil {
			t.Fatalf("cannot read %s: %v", src, rErr)
		}
		scanned++
		for _, m := range callRe.FindAllStringSubmatch(string(body), -1) {
			found[m[1]] = true
		}
	}
	if scanned == 0 {
		t.Fatal("scanned no production source — the glob is relative to the package directory and must match")
	}
	if len(found) == 0 {
		t.Fatalf("found no guarded field in %d production files; either the guard was deleted or this pattern no longer matches its call shape", scanned)
	}

	var missing []string
	for field := range found {
		if jsonObjectParamSizeNote[field] == "" {
			missing = append(missing, field)
		}
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		t.Errorf("these guarded fields have no entry in jsonObjectParamSizeNote, so their rejection ships with no size sentence: %v", missing)
	}

	// The reverse direction: a note for a field nothing guards is dead prose
	// that reads like an enforced contract.
	var unused []string
	for field := range jsonObjectParamSizeNote {
		if !found[field] {
			unused = append(unused, field)
		}
	}
	sort.Strings(unused)
	if len(unused) > 0 {
		t.Errorf("jsonObjectParamSizeNote describes fields no call site guards: %v", unused)
	}
}
