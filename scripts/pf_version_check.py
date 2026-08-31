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
# They are complementary and cannot substitute for each other. The record, verified stamp
# by stamp rather than recalled (an earlier draft of this comment claimed the defect shipped
# TWICE; it did not, and the wrong number outlived its own correction in the commit message):
#
#   93bda4a  1.1.9    before PR#264
#   789a0ef  1.1.10   PR#264 - as first pushed it had NO bump; review caught it and the
#                     bump was added in round 2. Caught by a human, not by a gate.
#   fc77479  1.1.10   PR#265 - unchanged from its base. Four files under
#                     plugins/polyforge/skills/ shipped under a version that was already
#                     released. THIS is the defect, and it went undetected.
#
# So: caught once by review, shipped once undetected. That is the case for a gate - review
# caught the first one by luck, and luck does not scale. check_root passed on both, and was
# right to: all five stamps did agree. It reads a cross-section; the defect is on the
# timeline. Keep the two error tags distinct so a red build says which one fired.
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
# bumped" is check_bump, below - added by aihub#302 after PR#265 shipped exactly that.
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
import re
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

        # catalog_revision is optional as a whole, but all-or-nothing across the two
        # places that CARRY it. (This used to read "the two places that drive install
        # detection" - a restatement of the refuted belief, sitting six lines from the
        # code and contradicting the header above. Nothing drives install detection but
        # version; these two carriers are held consistent only so the field cannot half-
        # change.) The root catalog is exempt: it has no such field by design.
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

        # Agreement is not enough on its own: five stamps all reading "" agree perfectly,
        # and `"version" in entry` is true, so [NO_VERSION] never fires either. An
        # unorderable version is a defect wherever it appears, not only on the PRs that
        # happen to touch the plugin, so it is checked here as well as in check_bump.
        for where, value in record["version"]:
            if parse_version(value) is None:
                problems.append(
                    "[BAD_VERSION] %s: version %r is not a plain dotted-numeric version "
                    "(e.g. '1.1.11'). Note whitespace counts: '1.1.10 ' is not '1.1.10'."
                    % (where, value)
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


_VERSION_RE = re.compile(r"^[0-9]+(\.[0-9]+)*$")


def parse_version(value):
    """'1.1.11' -> (1, 1, 11). None if it is not a plain dotted-numeric version.

    Deliberately STRICT, and deliberately not a general semver parser. The comparison
    below has to be an ORDERING, and anything this cannot order must be refused loudly
    rather than waved through - a permissive parser here would re-open exactly the hole
    it exists to close. Whitespace is not stripped: '1.1.10 ' is a different string that
    every one of the five stamps would have to carry identically, which is a stamping
    accident, not a version. If a pre-release scheme is ever adopted, extend this
    deliberately and add ordering cases to run_bump_self_test.
    """
    if not isinstance(value, str) or not _VERSION_RE.match(value):
        return None
    return tuple(int(p) for p in value.split("."))


def check_bump(changed_paths, sources, version_at_base, version_now):
    """Pure core: no git, no filesystem. Returns a list of problems.

    version_at_base(name) -> version string, or None if the plugin did not exist at the
    base (a brand-new plugin has nothing to bump from, so it is never a violation).

    The rule is that the version must INCREASE, not merely differ. "Differ" was the first
    implementation and it was wrong in a way strictly worse than the defect it was written
    to catch: rewriting all five stamps 1.1.11 -> 1.1.9 passed both this check and
    check_root, and 1.1.9 is a real released version whose cache directory is already
    populated on every machine that installed it. The new tree would reach those machines
    never, and two different trees would exist under one version - which is verbatim what
    the message below calls the failure this check exists to prevent.
    """
    problems = []

    def fail(msg):
        problems.append("[%s] %s" % (BUMP_PROBLEM, msg))

    for name in sorted(sources):
        prefix = sources[name] + "/"
        touched = sorted(p for p in changed_paths if p.startswith(prefix))
        if not touched:
            continue

        base_v = version_at_base(name)
        now_v = version_now(name)
        if base_v is None:
            # Brand-new plugin: there is no base version to increase from. Note this arm
            # is load-bearing, not decorative - without it the parse below rejects None
            # and reports a bogus violation, which is what pins it in the self-test.
            continue

        listed = touched[:_MAX_LISTED_FILES]
        more = len(touched) - len(listed)
        where = ("%d file(s) under %s changed in this PR:\n%s%s\n"
                 % (len(touched), prefix,
                    "".join("      %s\n" % p for p in listed),
                    "      ... and %d more" % more if more else ""))
        remedy = (
            "    The install cache is keyed on version (installPath is "
            "<cache>/<marketplace>/%s/<version>), so anyone already on that version will "
            "NEVER receive these files and `/plugin update` is a no-op for them. "
            "Restamping catalog_revision does NOT help: it is ignored at load time.\n"
            "    Fix: raise version in every stamp (%s, and each plugin.json variant "
            "under %s), then re-run this check.\n"
            "    If a parallel PR bumped first and you now conflict on plugin.json: "
            "rebase onto the new main and re-bump to the NEXT version. Do not resolve "
            "the conflict by keeping your side - that would ship two different trees "
            "under one version, which is the failure this check exists to prevent."
            % (name, " and ".join(CATALOG_RELPATHS), sources[name])
        )

        base_t, now_t = parse_version(base_v), parse_version(now_v)
        if base_t is None or now_t is None:
            bad = base_v if base_t is None else now_v
            fail("plugin %r: %sbut the version cannot be ordered: %r is not a plain "
                 "dotted-numeric version (e.g. '1.1.11'). An unorderable version cannot "
                 "be shown to have increased, so it is refused rather than assumed "
                 "good.\n%s" % (name, where, bad, remedy))
            continue
        if now_t == base_t:
            fail("plugin %r: %sbut version is still %r - the same value as at the base "
                 "commit.\n%s" % (name, where, base_v, remedy))
            continue
        if now_t < base_t:
            fail("plugin %r: %sand version went DOWN, %r -> %r. A downgrade is worse "
                 "than no bump: the older version is already installed somewhere, its "
                 "cache directory is already populated, and this tree would reach those "
                 "machines never while two different trees exist under one version.\n%s"
                 % (name, where, base_v, now_v, remedy))
            continue
    return problems


def _git(root, *args):
    import subprocess
    return subprocess.run(
        ["git", "-C", root] + list(args),
        stdout=subprocess.PIPE, stderr=subprocess.PIPE, check=True,
    ).stdout.decode("utf-8")


def git_changed_paths(root, base):
    """Repo-relative paths changed between base and HEAD.

    Three dots, i.e. from the merge base: that is what the PR actually proposes to land.
    Two dots would additionally report everything the BASE gained since the branch point,
    as if this PR had deleted it.

    Two flags here are load-bearing, and both were holes found in review:

    --no-renames  git's default rename detection emits ONLY the destination of a rename.
                  So `git mv plugins/polyforge/skills/.../copilot-tools.md docs/` showed up
                  as a single path under docs/, nothing matched the plugin prefix, and a
                  file vanishing from every future install passed green. With --no-renames
                  the same move is a delete plus an add and the delete lands under the
                  prefix.

    -z            core.quotePath defaults to true, so a non-ASCII path arrives C-quoted and
                  octal-escaped - '"plugins/polyforge/.../\\350\\257\\264\\346\\230\\216.md"' -
                  which does not start with 'plugins/polyforge/' and slipped through. -z
                  emits raw NUL-terminated paths with no quoting at all. Latent in this repo
                  today (no non-ASCII tracked paths) but this workspace is full of Chinese
                  prose, so it is one filename away from live.
    """
    out = _git(root, "diff", "--no-renames", "-z", "--name-only", "%s...HEAD" % base)
    return [p for p in out.split("\0") if p]


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
    # An unusable base must be LOUD. The one input that used to be silent was the empty
    # string, because `if args.require_bump_since:` treated it as "flag absent" and fell
    # through to check_root, which printed ITS success line and exited 0 - a different
    # check's green, standing in for this one. github.event.pull_request.base.sha is
    # exactly empty on any non-pull_request event, so adding `push:` or `merge_group:` to
    # the workflow would have turned this step into a no-op that still looked fine.
    if base is None or not str(base).strip():
        return ["[%s] no base commit given (--require-bump-since was empty). Under a "
                "pull_request workflow this is github.event.pull_request.base.sha, which "
                "is EMPTY on push / merge_group / workflow_dispatch events. This check "
                "cannot run without a base and must not pass by default." % BUMP_PROBLEM]
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
    """Cases for check_bump.

    The pure cases pin the RULE; the end-to-end cases drive the real git layer, because a
    green pure core says nothing about whether the diff and the base version are actually
    being read from the repository. Several cases below exist specifically because a
    mutation survived without them, and each says which one - a case that kills no mutant
    is decoration.
    """
    sources = {"polyforge": "plugins/polyforge"}
    failures = []

    def at(v):
        return lambda name: v

    cases = [
        # (label, changed_paths, base_version, head_version, expect_problem)
        ("a skill edit with no bump - the PR#265 shape",
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
        # --- the version must INCREASE, not merely differ -----------------------------
        # A downgrade to a real released version is worse than no bump: that cache
        # directory is already populated on every machine that installed it.
        ("a DOWNGRADE to a previously released version",
         ["plugins/polyforge/skills/using-polyforge/SKILL.md"], "1.1.11", "1.1.9", True),
        ("a downgrade of only the last component",
         ["plugins/polyforge/hooks/pf-session-start"], "1.1.11", "1.1.10", True),
        ("a minor-version increase is accepted",
         ["plugins/polyforge/hooks/pf-session-start"], "1.1.11", "1.2.0", False),
        ("a shorter version that is still larger is accepted",
         ["plugins/polyforge/hooks/pf-session-start"], "1.1.11", "2", False),
        # An unorderable version cannot be shown to have increased, so it is refused.
        ("an empty version string",
         ["plugins/polyforge/hooks/pf-session-start"], "1.1.10", "", True),
        ("a version with a trailing space",
         ["plugins/polyforge/hooks/pf-session-start"], "1.1.10", "1.1.10 ", True),
        ("a non-numeric version",
         ["plugins/polyforge/hooks/pf-session-start"], "1.1.10", "1.1.11-rc1", True),
        ("an unorderable BASE version is refused too",
         ["plugins/polyforge/hooks/pf-session-start"], "", "1.1.11", True),
        # --- negative controls that keep this from firing on every PR ------------------
        ("a server-only change never asks for a bump",
         ["internal/domain/memory.go", "internal/mcp/tools_memory.go"],
         "1.1.10", "1.1.10", False),
        ("a catalog-only edit outside the plugin dir does not trip it",
         [".claude-plugin/marketplace.json"], "1.1.10", "1.1.10", False),
        ("no files changed at all",
         [], "1.1.10", "1.1.10", False),
        ("a path that merely starts with the same letters is not inside the plugin",
         ["plugins/polyforge-extras/skills/x.md"], "1.1.10", "1.1.10", False),
        # KILLS the mutant `if base_v is None: continue` -> `if False:`. Under the old
        # "differ" rule this case passed either way, because None != "0.1.0" also
        # continued - so the carve-out was asserted by nothing. Now, without the carve-out
        # the None base reaches parse_version and is refused, and this case goes red.
        ("a brand-new plugin has no base version to bump from",
         ["plugins/polyforge/skills/x.md"], None, "0.1.0", False),
    ]

    for label, changed, base_v, head_v, expect_problem in cases:
        problems = check_bump(changed, sources, at(base_v), at(head_v))
        got = any(("[%s]" % BUMP_PROBLEM) in p for p in problems)
        if got != expect_problem:
            failures.append("%s: expected problem=%s, got %s" % (label, expect_problem, problems))

    # KILLS the mutant `for name in sorted(sources)[:1]`. Every case above has exactly one
    # plugin, so checking only the first was indistinguishable from checking all of them.
    two = {"aaa-quiet": "plugins/aaa-quiet", "zzz-loud": "plugins/zzz-loud"}
    problems = check_bump(["plugins/zzz-loud/skills/x.md"], two, at("1.0.0"), at("1.0.0"))
    if not any("zzz-loud" in p for p in problems):
        failures.append("a violation in the SECOND plugin of two was not reported: %s"
                        % problems)

    # --- the wiring hop: a real repository, a real diff, a real base blob --------------
    # Everything above calls check_bump directly. That leaves git_changed_paths and
    # git_manifest_version - the two places this check can silently read nothing and pass -
    # completely untested.
    def _run(root, *args):
        import subprocess
        subprocess.run(["git", "-C", root] + list(args),
                       stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL, check=True)

    def _fixture(repo):
        """A repo with one plugin, one server file, one committed base. Returns base sha."""
        _run(repo, "init", "-q", "-b", "main")
        _run(repo, "config", "user.email", "selftest@example.invalid")
        _run(repo, "config", "user.name", "selftest")
        # quotePath ON: the default, and the setting that used to hide non-ASCII paths.
        _run(repo, "config", "core.quotePath", "true")
        _write_fixture(repo, _catalogs(), {
            ".claude-plugin/plugin.json": {"version": "1.0.0",
                                           "catalog_revision": "abc123abc123"},
            "plugin.json": {"version": "1.0.0"},
            ".codex-plugin/plugin.json": {"version": "1.0.0"},
        })
        skill = os.path.join(repo, "plugins", "demo", "skills")
        os.makedirs(skill, exist_ok=True)
        for fn in ("SKILL.md", "extra.md"):
            with open(os.path.join(skill, fn), "w", encoding="utf-8") as fh:
                fh.write("before\n")
        os.makedirs(os.path.join(repo, "internal"), exist_ok=True)
        with open(os.path.join(repo, "internal", "server.go"), "w", encoding="utf-8") as fh:
            fh.write("package internal\n")
        os.makedirs(os.path.join(repo, "docs"), exist_ok=True)
        _run(repo, "add", "-A")
        _run(repo, "commit", "-qm", "base")
        return _git(repo, "rev-parse", "HEAD").strip()

    def _bump(repo, value="1.0.1"):
        for rel in (".claude-plugin/plugin.json", "plugin.json", ".codex-plugin/plugin.json"):
            path = os.path.join(repo, "plugins", "demo", rel)
            data = _load_json(path)
            data["version"] = value
            with open(path, "w", encoding="utf-8") as fh:
                json.dump(data, fh)

    def e2e(label, mutate, expect_problem, needle=None):
        """Build a fresh repo, apply `mutate(repo, skill_dir)`, commit, assert."""
        try:
            with tempfile.TemporaryDirectory() as repo:
                base = _fixture(repo)
                skill = os.path.join(repo, "plugins", "demo", "skills")
                mutate(repo, skill)
                _run(repo, "add", "-A")
                _run(repo, "commit", "-qm", label)
                problems = check_bump_since(repo, base)
                got = any(("[%s]" % BUMP_PROBLEM) in p for p in problems)
                if got != expect_problem:
                    failures.append("end-to-end %r: expected problem=%s, got %s"
                                    % (label, expect_problem, problems))
                elif needle and needle not in "".join(problems):
                    failures.append("end-to-end %r: failure does not mention %r: %s"
                                    % (label, needle, problems))
        except Exception as exc:
            failures.append("end-to-end %r could not run (%s) - the git layer is "
                            "therefore UNTESTED; do not read this run as green"
                            % (label, exc))

    def _write(path, text):
        with open(path, "w", encoding="utf-8") as fh:
            fh.write(text)

    e2e("a real unbumped plugin edit",
        lambda r, s: _write(os.path.join(s, "SKILL.md"), "after\n"),
        True, needle="plugins/demo/skills/SKILL.md")

    e2e("the same edit with a real bump",
        lambda r, s: (_write(os.path.join(s, "SKILL.md"), "after\n"), _bump(r)),
        False)

    # KILLS `--diff-filter=d` (drop deletions). A deleted plugin file is a change every
    # future install receives, so it must demand a bump like any other.
    e2e("deleting a plugin file with no bump",
        lambda r, s: os.remove(os.path.join(s, "extra.md")),
        True, needle="plugins/demo/skills/extra.md")

    # KILLS the loss of --no-renames. git's default rename detection reports ONLY the
    # destination, so moving a file OUT of the plugin showed up under docs/ alone, matched
    # no prefix, and passed green while every future install silently lost the file.
    def _rename_out(r, s):
        import shutil
        shutil.move(os.path.join(s, "extra.md"), os.path.join(r, "docs", "extra.md"))
    e2e("renaming a file OUT of the plugin dir with no bump",
        _rename_out, True, needle="plugins/demo/skills/extra.md")

    # KILLS the loss of -z (and any mutant that drops C-quoted lines). With
    # core.quotePath=true a non-ASCII path arrives octal-escaped and inside double quotes,
    # so it does not start with the plugin prefix.
    e2e("adding a non-ASCII plugin path with no bump",
        lambda r, s: _write(os.path.join(s, "说明.md"), "notes\n"),
        True, needle="说明.md")

    # KILLS any mutant that discards paths beginning with a double quote - the shape a
    # C-quoted path used to have. Under -z nothing is ever quoted, so such a filter looks
    # inert; it is not, because a double quote is a legal filename character and the filter
    # would silently drop a real file. Anchoring the rule on a name that genuinely starts
    # with one is what tells "this filter is dead code" apart from "this filter eats data".
    e2e('adding a plugin path whose name starts with a double quote',
        lambda r, s: _write(os.path.join(s, '"quoted.md'), "notes\n"),
        True, needle='"quoted.md')

    # KILLS three-dot -> two-dot. The base gains a plugin file after the branch point; the
    # PR itself touches only internal/. Two-dot reports the base's new file as if this PR
    # had deleted it and demands a bump for a change the PR did not make.
    try:
        with tempfile.TemporaryDirectory() as repo:
            _fixture(repo)
            _run(repo, "checkout", "-q", "-b", "feature")
            _write(os.path.join(repo, "internal", "server.go"), "package internal // edit\n")
            _run(repo, "commit", "-qam", "server-only change on the branch")
            _run(repo, "checkout", "-q", "main")
            _write(os.path.join(repo, "plugins", "demo", "skills", "late.md"), "late\n")
            _bump(repo, "1.0.2")
            _run(repo, "add", "-A")
            _run(repo, "commit", "-qm", "main moved on, with its own bump")
            moved_base = _git(repo, "rev-parse", "HEAD").strip()
            _run(repo, "checkout", "-q", "feature")
            problems = check_bump_since(repo, moved_base)
            if problems:
                failures.append(
                    "end-to-end 'base moved on': a server-only PR was asked to bump "
                    "because the BASE gained a plugin file after the branch point. The "
                    "diff must be three-dot (from the merge base). Got: %s" % problems)
    except Exception as exc:
        failures.append("end-to-end 'base moved on' could not run (%s)" % exc)

    # KILLS the anti-vacuity guard being replaced by `return []`: a tree with no catalog
    # must be reported, not silently treated as "nothing to check".
    try:
        with tempfile.TemporaryDirectory() as empty:
            _run(empty, "init", "-q", "-b", "main")
            _run(empty, "config", "user.email", "selftest@example.invalid")
            _run(empty, "config", "user.name", "selftest")
            with open(os.path.join(empty, "README.md"), "w", encoding="utf-8") as fh:
                fh.write("no catalog here\n")
            _run(empty, "add", "-A")
            _run(empty, "commit", "-qm", "base")
            head = _git(empty, "rev-parse", "HEAD").strip()
            if not check_bump_since(empty, head):
                failures.append("a tree with no plugin catalog passed silently - this "
                                "check must never pass vacuously")
    except Exception as exc:
        failures.append("end-to-end 'no catalog' could not run (%s)" % exc)

    # An unusable base must be LOUD. The empty string is the one that matters: it is what
    # github.event.pull_request.base.sha expands to on any non-pull_request event.
    for bad_base, why in ((None, "None"), ("", "empty string"), ("   ", "whitespace")):
        try:
            with tempfile.TemporaryDirectory() as repo:
                _fixture(repo)
                if not check_bump_since(repo, bad_base):
                    failures.append("a %s base passed silently - on a non-pull_request "
                                    "event this check would become a no-op" % why)
        except Exception as exc:
            failures.append("end-to-end 'bad base %s' could not run (%s)" % (why, exc))

    n_e2e = 12
    if failures:
        print("bump self-test FAILED:")
        for failure in failures:
            print("  - %s" % failure)
        return 1
    print("bump self-test passed (%d pure cases + 1 multi-plugin case + %d end-to-end "
          "git cases)" % (len(cases), n_e2e))
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
        # Both, unconditionally. `a() or b()` short-circuits, so a failing stamp self-test
        # left the bump self-test unrun and its status merely unknown - reported as one
        # red exit either way, which hides that half the suite never executed.
        rc_stamps = run_self_test()
        rc_bump = run_bump_self_test()
        sys.exit(1 if (rc_stamps or rc_bump) else 0)

    # `is not None`, NOT truthiness: an empty string means "the flag was passed with an
    # empty value", which check_bump_since reports loudly. Truthiness routed it to
    # check_root instead, whose success line then stood in for this check never running.
    if args.require_bump_since is not None:
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
