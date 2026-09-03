# _common/storage.md — artifact storage (injected for pf-execute)

**Artifact type for this step**: `@@ARTIFACT_TYPE@@`

Empty for execute — there is no per-step spec/plan artifact. Each step RETURNS its one-line
summary, reported via `artifact_summary` on `pf_update_step` (see
`_common/lifecycle.md`). Review steps save a `methodology.review` artifact per the
`common/review` scenario fragment.
