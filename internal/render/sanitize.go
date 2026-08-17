package render

// Stored-XSS sanitizer for agent-authored artifact HTML (aihub#240, resolves #144).
//
// Why this exists: render.Markdown runs goldmark with html.WithUnsafe(), which passes
// raw HTML and inline SVG straight through. That was justified by an assumption written
// into markdown.go's package doc — "artifact author == artifact reader, so XSS is not in
// scope" — which aihub#144 refuted: authed /ui and /v1 served that output with no CSP at
// all, while only the anonymous /share path was locked down. This package is the
// server-side half of the fix.
//
// This is one layer of three. The sandboxed iframe (safeembed.go) and the CSP response
// header are independent and neither is assumed here: per
// 01-static-html-render-engine-research.md §2.3, a client-side sanitizer can be bypassed
// or lag its CVE feed, so the server validates too.
//
// Over-sanitizing is treated as a defect, not as a safe default. A policy that silently
// eats gradients, filters or clip paths breaks every complex diagram the P0 spike exists
// to prove out, so the corpus in test/render/fixtures/xss_payloads.json asserts in both
// directions.

import (
	"regexp"
	"strings"

	"github.com/microcosm-cc/bluemonday"
)

// SanitizeArtifactHTML makes agent-authored HTML safe to embed in an authed artifact
// response. It is safe for concurrent use and idempotent: sanitizing already-sanitized
// output returns it unchanged.
func SanitizeArtifactHTML(in string) string {
	if in == "" {
		return ""
	}
	// 1. Strip the XML doctype / entity declarations before anything parses them.
	//    SVG is XML, so an artifact can carry an internal DTD subset and reach for
	//    XXE (file:///etc/passwd) or entity expansion (billion laughs). goldmark and
	//    the HTML tokenizer do not expand entities, but the sanitized bytes are also
	//    stored and may later be served as image/svg+xml or parsed by an XML reader,
	//    so the declarations must not survive the write path at all.
	in = stripDoctypeAndEntities(in)

	// 2. Sanitize. <style> is not allowlisted, and bluemonday additionally skips its
	//    *content*, so a stylesheet in agent-authored HTML is dropped whole. That is a
	//    deliberate capability reduction — see the note below on why the previous
	//    lift-and-splice design was removed rather than repaired.
	out := artifactPolicy.Sanitize(in)

	// 3. Restore canonical SVG camelCase. The tokenizer lowercases tag and attribute
	//    names. Browsers repair that for foreign content inside an HTML document, but
	//    XML parsing is case-sensitive — a stored `viewbox`/`lineargradient` would break
	//    the moment the same bytes are served standalone as image/svg+xml.
	out = restoreSVGCase(out)

	return out
}

// ---------------------------------------------------------------------------
// DTD / entity stripping
// ---------------------------------------------------------------------------

var (
	// Matches a doctype with an optional internal subset. The subset is matched as a
	// unit so the first '>' *inside* an <!ENTITY ...> declaration does not terminate
	// the match early and leave "]>" plus the entity payload behind as text.
	reDoctype = regexp.MustCompile(`(?is)<!doctype\s[^>\[]*(\[[^\]]*\])?\s*>`)
	// A bare declaration outside any doctype.
	reEntityDecl = regexp.MustCompile(`(?is)<!entity[^>]*>`)
)

func stripDoctypeAndEntities(s string) string {
	s = reDoctype.ReplaceAllString(s, "")
	s = reEntityDecl.ReplaceAllString(s, "")
	return s
}

// A previous revision also ran `\]\s*>` over the WHOLE document whenever a "<!" appeared
// anywhere in it, as a belt-and-braces sweep for malformed internal subsets. That was a
// net negative: it deleted every "]>" in the document, corrupting ordinary prose (an
// array index like a[0]>b) and, worse, stripping the "]]>" CDATA terminator out of d2's
// generated SVG. reDoctype already consumes a well-formed internal subset as part of its
// match, so the sweep bought nothing that was not already handled and damaged content
// that was never malformed.

// ---------------------------------------------------------------------------
// <style>: dropped, not filtered
// ---------------------------------------------------------------------------

// A <style> element in agent-authored HTML is discarded in full, together with its body.
// Nothing in this package parses CSS.
//
// Two earlier revisions tried to keep stylesheets by lifting each <style> body out,
// filtering it, and splicing it back. Both were bypassed, and the second bypass is why the
// mechanism is gone rather than repaired a third time:
//
//  1. A regex boundary (`<style[^>]*>(.*?)</style>`) missed the real RAWTEXT rule, so
//     `</style\n>` did not terminate the match. Arbitrary markup in between was treated as
//     a CSS body, skipped the policy, and was spliced back verbatim.
//  2. Switching to html.NewTokenizer fixed that boundary and introduced another. The
//     tokenizer sets RAWTEXT for <style> unconditionally; a conformant HTML5 parser does
//     not. Under the rules for parsing tokens in foreign content, `style` is not in the
//     breakout list, so inside <svg> it is a foreign element whose content is parsed as
//     markup — which is precisely why d2 wraps its own stylesheet in <![CDATA[...]]>.
//     `<svg><style><img src=x onerror=alert(1)></style></svg>` therefore lifted a live
//     <img> out as if it were CSS and spliced it back past the policy: a working stored
//     XSS on authed /ui, the defect class aihub#144 exists to close.
//
// The lesson is not "use a better parser". The security property depended on a hand-rolled
// agreement with the browser across two grammars — HTML tokenization and CSS block
// structure — and each repair moved the disagreement instead of removing it. A real CSS
// tokenizer would have closed (2)'s siblings (unbalanced blocks, nested at-rules, `{`
// inside comments and strings — all of which also bypassed the property allowlist) at the
// cost of keeping the lift-and-splice machinery that both bypasses lived in.
//
// bluemonday alone already handles this correctly: `style` is not allowlisted *and* is in
// its skip-content set, so element and body both disappear. The machinery existed only to
// preserve d2's inline stylesheet, and that requirement is met elsewhere now — FreezeDiagram
// and RenderDiagramsForUI emit our own trusted SVG, which is inserted *after* sanitization
// and never passes through this policy (see buildInnerDocument in safeembed.go and the
// ordering note in ui_embed.go). Untrusted content loses <style>; trusted diagram output
// keeps it.
//
// Cost, stated plainly: an agent cannot ship a stylesheet alongside its document. It can
// still use the style="" attribute, which is property- and value-restricted below.

// styleAttrProps is the property vocabulary permitted in inline style="" attributes.
//
// Absent by design and worth naming, because their absence is load-bearing:
// position, z-index, top/left/right/bottom, inset, pointer-events, content and
// clip. Together those are what turn a stylesheet into a UI-redress primitive —
// a fixed, top-layer element covering the viewer with attacker-chosen content.
//
// The line is placement vs presentation, but it is not perfectly clean: `transform` is
// present and can translate an element far from its box. What that cannot do is leave the
// document's flow — without position/z-index it scrolls with the content and cannot
// guarantee painting above positioned chrome — so it is a much weaker primitive than the
// fixed full-page overlay this list exists to prevent, and on the sandboxed {{md}} surfaces
// the frame contains it entirely. Stated rather than claiming presentation properties are
// categorically harmless, which is what an earlier version of this comment implied.
//
// This is now the only CSS vocabulary in the package, and it is enforced by bluemonday's
// own AllowStyles handling rather than by anything hand-rolled here. The <style>-element
// list it used to be shared with is gone, so there is no longer a second vocabulary that
// can drift out of step with this one.
var styleAttrProps = []string{
	"fill", "fill-opacity", "fill-rule",
	"stroke", "stroke-width", "stroke-opacity", "stroke-linecap", "stroke-linejoin",
	"stroke-dasharray", "stroke-dashoffset",
	"opacity", "color", "background-color",
	"font", "font-size", "font-family", "font-weight", "font-style",
	"text-anchor", "text-align", "letter-spacing", "word-spacing", "line-height",
	"width", "height", "max-width", "max-height", "min-width", "min-height",
	"margin", "padding", "border", "border-radius", "border-collapse",
	"display", "visibility", "overflow", "vertical-align", "white-space",
	"transform", "transform-origin", "shape-rendering", "vector-effect", "paint-order",
	"dominant-baseline", "alignment-baseline", "mix-blend-mode", "isolation",
}

// ---------------------------------------------------------------------------
// Policy
// ---------------------------------------------------------------------------

var (
	// Anchors may navigate; they may not carry script or inline documents. The
	// sandboxed iframe has no allow-top-navigation, so these are additionally inert
	// against frame-busting.
	reSafeLinkURL = regexp.MustCompile(`(?i)^(?:https?://[^\s"']+|mailto:[^\s"']+|/[^\s"']*|#[^\s"']*)$`)
	// Images must not reach the network: an artifact-embedded external image URL is a
	// pixel-grade read receipt on a private document (01 §2.6). Inline data: or a
	// same-document fragment only.
	//
	// This used to also admit `/[^\s"']*` for "root-relative, therefore first-party". That
	// branch made the sentence above false in two ways, and the second was a real bypass:
	//
	//   1. A root-relative URL still performs a network fetch. Same-origin, but the request
	//      happens, so "must not reach the network" was aspirational rather than enforced.
	//   2. `//evil.example/px.gif` matches `/[^\s"']*` — one slash, then more characters —
	//      and a protocol-relative URL resolves against the PAGE's scheme, i.e. straight to
	//      an external host. `<img src="//evil.example/px.gif">` survived verbatim, which is
	//      exactly the read receipt this rule exists to prevent.
	//
	// Dropping the branch closes (2) and makes (1) true. Nothing in the repo depended on it:
	// the first-party <script src="/ui/static/...">/<link> tags on the viewer are injected
	// AFTER sanitization and never pass through this policy.
	reSafeImageURL = regexp.MustCompile(`(?i)^(?:data:image/(?:png|jpeg|gif|webp|svg\+xml);base64,[a-z0-9+/=]+|#[^\s"']*)$`)
	// Paint values: a literal colour, or a reference to a def in this same document.
	// Named colours are matched as a bare word, which is why "burlywood" cannot be
	// confused with a url( ... ) form.
	rePaint = regexp.MustCompile(`(?i)^(?:none|transparent|currentcolor|inherit|#[0-9a-f]{3,8}|rgba?\([^)]*\)|hsla?\([^)]*\)|[a-z]+|url\(#[a-z0-9_:.\-]+\))$`)
	// Reference-only values.
	reLocalRef = regexp.MustCompile(`(?i)^(?:none|inherit|url\(#[a-z0-9_:.\-]+\))$`)
	// Inline style declaration values. See the AllowStyles call for why ':' and '/'
	// are excluded rather than blacklisting specific schemes.
	reStyleValue = regexp.MustCompile(`^[a-zA-Z0-9#%.,()\s_-]+$`)
)

var artifactPolicy = buildArtifactPolicy()

func buildArtifactPolicy() *bluemonday.Policy {
	p := bluemonday.NewPolicy()

	// --- prose structure produced by goldmark ---
	p.AllowElements(
		"p", "div", "span", "br", "hr",
		"h1", "h2", "h3", "h4", "h5", "h6",
		"ul", "ol", "li", "dl", "dt", "dd",
		"blockquote", "pre", "code", "kbd", "samp", "var",
		"em", "strong", "i", "b", "u", "s", "del", "ins", "sub", "sup", "small", "mark", "abbr", "time",
		"table", "thead", "tbody", "tfoot", "tr", "th", "td", "caption", "colgroup", "col",
		"section", "article", "header", "footer", "main", "aside", "nav",
		"figure", "figcaption", "details", "summary",
	)
	p.AllowAttrs("class", "id", "title", "lang", "dir", "role").Globally()
	p.AllowAttrs("colspan", "rowspan", "scope", "headers").OnElements("th", "td")
	p.AllowAttrs("span").OnElements("col", "colgroup")
	p.AllowAttrs("start", "reversed", "type").OnElements("ol")
	p.AllowAttrs("value").OnElements("li")
	p.AllowAttrs("open").OnElements("details")
	p.AllowAttrs("datetime").OnElements("time")
	p.AllowAttrs("cite").Matching(reSafeLinkURL).OnElements("blockquote", "del", "ins")
	// aria-* is presentation metadata with no script surface and matters for the
	// annotation layer's accessibility.
	p.AllowAttrs("aria-label", "aria-labelledby", "aria-describedby", "aria-hidden").Globally()

	p.AllowAttrs("href").Matching(reSafeLinkURL).OnElements("a")
	p.AllowAttrs("target", "rel").OnElements("a")
	p.AllowAttrs("src").Matching(reSafeImageURL).OnElements("img")
	p.AllowAttrs("alt", "width", "height", "loading", "decoding").OnElements("img")

	// --- SVG ---
	// Structure, shapes, text, paint servers, clipping and the filter primitives that
	// a dense architecture diagram actually uses (gradients, blur, drop shadows).
	svgElements := []string{
		"svg", "g", "defs", "symbol", "use", "marker", "switch", "title", "desc", "metadata",
		"path", "rect", "circle", "ellipse", "line", "polyline", "polygon",
		"text", "tspan", "textpath",
		"lineargradient", "radialgradient", "stop", "pattern",
		"clippath", "mask", "filter",
		"fegaussianblur", "feoffset", "femerge", "femergenode", "feblend", "fecolormatrix",
		"fecomposite", "feflood", "fedropshadow", "femorphology", "feturbulence",
		"fedisplacementmap", "fecomponenttransfer", "fefuncr", "fefuncg", "fefuncb", "fefunca",
		"image",
	}
	p.AllowElements(svgElements...)

	// Structural SVG elements carry no attributes of their own, and bluemonday drops any
	// element with no surviving attributes unless it is registered here.
	//
	// This is not cosmetic. <feMerge> is a pure container: strip it and its <feMergeNode>
	// children are left dangling, so the filter silently stops compositing — the figure
	// still renders, just wrong. <defs> is the same shape of problem for gradient and
	// filter definitions. Every count-based check over attributed elements
	// (linearGradient, feDropShadow, clipPath) stays green while this happens, which is
	// how it survived until a figure dense enough to use feMerge went through.
	p.AllowNoAttrs().OnElements(append(append([]string{}, svgElements...),
		"p", "div", "span", "em", "strong", "i", "b", "u", "s", "del", "ins",
		"ul", "ol", "li", "dl", "dt", "dd", "blockquote", "pre", "code",
		"table", "thead", "tbody", "tfoot", "tr", "th", "td", "caption",
		"figure", "figcaption", "section", "article", "header", "footer", "main",
		"h1", "h2", "h3", "h4", "h5", "h6", "sub", "sup", "small", "mark", "br", "hr",
	)...)

	// Geometry and typography carry no script surface.
	p.AllowAttrs(
		"x", "y", "x1", "y1", "x2", "y2", "cx", "cy", "r", "rx", "ry",
		"width", "height", "d", "points", "dx", "dy", "rotate",
		"transform", "gradienttransform", "patterntransform",
		"viewbox", "preserveaspectratio", "xmlns", "xmlns:xlink", "version",
		"offset", "spreadmethod", "gradientunits", "patternunits", "patterncontentunits",
		"clippathunits", "maskunits", "maskcontentunits", "primitiveunits", "filterunits",
		"markerwidth", "markerheight", "refx", "refy", "orient", "markerunits",
		"opacity", "fill-opacity", "stroke-opacity", "stop-opacity", "flood-opacity",
		"stroke-width", "stroke-linecap", "stroke-linejoin", "stroke-dasharray",
		"stroke-dashoffset", "stroke-miterlimit", "fill-rule", "clip-rule",
		"font-size", "font-family", "font-weight", "font-style", "letter-spacing",
		"word-spacing", "text-anchor", "dominant-baseline", "alignment-baseline",
		"textlength", "lengthadjust", "startoffset", "pathlength",
		"stddeviation", "in", "in2", "result", "mode", "operator", "values", "type",
		"tablevalues", "slope", "intercept", "amplitude", "exponent",
		"basefrequency", "numoctaves", "seed", "scale", "xchannelselector", "ychannelselector",
		"k1", "k2", "k3", "k4", "surfacescale", "specularconstant", "specularexponent",
		"diffuseconstant", "kernelmatrix", "order", "radius", "shape-rendering",
		"vector-effect", "paint-order", "overflow", "visibility", "display",
	).OnElements(svgElements...)

	// Paint and reference attributes are value-restricted: a colour or a same-document
	// reference, never an outbound url().
	p.AllowAttrs("fill", "stroke", "stop-color", "flood-color", "lighting-color", "color").
		Matching(rePaint).OnElements(svgElements...)
	p.AllowAttrs("filter", "clip-path", "mask", "marker-start", "marker-mid", "marker-end").
		Matching(reLocalRef).OnElements(svgElements...)

	// <use> and <image> resolve references; keep them same-document / inline only.
	p.AllowAttrs("href", "xlink:href").Matching(reSafeImageURL).OnElements("image")
	p.AllowAttrs("href", "xlink:href").Matching(regexp.MustCompile(`^#[A-Za-z0-9_:.\-]+$`)).
		OnElements("use", "textpath", "pattern", "lineargradient", "radialgradient")
	p.AllowAttrs("href", "xlink:href").Matching(reSafeLinkURL).OnElements("a")

	// The style="" attribute, restricted to a property whitelist. bluemonday validates
	// each declaration against AllowStyles below and drops the rest, which gives the
	// same guarantee the <style> block guard gives without hand-rolling a CSS parser.
	//
	// This was excluded in the first cut on the reasoning that d2 themes via presentation
	// attributes and its embedded stylesheet, so nothing needed it. Measuring rather than
	// assuming showed d2 emits six inline style="" attributes, and agent-authored complex
	// SVG is free to use many more — dropping them silently distorts exactly the dense
	// figures acceptance criterion 4 exists to protect.
	styleHosts := append(append([]string{}, svgElements...), "p", "div", "span", "td", "th", "figure", "figcaption")
	p.AllowAttrs("style").OnElements(styleHosts...)
	// Matching, because bluemonday validates values with per-property handlers and ships
	// none for SVG presentation properties — stroke-width et al. would be dropped as
	// unrecognised even though the property name is whitelisted.
	//
	// The value grammar excludes ':' and '/', which is what makes it safe: every external
	// reference form needs one or both (http://…, //host, data:, javascript:), while every
	// value we want keeps working — #rrggbb, rgba(0,0,0,.5), 2px, bold, sans-serif, and
	// the same-document url(#gradient) reference.
	//
	// Known, accepted residue: the grammar admits any function call, so
	// style="width:expression(alert(1))" survives as text. IE's CSS expression() has not
	// executed in any browser since IE10, and inside the sandboxed frame script-src is
	// 'none' regardless. Recorded so it reads as a decision rather than an oversight.
	p.AllowStyles(styleAttrProps...).Matching(reStyleValue).OnElements(styleHosts...)

	// Deliberately NOT allowed, each for a stated reason:
	//   <script>, <foreignObject>, <animate*>, <set>  — script or script-adjacent.
	//   on* handlers                                   — never whitelisted, so dropped.
	//   <iframe>, <object>, <embed>, <form>, <input>   — nested browsing contexts and
	//       credential-shaped UI have no place in a rendered document.
	//   position/z-index/top/left in style              — absent from AllowStyles, so a
	//       document cannot lift content out of its box to overlay the parent UI.

	p.AddSpaceWhenStrippingTag(true)
	return p
}

// ---------------------------------------------------------------------------
// SVG case restoration
// ---------------------------------------------------------------------------

// svgCanonicalNames maps the lowercased form emitted by the HTML tokenizer back to the
// case-sensitive spelling SVG/XML requires.
var svgCanonicalNames = map[string]string{
	// elements
	"lineargradient": "linearGradient", "radialgradient": "radialGradient",
	"clippath": "clipPath", "textpath": "textPath",
	"fegaussianblur": "feGaussianBlur", "feoffset": "feOffset",
	"femerge": "feMerge", "femergenode": "feMergeNode", "feblend": "feBlend",
	"fecolormatrix": "feColorMatrix", "fecomposite": "feComposite",
	"feflood": "feFlood", "fedropshadow": "feDropShadow",
	"femorphology": "feMorphology", "feturbulence": "feTurbulence",
	"fedisplacementmap": "feDisplacementMap", "fecomponenttransfer": "feComponentTransfer",
	"fefuncr": "feFuncR", "fefuncg": "feFuncG", "fefuncb": "feFuncB", "fefunca": "feFuncA",
	// attributes
	"viewbox": "viewBox", "preserveaspectratio": "preserveAspectRatio",
	"gradienttransform": "gradientTransform", "gradientunits": "gradientUnits",
	"patterntransform": "patternTransform", "patternunits": "patternUnits",
	"patterncontentunits": "patternContentUnits", "clippathunits": "clipPathUnits",
	"maskunits": "maskUnits", "maskcontentunits": "maskContentUnits",
	"primitiveunits": "primitiveUnits", "filterunits": "filterUnits",
	"markerwidth": "markerWidth", "markerheight": "markerHeight",
	"markerunits": "markerUnits", "refx": "refX", "refy": "refY",
	"spreadmethod": "spreadMethod", "startoffset": "startOffset",
	"textlength": "textLength", "lengthadjust": "lengthAdjust",
	"pathlength": "pathLength", "stddeviation": "stdDeviation",
	"basefrequency": "baseFrequency", "numoctaves": "numOctaves",
	"xchannelselector": "xChannelSelector", "ychannelselector": "yChannelSelector",
	"tablevalues": "tableValues", "kernelmatrix": "kernelMatrix",
	"surfacescale": "surfaceScale", "specularconstant": "specularConstant",
	"specularexponent": "specularExponent", "diffuseconstant": "diffuseConstant",
	"xlink:href": "xlink:href",
}

var reNameToken = regexp.MustCompile(`(?i)(</?)([a-z][a-z0-9:_-]*)|(\s)([a-z][a-z0-9:_-]*)(=)`)

func restoreSVGCase(s string) string {
	if !strings.Contains(s, "<svg") && !strings.Contains(s, "<SVG") {
		return s
	}
	return reNameToken.ReplaceAllStringFunc(s, func(m string) string {
		sub := reNameToken.FindStringSubmatch(m)
		// tag name form: sub[1]="<" or "</", sub[2]=name
		if sub[2] != "" {
			if canon, ok := svgCanonicalNames[strings.ToLower(sub[2])]; ok {
				return sub[1] + canon
			}
			return m
		}
		// attribute form: sub[3]=space, sub[4]=name, sub[5]="="
		if canon, ok := svgCanonicalNames[strings.ToLower(sub[4])]; ok {
			return sub[3] + canon + sub[5]
		}
		return m
	})
}
