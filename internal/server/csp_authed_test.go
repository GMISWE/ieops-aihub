package server

import (
	"context"
	"html"
	"html/template"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/labstack/echo/v4"
	htmlparse "golang.org/x/net/html"

	"github.com/GMISWE/ieops-aihub/internal/domain"
	"github.com/GMISWE/ieops-aihub/internal/render"
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
// as a CSS body and spliced it back past the policy. When this was written the page policy
// still carried script-src 'unsafe-inline', so the second layer did not stop it either.
// aihub#243 has since replaced that with a per-response nonce, which would now refuse the
// spliced script — but this stays asserted on the response bytes, because sanitization is the
// control that must hold on its own and CSP is the layer behind it, not the other way round.
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
	policy := uiPageCSPWithNonce("TESTNONCE")
	for _, want := range []string{
		"default-src 'none'",
		// A nonce, not 'unsafe-inline' (aihub#243): an inline script must name this
		// response's value to run, so agent content cannot execute even if it survives
		// the sanitizer.
		"script-src 'self' 'nonce-TESTNONCE'",
		"connect-src 'self'",
		"object-src 'none'",
		"base-uri 'none'",
		"frame-ancestors 'self'",
	} {
		if !strings.Contains(policy, want) {
			t.Errorf("policy missing %q: %s", want, policy)
		}
	}
	// 'unsafe-inline' must not creep back into script-src. A browser ignores it once a nonce
	// is present, so re-adding it would be inert on modern engines and a hole on old ones —
	// and it would make the policy read as though inline script were allowed.
	for _, src := range parseCSP(policy)["script-src"] {
		if src == "'unsafe-inline'" {
			t.Errorf("script-src admits 'unsafe-inline' again, undoing aihub#243: %s", policy)
		}
	}
	// No scheme-wildcard or wildcard source may appear: those are how a policy quietly
	// stops constraining anything.
	for _, forbidden := range []string{" *", "http:", "https:", "data: *"} {
		if strings.Contains(policy, forbidden) {
			t.Errorf("policy admits %q, which defeats origin restriction: %s", forbidden, policy)
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
	assertPageCSP(t, rec.Header().Get("Content-Security-Policy"), "uiSecurityHeaders")
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
// inlineHandlerRe matches an on*= event-handler attribute inside an opening tag.
//
// Anchored on "<" + non-">" so it cannot fire on prose that merely contains the text; the
// leading \s stops it matching the tail of an ordinary attribute name (content=, action=).
var inlineHandlerRe = regexp.MustCompile(`<[^>]*\s(on[a-z]+)\s*=`)

// TestUIPage_EveryExecutableInlineScriptCarriesTheNonce closes the gap that mutation testing
// found and nothing else covered.
//
// Removing the nonce attribute from the side-rail script left the ENTIRE suite green, which is
// the same silent-failure shape aihub#243 exists to eliminate: under a nonce policy an inline
// script without the attribute is simply refused, so the theme setter stops resolving
// data-theme (a flash of the wrong palette, or the wrong palette outright) and the side-rail
// scroll-spy and comment jumps quietly stop working. Nothing errors server-side, the page
// still renders, and no assertion anywhere notices.
//
// So this walks the real response bytes and requires every <script> that the browser would
// EXECUTE to name the nonce. Two kinds are exempt and both are checked for explicitly rather
// than skipped loosely:
//   - <script src="..."> — an external first-party asset, admitted by 'self'. A nonce would be
//     harmless but is not required, and none of them carry one today.
//   - <script type="application/json"> — the annotation data island. It is data, not code; the
//     browser never executes it and script-src does not govern it.
func TestUIPage_EveryExecutableInlineScriptCarriesTheNonce(t *testing.T) {
	// Both artifact layouts, because the theme setter is emitted from two DIFFERENT branches
	// (review pages add the pf-review-page class, spec/plan pages add pf-annot-active) and a
	// single fixture only ever exercises one of them. Mutation testing found exactly this:
	// stripping the nonce from the review branch left the suite green while a spec fixture
	// was the only thing under test.
	total := 0
	for _, memType := range []string{"methodology.spec", "methodology.review"} {
		t.Run(memType, func(t *testing.T) {
			mem := publicSharedMem()
			mem.Type = memType
			mem.WorkItemID = strptr("aihub#243")
			mem.Content = "# Heading one\n\ntext\n\n## Heading two\n\nmore text\n"
			// id-BEARING headings, deliberately. buildAnnotationHTML derives its section
			// dropdown from headings in the rendered HTML, so the default fixture's lone
			// id-less <h1> makes it return "" — and then the annotation form, which is the
			// surface that carried the aihub#243 inline-handler bug, never renders and this
			// scan silently checks nothing. That vacuity is exactly how the bug reached review.
			mem.RenderedHTML = htmlPtr(
				`<h1 id="intro">Intro</h1><p>x</p><h2 id="design">Design</h2><p>y</p>`)
			defer withLoadMemoryOverride(mem, nil)()

			// The side rail reaches for the version chain, which needs a pool this test
			// does not have.
			prevVCF := versionChainFn
			versionChainFn = func(_ context.Context, _ *pgxpool.Pool, _ string) ([]domain.MemoryVersionRef, error) {
				return nil, nil
			}
			defer func() { versionChainFn = prevVCF }()

			e := echo.New()
			c, rec := newUIContext(e, http.MethodGet, "/ui/artifacts/:id/html", "mem_share1")
			c.SetPath("/ui/artifacts/:id/html")
			setUser(c, authorUser())
			// Through the real middleware, because that is what mints the nonce and publishes
			// it in the header. Calling the handler bare would hand every script an empty
			// nonce and the assertions below would compare "" against "" and pass on a
			// broken page.
			h := uiSecurityHeaders()(handleArtifactHTML(nil))
			if err := h(c); err != nil {
				e.HTTPErrorHandler(err, c)
			}
			if rec.Code != http.StatusOK {
				t.Fatalf("status %d", rec.Code)
			}
			nonce := assertPageCSP(t, rec.Header().Get("Content-Security-Policy"), memType)

			body := rec.Body.String()
			var executable, external, data int
			for i := 0; ; {
				j := strings.Index(body[i:], "<script")
				if j < 0 {
					break
				}
				start := i + j
				end := strings.Index(body[start:], ">")
				if end < 0 {
					t.Fatalf("unterminated <script tag at %d", start)
				}
				tag := body[start : start+end+1]
				i = start + end + 1

				switch {
				case strings.Contains(tag, " src="):
					external++
				case strings.Contains(tag, `type="application/json"`):
					data++
				default:
					executable++
					if !strings.Contains(tag, `nonce="`+nonce+`"`) {
						t.Errorf("executable inline script is not nonced, so the page policy "+
							"will refuse to run it and whatever it does will silently stop "+
							"working:\n  %s", tag)
					}
				}
			}

			// aihub#243 review finding: scanning <script> tags ALONE is not enough, and this
			// test passing while the page shipped a broken handler is the proof. A nonce
			// authorises script ELEMENTS; script-src-attr falls back to script-src, which
			// admits an event-handler ATTRIBUTE only via 'unsafe-inline'/'unsafe-hashes'.
			// Neither is in this policy, so ANY on*= attribute on a /ui page is dead code that
			// fails on interaction rather than on load — invisible to a load-time probe.
			for _, m := range inlineHandlerRe.FindAllStringSubmatch(body, -1) {
				t.Errorf("inline event handler %s= on the %s layout: a CSP nonce cannot "+
					"authorise handler attributes, so this is refused at interaction time and "+
					"whatever it does silently stops working. Move it into a nonced <script>.\n  %s",
					m[1], memType, strings.TrimSpace(m[0]))
			}

			// The annotation form is only built for the non-review layout; when it is there,
			// its nonced mirror script must have been among the scripts walked above.
			if memType != "methodology.review" && !strings.Contains(body, `id="pf-annot-heading"`) {
				t.Error("the annotation section dropdown did not render, so neither the handler " +
					"scan nor the script scan covered it — fix the fixture, not the assertion")
			}

			// Anti-vacuity per layout: each one must at least emit its theme setter.
			if executable < 1 {
				t.Errorf("no executable inline scripts on the %s layout — has the fixture "+
					"stopped exercising the theme setter?", memType)
			}
			total += executable
			t.Logf("%s: %d executable inline, %d external, %d data-island", memType, executable, external, data)
		})
	}

	// Across both layouts the known sites (two theme-setter branches, the side rail, and the
	// annotation form's select mirror) must all have been walked.
	if total < 3 {
		t.Errorf("only %d executable inline scripts across both layouts; expected at least 3 "+
			"(theme setter x2 branches + side rail) — coverage has regressed", total)
	}
}

// TestArtifactViewer_SandboxedFrameRunsOnThePageNonce covers the third SafeEmbedDocument call
// site — the artifact viewer's own frame, reached only by fact.architecture artifacts that
// carry stored rendered HTML.
//
// The {{md}} / {{agentdoc}} helpers on the detail pages are covered by
// TestUIFuncMap_FramesRunOnThePageNonce, but this one lives in a different file and a
// different branch, and mutation testing confirmed nothing else notices when its Nonce field
// is removed: the frame then mints its own value, the inherited page policy refuses the
// bridge, and the diagram sits at 220px with an inner scrollbar (aihub#243).
func TestArtifactViewer_SandboxedFrameRunsOnThePageNonce(t *testing.T) {
	mem := publicSharedMem()
	mem.Type = "fact.architecture"
	mem.RenderedHTML = htmlPtr(`<h1>arch</h1><p>body</p>`)
	defer withLoadMemoryOverride(mem, nil)()

	prevVCF := versionChainFn
	versionChainFn = func(_ context.Context, _ *pgxpool.Pool, _ string) ([]domain.MemoryVersionRef, error) {
		return nil, nil
	}
	defer func() { versionChainFn = prevVCF }()

	e := echo.New()
	c, rec := newUIContext(e, http.MethodGet, "/ui/artifacts/:id/html", "mem_share1")
	c.SetPath("/ui/artifacts/:id/html")
	setUser(c, authorUser())
	h := uiSecurityHeaders()(handleArtifactHTML(nil))
	if err := h(c); err != nil {
		e.HTTPErrorHandler(err, c)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	nonce := assertPageCSP(t, rec.Header().Get("Content-Security-Policy"), "artifact viewer")

	body := rec.Body.String()
	if !strings.Contains(body, "<iframe") {
		t.Fatal("fact.architecture body was not framed — this test no longer covers the " +
			"sandboxed viewer path and the mutation it guards would go unnoticed")
	}
	// Two levels of escaping: the srcdoc attribute's, then the inner document's own.
	doc := html.UnescapeString(html.UnescapeString(body))
	if !strings.Contains(doc, "script-src 'nonce-"+nonce+"'") {
		t.Errorf("the viewer's frame does not run on the page nonce %q — the inherited page "+
			"policy will refuse its bridge and the document will stay 220px tall", nonce)
	}
	if !strings.Contains(doc, `<script nonce="`+nonce+`">`) {
		t.Error("the frame's bridge script is not tagged with the page nonce")
	}
}

// mdTestNonce is the page nonce the {{md}} / {{agentdoc}} helpers are handed in tests.
const mdTestNonce = "MDTESTNONCE"

// mdHelper resolves the {{md}} closure and binds the nonce argument, so the callers below —
// which care about sanitization and diagram ordering, not CSP — keep reading in terms of the
// three arguments they actually vary. TestUIFuncMap_FramesRunOnThePageNonce covers the bound
// one.
func mdHelper(t *testing.T) func(src, origin, theme string) template.HTML {
	t.Helper()
	md, ok := uiFuncMap()["md"].(func(string, string, string, string) template.HTML)
	if !ok {
		t.Fatal("md not registered in uiFuncMap or wrong signature")
	}
	return func(src, origin, theme string) template.HTML {
		return md(src, origin, theme, mdTestNonce)
	}
}

// TestUIFuncMap_FramesRunOnThePageNonce covers the wiring TestInnerCSP_FrameBridgeRunsOnThePageNonce
// cannot see.
//
// That test proves render.SafeEmbedDocument does the right thing when it is GIVEN the page
// nonce. This one proves the two /ui template helpers actually give it one. Drop the Nonce
// field from either closure and the frame mints its own value, the inherited page policy
// refuses the bridge, and every embedded document on the wi and memory detail pages silently
// stops reporting its height and sits at 220px (aihub#243).
func TestUIFuncMap_FramesRunOnThePageNonce(t *testing.T) {
	agentdoc, ok := uiFuncMap()["agentdoc"].(func(string, string, string, string) template.HTML)
	if !ok {
		t.Fatal("agentdoc not registered in uiFuncMap or wrong signature")
	}

	for _, tc := range []struct {
		name  string
		frame string
	}{
		{"md", string(mdHelper(t)("hello", "https://aihub.test", "dark"))},
		{"agentdoc", string(agentdoc("<p>hello</p>", "https://aihub.test", "dark", mdTestNonce))},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// Two levels of escaping to undo: the srcdoc attribute's, and then the inner
			// document's own attribute escaping. Stopping after one leaves the policy
			// reading `script-src &#39;nonce-...&#39;` and the assertion below never matches.
			doc := html.UnescapeString(html.UnescapeString(tc.frame))
			if !strings.Contains(doc, "script-src 'nonce-"+mdTestNonce+"'") {
				t.Errorf("frame CSP does not run on the page nonce %q — the inherited page "+
					"policy will refuse the bridge and the frame will stay 220px tall\nframe: %.600s",
					mdTestNonce, doc)
			}
			if !strings.Contains(doc, `<script nonce="`+mdTestNonce+`">`) {
				t.Errorf("bridge script is not tagged with the page nonce\nframe: %.600s", doc)
			}
		})
	}
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

// TestArtifactUI_AnnotationIslandCannotBeClobbered pins the server-side half of a DOM
// clobbering fix.
//
// The viewer inlines the sanitized agent body BEFORE its own chrome, and the sanitizer allows
// `id` globally — it has to, because d2 figures reference their own gradients and clip paths by
// fragment. getElementById returns the first element in document order with a given id
// regardless of tag, so an artifact containing <div id="pf-annot-data">{"mem_id":"…"}</div> was
// read by annot.js instead of the real island, and payload.mem_id decides the reply/resolve POST
// targets. An artifact author could choose which artifact a reviewer's comment landed on.
//
// annot.js now selects `script#pf-annot-data[type="application/json"]`. That is unforgeable
// only if two things hold on this side, and both are asserted here rather than assumed:
// the real island really is a <script type="application/json">, and agent content cannot
// produce one. The behavioural half (that the browser picks ours) belongs to the T7 checklist.
func TestArtifactUI_AnnotationIslandCannotBeClobbered(t *testing.T) {
	defer withVersionChainOverride()()
	mem := publicSharedMem()
	mem.WorkItemID = strptr("aihub#240")
	// A heading WITH AN id is required for the annotation chrome to exist at all:
	// extractHeadingsFromHTML skips headings without one, and buildAnnotationHTML returns ""
	// when there are no commits and no headings. goldmark emits those ids automatically; a
	// hand-written rendered_html has to include them or the island is never built and this
	// test passes for the wrong reason.
	mem.RenderedHTML = htmlPtr(
		`<h1>t</h1><h2 id="section">Section</h2><p>keep</p>` +
			// Vector 1: same id, different tag, emitted before our chrome.
			`<div id="pf-annot-data">{"mem_id":"mem_attacker","commits":[]}</div>` +
			// Vector 2: forge the real thing outright. The sanitizer drops <script>.
			`<script type="application/json" id="pf-annot-data">{"mem_id":"mem_attacker"}</script>` +
			// Vector 3 — the one that actually defeated the first version of this fix. A d2 fence
			// survives sanitization as inert markup and is compiled AFTERWARDS, and d2's |md block
			// emits caller text as real markup inside <foreignObject>. That produced a genuine
			// <script id="pf-annot-data" type="application/json"> ahead of the server's, with the
			// attributes in the opposite order to the literal this test used to count.
			`<pre><code class="language-d2">x: |md &lt;script id=&#34;pf-annot-data&#34; ` +
			`type=&#34;application/json&#34;&gt;{&#34;mem_id&#34;:&#34;mem_attacker&#34;}` +
			`&lt;/script&gt; |</code></pre>`)
	defer withLoadMemoryOverride(mem, nil)()

	body := renderArtifactAt(t, "/ui/artifacts/:id/html").Body.String()

	// 1. Exactly one element matches the selector annot.js uses — evaluated with DOM semantics,
	//    not by counting a literal string.
	//
	//    Counting `<script type="application/json" id="pf-annot-data">` was how the first version
	//    of this guard worked, and it was blind: querySelector does not care about attribute
	//    order, and the d2 compile path emitted `<script id="…" type="…">`, so the literal count
	//    was 1 while a forged island preceded the real one in document order. The test passed
	//    with the clobber live. Anything asserting a DOM property has to be evaluated as a DOM.
	matches := islandCandidates(t, body)
	if len(matches) != 1 {
		t.Errorf("%d elements match script#pf-annot-data[type=\"application/json\"], want exactly "+
			"1 (the server's). Matches in document order: %v", len(matches), matches)
	}
	if len(matches) > 0 && strings.Contains(matches[0], "mem_attacker") {
		t.Errorf("the FIRST match in document order is the agent's — that is the one "+
			"querySelector returns: %s", matches[0])
	}

	// 2. No <script> anywhere in the response carries the attacker's payload. This is the
	//    assertion that would fail if <script> were ever added to the element allowlist.
	for i := 0; i < len(body); {
		j := strings.Index(body[i:], "<script")
		if j < 0 {
			break
		}
		i += j
		end := strings.Index(body[i:], "</script>")
		if end < 0 {
			end = len(body) - i
		}
		if strings.Contains(body[i:i+end], "mem_attacker") {
			t.Errorf("a <script> element carries the agent's payload:\n%.300s", body[i:i+end])
		}
		i += end
	}

	// 3. The lookup in annot.js must stay qualified. A revert to getElementById is invisible
	//    from the server side, and it is the whole fix.
	js := string(render.AnnotJS())
	if !strings.Contains(js, `querySelector('script#pf-annot-data[type="application/json"]')`) {
		t.Error("annot.js no longer uses the type-qualified selector for the data island; " +
			"getElementById returns the first element with that id regardless of tag, and the " +
			"agent body is emitted before this chrome (aihub#240)")
	}
	if !strings.Contains(body, "keep") {
		t.Error("legitimate content was lost")
	}
}

// islandCandidates returns the text of every element matching
// `script#pf-annot-data[type="application/json"]`, in document order, evaluated by parsing the
// response rather than by matching bytes.
//
// x/net/html is already an indirect dependency of this module (bluemonday uses it), so this adds
// no dependency and gets real tokenization: attribute order, quoting style, whitespace and case
// all stop mattering, which is the whole point — every one of those is a way a byte-level guard
// misses a DOM-level property.
func islandCandidates(t *testing.T, body string) []string {
	t.Helper()
	doc, err := htmlparse.Parse(strings.NewReader(body))
	if err != nil {
		t.Fatalf("parse response: %v", err)
	}
	var out []string
	var walk func(*htmlparse.Node)
	walk = func(n *htmlparse.Node) {
		if n.Type == htmlparse.ElementNode && n.Data == "script" {
			var id, typ string
			for _, a := range n.Attr {
				switch strings.ToLower(a.Key) {
				case "id":
					id = a.Val
				case "type":
					typ = a.Val
				}
			}
			if id == "pf-annot-data" && typ == "application/json" {
				var text strings.Builder
				for c := n.FirstChild; c != nil; c = c.NextSibling {
					if c.Type == htmlparse.TextNode {
						text.WriteString(c.Data)
					}
				}
				out = append(out, text.String())
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(doc)
	return out
}
