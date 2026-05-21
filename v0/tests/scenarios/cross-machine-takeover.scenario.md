---
name: cross-machine-takeover
description: |
  Wang loses Wi-Fi mid-edit on his mbp; lease expires; he resumes from
  desktop. Exercises full lease-expiry → ABA-safe takeover lifecycle
  through the polyforge skill stack (not direct HTTP).
env:
  AIHUB_LEASE_SECONDS: "2"
cast:
  - id: wang-mbp
    user: u_wangwu
    machine_id: wang-mbp
  - id: wang-desktop
    user: u_wangwu
    machine_id: wang-desktop
expected_runtime_s: 15
---

# Cross-Machine Takeover

## Background

Wang is debugging a search-results regression on his MacBook in the
office. He claims `wi_fix_search_regression` at 14:00 and starts editing.
At 14:05 he heads home; his MBP suspends on the train and the lease
renewer dies with it. By the time he opens his desktop at 14:30, the
lease is long expired. He runs `/pf3-resume` on the desktop and expects
to pick up exactly where he left off.

This scenario tests:
- pf3-start creates + claims a wi atomically
- A dead lease releases the wi for takeover after expiry
- pf3-resume from a different machine succeeds with epoch+1
- The old attempt is recorded as superseded (not lost)
- The takeover lineage (parent_attempt_id) is preserved
- agent_events captures the full takeover audit trail

## Timeline

### T+0: Wang on mbp starts work
@wang-mbp:
  - skill: |
      /pf3-start --goal "fix search-results regression"
                 --project marketplace
    capture:
      work_item_id: $WI
      attempt_id: $RA1
      claim_epoch: $EPOCH1

### T+0.1: Wang writes a WIP note locally
@wang-mbp:
  - bash: |
      mkdir -p {workspace}/scratch
      echo "WIP: investigate /v1/search filter regression" \
        > {workspace}/scratch/wang-progress.txt

### T+3: Lease expires (simulated wifi drop — no renewer)
- sleep: 3

### T+3.1: Wang on desktop resumes
@wang-desktop:
  - skill: /pf3-resume $WI
    capture:
      attempt_id: $RA2
      claim_epoch: $EPOCH2

## Assertions

- compare: $EPOCH1 == 1
- compare: $EPOCH2 == 2
- compare: $RA1 != $RA2
- sql:
    query: |
      SELECT status FROM run_attempts WHERE id = $RA1
    expect_first_row:
      status: superseded
- sql:
    query: |
      SELECT parent_attempt_id FROM run_attempts WHERE id = $RA2
    expect_first_row:
      parent_attempt_id: $RA1
- sql:
    query: |
      SELECT count(*) FROM agent_events
      WHERE work_item_id = $WI AND event_type = 'attempt_taken_over'
    expect_scalar: 1
- file:
    path: "{workspace}/scratch/wang-progress.txt"
    contains: "investigate /v1/search filter regression"
