# pf-execute — native engine (Wi Agent main loop)

> Injected when `superpowers` is absent; bracket / ship / wrap come from `_common/lifecycle.md`.
> 📄 **`Read @@PLUGIN_ROOT@@/skills/pf-execute/references/engine-native-details.md` before step 1**
> — startup commands, the `@include` rule, the rhs=true loop, older-binary fallbacks.

## Startup — 🔴 run the on-demand file §0 first

§0 carries all six startup steps verbatim: the scenario clone at
`<workspace_root>/.repo/<owner>__<repo>/`, **pinning its SHA** into `.pf_meta.json`, template
resolution, section scan, `@include` expansion at that sha. `Read` it — the fallback chain, the
pinning and the `@include`/`level:` pair rule are each easy to get subtly wrong.

Prior-step context = `pf_get_step` → `completed_steps`; nothing writes a worktree step file.

## Execute (rhs=false, auto mode)

```python
# Model tier by STEP KIND (aihub#338): review steps dispatch on the raised tier, every other
# step on the default. Keyed on the step id, never on `level:` — that is review DEPTH, a
# different parameter that happens to share the key name (§0f has the mapping and its gate).
DEFAULT_TIER, RAISED_TIER = "sonnet", "opus"
def is_review(sid):
    return sid.endswith("_review") or sid in ("review", "code_review", "release_review")

sa_id = new_ulid()
pf_update_step(work_item_id=<current>, step_id=sections[0].step_id, status="in_progress")

for i, (step_id, content) in enumerate(sections):
    expanded = expand_includes(content, sha)

    # subagent prompt, verbatim in §0b: pf_get_step FIRST (completed_steps is what is
    # already done), then the expanded instructions, then "return one summary line".
    dispatch subagent(model=RAISED_TIER if is_review(step_id) else DEFAULT_TIER, prompt=...)

    # long steps: pf_update_step(..., status="in_progress", heartbeat=true) every ~5 min

    if a step called pf_pause_attempt (or a pf_* call is rejected "attempt is paused"):
        break   # stop the loop; no retry, and do NOT call pf_complete_attempt (§0e)

    if is_review(step_id):
        result = parse_review_result(subagent_output)
        if result == "FAIL":
            # §0c: pf_update_step(status="failed", step_attempt_id=sa_id,
            # error_type="review_fail") THEN pf_complete_attempt(status="failed", note=...)
            break   # both, in that order; then output the review issues
        elif result == "WARN":
            print the warning and continue

    # complete this step and start the next in ONE call (_common/lifecycle.md ## Bracket
    # every step). Omit both next_* args on the last step.
    next_sa = new_ulid() if i + 1 < len(sections) else None
    pf_update_step(..., step_id=step_id, status="completed", step_attempt_id=sa_id,
                   artifact_summary=<the subagent's returned summary line>,
                   next_step=sections[i+1].step_id if next_sa else None,
                   next_step_attempt_id=next_sa)
    sa_id = next_sa

# all steps done -> wrap + cleanup (_common/lifecycle.md ## Once per wi)
```

**`parse_review_result(output)`** = the LAST `<!-- REVIEW_RESULT: (PASS|WARN|FAIL) -->` match;
no marker → warn it is missing and return `WARN`, never an auto-fail.

**The model tier is keyed on the step's KIND, never on `level:`** — §0f has the contract, the
mapping and its gate.

## Execute (rhs=true, interactive mode)

Same bracket and completion call; you present each step instead of dispatching. 📄 **`Read` §1
first** — `skip` **completes** the step (§1b), and §0d has the startup three-segment values.
