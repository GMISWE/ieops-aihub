# _common/storage.md — artifact storage

**Artifact type for this step**: `@@ARTIFACT_TYPE@@`

Empty for execute: no per-step spec/plan artifact. Each step RETURNS its one-line summary,
reported via `artifact_summary` on `pf_update_step`. Review steps save a `methodology.review`
artifact per the `common/review` scenario fragment.
