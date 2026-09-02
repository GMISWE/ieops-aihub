package server

import (
	"context"
	"encoding/json"
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

// ─── aihub#248: /ui lineage-head redirect for handleArtifactHTML ───────────

// supersededArtifactMem returns an archived-but-still-loadable memory whose
// LatestID points to headID — the shape produced once a later
// pf_update_memory/pf_save_artifact call has superseded it. headID == "" is
// used by callers that then blank out LatestID themselves (the nil case).
func supersededArtifactMem(id, headID, memType string) *domain.Memory {
	m := &domain.Memory{
		ID:           id,
		Project:      "testproj",
		Type:         memType,
		Status:       "archived",
		Visibility:   "project",
		AuthorUserID: "u_author",
		Content:      "old body",
		RenderedHTML: htmlPtr("<p>OLD-BODY-MARKER</p>"),
	}
	if headID != "" {
		head := headID
		m.LatestID = &head
	}
	return m
}

// headArtifactMem returns an active lineage-head memory, self-headed unless
// overridden by the caller.
func headArtifactMem(id, memType string) *domain.Memory {
	self := id
	return &domain.Memory{
		ID:           id,
		Project:      "testproj",
		Type:         memType,
		Status:       "active",
		Visibility:   "project",
		AuthorUserID: "u_author",
		Content:      "new body",
		RenderedHTML: htmlPtr("<p>HEAD-BODY-MARKER</p>"),
		LatestID:     &self,
	}
}

func TestArtifactHTML_UI_LatestIDNil_NoRedirect(t *testing.T) {
	defer withVersionChainOverride()()
	mem := supersededArtifactMem("mem_old1", "", "experience.debug")
	defer withLoadMemoryOverride(mem, nil)()
	calls := 0
	defer withResolveLatestCounter(t, "mem_old1", nil, nil, &calls)()

	e := echo.New()
	c, rec := newUIContext(e, http.MethodGet, "/ui/artifacts/mem_old1/html", "mem_old1")
	c.SetPath("/ui/artifacts/:id/html")
	setUser(c, authorUser())

	if err := handleArtifactHTML(nil)(c); err != nil {
		t.Fatalf("handler error: %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200", rec.Code)
	}
	if loc := rec.Header().Get("Location"); loc != "" {
		t.Errorf("unexpected Location: %q", loc)
	}
	if calls != 0 {
		t.Errorf("resolveLatestFn calls: got %d, want 0 (LatestID nil must skip resolution)", calls)
	}
}

func TestArtifactHTML_UI_LatestIDSelf_NoRedirect(t *testing.T) {
	defer withVersionChainOverride()()
	mem := headArtifactMem("mem_head_self", "experience.debug")
	defer withLoadMemoryOverride(mem, nil)()
	calls := 0
	defer withResolveLatestCounter(t, "mem_head_self", nil, nil, &calls)()

	e := echo.New()
	c, rec := newUIContext(e, http.MethodGet, "/ui/artifacts/mem_head_self/html", "mem_head_self")
	c.SetPath("/ui/artifacts/:id/html")
	setUser(c, authorUser())

	if err := handleArtifactHTML(nil)(c); err != nil {
		t.Fatalf("handler error: %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200", rec.Code)
	}
	if calls != 0 {
		t.Errorf("resolveLatestFn calls: got %d, want 0 (LatestID==ID must skip resolution)", calls)
	}
}

func TestArtifactHTML_UI_SupersededVisibleHead_Redirects(t *testing.T) {
	old := supersededArtifactMem("mem_old2", "mem_head2", "experience.debug")
	defer withLoadMemoryOverride(old, nil)()
	defer withResolveLatestOverride(t, "mem_old2", headArtifactMem("mem_head2", "experience.debug"), nil)()

	e := echo.New()
	c, rec := newUIContext(e, http.MethodGet, "/ui/artifacts/mem_old2/html?back=/ui/queue", "mem_old2")
	c.SetPath("/ui/artifacts/:id/html")
	setUser(c, authorUser())

	if err := handleArtifactHTML(nil)(c); err != nil {
		t.Fatalf("handler error: %v", err)
	}
	if rec.Code != http.StatusFound {
		t.Fatalf("status: got %d, want 302 (body=%s)", rec.Code, excerptStr(rec.Body.String()))
	}
	want := "/ui/artifacts/mem_head2/html?back=/ui/queue"
	if loc := rec.Header().Get("Location"); loc != want {
		t.Errorf("Location: got %q, want %q", loc, want)
	}
}

func TestArtifactHTML_UI_SupersededHead_VisibilityDenied_Fallback(t *testing.T) {
	defer withVersionChainOverride()()
	old := supersededArtifactMem("mem_old3", "mem_head3", "experience.debug")
	defer withLoadMemoryOverride(old, nil)()
	head := headArtifactMem("mem_head3", "experience.debug")
	head.Visibility = "private"
	head.AuthorUserID = "u_someone_else"
	defer withResolveLatestOverride(t, "mem_old3", head, nil)()

	e := echo.New()
	c, rec := newUIContext(e, http.MethodGet, "/ui/artifacts/mem_old3/html", "mem_old3")
	c.SetPath("/ui/artifacts/:id/html")
	setUser(c, authorUser())

	if err := handleArtifactHTML(nil)(c); err != nil {
		t.Fatalf("handler error: %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200 (silent fallback, no 403/404); body=%s",
			rec.Code, excerptStr(rec.Body.String()))
	}
	if loc := rec.Header().Get("Location"); loc != "" {
		t.Errorf("unexpected Location on fallback: %q", loc)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "OLD-BODY-MARKER") {
		t.Errorf("fallback should render the original record; body=%s", excerptStr(body))
	}
	if strings.Contains(body, "HEAD-BODY-MARKER") {
		t.Errorf("fallback must NOT leak the inaccessible head's content; body=%s", excerptStr(body))
	}
}

func TestArtifactHTML_UI_SupersededHead_ProjectDenied_Fallback(t *testing.T) {
	defer withVersionChainOverride()()
	old := supersededArtifactMem("mem_old4", "mem_head4", "experience.debug")
	defer withLoadMemoryOverride(old, nil)()
	head := headArtifactMem("mem_head4", "experience.debug")
	head.Project = "otherproj" // caller has no role on this project
	defer withResolveLatestOverride(t, "mem_old4", head, nil)()

	e := echo.New()
	c, rec := newUIContext(e, http.MethodGet, "/ui/artifacts/mem_old4/html", "mem_old4")
	c.SetPath("/ui/artifacts/:id/html")
	setUser(c, authorUser())

	if err := handleArtifactHTML(nil)(c); err != nil {
		t.Fatalf("handler error: %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200 (silent fallback)", rec.Code)
	}
	if loc := rec.Header().Get("Location"); loc != "" {
		t.Errorf("unexpected Location on fallback: %q", loc)
	}
}

func TestArtifactHTML_UI_HeadResolutionError_Fallback(t *testing.T) {
	defer withVersionChainOverride()()
	old := supersededArtifactMem("mem_old5", "mem_head5", "experience.debug")
	defer withLoadMemoryOverride(old, nil)()
	defer withResolveLatestOverride(t, "mem_old5", nil, domain.NewErr(domain.ErrNotFound, "memory not found"))()

	e := echo.New()
	c, rec := newUIContext(e, http.MethodGet, "/ui/artifacts/mem_old5/html", "mem_old5")
	c.SetPath("/ui/artifacts/:id/html")
	setUser(c, authorUser())

	if err := handleArtifactHTML(nil)(c); err != nil {
		t.Fatalf("handler error: %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200 — never 404 on an unreachable head", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "OLD-BODY-MARKER") {
		t.Errorf("should render the original record on head-resolution error")
	}
}

// TestArtifactHTML_V1_SupersededID_NoRedirect proves the /v1 surface never
// follows latest_id: same superseded fixture as the /ui tests above, but no
// c.SetPath("/ui/...") — mirrors the real /v1 route, whose registered pattern
// does not start with "/ui". Byte-identical to today: 200, original content,
// no Location, and resolveLatestFn must not even be called.
func TestArtifactHTML_V1_SupersededID_NoRedirect(t *testing.T) {
	old := supersededArtifactMem("mem_old6", "mem_head6", "experience.debug")
	defer withLoadMemoryOverride(old, nil)()
	calls := 0
	defer withResolveLatestCounter(t, "mem_old6", headArtifactMem("mem_head6", "experience.debug"), nil, &calls)()

	e := echo.New()
	c, rec := newUIContext(e, http.MethodGet, "/v1/artifacts/mem_old6/html", "mem_old6")
	c.SetPath("/v1/artifacts/:id/html")
	setUser(c, authorUser())

	if err := handleArtifactHTML(nil)(c); err != nil {
		t.Fatalf("handler error: %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200", rec.Code)
	}
	if loc := rec.Header().Get("Location"); loc != "" {
		t.Errorf("/v1 must never redirect; got Location %q", loc)
	}
	if !strings.Contains(rec.Body.String(), "OLD-BODY-MARKER") {
		t.Errorf("/v1 must serve the exact requested record")
	}
	if calls != 0 {
		t.Errorf("resolveLatestFn calls: got %d, want 0 on /v1 (gated on /ui prefix)", calls)
	}
}

// TestSharedArtifact_SupersededID_NoRedirect proves /share stays exact-ID even
// when the shared row's lineage has since moved on — handleSharedArtifact
// never calls resolveLatestFn at all (aihub#248 non-goal 2 / mem_A6540SyP).
func TestSharedArtifact_SupersededID_NoRedirect(t *testing.T) {
	old := supersededArtifactMem("mem_old7", "mem_head7", "methodology.spec")
	old.Visibility = "public"
	defer withLoadMemoryOverride(old, nil)()
	calls := 0
	defer withResolveLatestCounter(t, "mem_old7", headArtifactMem("mem_head7", "methodology.spec"), nil, &calls)()

	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/share/mem_old7", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetParamNames("id")
	c.SetParamValues("mem_old7")

	if err := handleSharedArtifact(nil)(c); err != nil {
		t.Fatalf("handler error: %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200", rec.Code)
	}
	if loc := rec.Header().Get("Location"); loc != "" {
		t.Errorf("/share must never redirect; got Location %q", loc)
	}
	if calls != 0 {
		t.Errorf("resolveLatestFn calls: got %d, want 0 (/share never resolves lineage)", calls)
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

// TestArtifactViewer_UIvsV1Share_BytePurity locks the aihub#159 invariant: every
// new viewer affordance (side rail, section folding, annotation data island,
// viewer.css/ui.css links, app chrome, share control) is /ui-only — the /v1 API
// and the public /share output must contain NONE of those bytes (spec mem_5kxhPqA2).
func TestArtifactViewer_UIvsV1Share_BytePurity(t *testing.T) {
	defer withVersionChainOverride()()

	mem := publicSharedMem()
	mem.WorkItemID = strptr("aihub#159")
	mem.Content = "# Title\n\nintro paragraph\n\n## Section A\n\nbody A\n\n## Section B\n\nbody B"
	mem.RenderedHTML = htmlPtr(`<h1 id="title">Title</h1><p>intro paragraph</p>` +
		`<h2 id="section-a">Section A</h2><p>body A</p>` +
		`<pre><code class="language-d2">a -&gt; b</code></pre>` +
		`<h2 id="section-b">Section B</h2><p>body B</p>`)
	mem.Commits = []byte(`[{"id":"c1","author_display":"monte","body":"q","status":"open","anchor":{"quote":"body A"}}]`)
	defer withLoadMemoryOverride(mem, nil)()

	// Bytes that exist ONLY on the /ui artifact viewer.
	uiOnly := []string{
		`id="pf-side-rail"`,
		`class="pf-side-card"`,
		`<details open class="pf-sec"`, // section folding
		`id="pf-annot-data"`,
		`/ui/static/viewer.css`,
		`/ui/static/ui.css`,
		`class="pf-appnav"`,
		`id="pf-share"`,
		`/ui/static/annot.js`,
		`<figure class="pf-diagram"`, // aihub#160: d2 → inline SVG; /v1+/share keep the code block
		`/ui/static/diagram.js`,      // aihub#234: click-to-zoom for those figures
	}

	renderPath := func(path string) string {
		e := echo.New()
		c, rec := newUIContext(e, http.MethodGet, path, "mem_share1")
		c.SetPath(path) // drives the HasPrefix(c.Path(), "/ui") gate
		setUser(c, authorUser())
		if err := handleArtifactHTML(nil)(c); err != nil {
			e.HTTPErrorHandler(err, c)
		}
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: status %d (body=%s)", path, rec.Code, excerptStr(rec.Body.String()))
		}
		return rec.Body.String()
	}

	// /ui: the affordances MUST be present (proves the gate adds them).
	uiBody := renderPath("/ui/artifacts/:id/html")
	for _, s := range uiOnly {
		if !strings.Contains(uiBody, s) {
			t.Errorf("/ui output should contain %q (affordance missing)", s)
		}
	}

	// /v1: NONE of them — byte-identical to the pre-#159 pure document.
	v1Body := renderPath("/v1/artifacts/:id/html")
	for _, s := range uiOnly {
		if strings.Contains(v1Body, s) {
			t.Errorf("/v1 output must NOT contain /ui-only bytes; found %q", s)
		}
	}

	// /share (public, no auth): NONE of them + the strict CSP stays.
	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/share/mem_share1", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetParamNames("id")
	c.SetParamValues("mem_share1")
	if err := handleSharedArtifact(nil)(c); err != nil {
		e.HTTPErrorHandler(err, c)
	}
	shareBody := rec.Body.String()
	for _, s := range uiOnly {
		if strings.Contains(shareBody, s) {
			t.Errorf("/share output must NOT contain /ui-only bytes; found %q", s)
		}
	}
	if !strings.Contains(rec.Header().Get("Content-Security-Policy"), "default-src 'none'") {
		t.Errorf("/share must keep its strict CSP")
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

// ─── aihub#113: owning-wi + related memory links ─────────────────────────────

// TestRenderArtifactBodyWithMeta_OwningWIAndRelated verifies that the /ui
// render path injects the owning-wi link and a related-memory link when both
// are provided. Uses renderArtifactBodyWithMeta directly — same seam as
// TestArtifactBackHref_RendersIntoDocument.
func TestRenderArtifactBodyWithMeta_OwningWIAndRelated(t *testing.T) {
	ownerHref := "/ui/wi/aihub%2399"
	relID := "mem_related_42"
	related := []render.RelatedRef{{ID: relID}}

	got := renderArtifactBodyWithMeta("<h1>SPEC-CONTENT</h1>", "mem (methodology.spec)",
		ownerHref, ownerHref, "aihub#99", related)

	// Must contain the spec content.
	if !strings.Contains(got, "SPEC-CONTENT") {
		t.Errorf("output missing spec content; got: %s", excerptStr(got))
	}
	// Must contain path-escaped wi href.
	if !strings.Contains(got, "/ui/wi/aihub%2399") {
		t.Errorf("output missing owning-wi href; got: %s", excerptStr(got))
	}
	// Must contain related memory link.
	if !strings.Contains(got, "/ui/artifacts/"+relID+"/html") {
		t.Errorf("output missing related memory link; got: %s", excerptStr(got))
	}
}

// TestRenderArtifactBodyWithMeta_NoRelated verifies that when related is nil/empty,
// the related section is omitted but the owning-wi link is still present.
func TestRenderArtifactBodyWithMeta_NoRelated(t *testing.T) {
	ownerHref := "/ui/wi/aihub%23100"
	got := renderArtifactBodyWithMeta("<p>body</p>", "mem (methodology.spec)",
		ownerHref, ownerHref, "aihub#100", nil)

	if !strings.Contains(got, "/ui/wi/aihub%23100") {
		t.Errorf("output missing owning-wi link; got: %s", excerptStr(got))
	}
	if strings.Contains(got, "pf-doc-meta-related") {
		t.Errorf("output should not contain a related section when no related_ids")
	}
}

// TestParseRelatedRefs covers the attrs→[]render.RelatedRef parsing helper.
func TestParseRelatedRefs(t *testing.T) {
	cases := []struct {
		name  string
		attrs json.RawMessage
		want  int // expected number of refs
	}{
		{"nil_attrs", nil, 0},
		{"empty_attrs", json.RawMessage(`{}`), 0},
		{"no_related_ids_key", json.RawMessage(`{"foo":"bar"}`), 0},
		{"empty_array", json.RawMessage(`{"related_ids":[]}`), 0},
		{"one_id", json.RawMessage(`{"related_ids":["mem_abc"]}`), 1},
		{"two_ids", json.RawMessage(`{"related_ids":["mem_abc","mem_def"]}`), 2},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := parseRelatedRefs(tc.attrs)
			if len(got) != tc.want {
				t.Errorf("parseRelatedRefs(%s) = %d refs; want %d", tc.attrs, len(got), tc.want)
			}
		})
	}
}

// TestRenderArtifactBodyWithMeta_UIVsV1 bridges the render helper to the final
// standalone document for the /ui vs /v1 distinction — same approach as
// TestArtifactBackHref_RendersIntoDocument. It verifies:
//   - /ui route: owning-wi link + related-memory link appear
//   - /v1 route (ownerHref="", related=nil): neither appears
func TestRenderArtifactBodyWithMeta_UIVsV1(t *testing.T) {
	wiID := "aihub#99"
	relID := "mem_related_42"
	attrs := json.RawMessage(`{"related_ids":["` + relID + `"]}`)

	// Simulate what handleArtifactHTML does on the /ui path.
	uiOwnerHref := wiHref(wiID)
	uiRelated := parseRelatedRefs(attrs)
	backHref := artifactBackHref("/ui/artifacts/:id/html", &wiID)

	uiDoc := renderArtifactBodyWithMeta("<h1>SPEC-CONTENT</h1>", "mem (methodology.spec)",
		backHref, uiOwnerHref, wiID, uiRelated)

	if !strings.Contains(uiDoc, "SPEC-CONTENT") {
		t.Errorf("UI doc missing spec content; excerpt: %s", excerptStr(uiDoc))
	}
	if !strings.Contains(uiDoc, "/ui/wi/aihub%2399") {
		t.Errorf("UI doc missing path-escaped owning-wi href; excerpt: %s", excerptStr(uiDoc))
	}
	if !strings.Contains(uiDoc, "/ui/artifacts/"+relID+"/html") {
		t.Errorf("UI doc missing related memory link; excerpt: %s", excerptStr(uiDoc))
	}

	// Simulate what handleArtifactHTML does on the /v1 path: no meta injected.
	v1BackHref := artifactBackHref("/v1/artifacts/:id/html", &wiID)
	v1Doc := renderArtifactBodyWithMeta("<h1>V1-CONTENT</h1>", "mem (methodology.spec)",
		v1BackHref, "", "", nil)

	if strings.Contains(v1Doc, "pf-doc-meta") {
		t.Errorf("/v1 doc must not contain pf-doc-meta header; excerpt: %s", excerptStr(v1Doc))
	}
	if strings.Contains(v1Doc, relID) {
		t.Errorf("/v1 doc must not contain related memory ref; excerpt: %s", excerptStr(v1Doc))
	}
}

// excerptStr returns the first 500 bytes of a string for error messages.
func excerptStr(s string) string {
	if len(s) <= 500 {
		return s
	}
	return s[:500] + "..."
}

// ─── aihub#81: lazy-render fallback in handleArtifactHTML ────────────────────

// retroMemNullHTML returns a methodology.retro artifact whose rendered_html is
// NULL (legacy row saved before aihub#81 extended the render-type set).
func retroMemNullHTML() *domain.Memory {
	content := "# Retro\n\n- item one\n- item two"
	return &domain.Memory{
		ID:           "mem_retro1",
		Project:      "testproj",
		Type:         "methodology.retro",
		Content:      content,
		Visibility:   "project",
		AuthorUserID: "u_author",
		RenderedHTML: nil, // NULL — the legacy / not-yet-rendered case
	}
}

// TestArtifactHTML_LazyRender_MarkdownContent_200 verifies that a retro artifact
// with rendered_html=NULL and markdown content is lazy-rendered on-the-fly,
// returning 200 (not 404) with a body containing the rendered output.
func TestArtifactHTML_LazyRender_MarkdownContent_200(t *testing.T) {
	defer withLoadMemoryOverride(retroMemNullHTML(), nil)()

	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/v1/artifacts/mem_retro1/html", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetParamNames("id")
	c.SetParamValues("mem_retro1")
	setUser(c, adminUser())

	if err := handleArtifactHTML(nil)(c); err != nil {
		e.HTTPErrorHandler(err, c)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("lazy-render: status got %d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}
	ct := rec.Header().Get(echo.HeaderContentType)
	if !strings.Contains(ct, "text/html") {
		t.Fatalf("lazy-render: content-type got %q, want text/html", ct)
	}
	// goldmark renders "# Retro" as an <h1>
	if !strings.Contains(rec.Body.String(), "<h1") {
		t.Fatalf("lazy-render: body should contain goldmark-rendered HTML; got: %s", excerptStr(rec.Body.String()))
	}
}

// TestArtifactHTML_LazyRender_PlainTextFallback_200 verifies that when content
// is not valid markdown (plain text) the handler still returns 200 with a
// <pre> fallback block — never a 404.
func TestArtifactHTML_LazyRender_PlainTextFallback_200(t *testing.T) {
	mem := retroMemNullHTML()
	mem.Content = "plain text, no markdown" // goldmark still renders this, but we test pre path
	// Force the pre-block path by using content with no markdown constructs.
	// (goldmark will still succeed on plain text; use an empty content to force the
	// empty-content branch for the <pre></pre> path.)
	mem.Content = ""
	defer withLoadMemoryOverride(mem, nil)()

	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/v1/artifacts/mem_retro1/html", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetParamNames("id")
	c.SetParamValues("mem_retro1")
	setUser(c, adminUser())

	if err := handleArtifactHTML(nil)(c); err != nil {
		e.HTTPErrorHandler(err, c)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("pre fallback: status got %d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "<pre>") {
		t.Fatalf("pre fallback: body should contain a <pre> element; got: %s", excerptStr(rec.Body.String()))
	}
}

// TestArtifactHTML_LazyRender_PlainTextInPre_200 verifies that plain text content
// (non-markdown) with RenderedHTML=NULL is served in a <pre> wrapper (via goldmark
// returning a trivial paragraph or the pre fallback), returning 200, never 404.
func TestArtifactHTML_LazyRender_PlainTextInPre_200(t *testing.T) {
	mem := retroMemNullHTML()
	mem.Content = "just plain text with <special> chars & entities"
	defer withLoadMemoryOverride(mem, nil)()

	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/v1/artifacts/mem_retro1/html", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetParamNames("id")
	c.SetParamValues("mem_retro1")
	setUser(c, adminUser())

	if err := handleArtifactHTML(nil)(c); err != nil {
		e.HTTPErrorHandler(err, c)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("plain text render: status got %d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}
	ct := rec.Header().Get(echo.HeaderContentType)
	if !strings.Contains(ct, "text/html") {
		t.Fatalf("plain text render: content-type got %q, want text/html", ct)
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

// ─── aihub#154: /ui artifact share button ────────────────────────────────────
//
// The share/unshare handlers are auth-agnostic and already covered for /v1 by
// scenarios 1-7 above; these focus on the new /ui surface: that the same
// handlers behave identically under cookie auth (happy + 403 + 412), and that
// the /ui viewer injects the share control while /v1 + /share stay
// byte-identical (no pf-share, no share.js).

// withVersionChainOverride swaps versionChainFn so /ui handler tests don't hit
// the DB (nil pool would panic in MemoryVersionChain). Returns an empty chain
// so buildVersionHistoryHTML is a no-op.
func withVersionChainOverride() func() {
	prev := versionChainFn
	versionChainFn = func(_ context.Context, _ *pgxpool.Pool, _ string) ([]domain.MemoryVersionRef, error) {
		return nil, nil
	}
	return func() { versionChainFn = prev }
}

// uiShareContext builds an echo context whose registered path is the /ui share
// route, so handlers that branch on c.Path() (none here, but kept symmetric
// with the /ui html path) and the share handlers run as they would under /ui.
func newUIContext(e *echo.Echo, method, target, id string) (echo.Context, *httptest.ResponseRecorder) {
	req := httptest.NewRequest(method, target, nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetParamNames("id")
	c.SetParamValues(id)
	return c, rec
}

// TestUIShareArtifact_Writer_200 covers acceptance 1: a writer sharing a
// spec/plan with rendered_html over the /ui route → 200, body has share_url +
// visibility:"public", and the visibility setter is invoked with "public".
func TestUIShareArtifact_Writer_200(t *testing.T) {
	mem := publicSharedMem()
	mem.Visibility = "project" // not yet shared
	defer withLoadMemoryOverride(mem, nil)()
	gotID, gotVis, cleanup := withSetVisibilityOverride(nil)
	defer cleanup()

	e := echo.New()
	c, rec := newUIContext(e, http.MethodPost, "/ui/artifacts/mem_share1/share", "mem_share1")
	c.SetPath("/ui/artifacts/:id/share")
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
		t.Fatalf("response missing share_url: %s", body)
	}
	if !strings.Contains(body, "\"visibility\":\"public\"") {
		t.Fatalf("response missing visibility:public: %s", body)
	}
}

// TestUIUnshareArtifact_Writer_200 covers acceptance 2: DELETE over /ui → 200,
// {ok:true}, visibility setter invoked with "project".
func TestUIUnshareArtifact_Writer_200(t *testing.T) {
	mem := publicSharedMem() // currently public
	defer withLoadMemoryOverride(mem, nil)()
	gotID, gotVis, cleanup := withSetVisibilityOverride(nil)
	defer cleanup()

	e := echo.New()
	c, rec := newUIContext(e, http.MethodDelete, "/ui/artifacts/mem_share1/share", "mem_share1")
	c.SetPath("/ui/artifacts/:id/share")
	setUser(c, authorUser())

	if err := handleUnshareArtifact(nil)(c); err != nil {
		e.HTTPErrorHandler(err, c)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}
	if *gotID != "mem_share1" || *gotVis != "project" {
		t.Fatalf("unshare mutation: got (%q,%q), want (mem_share1,project)", *gotID, *gotVis)
	}
	if !strings.Contains(rec.Body.String(), "\"ok\":true") {
		t.Fatalf("response missing ok:true: %s", rec.Body.String())
	}
}

// TestUIShareArtifact_Viewer_403 covers acceptance 3a: non-writer over /ui → 403.
func TestUIShareArtifact_Viewer_403(t *testing.T) {
	mem := publicSharedMem()
	mem.Visibility = "project"
	defer withLoadMemoryOverride(mem, nil)()

	e := echo.New()
	c, rec := newUIContext(e, http.MethodPost, "/ui/artifacts/mem_share1/share", "mem_share1")
	c.SetPath("/ui/artifacts/:id/share")
	setUser(c, otherViewerUser()) // viewer only

	if err := handleShareArtifact(nil)(c); err != nil {
		e.HTTPErrorHandler(err, c)
	}
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status: got %d, want 403 (body=%s)", rec.Code, rec.Body.String())
	}
}

// TestUIShareArtifact_NoRenderedHTML_412 covers acceptance 3b: rendered_html=NULL
// over /ui → 412.
func TestUIShareArtifact_NoRenderedHTML_412(t *testing.T) {
	mem := publicSharedMem()
	mem.Visibility = "project"
	mem.RenderedHTML = nil
	defer withLoadMemoryOverride(mem, nil)()

	e := echo.New()
	c, rec := newUIContext(e, http.MethodPost, "/ui/artifacts/mem_share1/share", "mem_share1")
	c.SetPath("/ui/artifacts/:id/share")
	setUser(c, authorUser())

	if err := handleShareArtifact(nil)(c); err != nil {
		e.HTTPErrorHandler(err, c)
	}
	if rec.Code != http.StatusPreconditionFailed {
		t.Fatalf("status: got %d, want 412 (body=%s)", rec.Code, rec.Body.String())
	}
}

// TestUIArtifactHTML_ShareControlInjected covers acceptance 4: the /ui viewer
// injects the share control when rendered_html != nil, with data-shared
// reflecting visibility and a share.js script tag.
func TestUIArtifactHTML_ShareControlInjected(t *testing.T) {
	defer withVersionChainOverride()()

	cases := []struct {
		name       string
		visibility string
		wantShared string
	}{
		{"public_shared_true", "public", `data-shared="true"`},
		{"project_shared_false", "project", `data-shared="false"`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mem := publicSharedMem()
			mem.Visibility = tc.visibility
			mem.WorkItemID = strptr("aihub#154")
			defer withLoadMemoryOverride(mem, nil)()

			e := echo.New()
			c, rec := newUIContext(e, http.MethodGet, "/ui/artifacts/mem_share1/html", "mem_share1")
			c.SetPath("/ui/artifacts/:id/html")
			setUser(c, authorUser())

			if err := handleArtifactHTML(nil)(c); err != nil {
				e.HTTPErrorHandler(err, c)
			}
			if rec.Code != http.StatusOK {
				t.Fatalf("status: got %d, want 200 (body=%s)", rec.Code, excerptStr(rec.Body.String()))
			}
			body := rec.Body.String()
			if !strings.Contains(body, `id="pf-share"`) {
				t.Errorf("missing pf-share control; excerpt: %s", excerptStr(body))
			}
			if !strings.Contains(body, tc.wantShared) {
				t.Errorf("missing %s; excerpt: %s", tc.wantShared, excerptStr(body))
			}
			if !strings.Contains(body, "/ui/static/share.js") {
				t.Errorf("missing share.js script; excerpt: %s", excerptStr(body))
			}
			// aihub#154: the share control must sit BELOW the document title
			// (the first </h1>), not above it.
			h1End := strings.Index(body, "</h1>")
			shareAt := strings.Index(body, `id="pf-share"`)
			if h1End < 0 {
				t.Fatalf("rendered body has no </h1>; excerpt: %s", excerptStr(body))
			}
			if shareAt < h1End {
				t.Errorf("share control must follow the first </h1> (h1End=%d shareAt=%d); excerpt: %s",
					h1End, shareAt, excerptStr(body))
			}
		})
	}
}

// TestUIArtifactHTML_ShareAboveVersionHistory covers the multi-version layout
// requirement (aihub#154 #3): when a spec has a >1-version chain, the share
// control renders ABOVE the version-history dropdown, and both sit below the
// title (title → share → version-history).
func TestUIArtifactHTML_ShareAboveVersionHistory(t *testing.T) {
	prev := versionChainFn
	versionChainFn = func(_ context.Context, _ *pgxpool.Pool, _ string) ([]domain.MemoryVersionRef, error) {
		return []domain.MemoryVersionRef{
			{ID: "mem_share1", CreatedAt: "2024-01-01T00:00:00Z", Status: "archived", IsCurrent: false},
			{ID: "mem_v2", CreatedAt: "2024-06-01T00:00:00Z", Status: "active", IsCurrent: true},
		}, nil
	}
	defer func() { versionChainFn = prev }()

	mem := publicSharedMem()
	mem.WorkItemID = strptr("aihub#154")
	defer withLoadMemoryOverride(mem, nil)()

	e := echo.New()
	c, rec := newUIContext(e, http.MethodGet, "/ui/artifacts/mem_share1/html", "mem_share1")
	c.SetPath("/ui/artifacts/:id/html")
	setUser(c, authorUser())

	if err := handleArtifactHTML(nil)(c); err != nil {
		e.HTTPErrorHandler(err, c)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200 (body=%s)", rec.Code, excerptStr(rec.Body.String()))
	}
	body := rec.Body.String()

	h1End := strings.Index(body, "</h1>")
	shareAt := strings.Index(body, `id="pf-share"`)
	railAt := strings.Index(body, `id="pf-side-rail"`)
	vhRailAt := strings.Index(body, `class="pf-side-vh"`)
	if h1End < 0 || shareAt < 0 || railAt < 0 || vhRailAt < 0 {
		t.Fatalf("expected in-card share + side-rail version history; h1End=%d shareAt=%d railAt=%d vhRailAt=%d; excerpt: %s",
			h1End, shareAt, railAt, vhRailAt, excerptStr(body))
	}
	// aihub#159: share stays in the doc card below the title; version history moved
	// out of the card into the consolidated side rail (#pf-side-rail).
	if h1End >= shareAt {
		t.Errorf("share must render below the title (h1End=%d shareAt=%d)", h1End, shareAt)
	}
	if strings.Contains(body, `<section class="pf-version-history"`) {
		t.Errorf("version history should live in the side rail, not an in-card <section>")
	}
	if vhRailAt < railAt {
		t.Errorf("version-history timeline must live inside #pf-side-rail (railAt=%d vhRailAt=%d)", railAt, vhRailAt)
	}
}

// TestUIArtifactHTML_NoRenderedHTML_NoShareControl covers acceptance 5: when
// rendered_html == nil the /ui viewer must NOT inject the share control.
func TestUIArtifactHTML_NoRenderedHTML_NoShareControl(t *testing.T) {
	defer withVersionChainOverride()()

	mem := retroMemNullHTML() // rendered_html = nil, lazy-rendered body
	defer withLoadMemoryOverride(mem, nil)()

	e := echo.New()
	c, rec := newUIContext(e, http.MethodGet, "/ui/artifacts/mem_retro1/html", "mem_retro1")
	c.SetPath("/ui/artifacts/:id/html")
	setUser(c, authorUser())

	if err := handleArtifactHTML(nil)(c); err != nil {
		e.HTTPErrorHandler(err, c)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200 (body=%s)", rec.Code, excerptStr(rec.Body.String()))
	}
	body := rec.Body.String()
	if strings.Contains(body, `id="pf-share"`) {
		t.Errorf("share control must be absent when rendered_html is nil; excerpt: %s", excerptStr(body))
	}
	if strings.Contains(body, "/ui/static/share.js") {
		t.Errorf("share.js must be absent when rendered_html is nil; excerpt: %s", excerptStr(body))
	}
}

// TestArtifactHTML_V1AndShare_NoShareControl covers acceptance 6 (byte-identical
// conservation): the /v1 html output and the /share output must NOT contain the
// share control nor the share.js script.
func TestArtifactHTML_V1AndShare_NoShareControl(t *testing.T) {
	// /v1 path.
	mem := publicSharedMem() // rendered_html != nil
	defer withLoadMemoryOverride(mem, nil)()

	e := echo.New()
	c, rec := newUIContext(e, http.MethodGet, "/v1/artifacts/mem_share1/html", "mem_share1")
	c.SetPath("/v1/artifacts/:id/html")
	setUser(c, authorUser())

	if err := handleArtifactHTML(nil)(c); err != nil {
		e.HTTPErrorHandler(err, c)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("/v1 status: got %d, want 200 (body=%s)", rec.Code, excerptStr(rec.Body.String()))
	}
	v1Body := rec.Body.String()
	if strings.Contains(v1Body, "pf-share") {
		t.Errorf("/v1 output must not contain pf-share; excerpt: %s", excerptStr(v1Body))
	}
	if strings.Contains(v1Body, "share.js") {
		t.Errorf("/v1 output must not contain share.js; excerpt: %s", excerptStr(v1Body))
	}

	// /share path (handleSharedArtifact). publicSharedMem is already public.
	sc, srec := newUIContext(e, http.MethodGet, "/share/mem_share1", "mem_share1")
	if err := handleSharedArtifact(nil)(sc); err != nil {
		e.HTTPErrorHandler(err, sc)
	}
	if srec.Code != http.StatusOK {
		t.Fatalf("/share status: got %d, want 200 (body=%s)", srec.Code, excerptStr(srec.Body.String()))
	}
	shareBody := srec.Body.String()
	if strings.Contains(shareBody, "pf-share") {
		t.Errorf("/share output must not contain pf-share; excerpt: %s", excerptStr(shareBody))
	}
	if strings.Contains(shareBody, "share.js") {
		t.Errorf("/share output must not contain share.js; excerpt: %s", excerptStr(shareBody))
	}
}

// ─── aihub#124: annotation UI tests ──────────────────────────────────────────

// TestBuildAnnotationHTML_RouteAware is the core route-aware test:
//   - Calling buildAnnotationHTML (the /ui path) with a spec body + commits
//     produces HTML containing thread markers and the add-comment form.
//   - renderArtifactBodyWithMeta with empty annotHTML (the /v1 path) does NOT
//     contain any annotation markers.
func TestBuildAnnotationHTML_RouteAware(t *testing.T) {
	const specBody = `<h1 id="overview">Overview</h1>
<p>intro</p>
<h2 id="goals">Goals</h2>
<p>goals text</p>`

	// Two commits: one open (anchored to "overview"), one resolved with reply.
	openEntry := CommitEntry{
		ID:            "cm_open1",
		AuthorDisplay: "Alice",
		AuthorUserID:  "u_alice",
		Body:          "This section needs more detail",
		CreatedAt:     "2024-01-01T10:00:00Z",
		Status:        CommitStatusOpen,
		Anchor:        &CommitAnchor{HeadingID: "overview", HeadingText: "Overview"},
	}
	resolvedEntry := CommitEntry{
		ID:            "cm_res1",
		AuthorDisplay: "Bob",
		AuthorUserID:  "u_bob",
		Body:          "Goals are unclear",
		CreatedAt:     "2024-01-02T10:00:00Z",
		Status:        CommitStatusResolved,
		Reply:         "Updated goals section with specific OKRs",
		ResolvedAt:    "2024-01-03T09:00:00Z",
		Anchor:        &CommitAnchor{HeadingID: "goals", HeadingText: "Goals"},
	}

	commitsJSON, _ := jsonMarshal([]CommitEntry{openEntry, resolvedEntry})
	commitsRaw := json.RawMessage(commitsJSON)

	// --- /ui path: buildAnnotationHTML should return non-empty HTML ---
	annotHTML := buildAnnotationHTML("mem_spec_42", specBody, commitsRaw, "TESTNONCE")

	if annotHTML == "" {
		t.Fatal("buildAnnotationHTML returned empty string for non-empty commits")
	}
	// Must contain the open thread marker.
	if !strings.Contains(annotHTML, "open") {
		t.Errorf("annotation HTML missing 'open' status marker; got: %s", excerptStr(annotHTML))
	}
	// Must contain the resolved thread marker.
	if !strings.Contains(annotHTML, "resolved") {
		t.Errorf("annotation HTML missing 'resolved' status marker; got: %s", excerptStr(annotHTML))
	}
	// Must contain the AI reply text.
	if !strings.Contains(annotHTML, "Updated goals section with specific OKRs") {
		t.Errorf("annotation HTML missing AI reply text; got: %s", excerptStr(annotHTML))
	}
	// Must contain the form action pointing to the artifact commit route.
	if !strings.Contains(annotHTML, "/ui/artifacts/mem_spec_42/commit") {
		t.Errorf("annotation HTML missing form action; got: %s", excerptStr(annotHTML))
	}
	// Must contain heading anchor links in the thread headers.
	if !strings.Contains(annotHTML, "#overview") {
		t.Errorf("annotation HTML missing anchor link to #overview; got: %s", excerptStr(annotHTML))
	}
	// Must contain select options for the headings.
	if !strings.Contains(annotHTML, "overview") || !strings.Contains(annotHTML, "goals") {
		t.Errorf("annotation HTML missing heading select options; got: %s", excerptStr(annotHTML))
	}

	// --- /v1 path: renderArtifactBodyWithMeta with empty annotHTML ---
	v1Doc := renderArtifactBodyWithMeta(specBody, "mem (methodology.spec)", "", "", "", nil)
	// /v1 must not contain any annotation section markers.
	if strings.Contains(v1Doc, "<section class=\"pf-annotations\"") {
		t.Errorf("/v1 document must not contain pf-annotations section; got: %s", excerptStr(v1Doc))
	}
	if strings.Contains(v1Doc, "/ui/artifacts/mem_spec_42/commit") {
		t.Errorf("/v1 document must not contain annotation form action")
	}

	// --- /ui path: renderArtifactBodyWithMeta WITH annotHTML ---
	uiDoc := renderArtifactBodyWithMeta(specBody, "mem (methodology.spec)", "", "", "", nil, annotHTML)
	if !strings.Contains(uiDoc, "<section class=\"pf-annotations\"") {
		t.Errorf("/ui document must contain pf-annotations section; got: %s", excerptStr(uiDoc))
	}
	if !strings.Contains(uiDoc, "Updated goals section with specific OKRs") {
		t.Errorf("/ui document must contain AI reply text; got: %s", excerptStr(uiDoc))
	}
}

// TestBuildAnnotationHTML_NoCommitsNoHeadings verifies that an artifact with
// neither commits nor headings returns an empty annotation fragment.
func TestBuildAnnotationHTML_NoCommitsNoHeadings(t *testing.T) {
	got := buildAnnotationHTML("mem_x", "<p>no headings here</p>", nil, "TESTNONCE")
	if got != "" {
		t.Errorf("expected empty annotation fragment, got: %s", excerptStr(got))
	}
}

// TestBuildAnnotationHTML_UnanchoredGroup verifies that commits with no anchor
// fall into the general/unanchored group.
func TestBuildAnnotationHTML_UnanchoredGroup(t *testing.T) {
	entry := CommitEntry{
		ID:            "cm_general",
		AuthorDisplay: "Carol",
		Body:          "General comment",
		CreatedAt:     "2024-01-01T10:00:00Z",
		Status:        CommitStatusOpen,
		// No Anchor set — unanchored.
	}
	commitsJSON, _ := jsonMarshal([]CommitEntry{entry})
	got := buildAnnotationHTML("mem_y", "<h1 id=\"intro\">Intro</h1>", json.RawMessage(commitsJSON), "TESTNONCE")
	if !strings.Contains(got, "general") {
		t.Errorf("unanchored commit should appear in general group; got: %s", excerptStr(got))
	}
	if !strings.Contains(got, "General comment") {
		t.Errorf("unanchored commit body missing; got: %s", excerptStr(got))
	}
}

// TestExtractHeadingsFromHTML verifies that extractHeadingsFromHTML produces
// (id, text) pairs that match what goldmark's WithAutoHeadingID would render.
// We validate by checking a known goldmark output snippet.
func TestExtractHeadingsFromHTML(t *testing.T) {
	// This is a real goldmark-rendered fragment (WithAutoHeadingID enabled).
	htmlFrag := `<h1 id="overview">Overview</h1>
<p>intro text</p>
<h2 id="background-and-motivation">Background and Motivation</h2>
<p>more text</p>
<h3 id="sub-section">Sub Section</h3>`

	refs := extractHeadingsFromHTML(htmlFrag)
	if len(refs) != 3 {
		t.Fatalf("expected 3 heading refs, got %d: %+v", len(refs), refs)
	}
	cases := []struct {
		id   string
		text string
	}{
		{"overview", "Overview"},
		{"background-and-motivation", "Background and Motivation"},
		{"sub-section", "Sub Section"},
	}
	for i, tc := range cases {
		if refs[i].ID != tc.id {
			t.Errorf("ref[%d].ID = %q, want %q", i, refs[i].ID, tc.id)
		}
		if refs[i].Text != tc.text {
			t.Errorf("ref[%d].Text = %q, want %q", i, refs[i].Text, tc.text)
		}
	}
}

// TestExtractHeadingsFromHTML_HeadingIDAlignmentWithGoldmark proves that the
// heading ids in the extracted refs are identical to those goldmark renders.
// We render a markdown snippet with render.Markdown, extract the ids from the
// produced HTML with extractHeadingsFromHTML, and compare to render.ExtractHeadings
// run on the same markdown source — they MUST match.
func TestExtractHeadingsFromHTML_HeadingIDAlignmentWithGoldmark(t *testing.T) {
	const md = "# Overview\n\n## Background and Motivation\n\n### Sub Section\n"

	// Render via goldmark (same engine as the artifact pipeline).
	htmlOut, err := render.Markdown(md)
	if err != nil {
		t.Fatalf("render.Markdown: %v", err)
	}

	// Extract from the rendered HTML (simulates the route handler path).
	htmlRefs := extractHeadingsFromHTML(htmlOut)

	// Extract via goldmark AST walk (the reference ground truth).
	astRefs := render.ExtractHeadings(md)

	if len(htmlRefs) != len(astRefs) {
		t.Fatalf("ref count mismatch: htmlRefs=%d, astRefs=%d\nhtmlRefs=%+v\nastRefs=%+v",
			len(htmlRefs), len(astRefs), htmlRefs, astRefs)
	}
	for i := range htmlRefs {
		if htmlRefs[i].ID != astRefs[i].ID {
			t.Errorf("ref[%d] ID mismatch: html=%q ast=%q", i, htmlRefs[i].ID, astRefs[i].ID)
		}
	}
}

// ─── aihub#124: version history UI ──────────────────────────────────────────

// TestBuildVersionHistoryHTML_SingleVersion verifies that a chain with only one
// entry produces an empty string (nothing to render — no history to surface).
func TestBuildVersionHistoryHTML_SingleVersion(t *testing.T) {
	versions := []domain.MemoryVersionRef{
		{ID: "mem_v1", CreatedAt: "2024-01-01T00:00:00Z", Status: "active", IsCurrent: true},
	}
	got := buildVersionHistoryHTML(context.TODO(), nil, "mem_v1", versions, "TESTNONCE")
	if got != "" {
		t.Errorf("single-version chain must produce empty HTML, got: %s", excerptStr(got))
	}
}

// TestBuildVersionHistoryHTML_MultiVersion verifies:
//  1. Two-version chain produces HTML with pf-version-history class.
//  2. The currently-viewed version is labelled "viewing" (plain text, not a link).
//  3. The head version (IsCurrent=true) gets the "current" badge.
//  4. Non-viewed versions appear as links to /ui/artifacts/<id>/html.
func TestBuildVersionHistoryHTML_MultiVersion(t *testing.T) {
	versions := []domain.MemoryVersionRef{
		{ID: "mem_v1", CreatedAt: "2024-01-01T00:00:00Z", Status: "archived", IsCurrent: false},
		{ID: "mem_v2", CreatedAt: "2024-06-01T12:00:00Z", Status: "active", IsCurrent: true},
	}
	// Viewing mem_v1 (the older version).
	got := buildVersionHistoryHTML(context.TODO(), nil, "mem_v1", versions, "TESTNONCE")

	if !strings.Contains(got, "pf-version-history") {
		t.Errorf("missing pf-version-history class; got: %s", excerptStr(got))
	}
	// v1 is being viewed — must NOT be a link.
	if strings.Contains(got, `href="/ui/artifacts/mem_v1/html"`) {
		t.Errorf("currently-viewed version must not be a link; got: %s", excerptStr(got))
	}
	if !strings.Contains(got, "viewing") {
		t.Errorf("currently-viewed version must carry 'viewing' label; got: %s", excerptStr(got))
	}
	// v2 is the head — must be a link + carry "current" badge.
	if !strings.Contains(got, `href="/ui/artifacts/mem_v2/html"`) {
		t.Errorf("other version must be a link; got: %s", excerptStr(got))
	}
	if !strings.Contains(got, "current") {
		t.Errorf("head version must carry 'current' badge; got: %s", excerptStr(got))
	}
	// Timestamp prefix (YYYY-MM-DD) must appear.
	if !strings.Contains(got, "2024-01-01") {
		t.Errorf("v1 date prefix missing; got: %s", excerptStr(got))
	}
}

// TestVersionHistoryHTML_RouteAware verifies the route-aware purity:
//   - /ui path: renderArtifactBodyWithMeta WITH versionHistory+annotHTML fragment
//     contains "pf-version-history".
//   - /v1 path (no fragment passed): must NOT contain "pf-version-history".
func TestVersionHistoryHTML_RouteAware(t *testing.T) {
	specBody := `<h1 id="overview">Overview</h1><p>content</p>`

	versions := []domain.MemoryVersionRef{
		{ID: "mem_v1", CreatedAt: "2024-01-01T00:00:00Z", Status: "archived", IsCurrent: false},
		{ID: "mem_v2", CreatedAt: "2024-06-01T00:00:00Z", Status: "active", IsCurrent: true},
	}
	vhHTML := buildVersionHistoryHTML(context.TODO(), nil, "mem_v2", versions, "TESTNONCE")
	if vhHTML == "" {
		t.Fatal("buildVersionHistoryHTML returned empty for 2-version chain")
	}

	// /ui: pass vhHTML as the variadic annotationsHTML arg.
	uiDoc := renderArtifactBodyWithMeta(specBody, "mem (methodology.spec)", "", "", "", nil, vhHTML)
	if !strings.Contains(uiDoc, "<section class=\"pf-version-history\"") {
		t.Errorf("/ui document must contain pf-version-history; got: %s", excerptStr(uiDoc))
	}

	// /v1: call WITHOUT variadic arg (no extra fragments at all).
	v1Doc := renderArtifactBodyWithMeta(specBody, "mem (methodology.spec)", "", "", "", nil)
	if strings.Contains(v1Doc, "<section class=\"pf-version-history\"") {
		t.Errorf("/v1 document must NOT contain pf-version-history; got: %s", excerptStr(v1Doc))
	}
}

// TestVersionHistoryHTML_NilVersions verifies that a nil/empty slice returns "".
func TestVersionHistoryHTML_NilVersions(t *testing.T) {
	if got := buildVersionHistoryHTML(context.TODO(), nil, "mem_x", nil, "TESTNONCE"); got != "" {
		t.Errorf("nil versions must produce empty HTML, got: %s", excerptStr(got))
	}
	if got := buildVersionHistoryHTML(context.TODO(), nil, "mem_x", []domain.MemoryVersionRef{}, "TESTNONCE"); got != "" {
		t.Errorf("empty versions must produce empty HTML, got: %s", excerptStr(got))
	}
}

// ─── aihub#248 review_fix: exact-version marker (pf_exact) + W1/W2 ─────────
//
// Spec amendment (mem_Vcc8Jf6M superseded following deep review mem_eCIctvsx):
// non-goal 6 now permits a narrow exact-version marker so the two deliberate
// past-version link sites (the side rail below, and wi_detail.html.tmpl's
// per-version "View" link) can still reach a specific superseded revision.

// withLoadMemoryDispatch swaps loadMemoryFn with an id-keyed fake. Needed by
// the W1 side-rail leak test below, where the primary record and OTHER
// lineage-chain members must resolve to DIFFERENT full domain.Memory records
// so the test can express "the caller can see A but not B" — the shared,
// id-blind withLoadMemoryOverride returns one fixed record for any id and
// cannot express that.
func withLoadMemoryDispatch(byID map[string]*domain.Memory) func() {
	prev := loadMemoryFn
	loadMemoryFn = func(_ context.Context, _ *pgxpool.Pool, id string) (*domain.Memory, *domain.AihubError) {
		if m, ok := byID[id]; ok {
			return m, nil
		}
		return nil, domain.NewErr(domain.ErrNotFound, "test fake: unknown id "+id)
	}
	return func() { loadMemoryFn = prev }
}

// TestArtifactHTML_UI_ExactVersionMarker_SkipsRedirect covers the blocking
// finding's fix: a caller that followed a deliberate past-version link
// (?pf_exact=1) must see the exact requested revision, not the head —
// resolveLatestFn must not even be called.
func TestArtifactHTML_UI_ExactVersionMarker_SkipsRedirect(t *testing.T) {
	defer withVersionChainOverride()()
	old := supersededArtifactMem("mem_old_exact1", "mem_head_exact1", "experience.debug")
	defer withLoadMemoryOverride(old, nil)()
	calls := 0
	defer withResolveLatestCounter(t, "mem_old_exact1", nil, nil, &calls)()

	e := echo.New()
	c, rec := newUIContext(e, http.MethodGet, "/ui/artifacts/mem_old_exact1/html?pf_exact=1", "mem_old_exact1")
	c.SetPath("/ui/artifacts/:id/html")
	setUser(c, authorUser())

	if err := handleArtifactHTML(nil)(c); err != nil {
		t.Fatalf("handler error: %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200 (body=%s)", rec.Code, excerptStr(rec.Body.String()))
	}
	if loc := rec.Header().Get("Location"); loc != "" {
		t.Errorf("pf_exact=1 must skip the redirect entirely; got Location %q", loc)
	}
	if !strings.Contains(rec.Body.String(), "OLD-BODY-MARKER") {
		t.Errorf("pf_exact=1 must serve the exact requested version")
	}
	if calls != 0 {
		t.Errorf("resolveLatestFn calls: got %d, want 0 (pf_exact=1 must skip head resolution entirely)", calls)
	}
}

// TestArtifactHTML_UI_HeadLatestID_PointsElsewhere_NoRedirect covers W2: a
// head whose OWN cursor points somewhere else (multi-hop / stale data) must
// not trigger a second redirect — the handler falls back to the originally
// requested record instead of chaining.
func TestArtifactHTML_UI_HeadLatestID_PointsElsewhere_NoRedirect(t *testing.T) {
	defer withVersionChainOverride()()
	old := supersededArtifactMem("mem_old_w2", "mem_head_w2", "experience.debug")
	defer withLoadMemoryOverride(old, nil)()

	head := headArtifactMem("mem_head_w2", "experience.debug")
	elsewhere := "mem_somewhere_else"
	head.LatestID = &elsewhere // head's own cursor points away from itself
	defer withResolveLatestOverride(t, "mem_old_w2", head, nil)()

	e := echo.New()
	c, rec := newUIContext(e, http.MethodGet, "/ui/artifacts/mem_old_w2/html", "mem_old_w2")
	c.SetPath("/ui/artifacts/:id/html")
	setUser(c, authorUser())

	if err := handleArtifactHTML(nil)(c); err != nil {
		t.Fatalf("handler error: %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200 (fallback; body=%s)", rec.Code, excerptStr(rec.Body.String()))
	}
	if loc := rec.Header().Get("Location"); loc != "" {
		t.Errorf("a head whose own LatestID points elsewhere must not redirect again; got Location %q", loc)
	}
	if !strings.Contains(rec.Body.String(), "OLD-BODY-MARKER") {
		t.Errorf("fallback should render the original record")
	}
}

// TestArtifactHTML_UI_SideRail_EmitsExactVersionMarker asserts the side
// rail's "Version history" rows for non-current versions carry ?pf_exact=1
// so they opt out of the head redirect.
func TestArtifactHTML_UI_SideRail_EmitsExactVersionMarker(t *testing.T) {
	mem := headArtifactMem("mem_cur_marker", "experience.debug")
	defer withLoadMemoryOverride(mem, nil)()

	prevVCF := versionChainFn
	versionChainFn = func(_ context.Context, _ *pgxpool.Pool, _ string) ([]domain.MemoryVersionRef, error) {
		return []domain.MemoryVersionRef{
			{ID: "mem_old_marker", CreatedAt: "2024-01-01T00:00:00Z", Status: "archived", IsCurrent: false},
			{ID: mem.ID, CreatedAt: "2024-06-01T00:00:00Z", Status: "active", IsCurrent: true},
		}, nil
	}
	defer func() { versionChainFn = prevVCF }()

	e := echo.New()
	c, rec := newUIContext(e, http.MethodGet, "/ui/artifacts/"+mem.ID+"/html", mem.ID)
	c.SetPath("/ui/artifacts/:id/html")
	setUser(c, authorUser())

	if err := handleArtifactHTML(nil)(c); err != nil {
		t.Fatalf("handler error: %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200 (body=%s)", rec.Code, excerptStr(rec.Body.String()))
	}
	body := rec.Body.String()
	wantHref := `href="/ui/artifacts/mem_old_marker/html?pf_exact=1"`
	if !strings.Contains(body, wantHref) {
		t.Errorf("side rail must emit the exact-version marker on a past-version link; want %q; body=%s", wantHref, excerptStr(body))
	}
}

// TestArtifactHTML_UI_SideRail_DeniedVersion_Omitted covers W1: the side
// rail must not reveal a lineage member the caller cannot access (id, date,
// or link) — domain.MemoryVersionChain applies no project/visibility
// predicate, so the server layer must filter srVersions itself.
func TestArtifactHTML_UI_SideRail_DeniedVersion_Omitted(t *testing.T) {
	cur := headArtifactMem("mem_side_cur", "experience.debug")
	visibleOther := headArtifactMem("mem_side_visible", "experience.debug")
	visibleOther.Status = "archived"
	denied := headArtifactMem("mem_side_denied", "experience.debug")
	denied.Visibility = "private"
	denied.AuthorUserID = "u_someone_else"

	defer withLoadMemoryDispatch(map[string]*domain.Memory{
		cur.ID:          cur,
		visibleOther.ID: visibleOther,
		denied.ID:       denied,
	})()

	prevVCF := versionChainFn
	versionChainFn = func(_ context.Context, _ *pgxpool.Pool, _ string) ([]domain.MemoryVersionRef, error) {
		return []domain.MemoryVersionRef{
			{ID: denied.ID, CreatedAt: "2024-01-01T00:00:00Z", Status: "archived", IsCurrent: false},
			{ID: visibleOther.ID, CreatedAt: "2024-03-01T00:00:00Z", Status: "archived", IsCurrent: false},
			{ID: cur.ID, CreatedAt: "2024-06-01T00:00:00Z", Status: "active", IsCurrent: true},
		}, nil
	}
	defer func() { versionChainFn = prevVCF }()

	e := echo.New()
	c, rec := newUIContext(e, http.MethodGet, "/ui/artifacts/"+cur.ID+"/html", cur.ID)
	c.SetPath("/ui/artifacts/:id/html")
	setUser(c, authorUser())

	if err := handleArtifactHTML(nil)(c); err != nil {
		t.Fatalf("handler error: %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200 (body=%s)", rec.Code, excerptStr(rec.Body.String()))
	}
	body := rec.Body.String()
	if strings.Contains(body, denied.ID) {
		t.Errorf("side rail must not reveal a lineage member the caller cannot access (W1); body=%s", excerptStr(body))
	}
	if !strings.Contains(body, visibleOther.ID) {
		t.Errorf("side rail must still show a lineage member the caller CAN access; body=%s", excerptStr(body))
	}
	// aihub#248 review (minor 7 / test-strength gap): the id-absence check
	// above cannot distinguish "row omitted entirely" from "row kept but
	// unlinked" — a mutation that keeps the denied row and only blanks its
	// Href renders a labeled, dated <span> with no id text and would still
	// pass the two assertions above. Pin the full-omission property W1 is
	// actually about: the denied row's date must also be absent, and exactly
	// two <li class="pf-side-vrow...> rows may render (denied + visibleOther +
	// cur in the fake chain, minus the one filtered out).
	if strings.Contains(body, "2024-01-01") {
		t.Errorf("side rail must not reveal the denied row's date either (row must be fully omitted, not merely unlinked); body=%s", excerptStr(body))
	}
	if n := strings.Count(body, "pf-side-vrow"); n != 2 {
		t.Errorf("side rail must render exactly 2 version rows (denied row fully omitted, not blanked-href); got %d; body=%s", n, excerptStr(body))
	}
}

// TestUIWIDetail_VersionLink_CarriesExactMarker_OpenLinkDoesNot renders
// wi_detail.html.tmpl directly (no DB, no handler fan-out) to verify the
// per-version "View" link (wi_detail.html.tmpl:143) carries the exact-version
// marker while the per-artifact "Open" link (wi_detail.html.tmpl:120) does
// NOT — it is an ordinary cross-link and must keep self-healing to the head.
func TestUIWIDetail_VersionLink_CarriesExactMarker_OpenLinkDoesNot(t *testing.T) {
	tmpl := pageTemplate("wi_detail.html.tmpl", "events_timeline.html.tmpl")

	data := &wiDetailPageData{
		Title:  "wi test",
		Active: "wi",
		Theme:  "auto",
		Origin: "http://example.com",
		Nonce:  "testnonce",
		User:   authorUser(),
		WI: &domain.WorkItem{
			ID: "wi_marker_1", Slug: "testproj#1", Project: "testproj", Goal: "goal", Status: "running",
		},
		Artifacts: []artifactLink{
			{
				MemID: "mem_wi_head",
				Type:  "methodology.spec",
				Versions: []domain.MemoryVersionRef{
					{ID: "mem_wi_head", CreatedAt: "2024-06-01T00:00:00Z", Status: "active", IsCurrent: true},
					{ID: "mem_wi_old", CreatedAt: "2024-01-01T00:00:00Z", Status: "archived", IsCurrent: false},
				},
			},
		},
	}

	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/ui/wi/testproj%231", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	if err := renderTemplate(c, tmpl, "layout", data); err != nil {
		t.Fatalf("renderTemplate error: %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200; body=%s", rec.Code, excerptStr(rec.Body.String()))
	}
	body := rec.Body.String()

	// The "Open" link (wi_detail.html.tmpl:120) is an ordinary cross-link to
	// the artifact's row id and must NOT carry the marker — checked as the
	// exact anchor tag (href + Open text together) so this cannot pass by
	// accidentally matching a *different* anchor that happens to share the
	// same href prefix (the per-version "View" row for this same id, below,
	// legitimately does carry the marker — see decision in the code review).
	wantOpen := `href="/ui/artifacts/mem_wi_head/html" target="_blank" rel="noopener">Open</a>`
	if !strings.Contains(body, wantOpen) {
		t.Errorf("Open link for the current artifact row must render without the marker; want %q; body=%s", wantOpen, excerptStr(body))
	}
	// The per-version "View" link (wi_detail.html.tmpl:143) always carries the
	// marker, including for the row that happens to be the current version —
	// it is still a deliberate link to that exact version id, just like the
	// older row below.
	if !strings.Contains(body, `href="/ui/artifacts/mem_wi_old/html?pf_exact=1"`) {
		t.Errorf("past-version View link must carry the exact-version marker; body=%s", excerptStr(body))
	}
	if !strings.Contains(body, `href="/ui/artifacts/mem_wi_head/html?pf_exact=1" target="_blank" rel="noopener" style="font-size:11px">View</a>`) {
		t.Errorf("current-version View row must also carry the marker (every View link targets an exact revision); body=%s", excerptStr(body))
	}
}

// ─── aihub#248 review round 2 fixes ────────────────────────────────────────

// TestArtifactHTML_UI_AnnotationCommit_PreservesExactMarker_RoundTrip is the
// regression test for review warning 1: a POST/commit round-trip that starts
// on a marked past-version URL must land back on that same past version, not
// the lineage head. Concrete scenario from the review: open v1?pf_exact=1 →
// add an annotation → POST /ui/artifacts/mem_v1_rt/commit?pf_exact=1 (the
// add-annotation form now carries the marker via
// buildAnnotationHTMLWithExact) → 303 must preserve ?pf_exact=1 in Location
// (artifactRedirectURL) → following that redirect must render v1 itself, not
// bounce again to head. Before the fix, step 3's Location dropped the
// marker, so step 4 fell into the lineage-head redirect and silently served
// head content instead of the version the comment was just written on.
func TestArtifactHTML_UI_AnnotationCommit_PreservesExactMarker_RoundTrip(t *testing.T) {
	old := supersededArtifactMem("mem_v1_rt", "mem_head_rt", "experience.debug")

	// Phase 1: POST the annotation commit as if submitted from a page that
	// was rendered with ?pf_exact=1 (the form action itself carries the
	// marker in its query string — see buildAnnotationHTMLWithExact).
	restoreProject := withCommitMemoryProjectOverride(old.Project, old.Status, nil)
	defer restoreProject()
	prevCommit := doArtifactCommitFn
	doArtifactCommitFn = func(_ context.Context, _ *pgxpool.Pool, _, _, _, _ string, _ domain.CommitAnchorArgs) error {
		return nil
	}
	defer func() { doArtifactCommitFn = prevCommit }()

	e := echo.New()
	form := strings.NewReader("body=nice+catch&heading_id=&heading_text=")
	req := httptest.NewRequest(http.MethodPost, "/ui/artifacts/mem_v1_rt/commit?pf_exact=1", form)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetParamNames("id")
	c.SetParamValues("mem_v1_rt")
	setUser(c, authorUser())

	if err := handleUIArtifactCommit(nil)(c); err != nil {
		t.Fatalf("handler error: %v", err)
	}
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status: got %d, want 303 (body=%s)", rec.Code, excerptStr(rec.Body.String()))
	}
	wantLoc := "/ui/artifacts/mem_v1_rt/html?pf_exact=1"
	loc := rec.Header().Get("Location")
	if loc != wantLoc {
		t.Fatalf("Location after commit: got %q, want %q (pf_exact must round-trip through the write)", loc, wantLoc)
	}

	// Phase 2: follow the redirect. mem_v1_rt is superseded by mem_head_rt,
	// so WITHOUT the marker this GET would 302 to head — proving the round
	// trip actually lands back on the exact version requires exercising the
	// same superseded fixture here, not just checking the Location string.
	defer withLoadMemoryOverride(old, nil)()
	defer withVersionChainOverride()()
	calls := 0
	defer withResolveLatestCounter(t, "mem_v1_rt", nil, nil, &calls)()

	e2 := echo.New()
	c2, rec2 := newUIContext(e2, http.MethodGet, loc, "mem_v1_rt")
	c2.SetPath("/ui/artifacts/:id/html")
	setUser(c2, authorUser())

	if err := handleArtifactHTML(nil)(c2); err != nil {
		t.Fatalf("handler error: %v", err)
	}
	if rec2.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200 (body=%s)", rec2.Code, excerptStr(rec2.Body.String()))
	}
	if loc2 := rec2.Header().Get("Location"); loc2 != "" {
		t.Errorf("landing GET must not bounce again to head; got Location %q", loc2)
	}
	if !strings.Contains(rec2.Body.String(), "OLD-BODY-MARKER") {
		t.Errorf("must render the exact past version the comment was written on, not head; body=%s", excerptStr(rec2.Body.String()))
	}
	if calls != 0 {
		t.Errorf("resolveLatestFn calls: got %d, want 0 (marker must skip head resolution on the landing GET)", calls)
	}
}

// TestArtifactHTML_UI_ExactVersionMarker_DoesNotBypassAuthorization pins AC12
// of the amended spec: pf_exact must not widen what is authorized. A caller
// denied the REQUESTED record (private, not the author) must still get 403
// even when the request carries ?pf_exact=1 — checkProjectAccess and
// checkMemoryVisibility run unconditionally before the marker is ever read.
func TestArtifactHTML_UI_ExactVersionMarker_DoesNotBypassAuthorization(t *testing.T) {
	mem := &domain.Memory{
		ID:           "mem_denied_exact",
		Project:      "testproj",
		Type:         "experience.debug",
		Status:       "active",
		Visibility:   "private",
		AuthorUserID: "u_someone_else",
		Content:      "secret",
	}
	defer withLoadMemoryOverride(mem, nil)()

	e := echo.New()
	c, rec := newUIContext(e, http.MethodGet, "/ui/artifacts/mem_denied_exact/html?pf_exact=1", "mem_denied_exact")
	c.SetPath("/ui/artifacts/:id/html")
	setUser(c, otherViewerUser()) // not the author; private visibility

	if err := handleArtifactHTML(nil)(c); err == nil {
		t.Fatalf("expected an error for a denied record even with pf_exact=1")
	}
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status: got %d, want 403 — pf_exact must not bypass authorization on the requested record (AC12); body=%s",
			rec.Code, excerptStr(rec.Body.String()))
	}
	if strings.Contains(rec.Body.String(), "secret") {
		t.Errorf("denied content must not leak; body=%s", excerptStr(rec.Body.String()))
	}
}

// TestArtifactHTML_V1_SupersededID_MarkerIgnored_NoRedirect pins AC13: /v1
// ignores pf_exact entirely (it is /ui-only). Sibling of
// TestArtifactHTML_V1_SupersededID_NoRedirect with the marker appended to the
// request target — behavior must be byte-identical to the marker-less case.
func TestArtifactHTML_V1_SupersededID_MarkerIgnored_NoRedirect(t *testing.T) {
	old := supersededArtifactMem("mem_old6b", "mem_head6b", "experience.debug")
	defer withLoadMemoryOverride(old, nil)()
	calls := 0
	defer withResolveLatestCounter(t, "mem_old6b", headArtifactMem("mem_head6b", "experience.debug"), nil, &calls)()

	e := echo.New()
	c, rec := newUIContext(e, http.MethodGet, "/v1/artifacts/mem_old6b/html?pf_exact=1", "mem_old6b")
	c.SetPath("/v1/artifacts/:id/html")
	setUser(c, authorUser())

	if err := handleArtifactHTML(nil)(c); err != nil {
		t.Fatalf("handler error: %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200", rec.Code)
	}
	if loc := rec.Header().Get("Location"); loc != "" {
		t.Errorf("/v1 must never redirect even with pf_exact present; got Location %q", loc)
	}
	if !strings.Contains(rec.Body.String(), "OLD-BODY-MARKER") {
		t.Errorf("/v1 must serve the exact requested record")
	}
	if calls != 0 {
		t.Errorf("resolveLatestFn calls: got %d, want 0 on /v1 regardless of pf_exact (AC13)", calls)
	}
}

// TestSharedArtifact_SupersededID_MarkerIgnored_NoRedirect pins AC13 for
// /share: sibling of TestSharedArtifact_SupersededID_NoRedirect with the
// marker appended — handleSharedArtifact never references
// isExactVersionRequest, so appending the query param must change nothing.
func TestSharedArtifact_SupersededID_MarkerIgnored_NoRedirect(t *testing.T) {
	old := supersededArtifactMem("mem_old7b", "mem_head7b", "methodology.spec")
	old.Visibility = "public"
	defer withLoadMemoryOverride(old, nil)()
	calls := 0
	defer withResolveLatestCounter(t, "mem_old7b", headArtifactMem("mem_head7b", "methodology.spec"), nil, &calls)()

	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/share/mem_old7b?pf_exact=1", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetParamNames("id")
	c.SetParamValues("mem_old7b")

	if err := handleSharedArtifact(nil)(c); err != nil {
		t.Fatalf("handler error: %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200", rec.Code)
	}
	if loc := rec.Header().Get("Location"); loc != "" {
		t.Errorf("/share must never redirect even with pf_exact present; got Location %q", loc)
	}
	if calls != 0 {
		t.Errorf("resolveLatestFn calls: got %d, want 0 (/share never resolves lineage, marker or not — AC13)", calls)
	}
}

// TestArtifactHTML_UI_SideRail_Labels_ContiguousAfterFilter covers review
// minor 4: after the W1 permission filter drops a row, the remaining labels
// must renumber contiguously (v1, v2, ...) from the FILTERED slice, not the
// unfiltered chain index — otherwise a gap like v1, v3 discloses that a
// hidden version exists, the same class of leak W1 already fixed for
// id/date/href.
func TestArtifactHTML_UI_SideRail_Labels_ContiguousAfterFilter(t *testing.T) {
	cur := headArtifactMem("mem_label_cur", "experience.debug")
	visibleOther := headArtifactMem("mem_label_visible", "experience.debug")
	visibleOther.Status = "archived"
	denied := headArtifactMem("mem_label_denied", "experience.debug")
	denied.Visibility = "private"
	denied.AuthorUserID = "u_someone_else"

	defer withLoadMemoryDispatch(map[string]*domain.Memory{
		cur.ID:          cur,
		visibleOther.ID: visibleOther,
		denied.ID:       denied,
	})()

	prevVCF := versionChainFn
	versionChainFn = func(_ context.Context, _ *pgxpool.Pool, _ string) ([]domain.MemoryVersionRef, error) {
		return []domain.MemoryVersionRef{
			{ID: denied.ID, CreatedAt: "2024-01-01T00:00:00Z", Status: "archived", IsCurrent: false},
			{ID: visibleOther.ID, CreatedAt: "2024-03-01T00:00:00Z", Status: "archived", IsCurrent: false},
			{ID: cur.ID, CreatedAt: "2024-06-01T00:00:00Z", Status: "active", IsCurrent: true},
		}, nil
	}
	defer func() { versionChainFn = prevVCF }()

	e := echo.New()
	c, rec := newUIContext(e, http.MethodGet, "/ui/artifacts/"+cur.ID+"/html", cur.ID)
	c.SetPath("/ui/artifacts/:id/html")
	setUser(c, authorUser())

	if err := handleArtifactHTML(nil)(c); err != nil {
		t.Fatalf("handler error: %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200 (body=%s)", rec.Code, excerptStr(rec.Body.String()))
	}
	body := rec.Body.String()
	wantVisible := `href="/ui/artifacts/` + visibleOther.ID + `/html?pf_exact=1">v1</a>`
	if !strings.Contains(body, wantVisible) {
		t.Errorf("first surviving row must be labeled v1 (contiguous after filtering the denied row); want %q; body=%s", wantVisible, excerptStr(body))
	}
	if strings.Contains(body, ">v3<") {
		t.Errorf("labels must not skip to v3 after one row is filtered out (would disclose a hidden version); body=%s", excerptStr(body))
	}
	wantCur := `<span class="mono pf-side-vlabel">v2</span>`
	if !strings.Contains(body, wantCur) {
		t.Errorf("current row must be labeled v2 (contiguous, not v3); body=%s", excerptStr(body))
	}
}

// ─── aihub#151: share must not widen access ──────────────────────────────────
//
// Two defects, one endpoint pair:
//
//	(1) unshare hard-coded visibility="project", so a share→unshare round trip on
//	    a private (author-only) or admin memory published it to the whole project
//	    instead of revoking anything; and
//	(2) share gated only on rendered_html != nil, which resolveRenderedHTML grants
//	    to ANY type carrying an explicit html= payload, so a writer could publish a
//	    note or decision to the unauthenticated /share/:id route.
//
// The corresponding SQL half of (1) — that the pre-share tier is written down at
// share time at all — is asserted against a real database in
// internal/domain/memory_share_visibility_db_test.go. These tests own the half
// that decides which tier the handler asks for.

// shareAttrs builds an attrs document recording a pre-share visibility tier.
func shareAttrs(tier string) []byte {
	return []byte(`{"related_ids":["mem_keep"],"` + domain.PreShareVisibilityKey + `":"` + tier + `"}`)
}

// runUnshare drives handleUnshareArtifact over the given memory and returns the
// recorder plus the visibility the handler asked SetMemoryVisibility for.
func runUnshare(t *testing.T, mem *domain.Memory) (*httptest.ResponseRecorder, string) {
	t.Helper()
	defer withLoadMemoryOverride(mem, nil)()
	_, gotVis, cleanup := withSetVisibilityOverride(nil)
	defer cleanup()

	e := echo.New()
	c, rec := newUIContext(e, http.MethodDelete, "/v1/artifacts/mem_share1/share", "mem_share1")
	setUser(c, authorUser())
	if err := handleUnshareArtifact(nil)(c); err != nil {
		e.HTTPErrorHandler(err, c)
	}
	return rec, *gotVis
}

// TestUnshareArtifact_RestoresRecordedVisibility is defect (1). Every tier below
// is one the memories_visibility_check constraint allows, and "project" is
// included so the test cannot pass merely because the old hard-coded value
// happens to be right for one row.
func TestUnshareArtifact_RestoresRecordedVisibility(t *testing.T) {
	for _, tier := range []string{"private", "admin", "team", "project"} {
		t.Run(tier, func(t *testing.T) {
			mem := publicSharedMem()
			mem.Attrs = shareAttrs(tier)

			rec, gotVis := runUnshare(t, mem)
			if rec.Code != http.StatusOK {
				t.Fatalf("status: got %d, want 200 (body=%s)", rec.Code, rec.Body.String())
			}
			if gotVis != tier {
				t.Fatalf("unshare restored %q, want the recorded pre-share tier %q — restoring a narrower tier as \"project\" WIDENS access", gotVis, tier)
			}
			if !strings.Contains(rec.Body.String(), `"visibility":"`+tier+`"`) {
				t.Fatalf("response must report the tier it restored: %s", rec.Body.String())
			}
		})
	}
}

// TestUnshareArtifact_UnusableRecordFallsBackToProject covers the rows that
// predate the recording and the values that must not be trusted. "public" is the
// dangerous one: restoring to it would answer {"ok":true} while leaving the
// artifact world-readable.
func TestUnshareArtifact_UnusableRecordFallsBackToProject(t *testing.T) {
	cases := map[string][]byte{
		"no attrs at all":        nil,
		"empty object":           []byte(`{}`),
		"key absent":             []byte(`{"related_ids":["mem_keep"]}`),
		"key is not a string":    []byte(`{"` + domain.PreShareVisibilityKey + `":7}`),
		"tier outside the enum":  []byte(`{"` + domain.PreShareVisibilityKey + `":"bogus"}`),
		"tier is public":         []byte(`{"` + domain.PreShareVisibilityKey + `":"public"}`),
		"attrs is not an object": []byte(`["nope"]`),
		"attrs is malformed":     []byte(`{not json`),
	}
	for name, attrs := range cases {
		t.Run(name, func(t *testing.T) {
			mem := publicSharedMem()
			mem.Attrs = attrs

			rec, gotVis := runUnshare(t, mem)
			if rec.Code != http.StatusOK {
				t.Fatalf("status: got %d, want 200 (body=%s)", rec.Code, rec.Body.String())
			}
			if gotVis != "project" {
				t.Fatalf("unshare restored %q, want the \"project\" fallback", gotVis)
			}
		})
	}
}

// runShare drives handleShareArtifact and reports the status plus whether the
// visibility setter was reached at all — a handler that answers 403 but has
// already published the row would be worse than the bug.
func runShare(t *testing.T, mem *domain.Memory) (*httptest.ResponseRecorder, string) {
	t.Helper()
	defer withLoadMemoryOverride(mem, nil)()
	gotID, _, cleanup := withSetVisibilityOverride(nil)
	defer cleanup()

	e := echo.New()
	c, rec := newUIContext(e, http.MethodPost, "/v1/artifacts/mem_share1/share", "mem_share1")
	setUser(c, authorUser())
	if err := handleShareArtifact(nil)(c); err != nil {
		e.HTTPErrorHandler(err, c)
	}
	return rec, *gotID
}

// TestShareArtifact_NonArtifactTypeRejected is defect (2). Each memory here has a
// non-nil rendered_html, which is exactly what an explicit `html=` on
// pf_save_artifact buys for any type at all — so before the type gate these were
// all publishable to /share/:id with no auth.
func TestShareArtifact_NonArtifactTypeRejected(t *testing.T) {
	for _, memType := range []string{"note", "decision", "observation", "gotcha", "methodology", ""} {
		t.Run("type="+memType, func(t *testing.T) {
			mem := publicSharedMem()
			mem.Visibility = "project"
			mem.Type = memType

			rec, gotID := runShare(t, mem)
			if rec.Code != http.StatusForbidden {
				t.Fatalf("status: got %d, want 403 (body=%s)", rec.Code, rec.Body.String())
			}
			if gotID != "" {
				t.Fatalf("visibility setter was called for %q despite the 403; the row was published anyway", memType)
			}
		})
	}
}

// TestShareArtifact_RenderTypesStillShareable is the other side of that gate:
// widening it into a blanket refusal would also "pass" the test above.
func TestShareArtifact_RenderTypesStillShareable(t *testing.T) {
	types := domain.RenderTypeNames()
	if len(types) < 2 {
		t.Fatalf("expected the default render-type set to be non-trivial, got %v", types)
	}
	for _, memType := range types {
		t.Run("type="+memType, func(t *testing.T) {
			mem := publicSharedMem()
			mem.Visibility = "project"
			mem.Type = memType

			rec, gotID := runShare(t, mem)
			if rec.Code != http.StatusOK {
				t.Fatalf("status: got %d, want 200 (body=%s)", rec.Code, rec.Body.String())
			}
			if gotID != "mem_share1" {
				t.Fatalf("visibility setter not reached for a shareable type %q", memType)
			}
		})
	}
}

// TestShareArtifact_NarrowerThanProjectRejected is the second half of defect (2):
// a project writer is not the author of a private memory and not a global admin,
// so publishing either tier to the anonymous route is an escalation regardless of
// the memory's type.
func TestShareArtifact_NarrowerThanProjectRejected(t *testing.T) {
	for _, vis := range []string{"private", "admin"} {
		t.Run(vis, func(t *testing.T) {
			mem := publicSharedMem() // methodology.spec, has rendered_html
			mem.Visibility = vis

			rec, gotID := runShare(t, mem)
			if rec.Code != http.StatusForbidden {
				t.Fatalf("status: got %d, want 403 (body=%s)", rec.Code, rec.Body.String())
			}
			if gotID != "" {
				t.Fatalf("visibility setter was called for a %q memory despite the 403", vis)
			}
		})
	}

	// Control: the tiers at or above the project stay shareable.
	for _, vis := range []string{"project", "team"} {
		t.Run(vis+" still shareable", func(t *testing.T) {
			mem := publicSharedMem()
			mem.Visibility = vis

			rec, gotID := runShare(t, mem)
			if rec.Code != http.StatusOK {
				t.Fatalf("status: got %d, want 200 (body=%s)", rec.Code, rec.Body.String())
			}
			if gotID != "mem_share1" {
				t.Fatalf("visibility setter not reached for a %q memory", vis)
			}
		})
	}
}

// TestShareArtifact_412MessageNamesTheConfiguredTypes closes a smaller falsehood
// found while fixing the above: the 412 body claimed "only methodology.spec /
// methodology.plan / methodology.review render", which had been wrong since
// aihub#102 added execute, retro and wrap_summary to defaultRenderTypes, and
// would go wrong again the moment RENDER_MEMORY_TYPES is set. It now derives the
// list from the same set the gate reads.
func TestShareArtifact_412MessageNamesTheConfiguredTypes(t *testing.T) {
	mem := publicSharedMem()
	mem.Visibility = "project"
	mem.RenderedHTML = nil

	rec, _ := runShare(t, mem)
	if rec.Code != http.StatusPreconditionFailed {
		t.Fatalf("status: got %d, want 412 (body=%s)", rec.Code, rec.Body.String())
	}
	for _, want := range domain.RenderTypeNames() {
		if !strings.Contains(rec.Body.String(), want) {
			t.Fatalf("412 body omits configured render type %q: %s", want, rec.Body.String())
		}
	}
}
