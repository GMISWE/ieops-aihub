-- +goose Up
-- Remove attempt_zombied and wi_possibly_abandoned from the global event type
-- allowlist. The zombie sweep was removed in aihub#36; these types no longer
-- need to bypass the work_item_id NOT NULL check (they always had work_item_id
-- anyway, so this is a no-op for existing data).
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
            'wi_classification_missing',
            'wi_needs_attention',
            'partition_created'
        )
    ) NOT VALID;

ALTER TABLE agent_events VALIDATE CONSTRAINT chk_evt_work_item_id;

-- +goose Down
ALTER TABLE agent_events DROP CONSTRAINT IF EXISTS chk_evt_work_item_id;
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
            'wi_classification_missing',
            'wi_needs_attention',
            'attempt_zombied',
            'wi_possibly_abandoned',
            'partition_created'
        )
    ) NOT VALID;
ALTER TABLE agent_events VALIDATE CONSTRAINT chk_evt_work_item_id;
