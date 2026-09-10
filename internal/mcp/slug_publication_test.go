package mcp_test

// aihub#590 (2026-09-10) — the slug-support PUBLICATION sweep, both halves.
//
// The behaviour has been true for months and is held elsewhere: pf_recall
// resolves a work-item reference — canonical id or slug — before anything
// compares it to a column (aihub#363; internal/domain/card_claims_wave2_test.go
// `TestRecallResolvesTheWorkItemFilterBeforeComparingIt` at the seam,
// internal/server/recall_work_item_slug_db_test.go
// `TestRecallResolvesWorkItemIdOrSlug` against a database), and pf_read_events
// does the same (aihub#343; internal/server/get_step_history_source_test.go
// `TestListEventsFiltersOnTheResolvedWorkItemID` at the seam,
// internal/server/events_slug_db_test.go
// `TestListEventsBySlug_ReturnsTheSameStreamAsByID` against a database). What
// stayed wrong was the PUBLISHED text, in both directions at once:
//
//   - pf_get_step's description actively denied the capability — "pass THAT to
//     pf_recall / pf_read_events, which return nothing for a slug" — stale
//     since aihub#343/aihub#363 landed, recorded unfixed by the aihub#385
//     audit (§3.2 item 17), and carried through two probe waves because a
//     probe pinning the then-current wording would have arrived red on the
//     day it was corrected;
//   - pf_recall's and pf_read_events' own work_item_id properties hid it —
//     "Filter by work item ID" / "Work item ID (or use project)" — saying
//     nothing about the slug every human and skill actually types.
//
// A tool description is the only hop the model reads, so a denial there
// defeats a capability as effectively as removing it: the caller either burns
// a pf_get_work_item round-trip for a canonical id nothing needs, or skips the
// filter and reads the unfiltered answer as the filtered one. These arms pin
// the corrected text off a live tools/list — the same instrument the
// contract-card gate trusts — so the old wording cannot drift back green.
//
// MUTANTS (applied to this tree; the verdict is what ran):
//
//	M1 publication: restore "…; pass THAT to pf_recall / pf_read_events, which
//	   return nothing for a slug" in tools_step.go
//	                              RED  TestSlugAcceptanceIsNotDeniedByGetStep
//	                                   + TestSlugDenialIsNowhereInThePublishedSurface
//	M2 publication: revert pf_recall's work_item_id prop to
//	   "Filter by work item ID"   RED  TestSlugAcceptanceIsPublishedByRecall
//	M3 publication: revert pf_read_events' work_item_id prop to
//	   "Work item ID (or use project)"
//	                              RED  TestSlugAcceptanceIsPublishedByReadEvents
//	M4 enforcement: filter events on the caller's raw parameter again
//	   (f.WorkItemID = &wiID in handleListEvents)
//	                              RED  internal/server
//	                                   TestListEventsFiltersOnTheResolvedWorkItemID
//	M5 enforcement: drop `OR slug = $1` from resolveRecallWorkItemRef
//	                              RED  internal/domain
//	                                   TestRecallResolvesTheWorkItemReferenceBeforeAnyColumnComparison
//	                              (the wave-2 wiring arm
//	                              TestRecallResolvesTheWorkItemFilterBeforeComparingIt stays GREEN on
//	                              this mutant — it pins that resolution happens AHEAD of the router,
//	                              not the resolver's own SQL, which is that file's recorded M2)
//
// No database:
//
//	GOWORK=off go test ./internal/mcp/ -run TestSlug -count=1

import (
	"encoding/json"
	"strings"
	"testing"
)

// staleSlugDenial is the exact clause the aihub#385 audit measured false
// (§3.2 item 17): the pre-aihub#343 / pre-aihub#363 behaviour, published as if
// current. Held by its exact words so the red names the regression rather than
// a paraphrase of it.
const staleSlugDenial = "return nothing for a slug"

// TestSlugAcceptanceIsNotDeniedByGetStep holds pf_get_step's corrected closing
// advice in both directions: the stale denial is gone, and the sentence that
// replaced it states the capability the siblings really have.
func TestSlugAcceptanceIsNotDeniedByGetStep(t *testing.T) {
	desc := publishedTool(t, "pf_get_step").Description
	if strings.Contains(desc, staleSlugDenial) {
		t.Errorf("pf_get_step's description again claims pf_recall / pf_read_events %q — false since "+
			"aihub#343/aihub#363: both resolve id-or-slug server-side, and this clause sends every "+
			"caller through a canonical-id round-trip nothing needs.\n  got: %s", staleSlugDenial, desc)
	}
	for _, w := range []string{"pf_recall / pf_read_events", "resolve either form"} {
		if !strings.Contains(desc, w) {
			t.Errorf("pf_get_step's description no longer carries %q, which is how it states that the "+
				"two sibling tools accept a slug as well as a canonical id. Without it the canonical "+
				"echo reads as a required hop again.\n  got: %s", w, desc)
		}
	}
}

// TestSlugAcceptanceIsPublishedByRecall holds the second half of aihub#590 for
// pf_recall: the work_item_id property says id-or-slug, because domain.Recall
// has resolved both since aihub#363 and the schema is where a caller learns it.
func TestSlugAcceptanceIsPublishedByRecall(t *testing.T) {
	d := schemaProps(t, publishedTool(t, "pf_recall"))["work_item_id"].Description
	if !strings.Contains(d, "slug") {
		t.Errorf("pf_recall's work_item_id property no longer mentions the slug form: %q. "+
			"domain.Recall resolves id-or-slug (aihub#363); a schema that names only the id makes "+
			"the slug every human types look unsupported, which is the aihub#590 defect.", d)
	}
}

// TestSlugAcceptanceIsPublishedByReadEvents is the same arm for pf_read_events,
// whose handler has resolved id-or-slug since aihub#343.
func TestSlugAcceptanceIsPublishedByReadEvents(t *testing.T) {
	d := schemaProps(t, publishedTool(t, "pf_read_events"))["work_item_id"].Description
	if !strings.Contains(d, "slug") {
		t.Errorf("pf_read_events' work_item_id property no longer mentions the slug form: %q. "+
			"handleListEvents resolves id-or-slug (aihub#343); a schema that names only the id makes "+
			"the slug every human types look unsupported, which is the aihub#590 defect.", d)
	}
}

// TestSlugDenialIsNowhereInThePublishedSurface sweeps EVERY published tool —
// description and serialised InputSchema — for the stale clause, so the
// sentence cannot migrate to a tool the three arms above do not read. The
// audit found it in one place; one place is where it was, not where it could
// go.
func TestSlugDenialIsNowhereInThePublishedSurface(t *testing.T) {
	tools := publishedToolList(t)
	if len(tools) < 40 {
		t.Fatalf("tools/list published %d tool(s); the registry publishes 45, so this sweep is "+
			"reading an empty or partial surface rather than proving the phrase is gone", len(tools))
	}
	for _, tool := range tools {
		if strings.Contains(tool.Description, staleSlugDenial) {
			t.Errorf("%s's description carries %q — the claim aihub#590 corrected", tool.Name, staleSlugDenial)
		}
		raw, err := json.Marshal(tool.InputSchema)
		if err != nil {
			t.Fatalf("marshal %s InputSchema: %v", tool.Name, err)
		}
		if strings.Contains(string(raw), staleSlugDenial) {
			t.Errorf("%s's InputSchema carries %q — the claim aihub#590 corrected", tool.Name, staleSlugDenial)
		}
	}
}
