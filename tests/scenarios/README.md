# polyforge v1 E2E Skill Scenarios

Test scenarios that simulate human-driven skill execution end-to-end.
Each file is a self-contained test script executable by a subagent.

## Layout

```
tests/scenarios/
  layer2/    — Step management (pf_update_step, heartbeat, stale-cred, guard)
  layer3/    — Methodology artifact chain (spec/plan/execute, memory recall)
  e2e/       — Full lifecycle (init, worktree, claim→wrap, doctor, conflict)
  multi/     — Multi-role, multi-user real-world scenarios
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
AIHUB_URL=http://10.146.0.16:8080
WORKSPACE_ROOT=/root/code/aicoding/gmi-ws-v3

Report each ASSERT line as PASS or FAIL with the actual value observed.
At the end print: SCENARIO PASS or SCENARIO FAIL (with failure count).
Always execute CLEANUP steps regardless of pass/fail.
```

## Test accounts

| Role | User | API Key |
|------|------|---------|
| admin | xiaokang.w | `baOHJg3Gh7JMpV5kW2Q1BHPqweg3y5Ig` |
| machine/writer | Test Agent Alice | `pf_k1_H36gVOed7wzTH4cPA1FpsG37qsia117V` |
| human/writer | Test Writer Bob | `pf_k1_NekUaAWXMdZf5WVfrpdmd7V8d1NVn1VR` |
| human/viewer(project) | Test Viewer Carol | `pf_k1_2j5gcKsUTBRazaEWydEQ1i4bDRwdR6Bh` |

Carol's project_roles = {marketplace: "viewer"} — can GET but not POST/PATCH on project resources.

## Credentials round-trip (HTTP direct-call scenarios)

When running scenarios that use raw HTTP calls (multi/ and some e2e/), follow this pattern:

```bash
# 1. Generate a fresh session secret (64-hex chars)
MY_SECRET=$(python3 -c "import secrets; print(secrets.token_hex(32))")

# 2. Claim and save the attempt_id
MY_ATTEMPT=$(curl -s -X POST $BASE/v1/work_items/$WI_ID/claim \
  -H "Authorization: Bearer $MY_KEY" -H "Content-Type: application/json" \
  -d "{\"idempotency_key\":\"unique-key\",\"session_info\":{\"machine_id\":\"my-host\",\"session_secret\":\"$MY_SECRET\"}}" \
  | python3 -c "import sys,json; print(json.load(sys.stdin)['attempt_id'])")

# 3. Use saved credentials in subsequent calls
curl -X POST $BASE/v1/work_items/$WI_ID/complete \
  -H "Authorization: Bearer $MY_KEY" -H "Content-Type: application/json" \
  -d "{\"status\":\"wrapped\",\"attempt_id\":\"$MY_ATTEMPT\",\"claim_epoch\":1,\"session_secret\":\"$MY_SECRET\"}"
```

Replace `<64hex>` / `<secret>` placeholders in scenario files with `MY_SECRET` bound this way.
For scenarios where step 2 uses MCP pf_claim_work_item, read credentials from:
`~/.polyforge/state/<wi_id>.json` → fields: `attempt_id`, `claim_epoch`, `session_secret`.

## Field format reference

### declared_resources (pf_create_work_item)
```json
[{"type": "repo", "uri": "repo:<repoName>", "intent": "exclusive",
  "task_branch": "polyforge/<branchName>"}]
```

### requested_locks (pf_claim_work_item)
```json
[{"resource_type": "git_branch", "resource_key": "<repo>/<branch>"}]
```

### pf_update_step responses
- `status="in_progress"` → `{status: "in_progress"}`
- `status="completed"` → `{status: "completed"}`
- `heartbeat=true` → `{status: "heartbeat_ok"}`

## File conventions

- `CALL:` — MCP tool invocation (or HTTP call for multi-user scenarios)
- `AS <user>:` — context switch to that user's API key
- `ASSERT:` — verify (fail if not met)
- `ASSERT_ERROR:` — expect error containing this string
- `CLEANUP:` — always run regardless of pass/fail
- `NOTE:` — informational
