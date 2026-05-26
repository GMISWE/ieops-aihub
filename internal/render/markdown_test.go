package render

import (
	"strings"
	"testing"
)

func TestMarkdown_Empty(t *testing.T) {
	out, err := Markdown("")
	if err != nil {
		t.Fatalf("Markdown(\"\") returned err: %v", err)
	}
	if out != "" {
		t.Fatalf("Markdown(\"\") = %q; want empty", out)
	}
}

func TestMarkdown_InlineSVG(t *testing.T) {
	// SVG must pass through verbatim (Unsafe mode); a `<circle>` tag in the
	// output proves no escaping happened.
	src := "# Diagram\n\n<svg width=\"20\" height=\"20\"><circle cx=\"10\" cy=\"10\" r=\"5\"/></svg>\n"
	out, err := Markdown(src)
	if err != nil {
		t.Fatalf("Markdown returned err: %v", err)
	}
	if !strings.Contains(out, "<svg") || !strings.Contains(out, "<circle") {
		t.Fatalf("inline SVG not preserved; got:\n%s", out)
	}
}

func TestMarkdown_GFMTable(t *testing.T) {
	src := "| a | b |\n|---|---|\n| 1 | 2 |\n"
	out, err := Markdown(src)
	if err != nil {
		t.Fatalf("Markdown returned err: %v", err)
	}
	if !strings.Contains(out, "<table") || !strings.Contains(out, "<th>a") {
		t.Fatalf("GFM table not rendered; got:\n%s", out)
	}
}

func TestMarkdown_TaskList(t *testing.T) {
	src := "- [ ] todo\n- [x] done\n"
	out, err := Markdown(src)
	if err != nil {
		t.Fatalf("Markdown returned err: %v", err)
	}
	// goldmark's task list extension (enabled via GFM) emits checkbox inputs.
	if !strings.Contains(out, `type="checkbox"`) {
		t.Fatalf("task list checkbox not rendered; got:\n%s", out)
	}
}

func TestMarkdown_CodeHighlight(t *testing.T) {
	src := "```go\nfunc main() { println(\"hi\") }\n```\n"
	out, err := Markdown(src)
	if err != nil {
		t.Fatalf("Markdown returned err: %v", err)
	}
	// chroma in classes mode wraps tokens with class names like "chroma" and "kd" / "nf".
	// The presence of any class= attribute inside a <pre>/<code> block is the signal.
	if !strings.Contains(out, "<pre") || !strings.Contains(out, "class=") {
		t.Fatalf("code block not highlighted via chroma; got:\n%s", out)
	}
}

func TestMarkdown_AutoHeadingID(t *testing.T) {
	src := "## My Heading\n"
	out, err := Markdown(src)
	if err != nil {
		t.Fatalf("Markdown returned err: %v", err)
	}
	if !strings.Contains(out, `id="my-heading"`) {
		t.Fatalf("auto heading ID not added; got:\n%s", out)
	}
}
