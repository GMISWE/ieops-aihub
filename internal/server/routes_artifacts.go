package server

import (
	"context"
	"net/http"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/labstack/echo/v4"

	"github.com/GMISWE/ieops-aihub/internal/domain"
	"github.com/GMISWE/ieops-aihub/internal/render"
)

// RegisterArtifactRoutes wires the spec/plan HTML viewer endpoint
// (aihub#27 / IEBE-1694).
//
// Path note: Echo's path-param parser treats a literal ".html" suffix as part
// of the param value, which makes `/v1/artifacts/:id.html` ambiguous and
// unreliable. We use a trailing `/html` path segment instead — see
// TestArtifactHTML_RouteParamPlain below.
func RegisterArtifactRoutes(v1 *echo.Group, pool *pgxpool.Pool) {
	v1.GET("/artifacts/:id/html", handleArtifactHTML(pool))
	v1.POST("/artifacts/:id/share", handleShareArtifact(pool))
	v1.DELETE("/artifacts/:id/share", handleUnshareArtifact(pool))
}

// memVisibilitySetterFn mirrors domain.SetMemoryVisibility so the share/unshare
// handlers can be unit-tested without a DB, the same way loadMemoryFn wraps
// domain.GetMemoryByID (ui_handlers_memory.go). Production wiring is identical.
type memVisibilitySetterFn func(ctx context.Context, pool *pgxpool.Pool, id, visibility string) *domain.AihubError

// setMemoryVisibilityFn is the production-wired SetMemoryVisibility — swappable in tests.
var setMemoryVisibilityFn memVisibilitySetterFn = domain.SetMemoryVisibility

// handleArtifactHTML serves the cached rendered HTML for a spec/plan artifact.
//
// Status map:
//   - 401 — handled upstream by BearerAuth
//   - 404 — memory missing / redacted
//   - 403 — visibility denies the caller (private→non-author, admin→non-admin)
//   - 404 — rendered_html IS NULL (non spec/plan, or pre-feature legacy row)
//   - 200 — body = stored HTML, Content-Type: text/html; charset=utf-8
func handleArtifactHTML(pool *pgxpool.Pool) echo.HandlerFunc {
	return func(c echo.Context) error {
		u := GetUser(c)
		ctx, cancel := contextWithTimeout(c)
		defer cancel()

		memID := c.Param("id")
		if memID == "" {
			return writeError(c, domain.NewErr(domain.ErrBadRequest, "memory id is required"))
		}

		mem, aihubErr := domain.GetMemoryByID(ctx, pool, memID)
		if aihubErr != nil {
			return writeError(c, aihubErr)
		}

		// Project-level access (viewer minimum) + per-memory visibility check.
		if err := checkProjectAccess(c, u, mem.Project, "viewer"); err != nil {
			return err
		}
		if err := checkMemoryVisibility(c, u, mem); err != nil {
			return err
		}

		if mem.RenderedHTML == nil {
			// Distinguish from "memory not found" with a message — the row exists,
			// it just has no HTML payload (legacy spec/plan or unsupported type).
			return writeError(c, domain.NewErr(domain.ErrNotFound,
				"no HTML available for this artifact (rendered_html is NULL — only methodology.spec / methodology.plan render, and legacy rows are not backfilled)"))
		}

		// Wrap the stored body fragment in a standalone HTML document so the
		// `polyforge artifact view` browser flow gets usable styling without
		// extra setup. The fragment in `memories.rendered_html` is kept raw so
		// it can be embedded in other contexts (future webui, etc.) later.
		title := mem.ID + " (" + mem.Type + ")"
		return c.HTMLBlob(http.StatusOK, []byte(render.Document(*mem.RenderedHTML, title)))
	}
}

// handleSharedArtifact serves a publicly-shared artifact's rendered HTML with NO auth.
// The memory_id is itself the unguessable share link. Only memories with
// visibility='public' and non-null rendered_html are reachable; anything else returns a
// uniform 404 so the endpoint never leaks whether a given id exists.
func handleSharedArtifact(pool *pgxpool.Pool) echo.HandlerFunc {
	return func(c echo.Context) error {
		ctx, cancel := contextWithTimeout(c)
		defer cancel()

		mem, aihubErr := loadMemoryFn(ctx, pool, c.Param("id"))
		if aihubErr != nil || mem == nil || mem.Visibility != "public" || mem.RenderedHTML == nil {
			return writeError(c, domain.NewErr(domain.ErrNotFound, "not found"))
		}
		// rendered_html is produced with raw-HTML passthrough (render.Markdown uses
		// goldmark's unsafe renderer) and we now serve it to anonymous viewers, so a
		// malicious artifact author could embed <script>/onerror handlers. Lock the
		// public response down: a strict CSP blocks script execution and any external
		// fetch/form, and nosniff prevents content-type confusion. The authed /v1 path
		// keeps the original (trusted, project-member-only) behavior.
		h := c.Response().Header()
		h.Set("Content-Security-Policy",
			"default-src 'none'; style-src 'unsafe-inline'; img-src data: https:; "+
				"form-action 'none'; base-uri 'none'; frame-ancestors 'none'")
		h.Set("X-Content-Type-Options", "nosniff")
		title := mem.ID + " (" + mem.Type + ")"
		return c.HTMLBlob(http.StatusOK, []byte(render.Document(*mem.RenderedHTML, title)))
	}
}

// handleShareArtifact marks a spec/plan artifact public so it can be viewed without auth
// at /share/:id. Requires writer on the artifact's project; only artifacts that have
// rendered_html can be shared (412 otherwise — there is no 422 in this codebase).
func handleShareArtifact(pool *pgxpool.Pool) echo.HandlerFunc {
	return func(c echo.Context) error {
		u := GetUser(c)
		ctx, cancel := contextWithTimeout(c)
		defer cancel()

		mem, aihubErr := loadMemoryFn(ctx, pool, c.Param("id"))
		if aihubErr != nil {
			return writeError(c, aihubErr)
		}
		if err := checkProjectAccess(c, u, mem.Project, "writer"); err != nil {
			return err
		}
		if mem.RenderedHTML == nil {
			return writeError(c, domain.NewErr(domain.ErrPreconditionFailed,
				"artifact has no rendered HTML to share (only methodology.spec / methodology.plan render)"))
		}
		if aihubErr := setMemoryVisibilityFn(ctx, pool, mem.ID, "public"); aihubErr != nil {
			return writeError(c, aihubErr)
		}
		shareURL := c.Scheme() + "://" + c.Request().Host + "/share/" + mem.ID
		return c.JSON(http.StatusOK, map[string]any{
			"memory_id":  mem.ID,
			"share_url":  shareURL,
			"visibility": "public",
		})
	}
}

// handleUnshareArtifact revokes public sharing by resetting visibility to project.
// Same id is 404 on /share/:id immediately afterwards. Requires writer.
func handleUnshareArtifact(pool *pgxpool.Pool) echo.HandlerFunc {
	return func(c echo.Context) error {
		u := GetUser(c)
		ctx, cancel := contextWithTimeout(c)
		defer cancel()

		mem, aihubErr := loadMemoryFn(ctx, pool, c.Param("id"))
		if aihubErr != nil {
			return writeError(c, aihubErr)
		}
		if err := checkProjectAccess(c, u, mem.Project, "writer"); err != nil {
			return err
		}
		if aihubErr := setMemoryVisibilityFn(ctx, pool, mem.ID, "project"); aihubErr != nil {
			return writeError(c, aihubErr)
		}
		return c.JSON(http.StatusOK, map[string]bool{"ok": true})
	}
}

// checkMemoryVisibility enforces the per-row visibility rules that recall
// applies inline (memory.go ~L412-417). Extracted so handleArtifactHTML can
// reuse the exact same policy.
//
//   - visibility='private' → only the author can read
//   - visibility='admin'   → only global admin role
//   - visibility='project' / 'team' → relies on the upstream project access check
func checkMemoryVisibility(c echo.Context, u *UserContext, mem *domain.Memory) error {
	if u == nil {
		ae := domain.NewErr(domain.ErrUnauthorized, "not authenticated")
		writeError(c, ae) //nolint:errcheck
		return ae
	}
	// Admin bypasses both visibility tiers.
	if u.Role == "admin" {
		return nil
	}
	switch mem.Visibility {
	case "private":
		if mem.AuthorUserID != u.UserID {
			ae := domain.NewErr(domain.ErrForbidden,
				"this memory is private to its author")
			writeError(c, ae) //nolint:errcheck
			return ae
		}
	case "admin":
		ae := domain.NewErr(domain.ErrForbidden,
			"this memory requires admin role")
		writeError(c, ae) //nolint:errcheck
		return ae
	}
	return nil
}
