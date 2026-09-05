// Command aihub-embed-backfill computes embeddings for existing memories that
// have no emb_vector yet (or were embedded by a different model), so that the
// vector recall path (aihub#192) covers the pre-existing corpus.
//
// One-shot, idempotent: re-running only touches rows still missing a vector for
// the current EMBEDDING_MODEL. Reuses the same EMBEDDING_* config as the server.
//
//	DATABASE_URL=... EMBEDDING_ENABLED=true EMBEDDING_PROVIDER=openai \
//	EMBEDDING_BASE_URL=http://embed:8090 EMBEDDING_MODEL=qwen3-embedding \
//	EMBEDDING_DIMS=4096 aihub-embed-backfill
package main

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/GMISWE/ieops-aihub/internal/db"
	"github.com/GMISWE/ieops-aihub/internal/domain"
	"github.com/GMISWE/ieops-aihub/internal/embedding"
)

func main() {
	ctx := context.Background()

	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		fmt.Fprintln(os.Stderr, "DATABASE_URL required")
		os.Exit(1)
	}

	prov, err := embedding.FromEnv()
	if err != nil {
		fmt.Fprintln(os.Stderr, "embedding.FromEnv:", err)
		os.Exit(1)
	}
	if _, isNoop := prov.(*embedding.NoopProvider); isNoop {
		fmt.Fprintln(os.Stderr, "embedding is disabled (NoopProvider) — set EMBEDDING_ENABLED=true plus provider config")
		os.Exit(1)
	}
	if err := prov.Ping(ctx); err != nil {
		fmt.Fprintln(os.Stderr, "embedding backend unreachable:", err)
		os.Exit(1)
	}
	model, dims := prov.ModelID(), prov.Dims()

	pool, err := db.New(ctx, dsn)
	if err != nil {
		fmt.Fprintln(os.Stderr, "db connect:", err)
		os.Exit(1)
	}
	defer pool.Close()

	// Embeddable types only; skip methodology.* which is fetched deterministically by
	// work_item_id. Re-embed rows with a stale emb_model so a provider switch can be
	// backfilled.
	//
	// The prefix list comes from domain.EmbeddablePrefixes rather than being spelled out
	// here: recall (aihub#270) now hands every non-embeddable type to the text path, so a
	// prefix this backfill disagreed with would be a row that no path embeds and no path
	// text-searches — invisible on both. One list, no drift.
	embClauses := make([]string, 0, len(domain.EmbeddablePrefixes))
	embArgs := []any{model}
	for _, pfx := range domain.EmbeddablePrefixes {
		embArgs = append(embArgs, pfx+"%")
		embClauses = append(embClauses, fmt.Sprintf("type LIKE $%d", len(embArgs)))
	}
	rows, err := pool.Query(ctx, fmt.Sprintf(`
		SELECT id, content FROM memories
		WHERE status = 'active'
		  AND (%s)
		  AND (emb_vector IS NULL OR emb_model IS DISTINCT FROM $1)`,
		strings.Join(embClauses, " OR ")), embArgs...)
	if err != nil {
		fmt.Fprintln(os.Stderr, "query:", err)
		os.Exit(1)
	}
	type item struct{ id, content string }
	var todo []item
	for rows.Next() {
		var it item
		if err := rows.Scan(&it.id, &it.content); err != nil {
			fmt.Fprintln(os.Stderr, "scan:", err)
			os.Exit(1)
		}
		todo = append(todo, it)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		fmt.Fprintln(os.Stderr, "rows:", err)
		os.Exit(1)
	}

	fmt.Printf("backfill: %d memories to embed with model=%q dims=%d\n", len(todo), model, dims)
	var ok, fail int
	for i, it := range todo {
		// opt3 / aihub#361: the truncation this loop used to spell out inline lives in
		// domain.MemoryEmbedInput, which the live write path (domain.Remember) now calls
		// too. Same function, same bytes — a backfill can no longer replace a live vector
		// with a vector of different text under an identical emb_model.
		embInput := domain.MemoryEmbedInput(it.content)
		vec, embErr := prov.Embed(ctx, embInput)
		if embErr != nil || len(vec) == 0 {
			fail++
			fmt.Fprintf(os.Stderr, "  embed failed id=%s: %v\n", it.id, embErr)
			continue
		}
		if _, err := pool.Exec(ctx,
			`UPDATE memories SET emb_vector = $1::vector, emb_model = $2, emb_dims = $3, updated_at = clock_timestamp() WHERE id = $4`,
			vecLiteral(vec), model, dims, it.id,
		); err != nil {
			fail++
			fmt.Fprintf(os.Stderr, "  update failed id=%s: %v\n", it.id, err)
			continue
		}
		ok++
		if (i+1)%20 == 0 {
			fmt.Printf("  ... %d/%d\n", i+1, len(todo))
		}
	}
	fmt.Printf("backfill done (memories): %d ok, %d failed\n", ok, fail)

	// aihub#273: same pass for work_items (goal + content). All statuses on
	// purpose — the point of wi semantic search is finding similar HISTORICAL
	// work, which is mostly wrapped/cancelled rows.
	type wiRow struct{ id, goal, content string }
	var wtodo []wiRow
	wrows, err := pool.Query(ctx, `
		SELECT id, goal, COALESCE(content, '') FROM work_items
		WHERE emb_vector IS NULL OR emb_model IS DISTINCT FROM $1`, model)
	if err != nil {
		fmt.Fprintln(os.Stderr, "query work_items:", err)
		os.Exit(1)
	}
	for wrows.Next() {
		var r wiRow
		if err := wrows.Scan(&r.id, &r.goal, &r.content); err != nil {
			fmt.Fprintln(os.Stderr, "scan work_items:", err)
			os.Exit(1)
		}
		wtodo = append(wtodo, r)
	}
	wrows.Close()

	fmt.Printf("backfill: %d work_items to embed with model=%q dims=%d\n", len(wtodo), model, dims)
	var wok, wfail int
	for i, r := range wtodo {
		// aihub#361: same shared builder as domain.embedWorkItemBestEffort. The inline
		// version here differed from the live one in two ways nobody meant — no
		// TrimSpace, and an unconditional separator even for an empty goal.
		embInput := domain.WorkItemEmbedInput(r.goal, r.content)
		vec, embErr := prov.Embed(ctx, embInput)
		if embErr != nil || len(vec) == 0 {
			wfail++
			fmt.Fprintf(os.Stderr, "  embed failed id=%s: %v\n", r.id, embErr)
			continue
		}
		// No updated_at bump: work_items.updated_at keys nothing here and a
		// backfill must not look like a content edit.
		if _, err := pool.Exec(ctx,
			`UPDATE work_items SET emb_vector = $1::vector, emb_model = $2, emb_dims = $3 WHERE id = $4`,
			vecLiteral(vec), model, dims, r.id,
		); err != nil {
			wfail++
			fmt.Fprintf(os.Stderr, "  update failed id=%s: %v\n", r.id, err)
			continue
		}
		wok++
		if (i+1)%50 == 0 {
			fmt.Printf("  ... %d/%d\n", i+1, len(wtodo))
		}
	}
	fmt.Printf("backfill done (work_items): %d ok, %d failed\n", wok, wfail)
	if fail > 0 || wfail > 0 {
		os.Exit(1)
	}
}

// vecLiteral formats a float32 vector as a pgvector text literal "[f,f,...]".
// ponytail: mirrors domain.vecToPGLiteral; duplicated (5 lines) to avoid exporting
// a pg-encoding helper from the domain package just for this one-shot command.
func vecLiteral(v []float32) string {
	var b strings.Builder
	b.WriteByte('[')
	for i, f := range v {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(strconv.FormatFloat(float64(f), 'f', -1, 32))
	}
	b.WriteByte(']')
	return b.String()
}
