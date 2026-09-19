# pf_list_skill_versions — contract card

```json
{
  "tool": "pf_list_skill_versions",
  "description_sha256": "c67e3f2591fbe25059ea115c7eb394cd4734af8f4de6d8fdd6f5a97b9ca8179a",
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

One required parameter, and a description that promises two different
absence behaviours in the two places a composer can meet one: an INACCESSIBLE
version is omitted from the list, while a skill the caller cannot see at all
answers NOT_FOUND. Those are different answers on purpose — the first keeps the
enumeration usable under a partial grant, the second keeps the no-oracle rule.

| param | type | required | hop 1 promise |
|---|---|---|---|
| `skill_id` | string | yes | the `skill_`-prefixed registry id, as returned by pf_list_skills |

The description also names what this enumeration is FOR: picking an exact
skill version number for a workflow proposal, which is the aihub#720
composer's read.

## hop 2-3 — what leaves this process, and what binds it

`internal/mcp/tools_skills.go` (`registerSkillTools`) refuses an empty
`skill_id` in this process and then calls `pkg/client/skills.go`
(`ListSkillVersions`) → `GET /v1/skills/<id>/versions`, bound by
`internal/server/routes_skills.go` (`handleListSkillVersions`); the refusal —
no HTTP request for a missing or empty id — is held by
`TestSkillReadToolsRefuseMissingRequiredArgsBeforeAnyRequest`
(`internal/mcp/tools_skills_test.go`).

## hop 4 — what it actually does

- The enumeration is access-filtered version by version:
  `internal/domain/skill_registry_sharing.go` (`ListSkillVersions`) walks only
  the versions `skillVersionAccessSQL` admits for this caller, so a private
  newer version never appears in a grantee's list — the sharing arms that hold
  this are `TestSkillRegistrySharingAndLatestAccessible` and
  `TestSkillRegistryScopedKeysStayScoped`
  (`internal/domain/skill_registry_db_test.go`).
- Each item is a summary — version, visibility, digest, author — and the
  enumeration a grantee reads is the access-filtered one beside it: after a
  private v2 is published, the member's list is exactly [v1]
  (`TestSkillRegistrySharingAndLatestAccessible`,
  `internal/domain/skill_registry_db_test.go`). A version's bundle and
  contract are the exact-version read's half, through pf_get_skill_version.
- For a caller who cannot see the skill at all the whole call answers NOT_FOUND
  — `TestSkillRegistryNoMetadataOracle`
  (`internal/domain/skill_registry_db_test.go`) holds the same-bytes answer
  for the registry's reads.

## hop 5 — what comes back

`jsonResult`, no projection: the `items` array reaches the model exactly as
the handler sent it — `TestSkillToolsPassTheServerAnswerThroughUnprojected`
(`internal/mcp/tools_skills_test.go`) reads back a key no struct in this
process knows about. The single GET this tool issues is held on the wire by
`TestSkillToolsIssueOnlyGETRequests` (`internal/mcp/tools_skills_test.go`).

## Policy

- Spec D3 (aihub#708): a list of versions is a list of REFERENCES; no skill
  content travels with it. The composer reads one exact version's content only
  through the versioned read, where the no-oracle rule answers again.

## Open

- Nothing this card can settle. There is no pagination on this endpoint: the
  versions of one skill are few, as of 2026-09-19.
