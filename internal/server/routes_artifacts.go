package server

import (
	"context"
	"encoding/json"
	"fmt"
	"html"
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
	v1.POST("/artifacts/:id/share", handleShareArtifact(pool))
	v1.DELETE("/artifacts/:id/share", handleUnshareArtifact(pool))
}

// memVisibilitySetterFn mirrors domain.SetMemoryVisibility so the share/unshare
// handlers can be unit-tested without a DB, the same way loadMemoryFn wraps
// domain.GetMemoryByID (ui_handlers_memory.go). Production wiring is identical.
type memVisibilitySetterFn func(ctx context.Context, pool *pgxpool.Pool, id, visibility string) *domain.AihubError

// setMemoryVisibilityFn is the production-wired SetMemoryVisibility — swappable in tests.
var setMemoryVisibilityFn memVisibilitySetterFn = domain.SetMemoryVisibility

// handleArtifactHTML serves the rendered HTML for any artifact.
//
// Status map:
//   - 401 — handled upstream by BearerAuth
//   - 404 — memory missing / redacted
//   - 403 — visibility denies the caller (private→non-author, admin→non-admin)
//   - 200 — body = HTML document, Content-Type: text/html; charset=utf-8
//
// When rendered_html is NULL the handler lazy-renders on the fly (aihub#81):
//  1. Try render.Markdown(mem.Content) — produces a rich HTML fragment.
//  2. If that fails or content is empty — fallback: serve content in a <pre>.
//
// No DB write is performed on a GET; render-on-view only.
func handleArtifactHTML(pool *pgxpool.Pool) echo.HandlerFunc {
	return func(c echo.Context) error {
		u := GetUser(c)
		ctx, cancel := contextWithTimeout(c)
		defer cancel()

		memID := c.Param("id")
		if memID == "" {
			return writeError(c, domain.NewErr(domain.ErrBadRequest, "memory id is required"))
		}

		mem, aihubErr := loadMemoryFn(ctx, pool, memID)
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

		// Resolve the HTML body fragment to serve. Prefer the stored rendered_html;
		// if NULL, lazy-render on the fly so no renderable artifact ever 404s.
		var bodyFragment string
		if mem.RenderedHTML != nil {
			bodyFragment = *mem.RenderedHTML
		} else {
			// Lazy-render: try goldmark first, fall back to a <pre> block.
			if mem.Content != "" {
				rendered, rerr := render.Markdown(mem.Content)
				if rerr == nil && rendered != "" {
					bodyFragment = rendered
				} else {
					// render error or empty output: safe <pre> fallback.
					bodyFragment = fmt.Sprintf("<pre>%s</pre>", html.EscapeString(mem.Content))
				}
			} else {
				// Empty content: serve a minimal placeholder.
				bodyFragment = "<pre></pre>"
			}
		}

		title := mem.ID + " (" + mem.Type + ")"
		// Only the cookie-authed /ui mirror gets a "Back to work item" nav; the
		// /v1 (Bearer/CLI) route stays a pure content document. echo's c.Path()
		// returns the registered route pattern, so it is "/ui/artifacts/:id/html"
		// for the UI route and "/v1/artifacts/:id/html" for the API route.
		backHref := artifactBackHref(c.Path(), mem.WorkItemID)

		// Build owning-wi href and related-memory refs for the /ui metadata header.
		// These are only injected for /ui routes (backHref != "" is a reliable proxy
		// since artifactBackHref only returns non-empty for /ui + non-nil WorkItemID,
		// but we check the path prefix directly for clarity).
		ownerHref, ownerLabel, related := "", "", []render.RelatedRef(nil)
		if strings.HasPrefix(c.Path(), "/ui") {
			if mem.WorkItemID != nil {
				ownerHref = wiHref(*mem.WorkItemID)
				ownerLabel = *mem.WorkItemID
			}
			related = parseRelatedRefs(mem.Attrs)
		}
		return c.HTMLBlob(http.StatusOK, []byte(renderArtifactBodyWithMeta(bodyFragment, title, backHref, ownerHref, ownerLabel, related)))
	}
}

// renderArtifactBody returns the HTML body to serve for a stored rendered_html
// value. A caller-supplied custom render (pf_save_artifact html=, aihub#104) may
// already be a complete standalone document — detected by a leading <!doctype or
// <html — and is served verbatim to avoid double-wrapping (no back-nav is
// injected in that case). Otherwise the stored value is a body fragment (the
// goldmark auto-render path), so it is wrapped in a standalone document — with
// the optional back-nav — to give the `polyforge artifact view` browser flow
// usable styling. The fragment is kept raw in the column so it can be embedded
// elsewhere.
func renderArtifactBody(stored, title, backHref string) string {
	lc := strings.ToLower(strings.TrimSpace(stored))
	if strings.HasPrefix(lc, "<!doctype") || strings.HasPrefix(lc, "<html") {
		return stored
	}
	return render.Document(stored, title, backHref)
}

// renderArtifactBodyWithMeta is the /ui variant of renderArtifactBody that also
// injects the owning-wi link and related-memory links into the document header.
// Full-document artifacts (already have <!doctype / <html prefix) are served
// verbatim — same policy as renderArtifactBody — so no metadata is injected for
// those (they own their own HTML structure).
func renderArtifactBodyWithMeta(stored, title, backHref, ownerHref, ownerLabel string, related []render.RelatedRef) string {
	lc := strings.ToLower(strings.TrimSpace(stored))
	if strings.HasPrefix(lc, "<!doctype") || strings.HasPrefix(lc, "<html") {
		return stored
	}
	return render.DocumentWithMeta(stored, title, backHref, ownerHref, ownerLabel, related)
}

// wiHref returns the UI href for a wi detail page. The `wiref` template func
// in ui_embed.go delegates to this function so the path logic lives in one
// place. url.PathEscape encodes '#' in slug-style IDs (e.g. "aihub#98") as
// "%23" so the full slug survives the browser round-trip without being
// interpreted as a URL fragment.
func wiHref(slugOrID string) string {
	if slugOrID == "" {
		return ""
	}
	return "/ui/wi/" + url.PathEscape(slugOrID)
}

// parseRelatedRefs builds a []render.RelatedRef from the mem.Attrs JSONB value.
// It reads the "related_ids" key, which is a JSON array of memory-id strings
// written by pf_remember when RelatedMemoryIDs is set.
//
// Type and Summary are left empty for now — only the ID is available from the
// attrs source.
//
// TODO(aihub#112 Stream A): replace attrs.related_ids source with a join-table
// that provides enriched Related[] entries including type and summary, then
// populate the Type and Summary fields here.
func parseRelatedRefs(attrs json.RawMessage) []render.RelatedRef {
	ids := parseRelatedIDs(attrs)
	if len(ids) == 0 {
		return nil
	}
	refs := make([]render.RelatedRef, 0, len(ids))
	for _, id := range ids {
		refs = append(refs, render.RelatedRef{ID: id})
	}
	return refs
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

// handleSharedArtifact serves a publicly-shared artifact's rendered HTML with NO auth.
// The memory_id is itself the unguessable share link. Only memories with
// visibility='public' and non-null rendered_html are reachable; anything else returns a
// uniform 404 so the endpoint never leaks whether a given id exists.
// TODO(aihub#81): could apply the same lazy-render fallback here so artifacts shared
// before rendered_html was populated still serve instead of returning 404.
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
		// renderArtifactBody (not render.Document) so a custom full-document artifact
		// (pf_save_artifact html=) is served verbatim instead of double-wrapped; ""
		// backHref because an anonymous viewer has no /ui/wi to navigate back to.
		return c.HTMLBlob(http.StatusOK, []byte(renderArtifactBody(*mem.RenderedHTML, title, "")))
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
				"artifact has no rendered HTML to share (only methodology.spec / methodology.plan / methodology.review render)"))
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
