// Package server provides the HTTP API server for aihub.
package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"sort"
	"strings"
	"sync"

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
	ProjectScope *string // nil = unscoped; else caller is confined to this project
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

// projectMember is one element of the projects.members JSONB array, as the
// authorization path needs to read it: who, and with what role.
type projectMember struct {
	UserID string `json:"user_id"`
	Role   string `json:"role"`
}

// roleForUserInMembers returns the role recorded for callerUserID in one project's
// members JSONB. found reports whether a membership element for that user existed;
// decodeErr is non-nil when at least one element of the array was malformed, and is
// for logging only — it never suppresses the elements that did decode.
//
// This is a function, not an inline block, for two reasons that are the whole
// point of aihub#315: the same derivation exists on the pf_whoami side
// (internal/mcp/tools_lifecycle.go, fixed in aihub#312) and drifted from this one,
// and a defect that only exists inline in BearerAuth cannot be exercised without a
// database — so the mutant lands where nothing can see it (aihub#309's lesson).
//
// The two properties it exists to hold, both of which the inline version got wrong:
//
//  1. A malformed element must not discard the rest of the array. encoding/json
//     does not fail a slice wholesale: it records the first *json.UnmarshalTypeError
//     and keeps decoding, so the elements on either side of a bad one are filled
//     correctly and only the bad one is left zero-valued. Measured, not assumed —
//     `[{u_a,writer}, 5, {u_b,viewer}]` decodes to `[{u_a,writer}, {"",""},
//     {u_b,viewer}]` together with a non-nil error. Bailing out on that error threw
//     away data that had already decoded, and the user-visible effect was the whole
//     project disappearing from ProjectRoles — an unexplained permission denial
//     whose cause was one dirty row in a possibly unrelated project. Keeping the
//     partial result is never worse than bailing out: when members is not an array
//     at all the error is a whole-value type error and the slice comes back nil,
//     which is exactly what the old `continue` produced.
//
//  2. The identity comparison must be guarded on a non-empty caller id. A malformed
//     element decodes to UserID == "", so an empty callerUserID would match it and
//     inherit whatever Role that element happened to carry — `{"user_id":5,
//     "role":"viewer"}` decodes to `{"", "viewer"}`, a live over-report. Fixing (1)
//     is what makes that reachable, which is why both halves land together
//     (aihub#312 hit the same coupling). callerUserID is empty only if a users row
//     has an empty id, which no production path produces today; that is an upstream
//     accident rather than a local guarantee, so it is asserted here instead of
//     being asserted in a comment.
func roleForUserInMembers(membersRaw []byte, callerUserID string) (role string, found bool, decodeErr error) {
	if callerUserID == "" {
		return "", false, nil
	}
	var members []projectMember
	// The error is captured rather than acted on: the partially decoded slice is
	// the useful result. See (1) above.
	decodeErr = json.Unmarshal(membersRaw, &members)
	for _, m := range members {
		if m.UserID == callerUserID {
			return m.Role, true, decodeErr
		}
	}
	return "", false, decodeErr
}

// malformedMembersWarned records which projects have already had a malformed
// members element reported, so the warning below is emitted once per project per
// process rather than once per request.
var malformedMembersWarned sync.Map

// warnMalformedMembersOnce reports a project whose members JSONB has a malformed
// element, at most once per project for the life of the process.
//
// Loud, because the failure this replaced was silent: the user saw a permission
// denial and nothing said the cause was a dirty row in some project's members
// array. Once, because BearerAuth runs this query on EVERY authenticated
// non-admin request — an unconditional write here would put a synchronous stderr
// write on the hot auth path, at request rate, for as long as the dirty row
// exists. A restart re-arms it, which is the right cadence for a condition an
// operator has to go and fix in the database.
func warnMalformedMembersOnce(projName string, decodeErr error) {
	if _, seen := malformedMembersWarned.LoadOrStore(projName, struct{}{}); seen {
		return
	}
	fmt.Fprintf(os.Stderr,
		"auth: project %q has a malformed members element (%v); the well-formed elements were still applied. "+
			"This is reported once per process; fix the row in projects.members.\n",
		projName, decodeErr)
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

			uc.ProjectScope = projectScope
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
						role, found, decodeErr := roleForUserInMembers(membersRaw, uc.UserID)
						if decodeErr != nil {
							warnMalformedMembersOnce(projName, decodeErr)
						}
						if !found {
							continue
						}
						// Respect project_scope on the API key if set.
						if projectScope == nil || *projectScope == projName {
							uc.ProjectRoles[projName] = role
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

// notVisibleMessage is THE wording for "you may not see this", whatever the
// reason, and it deliberately does not say which reason (aihub#377, invariant 1).
//
// One constant, not a local per handler. The owner rejected a "smarter" design
// that answered 403 when the caller had named the project themselves and 404
// when it was derived from an object they pointed at, and the reason he gave is
// the reason this is a constant too: leave no branch and no branch can drift.
// A second copy of this sentence is a second thing to keep in step, and the
// endpoints that must agree byte-for-byte are spread over five files.
const notVisibleMessage = "not found, or you do not have access — " +
	"contact the project owner or an administrator to check your invitation"

// errNotVisible builds the one response every "not yours to see" path returns:
// a 404 carrying notVisibleMessage.
//
// Callers must use it for BOTH halves of the pair — the "no such object" branch
// and the "object exists but you are not a member" branch. Returning it from
// only one of the two leaves the responses distinguishable, which is the whole
// defect: identical status with a different message body is still an oracle.
func errNotVisible() *domain.AihubError {
	return domain.NewErr(domain.ErrNotFound, notVisibleMessage)
}

// hideNotFound is the other half of the pair: it turns a loader's "no such row"
// into errNotVisible() and leaves every other error exactly as it was.
//
// 🔴 The pass-through is not laziness, it is the point. Collapsing the whole
// error to a 404 would also swallow a dropped connection or a statement timeout,
// and an endpoint that answers "not found" during an outage is a second lie —
// one that sends readers hunting for deleted data. Only ErrNotFound is a
// visibility verdict; ErrInternalError is an availability fact and stays a 500.
//
// Handlers pair it with checkProjectAccess: the loader's error goes through
// here, the membership verdict comes from there, and both arrive as the same
// bytes. Using it on one branch and not the other is the defect, not the fix.
func hideNotFound(aerr *domain.AihubError) *domain.AihubError {
	if aerr != nil && aerr.Code == domain.ErrNotFound {
		return errNotVisible()
	}
	return aerr
}

// visibleProjects is every project this caller may read, as a sorted slice.
//
// It exists for the handlers that must resolve a reference BEFORE they know which
// project to check — the shape aihub#376 was filed for. Those cannot call
// checkProjectAccess first (there is no project yet), so they bound the query
// instead, and this is the bound.
//
// Derived from hasProjectAccess rather than by walking ProjectRoles directly, so
// the set can never disagree with the per-project decision: ProjectScope
// confinement and the roleLevel comparison are both applied by that one predicate.
// Sorted because a map iterates in random order and this becomes a SQL bind arg.
//
// An admin gets nil, not "every project" — ProjectRoles is empty for admins by
// design (middleware.go's BearerAuth skips the membership query), so callers must
// pass the admin flag separately and let the query skip the clause. Returning nil
// here for an admin would otherwise read as "no projects".
func visibleProjects(u *UserContext) []string {
	if u == nil {
		return nil
	}
	out := make([]string, 0, len(u.ProjectRoles))
	for p := range u.ProjectRoles {
		if hasProjectAccess(u, p, "viewer") {
			out = append(out, p)
		}
	}
	sort.Strings(out)
	return out
}

// checkProjectAccess verifies the caller has at least minRole on the given project.
// Admin users bypass all project checks.
//
// On denial the error response is written to c AND a non-nil error is returned so
// that callers' "if err != nil { return err }" guard reliably stops execution before
// any subsequent database write. (Previously writeError returned nil on a successful
// JSON write, causing the caller to continue into the DB even after a 403 was sent.)
//
// aihub#377 changed WHICH denial this gives. Project membership is the visibility
// boundary, so a caller who is not a member gets errNotVisible() — a 404 that is
// byte-identical to the one a nonexistent object gets. This function is where that
// happens for all of its call sites at once, and that is the point: before this,
// the leaky helper was one line and the safe one (hasProjectAccess + a hand-built
// 404) was five, so the leaky one was used 41 times against 4. Making the one-line
// call the correct call is the only thing that stops the ratio growing back.
func checkProjectAccess(c echo.Context, u *UserContext, project, minRole string) error {
	if u == nil {
		// Authentication, not visibility: there is no project in the answer to leak.
		ae := domain.NewErr(domain.ErrUnauthorized, "not authenticated")
		writeError(c, ae) //nolint:errcheck // response committed; return ae below
		return ae
	}
	if u.ProjectScope != nil && *u.ProjectScope != project {
		ae := errNotVisible()
		writeError(c, ae) //nolint:errcheck
		return ae
	}
	if u.Role == "admin" {
		return nil
	}
	if project == "" {
		// Malformed request, not a visibility verdict — no project was named, so
		// nothing can be disclosed about one. Stays a 400.
		ae := domain.NewErr(domain.ErrBadRequest, "project is required")
		writeError(c, ae) //nolint:errcheck
		return ae
	}
	userRole, ok := u.ProjectRoles[project]
	// Compared through roleLevel rather than tested for non-emptiness, matching
	// handleListWorkItems' ids= branch (router.go): roleLevel maps an unrecognised
	// string to 0, and migration 0013_backfill_projects.sql copied arbitrary legacy
	// role values in. A role that does not reach viewer is not a membership, so it
	// must not be told the project exists either.
	if !ok || userRole == "" || roleLevel[userRole] < roleLevel["viewer"] {
		ae := errNotVisible()
		writeError(c, ae) //nolint:errcheck
		return ae
	}
	// 🔴 This branch DELIBERATELY keeps its 403 and its explanatory message. Do not
	// "finish the job" by folding it into errNotVisible().
	//
	// aihub#377's invariant has a positive half that is easy to read past: "a user
	// who IS in a project can see everything about that project". The caller here
	// is a member — they are merely short of the role this endpoint needs. Turning
	// that into a 404 would hide the project from its own members, which is not
	// closing the leak, it is switching the feature off.
	//
	// It is also this change's ONLY built-in positive control. "Every denial is a
	// 404" can be satisfied by breaking authorization entirely, and this is the one
	// branch that goes red when someone does. TestCreateWorkItem_ViewerGets403BeforeDBWrite
	// and TestRemember_ViewerGets403BeforeDBWrite (router_auth_test.go) pin it, and
	// TestProjectVisibility_InsufficientRoleStillExplains (project_visibility_gate_test.go)
	// states the reasoning next to the gate that would otherwise invite removing it.
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
