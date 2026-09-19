# pf_list_skills — contract card

```json
{
  "tool": "pf_list_skills",
  "description_sha256": "e3e2dda0e57c339a9cf054e72700f77ba0fee9cfe86b16b12c51d62e2dcb2b89",
  "input_schema_sha256": "1131b8821e1ab608dbce4e23d473fb9c9c15066dc8c3aaddf885de88c0ffd4f6",
  "params": {
    "cursor": {
      "type": "string",
      "required": false
    },
    "limit": {
      "type": "number",
      "required": false
    },
    "owner": {
      "type": "string",
      "required": false
    }
  },
  "response_keys_observed": null,
  "hop4_coverage": "written"
}
```

## hop 0-1 — what the caller is told

Three optional parameters, no required ones, and a description that scopes the
answer to the caller's registry view — owned, shared or public — while stating
that a skill with no accessible version is not listed.

| param | type | required | hop 1 promise |
|---|---|---|---|
| `owner` | string | no | filter to one owner's user id; absent lists every skill the caller can see |
| `cursor` | string | no | page token from a previous response's next_cursor; absent starts from the first page |
| `limit` | number | no | page size, 1..200; 0 or absent means the default 50 |

The three promises the table abbreviates are the description's own: the
latest-ACCESSIBLE scoping (a skill appears only when the caller can read some
version of it), the read-only boundary (the registry's write and share surface
is not published here), and the pagination shape (items plus a next_cursor
when another page exists).

## hop 2-3 — what leaves this process, and what binds it

`internal/mcp/tools_skills.go` (`registerSkillTools`) builds the query string
with `buildListSkillsParams` and calls `pkg/client/skills.go` (`ListSkills`) →
`GET /v1/skills`, bound by `internal/server/routes_skills.go`
(`handleListSkills`). Absent parameters are OMITTED rather than sent empty —
the server rejects any unknown query parameter — and the forwarding half of
that claim is held by `TestListSkillsForwardsItsThreeOptionalParams`
(`internal/mcp/tools_skills_test.go`), which drives both the populated and the
empty call against a recorder and reads `limit` back off the wire after
sending it as a JSON number (`scalarArg` rather than `strArg` reads it, so a
string spelling leaves the process too).

## hop 4 — what it actually does

- The list runs the registry's own visibility predicate:
  `internal/domain/skill_registry_sharing.go` (`ListSkills`) applies
  `skillVersionAccessSQL` in a LATERAL join, so a skill with no version the
  caller can read produces no row at all — held by
  `TestSkillRegistryListPaginationAndOwnerFilter` and the no-metadata-oracle
  arms (`TestSkillRegistryNoMetadataOracle`, both in
  `internal/domain/skill_registry_db_test.go`).
- `limit` is validated server-side as 1..200 with a default of 50, and the
  response's `next_cursor` is what the handler builds from the last row —
  `TestSkillRegistryListPaginationAndOwnerFilter`
  (`internal/domain/skill_registry_db_test.go`) drives the range, the default
  and the owner filter end to end.
- The cursor compares bytewise on the skill id (`COLLATE "C"`), so the page
  order is the same whatever the database's LC_COLLATE is —
  `TestSkillRegistryListOrdersSkillIDsBytewise`
  (`internal/domain/skill_registry_db_test.go`).
- Each item is a `SkillDetail`: identity plus the caller's latest ACCESSIBLE
  version summary, never the skill's true latest unless the caller can see it
  — the shape `TestSkillRegistrySharingAndLatestAccessible`
  (`internal/domain/skill_registry_db_test.go`) pins.

## hop 5 — what comes back

`jsonResult`, no projection: `items` and, when another page exists,
`next_cursor` reach the model exactly as the handler sent them —
`TestSkillToolsPassTheServerAnswerThroughUnprojected`
(`internal/mcp/tools_skills_test.go`) reads back a key no struct in this
process knows about. Every request this tool makes is a GET —
`TestSkillToolsIssueOnlyGETRequests`
(`internal/mcp/tools_skills_test.go`) holds the read-only claim on the wire
rather than in prose.

## Policy

- Spec D3 (aihub#708): WI visibility never widens skill content. This tool is
  the DISCOVERY half of that boundary — it enumerates what a composer may
  reference, and carries no bundle, no contract and no share list.
## Open

- Nothing this card can settle. The registry's own read semantics are held by
  the aihub#708 Batch 1A arms cited above, and this tool adds only the
  forwarding seam, as of 2026-09-19.
