# pf_get_skill_version — contract card

```json
{
  "tool": "pf_get_skill_version",
  "description_sha256": "3150985e44625f10dd86ac7b9ca3716a42cc7ce08d7c35032ac789b2a54ff424",
  "input_schema_sha256": "5f741ae1f761b1d49ab5eada2e070d4ff0bf85316fceeb9a9dd339971efb36ea",
  "params": {
    "skill_id": {
      "type": "string",
      "required": true
    },
    "version": {
      "type": "number",
      "required": true
    }
  },
  "response_keys_observed": null,
  "hop4_coverage": "written"
}
```

## hop 0-1 — what the caller is told

Two required parameters and the strictest promise in the family: the version is
EXACT and never falls back to latest — a refusal is a refusal, never a
substitution. This is the tool a composer reads before pinning a
`skill_version` into a workflow proposal, so a silent latest-resolution here
would pin a flow to content nobody chose.
<!-- prose-only: because=counterfactual -->

| param | type | required | hop 1 promise |
|---|---|---|---|
| `skill_id` | string | yes | the `skill_`-prefixed registry id, as returned by pf_list_skills |
| `version` | number | yes | exact version number, a positive integer (never latest) |

The description's pointer to pf_get_skill for the "latest accessible" need is
what keeps the two tools from being read as one knob with two spellings.

## hop 2-3 — what leaves this process, and what binds it

`internal/mcp/tools_skills.go` (`registerSkillTools`) refuses an empty
`skill_id` and a non-integral or non-positive `version` in this process —
`skillVersionArg` is that guard, held by
`TestGetSkillVersionRefusesANonIntegralOrNonPositiveVersion` and
`TestSkillReadToolsRefuseMissingRequiredArgsBeforeAnyRequest`
(`internal/mcp/tools_skills_test.go`) — and then calls
`pkg/client/skills.go` (`GetSkillVersion`) →
`GET /v1/skills/<id>/versions/<version>`, bound by
`internal/server/routes_skills.go` (`handleGetSkillVersion`), whose
`skillVersionParam` re-requires a positive integer server-side.

## hop 4 — what it actually does

- The request names one exact segment: version 7 produces
  `GET /v1/skills/<id>/versions/7` — never the latest, never a neighbour —
  held on the wire by `TestGetSkillVersionRequestsTheExactVersionSegment`
  (`internal/mcp/tools_skills_test.go`).
- The answer is the registry's full version — the summary (version, visibility,
  digest, author) plus the immutable bundle and contract — and it arrives under
  the registry's own access check: `internal/domain/skill_registry_sharing.go`
  (`GetSkillVersion`) resolves through `skillVersionAccessSQL`, so a version
  the caller cannot read answers NOT_FOUND exactly like a missing one
  (`TestSkillRegistryNoMetadataOracle`,
  `internal/domain/skill_registry_db_test.go`); the route-level auth and
  exact-version binding are held by `TestSkillRoutesAuthVersionsAndExpectedLatest`
  (`internal/server/routes_skills_db_test.go`).
- Revocation is re-checked on every read: sharing withdrawn between a pin and
  this read changes the answer from content to NOT_FOUND — the recheck arms are
  `TestSkillRegistryRevocationAndAuthRecheck`
  (`internal/domain/skill_registry_db_test.go`).

## hop 5 — what comes back

`jsonResult`, no projection: `bundle` and `contract` reach the model as the
server stored them, alongside the summary fields. The one GET this tool issues
is held by `TestSkillToolsIssueOnlyGETRequests`
(`internal/mcp/tools_skills_test.go`); a refused version never leaves the
process at all.

## Policy

- Spec D3 (aihub#708), the content half: bundles and contracts travel ONLY
  through this exact-version read, under the registry's own access check — the
  discovery tools beside it carry references only.

## Open

- Nothing this card can settle. The response carries the version's stored
  digest, so a caller can verify transport integrity itself, as of 2026-09-19.
