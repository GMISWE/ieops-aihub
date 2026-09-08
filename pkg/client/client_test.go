package client

import (
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
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

// TestFormatDetails_Truncation verifies the ~500B cap so a pathological details
// blob cannot flood the error string.
func TestFormatDetails_Truncation(t *testing.T) {
	big := make([]string, 200)
	for i := range big {
		big[i] = `"k` + strings.Repeat("x", 5) + `":1`
	}
	raw := "{" + strings.Join(big, ",") + "}"
	got := formatDetails(json.RawMessage(raw))
	if !strings.HasSuffix(got, "...(truncated)") {
		t.Fatalf("oversized details must be truncated, got %q", got)
	}
	// " details=" (9) + 500 + "...(truncated)" (14) = 523.
	if len(got) != 9+500+len("...(truncated)") {
		t.Errorf("truncated length = %d, want %d", len(got), 9+500+len("...(truncated)"))
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
