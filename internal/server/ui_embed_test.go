package server

import (
	"html/template"
	"strings"
	"testing"
)

// TestUIFuncMap_MD_D2Diagram guards aihub#231: the memory/wi detail page
// renders markdown via the shared "md" template func, but goldmark
// (render.Markdown) doesn't know about d2 fences -- without the
// RenderDiagramsForUI post-process, a ```d2 block just sits there as an
// unrendered <pre><code class="language-d2"> block. This must inline-render
// to an <svg>, matching what the artifact viewer (routes_artifacts.go)
// already does.
func TestUIFuncMap_MD_D2Diagram(t *testing.T) {
	funcs := uiFuncMap()
	md, ok := funcs["md"].(func(string) template.HTML)
	if !ok {
		t.Fatalf("md not registered in uiFuncMap or wrong signature")
	}

	src := "# Title\n\n```d2\na -> b\n```\n"
	got := string(md(src))

	if !strings.Contains(got, "<svg") {
		t.Errorf("md(%q) = %q; want an inline <svg> for the d2 block", src, got)
	}
	if strings.Contains(got, `language-d2`) {
		t.Errorf("md(%q) = %q; still contains an unrendered language-d2 code block", src, got)
	}
}

// TestUIFuncMap_MD_InvalidD2Degrades guards the graceful-degradation
// invariant: a syntactically broken d2 block must fall back to a normal
// code block, never error out and never emit a partial/empty <svg>.
func TestUIFuncMap_MD_InvalidD2Degrades(t *testing.T) {
	funcs := uiFuncMap()
	md, ok := funcs["md"].(func(string) template.HTML)
	if !ok {
		t.Fatalf("md not registered in uiFuncMap or wrong signature")
	}

	// Unterminated shape/edge syntax -- d2lib.Compile should reject this.
	src := "```d2\na -> {\n```\n"
	got := string(md(src))

	if strings.Contains(got, "<svg") {
		t.Errorf("md(%q) = %q; invalid d2 must not produce an <svg>", src, got)
	}
	if !strings.Contains(got, "<code") {
		t.Errorf("md(%q) = %q; want the block to degrade to a plain code block", src, got)
	}
}

// TestUIFuncMap_MD_NonD2Untouched is a collateral-damage guard: the
// RenderDiagramsForUI post-process must only ever rewrite language-d2 blocks,
// so ordinary markdown (heading, table, non-d2 fenced code) must still render
// to its usual elements. Spot-checks the structural output rather than
// asserting byte-equality with the pre-fix rendering.
func TestUIFuncMap_MD_NonD2Untouched(t *testing.T) {
	funcs := uiFuncMap()
	md, ok := funcs["md"].(func(string) template.HTML)
	if !ok {
		t.Fatalf("md not registered in uiFuncMap or wrong signature")
	}

	src := "# Heading\n\n| a | b |\n|---|---|\n| 1 | 2 |\n\n```go\nfunc main() {}\n```\n"
	got := string(md(src))

	if strings.Contains(got, "<svg") {
		t.Errorf("md(%q) = %q; non-d2 content must not produce an <svg>", src, got)
	}
	if !strings.Contains(got, "<table") {
		t.Errorf("md(%q) = %q; want the markdown table rendered as <table>", src, got)
	}
	if !strings.Contains(got, `<pre class="chroma">`) {
		t.Errorf("md(%q) = %q; want the go code block preserved via chroma syntax highlighting", src, got)
	}
	if !strings.Contains(got, "<h1") {
		t.Errorf("md(%q) = %q; want the heading rendered as <h1>", src, got)
	}
}

// TestUIFuncMap_Truncate guards against the mid-rune byte-cut regression:
// the old s[:n] + "..." implementation would slice CJK strings in the
// middle of a multi-byte UTF-8 sequence, producing replacement-char
// garbage in wi list / detail / memories / event timeline / queue
// section views. truncate must count runes (user-visible chars), not
// bytes, so n is a display budget.
func TestUIFuncMap_Truncate(t *testing.T) {
	funcs := uiFuncMap()
	truncate, ok := funcs["truncate"].(func(int, string) string)
	if !ok {
		t.Fatalf("truncate not registered in uiFuncMap or wrong signature")
	}

	cases := []struct {
		name string
		n    int
		in   string
		want string
	}{
		{"empty", 10, "", ""},
		{"ascii_short", 10, "hello", "hello"},
		{"ascii_at_budget", 5, "hello", "hello"},
		{"ascii_over", 5, "hello world", "hello..."},
		{"cjk_short", 10, "你好", "你好"},
		{"cjk_at_budget", 2, "你好", "你好"},
		{"cjk_over", 2, "你好世界", "你好..."},
		// Byte 4 of "AB你CD" lands on the 2nd byte of 你 (3-byte rune),
		// so the old s[:4] would emit a replacement char. Rune-based
		// truncation must cleanly yield the first 4 runes + ellipsis.
		{"cjk_boundary_byte", 4, "AB你CD", "AB你C..."},
		{"mixed", 4, "ab你好cd", "ab你好..."},
		{"n_zero", 0, "anything", "anything"},
		{"n_negative", -1, "anything", "anything"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := truncate(tc.n, tc.in)
			if got != tc.want {
				t.Errorf("truncate(%d, %q) = %q; want %q", tc.n, tc.in, got, tc.want)
			}
			if strings.ContainsRune(got, '�') {
				t.Errorf("truncate(%d, %q) = %q contains U+FFFD replacement char — mid-rune cut", tc.n, tc.in, got)
			}
		})
	}
}

// TestUIFuncMap_Wiref guards against the slug-# fragment regression: a wi
// slug like "aihub#1" embedded as raw text in an href would let the browser
// treat "#1" as a URL fragment and strip it before the request, so the
// handler would only ever see "aihub" and return 404. wiref must PathEscape
// the # to %23 so the full slug survives the round-trip.
func TestUIFuncMap_Wiref(t *testing.T) {
	funcs := uiFuncMap()
	wiref, ok := funcs["wiref"].(func(string) string)
	if !ok {
		t.Fatalf("wiref not registered in uiFuncMap or wrong signature")
	}

	cases := []struct {
		in   string
		want string
	}{
		{"", ""},
		{"aihub#1", "/ui/wi/aihub%231"},
		{"aihub#59", "/ui/wi/aihub%2359"},
		{"wi_u3WPMDeB", "/ui/wi/wi_u3WPMDeB"},
		{"proj-with-dash#12", "/ui/wi/proj-with-dash%2312"},
	}
	for _, tc := range cases {
		got := wiref(tc.in)
		if got != tc.want {
			t.Errorf("wiref(%q) = %q; want %q", tc.in, got, tc.want)
		}
		if strings.Contains(tc.in, "#") && strings.Contains(got, "#") {
			t.Errorf("wiref(%q) returned %q which still contains a raw '#' — browser will strip it as URL fragment", tc.in, got)
		}
	}
}
