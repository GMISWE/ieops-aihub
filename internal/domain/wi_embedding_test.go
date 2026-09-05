package domain

import (
	"strings"
	"testing"
)

// aihub#361 renamed wiEmbedInput/wiEmbedInputMax to WorkItemEmbedInput/
// embedInputMaxRunes and moved them into embed_input.go, so that
// cmd/aihub-embed-backfill calls the same function instead of its own near-copy.
// These cases are unchanged: the composition and the rune budget are still the
// behaviour under test.

func TestWorkItemEmbedInput_Composition(t *testing.T) {
	if got := WorkItemEmbedInput("goal text", "content text"); got != "goal text\n\ncontent text" {
		t.Fatalf("composition: %q", got)
	}
	if got := WorkItemEmbedInput("goal only", ""); got != "goal only" {
		t.Fatalf("goal only: %q", got)
	}
	if got := WorkItemEmbedInput("", "content only"); got != "content only" {
		t.Fatalf("content only: %q", got)
	}
	if got := WorkItemEmbedInput("  ", "  "); got != "" {
		t.Fatalf("blank input must produce empty embed text, got %q", got)
	}
}

func TestWorkItemEmbedInput_TruncatesAtRuneBudget(t *testing.T) {
	// Multi-byte runes on purpose: the budget is runes, not bytes (a byte
	// truncation would split a rune and feed the provider invalid UTF-8).
	long := strings.Repeat("字", embedInputMaxRunes+500)
	got := WorkItemEmbedInput("g", long)
	if n := len([]rune(got)); n != embedInputMaxRunes {
		t.Fatalf("rune len = %d, want %d", n, embedInputMaxRunes)
	}
	if !strings.HasPrefix(got, "g\n\n") {
		t.Fatalf("goal must lead the embed input")
	}
}

// TestMemoryEmbedInput_TruncatesAtRuneBudget is the memory-side half that did
// not exist before aihub#361, because the memory write path had no budget to
// test. Same rune-not-byte requirement.
func TestMemoryEmbedInput_TruncatesAtRuneBudget(t *testing.T) {
	short := "a short memory"
	if got := MemoryEmbedInput(short); got != short {
		t.Fatalf("under-budget content must pass through verbatim, got %q", got)
	}

	long := strings.Repeat("字", embedInputMaxRunes+500)
	got := MemoryEmbedInput(long)
	if n := len([]rune(got)); n != embedInputMaxRunes {
		t.Fatalf("rune len = %d, want %d", n, embedInputMaxRunes)
	}
	if strings.ContainsRune(got, '�') {
		t.Fatal("truncation split a multi-byte rune — the budget must be applied over runes, not bytes")
	}
}

// TestMemoryEmbedInputDoesNotTrim pins the one deliberate asymmetry between the
// two builders. Memory content is stored verbatim and the live path never
// trimmed it; trimming here would re-point every re-embedded vector for no
// stated reason. If someone later decides memories SHOULD be trimmed, that is a
// corpus-wide re-embedding decision, not a tidy-up — this test is where it gets
// argued.
func TestMemoryEmbedInputDoesNotTrim(t *testing.T) {
	const padded = "\n  body  \n"
	if got := MemoryEmbedInput(padded); got != padded {
		t.Fatalf("MemoryEmbedInput trimmed its input (%q -> %q). The live write path has "+
			"never trimmed memory content; changing that silently changes what every "+
			"re-embedded row's vector represents.", padded, got)
	}
}
