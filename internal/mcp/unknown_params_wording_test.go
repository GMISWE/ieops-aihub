package mcp

// aihub#570 (2026-09-10) — the aihub#389 disclosure WORDING, pinned.
//
// The stderr line said unknown parameters "were forwarded to nothing and had no
// effect". Wave 1 measured the first half false: pf_predict_conflicts (the
// incidental finding this wi was filed from) and pf_update_work_item (the live
// probe in unknown_params.go's own header) forward the caller's whole argument
// map, so an unknown key rides the request to the server and dies at echo's
// c.Bind — it reaches the wire, it just reaches nothing that reads it. "Had no
// effect" was always true; the stated mechanism was not.
//
// aihub#586 then split the truth by family: for pf_remember / pf_save_artifact
// / pf_update_memory the unknown set really is stripped at the MCP boundary
// before the request is built (wire_strip.go), so for those three "nothing was
// forwarded" is true by construction. For every other tool the honest statement
// is the disjunction over its two possible shapes — never placed on the wire
// (typed-body handlers), or sent to the server and dropped at its JSON binding
// (the wholesale forwarders wire_strip.go records) — because the disclosure hop
// knows the tool name, not the handler's body-building style.
//
// These arms pin the corrected wording of both text sites in this repo (the
// stderr line and docs/mcp-tools.md's aihub#389 section) so the old clause
// cannot drift back. The plugin copy
// (plugins/polyforge/skills/_common/references/lifecycle-details.md, "it
// reached nothing and changed nothing") is outside this repo slice's edit
// scope and is reported as a rider on aihub#570 rather than pinned here.
//
// MUTANTS (applied to this tree; the verdict is what ran):
//
//	P1 publication: restore "— they were forwarded to nothing and had no
//	   effect (aihub#389)" as the log line   RED  TestUnknownParamsLogLine…
//	P2 publication: restore "these reached nothing and changed nothing" in
//	   docs/mcp-tools.md                     RED  TestUnknownParamsDocs…
//	E1 enforcement: delete the stripUnpublishedArgs call in addTool
//	                                         RED  wire_strip_family_test.go
//	                                              TestWireStrippedFamilyDrops
//	                                              UnpublishedKeysAndDisclosesThem
//	E2 enforcement: drop pf_remember from wireStrippedTools
//	                                         RED  TestUnknownParamsLogLine…
//	                                              (pf_remember answers the
//	                                              else-branch wording) +
//	                                              TestWireStrippedToolsAre
//	                                              ExactlyTheMemoryWriteFamily
//
// No database:
//
//	GOWORK=off go test ./internal/mcp/ -run TestUnknownParams -count=1

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// staleForwardClaim is the exact clause wave 1 measured false. Held by its
// exact words so the red names the regression rather than a paraphrase.
const staleForwardClaim = "forwarded to nothing"

// TestUnknownParamsLogLineNamesTheMechanismPerFamily holds the stderr sentence
// in both directions for both families: the wire-stripped memory writers say
// the strip, everything else says the disjunction, and NOBODY says the
// measured-false clause.
func TestUnknownParamsLogLineNamesTheMechanismPerFamily(t *testing.T) {
	unknown := []string{"bogus_a", "bogus_b"}

	// Named explicitly rather than looped off wireStrippedTools: reading the
	// map here would make this arm follow a mutation of the map instead of
	// catching it. TestWireStrippedToolsAreExactlyTheMemoryWriteFamily pins the
	// set; this pins what each named member SAYS.
	for _, tool := range []string{"pf_remember", "pf_save_artifact", "pf_update_memory"} {
		line := unknownParamsLogLine(tool, unknown)
		for _, want := range []string{
			"stripped at this boundary before the request was built",
			"nothing was forwarded",
			"had no effect",
			"aihub#586",
		} {
			if !strings.Contains(line, want) {
				t.Errorf("%s's disclosure line no longer says %q — since aihub#586 the strip is what "+
					"makes this family's \"nothing was forwarded\" true, and dropping the sentence "+
					"leaves the caller with no stated mechanism.\n  got: %s", tool, want, line)
			}
		}
	}

	// pf_update_work_item is the wholesale forwarder aihub#389's header
	// measured live; pf_get_step is a typed-path tool whose handler never
	// builds a body from the map. Same wording for both, because the
	// disjunction is the strongest claim that is true for each.
	for _, tool := range []string{"pf_update_work_item", "pf_get_step"} {
		line := unknownParamsLogLine(tool, unknown)
		for _, want := range []string{
			"had no effect",
			"never placed them on the wire",
			"dropped them at its JSON binding",
		} {
			if !strings.Contains(line, want) {
				t.Errorf("%s's disclosure line no longer says %q — the honest mechanism for a "+
					"non-stripped tool is the disjunction over its two shapes, and losing either arm "+
					"turns the sentence back into a single-mechanism claim that is false for some "+
					"tool.\n  got: %s", tool, want, line)
			}
		}
	}

	// Both families: the core of the disclosure and the absence of the old
	// claim. The stale clause is refused EVERYWHERE, including the stripped
	// family, where "nothing was forwarded" states the same fact without
	// reviving the exact words a reader has already learned to distrust.
	for _, tool := range []string{"pf_remember", "pf_save_artifact", "pf_update_memory",
		"pf_update_work_item", "pf_get_step"} {
		line := unknownParamsLogLine(tool, unknown)
		if strings.Contains(line, staleForwardClaim) {
			t.Errorf("%s's disclosure line again claims %q — the clause wave 1 measured false: a "+
				"wholesale-forwarding handler puts every unknown key on the wire, and the server "+
				"drops it at its JSON binding.\n  got: %s", tool, staleForwardClaim, line)
		}
		for _, want := range []string{tool, "bogus_a", "bogus_b", "aihub#389"} {
			if !strings.Contains(line, want) {
				t.Errorf("%s's disclosure line lost %q — the rewording must not cost the disclosure "+
					"its core: the tool, the offending names, and the work item that explains the "+
					"convention.\n  got: %s", tool, want, line)
			}
		}
	}
}

// TestUnknownParamsDocsStateTheHonestMechanism holds docs/mcp-tools.md's
// aihub#389 section to the same standard: the strip and the JSON-binding drop
// are both named, and the old "reached nothing" sentence is gone as a live
// claim (it survives only inside the section's own historical note, quoted).
func TestUnknownParamsDocsStateTheHonestMechanism(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "docs", "mcp-tools.md"))
	if err != nil {
		t.Fatalf("read docs/mcp-tools.md: %v — the doc this arm pins has moved, and an unreadable "+
			"doc must not pass as a corrected one", err)
	}
	doc := string(raw)

	const heading = "## An argument no tool publishes is reported, not rejected (aihub#389)"
	start := strings.Index(doc, heading)
	if start < 0 {
		t.Fatalf("docs/mcp-tools.md no longer carries the section %q; if the disclosure moved, "+
			"point this arm at its new home in the same change", heading)
	}
	section := doc[start:]
	if next := strings.Index(section[len(heading):], "\n## "); next >= 0 {
		section = section[:len(heading)+next]
	}

	for _, want := range []string{
		"stripped at the MCP boundary",
		"dropped at its JSON binding",
		"never leave the process",
	} {
		if !strings.Contains(section, want) {
			t.Errorf("the aihub#389 section of docs/mcp-tools.md no longer says %q — the corrected "+
				"paragraph states the mechanism per family, and losing a family's arm regresses the "+
				"doc toward the single false mechanism aihub#570 removed", want)
		}
	}
	if strings.Contains(section, "these reached nothing and changed nothing") {
		t.Errorf("the aihub#389 section of docs/mcp-tools.md again carries the sentence 'these " +
			"reached nothing and changed nothing' — false for the wholesale forwarders " +
			"wire_strip.go records, and the exact claim aihub#570 corrected")
	}
	if strings.Contains(section, staleForwardClaim) {
		t.Errorf("the aihub#389 section of docs/mcp-tools.md claims %q — the clause wave 1 "+
			"measured false", staleForwardClaim)
	}
}
