---
name: standup-claim-race
description: |
  PM queued wi_login_500 last night. At standup, both Zhang and Wang see
  it and fire /pf3-claim near-simultaneously. Server picks exactly one
  winner; the loser sees a clear ownership error.
cast:
  - id: pm-qian
    user: u_zhangsan      # PM uses zhangsan as proxy (no u_qian in fixtures yet)
    machine_id: qian-mbp
  - id: dev-zhang
    user: u_zhangsan
    machine_id: zhang-mbp
  - id: dev-wang
    user: u_wangwu
    machine_id: wang-mbp
expected_runtime_s: 10
---

# Standup Claim Race

## Background

The team uses polyforge to assign work. Last night PM (Qian) created
`wi_login_500` as a queued work_item to track an oncoming login 500
issue. This morning at 09:05 standup, Zhang and Wang both notice the wi
in their `/pf3-list-work-items` output. Both are eager to fix it. They
both press `/pf3-claim wi_login_500` within milliseconds.

This scenario tests:
- A queued wi created without auto-claim stays in 'queued' status
- Concurrent claim from two distinct users picks ONE winner atomically
- The losing claim receives a clear CONFLICT_BUSY_NOT_TAKEOVER_ELIGIBLE
  error with the winner's identity in details
- agent_events records only the winner's attempt_started (no orphan rows)

## Timeline

### T+0: PM creates the queued wi
@pm-qian:
  - skill: |
      /pf3-start --no-claim
                 --goal "fix /v1/login 500 errors"
                 --project marketplace
    capture:
      work_item_id: $WI

### T+0.5: Both devs see it and race to claim
@dev-zhang, @dev-wang:  # parallel cast — actions execute concurrently
  - skill: /pf3-claim $WI
    capture:
      attempt_id: $MY_RA
      claim_epoch: $MY_EPOCH
    on_error:
      code: $ERR_CODE

## Assertions

- one_succeeds:
    actors: [dev-zhang, dev-wang]
    success_capture: MY_RA           # var name (the `capture:` target above)
    failure_capture: $ERR_CODE       # informational only — see `loser:` below
- loser:
    var: $ERR_CODE
    err_code: CONFLICT_BUSY_NOT_TAKEOVER_ELIGIBLE
- sql:
    query: |
      SELECT count(*) FROM run_attempts WHERE work_item_id = $WI
    expect_scalar: 1
- sql:
    query: |
      SELECT count(*) FROM agent_events
      WHERE work_item_id = $WI AND event_type = 'attempt_started'
    expect_scalar: 1
- sql:
    query: |
      SELECT status, current_attempt_id IS NOT NULL AS has_attempt
      FROM work_items WHERE id = $WI
    expect_first_row:
      status: running
      has_attempt: true
