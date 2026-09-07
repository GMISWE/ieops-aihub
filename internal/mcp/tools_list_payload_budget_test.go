package mcp_test

// aihub#419 / T1-10 — one budget over the WHOLE tools/list payload.
//
// ─── Why a whole-payload budget, next to the per-schema one ────────────────
//
// tools_list_wi_schema_size_test.go budgets ONE tool's InputSchema (5,400 B for
// pf_list_work_items) and its header states the arithmetic: a schema sits in the
// prefix of every request, so prose added to a parameter description is a
// standing per-request charge. What that test cannot see is the sum. Fifty tools
// each growing 200 B is +10 kB per request with every individual budget still
// green — the failure mode a per-item limit structurally cannot catch, and the
// reason this file budgets the total as well.
//
// The measurement is the SERIALISED tools/list result, taken from a real session
// rather than from the schema constructors, because that is what the transport
// actually carries: names, descriptions, InputSchemas and the JSON-RPC framing
// around them.
//
// ─── Why the per-tool ceiling here is DERIVED, not a table ─────────────────
//
// Fifty hand-written per-tool numbers would be fifty things to re-baseline, and
// the first person to add a tool would add a fifty-first by copying a neighbour.
// The per-tool arm instead asks a question a table cannot: is any single tool
// out of proportion to the payload it sits in? The ceiling is a SHARE of the
// total, so it is scale-free — adding twenty small tools does not tighten it,
// which a multiple of the median would (the median falls, the ceiling with it,
// and the gate goes red on growth that made the average request cheaper). The
// first draft here did use a median multiple and reported five of today's tools
// as outliers for exactly that reason.
//
// 🔴 If either arm fails, do not just raise the number. The cheap fix is almost
// always to move prose to docs/mcp-tools.md, which is not resident and therefore
// free. Raising a budget is a decision someone should make on purpose, with the
// per-request cost in front of them.
//
// ⚠️ This file is deliberately SEPARATE from universal_contract_gate_test.go so
// it can be reverted on its own: T1-10 was leaned toward in the decision-table
// discussion and never formally adjudicated, and a budget nobody has signed off
// on should be one `git rm` away, not entangled with the contract gate.
//
// No database needed:
//
//	go test ./internal/mcp/ -run TestToolsListPayload -v

import (
	"context"
	"encoding/json"
	"sort"
	"strings"
	"testing"
)

// toolsListPayloadBudget is a CEILING on the serialised tools/list payload, in
// bytes.
//
// Measured 2026-09-07 at 50 tools: see the log line this test prints. The
// budget is that measurement plus roughly a sixth, which is headroom for
// ordinary growth and not for a redesign — an equality would fail on every
// wording tweak and be reflexively re-baselined, which is how a ratchet becomes
// a rubber stamp.
const toolsListPayloadBudget = 100_000

// toolsListPayloadFloor catches the payload failing to render at all, which
// would make the ceiling pass for the worst possible reason.
const toolsListPayloadFloor = 20_000

// toolsListPerToolShareLimit is the largest share of the whole payload any
// single tool may take, in percent.
//
// Measured 2026-09-07: the largest tool is pf_update_work_item at 6,398 B of
// 66,081 B, which is 9.7%. 15 leaves room for the genuinely complex schemas —
// pf_list_work_items and pf_update_work_item publish 21 and 16 parameters — and
// still fails on a tool that would dominate the prefix of every request,
// including the requests of callers that never invoke it.
const toolsListPerToolShareLimit = 15

func TestToolsListPayloadStaysWithinItsWireBudget(t *testing.T) {
	_, tools := newContractGate(t)

	type sized struct {
		name string
		size int
	}
	var sizes []sized
	total := 0
	for _, tool := range tools {
		blob, err := json.Marshal(tool)
		if err != nil {
			t.Fatalf("marshal %s: %v", tool.Name, err)
		}
		sizes = append(sizes, sized{tool.Name, len(blob)})
		total += len(blob)
	}

	if total < toolsListPayloadFloor {
		t.Fatalf("the whole tools/list payload is %d B, under the %d B floor — it is not "+
			"rendering, and the ceiling below would pass vacuously", total, toolsListPayloadFloor)
	}
	if total > toolsListPayloadBudget {
		t.Errorf("tools/list is %d B over %d tools, past the %d B budget by %d B "+
			"(~%d tokens on EVERY request).\n"+
			"Every individual schema budget can be green while this one fails — that is what "+
			"this arm is for. Move prose to docs/mcp-tools.md, which is not resident and "+
			"therefore free, or raise toolsListPayloadBudget deliberately and say why.",
			total, len(tools), toolsListPayloadBudget,
			total-toolsListPayloadBudget, (total-toolsListPayloadBudget)/4)
	}

	sort.Slice(sizes, func(i, j int) bool { return sizes[i].size < sizes[j].size })
	largest := sizes[len(sizes)-1]
	median := sizes[len(sizes)/2].size
	share := 100 * largest.size / total
	if share > toolsListPerToolShareLimit {
		t.Errorf("%s serialises to %d B, %d%% of the whole %d B payload (limit %d%%).\n"+
			"One tool that size is a standing charge on every request for every caller, "+
			"including the ones that never invoke it. Trim its descriptions into "+
			"docs/mcp-tools.md, or raise toolsListPerToolShareLimit on purpose.",
			largest.name, largest.size, share, total, toolsListPerToolShareLimit)
	}

	t.Logf("tools/list: %d B over %d tools (budget %d B, %.1f%% used); median tool %d B, "+
		"largest %s at %d B = %d%% of the payload (limit %d%%)",
		total, len(tools), toolsListPayloadBudget,
		100*float64(total)/float64(toolsListPayloadBudget),
		median, largest.name, largest.size, share, toolsListPerToolShareLimit)
}

// TestToolsListPayloadMeasuresTheSerialisedSession is the liveness arm for the
// measurement itself, and it exists because of a specific way this test could go
// quietly wrong: if it summed the schema CONSTRUCTORS instead of the session's
// answer it would miss the descriptions, the tool names and the framing, and
// would report a number a third of the real one while looking correct.
//
// So it asserts the thing being measured really came from a session: the payload
// must contain a tool name and a description string, neither of which a bare
// InputSchema carries.
func TestToolsListPayloadMeasuresTheSerialisedSessionNotTheSchemas(t *testing.T) {
	h, tools := newContractGate(t)

	raw, err := h.session.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatalf("tools/list: %v", err)
	}
	blob, err := json.Marshal(raw)
	if err != nil {
		t.Fatalf("marshal tools/list result: %v", err)
	}
	var res struct {
		Tools []json.RawMessage `json:"tools"`
	}
	if err := json.Unmarshal(blob, &res); err != nil {
		t.Fatalf("decode tools/list result: %v", err)
	}
	if len(res.Tools) != len(tools) {
		t.Errorf("the serialised tools/list carries %d tool(s) and the iterator saw %d — the "+
			"budget above is measuring a different payload from the one the session sends",
			len(res.Tools), len(tools))
	}
	// A bare InputSchema carries none of these three, so their presence is what
	// distinguishes "the session's answer" from "the schema constructors".
	for _, needle := range []string{"pf_whoami", "description", "inputSchema"} {
		if !strings.Contains(string(blob), needle) {
			t.Errorf("the serialised payload does not contain %q, so it is not a tools/list "+
				"response — whatever the budget is measuring, it is not the wire", needle)
		}
	}
	t.Logf("tools/list serialises to %d B carrying %d tools", len(blob), len(res.Tools))
}
