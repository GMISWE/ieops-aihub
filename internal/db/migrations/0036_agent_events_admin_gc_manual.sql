-- +goose Up

-- aihub#444, from the aihub#411 decision table §6.2 T2-5. Adds ONE entry to
-- chk_evt_work_item_id: `admin_gc_manual`.
--
-- ── Why this row belongs in this set ────────────────────────────────────────
--
-- Three sets governed agent_events.event_type and they disagreed. This one —
-- the 21-entry CHECK first written in 0025 and last replicated in 0026 — says
-- which events may be filed with NO work item. `admin_gc_manual` is an
-- admin-only type (internal/domain/event_types.go, AdminOnlyEventTypes) naming a
-- manual garbage collection, which by construction has no work item, and its
-- three siblings admin_redact / admin_unblock / admin_force_takeover are all
-- already here, as are system_gc and memory_gc. Its absence is an omission of
-- the same class as the flag inversion aihub#444 fixes in Go: it was added to
-- one set and to neither of the others.
--
-- The omission is not currently reachable through MCP — pf_emit_event marks
-- work_item_id REQUIRED — but POST /v1/events does not, and after the Go fix an
-- admin CAN legitimately send this type with admin:true. Leaving the CHECK
-- behind would move the defect rather than remove it: 403 today, 23514 -> 500
-- tomorrow.
--
-- ── Why this is a STRICT WIDENING, and what follows from that ───────────────
--
-- Old predicate:  work_item_id IS NOT NULL OR event_type IN (<21 names>)
-- New predicate:  work_item_id IS NOT NULL OR event_type IN (<the same 21> + 1)
--
-- Every row satisfying the old one satisfies the new one. 0026 ended with an
-- UNCONDITIONAL `VALIDATE CONSTRAINT`, so any database carrying 0026 has the old
-- predicate validated over every existing row — measured on a fresh pg16:
-- convalidated = t on the parent and on all seven partitions. A validated
-- narrower predicate over the same rows is a proof that the wider one holds too,
-- so VALIDATE below CANNOT fail on pre-existing data.
--
-- 🔴 That is why 0034's conditional-census shape is NOT copied here, and the
-- difference is worth stating because the two look superficially alike. 0034
-- added a NEW and NARROWING constraint to a table whose live distinct `type` set
-- had never been read, so its census bought real information and a real
-- fallback. Here the census would count zero by construction — and it would cost
-- exactly what it was supposed to avoid, because counting the violators is the
-- same full scan as validating. A census that can only return zero is a scan
-- with a story attached.
--
-- ⚠️ COST, named rather than waved at: agent_events is RANGE PARTITIONED on
-- created_at (0006 creates it that way, 0031 adds the default partition) and is
-- the highest-write table in the schema. The only published figure is aihub#266's
-- ~105,000 rows/day for the ieops project alone, and that was measured BEFORE the
-- alert flood it describes was fixed, so treat it as an upper bound rather than
-- the current rate — either way the table is large.
-- DROP + ADD recurses to every partition and VALIDATE scans every one of them.
-- The lock is what makes that acceptable: ADD ... NOT VALID takes a brief
-- ACCESS EXCLUSIVE and performs no scan, and VALIDATE CONSTRAINT takes SHARE
-- UPDATE EXCLUSIVE, which blocks neither reads nor writes. So the deploy pays
-- wall-clock time, not availability. Splitting the VALIDATE into a follow-up
-- migration would trade that time for a window in which convalidated is false
-- and nobody can tell whether it is false because of real outliers.
--
-- DEPLOY ORDER: migration FIRST. The binary shipping alongside it stops
-- returning 403 for `admin: true` + admin_gc_manual, so if the binary lands
-- first, that call reaches an INSERT the old CHECK still refuses and answers 500
-- instead. Binary-first is a strictly worse 403 for as long as the gap lasts, not
-- data loss. Migration-first has no such window: the wider CHECK accepts writes
-- the old binary never makes.
--
-- ROLLBACK: production rolls back by CONTAINER SWAP (docs/deployment.md), which
-- does not touch the schema, so the wider CHECK stays. Harmless — the old binary
-- refuses admin:true on this type in Go before the column is ever consulted.
-- Only `goose down` reverts it, and the Down section below restores 0026's exact
-- 21-name predicate. If any admin_gc_manual row with a NULL work_item_id exists
-- by then, that VALIDATE fails and says so, which is the correct outcome:
-- narrowing a constraint under live data is a decision, not a deploy step.
--
-- 🔴 That is measured, not predicted. Running `goose down` against a pg16 that
-- had four such rows in it (left by this work item's own DB tests before they
-- learned to clean up) failed exactly there:
--
--   ERROR 0036_agent_events_admin_gc_manual.sql: failed to execute SQL query
--   "ALTER TABLE agent_events VALIDATE CONSTRAINT chk_evt_work_item_id;":
--   check constraint "chk_evt_work_item_id" of relation "agent_events_2026_09"
--   is violated by some row (SQLSTATE 23514)
--
-- On a table with no such rows the same command succeeds and the constraint text
-- loses the name — verified in both directions, along with the re-`up`.
--
-- Go side: internal/domain/event_types.go (NullWorkItemEventTypes) mirrors this
-- list so the refusal is a 400 naming the field rather than a 500 carrying the
-- driver's text (§6.1 T1-4). internal/domain/event_types_test.go PARSES THIS FILE
-- and fails if the two stop naming the same set; it needs no database, so it runs
-- in the default `go test ./...`.

ALTER TABLE agent_events
    DROP CONSTRAINT IF EXISTS chk_evt_work_item_id;

ALTER TABLE agent_events
    ADD CONSTRAINT chk_evt_work_item_id CHECK (
        work_item_id IS NOT NULL
        OR event_type IN (
            'admin_force_takeover',
            'admin_gc_manual',
            'admin_redact',
            'admin_unblock',
            'memory_activated',
            'memory_archived',
            'memory_commit_deleted',
            'memory_commit_edited',
            'memory_commit_replied',
            'memory_commit_resolved',
            'memory_committed',
            'memory_created',
            'memory_gc',
            'memory_redacted',
            'memory_reinforced',
            'memory_updated',
            'partition_created',
            'phase_config_updated',
            'system_force_takeover',
            'system_gc',
            'wi_classification_missing',
            'wi_needs_attention'
        )
    ) NOT VALID;

ALTER TABLE agent_events VALIDATE CONSTRAINT chk_evt_work_item_id;

COMMENT ON CONSTRAINT chk_evt_work_item_id ON agent_events IS
    'aihub#444: which event types may be filed with NO work_item_id. This is NOT the event vocabulary — see internal/domain/event_types.go (EventVocabulary) for that, and NullWorkItemEventTypes for the Go mirror of this list.';

-- +goose Down
-- Revert to 0026's post-state (drop admin_gc_manual from the whitelist).
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
