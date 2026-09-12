#!/usr/bin/env python3
# pf_contract_lint.py - Polyforge MCP tool-contract lint.
#
# Vendored from GMISWE/GMI-marketplace scripts/pf_contract_lint.py @ c97a04d.
# To refresh: re-copy from that path at a newer SHA, bump the SHA above, and
# re-apply the aihub#211 modification below.
# aihub#211 modification: the ENUM_VIOLATION rule also skips @@ROUTER_TOKEN@@
# substitution placeholders (the skill router injects them into _common/*.md),
# so an enum param whose value is a template token is not a false positive.
# aihub#599 modification (2026-09-11): Rule C (SEGMENT_COUNT_DRIFT) compares
# ready-queue segment counts stated in plugin prose (word-numbers like "seven"
# as well as digits) against the "(N-section)" phrase in the published
# pf_get_ready_queue description — the copy the repo's Go gate
# (internal/mcp/ready_queue_section_count_test.go) pins to the ReadyQueue
# struct, the marshalling authority. Re-apply on refresh.
# aihub#619 modification (2026-09-12): main() audits baseline usage after the
# run — every baseline entry must have demoted at least one violation, or the
# run fails with STALE_BASELINE_ENTRY (see audit_stale_entries). This is the
# Python half of the double ratchet the Go gate gets from assertNoStaleEntries
# in internal/mcp/universal_contract_gate_test.go. Re-apply on refresh.
# aihub#629 modification (2026-09-12): main() refuses a run that collected zero
# .md files (exit 1) instead of printing "No .md files found" and exiting 0.
# The old exit sat before the baseline stale audit, so a typo'd --target plus a
# non-empty baseline scanned nothing, checked nothing, and read as green (see
# empty_scan_error). Re-apply on refresh.
#
# Rules
# -----
# A. Contract parity (against JSON schema from --schemas):
#    a) Unknown tool name used in a pf_xxx(...) call fragment -> UNKNOWN_TOOL
#    b) Unknown kwarg for a known tool -> UNKNOWN_PARAM
#    c) Param has enum in schema AND literal string value not in enum -> ENUM_VIOLATION
#    Note: using *fewer* params than the schema requires is never a violation.
#    Special: declared_resources / requested_locks -> only name presence checked.
#
# B. Silent failure (in ```bash blocks):
#    Line matching || (true|echo) without trailing "# lint-allow: silent <reason>"
#    -> SILENT_FAILURE
#
# C. Ready-queue segment-count parity (against --schemas):
#    A word-number or digit immediately modifying "segment(s)", on a line within
#    a few lines of a ready-queue/LCRS mention, that disagrees with the
#    "(N-section)" count in the pf_get_ready_queue description
#    -> SEGMENT_COUNT_DRIFT
#    A schema from which no count can be read makes this rule vacuous, so main()
#    refuses it up front — an absent check is not a passing one.
#
# Baseline
# --------
# JSON array of {"file": ..., "rule": ..., "match": ..., "wi": ...}.
# Every baseline entry MUST have a "wi" field (enforced at load time).
# Violations matching a baseline entry are demoted to NOTICE (still printed).
# Every entry must in turn demote at least one violation per run, or the run
# fails with STALE_BASELINE_ENTRY (aihub#619) — the baseline only holds
# exemptions that a live violation still needs.
#
# Output
# ------
#   <file>:<line> [RULE] description
# Exit code 1 if any un-baselined violations or stale baseline entries, or when
# the run cannot vouch at all: unusable schema, unloadable baseline, or zero
# .md files collected (aihub#629). 0 only when checks actually ran and passed.
#
# Usage
# -----
#   pf_contract_lint.py --schemas <path|url> --target <dir> [--target <dir>...]
#                        [--baseline <json>] [--self-test]


import argparse
import json
import os
import re
import sys
import urllib.request
from dataclasses import dataclass
from typing import Optional


# ---------------------------------------------------------------------------
# Schema loading
# ---------------------------------------------------------------------------

def load_schema(source: str) -> dict:
    """Load schema from a file path or http(s) URL."""
    if source.startswith("http://") or source.startswith("https://"):
        req = urllib.request.Request(source, headers={"User-Agent": "pf-contract-lint/1"})
        with urllib.request.urlopen(req, timeout=30) as resp:
            data = resp.read()
    else:
        with open(source, "rb") as f:
            data = f.read()
    return json.loads(data)


def load_baseline(path: Optional[str]) -> list:
    """Load and validate the baseline JSON array."""
    if not path:
        return []
    with open(path) as f:
        entries = json.load(f)
    if not isinstance(entries, list):
        raise ValueError(f"Baseline must be a JSON array, got {type(entries)}")
    for i, e in enumerate(entries):
        if not isinstance(e, dict):
            raise ValueError(f"Baseline entry {i} is not an object")
        if "wi" not in e or not e["wi"]:
            raise ValueError(
                f"Baseline entry {i} is missing required 'wi' field: {e!r}\n"
                "Every baseline entry must reference a work item (e.g. \"wi\": \"aihub#123\")"
            )
        # A baseline entry without file+rule+match would silently swallow whole
        # categories of future violations — require all three to keep each entry
        # scoped to exactly one known drift.
        for required in ("file", "rule", "match"):
            if not e.get(required):
                raise ValueError(
                    f"Baseline entry {i} is missing required '{required}' field: {e!r}\n"
                    "Every entry must pin file + rule + match (no wildcards)."
                )
    return entries


# ---------------------------------------------------------------------------
# Violation dataclass
# ---------------------------------------------------------------------------

@dataclass
class Violation:
    file: str
    line: int
    rule: str
    message: str
    match: str = ""

    def key(self):
        return (self.file, self.rule, self.match)


# ---------------------------------------------------------------------------
# Rule A: Contract parity
# ---------------------------------------------------------------------------

# Patterns to find pf_xxx( call fragments:
#   - inline code: `pf_xxx(...)`
#   - fenced code block: pf_xxx(
#   - plain prose: pf_xxx(
_TOOL_CALL_RE = re.compile(r'\bpf_(\w+)\s*\(([^)]*)\)?', re.DOTALL)
_KWARG_RE = re.compile(r'\b(\w+)\s*=')
_STRING_VALUE_RE = re.compile(r'\b(\w+)\s*=\s*["\']([^"\']*)["\']')

# Params that only have their name checked (not internal shape).
_SKIP_SHAPE_PARAMS = {"declared_resources", "requested_locks"}


def lint_rule_a(file_path: str, lines: list, schema: dict) -> list[Violation]:
    """Apply Rule A: contract parity against schema."""
    violations = []
    tools = schema.get("tools", {})

    # We scan line-by-line for tool call fragments, collecting multi-line blocks.
    # For simplicity we join all lines but track line offsets.
    full_text = "\n".join(lines)
    # Build a map: char_offset → line number (1-indexed).
    offsets = []
    pos = 0
    for i, ln in enumerate(lines, 1):
        offsets.append((pos, i))
        pos += len(ln) + 1  # +1 for \n

    def char_to_line(char_pos: int) -> int:
        lo, hi = 0, len(offsets) - 1
        while lo < hi:
            mid = (lo + hi + 1) // 2
            if offsets[mid][0] <= char_pos:
                lo = mid
            else:
                hi = mid - 1
        return offsets[lo][1]

    for m in _TOOL_CALL_RE.finditer(full_text):
        name = "pf_" + m.group(1)
        args_text = m.group(2)
        fragment = m.group(0)[:80]  # truncate for match key
        lineno = char_to_line(m.start())

        # (a) Unknown tool name.
        if name not in tools:
            violations.append(Violation(
                file=file_path, line=lineno,
                rule="UNKNOWN_TOOL",
                message=f"tool '{name}' not in schema",
                match=fragment,
            ))
            continue

        tool_params = tools[name].get("params", {})
        kwargs = [k.group(1) for k in _KWARG_RE.finditer(args_text)]
        string_vals = {
            kv.group(1): kv.group(2)
            for kv in _STRING_VALUE_RE.finditer(args_text)
        }

        for kw in kwargs:
            # (b) Unknown kwarg.
            if kw not in tool_params:
                # Skip shape-only params.
                if kw in _SKIP_SHAPE_PARAMS:
                    continue
                violations.append(Violation(
                    file=file_path, line=lineno,
                    rule="UNKNOWN_PARAM",
                    message=f"tool '{name}': param '{kw}' not in schema",
                    match=fragment,
                ))
                continue

            # (c) Enum violation: literal string not in allowed enum.
            param_def = tool_params.get(kw, {})
            enum_vals = param_def.get("enum")
            if not enum_vals:
                continue
            if kw not in string_vals:
                continue  # not a literal string value -> no check
            val = string_vals[kw]
            # Skip placeholder patterns: enum wildcards (* or |) and @@ROUTER_TOKEN@@
            # substitution placeholders injected by the skill router (aihub#211).
            if "*" in val or "|" in val or "@@" in val:
                continue
            if val not in enum_vals:
                violations.append(Violation(
                    file=file_path, line=lineno,
                    rule="ENUM_VIOLATION",
                    message=(
                        f"tool '{name}': param '{kw}' value '{val}' "
                        f"not in enum {enum_vals}"
                    ),
                    match=fragment,
                ))

    return violations


# ---------------------------------------------------------------------------
# Rule B: Silent failure in bash fenced blocks
# ---------------------------------------------------------------------------

_SILENT_RE = re.compile(r'\|\|\s*(true|echo)\b')
_LINT_ALLOW_RE = re.compile(r'#\s*lint-allow:\s*silent\b')

def lint_rule_b(file_path: str, lines: list) -> list[Violation]:
    """Apply Rule B: || true / || echo in bash fenced blocks."""
    violations = []
    in_bash_block = False

    for lineno, ln in enumerate(lines, 1):
        stripped = ln.strip()
        # Detect fenced code block delimiters.
        if stripped.startswith("```"):
            lang = stripped[3:].strip().lower()
            if not in_bash_block:
                # Opening: check if it's a bash/shell block.
                if lang in ("bash", "sh", "shell", "zsh"):
                    in_bash_block = True
            else:
                # Closing fence (any ``` closes).
                in_bash_block = False
            continue

        if in_bash_block:
            m = _SILENT_RE.search(ln)
            if m and not _LINT_ALLOW_RE.search(ln):
                # Window the fragment around the match so the `|| true/echo`
                # itself stays visible (and baseline-matchable) on long lines.
                s = ln.strip()
                idx = max(0, s.find(m.group(0)))
                start = max(0, idx - 60)
                match_text = s[start:idx + len(m.group(0)) + 20][:100]
                violations.append(Violation(
                    file=file_path, line=lineno,
                    rule="SILENT_FAILURE",
                    message=f"|| true/echo without lint-allow: silent annotation",
                    match=match_text,
                ))

    return violations


# ---------------------------------------------------------------------------
# Rule C: Ready-queue segment-count parity (aihub#599)
# ---------------------------------------------------------------------------
#
# The ready queue's section count is ONE number. The repo side already keeps
# three copies of it aligned (struct / schema description / design doc) through
# internal/mcp/ready_queue_section_count_test.go, with the ReadyQueue struct as
# the authority. The plugins copy (pf-status/SKILL.md says "seven segments")
# was the last surface nothing compared, and it spells the count as a WORD,
# which is why this rule parses word-numbers and not only digits.
#
# Why this lives here and not in a Go test: a Go test pinning a plugins/ file
# couples any future segment change to a forced plugin release in the same PR.
# This lint has the baseline mechanism — a drift can be demoted to a
# wi-referenced NOTICE that stays visible on every run until the next plugin
# release fixes the prose — so the coupling is soft where a test's would be
# hard (aihub#599, recorded by aihub#595's design follow-up).
#
# The count is read from the same "(N-section)" phrase the Go gate pins against
# the struct, so struct -> description -> this rule move together: a segment
# change forces the description in the same PR (the Go gate is red otherwise),
# and the fresh in-repo schema dump brings the new count here.
#
# Scope discipline (measured on the 2026-09-11 tree): the recognizer matches
# exactly the two live pf-status claims and nothing else in plugins/.
# Hyphenated compounds ("three-segment format", the output-format name used
# throughout the skills) do not match — the \s+ requires a spaced modifier —
# and an intervening noun breaks the match ("two path segments" of a URL).
# The queue-context window keeps a future unrelated "N segments" out of scope
# unless it is written next to a ready-queue/LCRS mention.

_WORD_NUMBERS = {
    "zero": 0, "one": 1, "two": 2, "three": 3, "four": 4, "five": 5,
    "six": 6, "seven": 7, "eight": 8, "nine": 9, "ten": 10, "eleven": 11,
    "twelve": 12, "thirteen": 13, "fourteen": 14, "fifteen": 15,
    "sixteen": 16, "seventeen": 17, "eighteen": 18, "nineteen": 19,
    "twenty": 20,
}

# Longest-first so "seventeen" is not consumed as "seven" + leftovers.
_SEGMENT_COUNT_RE = re.compile(
    r'\b(' + "|".join(sorted(_WORD_NUMBERS, key=len, reverse=True)) + r'|\d{1,2})\s+segments?\b',
    re.IGNORECASE,
)
_QUEUE_CONTEXT_RE = re.compile(r'ready[ _-]?queue|LCRS', re.IGNORECASE)
_QUEUE_CONTEXT_WINDOW = 5  # lines either side of the claim
_SECTION_COUNT_RE = re.compile(r'\((\d+)-section\)')


def ready_queue_section_count(schema: dict) -> Optional[int]:
    """Return the section count pf_get_ready_queue publishes, or None.

    Read from the "(N-section)" phrase in the tool description — the copy
    internal/mcp/ready_queue_section_count_test.go pins against the ReadyQueue
    struct, so the number here is transitively the struct's."""
    desc = schema.get("tools", {}).get("pf_get_ready_queue", {}).get("description") or ""
    m = _SECTION_COUNT_RE.search(desc)
    return int(m.group(1)) if m else None


def lint_rule_c(file_path: str, lines: list, schema: dict) -> list[Violation]:
    """Apply Rule C: ready-queue segment counts in prose match the schema."""
    violations = []
    expected = ready_queue_section_count(schema)

    for lineno, ln in enumerate(lines, 1):
        for m in _SEGMENT_COUNT_RE.finditer(ln):
            # Only claims written in ready-queue context are segment-count
            # claims; "segments" elsewhere (URL paths, display formats) are not.
            lo = max(0, lineno - 1 - _QUEUE_CONTEXT_WINDOW)
            hi = min(len(lines), lineno + _QUEUE_CONTEXT_WINDOW)
            if not _QUEUE_CONTEXT_RE.search("\n".join(lines[lo:hi])):
                continue
            token = m.group(1).lower()
            said = _WORD_NUMBERS.get(token)
            if said is None:
                said = int(token)
            if expected is None:
                # Unreachable through main(), which refuses a schema with no
                # count before linting; kept so a claim is never skipped
                # silently if this function is reached some other way.
                violations.append(Violation(
                    file=file_path, line=lineno,
                    rule="SEGMENT_COUNT_DRIFT",
                    message=(
                        f"claims '{m.group(0)}' for the ready queue, and the schema "
                        "publishes no '(N-section)' count on pf_get_ready_queue to "
                        "check it against — cannot verify"
                    ),
                    match=m.group(0),
                ))
                continue
            if said != expected:
                violations.append(Violation(
                    file=file_path, line=lineno,
                    rule="SEGMENT_COUNT_DRIFT",
                    message=(
                        f"claims '{m.group(0)}' (= {said}) ready-queue segments; the "
                        f"published pf_get_ready_queue description says ({expected}-section). "
                        "That description is pinned to the ReadyQueue struct by "
                        "internal/mcp/ready_queue_section_count_test.go, so the queue "
                        "moved and this prose did not (or vice versa). Fix the prose in "
                        "the next plugin release, or baseline this drift with a wi "
                        "reference until then."
                    ),
                    match=m.group(0),
                ))

    return violations


# ---------------------------------------------------------------------------
# Baseline matching
# ---------------------------------------------------------------------------

def match_baseline(v: Violation, baseline: list) -> Optional[dict]:
    """Return the first baseline entry that matches this violation, or None.

    The 'match' field is compared with word-boundary anchoring against v.match
    (the truncated call fragment) and v.message — so an entry like 'ids' matches
    the param token `ids` but NOT `work_item_ids`/`guids`/`rapids`. This keeps
    each baseline entry scoped to exactly the violation it was written for.
    """
    for entry in baseline:
        if entry.get("rule") and entry["rule"] != v.rule:
            continue
        if entry.get("file") and not v.file.endswith(entry["file"]):
            continue
        if entry.get("match"):
            needle_re = re.compile(
                r"(?<![0-9A-Za-z_])" + re.escape(entry["match"]) + r"(?![0-9A-Za-z_])"
            )
            if not needle_re.search(v.match) and not needle_re.search(v.message):
                continue
        return entry
    return None


def audit_stale_entries(baseline: list, used_entry_ids: set,
                        scanned_files: list, baseline_path: str) -> list[str]:
    """Return one printable error line per baseline entry unused this run.

    An entry is used when it demoted at least one violation. An unused entry is
    an exemption with nothing behind it: either the drift it excused is fixed
    (delete the entry in the same change), or the lint stopped looking where
    the entry points — the shape aihub#613 and aihub#540 each had to find by
    reading, because nothing made it red. Semantics mirror assertNoStaleEntries
    in internal/mcp/universal_contract_gate_test.go, which does the same for
    the Go contract gates' baseline (aihub#619).

    The one legitimate way an entry matches nothing is a run whose --target set
    never scanned the entry's file, so that case gets its own message: on the
    full CI target set it means the file was deleted or renamed (stale, delete
    or retarget); on a narrower local run it means re-run with the full set
    before judging. Both are errors — a partial run cannot vouch for the
    baseline, and an absent check is not a passing one.
    """
    errors = []
    for entry in baseline:
        if id(entry) in used_entry_ids:
            continue
        ident = (
            f"rule={entry['rule']} file={entry['file']} "
            f"match={entry['match']!r} (wi {entry['wi']})"
        )
        note = f" Note recorded: {entry['note']}" if entry.get("note") else ""
        if any(f.endswith(entry["file"]) for f in scanned_files):
            errors.append(
                f"{baseline_path} [STALE_BASELINE_ENTRY] {ident}: matched no "
                "violation this run. Either the drift is fixed — DELETE this "
                "entry in the same change that fixed it — or the lint no longer "
                "looks where the entry points, which is worse than the original "
                "drift because it is an exemption with nothing behind it. A "
                "deleted entry stays recoverable: "
                f"git log -S '{entry['match']}' -- {baseline_path}.{note}"
            )
        else:
            errors.append(
                f"{baseline_path} [STALE_BASELINE_ENTRY] {ident}: its file is "
                f"not among the {len(scanned_files)} scanned file(s), so the "
                "entry can never match. Either the file was deleted or renamed "
                "(then DELETE or retarget the entry), or this run's --target "
                "set is narrower than the CI run's (then re-run with the full "
                "target set, e.g. --target plugins/, before judging the entry "
                f"stale).{note}"
            )
    return errors


# ---------------------------------------------------------------------------
# File scanning
# ---------------------------------------------------------------------------

def collect_md_files(targets: list) -> list:
    """Collect all *.md files from target directories (recursive)."""
    files = []
    for target in targets:
        if os.path.isfile(target):
            if target.endswith(".md"):
                files.append(target)
        else:
            for root, dirs, fnames in os.walk(target):
                # Skip hidden dirs.
                dirs[:] = [d for d in dirs if not d.startswith(".")]
                for fn in sorted(fnames):
                    if fn.endswith(".md"):
                        files.append(os.path.join(root, fn))
    return sorted(files)


def empty_scan_error(targets: list, files: list) -> Optional[str]:
    """Return the refusal message when a run collected zero .md files, else None.

    Zero files means zero checks, and this script used to read that as a PASS:
    print "No .md files found in specified targets." and exit 0, BEFORE the
    baseline stale audit ever ran. So a typo'd --target plus a non-empty
    baseline scanned nothing, checked nothing, and still went green in CI
    (aihub#629) — the same "an absent check reads as a passing one" shape
    aihub#599 closed for a count-less schema and aihub#619 closed for stale
    baseline entries.

    The two ways the list goes empty get DIFFERENT messages but the same
    nonzero exit. A target that does not exist is almost certainly a typo —
    os.walk on a missing path silently yields nothing — so that message routes
    to fixing the path. An existing target with no .md files may be
    legitimately empty, but an empty run still vouches for nothing (aihub#619's
    rule for partial --target runs: a run that never looked cannot judge), so
    the repair is to drop the target from the invocation, not to let the run
    pass.
    """
    if files:
        return None
    missing = [t for t in targets if not os.path.exists(t)]
    if missing:
        return (
            "ERROR: no .md files found, and these --target path(s) do not "
            f"exist: {', '.join(missing)}. A mistyped target scans zero files "
            "and checks nothing, and until aihub#629 that read as a passing "
            "run. Fix the path."
        )
    return (
        f"ERROR: the --target path(s) ({', '.join(targets)}) exist but "
        "contain no .md files, so zero checks ran and this run can vouch for "
        "nothing — an absent check is not a passing one. If the target is "
        "legitimately empty now, remove it from the invocation rather than "
        "letting an empty scan stand in for a green one."
    )


def lint_file(file_path: str, schema: dict) -> list[Violation]:
    """Run both rule sets on a single file."""
    try:
        with open(file_path, encoding="utf-8") as f:
            content = f.read()
    except OSError as e:
        return [Violation(file=file_path, line=0, rule="IO_ERROR", message=str(e))]

    lines = content.splitlines()
    violations = []
    violations.extend(lint_rule_a(file_path, lines, schema))
    violations.extend(lint_rule_b(file_path, lines))
    violations.extend(lint_rule_c(file_path, lines, schema))
    return violations


# ---------------------------------------------------------------------------
# Self-test
# ---------------------------------------------------------------------------

_FIXTURES = {
    # fmt: off
    "unknown_tool.md": (
        "# Test\n"
        "`pf_nonexistent(work_item_id=\"foo\")`\n",
        [("UNKNOWN_TOOL", "pf_nonexistent")],
    ),
    "unknown_param.md": (
        "# Test\n"
        "`pf_recall(project=\"foo\", bad_param=\"bar\")`\n",
        [("UNKNOWN_PARAM", "bad_param")],
    ),
    "enum_violation.md": (
        "# Test\n"
        '`pf_remember(project="foo", type="not_a_real_type", content="x", visibility="project")`\n',
        [("ENUM_VIOLATION", "not_a_real_type")],
    ),
    "valid_subset.md": (
        "# Test\n"
        # Using fewer params than required is always valid.
        "`pf_recall(project=\"foo\")`\n",
        [],  # no violations
    ),
    "lint_allow.md": (
        "# Test\n"
        "```bash\n"
        "some_cmd || true  # lint-allow: silent pre-existing pattern from publish_dev\n"
        "```\n",
        [],  # exempted by annotation
    ),
    "silent_failure.md": (
        "# Test\n"
        "```bash\n"
        "some_cmd || true\n"
        "```\n",
        [("SILENT_FAILURE", "|| true")],
    ),
    "baseline_demotion.md": (
        "# Test\n"
        "`pf_nonexistent2(work_item_id=\"foo\")`\n",
        [],  # baselined → NOTICE only, not a failure
    ),
    "save_artifact_bare_type.md": (
        "# Test\n"
        '`pf_save_artifact(type="spec", work_item_id="x", content="y")`\n',
        [("ENUM_VIOLATION", "spec")],  # server 400s on bare "spec"; must be methodology.spec (aihub#211)
    ),
    "save_artifact_router_placeholder.md": (
        "# Test\n"
        '`pf_save_artifact(type="@@ARTIFACT_TYPE@@", work_item_id="x", content="y")`\n',
        [],  # @@...@@ router substitution token is skipped, not an enum violation (aihub#211)
    ),
    # ── Rule C calibration (aihub#599). The positives prove the rule fires on a
    # wrong count in BOTH spellings; the negatives are lifted from the live
    # plugins corpus so that the shapes the rule must NOT fire on are pinned
    # here rather than only observed once on one tree.
    "segment_count_drift.md": (
        "# Test\n"
        "Inspect the project-wide ready queue (LCRS six segments).\n",
        [("SEGMENT_COUNT_DRIFT", "six segments")],  # word-number, wrong count
    ),
    "segment_count_digit_drift.md": (
        "# Test\n"
        "The LCRS ready queue returns 9 segments in one call.\n",
        [("SEGMENT_COUNT_DRIFT", "9 segments")],  # digit spelling, wrong count
    ),
    "segment_count_match.md": (
        "# Test\n"
        "Inspect the project-wide ready queue (LCRS seven segments).\n"
        "Returns all seven segments in one call.\n",
        [],  # the live pf-status claims: word "seven" == (7-section), no violation
    ),
    "segment_count_unrelated.md": (
        "# Test\n"
        "pf_get_ready_queue is discussed near here, so the window is live.\n"
        "Output three-segment format.\n"          # hyphenated format name, not a claim
        "Do not invent a source value; use one of the seven.\n"  # no "segments"
        "owner, repo = the last two path segments of the URL.\n",  # intervening noun
        [],
    ),
    "segment_count_no_context.md": (
        "# Test\n"
        "The report has four segments.\n",  # no ready-queue/LCRS context in window
        [],
    ),
    "segment_count_unverifiable.md": (
        "# Test\n"
        "The ready queue has seven segments.\n",
        # Linted against a schema with NO "(N-section)" count (special-cased in
        # run_self_test): the claim must be reported, not skipped.
        [("SEGMENT_COUNT_DRIFT", "cannot verify")],
    ),
    # fmt: on
}

_MINIMAL_SCHEMA = {
    "generated_from": "test",
    "tools": {
        "pf_recall": {
            "description": "Recall memories",
            "params": {
                "project": {"type": "string", "required": True},
                "query": {"type": "string", "required": False},
            },
        },
        "pf_remember": {
            "description": "Store memory",
            "params": {
                "project": {"type": "string", "required": True},
                "type": {
                    "type": "string",
                    "required": True,
                    "enum": [
                        "experience.debug", "experience.approach", "experience.pitfall",
                        "experience.code", "fact.architecture", "fact.constraint",
                        "fact.reference", "fact.note", "rule.scheduling",
                        "rule.convention", "rule.process", "rule.coding", "rule.work",
                        "methodology.spec", "methodology.plan", "methodology.review",
                        "methodology.execute", "methodology.retro", "methodology.wrap_summary",
                    ],
                },
                "content": {"type": "string", "required": True},
                "visibility": {"type": "string", "required": True},
            },
        },
        "pf_save_artifact": {
            "description": "Save a methodology artifact",
            "params": {
                "type": {
                    "type": "string",
                    "required": True,
                    "enum": [
                        "methodology.spec", "methodology.plan", "methodology.review",
                        "methodology.execute", "methodology.retro", "methodology.wrap_summary",
                    ],
                },
                "work_item_id": {"type": "string", "required": True},
                "content": {"type": "string", "required": False},
            },
        },
        # aihub#599: the "(7-section)" phrase is what Rule C reads; the live
        # description carries it and the repo's Go gate keeps it equal to the
        # ReadyQueue struct's segment count.
        "pf_get_ready_queue": {
            "description": "Get the LCRS (7-section) ready queue for a project.",
            "params": {
                "project": {"type": "string", "required": True},
            },
        },
    },
}

_SELF_TEST_BASELINE = [
    {"file": "baseline_demotion.md", "rule": "UNKNOWN_TOOL", "match": "pf_nonexistent2", "wi": "aihub#999"},
]


def run_self_test():
    """Run built-in fixture tests; exit(1) on failure."""
    import tempfile

    failures = []
    passed = 0

    with tempfile.TemporaryDirectory() as tmpdir:
        for fixture_name, (content, expected) in _FIXTURES.items():
            fpath = os.path.join(tmpdir, fixture_name)
            with open(fpath, "w") as f:
                f.write(content)

            # The unverifiable fixture is the one case linted against a schema
            # that publishes NO section count (main() refuses such a schema for
            # a real run; this proves the rule itself would still report the
            # claim rather than skip it).
            schema = _MINIMAL_SCHEMA
            if fixture_name == "segment_count_unverifiable.md":
                schema = json.loads(json.dumps(_MINIMAL_SCHEMA))
                del schema["tools"]["pf_get_ready_queue"]

            viols = lint_file(fpath, schema)

            # Apply baseline for the baseline_demotion fixture.
            if fixture_name == "baseline_demotion.md":
                remaining = [v for v in viols if not match_baseline(v, _SELF_TEST_BASELINE)]
            else:
                remaining = viols

            if not expected:
                # Expect no violations.
                if remaining:
                    failures.append(
                        f"FAIL {fixture_name}: expected no violations, got {[v.rule for v in remaining]}"
                    )
                else:
                    passed += 1
            else:
                # Check each expected (rule, fragment) is present.
                for exp_rule, exp_fragment in expected:
                    found = any(
                        v.rule == exp_rule and exp_fragment in (v.match + v.message)
                        for v in viols
                    )
                    if not found:
                        failures.append(
                            f"FAIL {fixture_name}: expected {exp_rule!r} containing {exp_fragment!r}, "
                            f"got violations: {[(v.rule, v.match) for v in viols]}"
                        )
                    else:
                        passed += 1

        # ── Stale-entry audit checks (aihub#619) ──
        # The audit is a main()-level mechanism (it needs the whole run's
        # used-set), so it is exercised directly rather than through lint_file
        # fixtures. The used-set is derived the way main() derives it: by
        # matching real violations from a fixture against the baseline.
        used_entry = _SELF_TEST_BASELINE[0]
        stale_in_scope = {
            "file": "baseline_demotion.md", "rule": "UNKNOWN_TOOL",
            "match": "pf_never_called", "wi": "aihub#998",
            "note": "self-test only: nothing in any fixture says pf_never_called",
        }
        stale_out_of_scope = {
            "file": "deleted_elsewhere.md", "rule": "UNKNOWN_PARAM",
            "match": "ghost", "wi": "aihub#997",
        }
        audit_baseline = [used_entry, stale_in_scope, stale_out_of_scope]
        scanned = [os.path.join(tmpdir, name) for name in _FIXTURES]
        demo_viols = lint_file(
            os.path.join(tmpdir, "baseline_demotion.md"), _MINIMAL_SCHEMA
        )
        used_ids = set()
        for v in demo_viols:
            b = match_baseline(v, audit_baseline)
            if b:
                used_ids.add(id(b))
        audit_errors = audit_stale_entries(
            audit_baseline, used_ids, scanned, "test_baseline.json"
        )
        audit_checks = [
            # A used entry must never be flagged: this is what keeps the audit
            # from destroying live exemptions like the aihub#448 UNKNOWN_TOOL
            # entries that carry aihub#613's republication recovery notes.
            ("audit_used_entry_not_flagged",
             not any("pf_nonexistent2" in e for e in audit_errors)),
            # An in-scope entry that demoted nothing is stale — the aihub#613 /
            # aihub#540 orphan shape must now be red, not silent.
            ("audit_in_scope_stale_flagged",
             any("pf_never_called" in e and "matched no violation" in e
                 for e in audit_errors)),
            # The note travels with the error, like the Go message's
            # "Note recorded:" — it may carry recovery leads (aihub#613).
            ("audit_note_recorded",
             any("pf_never_called" in e and "Note recorded:" in e
                 for e in audit_errors)),
            # An entry whose file was never scanned gets the out-of-scope
            # message (deleted file, or a partial --target run), still an error.
            ("audit_out_of_scope_flagged",
             any("ghost" in e and "not among" in e for e in audit_errors)),
            ("audit_exactly_two_stale", len(audit_errors) == 2),
        ]
        for name, ok in audit_checks:
            if ok:
                passed += 1
            else:
                failures.append(f"FAIL {name}: audit errors: {audit_errors!r}")

        # ── Empty-scan refusal (aihub#629) ──
        # main()-level like the stale audit. Unit-check the two message shapes
        # and the non-refusal on a real file list, then prove END TO END that
        # main() wires the refusal to a nonzero exit. The negative control
        # these pin is the old behaviour: "No .md files found in specified
        # targets." on stderr and exit 0 — a typo'd --target plus a non-empty
        # baseline read as a passing run, with the stale audit never reached.
        typo_target = os.path.join(tmpdir, "no_such_dir")
        empty_dir = os.path.join(tmpdir, "genuinely_empty")
        os.makedirs(empty_dir)
        typo_msg = empty_scan_error([typo_target], [])
        empty_dir_msg = empty_scan_error([empty_dir], [])
        empty_checks = [
            ("empty_scan_typo_target_refused",
             typo_msg is not None and "do not exist" in typo_msg
             and typo_target in typo_msg),
            ("empty_scan_existing_empty_dir_refused",
             empty_dir_msg is not None
             and "contain no .md files" in empty_dir_msg),
            # The two shapes must not collapse into one message, or the typo
            # case loses its "fix the path" routing.
            ("empty_scan_shapes_distinguished", typo_msg != empty_dir_msg),
            ("empty_scan_nonempty_run_not_refused",
             empty_scan_error([tmpdir], scanned) is None),
        ]
        for name, ok in empty_checks:
            if ok:
                passed += 1
            else:
                failures.append(
                    f"FAIL {name}: typo={typo_msg!r} empty={empty_dir_msg!r}"
                )

        # End to end through a real process: the refusal must reach the EXIT
        # CODE, not just a message — a guard main() never consults is the
        # exact no-op shape this fixture exists to keep red.
        import subprocess
        schema_path = os.path.join(tmpdir, "e2e_schema.json")
        with open(schema_path, "w") as f:
            json.dump(_MINIMAL_SCHEMA, f)
        proc = subprocess.run(
            [sys.executable, os.path.abspath(__file__),
             "--schemas", schema_path, "--target", typo_target],
            capture_output=True, text=True,
        )
        e2e_checks = [
            ("empty_scan_e2e_exit_nonzero", proc.returncode != 0),
            ("empty_scan_e2e_message_on_stderr",
             "no .md files" in proc.stderr.lower()),
        ]
        for name, ok in e2e_checks:
            if ok:
                passed += 1
            else:
                failures.append(
                    f"FAIL {name}: rc={proc.returncode} stderr={proc.stderr!r}"
                )

    if failures:
        print(f"self-test: {len(failures)} failure(s):")
        for f in failures:
            print(f"  {f}")
        sys.exit(1)
    else:
        print(f"self-test: {passed} fixture(s) passed")


# ---------------------------------------------------------------------------
# Main
# ---------------------------------------------------------------------------

def main():
    parser = argparse.ArgumentParser(
        description="Lint polyforge MCP tool calls against schema contract."
    )
    parser.add_argument("--schemas", help="Path or URL to pf-tool-schemas.json")
    parser.add_argument(
        "--target", action="append", dest="targets",
        help="Directory or file to lint (repeatable)",
    )
    parser.add_argument("--baseline", help="Path to baseline JSON file")
    parser.add_argument(
        "--self-test", action="store_true",
        help="Run built-in fixture tests and exit",
    )
    args = parser.parse_args()

    if args.self_test:
        run_self_test()
        sys.exit(0)

    if not args.schemas:
        parser.error("--schemas is required (unless --self-test)")
    if not args.targets:
        parser.error("--target is required (unless --self-test)")

    # Load schema.
    try:
        schema = load_schema(args.schemas)
    except Exception as e:
        print(f"ERROR: failed to load schemas from {args.schemas!r}: {e}", file=sys.stderr)
        sys.exit(1)

    # Rule C is vacuous against a schema that publishes no section count, and a
    # vacuous rule reads as a passing one. Refuse the run instead (aihub#599).
    if ready_queue_section_count(schema) is None:
        print(
            "ERROR: the schema publishes no '(N-section)' count in the "
            "pf_get_ready_queue description, so the SEGMENT_COUNT_DRIFT rule has "
            "nothing to check prose against. The count has been in that "
            "description since the tool was added and the repo's Go gate "
            "(internal/mcp/ready_queue_section_count_test.go) pins it to the "
            "ReadyQueue struct — a dump without it is stale or truncated. Do not "
            "skip this silently; an absent check is not a passing one.",
            file=sys.stderr,
        )
        sys.exit(1)

    # Load baseline.
    try:
        baseline = load_baseline(args.baseline)
    except Exception as e:
        print(f"ERROR: failed to load baseline: {e}", file=sys.stderr)
        sys.exit(1)

    # Collect files. Zero files is a refusal, not a pass (aihub#629): the old
    # sys.exit(0) here sat BEFORE the baseline stale audit, so a run that
    # scanned nothing could never turn red no matter what the baseline held.
    files = collect_md_files(args.targets)
    refusal = empty_scan_error(args.targets, files)
    if refusal:
        print(refusal, file=sys.stderr)
        sys.exit(1)

    all_violations = []
    for fpath in files:
        viols = lint_file(fpath, schema)
        all_violations.extend(viols)

    # Categorize and print.
    has_error = False
    notice_count = 0
    error_count = 0
    used_entry_ids = set()

    for v in sorted(all_violations, key=lambda x: (x.file, x.line)):
        b = match_baseline(v, baseline)
        if b:
            used_entry_ids.add(id(b))
            level = "NOTICE"
            notice_count += 1
        else:
            level = "ERROR"
            error_count += 1
            has_error = True

        wi_ref = f" (baseline: {b['wi']})" if b else ""
        print(f"{v.file}:{v.line} [{v.rule}] {v.message}{wi_ref}")

    # Summary.
    total = len(all_violations)
    print(
        f"\n{total} violation(s): {error_count} error(s), {notice_count} notice(s) "
        f"in {len(files)} file(s)"
    )

    # Stale-entry audit (aihub#619): every baseline entry must have demoted at
    # least one violation this run, or the baseline is only a one-way ratchet —
    # entries get in with a wi reference but never leave, and orphans (aihub#613's
    # seven, aihub#540's one) can only be found by reading.
    if args.baseline:
        stale_errors = audit_stale_entries(
            baseline, used_entry_ids, files, args.baseline
        )
        for line in stale_errors:
            print(line)
        print(
            f"baseline audit: {len(baseline)} entry(ies), "
            f"{len(baseline) - len(stale_errors)} used, {len(stale_errors)} stale"
        )
        if stale_errors:
            has_error = True

    if has_error:
        sys.exit(1)


if __name__ == "__main__":
    main()
