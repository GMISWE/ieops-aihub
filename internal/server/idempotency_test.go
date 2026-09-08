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
	"bytes"
	"container/list"
	"context"
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/labstack/echo/v4"

	"github.com/GMISWE/ieops-aihub/internal/domain"
)

// resetIdempotencyCache empties the process-global cache so tests do not inherit
// each other's entries. Registered with t.Cleanup by newIdempotencyHarness.
//
// It resets all FOUR pieces of state, not just the map: the recency list, the
// byte total and the counters are as global as the entries are, and a test that
// asserts on IdempotencyCacheBytes or IdempotencyCacheStats reads whatever the
// previous test left behind otherwise (aihub#461).
func resetIdempotencyCache() {
	idempotencyMu.Lock()
	defer idempotencyMu.Unlock()
	idempotencyCache = map[string]*list.Element{}
	idempotencyLRU = list.New()
	idempotencyBytes = 0
	idempotencyStats = IdempotencyStats{}
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
	const overflow = 500
	keys := make([]string, 0, maxIdempotencyEntries+overflow)
	for i := range maxIdempotencyEntries + overflow {
		key := "k_1:flood-" + strconv.Itoa(i)
		keys = append(keys, key)
		// Distinct, increasing ExpiresAt: with a constant TTL that is insertion
		// order, so this is what makes the FIFO claim in storeIdempotent's doc
		// comment falsifiable. Seeding them all with one timestamp — the first
		// version of this test — leaves eviction picking an arbitrary map entry
		// and the ordering untested.
		storeIdempotent(key, &cachedResponse{
			StatusCode:  200,
			Body:        []byte("x"),
			Fingerprint: "f",
			ExpiresAt:   now.Add(idempotencyTTL + time.Duration(i)*time.Millisecond),
		})
	}

	if len(keys) != len(uniqueStrings(keys)) {
		t.Fatalf("the fixture generated duplicate keys, so it never reached the cap")
	}
	if n := IdempotencyCacheLen(); n > maxIdempotencyEntries {
		t.Fatalf("cache holds %d entries, cap is %d — nothing bounds it", n, maxIdempotencyEntries)
	}
	if n := IdempotencyCacheLen(); n < maxIdempotencyEntries/2 {
		t.Fatalf("cache holds only %d entries; eviction is throwing away far more than it needs to", n)
	}

	// FIFO: the oldest keys went first and the newest are all still there.
	for _, key := range keys[:overflow] {
		if _, ok := loadIdempotent(key); ok {
			t.Fatalf("%s survived; eviction is not dropping the entry closest to expiry first", key)
		}
	}
	for _, key := range keys[len(keys)-overflow:] {
		if _, ok := loadIdempotent(key); !ok {
			t.Fatalf("%s was evicted; the most recent entries must be the ones kept", key)
		}
	}
}

// uniqueStrings is a test helper: the set of distinct values in s.
func uniqueStrings(s []string) []string {
	seen := map[string]struct{}{}
	out := make([]string, 0, len(s))
	for _, v := range s {
		if _, ok := seen[v]; ok {
			continue
		}
		seen[v] = struct{}{}
		out = append(out, v)
	}
	return out
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

	const tick = 2 * time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	startIdempotencyCachePurger(ctx, tick)

	deadline := time.Now().Add(5 * time.Second)
	for IdempotencyCacheLen() != 0 {
		if time.Now().After(deadline) {
			t.Fatalf("the purger never ran: cache still holds %d expired entries", IdempotencyCacheLen())
		}
		time.Sleep(tick)
	}

	// Stopping: after cancel, a newly inserted expired entry must survive. The
	// generous margins are deliberate — a sweep already in flight when cancel
	// lands must be allowed to finish BEFORE the probe entry is inserted, or a
	// scheduling stall makes this test flake rather than fail.
	cancel()
	time.Sleep(100 * tick)
	storeIdempotent("k_1:stale2", &cachedResponse{StatusCode: 200, Fingerprint: "f", ExpiresAt: time.Now().Add(-time.Second)})
	time.Sleep(100 * tick)
	if n := IdempotencyCacheLen(); n != 1 {
		t.Fatalf("cache holds %d entries after cancel, want 1 — the purger goroutine outlived its context", n)
	}
}

// TestIdempotencyPurgeInterval_IsPositive is the assertion that replaces the one
// an independent review measured to be unfalsifiable. The scheduler used to take
// an interval argument, and passing 0 made it return without starting anything —
// the production purger never ran and TestMainSchedulesTheIdempotencyPurger below
// stayed green, because counting a call's arguments in an AST says nothing about
// their values. The argument is gone; this pins the constant that replaced it,
// which is a value a test CAN evaluate.
func TestIdempotencyPurgeInterval_IsPositive(t *testing.T) {
	if idempotencyPurgeInterval <= 0 {
		t.Fatalf("idempotencyPurgeInterval is %v; a non-positive interval means the sweep never runs", idempotencyPurgeInterval)
	}
	if idempotencyPurgeInterval >= idempotencyTTL {
		t.Fatalf("idempotencyPurgeInterval %v is not shorter than the TTL %v, so entries outlive their expiry by a whole sweep",
			idempotencyPurgeInterval, idempotencyTTL)
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
			// One argument, the context. The cadence is a package constant
			// (idempotencyPurgeInterval) precisely so that this test does not have
			// to reason about an argument's VALUE, which it cannot do — see
			// TestIdempotencyPurgeInterval_IsPositive.
			if len(call.Args) != 1 {
				t.Errorf("server.StartIdempotencyCachePurger called with %d args, want 1 (ctx)", len(call.Args))
			}
		}
		return true
	})

	if calls != 1 {
		t.Fatalf("func main calls server.StartIdempotencyCachePurger %d times, want exactly 1 — an unscheduled purger is the defect aihub#152 reported", calls)
	}
}

// ─── aihub#461: the byte budget, O(1) eviction and the calibration instrument ──
//
// Everything below is new in aihub#461. Read the constant block in
// idempotency.go first: the numbers these tests defend were measured against a
// populated cache for the first time (aihub#436 populated it; nothing sent the
// header before that), and the measurement is dated because it will rot.

// TestIdempotencyCaps_AreMutuallyConsistent gates the relationships BETWEEN the
// caps, which is where a plausible-looking edit to any one of them goes wrong.
//
// Each clause names the failure it prevents, because "these constants look
// reasonable" is not a test.
func TestIdempotencyCaps_AreMutuallyConsistent(t *testing.T) {
	// A single maximal entry must not be able to dominate the budget. If it
	// could, one big response would evict the entire cache to make room for
	// itself, and makeRoomLocked's nil-victim branch would be reachable rather
	// than the assertion it is documented as.
	if minBudget := 16 * (maxIdempotencyBodyBytes + idempotencyEntryOverhead); maxIdempotencyTotalBytes < minBudget {
		t.Errorf("maxIdempotencyTotalBytes=%d is under 16 maximal entries (%d); one large response would evict everything",
			maxIdempotencyTotalBytes, minBudget)
	}

	// Both caps must be reachable, or one of them is decoration. The crossover
	// is maxIdempotencyTotalBytes/maxIdempotencyEntries: below that mean entry
	// size the entry cap binds first, above it the byte budget does. The
	// measured mean is ~770 B, so the crossover has to sit ABOVE that (else the
	// entry cap could never bind in production) and within an order of
	// magnitude of maxIdempotencyBodyBytes (else the byte budget could not bind
	// before the entry cap even for the largest permitted responses).
	crossover := maxIdempotencyTotalBytes / maxIdempotencyEntries
	if crossover <= 1024 {
		t.Errorf("crossover mean entry size is %d B, at or below the measured mean of ~770 B: the byte budget would bind first on ordinary traffic and the entry cap would be dead", crossover)
	}
	if crossover >= maxIdempotencyBodyBytes {
		t.Errorf("crossover mean entry size is %d B, at or above the per-entry cap %d: no reachable response mix could ever make the byte budget bind",
			crossover, maxIdempotencyBodyBytes)
	}
}

// idempotencyFlood stores n entries of bodyBytes each, with strictly increasing
// ExpiresAt (constant TTL ⇒ insertion order), and returns their keys.
func idempotencyFlood(t *testing.T, n, bodyBytes int, prefix string) []string {
	t.Helper()
	now := time.Now()
	body := []byte(strings.Repeat("x", bodyBytes))
	keys := make([]string, 0, n)
	for i := range n {
		key := "k_1:" + prefix + strconv.Itoa(i)
		keys = append(keys, key)
		storeIdempotent(key, &cachedResponse{
			StatusCode:  200,
			Body:        body,
			Fingerprint: strings.Repeat("f", 64),
			ExpiresAt:   now.Add(idempotencyTTL + time.Duration(i)*time.Millisecond),
		})
	}
	if len(keys) != len(uniqueStrings(keys)) {
		t.Fatalf("the fixture generated duplicate keys, so it never reached any cap")
	}
	return keys
}

// TestIdempotency_ByteBudgetBoundsTheCache is the cap aihub#461 added, stated as
// the property that matters: entries the entry cap would happily hold are
// evicted because their BYTES do not fit.
//
// The fixture is sized so the entry cap cannot be what bounds the result — 600
// entries against a cap of 4096 — so a green result cannot be produced by the
// pre-existing entry cap doing the work.
func TestIdempotency_ByteBudgetBoundsTheCache(t *testing.T) {
	resetIdempotencyCache()
	t.Cleanup(resetIdempotencyCache)

	const bodyBytes = 64 << 10 // 64 KiB: ~512 of these fill the 32 MiB budget
	const entries = 600
	keys := idempotencyFlood(t, entries, bodyBytes, "bytes-")

	if got := IdempotencyCacheBytes(); got > maxIdempotencyTotalBytes {
		t.Fatalf("cache accounts %d bytes, budget is %d — nothing bounds it", got, maxIdempotencyTotalBytes)
	}
	if n := IdempotencyCacheLen(); n >= entries {
		t.Fatalf("all %d entries survived: %d × %d B is %d bytes, which is over the %d-byte budget, so the byte cap did nothing",
			n, entries, bodyBytes, entries*bodyBytes, maxIdempotencyTotalBytes)
	}
	if n := IdempotencyCacheLen(); n > maxIdempotencyEntries {
		t.Fatalf("cache holds %d entries, over the entry cap %d", n, maxIdempotencyEntries)
	}

	// It has to be the BYTE cap that evicted, not the entry cap: 600 < 4096.
	s := IdempotencyCacheStats()
	if s.EvictedForByteCap == 0 {
		t.Fatalf("no eviction was attributed to the byte cap (stats: %+v); either the accounting is not maintained or the entry cap is doing the work", s)
	}
	if s.EvictedForEntryCap != 0 {
		t.Fatalf("%d evictions were attributed to the entry cap with only %d entries stored; the attribution is wrong", s.EvictedForEntryCap, entries)
	}

	// The oldest went first and the newest survived, as in the entry-cap case.
	if _, ok := loadIdempotent(keys[0]); ok {
		t.Errorf("%s survived; eviction is not dropping the least recently used entry first", keys[0])
	}
	if _, ok := loadIdempotent(keys[len(keys)-1]); !ok {
		t.Errorf("%s was evicted; the most recent entry must be the one kept", keys[len(keys)-1])
	}
}

// TestIdempotency_ByteAccountingIsMaintainedOnEveryRemovalPath is the test for
// the failure mode that has no symptom: a removal that forgets to decrement the
// byte total. The cache then shrinks while its accounted size grows, and the
// budget evicts a healthy cache down to nothing — with IdempotencyCacheLen, the
// only pre-aihub#461 observable, reporting nothing unusual.
//
// All three removal paths are exercised: expiry-on-read, the purge sweep, and
// eviction.
func TestIdempotency_ByteAccountingIsMaintainedOnEveryRemovalPath(t *testing.T) {
	resetIdempotencyCache()
	t.Cleanup(resetIdempotencyCache)

	// Path 1: expiry on read.
	storeIdempotent("k_1:expired", &cachedResponse{StatusCode: 200, Body: []byte("body"), Fingerprint: "f", ExpiresAt: time.Now().Add(-time.Second)})
	if got := IdempotencyCacheBytes(); got == 0 {
		t.Fatalf("storing an entry accounted 0 bytes")
	}
	if _, ok := loadIdempotent("k_1:expired"); ok {
		t.Fatalf("an expired entry was reported live")
	}
	if got := IdempotencyCacheBytes(); got != 0 {
		t.Errorf("after expiry-on-read the cache holds %d entries and accounts %d bytes, want 0", IdempotencyCacheLen(), got)
	}

	// Path 2: the purge sweep.
	storeIdempotent("k_1:stale", &cachedResponse{StatusCode: 200, Body: []byte("body"), Fingerprint: "f", ExpiresAt: time.Now().Add(-time.Second)})
	PurgeExpiredIdempotencyCache()
	if got := IdempotencyCacheBytes(); got != 0 {
		t.Errorf("after the purge sweep the cache accounts %d bytes, want 0", got)
	}

	// Path 3: eviction, and the accounted total must equal a fresh sum over
	// what is actually left rather than a number that has drifted.
	idempotencyFlood(t, maxIdempotencyEntries+64, 128, "acct-")
	want := 0
	idempotencyMu.Lock()
	for _, el := range idempotencyCache {
		want += el.Value.(*idempotencyEntry).bytes
	}
	got := idempotencyBytes
	idempotencyMu.Unlock()
	if got != want {
		t.Errorf("accounted total is %d bytes but the live entries sum to %d — a removal path is not maintaining it", got, want)
	}

	// And a repeated key must replace rather than double-count.
	before := IdempotencyCacheBytes()
	entry := &cachedResponse{StatusCode: 200, Body: []byte("same"), Fingerprint: "f", ExpiresAt: time.Now().Add(time.Hour)}
	storeIdempotent("k_1:repeat", entry)
	afterFirst := IdempotencyCacheBytes()
	storeIdempotent("k_1:repeat", entry)
	if afterSecond := IdempotencyCacheBytes(); afterSecond != afterFirst {
		t.Errorf("re-storing one key moved the accounted total from %d to %d (started at %d); the same key must not be counted twice",
			afterFirst, afterSecond, before)
	}
}

// TestIdempotencyEviction_IsConstantWork is the O(1) claim in makeRoomLocked,
// gated.
//
// It counts list elements examined (IdempotencyStats.EvictionScanned) rather
// than measuring elapsed time, and the timing is what says why that matters
// rather than what the gate asserts. Measured on one idle machine, 2,000
// inserts into a full cache: 418.7µs each through the scan this replaced,
// 1.2µs each through the recency list — and the first figure is a LOWER bound
// on the original, which ran an expiry sweep over the whole cache before the
// victim scan as well. A threshold separating those two is still a flake on a
// loaded CI runner, whereas counting the work is deterministic: an O(n)
// implementation lands near evictions × maxIdempotencyEntries instead of near
// evictions, which is the 2,048,500-versus-500 this gate was checked against.
func TestIdempotencyEviction_IsConstantWork(t *testing.T) {
	resetIdempotencyCache()
	t.Cleanup(resetIdempotencyCache)

	const overflow = 500
	idempotencyFlood(t, maxIdempotencyEntries+overflow, 16, "scan-")

	s := IdempotencyCacheStats()
	evicted := s.EvictedForEntryCap + s.EvictedForByteCap
	if evicted == 0 {
		t.Fatalf("flooding %d entries past a cap of %d evicted nothing; the fixture, not the eviction, is broken",
			overflow, maxIdempotencyEntries)
	}
	// One examination per eviction is what the recency list costs. The slack
	// absorbs bookkeeping without admitting a scan: even 2 examinations per
	// eviction is O(1), while one O(n) pass over this cache is 4096.
	if budget := 2*evicted + 16; s.EvictionScanned > budget {
		t.Fatalf("eviction examined %d entries to evict %d (budget %d): that is a scan over the cache, not O(1) work",
			s.EvictionScanned, evicted, budget)
	}
}

// TestIdempotency_LRUKeepsWhatWasRecentlyUsed separates LRU from the FIFO it
// replaced, and it is the discriminating test for the eviction ORDER: under
// FIFO the promoted entry below is the very next one evicted, and under an
// evict-from-the-front mutation the newest entry disappears instead.
//
// The behaviour matters exactly when this cache stops being write-only. Today
// nothing reuses a key (see IdempotencyMiddleware) so recency order equals
// insertion order and LRU degenerates to the FIFO it replaced — the point is
// that the day a reusing caller appears, the entry it keeps hitting is the one
// eviction must not take.
func TestIdempotency_LRUKeepsWhatWasRecentlyUsed(t *testing.T) {
	resetIdempotencyCache()
	t.Cleanup(resetIdempotencyCache)

	keys := idempotencyFlood(t, maxIdempotencyEntries, 16, "lru-")
	oldest, secondOldest := keys[0], keys[1]

	// Use the oldest entry. This is the only thing that distinguishes it.
	if _, ok := loadIdempotent(oldest); !ok {
		t.Fatalf("%s is missing before the cache was even over its cap", oldest)
	}

	// One more entry: exactly one eviction.
	storeIdempotent("k_1:lru-newcomer", &cachedResponse{
		StatusCode: 200, Body: []byte("x"), Fingerprint: "f",
		ExpiresAt: time.Now().Add(idempotencyTTL),
	})

	if _, ok := loadIdempotent(oldest); !ok {
		t.Errorf("%s was evicted although it was the most recently USED entry; eviction is ordered by insertion, not by use", oldest)
	}
	if _, ok := loadIdempotent(secondOldest); ok {
		t.Errorf("%s survived; the least recently used entry was not the victim", secondOldest)
	}
	if _, ok := loadIdempotent("k_1:lru-newcomer"); !ok {
		t.Errorf("the entry just stored was evicted; eviction is taking from the front of the recency list")
	}
}

// idempotencyMeasuredMix is the mutating-request mix this cache was sized
// against: calls per 24h at the busiest 24h in the record
// (2026-09-07T12:46Z → 2026-09-08T12:46Z, ~1.8e3 mutating requests server-wide),
// paired with the real Go value each endpoint answers with.
//
// 🔴 It is a PINNED MEASUREMENT and it will rot. Re-derive it from the line
// StartIdempotencyCachePurger writes every 10 minutes:
//
//	docker logs <container> 2>&1 | grep -F 'idempotency:' | tail -20
//
// peak_entries there is this table's `calls` column summed, measured rather
// than estimated, and peak_bytes is what this test computes. If they disagree
// by more than ~2×, fix the table — not the assertion below.
//
// How the `calls` column was counted — including which parts of it are
// bracketed rather than observed — is on maxIdempotencyTotalBytes. Do not
// re-derive it from this list: the two sources have opposite biases and the
// per-endpoint mapping is the part that needs the caveats, which are stated
// once, there.
//
// The `value` column is the actual response type, not a transcript of one, so
// this measurement follows the struct: add a field to domain.WorkItem and the
// steady state recomputes here. The `target` paths are synthetic — only the
// response shape is under measurement — and the work-item content is 850
// characters, chosen so the fixture serialises to 1,754 B: the mean response
// over the 1,102 real work items whose distribution is on
// maxIdempotencyBodyBytes.
var idempotencyMeasuredMix = []struct {
	name   string
	calls  int
	method string
	target string
	value  any
}{
	{"step_update_fused", 400, http.MethodPatch, "/v1/shape/step",
		map[string]any{"status": "completed", "next_step": "code_review", "next_step_status": "in_progress"}},
	{"event_created", 421, http.MethodPost, "/v1/shape/event",
		map[string]string{"event_id": "evt_MnQ4xR2v"}},
	{"memory_created", 217, http.MethodPost, "/v1/shape/memory", map[string]any{
		"id": "mem_8sKd0PqW", "memory_id": "mem_8sKd0PqW", "is_new": true,
		"type": "methodology.execute", "project": "aihub", "visibility": "project",
		"activation_count": 0, "stability_days": 1.0, "base_strength": 3.0,
		"created_at": time.Date(2026, 9, 8, 12, 46, 19, 0, time.UTC),
	}},
	{"work_item_created", 121, http.MethodPost, "/v1/shape/wi_create", measuredWorkItemFixture(850)},
	{"work_item_updated", 190, http.MethodPatch, "/v1/shape/wi_update", measuredWorkItemFixture(850)},
	{"attempt_claimed", 76, http.MethodPost, "/v1/shape/claim", measuredClaimFixture()},
	{"attempt_completed", 82, http.MethodPost, "/v1/shape/complete", map[string]bool{"ok": true}},
	{"locks_acquired", 75, http.MethodPost, "/v1/shape/locks", measuredLocksFixture()},
	{"commit_locks", 130, http.MethodPost, "/v1/shape/commit_locks", &domain.ReconcileCommitLocksResponse{
		Checked: 3, Probed: 3,
		Covered:       []string{"internal/server/idempotency.go", "internal/server/idempotency_test.go"},
		AcquiredPaths: []string{"internal/server/router.go"},
	}},
	{"dependency_created", 26, http.MethodPost, "/v1/shape/dependency",
		map[string]any{"blocked_work_item_id": "wi_UZ0OzzEB", "blocking_work_item_id": "wi_KrKQAik0", "kind": "blocks"}},
	{"misc_small", 18, http.MethodPost, "/v1/shape/misc", map[string]bool{"ok": true}},
}

// measuredWorkItemFixture builds the largest mutating response shape in the API
// — the full domain.WorkItem that POST /v1/work_items and PATCH
// /v1/work_items/:id both answer with — with `content` of contentBytes
// characters.
func measuredWorkItemFixture(contentBytes int) *domain.WorkItem {
	wiType, rhs, content := "chore", true, strings.Repeat("x", contentBytes)
	at := time.Date(2026, 9, 8, 12, 34, 21, 0, time.UTC)
	attempt := "ra_KrKQAik0"
	return &domain.WorkItem{
		ID: "wi_UZ0OzzEB", Seq: 461, Slug: "aihub#461", Project: "aihub", Scenario: "coding",
		Goal:     "re-check the idempotency cache caps now that every POST/PATCH populates them",
		Source:   "human",
		WIType:   &wiType,
		Priority: "normal", RequiresHumanSession: &rhs,
		Labels: []string{"idempotency", "aihub-436-followup", "capacity"},
		Status: "running",
		DeclaredResources: json.RawMessage(`[{"intent":"write","repo":"aihub","type":"path","uri":"file:internal/server/idempotency.go"},` +
			`{"intent":"write","repo":"aihub","type":"path","uri":"file:internal/server/idempotency_test.go"}]`),
		ResourcesVersion: 1,
		ReporterUserID:   "u_5dFjeaMZ", ReporterDisplay: "xiaokang.w",
		CurrentAttemptID: &attempt, CurrentAttemptEpoch: 1,
		Attrs:     json.RawMessage(`{}`),
		Content:   &content,
		CreatedAt: at, UpdatedAt: at,
	}
}

func measuredClaimFixture() *domain.ClaimResponse {
	rhs, wiType := true, "chore"
	return &domain.ClaimResponse{
		AttemptID: "ra_KrKQAik0", ClaimEpoch: 1, CurrentAttemptEpoch: 1,
		AcquiredLocks: []domain.ResourceLock{
			{ResourceType: "file_scope", ResourceKey: "aihub:aihub:internal/server/idempotency.go", OwnerAttemptID: "ra_KrKQAik0", ClaimEpoch: 1},
			{ResourceType: "file_scope", ResourceKey: "aihub:aihub:internal/server/idempotency_test.go", OwnerAttemptID: "ra_KrKQAik0", ClaimEpoch: 1},
		},
		RequiresHumanSession: &rhs, WIType: &wiType,
		Slug: "aihub#461", Project: "aihub", ID: "wi_UZ0OzzEB",
		Goal: "re-check the idempotency cache caps now that every POST/PATCH populates them",
	}
}

func measuredLocksFixture() *domain.AcquireLocksResponse {
	return &domain.AcquireLocksResponse{
		Acquired: []domain.ResourceLock{},
		AlreadyHeld: []domain.ResourceLock{
			{ResourceType: "file_scope", ResourceKey: "aihub:aihub:internal/server/idempotency.go", OwnerAttemptID: "ra_KrKQAik0", ClaimEpoch: 1},
			{ResourceType: "file_scope", ResourceKey: "aihub:aihub:internal/server/idempotency_test.go", OwnerAttemptID: "ra_KrKQAik0", ClaimEpoch: 1},
		},
	}
}

// TestIdempotency_MeasuredSteadyStateFitsTheBudget is the derivation of
// maxIdempotencyTotalBytes, executed rather than asserted in a comment.
//
// It drives the REAL middleware over the REAL response types (an in-memory echo
// server; the only synthetic parts are the request paths and the traffic
// weights), reads each shape's accounted cost from IdempotencyCacheBytes, and
// checks that a full TTL window of the measured mix leaves the budget with
// headroom — and that the budget is not so far above the measurement that it
// stops bounding anything.
//
// Both bounds catch a real defect. Too small: the cache evicts inside its TTL
// on an ordinary day, silently narrowing the replay window. Too large: 4 GiB is
// what this constant replaced, which is not a bound a container can act on.
func TestIdempotency_MeasuredSteadyStateFitsTheBudget(t *testing.T) {
	resetIdempotencyCache()
	t.Cleanup(resetIdempotencyCache)

	e := echo.New()
	e.Use(func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			c.Set(string(ctxUser), &UserContext{UserID: "u_test", Role: "writer", APIKeyID: "k_measure"})
			return next(c)
		}
	})
	e.Use(IdempotencyMiddleware())
	for _, shape := range idempotencyMeasuredMix {
		value := shape.value
		e.Add(shape.method, shape.target, func(c echo.Context) error {
			return c.JSON(http.StatusOK, value)
		})
	}

	total, calls := 0, 0
	for _, shape := range idempotencyMeasuredMix {
		before := IdempotencyCacheBytes()
		req := httptest.NewRequest(shape.method, shape.target, strings.NewReader(`{"probe":1}`))
		req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
		req.Header.Set("Idempotency-Key", "measure-"+shape.name)
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: status %d, want 200 — the fixture never reached the cache", shape.name, rec.Code)
		}
		size := IdempotencyCacheBytes() - before
		if size <= 0 {
			t.Fatalf("%s: the response was not cached (accounted delta %d)", shape.name, size)
		}
		if size*4 > maxIdempotencyBodyBytes {
			t.Errorf("%s: one entry accounts %d B, within 4× of the per-entry cap %d — the cap no longer has the headroom its comment claims",
				shape.name, size, maxIdempotencyBodyBytes)
		}
		t.Logf("%-20s %5d calls/24h × %6d B = %9d B", shape.name, shape.calls, size, shape.calls*size)
		total += shape.calls * size
		calls += shape.calls
	}

	t.Logf("measured steady state: %d entries, %d B (%.2f MiB); budget %d B (%.0f×)",
		calls, total, float64(total)/(1<<20), maxIdempotencyTotalBytes, float64(maxIdempotencyTotalBytes)/float64(total))

	if total*8 > maxIdempotencyTotalBytes {
		t.Errorf("a TTL window of the measured mix is %d B and the budget is %d B — under 8× headroom, so an ordinary busy day evicts inside the TTL",
			total, maxIdempotencyTotalBytes)
	}
	if total*64 < maxIdempotencyTotalBytes {
		t.Errorf("a TTL window of the measured mix is %d B and the budget is %d B — over 64× the measurement, so the budget is not bounding anything reachable",
			total, maxIdempotencyTotalBytes)
	}
}

// syncBuffer is a bytes.Buffer that is safe to write from the purger goroutine
// while the test reads it. Without the mutex this test is a data race that only
// fails under -race, which CI runs.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// TestIdempotencyCachePurger_LogsTheCalibrationLine tests the WIRING, not the
// formatter: that the goroutine production actually runs emits the line the
// constant block tells the next person to re-measure from. A correct formatter
// nobody calls is the aihub#152 defect over again (PurgeExpiredIdempotencyCache
// was correct code with no caller).
func TestIdempotencyCachePurger_LogsTheCalibrationLine(t *testing.T) {
	resetIdempotencyCache()
	t.Cleanup(resetIdempotencyCache)

	var out syncBuffer
	prev := setIdempotencyLogWriter(&out)
	t.Cleanup(func() { setIdempotencyLogWriter(prev) })

	storeIdempotent("k_1:logged", &cachedResponse{
		StatusCode: 200, Body: []byte(`{"ok":true}`), Fingerprint: "f",
		ExpiresAt: time.Now().Add(idempotencyTTL),
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	startIdempotencyCachePurger(ctx, 2*time.Millisecond)

	deadline := time.Now().Add(2 * time.Second)
	for !strings.Contains(out.String(), "idempotency:") {
		if time.Now().After(deadline) {
			t.Fatalf("the purger ran for 2s and never wrote a stats line; got %q", out.String())
		}
		time.Sleep(2 * time.Millisecond)
	}

	line := out.String()
	// The keys a calibration reader needs. bytes= and hits= are the two that
	// did not exist before aihub#461, and hits= is the one that can falsify
	// the "structurally zero hit rate" this cache is sized against.
	for _, want := range []string{"entries=", "bytes=", "peak_entries=", "peak_bytes=", "stores=", "hits=", "replays=", "evicted_byte_cap="} {
		if !strings.Contains(line, want) {
			t.Errorf("the stats line is missing %q: %s", want, line)
		}
	}
}

// TestIdempotencyCacheStats_CountHitsReplaysAndReuse makes the counters
// falsifiable. They are the instrument the sizing above depends on, and an
// instrument that reports zero because it is never incremented is
// indistinguishable from the finding it is supposed to be able to report — a
// production hit rate of exactly zero.
func TestIdempotencyCacheStats_CountHitsReplaysAndReuse(t *testing.T) {
	h := newIdempotencyHarness(t, "k_stats")

	if s := IdempotencyCacheStats(); s.Stores != 0 || s.Hits != 0 {
		t.Fatalf("counters are not reset between tests: %+v", s)
	}

	h.do(http.MethodPost, "/v1/work_items", `{"goal":"a"}`, "stats-key")
	if s := IdempotencyCacheStats(); s.Stores != 1 || s.Hits != 0 || s.Replays != 0 {
		t.Errorf("after one fresh request: stores=%d hits=%d replays=%d, want 1/0/0", s.Stores, s.Hits, s.Replays)
	}

	// Same key, same request: a replay.
	h.do(http.MethodPost, "/v1/work_items", `{"goal":"a"}`, "stats-key")
	if s := IdempotencyCacheStats(); s.Hits != 1 || s.Replays != 1 || s.ReuseRejected != 0 {
		t.Errorf("after a replay: hits=%d replays=%d reuse_rejected=%d, want 1/1/0", s.Hits, s.Replays, s.ReuseRejected)
	}

	// Same key, different body: a 409, which is a hit that is NOT a replay.
	rec := h.do(http.MethodPost, "/v1/work_items", `{"goal":"b"}`, "stats-key")
	if rec.Code != http.StatusConflict {
		t.Fatalf("reused key with a different body: status %d, want 409", rec.Code)
	}
	if s := IdempotencyCacheStats(); s.Hits != 2 || s.Replays != 1 || s.ReuseRejected != 1 {
		t.Errorf("after a rejected reuse: hits=%d replays=%d reuse_rejected=%d, want 2/1/1", s.Hits, s.Replays, s.ReuseRejected)
	}
}

// TestIdempotencyEviction_HasNoScanOverTheCache is the structural half of the
// O(1) claim, and it exists because the counter half can be defeated by the
// obvious mistake: reintroducing a scan WITHOUT instrumenting it leaves
// EvictionScanned small and TestIdempotencyEviction_IsConstantWork green. A
// counter reports what the code chooses to report; this reads the source.
//
// The rule is narrow on purpose — makeRoomLocked may contain the one loop that
// drops victims one at a time, and nothing else. A `range` over the map or the
// list, or a second nested loop, is the two-pass sweep-then-scan this replaced.
func TestIdempotencyEviction_HasNoScanOverTheCache(t *testing.T) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "idempotency.go", nil, 0)
	if err != nil {
		t.Fatalf("parse idempotency.go: %v", err)
	}

	var body *ast.BlockStmt
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if ok && fn.Name.Name == "makeRoomLocked" {
			body = fn.Body
		}
	}
	if body == nil {
		t.Fatal("makeRoomLocked is gone; if eviction moved, move this gate with it rather than deleting it")
	}

	loops, ranges := 0, 0
	ast.Inspect(body, func(n ast.Node) bool {
		switch n.(type) {
		case *ast.ForStmt:
			loops++
		case *ast.RangeStmt:
			ranges++
		}
		return true
	})
	if ranges != 0 {
		t.Errorf("makeRoomLocked contains %d range statement(s): iterating the cache to choose a victim is the O(n) eviction aihub#461 removed", ranges)
	}
	if loops > 1 {
		t.Errorf("makeRoomLocked contains %d for-loops, want at most 1 (the one that drops victims): a nested loop is a scan over the cache", loops)
	}
}
