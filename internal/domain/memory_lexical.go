package domain

// The memory half of the aihub#360 lexical section — see lexical.go for the
// measurement (aihub#367, 2026-09-06) and the design rulings this implements.
//
// Note what this deliberately is NOT: a re-ranking of items[]. A separate
// "lexical" once existed there — the recall_algo="lexical" branch in
// recallText, which RANKED the text path's page by ts_rank over content_tsv,
// still inside the single items[] list, and (as read from the control flow in
// recallRouted) was unreachable while an embedding provider was live and no
// work_item_id filter was set — the vector path answered first. aihub#632
// retired that branch with the recall_algo parameter, so this file is the one
// "lexical" in pf_recall: a SECOND, parallel result set by exact substring, on
// every recall that carries a query, whichever path served items[].

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// MemoryLexicalHit is one row of pf_recall's lexical section: a pointer plus
// the evidence line it matched on. Deliberately compact — no content body, no
// strength, and NO similarity (a substring match has no cosine; inventing one
// is the fused-score defect aihub#311 removed). The full memory is one
// pf_get_memory(id) away.
type MemoryLexicalHit struct {
	ID         string    `json:"id"`
	Type       string    `json:"type"`
	WorkItemID *string   `json:"work_item_id,omitempty"`
	Snippet    string    `json:"snippet"`
	CreatedAt  time.Time `json:"created_at"`
}

// MemoryLexicalSection is pf_recall's lexical section. Present on every recall
// whose request carried a non-empty query — INCLUDING when nothing matched:
// Total is never omitted and Items marshals as [] rather than null, because
// "these tokens appear in no visible row" is the answer a caller consults this
// section for, and an absent field cannot state it (aihub#360 requirement 1).
type MemoryLexicalSection struct {
	// Match is LexicalMatchAllTokensSubstring — how Items were selected.
	Match string `json:"match"`
	// TokensUsed / TokensDropped disclose the predicate actually applied:
	// lexicalTokens keeps the lexicalMaxTokens longest tokens and dropping only
	// widens the match, but a caller comparing total against expectation
	// deserves to know the cap fired.
	TokensUsed    int `json:"tokens_used"`
	TokensDropped int `json:"tokens_dropped,omitempty"`
	// Items are the top matches by reference time (the text path's recency
	// key), capped at the request's top_k. No relevance ordering exists here to
	// publish — that is the semantic section's job.
	Items []MemoryLexicalHit `json:"items"`
	// Total counts every visible match, independent of the page cap.
	Total int `json:"total"`
}

// recallLexical runs the lexical half of a two-section recall (aihub#360).
//
// The predicate reuses Recall's scoping — project, status set (per
// include_archived), expiry, the caller-derived visibility clauses, the type
// filter and the RESOLVED work_item_id — then ANDs one ILIKE per token. Two
// request knobs are deliberately NOT applied: min_strength (decay is a
// relevance prior, and this section answers existence — hiding an old, weak
// memory here would re-open the exact blind spot the section exists to close)
// and similarity_threshold (there is no similarity to threshold). Cursor
// paging is not consumed and none is emitted, same as the vector path.
//
// An error fails the whole recall rather than degrading to an absent section:
// absence must keep meaning "no query was sent" (aihub#360 requirement 1), and
// a silently missing section would read as exactly the false negative this
// feature repairs.
func recallLexical(ctx context.Context, pool *pgxpool.Pool, req *RecallRequest) (*MemoryLexicalSection, error) {
	tokens, dropped := lexicalTokens(req.Query)
	sec := &MemoryLexicalSection{
		Match:         LexicalMatchAllTokensSubstring,
		TokensUsed:    len(tokens),
		TokensDropped: dropped,
		Items:         []MemoryLexicalHit{},
	}
	if len(tokens) == 0 {
		// A whitespace-only query has no tokens to match; 0 hits of 0 tokens is
		// the honest answer, not an error.
		return sec, nil
	}

	args := []any{req.Project}
	idx := 2

	statusSet := "'active'"
	if req.IncludeArchived {
		statusSet = "'active','archived'"
	}
	where := fmt.Sprintf(`
		project = $1
		AND status IN (%s)
		AND (expires_at IS NULL OR expires_at > clock_timestamp())`, statusSet)

	// Visibility scoping — the same memoryVisibilityScopeSQL call recallText
	// makes (aihub#379: one SQL copy, not a mirror that can drift). This is
	// authorization, so it is the one part of the semantic predicate the
	// lexical section must never relax.
	if clause, visArgs, nextIdx := memoryVisibilityScopeSQL(req.CallerRole, req.CallerUserID, idx); clause != "" {
		where += clause
		args = append(args, visArgs...)
		idx = nextIdx
	}

	if clause, clauseArgs, nextIdx := typeFilterClause(req.Types, idx); clause != "" {
		where += " AND " + clause
		args = append(args, clauseArgs...)
		idx = nextIdx
	}

	// Same rule as recallText: the value bound here is the one Recall resolved
	// (aihub#363), never the caller's raw reference.
	if req.WorkItemID != nil {
		where += fmt.Sprintf(" AND work_item_id = $%d", idx)
		args = append(args, *req.WorkItemID)
		idx++
	}

	for _, tok := range tokens {
		where += fmt.Sprintf(" AND content ILIKE $%d", idx)
		args = append(args, lexicalPattern(tok))
		idx++
	}

	total, terr := countMemories(ctx, pool, where, args)
	if terr != nil {
		return nil, dbErrCause(terr, "lexical recall count query")
	}
	sec.Total = total
	if total == 0 {
		return sec, nil
	}

	topK := req.TopK
	if topK <= 0 {
		topK = 20
	}
	args = append(args, topK)
	query := fmt.Sprintf(`
		SELECT id, type, work_item_id, content, created_at
		FROM memories
		WHERE %s
		ORDER BY `+memRefTimeSQL+` DESC, id DESC
		LIMIT $%d`, where, idx)

	rows, err := pool.Query(ctx, query, args...)
	if err != nil {
		return nil, dbErrCause(err, "lexical recall query")
	}
	defer rows.Close()

	for rows.Next() {
		var hit MemoryLexicalHit
		var content string
		if scanErr := rows.Scan(&hit.ID, &hit.Type, &hit.WorkItemID, &content, &hit.CreatedAt); scanErr != nil {
			return nil, dbErrCause(scanErr, "failed to scan lexical recall row")
		}
		hit.Snippet = lexicalSnippet(content, tokens)
		sec.Items = append(sec.Items, hit)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, dbErrCause(err, "lexical recall rows error")
	}
	return sec, nil
}
