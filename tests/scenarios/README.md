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

## Running a scenario

Dispatch a subagent with:

```
You are a polyforge e2e test runner. Execute the scenario at:
  tests/scenarios/<path>.md

Use the polyforge MCP tools (pf_*) available in this session.
AIHUB_URL=http://10.146.0.16:8080  API_KEY=baOHJg3Gh7JMpV5kW2Q1BHPqweg3y5Ig
Report each ASSERT line as PASS or FAIL with the actual value observed.
At the end print: SCENARIO PASS or SCENARIO FAIL (with failure count).
Clean up any wi/state you created.
```

## File conventions

- `CALL:` — MCP tool invocation
- `ASSERT:` — field/condition to verify (fail if not met)
- `ASSERT_ERROR:` — expect an error containing this string
- `CLEANUP:` — always run regardless of pass/fail
- `NOTE:` — informational, not asserted
