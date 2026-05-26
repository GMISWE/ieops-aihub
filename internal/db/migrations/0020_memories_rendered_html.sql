-- +goose Up
-- Add rendered_html column for spec/plan artifact HTML rendering (aihub#27 / IEBE-1694).
-- Populated synchronously on insert for methodology.spec / methodology.plan only;
-- other memory types leave it NULL. Existing rows stay NULL (no backfill — per spec
-- decision 5).
ALTER TABLE memories ADD COLUMN IF NOT EXISTS rendered_html TEXT;

-- +goose Down
ALTER TABLE memories DROP COLUMN IF EXISTS rendered_html;
