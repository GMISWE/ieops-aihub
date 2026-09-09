# pf_pause_attempt — contract card

```json
{
  "tool": "pf_pause_attempt",
  "description_sha256": "fcfae2916f42bafa65ec2a703a2278ba4752aae19153db5edc845d913a161946",
  "input_schema_sha256": "b2a0b799191acc6ff59530c2dc1df8b535735fce5fe9b62b4d718a79380cc79e",
  "params": {
    "pause_reason": {
      "type": "string",
      "required": false
    },
    "work_item_id": {
      "type": "string",
      "required": true
    }
  },
  "response_keys_observed": [
    "status"
  ],
  "hop4_coverage": "written"
}
```

## hop 0-1 — what the caller is told

Two parameters. The description carries the lock semantics because they differ from
every other terminal path: pausing **releases** `file_scope` locks acquired
mid-attempt and **retains** every other lock type for resume.

⚠️ Since `aihub#416` that retained set is normally EMPTY — `file_scope` is the only
lock the server derives — so the description says so rather than describing a
retention a caller will never observe. It is non-empty only for an attempt that
supplied `requested_locks` explicitly.

| param | type | required | hop 1 promise |
|---|---|---|---|
| `work_item_id` | string | yes | used to find the state file |
| `pause_reason` | string | no | optional reason |

## hop 2-3 — what leaves this process, and what binds it

`internal/mcp/tools_lifecycle.go` (`registerLifecycleTools`) resolves the state file
via `internal/config/state.go` (`ResolveStateFile`) and calls
`pkg/client/client.go` (`PauseAttempt`) → `POST /v1/work_items/<id>/pause`, bound by
`internal/server/routes_step.go` (`handlePauseAttempt`).

Two details:

- The request is addressed by `sf.WIID` — the **resolved canonical** id — not by the
  string the caller passed, so a slug-addressed pause still hits the right row.
- `pause_reason` is forwarded only when non-empty, the same guard
  `pf_complete_attempt` applies for the same reason.

## hop 4 — what it actually does

- The attempt's status becomes `paused` and the local state file is **kept**, which
  is the whole difference from a terminal completion: resume needs those credentials.
- **From that moment the server hard-rejects every credential-checked `pf_*` call**
  with its own distinct code, `ErrAttemptPaused` ("attempt is paused; resume it
  before continuing"), deliberately different from a stale credential. So a step
  loop that pauses cannot corrupt step state — it simply cannot advance — but it can
  walk into a cascade of surprise credential errors if it retries.
- `internal/mcp/tools_step.go` (`classifyStepUpdateErr`) is the client-side half:
  `ATTEMPT_PAUSED` keeps the state file and points at resume, while a genuine stale
  credential deletes it. Getting that classification wrong is destructive in one
  direction only.

## hop 5 — what comes back

`jsonResult`, no projection. The corpus record above is the union of keys real
callers have been handed.

## Policy

- **§6.2 T2-3 — LANDED (`aihub#441`).** One status code for "invalid attempt
  credential" across all tools, and it is **403 `ATTEMPT_MISMATCH`**. The invalid
  `session_secret` refusal used to answer 401 `UNAUTHORIZED` from
  `verifyAttemptCredential` (this tool's path, plus `pf_complete_attempt`,
  `pf_wrap`, `pf_commit`, `pf_acquire_locks`, `pf_update_step`, `pf_save_artifact`)
  and 403 `ATTEMPT_MISMATCH` from `pf_emit_event`, for the same wrong secret.
  `ATTEMPT_PAUSED` being distinct from that class is the property this tool depends
  on, and it is the reason the ruling says *one* code for the credential class rather
  than one code for everything — it is unchanged, still 409, and still reached only
  by a caller whose secret is VALID, so the unification cannot shadow it.
- **§6.2 T2-15** — which lock types survive a pause is exactly the row the
  de-locking ruling shrinks to `file_scope`. Landed by `aihub#416` (2026-09-09):
  the pause SQL is byte-unchanged (it always named `file_scope` explicitly); what
  changed is that nothing else is being derived for it to retain.

## Open

- **§6.4 item 6 is CLOSED for this tool as of `aihub#416` (2026-09-09).** The
  prediction this bullet made came true — "retained for resume" now usually
  describes an empty set — and the resolution was to say so in the description
  rather than to drop the clause. Dropping it would be wrong in the one case that
  still reaches it: an attempt holding a `requested_locks` row keeps it across a
  pause, and a caller told otherwise would expect a release that does not happen.
