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
// The DIRECTION of the fix was forced: the cap cannot be removed from the
// backfill, because it exists to stay under the provider's context length
// ("input length exceeds the context length"). So the live path gains the cap.
//
// The VALUE was not forced — see embedInputMaxRunes below. Saying "we had no
// choice" about both would be the same species of overclaim this change exists
// to remove from the code's comments.

import (
	"strings"

	"github.com/GMISWE/ieops-aihub/internal/embedding"
)

// embedInputMaxRunes caps the text handed to the embedding provider.
//
// Runes, not bytes: a byte-sliced cap would split a multi-byte rune and feed
// the provider invalid UTF-8.
//
// 🔴 The VALUE is inherited, not derived. It was picked for the backfill (which
// must survive rows over 300 KB) and aihub#361 propagated it to the live path so
// both writers embed identical bytes. The fix forced "there must be some cap";
// it did not force this number, and nothing in internal/embedding holds a
// provider context length to check it against. Read
// embedding.DefaultInputMaxRunes for the full statement of what is and is not
// known here.
//
// Resolved once at package init from EMBEDDING_INPUT_MAX_RUNES so the server,
// cmd/aihub-embed-backfill and cmd/aihub-embed-verify all read the same
// environment and cannot be pointed at different budgets. Deliberately NOT an
// exported setter: a setter is a thing a caller can forget to call, and one
// caller forgetting is precisely the drift this file exists to end.
var embedInputMaxRunes = embedding.InputMaxRunes()

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
