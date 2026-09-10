package mcp_test

// aihub#543 probe wave 2, lane L5 — `docs/mcp-cards/pf_emit_event.md`'s hop 2-3,
// the two claims about what leaves this process, plus the client half of hop
// 0-1's "every other string is accepted":
//
//	"`pinned` and `admin` are forwarded only when true, so \"explicitly false\"
//	 and \"unset\" are the same on the wire"
//	    -> TestEmitEventForwardsPinnedAndAdminOnlyWhenTrue
//	"`payload` is forwarded as given"
//	    -> TestEmitEventForwardsThePayloadAsGiven
//	"`event_type` is still a free string … every other string is accepted"
//	    -> TestEmitEventTypeIsNotRefusedInProcess (the client half; the column's
//	       half is internal/domain's TestMigration0036… subtest and the Go
//	       refusal's half is TestEmitEventNullWorkItem_IsA400NotA500's)
//
// 🔴 Every verdict here comes from a request a fake aihub RECEIVED, not from
// calling the body builder: pf_emit_event has no extracted body function at all
// (the map is a literal inside the handler closure), and the omission direction
// is the one G1 structurally cannot see. TestContractEveryPublishedParamLeavesTheProcess
// forces each published parameter to a truthy probe value and asserts it ARRIVES;
// a handler that forwarded `pinned` unconditionally would satisfy it exactly as
// well as this one does, which is the aihub#452 blind spot recorded on the
// pf_claim_work_item card.
//
// No database:
//
//	GOWORK=off go test ./internal/mcp/ -run 'TestEmitEvent(Forwards|TypeIsNot)' -count=1

import (
	"encoding/json"
	"net/http"
	"sort"
	"testing"
	"time"
)

// emitEventWirePath is the only route pf_emit_event POSTs, and therefore the
// only one the body assertions below may read.
const emitEventWirePath = "/v1/events"

// driveEmitEvent runs one real pf_emit_event against a fake aihub in an
// isolated workspace and returns the body the server received.
//
// The FLOOR lives here rather than in each test, because two of the assertions
// below are about keys that must be ABSENT and an absent key cannot be told
// apart from a body that carried nothing at all. So the body is first required
// to carry the two keys this hop always renders — `event_type` and the injected
// `attempt_id` — and a body missing either fails here instead of silently
// satisfying an absence assertion.
func driveEmitEvent(t *testing.T, wiID string, args map[string]any) map[string]any {
	t.Helper()

	f := newFakeAihub(t)
	f.on(emitEventWirePath, func(map[string]any) (int, any) {
		return http.StatusOK, map[string]any{"event_id": "evt_probe543"}
	})

	args["work_item_id"] = wiID
	result, isErr := callToolBounded(t, f, "pf_emit_event", args, 20*time.Second)
	if isErr {
		t.Fatalf("pf_emit_event failed: %v — a refused call reaches the server with a body "+
			"nobody should draw conclusions from", result)
	}

	body := lastBodyFor(t, f, emitEventWirePath)
	if s, _ := body["event_type"].(string); s == "" {
		t.Fatalf("the event body carries no event_type (keys: %v) — the walk is broken and the "+
			"absence assertions below would pass against a body like this", sortedBodyKeys(body))
	}
	if s, _ := body["attempt_id"].(string); s == "" {
		t.Fatalf("the event body carries no attempt_id (keys: %v) — the credentials are injected "+
			"by this hop, so a body without them is not the body these tests are about",
			sortedBodyKeys(body))
	}
	return body
}

// TestEmitEventForwardsPinnedAndAdminOnlyWhenTrue pins the card's "`pinned` and
// `admin` are forwarded only when true, so \"explicitly false\" and \"unset\"
// are the same on the wire".
//
// Three arms, because the claim has three observable halves and no two of them
// fail together: true ARRIVES, explicit false is ABSENT, and omitted is ABSENT.
// The middle arm is the whole sentence — a caller who writes `pinned: false`
// gets a request byte-identical to one who wrote nothing, which is a fact about
// the wire that no reading of the schema can produce.
//
// ⚠️ And the two absences are asserted as an EXACT key set rather than as two
// `_, ok :=` misses. A check for "pinned is not there" stays green when a third
// optional key starts riding along on the same branch; the card's hop 2-3 is a
// statement about the whole body, so the whole body is what is compared.
//
// MUTANTS (applied to this tree; the verdict is what ran):
//
//	M1  enforcement: forward `pinned` unconditionally
//	    (body["pinned"] = boolArg(args, "pinned"))  RED  the false and omitted arms
//	M2  enforcement: forward `admin` unconditionally
//	                                                RED  the false and omitted arms
//	M3  publication: delete the whole citation clause from the card sentence
//	                                                RED  K12 — the sentence lands
//	                                                     back in the debt column and
//	                                                     the pf_emit_event ledger
//	                                                     row stops matching
//
//	── recorded GREEN, because it is a limit worth knowing ──
//	M3a publication: delete only the `Test…` SYMBOL from that sentence, leaving
//	    the backticked `*_test.go` path behind
//	                                                GREEN a citation is either
//	                                                      anchor form, so a sentence
//	                                                      naming the file stays
//	                                                      cited. No ledger movement
//	                                                      isolates one anchor of a
//	                                                      citation, or one arm of a
//	                                                      sentence that names
//	                                                      several — which is why M3
//	                                                      deletes the clause
func TestEmitEventForwardsPinnedAndAdminOnlyWhenTrue(t *testing.T) {
	const wiID = "wi_emit_flags"

	// The keys this hop renders on every call, whatever the flags say. Named
	// once so the two absence arms compare a whole body rather than probing for
	// the key they already expect to be missing.
	always := []string{"attempt_id", "claim_epoch", "event_type", "payload", "session_secret", "work_item_id"}

	t.Run("true arrives", func(t *testing.T) {
		seedStateFile(t, wiID)
		body := driveEmitEvent(t, wiID, map[string]any{
			"event_type": "note", "payload": map[string]any{"probe": "543"},
			"pinned": true, "admin": true,
		})
		if body["pinned"] != true {
			t.Errorf("pinned=true did not arrive as true: %#v (keys %v)", body["pinned"], sortedBodyKeys(body))
		}
		if body["admin"] != true {
			t.Errorf("admin=true did not arrive as true: %#v (keys %v)", body["admin"], sortedBodyKeys(body))
		}
	})

	for _, tc := range []struct {
		name string
		args map[string]any
	}{
		{
			// The sentence's own words: "explicitly false" and "unset" are the
			// same on the wire.
			name: "explicit false is absent",
			args: map[string]any{
				"event_type": "note", "payload": map[string]any{"probe": "543"},
				"pinned": false, "admin": false,
			},
		},
		{
			name: "omitted is absent",
			args: map[string]any{
				"event_type": "note", "payload": map[string]any{"probe": "543"},
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			seedStateFile(t, wiID)
			body := driveEmitEvent(t, wiID, tc.args)

			got := sortedBodyKeys(body)
			want := append([]string(nil), always...)
			sort.Strings(want)
			if len(got) != len(want) {
				t.Fatalf("the body carries keys %v, want exactly %v — a flag the caller did not "+
					"set to true must not appear at all, or \"false\" and \"unset\" stop being "+
					"the same request", got, want)
			}
			for i := range want {
				if got[i] != want[i] {
					t.Fatalf("the body carries keys %v, want exactly %v", got, want)
				}
			}
		})
	}
}

// TestEmitEventForwardsThePayloadAsGiven pins the card's "`payload` is forwarded
// as given".
//
// "As given" is a claim about BYTES, so it is asserted as bytes: the argument is
// re-marshalled and compared to the re-marshalled body value, which is what
// makes a normalisation, a re-key or a wrapper red. A shape check ("it is still
// an object") would go green on all three.
//
// The fixture is deliberately awkward — a nested object, an array, a number that
// is not an integer, an empty string and a null — because those are the values a
// hand-rolled copy loses. An `{"probe": "543"}` fixture cannot tell a verbatim
// forward from a re-encode.
//
// MUTANTS (applied to this tree; the verdict is what ran):
//
//	M4  enforcement: wrap the payload
//	    (body["payload"] = map[string]any{"payload": args["payload"]})
//	                                                RED
//	M5  enforcement: forward only the payload's top-level string values
//	                                                RED
//	M6  publication: delete the whole citation clause from the card sentence
//	                                                RED  K12
func TestEmitEventForwardsThePayloadAsGiven(t *testing.T) {
	const wiID = "wi_emit_payload"
	seedStateFile(t, wiID)

	payload := map[string]any{
		"nested":   map[string]any{"deep": []any{"a", "b"}},
		"fraction": 1.5,
		"empty":    "",
		"absent":   nil,
		"flag":     false,
	}
	want, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal the fixture: %v", err)
	}

	body := driveEmitEvent(t, wiID, map[string]any{"event_type": "note", "payload": payload})

	got, err := json.Marshal(body["payload"])
	if err != nil {
		t.Fatalf("marshal the payload the server received: %v", err)
	}
	if string(got) != string(want) {
		t.Errorf("payload arrived as %s, want %s — \"forwarded as given\" is a claim about the "+
			"bytes, and every difference here is a value the caller believes was stored and was not",
			got, want)
	}
}

// TestEmitEventTypeIsNotRefusedInProcess is the client half of the card's
// "`event_type` is still a free string and there is still no CHECK behind it …
// every other string is accepted", and it is GREEN by design — it is not
// evidence FOR anything, it is what stops the published 45-name vocabulary from
// being mistaken for a guard.
//
// The shape is TestCreateUserVocabEnumDoesNotRefuseInProcess's, for the same
// reason: polyforge registers through the untyped (*mcp.Server).AddTool method,
// whose callTool path invokes the handler with no schema step, so a published
// list constrains the CLIENT and not this process. If a future SDK upgrade
// starts validating untyped tools this goes red, and the card sentence and
// hop-4's "publishing 45 names under `enum` would state a closed contract
// nothing keeps" get corrected with it — which is the only way a claim about
// somebody else's code stays true.
//
// The other two halves of the same sentence are elsewhere and neither is here:
// that the COLUMN accepts the string is internal/domain's
// TestMigration0036_AdmitsAdminGcManualWithNoWorkItem, and that no Go refusal
// consults the vocabulary is TestEmitEventNullWorkItem_IsA400NotA500 — a request
// carrying a work item and an off-vocabulary type is stored.
//
// MUTANTS (applied to this tree; the verdict is what ran):
//
//	M7  enforcement: refuse an event_type outside domain.EventVocabulary in the
//	    pf_emit_event handler                       RED  no request is made at all
//	M8  publication: delete every citation from the card sentence this arm helps
//	    retire                                      RED  K12
//
//	── recorded GREEN ──
//	M8a publication: delete only THIS arm's citation from that sentence, leaving
//	    the two others it names                     GREEN the sentence stays cited.
//	                                                      A sentence resting on
//	                                                      three arms cannot lose one
//	                                                      of them visibly, which is
//	                                                      the same limit wave 1
//	                                                      recorded on pf_update_step
func TestEmitEventTypeIsNotRefusedInProcess(t *testing.T) {
	const wiID = "wi_emit_freestring"
	seedStateFile(t, wiID)

	// Not a random string: `artifact_action` is the retired type hop 2-3 says a
	// caller may still send through this tool, and `definitely_not_an_event_type`
	// is the off-every-list value the domain arms use. The first is in the
	// vocabulary with no publisher, the second is in nothing at all, so a guard
	// keyed on either list is red on one of them.
	for _, eventType := range []string{"artifact_action", "definitely_not_an_event_type"} {
		t.Run(eventType, func(t *testing.T) {
			body := driveEmitEvent(t, wiID, map[string]any{
				"event_type": eventType, "payload": map[string]any{"probe": "543"},
			})
			if got := body["event_type"]; got != eventType {
				t.Errorf("event_type reached the POST body as %#v, want %q — the published "+
					"vocabulary neither refuses nor rewrites it in this process", got, eventType)
			}
		})
	}
}
