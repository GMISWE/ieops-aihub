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

	// nonce is a test seam. Empty means "generate a fresh one".
	nonce string
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

	nonce := opt.nonce
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
	body := RenderDiagramsForUI(SanitizeArtifactHTML(agentHTML))

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
// this one — on /ui that means uiPageCSP AND innerCSP, with the more restrictive directive
// winning in each case. Reading this function as the whole story is wrong, and the mistake is
// invisible until something breaks.
//
// Today the conjunction is benign and each directive still lands where intended:
//
//	style-src   uiPageCSP 'self' 'unsafe-inline'  ∧  'unsafe-inline'  → inline OK, d2 paints
//	font-src    'self' data:                      ∧  data:           → data: OK, webfont loads
//	img-src     'self' data:                      ∧  data:           → data: only
//	script-src  'self' 'unsafe-inline'            ∧  'nonce-N'       → only our nonced bridge
//
// # aihub#243 will break this if it is not accounted for
//
// aihub#243 replaces uiPageCSP's `script-src 'unsafe-inline'` with a per-response nonce. The
// page nonce R and this frame's bridge nonce N are different values, so the inherited
// script-src becomes `'nonce-R'` while the frame's is `'nonce-N'`: the conjunction admits
// NEITHER, the bridge does not execute, and every embedded document silently stops reporting
// its height and sits at its 220px starting size with an inner scrollbar. Nothing errors.
//
// Whoever does aihub#243 must either mint the frame's bridge nonce from the same per-response
// nonce the page uses, or keep frames on a nonce-compatible script-src. This paragraph exists
// so that requirement is discovered by reading the code rather than by a user reporting that
// every document is 220 pixels tall.
func innerCSP(nonce string, withScript bool) string {
	parts := []string{
		"default-src 'none'",
		// Inline styles only: d2 embeds its stylesheet in the svg, and no external
		// stylesheet may be fetched.
		"style-src 'unsafe-inline'",
		// data: and same-origin only. An EXTERNAL image URL in a private document is a
		// read receipt, which is why no scheme/host form is admitted here.
		//
		// 'self' is present because the sanitizer admits root-relative sources
		// (reSafeImageURL allows /path), so without it a first-party <img src="/ui/..."> in
		// agent content rendered fine on the un-sandboxed artifact viewer and silently failed
		// inside a frame — the two controls disagreeing about the same input. The frame's base
		// URL is the embedding page's, so 'self' resolves to our own origin; this admits no
		// destination the parent page could not already load.
		"img-src 'self' data:",
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
// The token values and the .prose rules below are transcribed from ui.css. That is a real
// duplication and worth naming: the alternative is an embedded document whose typography
// does not match the page it sits in, which is what a naive port of these surfaces into a
// frame produces. Keep them in step; the numbers are deliberately identical, not merely
// similar.
//
// Theme is resolved in CSS across all three states, mirroring ui.css so the server can emit
// data-theme="auto" and the correct colours paint on first load — no flash, no script. An
// explicit light choice must still win under a dark OS, which is why the media query is
// guarded on :not([data-theme="dark"]) rather than applied bare.
const innerBaseCSS = `:root{--mono:"Geist Mono",ui-monospace,SFMono-Regular,"SF Mono",Menlo,monospace;` +
	`--r:7px;--s3:12px;` +
	`--surface-2:#f6f6f4;--border:#e6e5e1;--text:#1c1c20;--text-muted:#646469;--text-subtle:#94949b}` +
	`html[data-theme="dark"]{--surface-2:#1c1c20;--border:#2a2a2f;--text:#ededf0;` +
	`--text-muted:#a0a0a8;--text-subtle:#6e6e77}` +
	`@media(prefers-color-scheme:dark){html:not([data-theme="light"]){` +
	`--surface-2:#1c1c20;--border:#2a2a2f;--text:#ededf0;--text-muted:#a0a0a8;--text-subtle:#6e6e77}}` +

	// The frame paints no background of its own: it must read as part of the parent page,
	// and the iframe element is transparent so the parent's surface shows through.
	`html,body{margin:0;padding:0;background:transparent;` +
	`font:14px/1.6 system-ui,-apple-system,"Segoe UI",Roboto,sans-serif;color:var(--text)}` +
	`.pf-doc{padding:0}` +

	// Diagrams scale down to fit and are never upscaled. d2's outer <svg> may carry a
	// viewBox with no width/height, which CSS otherwise resolves to 100% of the column
	// and blows the figure up (mem_0v7S0TTo).
	`.pf-doc svg{max-width:100%;height:auto}` +
	`.pf-doc img{max-width:100%;height:auto}` +

	// --- transcribed from ui.css .prose (keep in step) ---
	`.prose{font-size:13.5px;min-width:0}` +
	`.prose>:first-child{margin-top:0}` +
	`.prose>:last-child{margin-bottom:0}` +
	`.prose p{margin:0 0 10px}` +
	`.prose h1,.prose h2,.prose h3,.prose h4{margin:14px 0 6px;font-size:14px;font-weight:650}` +
	`.prose h1{font-size:16px}` +
	`.prose code{font-family:var(--mono);font-size:12px;background:var(--surface-2);` +
	`border:1px solid var(--border);padding:1px 5px;border-radius:5px;` +
	`overflow-wrap:anywhere;word-break:break-word}` +
	`.prose pre{background:var(--surface-2);border:1px solid var(--border);` +
	`border-radius:var(--r);padding:var(--s3);overflow:auto;max-width:100%}` +
	`.prose pre code{background:none;border:0;padding:0}` +
	`.prose table{display:block;width:max-content;max-width:100%;overflow-x:auto;` +
	`border-collapse:collapse;margin:0 0 10px;font-size:13px}` +
	`.prose th,.prose td{border:1px solid var(--border);padding:6px 10px;` +
	`text-align:left;vertical-align:top}` +
	`.prose th{background:var(--surface-2);color:var(--text);font-weight:600}` +
	`.prose tbody tr:nth-child(even){background:var(--surface-2)}` +
	`.prose ul,.prose ol{margin:0 0 10px;padding-left:18px}` +
	`.prose li{margin:3px 0;color:var(--text-muted)}` +
	`.prose a{color:var(--text);text-decoration:underline;text-underline-offset:2px;` +
	`text-decoration-color:var(--text-subtle)}` +
	`.prose a:hover{text-decoration-color:var(--text)}`

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
