---
name: oncall-emit-storm
description: |
  Zhang is oncall during a prod incident. He claims wi_prod_outage and
  spawns 4 investigation agents in his terminal: grafana-watcher,
  kubectl-tailer, chaos-runner, codex-reviewer. All 4 share his
  AttemptCredential and emit progress notes in parallel.
cast:
  # Each "agent" is a separate cast member but all run as u_zhangsan (Zhang's
  # bastion shell spawned 4 sub-processes; they all share Zhang's bearer +
  # AttemptCredential implicitly because the actions below pass the cred as
  # explicit kwargs — there's no separate per-role credential store.
  - id: zhang
    user: u_zhangsan
    machine_id: zhang-bastion
  - id: agent-grafana
    user: u_zhangsan
    machine_id: zhang-bastion
  - id: agent-kubectl
    user: u_zhangsan
    machine_id: zhang-bastion
  - id: agent-chaos
    user: u_zhangsan
    machine_id: zhang-bastion
  - id: agent-codex
    user: u_zhangsan
    machine_id: zhang-bastion
expected_runtime_s: 12
---

# Oncall Emit Storm

## Background

PagerDuty just woke Zhang up — `/v1/login` is 500ing in prod. He claims
`wi_prod_outage_<timestamp>` and immediately launches a parallel
investigation. In his oncall console, he runs four `claude -p` agents in
parallel, each tailing a different signal: grafana metrics, kubectl logs,
a chaos repro, and a codex review of recent commits. All four agents
share Zhang's `AttemptCredential` (they're sub-processes of his session)
and each emits progress notes back to the wi as they find things.

This scenario tests:
- A single attempt can serve concurrent emit_event calls from multiple
  logical agents sharing one credential (no fence rejection)
- All emits persist with correct payload integrity (no overwrites)
- Event ordering is deterministic via (created_at, id)
- The wi's event log is the unified audit trail across all sub-agents

## Timeline

### T+0: Zhang claims the outage wi
@zhang:
  - skill: |
      /pf3-start --goal "investigate prod /v1/login 500 storm"
                 --project marketplace
    capture:
      work_item_id: $WI
      attempt_id: $RA
      claim_epoch: $EPOCH
      session_secret: $SECRET

### T+0.5: All 4 agents fire 5 emits each in parallel
@agent-grafana, @agent-kubectl, @agent-chaos, @agent-codex:
  - repeat: 5
    skill: |
      /pf3-note "{role} round {i}: probe payload"
                --work-item-id $WI
                --attempt-id $RA
                --claim-epoch $EPOCH
                --session-secret $SECRET

## Assertions

- sql:
    query: |
      SELECT count(*) FROM agent_events
      WHERE work_item_id = $WI AND event_type = 'note'
    expect_scalar: 20
- sql:
    query: |
      SELECT count(DISTINCT run_attempt_id) FROM agent_events
      WHERE work_item_id = $WI AND event_type = 'note'
    expect_scalar: 1
- sql:
    query: |
      SELECT count(*) FROM agent_events ae1
      JOIN agent_events ae2 ON ae1.id < ae2.id
       AND ae1.created_at > ae2.created_at
      WHERE ae1.work_item_id = $WI AND ae2.work_item_id = $WI
    expect_scalar: 0
    note: "the (created_at, id) ordering is monotonic — no inversions"
- no_payload_collision:
    query: |
      SELECT payload->>'role' AS role, payload->>'i' AS round
      FROM agent_events
      WHERE work_item_id = $WI AND event_type = 'note'
    expect_distinct_count: 20
