---
name: pf-plan
description: >
  Use when a claimed wi has a spec and needs an implementation plan broken into ordered
  steps before coding, or the user says plan, break this down, or create subtasks.
---

# pf-plan — Plan & Child Wi Creation

## Usage

**Purpose**: Write the plan artifact for the current wi — OpenSpec requirements + scenarios,
plus ordered implementation steps with declared touched files.

**Pattern**: `/pf-plan`

**Required**: a currently-claimed wi with a completed spec step.

**Flags**: none

## Contract

The plan MUST be written in OpenSpec grammar:

- `## Requirements`
- `### Requirement: <title>` — phrase each with an RFC-2119 keyword (MUST / SHALL / SHOULD)
- `#### Scenario: <title>` — at least one per requirement, in GIVEN/WHEN/THEN form

Additionally, the plan groups the work into ordered implementation steps. Every step MUST
carry a `Touched files: <repo-relative path> (write|read), ...` line, or `Touched files: (no
file edits)` when the step makes no changes — this list drives Step 5 below.

Example:

```markdown
## Requirements

### Requirement: The scheduler MUST defer pods with an unbound PVC
#### Scenario: Pod references an unbound PVC
- GIVEN a pod spec references a PVC that is not yet bound
- WHEN the scheduler evaluates the pod for placement
- THEN scheduling MUST be deferred until the PVC is bound

## Steps

1. **implement_pvc_guard** (m) — add the bound-PVC check to the scheduler predicate
   - Touched files: `pkg/scheduler/predicates.go` (write)
2. **write_tests** (s) — unit tests for the guard
   - Touched files: `pkg/scheduler/predicates_test.go` (write)
3. **ship** (xs) — commit, push, PR
   - Touched files: (no file edits)
```

## Produce with your engine

Polyforge does not pick or drive an authoring engine — use whatever your harness has
installed, or write it by hand. Examples only, no auto-detection:

- `superpowers:writing-plans` (if the superpowers plugin is installed)
- mattpocok's `to-issues`
- OpenSpec's `/opsx:propose`
- by hand, following the Contract above directly

Whichever you use, the output markdown must satisfy the Contract above before you record it.

## Mechanic

### Step 1: Memory-First recall

```
# opt3 P2: REUSE the merged Memory-First recall from the spec phase (it already covered
# methodology.plan|experience.*). Do NOT recall again. Only if running plan standalone
# (no spec-phase recall in this session) run it once:
pf_recall(project=<current>, query=<wi.goal>, type="methodology.spec|methodology.plan|fact.*|rule.*|experience.*", top_k=8)
```

Display results with `effective_strength >= 0.3` (💡 prefix). For any memory the model
judges actually useful, call `pf_activate_memory(id)`.

### Step 2: Mark the step in_progress

```
version = pf_get_step(work_item_id=<current>).version
sa_id    = new_ulid()
pf_update_step(work_item_id=<current>, step_id="plan", status="in_progress",
               expected_version=version)
```

### Step 3: Write the plan

Produce the markdown per the Contract above (read the head `methodology.spec` artifact first
via `pf_recall(project=<current>, work_item_id=<current>, type="methodology.spec", top_k=1)` to ground the
requirements), using the engine you picked in the previous section.

### Step 4: Record the artifact

There is no superpowers-bridge hook watching for this anymore — recording is always this
explicit call, whichever engine wrote the content:

```
pf_save_artifact(
  type="methodology.plan",
  work_item_id=<current>,
  content=<the markdown>,          # OR path="<abs path to the doc>" to read from disk (no inlining)
  structured_payload={"steps": [<step ids>]},
  supersedes_memory_id=<prior plan id, if revising>,
  visibility="project"
)
pf_emit_event(work_item_id=<current>, event_type="note", payload={text: "plan saved: mem_<id>"})
```

Use the literal string `"methodology.plan"` shown above — there is no router to substitute a
placeholder for you.

### Step 5: Derive and write `declared_resources` (plan only)

Parse the plan's per-step `Touched files:` lines into a `declared_resources` list:

- Files a step will MODIFY → `{"type": "path", "uri": "file:<repo-relative-path>", "intent": "write"}`
- Files a step will only READ → `{"type": "path", "uri": "file:<repo-relative-path>", "intent": "read"}`
- Steps marked `(no file edits)` → skip (no resource entry)

Collect unique file entries across all steps — if the same path appears as both write and
read, keep only the `write` entry (write is the stronger intent) — then:

```
pf_update_work_item(
  work_item_id=<current>,
  declared_resources=[
    {"type": "path", "uri": "file:<repo-relative-path>", "intent": "write"},
    ...
  ]
)
```

- `resources_version` is optional. Omit it to overwrite unconditionally (the normal plan-step
  case — you are the only writer). Pass the `resources_version` you last read from the wi when
  another session might be editing the same declaration concurrently: the write then applies
  only if nobody has changed `declared_resources` since, and otherwise fails with
  **409 `CONFLICT_CAS_FAILED`** reporting the current version, instead of silently clobbering
  the other writer. Every successful write of `declared_resources` increments the counter.
  (Before aihub#241 this argument always returned 400 and the counter never advanced, so the
  earlier instruction here was "never pass it" — that is fixed; re-read the version from the
  response rather than assuming it is still 0.)
- If the plan has no file changes at all, still call `pf_update_work_item` with an empty
  list to clear any stale resources.

### Step 6: Mark the step completed

```
pf_update_step(work_item_id=<current>, step_id="plan", status="completed",
               step_attempt_id=sa_id, artifact_summary="<one sentence, status only>")
```

If the run surfaced a pitfall or reusable approach worth remembering across sessions,
capture it (don't over-save):

```
pf_remember(type="experience.*|fact.*|rule.*", project=<current>, content=<finding>,
            work_item_id=<current>, visibility="project")
```

## NL Triggers

- "plan" / "make a plan" / "plan this out"
- "write a plan" / "break this down" / "create subtasks"
- "how should we approach this" / "next steps"
