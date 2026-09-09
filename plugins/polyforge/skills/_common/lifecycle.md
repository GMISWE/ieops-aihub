# _common/lifecycle.md — step lifecycle & ownership

> Resident = what every step needs. Once-per-wi detail is on demand: `Read`
> 📄 **`@@PLUGIN_ROOT@@/skills/_common/references/lifecycle-details.md`** (aihub#304 budget).

## Bracket every step

**No `pf_get_step` before `pf_update_step`** — the bracket needs no version number and the server
guards concurrency itself. Do call it for prior-step context (`completed_steps`). Start the FIRST
step, then complete-and-advance:

```
sa_id = new_ulid()
pf_update_step(work_item_id=<current>, step_id=<first step>, status="in_progress")
# ... engine runs ...
next_sa = new_ulid()
pf_update_step(work_item_id=<current>, step_id=<this step>, status="completed",
               step_attempt_id=sa_id,
               artifact_summary="<[structured line] status sentence, <=4096 chars>",
               next_step=<next step>, next_step_attempt_id=next_sa)   # starts the next one
sa_id = next_sa
```

- `next_step` completes one step and starts its successor in ONE transaction. Omit it on the LAST
  step and on `failed` (rejected there, not ignored). ⚠️ Not published? Older binary — §1.
- `step_id` is the scenario `## Step:` name; unvalidated, so a typo is silent.
- `artifact_summary`: status only, no diff / plan / code. Optional structured lead line:
  `pr=<owner/repo>#<number> base=<branch>` or `Pattern <A|B>:`.
- Long steps: add `heartbeat=true` to an `in_progress` call every ~5 min.

## Ownership

claim / locks / `pf_update_step` / `pf_save_artifact` / commit / push / PR / wrap / CI gating are
**polyforge's**; an engine produces content only.

**`.pf_*` hygiene**: never stage `.pf_meta.json`, the only file the engine writes — §5.

## Execute step only: `pf_acquire_locks(work_item_id=<current>)` BEFORE the loop

At the very start of the **execute** step — before the loop, before reading the scenario .md,
before any dispatch. Not for spec or plan. **`acquired`/`already_held`** → proceed.
**`ErrConflictLockTaken`** → **STOP, do NOT enter the loop** and do **NOT** call
`pf_complete_attempt(failed)`; the attempt stays active, waiting. 🔴 `Read` §3 — it has the
conflict fields, the report template and what to offer instead.

## Once per wi — 🔴 `Read` the on-demand file §0 before either

`commit_and_pr` → **`pf_ship(...)`**; end of the loop → **`pf_complete_attempt(status="wrapped",
note=...)`**, then worktree cleanup. §0 has the argument shapes, the ordering and the force-push
warning — do not reconstruct them from memory.

## Three-segment output

The format is in the header. For `requires_human_session=true` wi's take "Next steps" from
`using-polyforge/fragments/post-claim-routing.md` — on-demand, so `Read` it first.
