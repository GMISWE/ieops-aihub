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
		ID:   u.UserID,
		Role: u.Role,
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
