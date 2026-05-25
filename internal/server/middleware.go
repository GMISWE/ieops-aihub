// Package server provides the HTTP API server for aihub.
package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/labstack/echo/v4"

	"github.com/GMISWE/ieops-aihub/internal/auth"
	"github.com/GMISWE/ieops-aihub/internal/domain"
)

// ctxKey is a type for context keys in this package.
type ctxKey string

const (
	ctxUser ctxKey = "user"
)

// UserContext holds the authenticated user info.
type UserContext struct {
	UserID       string
	Email        string
	DisplayName  string
	UserType     string
	Role         string            // "writer" | "admin"
	ProjectRoles map[string]string // project → "viewer" | "writer" | "maintainer"
	APIKeyID     string
}

// GetUser retrieves the authenticated user from echo context.
func GetUser(c echo.Context) *UserContext {
	v := c.Get(string(ctxUser))
	if v == nil {
		return nil
	}
	u, _ := v.(*UserContext)
	return u
}

// BearerAuth validates the Authorization: Bearer <key> header and sets the user in context.
func BearerAuth(pool *pgxpool.Pool) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			header := c.Request().Header.Get("Authorization")
			if header == "" {
				return c.JSON(http.StatusUnauthorized, errorResponse(domain.NewErr(domain.ErrUnauthorized, "missing Authorization header")))
			}

			raw, ok := strings.CutPrefix(header, "Bearer ")
			if !ok {
				return c.JSON(http.StatusUnauthorized, errorResponse(domain.NewErr(domain.ErrUnauthorized, "Authorization header must use Bearer scheme")))
			}

			keyHash := auth.HashKey(raw)

			// Query users by iterating api_keys JSONB
			// We use a subquery that unnests api_keys and matches by key_hash
			rows, err := pool.Query(c.Request().Context(), `
				SELECT u.id, u.email, u.display_name, u.user_type, u.role,
				       k->>'id' as key_id, k->>'project_scope' as project_scope, k->>'revoked_at' as revoked_at
				FROM users u,
				     jsonb_array_elements(u.api_keys) AS k
				WHERE k->>'key_hash' = $1
				  AND (k->>'revoked_at') IS NULL`,
				keyHash,
			)
			if err != nil {
				return c.JSON(http.StatusInternalServerError, errorResponse(domain.NewErr(domain.ErrInternalError, "database error during auth")))
			}
			defer rows.Close()

			if !rows.Next() {
				return c.JSON(http.StatusUnauthorized, errorResponse(domain.NewErr(domain.ErrUnauthorized, "invalid or revoked API key")))
			}

			var uc UserContext
			var projectScope *string
			var revokedAt *string

			if err := rows.Scan(&uc.UserID, &uc.Email, &uc.DisplayName, &uc.UserType, &uc.Role,
				&uc.APIKeyID, &projectScope, &revokedAt); err != nil {
				return c.JSON(http.StatusInternalServerError, errorResponse(domain.NewErr(domain.ErrInternalError, "failed to scan user")))
			}
			rows.Close()

			if revokedAt != nil {
				return c.JSON(http.StatusUnauthorized, errorResponse(domain.NewErr(domain.ErrUnauthorized, "API key has been revoked")))
			}

			uc.ProjectRoles = make(map[string]string)

			// Non-admin users: load project memberships from projects.members JSONB.
			// Admin users bypass all project checks so we skip the extra query.
			if uc.Role != "admin" {
				prows, perr := pool.Query(c.Request().Context(), `
					SELECT name, members
					FROM projects
					WHERE members @> jsonb_build_array(jsonb_build_object('user_id', $1::text))`,
					uc.UserID,
				)
				if perr == nil {
					for prows.Next() {
						var projName string
						var membersRaw []byte
						if perr := prows.Scan(&projName, &membersRaw); perr != nil {
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
								// Respect project_scope on the API key if set.
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

			c.Set(string(ctxUser), &uc)
			return next(c)
		}
	}
}

// RequireAdmin returns a middleware that rejects non-admin users.
func RequireAdmin() echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			u := GetUser(c)
			if u == nil || u.Role != "admin" {
				return c.JSON(http.StatusForbidden, errorResponse(domain.NewErr(domain.ErrForbidden, "admin role required")))
			}
			return next(c)
		}
	}
}

// RequireProjectRole returns a middleware that requires the user to have at least the given role
// for the project specified in the request.
func RequireProjectRole(minRole string) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			u := GetUser(c)
			if u == nil {
				return c.JSON(http.StatusUnauthorized, errorResponse(domain.NewErr(domain.ErrUnauthorized, "not authenticated")))
			}
			if u.Role == "admin" {
				return next(c) // admin bypasses all project role checks
			}
			// Project comes from query or body; enforcement happens in domain layer
			return next(c)
		}
	}
}

// roleLevel maps role string to integer for comparison.
var roleLevel = map[string]int{
	"viewer":     1,
	"writer":     2,
	"maintainer": 3,
}

// checkProjectAccess verifies the caller has at least minRole on the given project.
// Admin users bypass all project checks.
//
// On denial the error response is written to c AND a non-nil error is returned so
// that callers' "if err != nil { return err }" guard reliably stops execution before
// any subsequent database write. (Previously writeError returned nil on a successful
// JSON write, causing the caller to continue into the DB even after a 403 was sent.)
func checkProjectAccess(c echo.Context, u *UserContext, project, minRole string) error {
	if u == nil {
		ae := domain.NewErr(domain.ErrUnauthorized, "not authenticated")
		writeError(c, ae) //nolint:errcheck — response committed; return ae below
		return ae
	}
	if u.Role == "admin" {
		return nil
	}
	if project == "" {
		ae := domain.NewErr(domain.ErrBadRequest, "project is required")
		writeError(c, ae) //nolint:errcheck
		return ae
	}
	userRole, ok := u.ProjectRoles[project]
	if !ok || userRole == "" {
		ae := domain.NewErr(domain.ErrForbidden,
			fmt.Sprintf("no access to project %q", project))
		writeError(c, ae) //nolint:errcheck
		return ae
	}
	if roleLevel[userRole] < roleLevel[minRole] {
		ae := domain.NewErr(domain.ErrForbidden,
			fmt.Sprintf("project %q requires %s role, you have %s", project, minRole, userRole))
		writeError(c, ae) //nolint:errcheck
		return ae
	}
	return nil
}

// errorResponse wraps an AihubError for JSON encoding.
func errorResponse(e *domain.AihubError) map[string]any {
	resp := map[string]any{
		"code":    e.Code,
		"message": e.Message,
	}
	if e.Details != nil {
		resp["details"] = e.Details
	}
	return resp
}

// writeError writes an AihubError as JSON response.
func writeError(c echo.Context, e *domain.AihubError) error {
	status := e.HTTPStatus
	if status == 0 {
		status = http.StatusInternalServerError
	}
	return c.JSON(status, errorResponse(e))
}

// internalError writes an HTTP 500 error.
func internalError(c echo.Context, msg string) error {
	return writeError(c, domain.NewErr(domain.ErrInternalError, msg))
}

// RequestID adds X-Request-ID to each request.
func RequestID() echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			reqID := c.Request().Header.Get("X-Request-ID")
			if reqID == "" {
				reqID = domain.NewBase62(12)
			}
			c.Response().Header().Set("X-Request-ID", reqID)
			c.Set("request_id", reqID)
			return next(c)
		}
	}
}

// GetProjectFromRequest extracts the project parameter from the request.
// Tries: query param ?project=, then request body (not parsed here).
func GetProjectFromRequest(c echo.Context) string {
	return c.QueryParam("project")
}

// Recovery returns a simple panic-recovery middleware.
func Recovery() echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) (err error) {
			defer func() {
				if r := recover(); r != nil {
					err = c.JSON(http.StatusInternalServerError, errorResponse(
						domain.NewErr(domain.ErrInternalError, "internal server error"),
					))
				}
			}()
			return next(c)
		}
	}
}

// contextWithTimeout applies a timeout to the request context (utility).
func contextWithTimeout(c echo.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(c.Request().Context(), 30*1e9) // 30s
}
