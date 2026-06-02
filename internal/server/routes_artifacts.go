package server

import (
	"context"
	"encoding/json"
	"fmt"
	"html"
	"net/http"
	"net/url"
	"strconv"
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
		annotHTML := ""
		if strings.HasPrefix(c.Path(), "/ui") {
			if mem.WorkItemID != nil {
				ownerHref = wiHref(*mem.WorkItemID)
				ownerLabel = *mem.WorkItemID
			}
			related = parseRelatedRefs(mem.Attrs)
			// aihub#124: build annotation UI (section threads + add-comment form).
			// Only for /ui — /v1 and /share must stay byte-for-byte pure.
			// Use bodyFragment (the resolved HTML — stored rendered_html OR the
			// lazy-rendered fallback from #146) so heading anchors align with the
			// rendered document even when rendered_html is NULL.
			annotHTML = buildAnnotationHTML(mem.ID, bodyFragment, mem.Commits)
			// aihub#124 version_history: fetch supersede chain and build version-history block.
			// Best-effort: an error is silently swallowed (non-fatal like other /ui enrichments).
			if versions, verErr := versionChainFn(ctx, pool, mem.ID); verErr == nil {
				if vhHTML := buildVersionHistoryHTML(ctx, pool, mem.ID, versions); vhHTML != "" {
					// Prepend version history before the annotation section.
					annotHTML = vhHTML + annotHTML
				}
			}
		}
		return c.HTMLBlob(http.StatusOK, []byte(renderArtifactBodyWithMeta(bodyFragment, title, backHref, ownerHref, ownerLabel, related, annotHTML)))
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
//
// annotationsHTML is an optional HTML fragment (aihub#124) appended before </body>
// in the /ui path. Pass "" to omit it (the /v1 path always passes "").
func renderArtifactBodyWithMeta(stored, title, backHref, ownerHref, ownerLabel string, related []render.RelatedRef, annotationsHTML ...string) string {
	lc := strings.ToLower(strings.TrimSpace(stored))
	if strings.HasPrefix(lc, "<!doctype") || strings.HasPrefix(lc, "<html") {
		return stored
	}
	return render.DocumentWithMeta(stored, title, backHref, ownerHref, ownerLabel, related, annotationsHTML...)
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

// ─── aihub#124: section-level annotation UI ──────────────────────────────────

// doArtifactCommitFn wraps domain.CommitMemory for the artifact commit path.
// Swappable in tests (same pattern as doCommitMemoryFn in ui_handlers_memory.go).
var doArtifactCommitFn = func(ctx context.Context, pool *pgxpool.Pool, memID, body, callerUserID, callerDisplay, headingID, headingText string) error {
	return domain.CommitMemory(ctx, pool, memID, body, callerUserID, callerDisplay, headingID, headingText)
}

// buildAnnotationHTML constructs the annotation section fragment for the /ui
// artifact viewer. It groups commits by heading and emits:
//   - A thread per heading group (open/resolved badges, AI reply when resolved)
//   - An add-comment form whose <select> is populated from heading ids extracted
//     from the rendered HTML — same ids as the anchors goldmark placed on headings.
//
// Returns "" when there are no commits AND no headings (e.g. non-spec artifact).
// The /v1 and /share paths never call this function.
func buildAnnotationHTML(memID, renderedHTML string, commitsRaw json.RawMessage) string {
	// Parse commits.
	var commits []CommitEntry
	if len(commitsRaw) > 0 {
		_ = json.Unmarshal(commitsRaw, &commits)
	}

	// Extract heading refs from the rendered HTML. The ids in the HTML are
	// byte-identical to what goldmark's WithAutoHeadingID assigns, so the form
	// <select> options and the rendered heading anchors stay in sync without any
	// separate slugification step.
	headings := extractHeadingsFromHTML(renderedHTML)

	if len(commits) == 0 && len(headings) == 0 {
		return ""
	}

	// Group commits by heading id ("" = unanchored / general).
	type threadGroup struct {
		HeadingID   string
		HeadingText string
		Entries     []CommitEntry
	}
	groupOrder := []string{}
	groups := map[string]*threadGroup{}

	addGroup := func(hid, htxt string) {
		if _, ok := groups[hid]; !ok {
			groups[hid] = &threadGroup{HeadingID: hid, HeadingText: htxt}
			groupOrder = append(groupOrder, hid)
		}
	}

	// Pre-seed groups for all document headings (so the form shows all options).
	for _, h := range headings {
		addGroup(h.ID, h.Text)
	}
	// Add unanchored group when any commit lacks an anchor.
	for i := range commits {
		if commits[i].Anchor == nil || commits[i].Anchor.HeadingID == "" {
			addGroup("", "(general / unanchored)")
			break
		}
	}
	// Distribute commits into their groups.
	for i := range commits {
		hid, htxt := "", "(general / unanchored)"
		if commits[i].Anchor != nil && commits[i].Anchor.HeadingID != "" {
			hid = commits[i].Anchor.HeadingID
			htxt = commits[i].Anchor.HeadingText
		}
		addGroup(hid, htxt)
		groups[hid].Entries = append(groups[hid].Entries, commits[i])
	}

	var b strings.Builder
	b.WriteString("<section class=\"pf-annotations\">\n")
	b.WriteString("<h2 class=\"pf-annotations-heading\">Annotations</h2>\n")

	// Render thread for each heading group that has commits.
	for _, hid := range groupOrder {
		g := groups[hid]
		if len(g.Entries) == 0 {
			continue // no commits for this heading yet
		}
		b.WriteString("<div class=\"pf-annot-section\">\n")
		b.WriteString("<h3 class=\"pf-annot-section-title\">")
		if hid != "" {
			b.WriteString("<a href=\"#")
			b.WriteString(html.EscapeString(hid))
			b.WriteString("\">")
			b.WriteString(html.EscapeString(g.HeadingText))
			b.WriteString("</a>")
		} else {
			b.WriteString(html.EscapeString(g.HeadingText))
		}
		b.WriteString("</h3>\n")

		for _, e := range g.Entries {
			statusClass, statusLabel := "pf-annot-open", "open"
			if e.IsResolved() {
				statusClass, statusLabel = "pf-annot-resolved", "resolved"
			}
			b.WriteString("<div class=\"pf-annot-entry ")
			b.WriteString(statusClass)
			b.WriteString("\">\n")
			b.WriteString("<div class=\"pf-annot-meta\"><strong>")
			b.WriteString(html.EscapeString(e.AuthorDisplay))
			b.WriteString("</strong> &middot; ")
			b.WriteString(html.EscapeString(e.CreatedAt))
			b.WriteString(" &middot; <span class=\"pf-annot-status\">")
			b.WriteString(statusLabel)
			b.WriteString("</span></div>\n")
			b.WriteString("<div class=\"pf-annot-body\">")
			b.WriteString(html.EscapeString(e.Body))
			b.WriteString("</div>\n")
			if e.IsResolved() && e.Reply != "" {
				b.WriteString("<div class=\"pf-annot-reply\"><strong>AI reply:</strong> ")
				b.WriteString(html.EscapeString(e.Reply))
				b.WriteString("</div>\n")
			}
			b.WriteString("</div>\n")
		}
		b.WriteString("</div>\n")
	}

	// Add-comment form.
	b.WriteString("<div class=\"pf-annot-form\">\n")
	b.WriteString("<h3 class=\"pf-annot-form-title\">Add annotation</h3>\n")
	b.WriteString("<form method=\"POST\" action=\"/ui/artifacts/")
	b.WriteString(html.EscapeString(memID))
	b.WriteString("/commit\">\n")

	if len(headings) > 0 {
		b.WriteString("<label for=\"pf-annot-heading\">Section:</label>\n")
		b.WriteString("<select id=\"pf-annot-heading\" name=\"heading_id\"")
		b.WriteString(" onchange=\"document.getElementById('pf-annot-htxt').value=this.options[this.selectedIndex].dataset.text\">\n")
		b.WriteString("<option value=\"\" data-text=\"\">\xe2\x80\x94 general \xe2\x80\x94</option>\n")
		for _, h := range headings {
			b.WriteString("<option value=\"")
			b.WriteString(html.EscapeString(h.ID))
			b.WriteString("\" data-text=\"")
			b.WriteString(html.EscapeString(h.Text))
			b.WriteString("\">")
			b.WriteString(html.EscapeString(h.Text))
			b.WriteString("</option>\n")
		}
		b.WriteString("</select>\n")
		b.WriteString("<input type=\"hidden\" id=\"pf-annot-htxt\" name=\"heading_text\" value=\"\">\n")
	} else {
		b.WriteString("<input type=\"hidden\" name=\"heading_id\" value=\"\">\n")
		b.WriteString("<input type=\"hidden\" name=\"heading_text\" value=\"\">\n")
	}

	b.WriteString("<label for=\"pf-annot-body\">Comment:</label>\n")
	b.WriteString("<textarea id=\"pf-annot-body\" name=\"body\" rows=\"3\" placeholder=\"Add a commit annotation…\" required></textarea>\n")
	b.WriteString("<button type=\"submit\">Add annotation</button>\n")
	b.WriteString("</form>\n")
	b.WriteString("</div>\n")
	b.WriteString("</section>\n")
	return b.String()
}

// extractHeadingsFromHTML scans a goldmark-rendered HTML fragment for heading
// tags (<h1>–<h6>) that carry an id attribute and returns their (id, text) pairs
// in document order. The ids are exactly what goldmark's WithAutoHeadingID emits,
// so form <select> options align with the rendered document anchors.
func extractHeadingsFromHTML(htmlFrag string) []render.HeadingRef {
	if htmlFrag == "" {
		return nil
	}
	var refs []render.HeadingRef
	remaining := htmlFrag
	for {
		hi := -1
		var endTag string
		lowerRem := strings.ToLower(remaining)
		for _, level := range []string{"h1", "h2", "h3", "h4", "h5", "h6"} {
			for _, prefix := range []string{"<" + level + " ", "<" + level + ">"} {
				if idx := strings.Index(lowerRem, prefix); idx >= 0 {
					if hi < 0 || idx < hi {
						hi = idx
						endTag = "</" + level + ">"
					}
				}
			}
		}
		if hi < 0 {
			break
		}
		// Find end of opening tag.
		tagEnd := strings.Index(remaining[hi:], ">")
		if tagEnd < 0 {
			break
		}
		openTag := remaining[hi : hi+tagEnd+1]
		rest := remaining[hi+tagEnd+1:]

		idVal := extractHTMLAttr(openTag, "id")
		if idVal == "" {
			remaining = rest
			continue
		}

		// Extract text content between open and close tag.
		closeIdx := strings.Index(strings.ToLower(rest), endTag)
		var textContent string
		if closeIdx >= 0 {
			textContent = stripHTMLTags(rest[:closeIdx])
			remaining = rest[closeIdx+len(endTag):]
		} else {
			remaining = rest
		}
		refs = append(refs, render.HeadingRef{ID: idVal, Text: textContent})
	}
	return refs
}

// extractHTMLAttr extracts the value of a named attribute from an HTML opening tag.
func extractHTMLAttr(tag, attrName string) string {
	lower := strings.ToLower(tag)
	needle := " " + attrName + "="
	idx := strings.Index(lower, needle)
	if idx < 0 {
		return ""
	}
	rest := tag[idx+len(needle):]
	if len(rest) == 0 {
		return ""
	}
	quote := rest[0]
	if quote != '"' && quote != '\'' {
		end := strings.IndexAny(rest, " \t\n\r>")
		if end < 0 {
			return rest
		}
		return rest[:end]
	}
	rest = rest[1:]
	end := strings.IndexByte(rest, quote)
	if end < 0 {
		return rest
	}
	return rest[:end]
}

// stripHTMLTags removes HTML tags from a string to produce plain text.
func stripHTMLTags(s string) string {
	var out strings.Builder
	inTag := false
	for _, r := range s {
		switch {
		case r == '<':
			inTag = true
		case r == '>':
			inTag = false
		case !inTag:
			out.WriteRune(r)
		}
	}
	return strings.TrimSpace(out.String())
}

// RegisterUIArtifactCommitRoute registers the /ui-only POST commit route on the
// given /ui group. Called from RegisterUIRoutes after the auth middleware is set up.
func RegisterUIArtifactCommitRoute(uiGroup *echo.Group, pool *pgxpool.Pool) {
	uiGroup.POST("/artifacts/:id/commit", handleUIArtifactCommit(pool))
}

// handleUIArtifactCommit handles POST /ui/artifacts/:id/commit.
//
// Appends a section-anchored annotation to a spec/plan artifact's commits column.
// Access: must be a logged-in writer on the artifact's project.
// Form fields: heading_id (optional), heading_text (optional), body (required).
// Redirects 303 back to /ui/artifacts/:id/html on success.
func handleUIArtifactCommit(pool *pgxpool.Pool) echo.HandlerFunc {
	return func(c echo.Context) error {
		u := GetUser(c)
		if u == nil {
			return redirectToLogin(c)
		}

		memID := c.Param("id")
		if memID == "" {
			return writeError(c, domain.NewErr(domain.ErrBadRequest, "artifact id is required"))
		}

		body := c.FormValue("body")
		if body == "" {
			return writeError(c, domain.NewErr(domain.ErrBadRequest, "body is required"))
		}
		headingID := c.FormValue("heading_id")
		headingText := c.FormValue("heading_text")

		ctx, cancel := contextWithTimeout(c)
		defer cancel()

		// Load (project, status) to check access before CommitMemory's own guard.
		project, _, loadErr := commitMemoryProjectFn(ctx, pool, memID)
		if loadErr != nil {
			return writeError(c, domain.NewErr(domain.ErrNotFound, "artifact not found"))
		}
		if err := checkProjectAccess(c, u, project, "writer"); err != nil {
			return err
		}

		if err := doArtifactCommitFn(ctx, pool, memID, body, u.UserID, u.DisplayName, headingID, headingText); err != nil {
			return domainErr(c, err)
		}

		return c.Redirect(http.StatusSeeOther, "/ui/artifacts/"+url.PathEscape(memID)+"/html")
	}
}

// ─── aihub#124: version history UI ──────────────────────────────────────────

// versionChainFn is the production-wired domain.MemoryVersionChain seam;
// swappable in tests (same pattern as loadMemoryFn / doArtifactCommitFn).
var versionChainFn = func(ctx context.Context, pool *pgxpool.Pool, memID string) ([]domain.MemoryVersionRef, error) {
	return domain.MemoryVersionChain(ctx, pool, memID)
}

// buildVersionHistoryHTML returns a small HTML fragment listing all versions in
// the supersede chain for memID. Returns "" when the chain has ≤1 entry (nothing
// to show) or on error (best-effort, non-fatal). Only called on the /ui path.
func buildVersionHistoryHTML(ctx context.Context, pool *pgxpool.Pool, memID string, versions []domain.MemoryVersionRef) string {
	if len(versions) <= 1 {
		return ""
	}

	var b strings.Builder
	b.WriteString("<section class=\"pf-version-history\">\n")
	b.WriteString("<h2 class=\"pf-version-history-heading\">Version history</h2>\n")
	b.WriteString("<ol class=\"pf-version-list\">\n")
	for i, v := range versions {
		label := "v" + strconv.Itoa(i+1)
		// Shorten timestamp: take up to 10 chars (YYYY-MM-DD) from the RFC3339 string.
		ts := v.CreatedAt
		if len(ts) > 10 {
			ts = ts[:10]
		}
		b.WriteString("<li class=\"pf-version-item")
		if v.IsCurrent {
			b.WriteString(" pf-version-current")
		}
		b.WriteString("\">")
		if v.ID == memID {
			// Currently viewed version: plain text, mark it.
			b.WriteString("<strong>")
			b.WriteString(html.EscapeString(label))
			b.WriteString("</strong>")
			b.WriteString(" <span class=\"pf-version-ts\">(")
			b.WriteString(html.EscapeString(ts))
			b.WriteString(")</span>")
			b.WriteString(" <span class=\"pf-version-badge\">viewing</span>")
		} else {
			b.WriteString("<a href=\"/ui/artifacts/")
			b.WriteString(html.EscapeString(v.ID))
			b.WriteString("/html\">")
			b.WriteString(html.EscapeString(label))
			b.WriteString("</a>")
			b.WriteString(" <span class=\"pf-version-ts\">(")
			b.WriteString(html.EscapeString(ts))
			b.WriteString(")</span>")
		}
		if v.IsCurrent && v.ID != memID {
			b.WriteString(" <span class=\"pf-version-badge pf-version-head\">current</span>")
		}
		b.WriteString("</li>\n")
	}
	b.WriteString("</ol>\n")
	b.WriteString("</section>\n")
	return b.String()
}
