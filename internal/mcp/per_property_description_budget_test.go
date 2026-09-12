package mcp_test

// aihub#634 (owner ruling 2026-09-12: option 2, the tight variant, of the four
// candidates aihub#616 put up) - a budget on each SINGLE published parameter
// description, with a reasoned allowlist for the few that are deliberately long.
//
// This file is a pure gate: it changes no description. It measures the same
// serialised session as tools_list_payload_budget_test.go, whose payload
// aihub#616 proved byte-identical to the real stdio wire (dual-channel, at
// commit 8205c91).
//
// Why a per-property arm, next to the arms that already exist:
//
// The whole-payload budget (100,000 B), the floor (20,000 B), the per-tool
// share limit (15%) and the one absolute per-schema budget
// (listWorkItemsSchemaBudget) were all green while aihub#392 landed two
// description edits worth +1,317 B resident on every request - 1.3% of the
// whole budget, invisible to every existing arm, and #392's own report says
// "nothing would have flagged this". A single description is the unit an edit
// actually touches, so it is the unit this gate watches.
//
// Where the number comes from (measured 2026-09-12, 45 tools at 73f2b50, via
// this file's own walker; rerun TestPerPropertyDescriptions with -v for the
// current distribution):
//
//	253 property descriptions, 39,297 B total, median 40 B
//	>500 B: 22   >600 B: 12   >700 B: 5   >800 B: 1
//
// (aihub#616 reported 219 descriptions / 30,008 B on the same day: that count
// is the top-level properties only. This walker also descends into items[] and
// nested object schemas - measured, the top-level subset reproduces #616's
// 219 / 30,008 B exactly - because a nested description is just as resident.)
//
// The owner band for N was 700-800. N = 700 because the population has a gap
// there: nothing measures between 682 and 748, so 700 splits the five
// deliberately long entries (the event vocabulary and the four copies of the
// declared_resources shape text, all allowlisted below with reasons) from a
// tail whose next member is 681 B. Within the band, 700 also minimises the
// silent zone: under N = 800 a new 790 B description - a #392-sized edit -
// would still land without a trace, and the 749-758 B cluster would sit
// unlisted, its length never priced. The tight variant exists to make every
// description past the line carry a written reason.
//
// Allowlist semantics (the aihub#619 double ratchet, same as aitaste's
// per-occurrence directives and pf_contract_lint.py's STALE_BASELINE_ENTRY):
// an entry exempts ONE tool+path up to a recorded bound, never a file or a
// tool. Red in both directions: an entry whose description is gone, renamed,
// or back at or under N is stale and must be deleted (an exemption with
// nothing behind it is worse than the drift it excused); an entry whose
// description outgrows its recorded bound is over budget again. The bound is
// the measured size plus modest headroom for wording maintenance - an exact
// equality would go red on every tweak and train people to re-baseline
// reflexively, which is how a ratchet becomes a rubber stamp (the
// tools_list_payload_budget_test.go lesson). The gate prints how many entries
// are in effect on every run, so the number is visible when it only ever
// goes up.
//
// If this gate is red, in order of preference: shorten the description; move
// the prose to docs/mcp-tools.md (not resident, therefore free) or to a code
// comment at the constructor (same); only then allowlist it, with a reason
// that states what the resident bytes buy on EVERY request - the entries
// below are the bar. Raising perPropertyDescriptionBudget itself is a
// re-adjudication of the aihub#634 ruling, not a fix.
//
// No database needed:
//
//	go test ./internal/mcp/ -run TestPerPropertyDescriptions -v

import (
	"encoding/json"
	"fmt"
	"sort"
	"testing"
)

// perPropertyDescriptionBudget is the ceiling, in wire bytes, on one published
// parameter description. Derivation and date are in the file header; the short
// form: owner band 700-800 (aihub#634), and the measured population is empty
// between 682 and 748, so 700 is the band's round number inside that gap.
const perPropertyDescriptionBudget = 700

// perPropertyPopulationFloor catches the walker going blind, which would turn
// every assertion below vacuous. 45 tools published 253 property descriptions
// on 2026-09-12; a walk that suddenly sees fewer than 150 is broken, not a
// trimmed toolset. (A walk that silently stopped descending into nested
// schemas would still clear this floor at 219 - that regression is caught by
// the allowlist's pf_batch_create_work_items entry instead, whose path only a
// nested walk can reach: losing it goes red as stale.)
const perPropertyPopulationFloor = 150

// descriptionExemption allowlists ONE tool+path up to maxBytes.
type descriptionExemption struct {
	tool     string
	path     string // property path inside inputSchema, as the walker prints it
	maxBytes int    // recorded upper bound: measured size plus modest headroom
	reason   string // what the resident bytes buy on every request
}

// perPropertyDescriptionAllowlist is every description allowed past the
// budget, each with the reason it is worth its per-request price. Measured
// sizes are from 2026-09-12 at 73f2b50.
var perPropertyDescriptionAllowlist = []descriptionExemption{
	{
		tool: "pf_emit_event", path: "event_type",
		// Measured 1,735 B. The bound leaves room for a handful of new
		// vocabulary entries (the text is generated from domain.EventVocabulary,
		// so it grows when the vocabulary does), not for a second paragraph.
		maxBytes: 1_900,
		reason: "the published event vocabulary (aihub#411 ruling T2-5, sized and justified in " +
			"tools_events.go at emitEventTypePropDescription). pf_read_events.types filters silently: a " +
			"typo, a type that never existed and a real event that did not happen all return the same " +
			"empty 200, and across 2,244 transcripts callers sent 34 distinct type strings of which " +
			"about half name nothing any code path emits. The vocabulary must be readable where calls " +
			"are composed, and this description is its only wire surface (pf_read_events.types points " +
			"here instead of repeating it).",
	},

	// The four declared_resources copies are ONE shared entry-shape text
	// (declaredResourcesProp in tools_lifecycle.go) behind four per-tool lead
	// sentences, published on every tool that accepts the array - so one edit
	// to the shared suffix moves all four entries together, and the create
	// and batch copies are literally the same constructed string. The shape
	// text is load-bearing: the server accepts unrecognised declared types
	// without error and simply derives no lock (aihub#238), so a caller who
	// guesses the entry shape gets a 200 and NO protection, and discovers it
	// as a lost parallel-edit race later. Which types take a lock (aihub#416),
	// the uri scheme rules and the repo field (aihub#261) exist only here on
	// the wire. Measured 749-758 B; bounds are measured plus ~12% for wording
	// maintenance on the shared suffix.
	{
		tool: "pf_predict_conflicts", path: "declared_resources",
		maxBytes: 840,
		reason: "shared declared_resources shape text (see the block comment above): wrong shape = " +
			"silent no-lock, and this schema is the caller's only contract.",
	},
	{
		tool: "pf_update_work_item", path: "declared_resources",
		maxBytes: 840,
		reason: "shared declared_resources shape text (see the block comment above): wrong shape = " +
			"silent no-lock, and this schema is the caller's only contract.",
	},
	{
		tool: "pf_create_work_item", path: "declared_resources",
		maxBytes: 840,
		reason: "shared declared_resources shape text (see the block comment above): wrong shape = " +
			"silent no-lock, and this schema is the caller's only contract.",
	},
	{
		tool: "pf_batch_create_work_items", path: "items[].declared_resources",
		maxBytes: 840,
		reason: "shared declared_resources shape text (see the block comment above), reached through " +
			"the batch item schema; also the walker's nested-descent canary (see " +
			"perPropertyPopulationFloor).",
	},
}

// walkSchemaDescriptions visits every description string inside one
// inputSchema, addressing each by a property path. It understands JSON Schema
// structure instead of scanning for the key "description", because a PARAMETER
// may itself be named "description": a key inside "properties" is a property
// name, not a schema keyword.
func walkSchemaDescriptions(path string, schema map[string]any, visit func(path, desc string)) {
	if d, ok := schema["description"].(string); ok && path != "" {
		visit(path, d)
	}
	if props, ok := schema["properties"].(map[string]any); ok {
		for name, sub := range props {
			if m, ok := sub.(map[string]any); ok {
				next := name
				if path != "" {
					next = path + "." + name
				}
				walkSchemaDescriptions(next, m, visit)
			}
		}
	}
	if items, ok := schema["items"].(map[string]any); ok {
		walkSchemaDescriptions(path+"[]", items, visit)
	}
	if ap, ok := schema["additionalProperties"].(map[string]any); ok {
		walkSchemaDescriptions(path+".*", ap, visit)
	}
	for _, comb := range []string{"anyOf", "oneOf", "allOf"} {
		if list, ok := schema[comb].([]any); ok {
			for i, sub := range list {
				if m, ok := sub.(map[string]any); ok {
					walkSchemaDescriptions(fmt.Sprintf("%s<%s:%d>", path, comb, i), m, visit)
				}
			}
		}
	}
}

// descriptionWireBytes is what one description costs on the wire: the length
// of its JSON serialisation minus the two quotes. json.Marshal HTML-escapes by
// default, exactly like the session serialisation, so '<' '>' '&' cost 6 bytes
// each and '"' '\' cost 2, while plain ASCII and UTF-8 pass 1:1 (the
// aihub#616 layer-3 measurement).
func descriptionWireBytes(t *testing.T, desc string) int {
	t.Helper()
	blob, err := json.Marshal(desc)
	if err != nil {
		t.Fatalf("marshal description: %v", err)
	}
	return len(blob) - 2
}

func TestPerPropertyDescriptionsStayWithinWireBudget(t *testing.T) {
	_, tools := newContractGate(t)

	type propKey struct{ tool, path string }
	sizes := map[propKey]int{}
	for _, tool := range tools {
		blob, err := json.Marshal(tool)
		if err != nil {
			t.Fatalf("marshal %s: %v", tool.Name, err)
		}
		var decoded struct {
			InputSchema map[string]any `json:"inputSchema"`
		}
		if err := json.Unmarshal(blob, &decoded); err != nil {
			t.Fatalf("decode %s: %v", tool.Name, err)
		}
		walkSchemaDescriptions("", decoded.InputSchema, func(path, desc string) {
			sizes[propKey{tool.Name, path}] = descriptionWireBytes(t, desc)
		})
	}

	if len(sizes) < perPropertyPopulationFloor {
		t.Fatalf("the walker found %d property descriptions, under the floor of %d - the walk is "+
			"broken (or the session is), and every per-description assertion would pass vacuously",
			len(sizes), perPropertyPopulationFloor)
	}

	allowed := map[propKey]descriptionExemption{}
	for _, e := range perPropertyDescriptionAllowlist {
		k := propKey{e.tool, e.path}
		if _, dup := allowed[k]; dup {
			t.Errorf("allowlist lists %s :: %s twice - delete one entry; a duplicate is a dead "+
				"exemption waiting to go stale unnoticed", e.tool, e.path)
			continue
		}
		allowed[k] = e
	}

	// Arm 1: every description past the budget is either allowlisted within
	// its recorded bound, or red.
	overBudget := 0
	for k, size := range sizes {
		if size <= perPropertyDescriptionBudget {
			continue
		}
		overBudget++
		e, ok := allowed[k]
		if !ok {
			t.Errorf("%s parameter %q publishes a %d B description, past the %d B per-property "+
				"budget by %d B. Those bytes are resident in the prefix of EVERY request of every "+
				"caller, including the ones that never invoke %s (1 ASCII byte = 1 wire byte = "+
				"roughly 0.25 tokens per request, aihub#616). Shorten it, or move the prose to "+
				"docs/mcp-tools.md or a code comment (not resident, therefore free). Only if the "+
				"length itself is load-bearing at call-composition time, add a "+
				"perPropertyDescriptionAllowlist entry with a measured bound and a reason that "+
				"clears the bar the existing entries set.",
				k.tool, k.path, size, perPropertyDescriptionBudget,
				size-perPropertyDescriptionBudget, k.tool)
			continue
		}
		if size > e.maxBytes {
			t.Errorf("%s :: %s is allowlisted up to %d B but now publishes %d B (+%d over its "+
				"recorded bound). The bound is measured size plus headroom for wording maintenance, "+
				"not a growth budget: shorten the edit, or re-measure and raise this entry's "+
				"maxBytes deliberately, updating its reason to say what the new bytes buy.",
				k.tool, k.path, e.maxBytes, size, size-e.maxBytes)
		}
	}

	// Arm 2 (the other direction of the ratchet): a stale allowlist entry is
	// red, exactly like an aitaste directive that covers nothing and
	// pf_contract_lint.py's STALE_BASELINE_ENTRY (aihub#619).
	for k, e := range allowed {
		size, present := sizes[k]
		if !present {
			t.Errorf("allowlist entry %s :: %s matches no published parameter description - the "+
				"property was removed or renamed. Delete or retarget the entry in the same change: "+
				"an exemption with nothing behind it is worse than the drift it excused, because "+
				"the next description landing on this path inherits %d B nobody re-justified.",
				k.tool, k.path, e.maxBytes)
			continue
		}
		if size <= perPropertyDescriptionBudget {
			t.Errorf("allowlist entry %s :: %s now measures %d B, within the %d B budget - it no "+
				"longer needs an exemption. Delete the entry so the ratchet can move down; an "+
				"exemption nobody needs is how an allowlist only ever grows.",
				k.tool, k.path, size, perPropertyDescriptionBudget)
		}
	}

	// The count printed every run, so growth of the allowlist is visible in
	// the log even when every arm is green - plus the distribution the next
	// re-derivation of N will want.
	var all []int
	total := 0
	over := map[int]int{500: 0, 600: 0, 800: 0}
	largest, largestKey := 0, propKey{}
	for k, size := range sizes {
		all = append(all, size)
		total += size
		for cut := range over {
			if size > cut {
				over[cut]++
			}
		}
		if size > largest {
			largest, largestKey = size, k
		}
	}
	sort.Ints(all)
	t.Logf("per-property descriptions: %d published, %d B total, median %d B; %d over the %d B "+
		"budget, allowlist %d entries in effect; >500 B: %d, >600 B: %d, >800 B: %d; largest "+
		"%s :: %s at %d B",
		len(all), total, all[len(all)/2], overBudget, perPropertyDescriptionBudget,
		len(allowed), over[500], over[600], over[800],
		largestKey.tool, largestKey.path, largest)
}
