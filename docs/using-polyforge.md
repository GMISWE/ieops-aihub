# 用 polyforge 干活

面向**使用** polyforge 做事的工程师。假设你已经读完 [`onboarding.md`](onboarding.md)
（装好 CLI、配好 API key、装好插件），并且至少建过一个 work item。这篇从那一步之后开始，
按**一次完整会话的时间顺序**走一遍：开工 → 它自己跑还是等你 → 你的代码在哪 → 收工。

**下面这些 `/pf-xxx` 不用背。** 说人话就行 ——「开个 wi，把跨区 SCAN 改掉」「这条先停一下」「搞定了」——
助手按 `using-polyforge` skill 里的 **NL Routing** 表把意图落到操作上；工作区的 `.polyforge/usage.md`
里另有一份中文触发词，「搞定」「拍板」这类没有英文对应的说法也在其中。文中照旧写出命令名，因为它们是精确形式，
而且会出现在输出里，你得认得。但那张表自己规定**意图不明时先问再做**：说得含糊，你先收到的是一句反问。

---

## 1. 开工：`/pf-work`

### 新建一个 wi

```
/pf-work --goal "把 gateway 的跨区 SCAN 换成定向查询"
```

依次会发生：

1. **推断 `wi_type`**。候选来自你项目 scenario 仓里的 `*.md` 文件名前缀
   （`<workspace>/.repo/<owner>__<repo>/`，例如 `GMISWE__polyforge-coding` 下的
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
（不是 AI 现编的），然后等你人工发话 —— 通常是你说一句「执行」（`/pf-execute`），或先把范围定清楚（`/pf-spec`）。

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

`--wrap` 还要一个**类还是实例**的标（scenario 仓 `common/commit_and_pr` 的 Step 4.5），PR body 和
wrap note 里各写一次：**A** 类已关闭（给出闸的 `file:line`，并展示它修复前会红）· **B** 只修了实例
（给出跟踪那个仍开着的类的 wi 编号）· **C** 做不出闸（仅当本次没改代码）。问它是想让「这个 bug 会不会
再来」当场就有答案。**没有任何东西检查你答得诚不诚实** —— `.ci` 只保证这条要求还在模板里，wrap note
是运行时数据，到不了 scenario 仓。靠自觉，所以 B 通常才是实话。

两个常踩的点：

- `--wrap` / `--fail` 的收尾说明要作为 `note=` 参数跟终态调用**同一次**发出去。
  终态调用会删掉 state 文件，凭据没了，之后再补一个 `pf_emit_event` 会因为没凭据而失败。
- 终态**不会**帮你删 worktree 目录。自己 `git worktree remove`，或者跑 `polyforge doctor --fix`。

### task 分支的 upstream（`aihub#257`）

polyforge 以前用 `git worktree add -b <task> <path> origin/main` 建 task 分支，git 的
`branch.autoSetupMerge` 默认值会把 **`origin/main` 设成这个分支的 upstream**。它一直没炸，只是因为
`push.default` 没人设过、走 git 内置的 `simple`（名字不匹配就拒绝）。**谁把 `push.default` 设成
`upstream` 或 `tracking`（全局或仓级），那条裸 `git push` 就会把 task 分支的 commit 直接推到 `main`，
exit 0，没有任何警告。**

现在：

- 新建的 task 分支带 `--no-track`，**没有 upstream**；claim 一个已存在的 worktree 会顺手把它那条
  指向 `main` 的 upstream 清掉。
- `pf_push` / `pf_ship` 推完会把 upstream 设成 `origin/<task 分支>`。
- ⚠️ **因此裸 `git pull` 的含义变了**：第一次 push 之后它合的是 `origin/<task 分支>` 而不是
  `origin/main`；第一次 push 之前它会直接报 "no tracking information" 而不是悄悄把 main 合进来。
  **要合基线分支就显式写 `git merge origin/main`。**
- 想知道自己工作区里还剩多少个旧的危险 worktree：`polyforge doctor` 的 `branch-upstream` 一行。
  它**只报不修** —— 改别人正在用的 worktree 的 `git push` 含义，不该由一条命令替所有人决定。
  最省事的缓解是 `git config --global push.default current`（裸 push 一律推同名分支，不动任何分支配置）。

---

## 再往下

| 想知道 | 去哪 |
|---|---|
| 装机、配 key、装插件 | [`docs/onboarding.md`](onboarding.md) |
| 全部 MCP 工具 | [`docs/mcp-tools.md`](mcp-tools.md) |
| 命令速查 | `.polyforge/usage.md` —— 在你自己 workspace 根目录下，不在本仓，所以链不了 |
| 架构与规格 | [`docs/design/polyforge-v1-design.md`](design/polyforge-v1-design.md) |
| 出问题了 | `/pf-doctor`（一个 skill，不是文件） |
