# pf_list_projects — contract card

```json
{
  "tool": "pf_list_projects",
  "description_sha256": "56b304227a8bbdd0019414e7c0f00dc7f65d9871c01eabbbd2b8cc7070bc59f9",
  "input_schema_sha256": "efddc7bd8bbcef73a14eb1ace1ffdaec81e518ef1e13c1e9271d0b8acb694a49",
  "params": {},
  "response_keys_observed": [
    "items"
  ],
  "hop4_coverage": "written"
}
```

## hop 0-1 — what the caller is told

Zero parameters — `emptyObjectSchema()`. The description does one job beyond listing:
it names `members_version` as "the compare-and-set token to pass back to
`pf_update_project` when changing members" — all three parts of which
`internal/mcp/list_tools_wire_shape_test.go`
(`TestPublishedMembersVersionTokenClaimNamesTheToolThatConsumesIt`) requires off a
live session, together with the parameter on `pf_update_project` that the advice
points at — because a guard nobody can find the input for is a guard nobody passes.

There is no parameter table because there is nothing to tabulate. Every risk on this
tool is on the response side.

## hop 2-3 — what leaves this process, and what binds it

`internal/mcp/tools_projects.go` (`registerProjectTools`) calls
`pkg/client/client.go` (`ListProjects`) with nil params → `GET /v1/projects`, bound by
`internal/server/routes_projects.go` (`handleListProjects`).

`pf_whoami` calls the same client method to build its `projects` array, so the two
tools' project views come from one endpoint and differ only in what this process does
with the result — `internal/mcp/tools_whoami_members_test.go` drives pf_whoami's
`projects` off a fixture answering `GET /v1/projects`, which is that route observed.

## hop 4 — what it actually does

- Returns every project **visible to the caller**: public, plus member, plus owned —
  the three-way `WHERE` held by `TestListProjectsScopesBySQLAndNeverRanksARole`.
  That is one of the **two** implementation shapes of "scoped to visible projects" in
  this repo; the work-item scoping rule is the other, and they are not
  interchangeable.
- Each item carries `members`, `members_version`, `owner_user_id`, `repos` and
  `scenario`, held in both directions by `internal/domain/card_claims_wave2_test.go`
  (`TestListProjectsSelectsAndBindsTheItemFieldsTheCardNames`): the column has to be
  in `projectSelectCols` and the field has to bind a JSON key, because an unselected
  column is marshalled at its zero value rather than dropped. `members_version` is
  the token `pf_update_project`'s CAS consumes, and this is where a caller reads it —
  `internal/mcp/project_members_cas_e2e_db_test.go`
  (`TestProjectMembersCASVersionIsReadableFromListProjects`) reads it back out of
  this tool against a real database and
  (`TestProjectMembersCASStaleVersionIsRefusedEndToEnd`) shows the value being
  consumed, refused and reported.
- `members` arrives as a JSON array. That shape is worth naming because `pf_whoami`'s
  member scan handled every shape **except** that one for a period, and every
  non-admin non-owner member silently fell through to the public/viewer default.
  <!-- prose-only: because=history -->

## hop 5 — what comes back

`jsonResult`, no projection: the single top-level key is `items`, and
`internal/mcp/list_tools_wire_shape_test.go`
(`TestListProjectsPassesTheServerAnswerThroughUnprojected`) holds both halves of that
by making the fake send a key nothing in this process has ever heard of and requiring
it to arrive — `items` alone would come back under a keep-list too. The corpus record
above spans 38 calls, **29 of them prose** rather than strict JSON — which is why the
observed top-level key union is just `items` and says nothing about per-item fields.
<!-- prose-only: because=measurement -->
A prose result contributes no keys, and reading that as "returns nothing" is the trap
the corpus README warns about.
<!-- prose-only: because=judgement -->

## Policy

- **§6.2 T2-11** — settled policy, nothing to rule, but the caveat must be recorded:
  **"scoped to visible projects" has two implementation shapes, so a new resolver must
  be told which one it inherits.** This endpoint is one of the two.
- **§6.2 T2-16** — the out-of-scope-reads-as-not-found rule applies to **admins
  too**; that is the exemption a later "admins see everything" change would quietly
  remove.
- **§6.2 T2-8** — the member role vocabulary in `members` is
  `viewer | writer | maintainer`, read out of `UpdateProject`'s own validation by
  `internal/domain/projects_test.go`
  (`TestRoleLevel_LadderIsExactlyTheValidatedVocabulary`) and ranked since `aihub#443`
  by one shared map rather than by two ladders that scored `maintainer` differently —
  `internal/server/middleware_project_roles_test.go`
  (`TestRoleLevelIsTheDomainLadder`) compares the two by contents and by pointer.

## Open

- **§6.4 item 1 is measured, not open.** `aihub#443`'s attrs record the DB read
  (a live-DB read dated 2026-09-08, so it dates rather than pins — a row count is not
  a property of a commit): 4 live `projects.members` rows hold `maintainer`, and
  the distinct role strings across all 10 projects and 47 member rows are exactly
  `viewer | writer | maintainer`.
  <!-- prose-only: because=measurement -->
  **The inversion could never bite here, for a simpler
  reason than the measurement**: this endpoint does not rank a role at all. It scopes
  by the SQL predicate in `internal/domain/projects.go` (`ListProjects`) — public, plus
  `owner_user_id`, plus a `members` containment test, and no predicate at all on the
  admin branch — while `roleLevel` stays off its path;
  `internal/domain/card_claims_wave2_test.go`
  (`TestListProjectsScopesBySQLAndNeverRanksARole`) reads all three terms, the
  unscoped branch and the absence of any ranking identifier out of that function's
  own AST. ⚠️ The SQL is not the whole scope, and the admin branch is not
  unscoped in the ANSWER: after the rows are scanned, `ListProjects` narrows them
  again in Go through `applyProjectScope`, which drops every project whose name is
  not the caller's api-key `project_scope` — on BOTH branches, so a scoped admin key
  sees one project rather than the whole table — held by that same arm's
  `the_scope_filter_runs_after_the_query` subtest and, on the filter's own behaviour,
  by `internal/domain/projects_test.go` (`TestApplyProjectScope`). It ranks no role
  either, so the conclusion above stands. `members` is reported rather than compared
  (`TestListProjectsScopesBySQLAndNeverRanksARole`). `pf_list_dependencies` is the
  tool where the inverted ladder did bite, and it is the one of T2-8's two named
  surfaces with live instances.
  <!-- prose-only: because=history -->
