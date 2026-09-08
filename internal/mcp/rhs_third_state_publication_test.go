package mcp_test

// The aihub#411 T2-9 ruling, made falsifiable: the published contract for
// `requires_human_session` must name the state a caller reaches BY DOING NOTHING.
//
// WHY A GATE AND NOT JUST THE DESCRIPTION
// ---------------------------------------
// The column is a *bool — true / false / NULL — and NULL is a real third state
// with its own ready-queue segment. It was published as a bare `boolean` saying
// only "Whether this wi requires a human session", with no mention of omission.
// aihub#397 measured exactly that gap and was CANCELLED as description-only, which
// the aihub#411 adjudication then ruled is not a cancellation reason. So this
// repair has already been undone once, by a route that left no red test behind.
//
// Every other arm in this package would stay green through a revert. The contract
// cards' K3 arm fires on a description hash moving, but the fix for K3 is to
// regenerate — and regeneration writes the new hash and touches no prose, so the
// diff a reviewer sees is one 64-hex string replacing another. K9 checks that the
// card's hop 0-1 quotes are verbatim, but only for cells that OPEN with a quote,
// and this card's row is prose. The budget gate only cares that the description is
// not too long, which shortening it back cannot fail.
//
// WHAT THIS ARM CAN AND CANNOT SEE
// --------------------------------
// It is a content check over published text, so it is weaker than a behavioural
// test and is not pretending otherwise: it cannot tell a description that explains
// the third state from one that merely contains the words. What it CAN do is make
// deleting the explanation a red test rather than a silent shortening, which is
// precisely the failure mode that destroyed aihub#397's finding.
//
// The behavioural half is deliberately NOT here. aihub#447 settled it by running
// the whole sequence — create with the field omitted, attrs_patch-only update,
// then claim — against a server built from origin/main and against one built from
// a live-era commit, on a real database. The result was that pf_update_work_item
// writes nothing to this column and the CLAIM writes it every time, so there is no
// defect on the update path to gate. Asserting that a call which never wrote the
// column still does not write it is a demonstration, not a gate; the honest place
// for that finding is the comment on domain.buildWorkItemUpdate, which names it.

import (
	"encoding/json"
	"strings"
	"testing"
)

// rhsThirdStateTools maps each tool that publishes `requires_human_session` to
// the substrings its description must carry.
//
// Split by tool because the two paths make DIFFERENT promises and a single
// vocabulary would hide that: a create can reach all three states, an update can
// reach only two. Requiring "omitted" of the update path would push whoever
// satisfies this arm into writing something false.
//
// pf_batch_create_work_items is here for the same reason its parameter exists at
// all: its item schema IS workItemFieldProps, so it inherits the create text —
// and an arm that skipped it would not notice a batch-specific copy being forked
// off, which is the exact drift the shared helper was written to prevent.
var rhsThirdStateTools = map[string][]string{
	"pf_create_work_item":        {"THREE states", "OMITTED", "NULL", "unclassified[]"},
	"pf_batch_create_work_items": {"THREE states", "OMITTED", "NULL", "unclassified[]"},
	"pf_update_work_item":        {"third state", "NULL", "explicit null"},
}

// publishedParamDescription returns the description the live InputSchema
// publishes for one property of one tool.
//
// It reads properties.<param>.description rather than searching the whole
// serialised schema, because a substring found anywhere in a tool's prose is not
// evidence about the parameter: pf_update_work_item's `attrs_patch` description
// already contains the word "null", and a whole-schema search would accept a
// requires_human_session description that said nothing at all.
func publishedParamDescription(t *testing.T, schema json.RawMessage, param string) (string, bool) {
	t.Helper()
	var decoded struct {
		Properties map[string]struct {
			Description string `json:"description"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(schema, &decoded); err != nil {
		t.Fatalf("decode InputSchema: %v", err)
	}
	p, ok := decoded.Properties[param]
	return p.Description, ok
}

func TestRequiresHumanSessionPublishesItsThirdState(t *testing.T) {
	_, tools := newContractGate(t)

	seen := 0
	for _, tool := range tools {
		want, ok := rhsThirdStateTools[tool.Name]
		if !ok {
			continue
		}
		seen++
		b, err := json.Marshal(tool.InputSchema)
		if err != nil {
			t.Fatalf("marshal %s InputSchema: %v", tool.Name, err)
		}
		desc, present := publishedParamDescription(t, b, "requires_human_session")
		if !present {
			// The batch tool nests the field inside `items`, so a direct lookup
			// misses it; fall back to the item schema rather than reporting a
			// parameter that is published one level down as absent.
			var nested struct {
				Properties map[string]struct {
					Items json.RawMessage `json:"items"`
				} `json:"properties"`
			}
			if err := json.Unmarshal(b, &nested); err == nil {
				if items := nested.Properties["items"].Items; len(items) > 0 {
					desc, present = publishedParamDescription(t, items, "requires_human_session")
				}
			}
		}
		if !present {
			t.Errorf("%s no longer publishes requires_human_session at all. If it was "+
				"withdrawn on purpose, delete its entry from rhsThirdStateTools in the same "+
				"change — an arm that silently stops checking a tool is the failure this "+
				"whole file exists to make loud.", tool.Name)
			continue
		}
		for _, phrase := range want {
			if strings.Contains(desc, phrase) {
				continue
			}
			t.Errorf("T2-9 THIRD_STATE_UNPUBLISHED: %s's requires_human_session description no "+
				"longer contains %q.\n\nThe column is *bool — true / false / NULL — and NULL has "+
				"its own ready-queue segment (unclassified[]), which items[] and readyOnlyPredicate "+
				"both exclude. A two-valued published type over a three-valued column means the "+
				"state a caller reaches BY DOING NOTHING is the one the contract does not mention. "+
				"aihub#411 T2-9 ruled that it must; aihub#397 had measured the same gap and was "+
				"cancelled as description-only, which is how the finding was lost the first time.\n\n"+
				"Live description was:\n%s", tool.Name, phrase, desc)
		}
	}

	if seen != len(rhsThirdStateTools) {
		t.Errorf("checked %d of the %d tools named in rhsThirdStateTools — the rest are not in the "+
			"live registry, so this arm was measuring less than it claims. A tool that stops being "+
			"published must leave this map in the same change.", seen, len(rhsThirdStateTools))
	}
}
