package mcp_test

// aihub#495 — aihub#440's editability matrix, published at the only hop a tool
// caller can read.
//
// WHAT SHIPPED WITHOUT IT
// -----------------------
// aihub#440 replaced three per-field status guards with one matrix and one error
// code per rejection KIND, and it moved a cell: `labels`, `priority`,
// `milestone`, `requires_human_session` and `declared_resources` had NO status
// guard at all — writable on a wrapped work item — and now answer 409
// CONFLICT_TERMINAL_STATE there. That is the batch's only 200 -> 409 transition.
//
// It was written down in three places and none of them is on the wire: the
// matrix comment above domain.wiEditTierByField, the table in
// docs/mcp-cards/pf_update_work_item.md, and the row in docs/mcp-tools.md. The
// tool description said "Update a work item (goal, wi_type, priority, labels,
// etc.)". A caller holding the correct-yesterday belief that priority is
// writable on a wrapped wi had nothing to read that would correct it, and hop 1
// is the only thing an LLM caller ever sees — the doctrine aihub#464 states and
// aihub#486 is the previous application of.
//
// The same gap on the same tool, one parameter over: `goal`'s description names
// the status rule and `wi_type`'s said only "Updated wi_type", though the two are
// ONE map entry pair in ONE tier under ONE predicate.
//
// WHY IT IS ANCHORED ON THE DOMAIN, NOT ON A LIST TYPED HERE
// ----------------------------------------------------------
// A gate that spelled out the five working-tier fields would be a third copy of
// the matrix and would go green on exactly the day a sixth field joined the tier
// unpublished — the failure it exists to prevent, arriving through the gate
// itself. domain.WorkItemFieldsByEditTier and domain.WorkItemStatusesByEditClass
// return the live membership, so this arm asks the schema about whatever the
// matrix currently holds. Same anchoring aihub#474 gave the goal cap and
// aihub#434 the visibility vocabulary.
//
// WHAT IT CAN AND CANNOT SEE
// --------------------------
// It is a content check over published text: it cannot tell a description that
// explains the matrix from one that merely contains the words. The behavioural
// half is domain.TestUpdateGate (3 tiers x 7 statuses x 4 actors), which is where
// it belongs. What this adds is that deleting the disclosure is a red test rather
// than a silent shortening — the failure mode that lost aihub#397's finding once
// and that regeneration of the contract card cannot catch, because the generator
// writes hashes and never reads prose.
//
// No database needed:
//
//	go test ./internal/mcp/ -run TestUpdateWorkItem -v

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/GMISWE/ieops-aihub/internal/domain"
)

// TestUpdateWorkItemPublishesTheWorkingTierRefusal is the 200 -> 409 arm.
func TestUpdateWorkItemPublishesTheWorkingTierRefusal(t *testing.T) {
	tool := publishedTool(t, "pf_update_work_item")
	desc := tool.Description

	working := domain.WorkItemFieldsByEditTier()["working"]
	if len(working) < 5 {
		t.Fatalf("domain.WorkItemFieldsByEditTier() reports %d working-tier field(s) (%v); "+
			"aihub#440 put six there. The matrix moved without this arm moving with it — "+
			"re-read it before trusting anything below.", len(working), working)
	}

	code := string(domain.ErrConflictTerminalState)
	if !strings.Contains(desc, code) {
		t.Errorf("TERMINAL_REFUSAL_UNPUBLISHED: pf_update_work_item's description never names %s.\n\n"+
			"Since aihub#440 that is what %v answer on a wrapped, failed or cancelled work item, "+
			"and five of them used to succeed there. The card and docs/mcp-tools.md both carry the "+
			"table; neither is on the wire.\n\nLive description was:\n%s", code, working, desc)
	}

	for _, field := range working {
		if !strings.Contains(desc, field) {
			t.Errorf("TERMINAL_REFUSAL_UNPUBLISHED: pf_update_work_item's description never names "+
				"the working-tier field %q.\n\nA caller cannot apply a rule to a field the rule's "+
				"statement does not mention. If %q was added to the tier after this text was "+
				"written, that is precisely the event this arm exists to make loud: name it in the "+
				"description in the same change.\n\nLive description was:\n%s", field, field, desc)
		}
	}

	// The closed column, by name. "terminal" alone is a word a caller has to map
	// onto a status they can read off pf_get_work_item; the three values are not.
	for _, status := range domain.WorkItemStatusesByEditClass()["closed"] {
		if !strings.Contains(desc, status) {
			t.Errorf("TERMINAL_REFUSAL_UNPUBLISHED: pf_update_work_item's description never names "+
				"the terminal status %q, so a caller reading it cannot tell which statuses the "+
				"refusal covers.\n\nLive description was:\n%s", status, desc)
		}
	}

	// The mixed-patch rule is the one thing no per-parameter description could
	// carry: it is a property of the PATCH, not of any field in it. Refusing whole
	// rather than partially applying is why a caller cannot treat a 409 as "the
	// other fields landed".
	for _, needle := range []string{"STRICTEST", "whole"} {
		if !strings.Contains(desc, needle) {
			t.Errorf("MIXED_PATCH_RULE_UNPUBLISHED: pf_update_work_item's description no longer "+
				"contains %q.\n\nThe strictest supplied tier governs the whole patch "+
				"(domain.strictestSuppliedEditTier), so attrs_patch + labels against a wrapped work "+
				"item is refused entirely and NOTHING is written. A caller who assumes partial "+
				"application reads a 409 as \"some of it landed\" and does not resend.\n\n"+
				"Live description was:\n%s", needle, desc)
		}
	}
}

// TestUpdateWorkItemPublishesTheContractTierStatusRule is the `wi_type` arm.
//
// It checks both contract-tier parameters rather than only the one that was
// wrong, because the defect was the PAIR disagreeing: they are one tier under one
// predicate, and a gate that pinned only `wi_type` would let them drift apart
// again in the other direction.
func TestUpdateWorkItemPublishesTheContractTierStatusRule(t *testing.T) {
	tool := publishedTool(t, "pf_update_work_item")
	b, err := json.Marshal(tool.InputSchema)
	if err != nil {
		t.Fatalf("marshal pf_update_work_item InputSchema: %v", err)
	}

	contract := domain.WorkItemFieldsByEditTier()["contract"]
	if len(contract) != 2 {
		t.Fatalf("domain.WorkItemFieldsByEditTier() reports %d contract-tier field(s) (%v); "+
			"aihub#440 put exactly goal and wi_type there. Re-read the matrix before trusting "+
			"anything below.", len(contract), contract)
	}

	classes := domain.WorkItemStatusesByEditClass()
	open, notOpen := classes["open"], append(append([]string{}, classes["live"]...), classes["closed"]...)

	for _, field := range contract {
		desc, present := publishedParamDescription(t, b, field)
		if !present {
			t.Errorf("pf_update_work_item no longer publishes %q at all, though the matrix still "+
				"assigns it the contract tier.", field)
			continue
		}
		for _, status := range open {
			if !strings.Contains(desc, status) {
				t.Errorf("CONTRACT_TIER_STATUS_UNPUBLISHED: pf_update_work_item's %q description "+
					"never names the editable status %q.\n\n%v share one tier, one predicate and one "+
					"map entry pair (domain.wiEditTierByField), and aihub#440 widened their status "+
					"set to %v in a single change. Publishing that rule on one of them and not the "+
					"other is how a caller comes to believe it applies to one field.\n\n"+
					"Live description was:\n%s", field, status, contract, open, desc)
			}
		}
		for _, status := range notOpen {
			if strings.Contains(desc, status) {
				t.Errorf("CONTRACT_TIER_STATUS_OVERPUBLISHED: pf_update_work_item's %q description "+
					"names %q, which is NOT in the editable class %v. A published status the gate "+
					"refuses is a caller told to retry into a 409.\n\nLive description was:\n%s",
					field, status, open, desc)
			}
		}
	}
}
