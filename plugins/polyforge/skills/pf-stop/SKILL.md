---
name: pf-stop
description: >
  Stop work on the current wi. Three modes: pause (release lease, keep locks, status →
  paused), wrap (terminal success), or fail (terminal failure).
---

# pf-stop — Stop / Complete Work Item

## Usage

**Purpose**: Terminate or pause work on the currently-claimed wi.

**Pattern**: `/pf-stop { --pause | --wrap | --fail }`

**Required**: exactly one of the flags below

**Flags**:
- `--pause` — release lease, keep locks, status → paused (resumable via `/pf-work <slug> --resume`)
- `--wrap` — terminal success; emits wrap note, calls `pf_wrap`, deletes state file (destructive)
- `--fail` — terminal failure; emits failure note, calls `pf_complete_attempt(failed)`, deletes state file (destructive)

## When to use

When the user is done working (wrap), needs to pause and come back later (pause), or
encounters a terminal failure that cannot be resolved in this session (fail).

## Mechanic

### Mode: pause (`/pf-stop --pause`)

1. If any step is in `in_progress` state:
   ```
   pf_update_step(
     work_item_id=<current>,
     step_id=<in_progress step>,
     status="failed",
     step_attempt_id=<current step_attempt_id>
   )
   ```
   This resets the step so it can be retried on resume.

2. Release lease (keep locks so no one else can claim the same resources):
   ```
   pf_complete_attempt(
     work_item_id=<wi_id>,
     status="paused"
   )
   ```

3. State file (`<workspace>/.polyforge/state/<wi_id>.json`) is kept for `/pf-work --resume`.

4. Output three-segment format.

---

### Mode: wrap (`/pf-stop --wrap`)

> **IMPORTANT**: Emit the wrap note BEFORE calling the terminal tool. `pf_wrap` and
> `pf_complete_attempt(wrapped)` delete the state file; any `pf_emit_event` after them
> fails because the MCP server can no longer read credentials.

1. Emit wrap note **first**:
   ```
   pf_emit_event(
     work_item_id=<current>,
     event_type="note",
     payload={text: "wrapped: <1-sentence summary of what was accomplished>"}
   )
   ```

2. Then call the terminal wrap — **coding scenario**:
   ```
   pf_wrap(
     workspace_root=<ws>,
     work_item_id=<current>
   )
   ```
   `pf_wrap` = `on_wrap` hook + `pf_complete_attempt(wrapped)` + workspace cleanup.
   Credentials (`attempt_id`, `claim_epoch`, `session_secret`) are injected automatically
   by the MCP server from the state file.

   **Non-coding scenario**:
   ```
   pf_complete_attempt(
     work_item_id=<current>,
     status="wrapped"
   )
   ```

3. State file is deleted by `pf_wrap` / `pf_complete_attempt`; no manual delete needed.

4. Suggest: "Run `/pf-retro` to save learnings to team memory."

5. Output: "✨ 这次的操作流程值得固化为 wi_type 吗？（输入名称固化，或回车跳过）"

   - 用户输入名称 → 调用 pf-crystallize，传入：
     - source_wi_id=<wrapped_wi_id>
     - wi_type_name=<用户输入>
   - 跳过/空输入 → 结束

6. Output three-segment format with wrap summary:
   - Goal achieved
   - Key decisions made
   - Follow-up items (if any)

---

### Mode: fail (`/pf-stop --fail`)

> **IMPORTANT**: Emit the failure note BEFORE calling `pf_complete_attempt(failed)`.
> The terminal call deletes the state file; `pf_emit_event` after it will fail.

1. Emit failure note **first**:
   ```
   pf_emit_event(
     work_item_id=<current>,
     event_type="note",
     payload={text: "failed reason: <user description of why it failed>"}
   )
   ```

2. Then mark the attempt as failed:
   ```
   pf_complete_attempt(
     work_item_id=<current>,
     status="failed"
   )
   ```

3. State file is deleted by `pf_complete_attempt`; no manual delete needed.

4. Output three-segment format. Suggest: "Create a bug wi with `/pf-work --goal 'bug: ...'`
   to track the root cause."

## NL Triggers

- "暂停" / "pause" / "save and come back later" / "take a break"
- "完成" / "done" / "wrap" / "搞定了" / "ship it" / "finished"
- "失败" / "failed" / "give up" / "abandon" / "this is broken"
