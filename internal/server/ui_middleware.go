package server

import (
	"net/http"
	"net/url"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/labstack/echo/v4"
)

// RequireUISession verifies the session cookie, rebuilds the UserContext, and
// stores it on the echo context. On any failure (no cookie, bad sig, expired,
// API key revoked) the request is redirected to /ui/login?next=<current-path>
// — never returning JSON, never 401-ing. Downstream UI handlers can therefore
// call GetUser(c) and rely on the same shape that BearerAuth provides.
func RequireUISession(sm *SessionManager, pool *pgxpool.Pool) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			cookie, err := c.Cookie(pfSessionCookieName)
			if err != nil || cookie == nil || cookie.Value == "" {
				return redirectToLogin(c)
			}
			_, apiKeyID, verr := sm.Verify(cookie.Value)
			if verr != nil {
				return redirectToLogin(c)
			}
			uc, lerr := loadUserByAPIKeyID(c.Request().Context(), pool, apiKeyID)
			if lerr != nil {
				return redirectToLogin(c)
			}
			c.Set(string(ctxUser), uc)
			return next(c)
		}
	}
}

// redirectToLogin sends the client back to /ui/login with the current path
// preserved as ?next= so we can land them where they were trying to go.
func redirectToLogin(c echo.Context) error {
	target := "/ui/login"
	req := c.Request()
	if req != nil && req.URL != nil {
		// Only preserve path+query; never propagate fragments or external URLs.
		next := req.URL.Path
		if req.URL.RawQuery != "" {
			next = next + "?" + req.URL.RawQuery
		}
		if isSafeNext(next) {
			target = target + "?next=" + url.QueryEscape(next)
		}
	}
	return c.Redirect(http.StatusFound, target)
}
