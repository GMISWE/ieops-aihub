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
	"net/http"
	"net/http/httptest"
	"net/url"
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
}

func newQueryRecorder(t *testing.T) *queryRecorder {
	t.Helper()
	q := &queryRecorder{}
	q.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q.mu.Lock()
		q.queries = append(q.queries, r.URL.Query())
		q.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"items": []any{}, "total": 0, "ready": []any{}})
	}))
	t.Cleanup(q.server.Close)
	return q
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
// session backed by the real aihub client.
func callToolAgainstRecorder(t *testing.T, q *queryRecorder, tool string, args map[string]any) {
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
	if res.IsError {
		if text, ok := res.Content[0].(*sdkmcp.TextContent); ok {
			t.Fatalf("call %s failed: %s", tool, text.Text)
		}
		t.Fatalf("call %s failed", tool)
	}
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
