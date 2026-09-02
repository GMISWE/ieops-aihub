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
// Content-Security-Policy for authed artifact/document responses (aihub#240,
// resolves #144). Until now only the anonymous /share path sent a CSP; /ui and /v1
// sent none, which meant a project member — the reader with a session — was the least
// protected of the three.
//
// Two policies, because the two surfaces load genuinely different things.
const (
	// artifactV1CSP locks down /v1/artifacts/:id/html AND /share/:id. Both responses are
	// pure content documents: no first-party stylesheet, no script, no fetch. So both run
	// the lockdown /share has used since the share feature shipped.
	//
	// handleSharedArtifact references this identifier rather than restating the policy. It
	// used to hold its own copy of the same string, under a comment here claiming the two
	// were kept identical so they could not drift — a claim nothing checked, since the
	// equality test only compared substrings of this constant and never read /share's copy.
	// One identifier makes it structural; TestArtifactV1CSP_MatchesSharePolicy now compares
	// the headers both handlers actually emit.
	artifactV1CSP = "default-src 'none'; style-src 'unsafe-inline'; img-src data: https:; " +
		"form-action 'none'; base-uri 'none'; frame-ancestors 'none'"
)

// uiPageCSPWithNonce builds the policy for ONE /ui response: the artifact viewer and the
// memory/wi detail pages that render agent markdown through the {{md}} / {{agentdoc}} helpers.
//
// It cannot be as strict as artifactV1CSP because /ui serves its own assets
// (ui.css, viewer.css, annot.js, annotator.js, share.js, theme.js, diagram.js)
// and emits inline <script> for the synchronous theme setter and the side-rail
// IntersectionObserver.
//
// script-src carries a per-response nonce instead of 'unsafe-inline' (aihub#243). That
// closes the weakening #240 shipped knowingly: with 'unsafe-inline', CSP alone would not
// stop an inline script in agent content, so sanitization (SanitizeArtifactHTML) was the
// only real control and CSP was decoration. Now an inline script must name this response's
// nonce to run, and the nonce is 128 bits of crypto/rand minted per request — an agent
// authoring content cannot know it, and it is never reused across responses.
//
// Note what a nonce does and does not neuter. Adding a nonce to script-src makes browsers
// IGNORE 'unsafe-inline' (which is why it is now removed rather than left as a fallback for
// older browsers — leaving it would be a lie in modern ones and a hole in ancient ones), but
// it does NOT affect 'self'. Every <script src="/ui/static/..."> tag on these pages keeps
// loading under 'self' untouched; only the three inline blocks this package emits need the
// attribute.
//
// The nonce is ALSO handed to the sandboxed srcdoc frames, and that is load-bearing rather
// than tidy — see EmbedOptions.Nonce in internal/render/safeembed.go. A srcdoc frame inherits
// the embedding page's policy container, so a frame minting its own nonce would be refused by
// this policy and its height-reporting bridge would die silently.
//
// What this policy buys beyond script-src: no external origin may be contacted or loaded
// from, object/embed are dead, <base> cannot be rewritten, and the page cannot be framed
// cross-origin.
func uiPageCSPWithNonce(nonce string) string {
	return "default-src 'none'; " +
		"script-src 'self' 'nonce-" + nonce + "'; " +
		"style-src 'self' 'unsafe-inline'; " +
		"img-src 'self' data:; " +
		"font-src 'self' data:; " +
		"connect-src 'self'; " +
		"form-action 'self'; " +
		"base-uri 'none'; " +
		"object-src 'none'; " +
		"frame-ancestors 'self'"
}

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

		// aihub#248: /ui only — if this id has been superseded (mem.LatestID !=
		// nil and != mem.ID), 302 to the lineage head's /ui URL instead of
		// silently serving stale content. /v1 and /share stay exact-ID and
		// byte-identical (mem_A6540SyP), hence the path-prefix gate — same
		// pattern already used below for sanitization/CSP branching.
		//
		// The head is re-authorized here with the PURE predicates
		// hasProjectAccess/memoryVisibleTo, never the side-effecting
		// checkProjectAccess/checkMemoryVisibility above: those commit a
		// 403/401 response to c on denial, which would leak "a newer version
		// exists but you can't see it" on the very path meant to silently fall
		// back to mem. UpdateMemory lets a new version's Visibility (and, in
		// principle, Project) diverge from its predecessor's, so reusing the
		// authorization decision already made for mem would be a privilege
		// escalation onto the head. Any failure to resolve or authorize the
		// head falls back to serving mem exactly as today — never a 404/403.
		//
		// aihub#248 review (W2): head.ID != mem.ID only rules out a self-redirect;
		// also require head.LatestID == nil || *head.LatestID == head.ID so a head
		// whose own cursor points elsewhere (multi-hop, or a 2-cycle) does not
		// redirect again — defensive even though normal write-path invariants
		// keep every real head self-headed.
		//
		// aihub#248 review (blocking, spec amendment to non-goal 6): a caller
		// that followed a deliberate past-version link — the side rail's
		// "Version history" rows below, or wi_detail.html.tmpl's per-version
		// "View" link — arrives with ?pf_exact=1 and must see that exact
		// revision, not the head. isExactVersionRequest skips head resolution
		// only; it never bypasses the checkProjectAccess/checkMemoryVisibility
		// gates on mem above.
		if strings.HasPrefix(c.Path(), "/ui") && !isExactVersionRequest(c) &&
			mem.LatestID != nil && *mem.LatestID != mem.ID {
			if head, aerr := resolveLatestFn(ctx, pool, mem.ID); aerr == nil && head != nil &&
				head.ID != mem.ID &&
				(head.LatestID == nil || *head.LatestID == head.ID) &&
				hasProjectAccess(u, head.Project, "viewer") && memoryVisibleTo(u, head) {
				return c.Redirect(http.StatusFound, appendQueryString(c, "/ui/artifacts/"+url.PathEscape(head.ID)+"/html"))
			}
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

		// aihub#240 (resolves #144): agent-authored HTML reaches an authed reader here.
		//
		// Two different treatments, because the two routes have different contracts:
		//
		//   /ui — the body is sanitized in place, before any chrome is injected below.
		//         Sanitizing here and not later matters: everything after this point
		//         appends OUR OWN markup (share control, annotation scaffold, side rail,
		//         first-party <script> tags), and running the sanitizer over that would
		//         strip the viewer's own behaviour.
		//
		//   /v1 — the body is left exactly as-is. aihub#138 makes /v1 and /share
		//         contractually byte-identical and TestArtifactViewer_UIvsV1Share_
		//         BytePurity enforces it, so the payload is neutralised by the response
		//         CSP instead (set below) rather than by rewriting bytes. This is the
		//         same trade /share itself already makes.
		// aihub#240 D7: an architecture-doc artifact carries agent-AUTHORED HTML — it is
		// the Step 1 output of the three-step design (agent writes md + html; the viewer
		// renders the html half). That half is the one case on this page where the body is
		// untrusted rich content rather than our own markdown render, so it goes inside the
		// sandboxed frame instead of being inlined. This is what makes Step 3 real here.
		//
		// Gated on type rather than a stored flag: resolveRenderedHTML (domain/memory.go:77)
		// distinguishes agent-supplied HTML from auto-rendered markdown at WRITE time, but
		// does not persist which one it took. fact.architecture is not in renderTypes, so a
		// non-NULL rendered_html on this type can only have come from the author — the
		// condition below is exactly "agent-authored html", with no schema change. Promoting
		// it to an explicit contract flag is P1 (proposal §7 open question 3).
		//
		// The frame is NOT given the pre-processed body: SafeEmbedDocument sanitizes and
		// THEN compiles d2 internally, and that order is load-bearing (compiling first costs
		// d2 its entire stylesheet — see ui_embed.go). So the sanitize + compile + fold steps
		// below are all skipped for this path and the raw body is handed over untouched.
		//
		// Annotation, viewer.css and diagram.js do not cross the frame boundary yet, so they
		// are inert on this artifact type. That is aihub#245, tracked as P1 and explicitly
		// accepted for the P0 demo (mem_YlnN3R8H).
		sandboxBody := strings.HasPrefix(c.Path(), "/ui") &&
			mem.Type == "fact.architecture" && mem.RenderedHTML != nil

		if strings.HasPrefix(c.Path(), "/ui") {
			if !sandboxBody {
				bodyFragment = render.SanitizeArtifactHTML(bodyFragment)
			}
		} else {
			// /ui gets its policy from the uiGroup middleware (ui_routes.go), which
			// also covers the {{md}} memory/wi detail pages. /v1 has no such group.
			h := c.Response().Header()
			h.Set("Content-Security-Policy", artifactV1CSP)
			h.Set("X-Content-Type-Options", "nosniff")
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
			// This response's CSP nonce (aihub#243). Every inline <script> below must carry
			// it or the policy refuses to run it; the value comes from uiSecurityHeaders,
			// which put the same nonce in the header.
			nonce := uiNonce(c)
			var uiHead strings.Builder
			// Set data-theme on <html> immediately (inline script runs synchronously).
			// For review pages, also add pf-review-page class so CSS grid + order work.
			if mem.Type == "methodology.review" {
				uiHead.WriteString("<script nonce=\"" + html.EscapeString(nonce) + "\">(function(){document.documentElement.setAttribute('data-theme','")
				uiHead.WriteString(theme)
				uiHead.WriteString("');document.addEventListener('DOMContentLoaded',function(){document.body.classList.add('pf-review-page');});")
				uiHead.WriteString("})();</script>\n")
			} else {
				uiHead.WriteString("<script nonce=\"" + html.EscapeString(nonce) + "\">(function(){document.documentElement.setAttribute('data-theme','")
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
			// aihub#240 D7: embedframe.js is the PARENT half of the frame's height protocol —
			// the framed document measures itself and postMessages the height; this listener
			// applies it. The app shell gets it from layout.html.tmpl, but the artifact viewer
			// builds its own head and never included it, so a framed body here sat at the
			// stylesheet's default 220px with an inner scrollbar while the bridge posted a
			// correct 1589 into a page with nobody listening. Only needed when the body is
			// actually framed.
			if sandboxBody {
				uiHead.WriteString("<script src=\"/ui/static/embedframe.js?v=")
				uiHead.WriteString(assetVersion)
				uiHead.WriteString("\" defer></script>\n")
			}
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
			// only for artifacts the share handler would actually accept. It used
			// to be gated on rendered_html alone, which matched the handler when
			// the handler's only content check was the same one. aihub#151 added
			// two more refusals, so the gate now asks shareRefusal as well —
			// otherwise a note saved with an explicit html= payload gets a Share
			// button that always answers 403. "Gate the control on the same
			// condition as the handler" is the claim this comment has always made;
			// calling the handler's own predicate is what makes it true rather
			// than a thing two places happen to agree on.
			// The fragment (control + script) is injected just
			// after the first </h1> so it renders directly below the document title;
			// it never reaches /v1 or /share, preserving their byte-identical output.
			shareControlHTML := ""
			// aihub#240 D7: the control is injected INTO the body, and the framed body is
			// a separate document whose CSP admits no script but our nonced bridge — the
			// control would render inert inside the frame. Suppressed rather than shipped
			// broken; rehoming it to the parent chrome is part of aihub#245 (P1).
			if mem.RenderedHTML != nil && shareRefusal(mem) == nil && !sandboxBody {
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
				// aihub#240 D7: skipped when the body is going into the frame —
				// SafeEmbedDocument does sanitize-then-compile itself, in that order.
				if !sandboxBody {
					bodyFragment = render.RenderDiagramsGated(bodyFragment)
				}
				// aihub#159: fold H2 sections into <details> for /ui readability (spec/plan).
				// /ui-only — /v1 + /share keep the flat body. Default-open so annot.js
				// text-quote anchoring (searches visible text) is unaffected.
				// aihub#240 D7: folding rewrites the body's section structure, which is a
				// parent-page readability affordance. Inside the frame it would only mangle
				// the agent's own layout, so the framed body is left exactly as authored.
				if !sandboxBody {
					bodyFragment = wrapH2SectionsForUI(bodyFragment)
				}
				annotHTML = buildAnnotationHTMLWithExact(mem.ID, bodyFragment, mem.Commits, nonce, isExactVersionRequest(c))
				// aihub#138 version_history: render version history INSIDE the doc card.
				// aihub#154: the share control + version history are injected together
				// just after the first </h1> — share above, version history below — so
				// the order is title → share → version history → body. Empty pieces
				// (single-version chain, or NULL rendered_html) collapse out cleanly.
				// aihub#159 step4b: version history relocates into the side rail (below);
				// only the share control stays injected in-card under the title.
				var srVersions []sideRailVersion
				if versions, verErr := versionChainFn(ctx, pool, mem.ID); verErr == nil && len(versions) > 1 {
					for _, v := range versions {
						// aihub#248 review (W1): domain.MemoryVersionChain's SQL filters
						// only status != 'redacted' — no project/visibility predicate — so
						// its rows (domain.MemoryVersionRef: id/created_at/status/is_current
						// only) are not enough on their own to answer "can THIS caller see
						// this row". Without this check a caller denied a lineage member
						// would still see that row's id, date, and link rendered in this
						// side rail — exactly the leak spec decision 4 exists to prevent.
						// v.ID == mem.ID is always safe to include as-is: mem was already
						// authorized above via the side-effecting
						// checkProjectAccess/checkMemoryVisibility. Every other row needs
						// its own full record — MemoryVersionRef carries no Project or
						// Visibility — loaded via the same loadMemoryFn seam used for the
						// primary record, then checked with the same pure predicates
						// (hasProjectAccess/memoryVisibleTo) used for the head redirect
						// above, so a denied row is omitted entirely rather than merely
						// stripped of its link.
						if v.ID != mem.ID {
							full, ferr := loadMemoryFn(ctx, pool, v.ID)
							// aihub#248 review (minor 5): ferr != nil is deliberately treated
							// the same as "denied" (fail-closed), not a bug to be "fixed" into
							// fail-open. This loop is on a leak-prevention path (W1): a
							// transient load failure that instead rendered the row would risk
							// showing an id/date/link the caller may not be authorized for.
							// Losing one row on a rare transient DB hiccup is an acceptable
							// cost for never leaking on a permissions path.
							if ferr != nil || full == nil ||
								!hasProjectAccess(u, full.Project, "viewer") || !memoryVisibleTo(u, full) {
								continue
							}
						}
						// aihub#248 review (minor 4): label from the FILTERED slice
						// (len(srVersions), the count of rows already kept), not the
						// unfiltered chain index. Numbering from the unfiltered index
						// would render e.g. v1, v3, v4 after a v2 is filtered out above —
						// disclosing that a hidden version exists, the same class of leak
						// W1 fixed for id/date/href.
						sv := sideRailVersion{Label: "v" + strconv.Itoa(len(srVersions)+1), Current: v.IsCurrent}
						if len(v.CreatedAt) >= 10 {
							sv.Date = v.CreatedAt[:10]
						}
						if v.ID != mem.ID {
							// aihub#248: this row deliberately targets a specific past
							// revision, not the lineage head, so it carries the pf_exact
							// marker to opt out of the /ui redirect above (spec amendment
							// to non-goal 6).
							sv.Href = "/ui/artifacts/" + url.PathEscape(v.ID) + "/html?" + exactVersionParam + "=1"
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
				annotHTML += buildSideRail(render.ExtractHeadings(mem.Content), srMeta, srVersions, srComments, nonce)
			}
		}
		// aihub#240 D7: hand the body to the sandbox last, so everything above operated on
		// the authored bytes and nothing downstream can re-process the frame markup.
		// SafeEmbedDocument sanitizes, compiles d2, and wraps the result in
		// <iframe srcdoc sandbox="allow-scripts"> (no allow-same-origin) with its own inner
		// CSP — the same call the {{md}} pages already use (ui_embed.go).
		if sandboxBody {
			bodyFragment = render.SafeEmbedDocument(bodyFragment, render.EmbedOptions{
				Title:        "document",
				BridgeScript: render.AnnotationBridgeFor(c.Scheme() + "://" + c.Request().Host),
				FrameClass:   "pf-embed",
				// Read again rather than reusing the /ui block's local: that one is scoped to
				// the chrome builder above, and themeFromCookie is a pure cookie read.
				Theme: themeFromCookie(c),
				// The frame inherits THIS page's policy container, so its bridge has to run
				// under THIS page's nonce. Letting SafeEmbedDocument mint its own would leave
				// the conjunction admitting neither value and kill the height protocol
				// silently (aihub#243).
				Nonce: uiNonce(c),
			})
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

func buildSideRail(headings []render.HeadingRef, m sideRailMeta, versions []sideRailVersion, comments []sideRailComment, nonce string) string {
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
	b.WriteString(`<script nonce="` + html.EscapeString(nonce) + `">(function(){var ls=document.querySelectorAll('#pf-side-rail .pf-side-toc a');if(!ls.length||!window.IntersectionObserver)return;var m={};ls.forEach(function(a){m[a.getAttribute('href').slice(1)]=a;});var io=new IntersectionObserver(function(es){es.forEach(function(e){if(e.isIntersecting){for(var k in m){m[k].classList.remove('active');}var a=m[e.target.id];if(a){a.classList.add('active');}}});},{rootMargin:'-80px 0px -70% 0px'});Object.keys(m).forEach(function(id){var el=document.getElementById(id);if(el){io.observe(el);}});})();(function(){document.querySelectorAll('#pf-side-rail .pf-side-cmt-item').forEach(function(btn){btn.addEventListener('click',function(e){e.stopPropagation();var id=btn.getAttribute('data-commit-id');var mk=document.querySelector('.pf-annot-marker[data-commit-id="'+id+'"]')||document.querySelector('mark[data-commit-id="'+id+'"]');if(mk){mk.scrollIntoView({behavior:'smooth',block:'center'});mk.click();}});});})();</script>` + "\n")
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
		// fetch/form, and nosniff prevents content-type confusion.
		//
		// artifactV1CSP is the same policy, referenced rather than restated. It used to be
		// duplicated here as a literal, with a comment on the constant claiming the two were
		// "kept deliberately identical so they cannot drift apart" — which nothing enforced:
		// the equality test only compared substrings of the constant and never read this
		// string at all. Sharing the identifier is what makes the claim true.
		//
		// /v1 now sends this too (aihub#240 / #144), so both authed and anonymous artifact
		// responses carry it; the difference between the routes is that /ui sanitizes its
		// body while /v1 and /share serve byte-identical stored bytes (aihub#138).
		h := c.Response().Header()
		h.Set("Content-Security-Policy", artifactV1CSP)
		h.Set("X-Content-Type-Options", "nosniff")
		title := mem.ID + " (" + mem.Type + ")"
		// renderArtifactBody (not render.Document) so a custom full-document artifact
		// (pf_save_artifact html=) is served verbatim instead of double-wrapped; ""
		// backHref because an anonymous viewer has no /ui/wi to navigate back to.
		return c.HTMLBlob(http.StatusOK, []byte(renderArtifactBody(*mem.RenderedHTML, title, "")))
	}
}

// nonPublishableVisibilities are the tiers that are NARROWER than the project, and
// so cannot be published to the anonymous /share/:id route by a project writer
// (aihub#151). 'private' is author-only and 'admin' is global-admin-only; a writer
// who is neither would otherwise be publishing someone else's restricted memory to
// the whole internet, and the round trip back through unshare cannot restore what
// was never a project-wide tier to begin with.
var nonPublishableVisibilities = map[string]bool{
	"private": true,
	"admin":   true,
}

// shareRefusal returns the reason this memory may not be published to the
// anonymous /share/:id route, or nil if there is none. It covers the two grounds
// that do NOT depend on rendered_html, both added by aihub#151:
//
//  1. the memory's TYPE must be a configured render type (domain.IsRenderType).
//     "has rendered_html" is not an artifact test: resolveRenderedHTML stores an
//     explicit `html=` verbatim for any type at all, so before this check a writer
//     could pf_save_artifact a note/decision with an html payload and publish it
//     world-readable.
//  2. the memory's current visibility must not be narrower than the project
//     (see nonPublishableVisibilities). Note that this is a property of the ROW,
//     not of the caller: even the author of a private memory has to widen it to
//     the project first, so that "who could already read this" is a question with
//     one answer before it is published to everyone. That is why both are 403 and
//     not, say, 404 — the object is not a publishable one, whoever is asking.
//
// It is a function rather than two inline checks because handleArtifactHTML asks
// the same question when it decides whether to render the /ui Share button. Two
// copies would put a button on the page that always 403s — which is what the
// first draft of this change did.
func shareRefusal(mem *domain.Memory) *domain.AihubError {
	if !domain.IsRenderType(mem.Type) {
		return domain.NewErr(domain.ErrForbidden,
			fmt.Sprintf("memory type %q is not a shareable artifact type (shareable: %s)",
				mem.Type, strings.Join(domain.RenderTypeNames(), ", ")))
	}
	if nonPublishableVisibilities[mem.Visibility] {
		return domain.NewErr(domain.ErrForbidden,
			fmt.Sprintf("artifact visibility %q is narrower than the project and cannot be published; change it to \"project\" first if that is what you intend", mem.Visibility))
	}
	return nil
}

// handleShareArtifact marks a spec/plan artifact public so it can be viewed without auth
// at /share/:id. Requires writer on the artifact's project, then shareRefusal (403),
// then rendered_html (412 — there is no 422 in this codebase).
//
// The 412 stays LAST so a spec that simply has not been rendered yet still gets
// the precondition answer rather than a forbidden one.
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
		if aerr := shareRefusal(mem); aerr != nil {
			return writeError(c, aerr)
		}
		if mem.RenderedHTML == nil {
			return writeError(c, domain.NewErr(domain.ErrPreconditionFailed,
				"artifact has no rendered HTML to share (renderable types: "+
					strings.Join(domain.RenderTypeNames(), ", ")+")"))
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

// restorableVisibilities is the set unshare is willing to put a memory back into.
// It is the memories_visibility_check constraint (migration 0023) minus 'public',
// because restoring to 'public' is not an unshare.
var restorableVisibilities = map[string]bool{
	"private": true,
	"project": true,
	"team":    true,
	"admin":   true,
}

// preShareVisibility reads the tier SetMemoryVisibility recorded when the memory
// was published. It returns "" when there is nothing usable to restore — no attrs,
// unparseable attrs, the key absent, or a value outside the schema's tier set —
// and the caller falls back to "project".
//
// A value outside restorableVisibilities is treated as absent rather than passed
// through: this string goes straight into an UPDATE against a CHECK constraint, and
// the fallback must not be "whatever attrs happened to contain".
func preShareVisibility(attrs json.RawMessage) string {
	if len(attrs) == 0 {
		return ""
	}
	var obj map[string]any
	if err := json.Unmarshal(attrs, &obj); err != nil {
		return ""
	}
	v, _ := obj[domain.PreShareVisibilityKey].(string)
	if !restorableVisibilities[v] {
		return ""
	}
	return v
}

// handleUnshareArtifact revokes public sharing by restoring the visibility tier the
// memory held before it was shared. Same id is 404 on /share/:id immediately
// afterwards. Requires writer.
//
// aihub#151: this used to hard-code "project". For a memory that was 'private'
// (author-only) or 'admin' before sharing, that was not a revoke — it was a WIDENING,
// leaving the row readable by every member of the project, and silently, since the
// endpoint answers {"ok":true} either way. The pre-share tier is recorded in attrs by
// SetMemoryVisibility at share time; "project" survives only as the fallback for rows
// shared before that recording existed, which is the tier those rows have always been
// restored to anyway.
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
		// Only a public memory has anything to revoke, and only a public memory
		// has a pre-share tier recorded. Without this guard, DELETE on an already
		// non-public artifact — previously a no-op — would move it to whatever
		// attrs.pre_share_visibility happened to say, and attrs is caller-writable
		// (RememberRequest.Attrs is bound straight from the request body, and
		// UpdateMemory inherits the head's attrs wholesale). The direction is only
		// ever narrowing, since 'public' is excluded from restorableVisibilities,
		// but "narrowing by an amount the caller chose" is still not what an
		// unshare of an unshared artifact should do.
		if mem.Visibility != "public" {
			return c.JSON(http.StatusOK, map[string]any{"ok": true, "visibility": mem.Visibility})
		}
		target := preShareVisibility(mem.Attrs)
		if target == "" {
			target = "project"
		}
		if aihubErr := setMemoryVisibilityFn(ctx, pool, mem.ID, target); aihubErr != nil {
			return writeError(c, aihubErr)
		}
		return c.JSON(http.StatusOK, map[string]any{"ok": true, "visibility": target})
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
//
// buildAnnotationHTML is the stable 4-arg entry point kept for existing
// callers/tests; it always renders write-form actions without the pf_exact
// marker (equivalent to "viewing the head"). Production /ui rendering goes
// through buildAnnotationHTMLWithExact so a POST made from a marked
// past-version page round-trips back to that same version — see aihub#248
// review warning 1 (annotation round-trip dropped pf_exact).
func buildAnnotationHTML(memID, renderedHTML string, commitsRaw json.RawMessage, nonce string) string {
	return buildAnnotationHTMLWithExact(memID, renderedHTML, commitsRaw, nonce, false)
}

// buildAnnotationHTMLWithExact is buildAnnotationHTML plus the exact-version
// marker: when exact is true, every write-form action this function emits
// (add-annotation, hidden selection form, inline reply, inline resolve)
// carries ?pf_exact=1 so the resulting POST's redirect (artifactRedirectURL)
// lands back on the same past version instead of the lineage head (aihub#248
// review warning 1). This is NOT a new marker emission site in the sense
// non-goal 6 restricts (side rail + wi-detail "View" link only): those two
// are the only places a link may be minted to a DIFFERENT memory id with the
// marker attached. Here the marker is preserved on forms that submit back to
// the SAME memID the page is already (rightfully) showing, carrying forward
// intent the caller already established by following one of those two links.
func buildAnnotationHTMLWithExact(memID, renderedHTML string, commitsRaw json.RawMessage, nonce string, exact bool) string {
	exactSuffix := ""
	if exact {
		exactSuffix = "?" + exactVersionParam + "=1"
	}
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
				replyAction := "/ui/artifacts/" + html.EscapeString(memID) + "/commit/" + html.EscapeString(e.ID) + "/reply" + exactSuffix
				resolveAction := "/ui/artifacts/" + html.EscapeString(memID) + "/commit/" + html.EscapeString(e.ID) + "/resolve" + exactSuffix
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
	b.WriteString("/commit" + exactSuffix + "\">\n")

	if len(headings) > 0 {
		b.WriteString("<label for=\"pf-annot-heading\">Section:</label>\n")
		// data-pf-chrome marks this as OUR element, not the artifact's — see the wiring
		// script below and chromeEl() in annot.js for why the id alone is not trustworthy.
		b.WriteString("<select id=\"pf-annot-heading\" data-pf-chrome name=\"heading_id\">\n")
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
		b.WriteString("<input type=\"hidden\" id=\"pf-annot-htxt\" data-pf-chrome name=\"heading_text\" value=\"\">\n")
		// aihub#243: this mirror used to be an inline `onchange=` attribute on the <select>.
		// A CSP nonce authorises <script> ELEMENTS; it cannot authorise event-handler
		// ATTRIBUTES — script-src-attr falls back to script-src, and only 'unsafe-inline'
		// (or 'unsafe-hashes') admits a handler attribute, neither of which this policy has.
		// So dropping 'unsafe-inline' silently killed the handler: heading_text submitted
		// empty and the empty value persisted into the stored anchor. Same behaviour, moved
		// into a nonced element.
		//
		// NOT put in annot.js: that file's main() returns unless the viewport is ≥1100px,
		// while viewer.css only SHOWS this form at ≤1040px. The two windows are disjoint, so
		// a listener installed there would never run on the pages where this form is visible.
		//
		// Queried by [data-pf-chrome] rather than getElementById: the sanitizer allows `id`
		// globally (d2 figures need it) and the agent body is emitted BEFORE this chrome, so
		// an artifact carrying id="pf-annot-htxt" would otherwise be written into instead.
		// data-* is not on the sanitizer's attribute allowlist, so this marker is unforgeable.
		b.WriteString("<script nonce=\"" + html.EscapeString(nonce) + "\">")
		b.WriteString("(function(){")
		b.WriteString("var s=document.querySelector('select[data-pf-chrome][id=\"pf-annot-heading\"]');")
		b.WriteString("var t=document.querySelector('input[data-pf-chrome][id=\"pf-annot-htxt\"]');")
		b.WriteString("if(!s||!t)return;")
		b.WriteString("s.addEventListener('change',function(){")
		b.WriteString("var o=s.options[s.selectedIndex];t.value=o?(o.getAttribute('data-text')||''):'';")
		b.WriteString("});")
		b.WriteString("})();")
		b.WriteString("</script>\n")
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
	// data-pf-chrome is an unforgeable marker: `data-*` is not on the sanitizer's attribute
	// allowlist, so agent content cannot carry it (verified for the quoted, valueless and
	// upper-case forms). annot.js requires it, which is what stops an artifact from supplying a
	// <div id="pf-margin-rail"> that wins document order — the agent body is inlined BEFORE this
	// chrome, and getElementById returns the first match regardless of tag.
	b.WriteString("<div id=\"pf-margin-rail\" data-pf-chrome hidden></div>\n")

	// ─── Hidden selection-comment form ────────────────────────────────────────
	// JS reveals + positions this on text selection.
	b.WriteString("<form id=\"pf-selform\" data-pf-chrome hidden method=\"POST\" action=\"/ui/artifacts/")
	b.WriteString(html.EscapeString(memID))
	b.WriteString("/commit" + exactSuffix + "\">\n")
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
// operations: back to the artifact HTML page.
//
// aihub#248 review warning 1: preserves the exact-version marker from the
// incoming request. Without this, annotating (or replying/resolving on) a
// past version reached via the side-rail's marked link 303s back to a
// marker-less URL, which then falls into the lineage-head redirect above and
// bounces the author to head — silently "hiding" the comment they just wrote
// on that past version (annotations are strictly per-version, :385). The
// marker only ever round-trips back to the SAME memID this request already
// wrote to; it is not a new mint site under non-goal 6's "two link sites"
// restriction (see buildAnnotationHTMLWithExact's doc comment).
func artifactRedirectURL(c echo.Context, memID string) string {
	path := "/ui/artifacts/" + url.PathEscape(memID) + "/html"
	if isExactVersionRequest(c) {
		path += "?" + exactVersionParam + "=1"
	}
	return path
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
		return c.Redirect(http.StatusSeeOther, artifactRedirectURL(c, memID))
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
		return c.Redirect(http.StatusSeeOther, artifactRedirectURL(c, memID))
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

		return c.Redirect(http.StatusSeeOther, artifactRedirectURL(c, memID))
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
func buildVersionHistoryHTML(ctx context.Context, pool *pgxpool.Pool, memID string, versions []domain.MemoryVersionRef, nonce string) string {
	if len(versions) <= 1 {
		return ""
	}

	// Build a map of version_id → review_mem_id for versions that have a review.
	// Best-effort: errors silently ignored (non-fatal).
	reviewLinks := buildVersionReviewLinks(ctx, pool, versions)

	var b strings.Builder
	b.WriteString("<section class=\"pf-version-history\">\n")
	nVers := len(versions)
	// aihub#243: this was an inline `onclick=` attribute. A CSP nonce cannot authorise
	// event-handler attributes (script-src-attr falls back to script-src, which admits only
	// 'unsafe-inline'/'unsafe-hashes'), so it would be refused under the current policy.
	//
	// This function has no production caller today — aihub#159 moved version history into the
	// side rail — so nothing was actually broken. It is fixed rather than annotated because a
	// comment does not survive someone re-wiring it, and a dead inline handler is exactly the
	// kind of thing that gets re-wired and then silently does nothing.
	b.WriteString("<button type=\"button\" class=\"pf-version-history-toggle\" data-pf-chrome>")
	b.WriteString("<span class=\"pf-vchev\"></span>History &mdash; ")
	b.WriteString(strconv.Itoa(nVers))
	b.WriteString(" version")
	if nVers != 1 {
		b.WriteString("s")
	}
	b.WriteString("</button>\n")
	b.WriteString("<script nonce=\"" + html.EscapeString(nonce) + "\">")
	b.WriteString("(function(){")
	b.WriteString("var b=document.querySelector('button[data-pf-chrome].pf-version-history-toggle');")
	b.WriteString("if(!b)return;")
	b.WriteString("b.addEventListener('click',function(){")
	b.WriteString("var p=b.nextElementSibling;while(p&&p.tagName==='SCRIPT'){p=p.nextElementSibling;}")
	b.WriteString("if(!p)return;var c=b.querySelector('.pf-vchev');")
	b.WriteString("p.hidden=!p.hidden;if(c)c.classList.toggle('open',!p.hidden);")
	b.WriteString("});")
	b.WriteString("})();")
	b.WriteString("</script>\n")
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
