## superpowers ↔ polyforge bridge (appended only when superpowers is enabled)

You have both polyforge and the `superpowers` plugin enabled in this workspace. Use
superpowers' skills **as-is** — do **not** alter or suppress their prompts or gates.

### Design / planning flow

- Run `superpowers:brainstorming` and `superpowers:writing-plans` normally, including
  their `<HARD-GATE>` and their default behavior of writing
  `docs/superpowers/specs/<date>-<topic>-design.md` and
  `docs/superpowers/plans/<date>-<feature>.md`. **Do not block or redirect those writes.**
- A `PostToolUse` bridge hook watches writes under `docs/superpowers/{specs,plans}/**.md`
  and, when a wi is claimed, asks you to mirror that file into aihub via
  `pf_save_artifact` (`type=methodology.spec` for specs, `type=methodology.plan` for
  plans), superseding the prior artifact for the same path on re-edits. The local file
  stays; aihub gets a tracked, annotatable copy. You do not need to pre-empt this — act on
  the hook's instruction when it fires.
- If no wi is claimed when a design doc is written, the hook will tell you to
  `/pf-work` (claim or create) first, then mirror.

### Engine routing for /pf-spec, /pf-plan and /pf-execute

Engine routing is enforced **mechanically by the `PreToolUse(Skill)` router hook**
(`hooks/pf-skill-router`), which fires the moment `/pf-spec`, `/pf-plan`, or `/pf-execute`
is invoked and injects that step's body at the call site (the SKILL.md files are stubs).
With superpowers enabled it points the step at `superpowers:brainstorming` (spec) /
`superpowers:writing-plans` (plan) / `superpowers:subagent-driven-development` +
`executing-plans` (execute — stopping before `finishing-a-development-branch`); without
superpowers it injects each skill's `engine.native.md`. In **both** cases the
`_common/{memory,storage,lifecycle}.md` fragments are injected so the polyforge lifecycle
(recall / save_artifact / step reporting / commit / PR / wrap) always runs. You don't route
engines by hand — act on whatever the router injects. (This session-start note is the distant
backstop; the router is the near, authoritative trigger at the call site.)

### Diagrams in superpowers docs

When a brainstorming/writing-plans doc includes a diagram, author it as a ` ```d2 ` block
(D2 syntax) — see the "Diagrams: author as d2" convention above. The bridge mirrors the doc
into an aihub artifact, and aihub renders ` ```d2 ` blocks to SVG in `/ui`; mermaid/other
syntaxes will not render. This is the one place a superpowers doc should prefer d2 over its
usual default.

> This fragment is conditional. When superpowers is not enabled for the workspace,
> `pf-session-start` does not append it, and none of the above applies.
