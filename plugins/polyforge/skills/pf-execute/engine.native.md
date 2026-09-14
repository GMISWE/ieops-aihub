# pf-execute - native engine (Wi Agent main loop)

> Injected when `superpowers` is absent; bracket / ship / wrap come from `_common/lifecycle.md`.
> **`Read @@PLUGIN_ROOT@@/skills/pf-execute/references/engine-native-details.md` before step 1**
> - §0h's `polyforge engine` verbs, the rhs=true loop, older-binary fallbacks.

## Startup - one command, not six steps

`polyforge engine startup --workspace-root=<ws> --worktree-root=<wt> --wi-type=<wi_type>
--scenario-url=<project.scenario> [--project=<name>]` IS §0 - clone at
`<workspace_root>/.repo/<owner>__<repo>/`, SHA pinned into `.pf_meta.json`, template resolved,
sections scanned, `@include`s expanded at that sha - and prints
`{scenario_path, legacy_fallback, sha, template_source, steps:[{id, content, expanded}]}`.
Do not hand-run those steps. `Read` §0 when it errors.

Prior-step context = `pf_get_step` -> `completed_steps`; nothing writes a worktree step file.

## Execute (rhs=false, auto mode)

```python
# The tier is a STEP KIND -> AGENT choice (aihub#338/#555); the models live ONLY in the agent
# files (agents/*.md), so the loop picks WHICH agent and never passes a model - an explicit one
# silently OVERRIDES the file. `polyforge engine resolve-role --step-id=<sid>` IS the predicate:
# is_review(sid) = sid.endswith("_review") or sid in ("review","code_review","release_review").
STEP_AGENT, REVIEW_AGENT = "polyforge:step-executor", "polyforge:step-reviewer"

sa_id = new_ulid()
pf_update_step(work_item_id=<current>, step_id=steps[0].id, status="in_progress")

for i, (step_id, expanded) in enumerate(steps):   # steps[] as `engine startup` printed them
    dispatch Agent(subagent_type=REVIEW_AGENT if is_review(step_id) else STEP_AGENT, prompt=§0b)
    # ^ copy §0b's template VERBATIM; subagent_type is the only channel that reaches a model.

    if a step called pf_pause_attempt (or a pf_* call is rejected "attempt is paused"):
        break   # stop the loop; no retry, and do NOT call pf_complete_attempt (§0e)

    if is_review(step_id):
        # parse_review_result = `polyforge engine parse-review`, subagent output on STDIN
        result = that verb's {"result": "PASS"|"WARN"|"FAIL"}
        if result == "FAIL":  do §0c, then break   # then output the review issues
        if result == "WARN":  print the warning and continue

    # Complete this step and start the next: mint next_sa = new_ulid() (None on the last step),
    # run `polyforge engine bracket-plan` (§0h: its flags, and QUOTE every value - an unquoted
    # summary is truncated at the first space, exit 0), make every pf_update_step call it prints
    # in that order, then sa_id = next_sa. It owns the fused-vs-two-call choice and the threading.
    next_sa = new_ulid() if i + 1 < len(steps) else None

# all steps done -> wrap + cleanup (_common/lifecycle.md ## Once per wi)
```

**The model tier is keyed on the step's KIND, never on `level:`** - that is review DEPTH, a
different parameter sharing the key name. §0f has the contract, the mapping and its gate.

## Execute (rhs=true, interactive mode)

Same bracket and completion call; you present each step instead of dispatching. **`Read` §1
first** - `skip` **completes** the step (§1b), and §0d has the startup three-segment values.
