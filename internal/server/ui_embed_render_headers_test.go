package server

import (
	"html/template"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/labstack/echo/v4"
)

// TestRenderTemplateVaryHeader guards the htmx "same URL, two response bodies"
// cache-poisoning bug. When the same URL serves a full page for direct
// navigation and a bare fragment for htmx fetches, any cache that stores either
// response without a Vary: HX-Request header will serve the wrong body for the
// other request type. Additionally, bare fragment responses must not be stored
// at all (Cache-Control: no-store) to prevent BFCache / proxy replays of
// unstyled fragments on full-page navigations.
//
// The discriminator is the HX-Request REQUEST header, not the template name:
// a fragment is only ever produced in response to an htmx request, so this is
// the correct, robust signal and never matches a normal full-page navigation
// (e.g. the login page, which is rendered with a non-"layout" block name but
// is NOT an htmx request and must NOT get Cache-Control: no-store).
func TestRenderTemplateVaryHeader(t *testing.T) {
	// Build a minimal template set covering both paths:
	//   layout        → full-page response (200)
	//   wi-list-body  → fragment response (200)
	tmpl := template.Must(template.New("layout").Parse(`<html><head></head><body>x</body></html>`))
	template.Must(tmpl.New("wi-list-body").Parse(`<div class="list"></div>`))

	t.Run("full-page non-htmx (layout): Vary set, no Cache-Control", func(t *testing.T) {
		e := echo.New()
		req := httptest.NewRequest(http.MethodGet, "/ui/wi", nil)
		// No HX-Request header — this is a plain full-page navigation.
		rec := httptest.NewRecorder()
		c := e.NewContext(req, rec)

		if err := renderTemplate(c, tmpl, "layout", nil); err != nil {
			t.Fatalf("renderTemplate(layout) returned error: %v", err)
		}
		if got := rec.Header().Get("Vary"); got != "HX-Request" {
			t.Errorf("Vary = %q; want %q", got, "HX-Request")
		}
		if got := rec.Header().Get("Cache-Control"); got != "" {
			t.Errorf("Cache-Control = %q; want empty for full-page (non-htmx) render", got)
		}
	})

	t.Run("fragment htmx request: Vary set and Cache-Control=no-store", func(t *testing.T) {
		e := echo.New()
		req := httptest.NewRequest(http.MethodGet, "/ui/wi", nil)
		req.Header.Set("HX-Request", "true")
		rec := httptest.NewRecorder()
		c := e.NewContext(req, rec)

		if err := renderTemplate(c, tmpl, "wi-list-body", nil); err != nil {
			t.Fatalf("renderTemplate(wi-list-body) returned error: %v", err)
		}
		if got := rec.Header().Get("Vary"); got != "HX-Request" {
			t.Errorf("Vary = %q; want %q", got, "HX-Request")
		}
		if got := rec.Header().Get("Cache-Control"); got != "no-store" {
			t.Errorf("Cache-Control = %q; want %q", got, "no-store")
		}
	})

	// Regression guard: the login page (and any other full-page render that does
	// not use "layout" as its block name) must NOT get Cache-Control: no-store
	// when served to a non-htmx request. The old discriminator (name != "layout")
	// would have wrongly set no-store here.
	t.Run("regression: full-page non-htmx non-layout name: no Cache-Control", func(t *testing.T) {
		loginTmpl := template.Must(template.New("login").Parse(`<html><body>login</body></html>`))

		e := echo.New()
		req := httptest.NewRequest(http.MethodGet, "/ui/login", nil)
		// No HX-Request header — plain browser navigation to the login page.
		rec := httptest.NewRecorder()
		c := e.NewContext(req, rec)

		if err := renderTemplate(c, loginTmpl, "login", nil); err != nil {
			t.Fatalf("renderTemplate(login) returned error: %v", err)
		}
		if got := rec.Header().Get("Cache-Control"); got != "" {
			t.Errorf("Cache-Control = %q; want empty — non-htmx full-page must not get no-store", got)
		}
	})

	// Lock request-based semantics: an htmx request gets no-store even when the
	// template happens to be named "layout" (e.g. a boosted navigation that the
	// handler treats as a fragment by some future code path). The old name-based
	// check would have wrongly allowed caching here.
	t.Run("htmx request with layout name: Cache-Control=no-store", func(t *testing.T) {
		e := echo.New()
		req := httptest.NewRequest(http.MethodGet, "/ui/wi", nil)
		req.Header.Set("HX-Request", "true")
		rec := httptest.NewRecorder()
		c := e.NewContext(req, rec)

		if err := renderTemplate(c, tmpl, "layout", nil); err != nil {
			t.Fatalf("renderTemplate(layout, htmx) returned error: %v", err)
		}
		if got := rec.Header().Get("Cache-Control"); got != "no-store" {
			t.Errorf("Cache-Control = %q; want %q — htmx request must always get no-store", got, "no-store")
		}
	})
}

// TestRenderHTMLStatusVaryHeader checks that the 404-status variant also sets
// Vary: HX-Request. It is always called with name="layout" on a non-htmx
// request (the detail page's 404 path), so Cache-Control must be empty.
func TestRenderHTMLStatusVaryHeader(t *testing.T) {
	tmpl := template.Must(template.New("layout").Parse(`<html><head></head><body>not found</body></html>`))

	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/ui/wi/missing", nil)
	// No HX-Request header — full-page 404 from a direct browser navigation.
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	if err := renderHTMLStatus(c, tmpl, "layout", nil, http.StatusNotFound); err != nil {
		t.Fatalf("renderHTMLStatus returned error: %v", err)
	}
	if got := rec.Header().Get("Vary"); got != "HX-Request" {
		t.Errorf("Vary = %q; want %q", got, "HX-Request")
	}
	if got := rec.Header().Get("Cache-Control"); got != "" {
		t.Errorf("Cache-Control = %q; want empty for full-page 404 (non-htmx)", got)
	}
	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d; want 404", rec.Code)
	}
}
