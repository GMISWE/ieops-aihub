# pf-execute — native engine (Wi Agent main loop)

> Injected when `superpowers` is absent; bracket / ownership / ship / wrap come from the injected
> `_common/lifecycle.md`. 📄 **`Read @@PLUGIN_ROOT@@/skills/pf-execute/references/engine-native-details.md`
> before step 1** — startup commands, the `@include` rule, the rhs=true loop, older-binary
> fallbacks.

## Startup — 🔴 run the on-demand file §0 first

Six steps: read the wi, resolve `<workspace_root>/.repo/<owner>__<repo>/` (§0 has the guarded
legacy fallback), **pin the scenario
SHA** into `.pf_meta.json`, resolve `{wi_type}.{project}.md` (falling back to `{wi_type}.md`),
scan `^## Step: (\w+)\s*$` in document order, and expand each section's `@include`s at the pinned
sha. **§0 has the exact commands and the `@include`/`level:` pair-parsing rule — `Read` it; the
fallback chain and the pinning are easy to get subtly wrong.** `level:` is review DEPTH, not a
model — §0f says why there is no per-step model override.

Prior-step context = `pf_get_step` → `completed_steps`; nothing writes a worktree step file.

## Execute (rhs=false, auto mode)

```python
default_model = "sonnet"   # EVERY step; there is no per-step override (§0f)

sa_id = new_ulid()
pf_update_step(work_item_id=<current>, step_id=sections[0].step_id, status="in_progress")

for i, (step_id, content) in enumerate(sections):
    expanded = expand_includes(content, sha)

    # subagent prompt: step id + wi id, "call pf_get_step FIRST; every step_id in
    # completed_steps is done", the expanded instructions, and "return your one-line
    # summary in your output; pf_remember anything worth keeping".
    dispatch subagent(model=default_model, prompt=...)

    # long steps: pf_update_step(..., status="in_progress", heartbeat=true) every ~5 min

    if a step called pf_pause_attempt (or a pf_* call is rejected "attempt is paused"):
        break   # stop the loop; no retry, and do NOT call pf_complete_attempt (§0e)

    if step_id.endswith("_review") or step_id in ("review", "code_review", "release_review"):
        result = parse_review_result(subagent_output)
        if result == "FAIL":
            # §0c: pf_update_step(status="failed", step_attempt_id=sa_id,
            # error_type="review_fail") THEN pf_complete_attempt(status="failed", note=...)
            break   # both, in that order; then output the review issues
        elif result == "WARN":
            print the warning and continue

    # complete THIS step and start the next in one call — signature in _common/lifecycle.md
    # ## Bracket every step. Omit both next_* args on the last step.
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

**No per-step model override exists** — §0f has the contract and its gate.

## Execute (rhs=true, interactive mode)

Same bracket and completion call; you present each step to the user instead of dispatching.
📄 **`Read` the on-demand file §1 before running interactively** — `skip` in particular must call
**no** `pf_update_step` at all, and getting that wrong desyncs the server's `current_step`.

At startup report three-segment; §0d has the literal values.
