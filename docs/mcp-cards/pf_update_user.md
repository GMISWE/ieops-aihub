# pf_update_user — contract card

```json
{
  "tool": "pf_update_user",
  "description_sha256": "3e110327081ad0220cea2e0391bd37a68435a2ceb455aadf9692419352f17e31",
  "input_schema_sha256": "1a527e2b4303c9a95caa406ae5a56529190f3eb0df038267fc5f04ca36aa961b",
  "params": {
    "author_aliases": {
      "type": "array",
      "required": false
    },
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

Four parameters, one required. "Update a user's display name, role or git author
aliases (**admin only**)."

| param | type | required | hop 1 promise |
|---|---|---|---|
| `id` | string | yes | "User ID" |
| `display_name` | string | no | updated display name |
| `role` | string | no | **enum** — the global role, NOT a project member role |
| `author_aliases` | array | no | **REPLACES the whole list**; omit to keep, send `[]` to clear |

`role` was published as the prose sentence "Updated global role: writer or admin"
until `aihub#496` (2026-09-09), while the sibling create path had published the same
column as a real `enum` since `aihub#463`. One column, two tools, two different
answers to "what may I send" — and only one of them machine-readable. It is now a
`propEnum` sourced from `domain.UserGlobalRoleList()`, the same list the validator
uses, so the published set and the accepted set are one value.

⚠️ The enum constrains the **client**, not this process. `aihub#463` measured that
polyforge's untyped `(*mcp.Server).AddTool` registration runs no schema step, so
nothing here refuses an out-of-vocabulary value on the strength of the enum; it is
how a caller learns the set before spending a round trip. The refusal is hop 4.

`author_aliases` is the reason this card exists. The handler has always forwarded it
— it copies its whole args map into the body — and the server has always bound it, so
**only the schema was missing**, and the value was reachable solely by a caller who
guessed a name no schema mentions. The consequence was narrow and total: aliases
could be set at creation and never changed from MCP again, and aliases are how a git
commit author maps to a user.

The empty-array spelling is published because the server **distinguishes** it and
nothing else said so: the request struct binds `[]string` and the handler tests for
non-nil, so an omitted field leaves the column alone while `[]` decodes to a non-nil
empty slice and CLEARS it. Absent and empty are different instructions here, and a
caller who read "omit to keep current" nowhere would reasonably send `[]` meaning "no
change".

## hop 2-3 — what leaves this process, and what binds it

`internal/mcp/tools_users.go` (`registerUserTools`) copies every argument except `id`
into the body of `PATCH /v1/admin/users/<id>` via
`pkg/client/client.go` (`UpdateUser`), bound by `internal/server/router.go`
(`handleUpdateUser`), under the admin group.

That forwarding is measured rather than assumed:
`internal/mcp/update_user_param_publication_test.go` drives the tool and asserts the
argument reaches the PATCH body.

## hop 4 — what it actually does

- Sets only the fields present in the body; the alias list is replaced wholesale
  when present.
- `role` writes the **global** role — `writer | admin` — not a project member role.
  Changing it does not touch `projects.members`.
- An out-of-vocabulary `role` is refused **before** the `UPDATE`, with a `400`
  naming the field, echoing the value and listing the legal set:

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
  `0001_initial.sql` and by `domain.userGlobalRoles`. It now calls
  `domain.ValidateUserGlobalRole`, so all three cannot drift apart.

  Omitting `role` is still distinct from sending it: the field binds as `*string`
  and validation runs inside the nil check, so a display-name-only update is not
  judged against the vocabulary. `internal/server/update_user_vocab_test.go` holds
  both directions, DB-free through the nil-pool instrument.
- There is no delete-user tool on this surface. A user is created and updated; the
  only removal available anywhere here is `pf_update_project`'s membership write.

## hop 5 — what comes back

`jsonResult`, no projection. **`response_keys_observed` is `null`** — the
`aihub#412` corpus holds no record for this tool. That is expected: the parameter
that makes it useful was unpublished for the whole corpus window, so its absence is
evidence about the gap rather than about the tool.

The key is pinned anyway, just not here: `aihub#482`'s K10 in
`internal/mcp/card_response_keys_live_e2e_db_test.go` drives this tool against a
live server and holds the result to `ok`, declared in
`docs/mcp-cards/live-response-keys.json`.

## Policy

- **§6.1 T1-9** — a bound-and-forwarded field with no published name is the mirror of
  a published field nothing reads, and the same rule governs both: withdraw, fix, or
  file. Here the fix was to publish it.
- **§6.2 T2-17** — the global role vocabulary is the third one; naming it in the
  cards is what that ruling asks for.

## Open

- Nothing this card can settle. The null corpus record is explained above rather than
  treated as a disuse signal.
- Noted, not a defect this card owns: `user_type` is **not updatable** on this path
  at all. The request struct binds only `display_name`, `role` and `author_aliases`,
  so a `user_type` sent to `PATCH /v1/admin/users/:id` is silently dropped rather
  than refused. It is unreachable from MCP — this tool does not publish it — so the
  exposure is HTTP-only. Recorded 2026-09-09 by `aihub#496`, whose scope was `role`.
