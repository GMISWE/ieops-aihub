# _common/storage.md — artifact storage (injected for pf-execute)

> Injected by `hooks/pf-skill-router` for every pf-execute step. pf-spec and pf-plan are
> self-contained thin skills now — they call `pf_save_artifact` inline with a literal type
> string (see their SKILL.md) and no longer depend on this fragment or router injection.

**Artifact type for this step**: `@@ARTIFACT_TYPE@@`

## execute steps

`@@ARTIFACT_TYPE@@` is empty for execute — there is no per-step spec/plan artifact. Each
step's one-line summary goes to `.pf_steps.json` and is reported via `artifact_summary` on
`pf_update_step` (see `_common/lifecycle.md`). Review steps save a `methodology.review`
artifact per the `common/review` scenario fragment.
