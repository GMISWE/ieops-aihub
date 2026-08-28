-- +goose Up
-- aihub#273: semantic recall for work items. Mirrors memories' embedding
-- columns (0006 B1): all three nullable — a row without an embedding leaves
-- them NULL and is served by the ILIKE text fallback instead.
-- No vector index on purpose: ~2k rows sequential-scan in well under a
-- millisecond (memories at ~3k rows runs the same way, see 0006's commented
-- HNSW block for the day either table outgrows that).
ALTER TABLE work_items ADD COLUMN IF NOT EXISTS emb_model TEXT;
ALTER TABLE work_items ADD COLUMN IF NOT EXISTS emb_dims INT;
ALTER TABLE work_items ADD COLUMN IF NOT EXISTS emb_vector VECTOR;

-- +goose Down
ALTER TABLE work_items DROP COLUMN IF EXISTS emb_vector;
ALTER TABLE work_items DROP COLUMN IF EXISTS emb_dims;
ALTER TABLE work_items DROP COLUMN IF EXISTS emb_model;
