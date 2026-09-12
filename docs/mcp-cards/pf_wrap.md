# pf_wrap — contract card

```json
{
  "tool": "pf_wrap",
  "description_sha256": "0ce419eb138a61273a756ad8c7515d111fd1e400340a9bdbef3f376df4680b4d",
  "input_schema_sha256": "f828a8f58680440cdc4d5473e98fd2e58b33660cfc69e511329979e9484ff704",
  "params": {
    "derived": {
      "type": "array",
      "required": true
    },
    "note": {
      "type": "string",
      "required": false
    },
    "pr_body": {
      "type": "string",
      "required": false
    },
    "pr_title": {
      "type": "string",
      "required": false
    },
    "repo": {
      "type": "string",
      "required": true
    },
    "work_item_id": {
      "type": "string",
      "required": true
    },
    "workspace_root": {
      "type": "string",
      "required": false
    }
  },
  "response_keys_observed": [
    "complete_result",
    "note_emitted",
    "ok",
    "pr",
    "pr_action",
    "pushed",
    "worktrees"
  ],
  "hop4_coverage": "written"
}
```

## hop 0-1 — what the caller is told

Seven parameters, three required.

| param | type | required | hop 1 promise |
|---|---|---|---|
| `work_item_id` | string | yes | which work item |
| `repo` | string | yes | repository name |
| `derived` | array | yes | "a wrap that omits it is refused before anything is pushed" — dispositions for findings the attempt did not fix; `[]` legal, absence refused (`TestWrapDerivedIsRequiredBeforeThePushHalf`) |
| `pr_title` | string | no | used only if a PR does not exist yet |
| `pr_body` | string | no | same |
| `note` | string | no | closing note recorded before the attempt is completed — the ordering held by `TestPublishedNoteOrderingIsTheOrderTheToolUses`, and every retry resends it (`TestWrapCompletesAsWrappedWithNoFlagAndEveryRetryResendsItsNote`) |
| `workspace_root` | string | no | workspace root path |

"Idempotent **only** when a PR on the branch already covers local HEAD" is the
qualifier that matters: local commits no PR covers are pushed, and a new PR is opened
if the existing one is merged or closed. `pr_action` in the response says which
happened.

`derived` (`aihub#350`) is unconditionally required here — refused when absent by
`internal/mcp/derived_wire_shape_test.go` (`TestWrapDerivedIsRequiredBeforeThePushHalf`)
— because this tool always completes as `wrapped`
(`TestWrapCompletesAsWrappedWithNoFlagAndEveryRetryResendsItsNote`): it is the
disposition list for findings the attempt noticed but did not fix — `folded` /
`folded:<text>` (the default, and deliberately the cheapest entry),
`filed:<wi id or slug>` (must resolve server-side), or `dropped:<reason>`. That the
grammar is one shared function across both hops, and what each verb demands, is held
by `internal/domain/complete_attempt_derived_guard_test.go`
(`TestValidateDerivedIsTheSharedShapeAuthority`,
`TestFoldedIsTheCheapestLegalDerivedEntry`); the pf_complete_attempt card carries the
measurement the field came from and the server half's arms.

## hop 2-3 — what leaves this process, and what binds it

`internal/coding/scenario.go` (`Wrap`) does the push and PR locally, then three HTTP
calls in a fixed order:

1. best-effort `push` / `pr_opened` events via
   `internal/mcp/tools_coding.go` (`emitCodingEvent`) → `POST /v1/events`;
2. the closing note via `internal/mcp/tools_events.go` (`emitNote`) →
   `POST /v1/events`, **after** the push/PR half succeeded and **before** the
   completion;
3. `pkg/client/client.go` (`CompleteAttempt`) →
   `POST /v1/work_items/<id>/complete` with `status: "wrapped"`, bound by
   `internal/server/router.go` (`handleCompleteAttempt`).

The note's position is forced from both sides: emitting it earlier would leave a
"wrapped" note on the timeline of a wrap that then failed at the push, and emitting
it later is impossible because the completion deletes the credentials.

`derived` rides the completion body of step 3, exactly as the caller stated it,
read off the recorded request by `internal/mcp/derived_wire_shape_test.go`
(`TestWrapDerivedRidesTheCompletionBody`) — and it is validated BEFORE step 1
runs at all, so a wrap refused for omitting or malforming it has pushed nothing,
opened nothing and recorded no note, with the zero-request refusal held by the
same file (`TestWrapDerivedIsRequiredBeforeThePushHalf`).

## hop 4 — what it actually does

- **It never sets `force_terminate_step`.** So wrapping with a step still
  `in_progress` always fails at the completion — which is the failure that actually
  happens, and it is why the note is recorded twice on that retry, all of it driven
  in `internal/mcp/wrap_completion_shape_test.go`: the flag's absence from the
  completion body by `TestWrapSendsNoForceTerminateStep` (with the flag's name taken
  from this sentence and required to be a real published parameter of
  `pf_complete_attempt`), and the duplicate by
  `TestWrapRecordsTheNoteAgainWhenTheCompletionFailed`, which retries a wrap whose
  completion answered that very conflict and counts the notes.
  Short duplicate notes are noise rather than damage, which is why this is
  documented rather than solved; the alternative is an idempotency key on events.
- The state file is deleted by the resolved canonical key and best-effort by the
  passed key, mirroring `pf_complete_attempt` — both halves in
  `internal/mcp/wrap_completion_shape_test.go`
  (`TestWrapDeletesBothTheCanonicalAndThePassedStateFileKeys`), which addresses the
  wrap by slug with the slug-keyed pre-claim file seeded, since that is the only
  shape in which the two keys differ at all.
- **The timeline events are the only durable record that a wrap delivered
  something**, because by then the state file and credentials are gone and a no-op
  replay looks identical from the outside.

## hop 5 — what comes back

`{ok, pr, pr_action, pushed, complete_result}` plus `pushed_sha` when there was one,
plus `note_emitted` / `note_error`, plus the worktree map. The corpus record above
spans 90 calls at a 10.00% error rate.

🔴 **In this repo's own workflow this tool is not the end-of-loop call.** The
in-tree plugin's own lifecycle reference —
`plugins/polyforge/skills/_common/references/lifecycle-details.md` — ends the run at
`pf_complete_attempt(work_item_id=..., status="wrapped")` rather than here, because
by then the PR has usually already been merged and re-running the push/PR half has
nothing to do, which
`internal/mcp/wrap_completion_shape_test.go`
(`TestTheInTreeLifecycleReferenceEndsTheLoopAtCompleteAttempt`) checks by reading
that reference's own code blocks: one of them has to make that call, and none of them
may call this tool. That is a process convention, not a property of the tool, and it is recorded
because a card that only described the tool would leave a reader thinking otherwise.

## Policy

- **§6.2 T2-3** — one status code for "invalid attempt credential"; this path
  authenticates through the same state file.
- **§6.1 T1-9** — the idempotency qualifier is published rather than implied.

## Open

- The double note on retry is stated behaviour, not a settled design.
