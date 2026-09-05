// Command aihub-embed-verify re-embeds a stored memory's content with the
// currently configured embedding provider and compares the fresh vector
// against the vector already stored in emb_vector, via pgvector's own cosine
// operator (<=>) — the exact operator RecallWithVector uses to rank recall
// results. A cosine close to 1.0 means the stored vector legitimately
// corresponds to the row's content; anything meaningfully below 1.0 means the
// stored vector and the row's content have drifted apart (wrong field
// embedded at write time, an id<->vector mismatch from a backfill run, a
// stale provider swap that never got backfilled, ...).
//
// # Why this exists (aihub#311)
//
// aihub#311 reports that querying a memory with a verbatim substring of its
// own content fails to return that memory — similarities cluster in a narrow,
// non-discriminating band regardless of query content. The leading untested
// hypothesis is that stored emb_vector values simply do not correspond to
// their row's content. The decisive probe is exactly what this command
// automates: re-embed a known row with the current provider and cosine it
// against what is stored. That probe needs a reachable embedding endpoint,
// which is not available from every machine that has DB access (in
// particular, not from the machine this command was written on — see
// wi_zAAnriiC's memories) — hence a small standalone command, for whoever
// holds the embedding-endpoint credential, rather than a test.
//
// Reuses the same EMBEDDING_* config as the server (internal/embedding.FromEnv)
// and the same DATABASE_URL as cmd/aihub-embed-backfill.
//
//	DATABASE_URL=... EMBEDDING_ENABLED=true EMBEDDING_PROVIDER=openai \
//	EMBEDDING_BASE_URL=http://embed:8090 EMBEDDING_MODEL=qwen3-embedding \
//	EMBEDDING_DIMS=1024 aihub-embed-verify -id mem_03hogVcW
//
//	# or sample N already-embedded rows instead of naming one:
//	DATABASE_URL=... EMBEDDING_ENABLED=true ... aihub-embed-verify -sample 5 -project ieops
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/GMISWE/ieops-aihub/internal/db"
	"github.com/GMISWE/ieops-aihub/internal/domain"
	"github.com/GMISWE/ieops-aihub/internal/embedding"
	"github.com/jackc/pgx/v5/pgxpool"
)

// cosineOKThreshold is the pass/fail line printed in the verdict. Re-embedding
// the exact same text twice through the same provider is expected to be
// deterministic (or so close to it that floating-point noise rounds to
// ~0.999+); anything meaningfully below 1.0 means the compared vectors are
// not really vectors of the same text.
const cosineOKThreshold = 0.98

// The text to re-embed comes from domain.MemoryEmbedInput — the same function
// the live write path (domain.Remember) and cmd/aihub-embed-backfill call, so
// this probe reproduces whichever of them wrote the row without needing to know
// which one it was.
//
// Before aihub#361 this file carried its own `const embedInputMax = 6000`
// mirroring the backfill, and the comment here recorded — correctly, at the
// time — that the live path truncated nothing. That made the probe only
// representative of the backfill path for any row over the cap, which is
// precisely the population where the two writers disagreed. Both writers now
// share the builder, so a row over the cap is no longer a special case for the
// comparison; it is still reported below, because the operator should know the
// vector under test covers a prefix of the content.

// truncatedForEmbedding reports whether the builder dropped any of content on
// the way to embInput. Derived by comparing the builder's output against its
// input rather than by re-declaring the budget here: a second copy of that
// number is exactly what aihub#361 was.
//
// This is equality, not a length comparison, so it stays correct if the builder
// ever grows a second transformation. It relies on MemoryEmbedInput being the
// identity for under-budget content, which is pinned by internal/domain's
// TestMemoryEmbedInputPassesUnderBudgetContentThrough.
//
// That citation was wrong in the first cut of this change: it named
// TestMemoryEmbedInputDoesNotTrim, which pins only that surrounding whitespace
// survives — a strictly weaker property that would still hold under a builder
// that mangled the middle of the string and made every row here read as
// truncated. Miscited support for a correct claim is what aihub#361 is about;
// check the test actually asserts what you say it does before pointing at it.
func truncatedForEmbedding(content, embInput string) bool {
	return embInput != content
}

func main() {
	idFlag := flag.String("id", "", "comma-separated memory id(s) to verify; if empty, sample rows instead")
	sample := flag.Int("sample", 5, "number of already-embedded rows to sample when -id is not given")
	project := flag.String("project", "", "restrict sampling to this project (ignored when -id is set)")
	flag.Parse()

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
	curModel, curDims := prov.ModelID(), prov.Dims()

	pool, err := db.New(ctx, dsn)
	if err != nil {
		fmt.Fprintln(os.Stderr, "db connect:", err)
		os.Exit(1)
	}
	defer pool.Close()

	targets, err := resolveTargets(ctx, pool, *idFlag, *project, *sample)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if len(targets) == 0 {
		fmt.Fprintln(os.Stderr, "no rows to verify (no -id given and the sample query found nothing embedded)")
		os.Exit(1)
	}

	fmt.Printf("aihub-embed-verify: current provider model=%q dims=%d, checking %d row(s)\n\n", curModel, curDims, len(targets))

	var mismatches, inconclusive, errored int
	for _, id := range targets {
		var (
			memProject, memType, content, storedModel string
			storedDims                                int
		)
		if err := pool.QueryRow(ctx,
			`SELECT project, type, content, coalesce(emb_model,''), coalesce(emb_dims,0) FROM memories WHERE id = $1`,
			id,
		).Scan(&memProject, &memType, &content, &storedModel, &storedDims); err != nil {
			fmt.Printf("id=%s: FAILED to load row: %v\n\n", id, err)
			errored++
			continue
		}

		embInput := domain.MemoryEmbedInput(content)
		truncated := truncatedForEmbedding(content, embInput)
		contentRunes, embInputRunes := len([]rune(content)), len([]rune(embInput))

		freshVec, embErr := prov.Embed(ctx, embInput)
		if embErr != nil || len(freshVec) == 0 {
			fmt.Printf("id=%s project=%s type=%s: FAILED to re-embed: %v\n\n", id, memProject, memType, embErr)
			errored++
			continue
		}

		fmt.Printf("id=%s project=%s type=%s content_len=%d_runes%s\n", id, memProject, memType, contentRunes, truncNote(truncated, embInputRunes))
		fmt.Printf("  stored : emb_model=%q emb_dims=%d\n", storedModel, storedDims)
		fmt.Printf("  current: emb_model=%q emb_dims=%d\n", curModel, curDims)
		if storedModel != curModel {
			fmt.Printf("  ! stored emb_model differs from the currently configured provider (expected if the provider was swapped since this row was embedded and it has not been backfilled since)\n")
		}
		if storedDims != len(freshVec) {
			fmt.Printf("  ! stored emb_dims=%d differs from freshly computed dims=%d — cosine cannot be computed (pgvector requires equal dimensions)\n\n", storedDims, len(freshVec))
			errored++
			continue
		}

		var cosine float64
		if err := pool.QueryRow(ctx,
			`SELECT 1 - (emb_vector <=> $2::vector) FROM memories WHERE id = $1`,
			id, vecLiteral(freshVec),
		).Scan(&cosine); err != nil {
			fmt.Printf("  cosine(stored, fresh) = ERROR: %v\n\n", err)
			errored++
			continue
		}

		verdict := "OK — stored vector matches its row's content"
		switch {
		case cosine < cosineOKThreshold && truncated:
			// Still inconclusive, but for a narrower reason than before
			// aihub#361. Both writers now go through domain.MemoryEmbedInput,
			// so a row written from now on is comparable whichever path wrote
			// it. A row written by the live Remember path BEFORE that fix
			// carries a full-text vector, and this probe re-embeds only the
			// prefix — a low cosine there is the expected shape of a perfectly
			// legitimate old row, not evidence of drift, and must not be
			// reported as a root-cause finding.
			verdict = fmt.Sprintf("INCONCLUSIVE — cosine is below threshold, but this row's content exceeds the %d-rune embedding budget; a row embedded by the pre-aihub#361 live Remember path carries an UNTRUNCATED vector, so a low cosine here is an expected artifact of comparing prefix-fresh against full-text-stored, not evidence of drift — this result does NOT indicate the aihub#311 root cause and should be disregarded", embInputRunes)
			inconclusive++
		case cosine < cosineOKThreshold:
			verdict = "MISMATCH — stored vector does NOT correspond to this row's content (this is the aihub#311 root cause if it reproduces)"
			mismatches++
		}
		fmt.Printf("  cosine(stored, fresh) = %.6f  -> %s\n\n", cosine, verdict)
	}

	fmt.Printf("done: %d row(s) checked, %d mismatch(es), %d inconclusive (truncated), %d error(s)\n", len(targets), mismatches, inconclusive, errored)
	if mismatches > 0 || errored > 0 {
		os.Exit(1)
	}
}

// resolveTargets returns the memory ids to verify: either the explicit -id
// list, or a sample of already-embedded rows (optionally scoped to -project).
func resolveTargets(ctx context.Context, pool *pgxpool.Pool, idFlag, project string, sample int) ([]string, error) {
	if idFlag != "" {
		var ids []string
		for _, id := range strings.Split(idFlag, ",") {
			id = strings.TrimSpace(id)
			if id != "" {
				ids = append(ids, id)
			}
		}
		return ids, nil
	}

	q := `SELECT id FROM memories WHERE emb_vector IS NOT NULL AND status = 'active'`
	args := []any{}
	if project != "" {
		args = append(args, project)
		q += fmt.Sprintf(` AND project = $%d`, len(args))
	}
	args = append(args, sample)
	q += fmt.Sprintf(` ORDER BY updated_at DESC LIMIT $%d`, len(args))

	rows, err := pool.Query(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("sample query: %w", err)
	}
	defer rows.Close()

	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("sample scan: %w", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("sample rows: %w", err)
	}
	return ids, nil
}

func truncNote(truncated bool, embInputRunes int) string {
	if truncated {
		return fmt.Sprintf(" (truncated to %d runes for embedding by domain.MemoryEmbedInput, the same builder both writers use)", embInputRunes)
	}
	return ""
}

// vecLiteral formats a float32 vector as a pgvector text literal "[f,f,...]".
// ponytail: mirrors domain.vecToPGLiteral; duplicated (5 lines) to avoid
// exporting a pg-encoding helper from the domain package just for this
// one-shot command (same rationale as cmd/aihub-embed-backfill/main.go).
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
