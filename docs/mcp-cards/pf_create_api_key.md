# pf_create_api_key — contract card

```json
{
  "tool": "pf_create_api_key",
  "description_sha256": "ebca46117cb99f0b6e4b50bb047e56fe9d1fda2bd1bd7c65a090ccb95388277a",
  "input_schema_sha256": "270eff7ee7f875b6a47bd696dd8a2b59512cccb0ce6e9d78f09a71eb62bb0b00",
  "params": {
    "name": {
      "type": "string",
      "required": true
    },
    "project_scope": {
      "type": "string",
      "required": false
    },
    "user_id": {
      "type": "string",
      "required": true
    }
  },
  "response_keys_observed": [
    "key_id",
    "raw_key"
  ],
  "hop4_coverage": "written"
}
```

## hop 0-1 — what the caller is told

Three parameters, two required, and a handling instruction.

| param | type | required | hop 1 promise |
|---|---|---|---|
| `user_id` | string | yes | user to create the key for |
| `name` | string | yes | descriptive name for the key |
| `project_scope` | string | no | optional project name restricting the key |

"Create an API key for a user (**admin only**). **Returns the plain key once — store
it securely.**"

`user_id` here names the key's **owner**, which is one of the three identities
§6.2 T2-18 says every `user_id`-shaped parameter must disambiguate. On this tool it
is unambiguous because there is only one user in the operation; on the filtering
tools it is not.

## hop 2-3 — what leaves this process, and what binds it

`internal/mcp/tools_users.go` (`registerUserTools`) checks both required values, then
copies every argument **except `user_id`** into the body of
`POST /v1/admin/users/<user_id>/keys` via `pkg/client/client.go` (`CreateAPIKey`),
bound by `internal/server/router.go` (`handleCreateAPIKey`), under the admin group.

`user_id` is the path segment; `name` and `project_scope` are body fields.

## hop 4 — what it actually does

- Mints a key, stores its hash, and returns the plaintext **once**.
- `project_scope` restricts the key to one project. That scoping interacts with the
  idempotency cache, whose key includes the API key id — so two keys for the same
  user are two idempotency namespaces.
- A scoped key still authenticates; what changes is what it can reach. Nothing in the
  response says which scope was applied beyond what the caller sent.

## hop 5 — what comes back

`jsonResult`, no projection: the observed keys are `key_id` and `raw_key`. Six calls
in the corpus window, no errors.

🔴 `raw_key` is a live credential in a non-projected response, so it lands in the
calling agent's transcript. Same property as `pf_rotate_identifier`, and the same
reason it is worth a card line: the blast radius of a leaked credential is the whole
bundle it was captured in.

## Policy

- **§6.2 T2-18** — state on each `user_id`-shaped parameter which of the three
  identities it filters. Here it is the key's owner, stated above.
- **§6.1 T1-5** — no projection at hop 5, which for this tool is a cost.

## Open

- Whether a secret-returning tool should redact at hop 5 is not covered by any
  adjudicated row. Recorded, not decided — the same open item `pf_rotate_identifier`
  carries.
