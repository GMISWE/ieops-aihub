package mcp_test

// aihub#543 probe wave 2 — the two `docs/mcp-cards/pf_remember.md` sentences
// about the SHAPE of what this tool publishes, as opposed to the words in one
// description.
//
//	"**None of them is an enum**, and `type` stopped being one in `aihub#445`…"
//	    -> TestRememberPublishesNoClosedEnumOnAnyParam
//	"`dedup_mode` and `supersedes_memory_id` … neither is enumerated."
//	    -> TestRememberPublishesNoClosedEnumOnAnyParam
//	"The type stays `number` … which is exactly why the description had to say
//	 `integer`"
//	    -> TestPublishedBaseStrengthTypeStaysNumber
//
// Both read a REAL session through `publishedTool`, not
// `cli.RunDumpMCPSchemas`: the generated contract JSON carries no per-property
// descriptions, so it cannot see the string the second arm is about.
//
//	GOWORK=off go test ./internal/mcp/ -run 'TestRememberPublishesNoClosedEnum|TestPublishedBaseStrengthTypeStaysNumber' -count=1

import (
	"encoding/json"
	"sort"
	"strings"
	"testing"

	"github.com/GMISWE/ieops-aihub/internal/domain"
)

// floorRememberProps is how many properties pf_remember publishes. It is here so
// that an absence census cannot answer "no enums" from a schema it failed to
// parse — the failure mode `withdrawnParams`' floor entry exists for, applied to
// a whole property set instead of one name.
//
// It is a FLOOR rather than an equality: a fourteenth parameter is somebody's
// decision to make, and it arrives already covered by the walk below.
const floorRememberProps = 13

// rememberPublishedProps decodes pf_remember's published InputSchema into the
// per-property objects, so a MISSING `enum` key can be told apart from an empty
// one — which `map[string]string` projections in this package cannot do.
func rememberPublishedProps(t *testing.T) map[string]map[string]any {
	t.Helper()
	tool := publishedTool(t, "pf_remember")
	raw, err := json.Marshal(tool.InputSchema)
	if err != nil {
		t.Fatalf("marshal pf_remember InputSchema: %v", err)
	}
	var schema struct {
		Properties map[string]map[string]any `json:"properties"`
		Required   []string                  `json:"required"`
	}
	if err := json.Unmarshal(raw, &schema); err != nil {
		t.Fatalf("decode pf_remember InputSchema: %v", err)
	}
	if len(schema.Properties) < floorRememberProps {
		t.Fatalf("pf_remember publishes %d propert(ies), floor is %d — this walk is not "+
			"reading the schema it thinks it is, and every absence below would be an "+
			"artefact of that rather than a fact about the tool",
			len(schema.Properties), floorRememberProps)
	}
	// The required four, named, for the same reason: a schema whose `properties`
	// happened to decode into unrelated keys would still satisfy the count.
	for _, req := range []string{"project", "type", "content", "visibility"} {
		if _, ok := schema.Properties[req]; !ok {
			t.Fatalf("pf_remember publishes no %q property, so this is not pf_remember's "+
				"schema; it publishes %v", req, sortedKeysOf(schema.Properties))
		}
	}
	return schema.Properties
}

func sortedKeysOf(m map[string]map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// TestRememberPublishesNoClosedEnumOnAnyParam is the card's "none of them is an
// enum", quantified over the property set instead of over `type` alone.
//
// 🔴 The existing arm next door (TestRememberTypeIsNotPublishedAsAClosedEnum)
// checks ONE parameter, which is the shape aihub#507's doc comment records as
// the one that drifts: a single named subject is the only subject that cannot
// silently lose the property. Two of this tool's other parameters are the live
// case — `dedup_mode` really has three legal spellings (`strict|suggest|off`)
// and `visibility` really has five, so both are standing invitations to publish
// a closed set, and `dedup_mode`'s set is not even enforced.
//
// The rule being held is aihub#238's: a value the server does not validate must
// not be published as a closed enum. It is stated here as an absence across the
// whole tool rather than as a fact about `type`, because the sentence in the
// card is about the whole tool.
//
// MUTANTS:
//
//	M1 enforcement: add `"enum": ["strict","suggest","off"]` to dedup_mode's
//	                published property        RED  ENUM_PUBLISHED names dedup_mode
//	M2 enforcement: point the walk at a tool name that does not exist
//	                                          RED  the floor (publishedTool fatals)
//	M3 enforcement: drop floorRememberProps to 0 and hand the walk an empty
//	                property map              RED  the required-four check fatals
//	M4 publication: delete the citation from the card sentence
//	                                          RED  K12 (the sentence goes back to
//	                                               unclassified and the ledger row
//	                                               no longer matches)
func TestRememberPublishesNoClosedEnumOnAnyParam(t *testing.T) {
	props := rememberPublishedProps(t)
	for _, name := range sortedKeysOf(props) {
		if raw, ok := props[name]["enum"]; ok {
			t.Errorf("ENUM_PUBLISHED: pf_remember publishes %q with enum %v.\n\n"+
				"None of this tool's parameters is a closed set at hop 4: `type` is a prefix "+
				"rule (domain.MemoryTypePrefixes plus the memories_type_check CHECK, so the "+
				"accepted set is infinite), `dedup_mode` is not validated at all, and every "+
				"other one is free text. Publishing any of them under the JSON-Schema `enum` "+
				"key states a closed contract nothing keeps — aihub#238's rule, and the reason "+
				"aihub#445 withdrew the 13-name `type` enum. If the list is worth publishing, "+
				"publish it in the description and say it is open.", name, raw)
		}
	}
}

// TestPublishedBaseStrengthTypeStaysNumber holds the OTHER half of the aihub#459
// wording decision: the word `integer` in the description is load-bearing only
// while the published TYPE is still `number`.
//
// 🔴 This is not a duplicate of TestPublishedBaseStrengthRangeIsTheEnforcedOne.
// That arm asserts the word and the refusal; neither of those moves if somebody
// "tidies" the schema by narrowing the type to `integer`. And that edit is the
// quiet one: it would make the description redundant, and a redundant sentence
// is the next thing deleted — after which a caller sending 2.5 gets a 400 with
// nothing in hop 1 to have predicted it, which is the aihub#433 shape exactly.
//
// So the pair is stated as a pair. Both tools are covered, because both reach
// domain.Remember and the refusal is the same one.
//
// MUTANTS:
//
//	M5 enforcement: publish base_strength as `"integer"` on pf_remember
//	                                          RED  pf_remember/base_strength
//	M6 enforcement: drop the word `integer` from the description
//	                                          RED  the description half (and the
//	                                               existing range arm too)
//	M7 enforcement: rename the property so neither half resolves
//	                                          RED  the presence check
//	M8 publication: delete the citation from the card sentence
//	                                          RED  K12
func TestPublishedBaseStrengthTypeStaysNumber(t *testing.T) {
	// The wire type JSON carries. Anchored on the domain constants' Go type
	// rather than typed out: MinBaseStrength/MaxBaseStrength are integral, and
	// the published type is `number` DESPITE that, which is the whole point.
	const wantType = "number"

	for _, tool := range []string{"pf_remember", "pf_update_memory"} {
		t.Run(tool, func(t *testing.T) {
			tl := publishedTool(t, tool)
			raw, err := json.Marshal(tl.InputSchema)
			if err != nil {
				t.Fatalf("marshal %s InputSchema: %v", tool, err)
			}
			var schema struct {
				Properties map[string]struct {
					Type        string `json:"type"`
					Description string `json:"description"`
				} `json:"properties"`
			}
			if err := json.Unmarshal(raw, &schema); err != nil {
				t.Fatalf("decode %s InputSchema: %v", tool, err)
			}
			p, ok := schema.Properties["base_strength"]
			if !ok {
				t.Fatalf("%s publishes no base_strength property, so neither half of this "+
					"pair can be read", tool)
			}
			if p.Type != wantType {
				t.Errorf("%s publishes base_strength as type %q, want %q.\n\n"+
					"The type is what the JSON wire carries, and narrowing it is what makes the "+
					"word `integer` in the description redundant — after which the sentence a "+
					"caller learns the rule from is the next thing deleted. If the narrowing is "+
					"deliberate, it is a schema change: say so, and rewrite the card sentence "+
					"that explains why the word is there.", tool, p.Type, wantType)
			}
			if !strings.Contains(strings.ToLower(p.Description), "integer") {
				t.Errorf("%s publishes base_strength as %q, which no longer says the value must "+
					"be a whole number. With the type left as %q, this description is the ONLY "+
					"place a caller can learn that an in-range %s like 2.5 is a 400 (aihub#459).",
					tool, p.Description, wantType, wantType)
			}
			// Anti-vacuity on the range constants themselves: an empty domain
			// bound would make "integral" meaningless and this arm would still
			// pass on the word alone.
			if domain.MinBaseStrength >= domain.MaxBaseStrength {
				t.Errorf("domain reports base_strength bounds [%g,%g] — no value is both in "+
					"range and integral, so the word asserted above describes nothing",
					float64(domain.MinBaseStrength), float64(domain.MaxBaseStrength))
			}
		})
	}
}
