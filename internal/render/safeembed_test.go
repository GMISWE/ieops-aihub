package render

import (
	"html"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
)

// The single most consequential assertion in this package. Granting allow-scripts and
// allow-same-origin together does not weaken isolation — it removes it outright: script
// inside the frame gains same-origin privileges and can simply strip the sandbox
// attribute off its own frame element (01-static-html-render-engine-research.md §2.1).
// There is deliberately no option, argument, or config that can add it.
func TestSafeEmbed_SandboxNeverGrantsSameOrigin(t *testing.T) {
	inputs := []string{
		"<p>hello</p>",
		"<svg viewBox=\"0 0 10 10\"><rect width=\"10\" height=\"10\"/></svg>",
		"",
		"<script>alert(1)</script>",
		"<iframe sandbox=\"allow-scripts allow-same-origin\" src=\"x\"></iframe>",
	}
	for _, in := range inputs {
		out := SafeEmbedDocument(in, EmbedOptions{Title: "t"})
		// Assert the attribute equals allow-scripts exactly, rather than searching the
		// response for the string "allow-same-origin". The substring form is unsound in
		// both directions: it misses `sandbox="allow-scripts allow-forms"` (a widening
		// it does not name) and it false-positives on any document that merely mentions
		// the token — which the annotation bridge's own comments do.
		if got := sandboxAttrOf(t, out); got != "allow-scripts" {
			t.Fatalf("sandbox = %q, want exactly \"allow-scripts\"\ninput: %q", got, in)
		}
	}
}

// The failsafe path is still a sandboxed frame. Degrading to unisolated rendering on an
// internal error would turn a display bug into an XSS.
func TestSafeEmbed_FailsafeKeepsSandbox(t *testing.T) {
	if got := sandboxAttrOf(t, failsafeFrame(EmbedOptions{Title: "t"})); got != "allow-scripts" {
		t.Fatalf("failsafe sandbox = %q, want exactly \"allow-scripts\"", got)
	}
}

// The agent's HTML lands inside a quoted srcdoc attribute. If its quotes are not
// escaped it breaks out of the attribute and back into the parent document, where the
// sandbox does not apply — the sanitizer's output would be re-parsed as parent markup.
func TestSafeEmbed_SrcdocAttributeCannotBeEscaped(t *testing.T) {
	breakout := `<p class="x">a</p>" onload="alert(1)`
	out := SafeEmbedDocument(breakout, EmbedOptions{Title: "t"})

	// Everything after the opening srcdoc=" up to the closing quote is the payload.
	i := strings.Index(out, `srcdoc="`)
	if i < 0 {
		t.Fatalf("no srcdoc attribute in output: %s", out)
	}
	rest := out[i+len(`srcdoc="`):]
	j := strings.Index(rest, `"`)
	if j < 0 {
		t.Fatalf("srcdoc attribute is unterminated: %s", out)
	}
	tail := rest[j:]
	// After the attribute closes, only the iframe's own remaining markup may appear.
	if strings.Contains(strings.ToLower(tail), "onload") {
		t.Errorf("payload escaped the srcdoc attribute into parent markup: %s", tail)
	}
}

// innerDoc returns the document the browser reconstructs from srcdoc, i.e. the output
// with exactly one level of attribute escaping removed. Asserting here rather than on
// the raw bytes is deliberate: the raw form is double-escaped (once for the inner
// document's own attributes, once for the srcdoc attribute that carries it), so a
// literal-byte assertion tests the encoding rather than the behaviour.
func innerDoc(t *testing.T, out string) string {
	t.Helper()
	i := strings.Index(out, `srcdoc="`)
	if i < 0 {
		t.Fatalf("no srcdoc attribute: %s", out)
	}
	rest := out[i+len(`srcdoc="`):]
	j := strings.Index(rest, `"`)
	if j < 0 {
		t.Fatalf("unterminated srcdoc: %s", out)
	}
	return html.UnescapeString(rest[:j])
}

// innerDocDecoded removes the second level too, yielding the values the inner parser
// hands to the engine (e.g. the CSP string as the browser applies it).
func innerDocDecoded(t *testing.T, out string) string {
	t.Helper()
	return html.UnescapeString(innerDoc(t, out))
}

func TestSafeEmbed_InnerDocumentCarriesCSP(t *testing.T) {
	decoded := innerDocDecoded(t, SafeEmbedDocument("<p>x</p>", EmbedOptions{Title: "t"}))
	for _, want := range []string{
		"Content-Security-Policy",
		"default-src 'none'",
		"connect-src 'none'",
		"object-src 'none'",
		// data: only, matching the sanitizer, which no longer admits any network form for
		// images. An earlier revision had `'self' data:` here on the reasoning that the frame
		// inherits the parent's base URL; that conflated base URL with the policy's
		// self-origin, and since the frame is sandboxed without allow-same-origin its origin
		// is opaque, so `'self'` matched nothing at all.
		"img-src data:",
		// No bridge script was requested, so nothing may execute.
		"script-src 'none'",
	} {
		if !strings.Contains(decoded, want) {
			t.Errorf("inner CSP missing %q\ndecoded document: %s", want, decoded)
		}
	}

	// What must NOT be reachable: any network form at all. An external image URL in a private
	// document is a read receipt, and a protocol-relative one (//host) is the form that
	// slipped past the sanitizer's own allowlist for three revisions.
	for _, forbidden := range []string{"img-src *", "img-src 'self'", "https:", "http:"} {
		if strings.Contains(decoded, "img-src") && strings.Contains(decoded, forbidden) {
			t.Errorf("inner CSP admits an external image source (%q): %s", forbidden, decoded)
		}
	}
}

// TestInnerBaseCSS_ProseRulesTrackUICSS is drift detection for a duplication the code admits
// to: innerBaseCSS transcribes ui.css's .prose rules because a frame under
// default-src 'none' cannot fetch a stylesheet, so the alternative is an embedded document
// whose typography does not match the page around it.
//
// The duplication is currently correct. What was missing is any way to NOTICE it going wrong:
// someone tuning .prose in ui.css six months from now gets no signal that a second copy
// exists. This compares the declarations that both files claim to share.
//
// It deliberately does not require the two to be identical — innerBaseCSS legitimately omits
// rules that cannot apply inside the frame. It requires that where both define the same
// property for the same selector, the values agree.
func TestInnerBaseCSS_IsADocumentStylesheet(t *testing.T) {
	// This replaces TestInnerBaseCSS_ProseRulesTrackUICSS, which asserted innerBaseCSS
	// transcribed ui.css's .prose block declaration-for-declaration.
	//
	// That coupling was a means to an end — "an embedded document must not look accidentally
	// different from its surroundings" — and the end changed. ui.css's .prose styles a compact
	// card inside a page; it set h1..h4 all to 14px against 13.5px body text, so a long report
	// rendered with no hierarchy whatsoever. The frame now carries a DOCUMENT sheet whose
	// proportions follow render/style.css, the sheet behind /v1 and /share, so an embedded
	// report and a shared one read as the same product. The guard therefore checks the
	// properties that make it a document sheet, not equality with a card sheet.

	// Matched against the raw const, not normaliseCSS output: that helper strips every space,
	// which turns ".prose h1{" into ".proseh1{" and makes every expectation below unreadable.
	css := innerBaseCSS

	// Anchored on the standalone size rule. A plain Index(".prose h2{") would match inside the
	// combined ".prose h1,.prose h2{" selector, whose block carries the border but no size.
	sizeOf := func(sel string) float64 {
		t.Helper()
		re := regexp.MustCompile(`(?:^|;|\})` + regexp.QuoteMeta(sel) + `\{font-size:([0-9.]+)em\}`)
		m := re.FindStringSubmatch(css)
		if m == nil {
			t.Fatalf("innerBaseCSS has no standalone %s{font-size:…em} rule", sel)
		}
		v, err := strconv.ParseFloat(m[1], 64)
		if err != nil {
			t.Fatalf("%s font-size unparseable: %v", sel, err)
		}
		return v
	}

	// 1. Hierarchy is strictly decreasing. This is the assertion the old flat-14px sheet
	//    would fail, and it is the whole reason for the rewrite.
	h1, h2, h3 := sizeOf(".prose h1"), sizeOf(".prose h2"), sizeOf(".prose h3")
	if !(h1 > h2 && h2 > h3) {
		t.Errorf("headings must form a strict hierarchy, got h1=%v h2=%v h3=%v", h1, h2, h3)
	}
	if h1 <= 1.0 {
		t.Errorf("h1 must be larger than body text, got %vem", h1)
	}

	// 2. h1/h2 carry the section rule that makes boundaries visible in a long document —
	//    the one affordance render/style.css leans on hardest.
	if !strings.Contains(css, ".prose h1,.prose h2{border-bottom:1px solid") {
		t.Error("h1/h2 should carry a bottom border, as render/style.css does")
	}

	// 3. No figure ever gets a horizontal scrollbar, and every figure fits the column.
	//    Over-wide graphs are re-laid-out vertically upstream (narrowerLayout); the sheet's
	//    job is only to make sure nothing here reintroduces a second scroll axis.
	if !strings.Contains(css, ".prose figure{overflow-x:hidden}") {
		t.Error("figures must not scroll horizontally")
	}
	if strings.Contains(css, "overflow-x:auto") && !strings.Contains(css, ".prose table{") {
		t.Error("unexpected overflow-x:auto outside the table rule")
	}
	if !strings.Contains(css, ".pf-doc svg{max-width:100%") {
		t.Error("figures must scale to fit the column")
	}
	if strings.Contains(css, "max-width:none") {
		t.Error("nothing may opt out of the column width")
	}

	// 4. Self-contained: the inner CSP is default-src 'none', so anything fetched is a blank.
	for _, bad := range []string{"@import", "url(http", "url(//"} {
		if strings.Contains(css, bad) {
			t.Errorf("innerBaseCSS must not reference external resources, found %q", bad)
		}
	}

	// 5. Dark palette defined in all three theme states (explicit dark, explicit light under a
	//    dark OS, and system default) — a token defined in only one is a flash or a wrong theme.
	for _, need := range []string{
		`html[data-theme="dark"]{`,
		`@media(prefers-color-scheme:dark){html:not([data-theme="light"]){`,
	} {
		if !strings.Contains(css, need) {
			t.Errorf("missing theme state: %s", need)
		}
	}
}

// TestInnerBaseCSS_ColourTokensTrackUICSS is the drift guard for the colour half of the
// duplication innerBaseCSS admits to. TestInnerBaseCSS_IsADocumentStylesheet covers the
// typography half, and deliberately does NOT require equality there — the sizes follow
// render/style.css. The colours are the opposite case: the frame is transparent over the
// parent's surface, so any divergence reads as a document that does not belong to the page.
//
// This test exists because the claim was false when it was written. innerBaseCSS carried
// GitHub's palette while its own comment said "transcribed from ui.css"; nothing checked, so
// nothing noticed (aihub#240). A comment cannot hold a duplication in step — this can.
//
// --link is excluded: ui.css has no such token (it paints .prose a with --text plus an
// underline), so there is nothing to track. See the comment on innerBaseCSS.
func TestInnerBaseCSS_ColourTokensTrackUICSS(t *testing.T) {
	tokens := []string{"--surface-2", "--border", "--text", "--text-muted", "--text-subtle"}

	// block returns the declarations between opener and the next "}". Both sheets declare
	// their palettes as one flat block per theme, so a brace counter would be over-built.
	block := func(css, opener, where string) string {
		i := strings.Index(css, opener)
		if i < 0 {
			t.Fatalf("%s: no %q block — repoint this guard rather than deleting it", where, opener)
		}
		rest := css[i+len(opener):]
		j := strings.Index(rest, "}")
		if j < 0 {
			t.Fatalf("%s: %q block is unterminated", where, opener)
		}
		return rest[:j]
	}

	// valueOf reads one custom property out of a block. Matching on ";--token:" (after
	// normalising whitespace away) keeps "--text" from matching inside "--text-muted".
	valueOf := func(blk, token, where string) string {
		re := regexp.MustCompile(`(?:^|[;{])` + regexp.QuoteMeta(token) + `:([^;}]+)`)
		m := re.FindStringSubmatch(normaliseCSS(blk))
		if m == nil {
			t.Fatalf("%s: %s is not declared", where, token)
		}
		return m[1]
	}

	// Comments first: ui.css documents these very tokens by name right inside the palette
	// blocks, and prose like "--text-subtle was #94949b" otherwise parses as a declaration.
	uiCSS := regexp.MustCompile(`(?s)/\*.*?\*/`).ReplaceAllString(readUICSSFromDisk(t), "")
	for _, pair := range []struct{ theme, uiOpener, frameOpener string }{
		{"light", `html[data-theme="light"], :root {`, `:root{`},
		{"dark", `html[data-theme="dark"] {`, `html[data-theme="dark"]{`},
	} {
		ui := block(uiCSS, pair.uiOpener, "ui.css "+pair.theme)
		frame := block(innerBaseCSS, pair.frameOpener, "innerBaseCSS "+pair.theme)
		for _, tok := range tokens {
			want := valueOf(ui, tok, "ui.css "+pair.theme)
			got := valueOf(frame, tok, "innerBaseCSS "+pair.theme)
			if got != want {
				t.Errorf("%s %s: innerBaseCSS has %s, ui.css has %s — the frame paints over the "+
					"page's own surface, so these must agree", pair.theme, tok, got, want)
			}
		}
	}

	// The frame declares its dark palette twice (explicit dark, and system-dark for anything
	// not explicitly light). Half-updating one of them is the exact mistake this catches.
	explicit := block(innerBaseCSS, `html[data-theme="dark"]{`, "innerBaseCSS dark")
	system := block(innerBaseCSS, `@media(prefers-color-scheme:dark){html:not([data-theme="light"]){`, "innerBaseCSS system-dark")
	for _, tok := range append(tokens, "--link") {
		if a, b := valueOf(explicit, tok, "explicit dark"), valueOf(system, tok, "system dark"); a != b {
			t.Errorf("dark %s: explicit block has %s, system block has %s", tok, a, b)
		}
	}
}

// readUICSSFromDisk reads internal/server/static/ui.css.
//
// From disk, not from an embed: ui.css belongs to the server package's embed.FS and this test
// lives in render, which cannot import it (server imports render, so the reverse would be a
// cycle). Tests run with the package directory as the working directory, so the relative path
// is stable. A missing file fails the test rather than skipping it — if the path moves, this
// guard must be repointed, not silently disabled.
func readUICSSFromDisk(t *testing.T) string {
	t.Helper()
	const rel = "../server/static/ui.css"
	b, err := os.ReadFile(rel)
	if err != nil {
		t.Fatalf("read %s: %v — repoint this drift guard rather than deleting it", rel, err)
	}
	return string(b)
}

// normaliseCSS strips whitespace so declaration comparison is not defeated by formatting.
func normaliseCSS(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch r {
		case ' ', '\t', '\n', '\r':
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// sandbox and frame-ancestors are not deliverable via <meta> (only via a real HTTP
// header). Emitting them there would read as protection while doing nothing, so the
// meta policy must not claim them.
func TestSafeEmbed_MetaCSPOmitsHeaderOnlyDirectives(t *testing.T) {
	out := SafeEmbedDocument("<p>x</p>", EmbedOptions{Title: "t"})
	meta := innerCSPForTest()
	for _, forbidden := range []string{"sandbox", "frame-ancestors"} {
		if strings.Contains(meta, forbidden) {
			t.Errorf("meta CSP claims header-only directive %q: %s", forbidden, meta)
		}
	}
	if !strings.Contains(out, "srcdoc=") {
		t.Fatal("expected srcdoc embedding")
	}
}

// Agent HTML is sanitized on the way in, so safe embedding does not depend on the
// caller having remembered to sanitize first.
func TestSafeEmbed_SanitizesAgentHTML(t *testing.T) {
	out := SafeEmbedDocument(`<p>ok</p><script>alert(1)</script><img src="x" onerror="alert(2)">`,
		EmbedOptions{Title: "t"})
	for _, bad := range []string{"alert(1)", "alert(2)", "onerror"} {
		if strings.Contains(out, bad) {
			t.Errorf("unsanitized content %q survived: %s", bad, out)
		}
	}
	if !strings.Contains(out, "ok") {
		t.Errorf("legitimate content dropped: %s", out)
	}
}

// The trusted bridge script (aihub#240 T4) is ours, not the agent's, and must land in
// <head> with the nonce the CSP grants — otherwise CSP blocks our own code.
func TestSafeEmbed_BridgeScriptIsNonced(t *testing.T) {
	out := SafeEmbedDocument("<p>x</p>", EmbedOptions{
		Title:        "t",
		BridgeScript: "console.log(1)",
		Nonce:        "TESTNONCE",
	})
	if doc := innerDoc(t, out); !strings.Contains(doc, `<script nonce="TESTNONCE">`) {
		t.Errorf("bridge script not nonced\ndocument: %s", doc)
	}
	if decoded := innerDocDecoded(t, out); !strings.Contains(decoded, "script-src 'nonce-TESTNONCE'") {
		t.Errorf("CSP does not grant the bridge nonce\ndecoded: %s", decoded)
	}
}

func TestSafeEmbed_NoBridgeMeansNoScriptSrc(t *testing.T) {
	out := SafeEmbedDocument("<p>x</p>", EmbedOptions{Title: "t", Nonce: "N"})
	if strings.Contains(out, "nonce-N") {
		t.Errorf("granted a script nonce with no bridge script to run: %s", out)
	}
}

// Malformed input must degrade, never panic: a panic here is a 500 on a page that is
// merely displaying a document (aihub-d2-rendering-research backlog: "panic -> 500, no
// recover" was a real defect in the path this replaces).
func TestSafeEmbed_MalformedInputDoesNotPanic(t *testing.T) {
	malformed := []string{
		"<svg><g/onload=x//<p>",
		strings.Repeat("<div>", 5000),
		"<<<>>>&&&",
		"\x00\x01\x02",
		strings.Repeat("<svg><path d=\"", 1000),
	}
	for _, in := range malformed {
		func() {
			defer func() {
				if r := recover(); r != nil {
					t.Errorf("panicked on %.30q: %v", in, r)
				}
			}()
			_ = SafeEmbedDocument(in, EmbedOptions{Title: "t"})
		}()
	}
}

func TestSafeEmbed_NonceIsUniquePerCall(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 50; i++ {
		n := newNonce()
		if n == "" {
			t.Fatal("empty nonce")
		}
		if seen[n] {
			t.Fatalf("nonce collision after %d draws: %s", i, n)
		}
		seen[n] = true
	}
}

func TestSafeEmbed_Concurrent(t *testing.T) {
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 20; j++ {
				_ = SafeEmbedDocument("<svg viewBox=\"0 0 4 4\"><rect width=\"4\" height=\"4\"/></svg>",
					EmbedOptions{Title: "t", BridgeScript: "void 0"})
			}
		}()
	}
	wg.Wait()
}
