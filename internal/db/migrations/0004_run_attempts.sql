-- +goose Up

CREATE TABLE run_attempts (
    id                   TEXT PRIMARY KEY,             -- ra_<8b62>
    work_item_id         TEXT NOT NULL REFERENCES work_items(id),
    status               TEXT NOT NULL DEFAULT 'running'
                         CHECK (status IN ('running', 'paused', 'wrapped',
                                           'failed', 'superseded', 'lost')),
    -- claim_epoch initial value rule (H9):
    -- fn_claim_work_item() inside:
    --   new_attempt.claim_epoch = wi.current_attempt_epoch + 1
    --   wi.current_attempt_epoch = new_attempt.claim_epoch  (same transaction)
    -- DDL DEFAULT 1 is only a safety net; should not be used directly
    claim_epoch          BIGINT NOT NULL DEFAULT 1,
    idempotency_key      TEXT NOT NULL,
    -- v1.21 ownership-only final design:
    -- After claim, ownership is permanent; no expires_at.
    -- Release paths: (a) self complete (wrap/fail/pause)
    --                (b) same user_id force_takeover from another machine
    --                (c) maintainer/admin force_takeover
    -- last_active_at is only for monitoring "how long since wi was active",
    -- NOT used as a permission gate
    last_active_at       TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
    actor_user_id        TEXT NOT NULL REFERENCES users(id),
    api_key_id           TEXT,
    actor_display        TEXT NOT NULL,
    machine_id           TEXT NOT NULL,
    session_secret_hash  TEXT NOT NULL,
    parent_attempt_id    TEXT REFERENCES run_attempts(id),
    -- v1.23: phase_yaml_snapshot removed; replaced by phase_config_version audit field
    -- fn_claim_work_item reads scenario_phase_configs[wi.scenario] directly; client no longer uploads
    phase_config_version INT,    -- snapshot of scenario_phase_configs.version at claim time (audit)
    prepared_workspace   JSONB,  -- {repo: abs_path}, local reference only, not validated
    started_at           TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
    ended_at             TIMESTAMPTZ,
    UNIQUE (work_item_id, idempotency_key),
    UNIQUE (work_item_id, claim_epoch)
);

CREATE INDEX idx_ra_wi_status ON run_attempts(work_item_id, status);
CREATE INDEX idx_ra_actor ON run_attempts(actor_user_id, status);
CREATE INDEX idx_ra_last_active ON run_attempts(last_active_at) WHERE status = 'running';
-- v1.21: idx_ra_expires removed (expires_at column removed)

-- resource_locks: typed locks owned by a run_attempt
CREATE TABLE resource_locks (
    resource_type    TEXT NOT NULL
                     CHECK (resource_type IN ('git_branch', 'worktree',
                                              'file_scope', 'tcp_port', 'deploy_env')),
    resource_key     TEXT NOT NULL,
    -- C-R7-7: run_attempts is append-only (never physically DELETEd); CASCADE is dead code.
    -- Changed to ON DELETE RESTRICT per M-R3-12 "no physical delete" principle.
    owner_attempt_id TEXT NOT NULL REFERENCES run_attempts(id) ON DELETE RESTRICT,
    claim_epoch      BIGINT NOT NULL,   -- H-R3-6: type matches run_attempts.claim_epoch
    acquired_at      TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (resource_type, resource_key)
);

CREATE INDEX idx_locks_owner ON resource_locks(owner_attempt_id);

-- +goose Down
DROP TABLE IF EXISTS resource_locks CASCADE;
DROP TABLE IF EXISTS run_attempts CASCADE;
