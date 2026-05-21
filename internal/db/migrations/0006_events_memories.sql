-- +goose Up

-- Enable pgvector extension for memories.emb_vector
CREATE EXTENSION IF NOT EXISTS vector;

-- agent_events: immutable audit event stream (partitioned by created_at)
CREATE TABLE agent_events (
    -- C-R3-1: PG requires partitioned table UNIQUE/PK to include partition key created_at
    id             TEXT NOT NULL,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (id, created_at),            -- composite PK including partition key
    -- H-R9-9: work_item_id is NULLABLE, allows global system events (phase_config_updated etc.)
    -- CHECK: standard lifecycle events (attempt_started etc.) must have wi_id; system events allow NULL
    work_item_id   TEXT REFERENCES work_items(id),
    run_attempt_id TEXT REFERENCES run_attempts(id),
    actor_user_id  TEXT REFERENCES users(id),
    actor_display  TEXT,           -- snapshot, written from users.display_name
    api_key_id     TEXT,
    project        TEXT,           -- denormalized from work_items.project (written on INSERT)
                                   -- avoids cross-partition JOIN for project-level queries
    event_type     TEXT NOT NULL,
    -- lifecycle: work_item_filed, attempt_started, attempt_completed,
    --            attempt_superseded, attempt_zombied, force_takeover, admin_force_takeover,
    --            wi_unblocked, stop_step_partial_failure
    -- locks: lock_acquired, lock_released
    -- coding: commit, push, pr_opened, push_blocked_base_moved
    -- step: step_started, step_completed, step_failed, step_heartbeat,
    --       step_agent_unresponsive
    -- memory: memory_saved, memory_activated, memory_archived, memory_reinforced
    -- conflict: conflict_prediction_overridden
    -- admin: admin_unblock, admin_redact
    -- misc: note, decision, wi_zombied, wi_stalled, wi_goal_updated
    payload        JSONB NOT NULL DEFAULT '{}',
                   -- H14: 64KB limit enforced by server middleware
    pinned         BOOLEAN NOT NULL DEFAULT FALSE
) PARTITION BY RANGE (created_at);

-- Initial partition (created by GC job going forward, pre-create first two months)
CREATE TABLE agent_events_2026_05 PARTITION OF agent_events
    FOR VALUES FROM ('2026-05-01') TO ('2026-06-01');
CREATE TABLE agent_events_2026_06 PARTITION OF agent_events
    FOR VALUES FROM ('2026-06-01') TO ('2026-07-01');
-- H13: GC job (daily tick) creates partitions 60 days ahead to prevent
-- month-boundary write failures. See §15 GC task list.

CREATE INDEX idx_evt_wi_time ON agent_events(work_item_id, created_at DESC);
CREATE INDEX idx_evt_project_time ON agent_events(project, created_at DESC)
    WHERE project IS NOT NULL;
CREATE INDEX idx_evt_attempt ON agent_events(run_attempt_id)
    WHERE run_attempt_id IS NOT NULL;
CREATE INDEX idx_evt_pinned ON agent_events(work_item_id) WHERE pinned = TRUE;
CREATE INDEX idx_evt_type_time ON agent_events(event_type, created_at DESC);
CREATE INDEX idx_evt_step ON agent_events(work_item_id, event_type, created_at DESC)
    WHERE event_type LIKE 'step_%';

-- memories: knowledge base with forgetting curve + activation mechanism
-- B1: memory_embeddings table merged into memories (1:1, eliminates JOIN)
CREATE TABLE memories (
    id                  TEXT PRIMARY KEY,             -- mem_<8b62>
    project             TEXT NOT NULL,
    author_user_id      TEXT NOT NULL REFERENCES users(id),
    work_item_id        TEXT REFERENCES work_items(id),
    visibility          TEXT NOT NULL DEFAULT 'project'
                        CHECK (visibility IN ('private', 'project', 'team')),

    -- type classification (see §13):
    -- experience.debug / experience.approach / experience.pitfall / experience.code
    -- fact.architecture / fact.constraint / fact.reference
    -- rule.scheduling / rule.convention / rule.process
    -- methodology.spec / methodology.plan / methodology.review
    -- methodology.execute / methodology.retro / methodology.wrap_summary
    type                TEXT NOT NULL,

    content             TEXT NOT NULL,
    attrs               JSONB NOT NULL DEFAULT '{}',
    -- reserved fields: attrs.related_ids, attrs.reinforcements, attrs.context_snippet

    -- forgetting curve fields
    base_strength       SMALLINT NOT NULL DEFAULT 3
                        CHECK (base_strength BETWEEN 1 AND 5),
    stability_days      REAL NOT NULL DEFAULT 7.0,   -- dynamically updated
    activation_count    INT NOT NULL DEFAULT 0,
    last_activated_at   TIMESTAMPTZ,
    last_activated_by   TEXT REFERENCES users(id),
    is_immortal         BOOL NOT NULL DEFAULT FALSE,
    -- H3: BEFORE INSERT trigger fn_mem_immortal forces this field:
    --   rule.*        → is_immortal=TRUE, stability_days=36500
    --   fact.*        → is_immortal=FALSE, stability_days=180 (C-R3-2)
    --   methodology.* → is_immortal=FALSE, stability_days=36500 (expires_at set at pf_wrap)
    -- User-supplied is_immortal value is overwritten by trigger

    -- status
    status              TEXT NOT NULL DEFAULT 'active'
                        CHECK (status IN ('active', 'archived', 'redacted')),
    archived_at         TIMESTAMPTZ,
    redacted_at         TIMESTAMPTZ,
    redaction_reason    TEXT,

    -- associations
    supersedes_id       TEXT REFERENCES memories(id),
    expires_at          TIMESTAMPTZ,

    -- B1: embedding fields merged in (nullable — memories without embedding e.g. rule.* leave NULL)
    emb_model           TEXT,      -- 'text-embedding-3-small' / 'nomic' / 'bge-small'
    emb_dims            INT,
    emb_vector          VECTOR,    -- H-R3-13: pgvector 0.7+ variable-length VECTOR

    created_at          TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp()
);

CREATE INDEX idx_mem_project_type ON memories(project, type)
    WHERE status = 'active';
CREATE INDEX idx_mem_wi ON memories(work_item_id)
    WHERE work_item_id IS NOT NULL;
CREATE INDEX idx_mem_author ON memories(author_user_id, visibility);
CREATE INDEX idx_mem_expires ON memories(expires_at)
    WHERE expires_at IS NOT NULL AND status = 'active';
CREATE INDEX idx_mem_activation ON memories(last_activated_at)
    WHERE status = 'active' AND is_immortal = FALSE;
-- HNSW vector index (created per-model with WHERE filter):
-- CREATE INDEX idx_mem_emb_openai ON memories
--   USING hnsw(emb_vector vector_cosine_ops) WHERE emb_model='text-embedding-3-small';

-- C-R3-2/C-R3-3: is_immortal + stability_days + expires_at forced by type
-- Decision: fact.* slow decay (180d) not immortal; rule.* immortal; methodology.* bound to wi lifecycle
CREATE OR REPLACE FUNCTION fn_mem_immortal() RETURNS trigger AS $$
BEGIN
    IF NEW.type LIKE 'rule.%' THEN
        -- team rules: never forgotten
        NEW.is_immortal = TRUE;
        NEW.stability_days = 36500;
        NEW.expires_at = NULL;
    ELSIF NEW.type LIKE 'fact.%' THEN
        -- C-R3-2: fact.* slow decay (180d base), is_immortal=FALSE
        NEW.is_immortal = FALSE;
        NEW.stability_days = 180.0;
    ELSIF NEW.type LIKE 'methodology.%' THEN
        -- C-R3-3: methodology.* is_immortal=FALSE (bound to wi lifecycle)
        -- expires_at set to wi.closed_at + 90d by server at pf_wrap time
        NEW.is_immortal = FALSE;
        NEW.stability_days = 36500;  -- no decay before wi closes; expires_at handles GC
    END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER trg_mem_immortal
    BEFORE INSERT ON memories
    FOR EACH ROW EXECUTE FUNCTION fn_mem_immortal();

-- +goose Down
DROP TRIGGER IF EXISTS trg_mem_immortal ON memories;
DROP FUNCTION IF EXISTS fn_mem_immortal();
DROP TABLE IF EXISTS memories CASCADE;
DROP TABLE IF EXISTS agent_events_2026_06;
DROP TABLE IF EXISTS agent_events_2026_05;
DROP TABLE IF EXISTS agent_events CASCADE;
DROP EXTENSION IF EXISTS vector;
