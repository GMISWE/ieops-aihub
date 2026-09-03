-- +goose Up

-- DEPLOY ORDER: apply this migration BEFORE starting the binary that reads
-- wi_watches. It is additive-only — a new table, no change to any existing one
-- — so the two orderings fail very differently and neither loses data:
--
--   migration first (the documented order): correct.
--   binary first: /ui/wi/<id> still renders. The detail handler's watch lookup
--     is deliberately best-effort (see fetchWatching in ui_handlers_wi.go), so
--     a missing table reads as "not watching" instead of 500-ing a page whose
--     primary content has nothing to do with watching. What DOES fail loudly
--     in that window is POST/DELETE /ui/wi/<id>/watch and GET /ui/wi?watching=1
--     — the two requests that are ABOUT the table (SQLSTATE 42P01).
--
-- ROLLBACK: the production rollback anchor is a CONTAINER swap (docs/deployment.md),
-- which does not touch the schema. Rolling the binary back therefore leaves this
-- table and every row in it intact; the old binary simply never selects from it,
-- and re-rolling forward finds the watches still there. The rollback is NOT lossy.
-- Only `goose down` / `make migrate-down` drops the table, and that destroys every
-- watch row irrecoverably — it is not part of the rollback procedure and must not
-- be run as one.
--
-- aihub#143: the user↔work-item "watching" relation behind the /ui Watching
-- scope. aihub#129 designed All / Mine / Watching; Mine and All shipped and
-- Watching was cut to an inert disabled button because there was no persisted
-- relation for it to filter on. This is that relation.
--
-- Shape notes:
--   * Composite PRIMARY KEY (user_id, work_item_id) rather than a surrogate id:
--     watching is set membership, so "watched twice" is not a state that should
--     be representable. It also gives the (user_id, ...) prefix index the
--     `watching` list scope reads on every request for free.
--   * ON DELETE CASCADE on both sides. A watch is meaningless once either end is
--     gone, and without the cascade deleting a user or a work item would fail on
--     the FK — turning "I watched it" into a reason a wi cannot be deleted.
--   * No updated_at / no trigger: rows are insert-or-delete only, never mutated,
--     so there is nothing for one to record.
CREATE TABLE IF NOT EXISTS wi_watches (
    user_id      TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    work_item_id TEXT NOT NULL REFERENCES work_items(id) ON DELETE CASCADE,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (user_id, work_item_id)
);

-- Reverse lookup ("who watches this wi"), which the PK's (user_id, work_item_id)
-- prefix order cannot serve. Nothing reads it yet; it exists because the FK
-- cascade from work_items scans this direction on every wi delete.
CREATE INDEX IF NOT EXISTS idx_wi_watches_work_item ON wi_watches(work_item_id);

-- +goose Down
DROP TABLE IF EXISTS wi_watches;
