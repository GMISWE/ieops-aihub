package server

// Security regression tests: auth-before-write invariant.
//
// These tests verify that handleCreateWorkItem and handleRemember reject
// under-privileged callers with 403 BEFORE touching the database.
//
// Strategy: inject a viewer UserContext directly into the echo context and
// pass a nil *pgxpool.Pool. If checkProjectAccess fires first, the handler
// returns 403 and never calls domain functions that would dereference the
// nil pool. A panic (nil pointer) would indicate the DB was reached before
// the auth check — i.e. the bug is present.

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/labstack/echo/v4"
)

// viewerUser returns a UserContext with only viewer access to "testproject".
func viewerUser() *UserContext {
	return &UserContext{
		UserID:      "u_viewer",
		Email:       "viewer@test.example",
		DisplayName: "Viewer User",
		UserType:    "human",
		Role:        "writer", // global role; project role is what matters
		ProjectRoles: map[string]string{
			"testproject": "viewer", // only viewer — not writer
		},
		APIKeyID: "k_viewer",
	}
}

// setUser injects uc into the echo context, mimicking what BearerAuth does.
func setUser(c echo.Context, uc *UserContext) {
	c.Set(string(ctxUser), uc)
}

// TestCreateWorkItem_ViewerGets403BeforeDBWrite verifies that POST /v1/work_items
// returns 403 for a viewer-only user without performing any database write.
// A nil pool is passed; any DB access before the auth check would panic.
func TestCreateWorkItem_ViewerGets403BeforeDBWrite(t *testing.T) {
	e := echo.New()

	body := `{"project":"testproject","goal":"do something"}`
	req := httptest.NewRequest(http.MethodPost, "/v1/work_items", strings.NewReader(body))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()

	c := e.NewContext(req, rec)
	setUser(c, viewerUser())

	// nil pool: any DB call before auth would panic — proving auth fires first.
	handler := handleCreateWorkItem(nil)
	if err := handler(c); err != nil {
		// Echo handlers may return an error instead of writing directly.
		e.HTTPErrorHandler(err, c)
	}

	if rec.Code != http.StatusForbidden {
		t.Errorf("handleCreateWorkItem viewer: expected 403, got %d (body: %s)",
			rec.Code, rec.Body.String())
	}
}

// TestRemember_ViewerGets403BeforeDBWrite verifies that POST /v1/memories
// returns 403 for a viewer-only user without performing any database write.
// project is provided directly to bypass the work_item_id backfill DB read.
// A nil pool is passed; any DB write before the auth check would panic.
func TestRemember_ViewerGets403BeforeDBWrite(t *testing.T) {
	e := echo.New()

	// Supply project explicitly so the wi backfill path (DB read) is skipped.
	body := `{"project":"testproject","type":"fact","content":"test content"}`
	req := httptest.NewRequest(http.MethodPost, "/v1/memories", strings.NewReader(body))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()

	c := e.NewContext(req, rec)
	setUser(c, viewerUser())

	// nil pool: any DB write before auth would panic — proving auth fires first.
	handler := handleRemember(nil)
	if err := handler(c); err != nil {
		e.HTTPErrorHandler(err, c)
	}

	if rec.Code != http.StatusForbidden {
		t.Errorf("handleRemember viewer: expected 403, got %d (body: %s)",
			rec.Code, rec.Body.String())
	}
}
