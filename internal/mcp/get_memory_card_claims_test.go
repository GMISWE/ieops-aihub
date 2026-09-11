package mcp_test

// aihub#543 probe wave 2 — four of the six candidate-assertable sentences of
// `docs/mcp-cards/pf_get_memory.md`, all of them about what this tool does at
// the MCP hop:
//
//	"The parenthetical is the contract: it names where the value comes from,
//	 which is the same reason `pf_update_work_item`'s `resources_version` names
//	 `pf_get_work_item`."
//	    -> TestGetMemoryIdDescriptionNamesWhereTheValueComesFrom
//	"`internal/mcp/tools_memory.go` (`getMemorySchema`) publishes it; the handler
//	 rejects an empty value and calls `pkg/client/client.go` (`GetMemory`) ->
//	 `GET /v1/memories/<id>` …"
//	    -> TestGetMemoryRefusesAnEmptyIdBeforeAnyRequest
//	"It does not activate the memory: incrementing the activation count is
//	 `pf_activate_memory`'s job …"
//	    -> TestGetMemoryMakesNoActivationRequestWhileActivateDoes
//	"`jsonResultCompact` … compact rather than indented …" (the sentence this
//	 wave CORRECTED — see below)
//	    -> TestGetMemoryAndItsIndentingSiblingAreBothCompact
//
// ─── 🔴 One of those sentences was measured FALSE ──────────────────────────
//
// Until this wave the card's hop 5 said:
//
//	"`jsonResultCompact`, not `jsonResult` — compact rather than indented,
//	 because this payload is read by the model and is reached precisely when the
//	 content is long."
//
// The contrast is gone. `jsonResult` marshals through marshalJSON, and 34df071
// changed that function from json.MarshalIndent to json.Marshal for EVERY tool
// in the server — so the two helpers produce byte-identical output today, and
// jsonResultCompact's own doc comment measured itself "vs the default
// MarshalIndent path" that no longer exists (corrected by aihub#592,
// 2026-09-10). Nothing was wrong with the
// behaviour: compact is what the card wants and compact is what every tool
// gives. What was wrong was the sentence, which told a reader this tool differed
// from its siblings in a way it does not, and pointed the next person who wants
// to save tokens at a conversion that has already happened everywhere.
//
// The arm below therefore asserts what IS true and is worth keeping true: this
// tool's answer carries no indentation, and neither does a sibling's, so a
// reintroduced MarshalIndent is red from either side.
//
// ─── Why these four are non-DB ─────────────────────────────────────────────
//
// Every one of them is a claim about what leaves this process — a published
// description, a refusal issued before any request, the set of requests a call
// makes, the bytes of the result. §3.3's first rule picks the shape; a database
// would add nothing a fake aihub cannot already record, and the absence claims
// (no request at all; no activation POST) are only observable on a recorder.
//
//	GOWORK=off go test ./internal/mcp/ -run TestGetMemory -count=1 -v

import (
	"net/http"
	"os"
	"regexp"
	"strings"
	"testing"
)

// getMemoryCardPath is the published side of all four claims.
const getMemoryCardPath = "../../docs/mcp-cards/pf_get_memory.md"

// getMemoryCard returns the card's prose, fences stripped and whitespace
// collapsed.
//
// The fence goes first because the machine block opens with three backticks and
// would put every backtick pairing after it off by one — the arms below read
// tool and parameter names out of backticked spans, and with the fence left in
// they would find none and report the card as publishing nothing.
func getMemoryCard(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile(getMemoryCardPath)
	if err != nil {
		t.Fatalf("read %s: %v — the card is the published side of every claim here, so an "+
			"unreadable card is a failure and not an empty pass", getMemoryCardPath, err)
	}
	var kept []string
	inFence := false
	for _, line := range strings.Split(string(raw), "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "```") {
			inFence = !inFence
			continue
		}
		if !inFence {
			kept = append(kept, line)
		}
	}
	return strings.Join(strings.Fields(strings.Join(kept, "\n")), " ")
}

// The published-description reader this file needs, publishedParamDescriptionFor,
// lives in internal/mcp/pr_gh_boundary_test.go — an identical helper landed there
// in the same wave (aihub#571, PR #465) while this file was being written, and the
// merge surfaced it as a redeclaration rather than as a conflict git could show.
// Kept THEIRS and deleted the copy here: two helpers with one meaning is the
// duplication this repo's own gates exist to refuse, and the reader is not a
// property of either card.

// TestGetMemoryIdDescriptionNamesWhereTheValueComesFrom is the hop-0-1 sentence
// about the parenthetical, quantified over BOTH pairs it names.
//
// 🔴 Quantified over the pair rather than asserted for `memory_id` alone,
// because the card's claim is that this is a CONVENTION — "the same reason
// pf_update_work_item's resources_version names pf_get_work_item". An arm
// checking one of the two would go green while the sentence's own evidence
// rotted away, which is the shape §3.1 records for aihub#507: a claim named for
// one tool is a claim the other tool can silently lose.
//
// Mutants, all applied to the tree and run (2026-09-10):
//
//	M1  getMemorySchema's memory_id description drops "pf_recall"      RED
//	M2  resources_version's description drops "pf_get_work_item"       RED (the pair)
//	M3  the card cites a different producer for resources_version     RED (publication)
//	M4  green control: reword the sentence keeping all three names     GREEN
func TestGetMemoryIdDescriptionNamesWhereTheValueComesFrom(t *testing.T) {
	card := getMemoryCard(t)

	// The producing tool for this card's own parameter, off the card's hop-1 row.
	m := regexp.MustCompile("the `id` of a (pf_[a-z_]+) item").FindStringSubmatch(card)
	if m == nil {
		t.Fatalf("%s no longer says which tool's item the `memory_id` value comes from. That "+
			"parenthetical IS the contract this sentence is about — with it gone the card "+
			"promises a bare id and this arm has nothing to require of the description.",
			getMemoryCardPath)
	}
	// The comparison pair the sentence itself cites.
	pair := regexp.MustCompile("`(pf_[a-z_]+)`'s `([a-z_]+)` names `(pf_[a-z_]+)`").FindStringSubmatch(card)
	if pair == nil {
		t.Fatalf("%s no longer cites the second instance of this convention. The sentence's whole "+
			"claim is that naming the producer is a convention rather than a one-off, and the "+
			"citation is the only evidence for it that a reader can check.", getMemoryCardPath)
	}

	for _, want := range []struct{ tool, param, names string }{
		{"pf_get_memory", "memory_id", m[1]},
		{pair[1], pair[2], pair[3]},
	} {
		desc := publishedParamDescriptionFor(t, want.tool, want.param)
		if !strings.Contains(desc, want.names) {
			t.Errorf("%s publishes %s as %q, which does not name %s — the tool a caller has to "+
				"call to obtain the value.\nThe card states this as a convention holding for both "+
				"pairs; a description that only says \"Memory ID\" leaves a caller to guess "+
				"which of several id-shaped things belongs here, and every wrong guess answers "+
				"404 rather than saying what it wanted.", want.tool, want.param, desc, want.names)
		}
	}
}

// TestGetMemoryRefusesAnEmptyIdBeforeAnyRequest is the hop-2-3 sentence, both
// halves: the refusal, and the request the accepted call really makes.
//
// 🔴 The zero-request assertion is the discriminating one. "An empty id is
// refused" is equally true of a handler that forwards it, gets a 404 from a
// route that does not match, and relays that — and the difference matters,
// because this tool is reached from a recall loop: one wasted round trip per
// truncated item is the cost of the escape hatch being cheap. The route is read
// off the card so the shape a caller is promised is the shape observed.
//
// Mutants, all applied to the tree and run (2026-09-10):
//
//	M1  the handler drops its empty-value check                        RED (refusal + request)
//	M2  the client builds a query parameter instead of a path segment  RED (route)
//	M3  the card publishes a different route                           RED (publication)
//	M4  green control: reword the hop-2-3 paragraph, same route        GREEN
func TestGetMemoryRefusesAnEmptyIdBeforeAnyRequest(t *testing.T) {
	// The route, off the card: `GET /v1/memories/<id>`.
	m := regexp.MustCompile("`GET (/v1/[a-z_]+)/<id>`").FindStringSubmatch(getMemoryCard(t))
	if m == nil {
		t.Fatalf("%s no longer publishes the route this tool reads from, so the observed request "+
			"below has nothing to be compared against", getMemoryCardPath)
	}
	route := m[1]

	// ── the refusal ─────────────────────────────────────────────────────────
	f := newFakeAihub(t)
	res, isErr := callTool(t, f, "pf_get_memory", map[string]any{"memory_id": ""})
	if !isErr {
		t.Errorf("pf_get_memory(memory_id=\"\") succeeded (%v). An empty id cannot name a memory, "+
			"so the only thing forwarding it can produce is a round trip that fails at the far "+
			"end — and this tool is called once per truncated recall item, so a request that "+
			"cannot succeed is a cost paid per item.", res)
	}
	if text, _ := res["_raw"].(string); !strings.Contains(text, "memory_id") {
		t.Errorf("the refusal does not name the parameter: %q. A caller holding several id-shaped "+
			"values cannot act on \"required\" without knowing which one is missing.", text)
	}
	if paths := f.paths(); len(paths) != 0 {
		t.Errorf("pf_get_memory(memory_id=\"\") was refused and still made %d request(s) (%v); the "+
			"card says the handler rejects the empty value, which means before the call",
			len(paths), paths)
	}

	// ── the accepted call ───────────────────────────────────────────────────
	g := newFakeAihub(t)
	g.on(route+"/mem_probe", func(map[string]any) (int, any) {
		return http.StatusOK, map[string]any{"id": "mem_probe", "content": "full body"}
	})
	callToolText(t, g, "pf_get_memory", map[string]any{"memory_id": "mem_probe"})
	calls := g.recorded()
	if len(calls) != 1 {
		t.Fatalf("pf_get_memory made %d request(s) (%v), want exactly 1 — the card calls this a "+
			"single by-id read with no body and no credentials", len(calls), g.paths())
	}
	if calls[0].Method != http.MethodGet || calls[0].Path != route+"/mem_probe" {
		t.Errorf("pf_get_memory made %s %s, want GET %s. The id travels as a PATH SEGMENT: a "+
			"query parameter would reach a route that does not exist and answer 404, which is "+
			"the same answer a deleted memory gives.",
			calls[0].Method, calls[0].Path, route+"/mem_probe")
	}
}

// TestGetMemoryMakesNoActivationRequestWhileActivateDoes is the hop-4 sentence
// about what this read does NOT do.
//
// 🔴 The positive half is not decoration. "pf_get_memory sends no activation
// request" is trivially true of a build where nothing activates anything — which
// is what a renamed or deleted activation route looks like from inside an
// absence check. Driving pf_activate_memory against the same recorder is what
// makes the absence a decision about this tool rather than an observation about
// the server, and the card names that tool as the one whose job it is.
//
// Mutants, all applied to the tree and run (2026-09-10):
//
//	M1  pf_get_memory's handler also calls ActivateMemory              RED (absence)
//	M2  pf_activate_memory stops posting to the activation route       RED (control)
//	M3  the card names a different tool as the activating one          RED (publication)
//	M4  green control: reword the sentence, same tool name             GREEN
func TestGetMemoryMakesNoActivationRequestWhileActivateDoes(t *testing.T) {
	m := regexp.MustCompile("activation count is `(pf_[a-z_]+)`'s job").FindStringSubmatch(getMemoryCard(t))
	if m == nil {
		t.Fatalf("%s no longer names the tool that DOES increment the activation count, so this "+
			"arm's control has no tool to drive and the absence below would be unfalsifiable",
			getMemoryCardPath)
	}
	activator := m[1]

	read := newFakeAihub(t)
	read.on("/v1/memories/mem_probe", func(map[string]any) (int, any) {
		return http.StatusOK, map[string]any{"id": "mem_probe", "content": "full body"}
	})
	callToolText(t, read, "pf_get_memory", map[string]any{"memory_id": "mem_probe"})
	for _, c := range read.recorded() {
		if c.Method != http.MethodGet || strings.Contains(c.Path, "activate") {
			t.Errorf("pf_get_memory made %s %s. Reading the full text of a truncated recall item "+
				"must not disturb its strength: an activation here would reinforce every memory "+
				"whose body a model merely finished reading, and the reinforcement is invisible "+
				"in the response, so the ranking would drift with nothing to point at.",
				c.Method, c.Path)
		}
	}

	act := newFakeAihub(t)
	act.on("/v1/memories/mem_probe/activate", func(map[string]any) (int, any) {
		return http.StatusOK, map[string]any{"activation_count": float64(2)}
	})
	callToolText(t, act, activator, map[string]any{"memory_id": "mem_probe"})
	posted := false
	for _, c := range act.recorded() {
		if c.Method == http.MethodPost && strings.Contains(c.Path, "activate") {
			posted = true
		}
	}
	if !posted {
		t.Errorf("%s made no activation request (%v), so the absence asserted above is not "+
			"evidence about pf_get_memory — it is what a build with no activation path at all "+
			"looks like", activator, act.paths())
	}
}

// TestGetMemoryAndItsIndentingSiblingAreBothCompact is the CORRECTED hop-5
// sentence.
//
// It asserts the property worth keeping — no indentation in what reaches the
// model — and refuses the stale contrast in the card's prose. It was written
// against two helpers (jsonResultCompact drove this tool, jsonResult the
// sibling) because the post-correction claim was that they AGREE; aihub#598
// then folded jsonResultCompact into jsonResult, so both tools now answer
// through the same helper and this arm holds the shared marshal point from two
// tool surfaces at once. TestJSONResultIsTheOnlyJSONResultSerializer
// (json_result_identity_test.go) holds that the fold stays folded.
//
// Mutants, applied to the tree and run (2026-09-10, two-helper era; M2/M3
// re-verified post-fold at aihub#598):
//
//	M1  jsonResultCompact marshals with MarshalIndent                  RED (this tool)
//	    (subject folded away by aihub#598 — kept as the record of what
//	    the two-helper era measured)
//	M2  marshalJSON returns to MarshalIndent                           RED (both tools now)
//	M3  the card's stale "compact rather than indented" returns        RED (publication)
//	M4  green control: reword the corrected sentence, keeping the
//	    claim                                                          GREEN
func TestGetMemoryAndItsIndentingSiblingAreBothCompact(t *testing.T) {
	card := getMemoryCard(t)
	if strings.Contains(card, "compact rather than indented") {
		t.Errorf("%s says \"compact rather than indented\" again. Measured false on 2026-09-10: "+
			"34df071 changed marshalJSON from json.MarshalIndent to json.Marshal, so `jsonResult` "+
			"is compact too and the two helpers are byte-identical. The sentence told a reader "+
			"this tool differed from its siblings in a way it does not, and pointed the next "+
			"token-saving change at a conversion that has already landed everywhere.",
			getMemoryCardPath)
	}

	body := map[string]any{
		"id": "mem_probe", "type": "fact.note",
		"content": "a long body, which is the case this tool is reached in",
		"attrs":   map[string]any{"k": "v"},
	}

	f := newFakeAihub(t)
	f.on("/v1/memories/mem_probe", func(map[string]any) (int, any) { return http.StatusOK, body })
	text, decoded := callToolText(t, f, "pf_get_memory", map[string]any{"memory_id": "mem_probe"})
	if len(decoded) == 0 {
		t.Fatal("pf_get_memory returned an empty object, so the bytes measured below are not a " +
			"memory and the indentation claim is about nothing")
	}
	requireCompactJSON(t, "pf_get_memory", text)

	// The sibling, on the shared marshaller. pf_activate_memory answers through
	// jsonResult — the helper the deleted sentence contrasted this one against.
	g := newFakeAihub(t)
	g.on("/v1/memories/mem_probe/activate", func(map[string]any) (int, any) {
		return http.StatusOK, map[string]any{
			"activation_count": float64(2), "new_stability_days": float64(30),
			"effective_strength": float64(4.2),
		}
	})
	sibling, _ := callToolText(t, g, "pf_activate_memory", map[string]any{"memory_id": "mem_probe"})
	requireCompactJSON(t, "pf_activate_memory", sibling)
}

// requireCompactJSON fails when a tool's result text carries the indentation
// json.MarshalIndent produces.
//
// Checked as a property of the BYTES rather than by comparing against a
// re-marshal: a re-marshal in the test would use whichever marshaller the test
// itself called, which is the same function under test.
//
// The newline is the whole check, deliberately. MarshalIndent's other tell —
// `": "` between a key and its value — also occurs inside ordinary memory
// CONTENT, so an arm looking for it would redden on a memory body that happened
// to quote a JSON fragment.
func requireCompactJSON(t *testing.T, tool, text string) {
	t.Helper()
	if strings.Contains(text, "\n") {
		t.Errorf("%s's result carries a newline, so it is indented: every tool in this server "+
			"marshals compactly since 34df071, and the payloads this pair returns are read by a "+
			"model that pays for the whitespace.\nFirst 200 bytes: %q",
			tool, text[:min(200, len(text))])
	}
}
