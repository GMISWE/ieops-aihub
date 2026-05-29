-- +goose Up
-- aihub#70 increment: enable edit/delete on existing commits.
-- (a) backfill `id` on existing commits.entries that lack one (newly-written
--     entries already include id via domain.CommitMemory).
-- (b) extend chk_evt_work_item_id whitelist with memory_commit_edited and
--     memory_commit_deleted so the audit events can persist (they have no
--     work_item_id — they're memory-scoped, like the existing memory_* set).

-- +goose StatementBegin
DO $$
DECLARE
    r RECORD;
BEGIN
    FOR r IN SELECT id, commits FROM memories WHERE commits != '[]'::jsonb LOOP
        UPDATE memories
        SET commits = (
            SELECT jsonb_agg(
                CASE
                    WHEN entry ? 'id' THEN entry
                    ELSE entry || jsonb_build_object('id', 'cm_' || substr(replace(gen_random_uuid()::text, '-', ''), 1, 8))
                END
            )
            FROM jsonb_array_elements(r.commits) AS entry
        )
        WHERE id = r.id;
    END LOOP;
END $$;
-- +goose StatementEnd

ALTER TABLE agent_events
    DROP CONSTRAINT IF EXISTS chk_evt_work_item_id;

ALTER TABLE agent_events
    ADD CONSTRAINT chk_evt_work_item_id CHECK (
        work_item_id IS NOT NULL
        OR event_type IN (
            'phase_config_updated',
            'admin_redact',
            'admin_unblock',
            'admin_force_takeover',
            'system_gc',
            'system_force_takeover',
            'memory_gc',
            'memory_created',
            'memory_activated',
            'memory_redacted',
            'memory_archived',
            'memory_committed',
            'memory_reinforced',
            'memory_commit_edited',
            'memory_commit_deleted',
            'wi_classification_missing',
            'wi_needs_attention',
            'partition_created'
        )
    ) NOT VALID;

ALTER TABLE agent_events VALIDATE CONSTRAINT chk_evt_work_item_id;

-- +goose Down
-- Revert the whitelist to the 0021 post-state (drop memory_commit_edited /
-- memory_commit_deleted). The id backfill is intentionally NOT reversed —
-- losing the generated id strings has no semantic consequence (writes will
-- continue to set fresh ids on new entries).

ALTER TABLE agent_events
    DROP CONSTRAINT IF EXISTS chk_evt_work_item_id;

ALTER TABLE agent_events
    ADD CONSTRAINT chk_evt_work_item_id CHECK (
        work_item_id IS NOT NULL
        OR event_type IN (
            'phase_config_updated',
            'admin_redact',
            'admin_unblock',
            'admin_force_takeover',
            'system_gc',
            'system_force_takeover',
            'memory_gc',
            'memory_created',
            'memory_activated',
            'memory_redacted',
            'memory_archived',
            'memory_committed',
            'memory_reinforced',
            'wi_classification_missing',
            'wi_needs_attention',
            'partition_created'
        )
    ) NOT VALID;

ALTER TABLE agent_events VALIDATE CONSTRAINT chk_evt_work_item_id;
