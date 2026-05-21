-- +goose Up
-- Expand agent_events CHECK whitelist to include memory and GC event types (C3 fix)
-- agent_events is partitioned by range (created_at); the CHECK constraint lives on the
-- parent table only (NO INHERIT pattern isn't needed here — DROP + ADD on parent propagates).
ALTER TABLE agent_events
    DROP CONSTRAINT IF EXISTS chk_evt_work_item_id;

-- NOTE: For partitioned tables in PG14+, ADD CONSTRAINT on the parent automatically
-- propagates to all existing and future partitions. If this fails with "partition
-- constraint is violated", add NOT VALID + VALIDATE separately.
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

-- Validate without full-table lock (safe on large partitioned tables)
ALTER TABLE agent_events VALIDATE CONSTRAINT chk_evt_work_item_id;

-- +goose Down
ALTER TABLE agent_events DROP CONSTRAINT IF EXISTS chk_evt_work_item_id;
ALTER TABLE agent_events
    ADD CONSTRAINT chk_evt_work_item_id CHECK (
        work_item_id IS NOT NULL
        OR event_type IN (
            'phase_config_updated', 'admin_redact', 'admin_unblock',
            'system_gc', 'system_force_takeover', 'memory_gc'
        )
    );
