package mcp_test

// aihub#389 phase 1 — THE gate: every tool the server publishes must disclose
// an argument it does not publish.
//
// ─── Quantified over the live toolset, not over a list ────────────────────
//
// The set of tools comes from `tools/list` over a real in-memory session, so a
// tool added tomorrow is covered the day it is added and a tool registered
// AROUND the shared wrapper fails here. That is the half a source scan cannot
// do: `TestEveryToolIsRegisteredThroughAddTool` below reads the package text
// and can be fooled (a helper, a loop, a rename); this one calls the thing.
//
// ─── What each subtest asserts, and what it deliberately does not ─────────
//
// It asserts the RESPONSE carries `request_adjusted` naming the bogus argument.
// It does NOT assert the call succeeded: most tools need a real aihub and fail
// with a connection error under a nil client, and that is fine — an unknown
// parameter is a plausible cause of the error a caller is staring at, so the
// disclosure must survive on the error path too. That is why the check reads
// every content block rather than only the first.
//
// 🔴 It does NOT assert rejection. Phase 1 reports; rejection is phase 2 and a
// separate work item (see unknown_params.go: rejecting today would break one
// pf_update_step call in five — and `additionalProperties:false` alone would
// reject nothing, because the untyped AddTool path runs no per-call SDK
// validation, aihub#463/aihub#547). A future reader who "fixes" this test to
// expect an error is implementing phase 2 without the number that licenses it.
//
// No database needed:
//
//	go test ./internal/mcp/ -run 'TestEveryRegisteredTool|TestUnknownParams|TestEveryToolIsRegistered' -v

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/GMISWE/ieops-aihub/internal/mcp"
	"github.com/GMISWE/ieops-aihub/pkg/client"
)

// bogusArgName is a name no tool can plausibly publish. Long and specific on
// purpose: a short name like `x` could collide with a real parameter added
// later, and this test would then go green for the wrong reason.
const bogusArgName = "bogus_probe_aihub_389_never_published"

// liveToolSession returns a client session against a fully registered server
// backed by the fake aihub.
//
// ⚠️ A NIL client is NOT usable here, though it is the technique every schema
// test in this package uses. Registration never touches the client, so
// mcp.New(nil, nil) is fine for reading schemas — but this test CALLS every
// tool, and a handler that reaches the client dereferences it: pf_list_projects
// panics with a nil pointer in pkg/client.(*Client).do. A panic would be a red
// arm by accident, from the harness rather than from the defect, so the session
// gets a real client pointed at a fake server.
//
// POLYFORGE_WORKSPACE_ROOT is repointed at a temp dir for the same reason it is
// in claim_handler_wiring_test.go: some handlers write a state file, and the
// live workspace's state directory holds every claimed work item's credentials.
func liveToolSession(t *testing.T) *sdkmcp.ClientSession {
	t.Helper()
	ctx := context.Background()
	t.Setenv("POLYFORGE_WORKSPACE_ROOT", t.TempDir())

	f := newFakeAihub(t)
	server := mcp.New(nil, client.New(f.server.URL, "test-key"))
	cTransport, sTransport := sdkmcp.NewInMemoryTransports()
	serverCtx, cancel := context.WithCancel(ctx)
	t.Cleanup(cancel)
	go func() {
		session, err := server.Connect(serverCtx, sTransport)
		if err != nil {
			return
		}
		_ = session.Wait()
	}()

	client := sdkmcp.NewClient(&sdkmcp.Implementation{Name: "unknown-params-test", Version: "1.0.0"}, nil)
	session, err := client.Connect(ctx, cTransport, nil)
	if err != nil {
		t.Fatalf("client connect: %v", err)
	}
	t.Cleanup(func() { _ = session.Close() })
	return session
}

// allPublishedTools lists every tool over the live session.
func allPublishedTools(t *testing.T, session *sdkmcp.ClientSession) []*sdkmcp.Tool {
	t.Helper()
	var out []*sdkmcp.Tool
	for tool, err := range session.Tools(context.Background(), nil) {
		if err != nil {
			t.Fatalf("tools iteration: %v", err)
		}
		out = append(out, tool)
	}
	if len(out) < 40 {
		// A broken session would return nothing and make every assertion below
		// vacuous. 40 is far below the real count (50 today) and far above zero,
		// so this fails on a broken harness rather than on adding a tool.
		t.Fatalf("only %d tool(s) published — the session is broken, not the toolset", len(out))
	}
	return out
}

// resultText concatenates every text block of a result, because the disclosure
// may be merged into the JSON body OR appended as its own block (an error
// result is a bare string, not an object).
func resultText(res *sdkmcp.CallToolResult) string {
	var b strings.Builder
	for _, c := range res.Content {
		if tc, ok := c.(*sdkmcp.TextContent); ok {
			b.WriteString(tc.Text)
			b.WriteString("\n")
		}
	}
	return b.String()
}

// unknownParamsEntry finds the aihub#389 entry in a response's request_adjusted
// list and returns the names it reports. Returns nil when the response carries
// no such entry.
//
// Parses rather than substring-matching, so a test cannot pass because the
// bogus name happens to appear somewhere else in the body — several tools echo
// their arguments back, which is exactly the coincidence that would make a
// substring assertion green with the mechanism removed.
func unknownParamsEntry(text string) []string {
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || !strings.HasPrefix(line, "{") {
			continue
		}
		var body struct {
			RequestAdjusted []struct {
				Param     string   `json:"param"`
				Requested []string `json:"requested"`
			} `json:"request_adjusted"`
		}
		if err := json.Unmarshal([]byte(line), &body); err != nil {
			continue
		}
		for _, e := range body.RequestAdjusted {
			if e.Param == "unknown_params" {
				return e.Requested
			}
		}
	}
	return nil
}

// TestEveryRegisteredToolDisclosesUnknownParams is the gate. It FAILS on the
// pre-fix tree for all 50 tools: the argument crossed every hop and the
// response was byte-identical to one sent without it.
func TestEveryRegisteredToolDisclosesUnknownParams(t *testing.T) {
	session := liveToolSession(t)
	tools := allPublishedTools(t, session)

	for _, tool := range tools {
		t.Run(tool.Name, func(t *testing.T) {
			res, err := session.CallTool(context.Background(), &sdkmcp.CallToolParams{
				Name:      tool.Name,
				Arguments: map[string]any{bogusArgName: "probe"},
			})
			if err != nil {
				// A transport-level error means the call never reached the handler,
				// so this subtest measured nothing. That is a broken harness, not a
				// missing disclosure, and it must not read as either a pass or as
				// this defect.
				t.Fatalf("transport error calling %s: %v", tool.Name, err)
			}

			reported := unknownParamsEntry(resultText(res))
			if reported == nil {
				t.Fatalf("%s accepted %q and said nothing about it.\n"+
					"The response is indistinguishable from one sent without the argument, so a "+
					"misspelled parameter name looks exactly like a working call — measured on "+
					"aihub#383 as a 200 with the work item unchanged. Every tool must go through "+
					"(*Server).addTool, which computes this against the tool's own published "+
					"schema.\nresponse was: %s",
					tool.Name, bogusArgName, resultText(res))
			}
			if !containsString(reported, bogusArgName) {
				t.Errorf("%s reported unknown params %v, which does not include %q",
					tool.Name, reported, bogusArgName)
			}
		})
	}
}

func containsString(hay []string, needle string) bool {
	for _, h := range hay {
		if h == needle {
			return true
		}
	}
	return false
}

// TestUnknownParamsDisclosureIsSilentOnACleanCall is the negative control, and
// it is the half that stops the mechanism from being a constant.
//
// Without it, `request_adjusted` could be attached to EVERY response and the
// gate above would still be green for all 50 tools — a field that always fires
// carries no information, and domain/request_adjusted.go's whole shape decision
// (absent, not empty, when nothing happened) exists to prevent exactly that.
func TestUnknownParamsDisclosureIsSilentOnACleanCall(t *testing.T) {
	session := liveToolSession(t)

	// pf_whoami publishes no parameters at all and needs no arguments, so a call
	// with an empty map is a clean call by construction.
	res, err := session.CallTool(context.Background(), &sdkmcp.CallToolParams{
		Name:      "pf_whoami",
		Arguments: map[string]any{},
	})
	if err != nil {
		t.Fatalf("transport error: %v", err)
	}
	if reported := unknownParamsEntry(resultText(res)); reported != nil {
		t.Errorf("a call with no arguments reported unknown params %v — the disclosure fires "+
			"unconditionally, so it says nothing about any particular call", reported)
	}
}

// TestUnknownParamsDisclosureKeepsTheServersOwnAdjustments is the aihub#314
// collision guard.
//
// `request_adjusted` is a LIST the server may already have populated on the same
// response — pf_list_work_items served with a clamped `limit` is the live case.
// Writing the aihub#389 disclosure as an object under that key, or assigning
// over it, would destroy the server's disclosure while looking correct: the
// aihub#389 entry would be there, and the caller would never learn its `limit`
// was changed. This asserts the merge APPENDS.
func TestUnknownParamsDisclosureKeepsTheServersOwnAdjustments(t *testing.T) {
	f := newFakeAihub(t)
	f.on("/v1/work_items", func(map[string]any) (int, any) {
		return 200, map[string]any{
			"items": []any{},
			"total": 0,
			// Exactly the shape internal/domain/request_adjusted.go produces.
			"request_adjusted": []any{
				map[string]any{"param": "limit", "requested": 500, "applied": 200},
			},
		}
	})

	result, isErr := callTool(t, f, "pf_list_work_items", map[string]any{
		"project":    "aihub",
		"limit":      500,
		bogusArgName: "probe",
	})
	if isErr {
		t.Fatalf("pf_list_work_items failed: %v", result)
	}

	raw, present := result["request_adjusted"]
	if !present {
		t.Fatalf("no request_adjusted at all: %#v", result)
	}
	entries, ok := raw.([]any)
	if !ok {
		t.Fatalf("request_adjusted is %T, want a list — writing it as anything else destroys "+
			"aihub#314's shape and every entry the server put in it: %#v", raw, raw)
	}

	var params []string
	for _, e := range entries {
		m, ok := e.(map[string]any)
		if !ok {
			t.Fatalf("request_adjusted entry is %T, want an object: %#v", e, e)
		}
		p, _ := m["param"].(string)
		params = append(params, p)
	}
	if !containsString(params, "limit") {
		t.Errorf("the server's own `limit` adjustment is gone; request_adjusted names %v. The "+
			"aihub#389 disclosure overwrote an aihub#314 one, which is this wi's defect committed "+
			"one layer up.", params)
	}
	if !containsString(params, "unknown_params") {
		t.Errorf("the unknown-parameter disclosure is missing; request_adjusted names %v", params)
	}
}

// TestUnknownParamsSurviveTheResponseProjections closes the hop the decision
// asks about explicitly: three tools project their response before returning it
// (list_wi_slim, recall_slim, claim_response_slim), and a projection that
// dropped `request_adjusted` would make the disclosure invisible for them.
//
// ⚠️ What this actually proves, stated because the obvious reading is wrong:
// the aihub#389 disclosure is attached in `(*Server).addTool`, which wraps the
// handler, so it is applied AFTER any projection the handler ran. No projection
// can strip it — that is structural, not tested. What IS worth testing, and
// what this covers, is the OTHER direction: that a `request_adjusted` the
// SERVER sent survives each of the three projections. That one is not
// structural (recall_slim is an opt-in whitelist, and aihub#314's header
// records three fields it has already eaten) and it is the half a future
// whitelist edit could break.
func TestUnknownParamsSurviveTheResponseProjections(t *testing.T) {
	serverAdjustment := []any{map[string]any{"param": "top_k", "requested": 500, "applied": 200}}

	for _, tc := range []struct {
		tool string
		path string
		args map[string]any
		body map[string]any
	}{
		{
			tool: "pf_list_work_items",
			path: "/v1/work_items",
			args: map[string]any{"project": "aihub"},
			body: map[string]any{"items": []any{}, "total": 0, "request_adjusted": serverAdjustment},
		},
		{
			tool: "pf_recall",
			path: "/v1/memories",
			args: map[string]any{"project": "aihub"},
			body: map[string]any{"items": []any{}, "request_adjusted": serverAdjustment},
		},
	} {
		t.Run(tc.tool, func(t *testing.T) {
			f := newFakeAihub(t)
			body := tc.body
			f.on(tc.path, func(map[string]any) (int, any) { return 200, body })

			result, isErr := callTool(t, f, tc.tool, tc.args)
			if isErr {
				t.Fatalf("%s failed: %v", tc.tool, result)
			}
			raw, present := result["request_adjusted"]
			if !present {
				t.Fatalf("%s's response projection dropped the server's request_adjusted "+
					"entirely, so a clamp the server disclosed reaches nobody (aihub#314). "+
					"Response: %#v", tc.tool, result)
			}
			entries, ok := raw.([]any)
			if !ok || len(entries) == 0 {
				t.Fatalf("%s: request_adjusted is %#v, want a non-empty list", tc.tool, raw)
			}
		})
	}
}

// TestEveryToolIsRegisteredThroughAddTool is the structural half: no tool may be
// added around the shared wrapper.
//
// ⚠️ Weaker than the behavioural gate above and kept anyway, for one reason: it
// names the fix. The behavioural test says "this tool discloses nothing", which
// is the symptom; this one says "you called s.mcp.AddTool directly", which is
// the cause. Where they disagree, the behavioural one is authoritative.
//
// The floor count is the liveness arm. A scanner that finds nothing exits 0
// exactly like a clean repo, so "zero bare calls" alone would also pass if the
// walk were broken, the file list empty, or every tool deleted.
func TestEveryToolIsRegisteredThroughAddTool(t *testing.T) {
	const dir = "."
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read %s: %v", dir, err)
	}

	bare := map[string]int{}
	wrapped := 0
	scanned := 0
	for _, e := range entries {
		n := e.Name()
		if e.IsDir() || !strings.HasSuffix(n, ".go") || strings.HasSuffix(n, "_test.go") {
			continue
		}
		b, readErr := os.ReadFile(filepath.Join(dir, n))
		if readErr != nil {
			t.Fatalf("read %s: %v", n, readErr)
		}
		scanned++
		src := string(b)
		wrapped += strings.Count(src, "s.addTool(")
		if c := strings.Count(src, "s.mcp.AddTool("); c > 0 && n != "server.go" {
			bare[n] = c
		}
	}

	if scanned < 10 {
		t.Fatalf("only scanned %d source file(s) — the walk is broken, and both counts below "+
			"would be meaningless", scanned)
	}
	// LIVENESS. Not a style check: without a floor, deleting every tool or
	// breaking the walk would satisfy "no bare calls" and this test would report
	// green over a server that publishes nothing.
	if wrapped < 40 {
		t.Fatalf("found only %d s.addTool call site(s) across %d file(s) — far below the ~50 "+
			"tools this server registers, so this scan is not seeing the registration code and "+
			"its zero-bare-calls verdict means nothing", wrapped, scanned)
	}
	for file, count := range bare {
		t.Errorf("%s calls s.mcp.AddTool directly %d time(s). A tool registered around "+
			"(*Server).addTool loses the aihub#389 unknown-parameter disclosure while every "+
			"other tool keeps it, and nothing in its response says so. Use s.addTool.",
			file, count)
	}
	// server.go must keep exactly one, inside the wrapper: zero would mean the
	// wrapper stopped registering anything, and more than one would mean a second
	// registration path grew next to it.
	b, err := os.ReadFile(filepath.Join(dir, "server.go"))
	if err != nil {
		t.Fatalf("read server.go: %v", err)
	}
	if n := strings.Count(string(b), "s.mcp.AddTool("); n != 1 {
		t.Errorf("server.go contains %d s.mcp.AddTool call(s), want exactly 1 (the one inside "+
			"addTool). Zero means the wrapper registers nothing; more than one means a second "+
			"registration path.", n)
	}
}
