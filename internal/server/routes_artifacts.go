package server

import (
	"context"
	"encoding/json"
	"fmt"
	"html"
	htmltemplate "html/template"
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

			// aihub#138: inject /ui-only design tokens + viewer stylesheet so
			// the artifact document renders with the #129 design system. These tags
			// are appended into <body> — DocumentWithMeta places annotHTML before
			// </body>, and its <html>/<head> output is frozen to keep /v1 + /share
			// byte-identical, so we cannot put the links in <head>. An inline script
			// sets data-theme synchronously; because the stylesheets load late there
			// is a brief restyle flash on first paint — an accepted tradeoff given the
			// frozen document shell. Theme comes from the cookie, defaulting to "auto"
			// (follow OS). viewer.css overrides the earlier-embedded style.css via the
			// token vars declared by ui.css.
			av := render.AssetVersion()
			theme := themeFromCookie(c)
			var uiHead strings.Builder
			// Set data-theme on <html> immediately (inline script runs synchronously).
			// For review pages, also add pf-review-page class so CSS grid + order work.
			if mem.Type == "methodology.review" {
				uiHead.WriteString("<script>(function(){document.documentElement.setAttribute('data-theme','")
				uiHead.WriteString(theme)
				uiHead.WriteString("');document.addEventListener('DOMContentLoaded',function(){document.body.classList.add('pf-review-page');});")
				uiHead.WriteString("})();</script>\n")
			} else {
				uiHead.WriteString("<script>(function(){document.documentElement.setAttribute('data-theme','")
				uiHead.WriteString(theme)
				uiHead.WriteString("');")
				// aihub#138: stabilise the /ui layout for spec/plan regardless of
				// annotation count. annot.js only adds pf-annot-active when it anchors
				// commits (and never for a zero-annotation or old version), which left
				// such pages without the grid/card layout, with the legacy flat list +
				// native select showing and the breadcrumb misplaced. Set it server-side
				// so every spec/plan version renders in the new UI; annot.js's later add
				// is then a no-op (the class is a pure CSS hook it never reads).
				uiHead.WriteString("if(document.body){document.body.classList.add('pf-annot-active');}")
				uiHead.WriteString("})();</script>\n")
			}
			// aihub#234: assets served from the server package's embedded static/ FS
			// (ui.css, theme.js, diagram.js) are versioned with THIS package's
			// assetVersion, not render.AssetVersion(). The latter is a content hash
			// over the render package's own bytes (annotator.js, annot.js, viewer.css,
			// share.js) and cannot see static/ at all, so a ui.css-only edit shipped
			// under an unchanged ?v= and the browser kept the cached copy for up to
			// the hour of its max-age. This mattered little while ui.css and viewer.css
			// changed together; it matters now that the diagram styling lives in ui.css.
			uiHead.WriteString("<link rel=\"stylesheet\" href=\"/ui/static/ui.css?v=")
			uiHead.WriteString(assetVersion)
			uiHead.WriteString("\">\n")
			uiHead.WriteString("<link rel=\"stylesheet\" href=\"/ui/static/viewer.css?v=")
			uiHead.WriteString(av)
			uiHead.WriteString("\">\n")
			// aihub#167: load theme.js so the 3-segment theme control (now unified
			// with the app-shell nav) works on the viewer. The inline single-toggle
			// handler was removed; theme.js is the canonical theme handler for all /ui.
			uiHead.WriteString("<script src=\"/ui/static/theme.js?v=")
			uiHead.WriteString(assetVersion)
			uiHead.WriteString("\" defer></script>\n")
			// aihub#234: click-to-zoom for d2 figures. Inline, a diagram is capped at
			// the column width (ui.css); this is the only way to read a wide one at
			// full size without page-zooming the prose along with it. Inside the /ui
			// gate like every other viewer affordance — /v1 and /share never compile
			// d2 in the first place.
			uiHead.WriteString("<script src=\"/ui/static/diagram.js?v=")
			uiHead.WriteString(assetVersion)
			uiHead.WriteString("\" defer></script>\n")

			// aihub#138 / #167: build the unified app-shell nav + breadcrumb for /ui
			// pages. Uses buildAppNav (same function as layout.html.tmpl) so the nav
			// is byte-identical across the app-shell and the artifact viewer.
			// The chrome is prepended to annotHTML so it is emitted after #pf-doc-col
			// in the DOM; CSS grid `order` floats it to the top.
			uiChrome := buildUIChrome(mem.WorkItemID, mem.Type, mem.ID, "", theme, u)

			// aihub#154: build the Share control + share.js — /ui path only, and
			// only for artifacts that actually have stored rendered HTML (i.e.
			// methodology.spec / plan / review). The handlers behind it 412 when
			// rendered_html is NULL, so gating the control on the same condition
			// keeps the UI honest. The fragment (control + script) is injected just
			// after the first </h1> so it renders directly below the document title;
			// it never reaches /v1 or /share, preserving their byte-identical output.
			shareControlHTML := ""
			if mem.RenderedHTML != nil {
				shareURL := c.Scheme() + "://" + c.Request().Host + "/share/" + mem.ID
				shared := mem.Visibility == "public"
				shareControlHTML = buildShareControlHTML(mem.ID, shareURL, shared, av)
			}

			// aihub#138: review viewer — methodology.review gets a dedicated layout
			// instead of the annotation scaffold. /v1 + /share stay plain document.
			// The structured verdict/findings/checked/outcome chrome is appended to
			// the body fragment so it renders INSIDE the single doc card (one
			// centered column, matching the #129 prototype); uiHead (theme +
			// stylesheet links) stays in annotHTML.
			if mem.Type == "methodology.review" {
				if chrome := buildReviewHTML(mem); chrome != "" {
					bodyFragment = bodyFragment + "\n" + chrome
				}
				// review has no version-history chain — only the share control is
				// injected, directly below the title.
				if shareControlHTML != "" {
					bodyFragment = insertAfterFirstH1(bodyFragment, shareControlHTML)
				}
				annotHTML = uiHead.String() + uiChrome
			} else {
				// aihub#124: build annotation UI (section threads + add-comment form).
				// Only for /ui — /v1 and /share must stay byte-for-byte pure.
				// Use bodyFragment (the resolved HTML — stored rendered_html OR the
				// lazy-rendered fallback from #146) so heading anchors align with the
				// rendered document even when rendered_html is NULL.
				// Annotations are strictly per-version: a page shows only the commits
				// made on the version being viewed (cross-version feedback flow is
				// pf-revise's job, which reads the old head explicitly by id).
				// aihub#160: render ```d2 code blocks to inline SVG (/ui-only; /v1 + /share
				// keep the raw code block). Done before folding so the figure lands inside
				// its section.
				bodyFragment = render.RenderDiagramsForUI(bodyFragment)
				// aihub#159: fold H2 sections into <details> for /ui readability (spec/plan).
				// /ui-only — /v1 + /share keep the flat body. Default-open so annot.js
				// text-quote anchoring (searches visible text) is unaffected.
				bodyFragment = wrapH2SectionsForUI(bodyFragment)
				annotHTML = buildAnnotationHTML(mem.ID, bodyFragment, mem.Commits)
				// aihub#138 version_history: render version history INSIDE the doc card.
				// aihub#154: the share control + version history are injected together
				// just after the first </h1> — share above, version history below — so
				// the order is title → share → version history → body. Empty pieces
				// (single-version chain, or NULL rendered_html) collapse out cleanly.
				// aihub#159 step4b: version history relocates into the side rail (below);
				// only the share control stays injected in-card under the title.
				var srVersions []sideRailVersion
				if versions, verErr := versionChainFn(ctx, pool, mem.ID); verErr == nil && len(versions) > 1 {
					for i, v := range versions {
						sv := sideRailVersion{Label: "v" + strconv.Itoa(i+1), Current: v.IsCurrent}
						if len(v.CreatedAt) >= 10 {
							sv.Date = v.CreatedAt[:10]
						}
						if v.ID != mem.ID {
							sv.Href = "/ui/artifacts/" + v.ID + "/html"
						}
						srVersions = append(srVersions, sv)
					}
				}
				if shareControlHTML != "" {
					bodyFragment = insertAfterFirstH1(bodyFragment, shareControlHTML)
				}
				// aihub#125: inject client-side annotation scripts — /ui path only.
				// ?v= content hash busts browser caches on deploys (assets are served
				// with Cache-Control max-age).
				annotHTML += "\n<script src=\"/ui/static/annotator.js?v=" + av + "\" defer></script>\n<script src=\"/ui/static/annot.js?v=" + av + "\" defer></script>\n"
				// Prepend ui.css + viewer.css links + theme setter + chrome.
				annotHTML = uiHead.String() + uiChrome + annotHTML
				// aihub#159 step4b: consolidated right rail (TOC + Details) in the
				// column freed by the inline-marker annotation rework.
				srMeta := sideRailMeta{
					Author:     mem.AuthorDisplay,
					Type:       mem.Type,
					Project:    mem.Project,
					Visibility: mem.Visibility,
					Strength:   fmt.Sprintf("%.2f", mem.BaseStrength),
					Created:    mem.CreatedAt.Format("2006-01-02"),
					Updated:    mem.UpdatedAt.Format("2006-01-02"),
				}
				if mem.WorkItemID != nil {
					srMeta.WorkItemHref = wiHref(*mem.WorkItemID)
					srMeta.WorkItemLabel = *mem.WorkItemID
				}
				var srComments []sideRailComment
				if len(mem.Commits) > 0 {
					var cl []CommitEntry
					if json.Unmarshal(mem.Commits, &cl) == nil {
						for _, c := range cl {
							srComments = append(srComments, sideRailComment{ID: c.ID, Author: c.AuthorDisplay, Body: c.Body, Status: c.Status})
						}
					}
				}
				annotHTML += buildSideRail(render.ExtractHeadings(mem.Content), srMeta, srVersions, srComments)
			}
		}
		return c.HTMLBlob(http.StatusOK, []byte(renderArtifactBodyWithMeta(bodyFragment, title, backHref, ownerHref, ownerLabel, related, annotHTML)))
	}
}

// wrapH2SectionsForUI wraps each top-level H2 section of a rendered artifact body
// in a <details open> block so the /ui viewer can fold long sections (aihub#159).
// /ui-only: /v1 + /share keep the flat rendered body, so byte-identical output is
// preserved. Default-open keeps every section's text in the visible DOM, so annot.js
// text-quote anchoring (which searches rendered text) is unaffected. Content before
// the first H2 (the H1 + intro + any injected share/version chrome) is left as-is.
func wrapH2SectionsForUI(body string) string {
	const open = "<h2"
	first := strings.Index(body, open)
	if first < 0 {
		return body
	}
	var b strings.Builder
	b.Grow(len(body) + 256)
	b.WriteString(body[:first])
	rest := body[first:]
	for rest != "" {
		// rest begins with "<h2"; find the next section boundary.
		nextRel := strings.Index(rest[len(open):], open)
		var section string
		if nextRel < 0 {
			section, rest = rest, ""
		} else {
			cut := len(open) + nextRel
			section, rest = rest[:cut], rest[cut:]
		}
		hClose := strings.Index(section, "</h2>")
		if hClose < 0 {
			b.WriteString(section) // malformed; pass through untouched
			continue
		}
		heading := section[:hClose+len("</h2>")]
		secBody := section[hClose+len("</h2>"):]
		b.WriteString(`<details open class="pf-sec"><summary class="pf-sec-sum">`)
		b.WriteString(heading)
		b.WriteString(`<span class="pf-sec-chev" aria-hidden="true"></span></summary><div class="pf-sec-body">`)
		b.WriteString(secBody)
		b.WriteString(`</div></details>`)
	}
	return b.String()
}

// sideRailMeta carries the pre-formatted Details fields for the /ui side rail so
// buildSideRail stays decoupled from *domain.Memory (and trivially testable).
type sideRailMeta struct {
	Author, Type, Project, Visibility string
	WorkItemHref, WorkItemLabel       string
	Strength, Created, Updated        string
}

// buildSideRail builds the consolidated /ui artifact-viewer right rail (aihub#159
// step4b): an "On this page" TOC (from the rendered heading anchors) + a Details
// card. It occupies the right grid column freed by the inline-marker annotation
// rework. /ui-only — never emitted on /v1 or /share.
// sideRailVersion is one row of the side rail's version-history timeline.
type sideRailVersion struct {
	Label   string // e.g. "v3"
	Date    string // YYYY-MM-DD
	Current bool
	Href    string // /ui/artifacts/<id>/html for other versions; "" for the viewed one
}

// sideRailComment is one entry in the side rail's Comments card.
type sideRailComment struct {
	ID, Author, Body, Status string
}

func buildSideRail(headings []render.HeadingRef, m sideRailMeta, versions []sideRailVersion, comments []sideRailComment) string {
	var b strings.Builder
	b.WriteString("<aside id=\"pf-side-rail\">\n")
	// chev is the collapse caret appended to each card's <summary>.
	const chev = "<span class=\"pf-side-chev\" aria-hidden=\"true\"></span>"
	if len(headings) >= 2 {
		b.WriteString("<details class=\"pf-side-card\" open><summary class=\"pf-side-hd\">On this page" + chev + "</summary><nav class=\"pf-side-toc\">")
		for _, h := range headings {
			b.WriteString("<a href=\"#")
			b.WriteString(html.EscapeString(h.ID))
			b.WriteString("\">")
			b.WriteString(html.EscapeString(h.Text))
			b.WriteString("</a>")
		}
		b.WriteString("</nav></details>\n")
	}
	b.WriteString("<details class=\"pf-side-card\"><summary class=\"pf-side-hd\">Details" + chev + "</summary>")
	row := func(k, vHTML string) {
		if vHTML == "" {
			return
		}
		b.WriteString("<div class=\"pf-side-row\"><span class=\"k\">")
		b.WriteString(html.EscapeString(k))
		b.WriteString("</span><span class=\"v\">")
		b.WriteString(vHTML)
		b.WriteString("</span></div>")
	}
	mono := func(s string) string {
		if s == "" {
			return ""
		}
		return "<span class=\"mono\">" + html.EscapeString(s) + "</span>"
	}
	row("Author", html.EscapeString(m.Author))
	row("Type", mono(m.Type))
	row("Project", html.EscapeString(m.Project))
	row("Visibility", html.EscapeString(m.Visibility))
	if m.WorkItemHref != "" {
		row("Work item", "<a class=\"link mono\" href=\""+html.EscapeString(m.WorkItemHref)+"\">"+html.EscapeString(m.WorkItemLabel)+"</a>")
	}
	row("Strength", mono(m.Strength))
	row("Created", mono(m.Created))
	row("Updated", mono(m.Updated))
	b.WriteString("</details>\n") // close Details card

	if len(versions) > 1 {
		b.WriteString("<details class=\"pf-side-card\"><summary class=\"pf-side-hd\">Version history <span class=\"pf-side-n\">")
		b.WriteString(strconv.Itoa(len(versions)))
		b.WriteString("</span>" + chev + "</summary><ol class=\"pf-side-vh\">")
		for _, v := range versions {
			b.WriteString("<li class=\"pf-side-vrow")
			if v.Current {
				b.WriteString(" cur")
			}
			b.WriteString("\"><span class=\"pf-side-vdot\"></span>")
			if v.Href != "" {
				b.WriteString("<a class=\"link mono pf-side-vlabel\" href=\"" + html.EscapeString(v.Href) + "\">" + html.EscapeString(v.Label) + "</a>")
			} else {
				b.WriteString("<span class=\"mono pf-side-vlabel\">" + html.EscapeString(v.Label) + "</span>")
			}
			if v.Current {
				b.WriteString("<span class=\"pf-side-vcur\">current</span>")
			}
			b.WriteString("<span class=\"pf-side-vdate\">" + html.EscapeString(v.Date) + "</span></li>")
		}
		b.WriteString("</ol></details>\n")
	}

	if len(comments) > 0 {
		open := 0
		for _, c := range comments {
			if c.Status == "" || c.Status == "open" {
				open++
			}
		}
		b.WriteString("<details class=\"pf-side-card\"><summary class=\"pf-side-hd\">Comments <span class=\"pf-side-n\">")
		b.WriteString(strconv.Itoa(len(comments)))
		b.WriteString("</span>" + chev + "</summary><div class=\"pf-side-cmt\"><div class=\"pf-side-cmt-sum\">")
		b.WriteString(strconv.Itoa(open) + " open · " + strconv.Itoa(len(comments)-open) + " resolved")
		b.WriteString("</div>")
		for _, c := range comments {
			st := c.Status
			if st == "" {
				st = "open"
			}
			body := c.Body
			if r := []rune(body); len(r) > 84 {
				body = string(r[:84]) + "…"
			}
			b.WriteString("<button type=\"button\" class=\"pf-side-cmt-item\" data-commit-id=\"" + html.EscapeString(c.ID) + "\">")
			b.WriteString("<span class=\"pf-side-cmt-top\"><span class=\"who\"><span class=\"av sm\" data-av-name=\"" + html.EscapeString(c.Author) + "\"></span><b>" + html.EscapeString(c.Author) + "</b></span><span class=\"pf-side-cmt-st " + st + "\">" + st + "</span></span>")
			b.WriteString("<span class=\"pf-side-cmt-body\">" + html.EscapeString(body) + "</span>")
			b.WriteString("</button>")
		}
		b.WriteString("</div></details>\n")
	}

	b.WriteString("</aside>\n")
	// aihub#159 step4c: TOC scroll-spy — highlight the side-rail link for the
	// section currently in view (/ui-only; no-op without IntersectionObserver).
	b.WriteString(`<script>(function(){var ls=document.querySelectorAll('#pf-side-rail .pf-side-toc a');if(!ls.length||!window.IntersectionObserver)return;var m={};ls.forEach(function(a){m[a.getAttribute('href').slice(1)]=a;});var io=new IntersectionObserver(function(es){es.forEach(function(e){if(e.isIntersecting){for(var k in m){m[k].classList.remove('active');}var a=m[e.target.id];if(a){a.classList.add('active');}}});},{rootMargin:'-80px 0px -70% 0px'});Object.keys(m).forEach(function(id){var el=document.getElementById(id);if(el){io.observe(el);}});})();(function(){document.querySelectorAll('#pf-side-rail .pf-side-cmt-item').forEach(function(btn){btn.addEventListener('click',function(e){e.stopPropagation();var id=btn.getAttribute('data-commit-id');var mk=document.querySelector('.pf-annot-marker[data-commit-id="'+id+'"]')||document.querySelector('mark[data-commit-id="'+id+'"]');if(mk){mk.scrollIntoView({behavior:'smooth',block:'center'});mk.click();}});});})();</script>` + "\n")
	return b.String()
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

// buildAppNav constructs the canonical <header class="pf-appnav"> HTML for the
// /ui top navigation bar (aihub#167). It is the single source for the nav so
// that both the app-shell (layout.html.tmpl via the {{appnav}} template func)
// and the artifact viewer (buildUIChrome) render the same markup from one place.
//
//   - active: "wi" | "memories" | "" — drives the .pf-active class on nav links.
//   - theme:  "auto" | "light" | "dark" — drives the .on class on the 3-segment
//     theme control.
//   - user:   when non-nil the display-name + logout form are emitted; pass nil
//     to omit (anonymous / non-authenticated views).
//
// All user-supplied strings are HTML-escaped. Returns template.HTML so it is
// safe to inject directly into a template with {{appnav .Active .Theme .User}}.
func buildAppNav(active, theme string, user *UserContext) htmltemplate.HTML {
	var b strings.Builder
	b.WriteString("<header class=\"pf-appnav\">\n")
	b.WriteString("  <a class=\"pf-appnav-brand\" href=\"/ui/\"><span class=\"pf-appnav-mark\">p</span> polyforge</a>\n")
	b.WriteString("  <nav class=\"pf-appnav-links\">")
	if active == "wi" {
		b.WriteString("<a href=\"/ui/wi\" class=\"pf-active\">Work Items</a>")
	} else {
		b.WriteString("<a href=\"/ui/wi\">Work Items</a>")
	}
	if active == "memories" {
		b.WriteString("<a href=\"/ui/memories\" class=\"pf-active\">Memories</a>")
	} else {
		b.WriteString("<a href=\"/ui/memories\">Memories</a>")
	}
	b.WriteString("</nav>\n")
	b.WriteString("  <span class=\"pf-appnav-spacer\"></span>\n")
	b.WriteString("  <div class=\"pf-theme-seg\" id=\"pf-theme-seg\" role=\"group\" aria-label=\"Color theme\">\n")
	if theme == "auto" {
		b.WriteString("    <button type=\"button\" data-theme-mode=\"auto\" class=\"on\">Auto</button>\n")
	} else {
		b.WriteString("    <button type=\"button\" data-theme-mode=\"auto\">Auto</button>\n")
	}
	if theme == "light" {
		b.WriteString("    <button type=\"button\" data-theme-mode=\"light\" class=\"on\">Light</button>\n")
	} else {
		b.WriteString("    <button type=\"button\" data-theme-mode=\"light\">Light</button>\n")
	}
	if theme == "dark" {
		b.WriteString("    <button type=\"button\" data-theme-mode=\"dark\" class=\"on\">Dark</button>\n")
	} else {
		b.WriteString("    <button type=\"button\" data-theme-mode=\"dark\">Dark</button>\n")
	}
	b.WriteString("  </div>\n")
	if user != nil {
		b.WriteString("  <span class=\"pf-nav-who\">")
		b.WriteString(html.EscapeString(user.DisplayName))
		b.WriteString("</span>\n")
		b.WriteString("  <form method=\"POST\" action=\"/ui/logout\" class=\"pf-nav-logout\">\n")
		b.WriteString("    <button type=\"submit\">Logout</button>\n")
		b.WriteString("  </form>\n")
	}
	b.WriteString("</header>\n")
	return htmltemplate.HTML(b.String()) //nolint:gosec // intentionally pre-escaped above
}

// buildUIChrome constructs the app-shell nav + breadcrumb HTML fragment for /ui
// artifact pages (aihub#138, unified in aihub#167). The fragment is emitted
// into annotHTML so it lands AFTER #pf-doc-col in the DOM; CSS grid `order`
// rules float it to the visual top without restructuring DocumentWithMeta's wrapper.
//
// The nav is now produced by buildAppNav so it is byte-identical to the nav
// rendered by the app-shell (layout.html.tmpl). The breadcrumb format varies:
//   - methodology.spec → "<wi> / Spec <memID>"
//   - methodology.plan → "<wi> / Plan <memID>"
//   - methodology.review → "<wi> / Review <memID>"
//   - other → "<wi> / <type> <memID>"
//
// When workItemID is nil the wi segment is rendered as plain text "artifact".
// active is typically "" for artifact pages (no nav link is highlighted).
// All interpolated values are HTML-escaped.
func buildUIChrome(workItemID *string, memType, memID, active, theme string, user *UserContext) string {
	var b strings.Builder
	b.WriteString(string(buildAppNav(active, theme, user)))

	// Breadcrumb.
	b.WriteString("<nav class=\"pf-crumb\">")
	if workItemID != nil && *workItemID != "" {
		b.WriteString("<a href=\"")
		b.WriteString(html.EscapeString(wiHref(*workItemID)))
		b.WriteString("\">")
		b.WriteString(html.EscapeString(*workItemID))
		b.WriteString("</a>")
	} else {
		b.WriteString("<span>artifact</span>")
	}
	b.WriteString(" <span>/</span> ")

	var typeLabel string
	switch memType {
	case "methodology.spec":
		typeLabel = "Spec"
	case "methodology.plan":
		typeLabel = "Plan"
	case "methodology.review":
		typeLabel = "Review"
	default:
		typeLabel = memType
	}
	b.WriteString("<span>")
	b.WriteString(html.EscapeString(typeLabel))
	b.WriteString("</span> <span class=\"pf-crumb-mono\">")
	b.WriteString(html.EscapeString(memID))
	b.WriteString("</span>")
	b.WriteString("</nav>\n")

	return b.String()
}

// insertAfterFirstH1 inserts inject right after the first closing </h1> tag in
// body. If body has no <h1> (shouldn't happen for spec/plan/review, whose
// rendered HTML always opens with the title heading), it falls back to
// prepending so the injected control is never lost.
func insertAfterFirstH1(body, inject string) string {
	idx := strings.Index(strings.ToLower(body), "</h1>")
	if idx < 0 {
		return inject + body
	}
	cut := idx + len("</h1>")
	return body[:cut] + inject + body[cut:]
}

// buildShareControlHTML builds the /ui-only Share control fragment for the
// artifact viewer (aihub#154). It is injected just after the first </h1> so it
// renders directly below the document title (above the version-history dropdown
// when present); the share.js <script> is included in the fragment itself (with
// a ?v= cache-buster) so it loads regardless of whether the annotation HTML is
// present, without touching DocumentWithMeta.
//
// shared toggles the initial button label + whether the read-only link row is
// shown pre-filled (so a refresh of an already-shared artifact still surfaces
// the link). shareURL is the canonical /share/<id> link for the current host.
// This fragment is NEVER emitted on /v1 or /share — only the /ui path calls it.
func buildShareControlHTML(memID, shareURL string, shared bool, assetVersion string) string {
	escID := html.EscapeString(memID)
	var b strings.Builder
	b.WriteString("<div id=\"pf-share\" data-mem-id=\"")
	b.WriteString(escID)
	b.WriteString("\" data-shared=\"")
	if shared {
		b.WriteString("true")
	} else {
		b.WriteString("false")
	}
	b.WriteString("\" class=\"pf-share\">\n")

	b.WriteString("  <button type=\"button\" data-pf-share-btn class=\"pf-share-btn\">")
	if shared {
		b.WriteString("Stop sharing")
	} else {
		b.WriteString("Share")
	}
	b.WriteString("</button>\n")

	b.WriteString("  <span data-pf-share-link class=\"pf-share-link\"")
	if !shared {
		b.WriteString(" hidden")
	}
	b.WriteString(">\n")
	b.WriteString("    <input type=\"text\" readonly value=\"")
	if shared {
		b.WriteString(html.EscapeString(shareURL))
	}
	b.WriteString("\">\n")
	b.WriteString("    <button type=\"button\" data-pf-share-copy class=\"pf-share-copy\">Copy</button>\n")
	b.WriteString("  </span>\n")

	b.WriteString("  <span data-pf-share-toast class=\"pf-share-toast\" role=\"status\" aria-live=\"polite\"></span>\n")
	b.WriteString("</div>\n")

	b.WriteString("<script src=\"/ui/static/share.js?v=")
	b.WriteString(assetVersion)
	b.WriteString("\" defer></script>\n")

	return b.String()
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
	if memoryVisibleTo(u, mem) {
		return nil
	}
	msg := "this memory is private to its author"
	if mem.Visibility == "admin" {
		msg = "this memory requires admin role"
	}
	ae := domain.NewErr(domain.ErrForbidden, msg)
	writeError(c, ae) //nolint:errcheck
	return ae
}

// ─── aihub#124: section-level annotation UI ──────────────────────────────────

// doArtifactCommitFn wraps domain.CommitMemory for the artifact commit path.
// Swappable in tests (same pattern as doCommitMemoryFn in ui_handlers_memory.go).
var doArtifactCommitFn = func(ctx context.Context, pool *pgxpool.Pool, memID, body, callerUserID, callerDisplay string, anchor domain.CommitAnchorArgs) error {
	return domain.CommitMemory(ctx, pool, memID, body, callerUserID, callerDisplay, anchor)
}

// buildAnnotationHTML constructs the annotation section fragment for the /ui
// artifact viewer. It groups commits by heading and emits:
//   - A thread per heading group (open/resolved badges, AI reply when resolved)
//   - An add-comment form whose <select> is populated from heading ids extracted
//     from the rendered HTML — same ids as the anchors goldmark placed on headings.
//
// Returns "" when there are no commits AND no headings (e.g. non-spec artifact).
// The /v1 and /share paths never call this function.
// escapeJSONForScriptTag escapes the "</script" byte sequence inside a JSON
// blob so that it cannot break out of a <script> tag when embedded as a data
// island. JSON.parse on the client side is unaffected because "\/" is
// semantically identical to "/" in JSON strings.
func escapeJSONForScriptTag(b []byte) []byte {
	// Replacing every "</" with "<\/" is sufficient: the script-data tokenizer
	// only exits a <script type="application/json"> block on a literal
	// "</script", and the "<!--" escaped-state breakout also requires an
	// unescaped "</script" to take effect. "\/" is a valid JSON escape for
	// "/", so the payload round-trips unchanged.
	return []byte(strings.ReplaceAll(string(b), "</", "<\\/"))
}

// buildAnnotationHTML constructs the annotation section fragment for the /ui
// artifact viewer. It emits:
//   - A JSON data island (<script type="application/json" id="pf-annot-data">)
//     containing all commits with their anchor/reply/replies payload for JS.
//   - A flat no-JS thread list (grouped by heading, showing quote excerpts,
//     replies, and inline reply/resolve forms for open commits).
//   - A margin-rail scaffold (#pf-margin-rail hidden) for JS to populate.
//   - A hidden selection-comment form (#pf-selform) for JS to reveal.
//
// Returns "" when there are no commits AND no headings (non-spec artifact).
// The /v1 and /share paths never call this function.
//
// Annotations are per-version: only the commits stored on this memory row are
// rendered (no supersede-chain inheritance — decided 2026-06-03).
func buildAnnotationHTML(memID, renderedHTML string, commitsRaw json.RawMessage) string {
	// Parse commits.
	var commits []CommitEntry
	if len(commitsRaw) > 0 {
		_ = json.Unmarshal(commitsRaw, &commits)
	}

	// Extract heading refs from the rendered HTML.
	headings := extractHeadingsFromHTML(renderedHTML)

	if len(commits) == 0 && len(headings) == 0 {
		return ""
	}

	var b strings.Builder

	// ─── Data island ─────────────────────────────────────────────────────────
	// Build a minimal JSON payload for JS (margin bubbles + highlight placement).
	// "</script" is escaped to "<\/script" to prevent tag breakout.
	type islandAnchor struct {
		HeadingID   string `json:"heading_id"`
		HeadingText string `json:"heading_text"`
		Quote       string `json:"quote,omitempty"`
		Prefix      string `json:"prefix,omitempty"`
		Suffix      string `json:"suffix,omitempty"`
	}
	type islandReply struct {
		ID            string `json:"id"`
		AuthorDisplay string `json:"author_display"`
		Body          string `json:"body"`
		CreatedAt     string `json:"created_at"`
	}
	type islandCommit struct {
		ID            string        `json:"id"`
		AuthorDisplay string        `json:"author_display"`
		Body          string        `json:"body"`
		CreatedAt     string        `json:"created_at"`
		Status        string        `json:"status"`
		Reply         string        `json:"reply,omitempty"`
		ResolvedAt    string        `json:"resolved_at,omitempty"`
		ResolvedBy    string        `json:"resolved_by,omitempty"`
		Anchor        *islandAnchor `json:"anchor,omitempty"`
		Replies       []islandReply `json:"replies,omitempty"`
	}
	type islandPayload struct {
		MemID   string         `json:"mem_id"`
		Commits []islandCommit `json:"commits"`
	}

	islandCommits := make([]islandCommit, 0, len(commits))
	for _, e := range commits {
		ic := islandCommit{
			ID:            e.ID,
			AuthorDisplay: e.AuthorDisplay,
			Body:          e.Body,
			CreatedAt:     e.CreatedAt,
			Status:        e.Status,
			Reply:         e.Reply,
			ResolvedAt:    e.ResolvedAt,
			ResolvedBy:    e.ResolvedBy,
		}
		if e.Anchor != nil {
			ic.Anchor = &islandAnchor{
				HeadingID:   e.Anchor.HeadingID,
				HeadingText: e.Anchor.HeadingText,
				Quote:       e.Anchor.Quote,
				Prefix:      e.Anchor.Prefix,
				Suffix:      e.Anchor.Suffix,
			}
		}
		for _, r := range e.Replies {
			ic.Replies = append(ic.Replies, islandReply{
				ID:            r.ID,
				AuthorDisplay: r.AuthorDisplay,
				Body:          r.Body,
				CreatedAt:     r.CreatedAt,
			})
		}
		islandCommits = append(islandCommits, ic)
	}

	payload := islandPayload{MemID: memID, Commits: islandCommits}
	islandJSON, islandErr := json.Marshal(payload)
	if islandErr == nil {
		islandJSON = escapeJSONForScriptTag(islandJSON)
		b.WriteString("<script type=\"application/json\" id=\"pf-annot-data\">")
		b.Write(islandJSON)
		b.WriteString("</script>\n")
	}

	// ─── Flat fallback list v2 ────────────────────────────────────────────────
	b.WriteString("<section class=\"pf-annotations\">\n")
	b.WriteString("<h2 class=\"pf-annotations-heading\">Annotations</h2>\n")

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

	for _, h := range headings {
		addGroup(h.ID, h.Text)
	}
	for i := range commits {
		if commits[i].Anchor == nil || commits[i].Anchor.HeadingID == "" {
			addGroup("", "(general / unanchored)")
			break
		}
	}
	for i := range commits {
		hid, htxt := "", "(general / unanchored)"
		if commits[i].Anchor != nil && commits[i].Anchor.HeadingID != "" {
			hid = commits[i].Anchor.HeadingID
			htxt = commits[i].Anchor.HeadingText
		}
		addGroup(hid, htxt)
		groups[hid].Entries = append(groups[hid].Entries, commits[i])
	}

	for _, hid := range groupOrder {
		g := groups[hid]
		if len(g.Entries) == 0 {
			continue
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
			b.WriteString("\" data-commit-id=\"")
			b.WriteString(html.EscapeString(e.ID))
			b.WriteString("\">\n")

			b.WriteString("<div class=\"pf-annot-meta\"><strong>")
			b.WriteString(html.EscapeString(e.AuthorDisplay))
			b.WriteString("</strong> &middot; ")
			b.WriteString(html.EscapeString(e.CreatedAt))
			b.WriteString(" &middot; <span class=\"pf-annot-status\">")
			b.WriteString(statusLabel)
			b.WriteString("</span></div>\n")

			// Quote excerpt (aihub#125): display-truncate at ~120 runes.
			if e.Anchor != nil && e.Anchor.Quote != "" {
				q := e.Anchor.Quote
				const maxQuote = 120
				runes := []rune(q)
				ellipsis := ""
				if len(runes) > maxQuote {
					runes = runes[:maxQuote]
					ellipsis = "…"
				}
				b.WriteString("<div class=\"pf-annot-quote\">&ldquo;")
				b.WriteString(html.EscapeString(string(runes)))
				b.WriteString(ellipsis)
				b.WriteString("&rdquo;</div>\n")
			}

			b.WriteString("<div class=\"pf-annot-body\">")
			b.WriteString(html.EscapeString(e.Body))
			b.WriteString("</div>\n")

			// Legacy AI reply (resolved only).
			if e.IsResolved() && e.Reply != "" {
				b.WriteString("<div class=\"pf-annot-reply\"><strong>AI reply:</strong> ")
				b.WriteString(html.EscapeString(e.Reply))
				b.WriteString("</div>\n")
			}

			// Threaded replies (aihub#125).
			if len(e.Replies) > 0 {
				b.WriteString("<div class=\"pf-annot-replies\">\n")
				for _, r := range e.Replies {
					b.WriteString("<div class=\"pf-annot-reply-item\">\n")
					b.WriteString("<div class=\"pf-annot-reply-meta\"><strong>")
					b.WriteString(html.EscapeString(r.AuthorDisplay))
					b.WriteString("</strong> &middot; ")
					b.WriteString(html.EscapeString(r.CreatedAt))
					b.WriteString("</div>\n")
					b.WriteString("<div class=\"pf-annot-body\">")
					b.WriteString(html.EscapeString(r.Body))
					b.WriteString("</div>\n")
					b.WriteString("</div>\n")
				}
				b.WriteString("</div>\n")
			}

			// Inline reply + resolve forms for open commits (aihub#125).
			if e.IsOpen() {
				replyAction := "/ui/artifacts/" + html.EscapeString(memID) + "/commit/" + html.EscapeString(e.ID) + "/reply"
				resolveAction := "/ui/artifacts/" + html.EscapeString(memID) + "/commit/" + html.EscapeString(e.ID) + "/resolve"
				b.WriteString("<div class=\"pf-annot-inline-forms\">\n")
				b.WriteString("<form method=\"POST\" action=\"")
				b.WriteString(replyAction)
				b.WriteString("\" class=\"pf-annot-inline-form\">\n")
				b.WriteString("<textarea name=\"body\" rows=\"2\" placeholder=\"Reply…\" required></textarea>\n")
				b.WriteString("<button type=\"submit\">Reply</button>\n")
				b.WriteString("</form>\n")
				b.WriteString("<form method=\"POST\" action=\"")
				b.WriteString(resolveAction)
				b.WriteString("\" class=\"pf-annot-inline-form\">\n")
				b.WriteString("<textarea name=\"reply\" rows=\"2\" placeholder=\"Resolution note (optional)\"></textarea>\n")
				b.WriteString("<button type=\"submit\">Resolve</button>\n")
				b.WriteString("</form>\n")
				b.WriteString("</div>\n")
			}

			b.WriteString("</div>\n") // pf-annot-entry
		}
		b.WriteString("</div>\n") // pf-annot-section
	}

	// ─── Add-comment form (unchanged — heading-dropdown creation path) ────────
	b.WriteString("<div class=\"pf-annot-form\">\n")
	b.WriteString("<h3 class=\"pf-annot-form-title\">Add annotation</h3>\n")
	b.WriteString("<form method=\"POST\" action=\"/ui/artifacts/")
	b.WriteString(html.EscapeString(memID))
	b.WriteString("/commit\">\n")

	if len(headings) > 0 {
		b.WriteString("<label for=\"pf-annot-heading\">Section:</label>\n")
		b.WriteString("<select id=\"pf-annot-heading\" name=\"heading_id\"")
		b.WriteString(" onchange=\"document.getElementById('pf-annot-htxt').value=this.options[this.selectedIndex].dataset.text\">\n")
		b.WriteString("<option value=\"\" data-text=\"\">— general —</option>\n")
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

	// ─── Margin rail scaffold ─────────────────────────────────────────────────
	// JS removes [hidden] and populates bubbles from the data island.
	b.WriteString("<div id=\"pf-margin-rail\" hidden></div>\n")

	// ─── Hidden selection-comment form ────────────────────────────────────────
	// JS reveals + positions this on text selection.
	b.WriteString("<form id=\"pf-selform\" hidden method=\"POST\" action=\"/ui/artifacts/")
	b.WriteString(html.EscapeString(memID))
	b.WriteString("/commit\">\n")
	b.WriteString("<input type=\"hidden\" name=\"quote\" value=\"\">\n")
	b.WriteString("<input type=\"hidden\" name=\"prefix\" value=\"\">\n")
	b.WriteString("<input type=\"hidden\" name=\"suffix\" value=\"\">\n")
	b.WriteString("<input type=\"hidden\" name=\"heading_id\" value=\"\">\n")
	b.WriteString("<input type=\"hidden\" name=\"heading_text\" value=\"\">\n")
	b.WriteString("<textarea name=\"body\" rows=\"3\" placeholder=\"Add annotation for selection…\" required></textarea>\n")
	b.WriteString("<button type=\"submit\">Add</button>\n")
	b.WriteString("</form>\n")

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

// RegisterUIArtifactReplyResolveRoutes registers the /ui-only POST reply and
// resolve routes for artifact-scoped commits (aihub#125). Called from
// RegisterUIRoutes after the auth middleware is set up.
func RegisterUIArtifactReplyResolveRoutes(uiGroup *echo.Group, pool *pgxpool.Pool) {
	uiGroup.POST("/artifacts/:id/commit/:commit_id/reply", handleUIArtifactReplyCommit(pool))
	uiGroup.POST("/artifacts/:id/commit/:commit_id/resolve", handleUIArtifactResolveCommit(pool))
}

// artifactRedirectURL builds the 303 redirect target for artifact-scoped write
// operations: always back to the artifact HTML page.
func artifactRedirectURL(memID string) string {
	return "/ui/artifacts/" + url.PathEscape(memID) + "/html"
}

// handleUIArtifactReplyCommit handles POST /ui/artifacts/:id/commit/:commit_id/reply.
//
// Appends a threaded reply to a commit on a spec/plan artifact. Thin wrapper
// around doReplyCommitFn (same seam as the memory reply handler). Redirects 303
// back to the artifact HTML page.
func handleUIArtifactReplyCommit(pool *pgxpool.Pool) echo.HandlerFunc {
	return func(c echo.Context) error {
		u := GetUser(c)
		if u == nil {
			return redirectToLogin(c)
		}
		memID := c.Param("id")
		commitID := c.Param("commit_id")
		if memID == "" || commitID == "" {
			return writeError(c, domain.NewErr(domain.ErrBadRequest, "artifact id and commit id are required"))
		}
		body := c.FormValue("body")
		if body == "" {
			return writeError(c, domain.NewErr(domain.ErrBadRequest, "body is required"))
		}

		ctx, cancel := contextWithTimeout(c)
		defer cancel()

		project, _, loadErr := commitMemoryProjectFn(ctx, pool, memID)
		if loadErr != nil {
			return writeError(c, domain.NewErr(domain.ErrNotFound, "artifact not found"))
		}
		if err := checkProjectAccess(c, u, project, "writer"); err != nil {
			return err
		}
		if err := doReplyCommitFn(ctx, pool, memID, commitID, u.UserID, u.DisplayName, body); err != nil {
			return domainErr(c, err)
		}
		return c.Redirect(http.StatusSeeOther, artifactRedirectURL(memID))
	}
}

// handleUIArtifactResolveCommit handles POST /ui/artifacts/:id/commit/:commit_id/resolve.
//
// Marks a commit as resolved with an optional resolution reply. Thin wrapper
// around doResolveCommitFn. Redirects 303 back to the artifact HTML page.
func handleUIArtifactResolveCommit(pool *pgxpool.Pool) echo.HandlerFunc {
	return func(c echo.Context) error {
		u := GetUser(c)
		if u == nil {
			return redirectToLogin(c)
		}
		memID := c.Param("id")
		commitID := c.Param("commit_id")
		if memID == "" || commitID == "" {
			return writeError(c, domain.NewErr(domain.ErrBadRequest, "artifact id and commit id are required"))
		}

		ctx, cancel := contextWithTimeout(c)
		defer cancel()

		project, _, loadErr := commitMemoryProjectFn(ctx, pool, memID)
		if loadErr != nil {
			return writeError(c, domain.NewErr(domain.ErrNotFound, "artifact not found"))
		}
		if err := checkProjectAccess(c, u, project, "writer"); err != nil {
			return err
		}
		reply := c.FormValue("reply")
		if err := doResolveCommitFn(ctx, pool, memID, commitID, reply, u.UserID, u.DisplayName); err != nil {
			return domainErr(c, err)
		}
		return c.Redirect(http.StatusSeeOther, artifactRedirectURL(memID))
	}
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
		anchor := domain.CommitAnchorArgs{
			HeadingID:   c.FormValue("heading_id"),
			HeadingText: c.FormValue("heading_text"),
			Quote:       c.FormValue("quote"),
			Prefix:      c.FormValue("prefix"),
			Suffix:      c.FormValue("suffix"),
		}

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

		if err := doArtifactCommitFn(ctx, pool, memID, body, u.UserID, u.DisplayName, anchor); err != nil {
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

// buildVersionHistoryHTML returns a collapsible HTML fragment listing all
// versions in the supersede chain for memID. Returns "" when the chain has ≤1
// entry (nothing to show) or on error (best-effort, non-fatal). Only called
// on the /ui path.
//
// For versions that have a linked review (keyed via attrs.structured_payload
// .reviewed_memory_id), a "Review" link is emitted.
// aihub#138: converted to collapsible <details> and added per-version review link.
func buildVersionHistoryHTML(ctx context.Context, pool *pgxpool.Pool, memID string, versions []domain.MemoryVersionRef) string {
	if len(versions) <= 1 {
		return ""
	}

	// Build a map of version_id → review_mem_id for versions that have a review.
	// Best-effort: errors silently ignored (non-fatal).
	reviewLinks := buildVersionReviewLinks(ctx, pool, versions)

	var b strings.Builder
	b.WriteString("<section class=\"pf-version-history\">\n")
	nVers := len(versions)
	b.WriteString("<button type=\"button\" class=\"pf-version-history-toggle\" onclick=\"")
	b.WriteString("var p=this.nextElementSibling;var c=this.querySelector('.pf-vchev');")
	b.WriteString("p.hidden=!p.hidden;if(c)c.classList.toggle('open',!p.hidden);\">")
	b.WriteString("<span class=\"pf-vchev\"></span>History &mdash; ")
	b.WriteString(strconv.Itoa(nVers))
	b.WriteString(" version")
	if nVers != 1 {
		b.WriteString("s")
	}
	b.WriteString("</button>\n")
	b.WriteString("<div class=\"pf-version-history-panel\" hidden>\n")
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
		b.WriteString("<span class=\"pf-version-dot\"></span>")
		b.WriteString("<span class=\"pf-version-content\">")
		if v.ID == memID {
			// Currently viewed version: plain text, mark it.
			b.WriteString("<span class=\"pf-version-label\">")
			b.WriteString(html.EscapeString(label))
			b.WriteString("</span>")
			b.WriteString(" <span class=\"pf-version-badge\">viewing</span>")
		} else {
			b.WriteString("<a href=\"/ui/artifacts/")
			b.WriteString(html.EscapeString(v.ID))
			b.WriteString("/html\" class=\"pf-version-label\">")
			b.WriteString(html.EscapeString(label))
			b.WriteString("</a>")
		}
		if v.IsCurrent && v.ID != memID {
			b.WriteString(" <span class=\"pf-version-badge pf-version-head\">current</span>")
		}
		b.WriteString("<div class=\"pf-version-ts\">")
		b.WriteString(html.EscapeString(ts))
		b.WriteString("</div>")
		b.WriteString("</span>") // pf-version-content
		// Per-version actions: View (when not currently viewed) + Review link if available.
		if v.ID != memID || reviewLinks[v.ID] != "" {
			b.WriteString("<span class=\"pf-version-actions\">")
			if v.ID != memID {
				b.WriteString("<a href=\"/ui/artifacts/")
				b.WriteString(html.EscapeString(v.ID))
				b.WriteString("/html\">View</a>")
			}
			if reviewID := reviewLinks[v.ID]; reviewID != "" {
				b.WriteString("<a href=\"/ui/artifacts/")
				b.WriteString(html.EscapeString(reviewID))
				b.WriteString("/html\" class=\"pf-review-link\">Review</a>")
			}
			b.WriteString("</span>")
		}
		b.WriteString("</li>\n")
	}
	b.WriteString("</ol>\n")
	b.WriteString("</div>\n") // pf-version-history-panel
	b.WriteString("</section>\n")
	return b.String()
}

// buildVersionReviewLinks queries the DB for methodology.review memories whose
// structured_payload.reviewed_memory_id references any version in the chain.
// Returns a map of reviewed_version_id → review_memory_id (first match wins).
// Best-effort: returns an empty map on any error.
func buildVersionReviewLinks(ctx context.Context, pool *pgxpool.Pool, versions []domain.MemoryVersionRef) map[string]string {
	result := map[string]string{}
	if pool == nil || len(versions) == 0 {
		return result
	}
	ids := make([]string, len(versions))
	for i, v := range versions {
		ids[i] = v.ID
	}
	// Query reviews whose attrs->>'structured_payload'->'reviewed_memory_id' is in the version set.
	// The payload is stored as JSONB in attrs under key "structured_payload".
	const q = `
SELECT id,
       attrs->'structured_payload'->>'reviewed_memory_id' AS reviewed_id
FROM memories
WHERE type = 'methodology.review'
  AND attrs->'structured_payload'->>'reviewed_memory_id' = ANY($1)
  AND status != 'redacted'
`
	rows, err := pool.Query(ctx, q, ids)
	if err != nil {
		return result
	}
	defer rows.Close()
	for rows.Next() {
		var reviewID, reviewedID string
		if err := rows.Scan(&reviewID, &reviewedID); err != nil {
			continue
		}
		if _, exists := result[reviewedID]; !exists {
			result[reviewedID] = reviewID
		}
	}
	if err := rows.Err(); err != nil {
		return result
	}
	return result
}

// ─── aihub#138: review viewer ────────────────────────────────────────────────

// reviewPayload is the structured_payload shape for methodology.review memories.
// Two shapes are supported:
//
//  1. Spec shape (pf-spec/pf-plan review): verdict + findings + checked + outcome +
//     reviewed_memory_id + reviewed_version.
//  2. Quick-review shape (pf-review quick): result + level + issues.
//
// Fields are intentionally all optional so partial payloads degrade gracefully.
type reviewPayload struct {
	// Spec-shape fields
	Verdict          string          `json:"verdict,omitempty"`
	Findings         []reviewFinding `json:"findings,omitempty"`
	Checked          []string        `json:"checked,omitempty"`
	Outcome          string          `json:"outcome,omitempty"`
	ReviewedMemoryID string          `json:"reviewed_memory_id,omitempty"`
	ReviewedVersion  string          `json:"reviewed_version,omitempty"`
	// Quick-review shape fields (real deployed reviews use these)
	Result string        `json:"result,omitempty"` // PASS|WARN|FAIL
	Level  string        `json:"level,omitempty"`  // quick|deep
	Issues []reviewIssue `json:"issues,omitempty"`
}

type reviewFinding struct {
	Severity string `json:"severity"` // mustfix|should|nit
	Title    string `json:"title"`
	Body     string `json:"body"`
	Section  string `json:"section,omitempty"` // optional target section link
}

type reviewIssue struct {
	Severity string `json:"severity"` // warning|minor|info
	Text     string `json:"text"`
}

// buildReviewHTML builds the /ui-only review viewer fragment for a
// methodology.review artifact. Falls back to the plain markdown body when
// structured_payload is absent or cannot be parsed.
func buildReviewHTML(mem *domain.Memory) string {
	var payload reviewPayload
	hasPayload := false
	if len(mem.Attrs) > 0 {
		var attrsObj map[string]json.RawMessage
		if err := json.Unmarshal(mem.Attrs, &attrsObj); err == nil {
			if spRaw, ok := attrsObj["structured_payload"]; ok {
				if err := json.Unmarshal(spRaw, &payload); err == nil {
					hasPayload = payload.Verdict != "" || payload.Result != "" ||
						len(payload.Findings) > 0 || len(payload.Issues) > 0
				}
			}
		}
	}
	if !hasPayload {
		// No usable structured payload — no review chrome injected.
		// The bodyFragment is already rendered as the document body by the
		// caller (renderArtifactBodyWithMeta). Return "" so the caller uses
		// the plain document path.
		return ""
	}

	var b strings.Builder

	// Verdict / result banner.
	verdict := payload.Verdict
	if verdict == "" {
		verdict = payload.Result // quick-review shape
	}
	if verdict != "" {
		verdictClass := "pf-verdict-unknown"
		lv := strings.ToLower(verdict)
		switch {
		case strings.HasPrefix(lv, "pass"):
			verdictClass = "pf-verdict-pass"
		case strings.HasPrefix(lv, "warn"):
			verdictClass = "pf-verdict-warn"
		case strings.HasPrefix(lv, "fail"):
			verdictClass = "pf-verdict-fail"
		}
		b.WriteString("<div class=\"pf-review-verdict ")
		b.WriteString(verdictClass)
		b.WriteString("\">Verdict: ")
		b.WriteString(html.EscapeString(verdict))
		if payload.Level != "" {
			b.WriteString(" (")
			b.WriteString(html.EscapeString(payload.Level))
			b.WriteString(")")
		}
		b.WriteString("</div>\n")
	}

	// Findings (spec-shape) — richer with title + body.
	if len(payload.Findings) > 0 {
		b.WriteString("<h2>Findings</h2>\n")
		for _, f := range payload.Findings {
			b.WriteString("<div class=\"pf-review-find\">\n")
			b.WriteString("<div class=\"pf-review-find-head\">")
			badgeClass := reviewSeverityBadgeClass(f.Severity)
			b.WriteString("<span class=\"pf-badge ")
			b.WriteString(badgeClass)
			b.WriteString("\">")
			b.WriteString(html.EscapeString(f.Severity))
			b.WriteString("</span>")
			if f.Title != "" {
				b.WriteString("<b>")
				b.WriteString(html.EscapeString(f.Title))
				b.WriteString("</b>")
			}
			b.WriteString("</div>\n")
			if f.Body != "" {
				b.WriteString("<p>")
				b.WriteString(html.EscapeString(f.Body))
				if f.Section != "" {
					b.WriteString(" <a href=\"#")
					b.WriteString(html.EscapeString(f.Section))
					b.WriteString("\">jump to section</a>")
				}
				b.WriteString("</p>\n")
			}
			b.WriteString("</div>\n")
		}
	}

	// Issues (quick-review shape) — flat severity + text.
	if len(payload.Issues) > 0 {
		b.WriteString("<h2>Findings</h2>\n")
		for _, issue := range payload.Issues {
			b.WriteString("<div class=\"pf-review-find\">\n")
			b.WriteString("<div class=\"pf-review-find-head\">")
			badgeClass := reviewSeverityBadgeClass(issue.Severity)
			b.WriteString("<span class=\"pf-badge ")
			b.WriteString(badgeClass)
			b.WriteString("\">")
			b.WriteString(html.EscapeString(issue.Severity))
			b.WriteString("</span>")
			b.WriteString("</div>\n")
			if issue.Text != "" {
				b.WriteString("<p>")
				b.WriteString(html.EscapeString(issue.Text))
				b.WriteString("</p>\n")
			}
			b.WriteString("</div>\n")
		}
	}

	// Checked list.
	if len(payload.Checked) > 0 {
		b.WriteString("<h2>Checked</h2>\n")
		b.WriteString("<ul class=\"pf-review-checked-list\">\n")
		for _, item := range payload.Checked {
			b.WriteString("<li><span class=\"pf-review-check-ok\"></span>")
			b.WriteString(html.EscapeString(item))
			b.WriteString("</li>\n")
		}
		b.WriteString("</ul>\n")
	}

	// Outcome.
	if payload.Outcome != "" {
		b.WriteString("<h2>Outcome</h2>\n")
		b.WriteString("<p class=\"pf-review-outcome\">")
		b.WriteString(html.EscapeString(payload.Outcome))
		b.WriteString("</p>\n")
	}

	return b.String()
}

// reviewSeverityBadgeClass maps a finding/issue severity string to a CSS badge class.
func reviewSeverityBadgeClass(severity string) string {
	switch strings.ToLower(severity) {
	case "mustfix", "must-fix", "must_fix", "critical", "error":
		return "pf-b-mustfix"
	case "should", "suggestion", "warning":
		return "pf-b-should"
	case "nit", "minor", "note":
		return "pf-b-nit"
	case "info":
		return "pf-b-info"
	default:
		return "pf-b-nit"
	}
}
