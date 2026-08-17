package server

import (
	"html"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/labstack/echo/v4"

	"github.com/GMISWE/ieops-aihub/internal/domain"
	"github.com/GMISWE/ieops-aihub/internal/render"
)

// The security headers on /ui are attached by middleware on the echo group, and until this
// file existed nothing proved the attachment. TestUISecurityHeaders_Middleware exercises
// uiSecurityHeaders() in isolation, which passes whether or not it is wired to anything.
//
// The gap was demonstrated, not theorised: deleting uiSecurityHeaders() from the group left
// the entire test suite green. That single-line deletion is aihub#144 verbatim — every authed
// /ui page served with no CSP — which is the defect this whole wi exists to close. A test of a
// security control that does not cover whether the control is installed reads as coverage it
// does not provide.

const testCookieSecret = "test-secret-must-be-long-enough-for-hmac-signing"

// registeredUI builds an echo instance with the real route table and returns a request helper.
//
// No session cookie and no database are needed, because uiSecurityHeaders() is attached BEFORE
// RequireUISession and therefore runs on every request to the group — including the
// unauthenticated one that gets redirected. That ordering is what makes this property testable
// at all: with the session check first, an earlier revision needed a seam in the
// identity-resolution path just so a test could reach the header middleware.
func registeredUI(t *testing.T) func(method, target string) *httptest.ResponseRecorder {
	t.Helper()
	e := echo.New()
	RegisterUIRoutes(e, nil, []byte(testCookieSecret))

	return func(method, target string) (rec *httptest.ResponseRecorder) {
		req := httptest.NewRequest(method, target, nil)
		rec = httptest.NewRecorder()
		// Named result plus recover: a handler reached with a nil pool may panic, and an
		// unnamed return would hand back nil after recovery, turning every assertion into a nil
		// dereference. Recovering here rather than installing echo's Recover middleware keeps
		// the chain under test the real one.
		defer func() { _ = recover() }()
		e.ServeHTTP(rec, req)
		return rec
	}
}

// TestUISecurityHeaders_AttachedToAuthedGroup is the test whose absence let the mutation
// through: a response from the /ui group must carry the policy.
func TestUISecurityHeaders_AttachedToAuthedGroup(t *testing.T) {
	do := registeredUI(t)

	// Several routes across different register* functions, because the group middleware is the
	// shared thing under test and a per-handler regression would look identical from any single
	// route.
	for _, target := range []string{"/ui/wi", "/ui/memories", "/ui/artifacts/mem_x/html"} {
		t.Run(target, func(t *testing.T) {
			rec := do(http.MethodGet, target)

			if got := rec.Header().Get("Content-Security-Policy"); got != uiPageCSP {
				t.Errorf("%s carries CSP %q, want uiPageCSP %q — if empty, uiSecurityHeaders() "+
					"is not attached to the /ui group and every page in it is served without a "+
					"policy (aihub#144)", target, got, uiPageCSP)
			}
			if got := rec.Header().Get("X-Content-Type-Options"); got != "nosniff" {
				t.Errorf("%s: nosniff = %q, want %q", target, got, "nosniff")
			}
			if got := rec.Header().Get("Referrer-Policy"); got != "no-referrer" {
				t.Errorf("%s: Referrer-Policy = %q, want %q", target, got, "no-referrer")
			}

			// Anti-vacuity. Without a session every one of these is a 302 to the login page, so
			// the assertions above could also be satisfied by a route that does not exist —
			// except that an unmatched route never enters the group and gets no headers at all.
			// Pin that we are observing the group's chain: the route matched AND the session
			// middleware ran.
			//
			// An earlier revision guarded this by making the request authed through a seam in the
			// identity path; the seam is gone and this is what remains observable without a
			// database. What is NOT covered here is an authed 200 — that needs a real session and
			// belongs to the T7 browser checklist. Stated so the coverage claim stays honest.
			if rec.Code != http.StatusFound {
				t.Fatalf("%s: status %d, want 302 — if this is 404 the route is not registered and "+
					"the header assertions above prove nothing about the group", target, rec.Code)
			}
			if loc := rec.Header().Get("Location"); !strings.HasPrefix(loc, "/ui/login") {
				t.Errorf("%s: redirect Location = %q, want the login page — the session middleware "+
					"is what proves the group chain ran", target, loc)
			}
		})
	}
}

// The login redirect used to be the one response in this group served bare.
//
// With the session check ahead of the header middleware, an unauthenticated request was
// short-circuited with a 302 before any header was written — so the request most likely to come
// from somewhere hostile got no CSP, no nosniff and no Referrer-Policy. Swapping the order
// fixed it; this pins the fix rather than trusting the ordering to stay put.
func TestUISecurityHeaders_OnUnauthenticatedRedirect(t *testing.T) {
	rec := registeredUI(t)(http.MethodGet, "/ui/wi")

	if rec.Code != http.StatusFound {
		t.Fatalf("expected a redirect to the login page without a session, got %d", rec.Code)
	}
	if got := rec.Header().Get("Content-Security-Policy"); got != uiPageCSP {
		t.Errorf("login redirect CSP = %q, want uiPageCSP", got)
	}
	if got := rec.Header().Get("X-Content-Type-Options"); got != "nosniff" {
		t.Errorf("login redirect nosniff = %q", got)
	}
}

// The unauthenticated entry points get the headers from a separate, explicit attachment
// (`sec` passed per-route). /ui/login is the one page that handles a credential, so it is the
// worst one to leave bare.
func TestUISecurityHeaders_AttachedToLoginPages(t *testing.T) {
	e := echo.New()
	RegisterUIRoutes(e, nil, []byte(testCookieSecret))

	req := httptest.NewRequest(http.MethodGet, "/ui/login", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("/ui/login status %d, want 200 (it must render without a DB)", rec.Code)
	}
	if got := rec.Header().Get("Content-Security-Policy"); got != uiPageCSP {
		t.Errorf("/ui/login CSP = %q, want uiPageCSP — the credential-handling page must not be "+
			"the one page without a policy", got)
	}
	if got := rec.Header().Get("X-Content-Type-Options"); got != "nosniff" {
		t.Errorf("/ui/login nosniff = %q", got)
	}
}

// TestInnerCSP_IsNoWiderThanThePage replaces a comment with a check.
//
// An `about:srcdoc` frame is a local scheme: it fetches no document of its own, so it inherits
// the policy container of the document that created it. The frame is therefore governed by the
// CONJUNCTION of uiPageCSP and its own <meta> policy, and safeembed.go carries a hand-written
// table of what that conjunction yields per directive.
//
// That table went stale within one revision — the inner img-src was widened and the table was
// not updated, leaving a security argument contradicting the code beside it. A table that has
// to be maintained by hand to stay true is the wrong artifact; this asserts the property the
// table was describing.
//
// The inner policy is read out of the document SafeEmbedDocument actually emits, so nothing
// test-only has to be exported from the render package for this to work.
func TestInnerCSP_IsNoWiderThanThePage(t *testing.T) {
	frame := render.SafeEmbedDocument("<p>x</p>", render.EmbedOptions{Title: "t"})
	inner := metaCSPOf(t, innerDoc(t, frame))

	page := parseCSP(uiPageCSP)
	for directive, innerSources := range parseCSP(inner) {
		pageSources, governed := page[directive]
		if !governed {
			// No directive of this name on the page, so default-src decides for it.
			pageSources = page["default-src"]
		}
		for _, src := range innerSources {
			if !cspSourceAdmitted(pageSources, src) {
				t.Errorf("inner policy allows %s %q which the page policy does not — the frame "+
					"inherits the page's policy container, so this source is unreachable and the "+
					"inner policy claims protection it does not deliver (page: %v)",
					directive, src, pageSources)
			}
		}
	}
}

// metaCSPOf extracts the <meta http-equiv="Content-Security-Policy"> content from a document.
func metaCSPOf(t *testing.T, doc string) string {
	t.Helper()
	const marker = `<meta http-equiv="Content-Security-Policy" content="`
	i := strings.Index(doc, marker)
	if i < 0 {
		t.Fatalf("no meta CSP in inner document: %.400s", doc)
	}
	rest := doc[i+len(marker):]
	j := strings.Index(rest, `"`)
	if j < 0 {
		t.Fatal("unterminated meta CSP")
	}
	// Unescape one more level. innerDoc removed the srcdoc attribute's escaping; what is left is
	// the inner document's own attribute escaping, so the policy still reads
	// `default-src &#39;none&#39;` at this point. Parsing that directly splits directives inside
	// the &#39; entity and compares garbage against garbage — which is how a first version of
	// this test "found" seven violations that did not exist.
	return html.UnescapeString(rest[:j])
}

func parseCSP(policy string) map[string][]string {
	out := map[string][]string{}
	for _, part := range strings.Split(policy, ";") {
		fields := strings.Fields(part)
		if len(fields) == 0 {
			continue
		}
		out[strings.ToLower(fields[0])] = fields[1:]
	}
	return out
}

// cspSourceAdmitted reports whether the page's source list admits an inner source.
//
// The nonce rule is the whole point of this function and the first version got it backwards.
// It admitted an inner nonce whenever the page carried ANY nonce, on the reasoning that "the
// page permits nonced script at all". That is not the property. The two policies are enforced
// independently: the frame's <script nonce="N"> must satisfy the inherited page policy too, and
// a page source of 'nonce-R' with R != N does not match it. Written the loose way, the test
// passed under exactly the change its own comment promised to catch — aihub#243 swapping
// 'unsafe-inline' for a per-response page nonce — which is worse than no test, because the
// #243 implementer is told a green suite will warn them.
//
// So: an inner nonce is admitted only by page 'unsafe-inline', and only when the page policy
// carries no nonce or hash of its own — a nonce or hash in a policy makes 'unsafe-inline' inert
// within that policy, which is a per-policy rule and does not cross between the two.
func cspSourceAdmitted(pageSources []string, innerSource string) bool {
	// 'none' allows nothing, so it never needs page permission.
	if innerSource == "'none'" {
		return true
	}
	pageHasNonceOrHash := false
	for _, p := range pageSources {
		if strings.HasPrefix(p, "'nonce-") || strings.HasPrefix(p, "'sha256-") ||
			strings.HasPrefix(p, "'sha384-") || strings.HasPrefix(p, "'sha512-") {
			pageHasNonceOrHash = true
		}
	}
	for _, p := range pageSources {
		switch {
		case p == innerSource, p == "*":
			return true
		case strings.HasPrefix(innerSource, "'nonce-"):
			// Only unsafe-inline admits a DIFFERENT party's nonce, and only while the page policy
			// has not neutered unsafe-inline with a nonce/hash of its own.
			if p == "'unsafe-inline'" && !pageHasNonceOrHash {
				return true
			}
		}
	}
	return false
}

// TestCSPSourceAdmitted_NonceSemantics pins the rule above directly, because the property is
// easy to state and easy to get backwards, and because TestInnerCSP_IsNoWiderThanThePage only
// exercises whichever combination uiPageCSP happens to carry today.
func TestCSPSourceAdmitted_NonceSemantics(t *testing.T) {
	cases := []struct {
		name  string
		page  []string
		inner string
		want  bool
	}{
		{"unsafe-inline admits the frame's nonce", []string{"'self'", "'unsafe-inline'"}, "'nonce-N'", true},
		{"a DIFFERENT page nonce does not (aihub#243)", []string{"'self'", "'nonce-R'"}, "'nonce-N'", false},
		{"the same nonce does", []string{"'self'", "'nonce-N'"}, "'nonce-N'", true},
		{"a page nonce neuters unsafe-inline", []string{"'unsafe-inline'", "'nonce-R'"}, "'nonce-N'", false},
		{"a page hash neuters unsafe-inline too", []string{"'unsafe-inline'", "'sha256-abc'"}, "'nonce-N'", false},
		{"'none' inner never needs permission", []string{"'self'"}, "'none'", true},
		{"exact match", []string{"data:"}, "data:", true},
		{"unrelated source", []string{"'self'", "data:"}, "https://cdn.example", false},
	}
	for _, tc := range cases {
		if got := cspSourceAdmitted(tc.page, tc.inner); got != tc.want {
			t.Errorf("%s: cspSourceAdmitted(%v, %q) = %v, want %v", tc.name, tc.page, tc.inner, got, tc.want)
		}
	}
}

// TestInnerCSP_SurvivesTheAihub243Switch is the scenario the conjunction check exists for.
//
// It does not assert that the switch is safe — it is not. It asserts that the check NOTICES,
// so that whoever implements aihub#243 finds out from a red test rather than from a user
// reporting that every embedded document is 220 pixels tall. Verified against a real browser:
// with a page nonce R and a frame nonce N, Chromium refuses the frame's bridge outright.
func TestInnerCSP_SurvivesTheAihub243Switch(t *testing.T) {
	frame := render.SafeEmbedDocument("<p>x</p>", render.EmbedOptions{
		Title:        "t",
		BridgeScript: render.AnnotationBridgeFor("https://aihub.test"),
	})
	inner := parseCSP(metaCSPOf(t, innerDoc(t, frame)))
	innerScript := inner["script-src"]
	if len(innerScript) == 0 || !strings.HasPrefix(innerScript[0], "'nonce-") {
		t.Fatalf("expected the frame to carry a nonced script-src, got %v", innerScript)
	}

	// Today's page policy admits it.
	if !cspSourceAdmitted(parseCSP(uiPageCSP)["script-src"], innerScript[0]) {
		t.Error("today's uiPageCSP should admit the frame's nonced bridge")
	}

	// aihub#243's page policy does not — and that must be visible here.
	after := strings.Replace(uiPageCSP, "script-src 'self' 'unsafe-inline'",
		"script-src 'self' 'nonce-PAGEVALUE'", 1)
	if after == uiPageCSP {
		t.Fatal("uiPageCSP no longer matches the string aihub#243 is expected to replace; " +
			"re-derive this scenario against the current policy")
	}
	if cspSourceAdmitted(parseCSP(after)["script-src"], innerScript[0]) {
		t.Error("the conjunction check does not notice aihub#243's nonce switch. The frame's " +
			"bridge would be refused by the inherited policy, the height protocol would stop " +
			"silently, and this test — whose whole purpose is to warn about that — would be green")
	}
}

// TestEmbedFrameJS_IsShippedAndReferenced covers the other mutation-verified gap: deleting
// internal/server/static/embedframe.js entirely left `go build ./...` and the whole suite green,
// because //go:embed static embeds a tree and a missing file inside it is not an error. The
// consequence is silent — layout.html.tmpl 404s the script and every embedded document on
// /ui/wi/:id and /ui/memories/:id stays at its 220px starting height with an inner scrollbar.
func TestEmbedFrameJS_IsShippedAndReferenced(t *testing.T) {
	js, err := staticFS.ReadFile("static/embedframe.js")
	if err != nil {
		t.Fatalf("static/embedframe.js must be embedded: %v", err)
	}
	layout, err := templateFS.ReadFile("templates/layout.html.tmpl")
	if err != nil {
		t.Fatalf("read layout template: %v", err)
	}
	if !strings.Contains(string(layout), "/ui/static/embedframe.js") {
		t.Error("layout.html.tmpl must load /ui/static/embedframe.js — without it no embedded " +
			"document is ever resized and long documents sit in a 220px scrolling box")
	}

	// The script is parent-side privileged code: it runs in the authed /ui origin and writes to
	// a frame's style. Assert the validation it is required to perform survives edits.
	src := string(js)
	for _, want := range []struct{ needle, why string }{
		{"'pf-annot-bridge'", "must check the protocol's source discriminator"},
		{"PROTOCOL_VERSION", "must pin the protocol version"},
		{"'height'", "must handle only the height message; the annotation types are aihub#245"},
		{"isFinite", "must reject NaN and Infinity, which are both typeof 'number'"},
		{"contentWindow === ", "must authenticate by ev.source identity, not by ev.origin — a " +
			"sandboxed srcdoc frame has an opaque origin and reports origin \"null\""},
		{"MAX_HEIGHT", "must clamp the reported height"},
		{"MIN_HEIGHT", "must clamp the reported height"},
	} {
		if !strings.Contains(src, want.needle) {
			t.Errorf("embedframe.js lost %q: it %s", want.needle, want.why)
		}
	}

	// The annotation half must stay unimplemented rather than half-implemented: a partial
	// handler drops reviewer comments silently. If these appear, aihub#245 has started and this
	// guard should move rather than be deleted.
	//
	// Matched against COMMENT-STRIPPED source, and on dispatch shapes rather than bare strings.
	// The first version searched the raw file for "'highlight'" and failed on the file's own
	// comment explaining which types it deliberately does not handle — the same false-positive
	// class as searching markup for "allow-same-origin" and hitting the bridge script's prose
	// about it. Both the if-chain and the switch shape are checked, because a source-level
	// guard that knows one spelling is a guard against one spelling.
	code := stripJSLineComments(src)
	for _, absent := range []string{"highlight", "selected", "clear"} {
		for _, shape := range []string{
			"d.type === '" + absent + "'",
			"data.type === '" + absent + "'",
			"case '" + absent + "'",
		} {
			if strings.Contains(code, shape) {
				t.Errorf("embedframe.js dispatches on %q (%q) — the annotation protocol belongs "+
					"to aihub#245 and must be wired together with its anchoring, not piecemeal",
					absent, shape)
			}
		}
	}
}

// stripJSLineComments removes // comments so source assertions match code, not prose.
// Deliberately simple: it is enough for this file, and a real JS parser here would be a
// dependency added to avoid one edge case that does not occur.
func stripJSLineComments(src string) string {
	var b strings.Builder
	for _, line := range strings.Split(src, "\n") {
		if i := strings.Index(line, "//"); i >= 0 {
			line = line[:i]
		}
		b.WriteString(line)
		b.WriteByte('\n')
	}
	return b.String()
}

// aihub#240 D7: the artifact viewer must put agent-AUTHORED html inside the sandbox.
//
// This is the wiring guard the first review round said was missing everywhere else:
// dropping the sandboxBody gate leaves every other test green, because every other test
// asserts on document content that is present either way. These assertions fail closed.
func TestArtifactViewer_D7_AgentAuthoredHTMLIsFramed(t *testing.T) {
	defer withVersionChainOverride()()

	renderAt := func(t *testing.T, mem *domain.Memory, path string) string {
		t.Helper()
		defer withLoadMemoryOverride(mem, nil)()
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
		return rec.Body.String()
	}

	archDoc := func() *domain.Memory {
		m := publicSharedMem()
		m.Type = "fact.architecture"
		m.Content = "# Arch\n\nbody"
		m.RenderedHTML = htmlPtr(`<h1 id="arch">Arch</h1>` +
			`<pre><code class="language-d2">a -&gt; b</code></pre>`)
		return m
	}

	ui := renderAt(t, archDoc(), "/ui/artifacts/:id/html")

	// 1. It is framed at all.
	if !strings.Contains(ui, "<iframe") || !strings.Contains(ui, "srcdoc=") {
		t.Fatal("agent-authored architecture doc must be served inside an <iframe srcdoc>")
	}
	// 2. The sandbox token set is exactly the isolating one. allow-same-origin together
	//    with allow-scripts voids the sandbox entirely, so this is the load-bearing check.
	//    Read the attribute itself rather than scanning the page: the bridge script's own
	//    commentary mentions allow-same-origin, so a substring check over the whole
	//    response reports a violation that is not there.
	sbAt := strings.Index(ui, `sandbox="`)
	if sbAt < 0 {
		t.Fatal("frame has no sandbox attribute at all")
	}
	sbVal := ui[sbAt+len(`sandbox="`):]
	sbVal = sbVal[:strings.Index(sbVal, `"`)]
	if sbVal != "allow-scripts" {
		t.Errorf("sandbox token set must be exactly \"allow-scripts\", got %q "+
			"(allow-same-origin alongside allow-scripts voids the sandbox)", sbVal)
	}
	// 3. The d2 fence was compiled INSIDE the frame and kept d2's stylesheet. This pins
	//    SafeEmbedDocument's sanitize→compile order: compiling before sanitizing would
	//    strip <style> and the figure would arrive unpainted (D2/D6).
	if !strings.Contains(ui, "&lt;svg") {
		t.Error("d2 fence should have compiled to inline SVG inside the frame")
	}
	//    Anchor the stylesheet check INSIDE the compiled figure: the frame ships its own
	//    document <style> too, so an unanchored search passes even with d2 stripped bare.
	if svgAt := strings.Index(ui, "&lt;svg"); svgAt < 0 {
		t.Error("no compiled SVG to inspect")
	} else if !strings.Contains(ui[svgAt:], ".fill-") {
		t.Error("compiled figure lost d2's fill/stroke classes — sanitize/compile order regressed")
	}
	if strings.Contains(ui, "language-d2") {
		t.Error("d2 fence survived uncompiled inside the frame")
	}
	// 4. The body must not ALSO be inlined into the parent page.
	if strings.Contains(ui, `<h1 id="arch">`) {
		t.Error("body is inlined in the parent page as well as framed")
	}

	// 5. /v1 is untouched: no frame, and the authored bytes pass through verbatim.
	v1 := renderAt(t, archDoc(), "/v1/artifacts/:id/html")
	if strings.Contains(v1, "<iframe") {
		t.Error("/v1 must not be framed — byte purity with /share")
	}
	if !strings.Contains(v1, `<h1 id="arch">Arch</h1>`) {
		t.Error("/v1 must serve the authored body verbatim")
	}

	// 6. No regression for the existing artifact types: spec/plan stay inlined.
	spec := publicSharedMem()
	spec.Type = "methodology.spec"
	spec.Content = "# S\n\nbody"
	spec.RenderedHTML = htmlPtr(`<h1 id="s">S</h1><p>body</p>`)
	if out := renderAt(t, spec, "/ui/artifacts/:id/html"); strings.Contains(out, "srcdoc=") {
		t.Error("methodology.spec must keep its inlined viewer (annotations still work there)")
	}
}
