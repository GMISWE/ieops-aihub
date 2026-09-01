-- +goose Up
-- aihub#206 (C1, spec A-1): the client can send a pause_reason on
-- complete_attempt(status=paused), but the server silently dropped it.
-- Persist it on the run_attempts row so the ready-queue "paused" segment
-- can surface why an attempt was paused.
ALTER TABLE run_attempts ADD COLUMN IF NOT EXISTS pause_reason TEXT;

-- +goose Down
ALTER TABLE run_attempts DROP COLUMN IF EXISTS pause_reason;
