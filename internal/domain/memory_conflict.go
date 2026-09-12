package domain

// The CONFLICT_SIMILAR_MEMORY refusal envelope (aihub#375).
//
// This lives in its own file rather than beside its caller in memory.go
// because memory.go is an embedding WRITER, and embed_writer_parity_test.go
// bans inline rune truncation in writer files outright — the embed-input
// budget lives in embed_input.go and nowhere else, and a second `[]rune(…)[:n]`
// in the same file is exactly the shape that drifted before aihub#361. The
// excerpt cap below is a DIFFERENT budget (an error-envelope bound, not the
// embedding input bound), and keeping it in a separate file is what keeps the
// two unconfusable — for the scanner and for a reader alike.

import "unicode/utf8"

// memoryConflictExcerptRunes bounds how much of the colliding memory's content
// a strict-dedup refusal carries in details.existing.content_excerpt (aihub#375).
//
// 120 runes is pf_recall's own "brief" budget (internal/mcp, briefRecallItem):
// the rendering this codebase already trusts to let a caller recognise a memory
// without reading it. The refusal needs exactly that much — enough to judge "is
// this the same note?" — and nothing more: `id` retrieves the full text
// (pf_get_memory), `content_len` says how much was elided, and `similarity` is
// the score that tripped the refusal.
//
// Before aihub#375 the field was `content`: the ENTIRE stored body, unbounded —
// a tens-of-KB memory is normal in this workspace. pkg/client.formatDetails'
// byte cap on the rendered details was in practice a patch for this one
// producer, paid by every error alike: any key the cut reached was lost, on
// envelopes that had nothing unbounded in them. Bounding the producer is what
// let that cap be raised for everyone else (client.DetailsRenderLimit; the
// budget test in memory_conflict_details_test.go ties the two together).
const memoryConflictExcerptRunes = 120

// memoryConflictErr builds the CONFLICT_SIMILAR_MEMORY refusal for a
// strict-mode dedup hit. Split out of Remember so the envelope has coverage
// that needs no database: the boundedness of `details` is a property of THIS
// function, not of the dedup query that decides to call it.
//
// The excerpt is a plain rune-boundary prefix with no "…" marker appended —
// elision is signalled machine-readably instead (content_len exceeding the
// excerpt's rune count), so the excerpt stays a literal prefix a caller can
// compare against its own text.
func memoryConflictErr(existing *Memory, sim float64) *AihubError {
	excerpt := existing.Content
	if runes := []rune(excerpt); len(runes) > memoryConflictExcerptRunes {
		excerpt = string(runes[:memoryConflictExcerptRunes])
	}
	return NewErrDetails(ErrConflictSimilarMemory,
		"similar memory already exists",
		map[string]any{"existing": map[string]any{
			"id":              existing.ID,
			"type":            existing.Type,
			"content_excerpt": excerpt,
			"content_len":     utf8.RuneCountInString(existing.Content),
			"similarity":      sim,
		}},
	)
}
