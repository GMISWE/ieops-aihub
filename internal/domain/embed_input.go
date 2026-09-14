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
// The VALUE was inherited (picked for the backfill, propagated by aihub#361)
// until aihub#504 derived it from the production provider's measured per-input
// ceiling on 2026-09-12. Read embedding.DefaultInputMaxRunes for the
// derivation — the measured token ceiling, the measured tokens-per-rune band
// of the real corpus, and the margin between them.
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

// ── The QUERY side (aihub#669) ───────────────────────────────────────────────
//
// Everything above composes the text of a STORED ROW. The two functions below
// compose the text of a SEARCH QUERY, and the rule is deliberately different:
//
//	a recall query is embedded with an instruct prefix and a stored document is
//	not. That asymmetry is the model's designed usage, not a defect.
//
// Qwen3-Embedding ships prompts.query (the template below) and
// prompts.document = "" (empty) in its own config_sentence_transformers.json.
// So there is nothing to re-embed: the prefix changes the point a QUESTION maps
// to, never the point a stored row maps to, which is why adopting it needs no
// backfill and why reverting it needs no migration either.
//
// WHAT THE PREFIX BUYS, AND WHAT IT COSTS (aihub#660, measured 2026-09-14 on
// production, 44 frozen queries, criterion unchanged, on the vector space
// aihub#650 repaired)
//
//	recall@1 / @5 / @10       20 / 26 / 30  ->  22 / 29 / 30
//	garbage-string top-1 cos      0.6300    ->      0.3290
//	real queries scoring BELOW that garbage string   17/36  ->  0/36
//
// Two properties of that result are easy to misread, so they are written down
// here rather than left to be re-derived:
//
//  1. The prefix lowers EVERY similarity. It does not raise the score of a
//     right answer; it pushes noise down harder than signal (real-query median
//     -0.0378, the garbage control -0.3010). Anything that reads absolute
//     cosines - a calibrated similarity_threshold or min_similarity, a
//     dashboard, a threshold in someone's notes - is reading a shifted scale
//     from the day this landed. The real-query top-1 median moved 0.6408 ->
//     0.6030.
//  2. The margin is thin. The lowest real query in that set clears the garbage
//     control by 0.0607, because a 91-character prefix dilutes a SHORT query
//     most. If a relative threshold or a relevance filter is ever added, that
//     number is its ceiling, not a comfortable gap.
//
// 🔴 THE PREFIX MUST NEVER REACH THE LEXICAL PATH. The lexical section
// (aihub#360) ANDs one case-insensitive substring match per whitespace token of
// the query. The prefix splits into 14 whitespace fields, and while some of them
// ("a", "the", "that") are everywhere, others ("Instruct:", "passages",
// "retrieve") are in no stored row at all - and under an AND, one such token is
// enough to empty the section. Measured in the same run: lexical target hits
// 2 -> 0, tokens_used mean 10.05 -> 15.64, queries pinned at the 16-token cap
// 13/42 -> 30/42. That is why this rule
// lives at the two Embed call sites and why NOTHING assigns the prefixed text
// back to a request field: memory_lexical.go, wi_lexical.go and
// buildListWorkItemsWhere's ILIKE fallback all read the caller's query string
// directly, and on the work-item side all three read it through the SAME
// *string. A value never written cannot leak.

// QueryEmbedPrefix is the instruct prefix prepended to a recall query before it
// is embedded.
//
// Verbatim from
// https://huggingface.co/Qwen/Qwen3-Embedding-0.6B/resolve/main/config_sentence_transformers.json
// key prompts.query: 91 bytes, one newline before "Query:", and NO separator
// after it, because sentence-transformers concatenates prompt and text so the
// trailing "Query:" fuses with the query's first character. aihub#660 verified
// byte-level delivery against the live server through its tokens_used echo
// (4/4 predictions exact; a newline lost in transport would have made every
// prediction one token low).
//
// Changing a single byte of this - including that newline - changes the point
// every query maps to while every stored vector stays where it is, and nothing
// in the data would show it. embed_query_prefix_test.go pins it byte for byte.
const QueryEmbedPrefix = "Instruct: Given a web search query, retrieve relevant passages that answer the query\nQuery:"

// QueryEmbedInput builds the exact text a search query is embedded from.
//
// Plain concatenation, no separator: see QueryEmbedPrefix.
//
// Exported with no out-of-package caller today, for the reason the two builders
// above are exported: a reproducer that embeds different bytes than the live
// path measures nothing, and as of this change the query side finally has bytes
// to get wrong. cmd/aihub-embed-verify is document-side only, so it needs this
// as little as a benchmark driver needs it badly.
//
// Deliberately does NOT truncate, unlike the two document-side builders above.
// The query side has never had a cap, aihub#660 measured the raw query, and
// adding one here would silently re-point the very queries it means to protect.
// A query long enough to need one is a different work item (aihub#364).
func QueryEmbedInput(query string) string {
	return QueryEmbedPrefix + query
}
