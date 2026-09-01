-- +goose Up
-- opt③ L1 recall precision: full-text index so recall can rank by lexical relevance
-- to the query (was query-blind recency). Generated STORED column backfills existing
-- rows automatically. to_tsvector(regconfig, text) is IMMUTABLE (explicit 'english'),
-- which a GENERATED column requires.
ALTER TABLE memories ADD COLUMN IF NOT EXISTS content_tsv tsvector
  GENERATED ALWAYS AS (to_tsvector('english', content)) STORED;
CREATE INDEX IF NOT EXISTS idx_mem_content_tsv ON memories USING GIN(content_tsv);

-- +goose Down
DROP INDEX IF EXISTS idx_mem_content_tsv;
ALTER TABLE memories DROP COLUMN IF EXISTS content_tsv;
