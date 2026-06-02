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
	"strings"

	chromahtml "github.com/alecthomas/chroma/v2/formatters/html"
	"github.com/yuin/goldmark"
	highlighting "github.com/yuin/goldmark-highlighting/v2"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/extension"
	"github.com/yuin/goldmark/parser"
	"github.com/yuin/goldmark/renderer/html"
	"github.com/yuin/goldmark/text"
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


// HeadingRef is a (id, text) pair extracted from a markdown source by ExtractHeadings.
// The id value matches what goldmark's parser.WithAutoHeadingID produces in the
// rendered HTML, so form selects built from these refs align exactly with the
// anchor ids on the rendered page — there is no separate slugification step.
type HeadingRef struct {
	ID   string
	Text string
}

// ExtractHeadings parses src with the same goldmark engine used by Markdown()
// and returns the ordered list of (id, text) pairs for every heading in the
// document. The ids are the auto-generated heading anchors emitted by the
// parser (WithAutoHeadingID). Returns nil when src is empty.
func ExtractHeadings(src string) []HeadingRef {
	if src == "" {
		return nil
	}
	source := []byte(src)
	reader := text.NewReader(source)
	doc := md.Parser().Parse(reader)

	var refs []HeadingRef
	ast.Walk(doc, func(n ast.Node, entering bool) (ast.WalkStatus, error) { //nolint:errcheck
		if !entering {
			return ast.WalkContinue, nil
		}
		if n.Kind() != ast.KindHeading {
			return ast.WalkContinue, nil
		}
		// Collect id attribute (set by WithAutoHeadingID).
		idVal, ok := n.AttributeString("id")
		if !ok {
			return ast.WalkContinue, nil
		}
		var id string
		switch v := idVal.(type) {
		case []byte:
			id = string(v)
		case string:
			id = v
		default:
			return ast.WalkContinue, nil
		}
		// Collect inline text by walking children.
		var txtBuf strings.Builder
		for child := n.FirstChild(); child != nil; child = child.NextSibling() {
			if t, ok := child.(*ast.Text); ok {
				txtBuf.Write(t.Value(source))
			}
		}
		refs = append(refs, HeadingRef{ID: id, Text: txtBuf.String()})
		return ast.WalkSkipChildren, nil
	}) //nolint:errcheck
	return refs
}
