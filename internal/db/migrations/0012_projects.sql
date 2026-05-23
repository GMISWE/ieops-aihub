-- +goose Up
CREATE TABLE projects (
    name              TEXT PRIMARY KEY CHECK (name ~ '^[a-z][a-z0-9_-]{0,39}$'),
    description       TEXT,
    visible           BOOL NOT NULL DEFAULT true,
    identifier_hash   TEXT,
    identifier_prefix TEXT,
    repos             JSONB NOT NULL DEFAULT '[]',
    members           JSONB NOT NULL DEFAULT '[]',
    wi_seq            BIGINT NOT NULL DEFAULT 0,
    scenario          TEXT NOT NULL DEFAULT 'coding' CHECK (scenario IN ('coding','writing','data')),
    owner_user_id     TEXT NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
    created_at        TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
    updated_at        TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp()
);

CREATE INDEX idx_projects_owner ON projects(owner_user_id);
CREATE INDEX idx_projects_members ON projects USING GIN(members jsonb_path_ops);
CREATE INDEX idx_projects_visible ON projects(visible) WHERE visible = true;

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION fn_projects_updated_at() RETURNS trigger AS $$
BEGIN NEW.updated_at = clock_timestamp(); RETURN NEW; END;
$$ LANGUAGE plpgsql;
-- +goose StatementEnd

CREATE TRIGGER trg_projects_updated_at BEFORE UPDATE ON projects
FOR EACH ROW EXECUTE FUNCTION fn_projects_updated_at();

-- +goose Down
DROP TABLE IF EXISTS projects CASCADE;
