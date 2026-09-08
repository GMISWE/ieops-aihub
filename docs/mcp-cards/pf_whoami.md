# pf_whoami — contract card

```json
{
  "tool": "pf_whoami",
  "description_sha256": "0d9503a3c408ba30f8246a8f7823cd9c3198535e3c0925454ad18ce16dea1aa2",
  "params": {},
  "response_keys_observed": [
    "api_key_id",
    "display_name",
    "email",
    "project_roles",
    "projects",
    "role",
    "server_version",
    "user_id",
    "user_type"
  ],
  "hop4_coverage": "written"
}
```

## hop 0-1 — what the caller is told

Zero parameters — `emptyObjectSchema()`. The description promises three things:
caller identity, project roles, and accessible projects.

There is no parameter table because there is nothing to tabulate, and that is
worth stating rather than leaving blank: a tool with no inputs cannot have a hop-1
or hop-2 defect, so every risk here is on the response side.

## hop 2-3 — what leaves this process, and what binds it

`internal/mcp/tools_lifecycle.go` (`registerLifecycleTools`) makes **two** HTTP
calls, not one:

1. `pkg/client/client.go` (`WhoAmI`) → `GET /v1/users/me`, bound by
   `internal/server/router.go` (`handleWhoami`).
2. `pkg/client/client.go` (`ListProjects`) → `GET /v1/projects`, bound by
   `internal/server/routes_projects.go` (`handleListProjects`).

The second is **best-effort**: if it fails, the tool still answers with the whoami
half and simply omits `projects`. So an absent `projects` key means "the second
call failed", not "you have access to none".

## hop 4 — what it actually does

The `projects` array is computed **in this process**, not by the server. For each
visible project the handler derives `relation` (`owner` | `member` | `public`) and
`role`, and the derivation has two properties a caller should know:

- **Admin and owner short-circuit to `owner`/`owner`.** `owner` is not a member
  role — it is the `projects.owner_user_id` column — so the `role` this tool
  reports is drawn from a **wider vocabulary** than the one `pf_update_project`'s
  `members` accepts. §6.2 T2-17 is the ruling that this two-vocabulary split stays
  and must be **named** here, because only the value a caller cannot send is
  visible to an LLM.
- **The member scan is a SECOND derivation of a fact the server also derives.**
  `internal/server/middleware.go` (`roleForUserInMembers`) computes the same
  "caller's role out of `projects.members`" for `project_roles`. Both were fixed
  independently (`aihub#312` here, `aihub#315` there) and nothing makes them agree;
  the server package gates its own copy with a test requiring one derivation, and
  nothing gates the pair across the mcp/server boundary. Measured 2026-09-02 against
  eight call sites: 8/8 agree. One shape outside that set still differs — a member
  whose `role` is not a string — and it is a payload difference, not an
  authorization one.

## hop 5 — what comes back

Passed through by `jsonResult` with `projects` added. No slim function, so nothing
the server sends is dropped. The corpus record above is the union of keys real
callers have been handed.

## Policy

- **§6.2 T2-17** — keep the two-vocabulary role split and name it in the contract
  cards. Named above: `role` here can be `owner`, which `pf_update_project` will
  refuse.
- **§6.2 T2-8** — the member-role vocabulary is `viewer | writer | maintainer`, and
  the two contradicting `roleLevel` ladders that ruling was about are now one shared
  map (`aihub#443`, LANDED). A `maintainer` reported here used to be a role one of the
  two ladders scored at 0.
- **§6.2 T2-11** — "scoped to visible projects" has **two** implementation shapes.
  A new resolver must be told which one it inherits; this tool inherits
  `ListProjects`' shape (public + member + owned), not the work-item scoping rule.

## Open

- **§6.4 item 1** — whether any live `projects.members` row actually holds
  `role:"maintainer"` needs a DB read nobody has made. Migration `0013` mapped
  `maintainer` to `writer` on backfill, so such a row can only have arrived from a
  later members write. The fix is right either way; the urgency is unmeasured.
- The mcp/server duplication above is held together by a comment, not a gate. That
  is stated in the source and is not closed by this card.
