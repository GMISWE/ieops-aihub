---
name: pf-status
description: >
  Use when the user wants to know what is happening: progress on a claimed wi, the
  project ready queue, or which items are stalled, blocked, or need attention.
---

# pf-status — Work Item Status & Ready Queue

## Usage

**Purpose**: Inspect current wi progress or the project-wide ready queue (LCRS six segments).

**Pattern**: `/pf-status [--all]`

**Required**: none (no-arg inside a claimed wi → single-wi view; no-arg outside → global view; `--all` forces global)

**Flags**:
- `--all` — force global LCRS view even when a wi is currently claimed

## When to use

Any time the user wants to know what is happening — current wi progress, team-wide
ready queue, or which items are stalled/blocked.

## Mechanic

### Single wi view (inside a claimed wi)

1. ```
   pf_list_work_items(
     ids=[<current_wi_id>],
     include_step_state=true
   )
   ```

2. ```
   pf_read_events(
     work_item_id=<current>,
     limit=5,
     pinned_first=true
   )
   ```

3. Render three-segment output with full wi fields + last 5 events.

---

### Global view (`/pf-status` without an active wi, or `/pf-status --all`)

1. ```
   pf_get_ready_queue(project=<from .polyforge.yaml>)
   ```
   Returns all six segments in one call.

2. Render LCRS (Layer-3 Concurrent Ready State) view:

```
📋 LCRS — project: <name>
──────────────────────────────────────────────────
🏃 running      (2): wi_xxx "fix login bug" [Alice, attempt #3, 28min left]
                     wi_yyy "add rate limiting" [Bot-1, attempt #1, 45min left]
⚡ items         (5): wi_aaa (urgent) "critical DB migration" [queued]
                     wi_bbb (high)   "auth refactor" [queued]
                     ... +3 more
⚠️  stalled       (1): wi_ccc "upgrade deps" blocked by: wi_ddd
⏸  paused        (0):
👤 needs you     (1): wi_eee "design new billing API" [requires_human_session]
❓ unclassified   (2): wi_fff (wi_type not set — run /pf-work <slug> to classify)
                     wi_ggg
```

3. Highlight segments needing attention:
   - `needs you` → bold, shown first
   - `stalled` → show blocker wi slug
   - Expired leases in `running` → flag with ⏰

4. Output three-segment format. "Next steps" section suggests the most important next action
   (e.g., "2 items need human-led sessions — run `/pf-work <slug>` to start").

## Output format notes

- `running`: show attempt owner (display name), attempt number, lease expiry
- `items` (ready queue): show priority, truncated goal (60 chars), status
- `stalled`: show which wi_id is blocking
- `needs you`: these are `requires_human_session=true` wi's awaiting a human session
- `unclassified`: wi's with `wi_type=NULL` that cannot be claimed until classified

## NL Triggers

- "status" / "progress" / "what's going on"
- "what work is there today" (also triggers Session 1 Orchestrator round via using-polyforge)
- "what needs me" / "needs my attention"
- "ready queue" / "show queue" / "show all"
- `/pf-status` (direct command)
