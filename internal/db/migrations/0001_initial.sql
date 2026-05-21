-- +goose Up

-- users: user identity + API keys
CREATE TABLE users (
    id              TEXT PRIMARY KEY,             -- u_<8b62>
    email           TEXT NOT NULL UNIQUE,
    display_name    TEXT NOT NULL,
    user_type       TEXT NOT NULL DEFAULT 'human'
                    CHECK (user_type IN ('human', 'machine')),
    role            TEXT NOT NULL DEFAULT 'writer'
                    CHECK (role IN ('writer', 'admin')),
    -- project-level roles: {"marketplace": "writer", "aihub": "maintainer", ...}
    -- valid values: viewer | writer | maintainer (validated at app layer on PATCH)
    project_roles   JSONB NOT NULL DEFAULT '{}',
    -- API keys array: [{id, key_hash, name, project_scope?, expires_at?, created_at, revoked_at}]
    api_keys        JSONB NOT NULL DEFAULT '[]',
    -- git commit author matching for pf_sync PR author → user_id
    author_aliases  TEXT[] NOT NULL DEFAULT '{}',
    created_at      TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp()
);

CREATE INDEX idx_users_email ON users(email);
CREATE INDEX idx_users_project_roles ON users USING GIN(project_roles);

-- scenario_phase_configs: scenario-level wi_type definitions + classification_rules SoT (v1.23)
-- All projects using the same scenario share one record.
-- Read: viewer+; Write: maintainer/admin + CAS version
CREATE TABLE scenario_phase_configs (
    scenario    TEXT PRIMARY KEY
                CHECK (scenario IN ('coding', 'writing', 'data')),
    -- content: parsed phase.yaml JSON: {wi_types:{...}, classification_rules:[...]}
    content     JSONB NOT NULL,
    version     INT  NOT NULL DEFAULT 1,  -- optimistic lock, incremented on each PUT
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
    updated_by  TEXT REFERENCES users(id)
);
-- C-R9-1: initial data inserted by 0007_seed_scenario_configs.sql
-- C-R9-3: pf_create_work_item returns 503 if scenario has no record here

-- +goose Down
DROP TABLE IF EXISTS scenario_phase_configs CASCADE;
DROP TABLE IF EXISTS users CASCADE;
