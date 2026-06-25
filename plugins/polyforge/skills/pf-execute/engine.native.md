# pf-execute — native engine (Wi Agent main loop)

> **Injected by the `PreToolUse(Skill)` router** (`hooks/pf-skill-router`) when the
> `superpowers` plugin is **absent**. When superpowers IS enabled, the router instead
> instructs you to delegate the **per-step implementation** to
> `superpowers:subagent-driven-development` / `superpowers:executing-plans` (TDD,
> systematic-debugging, code-review, verification), while **this loop's scenario-step
> iteration, step-status reporting, and wrap still run** — and you **stop before
> `superpowers:finishing-a-development-branch`** (commit/PR/wrap/CI belong to polyforge —
> see `_common/lifecycle.md`).
>
> In BOTH branches the lifecycle scaffolding (step-status reporting protocol, ownership,
> `.pf_*` hygiene, wrap) comes from the injected `_common/lifecycle.md`. This file is the
> native execution engine: read the scenario step graph and drive each step.

## Startup

1. Read the wi info:
   ```
   wi_info = pf_list_work_items(ids=[<current_wi_id>])
   ```

2. Resolve the scenario repo path (from .polyforge.yaml):
   ```
   scenario_path = <workspace_root>/.repo/<scenario_name>/
   ```

3. **SHA pinning**: read the scenario repo's current HEAD SHA and write it to `.pf_meta.json`
   in the worktree root:
   ```bash
   sha = git -C <scenario_path> rev-parse HEAD
   # write <worktree_root>/.pf_meta.json: {"scenario_sha": "<sha>", "started_at": "<ISO8601>"}
   ```

4. **Resolve the .md file** (using the pinned SHA, with a fallback chain):
   ```
   1. git show <sha>:{wi_type}.{project}.md   <- project-specific
   2. git show <sha>:{wi_type}.md             <- generic fallback (warn and continue)
   3. neither exists -> call pf_complete_attempt(failed), report and list the available .md files, stop
   ```

5. **Scan `## Step:` sections** (document order):
   ```
   scan by the regex ^## Step: (\w+)\s*$ (strict line start, ignore inside code fences)
   produce an ordered step list: [(step_id, content), ...]
   ```

6. **Expand `@include` directives** (pair-parsing rule):

   `@include:` and `level:` are a bound pair: `level:` must be the line immediately after
   `@include:`, and its scope is only that include. Multiple includes each have their own
   level (or no level).

   ```
   scan the section content line by line:
   - on @include: <path>
       -> read the next line; if it is "level: <value>" record the level, else level=null
       -> expand via git show <sha>:<path> (if missing, call pf_complete_attempt(failed) and stop)
       -> if there is a level, insert a line before the expanded content: "Review level: <value>"
   - any other line: keep as-is
   concatenate the expanded content with the remaining prose
   ```

   **level=opus special case**: pf-execute dispatches that step with `model: opus` (via the
   Agent tool's model parameter).

---

## `.pf_steps.json` convention

Stored in the worktree root, a flat dict, last-write-wins per key:
```json
{"prepare_context": "analyzed 3 files", "code_change": "edited publish.go"}
```
Each step appends/updates its key on completion. The next step reads the whole thing as context.

> Step-status reporting (the `pf_update_step` calls bracketing each step, heartbeat,
> `step_attempt_id`) is the **lifecycle** concern documented in `_common/lifecycle.md` —
> the loops below reference it. The mode is selected from the wi's `requires_human_session`
> (`false` -> auto dispatch, `true` -> interactive).

---

## Execute (rhs=false, auto mode)

```python
default_model = "sonnet"   # S1: superpowers-off consistency; per-step `level: opus` still overrides
for step_id, content in sections:
    expanded = expand_includes(content, sha)
    model = "opus" if step_level(content) == "opus" else default_model

    # report: entering this step (see _common/lifecycle.md ## Bracket every step)
    version = pf_get_step(work_item_id=<current>).version
    sa_id   = new_ulid()                       # client-generated, for the completion history row
    pf_update_step(work_item_id=<current>, step_id=step_id,
                   status="in_progress", expected_version=version)

    dispatch subagent(
        model=model,
        prompt=f"""
You are executing step {step_id} of wi {wi_id}.

Read .pf_steps.json in the worktree root for prior-step context (if any).

--- step instructions ---
{expanded}
--- END ---

When done, append this step's summary to .pf_steps.json: {{"step_id": "one-line summary"}}.
If there are learnings worth keeping, call pf_remember to store them in aihub.
""")

    # during long steps: pf_update_step(..., status="in_progress", heartbeat=true) every ~5 min

    # review step: check the REVIEW_RESULT marker
    if step_id.endswith("_review") or step_id in ("review", "code_review", "release_review"):
        result = parse_review_result(subagent_output)
        if result == "FAIL":
            pf_update_step(work_item_id=<current>, step_id=step_id, status="failed",
                           step_attempt_id=sa_id, error_type="review_fail")
            pf_complete_attempt(work_item_id=<current>, status="failed")
            break   # stop the whole loop, skip the completed report below; output the review issues
        elif result == "WARN":
            print the warning and continue

    # report: this step completed (status only; reuse the summary from .pf_steps.json)
    pf_update_step(work_item_id=<current>, step_id=step_id, status="completed",
                   step_attempt_id=sa_id,
                   artifact_summary=read_json(".pf_steps.json").get(step_id, ""))

# all steps done -> wrap + worktree cleanup (see _common/lifecycle.md ## Wrap & cleanup)
```

---

## Execute (rhs=true, interactive mode)

```python
for step_id, content in sections:
    expanded = expand_includes(content, sha)

    # report: entering this step
    version = pf_get_step(work_item_id=<current>).version
    sa_id   = new_ulid()
    pf_update_step(work_item_id=<current>, step_id=step_id,
                   status="in_progress", expected_version=version)

    output: f"## Step {step_id}\n\n{expanded}"

    wait for user input:
      "continue" / "done" / "ok"  -> fall through to the completed report below, then move to the next step
      "skip"                      -> record in .pf_steps.json; **do NOT report this step**; continue to the next step
      "fail"                      -> pf_update_step(step_id, status="failed", step_attempt_id=sa_id);
                                     pf_complete_attempt(failed); break (stop the whole loop)

    if step_id.endswith("_review") or step_id in ("review", "code_review", "release_review"):
        "PASS" / "continue"  -> fall through to the completed report below
        "WARN <desc>"        -> record the warning, ask whether to continue; if yes, report completed as usual
        "FAIL <desc>"        -> pf_update_step(step_id, status="failed", step_attempt_id=sa_id,
                                error_type="review_fail"); pf_complete_attempt(failed); break

    # only the "continue/done/ok" path (or review PASS / WARN-continue) reaches here: report this step completed (status only).
    pf_update_step(work_item_id=<current>, step_id=step_id, status="completed",
                   step_attempt_id=sa_id,
                   artifact_summary=read_json(".pf_steps.json").get(step_id, ""))

# all steps done -> wrap + worktree cleanup (see _common/lifecycle.md ## Wrap & cleanup)
```

---

## parse_review_result

```python
def parse_review_result(output):
    import re
    matches = re.findall(r'<!-- REVIEW_RESULT: (PASS|WARN|FAIL) -->', output)
    if not matches:
        # no marker = the subagent did not output the required format; treat as WARN (visible issue, not auto-fail)
        print("⚠️ review step did not output a REVIEW_RESULT marker; treating as WARN")
        return "WARN"
    return matches[-1]
```

---

## Output three-segment format (at startup)

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
