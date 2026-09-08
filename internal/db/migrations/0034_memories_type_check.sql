-- +goose Up

-- DEPLOY ORDER: safe in EITHER order against the binary, which is unusual
-- enough to say why. The constraint refuses exactly the values
-- internal/domain/memory.go (Remember) has already refused with a 400 since
-- aihub#289, so no write the running binary can perform is affected by it.
--   migration first (the documented order): correct.
--   binary first: also correct, and indistinguishable.
--
-- ROLLBACK: the production rollback anchor is a CONTAINER swap
-- (docs/deployment.md), which does not touch the schema, so rolling the binary
-- back leaves this constraint in place. That is harmless for the same reason —
-- the old binary refuses the same values in Go. Only `goose down` /
-- `make migrate-down` drops it, and dropping it destroys nothing: a constraint
-- holds no rows.
--
-- aihub#445, from the aihub#411 decision table §6.2 T2-6. memories.type carried
-- FOUR vocabularies for one column, and the column itself was the one layer
-- with no opinion at all:
--
--   DB   nothing. `type TEXT NOT NULL` (0006_events_memories.sql).
--   Go   four prefixes plus a '|' ban (internal/domain/memory.go, Remember) —
--        deliberately lenient.
--   MCP  a 13-value enum on pf_remember, withdrawn by this same work item: the
--        untyped AddTool path never enforced it (internal/domain/user_fields.go
--        records the measurement), so it stated a contract nothing kept.
--   UI   a 19-value select list (internal/domain/memory.go, MemoryTypeEnum),
--        documented in its own comment as "Select-UX list ONLY".
--
-- The owner ruling KEEPS the leniency. An off-list type carrying a legal prefix
-- is a legitimate value and stays one; this constraint must never be narrowed
-- to either curated list. What the ruling adds is the missing layer, and the
-- reason it has to be this layer: the prefix is what decides whether a memory
-- is ever recallable. internal/domain/embedding.go (EmbeddablePrefixes) embeds
-- experience./fact./rule. and the vector recall path's WHERE requires an
-- emb_vector, while a '|' inside the type is rejected outright by the read
-- path. A row whose type carries no legal prefix is therefore write-only data —
-- no type filter can name it, no select list renders it, and nothing forbade
-- creating one. Go refuses to create one; the column is the only layer that can
-- make it IMPOSSIBLE.
--
-- ── Equivalence with the Go check is a requirement, not a coincidence ────────
--
-- The predicate below is the Go predicate, term for term:
-- starts_with(type, p) is strings.HasPrefix(type, p), strpos(type, '|') = 0 is
-- !strings.Contains(type, "|"), and the four prefixes are the four elements of
-- internal/domain/memory.go (MemoryTypePrefixes), which Remember itself ranges
-- over. internal/domain/memory_type_check_test.go PARSES THIS FILE and fails if
-- the two stop naming the same set — it needs no database, so it runs in the
-- default `go test ./...`.
--
-- The direction of any future disagreement matters more than the fact of it.
-- A CHECK STRICTER than Go manufactures a new defect of exactly the class
-- aihub#433 fixed: Go answers 200, the column answers SQLSTATE 23514, and the
-- caller receives a 500 carrying the driver's constraint text instead of a 400
-- naming the field. A CHECK WIDER than Go is merely inert. So if these two ever
-- have to differ, widen the CHECK — never narrow it.
--
-- The policy this obeys is stated in full in internal/domain/work_item_fields.go
-- and applied per-CHECK rather than per-field (aihub#411 §6.1 T1-4): a
-- vocabulary a DB CHECK enforces is ALSO validated in Go and answered with a 400
-- naming the field; the CHECK is the last line of defence, never the
-- caller-facing one. Go's side of that already exists here and predates this
-- migration.
--
-- ── MIGRATION CAUTION: why NOT VALID, and what would retire it ──────────────
--
-- Adding a VALIDATED check to a populated table scans it and fails the entire
-- migration on the first off-prefix row. §6.4 item 4 records that the live
-- distinct `type` set has never been read, and it was not reachable from where
-- this migration was written either (production Postgres is inside the compose
-- network, docs/deployment.md). Assuming it clean is the move aihub#238
-- explicitly did not make with unmappable declared resource types.
--
-- So the constraint is added NOT VALID and then validated CONDITIONALLY, in this
-- same migration:
--
--   * NOT VALID still enforces every INSERT and every UPDATE, immediately. The
--     guarantee this work item is about — an unrecallable row becomes impossible
--     to CREATE — holds from the moment this runs, on any database, clean or not.
--     NOT VALID withholds exactly one thing: the scan of rows already there.
--   * The DO block counts those. If there are none it runs VALIDATE CONSTRAINT,
--     so on a clean database the end state is indistinguishable from a plain
--     validated CHECK and the existing rows are covered too. That is the
--     expected outcome, and it is what CI and every fresh database get.
--   * If there ARE outliers it records every distinct offending type with its
--     row count and leaves the constraint NOT VALID. The migration SUCCEEDS. A
--     deploy is not the place to decide what an unmappable historical type
--     should become, and failing here would leave the column unguarded for the
--     whole time that decision took — the worst of the two outcomes, not the
--     safe one.
--
-- That census IS the measurement §6.4 item 4 asks for, taken against the live
-- database at the one moment something is certainly connected to it. It is
-- written to the CONSTRAINT'S COMMENT, and that is not decoration:
--
--   SELECT obj_description(oid, 'pg_constraint')
--     FROM pg_constraint
--    WHERE conrelid = 'memories'::regclass AND conname = 'memories_type_check';
--
-- ⚠️ A RAISE alone would not survive the deploy. Measured on goose v3.28.0
-- against this file: the WARNING below is printed by psql and DISCARDED by
-- goose, whose pgx driver installs no notice handler — a `goose up` over a
-- table holding four outlier rows logged `OK 0034_memories_type_check.sql` and
-- nothing else. docs/deployment.md drives migrations with goose, so a census
-- that existed only as a log line would exist nowhere. The RAISE is kept
-- because it is free and psql is how this gets run by hand; the COMMENT is what
-- makes the measurement readable afterwards.
--
-- The comment states what was true AT MIGRATION TIME. `convalidated` in
-- pg_constraint is the live truth and does not go stale.
--
-- To take the census without deploying:
--
--   SELECT type, count(*) FROM memories
--    WHERE NOT (starts_with(type,'experience.') OR starts_with(type,'fact.')
--            OR starts_with(type,'rule.')       OR starts_with(type,'methodology.'))
--       OR strpos(type,'|') > 0
--    GROUP BY type ORDER BY type;
--
-- Once each outlier has been settled row by row, one statement finishes the job
-- and no migration is needed to rehearse it:
--
--   ALTER TABLE memories VALIDATE CONSTRAINT memories_type_check;
--
-- It takes SHARE UPDATE EXCLUSIVE, not ACCESS EXCLUSIVE, so it can run against a
-- live database. Re-running this migration also validates, once the rows are
-- gone: the DROP ... IF EXISTS below makes the whole Up section idempotent.

ALTER TABLE memories DROP CONSTRAINT IF EXISTS memories_type_check;

ALTER TABLE memories ADD CONSTRAINT memories_type_check CHECK (
    (
        starts_with(type, 'experience.')
        OR starts_with(type, 'fact.')
        OR starts_with(type, 'rule.')
        OR starts_with(type, 'methodology.')
    )
    AND strpos(type, '|') = 0
) NOT VALID;

-- +goose StatementBegin
DO $$
DECLARE
    outliers TEXT;
BEGIN
    SELECT string_agg(format('%L (%s rows)', t, n), ', ' ORDER BY t)
      INTO outliers
      FROM (
            SELECT type AS t, count(*) AS n
              FROM memories
             WHERE NOT (
                        starts_with(type, 'experience.')
                     OR starts_with(type, 'fact.')
                     OR starts_with(type, 'rule.')
                     OR starts_with(type, 'methodology.')
                   )
                OR strpos(type, '|') > 0
             GROUP BY type
           ) s;

    IF outliers IS NULL THEN
        ALTER TABLE memories VALIDATE CONSTRAINT memories_type_check;
        EXECUTE format('COMMENT ON CONSTRAINT memories_type_check ON memories IS %L',
            'aihub#445: VALIDATED at migration time — memories.type held no off-vocabulary row.');
        RAISE NOTICE 'aihub#445: memories.type holds no off-vocabulary rows; memories_type_check is VALIDATED over the whole table.';
    ELSE
        EXECUTE format('COMMENT ON CONSTRAINT memories_type_check ON memories IS %L',
            'aihub#445: NOT VALID at migration time. Enforced for every INSERT and UPDATE; '
            || 'these PRE-EXISTING rows are outside it: ' || outliers
            || '. Settle each one, then: ALTER TABLE memories VALIDATE CONSTRAINT memories_type_check;');
        RAISE WARNING 'aihub#445: memories_type_check was added but left NOT VALID — it guards every future write, and these existing rows are outside it: %. Settle each one, then run: ALTER TABLE memories VALIDATE CONSTRAINT memories_type_check;', outliers;
    END IF;
END $$;
-- +goose StatementEnd

-- +goose Down
ALTER TABLE memories DROP CONSTRAINT IF EXISTS memories_type_check;
