-- +goose Up
ALTER TABLE work_items
  ADD COLUMN content TEXT DEFAULT NULL
  CHECK (content IS NULL OR length(content) <= 20000);

-- +goose Down
ALTER TABLE work_items DROP COLUMN IF EXISTS content;
