package render

import (
	_ "embed"
	"html"
	"strings"
)

// RelatedRef is a lightweight reference to a related memory, used by
// DocumentWithMeta to render a "Related" section in the artifact HTML view.
//
// ID is always set. Type and Summary may be empty for now — they are sourced
// from mem.Attrs["related_ids"] (a plain JSON string array). A future wi
// (aihub#112 Stream A) will swap this source for a join-table with enriched
// Type and Summary; see the TODO comment in routes_artifacts.go handleArtifactHTML.
type RelatedRef struct {
	ID      string
	Type    string
	Summary string
}

//go:embed style.css
var defaultStylesheet string

// DocumentWithMeta wraps a rendered HTML body fragment in a complete HTML5
// document (same as Document) and injects a small metadata header above the
// body that shows:
//   - the owning work item as a clickable link (when ownerWIHref is non-empty)
//   - a "Related memories" section listing each related memory as a link to
//     /ui/artifacts/<id>/html (when related is non-empty)
//
// The backHref nav bar is identical to Document's.
// When both ownerWIHref and related are empty the output is identical to
// calling Document(body, title, backHref).
func DocumentWithMeta(body, title, backHref, ownerWIHref, ownerWILabel string, related []RelatedRef, annotationsHTML ...string) string {
	if title == "" {
		title = "polyforge artifact"
	}
	var b strings.Builder
	b.Grow(len(body) + len(defaultStylesheet) + 512)
	b.WriteString("<!DOCTYPE html>\n<html lang=\"en\">\n<head>\n")
	b.WriteString("<meta charset=\"utf-8\">\n")
	b.WriteString("<meta name=\"viewport\" content=\"width=device-width, initial-scale=1\">\n")
	b.WriteString("<title>")
	b.WriteString(html.EscapeString(title))
	b.WriteString("</title>\n<style>\n")
	b.WriteString(defaultStylesheet)
	b.WriteString("\n</style>\n</head>\n<body>\n")
	if backHref != "" {
		b.WriteString("<nav class=\"pf-doc-nav\"><a href=\"")
		b.WriteString(html.EscapeString(backHref))
		b.WriteString("\">&larr; Back to work item</a></nav>\n")
	}
	// Metadata header: owning wi + related memories.
	if ownerWIHref != "" || len(related) > 0 {
		b.WriteString("<div class=\"pf-doc-meta\">\n")
		if ownerWIHref != "" {
			b.WriteString("<p class=\"pf-doc-meta-wi\"><strong>Work item:</strong> <a href=\"")
			b.WriteString(html.EscapeString(ownerWIHref))
			b.WriteString("\">")
			b.WriteString(html.EscapeString(ownerWILabel))
			b.WriteString("</a></p>\n")
		}
		if len(related) > 0 {
			b.WriteString("<p class=\"pf-doc-meta-related\"><strong>Related:</strong>")
			for i, r := range related {
				if i > 0 {
					b.WriteString(", ")
				}
				b.WriteString(" <a href=\"/ui/artifacts/")
				b.WriteString(html.EscapeString(r.ID))
				b.WriteString("/html\">")
				label := r.ID
				if r.Summary != "" {
					label = r.Summary
				}
				b.WriteString(html.EscapeString(label))
				b.WriteString("</a>")
			}
			b.WriteString("</p>\n")
		}
		b.WriteString("</div>\n")
	}
	b.WriteString(body)
	// Inject annotation UI fragment (aihub#124) — only present on /ui path.
	if len(annotationsHTML) > 0 && annotationsHTML[0] != "" {
		b.WriteString("\n")
		b.WriteString(annotationsHTML[0])
	}
	b.WriteString("\n</body>\n</html>\n")
	return b.String()
}

// Document wraps a rendered HTML body fragment in a complete HTML5 document
// with an embedded default stylesheet. The stored rendered_html column keeps
// the fragment so it can be embedded elsewhere (e.g. a future webui); only the
// artifact HTTP endpoint needs the standalone document wrapping.
//
// If title is empty, falls back to "polyforge artifact".
//
// When backHref is non-empty, a sticky "Back to work item" nav bar is emitted
// at the top of <body>. The webui passes the wi detail URL here so a spec/plan
// opened in a new tab can navigate back; the CLI/API path passes "" so the
// standalone document stays a pure content view (byte-identical to the
// pre-backHref output aside from the extra param).
func Document(body, title, backHref string) string {
	if title == "" {
		title = "polyforge artifact"
	}
	var b strings.Builder
	b.Grow(len(body) + len(defaultStylesheet) + 256)
	b.WriteString("<!DOCTYPE html>\n<html lang=\"en\">\n<head>\n")
	b.WriteString("<meta charset=\"utf-8\">\n")
	b.WriteString("<meta name=\"viewport\" content=\"width=device-width, initial-scale=1\">\n")
	b.WriteString("<title>")
	b.WriteString(html.EscapeString(title))
	b.WriteString("</title>\n<style>\n")
	b.WriteString(defaultStylesheet)
	b.WriteString("\n</style>\n</head>\n<body>\n")
	if backHref != "" {
		b.WriteString("<nav class=\"pf-doc-nav\"><a href=\"")
		b.WriteString(html.EscapeString(backHref))
		b.WriteString("\">&larr; Back to work item</a></nav>\n")
	}
	b.WriteString(body)
	b.WriteString("\n</body>\n</html>\n")
	return b.String()
}
