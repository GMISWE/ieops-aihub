-- +goose Up
DROP TABLE IF EXISTS scenario_phase_configs CASCADE;

-- +goose Down
CREATE TABLE IF NOT EXISTS scenario_phase_configs (
    scenario    TEXT PRIMARY KEY
                CHECK (scenario IN ('coding', 'writing', 'data')),
    content     JSONB NOT NULL,
    version     INT  NOT NULL DEFAULT 1,
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
    updated_by  TEXT REFERENCES users(id)
);
