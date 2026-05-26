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
)

// RegisterUIRoutes wires the read-only /ui/* tree on the given echo instance.
//
// The cookieSecret is supplied by main.go (env-derived or random). Sessions
// are HMAC-SHA256 over (user_id|api_key_id|exp); see ui_session.go.
//
// Route map:
//
//   no-auth:
//     GET  /ui/                 -> 302 /ui/queue
//     GET  /ui/login            -> login form
//     POST /ui/login            -> issue cookie
//     POST /ui/logout           -> clear cookie
//     GET  /ui/static/*         -> embedded css + htmx
//
//   authed (RequireUISession):
//     GET  /ui/queue            -> queue overview (peer subagent)
//     GET  /ui/wi               -> wi list      (peer subagent)
//     GET  /ui/wi/:id           -> wi detail    (peer subagent)
//     GET  /ui/memories         -> memory index (peer subagent)
//     GET  /ui/memories/:id     -> memory view  (peer subagent)
func RegisterUIRoutes(e *echo.Echo, pool *pgxpool.Pool, cookieSecret []byte) {
	sm := NewSessionManager(cookieSecret)
	tmpl := parseTemplates()

	// No-auth pages.
	e.GET("/ui", func(c echo.Context) error {
		return c.Redirect(http.StatusFound, "/ui/queue")
	})
	e.GET("/ui/", func(c echo.Context) error {
		return c.Redirect(http.StatusFound, "/ui/queue")
	})
	e.GET("/ui/login", handleUILoginGet(tmpl))
	e.POST("/ui/login", handleUILoginPost(pool, sm, tmpl))
	e.POST("/ui/logout", handleUILogout(sm))

	// Static assets — served from embedded FS, no auth.
	staticHandler := http.StripPrefix("/ui/static/", http.FileServer(staticFSRoot()))
	e.GET("/ui/static/*", echo.WrapHandler(staticHandler))

	// Authed UI group. The peer subagents' register* functions attach to
	// this group so /ui/queue, /ui/wi, /ui/memories all share the session
	// middleware + the parsed template tree.
	uiGroup := e.Group("/ui", RequireUISession(sm, pool))
	registerUIQueueHandlers(uiGroup, pool, tmpl)
	registerUIWIHandlers(uiGroup, pool, tmpl)
	registerUIMemoryHandlers(uiGroup, pool, tmpl)
}
