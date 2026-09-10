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
description says it is required for human users — both halves read off a real session
by `internal/mcp/user_admin_surface_test.go`
(`TestCreateUserPublishesEmailAsProseRatherThanRequired`), with `display_name` as the
control so an unparsed `required` list cannot read as a correct absence. That is
another conditional requirement a flat `required` list cannot express, so it is prose
and the server enforces it: `internal/server/user_admin_write_shape_test.go`
(`TestCreateUserEmailIsRequiredForHumansAndGeneratedForMachines`) refuses a human with
no email and lets a machine through, asserted as the pair because either leg alone is
satisfied by a handler that refuses everybody or one that requires nothing.

`user_type` and `role` were prose — "User type: human or machine" — until
`aihub#463` made them enums drawn from `domain.UserTypeList` and
`domain.UserGlobalRoleList`, the same values the server refuses to insert, so the
published set and the accepted set are one value rather than two that agree today —
compared against the domain lists rather than against a literal by
`internal/mcp/create_user_vocab_test.go`
(`TestCreateUserVocabulariesArePublishedAsEnums`), whose far end is
`internal/domain/user_fields_test.go` (`TestUsersVocabulariesMatchTheMigrations`),
parsing the CHECK out of the SQL.
That is `aihub#396`'s ruling for `pf_create_work_item`'s `priority` and `source`,
applied to the second table it fits.
<!-- prose-only: because=external-state -->

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
  that is `pf_create_api_key`, a separate call, and the `no_credential_is_returned`
  leg of `internal/server/user_admin_write_shape_test.go`
  (`TestCreateUserResponseIsTheHandlersOwnProjection`) refuses any key-, token- or
  secret-shaped name in the answer this handler builds.
- `author_aliases` set here is **stored and read nowhere** — measured 2026-09-10 by
  `internal/mcp/user_admin_surface_test.go`
  (`TestAuthorAliasesIsWrittenAndNeverRead`), which censuses three writers and no
  reader of `users.author_aliases` across `internal/` and `pkg/`, uses `display_name`
  as the positive control for its read-detector, and also refuses a published
  description that claims attribution. So no code path in aihub maps a git commit
  author to a user by it; commit records take their author from the authenticated
  caller instead. 🔴 This bullet used to read "is how commits attribute to this
  user", and `pf_update_user.md` said the same; neither was true of this tree.
  The field is therefore the mirror of that card's §6.1 T1-9 case — a published field
  nothing reads — and it is recorded rather than fixed here, because wiring a reader
  is a behaviour change.
- Until `aihub#425` published the same field on `pf_update_user`, aliases could be set
  at creation and then **never changed from MCP again** — so the one case that could
  not be fixed was the one that matters: an alias that was wrong, or an author who
  acquired a new email.
- `machine` users get a generated email, which is what lets an agent identity exist
  without a mailbox: the `machine_without_email_proceeds` leg of
  `internal/server/user_admin_write_shape_test.go`
  (`TestCreateUserEmailIsRequiredForHumansAndGeneratedForMachines`) requires a machine
  user with no email to reach the `INSERT` where a human is refused. ⚠️ The generated
  address's exact spelling is not pinned anywhere: it is built immediately before the
  insert, so no answer carries it, and the arm above holds that a machine user needs
  no mailbox rather than what the mailbox is called.
- `aihub#463`: an out-of-vocabulary `user_type` or `role` is now a **400** naming the
  field, the rejected value and the legal set, refused before the `INSERT` by
  `internal/domain/user_fields.go` (`ValidateUserType`) from
  `internal/server/router.go` (`handleCreateUser`) — driven through the handler by
  `internal/server/create_user_vocab_test.go`
  (`TestCreateUser_IllegalVocabularyRejectedBeforeDB`,
  `TestCreateUser_IllegalUserTypeIsNotReportedAsAnEmailProblem`,
  `TestCreateUser_DefaultsSurviveValidation`) and over the answer's contents by
  `internal/domain/user_fields_test.go`
  (`TestUserVocabularyErrorsCarryTheLegalValues`). It used to reach the `INSERT`,
  violate the CHECK in `internal/db/migrations/0001_initial.sql` and come back as
  `500 INTERNAL_ERROR failed to create user` — that handler discards the pgx error,
  so not even the SQLSTATE survived, and the caller was told to retry something that
  can never succeed.

## hop 5 — what comes back

`jsonResult` — **this process** projects nothing, but the server does. The corpus
record above spans 6 calls; the observed keys are `display_name`, `email`, `id`,
`role`, `user_type`.
<!-- prose-only: because=measurement -->

🔴 `author_aliases` is absent from that list because the HANDLER drops it rather than
because those six calls set none: `internal/server/router.go` (`handleCreateUser`)
answers with a hand-built five-key map instead of the inserted row, so no
`pf_create_user` response can carry `author_aliases`, `created_at` or `updated_at`
however many callers set them — the response literal's key set is compared against
aihub#412's corpus record in both directions, and the written-but-unreturned column
asserted directly, by `internal/server/user_admin_write_shape_test.go`
(`TestCreateUserResponseIsTheHandlersOwnProjection`). This card used to reason the
other way, from "a non-projecting response" to a conclusion about caller behaviour;
that inference was wrong, and it was wrong in the direction that reads as
reassurance.

## Policy

- **§6.1 T1-4** — the ruling is to gate the published schema against **DB CHECKs
  rather than fields**; `user_type` and `role` are exactly the shape that produces a
  fourth instance if fixed one field at a time, which is why the ruling is applied
  per-CHECK and enumerated as such by `internal/domain/user_fields_test.go`
  (`TestEveryCheckedUserColumnIsValidatedInGo`), walking every CHECK-constrained
  `users` column and requiring a recorded validator or a recorded exemption for each
  — a per-field arm would have no population to walk.
- **§6.2 T2-17** — the global role vocabulary here (`writer|admin`) is a third
  vocabulary, distinct from member roles and from the owner column, which
  `internal/domain/role_vocabularies_test.go`
  (`TestTheRoleVocabulariesAreDistinctAndOwnerIsInNeither`) holds as a property of
  the live sets: the two differ, `owner` is in neither and has no `RoleLevel` rung,
  `maintainer` is member-only and `admin` is global-only.

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
  now calls `domain.ValidateUserGlobalRole`
  (`internal/server/update_user_vocab_test.go`,
  `TestUpdateUser_IllegalRoleNamesTheField`), and `pf_update_user.role` publishes
  the enum, bound to the domain list by `internal/mcp/user_admin_surface_test.go`
  (`TestUpdateUserRoleEnumIsTheDomainVocabulary`). See
  `internal/server/update_user_vocab_test.go`.
- `email` is still conditionally required in prose, which a flat `required` list
  cannot express — the pair of arms named in hop 0-1
  (`TestCreateUserPublishesEmailAsProseRatherThanRequired`,
  `TestCreateUserEmailIsRequiredForHumansAndGeneratedForMachines`) now hold the prose
  and the enforcement together, so the two cannot drift apart while the requirement
  stays unexpressible in the schema. Unchanged by `aihub#463`, which wrapped
  2026-09-08, and not a vocabulary question; re-checked the same day.
