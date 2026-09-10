package mcp_test

// aihub#543 probe wave 1 — two hop-1 claims of the pf_get_ready_queue card that
// no arm read.
//
// Both are about a PUBLISHED STRING or a PUBLISHED SCHEMA, so both are read off
// a real MCP session rather than off the source: the contract JSON
// cli.RunDumpMCPSchemas produces carries no per-property descriptions, so it
// cannot see the string the first arm is about.
//
//	TestPublishedReadyOnlyStatesItPagesDifferentlyFromTheQueue
//	  The card says the divergence between the two surfaces "is stated on
//	  `ready_only`'s own description because the natural reading is that they
//	  agree". That is a claim about a string on ANOTHER tool, and this is the only
//	  arm that reads it. internal/domain/ready_queue_page_divergence_test.go holds
//	  the SQL half; this holds the half a caller actually sees.
//
//	TestTheWithdrawnNonConflictingParamIsGoneFromTheSchemaAndTheWire
//	  aihub#387 withdrew `non_conflicting` rather than implementing it. The
//	  existing gate (ready_queue_param_wiring_test.go) is quantified over what the
//	  schema PUBLISHES, so it fires the day the name comes back UNREAD — and stays
//	  green the day it comes back with a reader, which is the day the card's "it is
//	  gone rather than implemented" becomes false with nothing to say so.
//
//	GOWORK=off go test ./internal/mcp/ -run 'TestPublishedReadyOnly|TestTheWithdrawn' -count=1 -v

import (
	"encoding/json"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/GMISWE/ieops-aihub/internal/domain"
)

// readyQueueSurfacedParams is the schema pf_get_ready_queue publishes, and it is
// the whole of it: two parameters, no third.
//
// Written down rather than counted, because the claim is about WHICH two. A
// count alone is satisfied by any pair, and the failure this pins is a specific
// name coming back — the one this tool is the repo's canonical example of.
var readyQueueSurfacedParams = map[string]bool{"project": true, "max": true}

// withdrawnReadyQueueParam is the name aihub#387 removed.
const withdrawnReadyQueueParam = "non_conflicting"

// TestPublishedReadyOnlyStatesItPagesDifferentlyFromTheQueue reads
// pf_list_work_items' `ready_only` description and requires it to carry both
// halves of the cross-tool fact: the shared predicate and the different page.
//
// 🔴 The expected words are not a quote of the whole sentence. A substring
// assertion against the full description is satisfied by nothing except that
// exact wording, so it goes red on a rewrite that keeps every promise — and the
// author's cheapest repair is then deleting the assertion. What is required
// instead is that the string still names the ready queue AND still says the page
// differs, which is the claim the pf_get_ready_queue card makes about it.
//
// ─── Recorded mutants ──────────────────────────────────────────────────────
//
// Every one was applied to this tree and the verdict is what RAN.
//
//	── publication side (the schema string, the code untouched) ──
//	M13 cut the "not the same page … different subsets" half of the description
//	                                            RED  the divergence goes unstated
//	M14 cut the "Same PREDICATE as pf_get_ready_queue's items[]" half
//	                                            RED  the queue goes unnamed
//	G3  control: reword "they return different subsets" as "each returns a
//	    different slice of them"              GREEN  the promise survives a
//	                                                 rewording, which is what keeps
//	                                                 this from being a quote check
//
//	── enforcement side (the declaration, the description untouched) ──
//	M15 ListWorkItemsLimitDefault = 60          RED  "says limit=50 and ListWorkItems
//	                                                 defaults to 60" — the direction a
//	                                                 hard-coded 50 would have missed
func TestPublishedReadyOnlyStatesItPagesDifferentlyFromTheQueue(t *testing.T) {
	tool := publishedTool(t, "pf_list_work_items")
	raw, err := json.Marshal(tool.InputSchema)
	if err != nil {
		t.Fatalf("marshal pf_list_work_items InputSchema: %v", err)
	}
	desc, ok := publishedParamDescription(t, raw, "ready_only")
	if !ok {
		t.Fatalf("pf_list_work_items publishes no `ready_only` parameter. The " +
			"pf_get_ready_queue card states that the divergence between the two surfaces is " +
			"stated on THIS description; with the parameter gone that sentence describes a " +
			"string no caller can read, and this arm cannot pass by not finding its subject.")
	}
	if len(desc) < 40 {
		t.Fatalf("`ready_only`'s description is %d character(s) (%q) — too short to carry "+
			"either half of the claim, and every check below would be asserting about a stub",
			len(desc), desc)
	}

	lower := strings.ToLower(desc)

	// Half one: it names the queue whose predicate it shares.
	if !strings.Contains(lower, "pf_get_ready_queue") && !strings.Contains(lower, "ready queue") {
		t.Errorf("`ready_only`'s description never names the ready queue:\n    %q\n"+
			"The two share one SQL constant (internal/domain/work_items.go, "+
			"readyOnlyPredicate), and a caller who cannot see that they are the same question "+
			"has no reason to expect the same answers — which is the half that IS true.", desc)
	}

	// Half two: it says the page is not the same. Any one of these carries it;
	// requiring a particular phrasing would be a quote check wearing a costume.
	divergence := []string{"not the same page", "different subsets", "different page",
		"different subset", "differ"}
	found := false
	for _, phrase := range divergence {
		if strings.Contains(lower, phrase) {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("`ready_only`'s description states no divergence from the ready queue's "+
			"items[]:\n    %q\nIt promises the same predicate and then says nothing about the "+
			"page, so the natural reading is that the two agree — and they do not, because the "+
			"limits and the ordering differ (internal/domain/ready_queue_page_divergence_test.go). "+
			"The pf_get_ready_queue card cites this string as where a caller is told; none of "+
			"%v appears in it.", desc, divergence)
	}

	// Half three: the page size it states is the one the endpoint enforces.
	//
	// 🔴 Read out of the description and compared with the CONSTANT, never
	// spelled out here. An arm that expected the literal 50 would go green on
	// the day the default moved and the description did not, which is the one
	// day it was needed — the rule TestPublishedBaseStrengthRangeIsTheEnforcedOne
	// states for itself, applied to a page size.
	//
	// Only the list side is bound. The ready queue's own default is unexported
	// in package domain, so this arm can compare one of the two numbers; the
	// other is held against the code by
	// internal/domain/ready_queue_page_divergence_test.go, which asserts the two
	// differ without spelling either out.
	stated := regexp.MustCompile(`limit=(\d+)`).FindStringSubmatch(desc)
	if stated == nil {
		t.Errorf("`ready_only`'s description states no `limit=` for its own page:\n    %q\n"+
			"It promises a page that differs from the ready queue's and then names no size, "+
			"so a caller cannot tell which of the two they are looking at.", desc)
	} else if stated[1] != strconv.Itoa(domain.ListWorkItemsLimitDefault) {
		t.Errorf("`ready_only`'s description says limit=%s and ListWorkItems defaults to %d. "+
			"The description is what a caller plans a page around; a number that has drifted "+
			"from the constant is worse than no number, because the next reader trusts it.",
			stated[1], domain.ListWorkItemsLimitDefault)
	}

	t.Logf("ready_only description (%d chars): %q", len(desc), desc)
}

// TestTheWithdrawnNonConflictingParamIsGoneFromTheSchemaAndTheWire pins the
// disposition aihub#387 chose: withdrawn, not implemented.
//
// Two arms, because the two failures are different. The schema arm catches the
// name being re-added; the wire arm catches a handler that forwards it anyway
// from an argument the schema no longer declares — the shape the MCP SDK makes
// invisible, since it drops undeclared arguments with no error.
//
// ─── Recorded mutants ──────────────────────────────────────────────────────
//
// Every one was applied to this tree and the verdict is what RAN.
//
//	── publication side (the schema, the handler untouched) ──
//	M17 re-add `non_conflicting` to the published schema
//	                                            RED  named as withdrawn, and the
//	                                                 unlisted-publication arm too
//	M19 delete the citation clause from the card sentence, leaving the sentence
//	                                            RED  K12 DEBT_GROWTH — the candidate
//	                                                 stays and its cover goes, which
//	                                                 is the finding that shape earns
//	G4  control: reword the sentence, citation untouched
//	                                          GREEN  K12 owns the publication side
//
//	── enforcement side (the handler, the schema untouched) ──
//	M18 forward args["non_conflicting"] onto the query string
//	                                            RED  the wire arm — a parameter the
//	                                                 schema no longer declares is one
//	                                                 no caller can discover
func TestTheWithdrawnNonConflictingParamIsGoneFromTheSchemaAndTheWire(t *testing.T) {
	published := publishedSchemaProps(t, readyQueueTool)

	if _, back := published[withdrawnReadyQueueParam]; back {
		t.Errorf("%s publishes %q again. aihub#387 withdrew it rather than implementing it, "+
			"and the reason was not cost: \"non-conflicting\" has no agreed definition here — "+
			"predicted from declared_resources, or read off the locks actually held? — and this "+
			"repo has measured pf_predict_conflicts untrustworthy in both directions. The "+
			"first version of this parameter cost aihub#186 a whole design written on top of a "+
			"switch that did nothing. Bring the ruling that reopened it, and a hop-3 reader.",
			readyQueueTool, withdrawnReadyQueueParam)
	}
	for name := range published {
		if !readyQueueSurfacedParams[name] {
			t.Errorf("%s publishes %q, which readyQueueSurfacedParams does not list. This tool "+
				"is the repo's canonical case of a published parameter that reached the wire "+
				"and was read by nothing, so a new one is a decision: add the row here, and "+
				"give it the hop-3 reader "+
				"internal/mcp/ready_queue_param_wiring_test.go demands.", readyQueueTool, name)
		}
	}
	for name := range readyQueueSurfacedParams {
		if _, ok := published[name]; !ok {
			t.Errorf("%s no longer publishes %q, which readyQueueSurfacedParams lists. A "+
				"parameter that goes away is fine and is policy here (aihub#387/#394/#448) — "+
				"but it takes the hop 0-1 table of docs/mcp-cards/pf_get_ready_queue.md and "+
				"this row with it, and a stale row means this arm is watching one parameter "+
				"more than exists.", readyQueueTool, name)
		}
	}

	// The wire half. The argument is sent as a caller who read the old schema
	// would send it; nothing may carry it to the server.
	q := newQueryRecorder(t)
	callToolAgainstRecorder(t, q, readyQueueTool, map[string]any{
		"project":                "aihub",
		withdrawnReadyQueueParam: true,
		"max":                    float64(25),
	})
	got := q.last(t)
	if !got.Has("project") {
		t.Fatalf("the recorded query %v carries no `project`, so this arm is asserting about a "+
			"request that never described the call — an absence proves nothing when nothing "+
			"arrived", got)
	}
	if got.Has(withdrawnReadyQueueParam) {
		t.Errorf("a call passing %q put `?%s=%s` on the wire (query %v). The schema does not "+
			"declare it, so no caller can discover it and the server has never read it: this is "+
			"a parameter that exists only between this process and the HTTP layer, which is "+
			"strictly worse than the published version aihub#387 withdrew — that one at least "+
			"appeared in the contract.",
			withdrawnReadyQueueParam, withdrawnReadyQueueParam,
			got.Get(withdrawnReadyQueueParam), got)
	}

	t.Logf("%s publishes %v; the wire carried %v", readyQueueTool, publishedTypeNames(published), got)
}

// publishedTypeNames is the sorted key list of a publishedSchemaProps map, for
// failure text. sortedKeys and sortedParamNames in this package take other map
// types; this is the third shape rather than a widening of either, because a
// generic helper would have to be introduced into two files this change has no
// other reason to touch.
func publishedTypeNames(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
