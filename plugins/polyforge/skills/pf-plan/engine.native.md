# pf-plan — native engine (planning methodology)

> **Injected by the `PreToolUse(Skill)` router** (`hooks/pf-skill-router`) when the
> `superpowers` plugin is **absent**. When superpowers IS enabled, the router instead
> instructs you to use `superpowers:writing-plans` as the plan engine and this file is
> not injected.
>
> Either way, the lifecycle scaffolding — Memory-First recall, marking the step, saving
> the `methodology.plan` artifact, marking the step completed, three-segment output — is supplied
> by the injected `_common/{memory,storage,lifecycle}.md` fragments in **both** branches.
> This file covers ONLY the "how to write the plan" engine.

## Read the spec

```
pf_recall(work_item_id=<current>, type="methodology.spec", top_k=1)
```

Parse acceptance criteria and design decisions to inform the plan.

## Draft plan steps

Each step must have:
- `id`: snake_case identifier
- `title`: concise description
- `depends_on`: list of step ids (empty for first step)
- `effort_hint`: `xs` / `s` / `m` / `l` / `xl`
- `description`: detail for Step Agent context

## Child wi vs subagent — choose the right parallelism primitive

**This is the most common planning mistake: defaulting to child wi's for all parallel work.**

| Signal | Use **subagent** | Use **child wi** |
|--------|-----------------|-----------------|
| Execution window | Same session | Cross-session or future |
| Ownership | One person drives | Different people may claim |
| Lifecycle | Ephemeral | Independent track/pause/resume |
| Complexity | Mechanical, bounded | Significant, may need its own spec/plan |

**Default to subagent.** Child wi's have lifecycle overhead (claim, state, retro, wrap).
Only escalate to child wi when the work will outlive the current session or needs independent ownership.

For steps that can run in parallel *within this session*, dispatch subagents via the Agent tool — no wi needed.

**When child wi's are appropriate**, use topological order (dependencies first):

```python
step_to_wi = {}
for step in topological_sort(plan.steps):
    child = pf_create_work_item(
        project=<current_project>,
        goal=step.title,
        parent_work_item_id=<current_wi_id>,
        source="auto_execute",
        wi_type=<inferred from step nature>,
        blocked_by=[step_to_wi[dep] for dep in step.depends_on]
    )
    step_to_wi[step.id] = child.id
```

For sequential steps in the same repo, keep them as steps in the parent wi, not child wi's.

## Plan Format (markdown)

```markdown
## Plan: <wi slug> — <goal>

### Steps
1. **prepare_context** (xs) — Load memory, analyze code structure
   - Depends on: —
   - Touched files: (read-only survey, no writes)
2. **implement_core** (l) — Implement the main logic change
   - Depends on: prepare_context
   - Touched files: `repo/path/to/file.go` (write), `repo/path/to/other.go` (write)
3. **write_tests** (m) — Add unit + integration tests
   - Depends on: implement_core
   - Touched files: `repo/path/to/file_test.go` (write)
4. **ship** (xs) — Commit, push, open PR
   - Depends on: write_tests
   - Touched files: (no file edits)

### Child wi's
- wi_aaa: "implement_core" (auto, ready to claim)
- wi_bbb: "write_tests" (auto, blocked by wi_aaa)

### Estimated total: ~1.5 days
```

> **Required**: every step in the plan MUST include a `Touched files:` line listing each file
> the step will modify (write) or only read. Use repo-relative paths (e.g. `cmd/bff/main.go`).
> Steps that make no file changes write `(no file edits)`; read-only steps write `(read-only)`.
> This list is used by the lifecycle layer to derive `declared_resources` for the work item.

## After the plan is written

`_common/storage.md` saves it as a `methodology.plan` artifact (check for a prior plan and
pass `supersedes_memory_id` if one exists); `_common/lifecycle.md` marks the step completed
(status only — never upload plan content there) and renders the three-segment output whose
next-step ("Next steps") section suggests `/pf-execute`.
