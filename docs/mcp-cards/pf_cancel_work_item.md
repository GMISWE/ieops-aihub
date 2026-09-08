# pf_cancel_work_item — contract card

```json
{
  "tool": "pf_cancel_work_item",
  "description_sha256": "22b5ffd38c1a47f1d8163d6c698ed89aaa90c30c25636a5c9335f0c94a8f3231",
  "input_schema_sha256": "9cefaa19ce14188a6f2e77d6c54018f1f18be9cb6b435b93695c6b3f1834e1c8",
  "params": {
    "reason": {
      "type": "string",
      "required": false
    },
    "work_item_id": {
      "type": "string",
      "required": true
    }
  },
  "response_keys_observed": [
    "ok"
  ],
  "hop4_coverage": "written"
}
```

## hop 0-1 — what the caller is told

Two parameters and a long description, because `aihub#355` changed both what this
tool does and what it can return.

| param | type | required | hop 1 promise |
|---|---|---|---|
| `work_item_id` | string | yes | id or slug |
| `reason` | string | no | cancellation reason |

The description publishes four outcomes a caller must branch on: legal from
`queued`/`paused`/`blocked`; 409 `CONFLICT_WI_ALREADY_CLAIMED` on a running item;
409 `CONFLICT_TERMINAL_STATE` on an already-terminal one; and 409
`CONFLICT_SERIALIZATION_FAILURE` with `retryable=true` on a lost race. Both of the
last two mean retry or re-read, not "the server is broken".

## hop 2-3 — what leaves this process, and what binds it

`internal/mcp/tools_lifecycle.go` (`registerLifecycleTools`) sends `{reason}` — and
only when non-empty — to `pkg/client/client.go` (`CancelWorkItem`) →
`POST /v1/work_items/<id>/cancel`, bound by `internal/server/router.go`
(`handleCancelWorkItem`).

No attempt credential is involved: cancelling is authorized by project role, which
is why it works on a work item this machine holds no state file for.

## hop 4 — what it actually does

- **It releases every resource lock still held on the work item's behalf**, each
  emitting a `lock_released` event with `cause=wi_cancelled`, so `pf_read_events`
  can confirm the release actually happened.
- **That release is what the change was for.** Pausing deliberately KEEPS the
  `git_branch` / `deploy_env` / worktree / `tcp_port` locks so a resume can go on
  holding the branch — and before `aihub#355`, cancelling a paused work item left
  them held forever: the wi was terminal, so no claim, takeover or completion could
  ever release them, and the orphan sweep skips a paused attempt's rows by design.
- **The status check is re-run inside the transaction against a locked row.** A
  cancel racing a claim now returns 409 correctly; the previous 200 was a lie, and it
  released a live attempt's locks.

## hop 5 — what comes back

`jsonResult`, no projection. The corpus record above spans 161 calls at a **17.39%
error rate** — the highest of any tool with 100 or more calls, though not the highest
overall (`pf_reinforce_memory` is, at 83.33% over 6). That is consistent with a tool
whose legal-state set is narrow and whose refusals are 409s rather than faults: a
high error rate here is not by itself evidence of a defect.

## Policy

- **§6.2 T2-3** — the ruling includes adding **the cancelled-attempt status the
  cancel path says it needs**; today the attempt-status vocabulary has no value for
  "ended because the work item was cancelled".
- **§6.2 T2-1** — one editability matrix for the whole struct and one error code per
  rejection KIND: 409 for state, 403 for permission. This tool's three 409s are three
  distinct states, which is the shape that ruling asks for — and `aihub#440` carried
  it across, so `pf_update_work_item` now refuses with **these** codes rather than
  with two field-specific ones of its own. Nothing about this tool changed.

## Open

- **§6.4 item 6** — what the de-locking ruling does to the release path here is
  `aihub#416`'s question, not this card's.
