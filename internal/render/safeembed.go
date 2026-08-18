package render

// Sandboxed embedding for agent-authored artifact HTML (aihub#240).
//
// This is the isolation layer of the three-step render architecture: the agent produces
// a finished HTML document, a subagent checks it against the markdown twin, and this
// file is what finally displays it — isolated, so untrusted rich content cannot reach
// the parent page.
//
// Chosen per 01-static-html-render-engine-research.md §3: a sandboxed iframe scores
// highest on isolation while natively supporting inline SVG and controlled interaction.
// Shadow DOM + DOMPurify was the runner-up and was rejected because its failure mode is
// categorically worse — a sanitizer miss there executes in the parent's origin, whereas
// a miss here is confined to an opaque origin.

import (
	"crypto/rand"
	_ "embed"
	"encoding/base64"
	"encoding/json"
	"html"
	"strings"
)

// annotationBridgeJS is first-party, security-reviewed code that runs inside the sandbox
// so annotation does not have to cost isolation. See annotation-bridge.js for the
// protocol and the reasoning behind quote-based anchoring.
//
//go:embed annotation-bridge.js
var annotationBridgeJS string

// AnnotationBridgeJS returns the bridge source unmodified, for callers that supply their
// own configuration prologue.
func AnnotationBridgeJS() string { return annotationBridgeJS }

// AnnotationBridgeFor returns the bridge with its configuration prologue bound to
// parentOrigin — the exact origin the frame is allowed to talk to.
//
// The frame is sandboxed without allow-same-origin, so it has an opaque origin and
// cannot discover the parent's on its own; the server is the only party that knows it.
// Passing it explicitly is what lets the bridge use a precise targetOrigin instead of
// '*', which would broadcast selected document text to any listening window.
//
// parentOrigin is JSON-encoded rather than concatenated: it reaches this function from
// request-derived data (scheme + Host), and Host is attacker-influenced, so splicing it
// raw into a script body would be a script-injection sink in our own trusted code.
func AnnotationBridgeFor(parentOrigin string) string {
	cfg, err := json.Marshal(map[string]string{"parentOrigin": parentOrigin})
	if err != nil {
		// Cannot happen for a map[string]string; degrade to a bridge that will refuse
		// to post rather than one that posts to '*'.
		cfg = []byte(`{"parentOrigin":""}`)
	}
	return "window.__PF_BRIDGE_CONFIG__=" + string(cfg) + ";\n" + annotationBridgeJS
}

// sandboxTokens is the frame's entire privilege set.
//
// allow-same-origin is absent by construction and must stay absent. Combined with
// allow-scripts it does not merely relax isolation, it voids it: script in the frame
// would run in the parent's origin and could remove the sandbox attribute from its own
// frame element. There is intentionally no option to extend this string.
const sandboxTokens = "allow-scripts"

// EmbedOptions configures one embed. Everything here is server-controlled; nothing in
// it originates from the artifact author.
type EmbedOptions struct {
	// Title is used for the inner document's <title>.
	Title string
	// BridgeScript is trusted, first-party JavaScript (the annotation bridge, aihub#240
	// T4). It is injected into the inner <head> under a CSP nonce. Leave empty for a
	// pure display embed, which then runs no script at all.
	BridgeScript string
	// FrameClass is an optional class on the <iframe> for parent-side layout.
	FrameClass string

	// Theme is the viewer's theme choice — "light", "dark" or "auto" — and is stamped on
	// the inner document's <html data-theme>. It must be supplied by the caller because a
	// sandboxed frame has an opaque origin and cannot read the parent's attribute or
	// cookie. Empty behaves as "auto".
	//
	// The inner stylesheet resolves all three states in CSS alone (see innerBaseCSS), so
	// "auto" paints correctly on first load from prefers-color-scheme with no script and
	// no flash — the same mechanism ui.css uses on the parent.
	Theme string

	// Nonce is the CSP nonce the frame's bridge script runs under, and it must be the
	// SAME per-response nonce the embedding page puts in its own script-src (aihub#243).
	//
	// This is not a preference. `about:srcdoc` is a local scheme, so the frame inherits the
	// embedding document's policy container and is governed by the CONJUNCTION of the page
	// policy and the inner <meta> one. Once the page moved off 'unsafe-inline' to a nonce,
	// a frame that minted its own value would present 'nonce-N' against the page's
	// 'nonce-R': the conjunction admits neither, the bridge never executes, and every
	// embedded document silently stops reporting its height and sits at ui.css's 220px
	// starting size with an inner scrollbar. Nothing errors, nothing logs.
	//
	// Empty means "mint a fresh one", which is correct ONLY for a frame that is not
	// embedded in a nonce-carrying page (tests, and the failsafe path). Callers rendering
	// into a /ui response must pass the page nonce — see uiPageCSPWithNonce.
	Nonce string
}

// themeAttr normalises Theme to one of the three values the stylesheet handles.
func (o EmbedOptions) themeAttr() string {
	switch o.Theme {
	case "light", "dark":
		return o.Theme
	default:
		return "auto"
	}
}

// SafeEmbedDocument sanitizes agentHTML, compiles any d2 fences it contains, wraps the
// result in a self-contained document, and returns the <iframe srcdoc="..."> markup that
// renders it in isolation.
//
// Pass agent-authored HTML straight in — do not sanitize or compile diagrams beforehand.
// Both steps happen here, in the only order that is correct; see buildInnerDocument.
//
// It never panics and never returns markup that could execute agent script in the
// parent document. On any internal failure it degrades to an empty sandboxed frame
// rather than falling back to unisolated rendering.
func SafeEmbedDocument(agentHTML string, opt EmbedOptions) (out string) {
	// A rendering fault must not take down a request that is only displaying a
	// document. The old D2 path had exactly this defect (panic -> 500, no recover);
	// the replacement does not inherit it.
	defer func() {
		if r := recover(); r != nil {
			out = failsafeFrame(opt)
		}
	}()

	nonce := opt.Nonce
	if nonce == "" && opt.BridgeScript != "" {
		nonce = newNonce()
	}

	doc := buildInnerDocument(agentHTML, opt, nonce)

	var b strings.Builder
	b.WriteString(`<iframe`)
	if opt.FrameClass != "" {
		b.WriteString(` class="` + html.EscapeString(opt.FrameClass) + `"`)
	}
	b.WriteString(` sandbox="` + sandboxTokens + `"`)
	b.WriteString(` referrerpolicy="no-referrer"`)
	b.WriteString(` loading="lazy"`)
	b.WriteString(` title="` + html.EscapeString(opt.Title) + `"`)
	// The whole inner document becomes one attribute value. Escaping is what keeps it
	// inside the attribute; without it a quote in the agent's markup would terminate
	// srcdoc early and the remainder would be parsed as parent markup, outside the
	// sandbox entirely.
	b.WriteString(` srcdoc="` + html.EscapeString(doc) + `"`)
	b.WriteString(`></iframe>`)
	return b.String()
}

func buildInnerDocument(agentHTML string, opt EmbedOptions, nonce string) string {
	// Sanitize first, compile diagrams second. This ordering is a correctness requirement,
	// not a preference, and it is internalised here rather than left to the caller because
	// both ways of getting it wrong are silent:
	//
	//   - caller compiles first, then passes the result in: the trusted d2 SVG meets the
	//     sanitizer, which drops its <style> and with it every fill and stroke. The figure
	//     still renders, unpainted.
	//   - caller passes raw markdown-derived HTML and compiles nothing: a d2 fence survives
	//     sanitization as <pre><code class="language-d2">, and nobody ever turns it into a
	//     diagram. The figure silently does not appear.
	//
	// Doing both here leaves the caller nothing to get wrong. A fence is still inert markup
	// when the policy runs, so sanitizing cannot damage it, and RenderDiagramsForUI's output
	// never passes through the policy. Same order as ui_embed.go's {{md}} path, for the same
	// reason.
	body := RenderDiagramsGated(SanitizeArtifactHTML(agentHTML))

	var b strings.Builder
	b.WriteString(`<!doctype html><html data-theme="` + html.EscapeString(opt.themeAttr()) +
		`"><head><meta charset="utf-8">`)
	b.WriteString(`<meta name="viewport" content="width=device-width,initial-scale=1">`)
	b.WriteString(`<meta http-equiv="Content-Security-Policy" content="` +
		html.EscapeString(innerCSP(nonce, opt.BridgeScript != "")) + `">`)
	b.WriteString("<title>" + html.EscapeString(opt.Title) + "</title>")
	b.WriteString("<style>" + innerBaseCSS + "</style>")
	if opt.BridgeScript != "" {
		// Nonced so the CSP above admits our script while still refusing any inline
		// script that survived sanitization.
		b.WriteString(`<script nonce="` + html.EscapeString(nonce) + `">`)
		b.WriteString(opt.BridgeScript)
		b.WriteString(`</script>`)
	}
	b.WriteString(`</head><body><div class="pf-doc prose">`)
	b.WriteString(body)
	b.WriteString(`</div></body></html>`)
	return b.String()
}

// innerCSP is the second line of defence inside the frame.
//
// Deliberately absent: `sandbox` and `frame-ancestors`. Neither is deliverable through
// a <meta> tag — only through a real HTTP header — so listing them here would read as
// protection while doing nothing. Frame isolation comes from the sandbox *attribute*;
// this policy governs what the document may load.
//
// # This is NOT the only policy governing the frame
//
// `about:srcdoc` is a local scheme, so the frame does not fetch its own document and gets no
// policy delivery of its own: it INHERITS the policy container of the document that created
// it. The frame is therefore governed by the conjunction of the embedding page's policy and
// this one — on /ui that means uiPageCSPWithNonce AND innerCSP, with the more restrictive directive
// winning in each case. Reading this function as the whole story is wrong, and the mistake is
// invisible until something breaks.
//
// The conjunction is benign and each directive lands where intended:
//
//	style-src   page 'self' 'unsafe-inline'  ∧  'unsafe-inline'  → inline OK, d2 paints
//	font-src    'self' data:                 ∧  data:            → data: OK, webfont loads
//	img-src     data:                        ∧  data:            → data: only
//	script-src  'self' 'nonce-N'             ∧  'nonce-N'        → only our nonced bridge
//
// # The script-src row is the one that had to be arranged, not observed
//
// aihub#243 replaced the page's `script-src 'unsafe-inline'` with a per-response nonce. Had the
// frame kept minting its own value, the page would present 'nonce-R' and the frame 'nonce-N',
// the conjunction would admit NEITHER, the bridge would not execute, and every embedded
// document would silently stop reporting its height and sit at its 220px starting size with an
// inner scrollbar. Nothing errors and nothing logs; it was reproduced on Chromium 131 before
// the change.
//
// So the two values are deliberately ONE value: /ui hands the page nonce to SafeEmbedDocument
// through EmbedOptions.Nonce, and both rows above read 'nonce-N' because they are the same N.
// A caller that omits it gets a self-minted nonce, which is correct only outside a
// nonce-carrying page. TestInnerCSP_FrameBridgeRunsOnThePageNonce and
// TestUIFuncMap_FramesRunOnThePageNonce hold both halves of that in place.
func innerCSP(nonce string, withScript bool) string {
	parts := []string{
		"default-src 'none'",
		// Inline styles only: d2 embeds its stylesheet in the svg, and no external
		// stylesheet may be fetched.
		"style-src 'unsafe-inline'",
		// data: and same-origin only. An EXTERNAL image URL in a private document is a
		// read receipt, which is why no scheme/host form is admitted here.
		//
		// 'self' is deliberately NOT here. An earlier version admitted it, reasoning that the
		// frame's base URL is the embedding page's so 'self' would resolve to our own origin
		// and re-admit the root-relative sources the sanitizer allows (reSafeImageURL permits
		// /path). That conflates the document's base URL with the policy's self-origin: this
		// frame is sandboxed WITHOUT allow-same-origin, so its origin is opaque and 'self'
		// matches nothing. The directive was a no-op, and writing it made the policy read as
		// though root-relative images work inside the frame when they never did.
		"img-src data:",
		"font-src data:",
		"form-action 'none'",
		"base-uri 'none'",
		"object-src 'none'",
		// No network egress of any kind from inside the frame.
		"connect-src 'none'",
	}
	if withScript && nonce != "" {
		parts = append(parts, "script-src 'nonce-"+nonce+"'")
	} else {
		parts = append(parts, "script-src 'none'")
	}
	return strings.Join(parts, "; ")
}

// innerCSPForTest exposes the policy string for assertions about what it must not claim.
func innerCSPForTest() string { return innerCSP("N", true) }

// innerBaseCSS is the frame's entire stylesheet. It has to be self-contained: the inner CSP
// is default-src 'none' with style-src 'unsafe-inline', so no <link> can be fetched — an
// embedded document cannot share ui.css with the page around it.
//
// The colour tokens below are transcribed from ui.css. That is a real duplication and worth
// naming: the alternative is an embedded document whose colours do not match the page it sits
// in, which is what a naive port of these surfaces into a frame produces. Keep them in step;
// the values are deliberately identical, not merely similar, and
// TestInnerBaseCSS_ColourTokensTrackUICSS fails the build when they drift.
//
// They were NOT identical until aihub#240 caught it: this sheet carried GitHub's palette
// (--text #e6edf3 / --surface-2 #161b22 / --border #30363d) while claiming to mirror ui.css
// (#ededf0 / #1c1c20 / #2a2a2f). Small numbers, but the frame is transparent over the page's
// own surface, so the mismatch showed up as a document that read as slightly foreign.
//
// --link is the one token with no ui.css counterpart: ui.css paints .prose a as var(--text)
// plus a subtle underline, which works in a card surrounded by chrome. An embedded document
// has no chrome, so links here keep a colour of their own. That is a deliberate divergence,
// not drift, and the drift guard excludes it for exactly that reason.
//
// --fig-* are the figure palette (aihub#240): the three semantic hues an agent-authored diagram
// uses for its subsystem borders and titles. They exist as tokens rather than as literals inside
// each SVG because NO single hue clears 4.5:1 on both a white and a near-black surface —
// measured: #177c36 is 5.29:1 on white and 3.45:1 on --surface, so a figure with its colours
// baked in can only be accessible in one theme. The dark values are ui.css's own
// --success / --danger / --link dark values, so a figure stays inside the product's palette.
// Figures reference them as var(--fig-data,#177c36): the literal fallback keeps a figure
// looking right when it is viewed outside this frame (a raw .svg file, /v1, a saved page).
//
// The .prose SIZES below are not from ui.css — they follow render/style.css. See
// TestInnerBaseCSS_IsADocumentStylesheet for why.
//
// Theme is resolved in CSS across all three states, mirroring ui.css so the server can emit
// data-theme="auto" and the correct colours paint on first load — no flash, no script. An
// explicit light choice must still win under a dark OS, which is why the media query is
// guarded on :not([data-theme="dark"]) rather than applied bare.
// It is also the ONLY stylesheet an embedded document gets. SanitizeArtifactHTML drops
// <style> and its body outright (D1), so an agent cannot ship CSS with its document and must
// not try: it authors semantic HTML — h1..h4, p, table, pre, blockquote, figure — and this
// sheet paints it. That is the classless-framework contract, and it is what makes the trade
// D1 made survivable. A figure needing per-shape colour uses presentation attributes
// (fill=, stroke=) or the allowlisted style="" attribute, never a scoped stylesheet.
//
// The proportions below follow render/style.css, the sheet that gives /v1 and /share their
// look, because an embedded document and a shared one should not read as different products.
// The previous version of this sheet set h1..h4 all to 14px against 13.5px body text, i.e. no
// hierarchy at all; a long report rendered as an undifferentiated wall.
const innerBaseCSS = `:root{--mono:ui-monospace,SFMono-Regular,"SF Mono",Menlo,monospace;` +
	`--r:6px;--s3:12px;` +
	`--surface-2:#f6f6f4;--border:#e6e5e1;--text:#1c1c20;--text-muted:#646469;` +
	`--text-subtle:#6e6e77;--link:#0969da;` +
	`--fig-data:#177c36;--fig-state:#a94f48;--fig-ctrl:#4a6c90}` +
	`html[data-theme="dark"]{--surface-2:#1c1c20;--border:#2a2a2f;--text:#ededf0;` +
	`--text-muted:#a0a0a8;--text-subtle:#8a8a94;--link:#4493f8;` +
	`--fig-data:#3fcf78;--fig-state:#f2655a;--fig-ctrl:#4493f8}` +
	`@media(prefers-color-scheme:dark){html:not([data-theme="light"]){` +
	`--surface-2:#1c1c20;--border:#2a2a2f;--text:#ededf0;--text-muted:#a0a0a8;` +
	`--text-subtle:#8a8a94;--link:#4493f8;` +
	`--fig-data:#3fcf78;--fig-state:#f2655a;--fig-ctrl:#4493f8}}` +

	// The frame paints no background of its own: it must read as part of the parent page,
	// and the iframe element is transparent so the parent's surface shows through.
	`html,body{margin:0;padding:0;background:transparent;color:var(--text);` +
	`font:15px/1.65 -apple-system,BlinkMacSystemFont,"Segoe UI","Helvetica Neue",` +
	`"PingFang SC","Hiragino Sans GB","Microsoft YaHei","Noto Sans CJK SC",sans-serif}` +
	`.pf-doc{padding:0}` +
	`.prose{min-width:0}` +
	`.prose>:first-child{margin-top:0}` +
	`.prose>:last-child{margin-bottom:0}` +

	// Headings carry the hierarchy. h1/h2 take the rule under them that style.css uses, which
	// is what makes section boundaries visible in a long document without extra markup.
	`.prose h1,.prose h2{border-bottom:1px solid var(--border);padding-bottom:.3em;` +
	`margin:1.6em 0 .6em;font-weight:600;line-height:1.3}` +
	`.prose h1{font-size:1.85em}` +
	`.prose h2{font-size:1.4em}` +
	`.prose h3,.prose h4,.prose h5,.prose h6{margin:1.4em 0 .5em;font-weight:600;line-height:1.35}` +
	`.prose h3{font-size:1.15em}` +
	`.prose h4{font-size:1em}` +
	`.prose h5,.prose h6{font-size:.92em;color:var(--text-muted)}` +

	`.prose p{margin:0 0 .9em}` +
	`.prose strong{font-weight:600}` +
	`.prose hr{border:0;border-top:1px solid var(--border);margin:1.8em 0}` +
	`.prose blockquote{margin:1em 0;padding:0 1em;border-left:4px solid var(--border);` +
	`color:var(--text-muted)}` +
	`.prose blockquote>:first-child{margin-top:0}` +
	`.prose blockquote>:last-child{margin-bottom:0}` +

	`.prose code{font-family:var(--mono);font-size:.88em;background:var(--surface-2);` +
	`border:1px solid var(--border);padding:.1em .35em;border-radius:4px;` +
	`overflow-wrap:anywhere;word-break:break-word}` +
	`.prose pre{background:var(--surface-2);border:1px solid var(--border);` +
	`border-radius:var(--r);padding:var(--s3);overflow-x:auto;max-width:100%;` +
	`margin:0 0 1em;line-height:1.45}` +
	`.prose pre code{background:none;border:0;padding:0;font-size:.86em}` +

	// Tables get the full column and scroll inside their own box when they cannot fit, rather
	// than stretching the document — the rule style.css already states for /v1.
	//
	// Two bugs lived in the previous version of this rule, both reported as "the tables look
	// squeezed" and both measured on a real document before being changed:
	//
	//  1. `width:max-content` together with `max-width:100%` resolves to the container width,
	//     which means the table could never be wider than its column — so `overflow-x:auto`
	//     was DEAD CODE. A table that did not fit was compressed, never scrolled. A synthetic
	//     10-column table was crushed to 88px per column with no scrollbar; it now scrolls.
	//  2. CJK text has a break opportunity between every pair of characters, so a Chinese
	//     column's min-content width is ONE GLYPH. Auto table layout is free to crush such a
	//     column to nothing and reflow its contents one character per line, which is exactly
	//     what happened: 42px and 58px columns in a 4-column table, cells four lines deep.
	//     min-width puts a floor under that. 5.5em is the narrowest value that stopped the
	//     crush without taking so much width from the prose column that IT started wrapping.
	//
	// The density is deliberately unchanged otherwise: raising font-size and padding at the
	// same time (the first attempt) made a table that previously fit on one line per row wrap
	// on 9 of 24 cells. The floor is the fix; the padding is not.
	`.prose table{display:block;overflow-x:auto;max-width:100%;` +
	`border-collapse:collapse;margin:0 0 1.15em;font-size:.93em}` +
	`.prose th,.prose td{border:1px solid var(--border);padding:8px 12px;` +
	`text-align:left;vertical-align:top;min-width:5.5em;overflow-wrap:anywhere}` +
	// A header that wraps reads as two columns; it is short by nature, so it never needs to.
	`.prose th{background:var(--surface-2);font-weight:600;white-space:nowrap}` +
	`.prose tbody tr:nth-child(even){background:var(--surface-2)}` +

	`.prose ul,.prose ol{margin:0 0 .9em;padding-left:1.6em}` +
	`.prose li{margin:.25em 0}` +
	`.prose li>p{margin:.3em 0}` +
	`.prose a{color:var(--link);text-decoration:none}` +
	`.prose a:hover{text-decoration:underline}` +

	// Figures scale down to fit and are never upscaled. d2's outer <svg> may carry a viewBox
	// with no width/height, which CSS otherwise resolves to 100% of the column and blows the
	// figure up (mem_0v7S0TTo).
	`.pf-doc svg{max-width:100%;height:auto}` +
	`.pf-doc img{max-width:100%;height:auto}` +
	`.prose figure{margin:1.2em 0}` +
	`.prose figcaption{margin-top:.5em;font-size:.88em;color:var(--text-muted)}` +

	// Figures never get a horizontal scrollbar. A figure that scrolls sideways inside a
	// document that scrolls downwards hides part of the shape behind an axis the reader is
	// not looking for, and a diagram is the one element whose whole point is being seen at
	// once. Over-wide graphs are handled upstream instead, by re-running the layout
	// vertically — see narrowerLayout in diagram_gate.go. Anything still too wide after that
	// scales to fit, which is degraded but complete.
	`.prose figure{overflow-x:hidden}`

// failsafeFrame is what a caller gets if embedding panicked. It is still a sandboxed
// frame: degrading to unisolated rendering would turn a display bug into an XSS.
func failsafeFrame(opt EmbedOptions) string {
	doc := `<!doctype html><html><head><meta charset="utf-8">` +
		`<meta http-equiv="Content-Security-Policy" content="` +
		html.EscapeString(innerCSP("", false)) + `">` +
		`</head><body><p>This document could not be rendered.</p></body></html>`
	// FrameClass and loading are carried over deliberately. Dropping them left a panicked
	// embed as a default 300x150 bordered box that embedframe.js could not find (it selects
	// iframe.pf-embed) and that ui.css did not style — so a rendering fault degraded the page
	// layout on top of the content. The security posture is unchanged either way: still
	// sandboxed, still carrying the inner CSP.
	var b strings.Builder
	b.WriteString(`<iframe`)
	if opt.FrameClass != "" {
		b.WriteString(` class="` + html.EscapeString(opt.FrameClass) + `"`)
	}
	b.WriteString(` sandbox="` + sandboxTokens + `"`)
	b.WriteString(` referrerpolicy="no-referrer"`)
	b.WriteString(` loading="lazy"`)
	b.WriteString(` title="` + html.EscapeString(opt.Title) + `"`)
	b.WriteString(` srcdoc="` + html.EscapeString(doc) + `"`)
	b.WriteString(`></iframe>`)
	return b.String()
}

// newNonce returns a fresh CSP nonce. crypto/rand is used because a predictable nonce
// would let injected markup name it and execute.
func newNonce() string {
	var buf [16]byte
	if _, err := rand.Read(buf[:]); err != nil {
		// rand.Read does not fail in practice; if it ever does, refuse to emit a
		// guessable nonce — the caller degrades to script-src 'none'.
		return ""
	}
	return base64.RawURLEncoding.EncodeToString(buf[:])
}
