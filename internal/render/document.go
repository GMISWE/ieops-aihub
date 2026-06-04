package render

import (
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
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

//go:embed annotator.js
var annotatorJS []byte

//go:embed annot.js
var annotJS []byte

//go:embed viewer.css
var viewerCSS []byte

// AnnotatorJS returns the embedded annotator.js bundle bytes (served at
// /ui/static/annotator.js). The bytes are the same across all calls — the
// embed is loaded once at program startup.
func AnnotatorJS() []byte { return annotatorJS }

// AnnotJS returns the embedded annot.js glue bytes (served at
// /ui/static/annot.js).
func AnnotJS() []byte { return annotJS }

// ViewerCSS returns the embedded viewer.css bytes (served at
// /ui/static/viewer.css). This is the /ui-only design-system override layer
// that reskins the artifact viewer using #129 tokens. Never embedded into
// /v1 or /share output — those use only style.css (frozen).
func ViewerCSS() []byte { return viewerCSS }

// assetVersion is a content hash over the embedded JS assets, computed once at
// startup. Appended as a ?v= cache-buster to the script URLs so a deploy with
// changed JS invalidates browser caches immediately despite Cache-Control
// max-age (aihub#125).
var assetVersion = func() string {
	all := append(append(append([]byte{}, annotatorJS...), annotJS...), viewerCSS...)
	sum := sha256.Sum256(all)
	return hex.EncodeToString(sum[:4])
}()

// AssetVersion returns the 8-hex-char content version of the embedded JS assets.
func AssetVersion() string { return assetVersion }

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
	// aihub#125: when the annotation UI is present (/ui path only), wrap the
	// metadata header + document content in a single column element so the
	// two-column grid (content | margin rail) has exactly one content cell.
	// Without this wrapper every direct <body> child becomes its own grid item
	// and auto-placement scatters the document across both columns.
	annotated := len(annotationsHTML) > 0 && annotationsHTML[0] != ""
	if annotated {
		b.WriteString("<div id=\"pf-doc-col\">\n")
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
	if annotated {
		b.WriteString("\n</div>\n")
	}
	// Inject annotation UI fragment (aihub#124) — only present on /ui path.
	if annotated {
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
