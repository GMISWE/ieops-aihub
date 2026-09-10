# pf_get_step — contract card

```json
{
  "tool": "pf_get_step",
  "description_sha256": "7c843970e4b28c379391a4c76e182b9d29685af5b54da1392f0af6d821257e69",
  "input_schema_sha256": "0d138f8f344be0161281deb07d0ff88f782e6397ea2be18bb413fb2c4cfe88e4",
  "params": {
    "work_item_id": {
      "type": "string",
      "required": true
    }
  },
  "response_keys_observed": [
    "completed_steps",
    "completed_steps_truncated",
    "current_step",
    "current_step_attempt",
    "current_step_status",
    "scenario_ref",
    "step_started_at",
    "version",
    "wi_type",
    "work_item_id"
  ],
  "hop4_coverage": "written"
}
```

## hop 0-1 — what the caller is told

One parameter, and the description makes six promises — that this record is
authoritative and unique, that `completed_steps` is the history oldest-first with
retries included, that a resuming agent should call it FIRST, that only an entry
whose `status` is `completed` means that step is done, that a slug is accepted and
the canonical id is echoed, and that there is no step graph here — each of them
required off a live session, one by one, by
`internal/mcp/get_step_wire_shape_test.go`
(`TestPublishedGetStepPromisesAreTheSixTheCardNames`).

| param | type | required | hop 1 promise |
|---|---|---|---|
| `work_item_id` | string | yes | "Work item ID" — a slug or a canonical id |

The negative half of the description is load-bearing and was added by `aihub#265`:
it used to advertise "step graph, current status, progress, previous steps" while
the endpoint returned none of the last two, which is why every scenario step graph
once opened by telling the agent to read `.pf_steps.json` — a file nothing in this
repo ever writes.

## hop 2-3 — what leaves this process, and what binds it

`internal/mcp/tools_step.go` (`registerStepTools`) validates the argument is
non-empty and calls `pkg/client/client.go` (`GetStep`), which issues
`GET /v1/work_items/<id>/step` — both halves read off a fake aihub's request log by
`internal/mcp/get_step_wire_shape_test.go`
(`TestGetStepValidatesTheIdBeforeIssuingOneGet`), which requires an empty argument
to produce NO request at all rather than a 404 from the router. The value travels as
a **path segment**, not a query parameter or a body field, so there is no forwarding
table for it to be dropped from.

`internal/server/routes_step.go` (`handleGetStep`) binds it from the route and
resolves slug-or-id server-side. No credential is sent: this is a read.

## hop 4 — what it actually does

The handler returns `server.StepState`, and the relationship between the
description and that struct is itself gated: `internal/mcp/tools_step_contract_test.go`
asserts in BOTH directions that every response field the description names is a
bound JSON key on the struct, so the tool cannot go back to promising something the
struct does not carry.

Three behaviours a caller has to know and cannot see from the schema:

- **`completed_steps` is the step-history table rather than a derived view**, which
  `internal/server/get_step_history_source_test.go`
  (`TestCompletedStepsQueryReadsTheHistoryTableItself`) holds against the query and
  the migration together — a view of the same name and columns satisfies every
  row-level arm in that package. Rows are filed by `pf_update_step` on a terminal
  transition, held over a whole seven-step graph by
  `internal/server/routes_step_history_row_db_test.go`
  (`TestHandleUpdateStep_EveryRecordedStepOutcomeHasAHistoryRow`), and keyed on
  `step_attempt_id` — the UNIQUE index is in the arm above and the 409 a caller sees
  is `TestHandleUpdateStep_DuplicateStepAttemptIsAConflictNotASilentDrop`. Since
  `aihub#399` a terminal transition without one is refused 400 rather than answered
  200, so "the step completed but is missing from the
  history" is no longer reachable through the tool — `aihub#390` is where that was
  reachable, and the two records disagreeing is what that work item measured.
- **`completed_steps: []` and the key being absent are different answers.** Empty
  means nothing has completed —
  `internal/server/routes_step_dbgated_test.go`
  (`TestHandleGetStep_EmptyHistoryIsAnEmptyArrayNeverNull`) reads that off a real
  response for a work item with no completions, `internal/mcp/tools_step_contract_test.go`
  (`TestGetStepCompletedStepsDistinguishesEmptyFromAbsent`) holds the three wire
  spellings apart, and `internal/mcp/get_step_wire_shape_test.go`
  (`TestGetStepDoesNotNormaliseAnAbsentHistory`) holds that this process forwards
  each shape unchanged instead of filling one in. Absent means the server predates
  `aihub#265`. The description states this because a client that conflates them
  reports "no prior steps" for a work item that has six.
- **A `failed` entry is not a finished step, and the query does not hide one.**
  `completedStepsQuery` (`internal/server/routes_step.go`) reads
  `wi_step_completions` by work item with NO status filter, and that table's
  `status` domain is `{completed, failed}`
  (`internal/db/migrations/0005_step_state.sql`) —
  `internal/server/get_step_history_source_test.go`
  (`TestCompletedStepsQueryHasNoStatusFilter`) reads both out of the query constant
  and out of the table's own CREATE TABLE body, and
  `internal/server/routes_step_dbgated_test.go`
  (`TestHandleGetStep_SummaryAndErrorTypeLandInTheirOwnFields`) seeds one completed
  and one failed row and reads both back. The entry a resuming agent cannot
  otherwise know about is the one `fnForceTerminateStep`
  (`internal/domain/run_attempts.go`) writes when an attempt is paused over an
  `in_progress` step — `status='failed'`, `error_type='force_terminate'`, naming a
  step nobody completed, which `internal/domain/card_claims_wave2_test.go`
  (`TestForceTerminateStepFilesAFailedRowUnderForceTerminate`) reads out of that
  INSERT while `internal/domain/complete_attempt_step_gate_test.go`
  (`TestTheStepInProgressRefusalIsGatedOnPausedOrTheFlagAlone`) holds when it runs.
  Until `aihub#450` the description said to treat every `step_id` in the list as
  done, which is that step skipped.
  <!-- prose-only: because=history -->
  The prose moved, not the query: filtering it would delete the retry history the
  same sentence promises and break the `aihub#390` invariant that `completed_steps`
  equals the attempt's `step_completed`/`step_failed` events.
  <!-- prose-only: because=counterfactual -->

The echoed `work_item_id` is the canonical id even when a slug was passed:
`internal/domain/work_item_ref_db_test.go` (`TestGetWorkItemResolvesIDOrSlug`) holds
the resolution and `internal/server/get_step_history_source_test.go`
(`TestGetStepEchoesTheResolvedWorkItemID`) holds the hop between it and the response,
which is where the echo could quietly become the caller's own parameter.
`pf_recall` and `pf_read_events` take either spelling: both map the reference onto
`work_items.id` before comparing it to a column —
`internal/domain/card_claims_wave2_test.go`
(`TestRecallResolvesTheWorkItemFilterBeforeComparingIt`) and
`internal/server/get_step_history_source_test.go`
(`TestListEventsFiltersOnTheResolvedWorkItemID`) hold the wiring, and
`internal/server/recall_work_item_slug_db_test.go`
(`TestRecallResolvesWorkItemIdOrSlug`) plus `internal/server/events_slug_db_test.go`
(`TestListEventsBySlug_ReturnsTheSameStreamAsByID`) drive it against a real database.

⚠️ This card used to say those two tools "return nothing for a slug", which was the
pre-`aihub#343` / pre-`aihub#363` behaviour, and `aihub#422`'s own test header had
already recorded that line as stale with nothing red anywhere. The **tool
description** carried the same clause long after — item 17 of the `aihub#385` audit's
§3.2, one of the findings `aihub#450` left behind when it landed the other two, and
deliberately unprobed while it stood, since a probe on the then-current wording
would have arrived red on the day it was corrected. `aihub#590` (2026-09-10)
corrected it: the description now says `pf_recall` / `pf_read_events` resolve either
form and the canonical echo is informational, and
`internal/mcp/slug_publication_test.go` (`TestSlugAcceptanceIsNotDeniedByGetStep`)
pins the corrected clause while refusing the stale one by its exact words, with
(`TestSlugDenialIsNowhereInThePublishedSurface`) sweeping every published
description and schema so the sentence cannot migrate to a tool that arm does not
read.

## hop 5 — what comes back

The result is passed through by `jsonResult` with no projection: this tool has no
slim function, so every key the server sends reaches the model, which
`internal/mcp/get_step_wire_shape_test.go`
(`TestGetStepForwardsEveryKeyTheServerSends`) holds by sending a key nothing in this
process has ever heard of — a known key arrives under a keep-list too. The corpus
record above is the union of top-level keys any caller has actually been handed over 435
calls with a 0.00% error rate — the highest-volume error-free tool in the census.

`current_step_attempt` and `scenario_ref` appear there and are not named in the
description; they are carried without being promised, which
`internal/mcp/get_step_wire_shape_test.go`
(`TestGetStepCarriesTwoKeysTheDescriptionDoesNotPromise`) holds in both directions —
`server.StepState` must bind them and the description must not name them, since
naming one turns it into a promise and `getStepAdvertised` is then where it belongs.

## Policy

- **§6.1 T1-9** — prose that contradicts hop 3 or 4 is a bug at the same priority
  as the behaviour, and "description-only" is not a cancellation reason. This tool
  is the worked example: `aihub#265` fixed the description by fixing what the
  endpoint returns, rather than by trimming the promise.
- **§6.2 T2-21** — the `aihub#400` findings about this call were revived rather
  than left cancelled, on the grounds that it is "the one call every resuming agent
  is told to make first". Landed as `aihub#450`: the `docs/mcp-tools.md` row that
  still promised a step graph, and the sentence that told the reader to ignore
  `status`.

## Open

- Nothing this card can settle. The `§6.4` items do not touch this tool.
