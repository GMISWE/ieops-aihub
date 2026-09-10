package mcp_test

// aihub#543 probe wave 1 — `docs/mcp-cards/pf_force_takeover.md`'s hop 0-1
// paragraph, which is two claims about PUBLISHED TEXT and one about the schema:
//
//	"Two parameters, and a description whose last clause is the whole reason this
//	 tool and `pf_claim_work_item`'s `force_takeover` flag carry **different**
//	 wording."
//	"\"No flag displaces another work item's lock, **except in a narrow
//	 commit-window race**\" lives here and only here."
//	    -> TestPublishedTakeoverRaceQualifierIsAbsentFromTheClaimFlag
//
// 🔴 WHY NO EXISTING ARM SEES THIS. K9 validates a card's verbatim quotes only
// inside a hop 0-1 TABLE CELL, and this quote is in prose; the two
// `description_sha256` fields pin each string to itself, so the two tools can
// drift into agreement with both hashes regenerated and every gate green. What
// the sentence claims is a RELATION between two published strings, and a
// relation between two things is exactly what a per-thing hash cannot hold.
//
// The expected clause is read OUT OF THE CARD rather than written down here, for
// the reason internal/domain's base-strength arm records: a literal in the test
// goes green on the day the published wording moves, which is the one day it was
// needed. Here that cuts both ways at once — the card's quote and the live
// description have to keep agreeing, which is the whole content of "lives here
// and only here" as far as this repo can check it.
//
// ⚠️ SCOPE. This arm holds ONE HALF of "and only here": the qualifier is on this
// tool's description and is absent from the one other place it used to live
// (aihub#430 deleted it from pf_claim_work_item's `force_takeover` prop). A
// repo-wide cross-card uniqueness census is a different instrument and is
// deferred to phase 1b by the aihub#543 §8 Q5 ruling; it is named here rather
// than left implicit, because an arm that quietly measures less than its title
// claims is the failure the floors in this package exist to refuse.
//
// No database:
//
//	GOWORK=off go test ./internal/mcp/ -run TestPublishedTakeoverRaceQualifier -count=1

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// takeoverQuoteRe finds the card's verbatim quote of the published description's
// last clause. Anchored on a fragment of the quote rather than on its position:
// the paragraph gets rewritten, and a positional read would then silently pick up
// a different string and compare the tool against it.
var takeoverQuoteRe = regexp.MustCompile(`"([^"]*No flag displaces[^"]*)"`)

// cardBoldRe is the card's own emphasis. The card marks the DISTINGUISHING clause
// bold inside the quote, so the split between "what both say" and "what only this
// one says" is taken from the card's markup instead of from a comma this file
// would have to know about.
var cardBoldRe = regexp.MustCompile(`\*\*([^*]+)\*\*`)

// takeoverCardQuote returns (whole quote, the bold clause inside it), both with
// markdown emphasis removed and whitespace collapsed — the card wraps the quote
// across a line break, and a reader of the rendered card sees one line.
func takeoverCardQuote(t *testing.T) (string, string) {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "docs", "mcp-cards", "pf_force_takeover.md"))
	if err != nil {
		t.Fatalf("read the card: %v — the card is one of the two sides this arm compares, so a "+
			"missing one is a failure and not an empty pass", err)
	}
	flat := strings.Join(strings.Fields(string(raw)), " ")

	m := takeoverQuoteRe.FindStringSubmatch(flat)
	if m == nil {
		t.Fatal("docs/mcp-cards/pf_force_takeover.md quotes no published sentence containing " +
			"\"No flag displaces\". Either the paragraph was rewritten — in which case this arm " +
			"is comparing the tool against nothing and every assertion below is vacuous — or the " +
			"claim it holds has gone, and this file should go with it.")
	}
	quoted := m[1]

	b := cardBoldRe.FindStringSubmatch(quoted)
	if b == nil {
		t.Fatalf("the card's quote %q carries no **bold** clause. The card marks the clause that "+
			"lives on this tool alone, and this arm takes the split from that markup rather than "+
			"from a comma; without it there is nothing to compare separately.", quoted)
	}
	clause := strings.TrimSpace(b[1])
	whole := strings.TrimSpace(cardBoldRe.ReplaceAllString(quoted, "$1"))

	if len(whole) < 40 || len(clause) < 10 {
		t.Fatalf("the card's quote is %q with distinguishing clause %q — too short to be the "+
			"published sentence, so the extraction is broken rather than the tools", whole, clause)
	}
	return whole, clause
}

// publishedRequired returns a published tool's `required` list.
func publishedRequired(t *testing.T, tool string) []string {
	t.Helper()
	raw, err := json.Marshal(publishedTool(t, tool).InputSchema)
	if err != nil {
		t.Fatalf("marshal InputSchema for %q: %v", tool, err)
	}
	var schema struct {
		Required []string `json:"required"`
	}
	if err := json.Unmarshal(raw, &schema); err != nil {
		t.Fatalf("decode InputSchema for %q: %v", tool, err)
	}
	return schema.Required
}

// TestPublishedTakeoverRaceQualifierIsAbsentFromTheClaimFlag is the hop 0-1
// paragraph, encoded.
//
// Three assertions, because the paragraph makes three claims and none of them
// implies another: the tool takes two parameters and both are required; the two
// published statements SHARE the aihub#393 sentence (otherwise "different
// wording" would be describing two unrelated strings, which is not the claim);
// and the commit-window clause is on this description and on the claim flag's
// description it is not.
//
// The shared half is what makes the third assertion mean something. A test that
// only checked "the claim flag does not say commit-window" is satisfied by a
// claim flag that says nothing at all — including by the flag being unpublished —
// so the absence would be measuring the wrong thing on exactly the day it
// mattered.
//
// MUTANTS (run against this tree; the verdict is what happened, not what was
// expected):
//
//	M1 enforcement: re-add ", except in a narrow commit-window race (aihub#410)"
//	   to pf_claim_work_item's `force_takeover` prop — the copy-back
//	   internal/mcp/tools_lifecycle.go's comment forbids
//	                                              RED  the_clause_lives_on_this_tool_alone
//	M2 enforcement: delete the clause from pf_force_takeover's own Description
//	                                              RED  the_clause_lives_on_this_tool_alone
//	M3 enforcement: publish a third parameter on pf_force_takeover
//	                                              RED  two_required_parameters
//	M4 enforcement: drop `reason` from the schema's required list
//	                                              RED  two_required_parameters
//	M5 publication: reword the card's quote (drop "narrow" from it)
//	                                              RED  the_clause_lives_on_this_tool_alone
//	                                                   — the card and the live
//	                                                   description are compared, so
//	                                                   editing either side alone is
//	                                                   red
//	M6 publication: delete the card's whole quoted sentence
//	                                              RED  the extraction floor in
//	                                                   takeoverCardQuote
//	G1 control:     reword an unrelated card sentence ("The clause is a measured
//	    statement, not a hedge")                GREEN  the quote extraction is
//	                                                   anchored on the quote, not on
//	                                                   a position, so an edit
//	                                                   elsewhere in the paragraph
//	                                                   must not move it
func TestPublishedTakeoverRaceQualifierIsAbsentFromTheClaimFlag(t *testing.T) {
	whole, clause := takeoverCardQuote(t)
	shared := strings.TrimRight(strings.TrimSpace(strings.Replace(whole, clause, "", 1)), " ,")
	if len(shared) < 20 {
		t.Fatalf("removing %q from %q leaves %q — the two halves cannot be told apart, so the "+
			"comparison below would be between a string and itself", clause, whole, shared)
	}

	toolDesc := publishedTool(t, "pf_force_takeover").Description
	flagDesc, ok := publishedParamDescription(t,
		mustMarshalSchema(t, "pf_claim_work_item"), "force_takeover")
	if !ok || strings.TrimSpace(flagDesc) == "" {
		t.Fatalf("pf_claim_work_item publishes no `force_takeover` description (%q). The card's "+
			"sentence is about how the two are worded, and an absent one makes every "+
			"comparison below trivially true.", flagDesc)
	}

	t.Run("two_required_parameters", func(t *testing.T) {
		props := publishedSchemaProps(t, "pf_force_takeover")
		required := publishedRequired(t, "pf_force_takeover")
		if len(props) != 2 {
			t.Errorf("pf_force_takeover publishes %d parameters (%v), and the card says two. A "+
				"third parameter is a third thing callers will send, and the card's hop-1 table "+
				"names the promise made for each one.", len(props), sortedPropNames(props))
		}
		for _, want := range []string{"work_item_id", "reason"} {
			if _, live := props[want]; !live {
				t.Errorf("pf_force_takeover publishes no %q (it publishes %v)",
					want, sortedPropNames(props))
			}
		}
		if len(required) != 2 {
			t.Errorf("pf_force_takeover marks %v required, want both parameters. `reason` is "+
				"recorded on the timeline and is the only account the evicted holder ever gets; "+
				"an optional one is a takeover with no stated cause.", required)
		}
	})

	t.Run("both_publish_the_same_aihub393_statement", func(t *testing.T) {
		if !strings.Contains(toolDesc, shared) {
			t.Errorf("pf_force_takeover's description does not carry the card's quoted sentence "+
				"%q.\nPublished: %q\nThe card quotes this description verbatim, so the two have "+
				"drifted and a reader of the card is being told what the tool no longer says.",
				shared, toolDesc)
		}
		if !strings.Contains(flagDesc, shared) {
			t.Errorf("pf_claim_work_item's `force_takeover` description does not carry %q.\n"+
				"Published: %q\nThe card's claim is that the two carry the SAME statement and "+
				"differ only in the last clause; with the statement gone from one side there is "+
				"no shared guarantee left for the clause to qualify.", shared, flagDesc)
		}
	})

	t.Run("the_clause_lives_on_this_tool_alone", func(t *testing.T) {
		if !strings.Contains(toolDesc, whole) {
			t.Errorf("pf_force_takeover's description does not carry the card's full quote "+
				"%q.\nPublished: %q\naihub#451 measured the exception this clause names on this "+
				"path; delete the clause only in the change that closes the gap.", whole, toolDesc)
		}
		if strings.Contains(flagDesc, clause) {
			t.Errorf("pf_claim_work_item's `force_takeover` description carries %q.\nPublished: "+
				"%q\nThe clause was deleted from there by aihub#430 and must not be copied back: "+
				"the claim path opens SERIALIZABLE and this one opens READ COMMITTED, so it is "+
				"one sentence about two different guarantees and it is false on that side.",
				clause, flagDesc)
		}
	})
}

// mustMarshalSchema returns a published tool's InputSchema as raw JSON, which is
// the shape publishedParamDescription reads.
func mustMarshalSchema(t *testing.T, tool string) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(publishedTool(t, tool).InputSchema)
	if err != nil {
		t.Fatalf("marshal InputSchema for %q: %v", tool, err)
	}
	return raw
}
