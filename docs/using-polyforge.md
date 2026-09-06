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

### ⚠️ commit 现在会替你**取锁**（`aihub#366`）

`pf_commit` 和 `pf_ship` 的 commit 那一步，行为比字面上重：**提交之前它会把这次提交包含的文件
列出来，跟「本 attempt 当前实际持有的 `file_scope` 锁」比一遍**，没被覆盖的那些**自动补上锁**，
补到的锁一直持有到 attempt 结束。所以 **commit 会扩大你的持锁面**。

为什么要有这道闸：`declared_resources` 是**发现问题时**填的，描述「问题在哪」；锁要覆盖的是
「**修法会碰到哪**」。实测（2026-09-05/06 连着四条 wi）claim 时拿到 2 / 2 / 0 / 0 把锁，
实际改了 5 / 9 / 20 / 4 个文件 —— 其中一次是**改到一半才发现自己碰了**第四个文件。

- **别人没占** → 静默补锁通过，返回值里 `lock_gate: "acquired"` 加 `locks_acquired_for`。
- **别人真的占着** → **commit 被拒**（`CONFLICT_LOCK_TAKEN`），一个文件都不提交，改动仍在暂存区，
  错误里给出**每一个**被挡的路径和**持有者**（actor / wi / attempt）。**不要用 force takeover 硬闯**
  —— 对方占着锁正是因为它在改那个文件。两条出路：等对方结束，或者把那些文件从这次提交里摘出去。

  🔴 **摘出去是两步，第二步不是可选的：**

  ```bash
  # 1) 从暂存区撤出（工作区改动保留）
  git -C <worktree> restore --staged <被挡的路径…>
  # 2) 重试时把 paths 缩到剩下的文件
  #    pf_commit(..., paths=["<你自己的路径…>"])
  ```

  **两步都做才有用，任何一步单做都无效**，方向还相反：

  - 只做第 1 步、然后**原样重试** —— 不带 `paths` 的重试会跑 `git add -A`，把你刚撤出去的文件
    原封不动加回暂存区。
  - 只做第 2 步、不先撤暂存区 —— `paths` 只决定往暂存区里**加**什么，从不做 reset，而被拒之前
    那轮 `git add` 已经把暂存区填满了。

  两种都栽在同一件事上：闸读的是**暂存区 vs HEAD**，不是你这次传了什么。实测（走真实 MCP 工具
  打到 fake server，服务端在 `paths` 含 `contested.txt` 时才拒）：

  ```
  被拒后的暂存区                                    [contested.txt mine.txt]
  git restore --staged contested.txt 之后            [mine.txt]
  接着「原样」重试      两次发出的 paths  [contested.txt mine.txt] [contested.txt mine.txt]
                                          → 又一个 409，HEAD 没动
  接着「带 paths=」重试 两次发出的 paths  [contested.txt mine.txt] [mine.txt]
                                          → lock_gate: acquired，HEAD 前进
  ```

  一步到位的替代写法是 `git stash push -- <被挡的路径…>`：它把文件从**工作区**也拿走，所以之后
  **原样重试**就能过（同一套实测：暂存区变成 `[mine.txt]`，原样重试发出 `[mine.txt]`，`acquired`）。
  代价是那些改动进了 stash，事后得 `git stash pop` 拿回来。
- **全都已覆盖** → 不取任何锁、不写任何东西，返回 `lock_gate: "covered"`。
- 补上的锁**不会**写进 `declared_resources`。这是故意的：`aihub#264` 会在每次
  `declared_resources` 被整表替换时释放差集，而 `/pf-plan` 第 5 步正是整表替换 —— 写进去反而会让
  下一次 plan 把锁**释放掉**。审计记录走 `pf_read_events` 的 `lock_acquired`，`cause=commit_gate`。
- **没有放行通道**：闸连不上服务端时 commit 直接失败，而不是当作检查通过。删 state 文件也不行 ——
  worktree 本来就是靠同一份 state 文件解析的，删了整个 `pf_commit` 都跑不了。
- **`lock_gate` 一共五个值，但它们对应六件事**，别混着读：
  - `covered` / `acquired` —— 提交成了。
  - `not_run` —— 闸**跑到了**，但暂存区跟 HEAD 一样，没有变更需要保护。`pf_ship` 走到这里就是
    「没东西要提交」；`pf_commit` 只有一种路径会到：**merge 提交**（内容来自 `MERGE_HEAD` 而不是
    暂存区），那次响应里 `sha` 照样有值 —— 所以别把这个值读成「没提交」。
  - `refused` —— 查了，别人占着，一个文件都没提交。
  - `could_not_run` —— **没查成**，两种原因共用这一个值（`lock_gate_detail` 里区分）：
    ① 查了但没查完（连不上、5xx、state 文件读不出来）；
    ② **commit 那一步在闸之前就挂了** —— `git add` 撞上别的 git 进程留下的 `.git/index.lock`、
    `git diff --cached` 读不到 HEAD 之类。②**不知道暂存区里剩了什么**，所以那件事只能从
    `side_effects` 读，不要从 `lock_gate_detail` 读。

  `pf_ship` 失败时返回的是 JSON，这两个字段都在里面；`pf_commit` 失败时返回的是纯错误串，
  **没有** `lock_gate` 字段。

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
