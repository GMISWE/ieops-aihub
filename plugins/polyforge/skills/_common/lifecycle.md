# _common/lifecycle.md — step lifecycle & ownership (always injected)

> Injected by `hooks/pf-skill-router` for EVERY pf-spec / pf-plan / pf-execute step, in
> **BOTH** branches. polyforge owns the wi lifecycle end-to-end; superpowers (when present)
> is only the content **engine** — it never owns any of the calls below.

## Bracket every step with status reporting

```
version = pf_get_step(work_item_id=<current>).version
sa_id   = new_ulid()
pf_update_step(work_item_id=<current>, step_id=<step>, status="in_progress",
               expected_version=version)        # returns step_attempt_id
# ... engine runs, artifact saved ...
pf_update_step(work_item_id=<current>, step_id=<step>, status="completed",
               step_attempt_id=sa_id, artifact_summary="<one sentence, status only, <=4096 chars>")
```

- **Use the real step id**: `step_id` = the scenario `## Step:` name (spec / plan / code_change / …).
  The server's `current_step` is a free-form string with no validation.
- **Status only**: `artifact_summary` is a one-sentence summary — **never** upload diff / plan /
  code / full artifact content.
- **heartbeat**: long steps call `pf_update_step(..., status="in_progress", heartbeat=true)`
  every ~5 min (the schema requires step_id/status even though heartbeat ignores them).

## Ownership — these are polyforge's, always

claim / resource locks / `pf_update_step` / `pf_save_artifact` / commit / push / PR / wrap /
CI gating are **polyforge** operations. When superpowers drives the engine it produces
**content only**; you still make every lifecycle call above through polyforge MCP tools.
**Using superpowers as the engine inside a claimed `/pf-*` step is NOT a lifecycle bypass** —
the bypass prohibition (iron rules) is about skipping claim / step / artifact / wrap, not about
which engine authors the content.

## execute boundary (D6)

When superpowers drives an execute step, let it run its implementation loop
(subagent-driven-development / executing-plans / TDD / systematic-debugging / code-review /
verification) but **stop before `superpowers:finishing-a-development-branch`**. Commit,
push, PR, wrap, and CI gating are done by polyforge (the scenario `commit_and_pr` step +
`pf_wrap`), never by the superpowers branch-finishing skill.

## Commit hygiene (.pf_*)

The marketplace repo has historically tracked `.pf_meta.json` / `.pf_steps.json`. Before any
`pf_commit`, make sure step-state scratch files are not staged: write them OUTSIDE the repo
worktree (e.g. the worktree parent), or `git checkout HEAD -- .pf_meta.json .pf_steps.json`,
or pass `pf_commit(paths=[...])` to stage only intended files. (team memory: mem_IG1CV2pN)

## Wrap & cleanup (end of execute loop)

```
# read worktree paths BEFORE wrap (pf_complete_attempt deletes the state file)
state = read_json(<workspace_root>/.polyforge/state/<wi_id>.json)
worktrees = state.get("worktrees", {})
worktree_parent = state.get("worktree_parent")
pf_complete_attempt(work_item_id=<current>, status="wrapped")
for repo_name, wt in worktrees.items():
    git -C <workspace_root>/.repo/<repo_name> worktree remove --force <wt>
if worktree_parent and os.path.isdir(worktree_parent): rm -rf <worktree_parent>
```

## Plan-step only: derive and write declared_resources

> This block runs ONLY at the end of the **plan step**, after the plan artifact has been saved
> (i.e. after `pf_save_artifact` and the note emit described in `_common/storage.md`). It does
> NOT run for spec or execute steps.

Parse the plan's per-step `Touched files:` lines to build a `declared_resources` list:
- Files the step will MODIFY → `{"type": "path", "uri": "file:<repo-relative-path>", "intent": "write"}`
- Files the step will only READ → `{"type": "path", "uri": "file:<repo-relative-path>", "intent": "read"}`
- Steps that say `(no file edits)` or `(read-only)` → skip (no resource entry)

Collect all unique file entries across all steps, then call:

```
pf_update_work_item(
  work_item_id=<current>,
  declared_resources=[
    {"type": "path", "uri": "file:<repo-relative-path>", "intent": "write"},
    ...
  ]
)
```

**IMPORTANT**:
- Do NOT pass `resources_version` — it triggers a 400 error (known aihub issue).
- Deduplicate: if the same path appears as both write and read across steps, emit only the
  `write` entry (write is the stronger intent).
- If the plan has no file changes at all (all steps read-only or no-edit), still call
  `pf_update_work_item` with an empty list to clear any stale resources.
- This is a lifecycle call — it runs in both the superpowers branch and the native branch,
  because `_common/lifecycle.md` is injected by the router for both.

---

## Execute-step only: acquire file locks before entering the loop

> This block runs ONLY at the **very start of the execute step**, BEFORE any implementation
> loop begins (before reading the scenario .md, before any subagent dispatch, before walking
> the user through steps interactively). It does NOT run for spec or plan steps.

```
result = pf_acquire_locks(work_item_id=<current>)
```

**If `pf_acquire_locks` returns a hard conflict** (error `ErrConflictLockTaken`, payload
contains `conflict_with: {attempt_id, actor_display, work_item_slug}`):

- **STOP. Do NOT enter the execute loop.**
- Report to the user:
  ```
  ## Result
  Cannot execute: file lock conflict.

  ## Status
  | file        | <conflicting file path(s)>        |
  | held by     | <actor_display> (<work_item_slug>) |
  | attempt_id  | <attempt_id>                      |

  ## Next steps
  - Coordinate with <actor_display> or wait for their wi (<work_item_slug>) to wrap.
  - Use `/pf-stop --pause` to release your lease while waiting.
  - Or use `/pf-status` to check when the blocking wi finishes.
  ```
- Do NOT call `pf_complete_attempt(failed)` — the attempt remains active (paused / waiting).

**If `pf_acquire_locks` succeeds** (status `acquired` or `already_held`) → proceed into the
execute loop normally (Startup → step graph scan → per-step dispatch).

This gate is a polyforge lifecycle call — it runs in both the superpowers branch and the native
branch, because `_common/lifecycle.md` is injected by the router for both.

---

## Three-segment output

Every pf-* response follows the mandatory three-segment format — its literal section labels are
`Result` / `Status` / `Next steps` (see the session-start `output-format` and `post-claim-routing`
fragments). The next-step ("Next steps") section is mechanically populated from the routing table
for `requires_human_session=true` wi's.
