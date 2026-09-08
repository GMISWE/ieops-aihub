# pf_create_user — contract card

```json
{
  "tool": "pf_create_user",
  "description_sha256": "2d565545d332fd2ab0e3371e540b6186b3cfef2157335371e0efa76758e521c6",
  "params": {
    "author_aliases": {
      "type": "array",
      "required": false
    },
    "display_name": {
      "type": "string",
      "required": true
    },
    "email": {
      "type": "string",
      "required": false
    },
    "role": {
      "type": "string",
      "required": false
    },
    "user_type": {
      "type": "string",
      "required": false
    }
  },
  "response_keys_observed": [
    "display_name",
    "email",
    "id",
    "role",
    "user_type"
  ],
  "hop4_coverage": "written"
}
```

## hop 0-1 — what the caller is told

Five parameters, one required. "Create a new user (**admin only**)."

| param | type | required | hop 1 promise |
|---|---|---|---|
| `display_name` | string | yes | human-readable display name |
| `user_type` | string | no | `human` or `machine`, default `human` |
| `role` | string | no | global role: `writer` or `admin`, default `writer` |
| `email` | string | no | required for human users; auto-generated for machine users |
| `author_aliases` | array | no | git author aliases for this user |

`email` is the interesting row: it is **not** in the `required` array, and the
description says it is required for human users. That is another conditional
requirement a flat `required` list cannot express, so it is prose and the server
enforces it.

Neither `user_type` nor `role` is a `propEnum`, so an out-of-vocabulary value is not
refused before the handler runs — unlike `pf_create_work_item`'s `priority` and
`source`, which were converted from prose to enums for exactly that reason.

## hop 2-3 — what leaves this process, and what binds it

`internal/mcp/tools_users.go` (`registerUserTools`) checks `display_name` locally and
passes the **whole argument map** to `pkg/client/client.go` (`CreateUser`) →
`POST /v1/admin/users`, bound by `internal/server/router.go` (`handleCreateUser`),
under the admin group.

## hop 4 — what it actually does

- Creates the user row and returns it. The response does **not** include an API key:
  that is `pf_create_api_key`, a separate call.
- `author_aliases` set here is how commits attribute to this user. Until `aihub#425`
  published the same field on `pf_update_user`, aliases could be set at creation and
  then **never changed from MCP again** — so the one case that could not be fixed was
  the one that matters: an alias that was wrong, or an author who acquired a new
  email.
- `machine` users get a generated email, which is what lets an agent identity exist
  without a mailbox.

## hop 5 — what comes back

`jsonResult`, no projection. The corpus record above spans 6 calls; the observed keys
are `display_name`, `email`, `id`, `role`, `user_type` — note that `author_aliases`
is not among them, which under a non-projecting response means those six calls did
not set one rather than that the field is dropped.

## Policy

- **§6.1 T1-4** — the ruling is to gate the published schema against **DB CHECKs, not
  fields**; `user_type` and `role` are exactly the shape that produces a fourth
  instance if fixed one field at a time.
- **§6.2 T2-17** — the global role vocabulary here (`writer|admin`) is a third
  vocabulary, distinct from member roles and from the owner column.

## Open

- Whether `user_type` and `role` should be `propEnum` is the same question
  `aihub#396` answered "yes" for the work-item fields. No adjudicated row extends
  that answer to this tool, so it is recorded rather than asserted.
