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

- "who am I" / "whoami" / "check my identity" / "my identity" → **whoami mode**
- "list users" / "show all users" / "which users exist" → **list mode**
- "invite user" / "invite a user" / "add a person" / "add member" → **invite mode**
- "edit user" / "update user" / "change role" → **update mode**
- "issue another key" / "new key" → **key-add mode**
- "revoke key" / "delete key" / "revoke" → **key-revoke mode**

## Mechanic

### Mode: whoami

**Permissions**: available to all users

```
result = pf_whoami()
```

Returns the current caller's identity information, including display_name, role, user_type, and a summary of the API key count.

---

### Mode: list

**Permissions**: admin only

```
users = pf_list_users()
```

Returns the list of all users, including id, display_name, user_type, role, and email.
The `id` field is used for subsequent update / key-add / key-revoke operations.

---

### Mode: invite

**Permissions**: admin only

**NL Triggers**: "invite user" / "invite a user" / "add a person" / "add member"

Call two MCP tools in sequence to create the user and issue the initial key in one step:

```
# Step 1: create the user
user = pf_create_user(
  display_name=<required>,
  user_type=<"human"|"machine", default: "human">,
  role=<"writer"|"admin", default: "writer">,
  email=<optional>,
  author_aliases=<optional, array of strings>
)

# Step 2: issue the initial API key for the new user
key = pf_create_api_key(
  user_id=<user.id from Step 1>,
  name=<optional, default: "initial">,
  project_scope=<optional, single project name (string)>
)
```

**Input fields**:

| field | required | default | description |
|------|------|--------|------|
| display_name | yes | — | user display name |
| user_type | no | "human" | "human" or "machine" |
| role | no | "writer" | "writer" or "admin" |
| email | yes (human) / auto (machine) | — | required for human users; auto-generated for machine users |
| key_name | no | "initial" | initial key name |
| project_scope | no | — | restrict the key to a single project it can access (single string; leave empty = global) |
| author_aliases | no | — | git commit author aliases, used for attribution |

**Token is shown only once**: the plaintext `token` returned by `pf_create_api_key` is displayed only in this response — ask the user to save it immediately.

> invite vs key-add difference: `invite` creates a new user AND issues the first key; `key-add` only issues an extra key for an **existing** user and does not create a user.

---

### Mode: update

**Permissions**: admin only

**NL Triggers**: "edit user" / "update user" / "change role"

```
pf_update_user(
  id=<user_id>,
  display_name=<optional>,
  role=<optional>
)
```

**Input fields**:

| field | required | description |
|------|------|------|
| id | yes | user ID, obtained via list mode |
| display_name | no | new display name |
| role | no | new role: "writer" or "admin" |

**When user_id is missing**: prompt the user to run list mode first ("I need to list users first to get the ID — please confirm whether to proceed?"), then perform the update.

---

### Mode: key-add

**Permissions**: admin only

**NL Triggers**: "issue another key" / "new key"

Issue an extra API key for an existing user:

```
key = pf_create_api_key(
  user_id=<required>,
  name=<required>,
  project_scope=<optional>
)
```

**Input fields**:

| field | required | description |
|------|------|------|
| user_id | yes | target user ID, obtained via list mode |
| name | yes | key name, to make its purpose easy to identify |
| project_scope | no | restrict the key to a single project it can access (single string; leave empty = global) |

**Token is shown only once**: same as invite mode — ask the user to save it immediately.

---

### Mode: key-revoke

**Permissions**: admin only

**NL Triggers**: "revoke key" / "delete key" / "revoke"

```
pf_revoke_api_key(
  user_id=<required>,
  key_id=<required>
)
```

**Input fields**:

| field | required | description |
|------|------|------|
| user_id | yes | target user ID, obtained via list mode |
| key_id | yes | the key ID to revoke; obtained via whoami (your own key) or the `api_keys` field returned by list mode |

Once revoked, the key is invalidated immediately and cannot be restored.

**When key_id is missing**: prompt the user to run whoami first (to look up their own key) or list mode (to look up another user's key) to obtain the key_id.

---

## Output

All responses follow the three-segment format:

### whoami example

```markdown
## Result
Current user identity information has been retrieved.

## Status
| field        | value               |
|--------------|---------------------|
| display_name | Alice               |
| role         | admin               |
| user_type    | human               |
| email        | alice@example.com   |
| api_keys     | 2 (active)          |

## Next steps
- `/pf-user` list — view all users
- `/pf-user` invite — invite a new member
```

### invite example

```markdown
## Result
User 'Bob' has been created, and the initial API key has been issued.

## Status
| field        | value                                       |
|--------------|---------------------------------------------|
| user_id      | usr_abc123                                  |
| display_name | Bob                                         |
| role         | writer                                      |
| user_type    | human                                       |
| key_name     | initial                                     |
| token        | ak_xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx         |

⚠️ **Save the token immediately — it cannot be viewed again afterward.**

## Next steps
- Send the token to Bob through a secure channel
- To issue another key: `/pf-user` key-add
- To change the role: `/pf-user` update
```

### list example

```markdown
## Result
Found 3 users.

## Status
| id          | display_name | type    | role   | email              |
|-------------|--------------|---------|--------|--------------------|
| usr_abc123  | Alice        | human   | admin  | alice@example.com  |
| usr_def456  | Bob          | human   | writer | bob@example.com    |
| usr_ghi789  | CI Agent     | machine | writer | —                  |

## Next steps
- `/pf-user` invite — invite a new member
- `/pf-user` update — change a user's role (requires id)
- `/pf-user` key-revoke — revoke a user's key (requires user_id + key_id)
```

### update example

```markdown
## Result
User 'Bob' has been updated.

## Status
| field        | value      |
|--------------|------------|
| user_id      | usr_def456 |
| display_name | Bob        |
| role         | admin      |

## Next steps
- `/pf-user` list — confirm the change
- `/pf-user` key-revoke — to revoke this user's key
```

### key-add example

```markdown
## Result
API key 'laptop' has been created for user 'Bob'.

## Status
| field        | value                                  |
|--------------|----------------------------------------|
| user_id      | usr_def456                             |
| key_id       | k_xxxxxxxx                             |
| name         | laptop                                 |
| project_scope| —                                      |
| token        | ak_xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx    |

⚠️ **Save the token immediately — it cannot be viewed again afterward.**

## Next steps
- Send the token to Bob through a secure channel
- `/pf-user` key-revoke — to revoke this key
```

### key-revoke example

```markdown
## Result
API key 'k_xxxxxxxx' has been revoked and is invalidated immediately.

## Status
| field   | value       |
|---------|-------------|
| user_id | usr_def456  |
| key_id  | k_xxxxxxxx  |
| status  | revoked ✓   |

## Next steps
- `/pf-user` key-add — to issue a new key for this user again
- `/pf-user` list — view the user list
```

## NL Triggers

| NL Triggers | Mode |
|--------|------|
| "who am I" / "whoami" / "check my identity" / "my identity" | whoami |
| "list users" / "show all users" / "which users exist" / "show users" | list |
| "invite user" / "invite a user" / "add a person" / "add member" | invite |
| "edit user" / "update user" / "change role" | update |
| "issue another key" / "new key" / "issue an extra key" | key-add |
| "revoke key" / "delete key" / "revoke" | key-revoke |
