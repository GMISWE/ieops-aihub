# pf_get_skill — contract card

```json
{
  "tool": "pf_get_skill",
  "description_sha256": "80c22086b7d95d4bca03d978cec8da66a7394e2b6e09ef16266b755c7a52ce8e",
  "input_schema_sha256": "f309d91cdb1231b285b50dc1153c4448d0d3c76f98865807a1c7179dcb8d2ef4",
  "params": {
    "skill_id": {
      "type": "string",
      "required": true
    }
  },
  "response_keys_observed": null,
  "hop4_coverage": "written"
}
```

## hop 0-1 — what the caller is told

One required parameter and a description whose two load-bearing promises are
the no-oracle answer (a skill the caller cannot see answers NOT_FOUND, the same
as a missing one) and the no-content boundary (the response carries identity
and version metadata, never a bundle or contract).

| param | type | required | hop 1 promise |
|---|---|---|---|
| `skill_id` | string | yes | the `skill_`-prefixed registry id, as returned by pf_list_skills |

The owner's exception in the description — an owner sees the identity even
before any version exists — is the registry's own disclosure rule, not a
separate path this tool takes.

## hop 2-3 — what leaves this process, and what binds it

`internal/mcp/tools_skills.go` (`registerSkillTools`) refuses an empty
`skill_id` in this process and then calls `pkg/client/skills.go` (`GetSkill`)
→ `GET /v1/skills/<id>`, bound by `internal/server/routes_skills.go`
(`handleGetSkill`); the refusal is held by
`TestSkillReadToolsRefuseMissingRequiredArgsBeforeAnyRequest`
(`internal/mcp/tools_skills_test.go`), which asserts no HTTP request is issued,
and the segment is escaped by `pkg/client/client.go` (`seg`).

## hop 4 — what it actually does

- The answer is a `SkillDetail`: identity plus the caller's latest ACCESSIBLE
  version summary, where "latest" is computed under
  `internal/domain/skill_registry_sharing.go` (`GetSkill`) through the same
  `skillVersionAccessSQL` predicate every registry read uses —
  `TestSkillRegistrySharingAndLatestAccessible`
  (`internal/domain/skill_registry_db_test.go`) holds that a shared older
  version is what a grantee reads while the owner reads the newer one.
- The no-oracle half: for a caller with no access at all the skill answers
  NOT_FOUND, byte-identical to a missing skill —
  `TestSkillRegistryNoMetadataOracle`
  (`internal/domain/skill_registry_db_test.go`) drives exactly that
  indistinguishability, and `TestSkillRegistryAuthorizationAndAdmin` (same
  file) holds the admin's wider read.
- The identity fields the response carries (`id`, `name`, `owner_user_id`,
  `display_name`, `latest_version`, `created_at`, `updated_at`,
  `latest_accessible`) come from one SELECT in `GetSkill`; for a non-owner the
  `updated_at` is overwritten with the caller's latest accessible version's
  timestamp, so even a metadata field cannot disclose a version the caller
  cannot read — the overwrite is part of
  `TestSkillRegistryPrivatePublishDoesNotLeakIdentityMetadata`
  (`internal/domain/skill_registry_db_test.go`).

## hop 5 — what comes back

`jsonResult`, no projection: every key of the `SkillDetail` the server sends
reaches the model — `TestSkillToolsPassTheServerAnswerThroughUnprojected`
(`internal/mcp/tools_skills_test.go`) reads back a key no struct in this
process knows about. The one GET this tool can issue is held on the wire by
`TestSkillToolsIssueOnlyGETRequests` (`internal/mcp/tools_skills_test.go`).

## Policy

- Spec D3 (aihub#708): this read discloses a skill's EXISTENCE only together
  with access to one of its versions (or ownership); it discloses no content.
  Both halves are the registry's, applied unchanged.

## Open

- Nothing this card can settle. There is no pagination on this endpoint: one
  skill, one answer, as of 2026-09-19.
