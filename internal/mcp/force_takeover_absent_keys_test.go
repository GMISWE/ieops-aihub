package mcp_test

// aihub#543 probe wave 1 — the two `docs/mcp-cards/pf_force_takeover.md` hop-5
// sentences about a key that is NOT there:
//
//	"Under a delete-list, `expires_at` needs no entry: v1.21 removed it, the
//	 response does not carry it, and naming it here would be the same rot that
//	 left it in the old CLAIM keep-list."
//	    -> TestTakeoverResponseHasNoExpiresAtToWithhold
//	"It is one line per `declared_resources` entry the lock mapper cannot
//	 understand — the same producer and key `pf_claim_work_item` carries — and it
//	 is `omitempty`, so a healthy work item never sees it."
//	    -> TestTheUnrecognizedResourcesReportIsOneKeyAndOneLinePerEntry
//
// 🔴 WHY K10 CANNOT HOLD EITHER. K10 is a ONE-directional ratchet on keys a live
// response DOES carry: it refuses an undeclared key. A key nothing emits is
// invisible to it by construction, so "the response does not carry `expires_at`"
// and "a healthy work item never sees `unrecognized_resources`" are both outside
// its reach — and they are the two sentences on this card that a reader is most
// likely to act on, because they tell a caller which keys never to wait for.
//
// The `omitempty` half is the sharper of the two: a projection that forwards by
// default plus a struct tag that drops an empty slice is a key whose ABSENCE is
// the healthy answer, and there is no live response in which to observe it.
//
// No database:
//
//	GOWORK=off go test ./internal/mcp/ -run 'TestTakeoverResponseHasNoExpiresAt|TestTheUnrecognizedResourcesReport' -count=1

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/GMISWE/ieops-aihub/internal/domain"
)

// jsonFieldOf returns the struct field carrying the given json NAME, plus the
// tag's remaining options, by reflection over the type.
//
// By name rather than by Go field name: the card's sentence is about the wire
// key, and a rename on the Go side that kept the tag would be invisible to a
// caller and must be invisible here too.
func jsonFieldOf(typ reflect.Type, name string) (reflect.StructField, []string, bool) {
	for i := 0; i < typ.NumField(); i++ {
		f := typ.Field(i)
		tag, _, _ := strings.Cut(f.Tag.Get("json"), ",")
		if tag != name {
			continue
		}
		_, opts, _ := strings.Cut(f.Tag.Get("json"), ",")
		return f, strings.Split(opts, ","), true
	}
	return reflect.StructField{}, nil, false
}

// TestTakeoverResponseHasNoExpiresAtToWithhold pins the card's "`expires_at`
// needs no entry".
//
// Two claims, and the second does not follow from the first for a reader: the
// response type has no such field (so the server cannot send one), AND the
// projection carries no delete-list entry for it (so nobody has "maintained" a
// field that does not exist). The second is asserted the way the delete-list's
// own shape arm asserts its subject — by PLANTING the key in the server's answer
// and watching it arrive. An entry in the delete-list would eat it.
//
// The third arm is the floor, and it is what keeps the second from being green
// for the wrong reason: with the projection deleted entirely, a planted key
// arrives too. So the same driven call plants the one key that MUST be dropped.
//
// MUTANTS (run against this tree; the verdict is what happened, not what was
// expected):
//
//	M11 enforcement: add an `expires_at` line to forceTakeoverWithheldKeys in
//	    internal/mcp/force_takeover_response_slim.go — the rot the card names
//	                                            RED  the_projection_carries_no_entry_for_it
//	M12 enforcement: add `ExpiresAt string \`json:"expires_at"\`` to
//	    domain.ForceTakeoverResponse            RED  the_response_type_has_no_such_field
//	M13 enforcement: replace the slimForceTakeoverResult call with the raw result
//	                                            RED  the floor (session_secret reaches
//	                                                 the model)
//	M14 publication: delete the sentence from the card
//	                                            RED  K12 — the candidate and its
//	                                                 citation leave together
func TestTakeoverResponseHasNoExpiresAtToWithhold(t *testing.T) {
	t.Run("the_response_type_has_no_such_field", func(t *testing.T) {
		fields := forceTakeoverResponseJSONFields(t)
		if _, present := fields["new_attempt_id"]; !present {
			t.Fatalf("domain.ForceTakeoverResponse has no new_attempt_id json field (it has %v) "+
				"— the reflection is reading something other than the response type, and the "+
				"absence below would be an artefact of that", sortedFieldNames(fields))
		}
		if ft, present := fields["expires_at"]; present {
			t.Errorf("domain.ForceTakeoverResponse declares expires_at (%s). v1.21 made this an "+
				"ownership-only response; a lease expiry on it is a promise nothing renews, and "+
				"the card tells callers the key is not there.", ft)
		}
	})

	// Planted rather than declared: the point of a delete-list is that a key the
	// process knows nothing about is forwarded, so the only way to observe "no
	// entry deletes this" is to send one.
	payload := fullForceTakeoverResponse(t)
	const plantedExpiry = "2026-09-10T00:00:00Z"
	const plantedSecret = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	payload["expires_at"] = plantedExpiry
	payload["session_secret"] = plantedSecret
	result := takeoverAgainstFullResponse(t, payload)

	t.Run("the_floor_the_projection_really_is_running", func(t *testing.T) {
		if v, present := result["session_secret"]; present {
			t.Fatalf("session_secret=%#v reached the model. The projection is not running at "+
				"all, so the arm below would report \"no entry for expires_at\" about a "+
				"projection that has no entries for anything.", v)
		}
	})

	t.Run("the_projection_carries_no_entry_for_it", func(t *testing.T) {
		if got, present := result["expires_at"]; !present || got != plantedExpiry {
			t.Errorf("a planted expires_at=%q came back as %#v (present=%v). The card says this "+
				"key needs NO delete-list entry, and a projection that drops it is maintaining a "+
				"field the response does not have — the same rot that left expires_at in the old "+
				"claim keep-list, restored on the route the card holds up as the fixed one.",
				plantedExpiry, got, present)
		}
	})
}

func sortedFieldNames(fields map[string]reflect.Type) []string {
	out := make([]string, 0, len(fields))
	for k := range fields {
		out = append(out, k)
	}
	return out
}

// unrecognizedResourceCarriers are every response type that carries the aihub#509
// report. Quantified over all three rather than over the pair the card's sentence
// names, because "the same key `pf_claim_work_item` carries" is an equality and a
// two-name list is satisfied by the third drifting: pf_acquire_locks carries the
// same report from the same producer, and a rename there would leave two of three
// agreeing and the convention broken.
var unrecognizedResourceCarriers = map[string]reflect.Type{
	"pf_claim_work_item": reflect.TypeOf(domain.ClaimResponse{}),
	"pf_force_takeover":  reflect.TypeOf(domain.ForceTakeoverResponse{}),
	"pf_acquire_locks":   reflect.TypeOf(domain.AcquireLocksResponse{}),
}

// TestTheUnrecognizedResourcesReportIsOneKeyAndOneLinePerEntry pins the three
// claims the card's `unrecognized_resources` sentence makes.
//
// "The same producer" is held next door and by name —
// internal/domain/declared_resources_wiring_test.go
// (TestTakeoverAndAcquireLocksReportUnrecognizedResources) scans both call sites
// for the UnrecognizedDeclaredResources call and the struct fill. What no arm
// held is the other three: that the KEY is the same on every response that
// carries it, that it is `omitempty`, and that the report is one line per
// unmappable entry rather than one line per payload.
//
// The cardinality arm is where the value is. "It is one line per entry" is the
// difference between a caller being told which declaration to fix and being told
// that something, somewhere, did not map — and a summary line would satisfy
// every existing assertion, all of which check that the report is non-empty.
//
// MUTANTS:
//
//	M15 enforcement: drop `,omitempty` from ForceTakeoverResponse's tag
//	                                            RED  omitempty/pf_force_takeover
//	M16 enforcement: rename the tag to `unmapped_resources` on that one struct
//	                                            RED  the_key_is_the_same_on_every_carrier
//	M17 enforcement: make UnrecognizedDeclaredResources return a single summary
//	    line instead of one per entry           RED  one_line_per_unmappable_entry
//	M18 enforcement: make it report every entry, mappable or not
//	                                            RED  one_line_per_unmappable_entry's
//	                                                 count, and the floor names the
//	                                                 mappable entry it invented a
//	                                                 line for
//	M19 publication: delete the sentence from the card
//	                                            RED  K12 — candidate and citation
//	                                                 leave together
//	G2  control:     reword the card sentence without touching its citation
//	                                          GREEN  this arm reads the structs and
//	                                                 the producer, not the card, so
//	                                                 K12 owns its publication side
func TestTheUnrecognizedResourcesReportIsOneKeyAndOneLinePerEntry(t *testing.T) {
	const key = "unrecognized_resources"

	t.Run("the_key_is_the_same_on_every_carrier", func(t *testing.T) {
		if len(unrecognizedResourceCarriers) < 2 {
			t.Fatal("fewer than two carriers to compare — an equality over one is not one")
		}
		for tool, typ := range unrecognizedResourceCarriers {
			f, _, ok := jsonFieldOf(typ, key)
			if !ok {
				t.Errorf("%s's response type %s declares no %q field. aihub#509 put the same "+
					"report on every response that re-derives locks from stored declarations; "+
					"a carrier that spells it differently is a second convention, and the card "+
					"promises one.", tool, typ.Name(), key)
				continue
			}
			if f.Type.Kind() != reflect.Slice || f.Type.Elem().Kind() != reflect.String {
				t.Errorf("%s's %s.%s is %s, want a []string — \"one line per entry\" is a "+
					"property of the shape as much as of the producer", tool, typ.Name(), key, f.Type)
			}
		}
	})

	t.Run("omitempty", func(t *testing.T) {
		for tool, typ := range unrecognizedResourceCarriers {
			t.Run(tool, func(t *testing.T) {
				_, opts, ok := jsonFieldOf(typ, key)
				if !ok {
					t.Skipf("%s carries no %q field; the arm above reports that", tool, key)
				}
				omit := false
				for _, o := range opts {
					if o == "omitempty" {
						omit = true
					}
				}
				if !omit {
					t.Errorf("%s.%s is not `omitempty`, so a healthy work item is handed "+
						"%q: [] — an empty warning list reads to a model as a warning it did not "+
						"understand, which is why the card promises the key is simply absent",
						typ.Name(), key, key)
				}
				// The tag in effect, not merely present: a marshaller configured to
				// keep zero values would leave the tag true and the promise false.
				blob, err := json.Marshal(reflect.New(typ).Interface())
				if err != nil {
					t.Fatalf("marshal a zero %s: %v", typ.Name(), err)
				}
				var decoded map[string]any
				if err := json.Unmarshal(blob, &decoded); err != nil {
					t.Fatalf("decode a zero %s: %v", typ.Name(), err)
				}
				if v, present := decoded[key]; present {
					t.Errorf("a zero %s serialises %q as %#v; the card says a healthy work item "+
						"never sees the key at all", typ.Name(), key, v)
				}
			})
		}
	})

	t.Run("one_line_per_unmappable_entry", func(t *testing.T) {
		// Two entries the mapper understands and three it does not: an unknown
		// type, a known type with no `uri`, and a second unknown type. Written as
		// the stored wire JSON, because that is what both call sites hand the
		// producer.
		const stored = `[
			{"type":"path","uri":"file:internal/mcp/tools_lifecycle.go","repo":"aihub"},
			{"type":"deploy_env","uri":"env:prod"},
			{"type":"service"},
			{"type":"repo","uri":"repo:aihub"},
			{"type":"cluster","uri":"k8s:tot"}
		]`
		lines := domain.UnrecognizedDeclaredResources(json.RawMessage(stored))
		if len(lines) != 3 {
			t.Fatalf("the report is %d line(s) for a payload with 3 unmappable entries:\n  %s\n"+
				"The card tells a caller the report is one line per entry, which is what lets "+
				"them fix the declaration named rather than audit the whole payload.",
				len(lines), strings.Join(lines, "\n  "))
		}
		// The FLOOR, and the direction a widened producer breaks: the two entries
		// the mapper does understand must contribute nothing. A producer reporting
		// every entry would also hit "3" on some payload, and this is what tells
		// the two apart.
		for _, l := range lines {
			for _, mappable := range []string{"tools_lifecycle.go", "repo:aihub"} {
				if strings.Contains(l, mappable) {
					t.Errorf("the report names %q, which the mapper DOES understand:\n  %s\n"+
						"Reporting a mappable entry turns the list into noise a caller learns to "+
						"skip, which is the same silence as not reporting at all", mappable, l)
				}
			}
		}
		// Each line names its own index, which is the part that makes it
		// actionable: two declarations can differ only in position.
		for i, want := range []string{"[1]", "[2]", "[4]"} {
			if !strings.Contains(lines[i], want) {
				t.Errorf("line %d is %q and does not name the declared_resources index %s",
					i, lines[i], want)
			}
		}
	})
}
