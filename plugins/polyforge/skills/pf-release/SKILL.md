---
name: pf-release
description: >
  Use when the team is ready to cut a versioned alpha release, promote an alpha
  channel to stable, or check the current release state.
---

# pf-release — Release Management

## Usage

**Purpose**: Cut an alpha release (tag + manifest of wrapped wi's since last release) or promote an existing alpha to stable.

**Pattern**: `/pf-release { alpha | promote }`

**Required**: a sub-mode (see below)

**Flags**:
- `alpha` shows current release state then cuts a new tag + manifest
- `promote` is destructive: rewrites the stable channel pointer to a chosen alpha

## When to use

When the team is ready to cut a versioned release or promote a previously-cut alpha
to a stable channel.

## Mechanic

### `/pf-release alpha` — Cut alpha release

1. Show current release state — determine scope from last release timestamp:
   ```
   // Look up the most recent release record to get the last_release_at baseline
   last_release_records = pf_recall(
     project=<current>,
     type="methodology.release",
     limit=1
   )
   last_release_at = last_release_records[0].attrs.released_at if last_release_records else null

   // List all wi's wrapped since the last release (or all wrapped if first release)
   if last_release_at:
       candidate_wis = pf_list_work_items(
         project=<current>,
         status=["wrapped"],
         since=last_release_at,
         limit=50
       )
   else:
       candidate_wis = pf_list_work_items(
         project=<current>,
         status=["wrapped"],
         limit=50
       )
   ```

2. Confirm release scope with the user: "These N wi's will be included in <version>."

3. Cut alpha:
   ```
   pf_cut_alpha(
     project=<current>,
     version=<version string, e.g. "v1.0.0-alpha.1">,
     tag_message="Release <version>: <brief description>",
     included_wi_ids=[<list of wrapped wi ids>]
   )
   ```

   Server actions (atomic):
   - Creates a `scenario=release` work item with the release manifest
   - Tags each repo at HEAD with the version tag
   - Emits `alpha_cut` event with manifest (wi ids, commit SHAs, tag names)
   - Returns `{release_wi_id, tags: [{repo, tag, sha}]}`

4. Emit release note:
   ```
   pf_emit_event(
     work_item_id=<release_wi_id>,
     event_type="note",
     payload={
       text: "Alpha <version> cut",
       manifest: {repos: [...], included_wis: [...]}
     }
   )
   ```

5. Record release timestamp as baseline for the next release:
   ```
   pf_remember(
     project=<current>,
     type="methodology.release",
     body="Release <version> cut at <current_timestamp>. Included <N> wi's.",
     visibility="team",
     dedup_mode="off",
     attrs={
       "version": <version string>,
       "released_at": <current ISO-8601 timestamp>,
       "included_wi_count": <N>,
       "release_wi_id": <release_wi_id>
     }
   )
   ```

6. Output three-segment format with tag URLs and included wi summary.

---

### `/pf-release promote` — Promote alpha to stable

1. Show available alpha releases:
   ```
   pf_list_work_items(
     project=<current>,
     scenario="release",
     label="alpha",
     status=["wrapped"],
     limit=10
   )
   ```

2. User selects the alpha to promote.

3. Confirm promotion: "Promote <alpha_version> → <stable_version>?"

4. Promote:
   ```
   pf_promote(
     source_release_wi_id=<alpha_wi_id>,
     from_channel="alpha",
     to_channel="stable",
     new_version=<stable_version string, e.g. "v1.0.0">
   )
   ```

   Server actions:
   - Creates a new `scenario=release` work item pointing to the alpha (via `parent_work_item_id`)
   - Tags each repo at the alpha's original commits with the new stable tag
   - Emits `promoted_to_stable` event
   - Returns `{release_wi_id, tags: [{repo, tag, sha}]}`

5. Output three-segment format with stable tag URLs.

---

### `/pf-release status` — Show release state

```
pf_list_work_items(
  project=<current>,
  scenario="release",
  limit=5
)
```

Display recent releases with status, version, included wi count, and tags.

## Output Format

```
## Result
Alpha v1.0.0-alpha.1 cut successfully. 3 repos tagged.

## Status
| version | v1.0.0-alpha.1                   |
| channel | alpha                             |
| repos   | aihub @ abc1234, marketplace @ def5678, ieops-v2 @ ghi9012 |
| wi's    | 12 wrapped wi's included          |
| release wi | marketplace#42               |

## Next steps
- Monitor staging with the alpha build
- `/pf-release promote` when ready for stable
```

## NL Triggers

- "release" / "release" / "cut release" / "cut a version"
- "cut alpha" / "cut alpha" / "alpha release"
- "promote" / "promote to stable" / "promote alpha to stable"
- "release status" / "current version"
