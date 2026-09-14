package domain

// ─── The lexical second section (aihub#360) ──────────────────────────────────
//
// Both retrieval surfaces — pf_recall (memories) and pf_list_work_items (work
// items) — answer a `query` with a SINGLE-VECTOR semantic ranking: one
// unchunked embedding per row, cosine against one embedded query. That shape
// has a structural blind spot which is a property of the index, not a data
// defect: an EXCERPT of a stored document is embedded as a different point
// than its parent, so the parent routinely does not come back.
//
// Measured in the REPAIRED embedding space, 2026-09-14 (aihub#660 arm 1), over
// the same 44 queries aihub#367 froze and committed at ce13215 BEFORE the first
// query ran, criterion unchanged character-for-character:
//
//	memories: @1 20/42, @5 26/42, @10 30/42 — twelve queries still miss at
//	@10, five of them structurally unwinnable under the frozen criterion.
//
// What those twelve lack is a lexical component, and no ranking change reaches
// them. The canonical sample is the blind spot isolated: the query
// `already_held empty` cannot retrieve the document whose first line is
// `already_held: []`.
//
// ⚠️ HISTORICAL, and withdrawn as evidence (aihub#677). This header used to
// cite aihub#367's 2026-09-06 reading — @1 0/42, production-shape @10 33.3%
// against a 14.1% random baseline, work items 0/6 at EVERY N — and to conclude,
// from "11 of the 12 production-shape misses were retrieved by a different
// query" plus "all 161 embeddable memories carried a vector", that the failures
// were not a data gap and NO RE-EMBEDDING FIXED THEM. Both steps fail:
//
//   - the reading was taken through the serving defect aihub#648 found in
//     production TEI (a causal checkpoint forwarded under bidirectional
//     attention; cosine 0.139-0.348 to a correct causal reference, 17/17
//     documents), so it measured serving and index shape at once and separated
//     neither;
//   - "every row has a vector" excludes a MISSING vector and nothing more. A
//     vector in the WRONG SPACE is present, is indistinguishable by emb_model,
//     and is exactly what the corpus held. aihub#650 re-embedded it and the
//     identical frozen set went 0/1/2 to 20/26/30 at @1/@5/@10.
//
// The claim this file is built on survives — a single unchunked vector per row
// cannot answer a verbatim-excerpt query, and twelve of them still miss in the
// repaired space. Its 2026-09-06 evidence does not. Do not quote those numbers
// forward.
//
// This file is the shared half of the repair the owner ruled for on 2026-09-06
// (aihub#360, option B): a lexical retrieval section returned ALONGSIDE the
// semantic one, never merged into it. Two sections because the two scores are
// incomparable — a cosine and a substring match cannot be sorted into one list
// without inventing a fused score, which is the shape aihub#311 removed as a
// defect and aihub#276 refuses on principle (do not compute a verdict you
// cannot compute). Consequences, all deliberate:
//
//   - SemanticInfo and every field on the semantic section are UNTOUCHED; the
//     lexical section carries its own denominator (`total`).
//   - A lexical hit carries NO similarity. It has no cosine, and publishing a
//     fake one is exactly the lie option B exists to avoid.
//   - An empty lexical section is an explicit, present `total: 0` — the
//     strongest "these tokens appear in no visible row" signal this system can
//     give — never an omitted field. Presence is keyed on the REQUEST (a
//     non-empty query), not on the result.
//
// ─── Why ILIKE substring, not pg_trgm and not tsquery ───────────────────────
//
// The failure shape being repaired is verbatim text that the semantic path
// cannot see — an excerpt, an identifier, an error string. Exact substring
// match is precisely what answers it, and ILIKE needs no extension, no new
// index and no migration at today's corpus size (hundreds of rows per table;
// a sequential scan is microseconds). The alternatives were considered and
// rejected for this wave:
//
//   - pg_trgm: fuzzy matching would help misspelled queries, but it needs
//     CREATE EXTENSION plus a migration, and it produces ANOTHER similarity
//     score — which either leaks into the response (the fake-similarity trap
//     above) or is computed only to be hidden. Revisit if the corpus outgrows
//     sequential ILIKE; that day the change is an index and an extension, not
//     a contract change.
//   - tsquery over the existing content_tsv (migration 0029): the column is
//     built with the 'english' configuration and the corpus is largely
//     Chinese, which the default parser does not segment — a Chinese excerpt
//     degrades to whole-blob tokens and matches nothing. It also stems English
//     (`already_held empty` must still match the literal `already_held: []`,
//     which AND-of-lexemes does not), and work_items has no tsvector column at
//     all, so the wi side would have needed a migration anyway. (A separate
//     recall_algo="lexical" branch used content_tsv for RANKING; aihub#632
//     retired it with the parameter, leaving this file's sections as the only
//     lexical semantics.)
//
// The predicate is AND-of-tokens: the query is split on Unicode whitespace and
// every token must appear as a case-insensitive substring. That keeps the two
// shapes that matter working — a verbatim excerpt matches (each of its tokens
// is verbatim in the parent, including multi-line excerpts, whose lines become
// separate tokens at the newlines), and Chinese text, which carries few or no
// spaces, matches as whole contiguous phrases. A long natural-language query
// whose tokens do not all appear verbatim correctly returns 0 lexical hits —
// that query is the semantic section's job.
//
// Ordering inside the section is reference-time descending — the same recency
// key the text recall path uses — because it is deterministic and invents no
// relevance number. `total` reports the full match count so a capped page is
// never mistaken for the whole answer.

import (
	"sort"
	"strings"
)

// LexicalMatchAllTokensSubstring is the published value of a lexical section's
// `match` field: every token must appear as a case-insensitive substring.
// A constant so the two sections (memory and work item) cannot drift apart,
// and so the tool descriptions can promise one spelling.
const LexicalMatchAllTokensSubstring = "all_tokens_substring"

// lexicalMaxTokens caps how many tokens the predicate ANDs together, keeping
// the SQL bounded when the query is a whole document. The cap keeps the
// LONGEST tokens — the most discriminative ones — and dropping a token only
// WIDENS the predicate (AND over a subset admits a superset of rows), so a
// capped query can never lose the target a full-token predicate would find.
// The drop is disclosed in the section's tokens_dropped field.
const lexicalMaxTokens = 16

// lexicalSnippetMaxRunes bounds the per-hit snippet. A hit is a pointer plus
// the evidence line it matched on, not a body: the full text stays one
// pf_get_memory / pf_get_work_item away.
const lexicalSnippetMaxRunes = 160

// lexicalTokens splits a query into the match tokens the lexical predicate
// ANDs together: Unicode-whitespace fields, case-insensitively deduplicated,
// longest first, capped at lexicalMaxTokens. dropped reports how many distinct
// tokens the cap removed (0 when none were).
func lexicalTokens(query string) (used []string, dropped int) {
	fields := strings.Fields(query)
	seen := make(map[string]bool, len(fields))
	tokens := make([]string, 0, len(fields))
	for _, f := range fields {
		key := strings.ToLower(f)
		if seen[key] {
			continue
		}
		seen[key] = true
		tokens = append(tokens, f)
	}
	// Longest first (by rune count), stable so equal-length tokens keep query
	// order — the ordering is what makes the cap keep the discriminative ones,
	// and it also fixes which token lexicalSnippet anchors on.
	sort.SliceStable(tokens, func(i, j int) bool {
		return len([]rune(tokens[i])) > len([]rune(tokens[j]))
	})
	if len(tokens) > lexicalMaxTokens {
		return tokens[:lexicalMaxTokens], len(tokens) - lexicalMaxTokens
	}
	return tokens, 0
}

// lexicalPattern renders one token as an ILIKE pattern: `%<escaped>%`, with
// the three LIKE metacharacters escaped so a token containing `%`, `_` or `\`
// matches itself literally rather than acting as a wildcard.
func lexicalPattern(token string) string {
	esc := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(token)
	return "%" + esc + "%"
}

// lexicalSnippet returns the evidence line for a hit: the line of content
// containing the first case-insensitive occurrence of the longest token,
// trimmed and capped at lexicalSnippetMaxRunes runes, with the window shifted
// so the match itself stays visible when the line is longer than the cap.
//
// tokens must be lexicalTokens output (longest first). A content the anchor
// token does not occur in — reachable on the work-item side, where the SQL
// matches goal OR content per token — yields the first non-blank line instead,
// so a hit never comes back with an empty snippet.
//
// Byte offsets are taken against strings.ToLower(content). For the handful of
// Unicode code points whose lowercase form changes byte length the offsets can
// drift by a few bytes; every slice below is clamped, so the worst case is a
// snippet window shifted slightly off the match, never a panic.
func lexicalSnippet(content string, tokens []string) string {
	if len(tokens) == 0 {
		return capRunes(firstNonBlankLine(content), lexicalSnippetMaxRunes)
	}
	lower := strings.ToLower(content)
	idx := strings.Index(lower, strings.ToLower(tokens[0]))
	if idx < 0 || idx > len(content) {
		return capRunes(firstNonBlankLine(content), lexicalSnippetMaxRunes)
	}
	lineStart := strings.LastIndexByte(content[:idx], '\n') + 1
	lineEnd := len(content)
	if rel := strings.IndexByte(content[idx:], '\n'); rel >= 0 {
		lineEnd = idx + rel
	}
	line := strings.TrimSpace(content[lineStart:lineEnd])
	runes := []rune(line)
	if len(runes) <= lexicalSnippetMaxRunes {
		return line
	}
	// The line overflows the cap: keep the match in view by starting the
	// window shortly before it. Offsets are recomputed against the TRIMMED
	// line, in runes.
	matchAt := strings.Index(strings.ToLower(line), strings.ToLower(tokens[0]))
	if matchAt < 0 {
		matchAt = 0
	}
	matchRune := len([]rune(line[:min(matchAt, len(line))]))
	start := 0
	if matchRune > lexicalSnippetMaxRunes-40 {
		start = matchRune - 40
	}
	if start+lexicalSnippetMaxRunes > len(runes) {
		start = len(runes) - lexicalSnippetMaxRunes
	}
	return string(runes[start : start+lexicalSnippetMaxRunes])
}

// firstNonBlankLine returns content's first line holding any non-whitespace,
// or "" for a blank content.
func firstNonBlankLine(content string) string {
	for _, raw := range strings.Split(content, "\n") {
		if line := strings.TrimSpace(raw); line != "" {
			return line
		}
	}
	return ""
}

// capRunes truncates s to at most n runes.
func capRunes(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n])
	}
	return s
}
