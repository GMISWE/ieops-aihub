-- +goose Up
ALTER TABLE users DROP COLUMN IF EXISTS project_roles;
DROP INDEX IF EXISTS idx_users_project_roles;

-- +goose Down
ALTER TABLE users ADD COLUMN project_roles JSONB NOT NULL DEFAULT '{}';
CREATE INDEX idx_users_project_roles ON users USING GIN(project_roles);
