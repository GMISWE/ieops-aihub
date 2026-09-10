# pf_activate_memory — contract card

```json
{
  "tool": "pf_activate_memory",
  "description_sha256": "7ddb6698a556717375909158d8b5f948e2e8fb15f3fe9c4e58bb6befe51c6df0",
  "input_schema_sha256": "b9432f0272d4db567a46d4add55a34ad39d12af6d789700a257d4d92ee66db67",
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
(`handleActivateMemory`) — all four halves measured against a fake aihub by `internal/mcp/memory_wire_shape_test.go`
(`TestActivateMemoryPostsTheIdInThePathWithNoBody`), which asserts the refusal as
NO REQUEST REACHING THE WIRE rather than as an error coming back: a handler that
forwarded the empty id and let the server answer 404 also returns an error, and
the claim is about where the refusal happens.

A POST with a **nil body**: the id is the path segment and nothing else is sent. No
attempt credentials, so activation works without a claimed work item — which is what
lets the Memory-First recall step activate what it read.

## hop 4 — what it actually does

Increments the activation count and updates the stability term of the forgetting
curve, which is what `effective_strength` is computed from at recall time — the
two columns are read back off the row by `internal/domain/memory_lifecycle_audit_db_test.go`
(`TestActivate_StillRevivesADecayedVersionWithNoSuccessor`), and the term rising
with the count, together with `MemoryStrength` rising with the term, is held
DB-free by `internal/domain/memory_activation_strength_test.go`
(`TestActivationRaisesTheStabilityTermTheStrengthFormulaReads`) over every
memory-type branch including the unrecognised one. So activation is not
bookkeeping: it changes which memories a later `pf_recall` returns above
`min_strength`, which the same file exhibits as a threshold CROSSING
(`TestActivationCanCarryAMemoryBackOverTheRecallThreshold` — a 21-day-old
`experience.*` memory at the default strength sits below 0.3 and one activation
carries it back over) and pins to the SQL that decides it
(`TestRecallThresholdsOnTheStabilityTermActivationMoves`, which requires the
`min_strength` predicate to keep dividing by the `stability_days` column this
tool writes).

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
  `pf_remember`'s `base_strength` — the two the ruling is about, and both halves of
  that are held by `internal/mcp/memory_published_word_test.go`
  (`TestActivateMemoryPublishesNoStrengthScaleOfItsOwn`): no bound built from
  `internal/domain/memory.go` (`MaxBaseStrength`) may appear anywhere on this
  tool's published surface, and the two parameters named above must still state the
  scale, which is the positive control that keeps the negative half from going
  vacuously green if every description in the file were deleted.

## Open

- Nothing this card can settle.
