package server

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/labstack/echo/v4"
	htmlparse "golang.org/x/net/html"

	"github.com/GMISWE/ieops-aihub/internal/render"
)

// The post-sanitization insertion boundary.
//
// # The invariant
//
// The /ui artifact viewer serves a document assembled from several sources. Exactly one of them
// has been through SanitizeArtifactHTML; everything appended afterwards is described in the code
// as "first-party". That description is about the CODE, and it is the wrong test. The invariant
// this file enforces is about the DATA:
//
//	Anything inserted into the response after SanitizeArtifactHTML has run must carry only data
//	that has itself been validated against a closed vocabulary, escaped for the context it lands
//	in, or serialised with a channel-appropriate encoder. "We wrote the code that emits it" is
//	not one of those three.
//
// The distinction is not academic — it is the difference between four rounds of review finding
// four variants of the same defect and none. Every insertion point below is first-party code.
// One of them was, for three revisions, first-party code over agent-controlled input:
//
//	insertion point       data                                     discipline
//	────────────────────  ───────────────────────────────────────  ──────────────────────────────
//	uiHead                theme, from a cookie                     closed vocabulary
//	                                                               (light|dark|auto, else auto)
//	share control         shareURL, built from the Host header     html.EscapeString
//	annotation chrome     memory id, commit bodies and authors,    html.EscapeString per field;
//	                      quotes, heading text                     the JSON island via
//	                                                               escapeJSONForScriptTag
//	compiled d2 figures   the agent's fence body, verbatim         RenderDiagramsGated
//	                                                               (before: nothing)
//
// # What this file does
//
// It drives hostile data through each insertion point via the real handler and asserts the
// response carries no live construct. Enumerating the points in a comment is what the code did
// before; the failure mode of a comment is that it stays true only until someone adds a fifth
// point. These tests fail when a new insertion point forgets the discipline, because they assert
// on the response rather than on the list.

// TestPostSanitizeBoundary_ThemeIsAClosedVocabulary — insertion point 1.
//
// The theme lands inside an inline <script> as a string literal. Nothing escapes it, and nothing
// needs to: themeFromCookie answers from {light, dark, auto} and defaults to auto. That is the
// discipline; this asserts the vocabulary is actually closed rather than merely intended to be.
func TestPostSanitizeBoundary_ThemeIsAClosedVocabulary(t *testing.T) {
	defer withVersionChainOverride()()
	mem := publicSharedMem()
	mem.WorkItemID = strptr("aihub#240")
	mem.RenderedHTML = htmlPtr(`<h1>t</h1><h2 id="s">S</h2><p>keep</p>`)
	defer withLoadMemoryOverride(mem, nil)()

	for _, hostile := range []string{
		`'});alert(1);({'`,
		`dark');alert(1);('`,
		`" onload="alert(1)`,
		`</script><script>alert(1)</script>`,
	} {
		e := echo.New()
		c, rec := newUIContext(e, "GET", "/ui/artifacts/:id/html", "mem_share1")
		c.SetPath("/ui/artifacts/:id/html")
		c.Request().AddCookie(&http.Cookie{Name: themeCookieName, Value: hostile})
		setUser(c, authorUser())
		if err := handleArtifactHTML(nil)(c); err != nil {
			e.HTTPErrorHandler(err, c)
		}
		body := rec.Body.String()

		if strings.Contains(body, "alert(1)") {
			t.Errorf("a hostile theme cookie reached the response: %q", hostile)
		}
		// And the fallback must be the safe value, not the raw one.
		if !strings.Contains(body, `'auto'`) && !strings.Contains(body, `"auto"`) {
			t.Errorf("theme %q did not fall back to auto", hostile)
		}
	}
}

// TestPostSanitizeBoundary_HostHeaderIsEscaped — insertion point 2.
//
// The share control embeds a URL built from c.Request().Host, which is attacker-influenceable.
// The discipline is html.EscapeString; this asserts it, because a Host-derived value reaching
// markup unescaped is the classic version of this whole class.
func TestPostSanitizeBoundary_HostHeaderIsEscaped(t *testing.T) {
	defer withVersionChainOverride()()
	mem := publicSharedMem()
	mem.WorkItemID = strptr("aihub#240")
	mem.RenderedHTML = htmlPtr(`<h1>t</h1><h2 id="s">S</h2><p>keep</p>`)
	defer withLoadMemoryOverride(mem, nil)()

	e := echo.New()
	c, rec := newUIContext(e, "GET", "/ui/artifacts/:id/html", "mem_share1")
	c.SetPath("/ui/artifacts/:id/html")
	c.Request().Host = `evil"><script>alert(1)</script><x y="`
	setUser(c, authorUser())
	if err := handleArtifactHTML(nil)(c); err != nil {
		e.HTTPErrorHandler(err, c)
	}
	body := rec.Body.String()

	if strings.Contains(body, "<script>alert(1)</script>") {
		t.Error("a hostile Host header broke out of the share control's markup")
	}
	if !strings.Contains(body, "alert(1)") {
		t.Skip("the share control was not emitted for this fixture; nothing to assert")
	}
	// Present but inert: it must appear escaped.
	if !strings.Contains(body, "&lt;script&gt;") && !strings.Contains(body, "&#34;") {
		t.Error("the Host-derived URL is present but shows no sign of escaping")
	}
}

// TestPostSanitizeBoundary_AnnotationFieldsAreEscaped — insertion point 3.
//
// Commit bodies, author names and quotes are user-supplied and land in the annotation chrome and
// in the JSON island. The chrome escapes per field; the island uses escapeJSONForScriptTag,
// which exists because a JSON payload inside <script> needs "</" neutralised rather than HTML
// escaping.
func TestPostSanitizeBoundary_AnnotationFieldsAreEscaped(t *testing.T) {
	defer withVersionChainOverride()()
	mem := publicSharedMem()
	mem.WorkItemID = strptr("aihub#240")
	mem.RenderedHTML = htmlPtr(`<h1>t</h1><h2 id="s">S</h2><p>keep</p>`)
	mem.Commits = json.RawMessage(`[{"id":"c1","author_display":"<img src=x onerror=alert(1)>",` +
		`"body":"</script><script>alert(2)</script>","created_at":"2026-01-01T00:00:00Z",` +
		`"status":"open","anchor":{"heading_id":"s","heading_text":"S","quote":"x"}}]`)
	defer withLoadMemoryOverride(mem, nil)()

	body := renderArtifactAt(t, "/ui/artifacts/:id/html").Body.String()

	for _, live := range []string{
		"<img src=x onerror=alert(1)>",
		"<script>alert(2)</script>",
	} {
		if strings.Contains(body, live) {
			t.Errorf("annotation field data reached the response as live markup: %q", live)
		}
	}
	// The island must not be terminable from inside its own payload.
	if strings.Contains(body, "</script><script>") {
		t.Error("a commit body closed the JSON island's script element")
	}
}

// TestPostSanitizeBoundary_ChromeElementsAreUnforgeable — the other half of insertion point 3.
//
// The chrome's own JS resolves its elements by id, and the agent body is inlined BEFORE the
// chrome, so `getElementById` handed our code the artifact's element. annot.js then wrote
// position:fixed and z-index onto it — our own script granting agent content precisely the
// placement properties the sanitizer withholds. Not a bypass of the allowlist; the allowlist
// defeated from the inside.
//
// The fix is a marker the sanitizer strips, so only server-rendered chrome can carry it. This
// asserts all three parts: the server stamps it, agent content cannot, and annot.js requires it.
func TestPostSanitizeBoundary_ChromeElementsAreUnforgeable(t *testing.T) {
	defer withVersionChainOverride()()
	mem := publicSharedMem()
	mem.WorkItemID = strptr("aihub#240")
	// Every chrome id the artifact could try to impersonate, including via the d2 channel.
	mem.RenderedHTML = htmlPtr(`<h1>t</h1><h2 id="s">S</h2><p>keep</p>` +
		`<div id="pf-selform" data-pf-chrome="1">forged</div>` +
		`<div id="pf-margin-rail" data-pf-chrome>forged</div>` +
		`<div id="pf-doc-col" data-pf-chrome="">forged</div>`)
	defer withLoadMemoryOverride(mem, nil)()

	body := renderArtifactAt(t, "/ui/artifacts/:id/html").Body.String()

	// 1. Every chrome element the JS resolves must carry the marker — asserted per element and
	//    evaluated as a DOM, not by counting the attribute. A total count passes while an
	//    individual element loses its marker, which is exactly the mutation that has to fail:
	//    dropping the marker from #pf-selform alone re-opens the placement primitive.
	// aihub#131 added two more: the in-place update WRITES through the annotation
	// section and the side rail (swapping thread groups and the Comments card), so
	// an artifact that wins either lookup does not just break the update — it gets
	// server-rendered reply/resolve forms, with live same-origin actions, imported
	// into DOM it controls.
	for _, id := range []string{"pf-doc-col", "pf-margin-rail", "pf-selform", "pf-annot-list", "pf-side-rail"} {
		marked := elementsWithIDAndMarker(t, body, id)
		if marked != 1 {
			t.Errorf("%d elements have id=%q AND data-pf-chrome, want exactly 1 (the server's). "+
				"annot.js resolves this element through the marker, so without it the lookup falls "+
				"back to whatever the artifact supplied", marked, id)
		}
	}

	// 2. The agent's attempts to carry the marker were stripped: its three decoys survive as
	//    inert content, but unmarked.
	if forged := strings.Count(body, `>forged<`); forged != 3 {
		t.Errorf("expected the agent's three <div>s to survive as inert content, found %d", forged)
	}
	for _, agentAttempt := range []string{`data-pf-chrome="1"`, `data-pf-chrome=""`} {
		if strings.Contains(body, agentAttempt) {
			t.Errorf("an agent-supplied %s survived sanitization — the marker is forgeable and the "+
				"chrome lookups it guards are back to being clobberable", agentAttempt)
		}
	}

	// 3. annot.js must require the marker rather than the bare id.
	js := string(render.AnnotJS())
	if !strings.Contains(js, `'[data-pf-chrome][id="'`) {
		t.Error("annot.js no longer resolves chrome elements through the data-pf-chrome marker; " +
			"getElementById returns the first element with the id regardless of tag, and the agent " +
			"body precedes the chrome in document order")
	}
	// The only id lookup left may be the heading anchor, which is agent-supplied BY DESIGN.
	rest := strings.ReplaceAll(js, "document.getElementById(anchor.heading_id)", "")
	for _, line := range strings.Split(rest, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "//") {
			continue
		}
		if strings.Contains(line, "getElementById(") {
			t.Errorf("annot.js resolves a chrome element by bare id:\n  %s", trimmed)
		}
	}
	// The spelling-independent half of this rule lives in
	// TestAnnotJS_DOMLookupsAreAllowlisted below. This loop stays because it names
	// the historically most common form and gives a targeted message, but it is a
	// denylist and must never be the only thing standing here.
}

// TestAnnotJS_DOMLookupsAreAllowlisted pins EVERY DOM lookup in annot.js against an
// explicit list of sanctioned call sites.
//
// 🔴 Why an allowlist. The first version of this rule (aihub#131) was a denylist of
// five spellings — `getElementById(`, `querySelector('#`, and three siblings. Nine
// mutants were run against it: two were caught and SEVEN walked straight through,
// including `querySelector('.pf-annotations')`, which is the verbatim defect a human
// reviewer had already caught in this same work item:
//
//	querySelector('[id="pf-side-rail"]')            attribute selector
//	querySelector(`#pf-side-rail`)                  template literal
//	querySelector('aside#pf-side-rail')             type-qualified
//	var _r='#pf-side-rail'; querySelector(_r)       indirection through a variable
//	querySelectorAll('[id=pf-side-rail]')[0]        unquoted attribute value
//	querySelector('.pf-annotations')                class instead of id
//	querySelector('section.pf-annotations')         type-qualified class
//
// Ask the question that settles a gate: what is the cheapest single edit that makes
// it green while reintroducing the bug? Against a denylist the answer is "spell the
// selector differently", which costs less than complying. Against this list the
// answer is "add an entry", which is a deliberate edit in a security-sensitive file
// that a reviewer will see.
//
// So the rule is inverted: every occurrence of getElementById / querySelector /
// querySelectorAll in annot.js must appear below with a reason it is safe. A new
// lookup is red WHATEVER it is spelled like, because being absent from this map is
// the failing condition — not matching some pattern.
//
// Three grounds make an entry safe, and every entry names which one it is:
//
//	SANCTIONED — the marker-qualified resolvers chromeElIn / islandIn themselves.
//	UNFORGEABLE — the selector requires a data-* attribute or a <script> element.
//	              Neither is on the sanitizer's allowlist, so agent content cannot
//	              produce one (internal/render/sanitize.go).
//	SCOPED — the receiver is an element already resolved through one of the above,
//	         so artifact content elsewhere in the document is out of reach.
//	BY DESIGN — the heading-anchor lookup, which is MEANT to resolve agent content.
//
// Note this test reads the file's raw text, comments included: a comment that spells
// out one of these calls will fail it. That is the fail-closed direction and matches
// the convention this package already follows for inlined assets.
func TestAnnotJS_DOMLookupsAreAllowlisted(t *testing.T) {
	allowed := map[string]string{
		// ── SANCTIONED: the two resolvers everything else is supposed to go through.
		`root.querySelector('[data-pf-chrome][id="' + id + '"]')`:             "chromeElIn — the marker-qualified resolver itself",
		`root.querySelector('script#pf-annot-data[type="application/json"]')`: "islandIn — <script> is not on the sanitizer's element allowlist",

		// ── BY DESIGN: resolves agent-authored content, which is the whole point.
		`document.getElementById(anchor.heading_id)`:     "heading anchor — agent-supplied by design (aihub#125)",
		`document.querySelectorAll('h1,h2,h3,h4,h5,h6')`: "heading anchor fallback by text — agent-supplied by design",

		// ── UNFORGEABLE: data-* is on no sanitizer allowlist, in any form.
		`document.querySelector('mark[data-commit-id="' + commit.id + '"]')`:                        "data-commit-id is unforgeable",
		`document.querySelector('mark[data-commit-id="' + id + '"]')`:                               "data-commit-id is unforgeable",
		`document.querySelectorAll('mark[data-commit-id="' + cid + '"]')`:                           "data-commit-id is unforgeable",
		`document.querySelector('.pf-annot-marker[data-commit-id="' + id + '"]')`:                   "data-commit-id is unforgeable",
		`document.querySelector('.pf-annot-entry[data-commit-id="' + cid + '"]')`:                   "data-commit-id is unforgeable",
		`document.querySelector('.pf-annot-entry[data-commit-id="' + anchored[i].commit.id + '"]')`: "data-commit-id is unforgeable",

		// ── SCOPED: receiver already resolved through a sanctioned lookup.
		`_selform.querySelector('textarea[name="body"]')`:                    "scoped to #pf-selform (chromeEl)",
		`_selform.querySelectorAll('[name="' + name + '"]')`:                 "scoped to #pf-selform (chromeEl)",
		`form.querySelectorAll('button[type="submit"], button:not([type])')`: "scoped to the form being submitted",
		`host.querySelector(':scope > .pf-annot-form')`:                      "scoped to #pf-annot-list (chromeEl)",
		`host.querySelectorAll(':scope > .pf-annot-section')`:                "scoped to #pf-annot-list (chromeEl)",
		`fresh.querySelectorAll(':scope > .pf-annot-section')`:               "scoped to the response document's #pf-annot-list (chromeElIn)",
		`rail.querySelector('.pf-side-cmt')`:                                 "scoped to #pf-side-rail (chromeEl/chromeElIn)",
		`root.querySelectorAll('.pf-side-cmt-item')`:                         "scoped to the Comments card just resolved from the rail",
	}

	js := string(render.AnnotJS())
	sites := domLookupSites(js)
	if len(sites) == 0 {
		t.Fatal("found no DOM lookups in annot.js at all — the extractor is broken, and a " +
			"broken extractor is a gate that passes everything")
	}

	used := map[string]bool{}
	for _, s := range sites {
		if _, ok := allowed[s.Expr]; !ok {
			t.Errorf("annot.js:%d performs an unsanctioned DOM lookup:\n  %s\n"+
				"Every lookup in this file must be listed in TestAnnotJS_DOMLookupsAreAllowlisted "+
				"with the ground that makes it safe (SANCTIONED / UNFORGEABLE / SCOPED / BY DESIGN). "+
				"If this resolves chrome, route it through chromeElIn or islandIn instead; if it is "+
				"genuinely safe, add it to the map and say why.", s.Line, s.Expr)
			continue
		}
		used[s.Expr] = true
	}
	for expr := range allowed {
		if !used[expr] {
			t.Errorf("allowlist entry no longer appears in annot.js, so it is a standing permission "+
				"for a call site that does not exist — delete it:\n  %s", expr)
		}
	}
}

// domLookupSite is one getElementById/querySelector/querySelectorAll call in a JS source.
type domLookupSite struct {
	Line int
	Expr string // receiver + method + arguments, whitespace-collapsed
}

// domLookupSites extracts every DOM lookup call, receiver included, from js.
//
// It is deliberately receiver-agnostic: keying on `document.` would miss
// `doc.querySelector(...)` against a parsed response document (which carries the
// same sanitized agent body), and would miss any future alias of either.
func domLookupSites(js string) []domLookupSite {
	var out []domLookupSite
	for _, method := range []string{"getElementById(", "querySelector(", "querySelectorAll("} {
		for i := 0; i+len(method) <= len(js); {
			idx := strings.Index(js[i:], method)
			if idx < 0 {
				break
			}
			at := i + idx
			i = at + len(method)

			// Walk back over the receiver expression (identifiers, dots, subscripts).
			start := at
			for start > 0 && (isJSIdentByte(js[start-1]) || js[start-1] == '.') {
				start--
			}
			// Scan forward to the matching ')', respecting quoted strings.
			end, ok := matchCloseParen(js, at+len(method)-1)
			if !ok {
				continue
			}
			out = append(out, domLookupSite{
				Line: 1 + strings.Count(js[:start], "\n"),
				Expr: collapseWS(js[start : end+1]),
			})
		}
	}
	return out
}

func isJSIdentByte(c byte) bool {
	return c == '_' || c == '$' || c == '[' || c == ']' ||
		(c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9')
}

// matchCloseParen returns the index of the ')' closing the '(' at open.
func matchCloseParen(s string, open int) (int, bool) {
	depth := 0
	var quote byte
	for i := open; i < len(s); i++ {
		c := s[i]
		if quote != 0 {
			if c == '\\' {
				i++
				continue
			}
			if c == quote {
				quote = 0
			}
			continue
		}
		switch c {
		case '\'', '"', '`':
			quote = c
		case '(':
			depth++
		case ')':
			depth--
			if depth == 0 {
				return i, true
			}
		}
	}
	return 0, false
}

func collapseWS(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// elementsWithIDAndMarker counts elements carrying both the given id and data-pf-chrome,
// evaluated by parsing the response. Byte counting cannot express "on the same element", which
// is the property that matters.
func elementsWithIDAndMarker(t *testing.T, body, id string) int {
	t.Helper()
	doc, err := htmlparse.Parse(strings.NewReader(body))
	if err != nil {
		t.Fatalf("parse response: %v", err)
	}
	n := 0
	var walk func(*htmlparse.Node)
	walk = func(node *htmlparse.Node) {
		if node.Type == htmlparse.ElementNode {
			hasID, hasMarker := false, false
			for _, a := range node.Attr {
				switch strings.ToLower(a.Key) {
				case "id":
					hasID = a.Val == id
				case "data-pf-chrome":
					hasMarker = true
				}
			}
			if hasID && hasMarker {
				n++
			}
		}
		for c := node.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(doc)
	return n
}
