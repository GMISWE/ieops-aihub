---
name: pf-spec
description: >
  Use when a claimed feature or bug wi needs its scope, non-goals, design, and
  acceptance criteria defined, or when a bug needs root-cause analysis, e.g. write a
  spec, scope this out, debug, or root cause.
---

# pf-spec — Spec & Debug Analysis

## Usage

**Purpose**: Write the spec artifact for the current wi — OpenSpec requirements + scenarios
— or run root-cause analysis for a bug in the same grammar.

**Pattern**: `/pf-spec`

**Required**: a currently-claimed `feature` / `critical_bug` wi.

**Flags**: none

## Contract

The spec MUST be written in OpenSpec grammar:

- `## Requirements`
- `### Requirement: <title>` — phrase each with an RFC-2119 keyword (MUST / SHALL / SHOULD)
- `#### Scenario: <title>` — at least one per requirement, in GIVEN/WHEN/THEN form

For a debug / root-cause spec, phrase the broken behavior as a Requirement being violated
(e.g. "The scheduler MUST NOT double-count draining nodes") with a Scenario that reproduces
it, plus a Requirement capturing the fix.

Example:

```markdown
## Requirements

### Requirement: The API MUST reject payloads missing the `name` field
#### Scenario: Missing required field
- GIVEN a request payload without a `name` field
- WHEN the endpoint receives the request
- THEN the API MUST respond 400 with a field-level error
```

## Produce with your engine

Polyforge does not pick or drive an authoring engine — use whatever your harness has
installed, or write it by hand. Examples only, no auto-detection:

- `superpowers:brainstorming` (if the superpowers plugin is installed)
- mattpocok's `grill-with-docs` + `to-prd`
- OpenSpec's `/opsx:propose`
- by hand, following the Contract above directly

Whichever you use, the output markdown must satisfy the Contract above before you record it.

## Mechanic

### Step 1: Memory-First recall

```
# opt3 P2: merged Memory-First recall — covers BOTH spec and plan phases; the plan phase
# reuses these results (no second recall). See pf-plan Step 1.
pf_recall(project=<current>, query=<wi.goal>, type="methodology.spec|methodology.plan|fact.*|rule.*|experience.*", top_k=8)
```

Display results with `effective_strength >= 0.3` (💡 prefix). For any memory the model
judges actually useful, call `pf_activate_memory(id)`.

### Step 2: Mark the step in_progress

```
sa_id = new_ulid()
pf_update_step(work_item_id=<current>, step_id="spec", status="in_progress")
```

No `pf_get_step` first: the bracket needs no version number, and the `expected_version` it
used to fetch was never bound server-side — the call bought a value that was discarded on
arrival (aihub#290).

### Step 3: Write the spec

Produce the markdown per the Contract above, using the engine you picked in the previous
section.

### Step 4: Record the artifact

There is no superpowers-bridge hook watching for this anymore — recording is always this
explicit call, whichever engine wrote the content:

```
pf_save_artifact(
  type="methodology.spec",
  work_item_id=<current>,
  content=<the markdown>,          # OR path="<abs path to the doc>" to read from disk (no inlining)
  structured_payload={"requirements": [<requirement titles>]},
  supersedes_memory_id=<prior spec id, if revising>,
  visibility="project"
)
pf_emit_event(work_item_id=<current>, event_type="note", payload={text: "spec saved: mem_<id>"})
```

Use the literal string `"methodology.spec"` shown above — there is no router to substitute a
placeholder for you.

### Step 5: Mark the step completed

Save the artifact (Step 4) before marking the step completed — never the reverse.

```
pf_update_step(work_item_id=<current>, step_id="spec", status="completed",
               step_attempt_id=sa_id, artifact_summary="<one sentence, status only>")
```

If the run surfaced a pitfall or reusable approach worth remembering across sessions,
capture it (don't over-save):

```
pf_remember(type="experience.*|fact.*|rule.*", project=<current>, content=<finding>,
            work_item_id=<current>, visibility="project")
```

## NL Triggers

- "design" / "spec" / "brainstorm" / "discuss the approach"
- "write a spec" / "define requirements" / "scope this out"
- "debug" / "what's going on with this bug" / "analyze this issue"
- "root cause" / "why is this failing"
