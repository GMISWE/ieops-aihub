package server

import (
	"net/http"
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

		title := mem.ID + " (" + mem.Type + ")"
		return c.HTMLBlob(http.StatusOK, []byte(renderArtifactBody(*mem.RenderedHTML, title)))
	}
}

// renderArtifactBody returns the HTML body to serve for a stored rendered_html
// value. A caller-supplied custom render (pf_save_artifact html=, aihub#104) may
// already be a complete standalone document — detected by a leading <!doctype or
// <html — and is served verbatim to avoid double-wrapping. Otherwise the stored
// value is a body fragment (the goldmark auto-render path), so it is wrapped in a
// standalone document to give the `polyforge artifact view` browser flow usable
// styling. The fragment is kept raw in the column so it can be embedded elsewhere.
func renderArtifactBody(stored, title string) string {
	lc := strings.ToLower(strings.TrimSpace(stored))
	if strings.HasPrefix(lc, "<!doctype") || strings.HasPrefix(lc, "<html") {
		return stored
	}
	return render.Document(stored, title)
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
