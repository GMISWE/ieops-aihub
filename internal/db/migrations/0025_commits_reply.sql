-- +goose Up
-- aihub#125: Add memory_commit_replied to the agent_events whitelist so the
-- fire-and-forget event emitted by ReplyCommit can persist without a work_item_id
-- (same pattern as memory_commit_resolved, memory_commit_edited, etc.).

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
            'memory_commit_resolved',
            'memory_commit_replied',
            'wi_classification_missing',
            'wi_needs_attention',
            'partition_created'
        )
    ) NOT VALID;

ALTER TABLE agent_events VALIDATE CONSTRAINT chk_evt_work_item_id;

-- +goose Down
-- Revert to 0022 post-state (drop memory_commit_resolved and memory_commit_replied).
-- NOTE: rolling back 0025 while keeping aihub#124+ code means resolve events on
-- wi-less memories silently fail the whitelist again (pre-0025 latent bug).

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
