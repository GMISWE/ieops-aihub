-- +goose Up
-- Sync wi_seq to current max seq in work_items per project.
-- Required after 0013 backfill which initialized wi_seq=0 for all projects
-- while existing work_items already had seq > 0 (from SEQUENCE objects).
UPDATE projects p
SET wi_seq = sub.max_seq
FROM (
    SELECT project, MAX(seq) AS max_seq
    FROM work_items
    GROUP BY project
) sub
WHERE p.name = sub.project
  AND p.wi_seq < sub.max_seq;

-- +goose Down
-- Cannot safely restore previous wi_seq values; this migration is safe to leave applied.
