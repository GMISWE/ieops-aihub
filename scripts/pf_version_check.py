#!/usr/bin/env python3
# pf_version_check.py - assert the vendored plugin's version stamps agree, and that a
# change to the plugin's contents comes with a version bump.
#
# TWO CHECKS, DELIBERATELY SEPARATE (aihub#302)
# --------------------------------------------
#   check_root()  a STATE predicate: do the five stamps agree with each other RIGHT NOW?
#                 Reports [VERSION_DRIFT] / [CATALOG_REVISION_DRIFT] / ...
#   check_bump()  a CHANGE predicate: did this PR touch the plugin without moving the
#                 version? Reports [NO_VERSION_BUMP].
#
# They are complementary and cannot substitute for each other, which is exactly how
# aihub#301's defect recurred twice in half an hour. PR#264 and PR#265 each edited files
# under plugins/polyforge/skills/ (PR#264 including fragments/memory-first.md, which is in
# the resident session payload) and moved no stamp. check_root passed both times and was
# right to: all five stamps read the same value. It reads a cross-section; the defect lives
# on the timeline. Keep the two error tags distinct so a red build says which one fired.
#
# Why this exists (aihub#232)
# ---------------------------
# A plugin's version is recorded in five places: two marketplace catalogs plus
# three per-plugin manifests. Nothing kept them in sync, so cutting 1.1.7 updated
# only .claude-plugin/plugin.json and left plugin.json, .codex-plugin/plugin.json
# and the repo-root marketplace.json at 1.1.6. Claude Code reads
# .claude-plugin/plugin.json and was therefore unaffected, while the codex and
# copilot distribution paths self-reported a version matching neither the catalog
# nor the shipped content. Because it is a metadata error and not a functional
# one, no test and no user-visible behaviour exposed the drift. This check turns
# that class of error red.
#
# The two catalogs are both live and are read by different harnesses:
#   .claude-plugin/marketplace.json   Claude Code / the plugin marketplace
#   marketplace.json (repo root)      GitHub Copilot CLI - see
#                                     plugins/polyforge/skills/using-polyforge/
#                                     references/copilot-tools.md
# Only the former carries catalog_revision.
#
# What is checked, per plugin named in either catalog:
#
#   version           required in every catalog entry and in every manifest that
#                     exists; all of them must be equal.
#
#   catalog_revision  checked only if at least one place carries it. When in use
#                     it must be present in BOTH the .claude-plugin catalog entry
#                     and .claude-plugin/plugin.json, and all carriers must
#                     agree. The root catalog is exempt: it has no such field by
#                     design.
#
# WHAT catalog_revision IS, AND WHAT IT IS NOT (corrected by aihub#302)
# --------------------------------------------------------------------
# VERSION is the update signal. catalog_revision is INERT.
#
# This header used to assert the opposite - "this field, not version, is the signal
# /plugin install uses to detect a new build" - and cited team memory mem_7yldi6xb for it.
# Both were wrong. The corrected memory is mem_zZ3xWv4g; mem_7yldi6xb is its archived
# predecessor and STILL RETURNS THE WRONG TEXT VERBATIM when fetched by id (pf_recall hides
# it because it is archived, pf_get_memory does not). So cite the new id, and do not
# restore the claim from the old one. Three independent confirmations:
#
#   1. `claude plugin validate` says verbatim:
#        Unknown field 'catalog_revision'. Claude Code ignores it at load time.
#      once on the plugin manifest and twice on the marketplace.
#   2. The install cache is keyed on VERSION: installPath is
#        <cache>/<marketplace>/polyforge/<version>
#      so a new cache entry appears when version changes, and only then.
#   3. Restamping catalog_revision alone therefore ships a release that reaches nobody -
#      silently, because /plugin update is a no-op when the version is unchanged.
#
# How the wrong claim survived: commit dd715cb's body asserted it, but the same commit
# also moved version 1.1.7 -> 1.1.8. The release reached users because of the version; the
# catalog_revision half was inert. The verdict was right, so nobody re-read the reason.
#
# The field is KEPT anyway, for two reasons that do not depend on anything reading it:
# the all-or-nothing rule below uses it to hold its two carriers consistent, and the
# convention is to change it alongside version. Do not describe it as "recomputable" from
# plugin content either: a release commit edits files inside plugins/polyforge/, so
# stamping it changes the very tree the value would be derived from (measured: the stamped
# value and `git rev-parse HEAD:plugins/polyforge` disagree on main today, harmlessly).
#
# Scope: check_root asserts the stamps AGREE with each other. "Content changed but nobody
# bumped" is check_bump, below - added by aihub#302 after that failure recurred twice.
#
# Usage
# -----
#   pf_version_check.py [--root <repo-root>] [--self-test]
#   pf_version_check.py --require-bump-since <base-sha> [--root <repo-root>]
#
# Output: one line per problem, exit 1 if there are any.

import argparse
import json
import os
import sys
import tempfile

# Ordered most-canonical first. The first entry is what Claude Code actually
# loads, which is why its absence is itself a problem.
MANIFEST_RELPATHS = [
    ".claude-plugin/plugin.json",
    "plugin.json",
    ".codex-plugin/plugin.json",
]
CANONICAL_MANIFEST = MANIFEST_RELPATHS[0]

CANONICAL_CATALOG = ".claude-plugin/marketplace.json"
ROOT_CATALOG = "marketplace.json"
CATALOG_RELPATHS = [CANONICAL_CATALOG, ROOT_CATALOG]


def _load_json(path):
    with open(path, encoding="utf-8") as fh:
        return json.load(fh)


def _as_posix(*parts):
    return os.path.join(*parts).replace("\\", "/")


def _new_record():
    return {
        "version": [],           # [(where, value), ...]
        "catalog_revision": [],  # [(where, value), ...]
        "sources": {},           # catalog relpath -> source string
        "catalogs": {},          # catalog relpath -> "<relpath> [plugins.<name>]"
    }


def _collect_catalogs(root, problems):
    """Read every catalog that exists. Returns [(relpath, data), ...]."""
    catalogs = []
    for relpath in CATALOG_RELPATHS:
        path = os.path.join(root, relpath)
        if not os.path.isfile(path):
            continue
        try:
            catalogs.append((relpath, _load_json(path)))
        except (ValueError, OSError) as exc:
            problems.append("[UNREADABLE] %s: %s" % (relpath, exc))
    return catalogs


def _read_catalog_entries(catalogs, problems):
    """Fold every catalog entry into a per-plugin record."""
    plugins = {}
    for relpath, catalog in catalogs:
        entries = catalog.get("plugins")
        if not isinstance(entries, list) or not entries:
            problems.append(
                "[EMPTY_CATALOG] %s: no plugins[] entries to check" % relpath
            )
            continue
        for entry in entries:
            if not isinstance(entry, dict):
                problems.append(
                    "[BAD_ENTRY] %s: plugins[] contains a non-object entry" % relpath
                )
                continue

            name = entry.get("name", "<unnamed>")
            record = plugins.setdefault(name, _new_record())
            where = "%s [plugins.%s]" % (relpath, name)
            record["catalogs"][relpath] = where

            if "version" in entry:
                record["version"].append((where, entry["version"]))
            else:
                problems.append("[NO_VERSION] %s: no version field" % where)
            if "catalog_revision" in entry:
                record["catalog_revision"].append((where, entry["catalog_revision"]))

            source = entry.get("source")
            if isinstance(source, str) and source:
                record["sources"][relpath] = source
            else:
                problems.append(
                    "[NO_SOURCE] %s: source is missing or not a path string" % where
                )
    return plugins


def _read_manifests(root, source, record, problems):
    """Fold every manifest under `source` into the record. Returns canonical-present."""
    plugin_dir = os.path.normpath(os.path.join(root, source))
    canonical_present = False

    for relpath in MANIFEST_RELPATHS:
        manifest_path = os.path.join(plugin_dir, relpath)
        if not os.path.isfile(manifest_path):
            continue
        where = _as_posix(source, relpath)
        try:
            manifest = _load_json(manifest_path)
        except (ValueError, OSError) as exc:
            problems.append("[UNREADABLE] %s: %s" % (where, exc))
            continue

        if relpath == CANONICAL_MANIFEST:
            canonical_present = True
        if "version" in manifest:
            record["version"].append((where, manifest["version"]))
        else:
            problems.append("[NO_VERSION] %s: no version field" % where)
        if "catalog_revision" in manifest:
            record["catalog_revision"].append((where, manifest["catalog_revision"]))

    return canonical_present


def check_root(root):
    """Check every plugin declared in either catalog. Returns a list of problems."""
    problems = []

    catalogs = _collect_catalogs(root, problems)
    if not catalogs:
        problems.append(
            "[MISSING_CATALOG] no catalog found (looked for %s)"
            % ", ".join(CATALOG_RELPATHS)
        )
        return problems

    plugins = _read_catalog_entries(catalogs, problems)

    for name, record in plugins.items():
        distinct_sources = set(record["sources"].values())
        if len(distinct_sources) > 1:
            detail = "; ".join(
                "%s=%r" % (relpath, source)
                for relpath, source in sorted(record["sources"].items())
            )
            problems.append(
                "[SOURCE_DRIFT] plugin %r: catalogs disagree on source -> %s"
                % (name, detail)
            )
        if not distinct_sources:
            continue

        # Prefer the canonical catalog's source; otherwise any (they agree, or
        # SOURCE_DRIFT above already reported that they do not).
        source = record["sources"].get(CANONICAL_CATALOG, sorted(distinct_sources)[0])
        canonical_present = _read_manifests(root, source, record, problems)

        if not canonical_present:
            problems.append(
                "[NO_CANONICAL] %s: not found - this is the manifest Claude Code reads"
                % _as_posix(source, CANONICAL_MANIFEST)
            )

        # catalog_revision is optional as a whole, but all-or-nothing across the
        # two places that drive install detection. The root catalog is exempt.
        if record["catalog_revision"]:
            carriers = {where for where, _ in record["catalog_revision"]}
            required = []
            if CANONICAL_CATALOG in record["catalogs"]:
                required.append(record["catalogs"][CANONICAL_CATALOG])
            if canonical_present:
                required.append(_as_posix(source, CANONICAL_MANIFEST))
            for required_where in required:
                if required_where not in carriers:
                    problems.append(
                        "[NO_CATALOG_REVISION] %s: catalog_revision is in use for "
                        "plugin %r but missing here" % (required_where, name)
                    )

        for field in ("version", "catalog_revision"):
            values = {json.dumps(value, sort_keys=True) for _, value in record[field]}
            if len(values) > 1:
                detail = "; ".join(
                    "%s=%r" % (where, value) for where, value in record[field]
                )
                problems.append(
                    "[%s_DRIFT] plugin %r: %s disagrees across %d places -> %s"
                    % (field.upper(), name, field, len(record[field]), detail)
                )

    return problems


# ---------------------------------------------------------------------------
# check_bump - "you changed the plugin, so bump its version" (aihub#302)
# ---------------------------------------------------------------------------
# THE RULE IS DELIBERATELY LOOSE: any path under a plugin's source directory counts.
# No carve-out for hooks/, bin/, tests/ or anything else, because ALL of it ships inside
# the version-keyed install cache, and an exception list is a document that rots quietly
# while every reader keeps trusting it. "Touched the plugin => bump" needs no maintenance
# and cannot be wrong in the dangerous direction.
#
# WHY "bumped relative to this PR's base" AND NOT "bumped at some point": the alternative
# considered was to require only that main carry a bump before the change is released.
# Rejected: that is a state predicate again, and it re-opens the exact hole this closes -
# it passes for a PR that ships skill text under a version somebody else already released.
#
# ACCEPTED COST: every PR that touches plugins/** now carries a version bump, so two
# parallel PRs will conflict on plugin.json. That is the design working, not a defect -
# two changes to the same shipped artefact must not land under one version. The remedy is
# in the error message, because whoever hits it is mid-merge and will not read this file.
#
# The version is read from the CANONICAL manifest only. check_root already guarantees the
# five stamps agree, so re-deriving agreement here would duplicate it and give a second,
# confusingly-worded failure for one underlying problem.

BUMP_PROBLEM = "NO_VERSION_BUMP"
_MAX_LISTED_FILES = 12


def _norm_source(source):
    """'./plugins/polyforge' -> 'plugins/polyforge' (no trailing slash)."""
    return os.path.normpath(source).replace("\\", "/").strip("/")


def plugin_sources(root):
    """{plugin name: normalized source relpath}, canonical catalog first."""
    sources = {}
    for relpath in reversed(CATALOG_RELPATHS):  # canonical last => it wins
        path = os.path.join(root, relpath)
        if not os.path.isfile(path):
            continue
        try:
            catalog = _load_json(path)
        except (ValueError, OSError):
            continue
        for entry in catalog.get("plugins") or []:
            if not isinstance(entry, dict):
                continue
            name, source = entry.get("name"), entry.get("source")
            if name and isinstance(source, str) and source:
                sources[name] = _norm_source(source)
    return sources


def check_bump(changed_paths, sources, version_at_base, version_now):
    """Pure core: no git, no filesystem. Returns a list of problems.

    version_at_base(name) -> version string, or None if the plugin did not exist at the
    base (a brand-new plugin has nothing to bump from, so it is never a violation).
    """
    problems = []
    for name in sorted(sources):
        prefix = sources[name] + "/"
        touched = sorted(p for p in changed_paths if p.startswith(prefix))
        if not touched:
            continue

        base_v = version_at_base(name)
        now_v = version_now(name)
        if base_v is None:
            continue  # new plugin in this PR
        if base_v != now_v:
            continue  # bumped (or otherwise moved) - that is all this check asks

        listed = touched[:_MAX_LISTED_FILES]
        more = len(touched) - len(listed)
        problems.append(
            "[%s] plugin %r: %d file(s) under %s changed in this PR, but version is "
            "still %r - the same value as at the base commit.\n%s%s\n"
            "    The install cache is keyed on version (installPath is "
            "<cache>/<marketplace>/%s/<version>), so everyone already on %s will NEVER "
            "receive these files, and `/plugin update` is a no-op for them. Restamping "
            "catalog_revision does NOT help: Claude Code ignores that field at load time.\n"
            "    Fix: bump version in every stamp (%s, and each plugin.json variant under "
            "%s), then re-run this check.\n"
            "    If a parallel PR bumped first and you now conflict on plugin.json: rebase "
            "onto the new main and re-bump to the NEXT version. Do not resolve the conflict "
            "by keeping your side - that would ship two different trees under one version, "
            "which is the failure this check exists to prevent."
            % (
                BUMP_PROBLEM, name, len(touched), prefix, base_v,
                "".join("      %s\n" % p for p in listed),
                "      ... and %d more" % more if more else "",
                name, base_v,
                " and ".join(CATALOG_RELPATHS), sources[name],
            )
        )
    return problems


def _git(root, *args):
    import subprocess
    return subprocess.run(
        ["git", "-C", root] + list(args),
        stdout=subprocess.PIPE, stderr=subprocess.PIPE, check=True,
    ).stdout.decode("utf-8")


def git_changed_paths(root, base):
    """Repo-relative paths changed between base and HEAD, three-dot (i.e. from the merge
    base), which is what a PR actually proposes to land."""
    out = _git(root, "diff", "--name-only", "%s...HEAD" % base)
    return [line.strip() for line in out.splitlines() if line.strip()]


def git_manifest_version(root, rev, source):
    """version from <rev>:<source>/.claude-plugin/plugin.json, or None if absent there."""
    path = "%s/%s" % (source, CANONICAL_MANIFEST)
    try:
        blob = _git(root, "show", "%s:%s" % (rev, path))
    except Exception:
        return None
    try:
        return json.loads(blob).get("version")
    except ValueError:
        return None


def check_bump_since(root, base):
    """Wire the git layer to the pure core. Returns a list of problems."""
    sources = plugin_sources(root)
    if not sources:
        return ["[%s] no plugin source found in any catalog, so nothing could be checked "
                "- this check must never pass vacuously" % BUMP_PROBLEM]
    try:
        changed = git_changed_paths(root, base)
    except Exception as exc:
        return ["[%s] cannot diff against base %r: %s. The workflow must check out with "
                "fetch-depth: 0 so the base commit is present." % (BUMP_PROBLEM, base, exc)]

    def at_base(name):
        return git_manifest_version(root, base, sources[name])

    def now(name):
        path = os.path.join(root, sources[name], CANONICAL_MANIFEST)
        try:
            return _load_json(path).get("version")
        except (ValueError, OSError):
            return None

    return check_bump(changed, sources, at_base, now)


# ---------------------------------------------------------------------------
# self-test
# ---------------------------------------------------------------------------

def _write_fixture(root, catalogs, manifests):
    """catalogs/manifests: {relpath: dict-or-None}. None means do not create it."""
    os.makedirs(os.path.join(root, ".claude-plugin"), exist_ok=True)
    for relpath, body in catalogs.items():
        if body is None:
            continue
        path = os.path.join(root, relpath)
        os.makedirs(os.path.dirname(path) or root, exist_ok=True)
        with open(path, "w", encoding="utf-8") as fh:
            json.dump(body, fh)
    for relpath, body in manifests.items():
        if body is None:
            continue
        path = os.path.join(root, "plugins", "demo", relpath)
        os.makedirs(os.path.dirname(path), exist_ok=True)
        with open(path, "w", encoding="utf-8") as fh:
            json.dump(body, fh)


def _entry(version="1.0.0", revision=None, source="./plugins/demo"):
    entry = {"name": "demo", "version": version}
    if source is not None:
        entry["source"] = source
    if revision is not None:
        entry["catalog_revision"] = revision
    return entry


def _catalogs(claude_entry=None, root_entry=None):
    """Both catalogs present by default, mirroring the real repo."""
    if claude_entry is None:
        claude_entry = _entry(revision="abc123abc123")
    if root_entry is None:
        root_entry = _entry()
    out = {}
    out[CANONICAL_CATALOG] = (
        {"name": "test", "plugins": [claude_entry]} if claude_entry else None
    )
    out[ROOT_CATALOG] = {"name": "test", "plugins": [root_entry]} if root_entry else None
    return out


def run_self_test():
    """Every case reintroduces a specific drift and asserts this checker catches it."""
    consistent = {
        ".claude-plugin/plugin.json": {
            "version": "1.0.0",
            "catalog_revision": "abc123abc123",
        },
        "plugin.json": {"version": "1.0.0"},
        ".codex-plugin/plugin.json": {"version": "1.0.0"},
    }

    def manifests(**overrides):
        out = {k: (dict(v) if v else v) for k, v in consistent.items()}
        out.update(overrides)
        return out

    cases = [
        # (label, catalogs, manifests, expected_rule_or_None)
        ("consistent across both catalogs", _catalogs(), consistent, None),
        (
            "the aihub#232 bug: two manifests left behind",
            _catalogs(
                claude_entry=_entry(version="1.1.7", revision="abc123abc123"),
                root_entry=_entry(version="1.1.7"),
            ),
            manifests(**{
                ".claude-plugin/plugin.json": {
                    "version": "1.1.7",
                    "catalog_revision": "abc123abc123",
                },
                "plugin.json": {"version": "1.1.6"},
                ".codex-plugin/plugin.json": {"version": "1.1.6"},
            }),
            "VERSION_DRIFT",
        ),
        (
            # The hole found reviewing aihub#232: the root catalog is a fifth
            # stamp on the Copilot install path and drifted in the real incident.
            "root marketplace.json left behind, everything else current",
            _catalogs(root_entry=_entry(version="0.9.0")),
            consistent,
            "VERSION_DRIFT",
        ),
        (
            "catalog entry ahead of every manifest",
            _catalogs(
                claude_entry=_entry(version="2.0.0", revision="abc123abc123"),
                root_entry=_entry(version="2.0.0"),
            ),
            consistent,
            "VERSION_DRIFT",
        ),
        (
            "catalog_revision restamped in only one place",
            _catalogs(claude_entry=_entry(revision="deadbeefdead")),
            consistent,
            "CATALOG_REVISION_DRIFT",
        ),
        (
            "catalog_revision dropped from the canonical manifest",
            _catalogs(),
            manifests(**{".claude-plugin/plugin.json": {"version": "1.0.0"}}),
            "NO_CATALOG_REVISION",
        ),
        (
            # Negative control for the rule above: the root catalog having no
            # catalog_revision is correct, not a violation.
            "root catalog legitimately carries no catalog_revision",
            _catalogs(),
            consistent,
            None,
        ),
        (
            "catalogs disagree on the plugin source",
            _catalogs(root_entry=_entry(source="./plugins/elsewhere")),
            consistent,
            "SOURCE_DRIFT",
        ),
        (
            "a manifest with no version at all",
            _catalogs(),
            manifests(**{"plugin.json": {"name": "demo"}}),
            "NO_VERSION",
        ),
        (
            "the manifest Claude Code reads is missing",
            _catalogs(claude_entry=_entry(), root_entry=_entry()),
            manifests(**{".claude-plugin/plugin.json": None}),
            "NO_CANONICAL",
        ),
        (
            "a codex-only distribution (optional manifests absent) stays clean",
            _catalogs(),
            manifests(**{"plugin.json": None, ".codex-plugin/plugin.json": None}),
            None,
        ),
        (
            "only the Claude catalog exists (no root catalog) stays clean",
            _catalogs(root_entry=False),
            consistent,
            None,
        ),
        (
            # An object-valued source must be reported, not raise a TypeError.
            "source is an object rather than a path string",
            _catalogs(root_entry={"name": "demo", "version": "1.0.0", "source": {}}),
            consistent,
            "NO_SOURCE",
        ),
        (
            "plugins[] is not a list",
            {CANONICAL_CATALOG: {"name": "test", "plugins": {}}, ROOT_CATALOG: None},
            consistent,
            "EMPTY_CATALOG",
        ),
    ]

    failures = []
    for label, catalogs, fixture_manifests, expected in cases:
        with tempfile.TemporaryDirectory() as root:
            _write_fixture(root, catalogs, fixture_manifests)
            problems = check_root(root)
        if expected is None:
            if problems:
                failures.append("%s: expected no problems, got %s" % (label, problems))
        elif not any(("[%s]" % expected) in problem for problem in problems):
            failures.append(
                "%s: expected a [%s] problem, got %s" % (label, expected, problems)
            )

    if failures:
        print("self-test FAILED:")
        for failure in failures:
            print("  - %s" % failure)
        return 1
    print("self-test passed (%d cases)" % len(cases))
    return 0


def run_bump_self_test():
    """Cases for check_bump. The pure ones pin the rule; the last one drives the real git
    layer, because a green pure core says nothing about whether the diff and the base
    version are actually being read from the repository."""
    sources = {"polyforge": "plugins/polyforge"}
    failures = []

    def at(v):
        return lambda name: v

    cases = [
        # (label, changed_paths, base_version, head_version, expect_problem)
        ("a skill edit with no bump - the PR#264 / PR#265 shape",
         ["plugins/polyforge/skills/using-polyforge/SKILL.md"], "1.1.10", "1.1.10", True),
        ("the resident payload edited with no bump",
         ["plugins/polyforge/skills/using-polyforge/fragments/memory-first.md"],
         "1.1.10", "1.1.10", True),
        # No carve-outs: these three are the paths an exception list would have exempted.
        ("a hook edited with no bump",
         ["plugins/polyforge/hooks/pf-session-start"], "1.1.10", "1.1.10", True),
        ("a launcher script edited with no bump",
         ["plugins/polyforge/bin/polyforge-mcp.sh"], "1.1.10", "1.1.10", True),
        ("a plugin test edited with no bump",
         ["plugins/polyforge/tests/using-polyforge-payload.test.sh"], "1.1.10", "1.1.10", True),
        ("the same edit WITH a bump",
         ["plugins/polyforge/skills/using-polyforge/SKILL.md"], "1.1.10", "1.1.11", False),
        # The negative control that keeps this from being a check on every PR.
        ("a server-only change never asks for a bump",
         ["internal/domain/memory.go", "internal/mcp/tools_memory.go"],
         "1.1.10", "1.1.10", False),
        ("a catalog-only edit outside the plugin dir does not trip it",
         [".claude-plugin/marketplace.json"], "1.1.10", "1.1.10", False),
        ("no files changed at all",
         [], "1.1.10", "1.1.10", False),
        ("a path that merely starts with the same letters is not inside the plugin",
         ["plugins/polyforge-extras/skills/x.md"], "1.1.10", "1.1.10", False),
        ("a brand-new plugin has no base version to bump from",
         ["plugins/polyforge/skills/x.md"], None, "0.1.0", False),
    ]

    for label, changed, base_v, head_v, expect_problem in cases:
        problems = check_bump(changed, sources, at(base_v), at(head_v))
        got = any(("[%s]" % BUMP_PROBLEM) in p for p in problems)
        if got != expect_problem:
            failures.append("%s: expected problem=%s, got %s" % (label, expect_problem, problems))

    # --- the wiring hop: a real repository, a real diff, a real base blob --------------
    # Everything above calls check_bump directly. That leaves git_changed_paths and
    # git_manifest_version - the two places this check can silently read nothing and pass -
    # completely untested. Build a throwaway repo and drive check_bump_since end to end,
    # in both directions.
    def _run(root, *args):
        import subprocess
        subprocess.run(["git", "-C", root] + list(args),
                       stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL, check=True)

    try:
        with tempfile.TemporaryDirectory() as repo:
            _run(repo, "init", "-q", "-b", "main")
            _run(repo, "config", "user.email", "selftest@example.invalid")
            _run(repo, "config", "user.name", "selftest")
            _write_fixture(repo, _catalogs(), {
                ".claude-plugin/plugin.json": {"version": "1.0.0",
                                               "catalog_revision": "abc123abc123"},
                "plugin.json": {"version": "1.0.0"},
                ".codex-plugin/plugin.json": {"version": "1.0.0"},
            })
            skill = os.path.join(repo, "plugins", "demo", "skills")
            os.makedirs(skill, exist_ok=True)
            with open(os.path.join(skill, "SKILL.md"), "w", encoding="utf-8") as fh:
                fh.write("before\n")
            os.makedirs(os.path.join(repo, "internal"), exist_ok=True)
            with open(os.path.join(repo, "internal", "server.go"), "w", encoding="utf-8") as fh:
                fh.write("package internal\n")
            _run(repo, "add", "-A")
            _run(repo, "commit", "-qm", "base")
            base = _git(repo, "rev-parse", "HEAD").strip()

            # 1. positive: touch the plugin, do not bump.
            with open(os.path.join(skill, "SKILL.md"), "w", encoding="utf-8") as fh:
                fh.write("after\n")
            _run(repo, "commit", "-qam", "edit the skill, no bump")
            problems = check_bump_since(repo, base)
            if not any(("[%s]" % BUMP_PROBLEM) in p for p in problems):
                failures.append("end-to-end: a real unbumped plugin edit did not fail: %s"
                                % problems)
            elif "plugins/demo/skills/SKILL.md" not in "".join(problems):
                failures.append("end-to-end: the failure does not name the file that "
                                "triggered it: %s" % problems)

            # 2. negative: same repo, server-only commit on top - must go quiet again
            #    only once the bump lands, so bump and re-check.
            for rel in (".claude-plugin/plugin.json", "plugin.json", ".codex-plugin/plugin.json"):
                p = os.path.join(repo, "plugins", "demo", rel)
                data = _load_json(p)
                data["version"] = "1.0.1"
                with open(p, "w", encoding="utf-8") as fh:
                    json.dump(data, fh)
            _run(repo, "commit", "-qam", "bump")
            problems = check_bump_since(repo, base)
            if problems:
                failures.append("end-to-end: bumping did not clear the failure: %s" % problems)

            # 3. negative control on the base itself: no diff at all, no demand.
            head = _git(repo, "rev-parse", "HEAD").strip()
            problems = check_bump_since(repo, head)
            if problems:
                failures.append("end-to-end: an empty diff still demanded a bump: %s"
                                % problems)
    except Exception as exc:  # git missing or unusable
        failures.append("end-to-end git case could not run (%s) - the git layer is "
                        "therefore UNTESTED; do not read this run as green" % exc)

    if failures:
        print("bump self-test FAILED:")
        for failure in failures:
            print("  - %s" % failure)
        return 1
    print("bump self-test passed (%d pure cases + 3 end-to-end git cases)" % len(cases))
    return 0


def main():
    parser = argparse.ArgumentParser(
        description="Assert plugin version stamps agree across catalogs and manifests."
    )
    parser.add_argument("--root", default=".", help="repo root (default: cwd)")
    parser.add_argument(
        "--self-test", action="store_true", help="run the checker's own tests and exit"
    )
    parser.add_argument(
        "--require-bump-since", metavar="BASE_SHA",
        help="assert that any change to a plugin's contents since BASE_SHA is accompanied "
             "by a version bump. Under a pull_request workflow pass "
             "github.event.pull_request.base.sha; do not pass a local origin/main ref, "
             "which can be stale.",
    )
    args = parser.parse_args()

    if args.self_test:
        sys.exit(run_self_test() or run_bump_self_test())

    if args.require_bump_since:
        problems = check_bump_since(args.root, args.require_bump_since)
        if problems:
            print("plugin contents changed without a version bump:")
            for problem in problems:
                print("  %s" % problem)
            sys.exit(1)
        print("no plugin contents changed without a version bump")
        sys.exit(0)

    problems = check_root(args.root)
    if problems:
        print("plugin version stamps are inconsistent:")
        for problem in problems:
            print("  %s" % problem)
        print(
            "\nEvery place above must carry the same version. When bumping, update the "
            "catalog entries in %s and all present plugin.json variants together."
            % " and ".join(CATALOG_RELPATHS)
        )
        sys.exit(1)

    print("plugin version stamps are consistent")
    sys.exit(0)


if __name__ == "__main__":
    main()
