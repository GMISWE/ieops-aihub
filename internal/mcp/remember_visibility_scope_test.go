package mcp_test

// aihub#543 probe wave 2 — the two `docs/mcp-cards/pf_remember.md` sentences the
// aihub#495 gate next door does NOT hold.
//
//	"the string is built from `MemoryVisibilityList`, which is also what
//	 `vocabularyErr` renders into the 400 — so the set a caller is shown and the
//	 set the refusal names are one value in one order"
//	    -> TestPublishedVisibilityVocabularyMatchesTheRefusalInOrder
//	"⚠️ **Scoped to this tool** … `pf_save_artifact`'s `visibility` still carries
//	 the same four-value literal and `pf_update_memory`'s names no values at all"
//	    -> TestOnlyTheScopedToolsPublishTheWholeVisibilityVocabulary
//
// 🔴 Both are gaps in a gate that already exists, which is why they are stated
// here rather than folded into it. TestPublishedMemoryVisibilityVocabularyIsThe-
// EnforcedOne compares the two vocabularies as SETS — it sorts both sides — so
// "one value in one ORDER" is exactly the half it cannot fail on; and its scope
// note names the other two tools in a COMMENT, which is the form that rots
// silently the day one of them is fixed.
//
//	GOWORK=off go test ./internal/mcp/ -run 'TestPublishedVisibilityVocabularyMatchesTheRefusal|TestOnlyTheScopedToolsPublish' -count=1

import (
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"testing"

	"github.com/GMISWE/ieops-aihub/internal/domain"
)

// TestPublishedVisibilityVocabularyMatchesTheRefusalInOrder holds the clause
// "one value in one order".
//
// 🔴 The order is not cosmetic, and that is the whole argument for a second arm.
// A caller reads the published list once, and the 400 it gets back is the only
// other place the same list appears; if the two orders diverge, the refusal
// reads as a DIFFERENT vocabulary from the one hop 1 offered, and the natural
// reading of a differing list is that some values were withdrawn. Both sides are
// sorted today — MemoryVisibilityList is sortedKeys, and vocabularyErr sorts its
// own copy — so this arm is the record of a property that currently holds by
// construction on both sides, and goes red the day either stops.
//
// The refusal is read off a REAL rejection, not off vocabularyErr directly:
// vocabularyErr is unexported and, more to the point, an arm that called it
// would stay green on a build where validateMemoryVisibility stopped calling it.
//
// MUTANTS:
//
//	M18 enforcement: build the published string from a reversed copy of
//	    MemoryVisibilityList()                RED  ORDER_DIVERGED
//	M19 enforcement: stop sorting in vocabularyErr and pass an unsorted slice
//	                                          RED  ORDER_DIVERGED (the 400's side)
//	M20 enforcement: return a plain ErrBadRequest with no `allowed` detail
//	                                          RED  the details floor
//	M21 publication: shorten the published description back to a literal
//	    "private|project|team|admin"          RED  ORDER_DIVERGED (and the existing
//	                                               aihub#495 set arm)
//	M22 publication: delete the citation from the card sentence
//	                                          RED  K12
func TestPublishedVisibilityVocabularyMatchesTheRefusalInOrder(t *testing.T) {
	// ── the set the refusal names ──────────────────────────────────────────
	//
	// Driven through domain.Remember with a nil pool: the visibility guard sits
	// above the first query, so an illegal value comes back as an error with the
	// pool untouched. Same instrument the base-strength arms use.
	_, _, err := domain.Remember(t.Context(), nil, &domain.RememberRequest{
		Project: "p", Type: "experience.debug", Content: "c",
		Visibility: "everyone_on_the_internet",
	})
	var ae *domain.AihubError
	if !errors.As(err, &ae) {
		t.Fatalf("an illegal visibility was not refused with an AihubError (%v), so there is "+
			"no refusal to read a vocabulary out of", err)
	}
	if ae.Code != domain.ErrBadRequest {
		t.Fatalf("an illegal visibility answered %s, want %s naming the field (aihub#411 T1-4: "+
			"the CHECK is the last line of defence and never the caller-facing one)",
			ae.Code, domain.ErrBadRequest)
	}
	details, ok := ae.Details.(map[string]any)
	if !ok {
		t.Fatalf("the 400's Details is %T, not the map vocabularyErr builds, so there is no "+
			"`allowed` list to read an order out of", ae.Details)
	}
	rawAllowed, ok := details["allowed"]
	if !ok {
		t.Fatalf("the 400 carries no `allowed` detail (details=%v). The card's claim is about "+
			"the set the refusal NAMES; a refusal that names nothing leaves a caller to guess, "+
			"and leaves this arm nothing to compare.", details)
	}
	refused, ok := rawAllowed.([]string)
	if !ok {
		t.Fatalf("details.allowed is %T, not a []string, so its ORDER is not observable here", rawAllowed)
	}
	if len(refused) < 5 {
		t.Fatalf("the refusal names %d value(s) (%v); memories_visibility_check has carried "+
			"five since migration 0023, so a shorter list means the mirror or the CHECK lost one",
			len(refused), refused)
	}

	// ── the set hop 1 shows ───────────────────────────────────────────────
	tool := publishedTool(t, "pf_remember")
	raw, err := json.Marshal(tool.InputSchema)
	if err != nil {
		t.Fatalf("marshal pf_remember InputSchema: %v", err)
	}
	desc, present := publishedParamDescription(t, raw, "visibility")
	if !present {
		t.Fatal("pf_remember publishes no `visibility` description, and it is a REQUIRED " +
			"parameter — there is then nowhere a caller can learn the vocabulary at all")
	}
	published := orderedPipeRun(desc)
	if len(published) < 5 {
		t.Fatalf("the published description names %d pipe-separated value(s) (%v):\n%s",
			len(published), published, desc)
	}

	// ── one value in one order ────────────────────────────────────────────
	if len(published) != len(refused) {
		t.Fatalf("ORDER_DIVERGED: hop 1 offers %v and the 400 names %v — different lengths, so "+
			"the two are not one vocabulary at all (the aihub#495 set arm owns this direction "+
			"too; this arm reports it because a length mismatch makes the order comparison "+
			"below meaningless)", published, refused)
	}
	for i := range published {
		if published[i] == refused[i] {
			continue
		}
		t.Errorf("ORDER_DIVERGED: hop 1 offers %v and the 400 names %v; they first differ at "+
			"index %d (%q vs %q).\n\n"+
			"Both are meant to be domain.MemoryVisibilityList() — sortedKeys on one side, a "+
			"sorted copy inside vocabularyErr on the other. A caller who reads the offered list "+
			"once and then reads a refusal in a different order has no way to tell a reordering "+
			"from a withdrawal, which is the failure the derivation exists to prevent.",
			published, refused, i, published[i], refused[i])
		break
	}
}

// orderedPipeRun extracts the pipe-separated value set from a description
// WITHOUT sorting it.
//
// Deliberately a near-twin of pipedVocabularyIn next door rather than a reuse of
// it: that helper sorts its result, which is precisely the information this arm
// exists to compare. Sharing it would have made this test pass by construction.
func orderedPipeRun(desc string) []string {
	best := ""
	for _, field := range strings.Fields(desc) {
		trimmed := strings.Trim(field, ".,;:()`\"'")
		if strings.Contains(trimmed, "|") && len(trimmed) > len(best) {
			best = trimmed
		}
	}
	if best == "" {
		return nil
	}
	out := []string{}
	for _, v := range strings.Split(best, "|") {
		if v = strings.TrimSpace(v); v != "" {
			out = append(out, v)
		}
	}
	return out
}

// visibilityVocabOutOfScope is the OTHER half of the aihub#495 gate's
// population: every tool that publishes the `visibility` column and is not in
// visibilityVocabTools, with how many values its description names today.
//
// 🔴 A table, not a comment. The scope note in visibility_vocab_publication_test.go
// records these two tools in prose, and prose is what stops being true without
// anything going red — so the day somebody widens pf_save_artifact's literal and
// forgets the map entry, the card's sentence about it becomes false in silence.
// Here the count is the claim, and the arm below checks it both ways.
//
// An entry is a statement that the tool is covered by SOMEBODY ELSE's work item,
// not that four values are correct. It is removed, not edited, when the tool
// joins visibilityVocabTools.
var visibilityVocabOutOfScope = map[string]int{
	// "private|project|team|admin (default: project)" — the same four-value
	// literal pf_remember carried before aihub#495.
	"pf_save_artifact": 4,
	// "New visibility (omit to keep current)" — names no values at all.
	"pf_update_memory": 0,
}

// TestOnlyTheScopedToolsPublishTheWholeVisibilityVocabulary partitions every
// tool that publishes `visibility` into the two tables and checks the partition
// both ways.
//
// This is the §3.1 rule for a hand-written map applied to a map that already
// existed with only one direction: "the out-of-scope tools must be NAMED rather
// than silently skipped, and the map must be checked both ways". The population
// is derived from the live tool list, so a THIRD tool that starts publishing the
// column is red here rather than quietly uncovered — which is the case a
// two-name comment cannot see.
//
// MUTANTS:
//
//	M23 enforcement: widen pf_save_artifact's literal to all five values
//	                                          RED  out-of-scope/pf_save_artifact
//	                                               (count 5, table says 4)
//	M24 enforcement: delete the pf_save_artifact entry from the table
//	                                          RED  UNCLASSIFIED_VISIBILITY_TOOL
//	M25 enforcement: add pf_save_artifact to visibilityVocabTools without
//	    removing it from this table            RED  DOUBLE_CLASSIFIED
//	M26 enforcement: drop floorVisibilityTools' worth of tools by pointing the
//	    walk at an empty tool list             RED  the floor
//	M27 publication: delete the citation from the card's ⚠️ scope bullet
//	                                          RED  K12
func TestOnlyTheScopedToolsPublishTheWholeVisibilityVocabulary(t *testing.T) {
	// floorVisibilityTools bounds how many tools the walk found publishing the
	// column. Three today (pf_remember, pf_save_artifact, pf_update_memory); a
	// walk that found fewer is reading the wrong tool list, and every partition
	// verdict below would then be an artefact.
	const floorVisibilityTools = 3

	legal := domain.MemoryVisibilityList()
	if len(legal) < 5 {
		t.Fatalf("domain.MemoryVisibilityList() returned %v — fewer than the five values the "+
			"column has carried since migration 0023", legal)
	}

	found := map[string]string{} // tool -> visibility description
	for _, tool := range publishedToolList(t) {
		raw, err := json.Marshal(tool.InputSchema)
		if err != nil {
			t.Fatalf("marshal %s InputSchema: %v", tool.Name, err)
		}
		if desc, present := publishedParamDescription(t, raw, "visibility"); present {
			found[tool.Name] = desc
		}
	}
	if len(found) < floorVisibilityTools {
		t.Fatalf("only %d published tool(s) carry a `visibility` parameter (%v), floor is %d — "+
			"a partition over a population this small is a fact about the walk",
			len(found), sortedToolNames(found), floorVisibilityTools)
	}

	for _, tool := range sortedToolNames(found) {
		desc := found[tool]
		_, scoped := visibilityVocabTools[tool]
		wantCount, out := visibilityVocabOutOfScope[tool]

		switch {
		case scoped && out:
			t.Errorf("DOUBLE_CLASSIFIED: %s is in visibilityVocabTools AND in "+
				"visibilityVocabOutOfScope. The second table's entries are the tools the first "+
				"one does not cover; carrying a tool in both means the count below is checked "+
				"against a description the gate next door is already asserting in full. Delete "+
				"the out-of-scope entry in the same change that added the map entry.", tool)
		case !scoped && !out:
			t.Errorf("UNCLASSIFIED_VISIBILITY_TOOL: %s publishes a `visibility` parameter and is "+
				"in neither table.\n\nThe column has %d legal values and %s's description names "+
				"%d. Either add it to visibilityVocabTools (the aihub#495 gate then asserts the "+
				"full vocabulary and its consequence) or record it here with what it publishes "+
				"today and whose work item owns it.\n\nLive description was:\n%s",
				tool, len(legal), tool, len(orderedPipeRun(desc)), desc)
		case out:
			if got := len(orderedPipeRun(desc)); got != wantCount {
				t.Errorf("out-of-scope/%s: its `visibility` description names %d value(s), and "+
					"this table says %d.\n\nIf it was WIDENED, the fix is one entry in "+
					"visibilityVocabTools and the deletion of this row — and the pf_remember "+
					"card's ⚠️ scope bullet, which states this count in prose, has to be "+
					"rewritten in the same change. If it was narrowed, say why.\n\n"+
					"Live description was:\n%s", tool, got, wantCount, desc)
			}
		}
	}

	// The other direction on both tables: an entry naming a tool that publishes
	// no visibility parameter at all is a stale exemption, and stale is how the
	// first two got here.
	for tool := range visibilityVocabOutOfScope {
		if _, publishes := found[tool]; !publishes {
			t.Errorf("STALE_OUT_OF_SCOPE: visibilityVocabOutOfScope names %s, which publishes no "+
				"`visibility` parameter. An exemption for a parameter that no longer exists "+
				"exempts nothing and reads as coverage.", tool)
		}
	}
	for tool := range visibilityVocabTools {
		if _, publishes := found[tool]; !publishes {
			t.Errorf("STALE_IN_SCOPE: visibilityVocabTools names %s, which publishes no "+
				"`visibility` parameter", tool)
		}
	}
}

func sortedToolNames(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
