# pf_activate_memory — contract card

```json
{
  "tool": "pf_activate_memory",
  "description_sha256": "7ddb6698a556717375909158d8b5f948e2e8fb15f3fe9c4e58bb6befe51c6df0",
  "params": {
    "memory_id": {
      "type": "string",
      "required": true
    }
  },
  "response_keys_observed": [
    "activation_count",
    "effective_strength",
    "new_stability_days"
  ],
  "hop4_coverage": "written"
}
```

## hop 0-1 — what the caller is told

One parameter and a one-line description that states two effects.

| param | type | required | hop 1 promise |
|---|---|---|---|
| `memory_id` | string | yes | "Memory ID" |

"Activate a memory (increments activation count, updates stability)" is the whole
published contract. It is short and it is accurate, which makes this tool the
control case for the rest of this directory: a card is cheap here because the schema
already says what hop 4 does.

## hop 2-3 — what leaves this process, and what binds it

`internal/mcp/tools_memory.go` (`activateMemorySchema`) publishes it; the handler
rejects an empty value and calls `pkg/client/client.go` (`ActivateMemory`) →
`POST /v1/memories/<id>/activate`, bound by `internal/server/routes_memory.go`
(`handleActivateMemory`).

A POST with a **nil body**: the id is the path segment and nothing else is sent. No
attempt credentials, so activation works without a claimed work item — which is what
lets the Memory-First recall step activate what it read.

## hop 4 — what it actually does

Increments the activation count and updates the stability term of the forgetting
curve, which is what `effective_strength` is computed from at recall time. So
activation is not bookkeeping: it changes which memories a later `pf_recall` returns
above `min_strength`.

There is no un-activate. The effect is monotone, which is why the Memory-First
instruction is to activate the ones the model judges **actually useful** rather than
everything a recall returned.

## hop 5 — what comes back

`jsonResult`, no projection. The corpus record above is the union of top-level keys
real callers have been handed.

## Policy

- **§6.2 T2-19** — strength scales must be published consistently. This tool changes
  a memory's effective strength without publishing a scale of its own, so the value
  it moves is only interpretable through `pf_recall`'s `min_strength` and
  `pf_remember`'s `base_strength` — the two the ruling is about.

## Open

- Nothing this card can settle.
