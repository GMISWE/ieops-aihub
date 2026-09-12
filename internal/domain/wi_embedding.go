package domain

// aihub#273: best-effort embedding for work items, mirroring the memory write
// path (memory.go, aihub#192): the embedding is computed BEFORE any transaction
// begins (it is a network call), and a provider failure logs a warning and
// leaves the emb_* columns NULL — semantic search quality degrades, correctness
// never does (the ILIKE text fallback still finds the row).

import (
	"context"
	"fmt"
	"os"
	"unicode/utf8"

	"github.com/jackc/pgx/v5/pgxpool"
)

// aihub#361: the wi embed text used to be built by a local wiEmbedInput here,
// with cmd/aihub-embed-backfill spelling out its own near-copy (same 6000-rune
// budget, but no TrimSpace and an unconditional "\n\n" separator). Both writers
// now call WorkItemEmbedInput in embed_input.go, so the composition and the
// budget cannot drift apart again.

// embedWorkItemBestEffort returns the pgvector literal / model / dims /
// embedded rune count for the given wi text, or all-nil when embedding is
// disabled, the input is empty, or the provider fails.
//
// embeddedLen (aihub#504, migration 0039) is the rune count of the composed
// goal+content input the vector actually embeds — WorkItemEmbedInput truncates
// at the input budget, and before this value was recorded that truncation was
// visible nowhere on the row. Returned alongside the vector so no writer can
// store one without the other.
func embedWorkItemBestEffort(ctx context.Context, goal, content string) (vecLit, model *string, dims, embeddedLen *int) {
	if isNoopProvider(embProvider) {
		return nil, nil, nil, nil
	}
	in := WorkItemEmbedInput(goal, content)
	if in == "" {
		return nil, nil, nil, nil
	}
	vec, err := embProvider.Embed(ctx, in)
	if err != nil || len(vec) == 0 {
		fmt.Fprintf(os.Stderr, "work_items: embed failed (leaving emb_vector NULL): %v\n", err)
		return nil, nil, nil, nil
	}
	lit := vecToPGLiteral(vec)
	m := embProvider.ModelID()
	d := embProvider.Dims()
	n := utf8.RuneCountInString(in)
	return &lit, &m, &d, &n
}

// refreshWorkItemEmbeddingBestEffort recomputes the embedding for a wi whose
// goal/content just changed (UpdateWorkItem). Runs outside any transaction;
// every failure is logged and swallowed.
func refreshWorkItemEmbeddingBestEffort(ctx context.Context, pool *pgxpool.Pool, wiID string) {
	if isNoopProvider(embProvider) {
		return
	}
	var goal string
	var content *string
	if err := pool.QueryRow(ctx, `SELECT goal, content FROM work_items WHERE id = $1`, wiID).Scan(&goal, &content); err != nil {
		fmt.Fprintf(os.Stderr, "work_items: embed refresh read failed id=%s: %v\n", wiID, err)
		return
	}
	c := ""
	if content != nil {
		c = *content
	}
	vecLit, model, dims, embeddedLen := embedWorkItemBestEffort(ctx, goal, c)
	if vecLit == nil {
		return
	}
	if _, err := pool.Exec(ctx,
		`UPDATE work_items SET emb_vector = $1::vector, emb_model = $2, emb_dims = $3, embedded_len = $4 WHERE id = $5`,
		*vecLit, *model, *dims, *embeddedLen, wiID); err != nil {
		fmt.Fprintf(os.Stderr, "work_items: embed refresh write failed id=%s: %v\n", wiID, err)
	}
}
