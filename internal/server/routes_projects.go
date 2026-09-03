package server

import (
	"net/http"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/labstack/echo/v4"

	"github.com/GMISWE/ieops-aihub/internal/domain"
)

// RegisterProjectRoutes adds all /v1/projects endpoints to the route group.
func RegisterProjectRoutes(v1 *echo.Group, pool *pgxpool.Pool) {
	v1.POST("/projects", handleCreateProject(pool))
	v1.GET("/projects", handleListProjects(pool))
	v1.GET("/projects/:name", handleGetProject(pool))
	v1.PATCH("/projects/:name", handleUpdateProject(pool))
	v1.POST("/projects/:name/rotate_identifier", handleRotateIdentifier(pool))
	v1.POST("/projects/:name/transfer_owner", handleTransferOwner(pool))
}

// callerToUserRecord converts a UserContext to a domain.UserRecord.
func callerToUserRecord(u *UserContext) *domain.UserRecord {
	return &domain.UserRecord{
		ID:           u.UserID,
		Role:         u.Role,
		ProjectScope: u.ProjectScope,
	}
}

// handleCreateProject handles POST /v1/projects.
func handleCreateProject(pool *pgxpool.Pool) echo.HandlerFunc {
	return func(c echo.Context) error {
		u := GetUser(c)
		if u == nil {
			return writeError(c, domain.NewErr(domain.ErrUnauthorized, "not authenticated"))
		}
		ctx, cancel := contextWithTimeout(c)
		defer cancel()

		var req domain.CreateProjectRequest
		if err := c.Bind(&req); err != nil {
			return writeError(c, domain.NewErr(domain.ErrBadRequest, "invalid request body"))
		}
		if req.Name == "" {
			return writeError(c, domain.NewErr(domain.ErrBadRequest, "name is required"))
		}

		p, aerr := domain.CreateProject(ctx, pool, callerToUserRecord(u), req)
		if aerr != nil {
			return domainErr(c, aerr)
		}
		return c.JSON(http.StatusCreated, p)
	}
}

// handleListProjects handles GET /v1/projects.
func handleListProjects(pool *pgxpool.Pool) echo.HandlerFunc {
	return func(c echo.Context) error {
		u := GetUser(c)
		if u == nil {
			return writeError(c, domain.NewErr(domain.ErrUnauthorized, "not authenticated"))
		}
		ctx, cancel := contextWithTimeout(c)
		defer cancel()

		projects, aerr := domain.ListProjects(ctx, pool, callerToUserRecord(u))
		if aerr != nil {
			return domainErr(c, aerr)
		}
		return c.JSON(http.StatusOK, map[string]any{"items": projects})
	}
}

// handleGetProject handles GET /v1/projects/:name.
func handleGetProject(pool *pgxpool.Pool) echo.HandlerFunc {
	return func(c echo.Context) error {
		u := GetUser(c)
		if u == nil {
			return writeError(c, domain.NewErr(domain.ErrUnauthorized, "not authenticated"))
		}
		ctx, cancel := contextWithTimeout(c)
		defer cancel()

		name := c.Param("name")
		if name == "" {
			return writeError(c, domain.NewErr(domain.ErrBadRequest, "name is required"))
		}

		// Read X-Project-Identifier header (bcrypt check for public access)
		identifier := c.Request().Header.Get("X-Project-Identifier")

		p, aerr := domain.GetProject(ctx, pool, name, callerToUserRecord(u), identifier)
		if aerr != nil {
			return domainErr(c, aerr)
		}
		return c.JSON(http.StatusOK, p)
	}
}

// handleUpdateProject handles PATCH /v1/projects/:name.
//
// aihub#260: the body binds into domain.UpdateProjectRequest, whose
// MembersVersion field carries the members compare-and-set precondition. There
// is no code here that names it — it rides the struct — and that is exactly the
// hop this repo has silently lost a parameter on before, so it gets its own
// assertion rather than being trusted: TestPatchProjectCarriesMembersVersion
// (internal/server/routes_projects_cas_db_test.go) drives a real HTTP PATCH and
// requires the 409, so a handler that dropped the field would go red here even
// though the domain function and the MCP schema were both correct.
//
// A failed precondition surfaces as 409 CONFLICT_CAS_FAILED through domainErr,
// with details.current_members_version — errorResponse copies AihubError.Details
// into the envelope, so the caller learns what to retry with.
//
// aihub#333 added a second precondition on the same field and the same hop:
// ExpectedRemovals, the user_ids a `members` write is allowed to drop. It rides
// the struct the same way, so it has the same assertion rather than the same
// trust — TestProjectMembersRemovalHTTPDeclaredShrinkSucceeds
// (internal/server/project_members_removal_db_test.go) drives a real PATCH and
// requires the 200, so a handler that dropped the field would go red there even
// though the domain check and the MCP schema were both correct. Note which half
// of the property that is: the REFUSAL is green whether or not the parameter
// arrives, because a dropped declaration refuses the write. Only the success
// half can see this hop.
//
// It surfaces as 412 PROJECT_MEMBERS_UNDECLARED_REMOVAL, deliberately not 409:
// re-sending the same short list can never succeed, so it must not land in a
// client's compare-and-set retry loop.
func handleUpdateProject(pool *pgxpool.Pool) echo.HandlerFunc {
	return func(c echo.Context) error {
		u := GetUser(c)
		if u == nil {
			return writeError(c, domain.NewErr(domain.ErrUnauthorized, "not authenticated"))
		}
		ctx, cancel := contextWithTimeout(c)
		defer cancel()

		name := c.Param("name")
		if name == "" {
			return writeError(c, domain.NewErr(domain.ErrBadRequest, "name is required"))
		}

		var req domain.UpdateProjectRequest
		if err := c.Bind(&req); err != nil {
			return writeError(c, domain.NewErr(domain.ErrBadRequest, "invalid request body"))
		}

		p, aerr := domain.UpdateProject(ctx, pool, name, callerToUserRecord(u), req)
		if aerr != nil {
			return domainErr(c, aerr)
		}
		return c.JSON(http.StatusOK, p)
	}
}

// handleRotateIdentifier handles POST /v1/projects/:name/rotate_identifier.
// Returns plain token once; plain is NEVER logged or stored.
func handleRotateIdentifier(pool *pgxpool.Pool) echo.HandlerFunc {
	return func(c echo.Context) error {
		u := GetUser(c)
		if u == nil {
			return writeError(c, domain.NewErr(domain.ErrUnauthorized, "not authenticated"))
		}
		ctx, cancel := contextWithTimeout(c)
		defer cancel()

		name := c.Param("name")
		if name == "" {
			return writeError(c, domain.NewErr(domain.ErrBadRequest, "name is required"))
		}

		plain, prefix, aerr := domain.RotateIdentifier(ctx, pool, name, callerToUserRecord(u))
		if aerr != nil {
			return domainErr(c, aerr)
		}
		// plain is returned exactly once to the caller; not logged, not stored
		return c.JSON(http.StatusOK, map[string]string{
			"plain":  plain,
			"prefix": prefix,
		})
	}
}

// handleTransferOwner handles POST /v1/projects/:name/transfer_owner.
func handleTransferOwner(pool *pgxpool.Pool) echo.HandlerFunc {
	return func(c echo.Context) error {
		u := GetUser(c)
		if u == nil {
			return writeError(c, domain.NewErr(domain.ErrUnauthorized, "not authenticated"))
		}
		ctx, cancel := contextWithTimeout(c)
		defer cancel()

		name := c.Param("name")
		if name == "" {
			return writeError(c, domain.NewErr(domain.ErrBadRequest, "name is required"))
		}

		var req struct {
			NewOwnerID string `json:"new_owner_id"`
		}
		if err := c.Bind(&req); err != nil {
			return writeError(c, domain.NewErr(domain.ErrBadRequest, "invalid request body"))
		}
		if req.NewOwnerID == "" {
			return writeError(c, domain.NewErr(domain.ErrBadRequest, "new_owner_id is required"))
		}

		if aerr := domain.TransferOwner(ctx, pool, name, req.NewOwnerID, callerToUserRecord(u)); aerr != nil {
			return domainErr(c, aerr)
		}
		return c.JSON(http.StatusOK, map[string]bool{"ok": true})
	}
}
