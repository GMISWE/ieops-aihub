-- +goose Up

CREATE TABLE wi_step_state (
    work_item_id           TEXT PRIMARY KEY REFERENCES work_items(id) ON DELETE CASCADE,
    wi_type                TEXT NOT NULL,
    -- graph_source (v1.23 update):
    --   'scenario_config'  = read from scenario_phase_configs[wi.scenario] (standard path)
    --   'scenario_default' = scenario built-in default graph (fallback when scenario_phase_configs empty)
    graph_source           TEXT NOT NULL DEFAULT 'scenario_config'
                           CHECK (graph_source IN ('scenario_config', 'scenario_default')),
    current_step           TEXT,  -- NULL = before start_step or all steps done
    -- C2: track current step execution state (Wi Agent timeout detection depends on this field)
    current_step_status    TEXT DEFAULT 'idle'
                           CHECK (current_step_status IN ('idle', 'in_progress')),
    current_step_attempt   TEXT,              -- current step_attempt_id, has value when in_progress
    step_started_at        TIMESTAMPTZ,       -- when current step started (for timeout detection)
    version                BIGINT NOT NULL DEFAULT 0,
    updated_at             TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp()
);

-- CAS update (advance step):
-- UPDATE wi_step_state
-- SET current_step=$next, current_step_status='idle',
--     current_step_attempt=NULL, step_started_at=NULL,
--     version=version+1, updated_at=clock_timestamp()
-- WHERE work_item_id=$id AND version=$expected
-- RETURNING version  -- 0 rows means concurrent conflict

-- CAS update (mark in_progress, server generates step_attempt_id):
-- BEGIN;
-- sa_id := NewID('sa');  -- server generated
-- UPDATE wi_step_state
-- SET current_step_status='in_progress',
--     current_step_attempt=sa_id,
--     step_started_at=clock_timestamp(),
--     version=version+1
-- WHERE work_item_id=$id AND version=$expected
--   AND current_step_status='idle'  -- prevent duplicate in_progress
-- RETURNING version;
-- -- 0 rows → ROLLBACK (concurrent race or wrong version)
-- COMMIT;

-- C6-4: completion must be atomic (INSERT completion + UPDATE state in same transaction)
-- with both conditions (CAS version + step_attempt_id) merged into one WHERE, eliminating TOCTOU:
-- BEGIN;
-- INSERT INTO wi_step_completions(id, work_item_id, step_id, step_attempt_id,
--                                  status, artifact_summary, completed_at, run_attempt_id)
-- VALUES(NewID('sc'), $wi_id, $step_id, $step_attempt_id,
--        'completed', $artifact, clock_timestamp(), $attempt_id);
-- -- UNIQUE(step_attempt_id) blocks concurrent double-write
--
-- UPDATE wi_step_state
-- SET current_step=$next_step,
--     current_step_status='idle',
--     current_step_attempt=NULL,
--     step_started_at=NULL,
--     version=version+1, updated_at=clock_timestamp()
-- WHERE work_item_id=$id
--   AND version=$expected                          ← CAS
--   AND current_step_attempt=$step_attempt_id;     ← prevents TOCTOU (C6-4 fix)
-- -- 0 rows → ROLLBACK
-- COMMIT;

-- wi_step_completions: append-only, independent of wi_step_state, records full step history (including retries)
CREATE TABLE wi_step_completions (
    id               TEXT PRIMARY KEY,   -- sc_<8b62>
    work_item_id     TEXT NOT NULL REFERENCES work_items(id),
    step_id          TEXT NOT NULL,
    step_attempt_id  TEXT NOT NULL,      -- server generated, prevents concurrent double-write
    status           TEXT NOT NULL CHECK (status IN ('completed', 'failed')),
    artifact_summary TEXT,               -- max 4096 Unicode characters (L10)
                                         -- length() in PG counts characters (not bytes)
                                         -- oversized artifacts stored in memory, only summary ref here
    error_type       TEXT,
    escalated        BOOL DEFAULT FALSE,
    completed_at     TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
    run_attempt_id   TEXT REFERENCES run_attempts(id),  -- H11: renamed from attempt_id
    CHECK (length(artifact_summary) <= 4096)
);

CREATE INDEX idx_wsc_wi ON wi_step_completions(work_item_id, step_id, completed_at DESC);
CREATE UNIQUE INDEX idx_wsc_attempt ON wi_step_completions(step_attempt_id);
-- step_attempt_id UNIQUE prevents same step_attempt double-write

-- +goose Down
DROP TABLE IF EXISTS wi_step_completions CASCADE;
DROP TABLE IF EXISTS wi_step_state CASCADE;
