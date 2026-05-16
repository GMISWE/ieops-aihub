"""Conflict predictor — 5-rule v3.0 MVP per design.md §12.4.

Severity ladder: info < warn < soft_block < hard_block. Response.severity =
max over predictions, default 'info' when no predictions.

Rules:
  1. same_resource_live_write — same URI, both intent ∈ write/delete/refactor → soft_block
  2. lock_conflict           — proposed lock_key already held → hard_block
  3. same_repo_refactor      — same repo URI, both refactor → soft_block
  4. path_overlap            — glob/path overlap (SQL candidates + Python prune) → soft_block (both write) or warn
  5. external_artifact       — same work_item recent PR/branch event → warn
"""
from __future__ import annotations

import fnmatch
from typing import Any

import sqlalchemy as sa
from sqlalchemy.ext.asyncio import AsyncConnection


_SEV_RANK = {"info": 0, "warn": 1, "soft_block": 2, "hard_block": 3}
_WRITE_INTENTS = ("write", "delete", "refactor")


def _max_severity(predictions: list[dict]) -> str:
    if not predictions:
        return "info"
    return max(predictions, key=lambda p: _SEV_RANK[p["severity"]])["severity"]


# ---------------------------------------------------------------------------
# Path glob overlap helper (Rule 4)
# ---------------------------------------------------------------------------

def path_glob_overlap(a: str, b: str) -> bool:
    """Return True if file URI a and b refer to overlapping path scopes.

    Cases:
      - exact match (same uri)
      - one contains the other as parent prefix
      - glob in one matches the other (fnmatch.fnmatchcase)
      - both globs share an unambiguous prefix (str-prefix on the leading
        non-wildcard portion)
    Assumes URIs start with `file:` and use forward slashes.
    """
    if a == b:
        return True
    # Strip 'file:' prefix
    pa = a[5:] if a.startswith("file:") else a
    pb = b[5:] if b.startswith("file:") else b

    def _has_glob(p: str) -> bool:
        return any(c in p for c in "*?[")

    def _matches_or_descendant(glob: str, path: str) -> bool:
        if fnmatch.fnmatchcase(path, glob):
            return True
        # Descendant check: 'src/auth/**' should match 'src/auth/login.py'
        if glob.endswith("/**"):
            prefix = glob[:-3]  # drop '/**'
            return path == prefix or path.startswith(prefix + "/")
        if glob.endswith("/*"):
            prefix = glob[:-2]
            return path == prefix or (path.startswith(prefix + "/") and "/" not in path[len(prefix) + 1:])
        return False

    ga, gb = _has_glob(pa), _has_glob(pb)
    if ga and not gb:
        return _matches_or_descendant(pa, pb)
    if gb and not ga:
        return _matches_or_descendant(pb, pa)
    if ga and gb:
        # both globs — share leading non-wildcard prefix?
        def _stem(p: str) -> str:
            out = []
            for ch in p:
                if ch in "*?[":
                    break
                out.append(ch)
            return "".join(out).rstrip("/")
        sa_, sb_ = _stem(pa), _stem(pb)
        if not sa_ or not sb_:
            # both wildcards from root → assume overlap
            return True
        return sa_.startswith(sb_) or sb_.startswith(sa_)
    # Both literal paths
    return pa.startswith(pb + "/") or pb.startswith(pa + "/")


# ---------------------------------------------------------------------------
# Main predictor
# ---------------------------------------------------------------------------

async def predict_conflicts(
    conn: AsyncConnection,
    *,
    project: str,
    declared_resources: list[dict],
    work_item_id: str | None = None,
    attempt_id: str | None = None,
) -> dict:
    predictions: list[dict] = []

    # ---- Rule 1: same_resource_live_write ----
    write_uris = [r["uri"] for r in declared_resources
                  if r.get("intent") in _WRITE_INTENTS]
    proposed_writes_by_uri = {r["uri"]: r.get("intent")
                              for r in declared_resources
                              if r.get("intent") in _WRITE_INTENTS}
    if write_uris:
        rows = (await conn.execute(sa.text("""
            SELECT wi.id AS wi, ra.id AS ra, ra.actor_user_id, ra.actor_display,
                   r->>'uri' AS uri, r->>'intent' AS their_intent
            FROM work_items wi
            JOIN run_attempts ra ON ra.id = wi.current_attempt_id
            JOIN jsonb_array_elements(wi.declared_resources) r ON true
            WHERE wi.project = :proj
              AND wi.id <> COALESCE(:wi, '')
              AND wi.status = 'running' AND ra.status = 'running'
              AND ra.lease_until > now()
              AND r->>'uri' = ANY(CAST(:uris AS text[]))
              AND r->>'intent' IN ('write','delete','refactor')
        """), {"proj": project, "wi": work_item_id or "",
               "uris": write_uris})).mappings().all()
        for r in rows:
            predictions.append({
                "rule_id": "same_resource_live_write",
                "severity": "soft_block",
                "resource_uri": r["uri"],
                "conflicts_with": {
                    "work_item_id": r["wi"], "attempt_id": r["ra"],
                    "actor_user_id": r["actor_user_id"],
                    "actor_display": r["actor_display"],
                },
                "message": (
                    f"{r['actor_display']}的 agent ({r['ra']}) 已声明 "
                    f"{r['their_intent']} {r['uri']} 且 attempt 活跃"
                ),
            })

    # ---- Rule 2: lock_conflict (hard_block) ----
    # We treat declared_resources type=repo + intent=write as implicitly
    # holding a git_branch + worktree lock-key candidate. But since clients
    # don't pass lock_keys to /predict in v3.0 (per §12.5), we only check
    # against resource_locks if requested_locks were carried as metadata —
    # for the predict route we synthesize from repo declarations. Phase 1A
    # keeps it minimal: scan resource_locks where resource_key derives from
    # declared repo URIs.
    proposed_lock_keys: list[tuple[str, str]] = []
    for r in declared_resources:
        if r.get("type") == "repo" and r.get("intent") in _WRITE_INTENTS:
            task_branch = r.get("task_branch")
            uri = r.get("uri", "")
            # repo:marketplace → marketplace
            repo_short = uri[5:] if uri.startswith("repo:") else uri
            if task_branch:
                proposed_lock_keys.append(("git_branch", f"{repo_short}/{task_branch}"))
    if proposed_lock_keys:
        types = [t for t, _ in proposed_lock_keys]
        keys = [k for _, k in proposed_lock_keys]
        rows = (await conn.execute(sa.text("""
            SELECT rl.resource_type, rl.resource_key,
                   ra.id AS owner_attempt, ra.actor_user_id, ra.actor_display,
                   ra.work_item_id
            FROM resource_locks rl
            JOIN run_attempts ra ON ra.id = rl.owner_attempt_id
            JOIN unnest(CAST(:types AS text[]), CAST(:keys AS text[]))
                AS req(rt, rk) ON rl.resource_type = req.rt AND rl.resource_key = req.rk
            WHERE ra.status = 'running' AND ra.lease_until > now()
              AND ra.id <> COALESCE(:exclude_aid, '')
        """), {"types": types, "keys": keys,
               "exclude_aid": attempt_id or ""})).mappings().all()
        for r in rows:
            predictions.append({
                "rule_id": "lock_conflict",
                "severity": "hard_block",
                "resource_uri": f"{r['resource_type']}:{r['resource_key']}",
                "conflicts_with": {
                    "work_item_id": r["work_item_id"], "attempt_id": r["owner_attempt"],
                    "actor_user_id": r["actor_user_id"],
                    "actor_display": r["actor_display"],
                },
                "message": (
                    f"锁 {r['resource_type']}:{r['resource_key']} 已被 "
                    f"{r['actor_display']} 持有"
                ),
            })

    # ---- Rule 3: same_repo_refactor (soft_block) ----
    refactor_repo_uris = [r["uri"] for r in declared_resources
                          if r.get("type") == "repo" and r.get("intent") == "refactor"]
    if refactor_repo_uris:
        rows = (await conn.execute(sa.text("""
            SELECT wi.id AS wi, ra.id AS ra, ra.actor_user_id, ra.actor_display,
                   r->>'uri' AS repo_uri
            FROM work_items wi
            JOIN run_attempts ra ON ra.id = wi.current_attempt_id
            JOIN jsonb_array_elements(wi.declared_resources) r ON true
            WHERE wi.project = :proj
              AND wi.id <> COALESCE(:wi, '')
              AND wi.status = 'running' AND ra.status = 'running'
              AND ra.lease_until > now()
              AND r->>'type' = 'repo' AND r->>'intent' = 'refactor'
              AND r->>'uri' = ANY(CAST(:uris AS text[]))
        """), {"proj": project, "wi": work_item_id or "",
               "uris": refactor_repo_uris})).mappings().all()
        # Dedupe against rule 1 to avoid double-counting (same resource_uri
        # already flagged by rule 1).
        existing_uris = {p["resource_uri"] for p in predictions}
        for r in rows:
            if r["repo_uri"] in existing_uris:
                continue
            predictions.append({
                "rule_id": "same_repo_refactor",
                "severity": "soft_block",
                "resource_uri": r["repo_uri"],
                "conflicts_with": {
                    "work_item_id": r["wi"], "attempt_id": r["ra"],
                    "actor_user_id": r["actor_user_id"],
                    "actor_display": r["actor_display"],
                },
                "message": (
                    f"{r['actor_display']}的 agent 正在 refactor 同一 repo {r['repo_uri']}"
                ),
            })

    # ---- Rule 4: path_overlap ----
    # Pull candidates (all path-type declared resources from other live work_items),
    # then prune in Python via path_glob_overlap.
    proposed_path_uris = [r for r in declared_resources if r.get("type") == "path"]
    if proposed_path_uris:
        rows = (await conn.execute(sa.text("""
            SELECT wi.id AS wi, ra.id AS ra, ra.actor_user_id, ra.actor_display,
                   r->>'uri' AS their_uri, r->>'intent' AS their_intent
            FROM work_items wi
            JOIN run_attempts ra ON ra.id = wi.current_attempt_id
            JOIN jsonb_array_elements(wi.declared_resources) r ON true
            WHERE wi.project = :proj
              AND wi.id <> COALESCE(:wi, '')
              AND wi.status = 'running' AND ra.status = 'running'
              AND ra.lease_until > now()
              AND r->>'type' = 'path'
        """), {"proj": project, "wi": work_item_id or ""})).mappings().all()
        existing_keys = {(p["resource_uri"], p["conflicts_with"]["attempt_id"])
                         for p in predictions}
        for proposed in proposed_path_uris:
            for r in rows:
                if not path_glob_overlap(proposed["uri"], r["their_uri"]):
                    continue
                key = (proposed["uri"], r["ra"])
                if key in existing_keys:
                    continue
                # Severity: both write/delete/refactor → soft_block, else warn
                proposed_intent = proposed.get("intent")
                their_intent = r["their_intent"]
                both_write = (
                    proposed_intent in _WRITE_INTENTS
                    and their_intent in _WRITE_INTENTS
                )
                severity = "soft_block" if both_write else "warn"
                predictions.append({
                    "rule_id": "path_overlap",
                    "severity": severity,
                    "resource_uri": proposed["uri"],
                    "conflicts_with": {
                        "work_item_id": r["wi"], "attempt_id": r["ra"],
                        "actor_user_id": r["actor_user_id"],
                        "actor_display": r["actor_display"],
                    },
                    "message": (
                        f"{r['actor_display']}的 agent 路径 {r['their_uri']} "
                        f"与 {proposed['uri']} 重叠 (intent={their_intent})"
                    ),
                })
                existing_keys.add(key)

    # ---- Rule 5: external_artifact (warn) ----
    if work_item_id is not None:
        rows = (await conn.execute(sa.text("""
            SELECT id, event_type, payload, created_at FROM agent_events
            WHERE work_item_id = :wid
              AND event_type IN ('pr_opened', 'branch_force_pushed')
              AND created_at > now() - interval '30 days'
            LIMIT 5
        """), {"wid": work_item_id})).mappings().all()
        for r in rows:
            predictions.append({
                "rule_id": "external_artifact",
                "severity": "warn",
                "resource_uri": f"event:{r['event_type']}",
                "conflicts_with": {
                    "work_item_id": work_item_id, "attempt_id": "",
                    "actor_user_id": "", "actor_display": "",
                },
                "message": (
                    f"该 work_item 30天内已有 {r['event_type']} 事件, "
                    "建议先 /pf3-reconcile"
                ),
            })

    severity = _max_severity(predictions)
    return {"severity": severity, "predictions": predictions}
