# _common/references/lifecycle-details.md - deferred lifecycle detail (ON DEMAND)

> **Not injected.** `_common/lifecycle.md` is injected by `hooks/pf-skill-router` into every
> pf-execute step and shares a hard **10,000-character** budget with the memory, engine and
> storage fragments (aihub#304 - past that limit the harness replaces the whole payload with a
> ~2,000-character preview, so growing the resident tier does not add context, it *destroys* it).
>
> Everything here is material you need only when you hit the specific situation it describes.
> `Read` this file then; do not paste it back into the resident fragment.

---

## 0. The two once-per-wi calls

### Ship - at the `commit_and_pr` step

```
pf_ship(work_item_id=<current>, repo=<repo>, workspace_root=<ws>,
        message="<conventional commit message>",
        pr_title="<title>", pr_body="<body>")
```

One call replacing `pf_commit` -> `pf_push` -> `pf_pr`.

- **It pushes to origin, and the push is a FORCE-push** (`--force-with-lease`, exactly what
  `pf_push` does). It refuses `main`/`master`/`dev`/`tot`. The name is short; the effect is not
  local.
- **Failure is reported as JSON, not as an error string.** Read `stage` (`commit`/`push`/`pr`) for
  where it stopped and `side_effects` for what already happened - most often a local commit that
  never left the machine. Do not assume a failed `pf_ship` did nothing.
- **Retrying is safe.** It commits only when something is staged and skips the push when a PR
  already covers HEAD, so a retry after a fixed push failure does not duplicate the commit. An
  open PR on the branch is reused.

Keep using `pf_commit` / `pf_push` / `pf_pr` separately only when you genuinely need to inspect
state between the stages - committing now but deliberately not pushing, or debugging which stage
is broken.

### Wrap & cleanup - at the end of the execute loop

```
result = pf_complete_attempt(work_item_id=<current>, status="wrapped",
                             note="wrapped: <1-sentence summary of what was accomplished>")
worktrees = result.get("worktrees", {})
for repo_name, wt in worktrees.items():
    git -C <workspace_root>/.repo/<repo_name> worktree remove --force <wt>
# all of a wi's worktrees live under one parent (pf.<slug>/); remove it once, AFTER the loop -
# never inside it, or the shared parent vanishes before later repos' removes
if worktrees:
    parent = os.path.dirname(next(iter(worktrees.values())))
    if os.path.isdir(parent): rm -rf <parent>
```

`pf_complete_attempt` returns the state file's `worktrees` map before deleting it, so there is no
need to read the state file yourself first. Pass the closing note as `note=` in the same call:
it is recorded *before* the attempt is completed, which is the only order that works - the
terminal call deletes the state file, so a `pf_emit_event` after it has no credentials to
authenticate with. The response's `note_emitted` says whether it landed; a failed note does not
fail the wrap, so check the field rather than assuming.

Because the note precedes the completion, a terminal call that fails *at the completion* has
already recorded it. **Drop `note=` when retrying**, unless the error says the note was not
recorded either.

---

## 1. Older server binaries: `next_step` on `pf_update_step`

The CLI binary updates on its own channel independently of this plugin, so a session can be
running a NEWER plugin against an OLDER binary. **Look at `pf_update_step`'s published
parameters** - do not guess from the version string:

- **It lists `next_step`** -> use the fused form in `lifecycle.md`.
- **It does not** -> fall back to two calls: `status="completed"` for this step, then a separate
  `status="in_progress"` for the next one - **and pass `step_attempt_id=next_sa` on that second
  call.** Omitting it leaves `current_step_attempt` NULL, which is the one way the two-call form
  is *not* equivalent to the fused one.

**Never pass `next_step` to a tool that does not publish it.** It is silently dropped, and the
next step then never starts - no error, no timeline event, just a wi that looks stalled.

The reverse direction does not fail, but **it is no longer silent**: an older plugin still
passing `expected_version` to a newer binary has it dropped as before - and since aihub#389 the
response says so, in `request_adjusted` with `param: "unknown_params"`. (`pf_update_step` used to
publish an `expected_version` the server never bound; it was dropped on arrival, so the
`pf_get_step` that fetched it bought nothing - aihub#290. That is why `lifecycle.md` tells you not
to make that call.)

**That disclosure applies to every `pf_*` tool, not just this one.** If a response comes back
with a `request_adjusted` entry naming `unknown_params`, you sent a parameter the tool does not
publish: it was not applied - for pf_remember / pf_save_artifact / pf_update_memory it is
stripped at the MCP boundary before the request is built (aihub#586); for every other tool it
either never leaves the process or is sent to the server and dropped at its JSON binding -
whatever the rest of the response says. Read the
names it lists and stop sending them - do not retry the same call. This paragraph used to say the
drop was simply "harmless", and while nothing breaks, a parameter you believe you passed and the
server never saw is not harmless; `expected_version` was 202 such calls in 21 days, each one an
agent believing it had compare-and-set protection it did not have.

**The prohibition is narrow: it is on calling `pf_get_step` *before* `pf_update_step`, not on
`pf_get_step` itself.** Call it whenever you actually need its answer - most often after a
**resume**, to discover which step is current. Nothing else reports that.

## 2. Older server binaries: `note` on the terminal call

If `pf_complete_attempt` / `pf_wrap` do not publish a `note` parameter, the binary predates
aihub#290. Emit the note with a separate call **before** the terminal one, as `pf-stop` describes:

```
pf_emit_event(event_type="note", payload={text: "wrapped: ..."})
pf_complete_attempt(work_item_id=<current>, status="wrapped")
```

Do not pass `note` to a tool that does not publish it: it is accepted and ignored, and the
state file is deleted immediately afterwards, so the reason is lost with no error at all.

## 3. Lock-conflict report template

`pf_acquire_locks` runs once per wi, at the very start of the **execute** step - before the
loop, before reading the scenario .md, before any dispatch; never for spec or plan.
`acquired`/`already_held` means proceed.

On `ErrConflictLockTaken` the payload carries `conflict_with`, which names the file(s), the
holding actor, their work item and their attempt_id. Stop and report exactly this shape:

```
## Result
Cannot execute: file lock conflict.

## Status
| file        | <conflicting file path(s)>         |
| held by     | <actor_display> (<work_item_slug>)  |
| attempt_id  | <attempt_id>                       |

## Next steps
- Coordinate with <actor_display> or wait for their wi (<work_item_slug>) to wrap.
- Use `/pf-stop --pause` to pause your attempt (releasing its `file_scope` locks) while waiting.
- Or use `/pf-status` to check when the blocking wi finishes.
```

Do **not** call `pf_complete_attempt(failed)`. The attempt remains active (paused / waiting), and
failing it would destroy a claim that is legitimately queued behind someone else's work.

## 4. Why `pf_ship` replaces three calls (aihub#286)

`pf_commit` -> `pf_push` -> `pf_pr` costs three MCP round-trips to return three few-hundred-byte
confirmations that **no decision depends on**: `pf_push` never reads the commit sha, `pf_pr` never
reads the push output. A round-trip costs the whole request prefix, not the size of its response.
A 93-day measurement puts the three-call pattern at **1.018% of billed input**.

The same reasoning is why the closing note is folded into the terminal call rather than emitted
beforehand (aihub#290: 201 measured note->terminal pairs, 0.325% of billed input) - and folding it
in removes the ordering hazard described in §2 along with the round-trip.

## 5. Commit hygiene background

Never stage `.pf_meta.json`, the only file the engine writes. `git checkout HEAD --` it, or
name the paths you do want: `pf_commit(paths=[...])`.

The marketplace repo has historically tracked `.pf_meta.json` / `.pf_steps.json`, which is how
step-state scratch files end up staged by accident. (team memory: `mem_IG1CV2pN`)

## 6. Ownership - the execute boundary when superpowers writes the content

Deferred out of the resident fragment by aihub#338: on the native branch there is no superpowers
to bound, and on the superpowers branch `hooks/pf-skill-router` already injects the same boundary
in its engine pointer, so a resident copy was paying for itself twice and reaching the wrong
audience once.

superpowers produces content only, which is **not** a lifecycle bypass - the iron rule is about
skipping claim / step / artifact / wrap, none of which it touches. **Execute boundary (D6)**: let
superpowers run its implementation loop, but **stop before
`superpowers:finishing-a-development-branch`**. Commit, PR, wrap and CI gating come back to
polyforge at that point.
