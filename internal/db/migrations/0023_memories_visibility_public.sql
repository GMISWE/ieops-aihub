-- +goose Up
-- Add 'public' visibility tier for unauthenticated artifact sharing (aihub#96).
-- A public artifact is reachable without auth via GET /share/:id, using its
-- memory_id as the unguessable link. Only methodology.spec / methodology.plan
-- (rendered_html non-null) are shareable.
ALTER TABLE memories DROP CONSTRAINT IF EXISTS memories_visibility_check;
ALTER TABLE memories ADD CONSTRAINT memories_visibility_check
    CHECK (visibility IN ('private', 'project', 'team', 'admin', 'public'));

-- +goose Down
-- Downgrade public rows first, otherwise restoring the old constraint would reject them.
UPDATE memories SET visibility='project' WHERE visibility='public';
ALTER TABLE memories DROP CONSTRAINT IF EXISTS memories_visibility_check;
ALTER TABLE memories ADD CONSTRAINT memories_visibility_check
    CHECK (visibility IN ('private', 'project', 'team', 'admin'));
