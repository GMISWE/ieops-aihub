package client

import (
	"bufio"
	"context"
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestFormatDetails covers the aihub#209 fix: the server already computes a
// `details` object on conflict errors (lock holder, dedup candidates,
// superseded_by, …) but the client used to decode only {code,message} and drop
// it. formatDetails renders that object into the error string so the metadata
// reaches the caller. Cases map to the wi's acceptance criteria.
func TestFormatDetails(t *testing.T) {
	cases := []struct {
		name      string
		raw       string
		wantEmpty bool
		wantSubs  []string // substrings that must appear in the suffix
	}{
		{name: "nil", raw: "", wantEmpty: true},
		{name: "literal null", raw: "null", wantEmpty: true},
		{
			// AC: lock-conflict error string contains actor_display + slug.
			name: "lock conflict conflict_with",
			raw:  `{"conflict_with":{"attempt_id":"ra_abc","actor_display":"monte","work_item_slug":"aihub#207"}}`,
			wantSubs: []string{
				" details=", "conflict_with", "actor_display", "monte", "aihub#207",
			},
		},
		{
			// AC: dedup conflict carries the candidate list.
			name:     "dedup candidates",
			raw:      `{"candidates":[{"slug":"aihub#100","goal":"x"},{"slug":"aihub#101","goal":"y"}]}`,
			wantSubs: []string{"candidates", "aihub#100", "aihub#101"},
		},
		{
			name:     "superseded_by",
			raw:      `{"superseded_by":{"actor_display":"xqr","at":"2026-07-06T14:00:00Z"}}`,
			wantSubs: []string{"superseded_by", "xqr"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := formatDetails(json.RawMessage(tc.raw))
			if tc.wantEmpty {
				if got != "" {
					t.Fatalf("want empty suffix, got %q", got)
				}
				return
			}
			if !strings.HasPrefix(got, " details=") {
				t.Fatalf("suffix must start with %q, got %q", " details=", got)
			}
			for _, sub := range tc.wantSubs {
				if !strings.Contains(got, sub) {
					t.Errorf("suffix %q missing %q", got, sub)
				}
			}
			// Compact: no newline or tab from indentation should survive.
			if strings.ContainsAny(got, "\n\t") {
				t.Errorf("suffix not compacted: %q", got)
			}
		})
	}
}

// TestFormatDetails_Truncation verifies the DetailsRenderLimit cap so a
// pathological details blob cannot flood the error string. The blob is sized
// relative to the cap rather than absolutely, so the test keeps measuring the
// cut wherever the cap moves (it was 500 until aihub#375).
func TestFormatDetails_Truncation(t *testing.T) {
	entry := `"k` + strings.Repeat("x", 5) + `":1,`
	n := 2*DetailsRenderLimit/len(entry) + 1
	big := make([]string, n)
	for i := range big {
		big[i] = `"k` + strings.Repeat("x", 5) + `":1`
	}
	raw := "{" + strings.Join(big, ",") + "}"
	got := formatDetails(json.RawMessage(raw))
	if !strings.HasSuffix(got, "...(truncated)") {
		t.Fatalf("oversized details must be truncated, got %q", got)
	}
	// " details=" (9) + DetailsRenderLimit + "...(truncated)" (14).
	if len(got) != len(" details=")+DetailsRenderLimit+len("...(truncated)") {
		t.Errorf("truncated length = %d, want %d", len(got),
			len(" details=")+DetailsRenderLimit+len("...(truncated)"))
	}
}

// ─── aihub#436: Idempotency-Key on every mutating request ──────────────────
//
// The design requires `Idempotency-Key: idem_...` on ALL POST/PATCH (§4.1, and
// H-R3-8 "header only; all POST/PATCH must carry it"). The server has enforced
// its half since aihub#152 — IdempotencyMiddleware is mounted on the whole /v1
// group — but no client sent the header, so a mandatory protocol element had a
// compliance rate of zero and the middleware was exercised only by its own unit
// tests. These tests are the client half.

// captureHeaders starts a test server that records the method and
// Idempotency-Key of every request it receives, and returns a Client aimed at
// it plus a function to read what was captured.
func captureHeaders(t *testing.T) (*Client, func() []capturedRequest) {
	t.Helper()
	var mu sync.Mutex
	var got []capturedRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		got = append(got, capturedRequest{
			Method:  r.Method,
			Path:    r.URL.Path,
			IdemKey: r.Header.Get("Idempotency-Key"),
		})
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"ok":true}`)) //nolint:errcheck
	}))
	t.Cleanup(srv.Close)
	return New(srv.URL, "pfk_test"), func() []capturedRequest {
		mu.Lock()
		defer mu.Unlock()
		out := make([]capturedRequest, len(got))
		copy(out, got)
		return out
	}
}

type capturedRequest struct {
	Method  string
	Path    string
	IdemKey string
}

// TestMutatingRequestsCarryIdempotencyKey is the acceptance criterion itself:
// every POST and PATCH leaves the client with an `idem_`-prefixed key, and no
// GET carries one.
//
// The GET half is not decoration. The middleware ignores the header on GET, so
// sending it there would be inert on the server — but net/http's
// (*Request).isReplayable treats the header as a promise that the request may be
// re-sent on a reused connection, and that promise should be made only where the
// design actually makes it.
func TestMutatingRequestsCarryIdempotencyKey(t *testing.T) {
	c, captured := captureHeaders(t)
	ctx := t.Context()

	// One call per verb shape the client can produce.
	if _, err := c.CreateWorkItem(ctx, map[string]any{"goal": "g"}); err != nil {
		t.Fatalf("CreateWorkItem: %v", err)
	}
	if _, err := c.UpdateWorkItem(ctx, "wi_1", map[string]any{"priority": "high"}); err != nil {
		t.Fatalf("UpdateWorkItem: %v", err)
	}
	if _, err := c.ClaimWorkItem(ctx, "aihub#1", map[string]any{"idempotency_key": "k"}); err != nil {
		t.Fatalf("ClaimWorkItem: %v", err)
	}
	if _, err := c.WhoAmI(ctx); err != nil {
		t.Fatalf("WhoAmI: %v", err)
	}
	if _, err := c.ListWorkItems(ctx, nil); err != nil {
		t.Fatalf("ListWorkItems: %v", err)
	}

	reqs := captured()
	if len(reqs) != 5 {
		t.Fatalf("captured %d requests, want 5 — the walk below would not be checking what it claims", len(reqs))
	}
	mutating := 0
	for _, r := range reqs {
		switch r.Method {
		case http.MethodPost, http.MethodPatch:
			mutating++
			if r.IdemKey == "" {
				t.Errorf("%s %s carried no Idempotency-Key; the design requires it on all POST/PATCH "+
					"(§4.1, H-R3-8) and the server's IdempotencyMiddleware is mounted on all of /v1",
					r.Method, r.Path)
				continue
			}
			if !strings.HasPrefix(r.IdemKey, "idem_") {
				t.Errorf("%s %s key %q lacks the documented %q prefix", r.Method, r.Path, r.IdemKey, "idem_")
			}
			if len(r.IdemKey) < len("idem_")+16 {
				t.Errorf("%s %s key %q is too short to be unguessable", r.Method, r.Path, r.IdemKey)
			}
		default:
			if r.IdemKey != "" {
				t.Errorf("%s %s carried Idempotency-Key %q; the header is for POST/PATCH only",
					r.Method, r.Path, r.IdemKey)
			}
		}
	}
	if mutating != 3 {
		t.Fatalf("saw %d mutating requests, want 3 — this test is not exercising what it says it is", mutating)
	}
}

// TestIdempotencyKeyIsFreshPerRequest pins the property the whole scheme rests
// on. The server caches a response for 24h under <api_key_id>:<key> and replays
// it for any later request presenting the same key with the same fingerprint. A
// key that were constant, or per-Client, would therefore make the SECOND
// genuinely-new create return the FIRST one's response — the header would turn
// from a safety net into a correctness bug.
func TestIdempotencyKeyIsFreshPerRequest(t *testing.T) {
	c, captured := captureHeaders(t)
	body := map[string]any{"goal": "same goal, two separate intents"}
	for i := range 2 {
		if _, err := c.CreateWorkItem(t.Context(), body); err != nil {
			t.Fatalf("CreateWorkItem #%d: %v", i, err)
		}
	}
	reqs := captured()
	if len(reqs) != 2 {
		t.Fatalf("captured %d requests, want 2", len(reqs))
	}
	if reqs[0].IdemKey == "" || reqs[1].IdemKey == "" {
		t.Fatalf("both requests must carry a key, got %q and %q", reqs[0].IdemKey, reqs[1].IdemKey)
	}
	if reqs[0].IdemKey == reqs[1].IdemKey {
		t.Fatalf("two separate requests reused key %q — the server would replay the first "+
			"response for the second request", reqs[0].IdemKey)
	}
}

// TestDoRawMutatingRequestCarriesIdempotencyKey covers the second request
// builder. doRaw's only call site today is a GET, so a behavioural test through
// the exported API cannot reach its POST path — but the contract is a property
// of the METHOD, not of which call sites happen to exist, and a POST added
// through doRaw later must be compliant by construction rather than by somebody
// remembering. Calling it directly is the point.
func TestDoRawMutatingRequestCarriesIdempotencyKey(t *testing.T) {
	c, captured := captureHeaders(t)
	if _, _, err := c.doRaw(t.Context(), http.MethodPost, "/v1/anything"); err != nil {
		t.Fatalf("doRaw POST: %v", err)
	}
	if _, _, err := c.doRaw(t.Context(), http.MethodGet, "/v1/anything"); err != nil {
		t.Fatalf("doRaw GET: %v", err)
	}
	reqs := captured()
	if len(reqs) != 2 {
		t.Fatalf("captured %d requests, want 2", len(reqs))
	}
	if !strings.HasPrefix(reqs[0].IdemKey, "idem_") {
		t.Errorf("doRaw POST carried key %q, want an idem_ prefixed one", reqs[0].IdemKey)
	}
	if reqs[1].IdemKey != "" {
		t.Errorf("doRaw GET carried Idempotency-Key %q; POST/PATCH only", reqs[1].IdemKey)
	}
}

// TestEveryRequestBuilderSetsStandardHeaders is the standing gate, and it is
// what makes the three tests above more than a sample.
//
// The behavioural tests can only ever cover the request builders somebody
// remembered to exercise — the same blind spot aihub#324's contract test exists
// to close one file over. This one reads the SHAPE instead: any function in this
// package that constructs an outbound request must hand it to the one helper
// that applies the standard headers. A third builder written later without that
// call goes red the moment it is written, whether or not it has a test.
func TestEveryRequestBuilderSetsStandardHeaders(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read ./pkg/client: %v", err)
	}
	fset := token.NewFileSet()
	builders := 0
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, perr := parser.ParseFile(fset, name, nil, 0)
		if perr != nil {
			t.Fatalf("parse %s: %v", name, perr)
		}
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			if !callsFunc(fn.Body, "NewRequestWithContext") && !callsFunc(fn.Body, "NewRequest") {
				continue
			}
			builders++
			if !callsFunc(fn.Body, "setStandardHeaders") {
				t.Errorf("%s:%d: %s builds an outbound request but never calls setStandardHeaders, "+
					"so it sends no Idempotency-Key on POST/PATCH. The design (§4.1, H-R3-8) requires "+
					"the header on every mutating request; route the request through setStandardHeaders "+
					"rather than setting Authorization by hand.",
					name, fset.Position(fn.Pos()).Line, fn.Name.Name)
			}
		}
	}
	// A structural test that matched nothing would pass forever. This package
	// has always had at least do() and doRaw(); zero means the walk broke.
	if builders < 2 {
		t.Fatalf("found %d request builder(s) in ./pkg/client, want at least 2 (do and doRaw) — "+
			"the AST walk is broken and this test is reporting success without having looked", builders)
	}
	t.Logf("checked %d request builder(s)", builders)
}

// callsFunc reports whether body contains a call whose selector or identifier is
// name — `http.NewRequestWithContext(...)`, `c.setStandardHeaders(...)` and a
// bare `setStandardHeaders(...)` all count.
func callsFunc(body *ast.BlockStmt, name string) bool {
	found := false
	ast.Inspect(body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		switch fun := call.Fun.(type) {
		case *ast.SelectorExpr:
			if fun.Sel.Name == name {
				found = true
			}
		case *ast.Ident:
			if fun.Name == name {
				found = true
			}
		}
		return !found
	})
	return found
}

// ─── aihub#472 — a written mutating request must not be re-sent ──────────────

// rstServer is a raw TCP HTTP/1.1 origin built for one job: read a request in
// full and then kill the connection WITHOUT answering it. That is the exact
// condition net/http's (*persistConn).shouldRetryRequest answers with a silent
// re-send, and httptest.Server cannot produce it — it speaks through net/http's
// own server, which has no way to consume a request and then reset.
//
// It counts arrivals per path and accepted connections, so a test can tell
// "the server ran this mutation once" from "the server ran it twice", which is
// the only observation that separates the bug from the fix. The client's own
// error is the second half: under the bug it is nil.
type rstServer struct {
	ln net.Listener

	mu      sync.Mutex
	hits    map[string]int // path -> how many times a request for it arrived
	resetOn map[string]int // path -> which arrival to answer with a TCP RST
	conns   int            // accepted connections == how many times the client dialled
}

// newRSTServer starts a listener on loopback. resetOn maps a path to the
// 1-based arrival number that gets reset; every other arrival is answered 200
// with a `{}` body and the connection is kept open for reuse.
func newRSTServer(t *testing.T, resetOn map[string]int) *rstServer {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	s := &rstServer{ln: ln, hits: map[string]int{}, resetOn: resetOn}
	go s.accept()
	t.Cleanup(func() { _ = ln.Close() })
	return s
}

func (s *rstServer) accept() {
	for {
		conn, err := s.ln.Accept()
		if err != nil {
			return
		}
		s.mu.Lock()
		s.conns++
		s.mu.Unlock()
		go s.serveConn(conn)
	}
}

func (s *rstServer) serveConn(conn net.Conn) {
	defer conn.Close() //nolint:errcheck
	br := bufio.NewReader(conn)
	for {
		req, err := http.ReadRequest(br)
		if err != nil {
			return
		}
		// Drain the body before deciding anything: the whole point of this
		// harness is a request the client has finished WRITING. A reset issued
		// mid-write would be nothingWrittenError, a different clause with a
		// different (and legitimate) answer.
		_, _ = io.Copy(io.Discard, req.Body)
		_ = req.Body.Close()

		s.mu.Lock()
		s.hits[req.URL.Path]++
		reset := s.resetOn[req.URL.Path] == s.hits[req.URL.Path]
		s.mu.Unlock()

		if reset {
			// SO_LINGER 0 turns the deferred Close into an RST, so the client
			// sees a non-EOF read failure on the first response byte —
			// transportReadFromServerError — rather than a clean EOF.
			if tcp, ok := conn.(*net.TCPConn); ok {
				_ = tcp.SetLinger(0)
			}
			return
		}
		if _, werr := io.WriteString(conn,
			"HTTP/1.1 200 OK\r\nContent-Type: application/json\r\nContent-Length: 2\r\n\r\n{}"); werr != nil {
			return
		}
	}
}

func (s *rstServer) baseURL() string { return "http://" + s.ln.Addr().String() }

func (s *rstServer) count(path string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.hits[path]
}

func (s *rstServer) connCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.conns
}

// warmPool sends two GETs and requires that they shared ONE connection.
//
// This is the test's green control, and it is not optional. Every assertion
// below is about what happens to a request that rides a REUSED connection:
// net/http only ever retries on one (shouldRetryRequest returns false for
// !pc.isReused()). If pooling were not happening here — a future global
// DisableKeepAlives, a proxy env var, a transport swap — every test in this
// group would pass while observing nothing at all. Failing here says "the
// premise is gone", which is a different report from "the fix regressed".
func warmPool(t *testing.T, c *Client, s *rstServer) {
	t.Helper()
	// Returning a connection to the idle pool is a hand-off to the transport's
	// readLoop goroutine that happens after ReadAll sees EOF, so a GET issued
	// immediately after another can lose the race and dial again. Rather than
	// sleep a guessed interval, send GETs until one of them opens NO new
	// connection: that observation IS the proof that pooling happened, and it
	// converges in one round on an unloaded machine without going flaky on a
	// loaded one.
	for attempt := range 100 {
		before := s.connCount()
		if _, _, err := c.doRaw(t.Context(), http.MethodGet, "/v1/warm"); err != nil {
			t.Fatalf("warm-up GET #%d: %v", attempt+1, err)
		}
		if attempt > 0 && s.connCount() == before {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("after 100 GETs the server had opened %d connections and no request ever reused "+
		"one — this transport is not pooling, so nothing below is exercising the "+
		"reused-connection retry it claims to", s.connCount())
}

// warmMutating sends one mutating request that IS answered, so that whatever
// carries mutations has had a chance to put a connection in an idle pool before
// the measured request goes out.
//
// Without this the tests below would only ever observe a process's FIRST
// mutation, which is dialled fresh and can never be retried no matter what the
// code does — a fix that merely moved mutations to a second POOLING transport
// would look correct because that transport's pool happened to be empty, and
// every mutation after the first would still be re-sendable in production. This
// was measured: with it removed, flipping DisableKeepAlives back to false left
// the whole group green.
func warmMutating(t *testing.T, c *Client, s *rstServer) {
	t.Helper()
	if _, _, err := c.doRaw(t.Context(), http.MethodPost, "/v1/warm_post"); err != nil {
		t.Fatalf("warm-up POST: %v", err)
	}
	// No convergence loop here, and it cannot be one: under the fix this client
	// never reuses, so "wait until a request opens no new connection" would only
	// ever time out. A fixed settle is the honest instrument — its only job is
	// to give a REGRESSION (a mutating transport that pools again) time to put
	// its connection back before the measured request goes looking for it.
	time.Sleep(200 * time.Millisecond)
	t.Logf("after warming both paths the server has accepted %d connection(s)", s.connCount())
}

// TestMutatingRequestIsNotRetriedByTransport is aihub#472's acceptance probe.
//
// aihub#436 put an Idempotency-Key on every POST/PATCH, which is what the
// design requires (§4.1, H-R3-8). Its side effect was that (*Request).
// isReplayable started admitting those methods, so the transport began
// re-sending a mutation it had already written in full whenever the reused
// connection died before the first response byte — and the caller saw err=nil
// for two executions.
//
// Both arms below are here on purpose and they fail for different reasons:
//
//   - "body" is the common case, a POST carrying JSON.
//   - "no body" is the case that refutes the obvious fix. Clearing req.GetBody
//     does not make this one non-replayable, because http.NewRequestWithContext
//     turns a zero-length *bytes.Reader into http.NoBody and isReplayable's
//     first disjunct is then satisfied by `r.Body == NoBody` with GetBody out
//     of the picture entirely. A fix that only clears GetBody leaves every
//     no-body mutation — ActivateMemory, RotateProjectIdentifier, any doRaw
//     POST — exactly as double-executable as before, and this arm is what says
//     so out loud.
func TestMutatingRequestIsNotRetriedByTransport(t *testing.T) {
	cases := []struct {
		name string
		path string
		call func(context.Context, *Client) error
	}{
		{
			name: "body",
			path: "/v1/work_items",
			call: func(ctx context.Context, c *Client) error {
				_, err := c.CreateWorkItem(ctx, map[string]any{"goal": "g"})
				return err
			},
		},
		{
			name: "no body",
			path: "/v1/memories/mem_x/activate",
			call: func(ctx context.Context, c *Client) error {
				_, err := c.ActivateMemory(ctx, "mem_x")
				return err
			},
		},
		{
			// DELETE is not in isMutatingMethod, so it keeps the pooled
			// transport — and that is safe only because it carries no
			// Idempotency-Key: isReplayable admits a non-GET method on the
			// header alone. This arm holds those two facts together. Give
			// DELETE the header without moving it to the mutating client and it
			// goes red, which is the mistake worth catching.
			name: "delete, no header, pooled transport",
			path: "/v1/admin/users/u_x/keys/k_x",
			call: func(ctx context.Context, c *Client) error {
				_, err := c.RevokeAPIKey(ctx, "u_x", "k_x")
				return err
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// Reset only the FIRST arrival. A retry therefore SUCCEEDS, which
			// reproduces the reported symptom exactly: the caller is told
			// nothing went wrong while the server ran the mutation twice.
			s := newRSTServer(t, map[string]int{tc.path: 1})
			c := New(s.baseURL(), "test-key")
			warmPool(t, c, s)
			warmMutating(t, c, s)

			err := tc.call(t.Context(), c)

			if got := s.count(tc.path); got != 1 {
				t.Errorf("server executed %s %d times, want 1 — the transport re-sent a "+
					"mutation it had already written in full (aihub#472)", tc.path, got)
			}
			if err == nil {
				t.Errorf("client got err=nil for a connection that was reset before it " +
					"answered; a mutation whose outcome is unknown must surface as an error, " +
					"not as success")
			}
			t.Logf("%s: server arrivals=%d, connections=%d, client err=%v",
				tc.path, s.count(tc.path), s.connCount(), err)
		})
	}
}

// TestSafeMethodIsStillRetriedByTransport is the other half of the fix, and the
// reason it is not simply "turn the retry off".
//
// GET is replayable by METHOD — isReplayable admits it with or without an
// Idempotency-Key, and re-sending it is what makes an idle connection the
// server has quietly closed invisible to callers. aihub#472 must not pay for
// mutating safety with that. If a future change disables connection reuse or
// replayability wholesale rather than for mutations only, this goes red.
func TestSafeMethodIsStillRetriedByTransport(t *testing.T) {
	const path = "/v1/artifacts/mem_x/html"
	s := newRSTServer(t, map[string]int{path: 1})
	c := New(s.baseURL(), "test-key")
	warmPool(t, c, s)

	_, err := c.GetArtifactHTML(t.Context(), "mem_x")
	if err != nil {
		t.Errorf("GET surfaced %v; a safe method whose connection was reset must still be "+
			"retried transparently", err)
	}
	if got := s.count(path); got != 2 {
		t.Errorf("server saw %d GETs, want 2 (the original plus the transport's retry) — "+
			"GET replayability was collateral damage of the aihub#472 fix", got)
	}
	t.Logf("GET %s: server arrivals=%d, connections=%d, client err=%v",
		path, s.count(path), s.connCount(), err)
}

// TestEveryRequestSenderPicksItsClientByMethod is the standing gate for
// aihub#472, and it is the half the two behavioural tests above cannot cover.
//
// They can only observe the request builders that exist today. The defect class
// is a builder written LATER that reaches for c.httpClient directly: it would
// send a correct Idempotency-Key, pass every header test in this file, and put
// the mutation straight back on a pooled connection where the transport may
// re-send it. Nothing behavioural would notice, exactly as nothing noticed when
// aihub#436 opened the window in the first place.
//
// So this reads the SHAPE: a function that hands a request to an *http.Client
// must have asked clientFor which client to use.
func TestEveryRequestSenderPicksItsClientByMethod(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read ./pkg/client: %v", err)
	}
	fset := token.NewFileSet()
	senders := 0
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, perr := parser.ParseFile(fset, name, nil, 0)
		if perr != nil {
			t.Fatalf("parse %s: %v", name, perr)
		}
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			if !callsFunc(fn.Body, "Do") {
				continue
			}
			senders++
			if !callsFunc(fn.Body, "clientFor") {
				t.Errorf("%s:%d: %s sends a request without asking clientFor which client to "+
					"use, so a POST/PATCH can go out on the pooled transport. net/http re-sends "+
					"a fully written request on a REUSED connection when it dies before the first "+
					"response byte, and the Idempotency-Key this package sets makes a mutation "+
					"eligible for that (aihub#472) — the server executes it twice and the caller "+
					"is told err=nil. Route the send through c.clientFor(method).Do(req).",
					name, fset.Position(fn.Pos()).Line, fn.Name.Name)
			}
		}
	}
	// A structural test that matched nothing would pass forever. This package
	// has always had at least do() and doRaw(); zero means the AST walk broke.
	if senders < 2 {
		t.Fatalf("found %d request sender(s) in ./pkg/client, want at least 2 (do and doRaw) — "+
			"the AST walk is broken and this test is reporting success without having looked", senders)
	}
	t.Logf("checked %d request sender(s)", senders)
}

// TestNonPoolingFallbackStillDisablesKeepAlives covers the branch taken when a
// program has replaced http.DefaultTransport with something that is not an
// *http.Transport, so there is nothing to clone.
//
// It is the branch nobody will ever see in this repo and the one where getting
// it wrong is silent: returning the shared default there would hand mutations
// back to a pooling transport, and every behavioural test in this file would
// still pass because they all run with the ordinary DefaultTransport in place.
func TestNonPoolingFallbackStillDisablesKeepAlives(t *testing.T) {
	notATransport := roundTripperFunc(func(*http.Request) (*http.Response, error) { return nil, nil })

	got, ok := nonPoolingFrom(notATransport).(*http.Transport)
	if !ok {
		t.Fatalf("fallback returned %T, want an *http.Transport built from scratch", got)
	}
	if !got.DisableKeepAlives {
		t.Error("the fallback transport pools connections, so a mutation sent through it can " +
			"ride a reused connection and be re-sent by net/http (aihub#472)")
	}
	if got == http.DefaultTransport {
		t.Error("the fallback returned the shared default transport itself")
	}

	// And the ordinary path, for the same property.
	cloned, ok := nonPoolingFrom(http.DefaultTransport).(*http.Transport)
	if !ok {
		t.Fatalf("clone path returned %T, want *http.Transport", cloned)
	}
	if !cloned.DisableKeepAlives {
		t.Error("the cloned transport pools connections")
	}
	if cloned == http.DefaultTransport {
		t.Error("the clone path handed back http.DefaultTransport itself — setting " +
			"DisableKeepAlives on it would disable pooling process-wide")
	}
}

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
