-- +goose Up

-- B2: wi_sequences table removed; seq generated via project-scoped sequences.
-- pf_create_work_item handler: nextval('wi_seq_' || project)
-- On first project use: CREATE SEQUENCE IF NOT EXISTS wi_seq_<project> AS BIGINT START 1
CREATE SEQUENCE IF NOT EXISTS wi_seq_aihub AS BIGINT START 1;
CREATE SEQUENCE IF NOT EXISTS wi_seq_marketplace AS BIGINT START 1;
CREATE SEQUENCE IF NOT EXISTS wi_seq_ieops AS BIGINT START 1;

CREATE TABLE work_items (
    id                      TEXT PRIMARY KEY,             -- wi_<8b62>
    seq                     BIGINT NOT NULL,
    slug                    TEXT GENERATED ALWAYS AS (project || '#' || seq) STORED,
    project                 TEXT NOT NULL,
    -- M2: v1 only implements coding scenario; 'writing'/'data' reserved
    -- Server returns 405 NOT_IMPLEMENTED for unimplemented scenarios
    scenario                TEXT NOT NULL DEFAULT 'coding'
                            CHECK (scenario IN ('coding', 'writing', 'data')),
    goal                    TEXT NOT NULL
                            CHECK (length(goal) <= 500 AND goal !~ E'[\n\r]'),
    -- goal updates allowed with restrictions (see §4.3 PATCH endpoint):
    --   1. Only reporter or project maintainer may update
    --   2. status must be queued or paused (running → 409)
    --   3. goal_change_reason required (min 10 chars)
    --   4. server emits wi_goal_updated event (old_goal preserved)
    source                  TEXT NOT NULL DEFAULT 'human'
                            CHECK (source IN ('human', 'auto_execute', 'auto_debug',
                                              'auto_review', 'sync_jira', 'sync_github', 'admin')),
    -- v1.22: kind field removed; wi_type is the single field determining execution path
    -- wi_type maps directly to phase.yaml step graph; no fixed enum
    -- Examples: fix_bug / critical_bug / feature / chore (team-customizable via phase.yaml)
    -- Set by wi_classification_rules (server config) or AI at creation time
    wi_type                 TEXT,
    priority                TEXT NOT NULL DEFAULT 'normal'
                            CHECK (priority IN ('low', 'normal', 'high', 'urgent')),
    -- requires_human_session: AI determines if wi needs human in session
    --   false = Orchestrator can dispatch subagent automatically (Session 1)
    --   true  = Human must participate in separate session (spec/design) (Session 2/3)
    --   NULL  = Unclassified (create-time phase.yaml not provided or AI didn't set)
    -- C-R8-1/C-R8-2: DEFAULT NULL (fail-safe), NOT false!
    --   NULL wi goes to unclassified[] queue, NOT items[] (prevents auto-execution)
    requires_human_session  BOOL DEFAULT NULL,
    milestone               TEXT,
    labels                  TEXT[] NOT NULL DEFAULT '{}'
                            CHECK (cardinality(labels) <= 20),
    status                  TEXT NOT NULL DEFAULT 'queued'
                            CHECK (status IN ('queued', 'running', 'paused', 'blocked',
                                              'wrapped', 'failed', 'cancelled')),
    declared_resources      JSONB NOT NULL DEFAULT '[]',
    resources_version       INT NOT NULL DEFAULT 0,
    external_share_type     TEXT CHECK (external_share_type IN ('jira', 'github', 'linear')
                                        OR external_share_type IS NULL),
    external_share_key      TEXT,
    reporter_user_id        TEXT NOT NULL REFERENCES users(id),
    reporter_display        TEXT NOT NULL,        -- snapshot, written at creation
    current_attempt_id      TEXT,                 -- denormalized for fast reads
    current_attempt_epoch   BIGINT DEFAULT 0,     -- monotonically increasing, prevents replay attacks
    parent_work_item_id     TEXT REFERENCES work_items(id),
    attrs                   JSONB NOT NULL DEFAULT '{}',
                            -- strict namespacing: {"github":{...},"jira":{...},"internal":{...}}
    created_at              TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
    updated_at              TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
    closed_at               TIMESTAMPTZ,
    UNIQUE (project, seq)
    -- M5-14: not using NULLS NOT DISTINCT (two (NULL,NULL) wi would collide)
    -- using partial unique index instead (WHERE external_share_type IS NOT NULL)
);

CREATE UNIQUE INDEX idx_wi_slug ON work_items(slug);
-- M5-14 fix: partial unique index, NULL not included
CREATE UNIQUE INDEX idx_wi_external_ref ON work_items(external_share_type, external_share_key)
    WHERE external_share_type IS NOT NULL AND external_share_key IS NOT NULL;
CREATE INDEX idx_wi_project_status ON work_items(project, status);
CREATE INDEX idx_wi_milestone ON work_items(project, milestone) WHERE milestone IS NOT NULL;
CREATE INDEX idx_wi_wi_type ON work_items(project, wi_type) WHERE wi_type IS NOT NULL;
CREATE INDEX idx_wi_labels ON work_items USING GIN(labels);
CREATE INDEX idx_wi_declared ON work_items USING GIN(declared_resources);
CREATE INDEX idx_wi_parent ON work_items(parent_work_item_id) WHERE parent_work_item_id IS NOT NULL;
CREATE INDEX idx_wi_closed ON work_items(closed_at) WHERE closed_at IS NOT NULL;

-- Trigger: auto-update updated_at
CREATE OR REPLACE FUNCTION fn_wi_updated_at() RETURNS trigger AS $$
BEGIN
    NEW.updated_at = clock_timestamp();
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER trg_wi_updated_at
    BEFORE UPDATE ON work_items
    FOR EACH ROW EXECUTE FUNCTION fn_wi_updated_at();

-- goal updates are controlled at the API layer (permission check + audit event),
-- not blocked at the DDL layer.
-- server handler on PATCH goal checks:
--   a. caller is reporter or maintainer
--   b. wi.status IN ('queued', 'paused') — running returns 409 GOAL_CHANGE_NOT_ALLOWED
--   c. goal_change_reason required
--   d. emits wi_goal_updated event {old_goal, new_goal, reason, changed_by}

-- Trigger: auto-set closed_at on terminal status transitions
CREATE OR REPLACE FUNCTION fn_wi_closed_at() RETURNS trigger AS $$
BEGIN
    IF NEW.status IN ('wrapped', 'failed', 'cancelled')
       AND OLD.status NOT IN ('wrapped', 'failed', 'cancelled') THEN
        NEW.closed_at = clock_timestamp();
    END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER trg_wi_closed_at
    BEFORE UPDATE OF status ON work_items
    FOR EACH ROW EXECUTE FUNCTION fn_wi_closed_at();

-- current_attempt_id + epoch atomicity protection:
-- RPC functions (fn_claim_work_item, fn_complete_attempt, fn_force_takeover)
-- update these fields within transactions; HTTP handlers must not write directly.

-- +goose Down
DROP TRIGGER IF EXISTS trg_wi_closed_at ON work_items;
DROP TRIGGER IF EXISTS trg_wi_updated_at ON work_items;
DROP FUNCTION IF EXISTS fn_wi_closed_at();
DROP FUNCTION IF EXISTS fn_wi_updated_at();
DROP TABLE IF EXISTS work_items CASCADE;
DROP SEQUENCE IF EXISTS wi_seq_ieops;
DROP SEQUENCE IF EXISTS wi_seq_marketplace;
DROP SEQUENCE IF EXISTS wi_seq_aihub;
