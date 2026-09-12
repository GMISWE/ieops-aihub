-- +goose Up

-- aihub#350: per-attempt disposition record for findings the attempt produced
-- but did not fix, written by complete_attempt(status=wrapped) and by nothing
-- else. A JSON array of strings, one entry per finding:
--
--   "folded"            recorded in this wi's own record (the wrap note / PR
--                       body), tracked nowhere else. The default, and the
--                       cheapest legal entry on purpose. "folded:<text>" is
--                       accepted too; the text is a courtesy, not a contract.
--   "filed:<ref>"       a new work item was actually opened; <ref> is its id or
--                       slug and was resolved against work_items at wrap time.
--   "dropped:<reason>"  judged not worth tracking; the reason is required.
--
-- WHY THE GATE EXISTS. Measured 2026-09-02 over all 349 aihub work items: of 24
-- internally-derived open items, 18 had a parent that was already wrapped - the
-- queue is sediment left by finished work, deposited at the moment a parent
-- wrapped and its findings were never dispositioned. 33% of derived inflow
-- crosses projects, which is why the gate is on the server-side wrap transition
-- and not in any one project's scenario template.
--
-- WHY A COLUMN AND NOT attrs. work_items.attrs is replaced whole by any later
-- attrs write, so a disposition parked there is one careless write away from
-- silently vanishing. A column is written once, on the terminal transition, by
-- the transition itself.
--
-- NULL vs '[]' both exist and mean different things: NULL is every attempt
-- completed before this migration, every paused/failed attempt, and every wrap
-- accepted by a binary predating the gate; '[]' is a wrap that explicitly
-- declared "no findings". FnCompleteAttempt REFUSES a wrap that omits the list,
-- so on new binaries NULL cannot be written by a wrap at all.
--
-- 🔴 WHAT THIS COLUMN DOES NOT CLAIM: that the list is truthful. The server
-- cannot read the wrap note's prose, so "derived": [] from an attempt that
-- found three things is accepted. The gate changes which disposition is
-- cheapest, not what is falsifiable - the same posture polyforge-scenario#11's
-- A/B/C wrap tags take, stated there and here rather than pretended away.
--
-- DEPLOY ORDER: apply BEFORE starting the binary that writes it. The UPDATE in
-- FnCompleteAttempt names this column on every completion, so the new binary
-- against the old schema fails every completion with 42703; the old binary
-- against the new schema never names it and is unaffected. ROLLBACK to the
-- previous binary is safe: rows carrying dispositions read fine, nothing else
-- reads the column yet.
ALTER TABLE run_attempts ADD COLUMN IF NOT EXISTS derived JSONB;

COMMENT ON COLUMN run_attempts.derived IS
    'aihub#350: dispositions of findings this attempt produced but did not fix - ["folded"|"folded:<text>"|"filed:<wi id or slug>"|"dropped:<reason>", ...]. Written only by complete_attempt(status=wrapped), which refuses a wrap that omits it; [] is an explicit "no findings", NULL predates the gate or is a non-wrapped completion. filed: refs were resolved against work_items at wrap time. Honesty is not verified: the server cannot read the note prose this list summarises.';

-- +goose Down
ALTER TABLE run_attempts DROP COLUMN IF EXISTS derived;
