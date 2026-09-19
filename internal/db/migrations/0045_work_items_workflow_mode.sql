-- +goose Up

-- aihub#720: work_items gains an EXPLICIT workflow-mode discriminator.
--
-- Until now the only signal a work item's step authority could be read from
-- was steps_version: 0 meant "no pinned workflow" and therefore "the legacy
-- scenario graph owns this work item's steps". That is an IMPLICIT legacy
-- signal, and it is ambiguous in both directions:
--
--   * a work item created with steps that are still being composed looks
--     exactly like a legacy one until the pin lands;
--   * a legacy work item and a DB-mode work item whose generation has not run
--     a single step are indistinguishable to every reader that has to decide
--     which dispatch path owns them.
--
-- workflow_mode names the authority explicitly, with three values:
--
--   legacy   the scenario-graph path: the work item keeps the template step
--            graph from the scenario clone. This is what every row created
--            before this migration is, and what a create without `steps`
--            still is — the default below preserves that semantics exactly,
--            so nothing about the legacy path changes in this migration.
--   db       the work item was born with a pinned workflow generation
--            (CreateWorkItem's `steps` branch): generation 1 was resolved,
--            validated and pinned in the SAME transaction that inserted the
--            row, so a db-mode row always has steps_version >= 1.
--   pending  the work item is waiting for orchestration: it was filed
--            without steps but with workflow_mode=pending, and it is NOT
--            claimable until a first workflow generation is pinned through
--            the revision route (a claim answers COMPOSE_PENDING). This is
--            the state an orchestrator parks a work item in while it decides
--            the flow.
--
-- ADDITIVE by the 0044 deploy rule: one new column with a safe default, so
-- apply BEFORE the binary that reads it; an old binary never names the column
-- and keeps working. Existing rows are backfilled conservatively as 'legacy'
-- by the DEFAULT — no UPDATE, no history rewrite, no generation touched.
--
-- The whole Up is idempotent (ADD COLUMN IF NOT EXISTS, constraint inline),
-- mirroring 0043/0044's replay-hazard rule (aihub#444): a test replaying it
-- against a migrated database is a no-op.

ALTER TABLE work_items ADD COLUMN IF NOT EXISTS workflow_mode TEXT NOT NULL DEFAULT 'legacy'
    CHECK (workflow_mode IN ('legacy', 'db', 'pending'));

-- +goose Down

ALTER TABLE work_items DROP COLUMN IF EXISTS workflow_mode;
