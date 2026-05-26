package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/labstack/echo/v4"
)

// requireSessionStub is a minimal RequireUISession that skips the DB lookup —
// used only by this test so we can validate the cookie-only branches without
// spinning up a real pool. The cookie-handling logic mirrors the real middleware.
func requireSessionStub(sm *SessionManager) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			cookie, err := c.Cookie(pfSessionCookieName)
			if err != nil || cookie == nil || cookie.Value == "" {
				return redirectToLogin(c)
			}
			uid, kid, verr := sm.Verify(cookie.Value)
			if verr != nil {
				return redirectToLogin(c)
			}
			c.Set(string(ctxUser), &UserContext{
				UserID:       uid,
				APIKeyID:     kid,
				Role:         "writer",
				ProjectRoles: map[string]string{},
			})
			return next(c)
		}
	}
}

func newTestEcho(sm *SessionManager) *echo.Echo {
	e := echo.New()
	e.GET("/ui/queue", func(c echo.Context) error {
		u := GetUser(c)
		if u == nil {
			return c.String(http.StatusInternalServerError, "no user")
		}
		return c.String(http.StatusOK, "ok:"+u.UserID)
	}, requireSessionStub(sm))
	return e
}

func TestRequireUISession_MissingCookie_Redirects(t *testing.T) {
	sm := NewSessionManager([]byte("test-secret"))
	e := newTestEcho(sm)

	req := httptest.NewRequest(http.MethodGet, "/ui/queue", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusFound {
		t.Fatalf("missing cookie: expected 302, got %d", rec.Code)
	}
	loc := rec.Header().Get("Location")
	if !strings.HasPrefix(loc, "/ui/login") {
		t.Fatalf("missing cookie: expected redirect to /ui/login, got %q", loc)
	}
	if !strings.Contains(loc, "next=") {
		t.Fatalf("missing cookie: expected next= in redirect, got %q", loc)
	}
}

func TestRequireUISession_InvalidCookie_Redirects(t *testing.T) {
	sm := NewSessionManager([]byte("test-secret"))
	e := newTestEcho(sm)

	req := httptest.NewRequest(http.MethodGet, "/ui/queue", nil)
	req.AddCookie(&http.Cookie{Name: pfSessionCookieName, Value: "totally-not-valid"})
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusFound {
		t.Fatalf("invalid cookie: expected 302, got %d", rec.Code)
	}
	if !strings.HasPrefix(rec.Header().Get("Location"), "/ui/login") {
		t.Fatalf("invalid cookie: expected redirect to /ui/login, got %q",
			rec.Header().Get("Location"))
	}
}

func TestRequireUISession_ValidCookie_PassesThrough(t *testing.T) {
	sm := NewSessionManager([]byte("test-secret"))
	e := newTestEcho(sm)

	token := sm.Sign("u_alice", "k_alice", pfSessionTTL)
	req := httptest.NewRequest(http.MethodGet, "/ui/queue", nil)
	req.AddCookie(&http.Cookie{Name: pfSessionCookieName, Value: token})
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("valid cookie: expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	if got := rec.Body.String(); got != "ok:u_alice" {
		t.Fatalf("valid cookie: expected handler to see user, got body %q", got)
	}
}

func TestIsSafeNext(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{"", false},
		{"/ui/", true},
		{"/ui/queue", true},
		{"/ui/wi/wi_abc", true},
		{"/v1/work_items", false},
		{"http://evil.example/", false},
		{"//evil.example/", false},
		{"/ui//evil.example/", false},
		{`/ui/\\evil.example`, false},
	}
	for _, tc := range cases {
		if got := isSafeNext(tc.in); got != tc.want {
			t.Errorf("isSafeNext(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}
