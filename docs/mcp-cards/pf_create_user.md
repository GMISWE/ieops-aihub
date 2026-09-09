# pf_create_user — contract card

```json
{
  "tool": "pf_create_user",
  "description_sha256": "2d565545d332fd2ab0e3371e540b6186b3cfef2157335371e0efa76758e521c6",
  "input_schema_sha256": "9f0307d6a3a4d8f731b4e09738174327c43bfda16444eed0f90c549b0470f96d",
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
      "required": false,
      "enum": [
        "admin",
        "writer"
      ]
    },
    "user_type": {
      "type": "string",
      "required": false,
      "enum": [
        "human",
        "machine"
      ]
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
| `user_type` | string | no | enum `human` or `machine`, default `human`; a machine user's email is generated, not supplied |
| `role` | string | no | enum `writer` or `admin`, default `writer` — the GLOBAL role, not a project member role |
| `email` | string | no | required for human users; auto-generated for machine users |
| `author_aliases` | array | no | git author aliases for this user |

`email` is the interesting row: it is **not** in the `required` array, and the
description says it is required for human users. That is another conditional
requirement a flat `required` list cannot express, so it is prose and the server
enforces it.

`user_type` and `role` were prose — "User type: human or machine" — until
`aihub#463` made them enums drawn from `domain.UserTypeList` and
`domain.UserGlobalRoleList`, the same values the server refuses to insert, so the
published set and the accepted set are one value rather than two that agree today.
That is `aihub#396`'s ruling for `pf_create_work_item`'s `priority` and `source`,
applied to the second table it fits.

⚠️ The enum is what a caller is OFFERED, not what stops it. `aihub#396` recorded
that the go-sdk validates an enum before the handler runs; on go-sdk v1.6.0 that
holds only for the generic `AddTool[In, Out]` path, and polyforge registers through
the untyped `(*mcp.Server).AddTool`, whose `callTool` hands the request straight to
the handler with no schema step. Measured in
`internal/mcp/create_user_vocab_test.go`: `role: "maintainer"` still arrives in the
`POST` body. What changed is the answer at the far end — see hop 4.

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
- `aihub#463`: an out-of-vocabulary `user_type` or `role` is now a **400** naming the
  field, the rejected value and the legal set, refused before the `INSERT` by
  `internal/domain/user_fields.go` (`ValidateUserType`) from
  `internal/server/router.go` (`handleCreateUser`). It used to reach the `INSERT`,
  violate the CHECK in `internal/db/migrations/0001_initial.sql` and come back as
  `500 INTERNAL_ERROR failed to create user` — that handler discards the pgx error,
  so not even the SQLSTATE survived, and the caller was told to retry something that
  can never succeed.

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

- ~~`pf_update_user` still publishes `role` as prose and `handleUpdateUser` still
  validates nothing, so an illegal role on the PATCH path remains a 500 from the
  CHECK.~~ **Closed by `aihub#496` (2026-09-09), and the bullet was wrong on its
  facts.** `handleUpdateUser` did validate `role` — inline, since the original
  round-2a commit — so an illegal role was answered `400 BAD_REQUEST "role must be
  writer or admin"`, **never a 500**; it was measured, not re-read. The real defect
  the bullet had mis-described was twofold: that 400 carried no `details`, so it
  named neither the rejected value nor the legal set, and the check was a **third
  hand-typed copy** of a vocabulary already held by the CHECK in
  `0001_initial.sql` and by `domain.userGlobalRoles`. Both are fixed: the handler
  now calls `domain.ValidateUserGlobalRole`, and `pf_update_user.role` publishes
  the enum. See `internal/server/update_user_vocab_test.go`.
- `email` is still conditionally required in prose, which a flat `required` list
  cannot express. Unchanged by `aihub#463`, which wrapped 2026-09-08, and not a
  vocabulary question; re-checked the same day.
