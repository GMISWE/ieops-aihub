# _common/lifecycle.md — step lifecycle & ownership (injected for pf-execute)

> Resident here = what every step needs; once-per-wi calls are on demand — `Read`
> 📄 **`@@PLUGIN_ROOT@@/skills/_common/references/lifecycle-details.md`** (not injected: hard
> 10,000-char payload budget, aihub#304).

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
  step and on `failed` (rejected there, not ignored). ⚠️ If `pf_update_step` does not publish it
  the binary is older — §1, and that fallback has a trap.
- `step_id` is the scenario `## Step:` name; unvalidated, so a typo is silent.
- `artifact_summary`: status only (no diff / plan / code); it may lead with one structured line —
  `pr=<owner/repo>#<number> base=<branch>` or `Pattern <A|B>:`.
- Long steps: add `heartbeat=true` to an `in_progress` call every ~5 min.

## Ownership

claim / locks / `pf_update_step` / `pf_save_artifact` / commit / push / PR / wrap / CI gating are
**polyforge's**; an engine produces content only. §6 has the superpowers execute boundary (D6).

**`.pf_*` hygiene**: never stage `.pf_meta.json` (the only file the engine writes) — `git
checkout HEAD --` it, or use `pf_commit(paths=[...])`.

## Execute step only: `pf_acquire_locks(work_item_id=<current>)` BEFORE the loop

At the very start of the **execute** step — before the loop, before reading the scenario .md,
before any dispatch. Not for spec or plan. **`acquired`/`already_held`** → proceed.
**`ErrConflictLockTaken`** (payload carries `conflict_with`) → **STOP, do NOT enter the loop**:
report the file, holder and attempt_id three-segment (template in §3), offer `/pf-stop --pause`,
and do **NOT** call `pf_complete_attempt(failed)` — the attempt stays active, waiting.

## Once per wi — 🔴 `Read` the on-demand file §0 before either

`commit_and_pr` → **`pf_ship(...)`**; end of the loop → **`pf_complete_attempt(status="wrapped",
note=...)`**, then each worktree, then their shared `pf.<slug>/` parent ONCE. §0 carries both
argument shapes, the force-push warning and the ordering — do not reconstruct them from memory.

## Three-segment output

Every pf-* response uses `Result` / `Status` / `Next steps`. For `requires_human_session=true`
wi's, take "Next steps" from `using-polyforge/fragments/post-claim-routing.md` — on-demand, so
`Read` it first.
