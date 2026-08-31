# _common/lifecycle.md — step lifecycle & ownership (injected for pf-execute)

> Injected by `hooks/pf-skill-router` for every pf-execute step. polyforge owns the wi
> lifecycle end-to-end; superpowers (when present) is only the content **engine** — it never
> owns any of the calls below. (pf-spec and pf-plan inline their own step-bracket calls —
> including, for pf-plan, the `declared_resources` derivation that used to live here — see
> their SKILL.md; they no longer depend on this fragment or router injection.)

## Bracket every step with status reporting

**Do not call `pf_get_step` before `pf_update_step`.** There is nothing to read: the step
bracket needs no version number, and the server's concurrency guard is its own
`current_step_status = 'idle'` predicate. (`pf_update_step` used to publish an
`expected_version` the server never bound — it was dropped on arrival, so the `pf_get_step`
that fetched it bought nothing. aihub#290.) Call `pf_get_step` only when you actually need
its answer, e.g. to discover which step is current after a resume.

Start the FIRST step, then complete-and-advance through the rest:

```
sa_id = new_ulid()
pf_update_step(work_item_id=<current>, step_id=<first step>, status="in_progress")
# ... engine runs, artifact saved ...
next_sa = new_ulid()
pf_update_step(work_item_id=<current>, step_id=<this step>, status="completed",
               step_attempt_id=sa_id,
               artifact_summary="<one sentence, status only, <=4096 chars>",
               next_step=<next step>, next_step_attempt_id=next_sa)   # starts the next one
sa_id = next_sa
```

`next_step` completes one step and starts its successor in a single call — the two
transitions share one server transaction and emit both timeline events, so it is
indistinguishable from the two calls it replaces. Omit `next_step` on the LAST step (there is
no successor to start), and on a `failed` status (it is rejected there, not ignored).

> **Compatibility — check the tool schema first.** `next_step` needs a server binary from
> aihub#290 or later, and the CLI binary updates on its own channel independently of this
> plugin, so a session can be running a NEWER plugin against an OLDER binary. Look at
> `pf_update_step`'s published parameters:
> - it lists `next_step` → use the fused form above;
> - it does **not** → fall back to two calls: `status="completed"` for this step, then a
>   separate `status="in_progress"` for the next one — and pass `step_attempt_id=next_sa` on
>   that second call. Omitting it leaves `current_step_attempt` NULL, which is the one way
>   the two-call form is *not* equivalent to the fused one. Never pass `next_step` to a tool
>   that does not publish it: it would be silently dropped and the next step would never start.
>
> The same rule applies in reverse for `expected_version`: an older plugin still passing it to
> a NEWER binary is harmless — the parameter is ignored, exactly as it always effectively was.

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
(subagent-driven-development / TDD / systematic-debugging / code-review /
verification) but **stop before `superpowers:finishing-a-development-branch`**. Commit,
push, PR, wrap, and CI gating are done by polyforge (the scenario `commit_and_pr` step +
`pf_wrap`), never by the superpowers branch-finishing skill.

## Commit + push + PR: one call, not three (aihub#286)

Use **`pf_ship`**, not `pf_commit` → `pf_push` → `pf_pr`:

```
pf_ship(work_item_id=<current>, repo=<repo>, workspace_root=<ws>,
        message="<conventional commit message>",
        pr_title="<title>", pr_body="<body>")
```

The three separate tools cost three MCP round-trips to return three few-hundred-byte
confirmations that **no decision depends on** — `pf_push` never reads the commit sha,
`pf_pr` never reads the push output. A round-trip costs the whole request prefix, not the
size of its response; 93-day measurement puts the three-call pattern at **1.018% of billed
input**.

Three things to know before using it:

- ⚠️ **It pushes to origin, and the push is a FORCE-push** (`--force-with-lease`, exactly
  what `pf_push` does). It refuses `main`/`master`/`dev`/`tot`. The name is short; the
  effect is not local.
- **Failure is reported as JSON, not as an error string.** Read `stage` (`commit` / `push` /
  `pr`) for where it stopped and `side_effects` for what already happened — most often a
  local commit that never left the machine. Do not assume a failed `pf_ship` did nothing.
- **Retrying is safe.** It commits only when something is staged and skips the push when a
  PR already covers HEAD, so a retry after a fixed push failure does not duplicate the
  commit. If an open PR already exists on the branch it is pushed to and reused.

Keep using `pf_commit` / `pf_push` / `pf_pr` separately only when you genuinely need to
inspect state between the steps — e.g. committing now but deliberately not pushing yet, or
debugging which stage is broken.

## Commit hygiene (.pf_*)

The marketplace repo has historically tracked `.pf_meta.json` / `.pf_steps.json`. Before any
`pf_commit`, make sure step-state scratch files are not staged: write them OUTSIDE the repo
worktree (e.g. the worktree parent), or `git checkout HEAD -- .pf_meta.json .pf_steps.json`,
or pass `pf_commit(paths=[...])` to stage only intended files. (team memory: mem_IG1CV2pN)

## Wrap & cleanup (end of execute loop)

`pf_complete_attempt` returns the state file's `worktrees` map before deleting it, so
there is no need to read the state file yourself first. Pass the closing note as `note=` in
the same call rather than emitting it beforehand with `pf_emit_event`:

```
result = pf_complete_attempt(work_item_id=<current>, status="wrapped",
                             note="wrapped: <1-sentence summary of what was accomplished>")
worktrees = result.get("worktrees", {})
for repo_name, wt in worktrees.items():
    git -C <workspace_root>/.repo/<repo_name> worktree remove --force <wt>
# all of a wi's worktrees live under one parent (pf.<slug>/); remove it once, after
# the loop — never inside it, or the shared parent vanishes before later repos' removes
if worktrees:
    parent = os.path.dirname(next(iter(worktrees.values())))
    if os.path.isdir(parent): rm -rf <parent>
```

The `note` is recorded before the attempt is completed, which is the only order that works —
the terminal call deletes the state file, so a `pf_emit_event` after it has no credentials to
authenticate with. Folding it in removes that ordering hazard along with the round-trip
(aihub#290: 201 measured note→terminal pairs, 0.325% of billed input). The response's
`note_emitted` says whether it landed; a failed note does **not** fail the wrap, so check the
field rather than assuming.

⚠️ Because the note precedes the completion, a terminal call that fails *at the completion*
has already recorded it. **Drop `note=` when retrying**, unless the error says the note was
not recorded either.

> **Compatibility**: if `pf_complete_attempt` / `pf_wrap` do not publish a `note` parameter,
> the binary predates aihub#290 — emit the note with a separate `pf_emit_event(event_type="note",
> payload={text: ...})` **before** the terminal call, as `pf-stop` describes.

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
`Result` / `Status` / `Next steps`. The labels and the format come from the `output-format`
fragment, which IS injected at session start, so it is already in your context.

The next-step ("Next steps") section is mechanically populated from the Post-claim routing table
for `requires_human_session=true` wi's. That table lives in `fragments/post-claim-routing.md`
under `using-polyforge`, and since aihub#285 it is **on-demand, not session-start** — it is NOT in
your context. `Read` it before filling in that segment.
