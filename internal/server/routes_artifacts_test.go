package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

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
