# polyforge v1 E2E Skill Scenarios

Test scenarios that simulate human-driven skill execution end-to-end.
Each file is a self-contained test script executable by a subagent.

## Layout

```
tests/scenarios/
  layer2/    — Step management (pf_update_step, heartbeat, stale-cred, guard)
  layer3/    — Methodology artifact chain (spec/plan/execute, memory recall)
  e2e/       — Full lifecycle (init, worktree, claim→wrap, doctor, conflict)
```

## ⚠️ All scenarios require MCP transport

Every `CALL: pf_*` assumes the polyforge MCP server is running and the state file is managed
by the MCP layer. Credentials (attempt_id, claim_epoch, session_secret) are injected
automatically by the MCP server from `<WORKSPACE>/.polyforge/state/<wi_id>.json`.

Error codes like `STALE_LOCAL_CREDENTIAL` are produced by the MCP wrapper layer, not the
HTTP server. The HTTP server returns `CONFLICT_EPOCH_MISMATCH` / `ATTEMPT_MISMATCH`.

## Running a scenario

Dispatch a subagent with:

```
You are a polyforge e2e test runner. Execute the scenario at:
  tests/scenarios/<path>.md

Use the polyforge MCP tools (pf_*) available in this session.
AIHUB_URL=http://10.146.0.16:8080  API_KEY=baOHJg3Gh7JMpV5kW2Q1BHPqweg3y5Ig
WORKSPACE_ROOT=/root/code/aicoding/gmi-ws-v3

Report each ASSERT line as PASS or FAIL with the actual value observed.
At the end print: SCENARIO PASS or SCENARIO FAIL (with failure count).
Always execute CLEANUP steps regardless of pass/fail.
```

## Field format reference

### declared_resources (pf_create_work_item)
```json
[{"type": "repo", "uri": "repo:<repoName>", "intent": "exclusive",
  "task_branch": "polyforge/<branchName>"}]
```
Types: `repo` | `path` | `document` | `section` | `service` | `external_ref`

### requested_locks (pf_claim_work_item)
```json
[{"resource_type": "git_branch", "resource_key": "<repo>/<branch>"}]
```

### pf_update_step responses
- `status="in_progress"` → `{status: "in_progress"}`
- `status="completed"` → `{status: "completed"}`
- `heartbeat=true` → `{status: "heartbeat_ok"}`
- Does NOT return `{ok: true}`

## File conventions

- `CALL:` — MCP tool invocation
- `ASSERT:` — field/condition to verify (fail if not met)
- `ASSERT_ERROR:` — expect an error containing this string
- `CLEANUP:` — always run regardless of pass/fail
- `NOTE:` — informational, not asserted
