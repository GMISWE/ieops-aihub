-- +goose Up
-- Add 'admin' to the memories visibility check constraint.
-- Admin-tier memories are only surfaced to users with global role='admin' (filtered in Recall).
ALTER TABLE memories DROP CONSTRAINT IF EXISTS memories_visibility_check;
ALTER TABLE memories ADD CONSTRAINT memories_visibility_check
    CHECK (visibility IN ('private', 'project', 'team', 'admin'));

-- +goose Down
ALTER TABLE memories DROP CONSTRAINT IF EXISTS memories_visibility_check;
ALTER TABLE memories ADD CONSTRAINT memories_visibility_check
    CHECK (visibility IN ('private', 'project', 'team'));
