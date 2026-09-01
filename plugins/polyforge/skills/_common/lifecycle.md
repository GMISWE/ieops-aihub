# _common/lifecycle.md — step lifecycle & ownership (injected for pf-execute)

> polyforge owns the wi lifecycle; superpowers is only the content **engine**. Resident here =
> what every step needs; the once-per-wi calls are on demand — `Read`
> 📄 **`@@PLUGIN_ROOT@@/skills/_common/references/lifecycle-details.md`** (not injected — hard
> 10,000-char payload budget, aihub#304).

## Bracket every step

**No `pf_get_step` before `pf_update_step`** — the bracket needs no version number and the server
guards concurrency itself. Start the FIRST step, then complete-and-advance:

```
sa_id = new_ulid()
pf_update_step(work_item_id=<current>, step_id=<first step>, status="in_progress")
# ... engine runs ...
next_sa = new_ulid()
pf_update_step(work_item_id=<current>, step_id=<this step>, status="completed",
               step_attempt_id=sa_id,
               artifact_summary="<one sentence, status only, <=4096 chars>",
               next_step=<next step>, next_step_attempt_id=next_sa)   # starts the next one
sa_id = next_sa
```

- `next_step` completes one step and starts its successor in ONE transaction. Omit it on the LAST
  step and on `failed` (rejected there, not ignored). ⚠️ If `pf_update_step` does not publish it
  the binary is older — read the on-demand file, that fallback has a trap.
- `step_id` is the scenario `## Step:` name; unvalidated, so a typo is silent.
- `artifact_summary` is status only — never diff / plan / code / artifact content.
- Long steps: add `heartbeat=true` to an `in_progress` call every ~5 min.

## Ownership

claim / locks / `pf_update_step` / `pf_save_artifact` / commit / push / PR / wrap / CI gating are
**polyforge's**; superpowers produces content only, which is **not** a lifecycle bypass — the iron
rule is about skipping claim / step / artifact / wrap. **Execute boundary (D6)**: let superpowers
run its implementation loop but **stop before `superpowers:finishing-a-development-branch`**.

**`.pf_*` hygiene**: never stage `.pf_meta.json` / `.pf_steps.json` — write them outside the
worktree, `git checkout HEAD --` them, or use `pf_commit(paths=[...])`.

## Execute step only: `pf_acquire_locks(work_item_id=<current>)` BEFORE the loop

At the very start of the **execute** step — before the loop, before reading the scenario .md,
before any dispatch. Not for spec or plan.

- **`ErrConflictLockTaken`** (payload carries `conflict_with`) → **STOP, do NOT enter the loop.**
  Report the file, holder and attempt_id three-segment (template in §3), offer `/pf-stop --pause`,
  and do **NOT** call `pf_complete_attempt(failed)` — the attempt stays active, waiting.
- **`acquired` / `already_held`** → proceed.

## Once per wi — 🔴 `Read` the on-demand file §0 before either

- **`commit_and_pr` step → `pf_ship(...)`**, not `pf_commit`→`pf_push`→`pf_pr`. It
  **force-pushes** to origin, and failure returns JSON that may already report a commit.
- **End of the loop → `pf_complete_attempt(status="wrapped", note=...)`**, then remove each
  worktree, then their shared `pf.<slug>/` parent ONCE. The terminal call deletes the state file,
  so `note=` must ride with it, never follow it.

Do not reconstruct either from memory — the argument shapes and the ordering are the point.

## Three-segment output

Every pf-* response uses `Result` / `Status` / `Next steps` (labels from the session-start
`output-format` fragment, already in context). For `requires_human_session=true` wi's, take "Next
steps" from `using-polyforge/fragments/post-claim-routing.md` — on-demand, so `Read` it first.
