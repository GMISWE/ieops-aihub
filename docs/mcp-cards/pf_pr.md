# pf_pr — contract card

```json
{
  "tool": "pf_pr",
  "description_sha256": "d98bc7d681ce9b2a0ef85aa0ddd2f616dd23d0774314b4fc529098e084b988d5",
  "input_schema_sha256": "43ced54eb8e2ed7a8090b2368e00a874792886c761df5155db3ab598a8f2ba73",
  "params": {
    "base": {
      "type": "string",
      "required": false
    },
    "body": {
      "type": "string",
      "required": true
    },
    "head": {
      "type": "string",
      "required": false
    },
    "repo": {
      "type": "string",
      "required": true
    },
    "title": {
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
    "baseRefName",
    "commits",
    "number",
    "state",
    "url"
  ],
  "hop4_coverage": "written"
}
```

## hop 0-1 — what the caller is told

Seven parameters, four required.

| param | type | required | hop 1 promise |
|---|---|---|---|
| `work_item_id` | string | yes | which work item's worktree |
| `repo` | string | yes | repository name |
| `title` | string | yes | PR title |
| `body` | string | yes | PR body |
| `head` | string | no | head branch (default: current) |
| `base` | string | no | base branch (default: the repo default branch) |
| `workspace_root` | string | no | workspace root path |

## hop 2-3 — what leaves this process, and what binds it

The PR is created by the **`gh` CLI** rather than by aihub:
`internal/coding/gh_ops.go` (`GHCreatePR`) runs in the worktree, which
`internal/mcp/pr_gh_boundary_test.go`
(`TestThePRIsCreatedByGhAndTheOnlyAihubHopIsTheEvent`) drives against a fake `gh`
that records its own working directory. The only HTTP call to aihub is the
best-effort `pr_opened` event through `internal/mcp/tools_coding.go`
(`emitCodingEvent`) → `POST /v1/events`, and `internal/mcp/pr_gh_boundary_test.go`
(`TestThePRIsCreatedByGhAndTheOnlyAihubHopIsTheEvent`) requires the observed request
list to be exactly that one path.

So there is no aihub hop 3 for `title`, `body`, `head` or `base`: they are `gh`
arguments, each one found in the recorded argv and absent from the event body by
`internal/mcp/pr_gh_boundary_test.go`
(`TestThePRIsCreatedByGhAndTheOnlyAihubHopIsTheEvent`).
`internal/mcp/helpers.go` (`prPayload`) is what turns the `gh` result into
the event payload, which is the only aihub-side record that the PR exists — its
four keys are asserted on the wire by `internal/mcp/pr_gh_boundary_test.go`
(`TestThePRIsCreatedByGhAndTheOnlyAihubHopIsTheEvent`).

## hop 4 — what it actually does

- Creates the PR and returns `gh`'s own JSON, which is why the observed response keys
  are GitHub's camelCase names (`baseRefName`, `commits`, `number`, `state`, `url`)
  rather than this repo's snake_case, driven with a `gh` that also prints a key no
  type in this repo declares by `internal/mcp/pr_gh_boundary_test.go`
  (`TestTheGhObjectReachesTheModelUnprojected`). **That is the only tool in the set
  whose response vocabulary is a third party's.**
- Failure to create the PR is a plain error result. Unlike `pf_ship`, there is no
  structured side-effect report, because a single-stage tool has nothing partial to
  report — a failing `gh` is driven, and the absence of `stage` / `side_effects`
  asserted, by `internal/mcp/pr_gh_boundary_test.go`
  (`TestAFailedPRIsAPlainErrorWithNoStageOrSideEffects`).
- The event is best-effort, so a PR can exist with no `pr_opened` on the timeline —
  which matters because after a wrap the timeline is the only durable record left;
  `internal/mcp/pr_gh_boundary_test.go`
  (`TestThePREventIsBestEffortAndAFailedEmitDoesNotFailTheCall`) refuses the emit
  server-side and requires the call to succeed anyway, with the attempted request as
  its control.

## hop 5 — what comes back

`gh`'s object, unprojected. The corpus record above spans 662 calls at a 10.88%
error rate; `state` and `commits` in that union are GitHub's fields, not aihub's.
<!-- prose-only: because=measurement -->

## Policy

- **§6.1 T1-5** — no projection, so nothing can be silently dropped here.
- **§6.2 T2-5 — LANDED (`aihub#444`).** `pr_opened` is a free-text event type — the
  column has no CHECK and the schema publishes no `enum`, because an MCP enum is
  advisory — but the vocabulary IS published now, on `pf_emit_event`'s `event_type`
  description, derived from `domain.EventVocabulary` rather than retyped, and
  `pr_opened` is in it, which `internal/mcp/pr_gh_boundary_test.go`
  (`TestThePREventTypeIsAPublishedVocabularyEntry`) holds by binding the type this
  tool actually emits to that published list and to the absence of an `enum`. This
  bullet read "with no published vocabulary; the ruling is to publish the vocabulary
  on `pf_emit_event`" until `aihub#583` measured it against the landed change.
  <!-- prose-only: because=history -->
  That binding is the join
  `internal/mcp/tools_events_vocab_test.go`
  (`TestEmitEventTypeDescriptionPublishesTheVocabulary` and
  `TestEmitEventTypeIsNotPublishedAsAClosedEnum`) cannot make: those two quantify
  over the list, so dropping `pr_opened` from it leaves them green.

## Open

- Nothing this card can settle. The camelCase response is `gh`'s contract, not this
  repo's, and is not something an adjudicated row governs.
  <!-- prose-only: because=judgement -->
