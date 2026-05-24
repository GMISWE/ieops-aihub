# polyforge v1 — 完整设计文档

> 版本: v1.15  
> 日期: 2026-05-21  
> 状态: 三轮 Opus review 合入 + user 系统 + ID 规范  
> 性质: 实现契约（implementation contract）— 所有细节在编码前锁定

---

## Changelog

- v1.24: 综合 Opus R9 Part A：C-R9-1 migration seed 默认数据；C-R9-2 CAS 冲突禁止自动 merge，强制手工确认；C-R9-3 scenario config 不存在→503；C-R9-4 classification_rules.set.wi_type 写入时校验；C-R9-5 zombie sweeper 改为 system force_takeover + lost 状态（释放 locks）；C-R9-6 wi.wi_type=NULL 时禁止 claim；C-R9-7 Wi Agent = 角色，两种载体明确区分；C-R9-10 fn_claim 时验证 wi_type 仍存在；H-R9-8 pf-add-type 先 server 后本地；H-R9-9 agent_events.work_item_id 改 NULLABLE；H-R9-11 paused 时 fn_complete_attempt 自动 force_terminate；H-R9-12 mode=resume 同 user 隐式 takeover；M-R9-20 GC sweep 7 SQL 语法修正；Part B 精简：memory_embeddings 合并进 memories（12→10 表）；wi_sequences 删除；pf_acquire_lock/pf_release_lock/pf_update_artifact/pf_reconcile_artifacts/pf_manage_actors 删除（38→32 工具）；execute-scenario 推 v2；pf-debug/pf-event/pf-review stub 内联（skill 18→12）；pf-add-type 合并到 NL 路由
- v1.23: 新增 scenario_phase_configs 表（scenario 级 SoT，替代 per-attempt phase_yaml_snapshot）；run_attempts 删 phase_yaml_snapshot、加 phase_config_version 审计字段；claim 不再上传 phase.yaml，server 直接查 scenario_phase_configs；新增 GET/PUT /v1/scenarios/{scenario}/phase_config HTTP API + pf_get_scenario_config/pf_update_scenario_config MCP 工具；wi_classification_rules 移入 scenario_phase_configs；/pf-add-type skill 新增；§27 KL4 跨 workspace 漂移已解决，移出 KL
- v1.22: 删除 work_items.kind 字段——wi_type 单字段决定执行路径和 requires_human_session；phase.yaml wi_types 完全用户可定制（无固定枚举）；wi_classification_rules 匹配条件改为 priority + wi_type_prefix；pf_list_work_items kind 过滤改为 wi_type；WorkItem schema 移除 kind
- v1.21: ownership 模型最终简化（删 expires_at，force_takeover 纯靠角色权限不看时间）；server-side wi_classification_rules 配置（aihub.yaml，create 时覆盖 client 判断，彻底解决 TOCTOU）；needs_human_session 通知改为 LCRS 内嵌告警（push 推 v2）
- v1.20: 综合 Opus R8（4C/12H/9M/11L）+ 12角色模拟：requires_human_session DDL DEFAULT 改 NULL（fail-safe）+ 新增 unclassified[] LCRS 段；§22 WIType struct 加 RequiresHumanSession 必填字段；fn_claim 写回不一致时返回 409 REQUIRES_HUMAN_SESSION_MISMATCH；pf_update_work_item 加 wi_type/requires_human_session（reporter/maintainer/admin, queued/paused 状态）；ClaimResponse + pf_get_step 响应加 requires_human_session；§17 加 WI_TYPE_MISMATCH/REQUIRES_HUMAN_SESSION_MISMATCH 错误码；§21.7 pf-execute 加 requires_human_session=true 分支（inline /pf-spec，不 dispatch subagent）；§21 NL 路由加"今天有哪些活"+"需要我来拍板"入口；§21.4 /pf-status 全局视图加 needs_human_session[] 渲染；§10.0 步骤编号修正（两个 "3." → "3./4."）；§10.3 LCRS 统一命令为 /pf-work（移除 pf claim 混用）；§15 GC 加 needs_human_session aging sweep；§27 加 AI wi_type 判断软约束 KL3；§30.4 加 migration backfill 警告；pf-spec start_step 心跳保护注释
- v1.19: 补 work_items.wi_type + requires_human_session 字段——AI 在创建 wi 时判断"是否需要人工主导"并写入；phase.yaml wi_type 定义加 requires_human_session 标志；Ready Queue 按此分拣（items[]=auto, needs_human_session[]=需人工）；§8.4 解析规则 wi.wi_type 优先级最高；/pf-start skill 加 AI 判断步骤；LCRS 恢复 "Needs Your Attention" 段；pf_create_work_item 加 wi_type? 参数；§18.1 WorkItem schema 补两个字段
- v1.18: 移除 work_items.phase 字段和 design/executable 两段切割——phase.yaml 统一驱动所有 wi 类型（含 feature）的完整步骤（spec→plan→implement→review→push）；feature wi 创建后直接进 Ready Queue；删除 design_phase[] LCRS 段、PHASE_PROMOTION_REQUIRES_SPEC_AND_PLAN 错误码、pf_create/update_work_item 的 phase 参数、L-R3-7 handler 逻辑；§8.2 补 feature wi_type 完整步骤定义
- v1.17: 综合 Opus R7（9C/15H/20M/21L）+ 12角色模拟：C-R7-9 wi_step_state re-claim 改 ON CONFLICT DO UPDATE；C-R7-6 idempotency cache key 加 api_key_id；C-R7-7 resource_locks CASCADE 死代码 → ON DELETE RESTRICT；C-R7-1 phase_yaml_snapshot wire format 澄清（client yaml→JSON object）；C-R7-2 unblock FOR UPDATE 加 ORDER BY id；C-R7-4 api_keys UNIQUE 靠 app 层；Alice-2 ClaimResponse 加 step_recovery_hint 字段；prior attempt 在 re-claim 时 status→superseded；prepare_workspace vs dirty worktree 矛盾修正；pf_wrap on_wrap hook 幂等保护；L-R3-7 phase handler 精确语义（仅 kind 字段变更时推 design，显式 phase 覆盖时尊重）；§12 补 pf complete-attempt CLI；§4.3 补 POST /v1/work_items/{id}/unblock（admin）；§5.7 + §4.3 对齐 paused[]/design_phase[] 五段；Orchestrator 空 round 行为明确；pf-orchestrate 作为 prompt-pattern 说明；pf_save_artifact 加 supersedes_memory_id 语义；pf-plan 末尾加 phase promote 步骤；§9.5.5 补 PROJECT_AMBIGUOUS 错误码；§17 补缺失错误码（AIHUB_UNAVAILABLE/CONFLICT_DUAL_WI_AGENT/STALE_LOCAL_CREDENTIAL/GOAL_CHANGE_NOT_ALLOWED/PROJECT_AMBIGUOUS）；force_takeover wi_step_completions.run_attempt_id=旧 attempt；pf_predict_conflicts 跨 project 折叠约束；pf_list_dependencies MCP schema 加 hidden 模式
- v1.16: 新增 §30 运维 & 升级（aihub重启/CC重启/MCP binary升级/aihub DB migration/版本兼容性）；§10.0 Orchestrator 明确 kind+phase 决定调度路径（design phase wi 不进 Ready Queue）；§10.3 LCRS 新增 design_phase[] 段展示待讨论 feature wi
- v1.15: 综合第六轮 Opus review + 11个角色模拟（12 subagents）：C6-1并发 unblock race(SELECT FOR UPDATE)；C6-2 session_secret+idempotency(先写 state file)；C6-3 force_terminate INSERT 必填字段；C6-4 CAS+step_attempt_id 合并 WHERE；state file 改 wi_id.json；pf_force_takeover 返回 new attempt credential；retro 改 dispatch subagent；step heartbeat 新语义(刷新 step_started_at)；crash-recovery §9.5.6；CI §9.5.7；pf_cancel_work_item MCP工具；paused[] 加进 LCRS；outcome server ground truth 校验；wall clock 机制；§3.4 加读 memory(team) viewer行；GET dependencies 跨project遮蔽；Orchestrator subagent error_details
- v1.14: 综合第五轮 Opus review + 角色模拟：决策A(session_secret由MCP server生成,不进prompt)；C5-3(pause保留state file)；C5-1(start_step必须先in_progress)；C5-4(force_terminate_step重置step_state)；C5-5(pf_save_artifact visibility=project默认)；M5-14(UNIQUE partial index)；新增pf_get_ready_queue MCP工具；CONFLICT_WI_ALREADY_CLAIMED错误码；unblock sweep改为fn_complete_attempt内同步触发；§21.2 mode=resume after takeover；§10.0补subagent contract+Round软上限；project参数MCP自动注入；SSH preflight文档；using-polyforge startup scan；pf_save_artifact visibility default
- v1.13: 综合第四轮 Opus review + 角色模拟决策：stop_step 删除（retro 内置 Wi Agent wrap）；state file O_TRUNC 覆盖写；workspace_root=os.Getwd()（每 Claude Code 窗口独立）；fan-out 严格 round barrier+防双派；§10.2 stalled=blocked+wi_stalled；§9.4 force_takeover 权限统一；PATCH status 删除+新增 /cancel；pf_update_work_item 加 goal 参数；§21.10 删 attempt 三元组；§21 type 全名；mode=resume after takeover；Alice WALL-09/19a/H4-11/Carol WALL-BD/C4-1/C4-2/C4-4
- v1.12: agent_events 加 project 冗余列 + idx_evt_project_time 索引；GET /v1/events 支持 project/user_id 过滤；pf_read_events 参数对齐；新增 §27 Known Limitations（Orchestrator audit/scope 软限制）
- v1.11: project_roles enum server 校验（PATCH /v1/admin/users 拒绝非法值）；跨 project pf_create_dependency 权限（caller 对 blocking_wi.project 须有 viewer+）
- v1.10: goal 从 immutable 改为权限控制+audit：删除 trg_wi_goal_immutable trigger，PATCH 允许 goal 字段（reporter/maintainer，status=queued/paused），新增 GOAL_CHANGE_NOT_ALLOWED 错误码，wi_goal_updated event payload；supersedes 问题通过 goal 更新解决，不需要 supersedes 触发状态变更
- v1.9: Layer 3 轮次模型（§10.0）；pf_create_work_item 加 blocked_by 参数，原子建依赖；pf-plan skill 改为拓扑顺序创建 child wi，消除 pf_create_dependency 权限问题；删除 Bug 2（phase 转换）误判说明
- v1.8: force_takeover 原子重置 wi_step_state（§4.3/§9.5）；takeover 后接手规则：task_branch 有 commit 则 review 继续，无 commit 则重做当前 step（§9.5）；删除 step_agent_tokens 相关引用
- v1.7: §24 重写：删除 step_agent_tokens 表，改为 MCP server 从本地 state file 注入凭证；coding tools 签名删除 attempt 三元组参数；state file 扩展 step 缓存字段（§9.5.4）；明确本地=read cache/aihub=CAS SoT 的分工；subagent prompt 规范（无 secret）
- v1.6: 新增 §9.5 Client 配置与状态持久化：.polyforge.yaml schema（含 default_project/dedup 阈值）、~/.polyforge/config.toml schema（machine_id/api_key）、per-wi state 文件 .polyforge/state/<wi_slug>.json（AttemptCredential 存储协议）、project 解析优先级规则；修正 api_key 从 .polyforge.yaml 移出
- v1.5: user display_name 全面补全：work_items.reporter_display 快照字段；agent_events.actor_display DDL；Memory 对象 author_display/last_activated_by_display；pf_list_work_items owner 对象含 display；pf_force_takeover 返回 prior_actor_display；pf_get_step 返回 current_actor_display；Ready Queue running/stalled 含 owner_display；三段式 owner 字段格式规范（actor_display + attempt_id）
- v1.4: user 系统（user_type/project_roles/api_keys 扩展，权限矩阵 §3.4，创建用户 API）；ID 格式统一为 8 位 base62（§2.2.1）；force_takeover 权限修正（idle>30min 任意 writer 可接管）
- v1.3: 合入第三轮 Opus adversarial review 43 条 finding（8C/14H/13M/8L）：C-R3-1 agent_events 分区 PK 修正；C-R3-2/3 fact.*/methodology.* trigger 量纲/注释；C-R3-5 step_agent_token 独立表；C-R3-6 paused→running 语义；C-R3-7 expires_at 随 mutating 刷新；C-R3-8 resource_type 映射表；H-R3-1 release tools 补充；H-R3-6 resource_locks.claim_epoch BIGINT；H-R3-7 wrapped/failed 释放 locks；H-R3-10/11 遗忘曲线量纲+示例数字重算；H-R3-12 stalled=blocked；H-R3-13 VECTOR 变长；H-R3-14 session_secret 设计说明；M-R3-1 wi_step_state INSERT timing；M-R3-3 previous_steps 改数组；M-R3-4 will_unlock 语义；plus 25+ Medium/Low 修正
- v1.2: 合入第二轮 Opus adversarial review 28 条 finding（12 Critical/14 High/2 Medium grouping）；PG 版本声明 18+；wi_step_state 增加 in_progress 跟踪字段（C2）；goal immutable trigger（C5）；claim_epoch 初始值规则（H9）；supersedes 循环检测（C8）；paused 释放锁（C10）；pf_wrap 语义澄清（C11）；补充 pf_create_dependency/pf_reinforce_memory/pf_diff 工具（C6/C12/M1）；pf_save_artifact 加 AttemptCredential（M5）；memory type 全名规范（H1）；遗忘曲线 NULL 公式（M8）；dedup 移除时间硬截断（M9）；min_strength 默认 0.3（M7）；methodology.* 改用 expires_at（H4）；zombie sweeper 24h（H5）；pf-execute retry 防死锁（M16）；step_started server-emit（M17）；admin event whitelist（H10）；phase.yaml schema_version 改名（M18）；新增 §23 冲突预测规则 / §24 Step Agent Token Scope；章节重编号
- v1.1: 合入 Opus adversarial review P0/P1 修正；完整 memory 系统设计；补充状态机矩阵；新增 idempotency key；修正 worktree 路径碰撞
- v1.0: 初稿

---

## 0. 核心设计哲学

> **版本说明**：本文档的 "polyforge v1" 是新一代 Go 重写代际（plugin v1.0），与已归档的 polyforge-v3（Python 版）是不同重写代际，无版本继承关系。

- **aihub PostgreSQL 是唯一状态权威（SoT）**，本地几乎零持久状态
- **ownership-only**：wi claim 后永久持有，无 `expires_at`，无 lease renewal。释放路径：(a) 自己 complete（pause/wrap/fail）；(b) 同 user_id 自我接管；(c) maintainer/admin force_takeover。`updated_at > 24h` 未更新的 running wi 出现在 `stale_running` 提醒段，不自动释放
- **单二进制分发**：`polyforge` Go binary，无 PyPI、无 uvx、无 editable install
- **LLM 是执行引擎**：Skills（Markdown）定义 how，MCP tools 提供副作用，aihub 持久化状态
- **三层分离**：Layer 1（并发控制）/ Layer 2（单 wi 执行）/ Layer 3（跨 wi 调度）
- **Memory-First**：任何执行或方案讨论前，先检索历史经验作参考

---

## 1. 仓库结构

### 1.1 两个仓库，两个 binary

```
aihub/                             # 仓库 1（ieops-aihub，原 Python 仓库复用）
  v0/                              # Python 代码归档（只读，不再修改）
    app/
    routes/
    alembic/
    tests/
    main.py
    pyproject.toml
    Dockerfile
    ...

  # ── Go 代码在仓库根目录重建 ──
  cmd/
    aihub/
      main.go                      # HTTP API server binary
    polyforge/
      main.go                      # MCP server binary（stdio，用户机器运行）
  internal/
    domain/
      work_items.go
      run_attempts.go
      locks.go
      events.go
      memory.go                    # memory CRUD + dedup + activation
      conflicts.go
      gc.go
      dedup.go                     # F3 wi dedup 算法
      step_state.go                # Layer 2 step 状态机
    db/
      db.go                        # pgx v5 connection pool
      migrations/
        0001_initial.sql
        0002_ownership_only.sql
        0003_slug_seq.sql
        0004_wi_dependencies.sql
        0005_step_state.sql
        0006_memory_v2.sql
    auth/
      bearer.go
      attempt.go
    embedding/
      provider.go                  # EmbeddingProvider interface
      openai.go
      ollama.go
    server/
      router.go                    # echo v4 路由
      middleware.go
      idempotency.go               # Idempotency-Key 中间件
    mcp/
      server.go                    # mark3labs/mcp-go MCP server
      tools_lifecycle.go
      tools_locks.go
      tools_events.go
      tools_memory.go
      tools_conflicts.go
      tools_step.go
      tools_release.go
      tools_actors.go
    coding/
      scenario.go
      git_ops.go
      gh_ops.go
      tools_coding.go
    scenario/
      protocol.go
      registry.go
    cli/
      init.go
      doctor.go
      ready.go
      stalled.go
    config/
      config.go
    version/
      version.go                   # var Version = "dev"
  pkg/
    client/
      client.go                    # aihub HTTP client
  go.mod
  go.sum
  Dockerfile                       # Go 版本
  Makefile

marketplace/
  plugins/
    polyforge/                     # Go 版本，正式名称
      .claude-plugin/plugin.json
      skills/
        using-polyforge/SKILL.md
        pf-work/SKILL.md
        pf-stop/SKILL.md
        pf-status/SKILL.md
        pf-spec/SKILL.md
        pf-plan/SKILL.md
        pf-review/SKILL.md
        pf-execute/SKILL.md
        pf-retro/SKILL.md
        pf-debug/SKILL.md
        pf-event/SKILL.md
        pf-sync/SKILL.md
        pf-release/SKILL.md
        execute-scenario/SKILL.md
        coding/
          code_change/SKILL.md
          commit_and_pr/SKILL.md
          prepare_context/SKILL.md
  archive/
    polyforge-legacy/
    polyforge-v2/
    polyforge-v3/                  # Python 版归档
```

### 1.2 目录处理步骤

#### aihub 仓库（ieops-aihub）

```bash
# Step 1：归档 Python 代码
mkdir v0
git mv app routes alembic tests scripts \
        main.py db.py embedder.py search.py auth.py backup.py models.py \
        pyproject.toml Dockerfile Makefile README.md alembic.ini \
        v0/
git commit -m "chore: archive Python v0 source to v0/"

# Step 2：Go 模块初始化
go mod init github.com/GMISWE/ieops-aihub
mkdir -p cmd/aihub cmd/polyforge
mkdir -p internal/{domain,db/migrations,auth,embedding,server,mcp,coding,scenario,cli,config,version}
mkdir -p pkg/client

# Step 3：更新根目录文件
# - 新 Dockerfile（Go multi-stage build）
# - 新 Makefile（go build / migrate / test）
# - 更新 README.md
# - 新 .github/workflows/（Go CI）
```

#### marketplace 仓库

```bash
# Step 1：归档旧 plugin
mkdir -p archive
git mv plugins/polyforge-legacy archive/  2>/dev/null || true
git mv plugins/polyforge-v2     archive/  2>/dev/null || true
git mv plugins/polyforge-v3     archive/

# Step 2：创建新 plugin 目录
mkdir -p plugins/polyforge/.claude-plugin
mkdir -p plugins/polyforge/skills/{coding,}
# 写入 plugin.json + 所有 SKILL.md
```

### 1.3 最终目录结构

#### aihub/（ieops-aihub 仓库）

```
aihub/
├── v0/                                  # Python 归档（只读）
│   ├── app/
│   │   ├── auth.py
│   │   ├── conflicts.py
│   │   ├── db.py
│   │   ├── errors.py
│   │   ├── events.py
│   │   ├── gc.py
│   │   ├── locks.py
│   │   ├── memory.py (partial)
│   │   ├── run_attempts.py
│   │   ├── schemas.py
│   │   └── work_items.py
│   ├── routes/
│   ├── alembic/
│   ├── tests/
│   ├── main.py
│   └── pyproject.toml
│
├── cmd/
│   ├── aihub/
│   │   └── main.go                      # HTTP API server 入口
│   └── polyforge/
│       └── main.go                      # MCP server 入口（stdio）
│
├── internal/
│   ├── domain/
│   │   ├── work_items.go               # CRUD + slug 生成
│   │   ├── run_attempts.go             # claim / complete / takeover（RPC 函数）
│   │   ├── locks.go                    # acquire / release
│   │   ├── events.go                   # emit（64KB cap）
│   │   ├── memory.go                   # remember / recall / activate / redact
│   │   ├── conflicts.go                # 5 规则冲突预测
│   │   ├── gc.go                       # GC sweeps（pg_try_advisory_lock）
│   │   ├── dedup.go                    # F3 n-gram Jaccard wi dedup
│   │   └── step_state.go              # Layer 2 step 状态机（CAS）
│   │
│   ├── db/
│   │   ├── db.go                       # pgx v5 pool init
│   │   └── migrations/
│   │       ├── 0001_initial.sql
│   │       ├── 0002_ownership_only.sql
│   │       ├── 0003_slug_seq.sql
│   │       ├── 0004_wi_dependencies.sql
│   │       ├── 0005_step_state.sql
│   │       └── 0006_memory_v2.sql
│   │
│   ├── auth/
│   │   ├── bearer.go                   # sha256 API key 校验
│   │   └── attempt.go                  # AttemptCredential 三元组
│   │
│   ├── embedding/
│   │   ├── provider.go                 # EmbeddingProvider interface
│   │   ├── openai.go
│   │   └── ollama.go
│   │
│   ├── server/
│   │   ├── router.go                   # echo v4 路由注册
│   │   ├── middleware.go               # auth / request-id / recovery
│   │   └── idempotency.go             # Idempotency-Key 缓存中间件
│   │
│   ├── mcp/
│   │   ├── server.go                   # mark3labs/mcp-go 初始化
│   │   ├── tools_lifecycle.go          # pf_whoami/create/list/update/claim/complete/force_takeover
│   │   ├── tools_locks.go              # pf_acquire_lock / pf_release_lock
│   │   ├── tools_events.go             # pf_emit_event / pf_read_events
│   │   ├── tools_memory.go             # pf_remember/recall/activate/redact/save_artifact
│   │   ├── tools_conflicts.go          # pf_predict_conflicts / pf_update_artifact
│   │   ├── tools_step.go               # pf_get_step / pf_update_step
│   │   ├── tools_release.go            # pf_cut_alpha / pf_promote
│   │   └── tools_actors.go             # pf_manage_actors
│   │
│   ├── coding/
│   │   ├── scenario.go                 # CodingScenario implements Scenario interface
│   │   ├── git_ops.go                  # git subprocess（exec.Command，永不用 cwd，用 -C）
│   │   ├── gh_ops.go                   # gh CLI wrappers
│   │   └── tools_coding.go             # pf_start/commit/push/pr/wrap/reconcile_artifacts
│   │
│   ├── scenario/
│   │   ├── protocol.go                 # Scenario interface 定义
│   │   └── registry.go                 # init() 注册 + All() 返回列表
│   │
│   ├── cli/
│   │   ├── init.go                     # pf init / pf init --apply
│   │   ├── doctor.go                   # pf doctor（5 项检查）
│   │   ├── ready.go                    # pf ready [--view=lcrs]
│   │   └── stalled.go                  # pf stalled
│   │
│   ├── config/
│   │   └── config.go                   # .polyforge.yaml 解析
│   │
│   └── version/
│       └── version.go                  # var Version/GitCommit/BuildTime = "dev"
│
├── pkg/
│   └── client/
│       └── client.go                   # aihub HTTP client（MCP server 调用 aihub）
│
├── go.mod
├── go.sum
├── Dockerfile                           # Go multi-stage build
├── Makefile                             # build / migrate / test / lint
└── .github/
    └── workflows/
        ├── ci.yml                       # go test + golangci-lint
        └── deploy.yml                   # build + push image + deploy
```

#### marketplace/（GMI-marketplace 仓库，相关部分）

```
marketplace/
├── plugins/
│   └── polyforge/                       # Go 版本，正式名称
│       ├── .claude-plugin/
│       │   └── plugin.json
│       └── skills/
│           ├── using-polyforge/
│           │   └── SKILL.md             # meta：Iron Rules + NL 路由 + 三段式规范 + Memory-First
│           ├── pf-work/
│           │   └── SKILL.md             # new/claim/resume/force_takeover
│           ├── pf-stop/
│           │   └── SKILL.md             # pause/wrap/fail
│           ├── pf-status/
│           │   └── SKILL.md             # list/timeline/ready/stalled
│           ├── pf-spec/SKILL.md
│           ├── pf-plan/SKILL.md
│           ├── pf-review/SKILL.md
│           ├── pf-execute/SKILL.md      # Wi Agent 调度 Step Agent
│           ├── pf-retro/SKILL.md
│           ├── pf-debug/SKILL.md
│           ├── pf-event/SKILL.md        # note/decision/memory share
│           ├── pf-sync/SKILL.md         # Jira/GitHub
│           ├── pf-release/SKILL.md      # cut alpha + promote
│           ├── execute-scenario/
│           │   └── SKILL.md
│           └── coding/
│               ├── prepare_context/
│               │   └── SKILL.md         # start_step：recall 历史，分析代码
│               ├── code_change/
│               │   └── SKILL.md
│               └── commit_and_pr/
│                   └── SKILL.md
│
└── archive/
    ├── polyforge-legacy/               # 原 polyforge v1（最早版）
    ├── polyforge-v2/
    └── polyforge-v3/                   # Python 版（本次重写前）
```

### 1.4 plugin.json

```json
{
  "name": "polyforge",
  "version": "1.0.0",
  "mcpServers": {
    "polyforge": {
      "command": "polyforge"
    }
  }
}
```

### 1.3 版本注入

```bash
go build \
  -ldflags "-X polyforge/internal/version.Version=${VERSION}
            -X polyforge/internal/version.GitCommit=$(git rev-parse --short HEAD)
            -X polyforge/internal/version.BuildTime=$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
  -o polyforge ./cmd/polyforge/
```

---

## 2. 数据库（10 张表）

### 2.1 表总览

```
users                  — 用户身份 + API keys
scenario_phase_configs — scenario 级 phase.yaml SoT（wi_type 定义 + classification_rules）
work_items             — 任务（slug、wi_type、milestone）
wi_sequences           — per-project 自增序号（生成 slug）
wi_dependencies        — wi 间有向边（blocks/supersedes/related）
run_attempts           — 执行尝试（ownership-only，last_active_at）
wi_step_state          — Layer 2 step 执行状态（独立表，高频写隔离）
resource_locks         — 资源锁
agent_events           — 不可变审计事件流
memories               — 知识库（含遗忘曲线、激活机制）
memory_embeddings      — 向量（独立表，model 隔离）
wi_step_completions    — step 完成历史（append-only，独立于 wi_step_state）
-- step_agent_tokens 表已删除（§24 重新设计：凭证注入走本地 state file）
```

### 2.2 全局约定

- **PostgreSQL 版本要求：>= 18**（当前最新稳定版 18.x）
  - `NULLS NOT DISTINCT`（PG 15+）✓
  - 声明式月分区（PG 10+）✓
- 所有时间戳使用 `TIMESTAMPTZ`，服务端用 `clock_timestamp()`
- BIGINT seq 不保证连续（PG SEQUENCE，rollback 产生 gap，可接受）

### 2.2.1 ID 格式

所有主键格式：`<prefix>_<8位 base62>`，总长 10-12 字符。

```
prefix  entity             示例
wi_     work_items         wi_A3f7B9c2
ra_     run_attempts       ra_8d2E4F1a
evt_    agent_events       evt_Q7b2R5p8
mem_    memories           mem_Z3x9Y2v1
sat_    step_agent_tokens  sat_P4m8N2k6
sc_     step_completions   sc_W1r9T5q3
u_      users              u_H6n3M7y4
key_    api_keys（内嵌JSONB中） key_X2p5D8w1
```

**base62 字符集**：`[0-9A-Za-z]`（62 个字符）
**碰撞概率**：62^8 ≈ 218 万亿，百万条记录时 ≈ 2.3×10^-9（可忽略）
**时间排序**：不依赖 ID，靠 `created_at TIMESTAMPTZ` 字段
**Go 生成**：

```go
const charset = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"

func NewID(prefix string) string {
    b := make([]byte, 8)
    for i := range b {
        n, _ := rand.Int(rand.Reader, big.NewInt(62))
        b[i] = charset[n.Int64()]
    }
    return prefix + "_" + string(b)
}
```

**idempotency_key**：同样用 8 位 base62，client 端生成，格式 `idem_<8b62>`

### 2.3 完整 DDL

#### users

```sql
CREATE TABLE users (
    id              TEXT PRIMARY KEY,             -- u_<8b62>
    email           TEXT NOT NULL UNIQUE,
    display_name    TEXT NOT NULL,
    user_type       TEXT NOT NULL DEFAULT 'human'
                    CHECK (user_type IN ('human', 'machine')),
                    -- machine: CI pipeline / agent fleet，email 自动合成
    role            TEXT NOT NULL DEFAULT 'writer'
                    CHECK (role IN ('writer', 'admin')),
                    -- 全局角色：admin 绕过所有 project 权限检查

    -- 项目级角色：替代原 projects TEXT[]
    -- 格式: {"marketplace": "writer", "aihub": "maintainer", "ieops": "viewer"}
    -- 合法值 enum：viewer | writer | maintainer（server PATCH 时校验，不在 DDL 层）
    project_roles   JSONB NOT NULL DEFAULT '{}',

    -- API keys，JSONB 数组（小团队无需独立表）
    -- [{id, key_hash, name, project_scope?, expires_at?, created_at, revoked_at}]
    api_keys        JSONB NOT NULL DEFAULT '[]',

    -- git commit author 匹配，用于 pf_sync 时关联 PR author → user_id
    author_aliases  TEXT[] NOT NULL DEFAULT '{}',

    created_at      TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp()
);
CREATE INDEX idx_users_email ON users(email);
CREATE INDEX idx_users_project_roles ON users USING GIN(project_roles);
-- 查某 project 所有成员：SELECT * FROM users WHERE project_roles ? 'marketplace'
```

#### scenario_phase_configs

```sql
-- scenario 级 phase.yaml SoT（v1.23）
-- 所有使用同一 scenario 的 project 共享同一份 wi_type 定义
-- 读：viewer+；写：maintainer/admin + CAS version
CREATE TABLE scenario_phase_configs (
    scenario    TEXT PRIMARY KEY
                CHECK (scenario IN ('coding','writing','data')),
    -- content: 解析后的 phase.yaml JSON（wi_types + classification_rules）
    -- 结构：{wi_types:{fix_bug:{...},...}, classification_rules:[...]}
    content     JSONB NOT NULL,
    version     INT  NOT NULL DEFAULT 1,  -- 乐观锁，每次 PUT +1
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
    updated_by  TEXT REFERENCES users(id)
);
-- C-R9-1: 初始数据由 migration seed 写入（0007_seed_scenario_configs.sql）
-- server 始终有记录，pf init 永远走 GET，不再有"首次写入"分支
-- 本地 .polyforge/phase.yaml 是工作副本，pf_update_scenario_config 同步到此表
-- C-R9-3: pf_create_work_item 时若 scenario 无记录 → 503 SERVICE_UNAVAILABLE（不是 500）
```

-- B2: wi_sequences 表已删除，seq_name 可由 project 名推导（wi_seq_<project>）
-- pf_create_work_item handler: nextval('wi_seq_' || project)
--   project 首次创建时 CREATE SEQUENCE IF NOT EXISTS wi_seq_<project> AS BIGINT START 1

#### work_items

```sql
CREATE TABLE work_items (
    id                  TEXT PRIMARY KEY,
    seq                 BIGINT NOT NULL,
    slug                TEXT GENERATED ALWAYS AS (project || '#' || seq) STORED,
    project             TEXT NOT NULL,
    -- M2: v1 只实现 coding scenario，'writing'/'data' 预留但未实现
    -- server 对未实现 scenario 返回 405 NOT_IMPLEMENTED
    scenario            TEXT NOT NULL DEFAULT 'coding'
                        CHECK (scenario IN ('coding', 'writing', 'data')),
    goal                TEXT NOT NULL
                        CHECK (length(goal) <= 500 AND goal !~ E'[\n\r]'),
                        -- M-R3-8: DDL 强制 goal 长度和单行约束
    -- goal 允许更新，但有限制（见 §4.3 PATCH 端点）：
    --   1. 只有 reporter 本人或项目 maintainer 可更新
    --   2. status 必须是 queued 或 paused（running 时拒绝 409）
    --   3. 必须传 goal_change_reason（min 10 chars）
    --   4. server emit wi_goal_updated event（保留 old_goal）

    source              TEXT NOT NULL DEFAULT 'human'
                        CHECK (source IN ('human','auto_execute','auto_debug',
                                          'auto_review','sync_jira','sync_github','admin')),
    -- v1.22: kind 字段删除，wi_type 单字段替代
    -- wi_type 直接映射到 phase.yaml 中的步骤图，无固定枚举
    -- 例：fix_bug / critical_bug / feature / chore（团队在 phase.yaml 中自定义）
    -- 由 wi_classification_rules（server 配置）或 AI 在创建时设置
    wi_type             TEXT,
    priority            TEXT NOT NULL DEFAULT 'normal'
                        CHECK (priority IN ('low','normal','high','urgent')),
    -- requires_human_session：AI 判断该 wi 是否需要人工在 session 中主导
    --   false = Orchestrator 可直接 dispatch subagent 自动执行（Session 1）
    --   true  = 需要人工开独立 session 参与（spec 讨论、方案拍板）（Session 2/3）
    --   NULL  = 尚未分类（create 时 phase.yaml 未提供或 AI 未传）
    -- C-R8-1/C-R8-2: DEFAULT NULL（fail-safe），而非 false！
    --   NULL 的 wi 进 unclassified[] 段，不进 items[]（防止未分类 wi 被自动执行）
    --   fn_claim_work_item 解析 phase_yaml_snapshot 后：
    --     (a) 若 wi.requires_human_session IS NULL → 写入 phase.yaml 的值
    --     (b) 若 wi.requires_human_session != phase.yaml 值 → 409 REQUIRES_HUMAN_SESSION_MISMATCH
    --     (c) 若一致 → 无操作（幂等）
    requires_human_session BOOL DEFAULT NULL,
    milestone           TEXT,
    labels              TEXT[] NOT NULL DEFAULT '{}'
                        CHECK (cardinality(labels) <= 20),
    status              TEXT NOT NULL DEFAULT 'queued'
                        CHECK (status IN ('queued','running','paused','blocked',
                                          'wrapped','failed','cancelled')),
    declared_resources  JSONB NOT NULL DEFAULT '[]',
    resources_version   INT NOT NULL DEFAULT 0,
    external_share_type TEXT CHECK (external_share_type IN ('jira','github','linear')
                                    OR external_share_type IS NULL),
    external_share_key  TEXT,
    reporter_user_id    TEXT NOT NULL REFERENCES users(id),
    reporter_display    TEXT NOT NULL,        -- 快照，同 actor_display 格式，创建时写入
    current_attempt_id  TEXT,                 -- denormalized，仅加速读取
    current_attempt_epoch BIGINT DEFAULT 0,   -- 单调递增，防重放攻击
    parent_work_item_id TEXT REFERENCES work_items(id),
    attrs               JSONB NOT NULL DEFAULT '{}',
                        -- 严格命名空间：{"github":{...},"jira":{...},"internal":{...}}
    created_at          TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
    closed_at           TIMESTAMPTZ,
    UNIQUE (project, seq),
    -- M5-14: 不用 NULLS NOT DISTINCT（会导致两个 (NULL,NULL) 的 wi 互相冲突）
    -- 改用 partial unique index（WHERE external_share_type IS NOT NULL）
);

CREATE UNIQUE INDEX idx_wi_slug ON work_items(slug);
-- M5-14 修正：partial unique index，NULL 不参与
CREATE UNIQUE INDEX idx_wi_external_ref ON work_items(external_share_type, external_share_key)
    WHERE external_share_type IS NOT NULL AND external_share_key IS NOT NULL;
CREATE INDEX idx_wi_project_status ON work_items(project, status);
CREATE INDEX idx_wi_milestone ON work_items(project, milestone) WHERE milestone IS NOT NULL;
CREATE INDEX idx_wi_wi_type ON work_items(project, wi_type) WHERE wi_type IS NOT NULL;
CREATE INDEX idx_wi_labels ON work_items USING GIN(labels);
CREATE INDEX idx_wi_declared ON work_items USING GIN(declared_resources);
CREATE INDEX idx_wi_parent ON work_items(parent_work_item_id) WHERE parent_work_item_id IS NOT NULL;
CREATE INDEX idx_wi_closed ON work_items(closed_at) WHERE closed_at IS NOT NULL;

-- 触发器：updated_at 自动更新
CREATE OR REPLACE FUNCTION fn_wi_updated_at() RETURNS trigger AS $$
BEGIN NEW.updated_at = clock_timestamp(); RETURN NEW; END;
$$ LANGUAGE plpgsql;
CREATE TRIGGER trg_wi_updated_at BEFORE UPDATE ON work_items
FOR EACH ROW EXECUTE FUNCTION fn_wi_updated_at();

-- goal 更新改为 API 层权限控制 + audit，不在 DDL 层阻断
-- server handler 在 PATCH goal 时检查：
--   a. caller 是 reporter 或 maintainer
--   b. wi.status IN ('queued', 'paused')，running 时返回 409 GOAL_CHANGE_NOT_ALLOWED
--   c. goal_change_reason 必填
--   d. emit wi_goal_updated event {old_goal, new_goal, reason, changed_by}

-- 触发器：closed_at 自动写入（terminal 状态转换时）
CREATE OR REPLACE FUNCTION fn_wi_closed_at() RETURNS trigger AS $$
BEGIN
    IF NEW.status IN ('wrapped','failed','cancelled')
       AND OLD.status NOT IN ('wrapped','failed','cancelled') THEN
        NEW.closed_at = clock_timestamp();
    END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;
CREATE TRIGGER trg_wi_closed_at BEFORE UPDATE OF status ON work_items
FOR EACH ROW EXECUTE FUNCTION fn_wi_closed_at();

-- 触发器：current_attempt_id + epoch 原子性保护（任何更新必须一致）
-- 在应用层 RPC 函数（fn_claim_work_item、fn_complete_attempt、fn_force_takeover）
-- 内部以事务更新，不允许 HTTP handler 直接裸写
```

#### wi_dependencies

```sql
CREATE TABLE wi_dependencies (
    blocked_wi_id   TEXT NOT NULL REFERENCES work_items(id) ON DELETE CASCADE,
    blocking_wi_id  TEXT NOT NULL REFERENCES work_items(id) ON DELETE CASCADE,
    kind            TEXT NOT NULL DEFAULT 'blocks'
                    CHECK (kind IN ('blocks','supersedes','related')),
    created_at      TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
    created_by      TEXT REFERENCES users(id),
    note            TEXT,
    PRIMARY KEY (blocked_wi_id, blocking_wi_id, kind),
    CHECK (blocked_wi_id != blocking_wi_id)
);
CREATE INDEX idx_wi_dep_blocking ON wi_dependencies(blocking_wi_id);
-- 写入时检测循环依赖（WITH RECURSIVE DFS，深度限 50）（C8）
-- kind='blocks' 和 kind='supersedes' 均做循环检测
-- kind='related' 是对称关系，不需要循环检测
```

#### run_attempts

```sql
CREATE TABLE run_attempts (
    id                   TEXT PRIMARY KEY,
    work_item_id         TEXT NOT NULL REFERENCES work_items(id),
    status               TEXT NOT NULL DEFAULT 'running'
                         CHECK (status IN ('running','paused','wrapped',
                                           'failed','superseded','lost')),
    -- claim_epoch 初始值规则（H9）：
    -- fn_claim_work_item() 内：
    --   new_attempt.claim_epoch = wi.current_attempt_epoch + 1
    --   wi.current_attempt_epoch = new_attempt.claim_epoch  (同一事务)
    -- DDL DEFAULT 1 仅作兜底，不应被直接使用
    claim_epoch          BIGINT NOT NULL DEFAULT 1,
    idempotency_key      TEXT NOT NULL,
    -- v1.21 ownership-only 最终设计：
    -- claim 后永久持有，无 expires_at。
    -- 释放路径：(a) 自己 complete（wrap/fail/pause）
    --            (b) 同 user_id 从另一台机器 force_takeover（同人自我接管）
    --            (c) maintainer/admin force_takeover
    -- last_active_at 仅用于监控"wi 多久没动了"，不作为权限门控
    last_active_at       TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
    actor_user_id        TEXT NOT NULL REFERENCES users(id),
    api_key_id           TEXT,
    actor_display        TEXT NOT NULL,
    machine_id           TEXT NOT NULL,
    session_secret_hash  TEXT NOT NULL,
    parent_attempt_id    TEXT REFERENCES run_attempts(id),
    -- v1.23: phase_yaml_snapshot 已删除，改为记录 scenario_phase_configs 的版本号
    -- fn_claim_work_item 直接读 scenario_phase_configs[wi.scenario]，无需 client 上传
    phase_config_version INT,    -- claim 时 scenario_phase_configs.version 的快照，供审计
    prepared_workspace   JSONB,              -- {repo: abs_path}，仅本地参考，不做校验
    started_at           TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
    ended_at             TIMESTAMPTZ,
    UNIQUE (work_item_id, idempotency_key),
    UNIQUE (work_item_id, claim_epoch)
);
CREATE INDEX idx_ra_wi_status ON run_attempts(work_item_id, status);
CREATE INDEX idx_ra_actor ON run_attempts(actor_user_id, status);
CREATE INDEX idx_ra_last_active ON run_attempts(last_active_at) WHERE status = 'running';
-- v1.21: idx_ra_expires 已删除（expires_at 列已移除）
```

#### wi_step_state

```sql
CREATE TABLE wi_step_state (
    work_item_id           TEXT PRIMARY KEY REFERENCES work_items(id) ON DELETE CASCADE,
    wi_type                TEXT NOT NULL,
    -- graph_source（v1.23 更新）：
    --   'scenario_config'  = 从 scenario_phase_configs[wi.scenario] 读取（标准路径）
    --   'scenario_default' = scenario 内置默认 graph（scenario_phase_configs 为空时兜底）
    graph_source           TEXT NOT NULL DEFAULT 'scenario_config'
                           CHECK (graph_source IN ('scenario_config','scenario_default')),
    current_step           TEXT,  -- NULL = start_step/所有步骤完成，等待 stop_step 或已结束
    -- C2：记录当前 step 的执行状态（Wi Agent 超时检测依赖此字段）
    current_step_status    TEXT DEFAULT 'idle'
                           CHECK (current_step_status IN ('idle','in_progress')),
    current_step_attempt   TEXT,              -- 当前 step_attempt_id，in_progress 时有值
    step_started_at        TIMESTAMPTZ,       -- 当前 step 开始时间（超时检测用）
    version                BIGINT NOT NULL DEFAULT 0,
    updated_at             TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp()
);
-- CAS update（推进 step）:
-- UPDATE wi_step_state
-- SET current_step=$next, current_step_status='idle',
--     current_step_attempt=NULL, step_started_at=NULL,
--     version=version+1, updated_at=clock_timestamp()
-- WHERE work_item_id=$id AND version=$expected
-- RETURNING version  -- 失败（0 rows）表示并发冲突

-- CAS update（标记 in_progress，server 生成 step_attempt_id）:
-- BEGIN;
-- sa_id := NewID('sa');  -- server 生成
-- UPDATE wi_step_state
-- SET current_step_status='in_progress',
--     current_step_attempt=sa_id,
--     step_started_at=clock_timestamp(),
--     version=version+1
-- WHERE work_item_id=$id AND version=$expected
--   AND current_step_status='idle'  -- 防止重复 in_progress
-- RETURNING version;
-- -- 0 rows → ROLLBACK（并发抢先或 version 不对）
-- COMMIT;

-- C6-4: completed 操作必须原子（INSERT completion + UPDATE state 同一事务）
-- 且两个条件（CAS version + step_attempt_id）合并到同一 WHERE，消除 TOCTOU：
-- BEGIN;
-- INSERT INTO wi_step_completions(id, work_item_id, step_id, step_attempt_id,
--                                  status, artifact_summary, completed_at, run_attempt_id)
-- VALUES(NewID('sc'), $wi_id, $step_id, $step_attempt_id,
--        'completed', $artifact, clock_timestamp(), $attempt_id);
-- -- UNIQUE(step_attempt_id) 阻止并发双写
--
-- UPDATE wi_step_state
-- SET current_step=$next_step,
--     current_step_status='idle',
--     current_step_attempt=NULL,
--     step_started_at=NULL,
--     version=version+1, updated_at=clock_timestamp()
-- WHERE work_item_id=$id
--   AND version=$expected           ← CAS
--   AND current_step_attempt=$step_attempt_id;  ← 防 TOCTOU（C6-4 修正）
-- -- 0 rows → ROLLBACK
-- COMMIT;
```

#### wi_step_completions

```sql
-- append-only，独立于 wi_step_state，记录完整 step 历史（含重试）
CREATE TABLE wi_step_completions (
    id               TEXT PRIMARY KEY,   -- sc_<ulid>
    work_item_id     TEXT NOT NULL REFERENCES work_items(id),
    step_id          TEXT NOT NULL,
    step_attempt_id  TEXT NOT NULL,      -- server 生成，防并发双写
    status           TEXT NOT NULL CHECK (status IN ('completed','failed')),
    artifact_summary TEXT,               -- max 4096 Unicode characters（L10）
                                         -- length() 在 PG 中计算字符数（非字节数）
                                         -- 超长 artifact 存 memory，这里只存摘要引用
    error_type       TEXT,
    escalated        BOOL DEFAULT FALSE,
    completed_at     TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
    run_attempt_id   TEXT REFERENCES run_attempts(id),  -- H11: 原 attempt_id 改名
    CHECK (length(artifact_summary) <= 4096)
);
CREATE INDEX idx_wsc_wi ON wi_step_completions(work_item_id, step_id, completed_at DESC);
CREATE UNIQUE INDEX idx_wsc_attempt ON wi_step_completions(step_attempt_id);
-- step_attempt_id UNIQUE 防止同一 step_attempt 双写
```

#### resource_locks

```sql
CREATE TABLE resource_locks (
    resource_type    TEXT NOT NULL
                     CHECK (resource_type IN ('git_branch','worktree',
                                              'file_scope','tcp_port','deploy_env')),
    resource_key     TEXT NOT NULL,
    -- C-R7-7: run_attempts 是 append-only（永不物理 DELETE），CASCADE 是死代码
    -- 改为 ON DELETE RESTRICT，与 M-R3-12 "禁止物理删" 原则一致
    owner_attempt_id TEXT NOT NULL REFERENCES run_attempts(id) ON DELETE RESTRICT,
    claim_epoch      BIGINT NOT NULL,   -- H-R3-6: 与 run_attempts.claim_epoch 类型统一
    acquired_at      TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (resource_type, resource_key)
);
CREATE INDEX idx_locks_owner ON resource_locks(owner_attempt_id);
```

#### agent_events

```sql
CREATE TABLE agent_events (
    -- C-R3-1: PG 要求分区表 UNIQUE/PK 必须包含分区键 created_at
    id             TEXT NOT NULL,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (id, created_at),            -- 复合 PK，包含分区键
    -- H-R9-9: work_item_id 改 NULLABLE，允许全局系统事件（phase_config_updated 等）
    -- CHECK: 普通 lifecycle 事件（attempt_started 等）必须有 wi_id；系统事件允许 NULL
    work_item_id   TEXT REFERENCES work_items(id),
    run_attempt_id TEXT REFERENCES run_attempts(id),
    actor_user_id  TEXT REFERENCES users(id),
    actor_display  TEXT,           -- 快照，从 users.display_name 写入
    api_key_id     TEXT,
    project        TEXT,           -- 冗余列，从 work_items.project 写入（INSERT 时）
                                   -- 避免 project 级查询 JOIN 跨分区表
    event_type     TEXT NOT NULL,
    -- lifecycle: work_item_filed, attempt_started, attempt_completed,
    --            attempt_superseded, force_takeover, admin_force_takeover,
    --            wi_unblocked, stop_step_partial_failure
    -- locks: lock_acquired, lock_released
    -- coding: commit, push, pr_opened, push_blocked_base_moved
    -- step: step_started, step_completed, step_failed, step_heartbeat,
    --       step_agent_unresponsive
    -- memory: memory_saved, memory_activated, memory_archived, memory_reinforced
    -- conflict: conflict_prediction_overridden
    -- admin: admin_unblock, admin_redact  (H-R3-4: 统一命名)
    -- misc: note, decision, wi_stalled, wi_goal_updated
    payload        JSONB NOT NULL DEFAULT '{}',
                   -- H14: 64KB 上限由 server middleware 校验
    pinned         BOOLEAN NOT NULL DEFAULT FALSE
) PARTITION BY RANGE (created_at);  -- monthly partitions

-- 初始 partition（按需创建）
CREATE TABLE agent_events_2026_05 PARTITION OF agent_events
    FOR VALUES FROM ('2026-05-01') TO ('2026-06-01');
-- H13: GC job（每日 tick）负责提前创建下 60 天的 partition
-- 避免月初 0 点因无 partition 导致全系统写入瘫痪
-- 实现见 §15 GC 任务列表

CREATE INDEX idx_evt_wi_time ON agent_events(work_item_id, created_at DESC);
CREATE INDEX idx_evt_project_time ON agent_events(project, created_at DESC)
    WHERE project IS NOT NULL;   -- project 级审计查询
CREATE INDEX idx_evt_attempt ON agent_events(run_attempt_id)
    WHERE run_attempt_id IS NOT NULL;
CREATE INDEX idx_evt_pinned ON agent_events(work_item_id) WHERE pinned = TRUE;
CREATE INDEX idx_evt_type_time ON agent_events(event_type, created_at DESC);
CREATE INDEX idx_evt_step ON agent_events(work_item_id, event_type, created_at DESC)
    WHERE event_type LIKE 'step_%';
```

#### memories

```sql
CREATE TABLE memories (
    id                  TEXT PRIMARY KEY,
    project             TEXT NOT NULL,
    author_user_id      TEXT NOT NULL REFERENCES users(id),
    work_item_id        TEXT REFERENCES work_items(id),
    visibility          TEXT NOT NULL DEFAULT 'project'
                        CHECK (visibility IN ('private','project','team')),

    -- 类型分类（详见 §13）
    type                TEXT NOT NULL,
    -- experience.debug / experience.approach / experience.pitfall / experience.code
    -- fact.architecture / fact.constraint / fact.reference
    -- rule.scheduling / rule.convention / rule.process
    -- methodology.spec / methodology.plan / methodology.review
    -- methodology.execute / methodology.retro / methodology.wrap_summary

    content             TEXT NOT NULL,
    attrs               JSONB NOT NULL DEFAULT '{}',
    -- 保留字段：attrs.related_ids, attrs.reinforcements, attrs.context_snippet

    -- 遗忘曲线相关
    base_strength       SMALLINT NOT NULL DEFAULT 3
                        CHECK (base_strength BETWEEN 1 AND 5),
    stability_days      REAL NOT NULL DEFAULT 7.0,   -- 动态更新
    activation_count    INT NOT NULL DEFAULT 0,
    last_activated_at   TIMESTAMPTZ,
    last_activated_by   TEXT REFERENCES users(id),
    is_immortal         BOOL NOT NULL DEFAULT FALSE,
    -- H3: BEFORE INSERT trigger fn_mem_immortal 会强制覆盖此字段：
    --   rule.* 和 fact.* → is_immortal=TRUE，stability_days=36500（用户传入值被覆盖）
    --   methodology.* → is_immortal=TRUE，但语义是"绑定 wi 生命周期"（见下方 H4 说明）
    -- 用户不应依赖传入的 is_immortal 值被保留

    -- 状态
    status              TEXT NOT NULL DEFAULT 'active'
                        CHECK (status IN ('active','archived','redacted')),
    archived_at         TIMESTAMPTZ,
    redacted_at         TIMESTAMPTZ,
    redaction_reason    TEXT,

    -- 关联
    supersedes_id       TEXT REFERENCES memories(id),
    expires_at          TIMESTAMPTZ,
    -- B1: memory_embeddings 合并进此表（1:1，消除 JOIN）
    -- nullable：无 embedding 的 memory（rule.* 等）直接留 NULL
    emb_model           TEXT,      -- 'text-embedding-3-small' / 'nomic' / 'bge-small'
    emb_dims            INT,
    emb_vector          VECTOR,    -- H-R3-13: pgvector 0.7+ 变长 VECTOR
    created_at          TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp()
);

CREATE INDEX idx_mem_project_type ON memories(project, type)
    WHERE status = 'active';
CREATE INDEX idx_mem_wi ON memories(work_item_id)
    WHERE work_item_id IS NOT NULL;
CREATE INDEX idx_mem_author ON memories(author_user_id, visibility);
CREATE INDEX idx_mem_expires ON memories(expires_at)
    WHERE expires_at IS NOT NULL AND status = 'active';
CREATE INDEX idx_mem_activation ON memories(last_activated_at)
    WHERE status = 'active' AND is_immortal = FALSE;
-- HNSW index 按 model 建（WHERE emb_model=... 过滤）：
-- CREATE INDEX idx_mem_emb_openai ON memories
--   USING hnsw(emb_vector vector_cosine_ops) WHERE emb_model='text-embedding-3-small';

-- C-R3-2/C-R3-3: is_immortal + stability_days + expires_at 按 type 强制设置
-- 决策：fact.* 缓慢衰减（180d）不永生；rule.* 永生；methodology.* 绑定 wi
CREATE OR REPLACE FUNCTION fn_mem_immortal() RETURNS trigger AS $$
BEGIN
    IF NEW.type LIKE 'rule.%' THEN
        -- 团队规则：永不遗忘
        NEW.is_immortal = TRUE;
        NEW.stability_days = 36500;
        NEW.expires_at = NULL;
    ELSIF NEW.type LIKE 'fact.%' THEN
        -- C-R3-2: fact.* 缓慢衰减（180d base），is_immortal=FALSE
        -- §7.2 中的 180d 定义优先（trigger 原来错误设为 36500d）
        NEW.is_immortal = FALSE;
        NEW.stability_days = 180.0;
    ELSIF NEW.type LIKE 'methodology.%' THEN
        -- C-R3-3 注释修正：methodology.* is_immortal=FALSE（绑定 wi 生命周期）
        -- expires_at 在 pf_wrap 时由 server 设为 wi.closed_at + 90d
        NEW.is_immortal = FALSE;
        NEW.stability_days = 36500;  -- wi 关闭前不衰减，关闭后靠 expires_at GC
    END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;
CREATE TRIGGER trg_mem_immortal BEFORE INSERT ON memories
FOR EACH ROW EXECUTE FUNCTION fn_mem_immortal();
```

---
-- B1: memory_embeddings 表已删除，embedding 字段（emb_model/emb_dims/emb_vector）合并进 memories 表

## 3. 状态机矩阵

### 3.1 work_items.status

```
-- 进入 running：
queued  → running  （claim 成功）
paused  → running  （re-claim 成功）

-- 进入 blocked（M15）：
queued  → blocked  （server 在 INSERT/UPDATE wi_dependencies 后
                    检测到有未完成 blocker → 自动设 status=blocked）
-- 注：blocked 不是用户主动设置，是 server 根据 wi_dependencies 自动维护

-- blocked 解锁（M14）：
blocked → queued   （GC job 的 unblock_dependent_wi sweep 检测到
                    所有 blocking wi 均 wrapped/cancelled/failed）

-- 进入 blocked（H-R3-5）：
running → blocked  （step_failed escalated=true → server emit wi_stalled event
                    + wi.status → blocked；参见 §3.3 / §3.1 统一定义）
                    注意：§10.2 stalled queue 仅查 status=blocked 且有 wi_stalled event

-- 完成/终止：
running → paused   （pf_complete_attempt(paused)，同时释放所有 locks，见 C10）
running → wrapped  （pf_complete_attempt(wrapped)，同时释放所有 locks，见 H-R3-7）
running → failed   （pf_complete_attempt(failed)，同时释放所有 locks，见 H-R3-7）
running → running  （force_takeover：旧 attempt 标 superseded，新 attempt 接管）
paused  → cancelled（user 显式放弃）
queued  → cancelled（admin 取消）
blocked → queued   （GC unblock sweep 或 admin 手动解锁）

-- terminal 不可逆：wrapped / failed / cancelled
-- C-R3-6: paused 保留为非 terminal；re-claim 创建新 attempt（旧 row 不变）
-- 需要继续：开新 wi + supersedes link（若 terminal）/ re-claim（若 paused）
```

### 3.2 run_attempts.status

```
running → paused     （pf_complete_attempt(paused)）
           -- C10 + H-R3-7: 所有 terminal/paused 转移，fn_complete_attempt 统一处理：
           --   UPDATE run_attempts SET status=$new
           --   DELETE FROM resource_locks WHERE owner_attempt_id=$id
           -- 保证 paused/wrapped/failed 都释放锁
running → wrapped    （pf_complete_attempt(wrapped)，同时 DELETE resource_locks）
running → failed     （pf_complete_attempt(failed)，同时 DELETE resource_locks）
running → superseded （force_takeover：旧 attempt 被新 attempt 替换）
running → lost       （force_takeover 时旧 attempt 被标为 superseded/lost）
paused  → running    （re-claim：创建新 attempt，旧 paused row 永保 paused）
           -- C-R3-6: re-claim 不是同一 row 复活，而是新 INSERT run_attempts
           --         wi.current_attempt_id 指向新 attempt，旧 row 不变
-- terminal：wrapped / failed / superseded / lost

-- C4: 只有 status='running' 的 attempt 才通过 AttemptCredential 校验（见 §22）
-- paused attempt 调用 mutating 工具会被拒绝（除 re-claim 外）
```

### 3.3 wi_step_state（step 内部状态）

```
-- wi_step_state.current_step_status 字段记录执行状态
-- wi_step_completions 记录完成/失败历史（append-only）

step 转移：
  idle → in_progress  （pf_update_step(in_progress) → server 生成 step_attempt_id）
  in_progress → completed  （pf_update_step(completed, step_attempt_id=$x)）
                           → server 推进 current_step，重置 current_step_status='idle'
  in_progress → failed (escalate=false)
                           → 重置 in_progress → idle，允许重试
  in_progress → failed (escalate=true)
                           → wi.status → blocked，emit wi_stalled event

-- step_attempt_id 机制（C2/§19.5）：
  1. pf_update_step(in_progress) → server 在 wi_step_state 写 current_step_attempt
                                   → 生成 step_attempt_id 返回给 Step Agent
  2. pf_update_step(completed, step_attempt_id=$x)
     → server 校验 wi_step_state.current_step_attempt == $x
     → 两个 Step Agent 并发：先到者写成功，后到者 409 CONFLICT_STEP_ATTEMPT_MISMATCH

-- Wi Agent 超时检测（§21.7）：
  wi_step_state.step_started_at 在 in_progress 时写入
  Wi Agent 60s 后查 current_step_status：
    仍是 in_progress → step_started_at 距今 > 60s → emit step_agent_unresponsive
    已是 idle → step 已完成或失败（读 wi_step_completions 确认）
```

---

## 3.4 权限矩阵

三层权限模型：

```
Layer 1  AuthN        API key 有效 + 未过期 + project_scope 匹配
Layer 2  AuthZ-Project users.project_roles[project] 角色
Layer 3  AuthZ-Attempt AttemptCredential 三元组（已 claim 的操作自动满足 Layer 2）
```

**全局 role=admin 绕过所有 project 检查。**

| 操作 | viewer | writer | maintainer | admin |
|------|:------:|:------:|:----------:|:-----:|
| 读 wi / events / memory(project) | ✓ | ✓ | ✓ | ✓ |
| 读 memory(team)（同 project 内）| ✓ | ✓ | ✓ | ✓ |
| 读 memory(private) | 仅自己 | 仅自己 | 仅自己 | ✓ |
| 创建 wi | ✗ | ✓ | ✓ | ✓ |
| claim wi | ✗ | ✓ | ✓ | ✓ |
| 执行 wi（持有 attempt 后） | ✗ | ✓ | ✓ | ✓ |
| 取消自己的 wi（queued/paused） | ✗ | ✓ | ✓ | ✓ |
| 取消他人 wi | ✗ | ✗ | ✓ | ✓ |
| force_takeover 自己的 attempt（同 user_id 换机器）| ✓ | ✓ | ✓ | ✓ |
| force_takeover 他人的 attempt | ✗ | ✗ | ✓ | ✓ |
| 写 memory(project) | ✗ | ✓ | ✓ | ✓ |
| 写 memory(team) | ✗ | ✗ | ✗ | ✓ |
| redact memory(他人) | ✗ | ✗ | ✗ | ✓ |
| 管理项目成员/project_roles | ✗ | ✗ | ✓ | ✓ |
| emit admin event | ✗ | ✗ | ✗ | ✓ |
| 创建/管理 user | ✗ | ✗ | ✗ | ✓ |

**force_takeover 权限判断（v1.21 简化，Go 实现）**：

```go
func forceTakeoverPerm(attempt *RunAttempt, caller *User, project string) Permission {
    // 同一个人换机器：总是允许（不需要 maintainer 权限）
    if attempt.ActorUserID == caller.ID {
        return PermWriter
    }
    // 他人的 attempt：需要 maintainer 或 admin
    return PermMaintainer
    // v1.21: 删除 expires_at 时间判断。ownership 是永久的，不靠超时释放。
}
```

---

## 4. HTTP API

### 4.1 认证

```
所有请求：Authorization: Bearer <api_key>
Mutating 操作：Idempotency-Key: <client-generated idem_<8b62>>（所有 POST/PATCH）
Attempt-scoped：X-Attempt-Id + X-Claim-Epoch + X-Session-Secret
Admin 操作：token 决定权限，不靠参数
```

**Idempotency-Key**：server 缓存 24h，重复请求直接回放响应，防网络重试双写。
- C-R7-6：cache key 必须是 `(Idempotency-Key, api_key_id)` 而非单 key，防跨用户碰撞回放
- 缓存存储：PG 独立表（同事务写入，先 COMMIT 再发响应；保证重启后不丢）或共享 Redis（多副本部署时必选）

### 4.2 ID 解析

接受：`wi_<ulid>`（精确）或 `<project>#<seq>`（slug）或 `#<seq>`（client 补全 project）。

### 4.3 端点（完整）

#### Work Items

```
POST   /v1/work_items
  body: {project, goal, scenario?, kind?, priority?, milestone?,
         labels?, declared_resources?, parent_work_item_id?,
         blocked_by?,    -- [wi_id, ...]，server 原子写入 wi_dependencies，引用的 wi 必须已存在
         source?, attrs?, force_create?, force_reason?}
  → {id, slug} 或 409 {code:DUPLICATE, existing:{id,slug,goal}}
                    或 409 {code:CANDIDATES, candidates:[{id,slug,goal,similarity}]}

GET    /v1/work_items
  query: project, status(multi), wi_type, milestone, label,
         user_id, source, ready_only, ids(multi), include_step_state, since, limit, cursor
  → {items:[{id,slug,...}], next_cursor}

GET    /v1/work_items/{id_or_slug}
  query: include_step_state, include_events(last N)
  → {work_item, current_attempt, per_repo_state, step_state?, recent_events?}

PATCH  /v1/work_items/{id_or_slug}
  body: {kind?, priority?, milestone?, labels?,
         declared_resources?, resources_version(CAS)?, attrs?,
         -- C4-3/决策6: status 字段从 PATCH 中删除
         -- 状态变更走专用接口（/claim /complete /cancel /force_takeover）
         goal?,                  -- 允许更新，但有限制：
         goal_change_reason?}    --   必须同时传；caller=reporter/maintainer；status=queued/paused
                                 --   goal 更新不重置 step_state（保留原步骤）
  → {work_item}
  goal 更新失败：409 GOAL_CHANGE_NOT_ALLOWED（wi 正在 running）
                403 FORBIDDEN（非 reporter/maintainer）

POST   /v1/work_items/{id_or_slug}/cancel
  -- 决策6: 新增专用 cancel 接口
  body: {}     -- 无需 attempt credential（caller 需有 cancel 权限）
  权限：reporter 本人（queued/paused）/ maintainer（任意非 terminal）/ admin
  约束：running 状态必须先 complete(paused) 再 cancel，或 admin 直接 force
  → {ok}

POST   /v1/work_items/{id_or_slug}/unblock
  -- H-R7-11 / Admin #6：§3.1 "admin 手动解锁 blocked wi" 的专用接口
  -- PATCH status 已删除，无法直接修改 wi.status，需此端点
  body: {reason}   -- admin only
  权限：admin
  side effect：UPDATE work_items SET status='queued' WHERE id=$ AND status='blocked';
               emit admin_unblock event（已在 admin whitelist）
  约束：仅 status=blocked 的 wi；terminal wi 返回 409
  → {ok}

POST   /v1/work_items/{id_or_slug}/claim
  body: {idempotency_key, session_info:{machine_id,session_secret},
         requested_locks?, mode:fresh|resume, force_takeover?,
         phase_yaml_snapshot?}       ← client 上传 phase.yaml 内容
  → {attempt_id, claim_epoch, expires_at, session_secret,
     acquired_locks, current_attempt_epoch}

POST   /v1/work_items/{id_or_slug}/complete
  body: {attempt_id, claim_epoch, session_secret,
         status: wrapped|failed|paused}
  -- H-R9-11: status=paused 时 server 自动 force_terminate in_progress step（不需要 client 传）
  -- status=wrapped/failed 时仍需显式传 force_terminate_step=true
  约束：如有 step 处于 in_progress：
    status=paused → server 自动 force_terminate（implicit）
    status=wrapped/failed → 拒绝（409），除非带 force_terminate_step=true
  force_terminate_step=true 时 server 原子事务（C5-4 + C6-3 必填字段补全）：
    INSERT INTO wi_step_completions (
      id, work_item_id, step_id, step_attempt_id,
      status, error_type, escalated, completed_at, run_attempt_id
    ) VALUES (
      NewID('sc'), $wi_id, wi_step_state.current_step,
      wi_step_state.current_step_attempt,
      'failed', 'force_terminate_step', false,
      clock_timestamp(), $attempt_id
    );
    -- 同时 emit step_failed event
    INSERT INTO agent_events(id, work_item_id, event_type, payload, project, created_at)
    VALUES(NewID('evt'), $wi_id, 'step_failed',
           '{"error_type":"force_terminate_step","escalated":false}',
           wi.project, clock_timestamp());
    UPDATE wi_step_state SET current_step_status='idle', current_step_attempt=NULL,
                             step_started_at=NULL, version=version+1
    WHERE work_item_id=$wi_id AND current_step_status='in_progress';
  → {ok}

POST   /v1/work_items/{id_or_slug}/force_takeover
  body: {reason}             -- H8: 必须带 Idempotency-Key header
                             -- server 用 (wi_id, prior_attempt_id) 隐式幂等
  权限：同一 user_id（自我接管，任何时间）/ maintainer / admin
  -- v1.21: 不再有"idle > 30min"时间门槛，纯靠角色
  server 事务内原子执行：
    ① 旧 attempt → superseded
    ② 若 wi_step_state.current_step_status='in_progress'：
       -- H-R7-4: run_attempt_id 必须写旧 attempt（被打断的那个），不是新 attempt
       INSERT wi_step_completions(
         id=NewID('sc'), work_item_id=$wi_id,
         step_id=wi_step_state.current_step,
         step_attempt_id=wi_step_state.current_step_attempt,
         run_attempt_id=<prior_attempt_id>,   ← 旧 attempt（被 takeover 的那个）
         status='failed', error_type='force_takeover', escalated=false,
         completed_at=clock_timestamp()
       )
       UPDATE wi_step_state SET current_step_status='idle', current_step_attempt=NULL,
                                step_started_at=NULL, version=version+1
  → {prior_attempt_id, prior_actor_display, ok}
```

#### Dependencies（C6：对应 MCP 工具 pf_create_dependency / pf_remove_dependency）

```
POST   /v1/work_items/{id}/dependencies
  body: {blocking_wi_id, kind:blocks|supersedes|related, note?}
  循环检测：kind='blocks' 和 kind='supersedes' 均做 WITH RECURSIVE 检测（C8），
            有环返回 409 CONFLICT_DEPENDENCY_CYCLE
  → {ok}

DELETE /v1/work_items/{id}/dependencies/{blocking_id}/{kind}
  → {ok}

GET    /v1/work_items/{id}/dependencies
  -- Dave-2 GAP-4 修正：跨 project 信息泄漏防护
  -- 返回的 wi entry 按 caller 的 project_roles 过滤：
  --   caller 有 viewer+ → 返回完整 {id, slug, kind, note}
  --   caller 无权限 → 折叠为 {id:"hidden", project:"<project_name>", kind:"<kind>"}
  --   (让对方知道"有依赖"但看不到具体 slug/goal，防止探测不可见 wi)
  → {blocking:[{id,slug?,kind,note,project},...], blocked_by:[...]}
```

-- B3/v1.24: /v1/locks 独立 endpoint 已删除
-- 锁由 fn_claim_work_item（POST /claim，自动 acquire）
-- 和 fn_complete_attempt（POST /complete，自动 release）隐式管理
-- admin 维护工具通过 server 内部 function 操作，不暴露为 HTTP endpoint

#### Events

```
POST   /v1/events
  body: {work_item_id, attempt_id, claim_epoch, session_secret,
         event_type, payload, pinned?, admin?}
  admin=true：token 必须 role=admin；event_type 必须在 admin whitelist
  → {event_id}

GET    /v1/events
  query: work_item_id?,     -- 按单个 wi 查（精确）
         project?,          -- 按 project 查所有 wi 的 events（审计场景）
         user_id?,          -- 按操作者查
         types(multi)?, since?, limit?, pinned_first?
  -- work_item_id 和 project 至少传一个
  -- project 查询走 idx_evt_project_time，无需 JOIN
  → {events:[{id, work_item_id, work_item_slug, event_type, actor_display,
              project, payload, pinned, created_at}], next_cursor}
```

#### Step State (Layer 2)

```
GET    /v1/work_items/{id}/step
  → {
      current_step, wi_type, graph_source, version,
      step_def: {id, action, description, requires},
      resolved_skill?,                    ← action 三级 fallback 结果
      previous_steps: {step_id: {artifact_summary, step_attempt_id}},
      progress: {current:N, total:M}
    }

PATCH  /v1/work_items/{id}/step
  body: {attempt_id, claim_epoch, session_secret,
         step_id, status:in_progress|completed|failed,
         step_attempt_id?,    ← in_progress 时 server 生成并返回；
                                completed/failed 时必须传回校验
         artifact_summary?,   ← max 4096 chars
         error_type?,
         escalated?,          ← true → server emit step_failed + wi stalled
         expected_version}    ← CAS，失败返回 412 + {current_version, current_state}
  → {step_attempt_id?, next_step?, version}
```

#### Conflicts

```
POST   /v1/conflicts/predict
  body: {work_item_id?, declared_resources, dry_run?}
  注意：predict 是 advisory，claim 时仍会二次原子检测
  → {severity, predictions:[...], will_unlock:[...]}
```

#### Artifacts

```
POST   /v1/artifacts
  body: {work_item_id, attempt_id, claim_epoch, session_secret,
         artifact_type:pr|branch|issue, artifact_key,
         action:adopt|ignore|close}
  → {ok}

GET    /v1/artifacts?work_item_id={id}
  → {artifacts:[...]}
```

#### Memory

```
POST   /v1/memories
  body: {project, type, content, visibility, work_item_id?,
         base_strength?, attrs?, expires_at?,
         dedup_mode:strict|suggest|off,    ← 默认 suggest
         related_memory_ids?, context_snippet?,
         supersedes_memory_id?}
  dedup_mode=strict  → 高度相似时 409 {code:SIMILAR_EXISTS, existing:{...}}
  dedup_mode=suggest → 写入，attrs.similar_to 记录已有 memory ID
  dedup_mode=off     → 强制写入（methodology.* 使用）
  → {memory_id} 或 409 {code:SIMILAR_EXISTS, existing:{id,content,similarity}}

POST   /v1/memories/{id}/activate
  body: {}    -- M3: actor_user_id 从 Bearer token 推导，不由 client 传入
  → {activation_count, new_stability_days, effective_strength}

POST   /v1/memories/{id}/reinforce
  body: {additional_context, strength_delta?, attempt_id, claim_epoch, session_secret}
  → {memory_id, activation_count, base_strength}

GET    /v1/memories
  -- H2: type 参数语法定义：string[]，每个元素是 <scheme>、<scheme>.*、<scheme>.<subtype>
  -- 例：type=["experience.*","rule.scheduling"] 或 type=["methodology.spec"]
  -- H2: type 参数语法定义：string[]，每个元素是 <scheme>、<scheme>.*、<scheme>.<subtype>
  -- 例：type=["experience.*","rule.scheduling"] 或 type=["methodology.spec"]
  query: project, type(string[], 前缀/精确匹配), visibility,
  -- M10: visibility 过滤语义：仅返回 visibility=X 的 memory
  --      server 另外强制 access control：private 只返回 author=caller 的条目
  work_item_id, query(语义/文本), top_k,
  similarity_threshold,
  min_strength(default 0.3, M7),  ← 改为 0.3（原 1.0 几乎过滤所有 memory）
  include_archived(default false), recency_weight(default 0.3), cursor
  → {items:[{
      id, type, content, visibility,
      author_display,                  -- 快照，如 "Wang Xiaokang"
      last_activated_by_display?,      -- 快照，如 "Alice (machine)"
      effective_strength, activation_count,
      last_activated_at, created_at
    }], next_cursor}

PATCH  /v1/memories/{id}/redact
  body: {reason}
  → {ok}
```

#### Ready Queue (Layer 3)

```
GET    /v1/work_items/ready
  query: project, max(default 10), non_conflicting
  -- v1.20：六段视图（加 unclassified[]）
  → {
      -- items: queued + no blocker + requires_human_session=false → Orchestrator 自动派发
      items: [{id, slug, kind, wi_type, priority, goal, unblocked_at}],
      -- running wi
      running: [{id, slug, goal, owner_display, owner_user_type, expires_at, last_active_at}],
      -- stalled（blocked + wi_stalled event）
      stalled: [{id, slug, stall_reason, stalled_since, last_actor_display,
                 stalled_at_step:{step_id,current,total}, error_type}],
      -- paused wi
      paused: [{id, slug, paused_since, last_actor_display, pause_reason}],
      -- needs_human_session: queued + no blocker + requires_human_session=true → Session 2/3
      needs_human_session: [{id, slug, kind, wi_type, priority, goal, created_at}],
      -- unclassified: queued + no blocker + requires_human_session IS NULL → 需补分类
      --   → 来自 v1.18 升级存量数据 或 AI 未提供 phase.yaml 的 create 调用
      unclassified: [{id, slug, kind, wi_type, priority, goal, created_at}]
    }
```

#### Users & Auth (Admin)

```
POST   /v1/admin/users
  body（人类用户）: {email, display_name, role?, project_roles?, author_aliases?}
  body（machine 用户）: {display_name, user_type:"machine", project_roles}
  -- machine user 的 email 自动合成：machine-<display_name_slug>@polyforge.internal
  → {id, email, display_name, user_type, role, project_roles}

GET    /v1/admin/users
  query: project?, role?, user_type?
  → {items:[{id, display_name, user_type, role, project_roles}]}

PATCH  /v1/admin/users/{id}
  body: {display_name?, role?, project_roles?, author_aliases?}
  -- project_roles 整体替换（非 merge），需要完整传入
  -- server 校验每个 value 必须是 "viewer"|"writer"|"maintainer"，否则 400 BAD_REQUEST
  → {user}

POST   /v1/admin/users/{id}/keys
  body: {name, project_scope?, expires_at?}
  → {key_id, raw_key}   -- raw_key 仅返回一次，之后只存 hash

DELETE /v1/admin/users/{id}/keys/{key_id}
  -- 软删：设 revoked_at = now()
  → {ok}
```

#### Scenario Phase Config

```
GET    /v1/scenarios/{scenario}/phase_config
  权限：viewer+
  → {scenario, content:{wi_types:{...}, classification_rules:[...]}, version, updated_at, updated_by}

PUT    /v1/scenarios/{scenario}/phase_config
  权限：maintainer/admin
  body: {content:{wi_types:{...}, classification_rules:[...]}, version}  ← CAS
  -- C-R9-2: 409 时禁止自动 merge，client 必须 GET 最新版本 + 人工 review diff + 重新 PUT
  -- /pf-add-type skill: 409 → 展示 diff，停下来等用户确认后再重试，绝不自动合并
  -- C-R9-4: server 写入前校验 classification_rules[].set.wi_type ∈ wi_types keys
  --   否则 → 400 INVALID_PHASE_YAML
  → {version}  或 409 CONFLICT_VERSION_MISMATCH
  side effect：emit phase_config_updated event {scenario, old_version, new_version, changed_by}
```

#### System

```
GET    /v1/health   → {status,version,db_ok}
GET    /v1/version  → {version,git_commit,build_time,min_client_version}
```

---

## 5. MCP Tools（32 个，见下文分类）

### 5.1 命名与认证约定

前缀 `pf_`。Mutating 工具必须带 `attempt_id + claim_epoch + session_secret`。
H-R3-9: server 从 attempt_id 反查 work_item_id，调用方无需显式传 work_item_id。
H-R3-8: Idempotency-Key 仅走 HTTP header（所有 POST/PATCH 必须带）；
        pf_claim_work_item body 的 idempotency_key 用于 DB 去重（两者语义不同，都必填）。

### 5.2 Core Tools（19 个）

```
pf_whoami()
  → {user_id, display_name, role, projects, server_version}

pf_get_scenario_config(scenario)
  -- 读取 scenario_phase_configs，AI 创建 wi 前用此查询可用 wi_type
  -- 权限：viewer+
  → {scenario, wi_types:{name:{requires_human_session, start_step, steps}},
     classification_rules:[...], version}

pf_update_scenario_config(scenario, content, version)
  -- 更新 scenario phase config（CAS），同步本地 phase.yaml 改动到 server
  -- 权限：maintainer/admin
  -- content: {wi_types:{...}, classification_rules:[...]}
  → {version}  或 409 CONFLICT_VERSION_MISMATCH

pf_create_work_item(project, goal, scenario?, priority?,
                    wi_type?,             -- AI 选择，对应 phase.yaml 中的键（如 fix_bug/feature）
                    requires_human_session?,  -- 从 phase.yaml wi_type 定义读取；可显式覆盖
                    milestone?, labels?, declared_resources?,
                    parent_work_item_id?, source?, attrs?,
                    blocked_by?,          -- [wi_id, ...]，创建时原子写入 wi_dependencies
                    force_create?, force_reason?)
  -- blocked_by：server 在同一事务内 INSERT work_items + INSERT wi_dependencies
  -- 引用的 blocking wi 必须已存在（FK 约束），调用方保证拓扑顺序
  → {id, slug} | {code:DUPLICATE,...} | {code:CANDIDATES,...}

pf_list_work_items(project?, status?, kind?, milestone?, label?,
                   user_id?, source?, ready_only?, ids?,
                   include_step_state?, since?, limit?, cursor?)
  → {
      items: [{
        id, slug, wi_type, priority, status, goal,
        reporter_display,        -- 快照
        owner?: {                -- 仅 status=running|paused 时有值
          attempt_id,
          display,               -- actor_display 快照，如 "Alice (machine)"
          last_active_at         -- 监控用：多久没动了（v1.21: expires_at 已删）
        },
        step?: {current, total, step_id}  -- 仅 include_step_state=true 且有 step_state
      }],
      next_cursor
    }

pf_update_work_item(id_or_slug, kind?, priority?, milestone?,
                    wi_type?,                   -- 允许 reporter/maintainer/admin 纠正错误分类
                    requires_human_session?,    -- 与 wi_type 联动；仅 queued/paused 状态允许修改
                    -- 修改 wi_type 时 server 校验：
                    --   1. phase_yaml_snapshot 是否包含该 wi_type
                    --   2. 若 requires_human_session 未传则从 phase.yaml 自动推断
                    --   3. emit wi_reclassified event {old_wi_type, new_wi_type, old_rhs, new_rhs, reason}
                    reclassify_reason?,         -- 修改 wi_type 时必填（min 10 chars，同 goal_change_reason）
                    labels?, declared_resources?, resources_version?,
                    attrs?,
                    goal?,                -- Alice WALL-19a: 允许更新 goal
                    goal_change_reason?)  -- 与 goal 同时必填；status=queued/paused 才允许
  → {work_item}

pf_claim_work_item(id_or_slug, idempotency_key, session_info,
                   requested_locks?, mode:fresh|resume,
                   force_takeover?)
  -- v1.23: phase_yaml_snapshot 参数已删除
  -- server 直接读 scenario_phase_configs[wi.scenario]，client 无需上传
  → {attempt_id, claim_epoch, expires_at, session_secret,
     acquired_locks, current_attempt_epoch}

pf_complete_attempt(attempt_id, claim_epoch, session_secret,
                    status:wrapped|failed|paused,
                    force_terminate_step?)
  → {ok}

pf_force_takeover(id_or_slug, reason)
  → {prior_attempt_id, prior_actor_display,
     new_attempt_id, new_claim_epoch, new_expires_at, ok}
  -- Carol-2 WALL-6: force_takeover 包含 claim 语义，返回 new attempt credential
  -- session_secret 由 MCP server 写入 state file（决策A），不返回 skill/LLM
  -- prior_actor_display 用于 skill 输出："forced takeover from Alice (machine)"

-- B3: pf_acquire_lock / pf_release_lock 已删除
-- locks 由 fn_claim_work_item（acquire）和 fn_complete_attempt（release）自动管理
-- 暴露给 LLM 是攻击面，内部 server 保留 HTTP endpoint 供 admin tooling

pf_emit_event(work_item_id, attempt_id, claim_epoch, session_secret,
              event_type, payload, pinned?, admin?)
  -- H10: admin=true 时 token 必须 role=admin
  -- admin-only event_type 白名单：
  --   attempt_superseded, admin_force_takeover,
  --   admin_unblock, admin_redact（server 自动 emit 时也走此路径）
  → {event_id}

pf_read_events(work_item_id?, project?, user_id?, types?, since?, limit?, pinned_first?)
  -- work_item_id 或 project 至少传一个
  → {events:[...]}

pf_predict_conflicts(work_item_id?, declared_resources, dry_run?)
  -- M-R3-4: will_unlock 语义：若 claim 成功，将解锁哪些 blocked wi
  -- 计算：SELECT wi FROM wi_dependencies WHERE blocking_wi_id IN (work_item_id 的 wi)
  --       且 status=blocked 且 所有其他 blocker 均 terminal
  -- CrossProject Wall#5: predictions[] 中涉及其他 project 的 wi 按权限过滤：
  --   caller 有 viewer+：返回完整 {attempt_id, actor_display, work_item_slug, rule}
  --   caller 无权限：折叠为 {project:"<project_name>", rule, severity}（不返回 slug/actor）
  --   403 details 不返回任何 target wi 标识（防探测信道）
  → {severity, predictions:[...], will_unlock:[{id,slug,goal}]}

-- B4: pf_update_artifact 已删除
-- adopt/ignore/close 语义改为 pf_emit_event(type='artifact_action', payload={artifact_type,artifact_key,action})
-- pf_reconcile_artifacts 也已删除（B5）

pf_remember(project, type, content, visibility,
            work_item_id?, base_strength?, attrs?,
            expires_at?, dedup_mode?, related_memory_ids?,
            context_snippet?, supersedes_memory_id?)
  -- M6: 拒绝 type 前缀为 'methodology.'（改用 pf_save_artifact）
  -- H1: type 使用全名（如 'experience.debug'，不是 'debug'）
  → {memory_id} | {code:SIMILAR_EXISTS,...}

pf_recall(project, query?, type?,   -- H2: type 为 string[]，支持通配符 experience.*
          visibility?, work_item_id?, top_k?,
          similarity_threshold?, min_strength?(default 0.3),
          include_archived?(default false), recency_weight?(default 0.3))
  → {items:[{id,type,content,effective_strength,activation_count,...}]}

pf_activate_memory(memory_id)
  -- actor_user_id 从 Bearer token 推导（M3）
  → {activation_count, new_stability_days, effective_strength}

pf_reinforce_memory(memory_id, additional_context,
                    strength_delta?, attempt_id, claim_epoch, session_secret)
  -- C12: 补充此工具（§21.8 retro 中已使用）
  → {memory_id, activation_count, base_strength}

pf_redact_memory(memory_id, reason)
  → {ok}

pf_save_artifact(type: "methodology.spec"|"methodology.plan"|"methodology.review"
                      |"methodology.execute"|"methodology.retro"|"methodology.wrap_summary",
                 work_item_id, attempt_id, claim_epoch, session_secret,
                 content, structured_payload?, visibility?,
                 supersedes_memory_id?)
  -- M5: AttemptCredential 由 MCP server 从 state file 自动注入
  -- H1: type 使用全名 methodology.* 形式
  -- M6: 只接受 methodology.* 类型，其他类型返回 400 INVALID_MEMORY_TYPE
  -- C5-5: visibility default = "project"（§2.3 DDL default）
  --       team visibility 仅 admin 可写（§3.4 权限矩阵）
  -- H4: wrap 时 server 自动将 methodology.* memory 的 expires_at = wi.closed_at + 90d
  -- H-R7-5 / Memory W1: memories 表无 UNIQUE(work_item_id, type)，重复调用会写多条
  --   caller 应先 pf_recall(work_item_id, type) 找旧 memory，传 supersedes_memory_id
  --   server 收到后将旧 memory.status='archived'，新 memory.supersedes_id=旧 id
  --   若未传 supersedes_memory_id 且同 (work_item_id, type) 已存在 → 仍写入，但标 attrs.duplicate_of
  → {memory_id}
```

### 5.3 Coding Scenario Tools（6 个）

```
pf_start(workspace_root, goal, project?, kind?, priority?,
         labels?, declared_resources?, no_claim?)
  → {work_item_id, slug, attempt_id?, prepared_paths?}

-- §24: attempt_id/claim_epoch/session_secret 由 MCP server 从本地 state file 自动注入
-- client 不传这三个参数
pf_commit(workspace_root, work_item_id, repo, message, paths?)
  → {sha, files, repo}

pf_push(workspace_root, work_item_id, repo, skip_base_check?)
  → {ok, branch, base_sha_at_push}
  | {error:base_moved, advice}

pf_pr(workspace_root, work_item_id, repo, title, body, head?, base?)
  → {url, number, repo}

pf_wrap(workspace_root, work_item_id)
  -- §24: attempt_id/claim_epoch/session_secret 由 MCP server 从 state file 注入
  -- = on_wrap hook（push + PR）+ pf_complete_attempt(wrapped) + cleanup_workspace
  -- H4: wrap 时 server 自动设置该 wi 的 methodology.* memory.expires_at = now() + 90d
  -- Alice-1 W24 幂等保护：on_wrap hook 在 push/PR 前先检查 wi.attrs.github.pr_number
  --   已存在 PR → 跳过 push+PR（幂等），直接走 complete_attempt
  --   无 PR → 执行 push + PR（§21.10 commit_and_pr flow）
  --   避免走 phase.yaml ship step 已 PR 后再 wrap 导致双重 PR
  → {ok}

pf_diff(workspace_root, work_item_id, repo, vs_base?)
  → {diff: string}

-- B5: pf_reconcile_artifacts 已删除（功能通过 pf_read_events + wi.attrs 实现）
```

### 5.4 Step Execution Tools（2 个）

```
pf_get_step(work_item_id)
  -- 读取 wi_step_state + phase_yaml_snapshot + wi_step_completions
  → {
      current_step, current_step_status,
      wi_type, requires_human_session,  -- H-R8-16：Wi Agent 据此决定 start_step inline vs dispatch
      graph_source, action, description, resolved_skill?,
      -- 当前执行者（in_progress 时有值，skill 输出 "executing: Alice (machine)"）
      current_actor_display?,
      previous_steps: [{step_id, artifact_summary, step_attempt_id, status, completed_at}],
      progress: {current, total},
      version
    }

pf_update_step(work_item_id, attempt_id, claim_epoch, session_secret,
               step_id, status:in_progress|completed|failed,
               step_attempt_id?, artifact_summary?, error_type?,
               escalated?, expected_version)
  -- M17: server 在状态转移时自动 emit step_started/step_completed/step_failed event
  --      client 无需显式调用 pf_emit_event 记录 step 事件
  → {step_attempt_id?, next_step?, version}
```

### 5.5 Dependency Tools（3 个）（C6）

```
pf_create_dependency(blocked_wi_id, blocking_wi_id,
                     kind:blocks|supersedes|related,
                     attempt_id, claim_epoch, session_secret,
                     note?)
  -- 跨 project 权限：blocking_wi 属于不同 project 时，
  -- caller 必须对 blocking_wi.project 有 viewer+ 权限（能看到才能引用）
  -- 否则返回 403 FORBIDDEN（防止信息泄漏：不能引用不可见的 wi）
  → {ok} | 409 CONFLICT_DEPENDENCY_CYCLE

pf_remove_dependency(blocked_wi_id, blocking_wi_id, kind,
                     attempt_id, claim_epoch, session_secret)
  → {ok}

pf_list_dependencies(wi_id)
  -- CrossProject Wall#4: caller 对 blocking/blocked wi 所属 project 无 viewer+ 时折叠
  → {
      blocking:  [{id, slug, kind, project}      -- 有权限
                | {id:"hidden", project, kind}], -- 无权限
      blocked_by:[{id, slug, kind, project}
                | {id:"hidden", project, kind}]
    }
```

### 5.6 Release Tools（2 个）（H-R3-1 补充）

```
pf_cut_alpha(workspace_root, project, repos, base_tag?, attempt_id, claim_epoch, session_secret)
  → {alpha_tag, artifacts:[{repo, sha, pr_url}]}

pf_promote(workspace_root, source_alpha_tag, new_stable_tag, project,
           attempt_id, claim_epoch, session_secret)
  → {stable_tag, artifacts:[...]}
```

### 5.6.5 Cancel（1 个）（Carol-1 WALL-4 补充）

```
pf_cancel_work_item(id_or_slug, reason?)
  -- 对应 POST /v1/work_items/{id}/cancel
  -- 权限：reporter 本人（queued/paused）/ maintainer（任意非 terminal）/ admin
  -- running 状态：必须先 force_takeover + complete(paused) 再 cancel；或 admin force=true
  → {ok}
```

### 5.7 Ready Queue（1 个）（Orchestrator 专用，补充 GAP-1）

```
pf_get_ready_queue(project, max?, non_conflicting?)
  -- 1:1 映射 GET /v1/work_items/ready，返回完整 LCRS 六段视图（v1.20）
  -- Layer 3 Orchestrator 使用此工具替代 HTTP curl
  → {
      items:   [{id, slug, kind, wi_type, priority, goal, unblocked_at}],
                -- requires_human_session=false，Orchestrator 直接 dispatch（Session 1）
      running: [{id, slug, goal, owner_display, owner_user_type,
                 expires_at, last_active_at}],
      stalled: [{id, slug, stall_reason, stalled_since, last_actor_display,
                 stalled_at_step:{step_id, current, total}, error_type}],
      paused:  [{id, slug, paused_since, last_actor_display, pause_reason}],
      needs_human_session: [{id, slug, kind, wi_type, priority, goal, created_at}],
                -- requires_human_session=true，需要 Alice 主导的 Session 2/3
      unclassified: [{id, slug, kind, wi_type, priority, goal, created_at}]
                -- requires_human_session=NULL，需补分类
    }
```

### 5.8 Scenario Testing（Phase 2 延后）

-- B6: pf_manage_actors + execute-scenario skill 均推 Phase 2，v1 不发布
-- 避免暴露无实现的 stub 给 LLM

---

## 6. Skills（12 个 v1 发布，3 个 Phase 2 延后）

### 6.1 三段式输出格式（强制）

```markdown
## 结果
<1-2 句，动词开头。错误在此明确指出。>

## 状态
| 字段 | 值 |
|---|---|
| wi      | <project#seq>                          |
| goal    | <截断到 60 字符>                        |
| status  | running                                |
| owner   | Wang Xiaokang (ra_8d2E4F1a)            |
|         | 格式：<actor_display> (<attempt_id>)   |
|         | 自己持有时：you (<attempt_id>)         |
| locks   | git_branch:polyforge/wi-xxx            |
| blocked | —                                      |
| step    | 2/4 review                             |
| expires | 28min                                  |

## 下一步
- `/pf-spec` — 写 spec，AI 引导定义范围
- `/pf-stop --pause` — 暂停释放锁
（最多 5 条；无操作写 _none_）
```

**owner 字段规范**：
- 自己持有：`you (ra_8d2E4F1a)`
- 他人持有：`<actor_display> (ra_8d2E4F1a)`，actor_display 来自 pf_list_work_items 响应中的 `owner.display`
- machine user：`Alice Agent Fleet (machine) (ra_8d2E4F1a)`
- 无持有人（queued/blocked）：`—`

多 wi 列表时状态段改表格（id/wi_type/priority/goal/status/owner_display）。违反格式 = bug。

### 6.2 Skill 目录（v1 发布，共 12 个）

| Skill | 覆盖操作 |
|-------|---------|
| `using-polyforge` | meta，Iron Rules，NL 路由（含"新 wi_type"流程 B7 合并），三段式规范，Memory-First |
| `pf-work` | new/claim/resume/force_takeover |
| `pf-stop` | pause/wrap/fail |
| `pf-status` | list/timeline/ready queue（六段 LCRS） |
| `pf-spec` | 写 spec artifact（B8: pf-debug 合并为 spec 的调试变体） |
| `pf-plan` | 写 plan，spawn child wi（含 blocked_by） |
| `pf-execute` | Wi Agent 调度 Step Agent |
| `pf-retro` | 回顾，批量 remember/activate |
| `pf-sync` | Jira/GitHub stub（Phase 2 完整实现） |
| `pf-release` | cut alpha + promote |
| `coding/prepare_context` | start_step：recall 历史经验，分析代码 |
| `coding/code_change` | code_change 步骤 |
| `coding/commit_and_pr` | commit + push + pr |

> **Phase 2 延后（不在 v1 发布）**：pf-review（§21 已有 review step，由 code_change subagent 执行）、pf-debug（合并进 pf-spec NL 路由）、pf-event（直接调 pf_emit_event，不需要独立 skill）、pf-add-type（合并进 using-polyforge NL 路由）、execute-scenario + pf_manage_actors（B6）

---

## 7. Memory 系统（完整设计）

### 7.1 类型分类（taxonomy）

```
experience.*          ← Wi Agent 执行中提炼，会遗忘（base_stability=7d）
  experience.debug    调试发现的 bug pattern
  experience.approach 解决某类问题的方法
  experience.pitfall  踩过的坑
  experience.code     代码层面的具体发现

fact.*                ← 客观事实，缓慢遗忘（base_stability=180d，is_immortal=FALSE）
                       C-R3-2: 非永生，与 trigger 一致；180d 缓慢衰减
  fact.architecture   架构决策和原因
  fact.constraint     约束条件
  fact.reference      外部文档摘要

rule.*                ← 团队规则，永不遗忘（is_immortal=TRUE）
  rule.scheduling     调度规则
  rule.convention     代码规范
  rule.process        流程规定

methodology.*         ← 绑定 wi 生命周期（H4）
  methodology.spec / plan / review / execute / retro / wrap_summary
  -- H4: is_immortal=FALSE（非永生），stability_days=36500（wi 未关闭前不衰减）
  -- wi wrap 时 server 自动设 expires_at = wi.closed_at + 90d
  -- 90d 后 GC 归档，不是"永久保留"
```

### 7.2 遗忘曲线

```
-- H-R3-10/11: 量纲统一，公式使用归一化 effective_strength ∈ [0, 1]
-- 归一化：raw = base_strength × e^(-days_since / stability_days)
--         effective_strength = raw / base_strength_max（= raw / 5）
-- 查询时直接用 raw（不除），阈值也用 raw 量纲（0-5），或全局除以 5 统一到 0-1

-- 采用 raw 量纲（不归一化），阈值用原值，更直观：
effective_strength(raw) = base_strength × e^(-days_since / stability_days)
  其中 days_since = COALESCE(now - last_activated_at, now - created_at) in days（M8）
  base_strength ∈ {1,2,3,4,5}

stability_days 随激活次数增长：
  stability_days = base_stability × (1 + activation_count × 0.5)

  base_stability by type（C-R3-2 修正）：
    experience.*  →   7 天（is_immortal=FALSE）
    fact.*        → 180 天（is_immortal=FALSE）  ← C-R3-2: 不是 36500d
    rule.*        → 36500 天（is_immortal=TRUE，不参与 GC）
    methodology.* → 36500 天（is_immortal=FALSE，wi 关闭后走 expires_at）

-- H-R3-11: 修正示例数字（base_strength=3, days_since=t）：
  experience.* 激活 0 次（stability=7d）：
    t=30d：3×e^(-30/7)  ≈ 0.04
    t=60d：3×e^(-60/7)  ≈ 0.0006 → GC 归档（< 0.1 阈值实际为 raw<0.1×base_max=0.5）
  experience.* 激活 3 次（stability=21d）：
    t=180d：3×e^(-180/21) ≈ 0.001
  experience.* 激活 10 次（stability=42d）：
    t=365d：3×e^(-365/42) ≈ 0.0003

-- GC 归档阈值（raw）：effective_strength < 0.3（宽松，宁可多保留）
-- min_strength 查询默认（raw）：0.3
-- §7.6 展示阈值也用 raw：>= 1.5 正常展示；0.5-1.5 加验证提示；< 0.5 跳过
```

### 7.3 激活机制（两步协议）

```
Step 1：pf_recall(query) → 返回候选（server 不更新任何字段）

Step 2：Wi Agent 确认某条 memory 有用 → pf_activate_memory(memory_id)
  server 更新：
    last_activated_at = clock_timestamp()
    last_activated_by = actor_user_id
    activation_count  = activation_count + 1
    stability_days    = base_stability × (1 + activation_count × 0.5)
  emit memory_activated event
  → {activation_count, new_stability_days, effective_strength}
```

**为什么分两步**：recall 只是"看到了"，activate 是"用到了"，防止检索噪声污染 stability。

### 7.4 GC（daily job）

```go
// GC 每天扫一次（pg_try_advisory_lock 单实例化）
// effective_strength < 0.1 且 is_immortal=false → 归档
UPDATE memories
SET status='archived', archived_at=clock_timestamp()
WHERE status='active'
  AND is_immortal=FALSE
  AND (
    CASE WHEN last_activated_at IS NULL
      THEN base_strength * exp(-extract(epoch from (now()-created_at))/86400 / stability_days)
      ELSE base_strength * exp(-extract(epoch from (now()-last_activated_at))/86400 / stability_days)
    END
  ) < 0.1;

-- 归档后可复活：pf_activate_memory 自动 status='active'
-- redacted 不可复活
```

### 7.5 Recall 排序算法

```
-- L-R3-4: 修正量纲，全部归一化到 [0,1]
normalized_strength = effective_strength(raw) / 5.0    -- 除以 base_strength_max
normalized_recency  = exp(-days_since_activation / 30) -- 30d 半衰，∈ (0,1]

final_score = semantic_similarity × (1 - recency_weight)
            + normalized_recency × recency_weight
            + normalized_strength × 0.1

-- 所有分量均 ∈ [0,1]，量纲一致
默认 recency_weight = 0.3
min_strength 默认 0.3（raw 值，M7 修正，与 GC 归档阈值对齐）
```

### 7.6 Memory-First 原则（所有 skill 遵守）

在以下场景执行任何操作**之前**，先做快速记忆检索：
- 用户提出新方案或需求时
- `/pf-work` 创建新 wi 前
- `/pf-spec` 开始前
- `prepare_context`（start_step）时

**展示规则**：

```
effective_strength >= 0.6  → 正常展示
0.3 ≤ strength < 0.6       → 展示但加"（较久未使用，请验证是否仍适用）"
strength < 0.3             → 默认跳过，除非 --include-stale

展示格式：
💡 相关历史经验（仅供参考，不代表必须遵循）：
  · [experience.pitfall] OAuth expiry clock_skew 问题
    激活 3 次，最近 14 天前，置信度 ★★★★
  · [rule.scheduling] v3.1 wi 必须等 v3.0 全部完成
    immortal，by alice
```

**原则**：memory 是参考，不是约束；展示后继续正常执行。

### 7.7 Wi Agent 增量提炼协议

```
每步完成后（Step Agent 返回后）：

1. Wi Agent（LLM）读 step artifact，判断：
   "这个发现对其他人或未来的任务有价值吗？"
   → 否（routine change）：跳过
   → 是（bug pattern / 踩坑 / 架构发现）：继续

2. 构造候选 memory（type, content, context_snippet）

3. pf_recall(query=候选内容, type=experience.*, top_k=3, similarity_threshold=0.75)
   → 无类似（< 0.65）：pf_remember(dedup_mode=suggest)
   → 高度相似（> 0.85）：pf_activate_memory(已有 id) 强化，不新建
   → 部分相关（0.65-0.85）：pf_remember，attrs.similar_to 记录已有 id

Wi Agent wrap 时（自动 retro，决策4）：
  批量 recall + remember + activate（结构化）
  pf_save_artifact(type="methodology.wrap_summary")
  -- retro 是 Wi Agent 内置行为，不依赖 phase.yaml stop_step

start_step（prepare_context）：
  pf_recall(query=wi.goal, type="experience.*|rule.*", top_k=5)
  → 注入 initial_context，传给 implement step
```

---

## 8. phase.yaml（Layer 2 工作流定义）

### 8.1 文件位置与发现顺序

```
1. run_attempts.phase_yaml_snapshot  ← claim 时 client 上传，优先级最高
   （解决 MCP server 无法读取本地文件的问题，see Opus F3.1）
2. 若 snapshot 为空，server 用 scenario 内置默认 graph
3. 无 graph → 自由执行模式（零回归）
```

client 在 claim 时读取 `<repo>/.polyforge/phase.yaml` 上传；每次 re-claim（resume/takeover）可更新 snapshot。

### 8.2 Schema

```yaml
schema_version: "phase.v1"   # H-R3-3: 使用 "phase.v1"，与 plugin.json version 区分
generated_by: ai | human

wi_types:
  # v1.22: wi_type 是团队完全自定义的标签，映射到具体执行步骤图
  # 没有固定枚举——团队可以根据项目场景增减（如加 security_fix / data_migration）
  # requires_human_session: true  → 需要人工开独立 session 主导（Session 2/3）
  # requires_human_session: false → Orchestrator 可直接 dispatch subagent（Session 1）
  # 这是 phase.yaml 的核心价值：用户可以在运行过程中调整 wi 的执行方式，
  # 只需改 phase.yaml 中对应 wi_type 的定义即可，无需改代码

  # ── 以下是默认示例，团队可按需修改 ────────────────
  feature:
    requires_human_session: true   # 需要人工主导 spec 和 plan 决策
    start_step:
      action: pf-spec
      description: "定义 spec：问题范围、Non-goals、设计决策、验收标准"
    steps:
      - id: plan
        action: pf-plan
        description: "制定实现计划，创建 child wi（可选）"
        requires: [start_step]
      - id: implement
        action: code_change
        requires: [plan]
      - id: review
        action: pf-review
        requires: [implement]
      - id: ship
        action: commit_and_pr
        requires: [review]

  simple_feature:
    requires_human_session: false  # 改动小且明确，无需 spec/plan，可自动执行
    start_step:
      action: prepare_context
      description: "分析代码结构，产出 initial_context"
    steps:
      - id: implement
        action: code_change
        requires: [start_step]
      - id: review
        action: pf-review
        requires: [implement]
      - id: ship
        action: commit_and_pr
        requires: [review]

  # ── bug 类 ──────────────────────────────────────────
  fix_bug:
    requires_human_session: false  # 小 bug，根因明确，可自动修复
    # 决策4: stop_step 删除。retro 是 Wi Agent 内置的 wrap 前行为，不在 phase.yaml 配置
    start_step:
      action: prepare_context
      description: "召回历史经验，分析相关代码，产出 initial_context"
    steps:
      - id: implement
        action: code_change
        description: "按 goal 实现修复"
        requires: [start_step]
      - id: review
        action: pf-review
        requires: [implement]
      - id: ship
        action: commit_and_pr
        requires: [review]

  critical_bug:
    requires_human_session: true   # 影响大/根因不明，需要人工主导讨论方案
    start_step:
      action: pf-spec
      description: "分析根因、影响范围、修复方案，与人类讨论确认"
    steps:
      - id: implement
        action: code_change
        requires: [start_step]
      - id: review
        action: pf-review
        requires: [implement]
      - id: ship
        action: commit_and_pr
        requires: [review]
```

### 8.3 Action 三级 fallback

```
1. coding scenario skills/ 目录找 SKILL.md
2. core plugin skills/ 目录找 SKILL.md
3. 用 description 字段作 NL 指令给 LLM
```

### 8.4 wi_type 解析规则

wi_type 直接作为 scenario_phase_configs 的键（v1.23，无需 client 上传 phase.yaml）：

```
wi.scenario → scenario_phase_configs[scenario].wi_types[wi_type]
           → 步骤图 + requires_human_session
```

fn_claim_work_item 处理（含 C-R9-6 / C-R9-10 / C-R9-12）：

```
-- C-R9-6: wi_type=NULL 时拒绝 claim，要求先补分类
if wi.wi_type IS NULL:
    → 400 WI_TYPE_MISMATCH("wi_type 未设置，请先 pf_update_work_item(wi_type=...)")

config = scenario_phase_configs[wi.scenario]

-- C-R9-10: wi_type 在创建后可能从 scenario config 中删除
if wi.wi_type NOT IN config.wi_types:
    → 409 WI_TYPE_MISMATCH {wi_type, available:[...]}

-- C-R9-12: 同 user_id re-claim（换机器），implicit force_takeover
if wi.status = 'running' AND wi.current_attempt.actor_user_id == caller.user_id:
    → 执行 force_takeover 逻辑，再继续 claim

resolved = config.wi_types[wi.wi_type].requires_human_session

if work_items.requires_human_session IS NULL:
    UPDATE work_items SET requires_human_session = resolved
    emit wi_classification_resolved event

elif work_items.requires_human_session != resolved:
    ROLLBACK → 409 REQUIRES_HUMAN_SESSION_MISMATCH

else:  // 一致，无操作
```

跨 workspace 漂移问题已从根本上解决（scenario_phase_configs 是 server 端 SoT）。

---

## 9. Wi Agent / Step Agent 交互协议

> **C-R9-7: Wi Agent 是角色，不是进程。** 有两种载体：
> - **(a) Orchestrator dispatched subagent**：处理 `items[]`（requires_human_session=false），被 Orchestrator 在 Session 1 dispatch
> - **(b) Alice 的 main CC session**：处理 `needs_human_session[]` 或单 wi `/pf-work`（Session 2/3），Alice 自己就是 Wi Agent
>
> 两种载体都走相同的 `fn_claim_work_item` → step loop → `pf_wrap` 路径。
> §21.7 `/pf-execute` 的 `requires_human_session=true` inline 分支，仅在载体 (b) 下有意义。

### 9.1 执行循环（Wi Agent）

```
claim wi（上传 phase_yaml_snapshot）
-- fn_claim_work_item 原子事务内（v1.23）：
--   1. INSERT run_attempts（含 phase_config_version = scenario_phase_configs[scenario].version）
--   2. 读 scenario_phase_configs[wi.scenario]，查 wi_types[wi.wi_type]
--   3. C-R7-9: wi_step_state 用 INSERT ... ON CONFLICT(work_item_id) DO UPDATE
--      re-claim（paused/resume）时 wi_step_state 行已存在，覆盖写以下字段：
--        wi_type, graph_source, current_step, current_step_status='idle',
--        step_started_at=NULL, current_step_attempt=NULL
--      但 version 字段 **不重置**（保持单调递增，避免 CAS 碰撞）
--   4. 无 phase_yaml_snapshot → 不插/更新 wi_step_state 行（自由执行模式）

1. 执行 start_step：
   dispatch Step Agent(start_step)
   读 pf_get_step 校验 start_step 已 completed（不信 Agent 文字返回）
   pf_activate_memory（对 recall 到的有用 memory）

2. 循环：
   step_info = pf_get_step(wi_id)
   if current_step == null → goto 3（所有步骤完成）

   dispatch Step Agent(step_info, previous_artifacts)

   校验（60s 超时）：
     pf_get_step 查看 step 是否推进（current_step_status 是否变化）
     如未推进 → emit step_agent_unresponsive，重试（最多 2 次）
     3 次失败 → pf_update_step(failed, escalated=true)

   如 step 完成 → Wi Agent（LLM）判断是否提炼经验（§7.7）

3. 自动 retro（决策4：不需要在 phase.yaml 定义 stop_step）：
   -- retro 是 Wi Agent 生命周期的内置行为，不是可选 step
   Wi Agent 直接执行（不派 Step Agent）：
     pf_recall（多条 query，查相关历史）
     批量 pf_remember / pf_activate_memory / pf_reinforce_memory
     pf_save_artifact(type="methodology.wrap_summary", content=<总结>)
   retro 部分失败 → emit retro_partial_failure，继续 wrap（不阻塞，知识残缺好过 wi 卡住）

4. pf_wrap（pf_complete_attempt(wrapped)）
```

### 9.2 Step Agent（scoped token）

**server 端强制**只能调以下工具（token scope 限制）：
- `pf_update_step`（in_progress/completed/failed）
- `pf_emit_event`
- `pf_recall`（只读）
- `pf_get_step`（只读）
- `pf_activate_memory`
- `pf_remember`（仅 experience.* 类型，visibility≤project）
- coding tools：`pf_commit`、`pf_push`、`pf_pr`

**禁止**：`pf_claim_work_item`、`pf_complete_attempt`、`pf_wrap`、`pf_create_work_item`

### 9.3 Artifact Summary 格式

```json
{
  "summary": "修了 auth.go:42 的空指针，新增 validateToken()",
  "files_changed": ["src/auth.go", "tests/auth_test.go"],
  "notes": "发现 refreshToken 有同款 bug，建议后续处理",
  "step_attempt_id": "sa_01xxx"
}
```

`artifact_summary` 限 4096 chars。完整 artifact 走 `pf_save_artifact` 存 memory，这里只存摘要。

### 9.4 Force Takeover 权限

```
H4-7 修正（§3.4 为准）：

writer / maintainer / admin：任何 attempt 且 expires_at < now()（idle > 30min）→ 允许
maintainer / admin：任何 attempt 任意时间 → 允许（必须 reason 字段）
其他 writer 且 expires_at 未到期 → 403 FORBIDDEN
```

### 9.5 Takeover 后接手未完成工作

新 agent 接手时，面对"上一个 agent 做了一半"的情况，遵循简单规则：

```
prepare_workspace（force_takeover 后必须用 mode=resume，不用 fresh）：
-- Carol WALL-B: force_takeover 语义是"继续旧工作"，mode=resume 从 task_branch 恢复
-- mode=fresh 仅用于全新 claim（从 origin/base 开始）
  git fetch origin
  git reset --hard origin/<task_branch>   ← 拿上一个 agent 最后 push 的状态
  （若 origin/<task_branch> 不存在 → reset to origin/<base>）

检查 task_branch 是否有提交：
  git log origin/<base>..origin/<task_branch> --oneline

─── 情况 A：没有提交 ───────────────────────────────────
  wi_step_state.current_step_status 已被 force_takeover 重置为 idle
  → 直接从 current_step 重新开始
  → pf_get_step 的 artifact_summary 告知"上次尝试做了什么"（即使代码没提交）

─── 情况 B：有提交 ──────────────────────────────────────
  pf_get_step → 读 current_step + previous artifact_summary
  review：git log + git diff origin/<base>..HEAD

  artifact_summary 与 diff 是否吻合？
    是 → 从 current_step 继续（代码已存在，只需继续下一步）
    否 → 告知用户差异，让用户决定：接受已提交代码 / 重新来过
```

**核心原则**：task_branch 上已 push 的 commit 是唯一跨 agent 传递代码的通道。未提交的本地变更在 agent 交接时视为丢失，新 agent 从已提交状态继续。这符合 git 作为协作 SoT 的基本语义。

---

## 9.4.5 wi_classification_rules（v1.23：存入 scenario_phase_configs）

classification_rules 作为 `scenario_phase_configs.content` 的一部分存储（不再是 aihub.yaml 静态配置），可通过 `pf_update_scenario_config` 动态修改。

**content 结构**：
```json
{
  "wi_types": {
    "fix_bug": {"requires_human_session": false, "start_step": {...}, "steps": [...]},
    "critical_bug": {"requires_human_session": true, ...}
  },
  "classification_rules": [
    {"name": "escalate_urgent_bugs",
     "match": {"wi_type_prefix": "bug", "priority": "urgent"},
     "set":   {"wi_type": "critical_bug"}},
    {"name": "escalate_features",
     "match": {"wi_type_prefix": "feature"},
     "set":   {"requires_human_session": true}}
  ]
}
```

`pf_create_work_item` handler 逻辑：
```
config = scenario_phase_configs[wi.scenario]
1. 跑 config.classification_rules（按顺序，第一条命中生效）
2. 命中 → 覆盖 client 传入的 wi_type / requires_human_session
3. 未命中 → 使用 client 传入值
4. client 也未传 → requires_human_session = NULL（进 unclassified[]）
5. 最终 wi_type 必须在 config.wi_types 中存在，否则 400 WI_TYPE_MISMATCH
```

---

## 9.5 Client 配置与状态持久化

### 9.5.1 目录结构

```
<workspace>/
  .polyforge.yaml              # workspace 配置（入库，见 §9.5.2）
  .polyforge/
    polyforge.md               # 使用指南，pf init 生成（入库）
    state/                     # per-wi AttemptCredential（不入库，加 .gitignore）
      wi_A3f7B9c2.json          # 命名用 wi_id（8b62），不用 slug（含#，shell 需 quote）
      wi_K8m2N4p1.json
  pf.42.01K5G3AB/              # worktrees（不入库）
    marketplace/

~/.polyforge/
  config.toml                  # 机器级配置（machine_id, api_key）
  last_workspace               # 最近 workspace 路径（pf doctor fallback）
```

`.gitignore` 必须包含：
```
.polyforge/state/
pf.*/
```

---

### 9.5.2 `.polyforge.yaml` Schema（workspace 级，入库）

```yaml
version: 1
scenario: coding         # coding | writing | data

aihub:
  url: http://10.146.0.16
  # api_key 不在这里（个人凭证，放 config.toml）

# 多 project workspace 的 default project
# 解析优先级见 §9.5.5
default_project: marketplace

projects:
  marketplace:
    repos:
      - name: marketplace
        url: git@github.com:GMISWE/GMI-marketplace.git
        github_owner_repo: GMISWE/GMI-marketplace
        description: "Internal plugin marketplace."
    description: "Internal plugin marketplace."

  aihub:
    repos:
      - name: aihub
        url: git@github.com:GMISWE/ieops-aihub.git
        github_owner_repo: GMISWE/ieops-aihub
        description: "polyforge v1 backend, PostgreSQL SoT."

  ieops:
    repos:
      - name: ieops-v2
        url: git@github.com:GMISWE/ieops-v2.git
        github_owner_repo: GMISWE/ieops-v2
        description: "K8s-native 全球 GPU 智能调度平台。"
      # ... 其他 repo

scenario_config:
  # {wi_slug} = project#seq，如 marketplace#42
  branch_pattern: "polyforge/{wi_slug}"

  dedup:
    high_threshold: 0.90     # 完全相同 → reject
    low_threshold:  0.65     # 部分相似 → 列候选
```

---

### 9.5.3 `~/.polyforge/config.toml` Schema（机器级，不入库）

```toml
# pf init 首次运行自动生成，不要手动修改 machine_id
machine_id = "0a622ba3-59ca-4d35-89c8-24ea918339c3"

[auth]
# 二选一：直接写 key，或引用 env var
# api_key = "pf_k1_xxxxxxxxxxxxxxxx"
api_key_env = "POLYFORGE_API_KEY"

# 可选：覆盖 .polyforge.yaml 里的 aihub.url（适用于本机特殊网络）
# [server]
# url = "http://10.146.0.16"
```

`machine_id` 规则：
- 首次 `pf init` 时用 `crypto/rand` 生成 UUID v4，写入并固定
- 跨 boot 稳定（文件持久化）
- 容器环境：如有 `POLYFORGE_MACHINE_ID` 环境变量则优先使用（保证容器内稳定性）

---

### 9.5.4 `.polyforge/state/<wi_id>.json`（per-wi AttemptCredential）

命名用 `wi_id`（如 `wi_A3f7B9c2.json`），**不用 slug**。
原因：slug 含 `#`（`marketplace#42`），shell 中需 quote，`ls/rm/glob` 容易翻车（Alice-1 WALL-1.2）。
state file 内的 `slug` 字段仍保留（展示用），文件名本身用纯 base62 ID。

```json
{
  "wi_id":          "wi_A3f7B9c2",
  "slug":           "marketplace#42",
  "project":        "marketplace",
  "attempt_id":     "ra_8d2E4F1a",
  "claim_epoch":    3,
  "session_secret": "a1b2c3d4...（64 hex，明文，文件权限 0600）",
  "claimed_at":     "2026-05-21T10:00:00Z",
  "worktrees": {
    "marketplace": "/root/code/aicoding/gmi-ws-v3/pf.42.01K5G3AB/marketplace"
  }
}
```

**只存机器私有内容**：
- `session_secret`：aihub 只存 sha256 hash，明文仅本机持有
- `worktrees`：绝对路径，跨机器无意义

**不存**：step 执行上下文、artifacts、completion 状态——这些在 aihub `wi_step_completions` 里，`pf_get_step` 直接返回，无需本地缓存。

**读写时机（C6-2: 先写 state file 再调 aihub，解决 retry secret 不一致）**：
- `pf_claim_work_item` 调用前（MCP server）：
  1. 生成 `(idempotency_key, session_secret)` 原子对（crypto/rand），keyed by idem_key 内存缓存
  2. **先写 state file（partial state）**：`{idem_key, session_secret, wi_id, claimed=false}`
     mode 0600，O_TRUNC|O_CREATE|O_WRONLY
  3. 调 aihub `/claim`，传 idem_key + session_secret
  4. aihub 成功 → 更新 state file 为完整凭证（attempt_id, claim_epoch 等），`claimed=true`
  5. aihub 失败或网络断 → retry 时：从内存缓存取同一 `(idem_key, secret)`，不重新生成
     → aihub 命中 idempotency cache，DB 里 hash(secret) 仍一致 ✓
  session_secret 永远不返回给 skill/LLM（决策A）
- `pf_complete_attempt(wrapped/failed)` → 删除 state file（terminal 状态，凭证不再需要）
- `pf_complete_attempt(paused)` → **保留** state file（C5-3: paused 是非 terminal，resume 时需凭证）
  注意：保留旧凭证 + 旧 attempt_id；resume 时会被新 claim 覆盖写入新凭证
- `pf_force_takeover` 后新 agent re-claim → 覆盖写入新 attempt 凭证
- credentials 校验返回 CONFLICT_EPOCH_MISMATCH / ATTEMPT_MISMATCH → MCP server
  自动删除 state file 并返回 STALE_LOCAL_CREDENTIAL，提示用户 re-claim
- `pf doctor` 第 4 项：扫描 state/ 目录，对比 server 端 wi 状态，标红孤儿文件

**多 wi 并发**：每个 wi 独立文件，互不干扰。

---

### 9.5.5 Project 解析规则

skill 内所有 `<current project>` 按以下优先级解析：

```
1. 显式参数 --project=marketplace
2. CWD 在 .repo/<repo_name>/ 内
   → 反查 .polyforge.yaml projects，找哪个 project 包含此 repo
3. CWD 在 pf.<seq>.<ulid8>/<repo_name>/ 内
   → 读 .polyforge/state/ 找匹配 worktree 的 wi → project
4. .polyforge.yaml default_project
5. 多 project 且无默认 → MCP server 返回 400 PROJECT_AMBIGUOUS
   details: {candidates:[...]}；skill 捕获后提示用户显式传 --project
   （MCP server 无交互式 UI，不能"弹出选择"，靠 skill 向用户询问）
```

### 9.5.6 Crash-Recovery Protocol（Alice-3 补充）

当 Wi Agent session OOM kill / 网络断等崩溃后，state file 保留（C5-3）。
新 session 起手扫描发现 running wi → 尝试 resume。

```
pf_claim_work_item(mode=resume) 时 server 返回 ClaimResponse + step_recovery_hint：
  "clean"                ← 正常 resume，step 是 idle
  "crashed_in_progress"  ← 上次崩溃时 step 是 in_progress 且 attempt 心跳已停
  "active_in_progress_conflict"  ← step 仍 in_progress 且 attempt 心跳刚停（< 15s），可能双 agent

判定逻辑（server 端）：
  if attempt.last_active_at < now() - 15s AND wi_step_state.current_step_status='in_progress':
    step_recovery_hint = "crashed_in_progress"
  else if attempt.last_active_at >= now() - 15s AND current_step_status='in_progress':
    step_recovery_hint = "active_in_progress_conflict"  → 409 CONFLICT_DUAL_WI_AGENT
  else:
    step_recovery_hint = "clean"

Wi Agent 收到 "crashed_in_progress" 时的处理：
  1. pf_update_step(status=failed, step_attempt_id=<sa_old>,
                    error_type="agent_crashed", escalated=false)
     -- agent_crashed 不计入 M16 的 3 次重试阈值
  2. 按 §9.5 接手规则判断 worktree 是否有 commits：
     情况 A（无 commits）→ 从 current_step 重做
     情况 B（有 commits）→ 展示三段式提示让用户决定：
       "崩溃前的 code_change 在 origin/<task_branch> 上有 N 个 commit；
        本地 worktree 可能有未提交修改。
        (1) 接受已提交代码并跳到下一步
        (2) 保留 dirty worktree 重做 current_step
        (3) 整体放弃 → /pf-stop fail"
     -- Alice-2 Wall#10：prepare_workspace（mode=resume）默认会
     --   git reset --hard origin/<task_branch>，会清掉 dirty worktree
     -- 因此 crash recovery 路径必须先展示提示，**用户确认(2)后才跳过** reset
     -- 实现：ClaimResponse.step_recovery_hint=crashed_in_progress 时
     --   prepare_workspace 跳过 git reset；仅在用户选 (1)/(3) 后再执行
  3. Wi Agent 重新 dispatch Step Agent 拿新 step_attempt_id
```

新增错误码 §17：`CONFLICT_DUAL_WI_AGENT`（409，两个 Wi Agent 同时活着）

---

### 9.5.7 CI / Ephemeral Runtime（CI-Bot 补充）

CI 环境（GitHub Actions 容器）特殊处理：

```toml
# ~/.polyforge/config.toml 在 CI 里不生成
# 全部从环境变量读：
# POLYFORGE_API_KEY=xxx           ← 直接 key（不走 api_key_env 间接引用）
# POLYFORGE_MACHINE_ID=ci-bot-${GITHUB_RUN_ID}  ← 每 job 唯一，稳定
# POLYFORGE_WORKSPACE_ROOT=$GITHUB_WORKSPACE    ← MCP server workspace
```

`pf init` 在 CI 里行为（`CI=true` env 自动检测）：
- **不生成 config.toml**（容器无持久 home）
- **不跑 `pf init --apply`**（repo 已通过 `actions/checkout` clone）
- `pf doctor` 跳过 config.toml 存在性检查

**CI job 必须显式 cleanup（trap EXIT）**：

```bash
trap 'on_exit $?' EXIT
on_exit() {
  rc=$1
  if [[ -n "${PF_ATTEMPT_ID:-}" && "$rc" != "0" ]]; then
    pf complete-attempt --status=failed --force-terminate-step \
                        --reason="CI exit rc=$rc, container terminated"
  fi
}
```

不执行 cleanup → 孤儿 attempt 期间 lock 持续占用，直到 GC orphan_lock_cleanup sweep 清除。

**§27 Known Limitations 补充**：v1 CI 整条 path（config.toml 跳过、trap cleanup、短 TTL attempt）是 workaround，未来考虑 server 端 `runtime=ephemeral` + 2h 短 TTL attempt 设计。

---

## 10. Layer 3 调度

### 10.0 轮次模型

Layer 3 以**轮次（round）**为单位运行（决策2：v1 严格 round barrier）：

```
Round N（人工触发，v1 不做自动触发）：
  1. pf_get_ready_queue(project, non_conflicting=true, max=5)
     → {items, running, stalled, paused, needs_human_session}

     -- items[]: requires_human_session=false → 直接 dispatch subagent（Session 1）
     -- needs_human_session[]: requires_human_session=true → 展示给 Alice，等她开 Session 2/3

  2. 展示 needs_human_session[] 和 unclassified[] 给用户（不 dispatch）：
     "以下 wi 需要你主导（Session 2/3）：
       marketplace#45  feature       'add SSO login'          → /pf-work marketplace#45
       marketplace#48  critical_bug  'payment data corruption'→ /pf-work marketplace#48
     以下 wi 需要补分类：
       marketplace#51  NULL          'something vague'        → /pf-work marketplace#51 重建"
     -- H-R8-14 / Alice-Session-1 W6: 统一使用 /pf-work（CC skill），不混用 pf claim（CLI）
     -- pf claim 仅用于 §12 CI/machine user 文档

  3. fan-out（仅 items[]，C4-4: 防双派）：
     -- v1: 单 LLM turn 内多 tool call 并行派发（Claude Code 原生支持）
     -- Orchestrator 在同一 assistant message 内 dispatch 所有 subagent
     -- 同一轮内 Ready Queue 只读一次，结果即派发集合，不重新读

  4. 严格 barrier：等待本轮所有 subagent 返回（全部 terminal 或 paused）
     -- paused/stalled 的 wi 由人工处理，不阻塞下一轮开始
     -- v2 考虑滑动窗口模型（max_parallel 并发，有完成即补入新 wi）

Round N+1（人工再次触发）：
  本轮 wi 完成后，blocked_by 解除的 wi 自动进入 Ready Queue
  下一轮 orchestrator 读到新的 ready wi，继续派发
```

关键点：
- Orchestrator **只处理当前时刻** Ready Queue 里的 wi，不做跨轮预判
- 每轮 Ready Queue **只读一次**，防止同一 wi 被多次派发（C4-4）
- `/pf-plan` 新建的 child wi 不进当前轮，等下一轮自然被 pick up
- 依赖关系（blocked_by）决定跨轮执行顺序，orchestrator 无需理解依赖图
- **v1 必须人工触发每轮**；自动触发推 v2
- Orchestrator 不 claim 任何 wi（§27 KL1）；stalled wi 仅展示，不自动处理

**Orch WALL-6 — 空/全失败 round 行为**：
- `items[] = []`（所有 wi 都在 running/stalled/paused）→ Orchestrator 渲染 LCRS，不 dispatch 任何 subagent，round 立即结束（< 1s），提示用户手动处理后再触发下一轮
- `items[]` 非空但所有 subagent 返回 `claim_failed` → round 仍算完成；Orchestrator 不重读 Ready Queue（C4-4），展示"所有 wi 已被其他 agent 抢走，下一轮再试"；用户须再次触发

**Orch WALL-8 — Layer 3 入口**：
v1 没有独立 `pf-orchestrate` skill（§21 只列单 wi 技能）。Orchestrator 是**一个 prompt pattern**：
- 用户在 main CC session 里手动触发（不是 subagent）
- 调 `pf_get_ready_queue` → 按 §10.0 步骤 fan-out dispatch → 按 §10.0.x 校验
- v2 考虑封装为 `/pf-orchestrate-round` skill

### 10.0.x Wi Agent subagent contract（GAP-2 补充）

Orchestrator dispatch Wi Agent 时 prompt 包含：
```
work_item_id, slug, wi_type, priority, goal, project, workspace_root
```

Wi Agent 返回给 Orchestrator 的 JSON（最后一段输出）：
```json
{
  "wi_id":         "wi_A3f7B9c2",
  "slug":          "marketplace#42",
  "outcome":       "wrapped" | "paused" | "failed" | "claim_failed" | "stalled",
  "attempt_id":    "ra_8d2E4F1a" | null,
  "error_code":    "CONFLICT_WI_ALREADY_CLAIMED" | null,
  "error_details": {
    "current_attempt": {
      "actor_display": "Bob (machine)",
      "expires_at": "2026-05-21T11:00:00Z",
      "claim_epoch": 3
    }
  },
  "stall_context": {
    "stall_reason": "OAuth provider down",
    "stalled_at_step": {"step_id":"code_change","current":2,"total":4},
    "error_type": "external_dependency"
  },
  "duration_ms":   12340
}
```

**Orch-1 修正：outcome 不可信，必须从 server ground truth 校验**：
```
Orchestrator 收到 subagent 结果后：
  1. 解析 JSON（regex 抽取，处理 LLM 可能的 ```json 包装）；找不到 → outcome=unknown
  2. pf_list_work_items(ids=[wi_id]) 取 server 端 status 作为 ground truth
  3. server status 决定本轮统计，LLM 自报 outcome 仅作 hint
  4. 若 server status=running 但 subagent 已退出 → 异常，记录并展示给用户
```

Orchestrator 根据 outcome 统计本轮结果（展示给用户）：
- `wrapped`：计入成功，折叠展示
- `claim_failed` + `error_details.current_attempt`：展示"#X 被 <actor_display> 抢走，expires <时间>"
- `stalled` + `stall_context`：展示完整 stall 信息，提示人工处理
- `paused`：展示，提示下轮 resume

**paused wi 在 LCRS 的可见性（Orch-1 补充）**：
`GET /v1/work_items/ready` 新增第四段 `paused[]`，防止 paused wi 从 LCRS 消失：
```json
{
  "items":   [...],   // ready，可立即执行
  "running": [...],
  "stalled": [...],   // status=blocked + wi_stalled event
  "paused":  [{id, slug, paused_since, last_actor_display, pause_reason}]
}
```

**Round 软上限（Orch-2 补充：wall clock 机制明确）**：
Orchestrator dispatch prompt 必须包含 `round_deadline: "<ISO UTC>"`（= round 触发时间 + 4h）。
Wi Agent SKILL.md 约束：每完成一个 step 后 `Bash: date -u +%FT%TZ` 校时，
剩余 < 15min → 立即 `pf_complete_attempt(paused)`，返回 `outcome="paused"`。
不依赖 LLM "感觉"时间（LLM 内置时钟不可靠）。

```
示例：feature wi 拆分为 A→B→C（串行）

Round 1：Ready = [A]        → 派 agent 执行 A
Round 2：A 完成 → Ready = [B] → 派 agent 执行 B
Round 3：B 完成 → Ready = [C] → 派 agent 执行 C
```

### 10.1 Ready Queue 计算

```sql
SELECT wi.id, wi.slug, wi.wi_type, wi.priority, wi.goal
FROM work_items wi
-- items[]：可自动执行（requires_human_session = false，非 NULL）
WHERE wi.project = $project
  AND wi.status = 'queued'
  AND wi.requires_human_session = false   -- NULL 的 wi 走 unclassified[]，不在这里
  AND NOT EXISTS (
    SELECT 1 FROM wi_dependencies dep
    JOIN work_items blocker ON dep.blocking_wi_id = blocker.id
    WHERE dep.blocked_wi_id = wi.id
      AND dep.kind = 'blocks'
      AND blocker.status NOT IN ('wrapped','cancelled','failed')
  )
ORDER BY
  CASE wi.priority WHEN 'urgent' THEN 4 WHEN 'high' THEN 3
                   WHEN 'normal' THEN 2 WHEN 'low' THEN 1 END DESC,
  CASE WHEN wi.wi_type LIKE '%bug%' AND wi.priority='urgent' THEN 1000 ELSE 0 END DESC,
  wi.created_at ASC
LIMIT $max;
```

`non_conflicting=true`：server 端预跑 predict_conflicts，返回互不冲突的 N 个 wi，避免 fan-out 后 claim 失败浪费 token。

### 10.2 Stalled Queue

**定义（C4-2/Carol WALL-D 修正）**：`status=blocked` AND EXISTS(`wi_stalled` event)。

来源：`step_failed(escalated=true)` → server 同事务内 INSERT `wi_stalled` event + UPDATE wi.status='blocked'。

注意区分：
- `step_agent_unresponsive`：心跳超时，wi 仍是 `running`，**不进** stalled queue；由 Wi Agent 重试处理
- stalled：wi 已变 `blocked`，当前轮次无法继续，需人工介入

`stalled[]` 字段来源：
- `stall_reason`：最近 `wi_stalled` event 的 payload.stall_reason
- `stalled_since`：该 event 的 created_at
- `last_actor_display`：该 event 的 actor_display

处理职责：**Orchestrator 仅展示，不自动 takeover**（IR1 + §27 KL1）。人工决定：(a) `/pf-work #X --force` 接管重做；(b) 创建 unblock wi；(c) 管理员解锁。

`GET /v1/work_items/ready` 的 `stalled` 字段返回；CLI `pf stalled`。

### 10.3 LCRS 视图

客户端一次拉取 snapshot（单事务，wi + dep 一致），Kahn 拓扑排序渲染：

```
═══ Running (2) ═══
  marketplace#38  bug  alice   "fix OAuth 401"      expires 28min
  marketplace#41  feat claude  "kubeconfig switch"  expires 25min

═══ Ready Roots（auto，本波可并行）═══
▶ marketplace#42  fix_bug    urgent  "fix payment timeout"
   └─ marketplace#43  chore   normal  "regression test"  waits:[#42]

═══ Needs Your Attention（requires_human_session=true）═══
  marketplace#45  feature        high   "add SSO login"           queued 2d
    → /pf-work marketplace#45
  marketplace#48  critical_bug  urgent  "payment data corruption" queued 1d  ⚠ urgent
    → /pf-work marketplace#48

═══ Unclassified（需补分类）═══
  marketplace#51  NULL           normal  "something"  queued 0d
    → /pf-work marketplace#51  或  pf_update_work_item(wi_type=..., reclassify_reason=...)

═══ Stalled（需处理）═══
  marketplace#39  feat  high  paused 2h, step_blocked: OAuth API down
    → /pf-work marketplace#39 --force  或  create unblock wi
```

---

## 11. F3 Dedup（wi 创建前重复检查）

嵌入 `pf_create_work_item` server handler（避免 TOCTOU，不暴露独立 endpoint）：

```go
// M9: 候选集不做时间硬截断（避免漏掉 paused 多个月后复活的重复 wi）
// 仅过滤：同 project + 活跃状态 + 标签/资源预过滤
candidates := db.Query(`
    SELECT id, slug, goal, labels, declared_resources
    FROM work_items
    WHERE project = $1
      AND status IN ('queued','running','paused','blocked')  -- 活跃状态
      AND (labels && $2::text[] OR declared_resources @> $3::jsonb)
    LIMIT 50
`, req.Project, req.Labels, req.DeclaredResources)

for _, c := range candidates {
    sim      := jaccardNGram(req.Goal, c.Goal, 3)        // weight 0.6
    labelSim := setOverlap(req.Labels, c.Labels)          // weight 0.2
    resSim   := resourceOverlap(req.Resources, c.Resources) // weight 0.2
    score    := 0.6*sim + 0.2*labelSim + 0.2*resSim

    if score >= cfg.Dedup.HighThreshold { // default 0.90
        return HTTP 409 DUPLICATE
    }
    if score >= cfg.Dedup.LowThreshold {  // default 0.65
        partials = append(partials, c)
    }
}
if len(partials) > 0 {
    return HTTP 409 CANDIDATES
}
```

阈值在 `.polyforge.yaml` 可配置，上线前用真实 wi 数据 calibrate。

**C9 — Idempotency-Key + dedup 竞态**：
- `force_create=true` 时 client **必须**生成新的 `Idempotency-Key`（不能复用前一次 409 的 key）
- 原因：server 缓存 24h，旧 key → 回放 409，`force_create` 永远无效
- `pf_create_work_item` 在文档和 skill 中明确注明此规则

---

## 12. CLI 命令（pf）

```
pf init              # scaffold .polyforge.yaml + ~/.polyforge/config.toml
                     # + 从 scenario_phase_configs GET 下载本地 .polyforge/phase.yaml
                     #   C-R9-1: server 始终有默认记录（migration seed），无"首次写入"分支
pf init --apply      # clone repos + 填充 CLAUDE.md managed 区块
                     # 前置条件（Dave WALL-D3）：
                     #   1. SSH key 已配置并加入 ssh-agent
                     #   2. GitHub org SSH key 已授权（SSO org 需额外 authorize）
                     #   3. gh CLI 已登录（pf init --apply 调 gh api 生成 repo description）
                     # 失败处理：clone 失败的 repo 跳过+记录，不 abort 整次 init；
                     #           可重复运行（idempotent，已 clone 的 repo 跳过）

pf doctor            # 5 项健康检查（见 §12.1）
pf doctor --fix      # 自动修复

pf ready [--view=lcrs] [--max=N] [--non-conflicting]
pf stalled
pf version

# CI-Bot WALL-CI-7：CI trap EXIT 使用的关键命令，必须定义
pf complete-attempt --status=<wrapped|failed|paused>
                    [--wi-id=<wi_id>]            # 省略时自动扫 state/ 目录
                    [--force-terminate-step]
                    [--reason=<text>]             # 追加为 note event，不是 API 字段
  # 状态文件发现规则（POLYFORGE_WORKSPACE_ROOT 优先于 os.Getwd()）：
  #   1. $POLYFORGE_WORKSPACE_ROOT/.polyforge/state/*.json（单文件则自动选，多文件需 --wi-id）
  #   2. <cwd>/.polyforge/state/*.json（向上搜索）
  # session_secret / attempt_id / claim_epoch 全部从 state file 读取（不入 prompt）
  # --reason 内部转为 pf_emit_event(type=note, pinned=false)，独立于 complete-attempt

# 机器用户完整 CLI surface（machine user 无 Claude Code + MCP，需直接调 CLI）
pf claim <id_or_slug>                   # → pf_claim_work_item
pf get-step [--wi-id=<id>]             # → pf_get_step
pf update-step --status=<in_progress|completed|failed> --step-id=<id>
               [--step-attempt-id=<sa>] [--artifact-summary=<json>]
               [--escalated] [--error-type=<type>] [--expected-version=<n>]
pf commit [--message=<msg>]            # → pf_commit
pf push                                # → pf_push
pf pr --title=<t> --body=<b>           # → pf_pr
pf wrap [--wi-id=<id>]                 # → pf_wrap（coding scenario）
```

### 12.1 doctor 5 项检查

```
1. workspace  从任意子目录能找到 .polyforge.yaml（向上搜索）
2. config     ~/.polyforge/config.toml 存在，aihub url 可达（pf_whoami ping）
3. repos      所有 .repo/<name>/ 存在且 remote 匹配 .polyforge.yaml
4. worktrees  pf.<xxx>/ 列表 vs server wi 列表，标红 orphan
5. version    GET /v1/version，比对 min_client_version 与本地 binary
```

### 12.2 CLAUDE.md 两阶段生成

`pf init` 生成带版本标记的模板，`pf init --apply` 填充：
- repos 表（从 README + gh api 生成 description）
- scheduling_rules（`pf_recall(type=rule.scheduling, visibility=team)`）
- 版本号（`<!-- polyforge:managed:version="1.0" -->`）

`pf doctor` 检查 CLAUDE.md 版本是否落后，落后提示 `pf init --apply`。

---

## 13. ID 规范

### 13.1 实体 ID（8 位 base62）

所有实体 PK 格式 `<prefix>_<8b62>`，见 §2.2.1。

### 13.2 wi 的人类可读 slug

wi 同时拥有：
- **内部 ID**：`wi_A3f7B9c2`（FK 引用、API 返回、日志）
- **人类可读 slug**：`marketplace#42`（CLI 显示、skill 输出、用户输入）

```sql
-- slug = project#seq，GENERATED ALWAYS AS，不可手动修改
-- seq 由 PG SEQUENCE nextval() 原子分配，不保证连续，只保证单调
```

用户输入接受：
```
wi_A3f7B9c2    → 内部 ID（精确）
marketplace#42 → slug（跨 project 唯一）
#42            → seq（client 补全当前 project context）
```

### 13.3 其他实体的显示规则

skill 输出和 CLI 中，非 wi 实体用短格式显示：
```
attempt_id:  ra_8d2E4F1a  （完整 10 字符，已经够短）
memory_id:   mem_Z3x9Y2v1  （完整 12 字符）
event_id:    evt_Q7b2R5p8  （完整 12 字符）
```

不需要额外缩短。8 位 base62 本身就是"够短"的 ID。

---

## 14. Worktree 路径

```
格式：pf.<project_seq>.<ulid8>/   
-- L-R3-5: ulid8 = ULID 的后 8 字符（[0-9A-Z]{8}），作为随机后缀
-- 例：  pf.42.01K5G3AB/marketplace/
-- project_seq 保证 project 内唯一；ulid8 防止极罕见跨 project 同 seq 碰撞
-- 完整格式保证全局唯一，无碰撞风险
```

---

## 15. Ownership-Only 模型

```
claim → expires_at = clock_timestamp() + interval '30 minutes'
        last_active_at = clock_timestamp()

-- C-R3-7: mutating tool call 同时刷新两个字段
mutating tool call →
  last_active_at = clock_timestamp()（活跃记录用）
-- v1.21: expires_at 已删除；claim 是永久持有，无 lease renewal

stale_running 提醒（ReadyQueue 新增段）：
  running → 永久持有直到显式操作（pause/wrap/fail/force_takeover）
  updated_at > 24h 未更新 → stale_running 提醒（不强制释放）
  -- GetReadyQueue 返回 stale_running[]：running wi 且 updated_at < now() - 24h
  -- 仅做可见性提醒；释放需调用方显式操作（pf_complete_attempt 或 pf_force_takeover）

takeover 条件（v1.21）：
  同 user_id（自我接管）→ 总是允许
  maintainer / admin  → 总是允许（必须带 reason）
  其他 writer          → 403，无论多久没动

GC job（60s tick，pg_try_advisory_lock 单实例保证只有一个实例跑）：
  1. 孤儿锁清理（owner_attempt_id 指向 non-running attempt）
  2. 过期 memory 归档（effective_strength < 0.1）
  3. methodology.* memory expires_at 过期归档
  4. event payload 超 64KB 截断
  5. unblock_dependent_wi（H5-7 + C6-1 修正）
     -- C6-1: 并发 wrap race 修复 —— 加 SELECT FOR UPDATE 防止两个 blocker 同时 wrap 互相看不到
     -- fn_complete_attempt(wrapped/failed/cancelled) 事务内（SERIALIZABLE 或 SELECT FOR UPDATE）：
     --   -- 先锁定候选 blocked wi，防止并发 wrap 漏解锁
     --   SELECT id FROM work_items
     --   WHERE id IN (
     --     SELECT dep.blocked_wi_id FROM wi_dependencies dep
     --     WHERE dep.blocking_wi_id = $wi_id AND dep.kind = 'blocks'
     --   ) AND status = 'blocked'
     --   ORDER BY id  ← C-R7-2: 全局确定的 lock 顺序，防止两个并发 wrap 互相等待死锁
     --   FOR UPDATE;  ← 锁住候选行，后到的 wrap 事务等待前者 commit 后重读
     --
     --   UPDATE work_items SET status='queued', updated_at=clock_timestamp()
     --   WHERE id IN (上面 SELECT 的 id 集合)
     --     AND NOT EXISTS (
     --       SELECT 1 FROM wi_dependencies dep2
     --       JOIN work_items blocker ON dep2.blocking_wi_id = blocker.id
     --       WHERE dep2.blocked_wi_id = work_items.id
     --         AND dep2.kind = 'blocks'
     --         AND dep2.blocking_wi_id != $wi_id  -- 排除当前正在 wrap 的 blocker
     --         AND blocker.status NOT IN ('wrapped','cancelled','failed')
     --     );
     --
     --   -- 同时 emit wi_unblocked events
     --   INSERT INTO agent_events (id, work_item_id, event_type, payload, project, created_at)
     --   SELECT NewID('evt'), id, 'wi_unblocked',
     --          jsonb_build_object('unblocked_by_wi', $wi_id), project, clock_timestamp()
     --   FROM (上面 UPDATE RETURNING id, project);
     -- GC 60s tick 仅作兜底
  6. H13 partition creator（daily tick 独立）：提前 60 天创建 agent_events 月分区
  7. needs_human_session aging（H-R8-5 补充，daily tick）：
     -- requires_human_session=true AND status='queued' AND created_at < now() - 7d
     -- → emit wi_needs_attention event（admin/maintainer 审阅）
     -- → 不自动降级（降级需人工决策），仅提升可见性
     -- critical_bug + priority=urgent：阈值缩短为 1d
     SELECT id, slug, kind, wi_type, priority, created_at
     FROM work_items
     WHERE requires_human_session = true AND status = 'queued'
       AND created_at < now() - CASE priority
                                  WHEN 'urgent' THEN interval '1 day'
                                  ELSE interval '7 days'
                                END;
  8. unclassified wi 告警（daily tick）：
     requires_human_session IS NULL AND status='queued' AND created_at < now() - 1d
     → emit wi_classification_missing event，提示 reporter 补分类
```

---

## 16. EmbeddingProvider

```go
type EmbeddingProvider interface {
    Embed(ctx context.Context, text string) ([]float32, error)
    EmbedBatch(ctx context.Context, texts []string) ([][]float32, error)
    Dims() int
    ModelID() string          // 'text-embedding-3-small'
    Ping(ctx context.Context) error
}
```

`memories` 和 `memory_embeddings` 均存 `model` 字段。recall 时 `WHERE model=$current_model` 过滤，切换 provider 不污染旧 memory。

---

## 17. 错误码枚举

### 18.1 HTTP 响应信封

所有错误响应格式统一：

```json
{
  "code": "CONFLICT_DUPLICATE",
  "message": "work item marketplace#38 has 94% similarity",
  "details": { ... }   // 按 code 类型附加结构化数据
}
```

### 18.2 错误码完整定义

```
HTTP 400
  BAD_REQUEST              请求格式错误（字段缺失/类型错误）
  GOAL_MULTILINE             goal 包含换行符
  GOAL_CHANGE_NOT_ALLOWED  goal 更新时 wi 处于 running 状态（需先 pause）
  INVALID_PHASE_YAML       phase_yaml_snapshot schema 无效
  INVALID_STEP_TRANSITION  step 状态转移不合法
  PROJECT_AMBIGUOUS        project 无法自动推断，需显式传参
                           details: {candidates:["marketplace","aihub",...]}

HTTP 401
  UNAUTHORIZED             Bearer token 无效或已撤销
  STALE_LOCAL_CREDENTIAL   MCP server 侧本地凭证已失效（attempt 被 superseded/taken over）
                           → MCP server 自动删除 state file，提示 re-claim

HTTP 403
  FORBIDDEN                权限不足
  ATTEMPT_MISMATCH         attempt_id 不属于当前 wi 的 current_attempt

HTTP 404
  NOT_FOUND                wi / memory / attempt 不存在

HTTP 409
  CONFLICT_EPOCH_MISMATCH         claim_epoch 与 current_attempt_epoch 不符
  CONFLICT_STEP_IN_PROGRESS       complete_attempt 时 step 仍 in_progress
  CONFLICT_STEP_ATTEMPT_MISMATCH  pf_update_step 的 step_attempt_id 不匹配
  CONFLICT_CAS_FAILED             resources_version / step version CAS 校验失败
                                  details: {current_version: N}
  CONFLICT_WI_ALREADY_CLAIMED     wi 已被其他 attempt claim（status=running）
                                  details: {current_attempt:{id,actor_display,expires_at,claim_epoch,owner_user_type}}
  CONFLICT_HARD_BLOCK             claim 时资源冲突无法绕过
                                  details: {predictions:[...]}
  CONFLICT_DUPLICATE              wi dedup 完全匹配
                                  details: {existing:{id,slug,goal,status}}
  CONFLICT_CANDIDATES             wi dedup 部分匹配
                                  details: {candidates:[{id,slug,goal,similarity}]}
  CONFLICT_SIMILAR_MEMORY         memory dedup（dedup_mode=strict）
                                  details: {existing:{id,type,content,similarity}}
  CONFLICT_DEPENDENCY_CYCLE       wi_dependencies 写入产生有向环
                                  details: {cycle_path:[wi_id,...]}
  CONFLICT_LOCK_TAKEN             lock 被其他 attempt 持有
                                  details: {conflict_with:{attempt_id,actor_display,work_item_slug}}
  CONFLICT_DUAL_WI_AGENT          crash recovery：step 仍 in_progress 且 attempt 心跳 < 15s（§9.5.6）
                                  details: {last_active_at, step_id, step_attempt_id}
  REQUIRES_HUMAN_SESSION_MISMATCH claim 时 wi.requires_human_session 与 phase.yaml 定义不符（§8.4）
                                  details: {db_value, phase_yaml_value, wi_type}
  WI_TYPE_MISMATCH                wi_type 在 phase.yaml 里不存在（§8.4 / §22 校验）
                                  details: {wi_type, available_wi_types:[...]}
  WI_RECLASSIFY_FORBIDDEN         PATCH wi_type 时权限不足或 wi.status 不允许（需 queued/paused）

HTTP 412
  PRECONDITION_FAILED      CAS version 前置条件失败（用于 step state）
                           details: {current_version: N, current_state: "..."}

HTTP 413
  PAYLOAD_TOO_LARGE        event payload 超 64KB / artifact_summary 超 4096 chars

HTTP 500
  INTERNAL_ERROR           服务端未预期错误
HTTP 503
  SERVICE_UNAVAILABLE      DB 不可达 / 迁移中
  AIHUB_UNAVAILABLE        MCP server 侧：3 次 retry 全败（§30.1），body: {retry_after_seconds:30}
```

---

## 18. 完整请求/响应 Schema

### 19.1 WorkItem 对象

```typescript
interface WorkItem {
  id: string               // wi_<ulid>
  seq: number
  slug: string             // project#seq
  project: string
  scenario: "coding" | "writing" | "data"
  goal: string
  source: string
  wi_type: string | null             // 映射到 phase.yaml 的执行类型，团队自定义（如 fix_bug/critical_bug）
  requires_human_session: boolean    // 决定进 items[]（auto）还是 needs_human_session[]
  priority: "low"|"normal"|"high"|"urgent"
  milestone: string | null
  labels: string[]
  status: "queued"|"running"|"paused"|"blocked"|"wrapped"|"failed"|"cancelled"
  declared_resources: DeclaredResource[]
  resources_version: number
  external_share_type: "jira"|"github"|"linear" | null
  external_share_key: string | null
  reporter_user_id: string
  reporter_display: string          // 快照，创建时写入，user 改名后不影响历史
  current_attempt_id: string | null
  current_attempt_epoch: number
  // 当 current_attempt_id 非 null 时，以下字段从 run_attempts 嵌入
  owner_display?: string            // actor_display 快照，如 "Alice (machine)"
  owner_expires_at?: string         // 判断 idle 状态用
  parent_work_item_id: string | null
  attrs: Record<string, unknown>
  created_at: string
  updated_at: string
  closed_at: string | null
}

interface DeclaredResource {
  type: "repo"|"path"|"document"|"section"|"service"|"external_ref"
  uri: string              // "repo:marketplace", "file:src/auth/**", "jira:IEBE-42"
  intent: "read"|"write"|"refactor"|"delete"
  base_branch?: string
  task_branch?: string
  state?: "declared"|"prepared"|"committed"|"pushed"|"pr_open"|"merged"
}
```

### 19.2 RunAttempt 对象

```typescript
interface RunAttempt {
  id: string
  work_item_id: string
  status: "running"|"paused"|"wrapped"|"failed"|"superseded"|"lost"
  claim_epoch: number
  expires_at: string | null
  last_active_at: string
  actor_user_id: string
  actor_display: string     // "<display_name> (<user_type>)"，快照，user 改名不影响历史
  machine_id: string
  started_at: string
  ended_at: string | null
}
// actor_display 格式约定：
//   human user:   "Wang Xiaokang"
//   machine user: "Alice Agent Fleet (machine)"
// server 在 INSERT run_attempts 时从 users 表读取并写入快照
```

### 19.3 pf_create_work_item — 完整请求

```typescript
interface CreateWorkItemRequest {
  project: string                    // required
  goal: string                       // required，单行，max 500 chars
  scenario?: string                  // default "coding"
  priority?: string                  // default "normal"
  wi_type?: string                   // 对应 phase.yaml 中的键，团队自定义（如 fix_bug/critical_bug）
  requires_human_session?: boolean   // 从 phase.yaml wi_type 定义读取；可显式覆盖
                                     // 决定 wi 进 items[]（auto）还是 needs_human_session[]
  milestone?: string
  labels?: string[]                  // max 20 items，每个 max 50 chars
  declared_resources?: DeclaredResource[]
  parent_work_item_id?: string
  blocked_by?: string[]              // [wi_id, ...]，server 原子写 wi_dependencies
  source?: string                    // default "human"
  attrs?: Record<string, unknown>    // 顶层 key 必须在 whitelist
  force_create?: boolean             // default false，跳过 dedup
  force_reason?: string              // force_create=true 时必填，min 10 chars
}

// 成功
interface CreateWorkItemResponse {
  id: string
  slug: string
  status: "queued"
  created_at: string
}

// 409 DUPLICATE
interface DuplicateError {
  code: "CONFLICT_DUPLICATE"
  message: string
  details: {
    existing: { id: string; slug: string; goal: string; status: string }
  }
}

// 409 CANDIDATES
interface CandidatesError {
  code: "CONFLICT_CANDIDATES"
  message: string
  details: {
    candidates: Array<{
      id: string; slug: string; goal: string
      similarity: number   // 0.0-1.0
      status: string
    }>
  }
}
```

### 19.4 pf_claim_work_item — 完整请求

```typescript
// 决策A: session_secret 由 MCP server 用 crypto/rand 生成，不经过 LLM prompt
// skill/LLM 层完全不传 session_secret
interface ClaimRequest {
  idempotency_key: string      // MCP server 生成（可由 skill 传，也可 server 自动生成）
  session_info: {
    machine_id: string         // 从 ~/.polyforge/config.toml 读取
    // session_secret 不在此处 —— 由 MCP server 内部 crypto/rand 生成
  }
  requested_locks?: Array<{
    resource_type: string
    resource_key: string
  }>
  mode?: "fresh" | "resume"    // default "fresh"
  force_takeover?: boolean     // default false
  phase_yaml_snapshot?: object
}

// MCP server 调用 aihub 时使用的内部请求（aihub HTTP API 层）
interface AihubClaimRequest extends ClaimRequest {
  session_info: {
    machine_id: string
    session_secret: string     // MCP server 生成 64-hex，不暴露给 skill/LLM
  }
}

interface ClaimResponse {
  attempt_id: string
  claim_epoch: number
  expires_at: string
  // session_secret 不返回给 skill/LLM —— MCP server 直接写入 state file
  acquired_locks: Array<{ resource_type: string; resource_key: string }>
  current_attempt_epoch: number
  // Alice-2 Wall#2 / §9.5.6：crash recovery hint，指导 Wi Agent 如何接手
  step_recovery_hint?: "clean" | "crashed_in_progress" | "active_in_progress_conflict"
  // H-R8-16 / Wi-Agent-C W1: Wi Agent 用于判断 start_step 是否需要 inline 执行
  requires_human_session: boolean
  // wi_type 同时返回，方便 Wi Agent 匹配 phase.yaml
  wi_type: string | null
}

// C-R7 + Alice-2 Wall#11：re-claim（mode=resume / force_takeover）时
// fn_claim_work_item 在同一事务内把 prior attempt.status 改为 'superseded'
// 确保 prior attempt 不会永远停留在 'running' 状态（否则只能等 24h zombie sweeper）
```

### 19.5 PATCH step — 完整交互

```typescript
// in_progress（开始执行，server 生成 step_attempt_id）
interface StepStartRequest {
  attempt_id: string
  claim_epoch: number
  session_secret: string
  step_id: string
  status: "in_progress"
  expected_version: number
}
interface StepStartResponse {
  step_attempt_id: string    // server 生成，后续 completed/failed 时必须带回
  version: number            // 新 version
}

// completed（server 推进 current_step）
interface StepCompleteRequest {
  attempt_id: string
  claim_epoch: number
  session_secret: string
  step_id: string
  status: "completed"
  step_attempt_id: string    // 必须与 in_progress 时拿到的一致
  artifact_summary?: string  // max 4096 chars
  expected_version: number
}
interface StepCompleteResponse {
  next_step: string | null   // null = 所有步骤完成
  version: number
}

// failed
interface StepFailRequest {
  attempt_id: string
  claim_epoch: number
  session_secret: string
  step_id: string
  status: "failed"
  step_attempt_id: string
  error_type: string         // "timeout"|"tool_error"|"external_dependency"|"agent_error" 等
  escalated: boolean         // true → server emit step_failed + wi→stalled
  expected_version: number
}
interface StepFailResponse {
  version: number
}

// CAS 失败（412 PRECONDITION_FAILED）
interface StepCASError {
  code: "PRECONDITION_FAILED"
  details: { current_version: number; current_state: string }
}
```

### 19.6 Memory 对象

```typescript
interface Memory {
  id: string                       // mem_<ulid>
  project: string
  author_user_id: string
  work_item_id: string | null
  visibility: "private"|"project"|"team"
  type: string                     // taxonomy 见 §7.1
  content: string
  attrs: {
    related_ids?: string[]
    reinforcements?: Array<{
      added_at: string
      from_wi: string
      context: string
    }>
    context_snippet?: string       // max 500 chars
    similar_to?: string[]          // memory id list（dedup_mode=suggest 时填）
  }
  base_strength: number            // 1-5
  stability_days: number
  activation_count: number
  last_activated_at: string | null
  last_activated_by: string | null
  last_activated_by_display: string | null  // 快照，同 actor_display 格式
  is_immortal: boolean
  status: "active"|"archived"|"redacted"
  effective_strength: number
  supersedes_id: string | null
  expires_at: string | null
  created_at: string
  // 查询时额外嵌入
  author_display: string    // 同 actor_display 格式，从 users.display_name 快照
}
```

---

## 19. 事件 Payload Schema

每个 `event_type` 的 `payload` 结构如下（其余字段自由扩展）：

```typescript
// work_item_filed
{ source: string; kind?: string }

// attempt_started
{ machine_id: string; actor_display: string; is_takeover: boolean;
  is_resume: boolean; claim_epoch: number }

// attempt_completed
{ status: "wrapped"|"failed"|"paused"; duration_seconds: number }

// attempt_superseded
{ superseded_by_attempt_id: string; reason: string; actor_user_id: string }

// force_takeover
{ prior_attempt_id: string; prior_actor: string; reason: string }

// lock_acquired
{ resource_type: string; resource_key: string }

// lock_released
{ resource_type: string; resource_key: string }

// commit
{ repo: string; sha: string; message: string; files: string[] }

// push
{ repo: string; branch: string; base_sha_at_push: string }

// push_blocked_base_moved
{ repo: string; base: string; base_now: string; base_when_branched: string }

// pr_opened
{ repo: string; number: number; url: string; title: string }

// step_started
{ step_id: string; wi_type: string; step_attempt_id: string;
  action: string; attempt_no: number }

// step_completed
{ step_id: string; step_attempt_id: string;
  artifact_summary?: string; duration_seconds: number }

// step_failed
{ step_id: string; step_attempt_id: string; error_type: string;
  escalated: boolean; suggested_action?: string }

// step_heartbeat
{ step_id: string; step_attempt_id: string }

// step_agent_unresponsive
{ step_id: string; waited_seconds: number; retry_no: number }

// memory_saved
{ memory_id: string; type: string; visibility: string }

// memory_activated
{ memory_id: string; activation_count: number;
  new_stability_days: number; effective_strength: number }

// memory_archived
{ memory_id: string; last_effective_strength: number; days_inactive: number }

// memory_reinforced
{ memory_id: string; context_snippet: string;
  new_base_strength: number; activation_count: number }

// conflict_prediction_overridden
{ reason: string; predictions: object[] }

// note
{ text: string }

// decision
{ text: string; options_considered?: string[] }

// wi_stalled
{ step_id: string; stall_reason: string; escalated_at: string }

// wi_goal_updated
{ old_goal: string; new_goal: string; reason: string; changed_by: string }
// 时间线标记：此 event 之前的 artifacts/events 属于旧 goal 上下文
```

---

## 20. Skill 完整 Mechanic

### 21.1 `using-polyforge`（meta skill，session 起手自动加载）

```markdown
# Session 起手扫描（WALL-5.1/Dave WALL-D4 补充）
扫描 .polyforge/state/*.json，对每个文件：
  1. 读 wi_id + attempt_id
  2. pf_list_work_items(ids=[wi_id]) 校验 server 端状态
  3. 若 attempt 仍 running → 展示"⚠️ 你有进行中的 wi: [slug]，是否 resume？"
  4. 若 attempt 已 superseded/lost → 提示删除孤儿 state file

# Iron Rules（所有操作前必须遵守）

IR1 写操作必须在 pf.<xxx>/ worktree 内
IR2 遇到障碍分析根因，不绕过
IR3 MCP 不可用时停止，不直接 HTTP
IR4 非平凡 feature = 一份 spec + 一份 plan

# Memory-First（每次行动前执行）
pf_recall(project, query=<user_intent>, type="experience.*|rule.*", top_k=5)
展示 effective_strength >= 0.3 的结果，标注"仅供参考"
用户/LLM 认为有用的条目 → pf_activate_memory(id)

# NL 路由表
# ── Session 1：批量自动执行 ──
今天有哪些活/今天干什么/这轮有什么/派活/ready queue  → Orchestrator round（pf_get_ready_queue + fan-out）
# ── Session 2/3：人工主导 ──
哪些活需要我来拍板/需要讨论的/needs attention        → pf_get_ready_queue → 展示 needs_human_session[]
# ── 单 wi 操作 ──
开始/新任务/new         → /pf-work --new
认领/claim              → /pf-work <slug>
继续/resume             → /pf-work <slug> --resume
暂停/pause              → /pf-stop --pause
完成/done/wrap/搞定      → /pf-stop --wrap
失败/failed             → /pf-stop --fail
状态/status/进度         → /pf-status
设计/spec/brainstorm    → /pf-spec
计划/plan               → /pf-plan
执行/execute/run it     → /pf-execute
review/代码审查          → /pf-review
回顾/retro              → /pf-retro
调试/debug/这个 bug      → /pf-debug
记录/note/log           → /pf-event note
同步/sync/jira/github   → /pf-sync
发布/release/cut        → /pf-release

# 三段式输出规范（所有 skill 强制）
## 结果 / ## 状态 / ## 下一步
```

### 21.2 `pf-work`

```
触发：用户想开始做某事

── 模式 A：新建 wi（--new 或自然语言意图） ──

1. Memory-First（using-polyforge 已处理，可直接展示结果）

2. AI 判断 wi_type（读 .polyforge/phase.yaml 的 wi_types 定义）：
   -- 根据 goal 描述 + kind + 复杂度，选择最合适的 wi_type
   -- 判断维度：
   --   是 bug？根因明确/改动小 → fix_bug（auto）
   --             影响大/根因不明 → critical_bug（human_session）
   --   是 feature？改动小/范围清晰 → simple_feature（auto）
   --               需要设计决策    → feature（human_session）
   -- 同时确定 requires_human_session（从 wi_types[wi_type].requires_human_session 读取）

3. pf_create_work_item(
     project=<from .polyforge.yaml>,
     goal=<user_goal>,
     wi_type=<AI 判断或 rules engine 推断>,
     wi_type=<AI 判断>,
     requires_human_session=<从 phase.yaml 读>,
     priority=<推断>,
     labels=<推断>,
     declared_resources=<推断或空>
   )
   → 成功：得到 {id, slug}
   → 409 DUPLICATE：展示已有 wi，询问"继续新建 / 直接认领已有 / 取消"
   → 409 CANDIDATES：展示候选列表，询问选择

3. 用户确认后 pf_predict_conflicts(declared_resources, dry_run=true)
   → 展示 impact preview（会抢哪些锁、解锁哪些下游）

4. pf_claim_work_item(
     id_or_slug=<新建的 wi>,
     idempotency_key=<client ULID>,
     session_info={machine_id},  -- session_secret 由 MCP server 内部生成，不在 skill prompt 里
     requested_locks=[{resource_type: "git_branch",
                       resource_key: "<project>/polyforge/<slug>"}],
     mode="fresh",
     phase_yaml_snapshot=<读 .polyforge/phase.yaml 上传>
   )
   → 得到 {attempt_id, claim_epoch, expires_at}
   -- session_secret 由 MCP server 写入 state file，不出现在 skill 返回值（决策A）
   → 409 CONFLICT_HARD_BLOCK：展示冲突详情，建议 /pf-resolve-conflict

5. 输出三段式

── 模式 B：认领已有 wi（/pf-work <slug>） ──

1. pf_predict_conflicts(wi_id=<slug>, dry_run=true) → impact preview
2. pf_claim_work_item(id_or_slug=<slug>, mode="fresh", ...)
3. 输出三段式

── 模式 C：恢复（/pf-work <slug> --resume） ──

1. pf_claim_work_item(id_or_slug=<slug>, mode="resume", ...)
   → prepared_workspace + step_state 从上次 attempt 恢复
2. 输出三段式（含 step 进度）

── 模式 D：强制接管（/pf-work <slug> --force） ──
-- WALL-D7 修正：writer 可接管任何 idle > 30min 的 attempt（不限同用户）
-- WALL-D6 修正：mode=resume（接手旧工作），不是 mode=fresh

1. 检查：expires_at < now()（idle > 30min）→ writer 可接管；admin 任意时间
2. pf_force_takeover(id_or_slug=<slug>, reason=<user 输入>)
3. pf_claim_work_item(mode="resume", ...)  -- 从 task_branch commits 接续
4. 输出三段式
```

### 21.3 `pf-stop`

```
── pause ──
1. pf_complete_attempt(
     attempt_id=<current>,
     claim_epoch=<current>,
     session_secret=<local>,
     status="paused"
   )
   注意：如有 step 处于 in_progress，先 pf_update_step(failed)，再 complete
2. 输出三段式

── wrap ──
1. coding scenario：pf_wrap（= on_wrap hook + complete_attempt + cleanup_workspace）
   非 coding：pf_complete_attempt(status="wrapped")
2. pf_emit_event(event_type="note", payload={text="wrapped: <1句总结>"})
3. 建议执行：/pf-retro（知识沉淀）
4. 输出三段式

── fail ──
1. pf_complete_attempt(status="failed")
2. pf_emit_event(event_type="note", payload={text="failed reason: <user 描述>"})
3. 输出三段式
```

### 21.3.5 `pf-add-type`

```
触发：用户想新增 wi_type / phase.yaml 里没有匹配的类型

1. pf_get_scenario_config(scenario)
   → 读当前 wi_types 列表，展示给用户作参考

2. 引导用户描述新 wi_type：
   - 这类工作有哪些步骤？
   - 需要人工主导还是可以自动执行？（requires_human_session）
   - 第一步是讨论方案（pf-spec）还是直接开始写代码（prepare_context）？

3. AI 根据对话起草新 wi_type YAML 定义，展示给用户确认

4. 用户确认后（H-R9-8: 先 server 后本地，保证 SoT 一致）：
   a. pf_update_scenario_config(scenario, new_content, version=<current>)
      → server 更新，emit phase_config_updated event
      → 409 CONFLICT_VERSION_MISMATCH：GET 最新版本，展示 diff，等用户确认后重试（禁止自动 merge）
   b. server 成功后，写入本地 .polyforge/phase.yaml（Edit tool）
      → 本地写失败（disk/权限）→ pf init 重拉即可，server 已是 SoT

5. 输出三段式：
   ## 结果
   新 wi_type '<name>' 已添加到 coding scenario。
   ## 状态
   现在可以用 /pf-work --goal "..." 创建该类型的 wi。
   ## 下一步
   /pf-work --goal "..."
```

### 21.4 `pf-status`

```
── 单 wi 状态 ──
1. pf_list_work_items(ids=[<current_wi>], include_step_state=true)
2. pf_read_events(wi_id, limit=5, pinned_first=true)
3. 输出三段式（wi 字段 + 最近 5 条 event）

── 全局视图 ──
1. pf_get_ready_queue(project)  ← v1.20: 一次调用拿六段（含 needs_human_session + unclassified）
2. 渲染 LCRS 六段视图（items/running/stalled/paused/needs_human_session/unclassified）
   -- Alice-Session-1 W1 修复：/pf-status 现在显示完整的"需要你来处理"列表
3. 输出三段式（需要关注的段落重点高亮）
```

### 21.5 `pf-spec`

```
1. Memory-First：pf_recall(query=wi.goal, type="methodology.spec|fact.*|rule.*", top_k=3)
   展示相关历史 spec 供参考

2. 引导用户/AI 定义：
   - 问题范围（What / Why）
   - Non-goals（不做什么）
   - 设计决策（关键 trade-off）
   - 验收标准（Acceptance Criteria）

3. pf_save_artifact(
     type="spec",
     work_item_id=<current>,
     content=<markdown spec>,
     structured_payload={
       feature: "<feature_name>",
       decisions: [{decision, reason, alternatives}],
       acceptance_criteria: [...]
     },
     visibility="project"
   )
   → {memory_id}

4. pf_emit_event(event_type="note",
     payload={text: "spec saved: mem_<id>"})

5. 输出三段式
```

### 21.6 `pf-plan`

```
1. Memory-First：pf_recall(query=wi.goal, type="methodology.plan|experience.*", top_k=3)

2. 读 spec：pf_recall(work_item_id=<current>, type="methodology.spec", top_k=1)

3. 产出 plan（包含 steps，每步含 id/title/depends_on/effort_hint/description）

4. spawn child wi（拓扑顺序创建，blocked_by 在创建时原子传入）：
   -- 先拓扑排序 plan.steps（保证依赖的 wi 先创建）
   step_to_wi = {}
   for step in topological_sort(plan.steps):
     child = pf_create_work_item(
       project=<current>,
       goal=step.title,
       parent_work_item_id=<current_wi>,
       source="auto_execute",
       blocked_by=[step_to_wi[dep] for dep in step.depends_on]
       -- v1.18: 不传 phase，child wi 直接进 Ready Queue
     )
     step_to_wi[step.id] = child.id

5. pf_save_artifact(
     type="methodology.plan",
     work_item_id=<current>,
     content=<markdown plan>,
     structured_payload={
       steps: [{id, title, depends_on, effort_hint, child_wi_id?}]
     },
     supersedes_memory_id=<先用 pf_recall(work_item_id, type=methodology.plan) 查，有则传旧 id>
   )

6. 输出三段式
```

### 21.7 `pf-execute`（Wi Agent 主循环）

```
1. pf_list_work_items(ids=[<current>], include_step_state=true)
   → 得到 step_state

── 有 phase.yaml（step mode）──

2. 执行 start_step：
   -- Alice-Session-2 W4 / H-R8-7 修复：requires_human_session=true 时 Wi Agent 不 dispatch
   -- subagent，而是由 Wi Agent（= Alice 的 main CC session）inline 执行 start_step skill

   if wi.requires_human_session == true AND start_step.action IN ['pf-spec', 'pf-plan']:
     -- inline 执行（不 dispatch subagent）：Wi Agent 自己跑 start_step，Alice 在场参与讨论
     -- pf-spec 中"引导用户/AI 定义"通过 main session LLM ↔ Alice 的对话实现
     -- 注意：pf-spec step in_progress 期间必须定期调 pf_emit_event(type=step_heartbeat)
     --       防止 30min 无 mutating tool call → expires_at 过期 → 被他人接管（B5 wall）
     inline_execute(start_step.action,
                    wi_id, step_attempt_id,
                    pf_recall_results=<Memory-First results>)
   else:
     -- auto wi 或非交互 start_step → 正常 dispatch subagent
     dispatch subagent("执行 start_step：
       调 coding/prepare_context skill
       完成后调 pf_update_step(step_id=start_step, status=completed,
                               artifact_summary=<initial_context>, ...)")
   校验：pf_get_step(wi_id) 确认 start_step 已 completed

3. loop:
   step_info = pf_get_step(wi_id)
   if step_info.current_step == null → goto 4（自动 retro）

   dispatch subagent("执行 step {step_id}:
     action={step_info.action}
     resolved_skill={step_info.resolved_skill}
     previous_context={step_info.previous_steps}
     先调 pf_update_step(in_progress) 拿 step_attempt_id
     执行 action（调 skill 或 NL 指令）
     完成后调 pf_update_step(completed, step_attempt_id, artifact_summary)
     失败后调 pf_update_step(failed, escalated=true/false)")

   等待（60s 超时）：pf_get_step 校验 wi_step_state.current_step_status
   如仍是 in_progress 且 step_started_at 距今 > 60s → step_agent_unresponsive
     M16 重试逻辑（防死锁）：
       1. 先调 pf_update_step(failed, step_attempt_id=<旧>, escalated=false)
          → 清除旧 step_attempt_id，重置 current_step_status='idle'
       2. 再 dispatch 新 subagent（会拿到新 step_attempt_id）
       3. 最多重试 2 次，第 3 次失败 → escalated=true
     不要直接重 dispatch（旧 step_attempt_id 仍在会导致 409 冲突）

   step 完成后：
     Wi Agent（LLM）判断 artifact_summary 是否值得提炼经验（§7.7）

4. 自动 retro（决策4：Wi Agent 内置）
   -- Alice-1 WALL-7.1: retro 改为 dispatch 轻量 subagent，避免 Wi Agent context 溢出
   -- Wi Agent 经历 4+ steps 后 context 已大，inline retro 极易触发 context overflow
   -- 解法：dispatch 专用 retro subagent，传入 compact inputs（只传 artifact_summary 列表，不传完整 transcript）
   dispatch retro subagent(
     wi_id=<id>,
     step_summaries=[{step_id, artifact_summary}],  ← 每步摘要，不传原始 transcript
     wi_goal=<goal>
   )
   retro subagent 内部：
     pf_read_events(wi_id, limit=50, types=['commit','push','pr_opened','step_completed'])
     批量 pf_recall + pf_remember + pf_activate_memory
     pf_save_artifact(type="methodology.wrap_summary", content=<总结>)
   任一失败 → emit retro_partial_failure，继续 wrap（best-effort，不阻塞）

5. pf_wrap
   -- pf_wrap 仅 coding scenario 使用（= on_wrap + complete_attempt + cleanup）
   -- 非 coding：直接调 pf_complete_attempt(status=wrapped)

── 无 phase.yaml（free mode）──

2. 读 plan（如有）：pf_recall(work_item_id, type="methodology.plan", top_k=1)

3. 按 plan steps 依次 dispatch subagent，或 inline 执行

4. 自动 retro（同上步骤 4）→ pf_wrap

5. 输出三段式
```

### 21.8 `pf-retro`

```
1. pf_list_work_items(ids=[<current>], include_step_state=true)
2. pf_read_events(wi_id, limit=100) → 完整事件流
3. pf_recall(query=wi.goal, type="experience.*", top_k=3)
   → 找相关历史经验

4. LLM 分析：
   - 本次任务做了什么
   - 碰到的问题和解决方法
   - 与历史经验的对比（新发现 / 印证了什么 / 打破了什么）
   - 下次类似任务的建议

5. 批量保存（recall-before-remember 模式）：
   for finding in findings:
     candidates = pf_recall(query=finding.content, type=finding.type, top_k=3)
     if similarity > 0.85 → pf_activate_memory + pf_reinforce_memory（如有新细节）
     elif similarity > 0.65 → pf_remember(dedup_mode="suggest")
     else → pf_remember(dedup_mode="off")

6. pf_save_artifact(
     type="retro",
     work_item_id=<current>,
     content=<markdown retro>,
     structured_payload={
       went_well: [...], went_wrong: [...],
       learnings: [...], next_time: [...]
     }
   )

7. pf_save_artifact(type="methodology.wrap_summary", content=<1 段总结>)

8. 输出三段式
```

### 21.9 `coding/prepare_context`（start_step）

```
1. pf_list_work_items(ids=[<current>], include_step_state=true)
   → 拿 goal / declared_resources / labels

2. Memory-First：
   pf_recall(project, query=wi.goal, type="experience.*", top_k=5,
             min_strength=1.5, recency_weight=0.4)
   pf_recall(project, query=wi.goal, type="rule.*", top_k=3)
   → 对有用的条目 pf_activate_memory(id)

3. 分析代码（declared_resources 中列出的文件/路径）：
   Bash: git -C <worktree> log --oneline -5
   Read: 关键文件（最多 5 个，各 100 行）

4. 构造 initial_context：
   {
     "goal_analysis": "<LLM 对 goal 的理解>",
     "relevant_files": ["<path>", ...],
     "prior_experience": ["<memory summary>", ...],
     "known_pitfalls": ["<pitfall>", ...],
     "suggested_approach": "<1 段建议>"
   }

-- C5-1 + Alice-1 WALL-6.1: start_step 必须先 in_progress 再 completed
-- expected_version 必须从 pf_get_step 获取（不能从 pf_list_work_items 拿，那里没有 version 字段）
4.5. version_info = pf_get_step(wi_id)  ← 先拿 version（list_work_items 不含此字段）
5a. pf_update_step(step_id="start_step", status="in_progress",
      expected_version=version_info.version)
    → 得到 step_attempt_id + 新 version

5b. pf_update_step(step_id="start_step",
     status="completed",
     step_attempt_id=<from 5a>,
     step_attempt_id=<从 in_progress 步骤拿到的>,
     artifact_summary=JSON.stringify(initial_context)[0:4096]
   )
```

### 21.10 `coding/commit_and_pr`（ship step）

```
1. pf_update_step(step_id="ship", status="in_progress", ...)
   → 得到 step_attempt_id

2. pf_diff(workspace_root, repo, vs_base=true)
   → 确认有变更，LLM 生成 commit message

3. pf_commit(
     workspace_root=<ws>,
     work_item_id=<id>,
     -- H4-11: attempt_id/claim_epoch/session_secret 由 MCP server 从 state file 注入
     repo=<repo>,
     message=<commit_message>
   )

4. pf_push(workspace_root, work_item_id, repo, skip_base_check=false)
   → 失败 base_moved → emit event，建议 rebase，退出 step（failed, escalated=false）

5. pf_pr(
     workspace_root, work_item_id, repo,
     title=<LLM 生成>,
     body=<包含 wi slug + goal + changes summary>
   )
   → 得到 {url, number}

6. pf_update_step(step_id="ship", status="completed",
     step_attempt_id=<from step 1>,
     artifact_summary="PR #{number}: {url}")

7. 输出三段式（PR url 在结果段）
```

### 21.11 `coding/code_change`（implement step）

```
1. pf_update_step(step_id=<current>, status="in_progress", ...)
   → 得到 step_attempt_id

2. 读 initial_context（从 pf_get_step 的 previous_steps[start_step].artifact_summary）

3. 自由实现（LLM 根据 goal + context 写代码）
   工具：Bash / Read / Edit / Write（在 worktree 内操作）

4. 实现完成后，LLM 判断是否有值得提炼的经验：
   if found_interesting:
     pf_recall(query=<finding>, type="experience.*", top_k=3)
     if no_similar → pf_remember(type="experience.code"|"experience.pitfall", ...)

5. pf_update_step(step_id=<current>, status="completed",
     step_attempt_id=<from step 1>,
     artifact_summary={
       summary: "<1句实现描述>",
       files_changed: ["..."],
       notes: "<可选：发现的问题或注意事项>"
     })
```

---

## 21. AttemptCredential 验证逻辑

所有 mutating MCP tool 和 HTTP API（attempt-scoped）的 server 端验证顺序：

```go
func verifyAttemptCredential(ctx, wiID, attemptID, claimEpoch, sessionSecret string) error {
    // 1. 加载 wi（读 current_attempt_id + current_attempt_epoch）
    wi := db.GetWorkItem(wiID)

    // 2. 验证 attempt 是否是当前 attempt
    if wi.CurrentAttemptID != attemptID {
        return ErrConflictEpochMismatch
    }

    // 3. 验证 claim_epoch（防重放攻击）
    attempt := db.GetAttempt(attemptID)
    if attempt.ClaimEpoch != claimEpoch {
        return ErrConflictEpochMismatch
    }

    // 4. 验证 session_secret（constant-time HMAC 比对）
    hash := sha256.Sum256([]byte(sessionSecret))
    if !hmac.Equal(hash[:], decodeHex(attempt.SessionSecretHash)) {
        return ErrUnauthorized
    }

    // 5. C4: 只接受 status='running' 的 attempt（paused attempt 不允许 mutating 操作）
    // paused attempt 只能通过 re-claim 恢复到 running，不能直接操作
    if attempt.Status != "running" {
        return ErrAttemptMismatch
    }

    // 6. 更新 last_active_at（heartbeat）
    db.UpdateLastActiveAt(attemptID)

    return nil
}
```

---

## 22. phase.yaml 验证规则

```go
type PhaseYAML struct {
    SchemaVersion string              `yaml:"schema_version"`  // H-R3-3: 必须 "phase.v1"（非 "1.0"）
    GeneratedBy   string              `yaml:"generated_by"`    // "ai"|"human"
    WITypes       map[string]WIType   `yaml:"wi_types"`        // min 1 entry
}

type WIType struct {
    // M-R8-23 / C-R8-2: RequiresHumanSession 必填，无默认值
    // 缺失 → 返回 400 INVALID_PHASE_YAML（"wi_type.requires_human_session is required"）
    // 不允许 Go 零值 false 兜底 —— 必须显式声明意图
    RequiresHumanSession bool    `yaml:"requires_human_session"` // 必填
    StartStep           *Step   `yaml:"start_step"`              // 可选
    StopStep            *Step   `yaml:"stop_step"`               // 可选
    Steps               []Step  `yaml:"steps"`                   // min 1
    // L-R8-32: requires_human_session=false 时 start_step.action 不可以是 pf-spec/pf-plan
    // → 校验规则：if !RequiresHumanSession && (StartStep.Action == "pf-spec" || "pf-plan")
    //             → 400 INVALID_PHASE_YAML("auto wi_type cannot have pf-spec/pf-plan as start_step")
}

type Step struct {
    ID          string   `yaml:"id"`          // 必填，snake_case，[a-z_]{1,50}
    Action      string   `yaml:"action"`      // 必填，[a-z_-]{1,100}
    Description string   `yaml:"description"` // action fallback 时必填
    Requires    []string `yaml:"requires"`    // step id 列表，校验 id 存在
}

验证规则：
- schema_version 必须 "phase.v1"（M18：与 plugin version 明显区分，不用 "1.0"）
- step.id 在同一 wi_type 内唯一
- requires 中的每个 id 必须是同 wi_type 内已定义的 step.id
  或特殊值 "start_step"（如果定义了 start_step）
- v1 不支持 requires 多个值（fan-in），若有 → 取第一个，emit warning event
- action = "start_step" / "stop_step" 是保留值，不能用于普通 step
- start_step 和 stop_step 的 id 固定为 "start_step" / "stop_step"
- 整个 wi_type 的 step DAG（含 start_step 和 stop_step）不能有环
```

---

## 23. 冲突预测规则（M12，5 条）

`pf_predict_conflicts` 按以下顺序应用 5 条规则，任一 hard_block 即停止：

```
规则 1：resource_lock 冲突
  running attempt 的 resource_locks 与 declared_resources 有相同 (type, key)
  → severity: hard_block
  → 在 claim 原子事务内二次校验（advisory 只是预检）

规则 2：同分支冲突
  declared_resources 含 {type:"repo", uri:"repo:X"} 且
  running attempt 的 resource_locks 含 (git_branch, "X/<任意>")
  → severity: soft_block（warn）

规则 3：path glob 重叠
  declared_resources 的 file:path 与 running attempt 的 file:path glob 重叠
  → intent 均含 write/refactor/delete → severity: soft_block
  → 至少一方是 read → severity: info

规则 4：同 repo refactor
  双方均声明 {uri:"repo:X", intent:"refactor"}
  → severity: soft_block

规则 5：external_ref 重叠
  双方均引用同一 jira:KEY / github:issue/N
  → severity: info（仅提示）

dry_run=true 时跳过规则 1 的 lock 检查，仅做资源预测。
predict_conflicts 是 advisory，claim 时仍在事务内原子执行规则 1。
```

---

## 24. Step Agent 凭证注入与上下文传递

### 24.1 设计原则

三个约束必须同时满足：
1. **secret 不进 LLM prompt**：`session_secret` 写进 subagent 文本 → 会原文回显到 transcript/event payload
2. **aihub CAS 不可绕过**：step 状态推进必须经过 `wi_step_state.version` CAS，防并发冲突
3. **跨 session 可恢复**：Wi Agent 崩溃后新 session 能从 aihub 恢复 `current_step`

### 24.2 凭证注入：MCP server 从本地 state file 读取

`step_agent_tokens` 独立表**删除**，改为 MCP server 透明注入：

**workspace_root 获取**（多 workspace 场景解决方案）：
```go
// MCP server 启动时（由 Claude Code 作为子进程启动）
// cwd = Claude Code 打开的项目目录 = workspace root，天然一一对应
workspaceRoot, _ := os.Getwd()
// 每个 Claude Code 窗口一个 workspace = 一个 MCP server 进程，自然隔离
// CLI（pf doctor / pf ready）另走向上搜 .polyforge.yaml 的逻辑（§9.5.5）
```

```go
// MCP server 中间件：mutating 工具自动注入凭证，read-only 工具直接放行
func credentialMiddleware(next ToolHandler) ToolHandler {
    return func(ctx context.Context, req ToolRequest) (*ToolResult, error) {
        if isReadOnly(req.ToolName) {
            // H4-2: read-only 工具（pf_list_work_items / pf_get_step / pf_recall 等）
            // 不注入凭证，不要求 state file 存在
            return next(ctx, req)
        }
        wiID := extractWIID(req)
        if wiID == "" {
            return next(ctx, req)
        }
        state, err := loadStateFile(workspaceRoot, wiID)
        if err != nil {
            return nil, ErrAttemptNotFound
        }
        // C4-1: 凭证过期（server 返回 CONFLICT_EPOCH_MISMATCH）时自动清理 state file
        ctx = withCredentials(ctx, state.AttemptID, state.ClaimEpoch, state.SessionSecret)
        ctx = withActiveWI(ctx, wiID)   // 防止 Step Agent 操作非当前 wi（H4-2）
        ctx = withProject(ctx, state.Project) // Bob finding: project 从 state file 注入
        // pf_remember / pf_recall / pf_save_artifact 的 project 参数可省略
        // MCP server 自动从 state.Project 填充（Step Agent prompt 无需传 project）
        return next(ctx, req)
    }
}
```

**工具签名简化**（coding tools 不再需要 attempt 三元组）：

```
pf_commit(workspace_root, work_item_id, repo, message, paths?)
pf_push(workspace_root, work_item_id, repo, skip_base_check?)
pf_pr(workspace_root, work_item_id, repo, title, body, head?, base?)
pf_update_step(work_item_id, step_id, status, step_attempt_id?,
               artifact_summary?, error_type?, escalated?, expected_version)
```

所有工具 `attempt_id / claim_epoch / session_secret` 参数从 client 传入改为 MCP server 自动注入。

### 24.3 subagent prompt 规范（无 secret）

Wi Agent dispatch Step Agent 时 prompt 只包含非敏感标识符：

```
执行 wi marketplace#42 的 step "review"：
  work_item_id:   wi_A3f7B9c2
  step_id:        review
  step_attempt_id: sa_P4m8N2k6   ← 仅用于 completed/failed 时校验，非 secret
  workspace_root: /root/code/aicoding/gmi-ws-v3
  worktrees:
    marketplace: .../pf.42.01K5G3AB/marketplace
  previous_context:
    implement: "修了 auth.go:42，新增 validateToken()"
```

`session_secret` 永远不出现在 LLM 文本中。

### 24.4 本地 state file 的职责边界

state file **只存机器私有内容**（见 §9.5.4）：
- `session_secret`：aihub 只存 hash，明文只在本机
- `worktrees`：绝对路径，跨机器无意义

**不存 step 上下文**：previous_steps artifacts、completion 状态等都在 aihub，直接通过 `pf_get_step` 获取。本地无缓存 = 无同步问题。

Wi Agent dispatch Step Agent 前的调用序列：
```
pf_get_step(wi_id)   ← 从 aihub 读 current_step + previous_steps
  → 构建 subagent prompt（含 step_id, step_attempt_id, previous context）
  → dispatch Agent()
```

### 24.5 v1 scope 限制（软限制）

v1 通过 skill prompt 告知 Step Agent 可调工具：

```
Step Agent 只能调以下工具：
  pf_update_step / pf_emit_event / pf_recall / pf_get_step
  pf_activate_memory / pf_remember（experience.* 类型，visibility≤project）
  pf_commit / pf_push / pf_pr（coding tools）

禁止：pf_claim / pf_complete_attempt / pf_wrap / pf_create_work_item
```

v2 在 MCP server 层硬限制（通过 step_context 检测活跃的 step_attempt_id 来区分调用来源）。

### 24.6 删除的内容

- `step_agent_tokens` 表：不再需要（凭证注入走本地文件）
- coding tools 的 `attempt_id / claim_epoch / session_secret` 参数：MCP server 自动注入
- `run_attempts.agent_role` 字段：不再需要

---

## 25. 其他修正汇总（第三轮 Medium/Low）

```
-- C-R3-4 (M2 已标注): scenario CHECK 允许 writing/data 但未实现
   → server handler 对 writing/data 返回 405 NOT_IMPLEMENTED

-- C-R3-8: resource_locks.resource_type 与 DeclaredResource.type 映射
   声明资源类型 → 锁类型映射表：
     "repo"         → git_branch（key: "<project>/<branch_name>"）
     "path"         → file_scope（key: "<repo>:<glob>"）
     "service"      → deploy_env（key: "<service_name>"）
     "external_ref" → 不申请锁（仅冲突预测规则 5 使用）
     "document"     → file_scope（同 path）
     "section"      → file_scope（同 path）
   client 在 claim 时按此映射生成 requested_locks，server 校验匹配

-- H-R3-1: pf_cut_alpha / pf_promote 已补充 §5.6
-- H-R3-2: pf_save_artifact 在 skill mechanic 中所有调用统一使用全名 methodology.*
-- H-R3-12: stalled = status=blocked AND 最新事件为 wi_stalled（不是 paused）
   §10.2 stalled queue 查询修正为：status=blocked AND EXISTS(wi_stalled event)
-- M-R3-2: GetStepResponse 已补充在 pf_get_step tool 定义（§5.4）
-- M-R3-5 修正（Alice-1 WALL-6.9）: step_agent_tokens 表已删除（§24.6）
-- step heartbeat 新语义：Step Agent 每 30s 调 pf_emit_event(type=step_heartbeat)
-- server 收到时刷新 wi_step_state.step_started_at = clock_timestamp()
-- 效果：Wi Agent 的 60s 超时检测窗口每次心跳后重置，长任务不被误判 unresponsive
-- v1 仍是软约束（skill prompt 指导 Step Agent 定时 emit），v2 做 server 端强制
-- M-R3-6: force_takeover 机制统一为：pf_force_takeover → 返回 {prior_attempt_id}
          再显式调 pf_claim_work_item。claim 内置 force_takeover: true 参数废弃。
-- M-R3-7: pf_remember dedup 相似度阈值（dedup_mode=strict/suggest）
          strict reject 阈值 = 0.85（cosine），与 pf_recall similarity_threshold 语义一致
-- M-R3-10: pf-review/pf-debug/pf-event/pf-sync/pf-release/execute-scenario
           skill mechanic 标注为 Phase 2 实现，v1 只提供 SKILL.md stub
-- M-R3-11: pf_create_dependency 携带的 attempt_id 校验 blocked_wi_id 的归属
           调用方必须是 blocked_wi 的当前 running attempt holder
-- M-R3-12: FK ON DELETE 策略统一
           所有引用 work_items 的外键：使用 ON DELETE RESTRICT（禁止物理删）
           admin 软删通过 wi.status='cancelled' 实现，不物理 DELETE
           resource_locks ON DELETE CASCADE 保留（attempt 删除时锁同步删）

-- L-R3-1: §1.3 重复编号已在文中修正（后者改为 §1.4 版本注入）
-- L-R3-2: 章节编号将在文档最终定稿时全量重编
-- L-R3-3: GET /v1/memories type 参数注释重复行已清理
-- L-R3-6: wi.attrs whitelist 完整内容：
          允许顶层 key：github / jira / linear / internal
          例：{"github": {"pr_number": 42}, "jira": {"issue_key": "IEBE-1234"}}
-- L-R3-7: 已废弃（v1.18 phase 字段移除，handler 不再处理 phase 逻辑）
-- L-R3-8: plugin.json 建议使用绝对路径：
          {"command": "/usr/local/bin/polyforge"} 或配置 PATH 环境变量

-- L5: wi_sequences SEQUENCE 显式 BIGINT
    CREATE SEQUENCE IF NOT EXISTS wi_seq_<project> AS BIGINT START 1;
-- L6: users.author_aliases 用于 git commit author → user_id 匹配
-- L7: recency_weight default = 0.3（API 文档和实现对齐）
-- L8: idempotency_key ULID 格式：^[0-9A-HJKMNP-TV-Z]{26}$
-- L9: v1 命名见 §0
-- L10: artifact_summary 4096 Unicode characters（PG length() 计字符数）
```

---

## 27. Known Limitations（v1 已知限制，不阻塞发布）

1. **Orchestrator 无 audit trail**：Layer 3 主 agent 不 claim wi，读 Ready Queue + 派发 subagent 的行为不进 agent_events。subagent 自身 events 已够重建时间线。v2 考虑加 `pf_register_orchestrator_session`。
2. **Step Agent scope 软限制**：v1 靠 skill prompt 约束 Step Agent 可调工具，v2 做 server 端 step_context 硬校验。
3. **AI 自判 wi_type 是软约束**（M-R8-21 / H-R8-7 / Bob 模拟）：v1 由 /pf-start skill 里的 LLM 自由选择 wi_type → requires_human_session。没有 server-side 规则引擎（如 priority=urgent + kind=bug → 强制 critical_bug）。风险：critical_bug 被误判成 fix_bug 导致被自动执行。缓解措施：(a) 新建 wi 前 reporter 检查 LCRS 的 items[] 分拣是否合理；(b) 判断错误时 pf_update_work_item(wi_type=..., reclassify_reason=...) 纠正；(c) fn_claim 时检测到 REQUIRES_HUMAN_SESSION_MISMATCH → 409 拒绝，迫使纠正。v2 考虑 server-side 规则表 + 第二意见 LLM 校验。
4. ~~**phase.yaml 跨 workspace 漂移**~~（v1.23 已从根本解决）：scenario_phase_configs 表是 server 端 SoT，所有 workspace 共享同一份定义。本地 .polyforge/phase.yaml 是工作副本，通过 pf_update_scenario_config 同步。不再是已知限制。
5. **needs_human_session wi 通知：v1 为 LCRS 内嵌告警，push 推 v2**：
   v1 方案：GC aging sweep（§15 sweep 7/8）在超时后 emit `wi_needs_attention` event；
   LCRS 渲染时对超时 wi 加高亮（⚠ urgent 标记 + age 显示）；
   用户每次打开 `/pf-status` 或触发 Orchestrator round 时就能看到。
   v2 方案：aihub.yaml 配置 webhook URL，GC 触发时 POST 到 Slack/钉钉/email 等。
6. **长 LLM 对话期间 attempt 过期风险**（多人协作模拟 B5）：pf-spec 步骤中 Alice 与用户长时间讨论，若 30min 内没有 mutating tool call，attempt expires_at 过期，普通 writer 可 force_takeover。缓解：pf-spec SKILL.md 应在每次对话后调 `pf_emit_event(type=step_heartbeat)`（仅需调用 emit，无需其他操作）刷新 last_active_at。

---

## 30. 运维 & 升级

### 30.1 aihub 重启

aihub 是 Go HTTP server；PostgreSQL 独立进程。所有状态持久化在 PG，重启不丢数据。

**重启过程**：
- in-flight HTTP 请求失败（connection reset / EOF）
- background goroutine（zombie sweeper、lock TTL sweep）在启动时重新 schedule
- run_attempts 无 `expires_at`（v1.21 已删），ownership 永久持有
- DB connection pool warm-up 约 1-2s，之后正常服务

**MCP server 一侧（用户机器）**：
- 当前 tool call 收到 connection error，MCP server 内置重试：**3 次，指数退避 500ms/1s/2s**
- 每个 mutating tool 在重试前检查 `Idempotency-Key`（已设计，§5.1），幂等安全
- 3 次均失败 → tool 返回 `{code:"AIHUB_UNAVAILABLE",retry_after_seconds:30}` 给 LLM
- skill SKILL.md 约束 LLM：收到 AIHUB_UNAVAILABLE → 暂停操作，告知用户 aihub 不可用

**健康检查**：
```
GET /v1/health
→ {"status":"ok","version":"1.x.x","db_ok":true}
```
MCP server 启动时调一次；LLM 可通过 `pf_whoami` 间接感知（server_version 字段）。

**in-progress step 不受影响**：step_state 存 DB，aihub 重启后 Wi Agent 继续调 tool，正常返回。

---

### 30.2 Claude Code / MCP server 重启

CC 是 MCP server（polyforge binary）的宿主。CC 退出 → MCP server 子进程收到 SIGPIPE/SIGTERM → 退出。

**state file 保障**（§9.5.3）：
- `.polyforge/state/<wi_id>.json` 在 pause 和 crash 时均保留（C5-3）
- CC 重启后新 MCP server 进程启动，无 in-memory 状态
- 用户执行 `/pf-status` → MCP server 读 aihub → 发现 claimed attempt + 本地 state file → 可继续

**CC 重启时 step 正在执行（crash recovery）**：
- `wi_step_state.status = in_progress`，但无 Wi Agent 在跑
- 这正是 §9.5.6 crash recovery 场景
- `pf_claim_work_item` ClaimResponse 返回 `step_recovery_hint = "crashed_in_progress"`
- `/pf-resume` skill 展示警告，用户决定 retry 还是 `pf_update_step(failed, error_type="agent_crashed")`

**startup scan 增强**：
MCP server 启动时（pf_whoami 调用之后），如果本地存在 state file：
```
1. 读 state file，取 wi_id + attempt_id
2. pf_get_step(step_id=current) → 检查 step status
3. 若 step status = in_progress：
   打印警告："⚠ wi_<id> 上次执行中断（step in_progress），请 /pf-resume 继续或 /pf-status 查看详情"
4. 若 step status = completed 但 wi.status = running：
   打印提示："wi_<id> 上次步骤已完成但 wi 仍在 running，可继续执行下一 step"
```
不静默通过，确保用户感知到中断状态。

**ownership 到期窗口**（30min takeover 阈值）：
- CC 重启耗时通常 < 1min，attempt 仍在有效窗口内
- 若用户离开 > 30min 后才重开 CC：`expires_at` 已过，其他 agent 可接管
- 若无其他 agent 接管：attempt 状态仍是 running（ownership-only 不自动释放）
- 用户可 `pf_force_takeover(wi_id)` 自己接管（takeover 自己的 wi 不受权限限制）

---

### 30.3 polyforge binary 升级（MCP server）

CC 通过 `~/.claude/settings.json` 配置 MCP server：
```json
{
  "mcpServers": {
    "polyforge": {
      "command": "polyforge",
      "args": ["mcp"]
    }
  }
}
```
CC 启动时 fork 出 MCP server 进程，stdio JSON-RPC 通信。

**升级流程**：
```bash
# 1. 安装新版 binary（PATH 下的 polyforge 被替换）
brew upgrade polyforge            # 或 curl -L ... | install
# 2. CC 不热重载 MCP server；需重启 CC 窗口
#    （关闭当前 CC session → 重开）
# 3. 新进程启动，pf_whoami 返回新 server_version
```

**version 兼容性检查**（MCP server 启动时）：
```
polyforge binary 内嵌 MIN_AIHUB_VERSION 常量（e.g. "1.2.0"）

启动时调 GET /v1/version：
  → {version:"1.3.0", min_client_version:"1.0.0"}

检查：
  server.version       >= polyforge.MIN_AIHUB_VERSION  → 继续
  polyforge.version    >= server.min_client_version     → 继续
  否则 → 打印错误 + 退出：
    "polyforge v1.1.0 需要 aihub >= 1.2.0（当前 aihub v1.0.0），请升级 aihub"
```

`GET /v1/version` 已在 §4 定义（返回 `{version, git_commit, build_time, min_client_version}`）。

---

### 30.4 aihub binary + DB migration 升级

**原则**：migration 向前兼容（只加列/表，不删旧列）；支持零停机滚动升级。

**v1.19 特别警告（H-R8-15 / migration 模拟）**：
v1.19 新增 `wi_type TEXT` 和 `requires_human_session BOOL DEFAULT NULL`。
migration 后存量旧 wi 的 `requires_human_session = NULL`（DEFAULT NULL，不是 false）。
v1.20 把 DEFAULT 改成了 NULL，NULL wi 进 `unclassified[]` 段而非 `items[]`，
**不会被自动执行**（与 v1.19 DEFAULT false 的危险不同）。
但仍需 backfill，否则 LCRS 里会有大量"unclassified"条目：
```sql
-- backfill 存量 feature wi（保守：full spec flow 要人参与）
UPDATE work_items SET wi_type='feature', requires_human_session=true
WHERE wi_type IS NULL AND kind='feature'
  AND status IN ('queued','paused') AND requires_human_session IS NULL;

-- backfill urgent bug（保守：critical_bug）
UPDATE work_items SET wi_type='critical_bug', requires_human_session=true
WHERE wi_type IS NULL AND kind='bug' AND priority='urgent'
  AND status IN ('queued','paused') AND requires_human_session IS NULL;

-- backfill normal bug → fix_bug（可自动）
UPDATE work_items SET wi_type='fix_bug', requires_human_session=false
WHERE wi_type IS NULL AND kind='bug' AND priority IN ('low','normal','high')
  AND status IN ('queued','paused') AND requires_human_session IS NULL;

-- backfill chore/task/docs/refactor → auto
UPDATE work_items SET wi_type=kind, requires_human_session=false
WHERE wi_type IS NULL AND kind IN ('chore','task','docs','refactor','spike')
  AND status IN ('queued','paused') AND requires_human_session IS NULL;

-- 不补 running/terminal wi（不影响调度，保留为 unclassified 作审计）
```
backfill 后 `aihub migrate verify` 检查所有 queued/paused wi 都有 wi_type，否则告警。

**升级步骤**：
```bash
# 1. 先跑 DB migration（aihub 仍在运行旧版）
aihub migrate up                  # Go 内嵌 migrate 工具，幂等

# 2. 停旧 aihub，起新 aihub
systemctl restart aihub           # 或 k8s rolling update
# 新 aihub 启动时自动做 migration 检查（防止跳步）

# 3. 验证
curl http://aihub/v1/health       # {"status":"ok","db_ok":true}
```

**migration 工具**：Go binary 内嵌 `golang-migrate/migrate`，migration 文件在 `internal/db/migrations/`。
CLI：`aihub migrate [up|down|version|status]`。

**回滚**：migration 文件必须包含 down 脚本（只删新增内容，不恢复旧数据）；
向前兼容列（nullable 或有 DEFAULT）允许旧版 aihub 在回滚前继续运行。

---

### 30.5 state file 格式版本

state file（`.polyforge/state/<wi_id>.json`）在字段增减时需要向前兼容：

```json
{
  "schema_version": 1,
  "wi_id": "wi_A3f7B9c2",
  ...
}
```

MCP server 读 state file 时：
- `schema_version` 字段缺失 → 按 v1 解析（兼容旧 binary 写入的文件）
- `schema_version` > 当前支持版本 → 打印警告，降级读取已知字段，忽略未知字段
- v1 不做 state file migration（字段只加不删）

---

## 29. Phase 2 Backlog（v1 完成后实现）

### 29.1 Skills（延后）

| Skill | 原因 | 说明 |
|---|---|---|
| `pf-review` | Phase 2 | v1 review step 由 Step Agent 自由执行（LLM 自我 review）；Phase 2 接入外部工具（gh review、multi-reviewer、checklist） |
| `pf-debug` | Phase 2 | v1 合并进 `pf-spec` NL 路由（debug 本质是先 spec 一下根因）；Phase 2 独立 mechanic + 工具链集成 |
| `pf-event` | Phase 2 | v1 直接调 `pf_emit_event` 即可，不需要独立 skill；Phase 2 可加结构化模板 |
| `pf-add-type` | Phase 2 | v1 合并进 `using-polyforge` NL 路由；Phase 2 可加 wi_type 校验向导 |

### 29.2 MCP Tools（延后）

| Tool | 原因 | 说明 |
|---|---|---|
| `pf_manage_actors` | Phase 2 | 配套 execute-scenario 使用，一起延后 |

### 29.3 Features（延后）

| Feature | 当前 v1 状态 | Phase 2 目标 |
|---|---|---|
| **execute-scenario（多 actor 场景测试）** | 不实现 | 注册虚拟演员（cast）按脚本模拟多用户并发；集成测试用 |
| **Orchestrator 自动触发** | 人工触发每轮 | cron / webhook 触发，无需人手动跑 |
| **Orchestrator 滑动窗口** | 严格 round barrier | max_parallel 并发，有完成即补入新 wi |
| **Step Agent server-side scope 硬校验** | skill prompt 软约束 | server 端按 step_context 校验可调工具白名单 |
| **pf-review 完整 mechanic** | stub | multi-reviewer / checklist / gh review API 集成 |
| **needs_human_session push 通知** | LCRS 内嵌告警 | webhook：aihub.yaml 配 URL，GC 触发时 POST 到 Slack/钉钉/email |
| **scenario_phase_configs per-wi_type 细粒度 CAS** | 整 content 级别 CAS | 拆成 `scenario_wi_types(scenario, wi_type)` 行级 CAS，避免全文冲突 |
| **phase.yaml 并行 step（fan-in）** | 只支持顺序 requires | `requires: [a, b]` 多前置 fan-in + 并行执行 |
| **SSH preflight 文档** | §12 三行注释 | 完整 onboarding step-by-step（v1.14 承诺的） |

---

## 28. Open Questions（待决议）

1. **dedup 阈值 calibration**：0.90/0.65 需用真实 ~150 个 wi 跑 grid search，上线前完成
2. **multi-repo wi 的 phase.yaml**：以 declared_resources 第一个 repo 为 primary，读该 repo 的 phase.yaml；显式配置 `primary_repo` 覆盖
3. **memory effective_strength 阈值 0.1**：GC 归档阈值需要 calibration，初期建议 0.05（宁可保留多的）
4. **pf-spec 长会话 step_heartbeat 频率**：每次用户消息 ack 后调一次；是否需要 server 端最大间隔强制？
