---
name: pf-execute
description: >
  Use when a claimed wi is ready to be carried out step by step against its scenario
  step graph, e.g. the user says execute, run it, or go.
---

# pf-execute - Wi Agent Main Loop

> **Stub.** The real body of this step is injected at call time by the `PreToolUse(Skill)`
> router (`hooks/pf-skill-router`): with superpowers enabled it tells you to delegate each
> step's implementation to `superpowers:subagent-driven-development` and **stop before finishing-a-development-branch**; without it, it injects this folder's
> `engine.native.md` (the native main loop). In both cases it also injects
> `../_common/{memory,storage,lifecycle}.md` (step reporting; commit/PR/wrap/CI owned by
> polyforge; `.pf_*` hygiene; wrap + cleanup).
>
> **Fallback:** if you are reading this line and did NOT receive an injected step body
> (the router did not fire; Codex / OpenCode / pi load this file directly), read this folder's `engine.native.md` plus
> `../_common/{memory,storage,lifecycle}.md` and follow those, or run `/pf-doctor`. When an
> injected body is present, it takes precedence over this stub.

## DB-aware compatibility (aihub#708 workflow v2): check BEFORE the loop

This skill drives the **legacy scenario step graph**. A wi may instead carry a **pinned DB
workflow generation**, and for those the stored flow, not this loop, is the authority:

```
wf = pf_get_workflow(work_item_id=<current>)
wf.steps_version > 0   -> pinned flow: do NOT run the scenario loop below.
                          Read `using-polyforge/fragments/workflow-v2.md`, then run
                          `polyforge engine workflow --work-item='<current>' --continue`.
                          Follow its prepare/submit/approval instructions exactly; do not
                          call the low-level workflow tools by hand or synthesize approval.
wf.steps_version == 0  -> no workflow: legacy path, this stub's loop applies unchanged.
```

For a pinned DB workflow this skill is a thin entry: read the fragment and invoke the
callable `engine workflow --continue` protocol. `--prepare` returns the exact pinned bundle,
concrete predecessor values and output schema to this main session; `--submit` records the
authored artifact/result. Never replace that with a hand-written start/result loop, and never
turn an approval hold into success.

The legacy path below is unchanged and remains the default for every wi without a pinned
flow.

## Usage

**Purpose**: Run the wi's scenario step graph to completion (the main execution loop).

**Pattern**: `/pf-execute`

**Required**: a currently-claimed wi (state file at `<workspace>/.polyforge/state/<wi_id>.json`).

**Flags**: Mode is derived from the wi's `requires_human_session` (`false` -> auto dispatch per step; `true` -> interactive step-by-step).

## NL Triggers

- "execute" / "run it" / "start executing" / "go"
