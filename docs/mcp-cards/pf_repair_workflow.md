# pf_repair_workflow — contract card

```json
{
  "tool": "pf_repair_workflow",
  "description_sha256": "f768e4c79fe068f0ae9abaf924981103122bdc3107798741740c8499989f4cbe",
  "input_schema_sha256": "bf0457fbcb51ffaf4e5299eac706eb0a5906a6c4f51b6e95dafafb04fa702328",
  "params": {
    "failed_step_attempt_id": {
      "type": "string",
      "required": true
    },
    "kind": {
      "type": "string",
      "required": true,
      "enum": [
        "episode",
        "retry"
      ]
    },
    "reason": {
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

Four parameters, all required, all forwarded verbatim.

| param | type | required | hop 1 promise |
|---|---|---|---|
| `work_item_id` | string | yes | the work item being recovered |
| `failed_step_attempt_id` | string | yes | the exact failed result being recovered |
| `kind` | string | yes | retry or episode |
| `reason` | string | yes | why recovery is authorized, recorded with it | <!-- probe-waiver: kind=pending-implementation | decided=2026-09-17 | citation=aihub#708 | reason=the wire-shape probe for this hop lands with Batch 2B's state-transition parity fixtures, which own the driver-side loop -->

The description names the two kinds, the binding of later starts to the
authorization, the pure policy's validation of the completed set, the bound on
open authorizations, the immutable parent lineage a retry derives when it
recovers an episode role's provider_error (with the bounded retry chain), and
the current-attempt fencing with the paused-after-FAIL consequence.

## hop 2-3 — what leaves this process, and what binds it

`internal/mcp/tools_workflows.go` (`registerWorkflowTools`) resolves the state
file and sends the three credentials plus the four fields to
`pkg/client/workflows.go` (`AuthorizeWorkflowRepair`) →
POST /v1/work_items/:id/workflow/repair, bound by
`internal/server/routes_workflows.go` (`handleWorkflowRepair`), gated on
`writer` plus the attempt credentials — the controller under existing fencing. <!-- probe-waiver: kind=pending-implementation | decided=2026-09-17 | citation=aihub#708 | reason=the wire-shape probe for this hop lands with Batch 2B's state-transition parity fixtures, which own the driver-side loop -->

## hop 4 — what it actually does

- **Binds the exact failed result** of the CURRENT generation: a result from an
  older generation answers CONFLICT_STEP_ATTEMPT_MATCH
  (`internal/domain/workflow_run.go`, `AuthorizeWorkflowRepair`). <!-- probe-waiver: kind=pending-implementation | decided=2026-09-17 | citation=aihub#708 | reason=the cross-generation refusal probe is Batch 2B's resume-after-revision loop -->
- **`episode` requires a FAIL-verdict review result; `retry` requires a
  `provider_error` outcome with no FAIL verdict** — both refusals driven in
  `TestWorkflowRetryAuthorizationBounded` and
  `TestWorkflowReviewFailPausesNotTerminal`.
- **Open authorizations are bounded** at eight per work item, mirroring the
  pure policy's episode bound (`maxOpenRepairAuthorizations`). <!-- probe-waiver: kind=pending-implementation | decided=2026-09-17 | citation=aihub#708 | reason=the ninth-episode refusal probe lands with Batch 2B's repair-loop tests -->
- **After a review FAIL the paused attempt's credentials are dead**, so the
  authorization is reachable only from the resumed attempt —
  `TestWorkflowReviewFailPausesNotTerminal` seeds the new attempt before
  opening the episode and drives its roles' invocations bound to it; the
  episode stays open until every role has recorded a completed result and
  closes only on the complete set.
- **A retry over an episode role's `provider_error` records the immutable
  parent lineage** (`internal/domain/workflow_run.go`,
  `resolveRetryLineage`): the failed attempt's own invocation row names the
  authorization it was bound to, and the server derives `parent_episode_id`
  (the episode whose role the retry's replacements stand in for) together with
  the chain depth, refusing a new retry authorization once the chain already
  holds `maxRetryChainDepth` retries (`TestWorkflowEpisodeRetryChainBounded`).
  A second binding of the step directly to the episode is refused while the
  retry's replacement carries the role, the failed history rows stay
  recorded, and the episode's effective role set — what the role-order gate,
  the close predicate and the successor gates read — selects the latest
  successful authorized replacement
  (`TestWorkflowEpisodeProviderErrorRetryLineageCompletes`).

## hop 5 — what comes back

`jsonResult`, no projection: the opened authorization (`id`, `kind`,
`work_item_id`, `failed_steps_version`, `failed_step_id`,
`failed_step_attempt_id`, `status`, `created_at`, and `parent_episode_id` on
retries that descend from an episode role invocation — empty otherwise, never
a request field). `id` is the value a later `pf_start_workflow_step` passes
as `repair_episode_id`
(`TestWorkflowRetryAuthorizationBounded`; the lineage half is driven by
`TestWorkflowEpisodeProviderErrorRetryLineageCompletes`).

## Policy

- Spec D8: authorized recovery explicitly opens repair, verification and a
  fresh producer-isolated review; no automatic unlimited repair.
- Spec D9: the retry kind is the bounded infrastructure-failure fallback —
  provider_error only, never a reroll of a review FAIL — record the new
  invocation before retrying.

## Open

- Whether Batch 2B's controller consumes the single-retry `RepairAuthorization`
  form from these rows directly or only through episodes is an engine design
  question as of 2026-09-17 (`TestWorkflowRetryAuthorizationBounded` drives the
  retry kind end to end; the episode-only consumption is the open half).

