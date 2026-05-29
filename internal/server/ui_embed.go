package server

import (
	"embed"
	"fmt"
	"html"
	"html/template"
	"io/fs"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/labstack/echo/v4"

	"github.com/GMISWE/ieops-aihub/internal/render"
)

// Embedded templates and static assets for the read-only Web UI.
//
// Templates live under internal/server/templates/.
// Static assets (CSS + vendored HTMX) live under internal/server/static/.
//
// HTMX is vendored at internal/server/static/htmx.min.js.
//   source : https://unpkg.com/htmx.org@2.0.4/dist/htmx.min.js
//   sha256 : e209dda5c8235479f3166defc7750e1dbcd5a5c1808b7792fc2e6733768fb447
//   bytes  : 50917
// Update this comment whenever the file is refreshed so the integrity claim
// stays honest. A CDN reference is deliberately NOT used so the UI works in
// air-gapped / restricted-network deployments.

//go:embed templates
var templateFS embed.FS

//go:embed static/*
var staticFS embed.FS

// staticFSRoot strips the "static/" prefix so /ui/static/foo.css resolves to
// static/foo.css inside the embed.FS.
func staticFSRoot() http.FileSystem {
	sub, err := fs.Sub(staticFS, "static")
	if err != nil {
		// Compile-time guaranteed: //go:embed verifies static/ exists.
		panic(fmt.Sprintf("ui: embed static: %v", err))
	}
	return http.FS(sub)
}

// parseTemplates parses layout + every page/partial template into a single
// root *template.Template. Pages are parsed via ParseFS in one shot so that
// each child template can refer to {{define "content"}}...{{end}} blocks
// defined in any sibling file, and so a single named handler invocation
// (ExecuteTemplate with the page filename) renders the full doc.
//
// The page files (wi_list.html.tmpl, wi_detail.html.tmpl,
// memories.html.tmpl, partials/*.tmpl) live in the templates/ directory and
// are picked up automatically by the embed.FS glob.
func parseTemplates() *template.Template {
	root := template.New("").Funcs(uiFuncMap())
	root = template.Must(root.ParseFS(templateFS, "templates/*.tmpl"))
	// Partials directory is optional at parse-time but the embed pattern above
	// requires the path to exist. If no partials are present yet (subagents
	// have not landed them), the ParseFS call will error — guard against that
	// by checking first.
	if entries, err := fs.ReadDir(templateFS, "templates/partials"); err == nil && len(entries) > 0 {
		root = template.Must(root.ParseFS(templateFS, "templates/partials/*.tmpl"))
	}
	return root
}

// uiFuncMap exposes a small set of helpers to all templates.
//
//   - md       : render a string as markdown -> safe HTML. Used for wi.Content
//                and memory.content fields. Falls back to escaped plain text
//                on renderer error.
//   - truncate : clip a long string with an ellipsis. Useful for wi list views.
//   - default  : replace empty strings with a placeholder.
//   - hasPrefix: strings.HasPrefix.
//   - wiref    : build /ui/wi/<slug-or-id> with '#' path-escaped.
//   - fmtTs    : parse an RFC3339 timestamp string and format it the same way
//                metadata-card timestamps are formatted ("2006-01-02 15:04 UTC").
//                Used by the memory_detail commits card (aihub#70) so commit
//                timestamps line up with the rest of the page.
func uiFuncMap() template.FuncMap {
	return template.FuncMap{
		"md": func(src string) template.HTML {
			out, err := render.Markdown(src)
			if err != nil {
				return template.HTML("<pre>" + html.EscapeString(src) + "</pre>")
			}
			return template.HTML(out)
		},
		// safeHTML passes a pre-rendered, trusted HTML fragment through
		// without escaping. Used for cached methodology.spec / methodology.plan
		// rendered_html on the wi detail page. The pointer form lets the
		// template gate on non-nil before invoking ({{if .RenderedHTML}}).
		"safeHTML": func(s *string) template.HTML {
			if s == nil {
				return template.HTML("")
			}
			return template.HTML(*s)
		},
		"truncate": func(n int, s string) string {
			// n is the maximum number of runes (user-visible characters),
			// not bytes. Byte-based truncation would slice mid-rune on
			// multi-byte UTF-8 (e.g. CJK) and emit garbled output.
			if n <= 0 || utf8.RuneCountInString(s) <= n {
				return s
			}
			return string([]rune(s)[:n]) + "..."
		},
		"default": func(fallback, value string) string {
			if value == "" {
				return fallback
			}
			return value
		},
		"hasPrefix": strings.HasPrefix,
		// wiref builds an href for a wi detail page from a slug or wi_id.
		// Slugs like "aihub#1" contain '#', which browsers treat as a URL
		// fragment and strip from the request — the handler would then see
		// only "aihub" and 404. PathEscape turns "#" into "%23" so the full
		// slug survives the round-trip.
		"wiref": func(slugOrID string) string {
			if slugOrID == "" {
				return ""
			}
			return "/ui/wi/" + url.PathEscape(slugOrID)
		},
		"fmtTs": func(s string) string {
			if s == "" {
				return "—"
			}
			if t, err := time.Parse(time.RFC3339, s); err == nil {
				return t.UTC().Format("2006-01-02 15:04 MST")
			}
			return s
		},
	}
}

// pageTemplate builds a self-contained *template.Template for a single page
// file plus the shared layout (and any caller-specified partials). This
// sidesteps the multi-page {{define "content"}} collision that would happen
// if we tried to pre-parse every page file into the same root template.
//
// Usage from a peer-subagent register* function:
//
//	listTmpl := pageTemplate("wi_list.html.tmpl")
//	g.GET("/wi", func(c echo.Context) error {
//	    return renderTemplate(c, listTmpl, "layout", data)
//	})
//
// `partials` are filenames inside templates/partials/. They may be empty.
func pageTemplate(pageFile string, partials ...string) *template.Template {
	t := template.New("").Funcs(uiFuncMap())
	files := []string{
		"templates/layout.html.tmpl",
		"templates/" + pageFile,
	}
	for _, p := range partials {
		files = append(files, "templates/partials/"+p)
	}
	return template.Must(t.ParseFS(templateFS, files...))
}

// partialTemplate builds a *template.Template for a standalone partial that
// has no full-page wrapper — i.e. it is only ever rendered as an htmx fragment
// via its own {{define}} block, never through "layout". Used by the queue
// section, which lost its full page when the ready queue moved into the wi
// list page as an embedded block.
func partialTemplate(partialFile string) *template.Template {
	t := template.New("").Funcs(uiFuncMap())
	return template.Must(t.ParseFS(templateFS, "templates/partials/"+partialFile))
}

// renderTemplate executes the named template against data and writes the
// result with Content-Type text/html; charset=utf-8. Errors surface as 500s.
//
// For full pages, pass name="layout"; for partial endpoints, pass the
// partial filename (e.g. "events_timeline.html.tmpl") so the layout chrome
// is skipped.
func renderTemplate(c echo.Context, tmpl *template.Template, name string, data any) error {
	var buf strings.Builder
	if err := tmpl.ExecuteTemplate(&buf, name, data); err != nil {
		return c.String(http.StatusInternalServerError, "template error: "+err.Error())
	}
	return c.HTMLBlob(http.StatusOK, []byte(buf.String()))
}
