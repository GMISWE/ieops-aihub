package domain

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// aihub#504: finalizeEmbeddedLen is the ONE rule deciding when a response
// carries embedded_len — present iff the stored vector embeds a STRICT PREFIX
// of the content. Every scan site applies it, so a bug here is a bug in
// pf_remember, pf_recall (both paths) and pf_get_memory simultaneously; that is
// why the rule gets its own calibration rather than riding along in a DB test.

func intp(n int) *int { return &n }

func TestFinalizeEmbeddedLen_KeepsAStrictPrefix(t *testing.T) {
	// Multi-byte content on purpose: the comparison must be runes, not bytes.
	// 100 CJK runes = 300 bytes; an embedded_len of 99 runes is a strict prefix
	// by the rune count and would NOT be one by the byte count (99 < 300).
	content := strings.Repeat("跨", 100)
	m := &Memory{Content: content, EmbeddedLen: intp(99)}
	m.finalizeEmbeddedLen()
	if m.EmbeddedLen == nil || *m.EmbeddedLen != 99 {
		t.Fatalf("embedded_len=99 over %d-rune content is a strict prefix and must survive; got %v",
			utf8.RuneCountInString(content), m.EmbeddedLen)
	}
}

func TestFinalizeEmbeddedLen_SuppressesFullCoverage(t *testing.T) {
	// The common case: the vector embeds everything. Forwarding the value would
	// spend response tokens confirming the default on every recall item, and —
	// worse — would make "field present" stop meaning "truncated".
	content := strings.Repeat("跨", 100)
	for name, l := range map[string]*int{
		"exact rune count":            intp(100),
		"over rune count (byte tale)": intp(300), // what a byte-counting writer would record
		"unrecorded (legacy NULL)":    nil,
	} {
		m := &Memory{Content: content, EmbeddedLen: l}
		m.finalizeEmbeddedLen()
		if m.EmbeddedLen != nil {
			t.Errorf("%s: embedded_len must be suppressed, got %d", name, *m.EmbeddedLen)
		}
	}
}

// TestMemoryEmbedInputLengthIsTheRecordedFact pins the relation the writers
// rely on: what Remember/backfill record as embedded_len — the rune count of
// the builder's output — is under the budget by construction, and equals the
// content's own rune count exactly when nothing was cut. If MemoryEmbedInput
// ever grew a transformation beyond truncation (a trim, a prefix), recording
// its output length as "runes of content covered" would silently become a lie;
// this test is the tripwire.
func TestMemoryEmbedInputLengthIsTheRecordedFact(t *testing.T) {
	t.Setenv("EMBEDDING_INPUT_MAX_RUNES", "") // exercise the shipped default
	under := strings.Repeat("界", 10)
	if got := MemoryEmbedInput(under); got != under {
		t.Fatalf("under-budget content must pass through identically; got %d runes for %d",
			utf8.RuneCountInString(got), utf8.RuneCountInString(under))
	}
	// embedInputMaxRunes is resolved once at package init, so build the
	// over-budget case against that resolved value rather than the env.
	over := strings.Repeat("界", embedInputMaxRunes+7)
	got := MemoryEmbedInput(over)
	if n := utf8.RuneCountInString(got); n != embedInputMaxRunes {
		t.Fatalf("over-budget content must be cut to exactly the budget (%d runes), got %d",
			embedInputMaxRunes, n)
	}
	if !strings.HasPrefix(over, got) {
		t.Fatal("the embedded text must be a prefix of the content — anything else and embedded_len describes the wrong runes")
	}
}
