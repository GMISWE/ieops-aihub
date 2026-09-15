-- +goose Up

-- aihub#684: escape hatch for the no-steps-recorded gate in FnCompleteAttempt.
-- complete_attempt(status="wrapped") refuses with 409 CONFLICT_NO_STEPS_RECORDED
-- when the WORK ITEM has a commit/push/pr_opened agent_event (it produced code)
-- but wi_step_state.version==0 (no step was ever opened for it). This column is
-- the caller's required, non-empty explanation when they resend the wrap asking
-- the gate to be bypassed rather than opening the steps.
--
-- WHY A COLUMN AND THE SAME SHAPE AS run_attempts.derived (aihub#350, migration
-- 0040): a required-and-recorded escape hatch that mirrors a precedent already
-- proven to work, not a new design. NULL means "the gate never fired, or this
-- attempt predates it"; a non-NULL value is a caller's on-the-record reason for
-- wrapping without ever opening a step. A whitespace-only value is normalized to
-- NULL before it is stored or checked (FnCompleteAttempt does this with
-- strings.TrimSpace), so it can never satisfy the gate and can never be told
-- apart from "not given" — the same posture that keeps derived's [] and NULL
-- distinguishable, applied to the one input that must not be satisfiable by
-- accident.
--
-- WHAT THIS COLUMN DOES NOT CLAIM: only wrapped completions ever gate on it
-- (paused/failed pass through untouched, by design — see the gate's own
-- comment), and it says nothing about whether the reason given is true. The
-- gate moves the friction to "say something", not to "say something honest".
--
-- DEPLOY ORDER (DB <-> hub binary): apply BEFORE starting the binary that
-- writes it, same reasoning as migration 0040 — the new binary's UPDATE names
-- this column on every completion, so it 42703s against the old schema; the
-- old binary never names it and is unaffected either way. ROLLBACK to the
-- previous binary is safe: rows carrying a reason read fine, nothing else
-- reads the column yet.
--
-- SEPARATE DEPLOY ORDER (hub <-> CLIENT binary) — do not conflate this with
-- the DB<->hub order above, it is a different coupling with a different
-- required order: the gate itself lives in the HUB binary (cmd/aihub ->
-- internal/domain.FnCompleteAttempt); the no_steps_reason escape hatch is
-- sent by the CLIENT binary (cmd/polyforge -> internal/mcp), which ships and
-- updates on its OWN schedule (the launcher's binary-update channel, gated by
-- its own throttle) rather than with the hub. If the hub is deployed first, a
-- caller still on an older polyforge build physically cannot send
-- no_steps_reason: the gate degrades from "explain yourself" to "you cannot
-- wrap" for that caller, and failed/paused (both wrong about the outcome)
-- become the only exits. REQUIRED ORDER: release the client (polyforge)
-- binary FIRST, then deploy the hub. Precedent: aihub#350 recorded this same
-- hub<->client coupling in its PR body for the `derived` escape hatch.
ALTER TABLE run_attempts ADD COLUMN IF NOT EXISTS no_steps_reason TEXT;

COMMENT ON COLUMN run_attempts.no_steps_reason IS
    'aihub#684: required escape hatch when complete_attempt(status="wrapped") finds commit/push/pr_opened events on this WORK ITEM but wi_step_state.version==0 (no step was ever opened). A whitespace-only value is normalized to NULL before it is stored or checked, so it can never satisfy the gate and can never be told apart from "not given". NULL means the gate never fired or this row predates it. DEPLOY ORDER: apply BEFORE starting the binary that writes it (same reasoning as migration 0040).';

-- +goose Down
ALTER TABLE run_attempts DROP COLUMN IF EXISTS no_steps_reason;
