# pf_rotate_identifier — contract card

```json
{
  "tool": "pf_rotate_identifier",
  "description_sha256": "c03ebc4e8f0f29519857adfb15b1d6b17bf5402086fe4099cafb559a1c2c21a0",
  "input_schema_sha256": "ebb939f2990b66ae8741abbcaefcd693d6794ac19a77fbc7ab9677a5cb963cb4",
  "params": {
    "name": {
      "type": "string",
      "required": true
    }
  },
  "response_keys_observed": null,
  "hop4_coverage": "written"
}
```

## hop 0-1 — what the caller is told

One parameter, and a description with a handling instruction in it.

| param | type | required | hop 1 promise |
|---|---|---|---|
| `name` | string | yes | "Project name" |

"Rotate the project identifier (bcrypt token). **Returns plain once — store it
securely.** Owner/admin only." Three facts in one line: what it rotates, that the
plaintext is unrecoverable afterwards, and who may call it.

## hop 2-3 — what leaves this process, and what binds it

`internal/mcp/tools_projects.go` (`registerProjectTools`) rejects an empty name and
calls `pkg/client/client.go` (`RotateProjectIdentifier`) →
`POST /v1/projects/<name>/rotate_identifier` with a **nil body**, bound by
`internal/server/routes_projects.go` (`handleRotateIdentifier`). The name is the path
segment; nothing else is sent.

The handler carries an explicit `NOTE: result contains plain token — do NOT log it`
at the call site, which is the only place that instruction can be enforced by a
reader.

## hop 4 — what it actually does

- Generates a new identifier, stores its bcrypt hash, and returns the plaintext once.
  The old identifier stops working immediately, so rotation is not additive.
- Authorization is owner or admin, checked server-side; a `maintainer` member cannot
  call it — worth stating rather than assuming, because `owner` is not a rung on the
  member ladder at all: `internal/domain/projects.go` (`checkProjectAccess`) settles
  ownership before any member role is ranked.
- `identifier_prefix` is the non-secret half that stays in the project row and is what
  `pf_list_projects` shows.

## hop 5 — what comes back

`jsonResult`, no projection. **`response_keys_observed` is `null`** — the
`aihub#412` corpus holds no record for this tool in its window, which for a rotation
operation is the expected shape rather than evidence of disuse.

The keys are pinned anyway, just not here: `aihub#482`'s K10 in
`internal/mcp/card_response_keys_live_e2e_db_test.go` drives this tool against a
live server and holds the result to `plain` and `prefix`, declared in
`docs/mcp-cards/live-response-keys.json`.

🔴 Because the response carries a secret and is not projected, **the plaintext lands
in whatever transcript the calling agent keeps.** That is a property of the tool
surface, not of any caller, and it is worth a card line: a leaked credential's blast
radius is the whole bundle it was captured in, not the one call.

## Policy

- **§6.2 T2-8** — who may call this is a role question, and the legal member roles
  are `viewer | writer | maintainer`; `owner` is a column.
- **§6.1 T1-5** — no projection, which here is a cost as well as a simplification.

## Open

- Whether a secret-returning tool should be projected or redacted at hop 5 is not
  covered by any adjudicated row. Recorded, not decided.
