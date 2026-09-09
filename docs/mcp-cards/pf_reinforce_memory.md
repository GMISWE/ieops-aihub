# pf_reinforce_memory — contract card

```json
{
  "tool": "pf_reinforce_memory",
  "description_sha256": "9954803dee8b3d7e57f17d935200e67175ab432db2335542cb4cbd19c0deb5b5",
  "input_schema_sha256": "aa6137b1a82c539bdf66b8701b05f392bcac1822c6e63cc6c7d7d156b16c4a18",
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
| `strength_delta` | number | no | "Integer delta added to the memory's stored strength, then clamped to 1-5. A fractional delta is refused with a 400: strength is a whole number. The response reports the value actually stored." |

The parenthetical on `work_item_id` understates it. The server VERIFIES the attempt
credentials against that work item and writes it into the reinforcement's
provenance, so it is not merely the key that names a state file.

`strength_delta`'s promise was the bare words "strength delta" until `aihub#475`.
True, and useless: it named the units without naming the granularity, so nothing
told a caller that `0.5` was a no-op — and the 200 body reported the untruncated
arithmetic, so the call looked like it had worked. That text stated the granularity
and pointed at the response, and stopped short of saying a fractional delta was
refused or rounded, because it was neither.

**`aihub#459` settled it on 2026-09-09: refused.** So the description no longer
explains truncation, and the removal is deliberate rather than tidying — a caller
told "a delta under 1 usually changes nothing" will still send `0.5` and then
wonder, while one told it is refused cannot. The truncation has not gone anywhere;
it has stopped being reachable through this parameter, and a description that keeps
explaining an unreachable mechanism teaches the wrong model.

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
- **The clamp is not the last thing that touches the value, and until `aihub#475`
  the code said it was.** `memories.base_strength` is `SMALLINT`, so pgx encodes the
  float64 through its int2 codec, which truncates toward zero and returns no error —
  measured at the pinned version in both wire formats (`3.5`→`3`, `4.999`→`4`,
  `0.9`→`0`, `-0.5`→`0`). It happens client-side, so Postgres never sees the
  fraction and no server-side rounding rule applies.
- **Reinforce still CLAMPS where the create path REJECTS, and that asymmetry is
  deliberate**: this call applies a delta to a stored value, so a sum outside the
  range is arithmetic rather than a stated intent, while a caller-supplied
  `base_strength` outside the range is a statement the server can refuse.
- **A non-integral `strength_delta` is a 400 since `aihub#459` (owner ruling
  2026-09-09), and the refusal is on the DELTA rather than on the sum.** Checking
  only the sum would be sufficient for storage and useless for the caller: `0.5` on
  a row stored at `3` is a well-formed sum of `3.5`, and "3.5 is not a whole number"
  names a value nobody typed. The check sits with the other argument checks, above
  the first query, so the answer is a fact about the argument rather than about the
  memory — a caller with a malformed delta and an invisible memory is told about
  their delta. The rule is `internal/domain/memory.go`
  (`ValidateIntegralStrength`), shared with the create path, and
  `internal/server/routes_memory_reinforce_integral_test.go` pins the placement by
  running the handler with a nil pool: a refusal cannot have touched the database,
  and a legal delta can only demonstrate that it got through by dying on it.
- **The consequence is that the clamp and the codec can no longer disagree.** A
  stored value is `SMALLINT` and therefore whole, an accepted delta is whole, and
  the clamp's two bounds are integers — so every sum this handler can produce is
  already whole and the int2 truncation is unreachable from the API. The
  `RETURNING` clause stays anyway: what makes it right is that the response cannot
  disagree with the row, not that a disagreement happens to be reachable today.
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

- **What a delta that overflows the RANGE should do is still nobody's ruling, and
  `aihub#459` narrowed the question rather than answering it.** Its premise was
  settled by `aihub#475` (of its two candidates — "pgx refuses to encode" versus
  "the value is coerced silently" — it is the second, truncating toward zero), and
  its own disposition landed on 2026-09-09: a non-integral `strength_delta` is
  refused, so the GRANULARITY half is closed. The clamp is untouched, so a delta of
  `+4` on a row stored at `4` is still silently answered as `5`, and the 200 body
  reports `5` — honest about the row, and silent about the fact that the caller
  asked for something the row could not take. That is the same "answered 200 having
  stored something you did not name" shape the integrality ruling refused one field
  over, and the argument for keeping it (a SUM is arithmetic, not a stated intent)
  is real but was never adjudicated against that parallel. Re-checked 2026-09-09:
  `aihub#459` is `running` with this change in flight; `aihub#475` wrapped
  2026-09-08.
