# Harness probe: `.claude/agents/*.md` as the substrate for aihub#338

Measured **2026-09-06** on Claude Code **2.1.258**, box `gmi-ws`.
Work item **aihub#369**. Nothing here is quoted from documentation — every row is a
command that was run and an output that came back. Where the official docs disagree
with a measurement, the measurement is recorded as the fact and the doc claim is
recorded as falsified.

## Instruments

Two independent ones, because they do **not** measure the same thing:

| Instrument | What it actually exercises |
|---|---|
| `claude --agent <name> -p "GO"` | the agent as the **main session agent** |
| `claude -p --output-format stream-json --verbose "Call the Agent tool … subagent_type=…"` | the agent as a **dispatched subagent** |

🔴 They diverge. `skills:` preload fires in the subagent path and **not** in the
`--agent` main-session path; git status is present in the subagent path and absent in
the `--agent` path. Any conclusion about aihub#338 must come from the **subagent**
instrument, because that is what `pf-execute` dispatches.

Every probe agent's body forbids all tool calls and demands a fixed field block. The
zero-tool-call criterion is **not** the agent's self-report: the harness itself reports
`tool_uses: 0` in the `<usage>` block of every Agent tool result quoted below.

---

## ① Plugin-provided `agents/` auto-registers — YES

Probe files were placed in `…/polyforge/1.1.22/agents/` **and** `…/1.1.23/agents/`
with different identity strings, because this box has a session bound to 1.1.22 while
`installed_plugins.json` records 1.1.23.

Negative control (`--agent` with a name that does not exist) prints the whole roster:

```
$ claude --agent zzprobe-368-doesnotexist -p "GO"
--agent 'zzprobe-368-doesnotexist' not found. Available agents: claude, Explore,
general-purpose, Plan, polyforge:zzprobe-368-plugin, polyforge:zzprobe-368-plugin-noskill,
statusline-setup, zzprobe-368-user, zzprobe-368-user-noskill
```

Both plugin files are in the roster, under the scoped name `polyforge:<name>`.
They also resolve for real, by bare name and by scoped name, and they dispatch as
subagents (`subagent_type="polyforge:zzprobe-368-plugin"` and `="zzprobe-368-plugin"`
both returned the probe block).

Controls that make this conclusion binding:

- **positive control** — a user-level agent in `~/.claude/agents/` resolved in the same
  run, so the mechanism is not broken on this box;
- **negative control** — a nonexistent name errored, so resolution is real and not a
  silent fallback to the default agent;
- **identity strings** — every plugin-agent response came back
  `AGENT_IDENTITY=…@PLUGIN-CACHE-1.1.23`, never `@PLUGIN-CACHE-1.1.22`.

### Which cache version a new process binds — 1.1.23

A new `claude` process binds the `installPath` in `installed_plugins.json`
(**1.1.23**), not whatever an already-running session happens to hold (1.1.22).
Relevant to aihub#365. Placing the probe in only one of the two would have produced a
false "does not register".

### Precedence: a user-level file shadows the plugin on the bare name

```
$ claude --agent zzprobe-368-plugin -p "GO"           # after adding a user-level file of the same name
AGENT_IDENTITY=zzprobe-368-plugin@USER-LEVEL-SHADOW
$ claude --agent polyforge:zzprobe-368-plugin -p "GO"
AGENT_IDENTITY=zzprobe-368-plugin@PLUGIN-CACHE-1.1.23
```

⇒ `polyforge:<name>` is the shadow-proof handle. The bare name is overridable, which is
an escape hatch, not a defect — but polyforge's own dispatches must use the scoped form
or they can be silently redirected by any file a user drops in `~/.claude/agents/`.

---

## ② `skills:` frontmatter on a **plugin** agent — WORKS, no plugin-specific restriction

Canary design: the probe skill's marker string lives **only in the body**, never in the
`description`, so it cannot leak through the skill listing every agent receives.

| dispatched subagent | `skills:` | `SKILL_PRELOADED` | canary reproduced |
|---|---|---|---|
| `zzprobe-368-user` (user scope) | yes | YES | verbatim |
| `zzprobe-368-user-noskill` (user scope) | no | NO | NONE |
| `polyforge:zzprobe-368-plugin` (plugin scope) | yes | YES | verbatim |
| `polyforge:zzprobe-368-plugin-noskill` (plugin scope) | no | NO | NONE |

All four at `tool_uses: 0`. The user-scope pair is the control group; plugin scope
behaves identically, so there is **no plugin-specific limitation** on `skills:`.

Both `skills: [using-polyforge]` and `skills: [polyforge:using-polyforge]` resolved
(token deltas +991 and +1,016 against baseline — statistically the same).

### 🔴 But preloading `using-polyforge` does not deliver the polyforge rules

`skills/using-polyforge/SKILL.md` is **3,682 bytes on disk** and is a *manifest*: a
maintainer comment plus eight `@include:` directives. The ~8.4 KB resident payload is
**assembled at SessionStart by `hooks/pf-session-start`**, not by the harness.

The harness's skill preloader injects the file verbatim; it does not run polyforge's
include expansion. Measured proof: the preload delta was **+1,016 tokens**, matching
3,682 bytes, not the ~3,400 it would take if the fragments had been expanded. The
canary the agent returned was the manifest's `MAINTAINER NOTES:` line — a manifest
line, not a rule.

⇒ `skills: [using-polyforge]` costs ~1,000 tokens per dispatch and hands the step agent
a list of include directives. It is not a way to give step agents the iron rules.

---

## ③ Custom agents DO get `MEMORY.md` — the doc claim is falsified, with the mechanism identified

The official doc says a non-fork subagent never inherits "the main conversation's auto
memory". Measured, on this build, across every entry point:

| dispatched subagent | `MEMORY_MD` | `CLAUDEMD_USER` | `CLAUDEMD_PROJECT` | `GIT_STATUS` |
|---|---|---|---|---|
| `general-purpose` | **YES** | YES | YES | YES |
| user-level custom | **YES** | YES | YES | YES |
| plugin-level custom | **YES** | YES | YES | YES |
| `Explore` | **NO** | NO | NO | NO |
| `Plan` | **NO** | NO | NO | NO |

Every YES came with verbatim content at `tool_uses: 0`:

```
MEMORY_MD_FIRSTLINE=# Memory Index
MEMORY_MD_ENTRY=- [hf_xet 在小内存容器必 OOM](hf-xet-ooms-small-cgroup.md) — `HF_HUB_DISABLE_XET=1`
```

Both verified against the file rather than taken on trust:

```
$ head -1 /root/.claude/projects/-root-code-aicoding-gmi-ws/memory/MEMORY.md
# Memory Index
$ grep -n 'hf_xet 在小内存容器必 OOM' /root/.claude/projects/-root-code-aicoding-gmi-ws/memory/MEMORY.md
123:- [hf_xet 在小内存容器必 OOM](hf-xet-ooms-small-cgroup.md) — `HF_HUB_DISABLE_XET=1`
```

### The discriminator

The docs define `Explore`/`Plan` as the two agents that skip **CLAUDE.md and git
status** — they say nothing about those two skipping auto memory. Yet `MEMORY.md`
disappears for exactly those two and survives everywhere else, flipping in lockstep
with CLAUDE.md and git status across the only boundary available.

⇒ `MEMORY.md` rides the **CLAUDE.md file-injection channel**, and the doc's "auto
memory" means the `memory: user|project|local` frontmatter feature
(`~/.claude/agent-memory/<name>/`) — same word, different object. Our 2026-09-04
measurement stands; the aihub#283 entry that said subagents do not inherit auto memory
is wrong about `MEMORY.md`.

**Consequence for aihub#338**: a custom agent — at either scope — inherits the full
CLAUDE.md hierarchy, `MEMORY.md`, and the git-status snapshot. Whatever a step agent
needs from those, it already has. This is one place where the choice of scope buys
nothing, because both scopes measured identical.

---

## ④ Cost of preloading a `using-polyforge`-sized payload — ≈ 2,900 tokens per dispatch

`subagent_tokens`, reported by the harness in each Agent tool result. Same outer
prompt, same cwd, same probe body; only the `skills:` entry differs.

| preloaded file | bytes | `subagent_tokens` | Δ vs baseline |
|---|---:|---:|---:|
| — (user scope, no `skills:`) | 0 | 33,937 | baseline |
| — (plugin scope, no `skills:`) | 0 | 33,961 | +24 (noise floor) |
| — (`general-purpose`) | 0 | 34,058 | +121 |
| tiny canary skill | 467 | 34,211 | +274 |
| `using-polyforge` (manifest, bare name) | 3,682 | 34,928 | +991 |
| `using-polyforge` (manifest, scoped name) | 3,682 | 34,953 | +1,016 |
| **assembled resident payload** (8 fragments) | 8,174 | 36,831 | **+2,894** |

The last row is the number that matters: a skill body the size of the real assembled
payload costs **≈ 2,900 tokens per dispatch**. `feature.aihub.md` has 9 steps,
`fix_bug.aihub.md` 7, `chore.aihub.md` 6 ⇒ **≈ 17k–26k tokens per work item** of pure
preload, before the step does any work.

Ratio on the CJK-heavy payload: ~2.8 bytes/token, plus a fixed per-skill wrapper of
roughly 150 tokens.

---

## Reproduction

Probe agents (`zzprobe-368-*`) were placed in `~/.claude/agents/`, in
`…/polyforge/1.1.22/agents/` and in `…/polyforge/1.1.23/agents/`; probe skills in
`~/.claude/skills/`. All were removed after the run; nothing else under the plugin
cache was touched. Each probe body forbids tool use and demands a fixed field block;
the harness-reported `tool_uses: 0` — not the agent's own claim — is the criterion that
the answer came from context rather than from a `Read`.
