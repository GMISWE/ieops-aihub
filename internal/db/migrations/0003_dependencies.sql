-- +goose Up

CREATE TABLE wi_dependencies (
    blocked_wi_id   TEXT NOT NULL REFERENCES work_items(id) ON DELETE CASCADE,
    blocking_wi_id  TEXT NOT NULL REFERENCES work_items(id) ON DELETE CASCADE,
    kind            TEXT NOT NULL DEFAULT 'blocks'
                    CHECK (kind IN ('blocks', 'supersedes', 'related')),
    created_at      TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
    created_by      TEXT REFERENCES users(id),
    note            TEXT,
    PRIMARY KEY (blocked_wi_id, blocking_wi_id, kind),
    CHECK (blocked_wi_id != blocking_wi_id)
);

-- Index for reverse lookup (find all wi blocked by a given wi)
CREATE INDEX idx_wi_dep_blocking ON wi_dependencies(blocking_wi_id);

-- Cycle detection (WITH RECURSIVE DFS, depth limit 50) enforced at app layer (C8):
-- kind='blocks' and kind='supersedes' both require cycle detection
-- kind='related' is symmetric, does not require cycle detection

-- +goose Down
DROP TABLE IF EXISTS wi_dependencies CASCADE;
