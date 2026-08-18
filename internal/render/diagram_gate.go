package render

// Gated d2 compilation (aihub#240).
//
// # The boundary this file exists to name
//
// A rendered /ui document is assembled from several sources, and only some of them have been
// through SanitizeArtifactHTML. Everything appended after that point is described in the code
// as "first-party", which is true of the CODE in every case and true of the DATA in all but
// one. The test that matters is the second one:
//
//	insertion point                     data it carries                          discipline
//	──────────────────────────────────  ───────────────────────────────────────  ─────────────────
//	uiHead (theme setter, asset tags)   theme from a cookie                      closed vocabulary
//	                                                                             (light|dark|auto)
//	share control                       shareURL built from the Host header      html.EscapeString
//	annotation chrome                   memory id, commit bodies, authors,       html.EscapeString
//	                                    quotes, heading text                     per field; the JSON
//	                                                                             island via
//	                                                                             escapeJSONForScriptTag
//	compiled d2 figures                 the agent's fence body                   ← NOTHING, until
//	                                                                               this file
//
// The first three validate or escape their data before it becomes markup. The fourth passed
// agent-authored text to a compiler and inserted the compiler's output verbatim, on the
// reasoning that d2 is our own in-process engine and its output is therefore ours. That
// reasoning conflates authorship with trust: RenderDiagramsForUI is first-party CODE over
// untrusted INPUT, and d2 has features that emit caller-supplied text as raw markup.
//
// # What went wrong without it
//
// Three d2 features reach through:
//
//	x: |md <script>alert(1)</script> |     → <foreignObject><div xmlns="…xhtml"><script>…
//	x.link: "javascript:alert(1)"          → <a href="javascript:alert(1)" xlink:href="…">
//	x.icon: https://evil.example/px.png    → <image href="https://evil.example/px.png">
//
// <foreignObject> is an HTML integration point, so a <script> inside it is a real HTML script
// element — and it is on SanitizeArtifactHTML's explicit deny list, which is exactly the point:
// the sanitizer would have removed it, and the figure is inserted after the sanitizer has run.
// On the artifact viewer, where the document is inlined into the page rather than framed, the
// page policy used to permit it outright: script-src carried 'unsafe-inline'. aihub#243 has
// since replaced that with a per-response nonce, so a forged inline script can no longer name
// a value it is allowed to run under — but this gate stays, and stays load-bearing. It is what
// stops the injection reaching the bytes at all, and the same channel still forges non-script
// elements the viewer's own chrome looks up by id, which no script policy governs.
//
// # Why a gate rather than sanitizing the output
//
// Running SanitizeArtifactHTML over the compiled SVG is the obvious move and is wrong: that
// policy drops <style>, and d2 keeps every fill, stroke and its embedded webfont there, so the
// figure would arrive structurally perfect and unpainted. That is the trade D2/D6 were decided
// around and it is not being reopened. Instead this file refuses input that can produce markup,
// and refuses output that contains markup — belt and braces, both failing closed to the
// original code block, which is the same degradation RenderDiagramsForUI already applies to a
// diagram that will not compile.

import (
	"html"
	"regexp"
	"strconv"
	"strings"
)

// Markup-emitting d2 features. Each is refused before layout runs.
//
// Matched on the DSL source, which is why the patterns are anchored to d2 syntax rather than to
// the payload: what makes a fence dangerous is that it asks d2 for a markup-producing feature
// at all, not what the agent put inside it. A source asking for `|md` is rejected even if its
// body looks harmless today, because "harmless markdown" is still markdown rendered into
// <foreignObject> and the set of constructs markdown can emit is not ours to enumerate.
var d2MarkupFeatures = []struct {
	name string
	re   *regexp.Regexp
}{
	// Block string with a markup-producing language tag: |md …|, |latex …|, |html …|, and the
	// ||/||| fence variants d2 also accepts. The tag may be followed by whitespace or a newline.
	{"markdown/latex/html block", regexp.MustCompile(`(?i)\|{1,3}\s*(md|markdown|latex|tex|html)\b`)},
	// A node link becomes <a href> around the shape, and d2 does not constrain the scheme.
	//
	// Anchored to attribute POSITIONS — start of a line, or after `.`/`{` — not to the bare
	// keyword. `x: see the link: below` is a label whose text happens to contain the word, and a
	// gate that refuses it is a gate that refuses ordinary prose; the first version of this
	// pattern did exactly that.
	{"link attribute", regexp.MustCompile(`(?im)(^[ \t]*|[.{][ \t]*)link[ \t]*:`)},
	// An icon becomes <image href> pointing wherever it says.
	{"icon attribute", regexp.MustCompile(`(?im)(^[ \t]*|[.{][ \t]*)icon[ \t]*:`)},
}

// Constructs that must not appear in compiled output. This is the second belt: it catches a d2
// feature we did not enumerate, or a future d2 version emitting markup from something that
// looks inert today.
//
// Deliberately NOT a general HTML policy — see the file header. It is a short, closed list of
// the things that turn an SVG into an execution or navigation surface.
var d2ForbiddenInOutput = []struct {
	name    string
	pattern string
}{
	{"HTML integration point", "<foreignobject"},
	{"script element", "<script"},
	{"javascript: URL", "javascript:"},
}

// reExternalRef matches an href/xlink:href/src whose value leaves the document. d2's own output
// references its defs with url(#…) and embeds fonts as data:, so a scheme or protocol-relative
// host is not something a legitimate figure needs.
var reExternalRef = regexp.MustCompile(`(?i)(?:xlink:)?(?:href|src)\s*=\s*["'](?:[a-z][a-z0-9+.-]*:|//)`)

// RenderDiagramsGated is RenderDiagramsForUI with the trust boundary enforced.
//
// Production code must call this, never RenderDiagramsForUI directly. The unqualified function
// remains exported because /v1 and /share must keep their frozen bytes and neither calls it, and
// because rewriting diagram.go is out of scope for this spike (it is declared read-only).
// TestNoProductionCallerUsesUngatedDiagramRendering pins the distinction.
func RenderDiagramsGated(h string) string {
	const open = `<pre><code class="language-d2">`
	const closeTag = `</code></pre>`
	if !strings.Contains(h, open) {
		return h
	}

	var b strings.Builder
	b.Grow(len(h) + 1024)
	rest := h
	for {
		i := strings.Index(rest, open)
		if i < 0 {
			b.WriteString(rest)
			break
		}
		b.WriteString(rest[:i])
		rest = rest[i+len(open):]
		j := strings.Index(rest, closeTag)
		if j < 0 {
			// Malformed input: emit verbatim and stop, matching RenderDiagramsForUI.
			b.WriteString(open)
			b.WriteString(rest)
			break
		}
		srcEscaped := rest[:j]
		rest = rest[j+len(closeTag):]

		keepAsCode := func() {
			b.WriteString(open)
			b.WriteString(srcEscaped)
			b.WriteString(closeTag)
		}

		// Belt 1: refuse the source.
		//
		// The source is compared in its UNESCAPED form, which is the form d2 receives —
		// diagram.go unescapes before compiling, so a check against the escaped text would miss
		// `&#124;md` and anything else that only becomes syntax after unescaping.
		src := unescapeForD2(srcEscaped)
		if d2SourceRejects(src) != "" {
			keepAsCode()
			continue
		}

		svg, err := RenderDiagram(src)
		if err != nil || !strings.Contains(svg, "<svg") {
			keepAsCode()
			continue
		}

		// Belt 2: refuse the output.
		if d2OutputRejects(svg) != "" {
			keepAsCode()
			continue
		}

		// A graph laid out `direction: right` can come out far wider than the column, and
		// scaling it to fit is what makes labels unreadable. Re-run the layout vertically and
		// take it if it is narrower: extending downwards costs scrolling the page, which the
		// reader is already doing, where extending sideways costs a second scroll axis inside
		// the document. The layout engine positioning the graph is the whole point of the
		// deterministic-layout half of the architecture; asking it for a different aspect is
		// using it, not overriding the author.
		svg = narrowerLayout(src, svg)

		b.WriteString(`<figure class="pf-diagram">`)
		b.WriteString(svg)
		b.WriteString(`</figure>`)
	}
	return b.String()
}

// d2SourceRejects returns the name of the markup-emitting feature the source asks for, or "".
func d2SourceRejects(src string) string {
	for _, f := range d2MarkupFeatures {
		if f.re.MatchString(src) {
			return f.name
		}
	}
	return ""
}

// d2OutputRejects returns why the compiled SVG is unacceptable, or "".
func d2OutputRejects(svg string) string {
	low := strings.ToLower(svg)
	for _, f := range d2ForbiddenInOutput {
		if strings.Contains(low, f.pattern) {
			return f.name
		}
	}
	if m := reExternalRef.FindString(svg); m != "" {
		// d2's own font payloads are data: URLs inside <style>, not href/src attributes, so an
		// external-looking reference in an attribute is never something a legitimate figure needs.
		if !strings.HasPrefix(strings.ToLower(strings.TrimSpace(m)), "xlink:href=\"data:") &&
			!strings.Contains(strings.ToLower(m), "\"data:") {
			return "external reference " + m
		}
	}
	return ""
}

// unescapeForD2 mirrors what diagram.go does to a fence body before handing it to the compiler
// (html.UnescapeString). Kept as its own function so the gate and the compiler cannot drift on
// what "the source" means: if diagram.go ever changes how it decodes a fence, this is the one
// place that has to follow, and the gate checking a different string than the compiler receives
// is precisely how a bypass would reappear.
func unescapeForD2(escaped string) string {
	return html.UnescapeString(escaped)
}

// reSVGWidth reads the outer <svg>'s intrinsic width in px. d2 emits a bare number
// (width="4295"), so no unit parsing is needed; anything else is treated as unknown.
var reSVGWidth = regexp.MustCompile(`<svg[^>]*\bwidth="([0-9]+(?:\.[0-9]+)?)"`)

// reTopLevelDirection matches a `direction:` declaration at column 0 — the one that governs the
// root graph. Indented ones belong to a container and are deliberately left alone: rewriting
// those would rearrange the insides of a group the author shaped on purpose.
var reTopLevelDirection = regexp.MustCompile(`(?m)^direction[ \t]*:[ \t]*[A-Za-z]+[ \t]*\r?\n?`)

// wideFigurePx is the intrinsic width past which scaling a figure to the column destroys it.
//
// Measured, not guessed: the /ui document column is ~800px. d2 lays a graph out at its natural
// size and the frame's `svg{max-width:100%}` scales it to fit, so effective text size is
// authored-size × column/intrinsic. The spike's own `direction: right` pipeline figure comes out
// 4295×397 — a 10.8:1 ribbon that scales to 18.7%, turning diagram.go's deliberately bumped
// 24px labels into 4.5px. It is present, undistorted, and unreadable.
//
// 1400px is where that ratio reaches ~0.57 and labels drop under ~14px.
const wideFigurePx = 1400

// svgIntrinsicWidth returns the outer <svg>'s width in px, or 0 when it does not declare one.
func svgIntrinsicWidth(svg string) float64 {
	m := reSVGWidth.FindStringSubmatch(svg)
	if m == nil {
		return 0
	}
	w, err := strconv.ParseFloat(m[1], 64)
	if err != nil {
		return 0
	}
	return w
}

// narrowerLayout returns a vertically laid-out rendering of src when the original is too wide to
// stay legible in the column, or the original otherwise.
//
// Deliberately NOT a horizontal scrollbar. A figure that scrolls sideways inside a document that
// scrolls downwards hides content behind an axis readers do not expect, and it hides it in the
// one place — a diagram — where seeing the whole shape at once is the point.
//
// The rewrite only touches a column-0 `direction:`. The re-layout is a fresh compile of a source
// that already cleared belt 1, and its output is put through belt 2 again rather than trusted for
// having come from an approved source: the gate's contract is that nothing reaches the page
// without both belts, and "we compiled it ourselves" is exactly the reasoning round 5 falsified.
//
// Falls back to the original whenever the alternative fails to compile, is not actually narrower,
// or fails belt 2. A figure that is still too wide after this scales to fit — degraded, but with
// no second scroll axis.
func narrowerLayout(src, svg string) string {
	w := svgIntrinsicWidth(svg)
	if w <= wideFigurePx {
		return svg
	}
	vertical := "direction: down\n" + reTopLevelDirection.ReplaceAllString(src, "")
	alt, err := RenderDiagram(vertical)
	if err != nil || !strings.Contains(alt, "<svg") {
		return svg
	}
	if aw := svgIntrinsicWidth(alt); aw == 0 || aw >= w {
		return svg
	}
	if d2OutputRejects(alt) != "" {
		return svg
	}
	return alt
}
