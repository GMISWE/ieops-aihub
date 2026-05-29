package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/labstack/echo/v4"

	"github.com/GMISWE/ieops-aihub/internal/domain"
	"github.com/GMISWE/ieops-aihub/internal/render"
)

// adminUser is a global-admin caller used by tests that need to bypass project
// and visibility checks.
func adminUser() *UserContext {
	return &UserContext{
		UserID:       "u_admin",
		DisplayName:  "Admin",
		Role:         "admin",
		ProjectRoles: map[string]string{},
		APIKeyID:     "k_admin",
	}
}

func authorUser() *UserContext {
	return &UserContext{
		UserID:      "u_author",
		DisplayName: "Author",
		Role:        "writer",
		ProjectRoles: map[string]string{
			"testproj": "writer",
		},
		APIKeyID: "k_author",
	}
}

func otherViewerUser() *UserContext {
	return &UserContext{
		UserID:      "u_other",
		DisplayName: "Other",
		Role:        "writer",
		ProjectRoles: map[string]string{
			"testproj": "viewer",
		},
		APIKeyID: "k_other",
	}
}

// TestArtifactHTML_RouteParamPlain asserts the registered route lets Echo deliver
// the raw memory_id as :id without any `.html` suffix attached. This guards
// against the regression where `/artifacts/:id.html` was originally proposed and
// silently produced ids like "mem_abc.html".
func TestArtifactHTML_RouteParamPlain(t *testing.T) {
	e := echo.New()
	v1 := e.Group("/v1")
	// Use a stub handler that captures the param so the test does not depend on DB.
	var got string
	v1.GET("/artifacts/:id/html", func(c echo.Context) error {
		got = c.Param("id")
		return c.NoContent(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodGet, "/v1/artifacts/mem_abc123/html", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200", rec.Code)
	}
	if got != "mem_abc123" {
		t.Fatalf("route param id: got %q, want %q (suffix must not bleed into :id)", got, "mem_abc123")
	}
}

// TestArtifactHTML_VisibilityPrivate_Forbidden verifies the inline-visibility
// helper rejects a private memory when the caller is not the author.
func TestArtifactHTML_VisibilityPrivate_Forbidden(t *testing.T) {
	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/v1/artifacts/mem_x/html", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	mem := &domain.Memory{
		ID:           "mem_x",
		AuthorUserID: "u_author",
		Visibility:   "private",
		Project:      "testproj",
	}
	if err := checkMemoryVisibility(c, otherViewerUser(), mem); err == nil {
		t.Fatalf("expected error for non-author on private memory")
	}
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status: got %d, want 403", rec.Code)
	}
}

// TestArtifactHTML_VisibilityPrivate_AuthorOK verifies the author of a private
// memory passes the visibility gate.
func TestArtifactHTML_VisibilityPrivate_AuthorOK(t *testing.T) {
	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/v1/artifacts/mem_x/html", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	mem := &domain.Memory{
		ID:           "mem_x",
		AuthorUserID: "u_author",
		Visibility:   "private",
		Project:      "testproj",
	}
	if err := checkMemoryVisibility(c, authorUser(), mem); err != nil {
		t.Fatalf("expected nil for author on private memory, got %v", err)
	}
}

// TestArtifactHTML_VisibilityAdmin_NonAdminForbidden asserts admin-only
// visibility blocks writers.
func TestArtifactHTML_VisibilityAdmin_NonAdminForbidden(t *testing.T) {
	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/v1/artifacts/mem_x/html", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	mem := &domain.Memory{Visibility: "admin", Project: "testproj"}
	if err := checkMemoryVisibility(c, authorUser(), mem); err == nil {
		t.Fatalf("expected forbidden for non-admin on admin-visibility memory")
	}
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status: got %d, want 403", rec.Code)
	}
}

// TestArtifactHTML_VisibilityAdmin_AdminOK asserts admins bypass the
// admin-visibility check.
func TestArtifactHTML_VisibilityAdmin_AdminOK(t *testing.T) {
	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/v1/artifacts/mem_x/html", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	mem := &domain.Memory{Visibility: "admin", Project: "testproj"}
	if err := checkMemoryVisibility(c, adminUser(), mem); err != nil {
		t.Fatalf("admin should bypass visibility=admin, got %v", err)
	}
}

// TestArtifactHTML_400_EmptyID asserts the handler short-circuits before any
// DB read when :id is empty. nil pool would panic if reached.
func TestArtifactHTML_400_EmptyID(t *testing.T) {
	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/v1/artifacts//html", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetParamNames("id")
	c.SetParamValues("")
	setUser(c, adminUser())

	handler := handleArtifactHTML(nil)
	if err := handler(c); err != nil {
		e.HTTPErrorHandler(err, c)
	}
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status: got %d, want 400 (body=%s)", rec.Code, rec.Body.String())
	}
}

// TestArtifactHTML_401_NoUser asserts the handler returns 401 when no user is
// in the context. The visibility helper is the bottleneck used here because
// the DB lookup happens first in the real flow; for an isolated unit test we
// confirm the helper rejects nil users.
func TestArtifactHTML_401_NoUser(t *testing.T) {
	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/v1/artifacts/mem_x/html", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	if err := checkMemoryVisibility(c, nil, &domain.Memory{Visibility: "private"}); err == nil {
		t.Fatalf("expected error for nil user")
	}
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status: got %d, want 401", rec.Code)
	}
}

// TestRenderArtifactBody_FullDocVerbatim verifies aihub#104: a stored value that is
// already a complete HTML document is served verbatim (no double-wrapping), case-
// and leading-whitespace-insensitive.
func TestRenderArtifactBody_FullDocVerbatim(t *testing.T) {
	docs := []string{
		"<!doctype html><html><head></head><body>x</body></html>",
		"<!DOCTYPE HTML>\n<html><body>x</body></html>",
		"  \n\t<html lang=\"en\"><body>x</body></html>",
	}
	for _, doc := range docs {
		if got := renderArtifactBody(doc, "mem_x (methodology.review)", ""); got != doc {
			t.Fatalf("full document must be served verbatim;\n got: %q\nwant: %q", got, doc)
		}
	}
}

// TestRenderArtifactBody_FragmentWrapped verifies a body fragment (goldmark
// auto-render path) is wrapped into a standalone document containing the fragment.
func TestRenderArtifactBody_FragmentWrapped(t *testing.T) {
	frag := "<h1>Hello</h1>\n<p>a fragment</p>"
	got := renderArtifactBody(frag, "My Title", "")
	if got == frag {
		t.Fatalf("fragment should be wrapped, got served verbatim")
	}
	if !strings.Contains(got, frag) {
		t.Fatalf("wrapped output should embed the fragment; got %q", got)
	}
	lc := strings.ToLower(got)
	if !strings.Contains(lc, "<html") && !strings.Contains(lc, "<!doctype") {
		t.Fatalf("wrapped output should be a full document; got %q", got)
	}
}

// strptr is a tiny helper for building *string test inputs.
func strptr(s string) *string { return &s }

// TestArtifactBackHref covers the route-aware back-link logic that decides
// whether the standalone artifact document gets a "Back to work item" nav:
//   - /ui route + a work item  -> nav to the path-escaped wi detail URL
//   - /v1 route                -> never a nav (pure content document)
//   - work item == nil         -> never a nav, and must not panic
func TestArtifactBackHref(t *testing.T) {
	cases := []struct {
		name       string
		routePath  string
		workItemID *string
		want       string
	}{
		{"ui_with_wi", "/ui/artifacts/:id/html", strptr("aihub#98"), "/ui/wi/aihub%2398"},
		{"ui_plain_wi", "/ui/artifacts/:id/html", strptr("wi_abc123"), "/ui/wi/wi_abc123"},
		{"v1_with_wi", "/v1/artifacts/:id/html", strptr("aihub#98"), ""},
		{"ui_nil_wi", "/ui/artifacts/:id/html", nil, ""},
		{"v1_nil_wi", "/v1/artifacts/:id/html", nil, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := artifactBackHref(tc.routePath, tc.workItemID)
			if got != tc.want {
				t.Errorf("artifactBackHref(%q, %v) = %q; want %q", tc.routePath, tc.workItemID, got, tc.want)
			}
			if strings.Contains(got, "#") {
				t.Errorf("back href %q contains a raw '#' — browser would strip it as a URL fragment", got)
			}
		})
	}
}

// TestArtifactBackHref_RendersIntoDocument bridges the back-link helper to the
// final standalone document so the two stay consistent: the /ui route yields a
// nav linking to the wi, while the /v1 route yields no rendered nav element.
// (The full handler needs a DB pool for GetMemoryByID, so the DB path is
// covered by checkMemoryVisibility tests above; here we exercise the render
// seam directly.)
func TestArtifactBackHref_RendersIntoDocument(t *testing.T) {
	wiID := "aihub#98"

	uiHref := artifactBackHref("/ui/artifacts/:id/html", &wiID)
	uiDoc := render.Document("<p>spec</p>", "mem (methodology.spec)", uiHref)
	if !strings.Contains(uiDoc, `<nav class="pf-doc-nav">`) {
		t.Errorf("/ui document missing rendered pf-doc-nav element")
	}
	if !strings.Contains(uiDoc, "/ui/wi/aihub%2398") {
		t.Errorf("/ui document missing path-escaped wi back-link; got: %s", uiDoc)
	}

	v1Href := artifactBackHref("/v1/artifacts/:id/html", &wiID)
	v1Doc := render.Document("<p>spec</p>", "mem (methodology.spec)", v1Href)
	if strings.Contains(v1Doc, `<nav class="pf-doc-nav">`) {
		t.Errorf("/v1 document must not render a pf-doc-nav element")
	}
}

// ─── aihub#96: public artifact share (/share/:id) ──────────────────────────────
//
// These mirror the no-DB strategy used elsewhere in the package: swap the
// loadMemoryFn / setMemoryVisibilityFn seams so the handlers run their full
// logic against fixture memories. The share endpoint takes NO auth.

// withSetVisibilityOverride swaps setMemoryVisibilityFn for a test, capturing the
// last (id, visibility) pair so callers can assert the mutation that would persist.
func withSetVisibilityOverride(aerr *domain.AihubError) (gotID, gotVis *string, cleanup func()) {
	prev := setMemoryVisibilityFn
	var id, vis string
	setMemoryVisibilityFn = func(_ context.Context, _ *pgxpool.Pool, memID, visibility string) *domain.AihubError {
		id, vis = memID, visibility
		return aerr
	}
	return &id, &vis, func() { setMemoryVisibilityFn = prev }
}

func htmlPtr(s string) *string { return &s }

// publicSharedMem is a methodology.spec artifact that has been made public and
// has a non-null rendered_html fragment.
func publicSharedMem() *domain.Memory {
	return &domain.Memory{
		ID:           "mem_share1",
		Project:      "testproj",
		Type:         "methodology.spec",
		Visibility:   "public",
		AuthorUserID: "u_author",
		RenderedHTML: htmlPtr("<h1>SPEC-BODY-MARKER</h1>"),
	}
}

// Scenario 1: public artifact + rendered_html non-null →
// GET /share/:id returns 200, text/html, body contains the fragment, NO auth set.
func TestSharedArtifact_Public_200(t *testing.T) {
	defer withLoadMemoryOverride(publicSharedMem(), nil)()

	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/share/mem_share1", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetParamNames("id")
	c.SetParamValues("mem_share1")
	// Deliberately NO setUser — public bypasses auth.

	if err := handleSharedArtifact(nil)(c); err != nil {
		e.HTTPErrorHandler(err, c)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get(echo.HeaderContentType); !strings.Contains(ct, "text/html") {
		t.Fatalf("content-type: got %q, want text/html", ct)
	}
	if !strings.Contains(rec.Body.String(), "SPEC-BODY-MARKER") {
		t.Fatalf("body does not contain the rendered fragment: %s", rec.Body.String())
	}
	// XSS hardening: the anonymous path must ship a strict CSP + nosniff so a
	// <script> embedded in a malicious artifact cannot execute on our origin.
	if csp := rec.Header().Get("Content-Security-Policy"); !strings.Contains(csp, "default-src 'none'") {
		t.Fatalf("public share must send a strict CSP, got %q", csp)
	}
	if rec.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("public share must send X-Content-Type-Options: nosniff")
	}
}

// Scenario 2: after unshare (visibility back to project) →
// GET /share/:id returns 404. We run the real unshare handler to flip the
// fixture's visibility, then re-load it through /share/:id.
func TestSharedArtifact_AfterUnshare_404(t *testing.T) {
	mem := publicSharedMem()
	defer withLoadMemoryOverride(mem, nil)()
	gotID, gotVis, cleanup := withSetVisibilityOverride(nil)
	defer cleanup()

	// Unshare as the writer/author.
	e := echo.New()
	ureq := httptest.NewRequest(http.MethodDelete, "/v1/artifacts/mem_share1/share", nil)
	urec := httptest.NewRecorder()
	uc := e.NewContext(ureq, urec)
	uc.SetParamNames("id")
	uc.SetParamValues("mem_share1")
	setUser(uc, authorUser())
	if err := handleUnshareArtifact(nil)(uc); err != nil {
		e.HTTPErrorHandler(err, uc)
	}
	if urec.Code != http.StatusOK {
		t.Fatalf("unshare status: got %d, want 200 (body=%s)", urec.Code, urec.Body.String())
	}
	if *gotID != "mem_share1" || *gotVis != "project" {
		t.Fatalf("unshare mutation: got (%q,%q), want (mem_share1,project)", *gotID, *gotVis)
	}

	// Simulate the persisted state and hit /share/:id again → 404.
	mem.Visibility = "project"
	sreq := httptest.NewRequest(http.MethodGet, "/share/mem_share1", nil)
	srec := httptest.NewRecorder()
	sc := e.NewContext(sreq, srec)
	sc.SetParamNames("id")
	sc.SetParamValues("mem_share1")
	if err := handleSharedArtifact(nil)(sc); err != nil {
		e.HTTPErrorHandler(err, sc)
	}
	if srec.Code != http.StatusNotFound {
		t.Fatalf("post-unshare status: got %d, want 404 (body=%s)", srec.Code, srec.Body.String())
	}
}

// Scenario 3: non-public artifact (visibility=project) → GET /share/:id returns 404.
func TestSharedArtifact_NonPublic_404(t *testing.T) {
	mem := publicSharedMem()
	mem.Visibility = "project"
	defer withLoadMemoryOverride(mem, nil)()

	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/share/mem_share1", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetParamNames("id")
	c.SetParamValues("mem_share1")
	if err := handleSharedArtifact(nil)(c); err != nil {
		e.HTTPErrorHandler(err, c)
	}
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status: got %d, want 404 (body=%s)", rec.Code, rec.Body.String())
	}
}

// Scenario 4: POST /v1/artifacts/:id/share on an artifact with rendered_html=NULL → 412.
func TestShareArtifact_NoRenderedHTML_412(t *testing.T) {
	mem := publicSharedMem()
	mem.Visibility = "project"
	mem.RenderedHTML = nil // not a spec/plan → nothing to share
	defer withLoadMemoryOverride(mem, nil)()

	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/v1/artifacts/mem_share1/share", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetParamNames("id")
	c.SetParamValues("mem_share1")
	setUser(c, authorUser()) // writer on testproj

	if err := handleShareArtifact(nil)(c); err != nil {
		e.HTTPErrorHandler(err, c)
	}
	if rec.Code != http.StatusPreconditionFailed {
		t.Fatalf("status: got %d, want 412 (body=%s)", rec.Code, rec.Body.String())
	}
}

// Scenario 5: POST /v1/artifacts/:id/share as a viewer (not writer) → 403.
func TestShareArtifact_Viewer_403(t *testing.T) {
	mem := publicSharedMem()
	mem.Visibility = "project"
	defer withLoadMemoryOverride(mem, nil)()

	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/v1/artifacts/mem_share1/share", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetParamNames("id")
	c.SetParamValues("mem_share1")
	setUser(c, otherViewerUser()) // viewer on testproj

	if err := handleShareArtifact(nil)(c); err != nil {
		e.HTTPErrorHandler(err, c)
	}
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status: got %d, want 403 (body=%s)", rec.Code, rec.Body.String())
	}
}

// Scenario 6: a caller who is NOT a project member (anonymous here, the strongest
// case) hits GET /share/:id for a public artifact → 200. Public bypasses the
// project access check entirely (handleSharedArtifact never calls checkProjectAccess).
func TestSharedArtifact_NonMember_200(t *testing.T) {
	defer withLoadMemoryOverride(publicSharedMem(), nil)()

	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/share/mem_share1", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetParamNames("id")
	c.SetParamValues("mem_share1")
	// No user at all: anonymous, definitionally not a project member.

	if err := handleSharedArtifact(nil)(c); err != nil {
		e.HTTPErrorHandler(err, c)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "SPEC-BODY-MARKER") {
		t.Fatalf("body does not contain the rendered fragment: %s", rec.Body.String())
	}
}

// Scenario 7: a writer shares a spec/plan artifact that has rendered_html →
// 200, visibility flipped to public, and the response carries the share_url.
// Covers the success path of POST /v1/artifacts/:id/share.
func TestShareArtifact_Writer_200(t *testing.T) {
	mem := publicSharedMem()
	mem.Visibility = "project" // not yet shared
	defer withLoadMemoryOverride(mem, nil)()
	gotID, gotVis, cleanup := withSetVisibilityOverride(nil)
	defer cleanup()

	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/v1/artifacts/mem_share1/share", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetParamNames("id")
	c.SetParamValues("mem_share1")
	setUser(c, authorUser()) // writer on testproj

	if err := handleShareArtifact(nil)(c); err != nil {
		e.HTTPErrorHandler(err, c)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}
	if *gotID != "mem_share1" || *gotVis != "public" {
		t.Fatalf("share mutation: got (%q,%q), want (mem_share1,public)", *gotID, *gotVis)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "share_url") || !strings.Contains(body, "/share/mem_share1") {
		t.Fatalf("response missing share_url for the artifact: %s", body)
	}
}
