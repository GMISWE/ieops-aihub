package render

import (
	"strings"
	"testing"
)

func TestRenderDiagram(t *testing.T) {
	svg, err := RenderDiagram("x -> y\ny -> z")
	if err != nil {
		t.Fatalf("RenderDiagram: %v", err)
	}
	if !strings.Contains(svg, "<svg") {
		t.Fatalf("expected an <svg> element, got %d bytes", len(svg))
	}
}

func TestRenderDiagramsForUI(t *testing.T) {
	md, err := Markdown("intro paragraph\n\n```d2\nskill -> render\nrender -> ui\n```\n\ntrailing paragraph")
	if err != nil {
		t.Fatalf("Markdown: %v", err)
	}
	t.Logf("goldmark output for a d2 fenced block:\n%s", md)

	out := RenderDiagramsForUI(md)
	if !strings.Contains(out, "<svg") {
		t.Fatalf("RenderDiagramsForUI did not inject an <svg>; goldmark output was:\n%s", md)
	}
	if strings.Contains(out, `class="language-d2"`) {
		t.Errorf("the d2 code block should be replaced by a figure, not kept")
	}
	if !strings.Contains(out, "intro paragraph") || !strings.Contains(out, "trailing paragraph") {
		t.Errorf("surrounding prose must be preserved")
	}

	// Invalid d2 → graceful fallback: keep the code block, no panic, no <svg>.
	bad, err := Markdown("```d2\n>>> not valid d2 <<<\n```")
	if err != nil {
		t.Fatalf("Markdown(bad): %v", err)
	}
	got := RenderDiagramsForUI(bad)
	if strings.Contains(got, "<svg") {
		t.Errorf("invalid d2 should not produce an <svg>")
	}

	// No d2 blocks → unchanged (byte-identical passthrough).
	plain := "<p>no diagrams here</p>"
	if RenderDiagramsForUI(plain) != plain {
		t.Errorf("passthrough should be byte-identical when there are no d2 blocks")
	}
}
