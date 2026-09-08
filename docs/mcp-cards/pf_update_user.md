# pf_update_user — contract card

```json
{
  "tool": "pf_update_user",
  "description_sha256": "3e110327081ad0220cea2e0391bd37a68435a2ceb455aadf9692419352f17e31",
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
      "required": false
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
| `role` | string | no | updated global role: `writer` or `admin` |
| `author_aliases` | array | no | **REPLACES the whole list**; omit to keep, send `[]` to clear |

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
- There is no delete-user tool on this surface. A user is created and updated; the
  only removal available anywhere here is `pf_update_project`'s membership write.

## hop 5 — what comes back

`jsonResult`, no projection. **`response_keys_observed` is `null`** — the
`aihub#412` corpus holds no record for this tool. That is expected: the parameter
that makes it useful was unpublished for the whole corpus window, so its absence is
evidence about the gap rather than about the tool.

## Policy

- **§6.1 T1-9** — a bound-and-forwarded field with no published name is the mirror of
  a published field nothing reads, and the same rule governs both: withdraw, fix, or
  file. Here the fix was to publish it.
- **§6.2 T2-17** — the global role vocabulary is the third one; naming it in the
  cards is what that ruling asks for.

## Open

- Nothing this card can settle. The null corpus record is explained above rather than
  treated as a disuse signal.
