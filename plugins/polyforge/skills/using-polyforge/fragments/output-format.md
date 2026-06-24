## Three-Segment Output Format (mandatory for all skills)

Every polyforge skill response MUST follow this format exactly. Violations = bug.

```markdown
## 结果
<1-2 sentences, verb-first. State errors here explicitly.>

## 状态
| 字段    | 值                                          |
|---------|---------------------------------------------|
| wi      | <project#seq>                               |
| goal    | <truncated to 60 chars>                     |
| status  | running                                     |
| owner   | you (ra_8d2E4F1a)                           |
| locks   | git_branch:polyforge/wi-xxx                 |
| blocked | —                                           |
| step    | 2/4 review                                  |
| expires | 28min                                       |

## 下一步
- `/pf-spec` — write spec, AI guides scope definition
- `/pf-stop --pause` — pause and release locks
(max 5 items; write _none_ if no actions available)
```

**owner field rules:**
- You hold it: `you (ra_8d2E4F1a)`
- Someone else: `<actor_display> (ra_8d2E4F1a)` (from `pf_list_work_items` response `owner.display`)
- Machine user: `Alice Agent Fleet (machine) (ra_8d2E4F1a)`
- No owner (queued/blocked): `—`

For multi-wi lists, replace the status table with columns: `id / wi_type / priority / goal / status / owner_display`.
