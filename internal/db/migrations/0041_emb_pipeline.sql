-- +goose Up
-- aihub#661: record WHICH PIPELINE produced a vector, not just which model.
--
-- emb_model answers "which checkpoint"; it has never answered "under what
-- serving semantics". On 2026-09-13 the difference was measured and priced:
-- text-embeddings-inference 1.7.2 forwarded Qwen/Qwen3-Embedding-0.6B with
-- BIDIRECTIONAL attention, 1.9.3 forwards the same checkpoint causally (the
-- only regime in which its last-token pooling is meaningful), and the cosine
-- between the two spaces on identical text is 0.139-0.348 (aihub#648, 17/17
-- documents). Across that swap emb_model and emb_dims were byte-identical, so
-- no SQL could separate the two populations.
--
-- The bill arrived at cmd/aihub-embed-backfill, whose selection predicate picks
-- rows whose provenance differs from the current one: none of its clauses could
-- see a pipeline change, so converging the corpus on 2026-09-14 required a
-- human to run `UPDATE memories SET embedded_len = NULL` (1671 rows) and the
-- same over work_items (2525 rows) BY HAND first — forging a "no provenance"
-- state to name rows the schema had no other way to name. This column ends
-- that: the backfill now compares the stamp directly, so a pipeline change puts
-- the old rows into the re-embed set automatically.
--
-- SHAPE (composed by domain.EmbedPipelineID, which documents every field):
--
--   doc:m=<model>;d=<dims>;in=<input budget runes>;s=<serving id>|qry:p=<prefix fingerprint>
--
-- The first segment is the identity of the DOCUMENT pipeline and is what the
-- backfill compares. The second records the query composition that was live at
-- write time and deliberately does not trigger re-embedding: Qwen3-Embedding
-- ships prompts.document = "", so the instruct prefix (aihub#669) is applied to
-- queries only and moves no stored point.
--
-- 🔴 s= IS OPERATOR-DECLARED (EMBEDDING_SERVING_ID). Pooling, attention
-- direction, backend and backend version exist nowhere else in this stamp,
-- because aihub cannot observe them. A row reading s=undeclared is a row whose
-- serving semantics are NOT covered — that is written down rather than implied
-- so it can be selected for:
--
--   SELECT count(*) FROM memories WHERE emb_pipeline LIKE '%;s=undeclared|%';
--
-- NULL IS NOT BACKFILLED HERE, for the reason 0039 gives for embedded_len: the
-- pipeline that produced a historical row cannot be reconstructed from the row,
-- and every value this migration could write would be a guess. NULL means
-- "identity unknown", the backfill's predicate selects exactly those rows, and
-- after one run the clause never matches them again.
ALTER TABLE memories ADD COLUMN IF NOT EXISTS emb_pipeline TEXT;
COMMENT ON COLUMN memories.emb_pipeline IS
    'identity of the embedding pipeline that produced emb_vector (aihub#661); NULL = no vector or unrecorded (pre-0041) identity; s=undeclared = serving semantics not covered';

ALTER TABLE work_items ADD COLUMN IF NOT EXISTS emb_pipeline TEXT;
COMMENT ON COLUMN work_items.emb_pipeline IS
    'identity of the embedding pipeline that produced emb_vector (aihub#661); NULL = no vector or unrecorded (pre-0041) identity; s=undeclared = serving semantics not covered';

-- +goose Down
ALTER TABLE memories DROP COLUMN IF EXISTS emb_pipeline;
ALTER TABLE work_items DROP COLUMN IF EXISTS emb_pipeline;
