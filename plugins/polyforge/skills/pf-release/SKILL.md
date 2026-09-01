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
   // No fields="brief" here (aihub#313): brief drops `attrs`, and `attrs.released_at` is
   // the entire reason for this call. It would return successfully and yield null.

   // List all wi's wrapped since the last release (or all wrapped if first release)
   if last_release_at:
       candidate_wis = pf_list_work_items(
         project=<current>,
         status="wrapped",
         since=last_release_at,
         limit=50
       )
   else:
       candidate_wis = pf_list_work_items(
         project=<current>,
         status="wrapped",
         limit=50
       )
   ```

   > **aihub#280 fixed `status`, and did NOT fix the release scope.** Be precise
   > about which is which:
   >
   > - **Fixed:** `status` must be a string. `status=["wrapped"]` was dropped by
   >   the MCP layer, so the status filter was not applying at all. Corrected above.
   > - **Fixed:** `since=` reaches the server now instead of being discarded.
   >
   > 🔴 **Still wrong, and now wrong in the more dangerous direction.** `since`
   > filters `wi.created_at`, not `closed_at`. So `status=wrapped&since=T` means
   > "created after T and currently wrapped" — **a wi created before the last
   > release and wrapped since it is silently excluded from the release notes.**
   >
   > Before aihub#280 the discarded `since` made this over-inclusive (the most
   > recent 50 wrapped), and a human confirming the list could see the extras.
   > Now it is under-inclusive, and a short list looks exactly like a correct one.
   > **Do not treat this call as "wrapped since the last release" — it is not.**
   > Expressing that set needs `closed_at` (stamped by `trg_wi_closed_at`, already
   > reachable via `sort=closed_at`) exposed as a `closed_since` filter, which no
   > param provides yet. That belongs to **aihub#176** along with everything else
   > on this page.
   >
   > Latent today: the `if last_release_at` branch is unreachable anyway, because
   > `methodology.release` is rejected by `pf_remember` *and* absent from
   > `MethodologyTypeEnum`, so step 5 can never write the record step 1 reads.
   > This always takes the `else` branch. Recorded rather than left as another
   > undocumented dead path.

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
     status="wrapped",
     limit=10
   )
   ```

   > ⚠️ **This returns 0 rows today, and that is the honest answer.** As of
   > aihub#280 the server honours `scenario=` (it used to discard it, which is
   > why this call previously listed *every* wi in the project and looked like it
   > worked). But `work_items.scenario` is constrained to `coding|writing|data`
   > and `CreateWorkItem` rejects everything except `coding`, so no work item can
   > hold `scenario="release"` — and the only documented producers of release
   > wis, `pf_cut_alpha` and `pf_promote`, are still 405 stubs. Deciding whether
   > "release" becomes a scenario (needs a migration) or a `wi_type` (needs none
   > — `validWIKinds` already lists it) belongs to **aihub#176**; do not paper
   > over it by dropping the filter.

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

> ⚠️ Same caveat as `promote` above: `scenario=` is honoured since aihub#280, but
> no row can hold `scenario="release"`, so this lists nothing until **aihub#176**
> makes release work items real. Previously it silently listed the project's
> entire backlog and presented it as the release history.

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
