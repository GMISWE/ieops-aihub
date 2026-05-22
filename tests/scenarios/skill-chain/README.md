# Skill-chain Scenarios

These scenarios test the full Skill→MCP→aihub execution chain.
Unlike layer2/3/e2e scenarios that call MCP tools directly,
these require a Claude session with access to polyforge skills.

## Running

Dispatch a subagent with this prompt:

```
You are a polyforge v1 skill-chain test runner. Execute scenario SC-XX.
You have access to polyforge skills (invoke via Skill tool) AND pf_* MCP tools.
For each SKILL_INVOKE step: use the Skill tool to load the named skill, then
follow its instructions exactly (don't call MCP tools directly without the skill's guidance).
Report PASS/FAIL per assertion.
```

## Format conventions

- `SKILL_INVOKE:` — must use Skill tool, follow skill instructions
- `EXPECTED SKILL BEHAVIOR:` — what the skill should instruct Claude to do
- `ASSERT MCP CALLS:` — which MCP tools should be called as a result
- `ASSERT STATE:` — final system state to verify
- `AS <user>:` — context switch (use different API key for direct HTTP calls)
- `NOTE:` — informational

## Key difference from other scenarios

| Scenario type | Tests |
|---|---|
| layer2/ | Step management MCP tools in isolation |
| layer3/ | Methodology artifact chain (spec/plan/execute) |
| e2e/ | Full lifecycle via direct MCP calls |
| multi/ | Multi-role workflows via direct MCP/HTTP |
| skill-chain/ (this dir) | Skills guide correct MCP tool selection and sequencing |

Skill-chain scenarios are the highest-level integration tests: they verify that
the skill layer correctly translates user intent into the right MCP call sequence,
with correct parameters, in the right order.

## Scenario index

### SC-* (single-user skill chain)

| ID | Description | wi_type | Session |
|---|---|---|---|
| SC-01 | fix_bug full cycle: pf-work→prepare_context→code_change→commit_and_pr→pf-stop | fix_bug | 3 (auto) |
| SC-02 | feature with spec+plan: pf-work→pf-spec→pf-plan→code_change→commit_and_pr→pf-stop | feature | 2 (human-led) |
| SC-03 | pf-status LCRS six-segment view with wi's in all states | mixed | any |
| SC-04 | pf-retro extracts learnings after wrap | any | any |

### SR-* (multi-role realistic)

| ID | Description | Roles |
|---|---|---|
| SR-01 | Orchestrator + two concurrent agents (sprint backlog execution) | Admin, Alice, Bob |
| SR-02 | Human review gate: critical_bug stays in needs_human_session[] until Bob claims | Admin, Alice, Bob |
| SR-03 | Manager monitors queue, notices stall, force-takeovers and rescues | Alice, Admin |
| SR-04 | Sprint planning with priorities and dependencies; parallel agents | Admin, Alice, Bob |

## Test accounts

| Role | User | API key |
|------|------|---------|
| admin | xiaokang.w | `baOHJg3Gh7JMpV5kW2Q1BHPqweg3y5Ig` |
| machine/writer | Test Agent Alice | `pf_k1_H36gVOed7wzTH4cPA1FpsG37qsia117V` |
| human/writer | Test Writer Bob | `pf_k1_NekUaAWXMdZf5WVfrpdmd7V8d1NVn1VR` |

## Prerequisites

- polyforge MCP server running and connected
- WORKSPACE_ROOT=/root/code/aicoding/gmi-ws-v3 (or equivalent)
- All polyforge skills accessible via Skill tool
- AIHUB_URL=http://10.146.0.16:8080
