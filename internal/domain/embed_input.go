package domain

// The ONE place that decides what text is handed to the embedding provider
// (aihub#361).
//
// WHAT WENT WRONG
// ---------------
// The rule lived in four places and disagreed with itself in the one that
// mattered:
//
//	memory.go:975                     embProvider.Embed(ctx, req.Content)   ← no cap at all
//	cmd/aihub-embed-backfill:103-104  cap 6000 runes (memories)
//	cmd/aihub-embed-backfill:156-157  cap 6000 runes (work items)
//	cmd/aihub-embed-verify:63         cap 6000 runes, to reproduce the backfill
//	wi_embedding.go:22                cap 6000 runes (work items, live)
//
// Two consequences, both silent:
//
//	1. A memory whose content exceeds the provider's context window fails to
//	   embed on the LIVE write path — Remember logs a warning and stores
//	   emb_vector = NULL. The row is then permanently invisible to vector
//	   recall until somebody runs the backfill, and recall's `total` counts
//	   only the embedded subset, so the absence reads as "not in the corpus".
//	2. For a memory between the cap and the provider limit, the live path
//	   stored a FULL-TEXT vector and any later backfill overwrites it with a
//	   PREFIX vector. emb_model is byte-identical in both cases, so the two
//	   populations cannot be told apart from the data — one index, two
//	   different embedding semantics.
//
// Fixing it by copying the `if len(rr) > 6000` into memory.go would have left
// five copies of a rule that had already drifted once. Every writer now calls
// one of the two functions below, so a future change to the budget or to the
// composition moves all of them together or none of them.
//
// The direction of the fix was forced: the cap cannot be removed from the
// backfill, because it exists to stay under the provider's context length
// ("input length exceeds the context length"). So the live path gains the cap.

import "strings"

// embedInputMaxRunes caps the text handed to the embedding provider.
//
// Runes, not bytes: a byte-sliced cap would split a multi-byte rune and feed
// the provider invalid UTF-8. The value is the one the backfill has always
// used; the leading runes carry the gist.
const embedInputMaxRunes = 6000

// MemoryEmbedInput builds the exact text a memories row is embedded from.
//
// Exported so cmd/aihub-embed-backfill and cmd/aihub-embed-verify embed the
// same bytes the live Remember path does. Deliberately does NOT trim: memory
// content is stored verbatim and the live path has never trimmed it, so
// trimming here would silently re-point every re-embedded vector.
func MemoryEmbedInput(content string) string {
	return truncateEmbedInput(content)
}

// WorkItemEmbedInput builds the exact text a work_items row is embedded from:
// goal first (the densest signal), then content.
//
// The trimming and the "no separator when the goal is empty" rule came from
// the live path (wi_embedding.go); the backfill did neither, which is the same
// class of defect as the memory divergence above, just with a smaller blast
// radius. One function, so there is no second rule to disagree with.
func WorkItemEmbedInput(goal, content string) string {
	s := strings.TrimSpace(goal)
	if c := strings.TrimSpace(content); c != "" {
		if s != "" {
			s += "\n\n"
		}
		s += c
	}
	return truncateEmbedInput(s)
}

// truncateEmbedInput is the shared budget. Both builders go through it so the
// cap is written down once.
func truncateEmbedInput(s string) string {
	if rr := []rune(s); len(rr) > embedInputMaxRunes {
		return string(rr[:embedInputMaxRunes])
	}
	return s
}
