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

- Returns every user row: id, display name, email, `user_type`, global role and
  author aliases.
- The **global** role vocabulary here is `writer | admin`, which is a **third**
  vocabulary distinct from both the member roles (`viewer|writer|maintainer`) and the
  project owner column. A card that did not say so would leave a reader assuming
  `role` means the same thing everywhere it appears.
- `author_aliases` is how a git commit author maps to a user, which is what makes it
  the field that matters most in this response and the one that could not be changed
  from MCP at all until `pf_update_user` published it.

## hop 5 — what comes back

`jsonResult`, no projection; the single top-level key is `items`. The corpus record
above spans 12 calls, all strict JSON, with no errors.

## Policy

- **§6.2 T2-17** — the two-vocabulary role split must be named in the cards. This
  card names the third one: the global role, which is neither a member role nor the
  owner column.
- **§6.2 T2-18** — `user_id`-shaped parameters elsewhere must say which identity they
  filter; the identities they can name are the rows this tool returns.

## Open

- Nothing this card can settle.
