# pf_complete_attempt — contract card

```json
{
  "tool": "pf_complete_attempt",
  "description_sha256": "ad8beae00b7025703d73a76489eefde89175938e61fe1dcb6be3ffa815cdb32a",
  "input_schema_sha256": "d274f3f98e87afe21a88e4a3caf98d3e3fcba62cbced454a654b1b6cf2db3c18",
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
| `pause_reason` | string | no | read only when `status="paused"`, and refused with any other status |

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
- **`pause_reason` is refused on any status but `paused`, and forwarded only when
  non-empty.** The refusal (`aihub#452`) is raised before the state file is resolved
  and before the note below is emitted, so a declined call writes nothing at all.
  The non-emptiness half still earns its keep on the pause path: the server writes
  whatever it is given, so an unguarded assignment would send `""` and turn "paused,
  reason not given" into "paused for no stated reason".
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
- **`pause_reason` is written on `paused` and on nothing else.** `internal/domain/run_attempts.go`
  (`FnCompleteAttempt`) refuses a non-empty reason on any other status next to the
  status check, before it opens a transaction, and normalises an empty one to `NULL`
  rather than `''` at the write. Both the column and the `attempt_completed` payload
  read the same normalised value, so the row and the event cannot disagree about
  whether a reason was recorded. The capability of recording a reason on a wrapped or
  failed attempt was decided against rather than narrowed by accident: the column's
  only reader is `internal/domain/work_items.go` (`GetReadyQueue`), whose paused
  segment filters `wi.status = 'paused'`, and `internal/db/migrations/0027_run_attempts_pause_reason.sql`
  scopes the column to `complete_attempt(status=paused)`. `note` is the field that
  records on every status, and the refusal names it.

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
- **§6.1 T1-9, second application** — `pause_reason` shipped with "read only when
  `status="paused"`" while both later hops read it on every status. Disposition 2
  (fix the code so the prose becomes true) was taken over rewriting the sentence,
  because the capability the sentence denied had no reader; `aihub#452`. The prose
  gained the one thing a guard adds that "read only" does not say — that the
  mismatch is refused rather than ignored.

## Open

- The double-note on retry is a known, stated behaviour rather than a settled
  design. Nothing in the response distinguishes a first note from a duplicate.
