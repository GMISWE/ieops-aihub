# pf_complete_attempt — contract card

```json
{
  "tool": "pf_complete_attempt",
  "description_sha256": "ad8beae00b7025703d73a76489eefde89175938e61fe1dcb6be3ffa815cdb32a",
  "input_schema_sha256": "59a74f8a27c1608c1fab4a023371d6c2bb95dcb1c484436b17a8c864c37334db",
  "params": {
    "force_terminate_step": {
      "type": "boolean",
      "required": false
    },
    "note": {
      "type": "string",
      "required": false
    },
    "pause_reason": {
      "type": "string",
      "required": false
    },
    "status": {
      "type": "string",
      "required": true
    },
    "work_item_id": {
      "type": "string",
      "required": true
    }
  },
  "response_keys_observed": [
    "note_emitted",
    "ok",
    "worktrees"
  ],
  "hop4_coverage": "written"
}
```

## hop 0-1 — what the caller is told

Five parameters. The description's second sentence is an ordering constraint, not a
convenience note: this call deletes the credentials `pf_emit_event` needs, so a note
emitted afterwards cannot authenticate.

| param | type | required | hop 1 promise |
|---|---|---|---|
| `work_item_id` | string | yes | used to find the state file |
| `status` | string | yes | `wrapped` \| `failed` \| `paused` |
| `force_terminate_step` | boolean | no | force-terminate an in-progress step |
| `note` | string | no | closing note recorded BEFORE the attempt is completed |
| `pause_reason` | string | no | read only when `status="paused"` |

`note` exists because the closing note and the terminal call were always two
round-trips in a fixed order — 201 measured adjacent pairs, 0.325% of billed input —
and the second read nothing out of the first.

## hop 2-3 — what leaves this process, and what binds it

`internal/mcp/tools_lifecycle.go` (`registerLifecycleTools`) resolves the state file
through `internal/config/state.go` (`ResolveStateFile`), emits the note via
`internal/mcp/tools_events.go` (`emitNote`) → `POST /v1/events`, then calls
`pkg/client/client.go` (`CompleteAttempt`) → `POST /v1/work_items/<id>/complete`,
bound by `internal/server/router.go` (`handleCompleteAttempt`).

- **`status` and `force_terminate_step` are forwarded**; the three credentials come
  from the state file.
- **`pause_reason` is forwarded only when non-empty.** The server writes it to the
  attempt row **unconditionally**, so an unguarded assignment would stamp an empty
  reason onto every wrap. The guard is what keeps "not paused" distinguishable from
  "paused, reason not given".
- **`note` never reaches this endpoint.** It becomes its own `note` event on the
  other call, which is the difference from `pause_reason`: one is a timeline event
  whatever the status, the other is a column read on one status.

## hop 4 — what it actually does

- **Terminal statuses delete the local state file**, by the resolved canonical key
  and best-effort by the key the caller passed, so a slug-addressed completion also
  clears a stale slug-keyed stub. `paused` keeps it, because resume needs the
  credentials.
- **The note is emitted first and its failure does not abort the call.** The outcome
  is reported instead: `note_emitted` on success, and on failure the error text gains
  a clause saying whether the note already landed — because the caller is about to
  decide whether to retry, and retrying re-sends the note.
- **This is NOT exactly-once for the note.** A retry after a failed completion
  records it twice. That is documented rather than solved; an idempotency key on
  events is a bigger change.
- **A step still `in_progress` makes the completion fail** unless
  `force_terminate_step` is set — which is why the fused `pf_wrap`, which never sets
  it, is the call that most often retries and duplicates its note.

## hop 5 — what comes back

The server's completion result, plus `note_emitted` / `note_error` when a note was
requested, plus the worktree paths read out of the state file this call is about to
delete — surfaced for all statuses so a caller does not have to have read that file
itself. No slim function.

## Policy

- **§6.2 T2-3** — one status code for "invalid attempt credential" across all tools;
  retire `lost` or give it a writer; and add the cancelled-attempt status the cancel
  path says it needs.
- **§6.1 T1-9** — the `note` ordering is published on the tool rather than left in a
  skill, because the hazard is invisible from the schema alone.

## Open

- The double-note on retry is a known, stated behaviour rather than a settled
  design. Nothing in the response distinguishes a first note from a duplicate.
