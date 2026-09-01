## Three-Segment Output Format (mandatory for all skills)

Every polyforge skill response MUST follow this format exactly. Violations = bug.

```markdown
## Result
<1-2 sentences, verb-first. State errors here explicitly.>

## Status
| field | value |
|---|---|
| wi | <project#seq> |
| goal | <truncated to 60 chars> |
| status | running |
| owner | you (ra_8d2E4F1a) |
| locks | git_branch:polyforge/wi-xxx |
| blocked | — |
| step | 2/4 review |
| expires | 28min |

## Next steps
- `/pf-spec` — write spec, AI guides scope definition
- `/pf-stop --pause` — pause and release locks
(max 5 items; write _none_ if no actions available)
```

For multi-wi lists, replace the status table with columns: `id / wi_type / priority / goal / status / owner_display`.
