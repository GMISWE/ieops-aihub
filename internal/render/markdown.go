// Package render wraps goldmark to convert markdown content into HTML used by
// the spec/plan artifact viewer (aihub#27 / IEBE-1694).
//
// The configuration enables:
//   - GFM (tables, task lists, strikethrough, autolinks)
//   - Footnotes
//   - Definition lists
//   - Auto heading IDs (so the viewer can deep-link sections)
//   - Inline raw HTML / SVG passthrough via the Unsafe renderer option
//
// NOTE ON THE UNSAFE RENDERER (aihub#240, resolves #144).
//
// This package used to justify html.WithUnsafe() with "artifact author == artifact
// reader, so XSS is not in scope". That premise was wrong, and aihub#144 is the proof:
// artifacts are authored by agents and read by every project member, the authed /ui and
// /v1 responses carried no CSP at all, and only the anonymous /share path was locked
// down — so logged-in users were the *least* protected readers.
//
// WithUnsafe stays, because raw HTML and inline SVG passthrough is the whole point of
// the renderer. What changed is that its output is no longer trusted on the way out:
//
//   - SanitizeArtifactHTML (sanitize.go) strips script, event handlers, javascript:
//     URIs, <style> elements, XML DTD declarations, and every network form of an image
//     source (images must be data: or a same-document fragment). Anchors are the deliberate
//     exception: they may still carry http(s) destinations, because navigating away is what
//     a link is for. So "no external resources" holds for anything the page LOADS, not for
//     anything the reader can choose to click;
//   - SafeEmbedDocument (safeembed.go) isolates a finished agent document in a
//     sandboxed iframe;
//   - the authed /ui and /v1 artifact responses now send a Content-Security-Policy.
//
// Callers rendering agent-authored markdown into an authed page must run the output
// through SanitizeArtifactHTML. Markdown() itself deliberately does not sanitize: the
// /v1 and /share responses are contractually byte-identical (aihub#138), so the
// decision belongs at the call site that knows which path it is serving.
//   - chroma syntax highlighting on fenced code blocks (CSS-class mode so the
//     consumer can theme via a stylesheet later)
//   - a custom block parser (svg_block.go, aihub#262) that treats a top-level
//     <svg>...</svg> as one raw HTML block even when it contains blank lines or
//     indented lines. Without it, CommonMark's stock HTML-block rules end a
//     line-initial <svg> at the first blank line, and everything after that point
//     gets re-parsed as markdown — an indented line becomes a code block, and an
//     open tag with trailing content becomes a <p>, which force-closes the <svg> per
//     the HTML5 foreign-content breakout rule. See svg_block.go's package-level
//     comment for the full mechanism and why it fails closed.
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
	"github.com/yuin/goldmark/util"
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
	goldmark.WithParserOptions(
		parser.WithAutoHeadingID(),
		// Priority numbers below are goldmark v1.8.2's own, read from
		// parser.DefaultBlockParsers() (parser/parser.go):
		//   SetextHeading 100, ThematicBreak 200, List 300, ListItem 400,
		//   CodeBlock (indented) 500, ATXHeading 600, FencedCodeBlock 700,
		//   Blockquote 800, HTMLBlock 900, Paragraph 1000.
		// 850 sits strictly between Blockquote/FencedCodeBlock (700/800) and
		// HTMLBlock (900): our svg block parser must run BEFORE the stock
		// HTML-block parser so it gets first refusal on a line-initial <svg>,
		// but AFTER FencedCodeBlock so an ```svg fence still wins first (and,
		// inherently, so a <svg> already inside an open fenced/indented code
		// block is never even offered to us — goldmark only tries new block
		// parsers when no block is currently open and continuing).
		parser.WithBlockParsers(util.Prioritized(newSVGBlockParser(), 850)),
	),
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
