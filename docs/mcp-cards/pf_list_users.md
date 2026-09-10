# pf_list_users — contract card

```json
{
  "tool": "pf_list_users",
  "description_sha256": "d8564a746756c623768dd2d6908a689083aa77434ffe236548bfaaacbf521f6e",
  "input_schema_sha256": "efddc7bd8bbcef73a14eb1ace1ffdaec81e518ef1e13c1e9271d0b8acb694a49",
  "params": {},
  "response_keys_observed": [
    "items"
  ],
  "hop4_coverage": "written"
}
```

## hop 0-1 — what the caller is told

Zero parameters — `emptyObjectSchema()`. "List all users (**admin only**)."

There is no parameter table because there is nothing to tabulate, so every property
of this tool is at hops 3-5.

## hop 2-3 — what leaves this process, and what binds it

`internal/mcp/tools_users.go` (`registerUserTools`) calls
`pkg/client/client.go` (`ListUsers`) → `GET /v1/admin/users`, bound by
`internal/server/router.go` (`handleListUsers`).

The route sits under the **admin group**, which carries its own middleware rather
than relying on a per-handler check. That is why the "admin only" in the description
is a statement about routing, not about a branch inside the handler.

## hop 4 — what it actually does

- Returns at most 100 user rows, newest first, each carrying `id`, `display_name`,
  `email`, `user_type` and the global `role` — the SELECT list, the response map, the
  cap and the ordering are all read out of the handler by
  `internal/server/list_users_response_shape_test.go`
  (`TestListUsersReturnsTheColumnsItSelectsAndCapsAtOneHundred`).

  ⚠️ This card used to say it returns "every user row" and that author aliases were
  among the fields. The query carries `ORDER BY created_at DESC LIMIT 100` and
  selects five columns
  (`TestListUsersReturnsTheColumnsItSelectsAndCapsAtOneHundred`). There is no cursor
  and no total here, so a deployment past a hundred users is answered with the
  hundred newest and nothing in the response says so.
- The **global** role vocabulary here is `writer | admin`, held against the migration
  CHECK by `internal/domain/user_fields_test.go`
  (`TestUsersVocabulariesMatchTheMigrations`), and it is a **third** vocabulary
  distinct from both the member roles (`viewer|writer|maintainer`) and the project
  owner column — `internal/domain/card_claims_wave2_test.go`
  (`TestTheThreeRoleVocabulariesAreNotSupersetsOfOneAnother`) holds the RELATION
  between them, which is the part a vocabulary arm on either set alone cannot see:
  neither set contains the other, and `owner` is in neither. A card that did not say
  so would leave a reader assuming `role` means the same thing everywhere it appears.
- `author_aliases` is the `users` column `pf_create_user` and `pf_update_user` write,
  and this response does **not** carry it: the five columns above are the whole item
  (`TestListUsersReturnsTheColumnsItSelectsAndCapsAtOneHundred`). No `SELECT`
  anywhere in this repo reads the column — `internal/domain/card_claims_wave2_test.go`
  (`TestNoSelectInThisRepoReadsAuthorAliases`) censuses every SQL literal in every
  non-test file — so the git-author mapping `docs/design/polyforge-v1-design.md`
  reserves it for is still a reservation. It could not be changed from MCP at all
  until `pf_update_user` published it, which
  `internal/mcp/update_user_param_publication_test.go`
  (`TestUpdateUserAuthorAliasesIsPublished`) keeps true.

## hop 5 — what comes back

`jsonResult`, no projection; the single top-level key is `items`, held by
`internal/mcp/list_tools_wire_shape_test.go`
(`TestListUsersPassesTheServerAnswerThroughUnprojected`), which requires a key
nothing in this process has heard of to arrive — `items` alone would come back under
a keep-list too. The corpus record above spans 12 calls, all strict JSON, with no
errors.

## Policy

- **§6.2 T2-17** — the two-vocabulary role split must be named in the cards. This
  card names the third one: the global role, which is neither a member role nor the
  owner column.
- **§6.2 T2-18** — `user_id`-shaped parameters elsewhere must say which identity they
  filter; the identities they can name are the rows this tool returns, because every
  `*_user_id` column in the schema FK-references `users(id)`
  (`internal/domain/card_claims_wave2_test.go`,
  `TestEveryUserIdColumnReferencesTheRowsListUsersReturns`). ⚠️ That arm holds the
  second clause; the first is a ruling about other tools' published prose and is the
  review half's, per `aihub#543` spec §5.3.

## Open

- Nothing this card can settle.
