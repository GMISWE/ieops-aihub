package mcp_test

// aihub#148 — hop 2 measured where it actually happens: the query string that
// leaves this process, observed by the server that receives it.
//
// recall_params_wiring_test.go asserts buildRecallParams in isolation. That is
// the right unit, and it is not sufficient on its own: the tool handler could
// stop calling it, call it with the wrong map, or hand its result to a client
// that drops the query — and every assertion in that file would stay green.
// aihub#309's measured lesson is exactly this shape (a mutant one layer away
// from the defect left four pure-function tests green while the defect stood),
// so these drive the REAL registered tool through the REAL pkg/client and read
// the RawQuery the server saw.
//
// No database needed:
//
//	go test ./internal/mcp/ -run TestWireQuery -v

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/GMISWE/ieops-aihub/internal/mcp"
	"github.com/GMISWE/ieops-aihub/pkg/client"
)

// queryRecorder answers every GET with one fixed, empty-but-valid payload and
// remembers the decoded query string of each request.
//
// It ignores the query entirely when answering, which is the point: a
// difference between two calls cannot have come from the server, and an
// assertion on what it RECORDED cannot be satisfied by anything except the
// parameter having been put on the wire.
type queryRecorder struct {
	mu      sync.Mutex
	queries []url.Values
	server  *httptest.Server
	// body overrides the fixed reply. Only respondWith sets it, and only a test
	// that asserts something about the RESPONSE needs to — the assertions above
	// are about the request, for which any valid payload does.
	body map[string]any
}

func newQueryRecorder(t *testing.T) *queryRecorder {
	t.Helper()
	q := &queryRecorder{}
	q.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q.mu.Lock()
		q.queries = append(q.queries, r.URL.Query())
		reply := q.body
		q.mu.Unlock()
		if reply == nil {
			reply = map[string]any{"items": []any{}, "total": 0, "ready": []any{}}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(reply)
	}))
	t.Cleanup(q.server.Close)
	return q
}

// respondWith replaces the fixed reply for the rest of the test.
func (q *queryRecorder) respondWith(body map[string]any) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.body = body
}

func (q *queryRecorder) last(t *testing.T) url.Values {
	t.Helper()
	q.mu.Lock()
	defer q.mu.Unlock()
	if len(q.queries) == 0 {
		t.Fatal("the MCP handler made no HTTP request at all")
	}
	return q.queries[len(q.queries)-1]
}

// callToolAgainstRecorder invokes one registered tool over a real in-memory MCP
// session backed by the real aihub client, and fails the test if the tool
// refuses the call.
func callToolAgainstRecorder(t *testing.T, q *queryRecorder, tool string, args map[string]any) {
	t.Helper()
	res := callToolAgainstRecorderResult(t, q, tool, args)
	if res.IsError {
		t.Fatalf("call %s failed: %s", tool, toolResultText(t, res))
	}
}

// toolResultText renders a tool result's first text block, which is where both
// a payload and a refusal arrive.
func toolResultText(t *testing.T, res *sdkmcp.CallToolResult) string {
	t.Helper()
	if len(res.Content) == 0 {
		return ""
	}
	if text, ok := res.Content[0].(*sdkmcp.TextContent); ok {
		return text.Text
	}
	return ""
}

// callToolAgainstRecorderResult is the same call, handing back the result
// instead of asserting it succeeded — a refusal is the SUBJECT of aihub#432's
// hop-2 tests, not a failure of their setup.
func callToolAgainstRecorderResult(t *testing.T, q *queryRecorder, tool string, args map[string]any) *sdkmcp.CallToolResult {
	t.Helper()
	ctx := context.Background()

	server := mcp.New(nil, client.New(q.server.URL, "test-key"))
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

	cl := sdkmcp.NewClient(&sdkmcp.Implementation{Name: "wire-query-test", Version: "1.0.0"}, nil)
	session, err := cl.Connect(ctx, cTransport, nil)
	if err != nil {
		t.Fatalf("client connect: %v", err)
	}
	t.Cleanup(func() { _ = session.Close() })

	res, err := session.CallTool(ctx, &sdkmcp.CallToolParams{Name: tool, Arguments: args})
	if err != nil {
		t.Fatalf("call %s: %v", tool, err)
	}
	return res
}

// TestWireQueryRecallCarriesSimilarityThreshold is aihub#148's hop-2 regression,
// through the registered tool rather than the helper it calls.
func TestWireQueryRecallCarriesSimilarityThreshold(t *testing.T) {
	for _, tc := range []struct {
		name  string
		shape any
		want  string
	}{
		{"the value the live probe used", float64(0.99), "0.99"},
		{"a mid-range floor", float64(0.5), "0.5"},
		{"quoted by a client that stringifies numbers", "0.99", "0.99"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			q := newQueryRecorder(t)
			callToolAgainstRecorder(t, q, "pf_recall", map[string]any{
				"project": "aihub", "query": "gateway rate limiting", "similarity_threshold": tc.shape,
			})
			got := q.last(t)
			if got.Get("similarity_threshold") != tc.want {
				t.Fatalf("pf_recall(similarity_threshold=%#v) put %q on the wire, want %q. "+
					"The parameter is published in pf_recall's InputSchema and implemented in "+
					"domain; dropping it here is what made 0.99 and no threshold at all return "+
					"byte-identical results (aihub#148). Full query: %v", tc.shape, got.Get("similarity_threshold"), tc.want, got)
			}
			// The rest of the request must be unchanged — a threshold that also
			// moved the query or the project would be a different bug.
			if got.Get("project") != "aihub" || got.Get("query") != "gateway rate limiting" {
				t.Errorf("threshold changed the rest of the request: %v", got)
			}
		})
	}
}

// TestWireQueryRecallOmitsThresholdByDefault: the OFF default, asserted on the
// wire. Noise queries outscore true hits on this corpus, so there is no safe
// global cutoff and the parameter must stay opt-in.
func TestWireQueryRecallOmitsThresholdByDefault(t *testing.T) {
	q := newQueryRecorder(t)
	callToolAgainstRecorder(t, q, "pf_recall", map[string]any{
		"project": "aihub", "query": "gateway rate limiting",
	})
	if got := q.last(t); got.Has("similarity_threshold") {
		t.Fatalf("a recall with no threshold sent similarity_threshold=%q; it must stay off: %v",
			got.Get("similarity_threshold"), got)
	}
}

// TestWireQueryRecallTopKAcceptsAJSONNumber is aihub#148 defect 2 on pf_recall.
//
// The acceptance value discriminates: `top_k` is published as a string, and the
// server's default page size is 20. A value of 5 that TOOK EFFECT appears on the
// wire as "5"; a value that FELL BACK appears not at all, and the server then
// silently pages at 20. "20" would have been indistinguishable from the default.
func TestWireQueryRecallTopKAcceptsAJSONNumber(t *testing.T) {
	for _, shape := range []any{float64(5), "5"} {
		q := newQueryRecorder(t)
		callToolAgainstRecorder(t, q, "pf_recall", map[string]any{
			"project": "aihub", "query": "gateway rate limiting", "top_k": shape,
		})
		if got := q.last(t); got.Get("top_k") != "5" {
			t.Errorf("pf_recall(top_k=%#v) put top_k=%q on the wire, want \"5\" — the caller's page "+
				"size is dropped and the server's default of 20 applies with nothing to notice: %v",
				shape, got.Get("top_k"), got)
		}
	}
}

// TestWireQueryReadyQueueMaxAcceptsAJSONNumber is aihub#148 defect 2 on
// pf_get_ready_queue.
//
// Discriminating value: handleGetReadyQueue defaults `max` to 10, so 3 tells the
// two outcomes apart. Sending 10 would be green whether or not the parameter
// survived.
func TestWireQueryReadyQueueMaxAcceptsAJSONNumber(t *testing.T) {
	for _, shape := range []any{float64(3), "3"} {
		q := newQueryRecorder(t)
		callToolAgainstRecorder(t, q, "pf_get_ready_queue", map[string]any{
			"project": "aihub", "max": shape,
		})
		if got := q.last(t); got.Get("max") != "3" {
			t.Errorf("pf_get_ready_queue(max=%#v) put max=%q on the wire, want \"3\" — the caller's "+
				"cap is dropped and the server's default of 10 applies silently: %v",
				shape, got.Get("max"), got)
		}
	}
}

// TestWireQueryRecallRefusesUnreadableNumbers is aihub#432's hop-2 regression,
// measured where a caller experiences it: the tool result, and the absence of a
// request.
//
// recall_params_wiring_test.go asserts buildRecallParams refuses. That is the
// right unit and it is not sufficient, for the reason this file's header
// already gives: the handler could stop calling it, or call it and ignore the
// error, and every assertion there would stay green while the request went out
// unfiltered exactly as before. So this drives the REAL registered tool and
// asserts BOTH halves of the refusal — the caller is told, and the query never
// leaves the process.
//
// The second half is the discriminating one. A test that only checked IsError
// would pass on an implementation that refused AFTER making the call, which is
// a different contract: `similarity_threshold` is a filter, and a request the
// server has already answered has already spent the read it should not have.
func TestWireQueryRecallRefusesUnreadableNumbers(t *testing.T) {
	for _, tc := range []struct {
		name  string
		param string
		shape any
	}{
		{"text where a threshold belongs", "similarity_threshold", "notanumber"},
		{"NaN disables the filter from the inside", "similarity_threshold", "NaN"},
		{"a boolean is not a number in any spelling", "similarity_threshold", true},
		{"the same reader guards min_strength", "min_strength", "high"},
		// `recency_weight` had an arm here ("0.4ish"). aihub#469 withdrew the
		// parameter, so there is no longer a reader to guard: an unparseable
		// value for a name this tool does not publish is not refused, it is
		// reported back as an unknown parameter.
	} {
		t.Run(tc.name, func(t *testing.T) {
			q := newQueryRecorder(t)
			res := callToolAgainstRecorderResult(t, q, "pf_recall", map[string]any{
				"project": "aihub", "query": "gateway rate limiting", tc.param: tc.shape,
			})
			if !res.IsError {
				t.Fatalf("pf_recall(%s=%#v) succeeded. A value this hop cannot read must be refused "+
					"naming the parameter: dropping it means the server sees no %s at all, and for "+
					"similarity_threshold that is its OFF value — the caller asked for a filter and "+
					"got an unfiltered page with nothing to notice (aihub#411 T1-2 / aihub#432)",
					tc.param, tc.shape, tc.param)
			}
			if text := toolResultText(t, res); !strings.Contains(text, tc.param) {
				t.Errorf("the refusal does not name the parameter: %q", text)
			}
			q.mu.Lock()
			sent := len(q.queries)
			q.mu.Unlock()
			if sent != 0 {
				t.Errorf("pf_recall(%s=%#v) was refused but still made %d HTTP request(s); a request "+
					"that cannot succeed must not be sent", tc.param, tc.shape, sent)
			}
		})
	}
}

// TestWireQueryRecallRefusesUnreadableBooleans is aihub#464, and it is the test
// above with one type changed — deliberately, because that is the shape of the
// defect.
//
// aihub#432 closed the numeric escape in buildRecallParams and wrote the test
// above for it. The boolean further down the SAME function kept reading through
// boolArg, which discards parseBoolArg's `ok` and answers false. The
// discriminating detail is what false MEANS here: `include_archived` defaults to
// false, so a refused-then-defaulted value produced a request byte-identical to
// one that never mentioned the parameter. Unlike similarity_threshold, whose
// absence at least leaves the page unfiltered and visibly large, this one hands
// back a perfectly ordinary active-only page — there is no artefact in the
// response for a caller to notice.
//
// So both halves are asserted for the same reason they are above, and the second
// is again the discriminating one: a refusal issued AFTER the call would have
// already spent a read the caller's request could not have wanted.
func TestWireQueryRecallRefusesUnreadableBooleans(t *testing.T) {
	for _, tc := range []struct {
		name  string
		shape any
	}{
		{"a typo one keystroke from yes", "yess"},
		{"a number with no boolean reading", float64(2)},
		{"an array is not a boolean in any spelling", []any{true}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			q := newQueryRecorder(t)
			res := callToolAgainstRecorderResult(t, q, "pf_recall", map[string]any{
				"project": "aihub", "query": "gateway rate limiting", "include_archived": tc.shape,
			})
			if !res.IsError {
				t.Fatalf("pf_recall(include_archived=%#v) succeeded. A value this hop cannot read "+
					"must be refused naming the parameter: defaulting it to false sends the server "+
					"a request identical to one that never asked for archived memories, so the "+
					"caller gets the active-only page and nothing anywhere says why (aihub#464)",
					tc.shape)
			}
			if text := toolResultText(t, res); !strings.Contains(text, "include_archived") {
				t.Errorf("the refusal does not name the parameter: %q", text)
			}
			q.mu.Lock()
			sent := len(q.queries)
			q.mu.Unlock()
			if sent != 0 {
				t.Errorf("pf_recall(include_archived=%#v) was refused but still made %d HTTP "+
					"request(s); a request that cannot succeed must not be sent", tc.shape, sent)
			}
		})
	}
}

// TestWireQueryRecallCarriesIncludeArchived is the green half of aihub#464 at the
// wire, and it carries a claim the rejection test cannot: that the FORWARDING
// still works, in every spelling parseBoolArg accepts.
//
// The `false` arms are the ones worth having. `include_archived=false` must
// reach the server as NO PARAMETER — the server reads absence as false, so
// forwarding "false" would be redundant, and a hop that started sending it would
// make "explicitly false" and "unset" the same bytes for a server that may one
// day want to tell them apart. That arm is also the one a naive "reject
// everything unreadable" fix breaks first, since it is the only readable shape
// whose correct outcome is an absent param.
func TestWireQueryRecallCarriesIncludeArchived(t *testing.T) {
	for _, tc := range []struct {
		shape any
		want  string
	}{
		{true, "true"},
		{"true", "true"},
		{float64(1), "true"},
		{false, ""},
		{"false", ""},
		{float64(0), ""},
	} {
		t.Run(fmt.Sprintf("include_archived=%#v", tc.shape), func(t *testing.T) {
			q := newQueryRecorder(t)
			callToolAgainstRecorder(t, q, "pf_recall", map[string]any{
				"project": "aihub", "query": "gateway rate limiting", "include_archived": tc.shape,
			})
			got := q.last(t)
			if got.Get("include_archived") != tc.want {
				t.Errorf("pf_recall(include_archived=%#v) put include_archived=%q on the wire, "+
					"want %q — full query: %v", tc.shape, got.Get("include_archived"), tc.want, got)
			}
			// The flag must not disturb the rest of the request; a boolean that
			// also moved the query or the project would be a different bug.
			if got.Get("project") != "aihub" || got.Get("query") != "gateway rate limiting" {
				t.Errorf("include_archived changed the rest of the request: %v", got)
			}
		})
	}
}

// TestWireQueryRecallStillAcceptsEveryReadableSpelling is the green control for
// the test above, and it is not decoration: a refusal that also refused the
// valid spellings would satisfy every assertion there while breaking the tool
// for every caller. `"0.99"` in particular is the quoted form aihub#148 added
// support for, and it must survive a change that adds a rejection path.
func TestWireQueryRecallStillAcceptsEveryReadableSpelling(t *testing.T) {
	for _, tc := range []struct {
		param string
		shape any
		want  string
	}{
		{"similarity_threshold", float64(0.99), "0.99"},
		{"similarity_threshold", "0.99", "0.99"},
		{"min_strength", float64(1.5), "1.5"},
		{"min_strength", "1.5", "1.5"},
		// `recency_weight` had both spellings here until aihub#469 withdrew it.
		// min_strength keeps this control populated for the numeric-string path,
		// so removing those two arms narrows the table without leaving the
		// green half of the pair vacuous.
	} {
		t.Run(fmt.Sprintf("%s=%#v", tc.param, tc.shape), func(t *testing.T) {
			q := newQueryRecorder(t)
			callToolAgainstRecorder(t, q, "pf_recall", map[string]any{
				"project": "aihub", "query": "gateway rate limiting", tc.param: tc.shape,
			})
			if got := q.last(t).Get(tc.param); got != tc.want {
				t.Errorf("pf_recall(%s=%#v) put %q on the wire, want %q", tc.param, tc.shape, got, tc.want)
			}
		})
	}
}

// TestWireQueryReadyQueueDisclosureReachesTheModel is aihub#432's LAST hop, and
// it is the one a server-side change is most likely to lose.
//
// internal/domain/request_adjusted.go's opening argument is that this process
// eats response fields by default: pf_recall's projection is an opt-in
// whitelist, and it has already swallowed three fields that existed server-side
// — `total` (aihub#249), the truncation pair (aihub#269) and `unmatched_types`
// (aihub#289). A `request_adjusted` that is correct in domain and never reaches
// the model would reproduce exactly the silence it was added to remove, and
// every test in internal/domain would stay green while it did.
//
// pf_get_ready_queue applies no projection today (jsonResult of the client's
// map[string]any). This test is what makes that a checked property rather than
// a fact of the current implementation.
func TestWireQueryReadyQueueDisclosureReachesTheModel(t *testing.T) {
	t.Run("a disclosure the server sent arrives intact", func(t *testing.T) {
		q := newQueryRecorder(t)
		q.respondWith(map[string]any{
			"items": []any{}, "running": []any{}, "stalled": []any{},
			"paused": []any{}, "needs_human_session": []any{}, "unclassified": []any{},
			"request_adjusted": []any{
				map[string]any{"param": "max", "requested": 5000, "applied": 200},
			},
		})
		res := callToolAgainstRecorderResult(t, q, "pf_get_ready_queue", map[string]any{
			"project": "aihub", "max": float64(5000),
		})
		if res.IsError {
			t.Fatalf("pf_get_ready_queue failed: %s", toolResultText(t, res))
		}
		var payload map[string]any
		if err := json.Unmarshal([]byte(toolResultText(t, res)), &payload); err != nil {
			t.Fatalf("tool result is not JSON: %v", err)
		}
		entries, present := payload["request_adjusted"]
		if !present {
			t.Fatalf("the server disclosed a clamped `max` and the model was not shown it: %v. "+
				"A projection that drops this field turns aihub#432's disclosure back into the "+
				"silence it replaced, with nothing in internal/domain able to notice.", payload)
		}
		list, ok := entries.([]any)
		if !ok || len(list) != 1 {
			t.Fatalf("request_adjusted arrived as %#v, want a one-entry list", entries)
		}
		entry, ok := list[0].(map[string]any)
		if !ok || entry["param"] != "max" {
			t.Errorf("request_adjusted entry = %#v, want one naming max", list[0])
		}
	})

	t.Run("no disclosure stays no key", func(t *testing.T) {
		// The other half of the convention: absence must survive the hop as
		// absence. A projection that helpfully filled in an empty list would
		// give the key a meaning it must not have (request_adjusted.go: an
		// absent field asserts NOTHING, and that is what makes omitting it safe
		// on a server that predates the field).
		q := newQueryRecorder(t)
		q.respondWith(map[string]any{
			"items": []any{}, "running": []any{}, "stalled": []any{},
			"paused": []any{}, "needs_human_session": []any{}, "unclassified": []any{},
		})
		res := callToolAgainstRecorderResult(t, q, "pf_get_ready_queue", map[string]any{
			"project": "aihub", "max": float64(25),
		})
		if res.IsError {
			t.Fatalf("pf_get_ready_queue failed: %s", toolResultText(t, res))
		}
		var payload map[string]any
		if err := json.Unmarshal([]byte(toolResultText(t, res)), &payload); err != nil {
			t.Fatalf("tool result is not JSON: %v", err)
		}
		if raw, present := payload["request_adjusted"]; present {
			t.Errorf("an unadjusted ready queue reached the model carrying request_adjusted=%#v; "+
				"nothing may invent the key, because its absence is what says \"nothing to report\"", raw)
		}
	})
}
