-- +goose Up

-- aihub#260: compare-and-set counter for projects.members.
--
-- `members` is a whole-list REPLACE, so adding one person means reading all N
-- and writing back N+1. Two admins doing that concurrently used to lose one of
-- the additions silently — and because trg_projects_updated_at rewrites
-- updated_at on every write, the result was indistinguishable afterwards from
-- "that person was never added". Observed on 2026-08-24.
--
-- Mirrors work_items.resources_version (0002_work_items.sql): INT NOT NULL
-- DEFAULT 0, incremented by Postgres itself (`members_version = members_version
-- + 1`) on every write of members, and used as a WHERE precondition when the
-- caller supplies one. DEFAULT 0 backfills every existing row, so no separate
-- backfill statement is needed and pre-existing projects start at the same
-- value a freshly created one does.
--
-- Deliberately NOT a compare-and-set on updated_at: that column moves for every
-- unrelated edit (the trigger), and a timestamp round-tripped through JSON
-- compares representations rather than values.
ALTER TABLE projects ADD COLUMN members_version INT NOT NULL DEFAULT 0;

-- +goose Down
ALTER TABLE projects DROP COLUMN IF EXISTS members_version;
