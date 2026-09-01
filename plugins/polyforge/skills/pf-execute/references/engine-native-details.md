# pf-execute/references/engine-native-details.md — deferred engine detail (ON DEMAND)

> **Not injected.** `engine.native.md` is injected by `hooks/pf-skill-router` and shares a hard
> **10,000-character** budget with the memory, storage and lifecycle fragments (aihub#304 — past
> that limit the harness replaces the whole payload with a ~2,000-character preview, so growing
> the resident tier removes context instead of adding it).
>
> `Read` this file when you are actually running an interactive (`requires_human_session=true`)
> execute loop, or when a tool does not publish a parameter the resident fragment uses.

---

## 0. Startup — the exact commands

1. Read the wi info: `wi_info = pf_list_work_items(ids=[<current_wi_id>])`.
2. Resolve the scenario repo path from `.polyforge.yaml`:
   `scenario_path = <workspace_root>/.repo/<scenario_name>/`.
3. **SHA pinning** — write it to `.pf_meta.json` in the worktree root:
   ```bash
   sha = git -C <scenario_path> rev-parse HEAD
   # <worktree_root>/.pf_meta.json: {"scenario_sha": "<sha>", "started_at": "<ISO8601>"}
   ```
4. **Resolve the .md file** using that pinned SHA, with a fallback chain:
   ```
   1. git show <sha>:{wi_type}.{project}.md   <- project-specific
   2. git show <sha>:{wi_type}.md             <- generic fallback (warn and continue)
   3. neither exists -> pf_complete_attempt(failed), report and list the available .md files, stop
   ```
5. **Scan `## Step:` sections** (document order):
   ```
   scan by the regex ^## Step: (\w+)\s*$ (strict line start, ignore inside code fences)
   produce an ordered step list: [(step_id, content), ...]
   ```
6. **Expand `@include` directives** (pair-parsing rule). `@include:` and `level:` are a bound
   pair: `level:` must be the line immediately after `@include:`, and its scope is only that
   include. Multiple includes each have their own level (or no level).
   ```
   scan the section content line by line:
   - on @include: <path>
       -> read the next line; if it is "level: <value>" record the level, else level=null
       -> expand via git show <sha>:<path> (if missing, call pf_complete_attempt(failed) and stop)
       -> if there is a level, insert a line before the expanded content: "Review level: <value>"
   - any other line: keep as-is
   concatenate the expanded content with the remaining prose
   ```
   **level=opus special case**: pf-execute dispatches that step with `model: opus` (via the Agent
   tool's model parameter).

## 0b. The auto-mode subagent prompt, verbatim

`engine.native.md` summarises this in prose to stay inside the payload budget. The literal
template the loop dispatches:

```
You are executing step {step_id} of wi {wi_id}.

Read .pf_steps.json in the worktree root for prior-step context (if any).

--- step instructions ---
{expanded}
--- END ---

When done, append this step's summary to .pf_steps.json: {"step_id": "one-line summary"}.
If there are learnings worth keeping, call pf_remember to store them in aihub.
```

## 0c. `fail_step_and_attempt` — the review-FAIL path, verbatim

`engine.native.md` abbreviates this call pair. When `parse_review_result` returns `FAIL`:

```python
# next_step is NOT valid on a failure — the loop stops here anyway.
pf_update_step(work_item_id=<current>, step_id=step_id, status="failed",
               step_attempt_id=sa_id, error_type="review_fail")
pf_complete_attempt(work_item_id=<current>, status="failed",
                    note="failed reason: review_fail at step " + step_id)
break   # stop the whole loop, skip the completed report; output the review issues
```

Both calls, in that order. `pf_update_step(failed)` alone leaves the attempt running;
`pf_complete_attempt(failed)` alone leaves the step showing `in_progress` forever.

## 0d. The startup three-segment report, with its literal values

```
## Result
Started executing <slug>, N steps total.

## Status
| wi     | <slug>     |
| steps  | N steps    |
| mode   | auto/human |
| status | running    |

## Next steps
- Running; monitor with /pf-status
- Pause with /pf-stop --pause
```

`mode` is `auto` when `requires_human_session=false` and `human` when it is true.

## 1. Execute (rhs=true, interactive mode) — the loop in full

> ⚠️ **Why this one is deferred, when the auto loop is not.** Interactive mode is a *per-step*
> mechanic, so the resident/on-demand criterion ("resident = every step, on-demand = once per
> wi") does not by itself put it here — the **budget** did. The two loops are near-identical and
> only one can be resident; the auto loop stays because `requires_human_session=false` is the
> common case. `engine.native.md` therefore carries a summary of this loop plus an explicit
> instruction to read this section before running interactively. If the budget ever frees up,
> this is the first thing that should come back.

```python
# Same bracket as auto mode: start the first step, then complete-and-advance. No pf_get_step.
sa_id = new_ulid()
pf_update_step(work_item_id=<current>, step_id=sections[0].step_id, status="in_progress")

for i, (step_id, content) in enumerate(sections):
    expanded = expand_includes(content, sha)

    output: f"## Step {step_id}\n\n{expanded}"

    wait for user input:
      "continue" / "done" / "ok"  -> fall through to the completed report below, then move to the
                                     next step
      "skip"                      -> record in .pf_steps.json; **do NOT report this step**;
                                     continue to the next step WITHOUT calling pf_update_step at
                                     all. The skipped step stays in_progress and stays the
                                     server's current_step; the next step you actually complete
                                     reports itself and advances from there.
      "fail"                      -> pf_update_step(step_id, status="failed", step_attempt_id=sa_id);
                                     pf_complete_attempt(failed, note="failed reason: <user description>");
                                     break (stop the whole loop)

    if step_id.endswith("_review") or step_id in ("review", "code_review", "release_review"):
        "PASS" / "continue"  -> fall through to the completed report below
        "WARN <desc>"        -> record the warning, ask whether to continue; if yes, report
                                completed as usual
        "FAIL <desc>"        -> pf_update_step(step_id, status="failed", step_attempt_id=sa_id,
                                error_type="review_fail");
                                pf_complete_attempt(failed, note="failed reason: <desc>"); break

    # only the "continue/done/ok" path (or review PASS / WARN-continue) reaches here:
    # report this step completed AND start the next one, in one call.
    next_sa = new_ulid() if i + 1 < len(sections) else None
    pf_update_step(work_item_id=<current>, step_id=step_id, status="completed",
                   step_attempt_id=sa_id,
                   artifact_summary=read_json(".pf_steps.json").get(step_id, ""),
                   next_step=sections[i+1].step_id if next_sa else None,
                   next_step_attempt_id=next_sa)
    sa_id = next_sa

# all steps done -> wrap + worktree cleanup (_common/lifecycle.md ## Once per wi, whose full
# sequence is §0 "Wrap & cleanup" of _common/references/lifecycle-details.md)
```

## 2. Compatibility — server binary older than aihub#290

Applies to BOTH loops. Check what the tools publish; do not infer it from a version string.

- **No `next_step` on `pf_update_step`** → drop the two `next_*` arguments and start each step
  with its own `pf_update_step(..., status="in_progress", step_attempt_id=...)` at the top of the
  loop, as before. Passing `next_step` anyway means it is silently dropped and the next step
  never starts.
- **No `note` on `pf_complete_attempt`** → emit
  `pf_emit_event(event_type="note", payload={text: "failed reason: ..."})` **before** the
  terminal call. Do not pass `note` to a tool that does not publish it: it is accepted and
  ignored, and the state file is deleted immediately afterwards, so the failure reason is lost
  with no error.

See `_common/references/lifecycle-details.md` for the same rules stated from the lifecycle side,
including the `step_attempt_id` trap in the two-call fallback.

## 3. Why `pf_list_work_items(ids=[...])` omits `project`

Since aihub#280 `ids` is a real filter and an id already names exactly one wi. Before that, `ids`
reached no forwarding table and no `project` was sent, so this call was a hard 400 and never ran
— which is why older copies of this engine passed `project` defensively.
