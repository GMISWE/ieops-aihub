package server

// aihub#152 — the idempotency cache had two defects and no tests at all:
//
//	(1) the key was "<api_key_id>:<idempotency_key>" and nothing else, so one key
//	    reused across two different requests replayed the FIRST response for the
//	    second, stamped X-Idempotency-Replayed: true; and
//	(2) PurgeExpiredIdempotencyCache was never called from anywhere, and even
//	    calling it would not have bounded the cache — a 24h TTL with no size cap
//	    grows for a day under a flood of unique keys.
//
// These run the real middleware over a real echo handler chain; nothing here is
// a re-derivation of the logic under test.

import (
	"context"
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/labstack/echo/v4"
)

// resetIdempotencyCache empties the process-global cache so tests do not inherit
// each other's entries. Registered with t.Cleanup by newIdempotencyHarness.
func resetIdempotencyCache() {
	idempotencyMu.Lock()
	defer idempotencyMu.Unlock()
	idempotencyCache = map[string]*cachedResponse{}
}

// idemHarness wires BearerAuth's output (a *UserContext) plus the real
// IdempotencyMiddleware in front of a counting handler.
type idemHarness struct {
	e     *echo.Echo
	calls int
}

func newIdempotencyHarness(t *testing.T, apiKeyID string) *idemHarness {
	t.Helper()
	resetIdempotencyCache()
	t.Cleanup(resetIdempotencyCache)

	h := &idemHarness{e: echo.New()}
	h.e.Use(func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			c.Set(string(ctxUser), &UserContext{UserID: "u_test", Role: "writer", APIKeyID: apiKeyID})
			return next(c)
		}
	})
	h.e.Use(IdempotencyMiddleware())

	handler := func(c echo.Context) error {
		h.calls++
		body, _ := json.Marshal(map[string]any{"call": h.calls, "path": c.Request().URL.Path})
		return c.JSONBlob(http.StatusOK, body)
	}
	h.e.POST("/v1/work_items", handler)
	h.e.POST("/v1/memories", handler)
	h.e.PATCH("/v1/work_items/:id", handler)
	h.e.GET("/v1/work_items", handler)
	return h
}

func (h *idemHarness) do(method, target, body, idemKey string) *httptest.ResponseRecorder {
	var req *http.Request
	if body == "" {
		req = httptest.NewRequest(method, target, nil)
	} else {
		req = httptest.NewRequest(method, target, strings.NewReader(body))
		req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	}
	if idemKey != "" {
		req.Header.Set("Idempotency-Key", idemKey)
	}
	rec := httptest.NewRecorder()
	h.e.ServeHTTP(rec, req)
	return rec
}

// TestIdempotency_SameRequestReplays is the behaviour the feature exists for, and
// the control for every test below: without it, "does not replay" would be
// satisfiable by a middleware that had simply stopped working.
func TestIdempotency_SameRequestReplays(t *testing.T) {
	h := newIdempotencyHarness(t, "k_1")

	first := h.do(http.MethodPost, "/v1/work_items", `{"goal":"a"}`, "key-1")
	second := h.do(http.MethodPost, "/v1/work_items", `{"goal":"a"}`, "key-1")

	if h.calls != 1 {
		t.Fatalf("handler ran %d times, want 1 — the second identical request must be replayed", h.calls)
	}
	if second.Header().Get("X-Idempotency-Replayed") != "true" {
		t.Fatalf("replay header missing: %v", second.Header())
	}
	if first.Body.String() != second.Body.String() {
		t.Fatalf("replayed body differs: %q vs %q", first.Body.String(), second.Body.String())
	}
}

// TestIdempotency_ReusedKeyDifferentRequestIsRejected is defect (1). Each case
// varies exactly ONE component of the fingerprint, so a fix that happened to
// cover only the body (the easiest one to think of) still fails the others.
func TestIdempotency_ReusedKeyDifferentRequestIsRejected(t *testing.T) {
	cases := []struct {
		name                 string
		method, target, body string
	}{
		{name: "different path", method: http.MethodPost, target: "/v1/memories", body: `{"goal":"a"}`},
		{name: "different body", method: http.MethodPost, target: "/v1/work_items", body: `{"goal":"b"}`},
		{name: "different query", method: http.MethodPost, target: "/v1/work_items?dry_run=1", body: `{"goal":"a"}`},
		{name: "different method", method: http.MethodPatch, target: "/v1/work_items", body: `{"goal":"a"}`},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newIdempotencyHarness(t, "k_1")
			first := h.do(http.MethodPost, "/v1/work_items", `{"goal":"a"}`, "key-1")
			if first.Code != http.StatusOK {
				t.Fatalf("seed request: got %d, want 200 (%s)", first.Code, first.Body.String())
			}

			rec := h.do(tc.method, tc.target, tc.body, "key-1")
			if rec.Code != http.StatusConflict {
				t.Fatalf("status: got %d, want 409 — the cached response for a DIFFERENT request must not be served (body=%s)",
					rec.Code, rec.Body.String())
			}
			if rec.Header().Get("X-Idempotency-Replayed") == "true" {
				t.Fatalf("response was marked as a replay: %v", rec.Header())
			}
			if !strings.Contains(rec.Body.String(), "IDEMPOTENCY_KEY_REUSED") {
				t.Fatalf("error code missing from body: %s", rec.Body.String())
			}
			if strings.Contains(rec.Body.String(), `"call":1`) {
				t.Fatalf("the first request's cached response leaked into the answer: %s", rec.Body.String())
			}
		})
	}
}

// TestIdempotency_KeysAreScopedPerAPIKey pins the part of the old key that was
// right, so the fingerprint work above cannot quietly drop it.
func TestIdempotency_KeysAreScopedPerAPIKey(t *testing.T) {
	h1 := newIdempotencyHarness(t, "k_1")
	h1.do(http.MethodPost, "/v1/work_items", `{"goal":"a"}`, "shared-key")

	h2 := newIdempotencyHarness(t, "k_2")
	// newIdempotencyHarness resets the cache, so re-seed h1's entry alongside.
	h1.do(http.MethodPost, "/v1/work_items", `{"goal":"a"}`, "shared-key")
	rec := h2.do(http.MethodPost, "/v1/work_items", `{"goal":"a"}`, "shared-key")

	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200 — another API key's entry must not be visible (body=%s)", rec.Code, rec.Body.String())
	}
	if h2.calls != 1 {
		t.Fatalf("handler ran %d times for the second key, want 1", h2.calls)
	}
}

// TestIdempotency_OnlyPostAndPatch keeps the middleware off the read path: a GET
// carrying the header must never be answered from the cache.
func TestIdempotency_OnlyPostAndPatch(t *testing.T) {
	h := newIdempotencyHarness(t, "k_1")
	h.do(http.MethodGet, "/v1/work_items", "", "key-1")
	h.do(http.MethodGet, "/v1/work_items", "", "key-1")
	if h.calls != 2 {
		t.Fatalf("handler ran %d times, want 2 — GET must not be cached", h.calls)
	}
	if n := IdempotencyCacheLen(); n != 0 {
		t.Fatalf("cache holds %d entries after two GETs, want 0", n)
	}
}

// TestIdempotency_HandlerStillSeesTheBody is the hazard the fingerprint work
// introduces: fingerprinting means reading the request body, and a body read and
// not restored is an empty body for every handler downstream. This asserts the
// handler binds the same JSON it was sent.
func TestIdempotency_HandlerStillSeesTheBody(t *testing.T) {
	resetIdempotencyCache()
	t.Cleanup(resetIdempotencyCache)

	e := echo.New()
	e.Use(func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			c.Set(string(ctxUser), &UserContext{UserID: "u_test", Role: "writer", APIKeyID: "k_1"})
			return next(c)
		}
	})
	e.Use(IdempotencyMiddleware())

	var seen map[string]any
	e.POST("/v1/work_items", func(c echo.Context) error {
		if err := c.Bind(&seen); err != nil {
			return err
		}
		return c.JSON(http.StatusOK, map[string]string{"ok": "1"})
	})

	req := httptest.NewRequest(http.MethodPost, "/v1/work_items", strings.NewReader(`{"goal":"round trip"}`))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	req.Header.Set("Idempotency-Key", "key-1")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}
	if seen["goal"] != "round trip" {
		t.Fatalf("handler saw %#v; the middleware consumed the request body without restoring it", seen)
	}
}

// TestIdempotency_OversizedRequestBypassesTheCache covers the other half of that
// hazard: a body too large to fingerprint must still reach the handler intact,
// and must not be cached under a fingerprint we could not compute.
func TestIdempotency_OversizedRequestBypassesTheCache(t *testing.T) {
	resetIdempotencyCache()
	t.Cleanup(resetIdempotencyCache)

	big := `{"goal":"` + strings.Repeat("x", maxIdempotencyRequestBytes+64) + `"}`

	e := echo.New()
	e.Use(func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			c.Set(string(ctxUser), &UserContext{UserID: "u_test", Role: "writer", APIKeyID: "k_1"})
			return next(c)
		}
	})
	e.Use(IdempotencyMiddleware())

	gotLen := 0
	calls := 0
	e.POST("/v1/work_items", func(c echo.Context) error {
		calls++
		var m map[string]any
		if err := c.Bind(&m); err != nil {
			return err
		}
		gotLen = len(m["goal"].(string))
		return c.JSON(http.StatusOK, map[string]string{"ok": "1"})
	})

	for range 2 {
		req := httptest.NewRequest(http.MethodPost, "/v1/work_items", strings.NewReader(big))
		req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
		req.Header.Set("Idempotency-Key", "key-big")
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("status: got %d, want 200 (body=%s)", rec.Code, rec.Body.String())
		}
	}

	if want := maxIdempotencyRequestBytes + 64; gotLen != want {
		t.Fatalf("handler saw a goal of %d bytes, want %d — the oversized body was truncated", gotLen, want)
	}
	if calls != 2 {
		t.Fatalf("handler ran %d times, want 2 — an unfingerprintable request must not be replayed", calls)
	}
	if n := IdempotencyCacheLen(); n != 0 {
		t.Fatalf("cache holds %d entries, want 0", n)
	}
}

// TestIdempotency_OversizedResponseNotCached is the response-side byte cap. The
// entry cap alone does not bound memory if one entry may be arbitrarily large.
func TestIdempotency_OversizedResponseNotCached(t *testing.T) {
	resetIdempotencyCache()
	t.Cleanup(resetIdempotencyCache)

	payload := strings.Repeat("y", maxIdempotencyBodyBytes+1024)

	e := echo.New()
	e.Use(func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			c.Set(string(ctxUser), &UserContext{UserID: "u_test", Role: "writer", APIKeyID: "k_1"})
			return next(c)
		}
	})
	e.Use(IdempotencyMiddleware())
	e.POST("/v1/work_items", func(c echo.Context) error {
		return c.JSON(http.StatusOK, map[string]string{"blob": payload})
	})

	req := httptest.NewRequest(http.MethodPost, "/v1/work_items", strings.NewReader(`{}`))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	req.Header.Set("Idempotency-Key", "key-1")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), payload) {
		t.Fatalf("the oversized response was truncated on its way to the CLIENT; only caching may be skipped")
	}
	if n := IdempotencyCacheLen(); n != 0 {
		t.Fatalf("cache holds %d entries, want 0 — a response over the byte cap must not be stored", n)
	}
}

// TestIdempotency_EntryCapBoundsTheCache is defect (2), stated as the property
// that actually matters. Note what it does NOT test: a ticker. Purging expired
// entries reclaims nothing here, because none of these have expired — the wi's
// suggested fix ("schedule PurgeExpiredIdempotencyCache on a ticker") does not
// bound the cache at all, and only the size cap does.
func TestIdempotency_EntryCapBoundsTheCache(t *testing.T) {
	resetIdempotencyCache()
	t.Cleanup(resetIdempotencyCache)

	now := time.Now()
	for i := range maxIdempotencyEntries + 500 {
		storeIdempotent(
			"k_1:flood-"+string(rune('a'+i%26))+"-"+time.Duration(i).String(),
			&cachedResponse{StatusCode: 200, Body: []byte("x"), Fingerprint: "f", ExpiresAt: now.Add(idempotencyTTL)},
		)
	}

	if n := IdempotencyCacheLen(); n > maxIdempotencyEntries {
		t.Fatalf("cache holds %d entries, cap is %d — nothing bounds it", n, maxIdempotencyEntries)
	}
	if n := IdempotencyCacheLen(); n < maxIdempotencyEntries/2 {
		t.Fatalf("cache holds only %d entries; eviction is throwing away far more than it needs to", n)
	}
}

// TestPurgeExpiredIdempotencyCache_DropsOnlyExpired covers the function that
// aihub#152 found defined and never called.
func TestPurgeExpiredIdempotencyCache_DropsOnlyExpired(t *testing.T) {
	resetIdempotencyCache()
	t.Cleanup(resetIdempotencyCache)

	now := time.Now()
	storeIdempotent("k_1:stale", &cachedResponse{StatusCode: 200, Fingerprint: "f", ExpiresAt: now.Add(-time.Second)})
	storeIdempotent("k_1:fresh", &cachedResponse{StatusCode: 200, Fingerprint: "f", ExpiresAt: now.Add(time.Hour)})

	PurgeExpiredIdempotencyCache()

	if n := IdempotencyCacheLen(); n != 1 {
		t.Fatalf("cache holds %d entries after purge, want 1", n)
	}
	if _, ok := loadIdempotent("k_1:fresh"); !ok {
		t.Fatalf("purge dropped the unexpired entry")
	}
}

// TestStartIdempotencyCachePurger_RunsAndStops asserts the scheduler the wi asked
// for: that it actually fires, and that it stops with its context rather than
// leaking a ticker for the life of the process.
func TestStartIdempotencyCachePurger_RunsAndStops(t *testing.T) {
	resetIdempotencyCache()
	t.Cleanup(resetIdempotencyCache)

	storeIdempotent("k_1:stale", &cachedResponse{StatusCode: 200, Fingerprint: "f", ExpiresAt: time.Now().Add(-time.Second)})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	StartIdempotencyCachePurger(ctx, time.Millisecond)

	deadline := time.Now().Add(5 * time.Second)
	for IdempotencyCacheLen() != 0 {
		if time.Now().After(deadline) {
			t.Fatalf("the purger never ran: cache still holds %d expired entries", IdempotencyCacheLen())
		}
		time.Sleep(time.Millisecond)
	}

	// Stopping: after cancel, a newly inserted expired entry must survive.
	cancel()
	time.Sleep(20 * time.Millisecond)
	storeIdempotent("k_1:stale2", &cachedResponse{StatusCode: 200, Fingerprint: "f", ExpiresAt: time.Now().Add(-time.Second)})
	time.Sleep(50 * time.Millisecond)
	if n := IdempotencyCacheLen(); n != 1 {
		t.Fatalf("cache holds %d entries after cancel, want 1 — the purger goroutine outlived its context", n)
	}
}

// TestRequestFingerprint_ComponentsCannotBeReshuffled pins the length prefixing.
// Concatenating method+target+body would make ("POST", "/a") and ("POS", "T/a")
// the same request.
func TestRequestFingerprint_ComponentsCannotBeReshuffled(t *testing.T) {
	pairs := [][2][3]string{
		{{"POST", "/a", ""}, {"POS", "T/a", ""}},
		{{"POST", "/a", "b"}, {"POST", "/ab", ""}},
		{{"POST", "/a", "bc"}, {"POST", "/a", "b c"}},
	}
	for _, p := range pairs {
		l := requestFingerprint(p[0][0], p[0][1], []byte(p[0][2]))
		r := requestFingerprint(p[1][0], p[1][1], []byte(p[1][2]))
		if l == r {
			t.Fatalf("%v and %v fingerprint identically", p[0], p[1])
		}
	}
}

// TestMainSchedulesTheIdempotencyPurger is the wiring hop, and it is the whole of
// aihub#152 defect 2: PurgeExpiredIdempotencyCache was correct code with no
// caller. Adding StartIdempotencyCachePurger and forgetting to call it would
// leave every test in this file green and the defect exactly where it was.
//
// It reads cmd/aihub/main.go's AST rather than its text, so reformatting or a
// different argument spelling cannot make it pass or fail spuriously; what it
// asserts is that the call exists in main(), which is the only thing that makes
// the sweep run in production.
func TestMainSchedulesTheIdempotencyPurger(t *testing.T) {
	const mainPath = "../../cmd/aihub/main.go"
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, mainPath, nil, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", mainPath, err)
	}

	var mainFn *ast.FuncDecl
	for _, decl := range file.Decls {
		if fd, ok := decl.(*ast.FuncDecl); ok && fd.Name.Name == "main" && fd.Recv == nil {
			mainFn = fd
			break
		}
	}
	if mainFn == nil {
		t.Fatalf("func main not found in %s", mainPath)
	}

	calls := 0
	ast.Inspect(mainFn, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		pkg, ok := sel.X.(*ast.Ident)
		if ok && pkg.Name == "server" && sel.Sel.Name == "StartIdempotencyCachePurger" {
			calls++
			if len(call.Args) != 2 {
				t.Errorf("server.StartIdempotencyCachePurger called with %d args, want 2 (ctx, interval)", len(call.Args))
			}
		}
		return true
	})

	if calls != 1 {
		t.Fatalf("func main calls server.StartIdempotencyCachePurger %d times, want exactly 1 — an unscheduled purger is the defect aihub#152 reported", calls)
	}
}
