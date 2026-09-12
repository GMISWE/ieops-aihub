package domain

// The work-item half of the aihub#360 lexical section — see lexical.go for the
// measurement (aihub#367: pf_list_work_items(query=) scored 0/6 at EVERY N,
// the worst of the three measured families) and the design rulings.
//
// Unlike the memory side, a text fallback DOES exist here (the ILIKE clause in
// buildListWorkItemsWhere), but it answers only when the vector path cannot —
// no provider, or an empty vector page. The measured failure is the OTHER
// case: the vector path answers, misses, and its full page reads as the whole
// answer. This section runs on every query= request regardless of which path
// served items[], so a caller never has to know the server's provider state to
// get the substring answer.

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// WorkItemLexicalHit is one row of pf_list_work_items' lexical section: a
// pointer plus the evidence line it matched on. No similarity — a substring
// match has no cosine — and no goal/content body; the record is one
// pf_get_work_item(id) away.
type WorkItemLexicalHit struct {
	ID        string    `json:"id"`
	Slug      string    `json:"slug"`
	Status    string    `json:"status"`
	Snippet   string    `json:"snippet"`
	CreatedAt time.Time `json:"created_at"`
}

// WorkItemLexicalSection is pf_list_work_items' lexical section — present on
// every request that carried a non-empty query, including when nothing
// matched: Total is never omitted and Items marshals as [], because "these
// tokens appear in no visible work item" is the answer this section exists to
// state and an absent field cannot state it (aihub#360 requirement 1).
type WorkItemLexicalSection struct {
	Match         string               `json:"match"`
	TokensUsed    int                  `json:"tokens_used"`
	TokensDropped int                  `json:"tokens_dropped,omitempty"`
	Items         []WorkItemLexicalHit `json:"items"`
	Total         int                  `json:"total"`
}

// listWorkItemsLexical runs the lexical half of a two-section work-item list
// (aihub#360).
//
// Scoping reuses buildListWorkItemsWhere with the same filter the caller sent,
// minus the three knobs that cannot apply here: Query (replaced by the token
// predicate), Cursor (this section does not paginate) and MinSimilarity (a
// vector-path floor; there is no similarity on this path — the builder never
// reads it, cleared for the reader). Each token must appear in goal OR
// content, matching what the embedding input covers (WorkItemEmbedInput is
// goal+content) so the two sections answer about the same document.
//
// An error fails the whole list rather than degrading to an absent section —
// absence must keep meaning "no query was sent"; see recallLexical.
func listWorkItemsLexical(ctx context.Context, pool *pgxpool.Pool, project string, f ListWorkItemsFilter) (*WorkItemLexicalSection, *AihubError) {
	tokens, dropped := lexicalTokens(*f.Query)
	sec := &WorkItemLexicalSection{
		Match:         LexicalMatchAllTokensSubstring,
		TokensUsed:    len(tokens),
		TokensDropped: dropped,
		Items:         []WorkItemLexicalHit{},
	}
	if len(tokens) == 0 {
		return sec, nil
	}

	tf := f
	tf.Query = nil
	tf.Cursor = nil
	tf.MinSimilarity = 0
	joinClause, where, args := buildListWorkItemsWhere(project, tf)

	for _, tok := range tokens {
		n := len(args) + 1
		args = append(args, lexicalPattern(tok))
		cond := fmt.Sprintf("(wi.goal ILIKE $%d OR wi.content ILIKE $%d)", n, n)
		if where == "" {
			where = "WHERE " + cond
		} else {
			where += " AND " + cond
		}
	}

	// Single-assign rather than if-init, so the aihub#607 QueryRow census
	// (nontx_query_errors_test.go) sees this site instead of holding it as a
	// blind-spot shape.
	var total int
	err := pool.QueryRow(ctx,
		fmt.Sprintf("SELECT count(*) FROM work_items wi%s %s", joinClause, where),
		args...).Scan(&total)
	if err != nil {
		return nil, dbErrCause(err, "lexical work-item count query")
	}
	sec.Total = total
	if total == 0 {
		return sec, nil
	}

	// wi.content is read to compute the snippet and never leaves this
	// function: the hit carries a <=160-rune evidence line, so the "list
	// responses never serve bodies" property the cards publish still holds on
	// the wire. This SELECT is not one of the full-record pair
	// list_work_items_select_columns_test.go censuses (it carries neither
	// declared_resources nor resources_version).
	query := fmt.Sprintf(`
		SELECT wi.id, wi.slug, wi.status, wi.goal, wi.content, wi.created_at
		FROM work_items wi%s
		%s
		ORDER BY wi.created_at DESC, wi.id DESC
		LIMIT %d`, joinClause, where, f.Limit)

	rows, err := pool.Query(ctx, query, args...)
	if err != nil {
		return nil, dbErrCause(err, "lexical work-item query")
	}
	defer rows.Close()

	for rows.Next() {
		var hit WorkItemLexicalHit
		var goal string
		var content *string
		if scanErr := rows.Scan(&hit.ID, &hit.Slug, &hit.Status, &goal, &content, &hit.CreatedAt); scanErr != nil {
			return nil, dbErrCause(scanErr, "failed to scan lexical work-item row")
		}
		doc := goal
		if content != nil && *content != "" {
			doc = goal + "\n" + *content
		}
		hit.Snippet = lexicalSnippet(doc, tokens)
		sec.Items = append(sec.Items, hit)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, dbErrCause(err, "lexical work-item rows error")
	}
	return sec, nil
}
