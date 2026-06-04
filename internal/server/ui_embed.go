package server

import (
	"embed"
	"encoding/json"
	"fmt"
	"html"
	"html/template"
	"io/fs"
	"net/http"
	"strconv"
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

// static holds the CSS, the vendored HTMX bundle, the theme-toggle JS, and the
// self-hosted Geist / Geist Mono woff2 fonts under static/fonts/. The directive
// embeds the whole static tree (not static/*) so nested subdirectories like
// static/fonts/ are included recursively.
//
//go:embed static
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

// cacheStatic wraps the embedded-static file server so responses carry a
// Cache-Control header. The default http.FileServer emits none, so the browser
// revalidates (often re-downloads) every asset on each navigation — for the
// self-hosted woff2 fonts that means a visible font swap (FOUT) on refresh
// (aihub#129 round-3 #1).
//
// Fonts are content-addressed by filename and never mutate in place, so they get
// a one-year immutable cache. CSS/JS may change between deploys but the embed is
// rebuilt each release, so a short max-age keeps them fresh without re-fetching
// on every in-session navigation.
func cacheStatic(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, ".woff2"), strings.HasSuffix(r.URL.Path, ".woff"):
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		case strings.HasSuffix(r.URL.Path, ".css"), strings.HasSuffix(r.URL.Path, ".js"):
			w.Header().Set("Cache-Control", "public, max-age=3600")
		}
		next.ServeHTTP(w, r)
	})
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

// assetVersion is a per-process cache-busting token appended to mutable static
// asset URLs (?v=...). It changes on every binary restart, so a rebuilt
// ui.css / dropdown.js / theme.js is fetched fresh on the next navigation
// instead of being served from the browser's long-lived static cache
// (Cache-Control: max-age=3600). Fonts are content-stable and stay immutable
// without a token.
var assetVersion = fmt.Sprintf("%d", time.Now().Unix())

// uiFuncMap exposes a small set of helpers to all templates.
//
//   - md       : render a string as markdown -> safe HTML. Used for wi.Content
//     and memory.content fields. Falls back to escaped plain text
//     on renderer error.
//   - truncate : clip a long string with an ellipsis. Useful for wi list views.
//   - default  : replace empty strings with a placeholder.
//   - hasPrefix: strings.HasPrefix.
//   - wiref    : build /ui/wi/<slug-or-id> with '#' path-escaped.
//   - fmtTs    : parse an RFC3339 timestamp string and format it the same way
//     metadata-card timestamps are formatted ("2006-01-02 15:04 UTC").
//     Used by the memory_detail commits card (aihub#70) so commit
//     timestamps line up with the rest of the page.
func uiFuncMap() template.FuncMap {
	return template.FuncMap{
		"md": func(src string) template.HTML {
			out, err := render.Markdown(src)
			if err != nil {
				return template.HTML("<pre>" + html.EscapeString(src) + "</pre>")
			}
			return template.HTML(out)
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
		// assetv returns the per-process cache-busting token for static asset URLs.
		"assetv": func() string { return assetVersion },
		// wiref builds an href for a wi detail page from a slug or wi_id.
		// Slugs like "aihub#1" contain '#', which browsers treat as a URL
		// fragment and strip from the request — the handler would then see
		// only "aihub" and 404. PathEscape turns "#" into "%23" so the full
		// slug survives the round-trip.
		"wiref": func(slugOrID string) string { return wiHref(slugOrID) },
		"fmtTs": func(s string) string {
			if s == "" {
				return "—"
			}
			if t, err := time.Parse(time.RFC3339, s); err == nil {
				return t.UTC().Format("2006-01-02 15:04 MST")
			}
			return s
		},
		// inc adds 1 to an integer; used in range loops to convert 0-based indices
		// to 1-based labels (e.g. {{inc $i}} → "1", "2", …) for version-history lists.
		"inc": func(n int) int { return n + 1 },
		// sub subtracts b from a; used to label a newest-first version list with
		// its chronological version number (total - displayIndex).
		"sub": func(a, b int) int { return a - b },
		// reltime renders a time as a compact relative phrase ("12m ago",
		// "3h ago", "2d ago") for the activity feed. Falls back to an absolute
		// date for anything older than a week. Used by the wi-detail activity
		// stream so each event reads like the prototype's ".ts" line.
		"reltime": func(t time.Time) string { return relTime(t) },
		// artifactInitial returns a single uppercase letter for an artifact's
		// methodology type, used inside the .art .ico avatar (e.g.
		// "methodology.spec" → "S", "methodology.wrap_summary" → "W"). It reads
		// the segment after the last dot so it works for any methodology.* type.
		"artifactInitial": func(t string) string {
			seg := t
			if i := strings.LastIndex(t, "."); i >= 0 && i+1 < len(t) {
				seg = t[i+1:]
			}
			seg = strings.TrimSpace(seg)
			if seg == "" {
				return "?"
			}
			return strings.ToUpper(seg[:1])
		},
		// shortDate renders an RFC3339 timestamp string as "YYYY-MM-DD" for the
		// artifact version-timeline rows. Empty / unparseable input yields "—".
		"shortDate": func(s string) string {
			if s == "" {
				return "—"
			}
			if ts, err := time.Parse(time.RFC3339, s); err == nil {
				return ts.UTC().Format("2006-01-02")
			}
			if len(s) >= 10 {
				return s[:10]
			}
			return s
		},
	}
}

// relTime formats t as a compact relative phrase relative to now. Zero times
// render as "—". The thresholds (minute / hour / day) match the prototype's
// activity-feed timestamps; anything beyond a week falls back to an absolute
// date so old events stay unambiguous.
func relTime(t time.Time) string {
	if t.IsZero() {
		return "—"
	}
	d := time.Since(t)
	switch {
	case d < 0:
		return "just now"
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return strconv.Itoa(int(d.Minutes())) + "m ago"
	case d < 24*time.Hour:
		return strconv.Itoa(int(d.Hours())) + "h ago"
	case d < 7*24*time.Hour:
		return strconv.Itoa(int(d.Hours()/24)) + "d ago"
	default:
		return t.UTC().Format("2006-01-02")
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

// parseRelatedIDs extracts the "related_ids" string array from a mem.Attrs
// JSONB value. It returns the non-empty IDs, or nil for missing/empty/malformed
// input. Both parseRelatedRefs (routes_artifacts.go) and parseMemRelatedRefs
// (ui_handlers_memory.go) delegate here so the JSON-parsing logic lives in
// exactly one place.
func parseRelatedIDs(attrs json.RawMessage) []string {
	if len(attrs) == 0 {
		return nil
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(attrs, &obj); err != nil {
		return nil
	}
	raw, ok := obj["related_ids"]
	if !ok {
		return nil
	}
	var ids []string
	if err := json.Unmarshal(raw, &ids); err != nil {
		return nil
	}
	out := ids[:0]
	for _, id := range ids {
		if id != "" {
			out = append(out, id)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
