package server

import (
	"net/http"
	"net/url"
	"strings"

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
}

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
		// Only the cookie-authed /ui mirror gets a "Back to work item" nav;
		// the /v1 (Bearer/CLI) route stays a pure content document. echo's
		// c.Path() returns the registered route pattern, so it is
		// "/ui/artifacts/:id/html" for the UI route and
		// "/v1/artifacts/:id/html" for the API route.
		backHref := artifactBackHref(c.Path(), mem.WorkItemID)
		return c.HTMLBlob(http.StatusOK, []byte(render.Document(*mem.RenderedHTML, title, backHref)))
	}
}

// artifactBackHref returns the wi detail URL for the standalone artifact
// document's back-link, or "" when no nav should be emitted. A nav is only
// added for the /ui (cookie/webui) route and only when the artifact is tied to
// a work item. The /v1 (Bearer/CLI) route always gets "" so its document stays
// a pure content view.
func artifactBackHref(routePath string, workItemID *string) string {
	if strings.HasPrefix(routePath, "/ui") && workItemID != nil {
		return "/ui/wi/" + url.PathEscape(*workItemID)
	}
	return ""
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
