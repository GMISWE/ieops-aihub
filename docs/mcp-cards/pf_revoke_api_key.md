# pf_revoke_api_key — contract card

```json
{
  "tool": "pf_revoke_api_key",
  "description_sha256": "abd73839fdaf8f867c95b97d92d61d8c8c527f36da3748bc2bb059388585e358",
  "input_schema_sha256": "b5abe0f8385e1ba72f24749483994ef3386683292d4b81bcd3668c173a714262",
  "params": {
    "key_id": {
      "type": "string",
      "required": true
    },
    "user_id": {
      "type": "string",
      "required": true
    }
  },
  "response_keys_observed": [
    "ok"
  ],
  "hop4_coverage": "written"
}
```

## hop 0-1 — what the caller is told

Two parameters, both required. "Revoke an API key (**admin only**)."

| param | type | required | hop 1 promise |
|---|---|---|---|
| `user_id` | string | yes | user that owns the key |
| `key_id` | string | yes | API key id to revoke |

`user_id` names the key's **owner** — the same identity `pf_create_api_key`'s does,
and one of the three §6.2 T2-18 requires every `user_id`-shaped parameter to
disambiguate, which `internal/mcp/api_key_surface_test.go`
(`TestApiKeyToolsPublishTheOwnersIdentityUnderTheAdminGroup`) holds for both tools
together with the admin-group registration behind the notice. `key_id` is the id
`pf_create_api_key` returned; the plaintext is never accepted here, which is why
revocation does not need the secret, and
`internal/mcp/api_key_surface_test.go`
(`TestApiKeyWireShapePutsTheOwnerInThePathAndTheRestInTheBody`) drives a creation and
feeds its `key_id` straight into a revocation to hold that round trip.

## hop 2-3 — what leaves this process, and what binds it

`internal/mcp/tools_users.go` (`registerUserTools`) checks both values and calls
`pkg/client/client.go` (`RevokeAPIKey`) →
`DELETE /v1/admin/users/<user_id>/keys/<key_id>` with **no body**, bound by
`internal/server/router.go` (`handleRevokeAPIKey`) under the admin group.

Both parameters are path segments. There is no body, so there is no forwarding table
and nothing at hop 2 that can be dropped.

## hop 4 — what it actually does

- Marks the key revoked; requests carrying it stop authenticating.
- Because the idempotency cache key includes the API key id, revoking a key also
  ends the namespace its in-flight idempotency records lived in — a retry under a new
  key is a new namespace, not a replay.
- There is no un-revoke, and no listing of keys on this surface: `pf_list_users`
  returns users rather than their keys, so a caller must already hold the `key_id`
  from creation — `internal/mcp/api_key_surface_test.go`
  (`TestTheApiKeySurfaceOffersOnlyCreationAndRevocation`) enumerates every
  non-comment mention of `revoked_at` in the `.go` and `.sql` files under
  `internal/` — tests excluded, and `pkg/` outside the walk — and requires each one
  to be either the single writer or one of three recognised reads (the authentication
  null-check, a `/ui` projection alias, or the bearer struct field), with exactly one
  writer; it also censuses the registry for a third api-key tool and reads the users
  query's own column list.

## hop 5 — what comes back

`jsonResult`, no projection: the single observed key is `ok`, and the no-projection
half is driven by `internal/mcp/secret_response_relay_test.go`
(`TestSecretReturningToolsRelayTheServerResponseUnprojected`), which relays a key no
card lists — a one-key response is where a keep-list would be least visible. Three
calls in the corpus window, no errors.

## Policy

- **§6.2 T2-18** — the identity `user_id` names is stated above, and
  `internal/mcp/api_key_surface_test.go`
  (`TestApiKeyToolsPublishTheOwnersIdentityUnderTheAdminGroup`) requires the
  parameter and a non-empty description of it on both key tools.
- **§6.1 T1-9** — the description matches hop 4 in one line, so no disposition is
  needed.

## Open

- That there is no way to enumerate a user's keys from this surface is a gap in
  reachability, not a defect in this tool, and no adjudicated row covers it.
