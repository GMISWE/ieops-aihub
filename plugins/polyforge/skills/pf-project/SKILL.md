---
name: pf-project
description: Manage polyforge projects — create, update, rotate identifier, list. Use when the user wants to create a new project, update project repos/description, rotate an access identifier, or list available projects.
---

# pf-project — Project Management

## Usage

**Purpose**: Manage polyforge projects — create, update, rotate access identifier, or list.

**Pattern**: `/pf-project { create | update | rotate | list } [<name>] [--description <text>] [--visible | --private] [--scenario <name>] [--repos <json>]`

**Required**: a sub-mode (see below)

**Flags**:
- `--description <text>` — project description
- `--visible` / `--private` — visibility toggle (default visible on create)
- `--scenario <name>` — scenario type, e.g. `coding`
- `--repos <json>` — array of `{name, url, github_owner_repo, description}` entries
- `rotate` is destructive: existing identifier is invalidated and a new one is issued

## When to use

- "创建 project" / "new project" / "create project"
- "更新 project" / "update project repos"
- "轮换 identifier" / "rotate identifier" / "reset access token"
- "列出 project" / "list projects" / "show projects"

## Mechanic

### Mode A — Create project

```
pf_create_project(
  name=<lowercase, 1-40 chars, a-z0-9_->,
  description=<optional>,
  visible=<true|false, default true>,
  scenario=<coding|writing|data, default coding>,
  repos=<optional array of {name, url, github_owner_repo, description}>
)
```

- 任意 writer+ 可创建
- `visible=false` → 私有 project，需通过 identifier 授权给他人访问
- name 全局唯一，创建后不可更改

### Mode B — Update project

```
pf_update_project(
  name=<project name>,
  description=<optional>,
  visible=<optional>,
  repos=<optional array>
)
```

- 只有 owner/admin 可操作
- repos 内 name/url 在同一 project 内必须唯一

### Mode C — Rotate identifier

```
result = pf_rotate_identifier(name=<project name>)
# result: {plain: "pi_xxx...", prefix: "pi_ab12"}
```

- 只有 owner/admin 可操作
- 轮换后旧 identifier **立即失效**
- `plain` 是明文 token，**只展示一次**，请用户立即保存
- `prefix` 用于标识此 token（如有多处使用）

**展示给用户：**
```
✅ Identifier 已轮换
Token: pi_xxxxxxxxxxxxx...  ← 请立即保存，此后无法再查看
Prefix: pi_ab12
```

### Mode D — List projects

```
projects = pf_list_projects()
# 返回 caller 可见的 projects
```

展示格式：
```
| name        | visible | owner    | repos |
|-------------|---------|----------|-------|
| marketplace | public  | xiaokang | 1     |
| aihub       | public  | xiaokang | 1     |
```

## Output (three-segment format)

**Create/Update/Rotate：**
```
## 结果
Project '<name>' 已[创建/更新/identifier 已轮换]。

## 状态
| 字段    | 值              |
|---------|-----------------|
| name    | <name>          |
| visible | public/private  |
| owner   | <display>       |
| repos   | <count>         |

## 下一步
- 用 /pf-work --goal "..." 在此 project 下创建 wi
- 如需授权他人访问私有 project：分享 project name + identifier token
```

## NL Triggers

- "创建 project" / "new project" / "create project"
- "更新 project" / "修改 repos" / "update project"
- "轮换 identifier" / "reset token" / "rotate access"
- "列出 project" / "show projects" / "list projects"
