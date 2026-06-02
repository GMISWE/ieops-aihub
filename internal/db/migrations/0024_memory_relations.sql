-- +goose Up
-- aihub#74 Stream A: memory-to-memory "related" links table.
-- Uses a separate table to avoid touching the 6 lockstep scan sites in
-- memories (Memory struct / Remember INSERT+Scan / Recall SELECT+Scan /
-- scanMemoryLite / GetMemoryByID SELECT+Scan).
-- NOTE: memories are soft-deleted (UPDATE status='redacted'/'archived'), never hard
-- DELETEd, so ON DELETE CASCADE is latent and rarely fires. The read-enrichment
-- queries (loadForwardRelations/loadBacklinks) filter `m.status != 'redacted'` so
-- redacted memories never leak through the relation graph; the CASCADE is kept only
-- as a safety net if a hard delete is ever introduced.
CREATE TABLE memory_relations (
    from_mem   TEXT NOT NULL REFERENCES memories(id) ON DELETE CASCADE,
    to_mem     TEXT NOT NULL REFERENCES memories(id) ON DELETE CASCADE,
    project    TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (from_mem, to_mem)
);
CREATE INDEX idx_memrel_to ON memory_relations(to_mem);   -- backlink lookups

-- Backfill existing attrs.related_ids JSONB arrays into the table.
-- Filter to target ids that actually exist (FK would otherwise fail the migration).
INSERT INTO memory_relations (from_mem, to_mem, project, created_at)
SELECT m.id, rid, m.project, clock_timestamp()
FROM memories m
CROSS JOIN LATERAL jsonb_array_elements_text(m.attrs->'related_ids') AS rid
WHERE m.attrs ? 'related_ids'
  AND jsonb_typeof(m.attrs->'related_ids') = 'array'
  AND EXISTS (SELECT 1 FROM memories t WHERE t.id = rid)
ON CONFLICT DO NOTHING;

-- +goose Down
DROP TABLE IF EXISTS memory_relations CASCADE;
