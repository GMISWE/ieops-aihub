# 用 polyforge 干活

面向**使用** polyforge 做事的工程师。假设你已经读完 [`onboarding.md`](onboarding.md)
（装好 CLI、配好 API key、装好插件），并且至少建过一个 work item。这篇从那一步之后开始，
按**一次完整会话的时间顺序**走一遍：开工 → 它自己跑还是等你 → 你的代码在哪 → 收工。

---

## 1. 开工：`/pf-work`

### 新建一个 wi

```
/pf-work --goal "把 gateway 的跨区 SCAN 换成定向查询"
```

依次会发生：

1. **推断 `wi_type`**。候选来自你项目 scenario 仓里的 `*.md` 文件名前缀
   （`<workspace>/.repo/<scenario>/`，例如 `polyforge-coding` 下的
   `fix_bug` / `feature` / `chore` / `release` …）。选中的类型必须真的存在
   `<wi_type>.<project>.md` 或 `<wi_type>.md`，且文件里至少有一个 `## Step:` 小节；
   都不满足就退回内置的 `default`，并提示你一句。
2. **抽 content 草稿**。从当前对话里提取背景和上下文（不含方案，方案属于 spec/plan），
   给你确认或修改。对话里没料就跳过，content 是可选的。
3. **冲突预览**，然后创建 wi。
4. **问你一句**：`Created <slug> (<goal>). Claim and start working on it now?`
   回「是」当场认领；回「不」这个 wi 就留在队列上，以后再认领。

如果你只想建、不想认领（AI 在执行中途发现问题时就该这样），说明「静默模式 / silent create」。

### 认领一个已有的 wi

```
/pf-work aihub#330            # 认领队列里的
/pf-work aihub#330 --resume   # 恢复之前 --pause 掉的
/pf-work aihub#330 --force    # 从别人手里接管，必须给 reason
```

四条路径（新建后认领 / 认领 / 恢复 / 接管）认领成功后都会 `pf_recall` 这个 wi 关联的记忆并显示出来，
所以接手别人的活时，之前那位留下的交接笔记会自动出现在你面前。

pf-* 技能的回复统一是 `Result` / `Status` / `Next steps` 三段。

---

## 2. 它自己跑，还是等你？

认领之后的走向，由 wi 上 `requires_human_session` 这一个字段决定。
**`false`**：不出三段输出，直接把 `/pf-execute` 作为 subagent 派出去，照 step graph 一路跑到底，
你只看它报进度。**`true`**：停住，出三段输出，其中 `Next steps` 按 `wi_type` 从路由表机械填出来
（不是 AI 现编的），然后等你人工发话 —— 通常是你自己敲 `/pf-execute`，或先 `/pf-spec` 把范围定清楚。

这个值**在建 wi 的时候定下来、存在 wi 上**：客户端读 scenario 仓的 `<wi_type>.<project>.md`
frontmatter，没这个文件就退到 `<wi_type>.md`，两个都没有则取 `true`。
所以想知道你手上这类活属于哪种，去 scenario 仓打开对应文件看头几行就行 ——
例如 `fix_bug.aihub.md` 是 `false`，`critical_bug.ieops.md` 是 `true`。
服务端在 claim 时只兜底：wi 上该字段为 NULL 才写默认值 `true`，已有值原样沿用；
这也意味着**事后改 frontmatter 不会动到已经建好的 wi**。

---

## 3. 你的代码在哪

- **worktree**：`<workspace>/pf.<project>-<seq>/<repo>/`，例如 `pf.aihub-330/aihub/`。
  claim 会给项目下的**每个**仓都建一个，不只是你打算改的那个；完整的
  「仓名 → 绝对路径」映射在 claim 返回值的 `worktrees` 字段里。
- **分支**：进到 worktree 里跑 `git branch --show-current`。
  别照命名规则倒推 —— 分支名是 claim 时算出来的，算完**哪儿都没存**；规则本身也刚改过
  （旧的是 `polyforge/<8 位随机字符>`，新的带项目名和序号），你机器上那版二进制决定你拿到哪一种。
- **state 文件**：`<workspace>/.polyforge/state/<wi_id>.json`，权限 0600，里面是
  `attempt_id` / `claim_epoch` / `session_secret` / `worktrees` 这些字段。
  它是凭据文件：别手改，也别往聊天或日志里贴。

---

## 4. 收工：`/pf-stop`

三个 flag 选一个。**真正的差别在锁上**：

- **`/pf-stop --pause`** — attempt 结束、wi 变 `paused`。
  **只释放 `file_scope` 类型的锁；`git_branch` / `deploy_env` 的锁继续替你占着**，
  这样 `--resume` 回来时分支和环境还是你的。state 文件保留。
- **`/pf-stop --wrap`** — 成功终态。coding 场景走 `pf_wrap` = push + PR +
  `complete_attempt(wrapped)` + 删 state 文件；**这个 attempt 的锁全部释放**。
- **`/pf-stop --fail`** — 失败终态，`pf_complete_attempt(status="failed")`；
  和 wrap 一样**锁全部释放**、state 文件删掉。

两个常踩的点：

- `--wrap` / `--fail` 的收尾说明要作为 `note=` 参数跟终态调用**同一次**发出去。
  终态调用会删掉 state 文件，凭据没了，之后再补一个 `pf_emit_event` 会因为没凭据而失败。
- 终态**不会**帮你删 worktree 目录。自己 `git worktree remove`，或者跑 `polyforge doctor --fix`。

---

## 再往下

| 想知道 | 去哪 |
|---|---|
| 装机、配 key、装插件 | `docs/onboarding.md` |
| 全部 MCP 工具 | `docs/mcp-tools.md` |
| 命令速查 | 你 workspace 里的 `.polyforge/usage.md` |
| 架构与规格 | `docs/design/polyforge-v1-design.md` |
| 出问题了 | `/pf-doctor` |
