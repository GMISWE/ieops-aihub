package server

import (
	"html"
	"html/template"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/labstack/echo/v4"
)

// aihub#240 (resolves #144). Until this change the anonymous /share path was the only
// artifact surface with a CSP: a logged-in project member was the least protected
// reader of the three. These tests pin both halves of the fix and, just as importantly,
// pin the constraint it had to respect — /v1 stays byte-identical (aihub#138).

// renderArtifactAt drives handleArtifactHTML for a given registered route pattern.
// c.SetPath is what the HasPrefix(c.Path(), "/ui") gate reads.
func renderArtifactAt(t *testing.T, path string) *httptest.ResponseRecorder {
	t.Helper()
	e := echo.New()
	c, rec := newUIContext(e, http.MethodGet, path, "mem_share1")
	c.SetPath(path)
	setUser(c, authorUser())
	if err := handleArtifactHTML(nil)(c); err != nil {
		e.HTTPErrorHandler(err, c)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("%s: status %d", path, rec.Code)
	}
	return rec
}

func TestArtifactV1_SendsCSP(t *testing.T) {
	defer withVersionChainOverride()()
	mem := publicSharedMem()
	mem.RenderedHTML = htmlPtr("<h1>t</h1><p>x</p>")
	defer withLoadMemoryOverride(mem, nil)()

	rec := renderArtifactAt(t, "/v1/artifacts/:id/html")

	csp := rec.Header().Get("Content-Security-Policy")
	if csp == "" {
		t.Fatal("/v1 artifact response has no CSP — this is the aihub#144 defect")
	}
	for _, want := range []string{"default-src 'none'", "frame-ancestors 'none'", "base-uri 'none'"} {
		if !strings.Contains(csp, want) {
			t.Errorf("/v1 CSP missing %q: %s", want, csp)
		}
	}
	if rec.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Error("/v1 missing nosniff")
	}
}

// TestArtifactV1CSP_MatchesSharePolicy asserts the invariant the constant's comment claims:
// /v1 and /share send byte-identical policies.
//
// The previous version of this test checked that artifactV1CSP contained six substrings. It
// never read /share's policy, so it could not have caught the two diverging — and they were
// separate string literals at the time, which is exactly the drift the comment said could not
// happen. Both routes are now driven through their real handlers and the emitted headers are
// compared directly, so this passes for the right reason: they are the same value, not merely
// similar-looking ones.
func TestArtifactV1CSP_MatchesSharePolicy(t *testing.T) {
	defer withVersionChainOverride()()
	mem := publicSharedMem()
	mem.RenderedHTML = htmlPtr("<h1>t</h1><p>x</p>")
	defer withLoadMemoryOverride(mem, nil)()

	v1CSP := renderArtifactAt(t, "/v1/artifacts/:id/html").Header().Get("Content-Security-Policy")

	e := echo.New()
	c, rec := newUIContext(e, http.MethodGet, "/share/:id", "mem_share1")
	c.SetPath("/share/:id")
	if err := handleSharedArtifact(nil)(c); err != nil {
		e.HTTPErrorHandler(err, c)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("/share: status %d", rec.Code)
	}
	shareCSP := rec.Header().Get("Content-Security-Policy")

	if shareCSP == "" {
		t.Fatal("/share sent no CSP")
	}
	if v1CSP != shareCSP {
		t.Errorf("/v1 and /share policies diverged\n  /v1:   %s\n  /share: %s", v1CSP, shareCSP)
	}
	// Pin the content too, so "identical" cannot be satisfied by both becoming empty or both
	// losing a directive together.
	for _, want := range []string{
		"default-src 'none'",
		"style-src 'unsafe-inline'",
		"img-src data: https:",
		"form-action 'none'",
		"base-uri 'none'",
		"frame-ancestors 'none'",
	} {
		if !strings.Contains(shareCSP, want) {
			t.Errorf("shared policy lost %q: %s", want, shareCSP)
		}
	}
	if rec.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Error("/share missing nosniff")
	}
}

// The whole point of the /v1 treatment: neutralise the payload with a header instead of
// rewriting bytes, because aihub#138 makes /v1 and /share contractually byte-identical.
// If a future change starts sanitizing /v1 too, this fails and the contract question
// gets raised deliberately rather than discovered in a diff.
func TestArtifactV1_BodyIsNotSanitized(t *testing.T) {
	defer withVersionChainOverride()()
	mem := publicSharedMem()
	mem.RenderedHTML = htmlPtr(`<h1>t</h1><p onclick="x()">kept</p>`)
	defer withLoadMemoryOverride(mem, nil)()

	body := renderArtifactAt(t, "/v1/artifacts/:id/html").Body.String()
	if !strings.Contains(body, `onclick="x()"`) {
		t.Error("/v1 body was rewritten — aihub#138 byte-identity with /share is broken; " +
			"/v1 is protected by its CSP header, not by sanitizing bytes")
	}
}

// /ui is the opposite trade: bytes are already /ui-specific there, so agent content is
// sanitized outright and CSP is the second layer.
func TestArtifactUI_BodyIsSanitized(t *testing.T) {
	defer withVersionChainOverride()()
	mem := publicSharedMem()
	mem.WorkItemID = strptr("aihub#240")
	mem.RenderedHTML = htmlPtr(
		`<h1>t</h1><p>kept</p><script>alert(1)</script><p onclick="alert(2)">also kept</p>`)
	defer withLoadMemoryOverride(mem, nil)()

	body := renderArtifactAt(t, "/ui/artifacts/:id/html").Body.String()

	for _, bad := range []string{"alert(1)", "alert(2)", `onclick="alert`} {
		if strings.Contains(body, bad) {
			t.Errorf("/ui body kept agent payload %q", bad)
		}
	}
	for _, good := range []string{"kept", "also kept"} {
		if !strings.Contains(body, good) {
			t.Errorf("/ui body dropped legitimate content %q", good)
		}
	}
}

// TestArtifactUI_StyleBlockPayloadsDoNotReachTheReader is the end-to-end form of the defect
// that got through this route twice.
//
// TestArtifactUI_BodyIsSanitized above uses a bare <script> and an onclick handler, and both
// were correctly stripped throughout — including while the payloads below were being served
// live. That is the whole lesson: the handler-level test existed, passed, and proved nothing
// about the class that actually broke, because the class was never in it.
//
// The first two cases are the working stored XSS. Inside <svg>, `style` is foreign content, so
// a conformant parser treats its contents as markup; the sanitizer's <style> lifter used a
// bare tokenizer, which sets RAWTEXT unconditionally, and carried a live <img>/<script> out
// as a CSS body and spliced it back past the policy. uiPageCSP still carries
// script-src 'unsafe-inline' (aihub#243), so the second layer did not stop it either — which
// is why this is asserted on the response bytes and not left to CSP.
//
// The remaining cases are the UI-redress half: ordinary CSS that defeated the property
// allowlist and produced a full-page overlay primitive on an authed viewer.
func TestArtifactUI_StyleBlockPayloadsDoNotReachTheReader(t *testing.T) {
	cases := []struct {
		name    string
		payload string
		banned  []string
	}{
		{
			name:    "foreign content lifts a live img",
			payload: `<svg viewBox="0 0 10 10"><style><img src=x onerror=alert(1)></style><rect width="10" height="10"/></svg>`,
			banned:  []string{"onerror", "alert(1)"},
		},
		{
			name:    "foreign content lifts a script element",
			payload: `<svg><style><script>alert(2)</script></style></svg>`,
			banned:  []string{"alert(2)"},
		},
		{
			name:    "unbalanced block is a full-page overlay",
			payload: `<style>.x{position:fixed;top:0;left:0;right:0;bottom:0;z-index:2147483647;background:red</style>`,
			banned:  []string{"position:fixed", "2147483647"},
		},
		{
			name:    "at-rule nesting is a full-page overlay",
			payload: `<style>@media all{.x{position:fixed;top:0;width:100vw;height:100vh;z-index:99999}}</style>`,
			banned:  []string{"position:fixed", "99999"},
		},
		{
			name:    "font-face reaches an external origin",
			payload: `<style>@media all{@font-face{src:url(https://evil.example/f.woff)}}</style>`,
			banned:  []string{"evil.example"},
		},
	}

	// The response shell embeds a first-party stylesheet inline, so "<style" is present in
	// every /ui artifact response regardless of the body. Counting against a benign control
	// isolates the agent's contribution instead of asserting an absolute that is not true.
	renderBody := func(t *testing.T, body string) string {
		t.Helper()
		defer withVersionChainOverride()()
		mem := publicSharedMem()
		mem.WorkItemID = strptr("aihub#240")
		mem.RenderedHTML = htmlPtr(body)
		defer withLoadMemoryOverride(mem, nil)()
		return renderArtifactAt(t, "/ui/artifacts/:id/html").Body.String()
	}
	baseline := strings.Count(strings.ToLower(renderBody(t, `<p>keep</p>`)), "<style")

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body := renderBody(t, `<p>keep</p>`+tc.payload)

			for _, bad := range tc.banned {
				if strings.Contains(body, bad) {
					t.Errorf("payload %q reached the authed /ui reader", bad)
				}
			}
			// The agent's <style> must not add one to the document. Differential, so a change
			// to the viewer's own chrome cannot silently satisfy or break this.
			if n := strings.Count(strings.ToLower(body), "<style"); n != baseline {
				t.Errorf("agent content contributed %d <style> element(s) to the response "+
					"(baseline %d, got %d)", n-baseline, baseline, n)
			}
			if !strings.Contains(body, "keep") {
				t.Error("legitimate content was lost")
			}
		})
	}
}

// Sanitizing the agent body must not eat the viewer's own chrome and scripts, which are
// injected after the sanitize point. Over-sanitizing here would break the page while
// every security assertion still passed.
func TestArtifactUI_FirstPartyChromeSurvivesSanitizing(t *testing.T) {
	defer withVersionChainOverride()()
	mem := publicSharedMem()
	mem.WorkItemID = strptr("aihub#240")
	mem.RenderedHTML = htmlPtr(`<h1 id="title">t</h1><p>body</p>`)
	defer withLoadMemoryOverride(mem, nil)()

	body := renderArtifactAt(t, "/ui/artifacts/:id/html").Body.String()
	for _, want := range []string{
		"/ui/static/viewer.css",
		"/ui/static/ui.css",
		"/ui/static/annot.js",
		`id="pf-side-rail"`,
		`class="pf-appnav"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("sanitizing stripped first-party chrome %q", want)
		}
	}
}

// d2 fences must still compile. Sanitizing runs BEFORE RenderDiagramsForUI precisely so
// that the engine's own SVG output is never fed through the whitelist; if that order is
// ever flipped, the figure silently degrades back to a code block.
func TestArtifactUI_D2StillCompilesAfterSanitizing(t *testing.T) {
	defer withVersionChainOverride()()
	mem := publicSharedMem()
	mem.WorkItemID = strptr("aihub#240")
	mem.RenderedHTML = htmlPtr(
		`<h1>t</h1><pre><code class="language-d2">a -&gt; b</code></pre>`)
	defer withLoadMemoryOverride(mem, nil)()

	body := renderArtifactAt(t, "/ui/artifacts/:id/html").Body.String()
	if !strings.Contains(body, `<figure class="pf-diagram"`) {
		t.Error("d2 fence did not compile to a figure — sanitize/compile order is wrong")
	}
	if !strings.Contains(body, "<svg") {
		t.Error("compiled diagram has no svg")
	}
}

// The /ui policy admits first-party assets, so it cannot be default-src 'none'. What it
// must still do is refuse every external origin and kill the object/base vectors.
func TestUIPageCSP_NoExternalOriginsAllowed(t *testing.T) {
	for _, want := range []string{
		"default-src 'none'",
		"script-src 'self' 'unsafe-inline'",
		"connect-src 'self'",
		"object-src 'none'",
		"base-uri 'none'",
		"frame-ancestors 'self'",
	} {
		if !strings.Contains(uiPageCSP, want) {
			t.Errorf("uiPageCSP missing %q: %s", want, uiPageCSP)
		}
	}
	// No scheme-wildcard or wildcard source may appear: those are how a policy quietly
	// stops constraining anything.
	for _, forbidden := range []string{" *", "http:", "https:", "data: *"} {
		if strings.Contains(uiPageCSP, forbidden) {
			t.Errorf("uiPageCSP admits %q, which defeats origin restriction: %s", forbidden, uiPageCSP)
		}
	}
}

func TestUISecurityHeaders_Middleware(t *testing.T) {
	e := echo.New()
	h := uiSecurityHeaders()(func(c echo.Context) error {
		return c.String(http.StatusOK, "ok")
	})
	req := httptest.NewRequest(http.MethodGet, "/ui/queue", nil)
	rec := httptest.NewRecorder()
	if err := h(e.NewContext(req, rec)); err != nil {
		t.Fatal(err)
	}
	if got := rec.Header().Get("Content-Security-Policy"); got != uiPageCSP {
		t.Errorf("CSP = %q, want uiPageCSP", got)
	}
	if rec.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Error("missing nosniff")
	}
	if rec.Header().Get("Referrer-Policy") != "no-referrer" {
		t.Error("missing Referrer-Policy")
	}
}

// The {{md}} helper is the second render path (memory + wi detail pages). It has the
// same sanitize/compile ordering as the artifact viewer, and until this test existed
// nothing checked it there — a mutation that flipped the order in ui_embed.go passed
// the whole suite, because the artifact-viewer ordering test exercises a different
// function entirely. aihub#231 was the same shape of gap: a second render path that
// nobody wired up to what the first one had.
// mdHelper resolves the {{md}} closure with its current signature.
func mdHelper(t *testing.T) func(src, origin, theme string) template.HTML {
	t.Helper()
	md, ok := uiFuncMap()["md"].(func(string, string, string) template.HTML)
	if !ok {
		t.Fatal("md not registered in uiFuncMap or wrong signature")
	}
	return md
}

// attrOf reads a double-quoted attribute value off the opening tag of frame.
func attrOf(frame, name string) (string, bool) {
	i := strings.Index(frame, " "+name+`="`)
	if i < 0 {
		return "", false
	}
	rest := frame[i+len(name)+3:]
	j := strings.Index(rest, `"`)
	if j < 0 {
		return "", false
	}
	return rest[:j], true
}

// innerDoc extracts the sandboxed document out of the srcdoc attribute {{md}} now emits.
//
// Assertions have to be made against this, not against the raw return value: everything
// inside srcdoc is HTML-attribute-escaped, so a naive strings.Contains for "<script" would
// report absence for a payload that is present and live once the browser parses it. Getting
// this wrong would make every test below vacuously pass.
func innerDoc(t *testing.T, frame string) string {
	t.Helper()
	i := strings.Index(frame, ` srcdoc="`)
	if i < 0 {
		t.Fatalf("no srcdoc in embed markup: %s", frame)
	}
	rest := frame[i+len(` srcdoc="`):]
	j := strings.Index(rest, `"`)
	if j < 0 {
		t.Fatalf("unterminated srcdoc: %s", frame)
	}
	return html.UnescapeString(rest[:j])
}

func TestUIFuncMap_MD_SanitizesBeforeCompilingD2(t *testing.T) {
	md := mdHelper(t)

	src := "```d2\na -> b: hello\n```\n"
	frame := string(md(src, "https://aihub.test", "auto"))
	doc := innerDoc(t, frame)

	if !strings.Contains(doc, "<svg") {
		t.Error("d2 fence did not compile inside the frame")
	}
	if strings.Contains(doc, `class="language-d2"`) {
		t.Error("d2 fence survived uncompiled")
	}
	// The ordering is what this test is for, and it is now load-bearing rather than a
	// preference: the sanitizer drops <style> outright, so compiling first would cost d2 its
	// whole stylesheet. Assert the theming arrived.
	for _, need := range []string{"<style", ".fill-", ".stroke-"} {
		if !strings.Contains(doc, need) {
			t.Errorf("compiled figure lost %q — sanitize must run before compiling, or the "+
				"figure renders with no fills or strokes", need)
		}
	}
}

// Agent markdown on the memory/wi detail pages is untrusted in exactly the same way as
// artifact content, and reaches an authed reader through a different handler.
func TestUIFuncMap_MD_SanitizesAgentPayload(t *testing.T) {
	md := mdHelper(t)

	frame := string(md("text\n\n<script>alert(1)</script>\n\n<p onclick=\"alert(2)\">kept</p>\n",
		"https://aihub.test", "auto"))
	doc := innerDoc(t, frame)

	for _, bad := range []string{"alert(1)", "alert(2)", "onclick"} {
		if strings.Contains(doc, bad) {
			t.Errorf("{{md}} path leaked %q into the frame document: %s", bad, doc)
		}
	}
	if !strings.Contains(doc, "kept") {
		t.Errorf("{{md}} dropped legitimate content: %s", doc)
	}
}

// TestUIFuncMap_MD_EmbedsInASandbox is the D3 wiring itself: this surface must no longer
// inline agent content into the page.
//
// Until this landed, SafeEmbedDocument had zero production callers — the isolation layer was
// built, tested, and protecting nothing, so the sanitizer was the only control on every
// authed render path. That is what made the two <style> bypasses severe rather than merely
// embarrassing.
func TestUIFuncMap_MD_EmbedsInASandbox(t *testing.T) {
	md := mdHelper(t)
	frame := string(md("# hi\n\ntext\n", "https://aihub.test", "dark"))

	if !strings.HasPrefix(frame, "<iframe") {
		t.Fatalf("{{md}} did not return an embedded frame: %s", frame)
	}
	// Read the sandbox ATTRIBUTE, not the markup as a string.
	//
	// A substring search is wrong twice over here, and this codebase has already been bitten
	// by both halves: it misses a privilege added elsewhere in the tag, and it false-positives
	// on the bridge script's own prose, which discusses allow-same-origin at length inside the
	// srcdoc. The attribute is the thing with security meaning.
	sandbox, ok := attrOf(frame, "sandbox")
	if !ok {
		t.Fatal("no sandbox attribute")
	}
	for _, tok := range strings.Fields(sandbox) {
		if tok == "allow-same-origin" {
			t.Fatal("allow-same-origin granted — sandbox plus same-origin is no isolation at all")
		}
	}
	if !strings.Contains(frame, `class="pf-embed"`) {
		t.Error("frame is missing the class embedframe.js identifies it by; its self-reported " +
			"height would never be applied and long documents would sit in a 220px box")
	}

	doc := innerDoc(t, frame)
	if !strings.Contains(doc, "Content-Security-Policy") {
		t.Error("inner document carries no CSP")
	}
	// Theme has to be stamped server-side: an opaque-origin frame cannot read the parent's
	// attribute or its cookie.
	if !strings.Contains(doc, `data-theme="dark"`) {
		t.Errorf("theme was not threaded into the frame: %s", doc)
	}
	// The bridge must be present, because it is what reports the height.
	if !strings.Contains(doc, "pf-annot-bridge") {
		t.Error("bridge script absent — nothing would report the document's height")
	}
	if !strings.Contains(doc, "https://aihub.test") {
		t.Error("parent origin was not threaded into the bridge; it fails closed and posts " +
			"nothing, so the height is never applied")
	}
}

// An unknown theme must not reach the document as an attribute value of its own.
func TestUIFuncMap_MD_ThemeIsNormalised(t *testing.T) {
	md := mdHelper(t)
	doc := innerDoc(t, string(md("x", "https://aihub.test", `"><script>alert(1)</script>`)))

	if strings.Contains(doc, "alert(1)") {
		t.Errorf("theme value was not normalised and broke out of the attribute: %s", doc)
	}
	if !strings.Contains(doc, `data-theme="auto"`) {
		t.Errorf("unrecognised theme should fall back to auto: %s", doc)
	}
}
