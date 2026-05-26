package server

import (
	"context"
	"encoding/json"
	"html/template"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/labstack/echo/v4"

	"github.com/GMISWE/ieops-aihub/internal/auth"
)

// loginPageData is the template context for login.html.tmpl.
type loginPageData struct {
	Next  string
	Error string
}

// handleUILoginGet renders the login page.
func handleUILoginGet(tmpl *template.Template) echo.HandlerFunc {
	return func(c echo.Context) error {
		next := c.QueryParam("next")
		if !isSafeNext(next) {
			next = "/ui/queue"
		}
		return renderTemplate(c, tmpl, "login.html.tmpl", loginPageData{Next: next})
	}
}

// handleUILoginPost validates an API key from the form and issues a session cookie.
//
// We accept the same raw API key string that BearerAuth accepts on the
// Authorization header — the user pastes their existing key once per browser
// and gets a 7-day signed cookie. On failure we re-render login.html with an
// error message; no DB tracing or backoff yet (MVP).
func handleUILoginPost(pool *pgxpool.Pool, sm *SessionManager, tmpl *template.Template) echo.HandlerFunc {
	return func(c echo.Context) error {
		next := c.FormValue("next")
		if !isSafeNext(next) {
			next = "/ui/queue"
		}
		rawKey := strings.TrimSpace(c.FormValue("api_key"))
		if rawKey == "" {
			return renderTemplate(c, tmpl, "login.html.tmpl", loginPageData{
				Next:  next,
				Error: "API key is required.",
			})
		}

		ctx, cancel := context.WithTimeout(c.Request().Context(), 10*time.Second)
		defer cancel()

		userID, apiKeyID, ok := lookupAPIKey(ctx, pool, auth.HashKey(rawKey))
		if !ok {
			return renderTemplate(c, tmpl, "login.html.tmpl", loginPageData{
				Next:  next,
				Error: "Invalid or revoked API key.",
			})
		}

		token := sm.Sign(userID, apiKeyID, pfSessionTTL)
		c.SetCookie(&http.Cookie{
			Name:     pfSessionCookieName,
			Value:    token,
			Path:     "/",
			Expires:  time.Now().Add(pfSessionTTL),
			HttpOnly: true,
			SameSite: http.SameSiteLaxMode,
			Secure:   isTLS(c),
		})
		return c.Redirect(http.StatusFound, next)
	}
}

// handleUILogout clears the session cookie and redirects to the login page.
func handleUILogout(_ *SessionManager) echo.HandlerFunc {
	return func(c echo.Context) error {
		c.SetCookie(&http.Cookie{
			Name:     pfSessionCookieName,
			Value:    "",
			Path:     "/",
			Expires:  time.Unix(0, 0),
			MaxAge:   -1,
			HttpOnly: true,
			SameSite: http.SameSiteLaxMode,
			Secure:   isTLS(c),
		})
		return c.Redirect(http.StatusFound, "/ui/login")
	}
}

// lookupAPIKey resolves a hashed API key to (user_id, api_key_id). Mirrors the
// query used by BearerAuth so a session cookie is exactly as strong as the
// bearer token it was minted from.
func lookupAPIKey(ctx context.Context, pool *pgxpool.Pool, keyHash string) (userID, apiKeyID string, ok bool) {
	rows, err := pool.Query(ctx, `
		SELECT u.id, k->>'id' as key_id, k->>'revoked_at' as revoked_at
		FROM users u,
		     jsonb_array_elements(u.api_keys) AS k
		WHERE k->>'key_hash' = $1
		  AND (k->>'revoked_at') IS NULL`,
		keyHash,
	)
	if err != nil {
		return "", "", false
	}
	defer rows.Close()
	if !rows.Next() {
		return "", "", false
	}
	var revokedAt *string
	if err := rows.Scan(&userID, &apiKeyID, &revokedAt); err != nil {
		return "", "", false
	}
	if revokedAt != nil {
		return "", "", false
	}
	return userID, apiKeyID, true
}

// loadUserByAPIKeyID rebuilds the same UserContext that BearerAuth would
// produce, given an api_key_id (the cookie carries the id, not the raw key).
// Used by the UI middleware.
func loadUserByAPIKeyID(ctx context.Context, pool *pgxpool.Pool, apiKeyID string) (*UserContext, error) {
	rows, err := pool.Query(ctx, `
		SELECT u.id, u.email, u.display_name, u.user_type, u.role,
		       k->>'project_scope' as project_scope, k->>'revoked_at' as revoked_at
		FROM users u,
		     jsonb_array_elements(u.api_keys) AS k
		WHERE k->>'id' = $1`,
		apiKeyID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	if !rows.Next() {
		return nil, errSessionBadPayload
	}
	var uc UserContext
	var projectScope *string
	var revokedAt *string
	if err := rows.Scan(&uc.UserID, &uc.Email, &uc.DisplayName, &uc.UserType, &uc.Role,
		&projectScope, &revokedAt); err != nil {
		return nil, err
	}
	rows.Close()
	if revokedAt != nil {
		return nil, errSessionExpired
	}
	uc.APIKeyID = apiKeyID
	uc.ProjectRoles = make(map[string]string)
	if uc.Role != "admin" {
		prows, perr := pool.Query(ctx, `
			SELECT name, members
			FROM projects
			WHERE members @> jsonb_build_array(jsonb_build_object('user_id', $1::text))`,
			uc.UserID,
		)
		if perr == nil {
			for prows.Next() {
				var projName string
				var membersRaw []byte
				if err := prows.Scan(&projName, &membersRaw); err != nil {
					continue
				}
				var members []struct {
					UserID string `json:"user_id"`
					Role   string `json:"role"`
				}
				if json.Unmarshal(membersRaw, &members) != nil {
					continue
				}
				for _, m := range members {
					if m.UserID == uc.UserID {
						if projectScope == nil || *projectScope == projName {
							uc.ProjectRoles[projName] = m.Role
						}
						break
					}
				}
			}
			prows.Close()
		}
	}
	return &uc, nil
}

// isSafeNext rejects open-redirect targets — only same-site /ui/* paths.
func isSafeNext(next string) bool {
	if next == "" {
		return false
	}
	if !strings.HasPrefix(next, "/ui/") && next != "/ui" {
		return false
	}
	// Disallow scheme/auth segments smuggled into the path.
	if strings.Contains(next, "//") || strings.Contains(next, "\\") {
		return false
	}
	return true
}

// isTLS detects whether the inbound request reached us over HTTPS, so the
// cookie can be marked Secure in prod while still working over plain HTTP
// in local dev (where Secure cookies are silently dropped).
func isTLS(c echo.Context) bool {
	r := c.Request()
	if r.TLS != nil {
		return true
	}
	if proto := r.Header.Get("X-Forwarded-Proto"); proto != "" {
		return strings.EqualFold(proto, "https")
	}
	return false
}
