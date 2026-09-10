package mcp_test

// aihub#543 probe wave 1 — the `docs/mcp-cards/pf_predict_conflicts.md`
// paragraph about the severity ceiling, read off a REAL session.
//
//	"🔴 **`aihub#416` moved the SEVERITY CEILING for `repo` and `service` entries,
//	 and the description now says so.** A payload of only those two types can no
//	 longer return `hard_block`…"
//	"The entry shape is published through the shared
//	 `internal/mcp/tools_lifecycle.go` (`declaredResourcesProp`)…"
//
// 🔴 WHAT THIS ARM CAN AND CANNOT SEE, said rather than implied. The mapper that
// makes the ceiling true (`derivedLock`, `resourceToLock`) is unexported in
// internal/domain and reaching it from here would mean an exported test-only shim
// in production code — the trade TestDeclaredResourcesProp_SaysExternalRefTakesNoLock
// already refused for the same reason. So the ENFORCED half is held where the
// function lives, by TestReadIntentIsHonouredOnlyOnTheTypesThatDeriveALock (which
// walks the live declared-type vocabulary and reports which types derive a lock)
// and by TestOnlyTheLockTableRuleHardBlocksAndItStopsTheRulesAfterIt (which holds
// that rule 1 is the only rule that can answer hard_block at all). This arm holds
// the PUBLISHED half, and it holds it against the vocabularies the server really
// uses rather than against strings written down here.
//
//	GOWORK=off go test ./internal/mcp/ -run TestPredictPublished -count=1

import (
	"encoding/json"
	"sort"
	"strings"
	"testing"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/GMISWE/ieops-aihub/internal/domain"
)

// ceilingBoundTypes are the declared types whose severity ceiling moved, and
// ceilingFreeTypes are the ones the card says are unaffected. Both are checked
// against domain.DeclaredResourceTypeList() before they are used, so a renamed
// declared type fails here rather than turning a Contains into a silent miss.
var (
	ceilingBoundTypes = []string{"repo", "service"}
	ceilingFreeTypes  = []string{"path", "document", "section"}
)

// TestPredictPublishedSeverityCeilingUsesTheEnforcedVocabularies is the
// published half of the ceiling paragraph.
//
// 🔴 NOT ONE SEVERITY VALUE IS WRITTEN DOWN IN THIS FILE. Each is spelled from
// the domain constant, which is the base-strength precedent: a test carrying its
// own copy of the value goes green on the day the value moves, and that is the
// one day it was needed. Here that matters more than usual, because the whole
// paragraph exists to say that a caller cannot detect this change from the
// response — so the description is the only place the promise lives.
//
// MUTANTS (run against this tree; the verdict is what happened):
//
//	M12 publication: delete the "SEVERITY CEILING CHANGED" headline from the
//	    pf_predict_conflicts description     RED  states_the_ceiling. ⚠️ It was
//	                                              GREEN before that arm existed: the
//	                                              remaining prose still named both
//	                                              types, all three severities and the
//	                                              reading rule, so everything survived
//	                                              except the conclusion. The arm was
//	                                              written because of this mutant
//	M13 publication: drop "judge for yourself" from the description
//	                                         RED  reading_rule
//	M14 enforcement: change domain.SeverityInfo's value to "notice"
//	                                         RED  names_the_enforced_severities —
//	                                              the description still says "info"
//	                                              while the server answers "notice"
//	M15 enforcement: rename the declared type "service" to "svc" in
//	    declaredResourceTypes                RED  the vocabulary floor
func TestPredictPublishedSeverityCeilingUsesTheEnforcedVocabularies(t *testing.T) {
	tool := publishedTool(t, "pf_predict_conflicts")
	desc := tool.Description

	// ── Floor 1: the vocabulary this arm indexes into must be the live one.
	live := map[string]bool{}
	for _, typ := range domain.DeclaredResourceTypeList() {
		live[typ] = true
	}
	for _, typ := range append(append([]string{}, ceilingBoundTypes...), ceilingFreeTypes...) {
		if !live[typ] {
			t.Fatalf("this arm is written about the declared type %q, which is not in the live "+
				"vocabulary %v. Every assertion below is a substring search for that name, so a type "+
				"that has been renamed turns each of them into a search for a word the description has "+
				"no reason to contain — a red for the wrong reason today and a green for the wrong "+
				"reason as soon as somebody 'fixes' it.", typ, domain.DeclaredResourceTypeList())
		}
	}

	// ── Floor 2: a description this walk cannot read makes every Contains below
	// fail for a reason that has nothing to do with the ceiling.
	if len(desc) < 200 {
		t.Fatalf("pf_predict_conflicts publishes a %d-character description (%q). The ceiling "+
			"paragraph is a published PROMISE — aihub#416 §5.3's required public declaration — and a "+
			"description too short to hold it is the failure this arm exists to report, not a "+
			"precondition for reporting one.", len(desc), desc)
	}

	t.Run("names_the_enforced_severities", func(t *testing.T) {
		for _, sev := range []domain.ConflictSeverity{
			domain.SeverityHardBlock, domain.SeveritySoftBlock, domain.SeverityInfo,
		} {
			if !strings.Contains(desc, string(sev)) {
				t.Errorf("the published description never says %q, and that is the value pf-work's "+
					"pre-claim gate branches on. The paragraph's whole argument is that this change is "+
					"invisible to a caller — same parameters, same response shape, same 200 — so a "+
					"description that stops naming the severity it is about publishes nothing.\n"+
					"description: %s", string(sev), desc)
			}
		}
	})

	t.Run("names_the_bound_types", func(t *testing.T) {
		for _, typ := range ceilingBoundTypes {
			if !strings.Contains(desc, typ) {
				t.Errorf("the published description never names the declared type %q, whose ceiling "+
					"moved. A caller that still reads hard_block as reachable for it will treat "+
					"severity=%q as \"checked, no conflict\" — which is the exclusion aihub#416 removed.",
					typ, string(domain.SeverityInfo))
			}
		}
		for _, typ := range ceilingFreeTypes {
			if !strings.Contains(desc, typ) {
				t.Errorf("the published description never names %q among the types that are unaffected. "+
					"A ceiling published without its exceptions reads as a ceiling on everything, and a "+
					"caller that stops trusting hard_block for a path has stopped reading the one answer "+
					"this tool still gives from the lock table.", typ)
			}
		}
	})

	// 🔴 ADDED AFTER A MUTANT CAME BACK GREEN. Deleting the description's
	// "SEVERITY CEILING CHANGED" headline left every assertion above satisfied —
	// the remaining prose still names both types, all three severities and the
	// reading rule. What it stopped saying was the ceiling itself: that hard_block
	// is no longer REACHABLE for those payloads. A published reason with the
	// conclusion removed is exactly the shape of a promise a caller cannot act on,
	// so the conclusion gets its own arm.
	t.Run("states_the_ceiling", func(t *testing.T) {
		stated := false
		for _, sentence := range strings.Split(desc, ". ") {
			if !strings.Contains(sentence, string(domain.SeverityHardBlock)) {
				continue
			}
			lower := strings.ToLower(sentence)
			if strings.Contains(lower, "no longer") || strings.Contains(lower, "cannot") {
				stated = true
				break
			}
		}
		if !stated {
			t.Errorf("no sentence of the published description says %q is UNREACHABLE for a "+
				"repo-or-service payload. The description may still explain why — that neither type "+
				"derives a lock — and a caller cannot act on a reason with its conclusion removed: "+
				"the ceiling is the part that tells them not to read a missing %q as \"checked, no "+
				"conflict\".\ndescription: %s",
				string(domain.SeverityHardBlock), string(domain.SeverityHardBlock), desc)
		}
	})

	t.Run("reading_rule", func(t *testing.T) {
		lower := strings.ToLower(desc)
		if !strings.Contains(lower, "judge for yourself") {
			t.Errorf("the published description no longer tells a caller how to READ %q on a service. "+
				"The card's sentence is \"somebody else declares it, judge for yourself\", NOT "+
				"\"checked, no conflict\" — and the difference is the whole reason the ceiling had to "+
				"be published rather than measured.\ndescription: %s", string(domain.SeverityInfo), desc)
		}
	})
}

// TestPredictPublishesTheSharedDeclaredResourcesEntryShape holds the card's hop
// 2-3 sentence about WHERE the entry shape comes from.
//
//	"The entry shape is published through the shared
//	 `internal/mcp/tools_lifecycle.go` (`declaredResourcesProp`), which is where
//	 the `type`-versus-lock-type distinction and the per-type URI scheme rules are
//	 stated."
//
// The four aihub#395 arms in resource_schema_test.go assert what
// declaredResourcesProp SAYS — the type-versus-lock-type distinction and the
// per-type URI schemes — by rendering the shared prop directly. None of them
// asserts that pf_predict_conflicts actually publishes THAT prop, and a
// hand-rolled copy on this one tool would leave all four green while the card's
// sentence became false. TestNoResourceArrayIsRegisteredWithoutItemSchema only
// catches the degenerate `prop("array")` form, which a copy would not be.
//
// Read off the live session and compared BETWEEN TOOLS rather than against the
// unexported prop: sharing is a relation among the tools that publish it, and
// the relation is what the sentence promises.
//
// MUTANTS (run against this tree; the verdict is what happened):
//
//	M16 enforcement: publish requestedLocksProp on pf_predict_conflicts instead of
//	    declaredResourcesProp                RED  the entry-shape comparison, naming
//	                                              pf_create_work_item and
//	                                              pf_update_work_item against
//	                                              pf_predict_conflicts
//	M17 walk: point the walk at a declared_resources property no tool publishes
//	                                         RED  the floor (0 publishers, want 3+)
//	M28 publication: drop this arm's citation from the card sentence
//	                                         RED  K12 DEBT_GROWTH
func TestPredictPublishesTheSharedDeclaredResourcesEntryShape(t *testing.T) {
	// Every tool that publishes a top-level declared_resources array, with the
	// entry shape it publishes. Quantified over the live tool list rather than
	// over a written list of four: a fifth tool that grows the field is exactly
	// the case a hand-written pair of names skips.
	shapes := map[string]string{}
	for _, tool := range publishedToolList(t) {
		if items, ok := publishedItemsSchema(t, tool, "declared_resources"); ok {
			shapes[tool.Name] = items
		}
	}

	// The floor. "They all agree" is true of one tool, and true of none.
	if len(shapes) < 3 {
		t.Fatalf("only %d published tool(s) carry a declared_resources array (%v). The shared prop "+
			"is used by the create, batch-create, update and predict tools, so a walk that found "+
			"fewer than three is not reading the schemas it thinks it is — and \"they all agree\" is "+
			"satisfied for free by a set this small.", len(shapes), sortedShapeNames(shapes))
	}
	mine, published := shapes["pf_predict_conflicts"]
	if !published {
		t.Fatalf("pf_predict_conflicts publishes no declared_resources entry shape at all; the tools "+
			"that do are %v. The card's hop 2-3 sentence is about this tool receiving the shared one.",
			sortedShapeNames(shapes))
	}

	for _, name := range sortedShapeNames(shapes) {
		if shapes[name] == mine {
			continue
		}
		t.Errorf("%s and pf_predict_conflicts publish DIFFERENT declared_resources entry shapes.\n"+
			"%s: %s\npf_predict_conflicts: %s\nThe card says the shape reaches this tool through the "+
			"shared declaredResourcesProp, which is what makes the four aihub#395 gates on that prop "+
			"gates on THIS tool. A per-tool copy is how two tools end up describing two contracts, "+
			"and the copy is the one nobody re-reads.", name, name, shapes[name], mine)
	}
}

// publishedItemsSchema returns the marshalled `items` schema of a published
// array property, and whether the tool publishes that property at all.
func publishedItemsSchema(t *testing.T, tool *sdkmcp.Tool, prop string) (string, bool) {
	t.Helper()
	raw, err := json.Marshal(tool.InputSchema)
	if err != nil {
		t.Fatalf("marshal InputSchema for %q: %v", tool.Name, err)
	}
	var schema struct {
		Properties map[string]struct {
			Type  string         `json:"type"`
			Items map[string]any `json:"items"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(raw, &schema); err != nil {
		t.Fatalf("decode InputSchema for %q: %v", tool.Name, err)
	}
	p, ok := schema.Properties[prop]
	if !ok || p.Type != "array" || p.Items == nil {
		return "", false
	}
	items, err := json.Marshal(p.Items)
	if err != nil {
		t.Fatalf("marshal the %s items schema for %q: %v", prop, tool.Name, err)
	}
	return string(items), true
}

func sortedShapeNames(shapes map[string]string) []string {
	out := make([]string, 0, len(shapes))
	for k := range shapes {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
