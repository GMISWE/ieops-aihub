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

	"github.com/jackc/pgx/v5/pgxpool"
)

// aihub#361: the wi embed text used to be built by a local wiEmbedInput here,
// with cmd/aihub-embed-backfill spelling out its own near-copy (same 6000-rune
// budget, but no TrimSpace and an unconditional "\n\n" separator). Both writers
// now call WorkItemEmbedInput in embed_input.go, so the composition and the
// budget cannot drift apart again.

// embedWorkItemBestEffort returns the pgvector literal / model / dims for the
// given wi text, or (nil, nil, nil) when embedding is disabled, the input is
// empty, or the provider fails.
func embedWorkItemBestEffort(ctx context.Context, goal, content string) (vecLit, model *string, dims *int) {
	if isNoopProvider(embProvider) {
		return nil, nil, nil
	}
	in := WorkItemEmbedInput(goal, content)
	if in == "" {
		return nil, nil, nil
	}
	vec, err := embProvider.Embed(ctx, in)
	if err != nil || len(vec) == 0 {
		fmt.Fprintf(os.Stderr, "work_items: embed failed (leaving emb_vector NULL): %v\n", err)
		return nil, nil, nil
	}
	lit := vecToPGLiteral(vec)
	m := embProvider.ModelID()
	d := embProvider.Dims()
	return &lit, &m, &d
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
	vecLit, model, dims := embedWorkItemBestEffort(ctx, goal, c)
	if vecLit == nil {
		return
	}
	if _, err := pool.Exec(ctx,
		`UPDATE work_items SET emb_vector = $1::vector, emb_model = $2, emb_dims = $3 WHERE id = $4`,
		*vecLit, *model, *dims, wiID); err != nil {
		fmt.Fprintf(os.Stderr, "work_items: embed refresh write failed id=%s: %v\n", wiID, err)
	}
}
