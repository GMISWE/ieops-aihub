package mcp_test

// aihub#543 probe wave 2 — three hop-0/hop-2 claims of the pf_read_events card
// that no arm read.
//
//	TestReadEventsRefusesACallNamingNeitherWorkItemNorProject
//	  The card's opening sentence is "eight parameters, none required — though the
//	  handler refuses a call carrying neither `work_item_id` nor `project`". Both
//	  halves were unheld. readEventsWireProbes covers the eight names one at a
//	  time and always sends work_item_id alongside, precisely so it never reaches
//	  this guard; and nothing read the schema's `required` list.
//
//	TestPublishedReadEventsCutoverCaveatNamesTheThreeLockEventsAndSaysDeploy
//	  The card says the cutover caveat rides on the TOOL DESCRIPTION rather than
//	  only in the design doc, and that it says *deploy* rather than *commit*
//	  deliberately. That is a claim about a string on the wire, and the three
//	  event names in it are checked against the domain vocabulary so a rename on
//	  one side alone is red.
//
//	TestReadEventsOmitsPinnedFirstWhenItIsFalse
//	  "`pinned_first` is forwarded only when true." The wire probe table beside
//	  this file sends true and asserts it arrives; nothing sent false, so
//	  forwarding it unconditionally — which makes "not specified" and "explicitly
//	  false" indistinguishable on the wire — was green.
//
//	GOWORK=off go test ./internal/mcp/ -run 'TestReadEvents|TestPublishedReadEvents' -count=1 -v

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/GMISWE/ieops-aihub/internal/domain"
)

// readEventsPublishedParams is the schema pf_read_events publishes, and the
// whole of it.
//
// Written down rather than counted, because the card's claim is "eight
// parameters, NONE required" and a count alone is satisfied by any eight. The
// names are what a caller reads.
var readEventsPublishedParams = []string{
	"work_item_id", "project", "user_id", "types",
	"cursor", "since", "limit", "pinned_first",
}

// lockCutoverEventTypes are the three types the description's caveat is about.
//
// Their absence before one particular deploy is the fact the caveat exists to
// state, so an arm about the caveat has to name them — and it checks each
// against domain.EventVocabulary rather than trusting this list, because a type
// renamed in the domain and left in the description is a warning about a name
// nothing emits.
var lockCutoverEventTypes = []string{"lock_acquired", "lock_released", "wi_resources_updated"}

// TestReadEventsRefusesACallNamingNeitherWorkItemNorProject holds both halves of
// the card's opening sentence.
//
// ⚠️ The refusal is asserted with a request COUNT, not only a status. GET
// /v1/events with neither parameter answers 400 at the handler too, so "the call
// came back an error" is true whether or not this process checked — and the card
// says the tool refuses, which is a statement about this hop.
//
// ─── Recorded mutants ──────────────────────────────────────────────────────
//
// Every one was applied to this tree and the verdict is what RAN.
//
//	── enforcement side (the handler, the schema untouched) ──
//	M9  delete the `if wiID == "" && project == ""` guard in tools_events.go
//	                                        RED  one request reached the server
//	M10 keep the guard but drop `project` from its condition
//	                                        RED  the project-only control arm
//	                                             was refused, which is the
//	                                             over-reach direction
//
//	── publication side (the schema, the handler untouched) ──
//	M11 add "work_item_id" to readEventsSchema's required list
//	                                        RED  the card says none of the eight
//	                                             is required, and a required
//	                                             parameter the guard does not
//	                                             insist on is a promise the
//	                                             behaviour contradicts
//	M12 drop `pinned_first` from readEventsSchema's properties
//	                                        RED  the published set stopped being
//	                                             the eight the card names, on the
//	                                             count check and on the by-name
//	                                             check both
func TestReadEventsRefusesACallNamingNeitherWorkItemNorProject(t *testing.T) {
	// Half one: the published schema. Eight properties, and no required list.
	tool := publishedTool(t, "pf_read_events")
	raw, err := json.Marshal(tool.InputSchema)
	if err != nil {
		t.Fatalf("marshal pf_read_events InputSchema: %v", err)
	}
	var schema struct {
		Required   []string       `json:"required"`
		Properties map[string]any `json:"properties"`
	}
	if err := json.Unmarshal(raw, &schema); err != nil {
		t.Fatalf("pf_read_events InputSchema is not valid JSON: %v", err)
	}
	if len(schema.Required) != 0 {
		t.Errorf("pf_read_events declares required=%v. The card says none of its eight "+
			"parameters is required, and the handler's own rule is a DISJUNCTION — either "+
			"`work_item_id` or `project` — which a `required` list cannot express. Declaring "+
			"one of them required promises a refusal the handler does not make for the other.",
			schema.Required)
	}
	for _, name := range readEventsPublishedParams {
		if _, ok := schema.Properties[name]; !ok {
			t.Errorf("pf_read_events no longer publishes %q, which the card counts among its "+
				"eight parameters. Published: %v", name, objectKeysSorted(schema.Properties))
		}
	}
	if len(schema.Properties) != len(readEventsPublishedParams) {
		t.Errorf("pf_read_events publishes %d parameter(s); the card says %d and names them "+
			"%v. Published: %v", len(schema.Properties), len(readEventsPublishedParams),
			readEventsPublishedParams, objectKeysSorted(schema.Properties))
	}

	// Half two: the refusal, and that it costs no round trip.
	t.Run("neither identifier is refused locally", func(t *testing.T) {
		q := newQueryRecorder(t)
		res := callToolAgainstRecorderResult(t, q, "pf_read_events", map[string]any{})
		if !res.IsError {
			t.Fatalf("a call naming neither work_item_id nor project was accepted: %s",
				toolResultText(t, res))
		}
		if n := q.count(); n != 0 {
			t.Errorf("the refusal cost %d HTTP request(s). None of the eight parameters is "+
				"required, so this disjunction is the ONLY thing standing between a caller and "+
				"an unscoped read; the card says this hop refuses, and a refusal that travels "+
				"to the server is a different claim.", n)
		}
		msg := toolResultText(t, res)
		for _, must := range []string{"work_item_id", "project"} {
			if !strings.Contains(msg, must) {
				t.Errorf("the refusal does not name %q. Either parameter satisfies the rule, so "+
					"a message naming one of them sends the caller to the wrong fix; got %q", must, msg)
			}
		}
	})

	// CONTROL, both directions. Without these, "refuse every call" satisfies
	// the arm above — and that is not a hypothetical over-reach: the guard is a
	// disjunction, so dropping either operand refuses one legal shape.
	for _, tc := range []struct {
		name string
		args map[string]any
	}{
		{"work_item_id alone", map[string]any{"work_item_id": "wi_readEventsProbe"}},
		{"project alone", map[string]any{"project": "aihub"}},
	} {
		t.Run(tc.name+" is accepted", func(t *testing.T) {
			q := newQueryRecorder(t)
			q.respondWith(map[string]any{"events": []any{}})
			res := callToolAgainstRecorderResult(t, q, "pf_read_events", tc.args)
			if res.IsError {
				t.Fatalf("pf_read_events(%v) was refused: %s. Either identifier satisfies the "+
					"rule; refusing one of them is the over-reach direction, and the arm above "+
					"cannot see it.", tc.args, toolResultText(t, res))
			}
			if q.count() != 1 {
				t.Errorf("an accepted call made %d request(s), want 1", q.count())
			}
		})
	}
}

// TestPublishedReadEventsCutoverCaveatNamesTheThreeLockEventsAndSaysDeploy holds
// the card's claim that the caveat is ON THE WIRE.
//
// 🔴 The required words are not a quote of the sentence. A substring assertion
// against the whole description is satisfied by nothing except that exact
// wording, so it goes red on a rewrite that keeps every promise and the author's
// cheapest repair is deleting the assertion. What is required is that the string
// still names the three types, still says *deploy*, and still says there is no
// backfill — which is the claim the card makes about it.
//
// ─── Recorded mutants ──────────────────────────────────────────────────────
//
//	── publication side (the description, the vocabulary untouched) ──
//	M13 cut the whole "NOTE: lock_acquired / ..." clause
//	                                        RED  the caveat is back to living
//	                                             only in the design doc
//	M14 rewrite "the deploy that shipped aihub#343" as "aihub#343"
//	                                        RED  the word the card calls
//	                                             deliberate is gone, and a reader
//	                                             holding the merge date reads the
//	                                             emptiness as evidence
//	M15 cut "; no backfill"                 RED  absence before the cutover stops
//	                                             being explained
//	G2  control: reword "no backfill" as "nothing was backfilled"
//	                                      GREEN  the promise survives a rewording
//
//	── enforcement side (the vocabulary, the description untouched) ──
//	M16 delete "lock_acquired" from domain.EventVocabulary
//	                                        RED  the description warns about a
//	                                             type the published vocabulary no
//	                                             longer carries, which is the
//	                                             aihub#259 shape one layer over
//	    ⚠️ An earlier draft of this list named a different mutant here — renaming
//	    EventLockAcquired's VALUE — and RAN GREEN. EventVocabulary is a
//	    hand-written list of literals with its own source-scanning arm
//	    (domain.TestEventVocabulary_CoversEveryEmitter), so the constant and the
//	    vocabulary are two declarations and only the second one is this arm's
//	    subject. Recorded rather than quietly replaced, because a mutant list
//	    that was never run is worse than none.
func TestPublishedReadEventsCutoverCaveatNamesTheThreeLockEventsAndSaysDeploy(t *testing.T) {
	tool := publishedTool(t, "pf_read_events")
	desc := tool.Description
	if len(desc) < 100 {
		t.Fatalf("pf_read_events' description is %d character(s) (%q) — too short to carry the "+
			"caveat, and every check below would be asserting about a stub", len(desc), desc)
	}

	// The three names, on both sides. Reading them off the domain vocabulary is
	// what makes a one-sided rename red: a warning about a type nothing emits
	// is the aihub#259 failure arriving in the description instead of the wire.
	vocab := map[string]bool{}
	for _, typ := range domain.EventVocabulary {
		vocab[typ] = true
	}
	if len(vocab) < 10 {
		t.Fatalf("domain.EventVocabulary holds %d type(s) — the check below would be comparing "+
			"against an empty vocabulary", len(vocab))
	}
	for _, typ := range lockCutoverEventTypes {
		if !vocab[typ] {
			t.Errorf("the card's caveat is about %q, which domain.EventVocabulary does not "+
				"carry. A description warning that a type is absent before some deploy, for a "+
				"type no code path can emit at all, tells a reader the wrong thing about why "+
				"their read came back empty.", typ)
			continue
		}
		if !strings.Contains(desc, typ) {
			t.Errorf("pf_read_events' description does not name %q:\n    %q\nThe card says the "+
				"cutover caveat rides on the tool itself rather than only in the design doc, "+
				"and hop 1 is the only thing a tool caller ever sees.", typ, desc)
		}
	}

	// *deploy*, not *commit* — the distinction the card calls deliberate.
	lower := strings.ToLower(desc)
	if !strings.Contains(lower, "deploy") {
		t.Errorf("pf_read_events' description never says `deploy`:\n    %q\nThe caveat is about "+
			"a rollout, and aihub rollouts need an explicit human instruction and can trail a "+
			"merge by days. A reader holding the commit date reads the emptiness in that gap as "+
			"\"the recorder was running and saw nothing\".", desc)
	}
	if !containsAnyFold(desc, []string{"no backfill", "not backfilled", "nothing was backfilled"}) {
		t.Errorf("pf_read_events' description no longer says the history was not backfilled:\n"+
			"    %q\nWithout that, the three names read as \"these exist\" and the empty result "+
			"before the cutover has no explanation attached.", desc)
	}
}

// TestReadEventsOmitsPinnedFirstWhenItIsFalse is the half readEventsWireProbes
// cannot reach: it sends true and asserts arrival, and a handler that forwarded
// the value unconditionally passes it.
//
// Forwarding `pinned_first=false` would make "not specified" and "explicitly
// false" the same bytes — the zero-value-versus-absent confusion this contract
// keeps tripping over, and the one pf_list_work_items has its own arm for
// (TestListWorkItemsOmitsFalseBooleans).
//
// ─── Recorded mutants ──────────────────────────────────────────────────────
//
//	── enforcement side ──
//	M17 replace the `if boolArg(args, "pinned_first")` guard with an
//	    unconditional params.Set("pinned_first", …)
//	                                        RED  false arrived on the wire
//	M18 delete the forwarding entirely      RED  the control arm lost the true
//	                                             case, which is what separates
//	                                             "omitted when false" from
//	                                             "never sent"
func TestReadEventsOmitsPinnedFirstWhenItIsFalse(t *testing.T) {
	for _, tc := range []struct {
		shape any
		want  string // "" means the key must be absent
	}{
		{shape: false, want: ""},
		{shape: true, want: "true"},
	} {
		q := newQueryRecorder(t)
		q.respondWith(map[string]any{"events": []any{}})
		callToolAgainstRecorder(t, q, "pf_read_events", map[string]any{
			"work_item_id": "wi_readEventsProbe", "pinned_first": tc.shape,
		})
		got := q.last(t)
		if _, present := got["pinned_first"]; !present && tc.want != "" {
			t.Errorf("pinned_first=%v put nothing on the wire, want %q — a flag that is "+
				"accepted and never sent is indistinguishable from one that is not published. "+
				"Full query: %v", tc.shape, tc.want, got)
			continue
		}
		if v := got.Get("pinned_first"); v != tc.want {
			t.Errorf("pinned_first=%v forwarded as %q, want %q. An explicit false must be "+
				"OMITTED: forwarding it makes it indistinguishable from not specifying the "+
				"parameter, which is the same zero-value-versus-absent confusion "+
				"pf_list_work_items has TestListWorkItemsOmitsFalseBooleans for. Full query: %v",
				tc.shape, v, tc.want, got)
		}
	}
}
