-- +goose Up
-- aihub#268: agent_events had no DEFAULT partition, so the *only* thing standing
-- between the service and a total write-path outage was the partition_create GC
-- sweep succeeding every month. Almost every aihub action writes agent_events
-- (wi state transitions, claim, memory ops, gc, queue scans), so a missing
-- monthly partition is not "the audit log dropped a row" — it is
-- `ERROR: no partition of relation "agent_events" found for row` on the whole
-- write path. Migration 0006 pre-created 2026_05..2026_10 only, and the sweep
-- had never actually had to create a partition before 2026-09-01 (its horizon,
-- "current month + 2", stayed inside October all summer), so its ability to do
-- so on this database — Cloud SQL, where the app role is not a superuser and
-- must still *own* agent_events to attach a partition — was never exercised.
--
-- A DEFAULT partition turns that hard failure into a degraded one: rows for a
-- month with no range partition land here instead of aborting the transaction.
-- It is a safety net, not the mechanism — RunPartitionCreate
-- (internal/domain/gc.go) still creates the real monthly partitions ahead of
-- time and now drains anything this table caught into them.
--
-- Cost of keeping it: queries whose created_at predicate cannot be proven
-- disjoint from "everything not covered by a range partition" must also scan
-- this partition. It is expected to stay empty, so that scan is on an empty
-- heap. Partition pruning for the normal bounded-range queries is unaffected.
--
-- Indexes: agent_events' indexes are defined on the partitioned parent, so
-- Postgres creates the matching per-partition indexes here automatically.

-- Pre-flight: attaching a partition requires OWNERSHIP of the parent, and on
-- Cloud SQL the application role is not a superuser. Postgres' own message for
-- this is a bare `must be owner of table agent_events`, which does not say which
-- role does own it or what to do — and a failed 0031 is the worst outcome
-- available here, because it means the safety net never lands while the sweep
-- that needs the same privilege keeps failing too. So say it plainly.
-- To check by hand before deploying:
--   SELECT tableowner FROM pg_tables WHERE tablename = 'agent_events';
--   SELECT current_user;
-- +goose StatementBegin
DO $$
DECLARE
    parent_owner name;
BEGIN
    SELECT pg_get_userbyid(relowner) INTO parent_owner
      FROM pg_class WHERE oid = 'agent_events'::regclass;

    -- Membership, not string equality: ownership through a granted role counts.
    IF NOT pg_has_role(current_user, parent_owner, 'USAGE') THEN
        RAISE EXCEPTION
            'aihub#268: cannot attach the DEFAULT partition — agent_events is owned by "%", but this migration is running as "%", which is not a member of it. Grant it (GRANT % TO %) or reassign (ALTER TABLE agent_events OWNER TO %), then re-run. The same privilege is required by the partition_create GC sweep, so leaving it unfixed reinstates the aihub#268 outage.',
            parent_owner, current_user, parent_owner, current_user, current_user;
    END IF;
END $$;
-- +goose StatementEnd

CREATE TABLE IF NOT EXISTS agent_events_default PARTITION OF agent_events DEFAULT;

-- +goose Down
-- NOTE: dropping a partition drops the rows it holds. In steady state this table
-- is empty (the sweep keeps range partitions ahead of clock_timestamp() and
-- drains this one), but if the sweep had been failing, this discards the events
-- it caught. Check `SELECT count(*) FROM agent_events_default` before rolling
-- back, and copy anything there out first.
DROP TABLE IF EXISTS agent_events_default;
