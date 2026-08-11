package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/labstack/echo/v4"
)

// The security headers on /ui are attached by middleware on the echo group, and until this
// file existed nothing proved the attachment. TestUISecurityHeaders_Middleware exercises
// uiSecurityHeaders() in isolation, which passes whether or not it is wired to anything.
//
// The gap was demonstrated, not theorised: deleting uiSecurityHeaders() from
//
//	e.Group("/ui", RequireUISession(sm, pool), uiSecurityHeaders())
//
// left the entire test suite green. That single-line deletion is aihub#144 verbatim — every
// authed /ui page served with no CSP — which is the defect this whole wi exists to close. A
// test of a security control that does not cover whether the control is installed reads as
// coverage it does not provide.
//
// These tests go through RegisterUIRoutes and assert on real responses. The authed cases
// need RequireUISession to pass, which is why uiUserLoader is a seam: the CSP middleware runs
// after the session check, so an unauthenticated request is redirected before ever reaching
// it and could not observe the header.

const testCookieSecret = "test-secret-must-be-long-enough-for-hmac-signing"

// withUIUserLoader makes the session middleware succeed without a database.
func withUIUserLoader(uc *UserContext) func() {
	prev := uiUserLoader
	uiUserLoader = func(_ context.Context, _ *pgxpool.Pool, _ string) (*UserContext, error) {
		return uc, nil
	}
	return func() { uiUserLoader = prev }
}

// registeredUI builds an echo instance with the real route table and returns a request
// helper that carries a valid session cookie.
func registeredUI(t *testing.T) func(method, target string) *httptest.ResponseRecorder {
	t.Helper()
	e := echo.New()
	RegisterUIRoutes(e, nil, []byte(testCookieSecret))
	sm := NewSessionManager([]byte(testCookieSecret))
	token := sm.Sign("u_test", "ak_test", time.Hour)

	// Named result: a recovered panic must still yield the recorder, and an unnamed return
	// would hand back nil instead — turning every assertion below into a nil dereference.
	return func(method, target string) (rec *httptest.ResponseRecorder) {
		req := httptest.NewRequest(method, target, nil)
		req.AddCookie(&http.Cookie{Name: pfSessionCookieName, Value: token})
		rec = httptest.NewRecorder()
		// The page handlers dereference the (nil) pool and panic. Recover here rather than
		// installing echo's Recover middleware, so the chain being tested is the real one and
		// not one with an extra layer. The headers are already staged on the recorder by then:
		// uiSecurityHeaders writes them before calling next(), which is exactly the property
		// that makes a failing response a valid place to observe them.
		defer func() { _ = recover() }()
		e.ServeHTTP(rec, req)
		return rec
	}
}

// TestUISecurityHeaders_AttachedToAuthedGroup is the test whose absence let the mutation
// through: a real authed /ui response must carry the policy.
//
// The handler behind this route needs a database and will fail with a nil pool. That is
// deliberate and does not weaken the assertion — uiSecurityHeaders sets the headers BEFORE
// calling next(), so what is being pinned is that the middleware is in the chain at all,
// independently of whether the page renders. Asserting on a failing response is the cheapest
// honest way to cover the wiring without a DB.
func TestUISecurityHeaders_AttachedToAuthedGroup(t *testing.T) {
	defer withUIUserLoader(authorUser())()
	do := registeredUI(t)

	// Several routes across different register* functions, because the group middleware is
	// the shared thing under test and a per-handler regression would look identical from any
	// single route.
	for _, target := range []string{"/ui/wi", "/ui/memories", "/ui/artifacts/mem_x/html"} {
		t.Run(target, func(t *testing.T) {
			rec := do(http.MethodGet, target)

			if got := rec.Header().Get("Content-Security-Policy"); got != uiPageCSP {
				t.Errorf("authed %s carries CSP %q, want uiPageCSP %q — if empty, "+
					"uiSecurityHeaders() is not attached to the /ui group and every authed "+
					"page is served without a policy (aihub#144)", target, got, uiPageCSP)
			}
			if got := rec.Header().Get("X-Content-Type-Options"); got != "nosniff" {
				t.Errorf("authed %s: nosniff = %q, want %q", target, got, "nosniff")
			}
			if got := rec.Header().Get("Referrer-Policy"); got != "no-referrer" {
				t.Errorf("authed %s: Referrer-Policy = %q, want %q", target, got, "no-referrer")
			}
			// A redirect means the session seam did not take effect, so the assertions above
			// would be vacuous — the CSP would be absent for the wrong reason.
			if rec.Code == http.StatusFound {
				t.Fatalf("%s redirected; the authed chain was never entered, so this test "+
					"proves nothing about the group middleware", target)
			}
		})
	}
}

// The unauthenticated entry points get the headers from a separate, explicit attachment
// (`sec` passed per-route). Those three registrations were unpinned for the same reason.
// /ui/login is the one page that handles a credential, so it is the worst one to leave bare.
func TestUISecurityHeaders_AttachedToLoginPages(t *testing.T) {
	e := echo.New()
	RegisterUIRoutes(e, nil, []byte(testCookieSecret))

	req := httptest.NewRequest(http.MethodGet, "/ui/login", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("/ui/login status %d, want 200 (it must render without a DB)", rec.Code)
	}
	if got := rec.Header().Get("Content-Security-Policy"); got != uiPageCSP {
		t.Errorf("/ui/login CSP = %q, want uiPageCSP — the credential-handling page must not "+
			"be the one page without a policy", got)
	}
	if got := rec.Header().Get("X-Content-Type-Options"); got != "nosniff" {
		t.Errorf("/ui/login nosniff = %q", got)
	}
}

// TestEmbedFrameJS_IsShippedAndReferenced covers the other mutation-verified gap: deleting
// internal/server/static/embedframe.js entirely left `go build ./...` and the whole suite
// green, because //go:embed static embeds a tree and a missing file inside it is not an
// error. The consequence is silent — layout.html.tmpl 404s the script and every embedded
// document on /ui/wi/:id and /ui/memories/:id stays at its 220px starting height with an
// inner scrollbar.
//
// Same shape as TestDiagramJS_LoadedOnBothSurfaces, which already models this for
// diagram.js; it simply was not extended when a second parent-side script arrived.
func TestEmbedFrameJS_IsShippedAndReferenced(t *testing.T) {
	js, err := staticFS.ReadFile("static/embedframe.js")
	if err != nil {
		t.Fatalf("static/embedframe.js must be embedded: %v", err)
	}
	layout, err := templateFS.ReadFile("templates/layout.html.tmpl")
	if err != nil {
		t.Fatalf("read layout template: %v", err)
	}
	if !strings.Contains(string(layout), "/ui/static/embedframe.js") {
		t.Error("layout.html.tmpl must load /ui/static/embedframe.js — without it no embedded " +
			"document is ever resized and long documents sit in a 220px scrolling box")
	}

	// The script is parent-side privileged code: it runs in the authed /ui origin and writes
	// to a frame's style. Assert the validation it is required to perform survives edits.
	// Source-level checks, because there is no JS runtime here — the behavioural half belongs
	// to the T7 browser checklist.
	src := string(js)
	for _, want := range []struct{ needle, why string }{
		{"'pf-annot-bridge'", "must check the protocol's source discriminator"},
		{"PROTOCOL_VERSION", "must pin the protocol version"},
		{"'height'", "must handle only the height message; the annotation types are aihub#245"},
		{"isFinite", "must reject NaN and Infinity, which are both typeof 'number'"},
		{"contentWindow === ", "must authenticate by ev.source identity, not by ev.origin — a " +
			"sandboxed srcdoc frame has an opaque origin and reports origin \"null\""},
		{"MAX_HEIGHT", "must clamp the reported height"},
		{"MIN_HEIGHT", "must clamp the reported height"},
	} {
		if !strings.Contains(src, want.needle) {
			t.Errorf("embedframe.js lost %q: it %s", want.needle, want.why)
		}
	}
	// The annotation half must stay unimplemented rather than half-implemented: a partial
	// handler drops reviewer comments silently. If these appear, aihub#245 has started and
	// this guard should move rather than be deleted.
	//
	// Matched against COMMENT-STRIPPED source, and on the dispatch shape rather than the bare
	// string. The first version of this check searched the raw file for "'highlight'" and
	// failed on the file's own comment explaining which types it deliberately does not
	// handle — the same false-positive class as searching markup for "allow-same-origin" and
	// hitting the bridge script's prose about it. Prose that names a thing is not an
	// implementation of it.
	code := stripJSLineComments(src)
	for _, absent := range []string{"highlight", "selected", "clear"} {
		if strings.Contains(code, "d.type === '"+absent+"'") ||
			strings.Contains(code, "data.type === '"+absent+"'") {
			t.Errorf("embedframe.js dispatches on %q — the annotation protocol belongs to "+
				"aihub#245 and must be wired together with its anchoring, not piecemeal", absent)
		}
	}
}

// stripJSLineComments removes // comments so source assertions match code, not prose.
// Deliberately simple: it is enough for this file, and a real JS parser here would be a
// dependency added to avoid one edge case that does not occur.
func stripJSLineComments(src string) string {
	var b strings.Builder
	for _, line := range strings.Split(src, "\n") {
		if i := strings.Index(line, "//"); i >= 0 {
			line = line[:i]
		}
		b.WriteString(line)
		b.WriteByte('\n')
	}
	return b.String()
}
