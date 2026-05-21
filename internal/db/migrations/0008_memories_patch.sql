-- +goose Up
-- Add missing columns to memories table (C1 fix)
-- Columns referenced by domain/memory.go but absent from 0006_events_memories.sql:
--   author_display, tags, source_artifact_id, updated_at
ALTER TABLE memories
    ADD COLUMN IF NOT EXISTS author_display     TEXT,
    ADD COLUMN IF NOT EXISTS tags               TEXT[]      NOT NULL DEFAULT '{}',
    ADD COLUMN IF NOT EXISTS source_artifact_id TEXT,
    ADD COLUMN IF NOT EXISTS updated_at         TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp();

-- Index for tag searches
CREATE INDEX IF NOT EXISTS idx_mem_tags ON memories USING GIN(tags);

-- updated_at trigger for memories
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION fn_mem_updated_at() RETURNS trigger AS $$
BEGIN NEW.updated_at = clock_timestamp(); RETURN NEW; END;
$$ LANGUAGE plpgsql;
-- +goose StatementEnd
CREATE TRIGGER trg_mem_updated_at BEFORE UPDATE ON memories
FOR EACH ROW EXECUTE FUNCTION fn_mem_updated_at();

-- +goose Down
DROP TRIGGER IF EXISTS trg_mem_updated_at ON memories;
DROP FUNCTION IF EXISTS fn_mem_updated_at();
DROP INDEX IF EXISTS idx_mem_tags;
ALTER TABLE memories
    DROP COLUMN IF EXISTS author_display,
    DROP COLUMN IF EXISTS tags,
    DROP COLUMN IF EXISTS source_artifact_id,
    DROP COLUMN IF EXISTS updated_at;
