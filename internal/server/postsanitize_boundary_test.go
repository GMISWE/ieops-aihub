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
		// aihub#131: `document.querySelector('#pf-side-rail')` is the same lookup as
		// getElementById with a different spelling, and the ban above — which matches
		// the literal "getElementById(" rather than the property "resolves chrome by
		// bare id" — did not see it. The cheapest way to satisfy a literal-string gate
		// is to phrase the hazard differently, so the gate has to name the hazard.
		for _, q := range []string{`querySelector('#`, `querySelector("#`, `querySelectorAll('#`, `querySelectorAll("#`} {
			if strings.Contains(line, q) {
				t.Errorf("annot.js resolves a chrome element by bare id through a CSS selector "+
					"(use chromeEl/chromeElIn, which require data-pf-chrome):\n  %s", trimmed)
			}
		}
	}
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
