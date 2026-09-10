# pf_update_project — contract card

```json
{
  "tool": "pf_update_project",
  "description_sha256": "a845d2eb45c660514475b05f1474a46a6497653c592be2dee52cbaff6a21cd3f",
  "input_schema_sha256": "3f896151daffbc0ef29cce43527e47a647969c1f2cc8c82047dbf16065e4da90",
  "params": {
    "description": {
      "type": "string",
      "required": false
    },
    "expected_removals": {
      "type": "array",
      "required": false
    },
    "members": {
      "type": "array",
      "required": false
    },
    "members_version": {
      "type": "integer",
      "required": false
    },
    "name": {
      "type": "string",
      "required": true
    },
    "repos": {
      "type": "array",
      "required": false
    },
    "scenario": {
      "type": "string",
      "required": false
    },
    "visible": {
      "type": "boolean",
      "required": false
    }
  },
  "response_keys_observed": [
    "created_at",
    "description",
    "members",
    "name",
    "owner_user_id",
    "repos",
    "scenario",
    "updated_at",
    "visible",
    "wi_seq"
  ],
  "hop4_coverage": "written"
}
```

## hop 0-1 — what the caller is told

Eight parameters, one required. Three of them exist to make one destructive
operation safe.

| param | type | required | hop 1 promise |
|---|---|---|---|
| `name` | string | yes | which project |
| `description` | string | no | updated description |
| `visible` | boolean | no | updated visibility |
| `scenario` | string | no | updated scenario repo URL |
| `repos` | array | no | updated repository list |
| `members` | array | no | **REPLACES the whole member list**: `[{user_id, role}]`, role is `viewer\|writer\|maintainer` |
| `members_version` | integer | no | compare-and-set guard; omitting it overwrites unconditionally |
| `expected_removals` | array | no | user_ids this write is allowed to REMOVE |

The `members` description says outright that anyone missing from the list you send
loses access, so adding one person means reading the current list and sending it back
with the addition — the consequence, the repair, the pointer at `expected_removals`
and the whole list reaching the wire are held together by
`internal/mcp/project_members_publication_test.go`
(`TestPublishedMembersDescriptionTeachesTheReadModifyWrite`) and, from the guard's
side, by `internal/mcp/project_members_cas_e2e_db_test.go`
(`TestProjectMembersRemovalToolSchemaAdvertisesExpectedRemovals`).

`members_version`'s description once ended with a NOTE saying it does **not** protect
against sending a short list yourself. That was true and honest while the gap was
open; it became a **lie** when `expected_removals` closed it, and a stale warning is
worse than none — a caller who reads it either sends a parameter it says nothing
about, or concludes the API cannot protect them and stops looking.
<!-- prose-only: because=history -->
The replacement is not a reassurance, it is the third parameter.

## hop 2-3 — what leaves this process, and what binds it

`internal/mcp/tools_projects.go` (`registerProjectTools`) copies every argument except
`name` into the body of `PATCH /v1/projects/<name>` via
`pkg/client/client.go` (`UpdateProject`), bound by
`internal/server/routes_projects.go` (`handleUpdateProject`).

Two coercions before the body is built, both fixing an opaque 400 two layers away:

- `members_version` is an INT column and `*int` on the wire, so a quoted `"3"` becomes
  a JSON number here rather than failing bind as "invalid request body" —
  indistinguishable from the server not knowing the parameter at all; the coerced
  value is read off the bytes a peer really received, together with the published type
  it produces, by `internal/mcp/project_members_publication_test.go`
  (`TestUpdateProjectCoercesBothMembersGuardsOnTheWire`), whose assertion is on the
  JSON TYPE because `"3"` and `3` both contain the digit and a substring check passes
  on the uncoerced body.
- `expected_removals` binds to a `[]string`, so a bare `"u_two"` — the natural mistake
  when removing exactly one person — is coerced rather than dropped, which
  `internal/mcp/helpers_test.go` (`TestNormalizeStringSliceArg`) holds on the
  coercion itself and `internal/mcp/project_members_cas_e2e_db_test.go`
  (`TestProjectMembersRemovalToolForwardsExpectedRemovalsOnTheWire`) holds on the
  wire. Dropped, it would
  come back as a 412 saying the removal was not declared while the declaration sat in
  the caller's own request.

## hop 4 — what it actually does

- **`members_version` guards against a CONCURRENT writer; `expected_removals` guards
  against YOUR OWN short list.** They are different failures and neither substitutes
  for the other: a caller who truncates their own list holds a version that still
  matches — driven both ways by
  `internal/domain/projects_members_removal_db_test.go`
  (`TestUpdateProjectMembersRemovalCallerWhoDoesNotKnowTheListCannotWipeIt`,
  `TestUpdateProjectMembersRemovalStaleVersionConflictTakesPrecedence`), over the set
  arithmetic in `internal/domain/projects_members_removal_test.go`
  (`TestUndeclaredRemovals`).
  The redundancy is the point, the way `--force-with-lease` restates what
  you think the remote is.
  <!-- prose-only: because=judgement -->
- A write that would drop somebody not named in `expected_removals` is refused **412
  `PROJECT_MEMBERS_UNDECLARED_REMOVAL`, which lists them** — end to end by
  `internal/domain/projects_members_removal_db_test.go`
  (`TestUpdateProjectMembersRemovalUndeclaredShrinkIsRefusedAndWritesNothing`) and
  `internal/server/project_members_removal_db_test.go`
  (`TestProjectMembersRemovalHTTPUndeclaredShrinkIs412`), over the arithmetic in
  `internal/domain/projects_members_removal_test.go` (`TestUndeclaredRemovals`), which
  is also where a same-size swap counting as a removal and a role change not counting
  are decided. A same-size swap counts as
  a removal; changing only a role does not.
- A stale `members_version` is **409 `CONFLICT_CAS_FAILED`** with
  `details.current_members_version` — reread and retry; held at three hops:
  `internal/domain/projects_members_cas_db_test.go`
  (`TestUpdateProjectMembersCASStaleVersionReturns409AndReportsCurrent`),
  `internal/domain/projects_members_cas_test.go`
  (`TestMembersCASConflictErr_ReportsBothVersions`) and
  `internal/server/routes_projects_cas_db_test.go`
  (`TestProjectMembersVersionHTTPStaleVersionIs409`).
- The counter is advanced **in the database** (`members_version = members_version + 1`
  computed by Postgres from the stored value), never by the client, and only on a
  write that actually touches `members` — compiled into the statement and asserted in
  all three directions by `internal/domain/projects_members_cas_test.go`
  (`TestBuildProjectUpdate_MembersWriteIncrementsVersionInSQL`,
  `TestBuildProjectUpdate_UnrelatedWriteDoesNotTouchMembersVersion`,
  `TestBuildProjectUpdate_SuppliedVersionIsNeverStored`).
- `role` here accepts `viewer|writer|maintainer` only. `owner` is the
  `projects.owner_user_id` column rather than a member role — the two-vocabulary
  split §6.2 T2-17 requires this card to name, held as a property of the two live
  vocabularies by `internal/domain/role_vocabularies_test.go`
  (`TestTheRoleVocabulariesAreDistinctAndOwnerIsInNeither`), whose
  `owner_is_in_neither` subtest reads both sets out of the code that decides each
  one.

## hop 5 — what comes back

`jsonResult`, no projection — the whole project row. The corpus record above spans 22
calls with no errors.

## Policy

- **§6.2 T2-17** — keep the two-vocabulary role split and **name it in the contract
  cards**. Named above: `pf_whoami` can report `role: "owner"` — pinned verbatim in
  the golden of `internal/mcp/tools_whoami_members_test.go`
  (`TestWhoamiAdminAndOwnerResponsesAreByteIdentical`) — and this tool will refuse it,
  which is the `owner_is_in_neither` half of
  `internal/domain/role_vocabularies_test.go`
  (`TestTheRoleVocabulariesAreDistinctAndOwnerIsInNeither`).
- **§6.2 T2-8 — LANDED** (`aihub#443`). `viewer | writer | maintainer` is the legal
  set, and it is now ranked by one map, `internal/domain/projects.go` (`RoleLevel`),
  which `internal/server/middleware.go` shares by value — the sharing is asserted by
  `internal/server/middleware_project_roles_test.go`
  (`TestRoleLevelIsTheDomainLadder`) and the set is read out of `UpdateProject`'s own
  validation, never restated, by `internal/domain/projects_test.go`
  (`TestRoleLevel_LadderIsExactlyTheValidatedVocabulary`,
  `TestRoleLevel_EveryLegalMemberRoleReachesViewer`). Before that the two packages
  scored `maintainer` 3 and 0 respectively, so this tool would accept a role that a
  read path treated as no membership at all.
- **§6.2 T2-1** — one editability matrix for the whole struct and one error code per
  rejection kind: 409 for state, 403 for permission. This tool has two distinct
  refusals (409 CAS, 412 undeclared removal) that are deliberately different kinds.

## Open

- **§6.4 item 1** — the ladder fix has landed and the DB read has been made:
  `aihub#443`'s attrs record 4 live `projects.members` rows holding `maintainer`
  (a live-DB read dated 2026-09-08 — it dates rather than pins, since a row count is
  not a property of a commit), with the distinct role strings across all 10 projects
  and 47 member rows exactly `viewer | writer | maintainer` — no legacy or arbitrary
  value survives in `members` today.
  <!-- prose-only: because=measurement -->
  So this tool's writes are the provenance of every
  `maintainer` row that exists, which is what makes the vocabulary it validates the
  operative one rather than migration `0013`'s: exactly one SQL string in production
  Go assigns the column
  (`internal/domain/projects_members_removal_test.go`,
  `TestMembersUpdateHasOneWritePathAndOneRemovalCheck`) and it validates the
  vocabulary first, while the one migration that writes `members` CASEs `maintainer`
  down to `writer` rather than storing it
  (`internal/domain/role_vocabularies_test.go`,
  `TestNoMigrationCanStoreAMaintainerMemberRole`). Rows written before the validation
  existed are a fact about a live database rather than about a commit, which is what
  the dated read above is for and this arm is not.
