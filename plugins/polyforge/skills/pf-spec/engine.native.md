# pf-spec — native engine (spec authoring methodology)

> **Injected by the `PreToolUse(Skill)` router** (`hooks/pf-skill-router`) when the
> `superpowers` plugin is **absent**. When superpowers IS enabled, the router instead
> instructs you to use `superpowers:brainstorming` as the spec engine and this file is
> not injected.
>
> Either way, the lifecycle scaffolding around the engine — Memory-First recall, marking
> the step, saving the `methodology.spec` artifact, emitting the note, marking the step
> completed, three-segment output — is supplied by the injected `_common/{memory,storage,lifecycle}.md`
> fragments in **both** branches. This file covers ONLY the "how to write the spec" engine.

## Guide spec definition (interactive or AI-driven)

Engage the user or AI to define:

- **What / Why**: Problem statement, motivation, context
- **Non-goals**: Explicitly what is out of scope
- **Design decisions**: Key trade-offs, chosen approach and alternatives considered
- **Acceptance criteria**: Concrete, testable conditions for "done"

For the **debug variant** (user describes a bug): gather symptoms, reproduce steps,
suspected root cause, impact assessment, and proposed fix approach.

The spec content is the markdown you pass as `content` to `pf_save_artifact` — see the
injected `_common/storage.md` for the save call and its `structured_payload` shape
(`feature` / `decisions` / `acceptance_criteria` / `non_goals`).

## Debug Variant

When triggered for debugging (`/pf-spec --debug` or NL "this bug / analyze why"), the
spec content follows the debug format:

```markdown
## Root Cause Analysis
...

## Reproduction Steps
1. ...

## Impact Assessment
- Severity: <high/medium/low>
- Affected: <systems/users>

## Proposed Fix
...

## Acceptance Criteria
- [ ] Bug is not reproducible with the fix applied
- [ ] Regression test added
```

## After the spec is written

`_common/storage.md` saves it as a `methodology.spec` artifact and emits the note;
`_common/lifecycle.md` marks the step completed and renders the three-segment output whose
next-step ("下一步") section suggests `/pf-plan` to decompose the spec into implementation steps.
