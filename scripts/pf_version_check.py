#!/usr/bin/env python3
# pf_version_check.py - assert the vendored plugin's version stamps agree.
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
#                     agree. This field - not version - is the signal
#                     /plugin install uses to detect a new build (team memory
#                     mem_7yldi6xb), so it drifts the same way and matters more.
#                     The root catalog is exempt: it has no such field by design.
#
# Scope: this asserts the stamps AGREE with each other. It deliberately does not
# recompute catalog_revision from plugin content, so it will not catch "content
# changed but nobody restamped". That is a stricter, workflow-changing check and
# is tracked separately.
#
# Usage
# -----
#   pf_version_check.py [--root <repo-root>] [--self-test]
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


def main():
    parser = argparse.ArgumentParser(
        description="Assert plugin version stamps agree across catalogs and manifests."
    )
    parser.add_argument("--root", default=".", help="repo root (default: cwd)")
    parser.add_argument(
        "--self-test", action="store_true", help="run the checker's own tests and exit"
    )
    args = parser.parse_args()

    if args.self_test:
        sys.exit(run_self_test())

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
