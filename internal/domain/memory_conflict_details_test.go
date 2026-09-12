package domain

// aihub#375 — the CONFLICT_SIMILAR_MEMORY envelope, without a database.
//
// The strict-dedup refusal used to carry the colliding memory's ENTIRE body in
// details.existing.content — the one unbounded field in any details producer,
// and the reason pkg/client.formatDetails grew a byte cap that every OTHER
// error then paid for. memoryConflictErr now bounds the field at the source;
// what these tests hold is the bound itself and the judgment elements the
// refusal must still carry (id to fetch the rest, similarity that tripped it,
// type, and how much was elided).
//
// The mutant these are built against: put existing.Content back into the map
// (under any key name). TestMemoryConflictErr_EnvelopeSurvivesTheRenderLimit
// goes red on that — the 12,000-rune body blows the whole-envelope budget —
// and it does so REGARDLESS of key spelling, which a shape-only assertion
// could not. The class-level version of the same mutant (a DIFFERENT
// NewErrDetails site acquiring an unbounded field) is the census's job:
// err_details_census_test.go.

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/GMISWE/ieops-aihub/pkg/client"
)

// TestMemoryConflictErr_EnvelopeSurvivesTheRenderLimit is the byte budget,
// same instrument as commit_locks_test.go's advice budget but stricter: the
// WHOLE envelope must fit under client.DetailsRenderLimit, not one key. The
// commit-lock refusal cannot promise that (its conflicts list scales with the
// commit); this envelope has nothing count-scaled in it, so promising less
// would be leaving information on the table — under the whole-envelope
// promise, key sort order stops mattering and nothing is ever cut.
//
// The content is deliberately hostile: far over the excerpt cap, CJK (3-byte
// runes), plus 4-byte runes and newlines (which escape to two bytes each). If
// this passes, the arithmetic worst case — every excerpt rune escaping to 6
// bytes — still lands under the limit with the current constants; the
// arithmetic subtest pins that so the pass is not an artifact of this one
// string.
func TestMemoryConflictErr_EnvelopeSurvivesTheRenderLimit(t *testing.T) {
	body := strings.Repeat("这是一条很长的记忆正文🀄，撞车时它绝不能整篇进 details。\n", 400)
	existing := &Memory{
		ID:      "mem_AbCdEfGh",
		Type:    "experience.pitfall",
		Content: body,
	}
	aerr := memoryConflictErr(existing, 0.8666666666666667)

	raw, err := json.Marshal(aerr.Details)
	if err != nil {
		t.Fatalf("marshal details: %v", err)
	}
	var compact bytes.Buffer
	if err := json.Compact(&compact, raw); err != nil {
		t.Fatalf("compact: %v", err)
	}
	if compact.Len() > client.DetailsRenderLimit {
		t.Errorf("the whole envelope is %d compacted bytes against a %d-byte render cap — "+
			"formatDetails will cut it, so some judgment element is lost. The producer's "+
			"promise is that NOTHING here is ever cut; shrink memoryConflictExcerptRunes "+
			"or change client.DetailsRenderLimit and this test together",
			compact.Len(), client.DetailsRenderLimit)
	}
	t.Logf("envelope = %d compacted bytes of a %d-byte cap", compact.Len(), client.DetailsRenderLimit)

	// The arithmetic worst case: every excerpt rune escaping to \uXXXX (6
	// bytes), plus the fixed keys with generous field sizes. This is what makes
	// the constant pair (excerpt runes, render limit) safe for contents this
	// test did not think of, control characters included.
	const worstFixed = len(`{"existing":{"content_excerpt":"","content_len":9999999999,` +
		`"id":"mem_XXXXXXXX","similarity":0.99999999999999999,` +
		`"type":"experience.some.long.type.name.nobody.uses"}}`)
	if worst := worstFixed + memoryConflictExcerptRunes*6; worst > client.DetailsRenderLimit {
		t.Errorf("arithmetic worst case %d exceeds the %d-byte cap: a content of escaped "+
			"control characters would be cut", worst, client.DetailsRenderLimit)
	}
}

// TestMemoryConflictErr_CarriesTheJudgmentElements holds the shape: what a
// caller needs to decide "same note, go update it" vs "false positive, retry
// with dedup_mode=suggest" — and the excerpt's two contract properties, prefix
// and rune bound. The elision signal is content_len > excerpt runes, NOT an
// appended marker: the excerpt stays a literal prefix a caller can compare
// against its own text byte for byte.
func TestMemoryConflictErr_CarriesTheJudgmentElements(t *testing.T) {
	body := strings.Repeat("一二三四五六七八九十", 30) // 300 runes, over the cap
	aerr := memoryConflictErr(&Memory{ID: "mem_AbCdEfGh", Type: "fact.reference", Content: body}, 0.91)

	details, ok := aerr.Details.(map[string]any)
	if !ok {
		t.Fatalf("details is %T, want map[string]any", aerr.Details)
	}
	ex, ok := details["existing"].(map[string]any)
	if !ok {
		t.Fatalf("details.existing is %T, want map[string]any", details["existing"])
	}
	if ex["id"] != "mem_AbCdEfGh" || ex["type"] != "fact.reference" || ex["similarity"] != 0.91 {
		t.Errorf("id/type/similarity = %v/%v/%v, want the colliding row's", ex["id"], ex["type"], ex["similarity"])
	}
	if got := ex["content_len"]; got != 300 {
		t.Errorf("content_len = %v, want 300 (runes of the FULL content, so the caller knows how much was elided)", got)
	}
	excerpt, _ := ex["content_excerpt"].(string)
	if n := utf8.RuneCountInString(excerpt); n != memoryConflictExcerptRunes {
		t.Errorf("excerpt is %d runes, want exactly %d for an over-cap content", n, memoryConflictExcerptRunes)
	}
	if !strings.HasPrefix(body, excerpt) {
		t.Errorf("excerpt is not a prefix of the content — a marker or re-encoding crept in: %q", excerpt)
	}
	if _, there := ex["content"]; there {
		t.Errorf("details.existing.content is back — that is the unbounded field aihub#375 removed")
	}

	// Under-cap content: carried whole, and the elision signal reads "nothing
	// elided" (content_len == excerpt runes).
	small := "短记忆"
	aerr = memoryConflictErr(&Memory{ID: "mem_00000000", Type: "fact.note", Content: small}, 0.99)
	ex = aerr.Details.(map[string]any)["existing"].(map[string]any)
	if ex["content_excerpt"] != small {
		t.Errorf("under-cap content must be carried whole, got %q", ex["content_excerpt"])
	}
	if ex["content_len"] != utf8.RuneCountInString(small) {
		t.Errorf("content_len = %v, want %d", ex["content_len"], utf8.RuneCountInString(small))
	}
}
