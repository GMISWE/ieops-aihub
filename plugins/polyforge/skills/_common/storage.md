# _common/storage.md — artifact storage (always injected)

> Injected by `hooks/pf-skill-router` for EVERY pf-spec / pf-plan / pf-execute step, in
> **BOTH** branches. Persisting the engine's output into aihub is a polyforge lifecycle
> concern — it happens whether the content was authored by superpowers or the native engine.

**Artifact type for this step**: `@@ARTIFACT_TYPE@@`

## spec / plan steps — save the artifact

```
pf_save_artifact(
  type="@@ARTIFACT_TYPE@@",
  work_item_id=<current>,
  content=<the markdown the engine produced>,
  structured_payload={ ... },   # spec: feature / decisions / acceptance_criteria / non_goals
                                 # plan: steps[]
  supersedes_memory_id=<prior artifact id, if revising>,
  visibility="project"
)
```

Then emit the note:
```
pf_emit_event(work_item_id=<current>, event_type="note",
              payload={text: "<spec|plan> saved: mem_<id>"})
```

> **Plan step only — touched files requirement**: the plan content saved above MUST include,
> for every step, a `Touched files:` line listing files that step will modify (write) or only
> read, using repo-relative paths. Example: `Touched files: cmd/bff/main.go (write), pkg/api/types.go (read)`.
> Steps with no file changes write `(no file edits)`. This list is consumed by the plan-step
> lifecycle block in `_common/lifecycle.md` to derive `declared_resources`. When using
> `superpowers:writing-plans` as the engine, instruct it to include this `Touched files:` line
> per step before it writes its output file.

> **superpowers branch**: the `pf-superpowers-bridge` PostToolUse hook saves the doc into aihub
> **deterministically** (it reads the wi state + config and POSTs directly). You do NOT need to call
> `pf_save_artifact` yourself; only do so if the bridge's output reports it could not save (missing
> creds / network), in which case follow the bridge's fallback instruction.

## execute steps

`@@ARTIFACT_TYPE@@` is empty for execute — there is no per-step spec/plan artifact. Each
step's one-line summary goes to `.pf_steps.json` and is reported via `artifact_summary` on
`pf_update_step` (see `_common/lifecycle.md`). Review steps save a `methodology.review`
artifact per the `common/review` scenario fragment.
