package mcp_test

// aihub#543 probe wave 2 — the sentences on `docs/mcp-cards/pf_create_work_item.md`
// and `docs/mcp-cards/pf_batch_create_work_items.md` that describe the two tools'
// PUBLISHED parameter set as one definition rather than two copies.
//
//	"Sixteen top-level parameters, fifteen of which come from
//	 `internal/mcp/tools_lifecycle.go` (`workItemFieldProps`) — one definition
//	 shared with `pf_batch_create_work_items`…"          -> TestBothCreateToolsPublishOneWorkItemFieldSet
//	"Two top-level parameters; every per-item field is the shared set … plus a
//	 per-item `project` override."                       -> TestBothCreateToolsPublishOneWorkItemFieldSet
//	"Because the entry schema IS `workItemFieldProps`, every per-item field
//	 carries that function's published description verbatim…"
//	                                                     -> TestBothCreateToolsPublishOneWorkItemFieldSet
//	"The `items` entry schema is published … rather than left as a bare array…"
//	                                                     -> TestBatchItemsPublishesItsOwnEntryShape
//	"`project` and `goal` sit in that tool's flat `required` list…"
//	                                                     -> TestBatchItemsPublishesItsOwnEntryShape
//	"There is no `brief` counterpart here because there is nothing for it to
//	 do…"                                                -> TestCreatePublishesNoBriefCounterpart
//	"`blocked_by` states what it DOES … because `aihub#357` was filed on the
//	 belief that it only flipped `status`."              -> TestPublishedBlockedByStatesTheEffectsItHas
//
// Every assertion reads a REAL session (`publishedTool`), not
// `cli.RunDumpMCPSchemas' contract JSON and not the `workItemFieldProps()`
// helper itself. Both alternatives would make this file agree with the code by
// construction: the contract JSON carries no per-property descriptions, so it
// cannot see the strings the verbatim arm is about, and a test that called the
// shared helper twice would be comparing one value with itself and would stay
// green through the exact fork it exists to catch — a tool that stops using the
// shared definition.
//
//	GOWORK=off go test ./internal/mcp/ -run 'TestBothCreateToolsPublish|TestBatchItemsPublishes|TestCreatePublishesNoBrief' -count=1 -v

import (
	"encoding/json"
	"sort"
	"strings"
	"testing"

	"github.com/GMISWE/ieops-aihub/internal/domain"
)

const (
	createToolName = "pf_create_work_item"
	batchToolName  = "pf_batch_create_work_items"
	// updateToolName is used only as an anti-vacuity control: it is the tool that
	// DOES publish `brief`, so it is what shows the walk can find one.
	updateToolName = "pf_update_work_item"
)

// publishedProp is one property as a live session publishes it.
type publishedProp struct {
	Type        string `json:"type"`
	Description string `json:"description"`
}

// publishedSubschema is the part of a JSON Schema object these arms read: the
// property map and the required list, at whatever nesting level.
type publishedSubschema struct {
	Properties map[string]publishedProp `json:"properties"`
	Required   []string                 `json:"required"`
	// Items is the entry schema of an array property, decoded lazily because
	// only `items` has one.
	Items json.RawMessage `json:"items"`
}

// topLevelSchema decodes one tool's whole published InputSchema.
func topLevelSchema(t *testing.T, tool string) publishedSubschema {
	t.Helper()
	raw, err := json.Marshal(publishedTool(t, tool).InputSchema)
	if err != nil {
		t.Fatalf("marshal %s InputSchema: %v", tool, err)
	}
	var out publishedSubschema
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("decode %s InputSchema: %v", tool, err)
	}
	if len(out.Properties) == 0 {
		t.Fatalf("%s publishes an InputSchema with no properties at all: %s", tool, raw)
	}
	return out
}

// batchItemsEntrySchema decodes pf_batch_create_work_items' per-item entry
// schema — the one nested under `items`.
//
// Fails rather than returning an empty schema when the entry shape is absent,
// because "the two tools publish the same field set" is answered `true` by
// comparing create's properties with nothing at all.
func batchItemsEntrySchema(t *testing.T, top publishedSubschema) publishedSubschema {
	t.Helper()
	items, ok := top.Properties["items"]
	if !ok {
		t.Fatalf("%s publishes no `items` property; it publishes %v",
			batchToolName, sortedPropKeys(top.Properties))
	}
	if items.Type != "array" {
		t.Fatalf("%s publishes `items` as %q, want array", batchToolName, items.Type)
	}
	// The raw `items` sub-object has to be re-decoded from the tool schema,
	// because publishedProp projects to type+description and drops the nested
	// entry schema.
	raw, err := json.Marshal(publishedTool(t, batchToolName).InputSchema)
	if err != nil {
		t.Fatalf("marshal %s InputSchema: %v", batchToolName, err)
	}
	var nested struct {
		Properties map[string]publishedSubschema `json:"properties"`
	}
	if err := json.Unmarshal(raw, &nested); err != nil {
		t.Fatalf("decode %s InputSchema: %v", batchToolName, err)
	}
	entryRaw := nested.Properties["items"].Items
	if len(entryRaw) == 0 {
		t.Fatalf("%s publishes `items` as a bare array with NO entry schema, so a caller has to "+
			"guess the element shape (aihub#238). Nothing below can compare a field set that is "+
			"not published.", batchToolName)
	}
	var entry publishedSubschema
	if err := json.Unmarshal(entryRaw, &entry); err != nil {
		t.Fatalf("decode %s items entry schema: %v", batchToolName, err)
	}
	if len(entry.Properties) == 0 {
		t.Fatalf("%s' items entry schema publishes no properties: %s", batchToolName, entryRaw)
	}
	return entry
}

func sortedPropKeys(m map[string]publishedProp) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// floorSharedWorkItemFields bounds how many fields the shared definition must
// still carry.
//
// A ceiling would be the wrong instrument: the set GROWS when a work-item field
// is added, and that is normal. What is not normal is it collapsing — a schema
// this walk has stopped parsing, or a helper that returned an empty map, both of
// which make "the two tools publish the same fields" trivially true of two empty
// sets. aihub#543 wave 2 measured 16 (15 shared plus `project`); the floor is set
// below that with room for a field to be withdrawn on purpose, and lowering it
// is a signed change like every other floor in this package.
const floorSharedWorkItemFields = 12

// TestBothCreateToolsPublishOneWorkItemFieldSet is the shared-definition arm.
//
// WHY IT HAD NO ARM. Three existing arms quantify over BOTH create tools for one
// field each — TestPublishedGoalCapIsTheEnforcedOne over `goal`,
// TestRequiresHumanSessionPublishesItsThirdState over
// `requires_human_session`, TestWorkItemToolSchemasPublishTheResponseShape over
// `content`. Each is evidence that its own field did not fork. None of them says
// anything about a field NEITHER of them names: `labels`, `milestone`, `source`,
// `attrs`, `blocked_by`, `declared_resources`, `force_create`, `force_reason`,
// `parent_work_item_id`, `priority`, `scenario`, `wi_type`. A batch-specific copy
// of the map that forked any of those twelve was invisible to every arm in this
// package, which is exactly the drift the shared helper was written to prevent
// and the reason both cards state it to callers.
//
// 🔴 The numbers "sixteen" and "fifteen" are DERIVED here, not typed. The arm
// partitions create's published properties into the ones whose description is
// byte-identical on the batch side and the ones that differ, and asserts that
// exactly one differs and that it is `project` — which is the card's arithmetic
// (shared + 1) computed from the live schema. A test that hard-coded 16 would go
// green on the day a seventeenth field was added to one tool only, because 16 of
// them would still match.
//
// MUTANTS. Every one was applied to this tree and the verdict is what RAN.
//
//	── enforcement side (the schema, the cards untouched) ──
//	M9  fork one shared description: give batchWorkItemsProp's copy of `labels`
//	    its own wording                       RED  descriptions_are_verbatim (2 differ,
//	                                               and the second is named)
//	M10 add a field to createWorkItemSchema only (`watchers`)
//	                                          RED  the_same_field_set (create has one
//	                                               the batch entry does not)
//	M11 drop `attrs` from workItemFieldProps  GREEN control: both tools lose it
//	                                               together, which is what a SHARED
//	                                               definition means and is not this
//	                                               arm's business — the withdrawal
//	                                               would be caught by K3's schema hash
//	                                               and by the cards' own param tables
//	M12 make batchWorkItemsProp reuse create's `project` string verbatim
//	                                          RED  one_field_differs_and_it_is_project
//	                                               (zero differ, so the per-item
//	                                               override is gone)
//	── publication side (the cards, the schema untouched) ──
//	M13 strip the citing sentence's citation, path and symbol both, on either card
//	                                          RED  K12 ledger drift on that card//
//
// ⚠️ The publication-side mutant strips the citation's FILE PATH as well as its
// test symbol, and that is not tidiness. Measured 2026-09-10: de-backticking the
// symbol alone left every one of this wave's seventeen sentences still counted as
// Cited, because cardclaims.CitesAnArm is satisfied by EITHER anchor — so the
// publication side of a citation is only as strong as whichever anchor a later
// edit leaves behind. Reported as an incidental finding of aihub#576.
func TestBothCreateToolsPublishOneWorkItemFieldSet(t *testing.T) {
	create := topLevelSchema(t, createToolName)
	batchTop := topLevelSchema(t, batchToolName)
	entry := batchItemsEntrySchema(t, batchTop)

	if len(create.Properties) < floorSharedWorkItemFields {
		t.Fatalf("%s publishes only %d top-level propert(ies) (%v), floor is %d — a schema this "+
			"small is a walk that stopped reading rather than a tool that stopped promising, and "+
			"every comparison below would be between two nearly-empty sets",
			createToolName, len(create.Properties), sortedPropKeys(create.Properties),
			floorSharedWorkItemFields)
	}

	t.Run("the_same_field_set", func(t *testing.T) {
		// Both directions, separately, because the edit that fixes them differs: a
		// field only create publishes is a field the batch caller cannot set, and a
		// field only the batch publishes is one create's caller cannot.
		for _, name := range sortedPropKeys(create.Properties) {
			if _, ok := entry.Properties[name]; !ok {
				t.Errorf("%s publishes %q and %s' items entries do not. The cards tell callers the "+
					"per-item fields ARE this tool's fields, so a caller batching work it can file "+
					"one at a time would lose this field with no error at any hop.",
					createToolName, name, batchToolName)
			}
		}
		for _, name := range sortedPropKeys(entry.Properties) {
			if _, ok := create.Properties[name]; !ok {
				t.Errorf("%s' items entries publish %q and %s does not. The two write the same "+
					"column through the same handler; a field reachable only in a batch is a "+
					"contract that exists in one of two places a caller would look.",
					batchToolName, name, createToolName)
			}
		}
	})

	t.Run("descriptions_are_verbatim", func(t *testing.T) {
		var differ []string
		for _, name := range sortedPropKeys(create.Properties) {
			b, ok := entry.Properties[name]
			if !ok {
				continue // reported by the set arm above
			}
			if create.Properties[name].Description != b.Description {
				differ = append(differ, name)
			}
		}
		sort.Strings(differ)

		// ── the claim, stated as the cards state it: exactly one field differs,
		// and it is the per-item `project` override.
		t.Run("one_field_differs_and_it_is_project", func(t *testing.T) {
			if len(differ) == 1 && differ[0] == "project" {
				return
			}
			if len(differ) == 0 {
				t.Errorf("every published description is identical on both tools, including "+
					"`project`. The batch tool's `project` is a PER-ITEM override that falls back "+
					"to the call's top-level one, and %s' is the project the wi is filed under — "+
					"one string cannot say both, so a caller reading the item schema is told the "+
					"top-level rule.", createToolName)
				return
			}
			t.Errorf("%d published description(s) differ between %s and %s' items entries: %v.\n"+
				"Only `project` may: it is the one field the batch tool redefines. Every other "+
				"field comes from one definition (workItemFieldProps) precisely so two "+
				"hand-maintained copies cannot drift, and a fork here is that drift — with both "+
				"cards stating the opposite to callers.\n"+
				"%s", len(differ), createToolName, batchToolName, differ,
				describeDescriptionFork(create, entry, differ))
		})
	})

	t.Logf("%s publishes %d top-level fields; %s' items entries publish %d; the shared set is %d "+
		"plus a per-item `project` override",
		createToolName, len(create.Properties), batchToolName, len(entry.Properties),
		len(create.Properties)-1)
}

// describeDescriptionFork prints both strings for every forked field, so the
// failure says WHAT diverged rather than only that something did.
func describeDescriptionFork(create, entry publishedSubschema, names []string) string {
	var b strings.Builder
	for _, n := range names {
		b.WriteString("\n  " + n + ":\n    " + createToolName + ": " + create.Properties[n].Description +
			"\n    " + batchToolName + ": " + entry.Properties[n].Description)
	}
	return b.String()
}

// TestBatchItemsPublishesItsOwnEntryShape is the entry-schema and required-list
// arm.
//
// WHY IT HAD NO ARM. TestNoResourceArrayIsRegisteredWithoutItemSchema bans a
// bare `prop("array", …)` for `declared_resources` and `requested_locks` by name,
// scanning this package's source. `items` is not in its list, so the array whose
// element shape a caller most needs — a whole work item — was the one array the
// aihub#238 rule was not checked on.
//
// The two required lists are asserted together because the batch card's reason
// for existing at all is the difference between them: `project` and `goal` are in
// create's flat `required`, `objectSchema` cannot express "required unless
// `items` is set", so an `items` array bolted onto that tool would publish a
// schema misdescribing its own contract.
//
// MUTANTS.
//
//	── enforcement side ──
//	M14 register `items` as a bare prop("array", …) with no entry schema
//	                                          RED  batchItemsEntrySchema fails, naming
//	                                               aihub#238
//	M15 drop "goal" from the entry schema's required list
//	                                          RED  entry_requires_goal
//	M16 drop "goal" from createWorkItemSchema's required list
//	                                          RED  create_requires_project_and_goal
//	M17 make the entry schema require `project` as well
//	                                          RED  entry_requires_only_goal — the half
//	                                               that makes the per-item fallback real
//	── publication side ──
//	M18 strip the citing sentence's citation, path and symbol both
//	                                          RED  K12 ledger drift
func TestBatchItemsPublishesItsOwnEntryShape(t *testing.T) {
	create := topLevelSchema(t, createToolName)
	batchTop := topLevelSchema(t, batchToolName)
	entry := batchItemsEntrySchema(t, batchTop)

	t.Run("batch_publishes_two_top_level_params", func(t *testing.T) {
		got := sortedPropKeys(batchTop.Properties)
		want := []string{"items", "project"}
		if strings.Join(got, ",") != strings.Join(want, ",") {
			t.Errorf("%s publishes %v at top level, want exactly %v. The card says two, and the "+
				"whole reason this is a separate tool is that per-item fields belong INSIDE "+
				"`items` — a work-item field promoted to the top level would be a field a caller "+
				"could set for the batch and not for an item.", batchToolName, got, want)
		}
	})

	t.Run("create_requires_project_and_goal", func(t *testing.T) {
		got := append([]string(nil), create.Required...)
		sort.Strings(got)
		want := []string{"goal", "project"}
		if strings.Join(got, ",") != strings.Join(want, ",") {
			t.Errorf("%s' flat required list is %v, want %v. That list is the batch card's stated "+
				"reason for a second tool: objectSchema cannot express \"required unless `items` "+
				"is set\", so these two being unconditionally required is what makes overloading "+
				"this tool a published schema that misdescribes itself.", createToolName, got, want)
		}
	})

	t.Run("entry_requires_only_goal", func(t *testing.T) {
		got := append([]string(nil), entry.Required...)
		sort.Strings(got)
		want := []string{"goal"}
		if strings.Join(got, ",") != strings.Join(want, ",") {
			t.Errorf("%s' items entry requires %v, want %v. `project` must NOT be required per "+
				"item — the card promises it falls back to the call's top-level one, and requiring "+
				"it would make every item repeat the value the top-level parameter exists to "+
				"supply.", batchToolName, got, want)
		}
	})

	t.Run("entry_publishes_the_cap_in_its_array_description", func(t *testing.T) {
		// The card says "1-50 objects". The number is not typed here: what is
		// asserted is that the array's description states a range at all, because a
		// caller who cannot see the cap discovers it by having a batch refused.
		desc := batchTop.Properties["items"].Description
		if !strings.Contains(desc, "1-") {
			t.Errorf("%s' `items` description states no batch range: %q. The cap is enforced "+
				"(an oversized array is refused outright, creating nothing), so a caller not told "+
				"about it learns it from a rejected call.", batchToolName, desc)
		}
	})
}

// TestCreatePublishesNoBriefCounterpart is the `brief` arm.
//
// WHY IT HAD NO ARM. TestWorkItemToolSchemasPublishTheResponseShape asserts that
// pf_update_work_item publishes `brief` as a boolean. The card's claim is the
// other direction — that create publishes NO counterpart, because a work item's
// content at creation is whatever the caller supplied, so an unsent content is an
// absent one and there is nothing for `brief` to drop. Nothing asserted the
// absence, and an absence is what a later change adds "for symmetry".
//
// 🔴 The control is not decoration. "Does create publish `brief`?" is answered
// `false` by a walk that reads no schema at all, by a renamed tool, by a schema
// shape this decoder no longer parses. The update tool publishing one is what
// fails first in every one of those cases.
//
// MUTANTS.
//
//	── enforcement side ──
//	M19 add `brief` to createWorkItemSchema   RED  create_publishes_no_brief
//	M20 add `brief` to workItemFieldProps     RED  both arms (create AND the batch
//	                                               entry, which is the shared-set
//	                                               consequence the cards describe)
//	M21 rename pf_update_work_item's `brief` to `brief_withdrawn`
//	                                          RED  the control, which is why it is here
//	── publication side ──
//	M22 strip the citing sentence's citation, path and symbol both
//	                                          RED  K12 ledger drift
func TestCreatePublishesNoBriefCounterpart(t *testing.T) {
	create := topLevelSchema(t, createToolName)
	entry := batchItemsEntrySchema(t, topLevelSchema(t, batchToolName))
	update := topLevelSchema(t, updateToolName)

	// The control first: without a `brief` somewhere, the two absences below are
	// not evidence about `brief`.
	if p, ok := update.Properties["brief"]; !ok || p.Type != "boolean" {
		t.Fatalf("%s publishes brief as %#v (present=%v), want a boolean. This walk cannot tell "+
			"\"create publishes no brief\" from \"this walk finds no property called brief "+
			"anywhere\" without one tool that does.", updateToolName, p, ok)
	}

	for tool, props := range map[string]map[string]publishedProp{
		createToolName:             create.Properties,
		batchToolName + " (items)": entry.Properties,
	} {
		if p, published := props["brief"]; published {
			t.Errorf("%s publishes `brief` (%#v). It has nothing to do: %s suppresses the content "+
				"echo whenever the caller SENT content, and a work item's body at creation is "+
				"whatever the caller supplied — so an unsent content is an absent one and there is "+
				"no stored body for `brief` to drop. A parameter with no effect is one callers "+
				"will set and reason from.", tool, p, createToolName)
		}
	}
}

// TestPublishedBlockedByStatesTheEffectsItHas is the `blocked_by` arm.
//
// WHY IT HAD NO ARM. aihub#357 was filed on the belief that `blocked_by` only
// flipped `status` and created no dependency edge. Measured against a from-zero
// database, that was false — the edges had been written since the first HTTP-API
// commit — and the reading came from two places, one of which was a one-line
// description ("List of blocking work item IDs") that said what the parameter WAS
// and nothing about what it DID. TestBlockedByIsMachineReadable now holds the
// behaviour: the `blocks` edge, the `dependency_created` event on the blocked
// wi's own timeline, and status=blocked; TestDeleteDependency_LastBlockerRemoved_Requeues
// holds the requeue. Nothing held the description, which is the half whose
// absence produced the wrong bug report.
//
// 🔴 Every token is taken from the vocabulary that DEFINES it — the event type
// from domain.EventVocabulary, the edge kind from domain.DependencyKindList(),
// the status from domain.WorkItemStatusValues() — rather than typed here. That
// is what makes this more than a spell-check: rename the event and the
// description has to be reworded in the same change, which is the direction the
// aihub#357 story runs. A test asserting the literal "dependency_created" would
// go green on the day the event was renamed and the description left behind,
// which is the one day it was needed.
//
// MUTANTS. Every one was applied to this tree and the verdict is what RAN.
//
//	── enforcement side (the description, the card untouched) ──
//	M74 shorten blockedByPropDescription back to "List of blocking work item IDs"
//	                                          RED  all four effect subtests
//	M75 drop the requeue sentence only         RED  names_the_requeue
//	M76 drop "and the wi is NOT created"       RED  names_the_refusal
//	M77 rename dependency_created to dependency_recorded in EventVocabulary,
//	    leaving the description alone          RED  names_the_event_type — the arm's
//	                                               two-sidedness, which is why the
//	                                               token is read from the vocabulary
//	── publication side (the card, the description untouched) ──
//	M78 strip the citing sentence's citation, path and symbol both
//	                                          RED  K12 ledger drift
func TestPublishedBlockedByStatesTheEffectsItHas(t *testing.T) {
	desc := topLevelSchema(t, createToolName).Properties["blocked_by"].Description
	if len(desc) < 80 {
		t.Fatalf("%s publishes blocked_by as %q — %d characters, which is the one-line shape "+
			"aihub#357 was filed against. A substring check over a string this short means "+
			"nothing, so this is a failure rather than a set of missing tokens.",
			createToolName, desc, len(desc))
	}

	// The event type, read from the vocabulary that declares it.
	t.Run("names_the_event_type", func(t *testing.T) {
		const want = "dependency_created"
		var declared bool
		for _, e := range domain.EventVocabulary {
			if e == want {
				declared = true
			}
		}
		if !declared {
			t.Fatalf("domain.EventVocabulary no longer declares %q (it declares %d types), so "+
				"there is no event for this description to name and the assertion below would be "+
				"about a string nothing emits", want, len(domain.EventVocabulary))
		}
		if !strings.Contains(desc, want) {
			t.Errorf("the published blocked_by description never names the %s event:\n    %s\n"+
				"aihub#357 was filed as \"sets status=blocked but creates no dependency edge at "+
				"all\". The edge was always written; what was missing was any machine-readable "+
				"record — and a description that does not name the event leaves the next reader "+
				"to measure it the same way.", want, desc)
		}
	})

	// The edge kind, read from the kind vocabulary.
	t.Run("names_the_edge_kind", func(t *testing.T) {
		kinds := domain.DependencyKindList()
		if len(kinds) == 0 {
			t.Fatalf("domain.DependencyKindList() is empty — there is no kind for this description " +
				"to name")
		}
		var named bool
		for _, k := range kinds {
			if strings.Contains(desc, k) {
				named = true
			}
		}
		if !named {
			t.Errorf("the published blocked_by description names none of the dependency kinds %v:\n"+
				"    %s\nThe parameter creates an edge of one specific kind, and a caller who "+
				"cannot see which cannot read the row it produces.", kinds, desc)
		}
	})

	// The status, read from the status vocabulary.
	t.Run("names_the_status_it_sets", func(t *testing.T) {
		const want = "blocked"
		var declared bool
		for _, s := range domain.WorkItemStatusValues() {
			if s == want {
				declared = true
			}
		}
		if !declared {
			t.Fatalf("%q is not a work-item status any more (%v), so this description should not be "+
				"promising it", want, domain.WorkItemStatusValues())
		}
		if !strings.Contains(desc, "status="+want) {
			t.Errorf("the published blocked_by description does not say a non-empty list makes the "+
				"new wi status=%s:\n    %s", want, desc)
		}
	})

	t.Run("names_the_requeue", func(t *testing.T) {
		if !strings.Contains(strings.ToLower(desc), "requeue") {
			t.Errorf("the published blocked_by description does not say what UNDOES the block:\n"+
				"    %s\nRemoving the last unfinished blocker requeues the wi, and a caller told "+
				"only how to block one has to guess whether the block is permanent.", desc)
		}
	})

	t.Run("names_the_refusal", func(t *testing.T) {
		lower := strings.ToLower(desc)
		if !strings.Contains(lower, "rejected") || !strings.Contains(lower, "not created") {
			t.Errorf("the published blocked_by description does not say that an entry naming no "+
				"work item is rejected AND that the wi is not created:\n    %s\nThe two halves "+
				"together are the transactional promise: a caller whose blocker id was a typo "+
				"gets no work item, rather than an unblocked one they then have to find.", desc)
		}
	})
}
