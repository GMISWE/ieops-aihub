---
name: pf-work
description: >
  Start, claim, resume, or force-takeover a work item. Four modes: (A) create new wi
  (decoupled from claim — dialog mode asks, silent mode queues), (B) claim existing
  queued wi, (C) resume paused wi, (D) force-takeover an idle/expired wi.
---

# pf-work — Work Item Lifecycle Entry

## Usage

**Purpose**: Enter the wi lifecycle — create a new wi, claim a queued one, resume a paused one, or force-takeover a stalled one.

**Pattern**: `/pf-work [<slug>] [--resume | --force]`

**Required**: none (no-arg = create-new dialog; with `<slug>` = claim/resume/takeover)

**Flags**:
- `--resume` — resume a paused wi (Mode C)
- `--force` — force-takeover an idle/expired wi (Mode D); destructive against current claimer, requires `reason`
- `--silent` / silent-mode trigger — Mode A: create + queue without prompting to claim (NL trigger, not a literal CLI flag)

## When to use

Any time the user wants to begin working on something — new task, picking up a queued
item, resuming yesterday's work, or taking over a stalled wi from another agent.

## 架构规则

pf-work 是 wi 生命周期的**唯一创建入口**。不管是人还是 AI（包括 step 执行中途发现问题），
创建 wi 都必须通过本 skill。

调用模式：
- **对话模式**（默认）：人/AI 在 session 讨论中创建 wi → 创建后询问是否认领
- **静默模式**：AI 在 step 执行中途创建 wi → 调用时说明"使用静默模式"或"静默创建" → 只创建放 queue，不询问

## Mechanic

### Post-claim routing

See `## Post-claim 下一步 Routing` in `using-polyforge/SKILL.md` — that section is the
**single source of truth** for what to suggest in "下一步" after any claim, and applies
to **all** skills that emit 三段式 output (not just `pf-work`). `using-polyforge` is
auto-loaded into every session's context, so this backreference resolves reliably
(unlike an in-file anchor jump, which the LLM does not consistently follow at
generation time — see `mem_5obNUSSR`).

### Mode A — New wi (default, triggered by intent to start something new)

1. **Memory-First** (using-polyforge handles this at session start; surface results).

2. **Resolve wi_type from scenario repo**:

   Read the project's scenario clone from `.repo/` (cloned by `polyforge init`):
   ```bash
   scenario_url  = project.scenario  // from .polyforge.yaml
   scenario_name = <last path segment of URL, strip .git>
                   // "git@github.com:GMISWE/polyforge-coding.git" → "polyforge-coding"
   scenario_path = <workspace_root>/.repo/<scenario_name>/

   // Scenario not cloned yet?
   if scenario_path does not exist:
       STOP: "⚠️ Scenario repo 尚未克隆，请先运行 polyforge init。"

   // 从 .md 文件名推断合法 wi_type
   // 列出 scenario_path 下所有 *.md 文件，提取 {wi_type} 前缀（第一个 . 之前）
   // 排除 "default"
   available_wi_types = [
       f.split(".")[0]
       for f in os.listdir(scenario_path)
       if f.endswith(".md") and not f.startswith("default")
   ]

   // 验证（创建 wi 时）：
   // 检查 {wi_type}.{project}.md 或 {wi_type}.md 至少存在一个
   // has_step_sections：文件中至少含一个 ^## Step: 行
   def has_step_sections(filepath):
       with open(filepath) as f:
           return any(re.match(r"^## Step: \w+", line) for line in f)

   def validate_wi_type(wi_type, project, scenario_path):
       specific = f"{wi_type}.{project}.md"
       generic  = f"{wi_type}.md"
       for path, tag in [(specific, "ok"), (generic, "warn")]:
           full = f"{scenario_path}/{path}"
           if os.path.exists(full):
               if not has_step_sections(full):
                   return "error", None  # 文件存在但无 ## Step: sections
               return tag, path
       return "error", None      # 拒绝创建

   // requires_human_session：从 .md 文件 frontmatter 读取
   // project-specific 文件优先；fallback 到通用文件；都没有则默认 true
   def get_rhs(wi_type, project, scenario_path):
       for path in [f"{wi_type}.{project}.md", f"{wi_type}.md"]:
           full = f"{scenario_path}/{path}"
           if os.path.exists(full):
               fm = parse_frontmatter(full)
               return fm.get("requires_human_session", True)
       return True  # 默认
   ```

   AI infers wi_type from goal description + complexity, **matching against available_wi_types**:
   - Bug, root cause clear, small change → `fix_bug`
   - Bug, large impact or root cause unknown → `critical_bug`
   - Feature needing design decisions → `feature`
   - Simple maintenance, no design needed → `chore`
   - …other wi_types defined by .md files in the project's scenario repo

   **If no project scenario configured** OR **validate_wi_type returns "error"**:
   → Fall back to built-in `default` wi_type (`requires_human_session=true`, steps=[]).
   Notify user: "⚠️ 无法匹配 wi_type，使用 default（需人工介入）。"

   **If validate_wi_type returns "warn"**:
   → Proceed with the generic .md flow; notify user:
   "⚠️ 未找到 {wi_type}.{project}.md，将使用通用流程 {wi_type}.md。"

2b. **AI extracts content draft from conversation**:
    From the current session conversation, extract a content draft describing the problem:
    - Background: why does this wi exist, what triggered it
    - Context: relevant information, known constraints, related discussions
    - Do NOT include solution approach (that belongs in spec/plan)

    Show the draft to the user for confirmation/modification:
    ```
    --- content draft ---
    <extracted background and context>

    Confirm (press Enter) or modify:
    ```

    If conversation context is insufficient for meaningful content, skip (content is optional).
    Pass the confirmed draft as `content=<draft>` to pf_create_work_item.

3. **Conflict preview** (before creating):
   ```
   pf_predict_conflicts(declared_resources=<new wi's resources>, dry_run=true)
   ```
   Show impact. If hard conflict → stop and explain.

4. **Create** (do NOT claim yet):
   ```
   pf_create_work_item(
     project=<from .polyforge.yaml>,
     goal=<user_goal>,
     wi_type=<inferred>,
     requires_human_session=<from get_rhs(wi_type, project, scenario_path)>,
     priority=<inferred: urgent/high/normal/low>,
     labels=[...],
     content=<confirmed draft>
   )
   ```
   - `400 PROJECT_NOT_FOUND` → prompt to create project first
   - `409 DUPLICATE` → show existing wi, ask: "Continue new / Claim existing / Cancel"
   - `409 CANDIDATES` → show candidate list, ask user to choose

5. **Interactive confirmation** (对话模式) / **Silent** (静默模式):

   **对话模式**（默认）：
   Output: "已创建 <slug>（<goal[:40]>）。要现在认领并继续处理吗？"
   
   → 人说"是"/"要"/"claim" → 直接认领（跳过 predict_conflicts，wi 刚创建无锁）：
     ```
     pf_claim_work_item(
       work_item_id=<wi_id>,
       idempotency_key=<client ULID>,
       mode="fresh"
     )
     ```
     然后召回 wi 关联记忆：
     ```
     pf_recall(project=<wi.project>, work_item_id=<wi_id>, top_k=10)
     ```
     **rhs 路由**（wi.requires_human_session）：
     - `false` → 不输出三段式；立即以 subagent 方式 dispatch `/pf-execute`（subagent 自己输出执行进度）。
     - `true`  → 输出三段式（"下一步" 按 `using-polyforge` 的 `## Post-claim 下一步 Routing` 决定），等待人工介入。
   
   → 人说"否"/"不用"/"放着" → 输出三段式，wi 留 queue。

   **静默模式**（调用时说明"使用静默模式"或"静默创建"）：
   直接输出三段式，不询问，不 claim，wi 留 queue。

6. Output three-segment format.

---

### Mode B — Claim existing queued wi (`/pf-work <slug>`)

1. `pf_predict_conflicts(work_item_id=<slug>, dry_run=true)` → conflict preview
2. `pf_claim_work_item(work_item_id=<slug>, mode="fresh", ...)`
3. After successful claim — recall wi-linked memories:
   ```python
   wi_memories = pf_recall(
     project=<wi.project>,
     work_item_id=<wi_id>,
     top_k=10
   )
   ```
   Display any results so the agent has full historical context for this wi.
   This call is made in ALL claim modes (A/B/C/D) to ensure memories linked by previous
   claimers (including after force_takeover) are always surfaced.
4. **rhs 路由**（wi.requires_human_session）：
   - `false` → 不输出三段式；立即以 subagent 方式 dispatch `/pf-execute`（subagent 自己输出执行进度）。
   - `true`  → 输出三段式（"下一步" 按 `using-polyforge` 的 `## Post-claim 下一步 Routing` 决定），等待人工介入。

---

### Mode C — Resume paused wi (`/pf-work <slug> --resume`)

1. ```
   pf_claim_work_item(
     work_item_id=<slug>,
     mode="resume",
     idempotency_key=<client ULID>
     // Do NOT pass scenario_ref — COALESCE on server preserves the original pinned SHA
   )
   ```
   Restores: prepared workspace + step state from the previous attempt.

   > ⚠️ If this wi was originally claimed on a different machine, the pinned
   > `scenario_ref` SHA may not exist in the local clone. pf-execute will auto-fetch
   > if needed, but verify local scenario clone is current: `polyforge init`.
2. After successful claim — recall wi-linked memories:
   ```python
   wi_memories = pf_recall(
     project=<wi.project>,
     work_item_id=<wi_id>,
     top_k=10
   )
   ```
   Display any results so the agent has full historical context for this wi.
   This call is made in ALL claim modes (A/B/C/D) to ensure memories linked by previous
   claimers (including after force_takeover) are always surfaced.
3. Show step progress: "Resuming at step 2/4 (review)".
4. **rhs 路由**（wi.requires_human_session）：
   - `false` → 不输出三段式；立即以 subagent 方式 dispatch `/pf-execute`（subagent 自己输出执行进度）。
   - `true`  → 输出三段式（含步骤进度；"下一步" 按 `using-polyforge` 的 `## Post-claim 下一步 Routing` 决定），等待人工介入。

---

### Mode D — Force takeover (`/pf-work <slug> --force`)

Permission rules:
- `writer` can take over any running wi (claim is static ownership; takeover is always explicit)
- `admin` can take over any attempt at any time (must supply `reason`)

Steps:
1. `pf_force_takeover(work_item_id=<slug>, reason=<user input>)`
2. `pf_claim_work_item(mode="fresh", ...)` — fresh claim.
4. After successful claim — recall wi-linked memories:
   ```python
   wi_memories = pf_recall(
     project=<wi.project>,
     work_item_id=<wi_id>,
     top_k=10
   )
   ```
   Display any results so the agent has full historical context for this wi.
   This call is made in ALL claim modes (A/B/C/D) to ensure memories linked by previous
   claimers (including after force_takeover) are always surfaced.
5. **rhs 路由**（wi.requires_human_session）：
   - `false` → 不输出三段式；立即以 subagent 方式 dispatch `/pf-execute`（subagent 自己输出执行进度）。
   - `true`  → 输出三段式（"下一步" 按 `using-polyforge` 的 `## Post-claim 下一步 Routing` 决定），等待人工介入。

---

### State file management

After a successful claim, `<workspace>/.polyforge/state/<wi_id>.json` contains:
```json
{
  "wi_id": "wi_xxx",
  "attempt_id": "ra_xxx",
  "claim_epoch": 1,
  "workspace_root": "/path/to/workspace",
  "repo": "repo-name",
  "task_branch": "polyforge/<slug>"
}
```
`session_secret` is stored in this file by the MCP server and is never shown in output.

## NL Triggers

- "开始" / "新任务" / "new task" / "let's start" / "I want to work on"
- "认领 [slug]" / "claim [slug]" / "pick up [slug]"
- "继续 [slug]" / "resume [slug]" / "pick this back up"
- "接管 [slug]" / "takeover [slug]" / "force claim [slug]"
