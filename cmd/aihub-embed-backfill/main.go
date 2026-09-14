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
//
//	# full re-embed that also converges archived memories (aihub#625):
//	DATABASE_URL=... EMBEDDING_ENABLED=true ... aihub-embed-backfill -include-archived
//
// # Flags
//
// -include-archived widens the memory selection from active-only to
// active+archived. The default stays active-only on purpose: routine
// maintenance runs keep the population they have always had, and the flag
// exists for full re-embeds where archived over-limit rows ride along
// (the aihub#625 ruling; aihub#637 added the flag). Redacted rows are never
// selected under either mode, and the work_items pass is unaffected: it
// already covers all statuses.
//
// # When the embedding pipeline changes (aihub#661)
//
// A pipeline change is now SELECTED AUTOMATICALLY, and retiring the manual
// pre-clear that used to be required is the whole point of aihub#661. Every
// vector writer stamps memories.emb_pipeline / work_items.emb_pipeline with the
// identity of the pipeline that produced the vector (migration 0041,
// domain.EmbedPipelineID), and the two selections below compare the stored
// stamp's document segment against the current one. So:
//
//   - change the model, the dimensions, the input budget, or
//     EMBEDDING_SERVING_ID, and every row written under the previous value
//     lands in the re-embed set on the next run, with nothing done by hand;
//   - rows written before migration 0041 carry emb_pipeline IS NULL — identity
//     unknown — and are selected by that clause explicitly. One run converges
//     them and the clause never matches them again.
//
// WHAT THIS REPLACED, AND WHY IT WAS EXPENSIVE. Until aihub#661 the only
// clauses here were emb_vector / emb_model / embedded_len, none of which can
// see a pipeline change: on 2026-09-13 text-embeddings-inference went 1.7.2 ->
// 1.9.3, the attention direction for Qwen/Qwen3-Embedding-0.6B went
// bidirectional -> causal (measured cosine between the two spaces: 0.139-0.348,
// aihub#648), and emb_model stayed byte-identical. Converging the corpus needed
// a human to run `UPDATE memories SET embedded_len = NULL` (1671 rows) and the
// same over work_items (2525 rows) BEFORE this tool, forging a "no provenance"
// state to name rows the schema could not otherwise name (aihub#650).
//
// 🔴 THE ONE THING STILL DONE BY HAND. aihub cannot observe pooling or
// attention direction, so those reach the stamp only through
// EMBEDDING_SERVING_ID, which an operator sets. Change the serving side without
// bumping it and this tool is blind again, exactly as it was on 2026-09-13 —
// which is why docs/deployment.md makes bumping it part of the change, not a
// follow-up. The manual embedded_len NULL-out survives only as the recovery
// path for a serving change that already shipped without the bump.
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
)

func main() {
	includeArchived := flag.Bool("include-archived", false,
		"also select archived memories (default: active only; redacted rows are never selected). For full re-embeds where archived over-limit rows ride along, per aihub#625.")
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
	model, dims := prov.ModelID(), prov.Dims()
	pipeline, pipelineDoc := currentPipeline(model, dims)

	pool, err := db.New(ctx, dsn)
	if err != nil {
		fmt.Fprintln(os.Stderr, "db connect:", err)
		os.Exit(1)
	}
	defer pool.Close()

	memSQL, memArgs := memoriesQuery(model, pipelineDoc, *includeArchived)
	rows, err := pool.Query(ctx, memSQL, memArgs...)
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

	population := "active"
	if *includeArchived {
		population = "active+archived"
	}
	fmt.Printf("backfill: %d memories to embed with model=%q dims=%d population=%s pipeline=%q\n",
		len(todo), model, dims, population, pipeline)
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
			`UPDATE memories SET emb_vector = $1::vector, emb_model = $2, emb_dims = $3, embedded_len = $4, emb_pipeline = $5, updated_at = clock_timestamp() WHERE id = $6`,
			vecLiteral(vec), model, dims, len([]rune(embInput)), pipeline, it.id,
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
	wrows, err := pool.Query(ctx, workItemsSelectSQL, model, pipelineDoc)
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
	// pgx defers execute-time errors to Err() (aihub#382, aihub#386).
	if err := wrows.Err(); err != nil {
		fmt.Fprintln(os.Stderr, "read work_items rows:", err)
		os.Exit(1)
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
			`UPDATE work_items SET emb_vector = $1::vector, emb_model = $2, emb_dims = $3, embedded_len = $4, emb_pipeline = $5 WHERE id = $6`,
			vecLiteral(vec), model, dims, len([]rune(embInput)), pipeline, r.id,
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

// memoriesQuery builds the SELECT that picks the memory rows to (re)embed,
// returning the SQL and its positional args.
//
// Status: 'active' always; 'archived' only when includeArchived is set
// (aihub#625 ruling, aihub#637 flag: archived over-limit rows ride along a
// full re-embed while the routine maintenance population stays active-only).
// 'redacted' is never selected under either mode: redaction is the soft
// delete every read path filters out, so a redacted row has no recall path a
// vector could serve.
//
// Embeddable types only; skip methodology.* which is fetched deterministically by
// work_item_id. Re-embed rows with a stale emb_model so a provider switch can be
// backfilled.
//
// The prefix list comes from domain.EmbeddablePrefixes rather than being spelled out
// here: recall (aihub#270) now hands every non-embeddable type to the text path, so a
// prefix this backfill disagreed with would be a row that no path embeds and no path
// text-searches — invisible on both. One list, no drift.
//
// `embedded_len IS NULL` (aihub#504): a row with a vector but no recorded
// embedded_len was embedded before migration 0039 recorded provenance —
// under an unknown historical budget, or (pre-aihub#361) as full text. It is
// indistinguishable from a prefix vector by its emb_model, which is exactly
// the mixed-population defect embed_input.go documents. Re-embedding it here
// converges the corpus: after one run every embedded row carries the budget
// it was embedded under, and the clause never matches again.
//
// `emb_pipeline IS NULL OR split_part(emb_pipeline, '|', 1) IS DISTINCT FROM`
// (aihub#661): the clause that makes a PIPELINE change select itself, so the
// manual `UPDATE ... SET embedded_len = NULL` of 2026-09-14 never has to be run
// again. pipelineDoc is domain.EmbedPipelineDoc — the DOCUMENT segment of the
// current stamp, not the whole stamp, and the split is what keeps the two
// apart. The second segment records the query composition, which provably moves
// no stored vector (Qwen3-Embedding ships prompts.document = ""; see
// domain/embed_input.go and embed_pipeline.go), so comparing the whole stamp
// would re-embed the entire corpus every time the instruct prefix is touched —
// work the evidence says is unnecessary.
//
// The IS NULL arm is written out rather than left to `split_part(NULL, ...) IS
// DISTINCT FROM` (which does select the row): "a row whose pipeline identity is
// unknown is re-embedded" is the rule migration 0041 turns on, and a rule that
// only holds as a side effect of three-valued logic is one refactor from being
// lost.
func memoriesQuery(model, pipelineDoc string, includeArchived bool) (string, []any) {
	statusPred := "status = 'active'"
	if includeArchived {
		statusPred = "status IN ('active', 'archived')"
	}
	embClauses := make([]string, 0, len(domain.EmbeddablePrefixes))
	args := []any{model, pipelineDoc}
	for _, pfx := range domain.EmbeddablePrefixes {
		args = append(args, pfx+"%")
		embClauses = append(embClauses, fmt.Sprintf("type LIKE $%d", len(args)))
	}
	sql := fmt.Sprintf(`
		SELECT id, content FROM memories
		WHERE %s
		  AND (%s)
		  AND (emb_vector IS NULL
		       OR emb_model IS DISTINCT FROM $1
		       OR embedded_len IS NULL
		       OR emb_pipeline IS NULL
		       OR split_part(emb_pipeline, '|', 1) IS DISTINCT FROM $2)`,
		statusPred, strings.Join(embClauses, " OR "))
	return sql, args
}

// workItemsSelectSQL picks the work_item rows to (re)embed. $1 is the current
// model, $2 the current pipeline DOCUMENT segment.
//
// All statuses on purpose — see the call site: the point of wi semantic search
// is finding similar HISTORICAL work, which is mostly wrapped/cancelled rows.
//
// It carries the same convergence clauses memoriesQuery does, and that is
// load-bearing rather than tidy: a pipeline change that selected one table and
// not the other would leave half the index in the old vector space with nothing
// saying so — the aihub#661 defect, reintroduced at half scale and harder to
// notice, because recall would still return plausible-looking rows.
//
// A named const rather than an inline literal so a test can reach it (a
// predicate no test can reach is a predicate that can be weakened without going
// red), and a const rather than a builder function because
// internal/citest/slugres traces `r.id` back to this `SELECT id ... FROM
// work_items` to prove it is a canonical work-item id and not a slug — moving
// the text behind a function call breaks that trace and the gate goes red
// (measured on this change, 2026-09-14). One is a real guard; the other is
// reach for a test. A const satisfies both.
const workItemsSelectSQL = `
		SELECT id, goal, COALESCE(content, '') FROM work_items
		WHERE emb_vector IS NULL
		   OR emb_model IS DISTINCT FROM $1
		   OR embedded_len IS NULL
		   OR emb_pipeline IS NULL
		   OR split_part(emb_pipeline, '|', 1) IS DISTINCT FROM $2`

// currentPipeline returns the two aihub#661 values this run needs, and they are
// NOT the same string: `write` is the full stamp every row this run touches is
// given, `compare` is only its document segment — what both selections match
// the stored stamp against.
//
// One function rather than two call-site expressions, because the difference
// between them is the entire design and picking the wrong one at the call site
// is invisible: passing the full stamp as `compare` still type-checks, still
// runs, and simply re-embeds the whole corpus every time the query-side
// instruct prefix is edited — writing back byte-identical vectors, since
// Qwen3-Embedding applies no prompt to documents (aihub#660). A function is a
// thing a test can hold; an expression inside main() is not.
func currentPipeline(model string, dims int) (write, compare string) {
	return domain.EmbedPipelineID(model, dims), domain.EmbedPipelineDoc(model, dims)
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
