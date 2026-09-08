# pf_pause_attempt — contract card

```json
{
  "tool": "pf_pause_attempt",
  "description_sha256": "97ab52c4996df4eec89ffcc91015a6cf6cc3bc758a3a5e4f1d32ccc8e38d6bed",
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
mid-attempt and **retains** `git_branch`/`deploy_env` for resume.

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

- **§6.2 T2-3** — one status code for "invalid attempt credential" across all tools.
  `ATTEMPT_PAUSED` being distinct from that class is the property this tool depends
  on, and it is the reason the ruling says *one* code for the credential class rather
  than one code for everything.
- **§6.2 T2-15** — which lock types survive a pause is exactly the row the
  de-locking ruling shrinks to `file_scope`.

## Open

- **§6.4 item 6** — after the de-locking ruling lands, "retained for resume" will
  describe a set with nothing in it. That is `aihub#416`'s to resolve, and this
  description will need re-reading when it does.
