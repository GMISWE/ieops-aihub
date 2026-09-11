# pf_update_user — contract card

```json
{
  "tool": "pf_update_user",
  "description_sha256": "f61b939af48f4c78145b09350dc9ad1e7bbfe742e7f7823e68e9760d6ebb05b9",
  "input_schema_sha256": "bd2cb1312890801cfb7cfa6667415becf3218b0faf87c373bb8498ed0332b5f3",
  "params": {
    "display_name": {
      "type": "string",
      "required": false
    },
    "id": {
      "type": "string",
      "required": true
    },
    "role": {
      "type": "string",
      "required": false,
      "enum": [
        "admin",
        "writer"
      ]
    }
  },
  "response_keys_observed": null,
  "hop4_coverage": "written"
}
```

## hop 0-1 — what the caller is told

Three parameters, one required. "Update a user's display name or role (**admin
only**)."

| param | type | required | hop 1 promise |
|---|---|---|---|
| `id` | string | yes | "User ID" |
| `display_name` | string | no | updated display name |
| `role` | string | no | **enum** — the global role, NOT a project member role |

`author_aliases` used to be the fourth row — published by `aihub#426`, WITHDRAWN
2026-09-10 by `aihub#587` (owner ruling); the history is under hop 4.

`role` was published as the prose sentence "Updated global role: writer or admin"
until `aihub#496` (2026-09-09), while the sibling create path had published the same
column as a real `enum` since `aihub#463`.
<!-- prose-only: because=history -->
One column, two tools, two different
answers to "what may I send" — and only one of them machine-readable.
<!-- prose-only: because=judgement --> It is now a
`propEnum` sourced from `domain.UserGlobalRoleList()`, the same list the validator
uses, so the published set and the accepted set are one value; the published enum is
compared against the domain list rather than against a literal by
`internal/mcp/user_admin_surface_test.go`
(`TestUpdateUserRoleEnumIsTheDomainVocabulary`), and domain's own side of that chain
is `internal/domain/user_fields_test.go` (`TestUsersVocabulariesMatchTheMigrations`),
which parses the CHECK out of the SQL.

⚠️ The enum constrains the **client**, not this process. `aihub#463` measured that
polyforge's untyped `(*mcp.Server).AddTool` registration runs no schema step, so
nothing here refuses an out-of-vocabulary value on the strength of the enum; it is
how a caller learns the set before spending a round trip — measured for THIS tool
rather than inferred from the sibling by `internal/mcp/user_admin_surface_test.go`
(`TestUpdateUserEnumDoesNotRefuseInProcess`), which first checks the value it sends is
outside the published enum, so the arm cannot go quietly vacuous the day the
vocabulary grows. The refusal is hop 4.

`author_aliases` was the reason this card exists, through two corrections.
It used to be BOUND but not published — the handler copies its whole args map
into the body, and the server bound the field — so the value was reachable only
by a caller who guessed a name no schema mentions, and `aihub#426` published it.
What `aihub#543` then measured (2026-09-10) is that the column the field fed has
no reader: no SQL statement anywhere in `internal/` or `pkg/` selects
`users.author_aliases` — the census is `TestAuthorAliasesIsNeitherWrittenNorRead`
— and commit records take their author from the authenticated caller, so the
"aliases drive attribution" sentence this card and `pf_create_user.md` both
carried was never true of this tree. That put the field
in the §6.1 T1-9 shape (a published field nothing reads: withdraw, fix, or
file), and the `aihub#587` owner ruling was to WITHDRAW: the parameter is gone
from both schemas, `handleUpdateUser` no longer binds it, and all three write
sites are removed — the zero-write, zero-read census with `display_name` as the
positive control in both directions is
`internal/mcp/user_admin_surface_test.go`
(`TestAuthorAliasesIsNeitherWrittenNorRead`).

What a stale caller experiences is disclosure rather than silence: a call still
sending `author_aliases` gets it named in the response's
`request_adjusted.unknown_params` entry, held for both user tools by
`internal/mcp/update_user_param_publication_test.go`
(`TestAuthorAliasesWithdrawalIsDisclosed`) — and a PATCH whose only payload is
the withdrawn name is refused with 400 "no fields to update" rather than
answered as a success for a write that did not happen
(`TestWithdrawnAliasArgAloneIsRefusedNotSilentlyHonoured`, same file). The
empty-array-clears spelling the old description published is gone with the
parameter: `[]` now lands in the same dropped-field bucket as every other
unbound name, held at the handler boundary by
`internal/server/user_admin_write_shape_test.go`
(`TestUpdateUserWritesTwoFieldsRefusesUserTypeAndDropsTheRest`,
`empty_alias_list_is_not_a_write`).

## hop 2-3 — what leaves this process, and what binds it

`internal/mcp/tools_users.go` (`registerUserTools`) copies every argument except `id`
into the body of `PATCH /v1/admin/users/<id>` via
`pkg/client/client.go` (`UpdateUser`), bound by `internal/server/router.go`
(`handleUpdateUser`), under the admin group.

That forwarding is measured rather than assumed:
`internal/mcp/update_user_param_publication_test.go`
(`TestAuthorAliasesWithdrawalIsDisclosed`, `the_name_still_reaches_the_wire`)
drives the tool with the withdrawn name and asserts it still reaches the PATCH
body — aihub#389 phase 1 is report-not-strip, so the wholesale copy is the
mechanism by which ANY argument, published or not, crosses the wire, and the far
end binds only what its request struct names.

## hop 4 — what it actually does

- Sets only the two updatable fields present in the body — `display_name` and
  `role`, since `aihub#587` — and any unbound name, `author_aliases` included, is
  silently dropped by the bind so the request behaves exactly as though it
  carried nothing, held leg by leg (both aliases spellings, the empty body, and
  a `display_name` write control) by
  `internal/server/user_admin_write_shape_test.go`
  (`TestUpdateUserWritesTwoFieldsRefusesUserTypeAndDropsTheRest`).
- `user_type` is the one name with a third verdict (`aihub#530`, 2026-09-11):
  bound only to be **refused**, a `400` with `details.field = "user_type"`
  echoing the value, before the `UPDATE` and whole-request — a rename riding
  beside it does not half-succeed — held leg by leg (beside a rename, alone,
  the `null` spelling counting as absent, and a two-field write control) by
  `internal/server/update_user_user_type_test.go`
  (`TestUpdateUserUserTypeIsRefusedNotDropped`), with the no-write half staying
  on the `user_type_is_not_a_write` leg of
  `TestUpdateUserWritesTwoFieldsRefusesUserTypeAndDropsTheRest`.
- `role` writes the **global** role — `writer | admin` — not a project member role,
  a split held as a property of the two live vocabularies by
  `internal/domain/role_vocabularies_test.go`
  (`TestTheRoleVocabulariesAreDistinctAndOwnerIsInNeither`) and driven value by value
  out of the domain list by `internal/server/update_user_vocab_test.go`
  (`TestUpdateUser_LegalRolesReachTheDB`).
  Changing it does not touch `projects.members` —
  `TestUpdateUserWritesTheUsersTableOnly` pins the handler's one UPDATE to the
  `users` table.
- An out-of-vocabulary `role` is refused **before** the `UPDATE`, with a `400`
  naming the field, echoing the value and listing the legal set — asserted on the
  structured `details` rather than on a message substring by
  `internal/server/update_user_vocab_test.go`
  (`TestUpdateUser_IllegalRoleNamesTheField`), whose nil pool is what pins the
  refusal's POSITION above the `UPDATE` and not merely its verdict:

  ```json
  {"code":"BAD_REQUEST",
   "details":{"allowed":["admin","writer"],"field":"role","got":"maintainer"},
   "message":"role \"maintainer\" is not a legal value; allowed: [admin writer]"}
  ```

  🔴 The record this corrects (`pf_create_user.md`'s Open section) said this path
  "remains a 500 from the CHECK". Measured 2026-09-09 by `aihub#496`: it was
  **always a 400**. `handleUpdateUser` has carried an inline `!= "writer" && !=
  "admin"` check since the original round-2a commit. What it lacked was `details` —
  so the answer named neither the value nor the set — and being hand-typed it was a
  **third copy** of a vocabulary already held by the `users.role` CHECK in
  `0001_initial.sql` and by `domain.userGlobalRoles`.
  <!-- prose-only: because=history -->
  It now calls
  `domain.ValidateUserGlobalRole`, so all three cannot drift apart.

  Omitting `role` is still distinct from sending it: the field binds as `*string`
  and validation runs inside the nil check, so a display-name-only update is not
  judged against the vocabulary, which `internal/server/update_user_vocab_test.go`
  (`TestUpdateUser_OmittedRoleIsNotValidated`, `TestUpdateUser_LegalRolesReachTheDB`)
  holds in both directions, DB-free through the nil-pool instrument.
- There is no delete-user tool on this surface. A user is created and updated;
  nothing here deletes the row — neither a published tool nor a `pkg/client` route,
  censused with the api-key `DELETE` as its positive control by
  `internal/mcp/user_admin_surface_test.go` (`TestNothingOnThisSurfaceDeletesAUser`) —
  and the only removal of a user's project ACCESS is `pf_update_project`'s membership
  write, which has exactly one write path
  (`internal/domain/projects_members_removal_test.go`,
  `TestMembersUpdateHasOneWritePathAndOneRemovalCheck`); `pf_revoke_api_key`, the
  other removal on this surface, takes a credential rather than a membership.

## hop 5 — what comes back

`jsonResult`, no projection. **`response_keys_observed` is `null`** — the
`aihub#412` corpus holds no record for this tool, and the two are bound in both
directions by K7 in `internal/mcp/contract_cards_gate_test.go`
(`TestContractCardsMatchTheCorpusResponseKeys`): a card list with no corpus record is
`CORPUS_INVENTED`, and a card `null` with a record present is
`CORPUS_NULL_MASKS_RECORD`. That is expected: over the aihub#412 corpus window
this tool was not called — `author_aliases`, then its published draw, was
unpublished for the whole window, and `role` arrived only with `aihub#496` — so
the absence is evidence about the window rather than about the tool.
<!-- prose-only: because=measurement -->

The key is pinned anyway, just not here: `aihub#482`'s K10 in
`internal/mcp/card_response_keys_live_e2e_db_test.go` drives this tool against a
live server and holds the result to `ok`, declared in
`docs/mcp-cards/live-response-keys.json`.

## Policy

- **§6.1 T1-9** — a bound-and-forwarded field with no published name is the mirror of
  a published field nothing reads, and the same rule governs both: withdraw, fix, or
  file. This card has now been through both directions: `aihub#426` fixed the first
  shape by publishing the bound field, `aihub#543` measured that the published field
  had no reader (the second shape), and the `aihub#587` owner ruling resolved it by
  WITHDRAWING — parameter, binding and write sites together, 2026-09-10.
  <!-- prose-only: because=history -->
- **§6.2 T2-17** — the global role vocabulary is the third one; naming it in the
  cards is what that ruling asks for.

## Open

- Nothing this card can settle. The null corpus record is explained above rather than
  treated as a disuse signal.
- SETTLED 2026-09-11 by `aihub#530`, kept as history: this bullet used to record
  that a `user_type` sent to `PATCH /v1/admin/users/:id` was **silently dropped**
  rather than refused — first noted 2026-09-09 by `aihub#496`, whose scope was
  `role` — so beside any bound field the caller got `{"ok":true}` for an identity
  write that never happened. The verdict is now **refusal rather than binding**:
  `user_type` is identity — the create-time email invariant hangs off it, a
  machine mailbox generated and a human one required, held by
  `internal/server/user_admin_write_shape_test.go`
  (`TestCreateUserEmailIsRequiredForHumansAndGeneratedForMachines`), and this
  handler binds no email with which to keep that pairing consistent — and the
  tool still does not publish the field, so binding it would be the `aihub#419`
  BOUND_FIELD_UNPUBLISHED shape.
  The refusal is under hop 4, held by
  `internal/server/update_user_user_type_test.go`
  (`TestUpdateUserUserTypeIsRefusedNotDropped`); the exposure was HTTP-only and
  the refusal is not: the handler forwards every argument (hop 2-3 above), so an
  MCP caller sending the unpublished name now gets the server's 400 naming
  `user_type` as the tool error, with the `request_adjusted.unknown_params`
  disclosure appended as its own text block — `internal/mcp/unknown_params.go`
  attaches it to an error result as an additional block precisely because an
  unknown parameter is a plausible cause of the error the caller is looking at —
  instead of the old disclosure on an `{"ok":true}` answer, which named the
  field while the other fields wrote anyway.
