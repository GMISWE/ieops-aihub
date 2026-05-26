package server

// Smoke tests for the /ui/* route tree.
//
// These intentionally only exercise paths that don't require a live DB:
//   - the /ui/ redirect
//   - the /ui/login page (no DB lookup until POST)
//   - the unauth bounce on /ui/queue
//   - the embedded static assets
//
// They do NOT test the page-handler logic (which lives in peer-subagent files
// and is covered by their own tests). They DO require that those handlers
// register themselves at /ui/queue, /ui/wi, /ui/memories — once those land
// the full router compiles and this file runs as part of `go test ./...`.

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestUIRoutes_RootRedirectsToQueue(t *testing.T) {
	e := NewRouter(nil, []byte("smoke-test-secret"))

	req := httptest.NewRequest(http.MethodGet, "/ui/", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusFound {
		t.Fatalf("GET /ui/: expected 302, got %d", rec.Code)
	}
	if loc := rec.Header().Get("Location"); loc != "/ui/queue" {
		t.Fatalf("GET /ui/: expected redirect to /ui/queue, got %q", loc)
	}
}

func TestUIRoutes_LoginGet_RendersForm(t *testing.T) {
	e := NewRouter(nil, []byte("smoke-test-secret"))

	req := httptest.NewRequest(http.MethodGet, "/ui/login", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("GET /ui/login: expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "<form") {
		t.Errorf("GET /ui/login: body missing <form>")
	}
	if !strings.Contains(body, `name="api_key"`) {
		t.Errorf("GET /ui/login: body missing api_key field")
	}
}

func TestUIRoutes_QueueWithoutCookie_RedirectsToLogin(t *testing.T) {
	e := NewRouter(nil, []byte("smoke-test-secret"))

	req := httptest.NewRequest(http.MethodGet, "/ui/queue", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusFound {
		t.Fatalf("GET /ui/queue (no cookie): expected 302, got %d", rec.Code)
	}
	loc := rec.Header().Get("Location")
	if !strings.HasPrefix(loc, "/ui/login") {
		t.Fatalf("GET /ui/queue (no cookie): expected redirect to /ui/login, got %q", loc)
	}
	if !strings.Contains(loc, "next=") {
		t.Errorf("GET /ui/queue (no cookie): expected next= in redirect, got %q", loc)
	}
}

func TestUIRoutes_StaticCSSServed(t *testing.T) {
	e := NewRouter(nil, []byte("smoke-test-secret"))

	req := httptest.NewRequest(http.MethodGet, "/ui/static/ui.css", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("GET /ui/static/ui.css: expected 200, got %d", rec.Code)
	}
	ct := rec.Header().Get("Content-Type")
	if !strings.Contains(ct, "text/css") {
		t.Errorf("GET /ui/static/ui.css: expected text/css content-type, got %q", ct)
	}
	if rec.Body.Len() < 100 {
		t.Errorf("GET /ui/static/ui.css: body too small (%d bytes)", rec.Body.Len())
	}
}

func TestUIRoutes_StaticHTMXServed(t *testing.T) {
	e := NewRouter(nil, []byte("smoke-test-secret"))

	req := httptest.NewRequest(http.MethodGet, "/ui/static/htmx.min.js", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("GET /ui/static/htmx.min.js: expected 200, got %d", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "htmx") {
		t.Errorf("GET /ui/static/htmx.min.js: body does not look like htmx (no 'htmx' substring in first chunk)")
	}
}
