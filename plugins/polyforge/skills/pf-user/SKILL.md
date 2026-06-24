---
name: pf-user
description: >
  Manage polyforge users and API keys — whoami, list users, invite (create user + issue
  initial key), update user role, issue and revoke API keys. Use when the user wants to
  check their own identity, list all team members, add a team member, change a user's
  role, or manage API keys.
---

# pf-user — User Management

## Usage

**Purpose**: Manage polyforge users and their API keys — whoami, list, invite, update role, issue/revoke keys.

**Pattern**: `/pf-user { whoami | list | invite | update | key-add | key-revoke } [<user_id>] [--role <role>] [--key-id <id>]`

**Required**: a sub-mode (see below)

**Flags**:
- `--role <role>` — `reader` | `writer` | `admin` for invite/update
- `--key-id <id>` — target API key for `key-revoke` (destructive: revokes the named key permanently)
- `list`, `invite`, `update`, `key-add`, `key-revoke` require admin role

## When to use

- "我是谁" / "whoami" / "查身份" / "my identity" → **whoami mode**
- "列出用户" / "list users" / "有哪些用户" → **list mode**
- "邀请用户" / "invite user" / "加人" / "add member" → **invite mode**
- "改用户" / "update user" / "修改角色" / "change role" → **update mode**
- "再发 key" / "issue another key" / "new key" → **key-add mode**
- "撤销 key" / "revoke key" / "删除 key" / "吊销" → **key-revoke mode**

## Mechanic

### Mode: whoami

**权限**: 所有用户可用

```
result = pf_whoami()
```

返回当前调用者的身份信息，包括 display_name、role、user_type 及 API key 数量摘要。

---

### Mode: list

**权限**: admin only

```
users = pf_list_users()
```

返回所有用户列表，含 id、display_name、user_type、role、email。
`id` 字段供后续 update / key-add / key-revoke 操作使用。

---

### Mode: invite

**权限**: admin only

**触发词**: "邀请用户" / "invite user" / "加人" / "add member"

顺序调用两个 MCP tool，一步完成用户创建和初始 key 发放：

```
# Step 1: 创建用户
user = pf_create_user(
  display_name=<required>,
  user_type=<"human"|"machine", default: "human">,
  role=<"writer"|"admin", default: "writer">,
  email=<optional>,
  author_aliases=<optional, array of strings>
)

# Step 2: 为新用户发放初始 API key
key = pf_create_api_key(
  user_id=<user.id from Step 1>,
  name=<optional, default: "initial">,
  project_scope=<optional, single project name (string)>
)
```

**输入字段**:

| 字段 | 必填 | 默认值 | 说明 |
|------|------|--------|------|
| display_name | yes | — | 用户显示名 |
| user_type | no | "human" | "human" 或 "machine" |
| role | no | "writer" | "writer" 或 "admin" |
| email | yes (human) / auto (machine) | — | human 用户必填；machine 用户自动生成 |
| key_name | no | "initial" | 初始 key 名称 |
| project_scope | no | — | 限制 key 可访问的单个 project（单个字符串；留空 = 全局） |
| author_aliases | no | — | git 提交作者别名，用于归因 |

**token 只展示一次**：`pf_create_api_key` 返回的 `token` 明文仅在此次响应中展示，请用户立即保存。

> invite vs key-add 区别：`invite` 同时创建新用户 + 发放首个 key；`key-add` 只为**已存在**用户补发额外 key，不创建用户。

---

### Mode: update

**权限**: admin only

**触发词**: "改用户" / "update user" / "修改角色" / "change role"

```
pf_update_user(
  id=<user_id>,
  display_name=<optional>,
  role=<optional>
)
```

**输入字段**:

| 字段 | 必填 | 说明 |
|------|------|------|
| id | yes | 用户 ID，通过 list mode 获取 |
| display_name | no | 新的显示名 |
| role | no | 新角色："writer" 或 "admin" |

**缺少 user_id 时**：提示用户先运行 list mode（"我需要先列出用户获取 ID，请确认是否执行？"），再执行 update。

---

### Mode: key-add

**权限**: admin only

**触发词**: "再发 key" / "issue another key" / "new key"

为已存在的用户补发额外 API key：

```
key = pf_create_api_key(
  user_id=<required>,
  name=<required>,
  project_scope=<optional>
)
```

**输入字段**:

| 字段 | 必填 | 说明 |
|------|------|------|
| user_id | yes | 目标用户 ID，通过 list mode 获取 |
| name | yes | key 名称，便于识别用途 |
| project_scope | no | 限制 key 可访问的单个 project（单个字符串；留空 = 全局） |

**token 只展示一次**：同 invite mode，请用户立即保存。

---

### Mode: key-revoke

**权限**: admin only

**触发词**: "撤销 key" / "revoke key" / "删除 key" / "吊销"

```
pf_revoke_api_key(
  user_id=<required>,
  key_id=<required>
)
```

**输入字段**:

| 字段 | 必填 | 说明 |
|------|------|------|
| user_id | yes | 目标用户 ID，通过 list mode 获取 |
| key_id | yes | 要撤销的 key ID；通过 whoami（自己的 key）或 list mode 返回的 `api_keys` 字段获取 |

撤销后 key 立即失效，无法恢复。

**缺少 key_id 时**：提示用户先运行 whoami（查自己的 key）或 list mode（查其他用户的 key）获取 key_id。

---

## Output

所有响应遵循三段式格式：

### whoami 示例

```markdown
## 结果
已获取当前用户身份信息。

## 状态
| 字段         | 值                  |
|--------------|---------------------|
| display_name | Alice               |
| role         | admin               |
| user_type    | human               |
| email        | alice@example.com   |
| api_keys     | 2 个（active）      |

## 下一步
- `/pf-user` list — 查看所有用户
- `/pf-user` invite — 邀请新成员
```

### invite 示例

```markdown
## 结果
用户 'Bob' 已创建，初始 API key 已发放。

## 状态
| 字段         | 值                                          |
|--------------|---------------------------------------------|
| user_id      | usr_abc123                                  |
| display_name | Bob                                         |
| role         | writer                                      |
| user_type    | human                                       |
| key_name     | initial                                     |
| token        | ak_xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx         |

⚠️ **请立即保存 token，此后无法再查看。**

## 下一步
- 将 token 通过安全渠道发送给 Bob
- 如需补发 key：`/pf-user` key-add
- 如需修改角色：`/pf-user` update
```

### list 示例

```markdown
## 结果
找到 3 个用户。

## 状态
| id          | display_name | type    | role   | email              |
|-------------|--------------|---------|--------|--------------------|
| usr_abc123  | Alice        | human   | admin  | alice@example.com  |
| usr_def456  | Bob          | human   | writer | bob@example.com    |
| usr_ghi789  | CI Agent     | machine | writer | —                  |

## 下一步
- `/pf-user` invite — 邀请新成员
- `/pf-user` update — 修改用户角色（需提供 id）
- `/pf-user` key-revoke — 撤销某用户的 key（需提供 user_id + key_id）
```

### update 示例

```markdown
## 结果
用户 'Bob' 已更新。

## 状态
| 字段         | 值         |
|--------------|------------|
| user_id      | usr_def456 |
| display_name | Bob        |
| role         | admin      |

## 下一步
- `/pf-user` list — 确认变更
- `/pf-user` key-revoke — 如需撤销该用户 key
```

### key-add 示例

```markdown
## 结果
API key 'laptop' 已为用户 'Bob' 创建。

## 状态
| 字段         | 值                                     |
|--------------|----------------------------------------|
| user_id      | usr_def456                             |
| key_id       | k_xxxxxxxx                             |
| name         | laptop                                 |
| project_scope| —                                      |
| token        | ak_xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx    |

⚠️ **请立即保存 token，此后无法再查看。**

## 下一步
- 将 token 通过安全渠道发送给 Bob
- `/pf-user` key-revoke — 如需撤销此 key
```

### key-revoke 示例

```markdown
## 结果
API key 'k_xxxxxxxx' 已撤销，立即失效。

## 状态
| 字段    | 值          |
|---------|-------------|
| user_id | usr_def456  |
| key_id  | k_xxxxxxxx  |
| status  | revoked ✓   |

## 下一步
- `/pf-user` key-add — 如需重新为该用户发放 key
- `/pf-user` list — 查看用户列表
```

## NL Triggers

| 触发词 | Mode |
|--------|------|
| "我是谁" / "whoami" / "查身份" / "my identity" | whoami |
| "列出用户" / "list users" / "有哪些用户" / "show users" | list |
| "邀请用户" / "invite user" / "加人" / "add member" | invite |
| "改用户" / "update user" / "修改角色" / "change role" | update |
| "再发 key" / "issue another key" / "new key" / "补发 key" | key-add |
| "撤销 key" / "revoke key" / "删除 key" / "吊销" | key-revoke |
