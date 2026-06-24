---
name: pf-crystallize
description: >
  Post-wrap skill — 将本次 wi session 的操作流程凝结为可复用的 {wi_type}.{project}.md
  workflow 文件，写入 scenario repo 并 push 到 main。由 pf-stop --wrap 触发调用。
---

# pf-crystallize — Workflow Crystallization

## Usage

**Purpose**: Crystallize a just-wrapped wi's workflow into a reusable scenario `{wi_type}.{project}.md` file in the scenario repo.

**Pattern**: `/pf-crystallize <source_wi_id> <wi_type_name> [--project <name>]`

**Required**: `<source_wi_id>` (the wrapped wi) and `<wi_type_name>` (snake_case `\w+`)

**Flags**:
- `--project <name>` — produce project-scoped `{wi_type}.{project}.md`; omit (or pass `通用` / `generic`) for project-agnostic `{wi_type}.md`
- Not user-invokable directly — dispatched by `/pf-stop --wrap` after the user opts in

## When to use

由 `/pf-stop --wrap` 在 wrap 完成后自动提示触发。传入参数：
- `source_wi_id`：刚刚 wrap 的 wi ID
- `wi_type_name`：用户输入的新 wi_type 名称

不直接用于用户调用。

## Mechanic

### Step 1: 引导问题

收集以下信息：
- `wi_type`：已从触发输入获取（仅含 `\w+` 字符，如 `deploy`、`data_migration`）
- `project`（可选）：
  - 用户输入 project 名 → 产出 `{wi_type}.{project}.md`
  - 回车跳过 / 输入"通用" → 产出 `{wi_type}.md`（无 project 后缀）
- `requires_human_session`（默认 false）：询问用户是否需要人工介入

**Early-exit guard**：若用户此时决定不固化（输入 "skip" / "不用" / 回车），立即输出"跳过固化。"并结束，不进行后续步骤。

### Step 2: 创建并认领 crystallize chore wi（IR1 合规）

所有文件写操作必须在 claimed worktree 内进行。

调用 `/pf-work` 静默模式创建 chore wi，然后立即认领：
```
使用静默模式：
goal: "crystallize {wi_type}[.{project}] from {source_slug}"
wi_type: chore
project: <source_wi.project>
```

pf-work 静默模式只创建并放入 queue，不询问。创建后立即调用 `/pf-work <slug>` 认领该 wi，在对应 worktree（`pf.<project>-N/polyforge-coding/`）内进行所有后续文件操作。

### Step 3: 提取步骤序列

结合两个信息源：

**3a. pf_read_events（结构化 timeline）**
```
events = pf_read_events(
  work_item_id=source_wi_id,
  limit=100
)
```
从 `step_completed` 事件提取步骤名称和 artifact_summary。

**3b. AI in-context window**
若在同一 session 内调用（session 内记得操作细节），补充 pf_read_events 缺失的细节。

> ⚠️ 跨 session 调用时 in-context 不可用，仅依赖 pf_read_events，产出质量依赖事件丰富程度。

综合两者，提取有序步骤列表：
- 每个步骤 `## Step: <id>`（下划线命名，如 `prepare_context`、`deploy_staging`）
- 按操作发生顺序排列

### Step 4: 公共 skill 提取

对每个步骤，扫描 `.repo/polyforge-coding/common/` 目录，用 LLM judgment 判断匹配度：

**a. 匹配现有 common/ skill（>80% 重合）**
→ 替换为 `@include: common/<name>/SKILL.md`
→ 若适用，附加 `level:` 参数（如 `level: quick` 用于 review）

**b. 新可复用逻辑**
→ 询问用户：
```
步骤 "<name>" 看起来可以提取为公共 skill，要写入 common/<name>/SKILL.md 吗？
```
→ 用户确认 → 生成 `common/<name>/SKILL.md`，步骤改为 `@include:`

**c. 专属逻辑**
→ inline 写在 `## Step:` 内容里

### Step 5: 生成草稿并展示

生成完整 workflow 文件（格式必须兼容 pf-execute）：

```markdown
---
requires_human_session: <true|false>
---

## Step: <step_id>
<@include: 或 inline 内容>
```

> **格式约束（pf-execute 兼容）**：
> - Frontmatter 只含 `requires_human_session`（bool）
> - 步骤标题严格格式：`## Step: <word>`（行首，`\w+` 匹配，无额外空格）
> - `@include:` 可附 `level:` 参数，必须是紧接下一行
> - Code fence 内的 `## Step:` 行不会被 pf-execute 识别为步骤

展示给用户：
```
--- 草稿预览 ---
{生成的 .md 内容}

确认写入（Enter）/ 修改（输入修改意见）/ 取消（skip）:
```

### Step 6: 写入 + commit + push（在 worktree 内）

用户确认后：

1. 写入 `{wi_type}[.{project}].md`（及新增的 `common/` 文件）到 polyforge-coding worktree
2. ```bash
   git add .
   git commit -m "feat(scenario): crystallize {wi_type}[.{project}] from {source_slug}"
   git push origin main
   ```
3. 输出："✅ 已固化并推送 `{filename}`，其他机器 `polyforge init` 后生效。"
4. 调用 `/pf-stop --wrap` 结束 crystallize chore wi

### Step 7: 用户取消路径（草稿阶段取消）

若用户在 Step 5 输入 "取消" / "skip"：
- 不写任何文件
- 调用 `/pf-stop --fail` 终止已认领的 crystallize wi（reason: "用户取消固化"）
- 输出："跳过固化。"

## NL Triggers

- 由 `pf-stop --wrap` 自动调用，传入 `source_wi_id` 和 `wi_type_name`
