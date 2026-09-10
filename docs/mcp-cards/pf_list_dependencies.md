# pf_list_dependencies — contract card

```json
{
  "tool": "pf_list_dependencies",
  "description_sha256": "c280606730c78659546249d247e29916ea764b794c68a45d4a9babed5e0864b8",
  "input_schema_sha256": "0d138f8f344be0161281deb07d0ff88f782e6397ea2be18bb413fb2c4cfe88e4",
  "params": {
    "work_item_id": {
      "type": "string",
      "required": true
    }
  },
  "response_keys_observed": [
    "blocked_by",
    "blocking"
  ],
  "hop4_coverage": "written"
}
```

## hop 0-1 — what the caller is told

One parameter.

| param | type | required | hop 1 promise |
|---|---|---|---|
| `work_item_id` | string | yes | "Work item ID" |

"List dependencies (blocking + blocked_by) for a work item. **Cross-project items
are folded if caller lacks viewer+ permission.**" Both halves matter: the response
has two directions, and one of them can be silently incomplete.

## hop 2-3 — what leaves this process, and what binds it

`internal/mcp/tools_dependency.go` (`registerDependencyTools`) rejects an empty value
and calls `pkg/client/client.go` (`ListDependencies`) →
`GET /v1/work_items/<id>/dependencies`, bound by `internal/server/router.go`
(`handleListDependencies`) — the refusal, the method, the path and the absence of a
body all read off the request the tool really makes in
`internal/mcp/dependency_card_claims_test.go`
(`TestDependencyToolsAddressEdgesByPathAndSendNoCredential`), which also requires the
empty-value case to leave the process untouched. Path segment; no body; no
credentials.

## hop 4 — what it actually does

- Returns both directions: `blocking` (what this work item blocks) and `blocked_by`
  (what blocks it) — one unambiguous edge read from both ends, with each list's
  identity asserted AND the opposite list required to be empty, by
  `internal/domain/dependencies_direction_test.go`
  (`TestListDependencies_Direction`); with a single edge a swapped projection returns
  exactly one entry too, just on the wrong side. Both are always present as arrays,
  never null.
- **"Folded" means one field is withheld rather than the row.** `internal/domain/dependencies.go`
  (`ListDependencies`) returns the far end's `slug`, `project`, `kind` and `note`
  unconditionally and replaces only the canonical `id` with the sentinel `"hidden"`,
  alongside an explicit `accessible: false` — driven against a real cross-project edge,
  with a viewer-side positive control and an equal-count assertion, by the
  `a_folded_row_withholds_only_the_id` subtest of
  `internal/domain/dependencies_direction_test.go`
  (`TestListDependencies_Direction`). So the count is honest, the row is
  identifiable, and the caller is told which case it is looking at rather than having
  to infer it from a magic string.
  It used to be the other way round: the slug was stripped and the **project name was
  still disclosed** — it withheld the part invariant 2 asks for and disclosed the part
  it was trying to protect, with nothing saying which had happened.
- **`Accessible` is a role comparison, and it used to be the wrong one.** It is now
  computed with `internal/domain/projects.go` (`RoleLevel`) — one map,
  `viewer:1, writer:2, maintainer:3`, which `internal/server/middleware.go` shares BY
  VALUE rather than copying, so the two packages rank a role identically by
  construction, and `TestRoleLevelIsTheDomainLadder` fails if the two fork again. Until `aihub#443` landed this function ran a second, disagreeing
  ladder that scored `maintainer` 0 and gave rung 3 to `owner`, so a `maintainer` on
  the far-end project failed `>= viewer` and had the edge marked inaccessible while
  the server package scored the same role at 3.
  `internal/server/middleware_project_roles_test.go` (`TestRoleLevelIsTheDomainLadder`)
  fails if the two fork again.
- The `accessible` flag is an explicit boolean rather than something to infer from
  `id == "hidden"`, because a sentinel string is a fact about the struct's history
  rather than an API; the same subtest asserts both fields on the same folded row, so
  a flag that stopped tracking the sentinel is red
  (`internal/domain/dependencies_direction_test.go`,
  `TestListDependencies_Direction`).
- `aihub#357` made both endpoints resolve slugs, held end to end through the real
  router by `internal/server/dependencies_slug_db_test.go`
  (`TestDependencyEndpointsResolveSlugs`).
  Before that a slug read matched no
  row and the handler answered 200 with two empty lists, indistinguishable from a work
  item with no dependencies — which is what made `pf_create_work_item`'s `blocked_by`
  look like it created no edges at all.
  <!-- prose-only: because=history -->

## hop 5 — what comes back

`jsonResult`, no projection. `slug` deliberately carries no `omitempty`: with one,
"you may not see this work item" and "this work item has no slug" would arrive as the
same absent field — the tag, the pointer type, the marshalled `"slug":null` and
`note`'s deliberate `omitempty` as a control are all held by
`internal/mcp/dependency_card_claims_test.go`
(`TestDependencyListEntryDisclosesSlugWithoutOmitempty`). The corpus record above is
the union of top-level keys real callers have been handed.

## Policy

- **§6.2 T2-8 — LANDED** (`aihub#443`, merged as `52e1263`). The domain func is gone;
  both packages read one map containing exactly the three legal member roles, and the
  test pins `maintainer` rather than a rung the vocabulary cannot produce — the one-map
  half by `internal/server/middleware_project_roles_test.go`
  (`TestRoleLevelIsTheDomainLadder`), which compares contents AND map identity, and the
  vocabulary half by `internal/domain/projects_test.go`
  (`TestRoleLevel_LadderIsExactlyTheValidatedVocabulary`).
  The `Accessible` computation above is **one** of the ladder's **six** non-test
  reader functions and reads it in **two** statements, one per direction of this
  response; `checkProjectAccess`'s member loop in `internal/domain/projects.go` is the
  other `domain.RoleLevel` reader, and the remaining four are in `internal/server`,
  reading the same map value through the `roleLevel` alias (`checkProjectAccess`,
  `hasProjectAccess`, `handleListWorkItems`, `checkProjectAccessSoft`) — 1 + 1 + 4 —
  where **this card said "three" until 2026-09-10 (`aihub#584`), counting
  domain-spelled STATEMENTS only**, a third of the blast radius of changing the
  ladder, and the set is now censused in both directions by
  `internal/mcp/dependency_card_claims_test.go`
  (`TestRoleLevelIsReadAtExactlyTheSitesThisCardNames`).
- **§6.2 T2-16** — the chain needs no change of its own; it inverts for exactly one
  legal role, so T2-8 is ruled first. Record that the out-of-scope-reads-as-not-found
  rule applies to **admins too** — `callerRole == "admin"` short-circuits the ladder
  here, which is the exemption a later "admins see everything" change would quietly
  remove; the `an_admin_short_circuits_the_ladder` subtest of
  `internal/domain/dependencies_direction_test.go`
  (`TestListDependencies_Direction`) drives it with an EMPTY project-roles map, which
  is what an admin really holds, against a non-admin control carrying the same empty
  map.
- **§6.2 T2-11** — "scoped to visible projects" has two implementation shapes; a new
  resolver must be told which one it inherits. This endpoint inherits the work-item
  scoping shape, not `ListProjects`'.

## Open

- **§6.4 item 1 is CLOSED for this tool, and it closed against the card's own
  hedge.** `aihub#443` made the DB read while fixing the ladder and recorded it in
  that work item's attrs — a **live-DB read dated 2026-09-08**, which dates rather
  than pins: a row count is not a property of any commit, so a later reader must
  re-run it rather than re-derive it from the tree.
  <!-- prose-only: because=external-state --> As of that read, 4 live
  `projects.members` rows hold `maintainer`, and across all 10 projects and 47 member
  rows the distinct role strings are exactly `viewer | writer | maintainer`.
  <!-- prose-only: because=measurement -->
  Migration `0013` mapped `maintainer` to `writer` on backfill, which
  `internal/mcp/dependency_card_claims_test.go`
  (`TestMigration0013MappedMaintainerToWriterOnBackfill`) holds along with the role
  filter that makes the mapping reachable.
  So those rows did arrive from later members writes, as this card predicted.
  **The inversion bit here**: one
  maintainer of another project holds the global role `writer` rather than `admin`, so
  `ListDependencies` scored that caller 0 and shadowed cross-project entries as
  `accessible:false` / `id:"hidden"`. This tool is the half of the T2-8 blast radius
  with live instances; the `GET /v1/projects/:name` half had none, because all 4 rows
  belong to their project's own `owner_user_id` and the owner check short-circuits
  before the member loop.
  <!-- prose-only: because=measurement -->
