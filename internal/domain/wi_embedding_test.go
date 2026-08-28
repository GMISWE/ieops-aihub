package domain

import (
	"strings"
	"testing"
)

func TestWIEmbedInput_Composition(t *testing.T) {
	if got := wiEmbedInput("goal text", "content text"); got != "goal text\n\ncontent text" {
		t.Fatalf("composition: %q", got)
	}
	if got := wiEmbedInput("goal only", ""); got != "goal only" {
		t.Fatalf("goal only: %q", got)
	}
	if got := wiEmbedInput("", "content only"); got != "content only" {
		t.Fatalf("content only: %q", got)
	}
	if got := wiEmbedInput("  ", "  "); got != "" {
		t.Fatalf("blank input must produce empty embed text, got %q", got)
	}
}

func TestWIEmbedInput_TruncatesAtRuneBudget(t *testing.T) {
	// Multi-byte runes on purpose: the budget is runes, not bytes (a byte
	// truncation would split a rune and feed the provider invalid UTF-8).
	long := strings.Repeat("字", wiEmbedInputMax+500)
	got := wiEmbedInput("g", long)
	if n := len([]rune(got)); n != wiEmbedInputMax {
		t.Fatalf("rune len = %d, want %d", n, wiEmbedInputMax)
	}
	if !strings.HasPrefix(got, "g\n\n") {
		t.Fatalf("goal must lead the embed input")
	}
}
