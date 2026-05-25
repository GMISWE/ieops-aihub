-- +goose Up
ALTER TABLE wi_step_state ADD COLUMN IF NOT EXISTS scenario_ref TEXT;

-- +goose Down
ALTER TABLE wi_step_state DROP COLUMN IF EXISTS scenario_ref;
