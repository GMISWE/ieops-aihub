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

	// Embeddable types only (mirrors domain.embeddableType); skip methodology.*
	// which is fetched deterministically by work_item_id. Re-embed rows with a
	// stale emb_model so a provider switch can be backfilled.
	rows, err := pool.Query(ctx, `
		SELECT id, content FROM memories
		WHERE status = 'active'
		  AND (type LIKE 'experience.%' OR type LIKE 'fact.%' OR type LIKE 'rule.%')
		  AND (emb_vector IS NULL OR emb_model IS DISTINCT FROM $1)`, model)
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
		vec, embErr := prov.Embed(ctx, it.content)
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
	fmt.Printf("backfill done: %d ok, %d failed\n", ok, fail)
	if fail > 0 {
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
