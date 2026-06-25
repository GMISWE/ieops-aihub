---
name: pf-help
description: >
  Use when the user asks how to use a /pf-* skill, what flags it takes, or wants a
  quick reference of the available polyforge skills.
---

# pf-help — /pf-* Skill Quick Reference

## Usage

**Purpose**: Show a quick reference of `/pf-*` skills — either all-at-once (table view) or a single skill's `## Usage` block.

**Pattern**: `/pf-help [<skill-name>]`

**Required**: none (no-arg prints the index; with `<skill-name>` prints one skill's Usage block)

**Flags**: none

## When to use

Any time the user asks "how do I use /pf-X?", "what flags does /pf-X take?", or wants a
quick reference of available pf skills — when a quick reference is faster than reading
the whole SKILL.md. This skill is a **read-only contract** — it prints help and stops.
It does NOT dispatch other skills or perform any wi-lifecycle action.

## Mechanic

This skill is short on purpose. The calling agent (you, when the user invokes
`/pf-help`) does the work; the body below is the contract.

### Mode A — no argument (`/pf-help`)

1. List all sibling skill directories under `plugins/polyforge/skills/`:

   ```
   pf-crystallize  pf-doctor    pf-execute
   pf-init         pf-plan     pf-project   pf-release
   pf-retro        pf-spec     pf-status    pf-stop
   pf-sync         pf-user     pf-work      pf-help
   using-polyforge
   ```

2. For each sibling, read `<sibling>/SKILL.md` and extract the `## Usage` block —
   specifically the `**Purpose**:` and `**Pattern**:` lines. Skip any skill whose
   SKILL.md has no `## Usage` block (and surface that as a one-line warning, since
   every skill is expected to have one after aihub#84).

3. Render a single grouped table:

   ```
   | Skill           | Purpose                                | Pattern                          |
   | --------------- | -------------------------------------- | -------------------------------- |
   | pf-work         | Enter the wi lifecycle …               | /pf-work [<slug>] [--resume|--force] |
   | pf-spec         | Produce the spec artifact …            | /pf-spec                         |
   | ...             | ...                                    | ...                              |
   ```

   Suggested grouping order: lifecycle (work / spec / plan / execute / stop / retro /
   crystallize / release / sync), management (project / user), platform
   (init / doctor / status), meta (using-polyforge / pf-help).

4. End with a one-line hint: "Run `/pf-help <skill-name>` for the full Usage block of
   any single skill, or read `plugins/polyforge/skills/<skill-name>/SKILL.md` for full
   docs (When-to-use, Mechanic, NL Triggers)."

### Mode B — with argument (`/pf-help pf-work`)

1. Normalize the argument:
   - Accept `pf-work`, `/pf-work`, or `work` — strip leading `/` and any leading
     `pf-` if the user typed `/pf-help work`.
   - Re-prefix to `pf-<arg>` if needed; `using-polyforge` is the one exception (no
     `pf-` prefix).

2. Resolve `<skill_dir> = plugins/polyforge/skills/<normalized>`.
   - If it does not exist: print "Unknown skill: <arg>. Available: <list from Mode A
     step 1>." and stop.

3. Read `<skill_dir>/SKILL.md`. Extract the `## Usage` block (from the line `## Usage`
   up to the next blank-line-then-`---` separator or the next `## ` heading).

4. Print verbatim, then append:

   ```
   Full docs: plugins/polyforge/skills/<normalized>/SKILL.md
              (sections: When to use, Mechanic, NL Triggers)
   ```

## Non-goals

- Do **not** invoke or dispatch the target skill — only print its Usage block.
- Do **not** edit any SKILL.md from inside `/pf-help`.
- Do **not** call any `pf_*` MCP tool — this skill is purely local-filesystem read.

## NL Triggers

- "/pf-help" / "/pf-help pf-work" / "pf help"
- "show pf commands" / "list polyforge skills"
- "how do I use /pf-X" / "pf-X usage" / "polyforge help"
