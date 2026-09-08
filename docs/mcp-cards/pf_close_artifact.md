# pf_close_artifact — contract card

```json
{
  "tool": "pf_close_artifact",
  "description_sha256": "5334a130fadf0a81567ebcae0890054b8c27c310492faebfb4120841ddbec99a",
  "input_schema_sha256": "d4de7d80cb7b02fe8a93e3de4b52d9cffec5a37df229456dbda1673c98aaeeb6",
  "params": {
    "artifact_type": {
      "type": "string",
      "required": false
    },
    "memory_id": {
      "type": "string",
      "required": true
    },
    "work_item_id": {
      "type": "string",
      "required": true
    }
  },
  "response_keys_observed": null,
  "hop4_coverage": "written"
}
```

## hop 0-1 — what the caller is told

Three parameters, shared verbatim with the other two artifact-action tools through
`internal/mcp/tools_memory.go` (`artifactActionSchema`).

| param | type | required | hop 1 promise |
|---|---|---|---|
| `work_item_id` | string | yes | which work item |
| `memory_id` | string | yes | "Artifact memory ID" |
| `artifact_type` | string | no | "Artifact type" |

The description says "wrapper around `pf_emit_event` `artifact_action`", which is
literally true and is the most important thing on the card: this tool publishes no
capability of its own.

## hop 2-3 — what leaves this process, and what binds it

`internal/mcp/tools_memory.go` (`emitArtifactAction`) resolves the state file and
`internal/mcp/tools_memory.go` (`buildArtifactActionBody`) renders the arguments into
the body of `POST /v1/events` via `pkg/client/client.go` (`EmitEvent`), bound by
`internal/server/routes_memory.go` (`handleEmitEvent`).

**`memory_id` lands NESTED and RENAMED**, as `payload.artifact_key`. Both are why the
wire guard walks JSON paths instead of comparing top-level key sets. `artifact_type`
lands as `payload.artifact_type`, and the action verb is a constant supplied by the
handler, not by the caller.

So the request is byte-for-byte something `pf_emit_event` could send directly, and
the only thing this tool adds is that the caller cannot get the payload shape wrong.

## hop 4 — what it actually does

It writes one `artifact_action` event with `action: "close"`. **No artifact row changes state**: the event
is the record, and whether anything reads it is exactly what §6.2 T2-7 is about.

`event_type` here is free text at the DB level like every other event type, so
`artifact_action` is a convention this file keeps rather than a value anything
enforces.

## hop 5 — what comes back

`jsonResult` of the event result; no projection. **`response_keys_observed` is
`null`**: the `aihub#412` corpus holds no record for this tool at all, over a 21-day
window in which every other high-traffic tool logged hundreds of calls. That absence
is not neutral — it is one of the three data points behind T2-7.

The keys themselves are pinned anyway, just not here: `aihub#482`'s K10 in
`internal/mcp/card_response_keys_live_e2e_db_test.go` drives this tool against a
live server and holds the result to `event_id`, declared in
`docs/mcp-cards/live-response-keys.json` because a generated corpus record with no
traffic behind it cannot carry them.

## Policy

- **§6.2 T2-7** — the ruling is to **retire the three action tools, or give
  `artifact_action` a reader**. Zero calls, zero readers, three schemas in every
  request's prefix. The null corpus record above is that measurement, per tool.
- **§6.1 T1-10** — the resident cost is a property of the whole `tools/list` payload,
  and three near-identical schemas is where that argument bites hardest.

## Open

- **§6.4 item 3** — **T2-7 cannot prove there is no consumer.** Zero calls in one
  21-day window is evidence of disuse; the artifact annotation flow in the web UI
  uses the same adopt/close/ignore vocabulary and **was not checked**. Retiring these
  tools needs that checked first, not assumed. This card therefore records the tool
  as a retirement CANDIDATE, not as dead.
