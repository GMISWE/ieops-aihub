-- +migrate Up
ALTER TABLE work_items
  ADD COLUMN content TEXT DEFAULT NULL
  CHECK (content IS NULL OR (
    length(content) <= 20000 AND
    position(E'\x00' in content) = 0
  ));

-- +migrate Down
ALTER TABLE work_items DROP COLUMN IF EXISTS content;
