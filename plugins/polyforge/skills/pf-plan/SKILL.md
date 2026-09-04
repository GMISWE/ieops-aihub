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
- mattpocock's `to-tickets`
- OpenSpec's `/opsx:propose`
- by hand, following the Contract above directly

Whichever you use, the output markdown must satisfy the Contract above before you record it.

## Mechanic

### Step 1: Memory-First recall

```
# opt3 P2: REUSE the merged Memory-First recall from the spec phase (it already covered
# methodology.plan and experience.*). Do NOT recall again. Only if running plan standalone
# (no spec-phase recall in this session) run it once.
# `type` is an ARRAY, one entry per type. A "a|b|c" string is NOT split — it arrives as
# one type name, matches nothing, and returns an empty set that reads like "no history".
pf_recall(project=<current>, query=<wi.goal>, type=["methodology.spec","methodology.plan","fact.*","rule.*","experience.*"], top_k=8, fields="brief")
```

Display results with `effective_strength >= 0.3` (💡 prefix). For any memory the model
judges actually useful, call `pf_activate_memory(id)`.

`fields="brief"` (aihub#313): display-and-activate only. Note this is deliberately NOT the
same as the Step 3 spec read below, which passes no `fields` because it exists precisely to
consume the spec's text.

### Step 2: Mark the step in_progress

```
sa_id = new_ulid()
pf_update_step(work_item_id=<current>, step_id="plan", status="in_progress")
```

No `pf_get_step` first: the bracket needs no version number, and the `expected_version` it
used to fetch was never bound server-side — the call bought a value that was discarded on
arrival (aihub#290).

### Step 3: Write the plan

Produce the markdown per the Contract above (read the head `methodology.spec` artifact first
via `pf_recall(project=<current>, work_item_id=<current>, type="methodology.spec", top_k=1)` to ground the
requirements — **no `fields="brief"` here**, aihub#313: this call exists to consume the spec's
text, and a first line cannot ground a plan), using the engine you picked in the previous section.

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
read, keep only the `write` entry (write is the stronger intent) — then read the wi for its
current `resources_version` and write the list back:

```
# `resources_version` is a top-level integer field of the response.
# brief=true only drops `content`; every other field, this one included, is still there.
wi = pf_get_work_item(work_item_id=<current>, brief=true)

pf_update_work_item(
  work_item_id=<current>,
  declared_resources=[
    {"type": "path", "uri": "file:<repo-relative-path>", "intent": "write"},
    ...
  ],
  resources_version=<wi.resources_version>
)
```

- **Always send `resources_version`.** Read it here, immediately before the write: the guarded
  window is exactly that read-to-write gap, so a value fetched earlier in the session only
  widens it for no benefit. It is never the wrong thing to send. The update then applies only
  if nobody has changed `declared_resources` since you read it; otherwise it fails with
  **409 `CONFLICT_CAS_FAILED`** carrying `details.current_resources_version` and leaves the
  stored list untouched — re-read the wi, re-derive from the plan, retry. Every successful
  write of `declared_resources` increments the counter, so the number is a token for "the
  list I read", not a timestamp.
- **Omitting it overwrites unconditionally**, and that is safe only when nothing wrote
  `declared_resources` between your read and your write — which you cannot establish
  afterwards, because both outcomes return 200. Do not assume the plan step is the only
  writer: any session with project access can call `pf_update_work_item`, `declared_resources`
  is a whole-list REPLACE, and file_scope locks are derived from whatever is stored at claim
  time and re-derived by `pf_acquire_locks` mid-attempt. So a clobber silently drops the other
  writer's paths *and* the locks protecting them. "I am the only writer" is precisely the
  assumption aihub#260 disproved on the identical shape in `pf_update_project`'s `members`.
  (Before aihub#241 passing this argument always returned 400 and the counter never advanced,
  so the instruction here used to be "never pass it" — that is fixed; read the new version off
  the update response rather than assuming it is still 0.)
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
pf_remember(type=<ONE concrete type — e.g. experience.pitfall / fact.architecture / rule.work>,
            project=<current>, content=<finding>,
            work_item_id=<current>, visibility="project")
```

## NL Triggers

- "plan" / "make a plan" / "plan this out"
- "write a plan" / "break this down" / "create subtasks"
- "how should we approach this" / "next steps"
