# pf_reinforce_memory — contract card

```json
{
  "tool": "pf_reinforce_memory",
  "description_sha256": "9954803dee8b3d7e57f17d935200e67175ab432db2335542cb4cbd19c0deb5b5",
  "input_schema_sha256": "2bd5f7f5716af00a37f2178578c19a3d949c908f811491915a47b3d1de31f2bb",
  "params": {
    "additional_context": {
      "type": "string",
      "required": true
    },
    "memory_id": {
      "type": "string",
      "required": true
    },
    "strength_delta": {
      "type": "number",
      "required": false
    },
    "work_item_id": {
      "type": "string",
      "required": true
    }
  },
  "response_keys_observed": [
    "activation_count",
    "base_strength",
    "memory_id"
  ],
  "hop4_coverage": "written"
}
```

## hop 0-1 — what the caller is told

Four parameters, three required — and `work_item_id` being required here is the
whole story of `aihub#325`.

| param | type | required | hop 1 promise |
|---|---|---|---|
| `memory_id` | string | yes | "Memory ID" |
| `additional_context` | string | yes | additional context for the memory |
| `work_item_id` | string | yes | "Work item ID (for credential injection)" |
| `strength_delta` | number | no | strength delta |

The parenthetical on `work_item_id` understates it. The server VERIFIES the attempt
credentials against that work item and writes it into the reinforcement's
provenance, so it is not merely the key that names a state file.

## hop 2-3 — what leaves this process, and what binds it

`internal/mcp/tools_memory.go` (`buildReinforceMemoryBody`) renders the arguments
and the resolved state file into the body of `PATCH /v1/memories/<id>/reinforce` via
`pkg/client/client.go` (`ReinforceMemory`), bound by
`internal/server/routes_memory.go` (`handleReinforceMemory`).

- `memory_id` is **absent from the body on purpose** — it is the path segment. That
  is asserted on the real request URL by `internal/mcp/memory_tools_wire_test.go`
  rather than exempted on trust: "it goes in the path" is a claim, and an unchecked
  claim is how a published parameter goes missing.
- `strength_delta` is forwarded only when present.
- **This tool declared `work_item_id` required, refused the call without one, and
  then built a body that did not contain it.** Every non-methodology reinforce
  answered `400 work_item_id is required when attempt_id/session_secret are
  provided`, because the handler always sends credentials from the state file and the
  server's gate demands the work item whenever credentials are present.
  `methodology.*` memories take the other branch, which binds to the TARGET memory's
  own work item — which is why `pf_save_artifact` traffic was unaffected and nobody
  noticed.

## hop 4 — what it actually does

- Appends the context to the memory's reinforcement list with `from_wi` provenance,
  and adjusts strength by `strength_delta` within the server's clamp. Since
  `aihub#433` that clamp reads `internal/domain/memory.go` (`MinBaseStrength`) and
  (`MaxBaseStrength`) rather than two literals that happened to agree with them.
- **Reinforce still CLAMPS where the create path REJECTS, and that asymmetry is
  deliberate**: this call applies a delta to a stored value, so a sum outside the
  range is arithmetic rather than a stated intent, while a caller-supplied
  `base_strength` outside the range is a statement the server can refuse.
- **Sending credentials without the work item they belong to is the one combination
  the gate rejects outright**, which is why the parameter is required rather than
  optional-with-a-default.

## hop 5 — what comes back

`jsonResult`, no projection. The corpus record above is the union of top-level keys
real callers have been handed — over **6 calls, 5 of them errors (83.33%, the highest
rate in the whole census)**. One of those five is the defect above, verbatim:
`docs/audits/aihub-412-corpus-facts/error-taxonomy.md` records
`400 BAD_REQUEST — work_item_id is required when attempt_id/session_secret are
provided` against this tool. The other four are ordinary caller errors (a missing
`memory_id`, a missing `additional_context`, a missing `work_item_id`, an absent state
file), so the rate is not attributable to the defect alone.

## Policy

- **§6.1 T1-3 (owner ruling) — LANDED** (`aihub#433`). The reinforce clamp's bounds
  were named as the authority for the range `pf_remember` should validate against;
  both now read the same exported constants, taken from the column's own DDL.
- **§6.2 T2-3** — one status code for "invalid attempt credential" across all tools;
  this is one of the credentialed memory paths that classification has to cover.

## Open

- **`aihub#459`** — the column is `SMALLINT` while Go and the schema say `number`, so
  a fractional `strength_delta` that lands the sum between two integers still cannot be
  stored as stated. Open, and shared with `pf_remember`.
