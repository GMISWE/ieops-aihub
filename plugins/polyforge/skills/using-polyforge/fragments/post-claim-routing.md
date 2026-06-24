## Post-claim 下一步 Routing

When a `requires_human_session=true` wi is being viewed or operated on — any 三段式
output that lists "下一步" (post-claim via `/pf-work`, status view via `/pf-status`,
mid-flow output from `/pf-spec`, `/pf-plan`, `/pf-retro`, `/pf-stop`, etc.) — the
"下一步" section is **mechanically populated** from the table below. No LLM judgment,
no session-context inference. This rule applies to **all skills** emitting 三段式
output when the user is operating inside a claimed wi.

### Source of `wi_type` (CRITICAL)

`wi_type` MUST be read from one of:

- the `wi_type` field in the `pf_claim_work_item` response (preferred — just returned)
- the `wi_type` field in `pf_get_work_item` (canonical, always current)
- the `wi_type` field shown in the wi detail page

**FORBIDDEN**: inferring `wi_type` from the wi's `goal` text, slug name, labels, parent
type, or recent session context. A wi with `goal` starting `"feat:"`, `"fix:"`, or
`"release"` may still have a completely different `wi_type` — for instance, a `chore`
that edits a release scenario file has `wi_type=chore` even though its goal mentions
"release". Treat the API field as the only authority.

### Routing table

| wi_type | primary 下一步 | alternates |
|---|---|---|
| `chore` | `/pf-execute` — iterate step graph directly | `/pf-stop --pause` |
| `fix_bug` | `/pf-execute` — start from prepare_context | `/pf-spec --debug` (if root cause unclear); `/pf-stop --pause` |
| `feature` | `/pf-execute` — start from prepare_context | `/pf-spec` (if scope unclear); `/pf-plan` (if approach contested); `/pf-stop --pause` |
| `critical_bug` | `/pf-spec --debug` — root cause first | `/pf-execute`; `/pf-stop --pause` |
| `release` | `/pf-release` (or `/pf-execute` against release graph) | `/pf-stop --pause` |
| _(no match — default)_ | `/pf-spec` — define scope before acting | `/pf-stop --pause` |

### Mandatory output rules for "下一步"

1. The **first** item under "下一步" MUST be the table's `primary` cell for the matched
   `wi_type`, copied verbatim (slash-command + its description).
2. **No suggestion may appear before the primary** — not `/pf-spec`, not `/pf-plan`,
   not any LLM-improvised option. The primary is always row 1.
3. Alternates from the table follow the primary as rows 2..N, in the order listed in
   the table cell, one per row.
4. Suggestions outside the table (e.g., `wi.content` explicitly invites a different
   skill, or memory recall surfaces a relevant procedure) may be appended **AFTER** all
   table-derived rows, each marked `_(from wi.content)_` or `_(from memory <mem_id>)_`.
   This is the **only** legitimate way to add to the list.
5. If the `wi_type` does not match any row, use the `_(no match — default)_` row.

### Worked example

A wi has `wi_type=chore`, `requires_human_session=true`, goal `"feat: add CI hook for
descriptions"`. Despite the misleading "feat:" prefix in the goal, the API field says
`chore`. The "下一步" output — from `/pf-work` post-claim, from `/pf-status`, or from
any other 三段式 skill — is:

```
## 下一步
- `/pf-execute` — iterate step graph directly
- `/pf-stop --pause`
```

That is the entire "下一步" — primary on row 1, the sole alternate on row 2, nothing
inserted before. If `wi.content` happened to invite `/pf-spec`, it would appear on
row 3 marked `_(from wi.content)_`, never on row 1.

### Revision after review

When a human reviewer has added section annotations to a spec or plan artifact in the /ui
viewer, the "下一步" for the reviewing session should include `/pf-revise`. This applies
in any skill output (三段式) after a spec/plan is saved and sent for review:

```
## 下一步
- ...  (primary from routing table above)
- `/pf-revise` — if reviewer has annotated the spec/plan, run this to apply the feedback
                  and resolve all open annotations in one round
```

`/pf-revise` is an **alternate** suggestion — never the primary — unless `wi.content`
explicitly invites it. After `/pf-revise` completes, the reviewer may annotate the NEW
head version for another round.

### Why this table exists

`/pf-execute` is itself data-driven over the wi's scenario step graph, so it is the
universal correct entry for any wi_type that has a graph. The table only encodes
exceptions (`release`, `critical_bug`, `default`). `/pf-spec` and `/pf-plan` are
escape valves — listed as alternates, never as primary for `chore`/`fix_bug`/`feature`,
because their scenario step graphs start with code-side steps, not a spec discussion.

The `rhs=false` auto-dispatch path (which calls `/pf-execute` as a subagent without
emitting 三段式) is unaffected by this table — it is already correct by construction.
