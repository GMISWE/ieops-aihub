// Package render wraps goldmark to convert markdown content into HTML used by
// the spec/plan artifact viewer (aihub#27 / IEBE-1694).
//
// The configuration enables:
//   - GFM (tables, task lists, strikethrough, autolinks)
//   - Footnotes
//   - Definition lists
//   - Auto heading IDs (so the viewer can deep-link sections)
//   - Inline raw HTML / SVG passthrough via the Unsafe renderer option
//     (artifact author == artifact reader, so XSS is not in scope)
//   - chroma syntax highlighting on fenced code blocks (CSS-class mode so the
//     consumer can theme via a stylesheet later)
package render

import (
	"bytes"

	chromahtml "github.com/alecthomas/chroma/v2/formatters/html"
	"github.com/yuin/goldmark"
	highlighting "github.com/yuin/goldmark-highlighting/v2"
	"github.com/yuin/goldmark/extension"
	"github.com/yuin/goldmark/parser"
	"github.com/yuin/goldmark/renderer/html"
)

// md is the shared goldmark engine. goldmark.Markdown is safe for concurrent use
// once configured, so we build it once at package init.
var md = goldmark.New(
	goldmark.WithExtensions(
		extension.GFM,
		extension.Footnote,
		extension.DefinitionList,
		highlighting.NewHighlighting(
			highlighting.WithStyle("github"),
			highlighting.WithFormatOptions(chromahtml.WithClasses(true)),
		),
	),
	goldmark.WithParserOptions(parser.WithAutoHeadingID()),
	goldmark.WithRendererOptions(html.WithUnsafe()),
)

// Markdown converts markdown source to HTML. The empty string is rendered as
// the empty string (no error). Returns the underlying goldmark error verbatim
// on failure so callers can log it without wrapping.
func Markdown(src string) (string, error) {
	if src == "" {
		return "", nil
	}
	var buf bytes.Buffer
	if err := md.Convert([]byte(src), &buf); err != nil {
		return "", err
	}
	return buf.String(), nil
}
