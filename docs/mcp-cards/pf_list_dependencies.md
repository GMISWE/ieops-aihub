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
(`handleListDependencies`). Path segment; no body; no credentials.

## hop 4 — what it actually does

- Returns both directions: `blocking` (what this work item blocks) and `blocked_by`
  (what blocks it). Both are always present as arrays, never null.
- **"Folded" means one field is withheld, not the row.** `internal/domain/dependencies.go`
  (`ListDependencies`) returns the far end's `slug`, `project`, `kind` and `note`
  unconditionally and replaces only the canonical `id` with the sentinel `"hidden"`,
  alongside an explicit `accessible: false`. So the count is honest, the row is
  identifiable, and the caller is told which case it is looking at rather than having
  to infer it from a magic string.
  It used to be the other way round: the slug was stripped and the **project name was
  still disclosed** — it withheld the part invariant 2 asks for and disclosed the part
  it was trying to protect, with nothing saying which had happened.
- **`Accessible` is a role comparison, and it used to be the wrong one.** It is now
  computed with `internal/domain/projects.go` (`RoleLevel`) — one map,
  `viewer:1, writer:2, maintainer:3`, which `internal/server/middleware.go` shares BY
  VALUE rather than copying, so the two packages rank a role identically by
  construction. Until `aihub#443` landed this function ran a second, disagreeing
  ladder that scored `maintainer` 0 and gave rung 3 to `owner`, so a `maintainer` on
  the far-end project failed `>= viewer` and had the edge marked inaccessible while
  the server package scored the same role at 3.
  `internal/server/middleware_project_roles_test.go` (`TestRoleLevelIsTheDomainLadder`)
  fails if the two fork again.
- The `accessible` flag is an explicit boolean rather than something to infer from
  `id == "hidden"`, because a sentinel string is a fact about the struct's history and
  not an API.
- `aihub#357` made both endpoints resolve slugs. Before that a slug read matched no
  row and the handler answered 200 with two empty lists, indistinguishable from a work
  item with no dependencies — which is what made `pf_create_work_item`'s `blocked_by`
  look like it created no edges at all.

## hop 5 — what comes back

`jsonResult`, no projection. `slug` deliberately carries no `omitempty`: with one,
"you may not see this work item" and "this work item has no slug" would arrive as the
same absent field. The corpus record above is the union of top-level keys real callers
have been handed.

## Policy

- **§6.2 T2-8 — LANDED** (`aihub#443`, merged as `52e1263`). The domain func is gone;
  both packages read one map containing exactly the three legal member roles, and the
  test pins `maintainer` rather than a rung the vocabulary cannot produce. The
  `Accessible` computation above is one of that map's three non-test call sites.
- **§6.2 T2-16** — the chain needs no change of its own; it inverts for exactly one
  legal role, so T2-8 is ruled first. Record that the out-of-scope-reads-as-not-found
  rule applies to **admins too** — `callerRole == "admin"` short-circuits the ladder
  here, which is the exemption a later "admins see everything" change would quietly
  remove.
- **§6.2 T2-11** — "scoped to visible projects" has two implementation shapes; a new
  resolver must be told which one it inherits. This endpoint inherits the work-item
  scoping shape, not `ListProjects`'.

## Open

- **§6.4 item 1 is still open even though the fix landed.** Whether any live
  `projects.members` row ever held `maintainer` — i.e. whether the inversion ever bit
  a real caller — needs a DB read nobody has made. Migration `0013` mapped
  `maintainer` to `writer` on backfill, so such a row could only have arrived from a
  later members write. The card records the code path and its history, not a claim
  about live data.
