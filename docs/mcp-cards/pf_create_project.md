# pf_create_project — contract card

```json
{
  "tool": "pf_create_project",
  "description_sha256": "23fc9a29fd3772a5e5f7f76f79bdb7201a93cd16850980dee36223a34d879762",
  "input_schema_sha256": "e768c3d52cad5d6a1654be73791e9634f374f67f29217260ed4e8060f544a18c",
  "params": {
    "description": {
      "type": "string",
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

Five parameters, one required.

| param | type | required | hop 1 promise |
|---|---|---|---|
| `name` | string | yes | lowercase letters/digits/dash/underscore, 1-40 chars |
| `description` | string | no | optional description |
| `visible` | boolean | no | publicly visible, default true |
| `scenario` | string | no | scenario repo URL |
| `repos` | array | no | `{name, url, github_owner_repo?, description?}` + an all-or-nothing structured block |

The `repos` description carries an **all-or-nothing** rule: if any structured field
(`positioning`, `tech_stack`, `main_modules`, `change_scenarios`, `generated_at`,
`generated_commit`) is set, all four content fields are required, in English — a
sentence `internal/mcp/project_publication_contract_test.go`
(`TestPublishedRepoBlockRuleLivesInProseAndNotInTheSchema`) requires of every tool
that publishes the parameter, naming each of the six fields. That is a conditional
requirement a flat `required` array cannot express, so it is stated in prose:
`TestPublishedRepoBlockRuleLivesInProseAndNotInTheSchema` reads this tool's
`required` array and finds `name` alone in it, which is the same limitation that
produced `pf_ship` and `pf_batch_create_work_items` as separate tools.

## hop 2-3 — what leaves this process, and what binds it

`internal/mcp/tools_projects.go` (`registerProjectTools`) checks `name` locally and
passes the **whole argument map** to `pkg/client/client.go` (`CreateProject`) →
`POST /v1/projects`, bound by `internal/server/routes_projects.go`
(`handleCreateProject`). Wholesale forwarding, so no hop-2 drift surface.

## hop 4 — what it actually does

- Creates the project row with the caller as `owner_user_id`, an empty `members`
  list, and `wi_seq` at its initial value — the counter that produces `project#N`
  slugs, and `internal/domain/project_creation_row_test.go`
  (`TestProjectCreationSetsTheCallerAsOwnerAndLeavesMembersAndTheCounterToTheSchema`)
  holds each part: the six columns the INSERT names, the caller in the owner
  position, the two schema defaults the statement leaves alone, and the increment
  that hands the slug out.
- **`scenario` is the URL the step graph is resolved from.** Its last two path
  segments become the owner-qualified clone directory, which is why two orgs' repos
  of the same name no longer share one checkout: they used to, and the second was
  never cloned while its projects silently ran the first org's step graph.
- `visible` decides whether the project appears in `pf_list_projects` for non-members.
- No members can be set at creation: the only way to add one is `pf_update_project`,
  which replaces the whole list —
  `internal/mcp/project_publication_contract_test.go`
  (`TestProjectMembersAreSettableOnlyThroughTheUpdateTool`) censuses the registry for
  the parameter and measures what becomes of a `members` key sent here anyway, and
  `internal/domain/project_creation_row_test.go` holds the reason it has no effect,
  which is that the create request struct has no field to bind it to.
- The all-or-nothing rule fires on a NARROWER trigger than the description's
  wording: `internal/domain/projects.go` (`validateDescriptionBlock`) runs on every
  repo entry but returns immediately unless the entry's own `hasDescriptionBlock`
  predicate is true, and that predicate tests the four content fields and nothing
  else — so a repo carrying only `generated_at` or only `generated_commit` is
  accepted with no content field at all, which
  `internal/domain/project_repo_block_rule_test.go`
  (`TestRepoDescriptionBlockTriggersOnTheFourContentFieldsOnly`) drives one field at
  a time, in both directions.

## hop 5 — what comes back

`jsonResult`, no projection — the whole project row. The corpus record above is the
union over a single observed call, which is what a create tool for a long-lived
object looks like in a 21-day window; it is not evidence of disuse in the sense
§6.2 T2-7 uses.

## Policy

- **§6.2 T2-11** — a new project inherits the visible-projects scoping shape from
  `visible` plus membership; the two implementation shapes caveat applies to anything
  that later resolves it.
- **§6.1 T1-4** — the ruling is to gate the published schema against DB CHECKs rather
  than field-by-field; `name`'s character rule is enforced in the domain and stated
  here in prose, with the three statements of it bound to each other by
  `internal/mcp/project_publication_contract_test.go`
  (`TestPublishedProjectNameRuleIsTheEnforcedOne`) — hop 1's stated bounds, the
  migration's CHECK and `projectNameRe`.

## Open

- Nothing this card can settle here. The all-or-nothing `repos` rule is unenforced by
  the schema and enforced by the server, which is the correct direction but leaves the
  published contract weaker than the real one —
  `internal/mcp/project_publication_contract_test.go`
  (`TestPublishedRepoBlockRuleLivesInProseAndNotInTheSchema`) holds the schema half
  and `internal/domain/project_repo_block_rule_test.go`
  (`TestRepoDescriptionBlockTriggersOnTheFourContentFieldsOnly`) the server half.
- The published wording of that rule is wider than the enforcement, and which of the
  two should move is not settled here: hop 1 says "any structured field" over a
  six-field block, and the server reads four of them. Measured 2026-09-10 under
  `aihub#582`, and pinned at the measured value by
  `internal/domain/project_repo_block_rule_test.go`, so closing the gap in either
  direction is a diff that has to move this card with it.
