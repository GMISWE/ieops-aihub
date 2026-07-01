-- +goose Up
-- aihub#201: latest_id cursor — every row in a supersede lineage points to the
-- current head (newest version). New rows self-point at INSERT (memory.go);
-- this migration backfills existing rows.
ALTER TABLE memories ADD COLUMN IF NOT EXISTS latest_id TEXT REFERENCES memories(id);

-- default: self-head for every row
UPDATE memories SET latest_id = id WHERE latest_id IS NULL;

-- Backfill per connected component (M1 review fix): a component's head must be
-- its NEWEST NON-REDACTED row (falling back to the newest of any status only
-- if the whole component is redacted) — never a redacted row, since Redact()
-- flips status in place (no new row) and GetMemoryByID filters out
-- status='redacted', so a redacted latest_id would 404 the whole lineage.
--
-- Step 1: walk UP via supersedes_id from every row to its component ROOT (the
-- row with supersedes_id IS NULL). This anchors every row in a component to
-- the same key regardless of how many branches/redactions sit between them,
-- which a walk-DOWN-from-each-start approach can split on.
WITH RECURSIVE root_walk(start_id, cur_id) AS (
    SELECT id, id FROM memories
    UNION ALL
    SELECT rw.start_id, m.supersedes_id
    FROM root_walk rw
    JOIN memories m ON m.id = rw.cur_id
    WHERE m.supersedes_id IS NOT NULL
),
component_roots AS (
    -- For each start_id, the root is the last non-NULL cur_id reached, i.e.
    -- the row whose own supersedes_id IS NULL.
    SELECT DISTINCT ON (rw.start_id) rw.start_id, rw.cur_id AS root_id
    FROM root_walk rw
    JOIN memories m ON m.id = rw.cur_id
    WHERE m.supersedes_id IS NULL
    ORDER BY rw.start_id, rw.cur_id
),
-- Step 2: from each component root, walk DOWN via reverse supersedes_id to
-- enumerate every member of the component.
down_walk(root_id, member_id) AS (
    SELECT root_id, root_id FROM (SELECT DISTINCT root_id FROM component_roots) r
    UNION ALL
    SELECT dw.root_id, m.id
    FROM down_walk dw
    JOIN memories m ON m.supersedes_id = dw.member_id
),
-- Step 3: pick ONE head per root — newest non-redacted row sorts first;
-- if every member is redacted, fall back to the newest of any status.
component_heads AS (
    SELECT DISTINCT ON (dw.root_id) dw.root_id, dw.member_id AS head_id
    FROM down_walk dw
    JOIN memories m ON m.id = dw.member_id
    ORDER BY dw.root_id, (m.status = 'redacted') ASC, m.created_at DESC
)
UPDATE memories t
SET latest_id = ch.head_id
FROM component_roots cr
JOIN component_heads ch ON ch.root_id = cr.root_id
WHERE t.id = cr.start_id AND t.latest_id IS DISTINCT FROM ch.head_id;

-- S4: memory_updated (aihub#201) is emitted without a work_item_id when the
-- updated memory has none — add it to the NULL-wi whitelist alongside the
-- other memory_* fire-and-forget events. Replicates the exact live
-- definition from 0025 verbatim, plus the one new entry.
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
            'memory_updated',
            'wi_classification_missing',
            'wi_needs_attention',
            'partition_created'
        )
    ) NOT VALID;

ALTER TABLE agent_events VALIDATE CONSTRAINT chk_evt_work_item_id;

-- +goose Down
-- Revert to 0025 post-state (drop memory_updated from the whitelist).
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

ALTER TABLE memories DROP COLUMN IF EXISTS latest_id;
