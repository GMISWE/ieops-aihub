package render

import (
	"strings"
	"testing"
)

func TestDocument_HasDoctypeAndHtmlSkeleton(t *testing.T) {
	got := Document("<p>hello</p>", "Test")
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
	got := Document(body, "")
	if !strings.Contains(got, body) {
		t.Errorf("rendered body fragment not embedded; got: %s", got)
	}
}

func TestDocument_IncludesStylesheet(t *testing.T) {
	got := Document("", "")
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
	got := Document("", `<script>alert("x")</script>`)
	if strings.Contains(got, `<title><script>`) {
		t.Errorf("title not HTML-escaped: %s", got)
	}
	if !strings.Contains(got, "&lt;script&gt;") {
		t.Errorf("expected escaped &lt;script&gt; in title")
	}
}

func TestDocument_EmptyTitleFallback(t *testing.T) {
	got := Document("", "")
	if !strings.Contains(got, "<title>polyforge artifact</title>") {
		t.Errorf("expected fallback title; got: %s", got)
	}
}
