-- +goose Up

-- aihub#416 D2: record, per attempt, the commit each repo's worktree started
-- from — {"<repo>": "<40-char sha>"}.
--
-- WHY A COLUMN EXISTS AT ALL. The de-locking ruling replaced the git_branch lock
-- with "git is the conflict detector", and that is true for WRITES (a rejected
-- push, a merge conflict) but says nothing about READS. A conclusion drawn by
-- reading a repo is only checkable if the tree it was read from is recorded, so
-- what the lock used to buy by exclusion is bought here by provenance instead.
--
-- 🔴 THIS IS NOT A LOCK, A LEASE OR A PRECONDITION, and every one of those
-- readings has to be refused explicitly because each has been proposed:
--
--   * It constrains nothing. An agent that pulls mid-attempt has left the pin
--     behind and no code path notices. The wi text's "a read-pinned repo must
--     refuse writes from the same wi or upgrade explicitly" was CANCELLED by the
--     owner ruling — a refusal keyed on a pin is a git_branch lock under a new
--     name, which is the thing being removed.
--   * It does not expire. Design v1.21 deleted expires_at from this schema
--     outright and handleRenewLease answers 410 Gone; a pin has no TTL, no
--     renewal and no staleness predicate. An old pin is not a broken pin, it is
--     an accurate record of an old starting point.
--
-- WHY A NEW COLUMN RATHER THAN run_attempts.prepared_workspace, which is JSONB,
-- on this very table and unused: that column is DEAD (aihub#487 gates it as
-- such) and its DDL comment says "local reference only, not validated". Reviving
-- it would attach a value that IS validated (40 hex chars, written server-side
-- from the claim) to a name and a comment that say the opposite, and cost the
-- same one ALTER TABLE.
--
-- SHAPE. JSONB rather than a side table because it is written once, read whole,
-- and never queried by key — the same reasons wi_step_state.scenario_ref
-- (migration 0018) is a column. NULL is the honest value for every row written
-- before this migration and for a claim that built no worktree at all; a repo
-- whose worktree could not be built is simply ABSENT from the object rather than
-- present with an empty string, so "no pin" and "pinned to nothing" cannot be
-- confused (owner ruling Q-4: the claim still succeeds and reports the failure
-- through worktree_problems).
--
-- DEPLOY ORDER: apply BEFORE starting the binary that writes it. The reverse
-- order fails loudly and harmlessly — the INSERT in FnClaimWorkItem names the
-- column, so a claim against the old schema is refused whole (42703) and nothing
-- is half-written. ROLLBACK to the previous binary is safe and lossless: that
-- binary never names the column, and rows carrying pins read fine because
-- nothing reads them.
ALTER TABLE run_attempts ADD COLUMN IF NOT EXISTS repo_pins JSONB;

COMMENT ON COLUMN run_attempts.repo_pins IS
    'aihub#416: {"<repo>": "<40-char sha>"} — the commit each worktree of this attempt started from. Provenance for a later reader, NOT a constraint: nothing enforces it, nothing detects a mid-attempt pull, and it does not expire. A repo with no usable worktree is absent from the object rather than mapped to "".';

-- +goose Down
ALTER TABLE run_attempts DROP COLUMN IF EXISTS repo_pins;
