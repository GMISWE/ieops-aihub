package render

import (
	"os"
	"strings"
	"testing"
	"time"

	xhtml "golang.org/x/net/html"
)

// svgCounts walks a fully-parsed HTML document (using golang.org/x/net/html's real tree
// constructor, which implements the HTML5 foreign-content and breakout rules — the same
// rules a browser applies) and counts elements by name, distinguishing ones that are
// descendants of an <svg> element from ones that are not.
//
// The task's own root-cause writeup is explicit that byte-counting or strings.Contains
// UNDER-REPORTS breakage: in a broken render most `<rect`/`<text>` bytes still exist (only
// the ones that fell into <pre><code> are HTML-escaped), so the only reliable signal is
// structural — is this element actually inside the <svg> in the parsed DOM, or did the
// <svg> get force-closed and the element ended up as a sibling <p>/<pre> instead.
type svgCounts struct {
	svgElements int // total <svg> elements anywhere in the document (nesting included)
	rectInSVG   int // <rect> elements that are descendants of some <svg>
	textInSVG   int // <text> elements that are descendants of some <svg>
	pInSVG      int // <p> elements that are descendants of some <svg> (the breakout smell)
	preInSVG    int // <pre> elements that are descendants of some <svg>
}

func countSVG(t *testing.T, htmlStr string) svgCounts {
	t.Helper()
	doc, err := xhtml.Parse(strings.NewReader(htmlStr))
	if err != nil {
		t.Fatalf("parsing rendered HTML: %v", err)
	}
	var c svgCounts
	var walk func(n *xhtml.Node, inSVG bool)
	walk = func(n *xhtml.Node, inSVG bool) {
		cur := inSVG
		if n.Type == xhtml.ElementNode {
			switch n.Data {
			case "svg":
				c.svgElements++
				cur = true
			case "rect":
				if inSVG {
					c.rectInSVG++
				}
			case "text":
				if inSVG {
					c.textInSVG++
				}
			case "p":
				if inSVG {
					c.pInSVG++
				}
			case "pre":
				if inSVG {
					c.preInSVG++
				}
			}
		}
		for child := n.FirstChild; child != nil; child = child.NextSibling {
			walk(child, cur)
		}
	}
	walk(doc, false)
	return c
}

func mustParse(t *testing.T, htmlStr string) *xhtml.Node {
	t.Helper()
	doc, err := xhtml.Parse(strings.NewReader(htmlStr))
	if err != nil {
		t.Fatalf("parsing rendered HTML: %v", err)
	}
	return doc
}

// findFirst returns the first element named tag in document order, or nil.
func findFirst(n *xhtml.Node, tag string) *xhtml.Node {
	if n.Type == xhtml.ElementNode && n.Data == tag {
		return n
	}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if found := findFirst(c, tag); found != nil {
			return found
		}
	}
	return nil
}

// textContent concatenates all text-node descendants of n, depth-first.
func textContent(n *xhtml.Node) string {
	if n.Type == xhtml.TextNode {
		return n.Data
	}
	var sb strings.Builder
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		sb.WriteString(textContent(c))
	}
	return sb.String()
}

func mustRender(t *testing.T, src string) string {
	t.Helper()
	out, err := Markdown(src)
	if err != nil {
		t.Fatalf("Markdown() error: %v", err)
	}
	return out
}

// TestSVGBlock_HeadlineCase is the exact scenario from the root-cause writeup: a blank
// line between two <text> element groups inside a top-level <svg>. Before the fix, the
// second <text> becomes a <p>, which force-closes the <svg> (HTML5 foreign-content
// breakout), leaving the figure with only the elements that preceded the blank line.
func TestSVGBlock_HeadlineCase(t *testing.T) {
	src := "<svg width=\"200\" height=\"100\" viewBox=\"0 0 200 100\">\n" +
		"  <rect width=\"100%\" height=\"100%\" fill=\"#eee\"/>\n" +
		"  <text x=\"10\" y=\"20\">Hello World</text>\n" +
		"\n" +
		"  <text x=\"10\" y=\"40\">Second line</text>\n" +
		"</svg>\n"
	out := mustRender(t, src)
	got := countSVG(t, out)
	want := svgCounts{svgElements: 1, rectInSVG: 1, textInSVG: 2}
	if got != want {
		t.Fatalf("counts = %+v, want %+v\nrendered:\n%s", got, want, out)
	}
}

// TestSVGBlock_IndentedLineAfterBlank covers a line indented >= 4 spaces that follows a
// blank line inside the <svg>. Before the fix that line becomes an indented code block
// (and, upstream of that, the blank line already force-closed the <svg> block).
func TestSVGBlock_IndentedLineAfterBlank(t *testing.T) {
	src := "<svg width=\"200\" height=\"100\" viewBox=\"0 0 200 100\">\n" +
		"  <rect width=\"100%\" height=\"100%\" fill=\"#eee\"/>\n" +
		"\n" +
		"    <text x=\"10\" y=\"20\">Indented</text>\n" +
		"</svg>\n"
	out := mustRender(t, src)
	got := countSVG(t, out)
	want := svgCounts{svgElements: 1, rectInSVG: 1, textInSVG: 1}
	if got != want {
		t.Fatalf("counts = %+v, want %+v\nrendered:\n%s", got, want, out)
	}
}

// TestSVGBlock_OpenTagWithTrailingContent isolates the specific line shape called out in
// the root-cause writeup as "the fatal one": an open tag with content after it
// (`<text x=...>Label</text>`), appearing after a blank line.
func TestSVGBlock_OpenTagWithTrailingContent(t *testing.T) {
	src := "<svg width=\"100\" height=\"50\">\n" +
		"  <rect width=\"100%\" height=\"100%\"/>\n" +
		"\n" +
		"  <text x=\"5\" y=\"10\">Label</text>\n" +
		"</svg>\n"
	out := mustRender(t, src)
	got := countSVG(t, out)
	want := svgCounts{svgElements: 1, rectInSVG: 1, textInSVG: 1}
	if got != want {
		t.Fatalf("counts = %+v, want %+v\nrendered:\n%s", got, want, out)
	}
	if got.pInSVG != 0 || got.preInSVG != 0 {
		t.Fatalf("figure has a <p> or <pre> descendant of <svg>: %+v\nrendered:\n%s", got, out)
	}
}

// TestSVGBlock_MultiLineOpeningTag covers an opening <svg ...> tag that itself spans more
// than one line before its closing '>'.
func TestSVGBlock_MultiLineOpeningTag(t *testing.T) {
	src := "<svg width=\"200\"\n" +
		"     viewBox=\"0 0 200 100\">\n" +
		"  <rect width=\"100%\" height=\"100%\"/>\n" +
		"\n" +
		"  <text x=\"1\" y=\"1\">A</text>\n" +
		"</svg>\n"
	out := mustRender(t, src)
	got := countSVG(t, out)
	want := svgCounts{svgElements: 1, rectInSVG: 1, textInSVG: 1}
	if got != want {
		t.Fatalf("counts = %+v, want %+v\nrendered:\n%s", got, want, out)
	}
}

// TestSVGBlock_Nested covers <svg> nested inside <svg>, which is legal SVG, plus a blank
// line inside the inner element so both depth tracking and blank-line tolerance are
// exercised together.
func TestSVGBlock_Nested(t *testing.T) {
	src := "<svg width=\"300\" height=\"200\">\n" +
		"  <rect width=\"100%\" height=\"100%\"/>\n" +
		"\n" +
		"  <svg x=\"10\" y=\"10\" width=\"100\" height=\"100\">\n" +
		"    <rect width=\"50\" height=\"50\"/>\n" +
		"\n" +
		"    <text x=\"5\" y=\"5\">Nested</text>\n" +
		"  </svg>\n" +
		"</svg>\n"
	out := mustRender(t, src)
	got := countSVG(t, out)
	want := svgCounts{svgElements: 2, rectInSVG: 2, textInSVG: 1}
	if got != want {
		t.Fatalf("counts = %+v, want %+v\nrendered:\n%s", got, want, out)
	}
	if got.pInSVG != 0 || got.preInSVG != 0 {
		t.Fatalf("figure has a <p> or <pre> descendant of <svg>: %+v\nrendered:\n%s", got, out)
	}
}

// TestSVGBlock_UppercaseTag covers <SVG>/</SVG> (case-insensitive trigger and matching).
func TestSVGBlock_UppercaseTag(t *testing.T) {
	src := "<SVG width=\"100\" height=\"50\">\n" +
		"  <rect width=\"100%\" height=\"100%\"/>\n" +
		"\n" +
		"  <text x=\"1\" y=\"1\">Upper</text>\n" +
		"</SVG>\n"
	out := mustRender(t, src)
	got := countSVG(t, out)
	want := svgCounts{svgElements: 1, rectInSVG: 1, textInSVG: 1}
	if got != want {
		t.Fatalf("counts = %+v, want %+v\nrendered:\n%s", got, want, out)
	}
}

// TestSVGBlock_SelfClosingSingleLine covers a self-closing <svg .../> that opens and
// closes on a single line, with ordinary markdown immediately following it.
func TestSVGBlock_SelfClosingSingleLine(t *testing.T) {
	src := "<svg width=\"10\" height=\"10\" fill=\"red\"/>\n" +
		"\n" +
		"# After\n" +
		"\n" +
		"Some *text*.\n"
	out := mustRender(t, src)
	got := countSVG(t, out)
	if got.svgElements != 1 {
		t.Fatalf("svgElements = %d, want 1\nrendered:\n%s", got.svgElements, out)
	}
	if !strings.Contains(out, "<h1") {
		t.Fatalf("expected the heading after the svg to still parse as markdown:\n%s", out)
	}
	if !strings.Contains(out, "<em>text</em>") {
		t.Fatalf("expected the paragraph after the svg to still parse as markdown:\n%s", out)
	}
}

// TestSVGBlock_TrailingContentSameLineAsClose covers content appearing after </svg> on the
// very same source line, followed by ordinary markdown on subsequent lines. Block-level
// constructs are line-atomic in CommonMark (a line cannot be half markup, half a new
// block), so the trailing text on the closing line is swallowed into the raw block —
// what matters is that this does not desynchronize the lookahead and that normal markdown
// resumes correctly afterward.
func TestSVGBlock_TrailingContentSameLineAsClose(t *testing.T) {
	src := "<svg width=\"20\" height=\"20\">\n" +
		"  <rect width=\"100%\" height=\"100%\"/>\n" +
		"</svg> trailing\n" +
		"\n" +
		"## Next heading\n" +
		"\n" +
		"More *prose* follows.\n"
	out := mustRender(t, src)
	got := countSVG(t, out)
	if got.svgElements != 1 || got.rectInSVG != 1 {
		t.Fatalf("counts = %+v, want svgElements=1 rectInSVG=1\nrendered:\n%s", got, out)
	}
	if !strings.Contains(out, "<h2") {
		t.Fatalf("expected the heading after the svg block to still parse as markdown:\n%s", out)
	}
	if !strings.Contains(out, "<em>prose</em>") {
		t.Fatalf("expected the paragraph after the svg block to still parse as markdown:\n%s", out)
	}
}

// TestSVGBlock_InertInsideFencedCodeBlock ensures a fenced code block containing an svg
// (with a blank line in it, so it would trigger our parser if it were live) is untouched:
// the fence must still win, and the svg text must come out escaped inside <pre><code>,
// never as a real <svg> element.
func TestSVGBlock_InertInsideFencedCodeBlock(t *testing.T) {
	src := "```html\n" +
		"<svg width=\"100\" height=\"50\">\n" +
		"  <rect width=\"100%\" height=\"100%\"/>\n" +
		"\n" +
		"  <text x=\"1\" y=\"1\">Fenced</text>\n" +
		"</svg>\n" +
		"```\n"
	out := mustRender(t, src)
	got := countSVG(t, out)
	if got.svgElements != 0 {
		t.Fatalf("expected no real <svg> element from inside a fenced code block, got %d\nrendered:\n%s", got.svgElements, out)
	}
	if !strings.Contains(out, "<pre") {
		t.Fatalf("expected the fenced block to still render as <pre>:\n%s", out)
	}
	// The syntax highlighter splits the escaped source across several <span> elements
	// (e.g. `&lt;` and `svg` land in separate spans), so a literal substring check for
	// "&lt;svg" on the raw HTML is not reliable. Instead, gather the actual text content
	// under the <pre> from the parsed DOM — which is stable regardless of how the
	// highlighter chose to tokenize it — and check it there.
	pre := findFirst(mustParse(t, out), "pre")
	if pre == nil {
		t.Fatalf("no <pre> element found in parsed DOM:\n%s", out)
	}
	if got := textContent(pre); !strings.Contains(got, "<svg") {
		t.Fatalf("expected the literal svg source text inside <pre>, got %q\nrendered:\n%s", got, out)
	}
}

// TestSVGBlock_InertInsideIndentedCodeBlock ensures a 4-space indented <svg> (a plain
// CommonMark indented code block) is untouched by the new parser.
func TestSVGBlock_InertInsideIndentedCodeBlock(t *testing.T) {
	src := "Some text.\n" +
		"\n" +
		"    <svg width=\"100\" height=\"50\">\n" +
		"      <rect width=\"100%\" height=\"100%\"/>\n" +
		"\n" +
		"      <text x=\"1\" y=\"1\">Indented block</text>\n" +
		"    </svg>\n" +
		"\n" +
		"More text.\n"
	out := mustRender(t, src)
	got := countSVG(t, out)
	if got.svgElements != 0 {
		t.Fatalf("expected no real <svg> element from a 4-space indented code block, got %d\nrendered:\n%s", got.svgElements, out)
	}
	if !strings.Contains(out, "<pre>") && !strings.Contains(out, "<pre ") {
		t.Fatalf("expected the indented block to still render as <pre>:\n%s", out)
	}
}

// TestSVGBlock_FailClosed is the fail-closed guarantee: an <svg> with no balancing </svg>
// anywhere in the document must render BYTE-IDENTICAL to the pre-fix renderer. The
// expected string below was captured by running this exact input through render.Markdown
// with the svg block parser registration removed (i.e. today's shipped behavior) —
// see the code review / PR description for how it was captured.
func TestSVGBlock_FailClosed(t *testing.T) {
	src := "Some intro.\n" +
		"\n" +
		"<svg width=\"100\" height=\"50\">\n" +
		"  <rect width=\"100%\" height=\"100%\" fill=\"#eee\"/>\n" +
		"\n" +
		"  <text x=\"1\" y=\"1\">Never closed</text>\n" +
		"\n" +
		"More paragraph text after.\n"
	const wantGolden = "<p>Some intro.</p>\n" +
		"<svg width=\"100\" height=\"50\">\n" +
		"  <rect width=\"100%\" height=\"100%\" fill=\"#eee\"/>\n" +
		"<p><text x=\"1\" y=\"1\">Never closed</text></p>\n" +
		"<p>More paragraph text after.</p>\n"
	out := mustRender(t, src)
	if out != wantGolden {
		t.Fatalf("fail-closed output changed.\ngot:\n%q\nwant (pre-fix golden):\n%q", out, wantGolden)
	}
}

// TestSVGBlock_RegressionOrdinaryMarkdown is the "everything else is unchanged" guard: a
// document exercising headings, GFM (tables, task lists, strikethrough, autolinks),
// footnotes, a highlighted fenced code block, and a d2 fence must render byte-identical to
// the pre-fix renderer, since none of it contains a top-level <svg>. The golden string was
// captured the same way as TestSVGBlock_FailClosed's.
func TestSVGBlock_RegressionOrdinaryMarkdown(t *testing.T) {
	src := "# Heading One\n" +
		"\n" +
		"Some *emphasis* and **strong** text with a footnote.[^1]\n" +
		"\n" +
		"| A | B |\n" +
		"|---|---|\n" +
		"| 1 | 2 |\n" +
		"\n" +
		"- [ ] todo\n" +
		"- [x] done\n" +
		"\n" +
		"~~strikethrough~~ and https://example.com autolink.\n" +
		"\n" +
		"```go\n" +
		"func main() {\n" +
		"\tfmt.Println(\"hi\")\n" +
		"}\n" +
		"```\n" +
		"\n" +
		"```d2\n" +
		"a -> b\n" +
		"```\n" +
		"\n" +
		"[^1]: a footnote body.\n"
	const wantGolden = "<h1 id=\"heading-one\">Heading One</h1>\n" +
		"<p>Some <em>emphasis</em> and <strong>strong</strong> text with a footnote.<sup id=\"fnref:1\"><a href=\"#fn:1\" class=\"footnote-ref\" role=\"doc-noteref\">1</a></sup></p>\n" +
		"<table>\n" +
		"<thead>\n" +
		"<tr>\n" +
		"<th>A</th>\n" +
		"<th>B</th>\n" +
		"</tr>\n" +
		"</thead>\n" +
		"<tbody>\n" +
		"<tr>\n" +
		"<td>1</td>\n" +
		"<td>2</td>\n" +
		"</tr>\n" +
		"</tbody>\n" +
		"</table>\n" +
		"<ul>\n" +
		"<li><input disabled=\"\" type=\"checkbox\"> todo</li>\n" +
		"<li><input checked=\"\" disabled=\"\" type=\"checkbox\"> done</li>\n" +
		"</ul>\n" +
		"<p><del>strikethrough</del> and <a href=\"https://example.com\">https://example.com</a> autolink.</p>\n" +
		"<pre class=\"chroma\"><code><span class=\"line\"><span class=\"cl\"><span class=\"kd\">func</span><span class=\"w\"> </span><span class=\"nf\">main</span><span class=\"p\">()</span><span class=\"w\"> </span><span class=\"p\">{</span><span class=\"w\">\n" +
		"</span></span></span><span class=\"line\"><span class=\"cl\"><span class=\"w\">\t</span><span class=\"nx\">fmt</span><span class=\"p\">.</span><span class=\"nf\">Println</span><span class=\"p\">(</span><span class=\"s\">&#34;hi&#34;</span><span class=\"p\">)</span><span class=\"w\">\n" +
		"</span></span></span><span class=\"line\"><span class=\"cl\"><span class=\"p\">}</span><span class=\"w\">\n" +
		"</span></span></span></code></pre><pre><code class=\"language-d2\">a -&gt; b\n" +
		"</code></pre>\n" +
		"<div class=\"footnotes\" role=\"doc-endnotes\">\n" +
		"<hr>\n" +
		"<ol>\n" +
		"<li id=\"fn:1\">\n" +
		"<p>a footnote body.&#160;<a href=\"#fnref:1\" class=\"footnote-backref\" role=\"doc-backlink\">&#x21a9;&#xfe0e;</a></p>\n" +
		"</li>\n" +
		"</ol>\n" +
		"</div>\n"
	out := mustRender(t, src)
	if out != wantGolden {
		t.Fatalf("ordinary markdown regressed.\ngot:\n%q\nwant (pre-fix golden):\n%q", out, wantGolden)
	}
}

// --- aihub#262 review-fix regression tests -------------------------------------------
//
// Every golden string below was captured by rendering the exact same input through both
// this file's fixed renderer and a build with the svg block parser's registration
// commented out in markdown.go (i.e. today's shipped, pre-fix behavior), and confirming
// the two outputs are byte-identical before hardcoding either one here — same method
// TestSVGBlock_FailClosed and TestSVGBlock_RegressionOrdinaryMarkdown already use.

// TestSVGBlock_SingleLine_TrailingMarkdown is finding [blocking] case 1: a single
// physical line containing a complete <svg>...</svg> followed by more markdown on the
// SAME line. A single line can never exhibit the blank-line bug this parser exists to
// fix, so the parser must not take the line over — **Red**, the link, and the code span
// must still parse as markdown instead of being swallowed into a raw HTML block.
func TestSVGBlock_SingleLine_TrailingMarkdown(t *testing.T) {
	src := "<svg width=\"42\" height=\"10\"><line x1=\"0\" y1=\"5\" x2=\"42\" y2=\"5\" stroke=\"#c77a22\"/></svg> **Red** means [down](http://x) `code`\n"
	const wantGolden = "<p><svg width=\"42\" height=\"10\"><line x1=\"0\" y1=\"5\" x2=\"42\" y2=\"5\" stroke=\"#c77a22\"/></svg> <strong>Red</strong> means <a href=\"http://x\">down</a> <code>code</code></p>\n"
	out := mustRender(t, src)
	if out != wantGolden {
		t.Fatalf("single-line svg with trailing markdown regressed.\ngot:\n%q\nwant:\n%q", out, wantGolden)
	}
}

// TestSVGBlock_SingleLine_AdjacentLegends is finding [blocking] case 2: two adjacent
// single-line SVGs (each its own <p>, blank-line separated — the real corpus shape from
// mem_CZKF5ZK5). Each must keep its own <p> wrapper; the parser must not fuse them into
// one multi-line raw HTML block.
func TestSVGBlock_SingleLine_AdjacentLegends(t *testing.T) {
	src := "<svg width=\"42\" height=\"10\"><line x1=\"0\" y1=\"5\" x2=\"42\" y2=\"5\" stroke=\"#c77a22\"/></svg>\n" +
		"\n" +
		"<svg width=\"42\" height=\"10\"><line x1=\"0\" y1=\"5\" x2=\"42\" y2=\"5\" stroke=\"#0e9e97\"/></svg>\n"
	const wantGolden = "<p><svg width=\"42\" height=\"10\"><line x1=\"0\" y1=\"5\" x2=\"42\" y2=\"5\" stroke=\"#c77a22\"/></svg></p>\n" +
		"<p><svg width=\"42\" height=\"10\"><line x1=\"0\" y1=\"5\" x2=\"42\" y2=\"5\" stroke=\"#0e9e97\"/></svg></p>\n"
	out := mustRender(t, src)
	if out != wantGolden {
		t.Fatalf("adjacent single-line legend svgs regressed.\ngot:\n%q\nwant:\n%q", out, wantGolden)
	}
}

// TestSVGBlock_SingleLine_SoftWrapContinuation is finding [blocking] case 3: a
// single-line <svg>...</svg> immediately followed (no blank line) by a soft-wrapped
// markdown continuation line. Both lines must stay one paragraph, exactly as before.
func TestSVGBlock_SingleLine_SoftWrapContinuation(t *testing.T) {
	src := "<svg width=\"10\" height=\"10\"></svg>\n" +
		"and *more* text\n"
	const wantGolden = "<p><svg width=\"10\" height=\"10\"></svg>\nand <em>more</em> text</p>\n"
	out := mustRender(t, src)
	if out != wantGolden {
		t.Fatalf("soft-wrapped continuation after single-line svg regressed.\ngot:\n%q\nwant:\n%q", out, wantGolden)
	}
}

// TestSVGBlock_Boundary_CloseExactlyAtEndOfLine pins segment.Stop's exact semantics for
// the first of the three boundary shapes called out in the review: the balancing </svg>
// ends right at the line's trailing '\n'. end lands ON that '\n' (one before Stop), so
// end <= segment.Stop and the line must NOT be taken over.
func TestSVGBlock_Boundary_CloseExactlyAtEndOfLine(t *testing.T) {
	src := "<svg width=\"1\" height=\"1\"></svg>\n"
	const wantGolden = "<p><svg width=\"1\" height=\"1\"></svg></p>\n"
	out := mustRender(t, src)
	if out != wantGolden {
		t.Fatalf("close-at-end-of-line boundary regressed.\ngot:\n%q\nwant:\n%q", out, wantGolden)
	}
}

// TestSVGBlock_Boundary_CloseFollowedByMoreTextOnLine pins the second boundary shape:
// the balancing </svg> is followed by more text before the line's own end. end still
// lands short of segment.Stop, so the line must NOT be taken over.
func TestSVGBlock_Boundary_CloseFollowedByMoreTextOnLine(t *testing.T) {
	src := "<svg width=\"1\" height=\"1\"></svg> trailing words\n"
	const wantGolden = "<p><svg width=\"1\" height=\"1\"></svg> trailing words</p>\n"
	out := mustRender(t, src)
	if out != wantGolden {
		t.Fatalf("close-followed-by-more-text boundary regressed.\ngot:\n%q\nwant:\n%q", out, wantGolden)
	}
}

// TestSVGBlock_Boundary_NoTrailingNewlineAtEOF pins the third boundary shape: the whole
// document is a single line with NO trailing newline at all, so segment.Stop ==
// len(source) (reader.go's AdvanceLine sets Stop to sourceLength when no '\n' is found).
// end also lands at len(source), so end <= segment.Stop and the line must NOT be taken
// over.
func TestSVGBlock_Boundary_NoTrailingNewlineAtEOF(t *testing.T) {
	src := "<svg width=\"1\" height=\"1\"></svg>"
	const wantGolden = "<p><svg width=\"1\" height=\"1\"></svg></p>\n"
	out := mustRender(t, src)
	if out != wantGolden {
		t.Fatalf("no-trailing-newline boundary regressed.\ngot:\n%q\nwant:\n%q", out, wantGolden)
	}
}

// TestSVGBlock_UnclosedThenLaterFenceContainingSVG is finding [warning] case A: an
// unclosed top-level <svg> followed, much later, by a FENCED code block that happens to
// contain a "</svg>" substring. Before the fence carve-out, the raw-byte lookahead would
// treat that fenced "</svg>" as the balancer and swallow the intervening prose and fence
// markers into one giant raw HTML block. The lookahead must stop at the fence wall,
// fail closed, and leave everything rendering exactly as it did pre-fix: the unclosed
// <svg> as its own (truncated) HTML block, the prose as paragraphs, and the fence as a
// normal highlighted code block.
func TestSVGBlock_UnclosedThenLaterFenceContainingSVG(t *testing.T) {
	src := "<svg width=\"10\" height=\"10\">\n" +
		"  <rect/>\n" +
		"\n" +
		"Some prose in between.\n" +
		"\n" +
		"```html\n" +
		"<svg>x</svg>\n" +
		"```\n" +
		"\n" +
		"More prose after.\n"
	const wantGolden = "<svg width=\"10\" height=\"10\">\n" +
		"  <rect/>\n" +
		"<p>Some prose in between.</p>\n" +
		"<pre class=\"chroma\"><code><span class=\"line\"><span class=\"cl\"><span class=\"p\">&lt;</span><span class=\"nt\">svg</span><span class=\"p\">&gt;</span>x<span class=\"p\">&lt;/</span><span class=\"nt\">svg</span><span class=\"p\">&gt;</span>\n" +
		"</span></span></code></pre><p>More prose after.</p>\n"
	out := mustRender(t, src)
	if out != wantGolden {
		t.Fatalf("unclosed-svg-then-later-fenced-svg regressed.\ngot:\n%q\nwant:\n%q", out, wantGolden)
	}
	if strings.Contains(out, "```") {
		t.Fatalf("fence markers leaked into the output:\n%s", out)
	}
}

// TestSVGBlock_UnclosedThenLaterInlineCodeSpanMentioningSVG is finding [warning] case B:
// an unclosed top-level <svg> followed, later, by prose that mentions "</svg>" inside a
// same-line inline code span ("The closing tag is `</svg>` here."). The coarse
// same-line-backtick heuristic must recognize this and refuse to treat it as the
// balancer, so the paragraph (and its code span) render normally instead of being
// swallowed into raw HTML.
func TestSVGBlock_UnclosedThenLaterInlineCodeSpanMentioningSVG(t *testing.T) {
	src := "<svg width=\"10\" height=\"10\">\n" +
		"  <rect/>\n" +
		"\n" +
		"The closing tag is `</svg>` here.\n" +
		"\n" +
		"More prose after.\n"
	const wantGolden = "<svg width=\"10\" height=\"10\">\n" +
		"  <rect/>\n" +
		"<p>The closing tag is <code>&lt;/svg&gt;</code> here.</p>\n" +
		"<p>More prose after.</p>\n"
	out := mustRender(t, src)
	if out != wantGolden {
		t.Fatalf("unclosed-svg-then-later-code-span-mentioning-svg regressed.\ngot:\n%q\nwant:\n%q", out, wantGolden)
	}
}

// TestSVGBlock_UnclosedDoesNotSwallowFollowingProse is the plain "swallows following
// prose" negative case (test-gaps finding, minor): an <svg> with no balancing </svg>
// anywhere must leave every paragraph that follows it fully intact and independently
// parsed, not merged into one block.
func TestSVGBlock_UnclosedDoesNotSwallowFollowingProse(t *testing.T) {
	src := "<svg width=\"10\" height=\"10\">\n" +
		"\n" +
		"First paragraph after.\n" +
		"\n" +
		"Second paragraph with **bold** after.\n"
	out := mustRender(t, src)
	if !strings.Contains(out, "<p>First paragraph after.</p>") {
		t.Fatalf("first following paragraph was not parsed as markdown:\n%s", out)
	}
	if !strings.Contains(out, "<p>Second paragraph with <strong>bold</strong> after.</p>") {
		t.Fatalf("second following paragraph was not parsed as markdown:\n%s", out)
	}
}

// TestSVGBlock_GuardrailInsideBlockquote and TestSVGBlock_GuardrailInsideListItem cover
// the test-gaps finding's "already work" guardrails: a multi-line <svg> with a blank
// line inside it, nested inside a blockquote or a list item. These must keep working
// after the narrowing and lookahead changes above.
func TestSVGBlock_GuardrailInsideBlockquote(t *testing.T) {
	src := "> <svg width=\"100\" height=\"50\">\n" +
		"> <rect width=\"100%\" height=\"100%\"/>\n" +
		">\n" +
		"> <text x=\"1\" y=\"1\">Q</text>\n" +
		"> </svg>\n"
	out := mustRender(t, src)
	got := countSVG(t, out)
	want := svgCounts{svgElements: 1, rectInSVG: 1, textInSVG: 1}
	if got != want {
		t.Fatalf("counts = %+v, want %+v\nrendered:\n%s", got, want, out)
	}
	if !strings.Contains(out, "<blockquote>") {
		t.Fatalf("expected a <blockquote> wrapper:\n%s", out)
	}
}

func TestSVGBlock_GuardrailInsideListItem(t *testing.T) {
	src := "- <svg width=\"100\" height=\"50\">\n" +
		"  <rect width=\"100%\" height=\"100%\"/>\n" +
		"\n" +
		"  <text x=\"1\" y=\"1\">L</text>\n" +
		"  </svg>\n"
	out := mustRender(t, src)
	got := countSVG(t, out)
	want := svgCounts{svgElements: 1, rectInSVG: 1, textInSVG: 1}
	if got != want {
		t.Fatalf("counts = %+v, want %+v\nrendered:\n%s", got, want, out)
	}
	if !strings.Contains(out, "<li>") {
		t.Fatalf("expected an <li> wrapper:\n%s", out)
	}
}

// buildUnclosedSVGDocument returns a document of n repeated, NEVER-closed top-level
// <svg ...> lines, each followed by a blank line — the exact pathological shape from the
// [warning] performance finding: every one of these lines is offered to Open() as an
// independent new-block attempt (the blank line ends whatever the previous, stock-parsed
// HTML block was), and none of them ever finds a balancing </svg> anywhere in the
// document.
func buildUnclosedSVGDocument(n int) string {
	var sb strings.Builder
	for i := 0; i < n; i++ {
		sb.WriteString("<svg width=\"10\" height=\"10\">\n\n")
	}
	return sb.String()
}

// buildNestedUnbalancedSVGDocument returns a document of n repeated "<svg>\n<svg>\n
// </svg>\n\n" groups. Each group's OUTER <svg> never balances (there are two opening tags
// and only one closing tag per group), so every top-level Open() attempt's lookahead does
// see plenty of real </svg> tokens on its way to failing — this is the shape that made the
// now-deleted memoization backstop miss entirely (see the package comment's "A
// memoization scheme that does NOT work" section): that memo's key was derived from the
// LAST </svg> token seen, which sits near EOF, so no earlier <svg> opener's own offset was
// ever >= it, and the memo never fired. This is round 2's second pathological shape
// (measured pre-fix at 8m20s / 8.25B tokenizer steps at 1MB).
func buildNestedUnbalancedSVGDocument(n int) string {
	var sb strings.Builder
	for i := 0; i < n; i++ {
		sb.WriteString("<svg>\n<svg>\n</svg>\n\n")
	}
	return sb.String()
}

// renderWork is what one render of a document cost, measured four ways. Three of the
// four (the counters) are deterministic: they are identical run to run and, unlike
// elapsed, identical no matter how busy the machine is. Only elapsed is reported to a
// human; nothing in this file asserts on it — see assertFenceScanIsOnePass below and
// aihub#339 for why every absolute wall-clock ceiling here was replaced by a counter.
type renderWork struct {
	elapsed            time.Duration
	tokenizerSteps     int64 // svgTokenizerStepsForTest delta: findSVGBlockEnd lookahead work
	fenceScanLines     int64 // svgFenceScanLinesForTest delta: lines walked for fence offsets
	fenceBoundaryCalls int64 // svgFenceBoundaryCallsForTest delta: firstFenceBoundary calls
}

// renderAndCountWork renders src and returns the work it cost — the wall-clock time plus
// the three process-wide work counters this package exports for tests
// (svgTokenizerStepsForTest, svgFenceScanLinesForTest, svgFenceBoundaryCallsForTest).
// The counters are robust, deterministic proxies for work done, preferred here over
// wall-clock per the review's guidance, since counts don't flake under machine load.
//
// Must not be called from a t.Parallel() test: all three are single process-wide
// counters, and the before/after deltas this function computes are only meaningful if
// nothing else in the process is concurrently rendering SVG blocks and bumping them in
// between.
func renderAndCountWork(t *testing.T, src string) renderWork {
	t.Helper()
	beforeSteps := svgTokenizerStepsForTest.Load()
	beforeScanLines := svgFenceScanLinesForTest.Load()
	beforeBoundaryCalls := svgFenceBoundaryCallsForTest.Load()
	start := time.Now()
	if _, err := Markdown(src); err != nil {
		t.Fatal(err)
	}
	elapsed := time.Since(start)
	return renderWork{
		elapsed:            elapsed,
		tokenizerSteps:     svgTokenizerStepsForTest.Load() - beforeSteps,
		fenceScanLines:     svgFenceScanLinesForTest.Load() - beforeScanLines,
		fenceBoundaryCalls: svgFenceBoundaryCallsForTest.Load() - beforeBoundaryCalls,
	}
}

// sourceLines counts the lines computeFenceOffsets walks for src: it consumes source
// byte-by-byte to the end, so a document ending in "\n" has exactly as many lines as it
// has newlines, and one more otherwise.
func sourceLines(src string) int64 {
	n := int64(strings.Count(src, "\n"))
	if !strings.HasSuffix(src, "\n") {
		n++
	}
	return n
}

// assertFenceScanIsOnePass asserts that the fence-offset lookup for a whole parse of src
// cost exactly ONE pass over the document, no matter how many <svg> opener lines it
// contains. computeFenceOffsets runs at most once per parse (its result is cached on
// parser.Context under svgFenceOffsetsKey) and walks every line exactly once, so its
// counter must come out equal to the document's line count on the nose; firstFenceBoundary
// binary-searches that cached index and walks no lines at all, so it can only ever be
// called once per Open() attempt, never more than there are lines.
//
// This is the load-immune replacement (aihub#339) for the absolute wall-clock ceilings
// that used to sit at the end of each test below. Those ceilings were nominally the
// backstop for "a regression the tokenizer-step counter cannot see", and the one concrete
// regression of that kind on record is round 3's per-call rescan of the rest of the
// document inside firstFenceBoundary — O(n^2) line walks with the step count unchanged.
// A wall-clock ceiling detected that only if the extra walking happened to cross a fixed
// number of seconds, which at these document sizes it does not; this equality detects it
// outright, and is unaffected by how many other tests, packages or agents are competing
// for the CPU at the time.
func assertFenceScanIsOnePass(t *testing.T, label, src string, w renderWork) {
	t.Helper()
	totalLines := sourceLines(src)
	if w.fenceScanLines != totalLines {
		t.Fatalf("%s: computeFenceOffsets walked %d lines rendering a %d-line document, want exactly %d (one cached pass over the whole document) — a larger value means fence lookup is re-scanning per Open() call instead of using the cached index, reintroducing the O(n^2) blowup round 3 removed",
			label, w.fenceScanLines, totalLines, totalLines)
	}
	if w.fenceBoundaryCalls > totalLines {
		t.Fatalf("%s: firstFenceBoundary was called %d times rendering a %d-line document, want at most one call per line (%d) — more means Open() is consulting the fence index repeatedly per attempt",
			label, w.fenceBoundaryCalls, totalLines, totalLines)
	}
}

// assertStepsWithinBudget asserts that the tokenizer steps spent rendering a document of
// length n are within the K * n bound that svgBlockWorkBudgetKey / K
// (svgBlockWorkBudgetMultiplier) guarantee by construction — see the package comment's
// "Performance" section. This is the direct, deterministic assertion that total lookahead
// work for the WHOLE parse is linear in document size, independent of document shape.
func assertStepsWithinBudget(t *testing.T, label string, n int, steps int64) {
	t.Helper()
	limit := int64(svgBlockWorkBudgetMultiplier) * int64(n)
	if steps > limit {
		t.Fatalf("%s: tokenizer steps = %d for a %d-byte document, want <= K*n = %d*%d = %d (the per-parse work budget was exceeded)",
			label, steps, n, svgBlockWorkBudgetMultiplier, n, limit)
	}
}

// TestSVGBlock_PathologicalRepeatedUnclosedIsLinearNotQuadratic is the performance
// finding's complexity guard for pathological shape 1 (repeated, independently unclosed
// top-level <svg> lines). It renders the pathological document at two sizes 16x apart and
// asserts both (a) growth in tokenizer steps tracks the 16x line-count increase rather
// than the ~256x a quadratic algorithm would produce, and (b) steps at both sizes are
// within the K*n bound the shared per-parse work budget guarantees by construction.
func TestSVGBlock_PathologicalRepeatedUnclosedIsLinearNotQuadratic(t *testing.T) {
	const small = 250
	const big = small * 16 // 4000

	smallSrc := buildUnclosedSVGDocument(small)
	smallWork := renderAndCountWork(t, smallSrc)
	smallSteps := smallWork.tokenizerSteps
	assertStepsWithinBudget(t, "small", len(smallSrc), smallSteps)
	assertFenceScanIsOnePass(t, "small", smallSrc, smallWork)

	bigSrc := buildUnclosedSVGDocument(big)
	bigWork := renderAndCountWork(t, bigSrc)
	bigSteps, elapsed := bigWork.tokenizerSteps, bigWork.elapsed
	assertStepsWithinBudget(t, "big", len(bigSrc), bigSteps)
	assertFenceScanIsOnePass(t, "big", bigSrc, bigWork)

	t.Logf("repeated-unclosed: %d lines -> %d steps; %d lines -> %d steps (ratio %.1fx for a %.0fx line-count increase); %d-line render took %s (fence scan %d lines / %d calls)",
		small, smallSteps, big, bigSteps, float64(bigSteps)/float64(smallSteps), float64(big)/float64(small), big, elapsed, bigWork.fenceScanLines, bigWork.fenceBoundaryCalls)

	// Linear growth would be ~16x; quadratic would be ~256x. Use a generous threshold
	// (40x) that comfortably separates the two without being sensitive to small
	// per-document constant overhead.
	const maxAcceptableRatio = 40.0
	if ratio := float64(bigSteps) / float64(smallSteps); ratio > maxAcceptableRatio {
		t.Fatalf("tokenizer step growth looks quadratic: %d lines -> %d steps, %d lines -> %d steps (%.1fx growth for a 16x line-count increase, want <= %.1fx)",
			small, smallSteps, big, bigSteps, ratio, maxAcceptableRatio)
	}

	// The `elapsed > 5*time.Second` ceiling that used to close this test was deleted in
	// aihub#339. Measured on a 12-core box under -race, this render takes 2.82-3.44s
	// idle — 1.45x below its own ceiling — and 6.6-7.7s with a single competing process
	// pinned to the same core, so the assertion reddened on machine load rather than on
	// anything about this package. The counters above are unmoved by that same load
	// (60000 and 960000 steps, exactly, at every contention level measured), and
	// assertFenceScanIsOnePass covers the "regression the step counter cannot see" the
	// deleted ceiling was nominally standing in for. See that helper's doc comment.
}

// TestSVGBlock_PathologicalNestedUnbalancedIsLinearNotQuadratic is the performance
// finding's complexity guard for pathological shape 2 (repeated "<svg>\n<svg>\n</svg>\n\n"
// groups, each with an unbalanced OUTER <svg>) — the shape round 2 measured at 8m20s /
// 8.25 billion tokenizer steps at 1MB pre-fix, and the shape the deleted "byte-suffix"
// memoization never helped with at all (see buildNestedUnbalancedSVGDocument's doc
// comment). Same two assertions as the repeated-unclosed test above: bounded step growth
// across a 16x size increase, and steps within the K*n budget bound at both sizes.
func TestSVGBlock_PathologicalNestedUnbalancedIsLinearNotQuadratic(t *testing.T) {
	const small = 250
	const big = small * 16 // 4000

	smallSrc := buildNestedUnbalancedSVGDocument(small)
	smallWork := renderAndCountWork(t, smallSrc)
	smallSteps := smallWork.tokenizerSteps
	assertStepsWithinBudget(t, "small", len(smallSrc), smallSteps)
	assertFenceScanIsOnePass(t, "small", smallSrc, smallWork)

	bigSrc := buildNestedUnbalancedSVGDocument(big)
	bigWork := renderAndCountWork(t, bigSrc)
	bigSteps, elapsed := bigWork.tokenizerSteps, bigWork.elapsed
	assertStepsWithinBudget(t, "big", len(bigSrc), bigSteps)
	assertFenceScanIsOnePass(t, "big", bigSrc, bigWork)

	t.Logf("nested-unbalanced: %d groups -> %d steps; %d groups -> %d steps (ratio %.1fx for a %.0fx group-count increase); %d-group render took %s (fence scan %d lines / %d calls)",
		small, smallSteps, big, bigSteps, float64(bigSteps)/float64(smallSteps), float64(big)/float64(small), big, elapsed, bigWork.fenceScanLines, bigWork.fenceBoundaryCalls)

	const maxAcceptableRatio = 40.0
	if ratio := float64(bigSteps) / float64(smallSteps); ratio > maxAcceptableRatio {
		t.Fatalf("tokenizer step growth looks quadratic: %d groups -> %d steps, %d groups -> %d steps (%.1fx growth for a 16x group-count increase, want <= %.1fx)",
			small, smallSteps, big, bigSteps, ratio, maxAcceptableRatio)
	}

	// The `elapsed > 5*time.Second` ceiling here was deleted alongside its twin above in
	// aihub#339, for the same reason and on the same evidence: 0.98-1.30s idle, 2.01s
	// with one competing process on the same core, i.e. a ceiling that measures the
	// machine. See assertFenceScanIsOnePass for what replaced it.
}

// TestSVGBlock_PathologicalAt256KiB is the explicit measurement round 2 asked for, for
// BOTH pathological shapes at once, sized per the round-3 review's [warning] W2 finding
// instead of the original ~1MB: under `go test -race` (which CI runs), the original
// 1MB-sized test cost ~66s of CI time for an assertion protected by a 2-minute ceiling
// that the very regression it exists to catch (an 82.5s slowdown) would have sailed
// through unnoticed — a ceiling that cannot fail is worse than no test. 256 KiB affords a
// genuinely TIGHT ceiling (so a reintroduced quadratic blowup is still caught) at about a
// quarter of the wall-clock cost. See TestSVGBlock_PathologicalAtOneMegabyte below
// (gated behind AIHUB_SLOW_TESTS=1) for an occasional full-size manual check, and the two
// …IsLinearNotQuadratic tests above (cheap, kept at their original 250/4000-line sizes)
// for the ongoing linear-growth evidence.
func TestSVGBlock_PathologicalAt256KiB(t *testing.T) {
	const target = 256 << 10 // 256 KiB

	repeatedN := target / len("<svg width=\"10\" height=\"10\">\n\n")
	repeatedSrc := buildUnclosedSVGDocument(repeatedN)
	repeated := renderAndCountWork(t, repeatedSrc)
	assertStepsWithinBudget(t, "repeated-unclosed @ 256KiB", len(repeatedSrc), repeated.tokenizerSteps)
	assertFenceScanIsOnePass(t, "repeated-unclosed @ 256KiB", repeatedSrc, repeated)
	t.Logf("repeated-unclosed @ 256KiB: %d bytes, %s, %d tokenizer steps (%.2fx document size), fence scan %d lines / %d calls",
		len(repeatedSrc), repeated.elapsed, repeated.tokenizerSteps, float64(repeated.tokenizerSteps)/float64(len(repeatedSrc)), repeated.fenceScanLines, repeated.fenceBoundaryCalls)

	nestedN := target / len("<svg>\n<svg>\n</svg>\n\n")
	nestedSrc := buildNestedUnbalancedSVGDocument(nestedN)
	nested := renderAndCountWork(t, nestedSrc)
	assertStepsWithinBudget(t, "nested-unbalanced @ 256KiB", len(nestedSrc), nested.tokenizerSteps)
	assertFenceScanIsOnePass(t, "nested-unbalanced @ 256KiB", nestedSrc, nested)
	t.Logf("nested-unbalanced @ 256KiB: %d bytes, %s, %d tokenizer steps (%.2fx document size), fence scan %d lines / %d calls",
		len(nestedSrc), nested.elapsed, nested.tokenizerSteps, float64(nested.tokenizerSteps)/float64(len(nestedSrc)), nested.fenceScanLines, nested.fenceBoundaryCalls)

	// The two `elapsed > 20*time.Second` ceilings that used to close this test were
	// deleted in aihub#339. They were the ones this test's own doc comment above calls
	// "genuinely TIGHT" — and they were: the repeated-unclosed render measures 7.0s idle
	// and 13.1s with a single competing process pinned to the same core, i.e. 65% of the
	// ceiling consumed by one busy neighbour. That is not a tight gate, it is a gate one
	// unrelated CI job away from red. Both are replaced by the counter assertions above,
	// which measured identical (2097120 steps, 8.00x document size, exactly) at every
	// contention level.
}

// TestSVGBlock_PathologicalAtOneMegabyte is the full ~1MB measurement, for BOTH
// pathological shapes at once, retained as an occasional manual check rather than a
// default CI test — per the round-3 review's [warning] W2 finding: `testing.Short()`
// doesn't help here because CI does not pass `-short`, so the only way to keep the
// expensive full-size run out of every CI invocation is an explicit opt-in env var. Run
// it directly with:
//
//	AIHUB_SLOW_TESTS=1 go test ./internal/render -run TestSVGBlock_PathologicalAtOneMegabyte -race -v
//
// Pre-fix (and pre-round-2-fix, with the unsound memo in place), the nested-unbalanced
// shape measured 8m20s / 8.25B tokenizer steps at 1MB; post-fix, both shapes are bounded
// to K*n tokenizer steps (K = svgBlockWorkBudgetMultiplier) and complete in well under a
// second outside of -race.
func TestSVGBlock_PathologicalAtOneMegabyte(t *testing.T) {
	if os.Getenv("AIHUB_SLOW_TESTS") == "" {
		t.Skip("slow full-size (~1MB) manual check; set AIHUB_SLOW_TESTS=1 to run — see doc comment; TestSVGBlock_PathologicalAt256KiB above covers this in CI")
	}
	const target = 1 << 20 // 1 MiB

	repeatedN := target / len("<svg width=\"10\" height=\"10\">\n\n")
	repeatedSrc := buildUnclosedSVGDocument(repeatedN)
	repeated := renderAndCountWork(t, repeatedSrc)
	assertStepsWithinBudget(t, "repeated-unclosed @ ~1MB", len(repeatedSrc), repeated.tokenizerSteps)
	assertFenceScanIsOnePass(t, "repeated-unclosed @ ~1MB", repeatedSrc, repeated)
	t.Logf("repeated-unclosed @ ~1MB: %d bytes, %s, %d tokenizer steps (%.2fx document size), fence scan %d lines / %d calls",
		len(repeatedSrc), repeated.elapsed, repeated.tokenizerSteps, float64(repeated.tokenizerSteps)/float64(len(repeatedSrc)), repeated.fenceScanLines, repeated.fenceBoundaryCalls)

	nestedN := target / len("<svg>\n<svg>\n</svg>\n\n")
	nestedSrc := buildNestedUnbalancedSVGDocument(nestedN)
	nested := renderAndCountWork(t, nestedSrc)
	assertStepsWithinBudget(t, "nested-unbalanced @ ~1MB", len(nestedSrc), nested.tokenizerSteps)
	assertFenceScanIsOnePass(t, "nested-unbalanced @ ~1MB", nestedSrc, nested)
	t.Logf("nested-unbalanced @ ~1MB: %d bytes, %s, %d tokenizer steps (%.2fx document size), fence scan %d lines / %d calls",
		len(nestedSrc), nested.elapsed, nested.tokenizerSteps, float64(nested.tokenizerSteps)/float64(len(nestedSrc)), nested.fenceScanLines, nested.fenceBoundaryCalls)

	// The two `elapsed > 2*time.Minute` ceilings here were deleted in aihub#339 with the
	// rest of this file's absolute wall-clock ceilings. This pair was the clearest case:
	// this test's own sibling doc comment already records that a 2-minute ceiling would
	// have let the 82.5s regression it exists to catch "sail through unnoticed" — a
	// ceiling that cannot fail on the defect, but can fail on a busy machine, is pure
	// noise. The counter assertions above are exact and load-immune.
}

// buildWellFormedSingleLineSVGDocument returns a document of n well-formed, complete,
// single-line "<svg ...><line/></svg>" legend lines, each its own paragraph (blank-line
// separated) — the [blocking] finding's hazard shape: every line is entirely valid
// content, each is offered to Open() as its own new-block attempt, and Open() correctly
// declines every one of them (findSVGBlockEnd's tokenizer finds the balancing </svg> in
// only a few steps, and "end <= segment.Stop" — see "Only multi-line <svg> is taken
// over" — sends the line back to the stock parser unmodified). Before the round-3 fix,
// none of that mattered: firstFenceBoundary's per-call, per-line rescan of "the rest of
// the document" ran to real EOF on every single one of these Open() calls regardless of
// how cheap the tokenizer scan itself was, making the whole document's total lookahead
// work quadratic in line count even though every individual decision was O(1).
func buildWellFormedSingleLineSVGDocument(n int) string {
	const legend = "<svg width=\"42\" height=\"10\"><line x1=\"0\" y1=\"5\" x2=\"42\" y2=\"5\" stroke=\"#c77a22\"/></svg>\n\n"
	var sb strings.Builder
	sb.Grow(n * len(legend))
	for i := 0; i < n; i++ {
		sb.WriteString(legend)
	}
	return sb.String()
}

// TestSVGBlock_WellFormedSingleLineSVGLegendsIsLinearNotQuadratic is the [blocking]
// finding's regression test: it reproduces the reviewer's A/B measurement shape (many
// well-formed, correctly-declined single-line <svg> legends) and asserts both a
// correctness property (every legend stays its own single-line <p>-wrapped inline SVG —
// never fused into a multi-line raw block, matching TestSVGBlock_SingleLine_*) and a
// TIGHT performance ceiling. Sized at 256 KiB per the same [warning] W2 sizing discipline
// applied to TestSVGBlock_PathologicalAt256KiB above (the reviewer's own A/B table used
// 128KB-1MB and measured up to a 339x slowdown / 82.5s at 1MB pre-fix; 256 KiB keeps this
// tight under -race while remaining large enough — tens of thousands of lines — to make a
// reintroduced O(n²) fence rescan impossible to hide from a several-second ceiling).
func TestSVGBlock_WellFormedSingleLineSVGLegendsIsLinearNotQuadratic(t *testing.T) {
	const target = 256 << 10 // 256 KiB
	const legend = "<svg width=\"42\" height=\"10\"><line x1=\"0\" y1=\"5\" x2=\"42\" y2=\"5\" stroke=\"#c77a22\"/></svg>\n\n"
	n := target / len(legend)

	src := buildWellFormedSingleLineSVGDocument(n)
	start := time.Now()
	out, err := Markdown(src)
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("Markdown() error: %v", err)
	}

	if got := strings.Count(out, "<p><svg"); got != n {
		t.Fatalf("expected all %d legends to stay single-line inline <p><svg> paragraphs (none taken over as a multi-line raw block), got %d such paragraphs\n(document was %d bytes)", n, got, len(src))
	}

	t.Logf("%d well-formed single-line svg legends (%d bytes) rendered in %s", n, len(src), elapsed)
	// The `elapsed > 5*time.Second` ceiling that used to sit here was deleted in
	// aihub#339. It described itself as a "tight ceiling" with "an enormous margin", and
	// both cannot be true at once: measured under -race on a 12-core box this render
	// takes 1.77s idle and 3.70s with one competing process pinned to the same core, so
	// two busy neighbours redden it. The deterministic guard immediately below, added in
	// round 5 for exactly this reason, already covers the same O(n²) fence rescan on the
	// same document — with an equality rather than a threshold. The ceiling added
	// nothing but a way for an unrelated CI job to fail this one.

	// --- round-5 review-fix: deterministic fence-lookup-work guard ---------------------
	//
	// The round-4 growth-ratio guard that used to live here (best-of-5 wall-clock timings
	// at ~128 KiB vs ~256 KiB, asserting ratio <= 2.5) was shown by review to flake on a
	// loaded machine: with all 16 cores busy, 2 of 12 runs measured ratios up to 3.51,
	// while the regressed build itself measures 3.65 on the same machine — the
	// false-positive range and the true-positive value overlap, so no threshold
	// separates them. It is replaced below by a deterministic counter assertion that
	// needs no threshold, and cannot flake under load, because it counts work done
	// rather than time elapsed.
	//
	// The invariant: total fence-lookup work for a whole parse is bounded by ONE pass
	// over the document (computeFenceOffsets, which runs exactly once per parse and is
	// cached on parser.Context — see svgFenceOffsetsKey) plus a constant amount of work
	// per Open() call (firstFenceBoundary's O(log n) binary search over that cached
	// index) — i.e. roughly total_lines + C*opener_lines for a small constant C.
	// Concretely, for this document's `n` opener (legend) lines and its `totalLines`
	// lines total:
	//
	//   - svgFenceScanLinesForTest (lines actually walked by computeFenceOffsets) must
	//     equal EXACTLY totalLines: one pass, regardless of how many opener lines the
	//     document has. In this fixed implementation firstFenceBoundary contributes
	//     nothing to this counter (it binary-searches, it never walks lines), so C=0
	//     here and the bound collapses to an exact equality rather than a fuzzy ceiling.
	//   - svgFenceBoundaryCallsForTest (calls to firstFenceBoundary) must equal EXACTLY
	//     n: one O(1) lookup per Open() attempt, never more.
	//
	// The round-3 regression this guards against had firstFenceBoundary itself re-walk
	// "the rest of the document" on every call instead of using the cached index: that
	// would add the REMAINING line count into svgFenceScanLinesForTest on every one of
	// the n calls (roughly n * average-remaining-lines ~ O(n^2)), overshooting
	// totalLines by orders of magnitude instead of equalling it exactly — see the
	// deliberately-reverted-and-restored proof for this test recorded alongside this
	// change (firstFenceBoundary temporarily reverted to a per-call rescan; this
	// assertion failed with scanLines many orders of magnitude above totalLines).
	totalLines := int64(strings.Count(src, "\n"))
	if !strings.HasSuffix(src, "\n") {
		totalLines++
	}

	// Read the two counters' deltas across exactly one render of src. Like
	// renderAndCountSteps above, this must not run from a t.Parallel() test:
	// svgFenceScanLinesForTest and svgFenceBoundaryCallsForTest are both process-wide
	// counters, and the before/after delta computed here is only meaningful if nothing
	// else in the process is concurrently rendering SVG blocks and bumping the same
	// counters in between.
	beforeScanLines := svgFenceScanLinesForTest.Load()
	beforeBoundaryCalls := svgFenceBoundaryCallsForTest.Load()
	if _, err := Markdown(src); err != nil {
		t.Fatalf("Markdown() error: %v", err)
	}
	scanLines := svgFenceScanLinesForTest.Load() - beforeScanLines
	boundaryCalls := svgFenceBoundaryCallsForTest.Load() - beforeBoundaryCalls

	t.Logf("fence-lookup work: %d opener lines, %d total lines -> computeFenceOffsets scanned %d lines (want exactly %d), firstFenceBoundary called %d times (want exactly %d)",
		n, totalLines, scanLines, totalLines, boundaryCalls, n)

	if scanLines != totalLines {
		t.Fatalf("computeFenceOffsets scanned %d lines rendering a %d-opener-line document with %d total lines, want exactly %d (one pass over the whole document) — a larger value means fence lookup is re-scanning per Open() call instead of using the cached index, reintroducing the O(n^2) blowup round 3 removed",
			scanLines, n, totalLines, totalLines)
	}
	if boundaryCalls != int64(n) {
		t.Fatalf("firstFenceBoundary was called %d times rendering a %d-opener-line document, want exactly %d (one call per Open() attempt)",
			boundaryCalls, n, n)
	}
}

// --- aihub#262 review round-2 regression tests -----------------------------------------

// TestSVGBlock_RawtextTitleDoesNotHideRealCloser and
// TestSVGBlock_RawtextScriptDoesNotHideRealCloser are the [warning] "memo's byte-suffix
// invariant is provably false" regression tests. An early, unclosed `<svg><title>` (a
// routine SVG accessibility child) or `<svg><script>` puts the x/net/html tokenizer into
// rawtext mode for the rest of the document — hiding every `</svg>` token in it from that
// one scan — but must NOT prevent a LATER, independent, well-formed multi-line <svg>
// elsewhere in the document from being fixed by this parser. The now-deleted memoization
// scheme broke exactly this: because its first scan saw zero `</svg>` tokens (they were
// all hidden inside rawtext), it recorded "no closer anywhere from the very start of the
// document" and every later Open() call — including the one for the well-formed second
// <svg> — short-circuited to nil without ever scanning. The fix in this round has no
// memoization to get this wrong.
// This test cannot use the countSVG tree-walk helper the way its script sibling does:
// golang.org/x/net/html's TREE BUILDER (unlike the standalone Tokenizer findSVGBlockEnd
// itself uses) treats SVG's <title> as an HTML integration point, and the dangling,
// unterminated <title> here makes the tree builder reparent everything that follows —
// including the second, well-formed <svg>'s own <rect> and <text> — underneath that one
// <title> element regardless of whether this parser's fix actually fired. That made a
// tree-shape assertion here accidentally pass even with the fix disabled (round-3 review
// finding: this test was vacuous). Verified directly: with the block parser's
// registration in markdown.go commented out, a countSVG-based assertion on this input
// still reported rectInSVG=1 && textInSVG=1.
//
// What IS a reliable, fix-specific signal is the raw bytes this parser is responsible
// for: only when the second <svg> is taken over as one contiguous raw HTML block does
// its blank line survive verbatim, with the original two-space indent on the <text> line
// and no "<p>" wrapper inserted in front of it. Pre-fix (or budget-exhausted) rendering
// always drops that blank line, strips the leading indent, and inserts "<p>" there
// instead — confirmed empirically the same way (parser disabled: fragment absent;
// parser enabled: fragment present).
func TestSVGBlock_RawtextTitleDoesNotHideRealCloser(t *testing.T) {
	src := "<svg width=\"1\" height=\"1\"><title>unterminated\n" +
		"\n" +
		"<svg width=\"200\" height=\"100\">\n" +
		"  <rect width=\"100%\" height=\"100%\"/>\n" +
		"\n" +
		"  <text x=\"1\" y=\"1\">Second</text>\n" +
		"</svg>\n"
	out := mustRender(t, src)
	const wantFixedFragment = "  <rect width=\"100%\" height=\"100%\"/>\n" +
		"\n" +
		"  <text x=\"1\" y=\"1\">Second</text>\n" +
		"</svg>"
	if !strings.Contains(out, wantFixedFragment) {
		t.Fatalf("the later well-formed <svg> was not taken over as one contiguous raw block:\ngot:\n%q\nwant substring:\n%q", out, wantFixedFragment)
	}
}

func TestSVGBlock_RawtextScriptDoesNotHideRealCloser(t *testing.T) {
	src := "<svg width=\"1\" height=\"1\"><script>var a=\"unterminated\n" +
		"\n" +
		"<svg width=\"200\" height=\"100\">\n" +
		"  <rect width=\"100%\" height=\"100%\"/>\n" +
		"\n" +
		"  <text x=\"1\" y=\"1\">Second</text>\n" +
		"</svg>\n"
	out := mustRender(t, src)
	got := countSVG(t, out)
	if got.rectInSVG != 1 || got.textInSVG != 1 {
		t.Fatalf("the later well-formed <svg> was not fixed: counts = %+v, want rectInSVG=1 textInSVG=1\nrendered:\n%s", got, out)
	}
}

// TestSVGBlock_FenceWallMiss_InsideBlockquote pins the [warning] "firstFenceBoundary is
// not detected precisely" finding's first documented miss: a fence opened inside a
// blockquote (`> ```) is not recognized by fencedCodeOpenRegexp (its 0-3-leading-space
// budget is checked against the RAW line, and a blockquote-prefixed line starts with '>',
// not a space or backtick), so the wall never goes up. An unclosed top-level <svg>
// earlier in the document therefore has its lookahead run straight through the fence and
// match the literal `</svg>` written inside the blockquote's fenced code block, consuming
// everything up through it as one raw HTML block and mangling the blockquote (splitting
// it into an absorbed prefix and a leftover, bogus empty code block) — exactly the
// documented, accepted fail-OPEN behavior. This pins the actual output so the documented
// contract cannot drift silently; it is not asserting this is *good* output, only that it
// is the known, accepted one.
func TestSVGBlock_FenceWallMiss_InsideBlockquote(t *testing.T) {
	src := "<svg width=\"10\" height=\"10\">\n" +
		"  <rect/>\n" +
		"\n" +
		"> ```\n" +
		"> </svg>\n" +
		"> ```\n" +
		"\n" +
		"More prose after.\n"
	const wantGolden = "<svg width=\"10\" height=\"10\">\n" +
		"  <rect/>\n" +
		"\n" +
		"> ```\n" +
		"> </svg>\n" +
		"<blockquote>\n" +
		"<pre><code></code></pre>\n" +
		"</blockquote>\n" +
		"<p>More prose after.</p>\n"
	out := mustRender(t, src)
	if out != wantGolden {
		t.Fatalf("fence-wall-miss-inside-blockquote regressed.\ngot:\n%q\nwant:\n%q", out, wantGolden)
	}
}

// TestSVGBlock_FenceWallMiss_IndentedInListItem pins the documented finding's second
// miss: a fence indented to a list item's own content column can be legal CommonMark (see
// TestSVGBlock_ListItemFenceIndent_IsLegalWithoutSVG for a plain, svg-free proof that
// goldmark itself renders this shape as a real fence), yet still have 4+ RAW leading
// spaces whenever the list marker itself is 4+ columns wide (here "10. "). Because
// fencedCodeOpenRegexp checks absolute column, not indentation relative to the list
// item, it misses this fence too, and an unclosed earlier <svg>'s lookahead again runs
// through it — this time destroying the <ol>/<li> structure entirely (the whole list item
// text is swallowed into the raw HTML block) rather than a blockquote.
func TestSVGBlock_FenceWallMiss_IndentedInListItem(t *testing.T) {
	src := "<svg width=\"10\" height=\"10\">\n" +
		"  <rect/>\n" +
		"\n" +
		"10. item one\n" +
		"\n" +
		"    ```\n" +
		"    </svg>\n" +
		"    ```\n" +
		"\n" +
		"More prose after.\n"
	const wantGolden = "<svg width=\"10\" height=\"10\">\n" +
		"  <rect/>\n" +
		"\n" +
		"10. item one\n" +
		"\n" +
		"    ```\n" +
		"    </svg>\n" +
		"<pre><code>```\n" +
		"</code></pre>\n" +
		"<p>More prose after.</p>\n"
	out := mustRender(t, src)
	if out != wantGolden {
		t.Fatalf("fence-wall-miss-indented-in-list-item regressed.\ngot:\n%q\nwant:\n%q", out, wantGolden)
	}
}

// TestSVGBlock_ListItemFenceIndent_IsLegalWithoutSVG is the control for the test above:
// with no <svg> anywhere in the document, the exact same 4-space-indented fence inside a
// "10. " list item (marker width 4) renders as a genuine fenced code block, proving the
// shape really is a fence that fencedCodeOpenRegexp's raw-column check misses — not a
// contrived, illegal shape.
func TestSVGBlock_ListItemFenceIndent_IsLegalWithoutSVG(t *testing.T) {
	src := "10. item one\n" +
		"\n" +
		"    ```\n" +
		"    code line\n" +
		"    ```\n" +
		"\n" +
		"More prose after.\n"
	out := mustRender(t, src)
	if !strings.Contains(out, "<ol start=\"10\">") {
		t.Fatalf("expected a real <ol> list, the fence must not have disturbed it:\n%s", out)
	}
	if !strings.Contains(out, "<pre><code>code line\n</code></pre>") {
		t.Fatalf("expected the indented fence to render as a genuine fenced code block:\n%s", out)
	}
}

// TestSVGBlock_FenceOverTrigger_InsideRealSVG pins the documented finding's over-trigger
// direction: a line inside a real, legitimate multi-line <svg> that happens to begin with
// ``` stops the lookahead there (the wall goes up even though this isn't a real markdown
// fence), so the whole figure fails closed and degrades to exactly pre-fix rendering —
// the blank line before the fence-looking line already ends the raw HTML block via
// goldmark's own stock HTMLBlockParser, and the fence-looking line plus the rest becomes
// an ordinary paragraph (a backtick run becomes an inline <code> span, and the runaway
// `<text>`/`</svg>` sit in that same paragraph as raw inline HTML) — safe precisely
// because it is no worse than what shipped before this file existed.
func TestSVGBlock_FenceOverTrigger_InsideRealSVG(t *testing.T) {
	src := "<svg width=\"10\" height=\"10\">\n" +
		"  <rect/>\n" +
		"\n" +
		"  ```not really a fence, just svg text``` <text>hi</text>\n" +
		"</svg>\n" +
		"\n" +
		"More prose after.\n"
	const wantGolden = "<svg width=\"10\" height=\"10\">\n" +
		"  <rect/>\n" +
		"<p><code>not really a fence, just svg text</code> <text>hi</text>\n" +
		"</svg></p>\n" +
		"<p>More prose after.</p>\n"
	out := mustRender(t, src)
	if out != wantGolden {
		t.Fatalf("fence-over-trigger-inside-real-svg regressed.\ngot:\n%q\nwant:\n%q", out, wantGolden)
	}
}

// --- aihub#262 review round-3 regression tests -----------------------------------------

// buildBudgetExhaustionDocument returns n independently-unclosed top-level <svg> openers
// (each a minimal "<svg>\n\n" — deliberately tiny so the exhaustion boundary shows up at
// a small, easily-tested n) followed by one well-formed, minimal multi-line <svg> figure
// ("<svg>\n<rect/>\n\n<text>t</text>\n</svg>\n") — the shape pinning the [warning] W3
// budget-exhaustion threshold documented in the package comment's "Performance" section.
//
// Each unclosed opener's own lookahead scans all the way to real EOF (there is no
// balancing </svg> anywhere), consuming real tokenizer steps charged against the shared,
// per-parse budget; the trailing figure is well-formed and would be fixed if any budget
// remains when its own Open() call runs. The reviewer's own measurement of a shape like
// this found the exhaustion boundary at 64 unclosed openers in a 559-byte document; this
// file's slightly different opener/figure text puts the boundary between 48 (still
// fixed) and 56 (already degraded) openers — consistent with, and safely inside, the
// guarantee: a single scan's step count can never exceed the number of bytes it scans, so
// K (=8) full-document-worth of scanning must be spent before the shared K*len(source)
// budget can be exhausted, which requires at least K = 8 unclosed top-level <svg>
// openers — an already-broken document — before the guarantee even permits degradation.
func buildBudgetExhaustionDocument(n int) string {
	var sb strings.Builder
	for i := 0; i < n; i++ {
		sb.WriteString("<svg>\n\n")
	}
	sb.WriteString("<svg>\n<rect/>\n\n<text>t</text>\n</svg>\n")
	return sb.String()
}

// TestSVGBlock_BudgetExhaustionThreshold pins the [warning] W3 finding: below the
// guaranteed threshold (K = 8 unclosed openers, per the package comment), a well-formed
// figure downstream of any number of earlier, unrelated, unclosed <svg> openers up to
// that point must still be fixed; only once a document already has well more than that
// many broken openers can the shared per-parse budget be exhausted, degrading the good
// figure to exactly the pre-fix rect=1/text=0 signature — never worse, and never below
// the documented guarantee.
func TestSVGBlock_BudgetExhaustionThreshold(t *testing.T) {
	// Below (and comfortably above) the K=8 floor: the good figure must always survive.
	for _, n := range []int{1, 8, 16, 32} {
		src := buildBudgetExhaustionDocument(n)
		out := mustRender(t, src)
		got := countSVG(t, out)
		if got.rectInSVG != 1 || got.textInSVG != 1 {
			t.Fatalf("n=%d unclosed openers: good figure was NOT fixed (counts = %+v), want rectInSVG=1 textInSVG=1 — this is at or above the K=8 guaranteed-safe floor and must never degrade\nrendered:\n%s", n, got, out)
		}
	}

	// Past this document's own exhaustion boundary (see buildBudgetExhaustionDocument's
	// doc comment: between 48 and 56 openers here) — n=64 matches the reviewer's own
	// measurement and lands safely past it — the good figure must show the EXACT pre-fix
	// degraded signature (rect=1, text=0), never worse than pre-fix rendering would have
	// been.
	const n = 64
	src := buildBudgetExhaustionDocument(n)
	out := mustRender(t, src)
	got := countSVG(t, out)
	if got.rectInSVG != 1 || got.textInSVG != 0 {
		t.Fatalf("n=%d unclosed openers: expected the good figure to show the pre-fix degraded signature (rectInSVG=1, textInSVG=0), got %+v\nrendered:\n%s", n, got, out)
	}
}
