-- +goose Up

-- aihub#441 (aihub#411 row T2-3, residues (a) and (b)): retire 'lost' from
-- run_attempts.status and add 'cancelled'.
--
-- DEPLOY ORDER: apply this migration BEFORE starting the binary that writes
-- 'cancelled'. The two orderings fail very differently and neither loses data:
--
--   migration first (the documented order): correct.
--   binary first: CancelWorkItem's new UPDATE violates the OLD CHECK
--     (SQLSTATE 23514). It runs inside the cancel transaction, which rolls back
--     whole, so pf_cancel_work_item is REFUSED rather than half-applied — the
--     work item stays queued/paused/blocked and its locks stay held, which is
--     exactly the pre-aihub#355 state and is recoverable by cancelling again
--     after the migration. No other path writes a status this migration
--     changes, so nothing else is affected in that window.
--
-- ROLLBACK: the production rollback anchor is a CONTAINER swap
-- (docs/deployment.md), which does not touch the schema. Rolling the binary
-- back therefore leaves the widened CHECK and any 'cancelled' rows in place.
-- That is safe to read: the old binary never writes 'cancelled', and its only
-- reader of this column is verifyAttemptCredential's `storedStatus != "running"`
-- branch, which handles an unrecognised status by NAMING it in an
-- ATTEMPT_MISMATCH message. So a row this migration made possible degrades to a
-- correct refusal on the old binary, not to a crash or a wrong verdict. The
-- rollback is NOT lossy. Only `goose down` / `make migrate-down` narrows the
-- CHECK again, and see the Down section for why that one is deliberately strict.
--
-- ── (a) Why 'lost' is retired rather than given a writer ─────────────────────
--
-- 'lost' has had ZERO writers since migration 0004 introduced it: no Go, no SQL,
-- no trigger and no migration ever assigns it, and no sweep in
-- internal/domain/gc.go assigns any run_attempts.status at all.
--
-- Its only ever-planned writer was design v1.24 C-R9-5 ("zombie sweeper 改为
-- system force_takeover + lost 状态"). That sweeper is not merely unbuilt, it is
-- unbuildable under the ownership model that replaced it. A zombie sweeper must
-- decide that a holder is dead, and the only signal for that is last_active_at
-- staleness — a TTL. Design v1.21 deleted exactly that mechanism, and 0004's own
-- DDL comment records the result two lines above this column: "After claim,
-- ownership is permanent; no expires_at" and "last_active_at is only for
-- monitoring ..., NOT used as a permission gate". The same reversal is visible
-- at the API surface (pf_renew_lease answers 410 Gone) and in the ready queue
-- (stale_running[] is an ownership REMINDER that releases nothing).
--
-- So 'lost' is not a feature waiting for an implementation. It is the residue of
-- a design that was reversed, and giving it a writer would mean re-introducing
-- the lease the reversal removed.
--
-- ── (b) Why 'cancelled' is added ─────────────────────────────────────────────
--
-- CancelWorkItem's own doc comment states the gap and defers it: "none of the
-- six legal run_attempts statuses means 'its work item was cancelled'" and
-- retiring the attempt "is a separate change because it alters run_attempts
-- semantics, not the lock set". This is that separate change.
--
-- 'superseded' cannot be reused for it. supersededByDetails keys on that exact
-- literal to attach a {"superseded_by": {actor_display, at}} payload, so a
-- cancelled attempt marked 'superseded' would offer a takeover story for a work
-- item nobody took over. And leaving the attempt 'paused' — today's behaviour —
-- makes verifyAttemptCredential answer ATTEMPT_PAUSED, whose entire contract
-- (aihub#209) is "keep your state file, this is resumable". A cancelled work
-- item is not resumable: FnClaimWorkItem rejects it as terminal. So the one
-- status a caller could observe was the one that gave it the wrong instruction.
--
-- Net vocabulary size is unchanged at six. That is arithmetic, not the point:
-- one value with no writer is replaced by one with a writer and a reader.

-- The pre-check exists so that a surprise is legible. Zero 'lost' rows are
-- expected in every database that has ever run this schema, because no released
-- binary could produce one. If this RAISEs, that expectation is false and the
-- finding is real: do NOT "fix" it by remapping the rows to another status,
-- because whatever wrote them is unaccounted for and remapping erases the
-- evidence. Without this block the same condition surfaces as a bare 23514
-- naming only the constraint.
-- +goose StatementBegin
DO $$
DECLARE lost_rows bigint;
BEGIN
    SELECT count(*) INTO lost_rows FROM run_attempts WHERE status = 'lost';
    IF lost_rows > 0 THEN
        RAISE EXCEPTION 'aihub#441: % run_attempts row(s) hold status=''lost'', a value no code path can write. Find the writer before narrowing the CHECK; do not remap the rows.', lost_rows;
    END IF;
END
$$;
-- +goose StatementEnd

ALTER TABLE run_attempts DROP CONSTRAINT run_attempts_status_check;
ALTER TABLE run_attempts ADD CONSTRAINT run_attempts_status_check
    CHECK (status IN ('running', 'paused', 'wrapped',
                      'failed', 'superseded', 'cancelled'));

-- +goose Down

-- Deliberately NOT tolerant. If any attempt was cancelled while this migration
-- was applied, re-adding the old CHECK fails with 23514 and the down migration
-- stops. Silently remapping those rows to 'paused' would restore precisely the
-- state aihub#441 removed: a cancelled work item holding an attempt that claims
-- to be resumable. A down migration that has to lie about data is a signal to
-- stop, not a step to automate.
ALTER TABLE run_attempts DROP CONSTRAINT run_attempts_status_check;
ALTER TABLE run_attempts ADD CONSTRAINT run_attempts_status_check
    CHECK (status IN ('running', 'paused', 'wrapped',
                      'failed', 'superseded', 'lost'));
