package server

// Web UI route wiring.
//
// IMPORTANT — peer-subagent dependencies:
//
// This file registers /ui/* routes. The auth + foundations live here, but the
// queue / wi / memory page handlers are owned by sibling subagents and must
// provide three plain functions with these signatures (registered AFTER the
// session middleware, so the *UserContext is already on echo.Context):
//
//   registerUIQueueHandlers(g *echo.Group, pool *pgxpool.Pool, tmpl *template.Template)
//   registerUIWIHandlers(g *echo.Group, pool *pgxpool.Pool, tmpl *template.Template)
//   registerUIMemoryHandlers(g *echo.Group, pool *pgxpool.Pool, tmpl *template.Template)
//
// They live in ui_handlers_queue.go, ui_handlers_wi.go, ui_handlers_memory.go.
// Until those files exist this package will not build — that is intentional;
// the parent agent verifies the full build only after the peer subagents land.
// Do not stub these out in ui_routes.go.

import (
	"crypto/rand"
	"encoding/base64"
	"net/http"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/labstack/echo/v4"

	"github.com/GMISWE/ieops-aihub/internal/render"
)

// RegisterUIRoutes wires the read-only /ui/* tree on the given echo instance.
//
// The cookieSecret is supplied by main.go (env-derived or random). Sessions
// are HMAC-SHA256 over (user_id|api_key_id|exp); see ui_session.go.
//
// Route map:
//
//	no-auth:
//	  GET  /ui/                 -> 302 /ui/wi
//	  GET  /ui/login            -> login form
//	  POST /ui/login            -> issue cookie
//	  POST /ui/logout           -> clear cookie
//	  GET  /ui/static/*         -> embedded css + htmx
//
//	authed (RequireUISession):
//	  GET  /ui/queue            -> 302 /ui/wi (legacy; queue is embedded there)
//	  GET  /ui/wi               -> wi list      (peer subagent)
//	  GET  /ui/wi/:id           -> wi detail    (peer subagent)
//	  GET  /ui/memories         -> memory index (peer subagent)
//	  GET  /ui/memories/:id     -> memory view  (peer subagent)
//
// uiSecurityHeaders attaches the CSP (and nosniff) to every authed /ui response
// (aihub#240, resolves #144).
//
// It lives on the group rather than in individual handlers so that a page added later
// is covered by default. That matters here specifically: aihub#231 was exactly the
// failure of adding a second render path (the {{md}} memory/wi detail pages) and not
// wiring it into what the first one had. Opt-out-by-default beats opt-in when the thing
// being opted into is a security control.
//
// Static assets under /ui/static are registered outside this group and are unaffected.
func uiSecurityHeaders() echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			// One nonce per response, minted before the handler runs so everything
			// downstream — the inline scripts this package emits and the sandboxed frames
			// render builds — can read the same value off the context (aihub#243).
			nonce := newCSPNonce()
			c.Set(uiNonceKey, nonce)

			h := c.Response().Header()
			h.Set("Content-Security-Policy", uiPageCSPWithNonce(nonce))
			h.Set("X-Content-Type-Options", "nosniff")
			h.Set("Referrer-Policy", "no-referrer")
			return next(c)
		}
	}
}

// uiNonceKey is the echo-context key holding this response's CSP nonce.
//
// A context value rather than a parameter threaded through every handler: the nonce is
// per-RESPONSE, and the middleware that mints it is also the one that publishes it in the
// header, so the two cannot drift. A handler that forgets to read it emits a script without
// the attribute, which fails loudly in the browser console rather than silently widening the
// policy — the safe direction for this particular mistake.
const uiNonceKey = "pf_ui_csp_nonce"

// uiNonce returns the CSP nonce minted for this response by uiSecurityHeaders.
//
// Returns "" when called outside the /ui middleware chain. An empty nonce renders as
// script-src 'nonce-', which matches nothing, so inline scripts are refused rather than
// admitted — the failure is a dead theme setter, never an open policy.
func uiNonce(c echo.Context) string {
	n, _ := c.Get(uiNonceKey).(string)
	return n
}

// newCSPNonce mints 128 bits of crypto/rand, base64url-encoded.
//
// It must be unpredictable AND fresh per response: a constant or guessable value would let
// an attacker who can get markup onto the page name the nonce themselves, which is the whole
// protection aihub#243 bought. TestUISecurityHeaders_NonceIsFreshPerResponse pins both.
//
// Fails CLOSED: if the entropy source errors, the empty string yields a policy that admits
// no inline script at all rather than a guessable nonce that admits an attacker's. Same
// posture as render.newNonce, and rand.Read does not fail in practice.
func newCSPNonce() string {
	var buf [16]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return ""
	}
	return base64.RawURLEncoding.EncodeToString(buf[:])
}

func RegisterUIRoutes(e *echo.Echo, pool *pgxpool.Pool, cookieSecret []byte) {
	sm := NewSessionManager(cookieSecret)
	tmpl := parseTemplates()

	// No-auth pages. The landing pages point at the wi list, which now hosts
	// the ready queue as an embedded block (the standalone /ui/queue page is
	// gone — it 302s here too).
	e.GET("/", func(c echo.Context) error {
		return c.Redirect(http.StatusFound, "/ui/wi")
	})
	e.GET("/ui", func(c echo.Context) error {
		return c.Redirect(http.StatusFound, "/ui/wi")
	})
	e.GET("/ui/", func(c echo.Context) error {
		return c.Redirect(http.StatusFound, "/ui/wi")
	})
	// The unauthenticated entry points get the same headers as the authed group. They
	// render a form and set a session cookie, so leaving them without a CSP, nosniff or
	// referrer policy meant the one page that handles a credential was the one page with
	// no baseline hardening.
	sec := uiSecurityHeaders()
	e.GET("/ui/login", handleUILoginGet(tmpl), sec)
	e.POST("/ui/login", handleUILoginPost(pool, sm, tmpl), sec)
	e.POST("/ui/logout", handleUILogout(sm), sec)

	// Static assets — served from embedded FS, no auth. Wrapped so font/css/js
	// responses carry a Cache-Control header: without it every navigation
	// re-fetches the woff2 fonts and the browser re-runs the font swap, which is
	// the visible FOUT the reviewer saw on refresh (aihub#129 round-3 #1).
	staticHandler := http.StripPrefix("/ui/static/", http.FileServer(staticFSRoot()))
	e.GET("/ui/static/*", echo.WrapHandler(cacheStatic(staticHandler)))

	// Authed UI group. The peer subagents' register* functions attach to
	// this group so /ui/queue, /ui/wi, /ui/memories all share the session
	// middleware + the parsed template tree.
	// Order matters: uiSecurityHeaders() runs BEFORE RequireUISession, not after.
	//
	// Reversed, the session check short-circuits an unauthenticated request with a 302 before
	// the header middleware ever runs, so the login redirect — the one response in this group
	// that is guaranteed to reach an unauthenticated client — carried no CSP, no nosniff and
	// no Referrer-Policy. This ordering also makes the attachment observable without a
	// database, which is why TestUISecurityHeaders_AttachedToAuthedGroup can pin it at all;
	// the previous arrangement needed a seam in the identity-resolution path just to be
	// testable. Both middlewares are order-independent in effect (this one only writes
	// response headers), so nothing is traded for it.
	uiGroup := e.Group("/ui", uiSecurityHeaders(), RequireUISession(sm, pool))
	registerUIQueueHandlers(uiGroup, pool, tmpl)
	registerUIWIHandlers(uiGroup, pool, tmpl)
	registerUIMemoryHandlers(uiGroup, pool, tmpl)

	// Mirror /v1/artifacts/:id/html under cookie auth so /ui/memories/<id>
	// spec/plan redirects and /ui/wi/<slug> artifact links work without a
	// Bearer token. Same handler — handleArtifactHTML is auth-agnostic, it
	// reads UserContext from echo.Context which RequireUISession populates
	// the same way BearerAuth does.
	uiGroup.GET("/artifacts/:id/html", handleArtifactHTML(pool))

	// aihub#154: /ui mirror of /v1 share/unshare so the viewer Share button works
	// under cookie auth. Reuses the auth-agnostic handlers (GetUser + checkProjectAccess).
	uiGroup.POST("/artifacts/:id/share", handleShareArtifact(pool))
	uiGroup.DELETE("/artifacts/:id/share", handleUnshareArtifact(pool))

	// aihub#124: section-level annotation commit — /ui only (no /v1 mirror).
	RegisterUIArtifactCommitRoute(uiGroup, pool)
	// aihub#125: artifact-scoped reply + resolve — /ui only.
	RegisterUIArtifactReplyResolveRoutes(uiGroup, pool)
	// aihub#125: vendored annotation JS + glue script.
	// Note: static JS files are on the uiGroup (/ui prefix) but do not require
	// auth themselves — the RequireUISession middleware allows static GETs.
	// We call render.AnnotatorJS() / render.AnnotJS() to avoid a separate FS
	// handler; the render package owns the embed.
	for _, entry := range []struct {
		path        string
		data        []byte
		contentType string
	}{
		{"/static/annotator.js", render.AnnotatorJS(), "text/javascript; charset=utf-8"},
		{"/static/annot.js", render.AnnotJS(), "text/javascript; charset=utf-8"},
		// aihub#154: /ui-only artifact share-toggle glue.
		{"/static/share.js", render.ShareJS(), "text/javascript; charset=utf-8"},
		// aihub#138: /ui-only design-system override for the artifact viewer.
		// Served separately from the embedded static/ FS so the render package
		// owns both the embed and the exported bytes.
		{"/static/viewer.css", render.ViewerCSS(), "text/css; charset=utf-8"},
	} {
		d := entry.data
		ct := entry.contentType
		uiGroup.GET(entry.path, func(c echo.Context) error {
			c.Response().Header().Set("Cache-Control", "public, max-age=3600")
			return c.Blob(http.StatusOK, ct, d)
		})
	}
}
