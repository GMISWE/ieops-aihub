## NL Routing

This table **indexes** the non-obvious intent → operation mappings; it is **not** an
exhaustive keyword classifier. Infer the user's intent and route to the nearest match — if
nothing fits or the intent is ambiguous, **ask rather than guess**. Each `/pf-*` skill's own
`NL Triggers` section is the authoritative source for its triggers; this table is a
convenience index and may lag. NL Routing decides *what operation*; **Repo Routing** (below)
decides *which repo* the work lands in.

| 说什么 | 对应操作 |
|--------|---------|
| 今天有哪些活 / 派活 / ready queue | `pf_get_ready_queue` |
| 哪些活需要我拍板 / needs attention | `pf_get_ready_queue` → `needs_human_session[]` |
| 开始 / 新任务 / new / start | `/pf-work` (Mode A) |
| 认领 / claim + slug | `/pf-work <slug>` (Mode B) |
| 继续 / resume + slug | `/pf-work <slug> --resume` (Mode C) |
| 接管 / takeover + slug | `/pf-work <slug> --force` (Mode D) |
| 暂停 / pause | `/pf-stop --pause` |
| 完成 / done / wrap / 搞定 | `/pf-stop --wrap` |
| 失败 / abandon | `/pf-stop --fail` |
| 状态 / status / 进度 | `/pf-status` |
| 设计 / spec / brainstorm | `/pf-spec` |
| 计划 / plan | `/pf-plan` |
| 执行 / execute / run it | `/pf-execute` |
| 回顾 / retro | `/pf-retro` |
| 这个 bug / 调试 / debug | `/pf-spec` (debug variant) |
| 记录 / note / log | `pf_emit_event(event_type="note", ...)` |
| 初始化 / init / setup workspace | `/pf-init` |
| 诊断 / doctor / 连不上 | `/pf-doctor` |
| 发布 / release / cut | `/pf-release` |
| 创建 / 更新 / 列出 project | `/pf-project` |
| 同步 Jira/GitHub / push to external | `/pf-sync` |
| 用户管理 / 查身份 / 发 key / list users | `/pf-user` |
| 修订 spec/plan 按批注 / 处理批注 / revise per annotations / resolve review comments | `/pf-revise` |

### Disambiguation (context-dependent intents)

Some words route differently by context — check **(a)** is there a slug? **(b)** is there a
running/claimed wi this session? **(c)** are we mid-step inside a skill flow?

- **继续 / go on / next / 接着做** — if a skill flow or step is in progress → *proceed within
  it* (advance to the next step), **not** resume. Only "resume `<slug>`" / "继续 `<slug>`" for a
  **paused** wi routes to `/pf-work <slug> --resume`.
- **完成 / done / 搞定** — mid-step ("this step is done") → continue the flow; whole-wi
  ("收尾 / 这个 wi 完成了 / wrap") → `/pf-stop --wrap`.
- **开始 / start** — with a slug → claim it (Mode B); without → new wi (Mode A).
- **状态 / status** — inside a claimed wi → that wi's detail; otherwise → project ready queue.

When still ambiguous, state your interpretation and confirm before acting.
