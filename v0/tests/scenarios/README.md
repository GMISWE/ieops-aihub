# Scenario tests (LLM-driven)

Natural-language test scenarios that simulate humans + AI agents collaborating
via polyforge. Unlike `tests/v3/wire_multi_agent/*.py` (which hit aihub's HTTP
API directly via pytest), these scenarios are **executed by Claude Code
subagents that actually invoke polyforge skills**. They test the full chain:

```
scenario.md
  → orchestrator (Claude Code session)
    → spawn N subagents (one per cast role)
      → each subagent invokes /pf3-* skills
        → MCP server
          → aihub (PG)
```

This catches bugs that the Python suite cannot:
- Skill-level usability problems (wrong defaults, missing kwargs)
- MCP wrapper bugs (the schema stringify issue we hit)
- Cross-session lease lifecycle (separate Claude Code sessions = separate
  Python processes = real multi-process concurrency at aihub)

## Scenario file format

Each `*.scenario.md` has YAML frontmatter + structured Markdown sections.

```markdown
---
name: <kebab-case-id>          # required, also the filename stem
description: |                  # required, ≤ 200 chars
  <one paragraph of human-readable context>
env:                            # optional env overrides for aihub
  AIHUB_LEASE_SECONDS: "2"
cast:                           # required, ≥1 role
  - id: <kebab-id>              # the "@<id>" handle used in timeline
    user: u_<username>          # must exist in REFERENCE_USERS (zhangsan/lisi/wangwu)
    machine_id: <machine>
    session_secret: <64-hex>    # optional; runner generates per-role if omitted
expected_runtime_s: 10          # optional sanity ceiling
---

# Title

## Background
(prose explaining the real-world situation being modeled)

## Timeline

### T+0: <action description>
@<cast-id>:
  - skill: /pf3-start --goal "<goal>" --project <project>
    capture:
      work_item_id: $WI
      attempt_id: $RA1
  - bash: echo "WIP" > {workspace}/scratch/note.txt

### T+0.5: <next action>
- sleep: 2.5

### T+3: <next>
@<cast-id-2>:
  - skill: /pf3-resume $WI
    capture:
      claim_epoch: $EPOCH2

## Assertions

- compare: $EPOCH2 == 2
- sql:
    query: |
      SELECT status FROM run_attempts WHERE id = $RA1
    expect_first_row:
      status: superseded
- sql:
    query: |
      SELECT count(*) FROM agent_events
      WHERE work_item_id = $WI AND event_type = 'attempt_taken_over'
    expect_scalar: 1
- file:
    path: "{workspace}/scratch/note.txt"
    contains: "WIP"

## Cleanup

(optional; runner falls back to truncating PG between scenario runs)
```

## Vars

- `$NAME` — set by `capture` blocks, available in later actions + assertions
- `{workspace}` — runner-provided workspace root (tmp dir per scenario)

## Execution

Two runtimes, each with its own default mode:

### Python pytest runner — Mode 1 (deterministic, CI-friendly)

```
pytest tests/scenario_runner/test_scenarios.py
```

The pytest runner inlines all cast roles into the test process — direct
MCP / HTTP calls, no subagents (pytest can't spawn Claude `Agent` tools).
Fast (<30s per scenario), deterministic, what CI runs.

Use when: iterating on scenario authoring, running suite-wide checks,
guarding against regressions.

### `/execute-scenario` skill — Mode 2 (subagent-driven, default)

```
/execute-scenario tests/scenarios/cross-machine-takeover.scenario.md
```

The skill (host plugin `polyforge-v3/skills/execute-scenario/`) defaults
to spawning one Claude Code `Agent` subagent per cast member. Each
subagent runs its slice of the timeline by invoking real skills + MCP
tools. Captures + sleeps are coordinated via the orchestrator. To force
inline-mode from the skill (slower per-role serialization but no subagent
overhead): pass `--mode inline`.

Use when: testing the skill flow as a real user would experience it,
investigating bug reports about the skill stack, exercising
LLM-driven natural-language → tool-call paths.

**SQL assertions** require PG creds; in Mode 2 subagents typically
don't have those — see SKILL.md "SQL assertions in Mode 2".

## File naming

`<scenario-id>.scenario.md` — single-file per scenario. No nested dirs.

## Authoring guidance

- Tell the **story first** (## Background prose). The runner doesn't parse
  it — humans need it.
- Keep timeline actions imperative + parseable. Don't write "agent then
  decides to..."; write "skill: /pf3-claim $WI".
- Every capture should be referenced in an assertion. Unused captures
  = dead code.
- Assertions are the ONLY ground truth. The scenario passes iff all
  assertions hold.
