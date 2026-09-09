-- +goose Up

-- aihub#416 D7: delete the resource_locks rows left behind by the retired
-- git_branch / deploy_env derivations, and leave one lock_released event per row
-- so the deletion is auditable.
--
-- ── Why they cannot be left alone ────────────────────────────────────────────
--
-- Migration 0037's sibling change stops DERIVING these two types
-- (domain.resourceToLock). It does not remove the rows already in the table, and
-- nothing else will either:
--
--   * every terminal path deletes by owner_attempt_id, so a row only goes when
--     its attempt ends;
--   * pause deletes file_scope ONLY (acquireLocksReleasePausedSQL), keeping
--     every other type "for resume";
--   * the orphan sweep (gc.go, orphanLockSweepSQL) skips any attempt that is
--     still running or paused.
--
-- So a permanently paused attempt holds its git_branch / deploy_env rows
-- forever. That is not a hypothetical — it is the reported symptom this work
-- item exists for: a paused observer blocking every deploy to an environment
-- with no automatic exit.
--
-- 🔴 After the derivation retires, those rows change character rather than
-- becoming harmless. Nothing derives them, so nothing probes them, so they stop
-- blocking anyone — and they also stop being visible. They become rows that
-- pf_acquire_locks still reports in `already_held` and that no declaration can
-- explain, which is worse than either a live lock or no lock: an auditor asking
-- "should this be held?" gets a row with no answerable provenance. Deleting them
-- in the same release as the change that orphans them is what keeps the lock
-- table meaning one thing.
--
-- ── Why an event per row, and not a bare DELETE ──────────────────────────────
--
-- aihub#343 exists because resource_locks rows were created and destroyed with
-- no audit trail, which made two disputes about whether a lock had been released
-- undecidable. A silent bulk DELETE here would manufacture the same condition on
-- purpose, at scale, in one statement: every future reader of those attempts'
-- timelines would see locks acquired and never released.
--
-- The events are byte-compatible with the ones package domain emits — same
-- event_type, same payload keys (resource_type / resource_key / attempt_id /
-- claim_epoch / cause / op_id), same one-row-per-lock granularity — so
-- pf_read_events needs no special case to read them. `cause` is the NEW value
-- 'derivation_retired' (domain.LockCauseDerivationRetired), and it has to be
-- new: attempt_terminal and orphan_sweep both assert the holder finished, and
-- these holders did not. Most of them are still paused right now.
--
-- All of it in ONE transaction (goose wraps a migration by default), so the
-- rows and their events land together or not at all. An event describing a
-- deletion that did not happen is the failure aihub#343's own header calls
-- "checkable and wrong ... worse than absent".
--
-- ── Counting ────────────────────────────────────────────────────────────────
--
-- The pre-delete counts are RAISEd as NOTICEs, and separately they are
-- RE-DERIVABLE from the event rows the same transaction writes:
--
--   SELECT payload->>'resource_type', count(*)
--   FROM agent_events
--   WHERE event_type = 'lock_released'
--     AND payload->>'cause' = 'derivation_retired'
--   GROUP BY 1;
--
-- 🔴 The second form is the one to rely on. Measured on 0034: goose DISCARDS
-- NOTICE output, so a count that exists only as a RAISE is a count nobody will
-- ever read. The NOTICE stays for an operator running this by hand with psql;
-- the events are what the deploying session records in aihub#416's attrs.
--
-- ── Ordering and rollback ───────────────────────────────────────────────────
--
-- Apply order does not matter for correctness, in either direction. Running this
-- BEFORE the new binary deploys means the old binary re-derives some of these
-- rows on its next claim; they are then ordinary live locks again and the
-- de-locking simply has not taken effect yet. Running it after is the clean
-- case. Neither loses data and neither wedges a claim.
--
-- The Down section deliberately does not restore the rows: see there.

-- +goose StatementBegin
DO $$
DECLARE
    op_id         text := 'lop_' || substr(md5(random()::text || clock_timestamp()::text), 1, 8);
    branch_rows   bigint;
    env_rows      bigint;
    event_rows    bigint;
BEGIN
    SELECT count(*) FILTER (WHERE resource_type = 'git_branch'),
           count(*) FILTER (WHERE resource_type = 'deploy_env')
      INTO branch_rows, env_rows
      FROM resource_locks;

    RAISE NOTICE 'aihub#416: retiring lock derivation — git_branch rows: %, deploy_env rows: % (op_id %)',
        branch_rows, env_rows, op_id;

    -- The event INSERT reads the rows through the DELETE's RETURNING clause, so
    -- there is exactly one event per row actually removed. Deriving the event
    -- set from a separate SELECT would let the two disagree if anything changed
    -- between them — which is the "recomputed record naming locks that were
    -- still held" mistake resource_events.go's header records.
    --
    -- work_item_id comes from the owning attempt because agent_events.work_item_id
    -- is FK-checked, and chk_evt_work_item_id exempts only a fixed list of event
    -- types that lock_released is not on — so an event with no work item cannot
    -- be written at all.
    --
    -- The LEFT JOIN and the IS NOT NULL below are therefore belt-and-braces, and
    -- are labelled as such rather than left to look load-bearing. Measured on the
    -- live schema: resource_locks.owner_attempt_id is
    -- `FOREIGN KEY ... REFERENCES run_attempts(id) ON DELETE RESTRICT` and
    -- run_attempts.work_item_id is NOT NULL with its own FK, so every lock row
    -- resolves to a work item and the filtered-out count is structurally zero.
    -- An inner join would behave identically TODAY; this form is chosen because
    -- its failure mode is "one row loses its event" rather than "the DELETE
    -- silently covers fewer rows than the count above reported", and the second
    -- NOTICE prints the difference so a surprise is legible instead of implied.
    WITH deleted AS (
        DELETE FROM resource_locks rl
        WHERE rl.resource_type IN ('git_branch', 'deploy_env')
        RETURNING rl.resource_type, rl.resource_key, rl.owner_attempt_id, rl.claim_epoch
    ), resolved AS (
        SELECT d.*, ra.work_item_id, wi.project
        FROM deleted d
        LEFT JOIN run_attempts ra ON ra.id = d.owner_attempt_id
        LEFT JOIN work_items wi ON wi.id = ra.work_item_id
    )
    INSERT INTO agent_events (id, work_item_id, run_attempt_id, event_type, payload, project)
    SELECT 'evt_' || substr(md5(random()::text || clock_timestamp()::text || r.resource_key), 1, 8),
           r.work_item_id,
           r.owner_attempt_id,
           'lock_released',
           jsonb_build_object(
               'resource_type', r.resource_type,
               'resource_key',  r.resource_key,
               'attempt_id',    r.owner_attempt_id,
               'claim_epoch',   r.claim_epoch,
               'cause',         'derivation_retired',
               'op_id',         op_id,
               'wi',            'aihub#416'
           ),
           r.project
    FROM resolved r
    WHERE r.work_item_id IS NOT NULL;

    GET DIAGNOSTICS event_rows = ROW_COUNT;

    RAISE NOTICE 'aihub#416: % lock_released event(s) written for % deleted row(s); % row(s) had no resolvable work item and were deleted without one',
        event_rows, branch_rows + env_rows, (branch_rows + env_rows) - event_rows;
END
$$;
-- +goose StatementEnd

-- +goose Down

-- Deliberately a NO-OP, and that is a decision rather than laziness.
--
-- The rows cannot be restored: a resource_locks row is (resource_type,
-- resource_key, owner_attempt_id, claim_epoch), and the first three are
-- recoverable from the events above while the whole point is that they should
-- not come back — re-inserting them would re-block the deploys this migration
-- unblocked, on behalf of attempts most of which are paused and will never
-- release them.
--
-- Rolling the BINARY back is separately safe and needs nothing here: the
-- production rollback anchor is a container swap (docs/deployment.md), the old
-- binary re-derives a git_branch or deploy_env lock the next time a work item
-- declaring a repo or a service is claimed, and until then it simply finds fewer
-- rows than it left. Nothing reads a lock row's absence as an error.
--
-- The events are NOT deleted either. They record something that really happened;
-- a down migration that erased its own audit trail would leave a database whose
-- lock history has a hole exactly where somebody would look.
SELECT 1;
