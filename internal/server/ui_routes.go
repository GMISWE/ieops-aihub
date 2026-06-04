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
	e.GET("/ui/login", handleUILoginGet(tmpl))
	e.POST("/ui/login", handleUILoginPost(pool, sm, tmpl))
	e.POST("/ui/logout", handleUILogout(sm))

	// Static assets — served from embedded FS, no auth. Wrapped so font/css/js
	// responses carry a Cache-Control header: without it every navigation
	// re-fetches the woff2 fonts and the browser re-runs the font swap, which is
	// the visible FOUT the reviewer saw on refresh (aihub#129 round-3 #1).
	staticHandler := http.StripPrefix("/ui/static/", http.FileServer(staticFSRoot()))
	e.GET("/ui/static/*", echo.WrapHandler(cacheStatic(staticHandler)))

	// Authed UI group. The peer subagents' register* functions attach to
	// this group so /ui/queue, /ui/wi, /ui/memories all share the session
	// middleware + the parsed template tree.
	uiGroup := e.Group("/ui", RequireUISession(sm, pool))
	registerUIQueueHandlers(uiGroup, pool, tmpl)
	registerUIWIHandlers(uiGroup, pool, tmpl)
	registerUIMemoryHandlers(uiGroup, pool, tmpl)

	// Mirror /v1/artifacts/:id/html under cookie auth so /ui/memories/<id>
	// spec/plan redirects and /ui/wi/<slug> artifact links work without a
	// Bearer token. Same handler — handleArtifactHTML is auth-agnostic, it
	// reads UserContext from echo.Context which RequireUISession populates
	// the same way BearerAuth does.
	uiGroup.GET("/artifacts/:id/html", handleArtifactHTML(pool))

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
