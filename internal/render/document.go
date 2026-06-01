package render

import (
	_ "embed"
	"html"
	"strings"
)

//go:embed style.css
var defaultStylesheet string

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
