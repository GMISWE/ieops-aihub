package render

import (
	"strings"
	"testing"
)

func TestDocument_HasDoctypeAndHtmlSkeleton(t *testing.T) {
	got := Document("<p>hello</p>", "Test", "")
	for _, want := range []string{
		"<!DOCTYPE html>",
		"<html lang=\"en\">",
		"<head>",
		"<meta charset=\"utf-8\">",
		"<meta name=\"viewport\"",
		"<body>",
		"</body>",
		"</html>",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in Document output", want)
		}
	}
}

func TestDocument_EmbedsBody(t *testing.T) {
	body := "<p>UNIQUE-MARKER-12345</p>"
	got := Document(body, "", "")
	if !strings.Contains(got, body) {
		t.Errorf("rendered body fragment not embedded; got: %s", got)
	}
}

func TestDocument_IncludesStylesheet(t *testing.T) {
	got := Document("", "", "")
	// The embedded stylesheet must be inlined within a <style> block.
	if !strings.Contains(got, "<style>") || !strings.Contains(got, "</style>") {
		t.Fatalf("expected <style>...</style> block, got: %s", got)
	}
	for _, want := range []string{
		"body {",     // base body rules
		"table {",    // table styling
		"pre.chroma", // code block container (chroma-aware selector)
		".chroma .k", // chroma keyword color
	} {
		if !strings.Contains(got, want) {
			t.Errorf("stylesheet missing rule containing %q", want)
		}
	}
}

func TestDocument_TitleEscaped(t *testing.T) {
	got := Document("", `<script>alert("x")</script>`, "")
	if strings.Contains(got, `<title><script>`) {
		t.Errorf("title not HTML-escaped: %s", got)
	}
	if !strings.Contains(got, "&lt;script&gt;") {
		t.Errorf("expected escaped &lt;script&gt; in title")
	}
}

func TestDocument_EmptyTitleFallback(t *testing.T) {
	got := Document("", "", "")
	if !strings.Contains(got, "<title>polyforge artifact</title>") {
		t.Errorf("expected fallback title; got: %s", got)
	}
}

// TestDocument_NoBackNavWhenEmpty asserts that an empty backHref emits no nav
// bar — the CLI/API standalone document must stay a pure content view.
func TestDocument_NoBackNavWhenEmpty(t *testing.T) {
	got := Document("<p>body</p>", "Test", "")
	// Note: the embedded stylesheet always contains the ".pf-doc-nav" CSS
	// rule, so we must assert the absence of the rendered <nav> element, not
	// the bare class-name substring.
	if strings.Contains(got, `<nav class="pf-doc-nav">`) {
		t.Errorf("expected no rendered pf-doc-nav element when backHref is empty; got: %s", got)
	}
}

// TestDocument_BackNavWhenHrefSet asserts a non-empty backHref renders the nav
// bar with the href HTML-escaped inside an <a href=...>.
func TestDocument_BackNavWhenHrefSet(t *testing.T) {
	got := Document("<p>body</p>", "Test", "/ui/wi/aihub%2398")
	if !strings.Contains(got, `<nav class="pf-doc-nav">`) {
		t.Errorf("expected <nav class=\"pf-doc-nav\"> when backHref set; got: %s", got)
	}
	if !strings.Contains(got, `<a href="/ui/wi/aihub%2398">`) {
		t.Errorf("expected back-link anchor with the href; got: %s", got)
	}
}

// TestDocument_BackHrefEscaped asserts the href is HTML-escaped so a hostile
// or quote-bearing href cannot break out of the attribute.
func TestDocument_BackHrefEscaped(t *testing.T) {
	got := Document("", "", `/ui/wi/"x"&y`)
	if strings.Contains(got, `href="/ui/wi/"x"&y"`) {
		t.Errorf("back href not escaped: %s", got)
	}
	if !strings.Contains(got, "&#34;x&#34;&amp;y") {
		t.Errorf("expected escaped quotes/amp in back href; got: %s", got)
	}
}
