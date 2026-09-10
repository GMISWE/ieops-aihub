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
| `user_id` | string | yes | user to create the key for — the row the mint lands on, per `TestCreateAPIKeyStoresOnlyTheHashAndTheMintedKeyAuthenticates` |
| `name` | string | yes | descriptive name for the key |
| `project_scope` | string | no | optional project name restricting the key |

"Create an API key for a user (**admin only**). **Returns the plain key once — store
it securely.**"

`user_id` here names the key's **owner**, which is one of the three identities
§6.2 T2-18 says every `user_id`-shaped parameter must disambiguate —
`internal/mcp/api_key_surface_test.go`
(`TestApiKeyToolsPublishTheOwnersIdentityUnderTheAdminGroup`) holds the quote above
against the live description as an equality, requires both key tools to publish a
non-empty `user_id` description, and reads the admin route group out of the router;
the identity itself is held on the WIRE, by
(`TestApiKeyWireShapePutsTheOwnerInThePathAndTheRestInTheBody`) under hop 2-3 below,
because a description can name an owner while the request filters on something else.
On this tool it is unambiguous because there is only one user in the operation; on
the filtering tools it is not.

## hop 2-3 — what leaves this process, and what binds it

`internal/mcp/tools_users.go` (`registerUserTools`) checks both required values, then
copies every argument **except `user_id`** into the body of
`POST /v1/admin/users/<user_id>/keys` via `pkg/client/client.go` (`CreateAPIKey`),
bound by `internal/server/router.go` (`handleCreateAPIKey`), under the admin group.

`user_id` is the path segment; `name` and `project_scope` are body fields, measured
on the recorded request by `internal/mcp/api_key_surface_test.go`
(`TestApiKeyWireShapePutsTheOwnerInThePathAndTheRestInTheBody`).

## hop 4 — what it actually does

- Mints a key, stores its hash, and returns the plaintext **once** — all three clauses driven against a real router and database by `internal/server/api_key_create_db_test.go` (`TestCreateAPIKeyStoresOnlyTheHashAndTheMintedKeyAuthenticates`): the stored row carries the hash and never the plaintext, and the minted key authenticates a real request.
- `project_scope` restricts the key to one project. That scoping interacts with the
  idempotency cache, whose key includes the API key id — so two keys for the same
  user are two idempotency namespaces.
- A scoped key still authenticates; what changes is what it can reach. Nothing in the
  response says which scope was applied beyond what the caller sent.

## hop 5 — what comes back

`jsonResult`, no projection: the observed keys are `key_id` and `raw_key`, and
`internal/mcp/secret_response_relay_test.go`
(`TestSecretReturningToolsRelayTheServerResponseUnprojected`) sends a key no card
lists and requires it to arrive, which is the half a keep-list would drop in
silence. Six calls in the corpus window, no errors.

🔴 `raw_key` is a live credential in a non-projected response, so it lands in the
calling agent's transcript — the relay itself is held verbatim, on a fixture value,
by `internal/mcp/secret_response_relay_test.go`
(`TestSecretReturningToolsRelayTheServerResponseUnprojected`). Same property as
`pf_rotate_identifier`, and the same reason it is worth a card line: the blast radius
of a leaked credential is the whole bundle it was captured in, and
`TestSecretReturningToolsRelayTheServerResponseUnprojected` drives the two tools
together over a population checked against every card carrying the phrase.

## Policy

- **§6.2 T2-18** — state on each `user_id`-shaped parameter which of the three
  identities it filters. Here it is the key's owner, stated above.
- **§6.1 T1-5** — no projection at hop 5, which for this tool is a cost.

## Open

- Whether a secret-returning tool should redact at hop 5 is not covered by any
  adjudicated row. Recorded rather than decided — the same open item
  `pf_rotate_identifier` carries, and `internal/mcp/secret_response_relay_test.go`
  (`TestBothSecretCardsRecordTheSameUnadjudicatedOpenItem`) requires both Open
  sections to keep carrying it.
